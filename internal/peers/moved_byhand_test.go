//go:build fakeinbox && peerstest

package peers_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// identity.md 5.3: a session moved by hand, without wy move (no move record: claude --resume
// <id> under another config folder, here twice, .claude-4 -> .claude-3 -> .claude-5), keeps
// the handles it had in the folders it left, found from the session folders Claude Code keeps
// there (session-env/<id>); a send to an old handle is delivered with the new handle, and a
// remote message addressed under an account it left is accepted (it was refused with
// account_mismatch). A folder shared with the current one through a symlink is no evidence.
func TestMovedByHandKeepsOldHandles(t *testing.T) {
	w := newWorld(t, peers.ModeBypass, peers.ModeBypass)
	w.allow([][]string{{"acc-work", "acc-personal"}, {"acc-personal", "acc-work"}, {"acc-remote", "acc-personal"}}, nil)
	c3 := w.slot(".claude-3", "acc-old")
	c4 := w.slot(".claude-4", "acc-older")
	c6 := w.slot(".claude-6", "acc-shared")
	trace := func(slot string, age time.Duration) {
		d := filepath.Join(slot, "session-env", "sess-b")
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(d, at, at); err != nil {
			t.Fatal(err)
		}
	}
	trace(w.slotB, 0)
	trace(c3, time.Hour)
	trace(c4, 2*time.Hour)
	if err := os.Symlink(filepath.Join(w.slotB, "session-env"), filepath.Join(c6, "session-env")); err != nil {
		t.Fatal(err)
	}
	e, c := w.asA()
	es, _, rf := e.List(false)
	if rf != nil {
		t.Fatal(rf)
	}
	var b peers.ListEntry
	for _, x := range es {
		if x.Session == "sess-b" {
			b = x
		}
	}
	if b.Handle != "worker@personal.laptop" || !slices.Equal(b.MovedFrom, []string{"worker@claude-3.laptop", "worker@claude-4.laptop"}) ||
		!slices.Contains(b.Aliases, "worker@claude-4.laptop") || slices.Contains(b.Aliases, "worker@claude-6.laptop") {
		t.Fatalf("moved by hand: %+v", b)
	}
	res, rf := e.Send(c, "worker@claude-4.laptop", "still there?")
	if rf != nil {
		t.Fatal(rf)
	}
	if !strings.Contains(res.Line(), "worker@claude-4.laptop moved to another account and is now worker@personal.laptop") {
		t.Fatalf("line: %s", res.Line())
	}
	if !w.b.Wait(1, 2*time.Second) {
		t.Fatal("not delivered")
	}
	// From another machine whose directory still has the old account.
	open := func() (string, error) { return "hi", nil }
	from := peers.EnvFrom{Session: "sess-r", Device: "dev_r", Machine: "m_R", Account: "acc-remote", Label: "remote", Mode: peers.ModeBypass}
	re := w.env()
	for i, c := range []struct{ acct, want string }{{"acc-older", "delivered"}, {"acc-old", "delivered"}, {"acc-shared", peers.CodeRejected}, {"acc-x", peers.CodeRejected}} {
		env := peers.Envelope{V: 1, Msg: "m-" + c.acct, Thread: "t_" + c.acct, Kind: peers.KindStart, From: from,
			To: peers.EnvTo{Session: "sess-b", Device: "dev_b", Machine: "m_TEST", Account: c.acct}}
		res := re.Receive(peers.Incoming{Env: env, MachineName: "desk", Open: open})
		if res.Status != c.want || (c.want == peers.CodeRejected && res.Detail != "account_mismatch") {
			t.Errorf("%d to %s: %+v", i, c.acct, res)
		}
	}
}
