package api

import (
	"context"
	"net/http"
	"path"
	"sort"
	"strconv"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/hub/usage"
	"github.com/mraj602/watchyard/internal/scan"
)

// GET /v1/analytics/{report} (docs/spec/analytics.md, api.yaml analytics): the usage
// analytics reports. Scopes come from the route table. A "day" is a
// day of the hub's time zone (hub.tz, else $TZ, else UTC), the same zone the alert rules use.
// Routed from server.go like the other read endpoints.
const (
	analyticsDefaultDays = 30
	analyticsMaxDays     = 366
	analyticsTop         = 50 // rows of the projects and sessions reports
	dayFmt               = "2006-01-02"
)

// anaHead is the front of every report.
type anaHead struct {
	Report string  `json:"report"`
	From   string  `json:"from"` // first day, hub time zone
	To     string  `json:"to"`   // last day, inclusive
	TZ     string  `json:"tz"`
	Seat   *string `json:"seat"` // the filter; null = every seat
}

// anaTokens are token counters with their API-equivalent price.
type anaTokens struct {
	Input           int64   `json:"input"`
	Output          int64   `json:"output"`
	Cache5m         int64   `json:"cache_5m"`
	Cache1h         int64   `json:"cache_1h"`
	CacheRead       int64   `json:"cache_read"`
	Replies         int64   `json:"replies"`
	APIUSD          float64 `json:"api_usd"`
	UnpricedReplies int64   `json:"unpriced_replies"` // replies of models with no known price; not in api_usd
}

// add folds counts of one model in, priced at the model's list rate.
func (t *anaTokens) add(model string, c store.Counts) {
	t.Input += c.Input
	t.Output += c.Output
	t.Cache5m += c.Cache5m
	t.Cache1h += c.Cache1h
	t.CacheRead += c.CacheRead
	t.Replies += c.Replies
	usd, ok := scan.CostOf(model, scan.Tokens{Input: c.Input, Output: c.Output, Cache5m: c.Cache5m, Cache1h: c.Cache1h, CacheRead: c.CacheRead})
	if ok {
		t.APIUSD += usd
	} else {
		t.UnpricedReplies += c.Replies
	}
}

func (t anaTokens) total() int64 { return t.Input + t.Output + t.Cache5m + t.Cache1h + t.CacheRead }

type anaDay struct {
	Day    string `json:"day"`
	Source string `json:"source"` // transcripts, or history for days from before token rows
	anaTokens
	TotalTokens int64 `json:"total_tokens"`
}

type anaSeatRow struct {
	Seat *string `json:"seat"`
	Name string  `json:"name"`
	anaTokens
}

type anaDaily struct {
	anaHead
	Totals anaTokens    `json:"totals"`
	Days   []anaDay     `json:"days"`
	Seats  []anaSeatRow `json:"seats"`
}

type anaModel struct {
	Model  string `json:"model"`
	Label  string `json:"label"`
	Priced bool   `json:"priced"`
	anaTokens
}

type anaProject struct {
	Project  string     `json:"project"`
	Name     string     `json:"name"`
	Sessions int        `json:"sessions"`
	LastTS   store.Time `json:"last_ts"`
	anaTokens
}

type anaSession struct {
	SessionID string     `json:"session_id"`
	Seat      *string    `json:"seat"`
	SeatName  string     `json:"seat_name"`
	Project   string     `json:"project"`
	Models    []string   `json:"models"`
	FirstTS   store.Time `json:"first_ts"`
	LastTS    store.Time `json:"last_ts"`
	anaTokens
}

type anaWindow struct {
	Seat      string     `json:"seat"`
	SeatName  string     `json:"seat_name"`
	Key       string     `json:"key"`
	Label     string     `json:"label"`
	StartedAt store.Time `json:"started_at"`
	ResetsAt  store.Time `json:"resets_at"`
	PeakPct   *float64   `json:"peak_pct"`
	FinalPct  *float64   `json:"final_pct"`
	UnusedPct *float64   `json:"unused_pct"`
	Samples   int64      `json:"samples"`
}

type anaHit struct {
	TS        store.Time `json:"ts"`
	Seat      *string    `json:"seat"`
	SeatName  string     `json:"seat_name"`
	SessionID string     `json:"session_id"`
	Type      *string    `json:"type"`
	Status    *string    `json:"status"`
	ResetsAt  store.Time `json:"resets_at"`
	WaitS     *int64     `json:"wait_s"` // seconds from the hit to the reset
}

// anaRange is the parsed query.
type anaRange struct {
	loc      *time.Location
	tz       string
	from, to time.Time // local midnights; to is the day after the last day
	seat     string
}

