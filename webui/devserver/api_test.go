package devserver

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mraj602/watchyard/webui"
)

// client is a browser-like caller of the mock with its own cookie jar.
type client struct {
	t   *testing.T
	c   *http.Client
	url string
}

func newClient(t *testing.T, ts *httptest.Server) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, c: &http.Client{Jar: jar}, url: ts.URL}
}

func (c *client) do(method, path string, body any) (int, map[string]any, http.Header) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
	}
	req, _ := http.NewRequest(method, c.url+path, rd)
	if body != nil || method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out, res.Header
}

func (c *client) want(code int, method, path string, body any) map[string]any {
	c.t.Helper()
	got, out, _ := c.do(method, path, body)
	if got != code {
		c.t.Fatalf("%s %s = %d (%v), want %d", method, path, got, out, code)
	}
	return out
}

func (c *client) cookie(token string) {
	u, _ := url.Parse(c.url)
	c.c.Jar.SetCookies(u, []*http.Cookie{{Name: CookieName, Value: token, Path: "/"}})
}

func newServer(t *testing.T, static ...fstest.MapFS) (*Server, *httptest.Server) {
	t.Helper()
	fsys := fstest.MapFS{"index.html": {Data: []byte("<html>")}, "sw.js": {Data: []byte("const B='__WY_BUILD__';")}}
	if len(static) > 0 {
		fsys = static[0]
	}
	hub := New(Options{Static: fsys, Dev: true, PingEvery: time.Hour})
	ts := httptest.NewServer(hub.Handler())
	t.Cleanup(ts.Close)
	return hub, ts
}

func TestDeviceFixturesMatchAPISpec(t *testing.T) {
	schemas := loadSchemas(t)
	// Alert is allOf(AlertBrief, own props): check against the union.
	full := &schema{props: map[string]string{}}
	for _, n := range []string{"Alert", "AlertBrief"} {
		full.required = append(full.required, schemas[n].required...)
		for k, v := range schemas[n].props {
			full.props[k] = v
		}
	}
	schemas["AlertFull"] = full

	d := Fixtures(time.Date(2026, 10, 12, 14, 3, 22, 0, time.UTC))
	seen := map[string]bool{}
	each(t, roundTrip(t, d.Alerts), func(a any) {
		check(t, schemas, "AlertFull", "alert", a)
		m := a.(map[string]any)
		seen[m["status"].(string)] = true
		each(t, m["deliveries"], func(x any) { check(t, schemas, "Delivery", "delivery", x) })
	})
	for _, st := range []string{"pending", "fired", "snoozed", "seen", "cancelled", "closed", "skipped"} {
		if !seen[st] {
			t.Errorf("no alert fixture in status %s", st)
		}
	}
	each(t, roundTrip(t, d.Targets), func(x any) { check(t, schemas, "PushTarget", "target", x) })
	each(t, roundTrip(t, d.Rules), func(x any) { check(t, schemas, "Rule", "rule", x) })
	check(t, schemas, "PushSettings", "settings", roundTrip(t, d.Settings))
	each(t, roundTrip(t, d.Channels), func(x any) { check(t, schemas, "Channel", "channel", x) })
	each(t, roundTrip(t, d.Devices), func(x any) {
		check(t, schemas, "Device", "device", x)
		check(t, schemas, "DevicePrefs", "prefs", x.(map[string]any)["prefs"])
	})
	each(t, roundTrip(t, d.Sessions), func(s any) {
		if b, ok := s.(map[string]any)["alert"].(map[string]any); ok {
			check(t, schemas, "AlertBrief", "session.alert", b)
		}
	})
	// Open alerts are on their sessions.
	for id, st := range map[string]string{"c41e0000-0000-4000-8000-000000000003": "pending", "c41e0000-0000-4000-8000-000000000004": "fired", "c41e0000-0000-4000-8000-000000000005": "seen"} {
		s, _ := d.Session(id)
		b, _ := s["alert"].(M)
		if b == nil || b["status"] != st {
			t.Errorf("session %s alert = %v, want %s", id, s["alert"], st)
		}
	}
}

