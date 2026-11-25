-- Watchyard hub schema, version 22.
--
-- One SQLite file (WAL mode). Applied by the hub at start inside one transaction
-- after `PRAGMA foreign_keys = ON`. Later versions add numbered migrations
-- (internal/hub/store/migrations/NNNN_*.sql; 0001 is version 1 of this file). This file is
-- always the full current schema, and a test checks that migrations from v1 produce it.
--   v1  the first schema.
--   v2  session_transition (every state change, not only hook-caused ones).
--   v3  pairing, owner sessions, push targets, rules, alerts, deliveries (hub_secret,
--       device.expires_at/prefs; pair_code, channel, push_target, rule, alert, delivery recreated).
--   v4  machine reach/record_v/held/backlog_oldest/backlog_at/info/info_at; pull_target
--       (machines.md 9).
--   v5  seat.look_src (desktop.md C10).
--   v6  the expo channel row, push_target.meta, delivery.ticket/receipt_at (push.md 12).
--   v7  owner_key, device_key, revocation_doc, link_state, link_queue (keys.md 9, link.md 5).
--   v8  the link channel (channel and push_target recreated with 'link'), delivery.delivered_at
--       (push.md 13.8).
--   v9  matrix_doc, peer_thread, peer_message, peer_audit (peers.md 10).
--   v10 link_queue recreated with the 'peer_thread_resume' type (peers.md 6.3, link.md 6.4).
--   v11 the expo channel removed: its channel row, targets, deliveries and access token;
--       push_target.meta, delivery.ticket/receipt_at (channel, push_target, delivery recreated).
--   v12 indexes: alert_seat, alert_machine, delivery_target.
--   v13 session_state.peers (states.md 3.1, push.md 3.5).
--   v14 device_accounts (keys.md 4.3, peers.md 5.3).
--   v15 indexes: record_machine_kind_slot_ts, token_row_session_ts, limit_sample_reset,
--       limit_sample_idle (the hub's hot reads).
--   v16 peer_tag (peers.md 3.4).
--   v17 account_lease (record.md 4.10, setup.md 8.5).
--   v18 seat_calibration (setup.md 8.5, Calibration).
--   v19 seat_watch, seat_verdict and the usage-notice base rules (setup.md 8.5, Usage relation;
--       push.md 3.9; record.md 4.11).
--   v20 seat.pick_mode, seat.pick_mode_src (setup.md 8.5, Kind).
--   v21 peer_trust; peer_message.from_kind/via_tag (peers.md 4.1a, 10).
--   v22 session.moved_from_seat/moved_at, session_move (identity.md 5.3: a session moved to another account).
--   v23 the switch-notice base rules session_moved and switch_digest (push.md 3.9; record.md 4.12).
--   v24 seat.pick_off, seat.drain_until and the drain-notice base rules (setup.md 8.5, Kind and
--       Owner drain; push.md 3.9).
--
-- Conventions
--   * Times are INTEGER milliseconds since the Unix epoch, UTC (`*_at`, `ts`).
--   * Ids made by clients are TEXT (record ULIDs, Claude session uuids, machine ids).
--     Ids made by the hub are TEXT with a prefix + ULID (`dev_…`, `seat_…`).
--   * JSON is stored as TEXT and checked with json_valid().
--   * Every table is STRICT. Booleans are INTEGER 0/1 with a CHECK.
--   * `owner_id` is on the tables a future shared hub could partition.
--     It is always 'owner' on a single-user hub.
--   * Driver: modernc.org/sqlite (pure Go, no cgo). Needs SQLite >= 3.37 for STRICT.
--
-- Groups: meta, identity, access, activity, usage, alerts, keys and link, peers.

PRAGMA foreign_keys = ON;

---------------------------------------------------------------------------- meta

CREATE TABLE schema_version (
  version     INTEGER NOT NULL PRIMARY KEY,
  applied_at  INTEGER NOT NULL,
  wy_version  TEXT    NOT NULL                      -- hub binary version that applied it
) STRICT;

INSERT INTO schema_version (version, applied_at, wy_version)
VALUES (24, CAST(unixepoch('subsec') * 1000 AS INTEGER), 'spec');

-- Hub-wide settings that are not secrets (retention days, last-message policy, …).
CREATE TABLE setting (
  key         TEXT    NOT NULL PRIMARY KEY,
  value       TEXT    NOT NULL CHECK (json_valid(value)),
  updated_at  INTEGER NOT NULL
) STRICT;

---------------------------------------------------------------------------- identity

-- The one owner of a single-user hub. Owner login is by passkey or password.
CREATE TABLE owner (
  id             TEXT    NOT NULL PRIMARY KEY,      -- 'owner'
  name           TEXT,
  password_hash  TEXT,                               -- argon2id PHC string; NULL until set
  created_at     INTEGER NOT NULL
) STRICT;

-- One Claude login.
CREATE TABLE account (
  uuid          TEXT    NOT NULL PRIMARY KEY,       -- oauthAccount.accountUuid / claude.ai account uuid
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  email         TEXT,
  display_name  TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL
) STRICT;
CREATE INDEX account_email ON account(email);

-- Who the limits belong to: a personal plan or a Team/Enterprise org.
CREATE TABLE org (
  uuid          TEXT    NOT NULL PRIMARY KEY,       -- oauthAccount.organizationUuid / claude.ai org uuid
  name          TEXT,
  plan          TEXT,                                -- normalised: free, pro, max_5x, max_20x, team, enterprise, unknown
  rate_tier     TEXT,                                -- raw organizationRateLimitTier / rate_limit_tier
  billing_type  TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL
) STRICT;

-- One account inside one org. Every usage number, card, label and colour hangs off this.
CREATE TABLE seat (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'seat_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  account_uuid  TEXT    NOT NULL REFERENCES account(uuid),
  org_uuid      TEXT    NOT NULL REFERENCES org(uuid),
  label         TEXT,                                -- "work"
  colour        TEXT    CHECK (colour IS NULL OR colour GLOB '#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'),
  sort_order    INTEGER NOT NULL DEFAULT 0,
  hidden        INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1)),
  track_web     INTEGER NOT NULL DEFAULT 1 CHECK (track_web IN (0, 1)),
  created_at    INTEGER NOT NULL, look_src TEXT CHECK (look_src IS NULL OR look_src IN ('ui', 'slot', 'import')), pick_mode TEXT CHECK (pick_mode IS NULL OR pick_mode IN ('protected', 'aggressive')), pick_mode_src TEXT CHECK (pick_mode_src IS NULL OR pick_mode_src IN ('ui', 'slot')), pick_off INTEGER NOT NULL DEFAULT 0 CHECK (pick_off IN (0, 1)), drain_until INTEGER,
  UNIQUE (account_uuid, org_uuid)
) STRICT;
-- sort_order: the display position (`order` on Seat). PUT /v1/seats/order puts the listed seats
-- first and renumbers every seat 0..n-1; seats are listed by sort_order, created_at, id, so a
-- new seat (max + 1) or one nobody ordered sorts after the ordered ones. No migration: the column
-- is in version 1.
-- pick_mode, pick_mode_src: v20 (setup.md 8.5, Kind). How `wy account next` should use the
-- seat: 'protected', 'aggressive' or NULL (not chosen: aggressive). pick_mode_src: 'ui'
-- (PATCH /v1/accounts/{seat}; also for null, Not chosen) or 'slot' (a slot_info `mode`,
-- applied only while pick_mode and pick_mode_src are both NULL: nobody has chosen). Added by
-- migration 0020 (ALTER TABLE ADD COLUMN), so the columns sit on the created_at line, as
-- SQLite writes them.
-- pick_off, drain_until: v24 (setup.md 8.5, Kind and Owner drain). pick_off 1: the account
-- is Off (never picked, nothing moved into it; pick_mode kept for when it is on again).
-- drain_until: the owner's drain end (ms), NULL when not draining; the alert engine clears it
-- when the drain ends. Added by migration 0024, on the created_at line too.
-- look_src: v5 (desktop.md C10). Who set label and colour last: 'ui' (PATCH /v1/accounts/{seat}),
-- 'slot' (slot_info; applies only while look_src is NULL or 'slot'), 'import' (rows imported by earlier versions; nothing writes it now).

