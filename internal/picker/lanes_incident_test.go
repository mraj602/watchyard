package picker

// The 2026-11-23 incident (testdata/lanes_incident_1123.json): at 19:03:48Z the worker asked
// for six lanes. claude-2 (aggressive) was at 86% of its week with the reset at 04:00Z and 32%
// of its 5-hour window, its three lanes of 18:46 had ended; the shipped rr_exp sent the six lanes
// one per account, three of them to protected accounts. The drain order must send them to
// claude-2 and none to a protected account.

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

type incidentDoc struct {
	Now      string   `json:"now"`
	N        int      `json:"n"`
	Actual   []string `json:"actual"`
	Accounts []struct {
		Label     string  `json:"label"`
		Protected bool    `json:"protected"`
		Ratio     float64 `json:"ratio"`
		F         float64 `json:"f"`
		Meter     float64 `json:"meter_units"`
		Five      struct {
			TS    string  `json:"ts"`
			Pct   float64 `json:"pct"`
			Reset *string `json:"reset"`
		} `json:"five"`
		Weekly struct {
			TS    string  `json:"ts"`
			Pct   float64 `json:"pct"`
			Reset *string `json:"reset"`
		} `json:"weekly"`
	} `json:"accounts"`
	Leases []struct {
		Label    string  `json:"label"`
		T0       string  `json:"t0"`
		SID      string  `json:"sid"`
		Launched bool    `json:"launched"`
		Ended    bool    `json:"ended"`
		Burned   float64 `json:"burned_units"`
		Recent   float64 `json:"recent_units"`
		Measured bool    `json:"measured"`
	} `json:"leases"`
}

func incidentInput(t *testing.T, p LaneParams) (LaneInput, incidentDoc) {
	t.Helper()
	b, err := os.ReadFile("testdata/lanes_incident_1123.json")
	if err != nil {
		t.Fatal(err)
	}
	var d incidentDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	tm := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	opt := func(s *string) time.Time {
		if s == nil {
			return time.Time{}
		}
		return tm(*s)
	}
	in := LaneInput{Now: tm(d.Now), Params: p, N: d.N}
	for i, a := range d.Accounts {
		w, h := a.Weekly.Pct, a.Five.Pct
		in.Accounts = append(in.Accounts, LaneAccount{Key: a.Label, Ord: i, Protected: a.Protected, Ratio: a.Ratio, F5: a.F / 1e6,
			Sample: LaneSample{TS: tm(a.Five.TS), Weekly: &w, FiveHour: &h, WeeklyReset: opt(a.Weekly.Reset), FiveHourReset: opt(a.Five.Reset)},
			Meter:  LaneMeter{M5: a.Meter, M7: a.Meter, KeepH: true}})
	}
	for _, l := range d.Leases {
		in.Leases = append(in.Leases, LaneLease{Key: l.Label, T0: tm(l.T0), SID: l.SID, Launched: l.Launched, Ended: l.Ended,
			Burned: l.Burned, Seen: l.Burned > 0, Recent: l.Recent, RecentOK: l.Measured, From: "hub"})
	}
	return in, d
}

func TestDrainIncident1123(t *testing.T) {
	for _, c := range []struct {
		name  string
		p     LaneParams
		stats LaneStats
	}{
		{"v3 fallback stats", DefaultLaneParams, LaneStats{}},
		// The day's measured lanes (11 finished on the worker: mean 6.0, p75 8.9 points, median 12 min).
		{"v3 measured stats", DefaultLaneParams, LaneStats{Cost: 8.9, Mean: 6.0, Len: 12 * time.Minute, N: 11, NLen: 11}},
	} {
		in, _ := incidentInput(t, c.p)
		in.Stats = c.stats
		r := rank(t, in)
		got := picksOf(r)
		t.Logf("%s: %v; claude-2 %+v", c.name, got, rowOf(r, "claude-2").Drain)
		if protPicks(r) != 0 || countOf(r, "claude-2") != 6 || r.Waiting != 0 {
			t.Errorf("%s: picks %v: all six to claude-2, none protected", c.name, got)
		}
		for _, p := range r.Picks {
			if !p.Drain || !p.FinishBy.Equal(time.Date(2026, 11, 24, 3, 50, 0, 481000000, time.UTC)) {
				t.Errorf("pick %+v: finish_by 03:50Z", p)
			}
		}
	}
	for _, c := range []struct {
		name string
		p    LaneParams
	}{{"rr_exp", ReferenceLaneParams}, {"draft", DraftLaneParams}} {
		in, d := incidentInput(t, c.p)
		r := rank(t, in)
		t.Logf("%s: %v (the worker picked %v)", c.name, picksOf(r), d.Actual)
		if c.name == "rr_exp" && (countOf(r, "claude-2") != 0 || protPicks(r) != 3) {
			t.Errorf("rr_exp no longer reproduces the incident: %v", picksOf(r))
		}
	}
}
