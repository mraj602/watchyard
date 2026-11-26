package mover

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"
)

// The coordinators layer of the simulator (setup.md 8.6, Pinned sessions): five week-long
// pinned sessions that burn little but must not stall, placed as on the owner's worker
// (claude-1 ci-lv1; claude-2 the Slack watcher; claude-3 an orchestrator and a planner;
// claude-7 the OTA planner), on top of every pattern's lanes.
//
//	WY_SIM_RUNS=300 WY_SIM_PAR=8 WY_SIM_OUT=/tmp/pins.md go test -run TestSimulatePins -timeout 3h ./internal/mover/

// coordAccounts are the coordinators' accounts (indexes of claude-1..7).
var coordAccounts = []int{0, 1, 2, 2, 6}

// coordSpec is one coordinator's burn, minute by minute from a day before the run
// (coordHist) to its end, 5-hour points per hour; heavy marks its heavy 5-hour windows.
type coordSpec struct {
	rate   []float64
	heavy  []bool
	burned []float64 // what it burned each minute (the day before: its schedule)
}

const coordHist = 1440

// coordinators draws the coordinators of a run: awake ~17 h a day; a base of 1-5% of a
// 5-hour window per 5 hours awake (60% steady, 40% in 10-minute dispatch bursts); 1-2 heavy
// 5-hour windows a day of 10-20 points each, in 4-8 bursts of 10-20 minutes; contexts of
// 300k-700k tokens. abnormal: the run's second day burns 4-5x.
func coordinators(seed uint64, abnormal bool) []*simLane {
	r := rand.New(rand.NewPCG(seed, 6))
	var out []*simLane
	n := coordHist + simDays*1440
	for k, acc := range coordAccounts {
		c := &coordSpec{rate: make([]float64, n), heavy: make([]bool, n), burned: make([]float64, n)}
		b5 := uniform(r, 1, 5)
		for d := -1; d < simDays; d++ {
			o := coordHist + d*1440
			wake, sleep := o+360+r.IntN(60), o+1380+r.IntN(60)
			sleep = min(sleep, n)
			for t := wake; t < sleep; t++ {
				c.rate[t] += 0.6 * b5 / 5
				if r.Float64() < 0.4*b5/2/300 { // a dispatch burst: 2 points over 10 minutes
					for u := t; u < min(t+10, n); u++ {
						c.rate[u] += 12
					}
				}
			}
			for range 1 + r.IntN(2) {
				st := wake + r.IntN(max(1, sleep-300-wake))
				h := uniform(r, 10, 20)
				type burst struct{ at, n int }
				var bs []burst
				tot := 0
				for range 4 + r.IntN(5) {
					x := burst{st + r.IntN(280), 10 + r.IntN(11)}
					bs = append(bs, x)
					tot += x.n
				}
				for _, x := range bs {
					for u := x.at; u < min(x.at+x.n, n); u++ {
						c.rate[u] += h * 60 / float64(tot)
					}
				}
				for u := st; u < min(st+300, n); u++ {
					c.heavy[u] = true
				}
			}
			if abnormal && d == 1 {
				f := uniform(r, 4, 5)
				for t := o; t < min(o+1440, n); t++ {
					c.rate[t] *= f
				}
			}
		}
		for t := 0; t < coordHist; t++ {
			c.burned[t] = c.rate[t] / 60
		}
		l := &simLane{laneSpec: laneSpec{start: -1, work: 1 << 30, est: 1, seed: r.Uint64()}, id: fmt.Sprintf("c%d", k), acc: acc,
			on: true, ctx: uniform(r, 300_000, 700_000), movedFrm: -1, typed: -1, pendTo: -1, coord: c}
		for t := coordHist - 1; t >= 0; t-- {
			if c.rate[t] > 0 {
				l.lastBurn = t - coordHist // its last burn the day before
				break
			}
		}
		l.r = rand.New(rand.NewPCG(l.seed, 4))
		l.sr = rand.New(rand.NewPCG(l.seed, 5))
		out = append(out, l)
	}
	return out
}

// step is one minute of a coordinator on account a: the points it burns (0 when it has
// nothing to do or a limit stopped it). Once a limit stopped it, it stays stopped (and its
// minutes count) until that window resets: it cannot answer its next event.
func (c *coordSpec) step(res *simResult, l *simLane, a *simAcc, m int, full bool) float64 {
	t := coordHist + m
	want := c.rate[t]
	l.busy = 0
	if full && (l.stopped || want > 0) {
		if !l.stopped {
			res.PinHits++
			res.Hits++
			if a.l7 >= 100 {
				res.PinHits7d++
			}
		}
		l.stopped = true
		res.PinStopped++
		res.StoppedMin++
		if c.heavy[t] {
			res.PinStoppedHeavy++
		}
		return 0
	}
	l.stopped = false
	if want <= 0 {
		l.idleFor++
		return 0
	}
	l.busy, l.idleFor, l.lastBurn = 1, 0, m
	c.burned[t] = want / 60
	res.CoordBurn += want / 60
	return want / 60
}

