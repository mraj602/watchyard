package picker

// The `lanes` strategy (docs/spec/setup.md 8.5, "Strategy lanes"): protected and aggressive
// accounts, the effective state from the newest readings plus the token meter plus the live
// leases, burst caps, the rr_exp order, batches that fill the accounts one lease at a time,
// and --urgent. RankLanes is pure: callers (and the parity test against the reference
// ranker) build its input by hand.

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LaneParams are the parameters of the lanes strategy (DefaultLaneParams). The lane price, the
// ratio and the meter factor are each account's own when it has calibrated factors
// (LaneAccount; setup.md 8.5, Calibration).
type LaneParams struct {
	Target       float64 // protected weekly target T
	Headstart    float64 // points the pace line allows above the even share of the week
	Handicap     float64 // protected score factor (shown only)
	Cap5h        float64 // protected 5-hour cap ...
	Lane5h       float64 // ... less one lane's budget = the protected 5-hour floor
	ProtMinHours float64 // hours floor of the protected score
	MinHours     float64 // hours floor of the aggressive score
	AggrSkip     float64 // aggressive weekly and 5-hour limit
	H5Weight     float64 // score damping by the 5-hour level
	ExpH         float64 // an aggressive account resetting within this many hours goes first ...
	ExpMax5h     float64 // ... while its 5-hour level is under this
	// Order is the account order: OrderFill (default; setup.md 8.5) or OrderRRExp (the
	// reference: one round robin over all eligible accounts, expiring aggressive ones first while
	// their week resets within ExpH and their 5-hour level is under ExpMax5h).
	// OrderFill groups: 0 aggressive accounts in their last day (LastDayH) with room left, 1
	// aggressive accounts whose room is at risk (ExpSlack), 2 the other aggressive accounts,
	// 3 protected accounts; groups 0 and 1 need the 5-hour level under ExpMax5h; each group takes
	// turns (newest lease oldest first). ExpH is not used.
	Order string
	// ExpSlack (fill_exp): room at risk when the hours to the weekly reset are at most ExpSlack
	// times the hours needed to use the weekly room (to AggrSkip) at full 5-hour speed (one
	// 5-hour window = AggrSkip/ratio weekly points, the account's own ratio).
	ExpSlack float64
	// LastDayH (fill_exp): an aggressive account whose week resets this soon, with room left,
	// goes before every other account.
	LastDayH    float64
	BurstAggr   int // new leases per aggressive account per BurstWindow
	BurstProt   int // new leases per protected account per BurstWindow
	BurstWindow time.Duration
	LeasePrice  float64 // first-5h lane price, 5-hour points
	LaneFull5   float64 // expected full lane cost (0 = LeasePrice)
	CostMult    float64 // safety factor on both
	Ratio       float64 // 5-hour points per weekly point
	F5          float64 // 5-hour points per weighted token unit
	LeaseTTL    time.Duration
	Settle      time.Duration
	StaleGuard  time.Duration
	StaleLease  time.Duration
	Unlaunched  time.Duration
}

// DefaultLaneParams are the shipped values (setup.md 8.5, Parameters).
var DefaultLaneParams = LaneParams{
	Target: 85, Headstart: 10, Handicap: 0.85, Cap5h: 80, Lane5h: 35, ProtMinHours: 24, MinHours: 0.5,
	AggrSkip: 98, H5Weight: 1, ExpH: 48, ExpMax5h: 90, Order: OrderFill, ExpSlack: 3, LastDayH: 24, BurstAggr: 6, BurstProt: 2, BurstWindow: time.Hour,
	LeasePrice: 4, LaneFull5: 0, CostMult: 2, Ratio: 7.2, F5: 1.4e-6,
	LeaseTTL: 5 * time.Hour, Settle: 10 * time.Minute, StaleGuard: time.Hour, StaleLease: 6 * time.Hour,
	Unlaunched: 10 * time.Minute,
}

// Account orders (LaneParams.Order).
const (
	OrderFill  = "fill_exp"
	OrderRRExp = "rr_exp"
)

