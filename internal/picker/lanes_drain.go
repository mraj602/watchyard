package picker

// The drain order (setup.md 8.5, "Order" and "Drain"): an account whose unused weekly room
// would expire before its weekly reset is drained first, up to the number of new lanes that
// room can absorb (its drain capacity), worked out from measured levels and the forward burn
// of the lanes already running on it, never from the fixed lease charges.

import (
	"fmt"
	"math"
	"time"
)

// Account classes (LaneRow.Class, `--json` lanes.class).
const (
	ClassDrain      = "drain"
	ClassAtRisk     = "at_risk"
	ClassAggressive = "aggressive"
	ClassUnknown    = "unknown"
	ClassProtected  = "protected"
)

// What limits a drain account's capacity (DrainInfo.LimitedBy).
const (
	LimitWeekly  = "weekly"
	Limit5h      = "5h"
	LimitSpill   = "spill"
	LimitStale   = "stale"
	LimitLanding = "landing"
)

// LaneStats are the measured lane statistics (setup.md 8.5, Drain): Cost the p75 of a lane's
// 5-hour-point cost in its first 5 hours, Len the median lane length. Zero keeps the
// parameter's fallback (LaneParams.LaneCost, LaneLen).
type LaneStats struct {
	Cost  float64
	Mean  float64 // the mean lane cost (0: Cost)
	Len   time.Duration
	N     int    // lanes the cost was measured on (0: fallback)
	NLen  int    // lanes the length was measured on
	Model string // the model the stats are for ("" pooled)
}

// DrainInfo is what the drain rules worked out for one account (LaneRow.Drain).
type DrainInfo struct {
	On          bool
	RoomPct     float64 // weekly room to the limit less the margin, weekly points
	RoomPts     float64 // the same in 5-hour points (R5)
	NeedH       float64 // hours to use it at full 5-hour speed
	HorizonH    float64 // drain entry horizon: max(floor, slack × need) (protected: 1 × need)
	Capacity    int     // new lanes the room absorbs now (C)
	CapWeekly   int
	Cap5h       int // -1: not limiting (the 5-hour window resets within FreshSoon or has not started)
	LimitedBy   string
	Running     int     // lanes running on it (active, settled and unlaunched leases)
	ForwardBurn float64 // 5-hour points the running lanes are expected to burn before finish_by
	Projected   float64 // projected weekly level at the reset
	Unavoidable float64 // weekly points that expire whatever happens
	Deficit     int     // lanes that must run without pause to use the room, less those running
	Spill       bool    // a new lane would run past the reset
	FinishBy    time.Time
	Reset       time.Time
	FreeAt      *time.Time
	Why         string // the reason when Capacity <= 0
	WM, HM      float64 // the measured weekly and 5-hour levels it was judged on
	waitShort   bool   // blocked by its 5-hour window (counts for the short wait)
}

// drainKey orders the accounts in the drain order: lower first.
type drainKey struct {
	group   int // 0 aggressive drain, 1 protected drain, 2 at risk, 3 aggressive, 4 unknown, 5 protected
	batched bool // drain: already picked in this batch (every draining account gets one first)
	deficit int
	finish  float64 // finish_by, unix seconds
	risk    float64 // hours to reset per hour of need
	last    float64
	n       int
	hrs     float64
	ord     int
}

func (a drainKey) less(b drainKey) bool {
	if a.group != b.group {
		return a.group < b.group
	}
	switch a.group {
	case 0, 1:
		if a.batched != b.batched {
			return !a.batched
		}
		if a.deficit != b.deficit {
			return a.deficit > b.deficit
		}
		if a.finish != b.finish {
			return a.finish < b.finish
		}
	case 2:
		if a.risk != b.risk {
			return a.risk < b.risk
		}
	}
	if a.last != b.last {
		return a.last < b.last
	}
	if a.n != b.n {
		return a.n < b.n
	}
	if a.group == 3 && a.hrs != b.hrs { // the earliest weekly reset first
		return a.hrs < b.hrs
	}
	return a.ord < b.ord
}

// laneStats are the lane cost E (the p75, 5-hour points), the mean cost em, the length
// (hours) and the new-lane burn rate r = em / length. Many lanes cost about their mean each;
// the capacity adds the spread (E − em) once (setup.md 8.5, Drain).
func (p LaneParams) laneStats(st LaneStats) (e, em, lmed, r float64) {
	e = p.LaneCost
	if st.Cost > 0 {
		e = st.Cost
	}
	if e <= 0 {
		e = 8
	}
	em = e
	if st.Mean > 0 && st.Mean < e {
		em = st.Mean
	}
	l := p.LaneLen
	if st.Len > 0 {
		l = st.Len
	}
	if l <= 0 {
		l = 15 * time.Minute
	}
	lmed = l.Hours()
	return e, em, lmed, em / lmed
}

