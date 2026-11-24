package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/mover"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
)

// switchScope is account.smart_switch_scope: which sessions Smart Account Switch may move
// (setup.md 8.6, Scope). Out of scope, a session still counts in its account's forecast but
// is never moved, by the timer or by hand.
type switchScope struct {
	all bool
	tag string
	ids []string
}

func parseScope(v string) switchScope {
	switch {
	case v == "" || v == "all":
		return switchScope{all: true}
	case strings.HasPrefix(v, "tag:"):
		return switchScope{tag: v[4:]}
	case strings.HasPrefix(v, "sessions:"):
		var s switchScope
		for _, id := range strings.Split(v[9:], ",") {
			if id = strings.TrimSpace(id); id != "" {
				s.ids = append(s.ids, id)
			}
		}
		return s
	}
	return switchScope{} // unknown: nothing is in scope
}

// in reports whether the session with id sid and peers tags is in scope.
func (s switchScope) in(sid string, tags []string) bool {
	if s.all {
		return true
	}
	if s.tag != "" {
		return peerspkg.HasTag(tags, s.tag)
	}
	for _, id := range s.ids {
		if sid == id || (len(id) >= 8 && strings.HasPrefix(sid, id)) {
			return true
		}
	}
	return false
}

// scopeTags is the scope's tag as a peers marker tag (none for other scopes).
func scopeTags(v string) []string {
	if s := parseScope(v); s.tag != "" {
		return []string{s.tag}
	}
	return nil
}

func (s switchScope) String() string {
	switch {
	case s.all:
		return "all"
	case s.tag != "":
		return "tag:" + s.tag
	}
	return "sessions:" + strings.Join(s.ids, ",")
}

// moveTestEnv is the variable of the test-only fault injection (setup.md 8.6, Testing).
const moveTestEnv = "WY_MOVE_TEST"

// moveTest is WY_MOVE_TEST: levels, burn and rule timings that replace the measured ones so
// an end-to-end test can make the switch move sessions. It is refused unless the scope is
// narrowed (not all), so it can never move the owner's other sessions.
type moveTest struct {
	// Accounts by label: the 5-hour and weekly levels (percent) and minutes to their resets.
	// Rate is the account's 5-hour burn, points per hour (instead of the meter).
	Accounts map[string]struct {
		FiveHour       *float64 `json:"five_hour"`
		FiveResetMin   *float64 `json:"five_reset_min"`
		Weekly         *float64 `json:"weekly"`
		WeeklyResetMin *float64 `json:"weekly_reset_min"`
		Rate           *float64 `json:"rate"`
	} `json:"accounts"`
	// Rates: 5-hour points per hour by session id (or a prefix of 8+ characters).
	Rates map[string]float64 `json:"rates"`
	// Rule timings in minutes, moves per account per window and the spike filter's passes.
	CooldownMin      *float64 `json:"cooldown_min"`
	NoReturnMin      *float64 `json:"no_return_min"`
	AccountWindowMin *float64 `json:"account_window_min"`
	OwnerGraceMin    *float64 `json:"owner_grace_min"`
	AccountMoves     *int     `json:"account_moves"`
	SpikePasses      *int     `json:"spike_passes"`
}

// loadMoveTest reads WY_MOVE_TEST (JSON, or @<file>); nil when unset.
func loadMoveTest(getenv func(string) string, scope switchScope) (*moveTest, error) {
	v := strings.TrimSpace(getenv(moveTestEnv))
	if v == "" {
		return nil, nil
	}
	if scope.all {
		return nil, errors.New(moveTestEnv + " is refused while account.smart_switch_scope is all: narrow the scope (tag:<tag> or sessions:<ids>) first")
	}
	b := []byte(v)
	if strings.HasPrefix(v, "@") {
		var err error
		if b, err = os.ReadFile(v[1:]); err != nil {
			return nil, fmt.Errorf("%s: %w", moveTestEnv, err)
		}
	}
	var t moveTest
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", moveTestEnv, err)
	}
	return &t, nil
}

func mins(v float64) time.Duration { return time.Duration(v * float64(time.Minute)) }

// applyParams puts the injected rule timings into p.
func (t *moveTest) applyParams(p *mover.Params) {
	set := func(dst *time.Duration, v *float64) {
		if v != nil {
			*dst = mins(*v)
		}
	}
	set(&p.Cooldown, t.CooldownMin)
	set(&p.NoReturn, t.NoReturnMin)
	set(&p.AccountWindow, t.AccountWindowMin)
	set(&p.OwnerGrace, t.OwnerGraceMin)
	if t.AccountMoves != nil {
		p.AccountMoves = *t.AccountMoves
	}
	if t.SpikePasses != nil {
		p.SpikePasses = *t.SpikePasses
	}
}

// applyAccount replaces a's measured levels with the injected ones.
func (t *moveTest) applyAccount(a *mover.Account, now time.Time) bool {
	x, ok := t.Accounts[a.Label]
	if !ok {
		return false
	}
	if x.FiveHour != nil {
		a.W5.Level = *x.FiveHour
	}
	if x.FiveResetMin != nil {
		a.W5.Reset = now.Add(mins(*x.FiveResetMin))
	}
	if x.Weekly != nil {
		a.W7.Level = *x.Weekly
	}
	if x.WeeklyResetMin != nil {
		a.W7.Reset = now.Add(mins(*x.WeeklyResetMin))
	}
	a.Stale = false
	return true
}

// rate is the injected burn of session sid, if any.
func (t *moveTest) rate(sid string) (float64, bool) {
	if r, ok := t.Rates[sid]; ok {
		return r, true
	}
	for id, r := range t.Rates {
		if len(id) >= 8 && strings.HasPrefix(sid, id) {
			return r, true
		}
	}
	return 0, false
}
