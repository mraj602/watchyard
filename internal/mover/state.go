package mover

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// State is the mover's files under <state>/move (setup.md 8.6, Files):
//
//	notes/<session>.json   the move note the SessionStart hook prints once on resume
//	runs/<pid>.json        a running `wy run` wrapper (by the wrapper's pid)
//	orders/<pid>.json      a relaunch order for that wrapper
//	pins.json              sessions the owner pinned to their account (no moves)
//	moves.jsonl            every move (the cooldown, no ping-pong, wy status)
type State struct{ Dir string }

// NewState is the mover state under the Watchyard state folder.
func NewState(stateDir string) State { return State{Dir: filepath.Join(stateDir, "move")} }

var sidRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (s State) path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir}, parts...)...)
}

func (s State) write(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

type noteFile struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// WriteNote leaves text for the session's next SessionStart.
func (s State) WriteNote(sessionID, text string, at time.Time) error {
	if !sidRe.MatchString(sessionID) {
		return fmt.Errorf("bad session id %q", sessionID)
	}
	return s.write(s.path("notes", sessionID+".json"), noteFile{Text: text, At: at})
}

// NoteTTL: a note not taken within it is dropped (the resume never happened).
const NoteTTL = time.Hour

// TakeNote returns the session's move note and removes it ("" when none or too old).
func (s State) TakeNote(sessionID string, now time.Time) string {
	if !sidRe.MatchString(sessionID) {
		return ""
	}
	p := s.path("notes", sessionID+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	_ = os.Remove(p)
	var n noteFile
	if json.Unmarshal(b, &n) != nil || now.Sub(n.At) > NoteTTL {
		return ""
	}
	return n.Text
}

// RunInfo is what a `wy run` wrapper records about itself and the command it runs.
type RunInfo struct {
	Wrapper int       `json:"wrapper"` // the wrapper's pid
	Child   int       `json:"child"`
	Argv    []string  `json:"argv"`
	Dir     string    `json:"dir"`
	At      time.Time `json:"at"`
}

// WriteRun records a running wrapper.
func (s State) WriteRun(r RunInfo) error {
	return s.write(s.path("runs", strconv.Itoa(r.Wrapper)+".json"), r)
}

// RemoveRun forgets the wrapper with pid wrapper.
func (s State) RemoveRun(wrapper int) { _ = os.Remove(s.path("runs", strconv.Itoa(wrapper)+".json")) }

// Run returns the run of the wrapper with pid wrapper, if one runs.
func (s State) Run(wrapper int) (RunInfo, bool) {
	var r RunInfo
	b, err := os.ReadFile(s.path("runs", strconv.Itoa(wrapper)+".json"))
	if err != nil || json.Unmarshal(b, &r) != nil || r.Wrapper != wrapper {
		return RunInfo{}, false
	}
	return r, true
}

// WriteOrder leaves a relaunch order for the wrapper with pid pid.
func (s State) WriteOrder(pid int, o RunOrder) error {
	return s.write(s.path("orders", strconv.Itoa(pid)+".json"), o)
}

// TakeOrder returns and removes the relaunch order for the wrapper with pid pid.
func (s State) TakeOrder(pid int) (RunOrder, bool) {
	p := s.path("orders", strconv.Itoa(pid)+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		return RunOrder{}, false
	}
	_ = os.Remove(p)
	var o RunOrder
	if json.Unmarshal(b, &o) != nil || len(o.Argv) == 0 {
		return RunOrder{}, false
	}
	return o, true
}

// Pins are the sessions pinned to their account.
func (s State) Pins() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(s.path("pins.json"))
	if err != nil {
		return out
	}
	var ids []string
	if json.Unmarshal(b, &ids) == nil {
		for _, id := range ids {
			out[id] = true
		}
	}
	return out
}

// SetPin pins (on) or unpins a session.
func (s State) SetPin(sessionID string, on bool) error {
	ps := s.Pins()
	if on {
		ps[sessionID] = true
	} else {
		delete(ps, sessionID)
	}
	ids := make([]string, 0, len(ps))
	for id := range ps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return s.write(s.path("pins.json"), ids)
}

// LogEntry is one line of moves.jsonl.
type LogEntry struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"session_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	FromDir   string    `json:"from_dir,omitempty"`
	ToDir     string    `json:"to_dir,omitempty"`
	Kind      Kind      `json:"kind"`
	Auto      bool      `json:"auto,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Argv      []string  `json:"argv,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Append adds a move to the log.
func (s State) Append(e LogEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path("moves.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// Log returns the moves since since, oldest first (failed ones left out).
func (s State) Log(since time.Time) []LogEntry {
	f, err := os.Open(s.path("moves.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e LogEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Error == "" && !e.At.Before(since) {
			out = append(out, e)
		}
	}
	return out
}

// Chain is the session's moves since since as a path of slot folders, oldest first: the
// folder it started in, then each one it moved to (nil when it never moved).
func (s State) Chain(sessionID string, since time.Time) []string {
	var out []string
	for _, e := range s.Log(since) {
		if e.SessionID != sessionID {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != e.FromDir {
			out = append(out, e.FromDir)
		}
		out = append(out, e.ToDir)
	}
	return out
}

// PastSlots are the slot folders the session left by a move since since, newest first,
// without repeats.
func (s State) PastSlots(sessionID string, since time.Time) []string {
	c := s.Chain(sessionID, since)
	var out []string
	seen := map[string]bool{}
	for i := len(c) - 2; i >= 0; i-- {
		if c[i] != "" && !seen[c[i]] {
			seen[c[i]] = true
			out = append(out, c[i])
		}
	}
	return out
}

// Records are the log's moves as Plan's Recent.
func Records(es []LogEntry) []Record {
	out := make([]Record, 0, len(es))
	for _, e := range es {
		out = append(out, Record{Session: e.SessionID, From: e.From, To: e.To, At: e.At})
	}
	return out
}

// SwitchState is what one auto pass leaves for the next (<state>/move/switch.json): when it
// ran and the spike filter's streaks.
type SwitchState struct {
	At     time.Time      `json:"at"`
	Streak map[string]int `json:"streak"`
}

// LoadSwitch reads the switch state at now. Streaks older than three passes are dropped (the
// passes were not consecutive).
func (s State) LoadSwitch(now time.Time, pass time.Duration) SwitchState {
	var x SwitchState
	if b, err := os.ReadFile(s.path("switch.json")); err == nil {
		_ = json.Unmarshal(b, &x)
	}
	if x.Streak == nil || now.Sub(x.At) > 3*max(pass, time.Minute) {
		x.Streak = map[string]int{}
	}
	return x
}

// SaveSwitch writes the switch state.
func (s State) SaveSwitch(x SwitchState) error { return s.write(s.path("switch.json"), x) }
