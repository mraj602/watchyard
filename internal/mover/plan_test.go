package mover

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

// acc is a logged-in aggressive account at 5-hour level l5 resetting in reset5 and weekly l7
// resetting in three days.
func acc(key string, l5 float64, reset5 time.Duration, l7 float64) Account {
	return Account{Key: key, Label: key, LoggedIn: true, W5: Window{Level: l5, Reset: t0.Add(reset5)},
		W7: Window{Level: l7, Reset: t0.Add(72 * time.Hour)}}
}

// lane is a movable background agent burning rate, busy or idle.
func lane(id, account string, rate float64, busy bool) Session {
	return Session{ID: id, Account: account, Rate: rate, Busy: busy, Context: 100_000, Movable: true}
}

// plan runs one pass with the spike filter already satisfied (Streak 1 on every account).
func plan(in Input) Result {
	if in.Params.Name == "" {
		in.Params = DefaultParams
	}
	if in.Now.IsZero() {
		in.Now = t0
	}
	if in.Streak == nil {
		in.Streak = map[string]int{}
		for _, a := range in.Accounts {
			in.Streak[a.Key+" "+Win5h], in.Streak[a.Key+" "+Win7d] = 1, 1
		}
	}
	return Plan(in)
}

func moved(r Result) map[string]string {
	m := map[string]string{}
	for _, mv := range r.Moves {
		m[mv.Session] = mv.To
	}
	return m
}

func skipWhy(r Result, id string) string {
	for _, k := range r.Skips {
		if k.Session == id {
			return k.Why
		}
	}
	return ""
}

func TestProjection(t *testing.T) {
	p := DefaultParams
	a := acc("a", 60, 2*time.Hour, 50)
	a.Rate = 20
	pr := p.Project(a, nil, 0, t0)
	if pr.P5 != 100 || pr.Window != Win5h || pr.Hit != 2*time.Hour {
		t.Fatalf("P5 %v window %q hit %v", pr.P5, pr.Window, pr.Hit)
	}
	// P7 = w + R5/ρ × min(to the weekly reset, 5 h).
	if want := 50 + 20/7.2*5; pr.P7 < want-1e-9 || pr.P7 > want+1e-9 {
		t.Fatalf("P7 %v want %v", pr.P7, want)
	}
	// A window not started projects a full 5 hours from 0.
	b := Account{Key: "b", Rate: 10, W7: Window{Level: 10, Reset: t0.Add(time.Hour)}}
	if pr := p.Project(b, nil, 0, t0); pr.P5 != 50 || pr.P7 != 10+10/7.2 {
		t.Fatalf("not started: P5 %v P7 %v", pr.P5, pr.P7)
	}
	// The sessions' own burn counts when the account's measure is lower.
	if pr := p.Project(acc("c", 0, time.Hour, 0), []Session{lane("x", "c", 30, true), lane("y", "c", 0, false)}, 0, t0); pr.R5 != 30 {
		t.Fatalf("R5 %v", pr.R5)
	}
	// Protected caps.
	pa := acc("p", 70, time.Hour, 10)
	pa.Protected, pa.Rate = true, 12
	if pr := p.Project(pa, nil, 0, t0); pr.Cap5 != 80 || pr.Window != Win5h {
		t.Fatalf("protected %+v", pr)
	}
}

// Rule 1: the 5-hour projection over the cap moves the highest burner, mid-turn allowed;
// idle sessions with no burn never move.
func TestPlanMovesTheBurner(t *testing.T) {
	r := plan(Input{
		Accounts: []Account{acc("a", 70, 2*time.Hour, 30), acc("b", 10, 4*time.Hour, 20)},
		Sessions: []Session{lane("hot", "a", 12, true), lane("warm", "a", 3, true), lane("idle", "a", 0, false)},
	})
	if got := moved(r); len(got) != 1 || got["hot"] != "b" {
		t.Fatalf("moves %v skips %v", got, r.Skips)
	}
	if !r.Moves[0].MidTurn || r.Moves[0].Window != Win5h {
		t.Fatalf("move %+v", r.Moves[0])
	}
	if skipWhy(r, "idle") != "" {
		t.Fatalf("an idle session with no burn was considered: %v", r.Skips)
	}
}

// Greedy: one move is enough when it brings P under cap−5; otherwise the next highest goes too.
func TestPlanGreedyStop(t *testing.T) {
	// a: 52 + 4 h × (10+6+4) = 132. Without 10: 92 (still > 90). Without 10 and 6: 68.
	in := Input{
		Accounts: []Account{acc("a", 52, 4*time.Hour, 10), acc("b", 0, 5*time.Hour, 10), acc("c", 0, 5*time.Hour, 10)},
		Sessions: []Session{lane("s10", "a", 10, true), lane("s6", "a", 6, true), lane("s4", "a", 4, false)},
	}
	r := plan(in)
	got := moved(r)
	if len(got) != 2 || got["s10"] == "" || got["s6"] == "" {
		t.Fatalf("moves %v", got)
	}
	if !strings.HasPrefix(skipWhy(r, "s4"), "enough moved") {
		t.Fatalf("s4: %q", skipWhy(r, "s4"))
	}
	// When one session alone brings it under, the fewest moves wins over the highest burn:
	// a at 52 + 4 h × (11+2) = 104; without 11: 60 (under 90); without 2: 96. s11 alone.
	in.Sessions = []Session{lane("s11", "a", 11, true), lane("s2", "a", 2, true)}
	if got := moved(plan(in)); len(got) != 1 || got["s11"] == "" {
		t.Fatalf("moves %v", got)
	}
}

// Hysteresis: a projection at cap−3 is over the trigger only above the cap; under the cap
// nothing moves, and a destination must stay 5 under its caps.
func TestPlanHysteresisAndDestinationRoom(t *testing.T) {
	in := Input{
		Accounts: []Account{acc("a", 60, 2*time.Hour, 30), acc("b", 80, 1*time.Hour, 30)},
		Sessions: []Session{lane("s", "a", 17, true)}, // a: 60+34 = 94 ≤ 95: no trigger
	}
	if r := plan(in); len(r.Moves) != 0 {
		t.Fatalf("moved under the cap: %+v", r.Moves)
	}
	in.Sessions[0].Rate = 20 // a: 100 > 95; b with 20: 80 + 20 = 100 > 90: no room
	r := plan(in)
	if len(r.Moves) != 1 || r.Moves[0].To != "" || !strings.Contains(r.Moves[0].Reason, "not moved") {
		t.Fatalf("moves %+v", r.Moves)
	}
	in.Accounts[1].W5.Level = 60 // b: 60 + 20 = 80 ≤ 90
	if got := moved(plan(in)); got["s"] != "b" {
		t.Fatalf("moves %v", got)
	}
}

