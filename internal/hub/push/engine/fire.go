package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/push/action"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Retry schedule of push.md 5.6: after the 1st, 2nd, 3rd, 4th failed try; the 5th failure
// is final.
var retryBackoff = []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}

// MaxRetryAfter caps a Retry-After header.
const MaxRetryAfter = time.Hour

// Delivery statuses and reasons (schema v3 delivery).
const (
	dPending = "pending"
	dHeld    = "held"
	dSent    = "sent"
	dFailed  = "failed"
	dSkipped = "skipped"
	dGone    = "gone"

	rMuted      = "muted"
	rQuiet      = "quiet"
	rFlood      = "flood"
	rResolved   = "resolved"
	rSeen       = "seen"
	rChannelOff = "channel_off"
	rStale      = "stale"
)

// push is one message to one target: one alert, or a summary of several.
type push struct {
	target model.Target
	alerts []model.Alert
	ds     []store.DeliveryRow
	msg    model.Message
	res    model.Result
	// sent is set once the send has an outcome (it returned, or it was started and did not
	// return within the pass budget, which counts as a timed-out try). A push that was never
	// started (pass budget used up, or over the per-pass cap) is not written back: its
	// deliveries stay pending with their retry slot and go out in a later pass.
	sent bool
	// started is set when a worker took the push.
	started bool
}

type firing struct {
	a    model.Alert
	rule model.Rule
}

// pass is one scheduler wake-up (push.md 3.6).
func (e *Engine) pass() {
	now := e.clk.Now()
	e.checkJump(now)
	ch := e.takeChanges()
	sweep := now.UnixMilli() >= e.nextSweep
	var pushes []*push
	var evs []events.Event
	frozen := false
	newSig := ""
	txCtx, cancelTx := context.WithTimeout(context.Background(), time.Minute)
	defer cancelTx()
	err := e.st.Write(txCtx, func(tx *store.Tx) error {
		pushes, evs, newSig = nil, nil, ""
		var err error
		if frozen, err = tx.HubIsFrozen(); err != nil {
			return err
		}
		if e.frozen && !frozen {
			// Unfrozen (a move called off, machines.md 8.1): machines could not reach this hub
			// while it was frozen, so silence counts from now, as after a suspend (push.md 3.3).
			e.markAwake(now)
			e.log.Printf("alert engine: hub unfrozen, resuming")
		}
		w, err := e.newWork(tx, now)
		if err != nil {
			return err
		}
		if err := w.reconcileChanges(ch); err != nil {
			return err
		}
		// Switch notices from machines (push.md 3.9): created pending even while frozen, fired
		// once unfrozen (the 6-hour stale rule still applies).
		if err := w.notices(ch.Notices); err != nil {
			return err
		}
		if frozen {
			// push.md 3.6: while frozen nothing is created by the sweep and nothing is fired or
			// sent (deliveries stay pending), so an old and a new hub never both push.
			evs = w.evs
			return nil
		}
		// A rule turned off (or an override that now says do not notify) cancels its pending
		// alerts at once (push.md 3.6 step 1b); fired and snoozed ones are left alone.
		if sig := rulesSig(w.env.Rules); sig != e.rulesSig {
			if err := w.cancelRuleOff(); err != nil {
				return err
			}
			newSig = sig
		}
		if sweep {
			if err := w.sweepMachines(); err != nil {
				return err
			}
			if err := w.expireNotices(); err != nil {
				return err
			}
			_, seats, _, err := tx.AlertSubjects(w.nowMS)
			if err != nil {
				return err
			}
			if err := w.reconcile(nil, seats, nil); err != nil {
				return err
			}
		}
		fired, err := w.fireDue()
		if err != nil {
			return err
		}
		ps, err := w.fanOut(fired)
		if err != nil {
			return err
		}
		more, err := w.dueDeliveries()
		if err != nil {
			return err
		}
		pushes = append(ps, more...)
		if err := w.render(pushes); err != nil {
			return err
		}
		evs = w.evs
		return nil
	})
	if err != nil {
		e.log.Printf("alert pass: %v", err)
		e.requeue(ch)
		e.wakeIn = 5 * time.Second
		return
	}
	if frozen != e.frozen && frozen {
		e.log.Printf("alert engine: hub frozen, nothing is sent until it is unfrozen")
	}
	e.frozen = frozen
	if frozen {
		cancelTx()
		e.publish(evs)
		e.wakeIn = FrozenPoll
		return
	}
	if sweep {
		e.nextSweep = now.UnixMilli() + e.cfg.SweepEvery.Milliseconds()
	}
	if newSig != "" {
		e.rulesSig = newSig
	}
	cancelTx()
	e.publish(evs)
	if max := e.cfg.MaxPushesPerPass; len(pushes) > max {
		// The rest stay pending (next_at is their first retry slot) and are picked up as
		// due retries by the next passes.
		e.log.Printf("alert pass: %d pushes due, sending %d now", len(pushes), max)
		pushes = pushes[:max]
	}
	e.sendAll(pushes)
	e.finish(pushes)
}

