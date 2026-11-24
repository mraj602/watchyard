package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/leases"
	"github.com/mraj602/watchyard/internal/mover"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
)

// moveTestVars are the environment variables the move fixture's getenv returns.
var moveTestVars = map[string]string{}

type moveProc map[int][2]int

func (f moveProc) Info(pid int) (peerspkg.ProcInfo, error) {
	p, ok := f[pid]
	if !ok {
		return peerspkg.ProcInfo{}, errors.New("no such process")
	}
	return peerspkg.ProcInfo{PID: pid, PPID: p[0]}, nil
}
func (f moveProc) ConfigDir(int) (string, bool, error) { return "", false, nil }
func (f moveProc) StartTime(pid int) (string, error) {
	p, ok := f[pid]
	if !ok {
		return "", errors.New("no such process")
	}
	return strconv.Itoa(p[1]), nil
}

// recHost records the steps of a move; no process is ever touched.
type recHost struct {
	calls  []string
	killed map[int]bool
}

func (h *recHost) Alive(l mover.Launch) bool { return !h.killed[l.PID] }
func (h *recHost) Kill(pid int) error {
	h.calls = append(h.calls, "kill "+strconv.Itoa(pid))
	if h.killed == nil {
		h.killed = map[int]bool{}
	}
	h.killed[pid] = true
	return nil
}
func (h *recHost) StopJob(dir, sid string) error {
	h.calls = append(h.calls, "stop "+sid)
	return nil
}
func (h *recHost) Copies(string, string, int) []int { return nil }
func (h *recHost) TTY(pid int) string               { return "/dev/pts/" + strconv.Itoa(pid%10) }
func (h *recHost) WriteTTY(tty, text string) error {
	h.calls = append(h.calls, "tty "+tty+": "+text)
	return nil
}
func (h *recHost) Carry(l mover.Launch, to string) error {
	h.calls = append(h.calls, "carry "+l.SessionID)
	return nil
}
func (h *recHost) Tmux(args ...string) error {
	h.calls = append(h.calls, "tmux "+strings.Join(args, " "))
	return nil
}
func (h *recHost) Start(dir string, argv []string, log string) error {
	h.calls = append(h.calls, "start "+strings.Join(argv, " "))
	return nil
}
func (h *recHost) Order(pid int, o mover.RunOrder) error {
	h.calls = append(h.calls, "order "+strconv.Itoa(pid))
	return nil
}
func (h *recHost) Note(sid, text string) error {
	h.calls = append(h.calls, "note "+sid+": "+text)
	return nil
}

func moveFixture(t *testing.T, five float64, status string) (*nextFixture, *recHost, func(args ...string) (string, string, int)) {
	defer func(d time.Duration) { t.Cleanup(func() { mover.Settle = d }) }(mover.Settle)
	mover.Settle = 0
	defer func(d time.Duration) { t.Cleanup(func() { mover.AfterWatch = d }) }(mover.AfterWatch)
	mover.AfterWatch = 0
	f := newNextFixture(t)
	f.usage = true
	f.machineToken()
	f.seats = []map[string]any{
		seat("seat_1", "a1", "o1", "one@example.test", "claude-1", five, 20),
		seat("seat_2", "a2", "o2", "two@example.test", "claude-2", 10, 30),
	}
	reg := filepath.Join(f.home, ".claude", "sessions", "100.json")
	if err := os.MkdirAll(filepath.Dir(reg), 0o700); err != nil {
		t.Fatal(err)
	}
	since := nextNow.Add(-20 * time.Minute).UnixMilli()
	body := `{"pid":100,"sessionId":"5e55a0a0-0000-4000-8000-000000000001","cwd":"/work","procStart":"5","kind":"interactive","name":"lane-a","status":"` +
		status + `","statusUpdatedAt":` + strconv.FormatInt(since, 10) + `}`
	if err := os.WriteFile(reg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &recHost{}
	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		e := moveEnv{nextEnv: nextEnv{home: f.home, dirs: f.dirs, getenv: func(k string) string { return moveTestVars[k] }, now: func() time.Time { return nextNow },
			stdout: &out, stderr: &errOut, lookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
			environ: func() []string { return nil }, authRun: f.authStatus},
			sys: &mover.Sys{FS: identity.OSFS{}, Proc: moveProc{100: {90, 5}, 101: {90, 6}, 90: {1, 1}}, State: mover.NewState(f.dirs.State.Path),
				Cmdline: func(int) []string { return []string{"claude", "--model", "opus", "--resume", "old", "hello"} },
				Panes:   func() map[int]string { return map[int]string{100: "%7"} }},
			host: h, tags: func() map[string][]string { return nil }}
		err := moveCmd(e, args)
		code := 0
		var ee *exitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			code = ee.code
		case errors.Is(err, errUsage):
			code = 2
		default:
			code = 1
			errOut.WriteString(err.Error())
		}
		return out.String(), errOut.String(), code
	}
	return f, h, run
}

