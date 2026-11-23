package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/leases"
	"github.com/mraj602/watchyard/internal/record"
)

const (
	sidA = "11111111-1111-4111-8111-111111111111"
	sidB = "22222222-2222-4222-8222-222222222222"
	sidC = "33333333-3333-4333-8333-333333333333"
)

func (f *nextFixture) ledger() []leases.Lease {
	f.t.Helper()
	ls, skipped, err := leases.Ledger{Dir: filepath.Join(f.dirs.State.Path, "next")}.Read(nextNow)
	if err != nil || skipped != 0 {
		f.t.Fatalf("ledger: %v, %d skipped", err, skipped)
	}
	return ls
}

func (f *nextFixture) outboxLeases() []record.AccountLeasePayload {
	f.t.Helper()
	files, _ := filepath.Glob(filepath.Join(f.dirs.State.Path, "outbox", "*.jsonl"))
	var out []record.AccountLeasePayload
	for _, fn := range files {
		b, _ := os.ReadFile(fn)
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var r record.Record
			if json.Unmarshal([]byte(line), &r) != nil || r.Kind != record.KindAccountLease {
				continue
			}
			if r.Seat == nil || r.Slot != "" {
				f.t.Errorf("lease record envelope: seat %v slot %q", r.Seat, r.Slot)
			}
			var p record.AccountLeasePayload
			if err := json.Unmarshal(r.Payload, &p); err != nil {
				f.t.Fatal(err)
			}
			out = append(out, p)
		}
	}
	return out
}

type lanesDoc struct {
	Strategy string `json:"strategy"`
	Reserved bool   `json:"reserved"`
	Picked   *struct {
		Folder    string `json:"folder"`
		SessionID string `json:"session_id"`
	} `json:"picked"`
	Picks []struct {
		Folder    string `json:"folder"`
		SessionID string `json:"session_id"`
		Urgent    bool   `json:"urgent"`
		Reason    string `json:"reason"`
	} `json:"picks"`
	Waiting  int        `json:"waiting"`
	NextFree *time.Time `json:"next_free"`
	Skipped  int        `json:"skipped_ledger_lines"`
	Accounts []struct {
		Folder string `json:"folder"`
		Kind   string `json:"kind"`
		Lanes  *struct {
			Weekly    float64 `json:"weekly_pct"`
			Leases    int     `json:"leases"`
			Burst     int     `json:"burst"`
			BurstCap  int     `json:"burst_cap"`
			SkipLevel int     `json:"skip_level"`
		} `json:"lanes"`
	} `json:"accounts"`
}

func decodeLanes(t *testing.T, out string) lanesDoc {
	t.Helper()
	var d lanesDoc
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	return d
}

// Two sessions asked for in one call land on two accounts (round robin), each with its
// session id; the leases go to the ledger and, as account_lease records, to the outbox, and
// a background flush is started. The same session id again is refused with exit 3.
func TestAccountNextLanesBatch(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30),
		seat("seat_2", "a2", "o2", "two@example.test", "", 10, 30)}
	r := f.run("--json", "--n", "2", "--session-id", sidA, "--session-id", sidB, "--model", "opus")
	d := decodeLanes(t, r.out)
	if r.code != 0 || d.Strategy != "lanes" || !d.Reserved || len(d.Picks) != 2 || d.Waiting != 0 || d.Picked == nil {
		t.Fatalf("code %d doc %+v err %s", r.code, d, r.errOut)
	}
	if d.Picks[0].Folder == d.Picks[1].Folder || d.Picks[0].SessionID != sidA || d.Picks[1].SessionID != sidB {
		t.Errorf("picks %+v, want two accounts in session order", d.Picks)
	}
	ls := f.ledger()
	if len(ls) != 2 || ls[0].SessionID != sidA || ls[0].Batch == "" || ls[0].Batch != ls[1].Batch || ls[0].Model != "opus" ||
		ls[0].Strategy != "lanes" || ls[0].Kind != "aggressive" || *ls[0].C5 != 8 || ls[0].Key != "acct:a1/o1" {
		t.Fatalf("ledger %+v", ls)
	}
	recs := f.outboxLeases()
	if len(recs) != 2 || recs[0].LeaseID != ls[0].ID || recs[1].SessionID != sidB || recs[0].Batch != ls[0].Batch {
		t.Errorf("outbox leases %+v", recs)
	}
	if len(f.spawns) != 1 || strings.Join(f.spawns[0], " ") != "flush --quiet" {
		t.Errorf("spawns %v, want one background flush", f.spawns)
	}
	// The same session id again: exit 3, nothing written.
	r = f.run("--session-id", sidA)
	if r.code != exitDuplicateSession || r.out != "" || !strings.Contains(r.errOut, sidA) || len(f.ledger()) != 2 {
		t.Errorf("duplicate: code %d out %q err %q leases %d", r.code, r.out, r.errOut, len(f.ledger()))
	}
	r = f.run("--n", "2", "--session-id", sidC, "--session-id", sidC)
	if r.code != exitDuplicateSession || len(f.ledger()) != 2 {
		t.Errorf("an id given twice: code %d", r.code)
	}
	// The next single pick sees both leases: one lease each in the last 60 minutes.
	r = f.run("--json", "--no-reserve")
	d = decodeLanes(t, r.out)
	for _, a := range d.Accounts[:2] {
		if a.Lanes == nil || a.Lanes.Leases != 1 || a.Lanes.Burst != 1 || a.Lanes.BurstCap != 6 || a.Kind != "aggressive" {
			t.Errorf("account %+v", a)
		}
	}
}

