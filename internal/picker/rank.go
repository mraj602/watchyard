package picker

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/calib"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/leases"
)

// Input is everything one ranking needs. It is pure data: tests build it by hand.
type Input struct {
	Now        time.Time
	Strategy   string // "" = Settings.Strategy
	Model      string // "" = Settings.Model
	Settings   Settings
	Candidates []Candidate
	// Pending counts live reservations per Candidate.Key (Decide fills it from Leases).
	Pending map[string]int
	// LastPicked is when each Candidate.Key was last picked; LastKey is the newest pick.
	LastPicked map[string]time.Time
	LastKey    string

	// Leases is this machine's lease ledger, oldest first (internal/leases).
	Leases []leases.Lease
	// Outbox is this machine's token rows and hook events: the lanes token meter.
	Outbox Outbox
	// N is the number of picks (lanes; 0 means 1), SIDs their session ids, Urgent --urgent.
	N      int
	SIDs   []string
	Urgent bool
	// Params are the lanes parameters; nil means DefaultLaneParams.
	Params *LaneParams
	// Calibration is the kept copy of the hub's factors by account key (CalibFile; lanes).
	Calibration map[string]calib.Factors
	// Expect is --expect (how long the caller expects its lane to run; 0 unknown) and HubDown
	// that a hub is set up but could not be read (lanes drain rules).
	Expect  time.Duration
	HubDown bool
}

// Ranked is one account in the result.
type Ranked struct {
	Label     string       `json:"label"`
	Folder    string       `json:"folder"`
	Dir       string       `json:"dir"`
	Default   bool         `json:"default_view"`
	Seat      string       `json:"seat,omitempty"`
	Email     string       `json:"email,omitempty"`
	MatchedBy string       `json:"seat_matched_by,omitempty"`
	LoggedIn  bool         `json:"logged_in"`
	Eligible  bool         `json:"eligible"`
	Excluded  string       `json:"excluded_reason,omitempty"`
	Score     float64      `json:"score"`
	Mode      string       `json:"mode"`
	ModeFrom  string       `json:"mode_from"` // local, hub, proposed or none (setup.md 8.5, Kind)
	Settings  string       `json:"settings_key,omitempty"`
	Live      int          `json:"live_sessions"`
	Pending   int          `json:"pending_reservations"`
	Source    string       `json:"data_source"`
	Freshest  *time.Time   `json:"data_at"`
	Stale     bool         `json:"data_stale"`
	Windows   []Projection `json:"windows"`
	// FreeAt is when an account that is over a cap or outside its hours can next be used.
	FreeAt *time.Time `json:"free_at,omitempty"`
	Reason string     `json:"reason"`
	// Kind and Lanes are set by the lanes strategy only.
	Kind  string    `json:"kind,omitempty"`
	Lanes *LaneView `json:"lanes,omitempty"`

	key      string
	priority float64
	policy   Policy
	cand     Candidate
}

// Key is the lease key of the account (Candidate.Key).
func (r Ranked) Key() string { return r.key }

// Account and Org are the uuids of the account's login ("" when unknown).
func (r Ranked) Account() string { return r.cand.Account }
func (r Ranked) Org() string     { return r.cand.Org }

// Pick is one placed pick: the account, the session id it is for, and its lease.
type Pick struct {
	Ranked
	SessionID string
	Urgent    bool   // --urgent passed a burst cap for it
	LaneKind  string // aggressive or protected
	C5, CF    float64
	// Drain picks (lanes, setup.md 8.5 Drain): finish by FinishBy, the weekly room ends at
	// Reset; Spill: the lane will likely run past the reset.
	Drain    bool
	Spill    bool
	FinishBy time.Time
	Reset    time.Time
}

// Hint is the one-line instruction for the agent on a drain pick ("" otherwise).
func (p Pick) Hint() string {
	if !p.Drain {
		return ""
	}
	return DrainHint(p.Label, p.Reset, p.FinishBy)
}

// Result is the ranking.
type Result struct {
	Now      time.Time `json:"time"`
	Strategy string    `json:"strategy"`
	Model    string    `json:"model,omitempty"`
	Pick     *Ranked   `json:"-"`
	// Picks are every placed pick in order (Pick is the first); Waiting is how many of the
	// N asked for were not placed.
	Picks   []Pick `json:"-"`
	Waiting int    `json:"-"`
	// Ranked has the eligible accounts best first, then the others.
	Ranked []Ranked `json:"accounts"`
	// NextFree is the earliest time an account that is not eligible now can be used.
	NextFree *time.Time `json:"next_free"`
	Warnings []string   `json:"warnings,omitempty"`
}