func TestDeviceResponsesMatchAPISpec(t *testing.T) {
	schemas := loadSchemas(t)
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)

	me := owner.want(200, "GET", "/v1/me", nil)
	check(t, schemas, "Me", "me", me)
	check(t, schemas, "Device", "me.device", me["device"])
	pc := owner.want(201, "POST", "/v1/pair/codes", M{"kind": "phone"})
	check(t, schemas, "PairCode", "paircode", pc)
	qr := pc["qr"].(map[string]any)
	if rows := qr["rows"].([]any); int(qr["size"].(float64)) != len(rows) || len(rows[0].(string)) != len(rows) {
		t.Errorf("qr is not square: %v", qr["size"])
	}
	check(t, schemas, "PairCodeStatus", "status", owner.want(200, "GET", "/v1/pair/codes/pc_dev1", nil))
	cfg := owner.want(200, "GET", "/v1/push/config", nil)
	check(t, schemas, "PushConfig", "config", cfg)
	if key, err := base64.RawURLEncoding.DecodeString(cfg["vapid_public_key"].(string)); err != nil || len(key) != 65 || key[0] != 4 {
		t.Errorf("vapid key is not an uncompressed P-256 point: %v %d", err, len(key))
	}
	check(t, schemas, "PushTestResult", "test", owner.want(200, "POST", "/v1/push/targets/"+targetPhone+"/test", nil))
	devs := owner.want(200, "GET", "/v1/devices", nil)
	each(t, devs["devices"], func(x any) { check(t, schemas, "Device", "devices", x) })

	phone := newClient(t, ts)
	reply := phone.want(200, "POST", "/v1/pair/claim", M{"secret": PairSecret, "name": "Phone X · Chrome", "cookie": true, "expect_kind": "phone"})
	check(t, schemas, "PairClaimReply", "claim", reply)
	if _, has := reply["token"]; has {
		t.Error("cookie claim returned the token")
	}
}

func TestClaimSetsPhoneCookie(t *testing.T) {
	_, ts := newServer(t)
	c := newClient(t, ts)
	c.want(401, "GET", "/v1/me", nil)
	c.want(401, "POST", "/v1/pair/claim", M{"code": "111-111"})
	c.want(400, "POST", "/v1/pair/claim", M{})
	c.want(409, "POST", "/v1/pair/claim", M{"secret": PairSecret, "expect_kind": "machine"})
	code, _, hdr := c.do("POST", "/v1/pair/claim", M{"code": "482 913", "name": "iPhone · Safari", "cookie": true, "expect_kind": "phone"})
	if code != 200 || !strings.Contains(hdr.Get("Set-Cookie"), "wy_session="+PhoneToken) || !strings.Contains(hdr.Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("claim = %d, Set-Cookie %q", code, hdr.Get("Set-Cookie"))
	}
	me := c.want(200, "GET", "/v1/me", nil)
	dev := me["device"].(map[string]any)
	if me["owner"] != false || dev["kind"] != "phone" || dev["name"] != "iPhone · Safari" || dev["current"] != true {
		t.Errorf("me = %v", me)
	}
	// Without cookie mode the token comes back in the body.
	c2 := newClient(t, ts)
	if r := c2.want(200, "POST", "/v1/pair/claim", M{"secret": PairSecret}); r["token"] != PhoneToken {
		t.Errorf("token = %v", r["token"])
	}
	// Existing board reads work for the phone.
	c.want(200, "GET", "/v1/overview", nil)
}

