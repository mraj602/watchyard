# Watchyard

[![ci](https://github.com/mraj602/watchyard/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/mraj602/watchyard/actions/workflows/ci.yml)
[![app](https://github.com/mraj602/watchyard/actions/workflows/app.yml/badge.svg?branch=main)](https://github.com/mraj602/watchyard/actions/workflows/app.yml)
[![phone](https://github.com/mraj602/watchyard/actions/workflows/phone.yml/badge.svg?branch=main)](https://github.com/mraj602/watchyard/actions/workflows/phone.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

An open-source, self-hosted board for many Claude Code accounts on many machines: every
session's state, every account's limits, and a notification when a session needs you. It is
built to be agent-neutral, and more coding agents (Codex CLI, Gemini CLI) are next on the
[roadmap](#roadmap).

**Website: <https://mraj602.github.io/watchyard/>**

> **Unofficial.** Watchyard is an independent community project. It is not affiliated with,
> endorsed by, sponsored by or connected to Anthropic. "Claude" and "Claude Code" are
> trademarks of Anthropic, PBC, used here only to describe compatibility. Use it only with
> your own accounts and within the terms of your plan. See [Terms and risk](#terms-and-risk).

If you run Claude Code in two or more accounts (say work and personal), on a laptop and a
server or two, you end up checking terminals to see which session is waiting for you and
which account still has room. Watchyard gives you one view of all of it, on your own hub:

- **One board** of every session on every machine, grouped by account: working, needs
  permission, asked a question, done, failed.
- **Every account's limits** (5-hour, 7-day, per model) with reset times and history.
- **Alerts** when a session needs you, on an Android app with its own connection to your hub
  (no Google push service and no proprietary Google code), as Web Push, through ntfy or as
  desktop notifications.
- **Account setup and sharing:** one command labels your Claude Code config folders and makes
  them share settings, skills, plugins and MCP servers. Logins are never touched.
- **Peers:** Claude Code sessions in your different accounts and machines can message each
  other, gated by a matrix you sign with your own key. The hub relays ciphertext only.

## Screenshots

![The board with sample data: sessions that need you, every session, account limits and peers, then an alert and a limits widget on Android](docs/media/demo.gif)

Recorded from the mock hub with sample data (`scripts/demo/record.cjs`). All the screenshots
below use sample data too.

The desktop app: the board in its Hub tab, and one tab per claude.ai account with that
account's limits above it.

<p>
  <img src="docs/media/desktop-hub.png" alt="The Watchyard desktop app's Hub view: the board's Now screen with four sessions that need you and three accounts in the sidebar" width="49%">
  <img src="docs/media/desktop.png" alt="The Watchyard desktop app with one tab per claude.ai account and a usage strip showing the active account's 5-hour and 7-day limits" width="49%">
</p>

The Android app (Now and Accounts) and the board in a terminal (`wy board`):

<p>
  <img src="docs/media/android-needs-you.png" alt="Watchyard Android app, Now screen: three account limit rings and a Needs you list with a rate-limited session, a question from Claude and a permission request" width="240">
  <img src="docs/media/android-accounts.png" alt="Watchyard Android app, Accounts screen: each account's 5-hour and 7-day limits with reset times and pace forecasts" width="240">
</p>

![wy board in a terminal: sessions that need you, then each account's sessions with state, context used and time in state](docs/media/terminal-board.png)

Android home-screen widgets (rendered previews from the app):

<p>
  <img src="phone/assets/widget-previews/wy-limits.png" alt="Limits widget: four accounts with 5-hour usage bars and reset times" width="420">
  <img src="phone/assets/widget-previews/wy-ring.png" alt="Ring widget: one account's tighter window as a ring" width="160">
</p>
<p>
  <img src="phone/assets/widget-previews/wy-needs.png" alt="Needs-you widget: the sessions waiting for you" width="420">
  <img src="phone/assets/widget-previews/wy-sessions.png" alt="Sessions widget: running, needs input and done counts" width="320">
</p>

## How it works

```mermaid
flowchart LR
  subgraph M["Each machine"]
    CC["Claude Code sessions<br/>(one config folder per account)"]
    WY["wy hook / wy statusline<br/>outbox on disk"]
    CC -- "hooks, status line" --> WY
  end
  D["Desktop app<br/>(claude.ai tabs, optional)"] -- "usage records via wy" --> WY
  WY -- "HTTPS, every minute<br/>and after each hook" --> H["Hub: wy hub serve<br/>SQLite, states, usage, alerts"]
  H --> B["Board in a browser<br/>and in the terminal"]
  H -- "own encrypted link" --> A["Android app<br/>notifications, widgets"]
  H -- "Web Push / ntfy" --> P["Other phones"]
```

- `wy` is one static Go binary. Claude Code runs it from hooks and the status line; it
  appends one record per event to a local outbox and exits. Nothing stays running except an
  optional per-machine agent for peers.
- Records go to the hub over HTTPS (or loopback, or your tailnet). If the hub is away they
  wait in the outbox and are sent later without duplicates.
- The hub is the same binary (`wy hub serve`) with one SQLite file. It works out session
  states and usage windows, sends alerts and serves the board. It never talks to Anthropic and
  never holds a Claude login.

More: [ARCHITECTURE.md](ARCHITECTURE.md).

## Quickstart

**Set it up with your coding agent.** Paste this into Claude Code, Codex or any agent that can
run commands:

```text
Help me install and set up Watchyard. Read
https://raw.githubusercontent.com/mraj602/watchyard/main/docs/guide/agents.md
and follow it: ask me what you need first, show me every plan before applying it,
and leave logins, tokens and passphrases to me.
```

**Or do it yourself.** Needs Claude Code, on Linux or macOS. This runs the hub on the same computer:

```sh
curl -fsSL https://github.com/mraj602/watchyard/releases/latest/download/install.sh | sh
wy setup            # shows a plan first; choose "Just this computer" for the hub
wy hub owner-link   # open the printed link: the board, signed in as the owner
```

The script puts one file, `wy`, in `~/.local/bin`, after checking its SHA-256 against the
release's `SHA256SUMS`. It edits no shell profile and no PATH: if `~/.local/bin` is not on
your `PATH`, it prints the line to add. It then starts `wy setup` when a terminal is
available (`--no-setup` skips that). On Windows, see [Releases](#releases).

**Build from source instead.** This needs Go 1.27+, `make` and `git`:

```sh
git clone https://github.com/mraj602/watchyard.git && cd watchyard
make build && install -D -m 0755 dist/wy ~/.local/bin/wy
```

Then run `wy setup` as above.

Restart your open Claude Code sessions afterwards so they load the plugin. Everything
`wy setup` changes is backed up; `wy setup --undo` reverses it. Next steps: a hub on a server,
more machines, the phone: [docs/guide/install.md](docs/guide/install.md).

## Components

| Component | What it is | Platforms | Docs |
| --- | --- | --- | --- |
| `wy` | The CLI on every machine: setup, hooks, status line, outbox, terminal board, peers | Linux, macOS; Windows with gaps (no peers or owner key) | [install](docs/guide/install.md), [accounts](docs/guide/accounts.md) |
| Hub | `wy hub serve`: storage, states, usage, alerts, the web board. Docker, systemd or a user service | Linux, macOS | [install](docs/guide/install.md#2-choose-where-the-hub-runs), [deploy/](deploy/README.md) |
| Board | The hub's web UI; installs on a phone as a web app; also `wy board` in a terminal and a pane inside Claude Code | Any modern browser | [board](docs/guide/board.md) |
| Desktop app | One tab per claude.ai account, a usage strip per account, the board in a Hub view | Linux, macOS, Windows | [desktop](docs/guide/desktop.md) |
| Android app | The board's screens, native notifications with Open, Snooze and Seen, six widgets | Android 12+ | [android](docs/guide/android.md) |
| Claude Code plugin | Hooks that run `wy hook`, skills for setup, status, accounts and peers | Claude Code | [board](docs/guide/board.md#claude-code-plugin-from-github) |

## Documentation

- [User guide](docs/guide/README.md): install, accounts and sharing, the board, desktop app,
  Android app, alerts, usage limits, peers and the owner key, how Watchyard talks to
  claude.ai, security and privacy,
  maintenance, troubleshooting, uninstall, FAQ.
- [ARCHITECTURE.md](ARCHITECTURE.md): components, data flow, storage, security boundaries,
  repository layout.
- [docs/spec/](docs/spec/README.md): the behaviour specs (records, API, schema, states,
  setup, push, keys, peers, and more). They are the source of truth.
- [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md), [CHANGELOG.md](CHANGELOG.md).

## Releases

Releases are on [GitHub Releases](https://github.com/mraj602/watchyard/releases). A tag
`vX.Y.Z` publishes:

- `wy` archives for Linux, macOS and Windows (amd64 and arm64), with `SHA256SUMS`, and the
  install scripts `install.sh` and `install.ps1`;
- the desktop app packages (AppImage, deb, rpm, dmg and the Windows installer), with
  `SHA256SUMS-desktop`;
- GitHub build-provenance attestations for every archive and package.

The Android APK is a separate release, `phone-vX.Y.Z`; the newest is
[Android 1.0.0](https://github.com/mraj602/watchyard/releases/tag/phone-v1.0.0). The maintainer signs it with their own
key, whose certificate's SHA-256 fingerprint is `a9e68504609ac2be216a35d787bb3e9d23b5fc66b9c4d939af2678e04724c89f`
([Android guide](docs/guide/android.md#verify-the-apk)).

Install `wy` on Linux or macOS with the one-liner in the [Quickstart](#quickstart). Add
`sh -s -- --version 1.0.0` to pick a version. On Windows, in PowerShell 5.1 or later:

```powershell
irm https://github.com/mraj602/watchyard/releases/latest/download/install.ps1 | iex
```

That puts `wy.exe` in `%USERPROFILE%\.local\bin`, after the same checksum check. To pass
options such as `-Version` or `-NoSetup`, run it as a script block, as the header of
[install/install.ps1](install/install.ps1) shows.

To check where a file came from, run `gh attestation verify <file> --repo mraj602/watchyard`.
The install script and `wy update` do it for you when the `gh` CLI is installed, and say so
when it is not (then only the checksum is checked). `wy update` keeps `wy` current ([maintenance](docs/guide/maintenance.md#wy-update)). The
desktop app and the Android app update as their guides describe.

## FAQ

**Does the hub see my claude.ai cookies, prompts or code?** No. Sign-ins stay in local
browser sessions on your devices, and the hub never receives cookies, tokens, prompts, code
or tool output. By default it receives the tail of Claude's last message in a turn, for
question detection; `wy config set privacy.last_message false` turns that off.
[Security and privacy](docs/guide/security.md).

**Do I need a server or Tailscale?** No. The hub can run on your laptop. A server keeps alerts
working while the laptop sleeps; Tailscale or a reverse proxy gives your phone an `https://`
address.

**Is there an iPhone app?** No, Android only. On an iPhone, install the board as a web app
(iOS 16.4+ for notifications) or use ntfy.

**Does it switch accounts when one hits a limit?** No, never. It shows which account has room;
you choose. `wy account next` recommends the folder to start your *next* session in, from each
account's login on this machine and its limits (`--env` prints `CLAUDE_CONFIG_DIR=…` for
scripts and agents that start sessions); it never moves a running session or changes a login.

More: [docs/guide/faq.md](docs/guide/faq.md).

## Roadmap

Watchyard is meant to be neutral: one board for whichever coding agents and providers you
use. In order of priority (plans, not promises; open an issue if one matters to you):

1. **More coding agents.** OpenAI Codex CLI, Google Gemini CLI and Antigravity next to Claude
   Code on the same board: the same session states, alerts and per-account limits, wherever the
   tool exposes them.
2. **Better analytics.** Usage, tokens and cost over time per account, provider, project and
   model; when and how often each account hits its limits; forecasts and exports.
3. **Artifacts across accounts.** Detect which account made each claude.ai artifact (and the
   other tools' equivalents), and let sessions in your other accounts use and share them
   through peers.
4. **Passkeys in desktop tabs.** claude.ai keeps offering passkeys after each sign-in, but
   Electron has no passkey prompt, so today they are added and used in your normal browser.
   Security keys first, then a phone as the passkey.
5. **Desktop updates** from inside the app, and code-signed macOS and Windows builds.
6. **A hosted hub,** for people who would rather not run their own. Self-hosting stays free.
7. **Android in app stores** (IzzyOnDroid, then F-Droid). Today the APK is on GitHub Releases.
8. **An iPhone app.** Today an iPhone uses the board as a web app or ntfy.

Not planned: shared or team hubs. A hub belongs to one person, and only you see your sessions,
accounts and usage.

## Similar tools

I could not find one tool that covers many accounts across many machines with session states,
alerts and peer messaging, but several good tools cover parts of it, and one of them may be all
you need:

| Tool | Focus | Compared with Watchyard |
| --- | --- | --- |
| [ccusage](https://github.com/ccusage/ccusage) | Usage reports from local Claude Code logs | Watchyard also records the official limit percentages per account, across machines, with alerts |
| [Claude-Code-Usage-Monitor](https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor) | Live terminal usage monitor with predictions | One account and machine; Watchyard adds a hub, session states and a phone |
| [claude-squad](https://github.com/smtg-ai/claude-squad) | Manage several agent sessions in one terminal | Runs sessions locally; Watchyard observes the sessions you already run, in many accounts |
| [CloudCLI / claudecodeui](https://github.com/siteboon/claudecodeui) | Web and mobile UI to drive Claude Code | Drives sessions; Watchyard tracks state and alerts across accounts and machines |
| [vibe-kanban](https://github.com/BloopAI/vibe-kanban) | A board that runs and orchestrates coding agents | Orchestration; Watchyard does not start or steer agents |
| [ccstatusline](https://github.com/sirmalloc/ccstatusline), [claude-hud](https://github.com/jarrodwatts/claude-hud) | Rich in-session status lines | Per session; Watchyard is the cross-session view (and wraps your status line) |
| [claude-swap](https://github.com/realiti4/claude-swap) | Switch between Claude Code accounts | Watchyard keeps all accounts side by side and never switches or rotates for you |

## Terms and risk

- Watchyard is unofficial and not affiliated with Anthropic.
- Its Claude Code integration uses documented extension points: hooks, the status line,
  plugins and `CLAUDE_CONFIG_DIR`. Logging in is always Claude Code's own `claude auth login`.
- The account tabs in the desktop app (and in the Android app) load claude.ai's own pages.
  You sign in on Anthropic's page; the sign-in stays in a local browser session on that
  device. Watchyard never reads, exports or sends cookies or tokens, and nothing from a tab
  reaches the hub.
- The desktop app's account tabs send a plain Chrome user agent, because claude.ai's bot
  protection rejects Electron's default one. claude.ai sees an ordinary Chrome browser.
- **Usage reading in the desktop app uses claude.ai's undocumented endpoints.** A script in
  each signed-in tab reads the same numbers as claude.ai's Settings → Usage page every 5
  minutes. It is off for each account until you accept a warning, it stops on errors such as
  401, 403 and 429, and it is not an official API, so it may break at any time. Anthropic's
  consumer terms restrict automated access, so it could go against the spirit of the terms
  and get an account flagged; you decide whether to turn it on.

Every place Watchyard talks to claude.ai, and how to turn each off:
[Privacy, risks and how Watchyard talks to claude.ai](docs/guide/claude-ai.md).

- Use your own accounts, each within the terms of its plan
  ([Consumer Terms](https://www.anthropic.com/legal/consumer-terms),
  [Usage Policy](https://www.anthropic.com/legal/aup)). Do not share accounts or credentials.
  Advertised plan limits assume ordinary, individual use.
- Watchyard never switches or rotates accounts to get around limits. "Most room right now"
  is a suggestion; you choose.
- Peers is for your own sessions in your own accounts, not for pooling accounts between people.

## Contributing

Contributions are welcome: read [CONTRIBUTING.md](CONTRIBUTING.md). Behaviour changes start in
[docs/spec/](docs/spec/README.md). Commits are signed off under the
[Developer Certificate of Origin](https://developercertificate.org/). Please follow the
[Code of Conduct](CODE_OF_CONDUCT.md), and report security problems privately
([SECURITY.md](SECURITY.md)).

## License

GNU Affero General Public License v3.0: see [LICENSE](LICENSE) and [NOTICE](NOTICE). You may
use, change and self-host Watchyard freely, including at work. If you distribute a changed
version, or run one as a service for others, you must publish your source under the same
licence. Contributions come in under the terms in [CONTRIBUTING.md](CONTRIBUTING.md#licence-of-contributions).
The license grants no rights to the Watchyard name or to any Anthropic trademark.

---

Watchyard is an independent, unofficial project, not affiliated with, endorsed by or
sponsored by Anthropic. "Claude" and "Claude Code" are trademarks of Anthropic, PBC.
