package picker

// Policy comparison: rr_exp (ReferenceLaneParams, shipped before), the draft (DraftLaneParams,
// fill_exp) and v3 (DefaultLaneParams, the drain order).
//
// 1. Golden replay: every call of testdata/lanes_golden.json.gz (121 calls, 11-14 to 11-20)
//    ranked under each policy on its recorded state (each call sees the recorded levels, not the
//    policy's own earlier picks, so waste is measured by the simulator, not here).
// 2. Week simulator: seven accounts (1/5/7 protected), minute steps over 7 days, demand
//    patterns, readings every 5 minutes from the simulated use (2 minutes late, noise ±0.3,
//    rounded), the owner's own use of the protected accounts in day hours, three 2-hour hub
//    outages, lanes that burn their cost evenly over their length and stop at a limit (100),
//    lane statistics measured from the finished lanes, lanes waiting over an hour dropped.
//    WY_SIM_RUNS sets the runs per pattern (default 12; the report used 300), WY_SIM_PAR the
//    parallel runs, WY_SIM_OUT a file for the table; -short skips it.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type policy struct {
	name string
	p    LaneParams
}

// The strategies compared (owner change 2, D). S5 is S3 with protected accounts purely last:
// no protected drain (their expiring room) ahead of the aggressive accounts.
var (
	s2Params = func() LaneParams { p := V3LaneParams; p.NoStall = true; return p }()
	s3Params = func() LaneParams { p := DefaultLaneParams; p.EstFloor = false; return p }()
	s4Params = func() LaneParams { p := s3Params; p.ProtSoft5h = 90; return p }()
	s5Params = func() LaneParams { p := s3Params; p.ProtDrainSlack = 0; return p }()
)

var policies = []policy{{"S0 rr_exp", ReferenceLaneParams}, {"S1 v3", V3LaneParams}, {"S2 no-stall", s2Params},
	{"S3 no-stall+est", s3Params}, {"S4 S3, protected before aggr. at 5h 90%", s4Params},
	{"S5 S3, protected purely last", s5Params}, {"S6 S3 + cost floor (default)", DefaultLaneParams}}

// ---------------------------------------------------------------- golden replay

type replayMetrics struct {
	picks, prot, protDrain, protWhileRoom, waits, expPicks, wallPicks int
}

