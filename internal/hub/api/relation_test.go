package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

func verdictRec(t *testing.T, machine string, at time.Time, eligible bool, reason string) []byte {
	t.Helper()
	lvl, rank, w := 2, 1, 61.5
	free := at.Add(3 * time.Hour).Format(time.RFC3339)
	a := record.AccountVerdict{Account: acctUUID, Org: orgUUID, Kind: record.LeaseProtected, Eligible: eligible, WeeklyPct: &w}
	if eligible {
		a.Rank = &rank
	} else {
		a.Reason, a.SkipLevel, a.FreeAt = reason, &lvl, &free
	}
	p := record.AccountVerdictsPayload{Strategy: "lanes", Accounts: []record.AccountVerdict{a,
		{Account: "unknown-acct", Org: orgUUID, Eligible: true}}}
	r, err := record.NewAccountVerdicts(record.Meta{Machine: machine, Client: "wy/0.6.0", Now: at}, p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

// GET /v1/accounts/{seat}/relation: 14 days of the hub's zone, no calibration before the
// job's first run, and the newest verdict of any machine (setup.md 8.5; record.md 4.11).
func TestSeatRelation(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	phone := h.token("phone")
	other := startHubDevice(t, h, "machine")
	now := time.Now().UTC().Truncate(time.Second)
	h.ingest(machine, slotLoginRec(t, h.machine, "~/.claude-2", acctUUID, now.Add(-3*time.Hour)),
		verdictRec(t, h.machine, now.Add(-10*time.Minute), false, "7d 92% >= pace 85"))
	h.ingest(other.token, verdictRec(t, other.machine, now.Add(-20*time.Minute), true, "")) // older: ignored
	var seats struct {
		Accounts []struct{ ID string } `json:"accounts"`
	}
	h.get("/v1/accounts", phone, &seats)
	if len(seats.Accounts) != 1 {
		t.Fatalf("seats = %+v", seats)
	}
	seat := seats.Accounts[0].ID
	var doc map[string]json.RawMessage
	h.get("/v1/accounts/"+seat+"/relation", phone, &doc)
	for _, k := range []string{"seat", "time", "tz", "today", "days", "heavy", "models", "calibration", "verdict"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("no %q", k)
		}
	}
	var days []map[string]any
	_ = json.Unmarshal(doc["days"], &days)
	if len(days) != 14 || string(doc["calibration"]) != "null" {
		t.Fatalf("days %d calibration %s", len(days), doc["calibration"])
	}
	var v map[string]any
	_ = json.Unmarshal(doc["verdict"], &v)
	if v["eligible"] != false || v["reason"] != "7d 92% >= pace 85" || v["skip_level"] != 2.0 || v["machine"] != h.machine ||
		v["strategy"] != "lanes" || v["kind"] != "protected" || v["weekly_pct"] != 61.5 || v["rank"] != nil || v["free_at"] == nil {
		t.Fatalf("verdict = %v", v)
	}
	if _, ok := v["account"]; ok {
		t.Fatal("the verdict names the login")
	}
	if n := h.count(`SELECT count(*) FROM seat_verdict`); n != 1 {
		t.Fatalf("%d verdict rows (the unknown account is skipped)", n)
	}
	if code, _ := h.do("GET", "/v1/accounts/seat_nope/relation", phone, nil); code != http.StatusNotFound {
		t.Fatalf("unknown seat: %d", code)
	}
	if code, _ := h.do("GET", "/v1/accounts/"+seat+"/relation", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
}
