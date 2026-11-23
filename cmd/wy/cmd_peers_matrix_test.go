//go:build fakeinbox && peerstest

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/keys"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
)

// fakeProcs is a fake /proc for the signing guard.
type fakeProcs map[int]keys.Proc

func (f fakeProcs) Read(pid int) (keys.Proc, error) {
	if p, ok := f[pid]; ok {
		return p, nil
	}
	return keys.Proc{}, errors.New("gone")
}

// underClaude is the real guard with a fake process tree in which a Claude Code process is
// an ancestor of this command.
func underClaude() error {
	return keys.Guard{
		GOOS: "linux", Getppid: func() int { return 100 }, HasEnv: func(string) bool { return false },
		Procs:   fakeProcs{100: {PPid: 90, Comm: "bash", Exe: "/usr/bin/bash"}, 90: {PPid: 1, Comm: "claude", Exe: "/x/claude/versions/2.1.300"}},
		OpenTTY: func() error { return nil },
	}.Check()
}

// matrixWorld is the keys rig (a real hub, an owner key, a connected machine) sharing its
// state folder with a peers world (two accounts, two fake inboxes in a temp runtime dir).
type matrixWorld struct {
	t   *testing.T
	rig *keysRig
	w   *peersWorld
}

func newMatrixWorld(t *testing.T) *matrixWorld {
	t.Helper()
	rig := newKeysRig(t)
	fp := rig.initOwner()
	if _, err := rig.ks.Confirm(context.Background(), fp, "cli"); err != nil {
		t.Fatal(err)
	}
	w := newPeersWorldAt(t, rig.env.state)
	w.proc(601, 600, "bash", "/usr/bin/bash", "CLAUDECODE=1", 5300) // the worker session's shell
	old := peersKeysEnv
	t.Cleanup(func() { peersKeysEnv = old })
	peersKeysEnv = func(o, e io.Writer) (keysEnv, error) {
		env := rig.env
		env.stdout, env.stderr = o, e
		return env, nil
	}
	return &matrixWorld{t: t, rig: rig, w: w}
}

// wy runs `wy peers …` with the terminal answering answers (passphrase prompts).
func (m *matrixWorld) wy(answers []string, args ...string) (code int, out, errs string) {
	m.t.Helper()
	m.rig.tty.answers = answers
	code, out, errs = runPeersWy(append([]string{"peers"}, args...)...)
	if len(m.rig.tty.answers) != 0 {
		m.t.Errorf("%v: unused answers %q", args, m.rig.tty.answers)
	}
	return
}

func (m *matrixWorld) hubMatrix() keys.MatrixBody {
	m.t.Helper()
	rec, err := m.rig.ks.MatrixCurrent(context.Background())
	if err != nil {
		m.t.Fatal(err)
	}
	return rec.Signed.Body
}

// api is the work session (account work, pid 500); worker the personal one (pid 600).
func (m *matrixWorld) asAPI()    { m.w.startPID = 501 }
func (m *matrixWorld) asWorker() { m.w.startPID = 601 }

