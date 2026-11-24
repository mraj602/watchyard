package mover

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Steps (setup.md 8.6, Between steps): a tool call is in flight from its pre to its post;
// out of order events (async hooks) still end it; a move ends the old process's steps.
func TestSteps(t *testing.T) {
	st := State{Dir: t.TempDir()}
	now := time.Now()
	if s := st.StepNow("s1", now); s.Known || s.In {
		t.Fatalf("no record: %+v", s)
	}
	_ = st.RecordStep("s1", "toolu_a", "pre", "Bash", "sleep 60", now.Add(-time.Minute))
	if s := st.StepNow("s1", now); !s.Known || !s.In || s.Tool != "Bash" || s.Cmd != "sleep 60" {
		t.Fatalf("in a step: %+v", s)
	}
	_ = st.RecordStep("s1", "toolu_a", "post", "Bash", "", now)
	if s := st.StepNow("s1", now); !s.Known || s.In {
		t.Fatalf("between steps: %+v", s)
	}
	// Post before pre (two async hooks racing): ended.
	_ = st.RecordStep("s1", "toolu_b", "post", "Read", "", now)
	_ = st.RecordStep("s1", "toolu_b", "pre", "Read", "/x", now)
	if s := st.StepNow("s1", now); s.In {
		t.Fatalf("post first: %+v", s)
	}
	// A step with no end for longer than StepStale is not counted.
	_ = st.RecordStep("s1", "toolu_c", "pre", "Bash", "x", now.Add(-3*time.Hour))
	if s := st.StepNow("s1", now); s.In {
		t.Fatalf("stale: %+v", s)
	}
	// EndSteps: the move killed it.
	_ = st.RecordStep("s1", "toolu_d", "pre", "Bash", "y", now)
	st.EndSteps("s1", now)
	if s := st.StepNow("s1", now); s.In {
		t.Fatalf("ended by the move: %+v", s)
	}
	// Bad ids are ignored.
	if err := st.RecordStep("../x", "toolu_e", "pre", "Bash", "", now); err != nil || st.StepNow("../x", now).Known {
		t.Error("bad session id recorded")
	}
}

func TestWaitStep(t *testing.T) {
	defer func(d time.Duration) { StepPoll = d }(StepPoll)
	StepPoll = 10 * time.Millisecond
	st := State{Dir: t.TempDir()}
	alive := func() bool { return true }
	// Unknown (no hooks): not between, no wait.
	if _, between := st.WaitStep("s1", time.Second, alive); between {
		t.Error("unknown counted as between")
	}
	_ = st.RecordStep("s1", "toolu_a", "pre", "Bash", "sleep 60", time.Now())
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = st.RecordStep("s1", "toolu_a", "post", "Bash", "", time.Now())
	}()
	start := time.Now()
	if _, between := st.WaitStep("s1", 5*time.Second, alive); !between || time.Since(start) < 90*time.Millisecond {
		t.Errorf("waited %s, between %v", time.Since(start), between)
	}
	// The wait runs out inside a step.
	_ = st.RecordStep("s1", "toolu_b", "pre", "Bash", "deploy", time.Now())
	if s, between := st.WaitStep("s1", 50*time.Millisecond, alive); between || s.Cmd != "deploy" {
		t.Errorf("ran out: %+v %v", s, between)
	}
}

func TestStepCommand(t *testing.T) {
	for in, want := range map[string]string{
		`{"command":"sleep 60 && echo done >> f","description":"wait"}`:               "sleep 60 && echo done >> f",
		`{"file_path":"/w/a.go","content":"x"}`:                                       "/w/a.go",
		`{"command":"curl -H 'Authorization: token ghp_abcdefghijklmnopqrstuvwx' x"}`: "",
		`{"command":"` + strings.Repeat("a", 100) + `"}`:                              strings.Repeat("a", 79) + "…",
		`{"command":"a\n  b"}`: "a b",
		`not json`:             "",
	} {
		if got := StepCommand(json.RawMessage(in)); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

// The mid-turn note says whether the last step finished, or which step it was killed inside.
func TestNoteSteps(t *testing.T) {
	at := time.Date(2026, 11, 24, 14, 5, 0, 0, time.Local)
	mv := Move{MidTurn: true, StepEnded: true}
	if s := Note("a", "b", at, mv); !strings.Contains(s, "Your last step finished before the move.") || strings.Contains(s, "may not have finished") {
		t.Error(s)
	}
	mv = Move{MidTurn: true, StepTool: "Bash", StepCmd: "git push"}
	if s := Note("a", "b", at, mv); !strings.Contains(s, "It was stopped inside a step (Bash: git push) that may still be running: check its result before redoing it.") {
		t.Error(s)
	}
	if s := Note("a", "b", at, Move{MidTurn: true}); !strings.Contains(s, "Your last step may not have finished") {
		t.Error(s)
	}
}

func TestPassLock(t *testing.T) {
	st := State{Dir: t.TempDir()}
	unlock, ok, err := st.PassLock()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, ok2, _ := st.PassLock(); ok2 {
		t.Error("second pass got the lock")
	}
	unlock()
	if u, ok3, _ := st.PassLock(); !ok3 {
		t.Error("lock not released")
	} else {
		u()
	}
}