// coordReserve sets each account's live pins and reserve at minute m: a pin is live unless
// it burned nothing for PinIdle. Flat: pins × PinReserve5/7. Measured: the sum of its live
// pins' heaviest 5-hour window of the last day, and a day of their burn on the weekly window.
func coordReserve(st simStrat, lanes []*simLane, m int, rsv5, rsv7 []float64, pins []int) {
	if !st.measured || m%10 == 0 {
		for i := range rsv5 {
			rsv5[i], rsv7[i], pins[i] = 0, 0, 0
		}
	} else {
		for i := range pins {
			pins[i] = 0
		}
	}
	for _, l := range lanes {
		if l.coord == nil {
			continue
		}
		if float64(m-l.lastBurn) >= PinIdle.Minutes() {
			continue
		}
		pins[l.acc]++
		if !st.measured {
			continue
		}
		if m%10 != 0 {
			continue
		}
		t := coordHist + m
		best, sum, day := 0.0, 0.0, 0.0
		for u := t - 1440; u < t; u++ {
			sum += l.coord.burned[u]
			if u-300 >= t-1440 {
				sum -= l.coord.burned[u-300]
			}
			best = math.Max(best, sum)
			day += l.coord.burned[u]
		}
		rsv5[l.acc] += best
		rsv7[l.acc] += day / simRatio
	}
	if !st.measured {
		for i := range pins {
			rsv5[i], rsv7[i] = st.p.PinReserve(pins[i])
		}
	}
}

// displaced counts the moves of a pass that only the reserve caused: their source would be
// under its caps without it (and they were not stopped at a limit).
func displaced(in Input, r Result) float64 {
	accs := map[string]Account{}
	for _, a := range in.Accounts {
		accs[a.Key] = a
	}
	byAcc := map[string][]Session{}
	for _, s := range in.Sessions {
		byAcc[s.Account] = append(byAcc[s.Account], s)
	}
	n := 0.0
	for _, mv := range r.Moves {
		a := accs[mv.From]
		if mv.To == "" || mv.Stopped || (a.Reserve5 == 0 && a.Reserve7 == 0) {
			continue
		}
		b := a
		b.Reserve5, b.Reserve7 = 0, 0
		if in.Params.Project(b, byAcc[b.Key], 0, in.Now).Window == "" {
			n++
		}
	}
	return n
}

func pinsStrat(name string, pinned bool, k5, k7 float64) simStrat {
	s := v2(name, func(p *Params) { p.PinReserve5, p.PinReserve7 = k5, k7 })
	s.coord, s.pinned = true, pinned
	return s
}

// pinStrategies: V2 as it was (coordinators movable), pinned with no reserve, the flat
// reserve grid, one measured reserve; then the abnormal-day rows.
var pinStrategies = func() []simStrat {
	out := []simStrat{
		pinsStrat("V2 today (pins movable)", false, 0, 0),
		pinsStrat("pins no-move, no reserve", true, 0, 0),
	}
	for _, k5 := range []float64{10, 15, 20} {
		for _, k7 := range []float64{3, 5} {
			out = append(out, pinsStrat(fmt.Sprintf("pins + reserve %.0f/%.0f", k5, k7), true, k5, k7))
		}
	}
	meas := pinsStrat("pins + measured reserve", true, 0, 0)
	meas.measured = true
	out = append(out, meas)
	for _, s := range []simStrat{out[1], out[2], out[4], out[6]} {
		s.ownerActs = true
		s.name += " + owner moves on alert (30 min)"
		out = append(out, s)
	}
	for _, s := range []simStrat{out[0], out[1], out[2], out[4], out[5], out[7]} {
		s.abnormal = true
		s.name = "abnormal day: " + s.name
		out = append(out, s)
	}
	// The scaled reserve and the drain ramp (setup.md 8.5 Drain aim, 8.6 Pinned sessions; owner
	// 2026-11-25), on the default 10/5, normal and abnormal days.
	var base simStrat
	for _, s := range out {
		if s.name == "pins + reserve 10/5" {
			base = s
		}
	}
	for _, ab := range []bool{false, true} {
		for _, v := range []struct {
			name         string
			scaled, ramp bool
		}{{"scaled", true, false}, {"drain ramp", false, true}, {"scaled + drain ramp", true, true}} {
			s := base
			s.scaled, s.ramp, s.abnormal = v.scaled, v.ramp, ab
			s.name = base.name + ", " + v.name
			if ab {
				s.name = "abnormal day: " + s.name
			}
			out = append(out, s)
		}
	}
	return out
}()

