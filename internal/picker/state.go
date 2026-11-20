package picker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// keepLast is how long a key stays in the pick log without a new pick.
const keepLast = 30 * 24 * time.Hour

// Reservation is a pick that has not shown up in the usage data yet: it counts as one more
// session on its account until Until.
type Reservation struct {
	Key    string    `json:"key"`
	Folder string    `json:"folder"`
	At     time.Time `json:"at"`
	Until  time.Time `json:"until"`
}

// Ledger is <state>/next/picks.json: live reservations and the pick log.
type Ledger struct {
	V            int                  `json:"v"`
	Reservations []Reservation        `json:"reservations"`
	Last         map[string]time.Time `json:"last"`
	LastKey      string               `json:"last_key,omitempty"`
}

// Pending counts the reservations live at now, per key.
func (l Ledger) Pending(now time.Time) map[string]int {
	out := map[string]int{}
	for _, r := range l.Reservations {
		if now.Before(r.Until) {
			out[r.Key]++
		}
	}
	return out
}

// Record adds a pick: a reservation for ttl (none when ttl is 0) and the pick log entry.
func (l *Ledger) Record(key, folder string, now time.Time, ttl time.Duration) {
	if ttl > 0 {
		l.Reservations = append(l.Reservations, Reservation{Key: key, Folder: folder, At: now.UTC(), Until: now.Add(ttl).UTC()})
	}
	if l.Last == nil {
		l.Last = map[string]time.Time{}
	}
	l.Last[key] = now.UTC()
	l.LastKey = key
}

// prune drops expired reservations and old log entries.
func (l *Ledger) prune(now time.Time) {
	var keep []Reservation
	for _, r := range l.Reservations {
		if now.Before(r.Until) {
			keep = append(keep, r)
		}
	}
	l.Reservations = keep
	for k, t := range l.Last {
		if now.Sub(t) > keepLast {
			delete(l.Last, k)
		}
	}
	if _, ok := l.Last[l.LastKey]; !ok {
		l.LastKey = ""
	}
}

// Store reads and writes the ledger in Dir (<state>/next), under Dir/picks.lock.
type Store struct{ Dir string }

func (s Store) path() string { return filepath.Join(s.Dir, "picks.json") }

// Read returns the ledger without changing it (a missing or damaged file is empty).
func (s Store) Read(now time.Time) (Ledger, error) {
	l, err := s.read()
	l.prune(now)
	return l, err
}

func (s Store) read() (Ledger, error) {
	l := Ledger{V: 1, Last: map[string]time.Time{}}
	b, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if json.Unmarshal(b, &l) != nil || l.V != 1 {
		return Ledger{V: 1, Last: map[string]time.Time{}}, nil // damaged: start again
	}
	if l.Last == nil {
		l.Last = map[string]time.Time{}
	}
	return l, nil
}

// Update runs fn on the ledger under the lock and writes the result. Bursts of
// `wy account next` therefore see each other's reservations.
func (s Store) Update(now time.Time, fn func(l *Ledger) error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(s.Dir, "picks.lock"))
	if err != nil {
		return fmt.Errorf("lock the pick log: %w", err)
	}
	defer unlock()
	l, err := s.read()
	if err != nil {
		return err
	}
	l.prune(now)
	if err := fn(&l); err != nil {
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path(), append(b, '\n'), 0o600)
}
