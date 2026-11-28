// Package grokpeer makes Grok Build sessions peers (docs/grok.md): it keeps a record per
// Grok session Watchyard has seen, a per-session inbox, and a drain that resumes the session
// headlessly with the queued messages. It implements peers.AgentHost.
//
// Mechanism: `grok -r <session> -p <text>` (resume with a prompt). A session is busy while a
// process holds its lock file (a `wy grok run --resume` or a drain); a message to a busy
// session is queued in its inbox and the holder picks it up when its turn ends; a message to
// an idle session starts a detached `wy grok drain`. Nothing here reads ~/.grok.
package grokpeer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/grokcli"
	"github.com/mraj602/watchyard/internal/peers"
)

const (
	// Account and Label name Grok in the matrix and in handles (name@grok.machine).
	Account = "grok"
	Label   = "grok"
	// Retention: a session that has not run for this long drops out of peers list.
	Retention = 7 * 24 * time.Hour
	maxInbox  = 64
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// Record is `<state>/grok/sessions/<id>.json`. It holds no prompt or answer text.
type Record struct {
	ID       string    `json:"id"`
	Name     string    `json:"name,omitempty"`
	Dir      string    `json:"dir"`
	Model    string    `json:"model,omitempty"`
	Effort   string    `json:"effort,omitempty"`
	Yolo     bool      `json:"yolo,omitempty"`
	Added    time.Time `json:"added"`
	LastRun  time.Time `json:"last_run"`
	Turns    int       `json:"turns"`
	Tokens   int       `json:"tokens"`
	LastFail string    `json:"last_fail,omitempty"`
}

// Runner runs one headless resume; tests replace it.
type Runner func(ctx context.Context, o grokcli.Options) (*grokcli.Result, error)

// Host is the peers.AgentHost for Grok.
type Host struct {
	State string
	Now   func() time.Time
	// Spawn starts `wy grok drain <id>` detached; nil uses the current executable.
	Spawn func(id string) error
	// Run is the headless runner (default grokcli.Run with the located binary).
	Run Runner
}

var _ peers.AgentHost = (*Host)(nil)

func (h *Host) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Host) dir() string { return filepath.Join(h.State, "grok") }

func (h *Host) recPath(id string) string { return filepath.Join(h.dir(), "sessions", id+".json") }
func (h *Host) lockPath(id string) string {
	return filepath.Join(h.dir(), "sessions", id+".lock")
}
func (h *Host) inboxDir(id string) string { return filepath.Join(h.dir(), "inbox", id) }

// Load reads one record.
func (h *Host) Load(id string) (Record, bool) {
	if !safeID.MatchString(id) {
		return Record{}, false
	}
	b, err := os.ReadFile(h.recPath(id))
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.ID != id {
		return Record{}, false
	}
	return r, true
}

// Save writes a record atomically.
func (h *Host) Save(r Record) error {
	if !safeID.MatchString(r.ID) {
		return fmt.Errorf("grok session id %q is not a safe file name", r.ID)
	}
	if err := os.MkdirAll(filepath.Dir(h.recPath(r.ID)), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicfile.Write(h.recPath(r.ID), b, 0o600)
}

// Forget removes a session's record and inbox.
func (h *Host) Forget(id string) error {
	if !safeID.MatchString(id) {
		return fmt.Errorf("bad session id")
	}
	_ = os.RemoveAll(h.inboxDir(id))
	if err := os.Remove(h.recPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Records lists every record, newest run first.
func (h *Host) Records() []Record {
	ents, _ := os.ReadDir(filepath.Join(h.dir(), "sessions"))
	var out []Record
	for _, d := range ents {
		id, ok := strings.CutSuffix(d.Name(), ".json")
		if !ok {
			continue
		}
		if r, ok := h.Load(id); ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastRun.After(out[j].LastRun) })
	return out
}

// Busy reports whether a process holds the session's lock.
func (h *Host) Busy(id string) bool {
	l, err := tryLock(h.lockPath(id))
	if err != nil {
		return true
	}
	l.Unlock()
	return false
}

// Sessions implements peers.AgentHost: records that ran within Retention.
func (h *Host) Sessions() []peers.HostedSession {
	var out []peers.HostedSession
	for _, r := range h.Records() {
		if h.now().Sub(r.LastRun) > Retention {
			continue
		}
		st := "idle"
		if h.Busy(r.ID) {
			st = "busy"
		}
		name := r.Name
		if name == "" {
			name = "grok-" + r.ID[:min(8, len(r.ID))]
		}
		out = append(out, peers.HostedSession{ID: r.ID, Name: name, Dir: r.Dir, Status: st, Account: Account,
			Label: Label, Bypass: r.Yolo, Last: r.LastRun})
	}
	return out
}

// Deliver implements peers.AgentHost: write the message to the inbox, then start a drain
// unless one (or a run of the session) holds the lock already.
func (h *Host) Deliver(id, text string) (bool, error) {
	if _, ok := h.Load(id); !ok {
		return false, peers.ErrTargetGone
	}
	if err := h.enqueue(id, text); err != nil {
		return false, err
	}
	if h.Busy(id) {
		return true, nil
	}
	if err := h.spawn(id); err != nil {
		return false, fmt.Errorf("message queued but the drain did not start: %w", err)
	}
	return false, nil
}

func (h *Host) enqueue(id, text string) error {
	d := h.inboxDir(id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	if ents, _ := os.ReadDir(d); len(ents) >= maxInbox {
		return errors.New("that Grok session's inbox is full")
	}
	name := fmt.Sprintf("%020d-%d.msg", h.now().UnixNano(), os.Getpid())
	return atomicfile.Write(filepath.Join(d, name), []byte(text), 0o600)
}

// Pending is the number of queued messages.
func (h *Host) Pending(id string) int {
	ents, _ := os.ReadDir(h.inboxDir(id))
	n := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".msg") {
			n++
		}
	}
	return n
}

// popAll removes and returns the queued messages, oldest first.
func (h *Host) popAll(id string) []string {
	d := h.inboxDir(id)
	ents, _ := os.ReadDir(d)
	var out []string
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".msg") {
			continue
		}
		p := filepath.Join(d, e.Name())
		if b, err := os.ReadFile(p); err == nil {
			out = append(out, string(b))
		}
		_ = os.Remove(p)
	}
	return out
}

