package mover

import (
	"fmt"
	"github.com/mraj602/watchyard/internal/projection"
	"math"
	"slices"
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
	// ExpiringRoom: within this long of a protected account's weekly reset its weekly cap is
	// Cap7 (its 5-hour cap stays Cap5Protected): its room left would expire unused (setup.md
	// 8.6, Expiring room). 0: never.
	ExpiringRoom time.Duration
	// StaleAfter: an account whose reading (Account.ReadAt) is older than this is neither a
	// trigger source nor a destination (setup.md 8.6, Stale data); StaleAlert: the owner is
	// alerted once when it lasts longer. 0: off.
	StaleAfter, StaleAlert time.Duration
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
	// UrgentWithin: a source that will hit 100% within this (or has reached a cap, or whose
	// session is stopped at a limit) is urgent: the account rate limit does not hold its moves
	// back and its highest burner goes first. 0: never urgent.
	UrgentWithin time.Duration
	// Cooldown: a session moves at most once within it. NoReturn: never back to the account it
	// left within it, unless that account's 5-hour window reset since.
	Cooldown time.Duration
	NoReturn time.Duration
	// AccountMoves: at most this many moves into one account per AccountWindow, counting only
	// moves of sessions burning at least MinBurn; AccountBurn: at most this much moved burn
	// (points/h) into one account per AccountWindow (0: no limit; one move always fits). Both
	// are anti-churn limits: an aggressive account with room takes a move over them rather
	// than a protected one taking it.
	AccountMoves  int
	AccountBurn   float64
	AccountWindow time.Duration
	// oldChurn: the limits as before (every move counts, no burn limit, no aggressive
	// account over them before a protected one); the simulator's comparison row.
	oldChurn bool
	// StoppedWait: a session a limit stopped moves only when that window resets at least this
	// far ahead (nearer, waiting costs less than a restart and a re-read).
	StoppedWait time.Duration
	// StepWait: a mid-turn move waits at most this long for the step in flight to end (or
	// less, when the hit comes sooner); then it kills inside the step.
	StepWait time.Duration
	// PassWait: one pass waits at most this long in all for its mid-turn moves (side by side);
	// a session still inside its step then waits on in the next pass, until StepWait in all.
	PassWait time.Duration
	// OwnerGrace: an interactive session the owner typed into this recently is left alone (0:
	// off, the default; the simulator compares 2 minutes).
	OwnerGrace time.Duration
	// Orgs: which organisations a session may move to: any, same-first or same.
	Orgs string
	// IdleMoves: idle sessions with no burn are candidates too (a simulator variant only).
	IdleMoves bool
	// DestHorizon: the simulator's comparison row only (oldDest): an aggressive destination
	// qualified when the moved session fit under its caps less Hysteresis for min(its expected
	// remaining time (DestHorizon when unknown), the 5-hour reset), at least DestFloor. The
	// switch itself judges a destination with the trigger's projection (to the resets), so a
	// move is not undone by the next trigger (live: 3b2611ed moved onto claude-6 at 06:12 and
	// off it at 06:35, "5h projected 111%").
	DestHorizon time.Duration
	oldDest     bool
	// noYoungDest (simulator): the destination counts young lanes at 0, as 1.3.3-pre9.
	noYoungDest bool
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
	// MinBurn: a running session is a candidate only when its own measured burn (points per
	// hour over the burn window) is at least this; busy with no burn frees nothing (live
	// 2026-11-25: a mid-turn session at 0.0 points/h was moved). 0: any burn above 0;
	// negative: none, a busy session with no burn too (the simulator's tables before it).
	MinBurn float64
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
	Cap5: 95, Cap7: 98, Cap5Protected: 80, Cap7Protected: 80, ExpiringRoom: 3 * time.Hour,
	StaleAfter: 10 * time.Minute, StaleAlert: 30 * time.Minute,
	Hysteresis: 5, BurnWindow: 10 * time.Minute, Pass: time.Minute, WeekSpan: 5 * time.Hour,
	SpikePasses: 2, ReadingPasses: 3, SpikeNear: 30 * time.Minute, UrgentWithin: time.Hour,
	Cooldown: 20 * time.Minute, NoReturn: time.Hour, AccountMoves: 5, AccountBurn: 40, AccountWindow: 15 * time.Minute,
	StoppedWait: 20 * time.Minute, StepWait: 3 * time.Minute, PassWait: 90 * time.Second, Orgs: OrgsAny,
	PinReserve5: 10, PinReserve7: 5, PinAlertWithin: 2 * time.Hour,
	CheapMoves: true, YoungGate: 15 * time.Minute, MinBurn: 1,
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
	// ReadAt: when the reading its levels start from was taken (zero: not known). Older than
	// Params.StaleAfter, the account is neither a source nor a destination.
	ReadAt time.Time
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
	// Pinned: the owner pinned it in Claude Code (its folder's jobs/pins.json) or it is outside
	// the scope. PinFrom says where the owner's pin is ("in Claude Code"; "" when none).
	Pinned  bool
	PinFrom string
	// Movable: its kind can be restarted (false for claude.ai --sdk-url sessions).
	Movable bool
	// Copies: other live processes of the session; never moved while there are any.
	Copies int
	// Moving: a move of it (another pass, or by hand) holds its lock now.
	Moving bool
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
	// MovedIn: GateYoung found it moved onto its account within YoungGate: its burn counts at
	// once (at the rate it moved with), so it is never young.
	MovedIn bool
}

