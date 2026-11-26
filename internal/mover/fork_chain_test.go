package mover

import (
	"testing"
	"time"
)

// Live 2026-11-26: history-byid-gate moved claude-2 -> claude-6 at 19:36 and Claude Code
// resumed it under a new id (780dc18a); at 19:46 the pull to claude-7 moved it again, inside
// the 20-minute cooldown, because the new id had no move history. A fork is the same session.
func TestForkedSessionKeepsMoveHistory(t *testing.T) {
	now := time.Date(2026, 11, 26, 14, 16, 22, 0, time.UTC) // 19:46:22 IST
	c6k, c7k, c2k := "acct:c6/oA", "acct:c7/oA", "acct:c2/oB"
	world := func(log []LogEntry) Input {
		c6 := Account{Key: c6k, Label: "claude-6", LoggedIn: true, Ratio: 8, ReadAt: now, Rate: 12.6, Meter: 12.6,
			W5: Window{Level: 30, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 50, Reset: now.Add(100 * time.Hour)}}
		c7 := Account{Key: c7k, Label: "claude-7", LoggedIn: true, Ratio: 8, ReadAt: now, DrainUntil: now.Add(18 * time.Hour),
			W5: Window{Level: 10, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 40, Reset: now.Add(82 * time.Hour)}}
		ss := []Session{{ID: "780dc18a", Account: c6k, Rate: 12.6, Busy: true, Movable: true, Context: 80_000, Started: now.Add(-3 * time.Hour)}}
		return Input{Now: now, Params: DefaultParams, Accounts: []Account{c6, c7}, Sessions: ss, Recent: Records(log)}
	}
	at := func(m int) time.Time {
		return time.Date(2026, 11, 26, 14, 6, 7, 0, time.UTC).Add(time.Duration(m) * time.Minute)
	}
	forked := LogEntry{At: at(0), SessionID: "67432abb", From: c2k, To: c6k, Rate: 11.2, RateKnown: true, ResumedAs: "780dc18a"}
	for _, c := range []struct {
		name     string
		log      []LogEntry
		wantMove bool
	}{
		{"no history: the pull goes", nil, true},
		{"moved 10 min ago under the old id, resumed_as recorded: cooldown holds", []LogEntry{forked}, false},
		{"resumed_as not recorded: the new id is a stranger", []LogEntry{{At: forked.At, SessionID: "67432abb", From: c2k, To: c6k, Rate: 11.2, RateKnown: true}}, true},
		{"moved 30 min ago: past the cooldown", []LogEntry{func() LogEntry { e := forked; e.At = now.Add(-30 * time.Minute); return e }()}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := Plan(GateYoung(world(c.log)))
			if got := len(r.Moves) > 0; got != c.wantMove {
				for _, m := range r.Moves {
					t.Logf("%s %s -> %s: %s", m.Session, m.From, m.To, m.Reason)
				}
				t.Fatalf("moved %v, want %v (skips %+v)", got, c.wantMove, r.Skips)
			}
		})
	}
}

func TestWithForksAndChain(t *testing.T) {
	st := State{Dir: t.TempDir()}
	now := time.Now()
	es := []LogEntry{
		{At: now.Add(-3 * time.Hour), SessionID: "a", FromDir: "/1", ToDir: "/2", From: "x", To: "y"},
		{At: now.Add(-2 * time.Hour), SessionID: "a", FromDir: "/2", ToDir: "/3", From: "y", To: "z", ResumedAs: "b"},
		{At: now.Add(-1 * time.Hour), SessionID: "b", FromDir: "/3", ToDir: "/4", From: "z", To: "w"},
	}
	for _, e := range es {
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	rs := Records(es)
	if len(rs) != 4 || !rs[2].Alias || rs[2].Session != "b" || rs[1].Alias {
		t.Fatalf("records %+v", rs)
	}
	if c := st.Chain("b", now.Add(-24*time.Hour)); len(c) != 4 || c[0] != "/1" || c[3] != "/4" {
		t.Fatalf("chain of the forked id %v", c)
	}
	// An alias is not another move: it adds nothing to the account's move count.
	if m := (DefaultParams).newMoveState(Input{Now: now, Recent: []Record{{Session: "b", To: "z", At: now, Alias: true, RateKnown: true, Rate: 50}}, Params: DefaultParams}); m.movedIn["z"] != 0 || m.burnIn["z"] != 0 {
		t.Fatalf("alias counted: %+v", m)
	}
}
