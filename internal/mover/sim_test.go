package mover

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The simulator (setup.md 8.6, Simulator): seven accounts, one protected, lanes that work in
// turns with idle gaps, helper panels that add bursts, pile-ups, and readings that lag and
// are noisy. Every strategy runs the same seeded workload; the mover passes every 2 minutes.
//
//	WY_SIM_RUNS=300 WY_SIM_PAR=4 WY_SIM_OUT=/tmp/sim.md go test -run TestSimulateStrategies ./internal/mover/

const (
	simDays      = 2
	simRatio     = 7.2
	simPass      = 2   // minutes between mover passes
	simMidTurn   = 3   // busy minutes a lane loses when it is killed mid-turn
	simCostFixed = 0.2 // the true cost of a move, 5-hour points: fixed plus per million context tokens
	simCostMTok  = 1.2
	simCacheTTL  = 5 // minutes
)

// simHolds reports whether the picker holds a new lane of rate off account i: its 5-hour window
// with the running lanes (true rates, the rest of their work) and the new lane for 2 hours
// reaches the limit before it resets.
func simHolds(accs []*simAcc, lanes []*simLane, i, m int, rate float64) bool {
	a := accs[i]
	if a.r5 < 0 {
		return false
	}
	T := float64(a.r5-m) / 60
	lv := a.l5 + a.other*T + rate*math.Min(2, T)
	for _, l := range lanes {
		if l.on && !l.done && !l.stopped && l.acc == i {
			lv += l.rate * 0.8 * math.Min(float64(l.work)/60, T)
		}
	}
	return lv >= 100
}

type laneSpec struct {
	start  int // minute
	work   int // busy minutes
	rate   float64
	burstP float64 // chance a turn starts a helper panel
	group  int     // > 0: a pile-up, the whole group starts on one account
	seed   uint64
	// attended: the owner works in it (each turn starts with input; Attended for 3 minutes).
	attended bool
}

type simPattern struct {
	name      string
	lanes     func(r *rand.Rand) []laneSpec
	lag       [2]int  // reading lag, minutes
	noise     float64 // reading noise, sd in points
	rateNoise float64 // per-minute token noise, relative sd
}

func uniform(r *rand.Rand, lo, hi float64) float64 { return lo + r.Float64()*(hi-lo) }

func spread(r *rand.Rand, n, workLo, workHi int, rateLo, rateHi, burstP float64) []laneSpec {
	var out []laneSpec
	for range n {
		out = append(out, laneSpec{start: r.IntN(simDays*1440 - 120), work: workLo + r.IntN(workHi-workLo+1),
			rate: uniform(r, rateLo, rateHi), burstP: burstP, seed: r.Uint64(), attended: r.Float64() < 0.3})
	}
	return out
}

func pileups(r *rand.Rand, every int, group int) []laneSpec {
	var out []laneSpec
	for t := 60 + r.IntN(every); t < simDays*1440-180; t += every/2 + r.IntN(every) {
		group++
		for range 4 {
			out = append(out, laneSpec{start: t + r.IntN(3), work: 70 + r.IntN(150), rate: uniform(r, 20, 30),
				burstP: 0.1, group: group, seed: r.Uint64()})
		}
	}
	return out
}

var simPatterns = []simPattern{
	{name: "bursts", lag: [2]int{1, 3}, lanes: func(r *rand.Rand) []laneSpec {
		return spread(r, 22, 60, 240, 10, 18, 0.35)
	}},
	{name: "long-opus", lag: [2]int{1, 3}, lanes: func(r *rand.Rand) []laneSpec {
		return spread(r, 15, 240, 600, 16, 26, 0.05)
	}},
	{name: "pile-up", lag: [2]int{1, 3}, lanes: func(r *rand.Rand) []laneSpec {
		return append(pileups(r, 480, 0), spread(r, 6, 60, 240, 10, 18, 0.1)...)
	}},
	{name: "noisy", lag: [2]int{3, 10}, noise: 3, rateNoise: 0.4, lanes: func(r *rand.Rand) []laneSpec {
		return append(append(spread(r, 10, 60, 240, 10, 18, 0.3), spread(r, 6, 240, 600, 16, 26, 0.05)...), pileups(r, 900, 0)...)
	}},
	{name: "mixed", lag: [2]int{1, 4}, noise: 1, rateNoise: 0.2, lanes: func(r *rand.Rand) []laneSpec {
		return append(append(spread(r, 12, 60, 240, 10, 18, 0.3), spread(r, 7, 240, 600, 16, 26, 0.05)...), pileups(r, 900, 0)...)
	}},
}

