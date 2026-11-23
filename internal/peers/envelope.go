package peers

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
)

// TTLs of a remote message (peers.md 9.3).
const (
	DefaultTTL = time.Hour
	MinTTL     = time.Minute
	MaxTTL     = 24 * time.Hour
)

// EnvFrom is the sending side of an envelope (peers.md 8.2).
type EnvFrom struct {
	Session string `json:"session"`
	Device  string `json:"device"`
	Machine string `json:"machine"`
	Account string `json:"account"`
	// Label is the sender's account label, for the receiver's header line only.
	Label   string `json:"label,omitempty"`
	Name    string `json:"name"`
	Handle  string `json:"handle"`
	Mode    Mode   `json:"mode"`
	HumanAt string `json:"human_at,omitempty"`
	// Kind is "service:doorbell" for the trusted sender (peers.md 4.1a, 8.2); absent for a
	// session.
	Kind string `json:"kind,omitempty"`
}

// EnvTo is the receiving side of an envelope.
type EnvTo struct {
	Session string `json:"session"`
	Device  string `json:"device"`
	Machine string `json:"machine"`
	Account string `json:"account"`
}

// Envelope is the body of a peer_envelope document (peers.md 8.2), signed by the sending
// machine's device key. Sealed holds the text, sealed to the receiving machine: the hub
// relays it and cannot read it.
type Envelope struct {
	V         int     `json:"v"`
	Msg       string  `json:"msg"`
	Thread    string  `json:"thread"`
	Kind      Kind    `json:"kind"`
	From      EnvFrom `json:"from"`
	To        EnvTo   `json:"to"`
	Hop       string  `json:"hop"`
	CreatedAt string  `json:"created_at"`
	Exp       int64   `json:"exp"`
	Size      int     `json:"size"`
	Sealed    string  `json:"sealed"`
	// Tag is the tag a trusted send went to (peers.md 4.1a, 8.2); absent otherwise.
	Tag string `json:"tag,omitempty"`
}

// Service reports whether the envelope is from the trusted sender (peers.md 4.1a).
func (e Envelope) Service() bool { return e.From.Kind != "" }

// PeerSealInfo is HPKE's info for a peer body: "wy-peer-v1\n" ‖ msg ‖ "\n" ‖ from.device ‖
// "\n" ‖ to.device (peers.md 8.2). The label must not change: machines that are
// not updated yet open these bodies.
func PeerSealInfo(msg, fromDevice, toDevice string) []byte {
	return []byte("wy-peer-v1\n" + msg + "\n" + fromDevice + "\n" + toDevice)
}

type sealedText struct {
	Text string `json:"text"`
}

// Reason words of an envelope that fails its checks (peers.md 8.4 step 1).
const (
	ReasonBadSignature = "bad_signature"
	ReasonUncertified  = "uncertified"
	ReasonRevoked      = "revoked"
	ReasonWrongDevice  = "wrong_device"
	ReasonExpired      = "expired"
	// ReasonUnboundAccount: the sender's account is not one the owner bound to the sending
	// machine (keys.md 4.3, peers.md 5.3).
	ReasonUnboundAccount = "unbound_account"
)

// EnvelopeError is a failed envelope or certificate check with its reason word.
type EnvelopeError struct {
	Reason string
	Err    error
}

func (e *EnvelopeError) Error() string { return e.Reason + ": " + e.Err.Error() }
func (e *EnvelopeError) Unwrap() error { return e.Err }

func envErr(reason string, format string, a ...any) error {
	return &EnvelopeError{Reason: reason, Err: fmt.Errorf(format, a...)}
}

// ReasonOf is the reason word of an error from this file (bad_signature for anything else).
func ReasonOf(err error) string {
	var ee *EnvelopeError
	if errors.As(err, &ee) {
		return ee.Reason
	}
	return ReasonBadSignature
}

func certReason(err error) string {
	switch {
	case errors.Is(err, keys.ErrUncertified):
		return ReasonUncertified
	}
	return ReasonBadSignature
}

// revoked reports whether either of a certificate's keys is listed, or its device is listed
// without keys. The device id alone is not enough: a live tofu device is listed with the keys
// it replaced (keys.md 4.2 step 3, 6).
func revoked(c keys.Cert, rev *keys.RevocationBody) bool {
	if rev == nil {
		return false
	}
	if rev.RevokedKey(c.Body.SignFP()) || rev.RevokedKey(c.Body.BoxFP()) {
		return true
	}
	return slices.ContainsFunc(rev.Devices, func(d keys.RevokedDevice) bool {
		return d.Device == c.Body.Device && len(d.Keys) == 0
	})
}

