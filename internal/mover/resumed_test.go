//go:build !windows

package mover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAwaitResumed(t *testing.T) {
	defer func(d time.Duration) { ResumeSeen = d }(ResumeSeen)
	ResumeSeen = 0
	l := Launch{SessionID: "s1", Name: "lane", CWD: "/w"}
	for _, c := range []struct{ name, seen, want string }{
		{"same id", "s1", "s1"},
		{"forked under a new id", "s2", "s2"},
		{"not seen", "", ""},
	} {
		h := &fakeHost{resumed: c.seen}
		if got := AwaitResumed(h, l, Target{Dir: "/h/.claude-4"}, time.Now()); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestStartWaitOutcome(t *testing.T) {
	dir := t.TempDir()
	h := RealHost{}
	for _, c := range []struct {
		name, script, errHas string
		wait                 time.Duration
	}{
		{"launch ok", "echo job started", "", 5 * time.Second},
		{"launch fails", "echo no such session >&2; exit 3", "exit status 3: no such session", 5 * time.Second},
		{"still running", "sleep 2", "", 50 * time.Millisecond},
	} {
		log := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-")+".log")
		err := h.StartWait(dir, []string{"sh", "-c", c.script}, log, c.wait)
		if c.errHas == "" && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if c.errHas != "" && (err == nil || !strings.Contains(err.Error(), c.errHas)) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.errHas)
		}
		if _, e := os.Stat(log); e != nil {
			t.Errorf("%s: no log: %v", c.name, e)
		}
	}
}

func TestDigestShowsFailedAndForkedMoves(t *testing.T) {
	st := State{Dir: t.TempDir()}
	now := time.Now()
	for _, e := range []LogEntry{
		{At: now.Add(-3 * time.Minute), SessionID: "5c0f0b81-aaaa", From: "a", To: "b", Auto: true, ResumedAs: "3c4dbb08-bbbb"},
		{At: now.Add(-2 * time.Minute), SessionID: "e73b4647-aaaa", From: "a", To: "b", Auto: true, Warn: "resumed session not seen on claude-4"},
		{At: now.Add(-1 * time.Minute), SessionID: "871831b5-aaaa", From: "a", To: "b", Auto: true, Error: "start: exit status 1: boom"},
	} {
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	d := st.BuildDigest(now.Add(-time.Hour), now, func(dir, key string) string { return key }, nil)
	if len(d.Moves) != 2 || len(d.Failed) != 1 {
		t.Fatalf("moves %d failed %d", len(d.Moves), len(d.Failed))
	}
	txt := d.Text()
	for _, want := range []string{"went on as session 3c4dbb08", "resumed session not seen on claude-4", "Moves that failed: 1", "FAILED: start: exit status 1: boom"} {
		if !strings.Contains(txt, want) {
			t.Errorf("digest lacks %q:\n%s", want, txt)
		}
	}
}
