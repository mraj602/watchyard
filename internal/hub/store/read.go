package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// scanAlertBrief decodes sessionAlertExpr's JSON (nil on anything unexpected).
func scanAlertBrief(raw string) *model.AlertBrief {
	var r struct {
		ID           string  `json:"id"`
		Trigger      string  `json:"trigger"`
		Status       string  `json:"status"`
		Reason       *string `json:"reason"`
		DueAt        int64   `json:"due_at"`
		FiredAt      int64   `json:"fired_at"`
		FireCount    int     `json:"fire_count"`
		SnoozedUntil int64   `json:"snoozed_until"`
		SeenAt       int64   `json:"seen_at"`
	}
	if json.Unmarshal([]byte(raw), &r) != nil || r.ID == "" {
		return nil
	}
	return &model.AlertBrief{ID: r.ID, Trigger: model.Trigger(r.Trigger), Status: model.Status(r.Status), Reason: r.Reason,
		DueAt: model.Time(r.DueAt), FiredAt: model.Time(r.FiredAt), FireCount: r.FireCount,
		SnoozedUntil: model.Time(r.SnoozedUntil), SeenAt: model.Time(r.SeenAt)}
}

// Read models for the API (api.yaml components/schemas). The read pool is used, so these
// never wait on ingest.

// Time is a millisecond timestamp that encodes as an RFC 3339 UTC string with ms, or null.
type Time int64

// MarshalJSON implements json.Marshaler.
func (t Time) MarshalJSON() ([]byte, error) {
	if t == 0 {
		return []byte("null"), nil
	}
	return []byte(`"` + time.UnixMilli(int64(t)).UTC().Format("2006-01-02T15:04:05.000Z") + `"`), nil
}