// Rule 2: the weekly projection over its cap (98 aggressive, 80 protected) moves too; the
// 5-hour cap of a protected account is 80.
func TestPlanWeeklyAndProtected(t *testing.T) {
	in := Input{
		Accounts: []Account{acc("a", 10, 4*time.Hour, 95), acc("b", 0, 5*time.Hour, 10)},
		Sessions: []Session{lane("s", "a", 6, true)}, // P5 34; P7 95 + 6/7.2×5 = 99.2 > 98
	}
	r := plan(in)
	if len(r.Moves) != 1 || r.Moves[0].Window != Win7d || r.Moves[0].To != "b" {
		t.Fatalf("moves %+v", r.Moves)
	}
	p := acc("p", 50, 3*time.Hour, 20)
	p.Protected = true
	in.Accounts = []Account{p, acc("b", 0, 5*time.Hour, 10)}
	in.Sessions = []Session{lane("s", "p", 11, true)} // 50 + 33 = 83 > 80
	if got := moved(plan(in)); got["s"] != "b" {
		t.Fatalf("protected 5h: %v", got)
	}
	in.Sessions[0].Rate = 9 // 77: under 80
	if got := moved(plan(in)); len(got) != 0 {
		t.Fatalf("protected under its cap: %v", got)
	}
}

// Hard now: a level at its cap triggers at once, without the spike filter.
func TestPlanHardNowAndSpikeFilter(t *testing.T) {
	in := Input{Now: t0, Params: DefaultParams, Streak: map[string]int{},
		Accounts: []Account{acc("a", 70, 3*time.Hour, 30), acc("b", 0, 5*time.Hour, 10)},
		Sessions: []Session{lane("s", "a", 10, true)}} // P5 100, hit of 100 in 3 h
	r := Plan(in)
	if len(r.Moves) != 0 || r.Streak["a 5h"] != 1 || !r.Accounts[0].Held {
		t.Fatalf("first pass moved: %+v %v", r.Moves, r.Streak)
	}
	if !strings.Contains(skipWhy(r, "s"), "spike filter") {
		t.Fatalf("skip %q", skipWhy(r, "s"))
	}
	in.Streak = r.Streak
	if r := Plan(in); len(r.Moves) != 1 || r.Streak["a 5h"] != 2 {
		t.Fatalf("second pass: %+v", r.Moves)
	}
	// A projection back under resets the streak.
	in.Sessions[0].Rate = 1
	if r := Plan(in); r.Streak["a 5h"] != 0 {
		t.Fatalf("streak %v", r.Streak)
	}
	// Hit within 30 min: at once.
	in.Streak = map[string]int{}
	in.Accounts[0].W5.Level, in.Sessions[0].Rate = 90, 30 // 100 in 20 min
	if r := Plan(in); len(r.Moves) != 1 {
		t.Fatalf("near hit not at once: %+v", r.Skips)
	}
	// Hard now: 5h already at 95 with a small burn (hit in 50 min): at once.
	in.Accounts[0].W5.Level, in.Sessions[0].Rate = 95, 6
	r = Plan(in)
	if len(r.Moves) != 1 || !r.Accounts[0].Hard {
		t.Fatalf("hard now: %+v %+v", r.Moves, r.Accounts[0])
	}
	// Without the spike filter: at once.
	in.Params.SpikePasses = 0
	in.Accounts[0].W5.Level, in.Sessions[0].Rate = 70, 10
	if r := Plan(in); len(r.Moves) != 1 {
		t.Fatal("no spike filter: not moved")
	}
}

// Interactive sessions are moved like any other burner (owner correction to v2): the move
// is marked Interactive (its pane or terminal gets a message). With the optional owner grace,
// one the owner typed into within it stays and another burner moves.
func TestPlanInteractiveMoves(t *testing.T) {
	owner := lane("owner", "a", 15, true)
	owner.Interactive = true
	in := Input{
		Accounts: []Account{acc("a", 50, 4*time.Hour, 30), acc("b", 0, 5*time.Hour, 10)},
		Sessions: []Session{owner, lane("bot", "a", 8, true)}, // 50 + 4×23 = 142
	}
	r := plan(in)
	if got := moved(r); len(got) != 1 || got["owner"] != "b" {
		t.Fatalf("moves %v", got)
	}
	if !r.Moves[0].Interactive || !r.Moves[0].MidTurn {
		t.Errorf("move %+v: want interactive, mid-turn", r.Moves[0])
	}
	// Owner grace (off by default): typed 1 min ago, grace 2 min: the bot moves instead.
	in.Sessions[0].Typed = t0.Add(-time.Minute)
	if got := moved(plan(in)); got["owner"] != "b" {
		t.Fatalf("grace off by default: %v", got)
	}
	in.Params = DefaultParams
	in.Params.SpikePasses, in.Params.OwnerGrace = 0, 2*time.Minute
	r = plan(in)
	if got := moved(r); len(got) != 1 || got["bot"] != "b" {
		t.Fatalf("owner grace: moves %v", got)
	}
	if !hasSkip(r, "owner", "owner grace") {
		t.Errorf("skips %+v", r.Skips)
	}
	in.Sessions[0].Typed = t0.Add(-3 * time.Minute)
	if got := moved(plan(in)); got["owner"] != "b" {
		t.Fatalf("grace over: %v", got)
	}
	// A stopped interactive session moves too.
	owner.Stopped, owner.Rate = true, 0
	in.Sessions = []Session{owner}
	in.Params = Params{}
	in.Accounts[0].W5.Level = 100
	r = plan(in)
	if len(r.Moves) != 1 || !r.Moves[0].Stopped || !r.Moves[0].Interactive {
		t.Fatalf("stopped interactive: moves %+v", r.Moves)
	}
}

// Only accounts logged in on this machine are destinations.
func TestPlanLoggedInOnly(t *testing.T) {
	b := acc("b", 0, 5*time.Hour, 10)
	b.LoggedIn = false
	in := Input{Accounts: []Account{acc("a", 80, 2*time.Hour, 30), b}, Sessions: []Session{lane("s", "a", 20, true)}}
	r := plan(in)
	if len(r.Moves) != 1 || r.Moves[0].To != "" || !strings.Contains(r.Moves[0].Reason, "logged-in") {
		t.Fatalf("moves %+v", r.Moves)
	}
	in.Accounts = append(in.Accounts, acc("c", 0, 2*time.Hour, 10))
	if got := moved(plan(in)); got["s"] != "c" {
		t.Fatalf("moves %v", got)
	}
	if to, _ := Destination(Input{Now: t0, Params: DefaultParams, Accounts: []Account{acc("a", 0, time.Hour, 0), b},
		Sessions: []Session{lane("s", "a", 1, false)}}, "s"); to != "" {
		t.Fatalf("by hand to a not logged-in account: %q", to)
	}
}

