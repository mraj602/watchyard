package store

import (
	"database/sql"
	"errors"
)

// SessionRow is the session table.
type SessionRow struct {
	ID                                              string
	SeatID, MachineID                               string
	SlotID                                          int64
	Cwd, Project, Name, Title, Kind, TranscriptPath string
	RemoteURL, Term, EndReason, Model               string
	StartedAt, EndedAt, LastEventAt, LastListedAt   int64
	MissedLists                                     int
	ContextPct                                      *float64
}

// GetSession loads a session; ok is false when it does not exist.
func (tx *Tx) GetSession(id string) (SessionRow, bool, error) {
	var s SessionRow
	var ctxPct sql.NullFloat64
	err := tx.queryRow(`SELECT id, coalesce(seat_id, ''), coalesce(machine_id, ''), coalesce(slot_id, 0), coalesce(cwd, ''),
		coalesce(project, ''), coalesce(name, ''), coalesce(title, ''), coalesce(kind, ''), coalesce(transcript_path, ''),
		coalesce(remote_url, ''), coalesce(term, ''), coalesce(end_reason, ''), coalesce(model, ''),
		coalesce(started_at, 0), coalesce(ended_at, 0), coalesce(last_event_at, 0), coalesce(last_listed_at, 0),
		missed_lists, context_pct FROM session WHERE id = ?`, id).Scan(
		&s.ID, &s.SeatID, &s.MachineID, &s.SlotID, &s.Cwd, &s.Project, &s.Name, &s.Title, &s.Kind, &s.TranscriptPath,
		&s.RemoteURL, &s.Term, &s.EndReason, &s.Model, &s.StartedAt, &s.EndedAt, &s.LastEventAt, &s.LastListedAt,
		&s.MissedLists, &ctxPct)
	if errors.Is(err, sql.ErrNoRows) {
		return s, false, nil
	}
	if ctxPct.Valid {
		v := ctxPct.Float64
		s.ContextPct = &v
	}
	return s, err == nil, err
}

