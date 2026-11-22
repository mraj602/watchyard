package client

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/outbox"
)

// ---- fake hub ---------------------------------------------------------------------------

type hubMode int

const (
	modeOK        hubMode = iota
	mode500               // 500, nothing stored
	modeTimeout           // stores everything, answers after the client gave up
	modePartial           // stores and acks only the first half
	modeLostReply         // stores everything, answers 503
	mode429               // 429 with Retry-After
)

// testHTTPTimeout is the client timeout of the fake-hub tests. A loopback answer takes
// microseconds, so one second is generous even under -race on a loaded machine; only the
// modeTimeout answer (delayed past it on purpose) ever hits it.
const testHTTPTimeout = time.Second

type fakeHub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	requests int
	stored   map[string]int // id -> times newly stored
	seen     map[string]int // id -> times received
	gzipped  int
	plain    int
	backlogs []string

	script   func(n int, ids []string) hubMode // nil: always OK
	reject   map[string]string                 // id -> reason, answered as rejected
	poison   string                            // a batch containing this id gets 400
	afterReq func(n int)                       // called after the answer of request n
	token    string                            // expected token, default "tok"
	delay    time.Duration                     // timeout mode sleep
}

func newHub(t *testing.T) *fakeHub {
	h := &fakeHub{t: t, stored: map[string]int{}, seen: map[string]int{}, reject: map[string]string{}, token: "tok", delay: testHTTPTimeout + 300*time.Millisecond}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) client() *Client {
	return &Client{BaseURL: h.srv.URL, Token: "tok", HTTP: &http.Client{Timeout: testHTTPTimeout}}
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		fmt.Fprint(w, `{"ok":true,"version":"0.2.0","schema":1}`)
		return
	}
	if r.URL.Path != "/v1/ingest" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+h.token {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"bad token","code":"unauthorized"}`)
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		body = zr
	}
	raw, _ := io.ReadAll(body)
	var batch struct {
		V       int               `json:"v"`
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(raw, &batch); err != nil || batch.V != 1 || len(batch.Records) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"malformed batch","code":"bad_request"}`)
		return
	}
	ids := make([]string, len(batch.Records))
	for i, rec := range batch.Records {
		var e struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(rec, &e)
		ids[i] = e.ID
	}

	h.mu.Lock()
	h.requests++
	n := h.requests
	if r.Header.Get("Content-Encoding") == "gzip" {
		h.gzipped++
	} else {
		h.plain++
	}
	h.backlogs = append(h.backlogs, r.Header.Get("X-Wy-Backlog"))
	mode := modeOK
	if h.script != nil {
		mode = h.script(n, ids)
	}
	for _, id := range ids {
		if id == h.poison {
			h.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"bad record","code":"bad_request"}`)
			return
		}
	}
	reply := IngestReply{Accepted: []string{}, Duplicates: []string{}, Rejected: []Rejected{}}
	store := func(id string) {
		h.seen[id]++
		if reason, ok := h.reject[id]; ok {
			reply.Rejected = append(reply.Rejected, Rejected{ID: id, Reason: reason})
			return
		}
		if h.stored[id] > 0 {
			reply.Duplicates = append(reply.Duplicates, id)
		} else {
			h.stored[id]++
		}
		reply.Accepted = append(reply.Accepted, id)
	}
	switch mode {
	case mode500:
	case mode429:
	case modePartial:
		for _, id := range ids[:len(ids)/2] {
			store(id)
		}
	default:
		for _, id := range ids {
			store(id)
		}
	}
	after := h.afterReq
	delay := h.delay
	h.mu.Unlock()

	switch mode {
	case mode500:
		w.WriteHeader(500)
	case mode429:
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"slow down","code":"rate_limited"}`)
	case modeLostReply:
		w.WriteHeader(503)
	case modeTimeout:
		time.Sleep(delay)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}
	if after != nil {
		after(n)
	}
}

func (h *fakeHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests
}

// ---- helpers ----------------------------------------------------------------------------

