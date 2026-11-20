package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// Seat is the login behind a slot: (account uuid, org uuid) plus display data
// (identity.md section 4). Src is "claude_json" or "auth_status".
type Seat struct {
	Account string `json:"account"`
	Org     string `json:"org"`
	Email   string `json:"email,omitempty"`
	Src     string `json:"src"`
}

// Key is the identity of the seat: (account uuid, org uuid).
func (s Seat) Key() [2]string { return [2]string{s.Account, s.Org} }

// OAuth is the part of .claude.json's oauthAccount that wy keeps. No other key is read.
type OAuth struct {
	AccountUUID      string `json:"accountUuid"`
	OrganizationUUID string `json:"organizationUuid"`
	EmailAddress     string `json:"emailAddress"`
}

// AuthStatus holds the fields used from `claude auth status --json`.
type AuthStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	Email            string `json:"email,omitempty"`
	OrgID            string `json:"orgId,omitempty"`
	OrgName          string `json:"orgName,omitempty"`
	SubscriptionType string `json:"subscriptionType,omitempty"`
}

// entry is <state>/identity/<slot hash>.json.
type entry struct {
	Slot         string      `json:"slot"`
	StateFile    string      `json:"state_file"`
	Size         int64       `json:"size"`
	MtimeNS      int64       `json:"mtime_ns"`
	OAuth        *OAuth      `json:"oauth,omitempty"`
	OAuthAt      string      `json:"oauth_at,omitempty"` // when oauth was first seen as it is now
	AuthStatusAt string      `json:"auth_status_at,omitempty"`
	LoggedIn     *bool       `json:"logged_in,omitempty"`
	Auth         *AuthStatus `json:"auth,omitempty"`
}

// Login is what is known about a slot's login without running claude.
type Login struct {
	Seat         Seat
	HasSeat      bool       // false: no oauthAccount and no usable auth status
	Auth         AuthStatus // last `claude auth status` result, zero if never run
	AuthStatusAt time.Time  // zero if never run
	OAuthAt      time.Time  // when the state file's oauthAccount was first seen as it is; zero if unknown
}

// LoggedIn is the one answer to "is this slot logged in" for every command that decides
// without asking claude again (identity.md 4): a seat is known and the last
// `claude auth status` did not say logged out after the state file's oauthAccount took its
// current value. Claude Code leaves oauthAccount in place on logout, so that key alone
// does not prove a login.
func (l Login) LoggedIn() bool {
	if !l.HasSeat {
		return false
	}
	if l.AuthStatusAt.IsZero() || l.Auth.LoggedIn {
		return true
	}
	return l.OAuthAt.After(l.AuthStatusAt) // logged in again since claude said logged out
}

// Store reads and caches slot logins under <state>/identity/.
type Store struct {
	StateDir string
	FS       FS
	Now      func() time.Time // default time.Now
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) cachePath(slot Slot) string {
	sum := sha256.Sum256([]byte(slot.Key()))
	return filepath.Join(s.StateDir, "identity", hex.EncodeToString(sum[:])[:16]+".json")
}

// Login returns the slot's login from the cache, rereading the state file only when its
// size or mtime changed. It never runs `claude`: it is safe in the hook's 20 ms budget.
// A missing state file is not an error (HasSeat is false) unless a cached auth status says
// the slot is logged in.
func (s *Store) Login(slot Slot) (Login, error) {
	cached, _ := s.readEntry(slot) // a damaged cache is just a miss
	fi, err := s.FS.Stat(slot.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return loginFrom(cached), nil
		}
		return Login{}, err
	}
	if cached != nil && cached.StateFile == slot.StateFile && cached.Size == fi.Size() && cached.MtimeNS == fi.ModTime().UnixNano() {
		return loginFrom(cached), nil
	}
	e, err := s.reread(slot, cached, fi)
	if err != nil {
		return Login{}, err
	}
	// The cache is an optimisation: a read-only state folder must not break hooks.
	_ = s.writeEntry(slot, e)
	return loginFrom(e), nil
}

