package mover

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Host is what a move does to the machine. The real one kills processes and runs tmux;
// tests use a fake that records the calls. Nothing else in the package touches a process.
type Host interface {
	// Alive reports whether l.PID still runs the same process (its start time unchanged).
	Alive(l Launch) bool
	// Kill sends SIGKILL to pid (never a clean exit: that would stop the session's
	// background shells).
	Kill(pid int) error
	// Tmux runs tmux with args.
	Tmux(args ...string) error
	// Start starts argv detached in dir, its output appended to log ("" discards it).
	Start(dir string, argv []string, log string) error
	// Order leaves the relaunch order for the `wy run` wrapper with pid wrapper.
	Order(wrapper int, o RunOrder) error
	// Note leaves the one-line move note for the session's SessionStart hook.
	Note(sessionID, text string) error
	// Carry makes the stopped session l resumable under config folder to: its transcript
	// (CarryTranscript) and, for an interactive one, its folder's trust (CarryTrust).
	Carry(l Launch, to string) error
	// StopJob runs `claude stop <session>` under config folder dir: the background
	// supervisor of that account marks the job stopped (it restarts a job whose process just
	// died) and stops any copy it already restarted.
	StopJob(dir, sessionID string) error
	// Copies are the pids of live processes of the session in config folder dir's registry,
	// other than except.
	Copies(dir, sessionID string, except int) []int
	// TTY is the terminal device of pid ("" none).
	TTY(pid int) string
	// WriteTTY writes text to terminal device tty (only one of this user's).
	WriteTTY(tty, text string) error
}

// Settle is how long a background move watches the old account for a copy its supervisor
// restarted before it resumes the session elsewhere.
var Settle = 5 * time.Second

// AfterWatch is how long a background move goes on watching the old account once the session
// runs on the new one: its supervisor restarted a job killed mid-turn 6 s after the resume
// (e2e v2). Reap covers a copy that comes back later still.
var AfterWatch = 60 * time.Second

// watchOld stops, for AfterWatch, any copy of l that the old account's supervisor restarts.
func watchOld(h Host, l Launch) {
	for deadline := time.Now().Add(AfterWatch); time.Now().Before(deadline); time.Sleep(time.Second) {
		if len(h.Copies(l.Slot, l.SessionID, l.PID)) == 0 {
			continue
		}
		stopCopies(h, l.Slot, l.SessionID, l.PID)
	}
}

// stopCopies stops the copies of a session that an account's supervisor restarted: claude
// stop, then a wait of up to StopWait for them to end. Only a copy that outlives it is
// killed, and the supervisor told again: a killed job is restarted, a stopped one stays
// stopped (e2e v2: a copy killed after claude stop came back and ran two steps again).
func stopCopies(h Host, dir, sid string, except int) {
	_ = h.StopJob(dir, sid)
	for deadline := time.Now().Add(StopWait); len(h.Copies(dir, sid, except)) > 0 && time.Now().Before(deadline); {
		time.Sleep(250 * time.Millisecond)
	}
	if cs := h.Copies(dir, sid, except); len(cs) > 0 {
		for _, pid := range cs {
			_ = h.Kill(pid)
		}
		time.Sleep(500 * time.Millisecond)
		_ = h.StopJob(dir, sid)
	}
}

// RunOrder is what a `wy run` wrapper reads after its claude ended: start this instead of
// exiting.
type RunOrder struct {
	SessionID string   `json:"session_id"`
	ConfigDir string   `json:"config_dir"`
	Argv      []string `json:"argv"` // the full command line (Command)
	// FromDir is the config folder the session ran in: the wrapper carries the transcript
	// from it (CarryTranscript) before it relaunches.
	FromDir string `json:"from_dir,omitempty"`
	// Message: the wrapper prints it in its terminal before it relaunches.
	Message string `json:"message,omitempty"`
}

// ErrNotMovable: the session's kind cannot be restarted by the mover (advise only).
var ErrNotMovable = errors.New("not movable from here")

