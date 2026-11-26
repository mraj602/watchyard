package picker

import (
	"strings"
	"testing"
	"time"
)

// An account like claude on 2026-11-26: owner drain until 23:25, week 76% (ratio about 6.9)
// resetting in 1.2 h, 5-hour 10% resetting in 2.3 h, burning 60 points/h. Without
// drain_ignore_5h the switch's 5-hour projection refuses the lane; with it the lane is taken
// (the weekly room still counts).
func TestDrainIgnore5hLane(t *testing.T) {
	run := func(ign bool) LaneRow {
		s := dworld()
		s[dZ] = dsmp(76, 10, 1.2, 2.3)
		in := laneIn(s, 1)
		in.Params = DefaultLaneParams
		in.Mover = &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{},
			ExpectFloorH: 0.5, Hysteresis: 5, DrainAim7: 0, DrainCapMax7: 0}
		for i := range in.Accounts {
			a := &in.Accounts[i]
			if a.Key == dZ {
				a.Ratio, a.Protected = 6.9, false
				a.DrainUntil = lnow.Add(1*time.Hour + 5*time.Minute)
				a.Ign5h = ign
				a.MoveUnits = 60 / DefaultLaneParams.F5
				in.Mover.Units[dZ] = a.MoveUnits
			}
		}
		return rowOf(rank(t, in), dZ)
	}
	off := run(false)
	if !off.Skip || !strings.Contains(off.Reason, "switch's projection") {
		t.Fatalf("without the flag: skip %v %q drain %+v, want refused on 5h", off.Skip, off.Reason, off.Drain)
	}
	on := run(true)
	if on.Skip || !on.Drain.On || !on.Drain.Ign5h {
		t.Fatalf("with the flag: skip %v %q drain %+v, want eligible", on.Skip, on.Reason, on.Drain)
	}
	if on.Drain.Cap5h >= 0 {
		t.Fatalf("5h capacity %v, want -1 (not limiting)", on.Drain.Cap5h)
	}
}
