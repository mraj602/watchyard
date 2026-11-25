package devserver

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tokens and secrets of the mock. Cookie `wy_session` holds one of the three
// tokens (DefaultToken is the plain board login: read only).
const (
	PhoneToken  = "wy_p_devphone00000000000000000000000000000000"
	OwnerToken  = "wy_o_devowner00000000000000000000000000000000"
	PairSecret  = "ZGV2c2VydmVyLXBhaXItc2VjcjE"
	PairCode    = "482913"
	OwnerSecret = "ZGV2c2VydmVyLW93bmVyLXNlYzE"
	// ExpiredOwnerSecret is an owner link made with --ttl 1m that has expired.
	ExpiredOwnerSecret = "ZGV2c2VydmVyLWV4cGlyZWQtbGluaw"
)

type pairState struct {
	created   bool
	kind      string
	createdAt time.Time
	expiresAt time.Time
	claimedAt time.Time
	cancelled bool
	device    M
}

// caller is the authenticated party of a request.
type caller struct {
	token  string
	kind   string // browser | phone | owner
	scopes []string
	cookie bool
}

func (c *caller) has(scopes ...string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, want := range scopes {
		for _, got := range c.scopes {
			if got == want {
				return true
			}
		}
	}
	return false
}

func (s *Server) callerFor(token string, cookie bool) *caller {
	switch token {
	case s.opts.Token:
		return &caller{token: token, kind: "browser", scopes: []string{"read"}, cookie: cookie}
	case PhoneToken:
		return &caller{token: token, kind: "phone", scopes: []string{"read", "push"}, cookie: cookie}
	case OwnerToken:
		return &caller{token: token, kind: "owner", scopes: []string{"owner", "read", "push"}, cookie: cookie}
	}
	return nil
}

// authenticate finds the caller of r, or nil. Revoked tokens do not authenticate.
func (s *Server) authenticate(r *http.Request) *caller {
	if s.opts.NoAuth {
		return s.callerFor(OwnerToken, false)
	}
	var c *caller
	if ck, err := r.Cookie(CookieName); err == nil {
		c = s.callerFor(ck.Value, true)
	}
	if c == nil {
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			c = s.callerFor(tok, false)
		}
	}
	if c == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked[c.token] {
		return nil
	}
	return c
}

// revokedCaller reports whether r carries a known token that was revoked.
func (s *Server) revokedCaller(r *http.Request) bool {
	tok := ""
	if ck, err := r.Cookie(CookieName); err == nil {
		tok = ck.Value
	} else if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		tok = t
	}
	if tok == "" || s.callerFor(tok, false) == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked[tok]
}

// guard authenticates, checks that the caller holds one of scopes (none listed: any
// caller) and applies the cookie rule for state-changing requests.
func (s *Server) guard(h func(http.ResponseWriter, *http.Request, *caller), scopes ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := s.authenticate(r)
		if c == nil {
			if s.revokedCaller(r) {
				// api.yaml Unauthorized: a known token says why it was refused.
				writeJSON(w, 401, M{"error": "this device was removed from the hub", "code": "unauthorized", "reason": "revoked"})
				return
			}
			writeErr(w, 401, "unauthorized", "missing or unknown token")
			return
		}
		if !c.has(scopes...) {
			writeErr(w, 403, "forbidden", "this token lacks the scope for that: "+strings.Join(scopes, " or "))
			return
		}
		if c.cookie && r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeErr(w, 403, "forbidden", "cookie requests that change state must be application/json")
				return
			}
			if o := r.Header.Get("Origin"); o != "" && !strings.HasSuffix(o, "://"+r.Host) {
				writeErr(w, 403, "forbidden", "cookie requests that change state must come from the same origin")
				return
			}
		}
		h(w, r, c)
	}
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func setCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, 400, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) newID(prefix string) string {
	s.seq++
	return prefix + strconv.Itoa(s.seq)
}

// ---- devices of the caller ----------------------------------------------------------

func selfID(c *caller) string {
	switch c.kind {
	case "phone":
		return devPhone
	case "owner":
		return devOwner
	}
	return devBoard
}

func findByID(list []M, id string) (M, int) {
	for i, x := range list {
		if x["id"] == id {
			return x, i
		}
	}
	return nil, -1
}

// ensureDevice returns the caller's own device, creating it in the list when needed
// (mu held).
func (s *Server) ensureDevice(c *caller) M {
	id := selfID(c)
	if d, _ := findByID(s.data.Devices, id); d != nil {
		return d
	}
	now := s.opts.Now()
	var d M
	switch c.kind {
	case "phone":
		d = newDevice(now, id, "phone", "phone "+now.Format("2006-01-02"), "wy_p_…dEv1", []string{"read", "push"}, 0, "pair:pc_dev1")
	case "owner":
		d = newDevice(now, id, "browser", "Chrome · owner session", "wy_o_…dEv2", []string{"owner", "read", "push"}, 0, "owner-link:pc_dev0")
		d["owner_session"] = true
		d["expires_at"] = ts(now.Add(30 * 24 * time.Hour))
	default:
		d = newDevice(now, id, "browser", "Board · browser", "wy_b_…dEv3", []string{"read"}, 0, "login:dev")
	}
	s.data.Devices = append(s.data.Devices, d)
	return d
}