// ReferenceLaneParams are the parameters of the reference ranker (the golden cases): the
// rr_exp order with the expiring turn only under 30% of the 5-hour window.
var ReferenceLaneParams = func() LaneParams {
	p := DefaultLaneParams
	p.ExpMax5h, p.Order = 30, OrderRRExp
	return p
}()

// Price is the charge of a new lane: c5 for its first 5 hours and cf, its expected full cost,
// in 5-hour points.
func (p LaneParams) Price() (c5, cf float64) {
	cm := p.CostMult
	if cm <= 0 {
		cm = 1
	}
	c5 = p.LeasePrice * cm
	cf = c5
	if p.LaneFull5 > 0 {
		cf = p.LaneFull5 * cm
	}
	return c5, math.Max(cf, c5)
}

// account is p with the account's calibrated factors in place of the meter factor, the ratio
// and the lane charge (a zero factor keeps the parameter).
func (p LaneParams) account(acc LaneAccount) LaneParams {
	if acc.F5 > 0 {
		p.F5 = acc.F5
	}
	if acc.Ratio > 0 {
		p.Ratio = acc.Ratio
	}
	if acc.Charge > 0 {
		p.LeasePrice, p.CostMult, p.LaneFull5 = acc.Charge, 1, 0
	}
	return p
}

// needHours is how long acc needs to use its weekly room up to AggrSkip at full 5-hour speed:
// each 5-hour window gives at most AggrSkip/ratio weekly points (fill_exp, setup.md 8.5).
func (p LaneParams) needHours(acc LaneAccount, w float64) float64 {
	room := math.Max(0, p.AggrSkip-w)
	if room == 0 || p.AggrSkip <= 0 {
		return 0
	}
	return room * p.account(acc).ratio() / p.AggrSkip * 5
}

func (p LaneParams) ratio() float64 {
	if p.Ratio > 0 {
		return p.Ratio
	}
	return 7.2
}

// LaneSample is the reading an account is judged on. Weekly or FiveHour nil: no reading.
// A percentage outside 0-100 or not finite makes the level unknown. A zero TS means there is
// no reading time.
type LaneSample struct {
	TS            time.Time
	Weekly        *float64
	FiveHour      *float64
	WeeklyReset   time.Time // zero: unknown (a week away)
	FiveHourReset time.Time // zero: unknown
}

// LaneMeter is the token meter of an account since its reading, in weighted token units.
type LaneMeter struct {
	M5, M7 float64
	// KeepH: the reading's 5-hour window is still open (else its 5-hour level no longer counts).
	KeepH bool
}

// LaneAccount is one account (seat) to rank.
type LaneAccount struct {
	Key       string
	Ord       int // tie-break, lowest first (label order)
	Protected bool
	Sample    LaneSample
	Meter     LaneMeter
	// Extra5 is the unexplained use (claude.ai) since the reading, 5-hour points.
	Extra5 float64
	// WebStale: a protected account whose claude.ai readings stopped with no rate known.
	WebStale bool
	// Live: a session of the account is running (it ran since the reading).
	Live bool
	// Limit7d and Limit5h replace the weekly limit (aggressive) or target (protected) and the
	// 5-hour limit (aggressive) or floor (protected); 0 keeps the parameter.
	Limit7d, Limit5h float64
	// F5 (5-hour points per unit), Ratio and Charge (the lane charge c = cf, 5-hour points)
	// are the account's calibrated factors; 0 keeps the parameter.
	F5, Ratio, Charge float64
	// K5 and K7 are the meter margin factors: g5 = K5 × f × m5, g7 = K7 × f × m7 / ρ.
	K5, K7 float64
}

