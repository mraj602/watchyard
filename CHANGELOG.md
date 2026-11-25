# Changelog

All notable changes to Watchyard are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Smart Account Switch: a background move records what its launch did** (setup.md 8.6).
  The `claude --bg --resume` launch used to run with its output discarded and its exit
  status ignored, so a failed launch left a move logged as a success, and a session Claude
  Code resumed under a new session id (live 2026-11-26: `5c0f0b81` went on as `3c4dbb08`,
  which looked like a move whose resume never ran) left no trace. The launch's output now goes
  to `move/logs/<session>.log`, its failure is the move's `error`, and `moves.jsonl` gets
  `resumed_as` (the new session id) or `warn` (no resumed session seen on the new account
  within 15 s). `wy move --digest` shows both and lists failed moves.

- **Smart Account Switch: in/out churn on a draining account** (setup.md 8.6). A move into an
  owner-drained account now leaves it `account.move_drain_dest_margin` (default 2) points under
  its caps, so a filled drain no longer re-triggers on the next pass's drift (the source side
  keeps no margin). A 5-hour move is skipped when the projected hit is within
  `account.move_reset_skip_min` (default 10) minutes of that window's reset: a stall costs at
  most that, a mid-turn move a kill and an uncached re-read. A limit reached now is unchanged.

### Added

- **Hub schema change: v23 → v25** (migrations `0024_switch_move.sql`, the same file as the
  board's Switches log, and `0025_seat_off_drain.sql`). Back up the hub database before
  deploying. A hub already at v24 (`switch_move`) applies only 0025. 0024 adds the
  `switch_move` table (unused until the Switches log ships); 0025 adds two columns to `seat`, `pick_off` (0/1, the Off mode) and
  `drain_until` (ms, the hub-shared owner drain), and the base rules `rule_drain_started`,
  `rule_drain_ended` and `rule_drain_pulled` (on). Nothing existing is rewritten; an older `wy`
  ignores the new fields.
- **Off mode** (setup.md 8.5 Kind, 8.6 Off). Next to Aggressive and Protected, an account can
  be **Off**: stricter than protected, `wy account next` never picks it (not with `--urgent`,
  not when it is the only one with room), the switch never moves a session into it, the
  sessions already there stay (not moved off, even over its caps), a drain on it is ignored,
  and its usage is still shown. `--explain`, `--plan` and the board say "off (owner)". Set on
  the board and in the phone app (hub-wide, `mode: "off"`), or on one machine with
  `wy account mode <label> aggressive|protected|off|none`.
- **Hub-shared drain** (setup.md 8.5 Owner drain). The owner drain is now also a seat setting
  on the hub (`drain_until`, the same on every machine): `wy account drain` writes it when a hub
  is connected (`PUT /v1/accounts/{seat}/drain`, machine token; `--local` for this machine
  only), the board and the phone set it with the Drain choice (until a time, default the end of
  today). This machine's key still overrides the hub's on this machine. The hub ends a drain at
  its time or when the week reaches 97%.
- **Drain notices** (push.md 3.9). `drain_started`, `drain_ended` (by time or by aim) and
  `drain_pulled` (the switch pulled sessions into a draining account; batched like
  `session_moved`, and no longer mixed into it) are pushes of their own.
- **Notification categories** (push.md 5.4, phone.md P6.1, pwa.md). Sessions, Limits, Account
  switch, Drain, Usage notices and Machines: each push carries its `category` and Android
  `channel`; a device turns whole categories off (`prefs.mute_categories`, off = not sent at
  all) in the Android app's Notifications screen or the web app's Settings. The Android app has
  a channel per category (new: `wy_switch`, `wy_drain`; switch notices used to share
  `wy_usage`), and a **Sound…** button opens the channel's system settings for its sound. The
  desktop app is unchanged (one on/off switch).
- **Owner drain** (setup.md 8.5 Owner drain, 8.6 Owner drain). `wy account drain <label>
  [--until <time>|off]` drains an account now, until `until` (default 23:59 today), not only in
  the day before its weekly reset: new lanes go to it first up to its drain aims (97 weekly, 96
  five-hour), the switch moves nothing off it before a 5-hour near hit and pulls the biggest
  movable burners in (the biggest that fits first, at least `move_pull_min_burn` 3
  points/h, counted at least at the lane rate, `move_pull_margin` 10 under its caps, none
  within `move_pull_quiet_min` 15 min of a move off it) while its 5-hour projection has room.
  The digest's weekly room marks it "(draining)". `--explain`, `--plan` and the board
  show "draining (owner, until 23:59)". It ends at `until` or at the weekly drain aim. Stored
  as `next.accounts.<label>.drain_until` on this machine.
- **Re-check after the step wait** (setup.md 8.6, Re-check). A mid-turn move is re-chosen on
  the state read again just before it is carried out: dropped when its source no longer needs
  it, sent to a better destination that fits now (aggressive before protected). The move
  record (`recheck`), `--list` and `--plan` (the last hour, from `move/rechecks.jsonl`) say so.
  Live: 441d209b went to protected claude-7 on a plan 66 s old, when claude-4 had room.
- **Near-hit moves** (setup.md 8.6, Near hit). When a source will reach 100% within 60 min
  (or is hard now), the account rate limit no longer holds its moves back, the highest burner
  goes first, and when no destination fits until its reset the aggressive account that stays
  under its caps the longest takes it, if that is at least as long as the source has left
  ("best available: claude-6 fits for 2h15m"). Live 2026-11-25: claude-4 at 78% moved only a
  2.7 points/h session while 25.9 and 16.9 points/h stayed. A soft trigger whose burn is
  mostly a session in its cooldown waits for it instead of moving small burners.
- **The picker uses the switch's projection** (setup.md 8.6, The picker and the switch). While
  the switch is on, `wy account next` places no new lane where the switch's projection (one
  shared function, `internal/projection`) of the account's measured burn plus the new lane would
  go over the switch's caps less the pin reserve; `--explain` says why.
