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
| `internal/grokcli` | the Go package behind both (fake-binary tests) |

Exit codes: 0 ok, 3 no `grok`, 4 not signed in, 1 other failure. Credentials are never read:
sign-in state comes from the words `grok models` prints, not from `~/.grok/auth.json`.

## Not built (designed)

- **Peers handle for Grok.** `wy peers` is built on Claude Code session records and hooks, so a
  Grok session has no handle yet. The smallest honest bridge is a file/outbox mailbox: Grok
  reads `wy peers` output through its shell tool and replies with `wy peers send`. A real
  adapter needs a `kind` field in the peers directory (docs/spec/peers.md) and a Grok hook
  (`grok` supports hooks, see its docs/user-guide/10-hooks.md) that records session start/stop.
- **Grok-to-Claude MCP server.** A small stdio MCP server (added with `grok mcp add`) could expose `ask_claude`; it does not exist yet.
  Until then Grok calls `claude -p` directly.
- **Remote UI.** `grok agent serve --bind 127.0.0.1:2419 --secret <token>` is a WebSocket ACP
  server; reach it over the tailnet. `grok dashboard` is a terminal view of local sessions.