// rulesSig changes whenever a rule is added, removed, switched or rescoped.
func rulesSig(rs []model.Rule) string {
	var b strings.Builder
	for _, r := range rs {
		sc, _ := json.Marshal(r.Scope)
		fmt.Fprintf(&b, "%s|%s|%t|%s;", r.ID, r.Trigger, r.Enabled, sc)
	}
	return b.String()
}

// cancelRuleOff cancels every pending (not yet fired) alert whose rule is now off: the
// rule that matches its subject today is disabled, or none matches (push.md 3.6 step 1b).
func (w *work) cancelRuleOff() error {
	pend, err := w.tx.AlertsPending()
	if err != nil {
		return err
	}
	for _, a := range pend {
		rule, ok := rules.Match(w.env.Rules, a.Trigger, rules.SubjectOf(a, a.Facts.Project))
		if ok && rule.Enabled {
			continue
		}
		if err := w.end(a, model.StatusCancelled, model.ReasonRuleOff); err != nil {
			return err
		}
	}
	return nil
}

// fireDue fires every alert that is due (push.md 3.6 step 3): when at least one alert is
// due now, the alerts due within batch_s after it fire with it (so they can share a push).
func (w *work) fireDue() ([]firing, error) {
	due, err := w.tx.AlertsDue(w.nowMS)
	if err != nil || len(due) == 0 {
		return nil, err
	}
	if b := int64(w.env.Settings.BatchS) * 1000; b > 0 {
		if due, err = w.tx.AlertsDue(w.nowMS + b); err != nil {
			return nil, err
		}
	}
	var out []firing
	for _, a := range due {
		f, ok, err := w.recheck(a)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// recheck re-reads the alert's condition and rule before firing it; if the episode is over
// or the rule is off it applies that outcome instead; if it is too late it is not pushed.
func (w *work) recheck(a model.Alert) (firing, bool, error) {
	project := a.Facts.Project
	switch {
	case rules.IsSessionTrigger(a.Trigger):
		id := deref(a.Session)
		r, found, err := w.tx.AlertSession(id)
		if err != nil {
			return firing{}, false, err
		}
		cur := sessionNow(id, r, found)
		if _, want := rules.SessionWant(cur); want != a.Episode {
			return firing{}, false, w.end(a, rules.EndStatus(a.Status), rules.EndReason(cur.State, found))
		}
		if w.env.Settings.SkipPeers && rules.PeersOnly(a.Trigger, cur.Detail, cur.Peers) {
			// The turn turned out to be sessions coordinating (a Stop that waits on a peer's
			// reply, folded during the delay): no push (push.md 3.5).
			return firing{}, false, w.end(a, rules.EndStatus(a.Status), model.ReasonPeers)
		}
		// Refresh the text inputs (a Remote Control link or question found during the delay).
		a.Facts = cur.Facts
		project = r.Project
	case a.Trigger == model.TriggerLimit80 || a.Trigger == model.TriggerLimit95:
		if int64(a.Facts.ResetsAt) <= w.nowMS {
			return firing{}, false, w.end(a, rules.EndStatus(a.Status), model.ReasonResetPassed)
		}
	case a.Trigger == model.TriggerMachineSilent:
		ms, err := w.tx.AlertMachines([]string{deref(a.Machine)}, w.nowMS)
		if err != nil {
			return firing{}, false, err
		}
		if len(ms) == 0 || rules.MachineEpisode(machineNow(ms[0])) != a.Episode {
			return firing{}, false, w.end(a, rules.EndStatus(a.Status), model.ReasonRecovered)
		}
	}
	rule, ok := rules.Match(w.env.Rules, a.Trigger, rules.SubjectOf(a, project))
	if !ok || !rule.Enabled {
		return firing{}, false, w.end(a, rules.EndStatus(a.Status), model.ReasonRuleOff)
	}
	if rules.Overdue(int64(a.DueAt), int64(w.env.Settings.MaxAgeS)*1000, w.nowMS) {
		if a.Status == model.StatusPending {
			reason := model.ReasonStale
			a.Status, a.Reason, a.DueAt, a.EndedAt = model.StatusSkipped, &reason, 0, model.Time(w.nowMS)
			if err := w.tx.AlertSave(a, w.nowMS); err != nil {
				return firing{}, false, err
			}
			w.event("skipped", a)
			w.e.log.Printf("alert skip %s trigger=%s %s reason=stale", a.ID, a.Trigger, subjectLog(a))
			return firing{}, false, nil
		}
		// A snooze re-push that is too late is dropped; the alert stays fired (open). The fire
		// count moves on so the snooze token of the earlier fire cannot be replayed.
		a.Status, a.DueAt = model.StatusFired, 0
		a.FireCount++
		if err := w.tx.AlertSave(a, w.nowMS); err != nil {
			return firing{}, false, err
		}
		w.event("fired", a)
		w.e.log.Printf("alert snooze dropped %s trigger=%s %s reason=stale", a.ID, a.Trigger, subjectLog(a))
		return firing{}, false, nil
	}
	if due, ok := w.holdAtFire(a); ok {
		// Catch-up hold (push.md 3.3): the machine is still sending its backlog, which may
		// hold a direct_push record for this episode. The stale guard above still applies.
		a.DueAt = model.Time(due)
		if err := w.tx.AlertSave(a, w.nowMS); err != nil {
			return firing{}, false, err
		}
		w.e.log.Printf("alert hold %s trigger=%s %s: machine catching up", a.ID, a.Trigger, subjectLog(a))
		return firing{}, false, nil
	}
	a.Status, a.DueAt = model.StatusFired, 0
	if a.FiredAt == 0 {
		a.FiredAt = model.Time(w.nowMS)
	}
	a.FireCount++
	if err := w.tx.AlertSave(a, w.nowMS); err != nil {
		return firing{}, false, err
	}
	w.event("fired", a)
	return firing{a: a, rule: rule}, true, nil
}

// quietFor is the target's effective quiet hours (push.md 5.2).
func (w *work) quietFor(t model.Target) model.QuietHours {
	q := w.env.Settings.QuietHours
	if t.Prefs != nil && t.Prefs.QuietHours != nil {
		q = *t.Prefs.QuietHours
		if q.TZ == "" {
			if t.Prefs.TZ != nil && *t.Prefs.TZ != "" {
				q.TZ = *t.Prefs.TZ
			} else {
				q.TZ = w.env.Settings.QuietHours.TZ
			}
		}
	}
	return q
}

func prefsOf(t model.Target) model.Prefs {
	if t.Prefs == nil {
		return model.Prefs{Tap: model.TapAuto}
	}
	return *t.Prefs
}

// floodHold reports whether one more push to target would break the flood cap, and when
// the cap frees (push.md 5.3). sent holds this pass's pushes per target.
func (w *work) floodHold(target string, sent map[string]int) (bool, int64, error) {
	f := w.env.Settings.Flood
	per := int64(f.PerS) * 1000
	times, err := w.tx.DeliveryPushes(target, w.nowMS-per)
	if err != nil {
		return false, 0, err
	}
	for range sent[target] {
		times = append(times, w.nowMS)
	}
	if len(times) < f.Max {
		return false, 0, nil
	}
	return true, times[len(times)-f.Max] + per, nil
}

// fanOut makes the deliveries of the alerts fired in this pass (push.md 5.1-5.3).
func (w *work) fanOut(fired []firing) ([]*push, error) {
	if len(fired) == 0 {
		return nil, nil
	}
	targets, err := w.tx.AlertTargets(w.nowMS)
	if err != nil {
		return nil, err
	}
	sentTo := map[string]int{}    // alert id -> targets it goes to now
	perTarget := map[string]int{} // pushes made this pass, per target
	var out []*push
	for _, t := range targets {
		prefs := prefsOf(t)
		q := w.quietFor(t)
		var session []firing
		var singles [][]firing
		for _, f := range fired {
			d := store.DeliveryRow{AlertID: f.a.ID, TargetID: t.ID, TargetKind: t.Kind, Fire: f.a.FireCount,
				CreatedAt: w.nowMS, UpdatedAt: w.nowMS}
			if prefs.Muted(f.a.Trigger) {
				d.Status, d.Reason = dSkipped, rMuted
				if _, err := w.tx.DeliveryInsert(&d); err != nil {
					return nil, err
				}
				continue
			}
			if hold, end := rules.Holds(q, f.rule.BypassQuiet, w.now); hold {
				d.Status, d.Reason, d.NextAt = dHeld, rQuiet, end.UnixMilli()
				if _, err := w.tx.DeliveryInsert(&d); err != nil {
					return nil, err
				}
				continue
			}
			if rules.IsSessionTrigger(f.a.Trigger) {
				session = append(session, f)
			} else {
				singles = append(singles, []firing{f})
			}
		}
		groups := singles
		if len(session) > 0 {
			groups = append([][]firing{session}, singles...)
		}
		for _, g := range groups {
			hold, freeAt, err := w.floodHold(t.ID, perTarget)
			if err != nil {
				return nil, err
			}
			p := &push{target: t}
			batch := store.NewID("pb_", w.now)
			for _, f := range g {
				d := store.DeliveryRow{AlertID: f.a.ID, TargetID: t.ID, TargetKind: t.Kind, Fire: f.a.FireCount,
					CreatedAt: w.nowMS, UpdatedAt: w.nowMS}
				if hold {
					d.Status, d.Reason, d.NextAt = dHeld, rFlood, freeAt
				} else {
					// In flight: next_at is the first retry slot, so a crash mid-send retries.
					d.Status, d.Batch, d.NextAt = dPending, batch, w.nowMS+retryBackoff[0].Milliseconds()
				}
				ok, err := w.tx.DeliveryInsert(&d)
				if err != nil {
					return nil, err
				}
				if ok && !hold {
					p.ds = append(p.ds, d)
					p.alerts = append(p.alerts, f.a)
					sentTo[f.a.ID]++
				}
			}
			if len(p.ds) > 0 {
				perTarget[t.ID]++
				out = append(out, p)
			}
		}
	}
	for _, f := range fired {
		w.e.log.Printf("alert fire %s trigger=%s %s fire=%d targets=%d", f.a.ID, f.a.Trigger, subjectLog(f.a), f.a.FireCount, sentTo[f.a.ID])
	}
	return out, nil
}

// dueDeliveries handles retries and held deliveries whose time has come (push.md 3.6 step
// 5, 5.2, 5.3, 5.6).
func (w *work) dueDeliveries() ([]*push, error) {
	ds, err := w.tx.DeliveriesDue(w.nowMS)
	if err != nil || len(ds) == 0 {
		return nil, err
	}
	var order []string
	byTarget := map[string][]store.DeliveryRow{}
	for _, d := range ds {
		if _, ok := byTarget[d.TargetID]; !ok {
			order = append(order, d.TargetID)
		}
		byTarget[d.TargetID] = append(byTarget[d.TargetID], d)
	}
	skip := func(d store.DeliveryRow, reason string) error {
		d.Status, d.Reason, d.NextAt, d.UpdatedAt = dSkipped, reason, 0, w.nowMS
		return w.tx.DeliverySave(d)
	}
	maxAge := int64(w.env.Settings.MaxAgeS) * 1000
	var out []*push
	for _, tid := range order {
		list := byTarget[tid]
		t, ok, err := w.tx.AlertTarget(tid, w.nowMS)
		if err != nil {
			return nil, err
		}
		if !ok {
			for _, d := range list {
				if err := skip(d, rChannelOff); err != nil {
					return nil, err
				}
			}
			continue
		}
		q := w.quietFor(t)
		held := &push{target: t}
		var retryOrder []string
		retries := map[string]*push{}
		for _, d := range list {
			a, err := w.tx.AlertGet(d.AlertID)
			if errors.Is(err, store.ErrNotFound) {
				if err := skip(d, rResolved); err != nil {
					return nil, err
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			if (a.Status != model.StatusFired && a.Status != model.StatusSnoozed) || d.Fire != a.FireCount {
				if err := skip(d, rResolved); err != nil {
					return nil, err
				}
				continue
			}
			if d.Status == dHeld {
				if d.Reason == rQuiet {
					rule, _ := rules.Match(w.env.Rules, a.Trigger, rules.SubjectOf(a, a.Facts.Project))
					if hold, end := rules.Holds(q, rule.BypassQuiet, w.now); hold {
						d.NextAt, d.UpdatedAt = end.UnixMilli(), w.nowMS
						if err := w.tx.DeliverySave(d); err != nil {
							return nil, err
						}
						continue
					}
				}
				held.ds, held.alerts = append(held.ds, d), append(held.alerts, a)
				continue
			}
			if rules.Overdue(d.NextAt, maxAge, w.nowMS) {
				if err := skip(d, rStale); err != nil {
					return nil, err
				}
				continue
			}
			p, ok := retries[d.Batch]
			if !ok {
				p = &push{target: t}
				retries[d.Batch] = p
				retryOrder = append(retryOrder, d.Batch)
			}
			p.ds, p.alerts = append(p.ds, d), append(p.alerts, a)
		}
		if len(held.ds) > 0 {
			// Released held deliveries go out as one push (a summary, or the alert itself).
			batch := store.NewID("pb_", w.now)
			for i := range held.ds {
				held.ds[i].Status, held.ds[i].Batch, held.ds[i].UpdatedAt = dPending, batch, w.nowMS
				held.ds[i].NextAt = w.nowMS + retryBackoff[0].Milliseconds()
				if err := w.tx.DeliverySave(held.ds[i]); err != nil {
					return nil, err
				}
			}
			out = append(out, held)
		}
		for _, b := range retryOrder {
			out = append(out, retries[b])
		}
	}
	return out, nil
}

// private reports whether pushes to t are private (push.md 5.5, 7.1).
func (w *work) private(t model.Target) bool {
	p := prefsOf(t)
	if p.Private != nil {
		return *p.Private
	}
	if t.Kind == model.KindNtfy {
		if u, err := url.Parse(t.Endpoint); err == nil && strings.EqualFold(u.Hostname(), "ntfy.sh") {
			return true
		}
	}
	return w.env.Settings.Private
}

// render builds every push's message (push.md 5.5, 6.4, 6.5, 7.2, 8).
func (w *work) render(ps []*push) error {
	if len(ps) == 0 {
		return nil
	}
	key, err := w.tx.ActionKey()
	if err != nil {
		return err
	}
	needs, err := w.tx.AlertNeedsYou()
	if err != nil {
		return err
	}
	rooms := map[string][]rules.RoomCandidate{}
	roomOf := func(a model.Alert) (*rules.Room, error) {
		key := ""
		switch {
		case a.Trigger == model.TriggerLimit80 || a.Trigger == model.TriggerLimit95:
			key = deref(a.WindowKey)
		case a.Trigger == model.TriggerFailed && a.Facts.Detail == "rate_limit":
			key = "five_hour"
		}
		if key == "" {
			return nil, nil
		}
		cands, ok := rooms[key]
		if !ok {
			rows, err := w.tx.AlertRoom(key, w.nowMS)
			if err != nil {
				return nil, err
			}
			for _, r := range rows {
				cands = append(cands, rules.RoomCandidate{Seat: r.Seat, Label: seatName(r.Name, r.Label, r.Email), Hidden: r.Hidden,
					HasWindow: r.HasWindow, UsedPct: r.Used, ResetsMS: r.ResetsAt, HasReading: r.HasReading})
			}
			rooms[key] = cands
		}
		return rules.WhoHasRoom(deref(a.Seat), cands, w.nowMS), nil
	}
	for _, p := range ps {
		prefs := prefsOf(p.target)
		loc := w.loc
		if prefs.TZ != nil && *prefs.TZ != "" {
			loc = rules.Location(*prefs.TZ)
		}
		opts := rules.TextOptions{Private: w.private(p.target), Loc: loc, Now: w.now}
		pl := model.Payload{V: model.PayloadVersion, Tap: prefs.TapMode(), NeedsYou: needs, Private: opts.Private, TS: model.Time(w.nowMS)}
		for _, a := range p.alerts[:min(len(p.alerts), model.MaxPayloadAlerts)] {
			pl.Alerts = append(pl.Alerts, a.ID)
		}
		pl.Alert = p.alerts[0].ID
		var trig model.Trigger
		var resets int64
		summary := len(p.alerts) > 1
		var txt rules.Text
		if summary {
			txt = rules.RenderSummary(p.alerts, opts)
			pl.Type = model.TypeSummary
		} else {
			a := p.alerts[0]
			if a.Trigger == model.TriggerFailed && a.Facts.ResetsAt == 0 && a.Seat != nil {
				r, err := w.tx.AlertCurrentReset(*a.Seat, "five_hour", w.nowMS)
				if err != nil {
					return err
				}
				a.Facts.ResetsAt = model.Time(r)
			}
			room, err := roomOf(a)
			if err != nil {
				return err
			}
			opts.Room, opts.Repush = room, a.FireCount > 1
			txt = rules.Render(a, opts)
			trig, resets = a.Trigger, int64(a.Facts.ResetsAt)
			pl.Type, pl.Trigger, pl.Session, pl.RemoteURL = model.TypeAlert, a.Trigger, deref(a.Session), a.Facts.RemoteURL
			exp := w.now.Add(action.Lifetime)
			fire := uint32(a.FireCount)
			pl.Actions = &model.Actions{
				Snooze: action.Encode(key, action.Token{Alert: a.ID, Action: action.Snooze, Fire: fire, Target: p.target.ID, Expires: exp}),
				Seen:   action.Encode(key, action.Token{Alert: a.ID, Action: action.Seen, Fire: fire, Target: p.target.ID, Expires: exp}),
			}
		}
		pl.Title, pl.Body, pl.URL, pl.Tag = txt.Title, txt.Body, txt.URL, txt.Tag
		p.msg = model.Message{Payload: pl, TTL: rules.TTL(trig, summary, resets, w.nowMS), Urgency: rules.Urgency(trig, summary),
			Priority: rules.Priority(trig, summary), HubURL: w.hub}
		for _, d := range p.ds {
			p.msg.Deliveries = append(p.msg.Deliveries, d.ID)
		}
	}
	return nil
}

// sendAll sends the pushes on a pool of Workers goroutines, each with SendTimeout, and
// returns when they are all done or PassBudget has run out, whichever is first. No
// transaction is open while sending. A send that does not return in time counts as a
// timed-out try (its late result is dropped); a push never started is left pending.
func (e *Engine) sendAll(ps []*push) {
	if len(ps) == 0 {
		return
	}
	type result struct {
		p *push
		r model.Result
	}
	results := make(chan result, len(ps))
	budget := time.NewTimer(e.cfg.PassBudget)
	defer budget.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.PassBudget)
	defer cancel()
	sem := make(chan struct{}, e.cfg.Workers)
	launched := 0
dispatch:
	for _, p := range ps {
		select {
		case sem <- struct{}{}:
		case <-budget.C:
			break dispatch
		}
		launched++
		p.started = true
		go func(p *push) {
			defer func() { <-sem }()
			s := e.senders[p.target.Kind]
			if s == nil {
				results <- result{p, model.Result{Outcome: model.OutcomeFailed, Err: "no sender for " + p.target.Kind}}
				return
			}
			sctx, scancel := context.WithTimeout(ctx, e.cfg.SendTimeout)
			defer scancel()
			results <- result{p, s.Send(sctx, p.target, p.msg)}
		}(p)
	}
	// results is buffered for every push, so a send that outlives the budget never blocks.
	grace := time.NewTimer(e.cfg.PassBudget + 500*time.Millisecond)
	defer grace.Stop()
	for got := 0; got < launched; got++ {
		select {
		case r := <-results:
			r.p.res, r.p.sent = r.r, true
		case <-grace.C:
			e.log.Printf("alert pass: %d of %d sends did not return in time", launched-got, len(ps))
			for _, p := range ps {
				if p.started && !p.sent { // a late result is dropped; the try counts as a timeout
					p.res, p.sent = model.Result{Outcome: model.OutcomeRetry, Err: "timeout"}, true
				}
			}
			return
		}
	}
	if launched < len(ps) {
		e.log.Printf("alert pass: send budget used up, %d pushes left pending", len(ps)-launched)
	}
}

// finish writes the send results back in one transaction (push.md 5.6) and works out when
// the scheduler wakes next.
//
// It runs on its own fresh context, never the one the transaction before the sends used: a
// slow send must not make the write fail, or successful sends would stay pending and be
// sent again. It is retried a few times for the same reason.
func (e *Engine) finish(ps []*push) {
	var err error
	for try := 0; try < finishTries; try++ {
		if try > 0 {
			time.Sleep(time.Duration(try) * 200 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
		err = e.finishOnce(ctx, ps)
		cancel()
		if err == nil {
			return
		}
		e.log.Printf("alert results (try %d): %v", try+1, err)
	}
	e.wakeIn = 5 * time.Second
}

const (
	finishTries   = 3
	finishTimeout = 30 * time.Second
)

func (e *Engine) finishOnce(ctx context.Context, ps []*push) error {
	return e.st.Write(ctx, func(tx *store.Tx) error {
		now := e.clk.Now()
		nowMS := now.UnixMilli()
		for _, p := range ps {
			if !p.sent {
				continue
			}
			if err := tx.AlertTargetResult(p.target.ID, p.res, nowMS); err != nil {
				return err
			}
			if p.res.Outcome != model.OutcomeSent {
				e.log.Printf("push fail %s %d %s", p.target.ID, p.res.HTTPStatus, p.res.Outcome)
			}
			ttl := p.msg.TTL
			for _, d := range p.ds {
				d.Tries++
				d.HTTPStatus, d.UpdatedAt, d.NextAt, d.Reason = p.res.HTTPStatus, nowMS, 0, ""
				switch p.res.Outcome {
				case model.OutcomeSent:
					d.Status, d.LastError = dSent, ""
				case model.OutcomeGone:
					d.Status, d.LastError = dGone, p.res.Err
				case model.OutcomeRetry:
					d.LastError = p.res.Err
					next := time.Duration(0)
					if d.Tries <= len(retryBackoff) {
						next = retryBackoff[d.Tries-1]
						if p.res.RetryAfter > 0 {
							next = min(p.res.RetryAfter, MaxRetryAfter)
						}
					}
					switch {
					case next == 0:
						d.Status = dFailed
					case ttl > 0 && nowMS+next.Milliseconds() > d.CreatedAt+ttl.Milliseconds():
						d.Status, d.LastError = dFailed, strings.TrimSpace(d.LastError+" (ttl passed)")
					default:
						d.Status, d.NextAt = dPending, nowMS+next.Milliseconds()
					}
				default:
					d.Status, d.LastError = dFailed, p.res.Err
				}
				if len(d.LastError) > 200 {
					d.LastError = d.LastError[:200]
				}
				if err := tx.DeliverySave(d); err != nil {
					return err
				}
			}
		}
		// Next wake-up: the earliest due alert, delivery or sweep.
		next := e.nextSweep
		if v, ok, err := tx.AlertNextDue(); err != nil {
			return err
		} else if ok && v < next {
			next = v
		}
		if v, ok, err := tx.DeliveryNextAt(); err != nil {
			return err
		} else if ok && v < next {
			next = v
		}
		e.wakeIn = time.Duration(next-nowMS) * time.Millisecond
		if e.wakeIn <= 0 {
			e.wakeIn = time.Second // something is overdue and was not handled: do not spin
		}
		return nil
	})
}
