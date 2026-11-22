package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
)

// UsageLeases measures the burn of live leases only, at most MaxBurn of them (the newest),
// and reads a session's rows once per request (setup.md 8.5).
func TestUsageLeasesBurnBound(t *testing.T) {
	st, ctx := openTargets(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	execT(t, st, ctx, `INSERT INTO account (uuid, first_seen, last_seen) VALUES ('acc', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO org (uuid, first_seen, last_seen) VALUES ('org', 1, 1)`)
	execT(t, st, ctx, `INSERT INTO seat (id, account_uuid, org_uuid, created_at) VALUES ('seat_a', 'acc', 'org', 1)`)
	execT(t, st, ctx, `INSERT INTO machine (id, name, first_seen) VALUES ('m_a', 'a', 1), ('m_b', 'b', 1)`)
	for i := 0; i < 5; i++ {
		at := now.Add(-time.Duration(50-i) * time.Minute).UnixMilli()
		sid := fmt.Sprintf("sess-%d", i)
		execT(t, st, ctx, `INSERT INTO account_lease (id, seat_id, machine_id, ts, session_id, kind, c5, cf, strategy, record_id)
			VALUES (?, 'seat_a', 'm_a', ?, ?, 'aggressive', 8, 8, 'lanes', 'r')`, fmt.Sprintf("L%d", i), at, sid)
		execT(t, st, ctx, `INSERT INTO token_row (key, session_id, ts, input, record_id) VALUES (?, ?, ?, 100, 'r')`,
			fmt.Sprintf("k%d", i), sid, at+1000)
	}
	bc := store.BurnCache{}
	q := store.LeaseQuery{Seat: "seat_a", Machine: "m_b", Since: now.Add(-7 * 24 * time.Hour), Limit: 500, Now: now,
		Life: 5 * time.Hour, MaxBurn: 2}
	ls, err := st.UsageLeases(ctx, q, bc)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 5 {
		t.Fatalf("%d leases", len(ls))
	}
	for i, l := range ls {
		measured := l.Burn != nil
		if measured != (i >= 3) || (measured && l.BurnedUnits != 100) || !l.Seen {
			t.Errorf("lease %d: burn %v units %v seen %v (only the newest 2 are measured)", i, l.Burn, l.BurnedUnits, l.Seen)
		}
	}
	if len(bc) != 2 {
		t.Errorf("cache holds %d burns, want 2", len(bc))
	}
	// A cached burn is not read again (the row added now is not seen in this request).
	execT(t, st, ctx, `INSERT INTO token_row (key, session_id, ts, input, record_id) VALUES ('k-more', 'sess-4', ?, 5, 'r')`,
		now.Add(-time.Minute).UnixMilli())
	if ls, err = st.UsageLeases(ctx, q, bc); err != nil || ls[4].BurnedUnits != 100 {
		t.Errorf("cached: %v %+v", err, ls[4])
	}
	if ls, err = st.UsageLeases(ctx, q, store.BurnCache{}); err != nil || ls[4].BurnedUnits != 105 {
		t.Errorf("a new request: %v %+v", err, ls[4])
	}
}
