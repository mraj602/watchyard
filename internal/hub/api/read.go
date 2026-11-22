package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/hub/usage"
	"github.com/mraj602/watchyard/internal/scan"
	"github.com/mraj602/watchyard/internal/version"
)

// Window is api.yaml Window.
type Window struct {
	Key       string     `json:"key"`
	Label     *string    `json:"label"`
	UsedPct   *float64   `json:"used_pct"`
	PeakPct   *float64   `json:"peak_pct"`
	ResetsAt  store.Time `json:"resets_at"`
	StartedAt store.Time `json:"started_at"`
	UnusedPct *float64   `json:"unused_pct"`
	Source    string     `json:"source"`
	UpdatedAt store.Time `json:"updated_at"`
	Pace      *PaceJSON  `json:"pace"`
	// State is current (resets_at ahead), reset (resets_at has passed and no newer reading)
	// or idle (claude.ai's newest reading is 0 % with no reset: the window has not started;
	// resets_at is null).
	State string `json:"state"`
}

// Window states (api.yaml Window.state).
const (
	StateCurrent = "current"
	StateReset   = "reset"
	StateIdle    = "idle"
)

// PaceJSON is Window.pace.
type PaceJSON struct {
	ProjectedPct float64    `json:"projected_pct"`
	FullAt       store.Time `json:"full_at"`
	Band         string     `json:"band"`
}

// windowOf renders a window. current is true for the overview and stream, where a window
// that has reset reads 0% used; the history endpoint passes false and keeps the real value.
func windowOf(w store.WindowRow, now time.Time, current bool) Window {
	label := usage.Label(w.Key)
	out := Window{Key: w.Key, Label: &label, UsedPct: w.Final, PeakPct: w.Peak, ResetsAt: store.Time(w.ResetsAt),
		StartedAt: store.Time(w.StartedAt), UnusedPct: w.Unused, UpdatedAt: store.Time(w.UpdatedAt)}
	switch {
	case len(w.Sources) > 1:
		out.Source = "both"
	case len(w.Sources) == 1:
		out.Source = w.Sources[0]
	default:
		out.Source = "web"
	}
	if w.Idle {
		out.State = StateIdle
		return out
	}
	out.State = StateCurrent
	resets := time.UnixMilli(w.ResetsAt)
	if !now.Before(resets) {
		out.State = StateReset
		// The window has reset: nothing is known to be used in the new one yet.
		if current {
			zero := 0.0
			out.UsedPct = &zero
		}
		if out.UnusedPct == nil && w.Final != nil {
			u := math.Max(0, 100-*w.Final)
			out.UnusedPct = &u
		}
		return out
	}
	if w.Final != nil && w.StartedAt != 0 {
		if p := usage.PaceOf(*w.Final, time.UnixMilli(w.StartedAt), resets, now); p != nil {
			out.Pace = &PaceJSON{ProjectedPct: p.ProjectedPct, Band: p.Band}
			if p.FullAt != nil {
				out.Pace.FullAt = store.Time(p.FullAt.UnixMilli())
			}
		}
	}
	return out
}

