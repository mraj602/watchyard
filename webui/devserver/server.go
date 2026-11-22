// Package devserver is a mock hub for developing and testing the web board: it serves the
// board's static files plus fixture JSON shaped like docs/spec/api.yaml, a login endpoint
// and a live /v1/stream. It is not the real hub and keeps nothing on disk.
package devserver

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/mraj602/watchyard/webui"
)

// CookieName is the session cookie the board relies on (see POST /v1/session/login).
const CookieName = "wy_session"

// Options configure a mock hub.
type Options struct {
	Static    fs.FS  // board files; required
	Token     string // the one accepted token; default DefaultToken
	NoAuth    bool   // skip authentication
	Now       func() time.Time
	PingEvery time.Duration // stream ping interval; default 25 s
	Dev       bool          // enable /_dev/* helpers and scripted live changes
}

// HubVersion is the version the mock hub reports (/health, /v1/me); machine fixtures compare
// against it.
const HubVersion = "0.5.0"

// DefaultToken is accepted by the login page of a mock hub.
const DefaultToken = "wy_b_devtoken0000000000000000000000000000000000"

// Server is the mock hub. Use Handler to serve it.
type Server struct {
	opts Options
	mu   sync.Mutex
	data *Data
	id   int
	subs map[chan event]*sub // open streams and whose token they belong to
	mux  *http.ServeMux

	// Identity, device and push state (api.go); all guarded by mu.
	revoked   map[string]bool // tokens whose device was revoked (cookie gets 401)
	pair      pairState
	lastTest  map[string]time.Time
	buildHash string // /_dev/build override; "" = hash of the static files
	seq       int

	peerState *peersState // peers.go; made on first use
	keyState  *keysState  // keys.go; made on first use
}

// sub is one open /v1/stream: a revoke closes end, as on the real hub.
type sub struct {
	token string
	end   chan struct{}
}

type event struct {
	id   int
	name string
	data []byte
}

// New builds a mock hub with fixtures relative to now.
func New(o Options) *Server {
	if o.Token == "" {
		o.Token = DefaultToken
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PingEvery == 0 {
		o.PingEvery = 25 * time.Second
	}
	s := &Server{opts: o, data: Fixtures(o.Now()), id: 4100, subs: map[chan event]*sub{}, mux: http.NewServeMux(),
		revoked: map[string]bool{}, lastTest: map[string]time.Time{}}
	s.routes()
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// Data returns the current fixtures (for tests; do not mutate while serving).
func (s *Server) Data() *Data { return s.data }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, M{"ok": true, "version": HubVersion, "schema": 3, "time": ts(s.opts.Now()), "ui": s.build()})
	})
	s.mux.HandleFunc("POST /v1/session/login", s.login)
	s.mux.HandleFunc("POST /v1/session/logout", func(w http.ResponseWriter, r *http.Request) {
		clearCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})
	read := func(f http.HandlerFunc) http.HandlerFunc {
		return s.guard(func(w http.ResponseWriter, r *http.Request, _ *caller) { f(w, r) }, "read")
	}
	s.mux.HandleFunc("GET /v1/overview", read(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, 200, s.data.Overview(s.id))
	}))
	s.mux.HandleFunc("GET /v1/sessions", read(s.listSessions))
	s.mux.HandleFunc("GET /v1/sessions/{id}", read(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d, ok := s.data.SessionDetail(r.PathValue("id"))
		if !ok {
			writeErr(w, 404, "not_found", "no such session")
			return
		}
		writeJSON(w, 200, d)
	}))
	s.mux.HandleFunc("GET /v1/accounts", read(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, 200, M{"accounts": s.data.Seats})
	}))
	s.mux.HandleFunc("GET /v1/machines", read(s.listMachines))
	s.mux.HandleFunc("GET /v1/stream", s.guard(s.stream, "read"))
	s.deviceRoutes()
	s.machineRoutes()
	s.desktopRoutes()
	s.peersRoutes()
	s.keysRoutes()
	s.analyticsRoutes()
	s.relationRoutes()
	if s.opts.Dev {
		s.mux.HandleFunc("POST /_dev/promote", s.promote)
		s.mux.HandleFunc("POST /_dev/build", s.devBuild)
		s.mux.HandleFunc("POST /_dev/reset", s.devReset)
		s.mux.HandleFunc("POST /_dev/pull-states", s.devPullStates)
		s.mux.HandleFunc("POST /_dev/scenario", s.devScenario)
	}
	s.mux.Handle("/", webui.HandlerWithBuild(s.opts.Static, s.build))
}

