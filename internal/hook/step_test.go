package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/clientenv"
	"github.com/mraj602/watchyard/internal/mover"
)

// wy hook --step records a session's tool calls as steps (setup.md 8.6, Between steps); a
// background shell is not a step; nothing goes to the outbox.
func TestRunStep(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WY_HOME", filepath.Join(home, "wy"))
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("WY_STATE_DIR", "")
	run := func(event, in string) { RunStep([]string{event}, strings.NewReader(in), os.Getenv) }
	dir, err := clientenv.StateDir(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	st := mover.NewState(dir)
	run("PreToolUse", `{"session_id":"s1","tool_name":"Bash","tool_use_id":"toolu_1","tool_input":{"command":"sleep 60 && echo x >> f"}}`)
	if s := st.StepNow("s1", time.Now()); !s.In || s.Tool != "Bash" || s.Cmd != "sleep 60 && echo x >> f" {
		t.Fatalf("pre: %+v", s)
	}
	run("PostToolUse", `{"session_id":"s1","tool_name":"Bash","tool_use_id":"toolu_1"}`)
	if s := st.StepNow("s1", time.Now()); !s.Known || s.In {
		t.Fatalf("post: %+v", s)
	}
	run("PreToolUse", `{"session_id":"s1","tool_name":"Bash","tool_use_id":"toolu_2","tool_input":{"command":"npm run dev","run_in_background":true}}`)
	if s := st.StepNow("s1", time.Now()); s.In {
		t.Fatalf("a background shell counted as a step: %+v", s)
	}
	run("PreToolUse", `{"session_id":"s1","tool_name":"Bash","tool_use_id":"toolu_3","tool_input":{"command":"x"}}`)
	run("PostToolUseFailure", `{"session_id":"s1","tool_name":"Bash","tool_use_id":"toolu_3"}`)
	if s := st.StepNow("s1", time.Now()); s.In {
		t.Fatalf("a failed tool call still in flight: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "outbox")); err == nil {
		t.Error("a step event went to the outbox")
	}
}
