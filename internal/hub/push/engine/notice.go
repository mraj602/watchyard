package engine

import (
	"context"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// noticeTriggers are the usage notices (push.md 3.9).
var noticeTriggers = []model.Trigger{model.TriggerLimitChange, model.TriggerUsageReset, model.TriggerSeatChange,
	model.TriggerWeeklyDrop, model.TriggerSessionMoved, model.TriggerSwitchDigest}

// Notices turns the calibration job's usage notices into alerts (push.md 3.9): each episode
// once, pending and due now (the scheduler fires it in its next pass), or skipped when its
// rule is off or it is history.
func (e *Engine) Notices(ctx context.Context, ns []model.Notice) error {
	if len(ns) == 0 {
		return nil
	}
	var w *work
	err := e.st.Write(ctx, func(tx *store.Tx) error {
		var err error
		if w, err = e.newWork(tx, e.clk.Now()); err != nil {
			return err
		}
		return w.notices(ns)
	})
	if err != nil {
		return err
	}
	e.publish(w.evs)
	e.Poke()
	return nil
}

// notices creates the alerts of the notices (each episode once).
func (w *work) notices(ns []model.Notice) error {
	if len(ns) == 0 {
		return nil
	}
	eps := make([]string, 0, len(ns))
	var unnamed []string
	for _, n := range ns {
		eps = append(eps, n.Episode)
		if n.Machine != "" && n.Facts.Machine == "" {
			unnamed = append(unnamed, n.Machine)
		}
	}
	if len(unnamed) > 0 {
		// Switch notices name their machine as the board does (push.md 3.9).
		ms, err := w.tx.AlertMachines(unnamed, w.nowMS)
		if err != nil {
			return err
		}
		names := map[string]string{}
		for _, m := range ms {
			names[m.ID] = machineNow(m).Facts.Machine
		}
		for i := range ns {
			if ns[i].Machine != "" && ns[i].Facts.Machine == "" {
				if ns[i].Facts.Machine = names[ns[i].Machine]; ns[i].Facts.Machine == "" {
					ns[i].Facts.Machine = ns[i].Machine
				}
			}
		}
	}
	have, err := w.tx.AlertsByEpisode(eps)
	if err != nil {
		return err
	}
	for i := range ns {
		ops := rules.Decide(rules.Current{Notice: &ns[i]}, have, w.env)
		if err := w.apply(ops); err != nil {
			return err
		}
		for _, op := range ops {
			if op.Kind == rules.OpCreate {
				have = append(have, op.Alert) // a notice handed twice in one call
			}
		}
	}
	return nil
}

// expireNotices closes the usage notices 24 hours after they fired, reason expired (push.md
// 3.9, part of the silence sweep).
func (w *work) expireNotices() error {
	as, err := w.tx.AlertsFiredOf(noticeTriggers, w.nowMS-rules.NoticeLifeMS)
	if err != nil {
		return err
	}
	for _, a := range as {
		if rules.NoticeExpired(a, w.nowMS) {
			if err := w.end(a, model.StatusClosed, model.ReasonExpired); err != nil {
				return err
			}
		}
	}
	return nil
}
