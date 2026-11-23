-- Version 21: the trusted sender (peers.md 4.1a, 10). peer_trust holds each machine's
-- approved Doorbell as its agent reported it (link.md 6.2 peer_trust) and the owner's
-- revocation; peer_message.from_kind marks a trusted send and via_tag the tag it went to.

CREATE TABLE peer_trust (
  machine_id    TEXT    NOT NULL REFERENCES machine(id) ON DELETE CASCADE,
  sender        TEXT    NOT NULL CHECK (sender IN ('doorbell')),
  device_id     TEXT    NOT NULL,                  -- the agent device that reported it
  account       TEXT    NOT NULL,                  -- account uuid it sends as
  program       TEXT    NOT NULL,
  interpreter   TEXT    NOT NULL,
  venv          TEXT    NOT NULL,
  approved_at   INTEGER NOT NULL,
  test_build    INTEGER NOT NULL DEFAULT 0 CHECK (test_build IN (0, 1)), -- made by a trusttest build (peers.md 15)
  revoked_at    INTEGER,
  revoked_by    TEXT,                              -- a device id, or 'cli' (wy peers trust doorbell --off)
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (machine_id, sender)
) STRICT;

ALTER TABLE peer_message ADD COLUMN from_kind TEXT CHECK (from_kind IS NULL OR from_kind IN ('service:doorbell'));
ALTER TABLE peer_message ADD COLUMN via_tag TEXT;
CREATE INDEX peer_message_from_kind ON peer_message(from_kind, created_at) WHERE from_kind IS NOT NULL;
