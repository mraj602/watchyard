package peers

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
)

// Env is everything the local peers code looks at, all injectable so tests run in a temp
// HOME and XDG_RUNTIME_DIR with a fake /proc and never touch the user's real sessions.
type Env struct {
	Home    string // user home folder (slots live directly in it)
	State   string // <state>
	Runtime string // $XDG_RUNTIME_DIR ("" means /run/user/<uid>)
	Machine identity.Machine
	// SlotExtra and SlotLabels are config.toml's slots.extra and slots.labels.
	SlotExtra  []string
	SlotLabels map[string]string

	Proc     Proc
	StartPID int // where the ancestor walk starts: os.Getppid()
	// PeerAlive, when set, reports whether the process that connected to the agent's socket
	// is still alive (its pidfd, SO_PEERPIDFD): the trusted sender's checks call it last, so
	// a pid reused while /proc was read does not pass (peers.md 2.3, 4.1a).
	PeerAlive func() bool
	FS        identity.FS
	Matrix    Matrix
	Now       func() time.Time
	// Alive reports whether a registry's socket accepts a connection (default: the 250 ms
	// connect of peers.md 3.1).
	Alive func(sock string) bool
	// NewID makes a message id (uuid v4); tests fix it.
	NewID func() string
	// Remote reaches other machines through the agent's link; nil: this machine only.
	Remote Remote
	// PastSlots lists the slot folders a session ran in before Watchyard moved it to another
	// account (setup.md 8.6), newest first; nil: none known.
	PastSlots func(sessionID string) []string
	// MarkerTags are tags this machine reads as settings (the account.smart_switch_scope tag):
	// adding one sends no notice to other holders (peers.md 3.4). no-move always is one.
	MarkerTags []string
	// Hosts are other coding agents whose sessions are listed and messaged like Claude's.
	Hosts []AgentHost
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) alive(sock string) bool {
	if e.Alive != nil {
		return e.Alive(sock)
	}
	return Alive(sock)
}

func (e Env) fs() identity.FS {
	if e.FS != nil {
		return e.FS
	}
	return identity.OSFS{}
}

func (e Env) locator() identity.Locator { return identity.Locator{Home: e.Home, FS: e.fs()} }

// Slots are the config folders wy manages (identity.md 3), without CLAUDE_CONFIG_DIR.
func (e Env) Slots() []identity.Slot {
	s, _ := e.locator().Discover(e.SlotExtra, func(string) string { return "" })
	return s
}

// SlotFor is the slot for a CLAUDE_CONFIG_DIR value as Claude Code sees it: unset means
// ~/.claude (the default view); set, even to ~/.claude, is the explicit view
// (identity.md 3).
func (e Env) SlotFor(value string, set bool) identity.Slot {
	loc := e.locator()
	if !set {
		return loc.SlotFor(filepath.Join(e.Home, ".claude"))
	}
	return loc.Current(func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return value
		}
		return ""
	})
}

// managed returns the managed slot with the same folder as s.
func (e Env) managed(s identity.Slot) (identity.Slot, bool) {
	for _, m := range e.Slots() {
		if m.Dir == s.Dir {
			return m, true
		}
		a, err1 := e.fs().EvalSymlinks(m.Dir)
		b, err2 := e.fs().EvalSymlinks(s.Dir)
		if err1 == nil && err2 == nil && a == b {
			return m, true
		}
	}
	return identity.Slot{}, false
}

// Caller is the session `wy peers send` runs in (peers.md 4.1).
type Caller struct {
	ClaudePID int
	Slot      identity.Slot
	Entry     RegistryEntry
}

