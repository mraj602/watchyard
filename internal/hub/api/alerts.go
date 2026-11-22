package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/push/engine"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Alert log and actions (push.md 4, 8; api.yaml tag alerts). Snooze, seen and action
// tokens go through the engine (model.AlertService); without one they answer 503. Scopes
// are checked by server.go from the route table; /v1/actions/{token} has none: the token
// is its credential.

func init() {
	Register("GET", "/v1/alerts", func(s *Server) http.Handler { return http.HandlerFunc(s.alertList) })
	Register("GET", "/v1/alerts/{id}", func(s *Server) http.Handler { return http.HandlerFunc(s.alertGet) })
	Register("POST", "/v1/alerts/{id}/snooze", func(s *Server) http.Handler { return http.HandlerFunc(s.alertSnooze) })
	Register("POST", "/v1/alerts/{id}/seen", func(s *Server) http.Handler { return http.HandlerFunc(s.alertSeen) })
	Register("POST", "/v1/sessions/{id}/seen", func(s *Server) http.Handler { return http.HandlerFunc(s.sessionSeen) })
	Register("POST", "/v1/actions/{token}", func(s *Server) http.Handler { return http.HandlerFunc(s.alertAction) })
}

const alertMaxBody = 4 << 10

// alertService is the engine as model.AlertService, or nil.
func (s *Server) alertService() model.AlertService {
	if s.cfg.Engine == nil {
		return nil
	}
	as, _ := s.cfg.Engine.(model.AlertService)
	return as
}

// alertHubTZ is the hub's zone: the hub.tz setting, else the machine's zone, else UTC
// (push.md 2, rules.HubZone).
func (s *Server) alertHubTZ(r *http.Request) string {
	z, _ := s.hubZone(r)
	return z
}

// hubZone is the hub's zone and where it came from (setting, system or utc).
func (s *Server) hubZone(r *http.Request) (string, string) {
	return rules.HubZone(s.store.AlertSettingString(r.Context(), store.AlertKeyHubTZ))
}

// alertRender fills the text fields as rendered for a non-private target.
func (s *Server) alertRender(r *http.Request, as []model.Alert) {
	loc := rules.Location(s.alertHubTZ(r))
	for i := range as {
		t := rules.Render(as[i], rules.TextOptions{Loc: loc, Now: s.now()})
		as[i].Title, as[i].URL = t.Title, t.URL
		as[i].Body = nil
		if t.Body != "" {
			b := t.Body
			as[i].Body = &b
		}
	}
}

func (s *Server) alertList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AlertFilter{Session: q.Get("session"), Cursor: q.Get("cursor"), Reason: q.Get("reason"), Limit: 50}
	if v := q.Get("status"); v != "" {
		for _, st := range strings.Split(v, ",") {
			st = strings.TrimSpace(st)
			switch model.Status(st) {
			case model.StatusPending, model.StatusFired, model.StatusSnoozed, model.StatusSeen, model.StatusCancelled,
				model.StatusClosed, model.StatusSkipped:
				f.Statuses = append(f.Statuses, st)
			default:
				auth.WriteError(w, http.StatusBadRequest, "bad_request", "unknown status "+strconv.Quote(st))
				return
			}
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "limit must be 1-200")
			return
		}
		f.Limit = n
	}
	as, next, err := s.store.AlertList(r.Context(), f)
	if errors.Is(err, store.ErrBadCursor) {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "bad cursor")
		return
	}
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	s.alertRender(r, as)
	var nx *string
	if next != "" {
		nx = &next
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": as, "next": nx})
}

// alertWrite answers one alert by id (200), or 404.
func (s *Server) alertWrite(w http.ResponseWriter, r *http.Request, id string) {
	a, err := s.store.AlertByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such alert")
		return
	}
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	one := []model.Alert{a}
	s.alertRender(r, one)
	writeJSON(w, http.StatusOK, one[0])
}

func (s *Server) alertGet(w http.ResponseWriter, r *http.Request) {
	s.alertWrite(w, r, r.PathValue("id"))
}

// alertError maps the engine's sentinel errors to statuses.
func alertError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrNotFound):
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such alert")
	case errors.Is(err, model.ErrConflict):
		auth.WriteError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, model.ErrBadToken):
		auth.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, model.ErrBadMAC):
		auth.WriteError(w, http.StatusUnauthorized, "unauthorized", err.Error())
	case errors.Is(err, model.ErrExpired):
		auth.WriteError(w, http.StatusGone, "gone", err.Error())
	default:
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "could not update the alert; retry")
	}
}

