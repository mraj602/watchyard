package board

import (
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// Field widths of a View: free text is cut to these many columns (T2 text safety applies
// first). Renderers cut further to their own width.
const (
	maxProject = 48
	maxTitle   = 80
	maxLabel   = 32
	maxMachine = 32
	maxLine2   = 200
	maxResume  = 200
	maxURL     = 512
)

// neutralColour is used for a seat with no colour and for the Unattributed group.
const neutralColour = "#64748b"

// UnattributedLabel is the group of sessions with no known seat; it is always last.
const UnattributedLabel = "Unattributed"

var (
	hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	// focusURL is T7's allow-list. The enforcing copy is internal/termctl; this one
	// only decides Row.CanFocus, and `wy term focus` checks again before opening anything.
	focusURL = regexp.MustCompile(`^(warp|warppreview)://session/[0-9a-f]{32}$`)
)

// Options are what both sources share.
type Options struct {
	// Now is the clock; zero means time.Now().
	Now time.Time
	// Loc is the time zone of reset times; nil means time.Local.
	Loc *time.Location
	// Machine is the machine the board runs on (ID empty when not known yet).
	Machine Machine
	// SessionID marks that row as `this_session` (the `--session` flag).
	SessionID string
	// IncludeEnded keeps ended and gone sessions of the last EndedWithin (the `e` key).
	IncludeEnded bool
	// Hub is the link state shown in the header.
	Hub HubInfo
	// WarpColour maps "#rrggbb" to one of Warp's six chromatic names. nil uses WarpName,
	// which is internal/warp's table (the tab configs' own), so the two cannot differ.
	WarpColour func(hex string) string
	// Drains are the owner's drains (`wy account drain`): until when, by account label; nil
	// none. Read at each build (the setting changes while the board runs).
	Drains func() map[string]time.Time
	// Offs: this machine's mode settings by account label, true for off and false for any
	// other mode (next.accounts.<label>.mode); nil none. A label not in it follows the hub.
	Offs func() map[string]bool
}

// EndedWithin is how long an ended or gone session stays on the board with the `e` toggle.
const EndedWithin = 48 * time.Hour

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// rowIn is one session as either source sees it, before words are chosen. rowFrom is the
// only function that turns it into a Row, so the two sources cannot disagree on a word.
type rowIn struct {
	ID       string
	State    state.State
	Detail   string
	NeedsYou bool
	Since    time.Time
	// Tool: see wordIn.
	Tool string
	// Question is the question line of an asked session.
	Question string
	// LastMessage is nil when last messages are off (privacy).
	LastMessage *string

	Project, Title        string
	Seat, Account, Colour string
	Machine, MachineName  string
	// TermHost is term.host: the machine whose terminal shows this session.
	TermHost   string
	ContextPct *float64
	Stale      bool
	Greyed     bool
	// FocusURL is the focus URL as delivered (hub) or recorded (local), "" when none. It
	// is only checked, never copied into the View.
	FocusURL   string
	Resume     string
	RemoteURL  string
	LimitReset *time.Time
}

// ctx is Options with the clock resolved.
type ctx struct {
	now  time.Time
	loc  *time.Location
	opts Options
}

func newCtx(o Options) ctx {
	loc := o.Loc
	if loc == nil {
		loc = time.Local
	}
	return ctx{now: o.now(), loc: loc, opts: o}
}

func (c ctx) warp(hex string) string {
	if c.opts.WarpColour != nil {
		return c.opts.WarpColour(hex)
	}
	return WarpName(hex)
}

// rowFrom builds a Row from a session, per T2: words, line 2, here, can_focus.
func (c ctx) rowFrom(in rowIn) Row {
	w := wordIn{State: in.State, Detail: in.Detail, Since: in.Since, Tool: field(in.Tool, 24), LimitReset: in.LimitReset}
	if w.LimitReset != nil && !w.LimitReset.After(c.now) {
		w.LimitReset = nil // the window has reset since the sample: no time to show
	}
	r := Row{
		ID: Line(in.ID), State: string(in.State), Detail: Line(in.Detail), NeedsYou: in.NeedsYou,
		Glyph:   GlyphFor(in.State),
		Status:  field(statusWord(w, c.now, c.loc), maxLabel),
		Project: field(in.Project, maxProject), Title: field(in.Title, maxTitle),
		Account: field(in.Account, maxLabel), Colour: colourOrNeutral(in.Colour), Seat: Line(in.Seat),
		Machine: Line(in.Machine), MachineName: field(in.MachineName, maxMachine),
		Since: in.Since.UTC(), Age: Age(c.now.Sub(in.Since)),
		Stale: in.Stale, Greyed: in.Greyed,
		Resume: field(in.Resume, maxResume),
	}
	if r.Project == "" {
		r.Project = "-"
	}
	me := c.opts.Machine.ID
	r.Here = me != "" && (in.Machine == me || in.TermHost == me)
	r.ThisSession = c.opts.SessionID != "" && in.ID == c.opts.SessionID
	if in.ContextPct != nil {
		p := pctInt(*in.ContextPct)
		r.ContextPct = &p
		r.ContextLevel = ContextLevel(p)
	} else {
		r.ContextLevel = ContextOK
	}
	r.CanFocus = focusURL.MatchString(in.FocusURL) && (in.TermHost == "" || in.TermHost == me || me == "")
	if strings.HasPrefix(in.RemoteURL, "https://claude.ai/") && len(in.RemoteURL) <= maxURL && Clean(in.RemoteURL) == in.RemoteURL &&
		!strings.ContainsAny(in.RemoteURL, " \"'<>") {
		r.RemoteURL = in.RemoteURL
	}
	r.Line2, r.Line2Kind = line2(in)
	return r
}

// line2 chooses the second line (T2 "Recap / last lines"): the question of an asked
// session, the error of a failed one, the recap of a done or working one.
func line2(in rowIn) (string, string) {
	switch in.State {
	case state.Asked:
		if q := field(in.Question, maxLine2); q != "" {
			return q, Line2Question
		}
		if in.LastMessage != nil && in.Detail == "question" {
			if q := Recap(*in.LastMessage, maxLine2); q != "" {
				return q, Line2Question
			}
		}
	case state.Failed:
		if in.Detail != "" {
			return field(errorWord(in.Detail), maxLine2), Line2Error
		}
	case state.Done, state.Working:
		if in.LastMessage != nil {
			if s := Recap(*in.LastMessage, maxLine2); s != "" {
				return s, Line2Recap
			}
		}
	}
	return "", Line2None
}

func colourOrNeutral(c string) string {
	if hexColour.MatchString(c) {
		return strings.ToLower(c)
	}
	return neutralColour
}

// ---------------------------------------------------------------------------------------
// The hub's read API as the board needs it. The hub source decodes /v1/overview, /v1/accounts and
// /v1/sessions into these (the JSON names are api.yaml's) and calls BuildHub.

// HubSession is api.yaml Session, the fields the board reads. Times decode from RFC 3339.
type HubSession struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	Detail      *string    `json:"detail"`
	NeedsYou    bool       `json:"needs_you"`
	StateSince  *time.Time `json:"state_since"`
	Question    *string    `json:"question"`
	LastMessage *string    `json:"last_message"`
	Seat        *string    `json:"seat"`
	SeatLabel   *string    `json:"seat_label"`
	SeatColour  *string    `json:"seat_colour"`
	Machine     *string    `json:"machine"`
	MachineName *string    `json:"machine_name"`
	Project     *string    `json:"project"`
	Cwd         *string    `json:"cwd"`
	Name        *string    `json:"name"`
	Title       *string    `json:"title"`
	ContextPct  *float64   `json:"context_pct"`
	LastEventAt *time.Time `json:"last_event_at"`
	RemoteURL   *string    `json:"remote_url"`
	Resume      *string    `json:"resume"`
	FocusURL    *string    `json:"focus_url"`
	Stale       bool       `json:"stale"`

	MachineStale bool `json:"machine_stale"`
	// Tool is not in the hub's Session today: the tool of the last PermissionRequest or
	// AskUserQuestion. When a later hub sends it, "perm" becomes "perm · Bash" with no
	// change here.
	Tool *string `json:"tool,omitempty"`
}

