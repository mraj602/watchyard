package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// The peer relay's tables (schema v9, peers.md 8.3, 10): the directory of sessions other
// machines may reach, peer_message (metadata only: ids, accounts, size, status) and
// peer_thread. Nothing here ever holds message text; the sealed body exists only in
// link_queue while it is queued.

// ErrDuplicate: a peer message id that is stored already.
var ErrDuplicate = errors.New("duplicate message id")

// PeerSession is one session as the peer directory sees it (peers.md 3.1, hub source).
type PeerSession struct {
	ID, Name, Project, Cwd string
	Account, AccountLabel  string // the session's seat
	MachineID, MachineName string
	SlotFolder             string // the slot's folder ("~/.claude-2"), "" when none
	State                  string // session_state.state, "" when none yet
	Live                   bool   // not ended or gone, on a machine that was not removed
}

const peerSessionSelect = `SELECT s.id, coalesce(s.name, ''), coalesce(s.project, ''), coalesce(s.cwd, ''),
	coalesce(se.account_uuid, ''), coalesce(se.label, ''), coalesce(s.machine_id, ''), coalesce(m.name, ''),
	coalesce(sl.folder, ''), coalesce(st.state, ''),
	(s.ended_at IS NULL AND coalesce(st.state, '') NOT IN ('ended', 'gone') AND m.removed_at IS NULL)
	FROM session s LEFT JOIN seat se ON se.id = s.seat_id LEFT JOIN machine m ON m.id = s.machine_id
	LEFT JOIN slot sl ON sl.id = s.slot_id LEFT JOIN session_state st ON st.session_id = s.id`

func scanPeerSession(sc interface{ Scan(...any) error }) (PeerSession, error) {
	var p PeerSession
	var live int
	err := sc.Scan(&p.ID, &p.Name, &p.Project, &p.Cwd, &p.Account, &p.AccountLabel, &p.MachineID, &p.MachineName, &p.SlotFolder,
		&p.State, &live)
	p.Live = live == 1
	return p, err
}

