package picker

// Unit tests of the lanes ranker, translated from the reference's unit suite (sections 1-7:
// no protected fallback, ordering, leases and caps, protected reserve and stale guard,
// validation, missing 5-hour readings, duplicate session ids), then tests of the data the
// adapter builds for it (readings, the token meter, unexplained claude.ai use).

import (
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/leases"
)

var lnow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

var (
	protNames = []string{"claude-1", "claude-5", "claude-7"}
	aggrNames = []string{"claude-2", "claude-3", "claude-4", "claude-6"}
	allNames  = []string{"claude-1", "claude-2", "claude-3", "claude-4", "claude-5", "claude-6", "claude-7"}
)

func isProt(name string) bool {
	for _, p := range protNames {
		if p == name {
			return true
		}
	}
	return false
}

func hrs(h float64) time.Time { return lnow.Add(time.Duration(h * float64(time.Hour))) }

func fp(v float64) *float64 { return &v }

// smp is a reading: weekly w, 5-hour h5, ageMin old, weekly reset in resetH hours, 5-hour
// reset in 3 hours.
func smp(w float64, h5 float64, ageMin float64, resetH float64) LaneSample {
	return LaneSample{TS: hrs(-ageMin / 60), Weekly: fp(w), FiveHour: fp(h5), WeeklyReset: hrs(resetH), FiveHourReset: hrs(3)}
}

// world is every account with a fresh reading: aggressive at aggrW, protected at protW.
func world(aggrW, protW float64) map[string]LaneSample {
	s := map[string]LaneSample{}
	for _, a := range allNames {
		w := aggrW
		if isProt(a) {
			w = protW
		}
		s[a] = smp(w, 10, 5, 72)
	}
	return s
}

func laneIn(s map[string]LaneSample, n int) LaneInput {
	in := LaneInput{Now: lnow, Params: DefaultLaneParams, N: n}
	for i, a := range allNames {
		sm, ok := s[a]
		if !ok {
			continue
		}
		in.Accounts = append(in.Accounts, LaneAccount{Key: a, Ord: i, Protected: isProt(a), Sample: sm, Meter: LaneMeter{KeepH: true}})
	}
	return in
}

