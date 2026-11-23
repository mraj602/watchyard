// Package linkclient is the machine side of the link (docs/spec/link.md): `wy agent run`
// logs in to the hub's /v1/link with this machine's device keys, verifies every frame
// against the pinned hub key, opens the queued bodies, stores accepted matrix and
// revocation documents, hands peer frames to a PeerHandler, acks, rejects and
// reconnects with link.md 8's backoff. It implements agent.LinkClient.
//
// State: <state>/agent/link.json ({"last", "rejected"}; link.md 6.1), and the documents in
// <state>/keys (keys.LoadAcceptedMatrix, keys.LoadAcceptedRevocations read them back).
//
// Contract: docs/spec/link.md 3-8, keys.md 3.5, 5.4, 6.
package linkclient

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/mraj602/watchyard/internal/agent"
	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
)

// PeerHandler is the peers seam: queued peer frames (peer_msg, peer_status,
// peer_thread_resume; their hub signature already checked) and the hub's
// ephemeral answers to peer requests (peers, peer_ack, notices) go to it.
type PeerHandler interface {
	// Queued handles a queued frame's opened body. nil: processed durably (delivered or the
	// refusal recorded); the client then moves `last` and acks. An error leaves the frame
	// unacked: the client reconnects and the hub replays it.
	Queued(ctx context.Context, t string, seq int64, body []byte) error
	// Ephemeral gets a hub answer (peers, peer_ack, notice) for a frame sent with Send.
	Ephemeral(t string, body []byte)
}

// Config wires a Client.
type Config struct {
	// State is <state>: device keys and pins in <state>/keys, link.json in <state>/agent.
	State string
	// URL is the hub's link URL (ws:// or wss://…/v1/link), made from hub.url by HubURL.
	URL string
	// HTTP is the client the WebSocket handshake goes through (hub.dial); nil: default.
	HTTP *http.Client
	// HubMayWiden is config peers.hub_may_widen (keys.md 5.4).
	HubMayWiden bool
	// Peers gets peer frames; nil: they are acked and dropped with a log line.
	Peers PeerHandler
	// Version is wy's version for hello.client.
	Version string
	// Log gets one line per connect, close and stored document. Nil discards.
	Log *log.Logger
	// Now is the clock; nil = time.Now.
	Now func() time.Time
	// Backoff overrides link.md 8's delays (tests); PingEvery the ping interval (default
	// the welcome's ping_s).
	Backoff   []time.Duration
	PingEvery time.Duration
}

// Backoff is link.md 8's reconnect schedule; each step gets ±20 % jitter.
var Backoff = []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second,
	30 * time.Second, 60 * time.Second, 120 * time.Second}

// HubURL turns hub.url (http[s]://host[:port][/]) into its link URL.
func HubURL(hubURL string) (string, error) {
	u := strings.TrimRight(hubURL, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + strings.TrimPrefix(u, "https://") + "/v1/link", nil
	case strings.HasPrefix(u, "http://"):
		return "ws://" + strings.TrimPrefix(u, "http://") + "/v1/link", nil
	}
	return "", fmt.Errorf("hub.url %q is not an http(s) URL", hubURL)
}

// Client is the machine link client.
type Client struct {
	cfg Config
	log *log.Logger

	mu     sync.Mutex
	status agent.LinkStatus
	ws     *websocket.Conn
	nonce  string
	wmu    sync.Mutex
}

// New returns a client; Run connects.
func New(cfg Config) *Client {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Backoff == nil {
		cfg.Backoff = Backoff
	}
	lg := cfg.Log
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	return &Client{cfg: cfg, log: lg, status: agent.LinkStatus{State: "none"}}
}

// Status is the link's state for `wy agent status`.
func (c *Client) Status() agent.LinkStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) setStatus(state, detail string) {
	c.mu.Lock()
	c.status = agent.LinkStatus{State: state, Detail: detail}
	c.mu.Unlock()
}

