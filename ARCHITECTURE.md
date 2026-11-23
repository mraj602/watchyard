# Architecture

This document explains how Watchyard is put together: its components, how data moves
between them, where it is stored, where the security boundaries are, and how the repository
is laid out. The exact behaviour of every part is specified in [docs/spec/](docs/spec/README.md);
when this document and a spec disagree, the spec wins.

Watchyard is unofficial and not affiliated with Anthropic.

## Components

| Component | Code | Runs | Job |
| --- | --- | --- | --- |
| `wy` | `cmd/wy`, `internal/*` | On every machine that runs Claude Code, started by Claude Code's hooks and status line, and by a one-minute timer | Records events and limits into a local outbox, sends them to the hub, sets up accounts and sharing, shows the board in a terminal, talks to peers |
| Hub | `internal/hub/*`, `webui/` (same binary: `wy hub serve`) | On one computer: a laptop, a server, or a container | Stores every record, works out session states and usage windows, sends alerts, serves the API, the live stream and the board |
| `wy agent` | `internal/agent` | Optional user service on each machine | Holds the machine's signed link to the hub, delivers peer messages into sessions |
| Board | `webui/static` (embedded in `wy`) | In a browser, or installed on a phone as a web app | Reads the hub's API and live stream |
| Desktop app | `app/` (Electron) | On a desktop | One claude.ai tab per account, optional web usage reading, the board in a Hub view, OS notifications |
| Android app | `phone/` (Expo, React Native) | On a phone | The board's screens, native notifications over its own link to the hub, widgets |
| Claude Code plugin | `plugin/watchyard`, `plugin/watchyard-pane` | Inside Claude Code | Hooks that run `wy hook`; skills; an optional board pane |

Design rules that hold everywhere:

- **Nothing stays running on a machine** except the optional agent. Claude Code starts
  `wy hook` and `wy statusline`; each appends a record and exits. A timer flushes the outbox.
- **Every connection is outgoing** from machines to the hub, and from the hub to push
  services. Nothing has to reach into a laptop or a server. The only exception is opt-in:
  the hub can pull a locked-down server's outbox over the operator's own SSH.
- **The outbox absorbs every gap.** Records wait on disk while the hub is unreachable (capped
  at 50 MB, oldest token rows dropped first, hook events kept) and are de-duplicated by
  record id when they arrive.
- **The hub cannot command clients.** Its replies to machines are lists of accepted record
  ids. `wy` never executes anything it receives. The hub can relay documents (matrix,
  revocations) that clients verify against keys they pinned.
- **Logins are never touched.** `wy` never reads, copies or sends a Claude Code credential or
  a claude.ai cookie. Logging in is always Claude Code's own `claude auth login`, and
  claude.ai sign-ins stay inside local browser sessions on the device.
- **Plan first, undo always.** Every command that changes files on a machine shows a plan,
  journals each change into a backup set, and can be undone byte for byte.
- **No automatic account switching.** Watchyard may show which account has room; the person
  chooses.

## Data flow

```
 Claude Code (each session, each config folder)
   │ hook stdin JSON              │ status-line stdin JSON        transcripts, claude agents
   ▼                              ▼                               ▼
 wy hook                       wy statusline                   wy scan / wy agents (timer)
   └──────────────┬───────────────┴───────────────┬───────────────┘
                  ▼  one record per fact (record.md)
          outbox: <state>/outbox/YYYY-MM-DD.jsonl  (outbox.md; fsynced, locked append)
                  │
                  ▼  wy flush (after each hook, debounced; and every minute)
          POST /v1/ingest   bearer machine token (tokens.md)
                  │
   ┌──────────────▼───────────────────────────────── hub (wy hub serve) ─────┐
   │ ingest ─► record table (raw, every field) ─► typed tables (schema.sql)  │
   │                          │                                              │
   │             state engine (states.md)   usage model (windows, pace)      │
   │                          └──────────┬───────────┘                       │
   │                   read API + /v1/stream (SSE)                           │
   │                          │ after each ingest batch                      │
   │      alert engine (push.md): reconcile ─► alert rows ─► scheduler       │
   │                          └─► senders: Web Push (VAPID, RFC 8291), ntfy, │
   │                              link (Android app; widget refresh)        │
   └──────────────────────────────────────┬───────────────────┬──────────────┘
                                          ▼                   ▼ outbound HTTPS
               board (browser, web app), Android app,   push services (browser
               desktop app, wy board, the pane mod       vendors), ntfy servers
```

