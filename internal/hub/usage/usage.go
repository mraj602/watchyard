package usage

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sample is one limit reading taken from a record.
type Sample struct {
	Key          string
	UsedPct      *float64
	ResetsAt     time.Time // zero = unknown
	UsedDollars  *float64
	LimitDollars *float64
	Inactive     bool // model-scoped limit with is_active = false
}

// labels are the friendly names of the known window keys; other keys show as their
// raw key.
var labels = map[string]string{
	"five_hour":            "5-hour",
	"seven_day":            "7-day",
	"seven_day_opus":       "7-day · Opus",
	"seven_day_sonnet":     "7-day · Sonnet",
	"seven_day_oauth_apps": "7-day · apps",
	"seven_day_cowork":     "7-day · Cowork",
	"iguana_necktie":       "Cloud session credits",
}

var order = []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_oauth_apps", "seven_day_cowork", "iguana_necktie"}

// Label is a window key's display name: the known names, "7-day · Fable" for
// model-scoped weekly keys, else the key itself.
func Label(key string) string {
	if l, ok := labels[key]; ok {
		return l
	}
	if g, model, ok := strings.Cut(key, "_"); ok && model != "" && !strings.Contains(model, "_") {
		name := strings.ToUpper(model[:1]) + model[1:]
		switch g {
		case "weekly":
			return "7-day · " + name
		case "session":
			return "5-hour · " + name
		}
	}
	return key
}

// Rank orders keys: known ones first in a fixed order, then the rest.
func Rank(key string) int {
	for i, k := range order {
		if k == key {
			return i
		}
	}
	return 99
}

// Length is a window's length: 5 h for five_hour and session_* keys, 7 days for
// seven_day* and weekly_*, 0 (unknown) for anything else (monthly credits, …).
func Length(key string) time.Duration {
	switch {
	case key == "five_hour" || strings.HasPrefix(key, "session_"):
		return 5 * time.Hour
	case strings.HasPrefix(key, "seven_day") || strings.HasPrefix(key, "weekly_"):
		return 7 * 24 * time.Hour
	}
	return 0
}

// ParseTime accepts an RFC 3339 string (any precision or offset), or a number of epoch
// seconds or milliseconds (record.md 4.2: the status line's resets_at is uncertain).
func ParseTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC()
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return epoch(f)
		}
		return time.Time{}
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return epoch(f)
	}
	return time.Time{}
}

func epoch(f float64) time.Time {
	if f <= 0 {
		return time.Time{}
	}
	if f > 1e12 { // milliseconds
		return time.UnixMilli(int64(f)).UTC()
	}
	return time.UnixMilli(int64(math.Round(f * 1000))).UTC()
}

func num(raw json.RawMessage) *float64 {
	if len(raw) == 0 {
		return nil
	}
	var f *float64
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f
}

