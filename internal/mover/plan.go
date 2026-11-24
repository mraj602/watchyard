package mover

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Params are the mover's rules (setup.md 8.6, Decision). The strategies M0-M4 are Params
// values; DefaultParams is the one the auto-mover uses.
type Params struct {
	Name string
	// Off: never move (M0).
	Off bool
	// Fixed5h > 0: the fixed-threshold rule (M1): move when the measured 5-hour level is at or
	// over it, whatever the forecast.
	Fixed5h float64
	// Horizon: act on a forecast hit only when it is this close (0: any time before the
	// reset). Bursty spikes far ahead do not move anything yet.
	Horizon time.Duration
	// Slack: after the moves the source's forecast must stay this many points under its
	// limit until its reset.
	Slack float64
	// Hysteresis: a destination's forecast with the session added must stay this many points
	// under its limit until its own reset (both windows).
	Hysteresis float64
	// CostAware: choose the sessions by the room they free per point the move costs (their
	// context re-read uncached), idle ones first; else the highest burn first.
	CostAware bool
	// MoveCostPerMTok: 5-hour points a move costs per million context tokens (the first turn
	// on the new account reads the whole context uncached). MoveCostFixed is added per move.
	MoveCostPerMTok float64
	MoveCostFixed   float64
	// MidTurn: what to do with a session in the middle of a turn. false: never move it before
	// it stops; when the hit is less than WaitHit away it is planned as a wait (moved as soon
	// as the limit stops it or its turn ends). true: move it at once (M1, M2).
	MidTurn bool
	WaitHit time.Duration
	// Cooldown: no session moves twice within it. NoReturn: never back to the account it
	// left within it (no ping-pong).
	Cooldown time.Duration
	NoReturn time.Duration
	// AccountMoves moves at most into one account per AccountWindow (0: no limit).
	AccountMoves  int
	AccountWindow time.Duration
	// BurstWeight: how much of a session's helper burst counts in its rate.
	BurstWeight float64
	// MaxPerPass: at most this many moves per account per pass (0: as many as needed).
	MaxPerPass int
	// TokenRules: the token-optimal rules of Smart Account Switch: sessions whose prompt cache
	// is already cold (idle longer than CacheTTL) first, as their move costs only MoveCostFixed;
	// then small contexts; never a session the owner is typing into (Attended) or one expected
	// to finish before the hit; a session moves once per 5-hour window; and a move must save
	// more than it costs (Benefit).
	TokenRules bool
	CacheTTL   time.Duration
	// OncePer: with TokenRules, a session moves at most once within it (the 5-hour window).
	OncePer time.Duration
	// StallValue: the owner's time, 5-hour points per lane-hour stalled at a limit, added to the
	// session's own burn in the benefit test.
	StallValue float64
	// DrainFirst: among destinations with room, the one whose 5-hour window resets soonest
	// first (its room expires unused otherwise), instead of the picker's order.
	DrainFirst bool
	// LindyFrac: a session without an estimate is expected to run this many times its age
	// again (0: 1, the Lindy rule).
	LindyFrac float64
}

