package picker

// Exhaustive scenario verification of the drain order (owner 2026-11-24): a combinatorial
// generator over seven accounts and the oracle invariants I1-I10 checked on every case, plus
// owner-style stories. The generator covers the full product of the focus account's weekly
// level × hours to reset × 5-hour level × 5-hour reset × running lanes (9 × 10 × 6 × 3 × 4 =
// 6480 cases); every other dimension (modes, the other accounts, reading state, batch size,
// --urgent, lane statistics) is drawn from a seeded source.

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var (
	genWeekly = []float64{0, 20, 50, 70, 85, 92, 96, 98, 99}
	genHours  = []float64{0.2, 1, 3, 6, 10, 20, 30, 50, 100, 160}
	gen5h     = []float64{0, 30, 60, 85, 94, 97}
	genT5     = []float64{0.1, 2.5, 0} // soon, mid, not started
	genBatch  = []int{1, 3, 6, 10}
	genModes  = [][]bool{ // protected per claude-1..7
		{true, false, false, false, true, false, true}, // the owner's: 1/5/7 protected
		{false, false, false, false, false, false, false},
		{true, false, true, true, true, true, true},
		{true, true, false, false, true, false, true},
	}
	genLanes   = []string{"none", "small", "long", "mixed"}
	genReading = []string{"fresh", "stale", "unknown", "hubdown"}
)

type genCase struct {
	id       int
	desc     string
	in       LaneInput
	lanesOf  map[string]string
	reading  map[string]string
	smallAcc map[string]bool
}

// genRunning adds the running lanes of a kind to key: many small (0.5-2 points, 5-15 min,
// measured), a few long (hours, measured), or both; some small ones ended.
func genRunning(rng *rand.Rand, key, kind string, now time.Time, f5 float64) []LaneLease {
	var out []LaneLease
	small := func() {
		n := 5 + rng.Intn(16)
		for i := 0; i < n; i++ {
			lenMin := 5 + rng.Float64()*10
			cost := 0.5 + rng.Float64()*1.5
			age := rng.Float64() * 20
			L := LaneLease{Key: key, T0: now.Add(-time.Duration(age * float64(time.Minute))), Launched: true, RecentOK: true}
			rate := cost / (lenMin / 60)
			if age >= lenMin {
				L.Ended, L.Seen, L.Burned = true, true, cost/f5
			} else {
				L.Seen = age > 0.5
				L.Burned = rate * age / 60 / f5
				L.Recent = rate * math.Min(age, 15) / 60 / f5
			}
			out = append(out, L)
		}
	}
	long := func() {
		n := 1 + rng.Intn(3)
		for i := 0; i < n; i++ {
			age := 60 + rng.Float64()*180
			rate := 2 + rng.Float64()*8
			out = append(out, LaneLease{Key: key, T0: now.Add(-time.Duration(age * float64(time.Minute))), Launched: true, Seen: true,
				RecentOK: true, Burned: rate * age / 60 / f5, Recent: rate * 0.25 / f5})
		}
	}
	switch kind {
	case "small":
		small()
	case "long":
		long()
	case "mixed":
		small()
		long()
	}
	return out
}