// Drain resumes the session with its queued messages until the inbox is empty. It returns
// at once (nil) when another process holds the session's lock: that holder drains.
func (h *Host) Drain(ctx context.Context, id string) error {
	for {
		l, err := tryLock(h.lockPath(id))
		if err != nil {
			return nil
		}
		err = h.drainLocked(ctx, id)
		l.Unlock()
		if err != nil || h.Pending(id) == 0 {
			return err
		}
	}
}

func (h *Host) drainLocked(ctx context.Context, id string) error {
	rec, ok := h.Load(id)
	if !ok {
		return peers.ErrTargetGone
	}
	for {
		msgs := h.popAll(id)
		if len(msgs) == 0 {
			return nil
		}
		res, err := h.run(ctx, grokcli.Options{Prompt: strings.Join(msgs, "\n\n"), Dir: rec.Dir, Model: rec.Model,
			Effort: rec.Effort, Yolo: rec.Yolo, SessionID: id})
		if err != nil {
			rec.LastFail = h.now().UTC().Format(time.RFC3339)
			_ = h.Save(rec)
			return err
		}
		h.Account(&rec, res)
		_ = h.Save(rec)
	}
}

// Account adds one run's usage to the record.
func (h *Host) Account(r *Record, res *grokcli.Result) {
	r.LastRun, r.LastFail = h.now().UTC(), ""
	r.Turns += res.NumTurns
	r.Tokens += res.Tokens
}

func (h *Host) run(ctx context.Context, o grokcli.Options) (*grokcli.Result, error) {
	if h.Run != nil {
		return h.Run(ctx, o)
	}
	home, _ := os.UserHomeDir()
	bin, err := grokcli.Locate(home)
	if err != nil {
		return nil, err
	}
	return grokcli.Run(ctx, bin, nil, o)
}

func (h *Host) spawn(id string) error {
	if h.Spawn != nil {
		return h.Spawn(id)
	}
	return spawnDetached(id)
}

// Hold takes the session's lock for a foreground run (`wy grok run --resume`); the caller
// calls Release when the turn ends, which then drains what arrived meanwhile.
func (h *Host) Hold(id string) (*Lock, error) {
	if !safeID.MatchString(id) {
		return nil, fmt.Errorf("bad session id")
	}
	return tryLock(h.lockPath(id))
}

// Register records a session after a run (new or resumed) and returns the record.
func (h *Host) Register(id, dir, name string, o grokcli.Options, res *grokcli.Result) (Record, error) {
	r, ok := h.Load(id)
	if !ok {
		r = Record{ID: id, Added: h.now().UTC()}
	}
	if dir != "" {
		r.Dir = dir
	}
	if r.Dir == "" {
		r.Dir, _ = os.Getwd()
	}
	if name != "" {
		r.Name = name
	}
	if o.Model != "" {
		r.Model = o.Model
	}
	if o.Effort != "" {
		r.Effort = o.Effort
	}
	r.Yolo = r.Yolo || o.Yolo
	if res != nil {
		h.Account(&r, res)
	} else if r.LastRun.IsZero() {
		r.LastRun = h.now().UTC()
	}
	return r, h.Save(r)
}
