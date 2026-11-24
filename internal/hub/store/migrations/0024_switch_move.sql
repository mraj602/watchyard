-- Version 24: the Smart Account Switch log (record.md 4.13, setup.md 8.6 Switch log): one row
-- per move of a session to another account, by the switch or by hand, with the projections
-- that made it and its result, for the board's Switches page. record_id keeps a replayed
-- record from making a second row. The labels are the ones the machine used when it moved.

CREATE TABLE switch_move (
  record_id     TEXT    NOT NULL PRIMARY KEY,
  machine_id    TEXT    NOT NULL REFERENCES machine(id) ON DELETE CASCADE,
  at            INTEGER NOT NULL,
  session_id    TEXT    NOT NULL,
  session_name  TEXT,
  kind          TEXT    NOT NULL,
  interactive   INTEGER NOT NULL CHECK (interactive IN (0, 1)),
  from_label    TEXT    NOT NULL,
  to_label      TEXT    NOT NULL,
  auto          INTEGER NOT NULL CHECK (auto IN (0, 1)),
  reason        TEXT,
  window        TEXT,
  p5            REAL,
  p7            REAL,
  cap5          REAL,
  cap7          REAL,
  burn5         REAL,
  cross_org     INTEGER NOT NULL CHECK (cross_org IN (0, 1)),
  error         TEXT
) STRICT;
CREATE INDEX switch_move_at ON switch_move(at DESC);
CREATE INDEX switch_move_session ON switch_move(session_id, at);
CREATE INDEX switch_move_machine ON switch_move(machine_id, at DESC);
