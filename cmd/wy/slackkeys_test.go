package main

import (
	"testing"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/picker"
)

func TestLaneParamsDrainSlack(t *testing.T) {
	c := config.Defaults()
	p := laneParams(c)
	if p.DrainSlack != 2 || p.ProtDrainSlack != 1.5 || p.DrainSlack != picker.DefaultLaneParams.DrainSlack || p.ProtDrainSlack != picker.DefaultLaneParams.ProtDrainSlack {
		t.Fatalf("defaults: %v %v", p.DrainSlack, p.ProtDrainSlack)
	}
	c.Account.DrainSlack, c.Account.DrainSlackProtected = 3, 1
	if p := laneParams(c); p.DrainSlack != 3 || p.ProtDrainSlack != 1 {
		t.Fatalf("set: %v %v", p.DrainSlack, p.ProtDrainSlack)
	}
	c.Account.DrainSlack, c.Account.DrainSlackProtected = 0, 0
	if p := laneParams(c); p.DrainSlack != 2 || p.ProtDrainSlack != 1.5 {
		t.Fatalf("zero falls back: %v %v", p.DrainSlack, p.ProtDrainSlack)
	}
}

func TestPinOverridesFallback(t *testing.T) {
	s := picker.Settings{Accounts: map[string]config.NextAccount{"claude-2": {PinReserve5h: 20}, "b@x.io": {PinReserve7d: 8}}}
	cands := []picker.Candidate{{Account: "a1", Org: "o", Label: "claude-2"}, {Account: "a2", Org: "o", Email: "b@x.io"}, {Account: "a3", Org: "o", Label: "claude-3"}, {Label: "claude-2"}}
	p := moveParams(config.Defaults())
	p.PinPer = pinOverrides(s, cands)
	for _, c := range []struct {
		key    string
		r5, r7 float64
	}{{"acct:a1/o", 40, 10}, {"acct:a2/o", 20, 16}, {"acct:a3/o", 20, 10}, {"acct:none", 20, 10}} {
		if r5, r7 := p.PinReserveOf(c.key, 2); r5 != c.r5 || r7 != c.r7 {
			t.Errorf("%s: %v %v, want %v %v", c.key, r5, r7, c.r5, c.r7)
		}
	}
}