// reread reads the slot's oauthAccount into a new cache entry, keeping the cached auth
// status. OAuthAt moves only when oauthAccount changed (a login), not on every rewrite of
// the state file.
func (s *Store) reread(slot Slot, cached *entry, fi os.FileInfo) (*entry, error) {
	oa, err := ReadOAuth(s.FS, slot.StateFile)
	if err != nil {
		return nil, err
	}
	e := &entry{Slot: slot.Name}
	if cached != nil {
		e.AuthStatusAt, e.LoggedIn, e.Auth = cached.AuthStatusAt, cached.LoggedIn, cached.Auth
		e.OAuthAt = cached.OAuthAt
	}
	// Bumped only against an oauthAccount that was really read before; unknown (an entry
	// written by an older wy, or by RefreshAuthStatus alone) stays unknown, so an earlier
	// "logged out" still holds until claude is asked again.
	switch {
	case cached == nil:
		e.OAuthAt = s.now().UTC().Format(time.RFC3339Nano)
	case (cached.Size != 0 || cached.MtimeNS != 0) && !sameOAuth(cached.OAuth, oa):
		e.OAuthAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	e.StateFile, e.Size, e.MtimeNS, e.OAuth = slot.StateFile, fi.Size(), fi.ModTime().UnixNano(), oa
	return e, nil
}

func sameOAuth(a, b *OAuth) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func loginFrom(e *entry) Login {
	var l Login
	if e == nil {
		return l
	}
	if e.OAuth != nil && e.OAuth.AccountUUID != "" {
		l.Seat = Seat{Account: e.OAuth.AccountUUID, Org: e.OAuth.OrganizationUUID, Email: e.OAuth.EmailAddress, Src: "claude_json"}
		l.HasSeat = true
	}
	if e.Auth != nil {
		l.Auth = *e.Auth
		if !l.HasSeat && e.Auth.LoggedIn {
			l.Seat = Seat{Org: e.Auth.OrgID, Email: e.Auth.Email, Src: "auth_status"}
			l.HasSeat = true
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, e.AuthStatusAt); err == nil {
		l.AuthStatusAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, e.OAuthAt); err == nil {
		l.OAuthAt = t
	}
	return l
}

// ReadOAuth reads exactly one key, oauthAccount, from a .claude.json state file. The file
// is opened read-only. It returns nil (no error) when the key is absent or null.
func ReadOAuth(fsys FS, stateFile string) (*OAuth, error) {
	f, err := fsys.Open(stateFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var doc struct {
		OAuthAccount *OAuth `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", stateFile, err)
	}
	return doc.OAuthAccount, nil
}

func (s *Store) readEntry(slot Slot) (*entry, error) {
	data, err := os.ReadFile(s.cachePath(slot))
	if err != nil {
		return nil, err
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) writeEntry(slot Slot, e *entry) error {
	if err := os.MkdirAll(filepath.Join(s.StateDir, "identity"), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return atomicfile.Write(s.cachePath(slot), append(data, '\n'), 0o600)
}

// Runner runs an external command with the given environment and returns its stdout.
type Runner func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)

// AuthTimeout is how long `claude auth status --json` may run (identity.md section 4).
const AuthTimeout = 10 * time.Second

// ExecRunner runs the real command with stdin closed (/dev/null).
func ExecRunner(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = nil
	return cmd.Output()
}

// AuthEnv builds the environment for `claude auth status` in a slot: CLAUDE_CONFIG_DIR is
// removed, then set to the slot's folder, except for ~/.claude, where it must stay unset
// (otherwise claude looks for ~/.claude/.claude.json; testdata/README.md).
func AuthEnv(environ []string, slot Slot) []string {
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			out = append(out, kv)
		}
	}
	if !slot.Default {
		out = append(out, "CLAUDE_CONFIG_DIR="+slot.Dir)
	}
	return out
}

// RefreshAuthStatus runs `claude auth status --json` for the slot (about 1 s), stores the
// result in the slot's cache entry and returns it. Never call it on a hook's hot path; use
// RefreshAuthStatusAsync after the record is written.
func (s *Store) RefreshAuthStatus(ctx context.Context, slot Slot, environ []string, run Runner) (AuthStatus, error) {
	if run == nil {
		run = ExecRunner
	}
	ctx, cancel := context.WithTimeout(ctx, AuthTimeout)
	defer cancel()
	out, err := run(ctx, AuthEnv(environ, slot), "claude", "auth", "status", "--json")
	var st AuthStatus
	if jerr := json.Unmarshal(out, &st); jerr != nil {
		if err != nil {
			return st, fmt.Errorf("claude auth status: %w", err)
		}
		return st, fmt.Errorf("claude auth status: unreadable output: %w", jerr)
	}
	// A logged-out slot makes some versions exit non-zero but still print JSON: use it.
	// The state file is read again first, so that a login it already shows (OAuthAt) is
	// ordered before this answer rather than mistaken for a later one.
	e, _ := s.readEntry(slot)
	if fi, ferr := s.FS.Stat(slot.StateFile); ferr == nil {
		if ne, rerr := s.reread(slot, e, fi); rerr == nil {
			e = ne
		}
	}
	if e == nil {
		// Size and mtime stay zero, so the next Login() rereads the state file.
		e = &entry{Slot: slot.Name, StateFile: slot.StateFile}
	}
	e.Slot = slot.Name
	e.Auth = &st
	li := st.LoggedIn
	e.LoggedIn = &li
	e.AuthStatusAt = s.now().UTC().Format(time.RFC3339Nano)
	if err := s.writeEntry(slot, e); err != nil {
		return st, err
	}
	return st, nil
}

// AuthResult is delivered by RefreshAuthStatusAsync.
type AuthResult struct {
	Status AuthStatus
	Err    error
}

// RefreshAuthStatusAsync runs RefreshAuthStatus in a goroutine. A short-lived process
// (the hook) must still stay alive until the channel delivers, or hand the work to a
// detached `wy` process; that decision belongs to the hook command.
func (s *Store) RefreshAuthStatusAsync(ctx context.Context, slot Slot, environ []string, run Runner) <-chan AuthResult {
	ch := make(chan AuthResult, 1)
	go func() {
		st, err := s.RefreshAuthStatus(ctx, slot, environ, run)
		ch <- AuthResult{Status: st, Err: err}
	}()
	return ch
}
