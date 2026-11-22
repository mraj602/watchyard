package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/calibrate"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

func calibSampleRec(t *testing.T, machine, sid string, at time.Time, five float64, fiveReset time.Time, seven float64, sevenReset time.Time) []byte {
	t.Helper()
	payload := fmt.Sprintf(`{"session_id":%q,"rate_limits":{"five_hour":{"used_percentage":%v,"resets_at":%d},"seven_day":{"used_percentage":%v,"resets_at":%d}}}`,
		sid, five, fiveReset.Unix(), seven, sevenReset.Unix())
	r, err := record.NewLimitSample(record.Meta{Machine: machine, Slot: "~/.claude-2", Client: "wy/0.6.0", Now: at,
		Seat: &record.Seat{Account: acctUUID, Org: orgUUID, Src: "claude_json"}}, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

func seatHook(t *testing.T, machine, sid, event string, at time.Time) []byte {
	t.Helper()
	p, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": event, "cwd": "/home/u/proj"})
	r, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: "~/.claude-2", Client: "wy/0.6.0", Now: at,
		Seat: &record.Seat{Account: acctUUID, Org: orgUUID, Src: "claude_json"}}, p, record.HookOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

// The hub's calibration job folds a seat's samples, token rows and leases, and GET /v1/usage
// returns the factors it stored (setup.md 8.5, Calibration). A second run with no new data
// changes nothing; new data is folded from the cursor on.
func TestUsageCalibration(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	now := time.Now().UTC().Truncate(time.Hour)
	start := now.Add(-4 * 24 * time.Hour)
	const sid = "11111111-2222-4333-8444-555555555555"
	const lane = "aaaaaaaa-2222-4333-8444-555555555555"
	weekReset := now.Add(3 * 24 * time.Hour)

	var recs [][]byte
	recs = append(recs, seatHook(t, h.machine, sid, "SessionStart", start))
	// From 08:00 to 20:00 each day: 1M units (input) every 10 minutes and a reading after it.
	// The truth: 2 five-hour points per million units, 8 five-hour points per weekly point.
	h5, w := 0.0, 0.0
	var end time.Time
	n := 0
	for at := start; at.Before(now.Add(-time.Hour)); at = at.Add(10 * time.Minute) {
		if at.Hour() < 8 || at.Hour() >= 20 {
			continue
		}
		if !end.IsZero() && !at.Before(end) {
			h5, end = 0, time.Time{}
		}
		if end.IsZero() {
			end = at.Add(5 * time.Hour)
		}
		r, err := record.NewTokenRows(record.Meta{Machine: h.machine, Client: "wy/0.6.0", Now: at}, record.TokenRowsPayload{
			Rows: []record.TokenRow{{Key: fmt.Sprintf("msg-%d", n), TS: at.Format(time.RFC3339Nano), SessionID: sid, Input: 1_000_000}}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := r.Encode()
		recs = append(recs, b)
		h5 += 2
		w += 2.0 / 8
		recs = append(recs, calibSampleRec(t, h.machine, sid, at.Add(time.Minute), h5, end, math.Round(w), weekReset))
		n++
	}
	// A picker-launched lane a day ago: 3M units in its first 5 hours.
	lr := leaseRec(t, h.machine, now.Add(-24*time.Hour), "01JB0000000000000000000009", lane, true)
	recs = append(recs, lr, seatHook(t, h.machine, lane, "SessionStart", now.Add(-24*time.Hour+time.Minute)))
	tr, _ := record.NewTokenRows(record.Meta{Machine: h.machine, Client: "wy/0.6.0", Now: now.Add(-23 * time.Hour)}, record.TokenRowsPayload{
		Rows: []record.TokenRow{{Key: "msg-lane", TS: now.Add(-23 * time.Hour).Format(time.RFC3339Nano), SessionID: lane, Input: 3_000_000}}})
	b, _ := tr.Encode()
	recs = append(recs, b)
	for i := 0; i < len(recs); i += 500 {
		j := min(i+500, len(recs))
		h.ingest(machine, recs[i:j]...)
	}

	var doc struct {
		Seats []struct {
			Calibration *struct {
				F struct {
					Value    float64  `json:"value"`
					Measured *float64 `json:"measured"`
					Weight   float64  `json:"weight"`
					N        int      `json:"n"`
				} `json:"f"`
				Ratio struct {
					Measured *float64 `json:"measured"`
				} `json:"ratio"`
				LaneUnits  *float64                    `json:"lane_units"`
				LanePrice  struct{ Measured *float64 } `json:"lane_price"`
				LaneCharge float64                     `json:"lane_charge"`
			} `json:"calibration"`
		} `json:"seats"`
	}
	h.get("/v1/usage", machine, &doc)
	if len(doc.Seats) != 1 || doc.Seats[0].Calibration != nil {
		t.Fatalf("before the first run: %+v", doc.Seats)
	}

	ctx := context.Background()
	if err := calibrate.Run(ctx, h.st, now); err != nil {
		t.Fatal(err)
	}
	code, body := h.do("GET", "/v1/usage", machine, nil)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	_ = json.Unmarshal(body, &doc)
	c := doc.Seats[0].Calibration
	if c == nil || c.F.Measured == nil || math.Abs(*c.F.Measured-2) > 0.05 || c.F.N < 20 || c.F.Weight <= 0 {
		t.Fatalf("calibration %s", body)
	}
	if c.Ratio.Measured == nil || math.Abs(*c.Ratio.Measured-8) > 0.6 {
		t.Fatalf("ratio %v", c.Ratio.Measured)
	}
	if c.LaneUnits == nil || *c.LaneUnits != 3e6 || c.LanePrice.Measured == nil {
		t.Fatalf("lane: %s", body)
	}
	firstN := c.F.N

	// Again with nothing new: the same pairs.
	if err := calibrate.Run(ctx, h.st, now); err != nil {
		t.Fatal(err)
	}
	h.get("/v1/usage", machine, &doc)
	if got := doc.Seats[0].Calibration.F.N; got != firstN {
		t.Fatalf("a second run changed n: %d -> %d", firstN, got)
	}

	// New readings and tokens after the first run are folded from the cursor on, once settled.
	var more [][]byte
	fr := now.Add(5 * time.Hour)
	for i := 1; i <= 6; i++ {
		at := now.Add(time.Duration(i) * 10 * time.Minute)
		r, _ := record.NewTokenRows(record.Meta{Machine: h.machine, Client: "wy/0.6.0", Now: at}, record.TokenRowsPayload{
			Rows: []record.TokenRow{{Key: fmt.Sprintf("msg-late-%d", i), TS: at.Format(time.RFC3339Nano), SessionID: sid, Input: 1_000_000}}})
		b, _ := r.Encode()
		more = append(more, b, calibSampleRec(t, h.machine, sid, at.Add(time.Minute), float64(2*i), fr, math.Round(w), weekReset))
	}
	h.ingest(machine, more...)
	if err := calibrate.Run(ctx, h.st, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.get("/v1/usage", machine, &doc)
	if got := doc.Seats[0].Calibration.F.N; got <= firstN {
		t.Fatalf("new data not folded: %d -> %d", firstN, got)
	}
	if n := h.count(`SELECT count(*) FROM seat_calibration`); n != 1 {
		t.Fatalf("%d calibration rows", n)
	}

	// A row calibrated for another login (the seat changed account or org) restarts.
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE seat_calibration SET org_uuid = 'another-org'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := calibrate.Run(ctx, h.st, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var after struct {
		Seats []struct {
			Calibration struct {
				Reset *struct {
					Reason string `json:"reason"`
				} `json:"reset"`
				F struct {
					N      int     `json:"n"`
					Weight float64 `json:"weight"`
				} `json:"f"`
			} `json:"calibration"`
		} `json:"seats"`
	}
	h.get("/v1/usage", machine, &after)
	if c := after.Seats[0].Calibration; c.Reset == nil || c.Reset.Reason != "seat_change" || c.F.N != 0 || c.F.Weight != 0 {
		t.Fatalf("after a seat change: %+v", c)
	}
	if n := h.count(`SELECT count(*) FROM seat_calibration WHERE org_uuid = ?`, orgUUID); n != 1 {
		t.Fatal("the row does not name the seat's login again")
	}
}