func TestAccountNextLanesFlags(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30)}
	for _, args := range [][]string{
		{"--n", "2", "--session-id", sidA},            // not K ids
		{"--session-id", "not-a-uuid"},                // not a UUID
		{"--n", "51"},                                 // too many
		{"--n", "-1"},                                 // too few
		{"--n", "2", "--env"},                         // --env starts one
		{"--n", "2", "--exec"},                        // --exec starts one
		{"--strategy", "smart", "--n", "2"},           // lanes only
		{"--strategy", "smart", "--session-id", sidA}, // lanes only
		{"--strategy", "smart", "--urgent"},           // lanes only
		{"--strategy", "random"},                      // unknown
	} {
		if r := f.run(args...); r.code != 2 {
			t.Errorf("%v: code %d, want 2 (err %q)", args, r.code, r.errOut)
		}
	}
	if len(f.leaseFiles()) != 0 {
		t.Errorf("a refused call wrote a lease")
	}
	if r := f.run("--n", "1", "--session-id", strings.ToUpper(sidA)); r.code != 0 {
		t.Errorf("an upper-case UUID: code %d err %q", r.code, r.errOut)
	}
}

// Burst caps stop a batch; the placed picks are printed and leased (exit 4), and --urgent
// then passes an aggressive account's burst cap, never a limit.
func TestAccountNextLanesCapsAndUrgent(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30),
		seat("seat_2", "a2", "o2", "two@example.test", "", 10, 30)}
	// No stalling (setup.md 8.5): past both burst caps (6 an hour each) the lanes still go to
	// the aggressive accounts, marked over; nothing waits.
	r := f.run("--n", "14")
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	if r.code != 0 || len(lines) != 14 || strings.Contains(lines[11], "over (") || !strings.Contains(lines[12], "over (") ||
		!strings.Contains(lines[13], "over (") {
		t.Fatalf("code %d, %d lines, out %q err %q", r.code, len(lines), r.out, r.errOut)
	}
	if n := len(f.ledger()); n != 14 {
		t.Fatalf("%d leases, want the 14 placed", n)
	}
	r = f.run("--json", "--no-reserve")
	d := decodeLanes(t, r.out)
	if r.code != 0 || len(d.Picks) != 1 || d.Picked == nil {
		t.Fatalf("capped: code %d doc %+v", r.code, d)
	}
	r = f.run("--urgent", "--session-id", sidA)
	if r.code != 0 || !strings.Contains(r.out, "urgent") || !strings.Contains(r.errOut, "passed its burst cap") {
		t.Fatalf("urgent: code %d out %q err %q", r.code, r.out, r.errOut)
	}
	ls := f.ledger()
	if last := ls[len(ls)-1]; !last.Urgent || last.SessionID != sidA {
		t.Errorf("urgent lease %+v", last)
	}
	if recs := f.outboxLeases(); !recs[len(recs)-1].Urgent {
		t.Errorf("urgent not on the hub record")
	}
	// At a hard limit --urgent places nothing.
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 99),
		seat("seat_2", "a2", "o2", "two@example.test", "", 99, 30)}
	if r := f.run("--urgent"); r.code != exitNoAccount {
		t.Errorf("urgent at the limits: code %d out %q", r.code, r.out)
	}
}

