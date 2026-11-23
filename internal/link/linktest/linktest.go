// Package linktest has test clients of the link (link.md 10): a phone (ECDSA P-256 sign
// key, RFC 8291 decryption with a software P-256 key and auth secret) and a machine (Ed25519,
// X25519 HPKE), each over the WebSocket or the long-poll fallback. They check frames in
// link.md 4.3's order, keep `last` as a device does, and send ack and reject.
//
// For tests only: the keys are made from fixed seeds and kept in memory.
package linktest

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
)

// ErrDup is a queued frame at or below `last` (link.md 4.3 step 3): dropped, not processed.
var ErrDup = errors.New("linktest: duplicate seq")

// Device is a test device: its keys, the pinned hub key and its durable `last`.
type Device struct {
	ID   string
	Kind string // machine | phone

	Sign  ed25519.PrivateKey // machine
	Box   *ecdh.PrivateKey   // machine X25519; phone P-256 (the RFC 8291 ua key)
	PSign *ecdsa.PrivateKey  // phone
	Auth  []byte             // phone, 16 bytes

	HubPub ed25519.PublicKey
	HubFP  string
	Known  *link.Known
	// NoCaps leaves link.CapPeerService out of a machine's hello (an old receiver).
	NoCaps bool

	mu   sync.Mutex
	last int64
}

// NewMachine makes a machine's keys from seed.
func NewMachine(seed byte) *Device {
	s := bytes.Repeat([]byte{seed}, 32)
	box, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{seed ^ 0x55}, 32))
	return &Device{Kind: "machine", Sign: ed25519.NewKeyFromSeed(s), Box: box}
}

// NewPhone makes a phone's keys from seed: a P-256 sign key, a P-256 box key and the auth
// secret, as the Android Keystore and the module would.
func NewPhone(seed byte) *Device {
	scalar := func(b byte) []byte { k := bytes.Repeat([]byte{b}, 32); k[0] = 0x01; return k }
	sk, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar(seed))
	if err != nil {
		panic(err)
	}
	box, err := ecdh.P256().NewPrivateKey(scalar(seed ^ 0x55))
	if err != nil {
		panic(err)
	}
	return &Device{Kind: "phone", PSign: sk, Box: box, Auth: bytes.Repeat([]byte{seed}, 16)}
}

// Pin sets the pinned hub key (keys.md 3.5).
func (d *Device) Pin(pub ed25519.PublicKey) {
	d.HubPub, d.HubFP = pub, keys.Fingerprint(pub)
}

// KeysNew is the device's key registration (PUT /v1/devices/{id}/keys).
func (d *Device) KeysNew() keys.DeviceKeysNew {
	if d.Kind == "phone" {
		pub, _ := d.PSign.PublicKey.Bytes()
		return keys.DeviceKeysNew{Sign: keys.KeySpec{Alg: keys.AlgP256, Pub: keys.B64(pub)},
			Box: keys.KeySpec{Alg: keys.AlgP256, Pub: keys.B64(d.Box.PublicKey().Bytes())}, Auth: keys.B64(d.Auth)}
	}
	return keys.DeviceKeysNew{Sign: keys.KeySpec{Alg: keys.AlgEd25519, Pub: keys.B64(d.Sign.Public().(ed25519.PublicKey))},
		Box: keys.KeySpec{Alg: keys.AlgX25519, Pub: keys.B64(d.Box.PublicKey().Bytes())}}
}

// Last is the highest processed seq.
func (d *Device) Last() int64 { d.mu.Lock(); defer d.mu.Unlock(); return d.last }

// SetLast sets `last` (a test of resync or replay).
func (d *Device) SetLast(n int64) { d.mu.Lock(); d.last = n; d.mu.Unlock() }

// Hello answers a challenge, after checking it against the pinned key.
func (d *Device) Hello(c link.Challenge) (link.Hello, error) {
	if err := link.CheckChallenge(c, d.HubPub); err != nil || c.Hub != d.HubFP {
		return link.Hello{}, fmt.Errorf("challenge: %w", link.ErrSignature)
	}
	client := link.Client{Kind: "agent", Wy: "test", OS: "linux"}
	if !d.NoCaps {
		client.Caps = []string{link.CapPeerService}
	}
	if d.Kind == "phone" {
		client = link.Client{Kind: "android", App: "test", SDK: 35}
	}
	if d.Kind != "phone" {
		return link.SignHelloEd25519(d.Sign, c, d.ID, d.Last(), client, d.Known), nil
	}
	last := d.Last()
	h := sha256.Sum256(link.HelloInput(c.Hub, c.Nonce, c.Exp, d.ID, last))
	sig, err := ecdsa.SignASN1(rand.Reader, d.PSign, h[:])
	if err != nil {
		return link.Hello{}, err
	}
	return link.Hello{T: link.TypeHello, V: link.Version, Device: d.ID, Nonce: c.Nonce, Exp: c.Exp, Last: last, Client: client,
		Sig: keys.B64(sig)}, nil
}

