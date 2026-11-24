package m5

import (
	"math"
	"sort"
	"time"
)

// Window kinds.
const (
	Win5h = "5h"
	Win7d = "7d"
)

// Window is one usage window of an account: its measured level now (percent; for the 5-hour
// window that is also 5-hour points), when it resets and the level at which Claude Code stops
// the account's sessions.
type Window struct {
	Level float64
	Reset time.Time // zero: unknown (taken as the window's length from now)
	Limit float64   // 0: 100
}

// Account is one account as the mover sees it.
type Account struct {
	Key       string
	Label     string
	Protected bool
	W5, W7    Window
	// Ratio is 5-hour points per weekly point (the picker's calibrated ρ); 0: 7.2.
	Ratio float64
	// Other is the 5-hour points per hour burnt on the account by what no session here shows
	// (the owner's claude.ai use, other machines without a lease): the picker's unexplained rate.
	Other float64
	// Stale: its readings are too old to forecast; it is never a destination.
	Stale bool
	// Excluded: not a destination (logged out, excluded by settings).
	Excluded bool
}

// Session is one running session as the mover sees it.
type Session struct {
	ID      string
	Account string
	Name    string
	// Rate is its recent burn in 5-hour points per hour (an EWMA of its token use over the
	// last 10-15 minutes times the account's meter factor f).
	Rate float64
	// Burst is extra burn its live helpers (subagents, review panels) add on top of Rate,
	// 5-hour points per hour: the last 5 minutes' rate less Rate when helpers run, else 0.
	Burst float64
	Age   time.Duration
	// Expected is how much longer it is expected to run (its --expect, else 0: the Lindy
	// rule, it runs about as long again as it has, at least MinRemaining).
	Expected time.Duration
	// Idle: at a turn boundary (its last event is Stop, an idle prompt or its own question)
	// with no subagent or tool still running. IdleFor: how long (its prompt cache is cold past
	// the cache TTL, so a move then costs nearly nothing extra).
	Idle    bool
	IdleFor time.Duration
	// Attended: the owner typed into it (its terminal or Remote Control) in the last few
	// minutes; never moved unless a limit stopped it.
	Attended bool
	// Pinned: the owner opted it out (tag no-move, wy move --pin-account); never moved.
	Pinned bool
	// Stopped: Claude Code stopped it at a limit (a limit-hit reply or StopFailure).
	Stopped bool
	// Context is its context in tokens (the move re-reads it uncached on the new account).
	Context float64
	// Model: its model ("opus", "sonnet", ...): the weight of the re-read in 5-hour points.
	Model string
	// MovedAt and MovedFrom: its last move (zero: never moved).
	MovedAt   time.Time
	MovedFrom string
	// Movable is false when the mover cannot restart it (its kind, setup.md 8.6 Mechanics);
	// it is then only planned, with the manual steps.
	Movable bool
}

// MinRemaining is the least a running session is expected to run on.
const MinRemaining = 15 * time.Minute

// remaining is how much longer s is expected to run: its Expected, else LindyFrac (default 1)
// times its age, at least MinRemaining.
func (p Params) remaining(s Session) time.Duration {
	if s.Expected > 0 {
		return s.Expected
	}
	f := p.LindyFrac
	if f <= 0 {
		f = 1
	}
	return max(time.Duration(f*float64(s.Age)), MinRemaining)
}

// rate is s's 5-hour burn the forecast uses: Rate plus BurstWeight × Burst.
func (p Params) rate(s Session) float64 {
	return math.Max(0, s.Rate) + p.BurstWeight*math.Max(0, s.Burst)
}

func ratio(a Account) float64 {
	if a.Ratio > 0 {
		return a.Ratio
	}
	return 7.2
}

func limitOf(w Window) float64 {
	if w.Limit > 0 {
		return w.Limit
	}
	return 100
}

// burn is one contribution to a window: rate per hour for hours.
type burn struct{ rate, hours float64 }

// hitHours is the first time, in hours from now, at which a window at level with the given
// burns reaches limit, or -1 when it does not within horizon hours. Already at or over the
// limit: 0.
func hitHours(level, limit float64, bs []burn, horizon float64) float64 {
	if level >= limit {
		return 0
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].hours < bs[j].hours })
	t, lv := 0.0, level
	rate := 0.0
	for _, b := range bs {
		rate += b.rate
	}
	for _, b := range append(bs, burn{0, math.Inf(1)}) {
		end := math.Min(b.hours, horizon)
		if end > t {
			if rate > 1e-12 && lv+rate*(end-t) >= limit {
				return t + (limit-lv)/rate
			}
			lv += rate * (end - t)
			t = end
		}
		if t >= horizon {
			return -1
		}
		rate -= b.rate
	}
	return -1
}

// WindowForecast is the forecast of one window.
type WindowForecast struct {
	Window   string
	Level    float64 // now
	Limit    float64
	ResetIn  time.Duration
	HitIn    time.Duration // -1: no hit before the reset
	AtReset  float64       // forecast level at the reset (not capped)
	BurnRate float64       // percent per hour now
}

// Hits reports whether the window reaches its limit before it resets.
func (f WindowForecast) Hits() bool { return f.HitIn >= 0 }

// Forecast is the forecast of one account: both windows.
type Forecast struct {
	Account string
	W5, W7  WindowForecast
}

// First is the window that hits first ("" when neither hits before its reset).
func (f Forecast) First() (WindowForecast, bool) {
	switch {
	case f.W5.Hits() && (!f.W7.Hits() || f.W5.HitIn <= f.W7.HitIn):
		return f.W5, true
	case f.W7.Hits():
		return f.W7, true
	}
	return WindowForecast{}, false
}

// Forecast forecasts account a with the sessions ss running on it, at now. margin lowers the
// limit (the hysteresis and slack of Plan).
func (p Params) Forecast(a Account, ss []Session, now time.Time, margin float64) Forecast {
	return Forecast{Account: a.Key,
		W5: p.window(Win5h, a.W5, a, ss, now, 5*time.Hour, 1, margin),
		W7: p.window(Win7d, a.W7, a, ss, now, 7*24*time.Hour, 1/ratio(a), margin)}
}

func (p Params) window(kind string, w Window, a Account, ss []Session, now time.Time, length time.Duration, scale, margin float64) WindowForecast {
	resetIn := length
	if !w.Reset.IsZero() {
		resetIn = max(0, w.Reset.Sub(now))
	}
	T := resetIn.Hours()
	lim := limitOf(w) - margin
	var bs []burn
	total := a.Other * scale
	if a.Other > 0 {
		bs = append(bs, burn{a.Other * scale, math.Inf(1)})
	}
	for _, s := range ss {
		if s.Stopped {
			continue // a stopped session burns nothing until it is moved or the window resets
		}
		r := p.rate(s) * scale
		total += r
		bs = append(bs, burn{r, p.remaining(s).Hours()})
	}
	f := WindowForecast{Window: kind, Level: w.Level, Limit: lim, ResetIn: resetIn, HitIn: -1, BurnRate: total}
	at := w.Level
	for _, b := range bs {
		at += b.rate * math.Min(b.hours, T)
	}
	f.AtReset = at
	if h := hitHours(w.Level, lim, bs, T); h >= 0 {
		f.HitIn = time.Duration(h * float64(time.Hour))
	}
	return f
}
