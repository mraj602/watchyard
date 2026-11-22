package engine

import (
	"context"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// noticeTriggers are the usage notices (push.md 3.9).
var noticeTriggers = []model.Trigger{model.TriggerLimitChange, model.TriggerUsageReset, model.TriggerSeatChange,
	model.TriggerWeeklyDrop}

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
		eps := make([]string, 0, len(ns))
		for _, n := range ns {
			eps = append(eps, n.Episode)
		}
		have, err := tx.AlertsByEpisode(eps)
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
	})
	if err != nil {
		return err
	}
	e.publish(w.evs)
	e.Poke()
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
