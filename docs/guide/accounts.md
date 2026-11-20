# Accounts and sharing

Claude Code keeps each login in a config folder: `~/.claude` by default, and any other
folder you point it at with `CLAUDE_CONFIG_DIR` (`~/.claude-2`, `~/.claude-3`, ...).
Watchyard calls each folder an **account** on this machine. It gives each one a label and a
colour, writes a launcher for it, and can make the folders share settings, skills, plugins and
more, so you set things up once.

Use only your own accounts, each used within the terms of its plan. Watchyard never
switches or rotates accounts for you, and it never shares a login between folders.

## What Watchyard never touches

- Logins: `.credentials.json`, the macOS Keychain item, `oauthAccount` and every other key of
  `.claude.json` except `mcpServers`. Logging in is always Claude Code's own
  `claude auth login`, run by you.
- Sessions, caches, usage counters and the other runtime files of a folder.
- Links you made yourself: a symlink that exists before a run exists after it, unchanged,
  unless the plan says otherwise and you agreed.

## Labels, colours and launchers

`wy setup` (step 2) and `wy account` set them:

```sh
wy account list                              # one row per folder: label, login, plan, what is shared
wy account label ~/.claude-2 work --colour teal
wy account add research                      # a new folder (the next free ~/.claude-N), shared like the others
wy account login research                    # runs `claude auth login` for that folder, in this terminal
wy account remove research                   # stop managing it; the folder and its login stay
```

Labels are lower case letters, digits and `-`, up to 24 characters. The colour palette is
`teal`, `amber`, `violet`, `rose`, `sky`, `lime`, `orange`, `slate`, or any `#rrggbb`. Labels
and colours go to the hub, so the board, the desktop app and the Android app use the same
names.

For each folder without a launcher of its own, setup writes a small `claude-N` script in
`~/.local/bin` that runs Claude Code with that folder (`--named-launchers` also writes
`claude-<label>`). Launchers you already have, including shell functions and aliases, are
detected and kept.

## Which account to start on next

`wy account next` recommends the folder for your next Claude Code session. It looks at which
folders are logged in on this machine and at each account's 5-hour and weekly limits (from
the hub, or from this machine's status line when there is no hub), projects them to the end
of a new session and to each reset, and picks one. It only recommends: it never moves a
running session or changes a login.

```sh
wy account next                       # claude-3  ~/.claude-3  (7d 23% → ~41% at reset Wed 10:00; 5h fresh; aggressive)
wy account next --explain             # every account, its projection, and why it was or was not picked
env $(wy account next --env) claude   # start Claude Code in the recommended folder
wy account next --exec -- claude      # the same, without the shell trick
wy account next --model opus          # also count the Opus weekly limit
```

It exits 4 when no account can take a new session and says when the first one frees up.
With a hub, the machine's own token from `wy connect` is enough to read every account's
usage (percentages and reset times only). Upgrade the hub first: an older hub cannot answer
that read, and then only this machine's own readings are used (or the board's token, if
`wy board connect` was run) and the output says so. A folder counts as logged in only if
`claude auth status` agrees, as in `wy account list`.

Each pick is held for about 12 minutes as a reservation, so ten agents started within a
minute spread over your accounts instead of all landing on one. `--no-reserve` asks without
holding anything.

Strategies (`--strategy`, or `wy config set next.strategy …`): `smart` (default: spend the
weekly room that would otherwise expire unused first, keeping clear of accounts about to fill
their 5 hours), `expiring`, `most-room`, `spread`, `fill` (a fixed order) and `sticky`.

Settings per account, keyed by its email (the same on every machine) or by a label:

```sh
wy config set next.accounts.me@work.example.mode conservative   # caps 5 h at 60 %, 7 d at 70 %
wy config set next.accounts.claude-3.mode aggressive            # caps at 98 % and preferred
wy config set next.accounts.me@work.example.hours 09:00-18:00
wy config set next.accounts.me@work.example.days mon-fri
wy config set next.accounts.claude-5.exclude true
```

