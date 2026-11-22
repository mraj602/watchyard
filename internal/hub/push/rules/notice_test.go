package rules

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// The usage notices' texts (push.md 5.5) and delivery parameters (6.4, 7.2).
func TestNoticeTexts(t *testing.T) {
	week := ms(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)) // Thursday
	notice := func(tr model.Trigger, seat string, f model.Facts) model.Alert {
		a := model.Alert{AlertBrief: model.AlertBrief{ID: "al_9", Trigger: tr, Status: model.StatusFired}, Facts: f}
		if seat != "" {
			a.Seat = sp(seat)
		}
		return a
	}
	cases := []struct {
		name             string
		a                model.Alert
		title, body, url string
		tag              string
	}{
		{"usage_reset", notice(model.TriggerUsageReset, "seat-work", model.Facts{Seat: "work", ResetsAt: week, Sessions: 3}),
			"work · usage reset", "5-hour and 7-day back to 0% · 7-day still resets Thu 09:00 · 3 sessions agree",
			"/#/accounts?seat=seat-work", "wy-u-usage_reset-seat-work"},
		{"seat_change_account", notice(model.TriggerSeatChange, "seat-work", model.Facts{Seat: "work", Folder: "~/.claude-2",
			Machine: "gpu-box", Was: "personal"}),
			"work · ~/.claude-2 on gpu-box changed account", "Was personal; now this account", "/#/accounts?seat=seat-work",
			"wy-u-seat_change-seat-work"},
		{"seat_change_org", notice(model.TriggerSeatChange, "seat-acme", model.Facts{Seat: "acme", Folder: "~/.claude",
			Machine: "laptop", Was: "work", OrgOnly: true, Org: "Acme"}),
			"acme · ~/.claude on laptop changed org", "Same account, now in Acme", "/#/accounts?seat=seat-acme",
			"wy-u-seat_change-seat-acme"},
		{"weekly_drop_no_reset", notice(model.TriggerWeeklyDrop, "seat-work", model.Facts{Seat: "work", Peak: 64.4, Pct: 31.9}),
			"work · 7-day dropped 64% → 31%", "Something changed, check · no reset", "/#/accounts?seat=seat-work",
			"wy-u-weekly_drop-seat-work"},
		{"limit_change_down", notice(model.TriggerLimitChange, "", model.Facts{Model: "Sonnet 4.5", Change: 0.75, Moved: 2, Refs: 2}),
			"Sonnet 4.5 limits look lower", "5-hour capacity 0.75× last week on 2 of 2 accounts · check", "/#/accounts",
			"wy-u-limit_change-Sonnet 4.5"},
	}
	for _, c := range cases {
		got := Render(c.a, TextOptions{Now: textNow})
		if got.Title != c.title || got.Body != c.body || got.URL != c.url || got.Tag != c.tag {
			t.Errorf("%s: %q / %q / %q / %q", c.name, got.Title, got.Body, got.URL, got.Tag)
		}
		p := Render(c.a, TextOptions{Now: textNow, Private: true})
		if p.Title != PrivateTitle || p.Body != "Account usage changed" {
			t.Errorf("%s private: %q / %q", c.name, p.Title, p.Body)
		}
		tr := c.a.Trigger
		if TTL(tr, false, 0, 0) != 24*time.Hour || Urgency(tr, false) != "low" || Priority(tr, false) != 2 {
			t.Errorf("%s: delivery %v %s %d", c.name, TTL(tr, false, 0, 0), Urgency(tr, false), Priority(tr, false))
		}
	}
}

func TestDecideNotice(t *testing.T) {
	rules := []model.Rule{{ID: "rule_usage_reset", Trigger: model.TriggerUsageReset, Base: true, Enabled: true}}
	env := Env{Rules: rules, NowMS: 10 * 3600_000}
	n := model.Notice{Trigger: model.TriggerUsageReset, Episode: UsageResetEpisode("seat-a", 9*3600_000), Seat: "seat-a",
		SinceMS: 9 * 3600_000}
	ops := Decide(Current{Notice: &n}, nil, env)
	if len(ops) != 1 || ops[0].Alert.Status != model.StatusPending || int64(ops[0].Alert.DueAt) != env.NowMS ||
		deref(ops[0].Alert.Seat) != "seat-a" || ops[0].Alert.Machine != nil {
		t.Fatalf("ops = %+v", ops)
	}
	if again := Decide(Current{Notice: &n}, []model.Alert{ops[0].Alert}, env); len(again) != 0 {
		t.Fatalf("again = %+v", again)
	}
	old := n
	old.SinceMS = env.NowMS - NoticeMaxAgeMS - 1
	old.Episode = UsageResetEpisode("seat-a", old.SinceMS)
	if ops := Decide(Current{Notice: &old}, nil, env); len(ops) != 1 || ops[0].Alert.Status != model.StatusSkipped ||
		deref(ops[0].Alert.Reason) != model.ReasonStale {
		t.Fatalf("old = %+v", ops)
	}
	// No rule for the trigger (an unknown one, say): nothing.
	other := n
	other.Trigger = model.TriggerWeeklyDrop
	if ops := Decide(Current{Notice: &other}, nil, env); len(ops) != 0 {
		t.Fatalf("no rule = %+v", ops)
	}
	fired := model.Alert{AlertBrief: model.AlertBrief{Trigger: model.TriggerUsageReset, Status: model.StatusSeen, FiredAt: 1000}}
	if NoticeExpired(fired, 1000+NoticeLifeMS-1) || !NoticeExpired(fired, 1000+NoticeLifeMS) {
		t.Fatal("NoticeExpired bounds")
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