func TestOwnerSignInAndScopes(t *testing.T) {
	_, ts := newServer(t)
	board := newClient(t, ts)
	board.want(204, "POST", "/v1/session/login", M{"token": DefaultToken})
	me := board.want(200, "GET", "/v1/me", nil)
	if me["owner"] != false || me["device"].(map[string]any)["kind"] != "browser" {
		t.Errorf("board me = %v", me)
	}
	// A read-only board cannot snooze, make codes, edit rules or push settings.
	board.want(403, "POST", "/v1/alerts/al_pending/snooze", M{})
	board.want(403, "POST", "/v1/alerts/al_pending/seen", nil)
	board.want(403, "POST", "/v1/pair/codes", M{"kind": "phone"})
	board.want(403, "PUT", "/v1/rules/rule_asked", M{"enabled": false})
	board.want(403, "PUT", "/v1/push/settings", M{})
	board.want(403, "GET", "/v1/devices", nil)
	board.want(403, "GET", "/v1/devices/"+devIPad, nil)
	board.want(200, "GET", "/v1/devices/self", nil)
	board.want(200, "GET", "/v1/rules", nil)
	board.want(200, "GET", "/v1/alerts", nil)
	// Cookie requests with a non-JSON body are refused.
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/push/targets", strings.NewReader("x=1"))
	req.Header.Set("Content-Type", "text/plain")
	if res, _ := phone.c.Do(req); res.StatusCode != 403 {
		t.Errorf("text/plain cookie POST = %d, want 403", res.StatusCode)
	}

	owner := newClient(t, ts)
	owner.want(401, "POST", "/v1/session/owner", M{"secret": "wrongwrongwrongwrongwrong"})
	owner.want(400, "POST", "/v1/session/owner", M{"secret": "x"})
	owner.want(204, "POST", "/v1/session/owner", M{"secret": OwnerSecret})
	me = owner.want(200, "GET", "/v1/me", nil)
	d := me["device"].(map[string]any)
	if me["owner"] != true || d["owner_session"] != true || d["expires_at"] == nil {
		t.Errorf("owner me = %v", me)
	}
	owner.want(200, "GET", "/v1/devices", nil)
	owner.want(409, "DELETE", "/v1/devices/self", nil)
	owner.want(409, "DELETE", "/v1/devices/"+devOwner, nil)
}

