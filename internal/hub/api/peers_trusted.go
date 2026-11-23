package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/peers"
)

// The trusted sender (peers.md 4.1a; api.yaml listTrustedSenders, revokeTrustedSender):
// Settings → Peers lists each machine's approved Doorbell with its recent use, and the owner
// can revoke it. Approval itself happens only at the machine's terminal.

func init() {
	Register("GET", "/v1/peers/trusted", func(s *Server) http.Handler { return http.HandlerFunc(s.trustedList) })
	Register("POST", "/v1/peers/trusted/revoke", func(s *Server) http.Handler { return http.HandlerFunc(s.trustedRevoke) })
}

// trustedRecentTags is how far back recent_tags looks.
const trustedRecentTags = 7 * 24 * time.Hour

// trustedEntry is api.yaml TrustedSender for one row.
func (s *Server) trustedEntry(r *http.Request, t store.PeerTrustRow, labels map[string]string) (map[string]any, error) {
	now := s.Store().Now()
	zone, _ := s.hubZone(r)
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	n := now.In(loc)
	midnight := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	use, err := s.Store().PeerTrustUse(r.Context(), t.Machine, peers.ServiceDoorbell, midnight.UnixMilli(),
		now.Add(-trustedRecentTags).UnixMilli())
	if err != nil {
		return nil, err
	}
	tags := []map[string]any{}
	for _, u := range use.Tags {
		tags = append(tags, map[string]any{"tag": u.Tag, "at": msTime(u.At)})
	}
	e := map[string]any{"sender": t.Sender, "machine": t.Machine, "machine_name": t.MachineName, "account": t.Account,
		"account_label": nullable(labels[t.Account]), "program": t.Program, "interpreter": t.Interpreter, "venv": t.Venv,
		"approved_at": msTime(t.ApprovedAt), "revoked_at": nil, "revoked_by": nullable(t.RevokedBy), "last_send_at": nil,
		"sent_today": use.Since, "recent_tags": tags, "test_build": t.TestBuild}
	if t.RevokedAt != 0 {
		e["revoked_at"] = msTime(t.RevokedAt)
	}
	if use.LastAt != 0 {
		e["last_send_at"] = msTime(use.LastAt)
	}
	return e, nil
}

func (s *Server) trustedList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store().PeerTrusts(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	labels, err := s.Store().AccountLabels(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	out := []map[string]any{}
	for _, t := range rows {
		e, err := s.trustedEntry(r, t, labels)
		if err != nil {
			keysFail(w, err)
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"senders": out})
}

// trustedRevoke narrows only, so the owner's paired phone (push) may do it as well as an
// owner session (the route table); a read-only sign-in or a machine token may not.
func (s *Server) trustedRevoke(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	var body struct {
		Machine string `json:"machine"`
		Sender  string `json:"sender"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	if body.Machine == "" || body.Sender != peers.TrustDoorbell {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "machine and sender (doorbell) are required")
		return
	}
	t, err := s.Store().RevokePeerTrustOwner(r.Context(), body.Machine, body.Sender, caller.ID)
	if errors.Is(err, store.ErrNotFound) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "The hub has no trusted sender for this machine: its agent never reported one. Revoke it on that machine with `wy peers trust doorbell --off`.")
		return
	}
	if err != nil {
		keysFail(w, err)
		return
	}
	labels, err := s.Store().AccountLabels(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	e, err := s.trustedEntry(r, t, labels)
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}
