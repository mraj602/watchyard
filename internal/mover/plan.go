package mover

import (
	"fmt"
	"github.com/mraj602/watchyard/internal/projection"
	"math"
	"sort"
	"strings"
	"time"
)

// Params are the rules of Smart Account Switch v2 (setup.md 8.6, Decision): move the
// sessions that burn, not the idle ones, when an account's projected 5-hour or weekly level at
// its current burn goes over its cap. Every number is a config key of [account] (config.go
// moveKeys); DefaultParams holds the defaults.
type Params struct {
	Name string
	// Off: never move (M0 in the simulator).
	Off bool
	// Cap5 and Cap7: the projected 5-hour and weekly levels an aggressive account may reach;
	// Cap5Protected and Cap7Protected the same for a protected account.
	Cap5, Cap7                   float64
	Cap5Protected, Cap7Protected float64
	// Hysteresis: moves go on until the source's projections are this far under its caps, and
	// a destination must stay this far under its own with the moved burn.
	Hysteresis float64
	// BurnWindow: the burn is measured over this window (the caller measures it; a session
	// with tokens in it burned). Pass: the time between two passes.
	BurnWindow time.Duration
	Pass       time.Duration
	// WeekSpan: the weekly projection looks this far ahead at most (the 5-hour burn now says
	// little about later days).
	WeekSpan time.Duration
	// SpikePasses: a trigger must hold on this many consecutive passes, unless the projected
	// hit of 100 is within SpikeNear or a cap is reached now (0 or 1: no spike filter).
	SpikePasses int
	// ReadingPasses: the passes a trigger needs when only the readings' rate makes it.
	ReadingPasses int
	SpikeNear     time.Duration
	// Cooldown: a session moves at most once within it. NoReturn: never back to the account it
	// left within it, unless that account's 5-hour window reset since.
	Cooldown time.Duration
	NoReturn time.Duration
	// AccountMoves: at most this many moves into one account per AccountWindow.
	AccountMoves  int
	AccountWindow time.Duration
	// StoppedWait: a session a limit stopped moves only when that window resets at least this
	// far ahead (nearer, waiting costs less than a restart and a re-read).
	StoppedWait time.Duration
	// StepWait: a mid-turn move waits at most this long for the step in flight to end (or
	// less, when the hit comes sooner); then it kills inside the step.
	StepWait time.Duration
	// OwnerGrace: an interactive session the owner typed into this recently is left alone (0:
	// off, the default; the simulator compares 2 minutes).
	OwnerGrace time.Duration
	// Orgs: which organisations a session may move to: any, same-first or same.
	Orgs string
	// IdleMoves: idle sessions with no burn are candidates too (a simulator variant only).
	IdleMoves bool
	// DestHorizon: an aggressive destination qualifies when the moved session fits under its
	// caps less Hysteresis for min(its expected remaining time (DestHorizon when unknown), the
	// time to the destination's 5-hour reset), at least DestFloor, on top of the
	// destination's own projection; a protected one only with room to its resets, and only
	// when no aggressive one fits. 0: every destination needs room to its resets.
	DestHorizon time.Duration
	// CheapMoves: among the sessions whose move alone fixes the overload, the most points
	// freed per re-read cost (context × ModelFactor) first, then the highest burn, then the
	// youngest.
	CheapMoves bool
	// PinReserve5 and PinReserve7: the points of the 5-hour and weekly windows kept free for
	// each live pinned session on an account (account.pin_reserve_5h / _7d): its caps for the
	// other sessions are cap - pins x reserve. The caller counts the live pins (Account.Pins)
	// and sets Account.Reserve5/7 (PinReserve).
	PinReserve5, PinReserve7 float64
	// PinAlertWithin: a pinned session that alone takes its account to 100 before the reset,
	// this soon, is an owner alert (Result.Alerts).
	PinAlertWithin time.Duration
	// YoungGate: a session's burn counts only once it is this old (Session.Started): a lane's
	// first minutes (reading the codebase, cache writes) burn far above its steady rate, and
	// the burn window then holds only those minutes. Younger sessions' rates are left out of
	// their account's meter (not of a rate from the readings). 0: counted at once.
	YoungGate time.Duration
}

// PinReserve is the reserve of an account with pins live pinned sessions: pins x
// PinReserve5 points of the 5-hour window and pins x PinReserve7 of the week.
func (p Params) PinReserve(pins int) (float64, float64) {
	return float64(pins) * math.Max(0, p.PinReserve5), float64(pins) * math.Max(0, p.PinReserve7)
}

// The values of Params.Orgs (account.move_orgs).
const (
	OrgsAny       = "any"
	OrgsSameFirst = "same-first"
	OrgsSame      = "same"
)

