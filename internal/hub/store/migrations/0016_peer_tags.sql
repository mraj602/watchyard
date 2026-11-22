-- Version 16: peer_tag (peers.md 3.4): the tags a session gave itself, so other sessions can
-- address it by role (`wy peers send tag:<tag>`). Cleared when the session ends or goes away.

CREATE TABLE peer_tag (
  session_id    TEXT    NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  tag           TEXT    NOT NULL CHECK (length(tag) BETWEEN 1 AND 40 AND tag NOT GLOB '*[^a-z0-9-]*'),
  device_id     TEXT    NOT NULL,                  -- the agent device that set it
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (session_id, tag)
) STRICT;
CREATE INDEX peer_tag_tag ON peer_tag(tag);
