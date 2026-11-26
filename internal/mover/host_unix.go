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

func (h RealHost) StartWait(dir string, argv []string, log string, wait time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = CleanEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = f, f
	off, _ := f.Seek(0, 2)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		return errors.New(err.Error() + logTail(log, off))
	case <-time.After(wait):
		return nil
	}
}

// logTail is the end (at most 300 bytes) of what was written to log after offset off, as
// ": text" ("" when nothing).
func logTail(log string, off int64) string {
	b, err := os.ReadFile(log)
	if err != nil || int64(len(b)) <= off {
		return ""
	}
	b = b[off:]
	if len(b) > 300 {
		b = b[len(b)-300:]
	}
	return ": " + strings.TrimSpace(string(b))
}

func (h RealHost) Resumed(l Launch, to string, since time.Time) string {
	found := ""
	for _, e := range peers.ReadRegistry(identity.OSFS{}, identity.Slot{Dir: to}) {
		if e.PID <= 1 || e.SessionID == "" || e.CWD != l.CWD || e.StartedAt < since.UnixMilli() {
			continue
		}
		if e.SessionID != l.SessionID && (l.Name == "" || e.Name != l.Name) {
			continue
		}
		if st, err := h.Proc.StartTime(e.PID); err != nil || !peers.ProcStartMatches(e.ProcStartString(), st) {
			continue
		}
		if e.SessionID == l.SessionID {
			return e.SessionID
		}
		found = e.SessionID
	}
	return found
}

func (h RealHost) StopJob(dir, sid string) error {
	bin := ClaudeBin(h.sessionExes(dir, sid)...)
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(CleanEnv(os.Environ()), "CLAUDE_CONFIG_DIR="+dir)
		return cmd.CombinedOutput()
	}
	// claude stop takes the job's short id, not the session id: with the session id it says
	// "No session matching" and exits 0 (e2e v2), and the job runs on. The short id is the
	// session id's first 8 characters; `claude agents --json` (seconds, in which the session
	// may start its next step) is asked only when that does not match.
	nomatch := func(out []byte) bool { return strings.Contains(string(out), "No session matching") }
	out, err := run("stop", JobID(sid, nil))
	if err == nil && nomatch(out) {
		agents, _ := run("agents", "--json")
		if id := JobID(sid, agents); id != JobID(sid, nil) {
			out, err = run("stop", id)
		}
	}
	if err == nil && nomatch(out) {
		err = errors.New(strings.TrimSpace(string(out)))
	}
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

// TTY is the terminal pid's standard input, output or error is (a /dev/pts/N or /dev/ttyN).
func (h RealHost) TTY(pid int) string {
	for _, fd := range []string{"0", "1", "2"} {
		t, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", fd))
		if err == nil && (strings.HasPrefix(t, "/dev/pts/") || strings.HasPrefix(t, "/dev/tty")) {
			return t
		}
	}
	return ""
}

// WriteTTY writes text to tty as lines of their own (the terminal may still be in the raw
// mode claude left it in), only to a terminal device of this user.
func (h RealHost) WriteTTY(tty, text string) error {
	if !strings.HasPrefix(tty, "/dev/pts/") && !strings.HasPrefix(tty, "/dev/tty") {
		return errors.New("not a terminal: " + tty)
	}
	fi, err := os.Stat(tty)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New(tty + " is not this user's terminal")
	}
	f, err := os.OpenFile(tty, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\r\n" + strings.ReplaceAll(text, "\n", "\r\n") + "\r\n")
	return err
}

// sessionExes are the claude executables of session sid's live processes in config folder
// dir's registry (hints for ClaudeBin).
func (h RealHost) sessionExes(dir, sid string) []string {
	var out []string
	for _, e := range peers.ReadRegistry(identity.OSFS{}, identity.Slot{Dir: dir}) {
		if e.SessionID == sid {
			if p := sessionExe(e.PID); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
