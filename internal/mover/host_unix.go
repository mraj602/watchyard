//go:build !windows

package mover

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/peers"
)

// RealHost is the Host of this machine.
type RealHost struct {
	Proc  peers.Proc
	State State
}

func (h RealHost) Alive(l Launch) bool {
	st, err := h.Proc.StartTime(l.PID)
	return err == nil && (l.ProcStart == "" || st == l.ProcStart)
}

func (h RealHost) Kill(pid int) error {
	if pid <= 1 {
		return errors.New("refusing to kill pid " + strconv.Itoa(pid))
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

func (h RealHost) Tmux(args ...string) error {
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil && len(out) > 0 {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return err
}

func (h RealHost) Start(dir string, argv []string, log string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = CleanEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if log != "" {
		if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (h RealHost) StopJob(dir, sid string) error {
	cmd := exec.Command("claude", "stop", sid)
	cmd.Env = append(CleanEnv(os.Environ()), "CLAUDE_CONFIG_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) > 0 {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return err
}

func (h RealHost) Copies(dir, sid string, except int) []int {
	var out []int
	for _, e := range peers.ReadRegistry(identity.OSFS{}, identity.Slot{Dir: dir}) {
		if e.SessionID != sid || e.PID == except || e.PID <= 1 {
			continue
		}
		if st, err := h.Proc.StartTime(e.PID); err == nil && peers.ProcStartMatches(e.ProcStartString(), st) {
			out = append(out, e.PID)
		}
	}
	return out
}

func (h RealHost) Carry(l Launch, to string) error {
	if err := CarryTranscript(l.Slot, to, l.SessionID); err != nil {
		return err
	}
	// claude -p skips the trust check; an interactive session asks again, and claude --bg
	// refuses an untrusted folder outright ("Workspace not trusted").
	if l.Kind != KindHeadless {
		_, err := CarryTrust(l.Slot, to, l.CWD)
		return err
	}
	return nil
}

func (h RealHost) Order(pid int, o RunOrder) error { return h.State.WriteOrder(pid, o) }

func (h RealHost) Note(sessionID, text string) error {
	return h.State.WriteNote(sessionID, text, time.Now())
}

// CleanEnv is env without what belongs to the session that runs wy (SessionEnv): the moved
// session gets the new account's from its launch command.
func CleanEnv(env []string) []string { return peers.WithoutSessionEnv(env, true) }
