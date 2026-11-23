package link

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mraj602/watchyard/internal/keys"
)

// Frame is a hub→device frame after login (link.md 4.1). Body and Sig are b64u: Body is the
// sealed body of a queued frame, or the exact signed JSON of an ephemeral one.
type Frame struct {
	T    string `json:"t"`
	Seq  int64  `json:"seq"`
	Exp  int64  `json:"exp"`
	Tag  string `json:"tag"`
	Body string `json:"body"`
	Sig  string `json:"sig"`
}

// Challenge is the hub's first message (link.md 3.1).
type Challenge struct {
	T     string `json:"t"`
	V     int    `json:"v"`
	Hub   string `json:"hub"`
	Nonce string `json:"nonce"`
	Exp   int64  `json:"exp"`
	Sig   string `json:"sig"`
}

// Client is hello.client (link.md 3.2): what the device runs. PingS is the device's own
// ping interval, which the hub uses for its idle timeout (2 × ping_s + 30).
type Client struct {
	Kind  string `json:"kind"` // android | agent
	App   string `json:"app,omitempty"`
	Build string `json:"build,omitempty"`
	SDK   int    `json:"sdk,omitempty"`
	Wy    string `json:"wy,omitempty"`
	OS    string `json:"os,omitempty"`
	PingS int    `json:"ping_s,omitempty"`
	// Caps are what the device can take beyond its version (link.md 3.2); unknown words are
	// ignored.
	Caps []string `json:"caps,omitempty"`
}

// CapPeerService: the agent understands a trusted sender's peer_msg (from.kind, peers.md 4.1a,
// 8.3); the hub relays one only to an agent whose last hello carried it.
const CapPeerService = "peer_service"

// Known is hello.known: the newest matrix and revocation versions a machine holds.
type Known struct {
	Matrix      int `json:"matrix"`
	Revocations int `json:"revocations"`
}

// Hello is the device's login (link.md 3.2).
type Hello struct {
	T      string `json:"t"`
	V      int    `json:"v"`
	Device string `json:"device"`
	Nonce  string `json:"nonce"`
	Exp    int64  `json:"exp"`
	Last   int64  `json:"last"`
	Client Client `json:"client"`
	Known  *Known `json:"known,omitempty"`
	Sig    string `json:"sig"`
}

// Welcome is the body of the welcome frame (link.md 3.3).
type Welcome struct {
	V          int       `json:"v"`
	Device     string    `json:"device"`
	Next       int64     `json:"next"`
	PingS      int       `json:"ping_s"`
	URLs       []string  `json:"urls"`
	Owner      *keys.Doc `json:"owner"`
	ServerTime string    `json:"server_time"`
}

// ActionResult is the body of action_result (link.md 6.3).
type ActionResult struct {
	V       int    `json:"v"`
	ID      string `json:"id"`
	Status  int    `json:"status"`
	Applied bool   `json:"applied"`
	Alert   any    `json:"alert"`
}

