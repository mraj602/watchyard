package calibrate

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/calib"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
)

type jobHub struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	got []model.Notice
}

func newJobHub(t *testing.T) *jobHub {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &jobHub{t: t, ctx: ctx, st: st}
	h.exec(`INSERT INTO account (uuid, email, first_seen, last_seen) VALUES ('acc1', 'alex@work.dev', 1, 1), ('acc2', 'alex@home.dev', 1, 1)`)
	h.exec(`INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org1', 1, 1)`)
	h.exec(`INSERT INTO seat (id, account_uuid, org_uuid, label, created_at) VALUES ('seat_w', 'acc1', 'org1', 'work', 1),
		('seat_p', 'acc2', 'org1', 'personal', 2)`)
	h.exec(`INSERT INTO setting (key, value, updated_at) VALUES ('hub.tz', '"UTC"', 1)`)
	return h
}

func (h *jobHub) exec(q string, args ...any) {
	h.t.Helper()
	if err := h.st.Write(h.ctx, func(tx *store.Tx) error { _, err := tx.ExecContext(h.ctx, q, args...); return err }); err != nil {
		h.t.Fatal(err)
	}
}

var (
	jBase      = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	jFiveReset = time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC)
	jWeekReset = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
)

// reading inserts one reading of seat_w: source web (sid "") or a status line of session sid.
func (h *jobHub) reading(min int, sid string, five, seven float64) {
	h.t.Helper()
	ts := jBase.Add(time.Duration(min) * time.Minute).UnixMilli()
	src, sess := "statusline", any(sid)
	if sid == "" {
		src, sess = "web", nil
	}
	h.exec(`INSERT INTO limit_sample (seat_id, window_key, ts, source, used_pct, resets_at, session_id, record_id) VALUES
		('seat_w', 'five_hour', ?, ?, ?, ?, ?, 'r'), ('seat_w', 'seven_day', ?, ?, ?, ?, ?, 'r')`,
		ts, src, five, jFiveReset.UnixMilli(), sess, ts, src, seven, jWeekReset.UnixMilli(), sess)
}

func (h *jobHub) run(at time.Time) {
	h.t.Helper()
	if err := Run(h.ctx, h.st, at, func(_ context.Context, ns []model.Notice) error {
		h.got = append(h.got, ns...)
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *jobHub) state() (*calib.State, *calib.Watch, *calib.Relation) {
	h.t.Helper()
	rows, err := h.st.CalibrationRows(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	ws, err := h.st.WatchRows(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	s := calib.NewState()
	for _, r := range rows {
		if r.Seat == "seat_w" {
			_ = json.Unmarshal(r.State, s)
		}
	}
	w, rel, _ := loadWatch(ws["seat_w"])
	return s, w, rel
}

// The 2026-10-17 pattern through the job: three sessions see both windows back to 0 with the
// same weekly reset, one session keeps the old values. One usage_reset notice; calibration
// holds while it is undecided and makes no pair across the mask.
func TestJobServerReset(t *testing.T) {
	h := newJobHub(t)
	// Rising use before, every 10 minutes from 10:00, with token rows.
	for i, m := range []int{-120, -110, -100, -90, -80, -70, -60} {
		h.reading(m, "s1", 20+float64(i)*3, 60+float64(i)*0.5)
		h.exec(`INSERT INTO token_row (key, session_id, seat_id, ts, model, input, output, record_id) VALUES (?, 's1', 'seat_w', ?, 'claude-opus-4-6', 100000, 20000, 'r')`,
			"k"+string(rune('a'+i)), jBase.Add(time.Duration(m-5)*time.Minute).UnixMilli())
	}
	h.reading(0, "", 0, 0)
	// The job runs between the first low reading and the others: the drop waits, the fold holds.
	h.run(jBase.Add(5 * time.Minute))
	if len(h.got) != 0 {
		t.Fatalf("decided early: %+v", h.got)
	}
	_, w, _ := h.state()
	if w.Hold() != jBase.UnixMilli() {
		t.Fatalf("hold = %d", w.Hold())
	}
	h.reading(6, "s1", 0, 0)
	h.reading(8, "s2", 38, 63) // stale
	h.reading(20, "s3", 1, 1)
	h.reading(40, "s2", 38, 63)
	h.reading(50, "s1", 2, 1.5)
	h.run(jBase.Add(30 * time.Minute))
	if len(h.got) != 1 || h.got[0].Trigger != model.TriggerUsageReset || h.got[0].Seat != "seat_w" ||
		h.got[0].SinceMS != jBase.UnixMilli() || h.got[0].Facts.Sessions != 3 || h.got[0].Facts.Seat != "work" ||
		int64(h.got[0].Facts.ResetsAt) != jWeekReset.UnixMilli() {
		t.Fatalf("notices = %+v", h.got)
	}
	_, w, _ = h.state()
	if len(w.Masks) != 1 || w.Masks[0].To != jFiveReset.UnixMilli() {
		t.Fatalf("masks = %+v", w.Masks)
	}
	// Later runs: nothing new, and no calibration pair spans the mask.
	h.reading(5*60+10, "s1", 5, 2)
	h.reading(5*60+30, "s1", 8, 2.5)
	h.run(jBase.Add(7 * time.Hour))
	if len(h.got) != 1 {
		t.Fatalf("notices after = %+v", h.got)
	}
	s, _, rel := h.state()
	for _, p := range s.Pairs {
		if p.T0 < jFiveReset.UnixMilli() && p.T1 > jBase.UnixMilli() {
			t.Fatalf("a pair spans the mask: %+v", p)
		}
	}
	if len(rel.Days) == 0 || rel.Days[0].Units["Opus 4.6"] == 0 {
		t.Fatalf("relation = %+v", rel.Days)
	}
}

// A folder that logs in to another account is a seat_change, handed at every run (the engine
// makes it one alert).
func TestJobSeatChange(t *testing.T) {
	h := newJobHub(t)
	h.exec(`INSERT INTO machine (id, name, first_seen) VALUES ('m_a', 'gpu-box', 1)`)
	h.exec(`INSERT INTO slot (id, machine_id, folder, first_seen, last_seen) VALUES (1, 'm_a', '~/.claude-2', 1, 1)`)
	since := jBase.Add(-time.Hour).UnixMilli()
	h.exec(`INSERT INTO slot_login (slot_id, since_at, seat_id, until_at, source_record) VALUES
		(1, ?, 'seat_p', ?, 'r'), (1, ?, 'seat_w', NULL, 'r')`, since-86_400_000, since, since)
	h.run(jBase)
	if len(h.got) != 1 {
		t.Fatalf("notices = %+v", h.got)
	}
	n := h.got[0]
	if n.Trigger != model.TriggerSeatChange || n.Seat != "seat_w" || n.Machine != "m_a" || n.SinceMS != since ||
		n.Facts.Was != "personal" || n.Facts.Seat != "work" || n.Facts.Machine != "gpu-box" || n.Facts.Folder != "~/.claude-2" ||
		n.Facts.OrgOnly || n.Episode != "c:1:"+itoa(since) {
		t.Fatalf("notice = %+v", n)
	}
}

func itoa(v int64) string { b, _ := json.Marshal(v); return string(b) }