// DefaultParams are the defaults of every rule (setup.md 8.6, Decision, the reason for each).
var DefaultParams = Params{
	Name: "V2 move the burners (default)",
	Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80,
	Hysteresis: 5, BurnWindow: 10 * time.Minute, Pass: time.Minute, WeekSpan: 5 * time.Hour,
	SpikePasses: 2, ReadingPasses: 3, SpikeNear: 30 * time.Minute,
	Cooldown: 20 * time.Minute, NoReturn: time.Hour, AccountMoves: 3, AccountWindow: 15 * time.Minute,
	StoppedWait: 20 * time.Minute, StepWait: 3 * time.Minute, Orgs: OrgsAny,
	PinReserve5: 10, PinReserve7: 5, PinAlertWithin: 2 * time.Hour,
	DestHorizon: 90 * time.Minute, CheapMoves: true, YoungGate: 15 * time.Minute,
}

// PinIdle: a pinned session idle this long holds no reserve (the caller counts the live pins).
const PinIdle = 6 * time.Hour

// Window kinds.
const (
	Win5h = "5h"
	Win7d = "7d"
)

// Window is one usage window of an account: its measured level now (percent; for the 5-hour
// window that is also 5-hour points) and when it resets (zero: not started, or unknown).
type Window struct {
	Level float64
	Reset time.Time
}

// Account is one account as the mover sees it.
type Account struct {
	Key       string // "acct:<account>/<org>"
	Label     string
	Protected bool
	W5, W7    Window
	// Ratio is 5-hour points per weekly point (the picker's calibrated ρ); 0: 7.2.
	Ratio float64
	// Rate is the account's measured 5-hour burn, points per hour (R5): its sessions' token
	// meter over the burn window × f, or the reading delta when that is over twice as large.
	// When it is below the sum of its sessions' rates here, that sum is used.
	Rate float64
	// RateNote says which measure Rate is when they disagreed (for --plan).
	RateNote string
	// Meter is the token meter's burn (this machine's sessions and other machines' the hub
	// measured); FromReadings: Rate is the readings' slope instead (over twice the meter). A
	// trigger that only the readings make needs ReadingPasses passes (they are noisy).
	Meter        float64
	FromReadings bool
	// LoggedIn: logged in on this machine (the folder exists and `wy account next` does not
	// call it "not logged in on this machine"). Only such an account is a destination.
	LoggedIn bool
	// Stale: its readings are too old to project; never a destination.
	Stale bool
	// Excluded: never a destination (excluded by settings).
	Excluded bool
	// Drain: the picker spends its room first (it expires unused otherwise).
	Drain bool
	// Pins: its live pinned sessions (running, not idle for PinIdle); Reserve5 and Reserve7
	// the points of its 5-hour and weekly windows kept free for them (Params.PinReserve): its
	// caps for every other session are its caps less these.
	Pins               int
	Reserve5, Reserve7 float64
}

// Org is the account's organisation (the part of its key after the last "/").
func (a Account) Org() string { return Org(a.Key) }

// Session is one running session as the mover sees it.
type Session struct {
	ID      string
	Account string
	Name    string
	// Rate is its burn over the burn window, 5-hour points per hour, its helpers included.
	Rate float64
	// Busy: in the middle of a turn (registry busy). A busy session or one with tokens in the
	// burn window burned; one that did neither is idle with no burn and never moves.
	Busy bool
	// Stopped: a limit ended its last turn (StopFailure rate_limit) and that window is full.
	Stopped bool
	// Interactive: a human-facing session (setup.md 8.6, Interactive): moved like any other,
	// with a message in its pane or terminal; Typed is when the owner last typed into it.
	Interactive bool
	Typed       time.Time
	// Pinned: the owner opted it out (no-move, --pin-account) or it is outside the scope.
	Pinned bool
	// Movable: its kind can be restarted (false for claude.ai --sdk-url sessions).
	Movable bool
	// Context is its context in tokens (a move re-reads it uncached).
	Context float64
	// Expected is how much longer it is expected to run (0: unknown).
	Expected time.Duration
	// ModelFactor is its model's re-read price relative to haiku (opus 5, sonnet 3, haiku 1;
	// 0: 3); Started is when it started (zero: unknown, taken as old).
	ModelFactor float64
	Started     time.Time
	// MovedAt and MovedFrom: its last move (zero: never moved).
	MovedAt   time.Time
	MovedFrom string
	// YoungRate: its measured burn while it is under Params.YoungGate (GateYoung set Rate to 0).
	YoungRate float64
}

func (s Session) burned() bool { return s.Busy || s.Rate > 0 }

