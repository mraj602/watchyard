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

func (h RealHost) Order(pid int, o RunOrder) error { return h.State.WriteOrder(pid, o) }

func (h RealHost) Note(sessionID, text string) error {
	return h.State.WriteNote(sessionID, text, time.Now())
}

// CleanEnv is env without what belongs to the session that runs wy (its config folder, its
// messaging token and the other CLAUDE_CODE_* settings Claude Code gives its children): the
// moved session gets the new account's from its launch command.
func CleanEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CLAUDE_CONFIG_DIR", k == "CLAUDECODE", strings.HasPrefix(k, "CLAUDE_CODE_"),
			strings.HasPrefix(k, "CLAUDE_AGENT_SDK"), k == "CLAUDE_ENV_FILE":
			continue
		}
		out = append(out, kv)
	}
	return out
}
