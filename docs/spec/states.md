# Session states

Status: spec v1. Implemented by the hub's state engine (`internal/hub/state`)
and, for the local view, by `wy status` reading the outbox (same code, same rules).

The engine folds a session's records, in `ts` order, into one current state. It is a pure
function of the records: replaying the `record` table must give the same `session_state`
rows. It never calls out, never sends anything to a client.

For that to hold, `session_state.updated_at` is the `ts` of the newest record folded in, not
the hub's clock. The hub folds each new record on top of the stored state; a record whose
`ts` is not after `updated_at` (it arrived late) makes the hub refold that session from its
records in the `record` table. Each step's state goes to `hook_event.state_after`, and each
change to `session_transition` (schema v2).

## 1. States

| State | Meaning | `needs_you` |
| --- | --- | --- |
| `working` | Claude is running a turn. | 0 |
| `needs_permission` | Claude is blocked on a permission prompt. | 1 |
| `asked` | Claude is waiting for an answer: a dialog, AskUserQuestion, or a turn that ended with a plain-text question. | 1 |
| `done` | Idle at the prompt, nothing pending. Also a freshly started session. | 0 |
| `failed` | The turn stopped with an error (rate limit, login, billing, overload…). | 1, except `detail` = `overloaded` / `server_error` → 0 |
| `ended` | SessionEnd was received, or (`detail` = `history`) the session is known only from its transcript (section 5.1). | 0 |
| `gone` | The session vanished without SessionEnd (crash, `kill`, machine reboot). | 0 |

`session_state.detail` holds a short machine-readable reason: the notification type, the
StopFailure error, the SessionEnd reason, or one of `started`, `resumed`, `question`,
`elicitation`, `tool` (AskUserQuestion / ExitPlanMode), `permission_request` (a
PermissionRequest event), `repaired`, `gone`, `history` (section 5.1). A session with no record yet is `done` with an
empty detail; a first record that changes nothing (a `compact` SessionStart) leaves it
there. A session brought back from `ended`/`gone` by a `compact` SessionStart gets
`done:compact`.

**Design decision:** a naive design would enter `working` at SessionStart. A new session
sits at an empty prompt, so showing it as "working" would be wrong on the board. Here
SessionStart enters `done` with `detail = started` (shown as "new" in the UI), except
`source = compact`, which keeps the current state.

## 2. Hook events installed by the plugin

The default keeps PreToolUse / PostToolUse off by default (one process per tool call). The
plugin (`plugin/watchyard/hooks/hooks.json`) installs these command hooks, all
`async: true` except SessionEnd, all running `wy hook`. SessionEnd is synchronous with
`timeout: 2` (it costs about 5 ms): an async hook can be killed when Claude Code exits, and
the session would then show as `gone` instead of `ended`. Beside these, synchronous entries
that record nothing: `wy hook --title <Event>` (terminal.md T6) and, on SessionStart only,
`wy hook --peers-context SessionStart` (peers.md 2.5).

| Event | Matcher | Why |
| --- | --- | --- |
| SessionStart | (all) | New session, seat, slot. |
| UserPromptSubmit | — | Back to `working`; cancels pending alerts. |
| PermissionRequest | (all) | `needs_permission`. |
| Notification | (all) | Permission prompts, idle prompts, elicitation dialogs. |
| PreToolUse | `AskUserQuestion\|ExitPlanMode` only | `asked`. One process only when these tools run, so it does not break that default. |
| Stop | — | `done` or `asked`. Also triggers `wy scan` for the session. |
| StopFailure | — | `failed`. |
| SubagentStop | — | Recorded only (and triggers a scan). |
| SessionEnd | — | `ended`. |
| PreCompact | — | Recorded only. |
| PermissionDenied | — | Leaves `needs_permission` after a "no". |

`wy hook` accepts **any** event name (Claude Code adds events over time) and records it; the
engine ignores events it has no row for below. A user can opt in to PreToolUse /
PostToolUse for all tools (`config.toml: hooks.tool_events = true`), which makes
`needs_permission` clear at the next tool event instead of at the next prompt or Stop.

## 3. Transition table

