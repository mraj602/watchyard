package agent

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// A trusted send never reports a hold as a success (peers.md 4.1a, 3.4, 7.2): a broadcast with
// any held line exits 9 whatever the others did, and 0 only when every line was admitted.
func TestLinesExitHeld(t *testing.T) {
	ok := Reply{Status: "delivered", To: "a"}
	queued := Reply{Status: "queued", To: "q"}
	held := heldReply("m", "t", "b")
	gone := Reply{To: "g", Error: &Error{Code: peers.CodeTargetGone}}
	unknown := Reply{To: "u", Error: &Error{Code: peers.CodeHeld, Detail: "u's permission mode is not known"}}
	for _, c := range []struct {
		name  string
		lines []Reply
		one   bool
		want  int
	}{
		{"all admitted", []Reply{ok, ok}, false, 0},
		{"queued", []Reply{ok, queued}, false, peers.ExitQueued},
		{"mixed holders", []Reply{ok, held}, false, peers.ExitHeld},
		{"held after another refusal", []Reply{gone, ok, held}, false, peers.ExitHeld},
		{"mode unknown", []Reply{unknown}, false, peers.ExitHeld},
		{"mode unknown, queued too", []Reply{queued, unknown}, false, peers.ExitHeld},
		{"one, held", []Reply{held}, true, peers.ExitHeld},
		{"one, gone then delivered", []Reply{gone, ok}, true, 0},
	} {
		if got := linesExit(c.lines, c.one); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
	if peers.ExitCode(peers.CodeHeld) != peers.ExitHeld || peers.ExitHeld != 9 {
		t.Errorf("held maps to %d", peers.ExitCode(peers.CodeHeld))
	}
	if d := peers.Describe(&peers.Refusal{Code: peers.CodeHeld, Detail: held.Error.Detail}); !strings.HasPrefix(d, "held: written to b") {
		t.Errorf("describe: %q", d)
	}
}

// A remote trusted send's final status: the receiver's rejected/held (mode not known) is the
// held code, and a delivery an older receiver marked held_likely becomes a held line.
func TestWaitFinalHeld(t *testing.T) {
	s := &server{cfg: Config{Now: time.Now}, log: io.Discard}
	res := peers.SendResult{Msg: "m1", Thread: "t1", Target: peers.Session{Handle: "builder@personal.worker"}, Status: "queued",
		Pending: &peers.Pending{Wait: func(time.Duration) (peers.Final, bool) {
			return peers.Final{Status: peers.CodeRejected, Detail: peers.CodeHeld}, true
		}}}
	r := s.waitFinal(Request{}, res)
	if r.Error == nil || r.Error.Code != peers.CodeHeld || !strings.Contains(r.Error.Detail, "nothing was written") {
		t.Fatalf("rejected/held: %+v", r)
	}
	if peers.ExitCode(r.Error.Code) != peers.ExitHeld {
		t.Errorf("exit %d", peers.ExitCode(r.Error.Code))
	}
	res.Pending.Wait = func(time.Duration) (peers.Final, bool) {
		return peers.Final{Status: "delivered", Detail: "held_likely"}, true
	}
	r = s.waitFinal(Request{}, res)
	if r.Error != nil || !r.HeldLikely {
		t.Fatalf("held_likely: %+v", r)
	}
	if h := heldAsRefusal(r); h.Error == nil || h.Error.Code != peers.CodeHeld || h.Msg != "m1" || h.To != "builder@personal.worker" {
		t.Errorf("held_likely as a service line: %+v", h)
	}
	if ok := heldAsRefusal(Reply{Status: "delivered", To: "x"}); ok.Error != nil {
		t.Errorf("a clean delivery: %+v", ok)
	}
}