func (s *Server) deviceOut(d M, c *caller) M {
	out := M{}
	for k, v := range d {
		out[k] = v
	}
	out["current"] = d["id"] == selfID(c)
	targets := []M{}
	for _, t := range s.data.Targets {
		if t["device"] == d["id"] {
			targets = append(targets, t)
		}
	}
	out["targets"] = targets
	return out
}

func (s *Server) tokenOfDevice(id string) string {
	switch id {
	case devPhone:
		return PhoneToken
	case devOwner:
		return OwnerToken
	case devBoard:
		return s.opts.Token
	}
	return ""
}

func (s *Server) removeTargetsOf(deviceID string) {
	kept := s.data.Targets[:0:0]
	for _, t := range s.data.Targets {
		if t["device"] != deviceID {
			kept = append(kept, t)
		}
	}
	s.data.Targets = kept
}

// ---- alerts -------------------------------------------------------------------------

func newPromotedAlert(d *Data, sess M, now time.Time, n int) M {
	id := fmt.Sprintf("al_promoted%d", n)
	title := fmt.Sprintf("%v · %v · asked a question", sess["seat_label"], sess["project"])
	a := M{"id": id, "trigger": "asked", "status": "pending", "reason": nil, "due_at": ts(now.Add(60 * time.Second)), "fired_at": nil,
		"fire_count": 0, "snoozed_until": nil, "seen_at": nil, "rule": "rule_asked", "session": sess["id"], "seat": sess["seat"],
		"machine": sess["machine"], "window_key": nil, "title": title, "body": sess["question"],
		"url": "/#/sessions/" + sess["id"].(string) + "?alert=" + id, "created_at": ts(now), "since_at": ts(now), "ended_at": nil,
		"deliveries": []M{}}
	return a
}

// emitAlert publishes a StreamAlert event (mu held).
func (s *Server) emitAlert(change string, a M) {
	s.emitLocked("alert", M{"change": change, "alert": Brief(a), "session": a["session"], "seat": a["seat"], "machine": a["machine"]})
}

func (s *Server) sessionNeeds(a M) bool {
	id, _ := a["session"].(string)
	if id == "" {
		return false
	}
	sess, ok := s.data.Session(id)
	return ok && sess["needs_you"] == true
}

func (s *Server) snoozeAlert(a M, minutes int) bool {
	switch a["status"] {
	case "pending", "fired", "snoozed":
	default:
		return false
	}
	until := ts(s.opts.Now().Add(time.Duration(minutes) * time.Minute))
	a["status"], a["snoozed_until"], a["due_at"] = "snoozed", until, until
	s.data.SyncAlerts()
	s.emitAlert("snoozed", a)
	return true
}