// analyticsSlots is how many analytics reports run at once, and analyticsBudget how long one
// may wait for its turn and run (analytics.md 2). A report scans every request row in its
// range; on the micro VM, after the hourly backup has pushed the database out of the page
// cache, one takes up to a minute. The board asks for four at once, and four was the whole
// read pool: auth lookups, /v1/sessions, ingest and a settings PATCH then queued behind them
// (503s, a 47 s PATCH). A report waiting for its turn holds no connection.
const (
	analyticsSlots  = 1
	analyticsBudget = 45 * time.Second
)

func (s *Server) analytics(w http.ResponseWriter, r *http.Request) {
	report := r.PathValue("report")
	switch report {
	case "daily", "models", "projects", "heatmap", "sessions", "limits", "hits":
	default:
		auth.WriteError(w, http.StatusNotFound, "not_found", "unknown report "+strconv.Quote(report))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), analyticsBudget)
	defer cancel()
	select {
	case s.anaSlots <- struct{}{}:
		defer func() { <-s.anaSlots }()
	case <-ctx.Done():
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "the hub is busy with other reports; try again")
		return
	}
	r = r.WithContext(ctx)
	rg, msg := s.analyticsRange(r)
	if msg != "" {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	if rg.seat != "" && rg.seat != store.SeatUnattributed {
		ok, err := s.store.SeatExists(ctx, rg.seat)
		if err != nil {
			auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
			return
		}
		if !ok {
			auth.WriteError(w, http.StatusNotFound, "not_found", "no such seat")
			return
		}
	}
	head := anaHead{Report: report, From: rg.from.Format(dayFmt), To: rg.to.AddDate(0, 0, -1).Format(dayFmt), TZ: rg.tz}
	if rg.seat != "" {
		head.Seat = &rg.seat
	}
	f := store.AnalyticsFilter{FromMS: rg.from.UnixMilli(), ToMS: rg.to.UnixMilli(), Seat: rg.seat}
	var (
		out any
		err error
	)
	switch report {
	case "daily":
		out, err = s.anaDaily(r, head, rg, f)
	case "models":
		out, err = s.anaModels(r, head, f)
	case "projects":
		out, err = s.anaProjects(r, head, f)
	case "heatmap":
		out, err = s.anaHeatmap(r, head, rg, f)
	case "sessions":
		out, err = s.anaSessions(r, head, f)
	case "limits":
		out, err = s.anaLimits(r, head, f)
	case "hits":
		out, err = s.anaHits(r, head, f)
	}
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// analyticsRange reads ?from=&to=&days=&seat=&tz=. from and to are days (YYYY-MM-DD,
// inclusive, in tz: the viewer's zone when the client passes one, else the hub's); to defaults
// to today and from to the day that makes the range `days` (30) long. msg is non-empty for a
// bad query.
func (s *Server) analyticsRange(r *http.Request) (rg anaRange, msg string) {
	q := r.URL.Query()
	rg.tz = s.alertHubTZ(r)
	if v := q.Get("tz"); v != "" {
		if !rules.ValidZone(v) {
			return rg, "tz must be an IANA time zone, such as Asia/Kolkata"
		}
		rg.tz = v
	}
	rg.loc = rules.Location(rg.tz)
	rg.seat = q.Get("seat")
	now := s.now().In(rg.loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, rg.loc)
	day := func(key string, def time.Time) (time.Time, bool) {
		v := q.Get(key)
		if v == "" {
			return def, true
		}
		t, err := time.ParseInLocation(dayFmt, v, rg.loc)
		return t, err == nil
	}
	to, ok := day("to", today)
	if !ok {
		return rg, "to must be a date, YYYY-MM-DD"
	}
	span := analyticsDefaultDays
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > analyticsMaxDays {
			return rg, "days must be 1-" + strconv.Itoa(analyticsMaxDays)
		}
		span = n
	}
	from, ok := day("from", to.AddDate(0, 0, 1-span))
	if !ok {
		return rg, "from must be a date, YYYY-MM-DD"
	}
	if from.After(to) {
		return rg, "from is after to"
	}
	if to.After(from.AddDate(0, 0, analyticsMaxDays-1)) {
		return rg, "the range is at most " + strconv.Itoa(analyticsMaxDays) + " days"
	}
	rg.from, rg.to = from, to.AddDate(0, 0, 1)
	return rg, ""
}

func seatLabel(names map[string]string, id string) string {
	if id == "" {
		return "Unattributed"
	}
	if n, ok := names[id]; ok {
		return n
	}
	return id
}

