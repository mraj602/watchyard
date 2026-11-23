package hook

import (
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/mraj602/watchyard/internal/clientenv"
	"github.com/mraj602/watchyard/internal/identity"
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

// PickerContextText is the account-picker part of the SessionStart paragraph, given when this
// machine has more than one logged-in Claude Code account, whether or not peers are on
// (peers.md 2.5). It is a constant, like the peers part.
const PickerContextText = "Starting another Claude Code session: let Watchyard choose the account, never pick one " +
	"by hand: `wy account next --session-id <new uuid> --exec -- claude <args>` (exit 4: no account is free now; " +
	"it says until when)."

// RunPeersContext is `wy hook --peers-context <Event>`: on SessionStart it prints the
// SessionStart hook JSON whose additionalContext is peers.StartContextHeader, then
// peers.StartContextText when [peers] enabled is true and the local matrix lets this
// session's account reach another account (A), then PickerContextText when this machine has
// more than one logged-in account (B). Either condition alone is enough (peers.md 2.5); with
// neither it prints nothing. It reads only
// local files (config, the slots' cached logins, the off file, the accepted matrix), never
// runs claude or connects to anything, never writes a record, and swallows every error and
// panic (the hook is synchronous, with a 1 s timeout). args are the arguments after the flag.
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
	parts := []string{peers.StartContextHeader}
	if env.Config.Peers.Enabled && env.Login.HasSeat {
		if t := peers.StartContext(env.Dirs.State.Path, env.Login.Seat.Account); t != "" {
			parts = append(parts, t)
		}
	}
	if severalAccounts(env) {
		parts = append(parts, PickerContextText)
	}
	if len(parts) == 1 {
		return
	}
	text := strings.Join(parts, " ")
	var out sessionStartOutput
	out.HookSpecificOutput.HookEventName = "SessionStart"
	out.HookSpecificOutput.AdditionalContext = text
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	_, _ = stdout.Write(append(b, '\n'))
}

// severalAccounts reports whether this machine's config folders (identity.md 3) hold logins to
// more than one account, by the cached logins only (identity.Login.LoggedIn; no claude run).
func severalAccounts(env *clientenv.Env) bool {
	home := env.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" || env.Store == nil {
		return false
	}
	slots, err := identity.Locator{Home: home, FS: identity.OSFS{}}.Discover(env.Config.Slots.Extra, env.Getenv)
	if err != nil {
		return false
	}
	seen := map[[2]string]bool{}
	for _, s := range slots {
		l, err := env.Store.Login(s)
		if err != nil || !l.LoggedIn() {
			continue
		}
		if seen[l.Seat.Key()] = true; len(seen) > 1 {
			return true
		}
	}
	return false
}
