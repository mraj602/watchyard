package mover

import (
	"math"
	"testing"
	"time"
)

// The switch's side of claude-3 (2026-11-25 22:30): 88% of the week, 2 live pins burning 5.7
// five-hour points an hour together, ratio 9.1, its week resetting in 5 h, drained by the
// owner. The flat reserve kept its weekly cap at 98 − 10 = 88; scaled to what the pins can
// burn by the reset (twice over) and with the drain ramp, the cap rises as the reset nears.
func TestScaledPinReserveClaude3(t *testing.T) {
	now := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	p := DefaultParams
	for _, c := range []struct {
		h7, h5   float64
		r5, r7   float64
		cap5, c7 float64 // the caps less the reserve
	}{
		// 5.7/9.1 × 5 × 2 = 6.26; weekly cap max(98, aim 96.67) − 6.26
		{5, 2, 20, 6.264, 96 - 20, 98 - 6.264},
		// 2 h: 2.51; the 5-hour window resets in 1 h: 5.7 × 2 = 11.4; 5h cap 98 + 2 × 0.5 − 2 = 97
		{2, 1, 11.4, 2.505, 97 - 11.4, 98.667 - 2.505},
		// 30 min: the floor of 1; weekly cap at most 99
		{0.5, 0.5, 5.7, 1, 97.5 - 5.7, 99 - 1},
		// 40 h out: the full reserve, the flat caps (the ramp starts 6 h out)
		{40, 2, 20, 10, 96 - 20, 98 - 10},
	} {
		a := Account{Key: "acct:c3/o", Label: "claude-3", Ratio: 9.1, Pins: 2, DrainUntil: now.Add(4 * time.Hour),
			W5: Window{Level: 30, Reset: now.Add(time.Duration(c.h5 * float64(time.Hour)))},
			W7: Window{Level: 88, Reset: now.Add(time.Duration(c.h7 * float64(time.Hour)))}}
		a.Reserve5, a.Reserve7 = p.ScaledPinReserve(a, 5.7, now)
		if math.Abs(a.Reserve5-c.r5) > 0.01 || math.Abs(a.Reserve7-c.r7) > 0.01 {
			t.Errorf("h7 %v: reserve %.3f/%.3f, want %.3f/%.3f", c.h7, a.Reserve5, a.Reserve7, c.r5, c.r7)
		}
		pr := p.Project(a, nil, 0, now)
		if math.Abs(pr.Cap5-c.cap5) > 0.01 || math.Abs(pr.Cap7-c.c7) > 0.01 {
			t.Errorf("h7 %v: caps %.2f/%.2f, want %.2f/%.2f", c.h7, pr.Cap5, pr.Cap7, c.cap5, c.c7)
		}
	}
	// Idle pins: the baseline of 1 point an hour each.
	a := Account{Pins: 2, Ratio: 9.1, W5: Window{Reset: now.Add(time.Hour)}, W7: Window{Reset: now.Add(time.Hour)}}
	if r5, r7 := p.ScaledPinReserve(a, 0, now); math.Abs(r5-4) > 1e-9 || math.Abs(r7-1) > 1e-9 {
		t.Errorf("idle pins: %v/%v, want 4/1", r5, r7)
	}
}