// Rank projects every candidate and orders them by the strategy.
func Rank(in Input) Result {
	s := in.Settings
	strategy := in.Strategy
	if strategy == "" {
		strategy = s.Strategy
	}
	if !config.ValidNextStrategy(strategy) || strategy == config.NextStrategyLanes {
		strategy = config.NextStrategySmart // Rank is the projection ranker; Decide runs lanes
	}
	model := in.Model
	if model == "" {
		model = s.Model
	}
	res := Result{Now: in.Now, Strategy: strategy, Model: model}

	// Global fallback rates: the mean history rate per kind over all accounts that have one.
	global := map[string][]float64{}
	for _, c := range in.Candidates {
		for _, w := range c.Windows {
			kind, length, ok := windowKind(w.Key, model)
			if !ok || w.Idle || w.ResetsAt.IsZero() || !in.Now.Before(w.ResetsAt) {
				continue
			}
			if r, ok := historyRate(w.Samples, in.Now, w.ResetsAt.Add(-length), lookbackFor(kind)); ok {
				global[kind] = append(global[kind], r)
			}
		}
	}
	mean := func(xs []float64) (float64, bool) {
		if len(xs) == 0 {
			return 0, false
		}
		t := 0.0
		for _, x := range xs {
			t += x
		}
		return t / float64(len(xs)), true
	}

	for _, c := range in.Candidates {
		pol, warn := s.PolicyFor(c)
		res.Warnings = append(res.Warnings, warn...)
		r := Ranked{Label: c.Label, Folder: c.Folder, Dir: c.Dir, Default: c.Default, Seat: c.SeatID, Email: c.Email,
			MatchedBy: c.MatchedBy, LoggedIn: c.LoggedIn, Mode: pol.Mode, ModeFrom: pol.ModeFrom, Settings: pol.Key, Live: c.LiveSessions,
			Pending: in.Pending[c.Key()], Source: c.Source, key: c.Key(), policy: pol, cand: c, Windows: []Projection{}}
		if r.Source == "" {
			r.Source = "none"
		}
		r.priority = pol.Priority
		if r.priority <= 0 {
			r.priority = math.Inf(1)
		}
		seen := map[string]bool{}
		for _, w := range c.Windows {
			kind, length, ok := windowKind(w.Key, model)
			if !ok || seen[w.Key] {
				continue
			}
			seen[w.Key] = true
			per := s.Session7d
			if short(kind) {
				per = s.Session5h
			}
			rate, from := 0.0, RateNone
			current := !w.Idle && !w.ResetsAt.IsZero() && in.Now.Before(w.ResetsAt)
			if current || (w.ResetsAt.IsZero() && w.UsedPct > 0) {
				start := time.Time{}
				if !w.ResetsAt.IsZero() {
					start = w.ResetsAt.Add(-length)
				}
				// A five-hour trend comes from recent readings only: the window's average
				// (or other accounts') says little about the next two hours, and the live
				// sessions set their own floor in project.
				if x, ok := historyRate(w.Samples, in.Now, start, lookbackFor(kind)); ok {
					rate, from = x, RateHistory
				} else if x, ok := windowRate(w, length); ok && weekly(kind) {
					rate, from = x, RateWindow
				} else if x, ok := mean(global[kind]); ok && weekly(kind) {
					rate, from = x, RateGlobal
				}
			}
			p := project(w, projectInput{now: in.Now, kind: kind, length: length, cap: pol.capFor(kind), perSession: per,
				session: s.Session, stale: s.Stale, rate: rate, rateFrom: from, live: c.LiveSessions, pending: r.Pending})
			r.Windows = append(r.Windows, p)
			if t := p.ReadingAt; !t.IsZero() && (r.Freshest == nil || t.After(*r.Freshest)) {
				tt := t
				r.Freshest = &tt
			}
			r.Stale = r.Stale || p.Stale
		}
		// Windows the seat has no reading for yet count as not started.
		for _, k := range []string{"five_hour", "seven_day"} {
			if seen[k] {
				continue
			}
			kind, length, _ := windowKind(k, model)
			per := s.Session7d
			if short(kind) {
				per = s.Session5h
			}
			p := project(Window{Key: k, Idle: true, Unknown: true, UpdatedAt: in.Now, Source: "none"}, projectInput{now: in.Now, kind: kind,
				length: length, cap: pol.capFor(kind), perSession: per, session: s.Session, stale: s.Stale, rateFrom: RateNone,
				live: c.LiveSessions, pending: r.Pending})
			r.Windows = append(r.Windows, p)
			r.Stale = true
		}
		sort.SliceStable(r.Windows, func(i, j int) bool { return windowOrder(r.Windows[i]) < windowOrder(r.Windows[j]) })
		judge(&r, in.Now)
		res.Ranked = append(res.Ranked, r)
	}

	score(res.Ranked, strategy, in, s)
	sort.SliceStable(res.Ranked, func(i, j int) bool {
		a, b := res.Ranked[i], res.Ranked[j]
		if a.Eligible != b.Eligible {
			return a.Eligible
		}
		if a.Eligible && a.Score != b.Score {
			return a.Score > b.Score
		}
		return naturalLess(a.Label+"\x00"+a.Folder, b.Label+"\x00"+b.Folder)
	})
	for i := range res.Ranked {
		r := &res.Ranked[i]
		r.Score = round2(r.Score)
		if r.Eligible && res.Pick == nil {
			res.Pick = r
		}
		if !r.Eligible && r.FreeAt != nil && (res.NextFree == nil || r.FreeAt.Before(*res.NextFree)) {
			t := *r.FreeAt
			res.NextFree = &t
		}
	}
	for i := range res.Ranked {
		res.Ranked[i].Reason = reason(res.Ranked[i], in.Now)
	}
	if res.Pick != nil {
		res.NextFree = nil
	}
	return res
}

