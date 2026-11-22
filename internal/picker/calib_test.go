package picker

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/calib"
)

// An account's calibrated factors replace the meter factor, the ratio and the lane charge;
// the margins add k × the meter's points (setup.md 8.5, effective state 2 and 3).
func TestLaneAccountFactors(t *testing.T) {
	acc := LaneAccount{Key: "a", Sample: smp(10, 10, 1, 72), Meter: LaneMeter{M5: 1e6, M7: 1e6, KeepH: true},
		F5: 2e-6, Ratio: 5, Charge: 12, K5: 0.5, K7: 1}
	in := LaneInput{Now: lnow, Params: DefaultLaneParams, Accounts: []LaneAccount{acc}, N: 1}
	res, err := RankLanes(in)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Table[0]
	// h = 10 + 2 (meter) + 1 (margin); w = 10 + 2/5 + 0.4 (margin).
	if math.Abs(r.FiveHour-13) > 1e-9 || math.Abs(r.Weekly-10.8) > 1e-9 || r.Margin5 != 1 || math.Abs(r.Margin7-0.4) > 1e-9 {
		t.Fatalf("row %+v", r)
	}
	if len(res.Picks) != 1 || res.Picks[0].C5 != 12 || res.Picks[0].CF != 12 {
		t.Fatalf("pick %+v, want the account's charge 12", res.Picks)
	}
	// A lease without its own charge is charged the account's current one.
	in.Leases = []LaneLease{{Key: "a", T0: hrs(-1), Launched: true}}
	res, _ = RankLanes(in)
	if r := res.Table[0]; math.Abs(r.FiveHour-25) > 1e-9 || math.Abs(r.Weekly-13.2) > 1e-9 {
		t.Fatalf("with a lease: 5h %v 7d %v, want 25 and 13.2", r.FiveHour, r.Weekly)
	}
	// Zero factors keep the parameters.
	plain := LaneAccount{Key: "a", Sample: smp(10, 10, 1, 72), Meter: LaneMeter{M5: 1e6, M7: 1e6, KeepH: true}}
	res, _ = RankLanes(LaneInput{Now: lnow, Params: DefaultLaneParams, Accounts: []LaneAccount{plain}, N: 1})
	if r := res.Table[0]; math.Abs(r.FiveHour-11.4) > 1e-9 || res.Picks[0].C5 != 8 {
		t.Fatalf("parameters: 5h %v c5 %v", r.FiveHour, res.Picks[0].C5)
	}
}

func hubFactors(at time.Time, f float64) *calib.Factors {
	x := calib.Fallbacks(at)
	x.F.Value, x.F.Measured, x.F.Weight, x.F.N, x.F.Days = f, &f, 1, 50, 6
	return &x
}

// The hub's factors while under 7 days old, else the kept copy, else a local fit.
func TestCalibrationSources(t *testing.T) {
	a := cand("a", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 10, 72*time.Hour, time.Minute))
	ob := Outbox{Tokens: []TokenUse{{At: t0.Add(-30 * time.Second), Session: "S", Slot: "~/.a", Units: 1e6}}}
	run := func(c Candidate, kept map[string]calib.Factors) *LaneView {
		t.Helper()
		res, err := Decide(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{c}, Outbox: ob, N: 1, Calibration: kept})
		if err != nil || res.Ranked[0].Lanes == nil || res.Ranked[0].Lanes.Calibration == nil {
			t.Fatalf("%v %+v", err, res.Ranked)
		}
		return res.Ranked[0].Lanes
	}
	hub := a
	hub.Calib = hubFactors(t0.Add(-time.Hour), 3)
	v := run(hub, nil)
	if v.Calibration.Source != CalibHub || v.Calibration.F.Value != 3 || math.Abs(v.FiveHourPct-13) > 0.051 {
		t.Fatalf("hub: %+v %+v", v, v.Calibration)
	}
	old := a
	old.Calib = hubFactors(t0.Add(-8*24*time.Hour), 3)
	kept := map[string]calib.Factors{a.Key(): *hubFactors(t0.Add(-2*24*time.Hour), 2.5)}
	if v := run(old, kept); v.Calibration.Source != CalibCached || v.Calibration.F.Value != 2.5 || math.Abs(v.FiveHourPct-12.5) > 0.051 {
		t.Fatalf("cached: %+v", v.Calibration)
	}
	v = run(old, nil)
	if c := v.Calibration; c.Source != CalibLocal || c.F.Value != calib.FallbackF || c.F.Weight != 0 || c.LaneCharge != 8 ||
		c.ResetReason != nil || c.UnexplainedFrom != nil || math.Abs(v.FiveHourPct-11.4) > 0.051 {
		t.Fatalf("local: %+v", c)
	}
	// A damaged copy (a zero factor) is not used.
	bad := *hubFactors(t0.Add(-time.Hour), 2)
	bad.Ratio.Value = 0
	if v := run(a, map[string]calib.Factors{a.Key(): bad}); v.Calibration.Source != CalibLocal {
		t.Fatalf("damaged copy used: %+v", v.Calibration)
	}
}

