package mover

import (
	"strings"
	"testing"
	"time"
)

// Two owner drains (live pre16, 23:44:50): claude-3 draining until Thu 02:45, at 89% of its
// week, its weekly reset Thu 03:30, no burn, a 5-hour pin reserve of 11.7; claude-4 draining
// until Sun 07:20 with its weekly reset Sunday. 4f4ab07c (home-37, 10.7 points/h) on claude-6
// was pulled to claude-4: claude-3 pulled nothing, its weekly projection 89 being over its cap
// less PullMargin (96 − 10). The account whose room expires first pulls first, and the pull's
// margin under the weekly cap is the same burn's weekly share (setup.md 8.6, Owner drain).
func TestPullSoonestExpiringDrainFirst(t *testing.T) {
	now := time.Date(2026, 11, 25, 18, 14, 50, 0, time.UTC) // 23:44:50 IST
	old := now.Add(-2 * time.Hour)
	c3k, c4k, c6k := "acct:c3/oA", "acct:c4/oA", "acct:c6/oA"
	c3reset := now.Add(3*time.Hour + 45*time.Minute)
	world := func(c3w7 float64) Input {
		p := DefaultParams
		c3 := Account{Key: c3k, Label: "claude-3", LoggedIn: true, Ratio: 9.1, ReadAt: now, Pins: 2, Reserve5: 11.7, Reserve7: 2.5,
			DrainUntil: c3reset.Add(-45 * time.Minute),
			W5:         Window{Level: 20, Reset: now.Add(2 * time.Hour)}, W7: Window{Level: c3w7, Reset: c3reset}}
		c4 := Account{Key: c4k, Label: "claude-4", LoggedIn: true, Ratio: 8, ReadAt: now, DrainUntil: now.Add(81 * time.Hour),
			W5: Window{Level: 10, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 40, Reset: now.Add(82 * time.Hour)}}
		c6 := Account{Key: c6k, Label: "claude-6", LoggedIn: true, Ratio: 8, ReadAt: now, Rate: 10.7, Meter: 10.7,
			W5: Window{Level: 30, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 50, Reset: now.Add(100 * time.Hour)}}
		ss := []Session{{ID: "4f4ab07c", Account: c6k, Rate: 10.7, Busy: true, Movable: true, Context: 80_000, Started: old}}
		// claude-4 listed first, as in the live config order.
		return Input{Now: now, Params: p, Accounts: []Account{c4, c6, c3}, Sessions: ss}
	}
	for _, c := range []struct {
		name string
		w7   float64
		want string
	}{
		// 89 + 12/9.1 × 3.75 h = 93.9 (counted at the lane rate 12), under the weekly cap
		// 98.4 − 2.5 = 95.9 less 10/9.1 = 1.1: claude-3 takes it.
		{"claude-3 at 89%: room until its reset, it pulls first", 89, c3k},
		// 93 + 4.9 = 97.9, over: claude-3 has no room for it; claude-4 takes it.
		{"claude-3 at 93%: no room, claude-4 pulls it", 93, c4k},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := Plan(world(c.w7))
			got := moved(r)
			if got["4f4ab07c"] != c.want {
				for _, m := range r.Moves {
					t.Logf("%s %s -> %s: %s", m.Session, m.From, m.To, m.Reason)
				}
				t.Fatalf("pulled to %q, want %q", got["4f4ab07c"], c.want)
			}
			if c.want == c3k && !strings.Contains(r.Moves[0].Reason, "claude-3 is draining (owner") {
				t.Errorf("reason %q", r.Moves[0].Reason)
			}
		})
	}
	// The order alone: room expiry is the weekly reset, else the drain's until.
	a := Account{DrainUntil: now.Add(81 * time.Hour), W7: Window{Reset: now.Add(82 * time.Hour)}}
	b := Account{DrainUntil: now.Add(3 * time.Hour), W7: Window{Reset: now.Add(4 * time.Hour)}}
	if !roomExpires(b, now).Before(roomExpires(a, now)) {
		t.Fatal("roomExpires order")
	}
}
