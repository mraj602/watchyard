//go:build !trusttest

package agent

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/peers/doorbelltest"
)

// trustAgent is an agent over a fake /proc and a throwaway HOME with the work account logged
// in and Doorbell installed in a venv. pid is the caller the peer credentials report.
type trustAgent struct {
	t                       *testing.T
	base, home, state, proc string
	venv, interp            string
	pid                     atomic.Int64 // the caller the peer credentials report
	gone                    atomic.Bool  // the caller's pidfd says it has ended
	r                       *running
}

func (a *trustAgent) write(path, content string, mode os.FileMode) {
	a.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		a.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		a.t.Fatal(err)
	}
}

// addProc writes one fake process: stat, comm, cmdline (argv), environ, exe and cwd.
func (a *trustAgent) addProc(pid, ppid int, comm, exe string, argv []string, start int) {
	d := filepath.Join(a.proc, fmt.Sprint(pid))
	a.write(filepath.Join(d, "stat"), fmt.Sprintf("%d (%s) S %d %s%d 0 0\n", pid, comm, ppid, strings.Repeat("0 ", 17), start), 0o600)
	a.write(filepath.Join(d, "comm"), comm+"\n", 0o600)
	a.write(filepath.Join(d, "cmdline"), strings.Join(argv, "\x00")+"\x00", 0o600)
	a.write(filepath.Join(d, "environ"), "PATH=/usr/bin\x00", 0o600)
	if err := os.Symlink(exe, filepath.Join(d, "exe")); err != nil {
		a.t.Fatal(err)
	}
	if err := os.Symlink(a.home, filepath.Join(d, "cwd")); err != nil {
		a.t.Fatal(err)
	}
}