// Notice is the body of notice (link.md 6.3).
type Notice struct {
	V       int    `json:"v"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Notice codes.
const (
	NoticeUnknownType = "unknown_type"
	NoticeForbidden   = "forbidden"
	NoticeRateLimited = "rate_limited"
	NoticeQueueFull   = "queue_full"
	NoticeDraining    = "draining"
)

// Up is a device→hub frame (link.md 6.2): fields inline, no signature. Raw keeps the whole
// object for the frame types whose handler is elsewhere (the peer frames).
type Up struct {
	T     string          `json:"t"`
	Seq   int64           `json:"seq,omitempty"`
	ID    string          `json:"id,omitempty"`
	Token string          `json:"token,omitempty"`
	Raw   json.RawMessage `json:"-"`
}

// ParseUp reads a device→hub frame.
func ParseUp(b []byte) (Up, error) {
	var u Up
	if err := json.Unmarshal(b, &u); err != nil || u.T == "" {
		return Up{}, ErrFrame
	}
	u.Raw = append(json.RawMessage(nil), b...)
	return u, nil
}

func decimal(n int64) string { return strconv.FormatInt(n, 10) }

func lines(parts ...string) []byte {
	var b []byte
	for i, p := range parts {
		if i > 0 {
			b = append(b, '\n')
		}
		b = append(b, p...)
	}
	return b
}

// The "wy-link-v1" labels below are part of the protocol: devices and hubs sign and verify
// them, so changing them would break every link and its test vectors.

// ChallengeInput is what the challenge signature covers (link.md 3.1).
func ChallengeInput(hubFP, nonce string, exp int64) []byte {
	return lines("wy-link-v1 challenge", hubFP, nonce, decimal(exp))
}

// HelloInput is what the hello signature covers (link.md 3.2).
func HelloInput(hubFP, nonce string, exp int64, device string, last int64) []byte {
	return lines("wy-link-v1 hello", hubFP, nonce, decimal(exp), device, decimal(last))
}

// FrameInput is what a hub frame signature covers (link.md 4.3).
func FrameInput(device, t string, seq, exp int64, tag, ctx string, payload []byte) []byte {
	b := lines("wy-link-v1", device, t, decimal(seq), decimal(exp), tag, ctx, "")
	return append(b, payload...)
}

// Signer makes hub signatures over wy-link-v1 inputs (hubkeys.HubKey.SignLink).
type Signer interface {
	SignLink(input []byte) ([]byte, error)
}

// NewChallenge signs a challenge.
func NewChallenge(s Signer, hubFP, nonce string, exp int64) (Challenge, error) {
	sig, err := s.SignLink(ChallengeInput(hubFP, nonce, exp))
	if err != nil {
		return Challenge{}, err
	}
	return Challenge{T: TypeChallenge, V: Version, Hub: hubFP, Nonce: nonce, Exp: exp, Sig: keys.B64(sig)}, nil
}

// CheckChallenge verifies a challenge against the pinned hub key (link.md 3.1). The device
// never answers one that fails.
func CheckChallenge(c Challenge, hub ed25519.PublicKey) error {
	if c.T != TypeChallenge || c.V != Version {
		return ErrFrame
	}
	if c.Hub != keys.Fingerprint(hub) {
		return fmt.Errorf("%w: challenge from hub %s, pinned %s", ErrSignature, keys.GroupFP(c.Hub), keys.GroupFP(keys.Fingerprint(hub)))
	}
	n, err := keys.UnB64(c.Nonce)
	if err != nil || len(n) != 32 || c.Exp < 0 {
		return ErrFrame
	}
	sig, err := keys.UnB64(c.Sig)
	if err != nil || !ed25519.Verify(hub, ChallengeInput(c.Hub, c.Nonce, c.Exp), sig) {
		return ErrSignature
	}
	return nil
}

// SignHelloEd25519 makes a machine's hello for challenge c.
func SignHelloEd25519(priv ed25519.PrivateKey, c Challenge, device string, last int64, client Client, known *Known) Hello {
	sig := ed25519.Sign(priv, HelloInput(c.Hub, c.Nonce, c.Exp, device, last))
	return Hello{T: TypeHello, V: Version, Device: device, Nonce: c.Nonce, Exp: c.Exp, Last: last, Client: client, Known: known,
		Sig: keys.B64(sig)}
}

// VerifyHello checks the hello signature with the device's sign key from its verified
// certificate: Ed25519 (machines) or ECDSA P-256 SHA-256 DER (phones; keys.md 2).
func VerifyHello(h Hello, hubFP, alg string, pub []byte) error {
	if h.T != TypeHello || h.Last < 0 {
		return ErrFrame
	}
	sig, err := keys.UnB64(h.Sig)
	if err != nil {
		return ErrSignature
	}
	in := HelloInput(hubFP, h.Nonce, h.Exp, h.Device, h.Last)
	switch alg {
	case keys.AlgEd25519:
		if len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, in, sig) {
			return nil
		}
	case keys.AlgP256:
		k, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
		if err != nil {
			return ErrSignature
		}
		d := sha256.Sum256(in)
		if ecdsa.VerifyASN1(k, d[:], sig) {
			return nil
		}
	}
	return ErrSignature
}

// SignFrame makes a hub frame (link.md 4.3). ctx is CtxQueue for a queued frame, the login
// nonce for an ephemeral one; payload is the sealed body or the ephemeral JSON.
func SignFrame(s Signer, device, t string, seq, exp int64, tag, ctx string, payload []byte) (Frame, error) {
	sig, err := s.SignLink(FrameInput(device, t, seq, exp, tag, ctx, payload))
	if err != nil {
		return Frame{}, err
	}
	return Frame{T: t, Seq: seq, Exp: exp, Tag: tag, Body: keys.B64(payload), Sig: keys.B64(sig)}, nil
}

// Ephemeral signs an ephemeral frame (seq 0) with body v as JSON, for the login nonce.
func EphemeralFrame(s Signer, device, t, nonce string, exp int64, v any) (Frame, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Frame{}, err
	}
	return SignFrame(s, device, t, 0, exp, "", nonce, b)
}

// Check is link.md 4.3 steps 1 and 2 on the device: the signature over its own device id
// (context CtxQueue for a queued type, the login nonce otherwise), then seq = 0 for an
// ephemeral type and > 0 for a queued one. It returns the decoded body. Steps 3 to 5
// (duplicates, expiry, opening) are the client's.
func Check(f Frame, hub ed25519.PublicKey, device, nonce string) ([]byte, error) {
	body, err := keys.UnB64(f.Body)
	if err != nil {
		return nil, ErrFrame
	}
	sig, err := keys.UnB64(f.Sig)
	if err != nil || f.T == "" || !ValidTag(f.Tag) {
		return nil, ErrFrame
	}
	queued := IsQueued(f.T, f.Seq)
	ctx := CtxQueue
	if !queued {
		ctx = nonce
	}
	if !ed25519.Verify(hub, FrameInput(device, f.T, f.Seq, f.Exp, f.Tag, ctx, body), sig) {
		return nil, ErrSignature
	}
	if queued != (f.Seq > 0) {
		return nil, ErrSeq
	}
	return body, nil
}