func rank(t *testing.T, in LaneInput) LaneResult {
	t.Helper()
	r, err := RankLanes(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rowOf(r LaneResult, key string) LaneRow {
	for _, row := range r.Table {
		if row.Key == key {
			return row
		}
	}
	return LaneRow{}
}

func lease(key string, agoH float64, sid string, launched bool) LaneLease {
	return LaneLease{Key: key, T0: hrs(-agoH), SID: sid, C5: fp(8), Launched: launched}
}

// 1. No fallback onto a protected account, ever.
func TestLanesNoProtectedFallback(t *testing.T) {
	r := rank(t, laneIn(world(99, 89), 3))
	for _, p := range r.Picks {
		if isProt(p.Key) {
			t.Fatalf("aggressive at 99%%: picked protected %s", p.Key)
		}
	}
	s := world(99, 95)
	for _, a := range protNames {
		x := s[a]
		x.FiveHour = fp(90)
		s[a] = x
	}
	r = rank(t, laneIn(s, 2))
	if len(r.Picks) != 0 || r.Waiting != 2 {
		t.Fatalf("everything skipped: picks %v waiting %d", r.Picks, r.Waiting)
	}
	if r.NextFree == nil || !r.NextFree.After(lnow) {
		t.Fatalf("next free %v, want a time after now", r.NextFree)
	}
}

// 1. Burst caps and --urgent: only the cap is passed, only for aggressive accounts.
func TestLanesUrgentPassesOnlyTheCap(t *testing.T) {
	s := world(40, 95)
	var ls []LaneLease
	for _, a := range aggrNames {
		for j := 0; j < 8; j++ {
			ls = append(ls, lease(a, 0.2, "", true))
		}
	}
	in := laneIn(s, 1)
	in.Leases = ls
	r := rank(t, in)
	if len(r.Picks) != 0 {
		t.Fatalf("burst-capped aggressive and full protected: picked %v without --urgent", r.Picks)
	}
	for _, a := range aggrNames {
		row := rowOf(r, a)
		if !row.Capped || row.SkipLevel != 0 || row.ETA == nil || row.ETA.Sub(lnow.Add(48*time.Minute)).Abs() > 2*time.Minute {
			t.Errorf("%s: capped %v level %d eta %v; want the oldest lease in the window + 60 min", a, row.Capped, row.SkipLevel, row.ETA)
		}
	}
	in.Urgent = true
	r = rank(t, in)
	if len(r.Picks) != 1 || isProt(r.Picks[0].Key) || !r.Picks[0].Forced {
		t.Fatalf("--urgent: %+v", r.Picks)
	}
	for _, a := range aggrNames {
		s[a] = smp(99, 10, 5, 72)
	}
	in = laneIn(s, 1)
	in.Leases, in.Urgent = ls, true
	if r := rank(t, in); len(r.Picks) != 0 {
		t.Fatalf("--urgent at the hard limit picked %v", r.Picks)
	}
}

// 2. Order: round robin, the newest lease oldest first; a batch visits every account once;
// expiring aggressive accounts go first.
func TestLanesOrder(t *testing.T) {
	in := laneIn(world(30, 20), 1)
	in.Leases = []LaneLease{lease("claude-2", 0.5, "a", true), lease("claude-3", 0.4, "b", true)}
	if p := rank(t, in).Picks[0].Key; p == "claude-2" || p == "claude-3" {
		t.Errorf("a recently leased account was picked first: %s", p)
	}
	in = laneIn(world(30, 20), 7)
	seen := map[string]bool{}
	for _, p := range rank(t, in).Picks {
		seen[p.Key] = true
	}
	if len(seen) != 7 {
		t.Errorf("a batch of 7 visited %d accounts", len(seen))
	}
	// claude-6 resets in 24 h with 5h under 30: it goes first even though it was leased last.
	s := world(30, 20)
	s["claude-6"] = smp(30, 10, 5, 24)
	in = laneIn(s, 1)
	in.Leases = []LaneLease{lease("claude-6", 0.1, "x", true)}
	r := rank(t, in)
	if r.Picks[0].Key != "claude-6" || !rowOf(r, "claude-6").Expiring {
		t.Errorf("expiring account not first: %s", r.Picks[0].Key)
	}
	s["claude-6"] = smp(30, 35, 5, 24) // 5h at 35: no longer expiring-first
	in = laneIn(s, 1)
	in.Leases = []LaneLease{lease("claude-6", 0.1, "x", true)}
	if p := rank(t, in).Picks[0].Key; p == "claude-6" {
		t.Errorf("claude-6 with 5h 35 still went first")
	}
}

func five(t *testing.T, in LaneInput, key string) (h, w float64) {
	t.Helper()
	row := rowOf(rank(t, in), key)
	return row.FiveHour, row.Weekly
}

// 3. Leases: an active lease charges c - burned; SessionEnd ends it; an unlaunched lease
// counts for 10 minutes; an expired lease the meter never saw keeps its weekly charge.
func TestLanesLeaseCharges(t *testing.T) {
	s := world(30, 20)
	base, _ := five(t, laneIn(s, 1), "claude-4")
	in := laneIn(s, 1)
	in.Leases = []LaneLease{lease("claude-4", 1, "s1", true)}
	if h, _ := five(t, in, "claude-4"); math.Abs(h-base-8) > 0.2 {
		t.Errorf("active lease adds %.2f to 5h, want 8", h-base)
	}
	in.Leases[0].Burned, in.Leases[0].Seen = 5/DefaultLaneParams.F5, true
	if h, _ := five(t, in, "claude-4"); math.Abs(h-base-3) > 0.2 {
		t.Errorf("lease with 5 points burned adds %.2f, want 3", h-base)
	}
	in.Leases = []LaneLease{lease("claude-4", 1, "s1", true)}
	in.Leases[0].Ended = true
	if h, _ := five(t, in, "claude-4"); math.Abs(h-base) > 0.2 {
		t.Errorf("ended lease adds %.2f", h-base)
	}
	in.Leases = []LaneLease{lease("claude-4", 0.25, "u1", false)}
	if h, _ := five(t, in, "claude-4"); math.Abs(h-base) > 0.2 {
		t.Errorf("unlaunched lease older than 10 min adds %.2f", h-base)
	}
	in.Leases = []LaneLease{lease("claude-4", 0.05, "u2", false)}
	if h, _ := five(t, in, "claude-4"); h < base+7 {
		t.Errorf("unlaunched lease younger than 10 min adds %.2f, want 8", h-base)
	}

	s6 := world(30, 20)
	s6["claude-4"] = smp(30, 10, 7*60, 72) // reading 7 h old, lease 6 h old
	_, bw := five(t, laneIn(s6, 1), "claude-4")
	in = laneIn(s6, 1)
	in.Leases = []LaneLease{lease("claude-4", 6, "old", true)}
	if _, w := five(t, in, "claude-4"); w < bw+1 {
		t.Errorf("expired unseen lease: weekly %.2f vs %.2f, want its charge kept", w, bw)
	}
	in.Leases[0].Burned, in.Leases[0].Seen = 2/DefaultLaneParams.F5, true
	if _, w := five(t, in, "claude-4"); math.Abs(w-bw) > 0.05 {
		t.Errorf("expired lease seen by the meter: weekly %.2f vs %.2f", w, bw)
	}
	s7 := world(30, 20)
	s7["claude-4"] = smp(30, 10, 60, 72) // a reading newer than the lease
	_, bw = five(t, laneIn(s7, 1), "claude-4")
	in = laneIn(s7, 1)
	in.Leases = []LaneLease{lease("claude-4", 6, "old2", true)}
	if _, w := five(t, in, "claude-4"); math.Abs(w-bw) > 0.05 {
		t.Errorf("a newer reading should end the kept weekly charge: %.2f vs %.2f", w, bw)
	}
}

// 3. Burst caps hold inside one batch; new leases carry no session id unless given.
func TestLanesBatchCaps(t *testing.T) {
	r := rank(t, laneIn(world(30, 20), 30))
	n := map[string]int{}
	for _, p := range r.Picks {
		n[p.Key]++
		if p.SID != "" || p.C5 != 8 || p.CF != 8 {
			t.Errorf("pick %+v", p)
		}
	}
	for _, a := range allNames {
		if (isProt(a) && n[a] > 2) || n[a] > 6 {
			t.Errorf("caps broken: %v", n)
		}
	}
	if len(r.Picks) != 4*6+3*2 || r.Waiting != 0 {
		t.Errorf("%d picks, %d waiting; want 30 placed", len(r.Picks), r.Waiting)
	}
	r = rank(t, laneIn(world(30, 20), 31))
	if len(r.Picks) != 30 || r.Waiting != 1 {
		t.Errorf("31 asked: %d placed, %d waiting", len(r.Picks), r.Waiting)
	}
}

// 4. Protected: the running lanes' reserve and the stale guard; a stale aggressive account
// (used only by claude -p, which sends no readings) keeps its estimate.
func TestLanesProtectedReserveAndStale(t *testing.T) {
	s := world(97.9, 20)
	s["claude-1"] = smp(78, 10, 5, 10) // late in the week: pace allows 78
	in := laneIn(s, 1)
	cf := 30.0
	in.Leases = []LaneLease{{Key: "claude-1", T0: hrs(-0.3), SID: "r0", C5: fp(4), CF: &cf, Launched: true},
		{Key: "claude-1", T0: hrs(-0.3), SID: "r1", C5: fp(4), CF: &cf, Launched: true}}
	row := rowOf(rank(t, in), "claude-1")
	if !row.Skip || row.SkipLevel != 2 || !contains(row.Reason, "reserve") {
		t.Errorf("reserve: %+v", row)
	}
	s = world(30, 20)
	s["claude-5"] = smp(20, 10, 90, 72)
	s["claude-6"] = smp(20, 10, 8*60, 72)
	in = laneIn(s, 1)
	in.Leases = []LaneLease{lease("claude-5", 0.5, "st", true), lease("claude-6", 7, "st6", true)}
	r := rank(t, in)
	if row := rowOf(r, "claude-5"); !row.Skip || !contains(row.Reason, "stale") || row.SkipLevel != 1 {
		t.Errorf("stale protected: %+v", row)
	}
	if row := rowOf(r, "claude-6"); row.Skip || row.Last || !row.Stale {
		t.Errorf("stale aggressive: %+v", row)
	}
	// A protected account whose claude.ai readings stopped with no rate known is stale too.
	in = laneIn(world(30, 20), 1)
	in.Accounts[0].WebStale = true // claude-1
	if row := rowOf(rank(t, in), "claude-1"); !row.Skip || !contains(row.Reason, "claude.ai") {
		t.Errorf("web-stale protected: %+v", row)
	}
}

// 5. Validation: bad readings never crash; an unknown protected account is skipped, an
// unknown aggressive one ranked last.
func TestLanesValidation(t *testing.T) {
	bad := []float64{math.NaN(), math.Inf(1), math.Inf(-1), -5, 150}
	for _, v := range bad {
		for _, acc := range []string{"claude-1", "claude-3"} {
			for _, field := range []string{"weekly", "five_hour"} {
				s := world(30, 20)
				x := s[acc]
				if field == "weekly" {
					x.Weekly = fp(v)
				} else {
					x.FiveHour = fp(v)
				}
				s[acc] = x
				row := rowOf(rank(t, laneIn(s, 2)), acc)
				if isProt(acc) && !row.Skip {
					t.Errorf("%s %s=%v: protected not skipped", acc, field, v)
				}
				if !isProt(acc) && !row.Last && !row.Skip {
					t.Errorf("%s %s=%v: aggressive neither last nor skipped", acc, field, v)
				}
			}
		}
	}
	for _, ts := range []time.Time{{}, time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC), lnow.Add(48 * time.Hour)} {
		s := world(30, 20)
		for _, acc := range []string{"claude-1", "claude-3"} {
			x := s[acc]
			x.TS = ts
			s[acc] = x
		}
		r := rank(t, laneIn(s, 1))
		if row := rowOf(r, "claude-1"); !row.Skip {
			t.Errorf("ts %v: protected not skipped", ts)
		}
		if row := rowOf(r, "claude-3"); !row.Stale {
			t.Errorf("ts %v: aggressive not stale", ts)
		}
	}
	s := world(30, 20)
	s["claude-2"] = LaneSample{TS: hrs(-0.1), FiveHour: fp(10), WeeklyReset: hrs(72)}
	r := rank(t, laneIn(s, 7))
	if row := r.Table[len(r.Table)-1]; row.Key != "claude-2" && !rowOf(r, "claude-2").Last {
		t.Errorf("unknown weekly not ranked last: %+v", r.Table)
	}
	if r, _ := RankLanes(LaneInput{Now: lnow, Params: DefaultLaneParams, N: 1}); len(r.Picks) != 0 || r.Waiting != 1 {
		t.Errorf("no accounts: %+v", r)
	}
	if r := rank(t, laneIn(world(30, 20), 0)); len(r.Picks) != 0 || len(r.Table) != 0 {
		t.Errorf("n=0: %+v", r)
	}
	// A negative meter never lowers the estimate.
	in := laneIn(world(30, 20), 1)
	pos := rank(t, in)
	for i := range in.Accounts {
		in.Accounts[i].Meter = LaneMeter{M5: -5e8, M7: -5e7, KeepH: true}
	}
	neg := rank(t, in)
	for _, row := range pos.Table {
		if rowOf(neg, row.Key).FiveHour != row.FiveHour {
			t.Errorf("negative meter changed %s", row.Key)
		}
	}
}

// 5. Fuzz: random junk never crashes and never picks a skipped account (except --urgent).
func TestLanesFuzz(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	vals := []float64{-1, 50, 101, math.NaN(), 0, 99.9}
	pick := func() *float64 {
		switch rng.Intn(4) {
		case 0:
			return nil
		case 1:
			return fp(vals[rng.Intn(len(vals))])
		}
		return fp(rng.Float64() * 100)
	}
	tm := func() time.Time {
		if rng.Intn(5) == 0 {
			return time.Time{}
		}
		return hrs(rng.Float64()*400 - 200)
	}
	for i := 0; i < 2000; i++ {
		s := map[string]LaneSample{}
		for _, a := range allNames {
			if rng.Intn(10) == 0 {
				continue
			}
			s[a] = LaneSample{TS: tm(), Weekly: pick(), FiveHour: pick(), WeeklyReset: tm(), FiveHourReset: tm()}
		}
		in := laneIn(s, rng.Intn(6)-1)
		for j := 0; j < rng.Intn(10); j++ {
			in.Leases = append(in.Leases, LaneLease{Key: allNames[rng.Intn(7)], T0: tm(), Launched: rng.Intn(2) == 0,
				Burned: rng.Float64()*1e7 - 1e6, Seen: rng.Intn(2) == 0})
		}
		for k := range in.Accounts {
			in.Accounts[k].Meter = LaneMeter{M5: rng.Float64()*1e7 - 1e6, M7: rng.Float64() * 1e7, KeepH: rng.Intn(2) == 0}
			in.Accounts[k].Extra5 = rng.Float64()*20 - 5
			in.Accounts[k].Live = rng.Intn(2) == 0
		}
		r, err := RankLanes(in)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if len(r.Picks) > 0 {
			if row := rowOf(r, r.Picks[0].Key); row.Skip && !row.Forced {
				t.Fatalf("case %d: picked a skipped account %+v", i, row)
			}
		}
		for _, row := range r.Table {
			if row.FiveHour < 0 || row.Weekly < 0 || row.FiveHour > 100 || row.Weekly > 100 || math.IsNaN(row.Score) && !row.Skip {
				t.Fatalf("case %d: values out of range %+v", i, row)
			}
		}
	}
}

// 6. A missing 5-hour reading counts as 0 (the owner's rule), not as the last value seen.
func TestLanesMissingFiveHour(t *testing.T) {
	s := world(30, 20)
	s["claude-3"] = LaneSample{TS: hrs(-0.1), Weekly: fp(30), WeeklyReset: hrs(72)}
	if row := rowOf(rank(t, laneIn(s, 1)), "claude-3"); row.FiveHour >= 1 || row.Skip {
		t.Errorf("missing 5h: %+v", row)
	}
}

// 7. Duplicate session ids: against a live lease, and inside a batch.
func TestLanesDuplicateSessions(t *testing.T) {
	in := laneIn(world(30, 20), 1)
	in.Leases = []LaneLease{lease("claude-2", 0.1, "dup", false)}
	in.SIDs = []string{"dup"}
	var de DuplicateSessionError
	if _, err := RankLanes(in); !errors.Is(err, ErrDuplicateSession) || !errors.As(err, &de) || de.SID != "dup" {
		t.Errorf("duplicate against a lease: %v", err)
	}
	in.Leases[0].T0 = hrs(-0.5) // an unlaunched lease past 10 minutes was dropped: the id is free
	if _, err := RankLanes(in); err != nil {
		t.Errorf("a dropped lease's id refused: %v", err)
	}
	in = laneIn(world(30, 20), 2)
	in.SIDs = []string{"a", "a"}
	if _, err := RankLanes(in); !errors.Is(err, ErrDuplicateSession) {
		t.Errorf("duplicate inside a batch: %v", err)
	}
}

// Readings: a session idle through a reset keeps sending its old window with the old
// resets_at; it must not undo the reset. An idle reading newer than the chosen one wins.
func TestChooseReading(t *testing.T) {
	newR, oldR := lnow.Add(4*time.Hour), lnow.Add(-time.Hour)
	w := &Window{Key: "five_hour", Samples: []Sample{
		{At: lnow.Add(-30 * time.Minute), Pct: 12, ResetsAt: newR, Source: "statusline"},
		{At: lnow.Add(-5 * time.Minute), Pct: 88, ResetsAt: oldR, Source: "statusline"}, // stale window, newer in time
		{At: lnow.Add(-20 * time.Minute), Pct: 14, ResetsAt: newR.Add(2 * time.Minute), Source: "web"},
		{At: lnow.Add(-time.Minute), Pct: 150, ResetsAt: newR}, // invalid
	}}
	got := chooseReading(w, lnow)
	if !got.ok || got.pct != 14 || !got.at.Equal(lnow.Add(-20*time.Minute)) {
		t.Errorf("reading %+v, want the newest of the newest window (14)", got)
	}
	w.Samples = append(w.Samples, Sample{At: lnow.Add(-2 * time.Minute), Pct: 0}) // idle: not started
	if got := chooseReading(w, lnow); got.pct != 0 || !got.reset.IsZero() {
		t.Errorf("idle reading not used: %+v", got)
	}
	w = &Window{Key: "seven_day", Samples: []Sample{{At: lnow.Add(-time.Hour), Pct: 20}, {At: lnow.Add(time.Hour), Pct: 25}}}
	if got := chooseReading(w, lnow); got.pct != 25 || !got.at.Equal(lnow) {
		t.Errorf("no resets_at: %+v, want the newest with its time capped at now", got)
	}
	w = &Window{Key: "five_hour", UsedPct: 40, ResetsAt: newR, UpdatedAt: lnow.Add(-time.Minute)}
	if got := chooseReading(w, lnow); got.pct != 40 {
		t.Errorf("current window reading: %+v", got)
	}
	if got := chooseReading(nil, lnow); got.ok {
		t.Errorf("no window: %+v", got)
	}
}

func units(at ...float64) []unitAt {
	var us []unitAt
	for i := 0; i+1 < len(at); i += 2 {
		us = append(us, unitAt{hrs(at[i]), at[i+1]})
	}
	return us
}

// The token meter: since the reading while its 5-hour window is open; after the reset, the
// next window starts at the first token; the weekly part since the last weekly reset.
func TestMeter(t *testing.T) {
	us := units(-2, 100, -0.5, 10, -0.2, 1)
	ts := hrs(-1)
	open := reading{at: ts, pct: 30, reset: hrs(1), ok: true}
	if m, keep := meter5(us, ts, open, lnow); m != 11 || !keep {
		t.Errorf("open window: %v %v, want 11 true", m, keep)
	}
	ended := reading{at: hrs(-4), pct: 30, reset: hrs(-0.6), ok: true}
	if m, keep := meter5(us, hrs(-4), ended, lnow); m != 11 || keep {
		t.Errorf("ended window: %v %v, want 11 false", m, keep)
	}
	us2 := units(-5.5, 100, -0.2, 1) // a window started 5.5 h ago has ended too
	if m, _ := meter5(us2, hrs(-8), reading{at: hrs(-8), pct: 50, reset: hrs(-6), ok: true}, lnow); m != 1 {
		t.Errorf("second window: %v, want 1", m)
	}
	if m, keep := meter5(us, hrs(-1), reading{}, lnow); m != 11 || keep {
		t.Errorf("no 5h reading: %v %v", m, keep)
	}
	week := reading{at: hrs(-3), pct: 40, reset: hrs(-1 - 7*24), ok: true} // reset 1 h ago (rolled by a week)
	if m := meter7(us, week, lnow); m != 11 {
		t.Errorf("weekly since the last reset: %v, want 11", m)
	}
	week.reset = hrs(50)
	if m := meter7(us, week, lnow); m != 111 {
		t.Errorf("weekly since the reading: %v, want 111", m)
	}
}

// Unexplained claude.ai use: the rise of clean intervals per hour when they add up to 10
// minutes, else the web readings' rise less the meter, per hour; added once the newest
// reading is over 15 minutes old.
func TestWebUse(t *testing.T) {
	rst := hrs(3)
	w := &Window{Key: "five_hour", Samples: []Sample{
		{At: hrs(-1.5), Pct: 10, ResetsAt: rst, Source: "web"},
		{At: hrs(-1.2), Pct: 12, ResetsAt: rst, Source: "statusline"}, // a session ran: not clean
		{At: hrs(-1), Pct: 20, ResetsAt: rst, Source: "web"},
		{At: hrs(-0.5), Pct: 30, ResetsAt: rst, Source: "web"},
		{At: hrs(-0.4), Pct: 31, ResetsAt: rst, Source: "statusline"},
	}}
	f5 := 1e-6
	us := units(-0.7, 2e6) // 2 points the meter saw inside the web span: not clean either
	extra, hasWeb, from := webUse(w, us, hrs(-0.5), f5, lnow)
	// The last 60 minutes of web readings (the bound included): 10 % to 30 %, less 2 points
	// metered = 18 points per hour; the reading is 30 min old: 9 points.
	if !hasWeb || from != unexplainedNet || math.Abs(extra-9) > 1e-9 {
		t.Errorf("extra %v web %v from %q, want 9 true net", extra, hasWeb, from)
	}
	if extra, _, _ := webUse(w, us, hrs(-0.1), f5, lnow); extra != 0 {
		t.Errorf("a reading 6 min old: extra %v, want 0", extra)
	}
	// Without the status-line reading the first interval is clean: 10 points in 30 minutes.
	clean := &Window{Key: "five_hour", Samples: append([]Sample{w.Samples[0]}, w.Samples[2:]...)}
	if extra, _, from := webUse(clean, us, hrs(-0.5), f5, lnow); from != unexplainedClean || math.Abs(extra-10) > 1e-9 {
		t.Errorf("clean: extra %v from %q, want 10 clean", extra, from)
	}
	// Clean intervals under 10 minutes in all fall back to the net rate.
	short := &Window{Key: "five_hour", Samples: []Sample{
		{At: hrs(-1), Pct: 10, ResetsAt: rst, Source: "web"},
		{At: hrs(-0.95), Pct: 15, ResetsAt: rst, Source: "web"},
		{At: hrs(-0.5), Pct: 20, ResetsAt: rst, Source: "web"},
	}}
	if extra, _, from := webUse(short, units(-0.7, 2e6), hrs(-0.5), f5, lnow); from != unexplainedNet || math.Abs(extra-8) > 1e-9 {
		t.Errorf("short clean: extra %v from %q, want 8 net", extra, from)
	}
	one := &Window{Key: "five_hour", Samples: []Sample{{At: hrs(-1), Pct: 20, Source: "web"}}}
	if _, hasWeb, from := webUse(one, nil, hrs(-1), f5, lnow); !hasWeb || from != "" {
		t.Errorf("one web reading: web %v from %q, want true none", hasWeb, from)
	}
	old := &Window{Key: "five_hour", Samples: []Sample{{At: hrs(-7), Pct: 10, Source: "web"}, {At: hrs(-6), Pct: 30, Source: "web"}}}
	if _, _, from := webUse(old, nil, hrs(-6), f5, lnow); from != "" {
		t.Error("a rate older than 5 hours is still used")
	}
	if _, hasWeb, _ := webUse(&Window{Samples: []Sample{{At: hrs(-1), Pct: 5, Source: "statusline"}}}, nil, hrs(-1), f5, lnow); hasWeb {
		t.Error("statusline readings counted as web")
	}
}

// Decide with lanes: token rows of a leased session count against the lease's account, a
// hook event in the last 10 minutes marks it live, and the lease's burn is taken off.
func TestDecideLanesMeterAndLeases(t *testing.T) {
	s := SettingsFrom(config.Next{Accounts: map[string]config.NextAccount{"b": {Mode: "protected"}}})
	a := cand("a", win("five_hour", 10, 3*time.Hour, 30*time.Minute), win("seven_day", 30, 72*time.Hour, 30*time.Minute))
	b := cand("b", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 10, 72*time.Hour, time.Minute))
	c5 := 8.0
	ledger := []leases.Lease{{Line: leases.Line{Op: leases.OpLease, At: t0.Add(-20 * time.Minute), Key: a.Key(), SessionID: "S1", C5: &c5, CF: &c5}}}
	ob := Outbox{Sessions: map[string]HookSession{"S1": {Last: t0.Add(-2 * time.Minute), Slot: "~/.b"}},
		Tokens: []TokenUse{{At: t0.Add(-10 * time.Minute), Session: "S1", Units: 2 / DefaultLaneParams.F5},
			{At: t0.Add(-30 * time.Second), Session: "other", Slot: "~/.b", Units: 1 / DefaultLaneParams.F5}}}
	res, err := Decide(Input{Now: t0, Settings: s, Candidates: []Candidate{a, b}, Leases: ledger, Outbox: ob, N: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != config.NextStrategyLanes || len(res.Picks) != 1 {
		t.Fatalf("result %+v", res)
	}
	var ra, rb Ranked
	for _, r := range res.Ranked {
		switch r.Label {
		case "a":
			ra = r
		case "b":
			rb = r
		}
	}
	if ra.Lanes == nil || rb.Lanes == nil || ra.Kind != "aggressive" || rb.Kind != "protected" {
		t.Fatalf("a %+v b %+v", ra, rb)
	}
	// a: 10 + 2 metered (the lease's session, attributed to a despite its hook slot) + (8 - 2) lease.
	if math.Abs(ra.Lanes.FiveHourPct-18) > 0.11 || math.Abs(ra.Lanes.Meter5hPct-2) > 0.11 || ra.Lanes.Leases != 1 {
		t.Errorf("a lanes %+v", *ra.Lanes)
	}
	// b: 10 + 1 metered from its folder's own row.
	if math.Abs(rb.Lanes.FiveHourPct-11) > 0.11 || rb.Lanes.Leases != 0 {
		t.Errorf("b lanes %+v", *rb.Lanes)
	}
	if res.Picks[0].Label != "b" || res.Picks[0].LaneKind != "protected" {
		t.Errorf("pick %+v: b has no lease yet, round robin takes it first", res.Picks[0].Ranked.Label)
	}
	// The same session id again is refused.
	if _, err := Decide(Input{Now: t0, Settings: s, Candidates: []Candidate{a, b}, Leases: ledger, Outbox: ob, N: 1, SIDs: []string{"S1"}}); !errors.Is(err, ErrDuplicateSession) {
		t.Errorf("duplicate: %v", err)
	}
	// Two folders on one account are ranked once.
	a2 := a
	a2.Folder, a2.Label = "~/.a2", "a2"
	res, _ = Decide(Input{Now: t0, Settings: s, Candidates: []Candidate{a, a2}, N: 1})
	if len(res.Ranked) != 2 || res.Ranked[1].Eligible || !contains(res.Ranked[1].Excluded, "same account") {
		t.Errorf("shared account: %+v", res.Ranked)
	}
}

// Per-account caps: cap_7d is the aggressive limit or the protected target, less reserve_pct.
func TestLaneLimits(t *testing.T) {
	p := DefaultLaneParams
	if a, b := laneLimits(Policy{}, false, p); a != 0 || b != 0 {
		t.Errorf("no settings: %v %v, want the parameters (0)", a, b)
	}
	if a, b := laneLimits(Policy{SetCap7d: 90, Reserve: 5}, true, p); a != 85 || b != 40 {
		t.Errorf("protected cap 90 reserve 5: %v %v, want 85 40", a, b)
	}
	if a, b := laneLimits(Policy{SetCap5h: 70}, false, p); a != 0 || b != 70 {
		t.Errorf("aggressive cap_5h 70: %v %v", a, b)
	}
}

// The outbox is read once: limit samples, token rows merged over their streaming snapshots
// (the largest value of each counter), and per session its newest hook event and login.
func TestReadOutboxMeter(t *testing.T) {
	dir := t.TempDir()
	at := func(d time.Duration) string { return lnow.Add(d).Format(time.RFC3339Nano) }
	lines := []string{
		`{"v":1,"id":"01JA0000000000000000000001","kind":"token_rows","ts":"` + at(-9*time.Minute) + `","machine":"m_x","slot":"~/.claude-2","payload":{"rows":[{"key":"msg1","ts":"` + at(-10*time.Minute) + `","session_id":"S1","input":10,"output":100,"cache_5m":0,"cache_1h":0,"cache_read":0}]}}`,
		`{"v":1,"id":"01JA0000000000000000000002","kind":"token_rows","ts":"` + at(-8*time.Minute) + `","machine":"m_x","payload":{"rows":[{"key":"msg1","ts":"` + at(-10*time.Minute) + `","session_id":"S1","input":12,"output":90,"cache_5m":4,"cache_1h":1,"cache_read":1000}]}}`,
		`{"v":1,"id":"01JA0000000000000000000003","kind":"token_rows","ts":"` + at(-8*24*time.Hour) + `","machine":"m_x","payload":{"rows":[{"key":"old","ts":"` + at(-8*24*time.Hour) + `","session_id":"S0","input":1,"output":1,"cache_5m":0,"cache_1h":0,"cache_read":0}]}}`,
		`{"v":1,"id":"01JA0000000000000000000004","kind":"hook_event","ts":"` + at(-7*time.Minute) + `","machine":"m_x","slot":"~/.claude-2","seat":{"account":"a2","org":"o2"},"payload":{"hook_event_name":"SessionStart","session_id":"S1"}}`,
		`{"v":1,"id":"01JA0000000000000000000005","kind":"hook_event","ts":"` + at(-time.Minute) + `","machine":"m_x","slot":"~/.claude-2","payload":{"hook_event_name":"SessionEnd","session_id":"S1"}}`,
		`{"v":1,"id":"01JA0000000000000000000006","kind":"limit_sample","ts":"` + at(-3*time.Minute) + `","machine":"m_x","slot":"~/.claude-2","seat":{"account":"a2","org":"o2"},"derived":{"five_hour_pct":12,"seven_day_pct":30},"payload":{}}`,
		`not json`,
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, lnow.Format("2006-01-02")+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ob, err := ReadOutbox(dir, lnow.Add(-7*24*time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ob.Samples) != 1 || len(ob.Tokens) != 1 {
		t.Fatalf("samples %d tokens %+v", len(ob.Samples), ob.Tokens)
	}
	// 12 + 5 × 100 + 1.25 × 4 + 2 × 1 + 0.1 × 1000
	if u := ob.Tokens[0]; u.Session != "S1" || u.Slot != "~/.claude-2" || math.Abs(u.Units-619) > 1e-9 {
		t.Errorf("token %+v, want 619 units", u)
	}
	hs := ob.Sessions["S1"]
	if !hs.Ended || !hs.Last.Equal(lnow.Add(-time.Minute)) || hs.Account != "a2" || hs.Slot != "~/.claude-2" {
		t.Errorf("session %+v", hs)
	}
	plain, _ := ReadOutbox(dir, lnow.Add(-7*24*time.Hour), false)
	if len(plain.Tokens) != 0 || len(plain.Sessions) != 0 || len(plain.Samples) != 1 {
		t.Errorf("without the meter: %+v", plain)
	}
}
