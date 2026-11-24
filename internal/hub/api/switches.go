package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// GET /v1/switches (setup.md 8.6 Switch log, api.yaml listSwitches): the Smart Account Switch
// log with the summary of the filter. Scopes come from the route table.

func init() {
	Register("GET", "/v1/switches", func(s *Server) http.Handler { return http.HandlerFunc(s.switchList) })
}

func (s *Server) switchList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SwitchFilter{Machine: q.Get("machine"), Account: q.Get("account"), Session: q.Get("session"), Cursor: q.Get("cursor"), Limit: 50}
	bad := func(msg string) { auth.WriteError(w, http.StatusBadRequest, "bad_request", msg) }
	switch q.Get("mode") {
	case "":
	case "auto", "manual":
		v := q.Get("mode") == "auto"
		f.Auto = &v
	default:
		bad("mode must be auto or manual")
		return
	}
	switch q.Get("result") {
	case "":
	case "resumed", "failed":
		v := q.Get("result") == "failed"
		f.Failed = &v
	default:
		bad("result must be resumed or failed")
		return
	}
	for name, dst := range map[string]*int64{"since": &f.Since, "until": &f.Until} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				bad(name + " must be an RFC 3339 time")
				return
			}
			*dst = t.UnixMilli()
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			bad("limit must be 1-200")
			return
		}
		f.Limit = n
	}
	moves, next, sum, err := s.store.SwitchMoves(r.Context(), f)
	if err == store.ErrBadCursor {
		bad("bad cursor")
		return
	}
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	var nx *string
	if next != "" {
		nx = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{"moves": moves, "next": nx, "summary": sum})
}