func TestMoveByHand(t *testing.T) {
	f, h, run := moveFixture(t, 40, "idle")
	out, errOut, code := run("--dry-run", "5e55a0a0", "claude-2")
	if code != 0 || !strings.Contains(out, "kill -9 100") || len(h.calls) != 0 {
		t.Fatalf("dry run: %d %q %q %q", code, out, errOut, h.calls)
	}
	out, errOut, code = run("lane-a", "2")
	if code != 0 {
		t.Fatalf("move: %d %q %q", code, out, errOut)
	}
	dir2 := filepath.Join(f.home, ".claude-2")
	cmd := "env CLAUDE_CONFIG_DIR=" + dir2 + " claude --model opus --resume 5e55a0a0-0000-4000-8000-000000000001"
	msg := "[Watchyard] This session was moved by hand by the owner from claude to claude-2 at " + nextNow.Local().Format("15:04") + ". It continues here under claude-2."
	want := []string{"tmux set-option -p -t %7 remain-on-exit on", "kill 100", "carry 5e55a0a0-0000-4000-8000-000000000001",
		"tmux respawn-pane -k -t %7 -c /work bash -ic " + mover.Shell([]string{"printf '\\n%s\\n\\n' " + mover.Shell([]string{msg}) + "; sleep 4; " + cmd}),
		"tmux set-option -p -u -t %7 remain-on-exit"}
	if len(h.calls) != 6 || !strings.HasPrefix(h.calls[0], "note 5e55a0a0-0000-4000-8000-000000000001: This session was moved by hand by the owner from claude to claude-2 at ") ||
		strings.Join(h.calls[1:], "|") != strings.Join(want, "|") {
		t.Fatalf("steps:\n%s", strings.Join(h.calls, "\n"))
	}
	es := mover.NewState(f.dirs.State.Path).Log(nextNow.Add(-time.Hour))
	if len(es) != 1 || es[0].To != "acct:a2/o2" || es[0].Kind != mover.KindTmux || es[0].Auto {
		t.Fatalf("log %+v", es)
	}
	ls, _, _ := (leases.Ledger{Dir: filepath.Join(f.dirs.State.Path, "next")}).Read(nextNow)
	if len(ls) != 1 || ls[0].Key != "acct:a2/o2" || ls[0].MovedFrom != "claude" || ls[0].Launched.IsZero() {
		t.Fatalf("leases %+v", ls)
	}
	// Mid-turn: refused without --now.
	_, h2, run2 := moveFixture(t, 40, "busy")
	if _, errOut, code := run2("lane-a", "2"); code == 0 || !strings.Contains(errOut, "middle of a turn") || len(h2.calls) != 0 {
		t.Fatalf("busy: %d %q", code, errOut)
	}
}

