package mover

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/mover/internal/m5"
)

// The simulator (setup.md 8.6, Simulator): seven accounts (three protected), lanes placed by
// the picker on their estimate whose true burn differs (0.3x-5x), helper panels, long Opus
// lanes, pile-ups, an owner's interactive sessions, weekly pressure, two organisations, a
// laptop where only some accounts are logged in, readings that lag and are noisy. Every
// strategy runs the same seeded workload: v2 passes every minute, the shipped M5 every 2.
//
//	WY_SIM_RUNS=300 WY_SIM_PAR=8 WY_SIM_OUT=/tmp/sim.md go test -run TestSimulateStrategies -timeout 3h ./internal/mover/

const (
	simDays      = 2
	simRatio     = 7.2
	simCostFixed = 0.2 // the true cost of a move, 5-hour points: fixed plus per million context tokens
	simCostMTok  = 1.2
	simCacheTTL  = 60 // minutes: the prompt cache of a subscription
	simAccounts  = 7
)

// simProtected are the protected accounts (claude-1..3).
func simProtected(i int) bool { return i < 3 }

type laneSpec struct {
	start  int     // minute
	work   int     // busy minutes
	est    float64 // the estimate the picker placed it on, 5-hour points per busy hour
	mult   float64 // true burn = est × mult
	burstP float64 // chance a turn starts a helper panel
	group  int     // > 0: a pile-up, the whole group lands on one expiring account
	seed   uint64
	// owner: the owner's interactive session (on the protected claude-1, never placed by the
	// picker); it is typed into at the start of every turn.
	owner bool
}

type simPattern struct {
	name      string
	lanes     func(r *rand.Rand) []laneSpec
	lag       [2]int  // reading lag, minutes
	noise     float64 // reading noise, sd in points
	rateNoise float64 // per-minute token noise, relative sd
	// setup changes the accounts after the default draw (weekly pressure, orgs, logins).
	setup func(r *rand.Rand, accs []*simAcc)
	orgs  bool // the move_orgs rows apply
	// integer: readings are whole percents (floored), as Claude Code reports them.
	integer bool
}

func uniform(r *rand.Rand, lo, hi float64) float64 { return lo + r.Float64()*(hi-lo) }

// logUniform draws between lo and hi evenly on a log scale (0.3x-5x).
func logUniform(r *rand.Rand, lo, hi float64) float64 {
	return math.Exp(uniform(r, math.Log(lo), math.Log(hi)))
}

func spread(r *rand.Rand, n, workLo, workHi int, rateLo, rateHi, burstP, multLo, multHi float64) []laneSpec {
	var out []laneSpec
	for range n {
		out = append(out, laneSpec{start: r.IntN(simDays*1440 - 120), work: workLo + r.IntN(workHi-workLo+1),
			est: uniform(r, rateLo, rateHi), mult: logUniform(r, multLo, multHi), burstP: burstP, seed: r.Uint64()})
	}
	return out
}

// pileups: batches of 5 lanes onto one expiring account, 2 of them hot.
func pileups(r *rand.Rand, every int) []laneSpec {
	var out []laneSpec
	group := 0
	for t := 60 + r.IntN(every); t < simDays*1440-180; t += every/2 + r.IntN(every) {
		group++
		for k := range 5 {
			m := uniform(r, 0.6, 1.2)
			if k < 2 {
				m = uniform(r, 2.5, 4)
			}
			out = append(out, laneSpec{start: t + r.IntN(3), work: 70 + r.IntN(150), est: uniform(r, 8, 14), mult: m,
				burstP: 0.1, group: group, seed: r.Uint64()})
		}
	}
	return out
}

func owners(r *rand.Rand, n int) []laneSpec {
	var out []laneSpec
	for d := range simDays {
		for range n {
			out = append(out, laneSpec{start: d*1440 + 480 + r.IntN(240), work: 300 + r.IntN(240), est: uniform(r, 6, 12), mult: 1,
				burstP: 0.15, seed: r.Uint64(), owner: true})
		}
	}
	return out
}