// account.move_orgs: any (the picker's order), same-first, same.
func TestPlanOrgs(t *testing.T) {
	in := Input{
		Accounts: []Account{acc("acct:a/o1", 80, 2*time.Hour, 30), acc("acct:x/o2", 0, 1*time.Hour, 10), acc("acct:c/o1", 0, 3*time.Hour, 10)},
		Sessions: []Session{lane("s", "acct:a/o1", 20, true)},
	}
	r := plan(in)
	if got := moved(r); got["s"] != "acct:x/o2" || !r.Moves[0].OrgChanged {
		t.Fatalf("any: %v", got) // the soonest 5-hour reset first, any organisation
	}
	in.Params = DefaultParams
	in.Params.Orgs = OrgsSameFirst
	if got := moved(plan(in)); got["s"] != "acct:c/o1" {
		t.Fatalf("same-first: %v", got)
	}
	in.Accounts[2].W5.Level = 90
	if got := moved(plan(in)); got["s"] != "acct:x/o2" {
		t.Fatalf("same-first, own full: %v", got)
	}
	in.Params.Orgs = OrgsSame
	r = plan(in)
	if got := moved(r); got["s"] != "" || !strings.Contains(r.Moves[0].Reason, "organisation") {
		t.Fatalf("same: %v %q", got, r.Moves[0].Reason)
	}
}

// Destination order: aggressive before protected (and protected only within 80/80), draining
// first, then the soonest 5-hour reset, then the most room.
func TestPlanDestinationOrder(t *testing.T) {
	p := acc("p", 0, 1*time.Hour, 10)
	p.Protected = true
	in := Input{
		Accounts: []Account{p, acc("a", 90, 2*time.Hour, 30), acc("far", 20, 4*time.Hour, 10), acc("soon", 40, 1*time.Hour, 10), acc("new", 0, 0, 10)},
		Sessions: []Session{lane("s", "a", 10, true)},
	}
	if got := moved(plan(in)); got["s"] != "soon" {
		t.Fatalf("soonest reset: %v", got)
	}
	in.Accounts[2].Drain = true
	if got := moved(plan(in)); got["s"] != "far" {
		t.Fatalf("drain first: %v", got)
	}
	// Only the protected one has room: it takes it within 75 (80 − 5).
	in.Accounts = []Account{p, acc("a", 90, 2*time.Hour, 30)}
	if got := moved(plan(in)); got["s"] != "p" {
		t.Fatalf("protected last: %v", got)
	}
	in.Sessions[0].Rate = 80 // p: 0 + 80 × 1 h = 80 > 75
	if got := moved(plan(in)); got["s"] != "" {
		t.Fatalf("protected over 75: %v", got)
	}
}

// Cooldown 20 min per session; no return to the account it left within 60 min unless its
// window reset since; at most 5 moves into one account per 15 min (soft for an aggressive
// account with room); no once-per-window limit.
func TestPlanAntiChurn(t *testing.T) {
	in := Input{
		Accounts: []Account{acc("a", 80, 2*time.Hour, 30), acc("b", 0, 1*time.Hour, 10), acc("c", 0, 4*time.Hour, 10)},
		Sessions: []Session{lane("s", "a", 10, true)}, // a: 80 + 2 h × 10 = 100 > 95
		Recent:   []Record{{Session: "s", From: "x", To: "a", At: t0.Add(-10 * time.Minute)}},
	}
	r := plan(in)
	if len(r.Moves) != 0 || !strings.Contains(skipWhy(r, "s"), "cooldown") {
		t.Fatalf("cooldown: %+v %v", r.Moves, r.Skips)
	}
	in.Recent[0].At = t0.Add(-25 * time.Minute) // past the cooldown: moves again (no once-per-window)
	in.Recent[0].From = "b"                     // but not back to b within 60 min
	if got := moved(plan(in)); got["s"] != "c" {
		t.Fatalf("no return: %v", got)
	}
	// b's window reset since the move: it started 10 min ago, after the move 25 min ago.
	in.Accounts[1].W5.Reset = t0.Add(4*time.Hour + 50*time.Minute)
	in.Accounts[2].W5.Reset = t0.Add(4*time.Hour + 55*time.Minute)
	if got := moved(plan(in)); got["s"] != "b" {
		t.Fatalf("return after a reset: %v", got)
	}
	// Account rate limit: 5 moves into c within 15 min send the next one to d, another
	// aggressive account with room.
	in.Accounts = []Account{acc("a", 80, 2*time.Hour, 30), acc("c", 0, 4*time.Hour, 10), acc("d", 0, 4*time.Hour, 10)}
	in.Recent = nil
	for i := range 5 {
		in.Recent = append(in.Recent, Record{Session: "o" + string(rune('0'+i)), From: "z", To: "c", At: t0.Add(-5 * time.Minute)})
	}
	if got := moved(plan(in)); got["s"] != "d" {
		t.Fatalf("rate limit: %v", got)
	}
	in.Recent = in.Recent[:4]
	if got := moved(plan(in)); got["s"] != "c" {
		t.Fatalf("under the rate limit: %v", got)
	}
	// A protected account is the only other: the limited aggressive one with room takes it.
	in.Recent = append(in.Recent, Record{Session: "o9", From: "z", To: "c", At: t0.Add(-5 * time.Minute)})
	in.Accounts[2].Protected = true
	if got := moved(plan(in)); got["s"] != "c" {
		t.Fatalf("aggressive over its limit before protected: %v", got)
	}
}

// A stopped session moves when its reset is 20 min or more away, else it waits; pinned,
// out of scope and claude.ai sessions never move.
func TestPlanStoppedPinnedCloud(t *testing.T) {
	st := lane("st", "a", 0, false)
	st.Stopped = true
	pin := lane("pin", "a", 30, true)
	pin.Pinned = true
	cloud := lane("cloud", "a", 30, true)
	cloud.Movable = false
	in := Input{
		Accounts: []Account{acc("a", 100, 30*time.Minute, 30), acc("b", 0, 5*time.Hour, 10)},
		Sessions: []Session{st, pin, cloud},
	}
	r := plan(in)
	if got := moved(r); len(got) != 1 || got["st"] != "b" || !r.Moves[0].Stopped {
		t.Fatalf("moves %v %+v", got, r.Moves)
	}
	if !strings.Contains(skipWhy(r, "pin"), "pinned") || !strings.Contains(skipWhy(r, "cloud"), "claude.ai") {
		t.Fatalf("skips %+v", r.Skips)
	}
	in.Accounts[0].W5.Reset = t0.Add(15 * time.Minute)
	r = plan(in)
	if got := moved(r); got["st"] != "" || !strings.Contains(skipWhy(r, "st"), "waiting") {
		t.Fatalf("near reset: %v %v", got, r.Skips)
	}
}

