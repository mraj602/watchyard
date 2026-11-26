// Package engine is the alert engine (push.md 3-5, 8, 10): it reconciles alerts with
// session, usage and machine state after every ingest batch and at start, runs the one
// scheduler goroutine that fires due alerts, fans them out to push targets through any
// model.Sender (with device prefs, quiet hours, batching and the flood cap), retries,
// handles snooze / seen / action tokens, and publishes `alert` stream events. It owns no
// HTTP: api/alerts.go calls Snooze, Seen and Act; api.Server calls Notify.
//
// Every timer is a column (alert.due_at, delivery.next_at), so a restart resumes where the
// last run stopped (subject to the stale guard). Time comes only from a clock.Clock.
//
// Construction in `wy hub serve`:
//
//	eng := engine.New(engine.Config{Store: st, Clock: clock.Real{}, Senders: []model.Sender{wp, nf},
//		TZ: rules.SystemZone(), PublicURL: publicURL, Log: logger})
//	srv := api.New(api.Config{Store: st, Engine: eng, ...})
//	eng.SetBroker(srv.Broker)          // alert stream events
//	if err := eng.Start(ctx); err != nil { ... }
//	... http.Server.Shutdown(...); eng.Stop()
package engine

import (
	"context"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/push/clock"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// Defaults of push.md 3.3 and 3.6.
const (
	DefaultWorkers     = 4
	DefaultSendTimeout = 10 * time.Second
	// DefaultPassBudget bounds all the sends of one pass; DefaultMaxPushesPerPass bounds how
	// many pushes one pass sends (the rest wait for the next pass).
	DefaultPassBudget       = 40 * time.Second
	DefaultMaxPushesPerPass = 200
	DefaultSweepEvery       = 60 * time.Second
	// JumpThreshold: a wall-clock step this much larger than monotonic time is a suspend.
	JumpThreshold = 60 * time.Second

	// Catch-up hold (push.md 3.3): a session alert created already overdue from a
	// machine that reported more records queued is due CatchUpStep later, and is postponed by
	// CatchUpStep at fire time while that machine's latest batch (at most CatchUpFresh old)
	// still reported more, for at most CatchUpMax after its creation.
	CatchUpStep  = 10 * time.Second
	CatchUpFresh = 10 * time.Second
	CatchUpMax   = 2 * time.Minute

	// FrozenPoll is how often the scheduler looks for an unfreeze while the hub is frozen
	// (push.md 3.6: it skips the sweep, fires and sends, machines.md 8.1).
	FrozenPoll = 10 * time.Second
)

// Config wires an Engine.
type Config struct {
	Store *store.Store
	// Clock is the time source; nil = clock.Real{}.
	Clock clock.Clock
	// Senders deliver pushes, one per kind (model.KindWebPush, model.KindNtfy). A target
	// whose kind has no sender gets a failed delivery ("no sender").
	Senders []model.Sender
	// Broker receives `alert` events; may also be set later with SetBroker. Nil: none.
	Broker *events.Broker
	// Log gets one line per fire, failed send and skip (push.md 10). Nil discards.
	Log *log.Logger
	// TZ is the hub time zone when the hub.tz setting is unset (the TZ environment variable
	// of `wy hub serve`); empty = UTC.
	TZ string
	// PublicURL is the hub's public origin (ntfy needs absolute links); empty falls back to
	// the hub.public_url setting.
	PublicURL string
	// Workers is the send pool size (4); SendTimeout the per-send timeout (10 s);
	// SweepEvery the silence sweep interval (60 s).
	Workers     int
	SendTimeout time.Duration
	SweepEvery  time.Duration
	// PassBudget is the most time one pass spends sending (40 s) and MaxPushesPerPass the
	// most pushes it sends (200); what does not fit stays pending for the next pass.
	PassBudget       time.Duration
	MaxPushesPerPass int
	// Link queues dismissals on the link targets that got an alert when it is seen or
	// resolved (push.md 13.3), in the engine's transaction. Nil: none (no link channel).
	Link LinkQueue
}

// LinkQueue is the link's enqueue inside a transaction (hublink.Hub.EnqueueTx).
type LinkQueue interface {
	EnqueueTx(tx *store.Tx, device, t, tag string, ttl time.Duration, plaintext []byte, ref string) (int64, error)
}

// Engine is the alert engine. It implements api.Engine (Notify) and model.AlertService.
type Engine struct {
	cfg     Config
	st      *store.Store
	clk     clock.Clock
	log     *log.Logger
	senders map[string]model.Sender

	keyMu  sync.Mutex
	actKey []byte // cached action-token key

	mu      sync.Mutex
	broker  *events.Broker
	changes ingest.Changes
	// backlog is, per machine, whether its latest batch reported more records queued and
	// when the engine heard it (the catch-up hold). In memory only: after a restart the hold
	// simply does not apply until the next batch.
	backlog map[string]backlogReport
	started bool
	stopped bool

	poke    chan struct{}
	syncReq chan chan struct{}
	stop    chan struct{}
	done    chan struct{}

	// Loop-owned state (only the scheduler goroutine, or Start before it runs).
	awakeMS   atomic.Int64
	lastNow   time.Time
	lastMono  time.Duration
	nextSweep int64
	wakeIn    time.Duration
	frozen    bool   // the last pass found hub.frozen set
	rulesSig  string // the rules (ids, on/off, scope) the last pass checked pending alerts against
}

// backlogReport is what a machine's latest batch said about its queue.
type backlogReport struct {
	more bool
	at   time.Time
}

// catchingUp reports whether machine's latest batch, at most CatchUpFresh before now,
// reported more records queued behind it (push.md 3.3 catch-up hold).
func (e *Engine) catchingUp(machine string, now time.Time) bool {
	e.mu.Lock()
	r, ok := e.backlog[machine]
	e.mu.Unlock()
	return ok && r.more && !now.Before(r.at) && now.Sub(r.at) <= CatchUpFresh
}

var _ model.AlertService = (*Engine)(nil)

// New builds an engine; nothing runs until Start.
func New(cfg Config) *Engine {
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkers
	}
	if cfg.PassBudget <= 0 {
		cfg.PassBudget = DefaultPassBudget
	}
	if cfg.MaxPushesPerPass <= 0 {
		cfg.MaxPushesPerPass = DefaultMaxPushesPerPass
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = DefaultSendTimeout
	}
	if cfg.SweepEvery <= 0 {
		cfg.SweepEvery = DefaultSweepEvery
	}
	lg := cfg.Log
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	e := &Engine{
		cfg: cfg, st: cfg.Store, clk: cfg.Clock, log: lg, senders: map[string]model.Sender{},
		broker: cfg.Broker, poke: make(chan struct{}, 1), syncReq: make(chan chan struct{}), backlog: map[string]backlogReport{},
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	for _, s := range cfg.Senders {
		if s != nil {
			e.senders[s.Kind()] = s
		}
	}
	e.changes = emptyChanges()
	return e
}

func emptyChanges() ingest.Changes {
	return ingest.Changes{Sessions: map[string]ingest.SessionChange{}, Seats: map[string]bool{}, Machines: map[string]bool{}}
}

// SetBroker sets where `alert` stream events go (the api.Server's broker).
func (e *Engine) SetBroker(b *events.Broker) {
	e.mu.Lock()
	e.broker = b
	e.mu.Unlock()
}

// Notify queues a committed ingest batch's changes for reconcile (push.md 3.5). It never
// blocks: changes merge while the engine is busy. ch is not modified.
func (e *Engine) Notify(ch ingest.Changes) {
	e.mu.Lock()
	for id, c := range ch.Sessions {
		e.changes.Sessions[id] = c
	}
	for id := range ch.Seats {
		e.changes.Seats[id] = true
	}
	for id := range ch.Machines {
		e.changes.Machines[id] = true
	}
	e.changes.Notices = append(e.changes.Notices, ch.Notices...)
	if len(ch.More) > 0 {
		now := e.clk.Now()
		for id, more := range ch.More {
			e.backlog[id] = backlogReport{more: more, at: now}
		}
	}
	e.mu.Unlock()
	e.Poke()
}

// Poke wakes the scheduler (new changes, a rule or settings change, an API action).
func (e *Engine) Poke() {
	select {
	case e.poke <- struct{}{}:
	default:
	}
}

func (e *Engine) takeChanges() ingest.Changes {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := e.changes
	e.changes = emptyChanges()
	return ch
}

func (e *Engine) requeue(ch ingest.Changes) {
	if !ch.Empty() {
		e.Notify(ch)
	}
}

// Start runs the start-up reconcile (with the first-start baseline, push.md 3.8), sets
// awake_at, and starts the scheduler goroutine. Call it once.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return nil
	}
	e.started = true
	e.mu.Unlock()
	now := e.clk.Now()
	e.markAwake(now)
	e.nextSweep = now.UnixMilli() // first pass sweeps
	if err := e.startup(ctx, now); err != nil {
		// No loop is running: let Stop return instead of waiting for it.
		e.mu.Lock()
		e.started = false
		e.mu.Unlock()
		return err
	}
	go e.loop()
	return nil
}

