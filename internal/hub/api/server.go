package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/drainproto"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/version"
	"github.com/mraj602/watchyard/webui"
)

// Config wires the hub's parts together.
type Config struct {
	Store *store.Store
	// WebUI is served at / without authentication (the pages call the API with the
	// board's cookie). Nil serves nothing at /.
	WebUI fs.FS
	// Log receives one line per request. Nil discards.
	Log *log.Logger
	// Now is the clock; nil = time.Now.
	Now func() time.Time
	// PingEvery is the SSE keep-alive interval; 0 = 25 s (api.yaml).
	PingEvery time.Duration

	// Engine is the alert engine (push.md 10): Notify is called after every ingest batch has
	// been published. Nil (tests, --no-push) is fine: nothing is called, and the alert
	// endpoints that need it answer 503.
	Engine Engine
	// Senders are the push channels by kind (webpush, ntfy), for the test-push endpoint.
	// Nil or missing kind: that endpoint answers 503.
	Senders map[string]model.Sender
	// PublicURL is the hub's public origin from `wy hub serve --public-url`; empty falls
	// back to the hub.public_url setting (pairing.md 2.3).
	PublicURL string
	// Puller runs a pull-mode machine's pull now (machines.md 6.2). Nil (tests, --no-pull) is fine:
	// POST /v1/machines/{id}/pull then answers 503.
	Puller Puller
	// Keys is the hub's signing key and the key documents (keys.md). Nil: the key
	// endpoints answer 503 and pairing registers no keys.
	Keys *hubkeys.Service
	// Link is the device link (link.md). Nil: /v1/link and /v1/link/poll answer 503.
	Link Link
	// PeerThreads is the peer relay (peers.md 6.3). Nil: POST
	// /v1/peers/threads/{id}/resume answers 503.
	PeerThreads PeerThreads
	// AppMirror is the mirrored Android app release (phone.md P16.2). Nil: /v1/app/android
	// answers 404.
	AppMirror AppMirror
}

// Engine is what the server needs from the alert engine. Notify must not block (changes
// are merged while the engine is busy) and must not modify ch. Handlers that need more
// type-assert to a wider interface, e.g. model.AlertService.
type Engine interface {
	Notify(ch ingest.Changes)
}

// Server is the hub's HTTP side: ingest, read API, live stream, board.
type Server struct {
	cfg    Config
	store  *store.Store
	Auth   *auth.Authenticator
	Broker *events.Broker
	Ingest *ingest.Ingester

	done     chan struct{}
	doneOnce sync.Once
	uiBuild  string // build hash of cfg.WebUI, "" without one

	actionLim ipLimiter     // POST /v1/actions/{token}, per client IP
	streams   streamSet     // open GET /v1/stream responses, so a revoke ends them at once
	anaSlots  chan struct{} // analytics reports that may run at once (analyticsSlots)
}

// HTTPServer is the http.Server `wy hub serve` runs s.Handler() on. It sets no ReadTimeout
// and no WriteTimeout on purpose: in net/http both bound the whole request, so either would
// cut /v1/stream (a server-sent event response that stays open for hours) at that age. The
// stream bounds each write itself (streamWriteTimeout, armed before every write) and sends a
// ping every PingEvery, so a dead client still frees its goroutine.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// New builds a server over an open store.
func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PingEvery <= 0 {
		cfg.PingEvery = 25 * time.Second
	}
	s := &Server{cfg: cfg, store: cfg.Store, done: make(chan struct{}), anaSlots: make(chan struct{}, analyticsSlots)}
	if cfg.WebUI != nil {
		s.uiBuild = webui.BuildHash(cfg.WebUI) // the same hash webui.Handler puts in sw.js
	}
	s.Auth = auth.New(cfg.Store)
	s.Auth.Now = cfg.Now
	// wy_o_ token hashes are keyed with the pairing pepper (tokens.md 2); read at first use.
	s.Auth.Pepper = func() ([]byte, error) { return pairService(s).Pepper() }
	s.Broker = events.New(cfg.Now)
	s.Ingest = &ingest.Ingester{Store: cfg.Store, Now: cfg.Now, OnChange: s.onChange}
	return s
}

// onChange publishes a batch to the stream, then tells the engine (nil-safe).
func (s *Server) onChange(ch ingest.Changes) {
	s.publish(ch)
	if s.cfg.Engine != nil {
		s.cfg.Engine.Notify(ch)
	}
}

// Store is the hub's store, for handler files.
func (s *Server) Store() *store.Store { return s.store }

// Engine is the configured alert engine, nil when there is none.
func (s *Server) Engine() Engine { return s.cfg.Engine }

// Config is the configuration the server was built with.
func (s *Server) Config() Config { return s.cfg }

// Close ends open streams (call on shutdown; http.Server.Shutdown does not end them).
func (s *Server) Close() { s.doneOnce.Do(func() { close(s.done) }) }

func (s *Server) now() time.Time { return s.cfg.Now() }

// Route is one routed operation (used by the api.yaml route test).
type Route struct {
	Method, Path string // Path in api.yaml form: /v1/sessions/{id}
	Scopes       []string
	Reserved     bool
}

