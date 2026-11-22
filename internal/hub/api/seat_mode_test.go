package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

// slotInfoModeRec is a slot_info record of the test login with a `mode` (record.md 4.7).
func slotInfoModeRec(t *testing.T, machine string, at time.Time, mode string) []byte {
	t.Helper()
	extra := record.Extra{}
	extra["mode"], _ = json.Marshal(mode)
	oa, _ := json.Marshal(map[string]string{"accountUuid": acctUUID, "emailAddress": "a@example.com", "organizationUuid": orgUUID})
	r, err := record.NewSlotInfo(record.Meta{Machine: machine, Slot: "~/.claude", Client: "wy/0.6.0", Now: at},
		record.SlotInfoPayload{LoggedIn: true, OAuthAccount: oa, Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Encode()
	return b
}

// seatModes reads the seat's mode from GET /v1/accounts, GET /v1/overview and GET /v1/usage
// and fails unless all three say want ("" for null).
func seatModes(t *testing.T, h *hub, owner, machine, want string) {
	t.Helper()
	modeOf := func(raw json.RawMessage) string {
		var m struct {
			Mode *string `json:"mode"`
		}
		var keys map[string]json.RawMessage
		_ = json.Unmarshal(raw, &keys)
		if _, ok := keys["mode"]; !ok {
			t.Fatalf("no mode key in %s", raw)
		}
		_ = json.Unmarshal(raw, &m)
		if m.Mode == nil {
			return ""
		}
		return *m.Mode
	}
	var acc struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	h.get("/v1/accounts?include_hidden=true", owner, &acc)
	var ov struct {
		Seats []json.RawMessage `json:"seats"`
	}
	h.get("/v1/overview", owner, &ov)
	var us struct {
		Seats []json.RawMessage `json:"seats"`
	}
	h.get("/v1/usage", machine, &us)
	if len(acc.Accounts) != 1 || len(ov.Seats) != 1 || len(us.Seats) != 1 {
		t.Fatalf("seats: %d accounts, %d overview, %d usage", len(acc.Accounts), len(ov.Seats), len(us.Seats))
	}
	for name, raw := range map[string]json.RawMessage{"accounts": acc.Accounts[0], "overview": ov.Seats[0], "usage": us.Seats[0]} {
		if got := modeOf(raw); got != want {
			t.Fatalf("%s: mode %q, want %q", name, got, want)
		}
	}
}

// PATCH /v1/accounts/{seat} mode (setup.md 8.5, Kind; api.yaml SeatPatch): protected,
// aggressive or null, owner only, anything else 400; every Seat and UsageSeat carries it.
func TestPatchSeatMode(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, usageDoc(t), nil))
	id := h.onlySeat(owner).ID
	path := "/v1/accounts/" + id
	seatModes(t, h, owner, machine, "")

	for _, c := range []struct {
		name, tok, body string
		want            int
	}{
		{"read token", h.token("phone"), `{"mode":"protected"}`, 403},
		{"machine token", machine, `{"mode":"protected"}`, 403},
		{"bad mode", owner, `{"mode":"normal"}`, 400},
		{"empty mode", owner, `{"mode":""}`, 400},
		{"number", owner, `{"mode":1}`, 400},
		{"protected", owner, `{"mode":"protected"}`, 200},
	} {
		if code, b := h.do("PATCH", path, c.tok, []byte(c.body)); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, code, b, c.want)
		}
	}
	seatModes(t, h, owner, machine, "protected")
	if n := h.count(`SELECT count(*) FROM seat WHERE id = ? AND pick_mode_src = 'ui'`, id); n != 1 {
		t.Fatal("pick_mode_src is not 'ui'")
	}
	// The answer is the Seat with its mode.
	code, b := h.do("PATCH", path, owner, []byte(`{"mode":"aggressive"}`))
	var s struct {
		Mode *string `json:"mode"`
	}
	if code != 200 || json.Unmarshal(b, &s) != nil || s.Mode == nil || *s.Mode != "aggressive" {
		t.Fatalf("%d %s", code, b)
	}
	// A label patch leaves the mode; null clears it.
	if code, b := h.do("PATCH", path, owner, []byte(`{"label":"work"}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	seatModes(t, h, owner, machine, "aggressive")
	if code, b := h.do("PATCH", path, owner, []byte(`{"mode":null}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	seatModes(t, h, owner, machine, "")
	if n := h.count(`SELECT count(*) FROM seat WHERE id = ? AND pick_mode IS NULL AND pick_mode_src = 'ui'`, id); n != 1 {
		t.Fatal("Not chosen does not keep pick_mode_src 'ui'")
	}
	// The owner's Not chosen is a choice: a slot_info proposal does not fill it.
	h.ingest(machine, slotInfoModeRec(t, h.machine, t0.Add(time.Minute), "protected"))
	seatModes(t, h, owner, machine, "")
}

// A slot_info mode fills seat.pick_mode only while it is NULL (record.md 4.7): the first
// machine's proposal wins, a later one or a UI choice is never overridden, other values are
// ignored.
func TestSlotInfoModeFillsOnlyWhenUnset(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, usageDoc(t), nil))
	id := h.onlySeat(owner).ID

	h.ingest(machine, slotInfoModeRec(t, h.machine, t0.Add(time.Minute), "normal"))
	seatModes(t, h, owner, machine, "")
	h.ingest(machine, slotInfoModeRec(t, h.machine, t0.Add(2*time.Minute), "aggressive"))
	seatModes(t, h, owner, machine, "aggressive")
	if n := h.count(`SELECT count(*) FROM seat WHERE id = ? AND pick_mode_src = 'slot'`, id); n != 1 {
		t.Fatal("pick_mode_src is not 'slot'")
	}
	h.ingest(machine, slotInfoModeRec(t, h.machine, t0.Add(3*time.Minute), "protected"))
	seatModes(t, h, owner, machine, "aggressive")

	if code, b := h.do("PATCH", "/v1/accounts/"+id, owner, []byte(`{"mode":"protected"}`)); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	h.ingest(machine, slotInfoModeRec(t, h.machine, t0.Add(4*time.Minute), "aggressive"))
	seatModes(t, h, owner, machine, "protected")
	if n := h.count(`SELECT count(*) FROM seat WHERE id = ? AND pick_mode_src = 'ui'`, id); n != 1 {
		t.Fatal("pick_mode_src is not 'ui'")
	}
}
