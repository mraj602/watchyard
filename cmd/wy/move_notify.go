package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/directpush"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/status"
	"github.com/mraj602/watchyard/internal/version"
)

// movePush sends one push to the owner's phone through direct push. Title and text are what
// the owner reads; in private mode the caller sends only a generic line.
type movePush struct {
	Send    func(ctx context.Context, title, text string) error
	Private bool
}

// moveOut is where the move notices and the digest go (setup.md 8.6, Notices and digest):
// to the hub as a switch_notice record, which the hub turns into a session_moved or
// switch_digest alert for every enabled target (push.md 3.9); and through direct push only
// when the hub is not reachable, as for session alerts (machines.md 5.4, condition 4).
type moveOut struct {
	// Hub appends a switch_notice record to the outbox; nil when no hub is set.
	Hub func(p record.SwitchNoticePayload) error
	// Reachable: the hub answered within direct push's after (default 2 min).
	Reachable bool
	// Direct is direct push; nil when it is off or has no topic.
	Direct *movePush
}

func (o moveOut) ready() bool { return o.Hub != nil || o.Direct != nil }

// deliver sends one notice: direct push first when the hub is unset or unreachable, then the
// record to the hub (direct set when the push went out, so the hub does not push it again).
// A record queued in the outbox counts as delivered: the flusher sends it when the hub is back.
func (o moveOut) deliver(ctx context.Context, p record.SwitchNoticePayload, title, private string) error {
	if o.Direct != nil && (o.Hub == nil || !o.Reachable) {
		text := p.Text
		if o.Direct.Private {
			text = private
		}
		err := o.Direct.Send(ctx, title, text)
		if o.Hub == nil {
			return err
		}
		p.Direct = err == nil
	}
	if o.Hub == nil {
		return fmt.Errorf("no hub and no direct push")
	}
	return o.Hub(p)
}

// moveOutFor builds the machine's notice paths from the config.
func moveOutFor(d paths.Dirs, cfg config.Config, now time.Time) moveOut {
	var o moveOut
	opts, err := directEnv{dirs: d, cfg: cfg}.options()
	if err != nil {
		return o
	}
	o.Direct = directMovePush(opts)
	if cfg.Hub.URL != "" {
		o.Reachable = now.Sub(directpush.LastContact(opts.OutboxDir)) <= opts.After()
		o.Hub = func(p record.SwitchNoticePayload) error {
			r, err := record.NewSwitchNotice(record.Meta{Machine: opts.MachineID, Client: "wy/" + version.Version, Now: now}, p)
			if err != nil {
				return err
			}
			ob := outbox.New(opts.OutboxDir)
			ob.NoFsync = !cfg.Outbox.Fsync
			_, err = ob.Append(r)
			return err
		}
	}
	return o
}

// queueSwitchMove writes one switch_move record (record.md 4.13) to the outbox for the hub's
// Switches log; the tick flushes it. Nothing happens without a hub. A failure is not the
// move's: the log on this machine (moves.jsonl) has it either way.
func queueSwitchMove(d paths.Dirs, cfg config.Config, now time.Time, p record.SwitchMovePayload) bool {
	if cfg.Hub.URL == "" {
		return false
	}
	opts, err := directEnv{dirs: d, cfg: cfg}.options()
	if err != nil {
		return false
	}
	r, err := record.NewSwitchMove(record.Meta{Machine: opts.MachineID, Client: "wy/" + version.Version, Now: now}, p)
	if err != nil {
		return false
	}
	ob := outbox.New(opts.OutboxDir)
	ob.NoFsync = !cfg.Outbox.Fsync
	_, err = ob.Append(r)
	return err == nil
}

// switchBackfillDays is how far back the first tick after the update sends moves.
const switchBackfillDays = 14

// entryPayload is a moves.jsonl line as a switch_move payload. An older line lacks the
// projections, the window and the reason: they stay empty. ok is false for a line the hub
// would refuse (no session, a kind that cannot move).
func entryPayload(cfg config.Config, e mover.LogEntry) (record.SwitchMovePayload, bool) {
	p := record.SwitchMovePayload{At: e.At.UTC().Format(time.RFC3339), SessionID: e.SessionID, SessionName: cutBytes(e.Name, record.SwitchMoveMaxLabel),
		Kind: string(e.Kind), Interactive: e.Kind == mover.KindTmux || e.Kind == mover.KindWrapped || e.Kind == mover.KindTerminal,
		From: slotName(cfg, e.FromDir, e.From), To: slotName(cfg, e.ToDir, e.To), Auto: e.Auto,
		Reason: cutBytes(e.Reason, record.SwitchMoveMaxText), Error: cutBytes(e.Error, record.SwitchMoveMaxError)}
	if p.From == "" {
		p.From = "?"
	}
	if p.To == "" {
		p.To = "?"
	}
	p.CrossOrg = e.From != "" && e.To != "" && mover.Org(e.From) != mover.Org(e.To)
	return p, p.Validate() == nil
}

