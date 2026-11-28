//go:build !windows

package grokpeer

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/grokcli"
	"github.com/mraj602/watchyard/internal/peers"
)

func newHost(t *testing.T) (*Host, *[]grokcli.Options) {
	t.Helper()
	var mu sync.Mutex
	var calls []grokcli.Options
	h := &Host{State: t.TempDir(), Spawn: func(string) error { return nil }}
	h.Run = func(_ context.Context, o grokcli.Options) (*grokcli.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, o)
		return &grokcli.Result{SessionID: o.SessionID, NumTurns: 1, Tokens: 10}, nil
	}
	return h, &calls
}

func TestRegisterAndSessions(t *testing.T) {
	h, _ := newHost(t)
	if _, err := h.Register("abc12345-xyz", "/work", "", grokcli.Options{Yolo: true}, &grokcli.Result{NumTurns: 2, Tokens: 5}); err != nil {
		t.Fatal(err)
	}
	ss := h.Sessions()
	if len(ss) != 1 {
		t.Fatalf("sessions = %v", ss)
	}
	s := ss[0]
	if s.Account != "grok" || s.Label != "grok" || s.Status != "idle" || s.Name != "grok-abc12345" || !s.Bypass || s.Dir != "/work" {
		t.Fatalf("session = %+v", s)
	}
}

func TestRetention(t *testing.T) {
	h, _ := newHost(t)
	now := time.Now()
	h.Now = func() time.Time { return now.Add(-Retention - time.Hour) }
	h.Register("old1", "/w", "", grokcli.Options{}, nil)
	h.Now = func() time.Time { return now }
	if len(h.Sessions()) != 0 {
		t.Fatal("an old session should drop out of the list")
	}
}

func TestDeliverIdleSpawnsDrain(t *testing.T) {
	h, _ := newHost(t)
	var spawned []string
	h.Spawn = func(id string) error { spawned = append(spawned, id); return nil }
	h.Register("s1", "/w", "worker", grokcli.Options{}, nil)
	queued, err := h.Deliver("s1", "hello")
	if err != nil || queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	if len(spawned) != 1 || h.Pending("s1") != 1 {
		t.Fatalf("spawned=%v pending=%d", spawned, h.Pending("s1"))
	}
}

func TestDeliverBusyQueuesWithoutSpawn(t *testing.T) {
	h, _ := newHost(t)
	spawned := 0
	h.Spawn = func(string) error { spawned++; return nil }
	h.Register("s1", "/w", "", grokcli.Options{}, nil)
	l, err := h.Hold("s1")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Unlock()
	if h.Sessions()[0].Status != "busy" {
		t.Fatal("a held session is busy")
	}
	queued, err := h.Deliver("s1", "later")
	if err != nil || !queued || spawned != 0 {
		t.Fatalf("queued=%v err=%v spawned=%d", queued, err, spawned)
	}
	if _, err := h.Hold("s1"); err == nil {
		t.Fatal("a second hold must fail")
	}
}

func TestDeliverUnknownSession(t *testing.T) {
	h, _ := newHost(t)
	if _, err := h.Deliver("nope", "x"); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := h.Deliver("../etc", "x"); err == nil {
		t.Fatal("a path-like id must be refused")
	}
}

func TestDrainJoinsMessagesAndAccounts(t *testing.T) {
	h, calls := newHost(t)
	h.Register("s1", "/w", "", grokcli.Options{Model: "m1", Yolo: true}, nil)
	h.Deliver("s1", "one")
	time.Sleep(time.Millisecond)
	h.Deliver("s1", "two")
	if err := h.Drain(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
	c := (*calls)[0]
	if c.SessionID != "s1" || c.Dir != "/w" || c.Model != "m1" || !c.Yolo || !strings.Contains(c.Prompt, "one") || strings.Index(c.Prompt, "one") > strings.Index(c.Prompt, "two") {
		t.Fatalf("options = %+v", c)
	}
	r, _ := h.Load("s1")
	if r.Turns != 1 || r.Tokens != 10 || h.Pending("s1") != 0 {
		t.Fatalf("record = %+v", r)
	}
}

func TestDrainSkippedWhileHeld(t *testing.T) {
	h, calls := newHost(t)
	h.Register("s1", "/w", "", grokcli.Options{}, nil)
	h.Deliver("s1", "x")
	l, _ := h.Hold("s1")
	if err := h.Drain(context.Background(), "s1"); err != nil || len(*calls) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(*calls))
	}
	l.Unlock()
	h.Drain(context.Background(), "s1")
	if len(*calls) != 1 {
		t.Fatal("the message must be delivered once the lock is free")
	}
}

func TestDrainFailureKeepsRecord(t *testing.T) {
	h, _ := newHost(t)
	h.Run = func(context.Context, grokcli.Options) (*grokcli.Result, error) {
		return nil, grokcli.ErrNotAuthenticated
	}
	h.Register("s1", "/w", "", grokcli.Options{}, nil)
	h.Deliver("s1", "x")
	if err := h.Drain(context.Background(), "s1"); err == nil {
		t.Fatal("expected the run error")
	}
	if r, _ := h.Load("s1"); r.LastFail == "" {
		t.Fatal("last_fail should be set")
	}
}

func TestForget(t *testing.T) {
	h, _ := newHost(t)
	h.Register("s1", "/w", "", grokcli.Options{}, nil)
	h.Deliver("s1", "x")
	if err := h.Forget("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Load("s1"); ok || h.Pending("s1") != 0 {
		t.Fatal("forget removes the record and the inbox")
	}
}

var _ peers.AgentHost = (*Host)(nil)