func windowOrder(p Projection) int {
	switch p.Kind {
	case Kind5h:
		return 0
	case Kind7d:
		return 1
	case KindModel5h:
		return 2
	}
	return 3
}

// judge sets Eligible, Excluded and FreeAt.
func judge(r *Ranked, now time.Time) {
	if !gate(r, now) {
		return
	}
	var over []string
	var free time.Time
	known := true
	for _, p := range r.Windows {
		if !p.Over {
			continue
		}
		over = append(over, fmt.Sprintf("%s ~%.0f%% > cap %.0f%%", shortKey(p.Key), p.AtSessionEnd, p.Cap))
		if p.ResetsAt.IsZero() {
			known = false
		} else if p.ResetsAt.After(free) {
			free = p.ResetsAt
		}
	}
	if len(over) > 0 {
		r.Excluded = "over cap at session end: " + strings.Join(over, ", ")
		if known && !free.IsZero() {
			r.FreeAt = &free
		}
		return
	}
	r.Eligible = true
}

// gate applies the rules every strategy applies first (logged in on this machine, not signed
// out on the hub, not excluded, inside its hours and days). It sets Excluded and FreeAt and
// reports false when one of them rules the account out.
func gate(r *Ranked, now time.Time) bool {
	c, pol := r.cand, r.policy
	switch {
	case !c.LoggedIn:
		r.Excluded = "not logged in on this machine"
		return false
	case c.SignedOut != nil:
		r.Excluded = "the hub saw it signed out on this machine since " + c.SignedOut.Local().Format("2006-01-02 15:04")
		return false
	case pol.Exclude:
		r.Excluded = "excluded in settings (next.accounts." + pol.Key + ".exclude)"
		return false
	}
	ok, next, err := pol.allowedAt(now)
	if err != nil {
		r.Excluded = "bad hours or days in settings: " + err.Error()
		return false
	}
	if !ok {
		r.Excluded = "outside its hours"
		if pol.Hours != "" {
			r.Excluded += " " + pol.Hours
		}
		if pol.Days != "" {
			r.Excluded += " " + pol.Days
		}
		if !next.IsZero() {
			r.FreeAt = &next
		}
		return false
	}
	return true
}

// score sets Score on the eligible accounts for the strategy (higher is better).
func score(rs []Ranked, strategy string, in Input, s Settings) {
	for i := range rs {
		r := &rs[i]
		if !r.Eligible {
			r.Score = 0
			continue
		}
		smart := smartScore(*r)
		switch strategy {
		case config.NextStrategyExpiring:
			u, h := urgency(*r)
			r.Score = u + 1e-4*h
		case config.NextStrategyMostRoom:
			m := 0.0
			for _, p := range r.Windows {
				m = math.Max(m, p.Now+p.Uncertainty+float64(r.Pending)*perSession(p.Kind, s))
			}
			r.Score = 100 - m
		case config.NextStrategySpread:
			last, ok := in.LastPicked[r.key]
			h := 1e6 // never picked: first
			if ok {
				h = math.Max(0, in.Now.Sub(last).Hours())
			}
			r.Score = h + 1e-6*smart
		case config.NextStrategyFill:
			r.Score = -r.priority
			if math.IsInf(r.priority, 1) {
				r.Score = -1e9
			}
		case config.NextStrategySticky:
			r.Score = smart
			if r.key == in.LastKey && underSoftCaps(*r, s.StickyMargin) {
				r.Score = 1e9
			}
		default:
			r.Score = smart
		}
	}
}

func perSession(kind string, s Settings) float64 {
	if short(kind) {
		return s.Session5h
	}
	return s.Session7d
}

