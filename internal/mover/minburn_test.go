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

// Live on pre5 (2026-11-25 04:00 IST), claude-3: level 7d 84.1, P7 89.1, cap 88 (2 pins),
// 9.1 points/h mostly from its pinned sessions. The usual target, cap − Hysteresis = 83, is
// under the level now: no set of moves could meet it, so every unpinned session was moved off,
// one per pass, down to 0.2 points/h. With the level over cap − Hysteresis the target is the cap;
// what is left under the floor is reported as "only pinned burn left".
func TestPlanUnreachableTarget(t *testing.T) {
	now := time.Date(2026, 11, 24, 22, 30, 0, 0, time.UTC)
	src := Account{Key: "acct:c3/oA", Label: "claude-3", Rate: 9.1, Meter: 9.1, LoggedIn: true, Ratio: 9.1, Pins: 2, Reserve5: 20, Reserve7: 10,
		W5: Window{Level: 30, Reset: now.Add(time.Hour)}, W7: Window{Level: 84.1, Reset: now.Add(53 * time.Hour)}}
	dst := Account{Key: "acct:c6/oB", Label: "claude-6", LoggedIn: true, Ratio: 8,
		W5: Window{Level: 10, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 31, Reset: now.Add(100 * time.Hour)}}
	old := now.Add(-2 * time.Hour)
	ss := []Session{
		{ID: "pin-a", Account: src.Key, Rate: 2.5, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
		{ID: "pin-b", Account: src.Key, Rate: 2.2, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
		{ID: "big", Account: src.Key, Rate: 3.0, Busy: true, Movable: true, Context: 80_000, Started: old},
		{ID: "mid", Account: src.Key, Rate: 1.2, Busy: true, Movable: true, Context: 80_000, Started: old},
		{ID: "tiny", Account: src.Key, Rate: 0.2, Busy: true, Movable: true, Context: 80_000, Started: old},
	}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{src, dst}, Sessions: ss,
		Streak: map[string]int{src.Key + " " + Win7d: 5}}
	r := Plan(in)
	pr := r.Accounts[0]
	if pr.Cap7 != 88 || pr.P7 < 89 || pr.P7 > 89.2 {
		t.Fatalf("projection %+v", pr)
	}
	// Moving "big" (3.0) brings P7 to 87.4, under the cap: enough; "mid" stays.
	if len(r.Moves) != 1 || r.Moves[0].Session != "big" || !skipped(r, "mid", "enough moved") {
		t.Fatalf("moves %+v skips %+v", r.Moves, r.Skips)
	}
	// The usual target (83) would have moved "mid" too, and every later burner over the floor.
	in.Params.MinBurn = -1
	if r := Plan(in); len(r.Moves) < 2 {
		t.Errorf("the old target moved %d", len(r.Moves))
	}
	// Next passes: big and mid gone, the pins and tiny left, still over the cap: nothing to
	// move, and --plan says why.
	in.Params = DefaultParams
	src.Rate, src.Meter = 4.9, 4.9
	src.W7.Level = 85.6
	in.Accounts[0] = src
	in.Sessions = []Session{ss[0], ss[1], ss[4]}
	r = Plan(in)
	if len(r.Moves) != 0 || !strings.Contains(r.Accounts[0].Left, "only pinned burn left (4.7 points/h pinned, 0.2 unpinned)") {
		t.Fatalf("moves %+v account %+v", r.Moves, r.Accounts[0])
	}
}

// Live (2026-11-25 04:00-04:06): claude-2 (5h 13%, 7d 18%) had taken 3 moves in 15 minutes,
// two of them 0.2-0.5 points/h, so the next mover went to protected claude-7 (then projected
// 74% against its cap 60). Moves under the burn floor do not count against the move limit,
// the limit is 5 and a burn limit of 40 points/h, and an aggressive account with room takes a
// move over them before a protected one does.
func TestPlanChurnLimitPrefersAggressive(t *testing.T) {
	now := time.Date(2026, 11, 24, 22, 36, 0, 0, time.UTC)
	src := Account{Key: "acct:c4/oA", Label: "claude-4", Rate: 30, Meter: 30, LoggedIn: true, Ratio: 8,
		W5: Window{Level: 60, Reset: now.Add(2 * time.Hour)}, W7: Window{Level: 50, Reset: now.Add(80 * time.Hour)}}
	c2 := Account{Key: "acct:c2/oA", Label: "claude-2", LoggedIn: true, Ratio: 8,
		W5: Window{Level: 13, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 18, Reset: now.Add(100 * time.Hour)}}
	c7 := Account{Key: "acct:c7/oA", Label: "claude-7", LoggedIn: true, Ratio: 8, Protected: true,
		W5: Window{Level: 5, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 20, Reset: now.Add(100 * time.Hour)}}
	old := now.Add(-time.Hour)
	ss := []Session{
		{ID: "lane-1", Account: src.Key, Rate: 15, Busy: true, Movable: true, Context: 60_000, Started: old},
		{ID: "lane-2", Account: src.Key, Rate: 15, Busy: true, Movable: true, Context: 60_000, Started: old},
	}
	recent := []Record{
		{Session: "x1", From: src.Key, To: c2.Key, At: now.Add(-6 * time.Minute), Rate: 0.2, RateKnown: true},
		{Session: "x2", From: src.Key, To: c2.Key, At: now.Add(-5 * time.Minute), Rate: 0.5, RateKnown: true},
		{Session: "x3", From: src.Key, To: c2.Key, At: now.Add(-4 * time.Minute), Rate: 6, RateKnown: true},
	}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{src, c2, c7}, Sessions: ss, Recent: recent,
		Streak: map[string]int{src.Key + " " + Win5h: 5}}
	r := Plan(in)
	if len(r.Moves) == 0 {
		t.Fatalf("no moves: %+v", r.Skips)
	}
	for _, m := range r.Moves {
		if m.To != c2.Key {
			t.Errorf("%s went to %s, want claude-2", m.Session, m.To)
		}
	}
	// As before (every move counts, 3 a window, no preference): the protected account took it.
	in.Params.oldChurn, in.Params.AccountMoves, in.Params.AccountBurn = true, 3, 0
	if r := Plan(in); len(r.Moves) == 0 || r.Moves[0].To != c7.Key {
		t.Errorf("old limits: %+v", r.Moves)
	}
}

