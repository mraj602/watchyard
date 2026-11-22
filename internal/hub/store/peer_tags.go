package store

import (
	"context"
	"errors"
)

// Session tags (schema v16, peers.md 3.4): short slugs a session gives itself. The relay
// writes them on a machine's peer_tag frame; the directory carries them; PutFold clears a
// session's tags when it ends or goes away, and the session's deletion cascades.

// ErrTooManyTags: the session holds the most tags it may.
var ErrTooManyTags = errors.New("too many tags")

// AddPeerTag gives a session a tag; added is false when it had it already. A session that
// would hold more than max tags is ErrTooManyTags.
func (tx *Tx) AddPeerTag(session, tag, device string, max int, nowMS int64) (added bool, err error) {
	var have, n int
	if err := tx.queryRow(`SELECT count(*), coalesce(sum(tag = ?), 0) FROM peer_tag WHERE session_id = ?`, tag, session).Scan(&n, &have); err != nil {
		return false, err
	}
	if have > 0 {
		return false, nil
	}
	if n >= max {
		return false, ErrTooManyTags
	}
	if _, err := tx.exec(`INSERT INTO peer_tag (session_id, tag, device_id, created_at) VALUES (?, ?, ?, ?)`, session, tag, device, nowMS); err != nil {
		return false, err
	}
	return true, nil
}

// RemovePeerTag takes a tag from a session; removed is false when it did not have it.
func (tx *Tx) RemovePeerTag(session, tag string) (removed bool, err error) {
	res, err := tx.exec(`DELETE FROM peer_tag WHERE session_id = ? AND tag = ?`, session, tag)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ClearPeerTags removes every tag of a session.
func (tx *Tx) ClearPeerTags(session string) error {
	_, err := tx.exec(`DELETE FROM peer_tag WHERE session_id = ?`, session)
	return err
}

// SessionPeerTags are one session's tags, sorted.
func (tx *Tx) SessionPeerTags(session string) ([]string, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT tag FROM peer_tag WHERE session_id = ? ORDER BY tag`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PeerTags maps each session that has tags to its tags, sorted.
func (s *Store) PeerTags(ctx context.Context) (map[string][]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT session_id, tag FROM peer_tag ORDER BY session_id, tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}
