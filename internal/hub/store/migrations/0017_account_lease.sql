-- Version 17: account_lease (record.md 4.10, setup.md 8.5): the picks `wy account next`
-- recorded, so every machine's picker can count the others' leases.

CREATE TABLE account_lease (
  id            TEXT    NOT NULL PRIMARY KEY,         -- payload.lease_id (a ULID made by the picker)
  seat_id       TEXT    NOT NULL REFERENCES seat(id),
  machine_id    TEXT    NOT NULL REFERENCES machine(id),
  ts            INTEGER NOT NULL,                     -- pick time
  session_id    TEXT,                                 -- --session-id; not an FK: the session starts later
  kind          TEXT    NOT NULL CHECK (kind IN ('aggressive', 'protected')),
  c5            REAL    NOT NULL,
  cf            REAL    NOT NULL,
  strategy      TEXT    NOT NULL,
  urgent        INTEGER NOT NULL DEFAULT 0 CHECK (urgent IN (0, 1)),
  model         TEXT,
  record_id     TEXT    NOT NULL
) STRICT;
CREATE INDEX account_lease_seat_ts ON account_lease(seat_id, ts);
