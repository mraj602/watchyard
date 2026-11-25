package picker

// The `lanes` strategy (docs/spec/setup.md 8.5, "Strategy lanes"): protected and aggressive
// accounts, the effective state from the newest readings plus the token meter plus the live
// leases, burst caps, the rr_exp order, batches that fill the accounts one lease at a time,
// and --urgent. RankLanes is pure: callers (and the parity test against the reference
// ranker) build its input by hand.

import (
	"errors"
	"fmt"
	"github.com/mraj602/watchyard/internal/projection"
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
	// Order is the account order: OrderDrain (default; setup.md 8.5, Order and Drain),
	// OrderFill (the 2026-11-23 draft, kept for the replay and the simulator only) or OrderRRExp
	// (the reference: one round robin over all eligible accounts, expiring aggressive ones first
	// while their week resets within ExpH and their 5-hour level is under ExpMax5h).
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

	// Drain (OrderDrain only; setup.md 8.5, Drain).
	DrainMinRoom   float64       // weekly points of room under which an account never drains
	DrainFloorH    float64       // an aggressive account with room drains within this many hours of its reset
	DrainSlack     float64       // ... or within DrainSlack × need
	ProtDrainSlack float64       // a protected account drains only within this × need (owner Q3: 1)
	RiskSlack      float64       // at risk (ordering only): hours to reset <= RiskSlack × need
	Guard          time.Duration // finish_by = weekly reset − Guard; no drain closer to the reset
	DrainMargin    float64       // weekly points kept under the limit (doubled when stale or hub down)
	DrainMargin5h  float64       // 5-hour points kept under the 5-hour limit by a drain's capacity
	LaneCost       float64       // E fallback: a lane's first-5h cost, 5-hour points
	LaneLen        time.Duration // Lmed fallback: the median lane length
	BurnWindow     time.Duration // a running lane's burn rate is measured over this
	FreshSoon      time.Duration // a 5-hour window resetting this soon does not limit a drain
	Backstop5h     float64       // no new drain lane at a measured 5-hour level at or over this
	SpillMax       int           // lanes running past the reset per account
	LandMax        float64       // no aggressive lane whose expected cost alone lands over this
	ShortWait      time.Duration // wait this long for an aggressive account rather than use a protected one
	HubDownFactor  float64       // drain capacities times this with the hub down

	// Owner change 2 (setup.md 8.5, No stalling and Estimates).
	NoStall     bool    // burst caps, the protected gate and drain capacity only order; a lane waits only when every account is at a hard limit or unreadable
	UseEstimate bool    // size lanes by the agent's estimate (--expect); keep long lanes off accounts that reset first
	ProtSoft5h  float64 // > 0 (comparison only): protected before aggressive accounts at or over this 5-hour level
	EstFloor    bool    // with estimates: a lane's cost is never taken under the measured one (mean for capacity, p95 for the hard checks)
	// Class benchmarks (from the worker's sessions, setup.md 8.5) and the wall-time cut points
	// between them; UnitsPerToken turns --expect-tokens into weighted units.
	ExpectShort, ExpectMedium, ExpectLong LaneClass
	ExpectCutShort, ExpectCutLong         time.Duration
	UnitsPerToken                         float64
}

// DefaultLaneParams are the shipped values (setup.md 8.5, Parameters).
var DefaultLaneParams = LaneParams{
	Target: 85, Headstart: 10, Handicap: 0.85, Cap5h: 80, Lane5h: 35, ProtMinHours: 24, MinHours: 0.5,
	AggrSkip: 98, H5Weight: 1, ExpH: 48, ExpMax5h: 90, Order: OrderDrain, ExpSlack: 3, LastDayH: 24, BurstAggr: 6, BurstProt: 2, BurstWindow: time.Hour,
	LeasePrice: 4, LaneFull5: 0, CostMult: 2, Ratio: 7.2, F5: 1.4e-6,
	LeaseTTL: 5 * time.Hour, Settle: 10 * time.Minute, StaleGuard: time.Hour, StaleLease: 6 * time.Hour,
	Unlaunched:   10 * time.Minute,
	DrainMinRoom: 0.5, DrainFloorH: 24, DrainSlack: 2, ProtDrainSlack: 1, RiskSlack: 4, Guard: 10 * time.Minute,
	DrainMargin: 1, DrainMargin5h: 2, LaneCost: 8, LaneLen: 15 * time.Minute, BurnWindow: 15 * time.Minute, FreshSoon: 15 * time.Minute,
	Backstop5h: 95, SpillMax: 2, LandMax: 99, ShortWait: 15 * time.Minute, HubDownFactor: 0.5,
	NoStall: true, UseEstimate: true, EstFloor: true,
	ExpectShort: LaneClass{Dur: 7 * time.Minute, Cost: 1.2}, ExpectMedium: LaneClass{Dur: 48 * time.Minute, Cost: 3.2},
	ExpectLong: LaneClass{Dur: 3 * time.Hour, Cost: 14}, ExpectCutShort: 25 * time.Minute, ExpectCutLong: 2 * time.Hour,
	UnitsPerToken: 0.19,
}

