package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/pair"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
)

func TestPeersMatrixEndpoints(t *testing.T) {
	h := newKeysHub(t)
	ctx := context.Background()
	trust := func() keys.Trust {
		tr, err := h.ks.Trust(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	matrix := func(m map[string]any) keys.MatrixBody {
		t.Helper()
		s, err := keys.VerifyMatrix(asDoc(t, m["doc"]), trust())
		if err != nil {
			t.Fatalf("the returned document does not verify: %v", err)
		}
		return s.Body
	}

	// A machine (write scope) and a phone (push scope).
	mc, _ := h.pair.Create(ctx, pair.CreateRequest{Kind: "machine", Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", URL: "https://hub.example.ts.net"})
	_, reply := h.call("POST", "/v1/pair/claim", "", map[string]any{"code": mc.Code, "keys": testMachineKeys(t)})
	machine := reply["token"].(string)
	pc, _ := h.pair.Create(ctx, pair.CreateRequest{Kind: "phone", URL: "https://hub.example.ts.net"})
	_, reply = h.call("POST", "/v1/pair/claim", "", map[string]any{"secret": pc.Secret})
	phone, _ := reply["token"].(string)
	if phone == "" {
		t.Fatalf("no phone token: %v", reply)
	}

	// Owner key proposed by the machine and confirmed by an owner session.
	ownerPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))
	odoc, _ := keys.NewOwnerKeyDoc(ownerPriv, h.ks.Key.FP, time.Now())
	if st, _ := h.call("POST", "/v1/keys/owner", machine, map[string]any{"doc": odoc}); st != 201 {
		t.Fatal("propose", st)
	}
	fp := keys.Fingerprint(ownerPriv.Public().(ed25519.PublicKey))
	if st, _ := h.call("POST", "/v1/keys/owner/confirm", h.owner, map[string]any{"fp": fp}); st != 200 {
		t.Fatal("confirm", st)
	}

	// First read: the hub makes version 1 (nothing allowed, same account on, 30 turns).
	st, m := h.call("GET", "/v1/peers/matrix", machine, nil)
	if st != 200 || m["signer"] != "hub" {
		t.Fatalf("GET: %d %v", st, m)
	}
	v1 := matrix(m)
	if v1.Version != 1 || len(v1.Allow) != 0 || !v1.SameAccount || v1.Paused || v1.MaxTurns != keys.NoTurnLimit {
		t.Fatalf("version 1: %+v", v1)
	}
	if _, m2 := h.call("GET", "/v1/peers/matrix", phone, nil); matrix(m2).Version != 1 {
		t.Fatal("a second read must not make another version")
	}

	// Widening edits: only an owner session. Machines and phones get 403.
	allowAB := map[string]any{"edit": map[string]any{"allow": [][]string{{"A", "B"}}}}
	for name, tok := range map[string]string{"write token": machine, "device": phone} {
		if st, _ := h.call("PUT", "/v1/peers/matrix", tok, allowAB); st != 403 {
			t.Errorf("%s widening edit: %d, want 403", name, st)
		}
	}
	st, m = h.call("PUT", "/v1/peers/matrix", h.owner, allowAB)
	if st != 200 || m["signer"] != "hub" || m["widens"] != true {
		t.Fatalf("owner session widening: %d %v", st, m)
	}
	if b := matrix(m); b.Version != 2 || !b.Allows("A", "B") {
		t.Fatalf("v2 %+v", b)
	}

	// An owner-signed document is taken from any caller with the route's scopes.
	next := func(version int, mut func(*keys.MatrixBody)) keys.Doc {
		b := v1
		b.Version, b.IssuedAt = version, keys.FormatTime(time.Now())
		b.Allow = [][]string{{"A", "B"}, {"B", "A"}}
		if mut != nil {
			mut(&b)
		}
		raw, _ := json.Marshal(b)
		d := keys.NewDoc(keys.TypeMatrix, raw)
		if err := d.Sign(keys.ByOwner, ownerPriv); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if st, _ := h.call("PUT", "/v1/peers/matrix", machine, map[string]any{"doc": next(5, nil)}); st != 409 {
		t.Errorf("skipped version: %d, want 409", st)
	}
	st, m = h.call("PUT", "/v1/peers/matrix", machine, map[string]any{"doc": next(3, nil)})
	if st != 200 || m["signer"] != "owner" || m["widens"] != true {
		t.Fatalf("owner doc via a machine: %d %v", st, m)
	}
	// A document signed by another key, or by the hub, is no owner document.
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	bad := next(4, nil)
	bad.Sigs = nil
	raw, _ := bad.BodyBytes()
	bad.Sigs = []keys.Sig{keys.SignDoc(keys.TypeMatrix, raw, keys.ByOwner, other)}
	if st, _ := h.call("PUT", "/v1/peers/matrix", machine, map[string]any{"doc": bad}); st != 400 {
		t.Errorf("document signed by the wrong key: %d, want 400", st)
	}
	hubSigned, _ := h.ks.SignMatrix(func() keys.MatrixBody { b := v1; b.Version = 4; return b }())
	if st, _ := h.call("PUT", "/v1/peers/matrix", machine, map[string]any{"doc": hubSigned}); st != 400 {
		t.Errorf("hub-signed document in doc: %d, want 400", st)
	}
	if st, _ := h.call("PUT", "/v1/peers/matrix", machine, map[string]any{}); st != 400 {
		t.Errorf("neither doc nor edit: %d, want 400", st)
	}

	// Narrowing: any device, hub-signed. base_version is honoured.
	stale := map[string]any{"edit": map[string]any{"base_version": 1, "deny": [][]string{{"A", "*"}}}}
	if st, _ := h.call("PUT", "/v1/peers/matrix", machine, stale); st != 409 {
		t.Errorf("stale base_version: %d, want 409", st)
	}
	st, m = h.call("PUT", "/v1/peers/matrix", phone, map[string]any{"edit": map[string]any{"base_version": 3, "deny": [][]string{{"A", "*"}}}})
	if b := matrix(m); st != 200 || m["signer"] != "hub" || m["widens"] != false || b.Allows("A", "B") || !b.Allows("B", "A") {
		t.Fatalf("deny A->*: %d %v", st, m)
	}

	// The kill switch: any device pauses, only an owner session resumes.
	st, m = h.call("POST", "/v1/peers/pause", phone, map[string]any{"paused": true})
	if st != 200 || !matrix(m).Paused {
		t.Fatalf("pause: %d %v", st, m)
	}
	if st, _ := h.call("POST", "/v1/peers/pause", machine, map[string]any{"paused": false}); st != 403 {
		t.Errorf("resume by a machine: %d, want 403", st)
	}
	if st, _ := h.call("POST", "/v1/peers/pause", machine, map[string]any{}); st != 400 {
		t.Errorf("pause without paused: %d, want 400", st)
	}
	st, m = h.call("POST", "/v1/peers/pause", h.owner, map[string]any{"paused": false})
	if st != 200 || matrix(m).Paused {
		t.Fatalf("resume by an owner session: %d %v", st, m)
	}

	// The turn limit: 0 is no limit and counts as the highest value (keys.md 5.2).
	turns := func(n int) map[string]any { return map[string]any{"edit": map[string]any{"max_turns": n}} }
	st, m = h.call("PUT", "/v1/peers/matrix", phone, turns(30))
	if st != 200 || m["widens"] != false || matrix(m).MaxTurns != 30 {
		t.Fatalf("no limit → 30 from a phone (narrowing): %d %v", st, m)
	}
	if st, _ := h.call("PUT", "/v1/peers/matrix", phone, turns(0)); st != 403 {
		t.Errorf("30 → no limit from a phone: %d, want 403", st)
	}
	for _, n := range []int{1, -1, 501} {
		if st, _ := h.call("PUT", "/v1/peers/matrix", h.owner, turns(n)); st != 400 {
			t.Errorf("max_turns %d: %d, want 400", n, st)
		}
	}
	st, m = h.call("PUT", "/v1/peers/matrix", h.owner, turns(0))
	if st != 200 || m["widens"] != true || matrix(m).MaxTurns != keys.NoTurnLimit {
		t.Fatalf("30 → no limit from an owner session: %d %v", st, m)
	}
	st, m = h.call("PUT", "/v1/peers/matrix", machine, turns(500))
	if st != 200 || m["widens"] != false || matrix(m).MaxTurns != 500 {
		t.Fatalf("no limit → 500 from a machine (narrowing): %d %v", st, m)
	}

	// Audit: owner session and viewers read it; machines and phones do not. Metadata only.
	if st, _ := h.call("GET", "/v1/peers/audit", machine, nil); st != 403 {
		t.Errorf("audit read by a machine: %d, want 403", st)
	}
	st, m = h.call("GET", "/v1/peers/audit?limit=50", h.owner, nil)
	entries, _ := m["entries"].([]any)
	if st != 200 || len(entries) < 5 {
		t.Fatalf("audit: %d %v", st, m)
	}
	events := map[string]int{}
	for _, e := range entries {
		em := e.(map[string]any)
		events[em["event"].(string)]++
		for k := range em["detail"].(map[string]any) {
			switch k {
			case "version", "signer", "widens", "paused", "same_account", "max_turns", "pairs":
			default:
				t.Errorf("unexpected audit detail field %q (metadata only)", k)
			}
		}
	}
	if events["pause"] != 1 || events["resume"] != 1 || events["matrix"] < 3 {
		t.Errorf("events %v", events)
	}
	if st, _ := h.call("GET", "/v1/peers/audit?since=garbage", h.owner, nil); st != 400 {
		t.Errorf("bad since: %d", st)
	}
	if st, m := h.call("GET", "/v1/peers/audit?since="+time.Now().Add(time.Hour).UTC().Format(time.RFC3339), h.owner, nil); st != 200 || len(m["entries"].([]any)) != 0 {
		t.Errorf("since in the future: %d %v", st, m)
	}
}

// GET /v1/peers lists live sessions with handles; GET /v1/peers/messages is metadata only.
func TestPeersDirectoryAndMessages(t *testing.T) {
	h := newKeysHub(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	const machine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"
	err := h.st.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.EnsureMachine(machine, "gpu-box", now); err != nil {
			return err
		}
		seat, err := tx.UpsertSeat(store.SeatKey{Account: "acc-1", Org: "org-1"}, now)
		if err != nil {
			return err
		}
		if err := tx.SeatLook(seat, "work", "", nil); err != nil {
			return err
		}
		for _, id := range []string{"sess-1", "sess-2"} {
			if err := tx.InsertSession(store.SessionRow{ID: id, SeatID: seat, MachineID: machine, Name: "api", Project: "~/src/api",
				StartedAt: now, LastEventAt: now}); err != nil {
				return err
			}
		}
		if err := tx.PutFold("sess-2", store.FoldRow{State: "ended", Since: now, UpdatedAt: now, LastEventAt: now}); err != nil {
			return err
		}
		return tx.InsertPeerMessage(store.PeerMessageRow{ID: "msg-1", Thread: "t_0123456789abcdef", FromSession: "sess-1", FromDevice: "dev_a",
			FromAcct: "acc-1", ToSession: "sess-9", ToDevice: "dev_b", ToAcct: "acc-2", Kind: "start", Size: 12, Status: "queued",
			CreatedAt: now, ExpiresAt: now + 3600_000})
	})
	if err != nil {
		t.Fatal(err)
	}
	st, m := h.call("GET", "/v1/peers", h.owner, nil)
	ss, _ := m["sessions"].([]any)
	if st != 200 || len(ss) != 1 {
		t.Fatalf("peers: %d %v", st, m)
	}
	s := ss[0].(map[string]any)
	if s["handle"] != "api@work.gpu-box" || s["machine"] != machine || s["machine_name"] != "gpu-box" || s["agent"] != false || s["state"] != "working" {
		t.Errorf("session %v", s)
	}
	st, m = h.call("GET", "/v1/peers/messages?session=sess-1", h.owner, nil)
	ms, _ := m["messages"].([]any)
	if st != 200 || len(ms) != 1 {
		t.Fatalf("messages: %d %v", st, m)
	}
	msg := ms[0].(map[string]any)
	for k := range msg {
		switch k {
		case "id", "thread", "from", "to", "kind", "size", "local", "status", "detail", "created_at", "final_at", "from_kind", "via_tag":
		default:
			t.Errorf("unexpected message field %q (metadata only)", k)
		}
	}
	if msg["from"].(map[string]any)["account"] != "acc-1" || msg["status"] != "queued" || msg["final_at"] != nil {
		t.Errorf("message %v", msg)
	}
	if n, ok := msg["from"].(map[string]any)["device_name"]; !ok || n != "" {
		t.Errorf("a device the hub does not know has an empty device_name: %v", msg["from"])
	}
	if st, _ := h.call("GET", "/v1/peers/messages?limit=0", h.owner, nil); st != 400 {
		t.Errorf("limit 0: %d", st)
	}
}
