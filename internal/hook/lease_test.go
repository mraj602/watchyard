package hook

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/leases"
)

func TestSessionStartAndEndMarkTheLease(t *testing.T) {
	sb := newSandbox(t)
	g := leases.Ledger{Dir: filepath.Join(sb.state(), "next")}
	const sid = "5b0f6a0e-1c2d-4e5f-8a9b-0c1d2e3f4a5b"
	if err := g.Append(leases.Line{Op: leases.OpLease, ID: "L1", At: time.Now().Add(-time.Minute), Key: "acct:a/o", SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	start := []byte(`{"hook_event_name":"SessionStart","session_id":"` + sid + `","source":"startup","cwd":"/tmp"}`)
	sb.run("SessionStart", start)
	sb.run("SessionStart", start) // a resume: launched is already there
	sb.run("SessionStart", []byte(`{"hook_event_name":"SessionStart","session_id":"other","source":"startup"}`))
	got, skipped, err := g.Read(time.Now())
	if err != nil || skipped != 0 || len(got) != 1 || got[0].Launched.IsZero() || !got[0].Ended.IsZero() {
		t.Fatalf("after SessionStart: %+v skipped %d err %v (errors: %s)", got, skipped, err, sb.errorLog())
	}
	sb.run("SessionEnd", []byte(`{"hook_event_name":"SessionEnd","session_id":"`+sid+`","reason":"exit"}`))
	got, _, _ = g.Read(time.Now())
	if len(got) != 1 || got[0].Ended.IsZero() {
		t.Fatalf("after SessionEnd: %+v", got)
	}
	if n := len(sb.ofKind("hook_event")); n != 4 {
		t.Fatalf("hook events: %d, want 4 (the ledger never replaces the record)", n)
	}
}

func TestNoLedgerNoMark(t *testing.T) {
	sb := newSandbox(t)
	sb.run("SessionStart", []byte(`{"hook_event_name":"SessionStart","session_id":"s1","source":"startup"}`))
	if got, _, _ := (leases.Ledger{Dir: filepath.Join(sb.state(), "next")}).Read(time.Now()); len(got) != 0 {
		t.Fatalf("a session without a lease wrote %v", got)
	}
	if e := sb.errorLog(); e != "" {
		t.Fatalf("errors: %s", e)
	}
}