func TestMoveAuto(t *testing.T) {
	// Off by default: nothing moves.
	f, h, run := moveFixture(t, 100, "idle")
	f.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "UserPromptSubmit", "StopFailure:rate_limit")
	if out, _, code := run("--auto"); code != 0 || !strings.Contains(out, "Smart Account Switch is off") || len(h.calls) != 0 {
		t.Fatalf("off: %q %q", out, h.calls)
	}
	out, _, code := run("--plan")
	if code != 0 || !strings.Contains(out, "stopped at the 5h limit") || !strings.Contains(out, "-> claude-2") || len(h.calls) != 0 {
		t.Fatalf("plan: %d %q %q", code, out, h.calls)
	}
	f.set("account.smart_switch", "on")
	out, errOut, code := run("--auto")
	if code != 0 || !strings.Contains(out, "moved 5e55a0a0 lane-a from claude to claude-2") {
		t.Fatalf("auto: %d %q %q", code, out, errOut)
	}
	if !strings.Contains(h.calls[0], "(stopped at the 5h limit, which resets at") {
		t.Errorf("note: %q", h.calls[0])
	}
	// The automatic move reaches the hub's log with the projections that made it.
	files, _ := filepath.Glob(filepath.Join(f.dirs.State.Path, "outbox", "*.jsonl"))
	var ob []byte
	for _, name := range files {
		x, _ := os.ReadFile(name)
		ob = append(ob, x...)
	}
	for _, want := range []string{`"kind":"switch_move"`, `"auto":true`, `"window":"5h"`, `"p5":`, `"cap5":`, `"from":"claude"`} {
		if !strings.Contains(string(ob), want) {
			t.Errorf("switch_move lacks %s: %s", want, ob)
		}
	}
	// Pinned: never moved.
	f2, h2, run2 := moveFixture(t, 100, "idle")
	f2.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "StopFailure:rate_limit")
	f2.set("account.smart_switch", "on")
	nativePins(t, f2, `["5e55a0a0"]`)
	if out, _, code := run2("--auto"); code != 0 || out != "" || len(h2.calls) != 0 {
		t.Fatalf("pinned moved: %q %q", out, h2.calls)
	}
}

