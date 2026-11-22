package store

import (
	"context"
	"time"
)

// AccountLease is one account_lease row (record.md 4.10): a pick of `wy account next`.
type AccountLease struct {
	ID        string // payload.lease_id
	Seat      string
	Machine   string
	TS        int64 // pick time, ms
	SessionID string
	Kind      string
	C5, CF    float64
	Strategy  string
	Urgent    bool
	Model     string
	RecordID  string
}

// InsertAccountLease stores a lease once per lease id; a resent record changes nothing.
func (tx *Tx) InsertAccountLease(l AccountLease) error {
	urgent := 0
	if l.Urgent {
		urgent = 1
	}
	_, err := tx.exec(`INSERT INTO account_lease (id, seat_id, machine_id, ts, session_id, kind, c5, cf, strategy, urgent, model, record_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		l.ID, l.Seat, l.Machine, l.TS, nullStr(l.SessionID), l.Kind, l.C5, l.CF, l.Strategy, urgent, nullStr(l.Model), l.RecordID)
	return err
}

// UsageLease is api.yaml UsageLease: another machine's lease, without its session id or
// machine.
type UsageLease struct {
	At          Time    `json:"at"`
	Kind        string  `json:"kind"`
	C5          float64 `json:"c5"`
	CF          float64 `json:"cf"`
	Urgent      bool    `json:"urgent"`
	HasSession  bool    `json:"has_session"`
	Launched    bool    `json:"launched"`
	Ended       bool    `json:"ended"`
	BurnedUnits float64 `json:"burned_units"`
	Seen        bool    `json:"seen"`
}

// leaseUnits is the weighted token units of a token_row (setup.md 8.5, the token meter).
const leaseUnits = `input + 5.0 * output + 1.25 * cache_5m + 2.0 * cache_1h + 0.1 * cache_read`

// UsageLeases returns a seat's leases since since that machine notMachine did not record,
// oldest first: the newest limit of them. Launched, ended, burned units and seen come from
// the session and token rows of the lease's session id since the pick.
func (s *Store) UsageLeases(ctx context.Context, seat, notMachine string, since time.Time, limit int) ([]UsageLease, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT l.ts, l.kind, l.c5, l.cf, l.urgent, l.session_id IS NOT NULL,
			s.id IS NOT NULL, coalesce(s.ended_at IS NOT NULL, 0),
			coalesce((SELECT sum(`+leaseUnits+`) FROM token_row t WHERE t.session_id = l.session_id AND t.ts >= l.ts), 0),
			EXISTS (SELECT 1 FROM token_row t WHERE t.session_id = l.session_id AND t.ts >= l.ts)
		FROM account_lease l LEFT JOIN session s ON s.id = l.session_id
		WHERE l.seat_id = ? AND l.machine_id != ? AND l.ts >= ?
		ORDER BY l.ts DESC, l.id DESC LIMIT ?`, seat, notMachine, since.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageLease{}
	for rows.Next() {
		var l UsageLease
		var ts int64
		if err := rows.Scan(&ts, &l.Kind, &l.C5, &l.CF, &l.Urgent, &l.HasSession, &l.Launched, &l.Ended, &l.BurnedUnits, &l.Seen); err != nil {
			return nil, err
		}
		l.At = Time(ts)
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
