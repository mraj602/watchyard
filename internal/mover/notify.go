package mover

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Notices (setup.md 8.6, Notices and digest): each automatic move is pushed to the owner's
// phone, at most once per NotifyEvery per machine; moves inside the window wait in
// <state>/move/notify.json and go out as one push.
const (
	NotifyEvery = 5 * time.Minute
	// NotifyKeep: a waiting move older than this is dropped (a push hours late says nothing).
	NotifyKeep = time.Hour
	// maxReason is the longest reason a push carries.
	maxReason = 120
)

// Notice is one automatic move waiting to be pushed.
type Notice struct {
	At      time.Time `json:"at"`
	Session string    `json:"session"`
	Name    string    `json:"name,omitempty"`
	From    string    `json:"from"` // label
	To      string    `json:"to"`   // label
	Reason  string    `json:"reason,omitempty"`
}

// NotifyFile is <state>/move/notify.json: the moves seen up to Cursor, the ones not pushed
// yet and when a push was last tried.
type NotifyFile struct {
	Cursor  time.Time `json:"cursor"`
	LastTry time.Time `json:"last_try"`
	Pending []Notice  `json:"pending"`
}

// LoadNotify reads notify.json (zero when none).
func (s State) LoadNotify() NotifyFile {
	var f NotifyFile
	if b, err := os.ReadFile(s.path("notify.json")); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f
}

// SaveNotify writes notify.json.
func (s State) SaveNotify(f NotifyFile) error { return s.write(s.path("notify.json"), f) }

func (n Notice) label() string {
	if n.Name != "" {
		return n.Name
	}
	if len(n.Session) > 8 {
		return n.Session[:8]
	}
	return n.Session
}

func cutRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// NoticeText is the push for the pending moves: one move is "<session> <from> -> <to>:
// <reason>"; several are "<n> moves: a x->y; b ...".
func NoticeText(ns []Notice) string {
	switch len(ns) {
	case 0:
		return ""
	case 1:
		n := ns[0]
		t := fmt.Sprintf("%s %s -> %s", n.label(), n.From, n.To)
		if n.Reason != "" {
			t += ": " + cutRunes(n.Reason, maxReason)
		}
		return t
	}
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, fmt.Sprintf("%s %s->%s", n.label(), n.From, n.To))
	}
	return fmt.Sprintf("%d moves: %s", len(ns), strings.Join(parts, "; "))
}

// Notify collects the automatic moves logged after the cursor and pushes the pending ones
// through send when the last try was NotifyEvery ago or more. enabled is account.move_notify;
// off, it only moves the cursor (turning it on never pushes old moves). send nil means no
// push path is ready: moves keep waiting (for NotifyKeep). name gives the label of a slot
// folder or account key. send gets the text and the moves it covers (oldest first). It reports how many moves went out. The first run only sets the
// cursor.
func (s State) Notify(now time.Time, enabled bool, name func(dir, key string) string, send func(text string, moves []Notice) error) (int, error) {
	f := s.LoadNotify()
	if f.Cursor.IsZero() {
		f.Cursor = now
		return 0, s.SaveNotify(f)
	}
	for _, e := range s.Log(f.Cursor) {
		if !e.At.After(f.Cursor) {
			continue
		}
		f.Cursor = e.At
		if e.Auto && enabled {
			f.Pending = append(f.Pending, Notice{At: e.At, Session: e.SessionID, Name: e.Name,
				From: name(e.FromDir, e.From), To: name(e.ToDir, e.To), Reason: e.Reason})
		}
	}
	if !enabled {
		f.Pending = nil
	}
	keep := f.Pending[:0]
	for _, n := range f.Pending {
		if now.Sub(n.At) <= NotifyKeep {
			keep = append(keep, n)
		}
	}
	f.Pending = keep
	sent := 0
	var err error
	if len(f.Pending) > 0 && send != nil && now.Sub(f.LastTry) >= NotifyEvery {
		f.LastTry = now
		if err = send(NoticeText(f.Pending), f.Pending); err == nil {
			sent = len(f.Pending)
			f.Pending = nil
		}
	}
	if serr := s.SaveNotify(f); err == nil {
		err = serr
	}
	return sent, err
}
