package hubsrc

import (
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/board"
)

// DefaultBuild hands the wire reads to board.BuildHub: the words, text safety and order are
// board's (tested there); this checks the hand-over of every field the board reads.
func TestDefaultBuildUsesBoardBuilder(t *testing.T) {
	now := time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC)
	p := func(s string) *string { return &s }
	f := func(x float64) *float64 { return &x }
	seats := []wireSeat{
		{ID: "s_work", Label: p("work"), Colour: p("#f59e0b"), Windows: []wireWindow{{Key: "five_hour", UsedPct: f(41), ResetsAt: &reset}}},
		{ID: "s_home", Email: p("me@example.com"), Colour: p("#3b82f6")},
	}
	sessions := []wireSession{
		{ID: "c1", State: "asked", Detail: p("question"), NeedsYou: true, StateSince: now.Add(-4 * time.Minute),
			Question: p("Keep\x1b]2;x\x07 it?"), Seat: p("s_work"), Machine: p("m_me"), Project: p("api"),
			FocusURL: p("warp://session/0123456789abcdef0123456789abcdef")},
		{ID: "c2", State: "failed", Detail: p("rate_limit"), StateSince: now.Add(-time.Minute), Seat: p("s_work"),
			Machine: p("m_gpu"), MachineName: p("gpu-box"), Cwd: p("/srv/web")},
		{ID: "c3", State: "ended", StateSince: now.Add(-time.Hour), Seat: p("s_home")},
		{ID: "c4", State: "done", StateSince: now.Add(-2 * time.Minute), Tool: p("Bash"), MachineStale: true},
	}
	in := BuildInput{Now: now, Loc: time.UTC, Machine: board.Machine{ID: "m_me", Name: "laptop"}, ThisSession: "c2",
		Hub: board.HubInfo{State: board.HubLive, URL: "https://hub.test"}, Source: board.SourceHub,
		WarpColour: func(string) string { return "magenta" }, Seats: seats, Sessions: sessions, MachineCount: 2}

	v := DefaultBuild(in)
	if err := v.Check(); err != nil {
		t.Fatal(err)
	}
	if v.Source != board.SourceHub || v.Counts.Machines != 2 || v.Hub.URL != "https://hub.test" {
		t.Errorf("view = %+v %+v", v.Source, v.Counts)
	}
	if _, ok := v.Rows["c3"]; ok {
		t.Error("ended row without IncludeEnded")
	}
	c1 := v.Rows["c1"]
	if c1.Status != "asked" || strings.ContainsAny(c1.Line2, "\x1b\x07") || !c1.Here || !c1.CanFocus || c1.Account != "work" {
		t.Errorf("c1 = %+v", c1)
	}
	if c2 := v.Rows["c2"]; c2.Status != "limit · 17:00" || c2.Here || !c2.ThisSession || c2.Project != "web" {
		t.Errorf("c2 = %+v", c2)
	}
	if c4 := v.Rows["c4"]; !c4.Stale {
		t.Errorf("c4 = %+v (machine_stale)", c4)
	}
	if v.Groups[0].WarpColour != "magenta" || v.Groups[1].Label != "me@example.com" || v.Groups[0].FiveHour == nil {
		t.Errorf("groups = %+v", v.Groups)
	}
	if v.Groups[len(v.Groups)-1].Label != board.UnattributedLabel {
		t.Errorf("last group = %+v", v.Groups[len(v.Groups)-1])
	}

	in.IncludeEnded = true
	if r, ok := DefaultBuild(in).Rows["c3"]; !ok || r.Glyph != board.GlyphEnded {
		t.Errorf("IncludeEnded: c3 = %+v %v", r, ok)
	}
}

func TestHubLessMatchesHubOrder(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id string, needs bool, since, act int) wireSession {
		a := base.Add(time.Duration(act) * time.Minute)
		return wireSession{ID: id, NeedsYou: needs, StateSince: base.Add(time.Duration(since) * time.Minute), LastEventAt: &a}
	}
	list := []wireSession{mk("d", false, 0, 1), mk("b", true, 5, 9), mk("c", false, 0, 7), mk("a", true, 2, 3)}
	for i := 1; i < len(list); i++ { // insertion sort with hubLess
		for j := i; j > 0 && hubLess(list[j], list[j-1]); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	var ids []string
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ""); got != "abcd" {
		t.Errorf("order = %s, want abcd (needs-you oldest first, then newest activity)", got)
	}
}

// The owner's drains reach the hub board: "draining (owner, until 23:59)" by the label.
func TestDefaultBuildDrains(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	label, weekly := "claude-4", 54.0
	in := BuildInput{Now: now, Loc: time.UTC, Hub: board.HubInfo{State: board.HubLive}, Source: board.SourceHub,
		Seats: []wireSeat{{ID: "s4", Label: &label, Windows: []wireWindow{{Key: "seven_day", UsedPct: &weekly}}}},
		Drains: func() map[string]time.Time {
			return map[string]time.Time{"claude-4": time.Date(2026, 11, 25, 23, 59, 0, 0, time.UTC)}
		}}
	v := DefaultBuild(in)
	if len(v.Groups) != 1 || v.Groups[0].Note != "draining (owner, until 23:59)" {
		t.Errorf("groups = %+v", v.Groups)
	}
}
