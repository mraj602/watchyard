package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

const repo = "../../.."

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

type hub struct {
	t       *testing.T
	path    string
	st      *store.Store
	srv     *api.Server
	http    *httptest.Server
	machine string
	tokens  map[string]string // kind -> token
}

func newMachineID() string { return "m_" + record.NewID(time.Now()) }

// startHub opens (or reopens) a hub on path and serves it.
func startHub(t *testing.T, path string) *hub {
	t.Helper()
	return startHubPing(t, path, time.Hour)
}

// startHubPing is startHub with a chosen SSE ping interval.
func startHubPing(t *testing.T, path string, ping time.Duration) *hub {
	t.Helper()
	return startHubOn(t, path, ping, nil)
}

// startHubClock is startHub on a chosen clock (the ingest clamp and every handler read it).
func startHubClock(t *testing.T, now func() time.Time) *hub {
	t.Helper()
	return startHubOn(t, "", time.Hour, now)
}

func startHubOn(t *testing.T, path string, ping time.Duration, now func() time.Time) *hub {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "hub.db")
	}
	st, err := store.Open(context.Background(), path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(api.Config{Store: st, WebUI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>board")}},
		PingEvery: ping, Now: now})
	h := &hub{t: t, path: path, st: st, srv: srv, http: httptest.NewServer(srv.Handler()), tokens: map[string]string{}}
	t.Cleanup(h.stop)
	return h
}

func (h *hub) stop() {
	if h.http == nil {
		return
	}
	h.srv.Close()
	h.http.Close()
	h.st.Close()
	h.http = nil
}

// token creates a device of kind and returns its token (machine and desktop on h.machine).
func (h *hub) token(kind string) string {
	h.t.Helper()
	if h.machine == "" {
		h.machine = newMachineID()
	}
	c, err := auth.Create(context.Background(), h.st, auth.CreateRequest{Kind: kind, Name: kind + "-" + record.NewID(time.Now())[20:],
		Machine: map[bool]string{true: h.machine}[auth.NeedsMachine(kind)]})
	if err != nil {
		h.t.Fatal(err)
	}
	return c.Token
}

func (h *hub) do(method, path, token string, body []byte, hdr ...string) (int, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.http.URL+path, bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func batch(recs ...[]byte) []byte {
	raws := make([]json.RawMessage, len(recs))
	for i, r := range recs {
		raws[i] = r
	}
	b, _ := json.Marshal(map[string]any{"v": 1, "client": "wy/0.0.0-test", "records": raws})
	return b
}

func (h *hub) ingest(token string, recs ...[]byte) ingest.Reply {
	h.t.Helper()
	code, body := h.do("POST", "/v1/ingest", token, batch(recs...))
	if code != http.StatusOK {
		h.t.Fatalf("ingest: %d %s", code, body)
	}
	var r ingest.Reply
	if err := json.Unmarshal(body, &r); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *hub) get(path, token string, into any) {
	h.t.Helper()
	code, body := h.do("GET", path, token, nil)
	if code != http.StatusOK {
		h.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	if into != nil {
		if err := json.Unmarshal(body, into); err != nil {
			h.t.Fatalf("GET %s: %v", path, err)
		}
	}
}

func (h *hub) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.st.R().QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// hookRecord makes a hook_event record from a raw hook payload.
func hookRecord(t *testing.T, machine string, at time.Time, payload []byte) []byte {
	t.Helper()
	r, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: "~/.claude", Client: "wy/0.0.0-test", Now: at},
		payload, record.HookOptions{LastMessages: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func simpleHook(t *testing.T, machine, session, event string, at time.Time) []byte {
	p, _ := json.Marshal(map[string]any{"session_id": session, "hook_event_name": event, "cwd": "/home/u/proj",
		"transcript_path": "/home/u/.claude/projects/-home-u-proj/" + session + ".jsonl"})
	return hookRecord(t, machine, at, p)
}

// loadRun turns a testdata/hooks/runs file into records 1 s apart.
func loadRun(t *testing.T, machine, file string) [][]byte {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var out [][]byte
	for i := 0; sc.Scan(); i++ {
		var env struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		out = append(out, hookRecord(t, machine, t0.Add(time.Duration(i)*time.Second), env.Payload))
	}
	return out
}

func sessionOf(rec []byte) string {
	var r struct {
		Payload struct {
			SessionID string `json:"session_id"` // the hook's stdin, kept as the payload
		} `json:"payload"`
	}
	_ = json.Unmarshal(rec, &r)
	return r.Payload.SessionID
}

// ---- api.yaml coverage ----

type specOp struct {
	method, path string
	scopes       []string
	reserved     bool
}

// specOps reads the operations from docs/spec/api.yaml (paths: section only).
func specOps(t *testing.T) []specOp {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, "docs/spec/api.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	pathRE := regexp.MustCompile(`^  (/\S*):\s*$`)
	opRE := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	scopeRE := regexp.MustCompile(`^      x-scopes:\s*\[(.*)\]`)
	var out []specOp
	var cur string
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "paths:") {
			in = true
			continue
		}
		if in && line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") {
			break
		}
		if !in {
			continue
		}
		if m := pathRE.FindStringSubmatch(line); m != nil {
			cur = m[1]
		} else if m := opRE.FindStringSubmatch(line); m != nil {
			out = append(out, specOp{method: strings.ToUpper(m[1]), path: cur})
		} else if m := scopeRE.FindStringSubmatch(line); m != nil && len(out) > 0 {
			for _, s := range strings.Split(m[1], ",") {
				if s = strings.TrimSpace(s); s != "" {
					out[len(out)-1].scopes = append(out[len(out)-1].scopes, s)
				}
			}
		} else if strings.HasPrefix(line, "      x-reserved:") && len(out) > 0 {
			out[len(out)-1].reserved = true
		}
	}
	if len(out) < 10 {
		t.Fatalf("parsed only %d operations from api.yaml", len(out))
	}
	return out
}

