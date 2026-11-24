package record

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSwitchMove(t *testing.T) {
	p5 := 96.5
	ok := SwitchMovePayload{At: "2026-11-24T10:00:00Z", SessionID: "s1", SessionName: "api", Kind: "tmux", Interactive: true,
		From: "claude-3", To: "claude-2", Auto: true, Reason: "5h 96% >= 95", Window: "5h", P5: &p5}
	r, err := NewSwitchMove(Meta{Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", Slot: "~/.claude", Now: time.Now()}, ok)
	if err != nil || r.Kind != KindSwitchMove || r.Slot != "" || r.Validate() != nil {
		t.Fatalf("record %+v err %v", r, err)
	}
	var back SwitchMovePayload
	if err := json.Unmarshal(r.Payload, &back); err != nil || back.P5 == nil || *back.P5 != 96.5 || back.P7 != nil {
		t.Fatalf("round trip %+v err %v", back, err)
	}
	failed := ok
	failed.Error = "claude not found"
	if err := failed.Validate(); err != nil {
		t.Fatalf("a failed move: %v", err)
	}
	for name, mod := range map[string]func(p *SwitchMovePayload){
		"at":         func(p *SwitchMovePayload) { p.At = "today" },
		"session":    func(p *SwitchMovePayload) { p.SessionID = "" },
		"kind":       func(p *SwitchMovePayload) { p.Kind = "cloud" },
		"from":       func(p *SwitchMovePayload) { p.From = "" },
		"to":         func(p *SwitchMovePayload) { p.To = strings.Repeat("x", SwitchMoveMaxLabel+1) },
		"reason":     func(p *SwitchMovePayload) { p.Reason = strings.Repeat("x", SwitchMoveMaxText+1) },
		"error":      func(p *SwitchMovePayload) { p.Error = "\xff" },
		"name utf-8": func(p *SwitchMovePayload) { p.SessionName = "\xff" },
	} {
		p := ok
		mod(&p)
		if p.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