// The strategies compared in the simulator (setup.md 8.6, Strategies).
var (
	// M0: no moves.
	M0 = Params{Name: "M0 no moves", Off: true}
	// M1: a fixed threshold, 5-hour level at or over 92: move the highest-burn session.
	M1 = Params{Name: "M1 fixed 5h 92%", Fixed5h: 92, MidTurn: true, MaxPerPass: 1, BurstWeight: 0}
	// M2: predictive: the forecast hits before the reset; the highest-burn sessions move,
	// to an account whose forecast with them does not hit before its reset.
	M2 = Params{Name: "M2 predictive", MidTurn: true}
	// M3: M2 with cost-aware selection, slack and hysteresis, a cooldown, no ping-pong, an
	// account rate limit and no mid-turn moves.
	M3 = Params{Name: "M3 M2 + cost, hysteresis, cooldown", Slack: 5, Hysteresis: 10, CostAware: true,
		MoveCostPerMTok: 1.2, MoveCostFixed: 0.2, WaitHit: 5 * time.Minute, Cooldown: 30 * time.Minute,
		NoReturn: 2 * time.Hour, AccountMoves: 3, AccountWindow: 30 * time.Minute}
	// M4 (Smart Account Switch, the default): M3 with the helper burst in the rate, a 90-minute
	// horizon (a far forecast hit waits for the next readings), 5 points of hysteresis instead
	// of 10 (more accounts qualify as destinations) and the token rules.
	M4 = Params{Name: "M4 Smart Account Switch", Horizon: 90 * time.Minute, Slack: 5, Hysteresis: 5,
		CostAware: true, MoveCostPerMTok: 1.2, MoveCostFixed: 0.2, WaitHit: 5 * time.Minute, Cooldown: 30 * time.Minute,
		NoReturn: 2 * time.Hour, AccountMoves: 3, AccountWindow: 30 * time.Minute, BurstWeight: 0.5,
		TokenRules: true, CacheTTL: 5 * time.Minute, OncePer: 5 * time.Hour, StallValue: 5}
	// M5 (the default since the end-to-end pass): M4 with the prompt cache taken as cold only
	// after an hour (a subscription keeps the main conversation's cache for an hour) and the
	// destination with the soonest 5-hour reset first (its room expires unused otherwise).
	// 300 runs per pattern: fewer stopped minutes, hits, moves and re-reads than M4.
	M5 = func() Params {
		p := M4
		p.Name, p.CacheTTL, p.DrainFirst = "M5 M4 + 1 h cache, drain-first (default)", time.Hour, true
		return p
	}()
	// DefaultParams is what the auto-mover and `wy move --plan` use.
	DefaultParams = M5
	// Strategies in the order the simulator reports them.
	Strategies = []Params{M0, M1, M2, M3, M4, M5}
)

// Record is a move that happened (for the cooldown, the rate limit and no ping-pong).
type Record struct {
	Session  string
	From, To string
	At       time.Time
}

// Input is one planning pass.
type Input struct {
	Now      time.Time
	Accounts []Account // in the destination order of the picker: drain first, protected last
	Sessions []Session
	Recent   []Record // moves of the last hours, any order
	Params   Params
}

// Move is one planned move.
type Move struct {
	Session string
	From    string
	To      string // "" when no account can take it (Reason says why)
	// Wait: the session is mid-turn and the hit is near: move it as soon as the limit stops it
	// or its turn ends, not now.
	Wait bool
	// Window, HitAt and ResetAt: what triggers the move (the source window that would hit).
	Window  string
	HitAt   time.Time
	ResetAt time.Time
	// ByHand: asked for with wy move <session>, not planned (the note says so when no
	// forecast hit explains it).
	ByHand bool
	// Saved is the source's 5-hour points the session would burn before the reset; Cost what
	// the move costs (points).
	Saved, Cost float64
	Reason      string
}

// Plan is one pass of the auto-mover: for every account whose forecast reaches a limit before
// that window resets (or that is already stopped), the sessions to move and where. It never
// changes anything; the caller carries the moves out.
func Plan(in Input) []Move {
	moves, _ := PlanWhy(in)
	return moves
}

// Skip is a session on an account forecast to hit that the pass leaves where it is, and why.
type Skip struct {
	Session string `json:"session"`
	Account string `json:"account"`
	Why     string `json:"why"`
}

