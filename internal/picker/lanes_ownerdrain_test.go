package picker

import (
	"strings"
	"testing"
	"time"
)

// setup.md 8.5, Owner drain: `wy account drain claude-4` (live 2026-11-25: the owner wants
// claude-4's week used up today, its weekly reset days away). While on, the account is an
// aggressive drain account with finish_by = until: new lanes go to it first.
func TestOwnerDrain(t *testing.T) {
	until := lnow.Add(11*time.Hour + 59*time.Minute)
	run := func(w float64, until time.Time, protected bool) (LaneResult, LaneInput) {
		s := dworld()
		s[dZ] = dsmp(w, 14, 91, 4)
		in := laneIn(s, 3)
		for i := range in.Accounts {
			if in.Accounts[i].Key == dZ {
				in.Accounts[i].DrainUntil = until
				if protected {
					// lanes_input: a protected account drained by its owner is aggressive meanwhile.
					in.Accounts[i].Protected, in.Accounts[i].OwnerProtected = false, true
				}
			}
		}
		return rank(t, in), in
	}
	t.Run("on: drained first, finish by until", func(t *testing.T) {
		r, in := run(53, until, false)
		d := drainOf(t, r, dZ)
		if !d.On || !d.Owner || !d.FinishBy.Equal(until) || rowOf(r, dZ).Class != ClassDrain {
			t.Fatalf("drain %+v class %s", d, rowOf(r, dZ).Class)
		}
		if got := picksOf(r); len(got) == 0 || got[0] != dZ || countOf(r, dZ) != 3 {
			t.Errorf("picks %v: all three on claude-4 (its week has 44%% to use)", got)
		}
		la, _ := in.Live()
		for _, a := range la {
			if a.Key == dZ && !a.DrainUntil.Equal(until) {
				t.Errorf("live account %+v: want DrainUntil", a)
			}
		}
		if v := drainView(d); !v.Owner {
			t.Errorf("view %+v", v)
		}
	})
	t.Run("a protected account, drained by its owner", func(t *testing.T) {
		r, _ := run(53, until, true)
		if d := drainOf(t, r, dZ); !d.On || !d.Owner || rowOf(r, dZ).Class != ClassDrain || picksOf(r)[0] != dZ {
			t.Fatalf("drain %+v picks %v", d, picksOf(r))
		}
	})
	t.Run("past until: over", func(t *testing.T) {
		r, in := run(53, lnow.Add(-time.Minute), false)
		if d := drainOf(t, r, dZ); d.On || d.Owner {
			t.Fatalf("drain %+v", d)
		}
		la, _ := in.Live()
		for _, a := range la {
			if a.Key == dZ && !a.DrainUntil.IsZero() {
				t.Errorf("live account %+v", a)
			}
		}
	})
	t.Run("at the drain aim: over, protected again", func(t *testing.T) {
		r, in := run(97.5, until, true)
		if d := drainOf(t, r, dZ); d.On || rowOf(r, dZ).Class == ClassDrain {
			t.Fatalf("drain %+v class %s", d, rowOf(r, dZ).Class)
		}
		la, _ := in.Live()
		for _, a := range la {
			if a.Key == dZ && (!a.DrainUntil.IsZero() || !a.Protected) {
				t.Errorf("live account %+v: want protected, no drain", a)
			}
		}
	})
	if got := OwnerDrainText(time.Date(2026, 11, 20, 23, 59, 0, 0, time.UTC), lnow); got != "draining (owner, until 23:59)" {
		t.Errorf("text %q", got)
	}
	if got := OwnerDrainText(time.Date(2026, 11, 22, 7, 30, 0, 0, time.UTC), lnow); !strings.HasPrefix(got, "draining (owner, until Sun 07:30") {
		t.Errorf("text %q", got)
	}
}
