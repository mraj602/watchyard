package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/auth"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	hublink "github.com/mraj602/watchyard/internal/hub/link"
	"github.com/mraj602/watchyard/internal/hub/peerrelay"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

const resumeThread = "t_0123456789abcdef"

// resumeHub is a hub with the peer relay and the link, two machines with agent keys, a
// session on each and a thread between them the loop guard stopped.
type resumeHub struct {
	*keysHub
	machines [2]string // agent device ids
	signs    [2]ed25519.PrivateKey
	relay    *peerrelay.Relay
}

func newResumeHub(t *testing.T, relay bool) *resumeHub {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), store.Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	hk, _ := hubkeys.FromSeed(bytes.Repeat([]byte{1}, 32))
	ks := hubkeys.New(st, hk)
	testPepperPath(t)
	cfg := api.Config{Store: st, PingEvery: time.Hour, PublicURL: "https://hub.example.ts.net", Keys: ks}
	lk := hublink.New(hublink.Config{Store: st, Keys: ks})
	var relayOf *peerrelay.Relay
	if relay {
		rl := peerrelay.New(st, ks, nil, nil)
		rl.Link = lk
		cfg.PeerThreads = rl
		relayOf = rl
	}
	cfg.Link = lk
	srv := api.New(cfg)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { srv.Close(); hs.Close(); lk.Close(); st.Close() })
	h := &resumeHub{keysHub: &keysHub{t: t, st: st, ks: ks, url: hs.URL}, relay: relayOf}
	h.owner = ownerSessionToken(t, st).Token

	now := time.Now().UnixMilli()
	for i, m := range []string{"m_01JA2W5N0G8H3Q7S2V6X9Y1Z4A", "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4B"} {
		c, err := auth.Create(ctx, st, auth.CreateRequest{Kind: "machine", Name: m[len(m)-1:], Machine: m})
		if err != nil {
			t.Fatal(err)
		}
		h.machines[i] = c.Device.ID
		dk, err := keys.NewDeviceKeys(time.Now(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub, _ := dk.Public()
		h.signs[i], _ = dk.SignKey()
		sess := []string{"sess-a", "sess-b"}[i]
		acct := []string{"acct-a", "acct-b"}[i]
		if err := st.Write(ctx, func(tx *store.Tx) error {
			if _, err := ks.RegisterTx(tx, c.Device, pub, keys.RegQR); err != nil {
				return err
			}
			seat, err := tx.UpsertSeat(store.SeatKey{Account: acct, Org: "org-1", Email: acct + "@example.test"}, now)
			if err != nil {
				return err
			}
			slot, err := tx.EnsureSlot(m, "~/.claude", now)
			if err != nil {
				return err
			}
			return tx.InsertSession(store.SessionRow{ID: sess, SeatID: seat, MachineID: m, SlotID: slot, Cwd: "/src",
				StartedAt: now, LastEventAt: now})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Write(ctx, func(tx *store.Tx) error {
		return tx.PutPeerThread(store.PeerThreadRow{ID: resumeThread, A: "sess-a", B: "sess-b", AAccount: "acct-a",
			BAccount: "acct-b", OpenedAt: now, LastAt: now, Turns: 30, PausedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *resumeHub) thread() store.PeerThreadRow {
	h.t.Helper()
	var th store.PeerThreadRow
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		var err error
		th, err = tx.PeerThread(resumeThread)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return th
}

func (h *resumeHub) queued(device string) []store.LinkRow {
	h.t.Helper()
	var rows []store.LinkRow
	if err := h.st.Write(context.Background(), func(tx *store.Tx) error {
		var err error
		rows, err = tx.LinkQueued(device)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return rows
}

// "Resume thread" (peers.md 6.3): owner only; an unknown or malformed id is 404; the hub's
// loop guard is reset, an audit row written and peer_thread_resume queued to both machines'
// agents (collapsed by tag when asked twice); a closed thread is 409.
func TestPeerThreadResume(t *testing.T) {
	h := newResumeHub(t, true)
	ctx := context.Background()
	path := "/v1/peers/threads/" + resumeThread + "/resume"

	// Who may: an owner session only. A read-only board sign-in, a phone and a machine get 403.
	read, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "browser", Name: "board", CreatedBy: "login:dev_x", Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	phone, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "phone", Name: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	machine, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "machine", Name: "c", Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4C"})
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"read": read.Token, "phone": phone.Token, "machine": machine.Token} {
		if st, _ := h.call("POST", path, tok, nil); st != 403 {
			t.Errorf("%s: %d, want 403", name, st)
		}
	}
	if st, _ := h.call("POST", path, "", nil); st != 401 {
		t.Errorf("no token: %d, want 401", st)
	}
	if th := h.thread(); th.Turns != 30 || th.PausedAt == 0 {
		t.Fatalf("a refused call changed the thread: %+v", th)
	}
	for _, d := range h.machines {
		if n := len(h.queued(d)); n != 0 {
			t.Fatalf("a refused call queued %d frames", n)
		}
	}

	// Unknown and malformed ids.
	for _, id := range []string{"t_ffffffffffffffff", "t_XYZ", "nope"} {
		if st, _ := h.call("POST", "/v1/peers/threads/"+id+"/resume", h.owner, nil); st != 404 {
			t.Errorf("%s: %d, want 404", id, st)
		}
	}

	// The owner resumes: the hub's guard resets and both agents are told.
	st, m := h.call("POST", path, h.owner, nil)
	if st != 200 || m["thread"] != resumeThread || m["was_paused"] != true || m["machines"] != float64(2) {
		t.Fatalf("resume: %d %v", st, m)
	}
	if th := h.thread(); th.Turns != 0 || th.PausedAt != 0 || th.ClosedAt != 0 {
		t.Fatalf("thread after resume: %+v", th)
	}
	for _, d := range h.machines {
		rows := h.queued(d)
		if len(rows) != 1 || rows[0].Type != link.TypePeerThreadResume || rows[0].Tag != "tr:"+resumeThread {
			t.Fatalf("%s queue: %+v", d, rows)
		}
	}
	entries, err := h.st.PeerAudit(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Event != "thread_resume" || entries[0].Thread != resumeThread {
		t.Fatalf("audit: %+v", entries)
	}
	var detail map[string]any
	_ = json.Unmarshal(entries[0].Detail, &detail)
	if detail["was_paused"] != true || detail["machines"] != float64(2) {
		t.Fatalf("audit detail %s", entries[0].Detail)
	}

	// Again: not paused any more; the queued frames collapse to one per machine.
	if st, m := h.call("POST", path, h.owner, nil); st != 200 || m["was_paused"] != false {
		t.Fatalf("second resume: %d %v", st, m)
	}
	for _, d := range h.machines {
		if n := len(h.queued(d)); n != 1 {
			t.Fatalf("%s: %d frames queued, want 1 (collapsed)", d, n)
		}
	}

	// A closed thread cannot be resumed.
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		th, err := tx.PeerThread(resumeThread)
		if err != nil {
			return err
		}
		th.ClosedAt = time.Now().UnixMilli()
		return tx.PutPeerThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.call("POST", path, h.owner, nil); st != 409 {
		t.Errorf("closed thread: %d, want 409", st)
	}
}

// A hub without the relay answers 503 rather than pretending.
func TestPeerThreadResumeWithoutRelay(t *testing.T) {
	h := newResumeHub(t, false)
	if st, _ := h.call("POST", "/v1/peers/threads/"+resumeThread+"/resume", h.owner, nil); st != 503 {
		t.Fatalf("%d, want 503", st)
	}
}

// A machine that refuses the resume (hub_may_widen off) reports it with a peer_log event
// (peers.md 6.3): only a machine of the thread may, and the hub then writes a
// thread_resume_refused audit row and marks its thread paused again, turns still 0.
func TestPeerThreadResumeRefusedReport(t *testing.T) {
	h := newResumeHub(t, true)
	ctx := context.Background()
	if st, m := h.call("POST", "/v1/peers/threads/"+resumeThread+"/resume", h.owner, nil); st != 200 {
		t.Fatalf("resume: %d %v", st, m)
	}
	up := func(device string, l peers.PeerLogUp) {
		t.Helper()
		l.T = link.TypePeerLog
		raw, _ := json.Marshal(l)
		if _, _, err := h.relay.Up(ctx, device, link.Up{T: link.TypePeerLog, Raw: raw}); err != nil {
			t.Fatal(err)
		}
	}
	report := func(device, thread, detail string) {
		t.Helper()
		up(device, peers.PeerLogUp{Event: peers.EventThreadResumeRefused, Thread: thread, Detail: detail,
			At: time.Now().UTC().Format(time.RFC3339Nano)})
	}
	refusals := func() []store.PeerAuditRow {
		t.Helper()
		rows, err := h.st.PeerAudit(ctx, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.PeerAuditRow
		for _, r := range rows {
			if r.Event == peers.EventThreadResumeRefused {
				out = append(out, r)
			}
		}
		return out
	}

	// A machine with agent keys but no session in the thread, an unknown thread, a malformed
	// id, an unknown reason and an unknown event: all dropped.
	outsider, err := auth.Create(ctx, h.st, auth.CreateRequest{Kind: "machine", Name: "c", Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4C"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		_, err := h.ks.RegisterTx(tx, outsider.Device, testMachineKeys(t), keys.RegQR)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	report(outsider.Device.ID, resumeThread, peers.ResumeRefusedReason)
	report(h.machines[0], "t_ffffffffffffffff", peers.ResumeRefusedReason)
	report(h.machines[0], "../threads", peers.ResumeRefusedReason)
	report(h.machines[0], resumeThread, "because")
	up(h.machines[0], peers.PeerLogUp{Event: "thread_bogus", Thread: resumeThread, Detail: peers.ResumeRefusedReason})
	if got := refusals(); len(got) != 0 {
		t.Fatalf("forged reports were recorded: %+v", got)
	}
	if th := h.thread(); th.PausedAt != 0 || th.Turns != 0 {
		t.Fatalf("a forged report changed the thread: %+v", th)
	}

	// Machine B of the thread refuses: one audit row, actor its device, detail its machine; the
	// hub's thread reads paused again, turns still 0.
	report(h.machines[1], resumeThread, peers.ResumeRefusedReason)
	got := refusals()
	if len(got) != 1 || got[0].Actor != h.machines[1] || got[0].Thread != resumeThread {
		t.Fatalf("refusal audit: %+v", got)
	}
	var detail map[string]any
	_ = json.Unmarshal(got[0].Detail, &detail)
	if m, _ := detail["machine"].(string); m == "" || len(detail) != 1 {
		t.Fatalf("refusal detail %s", got[0].Detail)
	}
	if th := h.thread(); th.PausedAt == 0 || th.Turns != 0 {
		t.Fatalf("thread after the refusal: %+v", th)
	}
	// A second resume finds the thread paused: a machine still held the stop.
	if st, m := h.call("POST", "/v1/peers/threads/"+resumeThread+"/resume", h.owner, nil); st != 200 || m["was_paused"] != true {
		t.Fatalf("resume after refusal: %d %v", st, m)
	}
}