// full98 is the hard weekly and 5-hour limit.
const full98 = 98.0

// Account orders (LaneParams.Order).
const (
	OrderDrain = "drain"
	OrderFill  = "fill_exp"
	OrderRRExp = "rr_exp"
)

// DraftLaneParams are the parameters of the 2026-11-23 draft (fill_exp), kept to compare
// against in the replay and the simulator.
var DraftLaneParams = func() LaneParams {
	p := DefaultLaneParams
	p.Order = OrderFill
	return p
}()

// V3LaneParams are the drain order as first built (strategy S1): lanes wait behind burst caps,
// the protected gate and drain capacity, and no estimates. Kept for the comparison and for the
// scenario tests of the drain arithmetic.
var V3LaneParams = func() LaneParams {
	p := DefaultLaneParams
	p.NoStall, p.UseEstimate = false, false
	return p
}()

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

// needHours is how long acc needs to use its weekly room up to its hard weekly limit at full
// 5-hour speed: each 5-hour window gives at most its hard 5-hour limit/ratio weekly points
// (fill_exp, setup.md 8.5). The limits are AggrSkip, no higher than the switch's caps, less
// the pin reserve (hard).
func (p LaneParams) needHours(acc LaneAccount, w float64) float64 {
	if p.AggrSkip <= 0 {
		return 0
	}
	h7, h5 := p.hard(acc)
	room := math.Max(0, h7-w)
	if room == 0 {
		return 0
	}
	return room * p.account(acc).ratio() / h5 * 5
}

// hard are acc's hard weekly and 5-hour limits: AggrSkip (98), no higher than Smart Account
// Switch's caps while it is on, less the pin reserve. Every account, protected too, takes no
// lane past them.
func (p LaneParams) hard(acc LaneAccount) (h7, h5 float64) {
	full := p.AggrSkip
	if full <= 0 {
		full = full98
	}
	return pinLess(switchMin(full, acc.SwitchCap7), acc.PinReserve7), pinLess(switchMin(full, acc.SwitchCap5), acc.PinReserve5)
}

// switchMin is lim, no higher than the switch's cap c (0: the switch is off).
func switchMin(lim, c float64) float64 {
	if c > 0 && c < lim {
		return c
	}
	return lim
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
	// Pins are its live pinned sessions on this machine and PinReserve5/7 the points of its
	// 5-hour and weekly windows kept free for them (account.pin_reserve_5h/_7d per pin;
	// setup.md 8.6, Pinned sessions): every limit of the account is that much lower for new lanes.
	Pins                     int
	PinReserve5, PinReserve7 float64
	// MoveUnits is its measured burn now, units per hour (MoverView.Units).
	MoveUnits float64
	// SwitchCap5 and SwitchCap7 are Smart Account Switch's caps for it while the switch is on
	// (MoverView, before the pin reserve; 0: off): its limits, the hard ones too, are no higher,
	// so the drain, the at-risk test and the rank aim at the level the switch keeps.
	SwitchCap5, SwitchCap7 float64
	// DrainUntil: the owner drains it until then (Policy.DrainUntil; zero: off). While it
	// is on (until after now, its weekly level under the drain aim) the account is an
	// aggressive drain account whose room is to be used by then (setup.md 8.5, Owner drain).
	DrainUntil time.Time
	// OwnerProtected: its mode is protected; the owner drain makes it aggressive meanwhile.
	OwnerProtected bool
}

// LaneLease is one lease, local or another machine's.
type LaneLease struct {
	Key      string
	T0       time.Time
	SID      string
	C5, CF   *float64 // nil: the current price
	Launched bool
	Ended    bool // its session ended
	// Moved: a move wrote it (a session moved here, not a new lane): never a fresh lane.
	Moved  bool
	Burned float64 // weighted units its session used since T0
	Seen   bool    // the meter saw its session
	// Remote: another machine's lease. Settle: nothing measures its session (no hub burn and
	// no local session), so a reading at least Settle after T0 ends it: that reading holds
	// the lane's use so far (setup.md 8.5, effective state 3).
	Remote, Settle bool
	// Shown only (--json, --explain): where Burned came from ("local", "hub", "none") and
	// whether the lease came from the kept copy of the last hub read.
	From string
	Copy bool
	// Recent is the units its session used in the last BurnWindow (RecentOK: something
	// measures the session), for the drain forward burn.
	Recent   float64
	RecentOK bool
	// Spill: picked as a lane that runs past its account's reset; Expect: --expect. EstRate:
	// the estimated burn (5-hour points per hour) while nothing measures it (0: the new-lane rate).
	Spill   bool
	Expect  time.Duration
	EstRate float64
	// ExpectEnd: when the lane is expected to end as the switch counts it (its lease line's
	// expect_end; zero: unknown, an older line).
	ExpectEnd time.Time
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
	// Drain inputs: the measured lane statistics, --expect, and whether the hub could not be
	// read (other machines' new leases unseen).
	Stats   LaneStats
	Expect  time.Duration
	Expects []LaneExpect // per lane (the last repeats); empty: Expect for every lane
	HubDown bool
	// Mover: Smart Account Switch's caps while it is on (nil: off).
	Mover *MoverView
	cur   LaneExpect // the estimate of the lane being placed (RankLanes)
	// prior are picks of the same batch already made by earlier calls (tests that rank a
	// batch one pick at a time).
	prior []LanePick
}

