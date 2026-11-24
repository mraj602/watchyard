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