**Records.** Every fact is one record: a hook event, a limit sample from the status line, a
token row from a transcript, a session list, a usage reading from the desktop app, machine
info. The hub keeps every record exactly as received (`record` table) and builds typed tables
from it, so a bug in the state engine or the usage model can be fixed and replayed over the
whole history (`wy hub refold`). Content rules decide what a record may hold
([record.md](docs/spec/record.md)): prompts, tool input and output and file contents are
removed on the machine before anything is written.

**Session states** are a pure fold of hook events ([states.md](docs/spec/states.md)). The same
fold runs on the hub and in `wy status`, so a machine without a hub shows the same states.

**Usage** comes from two sources merged per account: Claude Code's own `rate_limits` in the
status line, and, optionally, the desktop app's reading of claude.ai's usage page. Windows
(5-hour, 7-day, per model) are keyed by their reset time, so each window alerts once per
threshold ([analytics.md](docs/spec/analytics.md), [push.md](docs/spec/push.md)).

**Alerts.** After each ingest batch the alert engine reconciles rules against the current
states and windows. An alert row exists at most once per episode (a unique key), so restarts
and replays never alert twice. Delivery goes to Web Push, ntfy and the Android app's link,
with quiet hours, batching and a flood cap ([push.md](docs/spec/push.md)).

**More machines** reach the hub in one of three ways ([machines.md](docs/spec/machines.md)):
`direct` (they post to the hub), `forward` (through an SSH port forward from the operator's
laptop) or `pull` (the hub runs `ssh <server> wy outbox drain --stdio`). A server can also send
ntfy pushes itself when the hub has not heard from it, and the hub then marks those alerts as
already sent.

```
 server (reach pull)                                 hub (wy hub serve)
   outbox ─► wy outbox drain --stdio ◄── ssh ─── pull scheduler ─► ingest (same as /v1/ingest)
```

**The desktop app** reads usage in each account tab (only after the person accepts a warning
for that account), hands the readings to `wy outbox append`, and reads the hub back with a
read-only desktop token ([desktop.md](docs/spec/desktop.md)).

```
 app/ (Electron main) ── usage read per tab ─► wy outbox append ─► outbox ─► wy flush ─► POST /v1/ingest
        ▲                                                                              │
        └──── GET /v1/accounts, /v1/stream (desktop token) ◄─────────────── hub ◄──────┘
```

**The Android app** is a `phone` device: it reads the API by polling, and its notifications
arrive over its own long-lived connection to the hub (the link), sealed to keys kept in the
Android Keystore. Snooze and Seen call the hub with signed action tokens. No third-party push
service is used ([phone.md](docs/spec/phone.md), [push.md](docs/spec/push.md) 13-14).

```
 hub ── link sender ─► link queue ─► app's own connection (phone/, Android)
  ▲                                                                       │
  └──────────── read API (polling), /v1/actions/{token} (Snooze, Seen) ◄──┘
                                         app ─► widget snapshot ─► widgets
```

**The terminal** (`wy board`, the Claude Code pane mod, Warp tab configs) only reads: from the
hub's API and stream with a read-only board token, or from the local outbox when there is no
hub ([terminal.md](docs/spec/terminal.md)).

**Keys, the link and peers** are a second path next to ingest
([keys.md](docs/spec/keys.md), [link.md](docs/spec/link.md), [peers.md](docs/spec/peers.md)).
Each device holds Ed25519/X25519 keys. The hub's signing key and the owner key (made by the
person with `wy keys init`, sealed with a passphrase on their own computer) are pinned on every
device, and a machine certificate needs both signatures. Devices keep one signed connection to
the hub (`/v1/link`, WebSocket with a long-poll fallback) with a durable per-device queue. Peer
messages are sealed to the target machine, so the hub relays ciphertext and stores metadata
only.

