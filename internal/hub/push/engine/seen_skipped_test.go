package engine_test

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// Seen on Needs-you rows whose alert was never pushed (push.md 4, 10.1; 11.1 rows 50 and 51).

type needsOverview struct {
	NeedsYou []struct {
		ID       string     `json:"id"`
		State    string     `json:"state"`
		NeedsYou bool       `json:"needs_you"`
		Alert    *alertJSON `json:"alert"`
	} `json:"needs_you"`
	Counts struct {
		NeedsYou int `json:"needs_you"`
	} `json:"counts"`
}

// Row 50, the three cards seen live: two stale (asked, needs_permission) and one peers.
func TestRow50SeenOnSkippedRows(t *testing.T) {
	r := newAPIRig(t)
	h := r.h
	h.advanceTo(30 * time.Minute)
	stale1, stale2, peers := s1, sid(2), sid(3)
	h.askQ(stale1, 0)       // asked at 12:00, arrives at 12:30: stale
	h.permission(stale2, 0) // the same for a permission prompt
	h.peerPrompt(peers, 30*time.Minute)
	h.askQ(peers, 30*time.Minute+5*time.Second) // a peer turn ending on a question
	h.advanceTo(31 * time.Minute)
	h.expectSends(0)
	want := map[string]string{stale1: model.ReasonStale, stale2: model.ReasonStale, peers: model.ReasonPeers}

	var ov needsOverview
	r.must("GET", "/v1/overview", r.owner, nil, 200, &ov)
	if len(ov.NeedsYou) != 3 || ov.Counts.NeedsYou != 3 {
		t.Fatalf("overview before seen: %+v", ov)
	}
	ids := map[string]string{}
	for _, s := range ov.NeedsYou {
		if !s.NeedsYou || s.Alert == nil || s.Alert.Status != "skipped" || s.Alert.Reason == nil || *s.Alert.Reason != want[s.ID] {
			t.Fatalf("row %s: needs_you %v alert %+v, want skipped/%s", s.ID, s.NeedsYou, s.Alert, want[s.ID])
		}
		ids[s.ID] = s.Alert.ID
	}
	// A skipped alert never pushes: nothing to snooze.
	if st, _ := r.call("POST", "/v1/alerts/"+ids[peers]+"/snooze", r.phone, nil); st != 409 {
		t.Fatalf("snooze a skipped alert = %d, want 409", st)
	}
	for sid, id := range ids {
		var a alertJSON
		r.must("POST", "/v1/alerts/"+id+"/seen", r.phone, nil, 200, &a)
		if a.Status != "seen" || a.SeenAt == nil {
			t.Fatalf("seen %s: %+v", sid, a)
		}
	}
	ov = needsOverview{}
	r.must("GET", "/v1/overview", r.owner, nil, 200, &ov)
	if len(ov.NeedsYou) != 0 || ov.Counts.NeedsYou != 0 {
		t.Fatalf("overview after seen: %+v", ov)
	}
	var sess struct {
		State string     `json:"state"`
		Alert *alertJSON `json:"alert"`
	}
	r.must("GET", "/v1/sessions/"+peers, r.owner, nil, 200, &sess)
	if sess.State != "asked" || sess.Alert == nil || sess.Alert.Status != "seen" {
		t.Fatalf("session keeps its state with the seen brief: %+v", sess)
	}
	h.advanceTo(50 * time.Minute) // past every delay; not yet the machine's silent hour
	h.expectSends(0)

	// A new episode brings the row back with a new alert.
	h.answer(peers, 50*time.Minute)
	h.askQ(peers, 50*time.Minute+time.Second)
	h.advanceTo(50*time.Minute + 2*time.Second)
	ov = needsOverview{}
	r.must("GET", "/v1/overview", r.owner, nil, 200, &ov)
	if len(ov.NeedsYou) != 1 || ov.NeedsYou[0].ID != peers || ov.NeedsYou[0].Alert == nil || ov.NeedsYou[0].Alert.ID == ids[peers] {
		t.Fatalf("new episode: %+v", ov)
	}
}