func windowsOf(rows []store.WindowRow, now time.Time) []Window {
	out := make([]Window, 0, len(rows))
	for _, w := range rows {
		out = append(out, windowOf(w, now, true))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if usage.Rank(out[i].Key) != usage.Rank(out[j].Key) {
			return usage.Rank(out[i].Key) < usage.Rank(out[j].Key)
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Seat is api.yaml Seat.
type Seat struct {
	ID           string            `json:"id"`
	Account      string            `json:"account"`
	Org          string            `json:"org"`
	Email        *string           `json:"email"`
	OrgName      *string           `json:"org_name"`
	Plan         *string           `json:"plan"`
	Label        *string           `json:"label"`
	Colour       *string           `json:"colour"`
	Mode         *string           `json:"mode"`
	Order        int64             `json:"order"`
	Hidden       bool              `json:"hidden"`
	Windows      []Window          `json:"windows"`
	ExtraUsage   json.RawMessage   `json:"extra_usage"`
	Spend        json.RawMessage   `json:"spend"`
	WebRawAt     store.Time        `json:"web_raw_at"`
	Places       []store.Place     `json:"places"`
	SignedOut    []store.SlotPlace `json:"signed_out"`
	LiveSessions int               `json:"live_sessions"`
}

func (s *Server) seats(r *http.Request, includeHidden bool) ([]Seat, error) {
	now := s.now()
	rows, err := s.store.Seats(r.Context(), includeHidden, now)
	if err != nil {
		return nil, err
	}
	out := make([]Seat, 0, len(rows))
	for _, x := range rows {
		ws, err := s.store.Windows(r.Context(), x.ID, nil, time.Time{}, true)
		if err != nil {
			return nil, err
		}
		seat := Seat{ID: x.ID, Account: x.Account, Org: x.Org, Email: x.Email, OrgName: x.OrgName, Plan: x.Plan, Label: x.Label,
			Colour: x.Colour, Mode: x.Mode, Order: x.Order, Hidden: x.Hidden, Windows: windowsOf(ws, now), ExtraUsage: x.ExtraUsage,
			Spend: x.Spend, WebRawAt: store.Time(x.WebRawAt), Places: x.Places, SignedOut: x.SignedOut,
			LiveSessions: x.LiveSessions}
		if seat.ExtraUsage == nil {
			seat.ExtraUsage = json.RawMessage("null")
		}
		if seat.Spend == nil {
			seat.Spend = json.RawMessage("null")
		}
		out = append(out, seat)
	}
	return out, nil
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such item")
		return
	}
	if s.cfg.Log != nil {
		s.cfg.Log.Printf("api: %v", err)
	}
	auth.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
}

func boolParam(r *http.Request, name string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(name))
	return v
}

func listParam(r *http.Request, name string) []string {
	var out []string
	for _, v := range strings.Split(r.URL.Query().Get(name), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func timeParam(r *http.Request, name string) (time.Time, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	return t, err == nil
}

// withFocus fills focus_url for a desktop device on the terminal's machine only.
func withFocus(v store.SessionView, d store.Device) store.SessionView {
	if v.FocusLink != "" && d.Kind == "desktop" && d.MachineID != "" && d.MachineID == v.FocusTarget {
		link := v.FocusLink
		v.FocusURL = &link
	}
	return v
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	dev, _ := auth.DeviceFrom(r.Context())
	stream := s.Broker.LastID()
	seats, err := s.seats(r, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	needs, _, err := s.store.Sessions(r.Context(), store.SessionFilter{States: []string{"needs_you"}, Limit: 500, Days: 365}, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	// A session whose current alert is seen is hidden until its state changes (push.md 4).
	shown := needs[:0]
	for _, n := range needs {
		if n.Alert != nil && string(n.Alert.Status) == "seen" {
			continue
		}
		shown = append(shown, withFocus(n, dev))
	}
	needs = shown
	machines, err := s.store.Machines(r.Context(), nil, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	counts, err := s.store.Counts(r.Context(), now)
	if err != nil {
		s.fail(w, err)
		return
	}
	tz, tzSource := s.hubZone(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"time": store.Time(now.UnixMilli()), "seats": seats, "needs_you": nonNil(needs), "machines": nonNilM(machines),
		"counts": counts, "stream_id": stream, "version": version.Version, "tz": tz, "tz_source": tzSource,
	})
}

func nonNil(v []store.SessionView) []store.SessionView {
	if v == nil {
		return []store.SessionView{}
	}
	return v
}

func nonNilM(v []store.MachineView) []store.MachineView {
	if v == nil {
		return []store.MachineView{}
	}
	return v
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SessionFilter{States: listParam(r, "state"), Seats: listParam(r, "seat"), Machines: listParam(r, "machine"),
		IncludeEnded: boolParam(r, "include_ended")}
	if v, err := strconv.Atoi(q.Get("days")); err == nil {
		f.Days = min(max(v, 1), 365)
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = min(max(v, 1), 500)
	}
	since, ok := timeParam(r, "since")
	if !ok {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "since must be an RFC 3339 time")
		return
	}
	f.Since = since
	if c := q.Get("cursor"); c != "" {
		b, err := base64.RawURLEncoding.DecodeString(c)
		off, err2 := strconv.Atoi(strings.TrimPrefix(string(b), "o:"))
		if err != nil || err2 != nil || off < 0 {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "bad cursor")
			return
		}
		f.Offset = off
	}
	now := s.now()
	list, more, err := s.store.Sessions(r.Context(), f, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	dev, _ := auth.DeviceFrom(r.Context())
	for i := range list {
		list[i] = withFocus(list[i], dev)
	}
	var next *string
	if more {
		c := base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(f.Offset+len(list))))
		next = &c
	}
	// from: the start of the activity window, so the board can say "showing the last 2 days".
	writeJSON(w, http.StatusOK, map[string]any{"sessions": nonNil(list), "next": next,
		"from": store.Time(f.Cut(now).UnixMilli())})
}

// TokenTotals is SessionDetail.tokens.
type TokenTotals struct {
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Replies    int64   `json:"replies"`
	APIUSD     float64 `json:"api_usd"`
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.store.Session(r.Context(), id, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	dev, _ := auth.DeviceFrom(r.Context())
	v = withFocus(v, dev)
	timeline, trans, totals, err := s.store.SessionTimeline(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var t TokenTotals
	for _, x := range totals {
		t.Input += x.Input
		t.Output += x.Output
		t.CacheRead += x.CacheRead
		t.CacheWrite += x.Cache5m + x.Cache1h
		t.Replies += x.Replies
		usd, _ := scan.CostOf(x.Model, scan.Tokens{Input: x.Input, Output: x.Output, Cache5m: x.Cache5m, Cache1h: x.Cache1h, CacheRead: x.CacheRead})
		t.APIUSD += usd
	}
	t.APIUSD = math.Round(t.APIUSD*10000) / 10000
	writeJSON(w, http.StatusOK, struct {
		store.SessionView
		Timeline    []store.TimelineItem   `json:"timeline"`
		Transitions []store.TransitionView `json:"transitions"`
		Tokens      TokenTotals            `json:"tokens"`
	}{v, timeline, trans, t})
}

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	seats, err := s.seats(r, boolParam(r, "include_hidden"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": seats})
}

func (s *Server) windows(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	seat := r.PathValue("seat")
	if _, err := s.store.Seat(r.Context(), seat, now); err != nil {
		s.fail(w, err)
		return
	}
	since, ok := timeParam(r, "since")
	if !ok {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "since must be an RFC 3339 time")
		return
	}
	keys := listParam(r, "key")
	rows, err := s.store.Windows(r.Context(), seat, keys, since, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	ws := make([]Window, 0, len(rows))
	for _, x := range rows { // newest first, as stored
		ws = append(ws, windowOf(x, now, false))
	}
	out := map[string]any{"windows": ws}
	if boolParam(r, "samples") {
		sm, err := s.store.Samples(r.Context(), seat, keys, since, 5000)
		if err != nil {
			s.fail(w, err)
			return
		}
		out["samples"] = sm
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) machines(w http.ResponseWriter, r *http.Request) {
	all := false
	switch v := r.URL.Query().Get("all"); v {
	case "", "false", "0":
	case "true", "1":
		all = true
	default:
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "all must be true or false")
		return
	}
	ms, err := s.store.MachineList(r.Context(), store.MachineQuery{All: all}, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": nonNilM(ms)})
}