// Msg is one checked frame.
type Msg struct {
	Frame link.Frame
	Body  []byte // the opened body (queued) or the signed JSON (ephemeral)
}

// Process checks a frame (link.md 4.3): the signature, seq against the type, a duplicate,
// then opens a queued body and moves `last`. nonce is the login's nonce, for ephemeral frames.
func (d *Device) Process(f link.Frame, nonce string) (Msg, error) {
	body, err := link.Check(f, d.HubPub, d.ID, nonce)
	if err != nil {
		return Msg{Frame: f}, err
	}
	if !link.IsQueued(f.T, f.Seq) {
		return Msg{Frame: f, Body: body}, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if f.Seq <= d.last {
		return Msg{Frame: f}, ErrDup
	}
	var pt []byte
	if d.Kind == "phone" {
		pt, err = DecryptRFC8291(d.Box, d.Auth, body)
	} else {
		pt, err = link.OpenMachine(d.Box, d.ID, f.T, body)
	}
	if err != nil {
		return Msg{Frame: f}, link.ErrOpen
	}
	d.last = f.Seq
	return Msg{Frame: f, Body: pt}, nil
}

// DecryptRFC8291 opens an aes128gcm Web Push body (RFC 8291, RFC 8188, one record).
func DecryptRFC8291(ua *ecdh.PrivateKey, authSecret, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, link.ErrOpen
	}
	salt, rs, idLen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if idLen != 65 || len(body) < 21+idLen || rs < 18 {
		return nil, link.ErrOpen
	}
	asPubB, ct := body[21:21+idLen], body[21+idLen:]
	asPub, err := ecdh.P256().NewPublicKey(asPubB)
	if err != nil {
		return nil, link.ErrOpen
	}
	secret, err := ua.ECDH(asPub)
	if err != nil {
		return nil, link.ErrOpen
	}
	info := append(append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...), asPubB...)
	prkKey := hmacSum(authSecret, secret)
	ikm := hmacSum(prkKey, append(info, 1))
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	g, _ := cipher.NewGCM(blk)
	pt, err := g.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, link.ErrOpen
	}
	i := len(pt) - 1
	for i >= 0 && pt[i] == 0 {
		i--
	}
	if i < 0 || pt[i] != 2 {
		return nil, link.ErrOpen
	}
	return pt[:i], nil
}

func hmacSum(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// ---- WebSocket ----

// Conn is a logged-in WebSocket.
type Conn struct {
	D       *Device
	WS      *websocket.Conn
	Nonce   string
	Welcome link.Welcome
}

// Dial connects to url (ws://…/v1/link), checks the challenge, logs in and reads the
// welcome.
func Dial(ctx context.Context, url string, d *Device) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{link.Subprotocol}})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(link.MaxMessage)
	var ch link.Challenge
	if err := readJSON(ctx, ws, &ch); err != nil {
		ws.CloseNow()
		return nil, err
	}
	h, err := d.Hello(ch)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	c := &Conn{D: d, WS: ws, Nonce: ch.Nonce}
	if err := c.Send(ctx, h); err != nil {
		ws.CloseNow()
		return nil, err
	}
	var f link.Frame
	if err := readJSON(ctx, ws, &f); err != nil {
		ws.CloseNow()
		return nil, err
	}
	m, err := d.Process(f, c.Nonce)
	if err != nil || f.T != link.TypeWelcome {
		ws.CloseNow()
		return nil, fmt.Errorf("welcome: %v (%s)", err, f.T)
	}
	if err := json.Unmarshal(m.Body, &c.Welcome); err != nil {
		ws.CloseNow()
		return nil, err
	}
	return c, nil
}

func readJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	_, b, err := ws.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Send writes one device frame.
func (c *Conn) Send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WS.Write(ctx, websocket.MessageText, b)
}