func (s *Server) snoozeMin() int {
	switch v := s.data.Settings["snooze_min"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 15
}

func (s *Server) seenAlert(a M) bool {
	switch a["status"] {
	case "pending", "fired", "snoozed":
	case "skipped":
		if !s.sessionNeeds(a) {
			return false
		}
	default:
		return false
	}
	a["status"], a["seen_at"], a["due_at"], a["snoozed_until"] = "seen", ts(s.opts.Now()), nil, nil
	s.data.SyncAlerts()
	s.emitAlert("seen", a)
	return true
}

// ---- routes -------------------------------------------------------------------------

func (s *Server) deviceRoutes() {
	g := s.guard
	m := s.mux.HandleFunc
	m("GET /v1/me", g(s.me))
	m("POST /v1/session/owner", s.ownerSignIn)

	m("POST /v1/pair/codes", g(s.makePairCode, "owner"))
	m("GET /v1/pair/codes/{id}", g(s.getPairCode, "owner"))
	m("DELETE /v1/pair/codes/{id}", g(s.cancelPairCode, "owner"))
	m("POST /v1/pair/claim", s.claim)
	m("POST /v1/owner-links", g(s.makeOwnerLink, "owner"))

	m("GET /v1/devices", g(s.listDevices, "owner"))
	m("GET /v1/devices/{id}", g(s.getDevice, "owner", "read", "push"))
	m("PATCH /v1/devices/{id}", g(s.patchDevice, "owner", "push"))
	m("DELETE /v1/devices/{id}", g(s.revokeDevice, "owner", "push"))

	m("GET /v1/push/config", g(s.pushConfig, "read", "push", "owner"))
	m("PUT /v1/push/settings", g(s.putSettings, "owner"))
	m("PUT /v1/push/channels/{kind}", g(s.putChannel, "owner"))
	m("GET /v1/push/targets", g(s.listTargets, "push", "owner"))
	m("POST /v1/push/targets", g(s.addTarget, "push", "owner"))
	m("PATCH /v1/push/targets/{id}", g(s.patchTarget, "push", "owner"))
	m("DELETE /v1/push/targets/{id}", g(s.deleteTarget, "push", "owner"))
	m("POST /v1/push/targets/{id}/test", g(s.testTarget, "push", "owner"))

	m("GET /v1/rules", g(s.listRules, "read", "push", "owner"))
	m("POST /v1/rules", g(s.addRule, "owner"))
	m("PUT /v1/rules/{id}", g(s.putRule, "owner"))
	m("DELETE /v1/rules/{id}", g(s.deleteRule, "owner"))

	m("GET /v1/alerts", g(s.listAlerts, "read", "push", "owner"))
	m("GET /v1/alerts/{id}", g(s.getAlert, "read", "push", "owner"))
	m("POST /v1/alerts/{id}/snooze", g(s.snooze, "push", "owner"))
	m("POST /v1/alerts/{id}/seen", g(s.seen, "push", "owner"))
	m("POST /v1/sessions/{id}/seen", g(s.seenSession, "push", "owner"))
	m("POST /v1/actions/{token}", s.action)
}

// ---- me, owner, pairing ---------------------------------------------------------------

func (s *Server) me(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.deviceOut(s.ensureDevice(c), c)
	writeJSON(w, 200, M{"device": d, "owner": c.has("owner"), "hub": M{"version": HubVersion, "public_url": nil}})
}

func (s *Server) ownerSignIn(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Secret string `json:"secret"`
		Name   string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	if len(b.Secret) < 22 {
		writeErr(w, 400, "bad_request", "secret is required")
		return
	}
	if b.Secret == ExpiredOwnerSecret {
		writeJSON(w, 401, M{"error": "this sign-in link has expired", "code": "link_expired",
			"at": time.Now().Add(-2 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"), "ttl_s": 60})
		return
	}
	if b.Secret != OwnerSecret {
		writeErr(w, 401, "link_unknown", "this hub does not know that sign-in link") // api.yaml ownerSignIn
		return
	}
	s.mu.Lock()
	delete(s.revoked, OwnerToken)
	d := s.ensureDevice(&caller{kind: "owner"})
	d["revoked_at"] = nil
	if n := strings.TrimSpace(b.Name); n != "" {
		d["name"] = n
	}
	s.mu.Unlock()
	setCookie(w, OwnerToken, 30*24*3600)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) makePairCode(w http.ResponseWriter, r *http.Request, c *caller) {
	var b struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		TTL  int    `json:"ttl_s"`
	}
	if !decode(w, r, &b) {
		return
	}
	switch b.Kind {
	case "machine", "desktop", "phone", "browser":
	default:
		writeErr(w, 400, "bad_request", "kind must be machine, desktop, phone or browser")
		return
	}
	if b.TTL == 0 {
		b.TTL = 600
	}
	if b.TTL < 60 || b.TTL > 3600 {
		writeErr(w, 400, "bad_request", "ttl_s must be 60 to 3600")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	s.pair = pairState{created: true, kind: b.Kind, createdAt: now, expiresAt: now.Add(time.Duration(b.TTL) * time.Second)}
	url := origin(r) + "/#/pair?s=" + PairSecret + "&c=" + PairCode
	out := M{"id": "pc_dev1", "kind": b.Kind, "name": nilIfEmpty(b.Name), "code": "482-913", "secret": PairSecret, "url": url,
		"expires_at": ts(s.pair.expiresAt), "command": nil, "qr": fakeQR(url)}
	if b.Kind == "machine" {
		out["command"] = "wy connect " + origin(r) + " --code 482-913"
	}
	writeJSON(w, 201, out)
}

// makeOwnerLink is POST /v1/owner-links (pairing.md 4.1): the mock's one owner link
// (OwnerSecret), 10 minutes, tracked as pc_dev1 like a pairing code.
func (s *Server) makeOwnerLink(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	s.pair = pairState{created: true, kind: "owner", createdAt: now, expiresAt: now.Add(10 * time.Minute)}
	url := origin(r) + "/#/owner?s=" + OwnerSecret
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, M{"id": "pc_dev1", "url": url, "expires_at": ts(s.pair.expiresAt), "qr": fakeQR(url)})
}

// fakeQR is a deterministic 29x29 module matrix (finder patterns plus pseudo-random fill)
// inside a 4-module quiet zone. It is not a decodable QR code.
func fakeQR(seed string) M {
	const n, q = 29, 4
	size := n + 2*q
	grid := make([][]byte, size)
	for i := range grid {
		grid[i] = []byte(strings.Repeat("0", size))
	}
	x := uint32(2166136261)
	for _, ch := range []byte(seed) {
		x = (x ^ uint32(ch)) * 16777619
	}
	for y := 0; y < n; y++ {
		for xx := 0; xx < n; xx++ {
			x = x*1664525 + 1013904223
			if x>>24&1 == 1 {
				grid[q+y][q+xx] = '1'
			}
		}
	}
	finder := func(ox, oy int) {
		for y := -1; y <= 7; y++ {
			for xx := -1; xx <= 7; xx++ {
				px, py := ox+xx, oy+y
				if px < 0 || py < 0 || px >= n || py >= n {
					continue
				}
				on := xx >= 0 && xx <= 6 && y >= 0 && y <= 6 && (xx == 0 || xx == 6 || y == 0 || y == 6 || (xx >= 2 && xx <= 4 && y >= 2 && y <= 4))
				v := byte('0')
				if on {
					v = '1'
				}
				grid[q+py][q+px] = v
			}
		}
	}
	finder(0, 0)
	finder(n-7, 0)
	finder(0, n-7)
	rows := make([]string, size)
	for i := range grid {
		rows[i] = string(grid[i])
	}
	return M{"size": size, "rows": rows}
}

func (s *Server) pairStatus(now time.Time) string {
	switch {
	case s.pair.cancelled:
		return "cancelled"
	case !s.pair.claimedAt.IsZero() && !s.pair.claimedAt.Before(s.pair.createdAt):
		return "claimed"
	case now.After(s.pair.expiresAt):
		return "expired"
	}
	return "live"
}

func (s *Server) getPairCode(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pair.created || r.PathValue("id") != "pc_dev1" {
		writeErr(w, 404, "not_found", "no such code")
		return
	}
	st := s.pairStatus(s.opts.Now())
	out := M{"id": "pc_dev1", "kind": s.pair.kind, "status": st, "expires_at": ts(s.pair.expiresAt), "claimed_at": nil, "device": nil}
	if st == "claimed" {
		out["claimed_at"] = ts(s.pair.claimedAt)
		out["device"] = s.pair.device
	}
	writeJSON(w, 200, out)
}

func (s *Server) cancelPairCode(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pair.created || r.PathValue("id") != "pc_dev1" {
		writeErr(w, 404, "not_found", "no such code")
		return
	}
	if s.pairStatus(s.opts.Now()) == "live" {
		s.pair.cancelled = true
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code       string `json:"code"`
		Secret     string `json:"secret"`
		Name       string `json:"name"`
		ExpectKind string `json:"expect_kind"`
		Cookie     bool   `json:"cookie"`
		Client     string `json:"client"`
		Machine    string `json:"machine"`
	}
	if !decode(w, r, &b) {
		return
	}
	if (b.Code == "") == (b.Secret == "") {
		writeErr(w, 400, "bad_request", "give exactly one of code and secret")
		return
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, b.Code)
	if (b.Secret != "" && b.Secret != PairSecret) || (b.Code != "" && digits != PairCode) {
		writeErr(w, 401, "unauthorized", "invalid or expired code")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pair.created && s.pair.kind == "desktop" {
		s.claimDesktop(w, r, b.ExpectKind, b.Name, b.Machine)
		return
	}
	if b.ExpectKind != "" && b.ExpectKind != "phone" && b.ExpectKind != "browser" {
		writeErr(w, 409, "conflict", "this code is for a phone or browser, not a "+b.ExpectKind)
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		name = "phone " + s.opts.Now().Format("2006-01-02")
	}
	if len(name) > 60 {
		name = name[:60]
	}
	delete(s.revoked, PhoneToken)
	s.removeTargetsOf(devPhone)
	if _, i := findByID(s.data.Devices, devPhone); i >= 0 {
		s.data.Devices = append(s.data.Devices[:i], s.data.Devices[i+1:]...)
	}
	dev := s.ensureDevice(&caller{kind: "phone"})
	dev["name"] = name
	now := s.opts.Now()
	s.pair.claimedAt = now
	s.pair.device = M{"id": devPhone, "name": name, "kind": "phone"}
	reply := M{"device": devPhone, "kind": "phone", "name": name, "scopes": []string{"read", "push"}, "machine": nil,
		"hub": M{"url": origin(r), "version": HubVersion}}
	if b.Cookie {
		setCookie(w, PhoneToken, 400*24*3600)
	} else {
		reply["token"] = PhoneToken
	}
	writeJSON(w, 200, reply)
}

// ---- devices -----------------------------------------------------------------------

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDevice(c)
	all := r.URL.Query().Get("all") == "true"
	list := []M{}
	for _, d := range s.data.Devices {
		if d["revoked_at"] != nil && !all {
			continue
		}
		list = append(list, s.deviceOut(d, c))
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i]["created_at"].(string) > list[j]["created_at"].(string) })
	writeJSON(w, 200, M{"devices": list})
}

// resolve finds the device a path names for c; ok=false has already written the reply.
func (s *Server) resolve(w http.ResponseWriter, id string, c *caller) (M, bool) {
	if id == "self" {
		id = selfID(c)
	}
	if id != selfID(c) && !c.has("owner") {
		writeErr(w, 403, "forbidden", "only the owner can reach other devices")
		return nil, false
	}
	s.ensureDevice(c)
	d, _ := findByID(s.data.Devices, id)
	if d == nil {
		writeErr(w, 404, "not_found", "no such device")
		return nil, false
	}
	return d, true
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.resolve(w, r.PathValue("id"), c)
	if !ok {
		return
	}
	writeJSON(w, 200, s.deviceOut(d, c))
}

var tapValues = map[string]bool{"auto": true, "session": true, "claude": true}

// categoryNames are the notification categories (push.md 5.4, model.Categories).
var categoryNames = map[string]bool{"sessions": true, "limits": true, "switch": true, "drain": true, "usage": true, "machines": true}

// parsePrefs validates a DevicePrefs body and fills the defaults.
func parsePrefs(raw map[string]any) (M, bool) {
	out := defaultPrefs()
	for k, v := range raw {
		switch k {
		case "mute":
			arr, ok := v.([]any)
			if !ok {
				return nil, false
			}
			mute := []string{}
			for _, x := range arr {
				str, ok := x.(string)
				if !ok {
					return nil, false
				}
				mute = append(mute, str)
			}
			out["mute"] = mute
		case "mute_categories":
			// As the hub (push.md 5.4): known category names only.
			arr, ok := v.([]any)
			if !ok {
				return nil, false
			}
			cats := []string{}
			for _, x := range arr {
				str, ok := x.(string)
				if !ok || !categoryNames[str] {
					return nil, false
				}
				cats = append(cats, str)
			}
			out["mute_categories"] = cats
		case "quiet_hours":
			if v != nil {
				if _, ok := v.(map[string]any); !ok {
					return nil, false
				}
			}
			out["quiet_hours"] = v
		case "private":
			if _, ok := v.(bool); v != nil && !ok {
				return nil, false
			}
			out["private"] = v
		case "tap":
			str, ok := v.(string)
			if !ok || !tapValues[str] {
				return nil, false
			}
			out["tap"] = str
		case "tz":
			if _, ok := v.(string); v != nil && !ok {
				return nil, false
			}
			out["tz"] = v
		case "widgets":
			if _, ok := v.(bool); !ok {
				return nil, false
			}
			out["widgets"] = v
		default:
			return nil, false
		}
	}
	return out, true
}

func (s *Server) patchDevice(w http.ResponseWriter, r *http.Request, c *caller) {
	var b struct {
		Name  *string        `json:"name"`
		Prefs map[string]any `json:"prefs"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.resolve(w, r.PathValue("id"), c)
	if !ok {
		return
	}
	if b.Name != nil {
		n := strings.TrimSpace(*b.Name)
		if n == "" || len(n) > 60 {
			writeErr(w, 400, "bad_request", "name must be 1 to 60 characters")
			return
		}
		d["name"] = n
	}
	if b.Prefs != nil {
		p, ok := parsePrefs(b.Prefs)
		if !ok {
			writeErr(w, 400, "bad_request", "invalid prefs")
			return
		}
		d["prefs"] = p
	}
	writeJSON(w, 200, s.deviceOut(d, c))
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.resolve(w, r.PathValue("id"), c)
	if !ok {
		return
	}
	self := d["id"] == selfID(c)
	if self && c.kind == "owner" {
		writeErr(w, 409, "conflict", "this is the session you are using; sign out instead")
		return
	}
	d["revoked_at"] = ts(s.opts.Now())
	s.removeTargetsOf(d["id"].(string))
	if tok := s.tokenOfDevice(d["id"].(string)); tok != "" {
		s.revoked[tok] = true
		s.endStreamsLocked(tok)
	}
	if self {
		clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- push: config, settings, channels, targets ----------------------------------------

var (
	vapidOnce sync.Once
	vapidKey  string
)

func vapidPublic() string {
	vapidOnce.Do(func() {
		k, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		vapidKey = base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	})
	return vapidKey
}

func (s *Server) pushConfig(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, M{"vapid_public_key": vapidPublic(), "channels": s.data.Channels, "ntfy_default_server": "https://ntfy.sh"})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, c *caller) {
	var raw map[string]any
	if !decode(w, r, &raw) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := defaultSettings()
	for k := range next {
		if v, ok := raw[k]; ok {
			next[k] = v
		}
	}
	for k := range raw {
		if _, ok := next[k]; !ok {
			writeErr(w, 400, "bad_request", "unknown setting "+k)
			return
		}
	}
	if v, ok := next["snooze_min"].(float64); ok && (v < 5 || v > 240) {
		writeErr(w, 400, "bad_request", "snooze_min must be 5 to 240")
		return
	}
	if v, ok := next["refresh_min"].(float64); ok && (v < 10 || v > 240) {
		writeErr(w, 400, "bad_request", "refresh_min must be 10 to 240")
		return
	}
	s.data.Settings = next
	writeJSON(w, 200, next)
}

func (s *Server) putChannel(w http.ResponseWriter, r *http.Request, c *caller) {
	kind := r.PathValue("kind")
	var b struct {
		Enabled  *bool `json:"enabled"`
		Settings M     `json:"settings"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil {
		writeErr(w, 400, "bad_request", "enabled is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, _ := findBy(s.data.Channels, "kind", kind)
	if ch == nil {
		writeErr(w, 400, "bad_request", "kind must be webpush or ntfy")
		return
	}
	ch["enabled"] = *b.Enabled
	if b.Settings != nil {
		ch["settings"] = b.Settings
	}
	writeJSON(w, 200, ch)
}

func findBy(list []M, key, val string) (M, int) {
	for i, x := range list {
		if x[key] == val {
			return x, i
		}
	}
	return nil, -1
}

func (s *Server) listTargets(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	self := s.ensureDevice(c)["id"]
	out := []M{}
	for _, t := range s.data.Targets {
		if c.has("owner") || t["device"] == self {
			out = append(out, t)
		}
	}
	writeJSON(w, 200, M{"targets": out})
}

var base62 = []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz")

func randomTopic() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = base62[int(b[i])%len(base62)]
	}
	return "wy-" + string(b)
}

var topicRe = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/?"); i >= 0 {
		u = u[:i]
	}
	return u
}

