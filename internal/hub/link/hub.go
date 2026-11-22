// Package hublink is the hub side of the link (docs/spec/link.md): the durable queue
// (Enqueue: collapse, seq, seal, sign, caps; acks, rejects, expiry), the challenge login and
// its resync, the /v1/link WebSocket server and the /v1/link/poll fallback, revocation
// closes and the matrix and revocation broadcasts to machines.
//
// One goroutine reads each connection; queued frames go out from a pump goroutine woken
// after every commit that queued something for the device. Frames are sealed and signed
// once, at enqueue; a replay sends the stored bytes.
//
// Contract: docs/spec/link.md 2-9, push.md 13. Depends on: link, keys, hub/keys,
// hub/store, hub/push/webpush (RFC 8291 for phones).
package hublink

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/push/webpush"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
)

// Limits of link.md 2, 5.5 and 5.6.
const (
	MaxRows         = 500
	MaxBytes        = 4 << 20
	MaxTotal        = 50_000
	InFlight        = 64
	MaxChallenging  = 256             // WebSocket logins in progress, all callers
	MaxLoginsPerIP  = 8               // of those, from one address
	MaxNonces       = 10_000          // outstanding challenges (each lives ChallengeTTL)
	LoginTimeout    = 5 * time.Second // a connection must say hello this soon after the challenge
	ChallengeTTL    = 60 * time.Second
	LoginsPerMinute = 10
	PollsPerMinute  = 120
	PollWait        = 25 * time.Second
	MaxUp           = 64
	SweepEvery      = time.Minute
)

// TTLs of link.md 5.4 (alerts use push.md 6.4's table, given by the sender).
const (
	TTLRefresh    = 900 * time.Second
	TTLPeerStatus = 24 * time.Hour
	TTLDocs       = 30 * 24 * time.Hour
)

// Errors of Enqueue.
var (
	// ErrNoLink: the device is revoked, unknown or has no keys (no-op).
	ErrNoLink = errors.New("link: device has no link")
	// ErrQueueFull: the hub-wide cap is reached (alert, peer_msg), or a peer_msg does not
	// fit the device's queue.
	ErrQueueFull = errors.New("link: queue full")
	// ErrBadFrame: unknown type or a tag outside link.md 5.3's rule.
	ErrBadFrame = errors.New("link: bad frame type or tag")
)

// Settings are the link channel's settings (push.md 13.6).
type Settings struct {
	URLs  []string
	PingS int
}

// ActionFunc applies an action token as POST /v1/actions/{token} does; status is the HTTP
// status that call would answer (200, 400, 401, 404, 410, 503).
type ActionFunc func(ctx context.Context, token string) (alert any, applied bool, status int)

// PeerRelay is the seam for the peer frames a machine sends (peers.md 8): peer_msg,
// peer_result, peer_log, peers_list and peer_tag. It returns the ephemeral reply (type and body) or
// "" for none. Nil: the hub answers notice unknown_type.
type PeerRelay interface {
	Up(ctx context.Context, device string, f link.Up) (replyType string, reply any, err error)
}

// Config wires a Hub.
type Config struct {
	Store *store.Store
	Keys  *hubkeys.Service
	// Now is the clock; nil = time.Now.
	Now func() time.Time
	// Log gets one line per login and close, never a body. Nil discards.
	Log *log.Logger
	// Settings reads the link channel settings; nil = defaults (no URLs, 180 s).
	Settings func(ctx context.Context) Settings
	// Action applies `action` frames; nil answers 503.
	Action ActionFunc
	// Peers relays the machines' peer frames; nil answers unknown_type.
	Peers PeerRelay
	// PeerExpired is called (in the sweeper's transaction) for a peer_msg row that expired
	// unsent (link.md 5.4: the message becomes expired, the sender gets peer_status).
	PeerExpired func(tx *store.Tx, msgID string) error
	// PollWait overrides PollWait (tests); Idle overrides 2 × ping_s + 30 s (tests).
	PollWait time.Duration
	Idle     time.Duration
	// LoginTimeout overrides LoginTimeout (tests).
	LoginTimeout time.Duration
}

