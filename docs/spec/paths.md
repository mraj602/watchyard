# Paths

Status: spec v1. Implemented in `internal/paths`. Every other package gets
its folders from there; nothing else builds paths from `$HOME`.

`wy` uses three folders:

| Folder | Holds | Can be lost? |
| --- | --- | --- |
| **config** | `config.toml` only. | Recreated by `wy setup` / `wy connect`. |
| **state** | Machine id, device token, outbox, caches, logs, setup backups. | Losing it loses unflushed records and makes this a new machine. |
| **data** | The hub database and its backups. Only on the machine that runs the hub. | No: this is the long-term record. |

## 1. Locations

| | Linux, WSL, BSD | macOS | Windows |
| --- | --- | --- | --- |
| config | `$XDG_CONFIG_HOME/watchyard` (default `~/.config/watchyard`) | `$XDG_CONFIG_HOME/watchyard` (default `~/.config/watchyard`) | `%LOCALAPPDATA%\Watchyard\config` |
| state | `$XDG_STATE_HOME/watchyard` (default `~/.local/state/watchyard`) | `$XDG_STATE_HOME/watchyard` (default `~/.local/state/watchyard`) | `%LOCALAPPDATA%\Watchyard\state` |
| data | `$XDG_DATA_HOME/watchyard` (default `~/.local/share/watchyard`) | `$XDG_DATA_HOME/watchyard` (default `~/.local/share/watchyard`) | `%LOCALAPPDATA%\Watchyard\data` |

XDG variables are used only when set to an absolute path (the XDG rule); otherwise the
default applies. On Windows, `%LOCALAPPDATA%` is used if set and absolute, else
`<home>\AppData\Local`; XDG variables are ignored there.

**Overrides** (in this order, first wins):

1. `WY_CONFIG_DIR`, `WY_STATE_DIR`, `WY_DATA_DIR`: one folder each.
   Overrides must be absolute paths: a relative one is an error (a hook's working folder
   is arbitrary, so a relative path would scatter state).
2. `WY_HOME`: sets all three to `$WY_HOME/config`, `$WY_HOME/state`, `$WY_HOME/data`. Used by
   tests and by running a second, throwaway `wy` next to the real one.
3. The table above.

`wy paths` prints the three folders and where each came from; `wy doctor` shows the
same.

## 2. Decisions and why

### Linux: `~/.config/watchyard` is shared

An Electron app named `watchyard` would keep its user data in `~/.config/watchyard`
(`~/Library/Application Support/watchyard` on macOS, `%APPDATA%\watchyard` on Windows), and
older versions of Watchyard did. `wy` still uses `~/.config/watchyard` as its config folder,
so the folder can hold other files that `wy` does not own.

The desktop app (`app/`, package `watchyard-desktop`) sets its user data explicitly, before
`ready`, to `<appData>/watchyard-desktop` (desktop.md C1):

| | Linux | macOS | Windows |
| --- | --- | --- | --- |
| desktop app user data | `~/.config/watchyard-desktop` | `~/Library/Application Support/watchyard-desktop` | `%APPDATA%\watchyard-desktop` |

Electron would otherwise derive `Watchyard` from `productName`. `WY_APP_USER_DATA`
(absolute) overrides the folder for development and tests, and only in an unpackaged build.

`wy` keeps to strict rules in `~/.config/watchyard`:

- `wy` owns exactly one name in that folder: `config.toml` (plus its transient
  `.config.toml.<random>.tmp` during an atomic write, and `config.toml.bak` written by
  `wy setup`).
- `wy` never creates any other file there, never lists, cleans, moves or deletes the folder,
  and never reads other files in it.
- `wy uninstall --purge` removes only `config.toml` and `config.toml.bak`, never the folder.
- Neither desktop app writes `config.toml`. The desktop app reads what it needs by running
  `wy` (`wy desktop info --json` and the other commands of desktop.md C11); it does not open
  `config.toml` or any `wy` folder itself.
- If `wy` creates the folder, it uses mode `0700`, as Electron does.

Why not a separate name such as `~/.config/watchyard-wy` or `~/.config/wy`: the
docs all say `~/.config/watchyard/config.toml`; `wy` is a short, generic
name likely to clash with other tools. The rules above make sharing the folder harmless:
there is no file-name overlap and `wy` never deletes the folder.

Everything that `wy` writes often, and everything that matters (outbox, token, machine id),
lives in the state folder, which nothing else uses.

### macOS: XDG layout, not `~/Library/Application Support`

