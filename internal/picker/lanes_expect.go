package picker

import (
	"math"
	"time"
)

// Lane size classes (wy account next --expect; setup.md 8.5, Estimates).
const (
	ExpectShort  = "short"
	ExpectMedium = "medium"
	ExpectLong   = "long"
	// ExpectMeasured: no estimate; the measured median lane (length and mean cost).
	ExpectMeasured = "measured"
)

// LaneExpect is what the launching agent says about one lane: a class, and optionally its wall
// time and its tokens. All zero: no estimate.
type LaneExpect struct {
	Class  string
	Dur    time.Duration
	Tokens float64
}

// LaneClass is one class's benchmark: the lane's wall time and its 5-hour points.
type LaneClass struct {
	Dur  time.Duration
	Cost float64
}

// laneEst is a resolved estimate: hours, 5-hour points, the class used and whether the agent
// gave one.
type laneEst struct {
	dur, cost float64
	class     string
	given     bool
}

// rate is the estimate's burn, 5-hour points per hour.
func (e laneEst) rate() float64 { return e.cost / math.Max(e.dur, 1.0/60) }

// classOf is the class of a wall time, by the cut points between the benchmarks.
func (p LaneParams) classOf(d time.Duration) string {
	switch {
	case d <= p.ExpectCutShort:
		return ExpectShort
	case d <= p.ExpectCutLong:
		return ExpectMedium
	}
	return ExpectLong
}

func (p LaneParams) classBench(c string, st LaneStats) LaneClass {
	if m, ok := st.Classes[c]; ok && m.Dur > 0 && m.Cost > 0 {
		return m
	}
	switch c {
	case ExpectShort:
		return p.ExpectShort
	case ExpectLong:
		return p.ExpectLong
	}
	return p.ExpectMedium
}

// expectAt is lane k's estimate: Expects[k], else the last of Expects, else Expect (a wall time).
func (in LaneInput) expectAt(k int) LaneExpect {
	if len(in.Expects) > 0 {
		return in.Expects[min(k, len(in.Expects)-1)]
	}
	return LaneExpect{Dur: in.Expect}
}

// estimate resolves x for acc: the class from x.Class, else from x.Dur; the wall time x.Dur, else
// the class's; the cost from x.Tokens (weighted at UnitsPerToken, the account's f), else the
// class's cost scaled to the wall time given. No estimate: medium, or the measured median length
// and mean cost when there are lane statistics.
func (p LaneParams) estimate(x LaneExpect, acc LaneAccount, st LaneStats) laneEst {
	given := x.Class != "" || x.Dur > 0 || x.Tokens > 0
	c := x.Class
	if c == "" && x.Dur > 0 {
		c = p.classOf(x.Dur)
	}
	if c == "" {
		c = ExpectMedium
	}
	b := p.classBench(c, st)
	e := laneEst{dur: b.Dur.Hours(), cost: b.Cost, class: c, given: given}
	if !given && st.N > 0 && st.NLen > 0 {
		_, em, lmed, _ := p.laneStats(st)
		e.dur, e.cost, e.class = lmed, em, ExpectMeasured
	}
	if x.Dur > 0 {
		e.cost *= x.Dur.Hours() / math.Max(e.dur, 1.0/60)
		e.dur = x.Dur.Hours()
	}
	if x.Tokens > 0 {
		e.cost = x.Tokens * p.UnitsPerToken * p.account(acc).F5
	}
	if p.EstFloor { // never cheaper than the measured (or fallback) mean lane
		// A lane that costs more than it says burns at the rate it says for longer: the wall
		// time grows with the cost, so a short lane is not taken as a burst.
		if _, em, _, _ := p.laneStats(st); em > e.cost {
			e.dur *= em / math.Max(e.cost, 1e-9)
			e.cost = em
		}
	}
	return e
}