// hookEvents writes hook events of session sid to the outbox (event[:error], a minute apart,
// the last one a minute ago).
func (f *nextFixture) hookEvents(sid string, events ...string) {
	f.t.Helper()
	dir := filepath.Join(f.dirs.State.Path, "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	var b strings.Builder
	for i, ev := range events {
		name, errKind, _ := strings.Cut(ev, ":")
		at := nextNow.Add(-time.Duration(len(events)-i) * time.Minute)
		fmt.Fprintf(&b, `{"v":1,"id":"01JA00000000000000000H%04d","kind":"hook_event","ts":"%s","machine":"%s","slot":"~/.claude","payload":{"session_id":"%s","hook_event_name":"%s","error":"%s"}}`+"\n",
			i, at.Format(time.RFC3339Nano), f.machine, sid, name, errKind)
	}
	if err := os.WriteFile(filepath.Join(dir, nextNow.UTC().Format("2006-01-02")+".jsonl"), []byte(b.String()), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// fiveReset moves the 5-hour reset of every seat to now+d.
func (f *nextFixture) fiveReset(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.seats {
		for _, w := range s["windows"].([]map[string]any) {
			if w["key"] == "five_hour" {
				w["resets_at"] = nextNow.Add(d).Format(time.RFC3339)
			}
		}
	}
}

// TestMovePlanStoppedOnlyByLimit: the real worker case of 1.3.3-pre (idle tmux sessions at a
// prompt on an account at 100 % were planned as "stopped at the 5h limit"). Only a session
// whose last turn ended on the limit reply is stopped; it moves only when the reset is 20 min
// or more away; an idle session with no burn is never moved.
func TestMovePlanStoppedOnlyByLimit(t *testing.T) {
	const sid = "5e55a0a0-0000-4000-8000-000000000001"
	for _, c := range []struct {
		name   string
		events []string
		reset  time.Duration
		want   string // in the plan output
		moves  bool
	}{
		{"idle after a normal turn, reset in 20 min", []string{"UserPromptSubmit", "Stop"}, 20 * time.Minute,
			"No session needs a move.", false},
		{"idle after a normal turn, reset in 3 h", []string{"UserPromptSubmit", "Stop"}, 3 * time.Hour,
			"No session needs a move.", false},
		{"idle with no events at all", nil, 3 * time.Hour, "No session needs a move.", false},
		{"stopped by the limit, reset in 15 min", []string{"UserPromptSubmit", "StopFailure:rate_limit"}, 15 * time.Minute,
			"lane-a stays on claude: stopped at the 5h limit, which resets in 15m0s: waiting costs less than a move", false},
		{"stopped by the limit, reset in 3 h", []string{"UserPromptSubmit", "StopFailure:rate_limit"}, 3 * time.Hour,
			"lane-a (tmux): claude -> claude-2: stopped at the 5h limit", true},
		{"stopped earlier, then went on and finished", []string{"StopFailure:rate_limit", "UserPromptSubmit", "Stop"}, 3 * time.Hour,
			"No session needs a move.", false},
		{"another error is not the limit", []string{"UserPromptSubmit", "StopFailure:server_error"}, 3 * time.Hour,
			"No session needs a move.", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, h, run := moveFixture(t, 100, "idle")
			f.hookEvents(sid, c.events...)
			f.fiveReset(c.reset)
			out, _, code := run("--plan")
			if code != 0 || !strings.Contains(out, c.want) || strings.Contains(out, "-> claude-2") != c.moves || len(h.calls) != 0 {
				t.Fatalf("plan: %d\n%s", code, out)
			}
			f.set("account.smart_switch", "on")
			out, _, _ = run("--auto")
			if strings.Contains(out, "moved ") != c.moves {
				t.Fatalf("auto:\n%s\n%q", out, h.calls)
			}
		})
	}
}

// injectRates narrows the scope to the fixture's session and injects its burn and the
// accounts' levels (WY_MOVE_TEST) for one test.
func injectRates(t *testing.T, f *nextFixture, js string) {
	t.Helper()
	f.set("account.smart_switch_scope", "sessions:5e55a0a0")
	moveTestVars[moveTestEnv] = js
	t.Cleanup(func() { delete(moveTestVars, moveTestEnv) })
}

// A burning agent lane (tmux, a lease's task as its only prompt) is moved mid-turn after the
// spike filter's second pass; it resumes with the mid-turn note as its prompt.
func TestMoveAutoBurnerMidTurn(t *testing.T) {
	const sid = "5e55a0a0-0000-4000-8000-000000000001"
	f, h, run := moveFixture(t, 60, "busy")
	f.hookEvents(sid, "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	injectRates(t, f, `{"rates":{"5e55a0a0":20}}`) // 60 + 3 h × 20 = 120 > 95
	f.set("account.smart_switch", "on")
	out, _, code := run("--auto")
	if code != 0 || strings.Contains(out, "moved ") || len(h.calls) != 0 {
		t.Fatalf("first pass moved (spike filter): %q %q", out, h.calls)
	}
	out, _, _ = run("--plan")
	if !strings.Contains(out, "lane-a (tmux): claude -> claude-2: 5h projected 120% (cap 95%)") || !strings.Contains(out, "; mid-turn") {
		t.Fatalf("plan after one pass:\n%s", out)
	}
	out, errOut, code := run("--auto")
	if code != 0 || !strings.Contains(out, "moved 5e55a0a0 lane-a from claude to claude-2") {
		t.Fatalf("second pass: %d %q %q", code, out, errOut)
	}
	resp := ""
	for _, c := range h.calls {
		if strings.HasPrefix(c, "note ") {
			t.Fatalf("a mid-turn move wrote a SessionStart note: %q", c)
		}
		if strings.HasPrefix(c, "tmux respawn-pane") {
			resp = c
		}
	}
	if !strings.Contains(resp, "--resume "+sid+" ") || !strings.Contains(resp, "Watchyard moved this session from claude to claude-2 mid-turn at") ||
		!strings.Contains(resp, "Carry on with your task.") || !strings.Contains(resp, "different organisation") {
		t.Fatalf("respawn: %q", resp)
	}
}

// Owner correction to v2: an interactive tmux session the owner typed into is moved like
// an agent, mid-turn, in its pane, with the message above the resumed session and the note
// as its prompt.
func TestMoveAutoInteractiveMoves(t *testing.T) {
	const sid = "5e55a0a0-0000-4000-8000-000000000001"
	f, h, run := moveFixture(t, 60, "busy")
	f.hookEvents(sid, "UserPromptSubmit", "Stop", "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	injectRates(t, f, `{"rates":{"5e55a0a0":20},"spike_passes":1}`)
	out, _, _ := run("--plan")
	if !strings.Contains(out, "(tmux, interactive: goes on in place; it gets a message there): claude -> claude-2") {
		t.Fatalf("plan:\n%s", out)
	}
	f.set("account.smart_switch", "on")
	out, _, code := run("--auto")
	got := strings.Join(h.calls, "\n")
	if code != 0 || !strings.Contains(out, "moved 5e55") || !strings.Contains(got, "[Watchyard] This session moved from claude to claude-2") ||
		!strings.Contains(got, "It continues here under claude-2.") || !strings.Contains(got, "mid-turn") || strings.Contains(got, "note "+sid) {
		t.Fatalf("auto: %q\n%s", out, got)
	}
}

// Only accounts logged in on this machine are destinations, by the switch and by hand.
func TestMoveLoggedInOnly(t *testing.T) {
	f, h, run := moveFixture(t, 100, "idle")
	f.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "UserPromptSubmit", "StopFailure:rate_limit")
	f.fiveReset(3 * time.Hour)
	f.loggedOut["~/.claude-2"] = true
	out, _, _ := run("--plan")
	if strings.Contains(out, "-> claude-2") || !strings.Contains(out, "-> (none)") || !strings.Contains(out, "no logged-in account") {
		t.Fatalf("plan:\n%s", out)
	}
	if _, errOut, code := run("lane-a"); code != exitNoAccount || len(h.calls) != 0 {
		t.Fatalf("by hand: %d %q", code, errOut)
	}
	if _, errOut, code := run("lane-a", "claude-2"); code == 0 || !strings.Contains(errOut, "no logged-in account") {
		t.Fatalf("by hand to a logged-out account: %d %q", code, errOut)
	}
}

// Between steps (setup.md 8.6): a mid-turn move waits for the tool call in flight to end and
// says so in the note; when the wait runs out it names the step it was killed inside.
func TestMoveWaitsForStep(t *testing.T) {
	const sid = "5e55a0a0-0000-4000-8000-000000000001"
	defer func(d time.Duration) { mover.StepPoll = d }(mover.StepPoll)
	mover.StepPoll = 10 * time.Millisecond
	f, h, run := moveFixture(t, 60, "busy")
	f.hookEvents(sid, "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	injectRates(t, f, `{"rates":{"5e55a0a0":20},"spike_passes":1}`)
	f.set("account.smart_switch", "on")
	st := mover.NewState(f.dirs.State.Path)
	_ = st.RecordStep(sid, "toolu_1", "pre", "Bash", "sleep 60 && echo x >> f", time.Now())
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = st.RecordStep(sid, "toolu_1", "post", "Bash", "", time.Now())
	}()
	out, errOut, code := run("--auto")
	resp := strings.Join(h.calls, "\n")
	if code != 0 || !strings.Contains(out, "moved 5e55") || !strings.Contains(resp, "Your last step finished before the move.") {
		t.Fatalf("waited: %d %q %q\n%s", code, out, errOut, resp)
	}
	// The wait runs out (account.move_step_wait_min): moved inside the step, named.
	f2, h2, run2 := moveFixture(t, 60, "busy")
	f2.hookEvents(sid, "UserPromptSubmit")
	f2.fiveReset(3 * time.Hour)
	injectRates(t, f2, `{"rates":{"5e55a0a0":20},"spike_passes":1}`)
	f2.set("account.smart_switch", "on")
	f2.set("account.move_step_wait_min", "0.01")
	_ = mover.NewState(f2.dirs.State.Path).RecordStep(sid, "toolu_2", "pre", "Bash", "git push origin main", time.Now())
	out, _, _ = run2("--auto")
	resp = strings.Join(h2.calls, "\n")
	if !strings.Contains(out, "still inside a step (Bash)") || !strings.Contains(resp, "stopped inside a step (Bash: git push origin main) that may still be running") {
		t.Fatalf("ran out: %q\n%s", out, resp)
	}
	if s := mover.NewState(f2.dirs.State.Path).StepNow(sid, time.Now()); s.In {
		t.Error("the killed process's step is still in flight")
	}
}

// wy move --list reads the local file: a day by default, --since for longer, failed moves
// marked; each move, failed or not, also reaches the hub as a switch_move record.
func TestMoveListSinceAndHubRecord(t *testing.T) {
	f, _, run := moveFixture(t, 40, "idle")
	if _, errOut, code := run("lane-a", "2"); code != 0 {
		t.Fatalf("move: %d %q", code, errOut)
	}
	st := mover.NewState(f.dirs.State.Path)
	old := mover.LogEntry{At: nextNow.Add(-3 * 24 * time.Hour), SessionID: "5e55a0a0-0000-4000-8000-000000000001", From: "k1", To: "k2",
		Kind: mover.KindBackground, Auto: true, Reason: "5h 97% >= 95"}
	if err := st.Append(old); err != nil {
		t.Fatal(err)
	}
	bad := old
	bad.At, bad.Error = nextNow.Add(-2*time.Hour), "claude not found"
	if err := st.Append(bad); err != nil {
		t.Fatal(err)
	}
	out, _, _ := run("--list")
	if strings.Count(out, "\n") != 2 || strings.Contains(out, "5h 97%") && !strings.Contains(out, "FAILED: claude not found") {
		t.Fatalf("default list %q", out)
	}
	if !strings.Contains(out, "FAILED: claude not found") || !strings.Contains(out, "by hand") {
		t.Fatalf("default list %q", out)
	}
	out, _, _ = run("--list", "--since", "7d")
	if strings.Count(out, "\n") != 3 {
		t.Fatalf("7d list %q", out)
	}
	if out, _, _ = run("--list", "--since", "1h", "--json"); !strings.HasPrefix(out, "[") || strings.Contains(out, "claude not found") {
		t.Fatalf("1h json %q", out)
	}
	if _, errOut, code := run("--list", "--since", "soon"); code == 0 || !strings.Contains(errOut, "--since") {
		t.Fatalf("bad since: %d %q", code, errOut)
	}
	files, _ := filepath.Glob(filepath.Join(f.dirs.State.Path, "outbox", "*.jsonl"))
	var b []byte
	for _, name := range files {
		x, _ := os.ReadFile(name)
		b = append(b, x...)
	}
	if strings.Count(string(b), `"kind":"switch_move"`) != 1 {
		t.Fatalf("outbox %s", b)
	}
	for _, want := range []string{`"from":"claude"`, `"to":"claude-2"`, `"kind":"tmux"`, `"interactive":true`, `"reason":"by hand"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("record lacks %s: %s", want, b)
		}
	}
}
