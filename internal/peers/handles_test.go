//go:build fakeinbox && peerstest

package peers_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// handleWorld is the two-account world with a hub that names session A "api@team.laptop"
// (the seat's label "team", while this machine labels the slot "work") and has a session on
// another machine in B's account, under the canonical label "remote" and the alias its own
// machine shows ("desk@claude-1.desk").
func handleWorld(t *testing.T) (*world, *fakeRemote) {
	w := newWorld(t, peers.ModeBypass, peers.ModeBypass)
	w.allow([][]string{{"acc-work", "acc-personal"}, {"acc-personal", "acc-work"}}, nil)
	r := &fakeRemote{account: "acc-personal", aliases: []string{"desk@claude-1.desk"}, names: map[string]peers.HubName{
		"sess-a": {Handle: "api@team.laptop", Name: "api", Label: "team", Account: "acc-work", Mine: true},
		"sess-r": {Handle: "desk@remote.desk", Name: "desk", Label: "remote", Account: "acc-personal",
			Aliases: []string{"desk@claude-1.desk"}},
	}}
	return w, r
}

func handles(es []peers.ListEntry) map[string][]string {
	out := map[string][]string{}
	for _, e := range es {
		out[e.Handle] = e.Aliases
	}
	return out
}

// The hub's canonical handles win: this machine's own session shows the hub's handle (with
// the local label as an alias), a session the hub has not named yet takes the hub's label
// for its account, and a remote session gains this machine's label for its account as an
// alias.
func TestCanonicalHandlesFromHub(t *testing.T) {
	w, r := handleWorld(t)
	e, _ := w.asB()
	e.Remote = r
	es, _, rf := e.List(false)
	if rf != nil {
		t.Fatal(rf)
	}
	got := handles(es)
	if a, ok := got["api@team.laptop"]; !ok || !slices.Equal(a, []string{"api@work.laptop"}) {
		t.Errorf("A as B sees it: %v", got)
	}
	if a, ok := got["desk@remote.desk"]; !ok || !slices.Equal(a, []string{"desk@claude-1.desk", "desk@personal.desk"}) {
		t.Errorf("the remote session: %v", got)
	}
	e, _ = w.asA()
	e.Remote = r
	es, _, rf = e.List(false)
	if rf != nil {
		t.Fatal(rf)
	}
	// B is not in the hub's answer: it takes the hub's label for acc-personal ("remote").
	if a, ok := handles(es)["worker@remote.laptop"]; !ok || !slices.Equal(a, []string{"worker@personal.laptop"}) {
		t.Errorf("B as A sees it: %v", handles(es))
	}
}

// A send resolves the canonical handle and every alias; the envelope and the reply line
// carry the sender's canonical handle.
func TestSendByAliasCarriesCanonicalHandle(t *testing.T) {
	w, r := handleWorld(t)
	e, c := w.asA()
	e.Remote = r
	for i, to := range []string{"desk@remote.desk", "desk@claude-1.desk", "desk@personal.desk", "DESK@PERSONAL"} {
		if _, rf := e.Send(c, to, "hi"); rf != nil {
			t.Fatalf("send to %s: %v", to, rf)
		}
		if r.sent != i+1 || r.last.To.Entry.SessionID != "sess-r" || r.last.From.Handle != "api@team.laptop" ||
			r.last.From.AccountLabel != "team" {
			t.Fatalf("send to %s: %d %+v", to, r.sent, r.last.From)
		}
	}
	// Same machine, by the local label's alias: the header names A canonically.
	e, c = w.asA()
	e.Remote = r
	res, rf := e.Send(c, "worker@personal.laptop", "hello")
	if rf != nil {
		t.Fatal(rf)
	}
	if res.Target.Handle != "worker@remote.laptop" {
		t.Errorf("target: %s", res.Target.Handle)
	}
	if !w.b.Wait(1, 2*time.Second) {
		t.Fatal("nothing arrived")
	}
	body := w.b.All()[0].Wrapper.Body
	if !strings.Contains(body, "Reply with: wy peers send api@team.laptop ") || w.b.All()[0].Wrapper.FromName != "api@team.laptop" {
		t.Errorf("header: %q", body)
	}
	// B answers with the line it was given.
	eb, cb := w.asB()
	eb.Remote = r
	if _, rf := eb.Send(cb, "api@team.laptop", "back"); rf != nil {
		t.Fatalf("reply: %v", rf)
	}
	if !w.a.Wait(1, 2*time.Second) {
		t.Fatal("the reply did not arrive")
	}
}

