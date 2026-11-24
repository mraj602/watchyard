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

	"github.com/mraj602/watchyard/internal/calib"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/leases"
)

// Lane data rules (setup.md 8.5).
const (
	resetTolerance = 5 * time.Minute // store.ResetTolerance: one window's resets_at may wobble this much
	staleDrop      = 1.0             // points a reading may fall under an earlier one of its window (rounding)
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
	WeeklyPct float64 `json:"weekly_pct"`
	// WeeklyMeasuredPct is the weekly level without lease charges: the level Smart Account
	// Switch judges the account on (the same reading, meter and margins).
	WeeklyMeasuredPct float64    `json:"weekly_measured_pct"`
	FiveHourPct       float64    `json:"five_hour_pct"`
	WeeklyReservePct  float64    `json:"weekly_reserve_pct"`
	HoursToReset      float64    `json:"hours_to_reset"`
	ReadingAt         *time.Time `json:"reading_at"`
	ReadingAgeMin     *float64   `json:"reading_age_min"`
	FiveHourResetsAt  *time.Time `json:"five_hour_resets_at"`
	Meter5hPct        float64    `json:"meter_5h_pct"`
	Meter7dPct        float64    `json:"meter_7d_pct"`
	Unexplained5hPct  float64    `json:"unexplained_5h_pct"`
	Leases            int        `json:"leases"`
	Burst             int        `json:"burst"`
	BurstCap          int        `json:"burst_cap"`
	Capped            bool       `json:"capped"`
	LastLease         *time.Time `json:"last_lease"`
	Expiring          bool       `json:"expiring"` // alias of class == drain (drain order), kept for one release
	// Class is the account's class in the drain order (drain, at_risk, aggressive, unknown,
	// protected) and Drain its drain rules (setup.md 8.5, Drain).
	Class       string     `json:"class,omitempty"`
	Drain       *DrainView `json:"drain,omitempty"`
	Stale       bool       `json:"stale"`
	Unknown     []string   `json:"unknown"`
	SkipLevel   int        `json:"skip_level"`
	WeeklyCap   float64    `json:"weekly_cap"`    // the weekly level at which it takes no new session now
	FiveHourCap float64    `json:"five_hour_cap"` // the 5-hour level at which it takes no new session
	// Pins are the account's live pinned sessions on this machine; PinReserve5hPct and
	// PinReserve7dPct the points kept free for them, already off the caps (setup.md 8.6).
	Pins            int     `json:"pins"`
	PinReserve5hPct float64 `json:"pin_reserve_5h_pct"`
	PinReserve7dPct float64 `json:"pin_reserve_7d_pct"`
	// ExpiringRoom: a protected account near its weekly reset whose weekly cap is the
	// aggressive one until then (setup.md 8.6, Expiring room).
	ExpiringRoom float64    `json:"expiring_room_cap_7d,omitempty"`
	Calibration  *CalibView `json:"calibration"`
	// LeaseCharges are the account's recent leases and what each adds (setup.md 8.5).
	LeaseCharges []LeaseChargeView `json:"lease_charges"`
}

// DrainView is LaneView.Drain (`--json`).
type DrainView struct {
	On             bool       `json:"on"`
	RoomPct        float64    `json:"room_pct"`
	RoomPts        float64    `json:"room_pts"`
	NeedHours      float64    `json:"need_hours"`
	HorizonHours   float64    `json:"horizon_hours"`
	Capacity       *int       `json:"capacity"`
	CapacityWeekly *int       `json:"capacity_weekly"`
	Capacity5h     *int       `json:"capacity_5h"`
	LimitedBy      string     `json:"limited_by,omitempty"`
	Running        int        `json:"running"`
	ForwardBurn    float64    `json:"forward_burn_pts"`
	ProjectedPct   *float64   `json:"projected_at_reset_pct"`
	UnavoidablePct *float64   `json:"unavoidable_waste_pct"`
	FinishBy       *time.Time `json:"finish_by"`
}