- **Destination horizon** (`account.move_dest_horizon_min`, default 90). An aggressive account
  takes a moved session when the session fits under its caps − 5 for its expected remaining time
  (else 90 minutes), capped at that account's 5-hour reset, at least 60 minutes; a protected
  account still needs the full rule and only when no aggressive one fits. Simulator: protected
  minutes over 80 −4%, pinned and all stopped minutes unchanged (within 1%), 32% more moves.
- **Young sessions** (`account.move_young_min`, default 15; setup.md 8.6). A session's burn
  counts for the switch only once it is 15 minutes old: a lane's first minutes (reading the
  codebase, cache writes) burn several times its steady rate and the burn window then holds
  only those minutes, so two fresh lanes were moved 3 minutes after launch. The readings are
  still compared with the full meter. Simulator with a start-up burst: moves of young lanes
  whose account was under its caps 3.65 → 0.23 a day, stopped minutes not higher.
- **Cheaper moves.** When several sessions would each bring an account under its caps, the one
  with the most points freed per re-read (burn ÷ context × model factor) goes first, then the
  youngest.
- **Move notices and digest** (setup.md 8.6). Each automatic move is pushed to the phone
  ("<session> <from> -> <to>: <reason>"), at most once per 5 minutes per machine, with moves
  inside the window batched into one push (`account.move_notify`, default on). With a hub they go
  through the hub's alert pipeline: the machine sends a new `switch_notice` record (record.md
  4.12) and the hub makes a `session_moved` or `switch_digest` alert (push.md 3.9) for every
  enabled target (Android app, Web Push, ntfy, desktop), shown on the board and the phone, with
  its own rule (on by default) and a per-device mute ("Smart Account Switch moves and digest").
  Direct push is the fallback when the hub is unreachable or not set; a notice it sent reaches
  the hub marked `direct` and is not pushed twice. Hub schema v23 (migration 0023) adds the two
  base rules. `wy
  move --digest [--since 24h] [--json]` prints the moves, limit hits, weekly room at each reset,
  protected minutes over cap and moves that may not have been needed, and `wy tick` pushes it
  once a day at 09:00 local (`account.move_digest`, default on). Each auto pass now also leaves
  a line in `<state>/move/passes.jsonl`.