```
 session A ── wy peers send ─► agent.sock ─► wy agent (A's machine) ──┐ same machine: inbox of B
                                                 │ seal to B's machine, sign        │
                                                 ▼                                  │
                                    hub: /v1/link, matrix check, queue ──► wy agent (B's machine) ─► inbox of B
```

## Storage

| Where | What | Notes |
| --- | --- | --- |
| Machine, config folder (`~/.config/watchyard`) | `config.toml` | Settings only |
| Machine, state folder (`~/.local/state/watchyard`) | Machine id, device token (0600), outbox, setup backups and ledger, keys (`owner.sealed` on the owner's machine), agent state, logs | Losing it loses unsent records and makes the machine a new one |
| Hub, data folder (`~/.local/share/watchyard`, `/data` in Docker) | `hub.db` (SQLite, WAL), backups, exports, the mirrored Android release | The long-term record; back it up |
| Hub, state folder | `hub_sign.key`, `pair_pepper`, the pull key | Kept outside the database on purpose |
| Desktop app (`~/.config/watchyard-desktop` and the OS equivalents) | Tab list, the encrypted hub token, Chromium's per-tab sessions | Never sent anywhere |
| Android app | Device token in the Keystore-backed secure store, a last-known cache, widget snapshot | Never holds Claude credentials |

Folders per OS and every file: [paths.md](docs/spec/paths.md). The hub schema migrates itself at
start after copying the database file ([schema.sql](docs/spec/schema.sql)).

## Security boundaries

| Boundary | What protects it |
| --- | --- |
| Machine → hub | One bearer token per device, stored hashed on the hub. Machine tokens can only write records; phone and browser tokens read; owner sessions come from one-time links ([tokens.md](docs/spec/tokens.md), [pairing.md](docs/spec/pairing.md)). Plain HTTP only to loopback or a tailnet; otherwise HTTPS. |
| Hub → clients | Replies are lists of accepted ids. Documents the hub relays (matrix, certificates, revocations, link frames) are signed with keys devices pinned at pairing; a stolen database cannot forge them because the hub signing key lives outside it. |
| Hub's own authority | The owner key, not the hub, certifies machines and widens the peers matrix. Machines can refuse hub-signed widenings (`peers.hub_may_widen = false`). |
| Sessions → owner key | Owner-key commands refuse to run inside a Claude Code session (environment, process ancestry and a terminal check), and the passphrase is never stored. This stops accidents and injected instructions; it is not a sandbox ([keys.md](docs/spec/keys.md) 8.1). |
| Content | Prompts, code, tool input and output are removed before a record is written. The tail of Claude's last message is kept only while `privacy.last_message` is on. Peer message text never reaches the hub in clear. |
| claude.ai sign-ins | Stay in per-tab browser sessions on the device. The hub never receives cookies or tokens. The desktop app's usage reader runs only after a per-account warning is accepted, and stops on 401, 403 or 429. |
| Desktop app | Context isolation and the sandbox on every view, a fixed IPC allow-list, a strict CSP, `wy` run from an argv allow-list with secrets on stdin, Electron fuses flipped in packages ([desktop.md](docs/spec/desktop.md) C2-C3). |
| Supply chain | Release archives with SHA-256 sums and GitHub build attestations that the install scripts verify; the Android updater installs only an APK signed with the same key as the installed app. |

The threat model, including what the owner key and the signing guard cannot do, is in
[keys.md](docs/spec/keys.md) and summarised in [docs/guide/security.md](docs/guide/security.md).

## Repository layout

| Path | What |
| --- | --- |
| `cmd/wy/` | The `wy` command: subcommand dispatch only |
| `cmd/webui-dev/` | A mock hub for developing the board and the Android app |
| `internal/` | All Go code, one package per job (below) |
| `webui/` | The board: plain ES modules and CSS, embedded into `wy`; no build step |
| `app/` | The desktop app (Electron), its own `package.json`; see `app/README.md` |
| `phone/` | The Android app (Expo, React Native), its own `package.json`; see `phone/README.md`. `phone/android` and `phone/ios` are generated by `expo prebuild` and never committed. iOS code is kept compiling but not released. |
| `plugin/watchyard/` | The Claude Code plugin: hooks running `wy hook`, skills |
| `plugin/watchyard-pane/` | The Claude Code pane mod (TypeScript), installed by `wy term setup --pane` |
| `.claude-plugin/marketplace.json` | Lets Claude Code add this repository as a plugin marketplace |
| `deploy/` | Dockerfile, compose files, Caddyfile, systemd unit for the hub |
| `install/` | `install.sh` and `install.ps1`, published with each release; see `docs/RELEASING.md` |
| `docs/spec/` | The behaviour specs: the source of truth |
| `docs/guide/` | User guides |
| `docs/design/screens/` | Design mock-ups of the screens (HTML) |
| `docs/TESTING.md` | An ordered manual test walk-through on real machines |
| `docs/RELEASING.md` | How the maintainer signs and publishes releases |
| `test/` | End-to-end suites (setup, machines, terminal, push, peers, Android) |
| `testdata/`, `scripts/fixtures/` | Redacted real payloads and the maintainer-only scripts that make them |
| `scripts/` | CI checks, release build, icon generation, the pane check |

## Go packages

Client side (runs on every machine):

| Package | Job | Depends on |
| --- | --- | --- |
| `internal/version` | Build version | — |
| `internal/brand` | The project's and the command's names | — (leaf) |
| `internal/paths` | Config, state, data folders per OS | brand |
| `internal/config` | `config.toml` | paths |
| `internal/record` | Envelope, seven kinds, ids, content rules | — |
| `internal/outbox` | Local queue | record, paths |
| `internal/identity` | Machine id, slots, seats | paths, record |
| `internal/clientenv` | Folders, config, machine, slot and cached login for one client call | paths, config, identity, record |
| `internal/detach` | Detached background processes, debounce stamp files | — |
| `internal/hook` | `wy hook` | clientenv, detach, identity, leases, record, outbox |
| `internal/statusline` | `wy statusline` | clientenv, record, outbox |
| `internal/scan` | `wy scan`, `wy agents` | record, outbox, identity, paths |
| `internal/status` | `wy status` | outbox, record, identity, hub/state (pure fold) |
| `internal/ccjobs` | Read-only, best-effort reader of Claude Code's background-job files (job states, pins, daemon log) | — (leaf) |
| `internal/client` | `wy connect`, `wy flush`, `wy outbox drain` (the remote end of a pull), `<state>/hub.json` | outbox, record, config, paths, drainproto |
| `internal/drainproto` | The pull wire protocol (hello, batch, ack, refuse), shared by both sides | — (leaf) |
| `internal/directpush` | Direct ntfy from a machine the hub has not heard from: policy cache, episode scan, the eight conditions, markers | client, config, outbox, record, status, hub/state (pure fold) |
| `internal/machineinfo` | Builds and queues `machine_info` (reach, dial kind, backlog, held, direct push, timer) on change or every 6 h | client, config, directpush, identity, outbox, record, version |
| `internal/sshx` | Runs the operator's `ssh` (`WY_SSH_COMMAND`), `ssh -G`, `known_hosts` reads; never disables host key checking | — |
| `internal/machineadd` | `wy machine add/remove/repoint/rename`: probe, install, connect, forward blocks, pull authorization | sshx, update, client, config, identity, setup/{apply,backup,model}, atomicfile |
| `internal/update` | `wy update`: release manifest, verified download, replace and rollback, post-update steps, `--machines` | brand |
| `internal/doctor` | `wy doctor` (reach, held records, frozen hub, direct push, forward port; on the hub also push and pull-target checks, reading `hub.db` read-only with the SQLite driver, no hub package) | paths, config, identity, outbox, client, directpush, setup/ledger, setup/backup, share |
| `internal/timer` | OS timer running `wy tick --quiet` every minute | brand |
| `internal/picker` | `wy account next`: seat matching, the `lanes` ranker (readings, token meter, leases, burst caps, batches) and the projection strategies, hub reads (`/v1/usage` with the machine token, or the board token), one outbox pass for limit samples, token rows and hook events; each account's calibration (the hub's, the kept copy in `next/calibration.json`, or a local fit) and its meter margins; each leased session counted once from its local rows or the hub's burn, and other machines' leases kept in `next/hub_leases.json` | config, record, leases, calib, atomicfile |
| `internal/calib` | Online calibration of `wy account next` (setup.md 8.5, Calibration): used readings and pairs, the meter factor, ratio and lane price with their blending and restarts, the meter-margin corrector; the usage watch (weekly drops, server resets and their masks) and the usage relation (daily 5 h per 7 d ratio, capacity index per model, limit changes); pure, shared by the hub and the picker | — (leaf) |
| `internal/leases` | The lease ledger of `wy account next`: append-only day files with an fsync per line, the ledger lock, the hook's `launched`/`ended` marks | — (leaf) |

