package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// This file is what the widget refresh reads (push.md 13.5): the link targets that asked for
// refresh frames and the state a refresh carries.

// LinkRefreshTargets lists the link targets that get widget refresh frames: enabled, not
// gone, channel on, with a device that is neither revoked nor expired and has
// prefs.widgets = true. Prefs is set to the device's prefs.
func (s *Store) LinkRefreshTargets(ctx context.Context, now int64) ([]model.Target, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+targetCols+`, d.prefs FROM push_target t
		JOIN device d ON d.id = t.device_id JOIN channel c ON c.kind = t.kind
		WHERE t.kind = ? AND t.enabled = 1 AND t.gone_at IS NULL AND c.enabled = 1
		AND d.revoked_at IS NULL AND (d.expires_at IS NULL OR d.expires_at > ?)
		ORDER BY t.created_at, t.id`, model.KindLink, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Target
	for rows.Next() {
		var dprefs sql.NullString
		t, err := scanTargetWith(rows, &dprefs)
		if err != nil {
			return nil, err
		}
		p := model.Prefs{Tap: model.TapAuto}
		if dprefs.Valid {
			_ = json.Unmarshal([]byte(dprefs.String), &p)
		}
		if !p.WidgetsOn() {
			continue
		}
		t.Prefs = &p
		out = append(out, t)
	}
	return out, rows.Err()
}

// WidgetWindow is one seat's current usage window as the widgets show it.
type WidgetWindow struct {
	Seat     string
	Key      string // five_hour, seven_day
	Pct      float64
	ResetsAt int64 // ms
}

// WidgetState is what a widget refresh is made of (push.md 13.5).
type WidgetState struct {
	NeedsYou int
	Windows  []WidgetWindow // seat, then window, then resets_at
}

// WidgetState reads the needs-you count and the current five_hour and seven_day windows of
// visible seats (resets_at after now).
func (s *Store) WidgetState(ctx context.Context, now int64) (WidgetState, error) {
	var out WidgetState
	if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM session_state st JOIN session s ON s.id = st.session_id
		WHERE st.needs_you = 1 AND st.state NOT IN ('ended', 'gone') AND NOT `+sessionSeenExpr).Scan(&out.NeedsYou); err != nil {
		return out, err
	}
	rows, err := s.r.QueryContext(ctx, `SELECT w.seat_id, w.window_key, coalesce(w.final_pct, w.peak_pct, 0), w.resets_at
		FROM usage_window w JOIN seat se ON se.id = w.seat_id
		WHERE NOT `+seatHiddenSQL+` AND w.window_key IN ('five_hour', 'seven_day') AND w.resets_at > ?
		ORDER BY w.seat_id, w.window_key, w.resets_at`, now)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var w WidgetWindow
		if err := rows.Scan(&w.Seat, &w.Key, &w.Pct, &w.ResetsAt); err != nil {
			return out, err
		}
		out.Windows = append(out.Windows, w)
	}
	return out, rows.Err()
}
