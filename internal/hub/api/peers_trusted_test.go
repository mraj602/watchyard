package api_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
)

const trustMachineA, trustMachineB = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4A", "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4B"

// seedTrust gives machine A an approved Doorbell (as acct-a, whose seat is labelled "work") and
// two of its trusted sends, to tags ci and deploy.
func seedTrust(t *testing.T, h *resumeHub) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	err := h.st.Write(ctx, func(tx *store.Tx) error {
		seat, err := tx.UpsertSeat(store.SeatKey{Account: "acct-a", Org: "org-1", Email: "acct-a@example.test"}, now.UnixMilli())
		if err != nil {
			return err
		}
		if err := tx.SeatLook(seat, "work", "", nil); err != nil {
			return err
		}
		if _, err := tx.PutPeerTrust(store.PeerTrustRow{Machine: trustMachineA, Sender: "doorbell", Device: h.machines[0], Account: "acct-a",
			Program: "/v/bin/doorbell", Interpreter: "/py/bin/python3", Venv: "/v", ApprovedAt: now.Add(-time.Hour).UnixMilli()}, now.UnixMilli()); err != nil {
			return err
		}
		for i, tag := range []string{"ci", "deploy"} {
			if err := tx.InsertPeerMessage(store.PeerMessageRow{ID: "msg-" + tag, Thread: "t_" + tag, FromSession: "svc-doorbell-" + trustMachineA,
				FromDevice: h.machines[0], FromAcct: "acct-a", ToSession: "sess-b", ToDevice: h.machines[1], ToAcct: "acct-b", Kind: "start",
				Size: 5, Status: "delivered", CreatedAt: now.Add(-time.Duration(i+1) * time.Minute).UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(),
				FromKind: "service:doorbell", ViaTag: tag}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// GET /v1/peers/trusted lists each machine's approved Doorbell with its account label and
// recent use; POST /v1/peers/trusted/revoke is the owner's (or the paired phone's) to make.
func TestTrustedSenders(t *testing.T) {
	h := newResumeHub(t, true)
	ctx := context.Background()

	// Nothing approved: an empty list.
	if st, m := h.call("GET", "/v1/peers/trusted", h.owner, nil); st != 200 || len(m["senders"].([]any)) != 0 {
		t.Fatalf("empty list: %d %v", st, m)
	}
	seedTrust(t, h)

	st, m := h.call("GET", "/v1/peers/trusted", h.owner, nil)
	senders, _ := m["senders"].([]any)
	if st != 200 || len(senders) != 1 {
		t.Fatalf("list: %d %v", st, m)
	}
	e := senders[0].(map[string]any)
	if e["sender"] != "doorbell" || e["machine"] != trustMachineA || e["account"] != "acct-a" || e["account_label"] != "work" ||
		e["program"] != "/v/bin/doorbell" || e["interpreter"] != "/py/bin/python3" || e["venv"] != "/v" ||
		e["revoked_at"] != nil || e["revoked_by"] != nil || e["last_send_at"] == nil || e["approved_at"] == nil {
		t.Fatalf("entry: %v", e)
	}
	if tags, _ := e["recent_tags"].([]any); len(tags) != 2 || tags[0].(map[string]any)["tag"] != "ci" || tags[1].(map[string]any)["tag"] != "deploy" {
		t.Errorf("recent_tags: %v", e["recent_tags"])
	}
	if n, _ := e["sent_today"].(float64); n < 0 || n > 2 {
		t.Errorf("sent_today: %v", e["sent_today"])
	}

	// Who may read: owner and read-only sign-ins; a machine may not.
	read, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "browser", Name: "board", CreatedBy: "login:dev_x", Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	machine, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "machine", Name: "c", Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4C"})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := h.call("GET", "/v1/peers/trusted", read.Token, nil); st != 200 {
		t.Errorf("read-only list: %d", st)
	}
	if st, _ := h.call("GET", "/v1/peers/trusted", machine.Token, nil); st != 403 {
		t.Errorf("machine list: %d, want 403", st)
	}
	if st, _ := h.call("GET", "/v1/peers/trusted", "", nil); st != 401 {
		t.Errorf("no token: %d, want 401", st)
	}

	// Revoke: not for a read-only sign-in or a machine.
	body := map[string]string{"machine": trustMachineA, "sender": "doorbell"}
	for name, tok := range map[string]string{"read": read.Token, "machine": machine.Token} {
		if st, _ := h.call("POST", "/v1/peers/trusted/revoke", tok, body); st != 403 {
			t.Errorf("%s revoke: %d, want 403", name, st)
		}
	}
	if rows, _ := h.st.MachinePeerTrust(ctx, trustMachineA); len(rows) != 1 || !rows[0].Active() {
		t.Fatalf("a refused call changed the row: %+v", rows)
	}
	if entries, _ := h.st.PeerAudit(ctx, 0, 10); len(entries) != 0 {
		t.Fatalf("a refused call wrote audit rows: %+v", entries)
	}

	// Bad bodies: 400. An unknown machine: 404.
	for name, b := range map[string]any{
		"no machine":    map[string]string{"sender": "doorbell"},
		"bad sender":    map[string]string{"machine": trustMachineA, "sender": "slack"},
		"no sender":     map[string]string{"machine": trustMachineA},
		"not an object": []string{"x"},
	} {
		if st, _ := h.call("POST", "/v1/peers/trusted/revoke", h.owner, b); st != 400 {
			t.Errorf("%s: %d, want 400", name, st)
		}
	}
	if st, _ := h.call("POST", "/v1/peers/trusted/revoke", h.owner, map[string]string{"machine": "m_unknown", "sender": "doorbell"}); st != 404 {
		t.Errorf("unknown machine: %d, want 404", st)
	}
	// A machine that exists but has no trusted sender is the same 404.
	if st, _ := h.call("POST", "/v1/peers/trusted/revoke", h.owner, map[string]string{"machine": trustMachineB, "sender": "doorbell"}); st != 404 {
		t.Errorf("machine without a row: %d, want 404", st)
	}

	// The owner revokes: 200 with the entry; again: 200 and the first revocation stands.
	st, first := h.call("POST", "/v1/peers/trusted/revoke", h.owner, body)
	if st != 200 || first["revoked_at"] == nil || first["revoked_by"] == nil || first["machine"] != trustMachineA || first["account_label"] != "work" {
		t.Fatalf("revoke: %d %v", st, first)
	}
	st, again := h.call("POST", "/v1/peers/trusted/revoke", h.owner, body)
	if st != 200 || again["revoked_at"] != first["revoked_at"] || again["revoked_by"] != first["revoked_by"] {
		t.Fatalf("revoke again: %d %v (first %v)", st, again, first)
	}
	rows, err := h.st.MachinePeerTrust(ctx, trustMachineA)
	if err != nil || len(rows) != 1 || rows[0].Active() || rows[0].RevokedBy == "" || rows[0].RevokedBy != first["revoked_by"] {
		t.Fatalf("row: %+v %v", rows, err)
	}
	// The list shows it revoked.
	if _, m := h.call("GET", "/v1/peers/trusted", h.owner, nil); m["senders"].([]any)[0].(map[string]any)["revoked_at"] == nil {
		t.Errorf("list after revoke: %v", m)
	}

	// One trust_revoke audit row, for the first revocation only, naming the machine and sender.
	entries, err := h.st.PeerAudit(ctx, 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Event != "trust_revoke" || entries[0].Actor != rows[0].RevokedBy {
		t.Fatalf("audit: %+v %v", entries, err)
	}
	var d map[string]string
	if err := json.Unmarshal(entries[0].Detail, &d); err != nil || d["machine"] != trustMachineA || d["sender"] != "doorbell" {
		t.Errorf("audit detail %s (%v)", entries[0].Detail, err)
	}

	// The owner's paired phone may revoke too (it only narrows): a second machine's row.
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.PutPeerTrust(store.PeerTrustRow{Machine: trustMachineB, Sender: "doorbell", Device: h.machines[1], Account: "acct-b",
			Program: "/v/bin/doorbell", Interpreter: "/py/bin/python3", Venv: "/v", ApprovedAt: time.Now().UnixMilli()}, time.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	phone, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "phone", Name: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	if st, m := h.call("POST", "/v1/peers/trusted/revoke", phone.Token, map[string]string{"machine": trustMachineB, "sender": "doorbell"}); st != 200 ||
		m["revoked_by"] != phone.Device.ID {
		t.Errorf("phone revoke: %d %v", st, m)
	}
}
