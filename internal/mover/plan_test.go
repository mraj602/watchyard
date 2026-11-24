package mover

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

func acc(key string, l5 float64, reset5 time.Duration, l7 float64) Account {
	return Account{Key: key, Label: key, W5: Window{Level: l5, Reset: t0.Add(reset5)}, W7: Window{Level: l7, Reset: t0.Add(72 * time.Hour)}}
}

func idle(id, account string, rate float64, age, idleFor time.Duration) Session {
	return Session{ID: id, Account: account, Rate: rate, Age: age, Idle: true, IdleFor: idleFor, Context: 300_000, Movable: true}
}

func TestForecastHitBeforeReset(t *testing.T) {
	p := M4
	a := acc("a", 60, 3*time.Hour, 20)
	// 20 points an hour for 2 more hours: 100 at 2 h, before the reset at 3 h.
	f := p.Forecast(a, []Session{idle("s", "a", 20, 2*time.Hour, 0)}, t0, 0)
	w, hits := f.First()
	if !hits || w.Window != Win5h || w.HitIn != 2*time.Hour {
		t.Fatalf("forecast %+v", w)
	}
	// The same burn with the reset in 90 minutes: no hit.
	a.W5.Reset = t0.Add(90 * time.Minute)
	if _, hits := p.Forecast(a, []Session{idle("s", "a", 20, 2*time.Hour, 0)}, t0, 0).First(); hits {
		t.Error("hit after the reset")
	}
	// A session expected to end in 30 minutes burns only 10 more points.
	s := idle("s", "a", 20, 2*time.Hour, 0)
	s.Expected = 30 * time.Minute
	a.W5.Reset = t0.Add(3 * time.Hour)
	if _, hits := p.Forecast(a, []Session{s}, t0, 0).First(); hits {
		t.Error("a short session hits")
	}
	// The weekly window: 5-hour points over the ratio.
	b := acc("b", 0, 5*time.Hour, 98)
	b.W7.Reset = t0.Add(10 * time.Hour)
	f = p.Forecast(b, []Session{idle("s", "b", 36, 10*time.Hour, 0)}, t0, 0)
	if w, hits := f.First(); !hits || w.Window != Win7d || w.HitIn != 24*time.Minute {
		t.Errorf("weekly %+v", w)
	}
}

func planOf(p Params, accs []Account, ss []Session, recent ...Record) []Move {
	return Plan(Input{Now: t0, Accounts: accs, Sessions: ss, Params: p, Recent: recent})
}

func TestPlanRules(t *testing.T) {
	src := acc("a", 80, 3*time.Hour, 20)
	dst := acc("b", 10, 4*time.Hour, 20)
	prot := acc("p", 0, 5*time.Hour, 0)
	prot.Protected = true
	accs := []Account{prot, src, dst}
	hot := idle("hot", "a", 30, 2*time.Hour, 10*time.Minute) // hits in 40 min

	t.Run("moves when the hit comes before the reset, never to protected first", func(t *testing.T) {
		ms := planOf(M4, accs, []Session{hot})
		if len(ms) != 1 || ms[0].To != "b" || ms[0].Wait {
			t.Fatalf("%+v", ms)
		}
	})
	t.Run("no move when the reset comes first", func(t *testing.T) {
		a := src
		a.W5.Reset = t0.Add(30 * time.Minute)
		if ms := planOf(M4, []Account{a, dst}, []Session{hot}); len(ms) != 0 {
			t.Fatalf("%+v", ms)
		}
	})
	t.Run("pinned never moves", func(t *testing.T) {
		s := hot
		s.Pinned = true
		if ms := planOf(M4, accs, []Session{s}); len(ms) != 0 {
			t.Fatalf("%+v", ms)
		}
	})
	t.Run("not while the owner types into it", func(t *testing.T) {
		s := hot
		s.Attended = true
		if ms := planOf(M4, accs, []Session{s}); len(ms) != 0 {
			t.Fatalf("%+v", ms)
		}
		s.Stopped = true // unless the limit already stopped it
		if ms := planOf(M4, []Account{prot, acc("a", 100, 3*time.Hour, 20), dst}, []Session{s}); len(ms) != 1 || ms[0].To != "b" {
			t.Fatalf("stopped: %+v", ms)
		}
	})
	t.Run("not a session that ends before the hit", func(t *testing.T) {
		s := hot
		s.Expected = 20 * time.Minute
		other := idle("other", "a", 30, 3*time.Hour, 10*time.Minute)
		ms := planOf(M4, accs, []Session{s, other})
		for _, m := range ms {
			if m.Session == "hot" {
				t.Fatalf("moved a finishing session: %+v", ms)
			}
		}
	})
	t.Run("mid-turn: skipped, or a wait when the hit is near", func(t *testing.T) {
		s := hot
		s.Idle = false
		if ms := planOf(M4, accs, []Session{s}); len(ms) != 0 {
			t.Fatalf("far hit: %+v", ms)
		}
		near := acc("a", 98, 3*time.Hour, 20)
		ms := planOf(M4, []Account{prot, near, dst}, []Session{s})
		if len(ms) != 1 || !ms[0].Wait {
			t.Fatalf("near hit: %+v", ms)
		}
	})
	t.Run("cold cache first, then small context", func(t *testing.T) {
		warm := idle("warm", "a", 15, 3*time.Hour, time.Minute)
		cold := idle("cold", "a", 15, 3*time.Hour, 20*time.Minute)
		small := idle("small", "a", 15, 3*time.Hour, time.Minute)
		small.Context = 20_000
		ms := planOf(M4, accs, []Session{warm, small, cold})
		if len(ms) == 0 || ms[0].Session != "cold" || ms[0].Cost != M4.MoveCostFixed {
			t.Fatalf("%+v", ms)
		}
		ms = planOf(M4, accs, []Session{warm, small})
		if len(ms) == 0 || ms[0].Session != "small" {
			t.Fatalf("%+v", ms)
		}
	})
	t.Run("the move must save more than it costs", func(t *testing.T) {
		// At the limit 6 minutes before a reset 15 minutes away: 9 stalled minutes are not
		// worth a 1M-token re-read.
		a := acc("a", 99.9, 15*time.Minute, 20)
		s := idle("big", "a", 1, 3*time.Hour, time.Minute)
		s.Context = 1_000_000
		p := M4
		p.Horizon = 0
		if ms := planOf(p, []Account{a, dst}, []Session{s}); len(ms) != 0 {
			t.Fatalf("%+v", ms)
		}
	})
	t.Run("once per window, cooldown and no ping-pong", func(t *testing.T) {
		s := hot
		s.MovedAt = t0.Add(-time.Hour)
		if ms := planOf(M4, accs, []Session{s}); len(ms) != 0 {
			t.Fatalf("moved twice in a window: %+v", ms)
		}
		ms := planOf(M4, accs, []Session{hot}, Record{Session: "hot", From: "b", To: "a", At: t0.Add(-40 * time.Minute)})
		if len(ms) != 1 || ms[0].To != "p" { // not back to b, which it left 40 minutes ago
			t.Fatalf("ping-pong: %+v", ms)
		}
		ms = planOf(M4, accs, []Session{hot}, Record{Session: "hot", From: "c", To: "a", At: t0.Add(-10 * time.Minute)})
		if len(ms) != 0 {
			t.Fatalf("cooldown: %+v", ms)
		}
	})
	t.Run("a destination must stay under its limit until its reset", func(t *testing.T) {
		full := acc("b", 70, 4*time.Hour, 20)
		on := idle("there", "b", 10, 4*time.Hour, time.Minute)
		ms := planOf(M4, []Account{src, full}, []Session{hot, on})
		if len(ms) != 1 || ms[0].To != "" {
			t.Fatalf("%+v", ms)
		}
		// The protected account takes it when nothing else can.
		ms = planOf(M4, []Account{prot, src, full}, []Session{hot, on})
		if len(ms) != 1 || ms[0].To != "p" {
			t.Fatalf("protected last: %+v", ms)
		}
	})
	t.Run("off moves nothing", func(t *testing.T) {
		if ms := planOf(M0, accs, []Session{hot}); len(ms) != 0 {
			t.Fatal(ms)
		}
	})
}