func TestEveryAPIPathIsRouted(t *testing.T) {
	h := startHub(t, "")
	desktop := h.token("desktop")
	routes := map[string]api.Route{}
	for _, r := range h.srv.Routes() {
		routes[r.Method+" "+r.Path] = r
	}
	fill := strings.NewReplacer("{id}", "x", "{seat}", "x", "{report}", "x")
	for _, op := range specOps(t) {
		key := op.method + " " + op.path
		rt, ok := routes[key]
		if !ok {
			t.Errorf("%s is in api.yaml but not routed", key)
			continue
		}
		delete(routes, key)
		if rt.Reserved != op.reserved {
			t.Errorf("%s: reserved %v, api.yaml says %v", key, rt.Reserved, op.reserved)
		}
		want := append([]string(nil), op.scopes...)
		got := append([]string(nil), rt.Scopes...)
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("%s: scopes %v, api.yaml says %v", key, got, want)
		}
		if op.path == "/v1/stream" {
			continue // long-lived; covered by TestStream
		}
		code, body := h.do(op.method, fill.Replace(op.path), desktop, []byte("{}"))
		if strings.Contains(string(body), "no such endpoint") || code == http.StatusMethodNotAllowed {
			t.Errorf("%s: answered by the fallback (%d %s)", key, code, body)
		}
		if op.reserved && code != http.StatusNotImplemented {
			t.Errorf("%s: reserved but returned %d", key, code)
		}
	}
	for key := range routes {
		if !strings.HasPrefix(key, "GET /v1/seats") { // aliases of /v1/accounts
			t.Errorf("%s is routed but not in api.yaml", key)
		}
	}
	if code, body := h.do("GET", "/v1/nope", desktop, nil); code != http.StatusNotFound || !strings.Contains(string(body), "not_found") {
		t.Errorf("unknown path: %d %s", code, body)
	}
	code, body := h.do("GET", "/", "", nil)
	if code != http.StatusOK || !strings.Contains(string(body), "board") {
		t.Errorf("GET /: %d %s", code, body)
	}
	if code, _ := h.do("GET", "/health", "", nil); code != http.StatusOK {
		t.Errorf("/health: %d", code)
	}
}

// ---- auth ----

