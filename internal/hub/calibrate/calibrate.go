// Package calibrate is the hub's calibration job (setup.md 8.5, Calibration): every 15
// minutes it folds each seat's new samples, token rows and leases into its calibration state
// (internal/calib) and stores the factors that GET /v1/usage returns. Before that it watches
// each seat's newest readings for the usage notices (push.md 3.9), and after it folds the usage
// relation the board shows (setup.md 8.5, Usage relation) and hands the notices it found to
// the alert engine. Each run reads only the rows after the seat's cursors, by index, so it
// stays cheap on a small hub.
//
// Contract: docs/spec/setup.md 8.5 (Calibration, Usage relation), docs/spec/push.md 3.9,
// docs/spec/schema.sql (seat_calibration, seat_watch).
//
// Depends on: calib, hub/store, hub/push/model and hub/push/rules (episodes, the hub's zone),
// scan (prices and model labels).
package calibrate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mraj602/watchyard/internal/calib"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/scan"
)

// Job timing (setup.md 8.5, On the hub).
const (
	Every = 15 * time.Minute
	First = time.Minute
	// Settle: only data at least this old is folded (token rows arrive after the readings
	// they explain).
	Settle = 30 * time.Minute
	// WatchLag: the watch reads samples up to this long before now (a batch being written).
	WatchLag = 2 * time.Minute
	// SeatChangeDays: login periods of this many days are read for seat_change.
	SeatChangeDays = 8 * 24 * time.Hour
)

// Notifier hands usage notices to the alert engine (engine.Notices). Handing one twice is
// harmless: its episode makes it one alert.
type Notifier func(ctx context.Context, ns []model.Notice) error

// seatRun is one seat during a run.
type seatRun struct {
	row         store.CalibrationRow
	state       *calib.State
	cursor      int64
	leaseCursor int64
	watch       *calib.Watch
	watchCursor int64
	rel         *calib.Relation
	events      []calib.Event
}

// Run brings every seat's calibration, watch and relation up to date at now, and hands the
// notices it found to notify (nil: none are handed). The notices go first and the state is
// saved after, so a failed hand-over is retried by the next run.
func Run(ctx context.Context, st *store.Store, now time.Time, notify Notifier) error {
	rows, err := st.CalibrationRows(ctx)
	if err != nil {
		return err
	}
	watches, err := st.WatchRows(ctx)
	if err != nil {
		return err
	}
	tz, _ := rules.HubZone(st.AlertSettingString(ctx, store.AlertKeyHubTZ))
	loc := rules.Location(tz)
	upto := now.Add(-Settle).UnixMilli()
	laneUpto := now.Add(-Settle - calib.LaneSpan).UnixMilli()
	runs := make([]*seatRun, 0, len(rows))
	for _, row := range rows {
		r, err := foldSeat(ctx, st, row, watches[row.Seat], now, upto, laneUpto, loc)
		if err != nil {
			return fmt.Errorf("seat %s: %w", row.Seat, err)
		}
		runs = append(runs, r)
	}
	if notify != nil {
		ns, err := notices(ctx, st, runs, now, loc)
		if err != nil {
			return err
		}
		if len(ns) > 0 {
			if err := notify(ctx, ns); err != nil {
				return fmt.Errorf("notices: %w", err)
			}
		}
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
		watch, err := json.Marshal(r.watch)
		if err != nil {
			return err
		}
		rel, err := json.Marshal(r.rel)
		if err != nil {
			return err
		}
		if err := st.Write(ctx, func(tx *store.Tx) error {
			if err := tx.SaveCalibration(r.row.Seat, r.row.Account, r.row.Org, r.cursor, r.leaseCursor, state, factors); err != nil {
				return err
			}
			return tx.SaveWatch(r.row.Seat, r.watchCursor, watch, rel)
		}); err != nil {
			return fmt.Errorf("seat %s: %w", r.row.Seat, err)
		}
	}
	return nil
}

// loadWatch reads a seat's watch and relation (new ones when there is no row or it is
// unreadable).
func loadWatch(w store.WatchRow) (*calib.Watch, *calib.Relation, int64) {
	watch, rel, cursor := calib.NewWatch(), calib.NewRelation(), w.Cursor
	if len(w.State) > 0 && (json.Unmarshal(w.State, watch) != nil || watch.V != 1) {
		watch, cursor = calib.NewWatch(), 0
	}
	if len(w.Relation) > 0 && (json.Unmarshal(w.Relation, rel) != nil || rel.V != 1) {
		rel = calib.NewRelation()
	}
	if watch.Masks == nil {
		watch.Masks = []calib.Mask{}
	}
	if rel.Days == nil {
		rel.Days = []calib.RelDay{}
	}
	return watch, rel, cursor
}

