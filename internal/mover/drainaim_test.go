package mover

import (
	"testing"
	"time"
)

// The switch's weekly cap for a draining account follows account.drain_aim_7d and
// drain_cap_max_7d (claude-4: owner drain, weekly reset 62 h away).
func TestDrainCapsFollowKeys(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	a := Account{Drain: true, W5: Window{Reset: now.Add(4 * time.Hour)}, W7: Window{Reset: now.Add(62 * time.Hour)}}
	p := DefaultParams
	if _, c7 := p.caps(a, now); c7 != 98 {
		t.Fatalf("default cap7 %v, want 98 (the aggressive cap, aim 96)", c7)
	}
	p.DrainAim7, p.DrainCapMax7 = 100, 100
	if _, c7 := p.caps(a, now); c7 != 100 {
		t.Fatalf("keys at 100: cap7 %v, want 100", c7)
	}
	p.DrainAim7, p.DrainCapMax7 = 100, 99
	if _, c7 := p.caps(a, now); c7 != 99 {
		t.Fatalf("cap ceiling 99: cap7 %v", c7)
	}
}