func TestScopes(t *testing.T) {
	h := startHub(t, "")
	machine, desktop, phone := h.token("machine"), h.token("desktop"), h.token("phone")
	rec := simpleHook(t, h.machine, "s-scope", "SessionStart", t0)
	cases := []struct {
		name, method, path, token string
		body                      []byte
		want                      int
	}{
		{"machine writes", "POST", "/v1/ingest", machine, batch(rec), 200},
		{"desktop writes", "POST", "/v1/ingest", desktop, batch(rec), 200},
		{"phone cannot write", "POST", "/v1/ingest", phone, batch(rec), 403},
		{"machine cannot read", "GET", "/v1/sessions", machine, nil, 403},
		{"machine cannot stream", "GET", "/v1/stream", machine, nil, 403},
		{"desktop reads", "GET", "/v1/sessions", desktop, nil, 200},
		{"phone reads", "GET", "/v1/overview", phone, nil, 200},
		{"no token", "GET", "/v1/overview", "", nil, 401},
		{"bad token", "POST", "/v1/ingest", "wy_m_" + strings.Repeat("0", 43), batch(rec), 401},
		{"garbage token", "GET", "/v1/machines", "hello", nil, 401},
		// wy connect checks a token with an empty batch: 400 means the token is good.
		{"empty batch", "POST", "/v1/ingest", machine, batch(), 400},
	}
	for _, c := range cases {
		code, body := h.do(c.method, c.path, c.token, c.body)
		if code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, body, c.want)
		}
	}
	// A revoked token stops working at once.
	devs, err := auth.Find(context.Background(), h.st, auth.Hint(phone))
	if err != nil || len(devs) != 1 {
		t.Fatalf("find phone: %v %v", devs, err)
	}
	if err := auth.Revoke(context.Background(), h.st, devs[0].ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.do("GET", "/v1/overview", phone, nil); code != 401 {
		t.Errorf("revoked phone: %d", code)
	}
	// Only the hash is stored.
	if n := h.count(`SELECT count(*) FROM device WHERE token_hash = ?`, []byte(phone)); n != 0 {
		t.Error("raw token stored")
	}
}

func TestBoardLoginCookie(t *testing.T) {
	h := startHub(t, "")
	phone := h.token("phone")
	code, _ := h.do("POST", "/v1/session/login", "", []byte(`{"token":"wy_p_nope"}`), "Content-Type", "application/json")
	if code != 401 {
		t.Fatalf("bad login: %d", code)
	}
	// Login CSRF: a form post (not JSON) or a foreign Origin never logs in, even with a
	// valid token.
	good := []byte(`{"token":"` + phone + `"}`)
	if code, _ := h.do("POST", "/v1/session/login", "", good); code != 403 {
		t.Errorf("no content type: %d", code)
	}
	if code, _ := h.do("POST", "/v1/session/login", "", good, "Content-Type", "text/plain"); code != 403 {
		t.Errorf("text/plain: %d", code)
	}
	if code, _ := h.do("POST", "/v1/session/login", "", good, "Content-Type", "application/json", "Origin", "https://evil.example"); code != 403 {
		t.Errorf("foreign origin: %d", code)
	}
	if code, _ := h.do("POST", "/v1/session/login", "", good, "Content-Type", "application/json", "Origin", h.http.URL); code != 204 {
		t.Errorf("same origin: %d", code)
	}
	body, _ := json.Marshal(map[string]string{"token": phone})
	resp, err := http.Post(h.http.URL+"/v1/session/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", cookie)
	}
	with := func(method, path string, b []byte) int {
		req, _ := http.NewRequest(method, h.http.URL+path, bytes.NewReader(b))
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := with("GET", "/v1/overview", nil); c != 200 {
		t.Errorf("overview with cookie: %d", c)
	}
	if c := with("POST", "/v1/ingest", batch(simpleHook(t, newMachineID(), "s", "SessionStart", t0))); c != 401 && c != 403 {
		t.Errorf("ingest with cookie: %d", c)
	}
	if c := with("POST", "/v1/session/logout", nil); c != 204 {
		t.Errorf("logout: %d", c)
	}
	if c := with("GET", "/v1/overview", nil); c != 401 {
		t.Errorf("overview after logout: %d", c)
	}
}

// ---- ingest ----