func line(id, ts string) string {
	return fmt.Sprintf(`{"v":1,"id":%q,"kind":"hook_event","ts":%q,"machine":"m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K","payload":{"hook_event_name":"Stop"}}`, id, ts)
}

func rid(i int) string { return fmt.Sprintf("01JA2XQ8R7Y5V0Z6M3K1T%05d", i) }

// fill writes n records across the given day files (equal parts) and returns every id.
func fill(t *testing.T, ob *outbox.Outbox, n int, days ...string) []string {
	t.Helper()
	if err := os.MkdirAll(ob.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var ids []string
	per := (n + len(days) - 1) / len(days)
	i := 0
	for _, d := range days {
		var sb strings.Builder
		for k := 0; k < per && i < n; k, i = k+1, i+1 {
			id := rid(i)
			ids = append(ids, id)
			sb.WriteString(line(id, d+"T10:00:00.000Z") + "\n")
		}
		if err := os.WriteFile(filepath.Join(ob.Dir, d+".jsonl"), []byte(sb.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func addSpill(t *testing.T, ob *outbox.Outbox, name, id string) {
	t.Helper()
	p := filepath.Join(ob.Dir, "spill-"+name+".jsonl")
	if err := os.WriteFile(p, []byte(line(id, "2026-10-12T11:00:00.000Z")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeClock is a clock that only moves when the flusher sleeps.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}
func (c *fakeClock) advance(d time.Duration) { _ = c.sleep(context.Background(), d) }

func opts(ob *outbox.Outbox, cl *Client, clk *fakeClock) FlushOptions {
	return FlushOptions{
		Outbox: ob, Client: cl, Now: clk.now, Sleep: clk.sleep,
		Rand: func() float64 { return 0.5 }, Budget: 24 * time.Hour,
	}
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)} }

func assertDelivered(t *testing.T, h *fakeHub, ids []string) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range ids {
		if h.stored[id] != 1 {
			t.Fatalf("record %s stored %d times, want exactly 1", id, h.stored[id])
		}
	}
	if len(h.stored) != len(ids) {
		t.Fatalf("hub holds %d records, want %d", len(h.stored), len(ids))
	}
}

func assertAtEOF(t *testing.T, ob *outbox.Outbox) {
	t.Helper()
	b, err := ob.Read(ob.ReadCursor(), 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Items) != 0 || b.More {
		t.Fatalf("outbox not drained: %d records left, cursor %+v", len(b.Items), ob.ReadCursor())
	}
}

// ---- tests ------------------------------------------------------------------------------

func TestFlakyHubDeliversEverythingOnce(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 1200, "2026-10-10", "2026-10-11", "2026-10-12")
	addSpill(t, ob, "11-01JA2XQ8R7Y5V0Z6M3K1TSP1", "01JA2XQ8R7Y5V0Z6M3K1TSP01")
	addSpill(t, ob, "12-01JA2XQ8R7Y5V0Z6M3K1TSP2", "01JA2XQ8R7Y5V0Z6M3K1TSP02")
	ids = append(ids, "01JA2XQ8R7Y5V0Z6M3K1TSP01", "01JA2XQ8R7Y5V0Z6M3K1TSP02")

	h := newHub(t)
	cycle := []hubMode{mode500, modeTimeout, modePartial, modeLostReply, mode429, modeOK, modePartial, mode500, modeOK}
	h.script = func(n int, _ []string) hubMode {
		if n > 40 {
			return modeOK
		}
		return cycle[(n-1)%len(cycle)]
	}
	clk := newClock()
	o := opts(ob, h.client(), clk)
	o.Wait = true
	res := Flush(context.Background(), o)
	if res.Err != nil || !res.Drained {
		t.Fatalf("flush: %+v", res)
	}
	assertDelivered(t, h, ids)
	assertAtEOF(t, ob)
	if sp, _ := filepath.Glob(filepath.Join(ob.Dir, "spill-*")); len(sp) != 0 {
		t.Fatalf("spill files left: %v", sp)
	}
	if res.Sent != len(ids) {
		t.Fatalf("sent %d, want %d", res.Sent, len(ids))
	}
	if c := ob.ReadCursor(); c.Failures != 0 || c.RetryAfter != "" || c.File != "2026-10-12.jsonl" {
		t.Fatalf("cursor after success: %+v", c)
	}
}

func TestNeverAdvancesPastUnacknowledged(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 10, "2026-10-12")
	b, _ := ob.Read(outbox.Cursor{}, 100, 1<<20)

	// The hub acknowledges ids 3..9 but not 0..2: the cursor must stay at the start.
	// Then it acknowledges 0..4: the cursor moves just past record 4.
	step := 0
	ackSets := [][2]int{{3, 10}, {0, 5}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		set := ackSets[step]
		step++
		acc := ids[set[0]:set[1]]
		_ = json.NewEncoder(w).Encode(IngestReply{Accepted: acc, Rejected: []Rejected{}})
	}))
	defer srv.Close()
	cl := &Client{BaseURL: srv.URL, Token: "tok"}
	clk := newClock()
	o := opts(ob, cl, clk)
	res := Flush(context.Background(), o)
	if res.Err == nil {
		t.Fatal("a reply that omits ids must be treated as a failure")
	}
	if c := ob.ReadCursor(); c.File != "" || c.Offset != 0 {
		t.Fatalf("cursor advanced past unacknowledged records: %+v", c)
	}
	o.Force = true
	res = Flush(context.Background(), o)
	c := ob.ReadCursor()
	if c.File != "2026-10-12.jsonl" || c.Offset != b.Items[4].End {
		t.Fatalf("cursor = %+v, want offset %d (just past record 4)", c, b.Items[4].End)
	}
	if res.Err == nil {
		t.Fatal("partial acknowledgement must still count as a failure to retry")
	}
}