// Next reads the next frame unchecked (an error carries the close status).
func (c *Conn) Next(ctx context.Context) (link.Frame, error) {
	var f link.Frame
	err := readJSON(ctx, c.WS, &f)
	return f, err
}

// Recv reads and processes the next frame. A frame that fails its checks is rejected
// (link.md 5.6) and returned with the error; a duplicate is dropped silently.
func (c *Conn) Recv(ctx context.Context) (Msg, error) {
	for {
		f, err := c.Next(ctx)
		if err != nil {
			return Msg{}, err
		}
		m, err := c.D.Process(f, c.Nonce)
		if errors.Is(err, ErrDup) {
			continue
		}
		if err != nil && link.IsQueued(f.T, f.Seq) && f.Seq > 0 {
			_ = c.Send(ctx, map[string]any{"t": link.TypeReject, "seq": f.Seq})
		}
		return m, err
	}
}

// Ack sends a cumulative ack of `last`.
func (c *Conn) Ack(ctx context.Context) error {
	return c.Send(ctx, map[string]any{"t": link.TypeAck, "seq": c.D.Last()})
}

// Close closes with 1000.
func (c *Conn) Close() { _ = c.WS.Close(websocket.StatusNormalClosure, "") }

// CloseCode is the close status of a read error (-1 when it is not a close).
func CloseCode(err error) int { return int(websocket.CloseStatus(err)) }

// ---- long-poll (link.md 2.1) ----

// Poller is the long-poll path.
type Poller struct {
	D    *Device
	URL  string // http://…/v1/link/poll
	HTTP *http.Client
	next *link.Challenge
}

// PollReply is a checked poll reply.
type PollReply struct {
	Status  int
	Welcome link.Welcome
	Frames  []Msg
	Errs    []error // per frame, nil when it passed
	Results []Msg
}

func (p *Poller) post(ctx context.Context, body any, out any) (int, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := p.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, json.Unmarshal(rb, out)
}

// Poll makes one poll: a challenge when it has none, then a hello with `up`. Frames are
// processed (rejects go in the next poll's up through Rejects).
func (p *Poller) Poll(ctx context.Context, up ...any) (PollReply, error) {
	if p.next == nil {
		var ch link.Challenge
		st, err := p.post(ctx, map[string]string{"op": "challenge"}, &ch)
		if err != nil || st != http.StatusOK {
			return PollReply{Status: st}, err
		}
		p.next = &ch
	}
	h, err := p.D.Hello(*p.next)
	if err != nil {
		return PollReply{}, err
	}
	nonce := p.next.Nonce
	p.next = nil
	var raw struct {
		Welcome link.Frame     `json:"welcome"`
		Frames  []link.Frame   `json:"frames"`
		Results []link.Frame   `json:"results"`
		Next    link.Challenge `json:"next"`
	}
	req := map[string]any{"op": "poll", "hello": h}
	if len(up) > 0 {
		req["up"] = up
	}
	st, err := p.post(ctx, req, &raw)
	r := PollReply{Status: st}
	if err != nil || st != http.StatusOK {
		return r, err
	}
	if raw.Next.Nonce != "" {
		p.next = &raw.Next
	}
	w, err := p.D.Process(raw.Welcome, nonce)
	if err != nil || raw.Welcome.T != link.TypeWelcome {
		return r, fmt.Errorf("welcome: %v", err)
	}
	_ = json.Unmarshal(w.Body, &r.Welcome)
	for _, f := range raw.Frames {
		m, err := p.D.Process(f, nonce)
		if errors.Is(err, ErrDup) {
			continue
		}
		r.Frames, r.Errs = append(r.Frames, m), append(r.Errs, err)
	}
	for _, f := range raw.Results {
		m, err := p.D.Process(f, nonce)
		if err != nil {
			return r, fmt.Errorf("result %s: %w", f.T, err)
		}
		r.Results = append(r.Results, m)
	}
	return r, nil
}

// Rejects lists reject frames for the frames of r that failed their checks.
func (r PollReply) Rejects() []any {
	var out []any
	for i, e := range r.Errs {
		if e != nil && r.Frames[i].Frame.Seq > 0 {
			out = append(out, map[string]any{"t": link.TypeReject, "seq": r.Frames[i].Frame.Seq})
		}
	}
	return out
}

// WSURL turns http://host into ws://host/v1/link.
func WSURL(base string) string {
	return "ws" + strings.TrimPrefix(strings.TrimSuffix(base, "/"), "http") + "/v1/link"
}
