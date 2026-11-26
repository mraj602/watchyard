package picker

import (
	"strings"
	"testing"
)

// setup.md 8.5, The switch's projection: while Smart Account Switch is on, a new lane is not
// placed on an account whose measured burn plus the lane takes the switch's projection over
// its caps (live run: claude-3 ranked first while burning 27 points/h toward 107%).
func TestLanesMoverProjection(t *testing.T) {
	s := world(40, 20)
	in := laneIn(s, 1)
	in.Params = DefaultLaneParams
	first := rank(t, in).Picks[0].Key
	// The first pick burns 30 points/h now: 10 + 30 × 3 h = 100, over 95 with the lane.
	mv := &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{}}
	f5 := DefaultLaneParams.F5
	mv.Units[first] = 30 / f5
	in.Mover = mv
	for i := range in.Accounts {
		in.Accounts[i].MoveUnits = mv.Units[in.Accounts[i].Key]
	}
	r := rank(t, in)
	row := rowOf(r, first)
	if !row.Skip || row.SkipLevel != 3 || !strings.Contains(row.Reason, "the switch's projection") {
		t.Fatalf("row %+v", row)
	}
	if r.Picks[0].Key == first {
		t.Fatalf("still picked %s", first)
	}
	// A quiet account stays eligible: 10 + 2 × 3 h plus a lane is under the cap.
	mv.Units[first] = 2 / f5
	for i := range in.Accounts {
		in.Accounts[i].MoveUnits = mv.Units[in.Accounts[i].Key]
	}
	if r := rank(t, in); r.Picks[0].Key != first {
		t.Fatalf("quiet account skipped: %+v", rowOf(r, first))
	}
}