// HubWindow is api.yaml Window.
type HubWindow struct {
	Key      string     `json:"key"`
	UsedPct  *float64   `json:"used_pct"`
	ResetsAt *time.Time `json:"resets_at"`
}

// HubSeat is api.yaml Seat (one item of /v1/accounts).
type HubSeat struct {
	ID      string      `json:"id"`
	Email   *string     `json:"email"`
	Label   *string     `json:"label"`
	Colour  *string     `json:"colour"`
	Order   int         `json:"order"`
	Hidden  bool        `json:"hidden"`
	Windows []HubWindow `json:"windows"`
	// Mode and DrainUntil: the seat's picker mode (off shows) and owner drain (setup.md 8.5).
	Mode       *string    `json:"mode"`
	DrainUntil *time.Time `json:"drain_until"`
}

// HubMachine is the part of api.yaml Machine the board reads.
type HubMachine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HubOverview is api.yaml Overview, the fields the board reads. NeedsYou is the hub's list
// of sessions that need you, oldest first; only the ids are used, in that order.
type HubOverview struct {
	Time     time.Time    `json:"time"`
	Machines []HubMachine `json:"machines"`
	NeedsYou []struct {
		ID string `json:"id"`
	} `json:"needs_you"`
	StreamID int64 `json:"stream_id"`
}

