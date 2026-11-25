package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// GET /v1/usage (setup.md 8.5, tokens.md 3): each seat's limit windows, recent samples,
// other machines' recent leases, the calling machine's own of the last 5 hours (each with its session's
// burn for its first 5 hours) and its calibration factors for `wy account next`, readable with a machine token. It is the owner's own seat usage,
// percentages and times only: no email, no org name, no session, project or message, and
// places and signed-out folders only of the calling machine. Scopes read or write.
func init() {
	Register("GET", "/v1/usage", func(s *Server) http.Handler { return http.HandlerFunc(s.machineUsage) })
}

// usageDefaultSince and usageMaxSince bound ?since=.
const (
	usageDefaultSince = 26 * time.Hour
	usageMaxSince     = 8 * 24 * time.Hour
	usageMaxSamples   = 5000
	usageLeasesFor    = 7 * 24 * time.Hour
	usageMaxLeases    = 500
	// usageLeaseLife is the lease life: a younger lease with a session id is recent and its
	// burn is measured, for at most usageMaxBurn leases per seat and list.
	usageLeaseLife = 5 * time.Hour
	usageMaxBurn   = 200
)

// UsageDoc is api.yaml UsageDoc.
type UsageDoc struct {
	V       int         `json:"v"`
	Time    store.Time  `json:"time"`
	Machine *string     `json:"machine"`
	Since   store.Time  `json:"since"`
	Seats   []UsageSeat `json:"seats"`
}

// UsageSeat is api.yaml UsageSeat: the allow-listed fields of a seat.
type UsageSeat struct {
	ID           string            `json:"id"`
	Account      string            `json:"account"`
	Org          string            `json:"org"`
	Label        *string           `json:"label"`
	Mode         *string           `json:"mode"`
	DrainUntil   store.Time        `json:"drain_until"`
	Windows      []Window          `json:"windows"`
	Samples      []store.SampleRow `json:"samples"`
	LiveSessions int               `json:"live_sessions"`
	Places       []UsageSlot       `json:"places"`
	SignedOut    []UsageSlot       `json:"signed_out"`
	// Leases are other machines' picks of the last 7 days, oldest first (setup.md 8.5).
	Leases []store.UsageLease `json:"leases"`
	// OwnLeases are the calling machine's picks of the last 5 hours, measured the same way.
	OwnLeases []store.UsageLease `json:"own_leases"`
	// Calibration is the seat's calibration factors as the hub's calibration job stored them
	// (api.yaml Calibration), null before its first run for the seat.
	Calibration json.RawMessage `json:"calibration"`
}

// UsageSlot is a folder of the calling machine: logged in to the seat (places) or signed
// out of it (signed_out, with since).
type UsageSlot struct {
	Slot  string      `json:"slot"`
	Since *store.Time `json:"since,omitempty"`
}

func (s *Server) machineUsage(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	since := now.Add(-usageDefaultSince)
	if t, ok := timeParam(r, "since"); !ok {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "since must be an RFC 3339 time")
		return
	} else if !t.IsZero() {
		since = t
	}
	if since.Before(now.Add(-usageMaxSince)) {
		since = now.Add(-usageMaxSince)
	}
	caller, _ := auth.DeviceFrom(r.Context())
	rows, err := s.store.Seats(r.Context(), true, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	cal, err := s.store.CalibrationFactors(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := UsageDoc{V: 1, Time: store.Time(now.UnixMilli()), Since: store.Time(since.UnixMilli()), Seats: []UsageSeat{}}
	if caller.MachineID != "" {
		m := caller.MachineID
		out.Machine = &m
	}
	burns := store.BurnCache{}
	for _, x := range rows {
		ws, err := s.store.Windows(r.Context(), x.ID, nil, time.Time{}, true)
		if err != nil {
			s.fail(w, err)
			return
		}
		sm, err := s.store.Samples(r.Context(), x.ID, nil, since, usageMaxSamples)
		if err != nil {
			s.fail(w, err)
			return
		}
		q := store.LeaseQuery{Seat: x.ID, Machine: caller.MachineID, Since: now.Add(-usageLeasesFor), Limit: usageMaxLeases,
			Now: now, Life: usageLeaseLife, MaxBurn: usageMaxBurn}
		ls, err := s.store.UsageLeases(r.Context(), q, burns)
		if err != nil {
			s.fail(w, err)
			return
		}
		own := []store.UsageLease{}
		if caller.MachineID != "" {
			q.Own, q.Since = true, now.Add(-usageLeaseLife)
			if own, err = s.store.UsageLeases(r.Context(), q, burns); err != nil {
				s.fail(w, err)
				return
			}
		}
		seat := UsageSeat{ID: x.ID, Account: x.Account, Org: x.Org, Label: x.Label, Mode: x.Mode, DrainUntil: drainOf(x), Windows: windowsOf(ws, now),
			Samples: sm, LiveSessions: x.LiveSessions, Places: []UsageSlot{}, SignedOut: []UsageSlot{}, Leases: ls,
			OwnLeases: own, Calibration: cal[x.ID]}
		if seat.Samples == nil {
			seat.Samples = []store.SampleRow{}
		}
		if caller.MachineID != "" {
			for _, p := range x.Places {
				if p.Machine == caller.MachineID && p.Slot != nil {
					seat.Places = append(seat.Places, UsageSlot{Slot: *p.Slot})
				}
			}
			for _, p := range x.SignedOut {
				if p.Machine == caller.MachineID {
					t := p.Since
					seat.SignedOut = append(seat.SignedOut, UsageSlot{Slot: p.Slot, Since: &t})
				}
			}
		}
		out.Seats = append(out.Seats, seat)
	}
	writeJSON(w, http.StatusOK, out)
}