// LaneLease is one lease, local or another machine's.
type LaneLease struct {
	Key      string
	T0       time.Time
	SID      string
	C5, CF   *float64 // nil: the current price
	Launched bool
	Ended    bool    // its session ended
	Burned   float64 // weighted units its session used since T0
	Seen     bool    // the meter saw its session
	// Remote: another machine's lease. Settle: nothing measures its session (no hub burn and
	// no local session), so a reading at least Settle after T0 ends it: that reading holds
	// the lane's use so far (setup.md 8.5, effective state 3).
	Remote, Settle bool
	// Shown only (--json, --explain): where Burned came from ("local", "hub", "none") and
	// whether the lease came from the kept copy of the last hub read.
	From string
	Copy bool
}

// Lease states in LeaseCharge.
const (
	LeaseActive  = "active"
	LeaseSettled = "settled"
	LeaseEnded   = "ended"
	LeaseExpired = "expired"
)

// LeaseCharge is what one lease adds to its account (LaneRow.LeaseCharges): b, the 5-hour
// charge and the reserve while active, and the weekly charge kept after it, in points.
type LeaseCharge struct {
	T0         time.Time
	SID        string
	Remote     bool
	Copy       bool
	Launched   bool
	State      string
	From       string
	Burned     float64 // units
	B          float64 // f × burned
	Charge5    float64
	Reserve    float64
	WeeklyKept float64
}

// LaneInput is one call of the lanes ranker.
type LaneInput struct {
	Now      time.Time
	Params   LaneParams
	Accounts []LaneAccount
	Leases   []LaneLease
	N        int
	SIDs     []string // session ids per pick (may be shorter than N, or empty)
	Urgent   bool
}

// LaneRow is one account in the ranking of the first pick.
type LaneRow struct {
	Key          string
	Protected    bool
	Score        float64
	Skip         bool
	Reason       string
	Weekly       float64 // effective weekly level
	FiveHour     float64 // effective 5-hour level
	Reserve      float64 // weekly level with the running lanes' expected cost
	HoursToReset float64
	Leases       int // active leases
	Burst        int // leases in the burst window
	BurstCap     int
	Capped       bool
	Added5       float64 // 5-hour points added since the reading (meter, margin, leases, unexplained)
	Meter5       float64 // the meter's 5-hour points
	Meter7       float64 // the meter's weekly points
	Margin5      float64 // the meter margin g5, 5-hour points
	Margin7      float64 // the meter margin g7, weekly points
	WeeklyCap    float64 // the weekly level at which it takes no new session now (caps)
	FiveHourCap  float64 // the 5-hour level at which it takes no new session
	SampleAgeMin float64
	SampleAt     time.Time // zero: no reading time
	FiveReset    time.Time
	LastLease    time.Time
	Stale        bool
	Unknown      []string
	SkipLevel    int // 0 burst cap only, 1 stale, 2 a protected rule, 3 a hard limit
	Last         bool
	Expiring     bool
	Forced       bool
	ETA          *time.Time
	// LeaseCharges are the account's leases younger than the lease life and older ones that
	// keep a weekly charge, in lease order.
	LeaseCharges []LeaseCharge
}

// LanePick is one placed pick.
type LanePick struct {
	Key    string
	SID    string
	Forced bool
	C5, CF float64
}

// LaneResult is what RankLanes decided.
type LaneResult struct {
	Picks    []LanePick // placed, in order (fewer than N when the rest must wait)
	Waiting  int
	Table    []LaneRow // the ranking for the first pick
	NextFree *time.Time
}

// ErrDuplicateSession: a session id is already leased or asked for twice (exit 3).
var ErrDuplicateSession = errors.New("session id already leased")

// DuplicateSessionError names the session id.
type DuplicateSessionError struct{ SID string }

func (e DuplicateSessionError) Error() string   { return "session id " + e.SID + " is already leased" }
func (e DuplicateSessionError) Is(t error) bool { return t == ErrDuplicateSession }

var tsMin = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

const weekH = 168.0

type cleanSample struct {
	ts         time.Time
	noTS       bool
	unknown    []string
	weekly     *float64
	fiveHour   *float64
	weeklyRst  time.Time
	fiveRst    time.Time
	weeklyKnow bool
}

