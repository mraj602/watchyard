package refresh_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/push/clock"
	pushlink "github.com/mraj602/watchyard/internal/hub/push/link"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/refresh"
	"github.com/mraj602/watchyard/internal/hub/store"
)

var gNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

type rig struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	clk *clock.Fake
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clk := clock.NewFake(gNow)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"), store.Options{Now: clk.Now, NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &rig{t: t, ctx: context.Background(), st: st, clk: clk}
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if err := r.st.Write(r.ctx, func(tx *store.Tx) error { _, err := tx.ExecContext(r.ctx, q, args...); return err }); err != nil {
		r.t.Fatal(err)
	}
}

// device adds a phone device with prefs (JSON or "").
func (r *rig) device(id, prefs string) {
	r.t.Helper()
	var p any
	if prefs != "" {
		p = prefs
	}
	r.exec(`INSERT INTO device (id, kind, name, token_hash, token_hint, scopes, created_at, created_by, prefs)
		VALUES (?, 'phone', ?, randomblob(32), 'h', '["read","push"]', 1, 'test', ?)`, id, id, p)
}

// target makes the device's link target, as pairing does.
func (r *rig) target(device string) model.Target {
	r.t.Helper()
	id := "pt_" + device
	err := r.st.Write(r.ctx, func(tx *store.Tx) error {
		return tx.UpsertLinkTarget(id, device, device+" · app", json.RawMessage(`{"p256dh":"p","auth":"a"}`), tx.NowMS())
	})
	if err != nil {
		r.t.Fatal(err)
	}
	tg, err := r.st.TargetByID(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return tg
}

// recSender records refresh sends and answers scripted results (default sent).
type recSender struct {
	mu   sync.Mutex
	clk  func() time.Time
	got  []recSent
	plan []model.Result
}

type recSent struct {
	At     time.Time
	Target string
	Msg    model.Message
}

func (s *recSender) Kind() string { return model.KindLink }

func (s *recSender) Send(_ context.Context, t model.Target, m model.Message) model.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, recSent{At: s.clk(), Target: t.ID, Msg: m})
	if len(s.plan) > 0 {
		r := s.plan[0]
		s.plan = s.plan[1:]
		return r
	}
	return model.Result{Outcome: model.OutcomeSent}
}

func (s *recSender) script(rs ...model.Result) {
	s.mu.Lock()
	s.plan = append(s.plan, rs...)
	s.mu.Unlock()
}

func (s *recSender) sends() []recSent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recSent(nil), s.got...)
}

type refreshRig struct {
	*rig
	broker *events.Broker
	fs     *recSender
	rf     *refresh.Refresher
	tgt    model.Target
	logs   *bytes.Buffer
	t0     time.Time
}

func newRefreshRig(t *testing.T, sender model.Sender) *refreshRig {
	t.Helper()
	r := newRig(t)
	h := &refreshRig{rig: r, broker: events.New(r.clk.Now), fs: &recSender{clk: r.clk.Now}, logs: &bytes.Buffer{}, t0: r.clk.Now()}
	r.device("dev_w", `{"widgets": true}`)
	r.device("dev_off", `{"widgets": false}`)
	h.tgt = r.target("dev_w")
	r.target("dev_off")
	r.exec(`INSERT INTO account (uuid, first_seen, last_seen) VALUES ('acc', 1, 1)`)
	r.exec(`INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org', 1, 1)`)
	r.exec(`INSERT INTO seat (id, account_uuid, org_uuid, created_at) VALUES ('seat_a', 'acc', 'org', 1)`)
	r.exec(`INSERT INTO usage_window (seat_id, window_key, resets_at, final_pct, peak_pct, updated_at) VALUES
		('seat_a', 'five_hour', ?, 62, 62, 1), ('seat_a', 'seven_day', ?, 41, 41, 1)`,
		h.t0.Add(3*time.Hour).UnixMilli(), h.t0.Add(72*time.Hour).UnixMilli())
	r.exec(`INSERT INTO session (id) VALUES ('s1'), ('s2')`)
	r.exec(`INSERT INTO session_state (session_id, state, since_at, needs_you, updated_at) VALUES
		('s1', 'asked', 1, 1, 1), ('s2', 'working', 1, 0, 1)`)
	if sender == nil {
		sender = h.fs
	}
	h.rf = refresh.NewRefresher(refresh.RefresherOptions{Store: r.st, Sender: sender, Broker: h.broker, Clock: r.clk,
		Log: log.New(h.logs, "", 0)})
	h.rf.Start()
	t.Cleanup(h.rf.Stop)
	return h
}

// at moves the clock to t0 + d, letting the refresher see each of its timers on the way.
func (h *refreshRig) at(d time.Duration) {
	h.t.Helper()
	target := h.t0.Add(d)
	for {
		h.rf.Sync()
		next, ok := h.clk.NextTimer()
		if !ok || h.clk.Now().Add(next).After(target) {
			break
		}
		h.clk.Advance(next)
	}
	if rest := target.Sub(h.clk.Now()); rest > 0 {
		h.clk.Advance(rest)
	}
	h.rf.Sync()
}

