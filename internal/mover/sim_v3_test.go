package mover

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The live-run fixes and the picker/mover changes (A-C), before and after, on every pattern
// with the pinned coordinators (reserve 10/5):
//
//	WY_SIM_RUNS=300 WY_SIM_PAR=8 WY_SIM_OUT=/tmp/v3.md go test -run TestSimulateV3 -timeout 4h ./internal/mover/
var v3Strategies = func() []simStrat {
	mk := func(name string, oldRate, pickProj bool, horizon time.Duration, cheap bool) simStrat {
		s := pinsStrat(name, true, 10, 5)
		s.oldRate, s.pickProj = oldRate, pickProj
		s.p.DestHorizon, s.p.CheapMoves = horizon, cheap
		if oldRate {
			s.p.ReadingPasses = 0
		}
		return s
	}
	return []simStrat{
		mk("before (1.3.3-pre3)", true, false, 0, false),
		mk("+ rate fix (bug 4)", false, false, 0, false),
		mk("+ A picker uses the switch's projection", false, true, 0, false),
		mk("+ B destination horizon 90", false, false, 90*time.Minute, false),
		mk("+ C cheap moves", false, false, 0, true),
		mk("bug 4 + A + C", false, true, 0, true),
		mk("bug 4 + A + B + C", false, true, 90*time.Minute, true),
		mk("bug 4 + A + B(60) + C", false, true, 60*time.Minute, true),
	}
}()

const v3Header = "| strategy | false moves | moves | all stopped min | hits | pinned stopped min | prot 5h max | prot 7d max | prot>80 min | re-read Mtok | re-read cost (opus-eq Mtok) | no room | waste |\n" +
	"|---|---|---|---|---|---|---|---|---|---|---|---|---|\n"

func writeV3Row(b *strings.Builder, name string, s simResult) {
	days := s.runs * simDays
	waste := 0.0
	if s.resets7 > 0 {
		waste = s.WeeklyWaste / s.resets7
	}
	fmt.Fprintf(b, "| %s | %.2f | %.2f | %.1f | %.2f | %.1f | %.1f | %.1f | %.2f | %.2f | %.2f | %.2f | %.1f |\n", name,
		s.FalseMoves/days, s.Moves/days, s.StoppedMin/days, s.Hits/days, s.PinStopped/days, s.MaxProt5/s.runs, s.MaxProt7/s.runs,
		s.Over80Min/days, s.ReReadMTok/days, s.ReReadCost/days, s.NoDest/days, waste)
}

func TestSimulateV3(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator")
	}
	runs, par := envInt("WY_SIM_RUNS", 1), envInt("WY_SIM_PAR", 2)
	pats, strats := simPatterns, v3Strategies
	if os.Getenv("WY_SIM_RUNS") == "" {
		pats = []simPattern{simPatterns[0], simPatterns[len(simPatterns)-2]}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d seeded runs per pattern, %d days each, with the 5 pinned coordinators (reserve 10/5). Per day per run; "+
		"false moves = moves whose source was under its caps at its true burn (its lanes' and every other machine's).\n\n", runs, simDays)
	totals := map[string]simResult{}
	var names []string
	for _, pat := range pats {
		fmt.Fprintf(&b, "### %s\n\n%s", pat.name, v3Header)
		for _, st := range strats {
			sum := runMany(st, pat, runs, par)
			tot, ok := totals[st.name]
			if !ok {
				names = append(names, st.name)
			}
			tot.add(sum)
			totals[st.name] = tot
			writeV3Row(&b, st.name, sum)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### all patterns\n\n%s", v3Header)
	for _, n := range names {
		writeV3Row(&b, n, totals[n])
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("WY_SIM_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