-- A computer running wy. The id is made by wy on first run (identity.md).
CREATE TABLE machine (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'm_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  name          TEXT    NOT NULL,                    -- defaults to the hostname
  os            TEXT,                                -- linux, darwin, windows (GOOS)
  arch          TEXT,
  wy_version    TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER,                             -- receive time of its newest record
  last_record   INTEGER,                             -- client ts of its newest record
  backlog       INTEGER,                             -- outbox records queued, as last reported
  removed_at    INTEGER
, reach TEXT CHECK (reach IS NULL OR reach IN ('direct', 'forward', 'pull')), record_v INTEGER, held INTEGER NOT NULL DEFAULT 0, backlog_oldest INTEGER, backlog_at INTEGER, info TEXT CHECK (info IS NULL OR json_valid(info)), info_at INTEGER) STRICT;
-- reach .. info_at: v4 (machines.md 9). reach: how its records reach the hub; record_v: highest record v it
-- writes; held: records it holds back because the hub is too old; backlog_oldest: client ts of its oldest
-- unanswered record; backlog_at: hub time the backlog was reported; info/info_at: last machine_info payload.

-- One config folder on one machine.
CREATE TABLE slot (
  id            INTEGER NOT NULL PRIMARY KEY,
  machine_id    TEXT    NOT NULL REFERENCES machine(id) ON DELETE CASCADE,
  folder        TEXT    NOT NULL,                    -- '~/.claude-2'
  real_path     TEXT,
  is_default    INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
  launcher      TEXT,                                -- 'claude-2'
  claude_version TEXT,
  sharing       TEXT    CHECK (sharing IS NULL OR json_valid(sharing)),
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL,
  removed_at    INTEGER,
  UNIQUE (machine_id, folder)
) STRICT;

-- Which seat a slot was logged in to, and when. Periods never overlap per slot;
-- the open period has until_at NULL.
CREATE TABLE slot_login (
  slot_id       INTEGER NOT NULL REFERENCES slot(id) ON DELETE CASCADE,
  since_at      INTEGER NOT NULL,                    -- ts of the first record that showed this login
  seat_id       TEXT    REFERENCES seat(id),         -- NULL = logged out during this period
  until_at      INTEGER,
  source_record TEXT    NOT NULL,                    -- record id that opened the period
  PRIMARY KEY (slot_id, since_at),
  CHECK (until_at IS NULL OR until_at >= since_at)
) STRICT;
CREATE UNIQUE INDEX slot_login_open ON slot_login(slot_id) WHERE until_at IS NULL;
CREATE INDEX slot_login_seat ON slot_login(seat_id);

-- One account tab in the desktop app, joined to a seat once signed in.
CREATE TABLE web_seat (
  tab_id        TEXT    NOT NULL,
  machine_id    TEXT    NOT NULL REFERENCES machine(id) ON DELETE CASCADE,
  seat_id       TEXT    REFERENCES seat(id),
  label         TEXT,
  first_seen    INTEGER NOT NULL,
  last_seen     INTEGER NOT NULL,
  stopped_reason TEXT,                               -- 401/403/429 stop
  PRIMARY KEY (machine_id, tab_id)
) STRICT;

---------------------------------------------------------------------------- access

-- One device = one token. See tokens.md.
CREATE TABLE device (
  id            TEXT    NOT NULL PRIMARY KEY,       -- 'dev_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  kind          TEXT    NOT NULL CHECK (kind IN ('machine', 'desktop', 'phone', 'browser')),
  name          TEXT    NOT NULL,
  machine_id    TEXT    REFERENCES machine(id),      -- required for machine and desktop
  token_hash    BLOB    NOT NULL UNIQUE CHECK (length(token_hash) = 32),  -- SHA-256 of the full token string
  token_hint    TEXT    NOT NULL,                    -- 'wy_m_…k3Qz' (prefix + last 4) for display
  scopes        TEXT    NOT NULL CHECK (json_valid(scopes)),              -- '["write"]'
  created_at    INTEGER NOT NULL,
  created_by    TEXT    NOT NULL,                    -- 'cli', 'pair:<code id>', 'setup'
  last_used_at  INTEGER,
  last_used_ip  TEXT,
  revoked_at    INTEGER, expires_at INTEGER, prefs TEXT CHECK (prefs IS NULL OR json_valid(prefs)),
  CHECK (kind NOT IN ('machine', 'desktop') OR machine_id IS NOT NULL)
) STRICT;
CREATE INDEX device_machine ON device(machine_id);