An earlier design put macOS state in `~/Library/Application Support/Watchyard`. But an Electron
app's default user-data folder on macOS is `~/Library/Application Support/<name>`, and the
default APFS volume is case-insensitive, so that would be the **same folder** as an Electron
app's. Putting the outbox and the device token inside an app's folder is exactly the clash we
want to avoid. So on macOS `wy` uses the same `~/.config` / `~/.local/state` / `~/.local/share`
layout as on Linux. Command-line tools commonly do this on macOS (git, gh, fish), it is the
same on every Unix machine (simpler docs, the same paths on SSH servers), and there is no
clash: the desktop app uses its own `~/Library/Application Support/watchyard-desktop`.

launchd agents still go in `~/Library/LaunchAgents`, and their logs go to the
state folder.

### Windows: `%LOCALAPPDATA%`, not `%APPDATA%`

Electron's user data on Windows is `%APPDATA%\watchyard` (roaming). `wy` uses
`%LOCALAPPDATA%\Watchyard`, which does not clash, and which does not roam: the machine id,
device token and outbox are per machine and must never be copied to another computer by a
roaming profile. Config also lives there, so a roaming profile does not carry one machine's
hub token to another. Subfolders `config`, `state`, `data` keep the three roles apart inside
one folder.

## 3. What lives where

```
<config>/
  config.toml                  user settings (below); mode 0600

<state>/                       mode 0700
  machine.json                 machine id and name (identity.md)
  machine.lock                 taken while creating machine.json
  token                        device token, one line, mode 0600 (tokens.md)
  outbox/                      outbox.md
  identity/                    per-slot login cache (identity.md)
  scan/offsets.json            transcript read offsets (wy scan)
  scan/history.json            slots already history-imported
  statusline/last.json         last limit_sample per session (once-a-minute rule)
  statusline/wrapped-<session>.txt  last good output of the wrapped status line (timeout fallback)
  logs/wy.log                  rotated at 5 MB, 3 kept
  backup/<UTC time>/           journal + saved files of each wy setup/account/uninstall run (setup.md 7)
  setup.lock                   held while a setup run applies
  setup/ledger.json            what wy has installed now (setup.md 9.3)
  plugin/marketplace/          the local wy-local plugin marketplace (setup.md 5.2)
  share/mcp-base.json          last synced mcpServers, mode 0600 (setup.md 5.4)
  share/mcp-conflicts.jsonl    MCP merge conflicts (names only)
  share/copies.json            Windows synced copies (setup.md 5.7)
  hub.json                     last known hub version and record_v_max (machines.md 7.2)
  machine-info.json            last machine_info sent (machines.md 6.3)
  machines.json                operator: machines added over SSH, mode 0600 (machines.md 2)
  push/direct.json             direct ntfy topic and token, mode 0600 (machines.md 5.2)
  push/policy.json             direct push policy from the hub (machines.md 5.3)
  push/direct/<hash>.json      one marker per directly pushed episode (machines.md 5.4)
  update/                      downloads and verified archives (machines.md 7.3); cache/ for other platforms
  hub.sock                     forward mode with --forward-socket only (machines.md 3.3)
  pull/                        hub host only: id_ed25519(.pub), generated ssh_config and known_hosts (machines.md 3.4)
  board/token.json             read-only board token for wy board, the pane and wy account next (older hubs), mode 0600 (terminal.md T3)
  next/leases/<YYYY-MM-DD>.jsonl  wy account next: the lease ledger, append-only day files (UTC),
                               mode 0600, kept 8 days (setup.md 8.5); wy hook appends launched/ended
  next/leases.lock             held while wy account next reads the ledger, ranks and appends
  board/last-view.json         last board view shown, mode 0600 (terminal.md T3)
  term/title-<session>.json    last tab title per session, pruned after 7 days by wy tick (terminal.md T6)
  keys/                        mode 0700: device.json, owner.sealed (owner machine), owner.pub, hub.pub,
                               certs.json, matrix.json, revocations.json (keys.md 10)
  hub_sign.key                 hub machine only: the hub signing key, mode 0600, exported with the hub (keys.md 3.4)
  agent/                       wy agent: link.json (last seq), audit-out.jsonl, agent.sock on macOS (peers.md 2)
  peers/sessions/<session>.json  written by wy hook when peers are on: mode, human_at (peers.md 2.4)
  peers/delivered/<session>    time of the agent's last delivery into that session
  peers/threads.json, peers/seen.json, peers/audit.jsonl, peers/off   (peers.md 6.2, 8.4, 10, 11)
  logs/agent.log               rotated like wy.log

<data>/                        mode 0700, hub only
  hub.db, hub.db-wal, hub.db-shm
  backups/hub-<UTC time>.db    wy hub backup
  exports/hub-<UTC time>.wyx   wy hub export, mode 0600 (machines.md 8.1)
  app/android/                 mirrored phone release: current and previous APK + manifest (phone.md P16.2)
```