// Whoami finds the calling session: the nearest ancestor of StartPID that is a Claude Code
// process with a registry file in a slot wy manages.
//
// Linux: the config folder is CLAUDE_CONFIG_DIR read from the claude process's environment
// (that one variable only; unset means ~/.claude), and `<config>/sessions/<pid>.json` must
// exist with a start time matching the live process. macOS cannot read another process's
// environment: the one managed slot whose registry holds `<pid>.json` with the matching
// start time is used, and more than one is a refusal. The calling process's own environment
// is never consulted. No candidate: not_a_session (exit 4).
//
// The walk never skips past a Claude Code process it cannot identify (StopsWalk): a
// `claude -p` started from inside another session (its Bash tool, a script) has no registry
// file of its own and inherits the parent's environment, and continuing the walk would send
// as the parent session. Such a caller is refused instead. Only a process that merely looks
// like Claude Code by name (a wrapper script called `claude`) is passed over.
func (e Env) Whoami() (Caller, *Refusal) {
	if e.Proc == nil {
		return Caller{}, Refuse(CodeUnsupported, "%v", ErrUnsupported)
	}
	cands := ClaudeCandidates(e.Proc, e.StartPID)
	if len(cands) == 0 {
		return Caller{}, Refuse(CodeNotASession, "wy peers send works only inside a Claude Code session")
	}
	reason := "no registry file for the Claude Code process above this command"
	for _, c := range cands {
		live, _ := e.Proc.StartTime(c.PID)
		dir, set, err := e.Proc.ConfigDir(c.PID)
		switch {
		case err == nil:
			slot, ok := e.managed(e.SlotFor(dir, set))
			if !ok {
				reason = "its config folder is not one Watchyard manages"
				break
			}
			ent, err := ReadRegistryPID(e.fs(), slot, c.PID)
			if err != nil {
				reason = "no registry file for the Claude Code process above this command"
				break
			}
			if !ProcStartMatches(ent.ProcStartString(), live) {
				reason = "its registry file belongs to an older process with the same pid"
				break
			}
			return Caller{ClaudePID: c.PID, Slot: slot, Entry: ent}, nil
		case err == ErrNoEnv:
			var found []Caller
			for _, slot := range e.Slots() {
				ent, err := ReadRegistryPID(e.fs(), slot, c.PID)
				if err == nil && ProcStartMatches(ent.ProcStartString(), live) {
					found = append(found, Caller{ClaudePID: c.PID, Slot: slot, Entry: ent})
				}
			}
			if len(found) == 1 {
				return found[0], nil
			}
			if len(found) > 1 {
				return Caller{}, Refuse(CodeNotASession, "more than one config folder holds a registry file for pid %d", c.PID)
			}
			reason = "no registry file for the Claude Code process above this command"
		default:
			reason = "cannot read the Claude Code process's configuration folder"
		}
		if e.StopsWalk(c) {
			return Caller{}, Refuse(CodeNotASession,
				"the nearest Claude Code process (pid %d) is not a session Watchyard can identify (%s; a `claude -p` started inside another session has none), so this does not send as the session above it",
				c.PID, reason)
		}
	}
	return Caller{}, Refuse(CodeNotASession, "not inside a session Watchyard can identify: %s", reason)
}

// StopsWalk reports whether the Whoami walk must stop at candidate c when c cannot be
// identified as a session: c is Claude Code itself (IsClaudeProgram, not a wrapper that is
// only named claude), or a hook of a live session recorded c's pid in its session file
// (2.4). A nested `claude -p` is such a process: it is a session of its own, with its own
// hooks and session id, so the session above it must not be used in its place.
func (e Env) StopsWalk(c ProcInfo) bool {
	return IsClaudeProgram(c) || e.knownSessionPID(c.PID)
}

// knownSessionPID reports whether a session file of a session that has not ended names pid.
func (e Env) knownSessionPID(pid int) bool {
	if e.State == "" || pid <= 0 {
		return false
	}
	ents, err := os.ReadDir(SessionsDir(e.State))
	if err != nil {
		return false
	}
	for _, d := range ents {
		id, ok := strings.CutSuffix(d.Name(), ".json")
		if !ok {
			continue
		}
		if f, ok := ReadSessionFile(e.State, id); ok && f.PID == pid && f.EndedAt == nil {
			return true
		}
	}
	return false
}

// OffPath is <state>/peers/off (peers.md 11).
func (e Env) OffPath() string { return filepath.Join(e.State, "peers", "off") }

// Off reports whether `wy peers off` is in force on this machine.
func (e Env) Off() bool {
	_, err := os.Stat(e.OffPath())
	return err == nil
}

