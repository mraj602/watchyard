package calib

import (
	"math"
	"sort"
	"time"
)

// Reading rules (setup.md 8.5, Calibration).
const (
	// ResetTolerance is how far two resets_at of one window may be apart (store.ResetTolerance).
	ResetTolerance = 5 * time.Minute
	// dropPoints: a reading more than this under the last used one of its window is held.
	dropPoints = 1.0
	// SourceWeb is the source of claude.ai readings; every other source is a status line,
	// which only a running Claude Code session writes.
	SourceWeb = "web"
)

var readingMin = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// Reading is one reading of a seat: its five_hour and seven_day values (nil: none) with
// their resets_at (zero: none), from one source at one time.
type Reading struct {
	At         time.Time
	Source     string
	Session    string // the status-line session, "" for claude.ai
	Five       *float64
	FiveReset  time.Time
	Seven      *float64
	SevenReset time.Time
}

// Use is the weighted token units of one API response of the seat.
type Use struct {
	At    time.Time
	Units float64
}

// Meter sums a seat's token units between two times.
type Meter struct {
	at  []int64
	cum []float64 // cum[i] = units of the first i uses
}

// NewMeter makes a meter over us (any order).
func NewMeter(us []Use) *Meter {
	s := append([]Use(nil), us...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].At.Before(s[j].At) })
	m := &Meter{at: make([]int64, len(s)), cum: make([]float64, len(s)+1)}
	for i, u := range s {
		m.at[i] = u.At.UnixMilli()
		m.cum[i+1] = m.cum[i] + math.Max(0, u.Units)
	}
	return m
}

// Between is the units with from < at <= to (milliseconds). A nil meter has none.
func (m *Meter) Between(from, to int64) float64 {
	if m == nil || to <= from {
		return 0
	}
	i := sort.Search(len(m.at), func(k int) bool { return m.at[k] > from })
	j := sort.Search(len(m.at), func(k int) bool { return m.at[k] > to })
	return m.cum[j] - m.cum[i]
}

// point is a used reading of one window.
type point struct {
	At    int64   `json:"at"`
	Pct   float64 `json:"pct"`
	Reset int64   `json:"reset"`
	Seq   int64   `json:"seq"`
	Web   bool    `json:"web,omitempty"`
}

// track is the used readings of one window: the newest resets_at used, the last used reading
// and a held one (more than a point under it, waiting for confirmation).
type track struct {
	MaxReset int64  `json:"max_reset"`
	Last     *point `json:"last,omitempty"`
	Held     *point `json:"held,omitempty"`
}

func sameReset(a, b int64) bool {
	d := a - b
	return d <= ResetTolerance.Milliseconds() && d >= -ResetTolerance.Milliseconds()
}

// step offers one reading to the window: whether it is used, and whether it confirmed a drop.
func (t *track) step(p point) (used, drop bool) {
	if t.MaxReset != 0 && t.MaxReset-p.Reset > ResetTolerance.Milliseconds() {
		return false, false // a session that sat idle through a reset
	}
	if t.Last != nil && sameReset(t.Last.Reset, p.Reset) && p.Pct < t.Last.Pct-dropPoints {
		if t.Held == nil {
			h := p
			t.Held = &h
			return false, false
		}
		drop = true // the second reading in a row under the last used one: a real drop
	}
	t.Held = nil
	if p.Reset > t.MaxReset {
		t.MaxReset = p.Reset
	}
	t.Last = &p
	return true, drop
}

// Pair is two consecutive used five_hour readings of one 5-hour window (setup.md 8.5):
// times in milliseconds, the 5-hour rise, the weekly rise when both readings had a used
// weekly reading of one weekly window, the seat's token units between them, and whether it is
// clean (claude.ai readings only, no tokens, no status line between: unexplained use only).
type Pair struct {
	T0     int64    `json:"t0"`
	T1     int64    `json:"t1"`
	D5     float64  `json:"d5"`
	D7     *float64 `json:"d7,omitempty"`
	U      float64  `json:"u"`
	Clean  bool     `json:"clean,omitempty"`
	Window int64    `json:"w"` // the 5-hour window's resets_at
}

