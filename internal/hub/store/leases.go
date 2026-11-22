package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
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

// UsageLease is api.yaml UsageLease: a lease without its session id or machine.
type UsageLease struct {
	ID          string  `json:"id"`
	At          Time    `json:"at"`
	Kind        string  `json:"kind"`
	C5          float64 `json:"c5"`
	CF          float64 `json:"cf"`
	Urgent      bool    `json:"urgent"`
	HasSession  bool    `json:"has_session"`
	SessionHash *string `json:"session_hash"`
	Launched    bool    `json:"launched"`
	Ended       bool    `json:"ended"`
	BurnedUnits float64 `json:"burned_units"`
	Seen        bool    `json:"seen"`
	// Burn is the session's units per minute over the lease's life for a recent lease (nil
	// otherwise: JSON null).
	Burn []BurnMinute `json:"burn"`
}

// BurnMinute is one minute of a lease's burn: [seconds after the pick of the minute's newest
// row, rounded up; the minute's weighted units].
type BurnMinute [2]float64

// leaseUnits is the weighted token units of a token_row (setup.md 8.5, the token meter).
const leaseUnits = `input + 5.0 * output + 1.25 * cache_5m + 2.0 * cache_1h + 0.1 * cache_read`

// LeaseQuery selects the leases of one seat for GET /v1/usage (setup.md 8.5).
type LeaseQuery struct {
	Seat    string
	Machine string // the calling machine
	Own     bool   // its own leases; else every other machine's
	Since   time.Time
	Limit   int // the newest this many
	Now     time.Time
	// Life: a lease younger than this that names a session is recent (whether or not the
	// session ended), and its burn is measured; at most MaxBurn of them, the newest.
	Life    time.Duration
	MaxBurn int
}

// BurnCache holds the burns measured in one request, by session and pick time.
type BurnCache map[string][]BurnMinute

// UsageLeases returns a seat's leases as q selects them, oldest first. Launched and ended come
// from the session row (ended: SessionEnd, or gone from the session list), seen from one
// probe of the session's token rows since the pick; only recent leases have their burn read
// (one read of that session's rows by the token_row_session index, cached in bc).
func (s *Store) UsageLeases(ctx context.Context, q LeaseQuery, bc BurnCache) ([]UsageLease, error) {
	op := "!="
	if q.Own {
		op = "="
	}
	rows, err := s.r.QueryContext(ctx, `SELECT l.id, l.ts, l.kind, l.c5, l.cf, l.urgent, coalesce(l.session_id, ''),
			s.id IS NOT NULL, coalesce(s.ended_at IS NOT NULL, 0),
			l.session_id IS NOT NULL AND EXISTS (SELECT 1 FROM token_row t WHERE t.session_id = l.session_id AND t.ts >= l.ts)
		FROM account_lease l LEFT JOIN session s ON s.id = l.session_id
		WHERE l.seat_id = ? AND l.machine_id `+op+` ? AND l.ts >= ?
		ORDER BY l.ts DESC, l.id DESC LIMIT ?`, q.Seat, q.Machine, q.Since.UnixMilli(), q.Limit)
	if err != nil {
		return nil, err
	}
	out := []UsageLease{}
	var sids []string
	for rows.Next() {
		var l UsageLease
		var ts int64
		var sid string
		if err := rows.Scan(&l.ID, &ts, &l.Kind, &l.C5, &l.CF, &l.Urgent, &sid, &l.Launched, &l.Ended, &l.Seen); err != nil {
			rows.Close()
			return nil, err
		}
		l.At = Time(ts)
		if sid != "" {
			l.HasSession = true
			sum := sha256.Sum256([]byte(sid))
			h := hex.EncodeToString(sum[:])
			l.SessionHash = &h
		}
		out = append(out, l)
		sids = append(sids, sid)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	measured := 0
	for i := range out { // newest first
		l := &out[i]
		if sids[i] == "" || q.Now.Sub(time.UnixMilli(int64(l.At))) >= q.Life || measured >= q.MaxBurn {
			continue
		}
		measured++
		b, err := s.leaseBurn(ctx, sids[i], int64(l.At), q.Life, bc)
		if err != nil {
			return nil, err
		}
		l.Burn = b
		for _, m := range b {
			l.BurnedUnits += m[1]
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// leaseBurn is a session's units per minute from the pick at to at+life (never nil).
func (s *Store) leaseBurn(ctx context.Context, sid string, at int64, life time.Duration, bc BurnCache) ([]BurnMinute, error) {
	key := sid + "|" + strconv.FormatInt(at, 10)
	if b, ok := bc[key]; ok {
		return b, nil
	}
	rows, err := s.r.QueryContext(ctx, `SELECT max(ts), sum(`+leaseUnits+`) FROM token_row
		WHERE session_id = ? AND ts >= ? AND ts < ? GROUP BY ts / 60000 ORDER BY 1`, sid, at, at+life.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BurnMinute{}
	for rows.Next() {
		var ts int64
		var units float64
		if err := rows.Scan(&ts, &units); err != nil {
			return nil, err
		}
		after := (ts - at + 999) / 1000
		out = append(out, BurnMinute{float64(after), units})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if bc != nil {
		bc[key] = out
	}
	return out, nil
}