func genCases(seed int64) []genCase {
	rng := rand.New(rand.NewSource(seed))
	var out []genCase
	total := len(genWeekly) * len(genHours) * len(gen5h) * len(genT5) * len(genLanes)
	for i := 0; i < total; i++ {
		fw := genWeekly[i%9]
		fh := genHours[(i/9)%10]
		f5 := gen5h[(i/90)%6]
		ft5 := genT5[(i/540)%3]
		fl := genLanes[(i/1620)%4]
		modes := genModes[rng.Intn(len(genModes))]
		focus := rng.Intn(7)
		hubDown := rng.Intn(8) == 0
		c := genCase{id: i, lanesOf: map[string]string{}, reading: map[string]string{}, smallAcc: map[string]bool{}}
		in := LaneInput{Now: lnow, Params: DefaultLaneParams, N: genBatch[rng.Intn(4)], Urgent: rng.Intn(4) == 0, HubDown: hubDown}
		switch rng.Intn(3) {
		case 1:
			in.Stats = LaneStats{Cost: 2, Mean: 1.2, Len: 10 * time.Minute, N: 40, NLen: 40}
		case 2:
			in.Stats = LaneStats{Cost: 16, Mean: 11, Len: 40 * time.Minute, N: 20, NLen: 20}
		}
		var desc []string
		for k, name := range allNames {
			w, H, h, t5, lanes := genWeekly[rng.Intn(9)], genHours[rng.Intn(10)], gen5h[rng.Intn(6)], genT5[rng.Intn(3)], genLanes[rng.Intn(4)]
			if k == focus {
				w, H, h, t5, lanes = fw, fh, f5, ft5, fl
			}
			rd := genReading[rng.Intn(3)]
			if rng.Intn(3) > 0 {
				rd = "fresh"
			}
			if hubDown {
				rd = "hubdown"
			}
			s := dsmp(w, h, H, t5)
			acc := LaneAccount{Key: name, Ord: k, Protected: modes[k], Meter: LaneMeter{KeepH: true}}
			switch rd {
			case "stale":
				s.TS = lnow.Add(-90 * time.Minute)
				acc.Live = true
			case "unknown":
				s.Weekly = nil
			case "hubdown":
				s.TS = lnow.Add(-40 * time.Minute)
			}
			if rng.Intn(3) == 0 {
				acc.Ratio = 5 + rng.Float64()*5
			}
			acc.Sample = s
			in.Accounts = append(in.Accounts, acc)
			in.Leases = append(in.Leases, genRunning(rng, name, lanes, lnow, DefaultLaneParams.F5)...)
			// some recent leases for burst caps
			for j := rng.Intn(8); j > 0; j-- {
				L := dlease(name, 5+rng.Float64()*50)
				L.Ended, L.Seen, L.RecentOK = true, true, true
				in.Leases = append(in.Leases, L)
			}
			c.lanesOf[name], c.reading[name] = lanes, rd
			mode := "A"
			if modes[k] {
				mode = "P"
			}
			desc = append(desc, fmt.Sprintf("%s:%s w%.0f H%.1f h%.0f t5 %.1f %s %s", name, mode, w, H, h, t5, lanes, rd))
		}
		c.desc = fmt.Sprintf("case %d (seed %d) N=%d urgent=%v hubdown=%v stats=%+v\n  %s", i, seed, in.N, in.Urgent, hubDown, in.Stats,
			strings.Join(desc, "\n  "))
		c.in = in
		out = append(out, c)
	}
	return out
}

// stepwise runs a batch one pick at a time, as RankLanes does internally, and returns the
// table before each pick.
func stepwise(t *testing.T, in LaneInput) ([]LaneResult, []LanePick) {
	t.Helper()
	var tables []LaneResult
	var picks []LanePick
	ls := append([]LaneLease(nil), in.Leases...)
	for k := 0; k < in.N; k++ {
		one := in
		one.N, one.Leases = 1, ls
		one.prior = picks
		r := rank(t, one)
		tables = append(tables, r)
		if len(r.Picks) == 0 {
			break
		}
		p := r.Picks[0]
		picks = append(picks, p)
		c5, cf := p.C5, p.CF
		ls = append(ls, LaneLease{Key: p.Key, T0: in.Now, SID: p.SID, C5: &c5, CF: &cf, Spill: p.Spill, Expect: in.Expect})
	}
	return tables, picks
}

func accOf(in LaneInput, key string) LaneAccount {
	for _, a := range in.Accounts {
		if a.Key == key {
			return a
		}
	}
	return LaneAccount{}
}

