package mover

import (
	"testing"
	"time"
)

// Live 2026-11-27 (claude-7 owner-drained, 5-hour window resetting at 01:30): at 01:28 two
// claude-4 lanes (14.5 and 18.5 points/h) moved onto it at a projected 64% (2 minutes of
// their burn before the reset), and at 01:29 it pulled in another (8.1 points/h counted).
// After the reset its window was 2% with 49 points/h on it: "5h projected 170%", and the
// lanes moved off again by 01:39. A destination resetting within DestResetWithin must also
// fit the window after its reset.
func TestDestResetNextWindow(t *testing.T) {
	world := func(reset time.Duration) Input {
		c7 := acc("c7", 64, reset, 59)
		c7.DrainUntil, c7.Rate, c7.Ratio = t0.Add(13*time.Hour), 10, 7
		c7.W7.Reset = t0.Add(13 * time.Hour)
		c4 := acc("c4", 70, 90*time.Minute, 40) // 70 + 33 × 1.5 = 119.5: a near hit in 55 min
		c5 := acc("c5", 10, time.Hour, 30)      // room for both: 10 + 33 × 1 = 43
		c5.Protected = true
		return Input{Accounts: []Account{c7, c4, c5},
			Sessions: []Session{lane("s12", "c4", 14.5, true), lane("deeplink", "c4", 18.5, true)}}
	}
	// 2 minutes to its reset: claude-7 is no destination for 33 points/h (5 × 43 = 215 next window).
	r := plan(world(2 * time.Minute))
	for id, to := range moved(r) {
		if to == "c7" {
			t.Fatalf("%s moved onto c7 2 min before its reset: %v", id, moved(r))
		}
	}
	if got := moved(r); got["deeplink"] != "c5" || got["s12"] != "c5" {
		t.Fatalf("moves %v, want both onto c5", got)
	}
	// Turned off: the old behaviour (both onto claude-7).
	off := world(2 * time.Minute)
	off.Params = DefaultParams
	off.Params.DestResetWithin = 0
	if got := moved(plan(off)); got["s12"] != "c7" || got["deeplink"] != "c7" {
		t.Fatalf("DestResetWithin 0: moves %v, want both onto c7", got)
	}
	// Its reset 45 min away (beyond DestResetWithin): judged to its reset as before (64 + 28.5
	// × 0.75 = 85), the next window not counted.
	far := plan(world(45 * time.Minute))
	n := 0
	for _, to := range moved(far) {
		if to == "c7" {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("reset 45 min away: nothing onto c7 (%v)", moved(far))
	}
	// A lane expected to end before the reset burns nothing after it.
	short := world(30 * time.Minute) // 1 min, floored to ExpectFloor 30, ends at the reset
	for i := range short.Sessions {
		short.Sessions[i].Expected = time.Minute
	}
	short.Accounts[0].Rate = 0
	pr := DefaultParams.nextWindow(short.Accounts[0], short.Sessions, 0, t0)
	if pr.P5 > 1e-9 {
		t.Fatalf("lanes ending before the reset: next window %v, want 0", pr.P5)
	}
	// A near hit with no other room still takes claude-7 as the best available (it fits for
	// 2 min + about 2 h of its next window, more than the 55 min claude-4 has left).
	nb := world(2 * time.Minute)
	nb.Accounts = nb.Accounts[:2]
	if got := moved(plan(nb)); got["deeplink"] != "c7" {
		t.Fatalf("no other room: moves %v, want deeplink onto c7 (best available)", got)
	}
}

// The 01:29 pull: claude-7 owner-drained, resetting in a minute, pulls in nothing that its next
// window cannot hold; resetting in 3 h with room it does.
func TestDestResetNoPull(t *testing.T) {
	world := func(reset time.Duration, rate float64) Input {
		c7 := acc("c7", 62, reset, 58)
		c7.DrainUntil, c7.Rate, c7.Ratio = t0.Add(13*time.Hour), rate, 7
		c7.W7.Reset = t0.Add(13 * time.Hour)
		src := acc("c2", 30, 3*time.Hour, 40)
		return Input{Accounts: []Account{c7, src}, Sessions: []Session{lane("fix", "c2", 6.7, true)}}
	}
	if got := moved(plan(world(time.Minute, 43))); got["fix"] == "c7" {
		t.Fatalf("pulled into c7 a minute before its reset with 43 points/h on it: %v", got)
	}
	if got := moved(plan(world(time.Minute, 5))); got["fix"] != "c7" {
		t.Fatalf("next window has room (5 + 6.7 points/h): not pulled %v", got)
	}
}
