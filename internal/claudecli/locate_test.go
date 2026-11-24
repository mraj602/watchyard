package claudecli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The timer's PATH may lack claude: LocateFrom tries the usual places, then hints that are
// claude's binary (never a script runtime).
func TestLocateFrom(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec bits")
	}
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if _, err := os.Stat("/usr/local/bin/claude"); err == nil {
		t.Skip("this machine has /usr/local/bin/claude")
	}
	if _, err := os.Stat("/opt/homebrew/bin/claude"); err == nil {
		t.Skip("this machine has /opt/homebrew/bin/claude")
	}
	home := t.TempDir()
	mk := func(p string) string {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	node := mk(filepath.Join(home, "n", "node"))
	ver := mk(filepath.Join(home, ".local", "share", "claude", "versions", "2.1.331"))
	if _, err := LocateFrom(home, node); !errors.Is(err, ErrNotFound) {
		t.Errorf("node hint taken: %v", err)
	}
	if p, err := LocateFrom(home, node, ver); err != nil || p != ver {
		t.Errorf("hint: %q %v", p, err)
	}
	local := mk(filepath.Join(home, ".local", "bin", "claude"))
	if p, err := LocateFrom(home, ver); err != nil || p != local {
		t.Errorf("~/.local/bin first: %q %v", p, err)
	}
	lookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	if p, _ := LocateFrom(home); p != "/usr/bin/claude" {
		t.Errorf("PATH first: %q", p)
	}
}
