package picker

import (
	"fmt"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/leases"
)

// Live pre8 (2026-11-25): `wy account next --explain` refused every account, "at 56-59 points/h
// with this lane the switch's projection is 131-262%", while lanes burned ~9-14 points/h. The
// ledger held the switch's move leases (strategy "move"): a moved session keeps its id, its
// lease's `ended` line comes at the move and a new lease starts. Each lease's burn is its
// session's since the lease's time (every account), so the picker lease (ended at its first
// move) and each move lease (a few minutes, the resume's cache write) were measured at many
// times the lane's burn. A lane is now the picker lease's session over its whole life.
func TestMeasureLanesMoveLeases(t *testing.T) {
	f5 := 1e-6
	var led []leases.Lease
	var local []LaneLease
	lease := func(sid, strategy, from string, at, launched, ended time.Time, burned float64) {
		led = append(led, leases.Lease{Line: leases.Line{V: 1, Op: leases.OpLease, At: at, Key: "k", Model: "opus",
			SessionID: sid, Strategy: strategy, MovedFrom: from}, Launched: launched, Ended: ended})
		local = append(local, LaneLease{Key: "k", T0: at, SID: sid, Seen: true, Ended: !ended.IsZero(), Burned: burned / f5})
	}
	for i := 0; i < 12; i++ {
		t0 := lnow.Add(-time.Duration(4+i) * time.Hour)
		sid := fmt.Sprintf("s%02d", i)
		// 30 points over 3 hours (10 points/h), moved at 20 and at 60 minutes. Each lease's
		// burn is the session's since that lease.
		lease(sid, "lanes", "", t0, t0, t0.Add(20*time.Minute), 30)
		lease(sid, "move", "claude-2", t0.Add(20*time.Minute), t0.Add(20*time.Minute), t0.Add(60*time.Minute), 25)
		lease(sid, "move", "claude-4", t0.Add(60*time.Minute), t0.Add(60*time.Minute), t0.Add(180*time.Minute), 15)
	}
	// A picker lane of 10 minutes: under the least wall time, not a rate.
	t1 := lnow.Add(-2 * time.Hour)
	lease("short", "lanes", "", t1, t1, t1.Add(10*time.Minute), 9)
	st := measureLanes(led, local, nil, map[string]float64{"k": f5}, "opus", lnow)
	if st.NRate != 12 || !near1(st.Rate, 10, 1e-6) {
		t.Fatalf("rate %.1f over %d lanes, want 10 over 12 (whole lives, picker leases only)", st.Rate, st.NRate)
	}
	if st.NLen != 13 || st.Len < 3*time.Hour-time.Minute {
		t.Errorf("length %v over %d lanes, want the median life 3 h", st.Len, st.NLen)
	}
	if st.N != 13 {
		t.Errorf("cost over %d lanes, want the 13 picker lanes", st.N)
	}

	// The same session still running after its moves: to now.
	led, local = nil, nil
	for i := 0; i < 10; i++ {
		t0 := lnow.Add(-2 * time.Hour)
		sid := fmt.Sprintf("r%02d", i)
		lease(sid, "lanes", "", t0, t0, t0.Add(15*time.Minute), 24)
		lease(sid, "move", "claude-2", t0.Add(15*time.Minute), t0.Add(15*time.Minute), time.Time{}, 20)
	}
	st = measureLanes(led, local, nil, map[string]float64{"k": f5}, "", lnow)
	if st.NRate != 10 || !near1(st.Rate, 12, 1e-6) {
		t.Fatalf("running: rate %.1f over %d, want 12 (24 points over 2 h) over 10", st.Rate, st.NRate)
	}
}

// The new lane in the switch-projection check: the measured burn (the model's p75) within
// [0.5×, 3×] its class's, a lane without --expect a medium lane for the medium class's time.
func TestSwitchLaneClamp(t *testing.T) {
	p := DefaultLaneParams
	med := p.ExpectMedium.Cost / p.ExpectMedium.Dur.Hours() // 4 points/h
	sl := p.switchLane(9, "", 0, LaneStats{Rate: 57, NRate: 14})
	if !sl.clamped || !near1(sl.rate, 3*med, 1e-9) || sl.class != ExpectMedium || sl.dur != p.ExpectMedium.Dur {
		t.Fatalf("no --expect, measured 57: %+v", sl)
	}
	// Opus lanes measured at 11 (under 3 × 4): taken as measured.
	if sl = p.switchLane(4, "", 0, LaneStats{Rate: 11, NRate: 14}); sl.clamped || sl.rate != 11 {
		t.Fatalf("measured 11: %+v", sl)
	}
	// Not measured: the estimate's rate, within the band.
	if sl = p.switchLane(9, ExpectLong, 3*time.Hour, LaneStats{}); sl.clamped || sl.rate != 9 || sl.dur != 3*time.Hour {
		t.Fatalf("long at 9 (2.3 ... 14): %+v", sl)
	}
	if sl = p.switchLane(0.5, ExpectMedium, time.Hour, LaneStats{Rate: 1, NRate: 10}); !sl.clamped || sl.rate != 0.5*med {
		t.Fatalf("measured 1: %+v", sl)
	}
}