// running lanes on an account: the leases that may still burn (not ended, younger than the
// lease life; settled and unlaunched ones too).
type runLane struct {
	age, rate, rem float64 // hours, 5-hour points per hour, hours
	spill          bool
	since          bool // picked after the reading
}

//
// past is what the lanes nothing measures (no local session, no hub burn) used since the
// reading, at the new-lane rate: neither the reading nor the meter holds it (an ended one at
// most one lane length of it).
func (p LaneParams) runningLanes(acc LaneAccount, c cleanSample, leases []LaneLease, now time.Time, r, lmed, finishH float64) (out []runLane, past float64) {
	f5 := p.account(acc).F5
	bw := p.BurnWindow
	if bw <= 0 {
		bw = 15 * time.Minute
	}
	for _, L := range leases {
		if L.Key != acc.Key {
			continue
		}
		age := now.Sub(L.T0)
		if age < 0 {
			age = 0
		}
		if age >= p.LeaseTTL {
			continue
		}
		if !L.RecentOK {
			from := L.T0
			if c.ts.After(from) {
				from = c.ts
			}
			if sinceH := now.Sub(from).Hours(); sinceH > 0 {
				if L.Ended {
					sinceH = math.Min(sinceH, lmed)
				}
				past += r * sinceH
			}
		}
		if L.Ended {
			continue
		}
		ah := age.Hours()
		rate := r
		if L.Seen && L.RecentOK && age >= 5*time.Minute {
			win := math.Min(ah, bw.Hours())
			rate = math.Max(0, L.Recent) * f5 / win
		}
		rem := math.Max(math.Max(lmed-ah, ah), 10.0/60)
		if L.Expect > 0 {
			rem = math.Max(rem, L.Expect.Hours()-ah)
		}
		rem = math.Min(rem, p.LeaseTTL.Hours()-ah)
		out = append(out, runLane{age: ah, rate: rate, rem: rem, since: L.T0.After(c.ts),
			spill: L.Spill || rem > finishH})
	}
	return out, past
}

// forward is the 5-hour points the running lanes burn in the next t hours.
func forward(rs []runLane, t float64) float64 {
	if t <= 0 {
		return 0
	}
	s := 0.0
	for _, x := range rs {
		s += x.rate * math.Min(x.rem, t)
	}
	return s
}