Setup side (`wy setup`, `wy account`, `wy uninstall`, `wy tick`):

| Package | Job | Depends on |
| --- | --- | --- |
| `internal/atomicfile` | Write-to-temp-then-rename file writes | — |
| `internal/jsonedit` | Byte-preserving edits of `settings.json` and `.claude.json` | — |
| `internal/claudecli` | The `claude` CLI contract: run as a folder, parse `--json`, goal-state outcomes | — |
| `internal/service` | Hub service units (systemd user, launchd, Windows task), linger | setup/model, timer, brand |
| `internal/share` | Links between folders, MCP server sync, Windows synced copies (`share.Sync`) | setup/model, identity, jsonedit, atomicfile |
| `internal/account` | Add, log in, unshare and remove an account folder | setup/model, claudecli |
| `internal/setup/model` | State, Choices, Plan, Action, Ledger types; share items, palette | claudecli |
| `internal/setup/detect` | Read-only scan of the machine into `model.State` | setup/model, identity, config, claudecli, setup/statuswrap, setup/launcher |
| `internal/setup/plan` | `Plan(State, Choices)`: pure, deterministic action list and plan id | setup/model, identity, version, setup/launcher, setup/statuswrap |
| `internal/setup/render` | Plan as text and JSON | setup/model, identity |
| `internal/setup/apply` | Runs a plan step by step, journaled, rolls back on failure | setup/model, setup/backup, setup/ledger, atomicfile (the rest through `apply.Deps`) |
| `internal/setup/backup` | Backup sets, journal, `--undo` and `--undo-list` | setup/model, atomicfile |
| `internal/setup/ledger` | What wy installed (`setup/ledger.json`) | setup/model, atomicfile |
| `internal/setup/launcher` | Launcher scripts per shell and OS | — |
| `internal/setup/statuswrap` | Wrap and unwrap a status line command | — |
| `internal/setup/pluginmkt` | The `wy-local` marketplace folder and plugin install | claudecli, setup/model, `plugin/` (embedded) |
| `internal/setup/tui` | The five setup screens (`golang.org/x/term` raw mode, line prompts otherwise) | setup/model |
| `internal/setup/testtree` | Test helper: build trees, snapshot and diff them | — |

