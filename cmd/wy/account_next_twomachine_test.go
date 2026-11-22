package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

// twoMachineDoc is the part of `wy account next --json` the two-machine test reads.
type twoMachineDoc struct {
	Data     string `json:"data"`
	Accounts []struct {
		Folder string `json:"folder"`
		Lanes  *struct {
			Weekly       float64 `json:"weekly_pct"`
			FiveHour     float64 `json:"five_hour_pct"`
			Meter5h      float64 `json:"meter_5h_pct"`
			Leases       int     `json:"leases"`
			LeaseCharges []struct {
				At         time.Time `json:"at"`
				Machine    string    `json:"machine"`
				Copy       bool      `json:"copy"`
				State      string    `json:"state"`
				BurnedFrom string    `json:"burned_from"`
				BurnedPct  float64   `json:"burned_pct"`
				Charge5h   float64   `json:"charge_5h_pct"`
			} `json:"lease_charges"`
		} `json:"lanes"`
	} `json:"accounts"`
}

// Two machines, one real hub (setup.md 8.5, effective state 3). Machine A (the worker) takes
// leases on account a1 and its lanes burn tokens; machine B picks. B must count every token of
// A's lanes once: before the reading in the reading, after it in B's meter (from the hub's
// burn), and each lease only for what it has not burned yet. Then the hub goes down: B uses
// its kept copy of A's leases, which settle at a reading 10 minutes after their pick and
// otherwise charge in full.
func TestAccountNextTwoMachines(t *testing.T) {
	ctx := context.Background()
	now := nextNow // 12:00
	at := func(hm string) time.Time {
		x, err := time.Parse("15:04", hm)
		if err != nil {
			t.Fatal(err)
		}
		return time.Date(now.Year(), now.Month(), now.Day(), x.Hour(), x.Minute(), 0, 0, time.UTC)
	}

	// The hub, on the test's clock.
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(api.Config{Store: st, PingEvery: time.Hour, Now: func() time.Time { return now }})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); srv.Close(); st.Close() })

	// Machine B is the fixture (~/.claude logged in to a1/o1); machine A is the worker.
	f := newNextFixture(t)
	f.set("hub.url", hs.URL)
	machA := "m_" + record.NewID(now)
	ta, err := auth.Create(ctx, st, auth.CreateRequest{Kind: "machine", Name: "worker", Machine: machA})
	if err != nil {
		t.Fatal(err)
	}
	tb, err := auth.Create(ctx, st, auth.CreateRequest{Kind: "machine", Name: "laptop", Machine: f.machine})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dirs.State.Path, client.TokenFile), []byte(tb.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	seat := &record.Seat{Account: "a1", Org: "o1", Src: "claude_json"}
	meta := func(t0 time.Time, withSeat bool) record.Meta {
		m := record.Meta{Machine: machA, Slot: "~/.claude-5", Client: "wy/0.6.0", Now: t0}
		if withSeat {
			m.Seat = seat
		}
		return m
	}
	var recs [][]byte
	add := func(r *record.Record, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		b, err := r.Encode()
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, b)
	}
	lease := func(id, sid string, t0 time.Time) {
		add(record.NewAccountLease(meta(t0, true), record.AccountLeasePayload{LeaseID: id, SessionID: sid,
			Kind: record.LeaseAggressive, C5: 8, CF: 8, Strategy: "lanes", Folder: "~/.claude-5", Batch: "01JB0000000000000000000000"}))
	}
	hook := func(sid, event string, t0 time.Time) {
		p, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/srv/lane"})
		add(record.NewHookEvent(meta(t0, true), p, record.HookOptions{}))
	}
	tokens := func(sid string, t0 time.Time, millions float64) {
		add(record.NewTokenRows(meta(t0, false), record.TokenRowsPayload{Rows: []record.TokenRow{{
			Key: "msg-" + sid + t0.Format("1504"), TS: t0.Format(time.RFC3339Nano), SessionID: sid, Input: int64(millions * 1e6)}}}))
	}
	const s1, s2, s3, s4 = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000002",
		"aaaaaaaa-0000-4000-8000-000000000003", "aaaaaaaa-0000-4000-8000-000000000004"
	// A reading of a1 from an interactive session on A at 11:40: 5h 20 %, weekly 30 %.
	sample := fmt.Sprintf(`{"session_id":"cccccccc-0000-4000-8000-000000000009","rate_limits":{"five_hour":{"used_percentage":20,"resets_at":%d},"seven_day":{"used_percentage":30,"resets_at":%d}}}`,
		at("14:00").Unix(), now.Add(40*time.Hour).Unix())
	add(record.NewLimitSample(meta(at("11:40"), true), []byte(sample)))
	// L1 at 11:20: 3M units before the reading, 2M after (5M = 7 points burned, 1 left).
	lease("01JB0000000000000000000001", s1, at("11:20"))
	hook(s1, "SessionStart", at("11:20").Add(30*time.Second))
	tokens(s1, at("11:25"), 3)
	tokens(s1, at("11:50"), 2)
	// L2 at 11:45, after the reading: 1M units (1.4 points burned, 6.6 left).
	lease("01JB0000000000000000000002", s2, at("11:45"))
	hook(s2, "SessionStart", at("11:45").Add(30*time.Second))
	tokens(s2, at("11:55"), 1)
	// L3 at 11:30: 1M before the reading, 1M after; its session ended at 11:52 on A.
	lease("01JB0000000000000000000003", s3, at("11:30"))
	hook(s3, "SessionStart", at("11:30").Add(30*time.Second))
	tokens(s3, at("11:35"), 1)
	tokens(s3, at("11:50"), 1)
	hook(s3, "SessionEnd", at("11:52"))
	// L4 at 11:58: picked, not started yet.
	lease("01JB0000000000000000000004", s4, at("11:58"))
	raws := make([]json.RawMessage, len(recs))
	for i := range recs {
		raws[i] = recs[i]
	}
	body, _ := json.Marshal(map[string]any{"v": 1, "client": "wy/0.6.0", "records": raws})
	req, _ := http.NewRequest("POST", hs.URL+"/v1/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+ta.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest: %d", resp.StatusCode)
	}

	a1 := func(args ...string) (doc twoMachineDoc, view *struct{ h, w, meter float64 }) {
		t.Helper()
		r := f.run(append([]string{"--json"}, args...)...)
		if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
			t.Fatalf("json: %v\n%s\n%s", err, r.out, r.errOut)
		}
		for _, a := range doc.Accounts {
			if a.Folder == "~/.claude" && a.Lanes != nil {
				return doc, &struct{ h, w, meter float64 }{a.Lanes.FiveHour, a.Lanes.Weekly, a.Lanes.Meter5h}
			}
		}
		t.Fatalf("no lanes view of ~/.claude: %s", r.out)
		return
	}
	charges := func(doc twoMachineDoc) string {
		var out []string
		for _, a := range doc.Accounts {
			if a.Folder == "~/.claude" {
				for _, c := range a.Lanes.LeaseCharges {
					out = append(out, fmt.Sprintf("%s %s %s %s %.1f", c.At.Format("15:04"), c.Machine, c.State, c.BurnedFrom, c.Charge5h))
				}
			}
		}
		return strings.Join(out, "; ")
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.051 }

	// 1. Hub up. Meter (B's view, from the hub's burn): after 11:40, 2M (L1) + 1M (L2) + 1M (L3)
	// = 4M units = 5.6 points. Charges: L1 8 − 7 = 1, L2 8 − 1.4 = 6.6, L3 ended 0, L4 not
	// started 8. h = 20 + 5.6 + 15.6 = 41.2 (each lane counts c = 8 at most, or what it really
	// burned: L1 4.2 in the reading + 2.8 metered + 1 left = 8; L3 1.4 + 1.4 = 2.8). Weekly: 30 +
	// (5.6 + 15.6) / 7.2.
	doc, v := a1("--no-reserve")
	if doc.Data != "hub" || !near(v.meter, 5.6) || !near(v.h, 41.2) || !near(v.w, 30+21.2/7.2) {
		t.Errorf("hub up: data %s h %v w %v meter %v, want hub 41.2 %.2f 5.6", doc.Data, v.h, v.w, v.meter, 30+21.2/7.2)
	}
	if got := charges(doc); got != "11:20 other active hub 1.0; 11:30 other ended hub 0.0; 11:45 other active hub 6.6; 11:58 other active none 8.0" {
		t.Errorf("hub up, lease charges: %s", got)
	}
	r := f.run("--explain", "--no-reserve")
	if !strings.Contains(r.out, "(other machine): active; burned 5.00M units from hub = 7.0 points; charge 5h 1.0, reserve 1.0") ||
		!strings.Contains(r.out, "(other machine): ended; burned 2.00M units from hub = 2.8 points; no charge") ||
		!strings.Contains(r.out, "(other machine): active, not launched yet; burned none (no source); charge 5h 8.0") {
		t.Errorf("explain:\n%s", r.out)
	}

	// A pick with the hub up keeps the copy of A's leases; B's own pick is removed again so
	// only A's leases count below.
	if r := f.run("--json"); r.code != 0 {
		t.Fatalf("pick: %d %s", r.code, r.errOut)
	}
	if b, err := os.ReadFile(filepath.Join(f.dirs.State.Path, "next", "hub_leases.json")); err != nil ||
		strings.Contains(string(b), `"burn"`) || !strings.Contains(string(b), `"acct:a1/o1"`) {
		t.Fatalf("hub leases copy: %v %s", err, b)
	}
	if err := os.RemoveAll(filepath.Join(f.dirs.State.Path, "next", "leases")); err != nil {
		t.Fatal(err)
	}
	hs.Close()
	outbox := func(five float64, t0 time.Time) {
		t.Helper()
		line := fmt.Sprintf(`{"v":1,"id":"01JA0000000000000000000099","kind":"limit_sample","ts":"%s","machine":"%s","slot":"~/.claude","seat":{"account":"a1","org":"o1"},"derived":{"five_hour_pct":%v,"five_hour_resets_at":"%s","seven_day_pct":30,"seven_day_resets_at":"%s"},"payload":{}}`,
			t0.Format(time.RFC3339), f.machine, five, at("14:00").Format(time.RFC3339), now.Add(40*time.Hour).Format(time.RFC3339))
		dir := filepath.Join(f.dirs.State.Path, "outbox")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, now.Format("2006-01-02")+".jsonl"), []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 2. Hub down, a fresh reading on B at 11:55 (35 %): L1 and L2 settle (it is at least 10
	// minutes after their picks, so it holds their use), L3 ended, L4 (11:58) is too recent to
	// settle and charges 8. B's meter has none of A's tokens. h = 35 + 8.
	outbox(35, at("11:55"))
	doc, v = a1("--no-reserve")
	if doc.Data != "local" || !near(v.meter, 0) || !near(v.h, 43) {
		t.Errorf("hub down, fresh reading: data %s h %v meter %v, want local 43 0", doc.Data, v.h, v.meter)
	}
	if got := charges(doc); got != "11:20 other settled none 0.0; 11:30 other ended none 0.0; 11:45 other settled none 0.0; 11:58 other active none 8.0" {
		t.Errorf("hub down, fresh reading, lease charges: %s", got)
	}

	// 3. Hub down, no fresh reading (B's newest is from 11:22, under 10 minutes after L1): every
	// lease not ended charges in full, the conservative side. L1's use before 11:22 would be
	// counted twice here (none in this test). h = 15 + 3 × 8.
	outbox(15, at("11:22"))
	doc, v = a1("--no-reserve")
	if !near(v.h, 39) {
		t.Errorf("hub down, no fresh reading: h %v, want 39", v.h)
	}
	if got := charges(doc); got != "11:20 other active none 8.0; 11:30 other ended none 0.0; 11:45 other active none 8.0; 11:58 other active none 8.0" {
		t.Errorf("hub down, no fresh reading, lease charges: %s", got)
	}
	for _, a := range doc.Accounts {
		if a.Folder == "~/.claude" && (len(a.Lanes.LeaseCharges) == 0 || !a.Lanes.LeaseCharges[0].Copy) {
			t.Errorf("leases not marked as from the kept copy: %+v", a.Lanes.LeaseCharges)
		}
	}
	// --no-hub uses local data only: no kept copy.
	if _, v = a1("--no-reserve", "--no-hub"); !near(v.h, 15) {
		t.Errorf("--no-hub: h %v, want 15", v.h)
	}
}