// Hub is the hub's link.
type Hub struct {
	cfg Config
	key *hubkeys.HubKey
	log *log.Logger

	challenging atomic.Int32
	loginIP     map[string]int // address -> WebSocket logins in progress
	nonceKey    []byte         // HMAC secret of the stateless challenge nonces, per process

	mu       sync.Mutex
	nonces   map[string]int64 // spent nonces -> exp (unix s); see login.go
	conns    map[string]*conn // device -> its WebSocket
	polls    map[string]chan struct{}
	pollSent map[string]int64 // device -> highest seq a poll reply carried
	signal   map[string]chan struct{}
	logins   limiter
	pollLim  limiter
	actLim   limiter
	rejected map[string]int
	docs     struct{ matrix, revocations int }
	closed   bool
	started  bool

	stop chan struct{}
	done chan struct{}

	afterLogin func(device string) // tests: runs between a login's checks and registering it
}

// New builds a hub link. Start runs its sweeper.
func New(cfg Config) *Hub {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PollWait <= 0 {
		cfg.PollWait = PollWait
	}
	lg := cfg.Log
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	nk := make([]byte, 32)
	if _, err := rand.Read(nk); err != nil {
		panic(err)
	}
	return &Hub{nonceKey: nk, loginIP: map[string]int{}, cfg: cfg, key: cfg.Keys.Key, log: lg, nonces: map[string]int64{}, conns: map[string]*conn{},
		polls: map[string]chan struct{}{}, pollSent: map[string]int64{}, signal: map[string]chan struct{}{}, rejected: map[string]int{},
		stop: make(chan struct{}), done: make(chan struct{})}
}

func (h *Hub) now() time.Time { return h.cfg.Now() }

func (h *Hub) settings(ctx context.Context) Settings {
	s := Settings{PingS: 180}
	if h.cfg.Settings != nil {
		s = h.cfg.Settings(ctx)
	}
	if s.PingS <= 0 {
		s.PingS = 180
	}
	if s.URLs == nil {
		s.URLs = []string{}
	}
	return s
}

// ---- wake-ups ----

// waitChan is closed at the device's next notify.
func (h *Hub) waitChan(device string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.signal[device]
	if !ok {
		ch = make(chan struct{})
		h.signal[device] = ch
	}
	return ch
}

// notify wakes the device's connection or pending poll.
func (h *Hub) notify(device string) {
	h.mu.Lock()
	if ch, ok := h.signal[device]; ok {
		close(ch)
		delete(h.signal, device)
	}
	h.mu.Unlock()
}

// ---- the queue (link.md 5) ----

// Enqueue queues one frame in its own transaction (link.md 5.2) and returns its seq.
func (h *Hub) Enqueue(ctx context.Context, device, t, tag string, ttl time.Duration, plaintext []byte, ref string) (int64, error) {
	var seq int64
	err := h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		seq, err = h.EnqueueTx(tx, device, t, tag, ttl, plaintext, ref)
		return err
	})
	return seq, err
}

// EnqueueTx queues one frame in tx: refuse a device without a link, collapse by tag, take the
// seq, seal to the device's certified box key, sign, insert, apply the caps; the device is
// woken after the commit. On an error nothing of it stays in tx (a savepoint).
func (h *Hub) EnqueueTx(tx *store.Tx, device, t, tag string, ttl time.Duration, plaintext []byte, ref string) (int64, error) {
	if !slices.Contains(link.Queued, t) || !link.ValidTag(tag) {
		return 0, ErrBadFrame
	}
	var seq int64
	err := tx.Savepoint("link_enqueue", func() error {
		var err error
		seq, err = h.enqueue(tx, device, t, tag, ttl, plaintext, ref)
		return err
	})
	if err != nil {
		return 0, err
	}
	tx.AfterCommit(func() { h.notify(device) })
	return seq, nil
}

// TagFor is a notification tag (push.md 6.5) as a link tag: itself when it fits link.md
// 5.3's rule, else its Web Push Topic (22 base64url characters, also a valid tag). The
// alert sender and the dismissals use it, so a dismiss collapses its alert.
func TagFor(tag string) string {
	if link.ValidTag(tag) {
		return tag
	}
	return webpush.Topic(tag)
}

// sealer seals a body to one device's certified keys.
type sealer struct {
	device string
	cert   keys.Cert
	auth   []byte
}

func (s sealer) seal(t string, plaintext []byte) ([]byte, error) {
	if s.cert.Body.Box.Alg == keys.AlgP256 {
		return webpush.Encrypt(rand.Reader, plaintext, s.cert.Body.BoxPub(), s.auth, webpush.PadMultiple)
	}
	return link.SealMachine(s.cert.Body.BoxPub(), s.device, t, plaintext)
}

