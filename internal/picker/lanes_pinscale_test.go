package picker

import (
	"math"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/projection"
)

// The scaled pin reserve and the drain ramp on claude-3's numbers (worker, 2026-11-25 22:30):
// 88% of the week, 2 live pinned sessions burning about 5.7 five-hour points an hour together,
// ratio 9.1, the owner draining it until 02:45 and its week resetting Thu 03:30. The flat
// reserve (10% of the week) held it at 88 ("7d 88% >= 88") and ended the owner's drain at once.
// Scaled to what the pins can burn before the reset, twice over, the reserve shrinks as the
// reset nears and the drain uses the rest up to the ramped aim (setup.md 8.5 Drain aim, 8.6
// Pinned sessions).
func TestPinReserveScaledClaude3(t *testing.T) {
	const ratio, pinsRate = 9.1, 5.7
	for _, c := range []struct {
		name        string
		h7, h5      float64 // hours to the weekly and 5-hour resets
		r7, r5, aim float64 // the reserve and the drain's weekly aim now
		drain       bool
	}{
		// 5.7/9.1 × 5 × 2 = 6.26 (of 10); aim 96 + 4 × (1 − 5/6) − 6.26 = 90.40
		{"22:30, 5 h to the reset", 5, 2, 6.264, 20, 90.40, true},
		// 5.7/9.1 × 2 × 2 = 2.51; aim 98.67 − 2.51 = 96.16; the 5-hour window resets in 1 h: 5.7 × 1 × 2 = 11.4 (of 20)
		{"01:30, 2 h to the reset", 2, 1, 2.505, 11.4, 96.16, true},
		// 1.25; the ramp's 99.33 is over the ceiling 100 less the reserve and the drain margin:
		// aim 100 − 1.25 − 1 = 97.75
		{"02:30, 1 h", 1, 0.5, 1.253, 5.7, 97.75, true},
		// the floor of 1 point; aim 100 − 1 − 1 = 98
		{"03:00, 30 min", 0.5, 0.5, 1, 5.7, 98, true},
		// far from the reset: the full reserve; aim 96 − 10 = 86, under 88: no drain room
		{"40 h out: the full reserve", 40, 2, 10, 20, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := dworld()
			s[dZ] = dsmp(88, 30, c.h7, c.h5)
			in := laneIn(s, 1)
			in.Params = DefaultLaneParams
			for i := range in.Accounts {
				a := &in.Accounts[i]
				if a.Key != dZ {
					continue
				}
				a.Ratio, a.Protected = ratio, false
				a.DrainUntil = lnow.Add(time.Duration(math.Max(0.25, c.h7-0.75) * float64(time.Hour)))
				a.Pins, a.PinReserve5, a.PinReserve7 = 2, 20, 10
				a.PinUnits = pinsRate / in.Params.account(*a).F5
				scalePins(a, in.Params, lnow)
				if !near1(a.PinReserve7, c.r7, 0.01) || !near1(a.PinReserve5, c.r5, 0.01) || a.PinFull7 != 10 || a.PinFull5 != 20 {
					t.Fatalf("reserve 5h %.3f 7d %.3f (full %v/%v), want %.3f %.3f", a.PinReserve5, a.PinReserve7, a.PinFull5, a.PinFull7, c.r5, c.r7)
				}
			}
			r := rank(t, in)
			d := drainOf(t, r, dZ)
			if d.On != c.drain {
				t.Fatalf("drain %+v, want on %v (reason %q)", d, c.drain, rowOf(r, dZ).Reason)
			}
			if !c.drain {
				return
			}
			// It takes lanes (a lane estimated to outlast the week goes elsewhere first: runway).
			if row := rowOf(r, dZ); !near1(d.AimNow, c.aim, 0.01) || d.Capacity <= 0 || row.Skip {
				t.Errorf("aim %.2f (want %.2f) capacity %d skip %v %q: %+v", d.AimNow, c.aim, d.Capacity, row.Skip, row.Reason, d)
			}
			if !near1(d.AimEnd, 98, 1e-9) { // 100 less the reserve's floor (1) and the drain margin
				t.Errorf("aim at the reset %.2f, want 98", d.AimEnd)
			}
		})
	}
	// The ramp itself.
	for h, want := range map[float64]float64{-1: 96, 0: 100, 1.5: 99, 3: 98, 6: 96, 30: 96} {
		if got := projection.DrainAim7(h); !near1(got, want, 1e-9) {
			t.Errorf("DrainAim7(%v) = %v, want %v", h, got, want)
		}
	}
	for h, want := range map[float64]float64{0: 100, 1: 99, 2: 98, 5: 98} {
		if got := projection.DrainCeil5At(h); !near1(got, want, 1e-9) {
			t.Errorf("DrainCeil5At(%v) = %v, want %v", h, got, want)
		}
	}
}