// PlanWhy is Plan plus the sessions it considered and left, with the reason (wy move --plan).
func PlanWhy(in Input) ([]Move, []Skip) {
	var skips []Skip
	p := in.Params
	if p.Off {
		return nil, nil
	}
	now := in.Now
	byAcc := map[string][]Session{}
	for _, s := range in.Sessions {
		byAcc[s.Account] = append(byAcc[s.Account], s)
	}
	accIdx := map[string]int{}
	for i, a := range in.Accounts {
		accIdx[a.Key] = i
	}
	// Sources in a fixed order: the soonest hit first, then the account order.
	type src struct {
		a  Account
		f  WindowForecast
		hi time.Duration
	}
	var srcs []src
	for _, a := range in.Accounts {
		ss := byAcc[a.Key]
		if len(ss) == 0 {
			continue
		}
		f := p.Forecast(a, ss, now, 0)
		w, hits := f.First()
		stopped := false
		for _, s := range ss {
			stopped = stopped || s.Stopped
		}
		switch {
		case stopped:
			if !hits {
				w = f.W5
				if f.W7.Level >= f.W7.Limit {
					w = f.W7
				}
				w.HitIn = 0
			}
		case p.Fixed5h > 0:
			if a.W5.Level < p.Fixed5h {
				continue
			}
			w = f.W5
			w.HitIn = max(0, w.HitIn)
		case !hits:
			continue
		case p.Horizon > 0 && w.HitIn > p.Horizon:
			continue
		}
		srcs = append(srcs, src{a, w, w.HitIn})
	}
	sort.SliceStable(srcs, func(i, j int) bool {
		if srcs[i].hi != srcs[j].hi {
			return srcs[i].hi < srcs[j].hi
		}
		return accIdx[srcs[i].a.Key] < accIdx[srcs[j].a.Key]
	})

	movedIn := map[string]int{}     // planned and recent moves into each account
	added := map[string][]Session{} // sessions planned onto each account
	lastMove := map[string]Record{}
	for _, r := range in.Recent {
		if p.AccountWindow > 0 && now.Sub(r.At) < p.AccountWindow {
			movedIn[r.To]++
		}
		if l, ok := lastMove[r.Session]; !ok || r.At.After(l.At) {
			lastMove[r.Session] = r
		}
	}
	var out []Move
	for _, sc := range srcs {
		a := sc.a
		left := append([]Session(nil), byAcc[a.Key]...)
		cands := p.candidates(left, sc.f, now, a)
		n := 0
		for _, s := range cands {
			if p.MaxPerPass > 0 && n >= p.MaxPerPass {
				break
			}
			// Enough moved already: the rest no longer hits (with slack) before the reset.
			if !stoppedAny(left) && p.Fixed5h == 0 && !p.sourceHits(a, left, now) {
				break
			}
			if p.Fixed5h > 0 && n > 0 {
				break
			}
			skip := func(why string) { skips = append(skips, Skip{s.ID, a.Key, why}) }
			if s.Pinned {
				skip("pinned, or outside the scope")
				continue
			}
			if l, ok := lastMove[s.ID]; ok && p.Cooldown > 0 && now.Sub(l.At) < p.Cooldown {
				skip("moved " + now.Sub(l.At).Round(time.Minute).String() + " ago (cooldown)")
				continue
			}
			hitIn := max(0, sc.f.HitIn)
			if p.TokenRules && !s.Stopped {
				if s.Attended {
					skip("the owner typed into it in the last minutes")
					continue
				}
				if p.remaining(s) <= hitIn {
					skip("expected to end before the hit")
					continue
				}
				if p.OncePer > 0 && !s.MovedAt.IsZero() && now.Sub(s.MovedAt) < p.OncePer {
					skip("already moved once in this window")
					continue
				}
			}
			mv := Move{Session: s.ID, From: a.Key, Window: sc.f.Window, HitAt: now.Add(hitIn),
				ResetAt: now.Add(sc.f.ResetIn)}
			mv.Saved = p.rate(s) * math.Min(p.remaining(s).Hours(), sc.f.ResetIn.Hours())
			mv.Cost = p.moveCost(s)
			if p.TokenRules {
				stall := math.Max(0, min(p.remaining(s), sc.f.ResetIn).Hours()-hitIn.Hours())
				if stall*(p.rate(s)+p.StallValue) <= mv.Cost {
					skip(fmt.Sprintf("the move costs more (%.1f points) than the stall it saves", mv.Cost))
					continue
				}
			}
			midTurn := !s.Idle && !s.Stopped
			if midTurn && !p.MidTurn {
				if sc.f.HitIn > p.WaitHit {
					skip("mid-turn: moved at its next stop if the account still needs it")
					continue
				}
				mv.Wait = true
			}
			to, why := p.destination(in, s, lastMove[s.ID], movedIn, added)
			mv.To = to
			mv.Reason = p.reason(a, sc.f, s, mv, why)
			out = append(out, mv)
			if to == "" {
				continue
			}
			n++
			movedIn[to]++
			on := s
			on.Account, on.Stopped = to, false
			added[to] = append(added[to], on)
			left = without(left, s.ID)
		}
	}
	return out, skips
}

