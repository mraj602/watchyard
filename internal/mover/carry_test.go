package mover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/identity"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A folder with its own projects/ gets the transcript and the session's folder; one that
// shares projects/ (a link, as wy account add makes) gets nothing; a stale copy is replaced.
func TestCarryTranscript(t *testing.T) {
	home := t.TempDir()
	a, b, c := filepath.Join(home, ".claude-4"), filepath.Join(home, ".claude-1"), filepath.Join(home, ".claude-2")
	write(t, filepath.Join(a, "projects", "-w", "s1.jsonl"), "line1\nline2\n")
	write(t, filepath.Join(a, "projects", "-w", "s1", "subagents", "x.jsonl"), "sub\n")
	write(t, filepath.Join(b, "projects", "-w", "s1.jsonl"), "stale\n")
	if err := os.MkdirAll(c, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(a, "projects"), filepath.Join(c, "projects")); err != nil {
		t.Fatal(err)
	}
	if err := CarryTranscript(a, b, "s1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(b, "projects", "-w", "s1.jsonl")); string(got) != "line1\nline2\n" {
		t.Fatalf("copy %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(b, "projects", "-w", "s1", "subagents", "x.jsonl")); string(got) != "sub\n" {
		t.Fatalf("side folder %q", got)
	}
	if err := CarryTranscript(a, c, "s1"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(filepath.Join(c, "projects")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the shared projects link was replaced")
	}
	if err := CarryTranscript(a, b, "nope"); err != nil {
		t.Fatalf("no transcript: %v", err)
	}
}

// Trust follows the session only where it was trusted before, and only that one value is
// written.
func TestCarryTrust(t *testing.T) {
	home := t.TempDir()
	a, b := filepath.Join(home, ".claude-3"), filepath.Join(home, ".claude-4")
	write(t, filepath.Join(a, ".claude.json"), `{"oauthAccount": {"x": 1}, "projects": {"/w": {"hasTrustDialogAccepted": true}}}`)
	write(t, filepath.Join(b, ".claude.json"), `{"oauthAccount": {"y": 2}, "projects": {"/other": {"hasTrustDialogAccepted": true}}}`)
	if err := os.MkdirAll(filepath.Join(b, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	ok, err := CarryTrust(a, b, "/w/sub")
	if err != nil || !ok {
		t.Fatalf("carry: %v %v", ok, err)
	}
	got, _ := os.ReadFile(filepath.Join(b, ".claude.json"))
	if !strings.Contains(string(got), `"/w/sub"`) || !strings.Contains(string(got), `"oauthAccount": {"y": 2}`) || !trusted(filepath.Join(b, ".claude.json"), "/w/sub") {
		t.Fatalf("after: %s", got)
	}
	if ok, _ := CarryTrust(a, b, "/w/sub"); ok {
		t.Fatal("written twice")
	}
	if ok, _ := CarryTrust(a, b, "/untrusted"); ok || trusted(filepath.Join(b, ".claude.json"), "/untrusted") {
		t.Fatal("an untrusted folder became trusted")
	}
}

// A background job's own flags come from its supervisor record (its process may be a spare
// started with --bg-spare only); nothing else of the record is used.
func TestJobFlags(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "jobs", "fd7a3b01", "state.json"), `{"providerEnv": {"X": "y"}, "respawnFlags": ["--model", "haiku", "-n", "lane-c"]}`)
	fl, ok := jobFlags(identity.OSFS{}, dir, "fd7a3b01")
	if !ok || strings.Join(fl, " ") != "--model haiku -n lane-c" {
		t.Fatalf("flags %q %v", fl, ok)
	}
	if _, ok := jobFlags(identity.OSFS{}, dir, "../x"); ok {
		t.Fatal("a bad job id was read")
	}
	if got := KeptArgs([]string{"--bg-spare"}); len(got) != 0 {
		t.Fatalf("spare flag kept: %q", got)
	}
}