// build is the current build hash: the /_dev/build override, else the hash of the static
// files (recomputed, so edits served with -dir show up).
func (s *Server) build() string {
	s.mu.Lock()
	h := s.buildHash
	s.mu.Unlock()
	if h != "" {
		return h
	}
	return webui.BuildHash(s.opts.Static)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Token == "" {
		writeErr(w, 400, "bad_request", "expected {\"token\": \"...\"}")
		return
	}
	if body.Token != s.opts.Token {
		writeErr(w, 401, "unauthorized", "missing or unknown token")
		return
	}
	s.mu.Lock()
	delete(s.revoked, s.opts.Token)
	s.ensureDevice(&caller{token: s.opts.Token, kind: "browser"})
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: body.Token, Path: "/", MaxAge: 30 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	includeEnded := r.URL.Query().Get("include_ended") == "true"
	out := []M{}
	for _, x := range s.data.Sessions {
		st := x["state"].(string)
		if (st == "ended" || st == "gone") && !includeEnded {
			continue
		}
		out = append(out, x)
	}
	writeJSON(w, 200, M{"sessions": out, "next": nil})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, c *caller) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "internal", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan event, 32)
	me := &sub{token: c.token, end: make(chan struct{})}
	s.mu.Lock()
	s.subs[ch] = me
	s.id++
	hello := event{s.id, "hello", mustJSON(M{"version": "0.2.0-dev", "schema": 1})}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subs, ch); s.mu.Unlock() }()

	write := func(e event) { fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.id, e.name, e.data); fl.Flush() }
	write(hello)
	tick := time.NewTicker(s.opts.PingEvery)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-me.end:
			return
		case e := <-ch:
			write(e)
		case <-tick.C:
			s.mu.Lock()
			s.id++
			e := event{s.id, "ping", mustJSON(M{"time": ts(s.opts.Now())})}
			s.mu.Unlock()
			write(e)
		}
	}
}

// Emit sends one stream event to every connected board.
func (s *Server) Emit(name string, payload any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(name, payload)
}

// endStreamsLocked closes every open stream of token (a revoked device).
func (s *Server) endStreamsLocked(token string) {
	for ch, x := range s.subs {
		if x.token == token {
			close(x.end)
			delete(s.subs, ch)
		}
	}
}

func (s *Server) emitLocked(name string, payload any) {
	s.id++
	e := event{s.id, name, mustJSON(payload)}
	for ch := range s.subs {
		select {
		case ch <- e:
		default: // slow reader: it will resync on reconnect
		}
	}
}

// promote (dev only) turns the first working session into one that asked a question, as
// if Claude had just stopped to ask, and announces it on the stream.
func (s *Server) promote(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.data.Sessions {
		if x["state"] == "working" {
			x["state"], x["needs_you"], x["detail"] = "asked", true, "question"
			x["state_since"] = ts(s.opts.Now())
			x["question"] = "Live update: should I continue with the refactor?"
			s.seq++
			a := newPromotedAlert(s.data, x, s.opts.Now(), s.seq)
			s.data.Alerts = append(s.data.Alerts, a)
			s.data.SyncAlerts()
			s.emitLocked("session", M{"change": "state", "previous_state": "working", "session": x})
			s.emitAlert("created", a)
			writeJSON(w, 200, M{"promoted": x["id"]})
			return
		}
	}
	writeErr(w, 404, "not_found", "no working session")
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, M{"error": msg, "code": kind})
}
