package status

import (
	"fmt"
	"io"
	"path"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/identity"
)

// Window is how much of the outbox `wy status` reads (the last 7 days).
const Window = 7 * 24 * time.Hour

// Options configures Collect.
type Options struct {
	OutboxDir string
	Home      string            // for ~ in cwd; optional
	Labels    map[string]string // config slots.labels: slot -> account label
	All       bool              // include ended and gone sessions
	Now       time.Time         // zero: time.Now()
	// Keepalive, when set, says what Claude Code's daemon files know of a session of a slot
	// (states.md 5.2). Nil: nothing is known.
	Keepalive func(slot, sessionID string) Keepalive
}

// Keepalive is what Claude Code's daemon files say about one session (states.md 5.2): whether
// it runs as a background job, whether that job is pinned, and when the daemon last retired
// it for being idle (zero: never).
type Keepalive struct {
	Background bool
	Pinned     bool
	RetiredAt  time.Time
}

// Row is one session of the table.
type Row struct {
	Account    string      `json:"account"` // label, else email, else "-"
	Slot       string      `json:"slot"`
	Project    string      `json:"project"` // last folder of cwd
	Cwd        string      `json:"cwd"`     // with ~
	State      state.State `json:"state"`
	Detail     string      `json:"detail,omitempty"`
	Since      time.Time   `json:"since"`
	LastEvent  time.Time   `json:"last_event,omitempty"`
	ContextPct *float64    `json:"context_pct"`
	NeedsYou   bool        `json:"needs_you"`
	Question   string      `json:"question,omitempty"`
	SessionID  string      `json:"session_id"`
	Email      string      `json:"email,omitempty"`
	// Background: a Claude Code background session; Pinned: its job is pinned, so Claude Code
	// never ends it for being idle (states.md 5.2).
	Background bool `json:"background,omitempty"`
	Pinned     bool `json:"pinned,omitempty"`
	// RetiredIdleAt: Claude Code ended this background session for being idle at that time,
	// after its last event, so the process is gone although the fold still says State.
	RetiredIdleAt *time.Time `json:"retired_idle_at,omitempty"`
}

// Collect reads the outbox and returns the table rows: needs-you first, then working, done,
// and (with All) ended and gone.
func Collect(o Options) ([]Row, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	recs, err := Load(o.OutboxDir, now.Add(-Window))
	if err != nil {
		return nil, err
	}
	return Build(Adapt(recs), o, now), nil
}

// Build folds each session and orders the rows.
func Build(sessions map[string]*Session, o Options, now time.Time) []Row {
	var rows []Row
	for _, s := range sessions {
		if len(s.Inputs) == 0 {
			continue
		}
		f := state.Fold(s.Inputs)
		if f.State == "" {
			continue
		}
		if !o.All && (f.State == state.Ended || f.State == state.Gone) {
			continue
		}
		cwd := tilde(o.Home, s.Cwd)
		row := Row{
			Account: account(s, o.Labels), Slot: s.Slot, Cwd: cwd, Project: project(cwd),
			State: f.State, Detail: f.Detail, Since: f.Since, LastEvent: f.LastEventAt,
			ContextPct: s.ContextPct, NeedsYou: f.NeedsYou(), Question: f.Question, SessionID: s.ID,
		}
		if s.Seat != nil {
			row.Email = s.Seat.Email
		}
		if o.Keepalive != nil {
			keepalive(&row, o.Keepalive(s.Slot, s.ID))
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		if a.NeedsYou { // longest waiting first
			if !a.Since.Equal(b.Since) {
				return a.Since.Before(b.Since)
			}
		} else if !a.Since.Equal(b.Since) { // newest first
			return a.Since.After(b.Since)
		}
		return a.SessionID < b.SessionID
	})
	return rows
}

// keepalive applies what Claude Code's daemon says of the row's session (states.md 5.2): an
// idle retirement counts only when it is newer than the session's last event (a session
// respawned or resumed since then is running again) and the fold has not ended it already.
func keepalive(r *Row, k Keepalive) {
	r.Background, r.Pinned = k.Background, k.Background && k.Pinned
	if !k.Background || k.RetiredAt.IsZero() || r.State == state.Ended || r.State == state.Gone {
		return
	}
	last := r.LastEvent
	if last.IsZero() {
		last = r.Since
	}
	if k.RetiredAt.After(last) {
		at := k.RetiredAt
		r.RetiredIdleAt = &at
	}
}

func rank(r Row) int {
	switch {
	case r.NeedsYou:
		return 0
	case r.State == state.Working:
		return 1
	case r.State == state.Done:
		return 2
	case r.State == state.Failed: // transient failures (overloaded)
		return 3
	case r.State == state.Ended:
		return 4
	}
	return 5
}

func account(s *Session, labels map[string]string) string {
	if l := labels[s.Slot]; l != "" {
		return l
	}
	if s.Seat != nil {
		if s.Seat.Email != "" {
			return s.Seat.Email
		}
		if len(s.Seat.Account) >= 8 {
			return s.Seat.Account[:8]
		}
	}
	return "-"
}

func tilde(home, p string) string {
	if p == "" || home == "" {
		return p
	}
	return identity.Tilde(home, p)
}

func project(cwd string) string {
	if cwd == "" {
		return "-"
	}
	if cwd == "~" || cwd == "/" {
		return cwd
	}
	return path.Base(cwd)
}

// RetiredLabel is the STATE column of a session Claude Code ended for being idle.
const RetiredLabel = "ended:idle-retired"

// RetiredNote is printed under the table when a row is RetiredLabel.
const RetiredNote = RetiredLabel + ": Claude Code ended this background session for being idle. " +
	"`claude respawn <session>` starts it again; pin it (`claude agents`, select it, Ctrl+T) to keep it running."

// Label is the state as shown in the table: failed sessions carry their error code, a
// session Claude Code retired for idleness is RetiredLabel, and a pinned background session
// is marked "(pinned)".
func (r Row) Label() string {
	l := string(r.State)
	switch {
	case r.RetiredIdleAt != nil:
		return RetiredLabel
	case r.State == state.Failed && r.Detail != "":
		l += ":" + r.Detail
	}
	if r.Pinned {
		l += " (pinned)"
	}
	return l
}

// Age renders d as "42s", "7m", "3h", "2d".
func Age(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Render writes the table. now is the clock used for the "since" column.
func Render(w io.Writer, rows []Row, now time.Time) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No sessions in the last 7 days. Is the watchyard plugin installed? Run `wy doctor`.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\tSLOT\tPROJECT\tSTATE\tSINCE\tCTX\tSESSION")
	need, retired := 0, false
	for _, r := range rows {
		retired = retired || r.RetiredIdleAt != nil
		ctx := "-"
		if r.ContextPct != nil {
			ctx = fmt.Sprintf("%.0f%%", *r.ContextPct)
		}
		mark := ""
		if r.NeedsYou {
			need++
			mark = "!"
		}
		sid := r.SessionID
		if len(sid) > 8 {
			sid = sid[:8]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Account, r.Slot, r.Project,
			mark+r.Label(), Age(now.Sub(r.Since)), ctx, sid)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\n%d session%s, %d need%s you\n", len(rows), plural(len(rows)), need, map[bool]string{true: "", false: "s"}[need != 1])
	if err == nil && retired {
		_, err = fmt.Fprintln(w, RetiredNote)
	}
	return err
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
