-- Version 22: a session that continues under another account (identity.md 5.3). A live record
-- of the session from another slot moves it there; session.moved_from_seat is the seat it had
-- before the last move and moved_at when. session_move keeps every move, so each seat the
-- session left keeps its peers handle as an alias (peers.md 3.2) after several moves.

ALTER TABLE session ADD COLUMN moved_from_seat TEXT REFERENCES seat(id);
ALTER TABLE session ADD COLUMN moved_at INTEGER;

CREATE TABLE session_move (
  session_id    TEXT    NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  at            INTEGER NOT NULL,
  from_seat     TEXT    REFERENCES seat(id),
  to_seat       TEXT    REFERENCES seat(id),
  from_slot     INTEGER REFERENCES slot(id),
  to_slot       INTEGER REFERENCES slot(id)
) STRICT;
CREATE INDEX session_move_session ON session_move(session_id, at);
