package engine

import (
	"fmt"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/projection"
)

// DrainAimAt is the weekly level an owner's drain ends at (setup.md 8.5, Owner drain and Drain
// aim): the drain ramp's aim at the drain's until, h hours before the weekly reset (96 far
// from it, up to 99, 100 less the drain margin, at it; h < 0: the reset is unknown, 96).
func DrainAimAt(h float64) float64 {
	if h >= 0 && h < 0.0001 {
		h = 0.0001
	}
	return min(projection.DrainAim7(h), 99)
}

// drains turns the owners' drains into notices (push.md 3.9; part of the sweep): a seat
// with a drain_until in the future gets drain_started once per until; one whose until has
// passed, or whose weekly level reached DrainAimAt, has its drain cleared and gets
// drain_ended.
func (w *work) drains() error {
	ds, err := w.tx.DrainingSeats()
	if err != nil || len(ds) == 0 {
		return err
	}
	var ns []model.Notice
	for _, d := range ds {
		r, found, err := w.tx.AlertSeat(d.ID, w.nowMS)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		name := seatName(r.Name, r.Label, r.Email)
		weekly, hasWeekly := 0.0, false
		hLeft := -1.0 // hours from the until to the weekly reset
		for _, x := range r.Windows {
			if x.Key == "seven_day" && x.Peak != nil {
				weekly, hasWeekly = *x.Peak, true
				if x.ResetsAt > 0 {
					hLeft = max(0, float64(x.ResetsAt-d.Until)/3.6e6)
				}
			}
		}
		why := ""
		switch {
		case w.nowMS >= d.Until:
			why = rules.DrainEndTime
		case hasWeekly && weekly >= DrainAimAt(hLeft):
			why = rules.DrainEndAim
		}
		ep := fmt.Sprintf("d:%s:%d:", d.ID, d.Until)
		if why == "" {
			ns = append(ns, model.Notice{Trigger: model.TriggerDrainStarted, Episode: ep + "on", Seat: d.ID, SinceMS: w.nowMS,
				Facts: model.Facts{Seat: name, ResetsAt: model.Time(d.Until)}})
			continue
		}
		ended, err := w.tx.EndDrain(d.ID, d.Until)
		if err != nil {
			return err
		}
		if ended {
			ns = append(ns, model.Notice{Trigger: model.TriggerDrainEnded, Episode: ep + "end", Seat: d.ID, SinceMS: w.nowMS,
				Facts: model.Facts{Seat: name, Pct: weekly, Detail: why, ResetsAt: model.Time(d.Until)}})
		}
	}
	return w.notices(ns)
}