-- Secrets the hub makes for itself. Kept in the database so backups and moves keep push
-- subscriptions valid. (The pairing pepper is deliberately not here: pairing.md 2.1.)
CREATE TABLE hub_secret (
  key           TEXT    NOT NULL PRIMARY KEY,      -- vapid_private, action_key
  value         BLOB    NOT NULL,
  created_at    INTEGER NOT NULL
) STRICT;

-- One-time pairing codes and owner sign-in links (pairing.md 2 and 4).
CREATE TABLE pair_code (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'pc_' + ULID
  kind          TEXT    NOT NULL CHECK (kind IN ('machine', 'desktop', 'phone', 'browser', 'owner')),
  code_hash     BLOB    CHECK (code_hash IS NULL OR length(code_hash) = 32),   -- HMAC of the digits; NULL for owner links
  secret_hash   BLOB    NOT NULL UNIQUE CHECK (length(secret_hash) = 32),      -- HMAC of the QR/link secret
  name          TEXT,                               -- suggested device name
  machine_id    TEXT,                               -- optional, machine/desktop codes
  url           TEXT    NOT NULL,                   -- hub URL put in the QR
  created_at    INTEGER NOT NULL,
  created_by    TEXT    NOT NULL,                   -- 'cli', 'owner:<device id>'
  expires_at    INTEGER NOT NULL,
  claimed_at    INTEGER,
  cancelled_at  INTEGER,
  device_id     TEXT    REFERENCES device(id),
  claim_ip      TEXT
) STRICT;
CREATE INDEX pair_code_live ON pair_code(code_hash) WHERE claimed_at IS NULL AND cancelled_at IS NULL;

-- Channel kinds with a hub-wide switch.
CREATE TABLE channel (
  kind          TEXT    NOT NULL PRIMARY KEY CHECK (kind IN ('webpush', 'ntfy', 'link')),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  settings      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  updated_at    INTEGER NOT NULL
) STRICT;
INSERT INTO channel (kind, enabled, settings, updated_at) VALUES
  ('webpush', 1, '{}', 0),
  ('ntfy', 1, '{}', 0),
  ('link', 1, '{}', 0);
-- link: v8 (push.md 13): the Android app's own connection to the hub. v8 recreated channel and
-- push_target to widen the kind CHECK, v11 to narrow it; the other rows were copied unchanged.

-- One place a push goes: a browser subscription, an ntfy topic or a link device.
CREATE TABLE push_target (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'pt_' + ULID
  kind          TEXT    NOT NULL REFERENCES channel(kind),
  device_id     TEXT    REFERENCES device(id),     -- NULL: an ntfy topic added by the owner for the hub
  name          TEXT,                               -- "iPhone · Safari", "ntfy.sh/wy-4fQx…", "Pixel 9 · app"
  endpoint      TEXT    NOT NULL,                   -- webpush: endpoint URL; ntfy: server URL + '/' + topic; link: 'link:' + device id
  keys          TEXT    CHECK (keys IS NULL OR json_valid(keys)),     -- webpush and link: {"p256dh": "…", "auth": "…"} base64url
  secret        TEXT,                               -- ntfy access token; never returned by the API
  prefs         TEXT    CHECK (prefs IS NULL OR json_valid(prefs)),   -- only for targets without a device
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  last_ok_at    INTEGER,
  last_error    TEXT,
  last_error_at INTEGER,
  fail_count    INTEGER NOT NULL DEFAULT 0,
  gone_at       INTEGER,                            -- push service answered 404/410
  UNIQUE (kind, endpoint)
) STRICT;
CREATE INDEX push_target_device ON push_target(device_id);

---------------------------------------------------------------------------- activity

-- Every upload, exactly as received. The typed tables below are rebuilt from this.
CREATE TABLE record (
  id            TEXT    NOT NULL PRIMARY KEY,       -- client-made ULID / uuid (record.md 3.1)
  device_id     TEXT    NOT NULL REFERENCES device(id),
  machine_id    TEXT    NOT NULL,                    -- envelope machine (not an FK: kept even if the machine is removed)
  kind          TEXT    NOT NULL,
  v             INTEGER NOT NULL,
  ts            INTEGER NOT NULL,                    -- client time
  received_at   INTEGER NOT NULL,                    -- hub time
  slot          TEXT,                                -- envelope slot, as sent
  body          TEXT    NOT NULL CHECK (json_valid(body)),  -- the whole record, every field
  typed         INTEGER NOT NULL DEFAULT 0 CHECK (typed IN (0, 1)),  -- 1 once folded into typed tables
  type_error    TEXT                                 -- why typing failed, retried on replay
) STRICT;
CREATE INDEX record_kind_ts ON record(kind, ts);
CREATE INDEX record_machine_ts ON record(machine_id, ts);
CREATE INDEX record_machine_kind_slot_ts ON record(machine_id, kind, slot, ts);  -- a refold's session_list records (v15)
CREATE INDEX record_untyped ON record(received_at) WHERE typed = 0;

