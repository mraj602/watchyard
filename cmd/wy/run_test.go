//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/mover"
)

// wy run starts the command again from the order the mover leaves, then exits with the
// last command's code (setup.md 8.6, wy run).
func TestRunWrapperRelaunches(t *testing.T) {
	st := mover.NewState(t.TempDir())
	var errOut bytes.Buffer
	if code, err := wrap(st, []string{"sh", "-c", "exit 3"}, os.Environ(), &errOut); err != nil || code != 3 {
		t.Fatalf("plain: %d %v", code, err)
	}
	// The first command waits until the test has left an order, as the mover does before it
	// kills claude.
	flag := filepath.Join(t.TempDir(), "go")
	go func() {
		for {
			if r, ok := st.Run(os.Getpid()); ok && r.Child > 0 {
				_ = st.WriteOrder(os.Getpid(), mover.RunOrder{SessionID: "s1", ConfigDir: "/h/.claude-2", Argv: []string{"sh", "-c", "exit 5"}})
				_ = os.WriteFile(flag, nil, 0o600)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	code, err := wrap(st, []string{"sh", "-c", "while [ ! -e " + flag + " ]; do sleep 0.05; done; exit 9"}, os.Environ(), &errOut)
	if err != nil || code != 5 || !strings.Contains(errOut.String(), "moved this session to /h/.claude-2") {
		t.Fatalf("relaunch: %d %v %q", code, err, errOut.String())
	}
	if _, ok := st.Run(os.Getpid()); ok {
		t.Error("the run file stays after the wrapper ends")
	}
}

func TestClaudeShim(t *testing.T) {
	realDir, shimDir := t.TempDir(), t.TempDir()
	real := filepath.Join(realDir, "claude")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	path := shimDir + string(os.PathListSeparator) + realDir
	if err := installShim(shimDir, path, &out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(shimDir, "claude"))
	if !strings.Contains(string(b), shimMark) || !strings.Contains(string(b), " run -- "+real+` "$@"`) {
		t.Fatalf("shim:\n%s", b)
	}
	// Installing again finds the real claude past the shim; a foreign file is never replaced.
	if err := installShim(shimDir, path, &out); err != nil {
		t.Fatal(err)
	}
	if err := installShim(realDir, realDir, &out); err == nil {
		t.Error("replaced a claude that is not a shim")
	}
	if err := removeShim(realDir, &out); err == nil {
		t.Error("removed a claude that is not a shim")
	}
	if err := removeShim(shimDir, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(shimDir, "claude")); !os.IsNotExist(err) {
		t.Error("shim not removed")
	}
}