Also `cap_5h`, `cap_7d`, `cap_model`, `reserve_pct`, `priority` (for `fill`) and `weight`.
The full rules are in [setup.md 8.5](../spec/setup.md#85-wy-account-next).

**Agents that start other agents** should ask first: `env $(wy account next --env) claude …`
or `wy account next --exec -- claude …`, and stop when it exits 4 instead of starting the
session anyway.

## Sharing

The primary folder (`~/.claude` unless you choose another with `--primary`) holds the shared
items; the other folders link to it.

| Switch | Shares | Default |
| --- | --- | --- |
| `settings` | `settings.json` | on |
| `instructions` | `CLAUDE.md`, `rules/` | on |
| `addons` | `skills/`, `agents/`, `commands/`, `keybindings.json` | on |
| `plugins` | the plugin folder | on, and always on while `settings` is on |
| `mcp` | the `mcpServers` list in each folder's `.claude.json` (copied, not linked) | on, unless you already sync MCP servers yourself |
| `history` | `projects/` (transcripts and memory) | on |
| `prompts` | `history.jsonl`, `plans/` | on |

Never shared: logins and tokens, live sessions, caches and usage counters.

Notes:

- With `history` on, two accounts that resume the same session write into one transcript.
- On Linux and macOS a shared item is a symlink. On Windows directories are junctions and
  files are symlinks when allowed, otherwise copies that `wy tick` keeps in sync.
- An item is linked only when the primary has it. Nothing is created just to be shared,
  except an empty `settings.json` when `settings` is on.

### When a folder already differs

If a folder has its own version of an item, setup shows it as `separate (differs)` and asks
what to do: keep it separate (the default), use the primary's copy, or merge where that is
possible. In scripts, `--conflicts keep|primary|merge` applies one answer to everything and
`--resolve` sets it per item. Whatever is replaced is backed up first.

### Change sharing later

```sh
wy account share --dry-run                       # what would change for every folder
wy account share work --share defaults,-history  # all defaults except history, for one folder
wy account remove work --unshare                 # turn links into independent copies
```

Turning a switch off for an item that is already linked turns the link into an independent
copy of the shared file or folder.

## Plans, backups and undo

Every command that changes your folders (`wy setup`, `wy account add|label|share|remove`,
`wy uninstall`, `wy term setup`) works the same way:

1. It reads the machine and computes a plan.
2. It shows the plan and asks. `--dry-run` stops here; `--yes` applies without asking.
3. Before each change it saves what it replaces in a backup set under the state folder
   (`wy paths` shows where).
4. If a step fails, the run is rolled back.

```sh
wy setup --undo-list          # every run that changed something, newest first
wy setup --undo --dry-run     # what undoing the last run would do
wy setup --undo               # undo the last run, byte for byte
wy setup --undo <backup id>   # undo a specific run
```

A second `wy setup` with the same answers changes nothing ("0 changes").

## Scripted setup

Every screen of `wy setup` has a flag, so Claude or a script can drive it:

```sh
wy setup --labels '~/.claude=personal,~/.claude-2=work:amber' --share defaults --hub here --dry-run --json
wy setup --labels '~/.claude=personal,~/.claude-2=work:amber' --share defaults --hub here --yes
```

`--json` prints the plan and a plan id; `--expect-plan <id>` applies only if the plan is
still the same. The `/watchyard:setup` skill uses this to show you the plan, ask, and apply
exactly what you saw. Every flag: [docs/spec/setup.md](../spec/setup.md).

## What the plugin and status line do

Setup installs two things into Claude Code:

- **The Watchyard plugin** (from a local marketplace named `wy-local`). Its hooks run
  `wy hook <event>` on session start, prompt, permission request, notification, stop,
  failure and end. Each hook appends one record to the local outbox and exits at once; it
  never blocks Claude Code and never approves or denies anything. The plugin also adds the
  skills `/watchyard:status`, `/watchyard:setup`, `/watchyard:add-account` and the
  `peers` skill.
- **A status line wrapper.** `wy statusline` records Claude Code's own limit percentages
  (the `rate_limits` field) once a minute per session and then runs your previous status
  line command unchanged.

Skip either with `--no-plugin` or `--no-statusline`. What each record contains and what never
leaves the machine: [security.md](security.md).

If you prefer installing the plugin from GitHub instead, see [Claude Code plugin](board.md#claude-code-plugin-from-github).
