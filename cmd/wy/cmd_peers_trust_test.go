//go:build !trusttest

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/agent"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/keys"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/peers/doorbelltest"
)

// trustCLI is a temp HOME with two logged-in folders and a Doorbell venv, and fake guard, tty
// and agent for `wy peers trust`. Nothing real is read: no HOME, no agent socket, no terminal.
type trustCLI struct {
	t           *testing.T
	home, state string
	venv        string
	guardErr    error
	tty         *fakeTTY
	noAgent     bool
	calls       []agent.Request
	reply       agent.Reply
	goos        string
}

func newTrustCLI(t *testing.T) *trustCLI {
	base, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var is /private/var
	if err != nil {
		t.Fatal(err)
	}
	c := &trustCLI{t: t, home: filepath.Join(base, "h"), state: filepath.Join(base, "s"), goos: "linux"}
	for slot, acc := range map[string]string{".claude-2": "acc-work", ".claude-5": "acc-personal"} {
		peersWriteFileT(t, filepath.Join(c.home, slot, ".claude.json"), fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":"o"}}`, acc), 0o600)
	}
	c.venv = filepath.Join(c.home, ".local/share/uv/tools/agent-doorbell")
	interp := filepath.Join(base, "py/bin/python3.12")
	peersWriteFileT(t, interp, "#!fake\n", 0o755)
	peersWriteFileT(t, filepath.Join(c.venv, "pyvenv.cfg"), "home = x\n", 0o644)
	if err := os.MkdirAll(filepath.Join(c.venv, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(interp, filepath.Join(c.venv, "bin/python")); err != nil {
		t.Fatal(err)
	}
	doorbelltest.WriteDist(t, c.venv, "")

	oldEnv, oldDeps := peersEnv, peersTrustDeps
	t.Cleanup(func() { peersEnv, peersTrustDeps = oldEnv, oldDeps })
	peersEnv = func() (peerEnv, error) {
		return peerEnv{Enabled: true, Env: peerspkg.Env{Home: c.home, State: c.state, Machine: identity.Machine{ID: "m_T", Name: "laptop"},
			SlotLabels: map[string]string{"~/.claude-2": "work", "~/.claude-5": "personal"},
			Proc:       peerspkg.LinuxProc{Root: filepath.Join(base, "p")}, FS: identity.OSFS{}, Now: time.Now}}, nil
	}
	peersTrustDeps = func() trustDeps {
		return trustDeps{
			goos:  c.goos,
			guard: func() error { return c.guardErr },
			tty: func() (keysTTY, error) {
				if c.tty == nil {
					return nil, errors.New("no terminal")
				}
				return c.tty, nil
			},
			lookPath: func(string) (string, error) { return filepath.Join(c.venv, "bin/doorbell"), nil },
			call: func(pe peerEnv, req agent.Request) (agent.Reply, error) {
				c.calls = append(c.calls, req)
				if c.noAgent {
					return agent.Reply{}, agent.ErrNoAgent
				}
				if c.reply.Error != nil || c.reply.Trust != nil || c.reply.OK {
					return c.reply, nil
				}
				// The agent's part, done in place: the record is written by the Env.
				switch req.TrustOp {
				case agent.TrustSet:
					rec, err := pe.Env.ApproveDoorbell(req.Program, req.Account)
					if err != nil {
						return agent.Reply{Error: &agent.Error{Code: peerspkg.CodeFailed, Detail: err.Error()}}, nil
					}
					return agent.Reply{OK: true, Trust: &rec}, nil
				case agent.TrustOff:
					had, _ := pe.Env.RevokeDoorbell("cli")
					return agent.Reply{OK: had}, nil
				}
				rec, _ := pe.Env.LoadTrust()
				return agent.Reply{OK: true, Trust: rec}, nil
			},
		}
	}
	return c
}

func peersWriteFileT(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (c *trustCLI) run(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := dispatch(append([]string{"peers", "trust"}, args...), &o, &e)
	return code, o.String(), e.String()
}

// approve runs `wy peers trust doorbell args…` at a terminal that answers yes (and takes the
// default account when asked).
func (c *trustCLI) approve(args ...string) (int, string, string) {
	c.tty = &fakeTTY{t: c.t, answers: []string{"", "y"}}
	if len(args) > 0 {
		c.tty = &fakeTTY{t: c.t, answers: []string{"y"}}
	}
	return c.run(append([]string{"doorbell"}, args...)...)
}

func (c *trustCLI) record() *peerspkg.TrustRecord {
	rec, err := peerspkg.Env{State: c.state}.LoadTrust()
	if err != nil {
		c.t.Fatal(err)
	}
	return rec
}

// The default build refuses approval (and revocation) from inside a Claude Code session: the
// guard's refusal stops the command before the agent is asked or anything is written.
func TestPeersTrustRefusedInsideSession(t *testing.T) {
	c := newTrustCLI(t)
	c.guardErr = &keys.GuardError{PID: 500, Name: "claude"}
	c.tty = &fakeTTY{t: t, answers: []string{"", "y"}}
	for _, args := range [][]string{{"doorbell"}, {"doorbell", "--off"}} {
		code, out, errs := c.run(args...)
		if code != peerspkg.ExitGuard || !strings.Contains(errs, "runs inside a Claude Code session (pid 500") || strings.Contains(out, "approved") {
			t.Errorf("%v: %d %q %q", args, code, out, errs)
		}
	}
	if len(c.calls) != 0 || c.record() != nil {
		t.Errorf("the agent was asked (%v) or a record written", c.calls)
	}
	if peerspkg.TrustGuardOff {
		t.Fatal("the default build has the guard off")
	}
}

func TestPeersTrustNotLinux(t *testing.T) {
	c := newTrustCLI(t)
	c.goos = "darwin"
	code, out, _ := c.run("doorbell")
	if code != peerspkg.ExitUnsupported || out != "wy peers trust: not supported on this OS yet\n" || len(c.calls) != 0 || c.record() != nil {
		t.Errorf("%d %q %v", code, out, c.calls)
	}
}

// There is no --yes: approval always asks at the terminal. --account picks by label; the plan
// and the record are printed.
func TestPeersTrustNoYes(t *testing.T) {
	c := newTrustCLI(t)
	if code, _, errs := c.run("doorbell", "--yes"); code != 2 || !strings.Contains(errs, "-yes") || len(c.calls) != 0 {
		t.Errorf("--yes: %d %q %v", code, errs, c.calls)
	}
	code, out, errs := c.approve("--account", "personal")
	if code != 0 {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if len(c.calls) != 1 || c.calls[0].Program != filepath.Join(c.venv, "bin/doorbell") || c.calls[0].Account != "acc-personal" {
		t.Errorf("calls: %+v", c.calls)
	}
	if !strings.Contains(c.tty.printed.String(), "program:     "+filepath.Join(c.venv, "bin/doorbell")) || !strings.Contains(out, "Doorbell is approved on laptop") ||
		strings.Contains(out, "test build") {
		t.Errorf("out: %q", out)
	}
	if rec := c.record(); rec == nil || rec.Account != "acc-personal" || rec.Venv != c.venv {
		t.Errorf("record: %+v", rec)
	}
	if code, _, _ := c.approve("--account", "nobody"); code == 0 {
		t.Error("an unknown account accepted")
	}
}

// At a terminal: the account question, then y/N; no means nothing is written.
func TestPeersTrustAsks(t *testing.T) {
	c := newTrustCLI(t)
	c.tty = &fakeTTY{t: t, answers: []string{"", "n"}}
	code, out, _ := c.run("doorbell", "--program", c.venv)
	if code != 0 || !strings.Contains(out, "Not approved") || len(c.calls) != 0 {
		t.Fatalf("no: %d %q %v", code, out, c.calls)
	}
	if !strings.Contains(c.tty.printed.String(), "interpreter:") || len(c.tty.prompts) != 2 ||
		!strings.HasPrefix(c.tty.prompts[1], "Let Doorbell at this path send to tags as work?") {
		t.Errorf("tty: %q %q", c.tty.printed.String(), c.tty.prompts)
	}
	c.tty = &fakeTTY{t: t, answers: []string{"2", "y"}}
	if code, out, errs := c.run("doorbell"); code != 0 || c.record() == nil || c.record().Account != "acc-personal" {
		t.Fatalf("yes: %d %q %q", code, out, errs)
	}
	// Without a terminal: refused.
	c.tty = nil
	if code, _, errs := c.run("doorbell"); code != peerspkg.ExitGuard || !strings.Contains(errs, "terminal") {
		t.Errorf("no tty: %d %q", code, errs)
	}
}

func TestPeersTrustOff(t *testing.T) {
	c := newTrustCLI(t)
	if code, _, _ := c.approve(); code != 0 {
		t.Fatal("approve")
	}
	code, out, _ := c.run("doorbell", "--off")
	if code != 0 || !strings.Contains(out, "no longer a trusted sender") || c.record() != nil {
		t.Errorf("off: %d %q", code, out)
	}
	code, out, _ = c.run("doorbell", "--off")
	if code != 0 || !strings.Contains(out, "nothing changed") {
		t.Errorf("off again: %d %q", code, out)
	}
	// Without the agent, --off still removes the record here.
	if code, _, _ := c.approve(); code != 0 {
		t.Fatal("approve")
	}
	c.noAgent = true
	code, out, _ = c.run("doorbell", "--off")
	if code != 0 || !strings.Contains(out, "agent is not running") || c.record() != nil {
		t.Errorf("off without agent: %d %q", code, out)
	}
	// Approval needs the agent.
	if code, _, errs := c.approve(); code == 0 || !strings.Contains(errs, "agent is not running") {
		t.Errorf("approve without agent: %d %q", code, errs)
	}
}

func TestPeersTrustUsageAndShow(t *testing.T) {
	c := newTrustCLI(t)
	if code, _, errs := c.run("cron"); code != 2 || !strings.Contains(errs, "only doorbell") {
		t.Errorf("other sender: %d %q", code, errs)
	}
	if code, _, _ := c.run("doorbell", "--off", "--account", "work"); code != 2 {
		t.Errorf("--off with --account: %d", code)
	}
	if code, out, _ := c.run(); code != 0 || !strings.Contains(out, "not approved") {
		t.Errorf("show: %d %q", code, out)
	}
	c.approve()
	if code, out, _ := c.run(); code != 0 || !strings.Contains(out, "sends as:    work (acc-work)") || strings.Contains(out, "test build") {
		t.Errorf("show approved: %d %q", code, out)
	}
	// A record a test build made is shown as such, and still shown as approved.
	rec := c.record()
	rec.TestBuild = true
	if err := (peerspkg.Env{State: c.state}).SaveTrust(rec); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := c.run(); code != 0 || !strings.Contains(out, "Doorbell is approved on laptop") || !strings.Contains(out, "approved by a test build") {
		t.Errorf("show test build: %d %q", code, out)
	}
}