// backfillSwitchMoves sends the moves of the last 14 days in moves.jsonl that the hub has not
// been sent (setup.md 8.6, Switch log): everything after the high-water mark in
// <state>/move/hub_sent.json, which does not exist before the first run. The hub keeps one row
// per machine, session and time, so a move sent live and again here, or a resend after a crash,
// never doubles. queued reports records written (the tick flushes again).
func backfillSwitchMoves(d paths.Dirs, cfg config.Config, now time.Time) (queued bool) {
	if cfg.Hub.URL == "" {
		return false
	}
	st := mover.NewState(d.State.Path)
	from := now.Add(-switchBackfillDays * 24 * time.Hour)
	if cur, ok := st.HubSentCursor(); ok && cur.After(from) {
		from = cur.Add(time.Nanosecond)
	}
	var last time.Time
	for _, e := range st.LogAll(from) {
		if p, ok := entryPayload(cfg, e); ok {
			if !queueSwitchMove(d, cfg, e.At, p) {
				break // the hub path is not usable now: try again next tick
			}
			queued = true
		}
		last = e.At
	}
	if last.IsZero() {
		last = now // nothing to send: the mark still says the back-fill ran
	}
	_ = st.SaveHubSentCursor(last)
	return queued
}

// directMovePush is the machine's direct push path (push.md 7, machines.md 5): the ntfy topic
// of `wy direct-push on`. nil when direct push is off or has no topic.
func directMovePush(o directpush.Options) *movePush {
	sec, ok := directpush.Ready(o)
	if !ok {
		return nil
	}
	server := o.Config.Push.Direct.Server
	if server == "" {
		server = config.DefaultDirectServer
	}
	if directpush.CheckServer(server) != nil {
		return nil
	}
	return &movePush{Private: o.Config.Push.Direct.Private, Send: func(ctx context.Context, title, text string) error {
		r := directpush.Send(ctx, directpush.HTTPClient(), server, sec.Token,
			directpush.Message{Topic: sec.Topic, Title: title, Message: text, Priority: 3, Tags: []string{"twisted_rightwards_arrows"}})
		if !r.OK {
			return fmt.Errorf("ntfy: %s", r.Error)
		}
		return nil
	}}
}

