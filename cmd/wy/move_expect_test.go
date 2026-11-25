package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/leases"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/picker"
)

// The lease line keeps the lane's expected end as the picker counted it (setup.md 8.6, one
// rule): --expect's class and time, a medium lane's 48 minutes without one.
func TestLeaseLinesKeepExpect(t *testing.T) {
	now := nextNow
	res := picker.Result{Strategy: "lanes", Picks: []picker.Pick{
		{SessionID: "a", SwitchClass: "medium", SwitchDur: 48 * time.Minute},
		{SessionID: "b"}, // estimates off: nothing written, as before
	}}
	ls := leaseLines(res, "opus", now)
	if len(ls) != 2 {
		t.Fatalf("%d lines", len(ls))
	}
	if l := ls[0]; l.Expect != "medium" || l.ExpectMin != 48 || l.ExpectEnd == nil || !l.ExpectEnd.Equal(now.Add(48*time.Minute)) {
		t.Fatalf("line a: %+v", l)
	}
	if l := ls[1]; l.Expect != "" || l.ExpectEnd != nil {
		t.Fatalf("line b: %+v", l)
	}
}

// A move lease keeps its session's expected end, so the switch counts the lane the same on its
// new account.
func TestMoveLeaseKeepsExpect(t *testing.T) {
	f := newNextFixture(t)
	e := moveEnv{nextEnv: nextEnv{dirs: f.dirs, now: func() time.Time { return nextNow }}}
	g := leases.Ledger{Dir: filepath.Join(f.dirs.State.Path, "next")}
	end := nextNow.Add(20 * time.Minute).UTC()
	if err := g.Append(leases.Line{V: 1, Op: leases.OpLease, ID: "id0", At: nextNow.Add(-28 * time.Minute).UTC(), Key: "acct:a1/o1",
		SessionID: "s1", Strategy: "lanes", Expect: "medium", ExpectMin: 48, ExpectEnd: &end}); err != nil {
		t.Fatal(err)
	}
	moveLease(e, "s1", mover.Target{Key: "acct:a2/o1", Label: "claude-2", Dir: "/h/.claude-2"}, "claude-1", false)
	ls, _, err := g.Read(nextNow)
	if err != nil || len(ls) != 2 {
		t.Fatalf("ledger %v %v", ls, err)
	}
	if l := ls[1]; l.Strategy != "move" || l.Expect != "medium" || l.ExpectEnd == nil || !l.ExpectEnd.Equal(end) {
		t.Fatalf("move lease %+v", l.Line)
	}
}

// laneExpect: the time left to the lease's expected end, 0 once overrun (the switch then
// projects to the horizons), the session's latest end for a lease line without one, and
// older lines as before.
func TestLaneExpect(t *testing.T) {
	now := nextNow
	at := func(m int) time.Time { return now.Add(time.Duration(m) * time.Minute) }
	end := at(30)
	ledger := []leases.Lease{
		{Line: leases.Line{At: at(-18), SessionID: "c", Strategy: "lanes", ExpectEnd: &end}},
		{Line: leases.Line{At: at(-5), SessionID: "c", Strategy: "move"}}, // written before it kept it
	}
	lanes := []picker.LiveLane{
		{SID: "a", T0: at(-28), ExpectEnd: at(20)},
		{SID: "b", T0: at(-60), ExpectEnd: at(-12)}, // a medium lane 60 minutes in: overran
		{SID: "c", T0: at(-5)},
		{SID: "d", T0: at(-5)},                            // an older line: unknown
		{SID: "e", T0: at(-10), Expect: 25 * time.Minute}, // picked in this run
	}
	got := laneExpect(lanes, ledger, now)
	want := map[string]time.Duration{"a": 20 * time.Minute, "b": 0, "c": 30 * time.Minute, "e": 15 * time.Minute}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
}

// A moved lane's age is its session's first lease, not its move lease's.
func TestFirstLeases(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 4, 0, 0, 0, time.UTC)
	moved := t0.Add(95 * time.Minute)
	started := map[string]time.Time{"54c4e1e8": moved, "fresh": moved}
	var l1, l2, l3 leases.Lease
	l1.SessionID, l1.At = "54c4e1e8", t0
	l2.SessionID, l2.At, l2.Strategy = "54c4e1e8", moved, "move"
	l3.SessionID, l3.At = "other", t0.Add(-time.Hour)
	firstLeases(started, []leases.Lease{l1, l2, l3})
	if !started["54c4e1e8"].Equal(t0) || !started["fresh"].Equal(moved) || !started["other"].IsZero() {
		t.Fatalf("%v", started)
	}
}
