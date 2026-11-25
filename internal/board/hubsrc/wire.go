package hubsrc

import "time"

// The slices of the hub's read API (docs/spec/api.yaml) the board uses. They are decoded
// here, not borrowed from internal/hub/api, so the board does not link the hub's store, and
// unknown fields are ignored.

type wireWindow struct {
	Key      string     `json:"key"`
	UsedPct  *float64   `json:"used_pct"`
	ResetsAt *time.Time `json:"resets_at"`
}

type wireSeat struct {
	ID         string       `json:"id"`
	Email      *string      `json:"email"`
	Label      *string      `json:"label"`
	Colour     *string      `json:"colour"`
	Order      int          `json:"order"`
	Hidden     bool         `json:"hidden"`
	Windows    []wireWindow `json:"windows"`
	Mode       *string      `json:"mode"`
	DrainUntil *time.Time   `json:"drain_until"`
}

type wireMachine struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	RemovedAt *time.Time `json:"removed_at"`
}

type wireSession struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	Detail      *string    `json:"detail"`
	NeedsYou    bool       `json:"needs_you"`
	StateSince  time.Time  `json:"state_since"`
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

	MachineStale bool    `json:"machine_stale"`
	Tool         *string `json:"tool,omitempty"`
}

type wireOverview struct {
	Time     time.Time     `json:"time"`
	Machines []wireMachine `json:"machines"`
	StreamID int64         `json:"stream_id"`
}

type wireSessionEvent struct {
	Session wireSession `json:"session"`
	Change  string      `json:"change"`
}

type wireMachineEvent struct {
	Machine wireMachine `json:"machine"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// activity is the time the hub orders a session by within its class.
func (s wireSession) activity() time.Time {
	if s.LastEventAt != nil {
		return *s.LastEventAt
	}
	return s.StateSince
}

// hubLess is the hub's /v1/sessions order: needs-you first, oldest waiting first among them,
// then newest activity first, then id.
func hubLess(a, b wireSession) bool {
	if a.NeedsYou != b.NeedsYou {
		return a.NeedsYou
	}
	if a.NeedsYou && !a.StateSince.Equal(b.StateSince) {
		return a.StateSince.Before(b.StateSince)
	}
	if !a.activity().Equal(b.activity()) {
		return a.activity().After(b.activity())
	}
	return a.ID < b.ID
}

func ended(state string) bool { return state == "ended" || state == "gone" }
