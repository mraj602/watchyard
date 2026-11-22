package peers

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// MaxWalk is how many ancestors the walk visits (keys.md 8 step 2).
const MaxWalk = 64

// ProcInfo is what the walk needs to know about one process.
type ProcInfo struct {
	PID   int
	PPID  int
	Exe   string // readlink /proc/<pid>/exe; empty when unreadable
	Argv0 string
	Comm  string
	Args  string // the whole command line, space separated (macOS: ps args)
}

// Proc reads the process tree. Tests give it a fake /proc (LinuxProc{Root: dir}) or a map.
// The environment is reachable only through ConfigDir and HasEnv, so no caller can read, keep
// or log another variable: a session's environment holds CLAUDE_CODE_MESSAGING_TOKEN, a
// credential Watchyard treats like sessions/*.key (keys.md 8).
type Proc interface {
	Info(pid int) (ProcInfo, error)
	// ConfigDir is CLAUDE_CONFIG_DIR from the process's environment (set is false when the
	// variable is absent or empty). ErrNoEnv means this OS cannot read it (macOS).
	ConfigDir(pid int) (value string, set bool, err error)
	// StartTime is the process start in the form a registry's procStart uses (Linux: the
	// stat starttime in clock ticks, decimal). Inferred, not documented (registry.go).
	StartTime(pid int) (string, error)
}

// ErrNoEnv: another process's environment cannot be read here (macOS).
var ErrNoEnv = errors.New("this OS cannot read another process's environment")

// NewProc is the reader for the running OS.
func NewProc() Proc {
	switch runtime.GOOS {
	case "linux":
		return LinuxProc{Root: "/proc"}
	case "darwin":
		return PSProc{}
	}
	return unsupportedProc{}
}

type unsupportedProc struct{}

func (unsupportedProc) Info(int) (ProcInfo, error)          { return ProcInfo{}, ErrUnsupported }
func (unsupportedProc) ConfigDir(int) (string, bool, error) { return "", false, ErrUnsupported }
func (unsupportedProc) StartTime(int) (string, error)       { return "", ErrUnsupported }

// LinuxProc reads /proc (or a fake of it: Root is the folder that holds <pid>/).
type LinuxProc struct{ Root string }

func (l LinuxProc) path(pid int, name string) string {
	return filepath.Join(l.Root, strconv.Itoa(pid), name)
}

// statFields returns the fields of /proc/<pid>/stat after the command name (which may hold
// spaces and parentheses: everything up to the last ")" belongs to it). fields[0] is the
// state, fields[1] the ppid, fields[19] the start time.
func (l LinuxProc) statFields(pid int) ([]string, error) {
	b, err := os.ReadFile(l.path(pid, "stat"))
	if err != nil {
		return nil, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil, fmt.Errorf("proc %d: malformed stat", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return nil, fmt.Errorf("proc %d: short stat", pid)
	}
	return f, nil
}

func (l LinuxProc) Info(pid int) (ProcInfo, error) {
	f, err := l.statFields(pid)
	if err != nil {
		return ProcInfo{}, err
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return ProcInfo{}, fmt.Errorf("proc %d: bad ppid", pid)
	}
	in := ProcInfo{PID: pid, PPID: ppid}
	in.Exe, _ = os.Readlink(l.path(pid, "exe"))
	if b, err := os.ReadFile(l.path(pid, "comm")); err == nil {
		in.Comm = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(l.path(pid, "cmdline")); err == nil {
		parts := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		in.Argv0 = parts[0]
		in.Args = strings.Join(parts, " ")
	}
	return in, nil
}

func (l LinuxProc) StartTime(pid int) (string, error) {
	f, err := l.statFields(pid)
	if err != nil {
		return "", err
	}
	return f[19], nil
}

// ConfigDir scans environ for CLAUDE_CONFIG_DIR and returns only that value. An unreadable
// environ (not ours, or gone) is an error: the caller refuses rather than guessing.
func (l LinuxProc) ConfigDir(pid int) (string, bool, error) {
	b, err := os.ReadFile(l.path(pid, "environ"))
	if err != nil {
		return "", false, err
	}
	const key = "CLAUDE_CONFIG_DIR="
	for _, kv := range bytes.Split(b, []byte{0}) {
		if bytes.HasPrefix(kv, []byte(key)) {
			v := string(kv[len(key):])
			return v, v != "", nil
		}
	}
	return "", false, nil
}

// PSProc is the macOS reader: `ps -o ppid=,comm=,args= -p <pid>` per step (keys.md 8). It
// cannot read an environment.
type PSProc struct{}

func (PSProc) Info(pid int) (ProcInfo, error) {
	out, err := exec.Command("ps", "-o", "ppid=,comm=,args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ProcInfo{}, err
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return ProcInfo{}, fmt.Errorf("proc %d: no ps output", pid)
	}
	ppid, err := strconv.Atoi(f[0])
	if err != nil {
		return ProcInfo{}, err
	}
	args := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), f[0]))
	args = strings.TrimSpace(strings.TrimPrefix(args, f[1]))
	argv0 := f[1]
	if len(f) > 2 {
		argv0 = f[2]
	}
	return ProcInfo{PID: pid, PPID: ppid, Exe: f[1], Comm: filepath.Base(f[1]), Argv0: argv0, Args: args}, nil
}

