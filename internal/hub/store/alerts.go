package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// Alerts, deliveries and the facts the alert engine reads (push.md 3-5, 9). Every write
// goes through a Tx (store.Write); the engine never holds a transaction while sending.
// Names carry Alert/Delivery prefixes so they stay apart from the rest of the package.

const alertCols = `id, episode, trigger, rule_id, session_id, seat_id, machine_id, window_key, status, reason, since_at,
	created_at, due_at, fired_at, fire_count, snoozed_until, seen_at, seen_by, ended_at, facts, updated_at`

// alertOpenSQL lists the statuses reconcile may still end.
const alertOpenSQL = `status IN ('pending', 'fired', 'snoozed', 'seen')`

func alertNS(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

func alertScan(sc interface{ Scan(...any) error }) (model.Alert, error) {
	var a model.Alert
	var rule, session, seat, machine, window, reason, seenBy sql.NullString
	var since, due, fired, snoozed, seen, ended sql.NullInt64
	var created, updated int64
	var facts string
	err := sc.Scan(&a.ID, &a.Episode, &a.Trigger, &rule, &session, &seat, &machine, &window, &a.Status, &reason, &since,
		&created, &due, &fired, &a.FireCount, &snoozed, &seen, &seenBy, &ended, &facts, &updated)
	if err != nil {
		return a, err
	}
	a.Rule, a.Session, a.Seat, a.Machine, a.WindowKey, a.Reason = ns(rule), ns(session), ns(seat), ns(machine), ns(window), ns(reason)
	a.SinceAt, a.CreatedAt, a.DueAt, a.FiredAt = model.Time(since.Int64), model.Time(created), model.Time(due.Int64), model.Time(fired.Int64)
	a.SnoozedUntil, a.SeenAt, a.EndedAt = model.Time(snoozed.Int64), model.Time(seen.Int64), model.Time(ended.Int64)
	a.SeenBy = seenBy.String
	_ = json.Unmarshal([]byte(facts), &a.Facts)
	return a, nil
}

func alertRows(rows *sql.Rows, err error) ([]model.Alert, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Alert
	for rows.Next() {
		a, err := alertScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AlertInsert creates an alert; it does nothing (false) when its episode already has one
// (push.md 3.2: the unique episode is the dedupe).
func (tx *Tx) AlertInsert(a model.Alert) (bool, error) {
	facts, err := json.Marshal(a.Facts)
	if err != nil {
		return false, err
	}
	res, err := tx.exec(`INSERT INTO alert (`+alertCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(episode) DO NOTHING`,
		a.ID, a.Episode, string(a.Trigger), alertNS(a.Rule), alertNS(a.Session), alertNS(a.Seat), alertNS(a.Machine), alertNS(a.WindowKey),
		string(a.Status), alertNS(a.Reason), nullInt(int64(a.SinceAt)), int64(a.CreatedAt), nullInt(int64(a.DueAt)),
		nullInt(int64(a.FiredAt)), a.FireCount, nullInt(int64(a.SnoozedUntil)), nullInt(int64(a.SeenAt)), nullStr(a.SeenBy),
		nullInt(int64(a.EndedAt)), string(facts), int64(a.CreatedAt))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AlertSave writes an alert's mutable columns back.
func (tx *Tx) AlertSave(a model.Alert, at int64) error {
	facts, err := json.Marshal(a.Facts)
	if err != nil {
		return err
	}
	_, err = tx.exec(`UPDATE alert SET status = ?, reason = ?, due_at = ?, fired_at = ?, fire_count = ?, snoozed_until = ?,
		seen_at = ?, seen_by = ?, ended_at = ?, facts = ?, updated_at = ? WHERE id = ?`,
		string(a.Status), alertNS(a.Reason), nullInt(int64(a.DueAt)), nullInt(int64(a.FiredAt)), a.FireCount,
		nullInt(int64(a.SnoozedUntil)), nullInt(int64(a.SeenAt)), nullStr(a.SeenBy), nullInt(int64(a.EndedAt)), string(facts), at, a.ID)
	return err
}

// AlertGet returns one alert (ErrNotFound).
func (tx *Tx) AlertGet(id string) (model.Alert, error) {
	a, err := alertScan(tx.queryRow(`SELECT `+alertCols+` FROM alert WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// AlertsOf returns the alerts of one subject (col is session_id, seat_id or machine_id)
// that reconcile may end (open or seen), plus those whose episode is in episodes, whatever
// their status. Oldest first.
func (tx *Tx) AlertsOf(col, id string, episodes []string) ([]model.Alert, error) {
	switch col {
	case "session_id", "seat_id", "machine_id":
	default:
		return nil, errors.New("store: AlertsOf: bad column")
	}
	args := []any{id}
	q := `SELECT ` + alertCols + ` FROM alert WHERE ` + col + ` = ? AND (` + alertOpenSQL
	if len(episodes) > 0 {
		q += " OR " + in("episode", episodes, &args)
	}
	return alertRows(tx.QueryContext(tx.ctx, q+`) ORDER BY created_at, id`, args...))
}

// AlertsDue returns pending and snoozed alerts due at or before until, earliest first.
func (tx *Tx) AlertsDue(until int64) ([]model.Alert, error) {
	return alertRows(tx.QueryContext(tx.ctx, `SELECT `+alertCols+` FROM alert WHERE due_at IS NOT NULL AND due_at <= ?
		AND status IN ('pending', 'snoozed') ORDER BY due_at, created_at, id`, until))
}

// AlertsPending lists the alerts that wait to fire for the first time (status pending).
func (tx *Tx) AlertsPending() ([]model.Alert, error) {
	return alertRows(tx.QueryContext(tx.ctx, `SELECT `+alertCols+` FROM alert WHERE status = 'pending' ORDER BY created_at, id`))
}

// AlertNextDue is the earliest due_at of a pending or snoozed alert.
func (tx *Tx) AlertNextDue() (int64, bool, error) {
	var v sql.NullInt64
	err := tx.queryRow(`SELECT min(due_at) FROM alert WHERE due_at IS NOT NULL AND status IN ('pending', 'snoozed')`).Scan(&v)
	return v.Int64, v.Valid, err
}

// AlertSubjects lists what start-up reconcile visits (push.md 3.5): sessions that need you
// or have an alert reconcile may end; seats with a current window at or over 80% or such an
// alert; machines with such an alert.
func (tx *Tx) AlertSubjects(now int64) (sessions, seats, machines []string, err error) {
	strs := func(q string, args ...any) ([]string, error) {
		rows, err := tx.QueryContext(tx.ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
	if sessions, err = strs(`SELECT session_id FROM session_state WHERE needs_you = 1
		UNION SELECT session_id FROM alert WHERE session_id IS NOT NULL AND ` + alertOpenSQL + ` ORDER BY 1`); err != nil {
		return
	}
	if seats, err = strs(`SELECT seat_id FROM usage_window WHERE resets_at > ? AND peak_pct >= 80
		UNION SELECT seat_id FROM alert WHERE seat_id IS NOT NULL AND session_id IS NULL AND `+alertOpenSQL+` ORDER BY 1`, now); err != nil {
		return
	}
	machines, err = strs(`SELECT machine_id FROM alert WHERE machine_id IS NOT NULL AND trigger = 'machine_silent' AND ` + alertOpenSQL + ` ORDER BY 1`)
	return
}

// AlertSessionRow is what the engine reads about a session.
type AlertSessionRow struct {
	ID, State, Detail, Question             string
	NeedsYou                                bool
	SinceAt                                 int64
	SeatID, SeatLabel, Email                string
	SeatName                                string // the board's name of the seat (SeatDisplayNames)
	Project, MachineID, MachineName, Remote string
	Tool, Message, LastMessage              string
	LimitResetsAt                           int64  // newest limit_hit resets_at of the session
	Peers                                   string // session_state.peers (states.md 3.1)
}

// AlertSession reads a session's state and labels (found false when there is none).
func (tx *Tx) AlertSession(id string) (AlertSessionRow, bool, error) {
	var r AlertSessionRow
	var detail, question, seat, label, email, project, machine, mname, remote, tool, msg, lm, peers sql.NullString
	var needs int
	var limitResets sql.NullInt64
	err := tx.queryRow(`SELECT s.id, coalesce(st.state, 'done'), st.detail, st.question, coalesce(st.needs_you, 0),
		coalesce(st.since_at, s.started_at, 0), s.seat_id, se.label, a.email, s.project, s.machine_id, m.name, s.remote_url,
		(SELECT h.tool_name FROM hook_event h WHERE h.session_id = s.id AND h.tool_name IS NOT NULL
		   AND h.event IN ('PermissionRequest', 'PreToolUse', 'Notification') AND h.ts >= coalesce(st.since_at, 0) ORDER BY h.ts DESC LIMIT 1),
		(SELECT r.body ->> '$.payload.message' FROM hook_event h JOIN record r ON r.id = h.record_id
		   WHERE h.session_id = s.id AND h.event = 'Notification' AND h.ts >= coalesce(st.since_at, 0) ORDER BY h.ts DESC LIMIT 1),
		(SELECT h.last_message FROM hook_event h WHERE h.session_id = s.id AND h.event IN ('Stop', 'StopFailure') ORDER BY h.ts DESC LIMIT 1),
		(SELECT l.resets_at FROM limit_hit l WHERE l.session_id = s.id ORDER BY l.ts DESC LIMIT 1), st.peers
		FROM session s LEFT JOIN session_state st ON st.session_id = s.id LEFT JOIN seat se ON se.id = s.seat_id
		LEFT JOIN account a ON a.uuid = se.account_uuid LEFT JOIN machine m ON m.id = s.machine_id WHERE s.id = ?`, id).
		Scan(&r.ID, &r.State, &detail, &question, &needs, &r.SinceAt, &seat, &label, &email, &project, &machine, &mname, &remote,
			&tool, &msg, &lm, &limitResets, &peers)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.Detail, r.Question, r.NeedsYou = detail.String, question.String, needs == 1
	r.SeatID, r.SeatLabel, r.Email, r.Project = seat.String, label.String, email.String, project.String
	r.MachineID, r.MachineName, r.Tool, r.Message, r.LastMessage = machine.String, mname.String, tool.String, msg.String, lm.String
	r.LimitResetsAt, r.Peers = limitResets.Int64, peers.String
	if r.SeatID != "" {
		names, err := tx.alertSeatNames(r.SeatID)
		if err != nil {
			return r, false, err
		}
		r.SeatName = names[r.SeatID]
	}
	if v := claudeURL(remote); v != nil {
		r.Remote = *v
	}
	return r, true, nil
}

// AlertWindowRow is one usage window as the engine reads it.
type AlertWindowRow struct {
	Key                 string
	ResetsAt, UpdatedAt int64
	Peak, Final         *float64
}

// AlertSeatRow is a seat's labels and current windows (resets_at > now, newest per key).
type AlertSeatRow struct {
	ID, Label, Email string
	Name             string // the board's name of the seat (SeatDisplayNames)
	Hidden           bool
	Windows          []AlertWindowRow
}

// AlertSeat reads a seat (found false when there is none).
func (tx *Tx) AlertSeat(id string, now int64) (AlertSeatRow, bool, error) {
	r := AlertSeatRow{ID: id}
	var label, email sql.NullString
	var hidden int
	err := tx.queryRow(`SELECT se.label, a.email, `+seatHiddenSQL+` FROM seat se LEFT JOIN account a ON a.uuid = se.account_uuid WHERE se.id = ?`, id).
		Scan(&label, &email, &hidden)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.Label, r.Email, r.Hidden = label.String, email.String, hidden == 1
	names, err := tx.alertSeatNames(id)
	if err != nil {
		return r, false, err
	}
	r.Name = names[id]
	rows, err := tx.QueryContext(tx.ctx, `SELECT window_key, resets_at, updated_at, peak_pct, final_pct FROM usage_window w
		WHERE seat_id = ? AND resets_at > ? AND resets_at = (SELECT max(resets_at) FROM usage_window x WHERE x.seat_id = w.seat_id AND x.window_key = w.window_key)
		ORDER BY window_key`, id, now)
	if err != nil {
		return r, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var w AlertWindowRow
		var peak, final sql.NullFloat64
		if err := rows.Scan(&w.Key, &w.ResetsAt, &w.UpdatedAt, &peak, &final); err != nil {
			return r, false, err
		}
		w.Peak, w.Final = nf(peak), nf(final)
		r.Windows = append(r.Windows, w)
	}
	return r, true, rows.Err()
}

// AlertRoomRow is one seat as "who has room" sees it (push.md 5.7): its current (newest)
// window of one key, and whether it has a reading for any window at all.
type AlertRoomRow struct {
	Seat, Label, Email string
	Name               string // the board's name of the seat (SeatDisplayNames)
	Hidden             bool
	// HasWindow: the current window of the key has a reading (Used, ResetsAt, UpdatedAt);
	// ResetsAt is 0 for an idle window (not started, 0 %).
	HasWindow bool
	Used      float64
	ResetsAt  int64
	UpdatedAt int64
	// HasReading: some current window of the seat (any key) has a reading.
	HasReading bool
}

// AlertRoom lists every seat in list order (sort_order, created_at, id, as the board shows
// them) with its current window of key: the newest one, as the board's overview has it. A
// window with no used % is no reading.
func (tx *Tx) AlertRoom(key string, now int64) ([]AlertRoomRow, error) {
	names, err := tx.alertSeatNames()
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(tx.ctx, `SELECT se.id, se.label, a.email, `+seatHiddenSQL+`
		FROM seat se LEFT JOIN account a ON a.uuid = se.account_uuid ORDER BY se.sort_order, se.created_at, se.id`)
	if err != nil {
		return nil, err
	}
	var out []AlertRoomRow
	at := map[string]int{}
	for rows.Next() {
		var r AlertRoomRow
		var label, email sql.NullString
		var hidden int
		if err := rows.Scan(&r.Seat, &label, &email, &hidden); err != nil {
			rows.Close()
			return nil, err
		}
		r.Label, r.Email, r.Hidden = label.String, email.String, hidden == 1
		r.Name = names[r.Seat]
		at[r.Seat] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(tx.ctx, `SELECT w.seat_id, w.window_key, w.resets_at, w.final_pct, w.updated_at FROM usage_window w
		WHERE w.final_pct IS NOT NULL
		  AND w.resets_at = (SELECT max(x.resets_at) FROM usage_window x WHERE x.seat_id = w.seat_id AND x.window_key = w.window_key)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var seat, k string
		var resets, updated int64
		var used float64
		if err := rows.Scan(&seat, &k, &resets, &used, &updated); err != nil {
			return nil, err
		}
		i, ok := at[seat]
		if !ok {
			continue
		}
		r := &out[i]
		r.HasReading = true
		if k == key {
			r.HasWindow, r.Used, r.ResetsAt, r.UpdatedAt = true, used, resets, updated
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// An idle reading newer than the window (or with no window) is the current window, as in
	// the overview (Store.Windows): 0 % used, no reset time.
	idle, err := idleReadings(tx.ctx, tx, "")
	if err != nil {
		return nil, err
	}
	for seat, keys := range idle {
		i, ok := at[seat]
		if !ok {
			continue
		}
		r := &out[i]
		r.HasReading = true
		if ts, ok := keys[key]; ok && (!r.HasWindow || ts > r.UpdatedAt) {
			r.HasWindow, r.Used, r.ResetsAt, r.UpdatedAt = true, 0, 0, ts
		}
	}
	return out, nil
}

// AlertCurrentReset is the resets_at of a seat's current window of key (0 if none).
func (tx *Tx) AlertCurrentReset(seat, key string, now int64) (int64, error) {
	var v sql.NullInt64
	err := tx.queryRow(`SELECT min(resets_at) FROM usage_window WHERE seat_id = ? AND window_key = ? AND resets_at > ?`, seat, key, now).Scan(&v)
	return v.Int64, err
}

// AlertMachineRow is a machine as the silence sweep reads it.
type AlertMachineRow struct {
	ID, Name string
	LastSeen int64
	Live     int // sessions in working / needs_permission / asked that are not stale (states.md 5)
}

// AlertMachines lists machines that are not removed (ids filters; nil = all).
func (tx *Tx) AlertMachines(ids []string, now int64) ([]AlertMachineRow, error) {
	args := []any{now - StaleAfter.Milliseconds()}
	q := `SELECT m.id, m.name, coalesce(m.last_seen, 0),
		(SELECT count(*) FROM session s LEFT JOIN session_state st ON st.session_id = s.id WHERE s.machine_id = m.id
		   AND coalesce(st.state, 'done') IN (` + activeStatesSQL + `)
		   AND NOT (coalesce(st.state, 'done') = 'working' AND s.last_listed_at IS NULL AND coalesce(s.last_event_at, 0) < ?))
		FROM machine m WHERE m.removed_at IS NULL`
	if ids != nil {
		q += " AND " + in("m.id", ids, &args)
	}
	rows, err := tx.QueryContext(tx.ctx, q+` ORDER BY m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertMachineRow
	for rows.Next() {
		var r AlertMachineRow
		if err := rows.Scan(&r.ID, &r.Name, &r.LastSeen, &r.Live); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AlertNeedsYou counts live sessions needing you (the badge count of push.md 6.5), leaving
// out those whose current alert is seen (push.md 4).
func (tx *Tx) AlertNeedsYou() (int, error) {
	var n int
	err := tx.queryRow(`SELECT count(*) FROM session_state st JOIN session s ON s.id = st.session_id
		WHERE st.needs_you = 1 AND st.state NOT IN ('ended', 'gone') AND NOT ` + sessionSeenExpr).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------- targets

const alertTargetCols = `t.id, t.kind, coalesce(t.device_id, ''), coalesce(t.name, ''), t.endpoint, t.keys, coalesce(t.secret, ''),
	t.prefs, d.prefs, t.enabled, t.created_at, t.updated_at, coalesce(t.last_ok_at, 0), coalesce(t.last_error, ''),
	coalesce(t.last_error_at, 0), t.fail_count, coalesce(t.gone_at, 0)`

const alertTargetFrom = ` FROM push_target t LEFT JOIN device d ON d.id = t.device_id JOIN channel c ON c.kind = t.kind`

func alertScanTarget(sc interface{ Scan(...any) error }) (model.Target, error) {
	var t model.Target
	var keys, tprefs, dprefs sql.NullString
	var enabled int
	err := sc.Scan(&t.ID, &t.Kind, &t.DeviceID, &t.Name, &t.Endpoint, &keys, &t.Secret, &tprefs, &dprefs, &enabled,
		&t.CreatedAt, &t.UpdatedAt, &t.LastOKAt, &t.LastError, &t.LastErrorAt, &t.FailCount, &t.GoneAt)
	if err != nil {
		return t, err
	}
	t.Enabled = enabled == 1
	if keys.Valid {
		var k model.Keys
		if json.Unmarshal([]byte(keys.String), &k) == nil {
			t.Keys = &k
		}
	}
	// Effective prefs (push.md 5.4): the device's for a target with a device, else the target's.
	raw := tprefs
	if t.DeviceID != "" {
		raw = dprefs
	}
	p := model.Prefs{Tap: model.TapAuto}
	if raw.Valid {
		_ = json.Unmarshal([]byte(raw.String), &p)
	}
	// A target's own prefs.private wins over its device's (the ntfy.sh private default is
	// stored on the target, push.md 7.1).
	if t.DeviceID != "" && tprefs.Valid {
		var own struct {
			Private *bool `json:"private"`
		}
		if json.Unmarshal([]byte(tprefs.String), &own) == nil && own.Private != nil {
			p.Private = own.Private
		}
	}
	t.Prefs = &p
	return t, nil
}

// AlertTargets lists the targets a fired alert may go to (push.md 5.1): enabled, not gone,
// channel enabled, device (if any) neither revoked nor expired. Prefs is set to the
// effective prefs of each.
func (tx *Tx) AlertTargets(now int64) ([]model.Target, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT `+alertTargetCols+alertTargetFrom+` WHERE t.enabled = 1 AND t.gone_at IS NULL
		AND c.enabled = 1 AND (t.device_id IS NULL OR (d.revoked_at IS NULL AND (d.expires_at IS NULL OR d.expires_at > ?)))
		ORDER BY t.created_at, t.id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Target
	for rows.Next() {
		t, err := alertScanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AlertTarget returns one target if it may still receive pushes (ok false otherwise).
func (tx *Tx) AlertTarget(id string, now int64) (model.Target, bool, error) {
	t, err := alertScanTarget(tx.queryRow(`SELECT `+alertTargetCols+alertTargetFrom+` WHERE t.id = ? AND t.enabled = 1 AND t.gone_at IS NULL
		AND c.enabled = 1 AND (t.device_id IS NULL OR (d.revoked_at IS NULL AND (d.expires_at IS NULL OR d.expires_at > ?)))`, id, now))
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	return t, err == nil, err
}

// AlertTargetDevice returns a target's device id ("" when none or no such target).
func (tx *Tx) AlertTargetDevice(id string) (string, error) {
	var d sql.NullString
	err := tx.queryRow(`SELECT device_id FROM push_target WHERE id = ?`, id).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return d.String, err
}

// AlertTargetResult books one send's outcome on the target (push.md 5.6).
func (tx *Tx) AlertTargetResult(id string, r model.Result, now int64) error {
	var err error
	switch r.Outcome {
	case model.OutcomeSent:
		_, err = tx.exec(`UPDATE push_target SET last_ok_at = ?, fail_count = 0 WHERE id = ?`, now, id)
	case model.OutcomeGone:
		_, err = tx.exec(`UPDATE push_target SET gone_at = ?, enabled = 0, last_error = ?, last_error_at = ? WHERE id = ?`,
			now, alertErrText(r), now, id)
	default:
		_, err = tx.exec(`UPDATE push_target SET last_error = ?, last_error_at = ?, fail_count = fail_count + 1 WHERE id = ?`,
			alertErrText(r), now, id)
		if err == nil {
			// fail_count >= 20 and no success for 7 days: disable.
			_, err = tx.exec(`UPDATE push_target SET enabled = 0 WHERE id = ? AND fail_count >= 20
				AND coalesce(last_ok_at, created_at) < ?`, id, now-7*24*3600*1000)
		}
	}
	return err
}

func alertErrText(r model.Result) string {
	s := r.Err
	// The senders already name the status ("410 <reply>" for Web Push, "HTTP 401" for ntfy,
	// push.md 5.6 and 7.3): prefix it only when the text does not, or it reads "410 410".
	if code := strconv.Itoa(r.HTTPStatus); r.HTTPStatus != 0 && !strings.HasPrefix(s, code) && !strings.HasPrefix(s, "HTTP "+code) {
		s = strings.TrimSpace(code + " " + s)
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ---------------------------------------------------------------- deliveries

// DeliveryRow is one row of the delivery table.
type DeliveryRow struct {
	ID                   int64
	AlertID              string
	TargetID, TargetKind string
	Fire                 int
	Batch                string
	Status               string // pending, held, sent, failed, skipped, gone
	Reason               string
	Tries                int
	NextAt               int64
	HTTPStatus           int
	LastError            string
	CreatedAt, UpdatedAt int64
}

const deliveryCols = `id, alert_id, target_id, target_kind, fire, coalesce(batch, ''), status, coalesce(reason, ''), tries,
	coalesce(next_at, 0), coalesce(http_status, 0), coalesce(last_error, ''), created_at, updated_at`

func deliveryScan(sc interface{ Scan(...any) error }) (DeliveryRow, error) {
	var d DeliveryRow
	err := sc.Scan(&d.ID, &d.AlertID, &d.TargetID, &d.TargetKind, &d.Fire, &d.Batch, &d.Status, &d.Reason, &d.Tries,
		&d.NextAt, &d.HTTPStatus, &d.LastError, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// DeliveryInsert adds a delivery and sets d.ID; one per (alert, target, fire): a second
// insert for the same triple does nothing (inserted false).
func (tx *Tx) DeliveryInsert(d *DeliveryRow) (bool, error) {
	res, err := tx.exec(`INSERT INTO delivery (alert_id, target_id, target_kind, fire, batch, status, reason, tries, next_at,
		http_status, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(alert_id, target_id, fire) DO NOTHING`,
		d.AlertID, d.TargetID, d.TargetKind, d.Fire, nullStr(d.Batch), d.Status, nullStr(d.Reason), d.Tries, nullInt(d.NextAt),
		nullInt(int64(d.HTTPStatus)), nullStr(d.LastError), d.CreatedAt, d.UpdatedAt)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	d.ID, err = res.LastInsertId()
	return true, err
}

// DeliverySave writes a delivery's mutable columns back.
func (tx *Tx) DeliverySave(d DeliveryRow) error {
	_, err := tx.exec(`UPDATE delivery SET batch = ?, status = ?, reason = ?, tries = ?, next_at = ?, http_status = ?,
		last_error = ?, updated_at = ? WHERE id = ?`,
		nullStr(d.Batch), d.Status, nullStr(d.Reason), d.Tries, nullInt(d.NextAt), nullInt(int64(d.HTTPStatus)),
		nullStr(d.LastError), d.UpdatedAt, d.ID)
	return err
}

// DeliveryGet returns one delivery.
func (tx *Tx) DeliveryGet(id int64) (DeliveryRow, error) {
	d, err := deliveryScan(tx.queryRow(`SELECT `+deliveryCols+` FROM delivery WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DeliveriesDue returns pending (retry) and held deliveries with next_at at or before now.
func (tx *Tx) DeliveriesDue(now int64) ([]DeliveryRow, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT `+deliveryCols+` FROM delivery WHERE next_at IS NOT NULL AND next_at <= ?
		AND status IN ('pending', 'held') ORDER BY next_at, id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryRow
	for rows.Next() {
		d, err := deliveryScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeliveryNextAt is the earliest next_at of a pending or held delivery.
func (tx *Tx) DeliveryNextAt() (int64, bool, error) {
	var v sql.NullInt64
	err := tx.queryRow(`SELECT min(next_at) FROM delivery WHERE next_at IS NOT NULL AND status IN ('pending', 'held')`).Scan(&v)
	return v.Int64, v.Valid, err
}

// DeliveriesSkipUnsent marks an alert's pending and held deliveries skipped (reason).
func (tx *Tx) DeliveriesSkipUnsent(alertID, reason string, now int64) error {
	_, err := tx.exec(`UPDATE delivery SET status = 'skipped', reason = ?, next_at = NULL, updated_at = ?
		WHERE alert_id = ? AND status IN ('pending', 'held')`, reason, now, alertID)
	return err
}

// DeliveryPushes returns the send times of the distinct pushes (batches) a target received
// since since, oldest first (the flood cap, push.md 5.3).
func (tx *Tx) DeliveryPushes(target string, since int64) ([]int64, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT min(updated_at) FROM delivery WHERE target_id = ? AND status = 'sent' AND updated_at > ?
		GROUP BY coalesce(batch, 'd' || id) ORDER BY 1`, target, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- read API

// AlertFilter is the query of GET /v1/alerts.
type AlertFilter struct {
	Statuses []string
	Reason   string // with Statuses = [skipped] and Reason = baseline, baseline rows are listed
	Session  string
	Limit    int
	Cursor   string
}

func alertCursor(created int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(created, 10) + ":" + id))
}

// ErrBadCursor means the cursor of GET /v1/alerts is not one the hub made.
var ErrBadCursor = errors.New("bad cursor")

// AlertList returns one page of alerts, newest first, with deliveries.
func (s *Store) AlertList(ctx context.Context, f AlertFilter) ([]model.Alert, string, error) {
	var where []string
	var args []any
	if len(f.Statuses) > 0 {
		where = append(where, in("status", f.Statuses, &args))
	}
	if f.Reason != "" {
		where = append(where, "reason = ?")
		args = append(args, f.Reason)
	}
	if !(f.Reason == "baseline") {
		where = append(where, "coalesce(reason, '') != 'baseline'")
	}
	if f.Session != "" {
		where = append(where, "session_id = ?")
		args = append(args, f.Session)
	}
	if f.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		c, id, ok := strings.Cut(string(raw), ":")
		n, perr := strconv.ParseInt(c, 10, 64)
		if err != nil || !ok || perr != nil {
			return nil, "", ErrBadCursor
		}
		where = append(where, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, n, n, id)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + alertCols + ` FROM alert`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit+1)
	out, err := alertRows(s.r.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT ?`, args...))
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = alertCursor(int64(last.CreatedAt), last.ID)
	}
	if out == nil {
		out = []model.Alert{}
	}
	return out, next, s.alertAttachDeliveries(ctx, out)
}

// AlertByID returns one alert with its deliveries.
func (s *Store) AlertByID(ctx context.Context, id string) (model.Alert, error) {
	a, err := alertScan(s.r.QueryRowContext(ctx, `SELECT `+alertCols+` FROM alert WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	one := []model.Alert{a}
	err = s.alertAttachDeliveries(ctx, one)
	return one[0], err
}

func (s *Store) alertAttachDeliveries(ctx context.Context, alerts []model.Alert) error {
	if len(alerts) == 0 {
		return nil
	}
	ids := make([]string, len(alerts))
	at := map[string]int{}
	for i, a := range alerts {
		ids[i] = a.ID
		at[a.ID] = i
		alerts[i].Deliveries = []model.Delivery{}
	}
	var args []any
	rows, err := s.r.QueryContext(ctx, `SELECT d.alert_id, d.target_id, d.target_kind, t.name, d.fire, d.batch, d.status, d.reason,
		d.tries, d.http_status, d.updated_at FROM delivery d LEFT JOIN push_target t ON t.id = d.target_id
		WHERE `+in("d.alert_id", ids, &args)+` ORDER BY d.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d model.Delivery
		var alertID string
		var name, batch, reason sql.NullString
		var status sql.NullInt64
		var updated int64
		if err := rows.Scan(&alertID, &d.Target, &d.Kind, &name, &d.Fire, &batch, &d.Status, &reason, &d.Tries, &status, &updated); err != nil {
			return err
		}
		d.TargetName, d.Batch, d.Reason, d.UpdatedAt = ns(name), ns(batch), ns(reason), model.Time(updated)
		if status.Valid {
			v := int(status.Int64)
			d.HTTPStatus = &v
		}
		i := at[alertID]
		alerts[i].Deliveries = append(alerts[i].Deliveries, d)
	}
	return rows.Err()
}

// ---------------------------------------------------------------- direct push

// DirectPush is a direct_push record as the alert table sees it (record.md 4.9).
type DirectPush struct {
	Episode, Trigger       string // s:<session>:<state>:<since ms>; needs_permission, asked, failed
	Session, Seat, Machine string
	SinceMS, SentMS        int64
	Project                string // the session's project, for the alert's facts
}

// Outcomes of AlertDirectPush.
const (
	DirectCreated   = "created"   // no alert existed: one was created as fired/direct
	DirectMarked    = "marked"    // a pending (or skipped/stale) alert became fired/direct
	DirectDuplicate = "duplicate" // the hub had pushed (or ended) the episode already: both pushed or nothing to do
	DirectKept      = "kept"      // a skipped alert (rule_off, remote_control, …) stays as it is
)

// AlertDirectPush applies a successful direct push to the episode's alert (machines.md 5.5):
// no alert → create it `fired`, reason `direct`, fired_at = sent_at, fire_count 1; `pending`
// or `skipped`/`stale` → the same change (a pending alert then never fires); any other status
// → nothing changes. Nothing is ever sent from here.
func (tx *Tx) AlertDirectPush(d DirectPush, now int64) (string, model.Alert, error) {
	reason := model.ReasonDirect
	a, err := alertScan(tx.queryRow(`SELECT `+alertCols+` FROM alert WHERE episode = ?`, d.Episode))
	if errors.Is(err, sql.ErrNoRows) {
		a = model.Alert{
			AlertBrief: model.AlertBrief{ID: NewID("al_", time.UnixMilli(now)), Trigger: model.Trigger(d.Trigger),
				Status: model.StatusFired, Reason: &reason, FiredAt: model.Time(d.SentMS), FireCount: 1},
			Session: strPtr(d.Session), Seat: strPtr(d.Seat), Machine: strPtr(d.Machine),
			SinceAt: model.Time(d.SinceMS), CreatedAt: model.Time(now), Episode: d.Episode,
			Facts: model.Facts{Project: d.Project, State: d.Trigger},
		}
		ok, err := tx.AlertInsert(a)
		if err != nil || !ok {
			return DirectDuplicate, a, err
		}
		return DirectCreated, a, nil
	}
	if err != nil {
		return "", a, err
	}
	switch {
	case a.Status == model.StatusPending, a.Status == model.StatusSkipped && a.Reason != nil && *a.Reason == model.ReasonStale:
		a.Status, a.Reason, a.DueAt, a.EndedAt = model.StatusFired, &reason, 0, 0
		a.FiredAt, a.FireCount = model.Time(d.SentMS), max(a.FireCount, 1)
		if err := tx.AlertSave(a, now); err != nil {
			return "", a, err
		}
		return DirectMarked, a, tx.DeliveriesSkipUnsent(a.ID, "resolved", now)
	case a.Status == model.StatusSkipped:
		return DirectKept, a, nil
	}
	return DirectDuplicate, a, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// AlertsByEpisode returns the alerts with these episodes.
func (tx *Tx) AlertsByEpisode(episodes []string) ([]model.Alert, error) {
	if len(episodes) == 0 {
		return nil, nil
	}
	var args []any
	q := `SELECT ` + alertCols + ` FROM alert WHERE ` + in("episode", episodes, &args)
	return alertRows(tx.QueryContext(tx.ctx, q+` ORDER BY created_at, id`, args...))
}

// AlertsFiredOf returns the fired, snoozed and seen alerts of these triggers that first fired
// at or before firedBy (the usage notices' life, push.md 3.9).
func (tx *Tx) AlertsFiredOf(triggers []model.Trigger, firedBy int64) ([]model.Alert, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	ts := make([]string, len(triggers))
	for i, t := range triggers {
		ts[i] = string(t)
	}
	args := []any{firedBy}
	// session_id IS NULL and the open statuses: the alert_open partial index.
	q := `SELECT ` + alertCols + ` FROM alert WHERE session_id IS NULL AND status IN ('pending', 'fired', 'snoozed', 'seen')
		AND status <> 'pending' AND fired_at IS NOT NULL AND fired_at <= ? AND ` + in("trigger", ts, &args)
	return alertRows(tx.QueryContext(tx.ctx, q+` ORDER BY created_at, id`, args...))
}
