package engine_test

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/record"
)

// switchNotice sends a machine's switch_notice record through ingest (record.md 4.12).
func (h *harness) switchNotice(ts time.Duration, p record.SwitchNoticePayload) {
	h.t.Helper()
	r, err := record.NewSwitchNotice(record.Meta{Machine: h.machine, Client: "wy/0.0.0-test", Now: at(ts)}, p)
	if err != nil {
		h.t.Fatal(err)
	}
	h.ingest(r)
}

func movedPayload(ts time.Duration, moves int) record.SwitchNoticePayload {
	return record.SwitchNoticePayload{Trigger: record.SwitchNoticeMoved, Episode: "m:" + at(ts).Format("150405"),
		SinceAt: at(ts).UTC().Format(time.RFC3339), Moves: moves, Text: "api a → b (5h 96%)\nweb a → c (5h 97%)"}
}

// A switch notice from a machine is an alert pushed to every target, linked to the machine,
// once per episode, and muted per device like any trigger (push.md 3.9).
func TestSwitchNoticeFromMachine(t *testing.T) {
	h := newHarness(t)
	muted := h.addTarget(h.addDevice("Phone", `{"mute": ["session_moved"]}`), model.KindWebPush, "")
	h.switchNotice(0, movedPayload(0, 2))
	if len(h.wp.to(h.target)) != 1 || len(h.wp.to(muted)) != 0 {
		t.Fatalf("%+v", h.wp.sends())
	}
	m := h.wp.to(h.target)[0].Msg
	if m.Payload.Trigger != model.TriggerSessionMoved || m.Payload.Body != "api a → b (5h 96%)\nweb a → c (5h 97%)" ||
		m.Payload.URL != "/#/machines?machine="+h.machine || m.Payload.Tag != "wy-u-session_moved-"+h.machine {
		t.Fatalf("message = %+v", m)
	}
	if m.Payload.Title != "gpu-box · Moved 2 sessions" {
		t.Fatalf("title %q", m.Payload.Title)
	}
	as := h.byTrigger(model.TriggerSessionMoved)
	if len(as) != 1 || as[0].Machine == nil || *as[0].Machine != h.machine || as[0].Facts.Moves != 2 {
		t.Fatal(h.dump())
	}
	for _, d := range h.deliveries(as[0].ID) {
		if d.Target == muted && (d.Status != "skipped" || deref(d.Reason) != "muted") {
			t.Fatalf("%+v", d)
		}
	}
	// The same episode again (a retry of the machine) is the same alert.
	h.advanceTo(time.Minute)
	h.switchNotice(0, movedPayload(0, 2))
	if len(h.byTrigger(model.TriggerSessionMoved)) != 1 || len(h.wp.to(h.target)) != 1 {
		t.Fatal(h.dump())
	}
	// The digest is its own trigger.
	h.switchNotice(time.Minute, record.SwitchNoticePayload{Trigger: record.SwitchNoticeDigest, Episode: "d:2026-11-24",
		SinceAt: at(time.Minute).UTC().Format(time.RFC3339), Moves: 3, Text: "3 moves today"})
	if len(h.byTrigger(model.TriggerSwitchDigest)) != 1 || len(h.wp.to(h.target)) != 2 || len(h.wp.to(muted)) != 1 {
		t.Fatalf("%+v", h.wp.sends())
	}
}

// A notice the machine already pushed through direct push is kept, skipped with reason
// direct, and not pushed again.
func TestSwitchNoticeDirect(t *testing.T) {
	h := newHarness(t)
	p := movedPayload(0, 1)
	p.Direct = true
	h.switchNotice(0, p)
	h.expectSends(0)
	h.expectStatus(h.byTrigger(model.TriggerSessionMoved)[0], model.StatusSkipped, model.ReasonDirect)
}

// The rule off: recorded, skipped, nothing sent.
func TestSwitchNoticeRuleOff(t *testing.T) {
	h := newHarness(t)
	h.exec(`UPDATE rule SET enabled = 0 WHERE id = 'rule_session_moved'`)
	h.switchNotice(0, movedPayload(0, 1))
	h.expectSends(0)
	h.expectStatus(h.byTrigger(model.TriggerSessionMoved)[0], model.StatusSkipped, model.ReasonRuleOff)
}
