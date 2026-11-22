package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

func limitSampleRec(t *testing.T, machine, slot string, at time.Time, five, seven float64) []byte {
	t.Helper()
	payload := fmt.Sprintf(`{"session_id":"s-1","session_name":"secret-project","cwd":"/home/me/secret","rate_limits":{"five_hour":{"used_percentage":%v,"resets_at":%d},"seven_day":{"used_percentage":%v,"resets_at":%d}}}`,
		five, at.Add(3*time.Hour).Unix(), seven, at.Add(72*time.Hour).Unix())
	r, err := record.NewLimitSample(record.Meta{Machine: machine, Slot: slot, Client: "wy/0.6.0", Now: at,
		Seat: &record.Seat{Account: acctUUID, Org: orgUUID, Email: "a@example.com", Src: "claude_json"}}, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

func keysOf(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GET /v1/usage (setup.md 8.5, tokens.md 3): a machine token reads it; the reply holds only
// the allow-listed usage fields, and only the calling machine's folders.
func TestUsageForMachineToken(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	phone := h.token("phone")
	other := startHubDevice(t, h, "machine")
	now := time.Now().UTC().Truncate(time.Second)
	h.ingest(machine,
		slotLoginRec(t, h.machine, "~/.claude-2", acctUUID, now.Add(-2*time.Hour)),
		slotLoginRec(t, h.machine, "~/.claude-4", acctUUID, now.Add(-2*time.Hour)),
		slotLoginRec(t, h.machine, "~/.claude-4", "", now.Add(-time.Hour)),
		limitSampleRec(t, h.machine, "~/.claude-2", now.Add(-50*time.Minute), 20, 30),
		limitSampleRec(t, h.machine, "~/.claude-2", now.Add(-10*time.Minute), 32, 31))
	h.ingest(other.token, slotLoginRec(t, other.machine, "~/.claude-9", acctUUID, now.Add(-2*time.Hour)))

	if code, _ := h.do("GET", "/v1/usage", "", nil); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code, _ := h.do("GET", "/v1/usage?since=yesterday", machine, nil); code != http.StatusBadRequest {
		t.Errorf("bad since: %d", code)
	}
	code, body := h.do("GET", "/v1/usage", machine, nil)
	if code != http.StatusOK {
		t.Fatalf("machine token: %d %s", code, body)
	}
	for _, bad := range []string{"example.com", "secret", "Work Inc", "machine_name", other.machine, "claude-9", "s-1", "session_id", "session_name", "cwd"} {
		if strings.Contains(string(body), bad) {
			t.Errorf("reply holds %q: %s", bad, body)
		}
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keysOf(doc), ","); got != "machine,seats,since,time,v" {
		t.Errorf("doc keys %s", got)
	}
	var seats []map[string]json.RawMessage
	_ = json.Unmarshal(doc["seats"], &seats)
	if len(seats) != 1 {
		t.Fatalf("seats %s", doc["seats"])
	}
	if got := strings.Join(keysOf(seats[0]), ","); got != "account,id,label,leases,live_sessions,org,places,samples,signed_out,windows" {
		t.Errorf("seat keys %s", got)
	}
	var seat struct {
		Account string `json:"account"`
		Windows []struct {
			Key     string   `json:"key"`
			UsedPct *float64 `json:"used_pct"`
		} `json:"windows"`
		Samples []struct {
			Key     string   `json:"key"`
			UsedPct *float64 `json:"used_pct"`
		} `json:"samples"`
		Places    []map[string]any `json:"places"`
		SignedOut []map[string]any `json:"signed_out"`
	}
	_ = json.Unmarshal(doc["seats"][1:len(doc["seats"])-1], &seat)
	if seat.Account != acctUUID || len(seat.Windows) < 2 || len(seat.Samples) < 4 {
		t.Errorf("seat %+v", seat)
	}
	if len(seat.Places) != 1 || seat.Places[0]["slot"] != "~/.claude-2" || len(seat.Places[0]) != 1 {
		t.Errorf("places %v (only this machine's folders, slot only)", seat.Places)
	}
	if len(seat.SignedOut) != 1 || seat.SignedOut[0]["slot"] != "~/.claude-4" || seat.SignedOut[0]["since"] == nil {
		t.Errorf("signed_out %v", seat.SignedOut)
	}

	// A read token without a machine sees the usage but no folders; another machine sees its own.
	var ph struct {
		Machine *string `json:"machine"`
		Seats   []struct {
			Places []any `json:"places"`
		} `json:"seats"`
	}
	h.get("/v1/usage", phone, &ph)
	if ph.Machine != nil || len(ph.Seats) != 1 || len(ph.Seats[0].Places) != 0 {
		t.Errorf("phone %+v", ph)
	}
	h.get("/v1/usage", other.token, &ph)
	if len(ph.Seats[0].Places) != 1 {
		t.Errorf("other machine %+v", ph)
	}
	// since bounds the samples.
	var late struct {
		Seats []struct {
			Samples []any `json:"samples"`
		} `json:"seats"`
	}
	h.get("/v1/usage?since="+now.Add(-20*time.Minute).Format(time.RFC3339), machine, &late)
	if n := len(late.Seats[0].Samples); n != 2 {
		t.Errorf("since: %d samples, want the 2 of the last reading", n)
	}
}

func leaseRec(t *testing.T, machine string, at time.Time, id, sid string, seat bool) []byte {
	t.Helper()
	m := record.Meta{Machine: machine, Client: "wy/0.6.0", Now: at}
	if seat {
		m.Seat = &record.Seat{Account: acctUUID, Org: orgUUID, Src: "claude_json"}
	}
	p := record.AccountLeasePayload{LeaseID: id, SessionID: sid, Kind: record.LeaseAggressive, C5: 8, CF: 8, Strategy: "lanes",
		Folder: "~/.claude-2", Batch: "01JB0000000000000000000000"}
	var r *record.Record
	if seat {
		var err error
		if r, err = record.NewAccountLease(m, p); err != nil {
			t.Fatal(err)
		}
	} else { // a record the hub cannot type: no seat
		r = record.New(record.KindAccountLease, m)
		if err := r.SetPayload(p); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := r.Encode()
	return b
}

func tokenRec(t *testing.T, machine, sid string, at time.Time, input, output int64) []byte {
	t.Helper()
	r, err := record.NewTokenRows(record.Meta{Machine: machine, Client: "wy/0.6.0", Now: at}, record.TokenRowsPayload{
		Rows: []record.TokenRow{{Key: "msg-" + sid + at.Format("150405"), TS: at.Format(time.RFC3339Nano), SessionID: sid, Input: input, Output: output}}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

// GET /v1/usage lists other machines' leases of the seat, with launched, ended and burned
// units from their sessions, and never their session id or machine (setup.md 8.5).
func TestUsageLeases(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	phone := h.token("phone")
	other := startHubDevice(t, h, "machine")
	now := time.Now().UTC().Truncate(time.Second)
	const sid = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	h.ingest(machine, slotLoginRec(t, h.machine, "~/.claude-2", acctUUID, now.Add(-3*time.Hour)),
		leaseRec(t, h.machine, now.Add(-30*time.Minute), "01JB0000000000000000000001", "", true))
	h.ingest(other.token,
		slotLoginRec(t, other.machine, "~/.claude-9", acctUUID, now.Add(-3*time.Hour)),
		tokenRec(t, other.machine, sid, now.Add(-50*time.Minute), 1000, 1000), // before the pick: not burned
		leaseRec(t, other.machine, now.Add(-40*time.Minute), "01JB0000000000000000000002", sid, true),
		leaseRec(t, other.machine, now.Add(-40*time.Minute), "01JB0000000000000000000002", sid, true), // resent: one row
		leaseRec(t, other.machine, now.Add(-20*time.Minute), "01JB0000000000000000000003", "", true),
		leaseRec(t, other.machine, now.Add(-20*time.Minute), "01JB0000000000000000000004", "", false),
		leaseRec(t, other.machine, now.Add(-8*24*time.Hour), "01JB0000000000000000000005", "", true), // too old
		simpleHook(t, other.machine, sid, "SessionStart", now.Add(-39*time.Minute)),
		tokenRec(t, other.machine, sid, now.Add(-35*time.Minute), 100, 20),
		simpleHook(t, other.machine, sid, "SessionEnd", now.Add(-10*time.Minute)))
	if n := h.count(`SELECT count(*) FROM account_lease`); n != 4 {
		t.Errorf("%d lease rows, want 4 (one per lease id; the seatless one is not typed)", n)
	}
	if n := h.count(`SELECT count(*) FROM record WHERE kind = 'account_lease' AND typed = 0 AND type_error IS NOT NULL`); n != 1 {
		t.Errorf("%d untyped lease records, want 1", n)
	}
	code, body := h.do("GET", "/v1/usage", machine, nil)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(string(body), sid) || strings.Contains(string(body), other.machine) {
		t.Errorf("reply names a session or machine: %s", body)
	}
	var doc struct {
		Seats []struct {
			Leases []map[string]any `json:"leases"`
		} `json:"seats"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Seats) != 1 {
		t.Fatalf("%v %s", err, body)
	}
	ls := doc.Seats[0].Leases
	if len(ls) != 2 {
		t.Fatalf("leases %v, want the other machine's 2 of the last 7 days", ls)
	}
	var keys []string
	for k := range ls[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if got := strings.Join(keys, ","); got != "at,burned_units,c5,cf,ended,has_session,kind,launched,seen,urgent" {
		t.Errorf("lease keys %s", got)
	}
	a, b := ls[0], ls[1]
	if a["has_session"] != true || a["launched"] != true || a["ended"] != true || a["seen"] != true ||
		a["burned_units"] != 200.0 || a["kind"] != "aggressive" || a["c5"] != 8.0 || a["urgent"] != false {
		t.Errorf("first lease %v (burned = 100 + 5 × 20)", a)
	}
	if b["has_session"] != false || b["launched"] != false || b["seen"] != false || b["burned_units"] != 0.0 {
		t.Errorf("second lease %v", b)
	}
	var ph struct {
		Seats []struct {
			Leases []any `json:"leases"`
		} `json:"seats"`
	}
	h.get("/v1/usage", phone, &ph)
	if len(ph.Seats[0].Leases) != 3 {
		t.Errorf("a token with no machine sees %d leases, want all 3", len(ph.Seats[0].Leases))
	}
}
