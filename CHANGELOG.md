# Changelog

All notable changes to Watchyard are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.3.3] - 2026-11-24

### Added

- **Smart Account Switch** (`wy move`, setup.md 8.6), off by default (`wy config set
  account.smart_switch on`). It moves a running session to another account only when its account
  would reach its 5-hour or weekly limit before that window resets at the current burn, not at a
  fixed percentage. Cheapest levers first: the picker already holds new lanes off such an account;
  moves come only when that is not enough. Moves follow token-saving rules: only at a turn boundary
  (or once a limit has stopped the session), sessions with a cold prompt cache and small contexts
  first, never one the owner is typing into or one expected to finish before the hit, at most once
  per 5-hour window, only when the stall saved outweighs the context re-read, to an account with
  room for the session's expected life (protected accounts last), with a cooldown and no
  ping-pong. A session is stopped with SIGKILL (its background shells keep running) and resumed
  with its own flags under the new account: in its tmux pane, in its terminal under the new
  `wy run` wrapper (or the opt-in `claude` shim, `wy run --install-shim`), as a background
  session (`--resume <id> --bg`), or headless (`-p --resume`); plain terminals get the steps. The
  moved session gets one line on resume and nothing else changes for other sessions. `wy move
  --plan` shows the forecast and the moves; `wy move <session> [<account>]` moves one by hand;
  `--pin-account` or the peers tag `no-move` opts a session out. In the simulator (300 runs per
  pattern) the default cuts lane-minutes stopped at a limit from 4,210 to 538 a day, about as
  few as a fixed 92% threshold (540) with under half its limit hits, moves and token cost, no
  session killed mid-turn and no ping-pong. Destinations: the session's own organisation first,
  then the account whose 5-hour room expires soonest.
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

### Fixed

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

[Unreleased]: https://github.com/mraj602/watchyard/compare/v1.3.3...HEAD
[1.3.3]: https://github.com/mraj602/watchyard/compare/v1.3.2...v1.3.3
[1.3.2]: https://github.com/mraj602/watchyard/compare/v1.3.1...v1.3.2
[1.3.1]: https://github.com/mraj602/watchyard/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/mraj602/watchyard/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/mraj602/watchyard/compare/v1.0.0...v1.2.0
[1.0.0]: https://github.com/mraj602/watchyard/releases/tag/v1.0.0
