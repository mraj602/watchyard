//go:build fakeinbox && peerstest

package peers_test

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// fakeRemote is a hub with one session on another machine that queues every message. names
// is what Names returns (the hub's naming of the last answer); the remote session takes
// account, aliases from it when set.
type fakeRemote struct {
	sent    int
	last    peers.Outgoing
	account string
	aliases []string
	names   map[string]peers.HubName
	// tags are the hub's tags per session (Tag changes them); state is sess-r's state.
	tags  map[string][]string
	state string
	// sentTo counts messages per target session.
	sentTo map[string]int
}

func (r *fakeRemote) Sessions() ([]peers.Session, error) {
	acct := r.account
	if acct == "" {
		acct = "acc-remote"
	}
	return []peers.Session{{Entry: peers.RegistryEntry{SessionID: "sess-r"}, Account: acct, AccountLabel: "remote",
		Handle: "desk@remote.desk", Aliases: r.aliases, Name: "desk", MachineName: "desk", Remote: true, Device: "dev_r",
		Mode: peers.ModePrompting, Tags: r.tags["sess-r"], State: r.state}}, nil
}

func (r *fakeRemote) Send(o peers.Outgoing) (*peers.Pending, *peers.Refusal) {
	r.sent++
	r.last = o
	if r.sentTo == nil {
		r.sentTo = map[string]int{}
	}
	r.sentTo[o.To.Entry.SessionID]++
	return &peers.Pending{Expires: time.Now().Add(time.Hour), Wait: func(time.Duration) (peers.Final, bool) { return peers.Final{}, false }}, nil
}

func (r *fakeRemote) Log(peers.LocalLog) {}

func (r *fakeRemote) Names() map[string]peers.HubName {
	if r.tags == nil {
		return r.names
	}
	out := map[string]peers.HubName{}
	for k, v := range r.names {
		out[k] = v
	}
	for id, t := range r.tags {
		n := out[id]
		n.Tags = t
		if id != "sess-r" {
			n.Mine = true
		}
		out[id] = n
	}
	return out
}

// Tag is the hub's peer_tag: at most MaxTagsPerSession, add or remove.
func (r *fakeRemote) Tag(session, op, tag string) (peers.TagAnswer, error) {
	if r.tags == nil {
		r.tags = map[string][]string{}
	}
	ans := peers.TagAnswer{Session: session}
	have := peers.HasTag(r.tags[session], tag)
	switch {
	case op == peers.TagAdd && !have && len(r.tags[session]) >= peers.MaxTagsPerSession:
		ans.Error = peers.TagErrTooMany
	case op == peers.TagAdd && !have:
		r.tags[session] = peers.NormTags(append(r.tags[session], tag))
		ans.Changed = true
	case op == peers.TagRm && have:
		var keep []string
		for _, t := range r.tags[session] {
			if t != tag {
				keep = append(keep, t)
			}
		}
		r.tags[session] = keep
		ans.Changed = true
	}
	ans.Tags = r.tags[session]
	return ans, nil
}

// The loop guard of a thread to another machine: a human prompt resets the count once, and
// the count then runs on from there (it must not be reset again by every later send).
func TestRemoteThreadLoopGuardAfterHumanPrompt(t *testing.T) {
	w := newWorld(t, peers.ModeBypass, peers.ModeBypass)
	w.allow([][]string{{"acc-work", "acc-remote"}}, map[string]any{"max_turns": 2})
	h0 := w.now.Add(-time.Hour)
	w.sessionFile("sess-a", "bypassPermissions", &h0)
	e, c := w.asA()
	e.Remote = &fakeRemote{}
	send := func() *peers.Refusal { _, rf := e.Send(c, "desk", "hi"); return rf }
	for i := 0; i < 2; i++ {
		if rf := send(); rf != nil {
			t.Fatalf("send %d: %v", i+1, rf)
		}
	}
	if rf := send(); rf == nil || rf.Code != peers.CodeLoopGuard {
		t.Fatalf("third send: %v", rf)
	}
	// The human types in session A: two more messages, then the guard again.
	h1 := w.now.Add(-time.Minute)
	w.sessionFile("sess-a", "bypassPermissions", &h1)
	for i := 0; i < 2; i++ {
		if rf := send(); rf != nil {
			t.Fatalf("send %d after the prompt: %v", i+1, rf)
		}
	}
	if rf := send(); rf == nil || rf.Code != peers.CodeLoopGuard {
		t.Fatalf("third send after the prompt: %v", rf)
	}
	th := peers.LoadThreads(e.ThreadsPath()).FindOpen("sess-a", "sess-r")
	if th == nil || !th.HumanAt.Equal(h1) || th.Turns != 2 || th.LastFrom != "sess-a" {
		t.Errorf("thread: %+v", th)
	}
}
