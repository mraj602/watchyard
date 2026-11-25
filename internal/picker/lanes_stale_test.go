package picker

import (
	"strings"
	"testing"
)

// Stale data with the switch on (setup.md 8.6, Stale data; live pre6 05:37, the hub away:
// "claude-4 5h 100% >= 95" from a sample 3 h 37 min old while it was at ~68%). An old
// sample's level neither blocks an aggressive account nor draws a lane to it: it is ranked
// after the accounts with fresh data, and explain says so.
func TestLanesStaleData(t *testing.T) {
	mv := &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{},
		ExpectFloorH: 0.5, StaleMin: 10}
	s := world(50, 20)
	s["claude-2"] = smp(30, 100, 217, 72) // stale-high
	in := laneIn(s, 1)
	in.Params = DefaultLaneParams
	in.Mover = mv
	r := rank(t, in)
	row := rowOf(r, "claude-2")
	if row.Skip || !row.Last || !strings.Contains(row.Reason, "data stale (217 min old)") {
		t.Fatalf("stale-high claude-2 %+v", row)
	}
	if len(r.Picks) == 0 || r.Picks[0].Key == "claude-2" {
		t.Fatalf("picked the stale account first: %+v", r.Picks)
	}
	// Without the switch's view: as before (its 5h at 100% blocks it).
	in.Mover = nil
	if row := rowOf(rank(t, in), "claude-2"); !row.Skip {
		t.Fatalf("no switch: %+v", row)
	}
	// Stale-low: the emptiest account by its old sample does not draw the lane.
	s = world(60, 20)
	s["claude-2"] = smp(5, 0, 45, 72)
	in = laneIn(s, 1)
	in.Params, in.Mover = DefaultLaneParams, mv
	r = rank(t, in)
	if row := rowOf(r, "claude-2"); !row.Last || row.Skip || !strings.Contains(row.Reason, "neither blocks nor draws") {
		t.Fatalf("stale-low claude-2 %+v", row)
	}
	if len(r.Picks) == 0 || r.Picks[0].Key == "claude-2" {
		t.Fatalf("stale-low drew the lane: %+v", r.Picks)
	}
	// A protected account keeps its blocks on stale data.
	s = world(60, 20)
	for k := range s {
		if isProt(k) {
			s[k] = smp(20, 100, 217, 72)
			in = laneIn(s, 1)
			in.Params, in.Mover = DefaultLaneParams, mv
			if row := rowOf(rank(t, in), k); !row.Skip {
				t.Fatalf("protected stale-high %s not blocked: %+v", k, row)
			}
			break
		}
	}
}

// Live (pre7): the picker leased 2de58a08 (ai-lane-C3; the ledger line has no --expect, drain
// true) on claude-3 at 07:36, 7d 85%, cap 88 (98 less its 2 pins' 10), its 5 hours resetting in
// 45 minutes; the switch moved it off at 07:53, "7d projected 95% (cap 88%) at 16.4 points/h".
// A lane of unknown length counts as a medium lane (48 minutes, as the switch counts it from its
// lease line), at the measured 16.4 within 3× the medium class's 4 (12), and a placement must
// stay the switch's 5-point margin under the caps, as a moved session must.
func TestLanesNoPlacementTheSwitchUndoes(t *testing.T) {
	s := world(40, 20)
	s["claude-3"] = LaneSample{TS: hrs(-5.0 / 60), Weekly: fp(85), FiveHour: fp(22), FiveHourReset: hrs(0.75), WeeklyReset: hrs(49.5)}
	mv := &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{},
		ExpectFloorH: 0.5, Hysteresis: 5}
	mk := func(weekly float64, ex []LaneExpect) LaneInput {
		s["claude-3"] = LaneSample{TS: hrs(-5.0 / 60), Weekly: fp(weekly), FiveHour: fp(22), FiveHourReset: hrs(0.75), WeeklyReset: hrs(49.5)}
		in := laneIn(s, 1)
		in.Params, in.Stats, in.Mover, in.Expects = DefaultLaneParams, LaneStats{Rate: 16.4, NRate: 20}, mv, ex
		for i := range in.Accounts {
			if in.Accounts[i].Key == "claude-3" {
				in.Accounts[i].PinReserve5, in.Accounts[i].PinReserve7, in.Accounts[i].Pins = 20, 10, 2
				in.Accounts[i].MoveUnits = 1.8 / DefaultLaneParams.F5
				mv.Units["claude-3"] = in.Accounts[i].MoveUnits
			}
		}
		return in
	}
	if isProt("claude-3") {
		t.Skip("claude-3 is protected in this fixture")
	}
	// At 85 (no --expect): 85 + 1.8 × 5 h / ρ + 12 × 0.8 h / ρ ≈ 87.6, over 88 − 5.
	row := rowOf(rank(t, mk(85, nil)), "claude-3")
	if !row.Skip || !strings.Contains(row.Reason, "7d 85%") || !strings.Contains(row.Reason, "over its cap 88% less the switch's 5-point margin") {
		t.Fatalf("claude-3 at 85: %+v", row)
	}
	// At 79: 79 + 1.25 + 1.33 ≈ 81.6 with no --expect, and 79 + 1.25 + 16.4 × 0.5 h / ρ ≈ 81.4
	// for a short lane (counted for 30 minutes): both under 83, placed.
	for _, ex := range [][]LaneExpect{nil, {{Class: ExpectShort}}} {
		if row := rowOf(rank(t, mk(79, ex)), "claude-3"); strings.Contains(row.Reason, "switch's projection") {
			t.Fatalf("claude-3 at 79 (%v): %+v", ex, row)
		}
	}
	// Without the margin, at 85: 87.6, under 88.
	mv.Hysteresis = 0
	if row := rowOf(rank(t, mk(85, nil)), "claude-3"); strings.Contains(row.Reason, "switch's projection") {
		t.Fatalf("claude-3 at 85 without the margin: %+v", row)
	}
}
