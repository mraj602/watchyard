package peers_test

import (
	"github.com/mraj602/watchyard/internal/peers"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hookCtx(f *fx, pid int) peers.HookContext {
	return peers.HookContext{State: f.state, ConfigDir: "~/.claude-2", Now: f.now, FindPID: func() int { return pid }}
}

func TestSessionFileLifecycle(t *testing.T) {
	f := newFx(t)
	c := hookCtx(f, 4242)
	ev := func(name, mode, prompt string) peers.HookEvent {
		return peers.HookEvent{SessionID: "sess-1", Name: name, PermissionMode: mode, Prompt: prompt}
	}
	wrote, err := peers.UpdateSessionFile(c, ev("SessionStart", "default", ""))
	if err != nil || !wrote {
		t.Fatalf("start: %v %v", wrote, err)
	}
	sf, ok := peers.ReadSessionFile(f.state, "sess-1")
	if !ok || sf.PID != 4242 || sf.Mode != "default" || sf.ConfigDir != "~/.claude-2" || sf.HumanAt != nil || sf.EndedAt != nil || sf.V != 1 {
		t.Fatalf("after start: %+v", sf)
	}
	// An event that changes nothing writes nothing.
	if wrote, _ := peers.UpdateSessionFile(c, ev("PreToolUse", "default", "")); wrote {
		t.Error("PreToolUse with the same mode must not write")
	}
	// A human prompt.
	c.Now = f.now.Add(time.Minute)
	if wrote, _ := peers.UpdateSessionFile(c, ev("UserPromptSubmit", "default", "fix the bug")); !wrote {
		t.Fatal("human prompt not recorded")
	}
	sf, _ = peers.ReadSessionFile(f.state, "sess-1")
	if sf.HumanAt == nil || !sf.HumanAt.Equal(c.Now) {
		t.Fatalf("human_at %v", sf.HumanAt)
	}
	// A peer message that started a turn is not a human.
	c.Now = f.now.Add(2 * time.Minute)
	peers.UpdateSessionFile(c, ev("UserPromptSubmit", "default", "<cross-session-message from=...>"))
	sf, _ = peers.ReadSessionFile(f.state, "sess-1")
	if !sf.HumanAt.Equal(f.now.Add(time.Minute)) {
		t.Errorf("a peer prompt moved human_at to %v", sf.HumanAt)
	}
	// The permission mode changes on any event.
	c.Now = f.now.Add(3 * time.Minute)
	if wrote, _ := peers.UpdateSessionFile(c, ev("PreToolUse", "bypassPermissions", "")); !wrote {
		t.Error("a mode change must write")
	}
	sf, _ = peers.ReadSessionFile(f.state, "sess-1")
	if sf.Mode != "bypassPermissions" || sf.ModeAt == nil || !sf.ModeAt.Equal(c.Now) {
		t.Errorf("mode: %+v", sf)
	}
	if peers.ModeClass(sf, true) != peers.ModeBypass {
		t.Error("bypassPermissions is the bypass class")
	}
	c.Now = f.now.Add(4 * time.Minute)
	peers.UpdateSessionFile(c, ev("SessionEnd", "", ""))
	sf, _ = peers.ReadSessionFile(f.state, "sess-1")
	if sf.EndedAt == nil {
		t.Error("SessionEnd must set ended_at")
	}
	if peers.ModeClass(sf, true) != peers.ModePrompting {
		t.Error("an ended session counts as prompting")
	}
}

// The pid lookup is an ancestor walk (on macOS one ps per step): a pid that was not found
// must not be looked for again on every later hook event.
func TestSessionFilePIDLookupOnlyOnStartAndNewFile(t *testing.T) {
	f := newFx(t)
	calls := 0
	c := peers.HookContext{State: f.state, ConfigDir: "~/.claude-2", Now: f.now, FindPID: func() int { calls++; return 0 }}
	ev := func(name string) peers.HookEvent {
		return peers.HookEvent{SessionID: "s-pid", Name: name, PermissionMode: "default", Prompt: "x"}
	}
	peers.UpdateSessionFile(c, ev("UserPromptSubmit")) // a new file: looked up once
	for _, n := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop"} {
		peers.UpdateSessionFile(c, ev(n))
	}
	if calls != 1 {
		t.Errorf("looked up %d times, want 1", calls)
	}
	peers.UpdateSessionFile(c, ev("SessionStart"))
	if calls != 2 {
		t.Errorf("SessionStart must look up again (%d)", calls)
	}
}

// countProc counts the processes read.
type countProc struct {
	peers.Proc
	n int
}

func (c *countProc) Info(pid int) (peers.ProcInfo, error) { c.n++; return c.Proc.Info(pid) }

func TestNearestClaudeStopsAtTheFirst(t *testing.T) {
	f := newFx(t)
	f.proc(9, 1, "init-ish", "", "x", 1)
	for pid := 10; pid < 20; pid++ {
		f.proc(pid, pid-1, "zsh", "", "zsh", pid)
	}
	f.claudeProc(20, 19, "", 20)
	f.proc(21, 20, "sh", "/bin/sh", "sh", 21)
	p := &countProc{Proc: peers.LinuxProc{Root: f.procRoot}}
	if pid := peers.NearestClaude(p, 21); pid != 20 || p.n != 2 {
		t.Errorf("pid %d after %d reads", pid, p.n)
	}
	if pid := peers.NearestClaude(p, 15); pid != 0 {
		t.Errorf("no claude above 15, got %d", pid)
	}
}