Read: on event (row), from current state (column), go to the cell's state. `=` means no
change (the event is still stored in `hook_event` and updates `session.last_event_at`).
`·` (from `ended`/`gone`) means the session comes back: it is reopened, `ended_at` and
`end_reason` are cleared, then the cell's rule for a `done` session applies.

The current state of a session with no state yet is treated as `done`.

| Event (subtype) | working | needs_permission | asked | done | failed | ended / gone |
| --- | --- | --- | --- | --- | --- | --- |
| SessionStart (`startup`, `clear`) | done:started | done:started | done:started | done:started | done:started | · done:started |
| SessionStart (`resume`) | done:resumed | done:resumed | done:resumed | done:resumed | done:resumed | · done:resumed |
| SessionStart (`compact`) | = | = | = | = | = | · = (then `done`) |
| UserPromptSubmit | working | working | working | working | working | · working |
| PreToolUse (`AskUserQuestion`, `ExitPlanMode`) | asked:tool | asked:tool | = | asked:tool | asked:tool | · asked:tool |
| PreToolUse (other tool, opt-in) | = | working | working | working | working | · working |
| PostToolUse, PostToolUseFailure (opt-in) | = | working | working | working | working | · working |
| PermissionRequest | needs_permission | = | needs_permission | needs_permission | needs_permission | · needs_permission |
| Notification `permission_prompt` | needs_permission | = | needs_permission | needs_permission | needs_permission | · needs_permission |
| Notification `elicitation_dialog` | asked:elicitation | asked:elicitation | = | asked:elicitation | asked:elicitation | · asked:elicitation |
| Notification `idle_prompt` | done:repaired | = | = | = | = | = |
| Notification `auth_success`, other / unknown type | = | = | = | = | = | = |
| Elicitation (MCP, if Claude Code sends it as its own event) | asked:elicitation | asked:elicitation | = | asked:elicitation | asked:elicitation | · asked:elicitation |
| Stop, last message is a question (section 4) | asked:question | asked:question | asked:question | asked:question | asked:question | · asked:question |
| Stop, otherwise | done | done | done | done | done | · done |
| Stop with `stop_hook_active = true` | = | = | = | = | = | = |
| StopFailure (`error` = E) | failed:E | failed:E | failed:E | failed:E | failed:E | · failed:E |
| PermissionDenied | = | working | = | = | = | = |
| SubagentStart, SubagentStop | = | = | = | = | = | = |
| PreCompact, PostCompact | = | = | = | = | = | = |
| UserPromptExpansion, TaskCreated, TaskCompleted, Setup, ConfigChange, InstructionsLoaded | = | = | = | = | = | = |
| SessionEnd (`reason` = R) | ended:R | ended:R | ended:R | ended:R | ended:R | = |
| Any other event name | = | = | = | = | = | = |
| `session_list` lists it as `busy` | = | = | = | working:repaired | = | · working |
| `session_list` lists it as `idle` | done:repaired | = | = | = | = | · done |
| `session_list` lists it as `waiting` | = | = | = | = | = | · = |
| `session_list` (complete) does not list it | see section 5 | | | | | = |
| transcript activity (`token_rows`, section 5.1) | = | = | = | = | = | = (`ended:history`: ended_at moves on) |

A session with **no state yet** (no hook event and no list ever folded) goes to
`ended:history` on transcript activity; see section 5.1.

Notes on the table:

- **History and `since`.** The state history gets an entry whenever `(state, detail)`
  changes. `since` moves only when the *state* changes, so a second `permission_prompt`, or
  a new `StopFailure` error while already `failed`, does not reset it.
- **Ordering.** Records are applied in `(ts, id)` order per session. A late record (older
  `ts` than the last applied one, e.g. flushed from a slow machine) is stored and, if it
  would change the state, the engine refolds that session from its records. Sessions are
  small (hundreds of events), so refolding is cheap.
- **Clock skew.** The `ts` used for ordering is the record's `ts`, except that a `ts` more
  than 5 minutes after the hub's receive time is clamped to the receive time (record.md
  1.6). Without this one record from a clock that ran a day ahead would sort after every
  real event for a day and freeze the session. The raw `ts` stays in the stored record.
