package mover

import (
	"errors"
	"fmt"
	"strconv"
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
}

// RunOrder is what a `wy run` wrapper reads after its claude ended: start this instead of
// exiting.
type RunOrder struct {
	SessionID string   `json:"session_id"`
	ConfigDir string   `json:"config_dir"`
	Argv      []string `json:"argv"` // the full command line (Command)
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
	switch l.Kind {
	case KindTmux, KindWrapped:
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
		if err := h.Tmux("respawn-pane", "-k", "-t", l.Pane, "-c", l.CWD, "bash -ic "+Shell([]string{Shell(argv)})); err != nil {
			return argv, fmt.Errorf("tmux respawn-pane: %w (start it by hand: cd %s && %s)", err, Shell([]string{l.CWD}), Shell(argv))
		}
		_ = h.Tmux("set-option", "-p", "-u", "-t", l.Pane, "remain-on-exit")
	case KindWrapped:
		if err := h.Order(l.Wrapper, RunOrder{SessionID: l.SessionID, ConfigDir: t.Dir, Argv: argv}); err != nil {
			return nil, fmt.Errorf("wy run order: %w", err)
		}
		if err := h.Kill(l.PID); err != nil {
			return nil, err
		}
	case KindBackground, KindHeadless:
		if err := h.Kill(l.PID); err != nil {
			return nil, err
		}
		lg := ""
		if l.Kind == KindHeadless {
			lg = log
		}
		if err := h.Start(l.CWD, argv, lg); err != nil {
			return argv, fmt.Errorf("start: %w (start it by hand: cd %s && %s)", err, Shell([]string{l.CWD}), Shell(argv))
		}
	}
	return argv, nil
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
			"From another terminal: kill -9 " + strconv.Itoa(l.PID) + " (a clean exit would also stop the session's background shells),",
			"then in the session's terminal: cd " + Shell([]string{l.CWD}) + " && " + Shell(argv),
			"(Start sessions with `wy run claude ...` or the claude shim to let Watchyard do this.)",
		}
	}
	return []string{"kill -9 " + strconv.Itoa(l.PID), "cd " + Shell([]string{l.CWD}) + " && " + Shell(argv)}
}
