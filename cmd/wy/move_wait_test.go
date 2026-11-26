package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/mover"
)

// Live (2026-11-25 03:54-04:00): a pass with three mid-turn moves waited for each step in
// turn and held the pass lock, so no pass ran for 6 minutes. The waits now run side by side,
// a pass waits at most account.move_pass_wait_s in all, a session still inside its step waits
// on in the next pass (until move_step_wait_min in all), and the pass lock is released once
// the pass has planned: the next pass runs and leaves the waiting sessions alone.
func TestMoveWaitsSideBySide(t *testing.T) {
	const a, b = "5e55a0a0-0000-4000-8000-000000000001", "5e55a0b0-0000-4000-8000-000000000002"
	defer func(d time.Duration) { mover.StepPoll = d }(mover.StepPoll)
	mover.StepPoll = 10 * time.Millisecond
	f, h, run := moveFixture(t, 70, "busy")
	pinSession(t, f, "busy")
	f.hookEvents(a, "UserPromptSubmit")
	f.hookEvents(b, "UserPromptSubmit")
	f.fiveReset(3 * time.Hour)
	f.set("account.smart_switch_scope", "sessions:5e55a0a0,5e55a0b0")
	moveTestVars[moveTestEnv] = `{"rates":{"5e55a0a0":8,"5e55a0b0":8},"spike_passes":1}` // 70 + 3 h × 16 = 118
	t.Cleanup(func() { delete(moveTestVars, moveTestEnv) })
	f.set("account.smart_switch", "on")
	f.set("account.move_step_wait_min", "0.03") // 1.8 s in all
	f.set("account.move_pass_wait_s", "0.6")
	st := mover.NewState(f.dirs.State.Path)
	_ = st.RecordStep(a, "toolu_a", "pre", "Bash", "make test", time.Now())
	_ = st.RecordStep(b, "toolu_b", "pre", "Bash", "make lint", time.Now())

	// Pass 1: both wait, side by side (about 0.6 s, not 1.2), then wait on.
	var out1 string
	var wg sync.WaitGroup
	wg.Add(1)
	start := time.Now()
	go func() { defer wg.Done(); out1, _, _ = run("--auto") }()
	// Pass 2 starts while pass 1 waits: it is not blocked and leaves both sessions alone.
	time.Sleep(250 * time.Millisecond)
	out2, _, _ := run("--auto")
	wg.Wait()
	took := time.Since(start)
	if took > 1100*time.Millisecond || len(h.calls) != 0 {
		t.Fatalf("pass 1 took %s, calls %q\n%s", took, h.calls, out1)
	}
	if strings.Count(out1, "the next pass waits on") != 2 {
		t.Fatalf("pass 1:\n%s", out1)
	}
	if strings.Contains(out2, "Another Smart Account Switch pass") || strings.Contains(out2, "moved") {
		t.Fatalf("pass 2 blocked or moved:\n%s", out2)
	}

	// Pass 3, after the 1.8 s in all: both moved inside their steps, named.
	time.Sleep(1300 * time.Millisecond)
	out3, _, _ := run("--auto")
	if strings.Count(out3, "moved anyway") != 2 || strings.Count(out3, "moved 5e55") != 2 {
		t.Fatalf("pass 3:\n%s\ncalls %q", out3, h.calls)
	}
}
