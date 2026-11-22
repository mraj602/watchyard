package calib

import "math"

// Corrector is the online correction of the token meter, per account and window (the
// reference's MeterCorrector). At each new reading the caller reports what the meter had
// added since the previous reading and the error (reading minus estimate, positive = the
// meter under-read), and gets margins for the next picks.
//
// Modes: "last", the positive part of the last relative error times the current meter add;
// "ewmax", an exponentially weighted max of recent positive relative errors (Decay per
// update) times the current add; "last_abs", the last positive error in points, added as it
// is. A relative error divides by max(add, Floor) (Floor / 7 for the weekly window), so a
// reading after an idle stretch does not explode it, and is capped at Cap.
type Corrector struct {
	Mode  string
	Decay float64
	Floor float64
	Cap   float64
	M     map[string][2]float64 // per account: the 5-hour and weekly factors
}

// Corrector modes.
const (
	ModeLast    = "last"
	ModeEWMax   = "ewmax"
	ModeLastAbs = "last_abs"
)

// NewCorrector is a corrector with the shipped constants: decay 0.7, floor 2 points, cap 3.
func NewCorrector(mode string) *Corrector {
	return &Corrector{Mode: mode, Decay: 0.7, Floor: 2, Cap: 3, M: map[string][2]float64{}}
}

// Update reports one reading of acc: add5 and add7 the meter's points since the previous
// reading, err5 and err7 the reading minus the estimate. reset5 (reset7): a reset of that
// window fell between the two readings, so its error is no meter error and that factor keeps
// its old value.
func (c *Corrector) Update(acc string, add5, err5, add7, err7 float64, reset5, reset7 bool) {
	old := c.M[acc]
	n := c.next(old, add5, err5, add7, err7)
	if reset5 {
		n[0] = old[0]
	}
	if reset7 {
		n[1] = old[1]
	}
	c.M[acc] = n
}

func (c *Corrector) next(old [2]float64, add5, err5, add7, err7 float64) [2]float64 {
	if c.Mode == ModeLastAbs {
		return [2]float64{math.Max(0, err5), math.Max(0, err7)}
	}
	r5 := math.Min(c.Cap, math.Max(0, err5/math.Max(add5, c.Floor)))
	r7 := math.Min(c.Cap, math.Max(0, err7/math.Max(add7, c.Floor/7)))
	if c.Mode == ModeEWMax {
		r5, r7 = math.Max(r5, old[0]*c.Decay), math.Max(r7, old[1]*c.Decay)
	}
	return [2]float64{r5, r7}
}

// Margin is the margins of acc for a meter add of add5 and add7 points now.
func (c *Corrector) Margin(acc string, add5, add7 float64) (float64, float64) {
	m := c.M[acc]
	if c.Mode == ModeLastAbs {
		return m[0], m[1]
	}
	return m[0] * add5, m[1] * add7
}