// Record is a move that happened (cooldown, no return, the account rate limit).
type Record struct {
	Session  string
	From, To string
	At       time.Time
}

// Input is one planning pass.
type Input struct {
	Now      time.Time
	Accounts []Account // in the picker's order
	Sessions []Session
	Recent   []Record // moves of the last hours, any order
	Params   Params
	// Streak: per account, on how many consecutive passes before this one it triggered (the
	// spike filter).
	Streak map[string]int
}

// Projection is one account's projections at its burn now.
type Projection struct {
	Key, Label string
	Protected  bool
	Level5     float64
	Level7     float64
	R5         float64 // 5-hour points per hour
	RateNote   string  `json:",omitempty"`
	P5, P7     float64
	Cap5, Cap7 float64
	// Reset5 and Reset7: time to the resets; Hit: time to the first projected 100 (-1: none
	// before the resets / the week span).
	Reset5, Reset7 time.Duration
	Hit            time.Duration
	// Pins, Reserve5 and Reserve7: its live pinned sessions and the points kept free for them;
	// Cap5 and Cap7 are already less these.
	Pins               int     `json:",omitempty"`
	Reserve5, Reserve7 float64 `json:",omitempty"`
	// Window is the window over its cap ("5h", "7d", "" none); Hard: a cap is reached now.
	Window string
	Hard   bool
	// Streak counts the consecutive passes it triggered, this one included; Triggered is
	// whether this pass acts on it (Held: over the cap, waiting for the spike filter).
	Streak    int
	Triggered bool
	Held      bool
	// ReadingsOnly: over the caps only at the readings' rate (ReadingPasses passes needed).
	ReadingsOnly bool `json:",omitempty"`
	ratio        float64
}

// Move is one planned move.
type Move struct {
	Session string
	From    string
	To      string // "" when no account can take it (Reason says why)
	// Window: what triggers it ("5h" or "7d"; "" for a move by hand without a trigger); P its
	// projection; HitAt when it reaches 100 at that burn (zero: not before the reset).
	Window  string
	P       float64
	HitAt   time.Time
	ResetAt time.Time
	// MidTurn: the session is in the middle of a turn (it resumes with a prompt to go on).
	MidTurn bool
	// Stopped: a limit ended its last turn.
	Stopped bool
	// ByHand: asked for with wy move <session>.
	ByHand bool
	// OrgChanged: the destination is another organisation.
	OrgChanged bool
	// Interactive: a human-facing session (it gets a message in its pane or terminal).
	Interactive bool
	// StepEnded: a mid-turn move waited for the session's tool call in flight to end and
	// killed it between steps. StepTool and StepCmd: the step it was killed inside when the
	// wait ran out ("" none, or unknown).
	StepEnded bool
	StepTool  string
	StepCmd   string
	Rate      float64
	Reason    string
}

// Skip is a session on a triggered account the pass leaves where it is, and why.
type Skip struct {
	Session string `json:"session"`
	Account string `json:"account"`
	Why     string `json:"why"`
}

// Alert is a pinned session that alone takes its account to 100 before the window resets
// (setup.md 8.6, Pinned sessions): it is never moved, the owner is told once.
type Alert struct {
	Session string        `json:"session"`
	Account string        `json:"account"`
	Window  string        `json:"window"`
	P       float64       `json:"projected"` // the account's projection at the pinned burn alone
	Rate    float64       `json:"rate"`      // the pinned sessions' burn, 5-hour points per hour
	Level   float64       `json:"level"`
	HitAt   time.Time     `json:"hit_at"`
	ResetAt time.Time     `json:"reset_at"`
	In      time.Duration `json:"-"`
}

// Result is one pass: what to move, whom to tell, what is left and why, and the projections.
type Result struct {
	Moves    []Move
	Skips    []Skip
	Alerts   []Alert
	Accounts []Projection
	// Streak is the spike filter's state after this pass (keep it for the next one).
	Streak map[string]int
}

// young reports whether session s is under Params.YoungGate (its burn not counted yet).
func (p Params) young(s Session, now time.Time) bool {
	return p.YoungGate > 0 && !s.Started.IsZero() && now.Sub(s.Started) < p.YoungGate
}

