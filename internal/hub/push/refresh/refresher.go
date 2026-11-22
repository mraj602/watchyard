// Package refresh sends widget refresh frames to the Android app over its link (push.md
// 13.5): on session and usage changes, debounced, only when what a widget shows changed, and
// no more often than push.settings.refresh_min per target. It reaches the rest of the hub
// only through hub/store, hub/events and hub/push/{model,clock}.
package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/push/clock"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Refresh timing of push.md 13.5.
const (
	RefreshDebounce = 10 * time.Second
	refreshSendTime = 10 * time.Second
	refreshPassTime = time.Minute
	pctBucket       = 5
	frozenRecheck   = 10 * time.Second // how soon a pass skipped for a frozen hub looks again
)

// RefresherOptions configure a Refresher. Store, Sender and Broker are required.
type RefresherOptions struct {
	Store *store.Store
	// Sender queues the `refresh` frames for link targets (the link Sender; a fake in tests).
	Sender model.Sender
	// Broker is the hub's event broker; session and usage events start a pass.
	Broker *events.Broker
	// Clock is the time source; nil is clock.Real{}.
	Clock clock.Clock
	// Debounce is the wait after a change before a pass; 0 is RefreshDebounce.
	Debounce time.Duration
	// Log gets one line per pass that had targets (counts only, never the digest). Nil
	// discards.
	Log *log.Logger
}

// Refresher sends widget refresh frames to link targets whose devices asked for them
// (push.md 13.5). Digests live in memory: a restart sends at most one refresh per target.
type Refresher struct {
	o RefresherOptions

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
	syncReq chan chan struct{}

	// Loop-owned state.
	last       map[string]refreshed // target id -> last refresh
	debounceAt time.Time            // zero: no pass pending for a change
	wakeAt     time.Time            // zero: no timed pass (next reset or a deferred refresh)
	sub        <-chan events.Event
	unsub      func()
}

type refreshed struct {
	digest string // "" after a failed try: the next pass tries again
	at     time.Time
}

// NewRefresher builds a Refresher.
func NewRefresher(o RefresherOptions) *Refresher {
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Debounce <= 0 {
		o.Debounce = RefreshDebounce
	}
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	return &Refresher{o: o, last: map[string]refreshed{}, stop: make(chan struct{}), done: make(chan struct{}),
		syncReq: make(chan chan struct{})}
}

// Start subscribes to the broker and runs the loop until Stop. It sends nothing until the
// first change (or a reset passing).
func (r *Refresher) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return
	}
	r.started = true
	r.subscribe()
	go r.loop()
}

// Stop ends the loop, waiting for a pass in progress.
func (r *Refresher) Stop() {
	r.mu.Lock()
	started := r.started
	r.started = false
	r.mu.Unlock()
	if !started {
		return
	}
	close(r.stop)
	<-r.done
}

// Sync makes the loop take every event already published and every timer already due, run
// a pass if one is due, and returns when that is done (tests, the fakeclock build).
func (r *Refresher) Sync() {
	reply := make(chan struct{})
	select {
	case r.syncReq <- reply:
		<-reply
	case <-r.done:
	}
}

func (r *Refresher) subscribe() {
	ch, _, cancel := r.o.Broker.Subscribe(0)
	r.sub, r.unsub = ch, cancel
}

func (r *Refresher) loop() {
	defer close(r.done)
	defer func() { r.unsub() }()
	t := r.o.Clock.NewTimer(time.Hour)
	t.Stop()
	arm := func() {
		t.Stop()
		select { // drop a stale fire
		case <-t.C():
		default:
		}
		next := r.next()
		if next.IsZero() {
			return
		}
		t.Reset(max(next.Sub(r.o.Clock.Now()), 0))
	}
	for {
		select {
		case <-r.stop:
			return
		case ev, ok := <-r.sub:
			r.event(ev, ok)
		case <-t.C():
			r.maybePass()
		case reply := <-r.syncReq:
			r.drain(t)
			r.maybePass()
			arm()
			close(reply)
			continue
		}
		arm()
	}
}

// drain takes pending events and a due timer without blocking.
func (r *Refresher) drain(t clock.Timer) {
	for {
		select {
		case ev, ok := <-r.sub:
			r.event(ev, ok)
			continue
		case <-t.C():
		default:
		}
		return
	}
}

// event handles one broker event. A closed channel means the broker dropped a slow
// subscriber: subscribe again and treat it as a change.
func (r *Refresher) event(ev events.Event, ok bool) {
	if !ok {
		r.subscribe()
		r.changed()
		return
	}
	switch ev.Type {
	case "session", "usage":
		r.changed()
	case "alert":
		// Seen lowers the needs-you count (push.md 4).
		if strings.Contains(string(ev.Data), `"change":"seen"`) {
			r.changed()
		}
	}
}

func (r *Refresher) changed() {
	if r.debounceAt.IsZero() {
		r.debounceAt = r.o.Clock.Now().Add(r.o.Debounce)
	}
}

func (r *Refresher) next() time.Time {
	switch {
	case r.debounceAt.IsZero():
		return r.wakeAt
	case r.wakeAt.IsZero() || r.debounceAt.Before(r.wakeAt):
		return r.debounceAt
	}
	return r.wakeAt
}