// Each pick carries the lane as the switch-projection check counted it, for its lease line:
// without --expect a medium lane for the medium class's time; with one, its class and time.
func TestLanePickSwitchExpect(t *testing.T) {
	in := laneIn(world(40, 20), 1)
	in.Params = DefaultLaneParams
	pk := rank(t, in).Picks[0]
	if pk.SwitchClass != ExpectMedium || pk.SwitchDur != DefaultLaneParams.ExpectMedium.Dur {
		t.Fatalf("no --expect: %+v", pk)
	}
	in.Expects = []LaneExpect{{Dur: 20 * time.Minute}}
	pk = rank(t, in).Picks[0]
	// (EstFloor: a lane costing more than it says runs longer at its rate: 20 min at the short
	// class's rate, raised to the mean lane cost, 46 min 40 s; the check used the same time.)
	if pk.SwitchClass != ExpectShort || pk.SwitchDur < 20*time.Minute || pk.SwitchDur > time.Hour {
		t.Fatalf("--expect 20m: %+v", pk)
	}
}

// Live (pre9, ~10:00): four lanes went onto claude-2 within minutes, each checked against its
// nearly idle meter; 15 minutes later they burned 60-90 points/h. Lanes leased on the account
// in the last 15 minutes (this call's earlier picks too) now count at the lane burn.
func TestLanesFreshLanesCount(t *testing.T) {
	picksOn := func(young float64) int {
		s := world(40, 20)
		for k := range s { // only claude-2 has room
			s[k] = LaneSample{TS: hrs(-1.0 / 60), Weekly: fp(99), FiveHour: fp(20), FiveHourReset: hrs(3), WeeklyReset: hrs(100)}
		}
		s["claude-2"] = LaneSample{TS: hrs(-1.0 / 60), Weekly: fp(30), FiveHour: fp(20), FiveHourReset: hrs(3), WeeklyReset: hrs(100)}
		in := laneIn(s, 6)
		in.Params = DefaultLaneParams
		in.Stats = LaneStats{Rate: 12, NRate: 20}
		in.Mover = &MoverView{Cap5: 50, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{},
			ExpectFloorH: 0.5, Hysteresis: 5, YoungMin: young}
		n := 0
		r := rank(t, in)
		for _, pk := range r.Picks {
			if pk.Key == "claude-2" {
				n++
			}
		}
		t.Logf("young %v: picks %v", young, picksOf(r))
		return n
	}
	// 20 + k × 12 × 0.8 h <= 50 − 5: at most 2 lanes.
	if n := picksOn(15); n > 2 {
		t.Fatalf("%d lanes on claude-2 with the fresh lanes counted, want <= 2", n)
	}
	if n := picksOn(0); n <= 2 {
		t.Fatalf("without the rule: %d lanes on claude-2, want more (each checked against the idle meter)", n)
	}
}

// A session moved here is not a fresh lane: its move lease is never charged at the new-lane
// burn (the switch counts it at the burn it moved with).
func TestFreshLanesSkipMoved(t *testing.T) {
	now := time.Date(2026, 11, 25, 5, 0, 0, 0, time.UTC)
	m := &MoverView{YoungMin: 15}
	acc := LaneAccount{Key: "claude-6"}
	ls := []LaneLease{{Key: "claude-6", T0: now.Add(-5 * time.Minute), SID: "new"},
		{Key: "claude-6", T0: now.Add(-5 * time.Minute), SID: "moved", Moved: true}}
	if f := freshLanes(m, ls, acc, 12, DefaultLaneParams, now); len(f) != 1 {
		t.Fatalf("fresh lanes %+v, want only the new one", f)
	}
	var mv leases.Lease
	mv.Strategy = "move"
	if !moveLease(mv) {
		t.Fatal("a move lease is not one")
	}
}
