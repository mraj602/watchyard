package picker

// Decide runs a strategy over the gathered data. For `lanes` it builds the input of
// RankLanes (setup.md 8.5: readings, the token meter, unexplained claude.ai use, live
// sessions and leases) and maps its decision back to the accounts on this machine.

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/leases"
)

// Lane data rules (setup.md 8.5).
const (
	resetTolerance = 5 * time.Minute // store.ResetTolerance: one window's resets_at may wobble this much
	readingAhead   = 24 * time.Hour  // a reading further ahead than this is dropped
	webGap         = 15 * time.Minute
	webSpan        = 60 * time.Minute
	webMinSpan     = 10 * time.Minute
	webRateKeep    = 5 * time.Hour
	hookLive       = 10 * time.Minute
	window5h       = 5 * time.Hour
	week           = 7 * 24 * time.Hour
)

var readingMin = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// LaneView is Ranked.Lanes: the state the lanes strategy judged an account on (`--json`).
type LaneView struct {
	WeeklyPct        float64    `json:"weekly_pct"`
	FiveHourPct      float64    `json:"five_hour_pct"`
	WeeklyReservePct float64    `json:"weekly_reserve_pct"`
	HoursToReset     float64    `json:"hours_to_reset"`
	ReadingAt        *time.Time `json:"reading_at"`
	ReadingAgeMin    *float64   `json:"reading_age_min"`
	FiveHourResetsAt *time.Time `json:"five_hour_resets_at"`
	Meter5hPct       float64    `json:"meter_5h_pct"`
	Meter7dPct       float64    `json:"meter_7d_pct"`
	Unexplained5hPct float64    `json:"unexplained_5h_pct"`
	Leases           int        `json:"leases"`
	Burst            int        `json:"burst"`
	BurstCap         int        `json:"burst_cap"`
	Capped           bool       `json:"capped"`
	LastLease        *time.Time `json:"last_lease"`
	Expiring         bool       `json:"expiring"`
	Stale            bool       `json:"stale"`
	Unknown          []string   `json:"unknown"`
	SkipLevel        int        `json:"skip_level"`
}

// Decide ranks with the strategy of in (lanes or a projection strategy) and returns the
// picks. Its only error is ErrDuplicateSession (lanes, a session id already leased or given
// twice), with nothing decided.
func Decide(in Input) (Result, error) {
	strategy := in.Strategy
	if strategy == "" {
		strategy = in.Settings.Strategy
	}
	if !config.ValidNextStrategy(strategy) {
		strategy = config.NextStrategyLanes
	}
	p := DefaultLaneParams
	if in.Params != nil {
		p = *in.Params
	}
	if strategy == config.NextStrategyLanes {
		return rankLanes(in, p)
	}
	if in.Pending == nil {
		in.Pending = leases.Pending(in.Leases, in.Now, in.Settings.ReserveTTL)
		in.LastPicked, in.LastKey = leases.Last(in.Leases)
	}
	in.Strategy = strategy
	res := Rank(in)
	if res.Pick != nil {
		c5, cf := p.Price()
		res.Picks = []Pick{{Ranked: *res.Pick, LaneKind: laneKind(res.Pick.policy), C5: c5, CF: cf}}
	} else {
		res.Waiting = 1
	}
	return res, nil
}

func laneKind(p Policy) string {
	if p.Protected() {
		return leases.KindProtected
	}
	return leases.KindAggressive
}

// unitAt is weighted token units at a time.
type unitAt struct {
	at    time.Time
	units float64
}

// sumBetween sums the units with from < at <= to.
func sumBetween(us []unitAt, from, to time.Time) float64 {
	t := 0.0
	for _, u := range us {
		if u.at.After(from) && !u.at.After(to) {
			t += u.units
		}
	}
	return t
}

// windowSince sums the units of the 5-hour window open at now, where windows start at the
// first token after from and last 5 hours each (0 when none is open).
func windowSince(us []unitAt, from, now time.Time) float64 {
	var start time.Time
	sum := 0.0
	for _, u := range us {
		if !u.at.After(from) || u.at.After(now) {
			continue
		}
		if start.IsZero() || !u.at.Before(start.Add(window5h)) {
			start, sum = u.at, 0
		}
		sum += u.units
	}
	if start.IsZero() || !now.Before(start.Add(window5h)) {
		return 0
	}
	return sum
}

// reading is the reading of one window an account is judged on.
type reading struct {
	at    time.Time
	pct   float64
	reset time.Time
	ok    bool
}

