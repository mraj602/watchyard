package picker

// Scenario tests of the drain order (setup.md 8.5, Order and Drain), one per scenario of the
// v3 design catalogue (S01-S72). X is claude-2, Y claude-3, Z claude-4 (aggressive), P a
// protected account. Unless a case says otherwise: fallback lane statistics (E = Ē = 8 points,
// 15 minutes, so a new lane burns 32 points per hour), ratio 7.2, readings 5 minutes old, the
// other aggressive accounts at 20% with 140 hours left and the protected ones at 20% with 140
// hours left.

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/leases"
)

const (
	dX = "claude-2"
	dY = "claude-3"
	dZ = "claude-4"
	dW = "claude-6"
	dP = "claude-5"
)

// dsmp is a reading: weekly w, 5-hour h, the weekly reset in H hours, the 5-hour reset in t5
// hours (0: no 5-hour window open).
func dsmp(w, h, H, t5 float64) LaneSample {
	s := LaneSample{TS: hrs(-5.0 / 60), Weekly: fp(w), FiveHour: fp(h), WeeklyReset: hrs(H)}
	if t5 > 0 {
		s.FiveHourReset = hrs(t5)
	}
	return s
}

func dworld() map[string]LaneSample {
	s := map[string]LaneSample{}
	for _, a := range allNames {
		s[a] = dsmp(20, 10, 140, 3)
	}
	return s
}

// dlease is a launched lease picked ageMin minutes ago, not measured.
func dlease(key string, ageMin float64) LaneLease {
	return LaneLease{Key: key, T0: hrs(-ageMin / 60), Launched: true}
}

// seenLease is a launched lease whose session is measured burning ptsPerH 5-hour points per
// hour (default meter factor).
func seenLease(key string, ageMin, ptsPerH float64) LaneLease {
	L := dlease(key, ageMin)
	win := math.Min(ageMin, 15) / 60
	L.Seen, L.RecentOK = true, true
	L.Recent = ptsPerH * win / DefaultLaneParams.F5
	L.Burned = ptsPerH * ageMin / 60 / DefaultLaneParams.F5
	return L
}

// endedLease is a lease whose session ended and was measured (its use is in the meter).
func endedLease(key string, ageMin float64) LaneLease {
	L := dlease(key, ageMin)
	L.Ended, L.Seen, L.RecentOK = true, true, true
	return L
}

func picksOf(r LaneResult) []string {
	var out []string
	for _, p := range r.Picks {
		out = append(out, p.Key)
	}
	return out
}

func countOf(r LaneResult, key string) int {
	n := 0
	for _, p := range r.Picks {
		if p.Key == key {
			n++
		}
	}
	return n
}

func protPicks(r LaneResult) int {
	n := 0
	for _, p := range r.Picks {
		if isProt(p.Key) {
			n++
		}
	}
	return n
}

func drainOf(t *testing.T, r LaneResult, key string) DrainInfo {
	t.Helper()
	return rowOf(r, key).Drain
}