-- One Claude Code conversation.
CREATE TABLE session (
  id              TEXT    NOT NULL PRIMARY KEY,     -- Claude's session_id
  seat_id         TEXT    REFERENCES seat(id),       -- from the first record that named a seat; kept for life
  slot_id         INTEGER REFERENCES slot(id),
  machine_id      TEXT    REFERENCES machine(id),
  cwd             TEXT,
  project         TEXT,                              -- basename of cwd, for display
  name            TEXT,                              -- session_name / agents "name"
  title           TEXT,
  kind            TEXT,                              -- interactive, background
  transcript_path TEXT,
  remote_url      TEXT,                              -- Remote Control link
  term            TEXT    CHECK (term IS NULL OR json_valid(term)),  -- latest record.term
  started_at      INTEGER,
  ended_at        INTEGER,
  end_reason      TEXT,                              -- SessionEnd reason, or 'gone'
  last_event_at   INTEGER,
  last_listed_at  INTEGER,                           -- last complete session_list that listed it
  missed_lists    INTEGER NOT NULL DEFAULT 0,        -- complete lists since then that did not
  context_pct     REAL,
  model           TEXT
, moved_from_seat TEXT REFERENCES seat(id), moved_at INTEGER) STRICT;
-- seat_id: since v22 a live record of the session from another slot moves it there (identity.md 5.3).
-- moved_from_seat, moved_at: v22. The seat the session had before its last move
-- to another account (a live hook event or complete session_list from another slot) and when.
CREATE INDEX session_seat ON session(seat_id, last_event_at);
CREATE INDEX session_machine ON session(machine_id, last_event_at);
CREATE INDEX session_slot ON session(slot_id);

-- Every move of a session to another account (v22, identity.md 5.3), so each seat it left keeps
-- its peers handle as an alias (peers.md 3.2) after several moves.
CREATE TABLE session_move (
  session_id    TEXT    NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  at            INTEGER NOT NULL,
  from_seat     TEXT    REFERENCES seat(id),
  to_seat       TEXT    REFERENCES seat(id),
  from_slot     INTEGER REFERENCES slot(id),
  to_slot       INTEGER REFERENCES slot(id)
) STRICT;
CREATE INDEX session_move_session ON session_move(session_id, at);

