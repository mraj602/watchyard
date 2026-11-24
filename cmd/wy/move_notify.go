package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/directpush"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/status"
)

// movePush sends one push to the owner's phone. Title and text are what the owner reads; in
// private mode the caller sends only a generic line.
type movePush struct {
	Send    func(ctx context.Context, title, text string) error
	Private bool
}

// directMovePush is the machine's existing push path (push.md 7, machines.md 5): the ntfy
// topic of `wy direct-push on`. The hub only makes alerts from session records, so a move
// notice has no other way out. nil when direct push is off or has no topic.
func directMovePush(d paths.Dirs, cfg config.Config) *movePush {
	o, err := directEnv{dirs: d, cfg: cfg}.options()
	if err != nil {
		return nil
	}
	sec, ok := directpush.Ready(o)
	if !ok {
		return nil
	}
	server := cfg.Push.Direct.Server
	if server == "" {
		server = config.DefaultDirectServer
	}
	if directpush.CheckServer(server) != nil {
		return nil
	}
	return &movePush{Private: cfg.Push.Direct.Private, Send: func(ctx context.Context, title, text string) error {
		r := directpush.Send(ctx, directpush.HTTPClient(), server, sec.Token,
			directpush.Message{Topic: sec.Topic, Title: title, Message: text, Priority: 3, Tags: []string{"twisted_rightwards_arrows"}})
		if !r.OK {
			return fmt.Errorf("ntfy: %s", r.Error)
		}
		return nil
	}}
}

// slotName is the label of a slot folder (slots.labels), else ~/<folder>; key when the move
// has no folder.
func slotName(cfg config.Config, dir, key string) string {
	if dir == "" {
		return key
	}
	if l := cfg.Slots.Labels["~/"+filepath.Base(dir)]; l != "" {
		return l
	}
	return "~/" + filepath.Base(dir)
}

// moveNotices is `wy tick`'s part of the move notices and the daily digest: push the automatic
// moves that wait (setup.md 8.6, Notices and digest), then the digest when it is due.
func moveNotices(ctx context.Context, d paths.Dirs, cfg config.Config, now time.Time) error {
	return runMoveNotices(ctx, d, cfg, now, directMovePush(d, cfg))
}

func runMoveNotices(ctx context.Context, d paths.Dirs, cfg config.Config, now time.Time, p *movePush) error {
	st := mover.NewState(d.State.Path)
	name := func(dir, key string) string { return slotName(cfg, dir, key) }
	var send func(string, int) error
	if p != nil {
		send = func(text string, n int) error {
			title := "Watchyard moved a session"
			if n > 1 {
				title = "Watchyard moved sessions"
			}
			if p.Private {
				text = fmt.Sprintf("%d session(s) moved to another account", n)
			}
			return p.Send(ctx, title, text)
		}
	}
	_, err := st.Notify(now, cfg.Account.MoveNotify, name, send)
	if derr := moveDigestPush(ctx, d, cfg, st, now, p); err == nil {
		err = derr
	}
	return err
}

// moveDigestPush sends the daily digest once, from 09:00, when account.move_digest is on and
// the switch is on or moved something in the last 24 hours.
func moveDigestPush(ctx context.Context, d paths.Dirs, cfg config.Config, st mover.State, now time.Time, p *movePush) error {
	if !cfg.Account.MoveDigest || p == nil || !st.DigestDue(now) {
		return nil
	}
	if !cfg.Account.SmartSwitch && len(st.Log(now.Add(-24*time.Hour))) == 0 {
		return nil
	}
	dg := st.BuildDigest(now.Add(-24*time.Hour), now, func(dir, key string) string { return slotName(cfg, dir, key) }, limitHits(d, now.Add(-24*time.Hour)))
	text := dg.Summary()
	if p.Private {
		text = "The daily switch digest is ready: wy move --digest"
	}
	if err := p.Send(ctx, "Watchyard daily digest", text); err != nil {
		return err
	}
	return st.MarkDigest(now)
}

// limitHits are the sessions a limit stopped since since: the StopFailure hook events for
// rate_limit in the outbox (the same events world() reads for a stopped session).
func limitHits(d paths.Dirs, since time.Time) []mover.LimitHit {
	recs, err := status.Load(filepath.Join(d.State.Path, "outbox"), since)
	if err != nil {
		return nil
	}
	var out []mover.LimitHit
	for _, r := range recs {
		var p struct {
			SessionID string `json:"session_id"`
			Event     string `json:"hook_event_name"`
			Error     string `json:"error"`
		}
		if r.Kind != record.KindHookEvent || json.Unmarshal(r.Payload, &p) != nil || p.Event != "StopFailure" || p.Error != "rate_limit" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, r.TS); err == nil && !t.Before(since) {
			out = append(out, mover.LimitHit{At: t, Session: p.SessionID})
		}
	}
	return out
}

// parseDigestSpan reads --since: a Go duration, or whole days like 7d.
func parseDigestSpan(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		var days int
		if _, err := fmt.Sscanf(n, "%d", &days); err == nil && days > 0 && fmt.Sprint(days) == n {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--since: want a duration like 24h or 7d, got %q", s)
	}
	return d, nil
}

// printDigest is `wy move --digest [--since 24h] [--json]`.
func (e moveEnv) printDigest(since string, asJSON bool) error {
	span, err := parseDigestSpan(since)
	if err != nil {
		return err
	}
	now := e.now()
	cfg := e.config()
	st := mover.NewState(e.dirs.State.Path)
	dg := st.BuildDigest(now.Add(-span), now, func(dir, key string) string { return slotName(cfg, dir, key) }, limitHits(e.dirs, now.Add(-span)))
	if asJSON {
		return writeJSONOut(e.stdout, dg)
	}
	fmt.Fprint(e.stdout, dg.Text())
	return nil
}

// recordPass keeps the pass's projections for the digest (<state>/move/passes.jsonl).
func (w *moveWorld) recordPass(st mover.State, r mover.Result, now time.Time) {
	rec := mover.PassRecord{At: now}
	for _, a := range r.Accounts {
		rec.Accounts = append(rec.Accounts, mover.PassAccount{Key: a.Key, Label: a.Label, Protected: a.Protected, Level5: a.Level5, Level7: a.Level7,
			P5: a.P5, P7: a.P7, Cap5: a.Cap5, Cap7: a.Cap7, R5: a.R5, Reset7S: int64(a.Reset7.Seconds()), Window: a.Window, Note: a.RateNote})
	}
	_ = st.AppendPass(rec)
}