// Execute moves l to t: it leaves the note, stops claude with SIGKILL and starts it again by
// kind. template is account.move_launch; log is where a headless resume's output goes.
// It returns the command line it started.
func Execute(h Host, l Launch, t Target, template, note, log string) ([]string, error) {
	if !l.Kind.Movable() {
		return nil, fmt.Errorf("%s session: %w", l.Kind, ErrNotMovable)
	}
	if !h.Alive(l) {
		return nil, fmt.Errorf("pid %d is no longer the session", l.PID)
	}
	argv := Command(template, t, ResumeArgs(l, note))
	// What Start runs: claude by its full path when PATH lacks it (the timer's PATH);
	// argv, returned and shown, keeps the plain name.
	run := ClaudeArgv(argv, sessionExe(l.PID))
	switch l.Kind {
	case KindTmux, KindWrapped:
		if l.MidTurn {
			break // the note is its prompt
		}
		if err := h.Note(l.SessionID, note); err != nil {
			return nil, fmt.Errorf("move note: %w", err)
		}
	}
	switch l.Kind {
	case KindTmux:
		// The pane stays when claude dies, then gets the resumed session in place.
		if err := h.Tmux("set-option", "-p", "-t", l.Pane, "remain-on-exit", "on"); err != nil {
			return nil, fmt.Errorf("tmux: %w", err)
		}
		if err := h.Kill(l.PID); err != nil {
			return nil, err
		}
		gone(h, l)
		if err := h.Carry(l, t.Dir); err != nil {
			return argv, fmt.Errorf("transcript: %w (copy it into %s/projects by hand, then: cd %s && %s)", err, t.Dir, Shell([]string{l.CWD}), Shell(argv))
		}
		cmd := Shell(argv)
		if l.Message != "" {
			// The human sees where it went in the same pane: for a few seconds before claude
			// takes the screen (its alternate screen), and again when it ends.
			cmd = "printf '\\n%s\\n\\n' " + Shell([]string{l.Message}) + "; sleep 4; " + cmd
		}
		if err := h.Tmux("respawn-pane", "-k", "-t", l.Pane, "-c", l.CWD, "bash -ic "+Shell([]string{cmd})); err != nil {
			return argv, fmt.Errorf("tmux respawn-pane: %w (start it by hand: cd %s && %s)", err, Shell([]string{l.CWD}), Shell(argv))
		}
		_ = h.Tmux("set-option", "-p", "-u", "-t", l.Pane, "remain-on-exit")
	case KindWrapped:
		if err := h.Order(l.Wrapper, RunOrder{SessionID: l.SessionID, ConfigDir: t.Dir, Argv: argv, FromDir: l.Slot, Message: l.Message}); err != nil {
			return nil, fmt.Errorf("wy run order: %w", err)
		}
		if err := h.Kill(l.PID); err != nil {
			return nil, err
		}
	case KindTerminal:
		// A plain terminal cannot get the session back: it goes on as a background session
		// under the new account, and the terminal gets the message (where it went, how to
		// attach it there).
		tty := h.TTY(l.PID)
		if err := h.Kill(l.PID); err != nil {
			return nil, err
		}
		gone(h, l)
		if err := h.Carry(l, t.Dir); err != nil {
			return argv, fmt.Errorf("transcript: %w (copy it into %s/projects by hand, then: cd %s && %s)", err, t.Dir, Shell([]string{l.CWD}), Shell(argv))
		}
		if err := h.Start(l.CWD, run, ""); err != nil {
			return argv, fmt.Errorf("start: %w (start it by hand: cd %s && %s)", err, Shell([]string{l.CWD}), Shell(argv))
		}
		if tty != "" && l.Message != "" {
			if err := h.WriteTTY(tty, l.Message); err != nil {
				return argv, fmt.Errorf("moved, but its terminal %s was not told: %w", tty, err)
			}
		}
	case KindBackground, KindHeadless:
		if l.Kind == KindBackground {
			// Through the old account's supervisor (claude stop): it restarts a job whose
			// process just died, and keeps the job's background shells running. Resume
			// nowhere while a copy still runs there (setup.md 8.6).
			if err := stopOld(h, l); err != nil {
				return nil, err
			}
		} else {
			if err := h.Kill(l.PID); err != nil {
				return nil, err
			}
			gone(h, l)
		}
		if err := h.Carry(l, t.Dir); err != nil {
			return argv, fmt.Errorf("transcript: %w (copy it into %s/projects by hand, then: cd %s && %s)", err, t.Dir, Shell([]string{l.CWD}), Shell(argv))
		}
		lg := ""
		if l.Kind == KindHeadless {
			lg = log
		}
		if err := h.Start(l.CWD, run, lg); err != nil {
			return argv, fmt.Errorf("start: %w (start it by hand: cd %s && %s)", err, Shell([]string{l.CWD}), Shell(argv))
		}
		if l.Kind == KindBackground {
			watchOld(h, l)
		}
	}
	return argv, nil
}

// StopWait is how long stopOld waits for claude stop to end the session's process before it
// kills it.
var StopWait = 10 * time.Second

// MidTurnStopWait is StopWait for a session stopped mid-turn. claude stop interrupts its turn
// and the process exits on its own: within seconds between steps, after one to three minutes
// inside one (e2e v2). A kill is the last resort: its supervisor restarts a killed job, which
// then redoes steps (e2e v2: one step ran four times).
var MidTurnStopWait = 4 * time.Minute

