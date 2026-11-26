package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeGrok(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := grokLocate
	grokLocate = func(string) (string, error) { return p, nil }
	t.Cleanup(func() { grokLocate = old })
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
