# Accounts and sharing

Claude Code keeps each login in a config folder: `~/.claude` by default, and any other
folder you point it at with `CLAUDE_CONFIG_DIR` (`~/.claude-2`, `~/.claude-3`, ...).
Watchyard calls each folder an **account** on this machine. It gives each one a label and a
colour, writes a launcher for it, and can make the folders share settings, skills, plugins and
more, so you set things up once.

Use only your own accounts, each used within the terms of its plan. Watchyard never
switches or rotates accounts for you, and it never shares a login between folders.

## Choosing protected or aggressive

`wy account next` (below) decides where new Claude Code sessions go. Tell it, once per
account, how to treat that account:

- **Protected**: kept for you. New sessions only when no aggressive account has room, this week is behind an even pace and
  the 5-hour window is under 45%. Choose it for the account you use yourself: in Chrome, the
  Claude app, or interactively in a terminal. Agents then only borrow what you would not use
  anyway, and nothing ever falls back onto it when the other accounts are full.
- **Aggressive**: use it up. Sessions go here until 98% of the week or 5 hours. Choose it for
  accounts that only run agents.
- **Not chosen**: treated as aggressive, as before this setting existed. The board, the phone
  app and `wy account next --explain` show "Not chosen yet; treated as aggressive." with the
  choice one click away.

**Where to choose.** On the board or in the phone app: Accounts, open the account, then **How
should the picker use this account?** (owner only). The choice is kept on the hub, so every
machine uses it. `wy setup` and `wy account add` also ask once per account that has none
(protected, aggressive or skip); with a hub the answer goes to the hub, without one it is kept
in this machine's config.

**A setting on one machine still wins there.** `wy config set next.accounts.<email>.mode
protected` (or `aggressive`) overrides the hub's choice on that machine only; `--explain` says
"set on this machine", and the board names the machine when its setting differs from the
hub's. Settings made this way before this release keep working unchanged; to share one, pick
the same mode on the board and delete the `mode` line from this machine's config.toml (`wy paths`
shows where it is).

**At the limits.** A protected account stops taking new sessions as soon as its week gets
ahead of an even pace (plus 10 points, never above 85%) or its 5 hours reach 45%, and takes
at most 2 new sessions an hour; it frees up when the pace catches up or the window resets. An
aggressive account stops at 98% of either window and takes at most 6 new sessions an hour.
When no account can take a session, `wy account next` exits 4 and says when the first one
frees up: sessions never spill onto a protected account.

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
folders are logged in on this machine, at each account's newest 5-hour and weekly readings
(from the hub, or from this machine's status line when there is no hub), at the tokens this
machine's sessions used since those readings, and at the sessions it already handed out, and
picks one. It only recommends: it never moves a running session or changes a login.

```sh
wy account next                       # claude-3  ~/.claude-3  (aggressive; 7d 23%, resets Wed 10:00; 5h 12%)
wy account next --explain             # every account, its state, and why it was or was not picked
env $(wy account next --env) claude   # start Claude Code in the recommended folder
wy account next --exec -- claude      # the same, without the shell trick
wy account next --n 4 --session-id <uuid> ...   # four sessions at once, each with its own id
```

It exits 4 when no account can take a new session (or, with `--n`, when not all of them could
be placed: launch only the ones it printed) and says when the first one frees up.
With a hub, the machine's own token from `wy connect` is enough to read every account's
usage (percentages and reset times only). Upgrade the hub first: an older hub cannot answer
that read, and then only this machine's own readings are used (or the board's token, if
`wy board connect` was run) and the output says so. A folder counts as logged in only if
`claude auth status` agrees, as in `wy account list`.

Each pick is recorded as a lease in a ledger on this machine (and sent to the hub, without
waiting for it), so ten agents started within a minute spread over your accounts instead of
all landing on one: an account takes at most 6 new sessions an hour (2 for a protected one).
Give each session the id you will start it with (`--session-id`, a UUID; `--exec` passes it to
`claude`), and the lease follows that session until it ends. A pick whose session never starts
stops counting after 10 minutes. `--no-reserve` asks without recording anything, and
`--urgent` may go past an account's hourly cap (never past a limit) when nothing else is free.

Sessions picked on another machine count too, once: through the hub, each machine sees how
much every recent lease's session has used, so a lease counts only for what its session has
not used yet, and what it has used is counted in the readings or the token meter. With the hub
down, the leases from the last hub read are used until a new reading of that account shows
them. `--explain` lists each lease with where its use was measured.

The default strategy, `lanes`, treats each account as **aggressive** (use its room up to 98 %)
or **protected** (kept for you: new sessions only while its week is behind an even pace plus
10 points, under 85 %, and its 5 hours under 45 %; nothing ever falls back onto it). An aggressive
account whose week resets within two days is filled first, the soonest reset first, so its room
is not lost; then the other aggressive accounts take turns; protected accounts get a session only
when no aggressive account has room. Other strategies (`--strategy`, or `wy config set next.strategy …`) project usage
forward instead: `smart` (spend the weekly room that would otherwise expire unused first,
keeping clear of accounts about to fill their 5 hours), `expiring`, `most-room`, `spread`,
`fill` (a fixed order) and `sticky`.

`lanes` also learns each account: how many 5-hour points a million tokens cost, how 5-hour
points turn into weekly ones, and what a new session costs in its first 5 hours. A new
account uses safe defaults for about 3 days, then its own measured values (the hub works
them out for every machine; without one, each machine uses its own data). `--explain` shows
them on one line per account.

Settings per account, keyed by its email (the same on every machine) or by a label:

```sh
wy config set next.accounts.me@work.example.mode protected      # lanes: kept for you
wy config set next.accounts.claude-3.mode aggressive            # lanes: used up to 98 %
wy config set next.accounts.me@work.example.hours 09:00-18:00
wy config set next.accounts.me@work.example.days mon-fri
wy config set next.accounts.claude-5.exclude true
```

Also `cap_5h`, `cap_7d` and `reserve_pct` (which `lanes` reads too), and for the other
strategies `cap_model`, `priority` (for `fill`) and `weight`.
The full rules are in [setup.md 8.5](../spec/setup.md#85-wy-account-next).

**Why an account was or was not picked:** open the account on the board or in the phone app,
then **Usage and the picker** in its Details. It shows the newest verdict a machine sent in
words (its place in line, and each rule with its value against the limit, such as "5-hour 1%
(stops at 45%)"), how the 5-hour limit relates to the week ("6.8% of the 5-hour limit = 1% of
the week"), what a new session is set aside, daily charts, and what 1% of the 5-hour limit
buys at API prices per model. **How this works** at the end explains the measurements. The capacity index only counts for weeks when
Claude Code made most of the account's use; otherwise it says so. A machine without a hub
sends no verdicts.

**Agents that start other agents** should ask first, once for the whole batch:
`wy account next --n 3 --session-id <uuid> --session-id <uuid> --session-id <uuid>`, then start
each printed folder with `claude --session-id <its uuid>` (or one at a time with
`wy account next --exec --session-id <uuid> -- claude …`). Start only the sessions it placed,
and when it exits 4 wait instead of starting the rest anyway. Exit 3 means a session id was
already used: make new ones.

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
