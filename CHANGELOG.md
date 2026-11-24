# Changelog

All notable changes to Watchyard are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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

### Fixed

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
