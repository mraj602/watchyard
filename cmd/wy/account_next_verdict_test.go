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
	// mode_from and the caps (record.md 4.11): a2 protected on this machine, its weekly cap the
	// pace line 85 × (1 − 72/168) + 10, 5-hour floor 45; a1 not chosen, aggressive 98 and 98.
	if p.ModeFrom != "local" || p.WeeklyCap == nil || *p.WeeklyCap != 58.6 || p.FiveHourCap == nil || *p.FiveHourCap != 45 {
		t.Errorf("protected mode_from %q caps %v %v", p.ModeFrom, p.WeeklyCap, p.FiveHourCap)
	}
	if a := byAcct["a1"]; a.ModeFrom != "none" || a.Kind != "aggressive" || a.WeeklyCap == nil || *a.WeeklyCap != 98 ||
		a.FiveHourCap == nil || *a.FiveHourCap != 98 {
		t.Errorf("unchosen mode_from %q caps %v %v", a.ModeFrom, a.WeeklyCap, a.FiveHourCap)
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

// The seat's hub mode (UsageSeat.mode with the machine token, Seat.mode with the board token)
// makes an account protected on this machine; this machine's next.accounts mode wins over it,
// and slots.modes counts only while the hub has none (setup.md 8.5, Kind).
func TestAccountNextHubMode(t *testing.T) {
	for _, usage := range []bool{true, false} {
		f := newNextFixture(t)
		if usage {
			f.machineToken()
			f.usage = true
		}
		s1 := seat("seat_1", "a1", "o1", "one@example.test", "", 10, 10)
		s1["mode"] = "protected"
		s2 := seat("seat_2", "a2", "o2", "two@example.test", "", 10, 10)
		s2["mode"] = nil
		f.seats = []map[string]any{s1, s2}
		f.set("slots.modes.~/.claude-2", "protected")
		r := f.run("--json", "--no-reserve")
		var doc struct {
			Accounts []struct {
				Folder   string `json:"folder"`
				Kind     string `json:"kind"`
				ModeFrom string `json:"mode_from"`
			} `json:"accounts"`
		}
		if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
			t.Fatalf("%v: %s %s", err, r.out, r.errOut)
		}
		got := map[string]string{}
		for _, a := range doc.Accounts {
			got[a.Folder] = a.Kind + "/" + a.ModeFrom
		}
		if got["~/.claude"] != "protected/hub" || got["~/.claude-2"] != "protected/proposed" {
			t.Errorf("usage %v: %v", usage, got)
		}
		f.set("next.accounts.one@example.test.mode", "aggressive")
		r = f.run("--explain", "--no-reserve")
		if !strings.Contains(r.out, "aggressive (set on this machine); hub says protected") ||
			!strings.Contains(r.out, "protected (proposed here)") || strings.Contains(r.out, "Choose on the board") {
			t.Errorf("usage %v explain:\n%s", usage, r.out)
		}
	}
}

// Each hub read keeps the seats' hub modes in <state>/next/modes.json (0600; not with
// --no-reserve); with the hub down the kept mode still applies, from "hub", and --explain says
// it is the cached one (setup.md 8.5, Kind).
func TestAccountNextHubModeCached(t *testing.T) {
	f := newNextFixture(t)
	f.machineToken()
	f.usage = true
	s1 := seat("seat_1", "a1", "o1", "one@example.test", "", 10, 10)
	s1["mode"] = "protected"
	f.seats = []map[string]any{s1, seat("seat_2", "a2", "o2", "two@example.test", "", 10, 10)}
	path := filepath.Join(f.dirs.State.Path, "next", "modes.json")
	f.run("--json", "--no-reserve")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("--no-reserve kept the hub modes")
	}
	f.run("--json")
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("modes.json %v %v", fi, err)
	}
	f.hub.Close()
	r := f.run("--json", "--no-reserve")
	var doc struct {
		HubError string `json:"hub_error"`
		Accounts []struct {
			Folder   string `json:"folder"`
			Kind     string `json:"kind"`
			ModeFrom string `json:"mode_from"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
		t.Fatalf("%v: %s %s", err, r.out, r.errOut)
	}
	got := map[string]string{}
	for _, a := range doc.Accounts {
		got[a.Folder] = a.Kind + "/" + a.ModeFrom
	}
	if doc.HubError == "" || got["~/.claude"] != "protected/hub" || got["~/.claude-2"] != "aggressive/none" {
		t.Fatalf("hub down: %q %v", doc.HubError, got)
	}
	if r = f.run("--explain", "--no-reserve"); !strings.Contains(r.out, "protected (hub, cached)") {
		t.Fatalf("explain:\n%s", r.out)
	}
}
