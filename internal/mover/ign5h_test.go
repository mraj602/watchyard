package mover

import (
	"testing"
	"time"
)

// drain_ignore_5h (claude on 2026-11-26): owner drain, week 76% resetting in 1.2 h (ratio
// 6.9), 5-hour window resetting in 2.3 h. No move off it for a 5-hour near hit, a pull into it
// ignores its 5-hour room; the weekly room still applies.
func TestDrainIgnore5hMover(t *testing.T) {
	now := time.Date(2026, 11, 26, 17, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	mk := func(ign bool, l5, rate float64) Account {
		return Account{Key: "acct:c/o", Label: "claude", LoggedIn: true, Ratio: 6.9, ReadAt: now, Rate: rate, Meter: rate,
			DrainUntil: now.Add(70 * time.Minute), Ign5h: ign,
			W5: Window{Level: l5, Reset: now.Add(138 * time.Minute)}, W7: Window{Level: 76, Reset: now.Add(72 * time.Minute)}}
	}
	other := Account{Key: "acct:b/o", Label: "claude-b", LoggedIn: true, Ratio: 7, ReadAt: now,
		W5: Window{Level: 5, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 20, Reset: now.Add(100 * time.Hour)}}
	t.Run("near 5h hit: moved off without the flag, stays with it", func(t *testing.T) {
		for _, ign := range []bool{false, true} {
			r := Plan(Input{Now: now, Params: DefaultParams, Accounts: []Account{mk(ign, 75, 60), other},
				Sessions: []Session{{ID: "s", Account: "acct:c/o", Rate: 60, Busy: true, Movable: true, Context: 80_000, Started: old}}})
			got := moved(r)["s"] != ""
			if got == ign {
				t.Fatalf("ignore=%v: moved %v (%v)", ign, got, moved(r))
			}
		}
	})
	t.Run("pull accepted despite its 5h room", func(t *testing.T) {
		src := Account{Key: "acct:b/o", Label: "claude-b", LoggedIn: true, Ratio: 7, ReadAt: now, Rate: 20, Meter: 20,
			W5: Window{Level: 5, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 20, Reset: now.Add(100 * time.Hour)}}
		for _, ign := range []bool{false, true} {
			r := Plan(Input{Now: now, Params: DefaultParams, Accounts: []Account{mk(ign, 60, 0), src},
				Sessions: []Session{{ID: "p", Account: src.Key, Rate: 20, Busy: true, Movable: true, Context: 80_000, Started: old}}})
			if got := moved(r)["p"] == "acct:c/o"; got != ign {
				t.Fatalf("ignore=%v: pulled %v (%v)", ign, got, moved(r))
			}
		}
	})
}
