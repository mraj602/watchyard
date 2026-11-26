package mover

import (
	"strings"
	"testing"
	"time"
)

// A reset or hit time over 24 hours away names its day (live on pre5: "before its 03:30
// reset" for a weekly reset on Thursday, two days away).
func TestClockNamesTheDay(t *testing.T) {
	now := time.Date(2026, 11, 24, 21, 50, 0, 0, time.Local) // a Tuesday
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(3 * time.Hour), now.Add(3 * time.Hour).Format("15:04")},
		{now.Add(23 * time.Hour), now.Add(23 * time.Hour).Format("15:04")},
		{now.Add(29*time.Hour + 40*time.Minute), "Thu 03:30"},
		{now.Add(8 * 24 * time.Hour), now.Add(8 * 24 * time.Hour).Format("Jan 2 15:04")},
	}
	for _, c := range cases {
		if got := Clock(c.at, now); got != c.want {
			t.Errorf("Clock(%v) = %q, want %q", c.at, got, c.want)
		}
	}
	mv := Move{Window: Win7d, P: 90, ResetAt: now.Add(29*time.Hour + 40*time.Minute)}
	if n := Note("claude-3", "claude-6", now, mv); !strings.Contains(n, "before its Thu 03:30 reset") {
		t.Errorf("note %q", n)
	}
	mv.Stopped = true
	if r := DefaultParams.reason(Account{}, mv, "", now); !strings.Contains(r, "resets Thu 03:30") {
		t.Errorf("reason %q", r)
	}
	a := PinAlert{At: now, Window: Win7d, HitAt: now.Add(time.Hour), ResetAt: mv.ResetAt}
	if s := a.Text(); !strings.Contains(s, "resets Thu 03:30") || !strings.Contains(s, now.Add(time.Hour).Format("15:04")) {
		t.Errorf("alert %q", s)
	}
}
