package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// WindowRow is one usage_window plus which sources sampled it.
type WindowRow struct {
	Key                 string
	ResetsAt, StartedAt int64
	Peak, Final, Unused *float64
	Samples             int
	UpdatedAt           int64
	Sources             []string // statusline, web
	// Idle: the newest reading of this key is claude.ai's "not started" (0 %, no resets_at),
	// newer than every usage_window of the key. ResetsAt and StartedAt are 0, Final is 0 and
	// UpdatedAt is that reading's ts. Only from Windows(…, current = true).
	Idle bool
}

// idleSQL finds, per seat and key, the newest idle claude.ai reading (used 0, no resets_at;
// record.md 4.3). Status-line samples are left out: a status line without a reset time says
// nothing about whether the window has started.
const idleSQL = `SELECT seat_id, window_key, max(ts) FROM limit_sample
	WHERE source = 'web' AND resets_at IS NULL AND used_pct = 0`

type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// idleReadings returns seat → key → ts of the newest idle reading (seat "" = every seat).
func idleReadings(ctx context.Context, q queryer, seat string) (map[string]map[string]int64, error) {
	query, args := idleSQL, []any{}
	if seat != "" {
		query += ` AND seat_id = ?`
		args = append(args, seat)
	}
	rows, err := q.QueryContext(ctx, query+` GROUP BY seat_id, window_key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int64{}
	for rows.Next() {
		var s, k string
		var ts int64
		if err := rows.Scan(&s, &k, &ts); err != nil {
			return nil, err
		}
		if out[s] == nil {
			out[s] = map[string]int64{}
		}
		out[s][k] = ts
	}
	return out, rows.Err()
}

// withIdle puts an idle window in place of a key's current window when claude.ai's newest
// reading of that key is idle and newer than the window (or the key has no window yet).
func withIdle(rows []WindowRow, idle map[string]int64, keys []string) []WindowRow {
	if len(idle) == 0 {
		return rows
	}
	want := func(k string) bool {
		if len(keys) == 0 {
			return true
		}
		for _, x := range keys {
			if x == k {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	for i := range rows {
		seen[rows[i].Key] = true
		if ts, ok := idle[rows[i].Key]; ok && ts > rows[i].UpdatedAt {
			rows[i] = idleRow(rows[i].Key, ts)
		}
	}
	extra := make([]string, 0, len(idle))
	for k := range idle {
		if !seen[k] && want(k) {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		rows = append(rows, idleRow(k, idle[k]))
	}
	return rows
}

func idleRow(key string, ts int64) WindowRow {
	zero := 0.0
	return WindowRow{Key: key, Final: &zero, UpdatedAt: ts, Sources: []string{"web"}, Idle: true}
}

func nf(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

// Windows returns a seat's windows, newest reset first. keys filters by key; since keeps
// windows that reset at or after it. With current, only the newest window per key, replaced
// by an idle window (WindowRow.Idle) when claude.ai's newest reading of the key is idle.
func (s *Store) Windows(ctx context.Context, seat string, keys []string, since time.Time, current bool) ([]WindowRow, error) {
	args := []any{seat}
	q := `SELECT w.window_key, w.resets_at, coalesce(w.started_at, 0), w.peak_pct, w.final_pct, w.unused_pct, w.samples, w.updated_at,
		(SELECT group_concat(DISTINCT l.source) FROM limit_sample l WHERE l.seat_id = w.seat_id AND l.window_key = w.window_key
		   AND l.resets_at BETWEEN w.resets_at - ? AND w.resets_at + ?)
		FROM usage_window w WHERE w.seat_id = ?`
	args = []any{ResetTolerance, ResetTolerance, seat}
	if len(keys) > 0 {
		q += " AND " + in("w.window_key", keys, &args)
	}
	if !since.IsZero() {
		q += " AND w.resets_at >= ?"
		args = append(args, since.UnixMilli())
	}
	if current {
		q += ` AND w.resets_at = (SELECT max(resets_at) FROM usage_window x WHERE x.seat_id = w.seat_id AND x.window_key = w.window_key)`
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY w.resets_at DESC, w.window_key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WindowRow
	for rows.Next() {
		var w WindowRow
		var peak, final, unused sql.NullFloat64
		var src sql.NullString
		if err := rows.Scan(&w.Key, &w.ResetsAt, &w.StartedAt, &peak, &final, &unused, &w.Samples, &w.UpdatedAt, &src); err != nil {
			return nil, err
		}
		w.Peak, w.Final, w.Unused = nf(peak), nf(final), nf(unused)
		if src.Valid && src.String != "" {
			w.Sources = strings.Split(src.String, ",")
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil || !current {
		return out, err
	}
	rows.Close()
	idle, err := idleReadings(ctx, s.r, seat)
	if err != nil {
		return nil, err
	}
	return withIdle(out, idle[seat], keys), nil
}

// SampleRow is api.yaml LimitSample.
type SampleRow struct {
	TS       Time     `json:"ts"`
	Key      string   `json:"key"`
	Source   string   `json:"source"`
	UsedPct  *float64 `json:"used_pct"`
	ResetsAt Time     `json:"resets_at"`
}

// Samples returns a seat's raw limit samples, newest first (at most limit).
func (s *Store) Samples(ctx context.Context, seat string, keys []string, since time.Time, limit int) ([]SampleRow, error) {
	args := []any{seat}
	q := `SELECT ts, window_key, source, used_pct, coalesce(resets_at, 0) FROM limit_sample WHERE seat_id = ?`
	if len(keys) > 0 {
		q += " AND " + in("window_key", keys, &args)
	}
	if !since.IsZero() {
		q += " AND ts >= ?"
		args = append(args, since.UnixMilli())
	}
	args = append(args, limit)
	rows, err := s.r.QueryContext(ctx, q+" ORDER BY ts DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SampleRow{}
	for rows.Next() {
		var r SampleRow
		var ts, resets int64
		var used sql.NullFloat64
		if err := rows.Scan(&ts, &r.Key, &r.Source, &used, &resets); err != nil {
			return nil, err
		}
		r.TS, r.ResetsAt, r.UsedPct = Time(ts), Time(resets), nf(used)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SeatRow is a seat with its account and org, before windows are added.
type SeatRow struct {
	ID, Account, Org                    string
	Email, OrgName, Plan, Label, Colour *string
	Mode                                *string // seat.pick_mode (setup.md 8.5, Kind)
	Order                               int64
	Hidden                              bool
	ExtraUsage, Spend                   json.RawMessage // raw from the newest web_usage, nil if none
	WebRawAt                            int64
	Places                              []Place
	SignedOut                           []SlotPlace
	LiveSessions                        int
}

// SlotPlace is a Claude Code folder whose login was this seat until it logged out: its open
// slot_login period has no seat and the period just before it had this seat (api.yaml
// Seat.signed_out). A folder that moved to another login is that login's place, not this.
type SlotPlace struct {
	Machine     string `json:"machine"`
	MachineName string `json:"machine_name"`
	Slot        string `json:"slot"`
	Since       Time   `json:"since"`
}

// Place is where a seat is used: a slot logged in to it, or a desktop tab.
type Place struct {
	Machine     string  `json:"machine"`
	MachineName string  `json:"machine_name"`
	Slot        *string `json:"slot"`
	TabID       *string `json:"tab_id"`
	// StoppedReason is why the desktop app stopped reading this web seat (401, 403 or 429);
	// nil while reading and for a slot place (desktop.md C10).
	StoppedReason *string `json:"stopped_reason"`
}

// Seats lists seats in display order.
func (s *Store) Seats(ctx context.Context, includeHidden bool, now time.Time) ([]SeatRow, error) {
	q := `SELECT se.id, se.account_uuid, se.org_uuid, a.email, o.name, o.plan, se.label, se.colour, se.pick_mode, se.sort_order, ` + seatHiddenSQL + `
		FROM seat se LEFT JOIN account a ON a.uuid = se.account_uuid LEFT JOIN org o ON o.uuid = se.org_uuid`
	if !includeHidden {
		q += ` WHERE NOT ` + seatHiddenSQL
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY se.sort_order, se.created_at, se.id`)
	if err != nil {
		return nil, err
	}
	var out []SeatRow
	for rows.Next() {
		var r SeatRow
		var email, orgName, plan, label, colour, mode sql.NullString
		var hidden int
		if err := rows.Scan(&r.ID, &r.Account, &r.Org, &email, &orgName, &plan, &label, &colour, &mode, &r.Order, &hidden); err != nil {
			rows.Close()
			return nil, err
		}
		r.Email, r.OrgName, r.Plan, r.Label, r.Colour, r.Hidden = ns(email), ns(orgName), ns(plan), ns(label), ns(colour), hidden == 1
		r.Mode = ns(mode)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.seatExtras(ctx, &out[i], now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Seat returns one seat.
func (s *Store) Seat(ctx context.Context, id string, now time.Time) (SeatRow, error) {
	all, err := s.Seats(ctx, true, now)
	if err != nil {
		return SeatRow{}, err
	}
	for _, r := range all {
		if r.ID == id {
			return r, nil
		}
	}
	return SeatRow{}, ErrNotFound
}

func (s *Store) seatExtras(ctx context.Context, r *SeatRow, now time.Time) error {
	var ts int64
	var extra, spend sql.NullString
	err := s.r.QueryRowContext(ctx, `SELECT ts, raw -> '$.extra_usage', raw -> '$.spend' FROM web_usage WHERE seat_id = ? ORDER BY ts DESC LIMIT 1`, r.ID).
		Scan(&ts, &extra, &spend)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		r.WebRawAt = ts
		if extra.Valid && extra.String != "null" {
			r.ExtraUsage = json.RawMessage(extra.String)
		}
		if spend.Valid && spend.String != "null" {
			r.Spend = json.RawMessage(spend.String)
		}
	}
	rows, err := s.r.QueryContext(ctx, `SELECT sl.machine_id, m.name, sl.folder, NULL, NULL FROM slot_login l JOIN slot sl ON sl.id = l.slot_id
		JOIN machine m ON m.id = sl.machine_id WHERE l.seat_id = ? AND l.until_at IS NULL AND sl.removed_at IS NULL
		UNION ALL
		SELECT w.machine_id, m.name, NULL, w.tab_id, w.stopped_reason FROM web_seat w JOIN machine m ON m.id = w.machine_id WHERE w.seat_id = ?
		ORDER BY 2, 3, 4`, r.ID, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.Places = []Place{}
	for rows.Next() {
		var p Place
		var slot, tab, stopped sql.NullString
		if err := rows.Scan(&p.Machine, &p.MachineName, &slot, &tab, &stopped); err != nil {
			return err
		}
		p.Slot, p.TabID, p.StoppedReason = ns(slot), ns(tab), ns(stopped)
		r.Places = append(r.Places, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := s.seatSignedOut(ctx, r); err != nil {
		return err
	}
	return s.r.QueryRowContext(ctx, `SELECT count(*)`+sessionFrom+` WHERE s.seat_id = ? AND `+liveNowCondition,
		append([]any{r.ID}, liveArgs(now)...)...).Scan(&r.LiveSessions)
}

func (s *Store) seatSignedOut(ctx context.Context, r *SeatRow) error {
	rows, err := s.r.QueryContext(ctx, `SELECT sl.machine_id, m.name, sl.folder, l.since_at FROM slot_login l
		JOIN slot_login p ON p.slot_id = l.slot_id AND p.until_at = l.since_at
		JOIN slot sl ON sl.id = l.slot_id JOIN machine m ON m.id = sl.machine_id
		WHERE l.until_at IS NULL AND l.seat_id IS NULL AND p.seat_id = ? AND sl.removed_at IS NULL
		ORDER BY 2, 3`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.SignedOut = []SlotPlace{}
	for rows.Next() {
		var p SlotPlace
		var since int64
		if err := rows.Scan(&p.Machine, &p.MachineName, &p.Slot, &since); err != nil {
			return err
		}
		p.Since = Time(since)
		r.SignedOut = append(r.SignedOut, p)
	}
	return rows.Err()
}

// SlotView is a machine's slot (api.yaml Machine.slots).
type SlotView struct {
	Folder        string  `json:"folder"`
	Launcher      *string `json:"launcher"`
	Seat          *string `json:"seat"`
	LoggedIn      bool    `json:"logged_in"`
	ClaudeVersion *string `json:"claude_version"`
}

// MachineView is api.yaml Machine (machines.md 6.1).
type MachineView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	OS           *string    `json:"os"`
	Arch         *string    `json:"arch"`
	WyVersion    *string    `json:"wy_version"`
	LastSeen     Time       `json:"last_seen"`
	LastRecord   Time       `json:"last_record"`
	Backlog      *int64     `json:"backlog"`
	Silent       bool       `json:"silent"`
	LiveSessions int        `json:"live_sessions"`
	Slots        []SlotView `json:"slots"`

	Reach         *string           `json:"reach"`
	BacklogOldest Time              `json:"backlog_oldest"`
	BacklogAt     Time              `json:"backlog_at"`
	VersionStatus string            `json:"version_status"`
	RecordV       *int64            `json:"record_v"`
	Held          int64             `json:"held"`
	DirectPush    bool              `json:"direct_push"`
	TimerOK       *bool             `json:"timer_ok"`
	Linger        *bool             `json:"linger"`
	RemovedAt     Time              `json:"removed_at,omitempty"` // only removed machines (?all=true)
	Pull          *MachinePull      `json:"pull"`
	Alert         *model.AlertBrief `json:"alert"`
}

// Machines lists machines that are not removed, by name. ids filters (nil = all).
func (s *Store) Machines(ctx context.Context, ids []string, now time.Time) ([]MachineView, error) {
	return s.MachineList(ctx, MachineQuery{IDs: ids}, now)
}
