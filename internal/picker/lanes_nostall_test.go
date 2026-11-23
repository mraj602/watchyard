package picker

// Owner change 2 (setup.md 8.5, No stalling and Estimates): a lane waits only when every
// account is at a hard limit; the agent's estimate sizes the lane and keeps long lanes off
// accounts that reset before they would finish.

import (
	"testing"
	"time"
)

// nsIn is laneIn with the default (no-stall, estimates) parameters.
func nsIn(s map[string]LaneSample, n int) LaneInput {
	in := laneIn(s, n)
	in.Params = DefaultLaneParams
	return in
}

// burstAll gives every aggressive account its full burst of recent leases.
func burstAll(in *LaneInput) {
	for _, a := range aggrNames {
		for i := 0; i < in.Params.BurstAggr; i++ {
			in.Leases = append(in.Leases, dlease(a, 5))
		}
	}
}

func TestNoStallBurstCapped(t *testing.T) {
	// Every aggressive account burst-capped: v3 waits, no-stall places the lanes on them (over).
	in := laneIn(dworld(), 3)
	burstAll(&in)
	if r := rank(t, in); r.Waiting != 3 || len(r.Picks) != 0 {
		t.Fatalf("v3: want the batch to wait, got picks %v waiting %d", picksOf(r), r.Waiting)
	}
	in.Params = DefaultLaneParams
	r := rank(t, in)
	if r.Waiting != 0 || len(r.Picks) != 3 {
		t.Fatalf("no-stall: picks %v waiting %d", picksOf(r), r.Waiting)
	}
	for _, p := range r.Picks {
		if isProt(p.Key) || !p.Over {
			t.Errorf("no-stall: want burst-capped aggressive accounts first (over), got %+v", p)
		}
	}

	// The aggressive accounts at a hard limit: the protected ones take the lanes.
	s := dworld()
	for _, a := range aggrNames {
		s[a] = dsmp(99, 10, 140, 3)
	}
	in = nsIn(s, 2)
	r = rank(t, in)
	if r.Waiting != 0 || len(r.Picks) != 2 {
		t.Fatalf("protected fallback: picks %v waiting %d", picksOf(r), r.Waiting)
	}
	for _, p := range r.Picks {
		if !isProt(p.Key) {
			t.Errorf("want protected picks, got %s", p.Key)
		}
	}

	// Every account at a hard limit: the lanes wait.
	for _, a := range allNames {
		s[a] = dsmp(99, 10, 140, 3)
	}
	if r := rank(t, nsIn(s, 2)); r.Waiting != 2 || len(r.Picks) != 0 {
		t.Fatalf("all at hard limits: picks %v waiting %d", picksOf(r), r.Waiting)
	}
}

func TestNoStallProtectedGate(t *testing.T) {
	// Aggressive accounts at hard limits; the protected accounts are past their 5-hour gate
	// (45%) but under the hard limits: no-stall uses them (over) instead of waiting.
	s := dworld()
	for _, a := range aggrNames {
		s[a] = dsmp(99, 10, 140, 3)
	}
	for _, a := range protNames {
		s[a] = dsmp(20, 60, 140, 3)
	}
	in := laneIn(s, 1)
	if r := rank(t, in); len(r.Picks) != 0 {
		t.Fatalf("v3: want a wait past the protected gate, got %v", picksOf(r))
	}
	r := rank(t, nsIn(s, 1))
	if len(r.Picks) != 1 || !isProt(r.Picks[0].Key) || !r.Picks[0].Over {
		t.Fatalf("no-stall: want one protected pick over its gate, got %+v (waiting %d)", r.Picks, r.Waiting)
	}
}

func TestExpectRunway(t *testing.T) {
	// X and Z drain (weekly room left; 2 and 20 hours to their resets), X first. A short lane
	// goes to X; a long one (3 hours) would outlast X's reset and goes to Z first.
	s := dworld()
	s[dX] = dsmp(40, 10, 2, 3)
	s[dZ] = dsmp(40, 10, 20, 3)
	in := nsIn(s, 1)
	in.Expects = []LaneExpect{{Class: ExpectShort}}
	r := rank(t, in)
	if len(r.Picks) != 1 || r.Picks[0].Key != dX || r.Picks[0].Expect != ExpectShort {
		t.Fatalf("short lane: want %s (class short), got %+v", dX, r.Picks)
	}
	in.Expects = []LaneExpect{{Class: ExpectLong}}
	r = rank(t, in)
	if len(r.Picks) != 1 || r.Picks[0].Key != dZ || r.Picks[0].Expect != ExpectLong {
		t.Fatalf("long lane: want it off %s (class long), got %+v", dX, r.Picks)
	}
	// Minutes alone set the class by the cut points.
	in.Expects = []LaneExpect{{Dur: 150 * time.Minute}}
	if r = rank(t, in); len(r.Picks) != 1 || r.Picks[0].Key != dZ || r.Picks[0].Expect != ExpectLong {
		t.Fatalf("150 minutes: want a long lane off %s, got %+v", dX, r.Picks)
	}
}

func TestExpectEstimate(t *testing.T) {
	p := DefaultLaneParams
	acc := LaneAccount{Key: dX}
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{5 * time.Minute, ExpectShort}, {25 * time.Minute, ExpectShort}, {26 * time.Minute, ExpectMedium},
		{2 * time.Hour, ExpectMedium}, {121 * time.Minute, ExpectLong}} {
		if got := p.classOf(c.d); got != c.want {
			t.Errorf("classOf(%v) = %s, want %s", c.d, got, c.want)
		}
	}
	// No estimate: medium, not given.
	q := p
	q.EstFloor = false
	e := q.estimate(LaneExpect{}, acc, LaneStats{})
	if e.class != ExpectMedium || e.given || !near1(e.dur, p.ExpectMedium.Dur.Hours(), 1e-9) {
		t.Errorf("no estimate: %+v", e)
	}
	// Measured lanes: the measured median length.
	st := LaneStats{Cost: 4, Mean: 3, Len: 30 * time.Minute, N: 20, NLen: 20}
	if e = p.estimate(LaneExpect{}, acc, st); e.class != ExpectMeasured || !near1(e.dur, 0.5, 1e-9) {
		t.Errorf("no estimate, measured: %+v", e)
	}
	// Long: its benchmark, given.
	if e = p.estimate(LaneExpect{Class: ExpectLong}, acc, LaneStats{}); !e.given || e.cost < p.ExpectLong.Cost-1e-9 || !near1(e.dur, 3, 1e-9) {
		t.Errorf("long: %+v", e)
	}
	// Tokens set the cost (weighted units × the account's meter factor).
	e = q.estimate(LaneExpect{Class: ExpectMedium, Tokens: 1e6}, acc, LaneStats{})
	if want := 1e6 * q.UnitsPerToken * q.account(acc).F5; !near1(e.cost, want, 1e-9) {
		t.Errorf("tokens: cost %.3f, want %.3f", e.cost, want)
	}
	// The cost floor keeps the stated burn rate and stretches the wall time.
	short := q.estimate(LaneExpect{Class: ExpectShort}, acc, st)
	fl := p.estimate(LaneExpect{Class: ExpectShort}, acc, st)
	if fl.cost < 3-1e-9 || !near1(fl.rate(), short.rate(), 1e-9) || fl.dur <= short.dur {
		t.Errorf("floor: %+v vs %+v", fl, short)
	}
}