func stoppedAny(ss []Session) bool {
	for _, s := range ss {
		if s.Stopped {
			return true
		}
	}
	return false
}

func without(ss []Session, id string) []Session {
	var out []Session
	for _, s := range ss {
		if s.ID != id {
			out = append(out, s)
		}
	}
	return out
}

// sourceHits reports whether account a with sessions ss still reaches its limit less Slack
// before the window resets.
func (p Params) sourceHits(a Account, ss []Session, now time.Time) bool {
	f := p.Forecast(a, ss, now, p.Slack)
	_, hits := f.First()
	return hits
}

// moveCost is the 5-hour points a move of s costs: its context read uncached once.
func (p Params) moveCost(s Session) float64 {
	if p.TokenRules && s.Idle && s.IdleFor >= p.CacheTTL {
		return p.MoveCostFixed // its cache is cold: staying would re-read it too
	}
	return p.MoveCostFixed + p.MoveCostPerMTok*modelWeight(s.Model)*s.Context/1e6
}

// modelWeight is a model's 5-hour points per token relative to Opus.
func modelWeight(m string) float64 {
	switch m := strings.ToLower(m); {
	case strings.Contains(m, "haiku"):
		return 0.2
	case strings.Contains(m, "sonnet"):
		return 0.6
	}
	return 1
}

// cold reports whether s's prompt cache has expired (a move costs nothing extra).
func (p Params) cold(s Session) bool { return s.Idle && p.CacheTTL > 0 && s.IdleFor >= p.CacheTTL }

// candidates orders the sessions of a source: stopped ones first (moving them loses
// nothing); then, cost-aware, idle before mid-turn and the most room freed per point of move
// cost; else the highest burn. Ties by id.
func (p Params) candidates(ss []Session, f WindowForecast, now time.Time, a Account) []Session {
	out := append([]Session(nil), ss...)
	T := f.ResetIn.Hours()
	scale := 1.0
	if f.Window == Win7d {
		scale = 1 / ratio(a)
	}
	key := func(s Session) float64 {
		saved := p.rate(s) * scale * math.Min(p.remaining(s).Hours(), T)
		if !p.CostAware {
			return p.rate(s)
		}
		return saved / math.Max(p.moveCost(s), 0.05)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Stopped != b.Stopped {
			return a.Stopped
		}
		if p.TokenRules && p.cold(a) != p.cold(b) {
			return p.cold(a)
		}
		if p.CostAware && a.Idle != b.Idle {
			return a.Idle
		}
		if ka, kb := key(a), key(b); ka != kb {
			return ka > kb
		}
		return a.ID < b.ID
	})
	return out
}

