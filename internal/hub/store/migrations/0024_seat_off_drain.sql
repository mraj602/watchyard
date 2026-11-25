-- Version 24: account Off and the owner's drain, the same on every machine (setup.md 8.5,
-- Kind and Owner drain). seat.pick_off 1: the picker never picks the seat and the switch
-- never moves a session into it (stricter than protected; pick_mode is kept for when it is
-- turned on again). seat.drain_until: until when (ms) the owner drains it, NULL when not;
-- the alert engine clears it once it ends (by time or at the drain aim) and sends
-- drain_started / drain_ended; machines send drain_pulled (push.md 3.9).

ALTER TABLE seat ADD COLUMN pick_off INTEGER NOT NULL DEFAULT 0 CHECK (pick_off IN (0, 1));
ALTER TABLE seat ADD COLUMN drain_until INTEGER;

INSERT OR IGNORE INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES
  ('rule_drain_started', 'drain_started', 1, 1, 0, 0, '{}', 0),
  ('rule_drain_ended',   'drain_ended',   1, 1, 0, 0, '{}', 0),
  ('rule_drain_pulled',  'drain_pulled',  1, 1, 0, 0, '{}', 0);
