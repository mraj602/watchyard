// Package projection is the one forecast Smart Account Switch (internal/mover) and the lanes
// picker (internal/picker) both use (setup.md 8.6, Projection): where an account's 5-hour and
// weekly levels will be at its resets at a burn rate, so a new lane is never placed where the
// switch would move it off again.
//
// Depends on: nothing inside the module.
package projection

import "math"

// Levels projects the 5-hour level l5 and the weekly level l7 at r5 5-hour points per hour:
// P5 = l5 + r5 × h5 (h5: hours to the 5-hour reset) and P7 = l7 + r5/ratio × min(h7, span)
// (h7: hours to the weekly reset; span: how far the weekly projection looks at most).
func Levels(l5, l7, r5, ratio, h5, h7, span float64) (p5, p7 float64) {
	if ratio <= 0 {
		ratio = 7.2
	}
	r5 = math.Max(0, r5)
	return l5 + r5*math.Max(0, h5), l7 + r5/ratio*math.Max(0, math.Min(h7, span))
}

// Over reports whether a projection is over its caps less a reserve (pins) and a margin.
func Over(p5, p7, cap5, cap7, reserve5, reserve7, margin float64) bool {
	return p5 > math.Max(0, cap5-reserve5)-margin || p7 > math.Max(0, cap7-reserve7)-margin
}

// The drain ramp (setup.md 8.5, Drain aim): a draining account's weekly aim is held at
// DrainAimFar while its weekly reset is more than DrainRampH hours away, then rises in a
// straight line to 100 at the reset:
//
//	aim7 = 96 + 4 × (1 − h/6)   for h < 6 hours to the weekly reset
//
// Far from the reset, room is cheap to keep (it can still be used later) and a burst would stop
// lanes for days; near it, every point not used is lost and a stop lasts only until the reset.
// The 5-hour ceiling does the same in a draining 5-hour window: 98 until DrainRamp5H hours
// before its reset, then up to 100 at the reset. Both are ceilings before the pin reserve and
// the lane fit (a lane that would land over the landing guard, or hit 100 before the reset,
// is still refused).
const (
	DrainAimFar  = 96.0
	DrainRampH   = 6.0
	DrainCeil5   = 98.0
	DrainRamp5H  = 2.0
	DrainCapMax7 = 99.0 // the switch's weekly cap for a draining account goes no higher
)

// DrainAim7 is the weekly level a draining account aims at with h hours to its weekly reset
// (h < 0: unknown, the far aim).
func DrainAim7(h float64) float64 {
	return ramp(DrainAimFar, 100, DrainRampH, h)
}

// DrainCeil5At is a draining account's 5-hour ceiling with h hours to its 5-hour reset.
func DrainCeil5At(h float64) float64 {
	return ramp(DrainCeil5, 100, DrainRamp5H, h)
}

func ramp(lo, hi, over, h float64) float64 {
	if h < 0 || h >= over || math.IsNaN(h) {
		return lo
	}
	return lo + (hi-lo)*(1-h/over)
}

// DrainCaps are the switch's caps c5, c7 for a draining account with h5 and h7 hours to its
// resets: raised to the 5-hour aim (the ceiling less DrainMargin5, 96 rising to 98) and to the
// weekly aim (at most DrainCapMax7), never lowered.
func DrainCaps(c5, c7, h5, h7 float64) (float64, float64) {
	return math.Max(c5, DrainCeil5At(h5)-DrainMargin5), math.Max(c7, math.Min(DrainAim7(h7), DrainCapMax7))
}

// DrainMargin5 is the picker's default 5-hour drain margin (LaneParams.DrainMargin5h): the
// switch's 5-hour cap for a draining account is the picker's 5-hour drain aim.
const DrainMargin5 = 2.0

// PinSafety is how many times the pinned sessions' projected burn the scaled reserve keeps, and
// PinBaseRate the burn assumed per pinned session when less is measured, 5-hour points per hour
// (a coordinator's awake base, 5% of a 5-hour window per 5 hours).
const (
	PinSafety   = 2.0
	PinBaseRate = 1.0
)

// PinReserve scales a full pin reserve (k × pins, points of a window) to what the pins can
// plausibly burn before that window resets (setup.md 8.6, Pinned sessions):
//
//	min(full, max(1, rate × hours left × PinSafety))
//
// rate in points of that window per hour. h < 0 (the reset is unknown) keeps the full reserve.
func PinReserve(full, rate, h float64) float64 {
	if full <= 0 {
		return 0
	}
	if h < 0 || math.IsNaN(h) || math.IsInf(h, 0) {
		return full
	}
	return math.Min(full, math.Max(1, math.Max(0, rate)*h*PinSafety))
}
