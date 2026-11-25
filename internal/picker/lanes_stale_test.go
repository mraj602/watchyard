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
// The picker counted the lane of unknown length for 90 minutes, at most to the 5-hour reset;
// the switch to min(the weekly reset, 5 h). Now both count it to the trigger's horizon.
func TestLanesNoPlacementTheSwitchUndoes(t *testing.T) {
	s := world(40, 20)
	s["claude-3"] = LaneSample{TS: hrs(-5.0 / 60), Weekly: fp(85), FiveHour: fp(22), FiveHourReset: hrs(0.75), WeeklyReset: hrs(49.5)}
	in := laneIn(s, 1)
	in.Params = DefaultLaneParams
	in.Stats = LaneStats{Rate: 16.4, NRate: 20}
	mv := &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{}, ExpectFloorH: 0.5}
	in.Mover = mv
	for i := range in.Accounts {
		if in.Accounts[i].Key == "claude-3" {
			in.Accounts[i].PinReserve5, in.Accounts[i].PinReserve7, in.Accounts[i].Pins = 20, 10, 2
			in.Accounts[i].MoveUnits = 1.8 / DefaultLaneParams.F5
			mv.Units["claude-3"] = in.Accounts[i].MoveUnits
		}
	}
	row := rowOf(rank(t, in), "claude-3")
	if isProt("claude-3") {
		t.Skip("claude-3 is protected in this fixture")
	}
	if !row.Skip || !strings.Contains(row.Reason, "7d 85%") || !strings.Contains(row.Reason, "over its cap 88%") {
		t.Fatalf("claude-3 %+v", row)
	}
	// Expected to run 20 minutes (--expect short): counted for 30 minutes; at 83% it fits
	// (to the trigger's horizon it would not: 83 + 18.2 / ρ × 5 h).
	s["claude-3"] = LaneSample{TS: hrs(-5.0 / 60), Weekly: fp(83), FiveHour: fp(22), FiveHourReset: hrs(0.75), WeeklyReset: hrs(49.5)}
	acc := in.Accounts
	in = laneIn(s, 1)
	in.Accounts, in.Params, in.Stats, in.Mover = acc, DefaultLaneParams, LaneStats{Rate: 16.4, NRate: 20}, mv
	for i := range in.Accounts {
		if in.Accounts[i].Key == "claude-3" {
			in.Accounts[i].Sample = s["claude-3"]
		}
	}
	if row := rowOf(rank(t, in), "claude-3"); !row.Skip {
		t.Fatalf("unknown length at 83%%: %+v", row)
	}
	in.Expects = []LaneExpect{{Class: ExpectShort}}
	in.Params.UseEstimate = true
	if row := rowOf(rank(t, in), "claude-3"); row.Skip && strings.Contains(row.Reason, "switch's projection") {
		t.Fatalf("short lane refused: %+v", row)
	}
	// With the switch's 5-point margin (a placement is a destination): the short lane's 30
	// minutes take claude-3 over 88 − 5, so it is not placed there.
	mv.Hysteresis = 5
	if row := rowOf(rank(t, in), "claude-3"); !row.Skip || !strings.Contains(row.Reason, "less the switch's 5-point margin") {
		t.Fatalf("short lane at 83%% with the margin: %+v", row)
	}
}
