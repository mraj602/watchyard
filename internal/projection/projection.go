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