// sealerTx reads a device's verified certificate (and a phone's auth secret) in tx.
func (h *Hub) sealerTx(tx *store.Tx, device string) (sealer, error) {
	_, revoked, err := tx.DeviceLinkInfo(device)
	if errors.Is(err, store.ErrNotFound) || revoked {
		return sealer{}, ErrNoLink
	}
	if err != nil {
		return sealer{}, err
	}
	cert, err := h.cfg.Keys.CertTx(tx, device)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, hubkeys.ErrBadCert) {
		return sealer{}, ErrNoLink
	}
	if err != nil {
		return sealer{}, err
	}
	s := sealer{device: device, cert: cert}
	if cert.Body.Box.Alg == keys.AlgP256 {
		a, err := tx.LinkTargetAuth(device)
		if err != nil {
			return sealer{}, err
		}
		if s.auth, err = keys.UnB64(a); err != nil || len(s.auth) != 16 {
			return sealer{}, ErrNoLink
		}
	}
	return s, nil
}

func (h *Hub) enqueue(tx *store.Tx, device, t, tag string, ttl time.Duration, plaintext []byte, ref string) (int64, error) {
	s, err := h.sealerTx(tx, device)
	if err != nil {
		return 0, err
	}
	if t == link.TypeAlert || t == link.TypePeerMsg {
		n, err := tx.LinkTotal()
		if err != nil {
			return 0, err
		}
		if n >= MaxTotal {
			return 0, ErrQueueFull
		}
	}
	nowMS := tx.NowMS()
	refs, err := tx.LinkCollapse(device, t, tag)
	if err != nil {
		return 0, err
	}
	if err := tx.DeliveriesCollapsed(refs, nowMS); err != nil {
		return 0, err
	}
	seq, err := tx.LinkTakeSeq(device)
	if err != nil {
		return 0, err
	}
	exp := nowMS/1000 + int64(ttl/time.Second)
	missed := 0
	var body []byte
	for {
		pt := plaintext
		if missed > 0 {
			pt = withMissed(plaintext, missed)
		}
		if body, err = s.seal(t, pt); err != nil {
			return 0, fmt.Errorf("link: seal for %s: %w", device, err)
		}
		rows, size, err := tx.LinkUsage(device)
		if err != nil {
			return 0, err
		}
		if rows+1 <= MaxRows && size+int64(len(body)) <= MaxBytes {
			break
		}
		if t == link.TypePeerMsg {
			return 0, ErrQueueFull
		}
		if t != link.TypeAlert {
			break // refresh, matrix, revoke, peer_status are never dropped (they collapse)
		}
		_, ok, err := tx.LinkOldestAlert(device)
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		missed++
	}
	sig, err := h.key.SignLink(link.FrameInput(device, t, seq, exp, tag, link.CtxQueue, body))
	if err != nil {
		return 0, err
	}
	if err := tx.LinkInsert(store.LinkRow{DeviceID: device, Seq: seq, Type: t, Tag: tag, Body: body, Sig: sig, Exp: exp, Ref: ref,
		CreatedAt: nowMS}); err != nil {
		return 0, err
	}
	if missed > 0 {
		h.log.Printf("link: %s queue full, %d older alerts dropped", device, missed)
	}
	return seq, nil
}

// withMissed adds "missed": n to an alert body (link.md 5.5), so the app refetches.
func withMissed(plaintext []byte, n int) []byte {
	if len(plaintext) < 2 || plaintext[0] != '{' {
		return plaintext
	}
	head := fmt.Sprintf(`{"missed":%d`, n)
	rest := plaintext[1:]
	if len(rest) > 0 && rest[0] != '}' {
		head += ","
	}
	return append([]byte(head), rest...)
}

// ackTx is a cumulative ack: delete rows up to seq and set delivered_at on the deliveries
// they carried (link.md 5.6, push.md 13.4). Rows that expired before the ack were never
// sent (the pump skips them): they expire as in the sweeper, not count as delivered.
func (h *Hub) ackTx(tx *store.Tx, device string, seq int64) error {
	if err := h.expireTx(tx, device, seq); err != nil {
		return err
	}
	refs, err := tx.LinkAck(device, seq)
	if err != nil {
		return err
	}
	return tx.DeliveriesDelivered(refs, tx.NowMS())
}

