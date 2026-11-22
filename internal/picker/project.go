package picker

import (
	"math"
	"sort"
	"time"
)

// Rate sources, as `--json` names them.
const (
	RateHistory = "history" // exponentially weighted slope of recent readings
	RateWindow  = "window"  // used % over the time since the window started
	RateGlobal  = "global"  // mean history rate of the other accounts
	RateNone    = "none"
)

const (
	// lookback5h and lookback7d are how far back the burn rate looks; the weights halve
	// about every lookback/2·ln2.
	lookback5h = 2 * time.Hour
	lookback7d = 24 * time.Hour
	// minCoverage is the least span of history a history rate needs.
	minCoverage = 20 * time.Minute
	// minElapsed is the least age of a window for the window-average rate.
	minElapsed = 30 * time.Minute
)

func lookbackFor(kind string) time.Duration {
	if short(kind) {
		return lookback5h
	}
	return lookback7d
}

// historyRate is the burn rate in points per hour from the readings of the current
// window: the slope of each step between readings, weighted by exp(-age/τ) with τ half
// the lookback, over the lookback (or since the window started, if later). The time
// since the newest reading counts as a flat step: the status line writes a reading
// whenever the value changes, so no reading means no change. Drops (a reset, two sources
// disagreeing) count as flat. ok is false with less than minCoverage of history.
func historyRate(samples []Sample, now, windowStart time.Time, lookback time.Duration) (float64, bool) {
	pts := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.At.After(now) || (!windowStart.IsZero() && s.At.Before(windowStart)) {
			continue
		}
		pts = append(pts, s)
	}
	if len(pts) == 0 {
		return 0, false
	}
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].At.Before(pts[j].At) })
	from := now.Add(-lookback)
	if !windowStart.IsZero() && windowStart.After(from) {
		from = windowStart
	}
	var seq []Sample
	for i, p := range pts {
		if !p.At.After(from) {
			// the last reading at or before the start anchors the sequence there
			if i == len(pts)-1 || pts[i+1].At.After(from) {
				seq = append(seq, Sample{At: from, Pct: p.Pct})
			}
			continue
		}
		seq = append(seq, p)
	}
	if len(seq) == 0 {
		return 0, false
	}
	seq = append(seq, Sample{At: now, Pct: seq[len(seq)-1].Pct})
	if now.Sub(seq[0].At) < minCoverage {
		return 0, false
	}
	tau := lookback.Hours() / 2
	var num, den float64
	for i := 1; i < len(seq); i++ {
		a, b := seq[i-1], seq[i]
		dt := b.At.Sub(a.At).Hours()
		if dt <= 0 {
			continue
		}
		du := math.Max(0, b.Pct-a.Pct)
		mid := now.Sub(a.At.Add(b.At.Sub(a.At) / 2)).Hours()
		w := math.Exp(-mid / tau)
		num += w * du
		den += w * dt
	}
	if den <= 0 {
		return 0, false
	}
	return num / den, true
}

// windowRate is the average burn since the window started: used / elapsed.
func windowRate(w Window, length time.Duration) (float64, bool) {
	if w.Idle || w.ResetsAt.IsZero() || length == 0 {
		return 0, false
	}
	start := w.ResetsAt.Add(-length)
	el := w.UpdatedAt.Sub(start)
	if el < minElapsed {
		return 0, false
	}
	return w.UsedPct / el.Hours(), true
}