type simLane struct {
	laneSpec
	id                     string
	acc                    int
	r                      *rand.Rand
	on, done, stopped      bool
	busy, idle, restart    int
	burst                  int
	burstRate, ctx, ewma   float64
	last5                  [5]float64
	age, movedAt, movedFrm int
	idleFor, turnAge       int
}

type simObs struct {
	l5, l7 float64
	r5, r7 int
}

type simAcc struct {
	key       string
	protected bool
	l5, l7    float64
	r5, r7    int // the minute the window resets; r5 -1: not started
	other     float64
	h         []simObs
}

type simResult struct {
	StoppedMin, Hits, Moves, MoveCost, PingPong, ProtectedPts, WeeklyWaste, MidTurn, Waits, NoDest float64
	ReReadMTok, resets7                                                                            float64
}

func (r *simResult) add(o simResult) {
	r.StoppedMin += o.StoppedMin
	r.Hits += o.Hits
	r.Moves += o.Moves
	r.MoveCost += o.MoveCost
	r.PingPong += o.PingPong
	r.ProtectedPts += o.ProtectedPts
	r.WeeklyWaste += o.WeeklyWaste
	r.MidTurn += o.MidTurn
	r.Waits += o.Waits
	r.NoDest += o.NoDest
	r.ReReadMTok += o.ReReadMTok
	r.resets7 += o.resets7
}

var simT0 = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

func simAt(m int) time.Time { return simT0.Add(time.Duration(m) * time.Minute) }

