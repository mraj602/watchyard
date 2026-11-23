package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// The link's tables (link.md 5, schema v7): link_state per device and the durable queue
// link_queue. Sealing and signing are the hub link package's; this file only stores rows.

// LinkStateRow is one device's position on the link.
type LinkStateRow struct {
	DeviceID              string
	NextSeq, AckedSeq     int64
	Transport             string // ws | poll | ""
	ConnectedAt, LastSeen int64
	LastClose, LastIP     string
	Hello                 string // JSON of the last hello's client and known, "" when none
}

// LinkRow is one queued frame.
type LinkRow struct {
	DeviceID  string
	Seq       int64
	Type, Tag string
	Body, Sig []byte
	Exp       int64 // unix seconds
	Ref       string
	CreatedAt int64
}

const linkRowCols = `device_id, seq, type, tag, body, sig, exp, coalesce(ref, ''), created_at`

func scanLinkRows(rows *sql.Rows) ([]LinkRow, error) {
	defer rows.Close()
	var out []LinkRow
	for rows.Next() {
		var r LinkRow
		if err := rows.Scan(&r.DeviceID, &r.Seq, &r.Type, &r.Tag, &r.Body, &r.Sig, &r.Exp, &r.Ref, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const linkStateCols = `device_id, next_seq, acked_seq, coalesce(transport, ''), coalesce(connected_at, 0), coalesce(last_seen_at, 0),
	coalesce(last_close, ''), coalesce(last_ip, ''), coalesce(hello, '')`

func scanLinkState(sc interface{ Scan(...any) error }) (LinkStateRow, error) {
	var r LinkStateRow
	err := sc.Scan(&r.DeviceID, &r.NextSeq, &r.AckedSeq, &r.Transport, &r.ConnectedAt, &r.LastSeen, &r.LastClose, &r.LastIP, &r.Hello)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// LinkState is a device's link_state row (ErrNotFound when it has none yet).
func (tx *Tx) LinkState(device string) (LinkStateRow, error) {
	return scanLinkState(tx.queryRow(`SELECT `+linkStateCols+` FROM link_state WHERE device_id = ?`, device))
}

// LinkStateOf reads a device's link_state outside a transaction.
func (s *Store) LinkStateOf(ctx context.Context, device string) (LinkStateRow, error) {
	return scanLinkState(s.r.QueryRowContext(ctx, `SELECT `+linkStateCols+` FROM link_state WHERE device_id = ?`, device))
}

// LinkTakeSeq returns the device's next_seq and moves it on (link.md 5.2 step 3), making
// the row (next_seq 1) on first use.
func (tx *Tx) LinkTakeSeq(device string) (int64, error) {
	if _, err := tx.exec(`INSERT INTO link_state (device_id) VALUES (?) ON CONFLICT(device_id) DO NOTHING`, device); err != nil {
		return 0, err
	}
	var seq int64
	err := tx.queryRow(`UPDATE link_state SET next_seq = next_seq + 1 WHERE device_id = ? RETURNING next_seq - 1`, device).Scan(&seq)
	return seq, err
}

// LinkCollapse deletes the device's rows of type t with tag (link.md 5.3) and returns
// their refs. Nothing for tag "".
func (tx *Tx) LinkCollapse(device, t, tag string) ([]string, error) {
	if tag == "" {
		return nil, nil
	}
	rows, err := tx.QueryContext(tx.ctx, `DELETE FROM link_queue WHERE device_id = ? AND type = ? AND tag = ? RETURNING coalesce(ref, '')`,
		device, t, tag)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

// LinkInsert stores one queued frame.
func (tx *Tx) LinkInsert(r LinkRow) error {
	_, err := tx.exec(`INSERT INTO link_queue (device_id, seq, type, tag, body, sig, exp, ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.DeviceID, r.Seq, r.Type, r.Tag, r.Body, r.Sig, r.Exp, nullStr(r.Ref), r.CreatedAt)
	return err
}

// LinkUsage is the device's queued rows and body bytes (link.md 5.5).
func (tx *Tx) LinkUsage(device string) (rows, bytes int64, err error) {
	err = tx.queryRow(`SELECT count(*), coalesce(sum(length(body)), 0) FROM link_queue WHERE device_id = ?`, device).Scan(&rows, &bytes)
	return
}

// LinkTotal is the number of queued rows hub-wide.
func (tx *Tx) LinkTotal() (int64, error) {
	var n int64
	err := tx.queryRow(`SELECT count(*) FROM link_queue`).Scan(&n)
	return n, err
}

// LinkOldestAlert deletes the device's oldest alert row and returns it (ok false when it
// has none).
func (tx *Tx) LinkOldestAlert(device string) (LinkRow, bool, error) {
	rows, err := tx.QueryContext(tx.ctx, `DELETE FROM link_queue WHERE device_id = ? AND seq =
		(SELECT min(seq) FROM link_queue WHERE device_id = ? AND type = 'alert') RETURNING `+linkRowCols, device, device)
	if err != nil {
		return LinkRow{}, false, err
	}
	rs, err := scanLinkRows(rows)
	if err != nil || len(rs) == 0 {
		return LinkRow{}, false, err
	}
	return rs[0], true, nil
}

// LinkDelete deletes one row and returns its ref (found false when there was none).
func (tx *Tx) LinkDelete(device string, seq int64) (ref string, found bool, err error) {
	err = tx.queryRow(`DELETE FROM link_queue WHERE device_id = ? AND seq = ? RETURNING coalesce(ref, '')`, device, seq).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return ref, err == nil, err
}

// LinkAck is a cumulative ack (link.md 5.6): acked_seq = max(acked_seq, upTo), rows with
// seq ≤ upTo deleted. It returns the deleted rows' refs.
func (tx *Tx) LinkAck(device string, upTo int64) ([]string, error) {
	if _, err := tx.exec(`UPDATE link_state SET acked_seq = max(acked_seq, ?) WHERE device_id = ?`, upTo, device); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(tx.ctx, `DELETE FROM link_queue WHERE device_id = ? AND seq <= ? RETURNING coalesce(ref, '')`, device, upTo)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

// LinkQueued is the device's rows, in seq order.
func (tx *Tx) LinkQueued(device string) ([]LinkRow, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT `+linkRowCols+` FROM link_queue WHERE device_id = ? ORDER BY seq`, device)
	if err != nil {
		return nil, err
	}
	return scanLinkRows(rows)
}

// LinkReplace swaps a device's queue for rows (the resync of link.md 3.2 step 7) and sets
// next_seq and acked_seq.
func (tx *Tx) LinkReplace(device string, rows []LinkRow, nextSeq, acked int64) error {
	if _, err := tx.exec(`DELETE FROM link_queue WHERE device_id = ?`, device); err != nil {
		return err
	}
	for _, r := range rows {
		if err := tx.LinkInsert(r); err != nil {
			return err
		}
	}
	_, err := tx.exec(`UPDATE link_state SET next_seq = ?, acked_seq = ? WHERE device_id = ?`, nextSeq, acked, device)
	return err
}

// LinkPending reads up to limit rows past seq that have not expired (link.md 5.6), in order.
func (s *Store) LinkPending(ctx context.Context, device string, after, nowSec int64, limit int) ([]LinkRow, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+linkRowCols+` FROM link_queue WHERE device_id = ? AND seq > ? AND exp > ?
		ORDER BY seq LIMIT ?`, device, after, nowSec, limit)
	if err != nil {
		return nil, err
	}
	return scanLinkRows(rows)
}

// LinkHasType reports whether the device has a queued row of type t.
func (tx *Tx) LinkHasType(device, t string) (bool, error) {
	var n int
	err := tx.queryRow(`SELECT count(*) FROM link_queue WHERE device_id = ? AND type = ?`, device, t).Scan(&n)
	return n > 0, err
}

// LinkConnected records a login: transport, connection time, address and the hello's
// client and known (JSON).
func (tx *Tx) LinkConnected(device, transport, ip, hello string, at int64) error {
	if _, err := tx.exec(`INSERT INTO link_state (device_id) VALUES (?) ON CONFLICT(device_id) DO NOTHING`, device); err != nil {
		return err
	}
	_, err := tx.exec(`UPDATE link_state SET transport = ?, connected_at = ?, last_seen_at = ?, last_ip = ?, hello = ?
		WHERE device_id = ?`, transport, at, at, nullStr(ip), nullStr(hello), device)
	return err
}

// LinkClosed records the end of a connection ("4409 superseded").
func (tx *Tx) LinkClosed(device, closeWord string, at int64) error {
	_, err := tx.exec(`UPDATE link_state SET connected_at = NULL, last_seen_at = ?, last_close = ? WHERE device_id = ?`,
		at, closeWord, device)
	return err
}

// LinkSeen moves last_seen_at.
func (s *Store) LinkSeen(ctx context.Context, device string, at int64) error {
	return s.Write(ctx, func(tx *Tx) error {
		_, err := tx.exec(`UPDATE link_state SET last_seen_at = ? WHERE device_id = ?`, at, device)
		return err
	})
}

// LinkExpire deletes every row past its exp (link.md 5.4) and returns them.
func (tx *Tx) LinkExpire(nowSec int64) ([]LinkRow, error) {
	rows, err := tx.QueryContext(tx.ctx, `DELETE FROM link_queue WHERE exp <= ? RETURNING `+linkRowCols, nowSec)
	if err != nil {
		return nil, err
	}
	return scanLinkRows(rows)
}

// LinkExpireUpTo deletes the device's rows up to upTo that expired by nowSec: the pump
// skipped them, so a cumulative ack past them must not count them delivered.
func (tx *Tx) LinkExpireUpTo(device string, upTo, nowSec int64) ([]LinkRow, error) {
	rows, err := tx.QueryContext(tx.ctx, `DELETE FROM link_queue WHERE device_id = ? AND seq <= ? AND exp <= ? RETURNING `+linkRowCols,
		device, upTo, nowSec)
	if err != nil {
		return nil, err
	}
	return scanLinkRows(rows)
}

// LinkDevice is a device that may receive frames: not revoked, with a device_key row.
type LinkDevice struct {
	ID, Kind string
}

// LinkDevices lists the devices with keys that are not revoked, optionally of some kinds.
func (tx *Tx) LinkDevices(kinds ...string) ([]LinkDevice, error) {
	q := `SELECT d.id, d.kind FROM device d JOIN device_key k ON k.device_id = d.id WHERE d.revoked_at IS NULL`
	args := []any{}
	if len(kinds) > 0 {
		q += ` AND d.kind IN (?` + strings.Repeat(", ?", len(kinds)-1) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	rows, err := tx.QueryContext(tx.ctx, q+` ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkDevice
	for rows.Next() {
		var d LinkDevice
		if err := rows.Scan(&d.ID, &d.Kind); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeviceLinkInfo is what the link needs to know of a device: its kind and whether it is
// revoked (ErrNotFound when there is no such device).
func (tx *Tx) DeviceLinkInfo(id string) (kind string, revoked bool, err error) {
	var rev sql.NullInt64
	err = tx.queryRow(`SELECT kind, revoked_at FROM device WHERE id = ?`, id).Scan(&kind, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNotFound
	}
	return kind, rev.Valid, err
}

// LinkTargetAuth is the auth secret of a phone's link push target (push.md 13.7), "" when
// it has none.
func (tx *Tx) LinkTargetAuth(device string) (string, error) {
	var auth sql.NullString
	err := tx.queryRow(`SELECT json_extract(keys, '$.auth') FROM push_target WHERE kind = 'link' AND device_id = ?
		ORDER BY updated_at DESC LIMIT 1`, device).Scan(&auth)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return auth.String, err
}

// DeliveryRefs parses a link_queue ref "d:<id>[,<id>…]" into delivery ids (nil for any
// other ref).
func DeliveryRefs(ref string) []int64 {
	rest, ok := strings.CutPrefix(ref, "d:")
	if !ok {
		return nil
	}
	var ids []int64
	for _, p := range strings.Split(rest, ",") {
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// DeliveryRef is the link_queue ref of the deliveries a frame carries.
func DeliveryRef(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "d:" + strings.Join(parts, ",")
}

// DeliveriesDelivered sets delivered_at on the deliveries a frame carried, when the device
// acked it (push.md 13.4).
func (tx *Tx) DeliveriesDelivered(refs []string, at int64) error {
	for _, ref := range refs {
		for _, id := range DeliveryRefs(ref) {
			if _, err := tx.exec(`UPDATE delivery SET delivered_at = ? WHERE id = ? AND delivered_at IS NULL`, at, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeliveriesCollapsed marks the deliveries of a collapsed frame (link.md 5.3).
func (tx *Tx) DeliveriesCollapsed(refs []string, at int64) error {
	for _, ref := range refs {
		for _, id := range DeliveryRefs(ref) {
			if _, err := tx.exec(`UPDATE delivery SET last_error = 'collapsed', updated_at = ? WHERE id = ? AND delivered_at IS NULL`,
				at, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// LinkDeviceStatus is GET /v1/devices' link object (link.md 9).
type LinkDeviceStatus struct {
	State  LinkStateRow
	Queued int64
}

// LinkStatuses returns every device's link_state with its queue length, by device id.
func (s *Store) LinkStatuses(ctx context.Context) (map[string]LinkDeviceStatus, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+linkStateCols+`, (SELECT count(*) FROM link_queue q WHERE q.device_id = link_state.device_id)
		FROM link_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LinkDeviceStatus{}
	for rows.Next() {
		var st LinkDeviceStatus
		r := &st.State
		if err := rows.Scan(&r.DeviceID, &r.NextSeq, &r.AckedSeq, &r.Transport, &r.ConnectedAt, &r.LastSeen, &r.LastClose, &r.LastIP,
			&r.Hello, &st.Queued); err != nil {
			return nil, err
		}
		out[r.DeviceID] = st
	}
	return out, rows.Err()
}

// LinkDismissTargets lists the link targets (target id and device) that were sent a
// delivery of alert (push.md 13.3).
func (tx *Tx) LinkDismissTargets(alertID string) ([][2]string, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT DISTINCT t.id, t.device_id FROM delivery d JOIN push_target t ON t.id = d.target_id
		WHERE d.alert_id = ? AND d.target_kind = 'link' AND d.status = 'sent' AND t.device_id IS NOT NULL`, alertID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, [2]string{a, b})
	}
	return out, rows.Err()
}

// DeviceCaps reads client.caps from the device's last hello; none when it has not connected
// or its hello carried none.
func (tx *Tx) DeviceCaps(device string) ([]string, error) {
	var hello string
	err := tx.queryRow(`SELECT coalesce(hello, '') FROM link_state WHERE device_id = ?`, device).Scan(&hello)
	if errors.Is(err, sql.ErrNoRows) || hello == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h struct {
		Client struct {
			Caps []string `json:"caps"`
		} `json:"client"`
	}
	if json.Unmarshal([]byte(hello), &h) != nil {
		return nil, nil
	}
	return h.Client.Caps, nil
}
