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
	if out, errOut, code := run("--pin-account", "pin-b"); code != 0 || !strings.Contains(out, "pinned") {
		t.Fatalf("pin: %q %q", out, errOut)
	}
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
		if strings.Contains(c, "101") || strings.Contains(c, pin) {
			t.Fatalf("the pinned session was touched: %q", c)
		}
	}

	// The pin alone at 20 points/h from 60: 100 in 2 h, before the reset in 3 h: an alert
	// (once), with the command; it is not moved.
	moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":0,"5e55a0b0":20}}`
	h.calls = nil
	out, _, _ = run("--auto")
	want := "alert: pinned session 5e55a0b0 pin-b alone takes claude's 5h window to 100% at " + nextNow.Add(2*time.Hour).Local().Format("15:04")
	if !strings.Contains(out, want) || !strings.Contains(out, "move it by hand: wy move 5e55a0b0 claude-2") || len(h.calls) != 0 {
		t.Fatalf("alert:\n%s\n%q", out, h.calls)
	}
	if out, _, _ := run("--auto"); strings.Contains(out, "alert:") {
		t.Fatalf("alerted twice:\n%s", out)
	}
	if as := mover.NewState(f.dirs.State.Path).Alerts(nextNow); len(as) != 1 || as[0].Session != pin || as[0].Command != "wy move 5e55a0b0 claude-2" {
		t.Fatalf("kept alerts %+v", as)
	}
}

// livePins: only the owner's pins (pins file, no-move tag) on a known account, not idle for
// 6 hours; the reserve is pins × the settings.
func TestLivePins(t *testing.T) {
	now := nextNow
	ls := []mover.Launch{
		{SessionID: "a", Slot: "/h/.claude-2", Status: "busy", StatusSince: now.Add(-10 * time.Hour)},
		{SessionID: "b", Slot: "/h/.claude-2", Status: "idle", StatusSince: now.Add(-5 * time.Hour)},
		{SessionID: "c", Slot: "/h/.claude-2", Status: "idle", StatusSince: now.Add(-7 * time.Hour)}, // idle 7 h: released
		{SessionID: "d", Slot: "/h/.claude-2", Status: "busy"},                                       // not pinned
		{SessionID: "e", Slot: "/h/.claude-9", Status: "busy"},                                       // not logged in here
		{SessionID: "f", Slot: "/h/.claude-3", Status: "busy"},                                       // tag no-move
	}
	keys := map[string]string{"/h/.claude-2": "acct:2/o", "/h/.claude-3": "acct:3/o"}
	got := livePins(ls, keys, map[string]bool{"a": true, "b": true, "c": true, "e": true}, map[string][]string{"f": {"no-move"}}, now)
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