// Stop ends the scheduler goroutine and waits for it (call after the HTTP drain).
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.started || e.stopped {
		e.stopped = true
		e.mu.Unlock()
		return
	}
	e.stopped = true
	e.mu.Unlock()
	close(e.stop)
	<-e.done
}

// Sync runs one scheduler pass now and returns when it is done (tests, and the end-to-end
// harness after it moves a fake clock). Without a running scheduler it runs the pass on
// the caller's goroutine.
func (e *Engine) Sync() {
	e.mu.Lock()
	running := e.started && !e.stopped
	e.mu.Unlock()
	if !running {
		e.pass()
		return
	}
	reply := make(chan struct{})
	select {
	case e.syncReq <- reply:
		<-reply
	case <-e.done:
	}
}

func (e *Engine) loop() {
	defer close(e.done)
	e.pass()
	timer := e.clk.NewTimer(e.wakeIn)
	defer timer.Stop()
	for {
		var reply chan struct{}
		select {
		case <-e.stop:
			return
		case <-timer.C():
		case <-e.poke:
		case reply = <-e.syncReq:
		}
		e.pass()
		// Drop a wake-up that is already covered by this pass, then re-arm.
		select {
		case <-timer.C():
		default:
		}
		timer.Stop()
		timer.Reset(e.wakeIn)
		if reply != nil {
			// A Sync caller expects the scheduler idle when it returns: a poke that came in
			// before this pass started is covered by it (queued changes are not: keep it).
			e.mu.Lock()
			idle := e.changes.Empty()
			e.mu.Unlock()
			if idle {
				select {
				case <-e.poke:
				default:
				}
			}
			close(reply)
		}
	}
}

