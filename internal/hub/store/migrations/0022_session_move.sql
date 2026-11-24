-- Version 22: a session that continues under another account (identity.md 5.3). A live record
-- of the session from another slot moves it there; session.moved_from_seat is the seat it had
-- before the last move (its old peers handle stays an alias, peers.md 3.2) and moved_at when.

ALTER TABLE session ADD COLUMN moved_from_seat TEXT REFERENCES seat(id);
ALTER TABLE session ADD COLUMN moved_at INTEGER;