// ErrNotConnected: Send without a logged-in connection.
var ErrNotConnected = errors.New("linkclient: not connected")

// Send writes a device→hub frame (link.md 6.2: peer_msg, peer_result, peer_log,
// peers_list) on the live connection. The hub's answer goes to PeerHandler.Ephemeral.
func (c *Client) Send(ctx context.Context, v any) error {
	c.mu.Lock()
	ws := c.ws
	c.mu.Unlock()
	if ws == nil {
		return ErrNotConnected
	}
	return c.write(ctx, ws, v)
}

func (c *Client) write(ctx context.Context, ws *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return ws.Write(ctx, websocket.MessageText, b)
}

// ---- durable state (link.md 6.1) ----

type state struct {
	Last     int64 `json:"last"`
	Rejected int   `json:"rejected"`
}

func (c *Client) statePath() string { return filepath.Join(c.cfg.State, "agent", "link.json") }

func (c *Client) readState() state {
	var s state
	if b, err := os.ReadFile(c.statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (c *Client) writeState(s state) error {
	if err := os.MkdirAll(filepath.Dir(c.statePath()), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return atomicfile.Write(c.statePath(), append(b, '\n'), 0o600)
}

// LastSeq is the highest processed seq in <state>/agent/link.json (0 when none).
func LastSeq(stateDir string) int64 {
	return (&Client{cfg: Config{State: stateDir}}).readState().Last
}

// ---- the loop ----

// stopError ends Run: the device must not reconnect (link.md 8).
type stopError struct{ msg string }

func (e *stopError) Error() string { return e.msg }

// Run connects and reconnects until ctx is done or the hub says stop (4403, 4426, three
// 4401 in a row).
func (c *Client) Run(ctx context.Context) error {
	attempt, unauth := 0, 0
	for ctx.Err() == nil {
		start := c.cfg.Now()
		code, err := c.session(ctx)
		if ctx.Err() != nil {
			break
		}
		var se *stopError
		if errors.As(err, &se) {
			c.setStatus("stopped", se.msg)
			return err
		}
		lived := c.cfg.Now().Sub(start)
		if lived >= time.Minute {
			attempt = 0
		}
		delay := c.cfg.Backoff[min(attempt, len(c.cfg.Backoff)-1)]
		attempt++
		switch code {
		case link.CloseRevoked:
			msg := "the hub removed this machine (4403); pair it again with wy agent enable"
			c.setStatus("stopped", msg)
			return &stopError{msg}
		case link.CloseUpgrade:
			msg := "the hub needs a newer wy (4426); update wy"
			c.setStatus("stopped", msg)
			return &stopError{msg}
		case link.CloseUnauthorized:
			unauth++
			if unauth >= 3 {
				msg := "Hub does not recognise this device's key (4401 three times)"
				c.setStatus("stopped", msg)
				return &stopError{msg}
			}
		case link.CloseSuperseded:
			delay = max(delay, 120*time.Second)
		case link.CloseRateLimited, link.CloseFrozen:
			delay = max(delay, 60*time.Second)
		}
		if code != link.CloseUnauthorized && code != -1 {
			unauth = 0
		}
		detail := link.CloseWord(code)
		if err != nil && code == -1 {
			detail = err.Error()
		}
		c.setStatus("reconnecting", detail)
		c.log.Printf("link: disconnected (%s); reconnecting in %s", detail, delay.Round(time.Second))
		j := 1 + (rand.Float64()*0.4 - 0.2)
		t := time.NewTimer(time.Duration(float64(delay) * j))
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
	}
	c.setStatus("stopped", "agent stopping")
	return nil
}

// creds are this machine's keys and pins.
type creds struct {
	device string
	sign   ed25519.PrivateKey
	box    *ecdh.PrivateKey
	hub    ed25519.PublicKey
	hubFP  string
}

func (c *Client) creds() (creds, error) {
	dk, err := keys.ReadDeviceKeys(c.cfg.State)
	if err != nil {
		return creds{}, &stopError{"no device keys (run wy agent enable): " + err.Error()}
	}
	if dk.Device == "" {
		return creds{}, &stopError{"device keys are not registered with the hub (run wy agent enable)"}
	}
	var cr creds
	cr.device = dk.Device
	if cr.sign, err = dk.SignKey(); err != nil {
		return creds{}, &stopError{err.Error()}
	}
	if cr.box, err = dk.BoxKey(); err != nil {
		return creds{}, &stopError{err.Error()}
	}
	p, err := keys.ReadPin(keys.Path(c.cfg.State, keys.HubPinFile))
	if err != nil {
		return creds{}, &stopError{"no pinned hub key (pair this machine again): " + err.Error()}
	}
	cr.hub, _ = p.Key()
	cr.hubFP = p.FP
	return cr, nil
}

// known is hello.known: the stored document versions.
func (c *Client) known() *link.Known {
	k := &link.Known{}
	if m, err := keys.LoadAcceptedMatrix(c.cfg.State); err == nil && m != nil {
		k.Matrix = m.Body.Version
	}
	if r, err := keys.LoadAcceptedRevocations(c.cfg.State); err == nil && r != nil {
		k.Revocations = r.Body.Version
	}
	return k
}

// session is one connection: login, then frames until it ends. It returns the close code
// (-1 when the connection failed without one).
func (c *Client) session(ctx context.Context) (int, error) {
	cr, err := c.creds()
	if err != nil {
		return -1, err
	}
	c.setStatus("connecting", "")
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ws, _, err := websocket.Dial(dctx, c.cfg.URL, &websocket.DialOptions{HTTPClient: c.cfg.HTTP,
		Subprotocols: link.Subprotocols})
	cancel()
	if err != nil {
		return -1, err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(link.MaxMessage)

	// 3.1: the challenge, checked against the pin before anything is signed
	lctx, lcancel := context.WithTimeout(ctx, 30*time.Second)
	defer lcancel()
	var ch link.Challenge
	if err := readJSON(lctx, ws, &ch); err != nil {
		return int(websocket.CloseStatus(err)), err
	}
	if err := link.CheckChallenge(ch, cr.hub); err != nil || ch.Hub != cr.hubFP {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		return -1, &stopError{"the hub's challenge is not signed by the pinned hub key; not logging in"}
	}
	st := c.readState()
	pingS := int(c.cfg.PingEvery / time.Second)
	client := link.Client{Kind: "agent", Wy: c.cfg.Version, OS: runtime.GOOS, Caps: []string{link.CapPeerService}}
	if pingS >= 60 {
		client.PingS = pingS
	}
	hello := link.SignHelloEd25519(cr.sign, ch, cr.device, st.Last, client, c.known())
	if err := c.write(lctx, ws, hello); err != nil {
		return int(websocket.CloseStatus(err)), err
	}
	var wf link.Frame
	if err := readJSON(lctx, ws, &wf); err != nil {
		return int(websocket.CloseStatus(err)), err
	}
	wb, err := link.Check(wf, cr.hub, cr.device, ch.Nonce)
	var w link.Welcome
	if err != nil || wf.T != link.TypeWelcome || json.Unmarshal(wb, &w) != nil {
		return -1, fmt.Errorf("bad welcome: %v", err)
	}
	lcancel()
	c.pinOwner(cr, w.Owner)
	offset := time.Duration(0)
	if t, err := time.Parse(time.RFC3339Nano, w.ServerTime); err == nil {
		offset = t.Sub(c.cfg.Now())
	}

	c.mu.Lock()
	c.ws, c.nonce = ws, ch.Nonce
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.ws == ws {
			c.ws = nil
		}
		c.mu.Unlock()
	}()
	c.setStatus("connected", "")
	c.log.Printf("link: connected to %s as %s (hub at seq %d)", c.cfg.URL, cr.device, w.Next-1)

	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	// pings (the device owns the radio, link.md 2)
	every := c.cfg.PingEvery
	if every <= 0 {
		every = time.Duration(max(w.PingS, 60)) * time.Second
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
				pctx, pc := context.WithTimeout(sctx, every)
				err := ws.Ping(pctx)
				pc()
				if err != nil && sctx.Err() == nil {
					ws.CloseNow() // silent drop: reconnect
					return
				}
			}
		}
	}()
	// batched acks: at most one per 500 ms, or one per 16 frames (link.md 5.6)
	acks := &acker{c: c, ws: ws, sent: st.Last}
	go acks.loop(sctx)

	for {
		var f link.Frame
		if err := readJSON(ctx, ws, &f); err != nil {
			if ctx.Err() != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "")
				return link.CloseNormal, nil
			}
			acks.flush(context.Background())
			return int(websocket.CloseStatus(err)), err
		}
		if err := c.frame(sctx, ws, cr, ch.Nonce, f, offset, acks); err != nil {
			acks.flush(context.Background())
			_ = ws.Close(websocket.StatusNormalClosure, "")
			return -1, err
		}
	}
}