// validPct is a finite percentage in [0, 100].
func validPct(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 100 }

func cleanLaneSample(s LaneSample, now time.Time) cleanSample {
	c := cleanSample{weeklyRst: s.WeeklyReset, fiveRst: s.FiveHourReset}
	ts := s.TS
	if !ts.IsZero() && (ts.Before(tsMin) || ts.After(now.Add(24*time.Hour))) {
		ts = time.Time{}
	}
	if ts.IsZero() {
		c.noTS = true
		c.unknown = append(c.unknown, "ts missing")
		ts = now
	}
	if ts.After(now) {
		ts = now
	}
	c.ts = ts
	switch {
	case s.Weekly == nil:
		c.unknown = append(c.unknown, "weekly missing")
	case !validPct(*s.Weekly):
		c.unknown = append(c.unknown, cut("weekly invalid "+pyFloat(*s.Weekly), 60))
	default:
		w := *s.Weekly
		c.weekly = &w
		c.weeklyKnow = true
	}
	if s.FiveHour != nil {
		if !validPct(*s.FiveHour) {
			c.unknown = append(c.unknown, cut("5h invalid "+pyFloat(*s.FiveHour), 60))
		} else {
			h := *s.FiveHour
			c.fiveHour = &h
		}
	}
	return c
}

// effective: (weekly, five_hour, hours_to_reset) at now, reset-aware.
func (c cleanSample) effective(now time.Time) (w, h, hrs float64) {
	if c.weekly != nil {
		w = *c.weekly
	}
	wr := c.weeklyRst
	if wr.IsZero() {
		wr = now.Add(time.Duration(weekH * float64(time.Hour)))
	}
	if !wr.After(now) {
		w = 0
		for !wr.After(now) {
			wr = wr.Add(7 * 24 * time.Hour)
		}
	}
	if c.fiveHour != nil {
		h = *c.fiveHour
		if now.Sub(c.ts) > 5*time.Hour || (!c.fiveRst.IsZero() && !now.Before(c.fiveRst)) {
			h = 0
		}
	}
	return w, h, wr.Sub(now).Hours()
}

type laneState struct {
	w, h, wRes, hrs float64
	stale           bool
	unknown         []string
	leaseN, capN    int
	add5, m5, m7    float64
	g5, g7          float64
	ageMin          float64
	ts              time.Time
	noTS            bool
	lastLease       time.Time
	hasLease        bool
	fiveRst         time.Time
	charges         []LeaseCharge
}

