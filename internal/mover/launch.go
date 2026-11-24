package mover

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Kind is how a session runs, which decides how it is restarted (setup.md 8.6, Mechanics).
type Kind string

const (
	// KindTmux: an interactive session in a tmux pane: respawn-pane in the same pane.
	KindTmux Kind = "tmux"
	// KindWrapped: an interactive session under `wy run` (or the claude shim): the wrapper
	// starts it again in the same terminal.
	KindWrapped Kind = "wy-run"
	// KindTerminal: an interactive session in a plain terminal without the wrapper: advise
	// only (the steps are printed; nothing is stopped).
	KindTerminal Kind = "terminal"
	// KindBackground: a background session (claude agents, the daemon): resumed with --bg
	// under the new account.
	KindBackground Kind = "background"
	// KindHeadless: claude -p: started again with -p --resume and the same flags, the move
	// note as its prompt.
	KindHeadless Kind = "headless"
	// KindCloud: a claude.ai session driven through --sdk-url: it runs where claude.ai runs
	// it and cannot be moved from here.
	KindCloud Kind = "cloud"
)

// Movable reports whether the mover can restart a session of kind k by itself.
func (k Kind) Movable() bool {
	return k == KindTmux || k == KindWrapped || k == KindBackground || k == KindHeadless
}

// Launch is what the mover knows about a running session: enough to stop it and start it
// again under another account (its launch spec).
type Launch struct {
	PID       int
	ProcStart string // the process's start time, to tell a reused pid
	SessionID string
	Slot      string // its config folder now
	CWD       string
	Name      string
	Kind      Kind
	// Argv is the claude command line (argv[0] dropped).
	Argv []string
	// Pane is its tmux pane (%N) and PaneRoot whether claude is that pane's own process.
	Pane     string
	PaneRoot bool
	// Wrapper is the pid of the `wy run` wrapper above it (0: none).
	Wrapper int
	// RemoteControl: Remote Control was on (the registry has a bridge session id).
	RemoteControl bool
	// Status from the registry: "idle" or "busy"; StatusSince when it last changed.
	Status      string
	StatusSince time.Time
}

// Inputs of Detect: what the registry file, /proc and tmux say about one process.
type DetectInput struct {
	PID         int
	ProcStart   string
	SessionID   string
	Slot        string
	CWD         string
	Name        string
	RegKind     string // the registry's kind: interactive, bg, ...
	Entrypoint  string
	Bridge      bool
	Status      string
	StatusSince time.Time
	Cmdline     []string // /proc/<pid>/cmdline
	// Pane: the tmux pane whose process tree holds PID ("" none); PaneRoot: the pane's own
	// process is PID.
	Pane     string
	PaneRoot bool
	// Wrapper: the pid of a `wy run` wrapper above PID (0: none).
	Wrapper int
}

// Detect builds the launch spec of a session.
func Detect(d DetectInput) Launch {
	l := Launch{PID: d.PID, ProcStart: d.ProcStart, SessionID: d.SessionID, Slot: d.Slot, CWD: d.CWD, Name: d.Name, Pane: d.Pane,
		PaneRoot: d.PaneRoot, Wrapper: d.Wrapper, RemoteControl: d.Bridge, Status: d.Status, StatusSince: d.StatusSince}
	if len(d.Cmdline) > 1 {
		l.Argv = append([]string(nil), d.Cmdline[1:]...)
	}
	switch {
	case hasFlag(l.Argv, "--sdk-url"):
		l.Kind = KindCloud
	case d.RegKind == "bg" || hasFlag(l.Argv, "--bg"):
		l.Kind = KindBackground
	case hasFlag(l.Argv, "-p", "--print"):
		l.Kind = KindHeadless
	case d.Wrapper > 0:
		l.Kind = KindWrapped
	case d.Pane != "":
		l.Kind = KindTmux
	default:
		l.Kind = KindTerminal
	}
	return l
}

