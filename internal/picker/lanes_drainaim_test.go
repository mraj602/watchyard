package picker

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/projection"
)

// claude-4, owner-drained until the weekly reset (Sun 07:30) 62 h away: weekly 96%, 5-hour 1%,
// ratio about 8.9, no pins. By default the aim is the far 96 (room 0); with account.drain_aim_7d
// and drain_cap_max_7d at 100 the week is filled to 100 now (setup.md 8.5, Drain aim).
func TestDrainAimKeysClaude4(t *testing.T) {
	run := func(aim, capMax float64) LaneRow {
		s := dworld()
		s[dZ] = dsmp(96, 1, 62, 4)
		in := laneIn(s, 1)
		in.Params = DefaultLaneParams
		in.Params.DrainAim7d, in.Params.DrainCapMax7d = aim, capMax
		in.Mover = &MoverView{Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, WeekSpanH: 5, Units: map[string]float64{},
			ExpectFloorH: 0.5, Hysteresis: 5, DrainAim7: aim, DrainCapMax7: capMax}
		for i := range in.Accounts {
			a := &in.Accounts[i]
			if a.Key == dZ {
				a.Ratio, a.Protected = 8.9, false
				a.DrainUntil = lnow.Add(62*time.Hour - 10*time.Minute)
			}
		}
		return rowOf(rank(t, in), dZ)
	}
	def := run(0, 0)
	if !def.Drain.On || def.Drain.Target > 97.1 {
		t.Fatalf("default keys: %+v, want target about 96 + margin", def.Drain)
	}
	if def.Drain.AimNow < 95.9 || def.Drain.AimNow > 96.1 || def.Drain.Capacity > 0 {
		t.Fatalf("default keys: aim %.1f capacity %v, want 96 and no room", def.Drain.AimNow, def.Drain.Capacity)
	}
	row := run(100, 100)
	if !row.Drain.On || row.Skip || row.Drain.Target < 99.9 {
		t.Fatalf("keys at 100: skip %v %q drain %+v", row.Skip, row.Reason, row.Drain)
	}
	if row.Drain.AimNow < 98.9 || row.Drain.Capacity <= 0 {
		t.Fatalf("keys at 100: aim %.1f capacity %v, want 99 (100 less the 1-point drain margin) and room", row.Drain.AimNow, row.Drain.Capacity)
	}
}

func TestDrainAimKeysProjection(t *testing.T) {
	near := func(a, b float64) bool { return a > b-1e-9 && a < b+1e-9 }
	if got := projection.DrainAim7With(0, 62); !near(got, 96) {
		t.Errorf("default far aim %v", got)
	}
	if got := projection.DrainAim7With(100, 62); !near(got, 100) {
		t.Errorf("aim at 100 %v", got)
	}
	// the ramp still rises from the owner's aim; an aim over the ramp value is kept
	if got := projection.DrainAim7With(90, 3); !near(got, 95) {
		t.Errorf("ramp from 90 at 3 h: %v", got)
	}
	if got := projection.DrainAim7With(99, 6); !near(got, 99) {
		t.Errorf("aim 99 at 6 h: %v", got)
	}
	if _, c7 := projection.DrainCaps(95, 98, 3, 62); !near(c7, 98) {
		t.Errorf("default cap7 %v", c7)
	}
	if _, c7 := projection.DrainCapsWith(95, 98, 3, 62, 100, 100); !near(c7, 100) {
		t.Errorf("cap7 at 100: %v", c7)
	}
	if _, c7 := projection.DrainCapsWith(95, 98, 3, 62, 100, 0); !near(c7, 99) {
		t.Errorf("cap7 default ceiling: %v", c7)
	}
}
