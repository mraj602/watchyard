package calib

import (
	"math"
	"sort"
	"time"
)

// Fallbacks and rules (setup.md 8.5, Parameters and Calibration).
const (
	FallbackF         = 1.4 // 5-hour points per million weighted units
	FallbackRatio     = 7.2 // 5-hour points per weekly point
	FallbackLanePrice = 4.0 // 5-hour points of a lane's first 5 hours
	LaneMargin        = 2.0 // the lane charge is LaneMargin × the lane price

	PairWindow = 7 * 24 * time.Hour  // pairs for f and ρ
	LaneWindow = 14 * 24 * time.Hour // lanes for the lane price
	LaneSpan   = 5 * time.Hour       // a lane's cost is its first 5 hours

	minN    = 20  // usable measurements before blending
	minDays = 3.0 // days before blending

	minWindowUnits = 2e6 // units of a 5-hour window for the reference ratio, and of f's pairs
	minD7          = 5.0 // weekly points behind ρ

	missRun    = 6 // one-sided misses in a row that restart a calibration
	missMinE   = 1.0
	missPoints = 2.0
)

// Plausible ranges: a measured value outside is not used.
var (
	rangeF     = [2]float64{0.25, 8}
	rangeRatio = [2]float64{3, 15}
	rangePrice = [2]float64{0.5, 25}
)

// Restart reasons.
const (
	ReasonSeatChange = "seat_change"
	ReasonUnderRead  = "under_read"
	ReasonOverRead   = "over_read"
)

// Factor is one calibrated factor (api.yaml CalibrationFactor).
type Factor struct {
	Value       float64  `json:"value"`
	Fallback    float64  `json:"fallback"`
	Measured    *float64 `json:"measured"`
	Weight      float64  `json:"weight"`
	N           int      `json:"n"`
	Days        float64  `json:"days"`
	Implausible bool     `json:"implausible"`
}

// Reset is a restart of a seat's calibration.
type Reset struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// Factors is a seat's calibration (api.yaml Calibration). F is in points per million units.
type Factors struct {
	At                    time.Time  `json:"at"`
	Since                 *time.Time `json:"since"`
	Reset                 *Reset     `json:"reset"`
	F                     Factor     `json:"f"`
	Ratio                 Factor     `json:"ratio"`
	LanePrice             Factor     `json:"lane_price"`
	LaneCharge            float64    `json:"lane_charge"`
	LaneUnits             *float64   `json:"lane_units"`
	UnexplainedPctPerHour *float64   `json:"unexplained_pct_per_hour"`
	UnexplainedHours      float64    `json:"unexplained_hours"`
}

// Fallbacks are the factors of an account with no measurement.
func Fallbacks(now time.Time) Factors {
	return Factors{At: now.UTC(), F: Factor{Value: FallbackF, Fallback: FallbackF},
		Ratio: Factor{Value: FallbackRatio, Fallback: FallbackRatio}, LanePrice: Factor{Value: FallbackLanePrice, Fallback: FallbackLanePrice},
		LaneCharge: LaneMargin * FallbackLanePrice}
}

// CleanDay sums the clean pairs of one UTC day.
type CleanDay struct {
	Day   int64   `json:"day"` // the day's start, ms
	Rise  float64 `json:"rise"`
	Hours float64 `json:"hours"`
}

// Lane is the first-5h units of one launched lease.
type Lane struct {
	At    int64   `json:"at"`
	Units float64 `json:"units"`
}

// Run is the current run of one-sided misses.
type Run struct {
	Side  int   `json:"side,omitempty"` // +1 under-reads, -1 over-reads
	N     int   `json:"n,omitempty"`
	Start int64 `json:"start,omitempty"`
}

// ResetMS is Reset in milliseconds (state).
type ResetMS struct {
	At     int64  `json:"at"`
	Reason string `json:"reason"`
}

// State is a seat's calibration state, kept by the hub between runs (seat_calibration.state).
type State struct {
	V       int        `json:"v"`
	Tracker Tracker    `json:"tracker"`
	Pairs   []Pair     `json:"pairs"` // since the restart, of the last 7 days, that rose or used tokens
	Clean   []CleanDay `json:"clean"`
	Lanes   []Lane     `json:"lanes"`
	Run     Run        `json:"run"`
	Reset   *ResetMS   `json:"reset,omitempty"`
	// FUse is the meter factor in use (points per million units) and FUseMeasured whether the
	// measured value had a weight in it: misses count only then.
	FUse         float64 `json:"f_use"`
	FUseMeasured bool    `json:"f_use_measured,omitempty"`
}

// NewState is an empty state.
func NewState() *State { return &State{V: 1, FUse: FallbackF} }

// AddReadings folds readings (oldest first, newer than any folded before) with the seat's
// meter, which must hold its token uses since the last used five_hour reading.
func (s *State) AddReadings(rs []Reading, m *Meter) {
	for _, r := range rs {
		if st := s.Tracker.Add(r, m); st.Five != nil {
			s.AddPair(*st.Five)
		}
	}
}

