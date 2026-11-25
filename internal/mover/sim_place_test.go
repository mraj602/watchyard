package mover

import (
	"fmt"
	"github.com/mraj602/watchyard/internal/projection"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Placement (live 2026-11-24 on pre4.1: four lanes placed on claude-3 at 81-83% weekly, each
// moved off 4-20 minutes later): the lanes picker ranked claude-3 at risk against 98 while
// the switch's pin-adjusted cap was 88, and took a new lane's burn as its size class's cost
// over its wall time (about 4 points/h) while Opus lanes burn 12-26. The workload is the
// young-lanes one (startup burst, the 5 pinned coordinators, the shipped switch); the
// strategies differ only in the picker's placement.
//
//	WY_SIM_RUNS=300 WY_SIM_PAR=8 WY_SIM_OUT=/tmp/place.md go test -run TestSimulatePlacement -timeout 4h ./internal/mover/

// simClassRate is a medium lane's class rate: its class cost over its class wall time, 5-hour
// points per hour (what the picker took as a new lane's burn).
const simClassRate = 4.0

// rankKey orders the aggressive accounts the picker may place on: drain first (the soonest
// weekly reset), then at risk (the least time per hour of need), then the lowest 5-hour level.
type rankKey struct {
	group   int
	ord, l5 float64
}

func (a rankKey) less(b rankKey) bool {
	if a.group != b.group {
		return a.group < b.group
	}
	if a.ord != b.ord {
		return a.ord < b.ord
	}
	return a.l5 < b.l5
}

// rankOf is the lanes picker's class of an aggressive account (setup.md 8.5, Drain): the
// weekly room to its target, the hours it needs at full 5-hour speed, drain within
// max(24 h, 2 × need) of the reset while its running lanes do not fill the room (fwd, 5-hour
// points), at risk within 4 × need. capTarget: the target and the speed are the switch's caps
// less the pin reserve; else 98 (the reserve only on the drain's weekly limit), as before.
func rankOf(st simStrat, a *simAcc, m int, r5, r7, fwd float64) rankKey {
	hrs := float64(a.r7-m) / 60
	t7, t5, dr7 := 98.0, 98.0, 98-r7
	if st.capTarget {
		t7, t5 = math.Min(98, st.p.Cap7)-r7, math.Min(98, st.p.Cap5)-r5
		dr7 = t7
	}
	if st.ramp && !a.protected && hrs < projection.DrainRampH {
		dr7 = simAim7(st, a, m, 98) - 1 - r7 // the ramped aim (Drain aim)
		t7 = math.Max(t7, dr7)
	}
	need := math.Max(0, t7-a.l7) * simRatio / t5 * 5
	room := dr7 - 0.5 - a.l7
	if room > 0.5 && room*simRatio > fwd {
		if dneed := room * simRatio / t5 * 5; hrs <= math.Max(24, 2*dneed) {
			return rankKey{group: 0, ord: hrs, l5: a.l5}
		}
	}
	if need > 0 && hrs <= 4*need {
		return rankKey{group: 1, ord: hrs / need, l5: a.l5}
	}
	return rankKey{group: 2, l5: a.l5}
}

// measuredRate is the measured burn of the lanes of model mf, 5-hour points per hour of wall
// time: the quantile q of the finished lanes' and those running for 30 minutes or more
// (at least 5 of them; else the class rate).
func measuredRate(lanes []*simLane, mf, q float64) float64 {
	var rs []float64
	for _, l := range lanes {
		if l.coord != nil || l.owner || l.mf != mf || !l.on {
			continue
		}
		switch {
		case l.done && l.endAt > l.start:
			rs = append(rs, l.burned/float64(l.endAt-l.start+1)*60)
		case !l.done && l.age >= 30:
			rs = append(rs, l.burned/float64(l.age)*60)
		}
	}
	if len(rs) < 5 {
		return simClassRate
	}
	sort.Float64s(rs)
	pos := q * float64(len(rs)-1)
	i := int(pos)
	if i+1 >= len(rs) {
		return rs[len(rs)-1]
	}
	return rs[i] + (pos-float64(i))*(rs[i+1]-rs[i])
}

// weekly80s is the live case: aggressive accounts in the 80s of their week with the weekly
// reset 12-48 hours away (the at-risk ranking's turf), lanes that burn like Opus lanes.
var weekly80s = simPattern{name: "weekly-80s (live case)", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, lanes: func(r *rand.Rand) []laneSpec {
	return spread(r, 16, 60, 300, 12, 22, 0.2, 0.6, 1.6)
}, setup: func(r *rand.Rand, accs []*simAcc) {
	for i := 3; i < simAccounts; i++ {
		if r.Float64() < 0.6 {
			accs[i].l7, accs[i].r7 = uniform(r, 78, 88), (12+r.IntN(36))*60
		}
	}
}}

var placeStrategies = func() []simStrat {
	mk := func(name string, capTarget bool, rate int, q float64) simStrat {
		s := pinsStrat(name, true, 10, 5)
		s.pickProj = true
		s.p.DestHorizon, s.p.CheapMoves = 90*time.Minute, true
		s.p.YoungGate = DefaultParams.YoungGate
		s.startup, s.rank, s.capTarget, s.laneRate, s.rateQ = true, true, capTarget, rate, q
		return s
	}
	cheap := func(s simStrat) simStrat { s.p.CheapMoves = false; return s }
	return []simStrat{
		mk("before: target 98, lane at its class rate", false, laneClass, 0),
		mk("1 only: target cap − reserve", true, laneClass, 0),
		mk("2 only: lane at measured p75", false, laneMeasured, 0.75),
		mk("1 + 2: measured p50", true, laneMeasured, 0.5),
		mk("1 + 2: measured p75 (shipped)", true, laneMeasured, 0.75),
		mk("1 + lane at its true busy rate (bound)", true, laneTrue, 0),
		cheap(mk("1 + 2 p75, cheap moves off (diagnostic)", true, laneMeasured, 0.75)),
	}
}()

const placeHeader = "| strategy | lanes placed | moved within 30 min | % of placed | moves | all stopped min | hits | pinned stopped min | prot>80 min | weekly waste |\n" +
	"|---|---|---|---|---|---|---|---|---|---|\n"

func writePlaceRow(b *strings.Builder, name string, s simResult) {
	days := s.runs * simDays
	pct, waste := 0.0, 0.0
	if s.Placed > 0 {
		pct = 100 * s.Moved30 / s.Placed
	}
	if s.resets7 > 0 {
		waste = s.WeeklyWaste / s.resets7
	}
	fmt.Fprintf(b, "| %s | %.2f | %.3f | %.2f | %.2f | %.1f | %.2f | %.1f | %.2f | %.1f |\n", name,
		s.Placed/days, s.Moved30/days, pct, s.Moves/days, s.StoppedMin/days, s.Hits/days, s.PinStopped/days, s.Over80Min/days, waste)
}

func TestSimulatePlacement(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator")
	}
	runs, par := envInt("WY_SIM_RUNS", 1), envInt("WY_SIM_PAR", 2)
	pats := append(append([]simPattern{}, simPatterns...), weekly80s)
	if os.Getenv("WY_SIM_RUNS") == "" {
		pats = []simPattern{simPatterns[0], weekly80s}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d seeded runs per pattern, %d days each, with the 5 pinned coordinators (reserve 10/5), the "+
		"startup burst and the shipped switch (young gate 15 min). Per day per run; moved within 30 min = lanes the "+
		"picker placed that the switch moved off within 30 minutes of their placement.\n\n", runs, simDays)
	totals := map[string]simResult{}
	var names []string
	for _, pat := range pats {
		fmt.Fprintf(&b, "### %s\n\n%s", pat.name, placeHeader)
		for _, st := range placeStrategies {
			sum := runMany(st, pat, runs, par)
			tot, ok := totals[st.name]
			if !ok {
				names = append(names, st.name)
			}
			tot.add(sum)
			totals[st.name] = tot
			writePlaceRow(&b, st.name, sum)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### all patterns\n\n%s", placeHeader)
	for _, n := range names {
		writePlaceRow(&b, n, totals[n])
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("WY_SIM_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
