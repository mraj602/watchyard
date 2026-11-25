package picker

import (
	"fmt"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/config"
)

// Settings are the [next] table resolved: globals and per-account settings.
type Settings struct {
	Strategy     string
	Model        string
	Session5h    float64       // percentage points of a 5-hour window one new session uses
	Session7d    float64       // percentage points of a weekly window one new session uses
	Session      time.Duration // how long a session is expected to burn
	Stale        time.Duration // a reading older than this is uncertain
	ReserveTTL   time.Duration // how long a pick counts as a pending session
	StickyMargin float64       // sticky: points under each cap that end the stickiness
	Accounts     map[string]config.NextAccount
	// Proposed is config slots.modes: the mode this machine proposed for each folder's
	// login, used until the hub has one (setup.md 8.5, Kind).
	Proposed map[string]string
}

// Where a candidate's mode came from (setup.md 8.5, Kind; record.md 4.11 mode_from).
const (
	ModeFromLocal    = "local"    // this machine's next.accounts.<key>.mode
	ModeFromHub      = "hub"      // the seat's hub setting (UsageSeat.mode, Seat.mode)
	ModeFromProposed = "proposed" // this machine's slots.modes.<folder>
	ModeFromNone     = "none"     // not chosen: aggressive (mode normal)
)

// ModeWords is the one wording of the kinds (setup.md 8.5, Kind), used by --explain and the
// setup questions; the board's components/pick-mode.js and the phone's
// lib/format/pickMode.ts say the same.
var ModeWords = struct {
	Protected, Aggressive, NotChosen string
}{
	Protected:  "Kept for you. Used last: new sessions go here only when no aggressive account can take them, or for room that would expire unused anyway.",
	Aggressive: "Use it up. Sessions go here until 98% of the week or 5 hours.",
	NotChosen:  "Not chosen yet; treated as aggressive.",
}

// ValidHubMode reports whether m is a hub or proposed mode: protected or aggressive.
func ValidHubMode(m string) bool {
	return m == config.NextModeProtected || m == config.NextModeAggressive
}

// SettingsFrom resolves the typed [next] table; zero or invalid values take the defaults.
func SettingsFrom(n config.Next) Settings {
	d := config.Defaults().Next
	pick := func(v, def float64) float64 {
		if v > 0 {
			return v
		}
		return def
	}
	s := Settings{
		Strategy:     n.Strategy,
		Model:        n.Model,
		Session5h:    pick(n.Session5h, d.Session5h),
		Session7d:    pick(n.Session7d, d.Session7d),
		Session:      minutes(pick(n.SessionMin, d.SessionMin)),
		Stale:        minutes(pick(n.StaleMin, d.StaleMin)),
		ReserveTTL:   minutes(pick(n.ReserveMin, d.ReserveMin)),
		StickyMargin: pick(n.StickyMargin, d.StickyMargin),
		Accounts:     n.Accounts,
	}
	if !config.ValidNextStrategy(s.Strategy) {
		s.Strategy = config.NextStrategyLanes
	}
	if s.Accounts == nil {
		s.Accounts = map[string]config.NextAccount{}
	}
	return s
}

func minutes(m float64) time.Duration { return time.Duration(m * float64(time.Minute)) }

// Preset is what a mode means: the caps per window kind and how much the mode is preferred.
type Preset struct {
	Cap5h, Cap7d, CapModel float64
	Pref                   float64
}

// Presets are the modes of next.accounts.<key>.mode.
var Presets = map[string]Preset{
	config.NextModeConservative: {Cap5h: 60, Cap7d: 70, CapModel: 70, Pref: 0.6},
	config.NextModeProtected:    {Cap5h: 60, Cap7d: 70, CapModel: 70, Pref: 0.6},
	config.NextModeNormal:       {Cap5h: 90, Cap7d: 90, CapModel: 90, Pref: 1},
	config.NextModeAggressive:   {Cap5h: 98, Cap7d: 98, CapModel: 98, Pref: 1.4},
}

// Policy is the settings that apply to one candidate.
type Policy struct {
	Key  string // the next.accounts key that matched ("" = none)
	Mode string
	// ModeFrom is where Mode came from (ModeFrom*); HubMode is the seat's hub setting ("" none),
	// named by --explain when a local mode differs from it.
	ModeFrom    string
	HubMode     string
	Cap5h       float64
	Cap7d       float64
	CapModel    float64
	Reserve     float64
	Priority    float64 // 0 = unset
	Weight      float64
	Pref        float64
	Exclude     bool
	Hours, Days string
	// SetCap5h and SetCap7d are the explicit cap_5h and cap_7d (0 = not set): the lanes
	// strategy reads them instead of the mode's caps.
	SetCap5h, SetCap7d float64
	// DrainUntil: the owner drains the account until then (next.accounts.<key>.drain_until;
	// zero: off; setup.md 8.5, Owner drain).
	DrainUntil time.Time
}

// Protected reports whether the lanes strategy keeps the account for its owner: mode
// protected or conservative. Every other mode, and none, is aggressive.
func (p Policy) Protected() bool {
	return p.Mode == config.NextModeProtected || p.Mode == config.NextModeConservative
}

