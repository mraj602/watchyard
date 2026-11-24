package mover

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func tname(dir, key string) string {
	if dir != "" {
		return dir
	}
	return key
}

func TestNoticeText(t *testing.T) {
	a := Notice{Session: "5e55a0a0-0000", Name: "lane-a", From: "x", To: "y", Reason: "5h projects to 97%"}
	b := Notice{Session: "5e55a0b0-0000", From: "x", To: "z"}
	for _, c := range []struct {
		name string
		ns   []Notice
		want string
	}{
		{"none", nil, ""},
		{"one with name and reason", []Notice{a}, "lane-a x -> y: 5h projects to 97%"},
		{"one without name or reason", []Notice{b}, "5e55a0b0 x -> z"},
		{"batch", []Notice{a, b, a}, "3 moves: lane-a x->y; 5e55a0b0 x->z; lane-a x->y"},
		{"long reason is cut", []Notice{{Name: "n", From: "x", To: "y", Reason: strings.Repeat("r", 300)}}, "n x -> y: " + strings.Repeat("r", maxReason-1) + "…"},
	} {
		if got := NoticeText(c.ns); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNotifyRateLimitAndBatch(t *testing.T) {
	st := State{Dir: t.TempDir()}
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	var sent []string
	send := func(text string, n int) error { sent = append(sent, text); return nil }
	add := func(at time.Time, sid string, auto bool) {
		if err := st.Append(LogEntry{At: at, SessionID: sid, Name: sid, FromDir: "A", ToDir: "B", Auto: auto, Reason: "why"}); err != nil {
			t.Fatal(err)
		}
	}
	step := func(at time.Time, enabled bool, send func(string, int) error) int {
		n, err := st.Notify(at, enabled, tname, send)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The first run only sets the cursor: old moves are not pushed.
	add(t0.Add(-time.Hour), "old", true)
	if n := step(t0, true, send); n != 0 || len(sent) != 0 {
		t.Fatalf("first run sent %d %v", n, sent)
	}
	// One auto move goes out at once; a move by hand does not.
	add(t0.Add(time.Minute), "s1", true)
	add(t0.Add(time.Minute), "hand", false)
	if n := step(t0.Add(2*time.Minute), true, send); n != 1 || len(sent) != 1 || sent[0] != "s1 A -> B: why" {
		t.Fatalf("first push %d %v", n, sent)
	}
	// Three moves inside the 5 minutes wait, then go as one push.
	add(t0.Add(3*time.Minute), "s2", true)
	add(t0.Add(4*time.Minute), "s3", true)
	add(t0.Add(5*time.Minute), "s4", true)
	if n := step(t0.Add(6*time.Minute), true, send); n != 0 || len(sent) != 1 {
		t.Fatalf("pushed inside the window: %d %v", n, sent)
	}
	if got := len(st.LoadNotify().Pending); got != 3 {
		t.Fatalf("pending %d", got)
	}
	if n := step(t0.Add(7*time.Minute+time.Second), true, send); n != 3 || len(sent) != 2 || sent[1] != "3 moves: s2 A->B; s3 A->B; s4 A->B" {
		t.Fatalf("batch %d %v", n, sent)
	}
	if got := len(st.LoadNotify().Pending); got != 0 {
		t.Fatalf("pending after push: %d", got)
	}
	// A failed push keeps the move and is retried only after the window.
	add(t0.Add(20*time.Minute), "s5", true)
	fail := func(string, int) error { return errors.New("down") }
	if _, err := st.Notify(t0.Add(21*time.Minute), true, tname, fail); err == nil {
		t.Fatal("no error from a failed push")
	}
	if got := len(st.LoadNotify().Pending); got != 1 {
		t.Fatalf("pending after failure: %d", got)
	}
	if n := step(t0.Add(23*time.Minute), true, send); n != 0 {
		t.Fatalf("retried inside the window: %d", n)
	}
	if n := step(t0.Add(27*time.Minute), true, send); n != 1 || sent[2] != "s5 A -> B: why" {
		t.Fatalf("retry %d %v", n, sent)
	}
}

func TestNotifyOffAndNoPath(t *testing.T) {
	st := State{Dir: t.TempDir()}
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	calls := 0
	send := func(string, int) error { calls++; return nil }
	_, _ = st.Notify(t0, true, tname, send)
	_ = st.Append(LogEntry{At: t0.Add(time.Minute), SessionID: "a", FromDir: "A", ToDir: "B", Auto: true})
	// Off: nothing pushed, and turning it on later does not push the move made while off.
	if n, _ := st.Notify(t0.Add(2*time.Minute), false, tname, send); n != 0 || calls != 0 {
		t.Fatalf("pushed while off: %d %d", n, calls)
	}
	if n, _ := st.Notify(t0.Add(10*time.Minute), true, tname, send); n != 0 || calls != 0 {
		t.Fatalf("pushed an old move: %d %d", n, calls)
	}
	// No push path (send nil): the move waits, within NotifyKeep.
	_ = st.Append(LogEntry{At: t0.Add(11 * time.Minute), SessionID: "b", FromDir: "A", ToDir: "B", Auto: true})
	_, _ = st.Notify(t0.Add(12*time.Minute), true, tname, nil)
	if got := len(st.LoadNotify().Pending); got != 1 {
		t.Fatalf("pending without a path: %d", got)
	}
	// Too old by the time a path exists: dropped.
	if n, _ := st.Notify(t0.Add(12*time.Minute+NotifyKeep+time.Minute), true, tname, send); n != 0 || calls != 0 {
		t.Fatalf("pushed a stale move: %d %d", n, calls)
	}
}