type monoClock interface{ Mono() time.Duration }

func (e *Engine) markAwake(now time.Time) {
	e.awakeMS.Store(now.UnixMilli())
	e.lastNow = now
	if m, ok := e.clk.(monoClock); ok {
		e.lastMono = m.Mono()
	}
}

// checkJump compares wall-clock and monotonic time elapsed since the last wake-up; a
// difference over JumpThreshold (a suspended laptop, a clock step) resets awake_at
// (push.md 3.3).
func (e *Engine) checkJump(now time.Time) {
	wall := now.Round(0).Sub(e.lastNow.Round(0))
	var mono time.Duration
	if m, ok := e.clk.(monoClock); ok {
		cur := m.Mono()
		mono = cur - e.lastMono
		e.lastMono = cur
	} else {
		mono = now.Sub(e.lastNow) // monotonic when both readings carry one
	}
	e.lastNow = now
	if d := wall - mono; d > JumpThreshold || d < -JumpThreshold {
		e.awakeMS.Store(now.UnixMilli())
		e.log.Printf("alert clock jump %s: awake_at reset", d.Round(time.Second))
	}
}

// AwakeAt is the engine's awake_at (push.md 3.3).
func (e *Engine) AwakeAt() time.Time { return time.UnixMilli(e.awakeMS.Load()).UTC() }

func (e *Engine) publish(evs []events.Event) {
	e.mu.Lock()
	b := e.broker
	e.mu.Unlock()
	if b == nil {
		return
	}
	for _, ev := range evs {
		b.Publish(ev)
	}
}