// Row 51: a hub without the engine makes no alert, so the row is seen through the session.
func TestRow51SeenOnSessionWithoutAlert(t *testing.T) {
	r := newAPIRig(t, noStart())
	h := r.h
	h.askQ(s1, 0)
	h.answer(sid(2), 0) // working: does not need you
	var ov needsOverview
	r.must("GET", "/v1/overview", r.owner, nil, 200, &ov)
	if len(ov.NeedsYou) != 1 || ov.NeedsYou[0].Alert != nil {
		t.Fatalf("overview without the engine: %+v", ov)
	}
	if n := len(h.allAlerts()); n != 0 {
		t.Fatalf("%d alerts without the engine", n)
	}
	// Pushes off: the alert endpoints answer 503, the session one works.
	if st, _ := r.call("POST", "/v1/alerts/al_x/seen", r.phone, nil); st != 503 {
		t.Fatalf("alert seen without the engine = %d, want 503", st)
	}
	var a alertJSON
	r.must("POST", "/v1/sessions/"+s1+"/seen", r.phone, nil, 200, &a)
	if a.Status != "seen" || a.Trigger != "asked" || a.SeenAt == nil || a.ID == "" {
		t.Fatalf("session seen: %+v", a)
	}
	var seenBy string
	if err := h.st.R().QueryRow(`SELECT seen_by FROM alert WHERE id = ?`, a.ID).Scan(&seenBy); err != nil || seenBy != r.phoneID {
		t.Fatalf("seen_by %q (%v), want %q", seenBy, err, r.phoneID)
	}
	ov = needsOverview{}
	r.must("GET", "/v1/overview", r.owner, nil, 200, &ov)
	if len(ov.NeedsYou) != 0 || ov.Counts.NeedsYou != 0 {
		t.Fatalf("overview after session seen: %+v", ov)
	}
	// Again: a no-op on the same alert.
	var again alertJSON
	r.must("POST", "/v1/sessions/"+s1+"/seen", r.owner, nil, 200, &again)
	if again.ID != a.ID || again.Status != "seen" {
		t.Fatalf("seen again: %+v vs %+v", again, a)
	}
	if st, _ := r.call("POST", "/v1/sessions/"+sid(2)+"/seen", r.phone, nil); st != 409 {
		t.Fatalf("seen on a session that does not need you = %d, want 409", st)
	}
	if st, _ := r.call("POST", "/v1/sessions/nope/seen", r.phone, nil); st != 404 {
		t.Fatalf("seen on an unknown session = %d, want 404", st)
	}
	// The engine starts later: the episode already has its (seen) alert, so none is made or pushed.
	h.start()
	h.advanceTo(10 * time.Minute)
	h.expectSends(0)
	if got := h.allAlerts(); len(got) != 1 || got[0].ID != a.ID || got[0].Status != model.StatusSeen {
		t.Fatalf("alerts after the engine started: %s", h.dump())
	}
}

// With the engine, the session path is Seen on the episode's alert (pending or skipped).
func TestSeenSessionUsesTheEpisodeAlert(t *testing.T) {
	r := newAPIRig(t)
	h := r.h
	h.askQ(s1, 0)
	pending := h.alertOf(s1, model.TriggerAsked)
	if pending.Status != model.StatusPending {
		t.Fatal(h.dump())
	}
	var a alertJSON
	r.must("POST", "/v1/sessions/"+s1+"/seen", r.phone, nil, 200, &a)
	if a.ID != pending.ID || a.Status != "seen" {
		t.Fatalf("session seen on a pending alert: %+v", a)
	}
	h.advanceTo(5 * time.Minute)
	h.expectSends(0)

	h.peerPrompt(sid(2), 5*time.Minute)
	h.askQ(sid(2), 5*time.Minute+time.Second)
	h.advanceTo(6 * time.Minute)
	skipped := h.alertOf(sid(2), model.TriggerAsked)
	h.expectStatus(skipped, model.StatusSkipped, model.ReasonPeers)
	r.must("POST", "/v1/sessions/"+sid(2)+"/seen", r.phone, nil, 200, &a)
	if a.ID != skipped.ID || a.Status != "seen" {
		t.Fatalf("session seen on a skipped alert: %+v", a)
	}
	if n := len(h.allAlerts()); n != 2 {
		t.Fatalf("no extra alert: %s", h.dump())
	}
}