// The idle-moves variant (simulator only) also moves sessions with no burn.
func TestPlanIdleVariant(t *testing.T) {
	in := Input{
		Accounts: []Account{acc("a", 97, 2*time.Hour, 30), acc("b", 0, 5*time.Hour, 10)},
		Sessions: []Session{lane("idle", "a", 0, false)},
	}
	if got := moved(plan(in)); len(got) != 0 {
		t.Fatalf("idle moved: %v", got)
	}
	in.Params = DefaultParams
	in.Params.IdleMoves = true
	if got := moved(plan(in)); got["idle"] != "b" {
		t.Fatalf("idle variant: %v", got)
	}
}

// Moves planned in one pass add up on the destination.
func TestPlanDestinationAddsPlanned(t *testing.T) {
	in := Input{
		Accounts: []Account{acc("a", 90, 3*time.Hour, 30), acc("b", 0, 4*time.Hour, 10), acc("c", 0, 5*time.Hour, 10)},
		Sessions: []Session{lane("s1", "a", 15, true), lane("s2", "a", 15, true)}, // b: 4 h × 15 = 60; both: 120
	}
	in.Params = DefaultParams
	in.Params.DestHorizon = 0 // room to the resets
	got := moved(plan(in))
	if got["s1"] == got["s2"] || got["s1"] == "" || got["s2"] == "" {
		t.Fatalf("moves %v", got)
	}
}

// A destination is judged with the trigger's own projection (to its resets, Hysteresis
// under the caps), the moved session until its expected end as the trigger counts it there;
// a protected one only when no aggressive one fits. The old 90-minute horizon (oldDest, the
// simulator's comparison) let in moves the next trigger undid.
func TestPlanDestinationAsTrigger(t *testing.T) {
	// b resets in 4 h: 26.5 points/h to the reset is 106, over 90; for 90 minutes it is 40.
	mk := func() Input {
		in := Input{Accounts: []Account{acc("a", 90, 3*time.Hour, 30), acc("b", 0, 4*time.Hour, 10), prot("p", 0, 30*time.Minute, 10)},
			Sessions: []Session{lane("s", "a", 26.5, true)}}
		in.Params = DefaultParams
		return in
	}
	if got := moved(plan(mk())); got["s"] != "p" {
		t.Fatalf("room to the resets: %v", got)
	}
	in := mk()
	in.Params.DestHorizon, in.Params.oldDest = 90*time.Minute, true
	if got := moved(plan(in)); got["s"] != "b" {
		t.Fatalf("old horizon: %v", got)
	}
	// Expected to end in an hour: the trigger counts its burn on b for that hour (26.5).
	in = mk()
	in.Sessions[0].Expected = time.Hour
	if got := moved(plan(in)); got["s"] != "b" {
		t.Fatalf("short session: %v", got)
	}
	// Expected to run 4 more hours: to b's reset, over; the protected one takes it.
	in = mk()
	in.Sessions[0].Expected = 4 * time.Hour
	if got := moved(plan(in)); got["s"] != "p" {
		t.Fatalf("long session: %v", got)
	}
}

// Live (pre7 1082fe05): 3b2611ed (21 points/h) moved claude-2 -> claude-6 at 06:12 and
// claude-6 -> claude-4 at 06:35 ("5h projected 111% (cap 85%)"). At 06:12 claude-6 was at 39%
// with 2 points/h of its own, ai-lane-C2 (4.1) moved in at 06:10 (young by its new lease, so
// its burn was left out), its 5-hour reset 2.5 h away, cap 85 (95 less a pin's 10).
func TestPlanNoMoveTheTriggerUndoes(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 42, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	c2 := Account{Key: "acct:c2/oA", Label: "claude-2", Rate: 30, Meter: 30, LoggedIn: true, Ratio: 8, ReadAt: now,
		W5: Window{Level: 70, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 21, Reset: now.Add(100 * time.Hour)}}
	c6 := Account{Key: "acct:c6/oB", Label: "claude-6", Rate: 2 + 4.1, Meter: 2 + 4.1, LoggedIn: true, Ratio: 8, ReadAt: now, Pins: 1, Reserve5: 10, Reserve7: 5,
		W5: Window{Level: 39, Reset: now.Add(150 * time.Minute)}, W7: Window{Level: 39, Reset: now.Add(100 * time.Hour)}}
	ss := []Session{
		{ID: "3b2611ed", Account: c2.Key, Rate: 21, Busy: true, Movable: true, Context: 80_000, Started: old},
		{ID: "other", Account: c2.Key, Rate: 9, Busy: true, Movable: true, Context: 80_000, Started: old},
		{ID: "c2-lane", Account: c6.Key, Rate: 4.1, Busy: true, Movable: true, Context: 80_000, Started: now.Add(-2 * time.Minute)},
		{ID: "own", Account: c6.Key, Rate: 2, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
	}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{c2, c6}, Sessions: ss,
		Streak: map[string]int{c2.Key + " " + Win5h: 5},
		Recent: []Record{{Session: "c2-lane", From: "acct:c5/oA", To: c6.Key, At: now.Add(-2 * time.Minute), Rate: 4.1, RateKnown: true}}}
	r := Plan(in)
	if got := moved(r); got["3b2611ed"] == c6.Key {
		t.Fatalf("3b2611ed moved onto claude-6 (39 + (6.1 + 21) x 2.5 = 107 > 80): %+v", r.Moves)
	}
	// The old rule took it: C2's burn left out (young) and 3b2611ed for 90 minutes only,
	// 39 + 2 x 2.5 + 21 x 1.5 = 75.5 <= 80.
	in.Params.DestHorizon, in.Params.oldDest = 90*time.Minute, true
	in.Recent = nil
	if got := moved(Plan(in)); got["3b2611ed"] != c6.Key {
		t.Fatalf("old rule: %v", got)
	}
	// What the trigger then saw on claude-6 (06:35, C2 and 3b2611ed counted): over its cap.
	in.Params = DefaultParams
	pr := in.Params.Project(c6, []Session{ss[2], ss[3], {ID: "3b2611ed", Account: c6.Key, Rate: 20.1, Started: old}}, 0, now.Add(23*time.Minute))
	if pr.Window != Win5h {
		t.Fatalf("trigger at 06:35: %+v", pr)
	}
}