// chooseReading picks a window's reading from every reading of it (setup.md 8.5, Readings):
// valid ones only; the newest of those whose resets_at is within the tolerance of the newest
// resets_at seen; an idle reading (0 % without resets_at) newer than that replaces it; with
// no resets_at anywhere, the newest.
func chooseReading(w *Window, now time.Time) reading {
	if w == nil {
		return reading{}
	}
	rs := append([]Sample(nil), w.Samples...)
	if !w.Unknown && !w.UpdatedAt.IsZero() {
		cur := Sample{At: w.UpdatedAt, Pct: w.UsedPct, ResetsAt: w.ResetsAt}
		if w.Idle {
			cur.Pct, cur.ResetsAt = 0, time.Time{}
		}
		rs = append(rs, cur)
	}
	var valid []Sample
	var maxReset time.Time
	for _, s := range rs {
		if !validPct(s.Pct) || s.At.Before(readingMin) || s.At.After(now.Add(readingAhead)) {
			continue
		}
		if s.At.After(now) {
			s.At = now
		}
		valid = append(valid, s)
		if s.ResetsAt.After(maxReset) {
			maxReset = s.ResetsAt
		}
	}
	var best *Sample
	for i := range valid {
		s := &valid[i]
		if !maxReset.IsZero() && (s.ResetsAt.IsZero() || maxReset.Sub(s.ResetsAt) > resetTolerance) {
			continue
		}
		if best == nil || s.At.After(best.At) {
			best = s
		}
	}
	if best == nil {
		return reading{}
	}
	if !maxReset.IsZero() {
		for i := range valid {
			s := &valid[i]
			if s.ResetsAt.IsZero() && s.Pct == 0 && s.At.After(best.At) {
				best = s
			}
		}
	}
	return reading{at: best.At, pct: best.Pct, reset: best.ResetsAt, ok: true}
}

func windowOf(c Candidate, key string) *Window {
	for i := range c.Windows {
		if c.Windows[i].Key == key {
			return &c.Windows[i]
		}
	}
	return nil
}

// meter5 is the units counted against the 5-hour level since the reading, and whether the
// reading's 5-hour level still counts (its window is open).
func meter5(us []unitAt, ts time.Time, five reading, now time.Time) (float64, bool) {
	if !five.ok {
		return windowSince(us, ts, now), false
	}
	end := five.reset
	if end.IsZero() {
		end = five.at.Add(window5h)
	}
	if now.Before(end) {
		return sumBetween(us, ts, now), true
	}
	from := end
	if ts.After(from) {
		from = ts
	}
	return windowSince(us, from, now), false
}

// meter7 is the units counted against the weekly level: since the later of the weekly
// reading and the last weekly reset.
func meter7(us []unitAt, seven reading, now time.Time) float64 {
	if !seven.ok {
		return 0
	}
	since := seven.at
	if r := seven.reset; !r.IsZero() && !r.After(now) {
		for !r.Add(week).After(now) {
			r = r.Add(week)
		}
		if r.After(since) {
			since = r
		}
	}
	return sumBetween(us, since, now)
}

func sameWindow(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() && b.IsZero()
	}
	d := a.Sub(b)
	return d <= resetTolerance && d >= -resetTolerance
}

// webUse is the unexplained claude.ai use (setup.md 8.5, effective state 4): the 5-hour
// points to add since the reading at ts, whether the seat has web readings, and whether a
// rate is known.
func webUse(w *Window, us []unitAt, ts time.Time, f5 float64, now time.Time) (extra float64, hasWeb, hasRate bool) {
	if w == nil {
		return 0, false, false
	}
	var web []Sample
	for _, s := range w.Samples {
		if s.Source != "web" || !validPct(s.Pct) || s.At.Before(readingMin) || s.At.After(now.Add(readingAhead)) {
			continue
		}
		if s.At.After(now) {
			s.At = now
		}
		web = append(web, s)
	}
	if len(web) == 0 {
		return 0, false, false
	}
	sort.SliceStable(web, func(i, j int) bool { return web[i].At.Before(web[j].At) })
	last := web[len(web)-1]
	rate := 0.0
	if now.Sub(last.At) <= webRateKeep {
		for _, s := range web {
			if last.At.Sub(s.At) > webSpan || !sameWindow(s.ResetsAt, last.ResetsAt) {
				continue
			}
			if span := last.At.Sub(s.At); span >= webMinSpan {
				rise := last.Pct - s.Pct - f5*sumBetween(us, s.At, last.At)
				rate, hasRate = math.Max(0, rise/span.Hours()), true
			}
			break // s is the oldest reading of the span
		}
	}
	if age := now.Sub(ts); age > webGap && hasRate {
		extra = rate * math.Min(age.Hours(), window5h.Hours())
	}
	return extra, true, hasRate
}