// AddPair folds one pair: the miss run (which may restart the calibration), the clean sums
// and the pair list.
func (s *State) AddPair(p Pair) {
	if !p.Clean && s.FUseMeasured {
		if e := s.FUse * p.U / 1e6; e >= missMinE {
			d := p.D5 - e
			if math.Abs(d) >= math.Max(missPoints, e/2) {
				side := 1
				if d < 0 {
					side = -1
				}
				if s.Run.Side == side {
					s.Run.N++
				} else {
					s.Run = Run{Side: side, N: 1, Start: p.T0}
				}
				if s.Run.N >= missRun {
					reason := ReasonUnderRead
					if side < 0 {
						reason = ReasonOverRead
					}
					s.restart(s.Run.Start, reason)
				}
			} else {
				s.Run = Run{}
			}
		}
	}
	if p.Clean {
		day := p.T1 - p.T1%86_400_000
		if n := len(s.Clean); n > 0 && s.Clean[n-1].Day == day {
			s.Clean[n-1].Rise += p.D5
			s.Clean[n-1].Hours += p.Hours()
		} else {
			s.Clean = append(s.Clean, CleanDay{Day: day, Rise: p.D5, Hours: p.Hours()})
		}
	}
	if p.U > 0 || p.D5 != 0 || (p.D7 != nil && *p.D7 != 0) {
		s.Pairs = append(s.Pairs, p)
	}
}

// Restart drops the measurements before at (ms): the seat changed, or the meter missed on one
// side too often. The account is back on its fallbacks until it has enough new ones.
func (s *State) Restart(at int64, reason string) { s.restart(at, reason) }

func (s *State) restart(at int64, reason string) {
	keep := s.Pairs[:0]
	for _, p := range s.Pairs {
		if p.T0 >= at {
			keep = append(keep, p)
		}
	}
	s.Pairs = keep
	day := at - at%86_400_000
	clean := s.Clean[:0]
	for _, c := range s.Clean {
		if c.Day >= day {
			clean = append(clean, c)
		}
	}
	s.Clean = clean
	s.Reset = &ResetMS{At: at, Reason: reason}
	s.Run = Run{}
	s.FUse, s.FUseMeasured = FallbackF, false
}

// AddLane adds one launched lease's first-5h units.
func (s *State) AddLane(at time.Time, units float64) {
	s.Lanes = append(s.Lanes, Lane{At: at.UnixMilli(), Units: math.Max(0, units)})
}

// Prune drops pairs and clean days older than 7 days and lanes older than 14 days.
func (s *State) Prune(now time.Time) {
	cut := now.Add(-PairWindow).UnixMilli()
	keep := s.Pairs[:0]
	for _, p := range s.Pairs {
		if p.T1 >= cut {
			keep = append(keep, p)
		}
	}
	s.Pairs = keep
	day := cut - cut%86_400_000
	clean := s.Clean[:0]
	for _, c := range s.Clean {
		if c.Day >= day {
			clean = append(clean, c)
		}
	}
	s.Clean = clean
	lcut := now.Add(-LaneWindow).UnixMilli()
	lanes := s.Lanes[:0]
	for _, l := range s.Lanes {
		if l.At >= lcut {
			lanes = append(lanes, l)
		}
	}
	s.Lanes = lanes
}

// Pool is the lane cost pooled over seats: the mean first-5h units (nil: no lane), the
// number of lanes and the first lane's time.
type Pool struct {
	Units *float64
	N     int
	First time.Time
}

// PoolLanes pools the lanes of the last 14 days: the mean leaving out the largest ⌊n/100⌋.
func PoolLanes(now time.Time, lanes ...[]Lane) Pool {
	cut := now.Add(-LaneWindow).UnixMilli()
	var us []float64
	var first int64
	for _, ls := range lanes {
		for _, l := range ls {
			if l.At < cut || l.At > now.UnixMilli() {
				continue
			}
			us = append(us, l.Units)
			if first == 0 || l.At < first {
				first = l.At
			}
		}
	}
	p := Pool{N: len(us)}
	if len(us) == 0 {
		return p
	}
	sort.Float64s(us)
	us = us[:len(us)-len(us)/100]
	t := 0.0
	for _, u := range us {
		t += u
	}
	mean := t / float64(len(us))
	p.Units, p.First = &mean, time.UnixMilli(first)
	return p
}

