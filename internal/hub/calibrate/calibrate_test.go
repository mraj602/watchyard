package calibrate

import (
	"testing"

	"github.com/mraj602/watchyard/internal/hub/store"
)

func f(v float64) *float64 { return &v }

// Rows of one time, source and session are one reading; others stay apart, oldest first.
func TestReadings(t *testing.T) {
	rows := []store.CalibSample{
		{TS: 1000, Key: "five_hour", Source: "statusline", Session: "a", UsedPct: f(10), ResetsAt: 9000},
		{TS: 1000, Key: "seven_day", Source: "statusline", Session: "a", UsedPct: f(20), ResetsAt: 99000},
		{TS: 1000, Key: "five_hour", Source: "statusline", Session: "b", UsedPct: f(11), ResetsAt: 9000},
		{TS: 1000, Key: "five_hour", Source: "web", UsedPct: f(12), ResetsAt: 9000},
		{TS: 500, Key: "seven_day", Source: "web", UsedPct: f(19)},
	}
	rs := Readings(rows)
	if len(rs) != 4 {
		t.Fatalf("%d readings, want 4", len(rs))
	}
	if rs[0].At.UnixMilli() != 500 || rs[0].Five != nil || *rs[0].Seven != 19 || !rs[0].SevenReset.IsZero() {
		t.Fatalf("first %+v", rs[0])
	}
	if rs[1].Source != "web" || *rs[1].Five != 12 {
		t.Fatalf("web before status lines at one time: %+v", rs[1])
	}
	var a bool
	for _, r := range rs[2:] {
		if r.Five != nil && *r.Five == 10 {
			a = r.Seven != nil && *r.Seven == 20 && r.SevenReset.UnixMilli() == 99000 && r.FiveReset.UnixMilli() == 9000
		}
	}
	if !a {
		t.Fatalf("session a's rows not joined: %+v", rs)
	}
}