// destination is the first account in the picker's order that can take s: not its own, not
// stale or excluded, not the account it left within NoReturn, under the per-account move
// limit, and whose forecast with s (and the sessions already planned onto it) stays
// Hysteresis under its limits until its resets. Aggressive accounts before protected ones.
func (p Params) destination(in Input, s Session, last Record, movedIn map[string]int, added map[string][]Session) (string, string) {
	var ss []Session
	for _, x := range in.Sessions {
		ss = append(ss, x)
	}
	why := "no account has room for it until its reset"
	// The same organisation first: a session resumed under another organisation loses its
	// preserved thinking ("Claude can't use thinking from another organization") and rereads
	// and re-thinks at more tokens (e2e).
	type tier struct{ protected, sameOrg bool }
	accs := in.Accounts
	if p.DrainFirst {
		accs = append([]Account(nil), accs...)
		far := in.Now.Add(1000 * time.Hour)
		at := func(a Account) time.Time {
			if a.W5.Reset.After(in.Now) {
				return a.W5.Reset
			}
			return far // not started: its room does not expire
		}
		sort.SliceStable(accs, func(i, j int) bool { return at(accs[i]).Before(at(accs[j])) })
	}
	for _, tr := range []tier{{false, true}, {false, false}, {true, true}, {true, false}} {
		for _, a := range accs {
			if a.Protected != tr.protected || (Org(a.Key) == Org(s.Account)) != tr.sameOrg || a.Key == s.Account || a.Stale || a.Excluded {
				continue
			}
			if p.NoReturn > 0 && last.From == a.Key && in.Now.Sub(last.At) < p.NoReturn {
				continue
			}
			if p.AccountMoves > 0 && movedIn[a.Key] >= p.AccountMoves {
				why = "the accounts with room took their moves for now"
				continue
			}
			on := append([]Session(nil), added[a.Key]...)
			for _, x := range ss {
				if x.Account == a.Key {
					on = append(on, x)
				}
			}
			moved := s
			moved.Account, moved.Stopped = a.Key, false
			if p.Fixed5h > 0 {
				if a.W5.Level < p.Fixed5h && a.W7.Level < 98 {
					return a.Key, ""
				}
				continue
			}
			f := p.Forecast(a, append(on, moved), in.Now, p.Hysteresis)
			if _, hits := f.First(); !hits {
				return a.Key, ""
			}
		}
	}
	return "", why
}

// Org is the organisation part of an account key ("acct:<account>/<org>"; "" when none).
func Org(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[i+1:]
	}
	return ""
}

// reason is the move's one-line explanation.
func (p Params) reason(a Account, f WindowForecast, s Session, mv Move, why string) string {
	var b strings.Builder
	switch {
	case s.Stopped:
		fmt.Fprintf(&b, "stopped at the %s limit", f.Window)
	case p.Fixed5h > 0:
		fmt.Fprintf(&b, "5h %.0f%% >= %.0f%%", a.W5.Level, p.Fixed5h)
	default:
		fmt.Fprintf(&b, "%s would hit %.0f%% at %s, before its reset at %s (burn %.0f%%/h)", f.Window, f.Limit,
			mv.HitAt.Local().Format("15:04"), mv.ResetAt.Local().Format("15:04"), f.BurnRate)
	}
	if mv.Wait {
		b.WriteString("; mid-turn: moves when it stops")
	}
	if mv.To == "" {
		b.WriteString("; not moved: " + why)
	}
	return b.String()
}

// Destination is the account a move of session id would go to now under in.Params (wy move
// without an account), or "" and why none can take it.
func Destination(in Input, id string) (string, string) {
	p := in.Params
	var s Session
	found := false
	for _, x := range in.Sessions {
		if x.ID == id {
			s, found = x, true
		}
	}
	if !found {
		return "", "the session is not known"
	}
	movedIn := map[string]int{}
	var last Record
	for _, r := range in.Recent {
		if p.AccountWindow > 0 && in.Now.Sub(r.At) < p.AccountWindow {
			movedIn[r.To]++
		}
		if r.Session == id && r.At.After(last.At) {
			last = r
		}
	}
	return p.destination(in, s, last, movedIn, map[string][]Session{})
}
