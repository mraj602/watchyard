package mover

import (
	"strings"
	"testing"
	"time"
)

// Live pre24 2026-11-27 02:47-02:52: owner-drained claude-4, claude-6 and claude-7 were all
// over their 5-hour caps (claude-4 at its cap until its 02:50 reset). The near-hit fallback
// ("best available") then swapped low-burn sessions between claude-6 and claude-7: 7->6
// "fits for 41m" against the 36m claude-7 had left, 6->7 "fits for 37m" against 36m, eight
// moves of 1.1-5.6 points/h, each re-reading a whole context. A best-available destination
// must last BestMargin longer than the source (and BestFloor), must not be an account this
// pass moves burners off, and must not have sent burn to the source within NoReturn.
func TestBestAvailableNoSwap(t *testing.T) {
	drained := func(key string, l5, rate float64, reset5 time.Duration) Account {
		a := acc(key, l5, reset5, 60)
		a.DrainUntil, a.Rate = t0.Add(10*time.Hour), rate
		return a
	}
	world := func(c4 Account) Input {
		// claude-6: 60%, 60 points/h: 100% in 40 min. claude-7: 50%, 64 points/h: 100% in 47 min,
		// its 96% cap with a 1.5 points/h lane in 42 min (the old check: 42 >= 40, a "best available").
		c6 := drained("c6", 60, 60, 4*time.Hour)
		c7 := drained("c7", 50, 64, 4*time.Hour)
		ss := []Session{
			lane("6a", "c6", 1.5, true), lane("6b", "c6", 1.7, true), lane("6c", "c6", 1.8, true), lane("6d", "c6", 5.6, true),
			lane("7a", "c7", 1.4, true), lane("7b", "c7", 2.5, true), lane("7c", "c7", 3.9, true), lane("7d", "c7", 5.2, true),
		}
		return Input{Accounts: []Account{c4, c6, c7}, Sessions: ss}
	}
	swaps := func(r Result) []string {
		var bad []string
		for _, m := range r.Moves {
			if (m.From == "c6" && m.To == "c7") || (m.From == "c7" && m.To == "c6") {
				bad = append(bad, m.Session+" "+m.From+"->"+m.To+": "+m.Reason)
			}
		}
		return bad
	}

	// 02:47: claude-4 at its cap, resetting in 3 min: no destination; nothing swaps.
	c4 := drained("c4", 100, 0, 3*time.Minute)
	r := plan(world(c4))
	if bad := swaps(r); len(bad) > 0 {
		t.Fatalf("6<->7 swaps with claude-4 at its cap:\n%s", strings.Join(bad, "\n"))
	}
	for _, m := range r.Moves {
		if m.To != "" {
			t.Errorf("%s moved %s->%s with every account about to hit: %s", m.Session, m.From, m.To, m.Reason)
		}
	}

	// The next pass, after a 7->6 move 2 minutes ago: still no 6->7 (nor 7->6).
	in := world(c4)
	in.Recent = []Record{{Session: "7x", From: "c7", To: "c6", At: t0.Add(-2 * time.Minute), Rate: 1.4, RateKnown: true}}
	if bad := swaps(plan(in)); len(bad) > 0 {
		t.Fatalf("6<->7 swaps after a recent 7->6 move:\n%s", strings.Join(bad, "\n"))
	}
	if !DefaultParams.sentTo(in, in.Accounts[2], "c6") || DefaultParams.sentTo(in, in.Accounts[1], "c7") {
		t.Fatalf("sentTo: a 7->6 move 2 min ago keeps c7 off c6's best available, not c6 off c7's")
	}
	in.Now = t0.Add(time.Hour) // past NoReturn
	if DefaultParams.sentTo(in, in.Accounts[2], "c6") {
		t.Fatalf("sentTo past NoReturn")
	}

	// 02:50: claude-4 reset (fresh window, 0%, 20 points/h of its own): it takes the burners.
	fresh := drained("c4", 0, 20, 0)
	fresh.W5.Reset = time.Time{}
	r = plan(world(fresh))
	if bad := swaps(r); len(bad) > 0 {
		t.Fatalf("6<->7 swaps with claude-4 fresh:\n%s", strings.Join(bad, "\n"))
	}
	n := 0
	for _, m := range r.Moves {
		if m.To == "c4" {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("claude-4 fresh took nothing: %+v", r.Moves)
	}

	// 02:52: claude-4 itself over its cap by its reset but lasting hours: a 23.7 points/h
	// burner off claude-7 still goes there as the best available (the good move).
	busy := drained("c4", 0, 30, 5*time.Hour) // 30 points/h: 100% in 3h20m, over 96 by its reset
	gw := world(busy)
	gw.Sessions = append(gw.Sessions, lane("7big", "c7", 23.7, true))
	gw.Accounts[2].Rate = 88
	r = plan(gw)
	if got := moved(r); got["7big"] != "c4" {
		t.Fatalf("the 23.7 points/h burner: moves %v, want onto c4 (best available)\n%+v", got, r.Moves)
	}
	if bad := swaps(r); len(bad) > 0 {
		t.Fatalf("6<->7 swaps beside the good move:\n%s", strings.Join(bad, "\n"))
	}

	// MinBurn holds on a near hit too: at 8 none of the 1.4-5.6 points/h sessions is a candidate.
	mb := world(fresh)
	mb.Params = DefaultParams
	mb.Params.MinBurn = 8
	r = plan(mb)
	if len(r.Moves) > 0 {
		t.Fatalf("MinBurn 8: moved %+v", r.Moves)
	}
	if why := skipWhy(r, "6d"); !strings.Contains(why, "under the 8.0") {
		t.Fatalf("MinBurn 8: 6d skip %q", why)
	}
}