// Thresholds from states.md 5.
const (
	StaleAfter      = 12 * time.Hour
	MachineSilent   = 15 * time.Minute
	LiveWithin      = 12 * time.Hour // a done session idle this long is dormant (states.md 5)
	TimelineMax     = 500
	activeStatesSQL = `'working', 'needs_permission', 'asked'`
	// activityExpr is a session's last sign of life (states.md 5): its newest hook event,
	// state change, start or end. Not its last listing: `claude agents` lists an idle
	// session every 5 minutes for as long as its process runs, which says nothing about use.
	activityExpr     = `max(coalesce(s.last_event_at, 0), coalesce(st.since_at, 0), coalesce(s.started_at, 0), coalesce(s.ended_at, 0))`
	sessionStateExpr = `coalesce(st.state, 'done')`
	sessionNeedsExpr = `coalesce(st.needs_you, 0)`
	sessionSinceExpr = `coalesce(st.since_at, s.started_at, s.last_event_at, 0)`
	liveCondition    = sessionStateExpr + ` NOT IN ('ended', 'gone')`
	// liveNowCondition is "open and not dormant" (states.md 5): needs you, or any other open
	// state but done unless it is stale, or done with activity in the last LiveWithin. Its two
	// ? are both now - 12 h (liveArgs): every live count (overview, machines, accounts) uses it,
	// and the board's isLive (components/live.js) is the same rule.
	liveNowCondition = liveCondition + ` AND (` + sessionNeedsExpr + ` = 1 OR (` + sessionStateExpr + ` <> 'done' AND NOT (` +
		sessionStateExpr + ` = 'working' AND coalesce(s.last_listed_at, 0) = 0 AND coalesce(s.last_event_at, 0) > 0 AND s.last_event_at < ?)) OR ` +
		activityExpr + ` >= ?)`
	sessionSelectCols = `s.id, ` + sessionStateExpr + `, st.detail, ` + sessionNeedsExpr + `, ` + sessionSinceExpr + `, st.question,
	(SELECT h.last_message FROM hook_event h WHERE h.session_id = s.id AND h.event IN ('Stop', 'StopFailure') ORDER BY h.ts DESC LIMIT 1),
	s.seat_id, se.label, se.colour, s.machine_id, m.name, sl.folder, sl.launcher, s.project, s.cwd, s.name, s.title, s.kind,
	s.model, s.context_pct, s.started_at, s.last_event_at, s.ended_at, s.remote_url, s.term, coalesce(s.last_listed_at, 0),
	coalesce(m.last_seen, 0), ` + activityExpr + `, ` + sessionAlertExpr + `, ` + sessionWaitExpr
	// sessionWaitExpr is what a waiting session waits on, from the events since its state
	// began (the same inputs as a push, push.md 5.5): the tool of the newest PermissionRequest,
	// PreToolUse (AskUserQuestion, ExitPlanMode) or Notification, and Claude Code's own text of
	// the newest Notification. Only for needs_permission and asked; NULL otherwise.
	sessionWaitExpr = `CASE WHEN st.state IN ('needs_permission', 'asked') THEN
	(SELECT h.tool_name FROM hook_event h WHERE h.session_id = s.id AND h.tool_name IS NOT NULL
	   AND h.event IN ('PermissionRequest', 'PreToolUse', 'Notification') AND h.ts >= coalesce(st.since_at, 0) ORDER BY h.ts DESC LIMIT 1) END,
	CASE WHEN st.state IN ('needs_permission', 'asked') THEN
	(SELECT r.body ->> '$.payload.message' FROM hook_event h JOIN record r ON r.id = h.record_id
	   WHERE h.session_id = s.id AND h.event = 'Notification' AND h.ts >= coalesce(st.since_at, 0) ORDER BY h.ts DESC LIMIT 1) END`
	// sessionAlertExpr is the alert of the current episode while it is open or skipped (push.md
	// 4, 10.1), as JSON or NULL. A skipped one (stale, rule_off, peers, …) is listed too: its
	// episode is the current one, so it is still open, and Seen on it is what quiets the row.
	sessionAlertExpr = `(SELECT json_object('id', a.id, 'trigger', a.trigger, 'status', a.status, 'reason', a.reason,
	'due_at', a.due_at, 'fired_at', a.fired_at, 'fire_count', a.fire_count, 'snoozed_until', a.snoozed_until, 'seen_at', a.seen_at)
	FROM alert a WHERE a.session_id = s.id AND a.status IN ('pending', 'fired', 'snoozed', 'seen', 'skipped')
	AND a.episode = 's:' || s.id || ':' || st.state || ':' || st.since_at LIMIT 1)`
	// sessionSeenExpr is 1 when the current episode's alert is seen (push.md 4): the session
	// is hidden from the needs-you list and counts until its state changes (a new episode).
	sessionSeenExpr = `EXISTS (SELECT 1 FROM alert a WHERE a.session_id = s.id AND a.status = 'seen'
	AND a.episode = 's:' || s.id || ':' || st.state || ':' || st.since_at)`
	sessionFrom = ` FROM session s LEFT JOIN session_state st ON st.session_id = s.id LEFT JOIN seat se ON se.id = s.seat_id
	LEFT JOIN machine m ON m.id = s.machine_id LEFT JOIN slot sl ON sl.id = s.slot_id`
)

// SessionView is api.yaml Session.
type SessionView struct {
	ID          string  `json:"id"`
	State       string  `json:"state"`
	Detail      *string `json:"detail"`
	NeedsYou    bool    `json:"needs_you"`
	StateSince  Time    `json:"state_since"`
	Question    *string `json:"question"`
	LastMessage *string `json:"last_message"`
	// ToolName and Notification: what a needs_permission or asked session waits on
	// (sessionWaitExpr); null in other states.
	ToolName     *string  `json:"tool_name"`
	Notification *string  `json:"notification"`
	Seat         *string  `json:"seat"`
	SeatLabel    *string  `json:"seat_label"`
	SeatColour   *string  `json:"seat_colour"`
	Machine      *string  `json:"machine"`
	MachineName  *string  `json:"machine_name"`
	Slot         *string  `json:"slot"`
	Project      *string  `json:"project"`
	Cwd          *string  `json:"cwd"`
	Name         *string  `json:"name"`
	Title        *string  `json:"title"`
	Kind         *string  `json:"kind"`
	Model        *string  `json:"model"`
	ContextPct   *float64 `json:"context_pct"`
	StartedAt    Time     `json:"started_at"`
	LastEventAt  Time     `json:"last_event_at"`
	EndedAt      Time     `json:"ended_at"`
	RemoteURL    *string  `json:"remote_url"`
	Resume       *string  `json:"resume"`
	FocusURL     *string  `json:"focus_url"`
	Stale        bool     `json:"stale"`
	MachineStale bool     `json:"machine_stale"`
	// Dormant: open (not ended or gone), not needing you, and no activity for LiveWithin
	// (states.md 5): computed at read time, not a state. Not counted in counts.live.
	Dormant bool `json:"dormant"`
	// Activity is the newest of last_event_at, state_since, started_at and ended_at: what
	// the open rule, the days window and the order use. Null when nothing is known
	// (imported history with no times).
	Activity Time `json:"activity_at"`
	// Alert is the open alert of this session's current episode, or null.
	Alert *model.AlertBrief `json:"alert"`

	// FocusTarget/FocusLink: the terminal's focus URL and the machine allowed to see it
	// (record.md 3.3). The API copies FocusLink into FocusURL for that machine's desktop.
	FocusTarget string `json:"-"`
	FocusLink   string `json:"-"`
}