// LaneRow is one account in the ranking of the first pick.
type LaneRow struct {
	fwd5         bool // held by its running lanes' 5-hour burn (counts for the short wait)
	soft         bool // skipped only by a soft limit: it may take a lane rather than none (NoStall)
	Key          string
	Protected    bool
	Score        float64
	Skip         bool
	Reason       string
	Weekly       float64 // effective weekly level
	WeeklyM      float64 // measured weekly level (no lease charges): Smart Account Switch's (LaneInput.Live)
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
	// Pins and PinReserve5/7: its live pinned sessions and the points kept free for them
	// (already off the caps and limits).
	Pins                     int
	PinReserve5, PinReserve7 float64
	// ExpiringRoom: protected, within the switch's expiring window before its weekly reset:
	// the weekly level it may be drained to then (the aggressive cap less the pin reserve; 0:
	// not expiring; setup.md 8.6, Expiring room).
	ExpiringRoom float64
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
	// Class (OrderDrain): drain, at_risk, aggressive, unknown or protected; Drain its drain rules.
	Class string
	Drain DrainInfo
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
	// Drain picks: finish by FinishBy, the weekly reset at Reset; Spill runs past it.
	Drain    bool
	Spill    bool
	FinishBy time.Time
	Reset    time.Time
	Class    string
	// Over: placed past a soft limit (burst cap, protected gate, drain capacity) because no
	// account had room under it (NoStall). Expect: the size class used ("" the measured median).
	Over   bool
	Expect string
	// SwitchClass and SwitchDur: the lane as the switch-projection check counted it (its
	// --expect class and time; medium without one), kept in its lease line for the switch.
	SwitchClass string
	SwitchDur   time.Duration
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
	wM, hM          float64 // measured: the reading, the meter and its margins, unexplained use; no lease charges
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
	st.hM = math.Min(math.Max(h+m5part+g5+math.Max(0, acc.Extra5), 0), 100)
	st.wM = math.Min(math.Max(w+add7+g7+math.Max(0, acc.Extra5)/ratio, 0), 100)
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
	lim7, lim5 = switchMin(lim7, acc.SwitchCap7), switchMin(lim5, acc.SwitchCap5)
	return pinLess(lim7, acc.PinReserve7), pinLess(lim5, acc.PinReserve5)
}

