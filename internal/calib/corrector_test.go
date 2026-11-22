package calib

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The reference's v2_unit section 9: the corrector across a reset.
func TestCorrectorAcrossAReset(t *testing.T) {
	mc := NewCorrector(ModeEWMax)
	mc.Update("claude-2", 5.0, 2.0, 0, 0, false, false)
	before := mc.M["claude-2"]
	mc.Update("claude-2", 0.0, 80.0, 0, 0, true, false)
	if mc.M["claude-2"][0] != before[0] {
		t.Fatalf("5h component changed across a 5h reset: %v -> %v", before, mc.M["claude-2"])
	}
	mc.Update("claude-2", 5.0, 10.0, 0, 0, false, false)
	if !(mc.M["claude-2"][0] > before[0]) {
		t.Fatalf("no normal update without a reset: %v -> %v", before, mc.M["claude-2"])
	}
}

// The reference's docstring: relative error over max(add, floor), capped, positive part only;
// ewmax keeps the decayed maximum; last keeps the last; last_abs keeps points.
func TestCorrectorModes(t *testing.T) {
	c := NewCorrector(ModeEWMax)
	c.Update("a", 5, 2, 0, 0, false, false) // 2/5
	if got := c.M["a"][0]; !near(got, 0.4) {
		t.Fatalf("relative error = %v, want 0.4", got)
	}
	c.Update("a", 1, 1, 0, 0, false, false) // 1/max(1,2) = 0.5 > 0.4*0.7
	if got := c.M["a"][0]; !near(got, 0.5) {
		t.Fatalf("floor: %v, want 0.5", got)
	}
	c.Update("a", 4, -3, 0, 0, false, false) // negative: 0, decayed max 0.35
	if got := c.M["a"][0]; !near(got, 0.35) {
		t.Fatalf("decay: %v, want 0.35", got)
	}
	c.Update("a", 2, 100, 0, 0, false, false) // capped at 3
	if got := c.M["a"][0]; !near(got, 3) {
		t.Fatalf("cap: %v, want 3", got)
	}
	m5, m7 := c.Margin("a", 1.5, 0.2)
	if !near(m5, 4.5) || !near(m7, 0) {
		t.Fatalf("margin = %v %v, want 4.5 0", m5, m7)
	}

	// The weekly floor is floor/7.
	c.Update("a", 0, 0, 0.1, 1.0/7, false, false)
	if got := c.M["a"][1]; !near(got, 0.5) {
		t.Fatalf("weekly floor: %v, want 0.5", got)
	}

	l := NewCorrector(ModeLast)
	l.Update("a", 5, 4, 0, 0, false, false)
	l.Update("a", 5, 1, 0, 0, false, false)
	if got := l.M["a"][0]; !near(got, 0.2) {
		t.Fatalf("last: %v, want 0.2", got)
	}

	abs := NewCorrector(ModeLastAbs)
	abs.Update("a", 5, 4, 1, 0.5, false, false)
	if m5, m7 := abs.Margin("a", 100, 100); !near(m5, 4) || !near(m7, 0.5) {
		t.Fatalf("last_abs margin = %v %v, want 4 0.5", m5, m7)
	}
	abs.Update("a", 5, -2, 1, 0.5, true, false)
	if got := abs.M["a"]; !near(got[0], 4) || !near(got[1], 0.5) {
		t.Fatalf("last_abs across a 5h reset: %v", got)
	}
}

// A reset of one window keeps that factor; the other one updates on the old values (the
// reference's _skip).
func TestCorrectorResetKeepsOneComponent(t *testing.T) {
	c := NewCorrector(ModeEWMax)
	c.Update("a", 2, 2, 1, 1, false, false) // 1, 1
	c.Update("a", 2, 0, 1, 0.5, true, false)
	if got := c.M["a"]; !near(got[0], 1) || !near(got[1], 0.7) {
		t.Fatalf("got %v, want [1 0.7]", got)
	}
	c.Update("a", 2, 6, 1, 3, false, true)
	if got := c.M["a"]; !near(got[0], 3) || !near(got[1], 0.7) {
		t.Fatalf("got %v, want [3 0.7]", got)
	}
}