func machineKind(kind string) bool { return kind == "machine" || kind == "desktop" }

// CheckMachineCert checks a peer machine's certificate as both ends do (peers.md 8.2, 8.4 step
// 1): hub and owner signatures with this machine's pins, a machine (or desktop) certificate
// for device, not revoked.
func CheckMachineCert(d keys.Doc, t keys.Trust, rev *keys.RevocationBody, device string) (keys.Cert, error) {
	if t.Owner == nil {
		return keys.Cert{}, envErr(ReasonUncertified, "this machine pins no owner key, so it cannot check owner signatures")
	}
	c, err := keys.VerifyCert(d, t, true)
	if err != nil {
		return keys.Cert{}, envErr(certReason(err), "certificate: %v", err)
	}
	if !machineKind(c.Body.Kind) || c.Body.Sign.Alg != keys.AlgEd25519 || c.Body.Box.Alg != keys.AlgX25519 {
		return keys.Cert{}, envErr(ReasonBadSignature, "certificate of a %s, not a machine", c.Body.Kind)
	}
	if c.Body.Device != device {
		return keys.Cert{}, envErr(ReasonBadSignature, "certificate is for %s, not %s", c.Body.Device, device)
	}
	if revoked(c, rev) {
		return keys.Cert{}, envErr(ReasonRevoked, "device %s is revoked", device)
	}
	return c, nil
}

// SealEnvelope seals text to the target machine's certified box key and signs the envelope
// with this machine's device key. The caller has checked target with CheckMachineCert (the
// sender never seals to an uncertified key). It fills Size and Sealed.
func SealEnvelope(e Envelope, text string, target keys.Cert, sign ed25519.PrivateKey) (keys.Doc, error) {
	if target.Body.Device != e.To.Device || !target.OwnerSigned {
		return keys.Doc{}, errors.New("peers: the target certificate does not match the envelope")
	}
	pt, err := json.Marshal(sealedText{Text: text})
	if err != nil {
		return keys.Doc{}, err
	}
	sealed, err := link.SealHPKE(target.Body.BoxPub(), PeerSealInfo(e.Msg, e.From.Device, e.To.Device), pt)
	if err != nil {
		return keys.Doc{}, err
	}
	e.V, e.Size, e.Sealed = 1, len(text), keys.B64(sealed)
	body, err := json.Marshal(e)
	if err != nil {
		return keys.Doc{}, err
	}
	d := keys.NewDoc(keys.TypePeerEnvelope, body)
	return d, d.Sign(keys.ByDevice, sign)
}

// ParseEnvelope decodes verified envelope bytes and checks its shape.
func ParseEnvelope(raw []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return Envelope{}, err
	}
	if e.V != 1 || !safeID.MatchString(e.Msg) || !ValidThreadID(e.Thread) || (e.Kind != KindStart && e.Kind != KindReply) ||
		e.From.Session == "" || e.From.Device == "" || e.To.Session == "" || e.To.Device == "" || e.Sealed == "" ||
		e.From.Session == e.To.Session || !safeID.MatchString(e.From.Session) || !safeID.MatchString(e.To.Session) {
		return Envelope{}, errors.New("malformed envelope")
	}
	if e.From.Kind != "" || e.Tag != "" {
		// The trusted sender (peers.md 4.1a, 8.2): its own pseudo-session on its machine, never
		// bypass, never a reply, always to a tag. Any other kind is not understood.
		if e.From.Kind != ServiceDoorbell || e.From.Session != ServiceSessionID(e.From.Machine) || e.From.Mode != ModePrompting ||
			e.Kind != KindStart || !ValidTag(e.Tag) || e.From.HumanAt != "" {
			return Envelope{}, errors.New("malformed trusted-sender envelope")
		}
	}
	created, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
	if err != nil {
		return Envelope{}, errors.New("envelope created_at")
	}
	// peers.md 8.2: exp is at most created_at + 24 h.
	if e.Exp > created.Add(MaxTTL).Unix() {
		return Envelope{}, errors.New("envelope exp is more than 24 h after created_at")
	}
	return e, nil
}

