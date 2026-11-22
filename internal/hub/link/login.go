package hublink

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
)

// loginError is a refused login: the close code of link.md 8 and a reason word.
type loginError struct {
	code   int
	reason string
}

func (e *loginError) Error() string { return fmt.Sprintf("%d %s", e.code, e.reason) }

func refuse(code int, reason string) error { return &loginError{code, reason} }

// session is a logged-in device.
type session struct {
	device, kind string
	cert         keys.Cert
	nonce        string
	hello        link.Hello
	pingS        int
	acked, next  int64
}

// errBusy: MaxNonces signed-in challenges are still remembered as spent.
var errBusy = errors.New("too many spent challenges")

// Challenge nonces are stateless: random bytes plus an HMAC (with a per-process secret) over
// them and the expiry, so op challenge, which needs no credential, pins nothing in memory.
// A nonce is remembered as spent only once a hello with a valid device signature used it;
// that set is bounded by the device login rate limits, and unauthenticated callers cannot
// add to it.
func (h *Hub) nonceMAC(rnd []byte, exp int64) []byte {
	m := hmac.New(sha256.New, h.nonceKey)
	m.Write([]byte("wy-link-nonce\x00"))
	m.Write(rnd)
	fmt.Fprintf(m, "\x00%d", exp)
	return m.Sum(nil)[:16]
}

// newChallenge makes a challenge with a fresh single-use nonce (link.md 3.1).
func (h *Hub) newChallenge() (link.Challenge, error) {
	rnd := make([]byte, 16)
	if _, err := rand.Read(rnd); err != nil {
		return link.Challenge{}, err
	}
	exp := h.now().Add(ChallengeTTL).Unix()
	nonce := keys.B64(append(rnd, h.nonceMAC(rnd, exp)...))
	return link.NewChallenge(h.key, h.key.FP, nonce, exp)
}

// nonceValid reports whether nonce is one this hub issued for exp (not yet checked for
// expiry or use).
func (h *Hub) nonceValid(nonce string, exp int64) bool {
	b, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(b) != 32 {
		return false
	}
	return hmac.Equal(b[16:], h.nonceMAC(b[:16], exp))
}

// spendNonce records nonce as used; false when it already was. errBusy when the spent set
// is full of unexpired entries.
func (h *Hub) spendNonce(nonce string, exp int64) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, used := h.nonces[nonce]; used {
		return false, nil
	}
	if len(h.nonces) >= MaxNonces {
		now := h.now().Unix()
		for n, e := range h.nonces {
			if e < now {
				delete(h.nonces, n)
			}
		}
		if len(h.nonces) >= MaxNonces {
			return false, errBusy
		}
	}
	h.nonces[nonce] = exp
	return true, nil
}

// login runs link.md 3.2's steps 1-5 and 7-8 for a hello (step 6, closing the device's other
// connection, is the caller's). transport is "ws" or "poll".
func (h *Hub) login(ctx context.Context, hl link.Hello, transport, ip string) (*session, error) {
	// 1. the nonce: known, unused, not past exp, and the hello's exp is the challenge's
	if hl.T != link.TypeHello {
		return nil, refuse(link.CloseUnauthorized, "bad_hello")
	}
	if hl.V != link.Version {
		return nil, refuse(link.CloseUpgrade, "upgrade")
	}
	exp := hl.Exp
	if !h.nonceValid(hl.Nonce, exp) || h.now().Unix() > exp {
		return nil, refuse(link.CloseUnauthorized, "nonce")
	}
	// 2. the device, not revoked, with keys; 3. its certificate verifies (keys from it only)
	var s session
	err := func() error {
		d, err := h.cfg.Store.DeviceDetailByID(ctx, hl.Device)
		if errors.Is(err, store.ErrNotFound) {
			return refuse(link.CloseUnauthorized, "unknown_device")
		}
		if err != nil {
			return err
		}
		if d.RevokedAt != 0 {
			return refuse(link.CloseRevoked, "revoked")
		}
		s.kind = d.Kind
		s.cert, err = h.cfg.Keys.Cert(ctx, hl.Device)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return refuse(link.CloseUnauthorized, "no_keys")
		case errors.Is(err, hubkeys.ErrBadCert):
			h.log.Printf("link: warning: device_key row for %s does not match its certificate", hl.Device)
			return refuse(link.CloseUnauthorized, "bad_cert")
		case err != nil:
			return err
		}
		if s.cert.Body.Device != hl.Device || s.cert.Body.Hub != h.key.FP {
			h.log.Printf("link: warning: device_key row for %s does not match its certificate", hl.Device)
			return refuse(link.CloseUnauthorized, "bad_cert")
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	// 4. the signature, with the certificate's sign key
	if err := link.VerifyHello(hl, h.key.FP, s.cert.Body.Sign.Alg, s.cert.Body.SignPub()); err != nil {
		return nil, refuse(link.CloseUnauthorized, "signature")
	}
	// 5. rate
	h.mu.Lock()
	var allowed bool
	if transport == "poll" {
		allowed = h.pollLim.take(hl.Device, h.now(), PollsPerMinute, time.Minute)
	} else {
		allowed = h.logins.take(hl.Device, h.now(), LoginsPerMinute, time.Minute)
	}
	h.mu.Unlock()
	if !allowed {
		return nil, refuse(link.CloseRateLimited, "rate_limited")
	}
	// the signature held: now the nonce is spent (a replayed hello finds it so)
	switch fresh, err := h.spendNonce(hl.Nonce, exp); {
	case errors.Is(err, errBusy):
		return nil, refuse(link.CloseRateLimited, "busy")
	case !fresh:
		return nil, refuse(link.CloseUnauthorized, "nonce")
	}
	if fr, err := h.cfg.Store.Frozen(ctx); err == nil && fr != nil {
		return nil, refuse(link.CloseFrozen, "frozen")
	}
	s.device, s.nonce, s.hello = hl.Device, hl.Nonce, hl
	set := h.settings(ctx)
	s.pingS = set.PingS
	if p := hl.Client.PingS; p >= 60 && p <= 900 && p > s.pingS {
		s.pingS = p
	}
	// 7. resync after a restored backup, or 8. the signed `last` as a cumulative ack
	helloJSON, _ := json.Marshal(map[string]any{"client": hl.Client, "known": hl.Known})
	err = h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.LinkConnected(hl.Device, transport, ip, string(helloJSON), tx.NowMS()); err != nil {
			return err
		}
		st, err := tx.LinkState(hl.Device)
		if err != nil {
			return err
		}
		if hl.Last >= st.NextSeq {
			if err := h.resyncTx(tx, hl.Device, hl.Last, st.NextSeq); err != nil {
				return err
			}
		} else if err := h.ackTx(tx, hl.Device, hl.Last); err != nil {
			return err
		}
		st, err = tx.LinkState(hl.Device)
		s.acked, s.next = st.AckedSeq, st.NextSeq
		return err
	})
	if err != nil {
		return nil, err
	}
	h.log.Printf("link login %s (%s) via %s", hl.Device, s.kind, transport)
	return &s, nil
}