// frame checks and processes one frame (link.md 4.3). An error ends the connection without
// an ack for the frame (the hub replays it).
func (c *Client) frame(ctx context.Context, ws *websocket.Conn, cr creds, nonce string, f link.Frame, offset time.Duration, acks *acker) error {
	body, err := link.Check(f, cr.hub, cr.device, nonce)
	queued := link.IsQueued(f.T, f.Seq)
	if err != nil {
		if queued && f.Seq > 0 {
			c.reject(ctx, ws, f, err)
		} else {
			c.log.Printf("link: warning: dropped a %s frame that failed its checks: %v", f.T, err)
		}
		return nil
	}
	if !queued {
		switch {
		case f.T == link.TypeNotice:
			var n link.Notice
			_ = json.Unmarshal(body, &n)
			c.log.Printf("link: hub notice %s: %s", n.Code, n.Message)
			if c.cfg.Peers != nil {
				c.cfg.Peers.Ephemeral(f.T, body)
			}
		case c.cfg.Peers != nil:
			c.cfg.Peers.Ephemeral(f.T, body)
		}
		return nil
	}
	st := c.readState()
	if f.Seq <= st.Last {
		return nil // a replay of a processed frame: dropped (step 3)
	}
	if c.cfg.Now().Add(offset).Unix() > f.Exp {
		c.log.Printf("link: frame %d (%s) expired; dropped", f.Seq, f.T)
		return c.done(st, f.Seq, acks)
	}
	pt, err := link.OpenMachine(cr.box, cr.device, f.T, body)
	if err != nil {
		c.reject(ctx, ws, f, err)
		return nil
	}
	switch f.T {
	case link.TypeMatrix:
		c.storeMatrix(pt)
	case link.TypeRevoke:
		c.storeRevocations(pt)
	case link.TypePeerMsg, link.TypePeerStatus, link.TypePeerThreadResume:
		if c.cfg.Peers == nil {
			c.log.Printf("link: %s frame %d dropped: this agent does not handle peer messages yet", f.T, f.Seq)
			break
		}
		if err := c.cfg.Peers.Queued(ctx, f.T, f.Seq, pt); err != nil {
			return fmt.Errorf("processing %s %d: %w", f.T, f.Seq, err)
		}
	default:
		c.log.Printf("link: %s frame %d is not for machines; dropped", f.T, f.Seq)
	}
	return c.done(st, f.Seq, acks)
}

