package record

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestAccountVerdicts(t *testing.T) {
	lvl, rank, w, wc, fc := 3, 1, 40.0, 52.0, 45.0
	free := "2026-11-20T14:00:00Z"
	ok := AccountVerdictsPayload{Strategy: "lanes", NextFree: &free, Accounts: []AccountVerdict{
		{Account: "a", Org: "o", Kind: LeaseAggressive, Eligible: true, Rank: &rank, WeeklyPct: &w,
			ModeFrom: ModeFromHub, WeeklyCap: &wc, FiveHourCap: &fc},
		{Account: "b", Org: "o", Eligible: false, Reason: "5h 99% >= 95", SkipLevel: &lvl, FreeAt: &free},
	}}
	m := Meta{Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", Slot: "~/.claude", Now: time.Now(),
		Seat: &Seat{Account: "a", Org: "o", Src: "claude_json"}}
	r, err := NewAccountVerdicts(m, ok)
	if err != nil || r.Kind != KindAccountVerdicts || r.Slot != "" || r.Seat != nil || r.Validate() != nil {
		t.Fatalf("record %+v err %v", r, err)
	}
	bad := "tomorrow"
	nan := math.NaN()
	neg, four := -1, 4
	for name, mod := range map[string]func(p *AccountVerdictsPayload){
		"strategy":   func(p *AccountVerdictsPayload) { p.Strategy = "" },
		"next_free":  func(p *AccountVerdictsPayload) { p.NextFree = &bad },
		"none":       func(p *AccountVerdictsPayload) { p.Accounts = nil },
		"login":      func(p *AccountVerdictsPayload) { p.Accounts[0].Org = "" },
		"kind":       func(p *AccountVerdictsPayload) { p.Accounts[0].Kind = "normal" },
		"reason":     func(p *AccountVerdictsPayload) { p.Accounts[1].Reason = strings.Repeat("x", 301) },
		"skip_level": func(p *AccountVerdictsPayload) { p.Accounts[1].SkipLevel = &four },
		"rank":       func(p *AccountVerdictsPayload) { p.Accounts[0].Rank = &neg },
		"level":      func(p *AccountVerdictsPayload) { p.Accounts[0].WeeklyPct = &nan },
		"mode_from":  func(p *AccountVerdictsPayload) { p.Accounts[0].ModeFrom = "board" },
		"cap":        func(p *AccountVerdictsPayload) { p.Accounts[0].FiveHourCap = &nan },
		"many": func(p *AccountVerdictsPayload) {
			for len(p.Accounts) <= VerdictMaxAccounts {
				p.Accounts = append(p.Accounts, p.Accounts[0])
			}
		},
	} {
		p := ok
		p.Accounts = append([]AccountVerdict(nil), ok.Accounts...)
		mod(&p)
		if _, err := NewAccountVerdicts(m, p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
