package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
)

// The Smart Account Switch log (record.md 4.13, setup.md 8.6 Switch log): one switch_move row
// per move of a session to another account, read by the board's Switches page.

// SwitchMove is one row of the log as the API shows it.
type SwitchMove struct {
	ID           string   `json:"id"`
	At           Time     `json:"at"`
	MachineID    string   `json:"machine_id"`
	MachineName  string   `json:"machine_name"`
	SessionID    string   `json:"session_id"`
	SessionName  string   `json:"session_name"`
	SessionKnown bool     `json:"session_known"` // the hub has the session: its page opens
	Kind         string   `json:"kind"`
	Interactive  bool     `json:"interactive"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	Auto         bool     `json:"auto"`
	Reason       string   `json:"reason"`
	Window       string   `json:"window"`
	P5           *float64 `json:"p5"`
	P7           *float64 `json:"p7"`
	Cap5         *float64 `json:"cap5"`
	Cap7         *float64 `json:"cap7"`
	Burn5        *float64 `json:"burn5"`
	CrossOrg     bool     `json:"cross_org"`
	// MoveNo is this move's number in its session's path (1 for the first move that worked);
	// null for a failed move.
	MoveNo *int `json:"move_no"`
	// Result is "resumed" or "failed"; Error is why it failed.
	Result string  `json:"result"`
	Error  *string `json:"error"`
}

// SwitchMoveInsert is a switch_move row to store.
type SwitchMoveInsert struct {
	RecordID, Machine, SessionID, SessionName, Kind, From, To, Reason, Window, Error string
	At                                                                               int64
	Interactive, Auto, CrossOrg                                                      bool
	P5, P7, Cap5, Cap7, Burn5                                                        *float64
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// InsertSwitchMove stores a move once per record; a replayed record changes nothing.
func (tx *Tx) InsertSwitchMove(m SwitchMoveInsert) error {
	_, err := tx.exec(`INSERT INTO switch_move (record_id, machine_id, at, session_id, session_name, kind, interactive, from_label,
		to_label, auto, reason, window, p5, p7, cap5, cap7, burn5, cross_org, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(record_id) DO NOTHING`,
		m.RecordID, m.Machine, m.At, m.SessionID, nullStr(m.SessionName), m.Kind, b2i(m.Interactive), m.From, m.To, b2i(m.Auto),
		nullStr(m.Reason), nullStr(m.Window), m.P5, m.P7, m.Cap5, m.Cap7, m.Burn5, b2i(m.CrossOrg), nullStr(m.Error))
	return err
}

// SwitchFilter selects moves for GET /v1/switches. Empty fields do not filter.
type SwitchFilter struct {
	Machine string
	Account string // a label: the account it left or the one it went to
	Session string
	Auto    *bool
	Failed  *bool
	Since   int64 // ms, inclusive; 0 none
	Until   int64 // ms, exclusive; 0 none
	Cursor  string
	Limit   int
}

// SwitchAccountCount is how many moves left an account and how many arrived.
type SwitchAccountCount struct {
	Account string `json:"account"`
	Out     int    `json:"out"`
	In      int    `json:"in"`
}

// SwitchSummary is the strip above the log, for the same filter (cursor aside).
type SwitchSummary struct {
	Moves     int                  `json:"moves"` // moves that resumed the session
	Failed    int                  `json:"failed"`
	Auto      int                  `json:"auto"`
	Manual    int                  `json:"manual"`
	Sessions  int                  `json:"sessions"`       // sessions moved
	Sessions2 int                  `json:"sessions_twice"` // sessions moved 2 or more times
	ByAccount []SwitchAccountCount `json:"by_account"`
}

const switchCols = `m.record_id, m.at, m.machine_id, coalesce(ma.name, ''), m.session_id, coalesce(m.session_name, ''),
	EXISTS (SELECT 1 FROM session s WHERE s.id = m.session_id), m.kind, m.interactive, m.from_label, m.to_label, m.auto,
	coalesce(m.reason, ''), coalesce(m.window, ''), m.p5, m.p7, m.cap5, m.cap7, m.burn5, m.cross_org, m.error,
	CASE WHEN m.error IS NULL THEN (SELECT count(*) FROM switch_move x WHERE x.machine_id = m.machine_id AND x.session_id = m.session_id
		AND x.error IS NULL AND (x.at < m.at OR (x.at = m.at AND x.record_id <= m.record_id))) END`

func (f SwitchFilter) where(withCursor bool) (string, []any, error) {
	var w []string
	var a []any
	if f.Machine != "" {
		w = append(w, "m.machine_id = ?")
		a = append(a, f.Machine)
	}
	if f.Account != "" {
		w = append(w, "(m.from_label = ? COLLATE NOCASE OR m.to_label = ? COLLATE NOCASE)")
		a = append(a, f.Account, f.Account)
	}
	if f.Session != "" {
		w = append(w, "m.session_id = ?")
		a = append(a, f.Session)
	}
	if f.Auto != nil {
		w = append(w, "m.auto = ?")
		a = append(a, b2i(*f.Auto))
	}
	if f.Failed != nil {
		if *f.Failed {
			w = append(w, "m.error IS NOT NULL")
		} else {
			w = append(w, "m.error IS NULL")
		}
	}
	if f.Since > 0 {
		w = append(w, "m.at >= ?")
		a = append(a, f.Since)
	}
	if f.Until > 0 {
		w = append(w, "m.at < ?")
		a = append(a, f.Until)
	}
	if withCursor && f.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		c, id, ok := strings.Cut(string(raw), ":")
		n, perr := strconv.ParseInt(c, 10, 64)
		if err != nil || !ok || perr != nil {
			return "", nil, ErrBadCursor
		}
		w = append(w, "(m.at < ? OR (m.at = ? AND m.record_id < ?))")
		a = append(a, n, n, id)
	}
	if len(w) == 0 {
		return "", a, nil
	}
	return " WHERE " + strings.Join(w, " AND "), a, nil
}

// SwitchMoves lists the moves newest first, one page, with the cursor of the next ("" at the
// end), and the summary of everything the filter matches.
func (s *Store) SwitchMoves(ctx context.Context, f SwitchFilter) ([]SwitchMove, string, SwitchSummary, error) {
	var sum SwitchSummary
	where, args, err := f.where(true)
	if err != nil {
		return nil, "", sum, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+switchCols+` FROM switch_move m LEFT JOIN machine ma ON ma.id = m.machine_id`+where+
		` ORDER BY m.at DESC, m.record_id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", sum, err
	}
	defer rows.Close()
	out := []SwitchMove{}
	for rows.Next() {
		var m SwitchMove
		var at int64
		var inter, auto, cross int
		var errText sql.NullString
		var no sql.NullInt64
		if err := rows.Scan(&m.ID, &at, &m.MachineID, &m.MachineName, &m.SessionID, &m.SessionName, &m.SessionKnown, &m.Kind, &inter,
			&m.From, &m.To, &auto, &m.Reason, &m.Window, &m.P5, &m.P7, &m.Cap5, &m.Cap7, &m.Burn5, &cross, &errText, &no); err != nil {
			return nil, "", sum, err
		}
		m.At, m.Interactive, m.Auto, m.CrossOrg = Time(at), inter == 1, auto == 1, cross == 1
		m.Result = "resumed"
		if errText.Valid {
			m.Result, m.Error = "failed", &errText.String
		}
		if no.Valid {
			n := int(no.Int64)
			m.MoveNo = &n
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", sum, err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = alertCursor(int64(last.At), last.ID)
	}
	sum, err = s.switchSummary(ctx, f)
	return out, next, sum, err
}

func (s *Store) switchSummary(ctx context.Context, f SwitchFilter) (SwitchSummary, error) {
	sum := SwitchSummary{ByAccount: []SwitchAccountCount{}}
	where, args, err := f.where(false)
	if err != nil {
		return sum, err
	}
	ok := "m.error IS NULL"
	if where == "" {
		ok = " WHERE " + ok
	} else {
		ok = where + " AND " + ok
	}
	if err := s.r.QueryRowContext(ctx, `SELECT coalesce(sum(m.error IS NULL), 0), coalesce(sum(m.error IS NOT NULL), 0),
		coalesce(sum(m.error IS NULL AND m.auto = 1), 0), coalesce(sum(m.error IS NULL AND m.auto = 0), 0)
		FROM switch_move m`+where, args...).Scan(&sum.Moves, &sum.Failed, &sum.Auto, &sum.Manual); err != nil {
		return sum, err
	}
	if err := s.r.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(n >= 2), 0) FROM
		(SELECT count(*) AS n FROM switch_move m`+ok+` GROUP BY m.machine_id, m.session_id)`, args...).Scan(&sum.Sessions, &sum.Sessions2); err != nil {
		return sum, err
	}
	by := map[string]*SwitchAccountCount{}
	get := func(label string) *SwitchAccountCount {
		k := strings.ToLower(label)
		if by[k] == nil {
			by[k] = &SwitchAccountCount{Account: label}
		}
		return by[k]
	}
	for _, side := range []string{"from_label", "to_label"} {
		rows, err := s.r.QueryContext(ctx, `SELECT m.`+side+`, count(*) FROM switch_move m`+ok+` GROUP BY m.`+side+` COLLATE NOCASE`, args...)
		if err != nil {
			return sum, err
		}
		for rows.Next() {
			var l string
			var n int
			if err := rows.Scan(&l, &n); err != nil {
				rows.Close()
				return sum, err
			}
			if side == "from_label" {
				get(l).Out += n
			} else {
				get(l).In += n
			}
		}
		if err := rows.Err(); err != nil {
			return sum, err
		}
		rows.Close()
	}
	for _, c := range by {
		sum.ByAccount = append(sum.ByAccount, *c)
	}
	sort.Slice(sum.ByAccount, func(i, j int) bool {
		a, b := sum.ByAccount[i], sum.ByAccount[j]
		if a.In+a.Out != b.In+b.Out {
			return a.In+a.Out > b.In+b.Out
		}
		return a.Account < b.Account
	})
	return sum, nil
}
