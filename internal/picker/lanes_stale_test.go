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
		DestHorizonH: 1.5, DestFloorH: 1, StaleMin: 10}
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
