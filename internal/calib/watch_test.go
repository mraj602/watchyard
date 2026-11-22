package calib

import (
	"encoding/json"
	"testing"
	"time"
)

// A seat's synthetic readings: the weekly window resets Thursday 09:00 UTC, the 5-hour window
// at 16:00.
var (
	wBase      = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	wWeekReset = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	wFiveReset = time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC)
)

func wr(min int, src string, five, seven float64) Reading {
	r := Reading{At: wBase.Add(time.Duration(min) * time.Minute), Source: "statusline", Session: src,
		Five: pct(five), FiveReset: wFiveReset, Seven: pct(seven), SevenReset: wWeekReset}
	if src == SourceWeb {
		r.Source, r.Session = SourceWeb, ""
	}
	return r
}

func feed(w *Watch, rs ...Reading) []Event {
	var out []Event
	for _, r := range rs {
		out = append(out, w.Add(r)...)
	}
	return out
}

// warm brings the level to 64 % weekly (40 % 5-hour) from three sources.
func warm(w *Watch) {
	feed(w, wr(-60, "s1", 30, 60), wr(-40, SourceWeb, 35, 62), wr(-20, "s2", 40, 64))
}

// The 2026-10-17 pattern: the 5-hour and weekly windows both go back to 0 with the same
// resets_at, several sessions agree, one session keeps showing the old values.
func TestWatchServerReset(t *testing.T) {
	w := NewWatch()
	warm(w)
	if w.Level == nil || *w.Level != 64 {
		t.Fatalf("level = %v, want 64", w.Level)
	}
	if evs := feed(w, wr(0, SourceWeb, 0, 0)); len(evs) != 0 {
		t.Fatalf("first low reading decided: %+v", evs)
	}
	if w.Hold() != wBase.UnixMilli() {
		t.Fatalf("hold = %d, want the first low reading", w.Hold())
	}
	if evs := feed(w, wr(5, "s1", 0, 0), wr(8, "s2", 40, 64)); len(evs) != 0 {
		t.Fatalf("two sources decided: %+v", evs)
	}
	evs := feed(w, wr(20, "s3", 1, 1))
	if len(evs) != 1 || evs[0].Kind != EventUsageReset {
		t.Fatalf("events = %+v, want one usage_reset", evs)
	}
	e := evs[0]
	if e.T != wBase.UnixMilli() || e.Old != 64 || e.New != 1 || e.Sources != 3 || e.Window != wWeekReset.UnixMilli() {
		t.Fatalf("event = %+v", e)
	}
	if e.Mask == nil || e.Mask.From != wBase.UnixMilli() || e.Mask.To != wFiveReset.UnixMilli() {
		t.Fatalf("mask = %+v, want to the 5-hour reset", e.Mask)
	}
	if w.Hold() != 0 {
		t.Fatal("hold after the decision")
	}
	if !w.Masked(wBase.Add(time.Hour).UnixMilli()) || w.Masked(wFiveReset.UnixMilli()) || w.Masked(wBase.Add(-time.Minute).UnixMilli()) {
		t.Fatal("mask bounds")
	}
	// The stale session, and a session not seen before still at the old level, cannot raise the
	// level back nor open another candidate.
	if evs := feed(w, wr(30, "s2", 41, 64), wr(40, "s4", 40, 64), wr(60, "s1", 3, 2), wr(70, "s1", 5, 2.5)); len(evs) != 0 {
		t.Fatalf("after the reset: %+v", evs)
	}
	if *w.Level != 2.5 {
		t.Fatalf("level = %v, want 2.5", *w.Level)
	}
	// After the 5-hour window the stale session is still ignored for the rest of the week.
	late := wr(300, "s2", 40, 64)
	late.FiveReset = wFiveReset.Add(5 * time.Hour)
	if evs := feed(w, late); len(evs) != 0 || *w.Level != 2.5 {
		t.Fatalf("stale source after the window: %+v level %v", evs, *w.Level)
	}
}

// A drop to 0 seen by two sources only becomes a weekly_drop after 3 hours.
func TestWatchZeroTwoSources(t *testing.T) {
	w := NewWatch()
	warm(w)
	feed(w, wr(0, SourceWeb, 0, 0), wr(10, "s1", 1, 0), wr(30, SourceWeb, 2, 1))
	if evs := w.Tick(wBase.Add(2 * time.Hour)); len(evs) != 0 {
		t.Fatalf("decided early: %+v", evs)
	}
	evs := w.Tick(wBase.Add(3*time.Hour + time.Minute))
	if len(evs) != 1 || evs[0].Kind != EventWeeklyDrop || evs[0].Mask != nil || evs[0].Sources != 2 {
		t.Fatalf("events = %+v, want one weekly_drop without a mask", evs)
	}
	if len(w.Masks) != 0 {
		t.Fatal("a weekly drop masks nothing")
	}
}