func (s *Server) anaDaily(r *http.Request, head anaHead, rg anaRange, f store.AnalyticsFilter) (any, error) {
	ctx := r.Context()
	buckets, err := s.store.TokenBuckets(ctx, f)
	if err != nil {
		return nil, err
	}
	names, err := s.store.SeatNames(ctx)
	if err != nil {
		return nil, err
	}
	days := map[string]*anaDay{}
	var order []string
	for d := rg.from; d.Before(rg.to); d = d.AddDate(0, 0, 1) {
		k := d.Format(dayFmt)
		days[k] = &anaDay{Day: k, Source: "transcripts"}
		order = append(order, k)
	}
	out := anaDaily{anaHead: head, Days: []anaDay{}, Seats: []anaSeatRow{}}
	bySeat := map[string]*anaTokens{}
	for _, b := range buckets {
		k := time.UnixMilli(b.StartMS).In(rg.loc).Format(dayFmt)
		if d := days[k]; d != nil {
			d.add(b.Model, b.Counts)
		}
		out.Totals.add(b.Model, b.Counts)
		if bySeat[b.Seat] == nil {
			bySeat[b.Seat] = &anaTokens{}
		}
		bySeat[b.Seat].add(b.Model, b.Counts)
	}
	// Days before the first token row come from Claude Code's own daily totals, when imported.
	first, err := s.store.FirstTokenMS(ctx)
	if err != nil {
		return nil, err
	}
	firstDay := "9999-99-99"
	if first != 0 {
		firstDay = time.UnixMilli(first).In(rg.loc).Format(dayFmt)
	}
	hist, err := s.store.HistoryDays(ctx, head.From, head.To, rg.seat)
	if err != nil {
		return nil, err
	}
	for _, h := range hist {
		if d := days[h.Day]; d != nil && h.Day < firstDay {
			d.Source, d.TotalTokens, d.Replies = "history", h.Tokens, h.Messages
		}
	}
	for _, k := range order {
		d := days[k]
		if d.Source == "transcripts" {
			d.TotalTokens = d.total()
		}
		out.Days = append(out.Days, *d)
	}
	for id, t := range bySeat {
		out.Seats = append(out.Seats, anaSeatRow{Seat: strPtr(id), Name: seatLabel(names, id), anaTokens: *t})
	}
	sort.Slice(out.Seats, func(i, j int) bool {
		a, b := out.Seats[i], out.Seats[j]
		if a.APIUSD != b.APIUSD {
			return a.APIUSD > b.APIUSD
		}
		return a.Name < b.Name
	})
	return out, nil
}

func (s *Server) anaModels(r *http.Request, head anaHead, f store.AnalyticsFilter) (any, error) {
	rows, err := s.store.TokenSessions(r.Context(), f)
	if err != nil {
		return nil, err
	}
	by := map[string]*anaModel{}
	for _, t := range rows {
		m := by[t.Model]
		if m == nil {
			_, priced := scan.RateFor(t.Model)
			m = &anaModel{Model: t.Model, Label: scan.ModelLabel(t.Model), Priced: priced}
			by[t.Model] = m
		}
		m.add(t.Model, t.Counts)
	}
	list := make([]anaModel, 0, len(by))
	for _, m := range by {
		list = append(list, *m)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.APIUSD != b.APIUSD {
			return a.APIUSD > b.APIUSD
		}
		if a.Replies != b.Replies {
			return a.Replies > b.Replies
		}
		return a.Model < b.Model
	})
	return struct {
		anaHead
		Models []anaModel `json:"models"`
	}{head, list}, nil
}

func (s *Server) anaProjects(r *http.Request, head anaHead, f store.AnalyticsFilter) (any, error) {
	rows, err := s.store.TokenSessions(r.Context(), f)
	if err != nil {
		return nil, err
	}
	type acc struct {
		anaProject
		sess map[string]bool
	}
	by := map[string]*acc{}
	for _, t := range rows {
		p := by[t.Project]
		if p == nil {
			name := path.Base(t.Project)
			if t.Project == "" {
				name = "(unknown)"
			}
			p = &acc{anaProject: anaProject{Project: t.Project, Name: name}, sess: map[string]bool{}}
			by[t.Project] = p
		}
		p.add(t.Model, t.Counts)
		p.sess[t.Session] = true
		if store.Time(t.LastMS) > p.LastTS {
			p.LastTS = store.Time(t.LastMS)
		}
	}
	list := make([]anaProject, 0, len(by))
	for _, p := range by {
		p.Sessions = len(p.sess)
		list = append(list, p.anaProject)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].APIUSD != list[j].APIUSD {
			return list[i].APIUSD > list[j].APIUSD
		}
		return list[i].Project < list[j].Project
	})
	if len(list) > analyticsTop {
		list = list[:analyticsTop]
	}
	return struct {
		anaHead
		Projects []anaProject `json:"projects"`
	}{head, list}, nil
}

