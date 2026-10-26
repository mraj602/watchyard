package status

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/record"
)

// Session is everything the local view learned about one session id, before folding.
type Session struct {
	ID         string
	Slot       string
	Seat       *record.Seat
	Cwd        string // newest known cwd, as written in the record (not ~-normalised)
	Inputs     []state.Input
	ContextPct *float64
	contextAt  time.Time
}

// Adapt turns outbox records into one state.Input stream per session. It is the only place
// that knows both record and hub/state (hub/state itself never imports record).
//
//   - hook_event -> a state.Hook input for payload.session_id (derived.last_message and
//     derived.question feed the question detector).
//   - session_list -> one state.ListObs input per session of that slot that was known
//     before the list: listed (with its status) or missing. A complete list that omits a
//     session is what makes it gone. Sessions only ever seen in a list also get a stream.
//   - limit_sample -> the session's context percentage (derived.context_pct), newest wins.
//
// Records may come in any order; they are applied in (ts, id) order.
func Adapt(recs []*record.Record) map[string]*Session {
	type stamped struct {
		r  *record.Record
		ts time.Time
	}
	list := make([]stamped, 0, len(recs))
	for _, r := range recs {
		if t, err := r.Time(); err == nil {
			list = append(list, stamped{r, t})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].ts.Equal(list[j].ts) {
			return list[i].ts.Before(list[j].ts)
		}
		return list[i].r.ID < list[j].r.ID
	})

	sessions := map[string]*Session{}
	bySlot := map[string][]string{} // slot -> session ids, in order of first sight
	get := func(id, slot string, seat *record.Seat) *Session {
		s, ok := sessions[id]
		if !ok {
			s = &Session{ID: id, Slot: slot}
			sessions[id] = s
			bySlot[slot] = append(bySlot[slot], id)
		}
		if s.Seat == nil && seat != nil {
			s.Seat = seat // the first record that names a seat decides (identity.md 5.3)
		}
		return s
	}

	for _, it := range list {
		r := it.r
		switch r.Kind {
		case record.KindHookEvent:
			var p struct {
				SessionID string `json:"session_id"`
				Cwd       string `json:"cwd"`
			}
			if json.Unmarshal(r.Payload, &p) != nil || p.SessionID == "" {
				continue
			}
			in, err := state.HookInput(r.ID, it.ts, r.Payload, derivedString(r, "last_message"))
			if err != nil {
				continue
			}
			if q := derivedString(r, "question"); q != nil {
				in.Hook.Question = *q
			}
			in.Hook.PeerFacts(r.Derived)
			s := get(p.SessionID, r.Slot, r.Seat)
			if r.Slot != "" && s.Slot != r.Slot {
				// Resumed in another folder (identity.md 5.3): it moves there, with that
				// folder's seat, and only that folder's lists speak for it from now on.
				old := bySlot[s.Slot]
				for i, id := range old {
					if id == s.ID {
						bySlot[s.Slot] = append(old[:i:i], old[i+1:]...)
						break
					}
				}
				bySlot[r.Slot] = append(bySlot[r.Slot], s.ID)
				s.Slot, s.Seat = r.Slot, r.Seat
			}
			if p.Cwd != "" {
				s.Cwd = p.Cwd
			}
			s.Inputs = append(s.Inputs, in)

		case record.KindLimitSample:
			var p struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(r.Payload, &p) != nil || p.SessionID == "" {
				continue
			}
			s, ok := sessions[p.SessionID]
			if !ok {
				continue // no hook record for it (yet): nothing to attach the number to
			}
			if pct := derivedFloat(r, "context_pct"); pct != nil && it.ts.After(s.contextAt) {
				s.ContextPct, s.contextAt = pct, it.ts
			}

		case record.KindSessionList:
			var p record.SessionListPayload
			if r.DecodePayload(&p) != nil {
				continue
			}
			type listed struct{ status, cwd string }
			in := map[string]listed{}
			var order []string
			for _, raw := range p.Sessions {
				var x struct {
					SessionID string `json:"sessionId"`
					Cwd       string `json:"cwd"`
					Status    string `json:"status"`
				}
				if json.Unmarshal(raw, &x) != nil || x.SessionID == "" {
					continue
				}
				if _, dup := in[x.SessionID]; !dup {
					order = append(order, x.SessionID)
				}
				in[x.SessionID] = listed{x.Status, x.Cwd}
			}
			for _, id := range order {
				s := get(id, r.Slot, r.Seat)
				if s.Cwd == "" {
					s.Cwd = in[id].cwd
				}
			}
			for _, id := range bySlot[r.Slot] {
				l, ok := in[id]
				s := sessions[id]
				s.Inputs = append(s.Inputs, state.Input{ID: r.ID, TS: it.ts,
					List: &state.ListObs{Complete: p.Complete, Listed: ok, Status: l.status}})
			}
		}
	}
	return sessions
}

func derivedString(r *record.Record, key string) *string {
	raw, ok := r.Derived[key]
	if !ok {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return &s
}

func derivedFloat(r *record.Record, key string) *float64 {
	raw, ok := r.Derived[key]
	if !ok {
		return nil
	}
	var f *float64
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f
}
