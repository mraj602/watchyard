package status

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/record"
)

// identity.md 5.3: a session resumed in another folder moves there, with that folder's
// seat, and the old folder's complete lists no longer make it gone.
func TestAdaptMovesResumedSession(t *testing.T) {
	const sid = "33333333-4444-5555-6666-777777777777"
	seat1 := &record.Seat{Account: "aaaaaaaa-0000-4000-8000-000000000001", Org: "bbbbbbbb-0000-4000-8000-000000000001"}
	seat2 := &record.Seat{Account: "aaaaaaaa-0000-4000-8000-000000000002", Org: "bbbbbbbb-0000-4000-8000-000000000002"}
	hook := func(slot string, seat *record.Seat, payload string, at time.Time) *record.Record {
		r, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: slot, Seat: seat, Now: at, Client: "wy/test"}, []byte(payload), record.HookOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	list := func(slot string, at time.Time, ids ...string) *record.Record {
		var items []json.RawMessage
		for _, id := range ids {
			b, _ := json.Marshal(map[string]any{"sessionId": id, "status": "idle"})
			items = append(items, b)
		}
		r, err := record.NewSessionList(record.Meta{Machine: machine, Slot: slot, Now: at},
			record.SessionListPayload{Complete: true, Source: "test", Sessions: items})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	resumed := t0.Add(10 * time.Minute)
	recs := []*record.Record{
		hook("~/.claude", seat1, `{"session_id":"`+sid+`","hook_event_name":"SessionStart","source":"startup"}`, t0),
		list("~/.claude", t0.Add(time.Minute), sid),
		hook("~/.claude", seat1, `{"session_id":"`+sid+`","hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`, t0.Add(2*time.Minute)),
		hook("~/.claude-6", seat2, `{"session_id":"`+sid+`","hook_event_name":"SessionStart","source":"resume"}`, resumed),
		hook("~/.claude-6", seat2, `{"session_id":"`+sid+`","hook_event_name":"Stop"}`, resumed.Add(time.Second)),
		list("~/.claude", resumed.Add(5*time.Minute)),
		list("~/.claude-6", resumed.Add(5*time.Minute), sid),
		list("~/.claude", resumed.Add(10*time.Minute)),
		list("~/.claude-6", resumed.Add(10*time.Minute), sid),
	}
	s := Adapt(recs)[sid]
	if s == nil {
		t.Fatal("no session")
	}
	if s.Slot != "~/.claude-6" || s.Seat == nil || s.Seat.Account != seat2.Account {
		t.Fatalf("slot = %q, seat = %+v; want ~/.claude-6 and the second account", s.Slot, s.Seat)
	}
	var f state.Session
	for _, in := range s.Inputs {
		f.Apply(in)
	}
	if f.State == state.Gone || f.State == state.Ended {
		t.Fatalf("state = %s: the old folder's lists ended the resumed session", f.State)
	}
}