-- Current state only (states.md). History is in hook_event.
CREATE TABLE session_state (
  session_id    TEXT    NOT NULL PRIMARY KEY REFERENCES session(id) ON DELETE CASCADE,
  state         TEXT    NOT NULL CHECK (state IN ('working', 'needs_permission', 'asked', 'done', 'failed', 'ended', 'gone')),
  since_at      INTEGER NOT NULL,
  detail        TEXT,                              -- notification type, failure reason, 'started', …
  question      TEXT,                              -- question line, when state = asked and allowed
  needs_you     INTEGER NOT NULL CHECK (needs_you IN (0, 1)),
  last_event    TEXT,                              -- hook event name that set it
  last_record   TEXT,                              -- record id that set it
  detector      TEXT,                              -- question detector version, e.g. 'q1'
  updated_at    INTEGER NOT NULL
, peers TEXT CHECK (peers IS NULL OR peers IN ('message', 'wait'))) STRICT;
-- peers: v13 (states.md 3.1). The current turn is coordination between sessions: 'message' (a peer
-- message started it) or 'wait' (it ended waiting on another session's reply); NULL otherwise.
CREATE INDEX session_state_needs ON session_state(needs_you, since_at) WHERE needs_you = 1;

CREATE TABLE hook_event (
  record_id     TEXT    NOT NULL PRIMARY KEY REFERENCES record(id) ON DELETE CASCADE,
  session_id    TEXT    NOT NULL REFERENCES session(id),
  event         TEXT    NOT NULL,                    -- hook_event_name
  subtype       TEXT,                                -- notification_type / SessionStart source / SessionEnd reason / StopFailure error
  tool_name     TEXT,
  ts            INTEGER NOT NULL,
  last_message  TEXT,                                -- derived.last_message, if allowed
  state_after   TEXT                                 -- state the engine set after this event
) STRICT;
CREATE INDEX hook_event_session_ts ON hook_event(session_id, ts);
CREATE INDEX hook_event_ts ON hook_event(ts);

-- Every (state, detail) change of a session, in fold order (v2). Rebuilt with the session's
-- fold; session_state holds only the newest.
CREATE TABLE session_transition (
  session_id    TEXT    NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  seq           INTEGER NOT NULL,                    -- 0, 1, 2… in fold order
  at            INTEGER NOT NULL,                    -- ts of the input that caused it
  from_state    TEXT,                                -- NULL for the first one
  to_state      TEXT    NOT NULL,
  detail        TEXT,
  cause         TEXT    NOT NULL,                    -- hook event name, 'session_list' or 'gone'
  record_id     TEXT,                                -- record that caused it
  PRIMARY KEY (session_id, seq)
) STRICT;

-- One API response from transcripts, merged by keeping the largest counts.
CREATE TABLE token_row (
  key           TEXT    NOT NULL PRIMARY KEY,       -- API message id
  session_id    TEXT    NOT NULL,                    -- not an FK: rows can arrive before the session's hook events
  seat_id       TEXT    REFERENCES seat(id),         -- resolved from the session; NULL = Unattributed
  ts            INTEGER NOT NULL,
  project       TEXT,
  model         TEXT,
  input         INTEGER NOT NULL DEFAULT 0,
  output        INTEGER NOT NULL DEFAULT 0,
  cache_5m      INTEGER NOT NULL DEFAULT 0,
  cache_1h      INTEGER NOT NULL DEFAULT 0,
  cache_read    INTEGER NOT NULL DEFAULT 0,
  thinking      INTEGER NOT NULL DEFAULT 0,
  sidechain     INTEGER NOT NULL DEFAULT 0 CHECK (sidechain IN (0, 1)),
  record_id     TEXT    NOT NULL                     -- last record that raised a count
) STRICT;
CREATE INDEX token_row_ts ON token_row(ts);
CREATE INDEX token_row_session ON token_row(session_id);
CREATE INDEX token_row_session_ts ON token_row(session_id, ts);                  -- LastTokenRowAt (v15)
CREATE INDEX token_row_seat_ts ON token_row(seat_id, ts);

-- "You've hit your limit" replies found in transcripts.
CREATE TABLE limit_hit (
  session_id    TEXT    NOT NULL,
  ts            INTEGER NOT NULL,
  type          TEXT,
  status        TEXT,
  resets_at     INTEGER,
  seat_id       TEXT    REFERENCES seat(id),
  PRIMARY KEY (session_id, ts)
) STRICT;

-- Claude Code's own daily totals from before Watchyard (history_import).
CREATE TABLE history_day (
  slot_id       INTEGER NOT NULL REFERENCES slot(id) ON DELETE CASCADE,
  day           TEXT    NOT NULL CHECK (day GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
  model         TEXT    NOT NULL,                    -- '' for activity-only rows
  seat_id       TEXT    REFERENCES seat(id),
  tokens        INTEGER,
  messages      INTEGER,
  sessions      INTEGER,
  tool_calls    INTEGER,
  record_id     TEXT    NOT NULL,
  PRIMARY KEY (slot_id, day, model)
) STRICT;

---------------------------------------------------------------------------- usage

-- One limit reading: from the status line (per session) or from claude.ai (per seat).
CREATE TABLE limit_sample (
  seat_id       TEXT    NOT NULL REFERENCES seat(id),
  window_key    TEXT    NOT NULL,                    -- five_hour, seven_day, seven_day_opus, weekly_fable, iguana_necktie, …
  ts            INTEGER NOT NULL,
  source        TEXT    NOT NULL CHECK (source IN ('statusline', 'web')),
  used_pct      REAL,
  resets_at     INTEGER,
  used_dollars  REAL,
  limit_dollars REAL,
  session_id    TEXT,                                -- statusline only
  record_id     TEXT    NOT NULL,
  PRIMARY KEY (seat_id, window_key, ts, source)
) STRICT;
CREATE INDEX limit_sample_ts ON limit_sample(ts);
CREATE INDEX limit_sample_reset ON limit_sample(seat_id, window_key, resets_at); -- a window's sources (v15)
-- the newest idle reading (v15):
CREATE INDEX limit_sample_idle ON limit_sample(seat_id, window_key, ts)
  WHERE source = 'web' AND resets_at IS NULL AND used_pct = 0;

-- One row per pick `wy account next` recorded on some machine (record.md 4.10). Other
-- machines' pickers read the recent ones through GET /v1/usage (setup.md 8.5). `wy hub prune`
-- removes rows older than its cut, like token_row.
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

-- One row per seat: the calibration of `wy account next`'s lanes strategy (setup.md 8.5,
-- Calibration), brought up to date every 15 minutes by `wy hub serve` from the samples, token
-- rows and leases after its cursors. GET /v1/usage returns `factors` as they are.
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

-- The full claude.ai usage response per seat per poll. Every field kept (iguana_necktie…).
CREATE TABLE web_usage (
  seat_id       TEXT    NOT NULL REFERENCES seat(id),
  ts            INTEGER NOT NULL,
  tab_id        TEXT,
  raw           TEXT    NOT NULL CHECK (json_valid(raw)),  -- payload.usage, whole
  record_id     TEXT    NOT NULL,
  PRIMARY KEY (seat_id, ts)
) STRICT;

-- One limit window per seat: built from limit_sample by the usage model.
CREATE TABLE usage_window (
  seat_id       TEXT    NOT NULL REFERENCES seat(id),
  window_key    TEXT    NOT NULL,
  resets_at     INTEGER NOT NULL,
  started_at    INTEGER,                             -- resets_at minus the window length, or first sample
  peak_pct      REAL,
  final_pct     REAL,                                -- last sample before reset
  unused_pct    REAL,                                -- 100 - final_pct, once reset has passed
  samples       INTEGER NOT NULL DEFAULT 0,
  alerted_80    INTEGER NOT NULL DEFAULT 0 CHECK (alerted_80 IN (0, 1)),
  alerted_95    INTEGER NOT NULL DEFAULT 0 CHECK (alerted_95 IN (0, 1)),
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (seat_id, window_key, resets_at)
) STRICT;

-- Rebuilt on demand from token_row (and history_day for older days).
CREATE TABLE rollup_daily (
  day           TEXT    NOT NULL,                    -- local day of the hub's configured time zone
  seat_id       TEXT    NOT NULL DEFAULT '',         -- '' = Unattributed
  model         TEXT    NOT NULL DEFAULT '',
  project       TEXT    NOT NULL DEFAULT '',
  input         INTEGER NOT NULL DEFAULT 0,
  output        INTEGER NOT NULL DEFAULT 0,
  cache_5m      INTEGER NOT NULL DEFAULT 0,
  cache_1h      INTEGER NOT NULL DEFAULT 0,
  cache_read    INTEGER NOT NULL DEFAULT 0,
  replies       INTEGER NOT NULL DEFAULT 0,
  api_usd       REAL    NOT NULL DEFAULT 0,          -- API list-price equivalent
  built_at      INTEGER NOT NULL,
  PRIMARY KEY (day, seat_id, model, project)
) STRICT;

---------------------------------------------------------------------------- alerts

-- Global rules: one base rule per trigger plus scoped overrides (push.md 3).
CREATE TABLE rule (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'rule_<trigger>' for base rules, 'rule_' + ULID for overrides
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  trigger       TEXT    NOT NULL,                   -- unknown triggers are ignored by older hubs
  base          INTEGER NOT NULL DEFAULT 0 CHECK (base IN (0, 1)),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  delay_s       INTEGER NOT NULL DEFAULT 0 CHECK (delay_s BETWEEN 0 AND 86400),
  bypass_quiet  INTEGER NOT NULL DEFAULT 0 CHECK (bypass_quiet IN (0, 1)),
  scope         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(scope)),   -- {"seats","projects","machines","windows"}
  updated_at    INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX rule_base ON rule(trigger) WHERE base = 1;
-- The owner row normally appears after the migrations; the seeded rules below need it now.
INSERT OR IGNORE INTO owner (id, created_at) VALUES ('owner', 0);
INSERT INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES
  ('rule_needs_permission', 'needs_permission', 1, 1,   30, 0, '{}', 0),
  ('rule_asked',            'asked',            1, 1,   60, 0, '{}', 0),
  ('rule_failed',           'failed',           1, 1,    0, 1, '{}', 0),
  ('rule_limit_80',         'limit_80',         1, 1,    0, 0, '{"windows":["five_hour","seven_day"]}', 0),
  ('rule_limit_95',         'limit_95',         1, 1,    0, 0, '{"windows":["five_hour","seven_day"]}', 0),
  ('rule_reset',            'reset',            1, 0,    0, 0, '{"windows":["five_hour","seven_day"]}', 0),
  ('rule_machine_silent',   'machine_silent',   1, 1, 3600, 0, '{}', 0);
-- v19: the usage notices (push.md 3.9).
INSERT INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES
  ('rule_limit_change',     'limit_change',     1, 1,    0, 0, '{}', 0),
  ('rule_usage_reset',      'usage_reset',      1, 1,    0, 0, '{}', 0),
  ('rule_seat_change',      'seat_change',      1, 1,    0, 0, '{}', 0),
  ('rule_weekly_drop',      'weekly_drop',      1, 1,    0, 0, '{}', 0);
-- v23: the Smart Account Switch notices (push.md 3.9).
INSERT INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES
  ('rule_session_moved',    'session_moved',    1, 1,    0, 0, '{}', 0),
  ('rule_switch_digest',    'switch_digest',    1, 1,    0, 0, '{}', 0);
-- v24: the owner drain notices (push.md 3.9).
INSERT INTO rule (id, trigger, base, enabled, delay_s, bypass_quiet, scope, updated_at) VALUES
  ('rule_drain_started',    'drain_started',    1, 1,    0, 0, '{}', 0),
  ('rule_drain_ended',      'drain_ended',      1, 1,    0, 0, '{}', 0),
  ('rule_drain_pulled',     'drain_pulled',     1, 1,    0, 0, '{}', 0);

-- One row per episode (push.md 3.2 and 4).
CREATE TABLE alert (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'al_' + ULID
  episode       TEXT    NOT NULL UNIQUE,            -- dedupe key
  trigger       TEXT    NOT NULL,
  rule_id       TEXT,                               -- rule in force at creation (not an FK: overrides can be deleted)
  session_id    TEXT    REFERENCES session(id),
  seat_id       TEXT    REFERENCES seat(id),
  machine_id    TEXT    REFERENCES machine(id),
  window_key    TEXT,
  status        TEXT    NOT NULL CHECK (status IN ('pending', 'fired', 'snoozed', 'seen', 'cancelled', 'closed', 'skipped')),
  reason        TEXT,                               -- answered, ended, superseded, stale, rule_off, remote_control, baseline, recovered, reset_passed, direct
  since_at      INTEGER,                            -- when the condition began (client ts for sessions)
  created_at    INTEGER NOT NULL,
  due_at        INTEGER,                            -- next scheduler action (pending, snoozed); NULL otherwise
  fired_at      INTEGER,                            -- first push
  fire_count    INTEGER NOT NULL DEFAULT 0,
  snoozed_until INTEGER,
  seen_at       INTEGER,
  seen_by       TEXT,                               -- device id or 'ntfy:<target id>'
  ended_at      INTEGER,                            -- when it became cancelled, closed or skipped
  facts         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(facts)),   -- text inputs at creation: labels, question, pct, resets_at, remote_url
  updated_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX alert_due ON alert(due_at) WHERE due_at IS NOT NULL;
CREATE INDEX alert_open ON alert(session_id) WHERE status IN ('pending', 'fired', 'snoozed', 'seen');
CREATE INDEX alert_created ON alert(created_at);
CREATE INDEX alert_seat ON alert(seat_id) WHERE seat_id IS NOT NULL;
CREATE INDEX alert_machine ON alert(machine_id) WHERE machine_id IS NOT NULL;

-- One attempt to bring one alert (fire) to one target.
CREATE TABLE delivery (
  id            INTEGER NOT NULL PRIMARY KEY,
  alert_id      TEXT    NOT NULL REFERENCES alert(id) ON DELETE CASCADE,
  target_id     TEXT    NOT NULL,                   -- not an FK: the log outlives a removed target
  target_kind   TEXT    NOT NULL,
  fire          INTEGER NOT NULL,                   -- alert.fire_count this belongs to
  batch         TEXT,                               -- 'pb_' + ULID; deliveries sent as one push share it
  status        TEXT    NOT NULL CHECK (status IN ('pending', 'held', 'sent', 'failed', 'skipped', 'gone')),
  reason        TEXT,                               -- muted, quiet, flood, resolved, seen, channel_off
  tries         INTEGER NOT NULL DEFAULT 0,
  next_at       INTEGER,                            -- pending: next try; held: release time
  http_status   INTEGER,
  last_error    TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  delivered_at  INTEGER,                            -- link only: when the phone acked the frame that carried it
  UNIQUE (alert_id, target_id, fire)
) STRICT;
CREATE INDEX delivery_next ON delivery(next_at) WHERE next_at IS NOT NULL;
CREATE INDEX delivery_batch ON delivery(batch);
CREATE INDEX delivery_target ON delivery(target_id, status, updated_at);
-- delivered_at: v8 (push.md 13.4); v11 recreated delivery without its expo columns.

-- One remote the hub pulls over SSH (machines.md 3.4).
CREATE TABLE pull_target (
  id            TEXT    NOT NULL PRIMARY KEY,               -- 'pt_' + ULID
  owner_id      TEXT    NOT NULL DEFAULT 'owner' REFERENCES owner(id),
  name          TEXT    NOT NULL UNIQUE,                    -- machine name, used by the CLI
  machine_id    TEXT    UNIQUE REFERENCES machine(id),      -- NULL until bound (first hello)
  device_id     TEXT    REFERENCES device(id),              -- the pull device; NULL until bound
  host          TEXT    NOT NULL,                           -- HostName
  port          INTEGER NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
  user          TEXT    NOT NULL,
  jump          TEXT,                                       -- ProxyJump, as typed
  remote_wy     TEXT    NOT NULL DEFAULT '.local/bin/wy',   -- used only without a forced command
  host_keys     TEXT    NOT NULL CHECK (json_valid(host_keys)),  -- ["ssh-ed25519 AAAA…", …] pinned public keys
  interval_s    INTEGER NOT NULL DEFAULT 60 CHECK (interval_s BETWEEN 15 AND 3600),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  state         TEXT    NOT NULL DEFAULT 'new'
                CHECK (state IN ('new', 'ok', 'failing', 'host_key', 'auth', 'refused', 'disabled')),
  created_at    INTEGER NOT NULL,
  last_try_at   INTEGER,
  last_ok_at    INTEGER,
  last_error    TEXT,                                       -- one line, no secrets
  fail_count    INTEGER NOT NULL DEFAULT 0,
  next_at       INTEGER                                      -- scheduler: next run
) STRICT;
CREATE INDEX pull_target_next ON pull_target(enabled, next_at);

---------------------------------------------------------------------------- keys and link (v7)

-- The owner key's public half (keys.md 3). At most one is active; a key proposed by a device
-- waits as 'pending' until an owner session or the hub's CLI confirms it. On confirmation the
-- hub signs the document too (hub_sig); the hub and the devices use a key only with both
-- signatures, so a row written straight into the database is ignored.
CREATE TABLE owner_key (
  fp            TEXT    NOT NULL PRIMARY KEY,      -- keys.md 2: 24 base32 characters
  pub           BLOB    NOT NULL CHECK (length(pub) = 32),   -- Ed25519 public key (copy of the body's)
  body          BLOB    NOT NULL,                  -- the owner_key document body, as signed (keys.md 4.1)
  self_sig      BLOB    NOT NULL CHECK (length(self_sig) = 64),
  hub_sig       BLOB    CHECK (hub_sig IS NULL OR length(hub_sig) = 64),   -- the hub's confirmation (keys.md 3.3)
  state         TEXT    NOT NULL CHECK (state IN ('pending', 'active', 'retired')),
  proposed_by   TEXT    NOT NULL,                  -- device id, or 'cli' (hub machine)
  created_at    INTEGER NOT NULL,
  confirmed_at  INTEGER,
  confirmed_by  TEXT,                              -- owner session device id, or 'cli'
  retired_at    INTEGER,
  CHECK ((state = 'pending') = (hub_sig IS NULL))
) STRICT;
CREATE UNIQUE INDEX owner_key_active ON owner_key(state) WHERE state = 'active';

-- One device's link keys and its certificate (keys.md 4). The certificate body is kept as the
-- exact bytes that were signed; the columns beside it are copies for queries. The hub takes
-- keys only from cert after checking hub_sig (link.md 3.2), never from the copies.
CREATE TABLE device_key (
  device_id     TEXT    NOT NULL PRIMARY KEY REFERENCES device(id) ON DELETE CASCADE,
  sign_alg      TEXT    NOT NULL CHECK (sign_alg IN ('ed25519', 'p256')),
  sign_pub      BLOB    NOT NULL,                  -- 32 bytes (ed25519) or 65 bytes SEC1 uncompressed (p256)
  box_alg       TEXT    NOT NULL CHECK (box_alg IN ('x25519', 'p256')),
  box_pub       BLOB    NOT NULL,                  -- 32 bytes (x25519) or 65 bytes (p256, RFC 8291 p256dh)
  reg           TEXT    NOT NULL CHECK (reg IN ('qr', 'tofu', 'ssh', 'local')),
  cert          BLOB    NOT NULL,                  -- certificate body (JSON bytes, keys.md 4.2)
  hub_sig       BLOB    NOT NULL CHECK (length(hub_sig) = 64),
  owner_fp      TEXT,                              -- set once the owner key certified it
  owner_sig     BLOB    CHECK (owner_sig IS NULL OR length(owner_sig) = 64),
  created_at    INTEGER NOT NULL,
  certified_at  INTEGER,
  CHECK ((owner_fp IS NULL) = (owner_sig IS NULL))
) STRICT;

-- The accounts a machine's device may speak for in peer messages (keys.md 4.3): the body and
-- owner signature of its newest machine_accounts document. The hub re-verifies it against the
-- device's current certificate whenever it reads it, so a row for replaced keys counts for
-- nothing.
CREATE TABLE device_accounts (
  device_id     TEXT    NOT NULL PRIMARY KEY REFERENCES device(id) ON DELETE CASCADE,
  body          BLOB    NOT NULL,                  -- machine_accounts body (JSON bytes, keys.md 4.3)
  owner_fp      TEXT    NOT NULL,
  owner_sig     BLOB    NOT NULL CHECK (length(owner_sig) = 64),
  created_at    INTEGER NOT NULL
) STRICT;

-- Every version of the signed revocation list (keys.md 6). The list only grows.
CREATE TABLE revocation_doc (
  version       INTEGER NOT NULL PRIMARY KEY,
  body          BLOB    NOT NULL,                  -- the signed JSON bytes
  signer        TEXT    NOT NULL CHECK (signer IN ('owner', 'hub')),
  key_fp        TEXT    NOT NULL,
  sig           BLOB    NOT NULL CHECK (length(sig) = 64),
  made_by       TEXT    NOT NULL,                  -- device id or 'cli'
  created_at    INTEGER NOT NULL
) STRICT;

-- Per-device position on the link (link.md 5).
CREATE TABLE link_state (
  device_id     TEXT    NOT NULL PRIMARY KEY REFERENCES device(id) ON DELETE CASCADE,
  next_seq      INTEGER NOT NULL DEFAULT 1 CHECK (next_seq >= 1),
  acked_seq     INTEGER NOT NULL DEFAULT 0 CHECK (acked_seq >= 0),
  transport     TEXT    CHECK (transport IS NULL OR transport IN ('ws', 'poll')),
  connected_at  INTEGER,                           -- current connection; NULL while disconnected
  last_seen_at  INTEGER,                           -- last frame, ping or poll
  last_close    TEXT,                              -- close code and word: '4409 superseded'
  last_ip       TEXT,
  hello         TEXT    CHECK (hello IS NULL OR json_valid(hello))   -- last hello's app/wy version and known doc versions
) STRICT;

-- The durable queue: one sealed, signed frame per row until the device acks it (link.md 5).
CREATE TABLE link_queue (
  device_id     TEXT    NOT NULL REFERENCES device(id) ON DELETE CASCADE,
  seq           INTEGER NOT NULL,
  type          TEXT    NOT NULL CHECK (type IN ('alert', 'refresh', 'peer_msg', 'peer_status', 'matrix', 'revoke', 'peer_thread_resume')),
  tag           TEXT    NOT NULL DEFAULT '' CHECK (length(tag) <= 64 AND tag NOT GLOB '*[^A-Za-z0-9_:.-]*'),   -- collapse key; '' never collapses
  body          BLOB    NOT NULL,                  -- sealed to the device (link.md 4)
  sig           BLOB    NOT NULL CHECK (length(sig) = 64),   -- hub signature, made at enqueue
  exp           INTEGER NOT NULL,
  ref           TEXT,                              -- 'd:<delivery id>' or 'pm:<peer message id>'
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (device_id, seq)
) STRICT, WITHOUT ROWID;
CREATE INDEX link_queue_tag ON link_queue(device_id, tag) WHERE tag <> '';
CREATE INDEX link_queue_exp ON link_queue(exp);

---------------------------------------------------------------------------- peers (v9)

-- Every version of the signed account matrix (peers.md 5).
CREATE TABLE matrix_doc (
  version       INTEGER NOT NULL PRIMARY KEY,
  body          BLOB    NOT NULL,                  -- the signed JSON bytes
  signer        TEXT    NOT NULL CHECK (signer IN ('owner', 'hub')),
  key_fp        TEXT    NOT NULL,
  sig           BLOB    NOT NULL CHECK (length(sig) = 64),
  widens        INTEGER NOT NULL CHECK (widens IN (0, 1)),   -- compared with the version before it
  made_by       TEXT    NOT NULL,                  -- device id or 'cli'
  created_at    INTEGER NOT NULL
) STRICT;

-- One conversation between two sessions (peers.md 6.2). a_session opened it.
CREATE TABLE peer_thread (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 't_' + 16 hex
  a_session     TEXT    NOT NULL,
  b_session     TEXT    NOT NULL,
  a_account     TEXT    NOT NULL,                  -- account uuids
  b_account     TEXT    NOT NULL,
  opened_at     INTEGER NOT NULL,
  last_at       INTEGER NOT NULL,
  human_at      INTEGER,                           -- newest human prompt reported in the thread
  turns         INTEGER NOT NULL DEFAULT 0,        -- messages since human_at (loop guard)
  paused_at     INTEGER,                           -- the loop guard stopped it
  closed_at     INTEGER,
  CHECK (a_session <> b_session)
) STRICT;
CREATE INDEX peer_thread_open ON peer_thread(last_at) WHERE closed_at IS NULL;

-- One peer message, metadata only (peers.md 10).
CREATE TABLE peer_message (
  id            TEXT    NOT NULL PRIMARY KEY,      -- msg_id (uuid v4), also the inbox frame's msg_id
  thread_id     TEXT    NOT NULL,                  -- not an FK: the log outlives a pruned thread
  from_session  TEXT    NOT NULL,
  from_device   TEXT    NOT NULL,
  from_account  TEXT    NOT NULL,
  to_session    TEXT    NOT NULL,
  to_device     TEXT    NOT NULL,
  to_account    TEXT    NOT NULL,
  kind          TEXT    NOT NULL CHECK (kind IN ('start', 'reply')),
  size          INTEGER NOT NULL CHECK (size >= 0), -- body bytes before sealing
  local         INTEGER NOT NULL DEFAULT 0 CHECK (local IN (0, 1)),   -- same machine; reported by the agent
  status        TEXT    NOT NULL CHECK (status IN ('queued', 'delivered', 'refused', 'rejected', 'expired', 'target_gone', 'failed')),
  detail        TEXT,                              -- reason word: not_allowed, paused, loop_guard, bad_signature, …
  created_at    INTEGER NOT NULL,
  expires_at    INTEGER NOT NULL,
  final_at      INTEGER
, from_kind TEXT CHECK (from_kind IS NULL OR from_kind IN ('service:doorbell')), via_tag TEXT) STRICT;
-- from_kind, via_tag: v21 (peers.md 4.1a). from_kind: 'service:doorbell' for a trusted send, NULL for a
-- session's; via_tag: the tag it went to. Added by migration 0021 (ALTER TABLE ADD COLUMN), so the
-- columns sit on the final_at line, as SQLite writes them.
CREATE INDEX peer_message_thread ON peer_message(thread_id, created_at);
CREATE INDEX peer_message_created ON peer_message(created_at);
CREATE INDEX peer_message_from_kind ON peer_message(from_kind, created_at) WHERE from_kind IS NOT NULL;

-- Everything else worth a line: matrix versions, pause, loop guard, refusals at a machine.
CREATE TABLE peer_audit (
  id            INTEGER NOT NULL PRIMARY KEY,
  at            INTEGER NOT NULL,
  actor         TEXT    NOT NULL,                  -- device id, 'hub' or 'cli'
  event         TEXT    NOT NULL,                  -- matrix, pause, resume, loop_guard, rejected, off, on, revoke
  thread_id     TEXT,
  msg_id        TEXT,
  detail        TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(detail))
) STRICT;
CREATE INDEX peer_audit_at ON peer_audit(at);
-- event also: trust, trust_off, trust_revoke (v21, the trusted sender, peers.md 4.1a).

-- The tags a session gave itself (peers.md 3.4), held per session so every machine's directory
-- shows them. Cleared when the session ends or goes away (its state becomes ended or gone).
CREATE TABLE peer_tag (
  session_id    TEXT    NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  tag           TEXT    NOT NULL CHECK (length(tag) BETWEEN 1 AND 40 AND tag NOT GLOB '*[^a-z0-9-]*'),
  device_id     TEXT    NOT NULL,                  -- the agent device that set it
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (session_id, tag)
) STRICT;
CREATE INDEX peer_tag_tag ON peer_tag(tag);

-- Each machine's trusted sender (peers.md 4.1a): the approved Doorbell as the machine's agent
-- reported it (link.md 6.2 peer_trust), and the owner's revocation.
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