// GateYoung leaves out the burn of the sessions under Params.YoungGate (setup.md 8.6, Young
// sessions): their Rate becomes 0 (kept in YoungRate), and their account's measured Rate and
// Meter lose it unless the Rate is the readings' slope. Plan calls it; a caller projecting on
// its own calls it first.
func GateYoung(in Input) Input {
	p := in.Params
	if p.YoungGate <= 0 {
		return in
	}
	sub := map[string]float64{}
	var ss []Session
	for i, s := range in.Sessions {
		if !p.young(s, in.Now) || s.Rate <= 0 {
			continue
		}
		if ss == nil {
			ss = append([]Session(nil), in.Sessions...)
		}
		if !s.Stopped {
			sub[s.Account] += s.Rate
		}
		ss[i].YoungRate, ss[i].Rate = s.Rate, 0
	}
	if ss == nil {
		return in
	}
	in.Sessions = ss
	as := append([]Account(nil), in.Accounts...)
	for i, a := range as {
		x := sub[a.Key]
		if x <= 0 || a.FromReadings {
			continue
		}
		as[i].Rate, as[i].Meter = math.Max(0, a.Rate-x), math.Max(0, a.Meter-x)
		note := fmt.Sprintf("%.1f points/h of sessions under %d min left out (a session's first minutes burn far above its steady rate)",
			x, int(p.YoungGate.Minutes()))
		if a.RateNote != "" {
			note = a.RateNote + "; " + note
		}
		as[i].RateNote = note
	}
	in.Accounts = as
	return in
}

// Plan is one pass of Smart Account Switch. It changes nothing; the caller carries out the
// moves and notices.
func Plan(in Input) Result {
	in = GateYoung(in)
	p := in.Params
	now := in.Now
	res := Result{Streak: map[string]int{}}
	byAcc := map[string][]Session{}
	for _, s := range in.Sessions {
		byAcc[s.Account] = append(byAcc[s.Account], s)
	}
	// Planned burn moved in and out of each account during this pass.
	delta := map[string]float64{}
	proj := map[string]Projection{}
	for _, a := range in.Accounts {
		pr := p.Project(a, byAcc[a.Key], 0, now)
		over := pr.Window != ""
		// The streak is the trigger's (account and window): a 5-hour trigger after a weekly
		// one starts again.
		if over {
			k := a.Key + " " + pr.Window
			pr.Streak = in.Streak[k] + 1
			res.Streak[k] = pr.Streak
		}
		near := pr.Hit >= 0 && pr.Hit <= p.SpikeNear
		passes := p.SpikePasses
		if over && a.FromReadings && !pr.Hard {
			// Only the readings make it: the meter's projection is under the caps.
			m := a
			m.Rate = a.Meter
			if p.Project(m, byAcc[a.Key], 0, now).Window == "" {
				passes, near = max(passes, p.ReadingPasses), false
				pr.ReadingsOnly = true
			}
		}
		pr.Triggered = over && (passes <= 1 || pr.Streak >= passes || pr.Hard || near)
		pr.Held = over && !pr.Triggered
		proj[a.Key] = pr
		res.Accounts = append(res.Accounts, pr)
	}
	res.Alerts = p.pinAlerts(in, byAcc)
	if p.Off {
		return res
	}
	// Sources: a cap reached now first, then the soonest hit, then the picker's order.
	var srcs []Account
	for _, a := range in.Accounts {
		if proj[a.Key].Window != "" || hasStopped(byAcc[a.Key]) {
			srcs = append(srcs, a)
		}
	}
	sort.SliceStable(srcs, func(i, j int) bool {
		x, y := proj[srcs[i].Key], proj[srcs[j].Key]
		if x.Hard != y.Hard {
			return x.Hard
		}
		hx, hy := x.Hit, y.Hit
		if hx < 0 {
			hx = math.MaxInt64
		}
		if hy < 0 {
			hy = math.MaxInt64
		}
		return hx < hy
	})

	st := p.newMoveState(in)
	for _, a := range srcs {
		pr := proj[a.Key]
		ss := byAcc[a.Key]
		skip := func(s Session, why string) { res.Skips = append(res.Skips, Skip{s.ID, a.Key, why}) }
		// Stopped sessions first: they burn nothing now but want to (their burn intent), and
		// they wait on the limit until moved.
		for _, s := range sortedIDs(ss) {
			if !s.Stopped {
				continue
			}
			w, reset := stoppedWindow(a, now)
			if why := p.excluded(s, st, now); why != "" {
				skip(s, why)
				continue
			}
			if reset < p.StoppedWait {
				skip(s, fmt.Sprintf("stopped at the %s limit, which resets in %s: waiting costs less than a move", w, reset.Round(time.Minute)))
				continue
			}
			mv := Move{Session: s.ID, From: a.Key, Window: w, P: pr.pOf(w), ResetAt: now.Add(reset), Stopped: true, Rate: s.Rate, Interactive: s.Interactive}
			p.place(in, &res, st, s, a, mv, delta, skip)
		}
		if !pr.Triggered {
			if pr.Held {
				for _, s := range ss {
					if s.burned() && !s.Stopped && s.Pinned {
						skip(s, "pinned, or outside the scope")
					} else if s.burned() && !s.Stopped {
						need, by := p.SpikePasses, "spike filter"
						if pr.ReadingsOnly {
							need, by = max(need, p.ReadingPasses), "spike filter; only the readings' rate is over"
						}
						skip(s, fmt.Sprintf("%s over its cap on %d of %d passes (%s)", pr.Window, pr.Streak, need, by))
					}
				}
			}
			continue
		}
		// The burners: every session that burned in the burn window, any turn state,
		// interactive ones too (they get a message in their pane or terminal), moved greedily
		// until both projections are Hysteresis under the caps.
		var cands []Session
		for _, s := range ss {
			if s.Stopped {
				continue
			}
			if !s.burned() && !p.IdleMoves {
				continue // idle with no burn: moving it frees nothing
			}
			if why := p.excluded(s, st, now); why != "" {
				skip(s, why)
				continue
			}
			cands = append(cands, s)
		}
		for len(cands) > 0 {
			cur := p.Project(a, ss, delta[a.Key], now)
			if !cur.overBy(p.Hysteresis) {
				for _, s := range cands {
					skip(s, fmt.Sprintf("enough moved: %s projected 5h %.0f%%, 7d %.0f%%", a.Label, cur.P5, cur.P7))
				}
				break
			}
			sort.SliceStable(cands, func(i, j int) bool {
				x, y := cands[i], cands[j]
				ex, ey := cur.enoughWithout(p, x.Rate), cur.enoughWithout(p, y.Rate)
				if ex != ey {
					return ex // the fewest moves: one that brings it under alone first
				}
				if ex && p.CheapMoves {
					if cx, cy := x.Rate/reRead(x), y.Rate/reRead(y); cx != cy {
						return cx > cy // the most points freed per re-read
					}
					if x.Rate != y.Rate {
						return x.Rate > y.Rate
					}
					if !x.Started.Equal(y.Started) {
						return x.Started.After(y.Started) // the youngest
					}
				}
				if x.Rate != y.Rate {
					return x.Rate > y.Rate // the highest burn
				}
				if x.Context != y.Context {
					return x.Context < y.Context // the cheaper re-read
				}
				if x.Expected != y.Expected {
					return x.Expected > y.Expected // the longest expected remaining
				}
				return x.ID < y.ID
			})
			s := cands[0]
			cands = cands[1:]
			mv := Move{Session: s.ID, From: a.Key, Window: cur.Window, P: cur.pOf(cur.Window), ResetAt: now.Add(cur.resetOf(cur.Window)),
				MidTurn: s.Busy, Rate: s.Rate, Interactive: s.Interactive}
			if mv.Window == "" {
				mv.Window, mv.P, mv.ResetAt = pr.Window, pr.pOf(pr.Window), now.Add(pr.resetOf(pr.Window))
			}
			if cur.Hit >= 0 {
				mv.HitAt = now.Add(cur.Hit)
			}
			p.place(in, &res, st, s, a, mv, delta, skip)
		}
	}
	return res
}