// expireTx expires the device's rows up to seq that are past exp.
func (h *Hub) expireTx(tx *store.Tx, device string, seq int64) error {
	rows, err := tx.LinkExpireUpTo(device, seq, h.now().Unix())
	if err != nil {
		return err
	}
	return h.peerExpiredTx(tx, rows)
}

// peerExpiredTx tells the peer relay about expired peer_msg rows.
func (h *Hub) peerExpiredTx(tx *store.Tx, rows []store.LinkRow) error {
	for _, r := range rows {
		if id, ok := cutPrefix(r.Ref, "pm:"); ok && h.cfg.PeerExpired != nil {
			if err := h.cfg.PeerExpired(tx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// reject deletes one row the device refused (link.md 5.6); it never moves acked_seq.
func (h *Hub) reject(ctx context.Context, device string, seq int64) {
	found := false
	err := h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		_, found, err = tx.LinkDelete(device, seq)
		return err
	})
	if err != nil {
		h.log.Printf("link: reject %s seq %d: %v", device, seq, err)
		return
	}
	h.mu.Lock()
	h.rejected[device]++
	h.mu.Unlock()
	if found {
		h.log.Printf("link: warning: %s rejected frame %d (bad signature or body); row deleted", device, seq)
	}
}

// Rejected is how many frames the device rejected since the hub started.
func (h *Hub) Rejected(device string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rejected[device]
}

// ---- documents for machines (link.md 3.3, 7) ----

func isMachine(kind string) bool { return kind == "machine" || kind == "desktop" }

// docFrames returns the current matrix and revocation documents as frame bodies, with
// their versions (0 when there is none).
func (h *Hub) docFrames(ctx context.Context) (matrix []byte, mv int, revoke []byte, rv int, err error) {
	m, err := h.cfg.Keys.Matrix(ctx)
	if err != nil {
		return nil, 0, nil, 0, err
	}
	if m != nil {
		matrix, _ = json.Marshal(m.Doc)
		mv = m.Body.Version
	}
	r, err := h.cfg.Keys.Revocations(ctx)
	if err != nil {
		return nil, 0, nil, 0, err
	}
	if r != nil {
		revoke, _ = json.Marshal(r.Doc)
		rv = r.Body.Version
	}
	return
}

// sendKnown queues the current documents a machine's hello says it lacks (link.md 3.3),
// unless one is already queued.
func (h *Hub) sendKnown(ctx context.Context, device, kind string, known *link.Known) error {
	if !isMachine(kind) {
		return nil
	}
	var k link.Known
	if known != nil {
		k = *known
	}
	matrix, mv, revoke, rv, err := h.docFrames(ctx)
	if err != nil {
		return err
	}
	return h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		for _, d := range []struct {
			t    string
			have int
			v    int
			body []byte
		}{{link.TypeMatrix, k.Matrix, mv, matrix}, {link.TypeRevoke, k.Revocations, rv, revoke}} {
			if d.v == 0 || d.have >= d.v {
				continue
			}
			if q, err := tx.LinkHasType(device, d.t); err != nil || q {
				if err != nil {
					return err
				}
				continue
			}
			if _, err := h.EnqueueTx(tx, device, d.t, d.t, TTLDocs, d.body, ""); err != nil && !errors.Is(err, ErrNoLink) {
				return err
			}
		}
		return nil
	})
}