func (s *Server) addTarget(w http.ResponseWriter, r *http.Request, c *caller) {
	var b struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Server  string `json:"server"`
		Topic   string `json:"topic"`
		Token   string `json:"token"`
		Private *bool  `json:"private"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dev := s.ensureDevice(c)
	var devRef any = dev["id"]
	if c.kind == "owner" {
		devRef = nil // an owner's own targets belong to the hub, not a phone
		if b.Kind == "webpush" {
			devRef = dev["id"]
		}
	}
	now := ts(s.opts.Now())
	name := nilIfEmpty(strings.TrimSpace(b.Name))
	switch b.Kind {
	case "webpush":
		if !strings.HasPrefix(b.Endpoint, "https://") || b.Keys.P256dh == "" || b.Keys.Auth == "" {
			writeErr(w, 400, "bad_request", "webpush needs an https endpoint and keys {p256dh, auth}")
			return
		}
		if t, _ := findBy(s.data.Targets, "endpoint", b.Endpoint); t != nil {
			t["device"], t["enabled"], t["status"], t["last_error"], t["last_error_at"] = devRef, true, "new", nil, nil
			if name != nil {
				t["name"] = name
			}
			writeJSON(w, 201, t)
			return
		}
		if name == nil {
			name = dev["name"]
		}
		t := M{"id": s.newID("pt_"), "kind": "webpush", "device": devRef, "name": name, "endpoint_host": hostOf(b.Endpoint),
			"endpoint": b.Endpoint, "server": nil, "topic": nil, "token_set": false, "enabled": true, "status": "new",
			"last_ok_at": nil, "last_error": nil, "last_error_at": nil, "created_at": now}
		s.data.Targets = append(s.data.Targets, t)
		writeJSON(w, 201, t)
	case "ntfy":
		server := strings.TrimRight(b.Server, "/")
		if server == "" {
			server = "https://ntfy.sh"
		}
		if !strings.HasPrefix(server, "https://") && !strings.HasPrefix(server, "http://") {
			writeErr(w, 400, "bad_request", "server must be an http(s) URL")
			return
		}
		topic := b.Topic
		if topic == "" {
			topic = randomTopic()
		} else if !topicRe.MatchString(topic) {
			writeErr(w, 400, "bad_request", "invalid topic")
			return
		}
		private := server == "https://ntfy.sh"
		if b.Private != nil {
			private = *b.Private
		}
		prefs := defaultPrefs()
		prefs["private"] = private
		t := M{"id": s.newID("pt_"), "kind": "ntfy", "device": devRef, "name": name, "endpoint_host": hostOf(server),
			"endpoint": nil, "server": server, "topic": topic, "token_set": b.Token != "", "enabled": true, "status": "new",
			"last_ok_at": nil, "last_error": nil, "last_error_at": nil, "prefs": prefs, "created_at": now}
		s.data.Targets = append(s.data.Targets, t)
		writeJSON(w, 201, t)
	default:
		writeErr(w, 400, "bad_request", "kind must be webpush or ntfy")
	}
}

// target finds a target the caller may touch; ok=false has written the reply.
func (s *Server) target(w http.ResponseWriter, id string, c *caller) (M, bool) {
	t, _ := findBy(s.data.Targets, "id", id)
	if t == nil || (!c.has("owner") && t["device"] != selfID(c)) {
		writeErr(w, 404, "not_found", "no such target")
		return nil, false
	}
	return t, true
}

func (s *Server) patchTarget(w http.ResponseWriter, r *http.Request, c *caller) {
	var b struct {
		Enabled *bool          `json:"enabled"`
		Name    *string        `json:"name"`
		Prefs   map[string]any `json:"prefs"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.target(w, r.PathValue("id"), c)
	if !ok {
		return
	}
	if b.Enabled != nil {
		t["enabled"] = *b.Enabled
		if !*b.Enabled {
			t["status"] = "disabled"
		} else if t["status"] == "disabled" {
			t["status"] = "new"
		}
	}
	if b.Name != nil {
		if n := strings.TrimSpace(*b.Name); n == "" || len(n) > 60 {
			writeErr(w, 400, "bad_request", "name must be 1 to 60 characters")
			return
		} else {
			t["name"] = n
		}
	}
	if b.Prefs != nil {
		p, ok := parsePrefs(b.Prefs)
		if !ok {
			writeErr(w, 400, "bad_request", "invalid prefs")
			return
		}
		t["prefs"] = p
	}
	writeJSON(w, 200, t)
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.target(w, r.PathValue("id"), c)
	if !ok {
		return
	}
	_, i := findBy(s.data.Targets, "id", t["id"].(string))
	s.data.Targets = append(s.data.Targets[:i], s.data.Targets[i+1:]...)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testTarget(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.target(w, r.PathValue("id"), c)
	if !ok {
		return
	}
	id := t["id"].(string)
	now := s.opts.Now()
	if last, ok := s.lastTest[id]; ok && now.Sub(last) < 5*time.Second {
		writeErr(w, 429, "too_many_requests", "one test push per 5 seconds")
		return
	}
	s.lastTest[id] = now
	if t["status"] == "gone" {
		writeJSON(w, 200, M{"outcome": "gone", "http_status": 410, "error": "push service: 410 Gone"})
		return
	}
	t["status"], t["last_ok_at"], t["last_error"], t["last_error_at"] = "ok", ts(now), nil, nil
	writeJSON(w, 200, M{"outcome": "sent", "http_status": 201, "error": nil})
}

// ---- rules ---------------------------------------------------------------------------

var triggers = map[string]bool{"needs_permission": true, "asked": true, "failed": true, "limit_80": true, "limit_95": true, "reset": true, "machine_silent": true}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, M{"rules": s.data.Rules, "settings": s.data.Settings})
}

