package api_test

import (
	"encoding/json"
	"testing"
	"time"
)

// Off and the owner's drain (setup.md 8.5, Kind and Owner drain; api.yaml SeatPatch and
// putSeatDrain): mode off from the owner only; drain_until from the owner by PATCH, from a
// machine by PUT .../drain; an account that is off cannot drain, and off ends a drain.
func TestSeatOffAndDrain(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, usageDoc(t), nil))
	id := h.onlySeat(owner).ID
	path := "/v1/accounts/" + id
	until := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	in := func(d time.Duration) string { return `"` + time.Now().Add(d).UTC().Format(time.RFC3339) + `"` }
	seat := func(tok string) (mode string, drain *time.Time) {
		t.Helper()
		code, b := h.do("GET", "/v1/accounts", tok, nil)
		var acc struct {
			Accounts []struct {
				Mode       *string    `json:"mode"`
				DrainUntil *time.Time `json:"drain_until"`
			} `json:"accounts"`
		}
		if code != 200 || json.Unmarshal(b, &acc) != nil || len(acc.Accounts) != 1 {
			t.Fatalf("%d %s", code, b)
		}
		if m := acc.Accounts[0].Mode; m != nil {
			mode = *m
		}
		return mode, acc.Accounts[0].DrainUntil
	}
	for _, c := range []struct {
		name, method, p, tok, body string
		want                       int
	}{
		{"machine cannot set off", "PATCH", path, machine, `{"mode":"off"}`, 403},
		{"drain in the past", "PATCH", path, owner, `{"drain_until":` + in(-time.Hour) + `}`, 400},
		{"drain too far", "PATCH", path, owner, `{"drain_until":` + in(9*24*time.Hour) + `}`, 400},
		{"drain not a time", "PATCH", path, owner, `{"drain_until":"tonight"}`, 400},
		{"drain and off together", "PATCH", path, owner, `{"mode":"off","drain_until":` + in(time.Hour) + `}`, 400},
		{"read token cannot drain", "PUT", path + "/drain", h.token("phone"), `{"until":` + in(time.Hour) + `}`, 403},
		{"bad drain body", "PUT", path + "/drain", machine, `{"at":` + in(time.Hour) + `}`, 400},
		{"machine drains", "PUT", path + "/drain", machine, `{"until":"` + until.Format(time.RFC3339) + `"}`, 200},
	} {
		if code, b := h.do(c.method, c.p, c.tok, []byte(c.body)); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, b, c.want)
		}
	}
	if m, d := seat(owner); m != "" || d == nil || !d.Equal(until) {
		t.Fatalf("after drain: %q %v", m, d)
	}
	// Off ends the drain and keeps the account's mode for later.
	if code, b := h.do("PATCH", path, owner, []byte(`{"mode":"protected"}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := h.do("PATCH", path, owner, []byte(`{"mode":"off"}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if m, d := seat(owner); m != "off" || d != nil {
		t.Fatalf("off: %q %v", m, d)
	}
	if code, b := h.do("PUT", path+"/drain", machine, []byte(`{"until":`+in(time.Hour)+`}`)); code != 400 {
		t.Fatalf("drain while off: %d %s", code, b)
	}
	// On again: the kept mode; then the owner drains and stops it.
	if code, b := h.do("PATCH", path, owner, []byte(`{"mode":"protected"}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b := h.do("PATCH", path, owner, []byte(`{"drain_until":`+in(time.Hour)+`}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if m, d := seat(owner); m != "protected" || d == nil {
		t.Fatalf("drain: %q %v", m, d)
	}
	if code, b := h.do("PUT", path+"/drain", machine, []byte(`{"until":null}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if _, d := seat(owner); d != nil {
		t.Fatalf("stopped: %v", d)
	}
}
