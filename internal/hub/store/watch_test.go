package store_test

import (
	"testing"

	"github.com/mraj602/watchyard/internal/hub/store"
)

func watchSeats(t *testing.T) (*store.Store, func(q string, args ...any)) {
	st, ctx := openTargets(t)
	ex := func(q string, args ...any) { t.Helper(); execT(t, st, ctx, q, args...) }
	ex(`INSERT INTO account (uuid, first_seen, last_seen) VALUES ('acc1', 1, 1), ('acc2', 1, 1)`)
	ex(`INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org1', 1, 1), ('org2', 1, 1)`)
	ex(`INSERT INTO seat (id, account_uuid, org_uuid, created_at) VALUES ('seat_a', 'acc1', 'org1', 1), ('seat_b', 'acc2', 'org1', 1),
		('seat_c', 'acc1', 'org2', 1)`)
	ex(`INSERT INTO machine (id, name, first_seen) VALUES ('m_a', 'gpu-box', 1), ('m_b', 'laptop', 1)`)
	return st, ex
}

func TestSeatWatchRows(t *testing.T) {
	st, _ := watchSeats(t)
	ctx := t.Context()
	if _, ok, err := st.SeatRelation(ctx, "seat_a"); ok || err != nil {
		t.Fatalf("relation before a run: %v %v", ok, err)
	}
	if err := st.Write(ctx, func(tx *store.Tx) error { return tx.SaveWatch("seat_a", 42, []byte(`{"v":1}`), []byte(`{"days":[]}`)) }); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *store.Tx) error { return tx.SaveWatch("seat_a", 43, []byte(`{"v":1}`), []byte(`{"days":[1]}`)) }); err != nil {
		t.Fatal(err)
	}
	rows, err := st.WatchRows(ctx)
	if err != nil || len(rows) != 1 || rows["seat_a"].Cursor != 43 {
		t.Fatalf("rows = %+v %v", rows, err)
	}
	if rel, ok, err := st.SeatRelation(ctx, "seat_a"); !ok || err != nil || string(rel) != `{"days":[1]}` {
		t.Fatalf("relation = %s %v %v", rel, ok, err)
	}
	if err := st.Write(ctx, func(tx *store.Tx) error { return tx.SaveWatch("seat_a", 1, []byte(`{`), []byte(`{}`)) }); err == nil {
		t.Fatal("invalid JSON stored")
	}
}

// The newest verdict about a seat wins, whichever machine sent it.
func TestSeatVerdictNewest(t *testing.T) {
	st, _ := watchSeats(t)
	ctx := t.Context()
	put := func(machine string, ts int64, v string) {
		t.Helper()
		if err := st.Write(ctx, func(tx *store.Tx) error {
			return tx.PutSeatVerdict("seat_a", machine, ts, "lanes", []byte(v), "r")
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := st.SeatVerdict(ctx, "seat_a"); ok || err != nil {
		t.Fatal("verdict before any")
	}
	put("m_a", 200, `{"eligible":true}`)
	put("m_b", 100, `{"eligible":false}`) // older: kept out
	v, ok, err := st.SeatVerdict(ctx, "seat_a")
	if !ok || err != nil || v.Machine != "m_a" || v.MachineName != "gpu-box" || v.TS != 200 || string(v.Verdict) != `{"eligible":true}` {
		t.Fatalf("verdict = %+v %v %v", v, ok, err)
	}
	put("m_b", 300, `{"eligible":false}`)
	if v, _, _ = st.SeatVerdict(ctx, "seat_a"); v.Machine != "m_b" || v.TS != 300 {
		t.Fatalf("newer verdict = %+v", v)
	}
}

func TestSeatChanges(t *testing.T) {
	st, ex := watchSeats(t)
	ctx := t.Context()
	ex(`INSERT INTO slot (id, machine_id, folder, first_seen, last_seen) VALUES (1, 'm_a', '~/.claude-2', 1, 1), (2, 'm_b', '~/.claude', 1, 1)`)
	// Folder 1: seat_a, logged out, seat_b (account change), seat_c (another account: seat_c is acc1).
	ex(`INSERT INTO slot_login (slot_id, since_at, seat_id, until_at, source_record) VALUES
		(1, 1000, 'seat_a', 2000, 'r'), (1, 2000, NULL, 3000, 'r'), (1, 3000, 'seat_b', 4000, 'r'), (1, 4000, 'seat_c', NULL, 'r'),
		(2, 1500, 'seat_a', 2500, 'r'), (2, 2500, 'seat_a', NULL, 'r')`)
	cs, err := st.SeatChanges(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("changes = %+v", cs)
	}
	c := cs[0]
	if c.Slot != 1 || c.Since != 3000 || c.Seat != "seat_b" || c.PrevSeat != "seat_a" || c.Machine != "m_a" || c.MachineName != "gpu-box" || c.Folder != "~/.claude-2" ||
		c.Account != "acc2" || c.PrevAccount != "acc1" {
		t.Fatalf("change = %+v", c)
	}
	if cs[1].Seat != "seat_c" || cs[1].PrevSeat != "seat_b" || cs[1].Org != "org2" {
		t.Fatalf("second = %+v", cs[1])
	}
	if cs, _ := st.SeatChanges(ctx, 3500); len(cs) != 1 || cs[0].Since != 4000 {
		t.Fatalf("since 3500 = %+v", cs)
	}
}

func TestAlertEpisodeSince(t *testing.T) {
	st, ex := watchSeats(t)
	ctx := t.Context()
	ex(`INSERT INTO alert (id, episode, trigger, status, created_at, updated_at) VALUES
		('al_1', 'x:Opus 4.6:up:2026-11-10', 'limit_change', 'fired', 1000, 1000)`)
	for _, c := range []struct {
		prefix string
		since  int64
		want   bool
	}{
		{"x:Opus 4.6:up:", 500, true},
		{"x:Opus 4.6:up:", 1500, false},
		{"x:Opus 4.6:down:", 500, false},
		{"x:Opus 4:", 500, false},
	} {
		if got, err := st.AlertEpisodeSince(ctx, c.prefix, c.since); err != nil || got != c.want {
			t.Errorf("%q since %d = %v %v, want %v", c.prefix, c.since, got, err, c.want)
		}
	}
}
