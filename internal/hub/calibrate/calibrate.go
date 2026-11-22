// Package calibrate is the hub's calibration job (setup.md 8.5, Calibration): every 15
// minutes it folds each seat's new samples, token rows and leases into its calibration state
// (internal/calib) and stores the factors that GET /v1/usage returns. Each run reads only the
// rows after the seat's cursors, by index, so it stays cheap on a small hub.
//
// Contract: docs/spec/setup.md 8.5 (Calibration), docs/spec/schema.sql (seat_calibration).
//
// Depends on: calib, hub/store.
package calibrate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mraj602/watchyard/internal/calib"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Job timing (setup.md 8.5, On the hub).
const (
	Every = 15 * time.Minute
	First = time.Minute
	// Settle: only data at least this old is folded (token rows arrive after the readings
	// they explain).
	Settle = 30 * time.Minute
)

// seatRun is one seat during a run.
type seatRun struct {
	row         store.CalibrationRow
	state       *calib.State
	cursor      int64
	leaseCursor int64
}

// Run brings every seat's calibration up to date at now.
func Run(ctx context.Context, st *store.Store, now time.Time) error {
	rows, err := st.CalibrationRows(ctx)
	if err != nil {
		return err
	}
	upto := now.Add(-Settle).UnixMilli()
	laneUpto := now.Add(-Settle - calib.LaneSpan).UnixMilli()
	runs := make([]*seatRun, 0, len(rows))
	for _, row := range rows {
		r, err := foldSeat(ctx, st, row, now, upto, laneUpto)
		if err != nil {
			return fmt.Errorf("seat %s: %w", row.Seat, err)
		}
		runs = append(runs, r)
	}
	lanes := make([][]calib.Lane, 0, len(runs))
	for _, r := range runs {
		lanes = append(lanes, r.state.Lanes)
	}
	pool := calib.PoolLanes(now, lanes...)
	for _, r := range runs {
		f := r.state.Factors(now, pool)
		state, err := json.Marshal(r.state)
		if err != nil {
			return err
		}
		factors, err := json.Marshal(f)
		if err != nil {
			return err
		}
		if err := st.Write(ctx, func(tx *store.Tx) error {
			return tx.SaveCalibration(r.row.Seat, r.row.Account, r.row.Org, r.cursor, r.leaseCursor, state, factors)
		}); err != nil {
			return fmt.Errorf("seat %s: %w", r.row.Seat, err)
		}
	}
	return nil
}

func foldSeat(ctx context.Context, st *store.Store, row store.CalibrationRow, now time.Time, upto, laneUpto int64) (*seatRun, error) {
	r := &seatRun{row: row, cursor: row.Cursor, leaseCursor: row.LeaseCursor}
	s := calib.NewState()
	if len(row.State) > 0 {
		if err := json.Unmarshal(row.State, s); err != nil || s.V != 1 {
			s, r.cursor, r.leaseCursor = calib.NewState(), 0, 0 // unreadable: start again
		}
	}
	if row.CalAccount != "" && (row.CalAccount != row.Account || row.CalOrg != row.Org) {
		s.Restart(now.UnixMilli(), calib.ReasonSeatChange)
		s.Tracker = calib.Tracker{} // another login's readings are no anchor
	}
	r.state = s
	if first := now.Add(-calib.PairWindow).UnixMilli(); r.cursor < first {
		r.cursor = first
	}
	if first := now.Add(-calib.LaneWindow).UnixMilli(); r.leaseCursor < first {
		r.leaseCursor = first
	}

	if upto > r.cursor {
		samples, err := st.CalibSamples(ctx, row.Seat, r.cursor, upto)
		if err != nil {
			return nil, err
		}
		// Tokens since the last used five_hour reading (no older than a window before the
		// cursor: a pair never spans more than one 5-hour window).
		from := r.cursor
		if l := s.Tracker.Five.Last; l != nil && l.At < from {
			from = l.At
		}
		if floor := r.cursor - (calib.LaneSpan + 2*calib.ResetTolerance).Milliseconds(); from < floor {
			from = floor
		}
		uses, err := st.CalibUnits(ctx, row.Seat, from, upto)
		if err != nil {
			return nil, err
		}
		us := make([]calib.Use, len(uses))
		for i, u := range uses {
			us[i] = calib.Use{At: time.UnixMilli(u.TS), Units: u.Units}
		}
		s.AddReadings(Readings(samples), calib.NewMeter(us))
		r.cursor = upto
	}
	if laneUpto > r.leaseCursor {
		ls, err := st.CalibLanes(ctx, row.Seat, r.leaseCursor, laneUpto, calib.LaneSpan)
		if err != nil {
			return nil, err
		}
		for _, l := range ls {
			s.AddLane(time.UnixMilli(l.TS), l.Units)
		}
		r.leaseCursor = laneUpto
	}
	s.Prune(now)
	return r, nil
}

// Readings joins a seat's five_hour and seven_day sample rows (oldest first) into readings:
// the rows of one time, source and session are one reading.
func Readings(rows []store.CalibSample) []calib.Reading {
	type key struct {
		ts              int64
		source, session string
	}
	idx := map[key]int{}
	var out []calib.Reading
	for _, x := range rows {
		k := key{x.TS, x.Source, x.Session}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, calib.Reading{At: time.UnixMilli(x.TS), Source: x.Source})
		}
		var reset time.Time
		if x.ResetsAt != 0 {
			reset = time.UnixMilli(x.ResetsAt)
		}
		switch x.Key {
		case "five_hour":
			out[i].Five, out[i].FiveReset = x.UsedPct, reset
		case "seven_day":
			out[i].Seven, out[i].SevenReset = x.UsedPct, reset
		}
	}
	calib.SortReadings(out)
	return out
}

// Loop runs the job First after the start and then Every, until ctx ends. A failed run is
// logged and retried at the next tick.
func Loop(ctx context.Context, st *store.Store, now func() time.Time, logf func(string, ...any)) {
	t := time.NewTimer(First)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		if err := Run(c, st, now()); err != nil && ctx.Err() == nil {
			logf("calibration: %v", err)
		}
		cancel()
		t.Reset(Every)
	}
}
