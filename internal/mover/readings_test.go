package mover

import (
	"testing"
	"time"
)

// A trigger that only the readings' rate makes (the meter's projection is under the caps)
// needs ReadingPasses passes, even when its hit looks near; the streak is the trigger's
// (account and window).
func TestReadingsOnlyTrigger(t *testing.T) {
	now := time.Date(2026, 11, 24, 22, 24, 0, 0, time.UTC)
	in := Input{Now: now, Params: DefaultParams, Streak: map[string]int{}, Accounts: []Account{
		{Key: "a", Label: "a", LoggedIn: true, W5: Window{Level: 80, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 53, Reset: now.Add(90 * time.Hour)},
			Rate: 57, Meter: 2.5, FromReadings: true},
		{Key: "b", Label: "b", LoggedIn: true, W5: Window{Level: 5, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 10, Reset: now.Add(100 * time.Hour)}},
	}, Sessions: []Session{{ID: "s", Account: "a", Rate: 2.5, Busy: true, Movable: true}}}
	for pass := 1; pass <= 3; pass++ {
		r := Plan(in)
		pr := r.Accounts[0]
		if !pr.ReadingsOnly || pr.Streak != pass {
			t.Fatalf("pass %d: %+v", pass, pr)
		}
		if moved := len(r.Moves) > 0 && r.Moves[0].To != ""; moved != (pass == 3) {
			t.Fatalf("pass %d moves %+v", pass, r.Moves)
		}
		in.Streak = r.Streak
	}
	// The meter alone over the caps: the usual 2 passes.
	in.Streak = map[string]int{}
	in.Accounts[0].Meter, in.Sessions[0].Rate = 20, 20
	in.Accounts[0].Rate, in.Accounts[0].FromReadings = 20, false
	r := Plan(in)
	in.Streak = r.Streak
	if r.Accounts[0].ReadingsOnly || len(r.Moves) != 0 {
		t.Fatalf("meter over, pass 1: %+v %+v", r.Accounts[0], r.Moves)
	}
	if r := Plan(in); len(r.Moves) == 0 {
		t.Fatalf("meter over, pass 2: no move")
	}
	// A streak of a 5-hour trigger does not count for a weekly one.
	in.Streak = map[string]int{"a 7d": 5}
	if r := Plan(in); len(r.Moves) != 0 || r.Accounts[0].Streak != 1 {
		t.Fatalf("other window's streak counted: %+v", r.Accounts[0])
	}
}