func TestSessionFileHasNoPromptAndRestrictedMode(t *testing.T) {
	f := newFx(t)
	c := hookCtx(f, 1)
	peers.UpdateSessionFile(c, peers.HookEvent{SessionID: "s2", Name: "UserPromptSubmit", PermissionMode: "plan", Prompt: "TOP-SECRET-PROMPT"})
	p := filepath.Join(peers.SessionsDir(f.state), "s2.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRET") {
		t.Error("the prompt text must never be stored")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	for _, class := range []string{"default", "acceptEdits", "plan", "auto", "dontAsk", "weird", ""} {
		if peers.ModeClass(peers.SessionFile{Mode: class}, true) != peers.ModePrompting {
			t.Errorf("%q must be prompting", class)
		}
	}
	if peers.ModeClass(peers.SessionFile{}, false) != peers.ModePrompting {
		t.Error("no file is prompting")
	}
}

func TestDeliveredWindowFallback(t *testing.T) {
	f := newFx(t)
	c := hookCtx(f, 1)
	if err := peers.MarkDelivered(f.state, "s3", f.now); err != nil {
		t.Fatal(err)
	}
	c.Now = f.now.Add(5 * time.Second)
	peers.UpdateSessionFile(c, peers.HookEvent{SessionID: "s3", Name: "UserPromptSubmit", PermissionMode: "default", Prompt: "message body without wrapper"})
	if sf, _ := peers.ReadSessionFile(f.state, "s3"); sf.HumanAt != nil {
		t.Error("a prompt 5 s after a delivery is not a human prompt")
	}
	c.Now = f.now.Add(30 * time.Second)
	peers.UpdateSessionFile(c, peers.HookEvent{SessionID: "s3", Name: "UserPromptSubmit", PermissionMode: "default", Prompt: "now a person types"})
	if sf, _ := peers.ReadSessionFile(f.state, "s3"); sf.HumanAt == nil {
		t.Error("a prompt 30 s after a delivery is human")
	}
}

func TestSessionFileRejectsUnsafeIDsAndPrunes(t *testing.T) {
	f := newFx(t)
	if _, ok := peers.ParseHookEvent([]byte(`{"session_id":"../evil","hook_event_name":"Stop"}`)); ok {
		t.Error("a path-like session id must be refused")
	}
	if e, ok := peers.ParseHookEvent([]byte(`{"session_id":"abc-1","hook_event_name":"Stop","permission_mode":"plan"}`)); !ok || e.PermissionMode != "plan" {
		t.Error("parse")
	}
	if _, ok := peers.ReadSessionFile(f.state, "../x"); ok {
		t.Error("read with unsafe id")
	}
	if err := peers.MarkDelivered(f.state, "../../escaped", f.now); err == nil {
		t.Error("a delivery stamp with an unsafe id must be refused")
	}
	if _, err := os.Stat(filepath.Join(f.state, "escaped")); err == nil {
		t.Error("the delivery stamp was written outside peers/delivered")
	}
	c := hookCtx(f, 1)
	peers.UpdateSessionFile(c, peers.HookEvent{SessionID: "old", Name: "SessionStart"})
	p := filepath.Join(peers.SessionsDir(f.state), "old.json")
	old := f.now.Add(-8 * 24 * time.Hour)
	os.Chtimes(p, old, old)
	peers.PruneSessionFiles(f.state, f.now)
	if _, err := os.Stat(p); err == nil {
		t.Error("file older than 7 days must be pruned")
	}
}

// A session that goes on after a SessionEnd (same id, same process; seen live on a tmux
// planner) is alive again on its next event, so a bypass sender is not framed as prompting
// and held by a bypass receiver. An event racing right after the end is not proof.
func TestSessionFileRevivedAfterEnd(t *testing.T) {
	f := newFx(t)
	c := hookCtx(f, 4242)
	ev := func(name string) peers.HookEvent {
		return peers.HookEvent{SessionID: "sess-r", Name: name, PermissionMode: "bypassPermissions", Prompt: "go on"}
	}
	peers.UpdateSessionFile(c, ev("SessionStart"))
	c.Now = f.now.Add(time.Minute)
	peers.UpdateSessionFile(c, ev("SessionEnd"))
	c.Now = f.now.Add(time.Minute + 5*time.Second)
	peers.UpdateSessionFile(c, ev("Stop"))
	sf, _ := peers.ReadSessionFile(f.state, "sess-r")
	if sf.EndedAt == nil {
		t.Fatal("an event 5 s after the end must not revive the session")
	}
	c.Now = f.now.Add(10 * time.Minute)
	if wrote, _ := peers.UpdateSessionFile(c, ev("UserPromptSubmit")); !wrote {
		t.Fatal("a later event must write the revival")
	}
	sf, _ = peers.ReadSessionFile(f.state, "sess-r")
	if sf.EndedAt != nil || sf.PID != 4242 {
		t.Fatalf("not revived: %+v", sf)
	}
	if peers.ModeClass(sf, true) != peers.ModeBypass {
		t.Error("a revived bypass session is the bypass class again")
	}
}