- **`needs_permission` leaves** at the next UserPromptSubmit, Stop, StopFailure,
  SessionEnd, or (if opted in) tool event. A user who answers "yes" keeps the session in
  `needs_permission` until the turn ends, unless tool events are on. That is the trade-off
  of that default. The board shows "since" so a long-stale permission is visible.
- **PermissionDenied** (seen in the 2.1.296 event list, not captured) means the prompt was
  answered "no", and Claude continues the turn, so `needs_permission` → `working`.
- **Interrupts.** **Uncertain:** pressing Esc to interrupt a turn is not known to fire Stop.
  (PostToolUseFailure carries `is_interrupt`, which helps only when tool events are on.)
  The session then stays `working` (or `needs_permission`) until the next prompt, or until
  a `session_list` reports it `idle` (repair rows above), or an `idle_prompt` arrives.
- **`idle` repair** never clears `needs_permission` or `asked`, because an idle prompt
  showing a permission dialog or a question is exactly what those states mean.
- **`since` of a list repair to `done`.** A list only says the session is idle *now*, not
  since when. When a list makes a session `done` (`idle` repair of `working`, a session with
  no state yet, or `ended:history` coming back), `since` is the session's newest sign of
  life at or before the list's `ts`: the newest of its newest transcript row (`token_row`),
  the `startedAt` the list item reports, its `last_event_at` and its current `since`;
  the list's `ts` only when none is known. So an idle process from last week, first listed
  when `wy` is set up (or listed on a slot without hooks), reads "done for 7 days", never
  "done since the list (or the import) ran". A session that ended or went `gone` and is
  listed again comes back at the list's `ts`; `busy` repairs use the list's `ts`. When a
  transcript row that is newer than `since` but not newer than that list arrives later
  (the first list on a machine usually beats the import of its transcripts), the engine
  refolds the session, so arrival order does not change the result. `wy hub refold`
  applies the rule to sessions folded before it.
- **SessionEnd then SessionStart with the same id** (`claude --resume`): the session comes
  back (`·` column). **Uncertain** whether resume keeps the id in 2.1.296; both cases work.
- **`/clear`** ends the old session id (SessionEnd `clear`) and starts a new one
  (SessionStart `clear`). Two sessions; nothing special.
- **StopFailure `error` values.** Captured: `model_not_found` (`testdata/hooks/by-event/StopFailure.json`).
  Others (**uncertain**, from the hooks docs as remembered): `rate_limit`,
  `authentication_failed`, `billing_error`, `invalid_request`, `server_error`,
  `max_output_tokens`, `unknown`. StopFailure is sent instead of Stop, not after it. The hub keeps the raw string. `needs_you`
  is 0 only for `server_error` and `overloaded` (Claude Code retries those itself).
  Unknown values count as needs-you.
- **SessionEnd `reason` values:** `clear`, `logout`, `prompt_input_exit`,
  `bypass_permissions_disabled`, `other`. Stored as `detail`.
- **`asked:tool`** for `ExitPlanMode` means "plan ready for approval". Shown as "plan ready".

### 3.1 Coordination between sessions (`peers`)

Beside the state, the fold keeps `session_state.peers` (schema v13): whether the current turn
is sessions coordinating through peers ([peers.md](peers.md) 6.4). It never changes the state,
`detail`, `since` or `needs_you`, so the board shows such a session exactly as before; only
the alert rules read it ([push.md](push.md) 3.5).

| Event | `peers` after it |
| --- | --- |
| UserPromptSubmit with `derived.peer_prompt` (a peer message started the turn) | `message` |
| UserPromptSubmit without it (a person) | none |
| Stop (not `stop_hook_active`) with `derived.peer_wait`, when `peers` is none | `wait` |
| SessionStart `startup`, `clear`, `resume` | none |
| Anything else (SessionStart `compact` included) | unchanged |

So a turn a peer message started stays `message` to its end, a turn a person started becomes
`wait` only when it ends waiting on a reply, and the next prompt a person types clears it. The
`derived` flags come from `wy hook` ([record.md](record.md) 4.1); the same fold runs in
`wy status` and the machine's direct push.

## 4. "Reads as a question" (detector `q2`)

