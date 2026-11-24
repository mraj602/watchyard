package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

func lines(t *testing.T, path string, fn func([]byte)) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		fn(sc.Bytes())
	}
}

// WindowsOf finds the windows recorded in the golden file for each snapshot (testdata/
// snapshots.jsonl holds raw usage responses, windows.jsonl the expected windows), including
// keys it has never heard of (iguana_necktie).
func TestWindowsOfMatchesGolden(t *testing.T) {
	type snap struct {
		TS      int64  `json:"ts"`
		Account string `json:"account_id"`
		Raw     string `json:"raw"`
	}
	samples := map[string]map[string]Sample{}
	lines(t, "testdata/snapshots.jsonl", func(b []byte) {
		var s snap
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		m := map[string]Sample{}
		for _, w := range WindowsOf(json.RawMessage(s.Raw)) {
			m[w.Key] = w
		}
		samples[fmt.Sprint(s.TS, s.Account)] = m
	})
	checked := 0
	keys := map[string]bool{}
	lines(t, "testdata/windows.jsonl", func(b []byte) {
		var w struct {
			TS           int64    `json:"ts"`
			Account      string   `json:"account_id"`
			Key          string   `json:"key"`
			Pct          *float64 `json:"pct"`
			ResetsAt     string   `json:"resets_at"`
			LimitDollars *float64 `json:"limit_dollars"`
		}
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatal(err)
		}
		m, ok := samples[fmt.Sprint(w.TS, w.Account)]
		if !ok {
			return // its snapshot is not in the fixture sample
		}
		checked++
		keys[w.Key] = true
		s, ok := m[w.Key]
		if !ok {
			t.Errorf("%d %s: window %s not found", w.TS, w.Account, w.Key)
			return
		}
		if w.Pct != nil && (s.UsedPct == nil || math.Abs(*s.UsedPct-*w.Pct) > 0.05) { // the golden values are rounded to 0.1
			t.Errorf("%s: used %v, golden %v", w.Key, s.UsedPct, *w.Pct)
		}
		if w.ResetsAt != "" {
			want, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
			if !s.ResetsAt.Equal(want) {
				t.Errorf("%s: resets %v, golden %v", w.Key, s.ResetsAt, want)
			}
		}
		if w.LimitDollars != nil && (s.LimitDollars == nil || *s.LimitDollars != *w.LimitDollars) {
			t.Errorf("%s: limit dollars %v, golden %v", w.Key, s.LimitDollars, *w.LimitDollars)
		}
	})
	if checked == 0 || !keys["iguana_necktie"] || !keys["five_hour"] {
		t.Fatalf("checked %d windows, keys %v", checked, keys)
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 10, 12, 16, 30, 0, 0, time.UTC)
	for _, raw := range []string{`"2026-10-12T16:30:00+00:00"`, `"2026-10-12T16:30:00Z"`, `1791822600`, `1791822600000`} {
		if got := ParseTime(json.RawMessage(raw)); !got.Equal(want) {
			t.Errorf("ParseTime(%s) = %v", raw, got)
		}
	}
	if !ParseTime(json.RawMessage(`null`)).IsZero() {
		t.Error("null")
	}
}

func TestPace(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	resets := start.Add(10 * time.Hour)
	half := start.Add(5 * time.Hour)
	for used, band := range map[float64]string{30: "comfortable", 50: "on_track", 60: "fast", 80: "runaway"} {
		p := PaceOf(used, start, resets, half, nil)
		if p == nil || p.Band != band || p.ProjectedPct != used*2 {
			t.Errorf("used %v: %+v, want %s", used, p, band)
		}
	}
	if p := PaceOf(80, start, resets, half, nil); p.FullAt == nil || !p.FullAt.Equal(start.Add(time.Duration(6.25*float64(time.Hour)))) {
		t.Errorf("full at %v", p.FullAt)
	}
	if PaceOf(10, start, resets, start.Add(time.Minute), nil) != nil {
		t.Error("too young window has a pace")
	}
	if PaceOf(10, start, resets, resets, nil) != nil || PaceOf(10, time.Time{}, resets, half, nil) != nil {
		t.Error("pace after reset or without start")
	}
}

