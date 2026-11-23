package api_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

// relayEnv signs an envelope from machine A's agent (index 0) to sess-b on machine B.
func (h *resumeHub) relayEnv(t *testing.T, e peers.Envelope) peers.PeerAck {
	t.Helper()
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	doc := keys.NewDoc(keys.TypePeerEnvelope, body)
	if err := doc.Sign(keys.ByDevice, h.signs[0]); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(peers.PeerMsgUp{T: link.TypePeerMsg, Doc: doc})
	typ, ack, err := h.relay.Up(context.Background(), h.machines[0], link.Up{T: link.TypePeerMsg, Raw: raw})
	if err != nil || typ != link.TypePeerAck {
		t.Fatalf("relay: %q %v", typ, err)
	}
	return ack.(peers.PeerAck)
}

func (h *resumeHub) serviceEnv(msg string) peers.Envelope {
	now := time.Now()
	return peers.Envelope{V: 1, Msg: msg, Thread: resumeThread, Kind: peers.KindStart,
		From: peers.EnvFrom{Session: peers.ServiceSessionID(trustMachineA), Device: h.machines[0], Machine: trustMachineA, Account: "acct-a",
			Name: "doorbell", Handle: "doorbell", Mode: peers.ModePrompting, Kind: peers.ServiceDoorbell},
		To:  peers.EnvTo{Session: "sess-b", Device: h.machines[1], Machine: trustMachineB, Account: "acct-b"},
		Tag: "ci", CreatedAt: now.UTC().Format(time.RFC3339Nano), Exp: now.Add(time.Hour).Unix(), Sealed: "eA"}
}

func (h *resumeHub) hello(t *testing.T, device string, client link.Client) {
	t.Helper()
	j, _ := json.Marshal(map[string]any{"client": client})
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		return tx.LinkConnected(device, "ws", "", string(j), time.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
}

// A trusted sender's message goes only to an agent whose last hello carried peer_service
// (peers.md 8.3); a session's message does not depend on it.
func TestServiceEnvelopeNeedsReceiverCap(t *testing.T) {
	h := newResumeHub(t, true)
	seedTrust(t, h)
	ctx := context.Background()
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.AddPeerTag("sess-b", "ci", h.machines[1], peers.MaxTagsPerSession, time.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// No hello at all, then a hello without the cap: receiver_too_old.
	if ack := h.relayEnv(t, h.serviceEnv("pm-1")); ack.Status != "refused" || ack.Detail != peers.ReasonReceiverTooOld {
		t.Fatalf("no hello: %+v", ack)
	}
	h.hello(t, h.machines[1], link.Client{Kind: "agent", Wy: "1.2.0", Caps: []string{"something_else"}})
	if ack := h.relayEnv(t, h.serviceEnv("pm-2")); ack.Status != "refused" || ack.Detail != peers.ReasonReceiverTooOld {
		t.Fatalf("old receiver: %+v", ack)
	}
	// A session's message to the same old receiver is not refused for its age.
	sess := h.serviceEnv("pm-3")
	sess.From = peers.EnvFrom{Session: "sess-a", Device: h.machines[0], Machine: trustMachineA, Account: "acct-a", Name: "a", Handle: "a", Mode: peers.ModePrompting}
	sess.Tag = ""
	if ack := h.relayEnv(t, sess); ack.Detail == peers.ReasonReceiverTooOld {
		t.Fatalf("session envelope: %+v", ack)
	}

	// With the cap the check passes; the envelope is then refused further on (this fixture
	// binds no account to the machine).
	h.hello(t, h.machines[1], link.Client{Kind: "agent", Wy: "1.3.0", Caps: []string{link.CapPeerService}})
	if ack := h.relayEnv(t, h.serviceEnv("pm-4")); ack.Status != "refused" || ack.Detail != peers.ReasonUnboundAccount {
		t.Fatalf("new receiver: %+v", ack)
	}

	// A service envelope that names another machine's pseudo-session is refused forbidden.
	// ParseEnvelope already rejects it (the explicit check in the relay is the second line).
	bad := h.serviceEnv("pm-5")
	bad.From.Session = peers.ServiceSessionID(trustMachineB)
	if ack := h.relayEnv(t, bad); ack.Status != "refused" || ack.Detail != "forbidden" {
		t.Fatalf("foreign service session: %+v", ack)
	}
	bad = h.serviceEnv("pm-6")
	bad.From.Machine = trustMachineB
	bad.From.Session = peers.ServiceSessionID(trustMachineB)
	if ack := h.relayEnv(t, bad); ack.Status != "refused" || ack.Detail != "forbidden" {
		t.Fatalf("foreign service machine: %+v", ack)
	}
}
