-- Version 16: seat_calibration (setup.md 8.5, Calibration): per seat, the lanes strategy's
-- calibration state and factors, kept up to date by `wy hub serve` and returned by
-- GET /v1/usage.

CREATE TABLE seat_calibration (
  seat_id       TEXT    NOT NULL PRIMARY KEY REFERENCES seat(id),
  account_uuid  TEXT    NOT NULL,                     -- the login it was calibrated for: another
  org_uuid      TEXT    NOT NULL,                     -- one restarts it (reason seat_change)
  cursor        INTEGER NOT NULL,                     -- samples and token rows up to here are folded in
  lease_cursor  INTEGER NOT NULL,                     -- leases picked up to here are folded in
  state         TEXT    NOT NULL CHECK (json_valid(state)),    -- internal/calib State
  factors       TEXT    NOT NULL CHECK (json_valid(factors)),  -- api.yaml Calibration
  updated_at    INTEGER NOT NULL
) STRICT;
