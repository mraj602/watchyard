package mover

import (
	"strings"
	"testing"
	"time"
)

// Live 2026-11-26 01:00-01:04 (claude-4 owner-drained, 5h cap 97): two moves in filled it to
// its cap, and the next pass's drift moved a third lane out, 4 minutes before its 5h reset.
// A move into a draining account keeps DrainDestMargin under its caps, and a 5h trigger whose
// hit is within ResetSkip of the reset is not acted on (setup.md 8.6).
func TestDrainDestMargin(t *testing.T) {
	// claude-4: 75%, reset in 55 min (cap ramped to 97). Each lane adds rate × 0.917 h:
	// 12 + 12 = 24 -> 97.0 (the cap exactly), 12 alone -> 86.
	world := func(p Params) Input {
		c4 := acc("c4", 75, 55*time.Minute, 30)
		c4.Drain = true
		src := acc("a", 60, 2*time.Hour, 30)
		return Input{Params: p, Accounts: []Account{c4, src},
			Sessions: []Session{lane("s1", "a", 12, true), lane("s2", "a", 12, true), lane("s3", "a", 12, true)}}
	}
	count := func(r Result) (n int) {
		for _, m := range r.Moves {
			if m.To == "c4" {
				n++
			}
		}
		return n
	}
	off := DefaultParams
	off.DrainDestMargin = 0
	if n := count(plan(world(off))); n != 2 {
		t.Fatalf("no margin: %d moves into c4, want 2 (fills it to 97)", n)
	}
	r := plan(world(DefaultParams))
	if n := count(r); n != 1 {
		t.Fatalf("margin 2: %d moves into c4, want 1 (second would reach 97 > 95)", n)
	}
	// The source side keeps none: a draining source triggers at its cap, not 2 under it.
	src := acc("c4", 60, 2*time.Hour, 30)
	src.Drain = true
	pr := DefaultParams.Project(src, nil, 0, t0)
	if m5, _ := DefaultParams.margins(pr); m5 != 0 {
		t.Fatalf("draining source margin %v, want 0", m5)
	}
}

func TestNearResetSkip(t *testing.T) {
	// claude-4 draining at 78%, 24 points/h: hit 100% in 55 min.
	world := func(reset time.Duration) Input {
		c4 := acc("c4", 78, reset, 30)
		c4.Drain, c4.Rate = true, 24
		return Input{Accounts: []Account{c4, acc("b", 20, 2*time.Hour, 30)},
			Sessions: []Session{lane("s", "c4", 24, true)}}
	}
	// Reset 4 min after the hit: skipped, whatever the cap.
	r := plan(world(59 * time.Minute))
	if got := moved(r); len(got) != 0 {
		t.Fatalf("hit 4 min before the reset: moved %v", got)
	}
	if why := skipWhy(r, "s"); !strings.Contains(why, "before its reset") {
		t.Fatalf("skip reason %q", why)
	}
	// Reset 3 h away: the same hit is acted on (a near hit, within UrgentWithin).
	if got := moved(plan(world(3 * time.Hour))); got["s"] != "b" {
		t.Fatalf("hit well before the reset: moved %v", got)
	}
	// Turned off: moves as before.
	p := DefaultParams
	p.ResetSkip = 0
	in := world(59 * time.Minute)
	in.Params = p
	if got := moved(plan(in)); got["s"] != "b" {
		t.Fatalf("ResetSkip 0: moved %v", got)
	}
	// A limit reached now (Hard) is never skipped.
	h := world(59 * time.Minute)
	h.Accounts[0].W5.Level = 100
	if got := moved(plan(h)); got["s"] != "b" {
		t.Fatalf("at the limit: moved %v", got)
	}
}