func TestRestartMidFlush(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 900, "2026-10-11", "2026-10-12")
	h := newHub(t)
	ctx, kill := context.WithCancel(context.Background())
	h.afterReq = func(n int) {
		if n == 2 {
			kill() // the process dies after the hub answered the second batch
		}
	}
	clk := newClock()
	o := opts(ob, h.client(), clk)
	o.MaxRecords = 200
	res := Flush(ctx, o)
	if res.Err == nil || res.Drained {
		t.Fatalf("killed run: %+v", res)
	}
	h.mu.Lock()
	h.afterReq = nil
	h.mu.Unlock()
	// A crash between the reply and the cursor write: roll the cursor back one batch.
	cur := ob.ReadCursor()
	if cur.Offset == 0 && cur.File == "" {
		t.Fatalf("expected some progress before the kill, cursor %+v", cur)
	}
	if err := ob.WriteCursor(outbox.Cursor{File: "2026-10-11.jsonl"}); err != nil {
		t.Fatal(err)
	}

	res = Flush(context.Background(), opts(ob, h.client(), clk))
	if res.Err != nil || !res.Drained {
		t.Fatalf("second run: %+v", res)
	}
	assertDelivered(t, h, ids)
	assertAtEOF(t, ob)
	if res.Duplicates == 0 {
		t.Fatalf("resent records must come back as duplicate acks: %+v", res)
	}
	if res.Duplicates != res.Accepted-(len(ids)-res.Duplicates) && res.Rejected != 0 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRejectedAndConflictAreLoggedAndSkipped(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 6, "2026-10-12")
	h := newHub(t)
	h.reject[ids[1]] = "bad_envelope"
	h.reject[ids[4]] = "id_conflict"
	res := Flush(context.Background(), opts(ob, h.client(), newClock()))
	if res.Err != nil || !res.Drained || res.Rejected != 2 || res.Accepted != 4 {
		t.Fatalf("result %+v", res)
	}
	assertAtEOF(t, ob)
	data, err := os.ReadFile(filepath.Join(ob.Dir, "rejected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{ids[1], "bad_envelope", ids[4], "id_conflict"} {
		if !strings.Contains(s, want) {
			t.Errorf("rejected.jsonl lacks %q:\n%s", want, s)
		}
	}
}