// InsertSession creates a session from its first record.
func (tx *Tx) InsertSession(s SessionRow) error {
	_, err := tx.exec(`INSERT INTO session (id, seat_id, slot_id, machine_id, cwd, project, name, title, kind, transcript_path,
		remote_url, term, started_at, last_event_at, model) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, nullStr(s.SeatID), nullInt(s.SlotID), nullStr(s.MachineID), nullStr(s.Cwd), nullStr(s.Project), nullStr(s.Name),
		nullStr(s.Title), nullStr(s.Kind), nullStr(s.TranscriptPath), nullStr(s.RemoteURL), nullStr(s.Term),
		nullInt(s.StartedAt), nullInt(s.LastEventAt), nullStr(s.Model))
	if err == nil && s.SeatID != "" {
		err = tx.backfillSeat(s.ID, s.SeatID)
	}
	return err
}

// SessionInfo fills descriptive fields: each non-empty value replaces the stored one,
// except cwd/project/kind/transcript, which are kept from the first record that had them.
type SessionInfo struct {
	Cwd, Project, Kind, TranscriptPath  string // first wins
	Name, Title, RemoteURL, Term, Model string // newest wins
	StartedAt                           int64  // earliest wins
	ContextPct                          *float64
}

// PatchSession applies SessionInfo.
func (tx *Tx) PatchSession(id string, in SessionInfo) error {
	_, err := tx.exec(`UPDATE session SET
		cwd = coalesce(cwd, ?), project = coalesce(project, ?), kind = coalesce(kind, ?), transcript_path = coalesce(transcript_path, ?),
		name = coalesce(?, name), title = coalesce(?, title), remote_url = coalesce(?, remote_url), term = coalesce(?, term),
		model = coalesce(?, model), context_pct = coalesce(?, context_pct),
		started_at = CASE WHEN ? IS NULL THEN started_at WHEN started_at IS NULL OR ? < started_at THEN ? ELSE started_at END
		WHERE id = ?`,
		nullStr(in.Cwd), nullStr(in.Project), nullStr(in.Kind), nullStr(in.TranscriptPath),
		nullStr(in.Name), nullStr(in.Title), nullStr(in.RemoteURL), nullStr(in.Term), nullStr(in.Model), in.ContextPct,
		nullInt(in.StartedAt), in.StartedAt, in.StartedAt, id)
	return err
}

// SetSessionSeat gives a session its seat if it has none yet (identity.md 5.3: the first
// record that names a seat wins, for life), and attributes its token rows and limit hits.
func (tx *Tx) SetSessionSeat(id, seat string) error {
	if seat == "" {
		return nil
	}
	res, err := tx.exec(`UPDATE session SET seat_id = ? WHERE id = ? AND seat_id IS NULL`, seat, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return tx.backfillSeat(id, seat)
	}
	return nil
}

// SetSessionSlot gives a session its slot and machine if it has none.
func (tx *Tx) SetSessionSlot(id string, slot int64, machine string) error {
	_, err := tx.exec(`UPDATE session SET slot_id = coalesce(slot_id, ?), machine_id = coalesce(machine_id, ?) WHERE id = ?`,
		nullInt(slot), nullStr(machine), id)
	return err
}

// MoveSession gives a session that was resumed in another folder of its machine that
// folder and seat (identity.md 5.3). The list counters start over, since only lists of the
// new folder speak for the session from now on. Token rows and limit hits already
// attributed keep their seat.
func (tx *Tx) MoveSession(id string, slot int64, seat string) error {
	_, err := tx.exec(`UPDATE session SET slot_id = ?, seat_id = ?, last_listed_at = NULL, missed_lists = 0 WHERE id = ?`,
		nullInt(slot), nullStr(seat), id)
	if err != nil || seat == "" {
		return err
	}
	return tx.backfillSeat(id, seat)
}

func (tx *Tx) backfillSeat(id, seat string) error {
	if _, err := tx.exec(`UPDATE token_row SET seat_id = ? WHERE session_id = ? AND seat_id IS NULL`, seat, id); err != nil {
		return err
	}
	_, err := tx.exec(`UPDATE limit_hit SET seat_id = ? WHERE session_id = ? AND seat_id IS NULL`, seat, id)
	return err
}

// OpenSessionsOnSlot lists sessions of a slot that have a fold state that is not ended or
// gone (the ones a complete session_list can count misses for).
func (tx *Tx) OpenSessionsOnSlot(slotID int64) ([]string, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT s.id FROM session s JOIN session_state st ON st.session_id = s.id
		WHERE s.slot_id = ? AND st.state NOT IN ('ended', 'gone') ORDER BY s.id`, slotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FoldRow is the persisted part of a session's fold: session_state plus the fold fields
// of the session row. UpdatedAt is the ts of the newest input folded in (states.md 3,
// "Ordering": an input at or before it makes the engine refold the session).
type FoldRow struct {
	State, Detail, Question, Detector string
	Peers                             string // session_state.peers (states.md 3.1)
	LastEvent, LastRecord             string
	Since, UpdatedAt                  int64
	NeedsYou                          bool
	LastEventAt, LastListedAt         int64
	EndedAt                           int64
	EndReason                         string
	MissedLists                       int
}

// GetFold loads a session's fold row; ok is false when there is no session_state yet.
func (tx *Tx) GetFold(id string) (FoldRow, bool, error) {
	var f FoldRow
	var needs int
	err := tx.queryRow(`SELECT st.state, coalesce(st.detail, ''), coalesce(st.question, ''), coalesce(st.detector, ''),
		coalesce(st.last_event, ''), coalesce(st.last_record, ''), st.since_at, st.updated_at, st.needs_you, coalesce(st.peers, ''),
		coalesce(s.last_event_at, 0), coalesce(s.last_listed_at, 0), coalesce(s.ended_at, 0), coalesce(s.end_reason, ''), s.missed_lists
		FROM session_state st JOIN session s ON s.id = st.session_id WHERE st.session_id = ?`, id).Scan(
		&f.State, &f.Detail, &f.Question, &f.Detector, &f.LastEvent, &f.LastRecord, &f.Since, &f.UpdatedAt, &needs, &f.Peers,
		&f.LastEventAt, &f.LastListedAt, &f.EndedAt, &f.EndReason, &f.MissedLists)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	f.NeedsYou = needs == 1
	return f, err == nil, err
}

// PutFold writes a session's fold row (session_state and the session's fold columns).
func (tx *Tx) PutFold(id string, f FoldRow) error {
	needs := 0
	if f.NeedsYou {
		needs = 1
	}
	if _, err := tx.exec(`INSERT INTO session_state (session_id, state, since_at, detail, question, needs_you, last_event, last_record, detector, updated_at, peers)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET state = excluded.state, since_at = excluded.since_at, detail = excluded.detail,
		question = excluded.question, needs_you = excluded.needs_you, last_event = excluded.last_event,
		last_record = excluded.last_record, detector = excluded.detector, updated_at = excluded.updated_at, peers = excluded.peers`,
		id, f.State, f.Since, nullStr(f.Detail), nullStr(f.Question), needs, nullStr(f.LastEvent), nullStr(f.LastRecord),
		nullStr(f.Detector), f.UpdatedAt, nullStr(f.Peers)); err != nil {
		return err
	}
	_, err := tx.exec(`UPDATE session SET last_event_at = ?, last_listed_at = ?, missed_lists = ?, ended_at = ?, end_reason = ? WHERE id = ?`,
		nullInt(f.LastEventAt), nullInt(f.LastListedAt), f.MissedLists, nullInt(f.EndedAt), nullStr(f.EndReason), id)
	return err
}

// TransitionRow is one session_transition row.
type TransitionRow struct {
	At               int64
	From, To, Detail string
	Cause, RecordID  string
}

// AppendTransitions adds transitions after the session's existing ones; with replace, the
// existing ones are deleted first (refold).
func (tx *Tx) AppendTransitions(id string, ts []TransitionRow, replace bool) error {
	if replace {
		if _, err := tx.exec(`DELETE FROM session_transition WHERE session_id = ?`, id); err != nil {
			return err
		}
	}
	if len(ts) == 0 {
		return nil
	}
	var next int64
	if err := tx.queryRow(`SELECT coalesce(max(seq) + 1, 0) FROM session_transition WHERE session_id = ?`, id).Scan(&next); err != nil {
		return err
	}
	for i, t := range ts {
		if _, err := tx.exec(`INSERT INTO session_transition (session_id, seq, at, from_state, to_state, detail, cause, record_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, next+int64(i), t.At, nullStr(t.From), t.To, nullStr(t.Detail), t.Cause, nullStr(t.RecordID)); err != nil {
			return err
		}
	}
	return nil
}

// HookEventRow is one hook_event row.
type HookEventRow struct {
	RecordID, SessionID, Event, Subtype, ToolName string
	TS                                            int64
	LastMessage                                   *string
	StateAfter                                    string
}

// InsertHookEvent stores a typed hook event.
func (tx *Tx) InsertHookEvent(h HookEventRow) error {
	_, err := tx.exec(`INSERT INTO hook_event (record_id, session_id, event, subtype, tool_name, ts, last_message, state_after)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(record_id) DO UPDATE SET session_id = excluded.session_id, event = excluded.event,
		subtype = excluded.subtype, tool_name = excluded.tool_name, ts = excluded.ts, last_message = excluded.last_message,
		state_after = excluded.state_after`,
		h.RecordID, h.SessionID, h.Event, nullStr(h.Subtype), nullStr(h.ToolName), h.TS, h.LastMessage, nullStr(h.StateAfter))
	return err
}

// SetStateAfter sets hook_event.state_after for a record.
func (tx *Tx) SetStateAfter(recordID, state string) error {
	_, err := tx.exec(`UPDATE hook_event SET state_after = ? WHERE record_id = ?`, nullStr(state), recordID)
	return err
}

// SessionIDs lists every session id, sorted.
func (tx *Tx) SessionIDs() ([]string, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT id FROM session ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetSessionStart replaces started_at (0 = unknown, NULL). Only wy hub refold uses it:
// ingest keeps the earliest start (PatchSession).
func (tx *Tx) SetSessionStart(id string, at int64) error {
	_, err := tx.exec(`UPDATE session SET started_at = ? WHERE id = ?`, nullInt(at), id)
	return err
}
