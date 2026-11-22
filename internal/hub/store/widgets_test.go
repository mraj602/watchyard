package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
)

func execT(t *testing.T, st *store.Store, ctx context.Context, q string, args ...any) {
	t.Helper()
	if err := st.Write(ctx, func(tx *store.Tx) error { _, err := tx.ExecContext(ctx, q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

func TestLinkRefreshTargets(t *testing.T) {
	st, ctx := openTargets(t)
	for _, id := range []string{"dev_1", "dev_2", "dev_3", "dev_4", "dev_5"} {
		addDevice(t, st, ctx, id)
	}
	on := `{"widgets": true, "private": true}`
	execT(t, st, ctx, `UPDATE device SET prefs = ? WHERE id IN ('dev_1', 'dev_3', 'dev_4', 'dev_5')`, on)
	execT(t, st, ctx, `UPDATE device SET prefs = '{"widgets": false}' WHERE id = 'dev_2'`)
	execT(t, st, ctx, `UPDATE device SET revoked_at = 5 WHERE id = 'dev_3'`)
	execT(t, st, ctx, `UPDATE device SET expires_at = 50 WHERE id = 'dev_4'`)
	keys := json.RawMessage(`{"p256dh":"p","auth":"a"}`)
	mk := func(dev string) model.Target {
		t.Helper()
		id := "pt_" + dev
		if err := st.Write(ctx, func(tx *store.Tx) error { return tx.UpsertLinkTarget(id, dev, dev+" · app", keys, 1) }); err != nil {
			t.Fatal(err)
		}
		tg, err := st.TargetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}
	t1 := mk("dev_1")
	mk("dev_2") // widgets off
	mk("dev_3") // revoked
	mk("dev_4") // expired at 50
	t5 := mk("dev_5")
	_ = st.ApplyTargetResult(ctx, t5.ID, model.Result{Outcome: model.OutcomeGone})
	// A webpush target of a widgets device is not a link target.
	_, _ = st.UpsertTarget(ctx, model.Target{Kind: model.KindWebPush, DeviceID: "dev_1", Endpoint: "https://fcm.googleapis.com/y"})

	got, err := st.LinkRefreshTargets(ctx, 100)
	if err != nil || len(got) != 1 || got[0].ID != t1.ID || got[0].Prefs == nil || !got[0].Prefs.WidgetsOn() {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := st.LinkRefreshTargets(ctx, 10); len(got) != 2 { // dev_4 not yet expired
		t.Fatalf("%+v", got)
	}
	_, _ = st.SetChannel(ctx, model.KindLink, false, nil)
	if got, _ := st.LinkRefreshTargets(ctx, 100); len(got) != 0 {
		t.Fatalf("channel off: %+v", got)
	}
}

func TestWidgetState(t *testing.T) {
	st, ctx := openTargets(t)
	execT(t, st, ctx, `INSERT INTO account (uuid, first_seen, last_seen) VALUES ('acc', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org1', 1, 1), ('org2', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO seat (id, account_uuid, org_uuid, created_at) VALUES ('seat_a', 'acc', 'org1', 1)`)
	execT(t, st, ctx, `INSERT INTO seat (id, account_uuid, org_uuid, created_at, hidden) VALUES ('seat_h', 'acc', 'org2', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO usage_window (seat_id, window_key, resets_at, final_pct, peak_pct, updated_at) VALUES
		('seat_a', 'five_hour', 5000, 62.5, 70, 1), ('seat_a', 'seven_day', 90000, NULL, 41, 1),
		('seat_a', 'five_hour', 500, 99, 99, 1), ('seat_a', 'opus', 5000, 10, 10, 1), ('seat_h', 'five_hour', 5000, 80, 80, 1)`)
	execT(t, st, ctx, `INSERT INTO session (id) VALUES ('s1'), ('s2'), ('s3')`)
	execT(t, st, ctx, `INSERT INTO session_state (session_id, state, since_at, needs_you, updated_at) VALUES
		('s1', 'asked', 1, 1, 1), ('s2', 'working', 1, 0, 1), ('s3', 'gone', 1, 1, 1)`)
	s, err := st.WidgetState(ctx, 1000)
	if err != nil || s.NeedsYou != 1 || len(s.Windows) != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	if w := s.Windows[0]; w.Seat != "seat_a" || w.Key != "five_hour" || w.Pct != 62.5 || w.ResetsAt != 5000 {
		t.Fatalf("%+v", w)
	}
	if w := s.Windows[1]; w.Key != "seven_day" || w.Pct != 41 {
		t.Fatalf("%+v", w)
	}
}

// A seen alert on the session's current episode takes it out of the needs-you counts (widget
// refresh, badge, overview) until its state changes; another episode's seen alert does not.
func TestSeenLowersNeedsYouCounts(t *testing.T) {
	st, ctx := openTargets(t)
	execT(t, st, ctx, `INSERT INTO session (id) VALUES ('s1'), ('s2')`)
	execT(t, st, ctx, `INSERT INTO session_state (session_id, state, since_at, needs_you, updated_at) VALUES
		('s1', 'asked', 10, 1, 1), ('s2', 'asked', 20, 1, 1)`)
	count := func() (widget, badge, overview int) {
		t.Helper()
		w, err := st.WidgetState(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Write(ctx, func(tx *store.Tx) error { var e error; badge, e = tx.AlertNeedsYou(); return e }); err != nil {
			t.Fatal(err)
		}
		c, err := st.Counts(ctx, time.UnixMilli(1000))
		if err != nil {
			t.Fatal(err)
		}
		return w.NeedsYou, badge, c.NeedsYou
	}
	if w, b, o := count(); w != 2 || b != 2 || o != 2 {
		t.Fatalf("start: %d %d %d", w, b, o)
	}
	execT(t, st, ctx, `INSERT INTO alert (id, episode, trigger, session_id, status, created_at, updated_at) VALUES
		('al_1', 's:s1:asked:10', 'asked', 's1', 'seen', 1, 1),
		('al_2', 's:s2:asked:5', 'asked', 's2', 'seen', 1, 1)`) // al_2 is an older episode
	if w, b, o := count(); w != 1 || b != 1 || o != 1 {
		t.Fatalf("s1 seen: %d %d %d", w, b, o)
	}
	execT(t, st, ctx, `UPDATE session_state SET since_at = 30 WHERE session_id = 's1'`) // a new episode
	if w, b, o := count(); w != 2 || b != 2 || o != 2 {
		t.Fatalf("state changed: %d %d %d", w, b, o)
	}
}