func laneLive(acc LaneAccount, c cleanSample, now time.Time, p LaneParams, leases []LaneLease) laneState {
	w, h, hrs := c.effective(now)
	p = p.account(acc)
	ratio := p.ratio()
	f5 := p.F5
	price5, pricef := p.Price()
	m5 := math.Max(0, acc.Meter.M5)
	m7 := math.Max(0, acc.Meter.M7)
	if !acc.Meter.KeepH {
		h = 0
	}
	m5part := m5 * f5
	g5 := math.Max(0, acc.K5) * m5part
	add5 := m5part + g5 + math.Max(0, acc.Extra5)
	add7 := m7 * f5 / ratio
	g7 := math.Max(0, acc.K7) * add7
	st := laneState{hrs: hrs, unknown: c.unknown, ts: c.ts, noTS: c.noTS, fiveRst: c.fiveRst,
		m5: m5part, m7: add7, g5: g5, g7: g7}
	reserve, keepw := 0.0, 0.0
	leaseSince := false
	for _, L := range leases {
		if L.Key != acc.Key {
			continue
		}
		age := now.Sub(L.T0)
		if !st.hasLease || L.T0.After(st.lastLease) {
			st.lastLease, st.hasLease = L.T0, true
		}
		if age < p.BurstWindow {
			st.capN++
		}
		if L.T0.After(c.ts) {
			leaseSince = true
		}
		b := math.Max(0, L.Burned) * f5
		if !L.Seen {
			b = 0
		}
		c5 := price5
		if L.C5 != nil {
			c5 = math.Max(0, *L.C5)
		}
		cf := math.Max(c5, pricef)
		if L.CF != nil {
			cf = math.Max(0, *L.CF)
		}
		lc := LeaseCharge{T0: L.T0, SID: L.SID, Remote: L.Remote, Copy: L.Copy, Launched: L.Launched, From: L.From,
			Burned: math.Max(0, L.Burned), B: b}
		if lc.From == "" {
			lc.From = "none"
		}
		settled := L.Settle && !c.noTS && !c.ts.Before(L.T0.Add(p.Settle))
		switch {
		case L.Ended:
			lc.State = LeaseEnded
		case age >= p.LeaseTTL:
			lc.State = LeaseExpired
		case settled:
			lc.State = LeaseSettled
		default:
			lc.State = LeaseActive
		}
		if lc.State == LeaseActive {
			lc.Charge5, lc.Reserve = math.Max(0, c5-b), math.Max(0, cf-b)
			add5 += lc.Charge5
			reserve += lc.Reserve
			st.leaseN++
		} else if !L.Seen {
			// Never seen by the meter: keep its weekly charge until a newer reading or a
			// weekly reset (a settled lease has that reading).
			wr := c.weeklyRst
			resetSince := !wr.IsZero() && L.T0.Before(wr) && !wr.After(now)
			if c.ts.Before(L.T0.Add(p.Settle)) && !resetSince {
				lc.WeeklyKept = math.Max(0, c5-b) / ratio
				keepw += lc.WeeklyKept
			}
		}
		if age < p.LeaseTTL || lc.WeeklyKept > 0 {
			st.charges = append(st.charges, lc)
		}
	}
	h2 := h + add5
	wCore := w + add7 + g7 + keepw
	w2 := wCore + (add5-m5part-g5)/ratio
	wRes := wCore + reserve/ratio
	st.ageMin = now.Sub(c.ts).Minutes()
	ran := acc.Live || leaseSince
	stale := p.StaleGuard > 0 && st.ageMin > p.StaleGuard.Minutes() && ran
	stale6 := p.StaleLease > 0 && st.ageMin > p.StaleLease.Minutes() && leaseSince
	st.stale = stale || stale6 || c.noTS || (acc.WebStale && acc.Protected)
	st.w = math.Min(math.Max(w2, 0), 100)
	st.h = math.Min(math.Max(h2, 0), 100)
	st.wRes = math.Max(wRes, 0)
	st.add5 = add5
	return st
}

// limits of an account: weekly limit (aggressive) or target (protected), and the 5-hour
// limit (aggressive) or floor (protected).
func (p LaneParams) limits(acc LaneAccount) (lim7, lim5 float64) {
	if acc.Protected {
		lim7, lim5 = p.Target, p.Cap5h-p.Lane5h
	} else {
		lim7, lim5 = p.AggrSkip, p.AggrSkip
	}
	if acc.Limit7d > 0 {
		lim7 = acc.Limit7d
	}
	if acc.Limit5h > 0 {
		lim5 = acc.Limit5h
	}
	return lim7, lim5
}

// caps are the weekly and 5-hour levels at which an account takes no new session when its
// week resets in hrs hours (record.md 4.11 weekly_cap, five_hour_cap): for a protected account
// the pace line min(T, T × (1 − hrs/168) + headstart) and the 5-hour floor, for an aggressive
// one its limits; reserve_pct is already off them (laneLimits).
func (p LaneParams) caps(acc LaneAccount, hrs float64) (cap7, cap5 float64) {
	lim7, lim5 := p.limits(acc)
	if !acc.Protected {
		return lim7, lim5
	}
	frac := 1 - math.Min(hrs, weekH)/weekH
	return math.Min(lim7, lim7*frac+p.Headstart), lim5
}

func (p LaneParams) burstCap(protected bool) int {
	if protected && p.BurstProt > 0 {
		return p.BurstProt
	}
	return p.BurstAggr
}