// cutText keeps a notice text inside record.SwitchNoticeMaxText bytes, on a rune boundary.
func cutText(s string) string {
	if len(s) <= record.SwitchNoticeMaxText {
		return s
	}
	return strings.ToValidUTF8(s[:record.SwitchNoticeMaxText-3], "") + "…"
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

// moveNotices is `wy tick`'s part of the move notices and the daily digest: send the automatic
// moves that wait (setup.md 8.6, Notices and digest), then the digest when it is due. queued
// reports a record written to the outbox (the tick flushes again so it reaches the hub now).
func moveNotices(ctx context.Context, d paths.Dirs, cfg config.Config, now time.Time) (queued bool, err error) {
	o := moveOutFor(d, cfg, now)
	if h := o.Hub; h != nil {
		o.Hub = func(p record.SwitchNoticePayload) error {
			err := h(p)
			queued = queued || err == nil
			return err
		}
	}
	err = runMoveNotices(ctx, d, cfg, now, o)
	return queued, err
}

func runMoveNotices(ctx context.Context, d paths.Dirs, cfg config.Config, now time.Time, o moveOut) error {
	st := mover.NewState(d.State.Path)
	name := func(dir, key string) string { return slotName(cfg, dir, key) }
	var send func(string, []mover.Notice) error
	if o.ready() {
		send = func(text string, ns []mover.Notice) error {
			title := "Watchyard moved a session"
			if len(ns) > 1 {
				title = "Watchyard moved sessions"
			}
			first := ns[0].At
			p := record.SwitchNoticePayload{Trigger: record.SwitchNoticeMoved, Episode: "m:" + strconv.FormatInt(first.UnixMilli(), 10),
				SinceAt: first.UTC().Format(time.RFC3339), Moves: len(ns), Text: cutText(text)}
			return o.deliver(ctx, p, title, fmt.Sprintf("%d session(s) moved to another account", len(ns)))
		}
	}
	_, err := st.Notify(now, cfg.Account.MoveNotify, name, send)
	if derr := moveDigestPush(ctx, d, cfg, st, now, o); err == nil {
		err = derr
	}
	return err
}

// moveDigestPush sends the daily digest once, from 09:00, when account.move_digest is on and
// the switch is on or moved something in the last 24 hours.
func moveDigestPush(ctx context.Context, d paths.Dirs, cfg config.Config, st mover.State, now time.Time, o moveOut) error {
	if !cfg.Account.MoveDigest || !o.ready() || !st.DigestDue(now) {
		return nil
	}
	if !cfg.Account.SmartSwitch && len(st.Log(now.Add(-24*time.Hour))) == 0 {
		return nil
	}
	dg := st.BuildDigest(now.Add(-24*time.Hour), now, func(dir, key string) string { return slotName(cfg, dir, key) }, limitHits(d, now.Add(-24*time.Hour)))
	p := record.SwitchNoticePayload{Trigger: record.SwitchNoticeDigest, Episode: "d:" + now.Local().Format("2006-01-02"),
		SinceAt: now.UTC().Format(time.RFC3339), Moves: len(dg.Moves), Text: cutText(dg.Summary())}
	if err := o.deliver(ctx, p, "Watchyard daily digest", "The daily switch digest is ready: wy move --digest"); err != nil {
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
		pa := mover.PassAccount{Key: a.Key, Label: a.Label, Protected: a.Protected, Level5: a.Level5, Level7: a.Level7,
			P5: a.P5, P7: a.P7, Cap5: a.Cap5, Cap7: a.Cap7, R5: a.R5, Reset7S: int64(a.Reset7.Seconds()), Window: a.Window, Note: a.RateNote}
		if la, ok := w.live[a.Key]; ok {
			pa.Read7, pa.Meter7 = la.WeeklyRead, la.WeeklyMeter
			pa.Read5, pa.Meter5, pa.Leases5 = la.FiveRead, la.FiveMeter, la.FiveLeases
			if !la.ReadingAt.IsZero() {
				pa.Read7AgeS = int64(now.Sub(la.ReadingAt).Seconds())
				pa.Read5AgeS = pa.Read7AgeS
			}
		}
		rec.Accounts = append(rec.Accounts, pa)
	}
	_ = st.AppendPass(rec)
}

// staleAge is a stale reading's age for --plan and the alert ("42 min", "3 h 37 min").
func staleAge(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

// staleAlert tells the owner once when accounts have had stale data (no moves off or onto
// them) for longer than Params.StaleAlert (setup.md 8.6, Stale data): one line in the pass's
// log and one push (as the digest: the hub's switch_digest alert, or direct push when the hub
// is away). A new alert comes only when the set of such accounts changes to a new one.
func (w *moveWorld) staleAlert(e moveEnv, cfg config.Config, st mover.State, r mover.Result) {
	now := e.now()
	var labels, parts []string
	for _, pr := range w.input.Params.StaleAccounts(r) {
		l := w.accLabel(pr.Key)
		labels = append(labels, l)
		parts = append(parts, fmt.Sprintf("%s (data %s old)", l, staleAge(pr.StaleFor)))
	}
	sort.Strings(labels)
	if !st.Changed("stale-alerted", strings.Join(labels, ",")) || len(labels) == 0 {
		return
	}
	text := "Smart Account Switch moves nothing off or onto " + strings.Join(parts, ", ") +
		": their usage readings are old (is the hub reachable?)"
	fmt.Fprintln(e.stdout, "alert: "+text)
	o := moveOutFor(e.dirs, cfg, now)
	if !o.ready() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p := record.SwitchNoticePayload{Trigger: record.SwitchNoticeDigest, Episode: "stale:" + strconv.FormatInt(now.UnixMilli(), 10),
		SinceAt: now.UTC().Format(time.RFC3339), Text: cutText(text)}
	if err := o.deliver(ctx, p, "Smart Account Switch paused on stale data", "Smart Account Switch: an account's data is stale"); err != nil {
		fmt.Fprintln(e.stdout, "stale-data alert not sent: "+err.Error())
	}
}
