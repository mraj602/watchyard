package devserver

import (
	"net/http"
	"strings"
	"time"
)

// Desktop mock (desktop.md C10): PATCH /v1/accounts/{seat} and desktop pairing codes.

// DesktopToken is the token a claimed desktop code returns.
const DesktopToken = "wy_d_devdesktop000000000000000000000000000000"

func (s *Server) desktopRoutes() {
	s.mux.HandleFunc("PATCH /v1/accounts/{seat}", s.guard(s.patchSeat, "owner"))
	s.mux.HandleFunc("PUT /v1/seats/order", s.guard(s.putSeatOrder, "owner"))
}

func seatLabelOK(v string) bool {
	if v == "" || len(v) > 24 {
		return false
	}
	for i, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

func seatColourOK(v string) bool {
	if len(v) != 7 || v[0] != '#' {
		return false
	}
	for _, r := range v[1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// patchSeat sets label, colour, order or hidden. A key present with null clears label or
// colour; a missing key leaves it (desktop.md C10). It sets look_src to "ui".
func (s *Server) patchSeat(w http.ResponseWriter, r *http.Request, c *caller) {
	var raw map[string]any
	if !decode(w, r, &raw) {
		return
	}
	if len(raw) == 0 {
		writeErr(w, 400, "bad_request", "no field to change")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seat, _ := findByID(s.data.Seats, r.PathValue("seat"))
	if seat == nil {
		writeErr(w, 404, "not_found", "no such seat")
		return
	}
	next := M{}
	for k, v := range raw {
		switch k {
		case "label", "colour":
			if v == nil {
				next[k] = nil
				break
			}
			str, ok := v.(string)
			if !ok || (k == "label" && !seatLabelOK(str)) || (k == "colour" && !seatColourOK(str)) {
				writeErr(w, 400, "bad_request", k+" is not valid")
				return
			}
			next[k] = str
		case "order":
			n, ok := v.(float64)
			if !ok || n < 0 || n != float64(int64(n)) {
				writeErr(w, 400, "bad_request", "order must be a whole number")
				return
			}
			next[k] = int64(n)
		case "hidden":
			b, ok := v.(bool)
			if !ok {
				writeErr(w, 400, "bad_request", "hidden must be true or false")
				return
			}
			next[k] = b
		case "mode":
			if v == nil {
				next[k] = nil
				break
			}
			if str, ok := v.(string); !ok || (str != "protected" && str != "aggressive" && str != "off") {
				writeErr(w, 400, "bad_request", "mode must be protected, aggressive, off or null")
				return
			}
			next[k] = v
			if v == "off" {
				next["drain_until"] = nil
			}
		case "drain_until":
			// As the hub (store.SeatPatch): RFC 3339 in the next 8 days, or null; never with off.
			if v == nil {
				next[k] = nil
				break
			}
			str, ok := v.(string)
			t, err := time.Parse(time.RFC3339, str)
			if now := s.opts.Now(); !ok || err != nil || !t.After(now) || t.After(now.Add(8*24*time.Hour)) {
				writeErr(w, 400, "bad_request", "drain_until must be in the next 8 days")
				return
			}
			if m, _ := raw["mode"].(string); m == "off" || (raw["mode"] == nil && seat["mode"] == "off") {
				writeErr(w, 400, "bad_request", "an account that is off cannot drain")
				return
			}
			next[k] = t.UTC().Format(time.RFC3339)
		default:
			writeErr(w, 400, "bad_request", "unknown field "+k)
			return
		}
	}
	for k, v := range next {
		seat[k] = v
		if k == "label" || k == "colour" {
			seat["look_src"] = "ui"
		}
	}
	writeJSON(w, 200, seat)
}

// claimDesktop answers a claim of a desktop code (mu held): only expect_kind desktop (or
// none) is accepted, and the answer carries a read and write token for the given machine.
func (s *Server) claimDesktop(w http.ResponseWriter, r *http.Request, expect, name, machine string) {
	if expect != "" && expect != "desktop" {
		writeErr(w, 409, "conflict", "this code is for a desktop, not a "+expect)
		return
	}
	if machine == "" {
		writeErr(w, 400, "bad_request", "a desktop code needs a machine id")
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "desktop " + s.opts.Now().Format("2006-01-02")
	}
	if len(name) > 60 {
		name = name[:60]
	}
	s.pair.claimedAt = s.opts.Now()
	s.pair.device = M{"id": "dev_desktop1", "name": name, "kind": "desktop"}
	writeJSON(w, 200, M{"device": "dev_desktop1", "kind": "desktop", "name": name, "scopes": []string{"write", "read"},
		"machine": machine, "token": DesktopToken, "hub": M{"url": origin(r), "version": HubVersion}})
}
