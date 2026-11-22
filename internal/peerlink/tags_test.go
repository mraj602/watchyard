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

// tagHub answers every peer_tag at once with a `peers` frame holding the tag and the
// directory, as the hub does (link.md 6.2), or with nothing (an old hub's notice).
type tagHub struct {
	r   *Remote
	old bool
	got []peers.PeerTagUp
}

func (h *tagHub) Send(_ context.Context, v any) error {
	q, ok := v.(peers.PeerTagUp)
	if !ok {
		return nil
	}
	h.got = append(h.got, q)
	if h.old {
		b, _ := json.Marshal(link.Notice{V: 1, Code: link.NoticeUnknownType})
		go h.r.Ephemeral(link.TypeNotice, b)
		return nil
	}
	rep := peers.PeersReply{V: 1, ID: q.ID, Tag: &peers.TagAnswer{Session: q.Session, Tags: []string{q.Tag}, Changed: true},
		Mine: []peers.WireSession{{Session: q.Session, Handle: "api@work.laptop", MachineID: "m_1", Tags: []string{q.Tag}}}}
	b, _ := json.Marshal(rep)
	go h.r.Ephemeral(link.TypePeers, b)
	return nil
}

func TestTagRoundTrip(t *testing.T) {
	r := New(peers.Env{State: t.TempDir(), Machine: identity.Machine{ID: "m_1"}}, nil)
	r.ListWait = 2 * time.Second
	h := &tagHub{r: r}
	r.Link = h
	ans, err := r.Tag("sess-a", peers.TagAdd, "build")
	if err != nil || !ans.Changed || len(ans.Tags) != 1 || ans.Tags[0] != "build" {
		t.Fatalf("%+v %v", ans, err)
	}
	if len(h.got) != 1 || h.got[0].T != link.TypePeerTag || h.got[0].Op != peers.TagAdd || h.got[0].Session != "sess-a" {
		t.Fatalf("frame: %+v", h.got)
	}
	// The answer replaced the cached directory: Names has the tag at once.
	if n := r.Names()["sess-a"]; !n.Mine || len(n.Tags) != 1 || n.Tags[0] != "build" {
		t.Fatalf("names: %+v", n)
	}
	h.old = true
	if _, err := r.Tag("sess-a", peers.TagAdd, "x"); err == nil {
		t.Fatal("an old hub's notice must be an error")
	}
}