func near1(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestDrainScenarios(t *testing.T) {
	type sc struct {
		name string
		run  func(t *testing.T)
	}
	cases := []sc{
		// A. One expiring account.
		{"S01 11-23 incident shape: X w87 h45 H9, 3 young lanes, batch 6", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(87, 45, 9, 3)
			in := laneIn(s, 6)
			in.Leases = []LaneLease{dlease(dX, 18), dlease(dX, 18), dlease(dX, 18)}
			r := rank(t, in)
			// Unmeasured lanes 18 minutes old: their use since the reading (5 min at 32 pts/h) and
			// their expected rest (18 min more) count: 3 × 12.3 = 37 of the 53 5-hour points left,
			// less the 2-point 5-hour margin.
			d := drainOf(t, r, dX)
			if !d.On || d.Capacity != 1 || d.LimitedBy != Limit5h {
				t.Fatalf("drain %+v", d)
			}
			got := picksOf(r)
			if len(got) != 6 || countOf(r, dX) != 1 || got[0] != dX || protPicks(r) != 0 {
				t.Errorf("picks %v: X to its 5-hour capacity (1), the rest aggressive, never protected", got)
			}
			if !r.Picks[0].Drain || !r.Picks[0].FinishBy.Equal(hrs(9).Add(-10*time.Minute)) {
				t.Errorf("finish_by %v", r.Picks[0].FinishBy)
			}
		}},
		{"S02 the same under rr_exp sends X last and reaches protected accounts", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(87, 45, 9, 3)
			in := laneIn(s, 6)
			in.Params = ReferenceLaneParams
			in.Leases = []LaneLease{dlease(dX, 18), dlease(dX, 18), dlease(dX, 18)}
			r := rank(t, in)
			if countOf(r, dX) != 0 || protPicks(r) == 0 {
				t.Errorf("rr_exp picks %v: expected the old failure (X skipped, protected used)", picksOf(r))
			}
		}},
		{"S03 X w90 h10 H20, one lane", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(90, 10, 20, 3)
			r := rank(t, laneIn(s, 1))
			if r.Picks[0].Key != dX || !r.Picks[0].Drain {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		{"S04 X w60 H40: not drain (horizon 24 h), at risk and first", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(60, 10, 40, 3)
			r := rank(t, laneIn(s, 1))
			row := rowOf(r, dX)
			if row.Drain.On || row.Class != ClassAtRisk || r.Picks[0].Key != dX {
				t.Errorf("class %s drain %v pick %s", row.Class, row.Drain.On, r.Picks[0].Key)
			}
		}},
		{"S05 X w30 H72: at risk, first", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(30, 10, 72, 3)
			r := rank(t, laneIn(s, 1))
			if rowOf(r, dX).Class != ClassAtRisk || r.Picks[0].Key != dX {
				t.Errorf("class %s pick %s", rowOf(r, dX).Class, r.Picks[0].Key)
			}
		}},
		{"S06 X w97.5 H5: under the minimum drain room, a normal pick the landing guard allows", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(97.5, 10, 5, 3)
			r := rank(t, laneIn(s, 1))
			row := rowOf(r, dX)
			if row.Drain.On || row.Skip {
				t.Errorf("X drain %v skip %v %s", row.Drain.On, row.Skip, row.Reason)
			}
		}},
		{"S07 X w96 H2: the end game gives exactly one lane", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(96, 10, 2, 3)
			r := rank(t, laneIn(s, 3))
			if countOf(r, dX) != 1 || r.Picks[0].Key != dX {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		{"S08 X w60 h20 H3: 5-hour capacity, unavoidable waste shown", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(60, 20, 3, 3)
			r := rank(t, laneIn(s, 6))
			d := drainOf(t, r, dX)
			if countOf(r, dX) != 6 || d.Capacity != 9 || d.Unavoidable < 20 {
				t.Errorf("picks %v capacity %d unavoidable %.1f", picksOf(r), d.Capacity, d.Unavoidable)
			}
		}},
		{"S09 X w85 H0.5: six lanes with a deadline 20 minutes away", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 10, 0.5, 3)
			r := rank(t, laneIn(s, 6))
			if countOf(r, dX) != 6 {
				t.Errorf("picks %v", picksOf(r))
			}
			if fb := r.Picks[0].FinishBy; !fb.Equal(hrs(0.5).Add(-10 * time.Minute)) {
				t.Errorf("finish_by %v", fb)
			}
		}},
		{"S10 X w85 H0.1: inside the guard, no drain", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 10, 0.1, 3)
			r := rank(t, laneIn(s, 1))
			if rowOf(r, dX).Drain.On || r.Picks[0].Drain {
				t.Errorf("drain inside the guard")
			}
		}},
		{"S11 X w50 h95 t5 4 H20: 5h full until the reset, free_at = 5h reset - 15 min", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(50, 95, 20, 4)
			r := rank(t, laneIn(s, 1))
			row := rowOf(r, dX)
			if !row.Drain.On || !row.Skip || !strings.Contains(row.Reason, "5h full until") || r.Picks[0].Key == dX {
				t.Fatalf("row %+v", row)
			}
			if row.ETA == nil || !row.ETA.Equal(hrs(4).Add(-15*time.Minute)) {
				t.Errorf("eta %v", row.ETA)
			}
		}},
		{"S12 lease charges would demote X (h 67 measured, 90 charged; its 3 lanes burn 2 pts/h): X stays first", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(80, 67, 20, 4)
			in := laneIn(s, 1)
			in.Leases = []LaneLease{seenLease(dX, 6, 2), seenLease(dX, 6, 2), seenLease(dX, 6, 2)}
			r := rank(t, in)
			if r.Picks[0].Key != dX || rowOf(r, dX).FiveHour < 90 {
				t.Errorf("pick %s, charged h %.1f", r.Picks[0].Key, rowOf(r, dX).FiveHour)
			}
			in.Params = DraftLaneParams
			if p := rank(t, in).Picks[0].Key; p == dX {
				t.Errorf("the draft unexpectedly kept X first")
			}
		}},
		{"S13 X h96, 5h resets in 9 min: the last minutes still bound X (no backstop); free at the reset", func(t *testing.T) {
			// The week simulator showed that ignoring a window that resets soon runs lanes into
			// the 5-hour limit: a fresh lane burns r = 32 points an hour, 4.8 in 9 minutes, over
			// the 2 points left.
			s := dworld()
			s[dX] = dsmp(80, 96, 6, 0.15)
			r := rank(t, laneIn(s, 1))
			d := drainOf(t, r, dX)
			rst := hrs(0.15)
			if d.Cap5h != 0 || d.LimitedBy != Limit5h || d.FreeAt == nil || !d.FreeAt.Equal(rst) || protPicks(r) != 0 {
				t.Errorf("pick %v drain %+v", picksOf(r), d)
			}
			s[dX] = dsmp(80, 90, 6, 0.15) // 8 points left: one lane fits, the backstop does not apply
			if d := drainOf(t, rank(t, laneIn(s, 1)), dX); d.Cap5h != 1 {
				t.Errorf("h90: drain %+v", d)
			}
		}},
		{"S14 X h0 w85 H10: weekly capacity, no burst cap", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 0, 10, 3)
			r := rank(t, laneIn(s, 10))
			d := drainOf(t, r, dX)
			if d.Capacity != 10 || countOf(r, dX) != 10 {
				t.Errorf("capacity %d picks %v", d.Capacity, picksOf(r))
			}
		}},
		{"S15 X h97 t5 2 H4: no lane until the 5h reset", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(80, 97, 4, 2)
			r := rank(t, laneIn(s, 1))
			row := rowOf(r, dX)
			if !row.Skip || row.Drain.LimitedBy != Limit5h || row.ETA == nil || !row.ETA.Equal(hrs(2).Add(-15*time.Minute)) {
				t.Errorf("row %+v", row)
			}
		}},
		{"S16 weekly resets before the 5h window: min(weekly, 5h) room", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(90, 60, 1, 3)
			r := rank(t, laneIn(s, 1))
			d := drainOf(t, r, dX)
			if d.Capacity != 4 || d.LimitedBy != Limit5h {
				t.Errorf("drain %+v", d)
			}
		}},
		// B. Several expiring accounts.
		{"S17 X w88 H8 and Y w85 H9 share a batch of 6", func(t *testing.T) {
			s := dworld()
			s[dX], s[dY] = dsmp(88, 10, 8, 3), dsmp(85, 10, 9, 3)
			r := rank(t, laneIn(s, 6))
			if countOf(r, dX) < 2 || countOf(r, dY) < 2 || countOf(r, dX)+countOf(r, dY) != 6 {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		{"S18 X room 5 H2, Y room 30 H3: X to its capacity, Y the rest", func(t *testing.T) {
			s := dworld()
			s[dX], s[dY] = dsmp(93, 10, 2, 3), dsmp(68, 10, 3, 3)
			in := laneIn(s, 6)
			c0 := drainOf(t, rank(t, laneIn(s, 1)), dX).Capacity
			r := rank(t, in)
			if countOf(r, dX) > c0 || countOf(r, dY) < 3 || countOf(r, dX)+countOf(r, dY) != 6 {
				t.Errorf("picks %v (X capacity %d)", picksOf(r), c0)
			}
		}},
		{"S19 three drain accounts, batch 10: none over capacity, none starved", func(t *testing.T) {
			s := dworld()
			s[dX], s[dY], s[dZ] = dsmp(95.5, 10, 5, 3), dsmp(94, 10, 6, 3), dsmp(80, 10, 7, 3)
			r1 := rank(t, laneIn(s, 1))
			r := rank(t, laneIn(s, 10))
			for _, k := range []string{dX, dY, dZ} {
				c := drainOf(t, r1, k).Capacity
				if n := countOf(r, k); n == 0 || n > c {
					t.Errorf("%s: %d picks, capacity %d (%v)", k, n, c, picksOf(r))
				}
			}
		}},
		{"S20 X drain then Y at risk", func(t *testing.T) {
			s := dworld()
			s[dX], s[dY] = dsmp(88, 10, 10, 3), dsmp(30, 10, 60, 3)
			r := rank(t, laneIn(s, 1))
			if r.Table[0].Key != dX || r.Table[1].Key != dY || r.Table[1].Class != ClassAtRisk {
				t.Errorf("order %s %s (%s)", r.Table[0].Key, r.Table[1].Key, r.Table[1].Class)
			}
		}},
		{"S21 equal deficit: the earlier finish_by first", func(t *testing.T) {
			s := dworld()
			s[dX], s[dY] = dsmp(88, 10, 9, 3), dsmp(88, 10, 8, 3)
			r := rank(t, laneIn(s, 1))
			if dx, dy := drainOf(t, r, dX), drainOf(t, r, dY); dx.Deficit != dy.Deficit || r.Picks[0].Key != dY {
				t.Errorf("deficits %d %d pick %s", dx.Deficit, dy.Deficit, r.Picks[0].Key)
			}
		}},
		{"S22 claude-6 expiring, claude-7 protected in the same org: independent", func(t *testing.T) {
			s := dworld()
			s[dW] = dsmp(88, 10, 8, 3)
			r := rank(t, laneIn(s, 3))
			if countOf(r, dW) != 3 || protPicks(r) != 0 {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		// C. Lanes already running.
		{"S23 X room 5, five young lanes: drain full", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(92, 10, 8, 3)
			in := laneIn(s, 1)
			for i := 0; i < 5; i++ {
				in.Leases = append(in.Leases, dlease(dX, 2))
			}
			r := rank(t, in)
			row := rowOf(r, dX)
			if !row.Skip || row.Drain.Capacity != 0 || !strings.Contains(row.Reason, "drain full") {
				t.Errorf("row %+v", row)
			}
		}},
		{"S24 three 3-hour-old lanes at 5 pts/h: the Lindy forward burn", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 10, 8, 3)
			in := laneIn(s, 1)
			for i := 0; i < 3; i++ {
				in.Leases = append(in.Leases, seenLease(dX, 180, 5))
			}
			d := drainOf(t, rank(t, in), dX)
			// each runs about 3 h more, cut at its lease life (2 h left): 3 × 5 × 2 = 30
			if !near1(d.ForwardBurn, 30, 0.5) || d.CapWeekly != 4 {
				t.Errorf("drain %+v", d)
			}
		}},
		{"S25 another machine's lanes on X count through the hub burn", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 10, 8, 3)
			in := laneIn(s, 1)
			L := seenLease(dX, 30, 20)
			L.Remote = true
			in.Leases = []LaneLease{L}
			d := drainOf(t, rank(t, in), dX)
			if d.Running != 1 || !near1(d.ForwardBurn, 20*0.5, 0.2) {
				t.Errorf("drain %+v", d)
			}
		}},
		{"S26 hub down: capacity halved, margin doubled", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 0, 10, 3)
			in := laneIn(s, 1)
			up := drainOf(t, rank(t, in), dX)
			in.HubDown = true
			down := drainOf(t, rank(t, in), dX)
			if down.Capacity > (up.Capacity+1)/2 || !near1(up.RoomPct-down.RoomPct, 1, 1e-9) {
				t.Errorf("up %+v down %+v", up, down)
			}
		}},
		{"S27 lanes leased yesterday in normal mode do not block the drain", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 10, 8, 3)
			in := laneIn(s, 1)
			in.Leases = []LaneLease{seenLease(dX, 240, 2), seenLease(dX, 240, 2)}
			r := rank(t, in)
			if r.Picks[0].Key != dX || drainOf(t, r, dX).Capacity < 5 {
				t.Errorf("pick %s drain %+v", r.Picks[0].Key, drainOf(t, r, dX))
			}
		}},
		{"S28 an unlaunched lease counts for 10 minutes", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 10, 8, 3)
			in := laneIn(s, 1)
			L := dlease(dX, 5)
			L.Launched = false
			in.Leases = []LaneLease{L}
			if d := drainOf(t, rank(t, in), dX); d.Running != 1 {
				t.Errorf("running %d", d.Running)
			}
			in.Leases[0].T0 = hrs(-11.0 / 60)
			if d := drainOf(t, rank(t, in), dX); d.Running != 0 {
				t.Errorf("an 11-minute unlaunched lease still runs: %d", d.Running)
			}
		}},
		{"S29 ended lanes free their capacity at once", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 10, 8, 3)
			in := laneIn(s, 1)
			free := drainOf(t, rank(t, in), dX).Capacity
			for i := 0; i < 5; i++ {
				in.Leases = append(in.Leases, endedLease(dX, 6))
			}
			if d := drainOf(t, rank(t, in), dX); d.Capacity != free || d.Running != 0 {
				t.Errorf("capacity %d, want %d", d.Capacity, free)
			}
		}},
		// D. Batches.
		{"S30 batch 1, X drain", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 10, 8, 3)
			if p := rank(t, laneIn(s, 1)).Picks[0].Key; p != dX {
				t.Errorf("pick %s", p)
			}
		}},
		{"S31 batch 10, X room 2% H3: one lane, nine aggressive", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(96, 10, 3, 3)
			r := rank(t, laneIn(s, 10))
			if countOf(r, dX) != 1 || protPicks(r) != 0 || len(r.Picks) != 10 {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		{"S32 batch 10, X room 20 H1: X takes what one hour absorbs, past the burst cap", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(78, 10, 1, 3)
			r := rank(t, laneIn(s, 10))
			if countOf(r, dX) != 10 {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		{"S33 only X has room and it fills: protected only after X is at capacity", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(92, 10, 5, 3)
			for _, a := range []string{dY, dZ, dW} {
				s[a] = dsmp(98.5, 10, 100, 3)
			}
			c := drainOf(t, rank(t, laneIn(s, 1)), dX).Capacity
			r := rank(t, laneIn(s, 10))
			got := picksOf(r)
			for i, k := range got {
				if i < c && k != dX {
					t.Errorf("picks %v: X (capacity %d) first", got, c)
				}
			}
			if countOf(r, dX) != c || protPicks(r) != len(got)-c {
				t.Errorf("picks %v (capacity %d)", got, c)
			}
		}},
		{"S34 --urgent: X up to its capacity, others after", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 0, 9, 3)
			in := laneIn(s, 10)
			in.Urgent = true
			for i := 0; i < 4; i++ {
				in.Leases = append(in.Leases, endedLease(dX, 30))
			}
			c := drainOf(t, rank(t, laneIn(s, 1)), dX).Capacity
			r := rank(t, in)
			if countOf(r, dX) != min(c, 10) || r.Picks[0].Forced {
				t.Errorf("picks %v capacity %d", picksOf(r), c)
			}
		}},
		{"S35 two batches five minutes apart count each other", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(92, 10, 6, 3)
			c := drainOf(t, rank(t, laneIn(s, 1)), dX).Capacity
			in := laneIn(s, 6)
			r1 := rank(t, in)
			in2 := laneIn(s, 6)
			for _, p := range r1.Picks {
				L := dlease(p.Key, 5)
				in2.Leases = append(in2.Leases, L)
			}
			r2 := rank(t, in2)
			if countOf(r1, dX)+countOf(r2, dX) > c {
				t.Errorf("X got %d + %d, capacity %d", countOf(r1, dX), countOf(r2, dX), c)
			}
		}},
		// E. Stale or missing readings, hub down.
		{"S36 stale reading: one drain lane per fresh reading", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 10, 8, 3)
			s[dX] = func() LaneSample { x := s[dX]; x.TS = hrs(-1.5); return x }()
			in := laneIn(s, 3)
			in.Accounts[1].Live = true
			r := rank(t, in)
			if countOf(r, dX) != 1 {
				t.Errorf("picks %v", picksOf(r))
			}
			in.Leases = []LaneLease{dlease(dX, 30)}
			r = rank(t, in)
			if countOf(r, dX) != 0 || drainOf(t, r, dX).LimitedBy != LimitStale {
				t.Errorf("after a lane since the reading: %v %+v", picksOf(r), drainOf(t, r, dX))
			}
		}},
		{"S37 weekly reading missing: no drain", func(t *testing.T) {
			s := dworld()
			x := dsmp(85, 10, 8, 3)
			x.Weekly = nil
			s[dX] = x
			r := rank(t, laneIn(s, 1))
			if rowOf(r, dX).Drain.On || rowOf(r, dX).Class != ClassUnknown {
				t.Errorf("row %+v", rowOf(r, dX))
			}
		}},
		{"S38 weekly resets_at unknown: no drain", func(t *testing.T) {
			s := dworld()
			x := dsmp(85, 10, 8, 3)
			x.WeeklyReset = time.Time{}
			s[dX] = x
			if rowOf(rank(t, laneIn(s, 1)), dX).Drain.On {
				t.Errorf("drained with no reset time")
			}
		}},
		{"S39 a reading from before the weekly reset: a fresh week, no drain", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 10, -1, 3)
			if rowOf(rank(t, laneIn(s, 1)), dX).Drain.On {
				t.Errorf("drained an old week")
			}
		}},
		{"S40 claude -p only (no readings since it ran): stale rule caps it", func(t *testing.T) {
			s := dworld()
			x := dsmp(85, 10, 8, 3)
			x.TS = hrs(-2)
			s[dX] = x
			in := laneIn(s, 4)
			in.Accounts[1].Live = true
			if n := countOf(rank(t, in), dX); n != 1 {
				t.Errorf("%d lanes on a stale account", n)
			}
		}},
		{"S41 hub down with 40-minute-old readings: margin 2", func(t *testing.T) {
			s := dworld()
			x := dsmp(85, 10, 8, 3)
			x.TS = hrs(-40.0 / 60)
			s[dX] = x
			in := laneIn(s, 1)
			in.HubDown = true
			if d := drainOf(t, rank(t, in), dX); !near1(d.RoomPct, 98-2-85, 1e-9) {
				t.Errorf("room %.2f", d.RoomPct)
			}
		}},
		{"S42 no lane statistics: the fallbacks (8 points, 15 minutes)", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 0, 10, 3)
			if d := drainOf(t, rank(t, laneIn(s, 1)), dX); d.CapWeekly != 10 {
				t.Errorf("capacity %d", d.CapWeekly)
			}
		}},
		// F. Protected accounts.
		{"S43 protected with 30% room 2 h before its reset: drain at 1x (owner Q3)", func(t *testing.T) {
			s := dworld()
			s[dP] = dsmp(68, 10, 2, 3)
			r := rank(t, laneIn(s, 1))
			if !drainOf(t, r, dP).On || r.Picks[0].Key != dP {
				t.Errorf("pick %s drain %+v", r.Picks[0].Key, drainOf(t, r, dP))
			}
			s[dP] = dsmp(68, 10, 30, 3) // full speed could use it: not drain, protected last
			r = rank(t, laneIn(s, 1))
			if drainOf(t, r, dP).On || isProt(r.Picks[0].Key) {
				t.Errorf("pick %s", r.Picks[0].Key)
			}
		}},
		{"S44 every aggressive account at weekly 98: protected", func(t *testing.T) {
			s := dworld()
			for _, a := range aggrNames {
				s[a] = dsmp(98.2, 10, 100, 3)
			}
			if p := rank(t, laneIn(s, 1)).Picks[0].Key; !isProt(p) {
				t.Errorf("pick %s", p)
			}
		}},
		{"S45 every aggressive account burst-capped with room: wait (owner Q1)", func(t *testing.T) {
			s := dworld()
			in := laneIn(s, 1)
			for _, a := range aggrNames {
				for i := 0; i < 6; i++ {
					in.Leases = append(in.Leases, endedLease(a, 30))
				}
			}
			r := rank(t, in)
			if len(r.Picks) != 0 || r.Waiting != 1 || r.NextFree == nil {
				t.Errorf("picks %v waiting %d", picksOf(r), r.Waiting)
			}
		}},
		{"S46 protected at 77% resetting in 28 h: not used", func(t *testing.T) {
			s := dworld()
			s[dP] = dsmp(77, 10, 28, 3)
			r := rank(t, laneIn(s, 6))
			if countOf(r, dP) != 0 || drainOf(t, r, dP).On {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		{"S47 X at drain capacity, Y/Z with room: Y/Z, not protected", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(92, 10, 8, 3)
			in := laneIn(s, 3)
			for i := 0; i < 5; i++ {
				in.Leases = append(in.Leases, dlease(dX, 2))
			}
			r := rank(t, in)
			if countOf(r, dX) != 0 || protPicks(r) != 0 || len(r.Picks) != 3 {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		// G. All aggressive capped or at a limit.
		{"S48 every aggressive 5h full, the first frees in 12 min: wait", func(t *testing.T) {
			s := dworld()
			for _, a := range aggrNames {
				s[a] = dsmp(20, 98.5, 100, 2)
			}
			s[dY] = dsmp(20, 98.5, 100, 0.2)
			r := rank(t, laneIn(s, 1))
			if len(r.Picks) != 0 || r.NextFree == nil || !r.NextFree.Equal(hrs(0.2)) {
				t.Errorf("picks %v next %v", picksOf(r), r.NextFree)
			}
		}},
		{"S49 every aggressive 5h full for 3 h: protected", func(t *testing.T) {
			s := dworld()
			for _, a := range aggrNames {
				s[a] = dsmp(20, 98.5, 100, 3)
			}
			if p := rank(t, laneIn(s, 1)).Picks; len(p) != 1 || !isProt(p[0].Key) {
				t.Errorf("picks %v", p)
			}
		}},
		{"S50 everything at its limits: nothing", func(t *testing.T) {
			s := dworld()
			for _, a := range allNames {
				s[a] = dsmp(98.5, 10, 100, 3)
			}
			if r := rank(t, laneIn(s, 1)); len(r.Picks) != 0 || r.Waiting != 1 {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		// H. --urgent.
		{"S51 X draining with six lanes this hour: no burst cap, not urgent", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 10, 8, 3)
			in := laneIn(s, 1)
			for i := 0; i < 6; i++ {
				in.Leases = append(in.Leases, endedLease(dX, 40))
			}
			r := rank(t, in)
			if r.Picks[0].Key != dX || r.Picks[0].Forced {
				t.Errorf("pick %+v", r.Picks[0])
			}
		}},
		{"S52 X at capacity 0 with --urgent: not X", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(92, 10, 8, 3)
			in := laneIn(s, 1)
			in.Urgent = true
			for i := 0; i < 5; i++ {
				in.Leases = append(in.Leases, dlease(dX, 2))
			}
			if p := rank(t, in).Picks[0].Key; p == dX {
				t.Errorf("urgent passed the drain capacity")
			}
		}},
		{"S53 every aggressive at hard limits, protected over pace, --urgent: nothing", func(t *testing.T) {
			s := dworld()
			for _, a := range allNames {
				s[a] = dsmp(98.5, 10, 100, 3)
			}
			in := laneIn(s, 1)
			in.Urgent = true
			if r := rank(t, in); len(r.Picks) != 0 {
				t.Errorf("picks %v", picksOf(r))
			}
		}},
		// I. Models.
		{"S54 an Opus lane costs twice: about half the capacity", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 0, 10, 3)
			in := laneIn(s, 1)
			son := drainOf(t, rank(t, in), dX).CapWeekly
			in.Stats = LaneStats{Cost: 16, Mean: 16, Model: "opus"}
			opus := drainOf(t, rank(t, in), dX).CapWeekly
			if opus > son/2+1 || opus == 0 {
				t.Errorf("sonnet %d opus %d", son, opus)
			}
		}},
		{"S55 measured lane statistics: the model's own when there are enough, else pooled", func(t *testing.T) {
			testMeasureLanes(t)
		}},
		{"S56 the ratio is per account, the lane cost per model", func(t *testing.T) {
			s := dworld()
			s[dX], s[dY] = dsmp(88, 0, 8, 3), dsmp(88, 0, 8, 3)
			in := laneIn(s, 1)
			in.Accounts[2].Ratio = 10
			r := rank(t, in)
			if x, y := drainOf(t, r, dX), drainOf(t, r, dY); !near1(x.RoomPts, 9*7.2, 1e-9) || !near1(y.RoomPts, 90, 1e-9) {
				t.Errorf("room pts %.1f %.1f", x.RoomPts, y.RoomPts)
			}
		}},
		// J. Long vs short lanes.
		{"S57 --expect 3h at H 1: a spill lane, at most two running", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(78, 10, 1, 3)
			in := laneIn(s, 5)
			in.Expect = 3 * time.Hour
			r := rank(t, in)
			if countOf(r, dX) != 2 || !r.Picks[0].Spill {
				t.Errorf("picks %v spill %v", picksOf(r), r.Picks[0].Spill)
			}
		}},
		{"S58 short lanes: a near deadline is not a spill", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 10, 0.4, 3)
			in := laneIn(s, 1)
			if d := drainOf(t, rank(t, in), dX); !d.Spill {
				t.Errorf("15-minute lanes 14 minutes before finish_by should spill")
			}
			in.Stats = LaneStats{Len: 5 * time.Minute}
			if d := drainOf(t, rank(t, in), dX); d.Spill {
				t.Errorf("5-minute lanes spill")
			}
		}},
		{"S59 lanes switch to 2 hours: running lanes' age dominates", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(80, 10, 10, 3)
			in := laneIn(s, 1)
			in.Leases = []LaneLease{seenLease(dX, 90, 10), seenLease(dX, 90, 10)}
			if d := drainOf(t, rank(t, in), dX); !near1(d.ForwardBurn, 2*10*1.5, 0.2) {
				t.Errorf("forward burn %.1f", d.ForwardBurn)
			}
		}},
		// K. Odd events.
		{"S60 owner reset mid-week (w 0, H 50): drain", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(0, 0, 50, 3)
			if !drainOf(t, rank(t, laneIn(s, 1)), dX).On {
				t.Errorf("no drain after an owner reset")
			}
		}},
		{"S61 5-hour server reset: h 0 gives the full window", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 0, 8, 5)
			if d := drainOf(t, rank(t, laneIn(s, 1)), dX); d.Cap5h != 12 {
				t.Errorf("5h capacity %d", d.Cap5h)
			}
		}},
		{"S62 a ratio calibrated low: each reading corrects w, the margin holds", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(96.5, 0, 8, 3)
			in := laneIn(s, 1)
			in.Accounts[1].Ratio = 5
			d := drainOf(t, rank(t, in), dX)
			if d.Projected+float64(d.Capacity)*8/5 > 98 {
				t.Errorf("projected %.1f with %d lanes", d.Projected, d.Capacity)
			}
		}},
		{"S63 a ratio calibrated high: the landing guard", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(97, 0, 8, 3)
			in := laneIn(s, 1)
			in.Accounts[1].Ratio = 3
			r := rank(t, in)
			if r.Picks[0].Key == dX {
				t.Errorf("a lane landing over 99 was placed")
			}
		}},
		{"S64 the p75 spread is kept once", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 0, 10, 3)
			in := laneIn(s, 1)
			in.Stats = LaneStats{Cost: 4, Mean: 4}
			flat := drainOf(t, rank(t, in), dX).CapWeekly
			in.Stats = LaneStats{Cost: 12, Mean: 4}
			spread := drainOf(t, rank(t, in), dX).CapWeekly
			if flat-spread != 2 {
				t.Errorf("flat %d spread %d", flat, spread)
			}
		}},
		{"S65 a new seat (no calibration): fallbacks", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 0, 10, 3)
			in := laneIn(s, 1)
			in.Accounts[1].Ratio, in.Accounts[1].F5 = 0, 0
			if d := drainOf(t, rank(t, in), dX); !near1(d.RoomPts, 12*7.2, 1e-9) {
				t.Errorf("room %.1f", d.RoomPts)
			}
		}},
		{"S66 clocks minutes apart: no drain inside the 10-minute guard", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(85, 0, 0.15, 3)
			if drainOf(t, rank(t, laneIn(s, 1)), dX).On {
				t.Errorf("drain 9 minutes before the reset")
			}
		}},
		{"S67 the reset hour moves: finish_by follows the newest resets_at", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 0, 7.5, 3)
			r := rank(t, laneIn(s, 1))
			if !r.Picks[0].FinishBy.Equal(hrs(7.5).Add(-10 * time.Minute)) {
				t.Errorf("finish_by %v", r.Picks[0].FinishBy)
			}
		}},
		// L. Demand.
		{"S68 no demand: the projected waste is shown", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 0, 4, 3)
			d := drainOf(t, rank(t, laneIn(s, 1)), dX)
			if !near1(d.Projected, 88, 0.01) || d.Deficit < 1 {
				t.Errorf("drain %+v", d)
			}
		}},
		{"S69 a trickle of lanes: X while it has capacity", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(88, 0, 8, 3)
			var ls []LaneLease
			for i := 0; i < 5; i++ {
				in := laneIn(s, 1)
				in.Leases = ls
				p := rank(t, in).Picks[0]
				if p.Key != dX {
					t.Fatalf("lane %d went to %s", i, p.Key)
				}
				for j := range ls {
					ls[j].T0 = ls[j].T0.Add(-30 * time.Minute)
					ls[j].Ended = true
				}
				ls = append(ls, dlease(dX, 0))
			}
		}},
		{"S70 a big drain room does not take a whole batch past its capacity", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(60, 50, 20, 4)
			c := drainOf(t, rank(t, laneIn(s, 1)), dX).Capacity
			r := rank(t, laneIn(s, 20))
			if countOf(r, dX) != c || len(r.Picks) != 20 {
				t.Errorf("X %d of capacity %d; picks %v", countOf(r, dX), c, picksOf(r))
			}
		}},
		{"S71 X excluded (not an account here): the others", func(t *testing.T) {
			s := dworld()
			delete(s, dX)
			if p := rank(t, laneIn(s, 1)).Picks[0].Key; isProt(p) {
				t.Errorf("pick %s", p)
			}
		}},
		{"S72 the same input twice takes the last slot twice (accepted; the margin covers it)", func(t *testing.T) {
			s := dworld()
			s[dX] = dsmp(96, 10, 3, 3)
			a, b := rank(t, laneIn(s, 1)), rank(t, laneIn(s, 1))
			if a.Picks[0].Key != dX || b.Picks[0].Key != dX {
				t.Errorf("picks %v %v", picksOf(a), picksOf(b))
			}
		}},
	}
	if len(cases) != 72 {
		t.Fatalf("%d scenarios, want 72", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name[:3], c.run)
	}
}