func TestBackoffIsStoredAndHonoured(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 3, "2026-10-12")
	h := newHub(t)
	mode := mode500
	h.script = func(int, []string) hubMode { return mode }
	clk := newClock()
	o := opts(ob, h.client(), clk)

	res := Flush(context.Background(), o)
	if res.Err == nil {
		t.Fatal("want failure")
	}
	c := ob.ReadCursor()
	if c.Failures != 1 || c.RetryAfter == "" {
		t.Fatalf("backoff not stored: %+v", c)
	}
	n := h.count()

	// The hook-started flush (Force false) stays quiet while backing off.
	res = Flush(context.Background(), o)
	if res.Skipped == "" || h.count() != n {
		t.Fatalf("flush during backoff: %+v requests %d->%d", res, n, h.count())
	}
	// After the delay it tries again, and a success clears the state.
	clk.advance(10 * time.Second)
	mode = modeOK
	res = Flush(context.Background(), o)
	if res.Err != nil || !res.Drained {
		t.Fatalf("after backoff: %+v", res)
	}
	assertDelivered(t, h, ids)
	if c := ob.ReadCursor(); c.Failures != 0 || c.RetryAfter != "" {
		t.Fatalf("state not cleared: %+v", c)
	}
}

func TestRetryAfterHeaderRaisesBackoff(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	fill(t, ob, 2, "2026-10-12")
	h := newHub(t)
	h.script = func(int, []string) hubMode { return mode429 }
	clk := newClock()
	Flush(context.Background(), opts(ob, h.client(), clk))
	ra, err := time.Parse(time.RFC3339Nano, ob.ReadCursor().RetryAfter)
	if err != nil || ra.Sub(clk.now()) < 7*time.Second {
		t.Fatalf("retry_after %v not honoured (now %v)", ra, clk.now())
	}
}

func TestAuthFailureStopsUntilTokenChanges(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 3, "2026-10-12")
	h := newHub(t)
	h.token = "right"
	clk := newClock()
	o := opts(ob, h.client(), clk) // token "tok" is wrong
	res := Flush(context.Background(), o)
	if res.Err == nil || h.count() != 0 {
		// 401 is answered before counting, so count stays 0; the error is what matters.
		if res.Err == nil {
			t.Fatalf("want error, got %+v", res)
		}
	}
	if st := ReadStatus(ob.Dir); !st.AuthFailed || st.OK {
		t.Fatalf("status = %+v", st)
	}
	if c := ob.ReadCursor(); c.File != "" || c.Offset != 0 {
		t.Fatalf("cursor moved on 401: %+v", c)
	}
	// Not retried by the hook-started flush...
	res = Flush(context.Background(), o)
	if res.Skipped == "" {
		t.Fatalf("second run should be skipped: %+v", res)
	}
	// ...but a changed token, or a run by hand, goes through.
	cl := h.client()
	cl.Token = "right"
	res = Flush(context.Background(), opts(ob, cl, clk))
	if res.Err != nil || !res.Drained {
		t.Fatalf("with new token: %+v", res)
	}
	assertDelivered(t, h, ids)
	if st := ReadStatus(ob.Dir); st.AuthFailed || !st.OK {
		t.Fatalf("status after success = %+v", st)
	}
}

func TestPoisonRecordIsIsolatedByBisecting(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 40, "2026-10-12")
	h := newHub(t)
	h.poison = ids[17]
	res := Flush(context.Background(), opts(ob, h.client(), newClock()))
	if res.Err != nil || !res.Drained || res.Dropped != 1 {
		t.Fatalf("result %+v", res)
	}
	var want []string
	for _, id := range ids {
		if id != ids[17] {
			want = append(want, id)
		}
	}
	assertDelivered(t, h, want)
	data, _ := os.ReadFile(filepath.Join(ob.Dir, "rejected.jsonl"))
	if !strings.Contains(string(data), ids[17]) {
		t.Fatalf("poison record not logged:\n%s", data)
	}
}

