package mover

import (
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
			in.Streak[a.Key] = 1
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
	if len(r.Moves) != 0 || r.Streak["a"] != 1 || !r.Accounts[0].Held {
		t.Fatalf("first pass moved: %+v %v", r.Moves, r.Streak)
	}
	if !strings.Contains(skipWhy(r, "s"), "spike filter") {
		t.Fatalf("skip %q", skipWhy(r, "s"))
	}
	in.Streak = r.Streak
	if r := Plan(in); len(r.Moves) != 1 || r.Streak["a"] != 2 {
		t.Fatalf("second pass: %+v", r.Moves)
	}
	// A projection back under resets the streak.
	in.Sessions[0].Rate = 1
	if r := Plan(in); r.Streak["a"] != 0 {
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

// Interactive sessions are never moved: they get one notice, then again only after 30 min
// and only when the projection got worse; their burn still counts, so another burner moves.
func TestPlanInteractiveNotice(t *testing.T) {
	owner := lane("owner", "a", 15, true)
	owner.Interactive = true
	in := Input{
		Accounts: []Account{acc("a", 50, 4*time.Hour, 30), acc("b", 0, 5*time.Hour, 10)},
		Sessions: []Session{owner, lane("bot", "a", 8, true)}, // 50 + 4×23 = 142
	}
	r := plan(in)
	if got := moved(r); len(got) != 1 || got["bot"] != "b" {
		t.Fatalf("moves %v", got)
	}
	if len(r.Notices) != 1 || r.Notices[0].Session != "owner" || r.Notices[0].To != "b" {
		t.Fatalf("notices %+v", r.Notices)
	}
	p := r.Notices[0].P
	in.Noticed = map[string]NoticeRecord{"owner": {At: t0.Add(-10 * time.Minute), P: p - 20}}
	if r := plan(in); len(r.Notices) != 0 {
		t.Fatal("told again within 30 min")
	}
	in.Noticed["owner"] = NoticeRecord{At: t0.Add(-40 * time.Minute), P: p}
	if r := plan(in); len(r.Notices) != 0 {
		t.Fatal("told again with no worse projection")
	}
	in.Noticed["owner"] = NoticeRecord{At: t0.Add(-40 * time.Minute), P: p - 1}
	if r := plan(in); len(r.Notices) != 1 {
		t.Fatal("not told again after 30 min with a worse projection")
	}
	// A stopped interactive session is told, not moved.
	owner.Stopped, owner.Rate = true, 0
	in.Sessions, in.Noticed = []Session{owner}, nil
	in.Accounts[0].W5.Level = 100
	r = plan(in)
	if len(r.Moves) != 0 || len(r.Notices) != 1 {
		t.Fatalf("stopped interactive: moves %+v notices %+v", r.Moves, r.Notices)
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
// window reset since; at most 3 moves into one account per 15 min; no once-per-window limit.
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
	// Account rate limit: 3 moves into c within 15 min.
	in.Accounts = []Account{acc("a", 80, 2*time.Hour, 30), acc("c", 0, 4*time.Hour, 10)}
	in.Recent = nil
	for i := range 3 {
		in.Recent = append(in.Recent, Record{Session: "o" + string(rune('0'+i)), From: "z", To: "c", At: t0.Add(-5 * time.Minute)})
	}
	r = plan(in)
	if len(r.Moves) != 1 || r.Moves[0].To != "" || !strings.Contains(r.Moves[0].Reason, "rate limit") {
		t.Fatalf("rate limit: %+v", r.Moves)
	}
	in.Recent = in.Recent[:2]
	if got := moved(plan(in)); got["s"] != "c" {
		t.Fatalf("under the rate limit: %v", got)
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
	got := moved(plan(in))
	if got["s1"] == got["s2"] || got["s1"] == "" || got["s2"] == "" {
		t.Fatalf("moves %v", got)
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
	if got := Note("claude-4", "claude-2", at, Move{ByHand: true}); !strings.Contains(got, "(by hand, with wy move)") {
		t.Errorf("by hand: %s", got)
	}
}
