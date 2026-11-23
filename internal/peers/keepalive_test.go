package peers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeepAliveNote(t *testing.T) {
	const sid = "40f6a21a-2222-4000-8000-000000000002"
	dir := t.TempDir()
	if KeepAliveNote(dir, sid, "slack-watcher") != "" {
		t.Fatal("not a background session: no note")
	}
	job := filepath.Join(dir, "jobs", "40f6a21a")
	if err := os.MkdirAll(job, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(job, "state.json"), []byte(`{"state":"idle","sessionId":"`+sid+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	note := KeepAliveNote(dir, sid, "slack-watcher")
	for _, want := range []string{"background session (40f6a21a)", "not pinned", "1 to 8 hours", "tag slack-watcher", "claude agents", "Ctrl+T", "Do not edit"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %s", want, note)
		}
	}
	pins := filepath.Join(dir, "jobs", "pins.json")
	if err := os.WriteFile(pins, []byte(`["40f6a21a"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if KeepAliveNote(dir, sid, "slack-watcher") != "" {
		t.Fatal("pinned: no note")
	}
	b, _ := os.ReadFile(pins)
	if string(b) != `["40f6a21a"]` {
		t.Fatal("pins.json changed")
	}
	// Malformed pins: treated as no pins, never an error.
	_ = os.WriteFile(pins, []byte(`{`), 0o600)
	if KeepAliveNote(dir, sid, "x") == "" {
		t.Fatal("malformed pins: note expected")
	}
}

func TestDescribeNoHolderKeepsCode(t *testing.T) {
	rf := Refuse(CodeNoHolder, "no session you may message holds tag x")
	if ExitCode(rf.Code) != ExitUnsupported || ExitUnsupported != 3 {
		t.Fatal("no_holder must stay exit 3")
	}
	if d := Describe(rf); !strings.HasPrefix(d, "not delivered: no session you may message holds tag x. ") || !strings.HasSuffix(d, NoHolderHint) {
		t.Fatalf("describe %q", d)
	}
}