func TestIngestIdempotentAndIDConflict(t *testing.T) {
	h := startHub(t, "")
	a := h.token("machine")
	rec := simpleHook(t, h.machine, "s-idem", "SessionStart", t0)
	r1 := h.ingest(a, rec)
	if len(r1.Accepted) != 1 || len(r1.Duplicates) != 0 || len(r1.Rejected) != 0 {
		t.Fatalf("first: %+v", r1)
	}
	r2 := h.ingest(a, rec, rec)
	if len(r2.Accepted) != 1 || len(r2.Duplicates) != 1 || len(r2.Rejected) != 0 {
		t.Fatalf("resend: %+v", r2)
	}
	if n := h.count(`SELECT count(*) FROM record`); n != 1 {
		t.Fatalf("%d records stored", n)
	}
	if n := h.count(`SELECT count(*) FROM hook_event`); n != 1 {
		t.Fatalf("%d hook events", n)
	}
	// The same id from another device (on its own machine) is a conflict.
	other := startHubDevice(t, h, "machine")
	var env map[string]any
	_ = json.Unmarshal(rec, &env)
	env["machine"] = other.machine
	moved, _ := json.Marshal(env)
	r3 := h.ingest(other.token, moved)
	if len(r3.Rejected) != 1 || r3.Rejected[0].Reason != ingest.IDConflict {
		t.Fatalf("other device: %+v", r3)
	}
	// One bad record does not reject the batch; the raw record is kept whole.
	good := simpleHook(t, h.machine, "s-idem", "UserPromptSubmit", t0.Add(time.Second))
	var g map[string]any
	_ = json.Unmarshal(good, &g)
	g["iguana_necktie"] = map[string]any{"kept": true}
	good, _ = json.Marshal(g)
	r4 := h.ingest(a, []byte(`{"v":1,"id":"nope"}`), good, simpleHook(t, newMachineID(), "s-x", "Stop", t0))
	if len(r4.Accepted) != 1 || len(r4.Rejected) != 2 {
		t.Fatalf("mixed batch: %+v", r4)
	}
	reasons := []string{r4.Rejected[0].Reason, r4.Rejected[1].Reason}
	sort.Strings(reasons)
	if reasons[0] != "bad_id" || reasons[1] != "machine_mismatch" {
		t.Errorf("reasons %v", reasons)
	}
	var raw string
	if err := h.st.R().QueryRow(`SELECT body FROM record WHERE id = ?`, g["id"]).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "iguana_necktie") {
		t.Error("unknown field dropped from the raw record")
	}
}

type otherDevice struct{ machine, token string }

func startHubDevice(t *testing.T, h *hub, kind string) otherDevice {
	m := newMachineID()
	c, err := auth.Create(context.Background(), h.st, auth.CreateRequest{Kind: kind, Name: "other-" + m[20:], Machine: m})
	if err != nil {
		t.Fatal(err)
	}
	return otherDevice{machine: m, token: c.Token}
}

// The hook runs folded through ingest end in the states internal/hub/state expects, after
// every event, whether the records arrive in order, all at once or backwards.
func TestFixtureRunsThroughIngest(t *testing.T) {
	want := map[string][]string{
		"01-write-and-bash":    {"done", "working", "working", "working", "working", "working", "done", "ended"},
		"02-permission-needed": {"done", "working", "working", "needs_permission", "done", "ended"},
		"03-question":          {"done", "working", "asked", "ended"},
		"04-subagent":          {"done", "working", "working", "working", "working", "working", "done", "ended"},
		"05-tool-failure":      {"done", "working", "working", "working", "done", "ended"},
		"06-stop-failure":      {"done", "working", "failed", "ended"},
		"07-compact":           {"done", "done", "ended"},
		"08-plain-with-env":    {"done", "working", "done", "ended"},
	}
	files, _ := filepath.Glob(filepath.Join(repo, "testdata/hooks/runs/*.jsonl"))
	if len(files) != len(want) {
		t.Fatalf("%d runs", len(files))
	}
	for _, order := range []string{"one-by-one", "one-batch", "reversed"} {
		t.Run(order, func(t *testing.T) {
			h := startHub(t, "")
			tok := h.token("machine")
			desktop := h.token("desktop")
			for _, f := range files {
				name := strings.TrimSuffix(filepath.Base(f), ".jsonl")
				recs := loadRun(t, h.machine, f)
				switch order {
				case "one-by-one":
					for _, r := range recs {
						h.ingest(tok, r)
					}
				case "one-batch":
					h.ingest(tok, recs...)
				case "reversed":
					for i := len(recs) - 1; i >= 0; i-- {
						h.ingest(tok, recs[i])
					}
				}
				sid := sessionOf(recs[0])
				rows, err := h.st.R().Query(`SELECT coalesce(state_after, '') FROM hook_event WHERE session_id = ? ORDER BY ts, record_id`, sid)
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for rows.Next() {
					var s string
					_ = rows.Scan(&s)
					got = append(got, s)
				}
				rows.Close()
				if strings.Join(got, " ") != strings.Join(want[name], " ") {
					t.Errorf("%s: states %v, want %v", name, got, want[name])
				}
				var detail struct {
					State  string  `json:"state"`
					Detail *string `json:"detail"`
				}
				h.get("/v1/sessions/"+sid, desktop, &detail)
				if detail.State != "ended" || detail.Detail == nil || *detail.Detail != "other" {
					t.Errorf("%s: final %s %v", name, detail.State, detail.Detail)
				}
			}
		})
	}
}