// checkInvariants checks I1-I10 on one case; it returns the violations.
func checkInvariants(t *testing.T, in LaneInput) []string {
	t.Helper()
	p := in.Params
	var bad []string
	fail := func(f string, a ...any) { bad = append(bad, fmt.Sprintf(f, a...)) }
	r := rank(t, in)
	tables, steps := stepwise(t, in)

	// The batch equals its steps (and is deterministic: I9).
	if len(steps) != len(r.Picks) {
		fail("batch %v, steps %d", picksOf(r), len(steps))
	} else {
		for i := range steps {
			if steps[i].Key != r.Picks[i].Key || steps[i].Drain != r.Picks[i].Drain || steps[i].Forced != r.Picks[i].Forced {
				fail("pick %d: batch %+v, step %+v", i, r.Picks[i], steps[i])
			}
		}
	}
	e, _, _, _ := p.laneStats(in.Stats)
	start := map[string]DrainInfo{}
	for _, row := range r.Table {
		start[row.Key] = row.Drain
	}
	burst := map[string]int{}
	for _, row := range r.Table {
		burst[row.Key] = row.Burst
	}
	got := map[string]int{}
	spills := map[string]int{}
	for i, pk := range steps {
		tb := tables[i]
		acc := accOf(in, pk.Key)
		row := rowOf(tb, pk.Key)
		got[pk.Key]++
		// I1: no protected pick while an aggressive account could take the lane; a protected
		// drain pick only after every aggressive drain account is at capacity.
		if acc.Protected && row.Class == ClassDrain {
			// The exception (owner Q3): a protected account in 1x drain, after every aggressive
			// drain account is at capacity.
			for _, x := range tb.Table {
				if !x.Protected && x.Class == ClassDrain && x.Drain.Capacity > 0 {
					fail("I1: protected drain %s picked while aggressive drain %s had capacity %d", pk.Key, x.Key, x.Drain.Capacity)
				}
			}
		}
		if acc.Protected && row.Class != ClassDrain {
			for _, x := range tb.Table {
				if x.Protected {
					continue
				}
				if !x.Skip {
					fail("I1: protected %s picked while aggressive %s was eligible (%s)", pk.Key, x.Key, x.Class)
				}
				if x.Skip && x.SkipLevel == 0 && x.Capped && row.Class != ClassDrain {
					fail("I1: protected %s picked while aggressive %s was only burst-capped", pk.Key, x.Key)
				}
				if x.Class == ClassDrain && x.Drain.Capacity > 0 {
					fail("I1: protected %s picked while aggressive drain %s had capacity %d", pk.Key, x.Key, x.Drain.Capacity)
				}
			}
			// independent check: an obviously free aggressive account
			for _, a := range in.Accounts {
				if a.Protected || len(cleanLaneSample(a.Sample, in.Now).unknown) > 0 {
					continue
				}
				x := rowOf(tb, a.Key)
				if x.Class != ClassDrain && x.Weekly < 90 && x.FiveHour < 80 && x.Burst < p.BurstAggr && !x.Stale {
					fail("I1: protected %s picked while aggressive %s had clear room (w %.0f h %.0f burst %d)", pk.Key, a.Key, x.Weekly, x.FiveHour, x.Burst)
				}
			}
		}
		// I2: an aggressive drain account with capacity takes the pick.
		for _, x := range tb.Table {
			if !x.Protected && x.Class == ClassDrain && x.Drain.Capacity > 0 && pk.Class != ClassDrain {
				fail("I2: pick %s (%s) while aggressive drain %s had capacity %d", pk.Key, pk.Class, x.Key, x.Drain.Capacity)
			}
		}
		// I4: no pick past the hard limits; the 5-hour backstop; the landing guard.
		if pk.Drain {
			d := row.Drain
			lim5 := 98.0
			if acc.Protected {
				lim5 = p.Cap5h - p.Lane5h
			}
			soon := row.FiveReset.IsZero() || !row.FiveReset.After(in.Now) || row.FiveReset.Sub(in.Now) <= p.FreshSoon
			if d.WM >= 98 || d.HM >= lim5 || (!soon && d.HM >= p.Backstop5h) {
				fail("I4: drain pick %s at measured w %.1f h %.1f", pk.Key, d.WM, d.HM)
			}
			if d.WM+e/p.account(acc).ratio() > p.LandMax {
				fail("I4: drain pick %s lands over %.0f", pk.Key, p.LandMax)
			}
		} else if !acc.Protected {
			if row.Weekly >= 98 || row.FiveHour >= 98 {
				fail("I4: pick %s at w %.1f h %.1f", pk.Key, row.Weekly, row.FiveHour)
			}
			if len(row.Unknown) == 0 && row.Weekly+e/p.account(acc).ratio() > p.LandMax {
				fail("I4: pick %s lands over %.0f", pk.Key, p.LandMax)
			}
		}
		// I10: finish_by on every drain pick, none otherwise.
		if pk.Drain {
			if pk.FinishBy.IsZero() || !pk.FinishBy.Equal(pk.Reset.Add(-p.Guard)) || pk.Reset.Sub(in.Now.Add(hoursDur(row.HoursToReset))).Abs() > time.Second {
				fail("I10: drain pick %s finish_by %v reset %v", pk.Key, pk.FinishBy, pk.Reset)
			}
			if pk.Spill {
				spills[pk.Key]++
			}
		} else if !pk.FinishBy.IsZero() || !pk.Reset.IsZero() {
			fail("I10: non-drain pick %s carries finish_by", pk.Key)
		}
		// I7: a non-drain account keeps its burst cap (urgent passes only an aggressive one).
		if !pk.Drain {
			cp := p.burstCap(acc.Protected)
			if burst[pk.Key]+got[pk.Key]-1 >= cp && !(pk.Forced && !acc.Protected) {
				fail("I7: %s past its burst cap %d without --urgent", pk.Key, cp)
			}
		}
		// I8: unknown aggressive before protected (outside drain: a protected drain pick is the
		// I1 exception); protected drain only at H <= 1x need.
		if acc.Protected && row.Class != ClassDrain {
			for _, x := range tb.Table {
				if !x.Protected && x.Class == ClassUnknown && !x.Skip {
					fail("I8: protected %s before unknown aggressive %s", pk.Key, x.Key)
				}
			}
		}
		if pk.Forced && acc.Protected {
			fail("I8: --urgent picked protected %s", pk.Key)
		}
	}
	for _, row := range r.Table {
		if row.Protected && row.Drain.On && row.HoursToReset > p.ProtDrainSlack*row.Drain.NeedH+1e-9 {
			fail("I8: protected %s drains at H %.2f > need %.2f", row.Key, row.HoursToReset, row.Drain.NeedH)
		}
		// I7: a draining account is never held by the burst cap.
		if row.Class == ClassDrain && row.Capped {
			fail("I7: drain %s held by its burst cap", row.Key)
		}
	}
	// I5: drain lanes within capacity; spill lanes within SpillMax.
	for k, d := range start {
		if !d.On {
			continue
		}
		n := 0
		for _, pk := range steps {
			if pk.Key == k && pk.Drain {
				n++
			}
		}
		if n > max(d.Capacity, 0) {
			fail("I5: %s got %d drain lanes, capacity %d", k, n, d.Capacity)
		}
		if spills[k] > p.SpillMax {
			fail("I5: %s got %d spill lanes", k, spills[k])
		}
	}
	// I3: every aggressive drain account with capacity gets a lane when the batch is big enough.
	var withCap []string
	for _, row := range r.Table {
		if !row.Protected && row.Class == ClassDrain && row.Drain.Capacity > 0 {
			withCap = append(withCap, row.Key)
		}
	}
	if len(steps) >= len(withCap) {
		for _, k := range withCap {
			if got[k] == 0 {
				fail("I3: drain %s with capacity %d starved (%v)", k, start[k].Capacity, picksOf(r))
			}
		}
	}
	// I9: determinism and stability under permutation and relabelling.
	r2 := rank(t, in)
	if !reflect.DeepEqual(picksOf(r), picksOf(r2)) || r.Waiting != r2.Waiting {
		fail("I9: not deterministic: %v vs %v", picksOf(r), picksOf(r2))
	}
	perm := in
	perm.Accounts = append([]LaneAccount(nil), in.Accounts...)
	sort.SliceStable(perm.Accounts, func(i, j int) bool { return perm.Accounts[i].Key > perm.Accounts[j].Key })
	if rp := rank(t, perm); !reflect.DeepEqual(picksOf(r), picksOf(rp)) {
		fail("I9: account order changed the picks: %v vs %v", picksOf(r), picksOf(rp))
	}
	rel := relabel(in)
	rr := rank(t, rel)
	want := picksOf(r)
	for i := range want {
		want[i] = "x-" + want[i]
	}
	if !reflect.DeepEqual(want, picksOf(rr)) {
		fail("I9: relabelling changed the picks: %v vs %v", picksOf(r), picksOf(rr))
	}
	return bad
}

