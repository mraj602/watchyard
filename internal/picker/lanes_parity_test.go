package picker

// Parity with the reference ranker: testdata/lanes_golden.json.gz holds calls of the
// reference implementation (rank_v2 with the shipped parameters and the P1 fallback prices)
// over a week of real limit samples, replayed forward so the leases, burst caps, waits and
// urgent picks build up as they would. Each case stores the inputs and what the reference
// decided; RankLanes must decide the same.

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

type goldenDoc struct {
	Params map[string]any `json:"params"`
	V2     map[string]any `json:"v2_params"`
	Leases []goldenLease  `json:"leases"`
	Cases  []goldenCase   `json:"cases"`
}

type goldenLease struct {
	ID       int     `json:"id"`
	Account  string  `json:"account"`
	T0       string  `json:"t0"`
	SID      string  `json:"sid"`
	C5       float64 `json:"c5"`
	CF       float64 `json:"cf"`
	Launched bool    `json:"launched"`
}

type goldenSample struct {
	TS            *string  `json:"ts"`
	Weekly        *float64 `json:"weekly"`
	FiveHour      *float64 `json:"five_hour"`
	WeeklyReset   *string  `json:"weekly_reset"`
	FiveHourReset *string  `json:"five_hour_reset"`
}

type goldenCase struct {
	Now       string                  `json:"now"`
	N         int                     `json:"n"`
	Urgent    bool                    `json:"urgent"`
	Samples   map[string]goldenSample `json:"samples"`
	Protected []string                `json:"protected"`
	Metered   map[string]struct {
		M5    float64 `json:"m5"`
		M7    float64 `json:"m7"`
		KeepH bool    `json:"keep_h"`
	} `json:"metered"`
	Burned map[string]float64 `json:"burned"`
	Ended  []string           `json:"ended"`
	Live   []string           `json:"live"`
	SIDs   []string           `json:"sids"`
	Leases []int              `json:"leases"`
	Want   struct {
		Error    string    `json:"error"`
		SID      string    `json:"sid"`
		Picks    []*string `json:"picks"`
		NextFree *string   `json:"next_free"`
		Table    []struct {
			Account       string  `json:"account"`
			Score         float64 `json:"score"`
			Skip          bool    `json:"skip"`
			Reason        string  `json:"reason"`
			Weekly        float64 `json:"weekly"`
			FiveHour      float64 `json:"five_hour"`
			WeeklyReserve float64 `json:"weekly_reserve"`
			HoursToReset  float64 `json:"hours_to_reset"`
			Kind          string  `json:"kind"`
			Leases        int     `json:"leases"`
			Added5h       float64 `json:"added_5h"`
			SampleAgeMin  float64 `json:"sample_age_min"`
			SkipLevel     int     `json:"skip_level"`
			Last          bool    `json:"last"`
			Forced        bool    `json:"forced"`
			Capped        bool    `json:"capped"`
			ETA           *string `json:"eta"`
		} `json:"table"`
		New []struct {
			Account string  `json:"account"`
			SID     *string `json:"sid"`
			Forced  bool    `json:"forced"`
			C5      float64 `json:"c5"`
			CF      float64 `json:"cf"`
		} `json:"new"`
	} `json:"want"`
}

