package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/mover"
)

// pinSession adds a second session (pid 101, "pin-b") on the first account.
func pinSession(t *testing.T, f *nextFixture, status string) string {
	t.Helper()
	const sid = "5e55a0b0-0000-4000-8000-000000000002"
	since := nextNow.Add(-20 * time.Minute).UnixMilli()
	body := `{"pid":101,"sessionId":"` + sid + `","cwd":"/work","procStart":"6","kind":"interactive","name":"pin-b","status":"` +
		status + `","statusUpdatedAt":` + strconv.FormatInt(since, 10) + `}`
	if err := os.WriteFile(filepath.Join(f.home, ".claude", "sessions", "101.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return sid
}

// setup.md 8.6, Pinned sessions: a live pinned session keeps account.pin_reserve_5h/_7d of
// its account free. A burner on the same account that is under the cap but over cap - reserve
// moves; the pinned one never does; a pinned session that alone takes its account to 100 is
// one owner alert with the command to move it by hand.
func TestMovePinReserve(t *testing.T) {
	f, h, run := moveFixture(t, 60, "busy")
	pin := pinSession(t, f, "busy")
	f.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	f.set("account.smart_switch_scope", "sessions:5e55a0a0,5e55a0b0")
	moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":8,"5e55a0b0":1}}` // 60 + 3 h × 9 = 87
	t.Cleanup(func() { delete(moveTestVars, moveTestEnv) })

	// Not pinned: under the cap of 95, nothing moves.
	out, _, code := run("--plan")
	if code != 0 || !strings.Contains(out, "No session needs a move.") || !strings.Contains(out, "95/98") {
		t.Fatalf("plan without a pin:\n%s", out)
	}
	nativePins(t, f, `["5e55a0b0"]`)
	out, _, _ = run("--plan")
	if !strings.Contains(out, "85/93") || !strings.Contains(out, "1 pinned: 10/5") || !strings.Contains(out, "5h over its cap, pass 1 of 2") ||
		!strings.Contains(out, "5e55a0b0 pin-b stays on claude: pinned") || !strings.Contains(out, "keeps 10% of its account's 5-hour window") {
		t.Fatalf("plan with a pin:\n%s", out)
	}
	js, _, _ := run("--plan", "--json")
	if !strings.Contains(js, `"Pins": 1`) || !strings.Contains(js, `"Reserve5": 10`) {
		t.Fatalf("plan json:\n%s", js)
	}
	f.set("account.smart_switch", "on")
	run("--auto") // the spike filter's first pass
	out, errOut, code := run("--auto")
	if code != 0 || !strings.Contains(out, "moved 5e55a0a0 lane-a from claude to claude-2") {
		t.Fatalf("auto: %d %q %q", code, out, errOut)
	}
	for _, c := range h.calls {
		// Without the temp dir: its random name can contain "101".
		if c := strings.ReplaceAll(c, f.home, ""); strings.Contains(c, "101") || strings.Contains(c, pin) {
			t.Fatalf("the pinned session was touched: %q", c)
		}
	}

	// The pin alone at 20 points/h from 60: 100 in 2 h, before the reset in 3 h: an alert
	// (once), with the command; it is not moved.
	moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":0,"5e55a0b0":20}}`
	h.calls = nil
	out, _, _ = run("--auto")
	want := "alert: pinned session 5e55a0b0 pin-b alone takes claude's 5h window to 100% at " + nextNow.Add(2*time.Hour).Local().Format("15:04")
	if !strings.Contains(out, want) || !strings.Contains(out, "move it by hand: wy move 5e55a0b0 claude-2 --force") || len(h.calls) != 0 {
		t.Fatalf("alert:\n%s\n%q", out, h.calls)
	}
	if out, _, _ := run("--auto"); strings.Contains(out, "alert:") {
		t.Fatalf("alerted twice:\n%s", out)
	}
	if as := mover.NewState(f.dirs.State.Path).Alerts(nextNow); len(as) != 1 || as[0].Session != pin || as[0].Command != "wy move 5e55a0b0 claude-2 --force" {
		t.Fatalf("kept alerts %+v", as)
	}
}

// livePins: only the owner's pins (Claude Code's, per folder) on a known account, not idle for
// 6 hours; the reserve is pins × the settings.
func TestLivePins(t *testing.T) {
	now := nextNow
	ls := []mover.Launch{
		{SessionID: "a", Slot: "/h/.claude-2", Status: "busy", StatusSince: now.Add(-10 * time.Hour)},
		{SessionID: "b", Slot: "/h/.claude-2", Status: "idle", StatusSince: now.Add(-5 * time.Hour)},
		{SessionID: "c", Slot: "/h/.claude-2", Status: "idle", StatusSince: now.Add(-7 * time.Hour)}, // idle 7 h: released
		{SessionID: "d", Slot: "/h/.claude-2", Status: "busy"},                                       // not pinned
		{SessionID: "e", Slot: "/h/.claude-9", Status: "busy"},                                       // not logged in here
		{SessionID: "f", Slot: "/h/.claude-3", Status: "busy"},                                       // pinned in .claude-3
	}
	keys := map[string]string{"/h/.claude-2": "acct:2/o", "/h/.claude-3": "acct:3/o"}
	got := livePins(ls, keys, pinSet{native: map[string][]string{"/h/.claude-2": {"a", "b", "c"}, "/h/.claude-9": {"e"}, "/h/.claude-3": {"f"}}}, now)
	if strings.Join(got["acct:2/o"], ",") != "a,b" || strings.Join(got["acct:3/o"], ",") != "f" || len(got) != 2 {
		t.Fatalf("live pins %v", got)
	}
	p := mover.DefaultParams
	p.PinReserve5, p.PinReserve7 = 10, 3
	r := pinReserves(p, got)
	if r["acct:2/o"].Pins != 2 || r["acct:2/o"].R5 != 20 || r["acct:2/o"].R7 != 6 || r["acct:3/o"].R5 != 10 {
		t.Fatalf("reserves %+v", r)
	}
}

// nativePins writes Claude Code's own pins of the fixture's first folder (~/.claude).
func nativePins(t *testing.T, f *nextFixture, body string) string {
	t.Helper()
	file := filepath.Join(f.home, ".claude", "jobs", "pins.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// setup.md 8.6, Pinned sessions: Claude Code's own pins are the only pins. A session in its
// folder's jobs/pins.json (prefix match) is pinned and holds the reserve; wy's old pins file
// and the peers tag no-move pin nothing; --pin-account is retired; a by-hand move of a pinned
// session needs --force and says the pin stays; Claude Code's file is never written.
func TestMoveNativePin(t *testing.T) {
	f, h, run := moveFixture(t, 60, "busy")
	pin := pinSession(t, f, "busy")
	f.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	f.set("account.smart_switch_scope", "sessions:5e55a0a0,5e55a0b0")
	moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":8,"5e55a0b0":1}}`
	t.Cleanup(func() { delete(moveTestVars, moveTestEnv) })

	// wy's old pins file: ignored, with a note.
	old := filepath.Join(f.dirs.State.Path, "move", "pins.json")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte(`["`+pin+`"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, _ := run("--plan")
	if strings.Contains(out, "1 pinned") || !strings.Contains(out, "1 session(s) in") || !strings.Contains(out, "are not pinned: pins come from Claude Code only") {
		t.Fatalf("plan with only the old pins file:\n%s", out)
	}
	if _, errOut, code := run("--pin-account", "pin-b"); code == 0 || !strings.Contains(errOut, "pin or unpin the session in Claude Code") {
		t.Fatalf("--pin-account: %d %q", code, errOut)
	}

	native := `["48d8d877","5e55a0b0"]`
	file := nativePins(t, f, native)
	out, _, _ = run("--plan")
	if !strings.Contains(out, "1 pinned: 10/5") || !strings.Contains(out, "5e55a0b0 pin-b stays on claude: pinned (in Claude Code)") ||
		!strings.Contains(out, "pinned: 5e55a0b0 pin-b on claude (in Claude Code; holds a reserve)") {
		t.Fatalf("plan with a Claude Code pin:\n%s", out)
	}
	js, _, _ := run("--plan", "--json")
	if !strings.Contains(js, `"Pins": 1`) || !strings.Contains(js, `"from": "claude"`) {
		t.Fatalf("plan json:\n%s", js)
	}
	out, _, _ = run("--list")
	if !strings.Contains(out, "pinned: 5e55a0b0 pin-b on claude (in Claude Code; holds a reserve)") {
		t.Fatalf("list:\n%s", out)
	}
	js, _, _ = run("--list", "--json")
	if !strings.Contains(js, `"pins"`) || !strings.Contains(js, `"from": "claude"`) {
		t.Fatalf("list json:\n%s", js)
	}

	// By hand: refused without --force; with it, a warning that the pin stays, and the note
	// says the owner moved it (no projection claim).
	_, errOut, code := run(pin[:8], "claude-2", "--dry-run")
	if code == 0 || !strings.Contains(errOut, "pinned to claude in Claude Code") || !strings.Contains(errOut, "--force") {
		t.Fatalf("by hand without --force: %d %q", code, errOut)
	}
	out, errOut, code = run(pin[:8], "claude-2", "--dry-run", "--force")
	if code != 0 || !strings.Contains(errOut, "The pin stays on claude's list (~/.claude/jobs/pins.json)") ||
		!strings.Contains(out, "This session was moved by hand by the owner from claude to claude-2 at") || strings.Contains(out, "would hit") || strings.Contains(out, "would reach") {
		t.Fatalf("by hand with --force: %d %q %q", code, out, errOut)
	}
	for _, c := range h.calls {
		if strings.Contains(c, pin) {
			t.Fatalf("the pinned session was touched: %q", c)
		}
	}
	if b, _ := os.ReadFile(file); string(b) != native {
		t.Fatalf("Claude Code's pins were written: %s", b)
	}

	// Unparsable: no pins, a warning.
	nativePins(t, f, `["5e55a0b0"`)
	out, _, _ = run("--plan")
	if strings.Contains(out, "1 pinned") || !strings.Contains(out, "warning: Claude Code pins in") {
		t.Fatalf("plan with a broken pins file:\n%s", out)
	}
	// The switch's log has the warning once, not every pass.
	f.set("account.smart_switch", "on")
	if out, _, _ := run("--auto"); !strings.Contains(out, "warning: Claude Code pins in") {
		t.Fatalf("first pass:\n%s", out)
	}
	if out, _, _ := run("--auto"); strings.Contains(out, "warning: Claude Code pins") {
		t.Fatalf("warned twice:\n%s", out)
	}
}

// pinSet: Claude Code's pin counts for its own folder only (prefix match); the peers tag
// no-move is no pin.
func TestPinSetFolder(t *testing.T) {
	ps := pinSet{native: map[string][]string{"/h/.claude-3": {"aaaa", "bbbb"}}}
	for _, c := range []struct {
		l    mover.Launch
		want bool
	}{
		{mover.Launch{SessionID: "aaaa-1", Slot: "/h/.claude-3"}, true},
		{mover.Launch{SessionID: "aaaa-1", Slot: "/h/.claude-2"}, false}, // moved away: the old folder's pin
		{mover.Launch{SessionID: "cccc-3", Slot: "/h/.claude-3"}, false},
	} {
		if got := ps.pinned(c.l); got != c.want {
			t.Fatalf("%+v: %v", c.l, got)
		}
	}
	ls := []mover.Launch{{SessionID: "aaaa-1", Slot: "/h/.claude-3", Status: "busy"}, {SessionID: "bbbb-2", Slot: "/h/.claude-3", Status: "busy"},
		{SessionID: "bbbb-2", Slot: "/h/.claude-3", Status: "busy"}, {SessionID: "cccc-3", Slot: "/h/.claude-3", Status: "busy"}}
	got := livePins(ls, map[string]string{"/h/.claude-3": "acct:3/o"}, ps, nextNow)
	if strings.Join(got["acct:3/o"], ",") != "aaaa-1,bbbb-2" {
		t.Fatalf("live pins %v", got)
	}
}
