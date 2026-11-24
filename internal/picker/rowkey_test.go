package picker

import (
	"testing"
	"time"
)

// A session moved from A to B counts its rows before the move to A, after it to B (live
// 2026-11-25: all its rows went to B, its last lease, and put B's 5-hour level at 100%).
func TestRowKeyFollowsMoves(t *testing.T) {
	t0 := time.Date(2026, 11, 24, 22, 0, 0, 0, time.UTC)
	tl := []leaseAt{{t0, "acct:A"}, {t0.Add(time.Hour), "acct:B"}}
	cases := []struct {
		at   time.Time
		slot string
		cur  string
		want string
	}{
		{t0.Add(30 * time.Minute), "acct:A", "acct:B", "acct:A"},
		{t0.Add(time.Hour), "acct:B", "acct:B", "acct:B"},
		{t0.Add(2 * time.Hour), "acct:B", "acct:B", "acct:B"},
		{t0.Add(-time.Minute), "acct:S", "acct:B", "acct:S"}, // before its first lease: the folder that ran it
	}
	for _, c := range cases {
		if got := rowKey(tl, c.at, c.slot, c.cur); got != c.want {
			t.Errorf("row at %s: %s, want %s", c.at.Format("15:04"), got, c.want)
		}
	}
	// No lease: the session's account, else the folder's.
	if got := rowKey(nil, t0, "acct:S", "acct:H"); got != "acct:H" {
		t.Errorf("unleased: %s", got)
	}
	if got := rowKey(nil, t0, "acct:S", ""); got != "acct:S" {
		t.Errorf("unleased, no account: %s", got)
	}
}
