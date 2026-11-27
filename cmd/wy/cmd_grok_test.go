package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/grokpeer"
)

func fakeGrok(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := grokLocate
	grokLocate = func(string) (string, error) { return p, nil }
	oh, state := grokHost, t.TempDir()
	grokHost = func() *grokpeer.Host { return &grokpeer.Host{State: state, Spawn: func(string) error { return nil }} }
	t.Cleanup(func() { grokLocate, grokHost = old, oh })
}

func runGrokCmd(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	code := dispatch(append([]string{"grok"}, args...), &o, &e)
	return code, o.String(), e.String()
}

func TestGrokRun(t *testing.T) {
	fakeGrok(t, `echo '{"text":"hello from grok","sessionId":"s1","num_turns":1,"usage":{"total_tokens":5}}'`)
	code, out, errs := runGrokCmd("run", "say", "hi")
	if code != 0 || strings.TrimSpace(out) != "hello from grok" || !strings.Contains(errs, "s1") {
		t.Fatal(code, out, errs)
	}
	code, out, _ = runGrokCmd("run", "--json", "x")
	if code != 0 || !strings.Contains(out, `"sessionId":"s1"`) {
		t.Fatal(code, out)
	}
}

func TestGrokRunAuthAndUsage(t *testing.T) {
	fakeGrok(t, `echo '{"type":"error","message":"not authenticated"}'; exit 1`)
	if code, _, errs := runGrokCmd("run", "x"); code != 4 || !strings.Contains(errs, "grok login") {
		t.Fatal(code, errs)
	}
	if code, _, _ := runGrokCmd("run"); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := runGrokCmd(); code != 2 {
		t.Fatal(code)
	}
	if code, _, _ := runGrokCmd("bogus"); code != 2 {
		t.Fatal(code)
	}
	if code, out, _ := runGrokCmd("--help"); code != 0 || !strings.Contains(out, "wy grok run") {
		t.Fatal(code)
	}
}

func TestGrokStatus(t *testing.T) {
	fakeGrok(t, `case "$1" in --version) echo "grok 1.2.3";; models) echo "You are not authenticated.";; esac`)
	code, out, _ := runGrokCmd("status")
	if code != 4 || !strings.Contains(out, "grok 1.2.3") || !strings.Contains(out, "login --device-auth") {
		t.Fatal(code, out)
	}
	fakeGrok(t, `case "$1" in --version) echo "grok 1.2.3";; models) echo "Default model: grok-4.6";; esac`)
	if code, out, _ = runGrokCmd("status"); code != 0 || !strings.Contains(out, "signed in  true") {
		t.Fatal(code, out)
	}
}

func TestGrokNotFound(t *testing.T) {
	old := grokLocate
	defer func() { grokLocate = old }()
	grokLocate = func(string) (string, error) { return "", os.ErrNotExist }
	if code, _, _ := runGrokCmd("status"); code != 3 {
		t.Fatal(code)
	}
	if code, _, _ := runGrokCmd("run", "x"); code != 3 {
		t.Fatal(code)
	}
}

func TestGrokRunRegistersPeer(t *testing.T) {
	fakeGrok(t, `echo '{"text":"ok","sessionId":"sess-1","num_turns":2,"usage":{"total_tokens":7}}'`)
	if code, _, e := runGrokCmd("run", "--name", "worker", "hi"); code != 0 {
		t.Fatal(code, e)
	}
	ss := grokHost().Sessions()
	if len(ss) != 1 || ss[0].ID != "sess-1" || ss[0].Name != "worker" || ss[0].Account != "grok" {
		t.Fatalf("sessions = %+v", ss)
	}
	if code, out, _ := runGrokCmd("list"); code != 0 || !strings.Contains(out, "sess-1\tidle\tworker") {
		t.Fatal(code, out)
	}
	if code, _, _ := runGrokCmd("forget", "sess-1"); code != 0 || len(grokHost().Sessions()) != 0 {
		t.Fatal("forget failed")
	}
}

func TestGrokAdoptAndAccountLines(t *testing.T) {
	fakeGrok(t, `if [ "$1" = usage ] && [ "$2" = --json ]; then echo "Error: no service" >&2; exit 1; fi
echo '{"sessionId":"a1","session":{"inputTokens":3,"outputTokens":2,"totalTokens":5,"modelCalls":1,"costUsdTicks":100000000,"turnCount":1,"primaryModelId":"m"}}'`)
	if code, out, e := runGrokCmd("adopt", "a1", "--name", "pal", "--dir", "/w"); code != 0 || !strings.Contains(out, "pal") {
		t.Fatal(code, out, e)
	}
	var b bytes.Buffer
	grokAccountLines(context.Background(), &b)
	got := b.String()
	if !strings.Contains(got, "plan usage unavailable") || !strings.Contains(got, "pal") || !strings.Contains(got, "5 tokens") || !strings.Contains(got, "$0.010") {
		t.Fatalf("lines:\n%s", got)
	}
	if code, out, _ := runGrokCmd("usage", "a1"); code != 0 || !strings.Contains(out, "5 tokens") {
		t.Fatal(code, out)
	}
	if code, _, _ := runGrokCmd("usage"); code != 1 {
		t.Fatal("plan usage without the service should fail")
	}
}
