package main

import (
	"bytes"
	"errors"
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
		e := moveEnv{nextEnv: nextEnv{home: f.home, dirs: f.dirs, getenv: func(string) string { return "" }, now: func() time.Time { return nextNow },
			stdout: &out, stderr: &errOut, lookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
			environ: func() []string { return nil }, authRun: f.authStatus},
			sys: &mover.Sys{FS: identity.OSFS{}, Proc: moveProc{100: {90, 5}, 90: {1, 1}}, State: mover.NewState(f.dirs.State.Path),
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
	want := []string{"tmux set-option -p -t %7 remain-on-exit on", "kill 100", "carry 5e55a0a0-0000-4000-8000-000000000001", "tmux respawn-pane -k -t %7 -c /work bash -ic '" + cmd + "'",
		"tmux set-option -p -u -t %7 remain-on-exit"}
	if len(h.calls) != 6 || !strings.HasPrefix(h.calls[0], "note 5e55a0a0-0000-4000-8000-000000000001: Watchyard moved this session from claude to claude-2 at ") ||
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
	// Pinned: never moved.
	f2, h2, run2 := moveFixture(t, 100, "idle")
	f2.set("account.smart_switch", "on")
	if out, errOut, code := run2("--pin-account", "lane-a"); code != 0 || !strings.Contains(out, "pinned") {
		t.Fatalf("pin: %q %q", out, errOut)
	}
	if out, _, code := run2("--auto"); code != 0 || out != "" || len(h2.calls) != 0 {
		t.Fatalf("pinned moved: %q %q", out, h2.calls)
	}
}
