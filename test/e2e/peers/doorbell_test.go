//go:build fakeinbox

package peers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/peers/fakeinbox"
)

// The trusted sender, Doorbell (peers.md 4.1a), across a hub and two machines: approval on a
// machine, its report to the hub, tag sends, and the owner's revocation.

// svcEnv is the machine's Env as the agent builds it for a request: with the link's Remote.
func (m *machine) svcEnv() peers.Env {
	e := m.env()
	e.Remote = m.remote
	return e
}

// doorbellInstall writes a fake agent-doorbell virtual environment on the machine (a venv with
// the distribution, a console script and an interpreter) and returns the script's path.
func (m *machine) doorbellInstall() string {
	m.t.Helper()
	venv := filepath.Join(m.base, "venv")
	if _, err := os.Stat(filepath.Join(venv, "bin", "doorbell")); err == nil {
		return filepath.Join(venv, "bin", "doorbell")
	}
	real := filepath.Join(m.base, "py", "bin", "python3.12")
	m.write(real, "#!fake interpreter\n")
	if err := os.Chmod(real, 0o755); err != nil {
		m.t.Fatal(err)
	}
	m.write(filepath.Join(venv, "pyvenv.cfg"), "home = "+filepath.Dir(real)+"\n")
	m.write(filepath.Join(venv, "lib", "python3.12", "site-packages", "agent_doorbell-0.4.1.dist-info", "METADATA"),
		"Metadata-Version: 2.1\nName: agent-doorbell\nVersion: 0.4.1\n")
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(venv, "bin", "python")); err != nil {
		m.t.Fatal(err)
	}
	script := filepath.Join(venv, "bin", "doorbell")
	m.write(script, "#!"+filepath.Join(venv, "bin", "python")+"\nimport sys\nfrom doorbell.cli import main\nsys.exit(main())\n")
	if err := os.Chmod(script, 0o755); err != nil {
		m.t.Fatal(err)
	}
	return script
}

// approve is the owner approving Doorbell on the machine as account (what the agent's trust
// op does), without reporting it.
func (m *machine) approve(account string) peers.TrustRecord {
	m.t.Helper()
	rec, err := m.svcEnv().ApproveDoorbell(m.doorbellInstall(), account)
	if err != nil {
		m.t.Fatal(err)
	}
	return rec
}

// approveAndReport approves and reports the record to the hub, and waits for the hub's row.
func (m *machine) approveAndReport(account string) peers.TrustRecord {
	m.t.Helper()
	rec := m.approve(account)
	if err := m.remote.ReportTrust(); err != nil {
		m.t.Fatal(err)
	}
	m.h.waitTrust(m.id, func(r store.PeerTrustRow) bool { return r.Active() && r.Account == account })
	return rec
}

// waitTrust waits until the hub's peer_trust row of the machine satisfies ok.
func (h *testHub) waitTrust(machine string, ok func(store.PeerTrustRow) bool) store.PeerTrustRow {
	h.t.Helper()
	var last store.PeerTrustRow
	if !eventually(10*time.Second, func() bool {
		rows, err := h.st.MachinePeerTrust(h.ctx, machine)
		if err != nil {
			h.t.Fatal(err)
		}
		if len(rows) != 1 {
			return false
		}
		last = rows[0]
		return ok(last)
	}) {
		h.t.Fatalf("peer_trust row of %s: %+v", machine, last)
	}
	return last
}

// tag gives the machine's session a tag at the hub.
func (m *machine) tag(session, tag string) {
	m.t.Helper()
	ans, err := m.remote.Tag(m.session(session).id, peers.TagAdd, tag)
	if err != nil || ans.Error != "" {
		m.t.Fatalf("tag %s on %s: %+v %v", tag, session, ans, err)
	}
}

// ring is one Doorbell send to tag as rec on the machine.
func (m *machine) ring(rec peers.TrustRecord, tag, text string) ([]peers.ServiceLine, *peers.Refusal) {
	m.t.Helper()
	return m.svcEnv().SendService(rec, "tag:"+tag, text, false, false, peers.SendOpts{})
}

// trustedRows counts the hub's message rows from the hub holds a message row from a trusted sender.
func (h *testHub) trustedRows() (n int) {
	for _, r := range h.rows() {
		if r.FromKind == peers.ServiceDoorbell {
			n++
		}
	}
	return n
}

