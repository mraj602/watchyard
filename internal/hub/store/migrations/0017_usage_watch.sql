-- Version 17: seat_watch and seat_verdict (setup.md 8.5, Usage relation; record.md 4.11) and
-- the base rules of the usage notices (push.md 3.9).

-- One row per seat: what the calibration job watches for usage notices (push.md 3.9) and the
-- usage relation it folds (setup.md 8.5, Usage relation), brought up to date with the
-- calibration. GET /v1/accounts/{seat}/relation reads `relation`.
CREATE TABLE seat_watch (
  seat_id       TEXT    NOT NULL PRIMARY KEY REFERENCES seat(id),
  cursor        INTEGER NOT NULL,                     -- samples up to here are watched
  state         TEXT    NOT NULL CHECK (json_valid(state)),     -- internal/calib Watch: level, candidate, masks
  relation      TEXT    NOT NULL CHECK (json_valid(relation)),  -- internal/calib Relation: 15 days of sums
  updated_at    INTEGER NOT NULL
) STRICT;

-- The newest verdict of `wy account next` about each seat, from any machine (record.md 4.11).
CREATE TABLE seat_verdict (
  seat_id       TEXT    NOT NULL PRIMARY KEY REFERENCES seat(id),
  machine_id    TEXT    NOT NULL REFERENCES machine(id),
  ts            INTEGER NOT NULL,                     -- the run's time (record ts)
  strategy      TEXT    NOT NULL,
  verdict       TEXT    NOT NULL CHECK (json_valid(verdict)),   -- the account's item, plus next_free
  record_id     TEXT    NOT NULL
) STRICT;

INSERT OR IGNORE INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES
  ('rule_limit_change',     'limit_change',     1, 1,    0, 0, '{}', 0),
  ('rule_usage_reset',      'usage_reset',      1, 1,    0, 0, '{}', 0),
  ('rule_seat_change',      'seat_change',      1, 1,    0, 0, '{}', 0),
  ('rule_weekly_drop',      'weekly_drop',      1, 1,    0, 0, '{}', 0);
