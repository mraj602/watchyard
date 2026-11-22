package record

import (
	"math"
	"testing"
	"time"
)

func TestAccountLease(t *testing.T) {
	ok := AccountLeasePayload{LeaseID: "01JB0000000000000000000001", SessionID: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
		Kind: LeaseProtected, C5: 8, CF: 8, Strategy: "lanes", Folder: "~/.claude-3"}
	m := Meta{Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", Slot: "~/.claude-3", Now: time.Now(),
		Seat: &Seat{Account: "a", Org: "o", Src: "claude_json"}}
	r, err := NewAccountLease(m, ok)
	if err != nil || r.Kind != KindAccountLease || r.Slot != "" || r.Seat == nil || r.Validate() != nil {
		t.Fatalf("record %+v err %v", r, err)
	}
	for name, p := range map[string]AccountLeasePayload{
		"lease id": {LeaseID: "x", Kind: LeaseAggressive, Strategy: "lanes"},
		"session":  {LeaseID: ok.LeaseID, SessionID: "a b", Kind: LeaseAggressive, Strategy: "lanes"},
		"kind":     {LeaseID: ok.LeaseID, Kind: "normal", Strategy: "lanes"},
		"charge":   {LeaseID: ok.LeaseID, Kind: LeaseAggressive, Strategy: "lanes", C5: math.NaN()},
		"negative": {LeaseID: ok.LeaseID, Kind: LeaseAggressive, Strategy: "lanes", CF: -1},
		"strategy": {LeaseID: ok.LeaseID, Kind: LeaseAggressive},
	} {
		if _, err := NewAccountLease(m, p); err == nil {
			t.Errorf("%s: accepted %+v", name, p)
		}
	}
	m.Seat = nil
	if _, err := NewAccountLease(m, ok); err == nil {
		t.Error("a lease without a seat was accepted")
	}
}