Input: `derived.last_message` of a Stop record (the last ≤ 400 characters, record.md 4.1). If
it is absent (last messages not allowed), the result is always "not a question" and the
session goes to `done`; pushes for plain-text questions then need last messages on.

Steps, applied to the text:

1. Remove fenced code blocks (from a line starting with three backticks to the matching
   closing line, or to the end if unclosed) and inline code spans (`` `…` ``).
2. Trim. Split into paragraphs on blank lines. Take the **last paragraph** P.
3. Strip trailing characters from P that are any of: whitespace, `*`, `_`, `)`, `]`, `"`,
   `'`, `”`, `’`, `` ` ``, and emoji in the Unicode "So" category.
4. P is a question if **any** of these hold:
   - a. P ends with `?` (ASCII) or `？` (full width).
   - b. P contains at least two option lines, i.e. lines matching
     `^\s*(\d{1,2}|[A-Ha-h])[.)]\s+\S` or `^\s*[-*]\s+\*\*(Option|Choice)\b` (case
     insensitive), **and** the text before the first option line (in P or in the paragraph
     just before P) ends with `?`, or ends with `:` and its last line contains a choice word
     (whole words, case insensitive: `which`, `choose`, `choice(s)`, `pick`, `prefer`,
     `select`, `option(s)`, `decide`, `would you`, `do you want`, `want me to`, `should i`,
     `shall i`, `let me know`, `your call`). So "Pick one:" before a numbered list is a
     question, while "Three agents are running now:" or "Here is what I changed:" is a report.
   - c. The last sentence of P (P with trailing `.`, `!` and whitespace removed, then the
     text after the last `.`, `!` or newline; leading `*`, `_`, `>`, `-`, `#` and whitespace
     ignored, so `**Should I continue**` matches)
     starts with, case insensitive: `should i`, `shall i`, `do you want`, `would you like`,
     `want me to`, `which `, `can you confirm`, `let me know if`, `let me know which`.
5. Exclusions (checked after step 4; any one makes it "not a question"):
   - the `?` in 4a is part of a URL (the last whitespace-separated token of P matches
     `https?://\S+`; 4a then does not hold, 4b and 4c are still tried);
   - P (after step 3) is a single line shorter than 3 characters.
   - Known limits, kept on purpose: a rhetorical question that ends the message, and a list
     introduced by `:` whose lead-in has a choice word but is not really asking, both count
     as a question.

The question text stored in `session_state.question` is the last line of P that ends with
`?` (for 4b, the line before the first option; for 4c, the last line of P), with leading
`*`, `_`, `>` and spaces removed, cut to 120 characters. Option lines (4b) are matched on P
before step 3, so a trailing `)` or `*` cannot hide one.

The detector version (`q2`; `q1` treated any `:` lead-in in 4b as a question) is stored in
`session_state.detector`. A later version can be introduced on the hub and replayed over history without touching any client. The tests ship a table of example messages with the expected result.

## 5. `gone` and timeouts

A session can only become `gone` through complete session lists, never through silence
alone, because a quiet session (a long build, a person reading) is normal.

- `wy agents` runs on the machine's timer every **5 minutes** and sends one `session_list`
  per slot. With `complete = true` it lists every live session of that slot. The record's
  `ts` is the time that slot's `claude agents` run **started** (not when collection ended),
  because the list describes the sessions as they were then.
- A list whose `ts` is older than the session's `last_event_at` (a snapshot taken before a
  hook event but applied after it) does not repair `busy`/`idle`: the hook event wins.
