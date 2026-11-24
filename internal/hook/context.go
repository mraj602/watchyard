package hook

import (
	"encoding/json"
	"io"
	"time"

	"github.com/mraj602/watchyard/internal/clientenv"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/peers"
)

// PeersContextFlag is the argument that turns `wy hook` into the SessionStart context entry:
// `wy hook --peers-context SessionStart` (docs/spec/peers.md 2.5).
const PeersContextFlag = "--peers-context"

// sessionStartOutput is Claude Code's SessionStart hook JSON output: additionalContext is
// added to the session's context.
type sessionStartOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// RunPeersContext is `wy hook --peers-context <Event>`: on SessionStart, when [peers] enabled
// is true and the local matrix lets this session's account reach another account, it prints
// the SessionStart hook JSON with peers.StartContextText as additionalContext; otherwise it
// prints nothing. It reads only local files (config, the slot's cached login, the off file,
// the accepted matrix) and the move note Smart Account Switch left for this session (which it
// removes), never connects to anything, never writes a record, and swallows every
// error and panic (the hook is synchronous, with a 1 s timeout). args are the arguments after
// the flag.
func RunPeersContext(args []string, stdin io.Reader, stdout io.Writer, getenv func(string) string) {
	defer func() { _ = recover() }()
	in, err := readStdinWait(stdin, titleStdinWait)
	if err != nil {
		return
	}
	var p struct {
		SessionID string `json:"session_id"`
		Event     string `json:"hook_event_name"`
	}
	if json.Unmarshal(in, &p) != nil {
		return
	}
	event := p.Event
	if len(args) > 0 && args[0] != "" {
		event = args[0]
	}
	if event != "SessionStart" {
		return
	}
	env, err := clientenv.Load(getenv)
	if err != nil {
		return
	}
	var text string
	if env.Config.Peers.Enabled && env.Login.HasSeat {
		text = peers.StartContext(env.Dirs.State.Path, env.Login.Seat.Account)
	}
	// A session Smart Account Switch moved gets its one line once, on the resume (setup.md
	// 8.6, The moved session); nothing about the feature is said to any other session.
	if note := mover.NewState(env.Dirs.State.Path).TakeNote(p.SessionID, time.Now()); note != "" {
		if text != "" {
			text += "\n"
		}
		text += note
	}
	if text == "" {
		return
	}
	var out sessionStartOutput
	out.HookSpecificOutput.HookEventName = "SessionStart"
	out.HookSpecificOutput.AdditionalContext = text
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	_, _ = stdout.Write(append(b, '\n'))
}
