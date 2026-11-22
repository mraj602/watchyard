package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/picker"
	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
)

// modeEnv is a setupEnv on temporary folders only (WY_HOME in t.TempDir()).
func modeEnv(t *testing.T, cfg map[string]string) *setupEnv {
	t.Helper()
	home := t.TempDir()
	wyHome := filepath.Join(home, "wy")
	d, err := paths.Resolve(paths.Env{GOOS: runtime.GOOS, Home: home, Getenv: func(k string) string {
		if k == "WY_HOME" {
			return wyHome
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg) > 0 {
		cf, _ := config.Load(d.Config.Path)
		for k, v := range cfg {
			if err := cf.Set(k, []string{v}); err != nil {
				t.Fatal(err)
			}
		}
		if err := cf.Save(); err != nil {
			t.Fatal(err)
		}
	}
	return &setupEnv{home: home, dirs: d, getenv: func(string) string { return "" }}
}

// The mode question (setup.md 8.5, Choosing in wy setup): asked once per account with no mode
// yet, p/a/s with each definition, the answer to next.accounts.<email|label>.mode without a
// hub and to slots.modes.<folder> with one.
func TestSetupModeQuestion(t *testing.T) {
	e := modeEnv(t, map[string]string{
		"next.accounts.b@example.test.mode": "protected",  // a local key
		"slots.modes.~/.claude-3":           "aggressive", // a proposal
	})
	accs := []plan.Account{
		{Folder: "~/.claude", Label: "work", Email: "a@example.test", LoggedIn: true},
		{Folder: "~/.claude-2", Label: "home", Email: "B@example.test", LoggedIn: true},
		{Folder: "~/.claude-3", Label: "agents", Email: "c@example.test", LoggedIn: true},
		{Folder: "~/.claude-4", Label: "seen", Email: "d@example.test", LoggedIn: true}, // hub mode
		{Folder: "~/.claude-5", Label: "same", Email: "A@example.test", LoggedIn: true}, // a's login again
		{Folder: "~/.claude-6", Label: "out"},                                           // no login: not asked
		{Folder: "~/.claude-7", Label: "fresh", New: true},
	}
	ms := e.loadModeSetup(context.Background(), model.State{}, false)
	if ms.hub {
		t.Fatal("no hub configured, yet hub")
	}
	ms.hubModes["~/.claude-4"] = "protected"
	asks := ms.unchosen(accs)
	if len(asks) != 2 || asks[0].Label != "work" || strings.Join(asks[0].Folders, ",") != "~/.claude,~/.claude-5" ||
		asks[1].Label != "fresh" {
		t.Fatalf("asks %+v", asks)
	}

	var out bytes.Buffer
	got := ms.askModes(strings.NewReader("x\np\naggressive\n"), &out, asks)
	want := map[string]string{"next.accounts.a@example.test.mode": "protected", "next.accounts.fresh.mode": "aggressive"}
	if len(got) != len(want) || got["next.accounts.a@example.test.mode"] != "protected" || got["next.accounts.fresh.mode"] != "aggressive" {
		t.Fatalf("no hub: %v", got)
	}
	text := out.String()
	for _, s := range []string{
		"How should `wy account next` use work (a@example.test)?", "How should `wy account next` use fresh?",
		"p  protected: " + picker.ModeWords.Protected, "a  aggressive: " + picker.ModeWords.Aggressive,
		"s  skip: " + picker.ModeWords.NotChosen, "[p/a/S] ", "Answer p, a or s.",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("question lacks %q:\n%s", s, text)
		}
	}

	ms.hub = true
	got = ms.askModes(strings.NewReader("a\ns\n"), &out, asks)
	if len(got) != 2 || got["slots.modes.~/.claude"] != "aggressive" || got["slots.modes.~/.claude-5"] != "aggressive" {
		t.Fatalf("hub: %v", got)
	}
	// Skip (the default) and the end of the input leave them not chosen.
	if got = ms.askModes(strings.NewReader("\n"), &out, asks); len(got) != 0 {
		t.Fatalf("skip: %v", got)
	}
	if got = ms.askModes(strings.NewReader(""), &out, asks); len(got) != 0 {
		t.Fatalf("eof: %v", got)
	}
}

// With a hub connected (hub.url and a machine token) the seats' modes are read from
// GET /v1/usage, matched to the folders logged in to them; a hub that cannot be read still
// counts as a hub, and setup still asks.
func TestSetupModeReadsHub(t *testing.T) {
	failing, machine := false, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" || r.Header.Get("Authorization") != "Bearer wy_m_test" || failing {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		// The places of /v1/usage are the calling machine's.
		_ = json.NewEncoder(w).Encode(map[string]any{"v": 1, "machine": machine, "seats": []map[string]any{
			{"id": "seat_1", "account": "a1", "org": "o1", "label": "work", "mode": "protected", "windows": []any{},
				"samples": []any{}, "places": []map[string]any{{"slot": "~/.claude-2"}}, "signed_out": []any{}},
			{"id": "seat_2", "account": "a2", "org": "o2", "label": nil, "mode": nil, "windows": []any{},
				"samples": []any{}, "places": []map[string]any{{"slot": "~/.claude-3"}}, "signed_out": []any{}},
		}})
	}))
	defer srv.Close()
	e := modeEnv(t, map[string]string{"hub.url": srv.URL})
	e.http = srv.Client()
	if err := os.MkdirAll(e.dirs.State.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dirs.State.Path, client.TokenFile), []byte("wy_m_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	machine = m.ID
	st := model.State{Folders: []model.Folder{{Folder: "~/.claude-2", LoggedIn: true, Email: "a@example.test"},
		{Folder: "~/.claude-3", LoggedIn: true, Email: "b@example.test"}}}
	accs := []plan.Account{{Folder: "~/.claude-2", Label: "w", Email: "a@example.test", LoggedIn: true},
		{Folder: "~/.claude-3", Label: "h", Email: "b@example.test", LoggedIn: true}}
	ms := e.loadModeSetup(context.Background(), st, false)
	if !ms.hub || ms.hubModes["~/.claude-2"] != "protected" {
		t.Fatalf("hub %v modes %v", ms.hub, ms.hubModes)
	}
	if asks := ms.unchosen(accs); len(asks) != 1 || asks[0].Label != "h" {
		t.Fatalf("asks %+v", asks)
	}
	failing = true
	ms = e.loadModeSetup(context.Background(), st, false)
	if !ms.hub || len(ms.unchosen(accs)) != 2 {
		t.Fatalf("unreadable hub: hub %v asks %+v", ms.hub, ms.unchosen(accs))
	}
	// No token: no hub, unless this run connects one.
	_ = os.Remove(filepath.Join(e.dirs.State.Path, client.TokenFile))
	if ms = e.loadModeSetup(context.Background(), st, false); ms.hub {
		t.Fatal("hub without a token")
	}
	if ms = e.loadModeSetup(context.Background(), st, true); !ms.hub {
		t.Fatal("joining a hub in this run: answers are proposals")
	}
}