func loadGolden(t *testing.T) goldenDoc {
	t.Helper()
	f, err := os.Open("testdata/lanes_golden.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var d goldenDoc
	if err := json.NewDecoder(zr).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func goldenTime(t *testing.T, s *string) time.Time {
	t.Helper()
	if s == nil || *s == "" {
		return time.Time{}
	}
	v, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		t.Fatalf("time %q: %v", *s, err)
	}
	return v
}

// TestLanesGoldenParams: the golden cases were made with the shipped parameters; a change
// to DefaultLaneParams needs new golden cases.
func TestLanesGoldenParams(t *testing.T) {
	d := loadGolden(t)
	p := DefaultLaneParams
	num := func(m map[string]any, k string) float64 {
		v, ok := m[k].(float64)
		if !ok {
			t.Fatalf("golden %s: %v", k, m[k])
		}
		return v
	}
	checks := []struct {
		name      string
		got, want float64
	}{
		{"target", p.Target, num(d.V2, "target")},
		{"headstart", p.Headstart, num(d.V2, "headstart")},
		{"handicap", p.Handicap, num(d.V2, "handicap")},
		{"cap5h", p.Cap5h, num(d.V2, "cap5h")},
		{"lane5h", p.Lane5h, num(d.V2, "lane5h")},
		{"prot_min_hours", p.ProtMinHours, num(d.V2, "prot_min_hours")},
		{"min_hours", p.MinHours, num(d.V2, "min_hours")},
		{"aggr_skip", p.AggrSkip, num(d.V2, "aggr_skip")},
		{"h5_weight", p.H5Weight, num(d.V2, "h5_weight")},
		{"exp_h", p.ExpH, num(d.V2, "exp_h")},
		{"exp_max5h", p.ExpMax5h, num(d.V2, "exp_max5h")},
		{"batch_cap", float64(p.BurstAggr), num(d.V2, "batch_cap")},
		{"batch_cap_prot", float64(p.BurstProt), num(d.V2, "batch_cap_prot")},
		{"batch_window_min", p.BurstWindow.Minutes(), num(d.V2, "batch_window_min")},
		{"lease_ttl_h", p.LeaseTTL.Hours(), num(d.V2, "lease_ttl_h")},
		{"settle_min", p.Settle.Minutes(), num(d.V2, "settle_min")},
		{"stale_guard_min", p.StaleGuard.Minutes(), num(d.V2, "stale_guard_min")},
		{"stale_lease_h", p.StaleLease.Hours(), num(d.V2, "stale_lease_h")},
		{"unlaunched_min", p.Unlaunched.Minutes(), num(d.V2, "unlaunched_min")},
		{"lease_price", p.LeasePrice, num(d.Params, "lease_price")},
		{"cost_mult", p.CostMult, num(d.Params, "cost_mult")},
		{"meter_f5", p.F5, num(d.Params, "meter_f5")},
		{"ratio", p.Ratio, num(d.Params, "ratio_5h_7d")},
		{"meter_ratio", p.Ratio, num(d.Params, "meter_ratio")},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: DefaultLaneParams %v, golden %v", c.name, c.got, c.want)
		}
	}
	if d.Params["lane_full5"] != nil || p.LaneFull5 != 0 {
		t.Errorf("lane_full5: golden %v, DefaultLaneParams %v", d.Params["lane_full5"], p.LaneFull5)
	}
	if d.V2["order"] != "rr_exp" || d.V2["missing5h"] != "zero" || d.V2["stale_mode"] != "skip_protected" {
		t.Errorf("golden order/missing5h/stale_mode: %v %v %v", d.V2["order"], d.V2["missing5h"], d.V2["stale_mode"])
	}
}

func TestLanesParity(t *testing.T) {
	d := loadGolden(t)
	if len(d.Cases) < 100 {
		t.Fatalf("only %d golden cases", len(d.Cases))
	}
	var picks, waits, forced, errs, rows int
	for ci, c := range d.Cases {
		in := goldenInput(t, d, c)
		res, err := RankLanes(in)
		if c.Want.Error != "" {
			var de DuplicateSessionError
			if !errors.Is(err, ErrDuplicateSession) || !errors.As(err, &de) || de.SID != c.Want.SID {
				t.Errorf("case %d: want duplicate %s, got %v", ci, c.Want.SID, err)
			}
			errs++
			continue
		}
		if err != nil {
			t.Fatalf("case %d: %v", ci, err)
		}
		// picks: the placed accounts in order, then None for each waiting lane
		var wantPlaced []string
		wantWait := 0
		for _, p := range c.Want.Picks {
			if p == nil {
				wantWait++
			} else {
				wantPlaced = append(wantPlaced, *p)
			}
		}
		var got []string
		for _, p := range res.Picks {
			got = append(got, p.Key)
		}
		if !equalStrings(got, wantPlaced) || res.Waiting != wantWait {
			t.Errorf("case %d (%s, n=%d urgent=%v): picks %v waiting %d, want %v waiting %d", ci, c.Now, c.N, c.Urgent, got, res.Waiting, wantPlaced, wantWait)
		}
		if len(res.Picks) == len(c.Want.New) {
			for i, nw := range c.Want.New {
				g := res.Picks[i]
				sid := ""
				if nw.SID != nil {
					sid = *nw.SID
				}
				if g.SID != sid || g.Forced != nw.Forced || g.C5 != nw.C5 || g.CF != nw.CF {
					t.Errorf("case %d: new lease %d = %+v, want %+v", ci, i, g, nw)
				}
				if g.Forced {
					forced++
				}
			}
		}
		picks += len(res.Picks)
		waits += res.Waiting
		if len(res.Table) != len(c.Want.Table) {
			t.Errorf("case %d: %d rows, want %d", ci, len(res.Table), len(c.Want.Table))
			continue
		}
		for i, w := range c.Want.Table {
			g := res.Table[i]
			rows++
			kind := "aggressive"
			if g.Protected {
				kind = "protected"
			}
			if g.Key != w.Account || g.Skip != w.Skip || g.Last != w.Last || g.Capped != w.Capped ||
				g.Forced != w.Forced || g.SkipLevel != w.SkipLevel || g.Leases != w.Leases || kind != w.Kind ||
				g.Reason != w.Reason {
				t.Errorf("case %d row %d: got %s skip=%v last=%v capped=%v forced=%v level=%d leases=%d %s %q\n  want %s skip=%v last=%v capped=%v forced=%v level=%d leases=%d %s %q",
					ci, i, g.Key, g.Skip, g.Last, g.Capped, g.Forced, g.SkipLevel, g.Leases, kind, g.Reason,
					w.Account, w.Skip, w.Last, w.Capped, w.Forced, w.SkipLevel, w.Leases, w.Kind, w.Reason)
				continue
			}
			for _, x := range []struct {
				name      string
				got, want float64
				digits    int
			}{
				{"weekly", g.Weekly, w.Weekly, 1}, {"five_hour", g.FiveHour, w.FiveHour, 1},
				{"weekly_reserve", g.Reserve, w.WeeklyReserve, 1}, {"hours_to_reset", g.HoursToReset, w.HoursToReset, 2},
				{"score", g.Score, w.Score, 4}, {"added_5h", g.Added5, w.Added5h, 2}, {"sample_age_min", g.SampleAgeMin, w.SampleAgeMin, 1},
			} {
				if !roundsTo(x.got, x.want, x.digits) {
					t.Errorf("case %d row %d (%s): %s %v, want %v", ci, i, g.Key, x.name, x.got, x.want)
				}
			}
			if !sameTime(g.ETA, goldenTimePtr(t, w.ETA)) {
				t.Errorf("case %d row %d (%s): eta %v, want %v", ci, i, g.Key, g.ETA, w.ETA)
			}
		}
		if !sameTime(res.NextFree, goldenTimePtr(t, c.Want.NextFree)) {
			t.Errorf("case %d: next_free %v, want %v", ci, res.NextFree, c.Want.NextFree)
		}
	}
	t.Logf("%d cases: %d picks, %d waiting lanes, %d urgent picks, %d duplicate-id refusals, %d rows compared",
		len(d.Cases), picks, waits, forced, errs, rows)
	if waits == 0 || forced == 0 || errs == 0 {
		t.Errorf("golden cases no longer cover waits (%d), urgent picks (%d) and duplicates (%d)", waits, forced, errs)
	}
}

