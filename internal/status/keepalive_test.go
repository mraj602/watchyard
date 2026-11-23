package status

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

func TestKeepaliveRetiredAndPinned(t *testing.T) {
	last := t0
	cases := []struct {
		name  string
		st    state.State
		k     Keepalive
		label string
		ret   bool
	}{
		{"interactive", state.Done, Keepalive{}, "done", false},
		{"background", state.Done, Keepalive{Background: true}, "done", false},
		{"pinned", state.Done, Keepalive{Background: true, Pinned: true}, "done (pinned)", false},
		{"retired after the last event", state.Done, Keepalive{Background: true, RetiredAt: last.Add(8 * time.Hour)}, RetiredLabel, true},
		{"retired, then respawned", state.Done, Keepalive{Background: true, RetiredAt: last.Add(-time.Hour)}, "done", false},
		{"already ended", state.Ended, Keepalive{Background: true, RetiredAt: last.Add(time.Hour)}, "ended", false},
		{"pins only count for background jobs", state.Done, Keepalive{Pinned: true}, "done", false},
	}
	for _, c := range cases {
		r := Row{State: c.st, LastEvent: last, Since: last}
		keepalive(&r, c.k)
		if r.Label() != c.label || (r.RetiredIdleAt != nil) != c.ret {
			t.Errorf("%s: label %q retired %v", c.name, r.Label(), r.RetiredIdleAt)
		}
	}
	r := Row{State: state.Failed, Detail: "overloaded", Background: true, Pinned: true}
	if r.Label() != "failed:overloaded (pinned)" {
		t.Errorf("failed label %q", r.Label())
	}
}

// Collect asks Keepalive per (slot, session) and the table explains a retired row.
func TestStatusShowsIdleRetirement(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	seat := &record.Seat{Account: "aaaaaaaa-0000-4000-8000-000000000002", Org: "bbbbbbbb-0000-4000-8000-000000000002", Src: "claude_json"}
	sid, last := feed(t, ob, "03-question", indexOfEvent(t, "03-question", "SessionEnd"), t0, "~/.claude-2", seat)
	now := t0.Add(9 * time.Hour)
	var asked []string
	opts := Options{OutboxDir: ob.Dir, Now: now, Keepalive: func(slot, id string) Keepalive {
		asked = append(asked, slot+" "+id)
		return Keepalive{Background: true, RetiredAt: last.Add(8 * time.Hour)}
	}}
	rows, err := Collect(opts)
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(rows, sid)
	if row == nil || row.RetiredIdleAt == nil || !row.Background || row.Label() != RetiredLabel {
		t.Fatalf("row %+v", row)
	}
	if len(asked) != 1 || asked[0] != "~/.claude-2 "+sid {
		t.Fatalf("asked %v", asked)
	}
	var out bytes.Buffer
	if err := Render(&out, rows, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), RetiredLabel) || !strings.Contains(out.String(), RetiredNote) {
		t.Fatalf("table:\n%s", out.String())
	}
	// Without Keepalive nothing changes.
	opts.Keepalive = nil
	rows, _ = Collect(opts)
	if r := findRow(rows, sid); r == nil || r.RetiredIdleAt != nil || r.Background {
		t.Fatalf("no keepalive: %+v", r)
	}
}