- The hub keeps, per session, `last_listed_at` (newest complete list of its slot that listed
  it) and `missed_lists` (complete lists of its slot since then that did not list it, and
  whose `ts` is at least 60 seconds after the session's `last_event_at`). "Its slot" is the
  slot the session is in now: a session resumed in another folder moves to that slot, both
  counters start over, and the old slot's lists no longer count (identity.md 5.3).
- A session that is not `ended` becomes **`gone`** when `missed_lists >= 2`. `end_reason`
  = `gone`, `ended_at` = `ts` of the second missing list.
- Any later event or list entry for it brings it back (`·` column).
- **Never listed sessions.** A session seen only through hooks, on a slot that has never
  sent a complete list (an old `wy`, or `claude agents` failing), is never marked `gone`.
  If its last event is older than **12 hours** and it is `working`, the read API reports it
  with `stale = true` (computed at read time, not stored, so it is not a state).
- **Silent machine.** If a machine sends nothing for **15 minutes** while it has sessions in
  `working`, `needs_permission` or `asked`, the read API marks those sessions
  `machine_stale = true`. The machine-silent alert (1 hour) is described in push.md.
- **Done sessions with no list:** nothing happens; `done` is a resting state.

- **Dormant.** An open session (not `ended`/`gone`) that does not need you is reported with
  `dormant = true` (read time, not a state) when it is `done` with no activity for
  **12 hours**, or `working` and `stale` (above). Activity (`activity_at`) is the newest of
  `last_event_at`, `state_since`, `started_at` and `ended_at`. A listing is **not**
  activity: `claude agents` lists an idle session every 5 minutes for as long as its
  process runs, so a terminal left open on a finished session for days is dormant although
  it is listed. Any other open state (`failed`, a fresh `working`) is not dormant however
  old. `counts.live`, the machines' and accounts' live counts and the overview leave
  dormant sessions out; the session list still shows them inside its `days` window (a
  session listed in the window counts as in it). The board's "Open" filter is the same
  rule, and its other filter, "Ended or idle", holds exactly the sessions that are not
  open: ended, gone and dormant ones (marked "idle"), so a `done` badge there is no
  contradiction.

The timer interval and both thresholds are hub settings (`setting` table), with these
defaults.

### 5.1 Sessions from transcript history

`wy scan` reads whole transcript folders, so the first scan on a machine sends `token_rows` for every past session. Its per-session
facts (`sessions`, record.md 4.4) make a `session` row for a session the hub has never
seen. Such a session has **no live signal**: no hook event and no `session_list` entry
ever said it runs. The rules:

- **State.** A session with no state yet gets `ended` with `detail` = `history`
  (`end_reason` = `history`), `ended_at` = `state_since` = the `ts` of its newest
  transcript row the hub has. Newer rows move both forward; older rows change nothing.
  A session with any other state ignores transcript activity, so scanning a live
  session's transcript after each Stop never touches its state.
- **Coming back.** It is an ended session like any other: a later hook event or a list
  entry brings it back (`·` column), and a hook or list with a `ts` before the history
  state's `updated_at` refolds the session from its hook events and lists, which drops
  the history state.
- **Times.** `started_at` is the earliest row's `ts` (earliest wins, also for live
  sessions, whose transcripts can start before the process the list reports). A
  `token_rows` record never sets `started_at` from its own `ts` (that is when the scan
  ran): a session with facts but no rows has `started_at`, `ended_at` and `state_since`
  null. A session with no known time has no activity, so it is outside every `days`
  window of the session list.
- **Project.** `project` is the basename of the earliest row's `cwd` when the session has
  none (the transcript `cwd` is written with `~`, so `cwd` itself is left to hook events).
- **Not live.** History sessions are never in `counts.live` and need no one.

**Replay (`wy hub refold [--dry-run]`).** The raw records are kept, so the fold can be
rebuilt with the current engine over the whole history: every session with a hook event
or a list entry is refolded from those records (section 3, "Ordering"), and every other
session is rebuilt as history from all its `token_rows` records; `started_at` becomes the
earliest of its first hook event, its first row and (listed sessions) the stored start;
for a history session it is its first row, or null. It runs in one write transaction, so it
is safe while the hub runs (ingest waits for it), and running it twice changes nothing.
`--dry-run` reports the counts and writes nothing. It repairs hubs that typed history
before these rules (all imported sessions `done`, `started_at` = the scan time, no project).

## 6. Mapping to other views

| Watchyard | Warp cli-agent (section 14) | `claude agents` status |
| --- | --- | --- |
| working | in progress | busy |
| needs_permission | blocked (permission) | waiting (**uncertain**) |
| asked | blocked (question / input) | waiting / idle |
| done | success | idle |
| failed | failed | idle |
| ended | cancelled | not listed |
| gone | — | not listed |

When both the hub and a local source know a state, the hub's state wins.
