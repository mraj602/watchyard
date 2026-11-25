package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// The owner's drain (setup.md 8.5, Owner drain; push.md 3.9): drain_started once per until,
// drain_ended at until or at the drain aim (the hub clears drain_until), on the Drain channel,
// and a device that turned the drain category off gets none.
func TestDrainNotices(t *testing.T) {
	h := newHarness(t)
	off := h.addTarget(h.addDevice("Pixel", `{"mute_categories": ["drain"]}`), model.KindWebPush, "")
	h.sample("four@example.com", 0, 20, at(3*time.Hour), 60, at(80*time.Hour))
	until := at(6 * time.Hour)
	h.exec(`UPDATE seat SET drain_until = ?, label = 'claude-4'`, until.UnixMilli())
	h.advanceTo(2 * time.Minute)
	as := h.byTrigger(model.TriggerDrainStarted)
	if len(as) != 1 || len(h.wp.to(h.target)) != 1 || len(h.wp.to(off)) != 0 {
		t.Fatalf("started: %s %+v", h.dump(), h.wp.sends())
	}
	m := h.wp.to(h.target)[0].Msg.Payload
	if m.Category != model.CategoryDrain || m.Channel != "wy_drain" || !strings.HasPrefix(m.Title, "claude-4 · draining until") {
		t.Fatalf("payload %+v", m)
	}
	// Later sweeps: the same episode, no second push.
	h.advanceTo(10 * time.Minute)
	if len(h.byTrigger(model.TriggerDrainStarted)) != 1 || len(h.wp.to(h.target)) != 1 {
		t.Fatalf("again: %+v", h.wp.sends())
	}
	// The weekly level reaches the aim: ended, cleared.
	h.sample("four@example.com", 11*time.Minute, 30, at(3*time.Hour), 97.5, at(80*time.Hour))
	h.advanceTo(13 * time.Minute)
	ended := h.byTrigger(model.TriggerDrainEnded)
	if len(ended) != 1 || ended[0].Facts.Detail != "aim" {
		t.Fatalf("ended: %s", h.dump())
	}
	var body string
	for _, s := range h.wp.to(h.target) {
		if s.Msg.Payload.Trigger == model.TriggerDrainEnded {
			body = s.Msg.Payload.Body
		}
	}
	if !strings.HasPrefix(body, "Its week reached 9") {
		t.Fatalf("body %q", body)
	}
	h.write(func(tx *store.Tx) error {
		if ds, err := tx.DrainingSeats(); err != nil || len(ds) != 0 {
			t.Fatalf("drain_until kept: %v %v", ds, err)
		}
		return nil
	})
}

// Ended by time.
func TestDrainEndsAtUntil(t *testing.T) {
	h := newHarness(t)
	h.sample("four@example.com", 0, 20, at(3*time.Hour), 60, at(80*time.Hour))
	h.exec(`UPDATE seat SET drain_until = ?`, at(5*time.Minute).UnixMilli())
	h.advanceTo(7 * time.Minute)
	ended := h.byTrigger(model.TriggerDrainEnded)
	if len(h.byTrigger(model.TriggerDrainStarted)) != 1 || len(ended) != 1 || ended[0].Facts.Detail != "time" {
		t.Fatal(h.dump())
	}
}