// urgency is the use-it-or-lose-it term: the weekly room that would be unused at the reset,
// per hour left until that reset, on the binding weekly window (the one with the least
// unused room); and the least headroom at session end over all windows.
func urgency(r Ranked) (u, headroom float64) {
	headroom = math.Inf(1)
	bind := -1
	for i, p := range r.Windows {
		headroom = math.Min(headroom, p.Cap-p.AtSessionEnd)
		if weekly(p.Kind) && (bind < 0 || p.Unused < r.Windows[bind].Unused) {
			bind = i
		}
	}
	if math.IsInf(headroom, 1) {
		headroom = 100
	}
	if bind >= 0 {
		p := r.Windows[bind]
		u = p.Unused / math.Max(p.HoursToReset, 1)
	}
	return u, math.Max(0, headroom)
}

// smartScore = weight × mode preference × safety × confidence × (urgency + 0.02 × headroom),
// with safety = clamp((cap_5h − 5h at session end) / 20, 0.2, 1) and confidence 0.75 for an
// account with no usage data, else 1.
func smartScore(r Ranked) float64 {
	u, h := urgency(r)
	safety := 1.0
	for _, p := range r.Windows {
		if short(p.Kind) {
			safety = math.Min(safety, math.Max(0.2, math.Min(1, (p.Cap-p.AtSessionEnd)/20)))
		}
	}
	conf := 1.0
	if r.Source == "none" { // usage unknown: behind an account whose usage is known to fit
		conf = noDataFactor
	}
	return r.policy.Weight * r.policy.Pref * safety * conf * (u + 0.02*h)
}

// noDataFactor scales the smart score of an account with no usage data at all.
const noDataFactor = 0.75

func underSoftCaps(r Ranked, margin float64) bool {
	for _, p := range r.Windows {
		if p.AtSessionEnd > p.Cap-margin {
			return false
		}
	}
	return true
}

func shortKey(k string) string {
	switch k {
	case "five_hour":
		return "5h"
	case "seven_day":
		return "7d"
	}
	if r, ok := strings.CutPrefix(k, "seven_day_"); ok {
		return r + " 7d"
	}
	if r, ok := strings.CutPrefix(k, "weekly_"); ok {
		return r + " 7d"
	}
	if r, ok := strings.CutPrefix(k, "session_"); ok {
		return r + " 5h"
	}
	return k
}

// reason is the short text after the folder: "7d 23% → ~41% at reset Wed; 5h fresh; aggressive".
func reason(r Ranked, now time.Time) string {
	if !r.Eligible {
		s := r.Excluded
		if r.FreeAt != nil {
			s += "; free " + when(*r.FreeAt, now)
		}
		return s
	}
	var parts []string
	for _, p := range r.Windows {
		switch {
		case p.Source == "none":
			parts = append(parts, shortKey(p.Key)+" unknown")
		case short(p.Kind):
			if p.Now < 1 && p.Uncertainty < 1 {
				parts = append(parts, shortKey(p.Key)+" fresh")
			} else {
				parts = append(parts, fmt.Sprintf("%s %.0f%%", shortKey(p.Key), p.Now+p.Uncertainty))
			}
		default:
			if p.ResetsAt.IsZero() {
				parts = append(parts, fmt.Sprintf("%s %.0f%%, starts on use", shortKey(p.Key), p.Now))
			} else {
				parts = append(parts, fmt.Sprintf("%s %.0f%% → ~%.0f%% at reset %s", shortKey(p.Key), p.Now,
					math.Min(p.AtReset, 999), when(p.ResetsAt, now)))
			}
		}
	}
	// weekly first: it is what the pick is mostly about
	sort.SliceStable(parts, func(i, j int) bool {
		return strings.Contains(parts[i], "7d") && !strings.Contains(parts[j], "7d")
	})
	if r.Mode != config.NextModeNormal {
		parts = append(parts, r.Mode)
	}
	if r.Pending > 0 {
		parts = append(parts, fmt.Sprintf("+%d reserved", r.Pending))
	}
	switch {
	case r.Source == "local":
		parts = append(parts, "local data")
	case r.Source == "none":
		parts = append(parts, "no usage data")
	}
	if r.Stale && r.Freshest != nil && r.Source != "none" {
		parts = append(parts, "data "+age(now.Sub(*r.Freshest))+" old")
	}
	return strings.Join(parts, "; ")
}

// when writes a time near now: "15:20" today, "Wed 15:20" within a week, else the date.
func when(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case t.Sub(now) < 6*24*time.Hour && t.After(now):
		return t.Format("Mon 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

// naturalLess orders "claude-2" before "claude-10".
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, _ := strconv.Atoi(a[:min(da, 9)])
			nb, _ := strconv.Atoi(b[:min(db, 9)])
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digits(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}
