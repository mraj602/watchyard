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
	if got := strings.Join(keysOf(seats[0]), ","); got != "account,id,label,live_sessions,org,places,samples,signed_out,windows" {
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