// simulate runs one seeded workload under p. check, when set, sees every pass's input and plan.
func simulate(p Params, pat simPattern, seed uint64, check func(Input, []Move)) simResult {
	gen := rand.New(rand.NewPCG(seed, 1))
	specs := pat.lanes(gen)
	ar := rand.New(rand.NewPCG(seed, 2))
	obsR := rand.New(rand.NewPCG(seed, 3))
	var res simResult
	accs := make([]*simAcc, 7)
	for i := range accs {
		a := &simAcc{key: fmt.Sprintf("claude-%d", i+1), protected: i == 0, r5: -1, l7: uniform(ar, 10, 70),
			r7: ar.IntN(7 * 1440)}
		if ar.Float64() < 0.2 {
			a.l7 = uniform(ar, 80, 94)
		}
		if ar.Float64() < 0.3 {
			a.l5, a.r5 = uniform(ar, 0, 60), 1+ar.IntN(300)
		}
		if ar.Float64() < 0.3 {
			a.other = uniform(ar, 1, 4)
		}
		accs[i] = a
	}
	lanes := make([]*simLane, len(specs))
	byID := map[string]*simLane{}
	for i, s := range specs {
		l := &simLane{laneSpec: s, id: fmt.Sprintf("s%03d", i), r: rand.New(rand.NewPCG(s.seed, 4)), movedFrm: -1,
			ctx: 30_000}
		lanes[i], byID[l.id] = l, l
	}
	accIdx := map[string]int{}
	for i, a := range accs {
		accIdx[a.key] = i
	}
	atLimit := func(a *simAcc) bool { return a.l5 >= 100 || a.l7 >= 100 }
	burnAcc := func(a *simAcc, pts float64, m int) {
		if a.r5 < 0 {
			a.r5 = m + 300
		}
		a.l5 += pts
		a.l7 += pts / simRatio
		if a.protected {
			res.ProtectedPts += pts
		}
	}
	// place is the picker's choice for a new lane: the lowest 5-hour reading among the
	// aggressive accounts with room, else the protected one.
	// It holds new lanes off an account whose running lanes plus the new one would reach the
	// limit before the reset (the 1.3.2 picker's forward-burn check): lever 1, for every strategy.
	place := func(m int, rate float64) int {
		best := -1
		for i, a := range accs {
			if a.protected || a.l5 >= 90 || a.l7 >= 95 || simHolds(accs, lanes, i, m, rate) {
				continue
			}
			if best < 0 || a.l5 < accs[best].l5 {
				best = i
			}
		}
		if best < 0 {
			best = 0
		}
		return best
	}
	groups := map[int]int{}
	var recs []Record
	alpha := 1 - math.Exp(-1.0/10)
	for m := 0; m < simDays*1440; m++ {
		for _, a := range accs {
			if a.r5 >= 0 && m >= a.r5 {
				a.l5, a.r5 = 0, -1
			}
			if m >= a.r7 {
				if !a.protected {
					res.WeeklyWaste += math.Max(0, 100-a.l7)
					res.resets7++
				}
				a.l7, a.r7 = 0, a.r7+7*1440
			}
			if a.other > 0 {
				burnAcc(a, a.other/60, m)
			}
		}
		for _, l := range lanes {
			if l.start == m {
				if l.group > 0 {
					if _, ok := groups[l.group]; !ok {
						groups[l.group] = place(m, 0) // a pile-up: the batch lands on one account
					}
					l.acc = groups[l.group]
				} else {
					l.acc = place(m, l.rate*0.8)
				}
				l.on, l.busy = true, 4+l.r.IntN(20)
			}
			if !l.on || l.done {
				continue
			}
			l.age++
			a := accs[l.acc]
			b := 0.0
			switch {
			case l.restart > 0:
				l.restart--
			case l.stopped && atLimit(a):
				res.StoppedMin++
			default:
				l.stopped = false
				if l.busy == 0 {
					l.idle--
					l.idleFor++
					if l.idle <= 0 {
						l.idleFor, l.turnAge = 0, 0
						l.busy = 4 + l.r.IntN(20)
						if l.r.Float64() < l.burstP {
							l.burst, l.burstRate = 10+l.r.IntN(20), uniform(l.r, 20, 45)
						}
					}
					break
				}
				if atLimit(a) {
					l.stopped = true
					res.Hits++
					res.StoppedMin++
					break
				}
				rate := l.rate * uniform(l.r, 0.8, 1.2)
				if l.burst > 0 {
					rate += l.burstRate
					l.burst--
				}
				b = rate / 60
				l.turnAge++
				burnAcc(a, b, m)
				l.ctx += 6000
				if l.ctx > 800_000 {
					l.ctx = 200_000
				}
				l.work--
				l.busy--
				if l.work <= 0 {
					l.done = true
				} else if l.busy == 0 {
					l.idle = 1 + l.r.IntN(6)
					if l.r.Float64() < 0.15 {
						l.idle = 8 + l.r.IntN(53) // waits for the owner: its cache goes cold
					}
				}
			}
			ob := b * 60
			if pat.rateNoise > 0 && ob > 0 {
				ob = math.Max(0, ob*(1+pat.rateNoise*obsR.NormFloat64()))
			}
			l.ewma += (ob - l.ewma) * alpha
			copy(l.last5[:], l.last5[1:])
			l.last5[4] = ob
		}
		for _, a := range accs {
			a.h = append(a.h, simObs{a.l5, a.l7, a.r5, a.r7})
		}
		if p.Off || m%simPass != 0 {
			continue
		}
		lag := pat.lag[0] + obsR.IntN(pat.lag[1]-pat.lag[0]+1)
		in := Input{Now: simAt(m), Params: p, Recent: recs}
		for _, a := range accs {
			o := a.h[max(0, len(a.h)-1-lag)]
			acc := Account{Key: a.key, Label: a.key, Protected: a.protected, Ratio: simRatio, Other: a.other}
			if o.r5 > m {
				acc.W5 = Window{Level: clamp(o.l5 + pat.noise*obsR.NormFloat64()), Reset: simAt(o.r5)}
			}
			if o.r7 > m {
				acc.W7 = Window{Level: clamp(o.l7 + pat.noise*obsR.NormFloat64()/2), Reset: simAt(o.r7)}
			} else {
				acc.W7 = Window{Reset: simAt(a.r7)}
			}
			in.Accounts = append(in.Accounts, acc)
		}
		for _, l := range lanes {
			if !l.on || l.done {
				continue
			}
			s := Session{ID: l.id, Account: accs[l.acc].key, Rate: l.ewma, Age: time.Duration(l.age) * time.Minute,
				Idle: (l.busy == 0 || l.restart > 0) && !l.stopped, Stopped: l.stopped, Context: l.ctx, Movable: true,
				IdleFor: time.Duration(l.idleFor) * time.Minute, Attended: l.attended && l.busy > 0 && l.turnAge < 3, Model: "opus"}
			if l.burst > 0 {
				avg := 0.0
				for _, x := range l.last5 {
					avg += x / 5
				}
				s.Burst = math.Max(0, avg-l.ewma)
			}
			if l.movedFrm >= 0 {
				s.MovedAt, s.MovedFrom = simAt(l.movedAt), accs[l.movedFrm].key
			}
			in.Sessions = append(in.Sessions, s)
		}
		moves := Plan(in)
		if check != nil {
			check(in, moves)
		}
		for _, mv := range moves {
			if mv.To == "" {
				res.NoDest++
				continue
			}
			if mv.Wait {
				res.Waits++
				continue
			}
			l, to := byID[mv.Session], accIdx[mv.To]
			if l.busy > 0 && !l.stopped {
				res.MidTurn++
				l.work += simMidTurn
			}
			if l.movedFrm == to && m-l.movedAt < 120 {
				res.PingPong++
			}
			l.movedFrm, l.movedAt = l.acc, m
			recs = append(recs, Record{Session: l.id, From: accs[l.acc].key, To: mv.To, At: simAt(m)})
			// The re-read: the whole context uncached, unless its cache is already cold (idle
			// past the 5-minute TTL), when staying would have re-read it too.
			cost := simCostFixed
			if !(l.busy == 0 && l.idleFor >= simCacheTTL) {
				cost += simCostMTok * l.ctx / 1e6
				res.ReReadMTok += l.ctx / 1e6
			}
			burnAcc(accs[to], cost, m)
			res.MoveCost += cost
			l.acc, l.stopped, l.restart = to, false, 1
			res.Moves++
		}
	}
	return res
}

