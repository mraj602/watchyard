package mover

import (
	"testing"
	"time"
)

// setup.md 8.6, Stale data: what stale data held back (Result.StaleBlocks, the only thing the
// owner is told about), and an account that is Quiet since its reading is not stale.
func TestPlanStaleBlocks(t *testing.T) {
	fresh := func(a Account) Account { a.ReadAt = t0.Add(-2 * time.Minute); return a }
	old := func(a Account, age time.Duration) Account { a.ReadAt = t0.Add(-age); return a }
	hot := []Session{lane("hot", "a", 12, true)}
	for _, c := range []struct {
		name   string
		accs   []Account
		ss     []Session
		moveTo string // where "hot" goes ("" none)
		blocks map[string]string
	}{
		{name: "stale and idle, nothing needed it", accs: []Account{fresh(acc("a", 10, 2*time.Hour, 30)), old(acc("b", 10, 4*time.Hour, 20), 7*time.Hour)}, ss: hot},
		{name: "the only room is on a stale account", accs: []Account{fresh(acc("a", 80, 2*time.Hour, 30)), old(acc("b", 10, 4*time.Hour, 20), 7*time.Hour)}, ss: hot,
			blocks: map[string]string{"b": StaleDest}},
		{name: "another fresh account has room: nothing held back", accs: []Account{fresh(acc("a", 80, 2*time.Hour, 30)), old(acc("b", 10, 4*time.Hour, 20), 7*time.Hour), fresh(acc("c", 10, 4*time.Hour, 20))},
			ss: hot, moveTo: "c"},
		{name: "a stale source over its cap with sessions", accs: []Account{old(acc("a", 80, 2*time.Hour, 30), 40*time.Minute), fresh(acc("b", 10, 4*time.Hour, 20))}, ss: hot,
			blocks: map[string]string{"a": StaleSource}},
		{name: "quiet since its reading: a destination on the old reading", accs: []Account{fresh(acc("a", 80, 2*time.Hour, 30)), func() Account {
			b := old(acc("b", 10, 4*time.Hour, 20), 108*time.Hour)
			b.Quiet = true
			return b
		}()}, ss: hot, moveTo: "b"},
	} {
		r := plan(Input{Accounts: c.accs, Sessions: c.ss})
		if got := moved(r)["hot"]; got != c.moveTo {
			t.Errorf("%s: hot -> %q, want %q", c.name, got, c.moveTo)
		}
		got := map[string]string{}
		for _, b := range r.StaleBlocks {
			got[b.Key] = b.How
			if b.StaleFor <= 0 {
				t.Errorf("%s: %+v has no age", c.name, b)
			}
		}
		if len(got) != len(c.blocks) {
			t.Errorf("%s: blocks %v, want %v", c.name, got, c.blocks)
		}
		for k, v := range c.blocks {
			if got[k] != v {
				t.Errorf("%s: blocks %v, want %v", c.name, got, c.blocks)
			}
		}
	}
}

// A draining account whose data is stale pulls nothing; that is a block only when a busy
// burner elsewhere could have come in.
func TestPlanStaleDrainPull(t *testing.T) {
	d := acc("d", 10, 2*time.Hour, 30)
	d.DrainUntil, d.ReadAt = t0.Add(6*time.Hour), t0.Add(-time.Hour)
	o := acc("o", 20, 4*time.Hour, 20)
	o.ReadAt = t0.Add(-time.Minute)
	busy := Session{ID: "s", Account: "o", Rate: 10, Busy: true, Movable: true, Context: 50_000, Started: t0.Add(-2 * time.Hour)}
	r := plan(Input{Accounts: []Account{d, o}, Sessions: []Session{busy}})
	if len(r.Moves) != 0 || len(r.StaleBlocks) != 1 || r.StaleBlocks[0].How != StalePull {
		t.Fatalf("moves %v blocks %+v", moved(r), r.StaleBlocks)
	}
	if r := plan(Input{Accounts: []Account{d, o}}); len(r.StaleBlocks) != 0 {
		t.Fatalf("nothing to pull, blocks %+v", r.StaleBlocks)
	}
}