// Doorbell on the laptop sends to a tag a session on the worker holds: delivered with the
// service header, the hub records the sender kind and the tag, and holds no text.
func TestDoorbellSendsToTag(t *testing.T) {
	w := newWorld(t, 30)
	const text = "marmot-heron-5521 deploy finished"
	w.worker.tag("builder", "ci")
	rec := w.laptop.approveAndReport(accWork)

	line := w.laptop.ring1(t, rec, "ci", text)
	deliveredLine(t, line)
	got := onlyContent(t, w.worker.session("builder").inbox, 1)
	// Doorbell always prompts, so the bypass-mode target holds the message for its owner's
	// approval (peers.md 8.5); the send still counts as delivered.
	if got[0].Outcome != fakeinbox.Hold || got[0].Reason != "mode_mismatch" {
		t.Fatalf("outcome %s (%s)", got[0].Outcome, got[0].Reason)
	}
	body := got[0].Wrapper.Body
	header := "[Watchyard] from doorbell (service) on laptop · account work · tag ci · thread " + line.Res.Thread
	if !strings.HasPrefix(body, header) || !strings.Contains(body, text) {
		t.Errorf("body lacks the service header %q:\n%s", header, body)
	}
	if got[0].Wrapper.FromSession != peers.ServiceSessionID(laptopID) {
		t.Errorf("wrapper from-session %q", got[0].Wrapper.FromSession)
	}
	row := w.h.waitRow(line.Res.Msg, "delivered")
	if row.FromKind != peers.ServiceDoorbell || row.ViaTag != "ci" || row.FromAcct != accWork || row.ToAcct != accPersonal ||
		row.FromSession != peers.ServiceSessionID(laptopID) || row.FromDevice != w.laptop.device {
		t.Errorf("hub row: %+v", row)
	}
	w.h.assertNoText(text, "marmot-heron")

	// The hub's directory answer for the laptop carries the active trust row.
	rows, err := w.h.st.MachinePeerTrust(w.h.ctx, laptopID)
	if err != nil || len(rows) != 1 || !rows[0].Active() || rows[0].Venv != rec.Venv || rows[0].Program != rec.Program ||
		rows[0].Interpreter != rec.Interpreter {
		t.Errorf("trust rows: %+v %v", rows, err)
	}
	// Use shows on the board's side: the hub counts the send against the machine.
	use, err := w.h.st.PeerTrustUse(w.h.ctx, laptopID, peers.ServiceDoorbell, 0, 0)
	if err != nil || use.Since != 1 || len(use.Tags) != 1 || use.Tags[0].Tag != "ci" {
		t.Errorf("trust use: %+v %v", use, err)
	}
}

