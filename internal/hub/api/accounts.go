package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// PATCH /v1/accounts/{seat} (desktop.md C10, api.yaml patchSeat). The owner scope is
// enforced by the route table, so a read, write or desktop token gets 403 before this runs.
func init() {
	Register("PATCH", "/v1/accounts/{seat}", func(s *Server) http.Handler { return http.HandlerFunc(s.patchSeat) })
	Register("PUT", "/v1/accounts/{seat}/drain", func(s *Server) http.Handler { return http.HandlerFunc(s.putSeatDrain) })
}

// putSeatDrain is PUT /v1/accounts/{seat}/drain (setup.md 8.5, Owner drain): the owner's
// drain of a seat, the same on every machine, from `wy account drain` on a machine (its
// write token) or the owner. Body {"until": RFC 3339 | null}.
func (s *Server) putSeatDrain(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var body struct {
		Until *string `json:"until"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", `the body must be {"until": "<RFC 3339>"} or {"until": null}`)
		return
	}
	p := store.SeatPatch{DrainSet: true, NowMS: s.now().UnixMilli()}
	if body.Until != nil {
		t, err := time.Parse(time.RFC3339, *body.Until)
		if err != nil {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "until must be RFC 3339")
			return
		}
		ms := t.UnixMilli()
		p.DrainUntil = &ms
	}
	s.applySeatPatch(w, r, r.PathValue("seat"), p)
}

var seatPatchKeys = map[string]bool{"label": true, "colour": true, "order": true, "hidden": true, "mode": true, "drain_until": true}

// decodeSeatPatch reads a SeatPatch, keeping which keys were present (null clears).
func decodeSeatPatch(r *http.Request, w http.ResponseWriter) (store.SeatPatch, bool) {
	var p store.SeatPatch
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil || raw == nil {
		return p, false
	}
	for k, v := range raw {
		if !seatPatchKeys[k] {
			return p, false
		}
		isNull := bytes.Equal(bytes.TrimSpace(v), []byte("null"))
		switch k {
		case "label", "colour":
			var sp *string
			if err := json.Unmarshal(v, &sp); err != nil {
				return p, false
			}
			if k == "label" {
				p.LabelSet, p.Label = true, sp
			} else {
				p.ColourSet, p.Colour = true, sp
			}
		case "mode":
			var sp *string
			if json.Unmarshal(v, &sp) != nil {
				return p, false
			}
			p.ModeSet, p.Mode = true, sp
		case "drain_until":
			var sp *string
			if json.Unmarshal(v, &sp) != nil {
				return p, false
			}
			p.DrainSet = true
			if sp != nil {
				t, err := time.Parse(time.RFC3339, *sp)
				if err != nil {
					return p, false
				}
				ms := t.UnixMilli()
				p.DrainUntil = &ms
			}
		case "order":
			var n int64
			if isNull || json.Unmarshal(v, &n) != nil {
				return p, false
			}
			p.Order = &n
		case "hidden":
			var b bool
			if isNull || json.Unmarshal(v, &b) != nil {
				return p, false
			}
			p.Hidden = &b
		}
	}
	return p, true
}

func (s *Server) patchSeat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("seat")
	p, ok := decodeSeatPatch(r, w)
	if !ok {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "the body must be a JSON object with label, colour, order, hidden, mode or drain_until (RFC 3339)")
		return
	}
	p.NowMS = s.now().UnixMilli()
	s.applySeatPatch(w, r, id, p)
}

// applySeatPatch validates and applies p, then answers with the seat.
func (s *Server) applySeatPatch(w http.ResponseWriter, r *http.Request, id string, p store.SeatPatch) {
	if err := p.Validate(); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error { return tx.PatchSeat(id, p) })
	switch {
	case errors.Is(err, store.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such seat")
		return
	case errors.Is(err, store.ErrBadSeatPatch):
		auth.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	all, err := s.seats(r, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, seat := range all {
		if seat.ID != id {
			continue
		}
		// A `usage` stream event so open boards and the desktop app redraw the label, then
		// the engine (the same publish-and-notify path as ingest).
		s.onChange(ingest.Changes{Seats: map[string]bool{id: true}})
		writeJSON(w, http.StatusOK, seat)
		return
	}
	auth.WriteError(w, http.StatusNotFound, "not_found", "no such seat")
}
