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

// Stale data for over 30 minutes: one alert, not one per pass; a new one when the set of
// stale accounts changes.
func TestStaleAlertOnce(t *testing.T) {
	f := newNextFixture(t)
	now := time.Date(2026, 11, 25, 0, 40, 0, 0, time.UTC)
	var out bytes.Buffer
	e := moveEnv{nextEnv: nextEnv{dirs: f.dirs, now: func() time.Time { return now }, stdout: &out}}
	st := mover.NewState(f.dirs.State.Path)
	w := &moveWorld{input: mover.Input{Params: mover.DefaultParams, Accounts: []mover.Account{{Key: "k4", Label: "claude-4"}, {Key: "k5", Label: "claude-5"}}}}
	stale := func(keys ...string) mover.Result {
		var r mover.Result
		for _, k := range keys {
			r.Accounts = append(r.Accounts, mover.Projection{Key: k, StaleFor: 40 * time.Minute})
		}
		r.Accounts = append(r.Accounts, mover.Projection{Key: "fresh"}, mover.Projection{Key: "k9", StaleFor: 20 * time.Minute})
		return r
	}
	for i := 0; i < 3; i++ {
		w.staleAlert(e, config.Defaults(), st, stale("k4"))
	}
	if n := strings.Count(out.String(), "alert: "); n != 1 || !strings.Contains(out.String(), "claude-4 (data 40 min old)") || strings.Contains(out.String(), "k9") {
		t.Fatalf("alerts:\n%s", out.String())
	}
	w.staleAlert(e, config.Defaults(), st, stale("k4", "k5"))
	w.staleAlert(e, config.Defaults(), st, stale())
	w.staleAlert(e, config.Defaults(), st, stale())
	if n := strings.Count(out.String(), "alert: "); n != 2 {
		t.Fatalf("alerts:\n%s", out.String())
	}
}
