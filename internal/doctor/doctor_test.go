package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/record"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../testdata/auth", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	allHooks = `"SessionStart":[{"hooks":[{"type":"command","command":"wy hook","async":true}]}],` +
		`"UserPromptSubmit":[{"hooks":[{"type":"command","command":"/usr/local/bin/wy hook UserPromptSubmit"}]}],` +
		`"PermissionRequest":[{"hooks":[{"type":"command","command":"wy hook"}]}],` +
		`"Notification":[{"hooks":[{"type":"command","command":"wy hook"}]}],` +
		`"PreToolUse":[{"matcher":"AskUserQuestion|ExitPlanMode","hooks":[{"type":"command","command":"wy hook"}]}],` +
		`"Stop":[{"hooks":[{"type":"command","command":"wy hook"}]}],` +
		`"StopFailure":[{"hooks":[{"type":"command","command":"wy hook"}]}],` +
		`"SubagentStop":[{"hooks":[{"type":"command","command":"wy hook"}]}],` +
		`"SessionEnd":[{"hooks":[{"type":"command","command":"wy hook"}]}],` +
		`"PreCompact":[{"hooks":[{"type":"command","command":"wy hook"}]}],` +
		`"PermissionDenied":[{"hooks":[{"type":"command","command":"wy hook"}]}]`
)

type fakeEnv struct {
	home string
	dirs paths.Dirs
}

// newFakeHome makes ~/.claude (a real folder with the shared settings), ~/.claude-2 (settings.json
// is a symlink to it), ~/.claude-4 (logged out), logins from testdata/auth.
func newFakeHome(t *testing.T, settings string) fakeEnv {
	t.Helper()
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "settings.json"), settings)
	write(t, filepath.Join(home, ".claude.json"), fixtureFile(t, "oauth-1.json"))
	write(t, filepath.Join(home, ".claude-2", ".claude.json"), fixtureFile(t, "oauth-2.json"))
	if err := os.Symlink(filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".claude-2", "settings.json")); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	write(t, filepath.Join(home, ".claude-4", "settings.json"), "{}")
	// A credentials file and a session key sit where claude keeps them: doctor must not open them.
	write(t, filepath.Join(home, ".claude", ".credentials.json"), `{"SECRET":"x"}`)
	write(t, filepath.Join(home, ".claude-2", "sessions", "1.abc.key"), "SECRET")
	st := t.TempDir()
	return fakeEnv{home: home, dirs: paths.Dirs{
		Config: paths.Dir{Path: filepath.Join(st, "config"), Source: paths.SourceWYHome, Var: "WY_HOME"},
		State:  paths.Dir{Path: filepath.Join(st, "state"), Source: paths.SourceWYHome, Var: "WY_HOME"},
		Data:   paths.Dir{Path: filepath.Join(st, "data"), Source: paths.SourceWYHome, Var: "WY_HOME"},
	}}
}

type recFS struct {
	identity.FS
	opened []string
}

func (r *recFS) Open(name string) (io.ReadCloser, error) {
	r.opened = append(r.opened, name)
	return r.FS.Open(name)
}

func (e fakeEnv) opts(fs identity.FS) Options {
	return Options{
		Home: e.home, Dirs: e.dirs, Getenv: func(string) string { return "" }, FS: fs,
		LookPath: func(string) (string, error) { return "/fake/bin/claude", nil },
		Run: func(context.Context, []string, string, ...string) ([]byte, error) {
			return []byte("2.1.296 (Claude Code)\n"), nil
		},
		Now: func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) },
	}
}

func find(t *testing.T, rep Report, name, slot string) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name && c.Slot == slot {
			return c
		}
	}
	t.Fatalf("no check %q %q in %+v", name, slot, rep.Checks)
	return Check{}
}

