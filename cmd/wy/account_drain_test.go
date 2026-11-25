package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
)

// setup.md 8.5/8.6, Owner drain: `wy account drain claude-2` stores drain_until (the end of
// today by default); --plan names it, and the switch pulls a busy burner of another account
// onto it while its 5h projection has room; `off` ends it.
func TestAccountDrain(t *testing.T) {
	f, _, run := moveFixture(t, 60, "busy")
	f.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	f.set("account.smart_switch_scope", "sessions:5e55a0a0")
	moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":5}}` // claude: 60 + 3 h × 5 = 75, under its caps
	t.Cleanup(func() { delete(moveTestVars, moveTestEnv) })
	drain := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		err := accountDrain(nextEnv{home: f.home, dirs: f.dirs, getenv: func(string) string { return "" }, now: func() time.Time { return nextNow },
			stdout: &out, stderr: &errOut}, args)
		return out.String() + errOut.String(), err
	}
	if out, _, _ := run("--plan"); strings.Contains(out, "pulled in") || !strings.Contains(out, "No session needs a move.") {
		t.Fatalf("plan before:\n%s", out)
	}
	out, err := drain("claude-2")
	end := time.Date(nextNow.Year(), nextNow.Month(), nextNow.Day(), 23, 59, 0, 0, nextNow.Location())
	if err != nil || !strings.Contains(out, "claude-2: draining (owner, until 23:59)") {
		t.Fatalf("drain: %v %q", err, out)
	}
	cf, _ := config.Load(f.dirs.Config.Path)
	if v, _ := cf.Get("next.accounts.claude-2.drain_until"); v != end.Format(time.RFC3339) {
		t.Fatalf("stored %v, want %s", v, end.Format(time.RFC3339))
	}
	out, _, _ = run("--plan")
	if !strings.Contains(out, "draining (owner, until 23:59); under its caps") || !strings.Contains(out, "pulled in: claude-2 is draining (owner, until 23:59)") {
		t.Fatalf("plan with the drain:\n%s", out)
	}
	f.set("account.smart_switch", "on")
	out, errOut, code := run("--auto")
	if code != 0 || !strings.Contains(out, "moved 5e55a0a0 lane-a from claude to claude-2") {
		t.Fatalf("auto: %d %q %q", code, out, errOut)
	}
	if _, err := drain("claude-2", "--until", "00:01"); err == nil {
		t.Error("a time already past was taken")
	}
	if _, err := drain("claude-9"); err == nil {
		t.Error("an unknown label was taken")
	}
	if out, err := drain("claude-2", "off"); err != nil || !strings.Contains(out, "owner drain off") {
		t.Fatalf("off: %v %q", err, out)
	}
	cf, _ = config.Load(f.dirs.Config.Path)
	if v, _ := cf.Get("next.accounts.claude-2.drain_until"); v != nil && v != "" {
		t.Fatalf("still stored: %v", v)
	}
}

func TestParseDrainUntil(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 5, 0, 0, time.FixedZone("IST", 5*3600+1800))
	for in, want := range map[string]string{
		"":                          "2026-11-25T23:59:00+05:30",
		"18:30":                     "2026-11-25T18:30:00+05:30",
		"2026-11-26 07:00":          "2026-11-26T07:00:00+05:30",
		"2026-11-26T07:00:00+05:30": "2026-11-26T07:00:00+05:30",
	} {
		got, err := parseDrainUntil(in, now)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("%q: %v %v, want %s", in, got, err, want)
		}
	}
	if _, err := parseDrainUntil("tonight", now); err == nil {
		t.Error("tonight parsed")
	}
}