// drainInfo works out whether acc is draining and its capacity (setup.md 8.5, Drain).
func (p LaneParams) drainInfo(acc LaneAccount, c cleanSample, v laneState, in LaneInput, leases []LaneLease, now time.Time) DrainInfo {
	var d DrainInfo
	pa := p.account(acc)
	rho := pa.ratio()
	full := p.AggrSkip
	if full <= 0 {
		full = 98
	}
	lim7, lim5 := p.limits(acc)
	if acc.Protected {
		lim7 = full // a protected account in drain may use the room that would expire (owner Q3)
	}
	H := v.hrs
	guard := p.Guard.Hours()
	known := len(v.unknown) == 0 && c.weekly != nil && !c.weeklyRst.IsZero()
	if !known || H <= guard || (acc.Protected && v.stale) {
		return d
	}
	if lim7-v.wM <= p.DrainMinRoom || lim7-v.wM <= 0 {
		return d
	}
	stale := v.stale
	m := p.DrainMargin
	if stale || in.HubDown {
		m *= 2
	}
	R5 := math.Max(0, lim7-m-v.wM) * rho
	A0 := math.Max(0, lim5-v.hM)
	fiveOpen := !v.fiveRst.IsZero() && v.fiveRst.After(now)
	t5 := 5.0
	if fiveOpen {
		t5 = v.fiveRst.Sub(now).Hours()
	}
	// need is at full 5-hour speed (a protected account too: owner Q3, "even at 100%").
	a0full := math.Max(0, full-v.hM)
	var need float64
	switch {
	case R5 <= 0:
		need = 0
	case R5 <= a0full:
		need = 5 * R5 / full
	default:
		need = t5 + 5*(R5-a0full)/full
	}
	slack, floor := p.DrainSlack, p.DrainFloorH
	if acc.Protected {
		slack, floor = p.ProtDrainSlack, 0
	}
	horizon := math.Max(floor, slack*need)
	d.RoomPct, d.RoomPts, d.NeedH, d.HorizonH = math.Max(0, lim7-m-v.wM), R5, need, horizon
	if H > horizon {
		return d
	}
	d.On = true
	d.WM, d.HM = v.wM, v.hM
	d.Reset = now.Add(hoursDur(H))
	d.FinishBy = d.Reset.Add(-p.Guard)
	hRem := H - guard

	// Room usable before the reset at full speed (shown only).
	umax := math.Min(A0, full*math.Min(t5, H)/5)
	for start := t5; start < H; start += 5 {
		umax += full * math.Min(5, H-start) / 5
	}
	d.Unavoidable = math.Max(0, R5-umax) / rho

	e, em, lmed, r := p.laneStats(in.Stats)
	spread := e - em
	rs, past := p.runningLanes(acc, c, leases, now, r, lmed, hRem)
	d.Running = len(rs)
	F := past + forward(rs, hRem)
	d.ForwardBurn = F
	d.Projected = v.wM + F/rho
	n1 := math.Min(em, r*hRem)
	if in.Expect > 0 {
		n1 = math.Min(math.Max(em, r*in.Expect.Hours()), r*hRem)
	}
	n1 = math.Max(n1, 1e-6)
	if hRem > 0 {
		d.Deficit = int(math.Ceil(R5/(r*hRem))) - d.Running
	}

	cw := int(math.Floor((R5 - F - spread) / n1))
	if cw <= 0 && R5-F >= 0.5*n1 && d.Running == 0 {
		cw = 1 // the end game: one last lane uses the tiny room; the margin keeps it under the limit
	}
	if cw < 0 {
		cw = 0
	}
	soon := !fiveOpen || v.fiveRst.Sub(now) <= p.FreshSoon
	c5 := -1
	if !soon {
		t5p := math.Min(t5, H)
		n15 := math.Max(math.Min(em, r*t5p), 1e-6)
		c5 = int(math.Floor((A0 - past - forward(rs, t5p) - spread) / n15))
		if c5 < 0 {
			c5 = 0
		}
		bs := p.Backstop5h
		if acc.Protected {
			bs = math.Min(bs, lim5)
		}
		if v.hM >= bs {
			c5 = 0
		}
	}
	if v.hM >= lim5 { // the hard limit holds even when the window resets soon
		c5 = 0
	}
	if in.HubDown {
		half := func(x int) int {
			if x <= 0 {
				return x
			}
			return max(1, int(math.Floor(float64(x)*p.HubDownFactor)))
		}
		cw, c5 = half(cw), half(c5)
	}
	d.CapWeekly, d.Cap5h = cw, c5
	C, by := cw, LimitWeekly
	if c5 >= 0 && c5 < C {
		C, by = c5, Limit5h
	}
	// Spill lanes: a new lane that would run past the reset.
	d.Spill = hRem < lmed || (in.Expect > 0 && in.Expect.Hours() > hRem)
	if d.Spill && C > 0 {
		sp := 0
		for _, x := range rs {
			if x.spill {
				sp++
			}
		}
		if left := p.SpillMax - sp; left < C {
			C, by = max(0, left), LimitSpill
		}
	}
	if stale && C > 0 {
		since := 0
		for _, x := range rs {
			if x.since {
				since++
			}
		}
		if since > 0 {
			C, by = 0, LimitStale
		} else if C > 1 {
			C, by = 1, LimitStale
		}
	}
	if p.LandMax > 0 && v.wM+e/rho > p.LandMax && C > 0 {
		C, by = 0, LimitLanding
	}
	d.Capacity, d.LimitedBy = C, by
	if C > 0 {
		return d
	}
	fin := d.FinishBy.Local().Format("15:04")
	switch by {
	case Limit5h:
		if fiveOpen {
			t := v.fiveRst.Add(-p.FreshSoon)
			d.FreeAt = &t
			d.waitShort = true
			d.Why = "drain: 5h full until " + v.fiveRst.Local().Format("15:04")
		} else {
			d.Why = "drain: 5h full"
		}
	case LimitStale:
		d.Why = "drain: reading is stale; one drain lane per fresh reading"
	case LimitLanding:
		d.Why = fmt.Sprintf("drain: one lane (%s points) would land over %s%%", f0(e), f0(p.LandMax))
		t := d.Reset
		d.FreeAt = &t
	default:
		what := fmt.Sprintf("%d lanes running use", d.Running)
		if d.Running == 1 {
			what = "1 lane running uses"
		}
		if by == LimitSpill {
			d.Why = fmt.Sprintf("drain: %d lanes already run past the reset", p.SpillMax)
		} else {
			d.Why = "drain full: " + what + " the room left before " + fin
		}
	}
	if d.FreeAt == nil && len(rs) > 0 {
		end := math.Inf(1)
		for _, x := range rs {
			end = math.Min(end, x.rem)
		}
		t := now.Add(hoursDur(end))
		d.FreeAt = &t
	}
	if by == Limit5h && fiveOpen && len(rs) > 0 {
		// the earliest of the 5-hour reset and the end of the first running lane
		end := math.Inf(1)
		for _, x := range rs {
			end = math.Min(end, x.rem)
		}
		if t := now.Add(hoursDur(end)); t.Before(*d.FreeAt) {
			d.FreeAt = &t
		}
	}
	return d
}

// DrainHint is the one-line instruction for an agent on a drain pick (`--json` hint, stderr
// with `--exec`).
func DrainHint(label string, reset, finishBy time.Time) string {
	return fmt.Sprintf("Weekly room on %s ends at %s (finish by %s): plan work to finish or checkpoint by then; "+
		"if you hit a usage limit before that, stop and start a new session with `wy account next` instead of waiting.",
		label, reset.Local().Format("15:04"), finishBy.Local().Format("15:04"))
}
