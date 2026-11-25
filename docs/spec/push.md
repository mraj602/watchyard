# Alerts and pushes

Status: spec v1; section 12 is a tombstone (the Expo channel, removed in schema v11); sections 13 (the link
channel) and 14 (the Android `wy-link` module) describe the Android path. Implemented in `internal/hub/push/...` (rules, engine, channels),
`internal/hub/store` (alerts, rules, targets), `internal/hub/api` (handlers). The web app
side (service worker, permission, subscription) is in [pwa.md](pwa.md); pairing and devices
in [pairing.md](pairing.md).

The hub decides and sends every push; phones only receive. Nothing here can make a client
do anything: snooze and seen change only the hub's own alert rows.

## 1. Words

| Word | Meaning |
| --- | --- |
| **Trigger** | A kind of condition: `needs_permission`, `asked`, `failed`, `limit_80`, `limit_95`, `reset`, `machine_silent`, the usage notices `limit_change`, `usage_reset`, `seat_change`, `weekly_drop`, the switch notices `session_moved`, `switch_digest`, and the drain notices `drain_started`, `drain_ended`, `drain_pulled` (3.9). |
| **Rule** | Whether, and after how long, a trigger notifies, for which seats/projects/machines/windows. Global, owned by the owner (section 3). |
| **Episode** | One continuous occurrence of a condition: one session sitting in one needs-you state, one window instance crossing 80%, one silence of one machine. Identified by its **episode key** (3.2). |
| **Alert** | The hub's row for one episode (`alert` table): due time, status, snooze, seen. At most one per episode (unique key): this is the dedupe. |
| **Target** | One place a push goes: one browser's Web Push subscription, one ntfy topic, or one phone's link (`push_target`). |
| **Channel** | A kind of target, `webpush`, `ntfy` or `link` (the Android app's own connection, section 13), with a hub-wide on/off switch (`channel`). |
| **Delivery** | One attempt to bring one alert to one target (`delivery`): pending, held, sent, failed, skipped, gone. |
| **Device prefs** | Per-device choices: muted triggers, own quiet hours, private, tap behaviour (5.4). They decide whether *this device* receives an alert, never whether the alert exists. |

Global rules vs device prefs, in one line: **the rule decides if and when an alert fires; the
device prefs decide which of the owner's devices hear it.** A phone (push scope) can change
its own prefs; only the owner can change rules.

## 2. Hub settings

One JSON object in `setting` key `push.settings` (owner: `PUT /v1/push/settings`):

| Key | Default | Meaning |
| --- | --- | --- |
| `quiet_hours` | `{"enabled": false, "start": "23:00", "end": "08:00", "tz": <hub tz>}` | Hub-wide quiet hours (5.2). Devices may override. |
| `private` | `false` | Default for devices whose `prefs.private` is null (5.5). ntfy targets on `ntfy.sh` default to private regardless (7.1). |
| `skip_remote` | `false` | Don't push `needs_permission` / `asked` for sessions with a Remote Control link (Claude's own app already notifies). |
| `skip_peers` | `true` | Don't push an `asked:question` that ends a turn of coordination between sessions (3.5, "Sessions coordinating"). |
| `snooze_min` | `15` | Snooze length (5–240). |
| `max_age_s` | `900` | An alert that would first push more than this long after it was due is not pushed (`stale`, 3.3). |
| `batch_s` | `2` | Alerts due within this many seconds of each other are sent as one push per target (5.3). |
| `flood` | `{"max": 6, "per_s": 600}` | At most `max` pushes per target per `per_s` seconds; the excess is merged (5.3). |
| `vapid_subject` | the hub's public URL | VAPID `sub` claim (6.2). |
| `webpush_hosts` | see 6.6 | Push service hosts a subscription endpoint may point at. |

Other settings: `hub.public_url` (pairing.md 2.3), `hub.tz` (IANA name; used for hub-wide
quiet hours, for times in push text when a device has no `prefs.tz`, and for analytics days),
`push.vapid_public` (6.2), `push.baseline_done` (3.8).

**The hub's time zone** is `hub.tz` when it names a zone the hub can load; else the hub
machine's own zone: `$TZ` of `wy hub serve` when it names one (a leading `:` is dropped),
else the zone `/etc/localtime` links to (the part after `zoneinfo/`); else UTC. A hub run as a
systemd user service has no `$TZ`, so `/etc/localtime` is what makes an Asia/Kolkata machine's
hub keep Asia/Kolkata and not UTC. `GET /v1/overview` returns it as `tz`, with `tz_source`
`setting`, `system` or `utc`; every time a client shows as "hub time" is in this zone and names
it.

## 3. Rules

### 3.1 Triggers

| Trigger | Subject | Condition (all must hold) | Default rule | Leaves when |
| --- | --- | --- | --- | --- |
| `needs_permission` | session | `session_state.state = needs_permission` | on, delay **30 s** | state or `since_at` changes |
| `asked` | session | `state = asked` (any detail: `question`, `elicitation`, `tool`) | on, delay **60 s** | state or `since_at` changes |
| `failed` | session | `state = failed` and `needs_you = 1` (so not `overloaded` / `server_error`, states.md 1) | on, delay **0**, `bypass_quiet` | state or `since_at` changes |
| `limit_80` | seat × window | current `usage_window` (`resets_at > now`) of a watched key (default `five_hour`, `seven_day`) with `peak_pct >= 80`, from a sample at most 30 minutes old | on, delay 0 | the window's `resets_at` passes |
| `limit_95` | seat × window | as above with `>= 95` | on, delay 0 | as above |
| `reset` | seat × window | a watched window whose `peak_pct >= 80` reaches its `resets_at` | **off**, fires at `resets_at` | — |
| `machine_silent` | machine | no record received from the machine for `delay_s` (default **3600**), measured from `max(machine.last_seen, awake_at)` (3.3), while it has at least one session in `working`, `needs_permission` or `asked` that is not `stale` (states.md 5). A pull `hello` counts as hearing from it (machines.md 3.5), and removed machines (`removed_at`) never alert | on | a record or a pull `hello` arrives from it (`recovered`) |
| `limit_change` | model | most reference accounts' 5-hour capacity for one model moved more than 1.2× or under 0.83× week on week (3.9) | on, delay 0 | 24 h after it fired (`expired`) |
| `usage_reset` | seat | the seat's 5-hour and weekly use dropped to about 0 % with the weekly reset time unchanged, confirmed by several fresh sources (3.9) | on, delay 0 | as above |
| `seat_change` | seat × machine | a folder signed in to another account, or to the same account in another org (3.9) | on, delay 0 | as above |
| `weekly_drop` | seat | the weekly use dropped more than 10 points without a reset, and it is not a `usage_reset` (3.9) | on, delay 0 | as above |
| `session_moved` | machine | the machine's Smart Account Switch moved sessions on its own and sent a `switch_notice` record (3.9; at most one per 5 minutes per machine) | on, delay 0 | as above |
| `switch_digest` | machine | the machine sent its daily switch digest as a `switch_notice` record (3.9) | on, delay 0 | as above |
| `drain_started` | seat | the owner set the seat's hub drain (`seat.drain_until`, setup.md 8.5 Owner drain) and it has not ended (3.9) | on, delay 0 | as above |
| `drain_ended` | seat | the drain reached its `drain_until` (`time`) or the seat's weekly peak reached 97 % (`aim`; the hub clears `drain_until`) (3.9) | on, delay 0 | as above |
| `drain_pulled` | machine | the machine's switch pulled sessions into a draining account and sent a `switch_notice` record with trigger `drain_pulled` (3.9; batched like `session_moved`) | on, delay 0 | as above |

Not triggers (by design): "turn finished" (board only) and "context 90%"
(reserved).

`limit_80` and `limit_95` use `peak_pct`, so a window that dips back under 80% after crossing
it does not cross again. A first sample already at or above 95% creates both episodes: the
95 one fires and the 80 one is stored `skipped` with reason `superseded`, so "crossing 80%
sends exactly one push" also holds for a window that jumps straight past 95%.

### 3.2 Episode keys (dedupe)