// testMeasureLanes: lane statistics from the ledger (S55).
func testMeasureLanes(t *testing.T) {
	t.Helper()
	var led []leasesLease
	var local []LaneLease
	f5 := 1e-6
	for i := 0; i < 12; i++ {
		at := lnow.Add(-time.Duration(10+i) * time.Hour)
		model := "sonnet"
		cost := 2.0 + float64(i%4) // 2..5 points
		if i < 10 {
			model = "opus"
			cost = 10 + float64(i)
		}
		led = append(led, leasesLease{at: at, launched: at.Add(time.Minute), ended: at.Add(time.Duration(5+i) * time.Minute), model: model})
		local = append(local, LaneLease{Key: "k", T0: at, Seen: true, Ended: true, Burned: cost / f5})
	}
	st := measureLanes(toLedger(led), local, nil, map[string]float64{"k": f5}, "opus", lnow)
	if st.N != 10 || st.Model != "opus" || st.Cost < 15 || st.Cost > 17.5 || !near1(st.Mean, 14.5, 1e-6) {
		t.Errorf("opus stats %+v", st)
	}
	st = measureLanes(toLedger(led), local, nil, map[string]float64{"k": f5}, "", lnow)
	if st.N != 12 || st.Len < 9*time.Minute || st.Len > 11*time.Minute {
		t.Errorf("pooled stats %+v", st)
	}
	st = measureLanes(toLedger(led[:5]), local[:5], nil, map[string]float64{"k": f5}, "", lnow)
	if st.N != 0 || st.Cost != 0 || st.Len != 0 {
		t.Errorf("five lanes: %+v (fallbacks expected)", st)
	}
}