// HubData is everything one View of the hub source is built from.
type HubData struct {
	Overview HubOverview
	// Seats are /v1/accounts items in the hub's order.
	Seats []HubSeat
	// Sessions are /v1/sessions items in the hub's order.
	Sessions []HubSession
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// BuildHub turns the hub's reads into a View with source "hub" (T2, T3). Groups follow
// the order of d.Seats with "Unattributed" last, rows keep the order of d.Sessions, and
// needs_you keeps the order of the overview. o.Hub is copied into the View; the caller
// sets it to HubLive (or whatever it knows).
func BuildHub(d HubData, o Options) View {
	c := newCtx(o)
	seats := map[string]HubSeat{}
	for _, s := range d.Seats {
		seats[s.ID] = s
	}
	var rows []Row
	for _, s := range d.Sessions {
		st := state.State(s.State)
		since := time.Time{}
		if s.StateSince != nil {
			since = *s.StateSince
		}
		if (st == state.Ended || st == state.Gone) && !c.keepEnded(since) {
			continue
		}
		var reset *time.Time
		label, colour := deref(s.SeatLabel), deref(s.SeatColour)
		seatID := deref(s.Seat)
		if seat, ok := seats[seatID]; ok {
			if label == "" {
				label = seatLabel(seat)
			}
			if colour == "" {
				colour = deref(seat.Colour)
			}
			if w := findWindow(seat.Windows, "five_hour"); w != nil {
				reset = w.ResetsAt
			}
		}
		project := deref(s.Project)
		if project == "" && deref(s.Cwd) != "" {
			project = path.Base(strings.ReplaceAll(deref(s.Cwd), `\`, "/"))
		}
		title := deref(s.Title)
		if title == "" {
			title = deref(s.Name)
		}
		in := rowIn{
			ID: s.ID, State: st, Detail: deref(s.Detail), NeedsYou: s.NeedsYou, Since: since,
			Tool: deref(s.Tool), Question: deref(s.Question), LastMessage: s.LastMessage,
			Project: project, Title: title, Seat: seatID, Account: label, Colour: colour,
			Machine: deref(s.Machine), MachineName: deref(s.MachineName), ContextPct: s.ContextPct,
			Stale: s.Stale || s.MachineStale, FocusURL: deref(s.FocusURL), Resume: safeResume(deref(s.Resume)),
			RemoteURL: deref(s.RemoteURL), LimitReset: reset,
		}
		rows = append(rows, c.rowFrom(in))
	}
	var groups []Group
	for _, s := range d.Seats {
		if s.Hidden && !hasSeatRow(rows, s.ID) {
			continue
		}
		groups = append(groups, c.groupFrom(s, len(groups)))
	}
	var needs []string
	for _, n := range d.Overview.NeedsYou {
		needs = append(needs, n.ID)
	}
	machines := len(d.Overview.Machines)
	return c.assemble(SourceHub, rows, groups, needs, machines)
}

func hasSeatRow(rows []Row, seat string) bool {
	for _, r := range rows {
		if r.Seat == seat {
			return true
		}
	}
	return false
}

func seatLabel(s HubSeat) string {
	if l := deref(s.Label); l != "" {
		return l
	}
	if e := deref(s.Email); e != "" {
		return e
	}
	return "account"
}

func findWindow(ws []HubWindow, key string) *HubWindow {
	for i := range ws {
		if ws[i].Key == key {
			return &ws[i]
		}
	}
	return nil
}

// groupFrom is a seat's block: label, colours and the two windows.
func (c ctx) groupFrom(s HubSeat, index int) Group {
	colour := deref(s.Colour)
	if !hexColour.MatchString(colour) {
		colour = model.Palette[index%len(model.Palette)].Hex
	}
	g := Group{Seat: Line(s.ID), Label: field(seatLabel(s), maxLabel), Colour: strings.ToLower(colour), Rows: []string{}}
	g.WarpColour = c.warp(g.Colour)
	if w := findWindow(s.Windows, "five_hour"); w != nil {
		g.FiveHour = c.window(w, true)
	}
	if w := findWindow(s.Windows, "seven_day"); w != nil {
		g.SevenDay = c.window(w, false)
	}
	g.Limited = c.windowFull(s.Windows, "five_hour") || c.windowFull(s.Windows, "seven_day")
	var u time.Time
	if s.DrainUntil != nil {
		u = *s.DrainUntil
	}
	off := s.Mode != nil && *s.Mode == "off"
	if c.opts.Drains != nil {
		// This machine's own setting wins (next.accounts.<label>).
		if lu, ok := c.opts.Drains()[seatLabel(s)]; ok {
			u = lu
		}
	}
	if c.opts.Offs != nil {
		if lo, ok := c.opts.Offs()[seatLabel(s)]; ok {
			off = lo
		}
	}
	switch {
	case off:
		g.Note = OffNote
	case u.After(c.now) && (g.SevenDay == nil || g.SevenDay.Pct < DrainAim):
		g.Note = drainNote(u.In(c.loc), c.now.In(c.loc))
	}
	return g
}

// OffNote is the note of an account the owner turned off: its usage still shows.
const OffNote = "off (owner): never picked, nothing moved in"

// DrainAim is the weekly level an owner's drain ends at (the picker's 98 less its 1-point
// drain margin).
const DrainAim = 97

// drainNote is "draining (owner, until 23:59)", a day name when it is not today.
func drainNote(u, now time.Time) string {
	f := "15:04"
	if y, m, d := u.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
		f = "Mon 15:04"
	}
	return "draining (owner, until " + u.Format(f) + ")"
}

// window is one limit window as shown; nil when the window has no percentage.
func (c ctx) window(w *HubWindow, withReset bool) *Window {
	if w.UsedPct == nil {
		return nil
	}
	pct := pctInt(*w.UsedPct)
	if w.ResetsAt != nil && !w.ResetsAt.After(c.now) && *w.UsedPct < 100 {
		// the window rolled over after the sample: keep the number, drop the stale time
		return &Window{Pct: pct, Text: WindowText(pct, nil, c.now, c.loc, false)}
	}
	out := &Window{Pct: pct, Text: WindowText(pct, w.ResetsAt, c.now, c.loc, withReset)}
	if w.ResetsAt != nil {
		t := w.ResetsAt.UTC()
		out.ResetsAt = &t
	}
	return out
}

func (c ctx) windowFull(ws []HubWindow, key string) bool {
	w := findWindow(ws, key)
	return w != nil && w.UsedPct != nil && *w.UsedPct >= 100 && (w.ResetsAt == nil || w.ResetsAt.After(c.now))
}

func (c ctx) keepEnded(since time.Time) bool {
	return c.opts.IncludeEnded && c.now.Sub(since) <= EndedWithin
}

// resumeRE is the only shape of resume command taken from a hub: the hub is another
// machine's data, and the command goes to the clipboard (OSC 52, the pane's copy button)
// to be pasted into a shell, so nothing else is copied verbatim.
var resumeRE = regexp.MustCompile(`^(claude(-[0-9]{1,3})?|CLAUDE_CONFIG_DIR=(~/)?[A-Za-z0-9._@%+=:,/-]+ claude) --resume [0-9A-Za-z_-]{1,64}$`)

func safeResume(s string) string {
	if resumeRE.MatchString(s) && !strings.Contains(s, "..") {
		return s
	}
	return ""
}