// onTTY puts pid on /dev/pts/0 in session (the pid of its leader).
func (a *trustAgent) onTTY(pid, session int) {
	a.t.Helper()
	p := filepath.Join(a.proc, fmt.Sprint(pid), "stat")
	b, err := os.ReadFile(p)
	if err != nil {
		a.t.Fatal(err)
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	f := strings.Fields(s[i+1:])
	f[3], f[4] = fmt.Sprint(session), "34816"
	a.write(p, s[:i+1]+" "+strings.Join(f, " ")+"\n", 0o600)
}

// terminal is a shell (the session leader of /dev/pts/0) under the terminal emulator.
func (a *trustAgent) terminal(shell int) {
	a.addProc(shell, 1, "bash", "/usr/bin/bash", []string{"bash"}, shell*10)
	a.onTTY(shell, shell)
}

func newTrustAgent(t *testing.T) *trustAgent {
	base, err := os.MkdirTemp("", "wyt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if base, err = filepath.EvalSymlinks(base); err != nil { // macOS: /var is /private/var
		t.Fatal(err)
	}
	a := &trustAgent{t: t, base: base, home: filepath.Join(base, "h"), state: filepath.Join(base, "s"), proc: filepath.Join(base, "p")}
	slot := filepath.Join(a.home, ".claude-2")
	a.write(filepath.Join(slot, "settings.json"), "{}", 0o600)
	a.write(filepath.Join(slot, ".claude.json"),
		`{"oauthAccount":{"accountUuid":"acc-work","organizationUuid":"org-1","emailAddress":"w@example.test"}}`, 0o600)
	a.venv = filepath.Join(a.home, ".local/share/uv/tools/agent-doorbell")
	a.interp = filepath.Join(base, "py/bin/python3.12")
	a.write(a.interp, "#!fake\n", 0o755)
	a.write(filepath.Join(a.venv, "pyvenv.cfg"), "home = x\n", 0o644)
	if err := os.MkdirAll(filepath.Join(a.venv, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a.interp, filepath.Join(a.venv, "bin/python")); err != nil {
		a.t.Fatal(err)
	}
	doorbelltest.WriteDist(t, a.venv, "")
	if err := os.MkdirAll(a.proc, 0o700); err != nil {
		t.Fatal(err)
	}
	a.r = start(t, func(c *Config) {
		c.Load = func() (peers.Env, bool, error) {
			return peers.Env{Home: a.home, State: a.state, Machine: identity.Machine{ID: "m_TEST", Name: "laptop"},
				Proc: peers.LinuxProc{Root: a.proc}, FS: identity.OSFS{}, SlotLabels: map[string]string{"~/.claude-2": "work"}}, true, nil
		}
		c.PeerCred = func(*net.UnixConn) (int, int, error) { return int(a.pid.Load()), os.Getuid(), nil }
		c.PeerAlive = func(*net.UnixConn) (func() bool, func()) { return func() bool { return !a.gone.Load() }, func() {} }
	})
	t.Cleanup(func() { a.r.stop(t) })
	return a
}

func (a *trustAgent) call(req Request) Reply {
	a.t.Helper()
	rep, err := Call(a.r.sock, req, 2*time.Second)
	if err != nil {
		a.t.Fatal(err)
	}
	return rep
}

func (a *trustAgent) claude(pid, ppid int) {
	a.addProc(pid, ppid, "claude", "/home/u/.local/share/claude/versions/2.1.300", []string{"claude"}, 4242)
}

func (a *trustAgent) approved() bool {
	_, err := os.Stat(filepath.Join(a.state, "peers", "trusted.json"))
	return err == nil
}

// The approval guard: inside a Claude Code session (any ancestor), set and off are refused
// with nothing written; the record is only read (show).
func TestTrustRefusedInsideSession(t *testing.T) {
	a := newTrustAgent(t)
	a.claude(500, 1)
	a.addProc(501, 500, "bash", "/usr/bin/bash", []string{"bash"}, 4300)
	a.addProc(502, 501, "wy", "/usr/local/bin/wy", []string{"wy", "peers", "trust", "doorbell"}, 4301)
	a.onTTY(501, 501)
	a.onTTY(502, 501)
	a.pid.Store(502)
	rep := a.call(Request{Op: OpTrust, TrustOp: TrustSet, Program: a.venv, Account: "acc-work"})
	if rep.Error == nil || rep.Error.Code != peers.CodeNotASession || !strings.Contains(rep.Error.Detail, "pid 500") {
		t.Fatalf("set: %+v", rep)
	}
	if a.approved() {
		t.Fatal("a record was written from inside a session")
	}
	if rep := a.call(Request{Op: OpTrust, TrustOp: TrustOff}); rep.Error == nil || rep.Error.Code != peers.CodeNotASession {
		t.Errorf("off: %+v", rep)
	}
	if rep := a.call(Request{Op: OpTrust, TrustOp: TrustShow}); rep.Error != nil || rep.Trust != nil {
		t.Errorf("show: %+v", rep)
	}
}

// From a terminal: set writes the record (resolved by the agent), show reads it, off removes it.
func TestTrustFromTerminal(t *testing.T) {
	a := newTrustAgent(t)
	a.terminal(900)
	a.addProc(901, 900, "wy", "/usr/local/bin/wy", []string{"wy"}, 9001)
	a.onTTY(901, 900)
	a.pid.Store(901)
	rep := a.call(Request{Op: OpTrust, TrustOp: TrustSet, Program: filepath.Join(a.venv, "bin/doorbell"), Account: "acc-work"})
	if rep.Error != nil || rep.Trust == nil || rep.Trust.Venv != a.venv || rep.Trust.Interpreter != a.interp || rep.Warning != "" {
		t.Fatalf("set: %+v", rep)
	}
	if rep := a.call(Request{Op: OpTrust, TrustOp: TrustSet, Program: a.venv, Account: "acc-other"}); rep.Error == nil {
		t.Errorf("an unmanaged account: %+v", rep)
	}
	if rep := a.call(Request{Op: OpTrust, TrustOp: TrustShow}); rep.Trust == nil || rep.Trust.Account != "acc-work" {
		t.Errorf("show: %+v", rep)
	}
	if rep := a.call(Request{Op: OpTrust, TrustOp: TrustOff}); !rep.OK || a.approved() {
		t.Errorf("off: %+v", rep)
	}
}

// service_send identifies the caller itself: Doorbell (python → sh → wy) passes the check and
// needs the text on stdin; a Claude Code session above it, or no Doorbell at all, is
// not_a_session whatever the request says.
func TestServiceSendIdentity(t *testing.T) {
	a := newTrustAgent(t)
	a.terminal(900)
	a.pid.Store(900)
	if rep := a.call(Request{Op: OpTrust, TrustOp: TrustSet, Program: a.venv, Account: "acc-work"}); rep.Error != nil {
		t.Fatalf("set: %+v", rep)
	}
	tree := func(parent int) {
		for _, p := range []string{"700", "701", "702"} {
			os.RemoveAll(filepath.Join(a.proc, p))
		}
		a.addProc(700, parent, "doorbell", a.interp, []string{filepath.Join(a.venv, "bin/python"), filepath.Join(a.venv, "bin/doorbell")}, 7000)
		a.addProc(701, 700, "sh", "/usr/bin/sh", []string{"/bin/sh", "-c", "wy peers send tag:ci --stdin"}, 7001)
		a.addProc(702, 701, "wy", "/usr/local/bin/wy", []string{"wy", "peers", "send", "tag:ci", "--stdin"}, 7002)
		a.pid.Store(702)
	}
	tree(1)
	rep := a.call(Request{Op: OpServiceSend, To: "tag:ci", Text: "x"})
	if rep.Error == nil || rep.Error.Code != peers.CodeUsage || !strings.Contains(rep.Error.Detail, "stdin") {
		t.Errorf("no stdin: %+v", rep)
	}
	rep = a.call(Request{Op: OpServiceSend, To: "worker", Text: "x", Stdin: true})
	if rep.Error == nil || rep.Error.Code != peers.CodeNotAllowed || !strings.Contains(rep.Error.Detail, "only to a tag") {
		t.Errorf("handle: %+v", rep)
	}
	// No hub link in this agent: tags cannot be resolved, but the caller was identified.
	rep = a.call(Request{Op: OpServiceSend, To: "tag:ci", Text: "x", Stdin: true})
	if rep.Error == nil || rep.Error.Code != peers.CodeHubUnreachable {
		t.Errorf("tag: %+v", rep)
	}
	a.claude(500, 1)
	tree(500)
	rep = a.call(Request{Op: OpServiceSend, To: "tag:ci", Text: "x", Stdin: true})
	if rep.Error == nil || rep.Error.Code != peers.CodeNotASession || !strings.Contains(rep.Error.Detail, "Claude Code process") {
		t.Errorf("under claude: %+v", rep)
	}
	a.addProc(800, 1, "wy", "/usr/local/bin/wy", []string{"wy"}, 8000)
	a.pid.Store(800)
	rep = a.call(Request{Op: OpServiceSend, To: "tag:ci", Text: "x", Stdin: true})
	if rep.Error == nil || rep.Error.Code != peers.CodeNotASession {
		t.Errorf("plain terminal: %+v", rep)
	}
}

// The exploits of the review: a session starting wy outside its tree without a terminal
// (env -i setsid -f wy …, systemd-run --user wy …) or writing the op to the socket from a
// process of its own; a session's environment names; a requester that is gone (its pid may be
// reused). Each is refused by the agent with nothing written, whatever the request says.
func TestTrustGuardRefusesWithoutTerminal(t *testing.T) {
	a := newTrustAgent(t)
	set := Request{Op: OpTrust, TrustOp: TrustSet, Program: a.venv, Account: "acc-work"}
	// setsid -f / systemd-run: re-parented to pid 1 (or systemd --user), no controlling terminal.
	a.addProc(910, 1, "systemd", "/usr/lib/systemd/systemd", []string{"/usr/lib/systemd/systemd", "--user"}, 9100)
	a.addProc(911, 910, "wy", "/usr/local/bin/wy", []string{"wy", "peers", "trust", "doorbell"}, 9101)
	a.pid.Store(911)
	if rep := a.call(set); rep.Error == nil || rep.Error.Code != peers.CodeNotASession || !strings.Contains(rep.Error.Detail, "no controlling terminal") {
		t.Errorf("no terminal: %+v", rep)
	}
	// At a terminal, but with a Claude Code marker in the environment.
	a.terminal(920)
	a.addProc(921, 920, "wy", "/usr/local/bin/wy", []string{"wy"}, 9201)
	a.onTTY(921, 920)
	a.write(filepath.Join(a.proc, "921", "environ"), "PATH=/usr/bin\x00CLAUDECODE=1\x00", 0o600)
	a.pid.Store(921)
	if rep := a.call(set); rep.Error == nil || !strings.Contains(rep.Error.Detail, "CLAUDECODE") {
		t.Errorf("marker: %+v", rep)
	}
	// A clean terminal, but the requester ended while the agent read /proc.
	a.write(filepath.Join(a.proc, "921", "environ"), "PATH=/usr/bin\x00", 0o600)
	a.gone.Store(true)
	if rep := a.call(set); rep.Error == nil || !strings.Contains(rep.Error.Detail, "ended while it was checked") {
		t.Errorf("gone: %+v", rep)
	}
	if rep := a.call(Request{Op: OpTrust, TrustOp: TrustOff}); rep.Error == nil || rep.Error.Code != peers.CodeNotASession {
		t.Errorf("off while gone: %+v", rep)
	}
	if a.approved() {
		t.Fatal("a record was written")
	}
	a.gone.Store(false)
	if rep := a.call(set); rep.Error != nil || rep.Trust == nil {
		t.Errorf("the owner at the terminal: %+v", rep)
	}
}

// On an OS whose process reader has no command lines, trust is not supported.
func TestTrustUnsupportedProc(t *testing.T) {
	r := start(t, nil)
	defer r.stop(t)
	rep, err := Call(r.sock, Request{Op: OpTrust, TrustOp: TrustSet}, time.Second)
	if err != nil || rep.Error == nil || rep.Error.Code != peers.CodeUnsupported {
		t.Errorf("%+v %v", rep, err)
	}
}
