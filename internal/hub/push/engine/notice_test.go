package engine_test

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// noticeSeat makes a seat (from one reading) and returns its id.
func (h *harness) noticeSeat() string {
	h.t.Helper()
	h.sample("", 0, 10, at(4*time.Hour), 64, at(5*24*time.Hour))
	var id string
	h.write(func(tx *store.Tx) error { return tx.QueryRowContext(tx.Ctx(), `SELECT id FROM seat`).Scan(&id) })
	return id
}

func (h *harness) weeklyDrop(seat string, since time.Duration) model.Notice {
	t := at(since).UnixMilli()
	reset := at(5 * 24 * time.Hour)
	return model.Notice{Trigger: model.TriggerWeeklyDrop, Episode: rules.WeeklyDropEpisode(seat, reset.UnixMilli(), t), Seat: seat,
		SinceMS: t, Facts: model.Facts{Seat: "work", Peak: 64, Pct: 31, ResetsAt: model.Time(reset.UnixMilli())}}
}

func (h *harness) notices(ns ...model.Notice) {
	h.t.Helper()
	if err := h.eng.Notices(h.ctx, ns); err != nil {
		h.t.Fatal(err)
	}
	h.eng.Sync()
}

// Row 46: a usage notice is one low-urgency push, closed 24 hours after it fired.
func TestRow46UsageNotice(t *testing.T) {
	h := newHarness(t, withSweep(time.Hour))
	seat := h.noticeSeat()
	h.advanceTo(time.Hour)
	h.notices(h.weeklyDrop(seat, time.Hour-time.Minute))
	got := h.expectSends(1)
	m := got[0].Msg
	if m.Payload.Title != "work · 7-day dropped 64% → 31%" || m.Payload.Body != "Something changed, check · no reset (resets Sat 12:00)" {
		t.Fatalf("%q / %q", m.Payload.Title, m.Payload.Body)
	}
	if m.Urgency != "low" || m.TTL != 24*time.Hour || m.Payload.Trigger != model.TriggerWeeklyDrop ||
		m.Payload.URL != "/#/accounts?seat="+seat || m.Payload.Tag != "wy-u-weekly_drop-"+seat {
		t.Fatalf("message = %+v", m)
	}
	a := h.byTrigger(model.TriggerWeeklyDrop)[0]
	h.advanceTo(24*time.Hour + 30*time.Minute)
	h.expectStatus(a, model.StatusFired, "")
	h.advanceTo(25*time.Hour + 30*time.Minute)
	h.expectStatus(a, model.StatusClosed, model.ReasonExpired)
	h.expectSends(1)
}

// Row 47: a notice about something 7 hours old is history: skipped, nothing sent.
func TestRow47OldUsageNotice(t *testing.T) {
	h := newHarness(t)
	seat := h.noticeSeat()
	h.advanceTo(8 * time.Hour)
	h.notices(h.weeklyDrop(seat, time.Hour))
	h.expectSends(0)
	h.expectStatus(h.byTrigger(model.TriggerWeeklyDrop)[0], model.StatusSkipped, model.ReasonStale)
}

// Row 48: the same notice handed in two runs is one alert.
func TestRow48NoticeTwice(t *testing.T) {
	h := newHarness(t)
	seat := h.noticeSeat()
	n := h.weeklyDrop(seat, 0)
	h.notices(n, n)
	h.advanceTo(15 * time.Minute)
	h.notices(n)
	if got := h.byTrigger(model.TriggerWeeklyDrop); len(got) != 1 {
		t.Fatal(h.dump())
	}
	h.expectSends(1)
}

// Row 49: with the rule off the notice is recorded as skipped.
func TestRow49NoticeRuleOff(t *testing.T) {
	h := newHarness(t)
	seat := h.noticeSeat()
	h.exec(`UPDATE rule SET enabled = 0 WHERE id = 'rule_weekly_drop'`)
	h.notices(h.weeklyDrop(seat, 0))
	h.expectSends(0)
	h.expectStatus(h.byTrigger(model.TriggerWeeklyDrop)[0], model.StatusSkipped, model.ReasonRuleOff)
}

// A limit_change has no seat and links to the accounts page.
func TestLimitChangeNotice(t *testing.T) {
	h := newHarness(t)
	h.notices(model.Notice{Trigger: model.TriggerLimitChange, Episode: rules.LimitChangeEpisode("Opus 4.6", true, "2026-10-12"),
		SinceMS: T0.UnixMilli(), Facts: model.Facts{Model: "Opus 4.6", Up: true, Change: 1.3, Moved: 3, Refs: 4}})
	got := h.expectSends(1)
	m := got[0].Msg.Payload
	if m.Title != "Opus 4.6 limits look higher" || m.Body != "5-hour capacity 1.3× last week on 3 of 4 accounts · check" ||
		m.URL != "/#/accounts" || m.Tag != "wy-u-limit_change-Opus 4.6" {
		t.Fatalf("payload = %+v", m)
	}
}
