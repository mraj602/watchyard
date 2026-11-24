package mover

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// Young lanes (live 2026-11-25 on pre4: two lanes moved 3 minutes after launch): a lane's first
// minutes burn far above its steady rate (reading the codebase, cache writes), and the burn
// window then holds only those minutes. The workload adds a startup burst (the first 3-8 burned
// minutes at 3-6x); the strategies differ only in the session rate the plan sees.
//
//	WY_SIM_RUNS=300 WY_SIM_PAR=8 WY_SIM_OUT=/tmp/young.md go test -run TestSimulateYoung -timeout 4h ./internal/mover/
var youngStrategies = func() []simStrat {
	mk := func(name string, startup bool, measure func(l *simLane) float64) simStrat {
		s := pinsStrat(name, true, 10, 5)
		s.pickProj = true
		s.p.DestHorizon, s.p.CheapMoves = 90*time.Minute, true
		s.startup, s.measure = startup, measure
		return s
	}
	// shipped: Params.YoungGate as shipped; the sim's meter is the full one, so the readings
	// are compared with it, as in wy move.
	shipped := func(s simStrat) simStrat { s.p.YoungGate = DefaultParams.YoungGate; return s }
	floor := func(s simStrat) simStrat { s.p.MinBurn = DefaultParams.MinBurn; return s }
	oldChurn := func(s simStrat) simStrat { s.p.oldChurn, s.p.AccountMoves, s.p.AccountBurn = true, 3, 0; return s }
	youngMin := func(m int) func(simStrat) simStrat {
		return func(s simStrat) simStrat { s.p.YoungGate = time.Duration(m) * time.Minute; return s }
	}
	readFull := func(s simStrat) simStrat { s.readFull = true; return s }
	ageW := func(l *simLane, full float64) float64 { return math.Min(1, float64(l.age)/full) }
	gate := func(min int) func(l *simLane) float64 {
		return func(l *simLane) float64 {
			if l.coord == nil && l.age < min {
				return 0
			}
			return l.rate10()
		}
	}
	weighted := func(l *simLane) float64 {
		if l.coord != nil {
			return l.rate10()
		}
		return l.rate10() * ageW(l, 10)
	}
	cacheW := func(w float64) func(l *simLane) float64 {
		return func(l *simLane) float64 {
			s := 0.0
			for i := range l.obs {
				s += l.obs[i] - (1-w)*l.obsCW[i]
			}
			return math.Max(0, s/10)
		}
	}
	est := func(l *simLane) float64 {
		if l.coord != nil || l.owner || l.age >= 15 {
			return l.rate10()
		}
		return math.Max(l.est, l.rate10()*ageW(l, 10))
	}
	estW := func(l *simLane) float64 {
		if l.coord != nil || l.owner || l.age >= 15 {
			return l.rate10()
		}
		// Blend from the estimate to the measured rate over 15 minutes.
		w := ageW(l, 15)
		return (1-w)*l.est + w*l.rate10()
	}
	all := []simStrat{
		mk("no startup burst (reference)", false, nil),
		mk("startup burst, as now", true, nil),
		shipped(mk("shipped: YoungGate 15 min (Plan)", true, nil)),
		floor(shipped(mk("shipped + burn floor 1 point/h, greedy stop", true, nil))),
		oldChurn(floor(shipped(mk("same, anti-churn as before (3 moves, any burn)", true, nil)))),
		floor(youngMin(5)(mk("floor + young left out 5 min only", true, nil))),
		floor(youngMin(3)(mk("floor + young left out 3 min only", true, nil))),
		floor(mk("floor + young < 15 min at max(size-class est, rate × age/10)", true, est)),
		mk("(a) counts from 8 min", true, gate(8)),
		mk("(a) counts from 10 min", true, gate(10)),
		mk("(a) counts from 12 min", true, gate(12)),
		mk("(a) counts from 15 min", true, gate(15)),
		mk("(a) counts from 20 min", true, gate(20)),
		readFull(mk("(a) from 10 min, readings vs full meter", true, gate(10))),
		readFull(mk("(a) from 15 min, readings vs full meter", true, gate(15))),
		mk("(a) rate × min(1, age/10)", true, weighted),
		readFull(mk("(a) rate × min(1, age/10), readings vs full", true, weighted)),
		mk("(b) first-10-min cache writes × 0.25", true, cacheW(0.25)),
		mk("(b) first-10-min cache writes × 0.5", true, cacheW(0.5)),
		mk("(c) < 15 min: max(est, rate × age/10)", true, est),
		mk("(c') < 15 min: est → measured blend", true, estW),
	}
	// WY_SIM_YOUNG=short: the main candidates only (for the 300-run table).
	if os.Getenv("WY_SIM_YOUNG") == "short" {
		var out []simStrat
		for _, i := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
			out = append(out, all[i])
		}
		return out
	}
	return all
}()

const youngHeader = "| strategy | young moves | false young moves | moves under 1 point/h | moves | false moves | all stopped min | hits | pinned stopped min | prot 5h max | prot>80 min | re-read cost (opus-eq Mtok) |\n" +
	"|---|---|---|---|---|---|---|---|---|---|---|---|\n"

func writeYoungRow(b *strings.Builder, name string, s simResult) {
	days := s.runs * simDays
	fmt.Fprintf(b, "| %s | %.2f | %.2f | %.2f | %.2f | %.2f | %.1f | %.2f | %.1f | %.1f | %.2f | %.2f |\n", name,
		s.YoungMoves/days, s.YoungFalse/days, s.LowMoves/days, s.Moves/days, s.FalseMoves/days, s.StoppedMin/days, s.Hits/days, s.PinStopped/days,
		s.MaxProt5/s.runs, s.Over80Min/days, s.ReReadCost/days)
}

func TestSimulateYoung(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator")
	}
	runs, par := envInt("WY_SIM_RUNS", 1), envInt("WY_SIM_PAR", 2)
	pats := simPatterns
	if os.Getenv("WY_SIM_RUNS") == "" {
		pats = []simPattern{simPatterns[0], simPatterns[len(simPatterns)-2]}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d seeded runs per pattern, %d days each, with the 5 pinned coordinators (reserve 10/5), on the "+
		"current switch (bug 4 + A + B + C). Per day per run; young moves = moves of lanes under 15 minutes old; false "+
		"moves = the source was under its caps at its true steady burn (without the startup burst).\n\n", runs, simDays)
	totals := map[string]simResult{}
	var names []string
	for _, pat := range pats {
		fmt.Fprintf(&b, "### %s\n\n%s", pat.name, youngHeader)
		for _, st := range youngStrategies {
			sum := runMany(st, pat, runs, par)
			tot, ok := totals[st.name]
			if !ok {
				names = append(names, st.name)
			}
			tot.add(sum)
			totals[st.name] = tot
			writeYoungRow(&b, st.name, sum)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### all patterns\n\n%s", youngHeader)
	for _, n := range names {
		writeYoungRow(&b, n, totals[n])
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("WY_SIM_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