func TestPeersMatrixEndToEnd(t *testing.T) {
	m := newMatrixWorld(t)
	ctx := context.Background()

	// No matrix on this machine yet: every send is refused.
	code, out, _ := m.wy(nil, "send", "worker", "hello one")
	if code != 5 || !strings.Contains(out, "matrix is not configured") {
		t.Fatalf("before any matrix: %d %q", code, out)
	}

	// `wy peers matrix` fetches version 1 (hub-signed, nothing allowed) and prints it.
	code, out, errs := m.wy(nil, "matrix")
	if code != 0 {
		t.Fatalf("matrix: %d %q %q", code, out, errs)
	}
	for _, want := range []string{`from \ to`, "1 personal", "2 work", "matrix v1 · signed by hub ", "same account: on", "replies without you: no limit", "not paused"} {
		if !strings.Contains(out, want) {
			t.Errorf("matrix print lacks %q:\n%s", want, out)
		}
	}
	if code, out, _ := m.wy(nil, "send", "worker", "hello one"); code != 5 || !strings.Contains(out, "does not allow work → personal") {
		t.Fatalf("baseline denies: %d %q", code, out)
	}

	// allow: owner key, passphrase from the terminal. A wrong one changes nothing.
	if code, _, errs := m.wy([]string{"y", "not the passphrase"}, "allow", "work", "personal"); code == 0 || !strings.Contains(errs, "wrong passphrase") {
		t.Fatalf("wrong passphrase: %d %q", code, errs)
	}
	if v := m.hubMatrix().Version; v != 1 {
		t.Fatalf("a wrong passphrase changed the matrix: v%d", v)
	}
	if code, out, errs := m.wy([]string{pass1}, "allow", "work", "personal", "--yes"); code != 0 {
		t.Fatalf("allow: %d %q %q", code, out, errs)
	}
	rec, _ := m.rig.ks.MatrixCurrent(ctx)
	if rec.Signed.Signer != keys.ByOwner || rec.Signed.Body.Version != 2 || !rec.Widens {
		t.Fatalf("the allow must be an owner-signed widening: %+v", rec)
	}
	// ... and the stored copy on this machine is the one the evaluator reads.
	if s, err := keys.LoadAcceptedMatrix(m.rig.env.state); err != nil || s == nil || s.Body.Version != 2 {
		t.Fatalf("stored matrix: %v %v", s, err)
	}

	// allow -> send delivers; the reverse direction is still refused.
	m.asAPI()
	if code, out, _ := m.wy(nil, "send", "worker", "hello two"); code != 0 || !strings.HasPrefix(out, "delivered to worker@personal.laptop") {
		t.Fatalf("send after allow: %d %q", code, out)
	}
	if !m.w.b.Wait(1, 2*time.Second) {
		t.Fatal("nothing reached the worker's inbox")
	}
	m.asWorker()
	// One way: the worker may reply in the thread api opened, but not start another one.
	if code, out, _ := m.wy(nil, "send", "api", "thanks, done"); code != 0 {
		t.Fatalf("reply in the open thread: %d %q", code, out)
	}
	if !m.w.a.Wait(1, 2*time.Second) {
		t.Fatal("the reply did not arrive")
	}

	// deny narrows (hub-signed, no passphrase) and stops the replies too: the opener's cell is gone.
	if code, out, errs := m.wy(nil, "deny", "work", "personal"); code != 0 || !strings.Contains(out, "signed by the hub") {
		t.Fatalf("deny: %d %q %q", code, out, errs)
	}
	if code, out, _ := m.wy(nil, "send", "api", "one more"); code != 5 || !strings.Contains(out, "does not allow personal → work") {
		t.Fatalf("reply after the opener's cell was denied: %d %q", code, out)
	}
	m.asAPI()
	if code, out, _ := m.wy(nil, "send", "worker", "again"); code != 5 || !strings.Contains(out, "does not allow work → personal") {
		t.Fatalf("send after deny: %d %q", code, out)
	}
	if rec, _ := m.rig.ks.MatrixCurrent(ctx); rec.Signed.Signer != keys.ByHub || rec.Widens || rec.Signed.Body.Version != 3 {
		t.Fatalf("deny must be a hub-signed narrowing: %+v", rec)
	}

	// Both ways, then the kill switch stops everything, same-account traffic included.
	if code, _, errs := m.wy([]string{pass1}, "allow", "work", "personal", "--both", "--yes"); code != 0 {
		t.Fatalf("allow both: %d %q", code, errs)
	}
	m.asWorker()
	if code, out, _ := m.wy(nil, "send", "api", "worker starts a thread"); code != 0 {
		t.Fatalf("both ways: %d %q", code, out)
	}
	if code, out, errs := m.wy(nil, "pause"); code != 0 || !strings.Contains(out, "pause all peers") {
		t.Fatalf("pause: %d %q %q", code, out, errs)
	}
	for _, as := range []func(){m.asAPI, m.asWorker} {
		as()
		to := map[bool]string{true: "worker", false: "api"}[m.w.startPID == 501]
		if code, out, _ := m.wy(nil, "send", to, "while paused"); code != 5 || !strings.Contains(out, "paused") {
			t.Fatalf("paused: %d %q", code, out)
		}
	}
	// Resume widens: the owner key again.
	if code, _, _ := m.wy([]string{pass1}, "resume", "--yes"); code != 0 {
		t.Fatal("resume")
	}
	if m.hubMatrix().Paused {
		t.Fatal("still paused at the hub")
	}
	m.asAPI()
	if code, out, _ := m.wy(nil, "send", "worker", "after resume"); code != 0 {
		t.Fatalf("after resume: %d %q", code, out)
	}

	// The 7.3 print with both ways and the diagonal.
	_, out, _ = m.wy(nil, "matrix")
	if !strings.Contains(out, "⇄") || !strings.Contains(out, "=") || !strings.Contains(out, "matrix v6 · signed by owner ") {
		t.Fatalf("matrix print:\n%s", out)
	}
	_, out, _ = m.wy(nil, "matrix", "--json")
	var js struct {
		Version  int        `json:"version"`
		Signer   string     `json:"signer"`
		Accounts []peerAcct `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(out), &js); err != nil || js.Version != 6 || js.Signer != "owner" || len(js.Accounts) != 2 {
		t.Fatalf("matrix --json: %v %q", err, out)
	}

	// status and audit: ids, sizes and statuses; never a word of the messages.
	_, out, _ = m.wy(nil, "status")
	if !strings.Contains(out, "delivered") || !strings.Contains(out, "matrix v6") {
		t.Errorf("status:\n%s", out)
	}
	code, aout, errs := m.wy(nil, "audit", "--json")
	if code != 0 {
		t.Fatalf("audit: %d %q", code, errs)
	}
	rows, err := m.rig.ks.Store.PeerAudit(ctx, 0, 100)
	if err != nil || len(rows) == 0 {
		t.Fatalf("hub audit rows: %d %v", len(rows), err)
	}
	hubRows, _ := json.Marshal(rows)
	for _, text := range []string{"hello", "thanks", "worker starts", "while paused", "after resume", "one more"} {
		if strings.Contains(aout, text) || strings.Contains(string(hubRows), text) {
			t.Errorf("audit holds message text %q", text)
		}
	}
	if !strings.Contains(aout, `"status": "delivered"`) {
		t.Errorf("local audit lacks the delivered messages:\n%s", aout)
	}
}

func TestPeersWideningRefusedUnderClaude(t *testing.T) {
	m := newMatrixWorld(t)
	if code, _, _ := m.wy(nil, "matrix"); code != 0 {
		t.Fatal("matrix")
	}
	m.rig.env.guard = underClaude
	m.rig.env.tty = func() (keysTTY, error) { t.Fatal("the terminal was opened under Claude Code"); return nil, nil }
	before := m.hubMatrix().Version
	for _, args := range [][]string{
		{"allow", "work", "personal", "--yes"}, {"resume", "--yes"},
	} {
		code, _, errs := m.wy(nil, args...)
		if code != 4 || !strings.Contains(errs, "refusing to sign") {
			t.Errorf("%v: %d %q", args, code, errs)
		}
	}
	if m.hubMatrix().Version != before {
		t.Fatal("a refused widening reached the hub")
	}
	// Narrowing stays open to a session: deny, pause, lower limits (version 1 has no turn
	// limit, so any number narrows).
	if code, _, errs := m.wy(nil, "max-turns", "10"); code != 0 {
		t.Fatalf("lowering max turns under Claude: %d %q", code, errs)
	}
	before = m.hubMatrix().Version
	for _, n := range []string{"40", "none", "0"} {
		if code, _, errs := m.wy(nil, "max-turns", n); code != 4 || !strings.Contains(errs, "refusing to sign") {
			t.Errorf("max-turns %s under Claude: %d %q", n, code, errs)
		}
	}
	if m.hubMatrix().Version != before {
		t.Fatal("a refused raise reached the hub")
	}
	for _, n := range []string{"1", "501", "-3", "lots"} {
		if code, _, _ := m.wy(nil, "max-turns", n); code != 2 {
			t.Errorf("max-turns %s: %d, want usage", n, code)
		}
	}
	if code, _, errs := m.wy(nil, "pause"); code != 0 {
		t.Fatalf("pause under Claude: %d %q", code, errs)
	}
	if b := m.hubMatrix(); !b.Paused || b.MaxTurns != 10 {
		t.Fatalf("%+v", b)
	}
	// ... but resuming and raising the limit again are widenings.
	if code, _, _ := m.wy(nil, "resume"); code != 4 {
		t.Fatalf("resume under Claude: %d", code)
	}
	if code, _, _ := m.wy(nil, "same-account", "off"); code != 0 {
		t.Fatal("same-account off narrows")
	}
	if code, _, _ := m.wy(nil, "same-account", "on"); code != 4 {
		t.Fatalf("same-account on under Claude: %d", code)
	}
}

func TestPeersNoOwnerKeyPointsToTheBoard(t *testing.T) {
	m := newMatrixWorld(t)
	m.wy(nil, "matrix")
	if err := os.Remove(keys.Path(m.rig.env.state, keys.OwnerSealedFile)); err != nil {
		t.Fatal(err)
	}
	code, _, errs := m.wy(nil, "allow", "work", "personal")
	if code != 4 || !strings.Contains(errs, "More, Peers on the board") {
		t.Fatalf("%d %q", code, errs)
	}
}

func TestPeersHubMayWidenFalse(t *testing.T) {
	m := newMatrixWorld(t)
	ctx := context.Background()
	if code, _, _ := m.wy(nil, "matrix"); code != 0 {
		t.Fatal("matrix")
	}
	// An owner session on the board widens: the hub signs it.

	rec, err := m.rig.ks.EditMatrix(ctx, hubkeys.MatrixEdit{Allow: [][]string{{"acc-work", "acc-personal"}}}, "board", true)
	if err != nil || rec.Signed.Signer != keys.ByHub || !rec.Widens {
		t.Fatalf("board widening: %+v %v", rec, err)
	}
	// A machine with hub_may_widen = false keeps its old matrix and says why.
	m.w.noHubWiden = true
	code, out, errs := m.wy(nil, "matrix")
	if code != 0 || !strings.Contains(out, "matrix v1") || !strings.Contains(errs, "hub_may_widen") {
		t.Fatalf("hub_may_widen=false: %d\n%s\n%q", code, out, errs)
	}
	m.asAPI()
	if code, out, _ := m.wy(nil, "send", "worker", "x"); code != 5 {
		t.Fatalf("a refused widening must not allow anything: %d %q", code, out)
	}
	// With the default it takes the document.
	m.w.noHubWiden = false
	code, out, _ = m.wy(nil, "matrix")
	if code != 0 || !strings.Contains(out, "matrix v2 · signed by hub") {
		t.Fatalf("default: %d\n%s", code, out)
	}
	if code, out, _ := m.wy(nil, "send", "worker", "x"); code != 0 {
		t.Fatalf("send with the hub's widening: %d %q", code, out)
	}
}

func TestPeersAccountNames(t *testing.T) {
	list := peerAccounts(map[string]string{"u-w": "work", "u-p": "personal", "u-l": "lab"}, map[string]string{"u-x": "other"}, nil)
	for ref, want := range map[string]string{"work": "u-w", "WORK": "u-w", "1": "u-l", "2": "u-x", "u-p": "u-p", "*": "*"} {
		if got, err := resolveAccount(list, ref, true); err != nil || got != want {
			t.Errorf("%q: %q %v (list %+v)", ref, got, err, list)
		}
	}
	if _, err := resolveAccount(list, "nobody", true); err == nil || !strings.Contains(err.Error(), "known:") {
		t.Errorf("unknown account: %v", err)
	}
	if _, err := resolveAccount(list, "*", false); err == nil {
		t.Error("'*' is for deny only")
	}
}

// `wy peers audit` lists a trusted sender's sends from this machine's log (peers.md 7.1, 10):
// by name with the tag in the table, with from_kind, tag and detail in --json.
func TestPeersAuditListsServiceSends(t *testing.T) {
	m := newMatrixWorld(t)
	now := time.Now().UTC()
	lines := []map[string]any{
		{"ts": now.Add(-2 * time.Minute).Format(time.RFC3339Nano), "msg": "m-sess", "thread": "t_1", "from": "sess-a", "to": "sess-b",
			"from_account": "acc-work", "to_account": "acc-personal", "size": 5, "local": true, "status": "delivered"},
		{"ts": now.Add(-time.Minute).Format(time.RFC3339Nano), "msg": "m-svc", "thread": "t_2", "from": peerspkg.ServiceSessionID("m_T"),
			"to": "sess-b", "from_account": "acc-work", "to_account": "acc-personal", "size": 7, "local": true, "status": "delivered",
			"from_kind": peerspkg.ServiceDoorbell, "tag": "tohands-slack-manager"},
		{"ts": now.Format(time.RFC3339Nano), "msg": "m-held", "from": peerspkg.ServiceSessionID("m_R"), "to": "sess-b",
			"from_account": "acc-work", "to_account": "acc-personal", "size": 7, "local": false, "status": "rejected", "detail": "held",
			"from_kind": peerspkg.ServiceDoorbell, "tag": "ci"},
	}
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	peersWriteFile(t, filepath.Join(m.w.state, "peers", "audit.jsonl"), b.String())

	code, out, errs := m.wy(nil, "audit")
	if code != 0 {
		t.Fatalf("audit: %d %q", code, errs)
	}
	local := out[strings.Index(out, "THIS MACHINE"):]
	for _, want := range []string{"doorbell (service) tag:tohands-slack-manager → sess-b", "rejected (held)",
		"doorbell (service) tag:ci → sess-b", "sess-a → sess-b"} {
		if !strings.Contains(local, want) {
			t.Errorf("table lacks %q:\n%s", want, local)
		}
	}
	code, out, errs = m.wy(nil, "audit", "--json")
	if code != 0 {
		t.Fatalf("audit --json: %d %q", code, errs)
	}
	var js struct {
		Local []map[string]any `json:"local"`
	}
	if err := json.Unmarshal([]byte(out), &js); err != nil || len(js.Local) != 3 {
		t.Fatalf("json: %v %q", err, out)
	}
	svc := js.Local[1] // newest first
	if svc["msg"] != "m-svc" || svc["from_kind"] != peerspkg.ServiceDoorbell || svc["tag"] != "tohands-slack-manager" {
		t.Errorf("service line: %v", svc)
	}
	if js.Local[0]["detail"] != "held" {
		t.Errorf("held line: %v", js.Local[0])
	}
	if _, ok := js.Local[2]["from_kind"]; ok {
		t.Errorf("a session's line has from_kind: %v", js.Local[2])
	}
}