Terminal side (`wy board`, `wy term`, `wy open`, `wy ssh`, `wy hook --title`):

| Package | Job | Depends on |
| --- | --- | --- |
| `internal/termenv` | The injected environment (`Env`: lookups, GOOS, home, TTY facts) every terminal package reads instead of `os` | — (leaf) |
| `internal/board` | The board view model (versioned `View`), ordering and text, the local source (outbox fold), the board token, view-v1 golden | termenv, record, identity, status, setup/model, atomicfile, hub/state (pure fold) |
| `internal/board/hubsrc` | Hub source: read API, `/v1/stream`, offline merge with the outbox, `last-view.json` | board, atomicfile |
| `internal/board/tui` | `wy board` screen: raw-mode TUI, `--once`, `--ndjson` | board, termenv |
| `internal/termctl` | Tab and window titles, the focus allow-list, the OS opener (`xdg-open`, `open`, `start`); `fakeopen` test seam | termenv |
| `internal/warp` | Warp `tab_configs` per OS, detection, colour mapping, TOML render | termenv, setup/model |
| `plugin/watchyard-pane` | The Claude Code pane mod (TypeScript; not Go): runs `wy` as argv[0] only | — (embedded in `plugin/`, installed by `setup/pluginmkt`) |

Hub side (same binary, `wy hub …`):

