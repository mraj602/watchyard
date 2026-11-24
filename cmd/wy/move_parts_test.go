package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/leases"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/picker"
)

func TestSwitchScope(t *testing.T) {
	sid := "67ce5986-9646-4aba-a39d-d6261062e23d"
	for _, c := range []struct {
		v    string
		tags []string
		in   bool
	}{
		{"", nil, true},
		{"all", nil, true},
		{"tag:move-e2e", []string{"x", "move-e2e"}, true},
		{"tag:move-e2e", []string{"x"}, false},
		{"sessions:67ce5986,abc", nil, true},
		{"sessions:67ce", nil, false}, // a prefix needs 8 characters
		{"bogus", nil, false},
	} {
		if got := parseScope(c.v).in(sid, c.tags); got != c.in {
			t.Errorf("%q %v: %v", c.v, c.tags, got)
		}
	}
	if got := scopeTags("tag:move-e2e"); len(got) != 1 || got[0] != "move-e2e" {
		t.Errorf("scope tags %v", got)
	}
}

// WY_MOVE_TEST is refused while the scope is all, and replaces levels, rates and timings.
func TestMoveTestInjection(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if _, err := loadMoveTest(env(`{}`), parseScope("all")); err == nil {
		t.Fatal("injection accepted with scope all")
	}
	mt, err := loadMoveTest(env(`{"accounts": {"claude-2": {"five_hour": 90, "five_reset_min": 120}},
		"rates": {"67ce5986": 40}, "cooldown_min": 1, "account_moves": 9}`), parseScope("tag:x"))
	if err != nil || mt == nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	a := mover.Account{Label: "claude-2", Stale: true}
	if !mt.applyAccount(&a, now) || a.W5.Level != 90 || !a.W5.Reset.Equal(now.Add(2*time.Hour)) || a.Stale {
		t.Errorf("account %+v", a)
	}
	if r, ok := mt.rate("67ce5986-9646-4aba-a39d-d6261062e23d"); !ok || r != 40 {
		t.Errorf("rate %v %v", r, ok)
	}
	p := mover.DefaultParams
	mt.applyParams(&p)
	if p.Cooldown != time.Minute || p.AccountMoves != 9 {
		t.Errorf("params %+v", p)
	}
}

// wy burn and the mover read the same numbers: units in the window, calls per model, helpers.
func TestBurnStats(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	ts := []picker.TokenUse{
		{At: now.Add(-time.Minute), Session: "s1", Units: 1e6, Model: "claude-opus-4-8"},
		{At: now.Add(-2 * time.Minute), Session: "s1", Units: 5e5, Model: "claude-haiku-5-5-20261001", Agent: "a1"},
		{At: now.Add(-20 * time.Minute), Session: "s1", Units: 9e9}, // outside the window
		{At: now.Add(-time.Minute), Session: "s2", Units: 1e5},
	}
	b := burnStats(ts, now, 15*time.Minute)
	s1 := b["s1"]
	if s1.Units != 1.5e6 || len(s1.Helpers) != 1 || s1.Calls["opus-4-8"] != 1 || s1.Calls["haiku-5-5"] != 1 {
		t.Fatalf("s1 %+v", s1)
	}
	if r := s1.rate(2e-6); r < 11.99 || r > 12.01 { // 1.5M units × 2/M = 3 points in 15 min
		t.Errorf("rate %v", r)
	}
}

func TestNoticeText(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 11, 0, 0, time.Local)
	one := laneNotice{Session: "67ce5986-x", Name: "lane-a", From: "claude-3", To: "claude-2", At: at,
		Move: mover.Move{Window: "5h", HitAt: at.Add(12 * time.Minute)}, Old: "lane-a@claude-3.box", New: "lane-a@claude-2.box"}
	got := noticeText([]laneNotice{one})
	for _, want := range []string{"Your lane lane-a (67ce5986) moved claude-3 -> claude-2 at 12:11 (5h would have hit at 12:23)",
		"its handle is now lane-a@claude-2.box (the old handle lane-a@claude-3.box still works)", "still reach it"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	two := one
	two.New = ""
	if got := noticeText([]laneNotice{one, two}); !strings.HasPrefix(got, "2 of the sessions you started") || !strings.Contains(got, "restarting there") {
		t.Errorf("two: %q", got)
	}
	if !sameMachine("a@c-2.box", "a@c-3.box") || sameMachine("a@c-2.other", "a@c-3.box") || !sameMachine("x", "") {
		t.Error("sameMachine")
	}
}

// TestNoticeOnlyToParent: only the session recorded as a lane's parent in the ledger is told
// of its move; a session with no recorded parent tells nobody, and another session's parent
// is never told of a lane it did not start.
func TestNoticeOnlyToParent(t *testing.T) {
	f := newNextFixture(t)
	e := moveEnv{nextEnv: nextEnv{dirs: f.dirs, now: func() time.Time { return nextNow }}}
	g := leases.Ledger{Dir: filepath.Join(f.dirs.State.Path, "next")}
	const parent, child, other, otherParent, orphan = "p0000000-1", "c0000000-1", "o0000000-1", "q0000000-1", "x0000000-1"
	for i, l := range []leases.Line{
		{SessionID: child, Parent: parent},
		{SessionID: other, Parent: otherParent},
		{SessionID: orphan},
	} {
		l.V, l.Op, l.ID, l.At, l.Key = 1, leases.OpLease, "id"+strconv.Itoa(i), nextNow.Add(-time.Hour).UTC(), "acct:a1/o1"
		if err := g.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	to := mover.Target{Key: "acct:a2/o1", Label: "claude-2", Dir: "/h/.claude-2"}
	var ns []laneNotice
	for _, sid := range []string{child, orphan} {
		ns = append(ns, laneNotice{Session: sid, Parent: moveLease(e, sid, to, "claude-1", false)})
	}
	if ns[0].Parent != parent || ns[1].Parent != "" {
		t.Fatalf("parents from the ledger: %+v", ns)
	}
	got := noticesByParent(ns)
	if len(got) != 1 || len(got[parent]) != 1 || got[parent][0].Session != child {
		t.Fatalf("told %v, want only %s about %s", got, parent, child)
	}
	if _, ok := got[otherParent]; ok {
		t.Fatal("a non-parent was told")
	}
	// A second hop keeps the parent (the new lease carries it).
	if p := moveLease(e, child, mover.Target{Key: "acct:a3/o1", Label: "claude-3", Dir: "/h/.claude-3"}, "claude-2", false); p != parent {
		t.Fatalf("second hop parent %q", p)
	}
}