func TestSaveLoadCalibration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "next", "calibration.json")
	a, b := cand("a"), cand("b")
	if err := SaveCalibration(path, nil, []Candidate{a, b}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("written with no hub factors")
	}
	a.Calib = hubFactors(t0.Add(-time.Hour), 2)
	old := map[string]calib.Factors{
		b.Key():       *hubFactors(t0.Add(-24*time.Hour), 1.8),   // kept: still fresh
		"acct:gone/o": *hubFactors(t0.Add(-8*24*time.Hour), 1.1), // dropped: over 7 days
	}
	if err := SaveCalibration(path, old, []Candidate{a, b}, t0); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, st)
	}
	got := LoadCalibration(path)
	if len(got) != 2 || got[a.Key()].F.Value != 2 || got[b.Key()].F.Value != 1.8 {
		t.Fatalf("loaded %+v", got)
	}
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if LoadCalibration(path) != nil || LoadCalibration(filepath.Join(dir, "missing")) != nil {
		t.Fatal("an unreadable file is not empty")
	}
}

// Without hub factors the account is calibrated from this machine's outbox: 4 days of
// readings and tokens (2 points per million units, 8 five-hour points per weekly point).
func TestLocalFitFromOutbox(t *testing.T) {
	var five, seven []Sample
	var toks []TokenUse
	h5, w := 0.0, 0.0
	var end time.Time
	for at := t0.Add(-4 * 24 * time.Hour); at.Before(t0.Add(-time.Hour)); at = at.Add(10 * time.Minute) {
		if at.Hour() < 8 || at.Hour() >= 20 {
			continue
		}
		if !end.IsZero() && !at.Before(end) {
			h5, end = 0, time.Time{}
		}
		if end.IsZero() {
			end = at.Add(5 * time.Hour)
		}
		toks = append(toks, TokenUse{At: at, Session: "S", Slot: "~/.a", Units: 1e6})
		h5 += 2
		w += 0.25
		r := at.Add(time.Minute)
		five = append(five, Sample{At: r, Pct: h5, ResetsAt: end, Source: "local"})
		seven = append(seven, Sample{At: r, Pct: math.Round(w), ResetsAt: t0.Add(3 * 24 * time.Hour), Source: "local"})
	}
	last5, last7 := five[len(five)-1], seven[len(seven)-1]
	a := cand("a",
		Window{Key: "five_hour", UsedPct: last5.Pct, ResetsAt: last5.ResetsAt, UpdatedAt: last5.At, Source: "local", Samples: five},
		Window{Key: "seven_day", UsedPct: last7.Pct, ResetsAt: last7.ResetsAt, UpdatedAt: last7.At, Source: "local", Samples: seven})
	a.Source = "local"
	res, err := Decide(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{a}, Outbox: Outbox{Tokens: toks}, N: 1})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Ranked[0].Lanes.Calibration
	if c.Source != CalibLocal || c.F.Measured == nil || math.Abs(*c.F.Measured-2) > 0.05 || c.F.Weight <= 0 || c.F.N < 20 {
		t.Fatalf("f %+v", c.F)
	}
	if c.Ratio.Measured == nil || math.Abs(*c.Ratio.Measured-8) > 0.6 {
		t.Fatalf("ratio %+v", c.Ratio)
	}
	if want := calib.FallbackF + c.F.Weight*(*c.F.Measured-calib.FallbackF); math.Abs(c.F.Value-want) > 1e-3 {
		t.Fatalf("blend %v, want %v", c.F.Value, want)
	}
	// The blended f under-reads the true 2 by 0.2 points a pair, which the 5-hour margin
	// factor covers: 0.2 / max(1.8, 2) = 0.1. Nothing was metered since the reading: no margin.
	if math.Abs(c.Corrector5h-0.1) > 0.005 || c.Margin5hPct != 0 {
		t.Fatalf("margins %+v", c)
	}
	// The hub's history is short (its last day only) and has no factors: the local fit still
	// sees the outbox's 4 days.
	var ls []LocalSample
	for i := range five {
		f5, f7 := five[i].Pct, seven[i].Pct
		r5, r7 := five[i].ResetsAt, seven[i].ResetsAt
		ls = append(ls, LocalSample{At: five[i].At, Slot: "~/.a", Account: a.Account, Org: a.Org, Five: &f5, Seven: &f7, FiveReset: &r5, SevenReset: &r7})
	}
	short := a
	short.Windows = nil
	for _, w := range a.Windows {
		var keep []Sample
		for _, x := range w.Samples {
			if t0.Sub(x.At) < 26*time.Hour {
				keep = append(keep, Sample{At: x.At, Pct: x.Pct, ResetsAt: x.ResetsAt, Source: "statusline"})
			}
		}
		w.Samples, w.Source = keep, "hub"
		short.Windows = append(short.Windows, w)
	}
	r2, _ := Decide(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{short}, Outbox: Outbox{Tokens: toks, Samples: ls}, N: 1})
	if c2 := r2.Ranked[0].Lanes.Calibration; c2.Source != CalibLocal || c2.F.N < c.F.N || c2.F.Weight != c.F.Weight {
		t.Fatalf("short hub history: %+v, want the outbox's %d pairs", c2.F, c.F.N)
	}
	var out bytes.Buffer
	if err := Explain(&out, res, "local", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Calibration a (local): f ") {
		t.Fatalf("explain:\n%s", out.String())
	}
}

