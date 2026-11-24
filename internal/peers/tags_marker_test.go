package peers

import "testing"

// Marker tags (no-move, the switch's scope tag) tell no holder when added (peers.md 3.4).
func TestMarkerTags(t *testing.T) {
	e := Env{MarkerTags: []string{"move-e2e"}}
	for tag, want := range map[string]bool{"no-move": true, "move-e2e": true, "slack-watcher": false} {
		if got := e.markerTag(tag); got != want {
			t.Errorf("%s: %v", tag, got)
		}
	}
}