// done records seq as processed (durably, before the ack) and schedules the ack.
func (c *Client) done(st state, seq int64, acks *acker) error {
	st.Last = seq
	if err := c.writeState(st); err != nil {
		return fmt.Errorf("writing %s: %w", c.statePath(), err)
	}
	acks.processed(seq)
	return nil
}

func (c *Client) reject(ctx context.Context, ws *websocket.Conn, f link.Frame, why error) {
	c.log.Printf("link: warning: rejected frame %d (%s): %v", f.Seq, f.T, why)
	st := c.readState()
	st.Rejected++
	_ = c.writeState(st)
	_ = c.write(ctx, ws, map[string]any{"t": link.TypeReject, "seq": f.Seq})
}

// pinOwner pins the owner key from welcome on first sight (keys.md 3.5) when it carries
// the owner's self-signature and the hub's confirmation; a different key is only logged.
func (c *Client) pinOwner(cr creds, d *keys.Doc) {
	if d == nil {
		return
	}
	ok, err := keys.VerifyOwnerKey(*d, cr.hub)
	if err != nil || !ok.Confirmed {
		c.log.Printf("link: warning: the hub's owner key does not verify; ignored")
		return
	}
	err = keys.SetPin(keys.Path(c.cfg.State, keys.OwnerPinFile), keys.NewPin(ok.Pub, keys.PinTOFU, c.cfg.Now()))
	switch {
	case errors.Is(err, keys.ErrPinned):
		c.setStatus("connected", "the hub's owner key "+keys.GroupFP(ok.FP)+" differs from the pinned one (wy keys trust-owner)")
		c.log.Printf("link: warning: the hub's owner key %s differs from the pinned one; keeping the pin", keys.GroupFP(ok.FP))
	case err != nil:
		c.log.Printf("link: pinning the owner key: %v", err)
	}
}