func drainView(d DrainInfo) *DrainView {
	v := &DrainView{On: d.On, RoomPct: pyRound(d.RoomPct, 1), RoomPts: pyRound(d.RoomPts, 1), NeedHours: pyRound(d.NeedH, 2),
		HorizonHours: pyRound(d.HorizonH, 2), Running: d.Running, ForwardBurn: pyRound(d.ForwardBurn, 1)}
	if !d.On {
		return v
	}
	c, cw := d.Capacity, d.CapWeekly
	v.Capacity, v.CapacityWeekly, v.LimitedBy = &c, &cw, d.LimitedBy
	if d.Cap5h >= 0 {
		c5 := d.Cap5h
		v.Capacity5h = &c5
	}
	pr, un := pyRound(d.Projected, 1), pyRound(d.Unavoidable, 1)
	fb := d.FinishBy.UTC()
	v.ProjectedPct, v.UnavoidablePct, v.FinishBy = &pr, &un, &fb
	return v
}

// LeaseChargeView is one item of LaneView.LeaseCharges (`--json`).
type LeaseChargeView struct {
	At            time.Time `json:"at"`
	Machine       string    `json:"machine"` // this | other
	SessionID     string    `json:"session_id,omitempty"`
	Copy          bool      `json:"copy"`
	Launched      bool      `json:"launched"`
	State         string    `json:"state"`
	BurnedFrom    string    `json:"burned_from"`
	BurnedUnits   float64   `json:"burned_units"`
	BurnedPct     float64   `json:"burned_pct"`
	Charge5hPct   float64   `json:"charge_5h_pct"`
	ReservePct    float64   `json:"reserve_pct"`
	WeeklyKeptPct float64   `json:"weekly_kept_pct"`
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
// resets_at seen, less a stale one (more than staleDrop under the reading used before it,
// unless the next one confirms the drop); an idle reading (0 % without resets_at) newer than
// that replaces it; with no resets_at anywhere, the newest.
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
	// Within one window the level never falls (Calibration, Used readings): a reading more
	// than staleDrop under the last used one of its window is held; the next reading of the
	// window under it too confirms the drop (a usage reset) and is used, else the held one is
	// skipped: a session that sent an old level with the current resets_at (live 2026-11-25:
	// claude-7's week read 12% between readings of 26%).
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].At.Before(valid[j].At) })
	var best, held *Sample
	for i := range valid {
		s := &valid[i]
		if !maxReset.IsZero() && (s.ResetsAt.IsZero() || maxReset.Sub(s.ResetsAt) > resetTolerance) {
			continue
		}
		if !maxReset.IsZero() && best != nil && s.Pct < best.Pct-staleDrop {
			if held == nil {
				held = s
				continue
			}
		}
		held = nil
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

// Unexplained rate sources (`--json` lanes.calibration.unexplained_from).
const (
	unexplainedClean = "clean"
	unexplainedNet   = "net"
)

// webUse is the unexplained claude.ai use (setup.md 8.5, effective state 4): the 5-hour
// points to add since the reading at ts, whether the seat has web readings, and how the rate
// was measured ("" when no rate is known).
func webUse(w *Window, us []unitAt, ts time.Time, f5 float64, now time.Time) (extra float64, hasWeb bool, from string) {
	if w == nil {
		return 0, false, ""
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
		return 0, false, ""
	}
	sort.SliceStable(web, func(i, j int) bool { return web[i].At.Before(web[j].At) })
	last := web[len(web)-1]
	rate := 0.0
	if now.Sub(last.At) <= webRateKeep {
		var span []Sample // the web readings of the current window in the last 60 minutes
		for _, s := range web {
			if last.At.Sub(s.At) <= webSpan && sameWindow(s.ResetsAt, last.ResetsAt) {
				span = append(span, s)
			}
		}
		// Clean intervals first: no token row and no status-line reading between two
		// consecutive web readings, so no Claude Code session ran.
		var rise float64
		var clean time.Duration
		for i := 1; i < len(span); i++ {
			a, b := span[i-1], span[i]
			if !b.At.After(a.At) || sumBetween(us, a.At, b.At) != 0 || sessionBetween(w, a.At, b.At) {
				continue
			}
			rise += b.Pct - a.Pct
			clean += b.At.Sub(a.At)
		}
		switch {
		case clean >= webMinSpan:
			rate, from = math.Max(0, rise/clean.Hours()), unexplainedClean
		case len(span) > 0 && last.At.Sub(span[0].At) >= webMinSpan:
			s := span[0]
			net := last.Pct - s.Pct - f5*sumBetween(us, s.At, last.At)
			rate, from = math.Max(0, net/last.At.Sub(s.At).Hours()), unexplainedNet
		}
	}
	if age := now.Sub(ts); age > webGap && from != "" {
		extra = rate * math.Min(age.Hours(), window5h.Hours())
	}
	return extra, true, from
}

// sessionBetween reports a reading of the window from a Claude Code session (any source but
// claude.ai) strictly between two times.
func sessionBetween(w *Window, a, b time.Time) bool {
	for _, s := range w.Samples {
		if s.Source != "web" && s.At.After(a) && s.At.Before(b) {
			return true
		}
	}
	return false
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
			MatchedBy: c.MatchedBy, LoggedIn: c.LoggedIn, Mode: pol.Mode, ModeFrom: pol.ModeFrom, Settings: pol.Key, Live: c.LiveSessions,
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
	leased := map[string]bool{}
	for _, l := range in.Leases {
		if l.SessionID != "" {
			keyOf[l.SessionID] = l.Key
			leased[l.SessionID] = true
		}
	}
	perSession := map[string][]unitAt{}
	for _, t := range in.Outbox.Tokens {
		perSession[t.Session] = append(perSession[t.Session], unitAt{t.At, t.Units})
	}

	// Each leased session's use comes from one source, the larger (setup.md 8.5, effective
	// state 3): this machine's token rows of it, or the hub's burn. The meter counts the
	// session from that same source, so its tokens count once.
	bs := newBurnSources(perSession, in.Outbox.Sessions)

	// Leases: this machine's (with the hub's measure of them, by lease id), then other
	// machines' from the hub or from the kept copy.
	own := map[string]HubLease{}
	for _, g := range groups {
		for _, i := range g.idxs {
			for _, hl := range cands[i].OwnLeases {
				own[hl.ID] = hl
			}
		}
	}
	var ls []LaneLease
	for _, l := range in.Leases {
		ll := LaneLease{Key: l.Key, T0: l.At, SID: l.SessionID, C5: l.C5, CF: l.CF,
			Launched: !l.Launched.IsZero(), Ended: !l.Ended.IsZero(), From: burnNone}
		var hub *HubLease
		if hl, ok := own[l.ID]; ok && l.ID != "" {
			hub = &hl
			ll.Launched = ll.Launched || hl.Launched
			ll.Ended = ll.Ended || hl.Ended
			ll.Seen = hl.Seen
		}
		if l.SessionID != "" {
			b := bs.measure(l.Key, l.At, l.SessionID, hub)
			ll.Burned, ll.From, ll.Seen = b.units, b.from, ll.Seen || b.seen
			ll.Recent, ll.RecentOK = recentUnits(perSession[l.SessionID], hub, l.At, now, p.BurnWindow), b.measured
			// The session's own hook events or tokens show it started (and maybe ended) even if
			// the ledger missed its line.
			if hs, ok := in.Outbox.Sessions[l.SessionID]; ok && !hs.Last.Before(l.At) {
				ll.Launched = true
				ll.Ended = ll.Ended || hs.Ended
			}
			ll.Launched = ll.Launched || ll.Seen
		}
		ls = append(ls, ll)
	}
	nLocal := len(ls)
	for _, g := range groups {
		rep := cands[g.rep]
		for _, hl := range rep.RemoteLeases {
			c5, cf := hl.C5, hl.CF
			ll := LaneLease{Key: g.key, T0: hl.At, C5: &c5, CF: &cf, Launched: hl.Launched, Ended: hl.Ended, Seen: hl.Seen,
				Remote: true, Copy: rep.RemoteCopy}
			sid := bs.local(hl.SessionHash)
			if sid != "" {
				if !leased[sid] {
					keyOf[sid] = g.key
				}
				if hs, ok := in.Outbox.Sessions[sid]; ok && !hs.Last.Before(hl.At) {
					ll.Launched = true
					ll.Ended = ll.Ended || hs.Ended
				}
			}
			var hub *HubLease
			if hl.Burn != nil {
				h := hl
				hub = &h
			}
			b := bs.measure(g.key, hl.At, sid, hub)
			ll.Burned, ll.From, ll.Seen = b.units, b.from, ll.Seen || b.seen
			ll.Recent, ll.RecentOK = recentUnits(perSession[sid], hub, hl.At, now, p.BurnWindow), b.measured
			ll.Launched = ll.Launched || b.seen
			ll.Settle = !b.measured
			ls = append(ls, ll)
		}
	}

	// The meter: this machine's token rows of each account's sessions, with the sessions
	// counted from the hub's burn instead of their rows of that lease's life.
	perKey := map[string][]unitAt{}
	for _, t := range in.Outbox.Tokens {
		if bs.fromHub(t.Session, t.At) {
			continue
		}
		k := keyOf[t.Session]
		if k == "" {
			k = slotKey[t.Slot]
		}
		if k != "" {
			perKey[k] = append(perKey[k], unitAt{t.At, t.Units})
		}
	}
	for k, us := range bs.hubUnits {
		all := append(perKey[k], us...)
		sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
		perKey[k] = all
	}

	// The calibration of each account (setup.md 8.5, Calibration); the local fit's lane price
	// pools this machine's ledger.
	pool := calib.PoolLanes(now, localLanes(ls[:nLocal], perSession, now))
	views := map[string]*CalibView{}

	accts := make([]LaneAccount, 0, len(groups))
	for ord, g := range groups {
		rep := cands[g.rep]
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
		cf, src, k5, k7 := accountCalib(rep, in.Calibration, in.Outbox.Samples, us, pool, now)
		// A factor still at its fallback keeps the parameter (the same value by default).
		if cf.F.Weight > 0 {
			acc.F5 = cf.F.Value / 1e6
		}
		if cf.Ratio.Weight > 0 {
			acc.Ratio = cf.Ratio.Value
		}
		if cf.LanePrice.Weight > 0 {
			acc.Charge = cf.LaneCharge
		}
		acc.K5, acc.K7 = k5, k7
		f5 := acc.F5
		if f5 <= 0 {
			f5 = p.F5
		}
		extra, hasWeb, from := webUse(windowOf(rep, "five_hour"), us, sample.TS, f5, now)
		acc.Extra5 = extra
		acc.WebStale = acc.Protected && hasWeb && from == "" && now.Sub(sample.TS) > webGap
		cv := newCalibView(src, cf)
		cv.Corrector5h, cv.Corrector7d = pyRound(k5, 4), pyRound(k7, 4)
		if from != "" {
			cv.UnexplainedFrom = &from
		}
		views[g.key] = cv
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
		if in.Mover != nil {
			acc.MoveUnits = math.Max(0, in.Mover.Units[g.key])
		}
		if pr, ok := in.PinReserves[g.key]; ok && pr.Pins > 0 {
			acc.Pins, acc.PinReserve5, acc.PinReserve7 = pr.Pins, math.Max(0, pr.R5), math.Max(0, pr.R7)
		}
		accts = append(accts, acc)
	}

	f5Of := map[string]float64{}
	for _, a := range accts {
		f5Of[a.Key] = p.account(a).F5
	}
	lin := LaneInput{Now: now, Params: p, Mover: in.Mover, Accounts: accts, Leases: ls, N: n, SIDs: in.SIDs, Urgent: in.Urgent,
		Stats: measureLanes(in.Leases, ls[:nLocal], ls[nLocal:], f5Of, res.Model, now), Expect: in.Expect, HubDown: in.HubDown, Expects: in.Expects}
	if in.OnLanes != nil {
		in.OnLanes(lin)
	}
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
			if cv := views[row.Key]; cv != nil {
				c := *cv
				c.Margin5hPct, c.Margin7dPct = pyRound(row.Margin5, 2), pyRound(row.Margin7, 3)
				v.Calibration = &c
			}
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
		if pk.Over || pk.Expect != "" {
			r.Reason = laneNote(r.Reason, pk)
		}
		earlier[pk.Key]++
		res.Picks = append(res.Picks, Pick{Ranked: r, SessionID: pk.SID, Urgent: pk.Forced, LaneKind: r.Kind, C5: pk.C5, CF: pk.CF,
			Drain: pk.Drain, Spill: pk.Spill, FinishBy: pk.FinishBy, Reset: pk.Reset, Expect: pk.Expect, Over: pk.Over})
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
	v := LaneView{WeeklyPct: pyRound(row.Weekly, 1), WeeklyMeasuredPct: pyRound(row.WeeklyM, 1), FiveHourPct: pyRound(row.FiveHour, 1),
		WeeklyReservePct: pyRound(row.Reserve, 1), HoursToReset: pyRound(row.HoursToReset, 2),
		Meter5hPct: pyRound(row.Meter5+row.Margin5, 1), Meter7dPct: pyRound(row.Meter7+row.Margin7, 2), Unexplained5hPct: pyRound(extra, 1),
		Leases: row.Leases, Burst: row.Burst, BurstCap: row.BurstCap, Capped: row.Capped, Expiring: row.Expiring,
		Stale: row.Stale, Unknown: append([]string{}, row.Unknown...), SkipLevel: row.SkipLevel,
		WeeklyCap: pyRound(row.WeeklyCap, 1), FiveHourCap: pyRound(row.FiveHourCap, 1),
		Pins: row.Pins, PinReserve5hPct: pyRound(row.PinReserve5, 1), PinReserve7dPct: pyRound(row.PinReserve7, 1), ExpiringRoom: pyRound(row.ExpiringRoom, 1),
		LeaseCharges: []LeaseChargeView{}, Class: row.Class}
	if row.Class != "" {
		v.Drain = drainView(row.Drain)
	}
	for _, lc := range row.LeaseCharges {
		x := LeaseChargeView{At: lc.T0.UTC(), Machine: "this", Copy: lc.Copy, Launched: lc.Launched, State: lc.State,
			BurnedFrom: lc.From, BurnedUnits: math.Round(lc.Burned), BurnedPct: pyRound(lc.B, 2),
			Charge5hPct: pyRound(lc.Charge5, 2), ReservePct: pyRound(lc.Reserve, 2), WeeklyKeptPct: pyRound(lc.WeeklyKept, 3)}
		if lc.Remote {
			x.Machine = "other"
		} else {
			x.SessionID = lc.SID
		}
		v.LeaseCharges = append(v.LeaseCharges, x)
	}
	sort.SliceStable(v.LeaseCharges, func(i, j int) bool { return v.LeaseCharges[i].At.Before(v.LeaseCharges[j].At) })
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
	if row.Expiring && row.Class == "" {
		head += ", expiring"
	}
	if urgent {
		head += ", urgent (past its burst cap)"
	}
	parts := []string{head}
	if d := row.Drain; row.Class == ClassDrain && d.On {
		// "drain: 9% of week left, resets 09:30, finish by 09:20; 5h 50%; 2 of 5 drain lanes running"
		run := d.Running + earlier
		dr := fmt.Sprintf("drain: %.0f%% of week left, resets %s, finish by %s", math.Max(0, d.Target-d.WM),
			d.Reset.Local().Format("15:04"), d.FinishBy.Local().Format("15:04"))
		parts = append(parts, dr, fmt.Sprintf("5h %.0f%%", d.HM), fmt.Sprintf("%d of %d drain lanes running", run, d.Running+d.Capacity))
		if d.Spill {
			parts = append(parts, "runs past reset")
		}
		return strings.Join(append(parts, laneTail(r, row, sid, now)...), "; ")
	}
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
	return strings.Join(append(parts, laneTail(r, row, sid, now)...), "; ")
}

// laneNote adds the lane's size class and an over-soft-limit note after the reason's head:
// "aggressive, over (no account free under its soft limits); expect long; 7d 40% ...".
func laneNote(reason string, pk LanePick) string {
	head, rest, _ := strings.Cut(reason, "; ")
	if pk.Over {
		head += ", over (no account free under its soft limits)"
	}
	if pk.Expect != "" {
		head += "; expect " + pk.Expect
	}
	if rest == "" {
		return head
	}
	return head + "; " + rest
}

// laneTail is the end of laneReason: the data source and age, and the session id.
func laneTail(r Ranked, row LaneRow, sid string, now time.Time) []string {
	var parts []string
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
	return parts
}

// recentUnits is the units a leased session used in the last window before now: from this
// machine's token rows of it (us) or the hub's burn, the larger.
func recentUnits(us []unitAt, hub *HubLease, t0, now time.Time, window time.Duration) float64 {
	if window <= 0 {
		window = 15 * time.Minute
	}
	from := now.Add(-window)
	if t0.After(from) {
		from = t0
	}
	local := 0.0
	for _, u := range us {
		if !u.at.Before(from) && !u.at.After(now) {
			local += u.units
		}
	}
	h := 0.0
	if hub != nil && hub.Burn != nil {
		for _, m := range *hub.Burn {
			at := t0.Add(time.Duration(unitsOK(m[0]) * float64(time.Second)))
			if !at.Before(from) && !at.After(now) {
				h += unitsOK(m[1])
			}
		}
	}
	return math.Max(local, h)
}

// Lane statistics (setup.md 8.5, Drain): at least this many measured lanes, else fallbacks.
const laneStatsMin = 10

// laneRateQ is the quantile of the measured lane burn rates a new lane is taken to burn at in
// the switch-projection check (LaneStats.Rate); laneRateMinAge the least age of a running lane
// whose rate counts, laneRateMinEnded the least wall time of an ended one.
const (
	laneRateQ        = 0.75
	laneRateMinAge   = 30 * time.Minute
	laneRateMinEnded = 5 * time.Minute
)

// measureLanes measures the lane cost (p75 and mean of the 5-hour points a finished lane used,
// or a lane's first 5 hours) and the median lane length (launched to ended, from this
// machine's ledger) over the leases read (8 days); a model's own lanes when there are enough
// of them. local are the LaneLeases of ledger, in the same order; remote other machines'.
func measureLanes(ledger []leases.Lease, local, remote []LaneLease, f5Of map[string]float64, model string, now time.Time) LaneStats {
	type lane struct {
		cost  float64
		model string
	}
	var costs []lane
	var lens []lane
	var rates []lane // 5-hour points per hour of wall time (the field cost holds the rate)
	add := func(L LaneLease, m string) {
		f5 := f5Of[L.Key]
		if f5 <= 0 || !L.Seen || L.Burned <= 0 || (!L.Ended && now.Sub(L.T0) < window5h) {
			return
		}
		costs = append(costs, lane{L.Burned * f5, m})
	}
	// rate: a lane's burn over its wall time, from its start to its end (ended, at least
	// laneRateMinEnded) or to now (running for laneRateMinAge or more: a young lane's first
	// minutes burn far above its steady rate).
	rate := func(L LaneLease, m string, start, end time.Time) {
		f5 := f5Of[L.Key]
		if f5 <= 0 || !L.Seen || L.Burned <= 0 || start.IsZero() {
			return
		}
		least := laneRateMinEnded
		if end.IsZero() {
			if L.Ended {
				return // ended at a time this machine does not know
			}
			end, least = now, laneRateMinAge
		}
		h := end.Sub(start).Hours()
		if h < least.Hours() {
			return
		}
		rates = append(rates, lane{L.Burned * f5 / h, m})
	}
	for i, L := range local {
		m := ""
		if i < len(ledger) {
			m = ledger[i].Model
			l := ledger[i]
			start := l.At
			if !l.Launched.IsZero() {
				start = l.Launched
			}
			if !l.Ended.IsZero() {
				if d := l.Ended.Sub(start); d > 0 {
					lens = append(lens, lane{d.Hours(), m})
				}
			}
			rate(L, m, start, l.Ended)
		}
		add(L, m)
	}
	for _, L := range remote {
		add(L, "")
		rate(L, "", L.T0, time.Time{})
	}
	pickOf := func(xs []lane) []float64 {
		var mine, all []float64
		for _, x := range xs {
			all = append(all, x.cost)
			if model != "" && x.model == model {
				mine = append(mine, x.cost)
			}
		}
		if len(mine) >= laneStatsMin {
			return mine
		}
		return all
	}
	st := LaneStats{}
	if cs := pickOf(costs); len(cs) >= laneStatsMin {
		sort.Float64s(cs)
		sum := 0.0
		for _, c := range cs {
			sum += c
		}
		st.Cost, st.Mean, st.Tail, st.N = quantile(cs, 0.75), sum/float64(len(cs)), quantile(cs, 0.95), len(cs)
	}
	if ls := pickOf(lens); len(ls) >= laneStatsMin {
		sort.Float64s(ls)
		st.Len, st.NLen = hoursDur(quantile(ls, 0.5)), len(ls)
	}
	if rs := pickOf(rates); len(rs) >= laneStatsMin {
		sort.Float64s(rs)
		st.Rate, st.NRate = quantile(rs, laneRateQ), len(rs)
	}
	if model != "" {
		st.Model = model
	}
	return st
}

// quantile of sorted xs, linear between the nearest ranks.
func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	pos := q * float64(len(xs)-1)
	i := int(math.Floor(pos))
	if i+1 >= len(xs) {
		return xs[len(xs)-1]
	}
	return xs[i] + (pos-float64(i))*(xs[i+1]-xs[i])
}