func ns(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func scanSession(sc interface{ Scan(...any) error }, now time.Time) (SessionView, error) {
	var v SessionView
	var detail, question, lm, seat, label, colour, machine, mname, folder, launcher, project, cwd, name, title, kind, model, remote, term sql.NullString
	var ctxPct sql.NullFloat64
	var started, lastEv, ended sql.NullInt64
	var needs int
	var since, listed, lastSeen, activity int64
	var alertJSON, tool, notif sql.NullString
	err := sc.Scan(&v.ID, &v.State, &detail, &needs, &since, &question, &lm, &seat, &label, &colour, &machine, &mname, &folder,
		&launcher, &project, &cwd, &name, &title, &kind, &model, &ctxPct, &started, &lastEv, &ended, &remote, &term, &listed,
		&lastSeen, &activity, &alertJSON, &tool, &notif)
	if err != nil {
		return v, err
	}
	v.Detail, v.Question, v.LastMessage = ns(detail), ns(question), ns(lm)
	v.ToolName, v.Notification = ns(tool), ns(notif)
	v.NeedsYou, v.StateSince = needs == 1, Time(since)
	v.Seat, v.SeatLabel, v.SeatColour = ns(seat), ns(label), ns(colour)
	v.Machine, v.MachineName, v.Slot = ns(machine), ns(mname), ns(folder)
	v.Project, v.Cwd, v.Name, v.Title, v.Model, v.RemoteURL = ns(project), ns(cwd), ns(name), ns(title), ns(model), claudeURL(remote)
	if kind.Valid && (kind.String == "interactive" || kind.String == "background") {
		v.Kind = ns(kind)
	}
	if ctxPct.Valid {
		f := ctxPct.Float64
		v.ContextPct = &f
	}
	v.StartedAt, v.LastEventAt, v.EndedAt = Time(started.Int64), Time(lastEv.Int64), Time(ended.Int64)
	if r := resumeCommand(launcher, folder, v.ID); r != "" {
		v.Resume = &r
	}
	nowMS := now.UnixMilli()
	v.Stale = v.State == "working" && listed == 0 && lastEv.Int64 > 0 && nowMS-lastEv.Int64 > StaleAfter.Milliseconds()
	active := v.State == "working" || v.State == "needs_permission" || v.State == "asked"
	v.MachineStale = active && lastSeen > 0 && nowMS-lastSeen > MachineSilent.Milliseconds()
	v.Activity = Time(activity)
	v.Dormant = v.State != "ended" && v.State != "gone" && !v.NeedsYou && !(v.State != "done" && !v.Stale) &&
		nowMS-activity > LiveWithin.Milliseconds()
	if alertJSON.Valid {
		v.Alert = scanAlertBrief(alertJSON.String)
	}
	if term.Valid {
		var t struct {
			FocusURL string `json:"focus_url"`
			Host     string `json:"host"`
		}
		if json.Unmarshal([]byte(term.String), &t) == nil && t.FocusURL != "" {
			v.FocusLink, v.FocusTarget = t.FocusURL, t.Host
			if t.Host == "" {
				v.FocusTarget = machine.String
			}
		}
	}
	return v, nil
}

// resumeCommand is "claude-2 --resume <id>", or the folder spelled out when no launcher
// is known. Values come from client records, so they are quoted for a POSIX shell.
func resumeCommand(launcher, folder sql.NullString, id string) string {
	id = shellQuote(id)
	switch {
	case launcher.Valid && launcher.String != "" && safeWord.MatchString(launcher.String):
		return launcher.String + " --resume " + id
	case folder.Valid && folder.String == "~/.claude":
		return "claude --resume " + id
	case folder.Valid && folder.String != "":
		return "CLAUDE_CONFIG_DIR=" + shellPath(folder.String) + " claude --resume " + id
	}
	return ""
}

var safeWord = regexp.MustCompile(`^[A-Za-z0-9._@%+=:,/-]+$`)

// shellQuote quotes s as one POSIX shell word (single quotes, ' as '\”).
func shellQuote(s string) string {
	if safeWord.MatchString(s) && !strings.HasPrefix(s, "-") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellPath is shellQuote that keeps a leading ~/ working.
func shellPath(s string) string {
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		return "~/" + shellQuote(rest)
	}
	return shellQuote(s)
}

// claudeURL passes a stored remote_url only when it is a claude.ai link (rows written
// before the ingest filter may hold anything).
func claudeURL(v sql.NullString) *string {
	if !v.Valid || !strings.HasPrefix(v.String, "https://claude.ai/") {
		return nil
	}
	return ns(v)
}

// SessionFilter is the query of GET /v1/sessions.
type SessionFilter struct {
	States       []string // states, or "needs_you"
	Seats        []string
	Machines     []string
	IncludeEnded bool
	Since        time.Time // activity at or after; zero = Days
	Days         int
	Limit        int
	Offset       int
}

// Cut is the start of the activity window: Since, else now minus Days (default 2).
func (f SessionFilter) Cut(now time.Time) time.Time {
	if !f.Since.IsZero() {
		return f.Since
	}
	days := f.Days
	if days <= 0 {
		days = 2
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

func in(col string, vals []string, args *[]any) string {
	ph := make([]string, len(vals))
	for i, v := range vals {
		ph[i] = "?"
		*args = append(*args, v)
	}
	return col + " IN (" + strings.Join(ph, ", ") + ")"
}

// Sessions lists sessions for the board: needs-you first (oldest waiting first), then
// newest activity first. Without a state filter, ended and gone sessions are left out
// unless IncludeEnded. Only sessions active since Since (or within Days) are listed,
// except that sessions needing you are always listed. It returns one extra row's worth
// of knowledge: more = true when another page exists.
func (s *Store) Sessions(ctx context.Context, f SessionFilter, now time.Time) (out []SessionView, more bool, err error) {
	var where []string
	var args []any
	needsOnly, explicitClosed := false, false
	var states []string
	for _, st := range f.States {
		switch st {
		case "needs_you":
			needsOnly = true
		case "ended", "gone":
			explicitClosed = true
			states = append(states, st)
		case "":
		default:
			states = append(states, st)
		}
	}
	switch {
	case needsOnly && len(states) > 0:
		where = append(where, "("+sessionNeedsExpr+" = 1 OR "+in(sessionStateExpr, states, &args)+")")
	case needsOnly:
		where = append(where, sessionNeedsExpr+" = 1")
	case len(states) > 0:
		where = append(where, in(sessionStateExpr, states, &args))
	}
	if !explicitClosed && !f.IncludeEnded {
		where = append(where, liveCondition)
	}
	if len(f.Seats) > 0 {
		where = append(where, in("s.seat_id", f.Seats, &args))
	}
	if len(f.Machines) > 0 {
		where = append(where, in("s.machine_id", f.Machines, &args))
	}
	cut := f.Cut(now)
	// The days window also keeps a session listed in it (a running but idle process), so it
	// can still be found; it is dormant, not open.
	where = append(where, "("+activityExpr+" >= ? OR coalesce(s.last_listed_at, 0) >= ? OR "+sessionNeedsExpr+" = 1)")
	args = append(args, cut.UnixMilli(), cut.UnixMilli())
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT ` + sessionSelectCols + sessionFrom + ` WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + sessionNeedsExpr + ` DESC, CASE WHEN ` + sessionNeedsExpr + ` = 1 THEN ` + sessionSinceExpr + ` END ASC, ` +
		activityExpr + ` DESC, s.id LIMIT ? OFFSET ?`
	args = append(args, limit+1, f.Offset)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanSession(rows, now)
		if err != nil {
			return nil, false, err
		}
		out = append(out, v)
	}
	if len(out) > limit {
		out, more = out[:limit], true
	}
	return out, more, rows.Err()
}

// Session returns one session.
func (s *Store) Session(ctx context.Context, id string, now time.Time) (SessionView, error) {
	v, err := scanSession(s.r.QueryRowContext(ctx, `SELECT `+sessionSelectCols+sessionFrom+` WHERE s.id = ?`, id), now)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// TimelineItem is one hook event of a session.
type TimelineItem struct {
	TS          Time    `json:"ts"`
	Event       string  `json:"event"`
	Subtype     *string `json:"subtype"`
	ToolName    *string `json:"tool_name"`
	StateAfter  *string `json:"state_after"`
	LastMessage *string `json:"last_message"`
}

// TransitionView is one state change (session_transition).
type TransitionView struct {
	At     Time    `json:"at"`
	From   *string `json:"from"`
	To     string  `json:"to"`
	Detail *string `json:"detail"`
	Cause  string  `json:"cause"`
}

// TokenTotals are a session's token rows summed per model.
type TokenTotals struct {
	Model                                               string
	Input, Output, CacheRead, Cache5m, Cache1h, Replies int64
}

// SessionTimeline returns the last TimelineMax hook events, oldest first, the state
// transitions and per-model token totals.
func (s *Store) SessionTimeline(ctx context.Context, id string) ([]TimelineItem, []TransitionView, []TokenTotals, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT ts, event, subtype, tool_name, state_after, last_message FROM
		(SELECT * FROM hook_event WHERE session_id = ? ORDER BY ts DESC, record_id DESC LIMIT ?) ORDER BY ts, record_id`, id, TimelineMax)
	if err != nil {
		return nil, nil, nil, err
	}
	timeline := []TimelineItem{}
	for rows.Next() {
		var it TimelineItem
		var ts int64
		var sub, tool, after, lm sql.NullString
		if err := rows.Scan(&ts, &it.Event, &sub, &tool, &after, &lm); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		it.TS, it.Subtype, it.ToolName, it.StateAfter, it.LastMessage = Time(ts), ns(sub), ns(tool), ns(after), ns(lm)
		timeline = append(timeline, it)
	}
	rows.Close()
	rows, err = s.r.QueryContext(ctx, `SELECT at, from_state, to_state, detail, cause FROM session_transition WHERE session_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, nil, nil, err
	}
	trans := []TransitionView{}
	for rows.Next() {
		var t TransitionView
		var at int64
		var from, detail sql.NullString
		if err := rows.Scan(&at, &from, &t.To, &detail, &t.Cause); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		t.At, t.From, t.Detail = Time(at), ns(from), ns(detail)
		trans = append(trans, t)
	}
	rows.Close()
	rows, err = s.r.QueryContext(ctx, `SELECT coalesce(model, ''), sum(input), sum(output), sum(cache_read), sum(cache_5m), sum(cache_1h), count(*)
		FROM token_row WHERE session_id = ? GROUP BY coalesce(model, '')`, id)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var tot []TokenTotals
	for rows.Next() {
		var t TokenTotals
		if err := rows.Scan(&t.Model, &t.Input, &t.Output, &t.CacheRead, &t.Cache5m, &t.Cache1h, &t.Replies); err != nil {
			return nil, nil, nil, err
		}
		tot = append(tot, t)
	}
	return timeline, trans, tot, rows.Err()
}

// SessionCounts are the overview counts among live sessions.
type SessionCounts struct {
	Working  int `json:"working"`
	NeedsYou int `json:"needs_you"`
	Done     int `json:"done"`
	Live     int `json:"live"`
}

// liveArgs are the two arguments of liveNowCondition.
func liveArgs(now time.Time) []any {
	return []any{now.Add(-StaleAfter).UnixMilli(), now.Add(-LiveWithin).UnixMilli()}
}

// Counts counts live sessions: open and not dormant (states.md 5).
func (s *Store) Counts(ctx context.Context, now time.Time) (SessionCounts, error) {
	var c SessionCounts
	err := s.r.QueryRowContext(ctx, `SELECT coalesce(sum(`+sessionStateExpr+` = 'working'), 0), coalesce(sum(`+sessionNeedsExpr+` = 1 AND NOT `+sessionSeenExpr+`), 0),
		coalesce(sum(`+sessionStateExpr+` = 'done'), 0), count(*)`+sessionFrom+` WHERE `+liveNowCondition,
		liveArgs(now)...).Scan(&c.Working, &c.NeedsYou, &c.Done, &c.Live)
	return c, err
}
