package api_test

import (
	"context"
	"math"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// paceHub is a hub whose store and API share one clock, with a weekly window 89% used and
// 35 minutes from its reset; readings is how the last hour's readings climb (points an hour).
func paceHub(t *testing.T, used, perHour float64) map[string]any {
	t.Helper()
	now := time.Date(2026, 11, 25, 9, 25, 0, 0, time.UTC)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(api.Config{Store: st, Now: func() time.Time { return now }, PingEvery: time.Hour,
		WebUI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>board")}}})
	h := &hub{t: t, st: st, srv: srv, http: httptest.NewServer(srv.Handler()), tokens: map[string]string{}}
	t.Cleanup(h.stop)
	h.exec(`INSERT INTO account (uuid, email, first_seen, last_seen) VALUES ('acc_a', 'a@x.io', 1, 1)`)
	h.exec(`INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org', 1, 1)`)
	h.exec(`INSERT INTO seat (id, account_uuid, org_uuid, label, created_at) VALUES ('seat_a', 'acc_a', 'org', 'claude-5', 1)`)
	resets := now.Add(35 * time.Minute)
	h.exec(`INSERT INTO usage_window (seat_id, window_key, resets_at, started_at, peak_pct, final_pct, unused_pct, samples, updated_at)
		VALUES ('seat_a', 'seven_day', ?, ?, ?, ?, 0, 7, ?)`, resets.UnixMilli(), resets.Add(-7*24*time.Hour).UnixMilli(), used, used, now.UnixMilli())
	for min := 50; min >= 0; min -= 10 { // a reading every 10 minutes for the last 50
		at := now.Add(-time.Duration(min) * time.Minute)
		h.exec(`INSERT INTO limit_sample (seat_id, window_key, ts, source, used_pct, resets_at, record_id) VALUES ('seat_a', 'seven_day', ?, 'web', ?, ?, 'r')`,
			at.UnixMilli(), used-perHour*float64(min)/60, resets.UnixMilli())
	}
	var out map[string]any
	h.get("/v1/accounts", h.token("phone"), &out)
	accts := list(t, out, "accounts")
	ws := list(t, accts[0], "windows")
	return ws[0]["pace"].(map[string]any)
}

// The board's pace line follows the last hour's burn: 89% used, 35 minutes left, burning 9
// points an hour projects about 94%, not the week's average (about 89%).
func TestPaceUsesRecentRate(t *testing.T) {
	p := paceHub(t, 89, 9)
	if p["basis"] != "recent" || math.Abs(p["projected_pct"].(float64)-94.2) > 0.2 || p["band"] != "on_track" || math.Abs(p["rate_per_hour"].(float64)-9) > 0.2 {
		t.Fatalf("pace %v", p)
	}
}

// A quiet hour in a week that was busy: it projects where it is, not the week's average.
func TestPaceQuietHour(t *testing.T) {
	p := paceHub(t, 89, 0)
	if p["basis"] != "recent" || p["projected_pct"].(float64) != 89 {
		t.Fatalf("pace %v", p)
	}
}
