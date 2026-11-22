package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

func (f *nextFixture) outboxVerdicts() []record.AccountVerdictsPayload {
	f.t.Helper()
	files, _ := filepath.Glob(filepath.Join(f.dirs.State.Path, "outbox", "*.jsonl"))
	var out []record.AccountVerdictsPayload
	for _, fn := range files {
		b, _ := os.ReadFile(fn)
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var r record.Record
			if json.Unmarshal([]byte(line), &r) != nil || r.Kind != record.KindAccountVerdicts {
				continue
			}
			if r.Seat != nil || r.Slot != "" {
				f.t.Errorf("verdict record envelope: seat %v slot %q", r.Seat, r.Slot)
			}
			var p record.AccountVerdictsPayload
			if err := json.Unmarshal(r.Payload, &p); err != nil {
				f.t.Fatal(err)
			}
			out = append(out, p)
		}
	}
	return out
}

// Each run that may reserve records its verdicts once (setup.md 8.5, Verdicts to the hub): every
// account with a known login, eligible ones ranked, the others with why, their skip level and
// when they free up. A run that says the same within 15 minutes writes none; --no-reserve none.
func TestAccountNextVerdicts(t *testing.T) {
	f := newNextFixture(t)
	f.set("next.accounts.claude-2.mode", "protected")
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 99, 99),
		seat("seat_2", "a2", "o2", "two@example.test", "", 50, 30)} // protected: 5h 50 >= 45
	r := f.run("--json")
	if r.code != exitNoAccount {
		t.Fatalf("code %d err %s", r.code, r.errOut)
	}
	vs := f.outboxVerdicts()
	if len(vs) != 1 {
		t.Fatalf("verdicts %+v", vs)
	}
	v := vs[0]
	if v.Strategy != "lanes" || v.NextFree == nil || len(v.Accounts) != 2 {
		t.Fatalf("verdict %+v", v)
	}
	byAcct := map[string]record.AccountVerdict{}
	for _, a := range v.Accounts {
		byAcct[a.Account] = a
	}
	p := byAcct["a2"]
	if p.Eligible || p.Kind != "protected" || p.Org != "o2" || p.Reason == "" || p.SkipLevel == nil || *p.SkipLevel != 2 ||
		p.Rank != nil || p.FiveHourPct == nil || *p.FiveHourPct != 50 || p.BurstCap == nil {
		t.Errorf("protected account %+v", p)
	}
	if a := byAcct["a1"]; a.Eligible || a.SkipLevel == nil || *a.SkipLevel != 3 {
		t.Errorf("full account %+v", a)
	}
	if len(f.spawns) != 1 {
		t.Errorf("spawns %v, want one background flush", f.spawns)
	}
	// The same again: not written.
	f.run("--json")
	if n := len(f.outboxVerdicts()); n != 1 {
		t.Fatalf("a repeat was written: %d", n)
	}
	// The same 16 minutes later: written again.
	mark := filepath.Join(f.dirs.State.Path, "next", "verdict.json")
	if fi, err := os.Stat(mark); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mark %v %v", fi, err)
	}
	var m verdictMark
	b, _ := os.ReadFile(mark)
	_ = json.Unmarshal(b, &m)
	m.At = m.At.Add(-16 * time.Minute)
	b, _ = json.Marshal(m)
	_ = os.WriteFile(mark, b, 0o600)
	f.run("--json")
	if n := len(f.outboxVerdicts()); n != 2 {
		t.Fatalf("after 16 minutes: %d", n)
	}
	// Something changed: written at once. --no-reserve writes nothing.
	f.seats[1] = seat("seat_2", "a2", "o2", "two@example.test", "", 10, 30)
	f.run("--json", "--no-reserve")
	if n := len(f.outboxVerdicts()); n != 2 {
		t.Fatalf("--no-reserve wrote a verdict: %d", n)
	}
	f.run("--json")
	vs = f.outboxVerdicts()
	if len(vs) != 3 {
		t.Fatalf("after a change: %d", len(vs))
	}
	for _, a := range vs[2].Accounts {
		if a.Account == "a2" && (!a.Eligible || a.Rank == nil || *a.Rank != 1 || a.SkipLevel != nil) {
			t.Errorf("eligible account %+v", a)
		}
	}
}

func TestVerdictDigestRounds(t *testing.T) {
	w1, w2, w3 := 40.2, 40.4, 41.0
	t1, t2 := "2026-11-18T12:00:10Z", "2026-11-18T12:00:50Z"
	base := func(w *float64, at *string) record.AccountVerdictsPayload {
		return record.AccountVerdictsPayload{Strategy: "lanes", Accounts: []record.AccountVerdict{
			{Account: "a", Org: "o", WeeklyPct: w, FreeAt: at}}}
	}
	if verdictDigest(base(&w1, &t1)) != verdictDigest(base(&w2, &t2)) {
		t.Error("levels within a point and times within a minute differ")
	}
	if verdictDigest(base(&w1, &t1)) == verdictDigest(base(&w3, &t1)) {
		t.Error("a level a point away is the same")
	}
}