func TestNoteText(t *testing.T) {
	at := time.Date(2026, 11, 24, 14, 5, 0, 0, time.Local)
	mv := Move{Window: Win5h, P: 104, HitAt: time.Date(2026, 11, 24, 14, 40, 0, 0, time.Local), MidTurn: true}
	want := "Watchyard moved this session from claude-3 to claude-2 mid-turn at 14:05: claude-3's 5h would hit 100% at 14:40 at its" +
		" current burn (P5 104%). Your last step may not have finished: check its result before redoing it. Helper agents that were" +
		" running were stopped: restart them if you still need them. Background shells keep running (listed as stopped; check their" +
		" output files). Carry on with your task."
	if got := Note("claude-3", "claude-2", at, mv); got != want {
		t.Errorf("note:\n%s\nwant\n%s", got, want)
	}
	mv.MidTurn, mv.OrgChanged = false, true
	got := Note("claude-3", "claude-6", at, mv, "claude-4", "claude-3")
	if !strings.Contains(got, "(move 2: claude-4 → claude-3 → claude-6)") || !strings.Contains(got, "different organisation") ||
		strings.Contains(got, "mid-turn") || !strings.Contains(got, "no longer tracked") {
		t.Errorf("idle cross-org note: %s", got)
	}
	st := Note("claude-3", "claude-2", at, Move{Window: Win5h, Stopped: true, ResetAt: at.Add(time.Hour)})
	if !strings.Contains(st, "(stopped at the 5h limit, which resets at 15:05)") {
		t.Errorf("stopped note: %s", st)
	}
	// By hand: the owner's call, no projection claim even when the source has one; a note all
	// the same for the hook (IsNote).
	got = Note("claude-4", "claude-2", at, Move{ByHand: true, Window: Win5h, P: 181, HitAt: at.Add(time.Hour)}, "claude-6", "claude-4")
	if !strings.HasPrefix(got, "This session was moved by hand by the owner from claude-4 to claude-2 at 14:05 (move 2: claude-6 → claude-4 → claude-2).") ||
		strings.Contains(got, "would") || strings.Contains(got, "P5") || !IsNote(got) || !IsNote(Note("a", "b", at, Move{})) || IsNote("fix the build") {
		t.Errorf("by hand: %s", got)
	}
}

func hasSkip(r Result, id, why string) bool {
	for _, k := range r.Skips {
		if k.Session == id && strings.Contains(k.Why, why) {
			return true
		}
	}
	return false
}

// prot is a protected acc.
func prot(key string, l5 float64, reset5 time.Duration, l7 float64) Account {
	a := acc(key, l5, reset5, l7)
	a.Protected = true
	return a
}

// Params.CheapMoves: of the sessions whose move alone fixes the overload, the most points
// freed per re-read (context × model factor) goes first, then the burn, then the youngest.
func TestPlanCheapMoves(t *testing.T) {
	mk := func() Input {
		big := lane("big", "a", 20, true)
		big.Context, big.ModelFactor = 400_000, ModelFactor("claude-opus-4-8")
		small := lane("small", "a", 15, true)
		small.Context, small.ModelFactor = 50_000, ModelFactor("sonnet")
		return Input{Accounts: []Account{acc("a", 40, 3*time.Hour, 30), acc("b", 0, 4*time.Hour, 10), acc("c", 0, 4*time.Hour, 10)},
			Sessions: []Session{big, small}}
	}
	// a: 20 + 3 h × 35 = 125 > 95; without big 65, without small 80: either alone fixes it.
	in := mk()
	in.Accounts[0].W5.Level = 20
	in.Params = DefaultParams
	r := plan(in)
	if len(r.Moves) == 0 || r.Moves[0].Session != "small" {
		t.Fatalf("cheap first: %+v", r.Moves)
	}
	in = mk()
	in.Accounts[0].W5.Level = 20
	in.Params = DefaultParams
	in.Params.CheapMoves = false
	if r := plan(in); len(r.Moves) == 0 || r.Moves[0].Session != "big" {
		t.Fatalf("highest burn first: %+v", r.Moves)
	}
	// Same cost and burn: the youngest first.
	in = mk()
	in.Accounts[0].W5.Level = 20
	in.Params = DefaultParams
	in.Sessions[0].Rate, in.Sessions[0].Context, in.Sessions[0].ModelFactor = 15, 50_000, 3
	in.Sessions[0].Started, in.Sessions[1].Started = t0.Add(-20*time.Minute), t0.Add(-3*time.Hour)
	if r := plan(in); len(r.Moves) == 0 || r.Moves[0].Session != "big" {
		t.Fatalf("youngest first: %+v", r.Moves)
	}
	if ModelFactor("claude-haiku-4-5") != 1 || ModelFactor("x") != 0 {
		t.Error("model factors")
	}
}

// Live (pre9, 10:00:58): ai-lane-E (27.1 points/h) moved claude-6 → claude-2, accepted: claude-2
// at 6%, 0.1 points/h counted, its 5-hour reset ~2.5 h away, cap 85; its three fresh lanes'
// 8.7 points/h were left out (under 15 minutes): 6 + 27.2 × 2.5 = 74 <= 80. By 10:10 they
// burned 60-90 points/h and at 10:11 the trigger fired ("5h projected 95% (cap 85%)"). The
// destination counts young lanes at the lane burn (else their measured burn).
func TestPlanDestinationCountsYoungLanes(t *testing.T) {
	now := time.Date(2026, 11, 25, 4, 30, 58, 0, time.UTC)
	old := now.Add(-3 * time.Hour)
	c6 := Account{Key: "acct:c6/oB", Label: "claude-6", Rate: 30.5, Meter: 30.5, LoggedIn: true, Ratio: 8, ReadAt: now,
		W5: Window{Level: 9, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 44, Reset: now.Add(100 * time.Hour)}}
	c2 := Account{Key: "acct:c2/oA", Label: "claude-2", Rate: 0.1 + 8.7, Meter: 0.1 + 8.7, LoggedIn: true, Ratio: 8, ReadAt: now,
		W5: Window{Level: 6, Reset: now.Add(150 * time.Minute)}, W7: Window{Level: 24, Reset: now.Add(140 * time.Hour)}}
	ss := []Session{
		{ID: "ai-lane-E", Account: c6.Key, Rate: 27.1, Busy: true, Movable: true, Context: 80_000, Started: old},
		{ID: "c6-own", Account: c6.Key, Rate: 3.4, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
		{ID: "home-18", Account: c2.Key, Rate: 0.1, Busy: true, Movable: true, Context: 30_000, Started: old},
	}
	for i, r := range []float64{2.1, 3.0, 3.6} {
		ss = append(ss, Session{ID: fmt.Sprintf("fresh-%d", i), Account: c2.Key, Rate: r, Busy: true, Movable: true,
			Context: 20_000, Started: now.Add(-time.Duration(3+i) * time.Minute)})
	}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{c2, c6}, Sessions: ss,
		Streak: map[string]int{c6.Key + " " + Win5h: 5}}
	if got := moved(Plan(in)); got["ai-lane-E"] == c2.Key {
		t.Fatalf("ai-lane-E moved onto claude-2 with its young lanes at their measured 8.7: %v", got)
	}
	in.LaneRate = 12
	if got := moved(Plan(in)); got["ai-lane-E"] == c2.Key {
		t.Fatalf("ai-lane-E moved onto claude-2 with its young lanes at 3 × 12: %v", got)
	}
	// Without the young lanes (all older): 6 + (8.8 + 27.1) × 2.5 = 96, also refused; with
	// them at 0 (the pre9 rule) it was 6 + 27.2 × 2.5 = 74: accepted.
	in.LaneRate = 0
	in.Params.oldDest = true // the young lanes counted at 0, as pre9 (the horizon off)
	if got := moved(Plan(in)); got["ai-lane-E"] != c2.Key {
		t.Fatalf("pre9 rule: %v", got)
	}
}

