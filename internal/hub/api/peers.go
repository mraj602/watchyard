package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/peerrelay"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/peers"
)

// Peers endpoints (peers.md 5.4, 6.3, 10, 11; api.yaml tag peers): the matrix, the kill
// switch, thread resume and the audit log. The audit holds metadata only; no message text ever reaches it.
// Like the key endpoints they answer 503 when the hub has no signing key.

func init() {
	reg := func(method, path string, h func(*Server, http.ResponseWriter, *http.Request)) {
		Register(method, path, func(s *Server) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if s.cfg.Keys == nil {
					auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "this hub has no signing key; restart `wy hub serve`")
					return
				}
				h(s, w, r)
			})
		})
	}
	reg("GET", "/v1/peers/matrix", (*Server).matrixGet)
	reg("PUT", "/v1/peers/matrix", (*Server).matrixPut)
	reg("POST", "/v1/peers/pause", (*Server).peersPause)
	reg("GET", "/v1/peers/audit", (*Server).peersAudit)
	reg("GET", "/v1/peers", (*Server).peersList)
	reg("GET", "/v1/peers/messages", (*Server).peerMessages)
	reg("POST", "/v1/peers/threads/{id}/resume", (*Server).peerThreadResume)
}

// PeerThreads is what the server needs from the peer relay (peerrelay.Relay).
type PeerThreads interface {
	ResumeThread(ctx context.Context, id, by string) (peerrelay.ResumeResult, error)
}

// peerThreadResume is "Resume thread" (peers.md 6.3, owner scope by the route table): the
// hub resets its own loop guard for the thread and queues peer_thread_resume to both
// machines' agents. A hub-signed resume widens, so a machine with hub_may_widen = false
// refuses it and keeps its own stop until a human prompt (keys.md 5.4).
func (s *Server) peerThreadResume(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PeerThreads == nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "this hub does not relay peer messages")
		return
	}
	id := r.PathValue("id")
	if !peers.ValidThreadID(id) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such thread")
		return
	}
	caller, _ := auth.DeviceFrom(r.Context())
	res, err := s.cfg.PeerThreads.ResumeThread(r.Context(), id, caller.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such thread")
		return
	case errors.Is(err, peerrelay.ErrThreadClosed):
		auth.WriteError(w, http.StatusConflict, "conflict", "the thread is closed; a new message opens a new one")
		return
	case err != nil:
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": id, "was_paused": res.WasPaused, "machines": len(res.Devices)})
}

