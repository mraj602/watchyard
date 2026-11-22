package engine

import (
	"context"
	"errors"
	"io"
	"log"
	"time"

	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/push/action"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Snooze moves the alert's next push (push.md 4): allowed in pending, fired and snoozed;
// minutes 0 means the snooze_min setting. by is the acting device (logged only).
func (e *Engine) Snooze(ctx context.Context, alertID string, minutes int, by string) (model.AlertBrief, error) {
	if minutes != 0 && (minutes < 5 || minutes > 240) {
		return model.AlertBrief{}, errors.New("minutes must be 5-240")
	}
	return e.act(ctx, func(w *work) (model.AlertBrief, error) {
		a, err := w.tx.AlertGet(alertID)
		if errors.Is(err, store.ErrNotFound) {
			return model.AlertBrief{}, model.ErrNotFound
		}
		if err != nil {
			return model.AlertBrief{}, err
		}
		if !a.Status.Open() {
			return a.AlertBrief, model.ErrConflict
		}
		return w.snooze(a, minutes)
	})
}

func (w *work) snooze(a model.Alert, minutes int) (model.AlertBrief, error) {
	if minutes == 0 {
		minutes = w.env.Settings.SnoozeMin
	}
	until := model.Time(w.nowMS + int64(minutes)*60_000)
	a.Status, a.SnoozedUntil, a.DueAt = model.StatusSnoozed, until, until
	if err := w.tx.AlertSave(a, w.nowMS); err != nil {
		return model.AlertBrief{}, err
	}
	w.event("snoozed", a)
	return a.AlertBrief, nil
}

// Seen marks the alert seen (push.md 4): allowed in pending, fired and snoozed, and on a
// skipped alert whose episode is still open; seen again is a no-op. by is the device id
// (or "ntfy:<target id>").
func (e *Engine) Seen(ctx context.Context, alertID, by string) (model.AlertBrief, error) {
	return e.act(ctx, func(w *work) (model.AlertBrief, error) {
		a, err := w.tx.AlertGet(alertID)
		if errors.Is(err, store.ErrNotFound) {
			return model.AlertBrief{}, model.ErrNotFound
		}
		if err != nil {
			return model.AlertBrief{}, err
		}
		b, _, err := w.seen(a, by)
		return b, err
	})
}

// seen applies Seen; changed is false when it was already seen.
func (w *work) seen(a model.Alert, by string) (model.AlertBrief, bool, error) {
	switch {
	case a.Status == model.StatusSeen:
		return a.AlertBrief, false, nil
	case a.Status.Open():
	case a.Status == model.StatusSkipped:
		open, err := w.episodeOpen(a)
		if err != nil {
			return a.AlertBrief, false, err
		}
		if !open {
			return a.AlertBrief, false, model.ErrConflict
		}
	default:
		return a.AlertBrief, false, model.ErrConflict
	}
	a.Status, a.SeenAt, a.SeenBy, a.DueAt = model.StatusSeen, model.Time(w.nowMS), by, 0
	if err := w.tx.AlertSave(a, w.nowMS); err != nil {
		return a.AlertBrief, false, err
	}
	if err := w.tx.DeliveriesSkipUnsent(a.ID, rSeen, w.nowMS); err != nil {
		return a.AlertBrief, false, err
	}
	if err := w.dismiss(a); err != nil {
		return a.AlertBrief, false, err
	}
	w.event("seen", a)
	return a.AlertBrief, true, nil
}

// episodeOpen reports whether the condition an alert is about still holds.
func (w *work) episodeOpen(a model.Alert) (bool, error) {
	switch {
	case rules.IsSessionTrigger(a.Trigger):
		r, found, err := w.tx.AlertSession(deref(a.Session))
		if err != nil {
			return false, err
		}
		_, want := rules.SessionWant(sessionNow(deref(a.Session), r, found))
		return want == a.Episode, nil
	case rules.IsWindowTrigger(a.Trigger):
		return int64(a.Facts.ResetsAt) > w.nowMS, nil
	case a.Trigger == model.TriggerMachineSilent:
		ms, err := w.tx.AlertMachines([]string{deref(a.Machine)}, w.nowMS)
		if err != nil || len(ms) == 0 {
			return false, err
		}
		return rules.MachineEpisode(machineNow(ms[0])) == a.Episode, nil
	}
	return false, nil
}

// Act applies an action token from a push button (push.md 8). Seen is idempotent; snooze
// applies only while the alert is fired with the token's fire_count, so a replay or a tap
// after the re-push does nothing (applied false).
func (e *Engine) Act(ctx context.Context, token string) (model.AlertBrief, bool, error) {
	// The MAC and the expiry are checked before any write transaction: a bad or expired
	// token (the only thing an unauthenticated caller can send) never takes the write lock.
	key, err := e.actionKey(ctx)
	if err != nil {
		return model.AlertBrief{}, false, err
	}
	tok, err := action.Verify(key, token, e.clk.Now())
	if err != nil {
		return model.AlertBrief{}, false, err
	}
	applied := false
	b, err := e.act(ctx, func(w *work) (model.AlertBrief, error) {
		a, err := w.tx.AlertGet(tok.Alert)
		if errors.Is(err, store.ErrNotFound) {
			return model.AlertBrief{}, model.ErrNotFound
		}
		if err != nil {
			return model.AlertBrief{}, err
		}
		switch tok.Action {
		case action.Seen:
			by, err := w.tx.AlertTargetDevice(tok.Target)
			if err != nil {
				return model.AlertBrief{}, err
			}
			if by == "" {
				by = "ntfy:" + tok.Target
			}
			b, changed, err := w.seen(a, by)
			if errors.Is(err, model.ErrConflict) {
				return a.AlertBrief, nil // nothing to do: the episode is over
			}
			applied = changed
			return b, err
		case action.Snooze:
			if a.Status != model.StatusFired || uint32(a.FireCount) != tok.Fire {
				return a.AlertBrief, nil
			}
			applied = true
			return w.snooze(a, 0)
		}
		return a.AlertBrief, model.ErrBadToken
	})
	return b, applied && err == nil, err // a failed commit rolls the change back
}

// actionKey returns the action-token key. It never changes once made, so it is read once
// (making it on first use, as render does) and kept.
func (e *Engine) actionKey(ctx context.Context) ([]byte, error) {
	e.keyMu.Lock()
	defer e.keyMu.Unlock()
	if e.actKey != nil {
		return e.actKey, nil
	}
	var key []byte
	err := e.st.Write(ctx, func(tx *store.Tx) (err error) { key, err = tx.ActionKey(); return })
	if err != nil {
		return nil, err
	}
	e.actKey = key
	return key, nil
}

// act runs fn in one write transaction, publishes its events and wakes the scheduler.
func (e *Engine) act(ctx context.Context, fn func(w *work) (model.AlertBrief, error)) (model.AlertBrief, error) {
	var out model.AlertBrief
	var evs []events.Event
	err := e.st.Write(ctx, func(tx *store.Tx) error {
		w, err := e.newWork(tx, e.clk.Now())
		if err != nil {
			return err
		}
		b, err := fn(w)
		out = b
		if err != nil {
			return err
		}
		evs = w.evs
		return nil
	})
	if err != nil {
		return out, err
	}
	e.publish(evs)
	e.Poke()
	return out, nil
}

// Now is the engine's clock reading (handlers render times with it).
func (e *Engine) Now() time.Time { return e.clk.Now() }

// SeenSession marks a session's current Needs-you episode seen (push.md 4, "Seen on a
// session"): Seen on the episode's alert when it has one, else that alert is created as seen
// so reconcile never makes (or pushes) one for the episode. ErrNotFound for an unknown
// session, ErrConflict when it does not need you now.
func (e *Engine) SeenSession(ctx context.Context, sessionID, by string) (model.AlertBrief, error) {
	return e.act(ctx, func(w *work) (model.AlertBrief, error) {
		a, err := w.seenSession(sessionID, by)
		return a.AlertBrief, err
	})
}

// SeenSessionNoEngine is SeenSession for a hub that runs without the engine (pushes off):
// the same change in one transaction of st, returning the stream events for the caller to
// publish. No delivery can be pending and nothing is sent.
func SeenSessionNoEngine(ctx context.Context, st *store.Store, now time.Time, sessionID, by string) (model.Alert, []events.Event, error) {
	var out model.Alert
	var evs []events.Event
	err := st.Write(ctx, func(tx *store.Tx) error {
		w := &work{e: &Engine{log: log.New(io.Discard, "", 0)}, tx: tx, now: now, nowMS: now.UnixMilli()}
		a, err := w.seenSession(sessionID, by)
		out, evs = a, w.evs
		return err
	})
	if err != nil {
		evs = nil
	}
	return out, evs, err
}

func (w *work) seenSession(sessionID, by string) (model.Alert, error) {
	r, found, err := w.tx.AlertSession(sessionID)
	if err != nil {
		return model.Alert{}, err
	}
	if !found {
		return model.Alert{}, model.ErrNotFound
	}
	cur := sessionNow(sessionID, r, true)
	trig, want := rules.SessionWant(cur)
	if want == "" {
		return model.Alert{}, model.ErrConflict
	}
	have, err := w.tx.AlertsByEpisode([]string{want})
	if err != nil {
		return model.Alert{}, err
	}
	if len(have) > 0 {
		a := have[0]
		b, _, err := w.seen(a, by)
		a.AlertBrief = b
		return a, err
	}
	a := model.Alert{
		AlertBrief: model.AlertBrief{ID: store.NewID("al_", w.now), Trigger: trig, Status: model.StatusSeen,
			SeenAt: model.Time(w.nowMS)},
		Session: optStr(sessionID), Seat: optStr(cur.Seat), Machine: optStr(cur.Machine),
		SinceAt: model.Time(cur.SinceMS), CreatedAt: model.Time(w.nowMS), Episode: want, Facts: cur.Facts,
		SeenBy: by,
	}
	ok, err := w.tx.AlertInsert(a)
	if err != nil {
		return a, err
	}
	if !ok {
		// Made by a pass between the read and the insert: Seen on that one.
		have, err := w.tx.AlertsByEpisode([]string{want})
		if err != nil || len(have) == 0 {
			return a, err
		}
		b, _, err := w.seen(have[0], by)
		have[0].AlertBrief = b
		return have[0], err
	}
	w.event("seen", a)
	return a, nil
}

// optStr is nil for "", else &s (the alert's optional columns).
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