// stopOld stops a background session on its old account: claude stop there (its background
// shells keep running), SIGKILL only when the process outlives StopWait (MidTurnStopWait
// mid-turn), then for Settle no copy of it may run there (a supervisor that restarted it is
// stopped again, then killed).
func stopOld(h Host, l Launch) error {
	// An error here (an older claude without stop) is not fatal: the kill and the copy check
	// decide.
	_ = h.StopJob(l.Slot, l.SessionID)
	wait := StopWait
	if l.MidTurn {
		wait = MidTurnStopWait
	}
	for deadline := time.Now().Add(wait); h.Alive(l) && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
	}
	if h.Alive(l) {
		// The process outlived claude stop: kill it, then stop the job again, so its
		// supervisor marks it stopped instead of restarting it.
		if err := h.Kill(l.PID); err != nil {
			return err
		}
		gone(h, l)
		_ = h.StopJob(l.Slot, l.SessionID)
	}
	deadline := time.Now().Add(Settle)
	for {
		if cs := h.Copies(l.Slot, l.SessionID, l.PID); len(cs) > 0 {
			stopCopies(h, l.Slot, l.SessionID, l.PID)
			time.Sleep(500 * time.Millisecond)
			if cs := h.Copies(l.Slot, l.SessionID, l.PID); len(cs) > 0 {
				return fmt.Errorf("the old account's supervisor keeps a copy running (pid %d); not resumed elsewhere: stop it with CLAUDE_CONFIG_DIR=%s claude stop %s", cs[0], l.Slot, l.SessionID)
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

// gone waits up to two seconds for the killed session's process to be gone, so nothing
// writes its transcript any more.
func gone(h Host, l Launch) {
	for i := 0; i < 40 && h.Alive(l); i++ {
		time.Sleep(50 * time.Millisecond)
	}
}

// Manual is the steps that move l to t by hand (a plain terminal without the wrapper, or a
// dry run).
func Manual(l Launch, t Target, template, note string) []string {
	argv := Command(template, t, ResumeArgs(l, note))
	switch l.Kind {
	case KindCloud:
		return []string{"This session runs on claude.ai (--sdk-url); it cannot be moved from this machine."}
	case KindTerminal:
		return []string{
			"kill -9 " + strconv.Itoa(l.PID) + " (a clean exit would also stop the session's background shells),",
			"then, as a background session: cd " + Shell([]string{l.CWD}) + " && " + Shell(argv),
			"(its terminal gets a message; sessions started with `wy run claude ...` or the claude shim continue in their terminal instead.)",
		}
	}
	if l.Kind == KindBackground {
		// Through its supervisor: a killed background job is restarted on the old account.
		return []string{"CLAUDE_CONFIG_DIR=" + Shell([]string{l.Slot}) + " claude stop " + l.SessionID,
			"cd " + Shell([]string{l.CWD}) + " && " + Shell(argv)}
	}
	return []string{"kill -9 " + strconv.Itoa(l.PID), "cd " + Shell([]string{l.CWD}) + " && " + Shell(argv)}
}

// ReapWindow is how long after a background move each Smart Account Switch pass still looks
// for a copy of the session on the old account. The old account's supervisor restarted a job
// killed mid-turn after the move's own watch (Settle) had ended (e2e v2), so the watch goes on
// from pass to pass.
var ReapWindow = 30 * time.Minute

// Reap stops copies of background sessions that moved in the last ReapWindow and that the old
// account's supervisor brought back there: claude stop in the old folder, then a kill of any
// copy still running. Only the latest move of each session counts (its from folder is where a
// stray copy would run). It returns the moves whose copy it stopped.
func Reap(h Host, st State, now time.Time) []LogEntry {
	last := map[string]LogEntry{}
	for _, e := range st.Log(now.Add(-ReapWindow)) {
		if e.Kind == KindBackground && e.FromDir != "" && e.FromDir != e.ToDir {
			last[e.SessionID] = e
		} else {
			delete(last, e.SessionID)
		}
	}
	var out []LogEntry
	for _, e := range last {
		if len(h.Copies(e.FromDir, e.SessionID, 0)) == 0 {
			continue
		}
		stopCopies(h, e.FromDir, e.SessionID, 0)
		out = append(out, e)
	}
	return out
}

// JobID is the id `claude stop` takes for session sid: the job's id in `claude agents --json`
// (agents), else the session id's first 8 characters, which is what Claude Code uses.
func JobID(sid string, agents []byte) string {
	var list []struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(agents, &list) == nil {
		for _, a := range list {
			if a.SessionID == sid && a.ID != "" {
				return a.ID
			}
		}
	}
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}