func (h *refreshRig) publish(typ string) {
	h.broker.Publish(events.Event{Type: typ, Data: []byte(`{}`)})
	h.rf.Sync()
}

func (h *refreshRig) setPct(key string, pct float64) {
	h.exec(`UPDATE usage_window SET final_pct = ? WHERE seat_id = 'seat_a' AND window_key = ?`, pct, key)
}

func (h *refreshRig) expect(n int) []recSent {
	h.t.Helper()
	got := h.fs.sends()
	if len(got) != n {
		var b strings.Builder
		for _, s := range got {
			b.WriteString("\n  " + s.At.Sub(h.t0).String() + " " + s.Target)
		}
		h.t.Fatalf("%d refreshes, want %d:%s", len(got), n, b.String())
	}
	return got
}

func TestRefresherDebounceDigestAndMinimum(t *testing.T) {
	h := newRefreshRig(t, nil)
	h.at(time.Hour)
	h.expect(0) // nothing without a change

	// A change: one refresh 10 s later, to the widgets device only.
	h.publish("usage")
	h.at(time.Hour + 9*time.Second)
	h.expect(0)
	h.at(time.Hour + 10*time.Second)
	got := h.expect(1)
	p := got[0].Msg.Payload
	if got[0].Target != h.tgt.ID || p.Type != model.TypeRefresh || p.NeedsYou != 1 || p.V != 1 ||
		p.TS != model.Time(h.t0.Add(time.Hour+10*time.Second).UnixMilli()) || len(got[0].Msg.Data) == 0 {
		t.Fatalf("%+v", got[0])
	}

	// Events that change nothing the widget shows, or less than a 5-point bucket: none.
	h.publish("session")
	h.publish("machine") // not a refresh trigger at all
	h.setPct("five_hour", 64)
	h.publish("usage")
	h.at(time.Hour + 30*time.Second)
	h.expect(1)

	// A ring moves 10 points within 15 minutes: none yet; it goes out once 15 minutes have
	// passed since the last refresh.
	h.setPct("five_hour", 74)
	h.publish("usage")
	h.at(time.Hour + 14*time.Minute)
	h.expect(1)
	h.at(time.Hour + 15*time.Minute + 9*time.Second)
	h.expect(1)
	h.at(time.Hour + 15*time.Minute + 10*time.Second)
	h.expect(2)

	// The needs-you count changes: a refresh after the minimum.
	h.exec(`UPDATE session_state SET needs_you = 1, state = 'asked' WHERE session_id = 's2'`)
	h.publish("session")
	h.at(time.Hour + 31*time.Minute)
	got = h.expect(3)
	if got[2].Msg.Payload.NeedsYou != 2 || !got[2].At.Equal(h.t0.Add(time.Hour+30*time.Minute+10*time.Second)) {
		t.Fatalf("%+v at %v", got[2].Msg.Payload, got[2].At.Sub(h.t0))
	}
	// The log has counts, never the digest.
	if strings.Contains(h.logs.String(), "n=") || strings.Contains(h.logs.String(), "seat_a") || !strings.Contains(h.logs.String(), "1 sent") {
		t.Fatalf("log: %s", h.logs.String())
	}
}

func TestRefresherResetAndSetting(t *testing.T) {
	h := newRefreshRig(t, nil)
	// refresh_min 30.
	if err := h.st.PutPushSetting(h.ctx, store.AlertKeyPushSettings, []byte(`{"refresh_min": 30}`)); err != nil {
		t.Fatal(err)
	}
	h.publish("usage")
	h.at(10 * time.Second)
	h.expect(1)
	h.setPct("five_hour", 90)
	h.publish("usage")
	h.at(29 * time.Minute)
	h.expect(1)
	h.at(30*time.Minute + 10*time.Second)
	h.expect(2)
	// The five-hour window resets at 3 h with no event at all: one refresh then.
	h.at(3*time.Hour - time.Second)
	h.expect(2)
	h.at(3 * time.Hour)
	got := h.expect(3)
	if !got[2].At.Equal(h.t0.Add(3 * time.Hour)) {
		t.Fatal(got[2].At.Sub(h.t0))
	}
	// A restart (new refresher, empty digests) sends at most one refresh per target.
	h.rf.Stop()
	h.rf = refresh.NewRefresher(refresh.RefresherOptions{Store: h.st, Sender: h.fs, Broker: h.broker, Clock: h.clk})
	h.rf.Start()
	t.Cleanup(h.rf.Stop)
	h.publish("usage")
	h.publish("session")
	h.at(3*time.Hour + time.Minute)
	h.expect(4)
}

