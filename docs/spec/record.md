# Records

Status: spec v1. Applies to `wy` (every client command), the desktop app and the hub.
Change this file before changing any code that writes or reads records ("spec first").

A **record** is one fact a client saw: a hook event, a status-line limit reading, a claude.ai
usage response, and so on. Clients write records to their local outbox
([outbox.md](outbox.md)) and later upload them in batches to `POST /v1/ingest`
([api.yaml](api.yaml)). The hub stores every record exactly as received in the `record`
table ([schema.sql](schema.sql)) and builds its typed tables from it. A bug in a typed table
is fixed by replaying the `record` table.

## 1. Rules that apply to every record

1. **Raw is kept whole.** The hub never drops a field it does not know. Unknown top-level
   envelope fields and unknown payload fields are stored in `record.body` and survive
   replay. Example: every key of the claude.ai usage response, including
   `iguana_necktie`, `seven_day_omelette`, `tangelo` and any key added later.
2. **Unknown kinds are accepted.** A record whose `kind` the hub does not know is stored in
   `record` with `typed = 0` and reported as accepted. Old clients keep working with a new
   hub, and a new client's records are not lost on an old hub.
3. **Content never leaves the machine.** The only fields a client removes or shortens are
   the content fields in section 4 (prompts, tool input and output, full assistant
   messages). That list is closed: everything else is kept as Claude Code wrote it.
4. **No secrets.** No record ever holds a Claude token, cookie, API key or password. As a
   second line of defence, `wy` deletes any object key, at any depth, whose name matches
   `(?i)(token|secret|password|credential|cookie|api[_-]?key|session[_-]?key)` from
   payloads it did not build itself. The deletion is listed in `redacted` (path relative to
   the payload, `.` between keys, `[]` for arrays). Two exceptions: (a) token **counters**
   are kept: a key containing `tokens` (plural) whose value is a number or boolean
   (`input_tokens`, `total_input_tokens`, `exceeds_200k_tokens`, `totalTokens`), and an object
   under a key ending in `tokens_details`; a singular `token`/`id_token` or any string value
   is still deleted. Without this the filter would delete the counters of every status-line
   payload. (b) `web_usage` `payload.usage` is exempt (section 4.3).
5. **Idempotent.** The record `id` is made once, by the client, when the record is first
   written. Resending a record with the same `id` is harmless: the hub stores it once and
   reports it as accepted again.
6. **Time.** All times are RFC 3339 strings in UTC with millisecond precision and a `Z`
   suffix, e.g. `2026-10-11T14:03:22.418Z`. The hub also stores its own receive time.
   Unix-epoch numbers inside raw payloads (for example `startedAt` from `claude agents`)
   are left as they are.

   **Clock skew.** A client clock can be wrong. The hub keeps the record exactly as sent,
   but for ordering (session folds, usage `final_pct`, `machine.last_record`, the `ts`
   column of the `record` table) it uses `min(ts, received_at)` when `ts` is more than
   5 minutes after `received_at`; a `ts` within 5 minutes ahead is used as sent. Records
   are never rejected for their `ts` alone.
7. **Size.** One encoded record is at most 1 MiB. A client that would exceed this splits
   the payload (only `token_rows` and `history_import` can get that big) into several
   records.

## 2. The upload batch

One `POST /v1/ingest` request body. Sent as `application/json`, optionally with
`Content-Encoding: gzip`.

```json
{
  "v": 1,
  "client": "wy/0.1.0",
  "records": [ <record>, <record>, ... ]
}
```

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `v` | integer | yes | Batch format version. Always `1` for this spec. |
| `client` | string | no | Name and version of the uploader, `name/semver`. Informational. |
| `records` | array of record | yes | 1 to 1000 records. The batch is at most 8 MiB before gzip. |

The device is known from the bearer token, not from the body. A `device` field in the body
is ignored if present.

The reply is described in [api.yaml](api.yaml): every id in the batch appears exactly once,
either in `accepted` (stored now or already stored) or in `rejected` with a reason. The
client deletes (advances its cursor past) both. It keeps and retries only when the whole
request failed (network error, 5xx, 429).

## 3. The record envelope