// VerifyEnvelope is the signature half of the checks: the envelope is signed by sender's
// certified key and names that certificate's device and machine. It returns the body.
func VerifyEnvelope(doc keys.Doc, sender keys.Cert) (Envelope, error) {
	raw, err := keys.VerifyPeerEnvelope(doc, sender)
	if err != nil {
		return Envelope{}, envErr(ReasonBadSignature, "envelope: %v", err)
	}
	e, err := ParseEnvelope(raw)
	if err != nil {
		return Envelope{}, envErr(ReasonBadSignature, "%v", err)
	}
	if e.From.Device != sender.Body.Device || e.From.Machine != sender.Body.MachineID() {
		return Envelope{}, envErr(ReasonBadSignature, "envelope from %s/%s, certificate of %s/%s", e.From.Device, e.From.Machine,
			sender.Body.Device, sender.Body.MachineID())
	}
	return e, nil
}

// CheckSenderAccount is the account half of peers.md 8.4 step 1: the sender's
// machine_accounts document is signed by the pinned owner key for the sender's certificate,
// and lists the account the envelope claims. Anything else is unbound_account: the matrix may
// only be decided on an account the owner let that machine speak for.
func CheckSenderAccount(d *keys.Doc, t keys.Trust, sender keys.Cert, account string) error {
	if d == nil {
		return envErr(ReasonUnboundAccount, "the hub sent no machine_accounts for %s", sender.Body.Device)
	}
	b, err := keys.VerifyAccounts(*d, t, sender)
	if err != nil {
		return envErr(ReasonUnboundAccount, "machine_accounts: %v", err)
	}
	if !b.Has(account) {
		return envErr(ReasonUnboundAccount, "account %q is not bound to machine %s", account, sender.Body.MachineID())
	}
	return nil
}

// ClockSkew is how far ahead of the receiver's clock an envelope's exp may be beyond MaxTTL
// (peers.md 8.4 step 1). It keeps every acceptable exp inside seen.json's replay window.
const ClockSkew = time.Hour

// CheckIncoming is peers.md 8.4 step 1 on the receiving machine: the sender's certificate
// (hub and owner signatures with this machine's pins, machine kind, not revoked), the envelope
// signature by its key, the device and machine it names, to.device = self, exp not passed and
// not more than MaxTTL + ClockSkew ahead.
func CheckIncoming(doc, cert keys.Doc, t keys.Trust, rev *keys.RevocationBody, self string, now time.Time) (Envelope, keys.Cert, error) {
	// The certificate names the device it is for; which device that must be comes from the
	// envelope, read only after the signature verifies.
	var claimed keys.CertBody
	if raw, err := cert.BodyBytes(); err == nil {
		_ = json.Unmarshal(raw, &claimed)
	}
	c, err := CheckMachineCert(cert, t, rev, claimed.Device)
	if err != nil {
		return Envelope{}, keys.Cert{}, err
	}
	e, err := VerifyEnvelope(doc, c)
	if err != nil {
		return Envelope{}, keys.Cert{}, err
	}
	if e.To.Device != self {
		return Envelope{}, keys.Cert{}, envErr(ReasonWrongDevice, "envelope is for %s, this device is %s", e.To.Device, self)
	}
	if now.Unix() > e.Exp {
		return Envelope{}, keys.Cert{}, envErr(ReasonExpired, "expired at %s", time.Unix(e.Exp, 0).UTC().Format(time.RFC3339))
	}
	// A replayed message is caught by its id only while seen.json remembers it (48 h): an exp
	// further ahead would outlive that window.
	if e.Exp > now.Add(MaxTTL+ClockSkew).Unix() {
		return Envelope{}, keys.Cert{}, envErr(ReasonBadSignature, "exp %s is too far ahead of this machine's clock",
			time.Unix(e.Exp, 0).UTC().Format(time.RFC3339))
	}
	return e, c, nil
}

// Open decrypts the sealed text with this machine's box key.
func (e Envelope) Open(box *ecdh.PrivateKey) (string, error) {
	sealed, err := keys.UnB64(e.Sealed)
	if err != nil {
		return "", err
	}
	pt, err := link.OpenHPKE(box, PeerSealInfo(e.Msg, e.From.Device, e.To.Device), sealed)
	if err != nil {
		return "", err
	}
	var s sealedText
	if err := json.Unmarshal(pt, &s); err != nil {
		return "", err
	}
	return s.Text, nil
}

// HopTokens splits the envelope's hop chain (comma-separated 24-hex tokens), keeping valid
// tokens only, the last MaxHopTokens.
func HopTokens(hop string) []string { return CutHop(strings.Split(hop, ",")) }