func TestRefresherGoneAndFailures(t *testing.T) {
	h := newRefreshRig(t, nil)
	// A failed try is retried after the minimum (not at every event).
	h.fs.script(model.Result{Outcome: model.OutcomeRetry, HTTPStatus: 503, Err: "HTTP 503"})
	h.publish("usage")
	h.at(10 * time.Second)
	h.expect(1)
	if g, _ := h.st.TargetByID(h.ctx, h.tgt.ID); g.LastError != "HTTP 503" {
		t.Fatalf("%+v", g)
	}
	h.publish("usage")
	h.at(5 * time.Minute)
	h.expect(1)
	h.at(15*time.Minute + 10*time.Second)
	h.expect(2)
	// Gone disables the target; no more refreshes.
	h.setPct("five_hour", 99)
	h.fs.script(model.Result{Outcome: model.OutcomeGone, Err: "the device has no link keys or was removed"})
	h.publish("usage")
	h.at(31 * time.Minute)
	h.expect(3)
	if g, _ := h.st.TargetByID(h.ctx, h.tgt.ID); g.GoneAt == 0 || g.Enabled {
		t.Fatalf("%+v", g)
	}
	h.setPct("five_hour", 10)
	h.publish("usage")
	h.at(2 * time.Hour)
	h.expect(3)
	// A revoked device or widgets off: nothing either.
	h.exec(`UPDATE device SET prefs = '{"widgets": true}' WHERE id = 'dev_off'`)
	h.exec(`UPDATE device SET revoked_at = 1 WHERE id = 'dev_off'`)
	h.publish("usage")
	h.at(3 * time.Hour)
	h.expect(3)
}

// A frozen hub (a move in progress) sends no widget refresh; the pass runs once it is
// unfrozen.
func TestRefresherSkipsWhileFrozen(t *testing.T) {
	h := newRefreshRig(t, nil)
	h.exec(`INSERT INTO setting (key, value, updated_at) VALUES ('hub.frozen', '{"since": 1}', 1)`)
	h.publish("usage")
	h.at(time.Minute)
	h.expect(0)
	h.exec(`DELETE FROM setting WHERE key = 'hub.frozen'`)
	h.at(time.Minute + 11*time.Second)
	h.expect(1)
}

// queue records what the link sender enqueues.
type queue struct {
	mu  sync.Mutex
	got []queued
}

type queued struct {
	device, typ, tag string
	body             []byte
}

func (q *queue) Enqueue(_ context.Context, device, t, tag string, _ time.Duration, body []byte, _ string) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.got = append(q.got, queued{device, t, tag, append([]byte(nil), body...)})
	return int64(len(q.got)), nil
}

// TestRefresherThroughLinkSender sends a real refresh through the link sender: one `refresh`
// frame for the widgets device carrying the snapshot (link.md 6.4).
func TestRefresherThroughLinkSender(t *testing.T) {
	h := newRefreshRig(t, nil)
	h.rf.Stop()
	q := &queue{}
	h.rf = refresh.NewRefresher(refresh.RefresherOptions{Store: h.st, Sender: pushlink.New(q), Broker: h.broker, Clock: h.clk})
	h.rf.Start()
	t.Cleanup(h.rf.Stop)
	h.publish("session")
	h.at(10 * time.Second)
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.got) != 1 || q.got[0].device != "dev_w" || q.got[0].typ != "refresh" || q.got[0].tag != "refresh" {
		t.Fatalf("%+v", q.got)
	}
	var snap struct {
		V        int `json:"v"`
		NeedsYou int `json:"needs_you"`
		Seats    []struct {
			Seat    string  `json:"seat"`
			Window  string  `json:"window"`
			UsedPct float64 `json:"used_pct"`
		} `json:"seats"`
	}
	if err := json.Unmarshal(q.got[0].body, &snap); err != nil || snap.V != 1 || snap.NeedsYou != 1 || len(snap.Seats) != 2 ||
		snap.Seats[0].Seat != "seat_a" || snap.Seats[0].Window != "five_hour" || snap.Seats[0].UsedPct != 62 {
		t.Fatalf("%s %v", q.got[0].body, err)
	}
}

// A seen alert lowers the needs-you count, so its event starts a refresh; other alert
// changes do not.
func TestRefresherSeenEvent(t *testing.T) {
	h := newRefreshRig(t, nil)
	h.at(time.Hour)
	h.broker.Publish(events.Event{Type: "alert", Data: []byte(`{"change":"fired"}`)})
	h.rf.Sync()
	h.at(time.Hour + 20*time.Second)
	h.expect(0)
	h.exec(`UPDATE session_state SET needs_you = 0 WHERE session_id = 's1'`)
	h.broker.Publish(events.Event{Type: "alert", Data: []byte(`{"change":"seen"}`)})
	h.rf.Sync()
	h.at(time.Hour + 30*time.Second)
	got := h.expect(1)
	if got[0].Msg.Payload.NeedsYou != 0 {
		t.Fatalf("%+v", got[0].Msg.Payload)
	}
}