func clamp(x float64) float64 { return math.Min(100, math.Max(0, x)) }

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}

// TestSimulateStrategies compares M0-M4 on every pattern and writes the table to WY_SIM_OUT.
func TestSimulateStrategies(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator")
	}
	runs, par := envInt("WY_SIM_RUNS", 12), envInt("WY_SIM_PAR", 2)
	var b strings.Builder
	fmt.Fprintf(&b, "Mover simulator: %d seeded runs per pattern, %d days each, 7 accounts (claude-1 protected), pass every %d min.\n"+
		"Per day per run: stopped = lane-minutes stopped at a limit (primary), hits = lanes stopped by a limit, cost = 5-hour points spent "+
		"re-reading context after moves (a cold cache costs only the fixed part), re-read Mtok = context tokens re-read uncached by moves, protected = 5-hour points burnt on the protected account, waste = weekly percent unused at a weekly reset (per reset), mid-turn = lanes killed mid-turn, waits = planned waits.\n\n",
		runs, simDays, simPass)
	totals := map[string]simResult{}
	for _, pat := range simPatterns {
		fmt.Fprintf(&b, "### %s\n\n| strategy | stopped | hits | moves | cost | re-read Mtok | ping-pong | protected | waste | mid-turn | waits | no room |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n", pat.name)
		for _, p := range Strategies {
			sum := runMany(p, pat, runs, par)
			tot := totals[p.Name]
			tot.add(sum)
			totals[p.Name] = tot
			writeRow(&b, p.Name, sum, float64(runs*simDays))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### all patterns\n\n| strategy | stopped | hits | moves | cost | re-read Mtok | ping-pong | protected | waste | mid-turn | waits | no room |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, p := range Strategies {
		writeRow(&b, p.Name, totals[p.Name], float64(runs*simDays*len(simPatterns)))
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("WY_SIM_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if m0, m4 := totals[M0.Name], totals[DefaultParams.Name]; m4.StoppedMin >= m0.StoppedMin {
		t.Errorf("the default stops lanes no less than no moves: %.0f >= %.0f", m4.StoppedMin, m0.StoppedMin)
	}
}

func writeRow(b *strings.Builder, name string, s simResult, days float64) {
	waste := 0.0
	if s.resets7 > 0 {
		waste = s.WeeklyWaste / s.resets7
	}
	fmt.Fprintf(b, "| %s | %.1f | %.2f | %.2f | %.2f | %.2f | %.3f | %.1f | %.1f | %.2f | %.2f | %.2f |\n", name, s.StoppedMin/days,
		s.Hits/days, s.Moves/days, s.MoveCost/days, s.ReReadMTok/days, s.PingPong/days, s.ProtectedPts/days, waste, s.MidTurn/days,
		s.Waits/days, s.NoDest/days)
}

func runMany(p Params, pat simPattern, runs, par int) simResult {
	var mu sync.Mutex
	var sum simResult
	var wg sync.WaitGroup
	next := make(chan int)
	for range par {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				r := simulate(p, pat, uint64(1000+i), nil)
				mu.Lock()
				sum.add(r)
				mu.Unlock()
			}
		}()
	}
	for i := range runs {
		next <- i
	}
	close(next)
	wg.Wait()
	return sum
}