| Package | Job | Depends on |
| --- | --- | --- |
| `internal/hub/store` | SQLite, migrations, backup, prune | — |
| `internal/hub/auth` | Tokens, scopes, `wy hub token` | hub/store |
| `internal/hub/state` | Session state fold, question detector, gone | record (+ hub/store for persistence) |
| `internal/hub/usage` | Windows, resets, pace | record, hub/store |
| `internal/hub/calibrate` | The calibration job: every 15 minutes watches each seat's new samples (resets, drops) into `seat_watch`, folds samples, token rows and leases (by cursor and index, outside the reset masks) into `seat_calibration` and the usage relation, and hands the usage notices (push.md 3.9) to the engine | calib, scan, hub/store, hub/push/{model,rules} |
| `internal/hub/ingest` | `POST /v1/ingest` | record, hub/store, hub/auth, hub/state, hub/usage |
| `internal/hub/events` | Stream broker (SSE fan-out) | — |
| `internal/hub/api` | Read API, SSE, mounts ingest and webui; pairing, devices, push targets, rules and alerts handlers | hub/store, hub/auth, hub/ingest, hub/events, hub/state, hub/usage, hub/pair, hub/push/{model,rules,webpush,ntfy,link,refresh}, webui |
| `internal/hub/pair` | Pairing codes, owner links, claim, rate limits, QR | hub/store, hub/auth, paths, `rsc.io/qr` |
| `internal/hub/push/model` | Shared push types: triggers, alerts, settings, targets, payload, `Sender` | — (leaf) |
| `internal/hub/push/clock` | Real clock and the manual `Fake` the engine runs on | — (leaf) |
| `internal/hub/push/rules` | Pure rule logic: triggers, episode keys, due/stale, reconcile decisions, quiet hours, text | hub/push/model |
| `internal/hub/push/action` | Signed Snooze/Seen action tokens | hub/push/model |
| `internal/hub/push/engine` | Alert engine: reconcile after each ingest batch, scheduler, fan-out, retries | hub/store, hub/events, hub/ingest (change set), hub/push/{model,clock,rules,action} |
| `internal/hub/push/webpush` | Web Push sender: VAPID, `aes128gcm` encryption, endpoint allow-list | hub/store, hub/push/model |
| `internal/hub/push/ntfy` | ntfy sender, topics | hub/push/model |
| `internal/hub/push/refresh` | The widget `Refresher` (push.md 13.5): link targets only, sends `refresh` frames through the link sender | hub/store, hub/events, hub/push/{model,link,clock} |
| `internal/hub/push/webpushtest`, `ntfytest` | Fake Web Push service and ntfy stub for tests | — |
| `internal/hub/pull` | Pull scheduler: SSH sessions to pull targets (pinned host keys, the hub's key in `<state>/pull`), backoff, `RunNow`; records go through the ingester | hub/store, hub/ingest, drainproto, atomicfile, record, hub/api (the `Puller` and `PullStatus` types only) |
| `internal/hub/cli` | `wy hub …` and `wy phone pair`; `serve` builds the store, senders (Web Push, ntfy, link), engine, API, pull scheduler, widget refresher and calibration job, and shuts them down in order (API, HTTP, pull scheduler, refresher, engine, calibration job, store) | everything above, webui |
| `webui` | Embedded board and phone app (PWA) | — |

Keys, the link and peers (both sides):

| Package | Job |
| --- | --- |
| `internal/keys` | Formats, fingerprints, sealing, signed documents, the signing guard, `keys.Widens` (keys.md) |
| `internal/link`, `internal/linkclient` | The `/v1/link` wire format; the machine side of the link (link.md) |
| `internal/peers` | Matrix evaluation, threads, loop guard, limits, the message wrapper (peers.md) |
| `internal/peerlink` | The agent's path to sessions on other machines |
| `internal/agent` | `wy agent`: the per-machine service that holds the link and the control socket |
| `internal/desktop` | `wy desktop`: the JSON commands the desktop app runs |
| `internal/localtime` | Local time formatting for command output |
| `internal/hub/keys`, `hub/link`, `hub/peerrelay` | Hub key store and owner-key confirmation, the per-device link queue, the peer relay |
| `internal/hub/appmirror` | Mirrors the Android release from GitHub Releases for the in-app updater |
| `internal/hub/hublock` | One hub process per data folder |

Rules:

- `cmd/wy` only routes to packages; no logic there.
- Client packages never import `internal/hub/...`, with one exception: the pure state fold
  in `hub/state`, so `wy status` and the hub can never disagree. That fold imports only
  `record`.
- `internal/hub/push/...` never imports `hub/api`, `hub/cli`, `hub/pair` or `webui`: the API
  calls the engine (`api.Engine`, `model.AlertService`), never the other way round, and
  alerts reach the stream through the `events.Broker` the engine is handed. `push/model` and
  `push/clock` are leaves, `push/rules` imports only `push/model`. Enforced by
  `internal/hub/push/deps_test.go` (`go list -deps`).
- `internal/drainproto` is a leaf that both the client (`wy outbox drain`) and `hub/pull`
  import. `internal/directpush` imports only the pure `hub/state` fold from the hub side.
  `hub/pull` hands records to the ingester (`ingest.IngestWith`, `Contact`), never to the
  HTTP API; it imports `hub/api` only for the `Puller` interface types the API calls it
  through. Enforced by `internal/hub/pull/deps_test.go`.
- Test seams that must not reach a release build use the `fakeclock` build tag
  (`internal/hub/cli/hooks_fakeclock.go`: fake clock control socket, test push client); the
  end-to-end test in `test/e2e/push` builds `wy` with it.
- The `fakeopen` build tag (`internal/termctl/fakeopen.go`) is the same kind of seam: with
  `WY_TEST_OPENER=<file>` the OS opener records its argv instead of running; the terminal
  end-to-end test in `test/e2e/terminal` builds `wy` with it. Release builds contain neither
  `fakeclock` nor `fakeopen`; `scripts/ci/check-release-seams.sh` (`make release-check`, a CI
  step) builds with and without the tags and fails if the release binary has either seam.
- `internal/board/**` never imports hub internals beyond the pure `hub/state` fold (and
  `status`, which itself imports only that fold): no `hub/store`, `hub/api`, `hub/ingest` or
  `hub/cli`. It never calls `/v1/ingest` and never imports `internal/client` or
  `hub/ingest`; the board only reads. Enforced by `internal/board/noingest_test.go`.
- `internal/board/tui` depends only on `board` (and `termenv`); `board/hubsrc` only on `board`
  and `atomicfile`. `internal/termenv` is a leaf. `internal/termctl` and `internal/warp` see
  the machine only through a `termenv.Env`.
- `board`, `termctl`, `warp` and `termenv` never call `os.UserHomeDir` or read `HOME`
  (or `USERPROFILE`) themselves: the caller injects the environment, so tests run against
  temp homes and a real config is unreachable. Enforced by `internal/termenv/lint_test.go`.
- Hub packages talk to each other only through the store or plain function calls on
  records; no package reaches into another's tables.
- Dependencies stay minimal: standard library first; `modernc.org/sqlite` (pure Go, so builds
  stay `CGO_ENABLED=0` static binaries); one TOML parser. A new dependency needs a reason in
  its pull request, and the spec that needs it says so.


## Building and testing

```
make build          # ./dist/wy, a static binary (CGO_ENABLED=0)
make check          # gofmt, go vet, go test ./..., spec checks
make e2e-terminal   # wy board / term / open / ssh end to end (fake opener, temp HOME)
make e2e-machines   # SSH into a container (podman or docker)
make e2e-setup      # wy setup in Fedora and Ubuntu containers
make pane-check     # the Claude Code pane mod (needs claude on PATH)
make release-check  # a release build has no test seams
make app-test       # desktop app: lint and unit tests (no display)
make app-smoke      # desktop app smoke tests under xvfb, against a real hub built from here
make app-dist       # desktop app packages in app/dist (unsigned, never published)
make app-e2e        # the packaged AppImage end to end (after app-dist)
make phone-check    # Android app: typecheck, lint, tests, prebuild checks
make phone-android-debug  # an unsigned debug APK (needs ANDROID_HOME)
make e2e-android    # the Android app on an emulator against a real hub
```

npm runs only inside `app/` and `phone/`. Development setup, gates per component and the
pull request process are in [CONTRIBUTING.md](CONTRIBUTING.md).
