package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Version is the "v" of every View this build writes (docs/spec/terminal.md T2). A reader
// drops a View with any other v.
const Version = 1

// Limits of T2: a View holds at most MaxRows rows and its JSON is at most MaxBytes. Overflow
// keeps every needs-you row, then the newest, and sets Truncated to the number dropped.
const (
	MaxRows  = 300
	MaxBytes = 512 << 10
)

// Sources of a View (View.Source).
const (
	SourceHub   = "hub"
	SourceLocal = "local"
	SourceMixed = "mixed" // the hub is offline: this machine's rows are live, others greyed
)

// Hub link states (HubInfo.State).
const (
	HubLive     = "live"
	HubOffline  = "offline"
	HubNone     = "none"     // no hub.url
	HubUnpaired = "unpaired" // hub.url but no usable board token: "run `wy board connect`"
)

// Row glyphs (Row.Glyph): what the renderers draw (● ▲ ○ ■, or * ! o x with --ascii).
const (
	GlyphWorking = "working"
	GlyphNeeds   = "needs"
	GlyphDone    = "done"
	GlyphFailed  = "failed"
	GlyphEnded   = "ended"
	GlyphGone    = "gone"
)

// Kinds of a row's second line (Row.Line2Kind).
const (
	Line2Question = "question"
	Line2Recap    = "recap"
	Line2Error    = "error"
	Line2None     = "none"
)

// Context levels (Row.ContextLevel): ok below 80 %, warn from 80, high from 92.
const (
	ContextOK   = "ok"
	ContextWarn = "warn"
	ContextHigh = "high"

	ContextWarnPct = 80
	ContextHighPct = 92
)

// View is the whole board at one moment: one JSON object, one NDJSON line of
// `wy board --follow --ndjson`, the value of `wy board --once --json`, and what the pane mod
// keeps in one atom. JSON tags are fixed by T2.
type View struct {
	V           int       `json:"v"`
	GeneratedAt time.Time `json:"generated_at"`
	Source      string    `json:"source"`
	Hub         HubInfo   `json:"hub"`
	Machine     Machine   `json:"machine"`
	Counts      Counts    `json:"counts"`
	// NeedsYou lists row ids, oldest waiting first (the hub's order).
	NeedsYou []string `json:"needs_you"`
	// Groups are the accounts in /v1/accounts order, "Unattributed" last.
	Groups []Group `json:"groups"`
	// Rows holds every row by id; NeedsYou and Groups[].Rows refer to it.
	Rows map[string]Row `json:"rows"`
	// Truncated is the number of rows dropped by the MaxRows / MaxBytes caps (0: none).
	Truncated int `json:"truncated,omitempty"`
}

// HubInfo is the board's link to the hub.
type HubInfo struct {
	State string `json:"state"`
	URL   string `json:"url"`
	// OfflineSince is set while State is offline (the first failed reconnect), else null.
	OfflineSince *time.Time `json:"offline_since"`
}

// Machine is the machine the board runs on.
type Machine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Counts is the header line: "14 sessions · 3 machines", and the needs-you total.
type Counts struct {
	Sessions int `json:"sessions"`
	Machines int `json:"machines"`
	NeedsYou int `json:"needs_you"`
}

// Group is one account (seat) block: its label, colours, limit windows and rows.
type Group struct {
	Seat  string `json:"seat"`
	Label string `json:"label"`
	// Colour is "#rrggbb"; WarpColour is the nearest of Warp's six chromatic names (T8).
	Colour     string `json:"colour"`
	WarpColour string `json:"warp_colour"`
	// FiveHour and SevenDay are null when no limit sample is known for the seat.
	FiveHour *Window `json:"five_hour"`
	SevenDay *Window `json:"seven_day"`
	Limited  bool    `json:"limited"`
	// Note: the owner drains the account ("draining (owner, until 23:59)"; "" none).
	Note string `json:"note,omitempty"`
	// Rows are row ids in /v1/sessions order (needs-you first, then newest activity).
	Rows []string `json:"rows"`
}

// Window is one limit window as shown: "41% · 15:10".
type Window struct {
	Pct      int        `json:"pct"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Text     string     `json:"text"`
}

// Row is one session. Every string in it has passed T2's text safety (no C0/C1 controls,
// no ESC, no bidi overrides), so a renderer may print it raw. The focus URL itself is never
// here: CanFocus only says `wy term focus --session <id>` will find one (T7).
type Row struct {
	ID string `json:"id"`
	// State and Detail are the states.md state and detail.
	State    string `json:"state"`
	Detail   string `json:"detail"`
	NeedsYou bool   `json:"needs_you"`
	Glyph    string `json:"glyph"`
	// Status is the short word shown: asked, perm, limit · 17:00, done, 2m, new.
	Status  string `json:"status"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Account string `json:"account"`
	Colour  string `json:"colour"`
	Seat    string `json:"seat"`
	Machine string `json:"machine"`
	// MachineName is shown as a chip only when Here is false.
	MachineName string `json:"machine_name"`
	Here        bool   `json:"here"`
	ThisSession bool   `json:"this_session"`
	// ContextPct is null when no status-line sample gave the context size.
	ContextPct   *int      `json:"context_pct"`
	ContextLevel string    `json:"context_level"`
	Since        time.Time `json:"since"`
	Age          string    `json:"age"`
	Line2        string    `json:"line2"`
	Line2Kind    string    `json:"line2_kind"`
	Stale        bool      `json:"stale"`
	Greyed       bool      `json:"greyed"`
	CanFocus     bool      `json:"can_focus"`
	// Resume is the command `c` copies, e.g. "claude-2 --resume <id>".
	Resume string `json:"resume"`
	// RemoteURL is a claude.ai link; renderers show it only when it starts with
	// "https://claude.ai/".
	RemoteURL string `json:"remote_url,omitempty"`
}

// Source produces Views: the hub source (hubsrc), the local source (local.go), or a fake in
// tests. Start begins producing and returns a channel that receives a View on every change
// (the first one as soon as it is known) and is closed when ctx ends. Refresh asks for a
// full refetch or refold now (the `r` key); it never blocks.
type Source interface {
	Start(ctx context.Context) (<-chan View, error)
	Refresh()
}

// ErrVersion is returned by Decode for a View whose v is not Version.
var ErrVersion = errors.New("board: unsupported view version")

// Decode parses one View (one NDJSON line, or a `--once --json` document) and checks it:
// v must be Version, and every id in NeedsYou and Groups[].Rows must be in Rows. Unknown
// fields are ignored, so an older reader keeps working with a newer writer of the same v.
func Decode(data []byte) (View, error) {
	var probe struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return View{}, err
	}
	if probe.V == nil || *probe.V != Version {
		return View{}, ErrVersion
	}
	var v View
	if err := json.Unmarshal(data, &v); err != nil {
		return View{}, err
	}
	return v, v.Check()
}

// Check reports a View whose id lists point at rows that are not there, or whose row keys
// differ from the row ids.
func (v View) Check() error {
	if v.V != Version {
		return ErrVersion
	}
	for k, r := range v.Rows {
		if r.ID != k {
			return fmt.Errorf("board: row %q has id %q", k, r.ID)
		}
	}
	for _, id := range v.NeedsYou {
		if _, ok := v.Rows[id]; !ok {
			return fmt.Errorf("board: needs_you lists unknown row %q", id)
		}
	}
	for _, g := range v.Groups {
		for _, id := range g.Rows {
			if _, ok := v.Rows[id]; !ok {
				return fmt.Errorf("board: group %q lists unknown row %q", g.Seat, id)
			}
		}
	}
	return nil
}