| Trigger | Episode key |
| --- | --- |
| session triggers | `s:<session id>:<state>:<since_at ms>` |
| `limit_80`, `limit_95` | `l:<seat id>:<window key>:<resets_at ms>:80` / `:95` |
| `reset` | `r:<seat id>:<window key>:<resets_at ms>` |
| `machine_silent` | `m:<machine id>:<last_seen ms at the moment it fired>` |
| `limit_change` | `x:<model label>:<up\|down>:<hub day YYYY-MM-DD>` |
| `usage_reset` | `z:<seat id>:<ms of the first low reading>` |
| `seat_change` | `c:<slot id>:<since_at ms of the new login>` |
| `weekly_drop` | `w:<seat id>:<weekly resets_at ms>:<ms of the first low reading>` |
| `session_moved` | `sw:<machine id>:m:<ms of the batch's first move>` (the record's `episode` behind the machine id) |
| `switch_digest` | `sw:<machine id>:d:<the machine's local day YYYY-MM-DD>` |
| `drain_started` | `d:<seat id>:<drain_until ms>:on` (a new end time is a new drain) |
| `drain_ended` | `d:<seat id>:<drain_until ms>:end` |
| `drain_pulled` | `sw:<machine id>:p:<ms of the batch's first pull>` |

- `since_at` moves only when the *state* changes (states.md 3), so a second
  `permission_prompt`, a `PermissionRequest` for the same prompt, or a second StopFailure
  while `failed`, all fall in the same episode: one push.
- `usage_window.resets_at` is the window's primary key and stays fixed for the window's life
  (samples join a window within 5 minutes), so a window instance alerts once per
  threshold, however many samples arrive.
- `alert.episode` is `UNIQUE`: creating an alert is `INSERT … ON CONFLICT(episode) DO
  NOTHING`. Restarts, refolds, replays and two code paths racing can never make two alerts
  for one episode.
- The direct ntfy push from `wy` (machines.md 5) computes the same session key on the
  machine (it knows the session, the state and the record `ts` that entered it). It reports
  each push in a **`direct_push` record** (record.md 4.9; an earlier draft put
  `derived.direct_push` on a hook record, which cannot work because outbox records are
  never rewritten). When the hub types that record it creates the alert as `fired` with
  reason `direct`, or turns a `pending` (or `skipped`/`stale`) alert of that episode into
  `fired`/`direct`, and sends nothing. Rules in [machines.md](machines.md) 5.5.

### 3.3 Timing

All times are hub times in milliseconds except `since_at`, which is the client `ts` of the
record that entered the state (states.md: the fold uses record times, not the hub clock).

- **Due time** of a session alert: `due = clamp(since_at + delay, now, now + delay)`, with
  `now` = hub time when the alert is created. The clamp keeps the person's experience right
  despite delivery lag and clock skew: a record that reaches the hub 10 s late still pushes
  about `delay` after the event; a machine whose clock runs ahead can never postpone a push
  by more than `delay`.
- Limit and failure alerts: `due = now`. `reset`: `due = resets_at`. `machine_silent`: the
  sweep (3.6) creates it already due. Usage notices (3.9): `due = now`, with their own stale
  rule (a `since_at` more than 6 hours before `now`).
- **Stale guard.** If at creation `now > since_at + delay + max_age_s`, the alert is created
  `skipped` with reason `stale` (records flushed from an outbox after hours away are history,
  not news; the board still shows the session as needing you). The same holds when the
  scheduler finds an alert, a snooze or a retry more than `max_age_s` overdue (after a hub
  restart or a suspended laptop): it is not pushed.
- **Catch-up hold** (machines.md 5.5). A session alert created already overdue
  (`due <= now`) from a batch whose machine reported more records queued (`X-Wy-Backlog`
  above the batch size, or a drain batch with `more`) gets `due = now + 10 s`; at fire time it
  (like any pending session alert of that machine) is postponed by 10 s more while that
  machine's latest heartbeat (at most 10 s old) still reports a backlog, for at most 2 minutes
  after the alert's creation. The backlog reports are kept in memory only: after a hub restart
  nothing is held until the machine reports again. This lets a `direct_push` record a
  few batches behind arrive first. The stale guard is unchanged.
- **`awake_at`.** The engine sets `awake_at = now` when it starts and whenever it detects a
  wall-clock jump (the laptop running a merged hub was suspended): on each wake-up it compares
  wall-clock elapsed time with monotonic elapsed time, and a difference over 60 s counts as a
  jump. Silence is measured from `max(machine.last_seen, awake_at)`, so a hub that was itself
  down or asleep never reports every machine as silent.

### 3.4 Rule matching and overrides

`rule` rows are either a **base rule** (one per trigger, `base = 1`, id `rule_<trigger>`,
cannot be deleted, only disabled) or an **override** (`base = 0`), which applies to part of
the world:

```json
{"seats": ["seat_01JA…"], "projects": ["api"], "machines": [], "windows": ["seven_day_opus"]}
```

Empty or missing lists mean "any". `projects` compares with `session.project` (case
sensitive). `windows` is only meaningful for limit and reset triggers; for them an empty
`windows` means `["five_hour", "seven_day"]`.

For one subject the engine picks, among the rules of that trigger whose scope matches, the one
with the **most non-empty lists**; ties go to an override over the base rule, then to the
newest `updated_at`, then to the lowest id. The picked rule's `enabled`, `delay_s` and
`bypass_quiet` apply. A disabled pick means no push: the alert is still created, as
`skipped` with reason `rule_off`, so the Now inbox can show and act on it.

Examples: "asked: 5 minutes for project `infra`", "everything off for seat `research`" (one
override per trigger, or the API's `scope` with `enabled: false`), "push at 80% of
`seven_day_opus` for seat `client`".

The rule in force is resolved twice: when the alert is created (delay) and again when it
fires (an owner who switches a rule off during a delay stops the push: `cancelled`,
`rule_off`).

### 3.5 Reconcile (how alerts follow state)

The engine never reacts to individual events. After each committed ingest batch it receives
the batch's `Changes` (sessions, seats, machines touched; `internal/hub/ingest`) and
**reconciles** each touched subject: it compares what *should* exist now with what does.
This is idempotent, so it is also run for every needs-you session and every hot window at
start-up, and it is correct after refolds and late records.

For a session:

```
cur  := session_state + session (state, since_at, needs_you, seat, project, machine, remote_url)
want := episode key for cur, if cur matches a session trigger (3.1), else none
for each alert of this session in status pending | fired | snoozed | seen:
    if alert.episode != want:
        reason := answered    if cur.state in (working, done)
                  ended       if cur.state in (ended, gone)
                  superseded  otherwise (another needs-you state)
        pending          → cancelled (reason)        -- the push never happened
        fired | snoozed  → closed (reason)           -- a snooze re-push is dropped
        seen             → closed (reason)
if want != none and no alert has episode want:
    rule := match(trigger, subject)
    create alert (pending, due 3.3) or skipped (stale | rule_off | remote_control | peers | baseline)
```

`remote_control`: `skip_remote` is on, the trigger is `needs_permission` or `asked`, and the
session has a `remote_url`.

`peers`: `skip_peers` is on (the default) and the alert is only about sessions coordinating
with each other (below).

**Sessions coordinating** ([peers.md](peers.md) 6.4). When sessions talk through peers, a turn
often ends on a plain-text question meant for the other session, or with the session waiting
on the other session's reply. The question detector (states.md 4) cannot tell those from a
question to the person, so without this rule every coordination round pushed `asked`. The fold
keeps `session_state.peers` (states.md 3.1): `message` when a peer message started the current
turn, `wait` when the turn ended waiting on a reply to a thread message the session sent. An
alert is **peers-only** when its trigger is `asked`, the detail is `question` and `peers` is set.
A peers-only alert is created `skipped` with reason `peers`: nothing is pushed, the board
still shows the session as `asked` (it needs no one, but the state is the state), and the Now
inbox and the Alerts log list the skipped row. If a `wait` arrives during the delay of a pending
`asked:question` (same episode), the fire-time re-check (3.6 step 3) cancels it with reason
`peers`. Never peers-only, so always pushed as usual: `needs_permission` (a real permission
prompt), `asked:tool` (AskUserQuestion, ExitPlanMode), `asked:elicitation` and `failed`, in any
turn; and any turn a person started, unless it ends waiting on a peer. With `skip_peers` off,
peers-only alerts push like any other. Direct ntfy pushes from a machine apply the same rule
([machines.md](machines.md) 5.4 condition 1).

For a seat: for each current watched window, create the `limit_80` / `limit_95` alerts that
the window's `peak_pct` calls for, and the `reset` alert (status pending, `due = resets_at`)
when `reset` is enabled and `peak_pct >= 80`. Alerts whose window has reset are closed with
reason `reset_passed` (a `limit_*` alert never stays open past its window).

For a machine: a record from it closes its open `machine_silent` alert (`recovered`). No push
is sent for recovery.

"Answering in the terminal at 40 seconds means no push" (an acceptance case): the
UserPromptSubmit record reaches the hub about 2 s after the answer (`wy flush` debounce), the
fold moves the session to `working`, reconcile cancels the pending alert (due at 60 s) with
reason `answered`, and the Alerts log shows it as **cancelled**.

### 3.6 Scheduler

One goroutine owns all timing. It sleeps until the earliest of: the earliest `alert.due_at`,
the earliest `delivery.next_at`, the next silence sweep (every 60 s), or a poke (new
changes, an API action). Nothing is held only in memory: every timer is a column
(`alert.due_at`, `delivery.next_at`), so a restart resumes exactly (subject to the stale
guard). On wake-up:

1. Wall-clock jump check (3.3).
   1b. Rule off: when the rules changed since the last pass (a rule or override added,
   removed, switched or rescoped), every `pending` alert (waiting, never fired) is matched
   again (3.4); if the rule that matches it now is off, or none matches, it is **cancelled**
   with reason `rule_off` at once, so turning a rule off (or adding a "do not notify"
   override) never leaves alerts "waiting" until their due time (a reset alert waits for the
   window's reset). Alerts already fired, snoozed or seen are left as they are; a snoozed
   one meets the rule check of step 3 when its re-push is due.
2. Silence sweep: machines with a live session (3.1) and `max(last_seen, awake_at) + delay
   <= now` and no alert with their current key → create `machine_silent` (due now).
3. Due alerts: every alert with `due_at <= now + batch_s` in status `pending` or `snoozed`.
   For each: re-check the condition (re-read the session state; for limits the window) and
   the rule; if the episode is over, apply reconcile's outcome instead of firing. Otherwise
   **fire** it (section 4) as part of this batch.
4. Fan out the batch to targets (section 5).
5. Due deliveries: retries and held deliveries with `next_at <= now` (5.2, 5.6).

While the setting `hub.frozen` is set (a hub move, machines.md 8.1) the scheduler skips steps 1b and
2–5: nothing is created by the sweep and nothing is sent, so an old and a new hub never both
push. Unfreezing resumes, subject to the stale guard; it also counts as waking up
(`awake_at` = the unfreeze), so machines that kept queueing while the hub was frozen are not
reported silent the moment it resumes. Reconcile keeps running while frozen (an episode that
ends is still closed or cancelled), and the scheduler polls every 10 s to notice the unfreeze.

All database work goes through `store.Write`; the engine never holds a transaction while
sending HTTP. Sends run on a small worker pool (4) with a 10 s timeout each, inside a send
budget of 40 s per pass and at most 200 pushes per pass: a push that was not started (budget
used up, or over the cap) stays `pending` with its first retry slot and goes out in a later
pass; a send that has not returned when the budget ends counts as a timed-out try. The write
transaction before the sends and the write-back after them use separate contexts (the
write-back gets a fresh 30 s and is retried), so slow targets can never make the write-back
fail and leave a successful send `pending` to be sent again. Results are written back in one
transaction per pass.

### 3.7 Rules API

`GET /v1/rules` (read, push, owner) returns `{rules, settings}`; `POST /v1/rules`,
`PUT /v1/rules/{id}`, `DELETE /v1/rules/{id}` (owner) manage overrides and base-rule
switches; `PUT /v1/push/settings` (owner) replaces `push.settings`. Shapes in api.yaml
(`Rule`, `PushSettings`). Validation: `delay_s` 0–86400; ids of seats and machines must
exist; `projects` 1–100 characters each; at most 200 rules. An override may not have the
same scope as another rule of its trigger, the base rule included (the same seats, projects
and machines as sets; for limit and reset triggers the same windows, empty meaning
`["five_hour", "seven_day"]`): only one of them could ever be picked (3.4). `POST` or a
`PUT` that would make one answers 409 with code `duplicate_rule` and `rule`, the id of the
rule to change instead. An override for every subject is the base rule: change the base
rule. Rows that already break this (from before the check) still load and match as 3.4 says.

### 3.8 First start (baseline)

The engine never floods on first start. The first time the engine
starts on a hub (`push.baseline_done` unset), every condition that already holds (sessions
waiting, windows over 80%, silent machines) is recorded as `skipped` with reason
`baseline`, then `push.baseline_done = true`. Later starts reconcile normally. The Alerts log
hides `baseline` rows.

### 3.9 Usage and switch notices

Four triggers are not about a session, a limit crossing or a machine, but about the limits
themselves changing under the owner's feet (two more, the switch notices, are at the end of this
section). They are found by the hub's calibration job (every
15 minutes, setup.md 8.5, "On the hub"), which hands each one to the engine as a **notice**: its
trigger, episode key, subject (seat, machine), `since_at` and facts. They are low-urgency
(6.4, 7.2) and never batched with session alerts. The board shows the numbers behind them in
an account's Details (setup.md 8.5, Usage relation; pwa.md 3.0a; phone.md P4).

**Watching a seat's readings** (`usage_reset`, `weekly_drop`). The job reads each seat's
`five_hour` and `seven_day` samples after its watch cursor, up to now (not only settled data),
by the `limit_sample` key. Samples of one time, source and session form one reading; its
*source* is `web` for claude.ai, else the status-line session id. A reading whose `five_hour`
`resets_at` is more than 5 minutes older than the newest one seen is left out whole (a session
that sat idle through a 5-hour reset: nothing in it is fresh). Per seat it keeps:

- the **weekly level**: the highest weekly reading of the current weekly window (a reading whose
  `resets_at` is within 5 minutes of the newest weekly `resets_at` seen). A reading with an
  older `resets_at` is a session that sat idle through a reset and is ignored; a newer one starts
  a new window (the level is that reading, any open candidate is dropped). A reading 1 to 10
  points under the level is ignored (a session that reported an old value);
- a **candidate drop**: opened by a reading of the current window more than 10 points under the
  level. It records the first low reading's time `t`, the old level, whether it was a drop **to
  about 0** (that reading's weekly and 5-hour values are both 2 % or less), the newest
  `five_hour` `resets_at` seen before it, and the sources that agree. A reading *agrees* when
  its weekly value is more than 10 points under the old level and its `resets_at` is that of
  the current window, or it has no weekly `resets_at` and a weekly value of 2 % or less before
  the window's reset (claude.ai shows an emptied window that way). A reading at the old level
  (within 1 point) after `t` names a **stale source**: a session still showing the old value
(claude.ai is never a stale source).
- the **masks** of the last 8 days (below), and the stale sources of the current weekly window.

A candidate is decided, at a reading or when the job runs at `now`, as follows:

| Candidate | When | Outcome |
| --- | --- | --- |
| to about 0 | 3 or more distinct sources agree within 3 hours of `t` | **`usage_reset`** (since `t`); the level becomes the highest agreeing reading; mask |
| to about 0 | 3 hours after `t` with 2 agreeing readings or more but fewer than 3 sources | **`weekly_drop`** (since `t`) |
| not to about 0 | the second agreeing reading (any source, the first included) within 3 hours of `t` | **`weekly_drop`** (since `t`); the level becomes the highest agreeing reading |
| either | 3 hours after `t` with one agreeing reading only | dropped: no notice, the level is unchanged |

After a decided drop, readings from its stale sources are ignored for the rest of the weekly
window, and readings of any source at the old level or above (within 1 point) until the end of
the 5-hour window it happened in (as for the mask, below), so sessions that had not reported
yet cannot raise the level back. A weekly reset (a reading of a newer window)
never makes a candidate: that is not a drop.

**The mask.** A confirmed `usage_reset` masks the seat's readings from `t` to the end of the
5-hour window it happened in (the recorded `five_hour` `resets_at`, at least `t` + 1 hour, at
most `t` + 5 hours) from calibration (setup.md 8.5): stale sessions keep sending the old values
with the same reset times there, so pairs across it would be wrong. While a candidate to about
0 waits for its decision, the seat's calibration folds only readings before `t`. Masks are kept
8 days, longer than any pair.

**`seat_change`.** From `slot_login` (schema.sql): a folder's login period with a seat whose
folder's previous period with a seat (logged-out periods skipped) had another seat. The account uuid differs: *account changed*; the same
account in another org: *org changed*. Subject: the new seat and the folder's machine. Only
periods of the last 8 days are read, at every run: the episode key makes each period alert
once, and a period a machine reported late is still found.

**`limit_change`.** After the seats' usage relation is folded (setup.md 8.5), per model label
(`Opus 4.6`): the **reference accounts** are the seats that are Claude-Code-heavy in both this
week and last week and have a weekly median capacity index for that model in both weeks. With
at least 2 reference accounts, when more than half of them have `change` (this week's median /
last week's) over 1.2, the notice says the limits look **higher** (`up`, more API dollars per
5-hour point); more than half under 0.83, **lower** (`down`). It is not created when an alert
of the same model and direction was created in the last 14 days (a step change stays in the
week-on-week ratio for up to 13 days). Its `since_at` is the run's time; it has no seat.

**Timing and life.** A notice becomes an alert like any other (3.4, 3.5: rule match on the
trigger with its seat and machine as the subject; skipped `rule_off` when the rule is off; one
alert per episode). It is `pending` with `due = now`, or `skipped` with reason `stale` when its
`since_at` is more than 6 hours before `now` (history the job read late, for example at the first
run after an upgrade: the engine never floods). A fired, snoozed or seen notice closes 24 hours
after it fired, reason `expired` (the scheduler's silence sweep, 3.6 step 2, closes them). There
is no other end: nothing about the episode changes later.

**Switch notices** (`session_moved`, `switch_digest`). The other two notices come from a
machine, not from the calibration job: its Smart Account Switch (setup.md 8.6, Notices and
digest) sends a `switch_notice` record (record.md 4.12) after its automatic moves, batched at most
once per 5 minutes, and once a day with the digest. Ingest hands each one to the engine as a
notice in the batch's changes; the next pass creates the alert (also while the hub is frozen:
it stays `pending` until the hub is unfrozen). Subject: the record's machine (no seat), so a rule
scoped to machines and a device's `mute` apply as to any trigger. Episode: the record's `episode`
behind `sw:<machine id>:`, so a machine cannot repeat or shadow another's notice and a record sent
twice is one alert. Facts: `machine` (the machine's name), `moves`, and `message` (the machine's
text of the moves, or the digest's summary line). They follow the timing and life above (low
urgency, stale after 6 hours, closed 24 hours after they fired). A record with `direct: true`
was already pushed by the machine's direct push because it could not reach the hub (machines.md
5): its alert is kept for the board and the phone's list but created `skipped` with reason
`direct`, and nothing is sent. A hub older than this ignores the kind (stored untyped).

**Drain notices** (`drain_started`, `drain_ended`, `drain_pulled`; schema v25). A drain set on the
hub (`seat.drain_until`, written by `PATCH /v1/accounts/{seat}`, `PUT /v1/accounts/{seat}/drain`
or `wy account drain`) is shared by every machine (setup.md 8.5, Owner drain). The scheduler's
sweep (3.6, every 60 s) reads the seats with a drain: while `drain_until` is in the future and
the seat's weekly peak is under 97 %, it hands a `drain_started` notice (once per drain, by its
episode); when `drain_until` has passed, or the weekly peak reached 97 % (the drain aim), it
clears `drain_until` (only if it was not changed meanwhile) and hands a `drain_ended` notice
with detail `time` or `aim`. A drain the owner turns off by hand ends without a notice. Subject:
the seat. `drain_pulled` comes from a machine like `session_moved`: the machine's switch keeps
the moves that pulled a session into a draining account (reason `pulled in: …`) apart from its
other moves and sends them as their own `switch_notice` (trigger `drain_pulled`, episode
`p:<ms>`, batched at most once per 5 minutes); the subject is the machine. All three follow the
timing and life above, have the rules `rule_drain_started`, `rule_drain_ended`,
`rule_drain_pulled` (on) and go to the Drain category (5.4).

