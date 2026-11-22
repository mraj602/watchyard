package calib

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// series is a synthetic seat: token use at rate units per hour from 08:00 to 20:00 UTC each
// day, true 5-hour points of fTrue per million units, true weekly points of 5-hour / ratio,
// and a status-line reading every 5 minutes while it is used (whole percent, as Claude Code
// reports them). Windows start at the first token after the previous one ended.
type series struct {
	start  time.Time
	days   float64
	fTrue  float64
	ratio  float64
	rate   float64
	source string
	extra  func(t time.Time) float64 // 5-hour points of use the meter cannot see, per 5 minutes
}

func (s series) make() ([]Reading, []Use) {
	var rs []Reading
	var us []Use
	var h, w float64
	var end time.Time
	wreset := s.start.Add(8 * 24 * time.Hour)
	step := 5 * time.Minute
	src := s.source
	if src == "" {
		src = "statusline"
	}
	for t := s.start; t.Before(s.start.Add(time.Duration(s.days * 24 * float64(time.Hour)))); t = t.Add(step) {
		hr := t.Hour()
		active := hr >= 8 && hr < 20
		units := 0.0
		if active {
			units = s.rate / 12
		}
		if !end.IsZero() && !t.Before(end) {
			h, end = 0, time.Time{}
		}
		add := 0.0
		if s.extra != nil {
			add = s.extra(t)
		}
		if units > 0 || add > 0 {
			if end.IsZero() {
				end = t.Add(5 * time.Hour)
			}
			at := t.Add(time.Minute)
			if units > 0 {
				us = append(us, Use{At: at, Units: units})
			}
			d := s.fTrue*units/1e6 + add
			h += d
			w += d / s.ratio
		}
		if active && !end.IsZero() {
			rs = append(rs, Reading{At: t.Add(2 * time.Minute), Source: src, Five: pct(math.Round(math.Min(h, 100))), FiveReset: end,
				Seven: pct(math.Round(math.Min(w, 100))), SevenReset: wreset})
		}
	}
	return rs, us
}

func fold(rs []Reading, us []Use, now time.Time) (*State, Factors) {
	s := NewState()
	s.AddReadings(rs, NewMeter(us))
	s.Prune(now)
	return s, s.Factors(now, Pool{})
}

func within(got *float64, want, rel float64) bool {
	return got != nil && math.Abs(*got-want) <= rel*want
}

func TestFitMeasuresAndBlends(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s := series{start: start, days: 7, fTrue: 2.0, ratio: 6.0, rate: 2.5e6}
	rs, us := s.make()

	// Two days: measured, but still on the fallbacks.
	_, f := fold(cut(rs, start.Add(48*time.Hour)), us, start.Add(48*time.Hour))
	if !within(f.F.Measured, 2.0, 0.03) || !within(f.Ratio.Measured, 6.0, 0.06) {
		t.Fatalf("measured f %v ratio %v, want about 2.0 and 6.0", deref(f.F.Measured), deref(f.Ratio.Measured))
	}
	if f.F.Weight != 0 || f.F.Value != FallbackF || f.Ratio.Value != FallbackRatio || f.F.N < minN {
		t.Fatalf("under 3 days: %+v %+v", f.F, f.Ratio)
	}

	// Four days (3.5 from the first pair): halfway and more.
	now := start.Add(4 * 24 * time.Hour)
	_, f = fold(cut(rs, now), us, now)
	if f.F.Weight < 0.5 || f.F.Weight >= 1 {
		t.Fatalf("weight after 4 days = %v", f.F.Weight)
	}
	want := FallbackF + f.F.Weight*(*f.F.Measured-FallbackF)
	if math.Abs(f.F.Value-want) > 1e-3 {
		t.Fatalf("value %v, want %v", f.F.Value, want)
	}

	// Seven days: fully measured.
	now = start.Add(7 * 24 * time.Hour)
	st, f := fold(rs, us, now)
	if f.F.Weight != 1 || !within(&f.F.Value, 2.0, 0.03) || f.Ratio.Weight != 1 || !within(&f.Ratio.Value, 6.0, 0.06) {
		t.Fatalf("after 7 days: f %+v ratio %+v", f.F, f.Ratio)
	}
	if !st.FUseMeasured || st.FUse != f.F.Value {
		t.Fatalf("f in use not set: %v %v", st.FUse, st.FUseMeasured)
	}
	if f.Since == nil || f.Reset != nil || f.LaneUnits != nil || f.LanePrice.Value != FallbackLanePrice || f.LaneCharge != 8 {
		t.Fatalf("since/reset/lane: %+v", f)
	}

	// The factors encode as api.yaml Calibration.
	b, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"at", "since", "reset", "f", "ratio", "lane_price", "lane_charge", "lane_units", "unexplained_pct_per_hour", "unexplained_hours"} {
		if _, ok := m[k]; !ok {
			t.Errorf("factors JSON has no %q: %s", k, b)
		}
	}
}