// WindowsOf reads the windows of a raw claude.ai usage response with these
// rules: every top-level key whose value is an object with a non-null
// `utilization` is a window (iguana_necktie and future keys included); model-scoped limits
// come from `limits[]` items with scope.model.display_name, keyed
// lower(group || kind) + "_" + lower(model). Values are kept unrounded.
//
// The newer `limits[]` shape is also read for the unscoped kinds in limitKeys: such an item
// is the five_hour / seven_day window when the top-level field is null or missing. When both
// carry the window the top-level field is kept (its utilization is unrounded; the two agree
// in every reading seen). `is_active` is not a "started" flag (a weekly_all item with 24 %
// used reads is_active false), so it only sets Inactive. A window with used 0 and no
// resets_at is idle: claude.ai's "not started yet" (Sample.Idle, record.md 4.3).
func WindowsOf(usage json.RawMessage) []Sample {
	var top map[string]json.RawMessage
	if json.Unmarshal(usage, &top) != nil {
		return nil
	}
	var out []Sample
	for key, v := range top {
		var w map[string]json.RawMessage
		if len(v) == 0 || v[0] != '{' || json.Unmarshal(v, &w) != nil {
			continue
		}
		u := num(w["utilization"])
		if u == nil {
			continue
		}
		out = append(out, Sample{Key: key, UsedPct: u, ResetsAt: ParseTime(w["resets_at"]),
			UsedDollars: num(w["used_dollars"]), LimitDollars: num(w["limit_dollars"])})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if Rank(out[i].Key) != Rank(out[j].Key) {
			return Rank(out[i].Key) < Rank(out[j].Key)
		}
		return out[i].Key < out[j].Key
	})
	var limits []struct {
		Kind     string          `json:"kind"`
		Group    string          `json:"group"`
		Percent  *float64        `json:"percent"`
		ResetsAt json.RawMessage `json:"resets_at"`
		IsActive *bool           `json:"is_active"`
		Scope    *struct {
			Model *struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
			Surface json.RawMessage `json:"surface"`
		} `json:"scope"`
	}
	if raw, ok := top["limits"]; ok && json.Unmarshal(raw, &limits) == nil {
		have := map[string]bool{}
		for _, s := range out {
			have[s.Key] = true
		}
		filled := false
		for _, l := range limits {
			if l.Scope == nil || l.Scope.Model == nil || l.Scope.Model.DisplayName == "" {
				key, ok := limitKeys[l.Kind]
				scoped := l.Scope != nil && len(l.Scope.Surface) > 0 && string(l.Scope.Surface) != "null"
				if !ok || scoped || have[key] || l.Percent == nil {
					continue
				}
				pct := *l.Percent
				have[key], filled = true, true
				out = append(out, Sample{Key: key, UsedPct: &pct, ResetsAt: ParseTime(l.ResetsAt),
					Inactive: l.IsActive != nil && !*l.IsActive})
				continue
			}
			g := l.Group
			if g == "" {
				g = l.Kind
			}
			pct := 0.0
			if l.Percent != nil {
				pct = *l.Percent
			}
			out = append(out, Sample{Key: strings.ToLower(g + "_" + l.Scope.Model.DisplayName), UsedPct: &pct,
				ResetsAt: ParseTime(l.ResetsAt), Inactive: l.IsActive != nil && !*l.IsActive})
		}
		if filled { // five_hour / seven_day first, as from the top-level fields
			sort.SliceStable(out, func(i, j int) bool { return Rank(out[i].Key) < Rank(out[j].Key) })
		}
	}
	return out
}

// limitKeys maps an unscoped `limits[]` item's kind to the window it describes (record.md
// 4.3): "session" is the 5-hour window, "weekly_all" the all-models 7-day one.
var limitKeys = map[string]string{"session": "five_hour", "weekly_all": "seven_day"}

// Idle is a reading of a window that has not started: claude.ai reports 0 % used and no
// resets_at until the account is next used (record.md 4.3).
func (s Sample) Idle() bool { return s.UsedPct != nil && *s.UsedPct == 0 && s.ResetsAt.IsZero() }

