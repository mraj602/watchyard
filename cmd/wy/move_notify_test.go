package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/record"
)

type fakePush struct {
	sent [][2]string
	fail bool
}

func (f *fakePush) direct(private bool) *movePush {
	return &movePush{Private: private, Send: func(_ context.Context, title, text string) error {
		if f.fail {
			return errors.New("down")
		}
		f.sent = append(f.sent, [2]string{title, text})
		return nil
	}}
}

// push is a machine without a hub and with direct push.
func (f *fakePush) push(private bool) moveOut { return moveOut{Direct: f.direct(private)} }

func TestMoveNotifySettings(t *testing.T) {
	c := config.Defaults()
	if !c.Account.MoveNotify || !c.Account.MoveDigest {
		t.Fatalf("defaults: %+v", c.Account)
	}
	f := newNextFixture(t)
	f.set("account.move_notify", "off")
	f.set("account.move_digest", "off")
	cf, err := config.Load(f.dirs.Config.Path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := cf.Typed()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Account.MoveNotify || cfg.Account.MoveDigest {
		t.Fatalf("after set off: %+v", cfg.Account)
	}
}

func TestMoveNoticesPush(t *testing.T) {
	f := newNextFixture(t)
	st := mover.NewState(f.dirs.State.Path)
	t0 := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	cfg := config.Defaults()
	cfg.Slots.Labels = map[string]string{"~/.claude-a": "work", "~/.claude-b": "home"}
	fp := &fakePush{}
	tick := func(at time.Time, c config.Config, p moveOut) {
		t.Helper()
		if err := runMoveNotices(context.Background(), f.dirs, c, at, p); err != nil && !fp.fail {
			t.Fatal(err)
		}
	}
	move := func(at time.Time, sid string) {
		t.Helper()
		if err := st.Append(mover.LogEntry{At: at, SessionID: sid, Name: sid, FromDir: "/h/.claude-a", ToDir: "/h/.claude-b", Auto: true, Reason: "5h over"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name    string
		at      time.Duration
		move    string
		cfgFn   func(*config.Config)
		private bool
		nilPush bool
		want    []string // pushes sent by this step (text)
	}{
		{name: "first run sets the cursor", at: 0},
		{name: "one move goes out", at: time.Minute, move: "s1", want: []string{"s1 work -> home: 5h over"}},
		{name: "inside the window it waits", at: 3 * time.Minute, move: "s2"},
		{name: "still inside the window", at: 4 * time.Minute, move: "s3"},
		{name: "batched after 5 minutes", at: 7 * time.Minute, want: []string{"2 moves: s2 work->home; s3 work->home"}},
		{name: "off pushes nothing", at: 20 * time.Minute, move: "s4", cfgFn: func(c *config.Config) { c.Account.MoveNotify = false }},
		{name: "private says only the count", at: 30 * time.Minute, move: "s5", private: true, want: []string{"1 session(s) moved to another account"}},
		{name: "no push path pushes nothing", at: 40 * time.Minute, move: "s6", nilPush: true},
	} {
		c0 := cfg
		if c.cfgFn != nil {
			c.cfgFn(&c0)
		}
		at := t0.Add(c.at)
		if c.move != "" {
			move(at.Add(-30*time.Second), c.move)
		}
		before := len(fp.sent)
		p := fp.push(c.private)
		if c.nilPush {
			p = moveOut{}
		}
		tick(at, c0, p)
		var got []string
		for _, s := range fp.sent[before:] {
			got = append(got, s[1])
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: pushed %q, want %q", c.name, got, c.want)
		}
	}
}

// Pulls into an owner-drained account go out as their own drain_pulled batch (the Drain
// channel); the other moves of the same batch stay session_moved (push.md 3.9).
func TestMoveNoticesSplitPulls(t *testing.T) {
	f := newNextFixture(t)
	st := mover.NewState(f.dirs.State.Path)
	t0 := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	cfg := config.Defaults()
	var got []record.SwitchNoticePayload
	o := moveOut{Hub: func(p record.SwitchNoticePayload) error { got = append(got, p); return nil }}
	if err := runMoveNotices(context.Background(), f.dirs, cfg, t0, o); err != nil {
		t.Fatal(err)
	}
	for i, r := range []string{"5h over", "pulled in: claude-4 is draining (owner, until 18:00) and has room", "pulled in: claude-4 is draining"} {
		if err := st.Append(mover.LogEntry{At: t0.Add(time.Duration(i+1) * time.Minute), SessionID: fmt.Sprint("s", i), Name: fmt.Sprint("s", i),
			FromDir: "/h/.claude-a", ToDir: "/h/.claude-b", Auto: true, Reason: r}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runMoveNotices(context.Background(), f.dirs, cfg, t0.Add(10*time.Minute), o); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("payloads %+v", got)
	}
	if got[0].Trigger != record.SwitchNoticeMoved || got[0].Moves != 1 || !strings.HasPrefix(got[0].Episode, "m:") {
		t.Errorf("moved %+v", got[0])
	}
	if got[1].Trigger != record.SwitchNoticePulled || got[1].Moves != 2 || !strings.HasPrefix(got[1].Episode, "p:") {
		t.Errorf("pulled %+v", got[1])
	}
}

func TestMoveDigestPush(t *testing.T) {
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.Local)
	for _, c := range []struct {
		name   string
		at     time.Duration
		on     bool // account.smart_switch
		digest bool // account.move_digest
		move   bool // a move in the last 24 h
		nilp   bool
		want   int
	}{
		{"on, 09:00", 9 * time.Hour, true, true, false, false, 1},
		{"before 09:00", 8 * time.Hour, true, true, false, false, 0},
		{"off but a move happened", 9 * time.Hour, false, true, true, false, 1},
		{"off and quiet", 9 * time.Hour, false, true, false, false, 0},
		{"digest setting off", 9 * time.Hour, true, false, true, false, 0},
		{"no push path", 9 * time.Hour, true, true, true, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newNextFixture(t)
			st := mover.NewState(f.dirs.State.Path)
			cfg := config.Defaults()
			cfg.Account.SmartSwitch, cfg.Account.MoveDigest = c.on, c.digest
			at := day.Add(c.at)
			if c.move {
				_ = st.Append(mover.LogEntry{At: at.Add(-3 * time.Hour), SessionID: "s1", FromDir: "/h/.claude", ToDir: "/h/.claude-2", Reason: "r"})
			}
			fp := &fakePush{}
			p := fp.push(false)
			if c.nilp {
				p = moveOut{}
			}
			if err := runMoveNotices(context.Background(), f.dirs, cfg, at, p); err != nil {
				t.Fatal(err)
			}
			// A second tick the same day sends nothing more.
			if err := runMoveNotices(context.Background(), f.dirs, cfg, at.Add(time.Minute), p); err != nil {
				t.Fatal(err)
			}
			if len(fp.sent) != c.want {
				t.Fatalf("sent %d, want %d: %v", len(fp.sent), c.want, fp.sent)
			}
			if c.want == 1 && (fp.sent[0][0] != "Watchyard daily digest" || !strings.HasPrefix(fp.sent[0][1], "Last 24h: ")) {
				t.Errorf("digest push: %v", fp.sent[0])
			}
		})
	}
	// A failed send is not stamped: the next tick tries again.
	f := newNextFixture(t)
	cfg := config.Defaults()
	cfg.Account.SmartSwitch = true
	fp := &fakePush{fail: true}
	at := day.Add(9 * time.Hour)
	if err := runMoveNotices(context.Background(), f.dirs, cfg, at, fp.push(false)); err == nil {
		t.Fatal("no error from a failed digest")
	}
	fp.fail = false
	if err := runMoveNotices(context.Background(), f.dirs, cfg, at.Add(time.Minute), fp.push(false)); err != nil || len(fp.sent) != 1 {
		t.Fatalf("retry: %v %v", err, fp.sent)
	}
}

// wy move --digest reads the moves log, the pass records an auto pass leaves and the limit
// stops in the outbox.
func TestMoveDigestCommand(t *testing.T) {
	f, _, run := moveFixture(t, 100, "idle")
	f.hookEvents("5e55a0a0-0000-4000-8000-000000000001", "UserPromptSubmit", "StopFailure:rate_limit")
	f.set("account.smart_switch", "on")
	if out, errOut, code := run("--auto"); code != 0 || !strings.Contains(out, "moved 5e55a0a0 lane-a") {
		t.Fatalf("auto: %d %q %q", code, out, errOut)
	}
	if got := mover.NewState(f.dirs.State.Path).Passes(nextNow.Add(-time.Hour)); len(got) != 1 || len(got[0].Accounts) != 2 {
		t.Fatalf("pass records: %+v", got)
	}
	out, errOut, code := run("--digest")
	if code != 0 || !strings.Contains(out, "Moves: 1") || !strings.Contains(out, "lane-a") || !strings.Contains(out, "Limit hits (sessions a limit stopped): 1") ||
		!strings.Contains(out, "Weekly room at the reset") {
		t.Fatalf("digest: %d %q %q", code, out, errOut)
	}
	js, _, code := run("--digest", "--since", "2d", "--json")
	var d mover.Digest
	if code != 0 || json.Unmarshal([]byte(js), &d) != nil || len(d.Moves) != 1 || len(d.LimitHits) != 1 || d.Moves[0].Name != "lane-a" {
		t.Fatalf("digest json: %d %q", code, js)
	}
	if _, _, code := run("--digest", "--since", "soon"); code == 0 {
		t.Error("a bad --since was accepted")
	}
	if _, _, code := run("--digest", "--plan"); code != 2 {
		t.Errorf("--digest with --plan: exit %d", code)
	}
}

// With a hub the notices go to the hub as switch_notice records (push.md 3.9); direct push
// is the fallback only while the hub is unreachable, and then the record says direct so the
// hub keeps the alert without pushing it again.
func TestMoveNoticesHub(t *testing.T) {
	f := newNextFixture(t)
	st := mover.NewState(f.dirs.State.Path)
	t0 := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	cfg := config.Defaults()
	cfg.Account.MoveDigest = false
	fp := &fakePush{}
	var recs []record.SwitchNoticePayload
	hubFail := false
	out := func(reachable, direct bool) moveOut {
		o := moveOut{Reachable: reachable, Hub: func(p record.SwitchNoticePayload) error {
			if hubFail {
				return errors.New("disk full")
			}
			if err := p.Validate(); err != nil {
				t.Fatal(err)
			}
			recs = append(recs, p)
			return nil
		}}
		if direct {
			o.Direct = fp.direct(false)
		}
		return o
	}
	move := func(at time.Time, sid string) {
		t.Helper()
		if err := st.Append(mover.LogEntry{At: at, SessionID: sid, Name: sid, FromDir: "/h/.claude-a", ToDir: "/h/.claude-b", Auto: true, Reason: "5h over"}); err != nil {
			t.Fatal(err)
		}
	}
	tick := func(at time.Time, o moveOut) error { return runMoveNotices(context.Background(), f.dirs, cfg, at, o) }
	_ = tick(t0, out(true, true))
	// Reachable hub: a record, no direct push.
	move(t0.Add(time.Minute), "s1")
	if err := tick(t0.Add(2*time.Minute), out(true, true)); err != nil || len(recs) != 1 || len(fp.sent) != 0 {
		t.Fatalf("reachable: %v %+v %v", err, recs, fp.sent)
	}
	if r := recs[0]; r.Trigger != record.SwitchNoticeMoved || r.Moves != 1 || r.Direct ||
		r.Episode != "m:"+strconv.FormatInt(t0.Add(time.Minute).UnixMilli(), 10) || r.Text != "s1 ~/.claude-a -> ~/.claude-b: 5h over" {
		t.Fatalf("record %+v", r)
	}
	// Unreachable hub: direct push, and the record marked direct.
	move(t0.Add(10*time.Minute), "s2")
	if err := tick(t0.Add(11*time.Minute), out(false, true)); err != nil || len(recs) != 2 || len(fp.sent) != 1 || !recs[1].Direct {
		t.Fatalf("unreachable: %v %+v %v", err, recs, fp.sent)
	}
	// Unreachable and direct push failing: the record still goes (not direct), for the hub to
	// push when the machine reaches it again.
	fp.fail = true
	move(t0.Add(20*time.Minute), "s3")
	if err := tick(t0.Add(21*time.Minute), out(false, true)); err != nil || len(recs) != 3 || recs[2].Direct {
		t.Fatalf("direct failed: %v %+v", err, recs)
	}
	// Hub only (direct push off), unreachable: the record waits in the outbox.
	move(t0.Add(30*time.Minute), "s4")
	if err := tick(t0.Add(31*time.Minute), out(false, false)); err != nil || len(recs) != 4 || recs[3].Direct {
		t.Fatalf("hub only: %v %+v", err, recs)
	}
	// The record cannot be written: the move stays pending and is retried.
	hubFail = true
	move(t0.Add(40*time.Minute), "s5")
	if err := tick(t0.Add(41*time.Minute), out(true, false)); err == nil || len(st.LoadNotify().Pending) != 1 {
		t.Fatalf("hub failure: %v %+v", err, st.LoadNotify())
	}
	hubFail = false
	if err := tick(t0.Add(47*time.Minute), out(true, false)); err != nil || len(recs) != 5 || len(st.LoadNotify().Pending) != 0 {
		t.Fatalf("retry: %v %+v", err, recs)
	}
	// The digest goes to the hub too, once a day.
	cfg.Account.MoveDigest, cfg.Account.SmartSwitch = true, true
	day := time.Date(2026, 11, 25, 9, 30, 0, 0, time.Local)
	for i := 0; i < 2; i++ {
		if err := tick(day.Add(time.Duration(i)*time.Minute), out(true, true)); err != nil {
			t.Fatal(err)
		}
	}
	var digests int
	for _, r := range recs {
		if r.Trigger == record.SwitchNoticeDigest {
			digests++
			if r.Episode != "d:2026-11-25" || !strings.HasPrefix(r.Text, "Last 24h: ") {
				t.Fatalf("digest %+v", r)
			}
		}
	}
	if digests != 1 {
		t.Fatalf("digests %d: %+v", digests, recs)
	}
}

// moveOutFor writes a real switch_notice record into the outbox when a hub is set.
func TestMoveOutForHubRecord(t *testing.T) {
	f := newNextFixture(t)
	cfg := config.Defaults()
	cfg.Hub.URL = "https://hub.example"
	now := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	o := moveOutFor(f.dirs, cfg, now)
	if o.Hub == nil || o.Reachable || o.Direct != nil {
		t.Fatalf("out %+v", o)
	}
	p := record.SwitchNoticePayload{Trigger: record.SwitchNoticeMoved, Episode: "m:1", SinceAt: now.Format(time.RFC3339), Moves: 1, Text: "x"}
	if err := o.deliver(context.Background(), p, "t", "p"); err != nil {
		t.Fatal(err)
	}
	// The outbox names its day file by the wall clock, not by now.
	files, _ := filepath.Glob(filepath.Join(f.dirs.State.Path, "outbox", "20*.jsonl"))
	var b []byte
	var err error
	if len(files) != 1 {
		err = fmt.Errorf("day files %v", files)
	} else {
		b, err = os.ReadFile(files[0])
	}
	if err != nil || strings.Count(string(b), `"kind":"switch_notice"`) != 1 {
		t.Fatalf("outbox: %v %s", err, b)
	}
	if moveOutFor(f.dirs, config.Defaults(), now).ready() {
		t.Fatal("ready without a hub or direct push")
	}
}

func outboxMoves(t *testing.T, dirs paths.Dirs) []record.SwitchMovePayload {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dirs.State.Path, "outbox", "*.jsonl"))
	var out []record.SwitchMovePayload
	for _, name := range files {
		b, _ := os.ReadFile(name)
		for _, line := range strings.Split(string(b), "\n") {
			var r struct {
				Kind    string                   `json:"kind"`
				Payload record.SwitchMovePayload `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &r) == nil && r.Kind == record.KindSwitchMove {
				out = append(out, r.Payload)
			}
		}
	}
	return out
}

// The first tick after the update sends the last 14 days of moves.jsonl, old-format lines
// included; a mark keeps the next tick from sending them again.
func TestBackfillSwitchMoves(t *testing.T) {
	f := newNextFixture(t)
	cfg := config.Defaults()
	cfg.Hub.URL = "https://hub.example"
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	st := mover.NewState(f.dirs.State.Path)
	add := func(e mover.LogEntry) {
		t.Helper()
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	add(mover.LogEntry{At: now.Add(-20 * 24 * time.Hour), SessionID: "s-old", From: "acct:a/o1", To: "acct:b/o1", Kind: mover.KindBackground}) // too old
	// An old-format line: no name, no folders, no reason.
	add(mover.LogEntry{At: now.Add(-5 * 24 * time.Hour), SessionID: "s1", From: "acct:a/o1", To: "acct:b/o2", Kind: mover.KindTmux, Auto: true})
	add(mover.LogEntry{At: now.Add(-2 * time.Hour), SessionID: "s1", Name: "api", From: "k2", To: "k3", FromDir: "/h/.claude-2", ToDir: "/h/.claude-3",
		Kind: mover.KindBackground, Auto: true, Reason: "5h 97% >= 95", Error: "claude not found"})
	add(mover.LogEntry{At: now.Add(-time.Hour), SessionID: "", From: "x", To: "y", Kind: mover.KindBackground}) // no session: skipped
	if _, ok := st.HubSentCursor(); ok {
		t.Fatal("a mark before the first run")
	}
	if !backfillSwitchMoves(f.dirs, cfg, now) {
		t.Fatal("nothing queued")
	}
	got := outboxMoves(t, f.dirs)
	if len(got) != 2 || got[0].SessionID != "s1" || !got[0].Interactive || !got[0].CrossOrg || got[0].P5 != nil || got[0].Reason != "" || got[0].Window != "" {
		t.Fatalf("old line %+v", got)
	}
	if got[1].Error != "claude not found" || got[1].SessionName != "api" || got[1].Reason == "" || got[1].From != "~/.claude-2" {
		t.Fatalf("new line %+v", got[1])
	}
	cur, ok := st.HubSentCursor()
	if !ok || !cur.Equal(now.Add(-time.Hour)) {
		t.Fatalf("mark %v %v", cur, ok)
	}
	// Idempotent: a second tick sends nothing; a new move after the mark is the only one sent.
	if backfillSwitchMoves(f.dirs, cfg, now.Add(time.Minute)) || len(outboxMoves(t, f.dirs)) != 2 {
		t.Fatal("sent again")
	}
	add(mover.LogEntry{At: now.Add(2 * time.Minute), SessionID: "s2", From: "k1", To: "k2", Kind: mover.KindHeadless})
	if !backfillSwitchMoves(f.dirs, cfg, now.Add(3*time.Minute)) || len(outboxMoves(t, f.dirs)) != 3 {
		t.Fatalf("new move: %d", len(outboxMoves(t, f.dirs)))
	}
	// No hub: nothing happens and no mark is made.
	f2 := newNextFixture(t)
	if backfillSwitchMoves(f2.dirs, config.Defaults(), now) {
		t.Fatal("queued without a hub")
	}
}
