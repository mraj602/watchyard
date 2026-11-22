package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The rows the hub's calibration job reads and writes (setup.md 8.5, Calibration). Every read
// is a range on an index: limit_sample's key (seat_id, window_key, ts, source),
// token_row_seat_ts, account_lease_seat_ts and token_row_session.

// CalibrationRow is one seat with its seat_calibration row (zero cursors and empty state when
// it has none yet).
type CalibrationRow struct {
	Seat, Account, Org string // the seat's login now
	// CalAccount and CalOrg are the login the row was calibrated for ("" without a row).
	CalAccount, CalOrg  string
	Cursor, LeaseCursor int64
	State               []byte
}

// CalibrationRows lists every seat (hidden ones too) with its calibration row.
func (s *Store) CalibrationRows(ctx context.Context) ([]CalibrationRow, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT se.id, se.account_uuid, se.org_uuid,
			coalesce(c.account_uuid, ''), coalesce(c.org_uuid, ''), coalesce(c.cursor, 0), coalesce(c.lease_cursor, 0), coalesce(c.state, '')
		FROM seat se LEFT JOIN seat_calibration c ON c.seat_id = se.id ORDER BY se.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalibrationRow
	for rows.Next() {
		var r CalibrationRow
		var state string
		if err := rows.Scan(&r.Seat, &r.Account, &r.Org, &r.CalAccount, &r.CalOrg, &r.Cursor, &r.LeaseCursor, &state); err != nil {
			return nil, err
		}
		r.State = []byte(state)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CalibSample is one five_hour or seven_day limit_sample row.
type CalibSample struct {
	TS       int64
	Key      string
	Source   string
	Session  string
	UsedPct  *float64
	ResetsAt int64 // 0: none
}

// CalibSamples returns a seat's five_hour and seven_day samples with after < ts <= upto,
// oldest first.
func (s *Store) CalibSamples(ctx context.Context, seat string, after, upto int64) ([]CalibSample, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT ts, window_key, source, coalesce(session_id, ''), used_pct, coalesce(resets_at, 0)
		FROM limit_sample WHERE seat_id = ? AND window_key IN ('five_hour', 'seven_day') AND ts > ? AND ts <= ?
		ORDER BY ts, source, window_key`, seat, after, upto)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalibSample
	for rows.Next() {
		var x CalibSample
		var used sql.NullFloat64
		if err := rows.Scan(&x.TS, &x.Key, &x.Source, &x.Session, &used, &x.ResetsAt); err != nil {
			return nil, err
		}
		if used.Valid {
			v := used.Float64
			x.UsedPct = &v
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// CalibUse is the weighted units of one token row at its time.
type CalibUse struct {
	TS    int64
	Units float64
}

// CalibToken is one token row of a seat: its time, weighted units (leaseUnits), model and
// counters (the usage relation prices them, setup.md 8.5).
type CalibToken struct {
	TS                                         int64
	Units                                      float64
	Model                                      string
	Input, Output, Cache5m, Cache1h, CacheRead int64
}

// CalibUnits returns a seat's token rows with after < ts <= upto, oldest first.
func (s *Store) CalibUnits(ctx context.Context, seat string, after, upto int64) ([]CalibToken, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT ts, `+leaseUnits+`, coalesce(model, ''), input, output, cache_5m, cache_1h, cache_read
		FROM token_row WHERE seat_id = ? AND ts > ? AND ts <= ? ORDER BY ts`, seat, after, upto)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalibToken
	for rows.Next() {
		var u CalibToken
		if err := rows.Scan(&u.TS, &u.Units, &u.Model, &u.Input, &u.Output, &u.Cache5m, &u.Cache1h, &u.CacheRead); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CalibLanes returns a seat's leases with after < ts <= upto that name a session the hub has,
// oldest first, with the weighted units of that session's token rows in the span after the
// pick (setup.md 8.5, lane price).
func (s *Store) CalibLanes(ctx context.Context, seat string, after, upto int64, span time.Duration) ([]CalibUse, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT l.ts,
			coalesce((SELECT sum(`+leaseUnits+`) FROM token_row t WHERE t.session_id = l.session_id AND t.ts >= l.ts AND t.ts < l.ts + ?), 0)
		FROM account_lease l JOIN session s ON s.id = l.session_id
		WHERE l.seat_id = ? AND l.ts > ? AND l.ts <= ? AND l.session_id IS NOT NULL
		ORDER BY l.ts`, span.Milliseconds(), seat, after, upto)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalibUse
	for rows.Next() {
		var u CalibUse
		if err := rows.Scan(&u.TS, &u.Units); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SaveCalibration writes a seat's calibration row.
func (tx *Tx) SaveCalibration(seat, account, org string, cursor, leaseCursor int64, state, factors []byte) error {
	if !json.Valid(state) || !json.Valid(factors) {
		return errors.New("store: calibration state and factors must be JSON")
	}
	_, err := tx.exec(`INSERT INTO seat_calibration (seat_id, account_uuid, org_uuid, cursor, lease_cursor, state, factors, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(seat_id) DO UPDATE SET account_uuid = excluded.account_uuid, org_uuid = excluded.org_uuid,
			cursor = excluded.cursor, lease_cursor = excluded.lease_cursor, state = excluded.state,
			factors = excluded.factors, updated_at = excluded.updated_at`,
		seat, account, org, cursor, leaseCursor, string(state), string(factors), tx.NowMS())
	return err
}

// CalibrationFactors returns every seat's stored factors (api.yaml Calibration), by seat id.
func (s *Store) CalibrationFactors(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT seat_id, factors FROM seat_calibration`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var id, f string
		if err := rows.Scan(&id, &f); err != nil {
			return nil, err
		}
		out[id] = json.RawMessage(f)
	}
	return out, rows.Err()
}