// Hours is the time between the pair's readings.
func (p Pair) Hours() float64 { return float64(p.T1-p.T0) / 3.6e6 }

// SevenPair is two consecutive used seven_day readings of one weekly window.
type SevenPair struct {
	T0, T1 int64
	D7     float64
	U      float64
}

// Step is what one reading added: a 5-hour pair, or a 5-hour reset or confirmed drop since
// the previous used five_hour reading; the same for seven_day.
type Step struct {
	Five       *Pair
	FiveReset  bool
	Seven      *SevenPair
	SevenReset bool
}

// Tracker turns a seat's readings, oldest first, into pairs. Its state is kept between runs
// by the hub (JSON).
type Tracker struct {
	Five       track `json:"five"`
	Seven      track `json:"seven"`
	Seq        int64 `json:"seq"`
	LastNonWeb int64 `json:"last_non_web"` // newest status-line reading, used or not
}

func validPct(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 100
}

// Add offers the next reading (not older than the previous one) with the seat's meter.
func (t *Tracker) Add(r Reading, m *Meter) Step {
	var st Step
	if r.At.Before(readingMin) {
		return st
	}
	t.Seq++
	at := r.At.UnixMilli()
	web := r.Source == SourceWeb
	nonWebBefore := t.LastNonWeb
	if !web && at > t.LastNonWeb {
		t.LastNonWeb = at
	}
	var prev5, prev7 *point
	if t.Five.Last != nil {
		c := *t.Five.Last
		prev5 = &c
	}
	if t.Seven.Last != nil {
		c := *t.Seven.Last
		prev7 = &c
	}
	var used5, drop5, used7, drop7 bool
	if validPct(r.Five) && !r.FiveReset.IsZero() {
		used5, drop5 = t.Five.step(point{At: at, Pct: *r.Five, Reset: r.FiveReset.UnixMilli(), Seq: t.Seq, Web: web})
	}
	if validPct(r.Seven) && !r.SevenReset.IsZero() {
		used7, drop7 = t.Seven.step(point{At: at, Pct: *r.Seven, Reset: r.SevenReset.UnixMilli(), Seq: t.Seq, Web: web})
	}
	if used5 && prev5 != nil {
		cur := t.Five.Last
		if drop5 || !sameReset(prev5.Reset, cur.Reset) {
			st.FiveReset = true
		} else {
			p := Pair{T0: prev5.At, T1: at, D5: cur.Pct - prev5.Pct, U: m.Between(prev5.At, at), Window: cur.Reset}
			if used7 && !drop7 && prev7 != nil && prev7.Seq == prev5.Seq && sameReset(prev7.Reset, t.Seven.Last.Reset) {
				d := t.Seven.Last.Pct - prev7.Pct
				p.D7 = &d
			}
			p.Clean = prev5.Web && web && p.U == 0 && nonWebBefore <= prev5.At
			st.Five = &p
		}
	}
	if used7 && prev7 != nil {
		cur := t.Seven.Last
		if drop7 || !sameReset(prev7.Reset, cur.Reset) {
			st.SevenReset = true
		} else {
			st.Seven = &SevenPair{T0: prev7.At, T1: at, D7: cur.Pct - prev7.Pct, U: m.Between(prev7.At, at)}
		}
	}
	return st
}

// Forget drops the last used and held readings of both windows (their newest resets_at is
// kept): the next reading starts no pair with them.
func (t *Tracker) Forget() {
	t.Five.Last, t.Five.Held = nil, nil
	t.Seven.Last, t.Seven.Held = nil, nil
}

// SortReadings orders readings oldest first (ties: claude.ai readings first, then by source),
// the order Tracker.Add needs.
func SortReadings(rs []Reading) {
	sort.SliceStable(rs, func(i, j int) bool {
		if !rs[i].At.Equal(rs[j].At) {
			return rs[i].At.Before(rs[j].At)
		}
		return rs[i].Source > rs[j].Source // "web" before "statusline" and "local"
	})
}
