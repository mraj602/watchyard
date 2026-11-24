package picker

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/config"
)

// `wy account next --explain` prints each account once (live on pre4.1 the blocked ones
// showed twice, once in order and once at the end).
func TestExplainLanesEachAccountOnce(t *testing.T) {
	blocked := Ranked{Label: "claude-3", Folder: "~/.claude-3", Kind: "aggressive", Reason: "7d 83% >= 88"}
	res := Result{Strategy: config.NextStrategyLanes, Now: lnow, Ranked: []Ranked{
		{Label: "claude-6", Folder: "~/.claude-6", Kind: "aggressive", Eligible: true},
		blocked,
		{Label: "claude-1", Folder: "~/.claude-1", Reason: "not logged in on this machine"},
		blocked,
	}}
	var b bytes.Buffer
	if err := Explain(&b, res, "hub", ""); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(b.String(), "~/.claude-3"); n != 1 {
		t.Errorf("claude-3 listed %d times:\n%s", n, b.String())
	}
}
