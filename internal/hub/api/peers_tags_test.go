package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

// Session tags at the hub (peers.md 3.4, link.md 6.2 peer_tag): a machine's agent tags only
// its own live sessions, the answer is the directory after the change, every machine's
// directory and GET /v1/peers carry the tags, and a session that ends loses them.
func TestPeerTags(t *testing.T) {
	h := newResumeHub(t, true)
	ctx := context.Background()
	a, b := h.machines[0], h.machines[1]
	tag := func(device, session, op, tag string) peers.PeersReply {
		t.Helper()
		raw, _ := json.Marshal(peers.PeerTagUp{T: link.TypePeerTag, ID: "q1", Session: session, Op: op, Tag: tag})
		typ, body, err := h.relay.Up(ctx, device, link.Up{T: link.TypePeerTag, Raw: raw})
		if err != nil {
			t.Fatal(err)
		}
		rep, ok := body.(peers.PeersReply)
		if typ != link.TypePeers || !ok || rep.ID != "q1" || rep.Tag == nil {
			t.Fatalf("answer: %s %#v", typ, body)
		}
		return rep
	}
	tagsOf := func(ws []peers.WireSession, id string) string {
		for _, w := range ws {
			if w.Session == id {
				return strings.Join(w.Tags, ",")
			}
		}
		return "<absent>"
	}

	rep := tag(a, "sess-a", peers.TagAdd, "build")
	if rep.Tag.Error != "" || !rep.Tag.Changed || strings.Join(rep.Tag.Tags, ",") != "build" || tagsOf(rep.Mine, "sess-a") != "build" {
		t.Fatalf("add: %+v mine %v", rep.Tag, rep.Mine)
	}
	if rep := tag(a, "sess-a", peers.TagAdd, "build"); rep.Tag.Changed || rep.Tag.Error != "" {
		t.Fatalf("add again: %+v", rep.Tag)
	}
	tag(a, "sess-a", peers.TagAdd, "ci")

	// The other machine sees them in its directory.
	raw, _ := json.Marshal(peers.PeersListUp{T: link.TypePeersList, ID: "l1"})
	_, body, err := h.relay.Up(ctx, b, link.Up{T: link.TypePeersList, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	if got := tagsOf(body.(peers.PeersReply).Sessions, "sess-a"); got != "build,ci" {
		t.Fatalf("B's directory: %s", got)
	}

	// Refusals: another machine's session, a bad tag, an unknown session, the cap.
	if rep := tag(b, "sess-a", peers.TagAdd, "evil"); rep.Tag.Error != peers.TagErrForbidden {
		t.Errorf("other machine: %+v", rep.Tag)
	}
	if rep := tag(a, "sess-a", peers.TagAdd, "Bad"); rep.Tag.Error != peers.TagErrBadTag {
		t.Errorf("bad tag: %+v", rep.Tag)
	}
	if rep := tag(a, "sess-a", "frob", "x"); rep.Tag.Error != peers.TagErrBadTag {
		t.Errorf("bad op: %+v", rep.Tag)
	}
	if rep := tag(a, "sess-zz", peers.TagAdd, "x"); rep.Tag.Error != peers.TagErrUnknown {
		t.Errorf("unknown session: %+v", rep.Tag)
	}
	for i := 2; i < peers.MaxTagsPerSession; i++ {
		tag(a, "sess-a", peers.TagAdd, fmt.Sprintf("t%d", i))
	}
	if rep := tag(a, "sess-a", peers.TagAdd, "over"); rep.Tag.Error != peers.TagErrTooMany || len(rep.Tag.Tags) != peers.MaxTagsPerSession {
		t.Errorf("cap: %+v", rep.Tag)
	}
	if rep := tag(a, "sess-a", peers.TagRm, "ci"); !rep.Tag.Changed || strings.Contains(strings.Join(rep.Tag.Tags, ","), "ci") {
		t.Errorf("rm: %+v", rep.Tag)
	}

	// Viewers: GET /v1/peers carries tags ([] when none).
	st, m := h.call("GET", "/v1/peers", h.owner, nil)
	if st != 200 {
		t.Fatalf("GET /v1/peers: %d", st)
	}
	seen := map[string]any{}
	for _, s := range m["sessions"].([]any) {
		e := s.(map[string]any)
		seen[e["session"].(string)] = e["tags"]
	}
	if tags, _ := seen["sess-a"].([]any); len(tags) != peers.MaxTagsPerSession-1 || tags[0] != "build" {
		t.Errorf("sess-a tags: %v", seen["sess-a"])
	}
	if tags, ok := seen["sess-b"].([]any); !ok || len(tags) != 0 {
		t.Errorf("sess-b tags: %#v", seen["sess-b"])
	}

	// The session ends: its tags go.
	tag(b, "sess-b", peers.TagAdd, "build")
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		f, _, err := tx.GetFold("sess-a")
		if err != nil {
			return err
		}
		f.State, f.EndedAt, f.EndReason, f.Since, f.UpdatedAt = "ended", tx.NowMS(), "exit", tx.NowMS(), tx.NowMS()
		return tx.PutFold("sess-a", f)
	}); err != nil {
		t.Fatal(err)
	}
	all, err := h.st.PeerTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["sess-a"]; ok || strings.Join(all["sess-b"], ",") != "build" {
		t.Errorf("after the end: %v", all)
	}
	if rep := tag(a, "sess-a", peers.TagAdd, "build"); rep.Tag.Error != peers.TagErrUnknown {
		t.Errorf("tag an ended session: %+v", rep.Tag)
	}
}
