//go:build fakeinbox && peerstest

package peers_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// serviceWorld: A (work) and B (personal) on this machine, a personal session on another
// machine, the matrix letting work message personal only, and Doorbell approved as work.
func serviceWorld(t *testing.T) (*world, *fakeRemote, peers.Env, peers.TrustRecord) {
	w := newWorld(t, peers.ModeBypass, peers.ModeBypass)
	w.allow([][]string{{"acc-work", "acc-personal"}}, nil)
	r := &fakeRemote{account: "acc-personal", tags: map[string][]string{}}
	e := w.env()
	e.Remote = r
	rec := peers.TrustRecord{Venv: "/v", Program: "/v/bin/doorbell", Interpreter: "/py", Machine: "m_TEST", MachineName: "laptop",
		Account: "acc-work", AccountLabel: "work", ApprovedAt: w.now}
	return w, r, e, rec
}

// A trusted send to a tag reaches every holder the recorded account may message, with the
// service header, never as bypass, and the audit says so; no text is logged.
func TestSendServiceToTag(t *testing.T) {
	w, r, e, rec := serviceWorld(t)
	r.tags["sess-b"] = []string{"ci"}
	r.tags["sess-r"] = []string{"ci"}
	r.tags["sess-a"] = []string{"ci"} // work: a same-account holder, allowed by same_account
	lines, rf := e.SendService(rec, "tag:ci", "build 42 failed", false, false, peers.SendOpts{})
	if rf != nil {
		t.Fatal(rf)
	}
	if len(lines) != 3 {
		t.Fatalf("lines: %+v", lines)
	}
	for _, l := range lines {
		if l.Refusal != nil {
			t.Errorf("%s: %+v", l.Target.Handle, l.Refusal)
		}
	}
	if !w.b.Wait(1, 2*time.Second) || !w.a.Wait(1, 2*time.Second) {
		t.Fatal("not delivered on this machine")
	}
	got := w.b.All()[0].Wrapper
	if !strings.HasPrefix(got.Body, "[Watchyard] from doorbell (service) on laptop · account work · tag ci · thread t_") ||
		!strings.Contains(got.Body, "reads no replies") || !strings.HasSuffix(got.Body, "build 42 failed") {
		t.Errorf("body: %q", got.Body)
	}
	if got.FromMode != peers.ModePrompting || got.FromSession != "svc-doorbell-m_TEST" || got.FromName != "doorbell (service) on laptop" {
		t.Errorf("wrapper: %+v", got)
	}
	if r.sentTo["sess-r"] != 1 || r.last.Service != peers.ServiceDoorbell || r.last.Tag != "ci" {
		t.Errorf("remote: %v %+v", r.sentTo, r.last)
	}
	audit, _ := os.ReadFile(e.AuditPath())
	if strings.Count(string(audit), `"from_kind":"service:doorbell"`) < 2 || !strings.Contains(string(audit), `"tag":"ci"`) ||
		strings.Contains(string(audit), "build 42") {
		t.Errorf("audit: %s", audit)
	}
}

// The matrix is the recorded account's: approved as personal, Doorbell cannot reach work.
func TestSendServiceMatrixUsesRecordedAccount(t *testing.T) {
	w, r, e, rec := serviceWorld(t)
	rec.Account, rec.AccountLabel = "acc-personal", "personal"
	r.tags["sess-a"] = []string{"ops"}
	_, rf := e.SendService(rec, "tag:ops", "hello", false, false, peers.SendOpts{})
	if rf == nil || rf.Code != peers.CodeNoHolder {
		t.Fatalf("refusal: %+v", rf)
	}
	if len(w.a.All()) != 0 {
		t.Error("delivered despite the matrix")
	}
}

// --one delivers to the first holder only; --same-host keeps this machine's.
func TestSendServiceOneAndSameHost(t *testing.T) {
	w, r, e, rec := serviceWorld(t)
	r.tags["sess-b"] = []string{"ci"}
	r.tags["sess-r"] = []string{"ci"}
	lines, rf := e.SendService(rec, "tag:ci", "one", true, false, peers.SendOpts{})
	if rf != nil || len(lines) != 1 {
		t.Fatalf("--one: %+v %+v", lines, rf)
	}
	lines, rf = e.SendService(rec, "tag:ci", "here", false, true, peers.SendOpts{})
	if rf != nil || len(lines) != 1 || lines[0].Target.Session != "sess-b" {
		t.Fatalf("--same-host: %+v %+v", lines, rf)
	}
	_ = w
}

// Doorbell's sends to one holder share its own thread with that holder.
func TestSendServiceOwnThread(t *testing.T) {
	w, r, e, rec := serviceWorld(t)
	r.tags["sess-b"] = []string{"ci"}
	l1, rf := e.SendService(rec, "tag:ci", "first", false, false, peers.SendOpts{})
	if rf != nil || l1[0].Refusal != nil {
		t.Fatalf("%+v %+v", l1, rf)
	}
	l2, rf := e.SendService(rec, "tag:ci", "second", false, false, peers.SendOpts{})
	if rf != nil || l2[0].Refusal != nil {
		t.Fatalf("%+v %+v", l2, rf)
	}
	if l1[0].Res.Thread == "" || l1[0].Res.Thread != l2[0].Res.Thread {
		t.Errorf("threads: %s %s", l1[0].Res.Thread, l2[0].Res.Thread)
	}
	if !w.b.Wait(2, 2*time.Second) {
		t.Fatal("not delivered")
	}
}

// Content rules and the kill switch apply as to any send.
func TestSendServiceChecks(t *testing.T) {
	w, r, e, rec := serviceWorld(t)
	r.tags["sess-b"] = []string{"ci"}
	if _, rf := e.SendService(rec, "tag:ci", "", false, false, peers.SendOpts{}); rf == nil {
		t.Error("empty text sent")
	}
	if _, rf := e.SendService(rec, "tag:Bad Tag", "x", false, false, peers.SendOpts{}); rf == nil {
		t.Error("a bad tag accepted")
	}
	w.allow([][]string{{"acc-work", "acc-personal"}}, map[string]any{"paused": true})
	if _, rf := e.SendService(rec, "tag:ci", "x", false, false, peers.SendOpts{}); rf == nil || rf.Code != peers.CodePaused {
		t.Errorf("paused: %+v", rf)
	}
}
