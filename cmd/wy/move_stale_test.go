package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/mover"
)

// A pass whose state took over 2 minutes to read moves nothing (live 05:40:42: a pass that
// read its config before claude-5 was set protected moved a session minutes later).
func TestWorldTooOld(t *testing.T) {
	now := time.Now()
	if _, old := worldTooOld(now.Add(-90*time.Second), now); old {
		t.Error("90 s is not too old")
	}
	if age, old := worldTooOld(now.Add(-5*time.Minute), now); !old || age != 5*time.Minute {
		t.Errorf("5 min: %v %v", age, old)
	}
	if _, old := worldTooOld(time.Time{}, now); old {
		t.Error("unknown start")
	}
}

// --plan says when an account's data is stale.
func TestPlanSaysStale(t *testing.T) {
	w := &moveWorld{input: mover.Input{Params: mover.DefaultParams}}
	r := mover.Result{Accounts: []mover.Projection{{Key: "k4", Label: "claude-4", Level5: 100, P5: 140, Cap5: 95, Cap7: 98, Window: mover.Win5h,
		StaleFor: 217 * time.Minute, Hit: time.Minute}}}
	var b bytes.Buffer
	w.printPlan(&b, r, config.Defaults())
	if out := b.String(); !strings.Contains(out, "data stale (3 h 37 min), no moves") || strings.Contains(out, "moving its burners") || strings.Contains(out, "100% in") {
		t.Fatalf("plan:\n%s", out)
	}
	if got := staleAge(42 * time.Minute); got != "42 min" {
		t.Errorf("staleAge %q", got)
	}
}

// Stale data is told only when it held back a move (Result.StaleBlocks), over 30 minutes old,
// at most once per account per 6 hours, and names the hub only when this pass could not read
// it (live 2026-11-25: "(is the hub reachable?)" every hour for idle accounts while the hub was up).
func TestStaleAlertOnlyWhenBlocked(t *testing.T) {
	f := newNextFixture(t)
	now := time.Date(2026, 11, 25, 0, 40, 0, 0, time.UTC)
	var out bytes.Buffer
	e := moveEnv{nextEnv: nextEnv{dirs: f.dirs, now: func() time.Time { return now }, stdout: &out}}
	st := mover.NewState(f.dirs.State.Path)
	w := &moveWorld{input: mover.Input{Params: mover.DefaultParams, Accounts: []mover.Account{{Key: "k4", Label: "claude-4"}, {Key: "k5", Label: "claude-5"}}}}
	staleOnly := mover.Result{Accounts: []mover.Projection{{Key: "k4", StaleFor: 7 * time.Hour}, {Key: "k5", StaleFor: 108 * time.Hour}}}
	blocked := func(bs ...mover.StaleBlock) mover.Result { r := staleOnly; r.StaleBlocks = bs; return r }
	alerts := func() int { return strings.Count(out.String(), "alert: ") }

	for _, c := range []struct {
		name  string
		at    time.Duration
		r     mover.Result
		hub   string
		want  int // alerts in all, after this step
		has   []string
		hasnt []string
	}{
		{name: "stale but nothing held back", r: staleOnly, want: 0},
		{name: "under 30 minutes old", r: blocked(mover.StaleBlock{Key: "k4", StaleFor: 20 * time.Minute, How: mover.StaleDest}), want: 0},
		{name: "an idle account left out as the only destination", r: blocked(mover.StaleBlock{Key: "k5", StaleFor: 108*time.Hour + 16*time.Minute, How: mover.StaleDest}), want: 1,
			has:   []string{"claude-5 (data 108 h 16 min old: left out as the destination of a session that found no other)", "no new usage reading came in"},
			hasnt: []string{"hub"}},
		{name: "the same account again within 6 h", at: 3 * time.Hour, r: blocked(mover.StaleBlock{Key: "k5", StaleFor: 111 * time.Hour, How: mover.StaleDest}), want: 1},
		{name: "another account, the hub not read", at: 3 * time.Hour, hub: "the hub did not answer in time",
			r: blocked(mover.StaleBlock{Key: "k4", StaleFor: 7*time.Hour + 38*time.Minute, How: mover.StaleSource}, mover.StaleBlock{Key: "k5", StaleFor: 111 * time.Hour, How: mover.StaleDest}), want: 2,
			has: []string{"claude-4 (data 7 h 38 min old: over its cap, nothing moved off it)", "the hub could not be read this pass (the hub did not answer in time)"}},
		{name: "6 h later it may be told again", at: 6*time.Hour + time.Minute, r: blocked(mover.StaleBlock{Key: "k5", StaleFor: 114 * time.Hour, How: mover.StalePull}), want: 3,
			has: []string{"its drain pulled nothing in"}},
	} {
		now = time.Date(2026, 11, 25, 0, 40, 0, 0, time.UTC).Add(c.at)
		w.hubErr = c.hub
		before := out.Len()
		w.staleAlert(e, config.Defaults(), st, c.r)
		got := out.String()[before:]
		if alerts() != c.want {
			t.Fatalf("%s: %d alerts, want %d:\n%s", c.name, alerts(), c.want, out.String())
		}
		for _, x := range c.has {
			if !strings.Contains(got, x) {
				t.Errorf("%s: no %q in %q", c.name, x, got)
			}
		}
		for _, x := range c.hasnt {
			if strings.Contains(got, x) {
				t.Errorf("%s: %q in %q", c.name, x, got)
			}
		}
	}
}