func hasStopped(ss []Session) bool {
	for _, s := range ss {
		if s.Stopped {
			return true
		}
	}
	return false
}

func sortedIDs(ss []Session) []Session {
	out := append([]Session(nil), ss...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// stoppedWindow is the full window that stopped a session on a and the time to its reset.
func stoppedWindow(a Account, now time.Time) (string, time.Duration) {
	if a.W7.Level >= 100 && a.W5.Level < 100 {
		return Win7d, resetIn(a.W7, now, 7*24*time.Hour)
	}
	return Win5h, resetIn(a.W5, now, 5*time.Hour)
}

// excluded is why the switch never moves s now ("" when it may).
func (p Params) excluded(s Session, st *moveState, now time.Time) string {
	switch {
	case s.Pinned:
		return "pinned, or outside the scope"
	case !s.Movable:
		return "its kind cannot be restarted from this machine (claude.ai)"
	}
	if p.OwnerGrace > 0 && s.Interactive && !s.Typed.IsZero() && now.Sub(s.Typed) < p.OwnerGrace {
		return "the owner typed into it " + now.Sub(s.Typed).Round(time.Second).String() + " ago (owner grace)"
	}
	if l, ok := st.last[s.ID]; ok && p.Cooldown > 0 && now.Sub(l.At) < p.Cooldown {
		return "moved " + now.Sub(l.At).Round(time.Minute).String() + " ago (cooldown)"
	}
	return ""
}

// place finds mv's destination and records the move (or, with none, why).
func (p Params) place(in Input, res *Result, st *moveState, s Session, a Account, mv Move, delta map[string]float64, skip func(Session, string)) {
	to, why := p.destination(in, s, st, delta)
	mv.To = to
	if to != "" {
		mv.OrgChanged = Org(to) != a.Org()
		st.movedIn[to]++
		delta[to] += s.Rate
		if !s.Stopped {
			delta[a.Key] -= s.Rate
		}
	}
	mv.Reason = p.reason(a, mv, why)
	res.Moves = append(res.Moves, mv)
}

// overBy reports whether a projection is over its caps less m.
func (pr Projection) overBy(m float64) bool {
	return pr.P5 > pr.Cap5-m || pr.P7 > pr.Cap7-m || (m == 0 && pr.Hard)
}

// enoughWithout reports whether taking rate off the account brings both projections
// Hysteresis under the caps.
func (pr Projection) enoughWithout(p Params, rate float64) bool {
	h5 := pr.Reset5.Hours()
	h7 := math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours())
	ratio := pr.ratio
	return pr.P5-rate*h5 <= pr.Cap5-p.Hysteresis && pr.P7-rate/ratio*h7 <= pr.Cap7-p.Hysteresis
}

func (pr Projection) pOf(w string) float64 {
	if w == Win7d {
		return pr.P7
	}
	return pr.P5
}

func (pr Projection) resetOf(w string) time.Duration {
	if w == Win7d {
		return pr.Reset7
	}
	return pr.Reset5
}

// moveState is the pass's memory of moves: the last move per session and the moves into each
// account within the account window (recent and planned).
type moveState struct {
	last    map[string]Record
	movedIn map[string]int
}

func (p Params) newMoveState(in Input) *moveState {
	st := &moveState{last: map[string]Record{}, movedIn: map[string]int{}}
	for _, r := range in.Recent {
		if p.AccountWindow > 0 && in.Now.Sub(r.At) < p.AccountWindow {
			st.movedIn[r.To]++
		}
		if l, ok := st.last[r.Session]; !ok || r.At.After(l.At) {
			st.last[r.Session] = r
		}
	}
	return st
}

// caps are the caps of account a.
func (p Params) caps(a Account) (float64, float64) {
	if a.Protected {
		return p.Cap5Protected, p.Cap7Protected
	}
	return p.Cap5, p.Cap7
}

func ratio(a Account) float64 {
	if a.Ratio > 0 {
		return a.Ratio
	}
	return 7.2
}

func resetIn(w Window, now time.Time, length time.Duration) time.Duration {
	if w.Reset.IsZero() || !w.Reset.After(now) {
		return length // not started: a full window from its first use
	}
	return w.Reset.Sub(now)
}

// Project is account a's projections with the sessions ss on it and extra 5-hour points per
// hour added (moves planned in and out): P5 = h + R5 × hours to the 5-hour reset, P7 = w +
// R5/ρ × min(hours to the weekly reset, WeekSpan).
func (p Params) Project(a Account, ss []Session, extra float64, now time.Time) Projection {
	r5 := a.Rate
	sum := 0.0
	for _, s := range ss {
		if !s.Stopped {
			sum += math.Max(0, s.Rate)
		}
	}
	r5 = math.Max(r5, sum)
	r5 = math.Max(0, r5+extra)
	c5, c7 := p.caps(a)
	c5, c7 = math.Max(0, c5-math.Max(0, a.Reserve5)), math.Max(0, c7-math.Max(0, a.Reserve7))
	pr := Projection{Key: a.Key, Label: a.Label, Protected: a.Protected, Level5: a.W5.Level, Level7: a.W7.Level, R5: r5, RateNote: a.RateNote,
		Cap5: c5, Cap7: c7, Hit: -1, ratio: ratio(a), Pins: a.Pins, Reserve5: math.Max(0, a.Reserve5), Reserve7: math.Max(0, a.Reserve7)}
	pr.Reset5 = resetIn(a.W5, now, 5*time.Hour)
	pr.Reset7 = resetIn(a.W7, now, 7*24*time.Hour)
	if a.W5.Reset.IsZero() || !a.W5.Reset.After(now) {
		pr.Level5 = 0 // not started (or reset since the reading)
	}
	h7 := math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours())
	pr.P5, pr.P7 = projection.Levels(pr.Level5, pr.Level7, r5, pr.ratio, pr.Reset5.Hours(), pr.Reset7.Hours(), p.WeekSpan.Hours())
	pr.Hard = pr.Level5 >= c5 || pr.Level7 >= c7
	switch {
	case pr.P5 > c5 || pr.Level5 >= c5:
		pr.Window = Win5h
	case pr.P7 > c7 || pr.Level7 >= c7:
		pr.Window = Win7d
	}
	if pr.P7-c7 > pr.P5-c5 && (pr.P7 > c7 || pr.Level7 >= c7) {
		pr.Window = Win7d // the weekly one is further over
	}
	if r5 > 0 {
		hit := math.Inf(1)
		if t := (100 - pr.Level5) / r5; t <= pr.Reset5.Hours() {
			hit = math.Max(0, t)
		}
		if t := (100 - pr.Level7) / (r5 / pr.ratio); t <= h7 && t < hit {
			hit = math.Max(0, t)
		}
		if !math.IsInf(hit, 1) {
			pr.Hit = time.Duration(hit * float64(time.Hour))
		}
	}
	return pr
}