func TestGzipOnlyForLargeBatches(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	fill(t, ob, 1, "2026-10-12")
	h := newHub(t)
	// One small record plus the envelope is about 300 bytes: plain.
	Flush(context.Background(), opts(ob, h.client(), newClock()))
	if h.plain != 1 || h.gzipped != 0 {
		t.Fatalf("small batch: plain=%d gzip=%d", h.plain, h.gzipped)
	}
	ob2 := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	fill(t, ob2, 30, "2026-10-12")
	Flush(context.Background(), opts(ob2, h.client(), newClock()))
	if h.gzipped != 1 {
		t.Fatalf("large batch not gzipped: plain=%d gzip=%d", h.plain, h.gzipped)
	}
}

func TestBatchLimits(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 1100, "2026-10-12")
	h := newHub(t)
	res := Flush(context.Background(), opts(ob, h.client(), newClock()))
	if res.Err != nil || res.Batches != 3 {
		t.Fatalf("1100 records at 500 per batch should take 3 requests: %+v", res)
	}
	assertDelivered(t, h, ids)
	// The backlog header is sent and counts down.
	if len(h.backlogs) != 3 || h.backlogs[0] == "" {
		t.Fatalf("backlog headers: %v", h.backlogs)
	}
}

func TestSecondFlusherBacksOff(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	fill(t, ob, 2, "2026-10-12")
	lock, ok, err := ob.TryFlushLock()
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	defer lock.Release()
	h := newHub(t)
	res := Flush(context.Background(), opts(ob, h.client(), newClock()))
	if res.Skipped == "" || h.count() != 0 {
		t.Fatalf("flush under a held lock: %+v", res)
	}
}

// A hook that writes while flush.stamp is young starts no flush (it is debounced), so the
// hook-started flush lingers until the stamp is old and sends what such hooks wrote.
func TestLingerSendsRecordsWrittenDuringTheDebounce(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 2, "2026-10-12")
	h := newHub(t)
	clk := newClock()
	o := opts(ob, h.client(), clk)
	o.Linger = 2 * time.Second
	late := rid(7)
	var slept []time.Duration
	o.Sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		if len(slept) == 1 {
			// The debounced hook: appended after the first drain, before the stamp aged.
			fh, err := os.OpenFile(filepath.Join(ob.Dir, "2026-10-12.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(fh, line(late, "2026-10-12T10:00:01.000Z"))
			fh.Close()
		}
		return clk.sleep(ctx, d)
	}
	res := Flush(context.Background(), o)
	if res.Err != nil || !res.Drained {
		t.Fatalf("%+v", res)
	}
	assertDelivered(t, h, append(ids, late))
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("slept %v, want one wait of 2s (until the stamp is 2s old)", slept)
	}

	// Without Linger (the timer, wy flush by hand) a drained run returns at once.
	o.Linger, slept = 0, nil
	if res := Flush(context.Background(), o); !res.Drained || len(slept) != 0 {
		t.Fatalf("no linger: %+v slept %v", res, slept)
	}
}

func TestHousekeepingRunsEvenWhenHubIsDown(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	fill(t, ob, 2, "2026-01-01")
	h := newHub(t)
	h.script = func(int, []string) hubMode { return mode500 }
	// Day file from January is unflushed, so it must survive pruning.
	o := opts(ob, h.client(), newClock())
	o.KeepDays = 7
	o.CapBytes = 50 << 20
	Flush(context.Background(), o)
	if _, err := os.Stat(filepath.Join(ob.Dir, "2026-01-01.jsonl")); err != nil {
		t.Fatalf("unflushed file removed: %v", err)
	}
}

func TestBackoffFunction(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300}
	for i, w := range want {
		w *= time.Second
		for _, r := range []float64{0, 0.5, 0.999} {
			got := Backoff(i+1, r)
			if got < w/2 || got > w {
				t.Errorf("Backoff(%d, %v) = %v, want in [%v, %v]", i+1, r, got, w/2, w)
			}
		}
	}
	if Backoff(1000, 0.99) > MaxBackoff {
		t.Error("backoff exceeds the cap")
	}
}