// wy account add asks for the new folder (no login yet): with a hub the proposal waits in
// slots.modes.<folder>, without one it is keyed by the new label; a label that already has
// a local mode is not asked about.
func TestAccountAddModeQuestion(t *testing.T) {
	var out bytes.Buffer
	e := modeEnv(t, map[string]string{"next.accounts.kept.mode": "protected"})
	e.stdout = &out
	e.stdin = strings.NewReader("p\n")
	if got := e.askAddMode(context.Background(), model.State{}, "~/.claude-4", "agents"); len(got) != 1 ||
		got["next.accounts.agents.mode"] != "protected" {
		t.Fatalf("no hub: %v", got)
	}
	if !strings.Contains(out.String(), "How should `wy account next` use agents?") {
		t.Fatalf("question:\n%s", out.String())
	}
	e.stdin = strings.NewReader("p\n")
	if got := e.askAddMode(context.Background(), model.State{}, "~/.claude-4", "kept"); got != nil {
		t.Fatalf("asked about a label with a local mode: %v", got)
	}

	srv := httptest.NewServer(http.NotFoundHandler()) // a hub that cannot be read
	defer srv.Close()
	e = modeEnv(t, map[string]string{"hub.url": srv.URL})
	e.stdout, e.stdin, e.http = &out, strings.NewReader("a\n"), srv.Client()
	_ = os.MkdirAll(e.dirs.State.Path, 0o700)
	if err := os.WriteFile(filepath.Join(e.dirs.State.Path, client.TokenFile), []byte("wy_m_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.askAddMode(context.Background(), model.State{}, "~/.claude-4", "agents"); len(got) != 1 ||
		got["slots.modes.~/.claude-4"] != "aggressive" {
		t.Fatalf("hub: %v", got)
	}
}

// A non-interactive run (--yes, no terminal) asks nothing and writes no mode, even with
// answers waiting on stdin.
func TestSetupNonInteractiveAsksNoMode(t *testing.T) {
	h := detecttest.Laptop(t)
	s := newSetupHome(t, h)
	out, errOut, code := s.wy(t, "p\np\np\np\np\np\np\np\n", "setup", "--yes", "--hub", "later")
	if code != 0 || strings.Contains(out+errOut, "How should") {
		t.Fatalf("setup: exit %d\n%s\n%s", code, out, errOut)
	}
	out, errOut, code = s.wy(t, "p\n", "account", "add", "work", "--yes", "--no-login")
	if code != 0 || strings.Contains(out+errOut, "How should") {
		t.Fatalf("account add: exit %d\n%s\n%s", code, out, errOut)
	}
	cfg, _ := os.ReadFile(filepath.Join(s.wyHome, "config", "config.toml"))
	if strings.Contains(string(cfg), "mode") {
		t.Fatalf("a mode was written:\n%s", cfg)
	}
}