```json
{
  "v": 1,
  "id": "01JA2XQ8R7Y5V0Z6M3K1T9B4CD",
  "kind": "hook_event",
  "ts": "2026-10-11T14:03:22.418Z",
  "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
  "slot": "~/.claude-2",
  "seat": {
    "account": "3f1c6a2e-0000-4000-8000-000000000001",
    "org": "9b20d4f1-0000-4000-8000-000000000002",
    "email": "a@example.com",
    "src": "claude_json"
  },
  "term": {"app": "warp", "id": "6c1d…", "focus_url": "warp://focus/6c1d…"},
  "client": "wy/0.1.0",
  "redacted": ["prompt"],
  "payload": { ... }
}
```

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `v` | integer | yes | Record format version. `1`. A hub that sees a higher `v` stores the record raw and does not type it. The hub says which `v` it reads (`record_v_max`) and clients never send a higher one (machines.md 7.2). |
| `id` | string | yes | Idempotency key. See 3.1. |
| `kind` | string | yes | One of the kinds in section 4 (seven original kinds, plus `machine_info` and `direct_push`), or any future kind (`^[a-z][a-z0-9_]{0,39}$`). |
| `ts` | string | yes | When the client observed the fact (RFC 3339 UTC, ms). |
| `machine` | string | yes | The machine id from [identity.md](identity.md) (`m_` + ULID). |
| `slot` | string | depends | The config folder the fact came from, written with `~` for the home folder and `/` as separator (`~/.claude`, `~/.claude-2`, `~/work/.claude-x`). Required for `hook_event`, `limit_sample`, `session_list`, `slot_info`; optional for `token_rows` and `history_import` (omitted when the transcripts folder is shared by several slots); absent for `web_usage`. |
| `seat` | object | no | The login the slot had at `ts`. See 3.2. Absent when the folder is not logged in or the login could not be read. |
| `term` | object | no | Terminal the event came from. See 3.3. Only on `hook_event`. |
| `client` | string | no | `name/semver` of the program that wrote this record (`wy/0.1.0`, `watchyard-desktop/0.6.0`). |
| `redacted` | array of string | no | JSON paths (dot notation, `[]` for arrays) of fields the client removed or shortened, e.g. `["prompt", "tool_input"]`. Lets the hub tell "absent" from "removed". |
| `derived` | object | no | Values the client computed from content it then removed. Defined per kind below. Never contains content beyond what section 4 allows. |
| `payload` | object | yes | The fact itself. Per kind below. Always a JSON object. |

### 3.1 Record ids

- Format: a **ULID** (26 characters, Crockford base32, upper case), made with a
  cryptographic random source: `01JA2XQ8R7Y5V0Z6M3K1T9B4CD`. ULIDs sort by creation time,
  which keeps the hub's `record` index append-mostly and makes outbox order easy to check.
- The hub accepts any string matching `^[0-9A-Za-z_-]{16,64}$`, so the desktop app may use
  `crypto.randomUUID()` (36 characters, lower case with dashes).
- The id is made when the record is first written to the outbox and never changes, even
  across retries, hub moves or `wy hub import`.
- The hub's duplicate key is the id alone. If a second upload carries an id that is already
  stored **from the same device**, it is accepted (duplicate). If it is stored from a
  **different** device, it is rejected with reason `id_conflict` and not stored again.
- **Machines:** an id already stored from a different device **bound to the same machine**
  (the stored row's `machine_id` equals this record's `machine`, and the uploading device is
  bound to that machine) is also a duplicate, not a conflict. A machine that changes reach
  (pull device → its own token, machines.md 3.6), is re-paired, or resends after a hub move
  therefore never sees `id_conflict` for its own records.