// A protected account takes lanes only when no aggressive account can under the hard limits;
// past its gate it is still used rather than making the lanes wait (no stalling).
func TestAccountNextLanesProtected(t *testing.T) {
	f := newNextFixture(t)
	f.set("next.accounts.claude-2.mode", "protected")
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 99),
		seat("seat_2", "a2", "o2", "two@example.test", "", 99, 30)} // protected at its 5h hard limit
	r := f.run("--json", "--n", "3")
	d := decodeLanes(t, r.out)
	if r.code != exitNoAccount || len(d.Picks) != 0 || d.Waiting != 3 || len(f.leaseFiles()) != 0 {
		t.Fatalf("code %d doc %+v", r.code, d)
	}
	f.seats[1] = seat("seat_2", "a2", "o2", "two@example.test", "", 50, 30) // past its 45% gate
	r = f.run("--json", "--no-reserve", "--n", "3")
	if d = decodeLanes(t, r.out); r.code != 0 || len(d.Picks) != 3 || !strings.Contains(d.Picks[0].Reason, "protected, over (") {
		t.Fatalf("past the gate: code %d doc %+v", r.code, d)
	}
	var kinds []string
	for _, a := range d.Accounts {
		kinds = append(kinds, a.Folder+"="+a.Kind)
	}
	if !strings.Contains(strings.Join(kinds, " "), "~/.claude-2=protected") {
		t.Errorf("kinds %v", kinds)
	}
	// Under its floor and its pace line, the protected account takes lanes: 2 an hour within
	// its burst cap, the third over it.
	f.seats[1] = seat("seat_2", "a2", "o2", "two@example.test", "", 10, 30)
	r = f.run("--n", "3")
	if r.code != 0 || strings.Count(r.out, "~/.claude-2") != 3 || strings.Count(r.out, "over (") != 1 {
		t.Errorf("code %d out %q", r.code, r.out)
	}
}

// --exec with a session id starts claude with that id, so the hook marks the lease launched;
// a command that names its own session is left alone.
func TestAccountNextLanesExecSessionID(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30)}
	r := f.run("--exec", "--session-id", sidA, "--", "claude", "-p", "hi")
	if r.code != 0 || strings.Join(r.exec, " ") != "/usr/bin/claude claude --session-id "+sidA+" -p hi" {
		t.Fatalf("exec %v code %d err %s", r.exec, r.code, r.errOut)
	}
	r = f.run("--exec", "--session-id", sidB, "--", "claude", "--resume", "x")
	if strings.Join(r.exec, " ") != "/usr/bin/claude claude --resume x" {
		t.Errorf("exec %v", r.exec)
	}
	r = f.run("--exec", "--session-id", sidC, "--", "env")
	if strings.Join(r.exec, " ") != "/usr/bin/env env" {
		t.Errorf("exec %v", r.exec)
	}
}

// A ledger line that cannot be read is skipped and counted, never fatal.
func TestAccountNextLanesDamagedLedger(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30)}
	dir := filepath.Join(f.dirs.State.Path, "next", "leases")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nextNow.Format("2006-01-02")+".jsonl"), []byte("{bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.run("--json")
	d := decodeLanes(t, r.out)
	if r.code != 0 || d.Skipped != 1 || len(d.Picks) != 1 {
		t.Errorf("code %d doc %+v", r.code, d)
	}
}

