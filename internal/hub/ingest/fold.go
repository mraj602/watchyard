package ingest

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/hub/usage"
	"github.com/mraj602/watchyard/internal/record"
)

// The hub side of the state engine (states.md): the pure fold of internal/hub/state,
// applied one input at a time and persisted per session in session_state, the session
// row's fold columns and session_transition. session_state.updated_at is the ts of the
// newest input folded in; an input at or before it (a late record) makes the engine refold
// the session from its stored records, so the result never depends on arrival order.

// hookInput adapts a hook_event record to a fold input.
func hookInput(id string, ts time.Time, r *record.Record) (state.Input, error) {
	var lm *string
	if s, ok := derivedStr(r, "last_message"); ok {
		lm = &s
	}
	in, err := state.HookInput(id, ts, r.Payload, lm)
	if err != nil {
		return in, err
	}
	if q, ok := derivedStr(r, "question"); ok {
		in.Hook.Question = q
	}
	in.Hook.PeerFacts(r.Derived)
	return in, nil
}

func listInput(id string, ts time.Time, complete, listed bool, status string, lastActive int64) state.Input {
	return state.Input{ID: id, TS: ts, List: &state.ListObs{Complete: complete, Listed: listed, Status: status,
		LastActive: tm(lastActive)}}
}

// listLastActive is ListObs.LastActive for a session in a list taken at (ms): the newer of
// its newest transcript row at or before at (lastRow) and the start the list item reports
// (when it is not after at). Never the list's own time.
func listLastActive(it listItem, at, lastRow int64) int64 {
	started := usage.ParseTime(it.StartedAt).UnixMilli()
	if started <= 0 || started > at {
		started = 0
	}
	return max(started, lastRow)
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func tm(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func sessionFrom(f store.FoldRow) state.Session {
	return state.Session{
		State: state.State(f.State), Detail: f.Detail, Since: tm(f.Since), Question: f.Question, Detector: f.Detector, Peers: f.Peers,
		LastEventAt: tm(f.LastEventAt), LastListedAt: tm(f.LastListedAt), MissedLists: f.MissedLists,
		EndedAt: tm(f.EndedAt), EndReason: f.EndReason,
	}
}

func foldRowOf(s *state.Session, updated int64, lastEvent, lastRecord string) store.FoldRow {
	return store.FoldRow{
		State: string(s.State), Detail: s.Detail, Question: s.Question, Detector: s.Detector, Peers: s.Peers,
		LastEvent: lastEvent, LastRecord: lastRecord, Since: ms(s.Since), UpdatedAt: updated, NeedsYou: s.NeedsYou(),
		LastEventAt: ms(s.LastEventAt), LastListedAt: ms(s.LastListedAt), EndedAt: ms(s.EndedAt), EndReason: s.EndReason,
		MissedLists: s.MissedLists,
	}
}

func transitionsOf(h []state.Transition, recordID string) []store.TransitionRow {
	out := make([]store.TransitionRow, 0, len(h))
	for _, x := range h {
		out = append(out, store.TransitionRow{At: ms(x.At), From: string(x.From), To: string(x.To), Detail: x.Detail,
			Cause: x.Cause, RecordID: recordID})
	}
	return out
}

// fold applies one input to a session: incrementally when it is newer than everything
// folded so far, else by refolding the session.
func (t *typer) fold(sessionID string, in state.Input) error {
	f, ok, err := t.tx.GetFold(sessionID)
	if err != nil {
		return err
	}
	at := ms(in.TS)
	if ok && at <= f.UpdatedAt {
		return t.refold(sessionID)
	}
	var s state.Session
	if ok {
		s = sessionFrom(f)
	}
	s.Apply(in)
	if s.State == "" {
		return nil // nothing to persist yet (an unlisted session with no state)
	}
	lastEvent, lastRecord := f.LastEvent, f.LastRecord
	if n := len(s.History); n > 0 {
		lastEvent, lastRecord = s.History[n-1].Cause, in.ID
	}
	if err := t.tx.PutFold(sessionID, foldRowOf(&s, max(at, f.UpdatedAt), lastEvent, lastRecord)); err != nil {
		return err
	}
	if err := t.tx.AppendTransitions(sessionID, transitionsOf(s.History, in.ID), false); err != nil {
		return err
	}
	if in.Hook != nil {
		return t.tx.SetStateAfter(in.ID, string(s.State))
	}
	return nil
}

// transcript folds a transcript observation (token_rows) into a session. It only matters
// for a session no hook event or list has seen (states.md 5.1): a session with a live state
// ignores it, and a history session only moves forward. The one exception: a session a list
// made done (done:repaired) refolds when it now has a transcript row after its state_since
// but not after that list, so the list's backdating (ListObs.LastActive) sees rows that
// arrived after the list (the first list on a machine usually beats the import of its
// transcripts). The caller stores the rows first.
func (t *typer) transcript(sessionID string, at time.Time, recordID string) error {
	f, ok, err := t.tx.GetFold(sessionID)
	if err != nil {
		return err
	}
	var s state.Session
	if ok {
		s = sessionFrom(f)
		if s.State == state.Done && s.Detail == "repaired" {
			return t.relistDone(sessionID, f)
		}
		if !s.IsHistory() || !at.After(s.EndedAt) {
			return nil
		}
	}
	s.Apply(state.Input{ID: recordID, TS: at, Transcript: &state.TranscriptObs{}})
	lastEvent, lastRecord := f.LastEvent, f.LastRecord
	if n := len(s.History); n > 0 {
		lastEvent, lastRecord = s.History[n-1].Cause, recordID
	}
	if err := t.tx.PutFold(sessionID, foldRowOf(&s, max(ms(at), f.UpdatedAt), lastEvent, lastRecord)); err != nil {
		return err
	}
	return t.tx.AppendTransitions(sessionID, transitionsOf(s.History, recordID), false)
}

// relistDone refolds a session a list made done when a transcript row now lies between its
// state_since and that list (f.LastRecord): the list's LastActive has moved (transcript).
func (t *typer) relistDone(sessionID string, f store.FoldRow) error {
	listTS, ok, err := t.tx.RecordTS(f.LastRecord)
	if err != nil || !ok {
		return err
	}
	row, err := t.tx.LastTokenRowAt(sessionID, listTS)
	if err != nil || row <= f.Since {
		return err
	}
	return t.refold(sessionID)
}

// refold rebuilds a session's fold from its hook_event records and the session_list
// records of its slot.
func (t *typer) refold(sessionID string) error {
	inputs, err := loadInputs(t.tx, sessionID)
	if err != nil {
		return err
	}
	return persistFold(t.tx, sessionID, inputs)
}

// folderSpan is a stretch of a session's life in one folder (identity.md 5.3): from the
// first hook event sent from it until the first one sent from another (0: still there).
// Only that folder's session lists in that stretch speak for the session.
type folderSpan struct {
	folder      string
	from, until int64
}

// loadInputs reads every fold input of a session from the record table.
func loadInputs(tx *store.Tx, sessionID string) ([]state.Input, error) {
	sess, ok, err := tx.GetSession(sessionID)
	if err != nil || !ok {
		return nil, err
	}
	hooks, err := tx.HookRecordsOf(sessionID)
	if err != nil {
		return nil, err
	}
	var inputs []state.Input
	var rows []int64 // the session's transcript activity, loaded with the first list that lists it
	var spans []folderSpan
	from := sess.StartedAt
	for _, h := range hooks {
		var r record.Record
		if err := json.Unmarshal(h.Body, &r); err != nil {
			return nil, fmt.Errorf("refold %s: record %s: %w", sessionID, h.ID, err)
		}
		in, err := hookInput(r.ID, tm(h.TS), &r)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, in)
		if from == 0 || h.TS < from {
			from = h.TS
		}
		if r.Slot != "" && (len(spans) == 0 || spans[len(spans)-1].folder != r.Slot) {
			if len(spans) > 0 {
				spans[len(spans)-1].until = h.TS
			}
			spans = append(spans, folderSpan{folder: r.Slot, from: h.TS})
		}
	}
	if sess.MachineID == "" {
		return inputs, nil
	}
	if len(spans) == 0 {
		if sess.SlotID == 0 {
			return inputs, nil
		}
		folder, err := tx.SlotFolder(sess.SlotID)
		if err != nil {
			return nil, err
		}
		spans = []folderSpan{{folder: folder}}
	}
	spans[0].from = from
	for _, sp := range spans {
		lists, err := tx.SessionListRecords(sess.MachineID, sp.folder, sp.from)
		if err != nil {
			return nil, err
		}
		for _, l := range lists {
			if sp.until != 0 && l.TS >= sp.until {
				break
			}
			var r struct {
				Payload record.SessionListPayload `json:"payload"`
			}
			if json.Unmarshal(l.Body, &r) != nil {
				continue
			}
			listed, status, active := false, "", int64(0)
			for _, raw := range r.Payload.Sessions {
				var it listItem
				if json.Unmarshal(raw, &it) == nil && it.SessionID == sessionID {
					if rows == nil {
						if rows, err = tx.TokenRowTimes(sessionID); err != nil {
							return nil, err
						}
						if rows == nil {
							rows = []int64{}
						}
					}
					listed, status = true, it.Status
					active = listLastActive(it, l.TS, lastAtOrBefore(rows, l.TS))
					break
				}
			}
			if !listed && !r.Payload.Complete {
				continue
			}
			inputs = append(inputs, listInput(l.ID, tm(l.TS), r.Payload.Complete, listed, status, active))
		}
	}
	return inputs, nil
}

// persistFold folds inputs from scratch and replaces the session's persisted fold.
func persistFold(tx *store.Tx, sessionID string, inputs []state.Input) error {
	sort.SliceStable(inputs, func(i, j int) bool {
		if !inputs[i].TS.Equal(inputs[j].TS) {
			return inputs[i].TS.Before(inputs[j].TS)
		}
		return inputs[i].ID < inputs[j].ID
	})
	var s state.Session
	var trans []store.TransitionRow
	var updated int64
	var lastEvent, lastRecord string
	after := map[string]string{}
	for _, in := range inputs {
		n := len(s.History)
		s.Apply(in)
		for _, x := range s.History[n:] {
			trans = append(trans, transitionsOf([]state.Transition{x}, in.ID)...)
			lastEvent, lastRecord = x.Cause, in.ID
		}
		updated = max(updated, ms(in.TS))
		if in.Hook != nil {
			after[in.ID] = string(s.State)
		}
	}
	if s.State == "" {
		return nil
	}
	if err := tx.PutFold(sessionID, foldRowOf(&s, updated, lastEvent, lastRecord)); err != nil {
		return err
	}
	if err := tx.AppendTransitions(sessionID, trans, true); err != nil {
		return err
	}
	for id, st := range after {
		if err := tx.SetStateAfter(id, st); err != nil {
			return err
		}
	}
	return nil
}

// lastAtOrBefore is the newest of the sorted times that is at or before at, 0 if none.
func lastAtOrBefore(sorted []int64, at int64) int64 {
	i := sort.Search(len(sorted), func(i int) bool { return sorted[i] > at })
	if i == 0 {
		return 0
	}
	return sorted[i-1]
}