var simPatterns = []simPattern{
	{name: "unpredictable", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, lanes: func(r *rand.Rand) []laneSpec {
		return spread(r, 22, 60, 300, 8, 18, 0.2, 0.3, 5)
	}},
	{name: "spiky", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, lanes: func(r *rand.Rand) []laneSpec {
		return spread(r, 22, 60, 240, 10, 18, 0.4, 0.7, 1.5)
	}},
	{name: "long-opus", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, lanes: func(r *rand.Rand) []laneSpec {
		return spread(r, 15, 240, 600, 16, 26, 0.05, 0.6, 2)
	}},
	{name: "pile-up", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, lanes: func(r *rand.Rand) []laneSpec {
		return append(pileups(r, 480), spread(r, 6, 60, 240, 10, 18, 0.1, 0.5, 2)...)
	}},
	{name: "protected-owner", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, lanes: func(r *rand.Rand) []laneSpec {
		return append(owners(r, 2), spread(r, 14, 60, 300, 8, 18, 0.2, 0.5, 3)...)
	}},
	{name: "weekly-95", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, lanes: func(r *rand.Rand) []laneSpec {
		hot := spread(r, 3, 120, 360, 10, 16, 0.1, 3, 5)
		return append(hot, spread(r, 12, 60, 240, 8, 16, 0.2, 0.5, 2)...)
	}, setup: func(r *rand.Rand, accs []*simAcc) {
		for i := 3; i < simAccounts; i++ {
			if r.Float64() < 0.6 {
				accs[i].l7 = uniform(r, 90, 96)
			}
		}
	}},
	{name: "two-orgs", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, orgs: true, lanes: func(r *rand.Rand) []laneSpec {
		return append(spread(r, 18, 60, 300, 8, 18, 0.2, 0.5, 3), pileups(r, 900)...)
	}, setup: func(r *rand.Rand, accs []*simAcc) {
		accs[2].org, accs[6].org = "oB", "oB" // 2 + 5: one protected and one aggressive in oB
	}},
	{name: "laptop", lag: [2]int{1, 10}, noise: 1, rateNoise: 0.15, lanes: func(r *rand.Rand) []laneSpec {
		return spread(r, 14, 60, 300, 8, 18, 0.2, 0.5, 3)
	}, setup: func(r *rand.Rand, accs []*simAcc) {
		for i, a := range accs {
			a.loggedIn = i == 0 || i == 3 || i == 4 // claude-1 (protected), 4, 5
			if !a.loggedIn {
				a.other = uniform(r, 4, 15) // the other machines' lanes on it
			}
		}
	}},
	// remote-integer: the owner's own sessions run on another machine on four accounts (the
	// hub measures two of them), this machine has a few lanes, readings are whole percents.
	{name: "remote-integer", lag: [2]int{1, 10}, noise: 0.3, rateNoise: 0.15, integer: true, lanes: func(r *rand.Rand) []laneSpec {
		return spread(r, 10, 60, 300, 8, 18, 0.2, 0.5, 3)
	}, setup: func(r *rand.Rand, accs []*simAcc) {
		for k, i := range r.Perm(simAccounts)[:4] {
			accs[i].other = uniform(r, 2, 9) // 2-9 %/h of the other machine's sessions
			accs[i].otherSeen = k < 2
		}
	}},
	{name: "noisy", lag: [2]int{3, 10}, noise: 3, rateNoise: 0.4, lanes: func(r *rand.Rand) []laneSpec {
		return append(append(spread(r, 10, 60, 240, 10, 18, 0.3, 0.3, 5), spread(r, 6, 240, 600, 16, 26, 0.05, 0.6, 2)...), pileups(r, 900)...)
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
	obs                    [10]float64 // the observed burn of the last 10 minutes, points per hour
	last5                  [5]float64
	age, movedAt, movedFrm int
	typed                  int // the minute the owner last typed into it (-1: never)
	// The turn's steps (seconds): a tool call in flight (inTool; stepLeft to go, stepDone so
	// far) or the model's gap before the next one. sr draws them (its own stream).
	sr                 *rand.Rand
	inTool             bool
	stepLeft, stepDone float64
	// A mid-turn move waiting for the step in flight to end: to pendTo at minute pendAt (-1: none).
	pendTo, pendAt   int
	idleFor, turnAge int
	// coord: a week-long pinned coordinator (pins_sim_test.go), nil for a lane.
	coord      *coordSpec
	lastBurn   int
	wasStopped bool
	// mf: its model's re-read factor (opus 5, sonnet 3); expMult: the picker's estimate of its
	// remaining time over the truth.
	mf, expMult float64
	// The startup burst (sim_young_test.go): sb burned minutes left at sbF times its rate.
	// obsTrue is obs without the burst's excess (its steady burn); obsCW the cache writes of
	// its first 10 minutes (the burst's excess mostly is).
	sb             int
	sbF            float64
	obsTrue, obsCW [10]float64
	// burned: its burn so far, 5-hour points; endAt the minute it finished (sim_place_test.go).
	burned float64
	endAt  int
}

// trueRate10 is rate10 without the startup burst's excess.
func (l *simLane) trueRate10() float64 {
	s := 0.0
	for _, x := range l.obsTrue {
		s += x
	}
	return s / 10
}

// rate10 is the lane's measured burn over the last 10 minutes, points per hour.
func (l *simLane) rate10() float64 {
	s := 0.0
	for _, x := range l.obs {
		s += x
	}
	return s / 10
}

type simObs struct {
	l5, l7 float64
	r5, r7 int
	cum    float64 // local burn so far (every window), 5-hour points
}

type simAcc struct {
	key, org  string
	protected bool
	loggedIn  bool
	l5, l7    float64
	r5, r7    int // the minute the window resets; r5 -1: not started
	other     float64
	// otherSeen: the hub measures the other machines' sessions on it (their leases' burn).
	otherSeen  bool
	readStreak int     // passes in a row the readings rose over twice the meter
	cum        float64 // local burn so far
	h          []simObs
}

type simResult struct {
	StoppedMin, IntStoppedMin, Hits, Moves, MidTurn, MidTurnMTok, ReReadMTok, WeeklyWaste, resets7 float64
	NoDest, IntMoved, IntDelivered, NotLoggedIn, Repeats, StepWaits, WaitStopped                   float64
	MaxProt5, MaxProt7                                                                             float64 // sums over runs of the run's max
	// The coordinators layer (pins_sim_test.go).
	PinStopped, PinStoppedHeavy, PinHits, CoordMoves, CoordReRead, DisplacedMoves, DisplacedPlaces, Alerts float64
	PinHits7d, CoordBurn, OwnerMoves, PinStoppedNoRoom, PinHitsShared                                      float64
	RunsOver80                                                                                             float64 // runs whose protected level went over 80
	Over80Min, Over80Owner, Over80NoRoom, Over80Other                                                      float64 // protected minutes over 80, by cause
	// FalseMoves: moves whose source was under its caps at its true burn; ReReadCost: re-read
	// context in opus-equivalent Mtok (× model factor / 5).
	FalseMoves, ReReadCost float64
	// YoungMoves: moves of lanes under 15 minutes old (sim_young_test.go).
	YoungMoves, YoungFalse float64
	// Placed: lanes the picker placed; Moved30: of them, those moved within 30 minutes of
	// their placement (sim_place_test.go).
	Placed, Moved30 float64
	runs            float64
}

func (r *simResult) add(o simResult) {
	r.StoppedMin += o.StoppedMin
	r.IntStoppedMin += o.IntStoppedMin
	r.Hits += o.Hits
	r.Moves += o.Moves
	r.MidTurn += o.MidTurn
	r.MidTurnMTok += o.MidTurnMTok
	r.ReReadMTok += o.ReReadMTok
	r.WeeklyWaste += o.WeeklyWaste
	r.resets7 += o.resets7
	r.NoDest += o.NoDest
	r.IntMoved += o.IntMoved
	r.Repeats += o.Repeats
	r.StepWaits += o.StepWaits
	r.WaitStopped += o.WaitStopped
	r.IntDelivered += o.IntDelivered
	r.NotLoggedIn += o.NotLoggedIn
	r.MaxProt5 += o.MaxProt5
	r.MaxProt7 += o.MaxProt7
	r.RunsOver80 += o.RunsOver80
	r.Over80Min += o.Over80Min
	r.FalseMoves += o.FalseMoves
	r.ReReadCost += o.ReReadCost
	r.YoungMoves += o.YoungMoves
	r.YoungFalse += o.YoungFalse
	r.Placed += o.Placed
	r.Moved30 += o.Moved30
	r.Over80Owner += o.Over80Owner
	r.Over80NoRoom += o.Over80NoRoom
	r.Over80Other += o.Over80Other
	r.runs += o.runs
	r.PinStopped += o.PinStopped
	r.PinStoppedHeavy += o.PinStoppedHeavy
	r.PinHits += o.PinHits
	r.CoordMoves += o.CoordMoves
	r.CoordReRead += o.CoordReRead
	r.DisplacedMoves += o.DisplacedMoves
	r.DisplacedPlaces += o.DisplacedPlaces
	r.Alerts += o.Alerts
	r.PinHits7d += o.PinHits7d
	r.CoordBurn += o.CoordBurn
	r.OwnerMoves += o.OwnerMoves
	r.PinStoppedNoRoom += o.PinStoppedNoRoom
	r.PinHitsShared += o.PinHitsShared
}

// simStrat is one strategy: no moves, the shipped M5 or v2 with params, and the picker's
// forward 5-hour hold for new lanes (hold: the projected level that holds a lane off; 0 none).
type simStrat struct {
	name string
	off  bool
	m5   bool
	p    Params
	hold float64
	orgs bool // only on the patterns with two organisations
	// The coordinators layer (pins_sim_test.go): coord adds the 5 week-long coordinators;
	// pinned: they are pinned (else movable like any lane) and their accounts keep
	// p.PinReserve5/7 per live pin (measured: their own heaviest 5-hour window of the last day
	// instead); abnormal: the second day burns 4-5x.
	coord, pinned, measured, abnormal bool
	// ownerActs: the owner moves an alerted pinned session by hand 30 minutes after the alert
	// (to the account wy move would pick).
	ownerActs bool
	// oldRate: the account's burn as before the live-run fixes (the reading delta over 10
	// minutes, used at once; other machines' sessions not counted from the hub). pickProj:
	// the picker places no lane where the switch's projection would go over its caps.
	oldRate, pickProj bool
	// startup: lanes burn 3-6x for their first 3-8 burned minutes (sim_young_test.go);
	// measure: the session rate the plan sees (nil: rate10).
	startup bool
	measure func(l *simLane) float64
	// readFull: the readings are compared with the meter of the full rates (not measure's).
	readFull bool
	// The picker's placement as on the live lanes strategy (sim_place_test.go): rank puts the
	// aggressive accounts in drain, then at risk, first; capTarget aims the drain and at-risk
	// room at the switch's caps less the pin reserve (else 98, the reserve only on the
	// weekly limit); laneRate is the new lane's rate in the switch-projection check (pickProj):
	// laneEst its estimate (l.est), laneClass the size class (cost over wall time),
	// laneMeasured the measured per-model rate of finished and running lanes.
	rank, capTarget bool
	laneRate        int
	rateQ           float64 // laneMeasured: the quantile of the lanes' rates
}

// The new lane's rate in the picker's switch-projection check (simStrat.laneRate).
const (
	laneEst = iota
	laneClass
	laneMeasured
	laneTrue // its true busy rate (an upper bound on what a better estimate does)
)

// rateOf is the session rate the plan sees.
func (st simStrat) rateOf(l *simLane) float64 {
	if st.measure != nil {
		return st.measure(l)
	}
	return l.rate10()
}

func v2(name string, f func(p *Params)) simStrat {
	p := DefaultParams
	p.Name = name
	p.YoungGate = 0 // the tables before young sessions (sim_young_test.go) are kept as they were
	if f != nil {
		f(&p)
	}
	return simStrat{name: name, p: p, hold: 100}
}

var simStrategies = func() []simStrat {
	pickB := v2("V2 + picker (b) no 5h hold", nil)
	pickB.hold = 0
	pickC := v2("V2 + picker (c) 5h hold at 110", nil)
	pickC.hold = 110
	same1 := v2("V2 move_orgs same-first", func(p *Params) { p.Orgs = OrgsSameFirst })
	same1.orgs = true
	same := v2("V2 move_orgs same", func(p *Params) { p.Orgs = OrgsSame })
	same.orgs = true
	return []simStrat{
		{name: "M0 no moves", off: true, hold: 100},
		{name: "M5 shipped (1.3.3-pre)", m5: true, hold: 100},
		v2("V2 default", nil),
		v2("V2 caps 98", func(p *Params) { p.Cap5 = 98 }),
		v2("V2 caps 92", func(p *Params) { p.Cap5 = 92 }),
		v2("V2 no spike filter", func(p *Params) { p.SpikePasses = 0 }),
		v2("V2 idle moves too", func(p *Params) { p.IdleMoves = true }),
		v2("V2 + 2-min owner grace", func(p *Params) { p.OwnerGrace = 2 * time.Minute }),
		v2("V2 kill inside steps (before)", func(p *Params) { p.StepWait = 0 }),
		pickB, pickC, same1, same,
	}
}()

var simT0 = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

func simAt(m int) time.Time { return simT0.Add(time.Duration(m) * time.Minute) }

func simKey(i int, org string) string { return fmt.Sprintf("acct:claude-%d/%s", i+1, org) }

// simCheck sees every v2 pass's input and result.
type simCheck func(in Input, r Result)

// simulate runs one seeded workload under st.
func simulate(st simStrat, pat simPattern, seed uint64, check simCheck) simResult {
	gen := rand.New(rand.NewPCG(seed, 1))
	specs := pat.lanes(gen)
	ar := rand.New(rand.NewPCG(seed, 2))
	obsR := rand.New(rand.NewPCG(seed, 3))
	res := simResult{runs: 1}
	accs := make([]*simAcc, simAccounts)
	for i := range accs {
		a := &simAcc{org: "oA", protected: simProtected(i), loggedIn: true, r5: -1, l7: uniform(ar, 10, 70), r7: ar.IntN(7 * 1440)}
		if !a.protected && ar.Float64() < 0.2 {
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
	if pat.setup != nil {
		pat.setup(ar, accs)
	}
	for i, a := range accs {
		a.key = simKey(i, a.org)
	}
	lanes := make([]*simLane, len(specs))
	byID := map[string]*simLane{}
	for i, s := range specs {
		l := &simLane{laneSpec: s, id: fmt.Sprintf("s%03d", i), r: rand.New(rand.NewPCG(s.seed, 4)), movedFrm: -1, typed: -1, ctx: 30_000,
			sr: rand.New(rand.NewPCG(s.seed, 5)), pendTo: -1, mf: 5, expMult: 1}
		if mr := rand.New(rand.NewPCG(s.seed, 6)); true {
			if mr.Float64() < 0.4 {
				l.mf = 3 // a sonnet lane
			}
			l.expMult = logUniform(mr, 0.5, 2)
		}
		lanes[i], byID[l.id] = l, l
	}
	if st.coord {
		for _, c := range coordinators(seed, st.abnormal) {
			if !accs[c.acc].loggedIn {
				continue // the laptop: that coordinator runs on another machine (its burn is in other)
			}
			lanes = append(lanes, c)
			byID[c.id] = c
		}
	}
	accIdx := map[string]int{}
	for i, a := range accs {
		accIdx[a.key] = i
	}
	atLimit := func(a *simAcc) bool { return a.l5 >= 100 || a.l7 >= 100 }
	// The pin reserve of each account (points of the 5-hour and weekly windows kept free).
	rsv5, rsv7 := make([]float64, len(accs)), make([]float64, len(accs))
	pinsOn := make([]int, len(accs))
	burnAcc := func(a *simAcc, pts float64, m int, local bool) {
		if a.r5 < 0 {
			a.r5 = m + 300
		}
		a.l5 += pts
		a.l7 += pts / simRatio
		if local {
			a.cum += pts
		}
	}
	// holds: the picker's forward 5-hour check: the running lanes (their estimate while young,
	// then their measured burn) and the new lane for up to 2 hours reach st.hold before the reset.
	holds := func(i, m int, est, rsv float64) bool {
		a := accs[i]
		if st.hold <= 0 || a.r5 < 0 {
			return false
		}
		T := float64(a.r5-m) / 60
		lv := a.l5 + a.other*T + est*0.8*math.Min(2, T) + rsv
		for _, l := range lanes {
			if l.on && !l.done && !l.stopped && l.acc == i {
				r := l.est * 0.8
				if l.age >= 10 {
					r = l.rate10()
				}
				lv += r * math.Min(float64(l.work)/60, T)
			}
		}
		return lv >= st.hold
	}
	// place: the picker's choice for a new lane, among the accounts logged in here: the lowest
	// 5-hour reading among the aggressive ones with room, else a protected one under its gate
	// (5h < 60, 7d < 70), else the least-used aggressive one.
	var place func(m int, est float64) int
	// newMF is the model of the lane being placed; newRate its rate in the picker's
	// switch-projection check (simStrat.laneRate).
	newMF, newMult := 5.0, 1.0
	newRate := func(m int, est float64) float64 {
		switch st.laneRate {
		case laneTrue:
			return est * newMult
		case laneClass:
			return simClassRate
		case laneMeasured:
			return measuredRate(lanes, newMF, st.rateQ)
		}
		return est
	}
	placeR := func(m int, est float64, useR bool) int {
		r5, r7 := rsv5, rsv7
		if !useR {
			r5, r7 = make([]float64, len(accs)), make([]float64, len(accs))
		}
		// over: the switch's projection (projection.Levels via Project) with the new lane's
		// estimate added goes over its caps (the picker's check while the switch is on).
		over := func(i int) bool {
			if !st.pickProj {
				return false
			}
			a := accs[i]
			r := newRate(m, est)
			for _, l := range lanes {
				if l.on && !l.done && !l.stopped && l.acc == i {
					r += l.rate10()
				}
			}
			if a.otherSeen {
				r += a.other
			}
			acc := Account{Key: a.key, Protected: a.protected, Ratio: simRatio, Rate: r, Reserve5: r5[i], Reserve7: r7[i],
				W7: Window{Level: a.l7, Reset: simAt(a.r7)}}
			if a.r5 > m {
				acc.W5 = Window{Level: a.l5, Reset: simAt(a.r5)}
			}
			return st.p.Project(acc, nil, 0, simAt(m)).Window != ""
		}
		best := -1
		var bestK rankKey
		for i, a := range accs {
			if !a.loggedIn || a.protected || a.l5 >= 90-r5[i] || a.l7 >= 95-r7[i] || holds(i, m, est, r5[i]) || over(i) {
				continue
			}
			k := rankKey{group: 2, l5: a.l5}
			if st.rank {
				fwd := 0.0 // the running lanes' burn before the weekly reset, 5-hour points
				for _, l := range lanes {
					if l.on && !l.done && !l.stopped && l.acc == i {
						fwd += l.est * 0.8 * math.Min(float64(l.work)/60, float64(a.r7-m)/60)
					}
				}
				k = rankOf(st, a, m, r5[i], r7[i], fwd)
			}
			if best < 0 || k.less(bestK) {
				best, bestK = i, k
			}
		}
		if best >= 0 {
			return best
		}
		for i, a := range accs {
			if a.loggedIn && a.protected && a.l5 < 60-r5[i] && a.l7 < 70-r7[i] && !over(i) && (best < 0 || a.l5 < accs[best].l5) {
				best = i
			}
		}
		if best >= 0 {
			return best
		}
		// No room under the soft limits: the picker's hard limits (98 less the reserve), then any.
		for i, a := range accs {
			if a.loggedIn && !a.protected && a.l5 < 98-r5[i] && a.l7 < 98-r7[i] && !over(i) && (best < 0 || a.l5 < accs[best].l5) {
				best = i
			}
		}
		if best >= 0 {
			return best
		}
		for i, a := range accs {
			if a.loggedIn && !a.protected && (best < 0 || a.l5 < accs[best].l5) {
				best = i
			}
		}
		if best < 0 {
			best = 0
		}
		return best
	}
	place = func(m int, est float64) int {
		i := placeR(m, est, true)
		if st.pinned && i != placeR(m, est, false) {
			res.DisplacedPlaces++ // the reserve sent this lane elsewhere
		}
		return i
	}
	// expiring: a pile-up lands on the aggressive logged-in account whose 5-hour window resets
	// soonest (the picker drains expiring room first).
	expiring := func(m int) int {
		best := -1
		for i, a := range accs {
			if !a.loggedIn || a.protected || a.l7 >= 95-rsv7[i] {
				continue
			}
			if best < 0 || (a.r5 >= 0 && (accs[best].r5 < 0 || a.r5 < accs[best].r5)) {
				best = i
			}
		}
		if best < 0 {
			return place(m, 10)
		}
		return best
	}
	ownerAcc := 0
	for i, a := range accs {
		if a.protected && a.loggedIn {
			ownerAcc = i
			break
		}
	}
	groups := map[int]int{}
	var recs []Record
	var m5recs []m5.Record
	streak := map[string]int{}
	alpha := 1 - math.Exp(-1.0/10)
	pass := 1
	if st.m5 {
		pass = 2
	}
	alerted := map[string]bool{}
	ownerAt := map[string]int{}
	for m := 0; m < simDays*1440; m++ {
		if st.coord && st.pinned {
			coordReserve(st, lanes, m, rsv5, rsv7, pinsOn)
		}
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
			if a.other > 0 && !atLimit(a) { // other machines' sessions stop at the limit too
				burnAcc(a, a.other/60, m, false)
			}
		}
		for _, l := range lanes {
			if l.start == m {
				switch {
				case l.owner:
					l.acc = ownerAcc
				case l.group > 0:
					if _, ok := groups[l.group]; !ok {
						groups[l.group] = expiring(m)
					}
					l.acc = groups[l.group]
				default:
					newMF, newMult = l.mf, l.mult
					res.Placed++
					l.acc = place(m, l.est)
				}
				l.on, l.busy = true, 4+l.r.IntN(20)
				if st.startup && l.coord == nil {
					br := rand.New(rand.NewPCG(l.seed, 7))
					l.sb, l.sbF = 3+br.IntN(6), uniform(br, 3, 6)
				}
				l.newTurn()
				if l.owner {
					l.typed = m // the owner starts it with a prompt
				}
			}
			if !l.on || l.done {
				continue
			}
			l.age++
			a := accs[l.acc]
			b, bTrue, bCW := 0.0, -1.0, 0.0
			switch {
			case l.restart > 0:
				l.restart--
			case l.coord != nil:
				b = l.coord.step(&res, l, a, m, atLimit(a))
				if b > 0 {
					burnAcc(a, b, m, true)
				}
			case l.stopped && atLimit(a):
				res.StoppedMin++
				if l.owner {
					res.IntStoppedMin++
				}
			default:
				l.stopped = false
				if l.busy == 0 {
					l.idle--
					l.idleFor++
					if l.idle <= 0 {
						l.idleFor, l.turnAge = 0, 0
						l.newTurn()
						if l.owner {
							l.typed = m // every owner turn starts with the owner typing
						}
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
					if l.owner {
						res.IntStoppedMin++
					}
					break
				}
				rate := l.est * l.mult * uniform(l.r, 0.8, 1.2)
				steady := rate
				if l.sb > 0 {
					extra := rate * (l.sbF - 1)
					rate += extra
					bCW = extra * 0.85 / 60
					l.sb--
				}
				if l.age <= 10 {
					bCW += steady * 0.15 / 60
				}
				bTrue = steady / 60
				if l.burst > 0 {
					bTrue += l.burstRate / 60
					rate += l.burstRate
					l.burst--
				}
				b = rate / 60
				l.turnAge++
				burnAcc(a, b, m, true)
				l.burned += b
				l.ctx += 6000
				if l.ctx > 800_000 {
					l.ctx = 200_000
				}
				l.work--
				l.busy--
				l.steps(60)
				if l.work <= 0 {
					l.done, l.endAt = true, m
				} else if l.busy == 0 {
					l.idle = 1 + l.r.IntN(6)
					if l.owner || l.r.Float64() < 0.15 {
						l.idle = 8 + l.r.IntN(53) // waits for the owner: its cache may go cold
					}
				}
			}
			ob := b * 60
			if pat.rateNoise > 0 && ob > 0 {
				ob = math.Max(0, ob*(1+pat.rateNoise*obsR.NormFloat64()))
			}
			obT, obC := ob, 0.0
			if bTrue >= 0 && b > 0 {
				obT, obC = ob*bTrue/b, ob*bCW/b
			}
			copy(l.obsTrue[:], l.obsTrue[1:])
			l.obsTrue[9] = obT
			copy(l.obsCW[:], l.obsCW[1:])
			l.obsCW[9] = obC
			l.ewma += (ob - l.ewma) * alpha
			copy(l.obs[:], l.obs[1:])
			l.obs[9] = ob
			copy(l.last5[:], l.last5[1:])
			l.last5[4] = ob
		}
		ownerBurning := map[int]bool{}
		for _, l := range lanes {
			if l.owner && l.on && !l.done && l.busy > 0 {
				ownerBurning[l.acc] = true
			}
		}
		noRoom := true
		for _, a := range accs {
			if a.loggedIn && !a.protected && a.l5 < 85 && a.l7 < 93 {
				noRoom = false
			}
		}
		for _, l := range lanes {
			if l.coord == nil {
				continue
			}
			if l.stopped && atLimit(accs[l.acc]) && noRoom {
				res.PinStoppedNoRoom++
			}
			if l.stopped && !l.wasStopped {
				for _, x := range lanes {
					if x.coord == nil && x.on && !x.done && x.acc == l.acc && x.rate10() > 0 {
						res.PinHitsShared++ // lanes burned on its account in the last 10 minutes
						break
					}
				}
			}
			l.wasStopped = l.stopped
		}
		for i, a := range accs {
			a.h = append(a.h, simObs{a.l5, a.l7, a.r5, a.r7, a.cum})
			if a.protected {
				res.MaxProt5 = math.Max(res.MaxProt5, a.l5)
				res.MaxProt7 = math.Max(res.MaxProt7, a.l7)
				if a.l5 > 80 || a.l7 > 80 {
					res.Over80Min++
					switch {
					case ownerBurning[i]:
						res.Over80Owner++
					case noRoom:
						res.Over80NoRoom++
					default:
						res.Over80Other++
					}
				}
			}
		}
		// Mid-turn moves that waited for their step to end land now, between steps.
		for _, l := range lanes {
			if l.pendTo < 0 || m < l.pendAt {
				continue
			}
			to := l.pendTo
			l.pendTo = -1
			if l.done {
				continue
			}
			if l.stopped {
				res.WaitStopped++ // the limit stopped it while it waited
			}
			execMove(&res, l, accs, to, m, burnAcc, false)
		}
		if st.off || m%pass != 0 {
			continue
		}
		lag := pat.lag[0] + obsR.IntN(pat.lag[1]-pat.lag[0]+1)
		// What the machine reads: the lagged reading plus the local meter since it.
		type seen struct {
			l5, l7       float64
			r5, r7       int
			rate, meter  float64
			note         string
			fromReadings bool
		}
		read := func(x float64) float64 {
			if pat.integer {
				return math.Floor(clamp(x))
			}
			return x
		}
		view := make([]seen, len(accs))
		for i, a := range accs {
			o := a.h[max(0, len(a.h)-1-lag)]
			since := a.cum - o.cum
			v := seen{r5: a.r5, r7: a.r7}
			if a.r5 >= 0 {
				if o.r5 == a.r5 {
					v.l5 = o.l5 + since
				} else {
					v.l5 = a.l5 // a new window since the reading: the meter has all of it (approximately)
				}
				v.l5 = clamp(v.l5 + pat.noise*obsR.NormFloat64())
				if pat.integer {
					v.l5 = clamp(read(o.l5+pat.noise*obsR.NormFloat64()) + v.l5 - o.l5)
				}
			}
			if o.r7 == a.r7 {
				v.l7 = o.l7 + since/simRatio
			} else {
				v.l7 = a.l7
			}
			v.l7 = clamp(v.l7 + pat.noise*obsR.NormFloat64()/2)
			// R5: the local meter over 10 min, cross-checked with the reading delta.
			meter, full := 0.0, 0.0
			for _, l := range lanes {
				if l.on && !l.done && l.acc == i {
					meter += st.rateOf(l)
					full += l.rate10()
				}
			}
			if !st.oldRate && a.otherSeen {
				meter += a.other // other machines' sessions the hub measured
				full += a.other
			}
			v.rate, v.meter = meter, meter
			cmp := meter // what the readings are compared with
			if st.readFull {
				cmp = full
			}
			if st.oldRate {
				if j := len(a.h) - 1 - lag - 10; j >= 0 {
					o1 := a.h[j]
					if o1.r5 == o.r5 && o.r5 >= 0 {
						if d := (read(o.l5+pat.noise*obsR.NormFloat64()) - read(o1.l5+pat.noise*obsR.NormFloat64())) * 6; d > 2*meter && d > 1 {
							v.rate, v.note, v.fromReadings = d, "reading delta", true
						}
					}
				}
			} else if o.r5 >= 0 {
				// The slope of the readings of the last 30 minutes, 5 minutes apart, of this window;
				// used when over twice the meter on two passes in a row.
				var xs, ys []float64
				for k := 0; k <= 30; k += 5 {
					j := len(a.h) - 1 - lag - k
					if j < 0 || a.h[j].r5 != o.r5 {
						break
					}
					xs, ys = append(xs, -float64(k)/60), append(ys, read(a.h[j].l5+pat.noise*obsR.NormFloat64()))
				}
				used := false
				if len(xs) >= 4 {
					var mx, my float64
					for q := range xs {
						mx, my = mx+xs[q], my+ys[q]
					}
					mx, my = mx/float64(len(xs)), my/float64(len(xs))
					var sxy, sxx float64
					for q := range xs {
						sxy += (xs[q] - mx) * (ys[q] - my)
						sxx += (xs[q] - mx) * (xs[q] - mx)
					}
					if d := math.Max(0, sxy/sxx); d > 2*cmp && d > 1 {
						used = true
						a.readStreak++
						if a.readStreak >= 2 {
							v.rate, v.note, v.fromReadings = d, "readings", true
						}
					}
				}
				if !used {
					a.readStreak = 0
				}
			}
			view[i] = v
		}
		if st.m5 {
			in := m5.Input{Now: simAt(m), Params: m5.M5, Recent: m5recs}
			for i, a := range accs {
				v := view[i]
				acc := m5.Account{Key: a.key, Label: a.key, Protected: a.protected, Ratio: simRatio, Other: a.other, Excluded: !a.loggedIn}
				if v.r5 > m {
					acc.W5 = m5.Window{Level: v.l5, Reset: simAt(v.r5)}
				}
				acc.W7 = m5.Window{Level: v.l7, Reset: simAt(v.r7)}
				in.Accounts = append(in.Accounts, acc)
			}
			for _, l := range lanes {
				if !l.on || l.done {
					continue
				}
				s := m5.Session{ID: l.id, Account: accs[l.acc].key, Rate: l.ewma, Age: time.Duration(l.age) * time.Minute,
					Idle: (l.busy == 0 || l.restart > 0) && !l.stopped, Stopped: l.stopped, Context: l.ctx, Movable: true,
					IdleFor: time.Duration(l.idleFor) * time.Minute, Attended: l.owner && l.busy > 0 && l.turnAge < 3, Model: "opus"}
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
			for _, mv := range m5.Plan(in) {
				if mv.To == "" {
					res.NoDest++
					continue
				}
				if mv.Wait {
					continue
				}
				m5recs = append(m5recs, m5.Record{Session: mv.Session, From: mv.From, To: mv.To, At: simAt(m)})
				execMove(&res, byID[mv.Session], accs, accIdx[mv.To], m, burnAcc, false)
			}
			continue
		}
		in := Input{Now: simAt(m), Params: st.p, Recent: recs, Streak: streak}
		for i, a := range accs {
			v := view[i]
			acc := Account{Key: a.key, Label: a.key, Protected: a.protected, Ratio: simRatio, Rate: v.rate, RateNote: v.note, LoggedIn: a.loggedIn,
				Pins: pinsOn[i], Reserve5: rsv5[i], Reserve7: rsv7[i], Meter: v.meter, FromReadings: v.fromReadings && !st.oldRate}
			if v.r5 > m {
				acc.W5 = Window{Level: v.l5, Reset: simAt(v.r5)}
			}
			acc.W7 = Window{Level: v.l7, Reset: simAt(v.r7)}
			in.Accounts = append(in.Accounts, acc)
		}
		for _, l := range lanes {
			if !l.on || l.done {
				continue
			}
			s := Session{ID: l.id, Account: accs[l.acc].key, Rate: st.rateOf(l), Busy: l.busy > 0 && l.restart == 0 && !l.stopped,
				Stopped: l.stopped, Interactive: l.owner, Movable: true, Context: l.ctx, Pinned: l.coord != nil && st.pinned,
				ModelFactor: l.mf, Started: simAt(l.start)}
			if l.coord == nil && !l.owner && l.work > l.age {
				s.Expected = time.Duration(float64(l.work-l.age)*l.expMult) * time.Minute // the picker's estimate
			}
			if l.typed >= 0 {
				s.Typed = simAt(l.typed)
			}
			if l.movedFrm >= 0 {
				s.MovedAt, s.MovedFrom = simAt(l.movedAt), accs[l.movedFrm].key
			}
			in.Sessions = append(in.Sessions, s)
		}
		r := Plan(in)
		if check != nil {
			check(in, r)
		}
		for _, al := range r.Alerts {
			k := fmt.Sprintf("%s %s %s %d", al.Account, al.Session, al.Window, al.ResetAt.Unix()/600)
			if !alerted[k] {
				alerted[k] = true
				res.Alerts++
				if st.ownerActs {
					ownerAt[al.Session] = m + 30
				}
			}
		}
		var due []string
		for id, at := range ownerAt {
			if at <= m {
				due = append(due, id)
			}
		}
		sort.Strings(due)
		for _, id := range due {
			delete(ownerAt, id)
			in2 := in
			in2.Sessions = append([]Session(nil), in.Sessions...)
			for k := range in2.Sessions {
				if in2.Sessions[k].ID == id {
					in2.Sessions[k].Pinned = false
				}
			}
			if to, _ := Destination(in2, id); to != "" {
				res.OwnerMoves++
				execMove(&res, byID[id], accs, accIdx[to], m, burnAcc, false)
			}
		}
		if st.pinned {
			res.DisplacedMoves += displaced(in, r)
		}
		streak = r.Streak
		for _, mv := range r.Moves {
			if mv.To == "" {
				res.NoDest++
				continue
			}
			// A false move: at its true burn (its lanes' and every other machine's) the source
			// was under its caps.
			if i := accIdx[mv.From]; true {
				a := accs[i]
				tr := a.other
				for _, x := range lanes {
					if x.on && !x.done && !x.stopped && x.acc == i {
						tr += x.trueRate10()
					}
				}
				ta := Account{Key: a.key, Protected: a.protected, Ratio: simRatio, Rate: tr, Reserve5: rsv5[i], Reserve7: rsv7[i],
					W7: Window{Level: a.l7, Reset: simAt(a.r7)}}
				if a.r5 > m {
					ta.W5 = Window{Level: a.l5, Reset: simAt(a.r5)}
				}
				if st.p.Project(ta, nil, 0, simAt(m)).Window == "" && !byID[mv.Session].stopped {
					res.FalseMoves++
					if x := byID[mv.Session]; x.coord == nil && x.age < 15 && x.movedFrm < 0 {
						res.YoungFalse++
					}
				}
			}
			l := byID[mv.Session]
			if l.coord == nil && l.age < 15 && l.movedFrm < 0 {
				res.YoungMoves++
			}
			if l.coord == nil && !l.owner && l.group == 0 && l.age < 30 && l.movedFrm < 0 && l.pendTo < 0 {
				res.Moved30++
			}
			if l.pendTo >= 0 {
				continue // already waiting for its step to end
			}
			recs = append(recs, Record{Session: mv.Session, From: mv.From, To: mv.To, At: in.Now})
			// Mid-turn with a tool call in flight: wait for it to end if it does within
			// min(StepWait, time to the projected hit - 1 min), else kill inside it (a repeat).
			repeat := false
			if l.busy > 0 && !l.stopped && l.restart == 0 && l.inTool {
				capS := st.p.StepWait.Seconds()
				if !mv.HitAt.IsZero() {
					capS = math.Min(capS, mv.HitAt.Sub(in.Now).Seconds()-60)
				}
				if l.stepLeft <= capS {
					l.pendTo, l.pendAt = accIdx[mv.To], m+int(math.Ceil(l.stepLeft/60))
					res.StepWaits++
					continue
				}
				repeat = true
			}
			execMove(&res, l, accs, accIdx[mv.To], m, burnAcc, repeat)
		}
		if len(recs) > 400 {
			recs = recs[len(recs)-200:]
		}
	}
	if res.MaxProt5 > 80 || res.MaxProt7 > 80 {
		res.RunsOver80 = 1
	}
	return res
}

// execMove carries out one move in the simulator: a lane killed mid-turn redoes its step and
// loses its helpers; the re-read of its context (uncached unless its cache went cold) is
// burnt on the destination. An interactive (owner) session is moved the same way and its
// owner gets the message in its pane; it then runs on the destination, not stopped.
//
// repeat: it was killed inside a tool call: the resumed session repeats that step (its time
// so far is redone on the destination). Killed between steps, nothing is redone.
func execMove(res *simResult, l *simLane, accs []*simAcc, to, m int, burnAcc func(*simAcc, float64, int, bool), repeat bool) {
	if l.owner {
		res.IntMoved++
		defer func() {
			if l.acc == to && !l.stopped {
				res.IntDelivered++ // the message is delivered and it runs on the new account
			}
		}()
	}
	if !accs[to].loggedIn {
		res.NotLoggedIn++
	}
	mid := l.busy > 0 && !l.stopped && l.restart == 0
	cost := simCostFixed
	if !(l.busy == 0 && l.idleFor >= simCacheTTL) {
		cost += simCostMTok * l.ctx / 1e6 * l.mf / 5
		res.ReReadMTok += l.ctx / 1e6
		res.ReReadCost += l.ctx / 1e6 * l.mf / 5
		if mid {
			res.MidTurnMTok += l.ctx / 1e6
		}
	}
	if mid {
		res.MidTurn++
		l.burst = 0 // its helpers are stopped
		if repeat {
			res.Repeats++
			l.work += int(math.Ceil(l.stepDone / 60))
		}
	}
	if l.inTool && !repeat {
		l.inTool, l.stepLeft = false, 0 // it moved between steps: the next one starts there
	}
	if l.coord != nil {
		res.CoordMoves++
		if cost > simCostFixed {
			res.CoordReRead += l.ctx / 1e6
		}
	}
	l.movedFrm, l.movedAt = l.acc, m
	burnAcc(accs[to], cost, m, true)
	l.acc, l.stopped, l.restart = to, false, 1
	res.Moves++
}

func clamp(x float64) float64 { return math.Min(100, math.Max(0, x)) }

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}

const simHeader = "| strategy | stopped | of which interactive | hits | moves | mid-turn | repeated steps | step waits | stopped while waiting | mid-turn Mtok | re-read Mtok | prot 5h max | prot 7d max | runs prot>80 % | prot>80 min (owner/no room/other) | waste | interactive moved | to not logged in | no room |\n" +
	"|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n"

func writeRow(b *strings.Builder, name string, s simResult) {
	days := s.runs * simDays
	waste := 0.0
	if s.resets7 > 0 {
		waste = s.WeeklyWaste / s.resets7
	}
	fmt.Fprintf(b, "| %s | %.1f | %.1f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.1f | %.1f | %.1f | %.1f (%.1f/%.1f/%.1f) | %.1f | %.2f | %.2f | %.2f |\n", name,
		s.StoppedMin/days, s.IntStoppedMin/days, s.Hits/days, s.Moves/days, s.MidTurn/days, s.Repeats/days, s.StepWaits/days, s.WaitStopped/days, s.MidTurnMTok/days, s.ReReadMTok/days,
		s.MaxProt5/s.runs, s.MaxProt7/s.runs, 100*s.RunsOver80/s.runs, s.Over80Min/days, s.Over80Owner/days, s.Over80NoRoom/days,
		s.Over80Other/days, waste, s.IntMoved/days, s.NotLoggedIn/days, s.NoDest/days)
}

func runMany(st simStrat, pat simPattern, runs, par int) simResult {
	var mu sync.Mutex
	var sum simResult
	var wg sync.WaitGroup
	next := make(chan int)
	for range par {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				r := simulate(st, pat, uint64(1000+i), nil)
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

// TestSimulateStrategies compares the strategies on every pattern and writes the tables to
// WY_SIM_OUT. Without WY_SIM_RUNS it runs 2 seeds per pattern (a smoke run).
func TestSimulateStrategies(t *testing.T) {
	if testing.Short() {
		t.Skip("simulator")
	}
	runs, par := envInt("WY_SIM_RUNS", 2), envInt("WY_SIM_PAR", 2)
	var b strings.Builder
	fmt.Fprintf(&b, "Mover simulator v2: %d seeded runs per pattern, %d days each, %d accounts (claude-1..3 protected). V2 passes every minute, M5 every 2.\n"+
		"Per day per run: stopped = lane-minutes stopped at a limit (primary); hits = lanes stopped by a limit; mid-turn = moves that killed a turn; repeated steps = mid-turn kills inside a tool call (the resumed session repeats that step; its time is redone on the destination); step waits = mid-turn moves that waited for the tool call in flight to end (at most StepWait, and 1 min before the projected hit) and landed between steps; stopped while waiting = of those, lanes a limit stopped before the move landed; "+
		"mid-turn Mtok = context re-read uncached by those; re-read Mtok = all uncached re-reads; interactive moved = the owner's sessions moved (each with its message delivered); prot 5h/7d max = "+
		"the run's highest protected level (mean over runs); runs prot>80 = share of runs where a protected level went over 80; prot>80 min = protected "+
		"account-minutes over 80, split by cause (the owner's interactive session burning on it / no aggressive account here had room / other: "+
		"other machines' burn or a lane the picker had to put there); waste = weekly percent unused at an aggressive account's weekly reset.\n\n",
		runs, simDays, simAccounts)
	totals := map[string]simResult{}
	for _, pat := range simPatterns {
		fmt.Fprintf(&b, "### %s\n\n%s", pat.name, simHeader)
		for _, st := range simStrategies {
			if st.orgs && !pat.orgs {
				continue
			}
			sum := runMany(st, pat, runs, par)
			if !st.orgs {
				tot := totals[st.name]
				tot.add(sum)
				totals[st.name] = tot
			}
			writeRow(&b, st.name, sum)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### all patterns\n\n%s", simHeader)
	for _, st := range simStrategies {
		if !st.orgs {
			writeRow(&b, st.name, totals[st.name])
		}
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("WY_SIM_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m0, v := totals["M0 no moves"], totals["V2 default"]
	if v.StoppedMin >= m0.StoppedMin {
		t.Errorf("V2 stops lanes no less than no moves: %.0f >= %.0f", v.StoppedMin, m0.StoppedMin)
	}
	for _, st := range simStrategies {
		if st.m5 || st.off {
			continue
		}
		if r := totals[st.name]; r.IntDelivered != r.IntMoved || r.NotLoggedIn > 0 {
			t.Errorf("%s: %v of %v interactive moves delivered, %v moves to a not-logged-in account", st.name, r.IntDelivered, r.IntMoved, r.NotLoggedIn)
		}
	}
}

// TestPlanInvariants checks every v2 pass of the simulator (WY_SIM_RUNS seeds, 1 under
// -short on a few patterns): no move to an account not logged in here, stale or its own;
// with OwnerGrace, no interactive session the owner typed into within the grace moved; every
// moved interactive session gets its message and runs on the new account (not stopped); no
// move of a session with no burn unless stopped (but with IdleMoves); every destination's projection with all the burn planned onto it stays
// Hysteresis under its caps; the cooldown, no return and the account rate limit hold; the
// same seed gives the same result.
func TestPlanInvariants(t *testing.T) {
	runs := envInt("WY_SIM_RUNS", 2)
	pats := simPatterns
	if testing.Short() {
		runs, pats = 1, []simPattern{simPatterns[0], simPatterns[4], simPatterns[7]}
	}
	var strats []simStrat
	for _, st := range simStrategies {
		if !st.off && !st.m5 {
			strats = append(strats, st)
		}
	}
	if testing.Short() {
		strats = strats[:1]
	}
	type job struct {
		st  simStrat
		pat simPattern
		i   int
	}
	var jobs []job
	for _, st := range strats {
		for _, pat := range pats {
			if st.orgs && !pat.orgs {
				continue
			}
			for i := range runs {
				jobs = append(jobs, job{st, pat, i})
			}
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	next := make(chan job)
	for range envInt("WY_SIM_PAR", 2) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range next {
				errs := invariantRun(j.st, j.pat, j.i)
				mu.Lock()
				for _, e := range errs {
					t.Error(e)
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		next <- j
	}
	close(next)
	wg.Wait()
}

func invariantRun(st simStrat, pat simPattern, i int) []string {
	p := st.p
	var errs []string
	fail := func(in Input, f string, a ...any) {
		if len(errs) < 4 {
			errs = append(errs, fmt.Sprintf("%s %s seed %d %s: ", st.name, pat.name, 1000+i, in.Now.Format("01-02 15:04"))+fmt.Sprintf(f, a...))
		}
	}
	check := func(in Input, r Result) {
		accs := map[string]Account{}
		for _, a := range in.Accounts {
			accs[a.Key] = a
		}
		sess := map[string]Session{}
		byAcc := map[string][]Session{}
		for _, s := range in.Sessions {
			sess[s.ID] = s
			byAcc[s.Account] = append(byAcc[s.Account], s)
		}
		delta := map[string]float64{}
		horizon := map[string]bool{} // judged on the horizon rule (above)
		into := map[string]int{}
		last := map[string]Record{}
		for _, rc := range in.Recent {
			if in.Now.Sub(rc.At) < p.AccountWindow {
				into[rc.To]++
			}
			if l, ok := last[rc.Session]; !ok || rc.At.After(l.At) {
				last[rc.Session] = rc
			}
		}
		for _, mv := range r.Moves {
			if mv.To == "" {
				continue
			}
			s, a := sess[mv.Session], accs[mv.To]
			switch {
			case !a.LoggedIn || a.Stale || a.Excluded:
				fail(in, "%s moved to %s, not logged in / stale", s.ID, mv.To)
			case mv.To == s.Account:
				fail(in, "%s moved to its own account", s.ID)
			case p.OwnerGrace > 0 && s.Interactive && !s.Typed.IsZero() && in.Now.Sub(s.Typed) < p.OwnerGrace:
				fail(in, "interactive %s moved %s after the owner typed (grace %s)", s.ID, in.Now.Sub(s.Typed), p.OwnerGrace)
			case !s.Busy && s.Rate <= 0 && !s.Stopped && !p.IdleMoves:
				fail(in, "%s moved with no burn", s.ID)
			}
			if l, ok := last[s.ID]; ok {
				if in.Now.Sub(l.At) < p.Cooldown {
					fail(in, "%s moved again %s after its last move", s.ID, in.Now.Sub(l.At))
				}
				if l.From == mv.To && in.Now.Sub(l.At) < p.NoReturn && !resetSince(a, l.At, in.Now) {
					fail(in, "%s moved back to %s within %s", s.ID, mv.To, in.Now.Sub(l.At))
				}
			}
			into[mv.To]++
			if into[mv.To] > p.AccountMoves {
				fail(in, "%d moves into %s within %s", into[mv.To], mv.To, p.AccountWindow)
			}
			if p.DestHorizon > 0 && !a.Protected {
				// The horizon rule: its own projection (with the moves planned before) plus
				// this session for the horizon.
				pr := p.Project(a, byAcc[mv.To], delta[mv.To], in.Now)
				h := s.Expected
				if h <= 0 {
					h = p.DestHorizon
				}
				h = max(min(h, pr.Reset5), DestFloor)
				p5 := pr.P5 + s.Rate*min(h, pr.Reset5).Hours()
				p7 := pr.P7 + s.Rate/pr.ratio*math.Min(h.Hours(), math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours()))
				if p5 > pr.Cap5-p.Hysteresis+1e-9 || p7 > pr.Cap7-p.Hysteresis+1e-9 {
					fail(in, "destination %s projects 5h %.1f / 7d %.1f with the moved burn for %s (caps %v/%v)", mv.To, p5, p7, h, pr.Cap5, pr.Cap7)
				}
				horizon[mv.To] = true
			}
			delta[mv.To] += s.Rate
			if !s.Stopped {
				delta[mv.From] -= s.Rate
			}
		}
		for key, d := range delta {
			if d <= 0 || horizon[key] {
				continue
			}
			pr := p.Project(accs[key], byAcc[key], d, in.Now)
			if pr.P5 > pr.Cap5-p.Hysteresis+1e-9 || pr.P7 > pr.Cap7-p.Hysteresis+1e-9 {
				fail(in, "destination %s projects 5h %.1f / 7d %.1f with the moved burn (caps %v/%v)", key, pr.P5, pr.P7, pr.Cap5, pr.Cap7)
			}
		}
	}
	r1 := simulate(st, pat, uint64(1000+i), check)
	if r1.IntDelivered != r1.IntMoved {
		errs = append(errs, fmt.Sprintf("%s %s seed %d: %v of %v moved interactive sessions run on the new account", st.name, pat.name, 1000+i, r1.IntDelivered, r1.IntMoved))
	}
	if r2 := simulate(st, pat, uint64(1000+i), nil); r1 != r2 {
		errs = append(errs, fmt.Sprintf("%s %s seed %d: not deterministic", st.name, pat.name, 1000+i))
	}
	return errs
}

// The patterns' names are unique (the tables key on them).
func TestSimPatternsUnique(t *testing.T) {
	var ns []string
	for _, p := range simPatterns {
		ns = append(ns, p.name)
	}
	sort.Strings(ns)
	for i := 1; i < len(ns); i++ {
		if ns[i] == ns[i-1] {
			t.Fatalf("pattern %q twice", ns[i])
		}
	}
}

// newTurn starts a turn's steps: the model's first gap.
func (l *simLane) newTurn() { l.inTool, l.stepLeft, l.stepDone = false, uniform(l.sr, 5, 30), 0 }

// steps advances the turn by secs seconds of tool calls (60% 2-10 s, 30% 30-90 s, 10% 3-10
// min builds and tests) and model gaps (5-30 s) between them.
func (l *simLane) steps(secs float64) {
	for secs > 0 {
		if l.stepLeft <= 0 {
			if l.inTool {
				l.inTool, l.stepLeft = false, uniform(l.sr, 5, 30)
			} else {
				l.inTool, l.stepDone = true, 0
				switch x := l.sr.Float64(); {
				case x < 0.6:
					l.stepLeft = uniform(l.sr, 2, 10)
				case x < 0.9:
					l.stepLeft = uniform(l.sr, 30, 90)
				default:
					l.stepLeft = uniform(l.sr, 180, 600)
				}
			}
		}
		d := math.Min(secs, l.stepLeft)
		l.stepLeft -= d
		if l.inTool {
			l.stepDone += d
		}
		secs -= d
	}
}