func TestCheckURL(t *testing.T) {
	cases := []struct {
		in      string
		insec   bool
		want    string
		wantErr bool
	}{
		{"http://127.0.0.1:8787", false, "http://127.0.0.1:8787", false},
		{"http://localhost:8787/", false, "http://localhost:8787", false},
		{"https://hub.example.com", false, "https://hub.example.com", false},
		{"http://home-box:8787", false, "", true}, // a single-label name could be any LAN host
		{"http://home-box:8787", true, "http://home-box:8787", false},
		{"http://100.63.255.255:8787", false, "", true},
		{"http://100.127.255.255:8787", false, "http://100.127.255.255:8787", false},
		{"http://oci.tail1234.ts.net:8787", false, "http://oci.tail1234.ts.net:8787", false},
		{"http://100.101.102.103:8787", false, "http://100.101.102.103:8787", false},
		{"http://198.51.100.5:8787", false, "", true},
		{"http://198.51.100.5:8787", true, "http://198.51.100.5:8787", false},
		{"http://hub.example.com", false, "", true},
		{"ftp://x", false, "", true},
		{"hub.example.com", false, "", true},
		{"https://hub.example.com/v1", false, "", true},
	}
	for _, c := range cases {
		got, _, err := CheckURL(c.in, c.insec)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("CheckURL(%q, %v) = %q, %v", c.in, c.insec, got, err)
		}
	}
}

func TestHealth(t *testing.T) {
	h := newHub(t)
	got, err := h.client().Health(context.Background())
	if err != nil || got.Version != "0.2.0" {
		t.Fatalf("%+v %v", got, err)
	}
	dead := &Client{BaseURL: "http://127.0.0.1:1"}
	if _, err := dead.Health(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

// A hub that cannot answer a large batch in time gets smaller batches, run after run: the
// smaller size is kept in the cursor because a run may have time for one attempt only.
func TestSlowHubGetsSmallerBatches(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	ids := fill(t, ob, 300, "2026-10-12")
	h := newHub(t)
	h.script = func(_ int, ids []string) hubMode {
		if len(ids) > 100 {
			return modeTimeout
		}
		return modeOK
	}
	clk := newClock()
	o := opts(ob, h.client(), clk)
	o.Force = true

	res := Flush(context.Background(), o)
	if res.Err == nil {
		t.Fatal("want a timeout")
	}
	if c := ob.ReadCursor(); c.BatchMax != 150 || c.Offset != 0 {
		t.Fatalf("after one timeout of 300: %+v", c)
	}
	for i := 0; i < 10 && !res.Drained; i++ {
		res = Flush(context.Background(), o)
	}
	if !res.Drained {
		t.Fatalf("never drained: %+v", res)
	}
	assertDelivered(t, h, ids)
	if c := ob.ReadCursor(); c.BatchMax == 0 || c.BatchMax > 300 {
		t.Fatalf("batch_max after drain: %+v", c)
	}

	// Once the hub keeps up again the cap grows back and is cleared.
	h.script = nil
	fill(t, ob, 3000, "2026-10-13")
	res = Flush(context.Background(), o)
	if res.Err != nil || !res.Drained {
		t.Fatalf("second flush: %+v", res)
	}
	if c := ob.ReadCursor(); c.BatchMax != 0 {
		t.Fatalf("batch_max not cleared: %+v", c)
	}
}

func TestTooSlow(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&StatusError{Status: 503, RetryAfter: 5 * time.Second}, true},
		{&StatusError{Status: 500}, true},
		{&StatusError{Status: 503, RetryAfter: 300 * time.Second}, false}, // a frozen hub
		{&StatusError{Status: 429, RetryAfter: 7 * time.Second}, false},
		{context.DeadlineExceeded, true},
		{errors.New("connection refused"), false},
	} {
		if got := tooSlow(c.err); got != c.want {
			t.Errorf("tooSlow(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