func (r *Refresher) maybePass() {
	now := r.o.Clock.Now()
	next := r.next()
	if next.IsZero() || now.Before(next) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshPassTime)
	defer cancel()
	if err := r.pass(ctx, now); err != nil {
		r.o.Log.Printf("widget refresh: %v", err)
		r.wakeAt = now.Add(time.Minute) // try again later
	}
}

// pass is one refresh pass at now (push.md 13.5).
func (r *Refresher) pass(ctx context.Context, now time.Time) error {
	r.debounceAt, r.wakeAt = time.Time{}, time.Time{}
	nowMS := now.UnixMilli()
	// A frozen hub (a move in progress, machines.md 8) sends nothing, as the alert engine
	// does; look again soon so the pass runs once it is unfrozen.
	if frozen, err := r.o.Store.HubIsFrozen(ctx); err != nil {
		return err
	} else if frozen {
		r.wakeAt = now.Add(frozenRecheck)
		return nil
	}
	minGap := time.Duration(r.refreshMin(ctx)) * time.Minute
	targets, err := r.o.Store.LinkRefreshTargets(ctx, nowMS)
	if err != nil {
		return err
	}
	state, err := r.o.Store.WidgetState(ctx, nowMS)
	if err != nil {
		return err
	}
	snapshot := Snapshot(state, nowMS)
	digest, nextReset := Digest(state)
	wake := time.Time{}
	later := func(t time.Time) {
		if wake.IsZero() || t.Before(wake) {
			wake = t
		}
	}
	if nextReset > 0 {
		later(time.UnixMilli(nextReset))
	}
	keep := map[string]bool{}
	sent, deferred, failedN := 0, 0, 0
	for _, t := range targets {
		keep[t.ID] = true
		l, seen := r.last[t.ID]
		if seen && l.digest == digest {
			continue
		}
		if seen && now.Sub(l.at) < minGap {
			deferred++
			later(l.at.Add(minGap))
			continue
		}
		msg := model.Message{Payload: model.Payload{V: model.PayloadVersion, Type: model.TypeRefresh,
			NeedsYou: state.NeedsYou, TS: model.Time(nowMS)}, Urgency: "normal", Data: snapshot}
		sctx, cancel := context.WithTimeout(ctx, refreshSendTime)
		res := r.o.Sender.Send(sctx, t, msg)
		cancel()
		if err := r.o.Store.ApplyTargetResult(context.WithoutCancel(ctx), t.ID, res); err != nil {
			r.o.Log.Printf("widget refresh: record result: %v", err)
		}
		switch res.Outcome {
		case model.OutcomeSent:
			sent++
			r.last[t.ID] = refreshed{digest: digest, at: now}
		case model.OutcomeGone:
			failedN++
			delete(r.last, t.ID)
			keep[t.ID] = false
		default:
			failedN++
			r.last[t.ID] = refreshed{digest: l.digest, at: now}
			later(now.Add(minGap))
		}
	}
	for id := range r.last {
		if !keep[id] {
			delete(r.last, id)
		}
	}
	r.wakeAt = wake
	if len(targets) > 0 {
		r.o.Log.Printf("widget refresh: %d targets, %d sent, %d deferred, %d failed", len(targets), sent, deferred, failedN)
	}
	return nil
}

// refreshMin is push.settings.refresh_min (the default when unset or invalid).
func (r *Refresher) refreshMin(ctx context.Context) int {
	var s model.Settings
	s.Defaults()
	if raw, err := r.o.Store.PushSetting(ctx, store.AlertKeyPushSettings); err == nil {
		if p, err := model.ParseSettings(raw, "UTC"); err == nil {
			s = p
		}
	}
	return s.RefreshMin
}

// Snapshot is a link `refresh` body (link.md 6.4): {"v":1,"needs_you","seats":[{"seat",
// "window","used_pct","resets_at"}],"ts"}, enough to redraw the widgets offline.
func Snapshot(s store.WidgetState, nowMS int64) []byte {
	type seat struct {
		Seat     string     `json:"seat"`
		Window   string     `json:"window"`
		UsedPct  float64    `json:"used_pct"`
		ResetsAt model.Time `json:"resets_at"`
	}
	seats := make([]seat, 0, len(s.Windows))
	for _, w := range s.Windows {
		seats = append(seats, seat{w.Seat, w.Key, w.Pct, model.Time(w.ResetsAt)})
	}
	b, _ := json.Marshal(map[string]any{"v": 1, "needs_you": s.NeedsYou, "seats": seats, "ts": model.Time(nowMS)})
	return b
}

// Digest is what a widget shows, coarsened (push.md 13.5): the needs-you count, each seat ×
// window's used % in 5-point buckets, and the next reset. It also returns that next reset
// (ms, 0 when none), when a refresh is due anyway.
func Digest(s store.WidgetState) (string, int64) {
	var b strings.Builder
	fmt.Fprintf(&b, "n=%d", s.NeedsYou)
	var next int64
	for _, w := range s.Windows {
		fmt.Fprintf(&b, ";%s/%s=%d", w.Seat, w.Key, int(math.Floor(w.Pct/pctBucket)))
		if next == 0 || w.ResetsAt < next {
			next = w.ResetsAt
		}
	}
	fmt.Fprintf(&b, ";next=%d", next)
	return b.String(), next
}