// laneScore is the reference's score_one on an effective state: (score, skip, reason).
func laneScore(acc LaneAccount, w, h, hrs float64, p LaneParams) (float64, bool, string) {
	lim7, lim5 := p.limits(acc)
	damp := 1 - p.H5Weight*h/100
	if !acc.Protected {
		score := damp * (100 - w) / math.Max(hrs, p.MinHours)
		if w >= lim7 {
			return score, true, fmt.Sprintf("7d %s%% >= %s", f0(w), f0(lim7))
		}
		if h >= lim5 {
			return score, true, fmt.Sprintf("5h %s%% >= %s", f0(h), f0(lim5))
		}
		return score, false, ""
	}
	T := lim7
	minH := p.ProtMinHours
	if minH <= 0 {
		minH = p.MinHours
	}
	score := damp * p.Handicap * (T - w) / math.Max(hrs, minH)
	allowed, _ := p.caps(acc, hrs)
	if w >= allowed {
		return score, true, fmt.Sprintf("7d %s%% >= pace %s", f0(w), f0(allowed))
	}
	if h >= lim5 {
		return score, true, fmt.Sprintf("5h %s%% >= %s", f0(h), f0(lim5))
	}
	return score, false, ""
}

// laneETA is when a skipped account should become eligible (nil: it needs a new reading).
func laneETA(acc LaneAccount, v laneState, p LaneParams, now time.Time, leases []LaneLease) *time.Time {
	if len(v.unknown) > 0 || v.stale {
		return nil
	}
	t := now
	if cp := p.burstCap(acc.Protected); cp > 0 {
		var t0s []time.Time
		for _, L := range leases {
			if L.Key == acc.Key && now.Sub(L.T0) < p.BurstWindow {
				t0s = append(t0s, L.T0)
			}
		}
		sort.Slice(t0s, func(i, j int) bool { return t0s[i].Before(t0s[j]) })
		if len(t0s) >= cp {
			t = later(t, t0s[len(t0s)-cp].Add(p.BurstWindow))
		}
	}
	wreset := now.Add(hoursDur(v.hrs))
	lim7, lim5 := p.limits(acc)
	if v.h >= lim5 {
		hr := v.fiveRst
		if !hr.IsZero() && hr.After(now) {
			t = later(t, hr)
		} else {
			t = later(t, now.Add(5*time.Hour))
		}
	}
	if acc.Protected {
		T := lim7
		if v.wRes >= T || v.w >= T {
			t = later(t, wreset)
		} else {
			need := v.w - p.Headstart // the pace allows w once T*frac + headstart > w
			if need >= T*(1-math.Min(v.hrs, weekH)/weekH) {
				frac := need / T
				t = later(t, wreset.Add(-hoursDur(weekH*(1-frac))))
			}
		}
	} else if v.w >= lim7 {
		t = later(t, wreset)
	}
	return &t
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func hoursDur(h float64) time.Duration { return time.Duration(h * float64(time.Hour)) }

// laneKey is the rr_exp order key: lower first.
type laneKey struct {
	group int     // 0 expiring aggressive, 1 the rest (fill_exp: 1 other aggressive, 2 protected)
	last  float64 // newest lease, unix seconds; -inf when never leased
	hrs   float64
	ord   int
	n     int // fill_exp: leases in the burst window; fewer first when the newest leases tie (a batch)
}

func (a laneKey) less(b laneKey) bool {
	if a.group != b.group {
		return a.group < b.group
	}
	if a.last != b.last {
		return a.last < b.last
	}
	if a.n != b.n {
		return a.n < b.n
	}
	if a.hrs != b.hrs {
		return a.hrs < b.hrs
	}
	return a.ord < b.ord
}

// RankLanes ranks the accounts for in.N new lanes (setup.md 8.5). It never fails on bad
// readings; it returns ErrDuplicateSession (a DuplicateSessionError) when a requested session
// id is already leased or repeated, before deciding anything.
func RankLanes(in LaneInput) (LaneResult, error) {
	p := in.Params
	now := in.Now
	var res LaneResult
	if in.N <= 0 {
		return res, nil
	}
	// Leases: unlaunched ones older than the unlaunched time are orphans of a launch that
	// never happened.
	leases := make([]LaneLease, 0, len(in.Leases)+in.N)
	for _, L := range in.Leases {
		if L.Key == "" || L.T0.IsZero() {
			continue
		}
		if !L.Launched && p.Unlaunched > 0 && now.Sub(L.T0) > p.Unlaunched {
			continue
		}
		leases = append(leases, L)
	}
	have := map[string]bool{}
	for _, L := range leases {
		if L.SID != "" {
			have[L.SID] = true
		}
	}
	seen := map[string]bool{}
	for _, s := range in.SIDs {
		if s != "" && (have[s] || seen[s]) {
			return res, DuplicateSessionError{SID: s}
		}
		seen[s] = true
	}
	if len(in.Accounts) == 0 {
		res.Waiting = in.N
		return res, nil
	}
	clean := make([]cleanSample, len(in.Accounts))
	for i, a := range in.Accounts {
		clean[i] = cleanLaneSample(a.Sample, now)
	}
	for k := 0; k < in.N; k++ {
		table := make([]LaneRow, 0, len(in.Accounts))
		keys := make([]laneKey, 0, len(in.Accounts))
		for i, acc := range in.Accounts {
			v := laneLive(acc, clean[i], now, p, leases)
			prot := acc.Protected
			sc, skip, why := laneScore(acc, v.w, v.h, v.hrs, p)
			lim7, _ := p.limits(acc)
			cap7, cap5 := p.caps(acc, v.hrs)
			level := 0
			if skip {
				level = 2
				if !prot || v.h >= 98 || v.w >= 98 {
					level = 3
				}
			}
			last := false
			if len(v.unknown) > 0 {
				if prot {
					skip, why, level = true, "unknown: "+strings.Join(v.unknown, "; "), 2
				} else {
					last, why = true, "unknown (ranked last): "+strings.Join(v.unknown, "; ")
				}
			}
			if !skip && prot && v.wRes >= lim7 {
				skip, why, level = true, fmt.Sprintf("7d %s%% + running-lane reserve %s%% >= %s", f0(v.w), f0(v.wRes), f0(lim7)), 2
			}
			if !skip && v.stale && prot {
				why = fmt.Sprintf("stale sample %s min, 5h unknown", f0(pyRound(v.ageMin, 1)))
				if acc.WebStale && v.ageMin <= p.StaleGuard.Minutes() {
					why = fmt.Sprintf("no claude.ai reading for %s min and no rate of unseen use", f0(pyRound(v.ageMin, 1)))
				}
				skip, level = true, 1
			}
			cp := p.burstCap(prot)
			capped := cp > 0 && v.capN >= cp
			if !skip && capped {
				skip, why, level = true, fmt.Sprintf("%d new lanes in %s min >= cap %d", v.capN, f0(p.BurstWindow.Minutes()), cp), 0
			}
			ll := math.Inf(-1)
			if v.hasLease {
				ll = float64(v.lastLease.UnixNano()) / 1e9
			}
			key := laneKey{group: 1, last: ll, hrs: 0, ord: acc.Ord}
			var expiring bool
			if p.Order != OrderRRExp {
				// fill_exp groups (LaneParams.Order); ties within a group by the time left per hour
				// needed (most at risk first).
				need := p.needHours(acc, v.w)
				key.group, key.n = 2, v.capN
				if need > 0 {
					key.hrs = v.hrs / need
				}
				switch {
				case prot:
					key.group = 3
				case need > 0 && v.h < p.ExpMax5h && v.hrs <= p.LastDayH:
					key.group, expiring = 0, true
				case need > 0 && v.h < p.ExpMax5h && v.hrs <= p.ExpSlack*need:
					key.group, expiring = 1, true
				}
			} else {
				expiring = !prot && v.hrs <= p.ExpH && v.h < p.ExpMax5h
				if expiring {
					key = laneKey{group: 0, last: ll, hrs: v.hrs, ord: acc.Ord}
				}
			}
			row := LaneRow{Key: acc.Key, Protected: prot, Score: sc, Skip: skip, Reason: why, Weekly: v.w, FiveHour: v.h,
				Reserve: v.wRes, HoursToReset: v.hrs, Leases: v.leaseN, Burst: v.capN, BurstCap: cp, Capped: capped,
				Added5: v.add5, Meter5: v.m5, Meter7: v.m7, Margin5: v.g5, Margin7: v.g7, WeeklyCap: cap7, FiveHourCap: cap5, SampleAgeMin: v.ageMin, FiveReset: v.fiveRst,
				Stale: v.stale, Unknown: v.unknown, SkipLevel: level, Last: last, Expiring: expiring && !skip,
				LeaseCharges: v.charges}
			if !v.noTS {
				row.SampleAt = v.ts
			}
			if v.hasLease {
				row.LastLease = v.lastLease
			}
			if skip {
				row.ETA = laneETA(acc, v, p, now, leases)
			}
			table = append(table, row)
			keys = append(keys, key)
		}
		idx := make([]int, len(table))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			ra, rb := table[idx[a]], table[idx[b]]
			if ra.Skip != rb.Skip {
				return !ra.Skip
			}
			if ra.Last != rb.Last {
				return !ra.Last
			}
			return keys[idx[a]].less(keys[idx[b]])
		})
		sorted := make([]LaneRow, len(table))
		for i, j := range idx {
			sorted[i] = table[j]
		}
		table = sorted
		pick := -1
		if !table[0].Skip {
			pick = 0
		} else if in.Urgent {
			for i, r := range table {
				if r.Protected || r.SkipLevel != 0 || !r.Capped || r.Last {
					continue
				}
				if pick < 0 || lessRounded(r, table[pick]) {
					pick = i
				}
			}
			if pick >= 0 {
				table[pick].Forced = true
			}
		}
		if k == 0 {
			res.Table = table
			for _, r := range table {
				if r.ETA != nil && (res.NextFree == nil || r.ETA.Before(*res.NextFree)) {
					t := *r.ETA
					res.NextFree = &t
				}
			}
		}
		if pick < 0 {
			res.Waiting = in.N - k
			break
		}
		sid := ""
		if k < len(in.SIDs) {
			sid = in.SIDs[k]
		}
		r := table[pick]
		c5, cf := p.Price()
		for _, a := range in.Accounts {
			if a.Key == r.Key {
				c5, cf = p.account(a).Price()
				break
			}
		}
		res.Picks = append(res.Picks, LanePick{Key: r.Key, SID: sid, Forced: r.Forced, C5: pyRound(c5, 4), CF: pyRound(cf, 4)})
		lc5, lcf := pyRound(c5, 4), pyRound(cf, 4)
		leases = append(leases, LaneLease{Key: r.Key, T0: now, SID: sid, C5: &lc5, CF: &lcf})
	}
	return res, nil
}

// lessRounded orders --urgent candidates: lowest 5-hour level, then weekly, as shown
// (rounded to one decimal); the first in table order wins a tie.
func lessRounded(a, b LaneRow) bool {
	ah, bh := pyRound(a.FiveHour, 1), pyRound(b.FiveHour, 1)
	if ah != bh {
		return ah < bh
	}
	return pyRound(a.Weekly, 1) < pyRound(b.Weekly, 1)
}

// f0 formats like Python's "{:.0f}" (round half to even on the exact value).
func f0(x float64) string { return strconv.FormatFloat(x, 'f', 0, 64) }

// pyRound rounds like Python's round(x, n): correctly, half to even.
func pyRound(x float64, n int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	v, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	if err != nil {
		return x
	}
	return v
}

// pyFloat writes a float like Python's repr: "150.0", "-5.0", "100.5".
func pyFloat(x float64) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
