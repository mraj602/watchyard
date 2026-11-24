package mover

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mraj602/watchyard/internal/peers"
)

// A step is one tool call of a session, from its PreToolUse to its PostToolUse (or
// PostToolUseFailure). `wy hook --step` records them in <state>/move/steps/<session>/<tool use
// id>, one JSON line per event, so a mid-turn move can wait for the step in flight to end and
// kill the session between steps (setup.md 8.6, Between steps): killing claude does not end
// the command its tool call runs, and a resumed session would run that step again.

// stepLine is one event of a step.
type stepLine struct {
	E    string    `json:"e"` // "pre", "post", or "moved" (the move ended the process)
	At   time.Time `json:"at"`
	Tool string    `json:"tool,omitempty"`
	Cmd  string    `json:"cmd,omitempty"`
}

// StepStale: a step with no end after this long is not counted (its process died).
const StepStale = 2 * time.Hour

// StepPoll is how often a waiting move looks at the session's steps.
var StepPoll = 100 * time.Millisecond

// RecordStep appends one event of a step ("pre" or "post"). Ids that are not plain names are
// ignored.
func (s State) RecordStep(sessionID, toolUseID, event, tool, cmd string, at time.Time) error {
	if !sidRe.MatchString(sessionID) || !sidRe.MatchString(toolUseID) {
		return nil
	}
	dir := s.path("steps", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(stepLine{E: event, At: at, Tool: tool, Cmd: cmd})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, toolUseID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Step is what the mover knows about a session's tool calls now.
type Step struct {
	// Known: the session's tool calls are recorded (the hooks are installed); without it
	// nothing is known about steps.
	Known bool
	// In: a tool call is in flight (Tool, Cmd: the newest one; Since: when it started).
	In    bool
	Tool  string
	Cmd   string
	Since time.Time
}

// StepNow reads the session's steps at now. Ended steps older than a day are removed.
func (s State) StepNow(sessionID string, now time.Time) Step {
	var st Step
	if !sidRe.MatchString(sessionID) {
		return st
	}
	dir := s.path("steps", sessionID)
	es, err := os.ReadDir(dir)
	if err != nil {
		return st
	}
	st.Known = true
	for _, e := range es {
		p := filepath.Join(dir, e.Name())
		pre, ended, last := readStep(p)
		if ended || pre.At.IsZero() {
			if !last.IsZero() && now.Sub(last) > 24*time.Hour {
				_ = os.Remove(p)
			}
			continue
		}
		if now.Sub(pre.At) > StepStale {
			continue
		}
		if !st.In || pre.At.After(st.Since) {
			st.In, st.Tool, st.Cmd, st.Since = true, pre.Tool, pre.Cmd, pre.At
		}
	}
	return st
}

// readStep reads one step file: its pre line, whether it ended, and its newest event time.
func readStep(path string) (pre stepLine, ended bool, last time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l stepLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.At.After(last) {
			last = l.At
		}
		switch l.E {
		case "pre":
			pre = l
		default:
			ended = true
		}
	}
	return
}

// EndSteps marks every step of the session in flight as ended by a move (its process is
// gone: no PostToolUse will come).
func (s State) EndSteps(sessionID string, now time.Time) {
	if !sidRe.MatchString(sessionID) {
		return
	}
	dir := s.path("steps", sessionID)
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if _, ended, _ := readStep(filepath.Join(dir, e.Name())); !ended {
			_ = s.RecordStep(sessionID, e.Name(), "moved", "", "", now)
		}
	}
}

// WaitStep waits, up to max, until the session is between steps (no tool call in flight),
// polling every StepPoll while alive() holds. It returns the step it last saw and whether
// the session is known to be between steps now (false: unknown, or still in a step).
func (s State) WaitStep(sessionID string, max time.Duration, alive func() bool) (Step, bool) {
	deadline := time.Now().Add(max)
	for {
		st := s.StepNow(sessionID, time.Now())
		if !st.Known {
			return st, false
		}
		if !st.In {
			return st, true
		}
		if !time.Now().Before(deadline) || !alive() {
			return st, false
		}
		time.Sleep(StepPoll)
	}
}

// StepCommand is what a note may say about a tool call: the command, file, URL, pattern or
// description from its tool_input, one line, at most 80 characters; "" when it holds what
// looks like a secret (the peers secret filter, peers.md 9.1).
func StepCommand(toolInput json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(toolInput, &in) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "url", "pattern", "description", "prompt"} {
		v, ok := in[k].(string)
		if !ok || v == "" {
			continue
		}
		if peers.FindSecret(v) != "" {
			return ""
		}
		v = strings.Join(strings.Fields(v), " ")
		if utf8.RuneCountInString(v) > 80 {
			v = string([]rune(v)[:79]) + "…"
		}
		return v
	}
	return ""
}
