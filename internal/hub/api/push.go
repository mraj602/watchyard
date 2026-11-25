package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/ntfy"
	"github.com/mraj602/watchyard/internal/hub/push/webpush"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Push channel and target endpoints (push.md 6, 7, 13; api.yaml tag push). The sender side is
// in hub/push/webpush, hub/push/ntfy and hub/push/link; this file only validates, stores and
// shapes JSON. The ntfy token goes in and never comes out: the API shows token_set.

func init() {
	Register("GET", "/v1/push/config", func(s *Server) http.Handler {
		return http.HandlerFunc(s.pushConfig)
	})
	Register("PUT", "/v1/push/channels/{kind}", func(s *Server) http.Handler {
		return http.HandlerFunc(s.putChannel)
	})
	Register("GET", "/v1/push/targets", func(s *Server) http.Handler {
		return http.HandlerFunc(s.listTargets)
	})
	Register("POST", "/v1/push/targets", func(s *Server) http.Handler {
		return http.HandlerFunc(s.addTarget)
	})
	Register("PATCH", "/v1/push/targets/{id}", func(s *Server) http.Handler {
		return http.HandlerFunc(s.patchTarget)
	})
	Register("DELETE", "/v1/push/targets/{id}", func(s *Server) http.Handler {
		return http.HandlerFunc(s.deleteTarget)
	})
	Register("POST", "/v1/push/targets/{id}/test", func(s *Server) http.Handler {
		t := &testLimiter{last: map[string]time.Time{}}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.testTarget(w, r, t)
		})
	})
}

const (
	maxPushBody = 16 << 10
	// maxTargetsPerDevice caps what one push-scoped device may register, so a leaked or
	// hostile phone token cannot make the hub fan out to unbounded endpoints.
	maxTargetsPerDevice = 10
	testEvery           = 5 * time.Second
)

// ---- shapes ---------------------------------------------------------------------------

// pushTargetJSON is api.yaml PushTarget. There is no secret field: the ntfy token is
// reported as token_set only.
type pushTargetJSON struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Device       *string      `json:"device"`
	Name         *string      `json:"name"`
	EndpointHost string       `json:"endpoint_host"`
	Endpoint     *string      `json:"endpoint"`
	Server       *string      `json:"server"`
	Topic        *string      `json:"topic"`
	TokenSet     bool         `json:"token_set"`
	Enabled      bool         `json:"enabled"`
	Status       string       `json:"status"`
	LastOKAt     store.Time   `json:"last_ok_at"`
	LastError    *string      `json:"last_error"`
	LastErrorAt  store.Time   `json:"last_error_at"`
	Prefs        *model.Prefs `json:"prefs,omitempty"`
	CreatedAt    store.Time   `json:"created_at"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// targetStatus is api.yaml PushTarget.status.
func targetStatus(t model.Target) string {
	switch {
	case t.GoneAt != 0:
		return "gone"
	case !t.Enabled:
		return "disabled"
	case t.LastError != "" && t.LastErrorAt >= t.LastOKAt:
		return "failing"
	case t.LastOKAt != 0:
		return "ok"
	}
	return "new"
}

func targetJSON(t model.Target) pushTargetJSON {
	out := pushTargetJSON{
		ID: t.ID, Kind: t.Kind, Device: strPtr(t.DeviceID), Name: strPtr(t.Name), Endpoint: strPtr(t.Endpoint),
		TokenSet: t.Secret != "", Enabled: t.Enabled, Status: targetStatus(t),
		LastOKAt: store.Time(t.LastOKAt), LastError: strPtr(t.LastError), LastErrorAt: store.Time(t.LastErrorAt),
		CreatedAt: store.Time(t.CreatedAt),
	}
	switch t.Kind {
	case model.KindNtfy:
		server, topic := ntfy.SplitEndpoint(t.Endpoint)
		out.Server, out.Topic = strPtr(server), strPtr(topic)
		if u, err := url.Parse(server); err == nil {
			out.EndpointHost = u.Host
		}
	default:
		if u, err := url.Parse(t.Endpoint); err == nil {
			out.EndpointHost = u.Hostname()
		}
	}
	if t.Prefs != nil {
		p := *t.Prefs
		if p.Mute == nil {
			p.Mute = []model.Trigger{}
		}
		p.Tap = p.TapMode()
		out.Prefs = &p
	}
	return out
}

type channelJSON struct {
	Kind     string          `json:"kind"`
	Enabled  bool            `json:"enabled"`
	Settings json.RawMessage `json:"settings"`
}

func channelOf(c store.Channel) channelJSON {
	set := c.Settings
	if len(set) == 0 {
		set = json.RawMessage(`{}`)
	}
	return channelJSON{Kind: c.Kind, Enabled: c.Enabled, Settings: set}
}

// ---- helpers --------------------------------------------------------------------------

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPushBody))
	if err := dec.Decode(v); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "body must be a JSON object")
		return false
	}
	return true
}

func badRequest(w http.ResponseWriter, msg string) {
	auth.WriteError(w, http.StatusBadRequest, "bad_request", msg)
}

// caller is the authenticated device and whether it is the owner.
func caller(r *http.Request) (d store.Device, owner bool) {
	d, _ = auth.DeviceFrom(r.Context())
	return d, d.HasScope("owner")
}

// visibleTarget loads a target the caller may act on: the owner any, a push-scoped device
// its own. Others get "not found" so ids do not leak.
func (s *Server) visibleTarget(w http.ResponseWriter, r *http.Request) (model.Target, bool) {
	d, owner := caller(r)
	t, err := s.store.TargetByID(r.Context(), r.PathValue("id"))
	if err != nil || (!owner && t.DeviceID != d.ID) {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(w, err)
			return t, false
		}
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such push target")
		return t, false
	}
	return t, true
}

// webpushHosts is push.settings.webpush_hosts (the default list when unset or unreadable).
func (s *Server) webpushHosts(ctx context.Context) []string {
	raw, err := s.store.PushSetting(ctx, "push.settings")
	if err != nil {
		return model.DefaultWebPushHosts
	}
	var v struct {
		Hosts []string `json:"webpush_hosts"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Hosts == nil {
		return model.DefaultWebPushHosts
	}
	return v.Hosts
}

