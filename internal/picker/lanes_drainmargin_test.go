package picker

import (
	"strings"
	"testing"
	"time"
)

// A draining account's switch projection is checked against the drain aim itself, with no
// switch margin under it (setup.md 8.5, Drain aim; The switch's projection). Live pre15,
// claude-3 at 89% of the week, 4 h before its Thu 03:30 reset, the owner draining it, 2 pins
// at about 5.7 points/h: lanes were refused with "the switch's projection is 91%, over its cap
// 95% less the switch margin", so the drain could never come within 5 points of its aim.
func TestDrainSwitchMarginClaude3(t *testing.T) {
	const ratio, pinsRate = 9.1, 5.7
	run := func(units float64, drain bool) LaneRow {
		s := dworld()
		s[dZ] = dsmp(89, 30, 4, 2)
		in := laneIn(s, 1)
		in.Params = DefaultLaneParams
		in.Mover = &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{},
			ExpectFloorH: 0.5, Hysteresis: 5}
		for i := range in.Accounts {
			a := &in.Accounts[i]
			if a.Key != dZ {
				continue
			}
			a.Ratio, a.Protected = ratio, false
			if drain {
				a.DrainUntil = lnow.Add(3 * time.Hour)
			}
			a.Pins, a.PinReserve5, a.PinReserve7 = 2, 20, 10
			a.PinUnits = pinsRate / in.Params.account(*a).F5
			scalePins(a, in.Params, lnow)
			a.MoveUnits = units / in.Params.account(*a).F5
			in.Mover.Units[dZ] = a.MoveUnits
		}
		return rowOf(rank(t, in), dZ)
	}
	// The pins' own burn (5.7) plus a lane: 89 + about 13/9.1 × 2-4 h, under the aim (97.3 now
	// less the scaled reserve 5.0 = 92.3) but within 5 of it: taken.
	row := run(pinsRate, true)
	if row.Skip {
		t.Fatalf("draining claude-3 refused: %q (%+v)", row.Reason, row.Drain)
	}
	if !row.Drain.On {
		t.Fatalf("drain off: %+v", row.Drain)
	}
	// Burning far more, the projection is over the aim itself: still refused, with no margin
	// in the reason.
	row = run(40, true)
	if !row.Skip || !strings.Contains(row.Reason, "the switch's projection") || strings.Contains(row.Reason, "margin") {
		t.Fatalf("over the aim: skip %v %q", row.Skip, row.Reason)
	}
	// The automatic drain near the reset (no owner drain) gets the same: taken.
	row = run(pinsRate, false)
	if row.Skip || !row.Drain.On || row.Drain.Owner {
		t.Fatalf("automatic drain: skip %v %q (%+v)", row.Skip, row.Reason, row.Drain)
	}
}