- **`wy move --selftest` and mover checks in `wy doctor`** (setup.md 8.6). The self-test runs
  `wy move --plan` in the timer's own environment (`systemd-run --user --wait --pipe --quiet
  --collect -p KillMode=process` with the unit's PATH and `WY_*` values; directly with a cleared
  environment where systemd-run is not available, and on macOS from the LaunchAgent's) and says
  whether claude and `account.move_launch` are found there. `wy doctor`, with the switch on,
  also warns when the `account.move_launch` program cannot be found or run from the timer's
  PATH, and when hand-written hooks lack the step hooks (`wy hook --step`) that "between steps"
  moves need.
- **Smart Account Switch** (`wy move`, setup.md 8.6), off by default (`wy config set
  account.smart_switch on`). Every minute it projects each account's 5-hour and weekly level at
  the reset from the last 10 minutes of burn (P5 = level + burn × hours to the 5-hour reset; P7
  the same over at most 5 hours), and when an account will pass its cap (95/98, protected 80/80)
  it moves the sessions that burn it, the biggest burner first, until the account is back under
  its cap − 5. Background, headless and agent tmux sessions move even mid-turn: stopped with
  SIGKILL, a background session with `claude stop` (background shells keep running) and resumed under the new account with a note as the
  prompt, so they carry on by themselves (in their tmux pane, in their terminal under `wy run`,
  as a background session `--resume <id> --bg`, or headless `-p --resume`). Interactive
  sessions move the same way, and the person sees a message where the session ran: printed in
  its tmux pane or `wy run` terminal above the resumed session, or, for a plain terminal, whose
  session goes on as a background agent on the new account, written to that terminal with the
  commands to see it or attach it there (and a word on turning Remote Control on again). An
  optional owner grace (`account.move_owner_grace_min`, off) leaves a session alone for a while
  after its owner typed into it. Idle sessions never move. A spike filter (two passes in a row, unless the hit is within 30 minutes
  or the account is already at its cap), a 20-minute cooldown, no return within 60 minutes and at
  most 3 moves into one account per 15 minutes stop churn. Destinations are only accounts logged
  in on this machine with room for the moved burn until their resets: draining accounts first,
  then the soonest 5-hour reset, protected accounts last. `account.move_orgs` (`any`,
  `same-first`, `same`) decides whether a session may move to another organisation (a
  cross-organisation resume drops only the other organisation's thinking blocks). Every number
  is a setting (`account.move_*`). `wy move --plan` shows each account's projection, the moves
  and the notices; `wy move <session> [<account>]` moves one by hand; `--pin-account` or the
  peers tag `no-move` opts a session out. In the simulator (9 patterns, 300 runs each) it cuts
  limit hits from 7.3 to 3.4 a day against the decision this release had before (M5) and keeps
  protected accounts lower, for about 3x the moves and context re-read; stopped lane-minutes
  394 against M5's 381 (the rest is accounts with no room for the moved burn).
- Mid-turn moves happen between steps, not inside one. New plugin hooks (`wy hook --step` on
  `PreToolUse`, `PostToolUse` and `PostToolUseFailure`) record when each tool call starts and
  ends; before it stops a session mid-turn, the move waits until no tool call runs, up to
  `account.move_step_wait_min` (3 minutes) or one minute before the projected hit, then stops it
  at once. Background shells do not count. The note says the last step finished, or names the
  step it was stopped inside (its tool and command, at most 80 characters, never when it looks
  like a secret). A background session is stopped with `claude stop` and given up to 4 minutes
  to exit instead of being killed, since its supervisor restarts a killed job. `wy tick` starts
  the pass detached, one at a time, so the wait never holds the tick up. `claude stop` is given the job's short
  id (`claude agents --json`): with the session id it matched nothing and the job ran on.
- Everyone involved learns about a move. The moved session's note carries its whole path of
  accounts once it has moved more than once. The session that started a lane through `wy
  account next --exec` (recorded as the lease's `parent`, or `--parent <id>`) gets one short
  Watchyard notice per pass with the lane's new handle, with no polling (peers.md 8.7). A caller
  that sends to an old handle gets the message delivered and is told the new handle in the
  send line. `wy peers list` keeps every handle of the last 7 days of moves as an alias.
- `account.smart_switch_scope` (`all`, `tag:<tag>` or `sessions:<ids>`) limits which sessions
  may move, for a gradual rollout; and a test-only fault injection (`WY_MOVE_TEST`), refused
  while the scope is `all`.
- `wy burn [--minutes N] [--json]`: who is burning, per account (5-hour and weekly level, burn,
  when the 5-hour window hits) and per live session (burn per minute, account, name, kind,
  state, helper agents, calls per model, folder). The mover's forecast uses the same numbers.
- `wy move --plan` says, for each session on an account that hits, why it stays.
- `account.move_launch` takes the token `{launcher-config}` for a launcher that adds its own
  config flags: the moved session's `--settings`, `--mcp-config`, `--plugin-dir`... are left out
  instead of passed twice. A leading `~/` or `$HOME/` in it is expanded.
- Pinned sessions keep a reserve (setup.md 8.6, Pinned sessions). Each live pinned session
  (`--pin-account` or the peers tag `no-move`; idle 6 hours: released) keeps
  `account.pin_reserve_5h` (10) points of its account's 5-hour window and
  `account.pin_reserve_7d` (5) of its week free: the picker's limits for new lanes and the
  switch's caps for every other session are that much lower, so the switch moves the other
  burners off first and destinations keep their own reserve. A pinned session that alone takes
  its account to 100% within 2 hours is one alert (in `wy status` and the switch's log) with the
  `wy move` command to move it by hand; it is never moved. `wy account next --explain` / `--json`
  and `wy move --plan` show the reserve.
  In the simulator (9 patterns × 300 runs, 5 pinned coordinators each) 10/5 cuts the pinned
  sessions' stopped minutes by 9% and their weekly hits by 18% against pins with no reserve.
- A background session moved mid-turn is not left running twice: after the resume the move
  watches the old account for 20 s, and every pass for 30 minutes after, and stops a copy the
  old account's supervisor restarted (found in end-to-end tests: one came back 6 s after the
  resume).

### Removed

- **Manual pinning** (owner decision: pins come from Claude Code only). `wy move --pin-account`
  and `--unpin` only say to pin in Claude Code; `<state>/move/pins.json` is left in place and
  not read (`--plan`/`--list` warn while it lists sessions); the peers tag `no-move` pins nothing
  (still a marker tag).

### Fixed

- **Pin reserve scaled to the time left; drain aim ramps to the reset** (setup.md 8.5 Drain
  aim, 8.6 Pinned sessions). A flat pin reserve kept claude-3 at 88% of its week (2 pins, 10%
  kept free) until its reset, wasting the room, and ended the owner's drain at once. Each window
  now keeps only twice what the pins can burn before it resets (at most the full reserve, at
  least 1 point), and a draining account aims its week at 96%, rising to 99% over the last 6
  hours before the reset (5-hour: 96, rising to 98 in the window's last 2 hours), less the
  scaled reserve, with the lane fit and landing guard unchanged. The switch's caps, the hub's
  and the board's drain end follow. `--explain`, `--plan` and `--json` show the scaled reserve
  and the aim.
- **No switch margin under a drain's aim** (setup.md 8.5 Drain aim). The picker still refused
  lanes on a draining account once the switch's projection came within 5 points of the aim
  (claude-3 at 89%: "projection 91%, over its cap 95% less the switch margin"), so a drain
  never got near its aim. For a draining account the picker's check and the switch's moves use
  the aim as the cap with no margin; other accounts keep it, and pulls keep `PullMargin`.
- **Pulls go to the drain that expires first** (setup.md 8.6, Owner drain). With two owner
  drains, a 10.7 points/h session went to claude-4 (draining until Sunday) while claude-3, at
  89% with its week resetting in 4 hours, pulled nothing: the pull margin of 10 points applied
  to the weekly cap too (89 over 96 − 10). The weekly margin is now the same burn's weekly share
  (10 ÷ the ratio), and the account whose room is lost first pulls first.
- **Board drain field** (pwa.md 3.0a). The Until field was a time-only input that the radio
  buttons' style shrank to an 18 px box, and Change sent the next time the clock showed it, so a
  drain the CLI set for Sunday 07:20 became Thursday 07:20. It is now a date-and-time field,
  prefilled with the current end in local time, sized to read, and Start / Change send exactly
  what it shows.
- **Hub: analytics no longer stall everything else** (analytics.md 2). The board asks for four
  reports at once; after the hourly backup each took about a minute and together they held the
  whole read pool, so auth, `/v1/sessions`, ingest and a settings PATCH waited (503s, a 47 s
  PATCH). One report runs at a time now, waiting ones hold no connection, and a report gives up
  after 45 s.
- **Stale-data alerts only when they matter** (setup.md 8.6, Stale data). The switch told the
  owner "moves nothing off or onto … (is the hub reachable?)" whenever an account's reading was
  old, every hour for idle accounts. It now alerts only when stale data held back a move (a
  source over its cap, a session with no other destination, a drain's pull), at most once per
  account per 6 hours, and names the hub only when that pass's hub read failed. An account on
  which nothing ran since its reading is no longer stale (its level can only have held or
  fallen). A switch pass waits 20 s for the hub (was 5 s): the hub's hourly backup slowed
  `GET /v1/usage` past 5 s, and those passes saw every account stale.
- **Desktop notifications of switch and drain notices** name the machine (not its id) and show
  the hub's message (the moves, the digest's summary) under the title.
- **A moved session is not a new one** (setup.md 8.6, Young sessions). Its age is its first
  lease in the ledger, not its last move's, and its burn counts on its new account at once:
  live 54c4e1e8, moved onto claude-6 at 10:35, stayed "under 15 min old" while claude-6 was
  over its cap. The picker no longer charges a moved session as a fresh lane.
- `TestMovePinReserve` failed when the random temp folder name held "101".
- **Lane burn measured from move leases** (live pre8: `wy account next` refused every account,
  "at 56-59 points/h with this lane the switch's projection is 131-262%", lanes burning ~9-14).
  A lane is now a picker lease's session over its whole life across moves; move and handoff
  leases are not lanes; ended lanes count from 20 minutes. The switch-projection check takes a
  new lane at its model's measured burn (that model's p75, at least 10 lanes), within [0.5×,
  3×] its class's burn (`--explain`: "Lane burn (switch check)", with when it was clamped), and
  a lane without `--expect` as a medium lane (48 minutes).
- **Fresh lanes count** (live pre9: ai-lane-E moved onto claude-2 at 10:00:58 while its fresh
  lanes' burn was left out; they burned 60-90 points/h by 10:10 and the trigger fired at 10:11).
  The destination check counts lanes under 15 minutes at the picker's new-lane burn, and the
  picker's switch check counts lanes leased on the account in the last 15 minutes (this call's
  earlier picks included) at the new-lane burn less what the meter measured of them.
- **The switch knows a lane's length.** The lease line keeps `expect`, `expect_min` and
  `expect_end` (a medium lane's without `--expect`; a move lease keeps its session's); the
  switch projects a lane's burn to that end while it runs inside it (at least 30 minutes), to
  the horizons once it overruns. Older lines as before.
- **Moves the trigger undoes** (live, pre7: 3b2611ed moved claude-2 → claude-6 at 06:12 and
  claude-6 → claude-4 at 06:35). The destination check now projects with the trigger's own
  formula and horizon on that account after the move (to its resets, moves already planned this
  pass included), minus the hysteresis; the 90-minute destination horizon
  (`account.move_dest_horizon_min`) is retired and ignored. Sessions moved onto an account in the
  last 15 minutes count there at the burn they moved with.
- **Placements the switch undoes** (live, pre7: 2de58a08 placed on claude-3 at 07:36, moved off
  at 07:53). One rule for a session's burn in the picker's acceptance, the switch's trigger and
  its destination check: inside its lease's expected duration, max(expected remaining, 30 min);
  without `--expect` or overrun, to the horizons. The picker also leaves the destination's
  5-point margin under the caps (a placement is a destination). Simulator per run-day: undone
  moves 3.92 → 1.81, undone placements 1.02 → 0.79, moves −17%, stopped minutes −3%.
- **Moves on stale data** (live, pre6 05:37, the hub migrating: `wy move --plan` said "claude-4
  5h 100%: 5h cap reached now: moving its burners" from a reading 3 h 37 min old; the account was
  at ~68%). An account whose reading is older than `account.move_stale_min` (new, default 10) is
  neither a source nor a destination; `--plan` says "data stale (age), no moves"; one owner alert
  when it lasts over 30 minutes. With the switch on, the picker neither blocks an aggressive
  account nor draws a lane to one on stale data: it ranks it after the accounts with fresh data
  and `--explain` says so.
- **A pass acting on minutes-old state** (live 05:40:42: a pass of the pre5 binary, started
  before the pre6 install, moved 78f476e8 at 0.0 points/h onto aggressive caps for claude-5,
  set protected at 05:36; its log line has no `rate_known`, which pre6 writes). The current
  switch keeps claude-5's protected caps and never moves a session under the burn floor (test of
  that exact state); a pass whose state took over 2 minutes to read now moves nothing.
- **A moved session's tokens counted to its new account from before the move** (live
  2026-11-25 04:06, 04:16 and 04:25: claude-4's 5-hour level read 100% for one pass at a time
  while its reading was 52-60%, and three sessions were moved off it). The token meter now counts
  each row to the account of the session's lease begun by the row's time, not to its last lease.
- **Hit times before the pass** ("5h projected 110% ... 100% at 04:24" in a pass at 04:25).
  The hit time is now + (100 − level)/R5 from the current level and burn and is left out of the
  reason, the note, the human's message and `--plan` when it is not in the future; a pin alert
  on an account already at 100% says so instead of a hit time.
- **`wy move --plan` and moves by hand.** `--plan` is read-only and takes no lock (a shared,
  non-blocking probe of an existing session lock file; it creates nothing), and taking a session's
  lock retries for a second so a probe never makes a move refuse. A mid-turn `wy move --now` by
  hand waits at most `move_pass_wait_s` (90 s) for its step, not the 3-minute step wait (live:
  3.5 minutes per move), and says first that it waits. There was no lock contention: `--plan`
  never took `pass.lock`; the time was that step wait plus 10-16 s to read the state on a loaded
  worker.

- **The switch moved a session pinned in Claude Code** (live: 2abbe6d1 was pinned in Claude
  Code at 03:14 and moved at 03:20). Pins now come from Claude Code only: a session listed in its
  config folder's `jobs/pins.json` (short ids, prefix match; read only, mtime-cached; missing or
  unparsable is no pins, with a warning) is pinned for the switch, `--plan`, the picker's pin
  reserve, `--explain`/`--json` and the pin alerts. `wy move --plan`/`--list` list the pinned
  sessions; `wy move --list --json` is now an object (`moves`, `pins`, `warnings`). Moving such a
  session by hand needs `--force` and warns that its pin stays on the old account's list.
- **A by-hand move's note no longer gives the switch's reason.** It reads "This session was
  moved by hand by the owner from claude-6 to claude-2 at 14:05" (path and organisation note as
  before), with no projection ("would hit 100% ... P5 181%").

- **Expiring protected room went unused** (live: protected claude-5 at 85% of its week, an hour
  before its reset, 5 hours unused, took nothing: the switch's protected weekly cap 80 bounded its
  drain). Within `account.move_expiring_h` (default 3) hours of a protected account's weekly reset
  its weekly cap is the aggressive one (98) for the picker and the switch; its 5-hour cap stays
  80. `--explain` and `--plan` say "expiring room".
- **A short lane refused on an idle account** ("the switch's projection is 115%" for a 25-minute
  lane at 23 points/h). The picker projects the new lane only over its expected time (else 90
  minutes), at most to the 5-hour reset, at least an hour; the switch projects a running session
  with an expected end only until then.
- **Moves went to a protected account while an aggressive one had room** (live: claude-2 at
  13%/18% had taken 3 moves in 15 minutes, two of them 0.2-0.5 points/h). The per-account limit
  counts only moves at or over the burn floor, is 5 (`move_account_moves`), and a new
  `move_account_burn` (40 points/h of moved burn per 15 minutes) limits burn; an aggressive
  account with room takes a move over these limits before any protected account does. Moves
  log the session's burn (`rate` in moves.jsonl). Simulator (100 runs × 2 days per pattern, all
  patterns, per run-day), new limits against the old 3 moves of any burn: moves 40.5 vs 38.9,
  limit hits 9.6 vs 8.4, all stopped minutes 1189 vs 1174, protected 5h max 85.1 vs 85.3: the
  looser limits cost about one more limit hit per run-day there.
- **A pass blind for minutes behind its step waits** (live: three mid-turn moves waited in turn,
  no pass for 6 minutes while claude-4 went from P5 76 to 155). The waits run side by side, at
  most `account.move_pass_wait_s` (default 90) in a pass; a session still inside its step waits
  on in the next pass, up to `move_step_wait_min` in all. The pass lock is held only while a pass
  plans; each move holds its session's lock, so the next pass runs and leaves it alone.
- **`passes.jsonl` shows the 5-hour level's parts** (`l5_read`, `l5_read_age_s`, `l5_meter`,
  `l5_leases`): `--explain`'s 5-hour level is the switch's plus the lease charges of young lanes.
- **The switch moved every unpinned session off an account already near its cap** (live:
  claude-3 at 84.1% of its week, cap 88 with 2 pins, mostly pinned burn). Its target was cap − 5
  = 83, under the level now, so no moves could meet it. When a window's level is already at or
  over cap − 5 the moves aim for the cap itself; `--plan` says "only pinned burn left" when
  nothing a move would lower is left.
- **`wy move` never makes a second copy.** A session with two live processes (the old one and a
  resumed copy) is refused by hand and skipped by the switch; a by-hand move of a session moved
  in the last 5 minutes is refused without `--force`. Flags are read anywhere (`wy move <id>
  <account> --dry-run` is a dry run, tested in every order); a flag-like word left unread after
  `--` stops `wy move`, `wy connect`, `wy keys`, `wy peers` and `wy hub` commands before they act;
  `wy config set` refuses a `--word` it does not know as a value; `wy outbox append` and `wy
  machine list` refuse extra arguments.

- **The switch moved a session that burned nothing** (live on pre5: a mid-turn lane at 0.0
  points/h moved with a 4.6 points/h one). A running session is a candidate only when its own
  burn is at least `account.move_min_burn` (default 1 point/h); a session under
  `move_young_min` never is; the greedy stops at a pick that would not lower the projection.
- Move notes, reasons, lane notices and pin alerts name the day of a reset or hit time over
  24 hours away ("before its Thu 03:30 reset", not "before its 03:30 reset").

- **New lanes placed where the switch moves them off** (live on pre4.1: four lanes leased to
  claude-3 at 81-83% of its week, each moved off 4-20 minutes later). The lanes picker's drain,
  at-risk test and rank now aim at the switch's caps less the pin reserve (claude-3 with 2 pins
  had 6 weekly points of room to its cap 88, not 16 to 98), and the switch-projection check
  takes a new lane at the measured lane burn (the p75 of this setup's lanes' points per hour,
  per model) instead of its size class's cost over its wall time (about 4 points per hour).
- **Stale readings.** A reading more than 1 point under the one before it in the same window is
  skipped unless the next confirms the drop (a session sent claude-7's week at 12% between
  readings of 26%, and the switch judged it on 12 for 5 minutes). The switch logs the weekly
  reading and what the meter adds to it in `passes.jsonl`; `--json` shows
  `weekly_measured_pct`, the level the switch reads.
- `wy account next --explain` lists each account once.
- **Smart Account Switch: no false moves from integer readings** (setup.md 8.6, Projections).
  An account whose sessions ran on another machine was moved off on its readings alone: one
  point of rise over 10 minutes read as 6 points per hour. The burn now adds the other machines'
  sessions the hub measured; the readings are a slope over 15-30 minutes (readings 5 minutes
  apart, one 5-hour window) used only when they hold on two passes, a trigger from the readings
  alone needs 3 passes, and the spike filter counts per account and window. Simulator: 60% fewer
  false moves, 17% fewer moves, no change in stopped minutes.
- Smart Account Switch passes started by the timer were killed when `wy tick` exited (the
  systemd service's default `KillMode=control-group`): the timer's service now has
  `KillMode=process` (rewritten by `wy update`), and `wy doctor` warns about a unit without it.
- The switch could not run claude from the timer (its PATH lacks `~/.local/bin`): `claude stop`
  and the default resume command now use claude's full path when it is not on PATH
  (`~/.local/bin/claude` and the usual places, then the session's own executable); with the
  switch on, `wy doctor` warns when the timer cannot find it.
- A moved session's flags repeated with the same values (a launcher adding its config flags on
  an earlier move) are passed once.
- Peers: a session moved to another account by hand, without `wy move`, keeps its old handles
  as aliases (found from the folders Claude Code keeps per session), and a message sent under
  the account it left is delivered instead of refused with account_mismatch, by the hub and
  by the receiving machine.
- Moves: an idle session on an account at its limit is no longer planned as "stopped at the
  limit". Stopped now means a limit reply ended its last turn (a `StopFailure` for
  `rate_limit`); such a session moves only when the window resets 40 minutes or more from now,
  and an idle session that finished its turn is never moved because its account is full.
- Moves, found in end-to-end tests on a machine with 7 accounts: a background session is now
  stopped through its old account's supervisor (`claude stop`), which otherwise restarted it
  there and left two copies running; it is resumed with the flags from its job record (its
  process may be a pre-started spare); folder trust and, when needed, the transcript are carried
  to the new account, so a resumed session no longer waits at "Do you trust this folder?" and
  `claude --bg` no longer refuses with "Workspace not trusted"; a session with background shells
  still running counts as idle; the owner counts as at a session only when they typed into it,
  not when it restarted; a moved session's age is not reset by the move; `wy move <session>
  <account>` refuses an account at its limit without `--force`.
- `wy account next --exec -- claude --bg ...` binds the lease to the session the supervisor
  started (claude ignores `--session-id` with `--bg`) instead of to an id no session has, and no
  longer hangs when `claude --bg` starts the supervisor.

- A session resumed under another account (`CLAUDE_CONFIG_DIR=<other> claude --resume <id>`)
  now follows it: the hub, `wy status` and the board re-attribute it to the new account from its
  first live record there (a hook event or a complete `claude agents` list) instead of keeping it
  as "done" on the first account, and its old peers handle stays an alias so messages to it are
  delivered instead of being refused with `account_mismatch`. Hub schema v22 records where it
  moved from.

## [1.3.2] - 2026-11-24

### Changed

- `wy account next` (lanes) **drains** weekly room before it resets. An aggressive account whose
  week resets within 24 hours, or within twice the time it needs to use its room at full 5-hour
  speed, goes first and takes as many lanes as its room absorbs before the reset: the capacity
  comes from the measured lane cost (p75 and mean) and length and from the burn of the lanes
  already running, not from fixed per-lane charges, and the hourly cap does not apply. Drain picks
  carry `drain`, `finish_by` (the reset − 10 minutes) and `weekly_reset` in `--json`, a "drain: …"
  reason, and `WY_FINISH_BY` / `WY_ACCOUNT_RESET` in the `--exec` environment. A protected account
  drains only when its room would expire even at full speed, after every aggressive drain
  account. Outside drain: at-risk aggressive accounts, then the others in turn, then aggressive
  accounts with an unknown weekly level, then protected ones. On 2026-11-23 the old round
  robin sent three of six lanes to protected accounts while claude-2 had 12% of its week left
  hours before its reset; now all six go to claude-2.
- `wy account next` (lanes) skips an aggressive account whose running lanes will still burn its
  5-hour window to the limit, until the first of them is expected to end.
- `wy account next` (lanes) **never stalls**: a lane waits only when every account is at a hard
  limit (weekly 98, the 5-hour limit with the running lanes' burn, the backstop, the landing
  guard) or cannot be read. The hourly cap, the protected account's gate and a draining account's
  capacity only order the accounts; when all are reached the lane goes to an aggressive account,
  then a protected one, marked `over` (`"over": true` in `--json`). Protected accounts stay last.
- New `--expect short|medium|long|<duration>`, `--expect-minutes N` and `--expect-tokens N`
  (lanes): how big each new session will be. The class sizes come from measured sessions (short
  under 25 minutes, about 1.2 5-hour points; medium up to 2 hours, about 3.2; long, about 14).
  The estimate (never below the measured mean lane) sets drain capacity, the landing guard and
  the 5-hour check, and a long lane goes to accounts that do not reset before it would finish.
  The pick line says `expect <class>` and `--json` has `expect`. In the week simulator (300 runs,
  typical estimate errors) this cuts lanes running past a weekly reset in the long-lanes pattern
  from 4.2 to 0.9 per week (the old round robin: 1.5).
- Leases record `drain` and `finish_by` in the ledger and in `account_lease` records.

## [1.3.1] - 2026-11-23

### Fixed

- Phone: the microphone in a claude.ai tab works. The app asked for `RECORD_AUDIO` but did not
  declare `MODIFY_AUDIO_SETTINGS`, which Android's WebView also needs, so the page saw no
  microphone ("your microphone isn't available") even after the permission was granted.
- `make phone-apk-release` checks the keystore password before it builds. Before, a wrong
  password passed the check (keytool's error text was hashed as if it were the certificate)
  and the build failed only at signing, about 20 minutes later. The check now also unlocks the
  key and refuses a keystore whose certificate is not the pinned release certificate, unless
  `WY_ANDROID_ALLOW_OTHER_KEY=1` is set for a deliberate key change.

## [1.3.0] - 2026-11-23

### Added

- **Doorbell as a trusted sender** (Linux): `wy peers trust doorbell` at a terminal approves
  the Doorbell program on this machine to send to tags (`wy peers send tag:<tag> [--one]
  --stdin`) as one chosen account. The agent checks the sender itself from the socket's peer
  credentials and `/proc` (interpreter and script in the approved venv), never from the
  request. Approval needs a controlling terminal whose session has no Claude Code process
  above it, always asks y/N (no `--yes`), and accepts only an install whose `RECORD` matches. Doorbell never sends to a session by name or as a reply. Its messages carry the
  header "from doorbell (service) on <machine>", and the matrix, the limits and the audit
  (`service:doorbell`) apply. To revoke it, run `wy peers trust doorbell --off`, or use
  Settings → Peers → Trusted senders on the board or the phone. The hub has `peer_trust`
  (migration 0021) and `GET /v1/peers/trusted`, `POST /v1/peers/trusted/revoke`. The hub
  relays Doorbell's messages only to agents from 1.3.0 (`client.caps` `peer_service` in the
  link hello); an older receiver is refused with `receiver_too_old`. Doorbell has no
  permission class of its own (the owner's approval is the vetting), so each of its messages
  is framed in the receiving session's class and reaches bypass and prompting sessions alike
  without an approval dialog; the approval prompt says so. A receiving session whose mode is
  not known gets nothing, and the send exits 9 (`held`, new), never 0; a broadcast exits 0
  only when every holder took it. `wy peers audit` lists Doorbell's sends as
  `doorbell (service)` with the tag, and its `--json` carries `from_kind`, `tag` and `detail`.
- **Tags in the UI**: session rows on the board and the phone show the session's tags as chips;
  search matches tags, and `tag:<name>` matches one tag exactly (a chip sets it). Settings →
  Peers lists every tag with its holders and the trusted senders that sent to it recently.
  `Session.tags` in the read API.

## [1.2.0] - 2026-11-23

1.1.0 was never tagged; its changes ship here.

### Added

- `wy account next`: recommends the account (config folder) to start the next Claude Code
  session on, from each folder's login on this machine and the limits of its seat (hub, or
  this machine's status line), with strategies (`smart`, `expiring`, `most-room`, `spread`,
  `fill`, `sticky`), per-account modes, caps, hours and priorities under `[next]` in
  `config.toml`, a lease per pick so bursts of launches spread out, and `--env`, `--json`,
  `--explain` and `--exec` output. It only recommends.
- `wy account next` strategy `lanes`, the new default: protected and aggressive accounts
  (`next.accounts.<key>.mode protected`), the state from the newest readings plus this
  machine's token use since them, burst caps of 6 (aggressive) and 2 (protected) new sessions
  an hour, turns with expiring weekly room first, `--n` for several sessions in one call,
  `--session-id` to tie each pick to its session, and `--urgent`. Exit 3 for a session id
  already used; exit 4 also when only some of the sessions asked for were placed.
- A lease ledger for `wy account next` (`<state>/next/leases/`, append-only, one file per
  day): every pick is a lease, which `wy hook` marks launched and ended on `SessionStart` and
  `SessionEnd`, and which goes to the hub as an `account_lease` record.
- Hub: `GET /v1/usage`, readable with a machine token: each seat's limit percentages, reset
  times and recent samples, other machines' recent leases (no session ids), plus the calling
  machine's own folders, and nothing else. `wy account next` reads it, so upgrade the hub
  before the machines. Schema version 17 adds `account_lease`.
- `wy account next` (`lanes`) calibrates each account: its meter factor (5-hour points per
  million token units), its 5-hour-to-weekly ratio and its lane price adjust themselves to
  its measured use after about 3 days and 20 measurements, restart after a run of one-sided
  misses or a change of login, and a margin covers the meter's recent under-reading. The
  hub does the work every 15 minutes and returns the factors in `GET /v1/usage`; without a
  hub the account is fitted from this machine's data. `--json` and `--explain` show the
  factors. Schema version 18 adds `seat_calibration`.
- `wy account next` (`lanes`) counts each lane once, whichever machine picked or runs it:
  `GET /v1/usage` returns each lease's session use per minute for its first 5 hours (with a
  hash of the session id, never the id) and the calling machine's own recent leases, and a
  lease charges only what its session has not burned yet, while that session's tokens after
  the reading go into the meter from the same source. With the hub down, other machines'
  leases come from a copy kept at the last hub read (`<state>/next/hub_leases.json`) and stop
  charging at the first reading 10 minutes after their pick. `--json` (`lease_charges`) and
  `--explain` show each lease's state, where its burned units came from and its charge.
- Usage and the picker, per account, in the Details section of an open account on the board
  and in the phone app: the newest verdict of `wy account next` (eligible or not, why, when it
  frees up), the calibration factors (measured, blending or fallback, with `n`), 14 days of
  the 5-hour-to-weekly ratio labelled with each day's most-used model, and the 5-hour
  capacity index per model (API dollars per 1 % of the 5-hour window, normalised to last
  week). The hub keeps it up to date in its 15-minute calibration job (`GET
  /v1/accounts/{seat}/relation`), and `wy account next` sends its verdicts as an
  `account_verdicts` record (at most every 15 minutes unless they change).
- Four low-urgency usage notices, on by default: `limit_change` (a model's 5-hour capacity
  moved more than 1.2× or under 0.83× on most Claude-Code-heavy accounts), `usage_reset` (a
  server or owner reset put both windows back to about 0 %; the reset is kept out of
  calibration), `seat_change` (a folder logged in to another account or org) and
  `weekly_drop` (the 7-day window fell more than 10 points without a reset: something
  changed, check). Each expires 24 hours after it fires. Schema version 19 adds `seat_watch`,
  `seat_verdict` and their rules.
- A search box on the Sessions screen of the board (and so of the desktop app's Hub view) and
  of the Android app: it matches folders, titles, names and the last message, ignores case and
  accents, marks what it found, adds a line when the match is in a field the row does not show,
  and keeps the query while the list refreshes. It runs on the device over the list it already
  has: no new API and no message text beyond the existing last-message tail.

- `POST /v1/sessions/{id}/seen`: marks a session's current Needs-you episode seen even when it
  has no alert (a hub run with `--no-push`, or before the engine reached the episode); the
  board and the phone app use it for such rows.
- Protected or aggressive is now a per-account hub setting (`Seat.mode`, `PATCH
  /v1/accounts/{seat}` `mode`, `GET /v1/usage` `mode`; migration 0020), the same on every
  machine. The board and the phone app have an owner control on each account ("How should the
  picker use this account?") with a one-line definition under each choice; `wy setup` and `wy
  account add` ask once per account. A machine's own `next.accounts.<key>.mode` still wins on
  that machine and is shown as "set on this machine"; an account with no mode shows "Not
  chosen yet; treated as aggressive." (unchanged behaviour).

### Changed

- `wy doctor` treats a folder as logged out when `claude auth status` last said so, even if
  its `.claude.json` still holds the old `oauthAccount`.

- **Usage and the picker** on the board and the phone, rewritten in plain words: the verdict
  explains the account's place in line and each rule with its value against its limit, three
  tiles show how the 5-hour limit relates to the week, one line says what a new session is
  set aside, and the charts have titles, a y axis, "no data" gaps, model colours with full
  names and a text alternative; empty charts are replaced by one line, and the details moved
  to **How this works**.

### Fixed

- Needs-you rows whose alert was not pushed (`stale`, `rule_off`, `peers`, `remote_control`,
  `baseline`) had neither Seen nor the ⋯ menu on the board, in the desktop app and on the
  phone, so they could never be dismissed. The read API now carries their skipped alert as the
  session's `alert`, and Seen on them works; Snooze stays for alerts that can still push.

## [1.0.0] - 2026-10-24

The first public version. Everything below is new.

### Added

- `wy`, one static Go binary for Linux, macOS and Windows (Windows without peers and the owner
  key): hooks and status line recording into a local outbox, `wy status`, `wy doctor`,
  `wy update`.
- `wy setup`: detects Claude Code config folders, labels and colours accounts, makes them share
  settings, instructions, add-ons, plugins, MCP servers, history and prompts, adds launchers,
  and installs the Claude Code plugin. Shows a plan first, backs up every change, and
  `wy setup --undo` reverses a run.
- The hub (`wy hub serve`): SQLite storage of every record, session states, usage windows per
  account, analytics, a read API and a live stream, pairing and per-device tokens, backups,
  export and import. Runs as a user service, with Docker or with systemd.
- The board: a web UI served by the hub that installs on a phone as a web app, `wy board` in a
  terminal, and a pane inside Claude Code.
- Alerts for sessions that need you, failures, limit thresholds and silent machines, through
  Web Push, ntfy and the Android app, with quiet hours, private notifications, snooze and
  batching.
- More machines through direct connection, an SSH port forward, or pull over SSH.
- The desktop app (Linux, macOS, Windows): one claude.ai tab per account, opt-in usage reading
  per account, the board in a Hub view, OS notifications and a tray.
- The Android app: the board's screens, notifications over its own connection to the hub (no
  third-party push service), Open, Snooze and Seen, six home-screen widgets, and an updater.
- Keys and peers: an owner key, machine certificates, a signed link between devices and the
  hub, and end-to-end encrypted messages between your own Claude Code sessions, gated by a
  peers matrix.
- The Android app has no proprietary Google code (no Firebase, Play services or ML Kit); CI
  checks the built APK. Package name `io.github.mraj602.watchyard`.
- The desktop app pins a self-signed hub certificate on first use, warns when a hub on another
  machine is plain http, limits the frames inside account tabs to an allow-list, and shows when
  `wy` or the hub is newer. Release packages are unsigned but carry build-provenance
  attestations.
- Sessions that coordinate with each other through peers don't send "asked" alerts; permission
  prompts and real questions still do (`skip_peers`, on by default).
- Documentation: user guides, architecture, behaviour specs, contributing guide, security
  policy, release guide, a website and a demo recorded from sample data.
- Licensed under the GNU AGPL-3.0.

### Security

- Desktop: the tabs' sign-in cookies are encrypted on disk (Electron's cookie encryption fuse is
  on). A tab signed in by an earlier unpacked build may ask you to sign in again once.
- The hub's Android app mirror pins the release signing certificate and checks the APK itself,
  not only the manifest. It mirrors nothing until the maintainer sets the pin.
- Android: no app data in cloud backup or device transfer, lock-screen notifications show only a
  generic text, widget taps open the app with an explicit intent, and the phone no longer sends
  its token to the `moved_to` address in an unsigned `/health` reply (it asks you to pair again).
  `make phone-apk-release` refuses a wrong package id, cleartext traffic or a debuggable build.
- Desktop: a page can trigger only a few external opens or session links in a short time, and
  never brings the window or the browser to the front. Usage read from the page's own
  context is used only for a known account, and usage data is size- and shape-checked first.
- Desktop: sites that ask for a client certificate get none.
- The web board's "Open in app" link is now an Android intent pinned to the Watchyard package,
  so no other app can receive the pairing secret.

### Fixed during the release candidate

- The web board's sign-in page no longer loses a half-typed token or owner link when the page
  updates in the background (for example while the offline app installs).
- Desktop: signing in to claude.ai in an account tab works again. The tabs blocked the frames of
  the sign-in page's hCaptcha check, so it failed with "Couldn't verify your browser".
- Desktop: account tabs send Chrome's reduced user agent (`Chrome/<major>.0.0.0`), as Chrome
  itself does, instead of the full build number. claude.ai's Claude Code section kept asking to
  verify the device again, because the string was unusual and changed with every Electron update.

[Unreleased]: https://github.com/mraj602/watchyard/compare/v1.3.1...HEAD
[1.3.1]: https://github.com/mraj602/watchyard/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/mraj602/watchyard/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/mraj602/watchyard/compare/v1.0.0...v1.2.0
[1.0.0]: https://github.com/mraj602/watchyard/releases/tag/v1.0.0