type ruleInput struct {
	Trigger     *string `json:"trigger"`
	Enabled     *bool   `json:"enabled"`
	DelayS      *int    `json:"delay_s"`
	BypassQuiet *bool   `json:"bypass_quiet"`
	Scope       *struct {
		Seats    []string `json:"seats"`
		Projects []string `json:"projects"`
		Machines []string `json:"machines"`
		Windows  []string `json:"windows"`
	} `json:"scope"`
}

func (in *ruleInput) valid() bool {
	if in.DelayS != nil && (*in.DelayS < 0 || *in.DelayS > 86400) {
		return false
	}
	if in.Trigger != nil && !triggers[*in.Trigger] {
		return false
	}
	return true
}

func (in *ruleInput) scopeM() M {
	m := M{}
	if in.Scope == nil {
		return m
	}
	if len(in.Scope.Seats) > 0 {
		m["seats"] = in.Scope.Seats
	}
	if len(in.Scope.Projects) > 0 {
		m["projects"] = in.Scope.Projects
	}
	if len(in.Scope.Machines) > 0 {
		m["machines"] = in.Scope.Machines
	}
	if len(in.Scope.Windows) > 0 {
		m["windows"] = in.Scope.Windows
	}
	return m
}

// scopeKey is a rule scope as a comparable string: lists as sorted sets, windows only for
// window triggers with empty meaning five_hour+seven_day (push.md 3.7, rules.SameScope).
func scopeKey(trigger string, sc M) string {
	list := func(k string) string {
		var v []string
		switch x := sc[k].(type) {
		case []string:
			v = append(v, x...)
		case []any:
			for _, e := range x {
				v = append(v, fmt.Sprint(e))
			}
		}
		if k == "windows" && len(v) == 0 {
			v = []string{"five_hour", "seven_day"}
		}
		sort.Strings(v)
		v = slices.Compact(v)
		return strings.Join(v, ",")
	}
	key := list("seats") + "|" + list("projects") + "|" + list("machines")
	if trigger == "limit_80" || trigger == "limit_95" || trigger == "reset" {
		key += "|" + list("windows")
	}
	return key
}

