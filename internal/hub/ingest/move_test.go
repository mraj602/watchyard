package ingest_test

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

// identity.md 5.3: a session resumed under another config folder moves to that slot and its
// seat on its first live record there; the seat it left is kept for the peers alias, its
// token rows stay where they were, and a late record of the old slot does not move it back.
func TestSessionMovesToAnotherSlot(t *testing.T) {
	f := newFixture(t)
	f.now = t0.Add(time.Hour)
	dev := f.device("A", xmA)
	hook := func(slot, account, event string, at time.Time) *record.Record {
		t.Helper()
		r, err := record.NewHookEvent(record.Meta{Machine: xmA, Slot: slot, Seat: &record.Seat{Account: account, Org: "org-1"},
			Client: "wy/1.3.2", Now: at}, []byte(`{"session_id":"`+xmSID+`","hook_event_name":"`+event+`","cwd":"/home/a/proj","source":"resume"}`), record.HookOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	f.ingest(dev, hook("~/.claude-4", "acct-4", "SessionStart", t0), hook("~/.claude-4", "acct-4", "Stop", t0.Add(time.Minute)))
	f.ingest(dev, hook("~/.claude-2", "acct-2", "SessionStart", t0.Add(10*time.Minute)))
	// A late record of the old slot (older than what the hub has seen) changes nothing.
	f.ingest(dev, hook("~/.claude-4", "acct-4", "Notification", t0.Add(2*time.Minute)))

	var row store.SessionRow
	var seat2, from string
	if err := f.st.Write(f.ctx, func(tx *store.Tx) error {
		var err error
		if row, _, err = tx.GetSession(xmSID); err != nil {
			return err
		}
		from, err = tx.SessionMovedFrom(xmSID)
		if err != nil {
			return err
		}
		seat2, _, err = tx.SeatByLogin("acct-2", "org-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if row.SeatID != seat2 || seat2 == "" || from == "" || from == seat2 {
		t.Fatalf("session seat %q (want %q), moved from %q", row.SeatID, seat2, from)
	}
	ps, err := f.st.PeerSessions(f.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Account != "acct-2" || ps[0].MovedFromAccount != "acct-4" || ps[0].SlotFolder != "~/.claude-2" {
		t.Fatalf("peer sessions %+v", ps)
	}
}

// setup.md 8.6: a long session moved three times (4 -> 2 -> 5 -> 4) keeps every seat it left
// as an alias source (session_move), and the board shows the last move and the count.
func TestSessionMovesSeveralTimes(t *testing.T) {
	f := newFixture(t)
	f.now = t0.Add(time.Hour)
	dev := f.device("A", xmA)
	hook := func(slot, account string, at time.Time) *record.Record {
		t.Helper()
		r, err := record.NewHookEvent(record.Meta{Machine: xmA, Slot: slot, Seat: &record.Seat{Account: account, Org: "org-1"},
			Client: "wy/1.3.2", Now: at}, []byte(`{"session_id":"`+xmSID+`","hook_event_name":"SessionStart","cwd":"/home/a/proj","source":"resume"}`), record.HookOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	f.ingest(dev, hook("~/.claude-4", "acct-4", t0))
	f.ingest(dev, hook("~/.claude-2", "acct-2", t0.Add(10*time.Minute)))
	f.ingest(dev, hook("~/.claude-5", "acct-5", t0.Add(20*time.Minute)))
	f.ingest(dev, hook("~/.claude-4", "acct-4", t0.Add(30*time.Minute)))
	ps, err := f.st.PeerSessions(f.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Account != "acct-4" || ps[0].MovedFromAccount != "acct-5" {
		t.Fatalf("peer sessions %+v", ps)
	}
	if len(ps[0].Past) != 1 || ps[0].Past[0].Account != "acct-2" {
		t.Fatalf("past seats %+v (want acct-2 only: acct-5 is the last, acct-4 the current)", ps[0].Past)
	}
	v, err := f.st.Session(f.ctx, xmSID, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Moves != 3 || v.MovedFromSeat == nil || v.MovedAt == 0 {
		t.Fatalf("board view: moves %d from %v at %v", v.Moves, v.MovedFromSeat, v.MovedAt)
	}
}
