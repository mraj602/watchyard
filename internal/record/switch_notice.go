package record

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// KindSwitchNotice is one batched move notice or the daily digest of the Smart Account
// Switch (record.md 4.12, setup.md 8.6 Notices and digest). The hub turns it into a
// session_moved or switch_digest alert (push.md 3.9).
const KindSwitchNotice = "switch_notice"

// Switch notice triggers (record.md 4.12).
const (
	SwitchNoticeMoved  = "session_moved"
	SwitchNoticeDigest = "switch_digest"
	// SwitchNoticePulled: sessions pulled into an owner-drained account (setup.md 8.6, Owner
	// drain), batched like session_moved.
	SwitchNoticePulled = "drain_pulled"
)

// Bounds of a switch_notice payload.
const (
	SwitchNoticeMaxText    = 2000
	SwitchNoticeMaxEpisode = 100
	SwitchNoticeMaxMoves   = 10000
)

// SwitchNoticePayload is the payload of a switch_notice record.
type SwitchNoticePayload struct {
	Trigger string `json:"trigger"`
	// Episode names the notice on this machine (the hub prefixes the machine): the same
	// episode sent twice makes one alert.
	Episode string `json:"episode"`
	SinceAt string `json:"since_at"`
	Moves   int    `json:"moves"`
	Text    string `json:"text"`
	// Direct: the machine already pushed it through direct push (it could not reach the
	// hub); the hub keeps the alert without pushing it again.
	Direct bool `json:"direct,omitempty"`
}

// Validate checks the fields record.md 4.12 requires.
func (p SwitchNoticePayload) Validate() error {
	if p.Trigger != SwitchNoticeMoved && p.Trigger != SwitchNoticeDigest && p.Trigger != SwitchNoticePulled {
		return fmt.Errorf("switch_notice: bad trigger %q", p.Trigger)
	}
	if p.Episode == "" || len(p.Episode) > SwitchNoticeMaxEpisode {
		return fmt.Errorf("switch_notice: bad episode")
	}
	if _, err := time.Parse(time.RFC3339, p.SinceAt); err != nil {
		return fmt.Errorf("switch_notice: bad since_at")
	}
	if p.Moves < 0 || p.Moves > SwitchNoticeMaxMoves || ((p.Trigger == SwitchNoticeMoved || p.Trigger == SwitchNoticePulled) && p.Moves < 1) {
		return fmt.Errorf("switch_notice: bad moves %d", p.Moves)
	}
	if p.Text == "" || len(p.Text) > SwitchNoticeMaxText || !utf8.ValidString(p.Text) {
		return fmt.Errorf("switch_notice: bad text")
	}
	return nil
}

// NewSwitchNotice builds a switch_notice record: no seat and no slot, it is the machine's.
func NewSwitchNotice(m Meta, p SwitchNoticePayload) (*Record, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	m.Seat, m.Slot, m.Term = nil, "", nil
	r := New(KindSwitchNotice, m)
	return r, r.SetPayload(p)
}