// resyncTx renumbers a device's queue after its last (link.md 3.2 step 7): the device is
// ahead of a hub restored from a backup. The sealed bodies do not change; each row is signed
// again for its new seq. Nothing queued is lost.
func (h *Hub) resyncTx(tx *store.Tx, device string, last, next int64) error {
	rows, err := tx.LinkQueued(device)
	if err != nil {
		return err
	}
	for i := range rows {
		r := &rows[i]
		r.Seq = last + 1 + int64(i)
		if r.Sig, err = h.key.SignLink(link.FrameInput(device, r.Type, r.Seq, r.Exp, r.Tag, link.CtxQueue, r.Body)); err != nil {
			return err
		}
	}
	h.log.Printf("link: warning: link resync for %s: device at %d, hub at %d", device, last, next-1)
	return tx.LinkReplace(device, rows, last+1+int64(len(rows)), last)
}

// welcome is the welcome frame of a session (link.md 3.3), signed with its nonce.
func (h *Hub) welcome(ctx context.Context, s *session) (link.Frame, error) {
	set := h.settings(ctx)
	w := link.Welcome{V: link.Version, Device: s.device, Next: s.next, PingS: set.PingS, URLs: set.URLs,
		ServerTime: keys.FormatTime(h.now())}
	if o, err := h.cfg.Keys.ActiveOwner(ctx); err == nil && o != nil {
		d := o.Doc
		w.Owner = &d
	}
	return h.ephemeral(s, link.TypeWelcome, w)
}

// ephemeral signs an ephemeral frame for the session's nonce (link.md 4.3).
func (h *Hub) ephemeral(s *session, t string, body any) (link.Frame, error) {
	return link.EphemeralFrame(h.key, s.device, t, s.nonce, h.now().Add(ChallengeTTL).Unix(), body)
}

// frameOf is a queued row as a frame.
func frameOf(r store.LinkRow) link.Frame {
	return link.Frame{T: r.Type, Seq: r.Seq, Exp: r.Exp, Tag: r.Tag, Body: keys.B64(r.Body), Sig: keys.B64(r.Sig)}
}

// up handles one device→hub frame other than ack and reject (link.md 6.2) and returns the
// ephemeral reply, if any.
func (h *Hub) up(ctx context.Context, s *session, u link.Up) (*link.Frame, error) {
	var t string
	var body any
	switch u.T {
	case link.TypeAction:
		t, body = link.TypeActionResult, h.action(ctx, s.device, u)
	case link.TypePeerMsg, link.TypePeerResult, link.TypePeerLog, link.TypePeersList, link.TypePeerTag:
		switch {
		case !isMachine(s.kind):
			t, body = link.TypeNotice, link.Notice{V: 1, Code: link.NoticeForbidden, Message: "peer frames come from machines"}
		case h.cfg.Peers == nil:
			t, body = link.TypeNotice, link.Notice{V: 1, Code: link.NoticeUnknownType, Message: "this hub does not relay peer messages yet"}
		default:
			rt, rb, err := h.cfg.Peers.Up(ctx, s.device, u)
			if err != nil {
				return nil, err
			}
			if rt == "" {
				return nil, nil
			}
			t, body = rt, rb
		}
	default:
		t, body = link.TypeNotice, link.Notice{V: 1, Code: link.NoticeUnknownType, Message: "unknown frame type " + u.T}
	}
	f, err := h.ephemeral(s, t, body)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// action applies an action token as POST /v1/actions/{token} (link.md 6.2), with the same
// limit of 30 a minute, per device.
func (h *Hub) action(ctx context.Context, device string, u link.Up) link.ActionResult {
	r := link.ActionResult{V: 1, ID: u.ID}
	h.mu.Lock()
	ok := h.actLim.take(device, h.now(), 30, time.Minute)
	h.mu.Unlock()
	switch {
	case !ok:
		r.Status = 429
	case h.cfg.Action == nil:
		r.Status = 503
	default:
		r.Alert, r.Applied, r.Status = h.cfg.Action(ctx, u.Token)
	}
	return r
}