// A weekly window 89% used with 35 minutes left and a recent slope of 9 points an hour
// projects ~94%, where the whole week's average says ~89%; a quiet week projects where it is;
// too few or too narrow readings fall back to the average.
func TestPaceRecentRate(t *testing.T) {
	resets := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	start := resets.Add(-7 * 24 * time.Hour)
	now := resets.Add(-35 * time.Minute)
	at := func(minAgo int, used float64) Reading {
		return Reading{At: now.Add(-time.Duration(minAgo) * time.Minute), Used: used}
	}
	// 9 points an hour: 0.15 a minute.
	rising := []Reading{at(60, 89-9), at(45, 89-6.75), at(30, 89-4.5), at(15, 89-2.25), at(0, 89)}
	p := PaceOf(89, start, resets, now, rising)
	if p == nil || p.Basis != BasisRecent || p.ProjectedPct < 94 || p.ProjectedPct > 94.4 || p.RatePerHour < 8.9 || p.RatePerHour > 9.1 || p.Band != "on_track" {
		t.Fatalf("recent: %+v", p)
	}
	avg := PaceOf(89, start, resets, now, nil)
	if avg == nil || avg.Basis != BasisAverage || avg.ProjectedPct < 89 || avg.ProjectedPct > 89.6 {
		t.Fatalf("average: %+v", avg)
	}
	// Quiet: the same level in every reading projects the level itself.
	quiet := []Reading{at(60, 89), at(30, 89), at(0, 89)}
	if q := PaceOf(89, start, resets, now, quiet); q.Basis != BasisRecent || q.ProjectedPct != 89 || q.RatePerHour != 0 || q.FullAt != nil {
		t.Fatalf("quiet: %+v", q)
	}
	// A quiet week (40% a day and a half in) that is not burning now: 40, not the average's 187.
	wk := time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC)
	wstart, wresets := wk.Add(-36*time.Hour), wk.Add(-36*time.Hour).Add(7*24*time.Hour)
	flat := []Reading{{At: wk.Add(-50 * time.Minute), Used: 40}, {At: wk.Add(-25 * time.Minute), Used: 40}, {At: wk, Used: 40}}
	if q := PaceOf(40, wstart, wresets, wk, flat); q.ProjectedPct != 40 || q.Band != "comfortable" {
		t.Fatalf("quiet week: %+v", q)
	}
	if q := PaceOf(40, wstart, wresets, wk, nil); q.Basis != BasisAverage || q.ProjectedPct < 180 {
		t.Fatalf("quiet week average: %+v", q)
	}
	// Full time from the recent rate: 80% now, 10 an hour, 3 h left: full in 2 h.
	r2 := now.Add(3 * time.Hour)
	fast := []Reading{at(60, 70), at(30, 75), at(0, 80)}
	if f := PaceOf(80, r2.Add(-7*24*time.Hour), r2, now, fast); f.FullAt == nil || !f.FullAt.Equal(now.Add(2*time.Hour)) || f.Band != "fast" {
		t.Fatalf("full at: %+v", f)
	}
	// Too few readings, or readings bunched in a few minutes: the average.
	if q := PaceOf(89, start, resets, now, rising[:2]); q.Basis != BasisAverage {
		t.Fatalf("two readings: %+v", q)
	}
	bunched := []Reading{at(4, 88), at(2, 88.4), at(0, 89)}
	if q := PaceOf(89, start, resets, now, bunched); q.Basis != BasisAverage {
		t.Fatalf("bunched: %+v", q)
	}
	// Readings older than an hour do not count; a falling slope counts as 0.
	old := []Reading{at(180, 50), at(150, 60), at(120, 70)}
	if _, ok := RecentRate(old, now); ok {
		t.Fatal("old readings")
	}
	if r, ok := RecentRate([]Reading{at(50, 90), at(25, 89), at(0, 88)}, now); !ok || r != 0 {
		t.Fatalf("falling %v %v", r, ok)
	}
}

// An idle account (claude.ai: the 5-hour window has not started) reads as five_hour 0 % with
// no reset, seven_day absent, and the model-scoped week from limits[].
func TestWindowsOfIdleAccount(t *testing.T) {
	raw := json.RawMessage(`{"five_hour":{"utilization":0,"resets_at":null},"seven_day":null,"seven_day_opus":null,
		"extra_usage":{"is_enabled":false},
		"limits":[{"kind":"session","group":"session","percent":0,"resets_at":null,"is_active":true,"scope":null},
		  {"kind":"weekly_scoped","group":"weekly","percent":0,"resets_at":"2026-10-27T04:00:00+00:00","is_active":false,
		   "scope":{"model":{"id":null,"display_name":"Fable"},"surface":null}}]}`)
	ws := WindowsOf(raw)
	if len(ws) != 2 || ws[0].Key != "five_hour" || !ws[0].Idle() || ws[1].Key != "weekly_fable" || ws[1].Idle() {
		t.Fatalf("%+v", ws)
	}
	if !ws[1].ResetsAt.Equal(time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)) || !ws[1].Inactive {
		t.Fatalf("%+v", ws[1])
	}
}

// Unscoped limits[] items fill five_hour / seven_day when the top-level field is null; when
// both carry the window the top-level one (unrounded) is kept; unknown kinds are left out.
func TestWindowsOfLimitsFallback(t *testing.T) {
	raw := json.RawMessage(`{"five_hour":{"utilization":8.4,"resets_at":"2026-10-21T22:09:59Z"},"seven_day":null,
		"limits":[{"kind":"session","group":"session","percent":8,"resets_at":"2026-10-21T22:09:59Z","is_active":true},
		  {"kind":"weekly_all","group":"weekly","percent":7,"resets_at":"2026-10-28T03:00:00Z","is_active":false,"scope":{"model":null,"surface":null}},
		  {"kind":"weekly_odd","group":"weekly","percent":50,"resets_at":"2026-10-28T03:00:00Z"}]}`)
	ws := WindowsOf(raw)
	if len(ws) != 2 || ws[0].Key != "five_hour" || *ws[0].UsedPct != 8.4 || ws[1].Key != "seven_day" || *ws[1].UsedPct != 7 {
		t.Fatalf("%+v", ws)
	}
	if !ws[1].ResetsAt.Equal(time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)) || ws[1].Idle() {
		t.Fatalf("%+v", ws[1])
	}
	// Only limits[] (no top-level windows): an idle session is an idle five_hour.
	ws = WindowsOf(json.RawMessage(`{"limits":[{"kind":"session","group":"session","percent":0,"resets_at":null}]}`))
	if len(ws) != 1 || ws[0].Key != "five_hour" || !ws[0].Idle() {
		t.Fatalf("%+v", ws)
	}
}
