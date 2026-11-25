package mover

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Moves the trigger undoes (live pre7: 3b2611ed moved onto claude-6 at 06:12 and off it at
// 06:35). Undone = a trigger move of a session off the account it was moved onto under 30
// minutes before, no session arrived there since and its burn up at most 25%.
//
//	WY_SIM_RUNS=30 WY_SIM_PAR=8 WY_SIM_OUT=/tmp/churn.md go test -run TestSimulateChurn -timeout 4h ./internal/mover/
func TestSimulateChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator")
	}
	mk := func(name string, old bool, margin float64) simStrat {
		s := pinsStrat(name, true, 10, 5)
		s.pickProj, s.startup = true, true
		s.p.CheapMoves, s.p.YoungGate, s.p.MinBurn = true, DefaultParams.YoungGate, DefaultParams.MinBurn
		if old {
			s.p.DestHorizon, s.p.oldDest, s.oldPick = 90*time.Minute, true, true
		} else {
			s.expectPick, s.pickMargin = true, margin
		}
		return s
	}
	strats := []simStrat{
		mk("before (pre7): destination and new lane on a 90-min horizon", true, 0),
		mk("after: one rule, as the trigger projects (expected, at least 30 min, else to the horizons)", false, 0),
		mk("after, and the picker leaves the destination's 5 under the caps", false, DefaultParams.Hysteresis),
	}
	// The lease line's expect (1.3.3-pre9 → after): half the lanes leased without --expect
	// (the picker counts them as medium lanes); pre9's switch knew no lane's length (every
	// lane to the horizons), after it reads the lease's expected end (to the horizons once
	// overrun).
	lease := func(name string, se int) simStrat {
		s := mk(name, false, DefaultParams.Hysteresis)
		s.noExpFrac, s.switchExpect = 0.5, se
		return s
	}
	strats = append(strats,
		lease("pre9: half without --expect; the switch knows no lane's length", switchNone),
		lease("after: the switch reads the lease's expected end (medium without --expect)", switchLease))
	runs, par := envInt("WY_SIM_RUNS", 1), envInt("WY_SIM_PAR", 2)
	pats := simPatterns
	if os.Getenv("WY_SIM_RUNS") == "" {
		pats = []simPattern{simPatterns[0], simPatterns[len(simPatterns)-2]}
	}
	head := "| strategy | undone moves | undone placements (lane at its estimate / others rose / burn from the readings) | moves | all stopped min | hits | pinned stopped min | prot 5h max | prot>80 min | re-read cost |\n|---|---|---|---|---|---|---|---|---|---|\n"
	row := func(b *strings.Builder, name string, s simResult) {
		days := s.runs * simDays
		fmt.Fprintf(b, "| %s | %.2f | %.2f (%.2f / %.2f / %.2f) | %.2f | %.1f | %.2f | %.1f | %.1f | %.2f | %.2f |\n", name, s.Undone/days, s.PlaceUndone/days, s.PlaceUndoneAtEst/days, s.PlaceUndoneOthers/days, s.PlaceUndoneReadings/days, s.Moves/days, s.StoppedMin/days,
			s.Hits/days, s.PinStopped/days, s.MaxProt5/s.runs, s.Over80Min/days, s.ReReadCost/days)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d seeded runs per pattern, %d days each, per run-day.\n\n", runs, simDays)
	tot := make([]simResult, len(strats))
	for _, pat := range pats {
		fmt.Fprintf(&b, "### %s\n\n%s", pat.name, head)
		for i, st := range strats {
			r := runMany(st, pat, runs, par)
			tot[i].add(r)
			row(&b, st.name, r)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### all patterns\n\n%s", head)
	for i, st := range strats {
		row(&b, st.name, tot[i])
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("WY_SIM_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