// A weekly drop that is not to 0 needs a second agreeing reading.
func TestWatchWeeklyDrop(t *testing.T) {
	w := NewWatch()
	warm(w)
	if evs := feed(w, wr(0, "s1", 20, 31)); len(evs) != 0 {
		t.Fatalf("first reading decided: %+v", evs)
	}
	if w.Hold() != 0 {
		t.Fatal("a drop that is not to 0 holds nothing")
	}
	evs := feed(w, wr(15, "s1", 21, 31.5))
	if len(evs) != 1 || evs[0].Kind != EventWeeklyDrop || evs[0].Old != 64 || evs[0].New != 31.5 {
		t.Fatalf("events = %+v", evs)
	}
	if *w.Level != 31.5 {
		t.Fatalf("level = %v", *w.Level)
	}
}

// One stray low reading is dropped after 3 hours; the level stays.
func TestWatchStrayReading(t *testing.T) {
	w := NewWatch()
	warm(w)
	feed(w, wr(0, "s9", 0, 0))
	if evs := feed(w, wr(200, "s1", 42, 65)); len(evs) != 0 {
		t.Fatalf("events = %+v", evs)
	}
	if w.Cand != nil || *w.Level != 65 {
		t.Fatalf("cand %+v level %v", w.Cand, *w.Level)
	}
}

func TestWatchIgnores(t *testing.T) {
	w := NewWatch()
	warm(w)
	// 1 to 10 points under the level: an old value.
	feed(w, wr(1, "s5", 30, 55))
	if w.Cand != nil || *w.Level != 64 {
		t.Fatal("a small dip opened a candidate")
	}
	// An older weekly window: a session idle through a weekly reset.
	old := wr(2, "s6", 0, 0)
	old.SevenReset = wWeekReset.Add(-7 * 24 * time.Hour)
	feed(w, old)
	if w.Cand != nil {
		t.Fatal("an older window opened a candidate")
	}
	// An older 5-hour window: nothing of that reading is fresh.
	stale := wr(3, "s7", 0, 0)
	stale.FiveReset = wFiveReset.Add(-5 * time.Hour)
	feed(w, stale)
	if w.Cand != nil {
		t.Fatal("an older 5-hour window opened a candidate")
	}
	// A newer weekly window is a reset, never a drop.
	next := wr(4, "s1", 0, 0)
	next.SevenReset = wWeekReset.Add(7 * 24 * time.Hour)
	if evs := feed(w, next); len(evs) != 0 || w.Cand != nil || *w.Level != 0 {
		t.Fatalf("weekly reset: %+v level %v", evs, *w.Level)
	}
}

// claude.ai shows an emptied weekly window without a reset time; it agrees with a drop to 0.
func TestWatchWebWithoutReset(t *testing.T) {
	w := NewWatch()
	warm(w)
	feed(w, wr(0, "s1", 0, 0), wr(5, "s2", 0, 0))
	web := wr(10, SourceWeb, 0, 0)
	web.SevenReset = time.Time{}
	evs := feed(w, web)
	if len(evs) != 1 || evs[0].Kind != EventUsageReset {
		t.Fatalf("events = %+v", evs)
	}
}

// The state survives a run boundary mid-candidate.
func TestWatchJSON(t *testing.T) {
	w := NewWatch()
	warm(w)
	feed(w, wr(0, SourceWeb, 0, 0), wr(5, "s1", 0, 0))
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var back Watch
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if evs := back.Add(wr(10, "s2", 0, 0)); len(evs) != 1 || evs[0].Kind != EventUsageReset {
		t.Fatalf("after a round trip: %+v", evs)
	}
}

func TestWatchMaskBoundsAndPrune(t *testing.T) {
	t0 := wBase.UnixMilli()
	if got := maskEnd(t0, 0); got != t0+MaskMin.Milliseconds() {
		t.Fatalf("no 5-hour reset: %d", got)
	}
	if got := maskEnd(t0, t0+10*3600_000); got != t0+MaskMax.Milliseconds() {
		t.Fatalf("far reset: %d", got)
	}
	w := NewWatch()
	w.Masks = []Mask{{From: t0, To: t0 + 3600_000}, {From: t0 - 10*86_400_000, To: t0 - 10*86_400_000 + 1}}
	w.Prune(wBase)
	if len(w.Masks) != 1 || w.Masks[0].From != t0 {
		t.Fatalf("masks = %+v", w.Masks)
	}
}

// No calibration pair spans a mask.
func TestAddReadingsMasked(t *testing.T) {
	s := NewState()
	at := func(min int, five float64) Reading {
		return Reading{At: wBase.Add(time.Duration(min) * time.Minute), Source: SourceWeb, Five: pct(five), FiveReset: wFiveReset,
			Seven: pct(five / 7), SevenReset: wWeekReset}
	}
	mask := []Mask{{From: wBase.Add(30 * time.Minute).UnixMilli(), To: wBase.Add(60 * time.Minute).UnixMilli()}}
	ps := s.AddReadingsMasked([]Reading{at(0, 10), at(20, 12), at(40, 0), at(50, 13), at(70, 14), at(90, 16)}, nil, mask)
	if len(ps) != 2 {
		t.Fatalf("pairs = %+v, want 0→20 and 70→90", ps)
	}
	if ps[0].T1 != wBase.Add(20*time.Minute).UnixMilli() || ps[1].T0 != wBase.Add(70*time.Minute).UnixMilli() {
		t.Fatalf("pairs = %+v", ps)
	}
}
