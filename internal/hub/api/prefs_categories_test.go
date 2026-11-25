package api

import (
	"encoding/json"
	"testing"
)

// prefs.mute_categories (push.md 5.5): known categories only, none twice.
func TestDevicePrefsMuteCategories(t *testing.T) {
	for _, c := range []struct {
		raw string
		ok  bool
	}{
		{`{"mute_categories": ["drain", "switch"]}`, true},
		{`{"mute_categories": []}`, true},
		{`{"mute_categories": ["sound"]}`, false},
		{`{"mute_categories": ["drain", "drain"]}`, false},
	} {
		p, err := devCheckPrefs(json.RawMessage(c.raw))
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.raw, err)
		}
		if c.ok && c.raw == `{"mute_categories": ["drain", "switch"]}` && len(p.MuteCategories) != 2 {
			t.Errorf("kept %v", p.MuteCategories)
		}
	}
}
