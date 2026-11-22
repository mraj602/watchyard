package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	r := f.run("--n", "14")
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	if r.code != exitNoAccount || len(lines) != 12 || !strings.Contains(r.errOut, "12 of 14 sessions placed") {
		t.Fatalf("code %d, %d lines, err %q", r.code, len(lines), r.errOut)
	}
	if n := len(f.ledger()); n != 12 {
		t.Fatalf("%d leases, want the 12 placed", n)
	}
	r = f.run("--json", "--no-reserve")
	d := decodeLanes(t, r.out)
	if r.code != exitNoAccount || len(d.Picks) != 0 || d.Picked != nil || d.NextFree == nil || !d.NextFree.Equal(nextNow.Add(time.Hour)) {
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

// Nothing ever falls back onto a protected account.
func TestAccountNextLanesProtected(t *testing.T) {
	f := newNextFixture(t)
	f.set("next.accounts.claude-2.mode", "protected")
	f.seats = []map[string]any{seat("seat_1", "a1", "o1", "one@example.test", "", 10, 99),
		seat("seat_2", "a2", "o2", "two@example.test", "", 50, 30)} // protected: 5h 50 >= 45
	r := f.run("--json", "--n", "3")
	d := decodeLanes(t, r.out)
	if r.code != exitNoAccount || len(d.Picks) != 0 || d.Waiting != 3 || len(f.leaseFiles()) != 0 {
		t.Fatalf("code %d doc %+v", r.code, d)
	}
	var kinds []string
	for _, a := range d.Accounts {
		kinds = append(kinds, a.Folder+"="+a.Kind)
	}
	if !strings.Contains(strings.Join(kinds, " "), "~/.claude-2=protected") {
		t.Errorf("kinds %v", kinds)
	}
	// Under its floor and its pace line, the protected account takes lanes: at most 2 an hour.
	f.seats[1] = seat("seat_2", "a2", "o2", "two@example.test", "", 10, 30)
	r = f.run("--n", "3")
	if r.code != exitNoAccount || strings.Count(r.out, "~/.claude-2") != 2 || !strings.Contains(r.out, "protected") {
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