// The hub's calibration factors are used and kept in next/calibration.json; without the hub
// the kept copy is used, and an account the hub has not calibrated is fitted locally
// (setup.md 8.5, Calibration). --no-reserve writes nothing.
func TestAccountNextCalibration(t *testing.T) {
	f := newNextFixture(t)
	f.machineToken()
	f.usage = true
	s1 := seat("seat_1", "a1", "o1", "", "", 10, 30)
	fb := func(v float64) map[string]any {
		return map[string]any{"value": v, "fallback": v, "measured": nil, "weight": 0, "n": 0, "days": 0, "implausible": false}
	}
	s1["calibration"] = map[string]any{"at": nextNow.Add(-time.Hour).Format(time.RFC3339), "since": nil, "reset": nil,
		"f":     map[string]any{"value": 2.5, "fallback": 1.4, "measured": 2.5, "weight": 1, "n": 60, "days": 5, "implausible": false},
		"ratio": fb(7.2), "lane_price": fb(4), "lane_charge": 8, "lane_units": nil,
		"unexplained_pct_per_hour": nil, "unexplained_hours": 0}
	f.seats = []map[string]any{s1, seat("seat_2", "a2", "o2", "", "", 10, 30)}
	path := filepath.Join(f.dirs.State.Path, "next", "calibration.json")

	type calDoc struct {
		Accounts []struct {
			Folder string `json:"folder"`
			Lanes  *struct {
				Calibration *struct {
					Source     string  `json:"source"`
					LaneCharge float64 `json:"lane_charge"`
					F          struct {
						Value  float64 `json:"value"`
						Weight float64 `json:"weight"`
					} `json:"f"`
				} `json:"calibration"`
			} `json:"lanes"`
		} `json:"accounts"`
	}
	sources := func(args ...string) map[string]string {
		t.Helper()
		r := f.run(args...)
		var d calDoc
		if err := json.Unmarshal([]byte(r.out), &d); err != nil {
			t.Fatalf("json: %v\n%s\n%s", err, r.out, r.errOut)
		}
		out := map[string]string{}
		for _, a := range d.Accounts {
			if a.Lanes != nil && a.Lanes.Calibration != nil {
				c := a.Lanes.Calibration
				out[a.Folder] = c.Source + " " + strconv.FormatFloat(c.F.Value, 'f', -1, 64)
			}
		}
		return out
	}

	got := sources("--json", "--no-reserve")
	if got["~/.claude"] != "hub 2.5" || got["~/.claude-2"] != "local 1.4" {
		t.Fatalf("sources %v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("--no-reserve wrote the calibration copy")
	}
	sources("--json")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("calibration copy: %v %v", err, st)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"acct:a1/o1"`) || strings.Contains(string(b), `"acct:a2/o2"`) {
		t.Fatalf("copy %s", b)
	}
	if got := sources("--json", "--no-hub", "--no-reserve"); got["~/.claude"] != "cached 2.5" || got["~/.claude-2"] != "local 1.4" {
		t.Fatalf("without the hub: %v", got)
	}
	r := f.run("--explain", "--no-reserve")
	if !strings.Contains(r.out, "(hub): f 2.50/M units (weight 1.00, n 60)") {
		t.Fatalf("explain:\n%s", r.out)
	}
}

// drainSeat is an aggressive seat at weekly 80% whose week resets in 6 hours: it drains.
func drainSeat() map[string]any {
	s := seat("seat_2", "a2", "o2", "two@example.test", "", 10, 80)
	s["windows"].([]map[string]any)[1]["resets_at"] = nextNow.Add(6 * time.Hour).Format(time.RFC3339)
	return s
}

// A drain pick carries drain, finish_by (the weekly reset − 10 minutes) and weekly_reset in
// --json; --exec passes WY_FINISH_BY and WY_ACCOUNT_RESET (RFC 3339 UTC) for it, and nothing
// for a pick that is not a drain pick (owner Q4).
func TestAccountNextLanesDrain(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30), drainSeat()}
	r := f.run("--json", "--no-reserve", "--n", "3")
	var d struct {
		Picks []struct {
			Folder      string     `json:"folder"`
			Drain       bool       `json:"drain"`
			FinishBy    *time.Time `json:"finish_by"`
			WeeklyReset *time.Time `json:"weekly_reset"`
		} `json:"picks"`
		Accounts []struct {
			Folder string `json:"folder"`
			Lanes  *struct {
				Class string `json:"class"`
				Drain *struct {
					Capacity int `json:"capacity"`
				} `json:"drain"`
			} `json:"lanes"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(r.out), &d); err != nil || r.code != 0 || len(d.Picks) != 3 {
		t.Fatalf("code %d err %v out %s", r.code, err, r.out)
	}
	reset, finish := nextNow.Add(6*time.Hour), nextNow.Add(6*time.Hour-10*time.Minute)
	for _, p := range d.Picks {
		if !p.Drain || p.FinishBy == nil || !p.FinishBy.Equal(finish) || p.WeeklyReset == nil || !p.WeeklyReset.Equal(reset) ||
			!strings.HasSuffix(p.Folder, ".claude-2") {
			t.Errorf("pick %+v: every lane to the draining account, finish_by %s", p, finish)
		}
	}
	found := false
	for _, a := range d.Accounts {
		if strings.HasSuffix(a.Folder, ".claude-2") {
			found = a.Lanes != nil && a.Lanes.Class == "drain" && a.Lanes.Drain != nil && a.Lanes.Drain.Capacity >= 3
		}
	}
	if !found {
		t.Errorf("accounts: no drain row for .claude-2:\n%s", r.out)
	}
	r = f.run("--exec", "--", "claude")
	var env []string
	for _, kv := range r.env {
		if strings.HasPrefix(kv, "WY_") {
			env = append(env, kv)
		}
	}
	want := []string{"WY_FINISH_BY=" + finish.Format(time.RFC3339), "WY_ACCOUNT_RESET=" + reset.Format(time.RFC3339)}
	if r.code != 0 || strings.Join(env, " ") != strings.Join(want, " ") || !strings.Contains(r.errOut, "finish") {
		t.Errorf("drain exec: code %d env %v err %q", r.code, env, r.errOut)
	}
	// Not a drain pick: no WY_ variables.
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30)}
	r = f.run("--exec", "--", "claude")
	for _, kv := range r.env {
		if strings.HasPrefix(kv, "WY_") {
			t.Errorf("non-drain exec got %s", kv)
		}
	}
	// --expect: lanes only, not negative.
	for _, args := range [][]string{{"--expect", "-1h"}, {"--strategy", "smart", "--expect", "2h"}} {
		if r := f.run(args...); r.code != 2 {
			t.Errorf("%v: code %d", args, r.code)
		}
	}
	if r := f.run("--no-reserve", "--expect", "3h"); r.code != 0 {
		t.Errorf("--expect 3h: code %d err %q", r.code, r.errOut)
	}
}