func relabel(in LaneInput) LaneInput {
	out := in
	out.Accounts = nil
	for _, a := range in.Accounts {
		a.Key = "x-" + a.Key
		out.Accounts = append(out.Accounts, a)
	}
	out.Leases = nil
	for _, L := range in.Leases {
		L.Key = "x-" + L.Key
		out.Leases = append(out.Leases, L)
	}
	return out
}

// TestDrainInvariantsGenerated: at least 5,000 generated cases, every invariant on each.
func TestDrainInvariantsGenerated(t *testing.T) {
	cases := genCases(20261124)
	if len(cases) < 5000 {
		t.Fatalf("%d cases", len(cases))
	}
	fails, picks, drainPicks, protPicksN, waits := 0, 0, 0, 0, 0
	for _, c := range cases {
		bad := checkInvariants(t, c.in)
		r := rank(t, c.in)
		picks += len(r.Picks)
		waits += r.Waiting
		for _, p := range r.Picks {
			if p.Drain {
				drainPicks++
			}
			if accOf(c.in, p.Key).Protected {
				protPicksN++
			}
		}
		if len(bad) > 0 {
			fails++
			if fails <= 10 {
				t.Errorf("%s\n  picks %v\n  %s", c.desc, picksOf(r), strings.Join(bad, "\n  "))
			}
		}
	}
	t.Logf("generated: %d cases, %d picks (%d drain, %d protected), %d waiting; %d cases with a violation", len(cases), picks, drainPicks, protPicksN, waits, fails)
}

// I6: twenty tiny lanes running on a draining account with room left do not block it.
func TestDrainI6SmallLanes(t *testing.T) {
	s := dworld()
	s[dX] = dsmp(85, 40, 8, 3)
	in := laneIn(s, 1)
	in.Stats = LaneStats{Cost: 2, Mean: 1.2, Len: 10 * time.Minute, N: 40, NLen: 40}
	for i := 0; i < 20; i++ {
		in.Leases = append(in.Leases, seenLease(dX, 6+float64(i%5), 6))
	}
	r := rank(t, in)
	d := drainOf(t, r, dX)
	if r.Picks[0].Key != dX || d.Capacity < 5 {
		t.Errorf("20 tiny lanes blocked X: pick %s drain %+v", r.Picks[0].Key, d)
	}
	// The draft's fixed charges (8 points each) would have blocked it.
	in.Params = DraftLaneParams
	if p := rank(t, in).Picks[0].Key; p == dX {
		t.Logf("the draft also picked X")
	}
}