Runtime sockets (Linux, `$XDG_RUNTIME_DIR`): `wy/agent.sock` and `wy/r/<16 hex>.sock`, folders The agent socket falls back to `$TMPDIR/wy-<uid>/agent.sock` when the path would exceed the sun_path limit (103 bytes macOS, 107 Linux).
0700, sockets 0600 (peers.md 2.3, 8.6). `wy` reads, never writes, each slot's
`<config>/sessions/<pid>.json` and never opens `sessions/*.key` (identity.md 4).

Outside these folders, machine management writes only `~/.ssh/watchyard.conf` and one `Include` line in
`~/.ssh/config` on the operator (forward mode), and one line in `~/.ssh/authorized_keys` on a
pulled remote (machines.md 3.3, 3.4); both are journaled and undone by `wy machine remove`.
The terminal integration writes only its own `watchyard-*.toml` tab configs in Warp's tab config folder
(terminal.md T8), journaled and undone by `wy term warp off` or `wy setup --undo`.

Inside a Docker image the hub uses `WY_DATA_DIR=/data` (a volume).

## 4. `config.toml`

TOML, read with a small, well-maintained parser (candidate
`github.com/pelletier/go-toml/v2`). Unknown keys are kept on rewrite and reported by
`wy doctor`. Written atomically (temp file, fsync, rename), preserving comments where the
parser allows; `wy config set` is the supported way to change it. `wy config set hub.url <url>`
applies the same check as `wy connect` (plain `http` only to loopback or Tailscale
(`*.ts.net` names, `100.64.0.0/10` addresses), unless `hub.insecure_lan` is true or `--insecure-lan` is given, which
records it) and stores the normalised scheme://host[:port].

```toml
# Watchyard (wy) settings. Unofficial; not affiliated with Anthropic.
version = 1

[hub]
url = "https://hub.tailnet-name.ts.net"   # empty: no hub yet, or pulled
insecure_lan = false                         # allow plain HTTP to a non-loopback address
reach = "direct"                             # direct | forward | pull (machines.md 3)
dial = ""                                    # forward: "127.0.0.1:47830" or "unix:<path>"

[push.direct]                                # (machines.md 5); topic/token in <state>/push/direct.json
enabled = false
server = "https://ntfy.sh"
private = true
after_s = 120

[update]                                     # (machines.md 7.3)
base_url = ""                                # default GitHub releases; WY_INSTALL_BASE_URL wins
verify_attestation = true                  # check the build attestation when gh is installed

[machines]                                   # operator (machines.md 4.4)
hub_admin = ""                               # "local" or a command prefix, e.g. "ssh hub wy"

[privacy]
last_message = true                          # send derived.last_message (on for your own hub)

[hooks]
tool_events = false                          # PreToolUse/PostToolUse for all tools

[statusline]
wrap = "bash ~/.claude/statusline.sh"        # the user's original status line command
show = true                                  # false: print only the wrapped line (samples are still recorded)

[outbox]
fsync = true
max_mb = 50
keep_days = 7

[slots]
extra = []                                   # extra config folders outside ~/.claude*
labels = { "~/.claude-2" = "work" }          # local fallback; the hub's seat label wins
colours = { "~/.claude-2" = "#f59e0b" }      # (setup.md 9.2)

[share]                                      # (setup.md 9.2)
primary = "~/.claude"
mcp = true                                   # true | false | "external"
mcp_exclude = []

[peers]                                      # (peers.md 2.4, 8.5)
enabled = false                              # wy hook writes the session files; wy peers works only when true
delivery = "honest"                          # honest | accept | courier (peers.md 8.5)

[next]                                       # wy account next (setup.md 8.5)
strategy = "lanes"                           # lanes | smart | expiring | most-room | spread | fill | sticky
model = ""                                   # default --model
session_5h = 15                              # points of a 5-hour window one new session uses
session_7d = 2                               # points of a weekly window one new session uses
session_min = 120                            # how long a session burns, minutes
stale_min = 30                               # readings older than this are uncertain
reserve_min = 12                             # how long a lease counts as a pending session
sticky_margin = 10                           # sticky: points under each cap that end it

[next.accounts."me@example.com"]             # per account: email (recommended), seat id, folder or label
mode = "aggressive"                          # protected | conservative | normal | aggressive
# cap_5h, cap_7d, cap_model, reserve_pct, priority, weight, exclude, hours = "09:00-18:00", days = "mon-fri"

[terminal]                                   # (terminal.md T12)
titles = false                               # written only by wy term titles on|off (wy config set refuses it)
band = true                                  # pane: band above the prompt when another session needs you
pane_auto_open = true                        # pane: open as a sidebar in wide fullscreen sessions
source = "auto"                              # board: auto | hub | local
ascii = false
```

The device token is **not** in this file (it lives in `<state>/token`), so `config.toml` can
be shown or shared without leaking anything.
