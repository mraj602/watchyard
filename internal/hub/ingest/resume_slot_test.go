package ingest_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

// identity.md 5.3: a session resumed in another folder of its machine (`claude --resume`
// in a second account that shares projects/ with the first) moves to that folder and seat.

const (
	rsMachine = "m_01HZZZZZZZZZZZZZZZZZZZZZZR"
	rsSID     = "22222222-3333-4444-5555-666666666666"
)

var (
	rsSeat1 = &record.Seat{Account: "aaaaaaaa-0000-4000-8000-000000000001", Org: "bbbbbbbb-0000-4000-8000-000000000001", Email: "one@example.com"}
	rsSeat2 = &record.Seat{Account: "aaaaaaaa-0000-4000-8000-000000000002", Org: "bbbbbbbb-0000-4000-8000-000000000002", Email: "two@example.com"}
)

func rsHook(t *testing.T, slot string, seat *record.Seat, payload string, at time.Time) *record.Record {
	t.Helper()
	r, err := record.NewHookEvent(record.Meta{Machine: rsMachine, Slot: slot, Seat: seat, Client: "wy/0.5.0", Now: at}, []byte(payload), record.HookOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rsList(t *testing.T, slot string, at time.Time, ids ...string) *record.Record {
	t.Helper()
	var items []json.RawMessage
	for _, id := range ids {
		b, _ := json.Marshal(map[string]any{"sessionId": id, "cwd": "/home/a/proj", "status": "idle"})
		items = append(items, b)
	}
	r, err := record.NewSessionList(record.Meta{Machine: rsMachine, Slot: slot, Client: "wy/0.5.0", Now: at},
		record.SessionListPayload{Complete: true, Source: "test", Sessions: items})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rsIngest(t *testing.T, f *fixture, d store.Device, recs ...*record.Record) {
	t.Helper()
	f.mu.Lock()
	for _, r := range recs { // the hub's clock is past every record (it clamps future ts)
		if at, err := r.Time(); err == nil && at.After(f.now) {
			f.now = at
		}
	}
	f.mu.Unlock()
	rep, err := f.ing.Ingest(f.ctx, d, body(t, recs...), nil)
	if err != nil || len(rep.Accepted) != len(recs) {
		t.Fatal(err, rep)
	}
}

func rsSession(t *testing.T, f *fixture, at time.Time) store.SessionView {
	t.Helper()
	v, err := f.st.Session(f.ctx, rsSID, at)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func rsStart(t *testing.T, f *fixture) store.Device {
	t.Helper()
	d := f.device("R", rsMachine)
	rsIngest(t, f, d,
		rsHook(t, "~/.claude", rsSeat1, `{"session_id":"`+rsSID+`","hook_event_name":"SessionStart","source":"startup","cwd":"/home/a/proj"}`, t0),
		rsHook(t, "~/.claude", rsSeat1, `{"session_id":"`+rsSID+`","hook_event_name":"Stop","cwd":"/home/a/proj"}`, t0.Add(time.Minute)),
		rsHook(t, "~/.claude", rsSeat1, `{"session_id":"`+rsSID+`","hook_event_name":"SessionEnd","reason":"prompt_input_exit","cwd":"/home/a/proj"}`, t0.Add(2*time.Minute)),
	)
	return d
}

func TestResumeInAnotherFolderMovesSession(t *testing.T) {
	f := newFixture(t)
	d := rsStart(t, f)
	before := rsSession(t, f, t0.Add(3*time.Minute))
	if before.Slot == nil || *before.Slot != "~/.claude" || before.Seat == nil {
		t.Fatalf("before: slot = %v, seat = %v", before.Slot, before.Seat)
	}

	resumed := t0.Add(10 * time.Minute)
	rsIngest(t, f, d,
		rsHook(t, "~/.claude-6", rsSeat2, `{"session_id":"`+rsSID+`","hook_event_name":"SessionStart","source":"resume","cwd":"/home/a/proj"}`, resumed),
		rsHook(t, "~/.claude-6", rsSeat2, `{"session_id":"`+rsSID+`","hook_event_name":"UserPromptSubmit","cwd":"/home/a/proj"}`, resumed.Add(time.Second)),
	)
	v := rsSession(t, f, resumed.Add(time.Minute))
	if v.Slot == nil || *v.Slot != "~/.claude-6" {
		t.Fatalf("slot = %v, want ~/.claude-6", *v.Slot)
	}
	if v.Seat == nil || *v.Seat == *before.Seat {
		t.Fatalf("seat = %v, want the second account's seat (was %s)", v.Seat, *before.Seat)
	}
	if v.State != "working" {
		t.Fatalf("state = %s, want working", v.State)
	}

	// The old folder's complete lists no longer name it; they must not make it gone. The new
	// folder's lists do name it.
	rsIngest(t, f, d,
		rsList(t, "~/.claude", resumed.Add(5*time.Minute)),
		rsList(t, "~/.claude-6", resumed.Add(5*time.Minute), rsSID),
		rsList(t, "~/.claude", resumed.Add(10*time.Minute)),
		rsList(t, "~/.claude-6", resumed.Add(10*time.Minute), rsSID),
	)
	v = rsSession(t, f, resumed.Add(11*time.Minute))
	if v.State == "gone" || v.State == "ended" {
		t.Fatalf("state = %s: the old folder's lists ended the resumed session", v.State)
	}

	// A late record (older than the newest event) refolds the session: the old folder's
	// lists after the move still do not count, and the session stays in the new folder.
	rsIngest(t, f, d,
		rsHook(t, "~/.claude", rsSeat1, `{"session_id":"`+rsSID+`","hook_event_name":"Notification","notification_type":"idle_prompt","cwd":"/home/a/proj"}`, t0.Add(90*time.Second)),
	)
	v = rsSession(t, f, resumed.Add(11*time.Minute))
	if v.Slot == nil || *v.Slot != "~/.claude-6" {
		t.Fatalf("after a late record: slot = %v, want ~/.claude-6", v.Slot)
	}
	if v.State == "gone" || v.State == "ended" {
		t.Fatalf("after refold: state = %s", v.State)
	}
}

func TestReloginInSameFolderKeepsSessionSeat(t *testing.T) {
	f := newFixture(t)
	d := rsStart(t, f)
	before := rsSession(t, f, t0.Add(3*time.Minute))
	// The same folder now logged in to another account: the session keeps its seat.
	rsIngest(t, f, d,
		rsHook(t, "~/.claude", rsSeat2, `{"session_id":"`+rsSID+`","hook_event_name":"SessionStart","source":"resume","cwd":"/home/a/proj"}`, t0.Add(10*time.Minute)),
	)
	v := rsSession(t, f, t0.Add(11*time.Minute))
	if v.Seat == nil || before.Seat == nil || *v.Seat != *before.Seat {
		t.Fatalf("seat = %v, want %v", v.Seat, before.Seat)
	}
}