// A hub restarted in the middle of a stream of records loses nothing and stores nothing
// twice when the client resends the batch it never got a reply for.
func TestRestartMidStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	h := startHub(t, path)
	tok := h.token("machine")
	machine := h.machine
	files, _ := filepath.Glob(filepath.Join(repo, "testdata/hooks/runs/*.jsonl"))
	var all [][]byte
	for _, f := range files {
		all = append(all, loadRun(t, machine, f)...)
	}
	half := len(all) / 2
	h.ingest(tok, all[:half]...)
	h.stop()

	h2 := startHub(t, path)
	r := h2.ingest(tok, all[half-5:]...) // the client resends from its last acknowledged point
	if len(r.Duplicates) != 5 || len(r.Accepted) != len(all)-half+5 {
		t.Fatalf("after restart: %d accepted, %d duplicates", len(r.Accepted), len(r.Duplicates))
	}
	if n := h2.count(`SELECT count(*) FROM record`); n != len(all) {
		t.Errorf("%d records, want %d", n, len(all))
	}
	if n := h2.count(`SELECT count(*) FROM hook_event`); n != len(all) {
		t.Errorf("%d hook events, want %d", n, len(all))
	}
	if n := h2.count(`SELECT count(*) FROM session_state WHERE state = 'ended'`); n != len(files) {
		t.Errorf("%d ended sessions, want %d", n, len(files))
	}
	if n := h2.count(`SELECT count(*) FROM record WHERE typed = 0 OR type_error IS NOT NULL`); n != 0 {
		t.Errorf("%d records not typed", n)
	}
}

func TestConcurrentIngesters(t *testing.T) {
	h := startHub(t, "")
	const ingesters, batches, per = 20, 10, 5
	devs := make([]otherDevice, ingesters)
	for i := range devs {
		devs[i] = startHubDevice(t, h, "machine")
	}
	var wg sync.WaitGroup
	errs := make(chan error, ingesters)
	for i, d := range devs {
		wg.Add(1)
		go func(i int, d otherDevice) {
			defer wg.Done()
			sid := fmt.Sprintf("s-conc-%02d", i)
			for b := 0; b < batches; b++ {
				var recs []json.RawMessage
				for k := 0; k < per; k++ {
					ev := "PreToolUse"
					if b == 0 && k == 0 {
						ev = "SessionStart"
					}
					recs = append(recs, simpleHook(t, d.machine, sid, ev, t0.Add(time.Duration(b*per+k)*time.Second)))
				}
				body, _ := json.Marshal(map[string]any{"v": 1, "client": "wy/0.0.0-test", "records": recs})
				req, _ := http.NewRequest("POST", h.http.URL+"/v1/ingest", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+d.token)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					errs <- err
					return
				}
				var r ingest.Reply
				_ = json.NewDecoder(resp.Body).Decode(&r)
				resp.Body.Close()
				if resp.StatusCode != 200 || len(r.Accepted) != per {
					errs <- fmt.Errorf("ingester %d batch %d: %d %+v", i, b, resp.StatusCode, r)
					return
				}
			}
		}(i, d)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := h.count(`SELECT count(*) FROM record`); n != ingesters*batches*per {
		t.Errorf("%d records", n)
	}
	if n := h.count(`SELECT count(*) FROM session_state WHERE state = 'working'`); n != ingesters {
		t.Errorf("%d working sessions", n)
	}
	if n := h.count(`SELECT count(*) FROM machine WHERE last_seen IS NOT NULL`); n < ingesters {
		t.Errorf("%d machines heard from", n)
	}
}

// ---- stream ----

type sse struct {
	id, event, data string
}

func openStream(t *testing.T, h *hub, query, token, last string) (<-chan sse, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", h.http.URL+"/v1/stream"+query, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if last != "" {
		req.Header.Set("Last-Event-ID", last)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		cancel()
		t.Fatalf("stream: %d", resp.StatusCode)
	}
	ch := make(chan sse, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var cur sse
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.event != "" {
					ch <- cur
				}
				cur = sse{}
			case strings.HasPrefix(line, "id: "):
				cur.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				cur.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				cur.data = line[6:]
			}
		}
	}()
	return ch, cancel
}

func next(t *testing.T, ch <-chan sse, typ string, within time.Duration) sse {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed waiting for %s", typ)
			}
			if e.event == typ {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s event within %s", typ, within)
		}
	}
}