func foldSeat(ctx context.Context, st *store.Store, row store.CalibrationRow, wrow store.WatchRow, now time.Time,
	upto, laneUpto int64, loc *time.Location) (*seatRun, error) {
	r := &seatRun{row: row, cursor: row.Cursor, leaseCursor: row.LeaseCursor}
	r.watch, r.rel, r.watchCursor = loadWatch(wrow)
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
	if first := now.Add(-calib.PairWindow).UnixMilli(); r.watchCursor < first {
		r.watchCursor = first
	}

	// The watch (push.md 3.9): every reading up to now, ahead of the fold.
	if wupto := now.Add(-WatchLag).UnixMilli(); wupto > r.watchCursor {
		samples, err := st.CalibSamples(ctx, row.Seat, r.watchCursor, wupto)
		if err != nil {
			return nil, err
		}
		for _, rd := range Readings(samples) {
			r.events = append(r.events, r.watch.Add(rd)...)
		}
		r.watchCursor = wupto
	}
	r.events = append(r.events, r.watch.Tick(now)...)
	r.watch.Prune(now)
	if hold := r.watch.Hold(); hold != 0 && hold-1 < upto {
		upto = hold - 1 // a drop to about 0 waits for its decision: fold only readings before it
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
		rows, err := st.CalibUnits(ctx, row.Seat, from, upto)
		if err != nil {
			return nil, err
		}
		us := make([]calib.Use, len(rows))
		mus := make([]calib.ModelUse, len(rows))
		for i, u := range rows {
			us[i] = calib.Use{At: time.UnixMilli(u.TS), Units: u.Units}
			mus[i] = modelUse(u)
		}
		pairs := s.AddReadingsMasked(Readings(samples), calib.NewMeter(us), r.watch.Masks)
		// The usage relation (setup.md 8.5): the token rows not folded before, and the pairs.
		for i, u := range rows {
			if u.TS > r.rel.UsesUpto {
				r.rel.AddUse(mus[i], loc)
			}
		}
		if upto > r.rel.UsesUpto {
			r.rel.UsesUpto = upto
		}
		mm := calib.NewModelMeter(mus)
		for _, p := range pairs {
			r.rel.AddPair(p, mm, loc)
		}
		r.cursor = upto
	}
	r.rel.Prune(now, loc)
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

// modelUse is a token row as the relation sees it: its model's label, units and list price.
func modelUse(u store.CalibToken) calib.ModelUse {
	usd, ok := scan.CostOf(u.Model, scan.Tokens{Input: u.Input, Output: u.Output, Cache5m: u.Cache5m, Cache1h: u.Cache1h,
		CacheRead: u.CacheRead})
	label := ""
	if u.Model != "" {
		label = scan.ModelLabel(u.Model)
	}
	return calib.ModelUse{At: time.UnixMilli(u.TS), Model: label, Units: u.Units, USD: usd, Priced: ok}
}

// notices are the run's usage notices (push.md 3.9): the seats' decided drops, the folders
// that changed seat in the last 8 days and the models whose limits moved.
func notices(ctx context.Context, st *store.Store, runs []*seatRun, now time.Time, loc *time.Location) ([]model.Notice, error) {
	names, err := st.BoardSeatNames(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.Notice
	views := make([]calib.RelationView, 0, len(runs))
	for _, r := range runs {
		seat := r.row.Seat
		for _, e := range r.events {
			n := model.Notice{Seat: seat, SinceMS: e.T, Facts: model.Facts{Seat: names[seat], ResetsAt: model.Time(e.Window)}}
			switch e.Kind {
			case calib.EventUsageReset:
				n.Trigger, n.Episode = model.TriggerUsageReset, rules.UsageResetEpisode(seat, e.T)
				n.Facts.Sessions = e.Sources
			case calib.EventWeeklyDrop:
				n.Trigger, n.Episode = model.TriggerWeeklyDrop, rules.WeeklyDropEpisode(seat, e.Window, e.T)
				n.Facts.Peak, n.Facts.Pct = e.Old, e.New
			default:
				continue
			}
			out = append(out, n)
		}
		views = append(views, r.rel.View(now, loc))
	}
	cs, err := st.SeatChanges(ctx, now.Add(-SeatChangeDays).UnixMilli())
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		out = append(out, model.Notice{Trigger: model.TriggerSeatChange, Episode: rules.SeatChangeEpisode(c.Slot, c.Since),
			Seat: c.Seat, Machine: c.Machine, SinceMS: c.Since,
			Facts: model.Facts{Seat: names[c.Seat], Folder: c.Folder, Machine: c.MachineName, Was: names[c.PrevSeat],
				OrgOnly: c.Account == c.PrevAccount, Org: c.OrgName}})
	}
	day := now.In(loc).Format("2006-01-02")
	for _, lc := range calib.LimitChanges(views) {
		prefix := rules.LimitChangePrefix(lc.Model, lc.Up)
		dup, err := st.AlertEpisodeSince(ctx, prefix, now.Add(-calib.LimitDedupe).UnixMilli())
		if err != nil {
			return nil, err
		}
		if dup {
			continue
		}
		out = append(out, model.Notice{Trigger: model.TriggerLimitChange, Episode: prefix + day, SinceMS: now.UnixMilli(),
			Facts: model.Facts{Model: lc.Model, Up: lc.Up, Change: lc.Change, Moved: lc.Moved, Refs: lc.Refs}})
	}
	return out, nil
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
			out = append(out, calib.Reading{At: time.UnixMilli(x.TS), Source: x.Source, Session: x.Session})
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
func Loop(ctx context.Context, st *store.Store, now func() time.Time, notify Notifier, logf func(string, ...any)) {
	t := time.NewTimer(First)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		if err := Run(c, st, now(), notify); err != nil && ctx.Err() == nil {
			logf("calibration: %v", err)
		}
		cancel()
		t.Reset(Every)
	}
}
