package mover

import (
	"strings"
	"testing"
	"time"
)

// A lane's first minutes burn far above its steady rate: under YoungGate its burn is left out
// of its account's meter, so it does not trip the trigger (live 2026-11-25: two lanes moved
// 3 minutes after launch).
func TestGateYoung(t *testing.T) {
	now := time.Date(2026, 11, 25, 1, 10, 0, 0, time.UTC)
	p := DefaultParams
	acc := Account{Key: "acct:a/o", Rate: 45, Meter: 45, LoggedIn: true, Ratio: 7.2,
		W5: Window{Level: 10, Reset: now.Add(4*time.Hour + 57*time.Minute)}, W7: Window{Level: 36, Reset: now.Add(48 * time.Hour)}}
	young := Session{ID: "y", Account: acc.Key, Rate: 40, Busy: true, Movable: true, Started: now.Add(-3 * time.Minute)}
	old := Session{ID: "o", Account: acc.Key, Rate: 5, Busy: true, Movable: true, Started: now.Add(-time.Hour)}
	in := Input{Now: now, Params: p, Accounts: []Account{acc}, Sessions: []Session{young, old}}

	g := GateYoung(in)
	if g.Sessions[0].Rate != 0 || g.Sessions[0].YoungRate != 40 || g.Sessions[1].Rate != 5 {
		t.Fatalf("sessions %+v", g.Sessions)
	}
	if a := g.Accounts[0]; a.Rate != 5 || a.Meter != 5 || !strings.Contains(a.RateNote, "under 15 min left out") {
		t.Fatalf("account %+v", a)
	}
	if in.Sessions[0].Rate != 40 || in.Accounts[0].Rate != 45 {
		t.Fatal("GateYoung changed its input")
	}
	if r := Plan(in); len(r.Moves) != 0 {
		t.Fatalf("a young lane's start-up burn moved: %+v", r.Moves)
	}
	// Without the gate the same pass moves (P5 = 10 + 45 x 4.95 h).
	p0 := p
	p0.YoungGate = 0
	in0 := in
	in0.Params = p0
	in0.Streak = map[string]int{acc.Key + " " + Win5h: 5}
	in0.Accounts = append([]Account{acc}, Account{Key: "acct:b/o", LoggedIn: true, Ratio: 7.2,
		W7: Window{Level: 10, Reset: now.Add(72 * time.Hour)}})
	if r := Plan(in0); len(r.Moves) == 0 {
		t.Fatalf("no move without the gate: %+v", r)
	}
	// Past the gate it counts.
	in.Now = now.Add(13 * time.Minute)
	if g := GateYoung(in); g.Sessions[0].Rate != 40 {
		t.Fatal("still gated at 16 minutes")
	}
	// A rate from the readings is not reduced (it sees the account, not the sessions).
	in.Now = now
	in.Accounts[0].FromReadings = true
	if g := GateYoung(in); g.Accounts[0].Rate != 45 {
		t.Fatalf("readings rate reduced: %+v", g.Accounts[0])
	}
	// A session of unknown age is taken as old.
	in.Accounts[0].FromReadings = false
	in.Sessions[0].Started = time.Time{}
	if g := GateYoung(in); g.Sessions[0].Rate != 40 {
		t.Fatal("unknown age gated")
	}
}