// pinLess is limit lim less the pin reserve r (a limit at or under 0 takes no lane).
func pinLess(lim, r float64) float64 {
	if r <= 0 {
		return lim
	}
	return math.Max(lim-r, 1e-9)
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
	if in.Mover != nil {
		// The switch's caps bound every limit of the account (LaneAccount.SwitchCap5/7).
		accts := append([]LaneAccount(nil), in.Accounts...)
		for i := range accts {
			_, _, hrs := cleanLaneSample(accts[i].Sample, now).effective(now)
			accts[i].SwitchCap5, accts[i].SwitchCap7 = in.Mover.caps(accts[i].Protected, hrs)
		}
		in.Accounts = accts
	}
	in.Accounts = p.ownerDrainEnds(in.Accounts, in.Mover, now)
	clean := make([]cleanSample, len(in.Accounts))
	for i, a := range in.Accounts {
		clean[i] = cleanLaneSample(a.Sample, now)
	}
	drainOrder := p.Order == OrderDrain
	for k := 0; k < in.N; k++ {
		in.cur = in.expectAt(k)
		table := make([]LaneRow, 0, len(in.Accounts))
		keys := make([]laneKey, 0, len(in.Accounts))
		dkeys := make([]drainKey, 0, len(in.Accounts))
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
					if drainOrder {
						why = "unknown (after known aggressive accounts): " + strings.Join(v.unknown, "; ")
					}
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
			var d DrainInfo
			var fwdFree *time.Time
			soft := false
			class := ""
			dk := drainKey{last: ll, n: v.capN, hrs: v.hrs, ord: acc.Ord}
			switch p.Order {
			case OrderDrain:
				d = p.drainInfo(acc, clean[i], v, in, leases, now)
				need := p.needHours(acc, v.w)
				switch {
				case d.On:
					// Drain rules replace the normal ones (owner Q1: no burst cap; Q3: a protected
					// account's pace and target give way to room that would expire anyway).
					class, expiring, last, capped = ClassDrain, true, false, false
					skip, why, level = false, "", 0
					if d.Capacity <= 0 {
						skip, why, level = true, d.Why, 3
					}
					dk.group, dk.deficit, dk.finish = 0, d.Deficit, float64(d.FinishBy.Unix())
					for _, pk := range append(in.prior, res.Picks...) {
						dk.batched = dk.batched || pk.Key == acc.Key
					}
					if prot {
						dk.group = 1
					}
				case prot:
					class, dk.group = ClassProtected, 5
				case last:
					class, dk.group = ClassUnknown, 4
				case need > 0 && v.hrs <= p.RiskSlack*need:
					class, dk.group, dk.risk = ClassAtRisk, 2, v.hrs/need
				default:
					class, dk.group = ClassAggressive, 3
				}
				// The size of the lane (estimates): its landing cost and its burn until the
				// 5-hour reset; and whether it would run past this account's weekly reset.
				tl, _, lmed, r := p.laneStats(in.Stats)
				tl = p.laneTail(in.Stats)
				lr := r                // the new lane's burn, points per hour
				var ldur time.Duration // its expected time (0: unknown)
				lclass := ""           // its size class when --expect gave one
				if p.UseEstimate {
					est := p.estimate(in.cur, acc, in.Stats)
					if est.given {
						ldur, lclass = hoursDur(est.dur), est.class
					}
					if est.given && (!p.EstFloor || est.cost > tl) {
						tl = est.cost
					}
					lr = est.rate()
					if len(v.unknown) == 0 && v.hrs > 0 && est.dur > v.hrs-p.Guard.Hours() {
						dk.late = true // keep long lanes off accounts that reset first (runway)
					}
				}
				if !d.On && (!prot || p.NoStall) && (!skip || level == 0 || (prot && level == 2)) && len(v.unknown) == 0 && p.LandMax > 0 {
					if landMax := pinLess(p.LandMax, acc.PinReserve7); v.w+tl/p.account(acc).ratio() > landMax {
						skip, why, level = true, fmt.Sprintf("7d %s%%: one costly lane (%s points) would land over %s%%", f0(v.w), f0(tl), f0(landMax)), 3
						capped = false // a hard limit: --urgent may not pass it
					} else if !v.fiveRst.IsZero() && v.fiveRst.After(now) {
						// The 5-hour window: what the running lanes will still burn in it (the drain
						// model's forward burn) plus one more lane must stay under the limit.
						_, lim5 := p.limits(acc)
						if prot {
							_, lim5 = p.hard(acc) // NoStall: past its gate a protected account has the hard limit only
						}
						t5 := v.fiveRst.Sub(now).Hours()
						// A lane nothing measures is taken as a median lane (no Lindy rule: an old
						// lease with no burn data is more likely over than running).
						rs, past := p.runningLanes(acc, clean[i], leases, now, r, lmed, t5, false)
						for j := range rs {
							if !rs[j].measured && !rs[j].est {
								rs[j].rem = math.Max(lmed-rs[j].age, 10.0/60)
							}
						}
						if f := v.hM + past + forward(rs, t5); f+math.Min(tl, lr*t5) > lim5 {
							skip, why, level = true, fmt.Sprintf("5h %s%%: running lanes will burn %s more, one more lane would land over %s%%", f0(v.hM), f0(f-v.hM), f0(lim5)), 3
							capped = false
							// Free when the first running lane is expected to end, at the latest at the reset.
							t := v.fiveRst
							for _, x := range rs {
								if e := now.Add(hoursDur(x.rem)); e.Before(t) {
									t = e
								}
							}
							fwdFree = &t
						}
					}
				}
				// NoStall: limits that only order (the burst cap, the protected gate, drain
				// capacity) leave the account able to take a lane under the hard limits.
				if p.NoStall && skip {
					h7, h5 := p.hard(acc)
					hardOK := v.w < h7 && v.h < h5 &&
						(len(v.unknown) > 0 || v.w+tl/p.account(acc).ratio() <= pinLess(p.LandMax, acc.PinReserve7))
					switch {
					case d.On:
						soft = d.over
					case level == 0 && capped:
						soft = hardOK && (!prot || v.h < p.Backstop5h)
					case prot && level == 2 && len(v.unknown) == 0 && !v.stale:
						soft = hardOK && v.h < p.Backstop5h
					}
				}
				// The switch's projection (setup.md 8.5): with this lane's burn added, the account
				// must stay under the caps at which Smart Account Switch would move a session off.
				if in.Mover != nil && !skip && len(v.unknown) == 0 {
					// The new lane at the measured lane burn (setup.md 8.5, The switch's projection):
					// a size class's cost over its wall time is the average of lanes mostly idle.
					sl := p.switchLane(lr, lclass, ldur, in.Stats)
					if why5 := moverOver(in.Mover, acc, v, sl, p, now, freshLanes(in.Mover, leases, acc, sl.rate, p, now)); why5 != "" {
						skip, why, level, capped, soft = true, why5, 3, false, false
					}
				}
			case OrderFill:
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
			default:
				expiring = !prot && v.hrs <= p.ExpH && v.h < p.ExpMax5h
				if expiring {
					key = laneKey{group: 0, last: ll, hrs: v.hrs, ord: acc.Ord}
				}
			}
			// Stale data (the switch's view, setup.md 8.6): an old sample's level, high or low,
			// is not trusted. High does not block an aggressive account (live: the hub away,
			// "5h 100% >= 95" from a sample 3 h 37 min old while the account was at ~68%), and
			// low does not draw lanes: the account goes after those with fresh data. A protected
			// account keeps its blocks (its room is kept on any doubt).
			if in.Mover != nil && in.Mover.StaleMin > 0 && !v.noTS && len(v.unknown) == 0 && v.ageMin > in.Mover.StaleMin && !d.On {
				note := fmt.Sprintf("data stale (%s min old): its level (5h %s%%, 7d %s%%) neither blocks nor draws a lane; ranked after accounts with fresh data",
					f0(math.Floor(v.ageMin)), f0(v.h), f0(v.w))
				switch {
				case skip && !prot && level >= 2:
					skip, last, why, capped, soft, fwdFree = false, true, note, false, false, nil
				case !skip:
					last, why = true, note
				}
				if last && drainOrder {
					dk.group = max(dk.group, 4)
				}
			}
			row := LaneRow{Key: acc.Key, Protected: prot, Score: sc, Skip: skip, Reason: why, Weekly: v.w, WeeklyM: v.wM, FiveHour: v.h,
				Reserve: v.wRes, HoursToReset: v.hrs, Leases: v.leaseN, Burst: v.capN, BurstCap: cp, Capped: capped,
				Added5: v.add5, Meter5: v.m5, Meter7: v.m7, Margin5: v.g5, Margin7: v.g7, WeeklyCap: cap7, FiveHourCap: cap5, SampleAgeMin: v.ageMin, FiveReset: v.fiveRst,
				Pins: acc.Pins, PinReserve5: acc.PinReserve5, PinReserve7: acc.PinReserve7, ExpiringRoom: expiringCap(in.Mover, acc, prot, v.hrs),
				Stale: v.stale, Unknown: v.unknown, SkipLevel: level, Last: last, Expiring: expiring && !skip,
				LeaseCharges: v.charges, Class: class, Drain: d, fwd5: fwdFree != nil, soft: soft}
			if !v.noTS {
				row.SampleAt = v.ts
			}
			if v.hasLease {
				row.LastLease = v.lastLease
			}
			if skip {
				if d.On {
					row.ETA = d.FreeAt
				} else {
					row.ETA = laneETA(acc, v, p, now, leases)
					if fwdFree != nil && row.ETA != nil && fwdFree.After(*row.ETA) {
						row.ETA = fwdFree
					}
				}
			}
			table = append(table, row)
			keys = append(keys, key)
			dkeys = append(dkeys, dk)
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
			if drainOrder {
				return dkeys[idx[a]].less(dkeys[idx[b]])
			}
			if ra.Last != rb.Last {
				return !ra.Last
			}
			return keys[idx[a]].less(keys[idx[b]])
		})
		sorted := make([]LaneRow, len(table))
		groups := make([]int, len(table))
		for i, j := range idx {
			sorted[i] = table[j]
			groups[i] = dkeys[j].group
		}
		table = sorted
		pick, over := -1, false
		if drainOrder {
			pick, over = drainPick(table, groups, in.Urgent, now, p)
		} else if !table[0].Skip {
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
		lp := LanePick{Key: r.Key, SID: sid, Forced: r.Forced, C5: pyRound(c5, 4), CF: pyRound(cf, 4), Class: r.Class, Over: over}
		if r.Class == ClassDrain {
			lp.Drain, lp.Spill, lp.FinishBy, lp.Reset = true, r.Drain.Spill, r.Drain.FinishBy, r.Drain.Reset
		}
		pl := LaneLease{Key: r.Key, T0: now, SID: sid, Spill: lp.Spill, Expect: in.Expect}
		if p.UseEstimate {
			for _, a := range in.Accounts {
				if a.Key == r.Key {
					est := p.estimate(in.cur, a, in.Stats)
					lp.Expect = est.class
					pl.Expect, pl.EstRate = hoursDur(est.dur), est.rate()
					cls, d := "", time.Duration(0)
					if est.given {
						cls, d = est.class, hoursDur(est.dur)
					}
					sl := p.switchLane(0, cls, d, in.Stats)
					lp.SwitchClass, lp.SwitchDur = sl.class, sl.dur
					pl.ExpectEnd = now.Add(sl.dur)
				}
			}
		}
		res.Picks = append(res.Picks, lp)
		lc5, lcf := pyRound(c5, 4), pyRound(cf, 4)
		pl.C5, pl.CF = &lc5, &lcf
		leases = append(leases, pl)
	}
	return res, nil
}

