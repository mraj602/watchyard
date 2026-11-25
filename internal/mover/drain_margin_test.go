package mover

import (
	"testing"
	"time"
)

// A draining account's caps are the drain aim; the switch keeps no Hysteresis under them,
// as a destination or as a source (live pre15: claude-3 at 89% of the week, lanes refused at
// a projected 91% against "cap 95% less the switch margin": the drain never came within 5
// points of its aim). A non-draining account keeps the margin; a pull keeps PullMargin.
func TestDrainNoHysteresis(t *testing.T) {
	in := Input{
		Accounts: []Account{acc("a", 60, 2*time.Hour, 30), acc("b", 74, time.Hour, 30)},
		Sessions: []Session{lane("s", "a", 20, true)}, // a: 60 + 40 = 100 > 95
	}
	// b not draining: 74 + 20 = 94, over 95 less the 5-point margin: nowhere to go.
	if got := moved(plan(in)); got["s"] != "" {
		t.Fatalf("not draining: moved to %q", got["s"])
	}
	// b draining: its 5h cap ramps to 97 an hour before its reset; 94 is under it, no margin.
	in.Accounts[1].Drain = true
	if got := moved(plan(in)); got["s"] != "b" {
		t.Fatalf("draining: moves %v", got)
	}
	pr := DefaultParams.Project(in.Accounts[1], nil, 20, t0)
	if !pr.Draining || DefaultParams.hyst(pr) != 0 {
		t.Fatalf("draining projection %+v, hyst %v", pr, DefaultParams.hyst(pr))
	}
	pp := DefaultParams
	pp.pullMargin = true
	if pp.hyst(pr) != DefaultParams.Hysteresis {
		t.Fatalf("pull margin lost: %v", pp.hyst(pr))
	}
	// The moves off a draining source aim at its caps, not 5 under: one burner over is enough.
	src := acc("c", 90, time.Hour, 30)
	src.Drain = true
	if m5, m7 := DefaultParams.margins(DefaultParams.Project(src, nil, 10, t0)); m5 != 0 || m7 != 0 {
		t.Fatalf("draining source margins %v/%v", m5, m7)
	}
}