func (PSProc) ConfigDir(int) (string, bool, error) { return "", false, ErrNoEnv }

// StartTime on macOS is `ps -o lstart=`. The registry's procStart format there is
// not documented.
func (PSProc) StartTime(pid int) (string, error) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	return strings.TrimSpace(string(out)), err
}

// Process names of Claude Code (peers.md 14: not documented for every install; the registry
// file is what decides, so a false hit here only costs one lookup).
var (
	claudeBaseNames = []string{"claude", "claude.exe"}
	claudePathParts = []string{"/claude/versions/", "/@anthropic-ai/claude-code/"}
)

// LooksLikeClaude is the process test of keys.md 8 step 2, without its environment marker:
// CLAUDECODE is inherited by every child of a session (the Bash tool's shell, hooks), so
// naming it here would pick the shell as "the claude process" (peers.md 4.1 differs from
// the signing guard in this).
func LooksLikeClaude(p ProcInfo) bool {
	base := func(s string) string { return filepath.Base(strings.TrimSpace(s)) }
	for _, n := range claudeBaseNames {
		if (p.Exe != "" && base(p.Exe) == n) || (p.Argv0 != "" && base(p.Argv0) == n) || p.Comm == n {
			return true
		}
	}
	for _, part := range claudePathParts {
		if strings.Contains(p.Exe, part) {
			return true
		}
	}
	if a := strings.TrimSpace(p.Args); a != "" && (strings.HasSuffix(a, "/claude") || strings.HasSuffix(a, " claude")) {
		return true
	}
	return strings.HasSuffix(p.Argv0, "/claude")
}

// IsClaudeProgram reports whether p is the Claude Code program itself, not only something
// named like it: its executable is a Claude Code binary (native installer: a file under
// .../claude/versions/, or one named claude), or its command line runs the npm package's
// entry point. A shell wrapper called `claude` has the shell as its executable and fails it.
func IsClaudeProgram(p ProcInfo) bool {
	if p.Exe != "" {
		b := filepath.Base(strings.TrimSpace(p.Exe))
		for _, n := range claudeBaseNames {
			if b == n {
				return true
			}
		}
		for _, part := range claudePathParts {
			if strings.Contains(p.Exe, part) {
				return true
			}
		}
	}
	return strings.Contains(p.Args, "/@anthropic-ai/claude-code/")
}

// Ancestors returns start and the processes above it, nearest first, up to MaxWalk steps,
// stopping at pid 1 or a pid that cannot be read. Callers pass os.Getppid(). It does not say
// whether it reached the top: WalkAncestors does, for the checks that must not take a cut
// walk as "nothing above".
func Ancestors(p Proc, start int) []ProcInfo {
	out, _ := WalkAncestors(p, start)
	return out
}

// WalkAncestors is Ancestors plus whether the walk is complete (peers.md 2.3): it ended at the
// top of the tree, because the last process read has pid 1 above it (a subreaper such as
// systemd --user is passed through to pid 1) or none (parent 0: pid 1 itself, or the top of a
// PID namespace). A walk cut short by MaxWalk, by a process that cannot be read or by a loop
// is not complete.
func WalkAncestors(p Proc, start int) (out []ProcInfo, complete bool) {
	pid := start
	switch {
	case pid == 1:
		return nil, true // pid 1 is the top; it is not read (as before)
	case pid <= 0:
		return nil, false
	}
	for i := 0; i < MaxWalk; i++ {
		in, err := p.Info(pid)
		if err != nil {
			return out, false
		}
		out = append(out, in)
		if in.PPID <= 1 {
			return out, true
		}
		if in.PPID == pid {
			return out, false
		}
		pid = in.PPID
	}
	return out, false
}

// ClaudeCandidates are the ancestors of start that look like Claude Code, nearest first.
func ClaudeCandidates(p Proc, start int) []ProcInfo {
	c, _ := ClaudeCandidatesComplete(p, start)
	return c
}

// ClaudeCandidatesComplete is ClaudeCandidates plus whether the walk was complete; a check that
// clears a process of having a session above it must refuse when it was not.
func ClaudeCandidatesComplete(p Proc, start int) ([]ProcInfo, bool) {
	all, complete := WalkAncestors(p, start)
	var out []ProcInfo
	for _, a := range all {
		if LooksLikeClaude(a) {
			out = append(out, a)
		}
	}
	return out, complete
}

// NearestClaude is the pid of the nearest ancestor of start that looks like Claude Code, or 0.
// It stops at the first one: `wy hook` asks it, and on macOS every step runs ps.
func NearestClaude(p Proc, start int) int {
	pid := start
	for i := 0; i < MaxWalk && pid > 1; i++ {
		in, err := p.Info(pid)
		if err != nil {
			return 0
		}
		if LooksLikeClaude(in) {
			return in.PID
		}
		if in.PPID == pid {
			return 0
		}
		pid = in.PPID
	}
	return 0
}
