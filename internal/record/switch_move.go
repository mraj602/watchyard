package record

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// KindSwitchMove is one move of a session to another account, by the Smart Account Switch or
// by hand with `wy move` (record.md 4.13, setup.md 8.6 Switch log). The hub keeps it for the
// board's Switches page.
const KindSwitchMove = "switch_move"

// Bounds of a switch_move payload.
const (
	SwitchMoveMaxText   = 500
	SwitchMoveMaxError  = 1000
	SwitchMoveMaxLabel  = 100
	SwitchMoveMaxFolder = 4096
)

// SwitchMoveKinds are the ways a session runs when it is moved (the mover's launch kinds).
var SwitchMoveKinds = []string{"tmux", "wy-run", "terminal", "background", "headless"}

// SwitchMovePayload is the payload of a switch_move record.
type SwitchMovePayload struct {
	// At is when the move was made (RFC 3339 UTC).
	At          string `json:"at"`
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	// Kind is how the session ran: tmux, wy-run, terminal, background or headless.
	Kind string `json:"kind"`
	// Interactive: a human-facing session (tmux, wy-run, terminal): it got a message where
	// its owner sees it, and is never restarted without that notice.
	Interactive bool `json:"interactive,omitempty"`
	// From and To are the accounts' labels (the folder's own name when it has none).
	From string `json:"from"`
	To   string `json:"to"`
	// Auto: made by the Smart Account Switch; false: by hand with wy move.
	Auto   bool   `json:"auto,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Window is the limit window that triggered an automatic move ("5h" or "7d").
	Window string `json:"window,omitempty"`
	// P5 and P7 are the from-account's projected 5-hour and weekly levels at its burn, Cap5
	// and Cap7 the caps they were held to, Burn its 5-hour points per hour. Absent when the
	// machine had no projection (a move by hand).
	P5    *float64 `json:"p5,omitempty"`
	P7    *float64 `json:"p7,omitempty"`
	Cap5  *float64 `json:"cap5,omitempty"`
	Cap7  *float64 `json:"cap7,omitempty"`
	Burn5 *float64 `json:"burn5,omitempty"`
	// CrossOrg: the destination is another organisation.
	CrossOrg bool `json:"cross_org,omitempty"`
	// Error is why the move failed ("" when the session was resumed on the new account).
	Error string `json:"error,omitempty"`
}

// Validate checks the fields record.md 4.13 requires.
func (p SwitchMovePayload) Validate() error {
	if _, err := time.Parse(time.RFC3339, p.At); err != nil {
		return fmt.Errorf("switch_move: bad at")
	}
	if p.SessionID == "" || len(p.SessionID) > 100 {
		return fmt.Errorf("switch_move: bad session_id")
	}
	if len(p.SessionName) > SwitchMoveMaxLabel || !utf8.ValidString(p.SessionName) {
		return fmt.Errorf("switch_move: bad session_name")
	}
	ok := false
	for _, k := range SwitchMoveKinds {
		ok = ok || p.Kind == k
	}
	if !ok {
		return fmt.Errorf("switch_move: bad kind %q", p.Kind)
	}
	for _, l := range []string{p.From, p.To} {
		if l == "" || len(l) > SwitchMoveMaxLabel || !utf8.ValidString(l) {
			return fmt.Errorf("switch_move: bad account label")
		}
	}
	if len(p.Reason) > SwitchMoveMaxText || !utf8.ValidString(p.Reason) || len(p.Window) > 8 {
		return fmt.Errorf("switch_move: bad reason")
	}
	if len(p.Error) > SwitchMoveMaxError || !utf8.ValidString(p.Error) {
		return fmt.Errorf("switch_move: bad error")
	}
	return nil
}

// NewSwitchMove builds a switch_move record: no seat and no slot, it is the machine's.
func NewSwitchMove(m Meta, p SwitchMovePayload) (*Record, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	m.Seat, m.Slot, m.Term = nil, "", nil
	r := New(KindSwitchMove, m)
	return r, r.SetPayload(p)
}
