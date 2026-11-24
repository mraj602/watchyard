package mover

import (
	"strings"
	"testing"
	"time"
)

// Live on pre5 (2026-11-25 03:20 IST): one pass moved two sessions off claude-3 (7d projected
// 90%, cap 88 with 2 pins): a background session at 4.6 points/h, and a mid-turn headless lane
// at 0.0 points/h. Busy with no burn frees nothing: only sessions burning at least MinBurn are
// candidates, and the greedy stops when a pick would not lower the projection.
func TestPlanMinBurn(t *testing.T) {
	now := time.Date(2026, 11, 24, 21, 50, 5, 0, time.UTC)
	src := Account{Key: "acct:c3/oA", Label: "claude-3", Rate: 11.7, Meter: 11.7, LoggedIn: true, Ratio: 9.1, Pins: 2, Reserve5: 20, Reserve7: 10,
		W5: Window{Level: 13.6, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 83.1, Reset: now.Add(24 * time.Hour)}}
	dst := Account{Key: "acct:c6/oB", Label: "claude-6", LoggedIn: true, Ratio: 8,
		W5: Window{Level: 10, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 31, Reset: now.Add(100 * time.Hour)}}
	planner := Session{ID: "planner", Account: src.Key, Rate: 4.6, Busy: true, Movable: true, Context: 100_000}
	idle := Session{ID: "home-89", Account: src.Key, Rate: 0, Busy: true, Movable: true, Context: 50_000,
		Started: now.Add(-15*time.Minute - 36*time.Second)}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{src, dst}, Sessions: []Session{planner, idle},
		Streak: map[string]int{src.Key + " " + Win7d: 5}}
	r := Plan(in)
	if len(r.Moves) != 1 || r.Moves[0].Session != "planner" {
		t.Fatalf("moves %+v, want the planner only", r.Moves)
	}
	if !skipped(r, "home-89", "burns 0.0 points/h") {
		t.Errorf("skips %+v", r.Skips)
	}
	// Under the floor (0.5 < 1) is not a candidate either; at the floor it is.
	in.Sessions[1].Rate = 0.5
	if r := Plan(in); len(r.Moves) != 1 || !skipped(r, "home-89", "under the 1.0") {
		t.Errorf("0.5 points/h: moves %+v skips %+v", r.Moves, r.Skips)
	}
	// The floor is a setting: 0 keeps any burn above 0, never none.
	in.Params.MinBurn = 0
	in.Sessions[1].Rate = 0
	if r := Plan(in); len(r.Moves) != 1 {
		t.Errorf("MinBurn 0 moved a session with no burn: %+v", r.Moves)
	}
}

// The greedy stops at a pick that would not lower the source's projection (lowers), and a
// young session (its burn left out by GateYoung) is never a candidate for its zero rate.
func TestPlanNoZeroBenefitPicks(t *testing.T) {
	p := DefaultParams
	pr := Projection{Window: Win7d, ratio: 8, Reset5: 3 * time.Hour, Reset7: 24 * time.Hour}
	if p.lowers(pr, 0) || !p.lowers(pr, 0.2) {
		t.Error("lowers: zero must not lower, a burn must")
	}
	now := time.Date(2026, 11, 25, 1, 10, 0, 0, time.UTC)
	a := Account{Key: "acct:a/o", Rate: 60, Meter: 60, LoggedIn: true, Ratio: 7.2,
		W5: Window{Level: 30, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 36, Reset: now.Add(48 * time.Hour)}}
	b := Account{Key: "acct:b/o", LoggedIn: true, Ratio: 7.2, W5: Window{Level: 0, Reset: now.Add(4 * time.Hour)},
		W7: Window{Level: 10, Reset: now.Add(90 * time.Hour)}}
	old := Session{ID: "old", Account: a.Key, Rate: 30, Busy: true, Movable: true, Started: now.Add(-time.Hour)}
	old2 := Session{ID: "old2", Account: a.Key, Rate: 30, Busy: true, Movable: true, Started: now.Add(-time.Hour)}
	young := Session{ID: "young", Account: a.Key, Rate: 40, Busy: true, Movable: true, Started: now.Add(-5 * time.Minute)}
	in := Input{Now: now, Params: p, Accounts: []Account{a, b}, Sessions: []Session{old, old2, young},
		Streak: map[string]int{a.Key + " " + Win5h: 5}}
	r := Plan(in)
	for _, mv := range r.Moves {
		if mv.Session == "young" || mv.Rate <= 0 {
			t.Errorf("moved %+v", mv)
		}
	}
	if !skipped(r, "young", "under 15 min old") {
		t.Errorf("skips %+v", r.Skips)
	}
}

// A session's own rate is its real recent tokens once it is old enough: GateYoung zeroes it
// only while it is young, and its account's projection counts it again after.
func TestGateYoungThenCounted(t *testing.T) {
	now := time.Date(2026, 11, 25, 1, 10, 0, 0, time.UTC)
	a := Account{Key: "acct:a/o", Rate: 12, Meter: 12, LoggedIn: true, Ratio: 7.2,
		W5: Window{Level: 10, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 36, Reset: now.Add(48 * time.Hour)}}
	s := Session{ID: "s", Account: a.Key, Rate: 12, Busy: true, Movable: true, Started: now.Add(-14 * time.Minute)}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{a}, Sessions: []Session{s}}
	if g := GateYoung(in); g.Sessions[0].Rate != 0 || g.Accounts[0].Rate != 0 {
		t.Fatalf("young: %+v %+v", g.Sessions[0], g.Accounts[0])
	}
	in.Now = now.Add(2 * time.Minute) // 16 minutes old: its rate and its account's are its tokens
	if g := GateYoung(in); g.Sessions[0].Rate != 12 || g.Accounts[0].Rate != 12 {
		t.Fatalf("old enough: %+v %+v", g.Sessions[0], g.Accounts[0])
	}
}

func skipped(r Result, id, why string) bool {
	for _, s := range r.Skips {
		if s.Session == id && strings.Contains(s.Why, why) {
			return true
		}
	}
	return false
}