func TestAlertSnoozeSeenAndStream(t *testing.T) {
	hub, ts := newServer(t)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)

	res, err := phone.c.Get(ts.URL + "/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	events := make(chan string, 16)
	go func() {
		buf := make([]byte, 4096)
		acc := ""
		for {
			n, err := res.Body.Read(buf)
			acc += string(buf[:n])
			for {
				i := strings.Index(acc, "\n\n")
				if i < 0 {
					break
				}
				events <- acc[:i]
				acc = acc[i+2:]
			}
			if err != nil {
				return
			}
		}
	}()

	sn := phone.want(200, "POST", "/v1/alerts/al_pending/snooze", M{})
	if sn["status"] != "snoozed" || sn["snoozed_until"] == nil {
		t.Errorf("snooze = %v", sn)
	}
	deadline := time.After(2 * time.Second)
wait:
	for {
		select {
		case e := <-events:
			if strings.Contains(e, "event: alert") && strings.Contains(e, `"change":"snoozed"`) && strings.Contains(e, "al_pending") {
				break wait
			}
		case <-deadline:
			t.Fatal("no alert stream event")
		}
	}
	// The session's brief follows.
	sess := phone.want(200, "GET", "/v1/sessions/c41e0000-0000-4000-8000-000000000003", nil)
	if b := sess["alert"].(map[string]any); b["status"] != "snoozed" {
		t.Errorf("session alert = %v", b)
	}
	phone.want(400, "POST", "/v1/alerts/al_fired/snooze", M{"minutes": 2})
	phone.want(200, "POST", "/v1/alerts/al_fired/snooze", M{"minutes": 30})
	phone.want(200, "POST", "/v1/alerts/al_fired/seen", nil)
	phone.want(409, "POST", "/v1/alerts/al_fired/snooze", M{}) // seen
	phone.want(409, "POST", "/v1/alerts/al_closed/seen", nil)
	phone.want(409, "POST", "/v1/alerts/al_cancelled/snooze", nil)
	phone.want(404, "POST", "/v1/alerts/nope/seen", nil)
	phone.want(404, "GET", "/v1/alerts/nope", nil)
	// skipped with a closed episode: 409; seen on an open one is allowed (promote below).
	phone.want(409, "POST", "/v1/alerts/al_skipped/seen", nil)

	list := phone.want(200, "GET", "/v1/alerts?status=seen,cancelled", nil)
	if n := len(list["alerts"].([]any)); n != 3 { // al_seen, al_fired (now seen), al_cancelled
		t.Errorf("filtered alerts = %d", n)
	}
	if n := len(phone.want(200, "GET", "/v1/alerts?limit=2", nil)["alerts"].([]any)); n != 2 {
		t.Errorf("limit = %d", n)
	}

	// Promote creates a pending alert and a stream event.
	if code, _, _ := phone.do("POST", "/_dev/promote", nil); code != 200 {
		t.Fatalf("promote = %d", code)
	}
	all := phone.want(200, "GET", "/v1/alerts?status=pending", nil)["alerts"].([]any)
	if len(all) != 1 || !strings.HasPrefix(all[0].(map[string]any)["id"].(string), "al_promoted") {
		t.Errorf("pending alerts after promote = %v", all)
	}
	phone.want(200, "POST", "/v1/alerts/"+all[0].(map[string]any)["id"].(string)+"/seen", nil)

	// Seen on a session (push.md 4): its alert's Seen; 409 when it does not need you.
	if a := phone.want(200, "POST", "/v1/sessions/c41e0000-0000-4000-8000-000000000003/seen", nil); a["id"] != "al_pending" || a["status"] != "seen" {
		t.Errorf("session seen = %v", a)
	}
	phone.want(409, "POST", "/v1/sessions/c41e0000-0000-4000-8000-000000000008/seen", nil)
	phone.want(404, "POST", "/v1/sessions/nope/seen", nil)
	_ = hub
}

func TestActionTokens(t *testing.T) {
	_, ts := newServer(t)
	c := newClient(t, ts) // no credentials: the token is the credential
	c.want(400, "POST", "/v1/actions/not-a-token", nil)
	c.want(400, "POST", "/v1/actions/a1.dev.nothing", nil)
	c.want(410, "POST", "/v1/actions/a1.expired.seen", nil)
	r := c.want(200, "POST", "/v1/actions/a1.dev.snooze", nil)
	if r["applied"] != true || r["alert"].(map[string]any)["status"] != "snoozed" {
		t.Errorf("snooze action = %v", r)
	}
	r = c.want(200, "POST", "/v1/actions/a1.al_snoozed.seen", nil)
	if r["applied"] != true || r["alert"].(map[string]any)["status"] != "seen" {
		t.Errorf("seen action = %v", r)
	}
	r = c.want(200, "POST", "/v1/actions/a1.al_snoozed.seen", nil)
	if r["applied"] != false {
		t.Errorf("second seen applied = %v", r["applied"])
	}
	c.want(404, "GET", "/v1/actions/a1.dev.seen", nil) // GET never acts
}

