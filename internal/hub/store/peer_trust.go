package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// The trusted sender at the hub (schema v21, peers.md 4.1a, 10): one peer_trust row per
// machine and sender (only "doorbell"). The machine's agent reports its record (link.md 6.2
// peer_trust); the owner's revocation (POST /v1/peers/trusted/revoke) sets revoked_at, which
// the agent reads back in the directory answer and the relay checks for every trusted envelope.

// PeerTrustRow is one peer_trust row, with the machine's name.
type PeerTrustRow struct {
	Machine, MachineName, Sender, Device string
	Account, Program, Interpreter, Venv  string
	ApprovedAt, RevokedAt, UpdatedAt     int64
	RevokedBy                            string
	// TestBuild: a trusttest build made the record (peers.md 10); the board says so.
	TestBuild bool
}

// Active reports whether the row allows trusted sends: approved and not revoked since.
func (r PeerTrustRow) Active() bool { return r.ApprovedAt > 0 && r.RevokedAt == 0 }

const peerTrustSelect = `SELECT t.machine_id, coalesce(m.name, ''), t.sender, t.device_id, t.account, t.program, t.interpreter, t.venv,
	t.approved_at, coalesce(t.revoked_at, 0), t.updated_at, coalesce(t.revoked_by, ''), t.test_build
	FROM peer_trust t LEFT JOIN machine m ON m.id = t.machine_id`

func scanPeerTrust(sc interface{ Scan(...any) error }) (PeerTrustRow, error) {
	var r PeerTrustRow
	var tb int
	err := sc.Scan(&r.Machine, &r.MachineName, &r.Sender, &r.Device, &r.Account, &r.Program, &r.Interpreter, &r.Venv,
		&r.ApprovedAt, &r.RevokedAt, &r.UpdatedAt, &r.RevokedBy, &tb)
	r.TestBuild = tb != 0
	return r, err
}

// PeerTrust reads one machine's row for sender; ok false when there is none.
func (tx *Tx) PeerTrust(machine, sender string) (PeerTrustRow, bool, error) {
	r, err := scanPeerTrust(tx.queryRow(peerTrustSelect+` WHERE t.machine_id = ? AND t.sender = ?`, machine, sender))
	if errors.Is(err, sql.ErrNoRows) {
		return PeerTrustRow{}, false, nil
	}
	return r, err == nil, err
}

// PutPeerTrust stores a machine's report of its record. Only a strictly newer approval (by the
// machine's own clock, which made both) replaces the row and clears a revocation; a report of
// the same approval refreshes nothing once revoked, and an older one is ignored (changed
// false). So a late report never undoes the owner's revocation; a new approval at the
// machine's terminal does. The hub's own clock is never compared with the machine's.
func (tx *Tx) PutPeerTrust(r PeerTrustRow, nowMS int64) (changed bool, err error) {
	old, ok, err := tx.PeerTrust(r.Machine, r.Sender)
	if err != nil {
		return false, err
	}
	if ok && (r.ApprovedAt < old.ApprovedAt || (old.RevokedAt != 0 && r.ApprovedAt == old.ApprovedAt)) {
		return false, nil
	}
	if ok && r.ApprovedAt == old.ApprovedAt && old.RevokedAt == 0 && r.Account == old.Account && r.Program == old.Program &&
		r.Interpreter == old.Interpreter && r.Venv == old.Venv && r.Device == old.Device && r.TestBuild == old.TestBuild {
		return false, nil
	}
	_, err = tx.exec(`INSERT INTO peer_trust (machine_id, sender, device_id, account, program, interpreter, venv, approved_at,
		revoked_at, revoked_by, updated_at, test_build) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
		ON CONFLICT(machine_id, sender) DO UPDATE SET device_id = excluded.device_id, account = excluded.account,
		program = excluded.program, interpreter = excluded.interpreter, venv = excluded.venv, approved_at = excluded.approved_at,
		revoked_at = NULL, revoked_by = NULL, updated_at = excluded.updated_at, test_build = excluded.test_build`,
		r.Machine, r.Sender, r.Device, r.Account, r.Program, r.Interpreter, r.Venv, r.ApprovedAt, nowMS, r.TestBuild)
	return err == nil, err
}