const pinsHeader = "| strategy | pinned stopped min | of which heavy windows | pinned hits (weekly / lanes on its account) | pinned stopped min, no room anywhere | all stopped min | hits | moves | coordinator moves | re-read Mtok | moves by reserve | lanes placed elsewhere by reserve | waste | prot 5h max | prot 7d max | owner alerts | owner moves | coordinator weekly %/week | lanes stopped at the weekly limit |\n" +
	"|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n"

func writePinsRow(b *strings.Builder, name string, s simResult) {
	days := s.runs * simDays
	waste := 0.0
	if s.resets7 > 0 {
		waste = s.WeeklyWaste / s.resets7
	}
	fmt.Fprintf(b, "| %s | %.1f | %.1f | %.3f (%.3f / %.3f) | %.1f | %.1f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.1f | %.1f | %.1f | %.3f | %.3f | %.1f | %.3f |\n", name,
		s.PinStopped/days, s.PinStoppedHeavy/days, s.PinHits/days, s.PinHits7d/days, s.PinHitsShared/days, s.PinStoppedNoRoom/days, s.StoppedMin/days, s.Hits/days, s.Moves/days, s.CoordMoves/days,
		s.ReReadMTok/days, s.DisplacedMoves/days, s.DisplacedPlaces/days, waste, s.MaxProt5/s.runs, s.MaxProt7/s.runs, s.Alerts/days,
		s.OwnerMoves/days, s.CoordBurn/days/float64(len(coordAccounts))*7/simRatio, s.Hits7d/days)
}