// Factors computes the seat's factors at now with the pooled lane cost, and sets the meter
// factor in use for the next misses.
func (s *State) Factors(now time.Time, pool Pool) Factors {
	f := Fallbacks(now)
	cut := now.Add(-PairWindow).UnixMilli()
	var pairs []Pair
	for _, p := range s.Pairs {
		if p.T1 >= cut && p.T1 <= now.UnixMilli() {
			pairs = append(pairs, p)
		}
	}
	var since int64
	first := func(t int64) {
		if t > 0 && (since == 0 || t < since) {
			since = t
		}
	}
	days := func(t int64) float64 {
		if t == 0 {
			return 0
		}
		return math.Max(0, float64(now.UnixMilli()-t)/86_400_000)
	}

	// The meter factor: the reference from window ratios, then the pairs without a large
	// unexplained rise.
	type win struct{ d5, u float64 }
	wins := map[int64]*win{}
	for _, p := range pairs {
		if p.U > 0 {
			w := wins[p.Window]
			if w == nil {
				w = &win{}
				wins[p.Window] = w
			}
			w.d5 += p.D5
			w.u += p.U
		}
	}
	var ratios [][2]float64
	for _, w := range wins {
		if w.u >= minWindowUnits {
			ratios = append(ratios, [2]float64{w.d5 / w.u, w.u})
		}
	}
	ref := weightedMedian(ratios)
	var sd5, su float64
	var nF int
	var firstF int64
	for _, p := range pairs {
		if p.U <= 0 {
			continue
		}
		if ref > 0 {
			if e := ref * p.U; p.D5-e > math.Max(3, 2*e) {
				continue // claude.ai use beside the session
			}
		}
		sd5 += p.D5
		su += p.U
		nF++
		if firstF == 0 || p.T0 < firstF {
			firstF = p.T0
		}
	}
	var mF *float64
	if su >= minWindowUnits {
		v := sd5 / su * 1e6
		mF = &v
	}
	f.F = blend(FallbackF, mF, nF, days(firstF), rangeF)
	if nF > 0 {
		first(firstF)
	}

	// The ratio.
	var r5, r7 float64
	var nR int
	var firstR int64
	for _, p := range pairs {
		if p.D7 == nil {
			continue
		}
		r5 += p.D5
		r7 += *p.D7
		if p.D5 != 0 || *p.D7 != 0 {
			nR++
			if firstR == 0 || p.T0 < firstR {
				firstR = p.T0
			}
		}
	}
	var mR *float64
	if r7 >= minD7 {
		v := r5 / r7
		mR = &v
	}
	f.Ratio = blend(FallbackRatio, mR, nR, days(firstR), rangeRatio)
	if nR > 0 {
		first(firstR)
	}

	// The lane price, from the pooled units and this seat's meter factor.
	var mP *float64
	if pool.Units != nil {
		v := *pool.Units * f.F.Value / 1e6
		mP = &v
		u := round(*pool.Units, 0)
		f.LaneUnits = &u
	}
	var firstL int64
	if !pool.First.IsZero() {
		firstL = pool.First.UnixMilli()
	}
	f.LanePrice = blend(FallbackLanePrice, mP, pool.N, days(firstL), rangePrice)
	f.LaneCharge = round(LaneMargin*f.LanePrice.Value, 4)

	// The unexplained rate from the clean pairs.
	var rise, hours float64
	day := cut - cut%86_400_000
	for _, c := range s.Clean {
		if c.Day >= day {
			rise += c.Rise
			hours += c.Hours
		}
	}
	if hours > 0 {
		v := round(math.Max(0, rise/hours), 3)
		f.UnexplainedPctPerHour = &v
	}
	f.UnexplainedHours = round(hours, 2)

	if since > 0 {
		t := time.UnixMilli(since).UTC()
		f.Since = &t
	}
	if s.Reset != nil {
		f.Reset = &Reset{At: time.UnixMilli(s.Reset.At).UTC(), Reason: s.Reset.Reason}
	}
	s.FUse, s.FUseMeasured = f.F.Value, f.F.Weight > 0
	return f
}

// blend moves from the fallback to the measured value once there are minN measurements over
// minDays: halfway then, fully at twice both.
func blend(fb float64, measured *float64, n int, days float64, rng [2]float64) Factor {
	f := Factor{Value: fb, Fallback: fb, N: n, Days: round(days, 2)}
	if measured == nil || math.IsNaN(*measured) || math.IsInf(*measured, 0) {
		return f
	}
	m := round(*measured, 4)
	f.Measured = &m
	if m < rng[0] || m > rng[1] {
		f.Implausible = true
		return f
	}
	if n < minN || days < minDays {
		return f
	}
	w := math.Min(1, math.Min(float64(n)/minN, days/minDays)/2)
	f.Weight = round(w, 3)
	f.Value = round(fb+w*(m-fb), 4)
	return f
}

// weightedMedian of (value, weight) pairs: the value where the cumulative weight reaches half
// (0 when there is none).
func weightedMedian(xs [][2]float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([][2]float64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i][0] < s[j][0] })
	tot := 0.0
	for _, x := range s {
		tot += x[1]
	}
	c := 0.0
	for _, x := range s {
		c += x[1]
		if c >= tot/2 {
			return x[0]
		}
	}
	return s[len(s)-1][0]
}

func round(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}