// --expect: a class or a wall time, --expect-minutes and --expect-tokens; the pick line and
// --json show the class used.
func TestAccountNextLanesExpect(t *testing.T) {
	f := newNextFixture(t)
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 30), drainSeat()}
	for _, args := range [][]string{{"--expect", "huge"}, {"--expect", "0s"}, {"--expect-minutes", "-5"},
		{"--expect-tokens", "-1"}, {"--expect", "2h", "--expect-minutes", "30"}, {"--strategy", "smart", "--expect", "long"}} {
		if r := f.run(append([]string{"--no-reserve"}, args...)...); r.code != 2 {
			t.Errorf("%v: code %d err %q", args, r.code, r.errOut)
		}
	}
	for _, c := range []struct {
		args []string
		want string
	}{{[]string{"--expect", "short"}, "short"}, {[]string{"--expect", "LONG"}, "long"}, {[]string{"--expect", "40m"}, "medium"},
		{[]string{"--expect-minutes", "200"}, "long"}, {[]string{"--expect", "medium", "--expect-tokens", "2000000"}, "medium"}} {
		r := f.run(append([]string{"--no-reserve"}, c.args...)...)
		if r.code != 0 || !strings.Contains(r.out, "; expect "+c.want) {
			t.Errorf("%v: code %d out %q err %q", c.args, r.code, r.out, r.errOut)
		}
		r = f.run(append([]string{"--no-reserve", "--json"}, c.args...)...)
		var d struct {
			Picks []struct {
				Expect string `json:"expect"`
				Over   *bool  `json:"over"`
			} `json:"picks"`
		}
		if err := json.Unmarshal([]byte(r.out), &d); err != nil || len(d.Picks) != 1 || d.Picks[0].Expect != c.want || d.Picks[0].Over == nil {
			t.Errorf("%v json: %v %s", c.args, err, r.out)
		}
	}
}