// Expiring room (live 2026-11-25 04:25): protected claude-5 at 85% of its week, resetting in
// about an hour, its 5 hours unused, took nothing. Within ExpiringRoom of its weekly reset a
// protected account's weekly cap is Cap7; its 5-hour cap stays Cap5Protected. A session expected
// to end soon is projected only until then (a short lane cannot burn 5 hours).
func TestPlanExpiringRoomAndShortSessions(t *testing.T) {
	now := time.Date(2026, 11, 24, 22, 55, 0, 0, time.UTC)
	src := Account{Key: "acct:c4/oA", Label: "claude-4", Rate: 12, Meter: 12, LoggedIn: true, Ratio: 8,
		W5: Window{Level: 80, Reset: now.Add(2 * time.Hour)}, W7: Window{Level: 50, Reset: now.Add(80 * time.Hour)}}
	c5 := Account{Key: "acct:c5/oA", Label: "claude-5", LoggedIn: true, Ratio: 8, Protected: true,
		W7: Window{Level: 85, Reset: now.Add(64 * time.Minute)}}
	lane := Session{ID: "lane", Account: src.Key, Rate: 12, Busy: true, Movable: true, Context: 50_000, Started: now.Add(-time.Hour)}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{src, c5}, Sessions: []Session{lane},
		Streak: map[string]int{src.Key + " " + Win5h: 5}}
	r := Plan(in)
	if len(r.Moves) != 1 || r.Moves[0].To != c5.Key || !r.Accounts[1].Expiring || r.Accounts[1].Cap7 != 98 || r.Accounts[1].Cap5 != 80 {
		t.Fatalf("moves %+v accounts %+v", r.Moves, r.Accounts)
	}
	in.Params.ExpiringRoom = 0
	if r := Plan(in); len(r.Moves) != 1 || r.Moves[0].To != "" {
		t.Fatalf("without expiring room: %+v", r.Moves)
	}
	// The same lane expected to end in 20 minutes, counted for ExpectFloor (30): 80 + 12 × 0.5
	// = 86, under the cap (to the reset it is 104).
	in.Params = DefaultParams
	in.Sessions[0].Expected = 20 * time.Minute
	if r := Plan(in); len(r.Moves) != 0 || r.Accounts[0].P5 > 86.1 {
		t.Fatalf("short session: %+v %+v", r.Moves, r.Accounts[0])
	}
}

// Live 2026-11-25 04:25: a pass read claude-4's 5-hour level as 100% (it was ~60%) and its
// moves said "100% at 04:24", before the pass. The hit time is now + (100 − level)/R5 and is
// left out when that is not in the future.
func TestPlanHitTime(t *testing.T) {
	now := time.Date(2026, 11, 24, 22, 55, 0, 0, time.UTC)
	src := Account{Key: "acct:c4/oA", Label: "claude-4", Rate: 15, Meter: 15, LoggedIn: true, Ratio: 8,
		W5: Window{Level: 60, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 40, Reset: now.Add(100 * time.Hour)}}
	dst := Account{Key: "acct:c5/oA", Label: "claude-5", LoggedIn: true, Ratio: 8,
		W5: Window{Level: 10, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 20, Reset: now.Add(100 * time.Hour)}}
	old := now.Add(-2 * time.Hour)
	ss := []Session{
		{ID: "a", Account: src.Key, Rate: 9, Busy: true, Movable: true, Context: 80_000, Started: old},
		{ID: "b", Account: src.Key, Rate: 6, Busy: true, Movable: true, Context: 80_000, Started: old},
	}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{src, dst}, Sessions: ss,
		Streak: map[string]int{src.Key + " " + Win5h: 5}}
	r := Plan(in)
	if len(r.Moves) == 0 {
		t.Fatalf("no moves: %+v %+v", r.Accounts, r.Skips)
	}
	want := now.Add(time.Duration(40.0 / 15 * float64(time.Hour)))
	if m := r.Moves[0]; !m.HitAt.Equal(want) || !strings.Contains(m.Reason, "100% at "+Clock(want, now)) {
		t.Fatalf("hit %v (want %v), reason %q", m.HitAt, want, m.Reason)
	}
	// At 100% already: no hit time, and no "100% at" before the pass.
	src.W5.Level = 100
	in.Accounts[0] = src
	r = Plan(in)
	if len(r.Moves) == 0 {
		t.Fatalf("no moves at 100%%: %+v", r.Skips)
	}
	for _, m := range r.Moves {
		if !m.HitAt.IsZero() || strings.Contains(m.Reason, "100% at") {
			t.Errorf("move %s: hit %v reason %q", m.Session, m.HitAt, m.Reason)
		}
	}
	if pr := r.Accounts[0]; !pr.HitAt(now).IsZero() {
		t.Errorf("projection hit at %v", pr.HitAt(now))
	}
}
