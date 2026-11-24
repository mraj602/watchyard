package hook

import (
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// derivedOf is derived.<key> of the newest hook record ("" when absent).
func (sb *sandbox) derivedOf(key string) string {
	sb.t.Helper()
	recs := sb.ofKind("hook_event")
	if len(recs) == 0 {
		sb.t.Fatal("no hook record")
	}
	return string(recs[len(recs)-1].Derived[key])
}

func TestHookMarksPeerPrompts(t *testing.T) {
	sb := newSandbox(t)
	// Peers off: Claude Code's own /peers messages use the same wrapper, so it still counts.
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-1","prompt":"<cross-session-message from=\"uds:x\">\nhi\n</cross-session-message>"}`))
	if got := sb.derivedOf("peer_prompt"); got != "true" {
		t.Fatalf("peer_prompt = %q", got)
	}
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-1","prompt":"please continue"}`))
	if got := sb.derivedOf("peer_prompt"); got != "" {
		t.Fatalf("a person's prompt got peer_prompt = %q", got)
	}
	// The delivery-window fallback (peers.md 2.4).
	if err := peers.MarkDelivered(sb.state(), "sess-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-1","prompt":"continue"}`))
	if got := sb.derivedOf("peer_prompt"); got != "true" {
		t.Fatalf("a prompt right after a delivery: peer_prompt = %q", got)
	}
	for _, r := range sb.ofKind("hook_event") {
		if strings.Contains(string(r.Payload), "cross-session") {
			t.Fatal("the prompt must never be stored")
		}
	}
}

func TestHookMarksStopWaitingOnAPeer(t *testing.T) {
	sb := newSandbox(t)
	enablePeers(t, sb)
	stop := []byte(`{"session_id":"sess-a","stop_hook_active":false,"last_assistant_message":"Asked api. Want me to wait?"}`)
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-a","prompt":"ask api about the schema"}`))
	sb.run("Stop", stop)
	if got := sb.derivedOf("peer_wait"); got != "" {
		t.Fatalf("no thread: peer_wait = %q", got)
	}

	now := time.Now()
	path := peers.Env{State: sb.state()}.ThreadsPath()
	ts := peers.LoadThreads(path)
	ts.Start("sess-a", "sess-b", "acc-a", "acc-b", now).RecordMessageFrom("sess-a", now)
	if err := ts.Save(path); err != nil {
		t.Fatal(err)
	}
	sb.run("Stop", stop)
	if got := sb.derivedOf("peer_wait"); got != "true" {
		t.Fatalf("sess-a sent the last message: peer_wait = %q", got)
	}

	// The reply came: sess-b owes nothing, sess-a is not waiting any more.
	ts = peers.LoadThreads(path)
	ts.FindOpen("sess-a", "sess-b").RecordMessageFrom("sess-b", now.Add(time.Second))
	if err := ts.Save(path); err != nil {
		t.Fatal(err)
	}
	sb.run("Stop", stop)
	if got := sb.derivedOf("peer_wait"); got != "" {
		t.Fatalf("after the reply: peer_wait = %q", got)
	}
}

// A move note as a resume prompt is marked move_prompt: it is not the owner typing (setup.md
// 8.6, Interactive).
func TestHookMarksMovePrompts(t *testing.T) {
	sb := newSandbox(t)
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-m","prompt":"Watchyard moved this session from claude-3 to claude-2 mid-turn at 14:05. Carry on with your task."}`))
	if got := sb.derivedOf("move_prompt"); got != "true" {
		t.Fatalf("move_prompt = %q", got)
	}
	sb.run("UserPromptSubmit", []byte(`{"session_id":"sess-m","prompt":"what did Watchyard moved this session mean?"}`))
	if got := sb.derivedOf("move_prompt"); got != "" {
		t.Fatalf("an owner prompt got move_prompt = %q", got)
	}
}
