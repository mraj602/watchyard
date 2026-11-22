# Setting up Watchyard with an AI agent

For AI agents (Claude Code, Codex CLI, Gemini CLI and others) that a person asked to install
or set up Watchyard. It does not repeat the guides: it says which one to follow for each
step, and what the agent must leave to the person. If you are working on Watchyard's own
code instead, read [AGENTS.md](../../AGENTS.md).

## Where the steps are

| Step | Follow |
| --- | --- |
| Install `wy`, detect, choose sharing and the hub, dry-run, apply | The setup skill, [`plugin/watchyard/skills/setup/SKILL.md`](../../plugin/watchyard/skills/setup/SKILL.md). It is written for an agent: every command, question, default and exit code. In Claude Code with the plugin installed it is `/watchyard:setup`; any other agent reads the file and follows it. |
| Background: words, requirements, where the hub runs | [install.md](install.md), sections "Requirements" to "2. Choose where the hub runs" |
| Hub on a server (Docker, systemd), HTTPS for phones | [install.md, 3b](install.md#3b-hub-on-a-server) |
| More machines | [install.md, 4](install.md#4-more-machines) |
| Desktop app | [desktop.md](desktop.md#download), then [Hub tab and pairing](desktop.md#hub-tab-and-pairing) |
| Android app | [android.md](android.md#install-the-apk), then [Pair with your hub](android.md#pair-with-your-hub) |
| Peers and the owner key | [peers.md](peers.md#set-up-step-by-step) |
| Checking and fixing | [troubleshooting.md](troubleshooting.md), [accounts.md: undo](accounts.md#plans-backups-and-undo), [uninstall.md](uninstall.md) |

When a guide and a spec in [docs/spec/](../spec/README.md) disagree, the spec is right.

## Ask before you start

The setup skill asks about this machine (hub, sharing, labels, new accounts). Before it,
find out what decides the rest, in one message with defaults:

- How many machines run Claude Code, and can this one `ssh` to them? (Default: just this one.)
- Should the hub run here or on an always-on server? (Default: here.)
- Alerts or the board on an Android phone? That needs an `https://` address for the hub.
  (Default: no.)
- The desktop app, and on which OS? Peers between accounts? (Default: no to both.)

## What stays with the person

`wy` enforces some of these; the rest are on you. They hold for the whole session, not only
for `wy setup`.

1. **Secrets never go through the chat.** Do not ask for, print or put on a command line a
   hub token, device token, passphrase, recovery code or the link from `wy hub owner-link`
   (it signs a browser in as the owner). Give the person the command to run in their own
   terminal. A short pairing code (`482-913`, valid once for 10 minutes) may be typed in the
   chat.
2. **Logins are theirs.** Never run `claude auth login`, and never read or edit
   `.credentials.json`, a Keychain item or `oauthAccount`.
3. **Plan, show, ask, apply.** Changes go through `wy setup --dry-run`, an explicit yes and
   `--expect-plan`, as the skill says. Do not edit Watchyard or Claude Code files by hand.
4. **Signing is theirs.** `wy keys` (init, certify, revoke, passwd, recover, trust-owner),
   `wy agent enable --repin-hub` and the `wy peers` commands that widen access refuse inside
   Claude Code or without a terminal ([the signing guard](peers.md#the-signing-guard)). Do
   not work around the refusal; hand the person the commands from peers.md.
5. **The network is theirs.** Do not run `tailscale up`, `down`, `logout`, `set` or `serve`,
   and do not open firewall ports: suggest the command. Never publish the hub on `0.0.0.0` or
   a public address, and use `--insecure-lan` only for a private network or tailnet, with the
   person's agreement.
6. **Other machines need consent.** Before running anything on another machine, say which
   one and what will happen. Run `wy machine add <host> --dry-run` yourself; the person runs
   the real `wy machine add`, which asks about host keys and logins.
7. **Downloads are verified.** The desktop packages are checked against `SHA256SUMS-desktop`
   and the APK's signing certificate against the fingerprint in android.md. Never suggest
   skipping a check, `--no-verify`-style flags, or `WY_VERIFY_ATTESTATION=0`.

## Starting Claude Code sessions yourself

If your task launches other Claude Code sessions (subagents in a terminal, a batch of
workers), ask Watchyard which account to use, once for the batch: make a UUID per session and
run `wy account next --n <K> --session-id <uuid> ...` (one `--session-id` per session), then
start each printed folder with `CLAUDE_CONFIG_DIR=<folder> claude --session-id <its uuid> …`;
for a single session `wy account next --exec --session-id <uuid> -- claude …` does both. Each
pick is recorded, so launches spread over the accounts and stop at their limits. Start only
the sessions it printed: when it exits 4, the rest have no room; report the time it prints
instead of starting them. Exit 3 means a session id was already used: make new ones. Leave
`--urgent` to the person. Never log in, log out or edit a
folder to make room; that stays with the person.

## Done when

- `wy doctor` shows no failed checks (warnings it explains are fine).
- After the person restarts their Claude Code sessions, a new session shows in `wy status`.
- With a hub: `wy board --once` lists this machine's sessions.
- Tell the person what changed, how to undo it (`wy setup --undo`) and what is left for them
  (logins, `owner-link`, keys), one command per line.
