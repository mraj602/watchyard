package peerlink

import (
	"time"

	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

// The trusted sender at the hub (peers.md 4.1a, link.md 6.2 peer_trust, 6.3 trust): this
// machine's record is the source of the approval, which only a terminal here can make; the
// hub holds a copy for the board and for checking trusted envelopes, and the owner's
// revocation, which comes back in every directory answer and is applied here like any other
// owner setting read from the hub.

// ReportTrust sends this machine's record to the hub (peer_trust set), or off when there is
// none. A failure is not fatal: the next directory answer shows the hub's view, and
// reconcileTrust reports again.
func (r *Remote) ReportTrust() error {
	rec, err := r.Env.LoadTrust()
	if err != nil {
		return err
	}
	up := peers.PeerTrustUp{T: link.TypePeerTrust, Op: "off", Sender: peers.TrustDoorbell}
	if rec != nil {
		up = peers.PeerTrustUp{T: link.TypePeerTrust, Op: "set", Sender: peers.TrustDoorbell, Account: rec.Account,
			Program: rec.Program, Interpreter: rec.Interpreter, Venv: rec.Venv, ApprovedAt: rec.ApprovedAt.UTC().Format(time.RFC3339), TestBuild: rec.TestBuild}
	}
	return r.send(up)
}

// reconcileTrust compares a fresh directory answer's trust rows with the local record: when
// the hub's row is revoked and its approval is this record's (or a newer one), the record is
// deleted (the owner revoked it on the board or the phone); a record the hub lacks, or holds
// an older approval of, is reported; a row the hub holds while this machine has no record is
// turned off there. Both approval times come from this machine's clock, so clock skew between
// the machine and the hub does not matter. An answer without a trust field means the hub holds
// no row (or is older than 1.3.0 and ignores the report): a local record is reported again.
func (r *Remote) reconcileTrust(rep *peers.PeersReply) {
	if rep == nil {
		return
	}
	rec, err := r.Env.LoadTrust()
	if err != nil {
		return
	}
	st, has := rep.Trust[peers.TrustDoorbell]
	switch {
	case rec == nil && has && st.RevokedAt == nil && rep.Trust != nil:
		_ = r.ReportTrust() // off
	case rec == nil:
	case has && st.RevokedAt != nil && !olderThan(st.ApprovedAt, rec.ApprovedAt):
		if ok, err := r.Env.RevokeDoorbell("hub"); err == nil && ok {
			r.Logger.Printf("trusted sender doorbell revoked at the hub (%s); record removed", *st.RevokedAt)
		}
	case !has || olderThan(st.ApprovedAt, rec.ApprovedAt):
		_ = r.ReportTrust()
	}
}

// olderThan reports whether the hub's approved_at is before the local one (second precision).
// An unreadable time is not older: a revocation with it applies, which is the narrowing side.
func olderThan(hub string, local time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, hub)
	return err == nil && t.Truncate(time.Second).Before(local.Truncate(time.Second))
}