// Without a hub nothing changes: the local labels make the handles and there are no aliases.
func TestHandlesWithoutHub(t *testing.T) {
	w, _ := handleWorld(t)
	e, _ := w.asA()
	es, _, rf := e.List(false)
	if rf != nil {
		t.Fatal(rf)
	}
	if a, ok := handles(es)["worker@personal.laptop"]; !ok || len(a) != 0 {
		t.Errorf("%v", handles(es))
	}
}

// An alias two sessions share is ambiguous (both canonical handles named); a canonical
// handle wins over another session's alias; an alias prefix resolves when unique.
func TestResolveAliases(t *testing.T) {
	mk := func(id, handle string, aliases ...string) peers.Session {
		s := peers.Session{Handle: handle, Name: strings.SplitN(handle, "@", 2)[0], Aliases: aliases}
		s.Entry.SessionID = id
		return s
	}
	dir := []peers.Session{
		mk("s-1", "api@team.laptop", "api@claude.laptop"),
		mk("s-2", "api@ops.laptop", "api@claude.laptop"),
		mk("s-3", "docs@claude.laptop"),
		mk("s-4", "docs@work.laptop", "docs@claude.laptop"),
		mk("s-5", "web@team.desktop", "web@claude-1.desktop"),
	}
	_, cands, rf := peers.Resolve(dir, "api@claude.laptop")
	if rf == nil || rf.Code != peers.CodeAmbiguous || !slices.Equal(cands, []string{"api@team.laptop", "api@ops.laptop"}) {
		t.Errorf("shared alias: %v %v", cands, rf)
	}
	if !strings.Contains(rf.Detail, "api@team.laptop") || !strings.Contains(rf.Detail, "api@ops.laptop") {
		t.Errorf("detail: %s", rf.Detail)
	}
	if s, _, rf := peers.Resolve(dir, "docs@claude.laptop"); rf != nil || s.Entry.SessionID != "s-3" {
		t.Errorf("canonical over alias: %v %v", s.Entry.SessionID, rf)
	}
	for in, id := range map[string]string{"web@claude-1": "s-5", "web@claude-1.desktop": "s-5", "api@ops": "s-2"} {
		if s, _, rf := peers.Resolve(dir, in); rf != nil || s.Entry.SessionID != id {
			t.Errorf("Resolve(%q) = %v %v", in, s.Entry.SessionID, rf)
		}
	}
}

// A session resumed under another account (identity.md 5.3) while the hub still names it
// under the account it left: this machine names it for its account now, keeps the hub's
// handle as an alias, and a message to the old handle is delivered to it (no
// account_mismatch).
func TestMovedSessionKeepsOldHandle(t *testing.T) {
	w, r := handleWorld(t)
	r.names["sess-b"] = peers.HubName{Handle: "worker@team.laptop", Name: "worker", Label: "team", Account: "acc-work", Mine: true}
	e, c := w.asA()
	e.Remote = r
	es, _, rf := e.List(false)
	if rf != nil {
		t.Fatal(rf)
	}
	a, ok := handles(es)["worker@remote.laptop"]
	if !ok || !slices.Contains(a, "worker@team.laptop") {
		t.Fatalf("moved session: %v", handles(es))
	}
	res, rf := e.Send(c, "worker@team.laptop", "still there?")
	if rf != nil {
		t.Fatal(rf)
	}
	if res.Target.Handle != "worker@remote.laptop" || res.Target.Account != "acc-personal" {
		t.Fatalf("target %+v", res.Target)
	}
	if !w.b.Wait(1, 2*time.Second) {
		t.Fatal("nothing arrived")
	}
}
