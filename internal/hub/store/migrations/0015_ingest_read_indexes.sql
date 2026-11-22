-- Version 15: indexes for the reads that made a small hub fall behind as its record log grew.
-- A refold read every record of the machine since the session began (record_machine_ts) to
-- find the slot's session_list records; LastTokenRowAt read every token row of the session
-- to find one max(ts); /v1/overview and /v1/accounts scanned each seat's limit samples for
-- the idle reading and for the sources of each window. IF NOT EXISTS: hubs that were fixed
-- by hand before this release already have them.

CREATE INDEX IF NOT EXISTS record_machine_kind_slot_ts ON record(machine_id, kind, slot, ts);
CREATE INDEX IF NOT EXISTS token_row_session_ts ON token_row(session_id, ts);
CREATE INDEX IF NOT EXISTS limit_sample_reset ON limit_sample(seat_id, window_key, resets_at);
CREATE INDEX IF NOT EXISTS limit_sample_idle ON limit_sample(seat_id, window_key, ts)
  WHERE source = 'web' AND resets_at IS NULL AND used_pct = 0;
