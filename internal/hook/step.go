package hook

import (
	"encoding/json"
	"io"
	"time"

	"github.com/mraj602/watchyard/internal/clientenv"
	"github.com/mraj602/watchyard/internal/mover"
)

// StepFlag is `wy hook --step <Event>`: PreToolUse, PostToolUse and PostToolUseFailure of
// every tool, recorded only as the session's steps for Smart Account Switch (setup.md 8.6,
// Between steps): a mid-turn move waits for the step in flight to end. Nothing goes to the
// outbox; it reads only the state folder and writes one line.
const StepFlag = "--step"

// RunStep records one step event. It never fails the caller and prints nothing.
func RunStep(args []string, stdin io.Reader, getenv func(string) string) {
	defer func() { _ = recover() }()
	in, err := readStdinWait(stdin, stdinWait)
	if err != nil {
		return
	}
	var p struct {
		SessionID string          `json:"session_id"`
		Event     string          `json:"hook_event_name"`
		Tool      string          `json:"tool_name"`
		ToolUseID string          `json:"tool_use_id"`
		Input     json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(in, &p) != nil || p.SessionID == "" || p.ToolUseID == "" {
		return
	}
	event := p.Event
	if len(args) > 0 {
		event = args[0]
	}
	dir, err := clientenv.StateDir(getenv)
	if err != nil {
		return
	}
	st := mover.NewState(dir)
	switch event {
	case "PreToolUse":
		// A background shell is not a step: the call returns at once and the shell runs on.
		var bg struct {
			RunInBackground bool `json:"run_in_background"`
		}
		if json.Unmarshal(p.Input, &bg) == nil && bg.RunInBackground {
			return
		}
		_ = st.RecordStep(p.SessionID, p.ToolUseID, "pre", p.Tool, mover.StepCommand(p.Input), time.Now())
	case "PostToolUse", "PostToolUseFailure":
		_ = st.RecordStep(p.SessionID, p.ToolUseID, "post", p.Tool, "", time.Now())
	}
}
