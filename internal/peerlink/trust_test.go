package peerlink

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

// trustSink records the peer_trust frames the Remote sends.
type trustSink struct{ got []peers.PeerTrustUp }

func (s *trustSink) Send(_ context.Context, v any) error {
	if up, ok := v.(peers.PeerTrustUp); ok {
		s.got = append(s.got, up)
	}
	return nil
}

var trustApproved = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

// trustRemote is a Remote with a local Doorbell record approved at trustApproved (none when
// noRecord) and a sink for what it reports.
func trustRemote(t *testing.T, noRecord bool) (*Remote, *trustSink) {
	t.Helper()
	env := peers.Env{State: t.TempDir(), Machine: identity.Machine{ID: "m_1", Name: "laptop"}}
	if !noRecord {
		rec := peers.TrustRecord{Venv: "/v", Program: "/v/bin/doorbell", Interpreter: "/py/bin/python3", Machine: "m_1",
			Account: "acc-work", ApprovedAt: trustApproved}
		if err := env.SaveTrust(&rec); err != nil {
			t.Fatal(err)
		}
	}
	r := New(env, nil)
	sink := &trustSink{}
	r.Link = sink
	return r, sink
}

func trustAnswer(approved time.Time, revoked bool) *peers.PeersReply {
	st := peers.TrustState{ApprovedAt: approved.Format(time.RFC3339)}
	if revoked {
		at := approved.Add(time.Hour).Format(time.RFC3339)
		st.RevokedAt = &at
	}
	return &peers.PeersReply{V: 1, Trust: map[string]peers.TrustState{peers.TrustDoorbell: st}}
}

func hasRecord(t *testing.T, r *Remote) bool {
	t.Helper()
	rec, err := r.Env.LoadTrust()
	if err != nil {
		t.Fatal(err)
	}
	return rec != nil
}

func TestReconcileTrust(t *testing.T) {
	t.Run("revoked with the same approval removes the record", func(t *testing.T) {
		r, sink := trustRemote(t, false)
		r.reconcileTrust(trustAnswer(trustApproved, true))
		if hasRecord(t, r) {
			t.Error("record kept")
		}
		if len(sink.got) != 0 {
			t.Errorf("reported %+v", sink.got)
		}
	})
	t.Run("revoked with a newer approval removes the record", func(t *testing.T) {
		r, _ := trustRemote(t, false)
		r.reconcileTrust(trustAnswer(trustApproved.Add(time.Minute), true))
		if hasRecord(t, r) {
			t.Error("record kept")
		}
	})
	t.Run("revoked at an older approval keeps the record and reports it", func(t *testing.T) {
		r, sink := trustRemote(t, false)
		r.reconcileTrust(trustAnswer(trustApproved.Add(-time.Hour), true))
		if !hasRecord(t, r) {
			t.Fatal("a re-approved record was removed")
		}
		if len(sink.got) != 1 || sink.got[0].Op != "set" || sink.got[0].Account != "acc-work" ||
			sink.got[0].ApprovedAt != trustApproved.Format(time.RFC3339) || sink.got[0].T != link.TypePeerTrust ||
			sink.got[0].Venv != "/v" || sink.got[0].Program != "/v/bin/doorbell" || sink.got[0].Interpreter != "/py/bin/python3" {
			t.Errorf("reported %+v", sink.got)
		}
	})
	t.Run("active row with the same approval does nothing", func(t *testing.T) {
		r, sink := trustRemote(t, false)
		r.reconcileTrust(trustAnswer(trustApproved, false))
		if !hasRecord(t, r) || len(sink.got) != 0 {
			t.Errorf("record %v, reported %+v", hasRecord(t, r), sink.got)
		}
	})
	t.Run("the hub lacks the row: reported", func(t *testing.T) {
		r, sink := trustRemote(t, false)
		r.reconcileTrust(&peers.PeersReply{V: 1, Trust: map[string]peers.TrustState{}})
		if !hasRecord(t, r) || len(sink.got) != 1 || sink.got[0].Op != "set" {
			t.Errorf("record %v, reported %+v", hasRecord(t, r), sink.got)
		}
	})
	t.Run("the hub holds an older active approval: reported", func(t *testing.T) {
		r, sink := trustRemote(t, false)
		r.reconcileTrust(trustAnswer(trustApproved.Add(-time.Hour), false))
		if !hasRecord(t, r) || len(sink.got) != 1 || sink.got[0].Op != "set" {
			t.Errorf("record %v, reported %+v", hasRecord(t, r), sink.got)
		}
	})
	t.Run("no local record and an active row: off reported", func(t *testing.T) {
		r, sink := trustRemote(t, true)
		r.reconcileTrust(trustAnswer(trustApproved, false))
		if len(sink.got) != 1 || sink.got[0].Op != "off" || sink.got[0].Sender != peers.TrustDoorbell || sink.got[0].Account != "" {
			t.Errorf("reported %+v", sink.got)
		}
	})
	t.Run("no local record and a revoked row: nothing", func(t *testing.T) {
		r, sink := trustRemote(t, true)
		r.reconcileTrust(trustAnswer(trustApproved, true))
		if len(sink.got) != 0 {
			t.Errorf("reported %+v", sink.got)
		}
	})
	t.Run("no local record and no row: nothing", func(t *testing.T) {
		r, sink := trustRemote(t, true)
		r.reconcileTrust(&peers.PeersReply{V: 1, Trust: map[string]peers.TrustState{}})
		if len(sink.got) != 0 {
			t.Errorf("reported %+v", sink.got)
		}
	})
	t.Run("an answer without trust and no local record does nothing", func(t *testing.T) {
		r, sink := trustRemote(t, true)
		r.reconcileTrust(&peers.PeersReply{V: 1})
		r.reconcileTrust(nil)
		if hasRecord(t, r) || len(sink.got) != 0 {
			t.Errorf("record %v, reported %+v", hasRecord(t, r), sink.got)
		}
	})
	// The hub omits trust when it has no row (link.md 6.3), so for a machine with a record an
	// answer without it is the hub lacking the row: the record is kept and reported, once.
	t.Run("an answer without trust and a local record: kept and reported", func(t *testing.T) {
		r, sink := trustRemote(t, false)
		r.reconcileTrust(&peers.PeersReply{V: 1})
		if !hasRecord(t, r) || len(sink.got) != 1 || sink.got[0].Op != "set" {
			t.Errorf("record %v, reported %+v", hasRecord(t, r), sink.got)
		}
		r.reconcileTrust(nil)
		if len(sink.got) != 1 {
			t.Errorf("a nil answer reported: %+v", sink.got)
		}
	})
}

// A fresh directory read applies the revocation through directory().
func TestDirectoryAppliesTrustRevocation(t *testing.T) {
	r, _ := trustRemote(t, false)
	r.ListWait = 2 * time.Second
	r.Link = trustDirHub{r: r, rep: trustAnswer(trustApproved, true)}
	if _, err := r.Sessions(); err != nil {
		t.Fatal(err)
	}
	if hasRecord(t, r) {
		t.Error("record kept after a directory answer that revokes it")
	}
}

// trustDirHub answers every peers_list with rep.
type trustDirHub struct {
	r   *Remote
	rep *peers.PeersReply
}

func (h trustDirHub) Send(_ context.Context, v any) error {
	q, ok := v.(peers.PeersListUp)
	if !ok {
		return nil
	}
	rep := *h.rep
	rep.ID = q.ID
	b, _ := json.Marshal(rep)
	go h.r.Ephemeral(link.TypePeers, b)
	return nil
}