func TestDevicesAndSelfDelete(t *testing.T) {
	_, ts := newServer(t)
	phone := newClient(t, ts)
	phone.want(200, "POST", "/v1/pair/claim", M{"secret": PairSecret, "name": "Phone", "cookie": true})
	phone.want(403, "GET", "/v1/devices", nil)
	phone.want(403, "PATCH", "/v1/devices/"+devAndroid, M{"name": "x"})
	d := phone.want(200, "PATCH", "/v1/devices/self", M{"prefs": M{"mute": []string{"limit_80"}, "tap": "claude", "tz": "Asia/Kolkata"}})
	p := d["prefs"].(map[string]any)
	if p["tap"] != "claude" || p["private"] != nil || len(p["mute"].([]any)) != 1 {
		t.Errorf("prefs = %v", p)
	}
	phone.want(400, "PATCH", "/v1/devices/self", M{"prefs": M{"tap": "carrier-pigeon"}})
	// Register, upsert, test, remove.
	body := M{"kind": "webpush", "endpoint": "https://fcm.googleapis.com/fcm/send/abc", "keys": M{"p256dh": "BNcR", "auth": "tBHI"}, "name": "Phone"}
	t1 := phone.want(201, "POST", "/v1/push/targets", body)
	t2 := phone.want(201, "POST", "/v1/push/targets", body)
	if t1["id"] != t2["id"] || t1["device"] != devPhone {
		t.Errorf("upsert made %v then %v", t1["id"], t2["id"])
	}
	phone.want(400, "POST", "/v1/push/targets", M{"kind": "webpush", "endpoint": "http://x"})
	if got := phone.want(200, "GET", "/v1/push/targets", nil)["targets"].([]any); len(got) != 1 {
		t.Errorf("phone sees %d targets", len(got))
	}
	phone.want(404, "POST", "/v1/push/targets/"+targetPhone+"/test", nil) // not its own
	phone.want(200, "POST", "/v1/push/targets/"+t1["id"].(string)+"/test", nil)
	phone.want(429, "POST", "/v1/push/targets/"+t1["id"].(string)+"/test", nil)
	nt := phone.want(201, "POST", "/v1/push/targets", M{"kind": "ntfy"})
	if !strings.HasPrefix(nt["topic"].(string), "wy-") || len(nt["topic"].(string)) != 27 || nt["prefs"].(map[string]any)["private"] != true {
		t.Errorf("ntfy target = %v", nt)
	}

	// Owner view and revoke by id.
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	devs := owner.want(200, "GET", "/v1/devices", nil)["devices"].([]any)
	if len(devs) < 6 {
		t.Errorf("owner sees %d devices", len(devs))
	}
	owner.want(204, "DELETE", "/v1/devices/"+devIPad, nil)
	if n := len(owner.want(200, "GET", "/v1/devices?all=true", nil)["devices"].([]any)); n < len(devs) {
		t.Errorf("all=true shows fewer devices (%d < %d)", n, len(devs))
	}
	if n := len(owner.want(200, "GET", "/v1/devices", nil)["devices"].([]any)); n != len(devs)-1 {
		t.Errorf("revoked device still listed")
	}
	owner.want(404, "GET", "/v1/devices/dev_nope", nil)

	// Unpair: cookie cleared, token dead until a new claim.
	code, _, hdr := phone.do("DELETE", "/v1/devices/self", nil)
	if code != 204 || !strings.Contains(hdr.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("self delete = %d %q", code, hdr.Get("Set-Cookie"))
	}
	phone.want(401, "GET", "/v1/me", nil)
	phone.cookie(PhoneToken)
	phone.want(401, "GET", "/v1/overview", nil)
	owner.want(200, "GET", "/v1/me", nil)
	phone.want(200, "POST", "/v1/pair/claim", M{"secret": PairSecret, "cookie": true})
	phone.want(200, "GET", "/v1/me", nil)
}

func TestPairCodeLifecycle(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	owner.want(404, "GET", "/v1/pair/codes/pc_dev1", nil)
	owner.want(400, "POST", "/v1/pair/codes", M{"kind": "toaster"})
	pc := owner.want(201, "POST", "/v1/pair/codes", M{"kind": "machine", "name": "gpu"})
	if !strings.Contains(pc["url"].(string), "/#/pair?s="+PairSecret+"&c="+PairCode) || pc["command"] == nil {
		t.Errorf("pair code = %v", pc)
	}
	if owner.want(200, "GET", "/v1/pair/codes/pc_dev1", nil)["status"] != "live" {
		t.Error("new code is not live")
	}
	phone := newClient(t, ts)
	phone.want(200, "POST", "/v1/pair/claim", M{"code": "482-913", "name": "Phone", "cookie": true})
	st := owner.want(200, "GET", "/v1/pair/codes/pc_dev1", nil)
	if st["status"] != "claimed" || st["device"].(map[string]any)["name"] != "Phone" {
		t.Errorf("status = %v", st)
	}
}

