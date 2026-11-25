package mover

import (
	"strings"
	"testing"
	"time"
)

// setup.md 8.5, Kind off: the switch never moves anything into an account that is off, its
// own sessions stay even when it runs hot, and a drain never pulls from it.
func TestPlanOff(t *testing.T) {
	t.Run("its sessions stay even over the cap", func(t *testing.T) {
		a := acc("a", 70, 2*time.Hour, 30)
		a.Off, a.Protected = true, true
		r := plan(Input{Accounts: []Account{a, acc("b", 10, 4*time.Hour, 20)},
			Sessions: []Session{lane("hot", "a", 12, true)}})
		if len(r.Moves) != 0 {
			t.Fatalf("moves %v", moved(r))
		}
		if why := skipWhy(r, "hot"); !strings.HasPrefix(why, "off (owner)") {
			t.Fatalf("skip %q", why)
		}
	})
	t.Run("never a destination", func(t *testing.T) {
		b := acc("b", 10, 4*time.Hour, 20)
		b.Off, b.Protected = true, true
		r := plan(Input{Accounts: []Account{acc("a", 70, 2*time.Hour, 30), b},
			Sessions: []Session{lane("hot", "a", 12, true)}})
		if got := moved(r); got["hot"] == "b" {
			t.Fatalf("moved into an account that is off: %v", got)
		}
	})
	t.Run("a drain pulls nothing from it", func(t *testing.T) {
		d := acc("d", 10, 2*time.Hour, 30)
		d.DrainUntil = t0.Add(6 * time.Hour)
		o := acc("o", 20, 4*time.Hour, 20)
		o.Off, o.Protected = true, true
		old := t0.Add(-2 * time.Hour)
		r := plan(Input{Accounts: []Account{d, o},
			Sessions: []Session{{ID: "s", Account: "o", Rate: 10, Busy: true, Movable: true, Context: 50_000, Started: old}}})
		if len(r.Moves) != 0 {
			t.Fatalf("pulled %v", moved(r))
		}
	})
}
