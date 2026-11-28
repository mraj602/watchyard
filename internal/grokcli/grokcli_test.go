package grokcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fake writes a shell script standing in for grok.
func fake(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArgs(t *testing.T) {
	got := strings.Join(Args(Options{Prompt: "hi", Model: "m", Effort: "high", MaxTurns: 3, Yolo: true, SessionID: "s", Dir: "/d"}), " ")
	want := "--output-format json --no-auto-update -m m --effort high --max-turns 3 --always-approve -r s --cwd /d -p hi"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRunOK(t *testing.T) {
	bin := fake(t, `printf "%s" '{"text":"done\n","stopReason":"end_turn","sessionId":"abc","num_turns":2,"usage":{"total_tokens":99}}'`)
	r, err := Run(context.Background(), bin, os.Environ(), Options{Prompt: "x"})
	if err != nil || r.Text != "done\n" || r.SessionID != "abc" || r.Tokens != 99 || r.NumTurns != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRunErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Run(ctx, "/nonexistent", nil, Options{Prompt: " "}); err == nil {
		t.Fatal("empty prompt accepted")
	}
	bin := fake(t, `echo '{"type":"error","message":"Not authenticated, please sign in"}'; exit 1`)
	if _, err := Run(ctx, bin, nil, Options{Prompt: "x"}); !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("got %v", err)
	}
	bin = fake(t, `echo '{"type":"error","message":"boom"}'; exit 1`)
	if _, err := Run(ctx, bin, nil, Options{Prompt: "x"}); err == nil || errors.Is(err, ErrNotAuthenticated) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
	bin = fake(t, `echo "login required" >&2; exit 2`)
	if _, err := Run(ctx, bin, nil, Options{Prompt: "x"}); !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("got %v", err)
	}
	bin = fake(t, `echo oops >&2; exit 2`)
	if _, err := Run(ctx, bin, nil, Options{Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("got %v", err)
	}
	bin = fake(t, `echo notjson`)
	if _, err := Run(ctx, bin, nil, Options{Prompt: "x"}); err == nil {
		t.Fatal("non-JSON accepted")
	}
	bin = fake(t, `sleep 5`)
	if _, err := Run(ctx, bin, nil, Options{Prompt: "x", Timeout: 100 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v", err)
	}
}

func TestPromptNotInError(t *testing.T) {
	bin := fake(t, `exit 3`)
	_, err := Run(context.Background(), bin, nil, Options{Prompt: "SECRET-PROMPT"})
	if err == nil || strings.Contains(err.Error(), "SECRET-PROMPT") {
		t.Fatalf("got %v", err)
	}
}

func TestLocate(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	if _, err := Locate(home); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".grok", "bin")
	os.MkdirAll(p, 0o755)
	os.WriteFile(filepath.Join(p, "grok"), []byte("x"), 0o755)
	if got, err := Locate(home); err != nil || !strings.HasSuffix(got, ".grok/bin/grok") {
		t.Fatal(got, err)
	}
}

func TestCheck(t *testing.T) {
	bin := fake(t, `case "$1" in --version) echo "grok 9.9.9";; models) echo "You are not authenticated.";; esac`)
	st, err := Check(context.Background(), bin, nil)
	if err != nil || st.Version != "grok 9.9.9" || st.Authenticated {
		t.Fatalf("%+v %v", st, err)
	}
	bin = fake(t, `case "$1" in --version) echo "grok 9.9.9";; models) echo "Default model: grok-4.6";; esac`)
	if st, _ = Check(context.Background(), bin, nil); !st.Authenticated {
		t.Fatal("expected authenticated")
	}
	if _, err = Check(context.Background(), fake(t, `true`), nil); err == nil {
		t.Fatal("silent --version accepted")
	}
}