// drainPick is the pick of the drain order on a sorted table (setup.md 8.5, Order): the first
// eligible account that is not protected (drain accounts, then at risk, aggressive and unknown
// ones); else with --urgent the aggressive account blocked only by its burst cap with the
// lowest 5-hour level; else, when an aggressive account is blocked only by its burst cap or
// frees up within ShortWait from its 5-hour window, nothing (the batch waits); else the first
// eligible protected account. With NoStall the wait goes: an account held only by soft limits
// takes the lane (over: aggressive first, then eligible protected, then protected past its
// gate), and -1 means every account is at a hard limit or unreadable. -1: nothing.
func drainPick(table []LaneRow, groups []int, urgent bool, now time.Time, p LaneParams) (int, bool) {
	// past is an aggressive account at or over the soft 5-hour level (ProtSoft5h, comparison
	// only): protected accounts go before it.
	past := func(r LaneRow) bool {
		return p.ProtSoft5h > 0 && !r.Protected && r.Class != ClassDrain && r.FiveHour >= p.ProtSoft5h
	}
	for i, r := range table {
		if !r.Skip && groups[i] < 5 && !past(r) {
			return i, false
		}
	}
	if p.ProtSoft5h > 0 {
		for i, r := range table {
			if !r.Skip && r.Protected {
				return i, false
			}
		}
		for i, r := range table {
			if !r.Skip && groups[i] < 5 {
				return i, false
			}
		}
	}
	if urgent {
		pick := -1
		for i, r := range table {
			if r.Protected || r.SkipLevel != 0 || !r.Capped || r.Class == ClassDrain {
				continue
			}
			if pick < 0 || lessRounded(r, table[pick]) {
				pick = i
			}
		}
		if pick >= 0 {
			table[pick].Forced = true
			return pick, false
		}
	}
	if p.NoStall {
		// No stalling (owner change 2): an account held only by a soft limit takes the lane:
		// aggressive ones first (table order), then protected ones that are eligible, then
		// protected ones past their gate. A lane waits only when every account is at a hard
		// limit or unreadable.
		for _, late := range []bool{false, true} {
			for i, r := range table {
				if r.Skip && r.soft && !r.Protected && past(r) == late {
					return i, true
				}
			}
			if !late {
				for i, r := range table {
					if !r.Skip && r.Protected {
						return i, false
					}
				}
			}
		}
		for i, r := range table {
			if r.Skip && r.soft && r.Protected {
				return i, true
			}
		}
		return -1, false
	}
	for _, r := range table {
		if r.Protected || !r.Skip {
			continue
		}
		if r.SkipLevel == 0 && r.Capped {
			return -1, false // room behind a burst cap: wait (owner Q1)
		}
		short := r.Drain.waitShort || r.fwd5 || (r.Class != ClassDrain && r.FiveHour >= r.FiveHourCap)
		if short && r.ETA != nil && !r.ETA.After(now.Add(p.ShortWait)) {
			return -1, false
		}
	}
	for i, r := range table {
		if !r.Skip && r.Protected {
			return i, false
		}
	}
	return -1, false
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
func f1(x float64) string { return strconv.FormatFloat(x, 'f', 1, 64) }

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

// caps are the switch's caps for an aggressive or a protected account (before the pin
// reserve) whose week resets in hrs hours: a protected one's weekly cap is Cap7 within
// ExpiringH of its reset (expiring room, setup.md 8.6).
func (m *MoverView) caps(protected bool, hrs float64) (c5, c7 float64) {
	if protected {
		if m.expiring(protected, hrs) {
			return m.Cap5Protected, math.Max(m.Cap7, m.Cap7Protected)
		}
		return m.Cap5Protected, m.Cap7Protected
	}
	return m.Cap5, m.Cap7
}

func (m *MoverView) expiring(protected bool, hrs float64) bool {
	return m != nil && protected && m.ExpiringH > 0 && hrs > 0 && hrs <= m.ExpiringH
}

// moverOver is why Smart Account Switch would move a session off acc once a new lane burning
// lr 5-hour points per hour runs there ("" when it would not): its projection at its measured
// burn plus the lane (projection.Levels, the switch's own maths) over the switch's caps less
// the pin reserve.
func moverOver(m *MoverView, acc LaneAccount, v laneState, sl switchLaneEst, p LaneParams, now time.Time, fresh []freshLane) string {
	lr, expect := sl.rate, sl.dur
	pa := p.account(acc)
	c5, c7 := m.caps(acc.Protected, v.hrs)
	if c5 <= 0 && c7 <= 0 {
		return ""
	}
	h5 := 5.0
	if !v.fiveRst.IsZero() && v.fiveRst.After(now) {
		h5 = v.fiveRst.Sub(now).Hours()
	}
	// The account's own burn to the resets, as the switch projects it; the new lane as the
	// switch's trigger will count it once it runs (mover.ExpectFloor): with an expected
	// duration (--expect) for max(that, ExpectFloorH), at most to each window's horizon; else
	// to the horizons (the 5-hour reset; min(the weekly reset, WeekSpanH)). A short lane cannot
	// burn 5 hours, and a lane of unknown length is not let in on a shorter horizon than the
	// trigger's (live pre7: 2de58a08 placed on claude-3 at 07:36 on a 90-minute horizon, moved
	// off at 07:53, "7d projected 95% (cap 88%)").
	r := acc.MoveUnits*pa.F5 + math.Max(0, lr)
	p5, p7 := projection.Levels(v.hM, v.wM, acc.MoveUnits*pa.F5, pa.ratio(), h5, v.hrs, m.WeekSpanH)
	hl := math.Inf(1)
	if expect > 0 {
		hl = math.Max(expect.Hours(), m.ExpectFloorH)
	}
	l5, l7 := projection.Levels(0, 0, math.Max(0, lr), pa.ratio(), math.Min(hl, h5), math.Min(hl, v.hrs), math.Min(hl, m.WeekSpanH))
	p5, p7 = p5+l5, p7+l7
	for _, f := range fresh { // lanes leased minutes ago, not in the meter yet
		fh := math.Inf(1)
		if f.left > 0 {
			fh = math.Max(f.left.Hours(), m.ExpectFloorH)
		}
		f5, f7 := projection.Levels(0, 0, f.rate, pa.ratio(), math.Min(fh, h5), math.Min(fh, v.hrs), math.Min(fh, m.WeekSpanH))
		p5, p7, r = p5+f5, p7+f7, r+f.rate
	}
	// Under the caps less the destination's margin, as a moved session must be (a placement
	// is a destination too).
	if !projection.Over(p5, p7, c5, c7, acc.PinReserve5, acc.PinReserve7, m.Hysteresis) {
		return ""
	}
	marg := ""
	if m.Hysteresis > 0 {
		marg = fmt.Sprintf(" less the switch's %s-point margin", f0(m.Hysteresis))
	}
	if p5-pinLess(c5, acc.PinReserve5) >= p7-pinLess(c7, acc.PinReserve7) {
		return fmt.Sprintf("5h %s%%: at %s points/h with this lane the switch's projection is %s%%, over its cap %s%%%s (it would move a session off)",
			f0(v.hM), f0(pyRound(r, 1)), f0(p5), f0(pinLess(c5, acc.PinReserve5)), marg)
	}
	return fmt.Sprintf("7d %s%%: at %s points/h with this lane the switch's projection is %s%%, over its cap %s%%%s (it would move a session off)",
		f0(v.wM), f0(pyRound(r, 1)), f0(p7), f0(pinLess(c7, acc.PinReserve7)), marg)
}

// expiringCap is the weekly level a protected account near its reset may be drained to (0: not
// expiring room).
func expiringCap(m *MoverView, acc LaneAccount, prot bool, hrs float64) float64 {
	if !m.expiring(prot, hrs) {
		return 0
	}
	_, c7 := m.caps(prot, hrs)
	return pinLess(c7, acc.PinReserve7)
}

// laneRateLo, laneRateHi: the switch-projection check takes a new lane at the measured lane
// burn (the model's p75, LaneStats.Rate) within [laneRateLo, laneRateHi] × its size class's
// burn (cost over wall time; medium without --expect). A measured burn far outside it is a
// measuring fault, not a lane (live pre8: 57 points/h from move leases against ~9-14).
const (
	laneRateLo = 0.5
	laneRateHi = 3.0
)

// switchLaneEst is a new lane as the switch-projection check counts it: its burn (points per
// hour), its class and expected time (medium without --expect, as the switch counts a lane
// whose lease says so); Clamped: the burn was brought into the band (From: before).
type switchLaneEst struct {
	rate, from float64
	dur        time.Duration
	class      string
	clamped    bool
}

// switchLane is the new lane for the switch-projection check: at the measured lane burn rate
// st.Rate when measured (else lr, its estimate's), within [laneRateLo, laneRateHi] × its
// class's burn; a lane without --expect (class "") is a medium lane, for the medium class's
// time (the switch counts it so too, from its lease line, until it overruns).
func (p LaneParams) switchLane(lr float64, class string, dur time.Duration, st LaneStats) switchLaneEst {
	if st.Rate > 0 {
		lr = st.Rate
	}
	sl := switchLaneEst{rate: lr, from: lr, dur: dur, class: class}
	switch class {
	case ExpectShort, ExpectMedium, ExpectLong:
	default:
		sl.class = ExpectMedium
	}
	b := p.classBench(sl.class, st)
	if class == "" || dur <= 0 {
		b = p.classBench(ExpectMedium, st)
		sl.class, sl.dur = ExpectMedium, b.Dur
	}
	if b.Dur > 0 && b.Cost > 0 {
		c := b.Cost / b.Dur.Hours()
		switch {
		case sl.rate > laneRateHi*c:
			sl.rate, sl.clamped = laneRateHi*c, true
		case sl.rate < laneRateLo*c:
			sl.rate, sl.clamped = laneRateLo*c, true
		}
	}
	return sl
}

// SwitchLaneRate is the burn the switch-projection check counts a new lane without --expect
// at (points per hour): the switch counts its young lanes at it in its destination check.
func (in LaneInput) SwitchLaneRate() float64 {
	p := in.Params
	_, _, _, lr := p.laneStats(in.Stats)
	if p.UseEstimate {
		lr = p.estimate(LaneExpect{}, LaneAccount{}, in.Stats).rate()
	}
	return p.switchLane(lr, "", 0, in.Stats).rate
}

// SwitchLaneNote is the --explain line of the new lane's burn in the switch-projection check,
// for a lane of the given class ("" no --expect): "" when the switch is off.
func SwitchLaneNote(in LaneInput, class string, dur time.Duration) string {
	if in.Mover == nil {
		return ""
	}
	p := in.Params
	_, _, _, lr := p.laneStats(in.Stats)
	if p.UseEstimate {
		x := LaneExpect{Class: class}
		if class == "" {
			x = LaneExpect{}
		}
		lr = p.estimate(x, LaneAccount{}, in.Stats).rate()
	}
	sl := p.switchLane(lr, class, dur, in.Stats)
	s := fmt.Sprintf("Lane burn (switch check): a new %s lane at %s points/h", sl.class, f1(sl.rate))
	lanes := "lanes"
	if in.Stats.Model != "" {
		lanes = in.Stats.Model + " lanes"
	}
	if in.Stats.NRate > 0 {
		s += fmt.Sprintf(" (measured: p75 of %d %s)", in.Stats.NRate, lanes)
	} else {
		s += fmt.Sprintf(" (its estimate: fewer than %d %s measured)", laneStatsMin, lanes)
	}
	if sl.clamped {
		c := sl.rate / laneRateHi
		lim := "at most " + f0(laneRateHi)
		if sl.from < sl.rate {
			c, lim = sl.rate/laneRateLo, "at least "+f1(laneRateLo)
		}
		s += fmt.Sprintf("; clamped from %s to %s× the %s class's %s points/h", f1(sl.from), lim, sl.class, f1(c))
	}
	s += fmt.Sprintf("; for %s min", f0(sl.dur.Minutes()))
	if class == "" {
		s += " (no --expect: a medium lane, as the switch counts it until it overruns)"
	}
	return s
}

// freshLane is a lane leased on an account in the last MoverView.YoungMin minutes: the part of
// its burn the meter does not have yet (points per hour) and its expected time left (0:
// unknown).
type freshLane struct {
	rate float64
	left time.Duration
}

// freshLanes are acc's lanes leased in the last m.YoungMin minutes, this call's earlier picks
// included, each at the new-lane burn rate less what the meter measured of it (live pre9: four
// lanes leased onto claude-2 around 10:00 each saw it nearly idle; 15 minutes later they burned
// 60-90 points/h and the switch moved sessions off).
func freshLanes(m *MoverView, ls []LaneLease, acc LaneAccount, rate float64, p LaneParams, now time.Time) []freshLane {
	if m == nil || m.YoungMin <= 0 || rate <= 0 {
		return nil
	}
	f5, bw := p.account(acc).F5, p.BurnWindow.Hours()
	var out []freshLane
	for _, L := range ls {
		// A moved session is not a new lane: its burn is measured (the switch counts it at the
		// burn it moved with).
		if L.Key != acc.Key || L.Ended || L.Remote || L.Moved || now.Sub(L.T0) >= time.Duration(m.YoungMin*float64(time.Minute)) {
			continue
		}
		measured := 0.0
		if L.RecentOK && bw > 0 {
			measured = math.Max(0, L.Recent) * f5 / bw
		}
		if x := rate - measured; x > 0 {
			f := freshLane{rate: x}
			if !L.ExpectEnd.IsZero() {
				f.left = max(0, L.ExpectEnd.Sub(now))
			}
			out = append(out, f)
		}
	}
	return out
}