func (c *Client) storeMatrix(pt []byte) {
	var d keys.Doc
	if err := json.Unmarshal(pt, &d); err != nil {
		c.log.Printf("link: warning: matrix frame is not a document; ignored")
		return
	}
	s, ok, err := keys.SaveAcceptedMatrix(c.cfg.State, d, c.cfg.HubMayWiden)
	switch {
	case errors.Is(err, keys.ErrHubWidening):
		c.log.Printf("link: the hub's matrix v%d widens what this machine allows and peers.hub_may_widen is false; kept the stored one", s.Body.Version)
	case err != nil:
		c.log.Printf("link: warning: matrix refused: %v", err)
	case ok:
		c.log.Printf("link: matrix v%d stored (signed by %s)", s.Body.Version, s.Signer)
	}
}

func (c *Client) storeRevocations(pt []byte) {
	var d keys.Doc
	if err := json.Unmarshal(pt, &d); err != nil {
		c.log.Printf("link: warning: revoke frame is not a document; ignored")
		return
	}
	s, ok, err := keys.SaveAcceptedRevocations(c.cfg.State, d)
	switch {
	case err != nil:
		c.log.Printf("link: warning: revocation list refused: %v", err)
	case ok:
		c.log.Printf("link: revocation list v%d stored (%d devices)", s.Body.Version, len(s.Body.Devices))
	}
}

func readJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	typ, b, err := ws.Read(ctx)
	if err != nil {
		return err
	}
	if typ != websocket.MessageText {
		return link.ErrFrame
	}
	return json.Unmarshal(b, v)
}

// acker batches cumulative acks.
type acker struct {
	c    *Client
	ws   *websocket.Conn
	mu   sync.Mutex
	last int64 // processed
	sent int64 // acked
}

func (a *acker) processed(seq int64) {
	a.mu.Lock()
	a.last = max(a.last, seq)
	now := a.last-a.sent >= 16
	a.mu.Unlock()
	if now {
		a.flush(context.Background())
	}
}

func (a *acker) flush(ctx context.Context) {
	a.mu.Lock()
	last, sent := a.last, a.sent
	a.mu.Unlock()
	if last <= sent {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.c.write(wctx, a.ws, map[string]any{"t": link.TypeAck, "seq": last}); err == nil {
		a.mu.Lock()
		a.sent = max(a.sent, last)
		a.mu.Unlock()
	}
}

func (a *acker) loop(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.flush(ctx)
		}
	}
}
