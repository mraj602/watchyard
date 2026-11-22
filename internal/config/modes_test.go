package config

import (
	"encoding/json"
	"testing"
)

// slots.modes.<folder> (setup.md 8.5, Kind; paths.md) takes protected or aggressive only, is
// typed into Slots.Modes and is not an unknown key for wy doctor.
func TestSlotModes(t *testing.T) {
	dir := t.TempDir()
	f, _ := Load(dir)
	if c, _ := f.Typed(); c.Slots.Modes == nil || len(c.Slots.Modes) != 0 {
		t.Fatalf("default modes %#v", c.Slots.Modes)
	}
	if err := f.Set("slots.modes.~/.claude-2", []string{"protected"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"normal", "conservative", "", "Protected"} {
		if err := f.Set("slots.modes.~/.claude-2", []string{bad}); err == nil {
			t.Errorf("mode %q accepted", bad)
		}
	}
	if err := f.SetJSON("slots.modes.~/.claude-3", json.RawMessage(`"aggressive"`)); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	g, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.Typed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Slots.Modes["~/.claude-2"] != "protected" || c.Slots.Modes["~/.claude-3"] != "aggressive" {
		t.Fatalf("modes %#v", c.Slots.Modes)
	}
	if v, _ := g.Get("slots.modes.~/.claude-2"); v != "protected" {
		t.Fatalf("get %#v", v)
	}
	if u := g.Unknown(); len(u) != 0 {
		t.Fatalf("unknown keys %v", u)
	}
	if err := g.Delete("slots.modes.~/.claude-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Get("slots.modes.~/.claude-2"); err == nil {
		t.Fatal("deleted mode still there")
	}
}