// TestSimulatePins compares the pinned-session strategies on every pattern with the
// coordinators layer and writes the tables to WY_SIM_OUT. Without WY_SIM_RUNS: a smoke run (1
// seed, 2 patterns, 3 strategies).
func TestSimulatePins(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator")
	}
	runs, par := envInt("WY_SIM_RUNS", 1), envInt("WY_SIM_PAR", 2)
	pats, strats := simPatterns, pinStrategies
	if os.Getenv("WY_SIM_RUNS") == "" {
		pats, strats = []simPattern{simPatterns[0], simPatterns[4]}, []simStrat{pinStrategies[0], pinStrategies[1], pinStrategies[4]}
	}
	if f := os.Getenv("WY_SIM_PINS"); f != "" { // only the strategies whose name contains it
		var keep []simStrat
		for _, s := range strats {
			if strings.Contains(s.name, f) {
				keep = append(keep, s)
			}
		}
		strats = keep
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pinned coordinators: %d seeded runs per pattern, %d days each; 5 coordinators (claude-1 ci-lv1, claude-2 Slack watcher, claude-3 orchestrator + planner, claude-7 OTA planner) on top of each pattern's lanes.\n"+
		"Per day per run: pinned stopped min = coordinator-minutes stopped at a limit (primary; from a limit hit until that window resets); of which heavy windows = during their 10-20%% windows; "+
		"pinned hits = coordinators stopped by a limit; all stopped min and hits include them; coordinator moves = moves of coordinators (only when they are movable); "+
		"moves by reserve = moves whose source was under its caps without the reserve; lanes placed elsewhere by reserve = new lanes the picker put on another account because of the reserve; "+
		"owner alerts = distinct alerts that a pinned session alone takes its account to 100 within 2 h.\n\n", runs, simDays)
	totals := map[string]simResult{}
	var names []string
	for _, pat := range pats {
		fmt.Fprintf(&b, "### %s\n\n%s", pat.name, pinsHeader)
		for _, st := range strats {
			sum := runMany(st, pat, runs, par)
			tot, ok := totals[st.name]
			if !ok {
				names = append(names, st.name)
			}
			tot.add(sum)
			totals[st.name] = tot
			writePinsRow(&b, st.name, sum)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### all patterns\n\n%s", pinsHeader)
	for _, n := range names {
		writePinsRow(&b, n, totals[n])
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("WY_SIM_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPinInvariants checks every pass with pins: no pinned session moves, every account's
// caps are its caps less its reserve, destinations keep their reserve free (the shared
// destination check of invariantRun), the same seed gives the same result.
func TestPinInvariants(t *testing.T) {
	runs := envInt("WY_SIM_RUNS", 1)
	pats := simPatterns
	if os.Getenv("WY_SIM_RUNS") == "" || testing.Short() {
		runs, pats = 1, []simPattern{simPatterns[0]}
	}
	for _, st := range []simStrat{pinStrategies[1], pinStrategies[4], pinStrategies[8]} {
		for _, pat := range pats {
			for i := range runs {
				for _, e := range invariantRun(st, pat, i) {
					t.Error(e)
				}
				p := st.p
				check := func(in Input, r Result) {
					pinned := map[string]bool{}
					for _, s := range in.Sessions {
						pinned[s.ID] = s.Pinned
					}
					for _, mv := range r.Moves {
						if pinned[mv.Session] {
							t.Errorf("%s %s: pinned %s moved", st.name, pat.name, mv.Session)
						}
					}
					for k, a := range in.Accounts {
						pr := r.Accounts[k]
						c5, c7 := p.caps(a, in.Now)
						if math.Abs(pr.Cap5-math.Max(0, c5-a.Reserve5)) > 1e-9 || math.Abs(pr.Cap7-math.Max(0, c7-a.Reserve7)) > 1e-9 {
							t.Errorf("%s: caps %v/%v, want %v-%v / %v-%v", a.Key, pr.Cap5, pr.Cap7, c5, a.Reserve5, c7, a.Reserve7)
						}
						if !st.measured {
							w5, w7 := p.PinReserve(a.Pins)
							if a.Reserve5 != w5 || a.Reserve7 != w7 {
								t.Errorf("%s: reserve %v/%v with %d pins", a.Key, a.Reserve5, a.Reserve7, a.Pins)
							}
						}
					}
				}
				simulate(st, pat, uint64(1000+i), check)
			}
		}
	}
}

// A pinned session alone over its account: an alert, no move; a non-pinned burner on the
// same account moves to keep the reserve free.
func TestPinReserveMovesTheOthers(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	p := DefaultParams
	p.PinReserve5, p.PinReserve7 = 15, 3 // not the defaults: the numbers below
	p.DestHorizon = 0                    // destinations need room to their resets
	r5, r7 := p.PinReserve(1)
	in := Input{Now: now, Params: p, Accounts: []Account{
		{Key: "acct:a/o", Label: "a", LoggedIn: true, W5: Window{Level: 60, Reset: now.Add(3 * time.Hour)}, W7: Window{Level: 40, Reset: now.Add(72 * time.Hour)},
			Pins: 1, Reserve5: r5, Reserve7: r7},
		{Key: "acct:b/o", Label: "b", LoggedIn: true, W5: Window{Level: 5, Reset: now.Add(4 * time.Hour)}, W7: Window{Level: 10, Reset: now.Add(100 * time.Hour)}},
	}, Sessions: []Session{
		{ID: "pin", Account: "acct:a/o", Rate: 1, Busy: true, Pinned: true, Movable: true},
		{ID: "lane", Account: "acct:a/o", Rate: 6, Busy: true, Movable: true},
	}, Streak: map[string]int{"acct:a/o 5h": 5, "acct:a/o 7d": 5}}
	// Without the reserve: 60 + 7 × 3 = 81 < 95, nothing moves.
	noRes := in
	noRes.Accounts = append([]Account(nil), in.Accounts...)
	noRes.Accounts[0].Reserve5, noRes.Accounts[0].Reserve7 = 0, 0
	if r := Plan(noRes); len(r.Moves) != 0 {
		t.Fatalf("moves without the reserve: %+v", r.Moves)
	}
	// With it the cap is 95 - 15 = 80: the lane moves, the pin stays.
	r := Plan(in)
	if len(r.Moves) != 1 || r.Moves[0].Session != "lane" || r.Moves[0].To != "acct:b/o" {
		t.Fatalf("moves %+v", r.Moves)
	}
	if r.Accounts[0].Cap5 != 80 || r.Accounts[0].Reserve5 != 15 || r.Accounts[0].Pins != 1 {
		t.Fatalf("projection %+v", r.Accounts[0])
	}
	if len(r.Alerts) != 0 {
		t.Fatalf("alerts %+v", r.Alerts)
	}
	// The pin alone at 30 points/h from 70: 100 in 1 h, before the reset: one alert, no move.
	in.Accounts[0].W5.Level = 70
	in.Sessions = []Session{{ID: "pin", Account: "acct:a/o", Rate: 30, Busy: true, Pinned: true, Movable: true}}
	r = Plan(in)
	if len(r.Moves) != 0 || len(r.Alerts) != 1 || r.Alerts[0].Session != "pin" || r.Alerts[0].Window != Win5h || r.Alerts[0].In != time.Hour {
		t.Fatalf("moves %+v alerts %+v", r.Moves, r.Alerts)
	}
	// A destination keeps its own reserve free: b with 4 pins (cap 95 - 60 = 35) cannot take
	// 6 points/h for 4 hours from 5 (29 + 5 hysteresis > 35 - ... ).
	in.Accounts[0].W5.Level = 60
	in.Sessions = []Session{{ID: "lane", Account: "acct:a/o", Rate: 8, Busy: true, Movable: true}}
	in.Accounts[1].Pins, in.Accounts[1].Reserve5, in.Accounts[1].Reserve7 = 4, 60, 12
	r = Plan(in)
	if len(r.Moves) != 1 || r.Moves[0].To != "" {
		t.Fatalf("moved into a reserve: %+v", r.Moves)
	}
}