## 4. Alert lifecycle

| Status | Meaning | `due_at` |
| --- | --- | --- |
| `pending` | Waiting for its delay. | first push time |
| `fired` | Pushed (deliveries made); the episode is still open. | — |
| `snoozed` | Pushed, then snoozed; will push again if still open. | re-push time |
| `seen` | Acknowledged ("Seen" from a push, the Now inbox or the session page). No more pushes for this episode. | — |
| `cancelled` | The episode ended before any push (`answered`, `ended`, `superseded`, `rule_off`), or it turned out peers-only before it was due (`peers`, 3.5). | — |
| `closed` | The episode ended after a push or a seen (same reasons, plus `recovered`, `reset_passed`, `expired` for usage notices, 3.9). | — |
| `skipped` | Never pushes by design (`stale`, `rule_off`, `remote_control`, `peers`, `superseded`, `baseline`). | — |

```
            create
              │
   ┌──────────┼───────────────┐
   ▼          ▼               ▼
 skipped   pending ──────► cancelled         (episode over before due, or rule off at fire time)
              │ due
              ▼
            fired ◄──────── snoozed           (snooze due and still open: fire again, fire_count+1)
              │ ├─ snooze ──►   ▲
              │ │               │
              │ └───────────────┘
              ├─ seen ──► seen
              ▼
            closed        (episode over; also from snoozed and seen)
```

- **Fire.** `fired_at` = first push time, `fire_count += 1`, deliveries are made (section 5).
  A fire with zero eligible targets still counts as fired (the log says "no devices").
- **Snooze** (`POST /v1/alerts/{id}/snooze`, an action token, or a swipe in the Now inbox):
  allowed in `pending`, `fired`, `snoozed`; sets `snoozed_until = due_at = now + snooze_min`
  (or the request's `minutes`, 5–240) and status `snoozed`. A snoozed alert that is still
  open at `due_at` fires again; the push says "Still waiting · 19 min" and replaces the
  earlier notification (same tag). Snoozing a `pending` alert simply moves its first push.
- **Seen** (`POST /v1/alerts/{id}/seen` or action token): allowed in `pending`, `fired`,
  `snoozed`; sets `seen_at`, `seen_by` (device id), status `seen`, drops `due_at`, and marks
  its not-yet-sent deliveries `skipped` (`seen`). It does not change the session's state, but
  the session leaves the Needs-you list, `counts.needs_you` and the badge count (6.5, 13.5)
  until its state next changes (a new episode, so a new alert; states.md 1). Other session
  lists still show it, with its real state and the alert brief. The hub's own count (the
  `needs_you` of a payload, a widget refresh and `GET /v1/overview`) omits such sessions.
- Snooze and seen on `cancelled`, `closed` or `skipped` alerts: 409 `conflict`, except seen on
  a `skipped` alert whose episode is still open, which is allowed (so the inbox can quiet a
  `rule_off`, `stale`, `peers`, `remote_control` or `baseline` row). Such an alert is the
  session's `alert` brief in the read API (10.1), so every Needs-you row carries the alert its
  Seen acts on. Snooze stays for `pending`, `fired` and `snoozed` only: a skipped alert never
  pushes, so there is nothing to put off.
- **Seen on a session** (`POST /v1/sessions/{id}/seen`, scopes `push` or owner): marks the
  session's current Needs-you episode seen, for a row that has no alert at all (the hub runs
  with `--no-push`, so no alert is ever made; or the engine has not reached a fresh episode
  yet). The session must need you now (`needs_you` 1 and not ended or gone), else 409
  `conflict`; an unknown session is 404. When the episode has an alert, this is Seen on that
  alert (the rules above; already seen is a no-op). When it has none, the hub creates the
  episode's alert directly as `seen` (the trigger of 3.1, `seen_at`, `seen_by`, no rule, no
  `due_at`, nothing delivered) and publishes an `alert` event with change `seen`; the
  episode's unique key (3.2) then stops reconcile from making a second alert for it, so it
  never pushes. It works with or without the engine (pushes off). The answer is the alert,
  as for `POST /v1/alerts/{id}/seen`. Clients call it only for a Needs-you row with no
  `alert` brief; a row with one uses the alert's own Seen (older hubs have no such path).
- Cross-device: a push already on a lock screen cannot be withdrawn (Web Push has no silent
  "dismiss" message on iOS, and Chrome shows a generic notice for pushes that show nothing).
  Seen on one device stops future pushes everywhere.
- Every status change publishes one `alert` event on `/v1/stream` (api.yaml `StreamAlert`)
  and sets the `alert` brief on the session in the read API.

## 5. Delivery

### 5.1 Fan-out

For each alert fired in a batch, the eligible targets are those with `enabled = 1`,
`gone_at IS NULL`, whose channel is enabled, and whose device (if any) is not revoked or
expired. For each target, with its effective prefs (5.4):

1. Trigger in `prefs.mute` → delivery `skipped` (`muted`).
2. Quiet hours active for this target and the rule does not bypass them → delivery `held`,
   `next_at` = end of the quiet period (5.2).
3. Over the flood cap → delivery `held` (`flood`), `next_at` = when the cap frees (5.3).
4. Otherwise → delivery `pending`, sent in this batch.

### 5.2 Quiet hours

- Shape: `{"enabled": bool, "start": "HH:MM", "end": "HH:MM", "tz": "<IANA>",
  "critical": bool}`. `start > end` spans midnight (23:00–08:00). `start == end` is off.
  Inclusive start, exclusive end, evaluated in `tz` with Go's `time.LoadLocation`, so
  daylight-saving days are handled by the zone (a non-existent local time moves forward).
- Effective quiet hours: the device's `prefs.quiet_hours` when not null, else the hub's.
- "Quiet hours hold everything except failures": a rule with
  `bypass_quiet = 1` (default only `failed`) goes through, unless the effective quiet hours
  say `"critical": false` (a device that wants true silence).
- At the end of a quiet period the scheduler takes every `held` (`quiet`) delivery of that
  target: those whose alert is still `fired` or `snoozed` (still open, not seen) are sent as
  **one** summary push ("While you were away: 2 sessions still need you"; a single one is
  sent as itself); the rest become `skipped` (`resolved`).

### 5.3 Batching, summaries and the flood cap

- **Batch.** Session alerts (`needs_permission`, `asked`, `failed`) due within `batch_s` of
  each other and going to the same target are sent as one push: title "3 sessions need you",
  body up to four lines `work · api — asked`, tag `wy-summary`, deep link `/#/`. Their
  deliveries share one `batch` id. Limit, reset, silence and usage alerts are never merged.
- **Flood cap.** At most `flood.max` pushes per target per `flood.per_s`. Further deliveries
  are `held` (`flood`) and go out as one summary when the window frees. This protects a phone
  from a burst (a replay, many sessions hitting a limit at once) and keeps within ntfy.sh
  limits.

### 5.4 Device prefs

`device.prefs` (schema v3), edited with `PATCH /v1/devices/self` (the device) or
`PATCH /v1/devices/{id}` (owner). Targets with a device use their device's prefs; targets
with no device (an ntfy topic the owner added) use `push_target.prefs`.

```json
{
  "mute": ["limit_80"],
  "quiet_hours": null,
  "private": null,
  "tap": "auto",
  "tz": "Asia/Kolkata"
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `mute` | `[]` | Triggers this device does not receive. A device cannot receive a trigger whose rule is off. |
| `mute_categories` | `[]` | Categories (below) this device does not receive at all: a trigger of a muted category is skipped with reason `muted` like a muted trigger. Unknown names are a 400. |
| `quiet_hours` | `null` | `null` = follow the hub; an object = this device's own (5.2). |
| `private` | `null` | `null` = hub default; `true`/`false` overrides (5.5). |
| `tap` | `"auto"` | What tapping a push opens: `session` (the web app's session page), `claude` (the Remote Control link when there is one), `auto` (session page if the hub answers within 1.5 s, else the Remote Control link; pwa.md 5). |
| `tz` | `null` | Time zone for times in push text ("resets 17:00"). The web app sends `Intl.DateTimeFormat().resolvedOptions().timeZone` when it registers. |

**Categories and channels.** Every trigger belongs to one category; the payload carries the
category (`category`) and the Android channel it goes to (`channel`, phone.md P6.1), so the
phone can give each its own sound and the owner can turn a whole category off per device:

| Category | Triggers | Android channel |
| --- | --- | --- |
| `sessions` | `needs_permission`, `asked`, `failed`, summaries | `wy_needs_you` |
| `limits` | `limit_80`, `limit_95`, `reset` | `wy_limits` |
| `switch` | `session_moved`, `switch_digest` | `wy_switch` |
| `drain` | `drain_started`, `drain_ended`, `drain_pulled` | `wy_drain` |
| `usage` | `limit_change`, `usage_reset`, `seat_change`, `weekly_drop` | `wy_usage` |
| `machines` | `machine_silent` | `wy_machines` |
| `test` | test pushes (never muted) | `wy_test` |

A category off (`mute_categories`) means nothing of it is sent to that device; sound,
vibration and the lock screen are set per channel in Android's own settings (the phone's
Notifications screen opens the channel's page). ntfy has no custom channels: the ntfy app
files messages by priority (7.2), and only `mute_categories` applies.

The phone's Alerts screen (`PhoneAlerts`) shows the rule list with the global delays
read-only and a switch per trigger that edits `mute`; the owner's board shows the same list
with the global switches and delays editable (`DeskAlerts`).

### 5.5 What a push says (text and privacy)

Labels: `seat` = the board's name for the account (webui `components/seat.js` `seatName`; hub `store.SeatDisplayNames`): the seat's label,
else the account email's local part; when two shown seats would read alike (compared
lowercase with only letters and digits kept, so "Alex.Kim" and "alexkim" collide), each
gets the first part of its email domain too ("alex · gmail"); no seat = "Unattributed"; `project` = `session.project`; `machine` = machine name; times in the
target's `tz`, else `hub.tz`, as `HH:MM` today, `Thu 17:00` within a week. Every field is cut
to fit (title 80, body 180 characters, with `…`).

| Trigger | Title | Body |
| --- | --- | --- |
| `needs_permission` | `work · api · needs permission` | Tool name and Claude Code's own notification text when kept (record.md 4.1: `message`, e.g. "Claude needs your permission to use Bash"), then `· gpu-box` |
| `asked`, detail `question` | `work · api · asked a question` | `session_state.question`, else the first line of the last message, else "Waiting for your answer · gpu-box" |
| `asked`, detail `tool` | `work · api · plan ready` (ExitPlanMode) or `asked a question` (AskUserQuestion) | `derived.question` when kept, else as above |
| `asked`, detail `elicitation` | `work · api · is waiting for input` | Notification `message` |
| `failed` | `client · data-pipeline · rate limited` (`rate_limit`), `login needed` (`authentication_failed`), `billing problem` (`billing_error`), else `stopped: <detail>` | rate limit: "Resets 17:00 · work has 59% left" (5.7); else machine name |
| `limit_80` / `limit_95` | `client · 5-hour at 82%` (at ≥ 100: `client · 5-hour limit reached`) | "Resets 17:00 · work has 59% left" |
| `reset` | `client · 5-hour reset` | "Back to 0% · peaked at 97%" |
| `machine_silent` | `old-vps silent for 1 h` | "3 live sessions · last record 13:02" |
| `limit_change` | `Opus 4.6 limits look higher` (`lower`) | "5-hour capacity 1.3× last week on 3 of 4 accounts · check" |
| `usage_reset` | `work · usage reset` | "5-hour and 7-day back to 0% · 7-day still resets Thu 09:00 · 3 sessions agree" (claude.ai counts as one) |
| `seat_change` | `work · ~/.claude-2 on gpu-box changed account` (`changed org`) | "Was personal; now this account" (org: "Same account, now in Acme") |
| `weekly_drop` | `work · 7-day dropped 64% → 31%` | "Something changed, check · no reset (resets Thu 09:00)" |
| `session_moved` | `gpu-box · Moved a session` (`Moved 3 sessions`) | The machine's text: "api ~/.claude -> ~/.claude-2: 5h 96% >= 95", or "3 moves: a x->y; b ..." |
| `switch_digest` | `gpu-box · Switch digest` | The digest's summary: "Last 24h: 4 moves, 1 limit hits ..." |
| `drain_started` | `claude-4 · draining until 18:00` | "New sessions go to it first and busy ones are pulled in, up to 97% of its week" |
| `drain_ended` | `claude-4 · drain ended` | `aim`: "Its week reached 97%; back to its usual mode"; `time`: "Its end time came at 64% of its week; back to its usual mode" |
| `drain_pulled` | `gpu-box · Pulled a session into a draining account` (`Pulled 2 sessions …`) | The machine's text of the pulls |
| snooze re-push | as the trigger, title prefixed `Still waiting · 19 min · ` | as the trigger |
| summary | `3 sessions need you` | up to four `work · api — asked` lines, then `+2 more` |

Question and message text appear only when the hub has them, which needs last messages
allowed; otherwise the generic body is used.

**Private pushes** (`prefs.private` or hub default) replace title and body with:
"Watchyard" / "1 session needs you" (session triggers), "N sessions need you" (summary),
"An account is near its limit" (limits), "A limit has reset", "A machine went quiet",
"Account usage changed" (usage notices), "A session moved to another account", "An account is
draining", "An account's drain ended", "Sessions moved to a draining account". The
deep link becomes `/#/alerts/<alert id>`, which the web app resolves after it has loaded
(pwa.md 3), so neither the text nor the link names a project, account or session.