type leasesLease struct {
	at, launched, ended time.Time
	model               string
}

func toLedger(xs []leasesLease) []leases.Lease {
	var out []leases.Lease
	for _, x := range xs {
		out = append(out, leases.Lease{Line: leases.Line{V: 1, Op: leases.OpLease, At: x.at, Key: "k", Model: x.model},
			Launched: x.launched, Ended: x.ended})
	}
	return out
}

// Found by the week simulator: an aggressive account outside drain whose long running lanes will
// still burn most of its 5-hour window is skipped before a new lane runs it into the limit, and
// frees when its first running lane is expected to end.
func TestLanesForward5h(t *testing.T) {
	s := dworld()
	s[dX] = dsmp(30, 60, 140, 3) // 3 hours left in the window
	in := laneIn(s, 1)
	// two long lanes, 2 hours old, each burning 8 points an hour: about 2 × 8 × 2 = 32 more
	in.Leases = append(in.Leases, seenLease(dX, 120, 8), seenLease(dX, 120, 8))
	r := rank(t, in)
	row := rowOf(r, dX)
	if !row.Skip || row.SkipLevel != 3 || !strings.Contains(row.Reason, "running lanes will burn") || countOf(r, dX) != 0 {
		t.Fatalf("row %+v picks %v", row, picksOf(r))
	}
	if row.ETA == nil || !row.ETA.Equal(hrs(2)) {
		t.Errorf("free at %v, want the lanes' expected end in 2 hours", row.ETA)
	}
	// One lane burning 8 an hour leaves room: X is eligible again.
	in.Leases = in.Leases[:len(in.Leases)-1]
	if row := rowOf(rank(t, in), dX); row.Skip {
		t.Errorf("one lane: %+v", row)
	}
	// rr_exp has no such check.
	in.Leases = append(in.Leases, seenLease(dX, 120, 8))
	in.Params = ReferenceLaneParams
	if row := rowOf(rank(t, in), dX); row.Skip {
		t.Errorf("rr_exp skipped: %s", row.Reason)
	}
}
