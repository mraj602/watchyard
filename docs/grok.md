# Grok as a second coding agent

Watchyard can hand work to xAI's **Grok Build** CLI (`grok`) the same way an orchestrator hands
work to a Claude lane. Claude (or `wy`) starts Grok headlessly and reads its result; Grok can
start Claude with `claude -p` from its own shell tool.

## What Grok Build is

- xAI's official terminal coding agent (`grok`, repo `xai-org/grok-build`, Apache-2.0). Install:
  `curl -fsSL https://x.ai/cli/install.sh | bash` (put the binary where you like with
  `GROK_BIN_DIR`; the script also edits `~/.bashrc`).
- Sign-in uses the **consumer subscription** (SuperGrok / X Premium+; the launch post says Grok
  Build is included). `grok login --device-auth` works over SSH. An `XAI_API_KEY` from
  console.x.ai is only a fallback and is billed per token, separately.
- Headless: `grok -p "<prompt>" --output-format json`. Also MCP servers (`~/.grok/config.toml`),
  AGENTS.md, skills, hooks, ACP (`grok agent stdio|serve`), `grok agent headless` (relay).

## What Watchyard adds

| Piece | What it does |
|---|---|
| `wy grok status` | finds `grok`, prints version, says whether it is signed in (exit 4 if not) |
| `wy grok run [flags] <prompt\|->` | headless run in `--dir`, prints the answer; `--json` for the full result; `--resume ID` continues a Grok session |
| `wy grok list` / `adopt <id>` / `forget <id>` | the Grok sessions that are peers (see below) |
| `wy grok usage [id]` | per-session tokens and cost, or the plan usage |
| `internal/grokcli` | the Go package behind these (fake-binary tests) |
| `internal/grokpeer` | the peers host: session records, busy lock, inbox, drain |

Exit codes: 0 ok, 3 no `grok`, 4 not signed in, 1 other failure. Credentials are never read:
sign-in state comes from the words `grok models` prints, not from `~/.grok/auth.json`.

## Orchestrator use (like `claude -p`)

```bash
echo "<prompt>" | wy grok run --name build-fix --dir ~/repo --yolo -
wy grok run --name build-fix --dir ~/repo --yolo "<prompt>"      # prompt as an argument
wy grok run --resume <session id> "<follow-up>"                  # continue it
```

The answer is on stdout; the last line on stderr is `grok session <id> (<n> turns, <n> tokens)`.
`--json` prints the full result. A run registers the session as a peer, so it can be continued
with `--resume` or messaged with `wy peers send` afterwards. `--yolo` lets its tools run
without asking (use it for headless work; the peer is then listed as bypass). `--resume` of a
session that is mid-turn fails with "session ... is busy" instead of racing it.

## Grok sessions as peers

A Grok session that `wy grok run --name N` started, or that `wy grok adopt <id> [--dir D]
[--name N] [--yolo]` added, is listed by `wy peers list` as `N@grok.<machine>` with account
`grok`, and `wy peers send N@grok.<machine> --stdin` delivers to it.

- **Mechanism: headless resume.** Delivery runs `grok -r <id> -p <message>` in the session's
  folder. The session keeps its history; the turn is one more in it.
- **Busy or idle** is an exclusive file lock (`<state>/grok/sessions/<id>.lock`) held while a
  turn runs. Idle: the message is delivered at once by a detached `wy grok drain <id>`. Busy:
  the message goes to a file inbox (`<state>/grok/inbox/<id>/`, at most 64) and the process
  that holds the lock drains it as soon as its turn ends, joining the waiting messages into
  one prompt. `wy peers send` reports `queued ... busy; the message is injected when its turn
  ends`. A failed drain keeps the messages.
- **Rules are the Claude ones.** Grok is the pseudo-account `grok` in the matrix, so every
  pair involving it needs an explicit cell (a hub-signed matrix must name `grok`), and
  threads, rate limits and the loop guard apply unchanged. Sessions untouched for 7 days
  drop from the list.
- **Why not the alternatives.** The leader socket and ACP (`grok agent serve`, JSON-RPC over a
  WebSocket with `_x.ai/sessions/changed` and `_x.ai/queue/changed` for activity) can inject
  into a live interactive session, but need a running server or leader, a secret, and a
  protocol client whose extension notifications are version-specific. Resume needs only the
  `grok` binary, behaves the same with or without a TUI open, and has an obvious failure mode
  (the command's exit code). The cost is that a message cannot interrupt a running turn and
  a session open in a Grok TUI does not redraw until reopened.

## Accounts

`wy account list` ends with a `grok (not Claude)` line (plan usage from `grok usage --json`,
shown when Grok's background service answers) and one line per known session (turns, tokens,
approximate cost from `grok usage <id>`). Grok is never a target of `wy account next`. The
board's accounts section is not fed by this (it reads hub data, which has no Grok seat).

## Not built

- **Replies from a Grok session as sender.** `wy peers send` finds its sender by process
  ancestry from a Claude session; run from a Grok shell tool the sender is not known, so
  Grok does not reply on its own. The orchestrator reads results from `wy grok run`.
- **Grok sessions on other machines.** The hub directory does not know Grok hosts; only the
  local machine lists them.
- **Grok-to-Claude MCP server.** A small stdio MCP server (added with `grok mcp add`) could
  expose `ask_claude`; until then Grok calls `claude -p` directly.
- **Remote UI.** `grok agent serve --bind 127.0.0.1:2419 --secret <token>` is a WebSocket ACP
  server; reach it over the tailnet. `grok dashboard` is a terminal view of local sessions.