func TestStream(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	phone := h.token("phone")
	ch, cancel := openStream(t, h, "", phone, "")
	defer cancel()
	next(t, ch, "hello", 2*time.Second)

	start := time.Now()
	h.ingest(tok, simpleHook(t, h.machine, "s-live", "SessionStart", t0))
	e := next(t, ch, "session", 2*time.Second)
	if time.Since(start) > 2*time.Second || !strings.Contains(e.data, "s-live") || e.id == "" {
		t.Fatalf("session event %+v after %s", e, time.Since(start))
	}

	// Resume: events after Last-Event-ID are replayed from the ring buffer.
	h.ingest(tok, simpleHook(t, h.machine, "s-live", "UserPromptSubmit", t0.Add(time.Second)))
	second := next(t, ch, "session", 2*time.Second)
	cancel()
	ch2, cancel2 := openStream(t, h, "?types=session", phone, e.id)
	defer cancel2()
	next(t, ch2, "hello", 2*time.Second)
	got := next(t, ch2, "session", 2*time.Second)
	if got.id != second.id || !strings.Contains(got.data, `"working"`) {
		t.Errorf("resumed %+v, want id %s", got, second.id)
	}

	// An id the hub never issued (an older hub) gets a reset.
	ch3, cancel3 := openStream(t, h, "", phone, "1")
	defer cancel3()
	next(t, ch3, "hello", 2*time.Second)
	if r := next(t, ch3, "reset", 2*time.Second); !strings.Contains(r.data, "reason") {
		t.Errorf("reset %+v", r)
	}

	// A token in the URL is not accepted, on the stream or anywhere else.
	if code, _ := h.do("GET", "/v1/stream?token="+phone, "", nil); code != 401 {
		t.Errorf("?token= on the stream: %d", code)
	}
	if code, _ := h.do("GET", "/v1/overview?token="+phone, "", nil); code != 401 {
		t.Errorf("?token= on overview: %d", code)
	}
}

func TestSessionsAPI(t *testing.T) {
	h := startHub(t, "")
	tok := h.token("machine")
	desktop := h.token("desktop")
	now := time.Now().Add(-time.Minute)
	h.ingest(tok,
		simpleHook(t, h.machine, "s-a", "SessionStart", now),
		simpleHook(t, h.machine, "s-a", "UserPromptSubmit", now.Add(time.Second)),
		simpleHook(t, h.machine, "s-b", "SessionStart", now),
	)
	perm, _ := json.Marshal(map[string]any{"session_id": "s-b", "hook_event_name": "PermissionRequest", "cwd": "/home/u/proj",
		"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	h.ingest(tok, hookRecord(t, h.machine, now.Add(2*time.Second), perm))

	var list struct {
		Sessions []struct {
			ID       string `json:"id"`
			NeedsYou bool   `json:"needs_you"`
		} `json:"sessions"`
	}
	h.get("/v1/sessions", desktop, &list)
	if len(list.Sessions) != 2 || list.Sessions[0].ID != "s-b" || !list.Sessions[0].NeedsYou {
		t.Fatalf("needs-you first: %+v", list.Sessions)
	}
	h.get("/v1/sessions?state=working", desktop, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].ID != "s-a" {
		t.Errorf("state filter: %+v", list.Sessions)
	}
	h.get("/v1/sessions?machine="+h.machine, desktop, &list)
	if len(list.Sessions) != 2 {
		t.Errorf("machine filter: %d", len(list.Sessions))
	}
	h.get("/v1/sessions?machine=m_none", desktop, &list)
	if len(list.Sessions) != 0 {
		t.Errorf("other machine: %d", len(list.Sessions))
	}
	var detail map[string]any
	h.get("/v1/sessions/s-a", desktop, &detail)
	if tr, _ := detail["transitions"].([]any); len(tr) < 2 {
		t.Errorf("transitions: %v", detail["transitions"])
	}
	if code, _ := h.do("GET", "/v1/sessions/nope", desktop, nil); code != 404 {
		t.Errorf("unknown session: %d", code)
	}
	var machines struct {
		Machines []map[string]any `json:"machines"`
	}
	h.get("/v1/machines", desktop, &machines)
	if len(machines.Machines) != 1 {
		t.Errorf("machines: %+v", machines)
	}
	var ov map[string]any
	h.get("/v1/overview", desktop, &ov)
	if _, ok := ov["counts"]; !ok {
		t.Errorf("overview: %v", ov)
	}
}