func (s Session) burned() bool { return s.Busy || s.Rate > 0 }

// Record is a move that happened (cooldown, no return, the account rate limit).
type Record struct {
	Session  string
	From, To string
	At       time.Time
	// Rate: its burn when it moved; RateKnown false for an older log line (counted as a move
	// at the floor and over).
	Rate      float64
	RateKnown bool
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
	// LaneRate: the burn the picker counts a new lane at (points per hour; its switch check,
	// setup.md 8.5). The destination check counts each lane under YoungGate (its burn left
	// out of the trigger's projection) at it, not 0: in a few minutes it will count. 0: at
	// its measured burn.
	LaneRate float64
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
	// Expiring: protected, within ExpiringRoom of its weekly reset: Cap7 is the aggressive cap.
	Expiring bool `json:",omitempty"`
	// StaleFor: its reading is this old, over Params.StaleAfter: no moves off or onto it.
	StaleFor time.Duration `json:",omitempty"`
	// Left: it stays over its target after this pass's moves because no move can lower it
	// further ("only pinned burn left ...").
	Left  string `json:",omitempty"`
	ratio float64
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
	// Urgent: its source will hit 100% within UrgentWithin, has reached a cap or the session is
	// stopped: the account rate limit did not hold it back (and a best-available destination,
	// BestFit long, may take it when none has room to its reset).
	Urgent  bool
	BestFit time.Duration `json:",omitempty"`
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
	return p.YoungGate > 0 && !s.MovedIn && !s.Started.IsZero() && now.Sub(s.Started) < p.YoungGate
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
	// A session moved here within YoungGate is young by its new lease, but its burn is known:
	// the rate it moved with counts (else the account looked empty for 15 minutes and took
	// more moves the trigger then undid; live: ai-lane-C2 moved onto claude-6 at 06:10, then
	// 3b2611ed at 06:12, both off again at 06:35).
	movedIn := map[string]float64{}
	for _, r := range in.Recent {
		if r.RateKnown && r.To != "" && in.Now.Sub(r.At) < p.YoungGate {
			movedIn[r.Session+" "+r.To] = r.Rate
		}
	}
	sub, add := map[string]float64{}, map[string]float64{}
	var ss []Session
	for i, s := range in.Sessions {
		known, moved := movedIn[s.ID+" "+s.Account]
		// A moved session is not new: its burn counts at once, whatever its age (live pre9:
		// 54c4e1e8, moved onto claude-6 at 10:35 at 21.9 points/h, "stays on claude-6: under
		// 15 min old" at 10:47 while claude-6 was over its cap).
		if !moved && (!p.young(s, in.Now) || s.Rate <= 0) {
			continue
		}
		if ss == nil {
			ss = append([]Session(nil), in.Sessions...)
		}
		if moved {
			// Its measured burn since the resume (the re-read) is left out; the burn it moved
			// with is counted in its place.
			if !s.Stopped {
				sub[s.Account] += s.Rate
				add[s.Account] += known
			}
			ss[i].YoungRate, ss[i].Rate, ss[i].MovedIn = s.Rate, known, true
			continue
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
		x, y := sub[a.Key], add[a.Key]
		if (x <= 0 && y <= 0) || a.FromReadings {
			continue
		}
		as[i].Rate, as[i].Meter = math.Max(0, a.Rate-x+y), math.Max(0, a.Meter-x+y)
		var note string
		if x-y > 0 || y <= 0 {
			note = fmt.Sprintf("%.1f points/h of sessions under %d min left out (a session's first minutes burn far above its steady rate)",
				x, int(p.YoungGate.Minutes()))
		}
		if y > 0 {
			if note != "" {
				note += "; "
			}
			note += fmt.Sprintf("%.1f points/h of sessions moved here in the last %d min counted at the burn they moved with", y, int(p.YoungGate.Minutes()))
		}
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
		if age := p.staleFor(a, now); age > 0 {
			// Its level rests on an old reading (live: the hub away, "5h 100%" from a reading
			// 3 h 37 min old while the account was at ~68%): nothing moves off it on that.
			pr.StaleFor, pr.Triggered, pr.Held = age, false, false
			delete(res.Streak, a.Key+" "+pr.Window)
		}
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
		if (proj[a.Key].Window != "" && proj[a.Key].StaleFor == 0) || hasStopped(byAcc[a.Key]) {
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
			p.place(in, &res, st, s, a, mv, delta, skip, urgency{near: true, hit: 0})
		}
		if !pr.Triggered {
			if pr.Held {
				for _, s := range ss {
					if s.burned() && !s.Stopped && s.Pinned {
						skip(s, p.excluded(s, st, now))
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
		var held float64 // counted burn of sessions held back for now (cooldown, a move under way)
		for _, s := range ss {
			if s.Stopped {
				continue
			}
			if !s.burned() && !p.IdleMoves {
				continue // idle with no burn: moving it frees nothing
			}
			if !p.IdleMoves && p.MinBurn >= 0 {
				switch {
				case p.young(s, now):
					// Its burn is left out (GateYoung): moving it would free nothing counted.
					skip(s, fmt.Sprintf("under %d min old: its burn does not count yet", int(p.YoungGate.Minutes())))
					continue
				case s.Rate <= 0 || s.Rate < p.MinBurn:
					skip(s, fmt.Sprintf("burns %.1f points/h, under the %.1f a move must free", s.Rate, p.MinBurn))
					continue
				}
			}
			if why := p.excluded(s, st, now); why != "" {
				if l, ok := st.last[s.ID]; !s.Pinned && (s.Moving || (ok && p.Cooldown > 0 && now.Sub(l.At) < p.Cooldown)) {
					held += s.Rate // not the owner grace: it moves another burner on purpose
				}
				skip(s, why)
				continue
			}
			cands = append(cands, s)
		}
		// A soft trigger (no hit within UrgentWithin) whose burn is mostly a session held back
		// for now waits for it rather than moving the small ones, which cannot bring the source
		// under its caps (live pre9: home-18 at 2.3 points/h moved off claude-2 at 10:12 while
		// ai-lane-E, 27.1, cooled down; home-00 at 1.7 off claude-6 at 10:37 while 54c4e1e8,
		// 21.9, moved there at 10:35). A near hit moves what it can, the highest burner first.
		if p.UrgentWithin > 0 && held > 0 && len(cands) > 0 {
			cur := p.Project(a, ss, delta[a.Key], now)
			var rest float64
			for _, s := range cands {
				rest += s.Rate
			}
			if u := p.urgencyOf(cur); !u.near && held > rest && !cur.enoughWithout(p, rest) {
				for _, s := range cands {
					skip(s, fmt.Sprintf("waiting: moving the others (%.1f points/h) would not bring %s under its caps while %.1f points/h held back for now burns more; it moves if a hit comes within %d min",
						rest, a.Label, held, int(p.UrgentWithin.Minutes())))
				}
				cands = nil
			}
		}
		// Urgency is the source's as it triggered this pass (not re-read after each move: one
		// big move pushes the hit out past UrgentWithin while the account is still headed
		// over 100% before its reset).
		var srcUrg *urgency
		for len(cands) > 0 {
			cur := p.Project(a, ss, delta[a.Key], now)
			if srcUrg == nil {
				u := p.urgencyOf(cur)
				srcUrg = &u
			}
			if !p.overTarget(cur) {
				for _, s := range cands {
					skip(s, fmt.Sprintf("enough moved: %s projected 5h %.0f%%, 7d %.0f%%", a.Label, cur.P5, cur.P7))
				}
				break
			}
			urgent := srcUrg.near
			sort.SliceStable(cands, func(i, j int) bool {
				x, y := cands[i], cands[j]
				ex, ey := cur.enoughWithout(p, x.Rate), cur.enoughWithout(p, y.Rate)
				if ex != ey {
					return ex // the fewest moves: one that brings it under alone first
				}
				if urgent && x.Rate != y.Rate {
					// A hit is near: the highest burner first (live pre9 10:31: claude-4 at 99%,
					// 100% at ~10:54; only a 2.7 points/h session moved, the 25.9 and 16.9 stayed).
					return x.Rate > y.Rate
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
			// Greedy: a pick that would not lower the source's projection frees nothing.
			if !p.IdleMoves && p.MinBurn >= 0 && !p.lowers(cur, s.Rate) {
				for _, x := range cands {
					skip(x, fmt.Sprintf("moving it would not lower %s's projection", a.Label))
				}
				break
			}
			cands = cands[1:]
			mv := Move{Session: s.ID, From: a.Key, Window: cur.Window, P: cur.pOf(cur.Window), ResetAt: now.Add(cur.resetOf(cur.Window)),
				MidTurn: s.Busy, Rate: s.Rate, Interactive: s.Interactive}
			if mv.Window == "" {
				mv.Window, mv.P, mv.ResetAt = pr.Window, pr.pOf(pr.Window), now.Add(pr.resetOf(pr.Window))
			}
			mv.HitAt = cur.HitAt(now)
			p.place(in, &res, st, s, a, mv, delta, skip, *srcUrg)
		}
		if p.MinBurn >= 0 && !p.IdleMoves {
			if cur := p.Project(a, ss, delta[a.Key], now); p.overTarget(cur) {
				// Nothing left that a move would lower: say what burns on.
				var pinned, low float64
				for _, s := range ss {
					switch {
					case s.Stopped || s.Rate <= 0:
					case s.Pinned:
						pinned += s.Rate
					default:
						if !slices.ContainsFunc(res.Moves, func(m Move) bool { return m.Session == s.ID && m.To != "" }) {
							low += s.Rate
						}
					}
				}
				if low < p.MinBurn {
					for i := range res.Accounts {
						if res.Accounts[i].Key == a.Key {
							res.Accounts[i].Left = fmt.Sprintf("only pinned burn left (%.1f points/h pinned, %.1f unpinned)", pinned, low)
						}
					}
				}
			}
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
	case s.Pinned && s.PinFrom != "":
		return "pinned (" + s.PinFrom + ")"
	case s.Pinned:
		return "pinned, or outside the scope"
	case !s.Movable:
		return "its kind cannot be restarted from this machine (claude.ai)"
	case s.Moving:
		return "a move of it is under way (waiting for its step)"
	case s.Copies > 0:
		return fmt.Sprintf("%d copies of it are running: stop the extra one first", s.Copies+1)
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
func (p Params) place(in Input, res *Result, st *moveState, s Session, a Account, mv Move, delta map[string]float64, skip func(Session, string), u urgency) {
	to, why := p.destination(in, s, st, delta, u)
	mv.To, mv.Urgent = to, u.near
	if to != "" && strings.HasPrefix(why, "best available") {
		mv.BestFit = st.bestFit
	}
	if to != "" {
		mv.OrgChanged = Org(to) != a.Org()
		if p.countsAsMove(s.Rate) {
			st.movedIn[to]++
		}
		st.burnIn[to] += math.Max(0, s.Rate)
		delta[to] += s.Rate
		if !s.Stopped {
			delta[a.Key] -= s.Rate
		}
	}
	mv.Reason = p.reason(a, mv, why, in.Now)
	res.Moves = append(res.Moves, mv)
}

// overBy reports whether a projection is over its caps less m.
// lowers reports whether taking rate off the account lowers its projection in the window
// over the cap (both when neither is named).
func (p Params) lowers(pr Projection, rate float64) bool {
	if rate <= 0 {
		return false
	}
	switch pr.Window {
	case "5h":
		return rate*pr.Reset5.Hours() > 0
	case "7d":
		return rate/pr.ratio*math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours()) > 0
	}
	return true
}

func (pr Projection) overBy(m float64) bool {
	return pr.P5 > pr.Cap5-m || pr.P7 > pr.Cap7-m || (m == 0 && pr.Hard)
}

// margins are how far under each cap the moves aim: Hysteresis, but none for a window whose
// level now is already at or over its cap less Hysteresis (no move takes a projection below
// the level now, so that target could never be met and every burner would be moved off). With
// MinBurn negative (the simulator's tables before it), always Hysteresis.
func (p Params) margins(pr Projection) (m5, m7 float64) {
	m5, m7 = p.Hysteresis, p.Hysteresis
	if p.MinBurn < 0 || p.IdleMoves {
		return m5, m7
	}
	if pr.Level5 >= pr.Cap5-p.Hysteresis {
		m5 = 0
	}
	if pr.Level7 >= pr.Cap7-p.Hysteresis {
		m7 = 0
	}
	return m5, m7
}

// overTarget reports whether a projection is still over what the moves aim for (margins).
func (p Params) overTarget(pr Projection) bool {
	m5, m7 := p.margins(pr)
	return pr.P5 > pr.Cap5-m5 || pr.P7 > pr.Cap7-m7
}

// enoughWithout reports whether taking rate off the account brings both projections to
// their targets (margins).
func (pr Projection) enoughWithout(p Params, rate float64) bool {
	h5 := pr.Reset5.Hours()
	h7 := math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours())
	ratio := pr.ratio
	m5, m7 := p.margins(pr)
	return pr.P5-rate*h5 <= pr.Cap5-m5 && pr.P7-rate/ratio*h7 <= pr.Cap7-m7
}

func (pr Projection) pOf(w string) float64 {
	if w == Win7d {
		return pr.P7
	}
	return pr.P5
}

// staleFor is how old a's reading is when that is over StaleAfter (0: fresh, unknown or off).
func (p Params) staleFor(a Account, now time.Time) time.Duration {
	if p.StaleAfter <= 0 || a.ReadAt.IsZero() {
		return 0
	}
	if age := now.Sub(a.ReadAt); age > p.StaleAfter {
		return age
	}
	return 0
}

// StaleAccounts are the accounts of r whose data has been stale for longer than StaleAlert.
func (p Params) StaleAccounts(r Result) []Projection {
	var out []Projection
	for _, pr := range r.Accounts {
		if p.StaleAlert > 0 && pr.StaleFor > p.StaleAlert {
			out = append(out, pr)
		}
	}
	return out
}

// HitAt is when the projection reaches 100% from its current level at its burn (now +
// (100 − level)/R5, the sooner of the two windows), or zero when that is not in the future:
// a level already at 100 has no hit time to give (live 2026-11-25: "100% at 04:24" in a pass
// at 04:25).
func (pr Projection) HitAt(now time.Time) time.Time {
	if pr.Hit <= 0 {
		return time.Time{}
	}
	return now.Add(pr.Hit)
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
	// burnIn: the burn moved into each account within the account window (recent and planned).
	burnIn map[string]float64
	// bestFit: how long the last best-available destination fits (destination → place).
	bestFit time.Duration
}

func (p Params) newMoveState(in Input) *moveState {
	st := &moveState{last: map[string]Record{}, movedIn: map[string]int{}, burnIn: map[string]float64{}}
	for _, r := range in.Recent {
		if p.AccountWindow > 0 && in.Now.Sub(r.At) < p.AccountWindow {
			if !r.RateKnown || p.countsAsMove(r.Rate) {
				st.movedIn[r.To]++
			}
			st.burnIn[r.To] += math.Max(0, r.Rate)
		}
		if l, ok := st.last[r.Session]; !ok || r.At.After(l.At) {
			st.last[r.Session] = r
		}
	}
	return st
}

// caps are the caps of account a at now: a protected account's weekly cap is Cap7 within
// ExpiringRoom of its weekly reset (expiring).
func (p Params) caps(a Account, now time.Time) (c5, c7 float64) {
	if a.Protected {
		if p.expiring(a, now) {
			return p.Cap5Protected, math.Max(p.Cap7, p.Cap7Protected)
		}
		return p.Cap5Protected, p.Cap7Protected
	}
	return p.Cap5, p.Cap7
}

// expiring: a is protected and its weekly reset is within ExpiringRoom.
func (p Params) expiring(a Account, now time.Time) bool {
	return a.Protected && p.ExpiringRoom > 0 && a.W7.Reset.After(now) && a.W7.Reset.Sub(now) <= p.ExpiringRoom
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
	c5, c7 := p.caps(a, now)
	c5, c7 = math.Max(0, c5-math.Max(0, a.Reserve5)), math.Max(0, c7-math.Max(0, a.Reserve7))
	pr := Projection{Key: a.Key, Label: a.Label, Protected: a.Protected, Level5: a.W5.Level, Level7: a.W7.Level, R5: r5, RateNote: a.RateNote,
		Cap5: c5, Cap7: c7, Hit: -1, ratio: ratio(a), Pins: a.Pins, Reserve5: math.Max(0, a.Reserve5), Reserve7: math.Max(0, a.Reserve7)}
	pr.Expiring = p.expiring(a, now)
	pr.Reset5 = resetIn(a.W5, now, 5*time.Hour)
	pr.Reset7 = resetIn(a.W7, now, 7*24*time.Hour)
	if a.W5.Reset.IsZero() || !a.W5.Reset.After(now) {
		pr.Level5 = 0 // not started (or reset since the reading)
	}
	h7 := math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours())
	pr.P5, pr.P7 = projection.Levels(pr.Level5, pr.Level7, r5, pr.ratio, pr.Reset5.Hours(), pr.Reset7.Hours(), p.WeekSpan.Hours())
	if p.MinBurn >= 0 && !p.IdleMoves {
		// A session expected to end sooner burns only until then: a short lane cannot burn
		// the rest of the window (live: an idle account, a 25-minute lane at 23 points/h,
		// projected 115%). Unknown (0) or overrun: the whole window, as before.
		for _, s := range ss {
			if s.Stopped || s.Expected <= 0 || s.Rate <= 0 {
				continue
			}
			e := max(s.Expected, ExpectFloor).Hours()
			pr.P5 -= s.Rate * math.Max(0, pr.Reset5.Hours()-e)
			pr.P7 -= s.Rate / pr.ratio * math.Max(0, math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours())-e)
		}
	}
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
func (p Params) destination(in Input, s Session, st *moveState, delta map[string]float64, u urgency) (string, string) {
	now := in.Now
	from := Org(s.Account)
	var accs []Account
	why := "no logged-in account on this machine has room for its burn"
	for _, a := range in.Accounts {
		if a.Key == s.Account || !a.LoggedIn || a.Stale || a.Excluded || p.staleFor(a, in.Now) > 0 {
			continue
		}
		if p.Orgs == OrgsSame && a.Org() != from {
			why = "no account of its organisation has room (account.move_orgs = same)"
			continue
		}
		accs = append(accs, a)
	}
	// Lanes under YoungGate count at the lane burn (else their measured burn), not 0: the
	// trigger leaves out their first minutes but counts them 15 minutes in, and a move
	// accepted onto an account of fresh lanes was then undone (live pre9: ai-lane-E, 27.1
	// points/h, moved claude-6 → claude-2 at 10:00:58 while claude-2 had 6.5-8.7 points/h of
	// young lanes left out; they burned 60-90 points/h by 10:10, "5h projected 95%").
	byAcc := map[string][]Session{}
	young := map[string]float64{}
	fromReadings := map[string]bool{} // the readings' slope has their burn already
	for _, a := range in.Accounts {
		fromReadings[a.Key] = a.FromReadings
	}
	for _, x := range in.Sessions {
		if !p.oldDest && !p.noYoungDest && p.young(x, now) && x.Rate <= 0 && !x.Stopped && !fromReadings[x.Account] {
			r := x.YoungRate
			if in.LaneRate > 0 {
				r = in.LaneRate
			}
			x.Rate = r
			young[x.Account] += r
		}
		byAcc[x.Account] = append(byAcc[x.Account], x)
	}
	// The trigger's projection of the account after the move (Project: to the resets, the
	// moves planned this pass in delta), the moved session at its burn until its expected end
	// as the trigger counts it there, Hysteresis under the caps: the next pass's trigger does
	// not fire on it for this move.
	room := func(a Account) (Projection, bool) {
		r := math.Max(0, s.Rate)
		a.Rate += young[a.Key]
		pr := p.Project(a, byAcc[a.Key], delta[a.Key]+r, now)
		if p.MinBurn >= 0 && !p.IdleMoves && s.Expected > 0 && r > 0 {
			e := max(s.Expected, ExpectFloor).Hours()
			pr.P5 -= r * math.Max(0, pr.Reset5.Hours()-e)
			pr.P7 -= r / pr.ratio * math.Max(0, math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours())-e)
		}
		return pr, pr.P5 <= pr.Cap5-p.Hysteresis && pr.P7 <= pr.Cap7-p.Hysteresis && pr.Level5 < pr.Cap5-p.Hysteresis &&
			pr.Level7 < pr.Cap7-p.Hysteresis
	}
	if p.DestHorizon > 0 && p.oldDest {
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
	limited := func(a Account) bool {
		if u.near {
			return false // the rate limit holds back only soft triggers, not a near hit
		}
		if p.AccountMoves > 0 && st.movedIn[a.Key] >= p.AccountMoves && p.countsAsMove(s.Rate) {
			return true
		}
		return !p.oldChurn && p.AccountBurn > 0 && st.burnIn[a.Key] > 0 && st.burnIn[a.Key]+math.Max(0, s.Rate) > p.AccountBurn
	}
	noReturn := func(a Account) bool {
		return moved && p.NoReturn > 0 && last.From == a.Key && now.Sub(last.At) < p.NoReturn && !resetSince(a, last.At, now)
	}
	for i, a := range accs {
		if noReturn(a) {
			continue
		}
		if limited(a) {
			why = "the accounts with room took their moves for now (account rate limit)"
			continue
		}
		if a.Protected && !p.oldChurn {
			// Before a protected account: an aggressive one with room that only the anti-churn
			// limits held back takes it (its projection check is the real guard).
			for _, b := range accs[:i] {
				if !b.Protected && !noReturn(b) && limited(b) {
					if _, ok := room(b); ok {
						return b.Key, ""
					}
				}
			}
		}
		if _, ok := room(a); ok {
			return a.Key, ""
		}
	}
	// Only aggressive accounts were left, held back by the limits: the one with room takes it.
	for _, b := range accs {
		if p.oldChurn {
			break
		}
		if !b.Protected && !noReturn(b) && limited(b) {
			if _, ok := room(b); ok {
				return b.Key, ""
			}
		}
	}
	// None has room for it to its reset, but the source will hit 100% before its own: the
	// account that stays under its caps the longest with it takes it, when that is at least
	// as long as the source has left (and ExpectFloor): a move that buys 2 hours beats a hit
	// in 20 minutes (live pre9 10:31: claude-4 at 99%, its 25.9 and 16.9 points/h burners
	// stayed, "no logged-in account on this machine has room for its burn", while claude-6
	// was at 16% and claude-2 at 34%). Only for an urgent source (a soft trigger waits: a
	// destination over its caps by its reset triggers there in turn), and only onto an
	// aggressive account (a protected one keeps its room).
	if u.near && !p.oldChurn && !p.oldDest {
		need := max(u.hit, ExpectFloor)
		best, bestFit, label := "", time.Duration(0), ""
		for _, b := range accs {
			if b.Protected || noReturn(b) {
				continue
			}
			pr, _ := room(b)
			if f := p.fitFor(pr); f >= need && f > bestFit {
				best, bestFit, label = b.Key, f, b.Label
			}
		}
		if best != "" {
			st.bestFit = bestFit
			return best, fmt.Sprintf("best available: %s fits for %s (no account has room for it to its reset; this one stays under its caps longer than the %s %s has left)",
				label, fmtDur(bestFit), fmtDur(u.hit), "the source")
		}
	}
	return "", why
}

// urgency is how pressing a source's moves are: near (a hit within UrgentWithin, a cap reached
// now, or a stopped session) lifts the account rate limit and puts the highest burner first;
// hit is the time to its projected 100% (-1: none before its resets), for the best-available
// destination.
type urgency struct {
	near bool
	hit  time.Duration
}

func (p Params) urgencyOf(pr Projection) urgency {
	u := urgency{hit: pr.Hit}
	if pr.Hard || pr.Level5 >= 100 || pr.Level7 >= 100 {
		u.hit = 0
	}
	u.near = p.UrgentWithin > 0 && u.hit >= 0 && u.hit < p.UrgentWithin
	return u
}

// fitFor is how long an account's projection stays under its caps (its burn to the resets as
// the projection counts it, spread evenly): the full time to its reset when it fits to it.
func (p Params) fitFor(pr Projection) time.Duration {
	fit := time.Duration(math.MaxInt64)
	one := func(level, proj, cap, h float64) {
		if h <= 0 {
			return
		}
		if level >= cap {
			fit = 0
			return
		}
		r := (proj - level) / h
		if r <= 0 || proj <= cap {
			return
		}
		if t := time.Duration((cap - level) / r * float64(time.Hour)); t < fit {
			fit = t
		}
	}
	one(pr.Level5, pr.P5, pr.Cap5, pr.Reset5.Hours())
	one(pr.Level7, pr.P7, pr.Cap7, math.Min(pr.Reset7.Hours(), p.WeekSpan.Hours()))
	return fit
}

// fmtDur is a duration as "2h10m" (minutes only under an hour).
func fmtDur(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	d = d.Round(time.Minute)
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// countsAsMove: a move of a session burning rate counts against AccountMoves (one under the
// burn floor frees nothing and is not churn worth limiting; with MinBurn negative all count).
func (p Params) countsAsMove(rate float64) bool {
	return p.oldChurn || p.MinBurn < 0 || rate >= p.MinBurn
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

// Clock is t as the owner reads it in notes, notices, reasons and alerts: "15:04" within 24
// hours of now, else with its weekday ("Thu 03:30"), and its date beyond 6 days ("Dec 2 03:30").
func Clock(t, now time.Time) string {
	t = t.Local()
	switch d := t.Sub(now); {
	case d > -24*time.Hour && d < 24*time.Hour:
		return t.Format("15:04")
	case d > -6*24*time.Hour && d < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("Jan 2 15:04")
}

// reason is the move's one-line explanation.
func (p Params) reason(a Account, mv Move, why string, now time.Time) string {
	var b strings.Builder
	switch {
	case mv.Stopped:
		fmt.Fprintf(&b, "stopped at the %s limit (resets %s)", mv.Window, Clock(mv.ResetAt, now))
	default:
		fmt.Fprintf(&b, "%s projected %.0f%% (cap %.0f%%) at %.1f points/h of burn", mv.Window, mv.P, p.capOf(a, mv.Window, now), mv.Rate)
		if mv.HitAt.After(now) {
			fmt.Fprintf(&b, ", 100%% at %s", Clock(mv.HitAt, now))
		}
		if mv.MidTurn {
			b.WriteString("; mid-turn")
		}
	}
	if mv.OrgChanged {
		b.WriteString("; another organisation")
	}
	switch {
	case mv.To == "":
		b.WriteString("; not moved: " + why)
	case why != "":
		b.WriteString("; " + why)
	}
	return b.String()
}

func (p Params) capOf(a Account, w string, now time.Time) float64 {
	c5, c7 := p.caps(a, now)
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
			return in.Params.destination(in, s, in.Params.newMoveState(in), map[string]float64{}, urgency{hit: -1})
		}
	}
	return "", "the session is not known"
}

// DestFloor is the shortest horizon a destination was judged on (Params.DestHorizon, the
// simulator's comparison row).
const DestFloor = time.Hour

// ExpectFloor: a session inside its expected duration (its lease's --expect) burns, as the
// switch's trigger and destination check and the picker's switch-projection check all count
// it, for max(its expected remaining time, ExpectFloor), at most to each window's horizon
// (the 5-hour reset; min(the weekly reset, WeekSpan)); once it overruns (or with none
// expected), to the full horizon. One rule, so the switch never undoes what the picker or the
// switch itself just did (live pre7: 2de58a08 leased on claude-3 at 07:36, moved off at
// 07:53).
const ExpectFloor = 30 * time.Minute

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
