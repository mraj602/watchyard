//go:build fakeinbox && peerstest

package peers_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// tagWorld: A (work) and B (personal) on this machine, and a session on another machine in
// the personal account, through a fake hub that holds tags. The matrix lets work message
// personal, not the reverse.
func tagWorld(t *testing.T) (*world, *fakeRemote) {
	w := newWorld(t, peers.ModeBypass, peers.ModeBypass)
	w.allow([][]string{{"acc-work", "acc-personal"}}, nil)
	return w, &fakeRemote{account: "acc-personal", tags: map[string][]string{}}
}

// tag add: the other holders the matrix allows are returned (the command prints them to the
// new holder) and each gets the notice through the normal send path, on this machine and on
// the other; adding it again tells no one again.
func TestTagAddNotifiesHolders(t *testing.T) {
	w, r := tagWorld(t)
	r.tags["sess-r"] = []string{"build"}
	r.tags["sess-b"] = []string{"build"}
	e, c := w.asA()
	e.Remote = r
	res, rf := e.Tag(c, peers.TagAdd, "build")
	if rf != nil {
		t.Fatal(rf)
	}
	if !res.Changed || strings.Join(res.Tags, ",") != "build" {
		t.Fatalf("result: %+v", res)
	}
	if got := handlesOf(res.Holders); got != "worker@personal.laptop desk@remote.desk" {
		t.Fatalf("holders: %s", got)
	}
	if len(res.Notices) != 2 {
		t.Fatalf("notices: %+v", res.Notices)
	}
	for _, n := range res.Notices {
		if n.Status != "delivered" && n.Status != "queued" {
			t.Errorf("notice %+v", n)
		}
	}
	if !w.b.Wait(1, 2*time.Second) {
		t.Fatal("B got no notice")
	}
	body := w.b.All()[0].Wrapper.Body
	if !strings.Contains(body, "api@work.laptop also tagged itself build") || !strings.Contains(body, "wy peers tag rm build") {
		t.Errorf("notice body: %q", body)
	}
	if r.sentTo["sess-r"] != 1 || !strings.Contains(r.last.Text, "also tagged itself build") {
		t.Errorf("remote notice: %v %q", r.sentTo, r.last.Text)
	}
	// Again: already held, holders listed, nobody told twice.
	res, rf = e.Tag(c, peers.TagAdd, "build")
	if rf != nil || res.Changed || len(res.Notices) != 0 || len(res.Holders) != 2 {
		t.Fatalf("second add: %+v %v", res, rf)
	}
	if r.sentTo["sess-r"] != 1 || len(w.b.All()) != 1 {
		t.Error("a second add sent notices")
	}
}

// A holder the matrix does not let the sender message is neither listed nor told.
func TestTagAddMatrixForbidsHolder(t *testing.T) {
	w, r := tagWorld(t)
	r.tags["sess-a"] = []string{"ops"}
	e, c := w.asB() // personal → work is not allowed
	e.Remote = r
	res, rf := e.Tag(c, peers.TagAdd, "ops")
	if rf != nil {
		t.Fatal(rf)
	}
	if len(res.Holders) != 0 || len(res.Notices) != 0 {
		t.Fatalf("forbidden holder leaked: %+v", res)
	}
	if w.a.Wait(1, 200*time.Millisecond) {
		t.Error("A was told")
	}
}

// The list carries tags and same_host; tag rm and tags; the cap; no hub; a bad tag.
func TestTagListRmLimits(t *testing.T) {
	w, r := tagWorld(t)
	r.tags["sess-r"] = []string{"build"}
	e, c := w.asA()
	e.Remote = r
	if _, rf := e.Tag(c, peers.TagAdd, "ci"); rf != nil {
		t.Fatal(rf)
	}
	es, _, rf := e.List(false)
	if rf != nil {
		t.Fatal(rf)
	}
	for _, le := range es {
		switch le.Session {
		case "sess-r":
			if le.SameHost || strings.Join(le.Tags, ",") != "build" {
				t.Errorf("remote entry: %+v", le)
			}
		case "sess-b":
			if !le.SameHost || len(le.Tags) != 0 {
				t.Errorf("local entry: %+v", le)
			}
		}
	}
	got, rf := e.Tag(c, peers.TagList, "")
	if rf != nil || strings.Join(got.Tags, ",") != "ci" {
		t.Fatalf("tags: %+v %v", got, rf)
	}
	if got, rf := e.Tag(c, peers.TagRm, "ci"); rf != nil || !got.Changed || len(got.Tags) != 0 {
		t.Fatalf("rm: %+v %v", got, rf)
	}
	if got, rf := e.Tag(c, peers.TagRm, "ci"); rf != nil || got.Changed {
		t.Fatalf("rm again: %+v %v", got, rf)
	}
	for i := 0; i < peers.MaxTagsPerSession; i++ {
		if _, rf := e.Tag(c, peers.TagAdd, fmt.Sprintf("t%d", i)); rf != nil {
			t.Fatal(rf)
		}
	}
	if _, rf := e.Tag(c, peers.TagAdd, "one-more"); rf == nil || rf.Code != peers.CodeLimitTags {
		t.Fatalf("over the cap: %v", rf)
	}
	if _, rf := e.Tag(c, peers.TagAdd, "Bad Tag"); rf == nil || rf.Code != peers.CodeUsage {
		t.Fatalf("bad tag: %v", rf)
	}
	e.Remote = nil
	if _, rf := e.Tag(c, peers.TagAdd, "x"); rf == nil || rf.Code != peers.CodeHubUnreachable {
		t.Fatalf("no hub: %v", rf)
	}
}

// A tag send's targets come from the sender's own list: the sender is never one of them, and
// --same-host keeps this machine's sessions only.
func TestTagTargetsFromList(t *testing.T) {
	w, r := tagWorld(t)
	r.tags["sess-a"] = []string{"build"}
	r.tags["sess-b"] = []string{"build"}
	r.tags["sess-r"] = []string{"build"}
	e, _ := w.asA()
	e.Remote = r
	es, _, rf := e.List(false)
	if rf != nil {
		t.Fatal(rf)
	}
	if got := handlesOf(peers.TagTargets(es, "build", false)); got != "worker@personal.laptop desk@remote.desk" {
		t.Errorf("targets: %s", got)
	}
	if got := handlesOf(peers.TagTargets(es, "build", true)); got != "worker@personal.laptop" {
		t.Errorf("same host: %s", got)
	}
	// B may message nobody (personal → work is not allowed; sess-r is its own account,
	// same_account on, so only sess-r).
	e, _ = w.asB()
	e.Remote = r
	es, _, _ = e.List(false)
	if got := handlesOf(peers.TagTargets(es, "build", false)); got != "desk@remote.desk" {
		t.Errorf("B's targets: %s", got)
	}
	if got := peers.TagTargets(es, "build", true); len(got) != 0 {
		t.Errorf("B same host: %v", got)
	}
}
