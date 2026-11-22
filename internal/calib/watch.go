package calib

import (
	"sort"
	"time"
)

// The usage watch (push.md 3.9): a seat's weekly level, a candidate drop under it and the
// masks of confirmed resets. The hub's calibration job feeds it every reading of the seat,
// oldest first, ahead of the calibration fold.
const (
	WatchDrop      = 10.0          // a drop is more than this many points under the level
	WatchZero      = 2.0           // "about 0": 2 % or less
	WatchSpan      = 3 * time.Hour // a candidate is decided within this time of its first reading
	WatchSources   = 3             // distinct sources that confirm a reset
	WatchReadings  = 2             // agreeing readings that confirm a weekly drop
	MaskMin        = time.Hour
	MaskMax        = 5 * time.Hour
	MaskKeep       = 8 * 24 * time.Hour
	watchDip       = 1.0 // a reading within this of a level is "at" it
	maxWatchSource = 50  // sources kept per candidate or window
)

// Notice kinds the watch finds.
const (
	EventUsageReset = "usage_reset"
	EventWeeklyDrop = "weekly_drop"
)

// Mask is a span (milliseconds, From <= at < To) whose readings calibration leaves out.
type Mask struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Candidate is an open drop under the weekly level.
type Candidate struct {
	T         int64    `json:"t"`          // the first low reading
	Level     float64  `json:"level"`      // the weekly level before it
	Zero      bool     `json:"zero"`       // that reading was about 0 on both windows
	FiveReset int64    `json:"five_reset"` // the newest five_hour resets_at seen before it
	First     float64  `json:"first"`      // its weekly value
	Max       float64  `json:"max"`        // the highest agreeing weekly value
	N         int      `json:"n"`          // agreeing readings, the first included
	Sources   []string `json:"sources"`    // distinct agreeing sources
	Stale     []string `json:"stale,omitempty"`
}

// guard ignores readings at the old level after a decided drop until the end of its 5-hour
// window: sessions that did not report a fresh value yet.
type guard struct {
	Level float64 `json:"level"`
	Until int64   `json:"until"`
}

// Watch is a seat's watch state, kept by the hub between runs (seat_watch.state).
type Watch struct {
	V         int        `json:"v"`
	Window    int64      `json:"window"`     // the current weekly window's resets_at
	Level     *float64   `json:"level"`      // its weekly level
	FiveReset int64      `json:"five_reset"` // the newest five_hour resets_at
	Cand      *Candidate `json:"cand,omitempty"`
	Stale     []string   `json:"stale,omitempty"` // sources ignored for the rest of the window
	Guard     *guard     `json:"guard,omitempty"`
	Masks     []Mask     `json:"masks"`
}

// Event is a decided candidate: a usage_reset or weekly_drop notice since T.
type Event struct {
	Kind      string  `json:"kind"`
	T         int64   `json:"t"`
	Old       float64 `json:"old"`        // the weekly level before
	New       float64 `json:"new"`        // the highest agreeing weekly value
	Window    int64   `json:"window"`     // the weekly resets_at (unchanged)
	FiveReset int64   `json:"five_reset"` // the 5-hour window's resets_at, 0 when unknown
	Sources   int     `json:"sources"`
	Mask      *Mask   `json:"mask,omitempty"`
}

// NewWatch is an empty watch.
func NewWatch() *Watch { return &Watch{V: 1, Masks: []Mask{}} }

// source names a reading's sender: web for claude.ai, else the status-line session.
func (r Reading) source() string {
	if r.Source == SourceWeb {
		return SourceWeb
	}
	if r.Session != "" {
		return r.Session
	}
	return r.Source
}

