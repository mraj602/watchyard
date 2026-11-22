package calib

// Margins replays the ewmax corrector over a seat's readings (any order) with the meter that
// the picker uses, f in points per million units and the ratio (setup.md 8.5, Meter
// margins): the 5-hour and weekly factors k5 and k7. The margins at the pick are k5 × the
// meter's 5-hour points since the reading and k7 × its weekly points.
func Margins(rs []Reading, m *Meter, f, ratio float64) (k5, k7 float64) {
	if ratio <= 0 {
		ratio = FallbackRatio
	}
	s := append([]Reading(nil), rs...)
	SortReadings(s)
	c := NewCorrector(ModeEWMax)
	var t Tracker
	for _, r := range s {
		st := t.Add(r, m)
		if p := st.Five; p != nil {
			add5 := f * p.U / 1e6
			c.Update("", add5, p.D5-add5, 0, 0, false, true)
		}
		if p := st.Seven; p != nil {
			add7 := f * p.U / 1e6 / ratio
			c.Update("", 0, 0, add7, p.D7-add7, true, false)
		}
		// A reset or a confirmed drop between two used readings leaves that factor as it was.
	}
	k := c.M[""]
	return k[0], k[1]
}