// Routes lists every operation the server routes.
func (s *Server) Routes() []Route {
	read := []string{"read"}
	base := []Route{
		{"GET", "/health", nil, false},
		{"POST", "/v1/session/login", nil, false},
		{"POST", "/v1/session/logout", nil, false},
		{"POST", "/v1/ingest", []string{"write"}, false},
		{"GET", "/v1/overview", read, false},
		{"GET", "/v1/sessions", read, false},
		{"GET", "/v1/sessions/{id}", read, false},
		{"GET", "/v1/accounts", read, false},
		{"GET", "/v1/seats", read, false},
		{"GET", "/v1/accounts/{seat}/windows", read, false},
		{"GET", "/v1/seats/{seat}/windows", read, false},
		{"GET", "/v1/machines", read, false},
		{"GET", "/v1/stream", read, false},
		{"GET", "/v1/analytics/{report}", read, false},
	}
	// The other operations are listed in routes.go, reserved until a handler registers.
	return append(base, handlerRoutesNow()...)
}

// Handler is the whole hub: API, stream, board, with request logging.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	handlers := map[string]http.Handler{
		"/health":                     http.HandlerFunc(s.health),
		"/v1/session/login":           s.Auth.LoginHandler(),
		"/v1/session/logout":          s.Auth.LogoutHandler(),
		"/v1/ingest":                  s.Ingest.Handler(),
		"/v1/overview":                http.HandlerFunc(s.overview),
		"/v1/sessions":                http.HandlerFunc(s.sessions),
		"/v1/sessions/{id}":           http.HandlerFunc(s.session),
		"/v1/accounts":                http.HandlerFunc(s.accounts),
		"/v1/seats":                   http.HandlerFunc(s.accounts),
		"/v1/accounts/{seat}/windows": http.HandlerFunc(s.windows),
		"/v1/seats/{seat}/windows":    http.HandlerFunc(s.windows),
		"/v1/machines":                http.HandlerFunc(s.machines),
		"/v1/stream":                  http.HandlerFunc(s.stream),
		"/v1/analytics/{report}":      http.HandlerFunc(s.analytics),
	}
	for _, rt := range s.Routes() {
		h := handlers[rt.Path]
		if f, ok := registered(rt.Method, rt.Path); ok {
			// Registered from a handler file's init(); wrapped with the table's scopes like
			// every other route. No scopes = the handler authenticates itself.
			h = f(s)
			if rt.Scopes != nil {
				h = s.Auth.Require(auth.Options{}, rt.Scopes...)(h)
			}
		} else if rt.Reserved {
			// Reserved: 501 for everyone, before auth (api.yaml x-reserved).
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth.WriteError(w, http.StatusNotImplemented, "not_implemented", "reserved; not implemented by this hub")
			})
		} else if rt.Scopes != nil {
			h = s.Auth.Require(auth.Options{}, rt.Scopes...)(h)
		}
		mux.Handle(rt.Method+" "+rt.Path, h)
	}
	mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// push.md 8: a GET (a link preview, a prefetch) of an action URL is 405, never an action.
		if strings.HasPrefix(r.URL.Path, "/v1/actions/") && r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			auth.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "action tokens are POST only")
			return
		}
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such endpoint")
	}))
	if s.cfg.WebUI != nil {
		// "/" not "GET /": a method pattern there would conflict with the "/v1/" fallback.
		mux.Handle("/", webui.Handler(s.cfg.WebUI))
	}
	return s.logRequests(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	body := map[string]any{"ok": true, "version": version.Version, "schema": store.LatestVersion(),
		"time": store.Time(s.now().UnixMilli()),
		// machines.md 7.1, 7.2: what this hub reads, so clients never send more.
		"record_v_max": record.MaxVersion, "batch_v": []int{1}, "drain_proto": drainproto.Proto}
	if fr, ok := s.frozenState(ctx); ok {
		body["frozen"] = fr
	}
	if s.uiBuild != "" {
		body["ui"] = s.uiBuild // pwa.md 4: the app compares it with its own to offer a reload
	}
	status := http.StatusOK
	if v, err := s.store.SchemaVersion(ctx); err != nil || s.store.Ping(ctx) != nil {
		body["ok"], status = false, http.StatusServiceUnavailable
	} else {
		body["schema"] = v
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// statusWriter records the status for the request log and keeps Flush working (SSE).
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests logs method, path (never the query: ?token= must not reach a log), status,
// duration and remote address.
func (s *Server) logRequests(next http.Handler) http.Handler {
	if s.cfg.Log == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		s.cfg.Log.Printf("%s %s %d %dB %s %s", r.Method, logPath(r.URL.Path), sw.status, sw.bytes,
			time.Since(start).Round(time.Millisecond), r.RemoteAddr)
	})
}

// logPath is the path as the request log shows it. An action URL's last segment is its
// token, the only credential of POST /v1/actions/{token} (push.md 8), so it is cut.
func logPath(p string) string {
	if strings.HasPrefix(p, "/v1/actions/") {
		return "/v1/actions/…"
	}
	return p
}
