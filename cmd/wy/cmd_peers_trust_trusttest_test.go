//go:build trusttest

package main

import (
	"bytes"
	"errors"
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

// The trusttest build (peers.md 15): approval and --off run where the guard would refuse and
// ask nothing, and say so; the record is the normal one.
func TestTrustTestBuildRelaxesGuard(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var is /private/var
	if err != nil {
		t.Fatal(err)
	}
	home, state := filepath.Join(base, "h"), filepath.Join(base, "s")
	write := func(p, s string, m os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), m); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude-2/.claude.json"), `{"oauthAccount":{"accountUuid":"acc-work","organizationUuid":"o"}}`, 0o600)
	venv := filepath.Join(home, "venv")
	interp := filepath.Join(base, "py/python3.12")
	write(interp, "#!fake\n", 0o755)
	write(filepath.Join(venv, "pyvenv.cfg"), "\n", 0o644)
	doorbelltest.WriteDist(t, venv, "#!"+interp)

	oldEnv, oldDeps := peersEnv, peersTrustDeps
	t.Cleanup(func() { peersEnv, peersTrustDeps = oldEnv, oldDeps })
	peersEnv = func() (peerEnv, error) {
		return peerEnv{Enabled: true, Env: peerspkg.Env{Home: home, State: state, Machine: identity.Machine{ID: "m_T", Name: "laptop"},
			SlotLabels: map[string]string{"~/.claude-2": "work"}, Proc: peerspkg.LinuxProc{Root: filepath.Join(base, "p")},
			FS: identity.OSFS{}, Now: time.Now}}, nil
	}
	peersTrustDeps = func() trustDeps {
		return trustDeps{goos: "linux",
			guard:    func() error { return &keys.GuardError{PID: 500, Name: "claude"} },
			tty:      func() (keysTTY, error) { return nil, errors.New("no terminal") },
			lookPath: func(string) (string, error) { return filepath.Join(venv, "bin/doorbell"), nil },
			call: func(pe peerEnv, req agent.Request) (agent.Reply, error) {
				if req.TrustOp == agent.TrustSet {
					rec, err := pe.Env.ApproveDoorbell(req.Program, req.Account)
					if err != nil {
						return agent.Reply{}, err
					}
					return agent.Reply{OK: true, Trust: &rec, Warning: peerspkg.TrustTestWarning}, nil
				}
				had, _ := pe.Env.RevokeDoorbell("cli")
				return agent.Reply{OK: had, Warning: peerspkg.TrustTestWarning}, nil
			}}
	}
	var o, e bytes.Buffer
	if code := dispatch([]string{"peers", "trust", "doorbell"}, &o, &e); code != 0 {
		t.Fatalf("%d %q %q", code, o.String(), e.String())
	}
	if !strings.Contains(e.String(), "TEST BUILD: approval guard is off") {
		t.Errorf("stderr: %q", e.String())
	}
	// The same record a normal build reads, marked test_build.
	rec, err := peerspkg.Env{State: state}.LoadTrust()
	if err != nil || rec == nil || rec.Venv != venv || rec.Account != "acc-work" || !rec.TestBuild {
		t.Errorf("record: %+v %v", rec, err)
	}
	// Marked as a test build's, and shown so.
	if !strings.Contains(o.String(), "approved by a test build") {
		t.Errorf("stdout: %q", o.String())
	}
	o.Reset()
	e.Reset()
	if code := dispatch([]string{"peers", "trust", "doorbell", "--off"}, &o, &e); code != 0 || !strings.Contains(o.String(), "no longer") {
		t.Errorf("off: %d %q %q", code, o.String(), e.String())
	}
}