// TestPlanInvariants checks every pass of the simulator: no destination whose forecast with
// the moved sessions then hits before its reset (M2-M4), no mid-turn move unless a limit
// stopped it and no session moved twice within the cooldown (M3, M4), and determinism.
func TestPlanInvariants(t *testing.T) {
	runs := envInt("WY_SIM_RUNS", 12)
	if testing.Short() {
		runs = 3
	}
	for _, p := range []Params{M2, M3, M4} {
		for _, pat := range simPatterns {
			for i := range runs {
				fails := 0
				check := func(in Input, moves []Move) {
					if fails > 3 {
						return
					}
					after := append([]Session(nil), in.Sessions...)
					dest := map[string]bool{}
					for _, mv := range moves {
						if mv.To == "" {
							continue
						}
						dest[mv.To] = true
						for j := range after {
							if after[j].ID == mv.Session {
								after[j].Account, after[j].Stopped = mv.To, false
								s := after[j]
								if mv.Wait {
									continue
								}
								if p.Cooldown > 0 && !s.Idle && !in.Sessions[j].Stopped {
									t.Errorf("%s %s run %d %s: %s moved mid-turn", p.Name, pat.name, i, in.Now.Format("15:04"), s.ID)
									fails++
								}
								for _, r := range in.Recent {
									if p.Cooldown > 0 && r.Session == s.ID && in.Now.Sub(r.At) < p.Cooldown {
										t.Errorf("%s %s run %d: %s moved twice within %s", p.Name, pat.name, i, s.ID, p.Cooldown)
										fails++
									}
								}
							}
						}
					}
					for _, a := range in.Accounts {
						if !dest[a.Key] {
							continue
						}
						var on []Session
						for _, s := range after {
							if s.Account == a.Key {
								on = append(on, s)
							}
						}
						if f, hits := p.Forecast(a, on, in.Now, 0).First(); hits {
							t.Errorf("%s %s run %d %s: moved onto %s, whose %s then hits in %s before its reset in %s",
								p.Name, pat.name, i, in.Now.Format("15:04"), a.Key, f.Window, f.HitIn, f.ResetIn)
							fails++
						}
					}
				}
				r1 := simulate(p, pat, uint64(1000+i), check)
				if r2 := simulate(p, pat, uint64(1000+i), nil); r1 != r2 {
					t.Errorf("%s %s run %d: not deterministic: %+v != %+v", p.Name, pat.name, i, r1, r2)
				}
			}
		}
	}
}
