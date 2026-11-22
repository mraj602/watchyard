package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// The rows of the usage notices and the usage relation (push.md 3.9; setup.md 8.5, Usage
// relation): seat_watch, seat_verdict and the slot_login periods behind seat_change.

// WatchRow is a seat's seat_watch row.
type WatchRow struct {
	Cursor          int64
	State, Relation []byte
}

// WatchRows returns every seat_watch row by seat id.
func (s *Store) WatchRows(ctx context.Context) (map[string]WatchRow, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT seat_id, cursor, state, relation FROM seat_watch`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]WatchRow{}
	for rows.Next() {
		var id, state, rel string
		var w WatchRow
		if err := rows.Scan(&id, &w.Cursor, &state, &rel); err != nil {
			return nil, err
		}
		w.State, w.Relation = []byte(state), []byte(rel)
		out[id] = w
	}
	return out, rows.Err()
}

// SaveWatch writes a seat's seat_watch row.
func (tx *Tx) SaveWatch(seat string, cursor int64, state, relation []byte) error {
	if !json.Valid(state) || !json.Valid(relation) {
		return errors.New("store: watch state and relation must be JSON")
	}
	_, err := tx.exec(`INSERT INTO seat_watch (seat_id, cursor, state, relation, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(seat_id) DO UPDATE SET cursor = excluded.cursor, state = excluded.state,
			relation = excluded.relation, updated_at = excluded.updated_at`,
		seat, cursor, string(state), string(relation), tx.NowMS())
	return err
}

// SeatRelation returns a seat's stored relation (ok false before the job's first run for it).
func (s *Store) SeatRelation(ctx context.Context, seat string) ([]byte, bool, error) {
	var rel string
	err := s.r.QueryRowContext(ctx, `SELECT relation FROM seat_watch WHERE seat_id = ?`, seat).Scan(&rel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(rel), true, nil
}

// SeatCalibration returns a seat's stored factors (ok false without a row).
func (s *Store) SeatCalibration(ctx context.Context, seat string) ([]byte, bool, error) {
	var f string
	err := s.r.QueryRowContext(ctx, `SELECT factors FROM seat_calibration WHERE seat_id = ?`, seat).Scan(&f)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(f), true, nil
}

// VerdictRow is a seat's seat_verdict row with its machine's name.
type VerdictRow struct {
	Machine     string
	MachineName string
	TS          int64
	Strategy    string
	Verdict     []byte
}

// PutSeatVerdict keeps a verdict about a seat unless a newer one is stored (record.md 4.11).
func (tx *Tx) PutSeatVerdict(seat, machine string, ts int64, strategy string, verdict []byte, record string) error {
	if !json.Valid(verdict) {
		return errors.New("store: verdict must be JSON")
	}
	_, err := tx.exec(`INSERT INTO seat_verdict (seat_id, machine_id, ts, strategy, verdict, record_id) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(seat_id) DO UPDATE SET machine_id = excluded.machine_id, ts = excluded.ts, strategy = excluded.strategy,
			verdict = excluded.verdict, record_id = excluded.record_id
		WHERE excluded.ts >= seat_verdict.ts`, seat, machine, ts, strategy, string(verdict), record)
	return err
}

// SeatVerdict returns a seat's newest verdict (ok false when none has arrived).
func (s *Store) SeatVerdict(ctx context.Context, seat string) (VerdictRow, bool, error) {
	var v VerdictRow
	var verdict string
	err := s.r.QueryRowContext(ctx, `SELECT v.machine_id, coalesce(m.name, ''), v.ts, v.strategy, v.verdict
		FROM seat_verdict v LEFT JOIN machine m ON m.id = v.machine_id WHERE v.seat_id = ?`, seat).
		Scan(&v.Machine, &v.MachineName, &v.TS, &v.Strategy, &verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	v.Verdict = []byte(verdict)
	return v, true, nil
}

// SeatChange is a folder's login period that moved it to another seat (push.md 3.9).
type SeatChange struct {
	Slot                           int64
	Since                          int64
	Machine, MachineName, Folder   string
	OrgName                        string // the new seat's org name, "" when unknown
	Seat, Account, Org             string
	PrevSeat, PrevAccount, PrevOrg string
}

// SeatChanges returns the login periods with a seat that opened at or after since, oldest
// first, whose folder's previous period with a seat had another seat.
func (s *Store) SeatChanges(ctx context.Context, since int64) ([]SeatChange, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT l.slot_id, l.since_at, sl.machine_id, coalesce(m.name, ''), sl.folder, l.seat_id, se.account_uuid,
			se.org_uuid, coalesce(o.name, ''), p.seat_id, ps.account_uuid, ps.org_uuid
		FROM slot_login l
		JOIN slot sl ON sl.id = l.slot_id
		LEFT JOIN machine m ON m.id = sl.machine_id
		JOIN seat se ON se.id = l.seat_id
		LEFT JOIN org o ON o.uuid = se.org_uuid
		JOIN slot_login p ON p.slot_id = l.slot_id AND p.since_at = (
			SELECT max(q.since_at) FROM slot_login q WHERE q.slot_id = l.slot_id AND q.since_at < l.since_at AND q.seat_id IS NOT NULL)
		JOIN seat ps ON ps.id = p.seat_id
		WHERE l.since_at >= ? AND l.seat_id IS NOT NULL AND p.seat_id <> l.seat_id
		ORDER BY l.since_at, l.slot_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeatChange
	for rows.Next() {
		var c SeatChange
		if err := rows.Scan(&c.Slot, &c.Since, &c.Machine, &c.MachineName, &c.Folder, &c.Seat, &c.Account, &c.Org, &c.OrgName,
			&c.PrevSeat, &c.PrevAccount, &c.PrevOrg); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AlertEpisodeSince reports whether an alert whose episode starts with prefix was created at
// or after since (the limit_change dedupe, push.md 3.9).
func (s *Store) AlertEpisodeSince(ctx context.Context, prefix string, since int64) (bool, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM alert
		WHERE created_at >= ? AND substr(episode, 1, length(?)) = ?`, since, prefix, prefix).Scan(&n)
	return n > 0, err
}

// BoardSeatNames is every seat's board name (SeatDisplayNames), hidden seats included: the
// push text's names (push.md 5.5).
func (s *Store) BoardSeatNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT se.id, coalesce(se.label, ''), coalesce(a.email, '') FROM seat se
		LEFT JOIN account a ON a.uuid = se.account_uuid ORDER BY se.sort_order, se.created_at, se.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var seats []SeatIdent
	for rows.Next() {
		var x SeatIdent
		if err := rows.Scan(&x.ID, &x.Label, &x.Email); err != nil {
			return nil, err
		}
		seats = append(seats, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return SeatDisplayNames(seats), nil
}

// SeatByLogin finds the seat of an account and org (ok false when the hub has none).
func (tx *Tx) SeatByLogin(account, org string) (string, bool, error) {
	var id string
	err := tx.queryRow(`SELECT id FROM seat WHERE account_uuid = ? AND org_uuid = ?`, account, org).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}