// PeerSessions lists the live sessions active since sinceMS, oldest machine first.
func (s *Store) PeerSessions(ctx context.Context, sinceMS int64) ([]PeerSession, error) {
	rows, err := s.r.QueryContext(ctx, peerSessionSelect+` WHERE coalesce(s.last_event_at, 0) >= ? AND s.machine_id IS NOT NULL
		ORDER BY s.machine_id, s.started_at, s.id`, sinceMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerSession
	for rows.Next() {
		p, err := scanPeerSession(rows)
		if err != nil {
			return nil, err
		}
		if p.Live {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// PeerSession reads one session, live or not; ok is false when the hub has never seen it.
func (tx *Tx) PeerSession(id string) (PeerSession, bool, error) {
	p, err := scanPeerSession(tx.queryRow(peerSessionSelect+` WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PeerSession{}, false, nil
	}
	return p, err == nil, err
}

const agentDeviceQuery = `SELECT d.machine_id, d.id FROM device d JOIN device_key k ON k.device_id = d.id
	WHERE d.kind IN ('machine', 'desktop') AND d.revoked_at IS NULL AND d.machine_id IS NOT NULL
	ORDER BY d.created_at, d.id`

// AgentDevices maps each machine to the device its agent logs in with: the newest
// non-revoked machine (or desktop) device of that machine that has link keys.
func (s *Store) AgentDevices(ctx context.Context) (map[string]string, error) {
	rows, err := s.r.QueryContext(ctx, agentDeviceQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var m, d string
		if err := rows.Scan(&m, &d); err != nil {
			return nil, err
		}
		out[m] = d // ordered oldest first: the newest wins
	}
	return out, rows.Err()
}

// DeviceMachine is a device's kind, machine and whether it is revoked.
func (tx *Tx) DeviceMachine(id string) (kind, machine string, revoked bool, err error) {
	var rev int64
	err = tx.queryRow(`SELECT kind, coalesce(machine_id, ''), coalesce(revoked_at, 0) FROM device WHERE id = ?`, id).Scan(&kind, &machine, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, ErrNotFound
	}
	return kind, machine, rev != 0, err
}

// PeerMessageRow is one peer_message row.
type PeerMessageRow struct {
	ID, Thread                        string
	FromSession, FromDevice, FromAcct string
	ToSession, ToDevice, ToAcct       string
	Kind                              string
	Size                              int64
	Local                             bool
	Status, Detail                    string
	CreatedAt, ExpiresAt, FinalAt     int64
	// FromKind is "service:doorbell" for the trusted sender (schema v21, peers.md 4.1a) and
	// ViaTag the tag it sent to; both "" for a session.
	FromKind, ViaTag string
}

const peerMessageCols = `id, thread_id, from_session, from_device, from_account, to_session, to_device, to_account, kind, size,
	local, status, coalesce(detail, ''), created_at, expires_at, coalesce(final_at, 0), coalesce(from_kind, ''), coalesce(via_tag, '')`

func scanPeerMessage(sc interface{ Scan(...any) error }) (PeerMessageRow, error) {
	var r PeerMessageRow
	var local int
	err := sc.Scan(&r.ID, &r.Thread, &r.FromSession, &r.FromDevice, &r.FromAcct, &r.ToSession, &r.ToDevice, &r.ToAcct, &r.Kind,
		&r.Size, &local, &r.Status, &r.Detail, &r.CreatedAt, &r.ExpiresAt, &r.FinalAt, &r.FromKind, &r.ViaTag)
	r.Local = local == 1
	return r, err
}

// InsertPeerMessage adds a row; a message id that exists already is ErrDuplicate.
func (tx *Tx) InsertPeerMessage(r PeerMessageRow) error {
	var final any
	if r.FinalAt != 0 {
		final = r.FinalAt
	}
	cols := strings.NewReplacer("coalesce(detail, '')", "detail", "coalesce(final_at, 0)", "final_at",
		"coalesce(from_kind, '')", "from_kind", "coalesce(via_tag, '')", "via_tag").Replace(peerMessageCols)
	_, err := tx.exec(`INSERT INTO peer_message (`+cols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, nullif(?, ''), ?, ?, ?,
		nullif(?, ''), nullif(?, ''))`,
		r.ID, r.Thread, r.FromSession, r.FromDevice, r.FromAcct, r.ToSession, r.ToDevice, r.ToAcct, r.Kind, r.Size, boolInt(r.Local),
		r.Status, r.Detail, r.CreatedAt, r.ExpiresAt, final, r.FromKind, r.ViaTag)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrDuplicate
	}
	return err
}

// PeerMessage reads one row (ErrNotFound when none).
func (tx *Tx) PeerMessage(id string) (PeerMessageRow, error) {
	r, err := scanPeerMessage(tx.queryRow(`SELECT `+peerMessageCols+` FROM peer_message WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// FinishPeerMessage sets a queued message's final status; final is false when the row was
// not queued any more (a late or repeated result changes nothing).
func (tx *Tx) FinishPeerMessage(id, status, detail string, at int64) (final bool, err error) {
	res, err := tx.exec(`UPDATE peer_message SET status = ?, detail = nullif(?, ''), final_at = ? WHERE id = ? AND status = 'queued'`,
		status, detail, at, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// PeerMessageQuery filters PeerMessages.
type PeerMessageQuery struct {
	SinceMS         int64
	Thread, Session string
	Limit           int
}

// PeerMessages lists rows newest first.
func (s *Store) PeerMessages(ctx context.Context, q PeerMessageQuery) ([]PeerMessageRow, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+peerMessageCols+` FROM peer_message WHERE created_at >= ?
		AND (? = '' OR thread_id = ?) AND (? = '' OR from_session = ? OR to_session = ?)
		ORDER BY created_at DESC, id DESC LIMIT ?`, q.SinceMS, q.Thread, q.Thread, q.Session, q.Session, q.Session, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerMessageRow
	for rows.Next() {
		r, err := scanPeerMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PeerThreadRow is one peer_thread row.
type PeerThreadRow struct {
	ID, A, B, AAccount, BAccount               string
	OpenedAt, LastAt, HumanAt, Turns, PausedAt int64
	ClosedAt                                   int64
}

// OpenPeerThread returns the open thread between two sessions (either order); ok false when
// there is none.
func (tx *Tx) OpenPeerThread(x, y string) (PeerThreadRow, bool, error) {
	var r PeerThreadRow
	err := tx.queryRow(`SELECT id, a_session, b_session, a_account, b_account, opened_at, last_at, coalesce(human_at, 0), turns,
		coalesce(paused_at, 0), coalesce(closed_at, 0) FROM peer_thread
		WHERE closed_at IS NULL AND ((a_session = ? AND b_session = ?) OR (a_session = ? AND b_session = ?))
		ORDER BY last_at DESC LIMIT 1`, x, y, y, x).Scan(
		&r.ID, &r.A, &r.B, &r.AAccount, &r.BAccount, &r.OpenedAt, &r.LastAt, &r.HumanAt, &r.Turns, &r.PausedAt, &r.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// PeerThread reads one thread, open or closed; ErrNotFound when there is none.
func (tx *Tx) PeerThread(id string) (PeerThreadRow, error) {
	var r PeerThreadRow
	err := tx.queryRow(`SELECT id, a_session, b_session, a_account, b_account, opened_at, last_at, coalesce(human_at, 0), turns,
		coalesce(paused_at, 0), coalesce(closed_at, 0) FROM peer_thread WHERE id = ?`, id).Scan(
		&r.ID, &r.A, &r.B, &r.AAccount, &r.BAccount, &r.OpenedAt, &r.LastAt, &r.HumanAt, &r.Turns, &r.PausedAt, &r.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// PutPeerThread inserts or replaces a thread row.
func (tx *Tx) PutPeerThread(r PeerThreadRow) error {
	_, err := tx.exec(`INSERT INTO peer_thread (id, a_session, b_session, a_account, b_account, opened_at, last_at, human_at, turns,
		paused_at, closed_at) VALUES (?, ?, ?, ?, ?, ?, ?, nullif(?, 0), ?, nullif(?, 0), nullif(?, 0))
		ON CONFLICT(id) DO UPDATE SET last_at = excluded.last_at, human_at = excluded.human_at, turns = excluded.turns,
		paused_at = excluded.paused_at, closed_at = excluded.closed_at`,
		r.ID, r.A, r.B, r.AAccount, r.BAccount, r.OpenedAt, r.LastAt, r.HumanAt, r.Turns, r.PausedAt, r.ClosedAt)
	return err
}

// PeerThreadHuman moves a thread's human_at forward (peers.md 6.3: the hub learns it from
// envelopes and peer_result), resetting the turn count and the pause when it is newer.
func (tx *Tx) PeerThreadHuman(id string, human int64) error {
	_, err := tx.exec(`UPDATE peer_thread SET human_at = ?, turns = 0, paused_at = NULL WHERE id = ? AND coalesce(human_at, 0) < ?`,
		human, id, human)
	return err
}

// CloseIdlePeerThreads closes threads with no message since cutoffMS (peers.md 6.1).
func (tx *Tx) CloseIdlePeerThreads(cutoffMS, now int64) error {
	_, err := tx.exec(`UPDATE peer_thread SET closed_at = ? WHERE closed_at IS NULL AND last_at < ?`, now, cutoffMS)
	return err
}

// LinkDropType deletes every queued row of one frame type (peers.md 11: a pause drops queued
// peer_msg rows) and returns them.
func (tx *Tx) LinkDropType(t string) ([]LinkRow, error) {
	rows, err := tx.QueryContext(tx.ctx, `DELETE FROM link_queue WHERE type = ? RETURNING `+linkRowCols, t)
	if err != nil {
		return nil, err
	}
	return scanLinkRows(rows)
}
