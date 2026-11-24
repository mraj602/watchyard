package plugin

import (
	"encoding/json"
	"io/fs"
	"sort"
	"testing"
)

// The eleven events of docs/spec/states.md section 2.
var wantEvents = []string{
	"Notification", "PermissionDenied", "PermissionRequest", "PostToolUse", "PostToolUseFailure", "PreCompact", "PreToolUse",
	"SessionEnd", "SessionStart", "Stop", "StopFailure", "SubagentStop", "UserPromptSubmit",
}

func TestEmbedHasPluginFiles(t *testing.T) {
	fsys := FS()
	for _, p := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "README.md", "skills/status/SKILL.md", "skills/peers/SKILL.md"} {
		if _, err := fs.Stat(fsys, p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	// Every skill folder on disk is embedded with its SKILL.md.
	entries, err := fs.ReadDir(fsys, "skills")
	if err != nil || len(entries) == 0 {
		t.Fatalf("skills: %v (%d)", err, len(entries))
	}
	for _, e := range entries {
		if _, err := fs.Stat(fsys, "skills/"+e.Name()+"/SKILL.md"); err != nil {
			t.Errorf("skill %s: %v", e.Name(), err)
		}
	}
}

func TestEmbeddedPluginJSON(t *testing.T) {
	b, err := fs.ReadFile(FS(), ".claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Name string }
	if err := json.Unmarshal(b, &m); err != nil || m.Name != Name {
		t.Fatalf("plugin.json name = %q (%v)", m.Name, err)
	}
}

func TestEmbeddedHooksHaveEveryEvent(t *testing.T) {
	b, err := fs.ReadFile(FS(), "hooks/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	var h struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	var got []string
	for ev, groups := range h.Hooks {
		got = append(got, ev)
		if len(groups) == 0 || len(groups[0].Hooks) == 0 || groups[0].Hooks[0].Command == "" {
			t.Errorf("event %s has no command", ev)
		}
	}
	sort.Strings(got)
	if len(got) != len(wantEvents) {
		t.Fatalf("events = %v, want %v", got, wantEvents)
	}
	for i := range got {
		if got[i] != wantEvents[i] {
			t.Fatalf("events = %v, want %v", got, wantEvents)
		}
	}
}