// duplicateRule answers 409 when another rule of trigger has scope sc.
func (s *Server) duplicateRule(w http.ResponseWriter, self, trigger string, sc M) bool {
	key := scopeKey(trigger, sc)
	for _, o := range s.data.Rules {
		osc, _ := o["scope"].(M)
		if o["id"] != self && o["trigger"] == trigger && scopeKey(trigger, osc) == key {
			id, _ := o["id"].(string)
			msg := "an override with this trigger and scope already exists (" + id + "); change that one instead"
			if o["base"] == true {
				msg = "this override has the same scope as the base rule; change the base rule (" + id + ") instead"
			}
			writeJSON(w, 409, M{"error": msg, "code": "duplicate_rule", "rule": id})
			return true
		}
	}
	return false
}

func (s *Server) addRule(w http.ResponseWriter, r *http.Request, c *caller) {
	var in ruleInput
	if !decode(w, r, &in) {
		return
	}
	if in.Trigger == nil || !in.valid() {
		writeErr(w, 400, "bad_request", "a valid trigger and delay_s (0 to 86400) are required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.Rules) >= 200 {
		writeErr(w, 400, "bad_request", "at most 200 rules")
		return
	}
	rule := M{"id": s.newID("rule_n"), "trigger": *in.Trigger, "base": false, "enabled": in.Enabled == nil || *in.Enabled, "delay_s": 0,
		"bypass_quiet": in.BypassQuiet != nil && *in.BypassQuiet, "scope": in.scopeM(), "updated_at": ts(s.opts.Now())}
	if in.DelayS != nil {
		rule["delay_s"] = *in.DelayS
	}
	if s.duplicateRule(w, "", *in.Trigger, in.scopeM()) {
		return
	}
	s.data.Rules = append(s.data.Rules, rule)
	writeJSON(w, 201, rule)
}

func (s *Server) putRule(w http.ResponseWriter, r *http.Request, c *caller) {
	var in ruleInput
	if !decode(w, r, &in) {
		return
	}
	if !in.valid() {
		writeErr(w, 400, "bad_request", "invalid delay_s or trigger")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, _ := findBy(s.data.Rules, "id", r.PathValue("id"))
	if rule == nil {
		writeErr(w, 404, "not_found", "no such rule")
		return
	}
	base := rule["base"] == true
	if base && in.Trigger != nil && *in.Trigger != rule["trigger"] {
		writeErr(w, 400, "bad_request", "a base rule's trigger cannot change")
		return
	}
	if in.Scope != nil && !base {
		t, _ := rule["trigger"].(string)
		if in.Trigger != nil {
			t = *in.Trigger
		}
		id, _ := rule["id"].(string)
		if s.duplicateRule(w, id, t, in.scopeM()) {
			return
		}
	}
	if in.Enabled != nil {
		rule["enabled"] = *in.Enabled
	}
	if in.DelayS != nil {
		rule["delay_s"] = *in.DelayS
	}
	if in.BypassQuiet != nil {
		rule["bypass_quiet"] = *in.BypassQuiet
	}
	if in.Scope != nil {
		sc := in.scopeM()
		if base {
			cur, _ := rule["scope"].(M)
			if cur == nil {
				cur = M{}
			}
			if w, ok := sc["windows"]; ok {
				cur["windows"] = w
			}
			sc = cur
		}
		rule["scope"] = sc
	}
	if !base && in.Trigger != nil {
		rule["trigger"] = *in.Trigger
	}
	rule["updated_at"] = ts(s.opts.Now())
	writeJSON(w, 200, rule)
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, i := findBy(s.data.Rules, "id", r.PathValue("id"))
	if rule == nil {
		writeErr(w, 404, "not_found", "no such rule")
		return
	}
	if rule["base"] == true {
		writeErr(w, 409, "conflict", "base rules can only be disabled")
		return
	}
	s.data.Rules = append(s.data.Rules[:i], s.data.Rules[i+1:]...)
	w.WriteHeader(http.StatusNoContent)
}

// ---- alerts --------------------------------------------------------------------------

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request, c *caller) {
	q := r.URL.Query()
	statuses := map[string]bool{}
	for _, st := range strings.Split(q.Get("status"), ",") {
		if st != "" {
			statuses[st] = true
		}
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeErr(w, 400, "bad_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []M{}
	for _, a := range s.data.Alerts {
		if len(statuses) > 0 && !statuses[a["status"].(string)] {
			continue
		}
		if sess := q.Get("session"); sess != "" && a["session"] != sess {
			continue
		}
		if a["reason"] == "baseline" && !(statuses["skipped"] && q.Get("reason") == "baseline") {
			continue
		}
		list = append(list, a)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i]["created_at"].(string) > list[j]["created_at"].(string) })
	if len(list) > limit {
		list = list[:limit]
	}
	writeJSON(w, 200, M{"alerts": list, "next": nil})
}

func (s *Server) getAlert(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data.Alert(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "not_found", "no such alert")
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) snooze(w http.ResponseWriter, r *http.Request, c *caller) {
	var b struct {
		Minutes *int `json:"minutes"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data.Alert(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "not_found", "no such alert")
		return
	}
	minutes := s.snoozeMin()
	if b.Minutes != nil {
		if *b.Minutes < 5 || *b.Minutes > 240 {
			writeErr(w, 400, "bad_request", "minutes must be 5 to 240")
			return
		}
		minutes = *b.Minutes
	}
	if !s.snoozeAlert(a, minutes) {
		writeErr(w, 409, "conflict", "an alert that is "+a["status"].(string)+" cannot be snoozed")
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) seen(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data.Alert(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "not_found", "no such alert")
		return
	}
	if !s.seenAlert(a) {
		writeErr(w, 409, "conflict", "an alert that is "+a["status"].(string)+" cannot be marked seen")
		return
	}
	writeJSON(w, 200, a)
}

// seenSession is POST /v1/sessions/{id}/seen (push.md 4): Seen on the session's alert, or a
// new seen alert when a Needs-you row has none.
func (s *Server) seenSession(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.data.Session(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "not_found", "no such session")
		return
	}
	if sess["needs_you"] != true {
		writeErr(w, 409, "conflict", "this session does not need you now")
		return
	}
	if b, _ := sess["alert"].(M); b != nil {
		a, _ := s.data.Alert(b["id"].(string))
		if a != nil && (a["status"] == "seen" || s.seenAlert(a)) {
			writeJSON(w, 200, a)
			return
		}
	}
	now := s.opts.Now()
	a := newPromotedAlert(s.data, sess, now, len(s.data.Alerts)+1)
	trig := "asked"
	if sess["state"] == "needs_permission" || sess["state"] == "failed" {
		trig = sess["state"].(string)
	}
	a["id"], a["trigger"], a["rule"], a["status"], a["due_at"], a["seen_at"] = fmt.Sprintf("al_sessionseen%d", len(s.data.Alerts)+1), trig, nil, "seen", nil, ts(now)
	a["url"] = "/#/sessions/" + sess["id"].(string) + "?alert=" + a["id"].(string)
	s.data.Alerts = append(s.data.Alerts, a)
	s.data.SyncAlerts()
	s.emitAlert("seen", a)
	writeJSON(w, 200, a)
}

var tokenRe = regexp.MustCompile(`^a1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

// action takes an action token. The mock reads the action from a segment named "snooze"
// or "seen" and applies it to an alert named in the other segment, else to the first
// pending (then fired) alert. A token containing "expired" is 410.
func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !tokenRe.MatchString(tok) {
		writeErr(w, 400, "bad_request", "malformed action token")
		return
	}
	if strings.Contains(tok, "expired") {
		writeErr(w, 410, "gone", "this action link has expired")
		return
	}
	parts := strings.Split(tok, ".")[1:]
	action := ""
	var named M
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range parts {
		switch p {
		case "snooze", "seen":
			action = p
		default:
			if a, ok := s.data.Alert(p); ok {
				named = a
			}
		}
	}
	if action == "" {
		writeErr(w, 400, "bad_request", "unknown action")
		return
	}
	if named == nil {
		for _, st := range []string{"pending", "fired"} {
			for _, a := range s.data.Alerts {
				if named == nil && a["status"] == st {
					named = a
				}
			}
		}
	}
	if named == nil {
		writeErr(w, 404, "not_found", "no such alert")
		return
	}
	applied := false
	if action == "snooze" {
		applied = s.snoozeAlert(named, s.snoozeMin())
	} else {
		applied = s.seenAlert(named)
	}
	writeJSON(w, 200, M{"alert": Brief(named), "applied": applied})
}

// ---- dev helpers ------------------------------------------------------------------------

func (s *Server) devBuild(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Hash string `json:"hash"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.mu.Lock()
	s.buildHash = b.Hash
	s.mu.Unlock()
	writeJSON(w, 200, M{"hash": s.build()})
}

func (s *Server) devReset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = Fixtures(s.opts.Now())
	s.revoked = map[string]bool{}
	s.pair = pairState{}
	s.lastTest = map[string]time.Time{}
	s.peerState = nil
	s.keyState = nil
	s.buildHash = ""
	writeJSON(w, 200, M{"reset": true})
	s.emitLocked("reset", M{})
}