// destination is the first account that can take s: logged in on this machine, not its own,
// not stale or excluded, of an organisation Orgs allows, not the account it left within
// NoReturn (unless that 5-hour window reset since), under the account rate limit, and whose
// projections with s's burn (and the burn already planned onto it this pass) stay Hysteresis
// under its caps. Aggressive accounts first in the picker's order (draining ones, then the
// soonest 5-hour reset, then the most room), protected ones last.
func (p Params) destination(in Input, s Session, st *moveState, delta map[string]float64) (string, string) {
	now := in.Now
	from := Org(s.Account)
	var accs []Account
	why := "no logged-in account on this machine has room for its burn"
	for _, a := range in.Accounts {
		if a.Key == s.Account || !a.LoggedIn || a.Stale || a.Excluded {
			continue
		}
		if p.Orgs == OrgsSame && a.Org() != from {
			why = "no account of its organisation has room (account.move_orgs = same)"
			continue
		}
		accs = append(accs, a)
	}
	byAcc := map[string][]Session{}
	for _, x := range in.Sessions {
		byAcc[x.Account] = append(byAcc[x.Account], x)
	}
	room := func(a Account) (Projection, bool) {
		pr := p.Project(a, byAcc[a.Key], delta[a.Key]+math.Max(0, s.Rate), now)
		return pr, pr.P5 <= pr.Cap5-p.Hysteresis && pr.P7 <= pr.Cap7-p.Hysteresis && pr.Level5 < pr.Cap5-p.Hysteresis &&
			pr.Level7 < pr.Cap7-p.Hysteresis
	}
	if p.DestHorizon > 0 {
		full := room
		room = func(a Account) (Projection, bool) {
			if a.Protected {
				return full(a)
			}
			// Its own projection, plus this session for the horizon only.
			pr := p.Project(a, byAcc[a.Key], delta[a.Key], now)
			h := s.Expected
			if h <= 0 {
				h = p.DestHorizon
			}
			h = max(min(h, pr.Reset5), DestFloor)
			r := math.Max(0, s.Rate)
			pr.P5 += r * min(h, pr.Reset5).Hours()
			pr.P7 += r / pr.ratio * math.Min(h.Hours(), math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours()))
			return pr, pr.P5 <= pr.Cap5-p.Hysteresis && pr.P7 <= pr.Cap7-p.Hysteresis && pr.Level5 < pr.Cap5-p.Hysteresis &&
				pr.Level7 < pr.Cap7-p.Hysteresis
		}
	}
	idx := map[string]int{}
	for i, a := range in.Accounts {
		idx[a.Key] = i
	}
	head := map[string]float64{}
	for _, a := range accs {
		pr, _ := room(a)
		head[a.Key] = math.Min(pr.Cap5-pr.P5, pr.Cap7-pr.P7)
	}
	far := now.Add(1000 * time.Hour)
	reset := func(a Account) time.Time {
		if a.W5.Reset.After(now) {
			return a.W5.Reset
		}
		return far // not started: its room does not expire
	}
	sort.SliceStable(accs, func(i, j int) bool {
		x, y := accs[i], accs[j]
		if x.Protected != y.Protected {
			return !x.Protected
		}
		if p.Orgs == OrgsSameFirst && (x.Org() == from) != (y.Org() == from) {
			return x.Org() == from
		}
		if x.Drain != y.Drain {
			return x.Drain
		}
		if rx, ry := reset(x), reset(y); !rx.Equal(ry) {
			return rx.Before(ry)
		}
		if head[x.Key] != head[y.Key] {
			return head[x.Key] > head[y.Key]
		}
		return idx[x.Key] < idx[y.Key]
	})
	last, moved := st.last[s.ID]
	for _, a := range accs {
		if moved && p.NoReturn > 0 && last.From == a.Key && now.Sub(last.At) < p.NoReturn && !resetSince(a, last.At, now) {
			continue
		}
		if p.AccountMoves > 0 && st.movedIn[a.Key] >= p.AccountMoves {
			why = "the accounts with room took their moves for now (account rate limit)"
			continue
		}
		if _, ok := room(a); ok {
			return a.Key, ""
		}
	}
	return "", why
}