func TestRules(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	r := owner.want(200, "GET", "/v1/rules", nil)
	rules := r["rules"].([]any)
	if len(rules) != 8 || rules[0].(map[string]any)["trigger"] != "needs_permission" || rules[1].(map[string]any)["delay_s"] != float64(60) {
		t.Errorf("rules = %v", rules)
	}
	owner.want(200, "PUT", "/v1/rules/rule_asked", M{"delay_s": 120, "enabled": false})
	owner.want(400, "PUT", "/v1/rules/rule_asked", M{"delay_s": 999999})
	owner.want(409, "DELETE", "/v1/rules/rule_asked", nil)
	n := owner.want(201, "POST", "/v1/rules", M{"trigger": "limit_80", "delay_s": 0, "scope": M{"seats": []string{"seat_client"}}})
	// Duplicate scopes are refused as the hub does (push.md 3.7).
	if e := owner.want(409, "POST", "/v1/rules", M{"trigger": "limit_80", "scope": M{"seats": []string{"seat_client"}}}); e["code"] != "duplicate_rule" || e["rule"] != n["id"] {
		t.Errorf("duplicate = %v", e)
	}
	if e := owner.want(409, "POST", "/v1/rules", M{"trigger": "asked", "delay_s": 300}); e["rule"] != "rule_asked" {
		t.Errorf("base duplicate = %v", e)
	}
	owner.want(409, "PUT", "/v1/rules/rule_o1", M{"scope": M{}})
	owner.want(204, "DELETE", "/v1/rules/"+n["id"].(string), nil)
	owner.want(404, "DELETE", "/v1/rules/"+n["id"].(string), nil)
	s := owner.want(200, "PUT", "/v1/push/settings", M{"snooze_min": 20, "private": true})
	if s["snooze_min"] != float64(20) || s["private"] != true || s["batch_s"] != float64(2) {
		t.Errorf("settings = %v", s)
	}
	owner.want(200, "PUT", "/v1/push/channels/ntfy", M{"kind": "ntfy", "enabled": false})
	owner.want(400, "PUT", "/v1/push/channels/carrier", M{"enabled": true})
	if got := owner.want(200, "POST", "/v1/alerts/al_fired/snooze", M{})["snoozed_until"]; got == nil {
		t.Error("snooze used no time")
	}
}

func TestBuildHashSwitchAndHealth(t *testing.T) {
	hub, ts := newServer(t)
	_ = hub
	c := newClient(t, ts)
	get := func(p string) string {
		res, err := c.c.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	want := webui.BuildHash(fstest.MapFS{"index.html": {Data: []byte("<html>")}, "sw.js": {Data: []byte("const B='__WY_BUILD__';")}})
	if got := get("/sw.js"); got != "const B='"+want+"';" {
		t.Errorf("sw.js = %q, want hash %s", got, want)
	}
	var h map[string]any
	_ = json.Unmarshal([]byte(get("/health")), &h)
	if h["ui"] != want || h["schema"] != float64(3) {
		t.Errorf("health = %v", h)
	}
	c.want(200, "POST", "/_dev/build", M{"hash": "abc123abc123"})
	if got := get("/sw.js"); got != "const B='abc123abc123';" {
		t.Errorf("sw.js after switch = %q", got)
	}
	_ = json.Unmarshal([]byte(get("/health")), &h)
	if h["ui"] != "abc123abc123" {
		t.Errorf("health ui = %v", h["ui"])
	}
	c.want(200, "POST", "/_dev/reset", nil)
	if got := get("/sw.js"); got != "const B='"+want+"';" {
		t.Errorf("sw.js after reset = %q", got)
	}
}
