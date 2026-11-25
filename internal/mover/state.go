package mover

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
//	alerts.json            pinned sessions that alone take their account to 100 (wy status)
//	notify.json            moves waiting for the next push, and when the last one went (8.6, Notices)
//	digest.stamp           the day the daily digest was last sent
//	passes.jsonl           one line per auto pass: each account's projection (the digest)
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

// Pins are the sessions in wy's old pins file (the retired wy move --pin-account): no longer
// pins (setup.md 8.6, Pinned sessions); read only to say they are ignored.
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

// Changed records text under name (<state>/move/<name>) and says whether it differs from the
// text recorded before: a warning goes to the switch's log once, not every pass.
func (s State) Changed(name, text string) bool {
	p := s.path(name)
	if b, err := os.ReadFile(p); err == nil && string(b) == text {
		return false
	} else if err != nil && text == "" {
		return false
	}
	if err := os.MkdirAll(s.Dir, 0o700); err == nil {
		_ = os.WriteFile(p, []byte(text), 0o600)
	}
	return true
}

// HubSent is <state>/move/hub_sent.json: the high-water mark of the moves sent to the hub as
// switch_move records (setup.md 8.6, Switch log). No file: nothing was back-filled yet.
type HubSent struct {
	At time.Time `json:"at"`
}

// HubSentCursor returns the mark; ok is false before the first back-fill.
func (s State) HubSentCursor() (time.Time, bool) {
	b, err := os.ReadFile(s.path("hub_sent.json"))
	if err != nil {
		return time.Time{}, false
	}
	var h HubSent
	if json.Unmarshal(b, &h) != nil {
		return time.Time{}, false
	}
	return h.At, true
}

// SaveHubSentCursor moves the mark forward (never back).
func (s State) SaveHubSentCursor(at time.Time) error {
	if cur, ok := s.HubSentCursor(); ok && !at.After(cur) {
		return nil
	}
	return s.write(s.path("hub_sent.json"), HubSent{At: at.UTC()})
}