// RevokePeerTrust marks a machine's row revoked by who (a device id, or "cli"); an already
// revoked row keeps its time (changed false). ErrNotFound when there is no row.
func (tx *Tx) RevokePeerTrust(machine, sender, by string, nowMS int64) (changed bool, err error) {
	old, ok, err := tx.PeerTrust(machine, sender)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrNotFound
	}
	if old.RevokedAt != 0 {
		return false, nil
	}
	_, err = tx.exec(`UPDATE peer_trust SET revoked_at = ?, revoked_by = ?, updated_at = ? WHERE machine_id = ? AND sender = ?`,
		nowMS, by, nowMS, machine, sender)
	return err == nil, err
}

// MachinePeerTrust lists one machine's rows (the directory answer's trust field).
func (s *Store) MachinePeerTrust(ctx context.Context, machine string) ([]PeerTrustRow, error) {
	return s.peerTrustRows(ctx, ` WHERE t.machine_id = ? ORDER BY t.sender`, machine)
}

// PeerTrusts lists every row, by machine name.
func (s *Store) PeerTrusts(ctx context.Context) ([]PeerTrustRow, error) {
	return s.peerTrustRows(ctx, ` ORDER BY coalesce(m.name, ''), t.machine_id, t.sender`)
}

func (s *Store) peerTrustRows(ctx context.Context, where string, args ...any) ([]PeerTrustRow, error) {
	rows, err := s.r.QueryContext(ctx, peerTrustSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerTrustRow
	for rows.Next() {
		r, err := scanPeerTrust(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TrustUse is a trusted sender's recent use on one machine: its newest message, the
// messages since sinceMS, and the tags it sent to since tagsSinceMS (newest first, each with
// its newest time).
type TrustUse struct {
	LastAt int64
	Since  int
	Tags   []TagUse
}

// TagUse is one tag a trusted sender sent to, and when last.
type TagUse struct {
	Tag string
	At  int64
}

// PeerTrustUse reads a trusted sender's use from peer_message: rows with that from_kind sent
// by an agent device of the machine.
func (s *Store) PeerTrustUse(ctx context.Context, machine, kind string, sinceMS, tagsSinceMS int64) (TrustUse, error) {
	var u TrustUse
	const from = ` FROM peer_message pm JOIN device d ON d.id = pm.from_device WHERE pm.from_kind = ? AND d.machine_id = ?`
	if err := s.r.QueryRowContext(ctx, `SELECT coalesce(max(pm.created_at), 0), coalesce(sum(pm.created_at >= ?), 0)`+from,
		sinceMS, kind, machine).Scan(&u.LastAt, &u.Since); err != nil {
		return u, err
	}
	rows, err := s.r.QueryContext(ctx, `SELECT pm.via_tag, max(pm.created_at) AS at`+from+
		` AND pm.via_tag IS NOT NULL AND pm.created_at >= ? GROUP BY pm.via_tag ORDER BY at DESC, pm.via_tag`, kind, machine, tagsSinceMS)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var t TagUse
		if err := rows.Scan(&t.Tag, &t.At); err != nil {
			return u, err
		}
		u.Tags = append(u.Tags, t)
	}
	return u, rows.Err()
}

// AccountLabels maps each account uuid to its seat's label (the first labelled seat of it);
// an account with no labelled seat is absent.
func (s *Store) AccountLabels(ctx context.Context) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT account_uuid, label FROM seat
		WHERE account_uuid IS NOT NULL AND label IS NOT NULL AND label != '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var a, l string
		if err := rows.Scan(&a, &l); err != nil {
			return nil, err
		}
		if _, ok := out[a]; !ok {
			out[a] = l
		}
	}
	return out, rows.Err()
}

// RevokePeerTrustOwner is the owner's revocation (POST /v1/peers/trusted/revoke): it marks
// the row revoked by the caller's device and writes a trust_revoke audit row when it changed.
// ErrNotFound when the machine has no row for sender.
func (s *Store) RevokePeerTrustOwner(ctx context.Context, machine, sender, by string) (PeerTrustRow, error) {
	var out PeerTrustRow
	err := s.Write(ctx, func(tx *Tx) error {
		now := tx.NowMS()
		changed, err := tx.RevokePeerTrust(machine, sender, by, now)
		if err != nil {
			return err
		}
		if changed {
			d, _ := json.Marshal(map[string]string{"machine": machine, "sender": sender})
			if err := tx.InsertPeerAudit(PeerAuditRow{At: now, Actor: by, Event: "trust_revoke", Detail: d}); err != nil {
				return err
			}
		}
		out, _, err = tx.PeerTrust(machine, sender)
		return err
	})
	return out, err
}