func goldenInput(t *testing.T, d goldenDoc, c goldenCase) LaneInput {
	t.Helper()
	now := goldenTime(t, &c.Now)
	prot := map[string]bool{}
	for _, a := range c.Protected {
		prot[a] = true
	}
	live := map[string]bool{}
	for _, a := range c.Live {
		live[a] = true
	}
	ended := map[string]bool{}
	for _, s := range c.Ended {
		ended[s] = true
	}
	names := make([]string, 0, len(c.Samples))
	for a := range c.Samples {
		names = append(names, a)
	}
	sort.Strings(names)
	in := LaneInput{Now: now, Params: DefaultLaneParams, N: c.N, SIDs: c.SIDs, Urgent: c.Urgent}
	for i, a := range names {
		s := c.Samples[a]
		acc := LaneAccount{Key: a, Ord: i, Protected: prot[a], Live: live[a],
			Sample: LaneSample{TS: goldenTime(t, s.TS), Weekly: s.Weekly, FiveHour: s.FiveHour,
				WeeklyReset: goldenTime(t, s.WeeklyReset), FiveHourReset: goldenTime(t, s.FiveHourReset)}}
		if m, ok := c.Metered[a]; ok {
			acc.Meter = LaneMeter{M5: m.M5, M7: m.M7, KeepH: m.KeepH}
		} else {
			acc.Meter.KeepH = true
		}
		in.Accounts = append(in.Accounts, acc)
	}
	for _, id := range c.Leases {
		L := d.Leases[id]
		if L.ID != id {
			t.Fatalf("lease pool out of order at %d", id)
		}
		c5, cf := L.C5, L.CF
		b, seen := c.Burned[L.SID]
		in.Leases = append(in.Leases, LaneLease{Key: L.Account, T0: goldenTime(t, &L.T0), SID: L.SID, C5: &c5, CF: &cf,
			Launched: L.Launched, Ended: L.SID != "" && ended[L.SID], Burned: b, Seen: seen})
	}
	return in
}

func goldenTimePtr(t *testing.T, s *string) *time.Time {
	if s == nil {
		return nil
	}
	v := goldenTime(t, s)
	return &v
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	d := a.Sub(*b)
	return d < time.Millisecond && d > -time.Millisecond
}

// roundsTo: got, rounded as the reference rounds, is want (a last-digit tie may round
// either way when the two computations differ in the last bit).
func roundsTo(got, want float64, digits int) bool {
	if pyRound(got, digits) == want {
		return true
	}
	return math.Abs(got-want) <= 0.5*math.Pow(10, -float64(digits))+1e-9
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
