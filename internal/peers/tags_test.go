package peers_test

import (
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/peers"
)

func TestValidTag(t *testing.T) {
	for _, ok := range []string{"a", "slack-watcher", "x1", "0", strings.Repeat("a", 40), "-"} {
		if !peers.ValidTag(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "Slack", "a_b", "a b", "a.b", "a@b", "tag:x", strings.Repeat("a", 41), "é", "a\n"} {
		if peers.ValidTag(bad) {
			t.Errorf("%q accepted", bad)
		}
		if rf := peers.CheckTag(bad); rf == nil || peers.ExitCode(rf.Code) != peers.ExitUsage {
			t.Errorf("CheckTag(%q) = %v", bad, rf)
		}
	}
}

// tag:<tag> is a tag address; anything with '@' or '.' after the prefix is a handle and
// resolves as before; a bad slug is a usage error, never a fall-through to handle matching.
func TestParseTagAddress(t *testing.T) {
	cases := []struct {
		in     string
		tag    string
		isTag  bool
		badTag bool
	}{
		{"tag:slack", "slack", true, false},
		{"TAG:slack", "slack", true, false},
		{"  tag:a-1 ", "a-1", true, false},
		{"tag:Slack", "", true, true},
		{"tag:", "", true, true},
		{"tag:x@work.laptop", "", false, false},
		{"@slack", "", false, false},
		{"slack", "", false, false},
		{"tagger@work.laptop", "", false, false},
	}
	for _, c := range cases {
		tag, isTag, bad := peers.ParseTagAddress(c.in)
		if tag != c.tag || isTag != c.isTag || (bad != nil) != c.badTag {
			t.Errorf("%q: tag %q isTag %v bad %v", c.in, tag, isTag, bad)
		}
	}
}

func entry(handle, state string, can string, sameHost bool, tags ...string) peers.ListEntry {
	return peers.ListEntry{Session: "s-" + handle, Handle: handle, State: state, Can: can, SameHost: sameHost, Tags: tags}
}

func handlesOf(es []peers.ListEntry) string {
	var hs []string
	for _, e := range es {
		hs = append(hs, e.Handle)
	}
	return strings.Join(hs, " ")
}

// Holders: only those with the tag that the matrix lets the sender message and that nothing
// blocks; ordered idle/done, then other states, then working/busy, then by handle.
func TestTagHoldersOrderAndFilter(t *testing.T) {
	es := []peers.ListEntry{
		entry("w1@a.m", "working", "start", true, "x"),
		entry("busy@a.m", "busy", "start", true, "x"),
		entry("asked@a.m", "asked", "start", false, "x"),
		entry("zdone@a.m", "done", "reply", false, "x"),
		entry("idle@a.m", "idle", "start", true, "x", "y"),
		entry("other@a.m", "idle", "start", true, "y"),
		entry("forbidden@a.m", "idle", "none", true, "x"),
	}
	blocked := entry("blocked@b.n", "idle", "start", false, "x")
	blocked.Blocked = "n's link keys are not owner-certified"
	es = append(es, blocked)
	if got := handlesOf(peers.TagHolders(es, "x")); got != "idle@a.m zdone@a.m asked@a.m busy@a.m w1@a.m" {
		t.Errorf("holders: %s", got)
	}
	// The order does not depend on the input order.
	rev := append([]peers.ListEntry(nil), es...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if a, b := handlesOf(peers.TagHolders(es, "x")), handlesOf(peers.TagHolders(rev, "x")); a != b {
		t.Errorf("not deterministic: %s / %s", a, b)
	}
	if got := handlesOf(peers.TagTargets(es, "x", true)); got != "idle@a.m busy@a.m w1@a.m" {
		t.Errorf("same host: %s", got)
	}
	if got := peers.TagTargets(es, "nobody", false); len(got) != 0 {
		t.Errorf("unknown tag: %v", got)
	}
}

func TestTagExitCodes(t *testing.T) {
	if peers.ExitCode(peers.CodeNoHolder) != 3 {
		t.Error("no_holder must exit 3")
	}
	if peers.ExitCode(peers.CodeLimitTags) != peers.ExitLimits {
		t.Error("too_many_tags must exit 6")
	}
	if peers.ExitCode(peers.CodeUsage) != peers.ExitUsage {
		t.Error("usage must exit 2")
	}
}

func TestNormTags(t *testing.T) {
	if got := strings.Join(peers.NormTags([]string{"b", "a", "b"}), ","); got != "a,b" {
		t.Errorf("NormTags: %s", got)
	}
	if peers.NormTags(nil) != nil {
		t.Error("nil stays nil")
	}
}
