package main

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/picker"
)

func rateCands(samples ...picker.Sample) []picker.Candidate {
	return []picker.Candidate{{Dir: "/h/.claude", Windows: []picker.Window{{Key: "five_hour", Samples: samples}}}}
}

// Live run 1.3.3-pre3: whole-percent readings a few minutes apart read as 10-60 points/h.
// The slope over 15-30 minutes of readings 5 minutes apart says what they really rose.
func TestReadingSlope(t *testing.T) {
	now := time.Date(2026, 11, 24, 22, 24, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	at := func(min float64, pct float64) picker.Sample {
		return picker.Sample{At: now.Add(-time.Duration(min * float64(time.Minute))), Pct: pct, ResetsAt: reset}
	}
	keys := map[string]string{"/h/.claude": "acct:a/o"}
	slope := func(ss ...picker.Sample) (float64, bool) {
		return readingSlope(rateCands(ss...), keys, "acct:a/o", now, 10*time.Minute)
	}
	// The old rate: 53 -> 54 in 2 minutes = 30 points/h. Over 30 minutes of 53s and one 54:
	// a few points/h.
	if r, ok := slope(at(0, 54), at(2, 53), at(7, 53), at(12, 53), at(18, 53), at(24, 53), at(29, 53)); !ok || r > 5 {
		t.Errorf("one-point step: %.1f %v", r, ok)
	}
	// A real burn: 50 -> 60 over 30 minutes = 20 points/h.
	if r, ok := slope(at(0, 60), at(5, 58), at(10, 57), at(15, 55), at(20, 53), at(25, 52), at(30, 50)); !ok || math.Abs(r-20) > 2 {
		t.Errorf("steady burn: %.1f %v", r, ok)
	}
	// Under 15 minutes of readings: no rate.
	if _, ok := slope(at(0, 54), at(5, 53), at(10, 53)); ok {
		t.Error("10 minutes of readings gave a rate")
	}
	// Across a reset: only the newest window's readings count (here too few).
	old := picker.Sample{At: now.Add(-25 * time.Minute), Pct: 90, ResetsAt: now.Add(-10 * time.Minute)}
	if _, ok := slope(at(0, 3), at(5, 2), old); ok {
		t.Error("a rate across a reset")
	}
}

// Other machines' sessions the hub measured count in the account's burn.
func TestRemoteRate(t *testing.T) {
	now := time.Date(2026, 11, 24, 22, 24, 0, 0, time.UTC)
	burn := [][2]float64{{0, 100}, {1500, 100}, {1560, 100}, {1620, 100}} // pick at now-30m: the last three in the window
	ended := [][2]float64{{1700, 1000}}
	c := picker.Candidate{Dir: "/h/.claude", RemoteLeases: []picker.HubLease{
		{ID: "l1", At: now.Add(-30 * time.Minute), Burn: &burn},
		{ID: "l1", At: now.Add(-30 * time.Minute), Burn: &burn}, // the same lease twice
		{ID: "l2", At: now.Add(-30 * time.Minute), Burn: &ended, Ended: true},
		{ID: "l3", At: now},
	}}
	keys := map[string]string{"/h/.claude": "acct:a/o"}
	// 300 units in 10 minutes × f 0.01 = 3 points per 10 min = 18 points/h.
	if r := remoteRate([]picker.Candidate{c}, keys, "acct:a/o", now, 10*time.Minute, 0.01); math.Abs(r-18) > 1e-9 {
		t.Errorf("remote rate %.2f", r)
	}
	c.RemoteCopy = true
	if r := remoteRate([]picker.Candidate{c}, keys, "acct:a/o", now, 10*time.Minute, 0.01); r != 0 {
		t.Errorf("kept copy counted: %.2f", r)
	}
}

// The readings over twice the meter are used from the second pass in a row, and a trigger
// only they make needs 3 passes before anything moves.
func TestMoveReadingsNeedPasses(t *testing.T) {
	f, h, run := moveFixture(t, 60, "busy")
	// The meter: 1 point/h (60 + 3 h = 63, under the cap); the readings: 40 points/h.
	moveTestVars[moveTestEnv] = `{"accounts":{"claude":{"rate":1,"readings":40,"five_reset_min":180}},"rates":{"5e55a0a0":1}}`
	t.Cleanup(func() { delete(moveTestVars, moveTestEnv) })
	f.set("account.smart_switch_scope", "sessions:5e55a0a0")
	out, _, _ := run("--plan")
	if !strings.Contains(out, "used if it holds on the next pass") || !strings.Contains(out, "No session needs a move.") {
		t.Fatalf("first look:\n%s", out)
	}
	f.set("account.smart_switch", "on")
	// Pass 1: the readings are not used yet; passes 2 and 3: used, the trigger is held.
	for pass := 1; pass <= 3; pass++ {
		out, errOut, _ := run("--auto")
		if strings.Contains(out, "moved ") || len(h.calls) != 0 {
			t.Fatalf("pass %d moved: %q %q %q", pass, out, errOut, h.calls)
		}
	}
	out, errOut, _ := run("--auto")
	if !strings.Contains(out, "moved 5e55a0a0") {
		t.Fatalf("pass 4: %q %q", out, errOut)
	}
}