func alertBy(r *http.Request) string {
	if d, ok := auth.DeviceFrom(r.Context()); ok {
		return d.ID
	}
	return ""
}

func (s *Server) alertSnooze(w http.ResponseWriter, r *http.Request) {
	as := s.alertService()
	if as == nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "pushes are off on this hub")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, alertMaxBody+1))
	if err != nil || len(body) > alertMaxBody {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body too large")
		return
	}
	var in struct {
		Minutes *int `json:"minutes"`
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "body must be a JSON object")
			return
		}
	}
	minutes := 0
	if in.Minutes != nil {
		if *in.Minutes < 5 || *in.Minutes > 240 {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "minutes must be 5-240")
			return
		}
		minutes = *in.Minutes
	}
	id := r.PathValue("id")
	if _, err := as.Snooze(r.Context(), id, minutes, alertBy(r)); err != nil {
		alertError(w, err)
		return
	}
	s.alertWrite(w, r, id)
}

func (s *Server) alertSeen(w http.ResponseWriter, r *http.Request) {
	as := s.alertService()
	if as == nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "pushes are off on this hub")
		return
	}
	id := r.PathValue("id")
	if _, err := as.Seen(r.Context(), id, alertBy(r)); err != nil {
		alertError(w, err)
		return
	}
	s.alertWrite(w, r, id)
}

// sessionSeener is the engine's Seen on a session's current episode (push.md 4).
type sessionSeener interface {
	SeenSession(ctx context.Context, sessionID, by string) (model.AlertBrief, error)
}

// sessionSeen is POST /v1/sessions/{id}/seen (push.md 4, "Seen on a session"): for a
// Needs-you row with no alert. Through the engine when there is one; without it (pushes
// off) the same change is made here and its event published on the stream.
func (s *Server) sessionSeen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var b model.AlertBrief
	var err error
	if ss, ok := s.cfg.Engine.(sessionSeener); ok && s.cfg.Engine != nil {
		b, err = ss.SeenSession(r.Context(), id, alertBy(r))
	} else {
		var a model.Alert
		var evs []events.Event
		a, evs, err = engine.SeenSessionNoEngine(r.Context(), s.store, s.now(), id, alertBy(r))
		b = a.AlertBrief
		for _, ev := range evs {
			s.Broker.Publish(ev)
		}
	}
	if errors.Is(err, model.ErrNotFound) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if errors.Is(err, model.ErrConflict) {
		auth.WriteError(w, http.StatusConflict, "conflict", "this session does not need you now")
		return
	}
	if err != nil {
		alertError(w, err)
		return
	}
	s.alertWrite(w, r, b.ID)
}

// alertAction is POST /v1/actions/{token} (push.md 8): the token is the only credential.
func (s *Server) alertAction(w http.ResponseWriter, r *http.Request) {
	if wait := s.actionLim.take(auth.ClientIP(r), s.now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		auth.WriteError(w, http.StatusTooManyRequests, "too_many", "too many action requests from this address")
		return
	}
	as := s.alertService()
	if as == nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "pushes are off on this hub")
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, alertMaxBody)) // ntfy sends "{}"
	b, applied, err := as.Act(r.Context(), r.PathValue("token"))
	if err != nil {
		alertError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert": b, "applied": applied})
}

// Action taps are rare (a person pressing a button); a token-guessing client is not.
const (
	actionsPerWindow = 30
	actionWindow     = time.Minute
)

// ipLimiter is a per-IP sliding-window counter: actionsPerWindow requests per actionWindow.
type ipLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

// take counts a request from ip at now and returns how long it must wait when over the limit.
func (l *ipLimiter) take(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	if len(l.hits) > 1024 { // forget idle addresses so the map stays small
		for k, ts := range l.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > actionWindow {
				delete(l.hits, k)
			}
		}
	}
	cut := now.Add(-actionWindow)
	ts := l.hits[ip]
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= actionsPerWindow {
		l.hits[ip] = ts
		return max(ts[0].Add(actionWindow).Sub(now), time.Second)
	}
	l.hits[ip] = append(ts, now)
	return 0
}