// laneGroup is the folders on this machine logged in to one account (key).
type laneGroup struct {
	key  string
	rep  int   // index in rs of the folder that represents the account
	idxs []int // every folder of the account that passed the shared rules
}

func rankLanes(in Input, p LaneParams) (Result, error) {
	now := in.Now
	s := in.Settings
	res := Result{Now: now, Strategy: config.NextStrategyLanes, Model: in.Model}
	if res.Model == "" {
		res.Model = s.Model
	}
	n := in.N
	if n <= 0 {
		n = 1
	}

	// The folders, in natural label order, through the rules every strategy applies.
	cands := append([]Candidate(nil), in.Candidates...)
	sort.SliceStable(cands, func(i, j int) bool {
		return naturalLess(cands[i].Label+"\x00"+cands[i].Folder, cands[j].Label+"\x00"+cands[j].Folder)
	})
	rs := make([]Ranked, len(cands))
	var groups []*laneGroup
	byKey := map[string]*laneGroup{}
	slotKey := map[string]string{}
	for i, c := range cands {
		pol, warn := s.PolicyFor(c)
		res.Warnings = append(res.Warnings, warn...)
		r := Ranked{Label: c.Label, Folder: c.Folder, Dir: c.Dir, Default: c.Default, Seat: c.SeatID, Email: c.Email,
			MatchedBy: c.MatchedBy, LoggedIn: c.LoggedIn, Mode: pol.Mode, Settings: pol.Key, Live: c.LiveSessions,
			Source: c.Source, key: c.Key(), policy: pol, cand: c, Windows: []Projection{}, Kind: laneKind(pol)}
		if r.Source == "" {
			r.Source = "none"
		}
		if c.LoggedIn {
			slotKey[c.Folder] = c.Key()
		}
		if gate(&r, now) {
			g := byKey[r.key]
			if g == nil {
				g = &laneGroup{key: r.key, rep: i}
				byKey[r.key] = g
				groups = append(groups, g)
			}
			g.idxs = append(g.idxs, i)
		}
		rs[i] = r
	}

	// Sessions to accounts: a lease's account, else the login of its hook events, else the
	// login of the token row's folder.
	keyOf := map[string]string{}
	for sid, hs := range in.Outbox.Sessions {
		switch {
		case hs.Account != "":
			keyOf[sid] = "acct:" + hs.Account + "/" + hs.Org
		case slotKey[hs.Slot] != "":
			keyOf[sid] = slotKey[hs.Slot]
		}
	}
	for _, l := range in.Leases {
		if l.SessionID != "" {
			keyOf[l.SessionID] = l.Key
		}
	}
	perKey := map[string][]unitAt{}
	perSession := map[string][]unitAt{}
	for _, t := range in.Outbox.Tokens {
		u := unitAt{t.At, t.Units}
		perSession[t.Session] = append(perSession[t.Session], u)
		k := keyOf[t.Session]
		if k == "" {
			k = slotKey[t.Slot]
		}
		if k != "" {
			perKey[k] = append(perKey[k], u)
		}
	}

	// Leases: this machine's, then other machines' from the hub.
	var ls []LaneLease
	for _, l := range in.Leases {
		ll := LaneLease{Key: l.Key, T0: l.At, SID: l.SessionID, C5: l.C5, CF: l.CF,
			Launched: !l.Launched.IsZero(), Ended: !l.Ended.IsZero()}
		if l.SessionID != "" {
			for _, u := range perSession[l.SessionID] {
				if !u.at.Before(l.At) {
					ll.Burned += u.units
					ll.Seen = true
				}
			}
			// The session's own hook events or tokens show it started even if the ledger
			// missed its launched line.
			if hs, ok := in.Outbox.Sessions[l.SessionID]; ok && !hs.Last.Before(l.At) {
				ll.Launched = true
			}
			ll.Launched = ll.Launched || ll.Seen
		}
		ls = append(ls, ll)
	}

	accts := make([]LaneAccount, 0, len(groups))
	for ord, g := range groups {
		rep := cands[g.rep]
		for _, hl := range rep.RemoteLeases {
			c5, cf := hl.C5, hl.CF
			// Other machines' token use is not in this machine's meter: their leases keep
			// their full charge while active (no burned units taken off).
			ls = append(ls, LaneLease{Key: g.key, T0: hl.At, C5: &c5, CF: &cf, Launched: hl.Launched, Ended: hl.Ended, Seen: hl.Seen})
		}
		five := chooseReading(windowOf(rep, "five_hour"), now)
		seven := chooseReading(windowOf(rep, "seven_day"), now)
		sample := LaneSample{}
		if five.ok {
			v := five.pct
			sample.FiveHour, sample.FiveHourReset, sample.TS = &v, five.reset, five.at
		}
		if seven.ok {
			v := seven.pct
			sample.Weekly, sample.WeeklyReset = &v, seven.reset
			if sample.TS.IsZero() {
				sample.TS = seven.at
			}
		}
		us := perKey[g.key]
		m5, keep := meter5(us, sample.TS, five, now)
		acc := LaneAccount{Key: g.key, Ord: ord, Protected: rs[g.rep].policy.Protected(), Sample: sample,
			Meter: LaneMeter{M5: m5, M7: meter7(us, seven, now), KeepH: keep}}
		extra, hasWeb, hasRate := webUse(windowOf(rep, "five_hour"), us, sample.TS, p.F5, now)
		acc.Extra5 = extra
		acc.WebStale = acc.Protected && hasWeb && !hasRate && now.Sub(sample.TS) > webGap
		for _, i := range g.idxs {
			acc.Live = acc.Live || cands[i].LiveSessions > 0
		}
		for sid, hs := range in.Outbox.Sessions {
			if keyOf[sid] == g.key && !hs.Ended && now.Sub(hs.Last) <= hookLive {
				acc.Live = true
			}
		}
		for _, u := range us {
			if !sample.TS.IsZero() && u.at.After(sample.TS) && !u.at.After(now) {
				acc.Live = true
				break
			}
		}
		acc.Limit7d, acc.Limit5h = laneLimits(rs[g.rep].policy, acc.Protected, p)
		accts = append(accts, acc)
	}

	lin := LaneInput{Now: now, Params: p, Accounts: accts, Leases: ls, N: n, SIDs: in.SIDs, Urgent: in.Urgent}
	lr, err := RankLanes(lin)
	if err != nil {
		return res, err
	}
	res.Waiting = lr.Waiting
	if lr.Waiting > 0 {
		res.NextFree = lr.NextFree
		if len(lr.Picks) > 0 { // when the rest frees up: rank once more with the picks leased
			more := lin
			more.Leases = append(append([]LaneLease(nil), ls...), pickLeases(lr.Picks, now)...)
			more.N, more.SIDs = 1, nil
			if again, err := RankLanes(more); err == nil {
				res.NextFree = again.NextFree
			}
		}
	}

	rows := map[string]LaneRow{}
	for _, row := range lr.Table {
		rows[row.Key] = row
	}
	extraOf := map[string]float64{}
	for _, a := range accts {
		extraOf[a.Key] = a.Extra5
	}
	placed := make([]bool, len(rs))
	for _, row := range lr.Table {
		g := byKey[row.Key]
		for _, i := range g.idxs {
			r := &rs[i]
			v := laneView(row, extraOf[row.Key], now)
			r.Lanes, r.Pending, r.Stale, r.Score = &v, row.Leases, row.Stale, round2(row.Score)
			if !row.SampleAt.IsZero() {
				t := row.SampleAt
				r.Freshest = &t
			}
			switch {
			case i != g.rep:
				r.Excluded = "same account as " + rs[g.rep].Label + " (" + rs[g.rep].Folder + "), ranked once"
			case row.Skip:
				r.Excluded, r.FreeAt = row.Reason, row.ETA
			default:
				r.Eligible = true
			}
			if r.Eligible {
				r.Reason = laneReason(*r, row, 0, "", false, now)
			} else {
				r.Reason = reason(*r, now)
			}
		}
	}
	for i := range rs {
		if rs[i].Lanes == nil {
			rs[i].Reason = reason(rs[i], now)
		}
	}
	// The accounts in the order of the first pick, then the other folders.
	for _, row := range lr.Table {
		g := byKey[row.Key]
		res.Ranked = append(res.Ranked, rs[g.rep])
		placed[g.rep] = true
	}
	for i := range rs {
		if !placed[i] {
			res.Ranked = append(res.Ranked, rs[i])
		}
	}

	earlier := map[string]int{}
	for _, pk := range lr.Picks {
		g := byKey[pk.Key]
		r := rs[g.rep]
		row := rows[pk.Key]
		r.Reason = laneReason(r, row, earlier[pk.Key], pk.SID, pk.Forced, now)
		earlier[pk.Key]++
		res.Picks = append(res.Picks, Pick{Ranked: r, SessionID: pk.SID, Urgent: pk.Forced, LaneKind: r.Kind, C5: pk.C5, CF: pk.CF})
	}
	if len(res.Picks) > 0 {
		res.Pick = &res.Picks[0].Ranked
	}
	return res, nil
}