// The margins follow the meter's recent under-reading: readings that rise faster than the
// meter explains raise k5.
func TestMarginsAtThePick(t *testing.T) {
	rst := t0.Add(3 * time.Hour)
	var five []Sample
	var toks []TokenUse
	for i := 0; i < 6; i++ {
		at := t0.Add(time.Duration(i-6) * 10 * time.Minute)
		toks = append(toks, TokenUse{At: at, Session: "S", Slot: "~/.a", Units: 1e6})
		five = append(five, Sample{At: at.Add(time.Minute), Pct: float64(3 * (i + 1)), ResetsAt: rst, Source: "statusline"})
	}
	l := five[len(five)-1]
	a := cand("a", Window{Key: "five_hour", UsedPct: l.Pct, ResetsAt: rst, UpdatedAt: l.At, Source: "hub", Samples: five},
		win("seven_day", 10, 72*time.Hour, time.Minute))
	toks = append(toks, TokenUse{At: t0.Add(-10 * time.Second), Session: "S", Slot: "~/.a", Units: 1e6})
	res, err := Decide(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{a}, Outbox: Outbox{Tokens: toks}, N: 1})
	if err != nil {
		t.Fatal(err)
	}
	v := res.Ranked[0].Lanes
	// Each pair rose 3 against 1.4 expected: e = 1.6 over max(1.4, 2) = 0.8.
	if c := v.Calibration; math.Abs(c.Corrector5h-0.8) > 1e-9 || math.Abs(c.Margin5hPct-1.12) > 1e-9 {
		t.Fatalf("calibration %+v", c)
	}
	if math.Abs(v.Meter5hPct-2.5) > 0.051 { // 1.4 metered + 1.12 margin
		t.Fatalf("meter %v", v.Meter5hPct)
	}
}