// resetSince reports whether a's 5-hour window reset after t.
func resetSince(a Account, t, now time.Time) bool {
	if a.W5.Reset.IsZero() || !a.W5.Reset.After(now) {
		return true // not started since: it reset
	}
	return a.W5.Reset.Add(-5 * time.Hour).After(t)
}

// Org is the organisation part of an account key ("acct:<account>/<org>"; "" when none).
func Org(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[i+1:]
	}
	return ""
}

// reason is the move's one-line explanation.
func (p Params) reason(a Account, mv Move, why string) string {
	var b strings.Builder
	switch {
	case mv.Stopped:
		fmt.Fprintf(&b, "stopped at the %s limit (resets %s)", mv.Window, mv.ResetAt.Local().Format("15:04"))
	default:
		fmt.Fprintf(&b, "%s projected %.0f%% (cap %.0f%%) at %.1f points/h of burn", mv.Window, mv.P, p.capOf(a, mv.Window), mv.Rate)
		if !mv.HitAt.IsZero() {
			fmt.Fprintf(&b, ", 100%% at %s", mv.HitAt.Local().Format("15:04"))
		}
		if mv.MidTurn {
			b.WriteString("; mid-turn")
		}
	}
	if mv.OrgChanged {
		b.WriteString("; another organisation")
	}
	if mv.To == "" {
		b.WriteString("; not moved: " + why)
	}
	return b.String()
}

