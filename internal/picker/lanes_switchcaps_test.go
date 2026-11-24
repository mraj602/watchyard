package picker

import (
	"strings"
	"testing"
	"time"
)

// Live on pre4.1 (2026-11-24): four lanes were placed on claude-3 at 81-83% of its week and
// each was moved off 4-20 minutes later. With 2 live pins the switch's weekly cap was 88, so
// claude-3 had ~6 weekly points of room, not the ~16 to 98 the at-risk test counted.
func TestLanesTargetsAreSwitchCaps(t *testing.T) {
	const x = "claude-3"
	mk := func(pins int, mv *MoverView) LaneRow {
		s := world(30, 20)
		s[x] = smp(82, 10, 2, 26.5)
		in := laneIn(s, 1)
		in.Params = DefaultLaneParams
		in.Mover = mv
		for i := range in.Accounts {
			if in.Accounts[i].Key == x {
				in.Accounts[i].Ratio = 9.1
				in.Accounts[i].Pins, in.Accounts[i].PinReserve5, in.Accounts[i].PinReserve7 = pins, 10*float64(pins), 5*float64(pins)
			}
		}
		return rowOf(rank(t, in), x)
	}
	// No pins, no switch: 16 points to 98 need 7.4 h at full speed; 26.5 h <= 4 × 7.4: at risk.
	if r := mk(0, nil); r.Class != ClassAtRisk {
		t.Errorf("no pins: class %q, want at risk", r.Class)
	}
	// 2 pins: 6 points to 88 at 78 points per window need 3.5 h; 26.5 h > 4 × 3.5: not at risk.
	if r := mk(2, nil); r.Class != ClassAggressive {
		t.Errorf("2 pins: class %q, want aggressive", r.Class)
	}
	// The switch's caps bound the target the same way (weekly cap 90: 8 points of room).
	mv := &MoverView{Cap5: 95, Cap7: 90, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{}}
	if r := mk(0, mv); r.Class != ClassAggressive || r.WeeklyCap != 90 {
		t.Errorf("weekly cap 90: class %q, weekly cap %v", r.Class, r.WeeklyCap)
	}
	// Its limits are the switch's caps less the reserve: 2 pins, caps 95/98 -> 5h 75, 7d 88.
	mv.Cap7 = 98
	if r := mk(2, mv); r.FiveHourCap != 75 || r.WeeklyCap != 88 {
		t.Errorf("caps 5h %v 7d %v, want 75 and 88", r.FiveHourCap, r.WeeklyCap)
	}
}

// A protected account in drain uses the week's room up to the switch's protected weekly cap,
// not 98 (live: claude-5 at 85% with the protected cap at 80 showed 12% of drain room).
func TestLanesProtectedDrainStopsAtSwitchCap(t *testing.T) {
	const x = "claude-5"
	mk := func(mv *MoverView) LaneRow {
		s := world(30, 20)
		s[x] = smp(85, 0, 2, 2)
		in := laneIn(s, 1)
		in.Params = DefaultLaneParams
		in.Mover = mv
		return rowOf(rank(t, in), x)
	}
	if r := mk(nil); r.Class != ClassDrain || !r.Drain.On || r.Drain.Target != 98 {
		t.Fatalf("switch off: class %q drain %+v, want drain to 98", r.Class, r.Drain)
	}
	mv := &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{}}
	if r := mk(mv); r.Drain.On || r.Class == ClassDrain {
		t.Errorf("switch on (protected cap 80): class %q drain %+v, want no drain", r.Class, r.Drain)
	}
	mv.Cap7Protected = 92
	r := mk(mv)
	if !r.Drain.On || r.Drain.Target != 92 || r.Drain.RoomPct > 92-85 {
		t.Errorf("protected cap 92: drain %+v, want room under 7 to 92", r.Drain)
	}
	if want := "of week left"; !strings.Contains(laneReason(Ranked{Kind: "protected"}, r, 0, "", false, lnow), "7% "+want) {
		t.Errorf("reason %q, want 7%% %s", laneReason(Ranked{Kind: "protected"}, r, 0, "", false, lnow), want)
	}
}