// hubURL is the hub's public origin: --public-url, the hub.public_url setting, else the
// origin the request came to (pairing.md 2.3).
func (s *Server) hubURL(r *http.Request) string {
	if u := strings.TrimRight(s.cfg.PublicURL, "/"); u != "" {
		return u
	}
	if raw, err := s.store.PushSetting(r.Context(), "hub.public_url"); err == nil {
		var u string
		if json.Unmarshal(raw, &u) == nil && u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if auth.SecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- GET /v1/push/config --------------------------------------------------------------

func (s *Server) pushConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := ""
	if raw, err := s.store.PushSetting(ctx, webpush.SettingVapidPublic); err == nil {
		_ = json.Unmarshal(raw, &key)
	}
	if key == "" { // the engine has not started yet (or push is off): make the keys now
		v, err := webpush.LoadVAPID(ctx, s.store)
		if err != nil {
			s.fail(w, err)
			return
		}
		key = v.PublicKey()
	}
	chans, err := s.store.Channels(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := []channelJSON{}
	for _, c := range chans {
		switch c.Kind {
		case model.KindWebPush, model.KindNtfy:
			out = append(out, channelOf(c))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"vapid_public_key": key, "channels": out, "ntfy_default_server": ntfy.DefaultServer})
}

// ---- PUT /v1/push/channels/{kind} -----------------------------------------------------

func (s *Server) putChannel(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != model.KindWebPush && kind != model.KindNtfy && kind != model.KindLink {
		badRequest(w, "kind must be webpush, ntfy or link")
		return
	}
	var body struct {
		Kind     string          `json:"kind"`
		Enabled  *bool           `json:"enabled"`
		Settings json.RawMessage `json:"settings"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	switch {
	case body.Enabled == nil:
		badRequest(w, "enabled is required")
		return
	case body.Kind != "" && body.Kind != kind:
		badRequest(w, "kind in the body does not match the path")
		return
	}
	var settings json.RawMessage
	if len(body.Settings) > 0 && string(body.Settings) != "null" {
		var obj map[string]any
		if json.Unmarshal(body.Settings, &obj) != nil || obj == nil {
			badRequest(w, "settings must be an object")
			return
		}
		settings = body.Settings
	}
	if kind == model.KindLink {
		if err := checkLinkSettings(settings); err != nil {
			badRequest(w, err.Error())
			return
		}
	}
	c, err := s.store.SetChannel(r.Context(), kind, *body.Enabled, settings)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelOf(c))
}

// ---- GET /v1/push/targets -------------------------------------------------------------

func (s *Server) listTargets(w http.ResponseWriter, r *http.Request) {
	d, owner := caller(r)
	filter := d.ID
	if owner {
		filter = ""
	}
	ts, err := s.store.ListTargets(r.Context(), filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]pushTargetJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, targetJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
}

// ---- POST /v1/push/targets ------------------------------------------------------------

type newTargetBody struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Keys     *struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Server  string `json:"server"`
	Topic   string `json:"topic"`
	Token   string `json:"token"`
	Private *bool  `json:"private"`
}

func (s *Server) addTarget(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, owner := caller(r)
	var b newTargetBody
	if !decodeBody(w, r, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if len([]rune(b.Name)) > 60 {
		badRequest(w, "name is at most 60 characters")
		return
	}
	var t model.Target
	switch b.Kind {
	case model.KindWebPush:
		if b.Endpoint == "" || b.Keys == nil {
			badRequest(w, "webpush needs endpoint and keys {p256dh, auth}")
			return
		}
		if err := webpush.CheckEndpoint(b.Endpoint, s.webpushHosts(ctx)); err != nil {
			badRequest(w, err.Error())
			return
		}
		pub, authSecret, err := webpush.ValidateKeys(b.Keys.P256dh, b.Keys.Auth)
		if err != nil {
			badRequest(w, "keys: "+err.Error())
			return
		}
		name := b.Name
		if name == "" {
			name = "Browser"
		}
		t = model.Target{Kind: model.KindWebPush, DeviceID: d.ID, Name: name, Endpoint: b.Endpoint,
			Keys: &model.Keys{P256dh: pushB64(pub), Auth: pushB64(authSecret)}}
	case model.KindNtfy:
		server, err := ntfy.CheckServer(b.Server, owner)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		topic := b.Topic
		if topic == "" {
			if topic, err = ntfy.NewTopic(); err != nil {
				s.fail(w, err)
				return
			}
		} else if !ntfy.ValidTopic(topic) {
			badRequest(w, "topic must be 1-64 characters of A-Z a-z 0-9 - _")
			return
		}
		private := ntfy.IsPublicServer(server) // push.md 7.1: private by default on ntfy.sh
		if b.Private != nil {
			private = *b.Private
		}
		name := b.Name
		if name == "" {
			name = strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://") + "/" + topic
			if rs := []rune(name); len(rs) > 24 {
				name = string(rs[:23]) + "…"
			}
		}
		t = model.Target{Kind: model.KindNtfy, Name: name, Endpoint: ntfy.JoinEndpoint(server, topic),
			Secret: strings.TrimSpace(b.Token), Prefs: &model.Prefs{Private: &private}}
		if !owner { // a phone's topic belongs to it; the owner's is the hub's
			t.DeviceID = d.ID
		}
	case model.KindLink:
		badRequest(w, "link targets are made by pairing")
		return
	default:
		badRequest(w, "kind must be webpush or ntfy")
		return
	}

	// An endpoint already registered by another live device is not taken over by a
	// push-scoped caller: that would let one phone redirect another's pushes.
	old, err := s.store.TargetByEndpoint(ctx, t.Kind, t.Endpoint)
	if err == nil {
		if !owner && old.DeviceID != d.ID && s.deviceAlive(ctx, old.DeviceID) {
			auth.WriteError(w, http.StatusConflict, "conflict", "this endpoint is registered by another device")
			return
		}
		if t.Kind == model.KindNtfy && b.Token == "" {
			t.Secret = old.Secret // re-adding a topic without a token keeps the stored one
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	if t.DeviceID != "" && (err != nil || old.DeviceID != t.DeviceID) { // a new target for this device
		own, lerr := s.store.ListTargets(ctx, t.DeviceID)
		if lerr != nil {
			s.fail(w, lerr)
			return
		}
		if len(own) >= maxTargetsPerDevice {
			auth.WriteError(w, http.StatusConflict, "conflict",
				"this device already has "+strconv.Itoa(maxTargetsPerDevice)+" push targets; remove one first")
			return
		}
	}
	stored, err := s.store.UpsertTarget(ctx, t)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, targetJSON(stored))
}

func (s *Server) deviceAlive(ctx context.Context, id string) bool {
	if id == "" {
		return true // the owner's own target
	}
	d, err := s.store.DeviceByID(ctx, id)
	return err == nil && d.RevokedAt == 0
}

// ---- PATCH /v1/push/targets/{id} ------------------------------------------------------

// checkMuteCategories validates prefs.mute_categories (push.md 5.5): known categories, none
// twice. "" when valid.
func checkMuteCategories(cs []string) string {
	seen := map[string]bool{}
	for _, c := range cs {
		if !model.ValidCategory(c) {
			return "mute_categories has an unknown category " + c + " (sessions, limits, switch, drain, usage, machines)"
		}
		if seen[c] {
			return "mute_categories lists " + c + " twice"
		}
		seen[c] = true
	}
	return ""
}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func validPrefs(p *model.Prefs) string {
	for _, m := range p.Mute {
		if !m.Valid() {
			return "mute has an unknown trigger"
		}
	}
	if msg := checkMuteCategories(p.MuteCategories); msg != "" {
		return msg
	}
	switch p.Tap {
	case "", model.TapAuto, model.TapSession, model.TapClaude:
	default:
		return "tap must be auto, session or claude"
	}
	if p.TZ != nil {
		if _, err := time.LoadLocation(*p.TZ); err != nil {
			return "tz is not an IANA time zone"
		}
	}
	if q := p.QuietHours; q != nil {
		if !hhmm.MatchString(q.Start) || !hhmm.MatchString(q.End) {
			return "quiet_hours start and end must be HH:MM"
		}
		if q.TZ != "" {
			if _, err := time.LoadLocation(q.TZ); err != nil {
				return "quiet_hours tz is not an IANA time zone"
			}
		}
	}
	return ""
}

func (s *Server) patchTarget(w http.ResponseWriter, r *http.Request) {
	t, ok := s.visibleTarget(w, r)
	if !ok {
		return
	}
	var b struct {
		Enabled *bool        `json:"enabled"`
		Name    *string      `json:"name"`
		Prefs   *model.Prefs `json:"prefs"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	patch := store.TargetPatch{Enabled: b.Enabled}
	if b.Name != nil {
		n := strings.TrimSpace(*b.Name)
		if len([]rune(n)) > 60 {
			badRequest(w, "name is at most 60 characters")
			return
		}
		patch.Name = &n
	}
	if b.Prefs != nil {
		if t.DeviceID != "" {
			badRequest(w, "this target follows its device's preferences; change them with PATCH /v1/devices/{id}")
			return
		}
		if msg := validPrefs(b.Prefs); msg != "" {
			badRequest(w, "prefs: "+msg)
			return
		}
		p := *b.Prefs
		if p.Mute == nil {
			p.Mute = []model.Trigger{}
		}
		p.Tap = p.TapMode()
		patch.Prefs = &p
	}
	out, err := s.store.PatchTarget(r.Context(), t.ID, patch)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, targetJSON(out))
}

// ---- DELETE /v1/push/targets/{id} -----------------------------------------------------

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request) {
	t, ok := s.visibleTarget(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteTarget(r.Context(), t.ID); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- POST /v1/push/targets/{id}/test --------------------------------------------------

// testLimiter allows one test push per target per 5 seconds.
type testLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// take records an attempt at now and returns how long to wait when one ran too recently.
func (l *testLimiter) take(id string, now time.Time) (wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if at, ok := l.last[id]; ok && now.Sub(at) < testEvery && !now.Before(at) {
		return testEvery - now.Sub(at)
	}
	for k, at := range l.last { // forget old entries so the map stays small
		if now.Sub(at) > time.Minute {
			delete(l.last, k)
		}
	}
	l.last[id] = now
	return 0
}

func (s *Server) testTarget(w http.ResponseWriter, r *http.Request, lim *testLimiter) {
	t, ok := s.visibleTarget(w, r)
	if !ok {
		return
	}
	sender := s.cfg.Senders[t.Kind]
	if sender == nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "push is not available on this hub ("+t.Kind+" sender missing)")
		return
	}
	now := s.now()
	if wait := lim.take(t.ID, now); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		auth.WriteError(w, http.StatusTooManyRequests, "too_many", "one test push per 5 seconds per target")
		return
	}
	msg := model.Message{
		Payload: model.Payload{V: model.PayloadVersion, Type: model.TypeTest, Title: "Watchyard",
			Body: "Test push from your hub", Tag: "wy-test", URL: "/#/", Tap: model.TapAuto, TS: model.TimeOf(now),
			Category: model.CategoryTest, Channel: model.Channel(model.CategoryTest)},
		TTL: time.Minute, Urgency: "high", Priority: 3, HubURL: s.hubURL(r),
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res := sender.Send(ctx, t, msg)
	// The attempt is recorded on the target (last_ok / last_error / gone), never in the
	// alert log. A failed bookkeeping write does not change what the user is told.
	if err := s.store.ApplyTargetResult(r.Context(), t.ID, res); err != nil && s.cfg.Log != nil {
		s.cfg.Log.Printf("api: push test: record result: %v", err)
	}
	out := map[string]any{"outcome": res.Outcome, "http_status": nil, "error": nil}
	if res.HTTPStatus != 0 {
		out["http_status"] = res.HTTPStatus
	}
	if res.Err != "" {
		out["error"] = res.Err
	}
	writeJSON(w, http.StatusOK, out)
}

func pushB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