func hasSource(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func addSource(xs []string, s string) []string {
	if hasSource(xs, s) || len(xs) >= maxWatchSource {
		return xs
	}
	return append(xs, s)
}

// Add offers the next reading (not older than the previous one) and returns what it decided.
func (w *Watch) Add(r Reading) []Event {
	at := r.At.UnixMilli()
	evs := w.Tick(r.At)
	tol := ResetTolerance.Milliseconds()
	if !r.FiveReset.IsZero() {
		fr := r.FiveReset.UnixMilli()
		if w.FiveReset != 0 && w.FiveReset-fr > tol {
			return evs // a session that sat idle through a 5-hour reset: nothing of it is fresh
		}
		if fr > w.FiveReset {
			w.FiveReset = fr
		}
	}
	if !validPct(r.Seven) {
		return evs
	}
	pct := *r.Seven
	if r.SevenReset.IsZero() {
		// claude.ai shows an emptied weekly window without a reset time.
		if c := w.Cand; c != nil && pct <= WatchZero && (w.Window == 0 || at < w.Window) {
			evs = append(evs, w.agree(r, pct)...)
		}
		return evs
	}
	reset := r.SevenReset.UnixMilli()
	switch {
	case w.Window != 0 && w.Window-reset > tol:
		return evs // a session that sat idle through a weekly reset
	case w.Window == 0 || reset-w.Window > tol:
		// A new weekly window (or the first reading): a reset, never a drop.
		w.Window, w.Cand, w.Stale, w.Guard = reset, nil, nil, nil
		v := pct
		w.Level = &v
		return evs
	}
	src := r.source()
	if hasSource(w.Stale, src) {
		return evs
	}
	if g := w.Guard; g != nil {
		if at >= g.Until {
			w.Guard = nil
		} else if pct >= g.Level-watchDip {
			return evs
		}
	}
	level := *w.Level
	if c := w.Cand; c != nil {
		switch {
		case pct < c.Level-WatchDrop:
			return append(evs, w.agree(r, pct)...)
		case pct >= c.Level-watchDip && src != SourceWeb:
			c.Stale = addSource(c.Stale, src) // a session still showing the old value
		}
		if pct > level {
			w.Level = &pct
		}
		return evs
	}
	switch {
	case pct > level:
		w.Level = &pct
	case pct < level-WatchDrop:
		zero := pct <= WatchZero && validPct(r.Five) && *r.Five <= WatchZero
		w.Cand = &Candidate{T: at, Level: level, Zero: zero, FiveReset: w.FiveReset, First: pct, Max: pct, N: 1,
			Sources: []string{src}}
	}
	return evs
}

// agree counts an agreeing reading and decides the candidate when it can be.
func (w *Watch) agree(r Reading, pct float64) []Event {
	c := w.Cand
	c.N++
	c.Sources = addSource(c.Sources, r.source())
	if pct > c.Max {
		c.Max = pct
	}
	switch {
	case c.Zero && len(c.Sources) >= WatchSources:
		return []Event{w.decide(EventUsageReset)}
	case !c.Zero && c.N >= WatchReadings:
		return []Event{w.decide(EventWeeklyDrop)}
	}
	return nil
}

// Tick decides a candidate whose time ran out by now (push.md 3.9).
func (w *Watch) Tick(now time.Time) []Event {
	c := w.Cand
	if c == nil || now.UnixMilli() <= c.T+WatchSpan.Milliseconds() {
		return nil
	}
	if c.Zero && c.N >= WatchReadings {
		return []Event{w.decide(EventWeeklyDrop)}
	}
	w.Cand = nil // one reading only: no notice, the level stays
	return nil
}

// maskEnd is the end of the 5-hour window a drop at t happened in, clamped.
func maskEnd(t, fiveReset int64) int64 {
	lo, hi := t+MaskMin.Milliseconds(), t+MaskMax.Milliseconds()
	if fiveReset < lo {
		return lo
	}
	if fiveReset > hi {
		return hi
	}
	return fiveReset
}

func (w *Watch) decide(kind string) Event {
	c := w.Cand
	w.Cand = nil
	end := maskEnd(c.T, c.FiveReset)
	ev := Event{Kind: kind, T: c.T, Old: c.Level, New: c.Max, Window: w.Window, FiveReset: c.FiveReset,
		Sources: len(c.Sources)}
	v := c.Max
	w.Level = &v
	for _, s := range c.Stale {
		w.Stale = addSource(w.Stale, s)
	}
	w.Guard = &guard{Level: c.Level, Until: end}
	if kind == EventUsageReset {
		m := Mask{From: c.T, To: end}
		w.Masks = append(w.Masks, m)
		ev.Mask = &m
	}
	return ev
}

// Hold is the time before which calibration may fold readings while a candidate to about 0
// waits for its decision (0: no hold).
func (w *Watch) Hold() int64 {
	if w.Cand != nil && w.Cand.Zero {
		return w.Cand.T
	}
	return 0
}

// Masked reports whether a reading at (ms) is inside a mask.
func (w *Watch) Masked(at int64) bool { return masked(w.Masks, at) }

func masked(ms []Mask, at int64) bool {
	for _, m := range ms {
		if at >= m.From && at < m.To {
			return true
		}
	}
	return false
}

// Prune drops masks older than MaskKeep.
func (w *Watch) Prune(now time.Time) {
	cut := now.Add(-MaskKeep).UnixMilli()
	keep := w.Masks[:0]
	for _, m := range w.Masks {
		if m.To >= cut {
			keep = append(keep, m)
		}
	}
	w.Masks = keep
	sort.Slice(w.Masks, func(i, j int) bool { return w.Masks[i].From < w.Masks[j].From })
}
