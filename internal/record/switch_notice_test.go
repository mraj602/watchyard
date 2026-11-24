package record

import (
	"strings"
	"testing"
	"time"
)

func TestSwitchNotice(t *testing.T) {
	ok := SwitchNoticePayload{Trigger: SwitchNoticeMoved, Episode: "m:1763900000000",
		SinceAt: "2026-11-24T10:00:00Z", Moves: 2, Text: "api moved a → b (5h 96%)"}
	m := Meta{Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", Slot: "~/.claude", Now: time.Now(),
		Seat: &Seat{Account: "a", Org: "o", Src: "claude_json"}}
	r, err := NewSwitchNotice(m, ok)
	if err != nil || r.Kind != KindSwitchNotice || r.Slot != "" || r.Seat != nil || r.Validate() != nil {
		t.Fatalf("record %+v err %v", r, err)
	}
	digest := ok
	digest.Trigger, digest.Moves = SwitchNoticeDigest, 0
	if err := digest.Validate(); err != nil {
		t.Fatalf("a digest of a day without moves: %v", err)
	}
	for name, mod := range map[string]func(p *SwitchNoticePayload){
		"trigger":   func(p *SwitchNoticePayload) { p.Trigger = "limit_change" },
		"episode":   func(p *SwitchNoticePayload) { p.Episode = "" },
		"long ep":   func(p *SwitchNoticePayload) { p.Episode = strings.Repeat("e", SwitchNoticeMaxEpisode+1) },
		"since_at":  func(p *SwitchNoticePayload) { p.SinceAt = "today" },
		"no moves":  func(p *SwitchNoticePayload) { p.Moves = 0 },
		"neg moves": func(p *SwitchNoticePayload) { p.Moves = -1 },
		"no text":   func(p *SwitchNoticePayload) { p.Text = "" },
		"long text": func(p *SwitchNoticePayload) { p.Text = strings.Repeat("x", SwitchNoticeMaxText+1) },
		"bad utf8":  func(p *SwitchNoticePayload) { p.Text = "\xff" },
	} {
		p := ok
		mod(&p)
		if p.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
