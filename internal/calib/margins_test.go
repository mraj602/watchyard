package calib

import (
	"math"
	"testing"
	"time"
)

// A meter that reads half the truth gives k5 = 1 (relative error 100 %); a right one gives 0;
// a reset between two readings leaves k5 as it was, and older errors decay by 0.7 per reading.
func TestMargins(t *testing.T) {
	var rs []Reading
	var us []Use
	h, w := 0.0, 10.0
	for i := 0; i < 6; i++ {
		at := t0.Add(time.Duration(i) * 30 * time.Minute)
		if i > 0 {
			us = append(us, Use{At: at.Add(-time.Minute), Units: 2e6}) // 2 points at f = 1
			h += 4                                                     // the truth: 4
			w += 0.5
		}
		rs = append(rs, Reading{At: at, Source: "statusline", Five: pct(h), FiveReset: t0.Add(4 * time.Hour), Seven: pct(w), SevenReset: t0.Add(72 * time.Hour)})
	}
	m := NewMeter(us)
	k5, k7 := Margins(rs, m, 1.0, 8.0)
	if math.Abs(k5-1) > 1e-9 {
		t.Fatalf("k5 = %v, want 1", k5)
	}
	// weekly: add7 = 2/8 = 0.25, truth 0.5: error 0.25 over max(0.25, 2/7) = 0.875
	if math.Abs(k7-0.875) > 1e-9 {
		t.Fatalf("k7 = %v, want 0.875", k7)
	}
	if k5, _ := Margins(rs, m, 2.0, 8.0); k5 != 0 {
		t.Fatalf("a right meter: k5 = %v", k5)
	}

	// A reset after the under-reads: the first reading of the new window changes nothing.
	end := rs[len(rs)-1].At
	rs2 := append(append([]Reading(nil), rs...), Reading{At: end.Add(5 * time.Hour), Source: "statusline", Five: pct(1), FiveReset: end.Add(9 * time.Hour)})
	if k5, _ := Margins(rs2, m, 1.0, 8.0); math.Abs(k5-1) > 1e-9 {
		t.Fatalf("after a reset: k5 = %v, want 1", k5)
	}
	// Then three right readings: 1 × 0.7³.
	us2 := append([]Use(nil), us...)
	for i := 1; i <= 3; i++ {
		a := end.Add(5*time.Hour + time.Duration(i)*30*time.Minute)
		us2 = append(us2, Use{At: a.Add(-time.Minute), Units: 2e6})
		rs2 = append(rs2, Reading{At: a, Source: "statusline", Five: pct(1 + 2*float64(i)), FiveReset: end.Add(9 * time.Hour)})
	}
	if k5, _ := Margins(rs2, NewMeter(us2), 1.0, 8.0); math.Abs(k5-0.343) > 1e-9 {
		t.Fatalf("decay: k5 = %v, want 0.343", k5)
	}
	if k5, k7 := Margins(nil, nil, 1, 0); k5 != 0 || k7 != 0 {
		t.Fatal("no readings")
	}
}
