-- Version 23: the base rules of the Smart Account Switch notices (push.md 3.9): a machine moved
-- sessions to other accounts (session_moved) and its daily digest (switch_digest), both sent
-- as switch_notice records (record.md 4.12).

INSERT OR IGNORE INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES
  ('rule_session_moved',    'session_moved',    1, 1,    0, 0, '{}', 0),
  ('rule_switch_digest',    'switch_digest',    1, 1,    0, 0, '{}', 0);