// A new lane's burn in the switch-projection check is the measured lane burn, not its size
// class's cost over its wall time (live: a "medium" lane added ~4 points/h; Opus lanes burn
// 12-26).
func TestLanesMoverMeasuredLaneRate(t *testing.T) {
	s := world(40, 20)
	in := laneIn(s, 1)
	in.Params = DefaultLaneParams
	in.Params.UseEstimate = true
	in.Expects = []LaneExpect{{Class: ExpectMedium}}
	first := rank(t, in).Picks[0].Key
	mv := &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{}}
	mv.Units[first] = 2 / DefaultLaneParams.F5
	in.Mover = mv
	for i := range in.Accounts {
		in.Accounts[i].MoveUnits = mv.Units[in.Accounts[i].Key]
	}
	// Quiet (2 points/h) with a medium lane at its class rate: 10 + (2 + ~4) × 3 h is under 95.
	if r := rank(t, in); r.Picks[0].Key != first {
		t.Fatalf("quiet account skipped: %+v", rowOf(r, first))
	}
	// Lanes here burn 30 points/h: 10 + 32 × 3 h = 106, over 95.
	in.Stats = LaneStats{Rate: 30, NRate: 20}
	r := rank(t, in)
	row := rowOf(r, first)
	if !row.Skip || !strings.Contains(row.Reason, "at 32 points/h") {
		t.Fatalf("row %+v", row)
	}
	// A lane rate under the class's keeps the class's (the larger).
	in.Stats = LaneStats{Rate: 0.5, NRate: 20}
	if r := rank(t, in); r.Picks[0].Key != first {
		t.Errorf("a low measured rate lowered the lane's burn: %+v", rowOf(r, first))
	}
}

// measureLanes: the lane burn rate is per model, over the wall time of ended lanes and of lanes
// running for 30 minutes or more.
func TestMeasureLaneRates(t *testing.T) {
	var led []leasesLease
	var local []LaneLease
	f5 := 1e-6
	for i := 0; i < 14; i++ {
		at := lnow.Add(-time.Duration(2+i) * time.Hour)
		model, rate := "opus", 12+float64(i) // 12..23 points/h over an hour
		if i >= 12 {
			model, rate = "sonnet", 3
		}
		led = append(led, leasesLease{at: at, launched: at, ended: at.Add(time.Hour), model: model})
		local = append(local, LaneLease{Key: "k", T0: at, Seen: true, Ended: true, Burned: rate / f5})
	}
	st := measureLanes(toLedger(led), local, nil, map[string]float64{"k": f5}, "opus", lnow)
	if st.NRate != 12 || !near1(st.Rate, 12+0.75*11, 1e-6) {
		t.Errorf("opus rate %+v, want the p75 of 12..23 (20.25)", st)
	}
	// A running lane younger than 30 minutes is left out (its startup burst); one older counts.
	young := lnow.Add(-10 * time.Minute)
	old := lnow.Add(-time.Hour)
	led2 := append(led, leasesLease{at: young, launched: young, model: "opus"}, leasesLease{at: old, launched: old, model: "opus"})
	local2 := append(local, LaneLease{Key: "k", T0: young, Seen: true, Burned: 50 / f5}, LaneLease{Key: "k", T0: old, Seen: true, Burned: 40 / f5})
	if st := measureLanes(toLedger(led2), local2, nil, map[string]float64{"k": f5}, "opus", lnow); st.NRate != 13 {
		t.Errorf("running lanes: %+v, want 13 rates (the young one left out)", st)
	}
	// Too few lanes of any model: no measured rate.
	if st := measureLanes(toLedger(led[:5]), local[:5], nil, map[string]float64{"k": f5}, "", lnow); st.Rate != 0 || st.NRate != 0 {
		t.Errorf("five lanes: %+v", st)
	}
}

// The switch's weekly level is the picker's measured weekly level, from the same readings
// (LaneInput.Live and the lanes table share laneLive): one pass cannot read two levels.
func TestLiveIsPickerMeasuredLevel(t *testing.T) {
	s := world(40, 20)
	in := laneIn(s, 1)
	in.Params = DefaultLaneParams
	for i := range in.Accounts {
		in.Accounts[i].Meter = LaneMeter{M5: 3e6, M7: 2e6, KeepH: true}
		in.Accounts[i].K7, in.Accounts[i].Extra5 = 0.5, 4
	}
	in.Leases = []LaneLease{{Key: "claude-2", T0: lnow.Add(-time.Minute), SID: "a"}}
	r := rank(t, in)
	accs, _ := in.Live()
	for _, a := range accs {
		row := rowOf(r, a.Key)
		if !near1(a.Weekly, row.WeeklyM, 1e-9) {
			t.Errorf("%s: switch %v, picker measured %v", a.Key, a.Weekly, row.WeeklyM)
		}
		if !near1(a.WeeklyRead+a.WeeklyMeter, a.Weekly, 1e-9) {
			t.Errorf("%s: parts %v + %v != %v", a.Key, a.WeeklyRead, a.WeeklyMeter, a.Weekly)
		}
	}
	if row := rowOf(r, "claude-2"); row.Weekly <= row.WeeklyM {
		t.Errorf("the lease's charge should be in the picker's weekly only: %v vs %v", row.Weekly, row.WeeklyM)
	}
}
