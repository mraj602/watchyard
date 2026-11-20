package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
)

var nextNow = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// nextFixture is a temporary home with three folders (~/.claude logged in as a1,
// ~/.claude-2 as a2, ~/.claude-3 logged out), wy folders under it and a fake hub.
type nextFixture struct {
	t       *testing.T
	home    string
	dirs    paths.Dirs
	machine string
	hub     *httptest.Server
	seats   []map[string]any
	auth    []string
}

func newNextFixture(t *testing.T) *nextFixture {
	t.Helper()
	home := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/settings.json", "{}")
	write(".claude.json", `{"oauthAccount":{"accountUuid":"a1","organizationUuid":"o1","emailAddress":"one@example.test"}}`)
	write(".claude-2/.claude.json", `{"oauthAccount":{"accountUuid":"a2","organizationUuid":"o2","emailAddress":"two@example.test"}}`)
	write(".claude-3/.claude.json", `{}`)
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
	m, err := identity.LoadOrCreateMachine(d.State.Path, identity.MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f := &nextFixture{t: t, home: home, dirs: d, machine: m.ID}
	f.hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/v1/accounts":
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": f.seats})
		case strings.HasSuffix(r.URL.Path, "/windows") && r.URL.Query().Get("samples") == "true":
			_ = json.NewEncoder(w).Encode(map[string]any{"windows": []any{}, "samples": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.hub.Close)
	cf, _ := config.Load(d.Config.Path)
	if err := cf.Set("hub.url", []string{f.hub.URL}); err != nil {
		t.Fatal(err)
	}
	if err := cf.Set("slots.labels.~/.claude-2", []string{"claude-2"}); err != nil {
		t.Fatal(err)
	}
	if err := cf.Save(); err != nil {
		t.Fatal(err)
	}
	if err := board.SaveToken(d.State.Path, board.Token{V: 1, URL: f.hub.URL, Device: "dev_1", Token: "wy_b_test"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func seat(id, account, org, email, label string, five, seven float64) map[string]any {
	ts := func(d time.Duration) string { return nextNow.Add(d).Format(time.RFC3339) }
	return map[string]any{"id": id, "account": account, "org": org, "email": email, "label": label, "live_sessions": 0,
		"places": []any{}, "signed_out": []any{},
		"windows": []map[string]any{
			{"key": "five_hour", "used_pct": five, "resets_at": ts(3 * time.Hour), "updated_at": ts(-time.Minute), "state": "current"},
			{"key": "seven_day", "used_pct": seven, "resets_at": ts(72 * time.Hour), "updated_at": ts(-time.Minute), "state": "current"},
		}}
}

type nextRun struct {
	out, errOut string
	code        int
	exec        []string
	env         []string
}

func (f *nextFixture) run(args ...string) nextRun {
	var out, errOut bytes.Buffer
	var r nextRun
	e := nextEnv{home: f.home, dirs: f.dirs, getenv: func(string) string { return "" }, now: func() time.Time { return nextNow },
		stdout: &out, stderr: &errOut,
		lookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
		environ:  func() []string { return []string{"PATH=/usr/bin", "CLAUDE_CONFIG_DIR=/elsewhere"} },
		exec: func(path string, argv, env []string) error {
			r.exec, r.env = append([]string{path}, argv...), env
			return nil
		}}
	err := accountNext(e, args)
	var ee *exitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.code
	case errors.Is(err, errUsage):
		r.code = 2
	default:
		f.t.Fatalf("wy account next %v: %v", args, err)
	}
	r.out, r.errOut = out.String(), errOut.String()
	return r
}

func TestAccountNextFromHub(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{
		seat("seat_1", "a1", "o1", "one@example.test", "claude-1", 95, 20), // 5h nearly full
		seat("seat_2", "a2", "o2", "two@example.test", "laptop-two", 10, 30),
	}
	r := f.run("--no-reserve")
	if r.code != 0 || !strings.HasPrefix(r.out, "claude-2  ~/.claude-2  (7d 30% → ") {
		t.Fatalf("code %d out %q err %q", r.code, r.out, r.errOut)
	}
	if len(f.auth) == 0 || f.auth[0] != "Bearer wy_b_test" {
		t.Errorf("auth %v", f.auth)
	}
	if _, err := os.Stat(filepath.Join(f.dirs.State.Path, "next", "picks.json")); err == nil {
		t.Error("--no-reserve wrote the pick log")
	}

	r = f.run("--env")
	if r.out != "CLAUDE_CONFIG_DIR="+filepath.Join(f.home, ".claude-2")+"\n" {
		t.Errorf("env %q", r.out)
	}

	r = f.run("--json", "--no-reserve")
	var doc struct {
		Data     string `json:"data"`
		Picked   *struct{ Folder, Env string }
		Accounts []struct {
			Folder    string `json:"folder"`
			Eligible  bool   `json:"eligible"`
			Excluded  string `json:"excluded_reason"`
			Seat      string `json:"seat"`
			MatchedBy string `json:"seat_matched_by"`
			Pending   int    `json:"pending_reservations"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
		t.Fatalf("json: %v\n%s", err, r.out)
	}
	if doc.Data != "hub" || doc.Picked == nil || doc.Picked.Folder != "~/.claude-2" || len(doc.Accounts) != 3 {
		t.Fatalf("doc %+v", doc)
	}
	if a := doc.Accounts[0]; a.Pending != 1 || a.Seat != "seat_2" || a.MatchedBy != "account" {
		t.Errorf("first %+v (the --env run reserved it)", a)
	}
	for _, a := range doc.Accounts[1:] {
		if a.Eligible || a.Excluded == "" {
			t.Errorf("%+v", a)
		}
	}

	r = f.run("--explain", "--no-reserve")
	for _, want := range []string{"Strategy smart; data from the hub.", "claude-2", "not logged in on this machine", "Pick: claude-2"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("explain lacks %q:\n%s", want, r.out)
		}
	}
}

func TestAccountNextExec(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 85, 30),
		seat("seat_2", "a2", "o2", "two@example.test", "", 10, 30)}
	r := f.run("--exec", "--", "claude", "--resume")
	if r.code != 0 || strings.Join(r.exec, " ") != "/usr/bin/claude claude --resume" {
		t.Fatalf("exec %v code %d err %s", r.exec, r.code, r.errOut)
	}
	var ccd []string
	for _, kv := range r.env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			ccd = append(ccd, kv)
		}
	}
	if len(ccd) != 1 || ccd[0] != "CLAUDE_CONFIG_DIR="+filepath.Join(f.home, ".claude-2") {
		t.Errorf("env %v", r.env)
	}
	// The default view of ~/.claude runs with the variable unset.
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30)}
	r = f.run("--exec", "--strategy", "fill")
	if strings.Join(r.exec, " ") != "/usr/bin/claude claude" {
		t.Fatalf("exec %v", r.exec)
	}
	for _, kv := range r.env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			t.Errorf("default view got %s", kv)
		}
	}
	if r := f.run("--", "claude"); r.code != 2 {
		t.Errorf("a command without --exec: code %d", r.code)
	}
	if r := f.run("--env", "--json"); r.code != 2 {
		t.Errorf("--env --json: code %d", r.code)
	}
}

func TestAccountNextNoneEligible(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{
		seat("seat_1", "a1", "o1", "one@example.test", "", 95, 20),
		seat("seat_2", "a2", "o2", "two@example.test", "", 20, 97),
	}
	r := f.run()
	if r.code != exitNoAccount || r.out != "" || !strings.Contains(r.errOut, "the earliest frees up") {
		t.Fatalf("code %d out %q err %q", r.code, r.out, r.errOut)
	}
	r = f.run("--json")
	var doc struct {
		Picked   any        `json:"picked"`
		NextFree *time.Time `json:"next_free"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil || r.code != exitNoAccount || doc.Picked != nil ||
		doc.NextFree == nil || !doc.NextFree.Equal(nextNow.Add(3*time.Hour)) {
		t.Errorf("json %s code %d err %v", r.out, r.code, err)
	}
	if _, err := os.Stat(filepath.Join(f.dirs.State.Path, "next", "picks.json")); err != nil {
		t.Errorf("the pick log should exist (empty): %v", err)
	}
}

// With the hub out of reach, this machine's status-line readings are used, and the output
// says so.
func TestAccountNextHubUnreachable(t *testing.T) {
	f := newNextFixture(t)
	f.hub.Close()
	line := func(slot, acct, org string, five, seven float64) string {
		return fmt.Sprintf(`{"v":1,"id":"01JA%s","kind":"limit_sample","ts":"%s","machine":"%s","slot":"%s","seat":{"account":"%s","org":"%s"},"derived":{"five_hour_pct":%v,"five_hour_resets_at":"%s","seven_day_pct":%v,"seven_day_resets_at":"%s"},"payload":{}}`,
			acct, nextNow.Add(-10*time.Minute).Format(time.RFC3339), f.machine, slot, acct, org, five,
			nextNow.Add(2*time.Hour).Format(time.RFC3339), seven, nextNow.Add(40*time.Hour).Format(time.RFC3339))
	}
	ob := filepath.Join(f.dirs.State.Path, "outbox")
	if err := os.MkdirAll(ob, 0o700); err != nil {
		t.Fatal(err)
	}
	body := line("~/.claude", "a1", "o1", 97, 50) + "\n" + line("~/.claude-2", "a2", "o2", 12, 40) + "\n"
	if err := os.WriteFile(filepath.Join(ob, nextNow.Format("2006-01-02")+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.run("--no-reserve")
	if r.code != 0 || !strings.HasPrefix(r.out, "claude-2  ~/.claude-2  (7d 40% → ") || !strings.Contains(r.out, "local data") ||
		!strings.Contains(r.out, "[hub not read:") {
		t.Fatalf("code %d out %q err %q", r.code, r.out, r.errOut)
	}
	r = f.run("--no-hub", "--explain", "--no-reserve")
	if !strings.Contains(r.out, "local data from this machine's status line (no hub is set up)") && !strings.Contains(r.out, "local data") {
		t.Errorf("explain %s", r.out)
	}
}
