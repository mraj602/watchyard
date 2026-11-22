package keys

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// The signing guard (keys.md 8): commands that unseal the owner key or replace a pin refuse
// to run inside a Claude Code session. It stops a session signing by accident or on an
// injected instruction; it is not a sandbox (8.1): the passphrase is what a session lacks.

// MaxAncestors bounds the ancestry walk.
const MaxAncestors = 64

// claudeEnv are the variables Claude Code sets for the Bash tool and hooks. Only their names
// are ever tested: a session's environment also holds CLAUDE_CODE_MESSAGING_TOKEN, a
// credential, whose value the guard never reads.
var claudeEnv = []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_MESSAGING_SOCKET"}

// IsClaudeEnvName reports whether name is one of the variables of step 1 (a name only; the
// agent's approval guard tests another process's environment names with it, peers.md 4.1a).
func IsClaudeEnvName(name string) bool { return slices.Contains(claudeEnv, name) }

// Proc is what the guard knows of one process.
type Proc struct {
	PPid  int
	Exe   string // Linux: readlink /proc/<pid>/exe
	Argv0 string // Linux: /proc/<pid>/cmdline up to the first NUL
	Comm  string // Linux: /proc/<pid>/comm; macOS: ps comm=
	Args  string // macOS: ps args=
	// EnvNames are the names (never the values) of the process's environment variables;
	// nil when it could not be read, which is skipped, not a refusal.
	EnvNames []string
}

// ProcReader reads one process. An error ends the walk (the pid is gone or not ours).
type ProcReader interface {
	Read(pid int) (Proc, error)
}

// Guard is the check with every source injectable, so tests use a fake /proc, a fake ps and
// a fake terminal. DefaultGuard is the real one.
type Guard struct {
	GOOS    string
	Getppid func() int
	// HasEnv reports whether this process's environment has a variable of that name.
	HasEnv  func(name string) bool
	Procs   ProcReader
	OpenTTY func() error
}

// DefaultGuard reads this process, /proc (Linux) or ps (macOS), and /dev/tty.
func DefaultGuard() Guard {
	g := Guard{
		GOOS:    runtime.GOOS,
		Getppid: os.Getppid,
		HasEnv:  func(n string) bool { _, ok := os.LookupEnv(n); return ok },
		OpenTTY: func() error {
			f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
			if err != nil {
				return err
			}
			return f.Close()
		},
	}
	switch runtime.GOOS {
	case "linux":
		g.Procs = LinuxProcs{Root: "/proc"}
	case "darwin":
		g.Procs = PSProcs{}
	}
	return g
}

// GuardError is a refusal (exit 4 in the CLI).
type GuardError struct {
	PID    int    // the Claude Code process found, 0 for other reasons
	Name   string // its name
	Reason string // set for refusals that are not a found session
}

func (e *GuardError) Error() string {
	if e.Reason != "" {
		return "refusing to sign: " + e.Reason
	}
	return fmt.Sprintf("refusing to sign: this command runs inside a Claude Code session (pid %d, %s).\n"+
		"Owner-key changes need you at a terminal that Claude Code did not start, or the board's owner sign-in.", e.PID, e.Name)
}

// ErrGuard matches every GuardError with errors.Is.
var ErrGuard = errors.New("refused by the signing guard")

func (e *GuardError) Is(target error) bool { return target == ErrGuard }

// CheckNotUnderClaude runs the real guard.
func CheckNotUnderClaude() error { return DefaultGuard().Check() }

// Check runs the guard in keys.md 8's order; the first hit refuses: Windows (no owner-key
// commands there yet), this process's environment, its ancestors, then a terminal.
func (g Guard) Check() error {
	if g.GOOS == "windows" {
		return &GuardError{Reason: "owner-key commands are not available on Windows yet; use the board's owner sign-in"}
	}
	if name, ok := g.InClaudeEnv(); ok {
		return &GuardError{PID: os.Getpid(), Name: "environment " + name}
	}
	if pid, name, ok := g.ClaudeAncestor(); ok {
		return &GuardError{PID: pid, Name: name}
	}
	if g.OpenTTY == nil || g.OpenTTY() != nil {
		return &GuardError{Reason: "this command needs a terminal (/dev/tty), and there is none"}
	}
	return nil
}

// InClaudeEnv is step 1: whether this process's environment has one of Claude Code's
// variables (by name), and which.
func (g Guard) InClaudeEnv() (string, bool) {
	if g.HasEnv == nil {
		return "", false
	}
	for _, n := range claudeEnv {
		if g.HasEnv(n) {
			return n, true
		}
	}
	return "", false
}

