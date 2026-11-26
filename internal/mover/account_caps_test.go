package mover

import (
	"testing"
	"time"
)

func TestAccountOwnCaps(t *testing.T) {
	p := DefaultParams
	now := time.Date(2026, 11, 27, 5, 0, 0, 0, time.UTC)
	a := Account{Key: "k7", W5: Window{Reset: now.Add(2 * time.Hour)}, W7: Window{Reset: now.Add(9 * time.Hour)}}
	if c5, c7 := p.caps(a, now); c5 != p.Cap5 || c7 != p.Cap7 {
		t.Fatalf("no own caps: got %v/%v", c5, c7)
	}
	a.Cap5, a.Cap7 = 99, 100
	if c5, c7 := p.caps(a, now); c5 != 99 || c7 != 100 {
		t.Fatalf("own caps: got %v/%v, want 99/100", c5, c7)
	}
	a.Drain = true
	if c5, _ := p.caps(a, now); c5 != 99 {
		t.Fatalf("draining with own 5h cap: got %v, want 99", c5)
	}
	a.Cap5 = 120
	if c5, _ := p.caps(Account{Cap5: 120}, now); c5 != 100 {
		t.Fatalf("own cap over 100: got %v", c5)
	}
	if c5, _ := p.caps(Account{Cap5: 99, Protected: true}, now); c5 != p.Cap5Protected {
		t.Fatalf("protected keeps its cap: got %v", c5)
	}
}