Who can read push text:

| Channel | Payload readable by | So private mode protects |
| --- | --- | --- |
| Web Push | Only the subscribed browser: RFC 8291 encrypts the payload end to end; Apple, Google and Mozilla see a ciphertext, its size and timing | The lock screen and notification history only |
| ntfy, self-hosted | Your ntfy server; for iOS instant delivery your server forwards a hash of the topic and a message id (not the text) to ntfy.sh's upstream | The lock screen, and the ntfy server if it is not yours |
| ntfy.sh | ntfy.sh, and anyone who learns the topic name (topics are open; the random name is the only secret) | Content in transit and at rest on ntfy.sh. **Private is forced on** for ntfy.sh targets unless the owner explicitly unchecks it on that target |
| Link (Android app, section 13) | Only the app: sealed per RFC 8291 to a Keystore key; the hub and your tailnet carry ciphertext | The lock screen and notification history only |

Apple and Google can therefore not read the text of a Web Push; they still can for ntfy.

### 5.6 Retries and dead targets

| Result | Delivery | Target |
| --- | --- | --- |
| Web Push 201 (or any 2xx); ntfy 200 | `sent` | `last_ok_at`, `fail_count = 0` |
| Web Push 404 or 410 | `gone` | `gone_at = now`, `enabled = 0`. Never retried. The web app re-subscribes on its next start (pwa.md 6). Gone targets are deleted after 30 days. |
| 429, 500–599, timeout, connection error | `pending`, `tries + 1`, `next_at` = now + 15 s, 1 min, 5 min, 15 min (then `failed`); a `Retry-After` header (seconds or HTTP date) is honoured up to 1 hour | `last_error`, `fail_count + 1` |
| Web Push 400, 401, 403 (bad VAPID JWT, Apple `BadJwtToken`, key mismatch), 413 | `failed`, not retried | `last_error` with the status and the first 200 bytes of the reply; shown in Settings |
| ntfy 401, 403 (bad token, topic reserved), 413 | `failed`, not retried | `last_error` is `HTTP <status>` only (7.3) |
| Any target with `fail_count >= 20` and no success for 7 days | — | `enabled = 0`, reason shown in Settings |

Before every retry the alert is re-checked: if it is no longer `fired` or `snoozed`, the
delivery becomes `skipped` (`resolved`). Retries also stop once the push's TTL (6.4) has
passed.

### 5.7 "Who has room"

