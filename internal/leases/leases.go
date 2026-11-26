// Package leases is the ledger of `wy account next`: one lease per pick, kept in append-only
// day files under <state>/next/leases (setup.md 8.5, "Leases and the ledger").
//
// Each line is one JSON object written with a single write and flushed to disk before the
// next. `wy account next` reads, ranks and appends under the lock <state>/next/leases.lock;
// `wy hook` appends `launched` and `ended` lines without the lock (Mark).
//
// Depends on: nothing outside the standard library and golang.org/x/sys.
package leases

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Line ops.
const (
	OpLease    = "lease"
	OpLaunched = "launched"
	OpEnded    = "ended"
	// OpBind gives the lease with ID its session id after the launch: claude --bg ignores
	// --session-id and its supervisor names the session (setup.md 8.5).
	OpBind = "bind"
)

// Days is how many UTC day files are read (today and the 7 before it); older files are
// deleted when a lease is written.
const Days = 8

// Unlaunched is how long a lease without a `launched` line counts.
const Unlaunched = 10 * time.Minute

// Kinds of lane.
const (
	KindAggressive = "aggressive"
	KindProtected  = "protected"
)

// Line is one line of a day file. Only the fields of its op are set.
type Line struct {
	V         int       `json:"v"`
	Op        string    `json:"op"`
	ID        string    `json:"id,omitempty"`
	At        time.Time `json:"at"`
	Key       string    `json:"key,omitempty"`
	Account   string    `json:"account,omitempty"`
	Org       string    `json:"org,omitempty"`
	Folder    string    `json:"folder,omitempty"`
	Label     string    `json:"label,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	C5        *float64  `json:"c5,omitempty"`
	CF        *float64  `json:"cf,omitempty"`
	Model     string    `json:"model,omitempty"`
	Strategy  string    `json:"strategy,omitempty"`
	Urgent    bool      `json:"urgent,omitempty"`
	Batch     string    `json:"batch,omitempty"`
	// Drain: picked by the lanes drain rules, to finish by FinishBy (setup.md 8.5, Drain).
	Drain    bool       `json:"drain,omitempty"`
	FinishBy *time.Time `json:"finish_by,omitempty"`
	// MovedFrom: the session moved here from the account with this label (setup.md 8.6).
	MovedFrom string `json:"moved_from,omitempty"`
	// Expect: the lane's size class as the picker counted it (short, medium, long; "medium"
	// also without --expect) and ExpectMin its expected minutes; ExpectEnd when it is expected
	// to end (the pick + ExpectMin; a move lease keeps its session's). The switch projects the
	// session's burn to ExpectEnd while it runs inside it (setup.md 8.6). Absent on older lines.
	Expect    string     `json:"expect,omitempty"`
	ExpectMin float64    `json:"expect_min,omitempty"`
	ExpectEnd *time.Time `json:"expect_end,omitempty"`
	// Parent is the session that asked for the lease (its id), when it was asked from inside
	// a session: Watchyard tells it when the lane moves (setup.md 8.6).
	Parent string `json:"parent,omitempty"`
}

// Lease is a lease line with the times of its `launched` and `ended` lines (zero when absent).
type Lease struct {
	Line
	Launched time.Time
	Ended    time.Time
}

// Dropped reports whether the lease no longer counts at now: it was never launched and is
// older than Unlaunched.
func (l Lease) Dropped(now time.Time) bool {
	return l.Launched.IsZero() && now.Sub(l.At) > Unlaunched
}

// Ledger is the ledger kept in Dir (<state>/next): day files in Dir/leases, the lock
// Dir/leases.lock.
type Ledger struct{ Dir string }

func (g Ledger) days() string { return filepath.Join(g.Dir, "leases") }

func dayName(t time.Time) string { return t.UTC().Format("2006-01-02") + ".jsonl" }

// Lock takes the ledger lock (blocking) and returns the unlock function.
func (g Ledger) Lock() (func(), error) {
	if err := os.MkdirAll(g.Dir, 0o700); err != nil {
		return nil, err
	}
	unlock, err := lock(filepath.Join(g.Dir, "leases.lock"))
	if err != nil {
		return nil, fmt.Errorf("lock the lease ledger: %w", err)
	}
	return unlock, nil
}

// Read returns the leases of the last Days day files, oldest first, with the times of their
// `launched` and `ended` lines, and the number of lines it could not read. A missing folder
// is an empty ledger. Read takes no lock; callers that append after reading hold Lock.
func (g Ledger) Read(now time.Time) ([]Lease, int, error) {
	var out []Lease
	skipped := 0
	latest := map[string]int{} // session id -> index of its newest lease so far
	byID := map[string]int{}   // lease id -> index
	for i := Days - 1; i >= 0; i-- {
		name := filepath.Join(g.days(), dayName(now.AddDate(0, 0, -i)))
		b, err := os.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, skipped, err
		}
		s := scan(b, func(l Line) {
			switch l.Op {
			case OpLease:
				out = append(out, Lease{Line: l})
				if l.SessionID != "" {
					latest[l.SessionID] = len(out) - 1
				}
				if l.ID != "" {
					byID[l.ID] = len(out) - 1
				}
			case OpBind:
				if j, ok := byID[l.ID]; ok {
					out[j].SessionID = l.SessionID
					latest[l.SessionID] = j
				}
			case OpLaunched, OpEnded:
				j, ok := latest[l.SessionID]
				if !ok {
					return
				}
				if l.Op == OpLaunched && out[j].Launched.IsZero() {
					out[j].Launched = l.At
				}
				if l.Op == OpEnded && out[j].Ended.IsZero() {
					out[j].Ended = l.At
				}
			}
		})
		skipped += s
	}
	return out, skipped, nil
}

// scan calls fn for each readable line of a day file, in order, and returns how many
// non-empty lines it skipped.
func scan(b []byte, fn func(Line)) int {
	skipped := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		l, ok := parse(raw)
		if !ok {
			skipped++
			continue
		}
		fn(l)
	}
	if sc.Err() != nil { // a line over 1 MiB: the rest of the file is unreadable
		skipped++
	}
	return skipped
}

func parse(raw []byte) (Line, bool) {
	var l Line
	if json.Unmarshal(raw, &l) != nil || l.V != 1 || l.At.IsZero() {
		return Line{}, false
	}
	l.At = l.At.UTC()
	switch l.Op {
	case OpLease:
		return l, l.Key != ""
	case OpLaunched, OpEnded:
		return l, l.SessionID != ""
	case OpBind:
		return l, l.SessionID != "" && l.ID != ""
	}
	return Line{}, false
}

// Append writes lines to the day files of their times, each with one write followed by an
// fsync (and an fsync of the folder when the file is new). Lines without V get V 1. The
// caller holds Lock when the lines are leases.
func (g Ledger) Append(lines ...Line) error {
	if len(lines) == 0 {
		return nil
	}
	if err := os.MkdirAll(g.days(), 0o700); err != nil {
		return err
	}
	for _, l := range lines {
		if l.V == 0 {
			l.V = 1
		}
		l.At = l.At.UTC()
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		if err := appendLine(filepath.Join(g.days(), dayName(l.At)), append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func appendLine(path string, b []byte) error {
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, fs.ErrNotExist)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// A crash in the middle of a write leaves a torn last line: start a new line so this
	// one stays readable (the torn one is skipped).
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err == nil && last[0] != '\n' {
			b = append([]byte{'\n'}, b...)
		}
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if created {
		syncDir(filepath.Dir(path))
	}
	return nil
}

// Prune deletes day files older than Days days at now. Other files are left alone.
func (g Ledger) Prune(now time.Time) error {
	ents, err := os.ReadDir(g.days())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	oldest := dayName(now.AddDate(0, 0, -(Days - 1)))
	removed := false
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		if _, err := time.Parse("2006-01-02", strings.TrimSuffix(n, ".jsonl")); err != nil {
			continue
		}
		if n < oldest {
			if err := os.Remove(filepath.Join(g.days(), n)); err == nil {
				removed = true
			}
		}
	}
	if removed {
		syncDir(g.days())
	}
	return nil
}

// Mark appends a `launched` or `ended` line for session id sid when a lease of it is in the
// day files of today or yesterday (UTC) and its newest lease has no line of op yet. It takes
// no lock. It reports whether it wrote a line.
func (g Ledger) Mark(sid, op string, now time.Time) (bool, error) {
	if sid == "" || (op != OpLaunched && op != OpEnded) {
		return false, nil
	}
	found, done := false, false
	for _, t := range []time.Time{now.AddDate(0, 0, -1), now} {
		b, err := os.ReadFile(filepath.Join(g.days(), dayName(t)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		scan(b, func(l Line) {
			if l.SessionID != sid {
				return
			}
			switch l.Op {
			case OpLease, OpBind:
				found, done = true, false
			case op:
				if found {
					done = true
				}
			}
		})
	}
	if !found || done {
		return false, nil
	}
	return true, g.Append(Line{V: 1, Op: op, At: now, SessionID: sid})
}

// Pending counts, per key, the leases younger than ttl at now (the projection strategies'
// reservations).
func Pending(ls []Lease, now time.Time, ttl time.Duration) map[string]int {
	out := map[string]int{}
	for _, l := range ls {
		if !l.At.After(now) && now.Sub(l.At) < ttl {
			out[l.Key]++
		}
	}
	return out
}

// Last returns the newest lease time per key and the key of the newest lease.
func Last(ls []Lease) (map[string]time.Time, string) {
	out := map[string]time.Time{}
	var lastKey string
	var lastAt time.Time
	for _, l := range ls {
		if l.At.After(out[l.Key]) {
			out[l.Key] = l.At
		}
		if !l.At.Before(lastAt) {
			lastAt, lastKey = l.At, l.Key
		}
	}
	return out, lastKey
}