// StatusLineWindows reads rate_limits.five_hour / seven_day from a status-line payload.
func StatusLineWindows(payload json.RawMessage) []Sample {
	var p struct {
		RateLimits map[string]struct {
			UsedPercentage json.RawMessage `json:"used_percentage"`
			ResetsAt       json.RawMessage `json:"resets_at"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return nil
	}
	var out []Sample
	for _, key := range []string{"five_hour", "seven_day"} {
		w, ok := p.RateLimits[key]
		if !ok {
			continue
		}
		u := num(w.UsedPercentage)
		reset := ParseTime(w.ResetsAt)
		if u == nil && reset.IsZero() {
			continue
		}
		out = append(out, Sample{Key: key, UsedPct: u, ResetsAt: reset})
	}
	return out
}

var maxTier = regexp.MustCompile(`max_20x`)

// PlanOf normalises an org's plan (schema.sql org.plan) from claude.ai org fields or
// Claude Code's rate tier / subscription type: free, pro, max_5x, max_20x, team,
// enterprise or unknown.
func PlanOf(capabilities []string, rateTier, ravenType, subscription string) string {
	has := func(c string) bool {
		for _, x := range capabilities {
			if x == c {
				return true
			}
		}
		return false
	}
	tier := strings.ToLower(rateTier)
	sub := strings.ToLower(subscription)
	switch {
	case has("raven") || ravenType != "" || sub == "team" || strings.Contains(tier, "team"):
		return "team"
	case has("enterprise") || sub == "enterprise" || strings.Contains(tier, "enterprise"):
		return "enterprise"
	case maxTier.MatchString(tier):
		return "max_20x"
	case strings.Contains(tier, "max") || has("claude_max") || sub == "max":
		return "max_5x"
	case has("claude_pro") || sub == "pro" || strings.Contains(tier, "pro"):
		return "pro"
	case has("chat") || sub == "free":
		return "free"
	}
	return "unknown"
}

// Pace is the projection of a window (api.yaml Window.pace).
type Pace struct {
	ProjectedPct float64    `json:"projected_pct"`
	FullAt       *time.Time `json:"-"`
	Band         string     `json:"band"`
	// Basis says which rate the projection used: BasisRecent (the slope of the last hour's
	// readings, RatePerHour points an hour) or BasisAverage (the whole window so far).
	Basis       string  `json:"basis"`
	RatePerHour float64 `json:"rate_per_hour"`
}

// Pace bases.
const (
	BasisRecent  = "recent"
	BasisAverage = "average"
)

// Reading is one used-percent reading of a window.
type Reading struct {
	At   time.Time
	Used float64
}

// The recent rate needs RecentMin readings of the last RecentSpan that stretch over at least
// RecentMinSpan; with fewer the window's average rate is used.
const (
	RecentSpan    = time.Hour
	RecentMin     = 3
	RecentMinSpan = 20 * time.Minute
)

// RecentRate is the slope, in percent points an hour, of a straight line through the readings
// of the last RecentSpan (never below 0: a window's use only grows). ok is false when there
// are too few readings, or they cover too little time, to say.
func RecentRate(rs []Reading, now time.Time) (perHour float64, ok bool) {
	var pts []Reading
	for _, r := range rs {
		if !r.At.After(now) && now.Sub(r.At) <= RecentSpan {
			pts = append(pts, r)
		}
	}
	if len(pts) < RecentMin {
		return 0, false
	}
	first, last := pts[0].At, pts[0].At
	for _, r := range pts {
		if r.At.Before(first) {
			first = r.At
		}
		if r.At.After(last) {
			last = r.At
		}
	}
	if last.Sub(first) < RecentMinSpan {
		return 0, false
	}
	var sx, sy, sxx, sxy float64
	n := float64(len(pts))
	for _, r := range pts {
		x := r.At.Sub(first).Hours()
		sx, sy, sxx, sxy = sx+x, sy+r.Used, sxx+x*x, sxy+x*r.Used
	}
	den := n*sxx - sx*sx
	if den <= 0 {
		return 0, false
	}
	return math.Max(0, (n*sxy-sx*sy)/den), true
}

// PaceOf projects used% at reset. With enough recent readings (RecentRate) it adds the recent
// rate over the time left to the use now, so a week that was quiet and is burning now, or the
// other way round, reads true; otherwise it takes the whole window's average rate (used x
// total / elapsed). It returns nil when the window is too young (under 5 % elapsed) to say
// anything, already reset, or its start is unknown.
//
// Bands: comfortable (projected <= 70 %), on_track (<= 100 %), fast (<= 130 %),
// runaway (> 130 %).
func PaceOf(used float64, started, resets, now time.Time, recent []Reading) *Pace {
	if started.IsZero() || resets.IsZero() || !resets.After(started) || !now.Before(resets) {
		return nil
	}
	total := resets.Sub(started).Seconds()
	elapsed := now.Sub(started).Seconds()
	if elapsed < total*0.05 {
		return nil
	}
	proj := used * total / elapsed
	rate := used / (elapsed / 3600) // average points an hour
	p := &Pace{Basis: BasisAverage}
	if r, ok := RecentRate(recent, now); ok {
		rate, p.Basis = r, BasisRecent
		proj = used + r*resets.Sub(now).Hours()
	}
	p.ProjectedPct = math.Round(proj*10) / 10
	p.RatePerHour = math.Round(rate*10) / 10
	switch {
	case proj <= 70:
		p.Band = "comfortable"
	case proj <= 100:
		p.Band = "on_track"
	case proj <= 130:
		p.Band = "fast"
	default:
		p.Band = "runaway"
	}
	if used > 0 && proj > 100 && rate > 0 {
		full := now.Add(time.Duration((100 - used) / rate * float64(time.Hour)))
		if p.Basis == BasisAverage {
			full = started.Add(time.Duration(elapsed * 100 / used * float64(time.Second)))
		}
		if full.Before(resets) {
			p.FullAt = &full
		}
	}
	return p
}