// A target that does not hold the tag at the hub when the envelope arrives is refused there,
// even though the sender saw it as a holder in its directory.
func TestDoorbellTargetLostTagRefusedAtHub(t *testing.T) {
	w := newWorld(t, 30)
	in := w.worker.session("builder").inbox
	w.worker.tag("builder", "ci")
	rec := w.laptop.approveAndReport(accWork)

	// The tag goes away between the sender's directory read and the hub's check.
	w.h.setRogue(func(ctx context.Context, device string, u link.Up) (bool, string, any, error) {
		if u.T == link.TypePeerMsg {
			err := w.h.st.Write(ctx, func(tx *store.Tx) error {
				_, err := tx.RemovePeerTag("sess-builder", "ci")
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}
		return false, "", nil, nil
	})
	line := w.laptop.ring1(t, rec, "ci", "to a session that lost its tag")
	hubRefused(t, line, peers.CodeUnknownTarget)
	noMore(t, in, 0)
	row := w.h.newestRow()
	if row.Detail != peers.CodeUnknownTarget || row.FromKind != peers.ServiceDoorbell || row.ViaTag != "ci" {
		t.Errorf("hub row: %+v", row)
	}

}

// Without an active trust row at the hub, a service envelope is refused (forbidden), whether
// the machine never reported its approval or the owner revoked it; the machine's agent then
// drops its record on its next fresh directory read.
func TestDoorbellNeedsActiveTrust(t *testing.T) {
	w := newWorld(t, 30)
	in := w.worker.session("builder").inbox
	w.worker.tag("builder", "ci")

	// 1. Approved at the machine, never reported (the hub drops the report): no row.
	w.h.setRogue(func(_ context.Context, _ string, u link.Up) (bool, string, any, error) {
		return u.T == link.TypePeerTrust, "", nil, nil
	})
	rec := w.laptop.approve(accWork)
	line := w.laptop.ring1(t, rec, "ci", "nothing reported yet")
	hubRefused(t, line, "forbidden")
	noMore(t, in, 0)
	if rows, _ := w.h.st.MachinePeerTrust(w.h.ctx, laptopID); len(rows) != 0 {
		t.Fatalf("trust rows %+v", rows)
	}
	// Once the hub takes reports again, the agent's next directory read sees the hub lacks the
	// row and reports the record itself, ahead of the envelope on the same link.
	w.h.setRogue(nil)
	line = w.laptop.ring1(t, rec, "ci", "now reported")
	deliveredLine(t, line)
	onlyContent(t, in, 1)

	// 2. The owner revokes at the hub: refused at once, even with the machine's record in place.
	if _, err := w.h.st.RevokePeerTrustOwner(w.h.ctx, laptopID, peers.TrustDoorbell, "dev-owner"); err != nil {
		t.Fatal(err)
	}
	// With the 5 s directory cache in force the machine has not heard yet: its record stays,
	// and the hub refuses the envelope.
	w.laptop.remote.Fresh = 0
	if _, err := w.laptop.remote.Sessions(); err != nil { // fills the cache after the revocation
		t.Fatal(err)
	}
	if r, err := w.laptop.svcEnv().LoadTrust(); err != nil || r == nil {
		t.Fatalf("record after the revocation was already read: %v %v", r, err)
	}
	w.h.assertRevoked(laptopID, "dev-owner")
	line = w.laptop.ring1(t, rec, "ci", "after the revocation")
	hubRefused(t, line, "forbidden")
	noMore(t, in, 1)

	// The cache runs out: the next fresh read removes the record.
	w.h.clk.Advance(6 * time.Second)
	if _, err := w.laptop.remote.Sessions(); err != nil {
		t.Fatal(err)
	}
	if r, err := w.laptop.svcEnv().LoadTrust(); err != nil || r != nil {
		t.Fatalf("record after a fresh read: %v %v", r, err)
	}
	if _, err := os.Stat(w.laptop.svcEnv().TrustPath()); !os.IsNotExist(err) {
		t.Errorf("trusted.json: %v", err)
	}
	w.laptop.waitAudit("", "trust_off")
	// The hub's row stays revoked: the agent did not re-report anything.
	time.Sleep(200 * time.Millisecond)
	if r := w.h.waitTrust(laptopID, func(store.PeerTrustRow) bool { return true }); r.Active() {
		t.Errorf("row active again: %+v", r)
	}

	// 3. The owner approves again at the terminal: a newer approval replaces the revocation.
	w.h.clk.Advance(2 * time.Second)
	rec = w.laptop.approveAndReport(accWork)
	line = w.laptop.ring1(t, rec, "ci", "approved again")
	deliveredLine(t, line)
	onlyContent(t, in, 2)
}

func (h *testHub) assertRevoked(machine, by string) {
	h.t.Helper()
	rows, err := h.st.MachinePeerTrust(h.ctx, machine)
	if err != nil || len(rows) != 1 || rows[0].Active() || rows[0].RevokedBy != by {
		h.t.Fatalf("trust rows: %+v %v", rows, err)
	}
}

// A report whose account differs from the account the envelope claims: the hub holds the
// reported account, so an envelope for the account in the machine's record is refused.
func TestDoorbellAccountMismatchRefusedAtHub(t *testing.T) {
	w := newWorld(t, 30)
	in := w.worker.session("builder").inbox
	w.worker.tag("builder", "ci")
	rec := w.laptop.approve(accWork)

	// The machine reports another account than the one in its record.
	up := peers.PeerTrustUp{T: link.TypePeerTrust, Op: "set", Sender: peers.TrustDoorbell, Account: accPersonal, Program: rec.Program,
		Interpreter: rec.Interpreter, Venv: rec.Venv, ApprovedAt: rec.ApprovedAt.UTC().Format(time.RFC3339)}
	if err := w.laptop.lc.Send(context.Background(), up); err != nil {
		t.Fatal(err)
	}
	w.h.waitTrust(laptopID, func(r store.PeerTrustRow) bool { return r.Active() && r.Account == accPersonal })

	line := w.laptop.ring1(t, rec, "ci", "claims work, hub holds personal")
	hubRefused(t, line, "forbidden")
	noMore(t, in, 0)
	if n := w.h.trustedRows(); n != 0 {
		t.Errorf("%d trusted rows for a refused sender", n)
	}
}

// The account checked is the recorded one: a Doorbell approved as an account the matrix does
// not let start a thread with the target's account reaches nobody, at the sender and at the
// hub when the sender's matrix is wrong.
func TestDoorbellMatrixUsesRecordedAccount(t *testing.T) {
	w := newWorld(t, 30)
	apiIn := w.laptop.session("api").inbox
	builderIn := w.worker.session("builder").inbox
	w.laptop.tag("api", "ci")
	w.worker.tag("builder", "ci")

	// On the worker, approved as "other": it may start threads with personal (builder), not
	// with work (api).
	rec := w.worker.approveAndReport(accOther)
	line := w.worker.ring1(t, rec, "ci", "as other")
	if line.Refusal != nil || line.Target.Session != "sess-builder" {
		t.Fatalf("line: %+v %+v", line.Target, line.Refusal)
	}
	got := onlyContent(t, builderIn, 1)
	if !strings.HasPrefix(got[0].Wrapper.Body, "[Watchyard] from doorbell (service) on gpu-box · account other · tag ci") {
		t.Errorf("header:\n%s", got[0].Wrapper.Body)
	}
	if r := w.h.waitRow(line.Res.Msg, "delivered"); r.FromAcct != accOther || r.ToAcct != accPersonal || r.ViaTag != "ci" {
		t.Errorf("hub row: %+v", r)
	}
	noMore(t, apiIn, 0)

	// Only the session on the machine's account-denied side holds the tag: nobody to send to.
	if _, err := w.worker.remote.Tag("sess-builder", peers.TagRm, "ci"); err != nil {
		t.Fatal(err)
	}
	lines, rf := w.worker.ring(rec, "ci", "only api holds it now")
	if len(lines) != 0 {
		t.Fatalf("lines %+v", lines)
	}
	refusedWith(t, rf, peers.CodeNoHolder)
	noMore(t, apiIn, 0)

	// A machine whose matrix is wrong lets the envelope out: the hub applies the matrix to the
	// recorded account and refuses.
	w.worker.setMatrix(allowAll())
	line = w.worker.ring1(t, rec, "ci", "wrong matrix")
	refusedWith(t, line.Refusal, peers.CodeNotAllowed)
	row := w.h.newestRow()
	if row.Detail != peers.CodeNotAllowed || row.FromKind != peers.ServiceDoorbell || row.FromAcct != accOther {
		t.Errorf("hub row: %+v", row)
	}
	noMore(t, apiIn, 0)
}

// ring1 is ring when the send must reach exactly one holder.
func (m *machine) ring1(t *testing.T, rec peers.TrustRecord, tag, text string) peers.ServiceLine {
	t.Helper()
	lines, rf := m.ring(rec, tag, text)
	if rf != nil || len(lines) != 1 {
		t.Fatalf("send: %d lines, refusal %v", len(lines), rf)
	}
	return lines[0]
}

// deliveredLine waits for a line's message to be delivered (the hub answers queued first).
func deliveredLine(t *testing.T, line peers.ServiceLine) {
	t.Helper()
	if line.Refusal != nil {
		t.Fatalf("refused: %v", line.Refusal)
	}
	if line.Res.Status == "queued" && line.Res.Pending != nil {
		if f, ok := line.Res.Pending.Wait(10 * time.Second); !ok || f.Status != "delivered" {
			t.Fatalf("final status %+v (%v)", f, ok)
		}
		return
	}
	if line.Res.Status != "delivered" {
		t.Fatalf("status %q", line.Res.Status)
	}
}

// hubRefused checks the hub refused the line's envelope with reason.
func hubRefused(t *testing.T, line peers.ServiceLine, reason string) {
	t.Helper()
	if line.Refusal == nil || !strings.Contains(line.Refusal.Detail, reason) {
		t.Fatalf("line %+v / %v, want a hub refusal (%s)", line.Res.Status, line.Refusal, reason)
	}
}

// newestRow is the hub's newest peer_message row, which must be refused.
func (h *testHub) newestRow() store.PeerMessageRow {
	h.t.Helper()
	rows := h.rows()
	if len(rows) == 0 || rows[0].Status != "refused" {
		h.t.Fatalf("hub rows: %+v", rows)
	}
	return rows[0]
}
