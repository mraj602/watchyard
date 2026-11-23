package peers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// DeliveredWindow is the fallback of peers.md 2.4 (lab E12): a UserPromptSubmit within this
// long after the agent delivered into a session is not counted as a human prompt.
const DeliveredWindow = 10 * time.Second

// SessionFile is `<state>/peers/sessions/<session_id>.json` (peers.md 2.4), written by
// `wy hook` when peers are enabled. It holds no prompt text.
type SessionFile struct {
	V         int        `json:"v"`
	Session   string     `json:"session"`
	PID       int        `json:"pid"`
	ConfigDir string     `json:"config_dir"`
	Mode      string     `json:"mode"`
	ModeAt    *time.Time `json:"mode_at"`
	HumanAt   *time.Time `json:"human_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// SessionsDir is <state>/peers/sessions.
func SessionsDir(state string) string { return filepath.Join(state, "peers", "sessions") }

// DeliveredPath is <state>/peers/delivered/<session>.
func DeliveredPath(state, session string) string {
	return filepath.Join(state, "peers", "delivered", session)
}

// ReadSessionFile reads one session's file; ok is false when it is missing, torn or the id
// is not a safe file name.
func ReadSessionFile(state, session string) (SessionFile, bool) {
	if !safeID.MatchString(session) {
		return SessionFile{}, false
	}
	b, err := os.ReadFile(filepath.Join(SessionsDir(state), session+".json"))
	if err != nil {
		return SessionFile{}, false
	}
	var f SessionFile
	if json.Unmarshal(b, &f) != nil || f.Session != session {
		return SessionFile{}, false
	}
	return f, true
}

// ModeKnown reports whether a live session's file names a permission mode Watchyard knows,
// so ModeClass is its real class rather than the prompting default (peers.md 4.1a: the
// trusted sender is framed in the receiving class and needs it known).
func ModeKnown(f SessionFile, ok bool) bool {
	if !ok || f.EndedAt != nil {
		return false
	}
	switch f.Mode {
	case "bypassPermissions", "default", "acceptEdits", "plan", "auto", "dontAsk":
		return true
	}
	return false
}

// ModeClass is the sender's real permission class (peers.md 8.2): bypass when the session
// file says bypassPermissions, otherwise prompting (default, acceptEdits, plan, auto,
// dontAsk, anything unknown, or no file).
func ModeClass(f SessionFile, ok bool) Mode {
	if ok && f.Mode == "bypassPermissions" && f.EndedAt == nil {
		return ModeBypass
	}
	return ModePrompting
}

// HookEvent is the part of a hook payload peers uses.
type HookEvent struct {
	SessionID      string `json:"session_id"`
	Name           string `json:"hook_event_name"`
	PermissionMode string `json:"permission_mode"`
	Prompt         string `json:"prompt"`
}

// ParseHookEvent decodes the fields above from a hook payload.
func ParseHookEvent(payload []byte) (HookEvent, bool) {
	var e HookEvent
	if json.Unmarshal(payload, &e) != nil || !safeID.MatchString(e.SessionID) {
		return HookEvent{}, false
	}
	return e, true
}

// IsHumanPrompt: a prompt that does not start with the peer wrapper, and does not fall
// inside DeliveredWindow of an agent delivery (lastDelivery zero: none).
func IsHumanPrompt(prompt string, now, lastDelivery time.Time) bool {
	if strings.HasPrefix(prompt, "<"+WrapperTag) {
		return false
	}
	return lastDelivery.IsZero() || now.Sub(lastDelivery) > DeliveredWindow
}

// LastDelivery reads the agent's delivery stamp for a session; zero when absent.
func LastDelivery(state, session string) time.Time {
	fi, err := os.Stat(DeliveredPath(state, session))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// MarkDelivered records that a message was just written into a session's inbox (peers.md
// 2.4's fallback). It only touches the file's time. The session id comes from a registry
// file, so one that is not a safe file name is refused.
func MarkDelivered(state, session string, now time.Time) error {
	if !safeID.MatchString(session) {
		return fmt.Errorf("session id %q is not a safe file name", session)
	}
	p := DeliveredPath(state, session)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		return err
	}
	return os.Chtimes(p, now, now)
}

// HookContext is what the hook knows about its caller.
type HookContext struct {
	State     string
	ConfigDir string // the slot name, e.g. "~/.claude-2"
	Now       time.Time
	// FindPID returns the claude pid above this hook (0 when not found). It is called only
	// on SessionStart and when the file is new, never on every event: on macOS each step of
	// the walk runs ps, and a pid that was not found once is not found the next time either.
	FindPID func() int
}

func (c HookContext) findPID() int {
	if c.FindPID == nil {
		return 0
	}
	return c.FindPID()
}

// UpdateSessionFile applies one hook event to the session's file and writes it (atomic
// rename, 0600) only when something changed. It never reads the prompt beyond its prefix
// and never stores it. It does one small read and at most one write: a hook never waits on
// anything else (peers.md 2.4).
func UpdateSessionFile(c HookContext, e HookEvent) (wrote bool, err error) {
	f, had := ReadSessionFile(c.State, e.SessionID)
	changed := !had
	if !had {
		f = SessionFile{V: 1, Session: e.SessionID}
	}
	now := c.Now.UTC()
	if f.ConfigDir != c.ConfigDir {
		f.ConfigDir, changed = c.ConfigDir, true
	}
	if e.PermissionMode != "" && e.PermissionMode != f.Mode {
		f.Mode, f.ModeAt, changed = e.PermissionMode, &now, true
	}
	switch e.Name {
	case "SessionStart":
		if f.EndedAt != nil {
			f.EndedAt, changed = nil, true
		}
		if pid := c.findPID(); pid > 0 && pid != f.PID {
			f.PID, changed = pid, true
		}
	case "UserPromptSubmit":
		if IsHumanPrompt(e.Prompt, c.Now, LastDelivery(c.State, e.SessionID)) {
			f.HumanAt, changed = &now, true
		}
	case "SessionEnd":
		if f.EndedAt == nil {
			f.EndedAt, changed = &now, true
		}
	default:
		if !had && e.PermissionMode == "" {
			return false, nil // nothing worth a file yet
		}
	}
	if !had && e.Name != "SessionStart" {
		if pid := c.findPID(); pid > 0 {
			f.PID, changed = pid, true
		}
	}
	if !changed {
		return false, nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(SessionsDir(c.State), 0o700); err != nil {
		return false, err
	}
	return true, atomicfile.Write(filepath.Join(SessionsDir(c.State), e.SessionID+".json"), b, 0o600)
}

// PruneSessionFiles removes session files not modified for 7 days (called by wy tick).
func PruneSessionFiles(state string, now time.Time) {
	ents, err := os.ReadDir(SessionsDir(state))
	if err != nil {
		return
	}
	for _, d := range ents {
		if fi, err := d.Info(); err == nil && now.Sub(fi.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(filepath.Join(SessionsDir(state), d.Name()))
		}
	}
}