### 3.2 `seat`

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `account` | string (uuid) | yes | `oauthAccount.accountUuid` from the slot's `.claude.json`, or the claude.ai account uuid. |
| `org` | string (uuid) | yes | `oauthAccount.organizationUuid`, or the claude.ai org uuid. |
| `email` | string | no | `oauthAccount.emailAddress`. Display and fallback only, never a key. |
| `src` | string | no | Where it was read: `claude_json` (the slot's `.claude.json`), `auth_status` (`claude auth status --json`), `claude_ai` (desktop app tab). |

The hub turns `(account, org)` into a `seat` row and, for slots, opens or continues a
`slot_login` period (see [identity.md](identity.md)). A record without `seat` is still
stored; its session is attributed later or shows as Unattributed.

### 3.3 `term` (optional)

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `app` | string | yes | Terminal program, lower case: `warp`, `ghostty`, `iterm`, `wezterm`, `kitty`, `vscode`, `windows-terminal`, `tmux`, `unknown`. From `TERM_PROGRAM`, `WARP_*`, `TMUX` and similar. |
| `id` | string | no | The terminal's own pane or session id. Warp: `WARP_TERMINAL_SESSION_UUID`. |
| `focus_url` | string | no | URL that brings this pane forward. Warp: `WARP_FOCUS_URL`. Over SSH: `LC_WY_FOCUS` passed from the laptop. |
| `host` | string | no | Machine id of the machine whose terminal shows this pane, when it differs from `machine` (an SSH session shown in a laptop tab). Set by `wy hook` from `LC_WY_HOST`, which `wy ssh` sends (terminal.md T9). |

The hub serves `focus_url` only to readers on the machine named by `host` (or `machine` when
`host` is absent). Phones never receive it.

## 4. The kinds

### 4.1 `hook_event`

Written by `wy hook` for each Claude Code hook event (see [states.md](states.md) for which
events are installed).

- `payload` is the JSON object Claude Code wrote to the hook's stdin, with every field
  kept, except the content fields in the table below.
- `slot` and `seat` are required (seat when logged in).

Content fields (the closed list, checked against the captured payloads in `testdata/hooks/`).
Each is removed on **every** event, whether or not it is listed against that event below
(a content field on an event nobody has captured yet is still content); the Events column
says where Claude Code is known to send it. The one exception is `error` on StopFailure.

| Field in hook input | Events | What `wy` does |
| --- | --- | --- |
| `prompt` | UserPromptSubmit | Removed. `derived.prompt_chars` = its length in Unicode code points. |
| `tool_input` | PreToolUse, PostToolUse, PostToolUseFailure, PermissionRequest, PermissionDenied | Removed. `tool_name` and `tool_use_id` are kept. For `AskUserQuestion`, `derived.question` = the first question's text, cut to 300 characters, only if last messages are allowed. |
| `tool_response` | PostToolUse | Removed. |
| `permission_suggestions` | PermissionRequest | Removed (the suggested rules quote the command). `derived.permission_suggestions` = their count. |
| `error` | PostToolUseFailure | Removed: it is tool output (`"Exit code 1\ncat: …"`). `derived.error_chars` = its length. `is_interrupt` and `duration_ms` are kept. |
| `error` | StopFailure | **Kept**: it is a short error code (`model_not_found`, `rate_limit`, …), not content. |
| `last_assistant_message` | Stop, SubagentStop, StopFailure | Removed. If last messages are allowed, `derived.last_message` = the tail of the message (see below). Always `derived.last_message_chars` = full length. |
| `session_crons`, `background_tasks` | Stop, SubagentStop | Removed (items quote prompts and commands). `derived.session_crons` and `derived.background_tasks` = their counts. |
| `message` | Notification | Kept, cut to 300 characters. It is Claude Code's own UI text ("Claude needs your permission to use Bash"). |
| `custom_instructions` | PreCompact | Removed. `derived.custom_instructions_chars` = length. |

`derived.last_message`: take the message, drop trailing whitespace, keep the last 400
characters, and if that cut a line, drop the partial first line and prefix `…`. 400 is more
than a typical short excerpt on purpose: question detection needs the final paragraph and any
numbered options under it ([states.md](states.md) section 4). Push text uses only the
question line, cut to 120 characters.

`derived` fields for `hook_event`:

| Field | Type | Meaning |
| --- | --- | --- |
| `last_message` | string | See above. Present only when allowed. |
| `last_message_chars` | integer | Length of the full last assistant message. |
| `prompt_chars` | integer | Length of the prompt. |
| `question` | string | AskUserQuestion's first question, when allowed. |
| `custom_instructions_chars` | integer | Length of `/compact` instructions. |
| `error_chars` | integer | Length of a PostToolUseFailure error. |
| `permission_suggestions` | integer | Number of permission suggestions. |
| `session_crons`, `background_tasks` | integer | Number of items in the Stop/SubagentStop list of that name. |
| `remote_url` | string | Remote Control session URL, if `wy` found one in the transcript tail. The hub keeps it only when it starts with `https://claude.ai/` (the board renders it as a link) and drops anything else. |
| `peer_prompt` | boolean | UserPromptSubmit only, present only when `true`: a peer message started this turn. The prompt starts with `<cross-session-message`, or it came within 10 s of a delivery into the session ([peers.md](peers.md) 2.4). Only the prefix is compared; nothing of the prompt is kept. Set whether or not peers are enabled (Claude Code's own `/peers` uses the same wrapper). |
| `peer_wait` | boolean | Stop only, present only when `true` and `[peers] enabled`: the session waits on another session's reply ([peers.md](peers.md) 6.4). |
| `hook_ms` | integer | How long `wy hook` took until the outbox write returned. For the 20 ms budget. |

Example (Stop, last messages allowed):

```json
{
  "v": 1, "id": "01JA2XQ8R7Y5V0Z6M3K1T9B4CD", "kind": "hook_event",
  "ts": "2026-10-11T14:03:22.418Z", "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
  "slot": "~/.claude-2",
  "seat": {"account": "3f1c6a2e-…", "org": "9b20d4f1-…", "email": "a@example.com", "src": "claude_json"},
  "term": {"app": "ghostty"},
  "client": "wy/0.1.0",
  "redacted": ["last_assistant_message"],
  "derived": {"last_message": "Should I also update the migration?", "last_message_chars": 1874, "hook_ms": 6},
  "payload": {
    "hook_event_name": "Stop", "session_id": "c41e…", "transcript_path": "/home/me/.claude-2/projects/-home-me-api/c41e….jsonl",
    "cwd": "/home/me/api", "permission_mode": "default", "stop_hook_active": false,
    "prompt_id": "…", "effort": {"level": "high"}, "scratchpad_dir": "/tmp/…"
  }
}
```

### 4.2 `limit_sample`

Written by `wy statusline` when the 5-hour or 7-day value changes, at most once a minute per
session, and always on the first status-line run of a session. A value that reaches 80, 95 or
100 percent from below the last written one is written at once, inside the minute too, so
the hub's limit alerts (push.md) do not wait for the session's next status line.

- `--wrap <cmd>` runs the user's original command (same stdin) through `sh -c` when `sh` is
  on PATH (Claude Code itself uses a POSIX shell, Git Bash on Windows), else `cmd /c`. Its
  output is printed first with all trailing newlines trimmed, then ` │ ` and our part. A
  non-zero exit, or a background child that keeps stdout open (`exec.ErrWaitDelay`), still
  counts as success and the buffer is kept. After 2 s the command is dropped and the last
  good output of that session (`<state>/statusline/wrapped-<session>.txt`) is shown instead.
  A bad flag still runs the `--wrap` command found in the arguments and prints a line; the
  flag error is only returned afterwards.
- `payload` is the status-line JSON from stdin, kept whole. It holds no content fields.
- The hub reads these paths (names as of Claude Code 2.1.296; the hub tolerates each being
  missing):

| Path | Type | Use |
| --- | --- | --- |
| `session_id` | string | Session the sample came from. |
| `rate_limits.five_hour.used_percentage` | number | 5-hour window used %. |
| `rate_limits.five_hour.resets_at` | string or number | Reset time. **Uncertain:** may be epoch seconds; the hub accepts both. |
| `rate_limits.seven_day.used_percentage`, `.resets_at` | | 7-day window. |
| `context_window.used_percentage` | number | Context %. **Uncertain** name; older builds only had `exceeds_200k_tokens`. |
| `context_window.context_window_size` | integer | Context size. |
| `model.id`, `model.display_name` | string | Model. |
| `session_name` | string | Session name, if set. |
| `cost.total_cost_usd` | number | Claude Code's own cost estimate. Stored, not used for analytics. |

`derived` for `limit_sample` (computed by `wy` so the outbox can be read locally):

| Field | Type |
| --- | --- |
| `five_hour_pct`, `seven_day_pct` | number or null |
| `five_hour_resets_at`, `seven_day_resets_at` | RFC 3339 string or null |
| `context_pct` | number or null |

Example:

```json
{
  "v": 1, "id": "01JA2XRB3T0M2N4P6Q8R0S2T4V", "kind": "limit_sample",
  "ts": "2026-10-11T14:03:30.002Z", "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
  "slot": "~/.claude-2", "seat": {"account": "3f1c…", "org": "9b20…"},
  "client": "wy/0.1.0",
  "derived": {"five_hour_pct": 42, "five_hour_resets_at": "2026-10-11T17:00:00.000Z", "seven_day_pct": 18, "seven_day_resets_at": "2026-10-15T18:00:00.000Z", "context_pct": 37},
  "payload": {"session_id": "c41e…", "model": {"id": "claude-opus-5", "display_name": "Opus"},
              "rate_limits": {"five_hour": {"used_percentage": 42, "resets_at": 1760202000}, "seven_day": {"used_percentage": 18, "resets_at": 1760551200}},
              "context_window": {"used_percentage": 37, "context_window_size": 200000}, "cwd": "/home/me/api", "version": "2.1.296"}
}
```

### 4.3 `web_usage`

Written by the desktop app every 5 minutes per signed-in tab, and when a tab opens.
One record per organisation the app reports for the account, so each record names one seat.
The app leaves out a free organisation when the account has a paid one (desktop.md C5, "Which
orgs"), because every account has a personal free organisation and it is not a seat of its own.

- No `slot`. `seat` is required (`src: "claude_ai"`).
- `client` is `watchyard-desktop/<app version>` (desktop.md C1). The app does not post to
  `/v1/ingest` itself: it pipes the records to `wy outbox append`, which sets `id` and `v`,
  and `wy flush` uploads them with the machine's own token (desktop.md C5). `machine` is the
  id from `wy machine show --json`, and `ts` is the start time of the read.
- `payload.usage` is the raw `GET /api/organizations/{uuid}/usage` response, **kept whole**.
  Today it holds `five_hour`, `seven_day`, `seven_day_oauth_apps`, `seven_day_opus`,
  `seven_day_sonnet`, `seven_day_cowork`, `seven_day_omelette`, `tangelo`,
  `iguana_necktie`, `omelette_promotional`, `nimbus_quill`, `cinder_cove`, `copper_kite`,
  `brass_thimble`, `harbor_lantern`, `wattle_ember`, `amber_ladder`, `amber_cistern`,
  `juniper_tide`, `cedar_ember`, `amber_gauge`, `extra_usage`, `limits`, `spend`,
  `member_dashboard_available`, `seven_day_breakdown`, `weekly_scoped_shares`. Any of them
  may be null. Any new key is kept.
- **How the hub reads the windows** (internal/hub/usage `WindowsOf`; each window becomes a
  `limit_sample`, source `web`):
  - Every top-level key whose value is an object with a non-null `utilization` is a window of
    that key (`resets_at` as given, possibly null).
  - `limits[]` items with `scope.model.display_name` are model-scoped windows keyed
    lower(`group` or `kind`) + `_` + lower(model) (`weekly_fable`), `percent` as used.
  - Unscoped `limits[]` items (no model, no surface) of a known kind fill a window when its
    top-level field is null or missing: `session` → `five_hour`, `weekly_all` → `seven_day`
    (`percent`, `resets_at`). When both carry the window the top-level one is kept (its
    `utilization` is unrounded; the two agree in every reading seen). Other unscoped kinds are
    not read. `is_active` is not read as "started": a `weekly_all` item reads
    `is_active: false` with 24 % used, so it says nothing the hub can use.
  - A window with `utilization` (or `percent`) 0 and `resets_at` null is **idle**: claude.ai's
    "not started" (the account has not been used since its last window ended; the window starts
    with the next message). It is a real, current reading (api.yaml `Window.state` `idle`):
    0 % used, no reset time. Seen on idle accounts: `five_hour: {utilization: 0, resets_at:
    null}` with `seven_day: null` and only a model-scoped weekly item in `limits[]`. The hub
    never invents a reset time, nor a `seven_day` window from a model-scoped one.
  - A window with a `resets_at` folds into its `usage_window` (store `AddSample`); an idle
    reading is kept in `limit_sample` with `resets_at` NULL and makes no `usage_window`. The
    current views (`/v1/accounts`, `/v1/overview`, the `usage` event, "who has room", push.md
    5.7) show an idle window in place of a key's newest `usage_window` when the newest idle
    claude.ai reading of that key is newer than it (or the key has no window). A status-line
    sample without a reset time is not read as idle.
- The section 1 rule 4 key filter is **not** applied to `payload.usage`: its keys are
  usage counters, and a filter could drop a future counter whose name contains "token".
  The desktop app builds `payload` from the three usage endpoints only, which carry no
  credentials.

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `tab_id` | string | yes | The desktop app's tab id (web seat). Pattern `^(t_[0-9a-f]{16}\|[0-9a-f]{8})$`: `t_` + 16 lowercase hex for a tab made by the desktop app, or an 8-hex tab id from earlier versions, kept unchanged so its web seat continues (desktop.md C1). |
| `org` | object | yes | From `/api/organizations`: `uuid`, `name`, `capabilities`, `rate_limit_tier`, `billing_type`, `raven_type`, kept as returned for those keys. |
| `account` | object | no | From `/api/account`: `uuid`, `email_address`, `full_name`, `display_name`. Only these keys. |
| `active` | boolean | no | True if this org is the tab's last active org. |
| `usage` | object | yes, unless `error` | The raw usage response. |
| `error` | object | no | `{status, message}` when the usage call failed (401, 403, 429…). Recorded so the hub can show "stopped reading". |
| `fetch_ms` | integer | no | Time the three calls took. |

Example:

```json
{
  "v": 1, "id": "1b4e28ba-2fa1-11d2-883f-0016d3cca427", "kind": "web_usage",
  "ts": "2026-10-11T14:05:00.120Z", "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
  "seat": {"account": "3f1c…", "org": "9b20…", "email": "a@example.com", "src": "claude_ai"},
  "client": "watchyard-desktop/0.6.0",
  "payload": {
    "tab_id": "t_9c1e5f0a7b2d4e68", "active": true,
    "org": {"uuid": "9b20…", "name": "a@example.com's Organization", "capabilities": ["chat", "claude_max"], "rate_limit_tier": "default_claude_max_20x", "billing_type": "stripe_subscription", "raven_type": null},
    "account": {"uuid": "3f1c…", "email_address": "a@example.com"},
    "usage": {
      "five_hour": {"utilization": 17, "resets_at": "2026-10-12T17:30:00.186305+00:00", "limit_dollars": null, "used_dollars": null, "remaining_dollars": null, "locked_reason": null},
      "seven_day": null, "iguana_necktie": null, "tangelo": null,
      "extra_usage": {"is_enabled": false, "monthly_limit": null, "used_credits": null},
      "limits": [{"kind": "weekly_scoped", "group": "weekly", "percent": 0, "severity": "normal", "resets_at": "2026-10-15T18:00:00+00:00", "scope": {"model": {"id": null, "display_name": "Fable"}, "surface": null}, "is_active": false}],
      "spend": {"used": {"amount_minor": 0, "currency": "USD", "exponent": 2}, "percent": 0},
      "seven_day_breakdown": null
    }
  }
}
```

(The example is shortened with fewer keys than a real response; a real record carries
every key.)

### 4.4 `token_rows`

Written by `wy scan` after each Stop for that session (debounced) and on the timer. One API
response per row, keyed by the API message id, one row per message id.
A response can appear in several records with growing counts (streaming snapshots); the hub
merges rows by keeping the largest value of each counter (a token upsert).

- `slot` is set when the scanned transcripts folder belongs to one slot; omitted when
  `projects/` is shared. `seat` is never set: rows are attributed through their session.
- At most 2000 rows per record.

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `rows` | array | yes | See below. May be empty if only `limit_hits` or `sessions` are sent. |
| `limit_hits` | array | no | "You've hit your limit" replies: `{session_id, ts, type, status, resets_at}` (`type` = `quotaLimits.rateLimitType`). |
| `sessions` | array | no | Per-session facts found in transcripts: `{session_id, owner_account, owner_org, remote_url, title}`. `owner_*` come from `bridge-session` lines (Remote Control). A fact for a session the hub does not know makes its `session` row; with no hook event or list it is history (`ended:history`, times and project from the rows, [states.md](states.md) 5.1). Rows alone never make a session. |
| `source` | object | no | `{path, from, to}`: transcript file (with `~`) and byte range scanned. Debugging only. |

Row fields (all counts are integers ≥ 0):

| Field | Required | Source in the transcript line |
| --- | --- | --- |
| `key` | yes | `message.id`, else `requestId`, else `uuid` |
| `ts` | yes | `timestamp` (RFC 3339) |
| `session_id` | yes | `sessionId` |
| `cwd` | no | `cwd` (the project), written with `~` for the home folder and `/` as separator, like `slot` (a path outside the home folder stays absolute) |
| `model` | no | `message.model` |
| `input` | yes | `usage.input_tokens` |
| `output` | yes | `usage.output_tokens` |
| `cache_5m` | yes | `usage.cache_creation.ephemeral_5m_input_tokens`, else `cache_creation_input_tokens` |
| `cache_1h` | yes | `usage.cache_creation.ephemeral_1h_input_tokens` |
| `cache_read` | yes | `usage.cache_read_input_tokens` |
| `thinking` | no | `usage.output_tokens_details.thinking_tokens` |
| `sidechain` | no | `isSidechain` (boolean) |
| `agent_id` | no | `agentId` for subagent transcripts |

Example:

```json
{
  "v": 1, "id": "01JA2XT0Q1R2S3T4V5W6X7Y8Z9", "kind": "token_rows",
  "ts": "2026-10-11T14:03:25.000Z", "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
  "client": "wy/0.1.0",
  "payload": {
    "rows": [{"key": "msg_01ABC…", "ts": "2026-10-11T14:03:20.551Z", "session_id": "c41e…", "cwd": "/home/me/api",
              "model": "claude-opus-5", "input": 12, "output": 811, "cache_5m": 0, "cache_1h": 4210, "cache_read": 88123, "thinking": 120, "sidechain": false}],
    "limit_hits": [],
    "sessions": [{"session_id": "c41e…", "remote_url": "https://claude.ai/code/session_…"}],
    "source": {"path": "~/.claude-2/projects/-home-me-api/c41e….jsonl", "from": 1048576, "to": 1052211}
  }
}
```

### 4.5 `session_list`

Written by `wy agents` on the timer (every 5 minutes, see [states.md](states.md) section 5),
once per slot. It is the input for `gone` detection.

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `complete` | boolean | yes | True when the list is every live session of this slot. Only complete lists can mark a session gone. |
| `source` | string | yes | `claude_agents` (`claude agents --json` run with `CLAUDE_CONFIG_DIR` = the slot), or `sessions_dir` (reading `<slot>/sessions/*.json`, the fallback). |
| `sessions` | array | yes | One item per session, raw item **kept whole** as the source wrote it. |
| `error` | string | no | Why the list is incomplete. |

Items from `claude agents --json` (Claude Code 2.1.296) have `cwd`, `kind`
(`interactive`, `background`), `startedAt` (epoch ms), `sessionId`, `name`, and either
`pid` + `status` (`interactive`: `busy`, `idle`; `waiting` not yet seen, **uncertain**) or
`id` (8 hex) + `state` (`background`: `blocked` seen; no `pid`, no `status`). The fixtures are
in `testdata/agents/`.

`complete` is true only when `claude agents --json` exited 0 and printed a JSON array for
every run `wy` made for the slot. `~/.claude` is run twice, with `CLAUDE_CONFIG_DIR` unset
and set to `~/.claude`, because the two views differ (a background session started with
the variable set appears only in the second); the lists are merged by `sessionId`. The
`sessions_dir` fallback is never complete: it has no background sessions and can hold files
of processes that died without cleaning up. Items from `sessions/*.json` additionally carry `bridgeSessionId`,
`entrypoint`, `statusUpdatedAt`, `version` and more; `wy` drops `messagingSocketPath`
and never reads the `*.key` files next to them.

Example:

```json
{
  "v": 1, "id": "01JA2XV9…", "kind": "session_list", "ts": "2026-10-11T14:05:00.000Z",
  "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", "slot": "~/.claude-2", "seat": {"account": "3f1c…", "org": "9b20…"},
  "client": "wy/0.1.0",
  "payload": {"complete": true, "source": "claude_agents",
    "sessions": [{"pid": 3861972, "cwd": "/home/me/api", "kind": "interactive", "startedAt": 1760190000000, "sessionId": "c41e…", "name": "api", "status": "idle"}]}
}
```

### 4.6 `history_import`

Written once per slot by the first `wy scan`, so the hub has totals from before Watchyard.

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `source` | string | yes | `stats_cache` (`<slot>/stats-cache.json`). Other sources may be added. |
| `raw` | object | yes | The file, kept whole: `version`, `lastComputedDate`, `dailyActivity[]` (`date`, `messageCount`, `sessionCount`, `toolCallCount`), `dailyModelTokens[]` (`date`, `tokensByModel`), `modelUsage`, `totalSessions`, `totalMessages`, `longestSession`, `firstSessionDate`, `hourCounts`. |
| `part`, `parts` | integer | no | When split for size: this is part `part` of `parts` (1-based). |

The hub turns `dailyModelTokens` into `history_day` rows for that slot's seat (as known at
`ts`). These are only used for days with no `token_row` data.

### 4.7 `slot_info`

Written by `wy` on `wy connect`, on SessionStart when the login, label or colour it reads
differs from the last one it sent for that slot, on any other hook event when the login
differs (a `/login` inside a session that goes on), and once a day per slot.

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `folder` | string | yes | Same as `slot`. |
| `real_path` | string | no | Resolved path with `~` (symlinks followed). |
| `default` | boolean | yes | True for `~/.claude` (its state file is `~/.claude.json`). |
| `launcher` | string | no | Launcher name on PATH (`claude-2`, `claude-work`), if known. |
| `logged_in` | boolean | yes | From `auth_status.loggedIn`, else from `oauth_account` presence. |
| `oauth_account` | object | no | The `oauthAccount` object from the slot's `.claude.json`, kept whole. It holds no tokens (keys today: `accountUuid`, `emailAddress`, `organizationUuid`, `organizationName`, `organizationRole`, `organizationType`, `organizationRateLimitTier`, `userRateLimitTier`, `seatTier`, `billingType`, `displayName`, `fullName`, `hasExtraUsageEnabled`, `workspaceRole`, `accountCreatedAt`, `subscriptionCreatedAt`, `profileFetchedAt`, `ccOnboardingFlags`, `claudeCodeTrialDurationDays`, `claudeCodeTrialEndsAt`). |
| `auth_status` | object | no | Output of `claude auth status --json`, kept whole after the rule 4 filter (keys today: `loggedIn`, `authMethod`, `apiProvider`, `analyticsDisabled`, `projectsDirectory`, `configDirectory`, `email`, `orgId`, `orgName`, `subscriptionType`). |
| `claude_version` | string | no | `claude --version`. |
| `sharing` | object | no | Which parts are symlinked to which target, for the Accounts screen: `{"settings.json": "~/.claude/settings.json", ...}`. |
| `label` | string | no | Local label for this folder's login (`slots.labels`, set by `wy setup` / `wy account label`). The hub applies it to the seat only while the seat has no label set in the UI (setup.md 8.4). |
| `colour` | string | no | `#rrggbb`, same rule as `label`. |

### 4.8 `machine_info`

Written by `wy tick` when the content changed (counters and times excluded) and otherwise
every 6 hours, and by `wy connect`, `wy setup`, `wy update` and `wy machine rename`
(machines.md 6.3). No `slot`, no `seat`. The hub copies it into the `machine` row (`os`,
`arch`, `reach`, `record_v`, `held`, `backlog_oldest`, `info`, `info_at`) when it is newer than
the stored `info_at`. It also sets `machine.name` to `name` when `name` is a valid machine
name (1–60 characters, no control characters) and differs from the `name` of the stored
`info` it replaces; a first report, a repeat, or a report after one without `name` leaves
the name alone, so a rename on the board is kept until the machine is renamed again.

`name` was added later without a new `v`: it is optional, so `v` stays `1` (a new
`v` is only for a change an older hub would read wrongly, 3). A hub that does not know the
field keeps it in `info` only (payloads keep unknown fields, 6), and an older `wy` that
leaves it out never renames the machine.

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `os`, `arch` | string | yes | `runtime.GOOS`, `runtime.GOARCH`. |
| `wy` | string | yes | `wy` version. |
| `record_v` | integer | yes | Highest record `v` this `wy` writes. |
| `reach` | string | yes | `direct`, `forward` or `pull` (`hub.reach`); `none` without a hub. |
| `dial` | string | no | `tcp` or `unix` when `hub.dial` is set (never the address itself). |
| `outbox` | object | yes | `{backlog, unflushed_bytes, oldest_unflushed, held, rejected_total, corrupt_total, cap_dropped_total}`; `oldest_unflushed` RFC 3339 or null. |
| `direct_push` | object | yes | `{enabled, server_host, private, policy_at}`; never the topic or token. |
| `timer` | object | no | `{active, linger}` as `wy doctor` sees them (`linger` Linux only). |
| `claude_version` | string | no | `claude --version` on the login PATH. |
| `update` | object | no | `{last_check, latest}` from `wy update --check`, when it ran. |
| `name` | string | no | The machine's own name (`<state>/machine.json`, identity.md 2; `wy machine rename`). |

### 4.9 `direct_push`

Written by `wy` right after it sent a session push itself (machines.md 5.4, 5.5). No `seat`;
`slot` is the session's slot when known.

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `episode` | string | yes | push.md 3.2 session key, `s:<session>:<state>:<since_at ms>`. |
| `trigger` | string | yes | `needs_permission`, `asked` or `failed`. |
| `session_id` | string | yes | The session (same pattern as `hook_event`'s). |
| `since` | string | yes | RFC 3339 `ts` of the record that entered the state. |
| `record` | string | yes | Id of that record. |
| `sent_at` | string | yes | When the push was sent. |
| `channel` | string | yes | `ntfy`. |
| `server_host` | string | yes | Host name of the ntfy server (not the topic). |
| `ok` | boolean | yes | The ntfy server answered 200. |
| `status` | integer | no | HTTP status when not ok. |
| `error` | string | no | `timeout`, `connection failed`, … (push.md 7.3's reduced forms). |
| `private` | boolean | no | Private text was used. |

## 5. Validation on the hub

The hub rejects a record (and only that record) when:

| Reason | When |
| --- | --- |
| `bad_id` | `id` missing or not matching `^[0-9A-Za-z_-]{16,64}$`. |
| `bad_envelope` | `kind`, `ts` or `payload` missing or of the wrong type, or `ts` not RFC 3339; a `hook_event` whose `payload.session_id` is not `^[0-9A-Za-z][0-9A-Za-z_-]{0,63}$` (the hub builds a shell resume command from it). |
| `machine_mismatch` | `machine` differs from the machine bound to the device token. When every record of a batch is rejected for this reason the whole request is answered `403` (`code: machine_mismatch`) and the client keeps its outbox. |
| `id_conflict` | Same `id` already stored from another device, unless both devices are bound to the record's machine (3.1). |
| `too_large` | Encoded record over 1 MiB. |

Everything else is accepted and stored raw, including records with an unknown `kind`, a
higher `v`, or typed fields the hub cannot parse (those get `typed = 0` and an
`type_error` note in the `record` row, and are retried on replay).

Only a device bound to a machine may write records: a `write` token with no machine id gets
`403` (`code: machine_mismatch`) for the whole batch and nothing is stored.

### 5.1 Sessions belong to one machine

A session belongs to the machine whose record first named it (`session.machine_id`). A
record never changes another machine's session, so a stolen machine or desktop token cannot
end, rename, re-account or alert on sessions of other machines:

- A `hook_event` for another machine's session is stored but not applied: `typed = 0` with
  `type_error` "session belongs to another machine". No hook event row, no fold, no alert.
- A `session_list` item, `token_rows` session fact, row or limit hit, or a `limit_sample`
  session patch for another machine's session is skipped; the rest of the record applies.
- A `direct_push` for another machine's session is stored and ignored (4.9).

Each skip is logged by the hub (`ingest: <kind> record=<id> from machine <m> names session
<s> of machine <owner>: not applied`). A session the hub has with no machine yet is taken by
the first machine that names it.

## 6. Round trip

For every fixture in `testdata/`, the record built from it, encoded and decoded again, must
equal the input field for field. In Go this means the record and
payload types keep unknown fields (`json.RawMessage` or a `map[string]any` overflow), never
plain structs that drop them.