func TestLanesGoldenReplay(t *testing.T) {
	d := loadGolden(t)
	out := map[string]*replayMetrics{}
	for _, c := range d.Cases {
		in := goldenInput(t, d, c)
		if c.Want.Error != "" {
			continue
		}
		for _, pol := range policies {
			in.Params = pol.p
			r, err := RankLanes(in)
			if err != nil {
				continue
			}
			m := out[pol.name]
			if m == nil {
				m = &replayMetrics{}
				out[pol.name] = m
			}
			m.picks += len(r.Picks)
			m.waits += r.Waiting
			for _, pk := range r.Picks {
				acc := accOf(in, pk.Key)
				row := rowOf(r, pk.Key)
				if row.HoursToReset <= 24 && !acc.Protected {
					m.expPicks++
				}
				if row.Weekly >= 98 || row.FiveHour >= 98 {
					m.wallPicks++
				}
				if acc.Protected && pk.Drain {
					m.prot++
					m.protDrain++
				} else if acc.Protected {
					m.prot++
					for _, x := range r.Table {
						if !x.Protected && x.Weekly < 90 && x.FiveHour < 80 && len(x.Unknown) == 0 && x.Burst < pol.p.BurstAggr {
							m.protWhileRoom++
							break
						}
					}
				}
			}
		}
	}
	var lines []string
	lines = append(lines, "| policy | picks | to protected | of them protected drain (room that expires) | protected, not drain, while an aggressive account had room | waiting lanes | picks on aggressive accounts resetting within 24 h | picks at a 98 level |",
		"|---|---|---|---|---|---|---|---|")
	for _, pol := range policies {
		m := out[pol.name]
		lines = append(lines, fmt.Sprintf("| %s | %d | %d | %d | %d | %d | %d | %d |", pol.name, m.picks, m.prot, m.protDrain, m.protWhileRoom,
			m.waits, m.expPicks, m.wallPicks))
	}
	t.Logf("golden replay (%d calls):\n%s", len(d.Cases), strings.Join(lines, "\n"))
	for _, name := range []string{"S1 v3", "S6 S3 + cost floor (default)"} {
		if v, rr := out[name], out["S0 rr_exp"]; v.protWhileRoom > rr.protWhileRoom {
			t.Errorf("%s uses protected accounts more than rr_exp: %+v vs %+v", name, *v, *rr)
		}
	}
	if path := os.Getenv("WY_REPLAY_OUT"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
}

// ---------------------------------------------------------------- week simulator

type simDemand struct {
	at    int // minute
	lanes []simSpec
}

type simSpec struct {
	lenMin, cost float64 // minutes; 5-hour points over the whole lane
	est          LaneExpect
}

type simLane struct {
	acc            int
	start          int
	spec           simSpec
	burned, recent float64
	recentLog      []float64 // burn per minute, last 15
	done, hit      bool
	postReset      bool
	drain          bool
	estDur, estR   float64 // the pick's estimate (hours, points per hour) for its lease
	startH         float64
}

type simAcc struct {
	key      string
	prot     bool
	rho      float64
	w, h     float64
	winStart int // -1: no 5-hour window open
	reset    int // minute of the next weekly reset
	cum      []float64
	hist     []struct {
		w, h float64
		win  int
	}
	lastRead  int
	ownerRate float64 // 5-hour points per minute of the owner's own use (day hours)
}

type simResult struct {
	wasteAggr, wasteProt          float64
	protLanes, lanes, drainLanes  int
	spillLanes                    int
	spillPts                      float64
	hits, waits, dropped, overCap int
	hitW, hitDrain, hitProt       int
	delayed                       int   // lanes not placed the minute they came
	waitMins                      []int // per lane (placed, or 60 when dropped)
	hitStartH                     []float64
}

const simMinutes = 7 * 24 * 60

func lognorm(rng *rand.Rand, median, p75 float64) float64 {
	sigma := math.Log(p75/median) / 0.6745
	return median * math.Exp(sigma*rng.NormFloat64())
}

// Estimate error levels (owner change 2, C): "exact"; "typical": 30% give none, the rest are off
// by a log-normal factor (median 1, σ 0.5) and 15% of those say 3-4x too short; "bad": 60% none,
// σ 1, 30% of the rest 3-4x too short. Agents give a class and their minutes.
func simEstimates(seed int64, demands []simDemand, level string, p LaneParams) {
	rng := rand.New(rand.NewSource(seed ^ 0x5eed))
	none, sigma, under := 0.0, 0.0, 0.0
	switch level {
	case "typical":
		none, sigma, under = 0.3, 0.5, 0.15
	case "bad":
		none, sigma, under = 0.6, 1.0, 0.3
	}
	for i := range demands {
		for j := range demands[i].lanes {
			l := &demands[i].lanes[j]
			if rng.Float64() < none {
				l.est = LaneExpect{}
				continue
			}
			f := math.Exp(sigma * rng.NormFloat64())
			if rng.Float64() < under {
				f = 1 / (3 + rng.Float64())
			}
			d := time.Duration(math.Max(1, l.lenMin*f) * float64(time.Minute))
			l.est = LaneExpect{Class: p.classOf(d), Dur: d}
		}
	}
}

// simDemands makes the demand of one run (the same for every policy).
func simDemands(rng *rand.Rand, pattern string) []simDemand {
	var out []simDemand
	small := func() simSpec { return simSpec{lenMin: 5 + rng.Float64()*10, cost: 0.5 + rng.Float64()*1.5} }
	realLane := func() simSpec {
		if rng.Float64() < 0.15 {
			return simSpec{lenMin: 60 + rng.Float64()*120, cost: 3 + rng.Float64()*12}
		}
		return simSpec{lenMin: math.Max(3, lognorm(rng, 12, 17)), cost: math.Max(0.5, lognorm(rng, 4.3, 8))}
	}
	day := func(m int) bool { h := (m / 60) % 24; return h >= 3 && h < 18 } // 08:30-23:30 IST
	for m := 0; m < simMinutes; m++ {
		var lanes []simSpec
		switch pattern {
		case "steady-small":
			if m%5 == 0 {
				for i := 0; i < 1+rng.Intn(3); i++ {
					lanes = append(lanes, small())
				}
			}
		case "bursts-of-10":
			if m%40 == 0 {
				for i := 0; i < 10; i++ {
					lanes = append(lanes, small())
				}
			}
		case "long-lanes":
			if m%30 == 0 {
				lanes = append(lanes, simSpec{lenMin: 60 + rng.Float64()*180, cost: 10 + rng.Float64()*20})
			}
		case "night-gaps":
			if day(m) && m%4 == 0 {
				for i := 0; i < 1+rng.Intn(3); i++ {
					lanes = append(lanes, small())
				}
			}
		case "mixed-real":
			p := 1.0 / 25
			if !day(m) {
				p = 1.0 / 120
			}
			if rng.Float64() < p {
				for i := 0; i < 1+rng.Intn(6); i++ {
					lanes = append(lanes, realLane())
				}
			}
		}
		if len(lanes) > 0 {
			out = append(out, simDemand{at: m, lanes: lanes})
		}
	}
	return out
}

var simT0 = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

func simTime(m int) time.Time { return simT0.Add(time.Duration(m) * time.Minute) }

// simRun runs one week under one policy.
func simRun(seed int64, pattern string, pol LaneParams) simResult {
	return simRunErr(seed, pattern, pol, "typical")
}

func simRunErr(seed int64, pattern string, pol LaneParams, errLevel string) simResult {
	rng := rand.New(rand.NewSource(seed))
	demands := simDemands(rng, pattern)
	simEstimates(seed, demands, errLevel, DefaultLaneParams)
	var accs []*simAcc
	prot := []bool{true, false, false, false, true, false, true}
	for i := 0; i < 7; i++ {
		a := &simAcc{key: fmt.Sprintf("claude-%d", i+1), prot: prot[i], rho: 6 + rng.Float64()*3, winStart: -1,
			reset: rng.Intn(simMinutes), cum: make([]float64, simMinutes+1), lastRead: -100}
		frac := 1 - float64(a.reset)/float64(simMinutes) // share of its week already gone
		if a.prot {
			a.ownerRate = (40 + rng.Float64()*40) * a.rho / (15 * 7 * 60) // 40-80% of a week in day hours
			a.w = frac * (40 + rng.Float64()*40)
		} else {
			a.w = frac * (20 + rng.Float64()*80)
		}
		accs = append(accs, a)
	}
	hubDown := map[int]bool{}
	for s := 0; s < 3; s++ { // three 2-hour hub outages
		st := rng.Intn(simMinutes - 120)
		for m := st; m < st+120; m++ {
			hubDown[m] = true
		}
	}
	f5 := DefaultLaneParams.F5
	var lanes []*simLane
	var res simResult
	type queued struct {
		spec  simSpec
		since int
		wait  bool
	}
	var queue []queued
	di := 0
	var costs, lens []float64
	reading := func(a *simAcc, m int) (LaneSample, LaneMeter) {
		lag := 2
		at := max(0, a.lastRead-lag)
		st := a.hist[min(at, len(a.hist)-1)]
		w := math.Min(100, math.Round(st.w))
		h := math.Min(100, math.Round(st.h))
		s := LaneSample{TS: simTime(a.lastRead), Weekly: &w, FiveHour: &h, WeeklyReset: simTime(a.reset)}
		if st.win >= 0 {
			s.FiveHourReset = simTime(st.win + 300)
		}
		units := (a.cum[m] - a.cum[at]) / f5
		return s, LaneMeter{M5: units, M7: units, KeepH: st.win >= 0 && m < st.win+300}
	}
	for m := 0; m < simMinutes; m++ {
		// use this minute
		for _, a := range accs {
			if a.winStart >= 0 && m >= a.winStart+300 {
				a.h, a.winStart = 0, -1
			}
			if m >= a.reset {
				waste := math.Max(0, 98-a.w)
				if a.prot {
					res.wasteProt += waste
				} else {
					res.wasteAggr += waste
				}
				for _, l := range lanes {
					if accs[l.acc] == a && !l.done && !l.postReset {
						l.postReset = true
						res.spillLanes++
					}
				}
				a.w = 0
				a.reset += 7 * 24 * 60
			}
			a.cum[m+1] = a.cum[m]
			if a.prot && ((m/60)%24 >= 3 && (m/60)%24 < 18) && a.w < 99 && a.h < 99 {
				a.add(m, a.ownerRate)
			}
		}
		for _, l := range lanes {
			if l.done {
				continue
			}
			a := accs[l.acc]
			if a.w >= 100 || a.h >= 100 {
				l.done, l.hit = true, true
				res.hits++
				if a.w >= 100 {
					res.hitW++
				}
				if l.drain {
					res.hitDrain++
				}
				if a.prot {
					res.hitProt++
				}
				res.hitStartH = append(res.hitStartH, l.startH)
				if os.Getenv("WY_SIM_DEBUG") != "" {
					fmt.Printf("hit m=%d %s w=%.1f h=%.1f drain=%v lane=%+v started=%d\n", m, a.key, a.w, a.h, l.drain, l.spec, l.start)
				}
				continue
			}
			b := l.spec.cost / l.spec.lenMin
			a.add(m, b)
			a.cum[m+1] += b
			l.burned += b
			if l.postReset {
				res.spillPts += b
			}
			l.recentLog = append(l.recentLog, b)
			if len(l.recentLog) > 15 {
				l.recentLog = l.recentLog[1:]
			}
			if float64(m-l.start+1) >= l.spec.lenMin {
				l.done = true
				costs = append(costs, l.burned)
				lens = append(lens, l.spec.lenMin)
			}
		}
		for _, a := range accs {
			a.hist = append(a.hist, struct {
				w, h float64
				win  int
			}{a.w + rng.NormFloat64()*0.3, a.h + rng.NormFloat64()*0.3, a.winStart})
			if m-a.lastRead >= 5 {
				a.lastRead = m
			}
		}
		// demand
		for di < len(demands) && demands[di].at == m {
			for _, s := range demands[di].lanes {
				queue = append(queue, queued{spec: s, since: m})
			}
			di++
		}
		if len(queue) == 0 || (m%5 != 0 && queue[len(queue)-1].since != m) {
			continue
		}
		// drop lanes waiting over an hour
		kept := queue[:0]
		for _, q := range queue {
			if m-q.since > 60 {
				res.dropped++
				res.delayed++
				res.waitMins = append(res.waitMins, 60)
				continue
			}
			kept = append(kept, q)
		}
		queue = kept
		if len(queue) == 0 {
			continue
		}
		in := LaneInput{Now: simTime(m), Params: pol, N: min(len(queue), 50), HubDown: hubDown[m]}
		for _, q := range queue[:in.N] {
			in.Expects = append(in.Expects, q.spec.est)
		}
		for i, a := range accs {
			s, mt := reading(a, m)
			in.Accounts = append(in.Accounts, LaneAccount{Key: a.key, Ord: i, Protected: a.prot, Ratio: a.rho, Sample: s, Meter: mt})
		}
		for _, l := range lanes {
			if m-l.start > 8*60 {
				continue
			}
			rec := 0.0
			for _, x := range l.recentLog {
				rec += x
			}
			in.Leases = append(in.Leases, LaneLease{Key: accs[l.acc].key, T0: simTime(l.start), Launched: true, Ended: l.done,
				Burned: l.burned / f5, Seen: l.burned > 0, Recent: rec / f5, RecentOK: true,
				Expect: hoursDur(l.estDur), EstRate: l.estR})
		}
		if len(costs) >= 10 {
			cs := append([]float64(nil), costs...)
			ls := append([]float64(nil), lens...)
			if len(cs) > 300 {
				cs, ls = cs[len(cs)-300:], ls[len(ls)-300:]
			}
			sort.Float64s(cs)
			sort.Float64s(ls)
			sum := 0.0
			for _, c := range cs {
				sum += c
			}
			in.Stats = LaneStats{Cost: quantile(cs, 0.75), Tail: quantile(cs, 0.95), Mean: sum / float64(len(cs)), Len: time.Duration(quantile(ls, 0.5) * float64(time.Minute)), N: len(cs), NLen: len(ls)}
		}
		r, err := RankLanes(in)
		if err != nil {
			panic(err)
		}
		for i, pk := range r.Picks {
			q := queue[i]
			idx := 0
			for j, a := range accs {
				if a.key == pk.Key {
					idx = j
				}
			}
			if accs[idx].w+q.spec.cost/accs[idx].rho > 100 {
				res.overCap++
			}
			sl := &simLane{acc: idx, start: m, spec: q.spec, drain: pk.Drain, startH: accs[idx].h}
			if pol.UseEstimate && (q.spec.est != LaneExpect{}) {
				e := pol.estimate(q.spec.est, in.Accounts[idx], in.Stats)
				sl.estDur, sl.estR = e.dur, e.rate()
			}
			lanes = append(lanes, sl)
			res.waitMins = append(res.waitMins, m-q.since)
			if m > q.since {
				res.delayed++
			}
			if os.Getenv("WY_SIM_DEBUG") != "" && (accs[idx].h > 85 || accs[idx].w > 90) {
				fmt.Printf("pick m=%d %s w=%.1f h=%.1f drain=%v class=%s row=%+v\n", m, pk.Key, accs[idx].w, accs[idx].h, pk.Drain, pk.Class, rowOf(r, pk.Key).Drain)
			}
			res.lanes++
			if accs[idx].prot {
				res.protLanes++
			}
			if pk.Drain {
				res.drainLanes++
			}
			if q.since == m && r.Waiting > 0 {
				// placed at once
			}
		}
		rest := queue[len(r.Picks):]
		for i := range rest {
			if !rest[i].wait {
				rest[i].wait = true
				res.waits++
			}
		}
		queue = append([]queued(nil), rest...)
		// forget finished lanes older than 8 hours
		if m%60 == 0 {
			k := lanes[:0]
			for _, l := range lanes {
				if !(l.done && m-l.start > 8*60) {
					k = append(k, l)
				}
			}
			lanes = k
		}
	}
	return res
}

func (a *simAcc) add(m int, pts5 float64) {
	if a.winStart < 0 {
		a.winStart = m
	}
	a.h = math.Min(100, a.h+pts5)
	a.w = math.Min(100, a.w+pts5/a.rho)
}

func simPar() int {
	if v, err := strconv.Atoi(os.Getenv("WY_SIM_PAR")); err == nil && v > 0 {
		return v
	}
	return 2
}

func TestLanesWeekSimulator(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator: not with -short")
	}
	runs := 12
	if v, err := strconv.Atoi(os.Getenv("WY_SIM_RUNS")); err == nil && v > 0 {
		runs = v
	}
	patterns := []string{"steady-small", "bursts-of-10", "long-lanes", "night-gaps", "mixed-real"}
	errLevel := "typical"
	if v := os.Getenv("WY_SIM_ERR"); v != "" {
		errLevel = v
	}
	pols := policies
	if v := os.Getenv("WY_SIM_POLICIES"); v != "" { // e.g. "S3,S6": names starting with these
		pols = nil
		for _, pol := range policies {
			for _, w := range strings.Split(v, ",") {
				if strings.HasPrefix(pol.name, w+" ") {
					pols = append(pols, pol)
				}
			}
		}
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("week simulator, %d runs per pattern, estimates %s (means per run; waits in minutes over all lanes)", runs, errLevel),
		"| pattern | strategy | mean wait | p95 wait | lanes delayed | dropped (> 1 h) | weekly pts wasted, aggressive | wasted, protected | limit hits | lanes on protected | lanes past a reset | 5h pts past a reset | lanes |",
		"|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	type agg struct {
		s     simResult
		waits []int
	}
	for _, pat := range patterns {
		res := map[string]*agg{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, simPar())
		for i := 0; i < runs; i++ {
			for _, pol := range pols {
				wg.Add(1)
				sem <- struct{}{}
				go func(i int, pol policy) {
					defer wg.Done()
					defer func() { <-sem }()
					r := simRunErr(int64(1000+i), pat, pol.p, errLevel)
					mu.Lock()
					defer mu.Unlock()
					a := res[pol.name]
					if a == nil {
						a = &agg{}
						res[pol.name] = a
					}
					s := &a.s
					s.wasteAggr += r.wasteAggr
					s.wasteProt += r.wasteProt
					s.protLanes += r.protLanes
					s.lanes += r.lanes
					s.spillLanes += r.spillLanes
					s.spillPts += r.spillPts
					s.hits += r.hits
					s.dropped += r.dropped
					s.delayed += r.delayed
					a.waits = append(a.waits, r.waitMins...)
				}(i, pol)
			}
		}
		wg.Wait()
		n := float64(runs)
		for _, pol := range pols {
			a := res[pol.name]
			s := a.s
			sort.Ints(a.waits)
			sum := 0
			for _, w := range a.waits {
				sum += w
			}
			mean, p95 := 0.0, 0
			if len(a.waits) > 0 {
				mean, p95 = float64(sum)/float64(len(a.waits)), a.waits[int(0.95*float64(len(a.waits)-1))]
			}
			lines = append(lines, fmt.Sprintf("| %s | %s | %.2f | %d | %.1f | %.1f | %.1f | %.1f | %.2f | %.1f | %.2f | %.1f | %.0f |", pat, pol.name,
				mean, p95, float64(s.delayed)/n, float64(s.dropped)/n, s.wasteAggr/n, s.wasteProt/n, float64(s.hits)/n,
				float64(s.protLanes)/n, float64(s.spillLanes)/n, s.spillPts/n, float64(s.lanes)/n))
		}
		// The recommended strategy (the default, S6) stalls less than v3 and rr_exp and keeps
		// v3's gains on aggressive waste over rr_exp.
		if s6, s1, s0 := res["S6 S3 + cost floor (default)"], res["S1 v3"], res["S0 rr_exp"]; s6 != nil && s1 != nil && s0 != nil {
			if s6.s.delayed > s1.s.delayed || s6.s.delayed > s0.s.delayed || s6.s.wasteAggr >= s0.s.wasteAggr {
				t.Errorf("%s: S6 delayed %d vs S1 %d, rr_exp %d; aggressive waste %.1f vs rr_exp %.1f", pat, s6.s.delayed, s1.s.delayed, s0.s.delayed, s6.s.wasteAggr/n, s0.s.wasteAggr/n)
			}
		}
	}
	t.Log("\n" + strings.Join(lines, "\n"))
	if path := os.Getenv("WY_SIM_OUT"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
}
