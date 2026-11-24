package mover

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The tests' machine may lack claude on PATH and have ~/.local/bin/claude: every other test
// sees claude on PATH.
func init() { claudePath = func() (string, error) { return "claude", nil } }

// The timer's PATH lacks ~/.local/bin (live run, 1.3.3-pre3): claude is then run by its full
// path; on PATH it stays "claude".
func TestClaudeArgv(t *testing.T) {
	oldPath, oldLocate, oldExe := claudePath, locate, procExe
	t.Cleanup(func() { claudePath, locate, procExe = oldPath, oldLocate, oldExe })
	var hints []string
	locate = func(home string, h ...string) (string, error) {
		hints = h
		return "/home/u/.local/bin/claude", nil
	}
	argv := []string{"env", "CLAUDE_CONFIG_DIR=/home/u/.claude-2", "claude", "--resume", "s"}

	claudePath = func() (string, error) { return "/usr/bin/claude", nil }
	if got := ClaudeArgv(argv); strings.Join(got, " ") != strings.Join(argv, " ") {
		t.Errorf("on PATH: %q", got)
	}
	claudePath = func() (string, error) { return "", errors.New("not found") }
	if got := strings.Join(ClaudeArgv(argv, "/x/claude"), " "); got != "env CLAUDE_CONFIG_DIR=/home/u/.claude-2 /home/u/.local/bin/claude --resume s" {
		t.Errorf("off PATH: %q", got)
	}
	if len(hints) != 1 || hints[0] != "/x/claude" {
		t.Errorf("hints %q", hints)
	}
	if argv[2] != "claude" {
		t.Error("argv changed in place")
	}
	if got := strings.Join(ClaudeArgv([]string{"claude", "stop", "x"}), " "); got != "/home/u/.local/bin/claude stop x" {
		t.Errorf("bare: %q", got)
	}
	// A launcher finds claude itself.
	if got := strings.Join(ClaudeArgv([]string{"/home/u/bin/claude-launch", "2", "--resume", "s"}), " "); got != "/home/u/bin/claude-launch 2 --resume s" {
		t.Errorf("launcher: %q", got)
	}
	locate = func(string, ...string) (string, error) { return "", errors.New("none") }
	if ClaudeBin() != "claude" {
		t.Error("nothing found: plain claude")
	}

	// The session's own executable is a hint only when it is claude's binary.
	for exe, want := range map[string]string{
		"/home/u/.local/share/claude/versions/2.1.331": "/home/u/.local/share/claude/versions/2.1.331",
		"/usr/local/bin/claude":                        "/usr/local/bin/claude",
		"/usr/bin/node":                                "",
		"/usr/bin/bash":                                "",
	} {
		procExe = func(int) string { return exe }
		if got := sessionExe(42); got != want {
			t.Errorf("sessionExe(%s) = %q", exe, got)
		}
	}
}

// Execute starts the resume with claude's full path when PATH lacks it, and returns (shows)
// the plain command.
func TestExecuteResolvesClaude(t *testing.T) {
	oldPath, oldLocate, oldExe := claudePath, locate, procExe
	t.Cleanup(func() { claudePath, locate, procExe = oldPath, oldLocate, oldExe })
	claudePath = func() (string, error) { return "", errors.New("not found") }
	locate = func(string, ...string) (string, error) { return "/opt/claude/bin/claude", nil }
	procExe = func(int) string { return "" }
	h := &fakeHost{}
	l := Launch{PID: 7, SessionID: "s", Slot: "/h/.claude", CWD: "/w", Kind: KindHeadless, Argv: []string{"-p", "x"}}
	argv, err := Execute(h, l, Target{Dir: filepath.Join("/h", ".claude-2")}, "", "N", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(argv, " "), "/opt/claude") {
		t.Errorf("shown: %q", argv)
	}
	if last := h.calls[len(h.calls)-1]; !strings.HasPrefix(last, "start /w: env CLAUDE_CONFIG_DIR=/h/.claude-2 /opt/claude/bin/claude -p --resume s N") {
		t.Errorf("started %q", h.calls)
	}
}

// account.move_launch with {launcher-config}: the session's config flags are dropped, the
// launcher adds its own; KeptArgs keeps a flag repeated with the same values once.
func TestLauncherConfig(t *testing.T) {
	tg := Target{Dir: "/home/u/.claude-3", Label: "claude-3"}
	l := Launch{SessionID: "s", Kind: KindTmux, Argv: []string{"--plugin-dir", "/a/p", "--settings", "/a/s.json", "--model", "opus",
		"--mcp-config", "/a/m.json", "/a/n.json", "--plugin-dir", "/a/p", "--permission-mode", "auto", "task"}}
	args := ResumeArgs(l, "note")
	if got := strings.Join(args, " "); got != "--plugin-dir /a/p --settings /a/s.json --model opus --mcp-config /a/m.json /a/n.json --permission-mode auto --resume s" {
		t.Errorf("kept (deduped): %q", got)
	}
	got := strings.Join(Command("/home/u/bin/claude-launch {n} {launcher-config}", tg, args), " ")
	if got != "/home/u/bin/claude-launch 3 --model opus --permission-mode auto --resume s" {
		t.Errorf("launcher-config: %q", got)
	}
	// Without the token the launcher gets every flag (setup.md 8.6: it must not add them again).
	if got := strings.Join(Command("/home/u/bin/claude-launch {n}", tg, args), " "); !strings.Contains(got, "--plugin-dir /a/p --settings") {
		t.Errorf("plain template: %q", got)
	}
	if got := strings.Join(withoutFlags([]string{"--settings=/x", "--strict-mcp-config", "-p", "--", "--settings", "y"}, LauncherConfigFlags), " "); got != "-p -- --settings y" {
		t.Errorf("withoutFlags: %q", got)
	}
}
