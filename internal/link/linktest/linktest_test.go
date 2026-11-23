package linktest

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
)

// The test phone opens the shared alert-phone vector (RFC 8291 with the appendix's keys)
// exactly as the Android module must.
func TestPhoneOpensSharedVector(t *testing.T) {
	b, err := os.ReadFile("../testdata/alert-phone.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Keys   map[string]string `json:"keys"`
		Fields map[string]any    `json:"fields"`
		Frame  link.Frame        `json:"frame"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	ua, _ := keys.UnB64(v.Keys["ua_private"])
	auth, _ := keys.UnB64(v.Keys["auth"])
	hub, _ := keys.UnB64(v.Keys["hub"])
	priv, err := ecdh.P256().NewPrivateKey(ua)
	if err != nil {
		t.Fatal(err)
	}
	d := &Device{ID: v.Keys["device"], Kind: "phone", Box: priv, Auth: auth}
	d.Pin(ed25519.PublicKey(hub))
	m, err := d.Process(v.Frame, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Body) != v.Fields["plaintext"].(string) || d.Last() != 57 {
		t.Fatalf("opened %q, last %d", m.Body, d.Last())
	}
	if _, err := d.Process(v.Frame, ""); err != ErrDup {
		t.Fatalf("replay: %v", err)
	}
	body, _ := hex.DecodeString(v.Fields["payload_hex"].(string))
	body[len(body)-1] ^= 1
	if _, err := DecryptRFC8291(priv, auth, body); err == nil {
		t.Fatal("tampered body opened")
	}
}

type hubSigner struct{ k ed25519.PrivateKey }

func (s hubSigner) SignLink(in []byte) ([]byte, error) { return ed25519.Sign(s.k, in), nil }

// A test machine says it takes a trusted sender's message unless NoCaps models an old agent.
func TestMachineHelloCaps(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	for _, noCaps := range []bool{false, true} {
		d := NewMachine(7)
		d.ID, d.NoCaps = "dev_x", noCaps
		d.Pin(pub)
		c, err := link.NewChallenge(hubSigner{priv}, d.HubFP, keys.B64(make([]byte, 32)), 4102444800)
		if err != nil {
			t.Fatal(err)
		}
		h, err := d.Hello(c)
		if err != nil {
			t.Fatal(err)
		}
		if has := len(h.Client.Caps) == 1 && h.Client.Caps[0] == link.CapPeerService; has == noCaps {
			t.Errorf("NoCaps %v: caps %v", noCaps, h.Client.Caps)
		}
	}
}