func pickLeases(ps []LanePick, now time.Time) []LaneLease {
	out := make([]LaneLease, 0, len(ps))
	for _, pk := range ps {
		c5, cf := pk.C5, pk.CF
		out = append(out, LaneLease{Key: pk.Key, T0: now, SID: pk.SID, C5: &c5, CF: &cf})
	}
	return out
}

// laneLimits are the per-account limits from cap_7d, cap_5h and reserve_pct (0 keeps the
// parameter).
func laneLimits(pol Policy, protected bool, p LaneParams) (lim7, lim5 float64) {
	one := func(set, def float64) float64 {
		if set <= 0 && pol.Reserve <= 0 {
			return 0
		}
		v := def
		if set > 0 {
			v = set
		}
		return math.Max(v-pol.Reserve, 1e-9) // a limit at or under 0 takes no lane
	}
	def7, def5 := p.AggrSkip, p.AggrSkip
	if protected {
		def7, def5 = p.Target, p.Cap5h-p.Lane5h
	}
	return one(pol.SetCap7d, def7), one(pol.SetCap5h, def5)
}

func laneView(row LaneRow, extra float64, now time.Time) LaneView {
	v := LaneView{WeeklyPct: pyRound(row.Weekly, 1), FiveHourPct: pyRound(row.FiveHour, 1),
		WeeklyReservePct: pyRound(row.Reserve, 1), HoursToReset: pyRound(row.HoursToReset, 2),
		Meter5hPct: pyRound(row.Meter5, 1), Meter7dPct: pyRound(row.Meter7, 2), Unexplained5hPct: pyRound(extra, 1),
		Leases: row.Leases, Burst: row.Burst, BurstCap: row.BurstCap, Capped: row.Capped, Expiring: row.Expiring,
		Stale: row.Stale, Unknown: append([]string{}, row.Unknown...), SkipLevel: row.SkipLevel}
	if !row.SampleAt.IsZero() {
		t := row.SampleAt.UTC()
		a := pyRound(now.Sub(row.SampleAt).Minutes(), 1)
		v.ReadingAt, v.ReadingAgeMin = &t, &a
	}
	if !row.FiveReset.IsZero() {
		t := row.FiveReset.UTC()
		v.FiveHourResetsAt = &t
	}
	if !row.LastLease.IsZero() {
		t := row.LastLease.UTC()
		v.LastLease = &t
	}
	return v
}