func hasFlag(argv []string, names ...string) bool {
	for _, a := range argv {
		if a == "--" {
			return false
		}
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// valueFlags are claude flags that take a value (variadic ones take every value up to the
// next flag); the rest are switches. A flag not listed is taken as a switch.
var valueFlags = map[string]bool{
	"--model": true, "--permission-mode": true, "--settings": true, "--mcp-config": true, "--plugin-dir": true,
	"--add-dir": true, "--allowedTools": true, "--allowed-tools": true, "--disallowedTools": true, "--disallowed-tools": true,
	"--append-system-prompt": true, "--system-prompt": true, "--output-format": true, "--input-format": true,
	"--agents": true, "--agent": true, "--name": true, "-n": true, "--max-turns": true, "--fallback-model": true,
	"--setting-sources": true, "--betas": true, "--permission-prompt-tool": true, "--resume": true, "-r": true,
	"--session-id": true, "--debug-file": true, "--max-budget-usd": true, "--json-schema": true, "--tools": true,
	"--effort": true, "--remote-control-name": true,
}

var variadic = map[string]bool{"--add-dir": true, "--allowedTools": true, "--allowed-tools": true,
	"--disallowedTools": true, "--disallowed-tools": true, "--mcp-config": true, "--betas": true, "--tools": true}

// dropFlags are what a resume replaces: the session it resumes or continues, a fresh id, a
// fork, the background switch (set again by the mode).
var dropFlags = map[string]bool{"--resume": true, "-r": true, "--continue": true, "-c": true, "--session-id": true,
	"--fork-session": true, "--bg": true}

// KeptArgs are the session's flags without what a resume replaces and without its first
// prompt (a positional argument): cwd, model, permission mode, settings, MCP and plugin
// flags all stay.
func KeptArgs(argv []string) []string {
	var out []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			break // what follows is the prompt
		}
		if !strings.HasPrefix(a, "-") {
			continue // a positional: the first prompt
		}
		name, _, inline := strings.Cut(a, "=")
		var vals []string
		if valueFlags[name] && !inline {
			for i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				i++
				vals = append(vals, argv[i])
				if !variadic[name] {
					break
				}
			}
		}
		if (name == "--resume" || name == "-r") && len(vals) == 0 {
			continue
		}
		if dropFlags[name] {
			continue
		}
		out = append(out, a)
		out = append(out, vals...)
	}
	return out
}

// ResumeArgs are the claude arguments that continue l under another account: its kept flags
// and --resume <id>; a background session also --bg and a headless one -p, both with the
// move note as the prompt (they take no SessionStart context before a prompt).
func ResumeArgs(l Launch, note string) []string {
	args := KeptArgs(l.Argv)
	args = append(args, "--resume", l.SessionID)
	switch l.Kind {
	case KindBackground:
		args = append(args, "--bg", note)
	case KindHeadless:
		if !hasFlag(args, "-p", "--print") {
			args = append([]string{"-p"}, args...)
		}
		args = append(args, note)
	}
	return args
}

// Target is the account a session moves to.
type Target struct {
	Key   string
	Label string
	Dir   string // its config folder, absolute
}

var slotNum = regexp.MustCompile(`-(\d+)$`)

// SlotNumber is the N of a folder named ~/.claude-N (1 for ~/.claude).
func SlotNumber(dir string) string {
	if m := slotNum.FindStringSubmatch(filepath.Base(dir)); m != nil {
		return m[1]
	}
	return "1"
}

// Command is the command line that starts claude with args under t: the template
// (account.move_launch) with {dir}, {n} and {label} filled and the arguments appended, else
// `env CLAUDE_CONFIG_DIR=<dir> claude <args>`.
func Command(template string, t Target, args []string) []string {
	if strings.TrimSpace(template) == "" {
		return append([]string{"env", "CLAUDE_CONFIG_DIR=" + t.Dir, "claude"}, args...)
	}
	r := strings.NewReplacer("{dir}", t.Dir, "{n}", SlotNumber(t.Dir), "{label}", t.Label)
	var out []string
	for _, f := range strings.Fields(template) {
		out = append(out, r.Replace(f))
	}
	return append(out, args...)
}

// Shell quotes argv for sh.
func Shell(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r == '-' || r == '_' || r == '.' || r == '/' || r == '=' || r == ':' || r == '~' || r == '%' || r == '@' || r == '+' || r == ',' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
		}) < 0 {
			q[i] = a
			continue
		}
		q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// Note is the one line a moved session gets: as SessionStart context on resume, or as the
// prompt of a background or headless resume.
func Note(from, to string, at time.Time, mv Move) string {
	s := fmt.Sprintf("Watchyard moved this session from %s to %s at %s", from, to, at.Local().Format("15:04"))
	switch {
	case mv.Window != "" && !mv.HitAt.IsZero() && mv.HitAt.After(at):
		s += fmt.Sprintf(" (%s would have hit at %s, before its %s reset)", mv.Window, mv.HitAt.Local().Format("15:04"),
			mv.ResetAt.Local().Format("15:04"))
	case mv.Window != "":
		s += fmt.Sprintf(" (stopped at the %s limit, which resets at %s)", mv.Window, mv.ResetAt.Local().Format("15:04"))
	}
	return s + ". Background shells started before are still running but no longer tracked here: check their output files."
}