// LogEntry is one line of moves.jsonl.
type LogEntry struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"session_id"`
	Name      string    `json:"name,omitempty"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	FromDir   string    `json:"from_dir,omitempty"`
	ToDir     string    `json:"to_dir,omitempty"`
	Kind      Kind      `json:"kind"`
	Auto      bool      `json:"auto,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	// Rate is the session's burn when it moved (5-hour points per hour; the destination's
	// moved-burn limit counts it). RateKnown: recorded (a rate of 0 is known too).
	Rate      float64  `json:"rate,omitempty"`
	RateKnown bool     `json:"rate_known,omitempty"`
	Argv      []string `json:"argv,omitempty"`
	Error     string   `json:"error,omitempty"`
	// Recheck: the re-check after its step wait changed its destination (Move.Recheck).
	Recheck string `json:"recheck,omitempty"`
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
	var out []LogEntry
	for _, e := range s.LogAll(since) {
		if e.Error == "" {
			out = append(out, e)
		}
	}
	return out
}

// LogAll returns every line since since, oldest first, the failed moves (Error set) too.
func (s State) LogAll(since time.Time) []LogEntry {
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
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.At.Before(since) {
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
		out = append(out, Record{Session: e.SessionID, From: e.From, To: e.To, At: e.At, Rate: e.Rate, RateKnown: e.Rate > 0 || e.RateKnown})
	}
	return out
}

// SwitchState is what one auto pass leaves for the next (<state>/move/switch.json): when it
// ran and the spike filter's streaks.
type SwitchState struct {
	At     time.Time      `json:"at"`
	Streak map[string]int `json:"streak"`
	// ReadStreak: per account, on how many passes in a row its readings rose over twice its
	// meter (the readings are used from the second).
	ReadStreak map[string]int `json:"read_streak,omitempty"`
}

// LoadSwitch reads the switch state at now. Streaks older than three passes are dropped (the
// passes were not consecutive).
func (s State) LoadSwitch(now time.Time, pass time.Duration) SwitchState {
	var x SwitchState
	if b, err := os.ReadFile(s.path("switch.json")); err == nil {
		_ = json.Unmarshal(b, &x)
	}
	if x.Streak == nil || now.Sub(x.At) > 3*max(pass, time.Minute) {
		x.Streak, x.ReadStreak = map[string]int{}, nil
	}
	if x.ReadStreak == nil {
		x.ReadStreak = map[string]int{}
	}
	return x
}

// SaveSwitch writes the switch state.
func (s State) SaveSwitch(x SwitchState) error { return s.write(s.path("switch.json"), x) }

// PinAlert is an owner alert kept for `wy status` (<state>/move/alerts.json): a pinned
// session that alone takes its account to 100 before the window resets (setup.md 8.6, Pinned
// sessions). Command is the one line that moves it by hand ("" when no account has room).
type PinAlert struct {
	At      time.Time `json:"at"`
	Session string    `json:"session"`
	Name    string    `json:"name,omitempty"`
	Account string    `json:"account"` // its label
	Window  string    `json:"window"`
	HitAt   time.Time `json:"hit_at"`
	ResetAt time.Time `json:"reset_at"`
	Rate    float64   `json:"rate"`
	Command string    `json:"command"`
}

// Text is the alert's one line.
func (a PinAlert) Text() string {
	name := a.Session
	if len(name) > 8 {
		name = name[:8]
	}
	if a.Name != "" {
		name += " " + a.Name
	}
	t := fmt.Sprintf("pinned session %s alone takes %s's %s window to 100%% at %s (%.1f points/h; it resets %s)", name, a.Account, a.Window,
		Clock(a.HitAt, a.At), a.Rate, Clock(a.ResetAt, a.At))
	if !a.HitAt.After(a.At) { // already there: no hit time to give
		t = fmt.Sprintf("pinned session %s is on %s with its %s window at 100%% (%.1f points/h; it resets %s)", name, a.Account, a.Window,
			a.Rate, Clock(a.ResetAt, a.At))
	}
	if a.Command != "" {
		return t + ": move it by hand: " + a.Command
	}
	return t + "; no account has room for it now"
}

// Alerts are the pin alerts whose window has not reset by now.
func (s State) Alerts(now time.Time) []PinAlert {
	var xs, out []PinAlert
	if b, err := os.ReadFile(s.path("alerts.json")); err == nil {
		_ = json.Unmarshal(b, &xs)
	}
	for _, x := range xs {
		if x.ResetAt.After(now) {
			out = append(out, x)
		}
	}
	return out
}

// AddAlert keeps a once for its session and window (until that window resets) and reports
// whether it is new.
func (s State) AddAlert(a PinAlert, now time.Time) (bool, error) {
	xs := s.Alerts(now)
	for _, x := range xs {
		if x.Session == a.Session && x.Window == a.Window && x.ResetAt.Sub(a.ResetAt).Abs() < 10*time.Minute {
			return false, nil
		}
	}
	return true, s.write(s.path("alerts.json"), append(xs, a))
}

// RecheckEntry is one re-check after a step wait that changed a planned move (setup.md 8.6,
// Re-check): To "" dropped it.
type RecheckEntry struct {
	At        time.Time `json:"at"`
	SessionID string    `json:"session_id"`
	Name      string    `json:"name,omitempty"`
	From      string    `json:"from"`
	Planned   string    `json:"planned"`
	To        string    `json:"to,omitempty"`
	Why       string    `json:"why"`
}

const rechecksFile = "rechecks.jsonl"

// AppendRecheck records a re-check that changed a move; the file keeps its last 200 lines.
func (s State) AppendRecheck(e RecheckEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	p := s.path(rechecksFile)
	old, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSpace(string(old)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	lines = append(lines, string(b))
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	return os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// Rechecks are the re-checks since since, oldest first.
func (s State) Rechecks(since time.Time) []RecheckEntry {
	b, err := os.ReadFile(s.path(rechecksFile))
	if err != nil {
		return nil
	}
	var out []RecheckEntry
	for _, l := range strings.Split(string(b), "\n") {
		var e RecheckEntry
		if json.Unmarshal([]byte(l), &e) == nil && !e.At.Before(since) {
			out = append(out, e)
		}
	}
	return out
}

// StaleAlertDue returns the keys not alerted about within every (<state>/move/stale-alerts.json)
// and records them as alerted at now: a stale-data alert at most once per account per every.
func (s State) StaleAlertDue(keys []string, now time.Time, every time.Duration) []string {
	p := s.path("stale-alerts.json")
	last := map[string]time.Time{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	var due []string
	for _, k := range keys {
		if t, ok := last[k]; ok && now.Sub(t) < every && !t.After(now) {
			continue
		}
		due = append(due, k)
		last[k] = now
	}
	for k, t := range last {
		if now.Sub(t) > 7*24*time.Hour {
			delete(last, k)
		}
	}
	if len(due) > 0 {
		_ = s.write(p, last)
	}
	return due
}
