package mover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setup.md 8.6, Pinned sessions: Claude Code's own pins, <dir>/jobs/pins.json.
func TestParseNativePins(t *testing.T) {
	ids, err := ParseNativePins([]byte(`["48d8d877","2abbe6d1"," ","48d8d877"]`))
	if err != nil || strings.Join(ids, ",") != "48d8d877,2abbe6d1" {
		t.Fatalf("ids %v %v", ids, err)
	}
	for _, bad := range []string{`{"a":1}`, `[1,2]`, `not json`, ``} {
		if _, err := ParseNativePins([]byte(bad)); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestNativePinnedPrefix(t *testing.T) {
	pins := []string{"48d8d877", "2abbe6d1"}
	for id, want := range map[string]bool{
		"2abbe6d1-1111-4000-8000-000000000000": true,
		"48d8d877":                             true,
		"2abbe6d2-1111-4000-8000-000000000000": false,
		"x2abbe6d1":                            false,
	} {
		if got := NativePinned(pins, id); got != want {
			t.Fatalf("%s: %v", id, got)
		}
	}
	if NativePinned([]string{""}, "abc") || NativePinned(nil, "abc") {
		t.Fatal("an empty pin matched")
	}
}

// A missing file is no pins and no warning; an unparsable one is no pins and a warning; a
// change is read again (mtime/size cache).
func TestNativePinsFile(t *testing.T) {
	dir := t.TempDir()
	var warns []string
	warn := func(m string) { warns = append(warns, m) }
	if ids := NativePins(dir, warn); ids != nil || len(warns) != 0 {
		t.Fatalf("missing: %v %v", ids, warns)
	}
	path := NativePinsFile(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(s string, mod time.Time) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now().Add(-time.Hour)
	write(`["2abbe6d1"`, t0)
	if ids := NativePins(dir, warn); ids != nil || len(warns) != 1 || !strings.Contains(warns[0], "not understood") {
		t.Fatalf("unparsable: %v %v", ids, warns)
	}
	write(`["2abbe6d1"]`, t0.Add(time.Minute))
	if ids := NativePins(dir, warn); strings.Join(ids, ",") != "2abbe6d1" || len(warns) != 1 {
		t.Fatalf("fixed: %v %v", ids, warns)
	}
	// Same size and time: the cached ids (the file is not read again).
	write(`["48d8d877"]`, t0.Add(time.Minute))
	if ids := NativePins(dir, warn); strings.Join(ids, ",") != "2abbe6d1" {
		t.Fatalf("cache: %v", ids)
	}
	write(`["48d8d877"]`, t0.Add(2*time.Minute))
	if ids := NativePins(dir, warn); strings.Join(ids, ",") != "48d8d877" {
		t.Fatalf("changed: %v", ids)
	}
	b, _ := os.ReadFile(path)
	if string(b) != `["48d8d877"]` {
		t.Fatalf("the file was written: %s", b)
	}
}
