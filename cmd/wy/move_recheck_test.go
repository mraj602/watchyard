package main

import (
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/mover"
)

// setup.md 8.6, Re-check: a mid-turn move is re-chosen on the state read again after its step
// wait. Its source's burn falls during the wait: the move is dropped, said in the pass output,
// kept in rechecks.jsonl and shown by --plan for an hour.
func TestMoveRecheckDrops(t *testing.T) {
	f, h, run := moveFixture(t, 60, "busy")
	f.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	f.set("account.smart_switch_scope", "sessions:5e55a0a0")
	moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":15}}` // 60 + 3 h × 15 = 105
	t.Cleanup(func() { delete(moveTestVars, moveTestEnv) })
	f.set("account.smart_switch", "on")
	defer func(a time.Duration) { recheckAfter, beforeRecheck = a, nil }(recheckAfter)
	recheckAfter = 0
	beforeRecheck = func() { moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":1}}` } // 63: under its caps
	run("--auto")                                                                     // the spike filter's first pass
	out, errOut, code := run("--auto")
	if code != 0 || strings.Contains(out, "moved 5e55a0a0") || !strings.Contains(out, "5e55a0a0 lane-a: not moved to claude-2: re-checked after its step wait: claude no longer needs it") {
		t.Fatalf("auto: %d %q %q", code, out, errOut)
	}
	for _, c := range h.calls {
		if strings.Contains(c, "respawn") || strings.Contains(c, "kill") {
			t.Fatalf("a dropped move touched the session: %q", c)
		}
	}
	if rs := mover.NewState(f.dirs.State.Path).Rechecks(nextNow.Add(-time.Hour)); len(rs) != 1 || rs[0].To != "" || rs[0].Planned != "claude-2" {
		t.Fatalf("rechecks %+v", rs)
	}
	out, _, _ = run("--plan")
	if !strings.Contains(out, "re-check ") || !strings.Contains(out, "5e55a0a0 lane-a was planned claude -> claude-2, not moved: claude no longer needs it") {
		t.Fatalf("plan:\n%s", out)
	}
}

// Nothing changed during the wait: the move is carried out as planned, with no re-check note.
func TestMoveRecheckKeeps(t *testing.T) {
	f, _, run := moveFixture(t, 60, "busy")
	f.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	f.set("account.smart_switch_scope", "sessions:5e55a0a0")
	moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":15}}`
	t.Cleanup(func() { delete(moveTestVars, moveTestEnv) })
	f.set("account.smart_switch", "on")
	defer func(a time.Duration) { recheckAfter = a }(recheckAfter)
	recheckAfter = 0
	run("--auto")
	out, errOut, code := run("--auto")
	if code != 0 || !strings.Contains(out, "moved 5e55a0a0 lane-a from claude to claude-2") || strings.Contains(out, "re-checked") {
		t.Fatalf("auto: %d %q %q", code, out, errOut)
	}
	if es := mover.NewState(f.dirs.State.Path).Log(nextNow.Add(-time.Hour)); len(es) != 1 || es[0].Recheck != "" {
		t.Fatalf("log %+v", es)
	}
}