func TestDestination(t *testing.T) {
	in := Input{Now: t0, Params: M4, Accounts: []Account{acc("a", 80, 3*time.Hour, 20), acc("b", 10, 4*time.Hour, 20)},
		Sessions: []Session{idle("s", "a", 30, 2*time.Hour, 0)}}
	if to, _ := Destination(in, "s"); to != "b" {
		t.Errorf("to %q", to)
	}
	if to, why := Destination(in, "x"); to != "" || why == "" {
		t.Error("unknown session")
	}
}

// TestPlanFullAccountIdleSessions: the shape of the 1.3.3-pre worker case. Three idle tmux
// sessions at a prompt (contexts of different size, idle 5 min to 2 h) on an account at 100 %
// whose 5-hour window resets in 20 minutes, a destination with room: nothing moves, each with
// its reason. Stopped by the limit, they wait for a near reset and move for a far one.
func TestPlanFullAccountIdleSessions(t *testing.T) {
	for _, p := range []Params{M4, M5} {
		full := acc("c4", 100, 20*time.Minute, 30)
		dst := acc("c2", 20, 4*time.Hour, 20)
		ss := []Session{
			idle("voice", "c4", 0, 3*time.Hour, 5*time.Minute),
			idle("bench", "c4", 0, 6*time.Hour, 40*time.Minute),
			idle("aws", "c4", 0, 26*time.Hour, 2*time.Hour),
		}
		ss[0].Context, ss[1].Context, ss[2].Context = 40_000, 120_000, 180_000
		in := Input{Now: t0, Accounts: []Account{full, dst}, Sessions: ss, Params: p}
		ms, skips := PlanWhy(in)
		if len(ms) != 0 || len(skips) != 3 {
			t.Fatalf("%s idle: moves %+v skips %+v", p.Name, ms, skips)
		}
		for _, s := range skips {
			if !strings.Contains(s.Why, "idle, not stopped by the limit") {
				t.Errorf("%s: %+v", p.Name, s)
			}
		}
		for i := range in.Sessions {
			in.Sessions[i].Stopped, in.Sessions[i].Idle = true, false
		}
		ms, skips = PlanWhy(in)
		if len(ms) != 0 || len(skips) != 3 || !strings.Contains(skips[0].Why, "waiting is cheaper than a move") {
			t.Fatalf("%s stopped, near reset: %+v %+v", p.Name, ms, skips)
		}
		in.Accounts[0].W5.Reset = t0.Add(2 * time.Hour)
		ms, _ = PlanWhy(in)
		if len(ms) == 0 || ms[0].To != "c2" || !strings.HasPrefix(ms[0].Reason, "stopped at the 5h limit") {
			t.Fatalf("%s stopped, far reset: %+v", p.Name, ms)
		}
	}
}
