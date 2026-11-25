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
	// Near the weekly reset the aim ramps (setup.md 8.5, Drain aim): a drain until 1 h before it
	// goes on at 98 and ends at 99.
	reset := now.Add(2 * time.Hour)
	for w, want := range map[float64]string{98: "draining (owner, until 13:00)", 99: ""} {
		weekly := w
		s := HubSeat{ID: "seat_4", Label: &label, Windows: []HubWindow{{Key: "seven_day", UsedPct: &weekly, ResetsAt: &reset}}}
		if g := newCtx(Options{Now: now, Loc: time.UTC, Drains: drains(now.Add(time.Hour))}).groupFrom(s, 0); g.Note != want {
			t.Errorf("%v%% an hour before the reset: note %q, want %q", w, g.Note, want)
		}
	}
}