// Projection is one window of one account, projected.
type Projection struct {
	Key  string  `json:"key"`
	Kind string  `json:"kind"`
	Cap  float64 `json:"cap_pct"`
	// Used is the newest reading (0 for a window that has reset or not started).
	Used      float64   `json:"used_pct"`
	ReadingAt time.Time `json:"reading_at"`
	AgeMin    float64   `json:"age_min"`
	Stale     bool      `json:"stale"`
	// Now is the estimate now: the reading plus the burn since it was taken.
	Now float64 `json:"now_pct"`
	// Uncertainty is added on top of Now for a stale reading.
	Uncertainty float64 `json:"uncertainty_pct"`
	// AtSessionEnd is the high estimate when the new session ends (or at the reset, if
	// sooner): what eligibility compares with the cap.
	AtSessionEnd float64 `json:"at_session_end_pct"`
	// AtReset is the middle estimate at the reset, with this session and the pending ones.
	AtReset float64 `json:"at_reset_pct"`
	// ResetsAt is when the window resets; zero when it has not started (it starts on use).
	ResetsAt     time.Time `json:"resets_at"`
	HoursToReset float64   `json:"hours_to_reset"`
	Rate         float64   `json:"rate_pct_per_hour"`
	RateFrom     string    `json:"rate_from"`
	// Unused is the cap minus AtReset, at least 0: room that would be lost at the reset.
	Unused float64 `json:"unused_at_reset_pct"`
	Over   bool    `json:"over_cap"`
	Source string  `json:"source"`
}

// projectInput is what one window's projection needs besides the window.
type projectInput struct {
	now        time.Time
	kind       string
	length     time.Duration
	cap        float64
	perSession float64
	session    time.Duration
	stale      time.Duration
	rate       float64
	rateFrom   string
	live       int
	pending    int
}

// project applies the formulas of setup.md 8.5 to one window.
func project(w Window, in projectInput) Projection {
	p := Projection{Key: w.Key, Kind: in.kind, Cap: in.cap, Source: w.Source}
	at := w.UpdatedAt
	used := w.UsedPct
	hours := in.length.Hours()
	switch {
	case w.Idle || (w.ResetsAt.IsZero() && used == 0):
		used = 0 // not started: it starts, with a full length, on first use
	case !w.ResetsAt.IsZero() && !in.now.Before(w.ResetsAt):
		// Reset since the reading: nothing is known to be used in the new window; the
		// time since the reset is what can have been missed.
		used, at = 0, w.ResetsAt
	case !w.ResetsAt.IsZero():
		p.ResetsAt = w.ResetsAt
		hours = w.ResetsAt.Sub(in.now).Hours()
	}
	if at.IsZero() || at.After(in.now) {
		at = in.now
	}
	p.Used, p.ReadingAt = used, at
	age := in.now.Sub(at)
	p.AgeMin = round1(age.Minutes())
	sessH := in.session.Hours()
	if sessH <= 0 {
		sessH = 2
	}
	rate := math.Max(in.rate, float64(in.live)*in.perSession/sessH)
	p.Rate, p.RateFrom = round2(rate), in.rateFrom
	if float64(in.live)*in.perSession/sessH > in.rate && in.live > 0 {
		p.RateFrom = "live_sessions"
	}
	now := math.Min(100, used+rate*age.Hours())
	unc := 0.0
	if w.Unknown { // no reading at all: as uncertain as a stale one, one session's worth
		p.Stale = true
		unc = in.perSession
	} else if age > in.stale {
		p.Stale = true
		unc = math.Max(rate*age.Hours(), in.perSession*math.Min(1, age.Hours()/sessH))
		unc = math.Min(unc, 100-now)
	}
	share := math.Min(1, hours/sessH)
	sessions := float64(1 + in.pending)
	p.Now, p.Uncertainty = round1(now), round1(unc)
	p.AtSessionEnd = round1(now + unc + rate*math.Min(hours, sessH) + sessions*in.perSession*share)
	p.AtReset = round1(now + rate*hours + sessions*in.perSession)
	p.HoursToReset = round2(hours)
	p.Unused = round1(math.Max(0, in.cap-(now+rate*hours+sessions*in.perSession)))
	p.Over = now+unc+rate*math.Min(hours, sessH)+sessions*in.perSession*share > in.cap
	return p
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round2(x float64) float64 { return math.Round(x*100) / 100 }
