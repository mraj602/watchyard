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

// The new lane in the switch-projection check: a measured burn far above its class's is cut to
// 2× the class's, and a lane without --expect adds at most the medium class's cost.
func TestSwitchLaneClamp(t *testing.T) {
	p := DefaultLaneParams
	med := p.ExpectMedium.Cost / p.ExpectMedium.Dur.Hours() // 4 points/h
	sl := p.switchLane(57, "", 0, LaneStats{})
	if !sl.clamped || !near1(sl.rate, 2*med, 1e-9) || sl.class != ExpectMedium || sl.dur != p.ExpectMedium.Dur || sl.maxPts != p.ExpectMedium.Cost {
		t.Fatalf("no --expect at 57: %+v", sl)
	}
	sl = p.switchLane(9, ExpectLong, 3*time.Hour, LaneStats{})
	if sl.clamped || sl.rate != 9 || sl.maxPts != 0 {
		t.Fatalf("long at 9 (under 2 × 4.7): %+v", sl)
	}
}