// laneReason is the text after the folder for a lanes account:
// "aggressive; 7d 23%, resets Wed 10:00; 5h 12%; 2 leases in 60 min".
func laneReason(r Ranked, row LaneRow, earlier int, sid string, urgent bool, now time.Time) string {
	head := r.Kind
	if row.Expiring {
		head += ", expiring"
	}
	if urgent {
		head += ", urgent (past its burst cap)"
	}
	parts := []string{head}
	weekly := fmt.Sprintf("7d %.0f%%", row.Weekly)
	if len(row.Unknown) > 0 {
		weekly = "7d unknown"
	} else if row.HoursToReset > 0 {
		weekly += ", resets " + when(now.Add(hoursDur(row.HoursToReset)), now)
	}
	parts = append(parts, weekly, fmt.Sprintf("5h %.0f%%", row.FiveHour))
	if b := row.Burst + earlier; b > 0 {
		word := "leases"
		if b == 1 {
			word = "lease"
		}
		parts = append(parts, fmt.Sprintf("%d %s in 60 min", b, word))
	}
	switch r.Source {
	case "local":
		parts = append(parts, "local data")
	case "none":
		parts = append(parts, "no usage data")
	}
	if !row.SampleAt.IsZero() && now.Sub(row.SampleAt) > time.Hour && r.Source != "none" {
		parts = append(parts, "data "+age(now.Sub(row.SampleAt))+" old")
	}
	if sid != "" {
		parts = append(parts, "session "+sid)
	}
	return strings.Join(parts, "; ")
}
