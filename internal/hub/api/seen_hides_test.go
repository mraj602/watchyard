package api_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
)

// A session whose current alert is seen leaves the overview's needs-you list and count until
// its state changes; the session lists still show it (push.md 4, states.md 1).
func TestSeenHidesFromNeedsYou(t *testing.T) {
	h := startHub(t, "")
	tok, desktop := h.token("machine"), h.token("desktop")
	now := time.Now().UTC().Truncate(time.Second)
	h.ingest(tok, simpleHook(t, h.machine, "s-w", "SessionStart", now))
	perm := func(at time.Time) []byte {
		p, _ := json.Marshal(map[string]any{"session_id": "s-w", "hook_event_name": "PermissionRequest", "cwd": "/home/u/proj",
			"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
		return hookRecord(t, h.machine, at, p)
	}
	h.ingest(tok, perm(now.Add(time.Second)))

	type ovT struct {
		NeedsYou []struct {
			ID string `json:"id"`
		} `json:"needs_you"`
		Counts struct {
			NeedsYou int `json:"needs_you"`
			Live     int `json:"live"`
		} `json:"counts"`
	}
	var ov ovT
	h.get("/v1/overview", desktop, &ov)
	if len(ov.NeedsYou) != 1 || ov.Counts.NeedsYou != 1 {
		t.Fatalf("before seen: %+v", ov)
	}

	var episode string
	if err := h.st.R().QueryRow(`SELECT 's:' || session_id || ':' || state || ':' || since_at FROM session_state WHERE session_id = 's-w'`).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO alert (id, episode, trigger, session_id, status, created_at, seen_at, updated_at)
			VALUES ('al_1', ?, 'needs_permission', 's-w', 'seen', 1, 2, 2)`, episode)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ov = ovT{}
	h.get("/v1/overview", desktop, &ov)
	if len(ov.NeedsYou) != 0 || ov.Counts.NeedsYou != 0 || ov.Counts.Live != 1 {
		t.Fatalf("after seen: %+v", ov)
	}
	var list struct {
		Sessions []struct {
			ID       string `json:"id"`
			State    string `json:"state"`
			NeedsYou bool   `json:"needs_you"`
		} `json:"sessions"`
	}
	h.get("/v1/sessions", desktop, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].State != "needs_permission" || !list.Sessions[0].NeedsYou {
		t.Fatalf("session list keeps the real state: %+v", list.Sessions)
	}

	// The turn resumes and asks again: a new episode, so it needs you again.
	up, _ := json.Marshal(map[string]any{"session_id": "s-w", "hook_event_name": "UserPromptSubmit", "cwd": "/home/u/proj", "prompt": "go"})
	h.ingest(tok, hookRecord(t, h.machine, now.Add(2*time.Second), up))
	h.ingest(tok, perm(now.Add(3*time.Second)))
	ov = ovT{}
	h.get("/v1/overview", desktop, &ov)
	if len(ov.NeedsYou) != 1 || ov.Counts.NeedsYou != 1 {
		t.Fatalf("new state un-hides: %+v", ov)
	}
}
