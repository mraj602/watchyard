package hook

import (
	"os"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/clientenv"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/record"
)

// peerDerived adds the peers facts the alert rules read (push.md 3.5, peers.md 6.4) to a hook
// record's derived fields:
//
//   - derived.peer_prompt = true on a UserPromptSubmit that a peer message started: the prompt
//     starts with the <cross-session-message> wrapper, or it came within peers.DeliveredWindow
//     of a delivery into this session (peers.md 2.4). This holds whether or not [peers] is
//     enabled, since Claude Code's own /peers messages use the same wrapper.
//   - derived.move_prompt = true on a UserPromptSubmit whose prompt is a Watchyard move note
//     (it starts with mover.NotePrefix): the resume prompt of a moved session, not the owner
//     typing (setup.md 8.6, Interactive).
//   - derived.peer_wait = true on a Stop while the session waits on another session's reply:
//     it sent the newest message of an open thread after its own newest human prompt
//     (peers.ThreadSet.AwaitingReply). Only with [peers] enabled (there are no threads
//     otherwise).
//
// Only the prompt's prefix is compared; nothing of it is kept. It does at most two small
// reads and never waits.
func peerDerived(env *clientenv.Env, payload []byte, d record.Derived, now time.Time) {
	ev, ok := peers.ParseHookEvent(payload)
	if !ok {
		return
	}
	state := env.Dirs.State.Path
	switch ev.Name {
	case "UserPromptSubmit":
		if !peers.IsHumanPrompt(ev.Prompt, now, peers.LastDelivery(state, ev.SessionID)) {
			d.Set("peer_prompt", true)
		}
		if strings.HasPrefix(ev.Prompt, mover.NotePrefix) {
			d.Set("move_prompt", true)
		}
	case "Stop":
		if !env.Config.Peers.Enabled {
			return
		}
		var human time.Time
		if f, ok := peers.ReadSessionFile(state, ev.SessionID); ok && f.HumanAt != nil {
			human = *f.HumanAt
		}
		if peers.LoadThreads(peers.Env{State: state}.ThreadsPath()).AwaitingReply(ev.SessionID, human, now) {
			d.Set("peer_wait", true)
		}
	}
}

// recordPeers writes the session's peers file (<state>/peers/sessions/<session>.json,
// peers.md 2.4) when [peers] enabled is true. It does one small read and at most one atomic
// write, never connects to anything and never waits (hooks never wait). The prompt is only
// compared against the peer wrapper prefix and is never stored. Errors are logged, never
// returned.
func recordPeers(env *clientenv.Env, payload []byte, getenv func(string) string, event string) {
	if !env.Config.Peers.Enabled {
		return
	}
	ev, ok := peers.ParseHookEvent(payload)
	if !ok {
		return
	}
	proc := peers.NewProc()
	_, err := peers.UpdateSessionFile(peers.HookContext{
		State:     env.Dirs.State.Path,
		ConfigDir: env.Slot.Name,
		Now:       time.Now(),
		// The nearest Claude-looking ancestor of the hook (keys.md 8's walk). Only asked on
		// SessionStart or when the file is new.
		FindPID: func() int { return peers.NearestClaude(proc, os.Getppid()) },
	}, ev)
	if err != nil {
		logError(env, getenv, event, err)
	}
}