// SetOff writes or removes the off file.
func (e Env) SetOff(off bool) error {
	if !off {
		err := os.Remove(e.OffPath())
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.OffPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(e.OffPath(), []byte(e.now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// ThreadsPath, RatesPath and AuditPath are the files of peers.md 6.2, 9.2 and 10.
func (e Env) ThreadsPath() string { return filepath.Join(e.State, "peers", "threads.json") }
func (e Env) RatesPath() string   { return filepath.Join(e.State, "peers", "limits.json") }
func (e Env) AuditPath() string   { return filepath.Join(e.State, "peers", "audit.jsonl") }

// loginReader reads a slot's login from the cache the hooks keep (identity.md 4): no claude
// process is run and no credential file is opened.
func (e Env) loginReader() func(identity.Slot) (identity.Login, error) {
	st := &identity.Store{StateDir: e.State, FS: e.fs()}
	return st.Login
}

// tildeDir writes a folder as the slot name form.
func (e Env) tildeDir(dir string) string {
	if dir == "" {
		return ""
	}
	return identity.Tilde(e.Home, dir)
}

// HandlePart cleans one part of a handle (name, account label or machine name), so the
// local and the hub's directories make the same handles (peers.md 3.2).
func HandlePart(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '@' || r == '"' || r == '<' || r == '>' || r == '\'' || r == '`':
			return -1
		case r <= ' ' || r == 0x7f:
			return '-'
		}
		return r
	}, strings.TrimSpace(s))
	return strings.Trim(s, "-.")
}

// CanonicalLabel is the account part of a canonical handle (peers.md 3.2): the hub's seat
// label, else the first 8 characters of the account uuid. The hub and every agent make it
// the same way, so a session has one handle on every machine.
func CanonicalLabel(seatLabel, account string) string {
	if l := HandlePart(seatLabel); l != "" {
		return l
	}
	if len(account) > 8 {
		account = account[:8]
	}
	return HandlePart(account)
}

// FolderLabel is the label a machine shows for a slot when no label is set for it: the
// folder's name without its dot ("~/.claude-2" → "claude-2").
func FolderLabel(folder string) string {
	folder = strings.TrimRight(strings.ReplaceAll(folder, `\`, "/"), "/")
	if i := strings.LastIndexByte(folder, '/'); i >= 0 {
		folder = folder[i+1:]
	}
	return HandlePart(strings.TrimPrefix(folder, "."))
}

// MakeHandle is `<name>@<label>.<machine>` from parts already cleaned by HandlePart.
func MakeHandle(name, label, machine string) string {
	return name + "@" + label + "." + machine
}

// sessionVars are the variables Claude Code gives the processes of one session (its Bash
// tool, hooks): its id, pid, inbox socket and token, entry point and the like. A session
// started from inside another one must not inherit them (setup.md 8.5 --exec, 8.6).
var sessionVars = map[string]bool{
	"CLAUDECODE": true, "CLAUDE_PID": true, "CLAUDE_EFFORT": true, "CLAUDE_ENV_FILE": true,
	"CLAUDE_CODE_SESSION_ID": true, "CLAUDE_CODE_MESSAGING_SOCKET": true, "CLAUDE_CODE_MESSAGING_TOKEN": true,
	"CLAUDE_CODE_CHILD_SESSION": true, "CLAUDE_CODE_CHILD_NOTES_DIR": true, "CLAUDE_CODE_ENTRYPOINT": true,
	"CLAUDE_CODE_EXECPATH": true, "CLAUDE_CODE_SESSION_ATTENDED": true, "CLAUDE_CODE_SSE_PORT": true,
}

// WithoutSessionEnv is env without the variables of the session it may run inside
// (sessionVars, CLAUDE_AGENT_SDK*) and, with configDir, without CLAUDE_CONFIG_DIR. The
// owner's own CLAUDE_CODE_* settings (a model, a provider) stay. Only this process's own
// environment is ever passed in (keys.md 8).
func WithoutSessionEnv(env []string, configDir bool) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if sessionVars[k] || strings.HasPrefix(k, "CLAUDE_AGENT_SDK") || (configDir && k == "CLAUDE_CONFIG_DIR") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