// Live (pre9, worker 10:31): claude-4 at 78% 5h, burning ~44 points/h (ai-lane-E-r1 25.9,
// ai-lane-D 16.9, a3fc9098 2.7), projected 100% at ~10:54. The switch moved only the 2.7: the
// 25.9 was held back by claude-6's account rate limit, the 16.9 found "no logged-in account
// on this machine has room for its burn", while claude-6 was at 16% and claude-2 at 34%. A
// near hit lifts the rate limit, moves the highest burner first, and takes the account that
// stays under its caps longest when none has room to its reset.
func TestPlanNearHitMovesTheBurners(t *testing.T) {
	now := time.Date(2026, 11, 25, 5, 1, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	for _, c := range []struct {
		name  string
		level float64 // claude-4's 5-hour level: the hit in (100 − level) / 45.5 h
	}{{"78% (hit in 29 min)", 78}, {"69.7% (hit in 40 min)", 69.7}} {
		t.Run(c.name, func(t *testing.T) {
			c4 := Account{Key: "acct:c4/oA", Label: "claude-4", Rate: 45.5, Meter: 45.5, LoggedIn: true, Ratio: 8, ReadAt: now,
				W5: Window{Level: c.level, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 48, Reset: now.Add(100 * time.Hour)}}
			c6 := Account{Key: "acct:c6/oB", Label: "claude-6", Rate: 4.7, Meter: 4.7, LoggedIn: true, Ratio: 8, ReadAt: now, Pins: 1, Reserve5: 10, Reserve7: 5,
				W5: Window{Level: 16, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 43, Reset: now.Add(100 * time.Hour)}}
			c2 := Account{Key: "acct:c2/oA", Label: "claude-2", Rate: 1.5, Meter: 1.5, LoggedIn: true, Ratio: 8, ReadAt: now, Pins: 1, Reserve5: 10, Reserve7: 5,
				W5: Window{Level: 34, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 25, Reset: now.Add(140 * time.Hour)}}
			ss := []Session{
				{ID: "a3fc9098", Account: c4.Key, Rate: 2.7, Busy: true, Movable: true, Context: 30_000, Started: old},
				{ID: "04bd105f", Account: c4.Key, Rate: 16.9, Busy: true, Movable: true, Context: 120_000, Started: old},
				{ID: "54c4e1e8", Account: c4.Key, Rate: 25.9, Busy: true, Movable: true, Context: 150_000, Started: old},
				{ID: "c6-own", Account: c6.Key, Rate: 4.7, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
				{ID: "c2-own", Account: c2.Key, Rate: 1.5, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
			}
			// claude-6 took 20 points/h of moves 10 minutes ago: its account rate limit (40 per
			// 15 min) would hold 25.9 back.
			in := Input{Now: now, Params: DefaultParams, Accounts: []Account{c6, c2, c4}, Sessions: ss,
				Streak: map[string]int{c4.Key + " " + Win5h: 5},
				Recent: []Record{{Session: "earlier", From: c4.Key, To: c6.Key, At: now.Add(-10 * time.Minute), Rate: 20, RateKnown: true}}}
			r := Plan(in)
			got := moved(r)
			if got["54c4e1e8"] == "" || got["04bd105f"] == "" {
				t.Fatalf("the burners stayed: %v\n%+v", got, r.Moves)
			}
			if got["a3fc9098"] != "" {
				t.Errorf("the 2.7 points/h session moved too: %v", got)
			}
			for _, mv := range r.Moves {
				if mv.Session == "54c4e1e8" && (!mv.Urgent || !strings.Contains(mv.Reason, "best available: claude-")) {
					t.Errorf("54c4e1e8: %+v", mv)
				}
			}
			for _, mv := range r.Moves {
				t.Logf("%s -> %s: %s", mv.Session, mv.To, mv.Reason)
			}
			if r.Moves[0].Session != "54c4e1e8" {
				t.Errorf("first move %s, want the highest burner", r.Moves[0].Session)
			}
		})
	}
	// Not near (a soft trigger, 100% only in 2 h): no best-available move.
	c4 := Account{Key: "acct:c4/oA", Label: "claude-4", Rate: 20, Meter: 20, LoggedIn: true, Ratio: 8, ReadAt: now,
		W5: Window{Level: 50, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 48, Reset: now.Add(100 * time.Hour)}}
	c6 := Account{Key: "acct:c6/oB", Label: "claude-6", Rate: 25, Meter: 25, LoggedIn: true, Ratio: 8, ReadAt: now,
		W5: Window{Level: 16, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 43, Reset: now.Add(100 * time.Hour)}}
	in := Input{Now: now, Params: DefaultParams, Accounts: []Account{c6, c4},
		Sessions: []Session{{ID: "big", Account: c4.Key, Rate: 20, Busy: true, Movable: true, Context: 100_000, Started: old},
			{ID: "c6", Account: c6.Key, Rate: 25, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old}},
		Streak: map[string]int{c4.Key + " " + Win5h: 5}}
	if got := moved(Plan(in)); got["big"] != "" {
		t.Fatalf("soft trigger took a best-available destination: %v", got)
	}
}

// A moved session is not a new one: its burn counts at once on its new account and it is a
// candidate there (the cooldown still holds it); the small burner left does not move on a soft
// trigger while the held one burns more (live pre9 10:47: 54c4e1e8 ai-lane-E-r1, moved onto
// claude-6 at 10:35 at 21.9 points/h, "stays on claude-6: under 15 min old" while claude-6 was
// at 22%, P5 93% of its cap 85; home-00 at 1.7 points/h was moved off it at 10:37).
func TestPlanMovedSessionIsNotNew(t *testing.T) {
	now := time.Date(2026, 11, 25, 5, 17, 0, 0, time.UTC)
	old := now.Add(-3 * time.Hour)
	// claude-6 has a pinned session: its caps are 85/93.
	c6 := Account{Key: "acct:c6/oB", Label: "claude-6", LoggedIn: true, Ratio: 8, ReadAt: now, Pins: 1, Reserve5: 10, Reserve7: 5,
		W5: Window{Level: 22, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 45, Reset: now.Add(100 * time.Hour)}}
	c2 := Account{Key: "acct:c2/oA", Label: "claude-2", Rate: 1.5, Meter: 1.5, LoggedIn: true, Ratio: 8, ReadAt: now,
		W5: Window{Level: 34, Reset: now.Add(90 * time.Minute)}, W7: Window{Level: 25, Reset: now.Add(140 * time.Hour)}}
	c4 := Account{Key: "acct:c4/oA", Label: "claude-4", Rate: 3, Meter: 3, LoggedIn: true, Ratio: 8, ReadAt: now,
		W5: Window{Level: 88, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 48, Reset: now.Add(100 * time.Hour)}}
	for _, c := range []struct {
		name    string
		movedAt time.Time
		started time.Time // what the session's lease says: the move's (old code) or its first lease
		rate    float64   // its measured burn (with the re-read while the move is recent)
		want    string    // 54c4e1e8's skip ("" : it moves)
	}{
		{"12 min after the move, age from the move lease", now.Add(-12 * time.Minute), now.Add(-12 * time.Minute), 28.3, "cooldown"},
		{"12 min after the move, its real age", now.Add(-12 * time.Minute), old, 28.3, "cooldown"},
		{"after the cooldown", now.Add(-25 * time.Minute), old, 21.9, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			ss := []Session{
				// Its measured burn since the resume holds the re-read (30 points/h); it counts at
				// the 21.9 it moved with while the move is under YoungGate.
				{ID: "54c4e1e8", Account: c6.Key, Rate: c.rate, Busy: true, Movable: true, Context: 150_000, Started: c.started},
				{ID: "home-00", Account: c6.Key, Rate: 1.7, Busy: true, Movable: true, Context: 40_000, Started: old},
			}
			a6 := c6
			a6.Rate, a6.Meter = c.rate+1.7, c.rate+1.7 // 22 + 23.6 × 3 h ≈ 93 at the burn it moved with
			in := Input{Now: now, Params: DefaultParams, Accounts: []Account{a6, c2, c4}, Sessions: ss,
				Streak: map[string]int{c6.Key + " " + Win5h: 5},
				Recent: []Record{{Session: "54c4e1e8", From: c4.Key, To: c6.Key, At: c.movedAt, Rate: 21.9, RateKnown: true}}}
			r := Plan(in)
			got := moved(r)
			for _, mv := range r.Moves {
				t.Logf("%s -> %s: %s", mv.Session, mv.To, mv.Reason)
			}
			t.Logf("54c4e1e8: %s; home-00: %s", skipWhy(r, "54c4e1e8"), skipWhy(r, "home-00"))
			if strings.Contains(skipWhy(r, "54c4e1e8"), "min old") {
				t.Fatalf("a moved session is taken as new: %s", skipWhy(r, "54c4e1e8"))
			}
			if c.want == "" {
				if got["54c4e1e8"] == "" {
					t.Fatalf("54c4e1e8 stayed: %s", skipWhy(r, "54c4e1e8"))
				}
				if got["home-00"] != "" {
					t.Errorf("home-00 moved too: %v", got)
				}
				return
			}
			if !strings.Contains(skipWhy(r, "54c4e1e8"), c.want) {
				t.Errorf("54c4e1e8: %q, want %q", skipWhy(r, "54c4e1e8"), c.want)
			}
			if got["home-00"] != "" || !strings.Contains(skipWhy(r, "home-00"), "waiting") {
				t.Errorf("home-00 at 1.7 points/h moved while 21.9 cooled down: %v %q", got, skipWhy(r, "home-00"))
			}
		})
	}
}

// The re-check after a step wait (setup.md 8.6, Re-check), from the live pre10 passes:
// at 11:57:35 claude-2 (85% cap, P5 86.3) planned 441d209b (7.6 points/h) onto protected
// claude-7: claude-4 projected 59.44 + 7.6 × 4.04 h = 90.2 of its 90, claude-6 was held by a
// 4-minute-old lane counted at the new-lane burn. The move ran at 11:58:41 after its step
// wait, when claude-4 burned 9.6 points/h, not 11.3: re-chosen, claude-4 takes it.
func TestRecheckAfterStepWait(t *testing.T) {
	t1 := time.Date(2026, 11, 25, 6, 27, 35, 0, time.UTC)
	old := t1.Add(-3 * time.Hour)
	c2k, c4k, c6k, c7k := "acct:c2/oA", "acct:c4/oA", "acct:c6/oB", "acct:c7/oB"
	world := func(now time.Time, c4rate, c2rate54 float64) Input {
		c2 := Account{Key: c2k, Label: "claude-2", LoggedIn: true, Ratio: 8, ReadAt: now, Pins: 1, Reserve5: 10, Reserve7: 5,
			W5: Window{Level: 69, Reset: t1.Add(32*time.Minute + 25*time.Second)}, W7: Window{Level: 31, Reset: t1.Add(140 * time.Hour)}}
		c2.Rate = c2rate54 + 7.6 + 1.07
		c2.Meter = c2.Rate
		c4 := Account{Key: c4k, Label: "claude-4", LoggedIn: true, Ratio: 8.57, ReadAt: now, Rate: c4rate, Meter: c4rate,
			W5: Window{Level: 13.67, Reset: t1.Add(4*time.Hour + 2*time.Minute + 35*time.Second)}, W7: Window{Level: 53.2, Reset: t1.Add(91 * time.Hour)}}
		c6 := Account{Key: c6k, Label: "claude-6", LoggedIn: true, Ratio: 8, ReadAt: now, Rate: 10.66, Meter: 10.66, Pins: 1, Reserve5: 10, Reserve7: 5,
			W5: Window{Level: 48.4, Reset: t1.Add(112*time.Minute + 30*time.Second)}, W7: Window{Level: 47, Reset: t1.Add(102 * time.Hour)}}
		c7 := Account{Key: c7k, Label: "claude-7", Protected: true, LoggedIn: true, Ratio: 8, ReadAt: now, Pins: 2, Reserve5: 20, Reserve7: 10,
			W5: Window{Level: 12, Reset: t1.Add(132 * time.Minute)}, W7: Window{Level: 32, Reset: t1.Add(50 * time.Hour)}}
		ss := []Session{
			{ID: "54c4e1e8", Account: c2k, Rate: c2rate54, Busy: true, Movable: true, Context: 150_000, Started: old},
			{ID: "441d209b", Account: c2k, Rate: 7.6, Busy: true, Movable: true, Context: 80_000, Started: old},
			{ID: "40f6a21a", Account: c2k, Rate: 1.07, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
			{ID: "0d2d67fd", Account: c6k, Rate: 3.1, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
			{ID: "ea99cb89", Account: c6k, Rate: 3.25, Busy: true, Movable: true, Started: old},
			{ID: "1ef81ac8", Account: c6k, Rate: 4.1, Busy: true, Movable: true, Started: t1.Add(-163 * time.Second)},
			{ID: "3b2611ed", Account: c4k, Rate: c4rate, Busy: true, Movable: true, Started: old},
		}
		return Input{Now: now, Params: DefaultParams, Accounts: []Account{c6, c4, c2, c7}, Sessions: ss, LaneRate: 12,
			Streak: map[string]int{c2k + " " + Win5h: 2},
			Recent: []Record{{Session: "54c4e1e8", From: c6k, To: c2k, At: t1.Add(-25*time.Minute - 34*time.Second), Rate: 19.2, RateKnown: true}}}
	}
	r := Plan(world(t1, 11.32, 23.3))
	var planned Move
	for _, m := range r.Moves {
		t.Logf("11:57:35 %s -> %s: %s", m.Session, m.To, m.Reason)
		if m.Session == "441d209b" {
			planned = m
		}
	}
	if planned.To != c7k {
		t.Fatalf("the 11:57:35 plan: 441d209b to %q, want claude-7 (the case as it happened)", planned.To)
	}
	planned.MidTurn = true
	t2 := t1.Add(66 * time.Second)
	for _, c := range []struct {
		name          string
		at            time.Time
		c4rate, rate5 float64
		want          string // "" dropped
		changed       bool
	}{
		{"claude-4 has room now", t2, 9.6, 23.3, c4k, true},
		// (66 s on, at 11.3 points/h, claude-4's projection is 89.8: under its 90 too.)
		{"nothing changed", t1, 11.32, 23.3, c7k, false},
		{"the source no longer needs it", t2, 11.32, 12, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, why, ok := Recheck(world(c.at, c.c4rate, c.rate5), planned)
			t.Logf("%v %s: %q", ok, got.To, why)
			if c.want == "" {
				if ok || !strings.Contains(why, "re-checked after its step wait") {
					t.Fatalf("kept: %+v %q", got, why)
				}
				return
			}
			if !ok || got.To != c.want {
				t.Fatalf("to %q (ok %v, %q), want %q", got.To, ok, why, c.want)
			}
			if (got.Recheck != "") != c.changed || (c.changed && !strings.Contains(got.Recheck, "claude-4, not claude-7 as planned")) {
				t.Errorf("recheck note %q", got.Recheck)
			}
			if !got.MidTurn {
				t.Error("lost its mid-turn mark")
			}
		})
	}
}

// setup.md 8.6, Owner drain (`wy account drain claude-4`, live 2026-11-25): while its 5h
// projection has room claude-4 pulls the biggest busy burners in (not pinned, not interactive,
// out of cooldown); nothing moves off it before a 5h hit within 60 min or its cap.
func TestPlanOwnerDrain(t *testing.T) {
	now := time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	until := now.Add(11 * time.Hour)
	c4k, c2k, c6k := "acct:c4/oA", "acct:c2/oA", "acct:c6/oB"
	world := func(c4level, c4rate float64, reset5 time.Duration, until time.Time) Input {
		c4 := Account{Key: c4k, Label: "claude-4", LoggedIn: true, Ratio: 8, ReadAt: now, Rate: c4rate, Meter: c4rate, DrainUntil: until,
			W5: Window{Level: c4level, Reset: now.Add(reset5)}, W7: Window{Level: 53, Reset: now.Add(91 * time.Hour)}}
		c2 := Account{Key: c2k, Label: "claude-2", LoggedIn: true, Ratio: 8, ReadAt: now, Rate: 66, Meter: 66,
			W5: Window{Level: 20, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 30, Reset: now.Add(140 * time.Hour)}}
		c6 := Account{Key: c6k, Label: "claude-6", LoggedIn: true, Ratio: 8, ReadAt: now, Rate: 0, Meter: 0,
			W5: Window{Level: 10, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 40, Reset: now.Add(100 * time.Hour)}}
		ss := []Session{
			{ID: "c4-own", Account: c4k, Rate: c4rate, Busy: true, Movable: true, Context: 60_000, Started: old},
			{ID: "big", Account: c2k, Rate: 20, Busy: true, Movable: true, Context: 100_000, Started: old},
			{ID: "small", Account: c2k, Rate: 3, Busy: true, Movable: true, Context: 50_000, Started: old},
			{ID: "pinned", Account: c2k, Rate: 10, Busy: true, Movable: true, Pinned: true, PinFrom: "in Claude Code", Started: old},
			{ID: "human", Account: c2k, Rate: 15, Busy: true, Movable: true, Interactive: true, Started: old},
			{ID: "cooling", Account: c2k, Rate: 18, Busy: true, Movable: true, Started: old},
			{ID: "idle", Account: c6k, Rate: 0, Movable: true, Started: old},
		}
		return Input{Now: now, Params: DefaultParams, Accounts: []Account{c4, c2, c6}, Sessions: ss,
			Streak: map[string]int{c4k + " " + Win5h: 5},
			Recent: []Record{{Session: "cooling", From: c6k, To: c2k, At: now.Add(-5 * time.Minute), Rate: 18, RateKnown: true}}}
	}
	t.Run("room: the biggest movable burners come in", func(t *testing.T) {
		r := Plan(world(14, 9.6, 90*time.Minute, until))
		got := moved(r)
		for _, m := range r.Moves {
			t.Logf("%s %s -> %s: %s", m.Session, m.From, m.To, m.Reason)
		}
		if got["big"] != c4k || got["small"] != c4k {
			t.Fatalf("pulls %v: want big and small onto claude-4", got)
		}
		for _, id := range []string{"pinned", "human", "cooling", "idle", "c4-own"} {
			if got[id] != "" {
				t.Errorf("%s moved: %v", id, got)
			}
		}
		if r.Moves[0].Session != "big" || r.Moves[0].Pull == "" || !strings.Contains(r.Moves[0].Reason, "claude-4 is draining (owner, until 18:00)") {
			t.Errorf("first pull %+v", r.Moves[0])
		}
		if n := Note("claude-2", "claude-4", now, r.Moves[0]); !strings.Contains(n, "claude-4 is draining (owner, until 18:00) and has room") {
			t.Errorf("note %q", n)
		}
	})
	t.Run("no room left in its 5h window: nothing pulled", func(t *testing.T) {
		if got := moved(Plan(world(60, 9.6, 3*time.Hour, until))); got["big"] != "" || got["small"] != "" {
			t.Fatalf("pulled %v", got)
		}
	})
	t.Run("over its cap, no hit within 60 min: nothing moves off it", func(t *testing.T) {
		r := Plan(world(60, 15, 3*time.Hour, until)) // 60 + 3 × 15 = 105; 100% in 2h40m
		if got := moved(r); got["c4-own"] != "" {
			t.Fatalf("moved off: %v", got)
		}
		if w := skipWhy(r, "c4-own"); !strings.Contains(w, "draining (owner, until 18:00): nothing moves off it") {
			t.Errorf("skip %q", w)
		}
	})
	t.Run("a 5h hit within 60 min: its burners move", func(t *testing.T) {
		r := Plan(world(85, 30, 3*time.Hour, until)) // 100% in 30 min
		if got := moved(r); got["c4-own"] == "" {
			t.Fatalf("stayed: %s", skipWhy(r, "c4-own"))
		}
	})
	t.Run("past until: no pulls", func(t *testing.T) {
		if got := moved(Plan(world(14, 9.6, 90*time.Minute, now.Add(-time.Minute)))); len(got) != 0 {
			t.Fatalf("moves %v", got)
		}
	})
}