// peersList is the session directory for viewers (api.yaml PeerSession): every live
// session the hub knows, with whether its machine runs an agent.
func (s *Server) peersList(w http.ResponseWriter, r *http.Request) {
	dir, err := peerrelay.New(s.Store(), s.cfg.Keys, s.Store().Now, nil).Directory(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	out := []map[string]any{}
	for _, d := range dir {
		e := map[string]any{"session": d.Session, "handle": d.Handle, "name": d.Name, "account": d.Account,
			"account_label": nullable(d.AccountLabel), "machine": d.MachineID, "machine_name": d.Machine,
			"project": nullable(d.Project), "state": d.State, "agent": d.Device != nil, "tags": tagList(d.Tags)}
		if d.State == "" {
			e["state"] = "working" // no fold yet: it has only just started
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// tagList is a session's tags for JSON: an empty array rather than null.
func tagList(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// peerMessages is GET /v1/peers/messages: metadata only (ids, accounts, size, status), never
// text, newest first.
func (s *Server) peerMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pq := store.PeerMessageQuery{Thread: q.Get("thread"), Session: q.Get("session"), Limit: 100}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "since is an RFC 3339 time")
			return
		}
		pq.SinceMS = t.UnixMilli()
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "limit is 1 to 500")
			return
		}
		pq.Limit = n
	}
	rows, err := s.Store().PeerMessages(r.Context(), pq)
	if err != nil {
		keysFail(w, err)
		return
	}
	// The sending and receiving machines by name, so the board can say which device is which.
	names := map[string]string{}
	name := func(id string) string {
		if id == "" {
			return ""
		}
		if n, ok := names[id]; ok {
			return n
		}
		d, err := s.Store().DeviceDetailByID(r.Context(), id)
		if err != nil {
			d.Name = ""
		}
		names[id] = d.Name
		return d.Name
	}
	out := []map[string]any{}
	for _, m := range rows {
		e := map[string]any{"id": m.ID, "thread": m.Thread,
			"from": map[string]string{"session": m.FromSession, "device": m.FromDevice, "device_name": name(m.FromDevice), "account": m.FromAcct},
			"to":   map[string]string{"session": m.ToSession, "device": m.ToDevice, "device_name": name(m.ToDevice), "account": m.ToAcct},
			"kind": m.Kind, "size": m.Size, "local": m.Local, "status": m.Status, "detail": nullable(m.Detail),
			"created_at": msTime(m.CreatedAt), "final_at": nil}
		if m.FinalAt != 0 {
			e["final_at"] = msTime(m.FinalAt)
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

// matrixInfo is api.yaml MatrixInfo.
func matrixInfo(rec *hubkeys.MatrixRecord) map[string]any {
	return map[string]any{
		"doc": rec.Signed.Doc, "matrix": rec.Signed.Body, "signer": rec.Signed.Signer,
		"widens": rec.Widens, "made_by": rec.MadeBy, "created_at": msTime(rec.CreatedAt),
	}
}

func (s *Server) matrixGet(w http.ResponseWriter, r *http.Request) {
	rec, err := s.cfg.Keys.MatrixCurrent(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, matrixInfo(rec))
}

// matrixPut takes `doc` (an owner-signed document, any caller with the route's scopes) or
// `edit` (the hub signs; widening needs the owner scope).
func (s *Server) matrixPut(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	var body struct {
		Doc  *keys.Doc           `json:"doc"`
		Edit *hubkeys.MatrixEdit `json:"edit"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	if (body.Doc == nil) == (body.Edit == nil) {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "send exactly one of doc and edit")
		return
	}
	var rec *hubkeys.MatrixRecord
	var err error
	if body.Doc != nil {
		rec, err = s.cfg.Keys.PutOwnerMatrix(r.Context(), *body.Doc, caller.ID)
	} else {
		rec, err = s.cfg.Keys.EditMatrix(r.Context(), *body.Edit, caller.ID, caller.HasScope("owner"))
	}
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, matrixInfo(rec))
}

// peersPause is the kill switch (peers.md 11): a matrix edit of `paused`. Resuming widens, so
// it needs the owner scope (an owner session); the owner key signs a resume at a terminal
// with PUT /v1/peers/matrix instead.
func (s *Server) peersPause(w http.ResponseWriter, r *http.Request) {
	caller, _ := auth.DeviceFrom(r.Context())
	var body struct {
		Paused *bool `json:"paused"`
	}
	if !keysDecode(w, r, &body) {
		return
	}
	if body.Paused == nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "paused is required")
		return
	}
	cur, err := s.cfg.Keys.MatrixCurrent(r.Context())
	if err != nil {
		keysFail(w, err)
		return
	}
	if cur.Signed.Body.Paused == *body.Paused { // already so: nothing to sign
		writeJSON(w, http.StatusOK, matrixInfo(cur))
		return
	}
	rec, err := s.cfg.Keys.EditMatrix(r.Context(), hubkeys.MatrixEdit{Paused: body.Paused}, caller.ID, caller.HasScope("owner"))
	if err != nil {
		keysFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, matrixInfo(rec))
}

func (s *Server) peersAudit(w http.ResponseWriter, r *http.Request) {
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "since is an RFC 3339 time")
			return
		}
		since = t.UnixMilli()
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "limit is 1 to 500")
			return
		}
		limit = n
	}
	rows, err := s.Store().PeerAudit(r.Context(), since, limit)
	if err != nil {
		keysFail(w, err)
		return
	}
	entries := []map[string]any{}
	for _, row := range rows {
		e := map[string]any{"id": row.ID, "at": msTime(row.At), "actor": row.Actor, "event": row.Event, "thread": nil, "msg": nil,
			"detail": json.RawMessage(row.Detail)}
		if row.Thread != "" {
			e["thread"] = row.Thread
		}
		if row.Msg != "" {
			e["msg"] = row.Msg
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