func cut(rs []Reading, now time.Time) []Reading {
	var out []Reading
	for _, r := range rs {
		if !r.At.After(now) {
			out = append(out, r)
		}
	}
	return out
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Pairs with a large unexplained rise (claude.ai use beside a session) are left out of f.
func TestFitSkipsLargeUnexplainedRises(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	burst := func(t time.Time) float64 {
		if t.Hour() == 10 && t.Minute() < 20 {
			return 4 // 16 points in 20 minutes that the meter cannot see, every day
		}
		return 0
	}
	s := series{start: start, days: 7, fTrue: 1.6, ratio: 7, rate: 3e6}
	rs, us := s.make()
	_, plain := fold(rs, us, start.Add(7*24*time.Hour))
	s.extra = burst
	rs, us = s.make()
	_, f := fold(rs, us, start.Add(7*24*time.Hour))
	// Whole-percent readings put the first reading of each synthetic window a little low, so
	// both are a few percent over 1.6; the bursts must not move f further.
	if !within(f.F.Measured, *plain.F.Measured, 0.015) || !within(plain.F.Measured, 1.6, 0.04) {
		t.Fatalf("f with claude.ai bursts = %v, without %v, want about 1.6", deref(f.F.Measured), deref(plain.F.Measured))
	}
	if f.F.N >= plain.F.N {
		t.Fatalf("no burst pair left out: %d >= %d", f.F.N, plain.F.N)
	}
	// The ratio counts every rise in both windows.
	if !within(f.Ratio.Measured, 7, 0.06) {
		t.Fatalf("ratio = %v, want about 7", deref(f.Ratio.Measured))
	}
}

func TestFitImplausibleAndEmpty(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s := series{start: start, days: 7, fTrue: 12, ratio: 7, rate: 1e6}
	rs, us := s.make()
	_, f := fold(rs, us, start.Add(7*24*time.Hour))
	if !f.F.Implausible || f.F.Weight != 0 || f.F.Value != FallbackF {
		t.Fatalf("f of 12 points per million units: %+v", f.F)
	}
	_, f = fold(nil, nil, start)
	if f.F.Measured != nil || f.Ratio.Measured != nil || f.Since != nil || f.UnexplainedPctPerHour != nil || f.F.N != 0 {
		t.Fatalf("no data: %+v", f)
	}
	if f.F.Value != FallbackF || f.Ratio.Value != FallbackRatio || f.LanePrice.Value != FallbackLanePrice {
		t.Fatalf("no data, no fallbacks: %+v", f)
	}
}

func TestUnexplainedFromCleanPairs(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	// claude.ai only: a web reading every 5 minutes, 1 point per 10 minutes, no tokens.
	extra := func(t time.Time) float64 { return 0.5 }
	s := series{start: start, days: 2, fTrue: 1.4, ratio: 7, rate: 0, source: SourceWeb, extra: extra}
	rs, us := s.make()
	// make() writes readings only while "active"; claude.ai readings come all day here.
	var all []Reading
	for _, r := range rs {
		all = append(all, r)
	}
	_, f := fold(all, us, start.Add(48*time.Hour))
	if f.UnexplainedPctPerHour == nil || math.Abs(*f.UnexplainedPctPerHour-6) > 0.6 || f.UnexplainedHours < 10 {
		t.Fatalf("unexplained = %v over %v h, want about 6 points per hour", deref(f.UnexplainedPctPerHour), f.UnexplainedHours)
	}
	if f.F.N != 0 {
		t.Fatalf("clean pairs counted for f: %d", f.F.N)
	}
}

func TestRestartAfterOneSidedMisses(t *testing.T) {
	s := NewState()
	s.FUse, s.FUseMeasured = 1.4, true
	base := t0.UnixMilli()
	pair := func(i int, d5, u float64) Pair {
		return Pair{T0: base + int64(i)*600_000, T1: base + int64(i+1)*600_000, D5: d5, U: u, Window: base + 4*3_600_000}
	}
	s.AddPair(pair(0, 3, 2e6)) // e 2.8: a hit
	s.AddPair(pair(1, 1, 0.1e6))
	for i := 2; i < 7; i++ {
		s.AddPair(pair(i, 6, 2e6)) // e 2.8, d5 6: under-read
		if s.Reset != nil {
			t.Fatalf("restart after %d misses", i-1)
		}
	}
	s.AddPair(pair(7, 0.1, 0.2e6)) // e < 1: neither counts nor ends the run
	s.AddPair(pair(8, 6, 2e6))
	if s.Reset == nil || s.Reset.Reason != ReasonUnderRead || s.Reset.At != pair(2, 0, 0).T0 {
		t.Fatalf("no under_read restart at the run's start: %+v", s.Reset)
	}
	if s.FUseMeasured || s.FUse != FallbackF {
		t.Fatalf("still on the measured f after a restart: %v %v", s.FUse, s.FUseMeasured)
	}
	for _, p := range s.Pairs {
		if p.T0 < s.Reset.At {
			t.Fatalf("pair before the restart kept: %+v", p)
		}
	}
	if len(s.Pairs) != 7 {
		t.Fatalf("pairs kept = %d, want the run's 7", len(s.Pairs))
	}
	// On the fallback, misses do not count.
	for i := 9; i < 20; i++ {
		s.AddPair(pair(i, 0, 2e6))
	}
	if s.Reset.Reason != ReasonUnderRead {
		t.Fatalf("restart while on the fallback: %+v", s.Reset)
	}

	// Over-reads, broken once by a hit.
	o := NewState()
	o.FUse, o.FUseMeasured = 2, true
	for i := 0; i < 5; i++ {
		o.AddPair(pair(i, 0, 2e6)) // e 4, d5 0: over-read
	}
	o.AddPair(pair(5, 4, 2e6)) // a hit ends the run
	for i := 6; i < 11; i++ {
		o.AddPair(pair(i, 1, 2e6))
	}
	if o.Reset != nil {
		t.Fatalf("restart although a hit broke the run: %+v", o.Reset)
	}
	o.AddPair(pair(11, 0, 2e6))
	if o.Reset == nil || o.Reset.Reason != ReasonOverRead {
		t.Fatalf("no over_read restart: %+v", o.Reset)
	}
	f := o.Factors(t0.Add(3*time.Hour), Pool{})
	if f.Reset == nil || f.Reset.Reason != ReasonOverRead {
		t.Fatalf("factors without the restart: %+v", f.Reset)
	}
}

func TestSeatChangeRestart(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rs, us := series{start: start, days: 7, fTrue: 2, ratio: 6, rate: 5e6}.make()
	now := start.Add(7 * 24 * time.Hour)
	s, f := fold(rs, us, now)
	if f.F.Weight != 1 {
		t.Fatalf("weight %v", f.F.Weight)
	}
	s.Restart(now.UnixMilli(), ReasonSeatChange)
	f = s.Factors(now, Pool{})
	if f.F.N != 0 || f.F.Value != FallbackF || f.Ratio.Value != FallbackRatio || f.Reset == nil || f.Reset.Reason != ReasonSeatChange {
		t.Fatalf("after a seat change: %+v", f)
	}
}

func TestPoolLanes(t *testing.T) {
	now := t0
	var a, b []Lane
	for i := 0; i < 150; i++ {
		a = append(a, Lane{At: now.Add(-time.Duration(i) * time.Hour).UnixMilli(), Units: 1e6})
	}
	a = append(a, Lane{At: now.Add(-time.Hour).UnixMilli(), Units: 500e6}) // one runaway lane
	b = append(b, Lane{At: now.Add(-15 * 24 * time.Hour).UnixMilli(), Units: 9e6}, Lane{At: now.Add(-2 * time.Hour).UnixMilli(), Units: 1e6})
	p := PoolLanes(now, a, b)
	if p.N != 152 || p.Units == nil || *p.Units != 1e6 {
		t.Fatalf("pool = %+v %v (the largest 1%% left out, older than 14 days dropped)", p, deref(p.Units))
	}
	s := NewState()
	f := s.Factors(now, p)
	if f.LaneUnits == nil || *f.LaneUnits != 1e6 || f.LanePrice.Measured == nil || *f.LanePrice.Measured != 1.4 {
		t.Fatalf("lane price = %+v", f.LanePrice)
	}
	if f.LanePrice.Weight != 1 || f.LanePrice.Value != 1.4 || f.LaneCharge != 2.8 {
		t.Fatalf("lane price blended: %+v charge %v", f.LanePrice, f.LaneCharge)
	}
	if q := PoolLanes(now); q.N != 0 || q.Units != nil {
		t.Fatalf("empty pool: %+v", q)
	}
}

func TestPrune(t *testing.T) {
	s := NewState()
	now := t0
	old := now.Add(-8 * 24 * time.Hour).UnixMilli()
	s.Pairs = []Pair{{T0: old, T1: old + 1, D5: 1, U: 1}, {T0: now.UnixMilli() - 10, T1: now.UnixMilli(), D5: 1, U: 1}}
	s.Clean = []CleanDay{{Day: old - old%86_400_000, Rise: 1, Hours: 1}, {Day: now.UnixMilli() - now.UnixMilli()%86_400_000, Rise: 2, Hours: 1}}
	s.Lanes = []Lane{{At: now.Add(-15 * 24 * time.Hour).UnixMilli()}, {At: old, Units: 1}}
	s.Prune(now)
	if len(s.Pairs) != 1 || len(s.Clean) != 1 || len(s.Lanes) != 1 {
		t.Fatalf("after prune: %d pairs, %d clean days, %d lanes", len(s.Pairs), len(s.Clean), len(s.Lanes))
	}
}

// The state survives a JSON round trip mid-series (the hub stores it between runs), and
// folding in two parts gives the same factors as folding at once.
func TestStateRoundTripIncremental(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rs, us := series{start: start, days: 5, fTrue: 1.8, ratio: 7.5, rate: 4e6}.make()
	now := start.Add(5 * 24 * time.Hour)
	_, whole := fold(rs, us, now)

	mid := start.Add(60 * time.Hour)
	s := NewState()
	s.AddReadings(cut(rs, mid), NewMeter(us))
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var s2 State
	if err := json.Unmarshal(b, &s2); err != nil {
		t.Fatal(err)
	}
	var rest []Reading
	for _, r := range rs {
		if r.At.After(mid) {
			rest = append(rest, r)
		}
	}
	s2.AddReadings(rest, NewMeter(us))
	s2.Prune(now)
	parts := s2.Factors(now, Pool{})
	if *parts.F.Measured != *whole.F.Measured || *parts.Ratio.Measured != *whole.Ratio.Measured || parts.F.N != whole.F.N {
		t.Fatalf("incremental %+v != whole %+v", parts.F, whole.F)
	}
}