func (s *Server) anaHeatmap(r *http.Request, head anaHead, rg anaRange, f store.AnalyticsFilter) (any, error) {
	buckets, err := s.store.TokenBuckets(r.Context(), f)
	if err != nil {
		return nil, err
	}
	var usd [7][24]float64
	var replies [7][24]int64
	for _, b := range buckets {
		t := time.UnixMilli(b.StartMS).In(rg.loc)
		var c anaTokens
		c.add(b.Model, b.Counts)
		usd[t.Weekday()][t.Hour()] += c.APIUSD
		replies[t.Weekday()][t.Hour()] += b.Replies
	}
	return struct {
		anaHead
		APIUSD  [7][24]float64 `json:"api_usd"` // [weekday, 0 = Sunday][hour]
		Replies [7][24]int64   `json:"replies"`
	}{head, usd, replies}, nil
}

func (s *Server) anaSessions(r *http.Request, head anaHead, f store.AnalyticsFilter) (any, error) {
	ctx := r.Context()
	rows, err := s.store.TokenSessions(ctx, f)
	if err != nil {
		return nil, err
	}
	names, err := s.store.SeatNames(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string]*anaSession{}
	for _, t := range rows {
		x := by[t.Session]
		if x == nil {
			x = &anaSession{SessionID: t.Session, Seat: strPtr(t.Seat), SeatName: seatLabel(names, t.Seat),
				Project: t.Project, Models: []string{}, FirstTS: store.Time(t.FirstMS)}
			by[t.Session] = x
		}
		x.add(t.Model, t.Counts)
		x.Models = append(x.Models, t.Model)
		if store.Time(t.FirstMS) < x.FirstTS {
			x.FirstTS = store.Time(t.FirstMS)
		}
		if store.Time(t.LastMS) > x.LastTS {
			x.LastTS = store.Time(t.LastMS)
		}
	}
	list := make([]anaSession, 0, len(by))
	for _, x := range by {
		sort.Strings(x.Models)
		list = append(list, *x)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].APIUSD != list[j].APIUSD {
			return list[i].APIUSD > list[j].APIUSD
		}
		return list[i].SessionID < list[j].SessionID
	})
	if len(list) > analyticsTop {
		list = list[:analyticsTop]
	}
	return struct {
		anaHead
		Sessions []anaSession `json:"sessions"`
	}{head, list}, nil
}

func (s *Server) anaLimits(r *http.Request, head anaHead, f store.AnalyticsFilter) (any, error) {
	ctx := r.Context()
	rows, err := s.store.UsageWindowsBetween(ctx, f)
	if err != nil {
		return nil, err
	}
	names, err := s.store.SeatNames(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]anaWindow, 0, len(rows))
	for _, w := range rows {
		list = append(list, anaWindow{Seat: w.Seat, SeatName: seatLabel(names, w.Seat), Key: w.Key, Label: usage.Label(w.Key),
			StartedAt: store.Time(w.StartedAt), ResetsAt: store.Time(w.ResetsAt), PeakPct: w.Peak, FinalPct: w.Final,
			UnusedPct: w.Unused, Samples: w.Samples})
	}
	return struct {
		anaHead
		Windows []anaWindow `json:"windows"`
	}{head, list}, nil
}

// anaHits lists limit hits, one per seat, type and reset time (several sessions of one
// seat hit the same window).
func (s *Server) anaHits(r *http.Request, head anaHead, f store.AnalyticsFilter) (any, error) {
	ctx := r.Context()
	rows, err := s.store.LimitHitsBetween(ctx, f)
	if err != nil {
		return nil, err
	}
	names, err := s.store.SeatNames(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	list := []anaHit{}
	for _, h := range rows {
		if h.ResetsAt != 0 {
			k := h.SeatID + "|" + h.Type + "|" + strconv.FormatInt(h.ResetsAt, 10)
			if seen[k] {
				continue
			}
			seen[k] = true
		}
		x := anaHit{TS: store.Time(h.TS), Seat: strPtr(h.SeatID), SeatName: seatLabel(names, h.SeatID), SessionID: h.SessionID,
			Type: strPtr(h.Type), Status: strPtr(h.Status), ResetsAt: store.Time(h.ResetsAt)}
		if h.ResetsAt != 0 {
			wait := max(0, (h.ResetsAt-h.TS)/1000)
			x.WaitS = &wait
		}
		list = append(list, x)
	}
	return struct {
		anaHead
		Hits []anaHit `json:"hits"`
	}{head, list}, nil
}
