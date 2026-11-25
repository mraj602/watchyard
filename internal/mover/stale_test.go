package mover

import (
	"testing"
	"time"
)

// Live (pre6, 05:37, the hub migrating): claude-4's level came from a reading 3 h 37 min old
// and read "5h 100%"; it was ~68%. A stale account is neither a source nor a destination.
func TestPlanStaleData(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 7, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	stale := Account{Key: "acct:c4/oA", Label: "claude-4", Rate: 20, Meter: 20, LoggedIn: true, Ratio: 8, ReadAt: now.Add(-217 * time.Minute),
		W5: Window{Level: 100, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 40, Reset: now.Add(100 * time.Hour)}}
	fresh := Account{Key: "acct:c2/oA", Label: "claude-2", LoggedIn: true, Ratio: 8, ReadAt: now.Add(-2 * time.Minute),
		W5: Window{Level: 30, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 20, Reset: now.Add(100 * time.Hour)}}
	ss := []Session{
		{ID: "a", Account: stale.Key, Rate: 12, Busy: true, Movable: true, Context: 80_000, Started: old},
		{ID: "b", Account: stale.Key, Rate: 8, Busy: true, Movable: true, Context: 80_000, Started: old},
	}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{stale, fresh}, Sessions: ss,
		Streak: map[string]int{stale.Key + " " + Win5h: 5}}
	r := Plan(in)
	if len(r.Moves) != 0 {
		t.Fatalf("moved on stale data: %+v", r.Moves)
	}
	pr := r.Accounts[0]
	if pr.StaleFor != 217*time.Minute || pr.Triggered || pr.Held {
		t.Fatalf("projection %+v", pr)
	}
	if got := DefaultParams.StaleAccounts(r); len(got) != 1 || got[0].Key != stale.Key {
		t.Fatalf("stale accounts over 30 min: %+v", got)
	}
	// The same account read fresh: its sessions move.
	in.Accounts[0].ReadAt = now.Add(-time.Minute)
	if r := Plan(in); len(r.Moves) == 0 {
		t.Fatalf("no moves on fresh data: %+v", r.Skips)
	}
	// Stale-low is no destination: a fresh source over its cap, the only other account stale.
	in.Accounts[0].ReadAt = now.Add(-time.Minute)
	in.Accounts[1].ReadAt = now.Add(-45 * time.Minute)
	r = Plan(in)
	for _, m := range r.Moves {
		if m.To != "" {
			t.Fatalf("moved onto a stale account: %+v", m)
		}
	}
	// Under the threshold (10 min) and unknown reading times count as fresh.
	in.Accounts[1].ReadAt = now.Add(-9 * time.Minute)
	if r := Plan(in); !slicesHasTo(r.Moves, fresh.Key) {
		t.Fatalf("9-minute-old data refused: %+v %+v", r.Moves, r.Skips)
	}
	in.Accounts[1].ReadAt = time.Time{}
	if r := Plan(in); !slicesHasTo(r.Moves, fresh.Key) {
		t.Fatalf("unknown reading time refused: %+v", r.Moves)
	}
}

func slicesHasTo(ms []Move, key string) bool {
	for _, m := range ms {
		if m.To == key {
			return true
		}
	}
	return false
}

// Live 05:40:42: claude-5 (protected since 05:36, its week reset at 05:30: 7d 0.1%) at 5h
// 72% and 61 points/h; 78f476e8 had made no call for 19 minutes (burn 0.0, mid-turn) and was
// moved "at 0.0 points/h of burn" by a pass of the pre5 binary (no burn floor, config read
// before the change). The current switch keeps the protected caps and never moves it.
func TestPlanLiveClaude5(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 10, 42, 0, time.UTC)
	old := now.Add(-70 * time.Minute)
	c5 := Account{Key: "acct:c5/oA", Label: "claude-5", Protected: true, Rate: 61.3, Meter: 61.3, LoggedIn: true, Ratio: 8, ReadAt: now.Add(-2 * time.Minute),
		W5: Window{Level: 72.1, Reset: now.Add(2*time.Hour + 50*time.Minute)}, W7: Window{Level: 0.12, Reset: now.Add(604692 * time.Second)}}
	c2 := Account{Key: "acct:c2/oA", Label: "claude-2", Rate: 4.6, Meter: 4.6, LoggedIn: true, Ratio: 8, ReadAt: now.Add(-9 * time.Minute),
		W5: Window{Level: 34.1, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 19, Reset: now.Add(532693 * time.Second)}}
	ss := []Session{
		{ID: "78f476e8", Account: c5.Key, Rate: 0, Busy: true, Movable: true, Context: 120_000, Started: old},
		{ID: "hot", Account: c5.Key, Rate: 40, Busy: true, Movable: true, Context: 80_000, Started: old},
		{ID: "warm", Account: c5.Key, Rate: 21.3, Busy: true, Movable: true, Context: 80_000, Started: old},
	}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{c5, c2}, Sessions: ss,
		Streak: map[string]int{c5.Key + " " + Win5h: 5}}
	r := Plan(in)
	pr := r.Accounts[0]
	if !pr.Protected || pr.Cap5 != 80 || pr.Cap7 != 80 || pr.Expiring {
		t.Fatalf("claude-5 projection %+v", pr)
	}
	for _, m := range r.Moves {
		if m.Session == "78f476e8" {
			t.Fatalf("moved a session at 0 points/h: %+v", m)
		}
	}
	if !skipped(r, "78f476e8", "burns 0.0 points/h") {
		t.Fatalf("skips %+v", r.Skips)
	}
	if !slicesHasTo(r.Moves, c2.Key) {
		t.Fatalf("the burners stayed: %+v %+v", r.Moves, r.Skips)
	}
}