func wantLevel(t *testing.T, rep Report, name, slot string, lvl Level) Check {
	t.Helper()
	c := find(t, rep, name, slot)
	if c.Level != lvl {
		t.Errorf("%s %s: %s %q, want %s", name, slot, c.Level, c.Detail, lvl)
	}
	if c.Level != OK && c.Fix == "" {
		t.Errorf("%s %s: %s without a fix hint", name, slot, c.Level)
	}
	return c
}

func TestHealthyHome(t *testing.T) {
	e := newFakeHome(t, `{"hooks":{`+allHooks+`},"statusLine":{"type":"command","command":"wy statusline --wrap 'bash ~/x.sh'"}}`)
	fs := &recFS{FS: identity.OSFS{}}
	rep := Run(context.Background(), e.opts(fs))
	if rep.Failed() {
		t.Fatalf("failed: %+v", rep.Checks)
	}
	c := wantLevel(t, rep, "claude", "", OK)
	if !strings.Contains(c.Detail, "2.1.296") {
		t.Errorf("claude: %q", c.Detail)
	}
	for _, slot := range []string{"~/.claude", "~/.claude-2"} {
		wantLevel(t, rep, "login", slot, OK)
		wantLevel(t, rep, "hooks", slot, OK)
		wantLevel(t, rep, "status line", slot, OK)
		wantLevel(t, rep, "settings symlink", slot, OK)
	}
	if d := find(t, rep, "login", "~/.claude").Detail; !strings.Contains(d, "user1@example.com") {
		t.Errorf("login detail %q", d)
	}
	if d := find(t, rep, "settings symlink", "~/.claude-2").Detail; !strings.Contains(d, "symlink to ~/.claude/settings.json") {
		t.Errorf("symlink detail %q", d)
	}
	// Logged out: a warning, with a fix; its hooks are not judged.
	l := wantLevel(t, rep, "login", "~/.claude-4", Warn)
	if !strings.Contains(l.Detail, "logged out") || !strings.Contains(l.Fix, "CLAUDE_CONFIG_DIR=~/.claude-4") {
		t.Errorf("logged out: %+v", l)
	}
	for _, c := range rep.Checks {
		if c.Name == "hooks" && c.Slot == "~/.claude-4" {
			t.Error("hooks of a logged-out folder were judged")
		}
	}
	wantLevel(t, rep, "outbox", "", OK)
	wantLevel(t, rep, "outbox size", "", OK)
	wantLevel(t, rep, "oldest unflushed", "", OK)

	// Credentials and keys were never opened.
	for _, n := range fs.opened {
		if strings.HasSuffix(n, ".key") || strings.HasSuffix(n, ".credentials.json") {
			t.Errorf("opened %s", n)
		}
	}
	// The user's files are untouched; only the state folder got files.
	b, _ := os.ReadFile(filepath.Join(e.home, ".claude", ".credentials.json"))
	if string(b) != `{"SECRET":"x"}` {
		t.Error("credentials file changed")
	}
	if _, err := os.Stat(filepath.Join(e.dirs.State.Path, "doctor.json")); err != nil {
		t.Errorf("doctor.json not written: %v", err)
	}
}

