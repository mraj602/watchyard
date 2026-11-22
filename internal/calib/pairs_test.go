package calib

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func pct(v float64) *float64 { return &v }

func at(min float64) time.Time { return t0.Add(time.Duration(min * float64(time.Minute))) }

// rd is a reading: five and seven values (negative: none), at minutes after t0, the 5-hour
// window ending at r5 minutes and the weekly one at r7 minutes.
func rd(min float64, src string, five float64, r5 float64, seven float64, r7 float64) Reading {
	r := Reading{At: at(min), Source: src}
	if five >= 0 {
		r.Five, r.FiveReset = pct(five), at(r5)
	}
	if seven >= 0 {
		r.Seven, r.SevenReset = pct(seven), at(r7)
	}
	return r
}

func TestMeterBetween(t *testing.T) {
	m := NewMeter([]Use{{at(10), 5}, {at(0), 1}, {at(20), 7}, {at(20), 1}})
	cases := []struct {
		from, to float64
		want     float64
	}{{-1, 0, 1}, {0, 10, 5}, {0, 20, 13}, {10, 19, 0}, {20, 30, 0}, {-5, 100, 14}, {15, 5, 0}}
	for _, c := range cases {
		if got := m.Between(at(c.from).UnixMilli(), at(c.to).UnixMilli()); got != c.want {
			t.Errorf("Between(%v, %v] = %v, want %v", c.from, c.to, got, c.want)
		}
	}
	if (*Meter)(nil).Between(0, 1) != 0 {
		t.Fatal("nil meter")
	}
}

func TestPairsFromReadings(t *testing.T) {
	m := NewMeter([]Use{{at(3), 1e6}, {at(7), 2e6}, {at(12), 4e6}})
	var tr Tracker
	steps := []Step{
		tr.Add(rd(0, "statusline", 10, 120, 30, 5000), m),
		tr.Add(rd(5, "statusline", 12, 120, 30, 5000), m),
		tr.Add(rd(10, "web", 15, 121, 31, 5001), m),
	}
	if steps[0].Five != nil || steps[0].FiveReset {
		t.Fatalf("first reading made a pair: %+v", steps[0])
	}
	p := steps[1].Five
	if p == nil || p.D5 != 2 || p.U != 1e6 || p.D7 == nil || *p.D7 != 0 || p.Clean {
		t.Fatalf("pair 1 = %+v", p)
	}
	p = steps[2].Five
	if p == nil || p.D5 != 3 || p.U != 2e6 || p.D7 == nil || *p.D7 != 1 || p.Window != at(121).UnixMilli() {
		t.Fatalf("pair 2 = %+v (resets_at within 5 minutes are one window)", p)
	}
	if steps[2].Seven == nil || steps[2].Seven.D7 != 1 || steps[2].Seven.U != 2e6 {
		t.Fatalf("seven pair = %+v", steps[2].Seven)
	}
}

func TestStaleAndHeldReadings(t *testing.T) {
	var tr Tracker
	tr.Add(rd(0, "statusline", 40, 120, -1, 0), nil)
	// A session idle through a reset reports the old window: skipped.
	tr.Add(rd(1, "statusline", 2, 420, -1, 0), nil) // new window (reset at 120 passed)
	if st := tr.Add(rd(2, "statusline", 41, 120, -1, 0), nil); st.Five != nil || st.FiveReset {
		t.Fatalf("stale reading used: %+v", st)
	}
	// A reading more than a point under is held and then dropped when the next is not.
	tr.Add(rd(3, "statusline", 10, 420, -1, 0), nil)
	if st := tr.Add(rd(4, "statusline", 5, 420, -1, 0), nil); st.Five != nil || st.FiveReset {
		t.Fatalf("held reading used: %+v", st)
	}
	st := tr.Add(rd(5, "statusline", 11, 420, -1, 0), nil)
	if st.Five == nil || st.Five.D5 != 1 || st.Five.T0 != at(3).UnixMilli() {
		t.Fatalf("pair after a dropped held reading = %+v", st.Five)
	}
	// One point under is not a drop (two sessions a little apart).
	if st := tr.Add(rd(6, "statusline", 10, 420, -1, 0), nil); st.Five == nil || st.Five.D5 != -1 {
		t.Fatalf("one point under: %+v", st.Five)
	}
	// Two readings in a row under: a confirmed drop (an owner reset), no pair across it.
	tr.Add(rd(7, "web", 0, 420, -1, 0), nil)
	st = tr.Add(rd(8, "statusline", 1, 420, -1, 0), nil)
	if st.Five != nil || !st.FiveReset {
		t.Fatalf("confirmed drop made a pair: %+v", st)
	}
	st = tr.Add(rd(9, "statusline", 3, 420, -1, 0), nil)
	if st.Five == nil || st.Five.D5 != 2 {
		t.Fatalf("pair after the drop = %+v", st.Five)
	}
	// A reading without resets_at (idle window) is not used.
	if st := tr.Add(rd(10, "web", 0, 0, -1, 0), nil); st.Five != nil {
		t.Fatalf("idle reading used: %+v", st)
	}
	// A new window: a reset between two readings, no pair.
	st = tr.Add(rd(500, "statusline", 4, 800, -1, 0), nil)
	if st.Five != nil || !st.FiveReset {
		t.Fatalf("reset made a pair: %+v", st)
	}
}

func TestCleanPairs(t *testing.T) {
	m := NewMeter([]Use{{at(32), 1000}})
	var tr Tracker
	tr.Add(rd(0, "web", 10, 200, 20, 5000), m)
	if st := tr.Add(rd(5, "web", 12, 200, 20, 5000), m); st.Five == nil || !st.Five.Clean {
		t.Fatalf("web-web without tokens is clean: %+v", st.Five)
	}
	// A status-line reading between two web readings (even a skipped one) means a session ran.
	tr.Add(rd(7, "statusline", 30, 100, -1, 0), m) // stale window: skipped, still a session
	if st := tr.Add(rd(10, "web", 13, 200, 20, 5000), m); st.Five == nil || st.Five.Clean {
		t.Fatalf("status line between: not clean: %+v", st.Five)
	}
	if st := tr.Add(rd(15, "web", 13, 200, 20, 5000), m); st.Five == nil || !st.Five.Clean {
		t.Fatalf("clean again: %+v", st.Five)
	}
	tr.Add(rd(20, "statusline", 14, 200, 20, 5000), m)
	if st := tr.Add(rd(25, "web", 14, 200, 20, 5000), m); st.Five == nil || st.Five.Clean {
		t.Fatalf("status-line start: not clean: %+v", st.Five)
	}
	if st := tr.Add(rd(35, "web", 15, 200, 20, 5000), m); st.Five == nil || st.Five.Clean || st.Five.U != 1000 {
		t.Fatalf("tokens between: not clean: %+v", st.Five)
	}
}

func TestSortReadings(t *testing.T) {
	rs := []Reading{rd(5, "statusline", 1, 9, -1, 0), rd(1, "local", 1, 9, -1, 0), rd(1, "web", 1, 9, -1, 0), rd(1, "statusline", 1, 9, -1, 0)}
	SortReadings(rs)
	got := ""
	for _, r := range rs {
		got += r.Source + " "
	}
	if got != "web statusline local statusline " {
		t.Fatalf("order: %s", got)
	}
}