For a rate-limit failure or a limit alert, the body names the seat with the most room in the
same window key (`five_hour` for a rate-limit failure, the alert's window for a limit alert),
by the rule the board and the phone use for "Most room right now" (pwa.md, Accounts):

- A seat's **effective use** of the window is its current (newest) window's `used_pct`, so an
  older reading still counts until its reset; it is **0** when that window's `resets_at` has
  passed, when it is idle (claude.ai's newest reading of the key, newer than the window, is 0 %
  with no reset time: not started, record.md 4.3), or when the seat has no reading for this
  window but has one for another. A seat with
  no reading at all is skipped.
- Among non-hidden seats other than the subject, the lowest effective use wins; a tie goes to
  the first in list order (the board's seat order: `sort_order`, then creation, then id); and
  only if it is under 80%.

So an idle account (0%, its window already reset or never read for this key) is named before a
busier one. Wording: "work has 59% left" (100 minus the effective use; an idle account "has
100% left"). Nothing is said when no seat qualifies. Watchyard never switches accounts.

The Accounts page's "Most room right now" line uses a different, idle-friendly rule (pwa.md 3.0a):
a window counts as 0 when its `resets_at` has passed or when the account has no reading for it but
has one for another window; an account with no reading at all is skipped; the score is the larger of
the 5 h and 7 d use, lowest first, under 80%. Seats that are idle are the ones with most room.

### 5.8 Remote Control link and deep links

- **Remote Control link.** `session.remote_url`, filled by ingest from a hook record's
  `derived.remote_url` (`wy hook` finds it in the transcript tail) or from a `token_rows`
  record's `sessions[].remote_url` (`wy scan`), newest wins (record.md 4.1 and 4.4). The
  engine copies it into `alert.facts` when the alert is created and refreshes it when it
  fires, so a link found during the delay is still included. **Uncertain:** a stored link
  says Remote Control was on at some point, not that it is on now; a stale link opens a
  finished session in the Claude app.
- **Deep links** into the web app (hash routes, pwa.md 3):

  | Push | Link |
  | --- | --- |
  | session alert | `/#/sessions/<session id>?alert=<alert id>` |
  | limit, reset | `/#/accounts?seat=<seat id>` |
  | usage notice | `/#/accounts?seat=<seat id>`; `limit_change`: `/#/accounts` |
  | machine silent | `/#/machines?machine=<machine id>` |
  | summary | `/#/` (Now) |
  | private (any) | `/#/alerts/<alert id>` (the app looks the alert up and redirects) |

  ntfy needs absolute URLs: the hub prefixes `hub.public_url`. Without one, ntfy messages
  carry no `click` and no Snooze/Seen actions (7.2).

## 6. Web Push

### 6.1 Decision: implement it in the hub, no library

Web Push needs three pieces: a VAPID JWT (RFC 8292, ES256), the `aes128gcm` payload
encryption (RFC 8291 over RFC 8188), and an HTTP POST (RFC 8030).

- The usual Go library, `github.com/SherClockHolmes/webpush-go`, is alive but slow-moving: as
  of October 2026 its newest release is v1.4.0 (January 2025), the one before was 2023, it has
  10 open issues, and it pulls `github.com/golang-jwt/jwt/v5` and `golang.org/x/crypto` into a
  module that has neither.
- Everything needed is in Go 1.27's standard library: `crypto/ecdh` (P-256 key agreement),
  `crypto/hkdf` (standard since Go 1.24), `crypto/aes` + `crypto/cipher` (GCM),
  `crypto/ecdsa` + `crypto/sha256` (ES256), `encoding/base64`.
- Our implementation is about 250 lines, single record (4096 bytes) only, and is tested
  byte-for-byte against the worked example in RFC 8291 section 5 and appendix A (section 11.2) plus an
  independent decoder (`http_ece`, Martin Thomson's reference implementation) in an opt-in
  test.

So: package `internal/hub/push/webpush`, no new dependency (ARCHITECTURE.md rule:
"standard library first").

### 6.2 VAPID keys

- Made at the engine's first start: `ecdh.P256().GenerateKey(rand.Reader)`. The private
  scalar (32 bytes) goes in `hub_secret` key `vapid_private`; the public key, uncompressed
  (65 bytes, `0x04‖X‖Y`) and base64url without padding, in setting `push.vapid_public` and in
  `GET /v1/push/config` (the web app passes it as `applicationServerKey`).
- The keys are part of the database, so `wy hub backup`, export and import keep every
  subscription valid. Rotating the keys invalidates every subscription (browsers bind a
  subscription to the key), so there is no rotation command.
- JWT: header `{"typ":"JWT","alg":"ES256"}`, claims `{"aud": "<scheme>://<host>[:port]
  of the endpoint>", "exp": now + 12 h, "sub": <vapid_subject>}`. The signature is the raw
  64-byte `R‖S` (each left-padded to 32 bytes), **not** ASN.1 DER as `ecdsa.SignASN1` returns.
  Tokens are cached per audience and reused until 1 hour before `exp`.
- `vapid_subject` defaults to `hub.public_url` (an `https:` URL). **Uncertain:** Apple has been
  reported to reject `sub` values it considers invalid (`BadJwtToken`); if a real iPhone
  test fails with 403 `BadJwtToken`, set `vapid_subject` to `mailto:<your email>`.
- Header: `Authorization: vapid t=<jwt>, k=<public key b64url>`.

### 6.3 Encryption (RFC 8291 / RFC 8188 `aes128gcm`)

Inputs from the subscription: `ua_public` (`keys.p256dh`, 65 bytes) and `auth_secret`
(`keys.auth`, 16 bytes). Per message:

```
as_private, as_public = new ephemeral P-256 key pair          (65-byte uncompressed public)
salt                  = 16 random bytes
ecdh_secret           = ECDH(as_private, ua_public)
key_info              = "WebPush: info" ‖ 0x00 ‖ ua_public ‖ as_public
IKM                   = HKDF(salt = auth_secret, ikm = ecdh_secret, info = key_info, 32)
PRK                   = HKDF-Extract(salt = salt, ikm = IKM)
CEK                   = HKDF-Expand(PRK, "Content-Encoding: aes128gcm" ‖ 0x00, 16)
NONCE                 = HKDF-Expand(PRK, "Content-Encoding: nonce" ‖ 0x00, 12)
plaintext record      = payload ‖ 0x02 ‖ 0x00 * pad           (0x02 = last record)
ciphertext            = AES-128-GCM(CEK, NONCE, record)       (16-byte tag appended)
body                  = salt(16) ‖ rs(4, big-endian, 4096) ‖ idlen(1) = 65 ‖ as_public(65) ‖ ciphertext
```

- `pad` brings `len(payload) + 1` up to the next multiple of 128 bytes, so the push service
  learns only a coarse size. The payload is at most 3 000 bytes of JSON (the record limit is
  4096 − 86 header − 16 tag − 1 delimiter = 3 993); text fields are cut before encoding.
- `rand` is injectable (an `io.Reader` field) so the RFC example can be reproduced exactly.
- Keys are validated on registration (`POST /v1/push/targets`): `p256dh` must decode to a
  point on P-256 (`ecdh.P256().NewPublicKey`), `auth` to 16 bytes.

### 6.4 The request

```
POST <endpoint>
Authorization: vapid t=…, k=…
Content-Encoding: aes128gcm
Content-Type: application/octet-stream
TTL: <seconds>
Urgency: high | normal | low
Topic: <first 22 chars of base64url(SHA-256(tag))>
```

| Alert | TTL | Urgency |
| --- | --- | --- |
| session triggers, summaries | 21 600 (6 h) | high |
| `limit_95` | until the window resets, at most 86 400 | high |
| `limit_80`, `machine_silent` | 21 600 | normal |
| `reset` | 3 600 | low |
| usage and switch notices (3.9) | 86 400 | low |
| test | 60 | high |

`Topic` (RFC 8030 5.4) makes the push service replace an undelivered message with the same
topic, so a phone that was offline gets the latest state of a session, not every step.
No redirects are followed; the client timeout is 10 s.

### 6.5 The payload (shared with the service worker)

The plaintext is one JSON object. pwa.md 5 is its consumer; `internal/hub/push/model`
holds the Go type.

```json
{
  "v": 1,
  "type": "alert",
  "alert": "al_01JA3B…",
  "alerts": ["al_01JA3B…"],
  "trigger": "asked",
  "title": "work · api · asked a question",
  "body": "Should I also update the migration?",
  "tag": "wy-s-c41e0000-0000-4000-8000-000000000003",
  "url": "/#/sessions/c41e0000-0000-4000-8000-000000000003?alert=al_01JA3B…",
  "remote_url": "https://claude.ai/code/session_…",
  "tap": "auto",
  "session": "c41e0000-0000-4000-8000-000000000003",
  "needs_you": 3,
  "actions": {"snooze": "a1.…", "seen": "a1.…"},
  "private": false,
  "ts": "2026-10-12T14:04:01.000Z"
}
```

| Field | Meaning |
| --- | --- |
| `category` / `channel` | The trigger's category and Android channel id (5.4); absent from a hub before v25. |
| `type` | `alert`, `summary` or `test`; `refresh` only in a link `refresh` frame (13.5: never shown, not this object's other fields). |
| `alert` / `alerts` | The alert (for `summary`: the first) and the alerts in the push, at most 20 ids: a larger summary lists the first ones (and fewer if the payload would not fit 3 000 bytes); the app refetches the list. |
| `tag` | Notification tag: `wy-s-<session>`, `wy-l-<seat>-<window>`, `wy-r-<seat>-<window>`, `wy-m-<machine>`, `wy-u-<trigger>-<seat or model>`, `wy-summary`, `wy-test`. The same tag replaces the earlier notification. |
| `url` | Deep link, relative to the hub origin. Private: `/#/alerts/<alert>`. Summary: `/#/`. |
| `remote_url` | The session's Remote Control link, when known (5.8), also in private mode (the payload is encrypted; it is never displayed). |
| `tap` | The device's `prefs.tap`. |
| `needs_you` | Sessions needing you at send time, for the app icon badge (`navigator.setAppBadge`). |
| `actions` | Action tokens (section 8) for Snooze and Seen; absent for `test` and summaries. |

### 6.6 Endpoint allow-list (SSRF guard)

A push-scoped token chooses the URL the hub will POST to, so endpoints are checked on
registration: `https://` only, port 443, and the host must match `push.settings.webpush_hosts`,
default:

```
fcm.googleapis.com, *.googleapis.com, web.push.apple.com, *.push.apple.com,
updates.push.services.mozilla.com, *.push.services.mozilla.com, *.notify.windows.com
```

Others get 400 "unknown push service host; the owner can add it to webpush_hosts". The send
client also refuses to connect to non-public addresses after DNS resolution (a custom
`net.Dialer.Control`, shared with ntfy as `internal/hub/push/netguard`): loopback, private,
link-local, unspecified, multicast, carrier-grade NAT (100.64.0.0/10), `0.0.0.0/8`,
`198.18.0.0/15`, `240.0.0.0/4` and the NAT64 prefix `64:ff9b::/96`. It follows no redirect.
The service worker likewise ignores a payload deep link that does not start with a single `/`
or that contains a backslash (browsers read `/\` as `//`).

## 7. ntfy

### 7.1 Targets

An ntfy target is `{server, topic, token?, name?, private?}`:

- `server`: default `https://ntfy.sh`. The **owner** may name any `https://` URL, or `http://`
  for loopback, private, and tailnet addresses (the `wy connect` rule, tokens.md 4), so a
  self-hosted ntfy keeps working. A **push-scoped device** may name only an `https://` server
  on a public DNS name or public address (no loopback, private, tailnet, single-label or
  `.local`/`.internal` hosts), otherwise 400. The hub's ntfy client mirrors this at connect
  time: targets with a `device_id` use the dial guard of 6.6 (public addresses only), the
  owner's own targets (`device_id` NULL) may reach any address; neither follows redirects.
- A device may hold at most **10** push targets of any kind (409 `conflict` on the 11th;
  re-adding one of its own is not a new target).
- `topic`: when omitted the hub makes one: `wy-` + 24 base62 characters (about 143 bits).
  Topics on ntfy.sh are readable by anyone who knows the name, so the name is the secret
  and is shown only to the owner and the device that made it.
- `token`: optional ntfy access token (`tk_…`), sent as `Authorization: Bearer`; stored in
  `push_target.secret`, never returned by the API (the API shows `token_set: true`).
- Who adds one: a phone for itself (`POST /v1/push/targets` with its push scope; the target
  gets its `device_id` and follows its prefs) or the owner for the hub (`device_id` NULL;
  prefs on the target). Private defaults to true on `ntfy.sh` (5.5).
- The web app's Settings shows how to subscribe: the server and topic, and an
  `ntfy://<host>/<topic>` link (**uncertain** whether every ntfy app version handles it;
  the server/topic text always works).
- Setup note: set `hub.public_url` first. Without it ntfy alerts have no click link and no
  Snooze/Seen buttons (7.2).

### 7.2 Message

JSON publishing to the server root, which keeps
UTF-8 titles intact:

```json
{
  "topic": "wy-4fQx0Lr2…",
  "title": "work · api · asked a question",
  "message": "Should I also update the migration?",
  "priority": 4,
  "tags": ["question"],
  "click": "https://hub.example.ts.net/#/sessions/c41e…?alert=al_01JA3B…",
  "actions": [
    {"action": "view", "label": "Open in Claude", "url": "https://claude.ai/code/session_…", "clear": true},
    {"action": "http", "label": "Snooze 15 min", "url": "https://hub.example.ts.net/v1/actions/a1.…", "method": "POST", "headers": {"Content-Type": "application/json"}, "body": "{}", "clear": true},
    {"action": "http", "label": "Seen", "url": "https://hub.example.ts.net/v1/actions/a1.…", "method": "POST", "headers": {"Content-Type": "application/json"}, "body": "{}", "clear": true}
  ]
}
```

- `click` is the hub deep link (or the Remote Control link when `tap` is `claude`). The first
  action is **Open in Claude** when the session has a Remote Control link, else **Open** with
  the deep link. ntfy allows three actions.
- `http` actions are run by the ntfy app on the phone, so they need the phone to reach the
  hub (the tailnet), like opening the app does. When they fail, the ntfy app shows the error.
- Priority: 4 for session triggers, `limit_95` and summaries; 3 for `limit_80` and
  `machine_silent`; 2 for `reset` and the usage notices. Tags (ntfy emoji): `lock`
  (permission), `question` (asked), `rotating_light` (failed), `warning` (limits), `zzz`
  (silent), `bar_chart` (usage notices), `hourglass_flowing_sand` (drain notices), `bell` (summary).
- Private: title and body as 5.5, `click` = `/#/alerts/<id>` deep link, no Remote Control
  action (ntfy sees the text, unlike Web Push), Snooze and Seen kept. Their URLs reveal the hub's host name and a
  24-hour action token to the ntfy server (a leaked token can only snooze or mark that one alert seen, 24 hours); that is the price of
  the buttons, and security.md tells the owner.
- **Without `hub.public_url`** (`wy hub serve --public-url https://…`, pairing.md 2.3) the hub
  has no absolute address to put in a message: ntfy alerts carry **no `click` link and no
  Snooze/Seen buttons**, only the title, the body and "Open in Claude" when the session has a
  Remote Control link. Set the public URL to get the tap-through and the buttons.
- Messages are at most 4 096 bytes (ntfy's limit); the builder cuts the body first.
- **iOS caveat.** Tapping a link from the ntfy app opens Safari, which on iOS is not the
  installed web app and has no pairing cookie: the deep link lands on the pair screen. On
  iOS the useful ntfy taps are "Open in Claude" and the Snooze/Seen buttons. Android opens
  links inside the installed web app (Chrome captures links in its scope).

### 7.3 Responses

200 is `sent`. 429 (ntfy.sh's daily and burst limits) and 5xx are retried (5.6). 401/403/413
fail without retry. ntfy has no "gone" state: a topic always accepts. The reply body is never
kept or shown: `last_error` and the `/test` result carry only the outcome and `HTTP <status>`
(transport errors are reduced to `timeout`, `canceled`, `refusing to connect to a non-public
address` or `connection failed`), so a hub is not an oracle for internal services.

## 8. Action tokens (Snooze and Seen without a session)

Buttons in a push must work without the app's cookie: the ntfy app has none, and a service
worker may run when the cookie is not usable. Each push carries one token per action.

```
a1.<base64url(payload)>.<base64url(mac)>
payload = alert id ‖ 0x00 ‖ action ("snooze" | "seen") ‖ 0x00 ‖ fire_count (uint32) ‖ target id ‖ 0x00 ‖ expires (uint64 unix seconds)
mac     = first 16 bytes of HMAC-SHA256(action_key, "a1" ‖ payload)
```

- `action_key`: 32 random bytes in `hub_secret`, made at first use.
- `POST /v1/actions/{token}` (no other credential; GET is 405 so link previews and
  prefetching can never act): at most 30 requests a minute per client IP (429 with
  `Retry-After`); verify the MAC (constant time) and `expires` (fire time + 24 h) **before**
  any write transaction, with the key cached in memory, so a flood of bad tokens never takes
  the write lock; then check that the alert exists. Then:
  - `seen`: idempotent; applies 4's seen rules.
  - `snooze`: applies only if `alert.fire_count` equals the token's `fire_count` and the alert
    is `fired`; otherwise it does nothing. So a token snoozes at most once (a replay or a
    second tap after the re-push is a no-op), which is the "one-time" property without a
    table of used tokens.
  - Reply 200 `{"alert": AlertBrief, "applied": bool}`; 400 malformed; 401 bad MAC;
    410 expired; 404 unknown alert.
- `seen_by` records the target's device (or `ntfy:<target id>`).
- A leaked token (ntfy.sh sees the action URLs) can only snooze or mark seen one alert for 24
  hours.

## 9. Schema v3

The implementer adds `internal/hub/store/migrations/0003_push.sql` with exactly this, updates
`docs/spec/schema.sql` to v3 (the full schema, with a `v3` line in its header), and the
schema test keeps comparing the two. `pair_code`, `channel`, `push_target`, `rule`, `alert`
and `delivery` have existed since v1 but nothing has ever written them, so they are dropped
and recreated in their final shape rather than altered (SQLite cannot change a CHECK or a NOT
NULL in place). `usage_window.alerted_80` / `alerted_95` stay but are unused: the alert
episode keys are the record of what fired.

```sql
-- Version 3: pairing, owner sessions, push targets, rules, alerts, deliveries.
-- push.md section 9. The v1 placeholders for these tables were never written, so they are
-- recreated with their final shape.

DROP TABLE delivery;
DROP TABLE alert;
DROP TABLE rule;
DROP TABLE push_target;
DROP TABLE channel;
DROP TABLE pair_code;

-- Owner sessions expire (pairing.md 4); NULL = never.
ALTER TABLE device ADD COLUMN expires_at INTEGER;
-- Per-device push preferences (push.md 5.4).
ALTER TABLE device ADD COLUMN prefs TEXT CHECK (prefs IS NULL OR json_valid(prefs));

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
  kind          TEXT    NOT NULL PRIMARY KEY CHECK (kind IN ('webpush', 'ntfy')),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  settings      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  updated_at    INTEGER NOT NULL
) STRICT;
INSERT INTO channel (kind, enabled, settings, updated_at) VALUES
  ('webpush', 1, '{}', 0),
  ('ntfy', 1, '{}', 0);

-- One place a push goes: a browser subscription or an ntfy topic.
CREATE TABLE push_target (
  id            TEXT    NOT NULL PRIMARY KEY,      -- 'pt_' + ULID
  kind          TEXT    NOT NULL REFERENCES channel(kind),
  device_id     TEXT    REFERENCES device(id),     -- NULL: an ntfy topic added by the owner for the hub
  name          TEXT,                               -- "iPhone · Safari", "ntfy.sh/wy-4fQx…"
  endpoint      TEXT    NOT NULL,                   -- webpush: endpoint URL; ntfy: server URL + '/' + topic
  keys          TEXT    CHECK (keys IS NULL OR json_valid(keys)),     -- webpush: {"p256dh": "…", "auth": "…"} base64url
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
  reason        TEXT,                               -- answered, ended, superseded, stale, rule_off, remote_control, peers, baseline, recovered, reset_passed, direct
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
  UNIQUE (alert_id, target_id, fire)
) STRICT;
CREATE INDEX delivery_next ON delivery(next_at) WHERE next_at IS NOT NULL;
CREATE INDEX delivery_batch ON delivery(batch);
```

Retention: `wy hub prune` and the daily maintenance delete alerts (and their deliveries)
older than 365 days, gone targets after 30 days, and pair codes 30 days after they expire.

## 10. Engine structure

```
internal/hub/push/model      types shared by everything below and the API: Trigger, Alert, AlertBrief,
                             Prefs, QuietHours, Message, Payload (6.5), Target, Result, Sender
internal/hub/push/clock      Clock (Now, Timer) + Fake for tests
internal/hub/push/rules      pure: trigger table, episode keys, rule matching, due/stale, quiet-hour
                             arithmetic, reconcile decisions, text rendering (5.5), who-has-room
internal/hub/push/engine     scheduler, reconcile I/O, fan-out, batching, flood cap, retries, snooze/seen,
                             baseline, awake_at; owns no HTTP
internal/hub/push/action     action tokens (section 8)
internal/hub/push/webpush    VAPID, RFC 8291 encryption, Sender; webpushtest: fake push service + decoder
internal/hub/push/ntfy       Sender; ntfytest: httptest stub
```

```go
// model.Sender is implemented by webpush and ntfy.
type Sender interface {
    Kind() string                                   // "webpush" | "ntfy"
    Send(ctx context.Context, t Target, m Message) Result
}
type Result struct {
    Outcome    Outcome        // Sent, Retry, Gone, Failed
    HTTPStatus int
    RetryAfter time.Duration  // from Retry-After, 0 if none
    Err        string         // first 200 bytes of the reply or the transport error
}
```

`engine.Engine` is created by `wy hub serve` with the store, the clock, the senders and the
`events.Broker`; `api.Server` calls `engine.Notify(ingest.Changes)` after publishing a batch
(non-blocking; changes are merged while the engine is busy), and the alert handlers call
`engine.Snooze`, `engine.Seen`, `engine.Act(token)`. A nil engine (tests, `--no-push`) makes
the alert endpoints answer 503.

Logging: one line per fire (`alert fire al_… trigger=asked session=c41e… targets=2`), per
non-2xx delivery (`push fail pt_… 410 gone`), and per skip reason other than `baseline`;
never the payload text.

### 10.1 Design decisions

Where the sections above left a case open, the engine does this:

- **Batching (5.3)** looks ahead only when something is due now: alerts due within `batch_s`
  of a due alert go out in the same push; nothing is pushed early on its own.
- **Snooze past `max_age_s`**: a snooze whose re-push time is overdue (the hub slept) goes back
  to `fired` without a re-push, like a stale pending alert is skipped `stale`. Its
  `fire_count` is incremented so the snooze token of the earlier fire cannot snooze it again.
- **Reset alerts** are created at reconcile time, due at `resets_at`, only when the reset rule
  is enabled and the peak reached 80%; a fired reset alert closes 1 h after `resets_at`
  (`reset_passed`). A reset alert found at baseline stays `pending` (its push is in the future).
- **Summaries of mixed triggers** (limits with sessions) are titled `N alerts`; the private
  variant is `N things need you`. Only all-session summaries say `N sessions need you`.
- **Targets that stop being eligible** (disabled, channel off) between fan-out and send get
  their delivery `skipped` with reason `channel_off`; a retry whose alert has ended or moved to
  another fire is `skipped` `resolved`; a retry older than `max_age_s` past its first attempt is
  `skipped` `stale`.
- **Held deliveries** (quiet hours) are released together as one push at the end of the quiet
  window and are not counted against the flood cap a second time.
- **Crash safety**: a delivery being sent carries `next_at = now + 15 s`, so a hub that dies
  mid-send retries it after restart instead of losing it.
- **Quiet hours on a fall-back night**: a local end time that happens twice is its first
  occurrence (the spring-forward gap rule of 5.2 is unchanged).
- **ntfy private flag**: a target's own `prefs.private` wins over its device's prefs, then the
  ntfy.sh default (5.5), then the hub default.
- **`Session.alert`** is the alert of the session's current episode while it is open
  (`pending`, `fired`, `snoozed`, `seen`) or `skipped` (any reason: `stale`, `rule_off`,
  `peers`, `remote_control`, `baseline`); otherwise null. A skipped alert of the current
  episode belongs to an open episode by definition (a new state or `since` is a new episode),
  so it is the row Seen acts on (4). Hubs up to 1.1.0 left a skipped alert out, so its
  Needs-you row had no brief and could not be marked seen.
- **`PUT /v1/push/settings`** stores the object as sent over the defaults; a missing
  `quiet_hours.tz` is not frozen to the current hub zone (the response shows the effective one).
- **`POST /v1/rules`**: `delay_s` and `bypass_quiet` default to the base rule of the same
  trigger; `enabled` defaults to true. A base rule's `scope` may only set `windows`.
- **`GET /v1/actions/{token}`** answers 404 (the `/v1/` fallback), not 405; only POST acts.

## 11. Tests

### 11.1 Rules engine (fake clock, real store in a temp directory)

Each row starts from an empty hub at T = 0 with one webpush target (fake sender) and default
rules unless stated; the "events" column lists ingested records (with their `ts`) and clock
moves; "expect" lists sends and final alert status.

| # | Scenario | Events | Expect |
| --- | --- | --- | --- |
| 1 | Question pushed after 1 min | Stop(question) ts 0, arrives 0.5 s; advance to 59 s; to 60 s | Nothing at 59 s; one push at 60 s; status `fired` |
| 2 | Answered at 40 s | as 1; UserPromptSubmit ts 40 s arrives 42 s; advance to 120 s | No push; `cancelled` / `answered` |
| 3 | Permission 30 s | PermissionRequest 0; Notification(permission_prompt) 0.2 s; advance 30 s | One push at 30 s (same episode) |
| 4 | Permission answered by Stop | as 3; Stop ts 10 s | `cancelled` / `answered` |
| 5 | Asked then permission | asked at 0; PermissionRequest at 20 s | asked alert `cancelled` / `superseded`; permission alert pushes at 50 s |
| 6 | Failure immediate | StopFailure(rate_limit) | Push in the same pass; body names the seat with most room |
| 7 | Overloaded is not a failure push | StopFailure(overloaded) | No alert |
| 8 | Repeat failure same episode | StopFailure ×2 (different errors) | One push |
| 9 | Late record (outbox) | Stop(question) ts 0 arrives at 50 s | Push at 60 s (due = clamp(60, 50, 110)) |
| 10 | Very late record | Stop(question) ts 0 arrives at 30 min | `skipped` / `stale`, no push |
| 11 | Clock-skewed machine | Stop(question) ts = +10 min (future), arrives 0 | Push at 60 s (clamped) |
| 12 | Limit 80 once | limit samples 79, 81, 85, 82 in one window | One push at the 81 sample |
| 13 | Limit jump past 95 | first sample 97 | One push (95); 80 `skipped` / `superseded` |
| 14 | New window | 81 in window A; window A resets; 81 in window B | Two pushes (one per window) |
| 15 | Stale sample | sample at 85 with ts 2 h old | No alert |
| 16 | Quiet hours hold | quiet 23:00–08:00; asked at 23:10 | Delivery `held`; at 08:00 one push if still asked; `skipped` / `resolved` if answered at 02:00 |
| 17 | Failure bypasses quiet | quiet on; StopFailure(billing_error) at 01:00 | Pushed at once |
| 18 | Device quiet without critical | device quiet `critical: false`; failure at 01:00 | Held for that device only; another device without quiet gets it |
| 19 | Snooze | fired at 60 s; snooze at 70 s; still asked at 16 min 10 s | Second push "Still waiting"; `fire_count = 2` |
| 20 | Snooze then answered | as 19; answered at 5 min | No second push; `closed` / `answered` |
| 21 | Seen | fired; seen; 30 min pass | No more pushes; `seen`, then `closed` when answered |
| 22 | Action token replay | snooze token used twice | Second call `applied: false` |
| 23 | Batch | 3 sessions asked within 1 s | One summary push "3 sessions need you" with 3 deliveries in one batch |
| 24 | Flood cap | 10 limit alerts in one minute | 6 pushes, then one summary when the window frees |
| 25 | Muted trigger | device mutes `limit_80` | Delivery `skipped` / `muted` for that device only |
| 26 | Rule off during delay | asked at 0; owner disables `asked` at 30 s | `cancelled` / `rule_off` at once (3.6 step 1b), nothing at 60 s |
| 27 | Override | override `asked` delay 300 for project `infra` | `infra` pushes at 5 min, others at 1 min |
| 28 | Remote Control skip | `skip_remote` on; session with `remote_url` asks | `skipped` / `remote_control` |
| 29 | Hub restart mid-delay | asked at 0; engine stopped at 20 s, restarted at 40 s | Push at 60 s (timer from the table) |
| 30 | Hub down past max age | asked at 0; engine down 0–20 min | No push after restart; `skipped` / `stale` |
| 31 | Machine silent | session working; machine sends nothing; advance 61 min | One push; record arrives → `closed` / `recovered`; silent again 1 h → a second episode |
| 32 | Hub was down, not the machine | machine last seen T; hub down T…T+2 h; restart | No silent push before `awake_at + 1 h` |
| 33 | Laptop suspend | wall clock jumps 3 h, monotonic does not | Treated as restart: overdue alerts stale, silence from `awake_at` |
| 34 | Baseline | first start with 2 sessions asked and a window at 90% | 3 `skipped` / `baseline`; nothing sent |
| 35 | Refold | late record makes an `asked` session `working` retroactively | Pending alert `cancelled` |
| 36 | Gone target | fake service returns 410 | Delivery `gone`, target disabled, next alert not sent to it |
| 37 | Retry | 503, 503, 201 | Sent on the third try at +15 s, +1 min; skipped if the session was answered in between |
| 38 | Private | device private | Payload title "Watchyard", url `/#/alerts/<id>`, no question text |
| 39 | Peer message answered | a peer message starts a turn (`peer_prompt`); it ends on a question | `skipped` / `peers`, nothing sent; the session is still `asked`, `needs_you` 1 |
| 40 | Waiting on a peer | a person's turn ends on a question with `peer_wait`; then the reply starts a turn that ends on a question | Both `skipped` / `peers`; nothing sent |
| 41 | Permission in a peer turn | peer turn, then PermissionRequest | Pushed at 30 s |
| 42 | AskUserQuestion in a peer turn | peer turn, then PreToolUse AskUserQuestion | Pushed at 60 s |
| 43 | A person's turn after a peer turn | peer turn ends on a question; a person types; that turn ends on a question | Pushed 60 s after the second question |
| 44 | `skip_peers` off | as 39 with `skip_peers: false` | Pushed at 60 s |
| 45 | Wait during the delay | a person's turn ends on a question (pending); a Stop with `peer_wait` in the same episode | `cancelled` / `peers` at fire time; nothing sent |
| 46 | Usage notice | the job hands a `weekly_drop` notice (since 1 min ago) | One low-urgency push in the next pass; `closed` / `expired` 24 h after it fired |
| 47 | Old usage notice | a notice whose `since_at` is 7 h old | `skipped` / `stale`, nothing sent |
| 48 | Notice twice | the same notice handed in two runs | One alert |
| 49 | Notice rule off | `weekly_drop` rule disabled | `skipped` / `rule_off` |
| 50 | Seen on a skipped row | a session asked for 7 days (`skipped` / `stale`); another asked in a peer turn (`skipped` / `peers`) | Both overviews carry the skipped brief; Seen on each → `seen`, both leave `needs_you` and `counts.needs_you`; Snooze → 409 |
| 51 | Seen with no alert | hub without the engine; a session asks | `POST /v1/sessions/{id}/seen` makes a `seen` alert for the episode; the session leaves Needs you; a later engine pass makes no second alert; a session that does not need you → 409 |
| 52 | Switch notice | a machine sends a `switch_notice` (`session_moved`, 2 moves); a device mutes `session_moved` | One low-urgency push to the other targets, linked to `/#/machines?machine=<id>`, tag `wy-u-session_moved-<machine>`; the muted device's delivery `skipped` / `muted`; the same record again is one alert; a `switch_digest` is its own alert |
| 53 | Switch notice sent direct | `switch_notice` with `direct: true` | `skipped` / `direct`, nothing sent |
| 54 | Switch notice rule off | `session_moved` rule disabled | `skipped` / `rule_off` |
| 55 | Drain notices | the owner drains a seat until 4 h from now; later its week reaches 97 % | One `drain_started` (category `drain`, channel `wy_drain`), none to a device with `mute_categories: ["drain"]`; then one `drain_ended` (`aim`) and `drain_until` cleared; a drain whose time passed ends with `time` |
| 56 | Categories | a `session_moved` and a `drain_pulled` notice; a device mutes the `drain` category | `session_moved` on `wy_switch` to both; `drain_pulled` on `wy_drain` only to the other |

The detectors of 3.9 are pure (`internal/calib`, `watch.go`) and get table tests on synthetic
series: a server or owner reset (5-hour and weekly to 0 with the same weekly `resets_at`, three
sessions agreeing, stale sessions still at the old level, the 2026-10-17 pattern); the same with
only two sources (a `weekly_drop`); a single glitch reading (nothing); a weekly drop of 11 and of 9
points; a normal weekly reset (nothing); claude.ai's empty window without `resets_at`; the mask's
bounds; and `limit_change` with 2, 3 and 4 reference accounts.

`rules` (pure) gets its own table tests for episode keys, due/stale arithmetic, rule
matching, quiet-hour windows across midnight and DST (Europe/London 2026-03-29 and
2026-10-25, America/New_York), and the text table of 5.5 (golden files).

### 11.2 Web Push

- **RFC 8291 example** (section 5, intermediate values in appendix A): the given `ua_public`, `auth_secret`, `as_private` and
  `salt`, plaintext "When I grow up, I want to be a watermelon" → the exact body bytes given
  in the RFC (no padding in this test; padding is a separate option).
- **RFC 8292**: sign a JWT, verify it with `ecdsa.Verify` on `R‖S`; check `aud` for
  endpoints with and without ports; check the cached token is reused and renewed.
- **Fake push service** (`webpushtest.Server`): an `httptest` TLS server that holds a test
  subscription's private key, decrypts every request with an independent decoder written in
  the test package (RFC 8188 decryption), verifies the `vapid` header, and answers scripted
  statuses (201, 404, 410, 413, 429 + `Retry-After`, 500, slow). Used by the engine tests and
  the end-to-end test.
- **Independent check** (opt-in, `WY_WEBPUSH_NODE=1`): Node's `http_ece` package decrypts a
  body made by our encoder.

### 11.3 ntfy

- `ntfytest.Server` (`httptest`): records published JSON; asserts title, priority, tags,
  click, the three actions and the Bearer token; scripted 429/401/500.
- Opt-in real server (`WY_NTFY_DOCKER=1`): `docker run --rm -p 127.0.0.1:0:80
  binwiederhier/ntfy serve` (podman works the same), publish through the sender, read it back
  with `GET /<topic>/json?poll=1`, and check the actions survived. CI runs it on Linux only.

### 11.4 End to end (hub process)

`wy hub serve` in a test with the fake clock, the fake push service and an ntfy stub: pair a
phone (pairing.md), register a webpush target, ingest a Stop(question) through
`/v1/ingest` with a machine token, advance 60 s, decrypt the push the fake service received
and check it against 6.5; then repeat with a UserPromptSubmit at 40 s and check nothing
arrives; then cross 80% with three `limit_sample` records and check exactly one push.

## 12. (Removed) The Expo channel

It carried the native phone app's pushes through Expo's push service. It was removed in schema
v11, before any release; nothing of it remains in the hub. Widget refresh moved to the link
(13.5).

## 13. The link channel

Status: spec v1. The Android app's native notifications without Google or any
account: the hub queues each push on the device's **link** ([link.md](link.md)) and the app's
own service (14) posts it. Web Push and ntfy do not change. Code: `internal/hub/push/link` (the
sender), `internal/hub/link` (queue and transport). Schema v8.

### 13.1 Choice

| | Link (chosen) | Web Push from the installed web app |
| --- | --- | --- |
| Third parties | None: hub → phone over Tailscale (optionally Funnel) | Google's push service carries ciphertext; no account |
| Account needed | No | No |
| Offline phone | Durable queue, replayed in order from the last ack (link.md 5) | Push service TTL |
| Encryption | RFC 8291 to a Keystore key, hub-signed | RFC 8291 |
| Cost on the phone | A foreground service and one idle socket (battery use is measured) | None beyond FCM |

Web Push in the installed web app stays the backup; a phone can have both.

### 13.2 Sender

`internal/hub/push/link` implements the same sender interface as `webpush` and `ntfy`.

- The payload is 6.5's object built for the target exactly as for Web Push (the same `private`
  handling; `remote_url` is kept, since the body is sealed end to end), plus one field `origin`
  (the hub's `hub.public_url`) so the app can drop an action meant for another hub
  (phone.md P6.4).
- It calls `link.Enqueue(target.device_id, "alert", tag, ttl, payload, "d:" + the ids of the
  deliveries the push carries, comma-separated)` (link.md 5.2) with 6.4's TTL; a summary
  carries several. `tag` is `payload.tag` when it fits link.md 5.3's rule, else its Web Push
  `Topic` (6.3), and dismissals (13.3) use the same mapping. Sealing is RFC 8291 with the
  certificate's box key (`p256dh` = the phone's `box_pub`) and the target's `auth` through
  `webpush.Encrypt`, the function of 6.3, `padMultiple` 128.
- Results: enqueued → `sent`; no `device_key`, or the device revoked →
  `gone` (target disabled, as for Web Push 410); `ErrQueueFull` → `retry` (5.6's backoff).
- `sent` means queued for the phone, not on it: `delivery.delivered_at` (13.4) says that.

### 13.3 Dismissals and summaries

- When an alert becomes `seen` or `resolved` (section 4), the hub enqueues on every link target
  that received it a `dismiss` body (`{"v":1,"type":"dismiss","tag","alerts"}`) with the **same
  tag**, so it collapses an undelivered alert (link.md 5.3): a phone never shows an alert that
  was already handled, and Seen on one phone clears the others.
- Summaries (5.3) and tests are `alert` frames with `type` `summary` / `test`.
- Quiet hours, batching, the flood cap and device prefs apply unchanged (5.1–5.4): the link is
  only a transport.

### 13.4 Delivered

`delivery.delivered_at` (schema v8) is set when the phone acks the frame (link.md 5.6). The
board's delivery log shows "on phone 09:00:03" for link deliveries. The "zero missed alerts
in 7 days" goal is measured from it: a link delivery with `status = 'sent'`, older than its TTL,
with `delivered_at IS NULL` and `last_error` not `collapsed`, is a miss.

### 13.5 Widget refresh

The widgets redraw from `refresh` frames over the link. `refresh.Refresher` (`internal/hub/push/refresh`)
subscribes to the `events.Broker`, with session and usage changes and `alert` events
with `change = seen` (which lower the needs-you count) debounced 10 s. For each
enabled link target whose device has `prefs.widgets = true` and is not revoked, it computes a
digest: the needs-you count, the `used_pct` of each seat x watched window bucketed to 5 points,
and the next `resets_at`. When the digest differs from the last one sent to that target, and
the last refresh is at least `refresh_min` (default 15, 10-240) ago, it calls
`link.Enqueue(device, "refresh", "refresh", 900 s, snapshot)`. The tag keeps only the newest.
The body (link.md 6.4) is enough to redraw the widgets with no network call (phone.md P11).

- It also sends one when a watched window passes its `resets_at`, still subject to the minimum.
- Refreshes ignore quiet hours, are not alerts and write no `delivery` rows. A `gone` result
  disables the target as usual. Digests live in memory, so a restart sends at most one refresh
  per target.
- It sends nothing while the hub is frozen (`hub.frozen`); a skipped pass looks again 10 s later.
- It logs one line per refresh pass with the count, never the digest.

### 13.6 Settings

- `PUT /v1/push/channels/link` (owner) takes `settings: {urls?: [string], ping_s?: integer}`:
  the URLs devices try (default `[wss://<hub.public_url host>/v1/link]`) and the default ping
  (180, range 60–900). They reach devices in the claim reply and in `welcome` (link.md 3.3).

### 13.7 Registration

A phone's `link` target is made by the hub, never posted by the app:

- At a claim with keys (keys.md 7.1) by a phone: `push_target {kind: "link", device_id,
  endpoint: "link:" + device id, keys: {"p256dh": b64u(box_pub), "auth": b64u(auth)}, name:
  "<model> · app"}`, in the claim's
  transaction.
- A phone paired before the link existed sends `PUT /v1/devices/self/keys` with the claim's `keys` object
  (`reg = tofu`), which makes the certificate and the target.
- New keys replace the old target (same `endpoint`) and certificate. The cap of 10 targets per
  device counts it.
- `POST /v1/push/targets` with `kind: "link"` is refused (400 "link targets are made by
  pairing"). `PATCH` and `DELETE /v1/push/targets/{id}` work as for other kinds; the app's
  "Instant delivery" switch is the target's `enabled`.
- `PushTarget` for `link` adds `link: {connected, last_seen_at, queued}` (link.md 9).

### 13.8 API and schema (v8)

**api.yaml:** `Channel.kind`, the `/v1/push/channels/{kind}` enum, `PushTarget.kind` and
`Delivery.kind` gain `link`; `Delivery` gains `delivered_at`; `PushTarget` gains `link`;
`PairClaim` gains `keys` and `keys_sealed`; `PairClaimReply` gains `hub_key`, `owner_key`,
`cert`, `link` and `reg` (keys.md 7). `PushTargetNew.kind` does not gain `link` (13.7).

**Migration** `0008_link_channel.sql`: `channel` and `push_target` are rebuilt with their rows
(SQLite cannot change a CHECK) so `kind` may be `link`; the `link` channel row is seeded,
enabled; `delivery.delivered_at INTEGER` is added.

**Go model:** `KindLink = "link"`, `TypeDismiss = "dismiss"`, `Payload.Origin`.

### 13.9 Tests

- Sender: the payload equals the Web Push payload for the same target plus `origin`; RFC 8291
  vectors through the link path; `gone` without keys; `retry` on a full queue.
- A dismiss collapses an unacked alert with the same tag (the test phone client sees only the
  dismiss); Seen on one device enqueues dismisses for the other link devices.
- `delivered_at` is set on ack, not on enqueue; the missed-alert query uses it.
- The widget refresher sends to link targets only, at most one frame per `refresh_min`.

## 14. The Android `wy-link` module

A local Expo module in `phone/modules/wy-link` (Kotlin, `expo-modules-core`), autolinked by the
app. It owns everything that must run without the React Native runtime: the foreground service,
the link client, the Keystore keys, notifications and their actions, the watchdog and the
updater (phone.md P16). JavaScript only configures it and reads its state.

### 14.1 Layout

```
phone/modules/wy-link/
  expo-module.config.json      {"platforms":["android"],"android":{"modules":["dev.watchyard.link.WyLinkModule"]}}
  index.ts                     the JS API (14.8); every function rejects "wy-link: Android only" elsewhere
  src/WyLink.types.ts
  android/build.gradle         okhttp (the version React Native already ships), androidx.work, androidx.core,
                               com.google.crypto.tink:tink-android (Ed25519 verification only)
  android/src/main/AndroidManifest.xml
  android/src/main/java/dev/watchyard/link/
    WyLinkModule.kt            Expo module: functions and events
    WyLinkService.kt           foreground service (specialUse)
    LinkClient.kt              OkHttp WebSocket and long-poll, login, acks, backoff
    Frames.kt                  parsing, hub signature checks, seq bookkeeping
    Crypto.kt                  Keystore keys, RFC 8291 decryption, login signature
    Notifier.kt                channels, posting, ids, dismissals, actions
    Receivers.kt               BootReceiver (BOOT_COMPLETED, MY_PACKAGE_REPLACED), AlarmReceiver
                               (the exact-alarm restarts and retries, 14.4), ActionReceiver (Snooze, Seen)
    Watchdog.kt                WorkManager periodic worker
    Store.kt                   the module's private SharedPreferences
    Updates.kt                 the updater's pure half: manifest parsing, version and build compare,
                               the GitHub release filter, the signing-certificate decision (SigningSource)
    UpdateClient.kt            checkUpdate: the hub first, then GitHub (Fetcher: OkHttp, faked in tests)
    UpdateInstaller.kt         installUpdate: download, SHA-256, certificate check through PackageManager,
                               PackageInstaller session; InstallResultReceiver opens Android's confirmation
  android/src/test/…           JVM tests (14.10)
```

As built, the Kotlin is split a little further: `Codec.kt` (b64u, base32, fingerprints),
`Keystore.kt` (the two aliases of 14.3; `Crypto.kt` keeps RFC 8291, HKDF, the claim seal and
the Tink verifier), `Backoff.kt`, `Channels.kt`, `Actions.kt` (the 14.6 order), `Pairing.kt`
(keys.md 7.3's checks) and `Foreground.kt` (`goForeground()`'s API branches), so each piece has
a JVM test without Android classes.

Hub signatures (Ed25519) are verified with Tink's `Ed25519Verify` (pure Java, maintained by
Google, no native code) on every API level: the platform's `Signature("Ed25519")` exists only
from API 33, and one code path is simpler than two. No hand-written crypto and no other new
dependency; ECDH, HKDF and AES-GCM come from the platform.

### 14.2 Manifest

```xml
<uses-permission android:name="android.permission.INTERNET"/>
<uses-permission android:name="android.permission.ACCESS_NETWORK_STATE"/>
<uses-permission android:name="android.permission.FOREGROUND_SERVICE"/>
<uses-permission android:name="android.permission.FOREGROUND_SERVICE_SPECIAL_USE"/>
<uses-permission android:name="android.permission.RECEIVE_BOOT_COMPLETED"/>
<uses-permission android:name="android.permission.WAKE_LOCK"/>
<uses-permission android:name="android.permission.REQUEST_IGNORE_BATTERY_OPTIMIZATIONS"/>
<uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES"/>
<uses-permission android:name="android.permission.SCHEDULE_EXACT_ALARM" android:maxSdkVersion="32"/>
<uses-permission android:name="android.permission.USE_EXACT_ALARM"/>
<application>
  <service android:name=".WyLinkService" android:exported="false"
           android:foregroundServiceType="specialUse">
    <property android:name="android.app.PROPERTY_SPECIAL_USE_FGS_SUBTYPE"
              android:value="Keeps an encrypted connection to the user's own self-hosted server to receive its notifications; no push service is used."/>
  </service>
  <receiver android:name=".BootReceiver" android:exported="true">
    <intent-filter>
      <action android:name="android.intent.action.BOOT_COMPLETED"/>
      <action android:name="android.intent.action.MY_PACKAGE_REPLACED"/>
    </intent-filter>
  </receiver>
  <receiver android:name=".ActionReceiver" android:exported="false"/>
  <receiver android:name=".AlarmReceiver" android:exported="false"/>
</application>
```

`AlarmReceiver` receives the exact alarms of 14.4 (the `onTaskRemoved` restart and reconnect
timers over 10 s): an alarm's `PendingIntent` must target a component, and a broadcast lets the
receiver start the service (allowed from an exact alarm) or tell a running one to retry now.

- **`specialUse`, not `dataSync`:** `dataSync` is capped at 6 hours a day from Android 15 and
  may not start from `BOOT_COMPLETED`; ntfy's Android app uses `specialUse` for the same need.
  The app is never in the Play Store, so Play's review of the subtype text
  does not apply.
- **`minSdkVersion` 31** (phone.md P13), and only for one reason: a **non-exportable** key
  agreement key (`KeyProperties.PURPOSE_AGREE_KEY`, `KeyAgreement("ECDH", "AndroidKeyStore")`)
  exists from API 31; below it the RFC 8291 private key would have to live in app memory, which
  is ruled out. The foreground service type is **not** a reason: `specialUse` is an API
  34 type, and on 31–33 the service simply runs without one (next point), as ntfy does with a
  far lower minSdk.
- **Starting in the foreground, per API level.** API 34+: `startForeground(id, n,
  FOREGROUND_SERVICE_TYPE_SPECIAL_USE)`. API 31–33: the two-argument `startForeground(id, n)`
  (those releases do not know `specialUse`, and have no `dataSync` cap either). One helper,
  `WyLinkService.goForeground()`, with a test per branch.
- **Background starts (API 31+).** Android 12 forbids starting a foreground service from the
  background except in listed cases. The ones this design uses: `BOOT_COMPLETED` and
  `MY_PACKAGE_REPLACED`; the app being exempt from battery optimisation; an exact alarm
  firing. So the watchdog and `onTaskRemoved` restarts work only with the battery exemption or
  through an exact alarm; without either, `ForegroundServiceStartNotAllowedException` is caught,
  the 15-minute check of 14.9 runs instead, and Settings → Notifications says "Instant delivery
  is paused until you open the app (battery optimisation is on)".
- **Exact alarms.** `USE_EXACT_ALARM` (granted at install on API 33+; Play restricts it to alarm
  apps, which does not matter outside the Play Store) and `SCHEDULE_EXACT_ALARM` for 31–32 (on
  by default there). `canScheduleExactAlarms()` false → inexact `setAndAllowWhileIdle`.
- `REQUEST_INSTALL_PACKAGES` is for the updater (phone.md P16).

### 14.3 Keys (Android Keystore)

Made by `prepareKeys()` before the claim (phone.md P2):

| Alias | Spec | Use |
| --- | --- | --- |
| `wy_link_sign` | EC P-256, `PURPOSE_SIGN`, SHA-256, not exportable | The login signature (link.md 3.2), ECDSA DER |
| `wy_link_agree` | EC P-256, `PURPOSE_AGREE_KEY`, not exportable | RFC 8291's ECDH with the frame's `as_public` (`KeyAgreement.getInstance("ECDH", "AndroidKeyStore")`) |

- StrongBox when present (`setIsStrongBoxBacked(true)`, retried without it), no user
  authentication: the service must work while the phone is locked after its first unlock.
- The rest of RFC 8291 (HKDF, AES-128-GCM) runs in Kotlin on the ECDH output; the shared secret
  is in app memory for one frame only.
- `prepareKeys()` returns `{sign: {alg: "p256", pub}, box: {alg: "p256", pub}, auth}` (b64u) for
  the claim. The auth secret leaves the phone only in that claim (sealed by the QR, keys.md 7.1)
  and is never shown. It is kept in `Store` as it is, not wrapped by another Keystore key: on
  its own it opens nothing (RFC 8291 also needs the non-exportable `wy_link_agree`), so a
  wrapping key would add an alias and no protection.
- Unpair (phone.md P3) calls `reset()`: the two aliases, the pins and the module's storage are
  deleted.

### 14.4 Service and connection

| Concern | Design |
| --- | --- |
| Start | `start()` from JS after pairing and on each app start while instant delivery is on; `BootReceiver` (boot, app update); the watchdog. `startForegroundService`, then `goForeground()` (14.2) within 5 s. |
| Persistent notification | Channel `wy_connection` (name "Connection (always on)", importance MIN, no badge, no sound; small icon `wy_notify_connection`, grey, no large icon, phone.md P6.1a): "Connected to hub · 2 min", "Reconnecting… (next try in 20 s)", "Not connected: \<reason>". Tapping it opens Now (`watchyard://now`), like any other notification, never a settings page; its **Settings** button opens Settings → Notifications (`watchyard://settings/notifications`, phone.md P7). Every notification the module posts has a content intent that names the app's launcher activity, so a tap always starts the app (a notification with none makes Samsung open the app's notification settings). It can be hidden in Android's settings. |
| Connection | OkHttp `WebSocket` to the first working `urls` entry, subprotocol `wy-link.1` (link.md 2), `pingInterval` = `ping_s` (default 180; Settings allows 300–600 on the tailnet), read timeout 0, connect timeout 15 s. `ping_s` is the claim reply's (or `setPingInterval`'s); the `welcome`'s `ping_s` is not applied over the phone's own choice. Long-poll (link.md 2.1) after 3 failed handshakes while `GET /health` answers; while on long-poll the WebSocket is tried again every 30 min. In long-poll mode, pending actions (14.6) go in the `up` frames of the poll requests. |
| Login | link.md 3: the challenge's hub signature and fingerprint are checked against the pin from pairing (`h=`); `hello` is signed with `wy_link_sign`. |
| Reconnect | 1, 2, 5, 10, 20, 30, 60 s, then 120 s, each ±20 % jitter; reset after a connection that lasted 60 s. `registerDefaultNetworkCallback`: a new default network (Wi-Fi ↔ mobile, a VPN such as Tailscale coming up) reconnects at once with the backoff reset. |
| Close codes | link.md 8. **4403**: stop the service and the watchdog, post "This phone was removed from the hub", emit `revoked` (JS shows Pair). **4426**: stop, emit `upgrade` (the updater checks). **4409**: this connection only; the newer connection is normally this same client's, so nothing reconnects at once, but a retry is still scheduled after 120 s as a safety net (a superseding connection that then died would otherwise leave the phone offline). **4401** and **4400** three times in a row: stop with status `error`. **4429** and **4503**: retry after 60 s. |
| Doze | The app asks once for the battery-optimisation exemption (`ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`), with the maker guide (phone.md P6.7). Reconnect timers longer than 10 s use `AlarmManager.setExactAndAllowWhileIdle` when exempt and `canScheduleExactAlarms()` (14.2), a coroutine delay otherwise. |
| Wake lock | `PARTIAL_WAKE_LOCK` only while one frame is verified, decrypted and posted, 10 s cap; never across the connection. |
| Task removed | `onTaskRemoved` sets an exact alarm 1 s ahead that restarts the service (an alarm is one of the allowed background starts, 14.2). |
| Watchdog | WorkManager `PeriodicWorkRequest` every 15 min, `KEEP`: with instant delivery on, start the service if it is not running (allowed only when battery-exempt, 14.2; otherwise it does the 15-minute check); with it off, do the 15-minute check (14.9). |
| Storage | `Store`, the module's own SharedPreferences (credential-protected; the service starts at `BOOT_COMPLETED`, which comes after the first unlock): origin, device id, urls, the hub key pin, `last`, the clock offset, the auth secret, pending actions, counters. The bearer token stays in SecureStore (phone.md P3); the service does not need it. |

### 14.5 Frames and notifications

Per frame, under the wake lock:

1. Check it in link.md 4.3's order with the pinned hub key: signature, `seq`, duplicate
   (`seq ≤ last`: ack, drop), `exp` with the welcome's clock offset (past: ack, drop).
   A bad signature → `reject` (link.md 6.2), count it, drop; `last` does not move.
2. Decrypt (RFC 8291) and parse; failure → `reject` as in step 1.
3. Act:
   - `alert` with `type` `alert`, `summary` or `test`: post on the channel of phone.md P6.1 for
     the trigger (`wy_test` for tests). **Notification id** = the first 4 bytes of SHA-256(tag)
     read **big-endian** as a signed int (`ByteBuffer.getInt`, Java's default), so a new alert
     with the same tag replaces the old one and the tag's `dismiss` cancels it. Group `wy`.
     Buttons when `actions` is present: Open (the app at `url`), "Snooze \<n> min", "Seen"
     (14.6); `<n>` is the hub's `snooze_min`, which the app passes with `setSnoozeMinutes`
     (default 15). The buttons are left off a payload whose `origin` is not the paired hub's.
     **Tap and Open** (native, no JS): `tap: "claude"` with an `https` `remote_url` opens that
     link; anything else opens the app at the payload's `url` mapped to the app link
     (`/#/sessions/x` → `watchyard://sessions/x`, phone.md P7). The `auto` rule's `/health`
     probe of phone.md P6.4 is not run natively. Private payloads carry the hub's private text
     (5.5). The small and large icons follow the trigger (phone.md P6.1a: the mark with a
     kind badge; `summary` and `test` by type, unknown triggers the plain mark). `when` = the payload's `ts`. A `missed` count emits `refresh` to JS.
   - `alert` with `type: "dismiss"`: cancel the tag's notification.
   - `refresh`: store the snapshot and ask the widgets to redraw (an `APPWIDGET_UPDATE`
     broadcast to the app's widget providers, which runs `react-native-android-widget`'s
     headless task), with no network call; the task reads it with `getRefresh()` and draws
     the stored snapshot with it applied (phone.md P11). Emits `refresh` to JS when the app runs.
   - Unknown `t` or `type`: ignore.
4. Store `last = seq`, then ack (batched, link.md 5.6).

A crash between posting and storing `last` replays the frame; the tag-derived id makes the
replay replace the notification, not stack it.

### 14.6 Actions

Snooze and Seen work with the app killed. `ActionReceiver` gets the alert id, action and token,
cancels the notification at once, then:

1. Link connected: send `{"t":"action","id","token"}`, wait up to 5 s for `action_result`.
2. Otherwise, or on timeout: `POST <origin>/v1/actions/<token>` (no bearer, body `{}`, 10 s;
   section 8).

`ActionReceiver` uses `goAsync()`, whose window is about 10 s: it hands the action to the
running service (which has the link), and only when the service is not running does it do
steps 2 and 3 itself, with a 9 s budget for both.
3. Otherwise: keep `{token, action, alert, at}` in `Store` (at most 50; dropped after 24 h,
   when the token expires) and send them over the link at the next connection.

A 401, 404 or 410 answer is final. A re-fired alert comes with fresh tokens. The JS response
handler of phone.md P6.4 is not involved for link notifications.

### 14.7 Verification state

| State | When |
| --- | --- |
| "Verified by QR" | `reg = qr`: the keys went in sealed by the QR and the hub key matched `h=`. |
| "Not verified: compare check code" | `reg = tofu` (typed code, or keys added after pairing). The check code is the first 8 characters of the device's sign key fingerprint (keys.md 2), also shown on the board's Devices list. |
| "Hub key changed" | A challenge signed by another key: the service stops and asks to pair again. |

### 14.8 JS API

```ts
// phone/modules/wy-link/index.ts
export type SignedDoc = { type: string; body: string; sigs: { by: string; fp?: string; sig: string }[] };
export type LinkConfig = {
  origin: string; device: string; urls: string[]; pingS: number;
  hubKey: { pub: string; fp: string }; reg: "qr" | "tofu";
  cert?: SignedDoc | null;                // the claim reply's certificate, checked natively (keys.md 7.3)
  qrFp?: string | null;                   // the QR's h=, when the claim was a QR scan
};
export type LinkState = {
  enabled: boolean;                       // the "Instant delivery" switch
  status: "off" | "connecting" | "connected" | "backoff" | "revoked" | "upgrade" | "error";
  transport: "ws" | "poll" | null;
  connectedSince: number | null;          // ms since epoch
  nextRetryAt: number | null;
  lastError: string | null;               // close code and word, or a network error class; never a body
  lastSeq: number;
  received: number; rejected: number;     // since install
  batteryExempt: boolean;
  notificationsAllowed: boolean;
  verified: "qr" | "tofu" | "changed";
  pingS: number;
  checkCode: string | null;               // 14.7: "k7q2-m4xa", null without keys
};
export type LinkRefresh = { v: 1; needs_you: number; seats: { seat: string; window: string; used_pct: number; resets_at: string | null }[]; ts: string };
export type PublicKeys = { sign: { alg: "p256"; pub: string }; box: { alg: "p256"; pub: string }; auth: string };

export const available: boolean;                              // false off Android / without the module
export function prepareKeys(): Promise<PublicKeys>;           // make keys for a claim (reused until configure)
export function sealKeys(k: string, secret: string): Promise<string>;  // keys.md 7.1 keys_sealed (b64u)
export function configure(cfg: LinkConfig): Promise<void>;    // after a claim: checks, pins and urls
export function start(): Promise<void>;                       // instant delivery on, service started
export function stop(): Promise<void>;                        // off: the watchdog does 15-minute checks
export function reconnect(): Promise<void>;                   // "Reconnect now": backoff reset, connect at once
export function getState(): Promise<LinkState>;
export function setPingInterval(seconds: number): Promise<void>;   // 60–900
export function setSnoozeMinutes(minutes: number): Promise<void>;  // the hub's snooze_min (the Snooze button)
export function getRefresh(): Promise<LinkRefresh | null>;    // the newest refresh frame, for the widgets
export function requestBatteryExemption(): Promise<boolean>;  // system dialog; resolves with the new state
export function openBatterySettings(): Promise<void>;         // the maker's screen when known, else Android's
export function sendLocalTest(): Promise<void>;               // a local notification on wy_test, no hub
export function reset(): Promise<void>;                       // unpair: keys, pins and storage deleted
export function addListener(event: "state", cb: (s: LinkState) => void): Subscription;
export function addListener(event: "refresh" | "revoked" | "upgrade", cb: () => void): Subscription;

// The updater (phone.md P16), same module.
export type UpdateInfo = { version: string; build: number; url: string; sha256: string; certSha256: string; size: number; notes: string; source: "hub" | "github" };
export function checkUpdate(origin: string, token: string): Promise<UpdateInfo | null>;
export function installUpdate(info: UpdateInfo): Promise<"started" | "refused">;
```

`sealKeys` exists because the seal (AES-128-GCM over the keys JSON, keys.md 7.1) needs the auth
secret, which never leaves the module except inside that seal or the clear `keys` of a typed
code. `configure` makes keys.md 7.3's certificate check natively (the app's JS has no Ed25519):
the hub signature with `hubKey`, the device and hub in the body, the phone's own keys, `reg =
qr` and `hubKey.fp = qrFp` when `qrFp` is given. It rejects with code `ERR_WY_HUB_KEY` (the key
is not the QR's) or `ERR_WY_TAMPERED` (anything else), and other codes (`ERR_WY_CONFIG`) for a
bad argument.

`checkUpdate(origin, token)` (phone.md P16.3) asks the hub (`GET <origin>/v1/app/android`
with the device token), then GitHub when the hub has no release or cannot be reached; it
resolves `null` when nothing newer fits this phone and rejects with `ERR_WY_UPDATE` (message
without any token) when neither is reachable. It does not throttle: the once-a-day rule lives
in the app (`src/lib/update`). `installUpdate(info)` downloads, requires the file's SHA-256 to
equal `info.sha256`, requires the APK's signing certificate (`GET_SIGNING_CERTIFICATES` on the
archive) to equal both `info.certSha256` and the installed app's, and that it is this package
at a higher versionCode, then commits a `PackageInstaller` session; it resolves `"started"`
(Android asks the user to confirm) or `"refused"` (the file is deleted). A hub download needs
the device token, which `installUpdate` does not receive: the module keeps the `(origin,
token)` of the last `checkUpdate` that found a hub release in memory only, sends the token
solely to that origin, and rejects with `ERR_WY_UPDATE` ("check for updates first") when
there is none (after a process restart). `REQUEST_INSTALL_PACKAGES` is declared in the module
manifest (14.2).

The jest fake (`test/fakes.ts`) gains a recording `wy-link` fake with scriptable state
(`linkState`, `emitLink`); `test/setup.ts` mocks `modules/wy-link` with it, and
`test/link-contract.test.ts` keeps `src/contract/link.ts` (the mirror `src/lib` uses, P1)
equal to the module's types.

### 14.9 Instant delivery off

`stop()` stops the service and its persistent notification. The watchdog then logs in once
every 15 minutes over long-poll (link.md 2.1), processes what is queued (14.5), acks and
disconnects. Notifications are up to 15 minutes late (more under Doze), with no persistent
notification.

### 14.10 Tests

- **JVM** (Robolectric where Android classes are needed): RFC 8291 decryption against RFC 8291
  appendix A and the hub's goldens (`internal/hub/push/webpush/testdata`, and
  `internal/link/testdata/*.json`, link.md 10, read in place: the module's `build.gradle` passes
  both directories to the test JVM as system properties `wy.linkVectors` and
  `wy.webpushVectors`, and the runner skips while a directory does not exist yet); Ed25519 against RFC 8032's vectors through Tink; the login signature verified by
  the Go side's golden check (`hello-phone`); frame signature checks (each field changed →
  `reject`, `last` unchanged); seq dedupe across a simulated restart; the clock offset; backoff
  bounds; the notification id from a tag; the action fallback order with a fake clock;
  close-code handling; `goForeground()` on API 31 and 34 (as built: a pure branch helper,
  `Foreground.go(sdk, …)`, tested on both levels without Robolectric).
- **Emulator** (API 31, 34–36): Doze (`adb shell dumpsys deviceidle force-idle`), standby buckets,
  network switch, reboot, app update, task removed, revocation (4403).
- **On the phone** : overnight Doze, reboot, Wi-Fi ↔ mobile, Tailscale off
  and on, hub restart, Snooze and Seen from the lock screen with the app killed, dismiss on two
  phones, revoke, and a battery week passing at 2 % a day or less attributed to the app.
