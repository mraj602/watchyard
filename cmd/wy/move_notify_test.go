package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/mover"
)

type fakePush struct {
	sent [][2]string
	fail bool
}

func (f *fakePush) push(private bool) *movePush {
	return &movePush{Private: private, Send: func(_ context.Context, title, text string) error {
		if f.fail {
			return errors.New("down")
		}
		f.sent = append(f.sent, [2]string{title, text})
		return nil
	}}
}

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
	tick := func(at time.Time, c config.Config, p *movePush) {
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
			p = nil
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
				p = nil
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
