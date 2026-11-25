package board

import (
	"testing"
	"time"
)

// An account the owner drains shows "draining (owner, until 23:59)" by its label, until then
// or until its weekly level reaches the drain aim.
func TestGroupOwnerDrainNote(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	label := "claude-4"
	seat := func(weekly float64) HubSeat {
		return HubSeat{ID: "seat_4", Label: &label, Windows: []HubWindow{{Key: "seven_day", UsedPct: &weekly}}}
	}
	drains := func(u time.Time) func() map[string]time.Time {
		return func() map[string]time.Time { return map[string]time.Time{"claude-4": u} }
	}
	for _, c := range []struct {
		name   string
		until  time.Time
		weekly float64
		want   string
	}{
		{"on", time.Date(2026, 11, 25, 23, 59, 0, 0, time.UTC), 53, "draining (owner, until 23:59)"},
		{"another day", time.Date(2026, 11, 26, 7, 0, 0, 0, time.UTC), 53, "draining (owner, until Thu 07:00)"},
		{"past", now.Add(-time.Minute), 53, ""},
		{"at the aim", now.Add(time.Hour), 97, ""},
	} {
		g := newCtx(Options{Now: now, Loc: time.UTC, Drains: drains(c.until)}).groupFrom(seat(c.weekly), 0)
		if g.Note != c.want {
			t.Errorf("%s: note %q, want %q", c.name, g.Note, c.want)
		}
	}
}