func (p Params) capOf(a Account, w string) float64 {
	c5, c7 := p.caps(a)
	if w == Win7d {
		return math.Max(0, c7-math.Max(0, a.Reserve7))
	}
	return math.Max(0, c5-math.Max(0, a.Reserve5))
}

// pinAlerts are the accounts whose pinned sessions alone take them to 100 before the window
// resets, within PinAlertWithin (setup.md 8.6, Pinned sessions): the switch cannot help (they
// never move), so the owner is told with the command to move one by hand. Pinned here means
// opted out by the owner and burning; the account's own rate (other machines) is not counted.
func (p Params) pinAlerts(in Input, byAcc map[string][]Session) []Alert {
	if p.PinAlertWithin <= 0 {
		return nil
	}
	var out []Alert
	for _, a := range in.Accounts {
		var pins []Session
		for _, s := range byAcc[a.Key] {
			if s.Pinned && s.Rate > 0 && !s.Stopped {
				pins = append(pins, s)
			}
		}
		if len(pins) == 0 {
			continue
		}
		b := a
		b.Rate, b.Reserve5, b.Reserve7 = 0, 0, 0
		pr := p.Project(b, pins, 0, in.Now)
		if pr.Hit < 0 || pr.Hit > p.PinAlertWithin {
			continue
		}
		w, reset := Win5h, pr.Reset5
		if t5 := (100 - pr.Level5) / pr.R5; t5 > pr.Reset5.Hours() || t5 > pr.Hit.Hours()+1e-9 {
			w, reset = Win7d, pr.Reset7 // the weekly window is the one it hits
		}
		sort.SliceStable(pins, func(i, j int) bool { return pins[i].Rate > pins[j].Rate })
		al := Alert{Session: pins[0].ID, Account: a.Key, Window: w, P: pr.pOf(w), Rate: pr.R5, HitAt: in.Now.Add(pr.Hit),
			ResetAt: in.Now.Add(reset), In: pr.Hit, Level: pr.Level5}
		if w == Win7d {
			al.Level = pr.Level7
		}
		out = append(out, al)
	}
	return out
}

// Destination is the account a move of session id would go to now (wy move without an
// account), or "" and why none can take it.
func Destination(in Input, id string) (string, string) {
	for _, s := range in.Sessions {
		if s.ID == id {
			return in.Params.destination(in, s, in.Params.newMoveState(in), map[string]float64{})
		}
	}
	return "", "the session is not known"
}

// DestFloor is the shortest horizon a destination is judged on (Params.DestHorizon).
const DestFloor = time.Hour

// reRead is s's re-read cost: its context (at least 20,000 tokens) × its model factor.
func reRead(s Session) float64 {
	f := s.ModelFactor
	if f <= 0 {
		f = 3
	}
	return math.Max(s.Context, 20_000) * f
}

// ModelFactor is a model's re-read price relative to haiku: opus 5, sonnet 3, haiku 1 (0:
// unknown).
func ModelFactor(model string) float64 {
	switch m := strings.ToLower(model); {
	case strings.Contains(m, "opus"):
		return 5
	case strings.Contains(m, "sonnet"):
		return 3
	case strings.Contains(m, "haiku"):
		return 1
	}
	return 0
}