func TestHooksAndStatusLineVariants(t *testing.T) {
	cases := []struct {
		name, settings string
		hooks, status  Level
		hookHas        string
	}{
		{"plugin enabled", `{"enabledPlugins":{"watchyard@watchyard":true},"statusLine":{"command":"bash x.sh"}}`, OK, Warn, "plugin watchyard@watchyard"},
		{"plugin disabled", `{"enabledPlugins":{"watchyard@watchyard":false}}`, Warn, Warn, "disabled"},
		{"none", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"bash ~/.claude/watchyard-hook.sh"}]}]}}`, Warn, Warn, "no `wy hook`"},
		{"partial", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"wy hook"}]}],"SessionStart":[{"hooks":[{"type":"command","command":"wy hook"}]}]}}`, Warn, Warn, "missing for: UserPromptSubmit"},
		{"look-alike", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"easy hook"}]}]},"statusLine":{"command":"easy statusline"}}`, Warn, Warn, "no `wy hook`"},
		{"wrapped exe", `{"statusLine":{"type":"command","command":"\"C:\\wy.exe\" statusline --wrap x"}}`, Warn, OK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newFakeHome(t, c.settings)
			rep := Run(context.Background(), e.opts(nil))
			h := wantLevel(t, rep, "hooks", "~/.claude", c.hooks)
			if !strings.Contains(h.Detail, c.hookHas) {
				t.Errorf("hooks detail %q lacks %q", h.Detail, c.hookHas)
			}
			wantLevel(t, rep, "status line", "~/.claude", c.status)
			// ~/.claude-2 reads the same file through the symlink.
			wantLevel(t, rep, "hooks", "~/.claude-2", c.hooks)
			if rep.Failed() {
				t.Error("warnings must not fail the run")
			}
		})
	}
	// Missing settings.json and invalid JSON.
	e := newFakeHome(t, `{not json`)
	rep := Run(context.Background(), e.opts(nil))
	if c := wantLevel(t, rep, "hooks", "~/.claude", Warn); !strings.Contains(c.Detail, "not valid JSON") {
		t.Errorf("%q", c.Detail)
	}
}

func TestSymlinkDrift(t *testing.T) {
	e := newFakeHome(t, `{}`)
	o := e.opts(nil)
	rep := Run(context.Background(), o)
	wantLevel(t, rep, "settings symlink", "~/.claude-2", OK)

	link := filepath.Join(e.home, ".claude-2", "settings.json")
	// Claude (or an editor) replaces the link with a copy of the file.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	write(t, link, `{}`)
	rep = Run(context.Background(), o)
	c := wantLevel(t, rep, "settings symlink", "~/.claude-2", Warn)
	if !strings.Contains(c.Detail, "not any more") || !strings.Contains(c.Fix, "--rebaseline") {
		t.Errorf("%+v", c)
	}
	wantLevel(t, rep, "settings symlink", "~/.claude", OK) // the regular file stayed regular
	if rep.Failed() {
		t.Error("a symlink change is a warning, not a failure")
	}
	// The baseline is not overwritten by the warning: still warns on the next run.
	rep = Run(context.Background(), o)
	wantLevel(t, rep, "settings symlink", "~/.claude-2", Warn)

}

func TestSymlinkTargetChange(t *testing.T) {
	e := newFakeHome(t, `{}`)
	o := e.opts(nil)
	Run(context.Background(), o)
	link := filepath.Join(e.home, ".claude-2", "settings.json")
	os.Remove(link)
	other := filepath.Join(e.home, "other.json")
	write(t, other, `{}`)
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	rep := Run(context.Background(), o)
	c := wantLevel(t, rep, "settings symlink", "~/.claude-2", Warn)
	if !strings.Contains(c.Detail, "~/other.json") {
		t.Errorf("%q", c.Detail)
	}
	o.Rebaseline = true
	Run(context.Background(), o)
	o.Rebaseline = false
	rep = Run(context.Background(), o)
	wantLevel(t, rep, "settings symlink", "~/.claude-2", OK)
}

func TestClaudeMissingFails(t *testing.T) {
	e := newFakeHome(t, `{}`)
	o := e.opts(nil)
	o.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	rep := Run(context.Background(), o)
	wantLevel(t, rep, "claude", "", Fail)
	if !rep.Failed() || rep.Fail != 1 {
		t.Errorf("%+v", rep)
	}
	// A broken `claude --version` is only a warning.
	o = e.opts(nil)
	o.Run = func(context.Context, []string, string, ...string) ([]byte, error) { return nil, errors.New("boom") }
	rep = Run(context.Background(), o)
	wantLevel(t, rep, "claude", "", Warn)
	if rep.Failed() {
		t.Error("version failure must not fail")
	}
}

func TestOutboxChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses file modes")
	}
	e := newFakeHome(t, `{}`)

	// Unwritable: a file where the folder should be.
	if err := os.MkdirAll(e.dirs.State.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.dirs.State.Path, "outbox"), "i am a file")
	rep := Run(context.Background(), e.opts(nil))
	wantLevel(t, rep, "outbox", "", Fail)
	if !rep.Failed() {
		t.Error("an unwritable outbox must fail")
	}
	os.Remove(filepath.Join(e.dirs.State.Path, "outbox"))

	// An old unflushed record: fine without a hub, a warning with one.
	old := record.New(record.KindHookEvent, record.Meta{Machine: "m_x", Now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)})
	old.Payload = json.RawMessage(`{"session_id":"s"}`)
	line, _ := old.Encode()
	write(t, filepath.Join(e.dirs.State.Path, "outbox", "2026-10-12.jsonl"), string(line)+"\n")
	rep = Run(context.Background(), e.opts(nil))
	c := wantLevel(t, rep, "oldest unflushed", "", OK)
	if !strings.Contains(c.Detail, "3h ago") || !strings.Contains(c.Detail, "no hub") {
		t.Errorf("%q", c.Detail)
	}
	if s := find(t, rep, "outbox size", ""); !strings.Contains(s.Detail, "1 day") {
		t.Errorf("%q", s.Detail)
	}
	write(t, filepath.Join(e.dirs.Config.Path, "config.toml"), "version = 1\n[hub]\nurl = \"https://hub.example\"\n[outbox]\nmax_mb = 50\n")
	rep = Run(context.Background(), e.opts(nil))
	wantLevel(t, rep, "oldest unflushed", "", Warn)
}

func TestConfigChecks(t *testing.T) {
	e := newFakeHome(t, `{}`)
	write(t, filepath.Join(e.dirs.Config.Path, "config.toml"), "version = 1\nfuture_key = 3\n")
	rep := Run(context.Background(), e.opts(nil))
	c := wantLevel(t, rep, "config", "", Warn)
	if !strings.Contains(c.Detail, "future_key") {
		t.Errorf("%q", c.Detail)
	}
	write(t, filepath.Join(e.dirs.Config.Path, "config.toml"), "version = [")
	rep = Run(context.Background(), e.opts(nil))
	wantLevel(t, rep, "config", "", Fail)
	if !rep.Failed() {
		t.Error("broken config.toml must fail")
	}
}

func TestReportText(t *testing.T) {
	e := newFakeHome(t, `{}`)
	rep := Run(context.Background(), e.opts(nil))
	var sb strings.Builder
	rep.WriteText(&sb)
	out := sb.String()
	if !strings.Contains(out, "WARN  hooks ~/.claude") || !strings.Contains(out, "fix: ") || !strings.Contains(out, " ok, ") {
		t.Errorf("text report:\n%s", out)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(b, &back); err != nil || len(back.Checks) != len(rep.Checks) {
		t.Fatalf("json round trip: %v", err)
	}
}

// oauthAccount outlives a logout: a later `claude auth status` saying logged out makes the
// folder logged out here too (identity.Login.LoggedIn, the rule wy account next uses).
func TestLoginLoggedOutDespiteOAuthAccount(t *testing.T) {
	e := newFakeHome(t, `{}`)
	o := e.opts(identity.OSFS{})
	st := &identity.Store{StateDir: e.dirs.State.Path, FS: identity.OSFS{}, Now: o.Now}
	slot := identity.Locator{Home: e.home, FS: identity.OSFS{}}.SlotFor("~/.claude-2")
	if _, err := st.Login(slot); err != nil {
		t.Fatal(err)
	}
	at := o.Now().Add(time.Minute)
	later := func() time.Time { return at }
	st.Now = later
	if _, err := st.RefreshAuthStatus(context.Background(), slot, nil, func(context.Context, []string, string, ...string) ([]byte, error) {
		return []byte(`{"loggedIn":false}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	o.Now = later
	l := wantLevel(t, Run(context.Background(), o), "login", "~/.claude-2", Warn)
	if !strings.Contains(l.Detail, "logged out (claude auth status") {
		t.Errorf("detail %q", l.Detail)
	}
}
