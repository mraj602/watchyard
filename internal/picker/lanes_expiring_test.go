package picker

import (
	"strings"
	"testing"
	"time"
)

// Live (2026-11-25 04:25): protected claude-5 at 85% of its week, the week resetting at 05:29,
// its 5 hours unused, and nothing used it: the switch's protected weekly cap (80) bounded its
// drain. Within the switch's expiring window (move_expiring_h, 3 h) a protected account's
// weekly cap is the aggressive one; its 5-hour cap stays 80.
func TestLanesExpiringProtectedRoom(t *testing.T) {
	s := world(99, 20) // every aggressive account full
	five := smp(85, 0, 5, 1.07)
	five.FiveHour, five.FiveHourReset = nil, time.Time{} // 5-hour window not started
	s["claude-5"] = five
	delete(s, "claude-1")
	delete(s, "claude-7")
	in := laneIn(s, 1)
	in.Params = DefaultLaneParams
	mv := &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{}, ExpiringH: 3,
		ExpectFloorH: 0.5}
	in.Mover = mv
	r := rank(t, in)
	row := rowOf(r, "claude-5")
	if len(r.Picks) == 0 || r.Picks[0].Key != "claude-5" || row.ExpiringRoom != 98 {
		t.Fatalf("picks %+v, claude-5 %+v", r.Picks, row)
	}
	// Outside the window (or with it off): the protected weekly cap holds, nothing placed there.
	mv.ExpiringH = 0
	r = rank(t, in)
	if len(r.Picks) > 0 && r.Picks[0].Key == "claude-5" {
		t.Fatalf("claude-5 picked without expiring room: %+v", rowOf(r, "claude-5"))
	}
}

// Live (2026-11-25 04:25): claude-5's 5 hours unused, a short lane (--expect short, under 25
// minutes) at 23 points/h was refused: "the switch's projection is 115%, over its cap 95%". The
// new lane is projected only over its expected time (else the destination horizon), at most to
// the 5-hour reset; the account's own burn as before.
func TestLanesMoverShortLane(t *testing.T) {
	s := world(40, 20)
	s["claude-2"] = LaneSample{TS: hrs(-5.0 / 60), Weekly: fp(40), WeeklyReset: hrs(72)} // 5h not started
	in := laneIn(s, 1)
	in.Params = DefaultLaneParams
	in.Stats.Rate = 23
	in.Expects = []LaneExpect{{Class: "short"}}
	mv := &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{},
		ExpectFloorH: 0.5}
	in.Mover = mv
	row := rowOf(rank(t, in), "claude-2")
	if strings.Contains(row.Reason, "switch's projection") {
		t.Fatalf("short lane refused: %+v", row)
	}
	// No expected duration: counted as a medium lane, at most the medium class's cost (live
	// pre8: lanes of unknown length projected to the reset at a measured 57 points/h refused
	// every account), not 23 × 5 = 115.
	in.Expects = nil
	row = rowOf(rank(t, in), "claude-2")
	if strings.Contains(row.Reason, "switch's projection") {
		t.Fatalf("unknown length refused: %+v", row)
	}
	// A short lane (under 30 minutes: counted for 30) on an account at 85: 85 + 23 × 0.5 =
	// 96.5, over 95.
	s["claude-2"] = LaneSample{TS: hrs(-5.0 / 60), Weekly: fp(40), FiveHour: fp(85), FiveHourReset: hrs(4), WeeklyReset: hrs(72)}
	in = laneIn(s, 1)
	in.Params, in.Stats.Rate, in.Expects, in.Mover = DefaultLaneParams, 23, []LaneExpect{{Class: "short"}}, mv
	row = rowOf(rank(t, in), "claude-2")
	if !strings.Contains(row.Reason, "switch's projection") {
		t.Fatalf("hot account: %+v", row)
	}
}
