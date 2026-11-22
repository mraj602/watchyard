package record

import (
	"fmt"
	"math"
	"time"
)

// KindAccountVerdicts is what one run of `wy account next` decided about every account it
// ranked (record.md 4.11, setup.md 8.5 Verdicts to the hub).
const KindAccountVerdicts = "account_verdicts"

// Bounds of an account_verdicts payload.
const (
	VerdictMaxAccounts = 50
	VerdictMaxReason   = 300
)

// AccountVerdictsPayload is the payload of an account_verdicts record.
type AccountVerdictsPayload struct {
	Strategy string           `json:"strategy"`
	NextFree *string          `json:"next_free,omitempty"`
	Accounts []AccountVerdict `json:"accounts"`
}

// AccountVerdict is one account of an account_verdicts record.
type AccountVerdict struct {
	Account     string   `json:"account"`
	Org         string   `json:"org"`
	Kind        string   `json:"kind,omitempty"`
	Eligible    bool     `json:"eligible"`
	Reason      string   `json:"reason,omitempty"`
	SkipLevel   *int     `json:"skip_level,omitempty"`
	Rank        *int     `json:"rank,omitempty"`
	Expiring    bool     `json:"expiring,omitempty"`
	FreeAt      *string  `json:"free_at,omitempty"`
	WeeklyPct   *float64 `json:"weekly_pct,omitempty"`
	FiveHourPct *float64 `json:"five_hour_pct,omitempty"`
	Burst       *int     `json:"burst,omitempty"`
	BurstCap    *int     `json:"burst_cap,omitempty"`
	Stale       bool     `json:"stale,omitempty"`
	// ModeFrom is where the kind came from (setup.md 8.5, Kind): local, hub, proposed, none.
	ModeFrom    string   `json:"mode_from,omitempty"`
	WeeklyCap   *float64 `json:"weekly_cap,omitempty"`
	FiveHourCap *float64 `json:"five_hour_cap,omitempty"`
}

// Verdict mode_from values (record.md 4.11).
const (
	ModeFromLocal    = "local"
	ModeFromHub      = "hub"
	ModeFromProposed = "proposed"
	ModeFromNone     = "none"
)

func validTime(p *string) bool {
	if p == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339, *p)
	return err == nil
}

func validLevel(p *float64) bool {
	return p == nil || (!math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 1000)
}

// Validate checks the fields record.md 4.11 requires.
func (p AccountVerdictsPayload) Validate() error {
	if p.Strategy == "" || len(p.Strategy) > 40 {
		return fmt.Errorf("account_verdicts: bad strategy %q", p.Strategy)
	}
	if !validTime(p.NextFree) {
		return fmt.Errorf("account_verdicts: bad next_free")
	}
	if len(p.Accounts) == 0 || len(p.Accounts) > VerdictMaxAccounts {
		return fmt.Errorf("account_verdicts: %d accounts", len(p.Accounts))
	}
	for _, a := range p.Accounts {
		if a.Account == "" || a.Org == "" || len(a.Account) > 100 || len(a.Org) > 100 {
			return fmt.Errorf("account_verdicts: an account without its login")
		}
		if a.Kind != "" && a.Kind != LeaseAggressive && a.Kind != LeaseProtected {
			return fmt.Errorf("account_verdicts: bad kind %q", a.Kind)
		}
		if len(a.Reason) > VerdictMaxReason {
			return fmt.Errorf("account_verdicts: reason too long")
		}
		if a.SkipLevel != nil && (*a.SkipLevel < 0 || *a.SkipLevel > 3) {
			return fmt.Errorf("account_verdicts: bad skip_level %d", *a.SkipLevel)
		}
		if (a.Rank != nil && *a.Rank < 1) || (a.Burst != nil && *a.Burst < 0) || (a.BurstCap != nil && *a.BurstCap < 0) {
			return fmt.Errorf("account_verdicts: bad rank or burst")
		}
		if !validTime(a.FreeAt) || !validLevel(a.WeeklyPct) || !validLevel(a.FiveHourPct) {
			return fmt.Errorf("account_verdicts: bad free_at or level")
		}
		switch a.ModeFrom {
		case "", ModeFromLocal, ModeFromHub, ModeFromProposed, ModeFromNone:
		default:
			return fmt.Errorf("account_verdicts: bad mode_from %q", a.ModeFrom)
		}
		if !validLevel(a.WeeklyCap) || !validLevel(a.FiveHourCap) {
			return fmt.Errorf("account_verdicts: bad cap")
		}
	}
	return nil
}

// NewAccountVerdicts builds an account_verdicts record: no seat and no slot, each account
// names its own login.
func NewAccountVerdicts(m Meta, p AccountVerdictsPayload) (*Record, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	m.Seat, m.Slot, m.Term = nil, "", nil
	r := New(KindAccountVerdicts, m)
	return r, r.SetPayload(p)
}