// PublishDocs queues the newest matrix and revocation documents to every machine when
// either is newer than the last ones published (link.md 7). The hub calls it after a
// change it made and from the sweeper, which catches changes made by `wy hub` commands.
func (h *Hub) PublishDocs(ctx context.Context) error {
	matrix, mv, revoke, rv, err := h.docFrames(ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	sendM, sendR := mv > h.docs.matrix, rv > h.docs.revocations
	h.mu.Unlock()
	if !sendM && !sendR {
		return nil
	}
	err = h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		ms, err := tx.LinkDevices("machine", "desktop")
		if err != nil {
			return err
		}
		for _, m := range ms {
			if sendM {
				if _, err := h.EnqueueTx(tx, m.ID, link.TypeMatrix, link.TypeMatrix, TTLDocs, matrix, ""); err != nil && !errors.Is(err, ErrNoLink) {
					return err
				}
			}
			if sendR {
				if _, err := h.EnqueueTx(tx, m.ID, link.TypeRevoke, link.TypeRevoke, TTLDocs, revoke, ""); err != nil && !errors.Is(err, ErrNoLink) {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.docs.matrix, h.docs.revocations = max(h.docs.matrix, mv), max(h.docs.revocations, rv)
	h.mu.Unlock()
	return nil
}

// Revoked closes a revoked device's link at once (4403) and publishes the new revocation
// list (link.md 7). The revocation itself (device row, queue, list) is hubkeys'.
func (h *Hub) Revoked(ctx context.Context, device string) {
	h.mu.Lock()
	c := h.conns[device]
	if p, ok := h.polls[device]; ok {
		close(p)
		delete(h.polls, device)
	}
	h.mu.Unlock()
	if c != nil {
		go c.close(link.CloseRevoked, "revoked")
	}
	if err := h.PublishDocs(ctx); err != nil {
		h.log.Printf("link: publishing the revocation list: %v", err)
	}
}

// stillLive re-reads the device after its connection or poll is registered: a revocation
// committed between login's check and the registration ran Revoked before there was
// anything to close, so the new link would stay up until the sweeper.
func (h *Hub) stillLive(ctx context.Context, device string) (bool, error) {
	d, err := h.cfg.Store.DeviceDetailByID(ctx, device)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return d.RevokedAt == 0, nil
}

// Connected reports whether the device is logged in now: a WebSocket, or a long poll
// waiting (the peer relay's "its machine is offline", peers.md 4.3).
func (h *Hub) Connected(device string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ws := h.conns[device]
	_, poll := h.polls[device]
	return ws || poll
}

// ---- the sweeper (link.md 5.4) ----

// Start notes the current document versions (machines learn older changes from `known` at
// login) and starts the sweeper.
func (h *Hub) Start(ctx context.Context) error {
	_, mv, _, rv, err := h.docFrames(ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.docs.matrix, h.docs.revocations = mv, rv
	h.started = true
	h.mu.Unlock()
	go func() {
		defer close(h.done)
		t := time.NewTicker(SweepEvery)
		defer t.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-t.C:
				h.Sweep(context.Background())
			}
		}
	}()
	return nil
}

// Sweep deletes expired rows, closes links of devices revoked behind the hub's back (`wy hub
// token revoke`) and publishes new documents.
func (h *Hub) Sweep(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	nowSec := h.now().Unix()
	var expired int
	err := h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		rows, err := tx.LinkExpire(nowSec)
		if err != nil {
			return err
		}
		expired = len(rows)
		return h.peerExpiredTx(tx, rows)
	})
	if err != nil {
		h.log.Printf("link sweep: %v", err)
	} else if expired > 0 {
		h.log.Printf("link sweep: %d expired frames deleted", expired)
	}
	h.mu.Lock()
	var devs []string
	for d := range h.conns {
		devs = append(devs, d)
	}
	h.mu.Unlock()
	for _, d := range devs {
		dd, err := h.cfg.Store.DeviceDetailByID(ctx, d)
		if errors.Is(err, store.ErrNotFound) || (err == nil && dd.RevokedAt != 0) {
			h.Revoked(ctx, d)
		}
	}
	if err := h.PublishDocs(ctx); err != nil {
		h.log.Printf("link sweep: documents: %v", err)
	}
	h.mu.Lock()
	now := h.now().Unix()
	for n, exp := range h.nonces {
		if exp < now {
			delete(h.nonces, n)
		}
	}
	h.mu.Unlock()
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}

// Close stops the sweeper and ends every connection: a draining notice, then 1001.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	started := h.started
	conns := make([]*conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	for d, p := range h.polls {
		close(p)
		delete(h.polls, d)
	}
	h.mu.Unlock()
	select {
	case <-h.stop:
	default:
		close(h.stop)
	}
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Go(c.drain)
	}
	wg.Wait()
	if started {
		<-h.done
	}
}

// ---- rate limits ----

// limiter is a sliding window per key.
type limiter struct {
	hits map[string][]time.Time
}

// take counts one event for key at now and reports whether it is within n per window.
func (l *limiter) take(key string, now time.Time, n int, window time.Duration) bool {
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	cut := now.Add(-window)
	ts := l.hits[key]
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= n {
		l.hits[key] = ts
		return false
	}
	l.hits[key] = append(ts, now)
	return true
}