// PolicyFor finds the account's settings: the next.accounts key equal to its hub seat id,
// then its email, then its folder, then its label on this machine, then its hub label.
// Keys never match by position, so a label that differs between machines only matters when
// it is used as the key. The mode is the first that is set of that key's mode, the seat's
// hub setting and this machine's slots.modes proposal; none is normal (aggressive for lanes).
func (s Settings) PolicyFor(c Candidate) (Policy, []string) {
	var warn []string
	var a config.NextAccount
	key := ""
	for _, k := range []string{c.SeatID, c.Email, c.Folder, c.Label, c.HubLabel} {
		if k == "" {
			continue
		}
		if v, ok := s.Accounts[k]; ok {
			a, key = v, k
			break
		}
		if c.Email != "" && k == c.Email { // emails compare without case
			for ak, v := range s.Accounts {
				if strings.EqualFold(ak, k) {
					a, key = v, ak
					break
				}
			}
			if key != "" {
				break
			}
		}
	}
	hubMode := ""
	if ValidHubMode(c.HubMode) {
		hubMode = c.HubMode
	}
	mode, from := "", ModeFromNone
	if a.Mode != "" {
		if _, ok := Presets[a.Mode]; ok {
			mode, from = a.Mode, ModeFromLocal
		} else {
			warn = append(warn, fmt.Sprintf("next.accounts.%s.mode %q is not protected, conservative, normal or aggressive; ignoring it", key, a.Mode))
		}
	}
	if mode == "" && hubMode != "" {
		mode, from = hubMode, ModeFromHub
	}
	if m := s.Proposed[c.Folder]; mode == "" && ValidHubMode(m) {
		mode, from = m, ModeFromProposed
	}
	if mode == "" {
		mode = config.NextModeNormal
	}
	pre := Presets[mode]
	p := Policy{Key: key, Mode: mode, ModeFrom: from, HubMode: hubMode, Cap5h: pre.Cap5h, Cap7d: pre.Cap7d, CapModel: pre.CapModel, Pref: pre.Pref,
		Priority: a.Priority, Weight: 1, Exclude: a.Exclude, Hours: a.Hours, Days: a.Days, Reserve: a.ReservePct}
	if a.Cap5h > 0 {
		p.SetCap5h = min(a.Cap5h, 100)
	}
	if a.Cap7d > 0 {
		p.SetCap7d = min(a.Cap7d, 100)
	}
	if a.Cap5h > 0 {
		p.Cap5h = min(a.Cap5h, 100)
	}
	if a.Cap7d > 0 {
		p.Cap7d = min(a.Cap7d, 100)
		if a.CapModel <= 0 {
			p.CapModel = p.Cap7d
		}
	}
	if a.CapModel > 0 {
		p.CapModel = min(a.CapModel, 100)
	}
	if a.Weight > 0 {
		p.Weight = a.Weight
	}
	if a.DrainUntil != "" {
		if t, err := time.Parse(time.RFC3339, a.DrainUntil); err == nil {
			p.DrainUntil = t
		} else {
			warn = append(warn, fmt.Sprintf("next.accounts.%s.drain_until %q is not a time; ignoring it", key, a.DrainUntil))
		}
	}
	return p, warn
}

// capFor is the cap of a window kind after reserve_pct.
func (p Policy) capFor(kind string) float64 {
	c := p.Cap7d
	switch kind {
	case Kind5h, KindModel5h:
		c = p.Cap5h
	case KindModel7d:
		c = p.CapModel
	}
	return max(0, c-p.Reserve)
}

// allowedAt reports whether now is inside the account's hours and days (local time of
// now), and if not, when it next is (zero if never).
func (p Policy) allowedAt(now time.Time) (bool, time.Time, error) {
	if p.Hours == "" && p.Days == "" {
		return true, time.Time{}, nil
	}
	var days map[time.Weekday]bool
	if p.Days != "" {
		d, err := config.ParseNextDays(p.Days)
		if err != nil {
			return true, time.Time{}, err
		}
		days = d
	}
	var start, end time.Duration
	hasHours := p.Hours != ""
	if hasHours {
		s, e, err := config.ParseNextHours(p.Hours)
		if err != nil {
			return true, time.Time{}, err
		}
		start, end = s, e
	}
	ok := func(t time.Time) bool {
		mid := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		of := t.Sub(mid)
		day := t.Weekday()
		if hasHours {
			if start < end {
				if of < start || of >= end {
					return false
				}
			} else { // over midnight: the part after midnight belongs to the day before
				switch {
				case of >= start:
				case of < end:
					day = (day + 6) % 7
				default:
					return false
				}
			}
		}
		return days == nil || days[day]
	}
	if ok(now) {
		return true, time.Time{}, nil
	}
	// Step by minutes to the next allowed time (at most 8 days).
	t := now.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 8*24*60; i++ {
		if ok(t) {
			return false, t, nil
		}
		t = t.Add(time.Minute)
	}
	return false, time.Time{}, nil
}
