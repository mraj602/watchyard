package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Live (2026-11-25 03:53): a `wy move <id> <account> --dry-run` was reported to move for
// real. Flags are read wherever they stand; a flag-like word left unread stops the command
// before anything happens; nothing runs on the host in a dry run, whatever the order.
func TestMoveFlagsAnywhere(t *testing.T) {
	_, h, run := moveFixture(t, 60, "busy")
	for _, args := range [][]string{
		{"lane-a", "claude-2", "--dry-run"},
		{"--dry-run", "lane-a", "claude-2"},
		{"lane-a", "--dry-run", "claude-2"},
		{"lane-a", "claude-2", "--dry-run", "--now"},
		{"lane-a", "claude-2", "--now", "--dry-run", "--bg"},
		{"5e55a0a0", "2", "--force", "--dry-run"},
	} {
		out, errOut, code := run(args...)
		if code != 0 || !strings.Contains(out, "--resume 5e55a0a0") || len(h.calls) != 0 {
			t.Fatalf("%q: %d %q %q calls %q", args, code, out, errOut, h.calls)
		}
		if strings.Contains(strings.Join(args, " "), "--bg") && !strings.Contains(out, "--bg") {
			t.Fatalf("%q: --bg not read: %q", args, out)
		}
	}
	// "--" ends the flags: a "--dry-run" after it is refused, nothing moves.
	if _, errOut, code := run("lane-a", "claude-2", "--", "--dry-run"); code != 2 || len(h.calls) != 0 {
		t.Fatalf("after --: %d %q calls %q", code, errOut, h.calls)
	}
	if err := flagLike([]string{"lane-a", "--dry-run"}); err == nil || !strings.Contains(err.Error(), `"--dry-run" was not read as a flag`) {
		t.Fatalf("flagLike: %v", err)
	}
	// Unknown flags anywhere are a usage error.
	if _, _, code := run("lane-a", "claude-2", "--dryrun"); code != 2 || len(h.calls) != 0 {
		t.Fatalf("unknown flag: %d calls %q", code, h.calls)
	}
	// Mid-turn without --now (wherever it would stand) is refused, not moved.
	if _, errOut, code := run("lane-a", "claude-2"); code == 0 || !strings.Contains(errOut, "middle of a turn") || len(h.calls) != 0 {
		t.Fatalf("mid-turn: %d %q", code, errOut)
	}
}

// A by-hand move never makes a second copy: two processes of one session are refused, and so
// is a move of a session moved within moveSettle (its resumed copy may still be starting).
func TestMoveNoSecondCopy(t *testing.T) {
	f, h, run := moveFixture(t, 60, "idle")
	if _, errOut, code := run("lane-a", "claude-2"); code != 0 {
		t.Fatalf("first move: %d %q", code, errOut)
	}
	n := len(h.calls)
	if _, errOut, code := run("lane-a", "claude-2"); code == 0 || !strings.Contains(errOut, "was moved 0s ago") || len(h.calls) != n {
		t.Fatalf("second move: %d %q calls %q", code, errOut, h.calls[n:])
	}
	// The same session twice in the registry (the old process and a resumed copy).
	f, h, run = moveFixture(t, 60, "idle")
	n = 0
	since := strconv.FormatInt(nextNow.Add(-20*time.Minute).UnixMilli(), 10)
	body := `{"pid":101,"sessionId":"5e55a0a0-0000-4000-8000-000000000001","cwd":"/work","procStart":"6","kind":"background","name":"lane-a","status":"idle","statusUpdatedAt":` + since + `}`
	if err := os.WriteFile(filepath.Join(f.home, ".claude", "sessions", "101.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := run("5e55a0a0", "claude-2", "--force"); code == 0 || !strings.Contains(errOut, "2 copies of 5e55a0a0 are running") || len(h.calls) != n {
		t.Fatalf("two copies: %d %q calls %q", code, errOut, h.calls[n:])
	}
}
