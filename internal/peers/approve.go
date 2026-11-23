package peers

import (
	"fmt"
	"os"
	"strconv"

	"github.com/mraj602/watchyard/internal/keys"
)

// ProcStat is what the trust checks read from /proc/<pid>/stat: the parent, the session (the
// pid of its leader), the controlling terminal (tty_nr, 0 for none) and the start time.
type ProcStat struct {
	PPID, Session, TTY int
	Start              string
}

// Stat reads /proc/<pid>/stat.
func (l LinuxProc) Stat(pid int) (ProcStat, error) {
	f, err := l.statFields(pid)
	if err != nil {
		return ProcStat{}, err
	}
	var st ProcStat
	for i, p := range []*int{&st.PPID, &st.Session, &st.TTY} {
		n, err := strconv.Atoi(f[[]int{1, 3, 4}[i]])
		if err != nil {
			return ProcStat{}, fmt.Errorf("proc %d: malformed stat", pid)
		}
		*p = n
	}
	st.Start = f[19]
	return st, nil
}

// EnvNames are the names of /proc/<pid>/environ's variables, never a value (keys.EnvNames).
func (l LinuxProc) EnvNames(pid int) ([]string, error) {
	f, err := os.Open(l.path(pid, "environ"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return keys.EnvNames(f)
}

// walkSnap is a walk up the tree with each process's start time read before its other files,
// so stable can tell whether any of them was replaced (a pid reused) while it was read.
type walkSnap struct {
	procs    []ProcInfo
	starts   []string
	complete bool
}

// snapWalk walks up from start as WalkAncestors does (peers.md 2.3).
func snapWalk(p Proc, d ProcDetail, start int) (w walkSnap) {
	pid := start
	switch {
	case pid == 1:
		w.complete = true
		return w
	case pid <= 0:
		return w
	}
	for i := 0; i < MaxWalk; i++ {
		st, err := d.Stat(pid)
		if err != nil {
			return w
		}
		in, err := p.Info(pid)
		if err != nil || in.PPID != st.PPID {
			return w
		}
		w.procs = append(w.procs, in)
		w.starts = append(w.starts, st.Start)
		if in.PPID <= 1 {
			w.complete = true
			return w
		}
		if in.PPID == pid {
			return w
		}
		pid = in.PPID
	}
	return w
}

// stable reports whether every process of the walk is still the one read: the same start time
// and the same parent (a process whose parent ended is re-parented, so a parent pid reused
// meanwhile shows here).
func (w walkSnap) stable(d ProcDetail) bool {
	for i, in := range w.procs {
		st, err := d.Stat(in.PID)
		if err != nil || st.Start != w.starts[i] || st.PPID != in.PPID {
			return false
		}
	}
	return true
}

// claude is the first process of the walk that looks like Claude Code, or nil.
func (w walkSnap) claude() *ProcInfo {
	for i := range w.procs {
		if LooksLikeClaude(w.procs[i]) {
			return &w.procs[i]
		}
	}
	return nil
}

// claudeEnvName is the first signing-guard marker among pid's environment names; err when the
// environment cannot be read.
func claudeEnvName(d ProcDetail, pid int) (string, error) {
	names, err := d.EnvNames(pid)
	if err != nil {
		return "", err
	}
	for _, n := range names {
		if keys.IsClaudeEnvName(n) {
			return n, nil
		}
	}
	return "", nil
}

// CheckApprover is the agent's guard for `wy peers trust doorbell` and `--off` (peers.md 4.1a
// step 2), from the requester's pid (the socket's peer credentials) and /proc only: a
// controlling terminal, no signing-guard marker name in its environment, no Claude Code
// process above it or above its terminal's session leader (both walks complete), and none of
// these processes replaced while they were read. alive, when not nil, reports whether the
// requester is still the process that connected (its pidfd). The trusttest build skips it.
func CheckApprover(p Proc, pid int, alive func() bool) *Refusal {
	no := func(format string, a ...any) *Refusal {
		return Refuse(CodeNotASession, "Doorbell is approved or revoked only by the owner at a terminal, outside any Claude Code session: "+format, a...)
	}
	d, ok := p.(ProcDetail)
	if !ok || p == nil {
		return no("this OS is not supported")
	}
	st, err := d.Stat(pid)
	if err != nil {
		return no("this command's process cannot be read")
	}
	if st.TTY == 0 {
		return no("this command has no controlling terminal")
	}
	if st.Session <= 0 {
		return no("this command's terminal has no session")
	}
	check := func(who string, pid int) (walkSnap, *Refusal) {
		if n, err := claudeEnvName(d, pid); err != nil {
			return walkSnap{}, no("the environment of %s (pid %d) cannot be read", who, pid)
		} else if n != "" {
			return walkSnap{}, no("%s (pid %d) has %s in its environment", who, pid, n)
		}
		w := snapWalk(p, d, pid)
		if c := w.claude(); c != nil {
			return w, no("a Claude Code process (pid %d) is above %s", c.PID, who)
		}
		if !w.complete {
			return w, no("the processes above %s cannot all be read, so a session above it cannot be ruled out", who)
		}
		return w, nil
	}
	self, r := check("this command", pid)
	if r != nil {
		return r
	}
	if self.starts == nil || self.starts[0] != st.Start {
		return no("this command's process changed while it was checked")
	}
	lead, err := d.Stat(st.Session)
	if err != nil {
		return no("the session leader of its terminal (pid %d) cannot be read", st.Session)
	}
	if lead.TTY != st.TTY {
		return no("the session leader (pid %d) is not on this command's terminal", st.Session)
	}
	leader, r := check("its terminal's session leader", st.Session)
	if r != nil {
		return r
	}
	if !self.stable(d) || !leader.stable(d) {
		return no("a process changed while it was checked; try again")
	}
	if alive != nil && !alive() {
		return no("this command's process ended while it was checked")
	}
	return nil
}