// ClaudeAncestor is step 2: the nearest ancestor that is Claude Code, walking from the parent
// towards pid 1, at most MaxAncestors steps, stopping at pid 1 or a process that cannot be
// read. `wy peers send` uses it to find its session's claude process (peers.md 4.1).
func (g Guard) ClaudeAncestor() (pid int, name string, ok bool) {
	if g.Procs == nil || g.Getppid == nil {
		return 0, "", false
	}
	pid = g.Getppid()
	for range MaxAncestors {
		if pid <= 1 {
			return 0, "", false
		}
		p, err := g.Procs.Read(pid)
		if err != nil {
			return 0, "", false
		}
		if n, hit := IsClaude(p); hit {
			return pid, n, true
		}
		pid = p.PPid
	}
	return 0, "", false
}

func isClaudeName(s string) bool {
	b := path.Base(filepath.ToSlash(s))
	return b == "claude" || b == "claude.exe"
}

// IsClaude reports whether a process is Claude Code (keys.md 8 step 2) and a name for it.
func IsClaude(p Proc) (string, bool) {
	for _, s := range []string{p.Exe, p.Argv0, p.Comm} {
		if s != "" && isClaudeName(s) {
			return "claude", true
		}
	}
	exe := filepath.ToSlash(p.Exe)
	if strings.Contains(exe, "/claude/versions/") || strings.Contains(exe, "/@anthropic-ai/claude-code/") {
		return path.Base(exe), true
	}
	if a := strings.TrimSpace(p.Args); a == "claude" || strings.HasSuffix(a, "/claude") {
		return "claude", true
	}
	if slices.Contains(p.EnvNames, "CLAUDECODE") {
		name := path.Base(filepath.ToSlash(p.Comm))
		if name == "" || name == "." {
			name = "CLAUDECODE"
		}
		return name, true
	}
	return "", false
}

// ---- Linux: /proc ----

// LinuxProcs reads processes from a /proc tree (Root "/proc"; tests pass a fake one).
type LinuxProcs struct{ Root string }

// Read reads stat (required), exe, cmdline, comm and environ (names only) of pid.
func (l LinuxProcs) Read(pid int) (Proc, error) {
	dir := filepath.Join(l.Root, strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Proc{}, err
	}
	// The comm field is in parentheses and may hold anything: the ppid is the second field
	// after the last ')'.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return Proc{}, errors.New("bad stat")
	}
	f := strings.Fields(string(stat[i+1:]))
	if len(f) < 2 {
		return Proc{}, errors.New("bad stat")
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return Proc{}, errors.New("bad stat")
	}
	p := Proc{PPid: ppid}
	p.Exe, _ = os.Readlink(filepath.Join(dir, "exe"))
	if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		p.Argv0, _, _ = strings.Cut(string(b), "\x00")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		p.Comm = strings.TrimRight(string(b), "\n")
	}
	if fh, err := os.Open(filepath.Join(dir, "environ")); err == nil {
		p.EnvNames, _ = EnvNames(fh)
		fh.Close()
	}
	return p, nil
}

// EnvNames reads a NUL-separated environment block and returns the variable names only. The
// values pass through the reader's buffer and are never copied or returned.
func EnvNames(r io.Reader) ([]string, error) {
	br := bufio.NewReader(r)
	names := []string{}
	for {
		var name []byte
		for {
			c, err := br.ReadByte()
			if err != nil {
				if len(name) > 0 {
					names = append(names, string(name))
				}
				if errors.Is(err, io.EOF) {
					return names, nil
				}
				return names, err
			}
			if c == 0 {
				break // a variable without '=': its whole text is a name
			}
			if c == '=' {
				if err := skipToNUL(br); err != nil && !errors.Is(err, io.EOF) {
					return names, err
				}
				break
			}
			name = append(name, c)
		}
		if len(name) > 0 {
			names = append(names, string(name))
		}
	}
}

func skipToNUL(br *bufio.Reader) error {
	for {
		_, err := br.ReadSlice(0)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return err
		}
	}
}

// ---- macOS: ps ----

// PSProcs reads processes with `ps -o ppid=,comm=,args= -p <pid>` (no environments of other
// processes on macOS). Run, when set, replaces running ps (tests).
type PSProcs struct {
	Run func(pid int) (string, error)
}

// Read runs ps for one pid.
func (p PSProcs) Read(pid int) (Proc, error) {
	run := p.Run
	if run == nil {
		run = func(pid int) (string, error) {
			out, err := exec.Command("ps", "-o", "ppid=,comm=,args=", "-p", strconv.Itoa(pid)).Output()
			return string(out), err
		}
	}
	out, err := run(pid)
	if err != nil {
		return Proc{}, err
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) < 2 {
		return Proc{}, errors.New("no such process")
	}
	ppid, err := strconv.Atoi(f[0])
	if err != nil {
		return Proc{}, errors.New("bad ps output")
	}
	return Proc{PPid: ppid, Comm: f[1], Args: strings.Join(f[2:], " ")}, nil
}
