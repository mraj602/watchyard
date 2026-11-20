# Setup, sharing and accounts

Status: spec v1. Implemented in `internal/setup` (planner, apply, backup, undo),
`internal/share` and `internal/jsonedit` (sharing), `internal/account` and
`internal/claudecli` (folders, logins, the `claude` CLI), `cmd/wy` (prompts), `install/`
(install scripts) and `plugin/watchyard/skills/setup` (the skill).

Facts about Claude Code below were checked against **2.1.296** on the development laptop on
12 October 2026 (section 13 lists what was run). Anything not checked is marked
**uncertain** and has a fallback.

## 0. Rules that override everything else

1. **Plan first.** Every mutating command (`wy setup`, `wy account add|share|remove|label`,
   `wy uninstall`) computes a plan and shows it before it changes anything. `--dry-run` stops
   there.
2. **Everything is backed up and journaled.** Each change is recorded in a backup set
   (section 7) *before* it happens. `wy setup --undo` reverses any set.
3. **Logins are never touched.** `wy` never reads, writes, moves, copies, backs up or
   restores `.credentials.json`, a Keychain item, `oauthAccount` or any other key of a
   `.claude.json` except `mcpServers` (section 5.4). It never renames or deletes a config
   folder that holds a login. Logging in is always `claude auth login`, run by the person.
4. **Links are never broken.** A symlink or junction that exists before a run exists after it
   with the same `readlink` string, unless the plan explicitly says it changes it and the
   person agreed. After every external command `wy` runs, it re-checks every link it saw at
   detect time; any change aborts the run and rolls it back (section 7.5).
5. **Adopt, don't replace.** Anything already set up by hand (links, launchers, MCP sync
   scripts, status lines) is recognised and kept. A second run with the same answers makes
   no changes ("0 changes" and no backup set).
6. **Never print secrets.** Plans, logs and `--json` output show MCP server *names* and
   hashes, never their values; settings `env` values are never printed; tokens are read from
   stdin only and never echoed.

## 1. Install scripts

They do one thing: put a verified `wy` binary in `~/.local/bin`. They edit no shell profile,
no PATH, no Claude folder and nothing else.

### 1.1 Release assets

Every tag `v<semver>` publishes on GitHub Releases:

| Asset | Content |
| --- | --- |
| `wy_<ver>_linux_amd64.tar.gz`, `_linux_arm64`, `_darwin_amd64`, `_darwin_arm64` | `wy`, `LICENSE`, `README.md` |
| `wy_<ver>_windows_amd64.zip`, `_windows_arm64.zip` | `wy.exe`, `LICENSE`, `README.md` |
| `SHA256SUMS` | `sha256sum` format, one line per archive |
| `Watchyard-<ver>-<os>-<arch>.<ext>` | The unsigned desktop app packages: `.AppImage`, `.deb`, `.rpm` (Linux x64), `.dmg` (macOS, the runner's arch) and `.exe` (Windows x64 NSIS installer); see desktop.md C13 |
| `SHA256SUMS-desktop` | `sha256sum` format, one line per desktop package (the install scripts read only `SHA256SUMS`) |
| (attestation) | GitHub build provenance for every archive and every desktop package (`actions/attest-build-provenance`) |

The release is published only when every build succeeded, in one `gh release create`.

`<ver>` has no leading `v`. Binaries are `CGO_ENABLED=0`, `-trimpath`, version stamped with
`-ldflags -X` as in the Makefile.

### 1.2 `install.sh` (Linux, macOS, WSL, BSD where Go builds)

```
curl -fsSL https://<site>/install.sh | sh
curl -fsSL https://<site>/install.sh | sh -s -- [--version 0.3.0] [--bin-dir DIR] [--no-setup] [-- <wy setup args>]
```

Steps, POSIX `sh` only (no bash-isms; `set -eu`):

1. Detect OS (`uname -s`: Linux, Darwin; anything else exits 2 with "unsupported OS, build
   from source") and arch (`uname -m`: `x86_64|amd64` → amd64, `aarch64|arm64` → arm64; else
   exit 2). On Darwin under Rosetta (`sysctl -n sysctl.proc_translated` = 1) use arm64.
2. Pick a downloader: `curl -fsSL` or `wget -qO-`; neither → exit 2.
3. Base URL: `${WY_INSTALL_BASE_URL:-https://github.com/<owner>/watchyard/releases}`.
   Version: `--version` (with or without `v`), else `latest`. For `latest` the files come from
   `<base>/latest/download/<name>`; for a version from `<base>/download/v<ver>/<name>`. No
   GitHub API call, so no rate limit. `WY_INSTALL_BASE_URL` exists for tests and mirrors; it
   must be https (http only for a loopback host, no credentials). Downloads use
   `curl --proto '=https' --proto-redir '=https' --tlsv1.2` (`wget --https-only`).
4. Download `SHA256SUMS` and the archive into a fresh `mktemp -d` (removed by `trap` on
   exit). For `latest`, the version is read from the archive name listed in `SHA256SUMS`.
5. Verify: the archive's line must be present in `SHA256SUMS`, and `sha256sum -c` (or
   `shasum -a 256`, or `openssl dgst -sha256`) must match. No tool or a mismatch → exit 3,
   nothing installed. If `gh` is installed (and `WY_VERIFY_ATTESTATION` is not `0`), also
   `gh attestation verify <archive> --repo <owner>/watchyard`; failure → exit 3. Without `gh`
   the script installs on the checksum alone and prints a `note:` saying the attestation was
   not checked; `WY_VERIFY_ATTESTATION=1` makes a missing `gh` exit 3.
6. Extract only `wy`. Install to `--bin-dir` (default `$HOME/.local/bin`, created with
   `mkdir -p`, mode 0755): an existing folder's mode is left alone), copy to a `mktemp` file `<bin>/.wy.XXXXXX`, `chmod 0755`, `mv -f` over `<bin>/wy`
   (atomic on one filesystem; a running `wy hook` keeps its old inode).
7. Print `wy <ver> downloaded, checksum ok, installed to ~/.local/bin/wy` (screen `Main`).
   If `<bin>` is not on `PATH`, print the one line to add for the user's shell (bash, zsh,
   fish) and do not add it.
8. Unless `--no-setup`: if `/dev/tty` can be opened, `exec <bin>/wy setup "$@" </dev/tty`
   (arguments after `--` go to `wy setup`, as in the Add-machine line
   `sh -s -- -- --hub join:https://hub --code 482-913`). Without a terminal, print
   "run: wy setup" and exit 0.

Exit codes: 0 ok, 2 unsupported/usage, 3 verification failed, 4 download failed.

### 1.3 `install.ps1` (Windows)

```
irm https://<site>/install.ps1 | iex
& ([scriptblock]::Create((irm https://<site>/install.ps1))) -Version 0.3.0 -NoSetup
```

Same steps in PowerShell 5.1+: arch from `$env:PROCESSOR_ARCHITECTURE` (`AMD64`, `ARM64`);
`Invoke-WebRequest` into a temp folder; `Get-FileHash -Algorithm SHA256` against
`SHA256SUMS`; `Expand-Archive`; install to `$HOME\.local\bin\wy.exe` (Claude Code's own
native installer uses `$HOME\.local\bin`), replacing an existing file by writing
`wy.exe.new` then `Move-Item -Force` (if `wy.exe` is locked by a running hook, retry 5 times
over 2 s, then fail with exit 4). PATH is not changed; the script prints the
`[Environment]::SetEnvironmentVariable` line to run. Then `wy setup` unless `-NoSetup`
or the host is non-interactive.

## 2. Terms

| Term | Meaning |
| --- | --- |
| **Folder** | A Claude Code config folder (a slot, identity.md 3): `~/.claude`, `~/.claude-2`, ... |
| **Primary** | The folder shared items live in. Default `~/.claude`; `--primary` or `share.primary` in `config.toml`. |
| **Item** | One shareable thing in a folder (section 5.1). |
| **Settings target** | The real `settings.json` a folder uses after following links. Folders sharing settings have one target. |
| **Plugin root** | The real folder Claude Code keeps plugin records in for a folder: `CLAUDE_CODE_PLUGIN_CACHE_DIR` if set (process or settings `env`), else `<folder>/plugins` after following links. |
| **Backup set** | `<state>/backup/<id>/`: the journal of one run (section 7). |
| **Ledger** | `<state>/setup/ledger.json`: what `wy` currently has installed, for `wy uninstall` and `wy doctor` (section 9.3). |

## 3. `wy setup`, interactive

Five screens, as drawn. Keys: Enter continue, Esc back, `q` quit (nothing applied), `e`
change something on screen 1. A dumb terminal (`TERM=dumb`, no raw mode) gets numbered
line prompts with the same defaults. Every screen's answers map one-to-one to the flags of
section 4; at the end the plan (section 6) is shown and applied only after "Apply these N
changes? [Y/n]".

### Step 1 · Detect (screen `Main`)

Read-only. Collects a `State` (the planner's whole input):

1. **Claude Code:** `exec.LookPath("claude")`, else `~/.local/bin/claude`; `claude --version`.
   Not found → the screen says so and setup stops with the install link (exit 7). Features
   are checked, not version numbers: `claude plugin marketplace --help` must list `add`, and
   `claude auth status --json` must parse. Older than 2.1.296 → a warning line only.
2. **Folders:** identity.md 3 discovery (non-directories such as `~/.claude-api-env` and
   files like `~/.claude.json.bak.*` are ignored), plus `slots.extra`. For each folder, in
   parallel with a 10 s timeout each: `claude auth status --json` (`CLAUDE_CONFIG_DIR` unset
   for `~/.claude`; also without `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` and
   `CLAUDE_CODE_OAUTH_TOKEN`, so the folder's own login is reported — same for `auth login`;
   on Windows these names are matched case-insensitively), giving logged in, email, org
   name, plan. `.credentials.json` is never
   looked at (on macOS it does not exist; the Keychain holds the login).
3. **Items:** for every folder and every item of 5.1: `lstat`, link target (raw `readlink`
   and resolved), type, size, and for files under 1 MiB a SHA-256. Directories are not hashed
   at detect time (`projects/` is gigabytes); they are compared lazily only when a conflict
   needs it (5.3).
4. **Plugins:** for each distinct plugin root: `installed_plugins.json` and
   `known_marketplaces.json` (parsed, never rewritten by `wy` except on undo, section 7.4);
   for each settings target: `enabledPlugins`, `extraKnownMarketplaces`, `env` *keys only*,
   `statusLine`.
5. **Launchers:**
   - executables named `claude-*` on `PATH` and in `~/.local/bin`; a file whose second-line
     marker is `# wy-launcher v1` is ours;
   - shell functions and aliases: one probe `"$SHELL" -ic 'for n in claude claude-2 ...; do
     type "$n"; done'` with stdin closed, `TERM=dumb` and a 3 s timeout, then a static scan
     of `~/.bashrc`, `~/.bash_profile`, `~/.profile`, `~/.zshrc`, `~/.config/fish/` for
     `claude-[0-9A-Za-z_-]+ *\(\)`, `alias claude-`, `function claude-`, `eval "claude-$`, and
     any script they call (`~/bin/claude-launch`). A folder whose name already has a launcher
     of any kind gets none from `wy`.
6. **MCP:** `mcpServers` names (values hashed) in each folder's state file (identity.md 3),
   and for `~/.claude` also `~/.claude/.claude.json` if it exists (Claude Code uses it when
   run with `CLAUDE_CONFIG_DIR=~/.claude`). Hand-made MCP sync is detected when a launcher
   script found in 5 mentions `mcpServers` or calls a script that does (on this laptop:
   `~/.claude/sync-mcp-to-2.sh`).
7. **Watchyard:** `config.toml`, `<state>/token`, `hub.url` and `/health` (2 s), timer and
   hub service status, the ledger, incomplete backup sets (section 7.6), Tailscale
   (`tailscale status --json`, if installed: self DNS name, used by step 4).

The screen shows one line per folder (`~/.claude-2  work  you@work.example  Team  logged in`),
then "Already set up by hand" with a verdict per finding (`keep`, `wrap`, `replace`,
`note`), then "Nothing has been changed yet."

`wy setup --detect --json` prints the `State` (section 6.3) and exits 0.

### Step 2 · Accounts (screen `SetupSharing`, top)

- One row per folder: status, label, email, launcher. Labels default to the existing
  `slots.labels` value, else the folder name without the dot (`claude`, `claude-2`).
  Colours default to the next free colour of the palette below.
- `a` adds an account: the next free folder (5.6), a label (required), a colour.
- For each folder not logged in, "log in now? [Y/n]". Default **Y** for folders created in
  this run, **n** for existing ones. Logins run *after* the plan is applied (step order in
  6.2), one at a time, with the terminal handed to `claude auth login --claudeai` (8.2).
- Label: `^[a-z0-9][a-z0-9-]{0,23}$`. Colour palette, in order: `teal #14b8a6`,
  `amber #f59e0b`, `violet #8b5cf6`, `rose #f43f5e`, `sky #0ea5e9`, `lime #84cc16`,
  `orange #f97316`, `slate #64748b`; any `#rrggbb` is accepted. Labels and colours belong
  to the seat (identity.md 5.6); locally they live in `slots.labels` / `slots.colours`
  and reach the hub in `slot_info` (8.4). Every path a label enters by is checked against
  this rule: `wy config set` / `SetJSON` refuse an invalid `slots.labels.*` value,
  `--labels` / `--add` / choices are refused by the planner, and an invalid label found in a
  hand-edited `config.toml` is ignored with a warning (the folder falls back to its default
  label). A default label derived from a folder name that breaks the rule is normalised
  (lower-cased, other characters become `-`).

### Step 3 · Sharing (screen `SetupSharing`, bottom)

Seven switches, space to toggle. Each row shows its current state on this machine: `link`
(will link), `linked by hand` (adopted), `shared elsewhere` (adopted, linked to somewhere
other than the primary), `separate (differs)` (a conflict, 5.3), `nothing to share yet`
(the primary has no such item). Below, greyed: "Never shared: login and tokens, live
sessions, caches, usage counters".

| Switch | Items | Default |
| --- | --- | --- |
| Settings | `settings.json` | on |
| Instructions | `CLAUDE.md`, `rules/` | on |
| Skills, agents, commands | `skills/`, `agents/`, `commands/`, `keybindings.json` | on |
| Plugins | plugin root (5.2) | on; **forced on while Settings is on** (enabled plugins live in `settings.json`, so sharing settings but not plugins would enable plugins a folder doesn't have) |
| MCP servers | `mcpServers` key of each state file (5.4) | on; shows `your launcher` and defaults **off** when hand-made MCP sync is detected |
| History and memory | `projects/` | on, with the caveat line "two accounts resuming one session write into one transcript" |
| Prompt history, plans | `history.jsonl`, `plans/` | on |

Turning a switch off for an item that is already linked converts the link into an
independent copy of the target (backed up, undoable). Turning it on where a folder differs
from the primary opens the conflict choice of 5.3 for that item.

### Step 4 · Hub (screen `SetupHub`)

`(•) Just this computer` / `( ) Connect to my hub` / `( ) I'll run the hub on another
server`. If a hub is already connected and healthy, a fourth option `(•) Keep <url>` is first
and selected.

| Choice | Flag | What happens at apply |
| --- | --- | --- |
| Just this computer | `--hub here` | Install the hub user service (`wy hub serve --listen 127.0.0.1:8787`; systemd user unit from `deploy/systemd`, launchd agent on macOS, Task Scheduler XML on Windows as in `internal/timer`), wait up to 10 s for `/health`, mint a machine token locally (`wy hub token create --kind machine --machine <id>` against the local data folder), then `wy connect http://127.0.0.1:8787 --token-stdin` (installs the timer). On Linux, if `loginctl show-user $USER -p Linger` is `no`, say "the hub stops when you log out; run `loginctl enable-linger`" (wy does not run it: it needs polkit/root on some systems). Prints a one-time browser sign-in token for the dashboard (interactive only; `--json` prints the command to make one instead). If Tailscale is up, says the hub is also reachable at `<self>.ts.net` only if the person later runs `tailscale serve` (wy does not). |
| Connect to my hub | `--hub join:<url>` + `--token-stdin` or `--code <code>` | `wy connect <url>` exactly as tokens.md 4 (HTTPS rule, `/health`, token proof, `<state>/token`, `hub.url`, timer). Interactive: the token is read with echo off. |
| On another server | `--hub later` + printed instructions | Prints `docker run ... ghcr.io/<owner>/watchyard-hub` and `wy hub token create --kind machine` for that server, then waits for the URL and token (Enter skips: becomes `later`). |
| Later | `--hub later` | Nothing. The summary says `wy connect <url> --token-stdin` or `wy setup --hub ...`. |
| Pulled by the hub | `--hub pull` | Not offered interactively (it needs the hub's key, which `wy machine add` brings). Sets `hub.reach = "pull"`, installs the timer. machines.md 3.4. |

The hub step runs after all local steps and its failure never rolls them back (6.2).

### Step 5 · Phone and done (screen `SetupDone`)

Phone: when the hub is on this machine (`--hub here`, or a kept hub on a loopback address)
and a person is at the terminal, the step asks "Pair a phone now? [y/N]" and on yes runs `wy
phone pair` (a phone needs an https address; without a `hub.public_url` it prints "run
`tailscale serve --bg <port>`, then `wy phone pair --url https://<name>.ts.net`" (`<port>` from `--listen`, default 8787) instead of
failing). With `--yes`, `--json` or no terminal it only prints "run `wy phone pair` on this
machine" and never blocks. A remote hub prints "open \<url> → Settings → Add phone, or run
`wy phone pair` on the hub"; with no hub, "later (connect a hub first, then `wy phone
pair`)". `--json` is unchanged. Then the summary: one line per applied change with `+`, `~`
or `−` (the screen's wording), the backup id, then `wy doctor` (the checks in 9.4) and "N
Claude sessions are open: restart them to start reporting" (count from `claude agents
--json` per folder). Last lines: next machine (`wy machine add user@host`) and the
dashboard address when a hub is connected.

## 4. `wy setup`, flags (for scripts and Claude)

```
wy setup [--dry-run] [--yes] [--json] [--detect]
         [--primary <folder>]
         [--labels '<folder|email>=<label>[:<colour>],...']
         [--add <label>[:<colour>],...] | [--accounts <label>,...]
         [--share defaults|none|<switch>,...]        switches: settings,instructions,addons,plugins,mcp,history,prompts
         [--conflicts keep|primary|merge] [--resolve '<folder>:<item>=keep|primary|folder|merge',...]
         [--hub keep|here|later|pull|join:<url>] [--token-stdin | --code <code> | --code-stdin]
         [--hub-dial <addr>] [--reach direct|forward] [--insecure-lan] [--listen <addr>]
         [--no-plugin] [--no-statusline] [--no-launchers] [--named-launchers] [--launcher-dir <dir>]
         [--login ask|never]
         [--expect-plan <plan id>]
wy setup --undo [<backup id>] [--force] [--dry-run] [--json]
wy setup --undo-list [--json]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dry-run` | off | Detect and plan, print, exit 0. Nothing written (not even `<state>`). |
| `--yes` | off | Apply without questions, using flags and defaults. Without `--yes` and without a terminal: print the plan and exit 3. |
| `--json` | off | Machine output on stdout (6.3); human text goes to stderr. Implies no prompts (needs `--yes` to apply). |
| `--detect` | off | Step 1 only. |
| `--primary` | `share.primary`, else `~/.claude` | Folder shared items live in. Changing it on a machine where links point elsewhere is refused (exit 2) unless every link is adopted as "shared elsewhere". |
| `--labels` | keep existing | Keys are folder paths (`~` allowed) or login emails. |
| `--add` | none | Create one new folder per label (5.6). Never logs in under `--yes`. |
| `--accounts` | none | Shorthand: labels for existing folders in natural order; extra labels become `--add`. |
| `--share` | `defaults` = all seven switches, except `mcp` when hand-made MCP sync is detected | `none` changes no sharing. A list replaces the defaults; `-name` removes one from the defaults (`--share defaults,-history`). Off never un-shares something already linked unless the switch is named with `-` explicitly *and* `--yes` is given. |
| `--conflicts` | `keep` | How to resolve every item that differs from the primary (5.3). `--resolve` overrides per item. |
| `--hub` | `keep` if a healthy hub is connected, else `later` under `--yes` (interactive default is `here`) | See step 4. `local` is accepted for `here`, and a bare `https://...` for `join:`. |
| `--token-stdin` | | Read the device token from the first stdin line. |
| `--code` | | Machine pairing code (`wy hub pair --kind machine`) claimed at the hub step instead of reading a token; needs `--hub join:<url>` and excludes `--token-stdin` (exit 2 otherwise). A dry run never claims it and it never appears in the plan. |
| `--code-stdin` | | As `--code`, read from the first stdin line, which may also be a 22-character pairing **secret** (claimed with `secret`, machines.md 4.2 step 6). Used by `wy machine add` so no code is ever an argument. Excludes `--code` and `--token-stdin`. |
| `--hub pull` | | This machine is pulled over SSH by the hub (machines.md 3.4): `hub.reach = "pull"`, no URL, no token; the timer is installed. The plan shows "hub: pulled over SSH (run `wy machine add --reach pull` from your laptop, or `wy hub pull add` on the hub)". |
| `--hub-dial` | | With `join:`, dial this loopback `host:port` or `unix:<path>` instead of the URL's host (forward mode, machines.md 3.3). Stored as `hub.dial`. |
| `--reach` | `direct` | Recorded as `hub.reach` (`forward` when `--hub-dial` is given). |
| `--no-plugin` | | Skip plugin installation. |
| `--no-statusline` | | Skip the status line wrap. |
| `--no-launchers` | | Write no launchers. |
| `--named-launchers` | off | Also write `claude-<label>` next to `claude-N`. |
| `--launcher-dir` | `~/.local/bin` | Where launchers go. |
| `--login` | `ask` interactive, `never` under `--yes` | `ask` offers `claude auth login` for folders not logged in. |
| `--expect-plan` | | Apply only if the freshly computed plan id equals this one (6.3), else exit 4 with the new plan. This is how Claude shows a plan, asks, then applies exactly that plan. |

Exit codes: 0 done (also "nothing to do"); 1 failed and rolled back; 2 usage (every
argument error, unknown flags included, in every `wy` command) or another command holds
`setup.lock` (7.6); 3 needs a person (no terminal and no `--yes`); 4 plan changed (`--expect-plan`) or a precondition
changed during apply (rolled back); 5 undo partly done (some entries skipped, listed);
6 local setup done but the hub step failed (local changes kept); 7 Claude Code not found.

`--yes` is always safe to rerun: the plan of a second run is empty if nothing changed.

## 5. Sharing

### 5.1 What can be shared, and how

| Item | Kind | Mechanism (Linux, macOS) | Windows |
| --- | --- | --- | --- |
| `settings.json` | file | symlink to primary | symlink if allowed (5.7), else synced copy |
| `CLAUDE.md`, `keybindings.json` | file | symlink | symlink if allowed, else synced copy |
| `history.jsonl` | file | symlink | symlink if allowed, else synced copy (line union) |
| `rules/`, `skills/`, `agents/`, `commands/`, `plans/`, `projects/` | dir | symlink | junction |
| plugin root | dir | `env.CLAUDE_CODE_PLUGIN_CACHE_DIR` in the settings target (5.2); an existing `plugins/` link is adopted | same |
| MCP servers | key | `mcpServers` copied into each state file (5.4) | same |

Never shared, never touched: `.credentials.json`, `.claude.json` (except `mcpServers`),
`sessions/`, `daemon/`, `jobs/`, `file-history/`, `session-env/`, `shell-snapshots/`,
`stats-cache.json`, `backups/`, `cache/`, `paste-cache/`, `ide/`, `state/`, `tasks/`,
`telemetry/`, `todos/`, `policy-limits.json`, `remote-settings.json`, `*.lock`, and anything
not in the table above. The table is data (`internal/share/items.go`); adding a row is a spec
change.

**When an item is linked.** Only when the primary has it. If the primary lacks an item and
no folder has it, the row says "nothing to share yet" and nothing is created (so adopting
this laptop creates no `agents/` or `rules/`). If the primary lacks it and exactly the
folders that have it agree byte-for-byte, the plan offers to *promote* one copy into the
primary and link the rest (a move, backed up). `settings.json` is the exception: if Settings
is on and the primary has none, `{}` is created there first.

**How a link is made.** Absolute target (`/home/u/.claude/skills`), created with
`os.Symlink` at a temp name in the folder and renamed over the item path, so the item path
never is absent. The target must exist first (a dangling link makes Claude Code fail with
ENOENT when it `mkdir`s inside it; seen with `plugins/`).

### 5.2 Plugins

**How Claude Code keeps plugins (2.1.296, verified):**

- The plugin root is `CLAUDE_CODE_PLUGIN_CACHE_DIR` if set, else `<config folder>/plugins`.
  It holds `installed_plugins.json`, `known_marketplaces.json`, `cache/`, `marketplaces/`,
  `data/`. The variable is honoured when set in the process environment **and** when set in
  the `env` block of `settings.json` (`claude plugin list` reads the right root either way).
- `claude plugin marketplace add <dir>` (a local directory) writes
  `extraKnownMarketplaces.<name>` into the running folder's user `settings.json` and the
  marketplace into `known_marketplaces.json` (and creates an empty `marketplaces/` in the
  root); `claude plugin install <p>@<m>` copies the plugin into `cache/<m>/<p>/<version>/`,
  writes `installed_plugins.json` and sets `enabledPlugins["<p>@<m>"] = true` in the running
  folder's settings. `marketplace remove` also drops that marketplace's `enabledPlugins`
  entries from the running folder's settings. Plugin commands only ever write the running
  folder's own `settings.json` (through a link when it is one).
- Writes to a symlinked `settings.json` go **through** the link: the link stays, the target
  is replaced by rename (new inode).
- `installPath` in `installed_plugins.json` is recorded through whichever folder ran the
  command (`/home/u/.claude-2/plugins/cache/...` when run as `~/.claude-2` with a linked
  `plugins/`). **A second install is a no-op only when the plugin is already enabled in the
  running folder's effective settings** (for example a folder whose `settings.json` links to
  the one that installed): it prints "already installed ... loads in place", exits 0 and
  changes nothing, so the first installer's path stays. If the running folder's settings
  lack the entry (own settings, shared root), the install runs in full: the **last
  installer wins**, `installPath` moves to that folder's path and `enabledPlugins` is set in
  its settings. That is why step 3 below uses `enable`, not `install`, for the other
  settings targets: it enables the plugin there without touching `installPath`.
- `claude plugin uninstall` run from any folder that shares a plugin root removes the record
  for **every** folder sharing it, while their `enabledPlugins` (if in other settings files)
  still say `true`. It removes the entry only from the running folder's settings (leaving
  `"enabledPlugins": {}`), and **keeps** `cache/<m>/<p>/<version>/`, adding an
  `.orphaned_at` file (epoch milliseconds) to it.
- **Working directory matters.** Claude Code reads `<cwd>/.claude/settings.json` as project
  settings. With cwd = `$HOME` that is the user's own `~/.claude/settings.json`, so the
  plugin shows `projectEnabled: true` and `claude plugin enable` run as another folder
  answers "already enabled". `wy` runs every plugin command with a neutral working
  directory (its state dir), never the caller's cwd or `$HOME`.
- `--json` outcome lines (2.1.296). Success: `{"command":"install|enable|disable|uninstall",
  "outcome":"ok","plugin":"p@m","pluginId":"p@m","scope":"user","message":"..."}` (uninstall
  adds `"keptData":false`; a no-op install adds `installedVersion`/`availableVersion`);
  `{"command":"marketplace-add|marketplace-remove","outcome":"ok","marketplace":"m",
  "message":"..."}` (adding a known marketplace is also `ok`: "already on disk"). When the
  goal already holds, `enable` and `disable` exit **1** with `{"command":"enable","outcome":
  "failed","plugin":"p@m","message":"Plugin \"p@m\" is already enabled","failureCode":
  "already_in_goal_state","alreadyInGoalState":true}` (stderr: `✘ Failed to enable plugin ...`).
  Other failures carry only a `failureCode`: `not_installed` (uninstall), `not_found`
  (install, unknown plugin or marketplace), `not_configured` (`marketplace remove` of an
  unknown name). `wy` treats `alreadyInGoalState: true` as success for every plugin and
  marketplace command, and also `not_installed` / `not_configured` for uninstall and
  marketplace remove, so setup is idempotent. `plugin list --json` rows have `id`, `version`,
  `scope`, `enabled`, `installPath`, `installedAt`, `lastUpdated`, `folderVersion`,
  `readFromFolder`, `projectEnabled`; `marketplace list --json` rows are flat:
  `{"name","source":"directory","path","installLocation"}`.
- Claude rewrites a `settings.json` it writes through its own schema: a value it finds
  invalid (observed: `"editorMode": "emacs"`) is dropped, and a first run in a new folder
  migrates `"model": "opus"` to `"opus[1m]"`. `wy` must expect settings it wrote to change
  under a later plugin command (a diff of the plugin commands' own keys, not of the file).

**The watchyard plugin is installed through a local directory marketplace that `wy`
owns:**

1. `wy` embeds `plugin/watchyard/` (go:embed). At setup it writes
   `<state>/plugin/marketplace/.claude-plugin/marketplace.json` (marketplace name
   `wy-local`, one plugin `watchyard`, version = `wy`'s version) and
   `<state>/plugin/marketplace/plugins/watchyard/` (written to a temp folder, then renamed).
   In this copy, each hook command is rewritten to call `wy` by absolute path:
   `[ -x '/home/u/.local/bin/wy' ] && '/home/u/.local/bin/wy' hook Stop || true`, so hooks
   work even when Claude Code is started without `~/.local/bin` on `PATH` (IDE, desktop
   app). The path must not contain the platform PATH separator (Claude Code refuses
   such plugin paths).
2. Per **plugin root** (not per folder): run, with `CLAUDE_CONFIG_DIR` set to the folder
   that owns that root (the folder whose own `plugins/` *is* the root, so `installPath` is
   canonical; for `~/.claude` the variable is unset), stdin closed, 60 s timeout:
   `claude plugin marketplace add <state>/plugin/marketplace --json`, then
   `claude plugin install watchyard@wy-local --json`.
3. Per **settings target** that did not get `enabledPlugins` from step 2 (folders with
   their own settings but a shared root): `claude plugin enable watchyard@wy-local --json`
   with that folder as `CLAUDE_CONFIG_DIR`.
4. After each command: parse its `--json` line (`outcome: ok`, or `alreadyInGoalState:
   true` on exit 1, which is also success), re-check every link
   (rule 4), and diff the plugin root against its pre-run listing to record created paths in
   the backup set.

**Exactly one watchyard plugin per folder.** Two enabled copies mean two `wy hook` runs and
two records per event with different ids, which the hub cannot de-duplicate. Detect
therefore lists every enabled `watchyard@*` (and `watchyard@skills-dir`, a plugin folder
inside `skills/`). If one is already enabled from another marketplace (for example the
GitHub marketplace, when Claude installed the plugin before running `wy setup`), it is
**adopted**: no `wy-local` install, and `wy doctor` warns only if its `hooks.json` lists
different events from the embedded one. If two are enabled, the plan disables all but
`wy-local` (or, without `wy-local`, all but the first), with a `!` line saying why.

**Sharing the plugin root:**

| Found | Plan |
| --- | --- |
| A folder's `plugins/` already links to the primary's (this laptop) | Adopt. No env key. Plugin commands run once, as the primary. |
| `plugins/` links somewhere else | Adopt as "shared elsewhere"; that root gets its own install. |
| Fresh folders (no `plugins/`, or an empty one) | Set `env.CLAUDE_CODE_PLUGIN_CACHE_DIR = "<abs primary>/plugins"` in the settings target (once, if settings are shared). It works for every way Claude is started (IDE, desktop app, hand-made launchers), not just `wy`'s launchers. |
| A non-primary folder whose own `plugins/` has plugins the shared root lacks | Same env key, plus, per missing plugin: `claude plugin marketplace add <its source from that folder's known_marketplaces.json>` and `claude plugin install <id>` into the shared root. The folder's own `plugins/` is left in place, unused. A failed install (network) is a warning, not a failure. |
| New folders on a machine that already uses links | New folders get links too ("match the layout you already use"). |

Considered and rejected: `--plugin-dir` in launchers (only works for launches through
`wy`'s launchers); `CLAUDE_CODE_PLUGIN_DIRS` in settings `env` (side-loading can be turned off
by managed policy and the plugin is not listed in `/plugin`); a `skills-dir` plugin in the
shared `skills/` (it works in 2.1.296 and loads all 11 hooks, but it puts `wy`'s files inside
the person's own skills folder and has no enable switch).

Updates (`wy update`, [machines.md](machines.md) 7.3 step 7): rewrite the marketplace
folder for the new version, then per root `claude plugin marketplace update wy-local` and
`claude plugin update watchyard@wy-local`.

### 5.3 Conflicts: a folder already has different content

For each folder F, item X, primary P:

| F/X | Result |
| --- | --- |
| absent | link |
| link resolving to P/X | adopted, untouched |
| link resolving elsewhere | adopted as "shared elsewhere", untouched, never re-pointed |
| regular file/dir identical to P/X (bytes; for dirs, same tree: names, types, modes, file hashes, link targets) | replaced by a link; the original is moved into the backup set |
| regular file/dir that differs | **conflict**, resolved by `--conflicts` / `--resolve` / the prompt |

Resolutions:

| Choice | Files | Directories |
| --- | --- | --- |
| `keep` (default, also under `--yes`) | F/X left as is, not linked; reported as `! separate` and in `wy doctor` as info | same |
| `primary` | F/X moved into the backup set, F/X linked to P/X | same |
| `folder` | P/X moved into the backup set (`move_to_backup`), then F/X moved to the now absent P/X, then F/X linked. Undo restores both exactly | same |
| `merge` | Only `history.jsonl`: union of lines, de-duplicated by exact bytes, ordered by each line's `timestamp` field (lines without one keep their relative order at the end); written to P, F's copy backed up, F linked. Other files: refused (use `primary` or `folder`) | Union: every entry of F missing from P is moved into P (one `move` per entry, each journaled; for `projects/` down to the session file); entries present in both and identical are dropped from F; entries present in both and different stay a conflict, and the directory is then **not** linked (F keeps the remainder; the report lists the entries). `settings.json` is never merged. |

`projects/` merges entry by entry at the project level and then the session file level, so
a merge of two large histories is a series of renames, not copies. All moves are renames
inside one filesystem; if F and P are on different filesystems the plan says so and `merge`
falls back to copy+delete, with the total size shown.

### 5.4 MCP servers (`.claude.json`, key `mcpServers` only)

The state file holds the login, so it is never linked, copied or rewritten as a whole.

**What is synced.** The top-level `mcpServers` object of each state file in the sync set
(every logged-in folder with MCP sharing on; for `~/.claude`, `~/.claude.json` and, if
present, `~/.claude/.claude.json`). Per-project MCP settings under `projects` are not synced
(a hand-made script may do that; see 11).

**One list, three-way merge.** The base is `<state>/share/mcp-base.json` (mode 0600; it
holds server definitions, which may contain secrets, and is never sent anywhere or printed).
On each sync:

1. Read each file's `mcpServers`. A file without the key (Claude Code recreated it, or never
   wrote one) votes for nothing: it receives the merged list. Only an explicit `{}` (or a
   list missing a server) counts as removing servers, and only for a file that was in the
   last synced set.
2. Per server name, compare each file with the base: added, removed, changed, unchanged.
3. Merge: a change made in exactly one file wins; the same change in several files is one
   change; different changes to one name: the primary's version wins (or, if the primary
   didn't change it, the lowest folder in natural order), and the losing version is logged in
   `<state>/share/mcp-conflicts.jsonl` (name and file, not values) and shown by `wy doctor`.
4. Write the merged object to each file whose `mcpServers` differs, then store it as the base.
   Each server removed everywhere is appended to `<state>/share/mcp-removed.jsonl` (time,
   name, the folder whose file no longer had it; never values), which `wy doctor` shows.

**Guard.** A pass whose merge would remove *every* server of a non-empty base while some file
still holds one is not run: nothing is written, `<state>/share/mcp-guard.json` records the
names and the folders that lack them, and `wy doctor` warns with how to resume (add them back,
or remove them in every folder by hand). The next pass that runs removes the guard file.

**Undo.** Every `mcp_sync` entry journals `mcp-base.json` with the state files, so undo
restores the base with them (or removes it if the run created it): a reverted file is never
read as a deletion by the next sync.

First sync (no base): the base is the primary's list; servers that exist only in other
folders are added to everyone (union), and same-name differences resolve to the primary.

**How a file is written (`internal/jsonedit`):**

1. Take Claude Code's own lock: `mkdir <file>.lock` (a directory, as seen in 2.1.296); if it
   exists and its mtime is older than 10 s it is stale and removed; wait up to 5 s, else skip
   this file until the next sync. **Uncertain**: whether every Claude Code write path takes
   this lock.
2. Skip the file (until the next sync) if a live session uses that folder
   (`<folder>/sessions/*.json` with a live pid), unless `wy share sync --now` is run by hand.
3. Read the bytes; tokenize the JSON; find the byte span of the top-level `mcpServers` value
   (or the position before the final `}` if absent); splice in the new value serialized with
   the file's indent (detected from the first nested line; default 2 spaces); every other
   byte stays identical.
4. Check: the result parses; every top-level key except `mcpServers` is deep-equal to before;
   `oauthAccount` byte-identical. Any failure → no write.
5. Write `<file>.wy.<rand>.tmp` in the same folder with the original mode (0600), fsync,
   compare the file's size and mtime with step 3 (changed → retry from 3, at most 3 times),
   rename over it, fsync the folder, remove the lock.

When it runs: at the end of `wy setup` / `wy account share`, and in `wy tick` (once a minute)
only when some file's `mcpServers` hash differs from the base. Off when MCP sharing is off
(`share.mcp = false`) or adopted from a hand-made script (`share.mcp = "external"`).

### 5.5 Settings edits made by `wy`

`wy` changes at most these JSON pointers in a settings target, each with the same byte-splice
editor (5.4 step 3–5, without the lock or session checks):

| Pointer | Why |
| --- | --- |
| `/statusLine/command` (and `/statusLine/type` if `statusLine` was absent) | Status line wrap (section 9.1) |
| `/env/CLAUDE_CODE_PLUGIN_CACHE_DIR` | Shared plugin root (5.2) |
| `/env/CLAUDE_CODE_DISABLE_TERMINAL_TITLE` | Watchyard tab titles on (`wy term titles on`, terminal.md T6, T10) |

Everything else in `settings.json` (`enabledPlugins`, `extraKnownMarketplaces`) is written by
`claude plugin ...`, never by `wy`. A settings file is edited at its resolved target (all
link levels followed); the temp file is created in the target's folder, so the rename
replaces the target and every link to it keeps working.

### 5.6 New folders (`wy account add`, `--add`)

Next free name: `~/.claude-N` with the smallest N ≥ 2 such that neither the path nor any
`~/.claude-N*` entry exists (a file named `~/.claude-N` blocks N). Created with mode 0700,
then the enabled items linked (5.1), the plugin root applied (5.2), and the folder added to
the MCP sync set. No `.claude.json` is created: Claude Code makes it on first run.

### 5.7 Windows and macOS

- **Windows directories:** junctions (`mklink /J` semantics through
  `golang.org/x/sys/windows` reparse points; no admin or Developer Mode needed). Detected by
  reparse tag, not by `os.ModeSymlink` (Go's reporting of junctions changed between
  versions).
- **Windows files:** `os.Symlink` is tried once at detect time in `<state>` (works with
  Developer Mode or admin). If it fails, file items become **synced copies**: each copy and
  its last-synced SHA-256 are kept in `<state>/share/copies.json`; `wy tick` and every
  `wy setup` compare: if only one side changed since the last sync, it is copied to the
  others; if two changed, nothing is copied and `wy doctor` reports the conflict.
  `history.jsonl` copies merge by line union instead. Settings edits by `wy` go to the
  primary and are then synced. The ledger (`copies`, 9.3) is the authority: a copy is synced
  only while the ledger lists it, so an entry left in `copies.json` never makes `wy` write a
  file; without a readable ledger the copies pass is skipped. A group that fails is reported
  and the pass goes on with the others. Undo of the link action, `wy account remove` and
  `wy uninstall` take the copy out of both. `wy account remove --unshare` makes independent
  copies and never registers them as synced copies.
- **macOS:** the login lives in the Keychain, tied to the config folder path (**uncertain**:
  exact item name), so: logins are detected only with `claude auth status`; a folder is never
  renamed or moved; `wy account remove` cannot delete a Keychain item (it says to run
  `claude auth logout` first if that is wanted). Symlinks behave as on Linux. Services are
  launchd agents in `~/Library/LaunchAgents`. `~/.local/bin` is often not on zsh's PATH; the
  installer prints the line.

## 6. The plan

### 6.1 Actions

The planner is a pure function `Plan(State, Choices) → Plan`. Each action names the paths it
touches and the exact state it expects to find (its precondition: type, link target, size,
SHA-256 or "absent"); apply checks every precondition before changing anything and again right
before acting. A precondition on a path an earlier action of the same plan changes (the
`link` after a `move` away, the `move` after a `move_to_backup`) is only checked right before
acting.

| `op` | Fields | Journaled as |
| --- | --- | --- |
| `mkdir` | `path`, `mode` | created |
| `link` | `path`, `target`, `kind` (`symlink`, `junction`, `copy`) | created (path was absent) |
| `replace_with_link` | `path`, `target`, `kind` | original moved into the set, link created |
| `move` | `from`, `to` | renamed (merge, promote, `folder` resolution). `to` must be absent: a move never merges into or overwrites an existing path (precondition error, exit 4); `to` is journaled as created |
| `move_to_backup` | `path` | the original moved into the set, nothing left at `path` (`folder` resolution) |
| `merge_lines` | `path`, `from` (history.jsonl) | both originals saved, merged file written |
| `json_set` / `json_delete` | `file` (resolved), `via` (links), `pointer`, `value` (redacted in output for `env` and `mcpServers`) | whole file saved + key before/after |
| `mcp_sync` | `files`, `names` added/changed/removed | each file saved + key before/after |
| `write_file` | `path`, `mode`, `content_sha256` (launchers, marketplace manifest) | created or original saved |
| `write_tree` | `path` (marketplace plugin copy) | created (or old tree moved into the set) |
| `run` | `argv`, `env` (`CLAUDE_CONFIG_DIR`, null = unset), `touches` (files saved before), `watch` (folders diffed after) | files saved, created paths listed |
| `service` | `name`, `unit` (hub service) | unit file saved/created; enable/disable recorded |
| `connect` | `url` (token never in the plan) | `config.toml` and `<state>/token` saved |
| `config_set` | `key`, `value` | `config.toml` saved |
| `login` | `folder` | not journaled (never undone) |

`touches` never includes a `.claude.json`: Claude Code rewrites its own state file on every
run, and restoring it would undo a login refresh.

### 6.2 Order of apply

1. Lock `<state>/setup.lock` (non-blocking; a second setup exits 2).
2. Create the backup set and write its manifest header (7.1).
3. Local steps, in this order: new folders → sharing (links, resolutions, env key) →
   plugin (marketplace tree, `claude plugin ...`) → status line → MCP sync → launchers →
   `config.toml`.
4. Any failure in step 3 → roll back everything done so far from the journal (7.5), mark the
   set `rolled-back`, exit 1 (or 4 for a precondition change).
5. Hub step (`connect` / `service`). Failure → report, keep the local steps, exit 6. The
   report says `partly applied` (JSON `mode: "partly-applied"`), not `applied`: the Hub
   section adds `✗ not done: <step>: <error>` and a `fix:` line (check `<url>/health` or
   the service, make a new code with `wy hub pair --kind machine`, rerun `wy setup --hub
   …`); `hub` in the JSON carries `step` and `fix`. The backup set is still `applied` (its
   local steps are done and undoable).
6. Logins (interactive only), one folder at a time.
7. `wy doctor`, summary, mark the set `applied`, update the ledger.

### 6.3 Output formats

**Text** (the dry run and the screen before "Apply?"). Sections in apply order; one line per
action or per group of identical actions. Markers: `=` keep, `+` add, `~` change, `−` remove,
`!` conflict or choice needed, `?` needs you, `·` skipped. Paths use `~`. Changes to a shared
target say how many folders they cover. The last line counts changes, files backed up, links
changed and logins touched (always 0). Example: section 11.

**JSON** (`--json`, stdout, one object, `"v": 1`):

```json
{
  "v": 1,
  "mode": "dry-run",
  "plan_id": "p_9c41e0a7d2b3",
  "primary": "~/.claude",
  "state": { "claude": {"path": "~/.local/bin/claude", "version": "2.1.296"},
             "folders": [{"folder": "~/.claude-2", "logged_in": true, "email": "you@work.example",
                          "org": "Work Inc", "plan": "team", "label": "work", "colour": "#f59e0b",
                          "launcher": {"name": "claude-2", "kind": "shell-function", "ours": false},
                          "items": {"settings.json": {"state": "linked", "target": "~/.claude/settings.json"}}}],
             "hub": {"url": null, "healthy": false} },
  "keep":      [{"what": "link", "path": "~/.claude-2/settings.json", "target": "~/.claude/settings.json"}],
  "actions":   [{"id": "a3", "step": "statusline", "op": "json_set", "file": "~/.claude/settings.json",
                 "via": ["~/.claude-2/settings.json"], "pointer": "/statusLine/command",
                 "before": "bash ~/.claude/statusline-command.sh",
                 "value": "/home/u/.local/bin/wy statusline --wrap 'bash ~/.claude/statusline-command.sh'",
                 "covers": ["~/.claude", "~/.claude-2"]}],
  "conflicts": [{"folder": "~/.claude-3", "item": "CLAUDE.md", "resolution": "keep",
                 "choices": ["keep", "primary", "folder"]}],
  "needs":     [{"kind": "login", "folder": "~/.claude-4",
                 "command": "CLAUDE_CONFIG_DIR=~/.claude-4 claude auth login --claudeai"}],
  "summary":   {"changes": 4, "backed_up": 3, "links_changed": 0, "logins_touched": 0}
}
```

After an apply, `mode` is `applied` (or `partly-applied` when only the hub step failed,
exit 6; or `rolled-back`), and the object adds `"backup":
"<id>"`, `"applied": [action ids]`, `"hub": {...}` and `"doctor": <wy doctor --json>`.

`plan_id` = `p_` + the first 12 hex characters of SHA-256 over the canonical JSON of
`actions` and `conflicts` (keys sorted, no whitespace) **including** each action's
precondition. Identical machine state and identical choices give the same id.

## 7. Backups and undo

### 7.1 Layout

```
<state>/backup/
  20261012T173000Z/            backup id = UTC start time, basic ISO 8601 (Windows-safe);
                               a clash in the same second appends -2, -3
    manifest.json              the journal (below), rewritten atomically after every entry
    files/000001               saved file bytes (one per saved file; mode 0600)
    trees/000002/              saved directories (moved here by rename, never copied
                               when on the same filesystem)
    keys/000003.json           a JSON value removed or replaced by a key edit
    undo-saved/                what an undo replaced (only with --force)
```

The backup folder is mode 0700 and lives only in `<state>` (never synced, never sent).

### 7.2 `manifest.json`

```json
{
  "v": 1,
  "id": "20261012T173000Z",
  "command": "wy setup --yes --hub later",
  "wy_version": "0.3.0",
  "machine": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K",
  "plan_id": "p_9c41e0a7d2b3",
  "started_at": "2026-10-12T17:30:00.120Z",
  "finished_at": "2026-10-12T17:30:04.880Z",
  "status": "in-progress | applied | rolled-back | undone | partly-undone",
  "entries": [
    {
      "seq": 3,
      "action": "a3",
      "op": "json_set",
      "phase": "intent | done | reverted",
      "paths": [
        {
          "path": "/home/u/.claude/settings.json",
          "via": ["/home/u/.claude-2/settings.json"],
          "before": {"type": "file", "mode": "0644", "size": 2317, "mtime_ns": 1760290000000000000,
                     "sha256": "...", "saved": "files/000003"},
          "intended": {"type": "file", "size": 2401, "sha256": "..."},
          "after":  {"type": "file", "mode": "0644", "size": 2401, "sha256": "..."},
          "keys":   [{"pointer": "/statusLine/command", "before_present": true,
                      "saved": "keys/000003.json", "after_sha256": "...", "intended": true}]
        }
      ],
      "created": []
    }
  ]
}
```

- `type` is one of `absent`, `file`, `dir`, `symlink`, `junction`, `copy`. A link's
  `target` is the raw `readlink` string, so relative links come back relative.
- An entry is written with `phase: intent` (and fsynced) *before* the action, and set to
  `done` after. A crash leaves at most one `intent` entry, which undo treats as "maybe done".
  The intent records what the action is about to make: `intended` is the state the path will
  have (file bytes computed first, a link's target, a folder; for computed content such as a
  merge or the marketplace tree, the result is built in a scratch folder first and hashed),
  and for a JSON edit each key's `after_sha256` with `intended: true`. Recovery restores
  `before` only when the path now equals `before` (nothing to do) or `intended`; anything
  else was changed after the crash and is skipped (or reverted key by key where keys are
  recorded).
- `after` of a folder made by a tree action (`link` with `copy`, `replace_with_link`,
  `write_tree`) carries a tree hash (every relative path, type, mode and content): a folder
  only equals its `after` while nothing was added, removed or changed inside it.
- `created` lists paths that did not exist before (files and folders), deepest first, for
  `run` actions found by diffing the watched folders, with their state at the end of the run.
  A `run` entry also records the names it may create under its watched folders
  (`wy-local`); undo removes only created paths under those names.

### 7.3 `wy setup --undo [id]`

- Without an id: the newest set with status `applied`, `rolled-back` (if anything is left),
  or `partly-undone`. Sets are a stack: undoing an older set while a newer one is still
  applied is refused (exit 2) unless `--force`. `--undo-list` shows every set with its
  command, time, status and number of entries.
- Undo always prints its own plan first (one line per entry) and asks, unless `--yes`.
- Entries are undone in reverse `seq` order. Per path:

| Current state | Undo does |
| --- | --- |
| equals `after` (type, link target, size, hash, mode; a tree's hash) | Restores `before` **exactly**: saved file bytes with the original mode and mtime; saved trees renamed back; a link recreated with the original raw target (as a symlink if it was one, as a junction if it was one); `absent` → the created path removed: a link or file itself, a tree only when its tree hash still matches, and a parent folder the action created only when it is empty |
| differs from `after`, entry has `keys` (a JSON edit) | Key-level revert: each recorded pointer is set back to its saved value (or removed if it was absent), other changes made since are kept; reported as "restored key, kept later edits" |
| differs from `after`, no `keys` | Skipped and listed (exit 5). With `--force`: the current thing is moved to `undo-saved/` first, then `before` is restored |
| `created` folder that now holds files wy did not create | Kept and listed (a login may have landed in it: `wy` never deletes a folder holding a login). The one exception: when its only entries are a `.claude.json` without `oauthAccount` and an empty `sessions/` (what any first `claude` command leaves), those go with the folder |
| `created` file that changed since the run | Skipped and listed (exit 5) |

- `run` entries are undone without running `claude`: their `touches` files are restored by
  the rules above (the whole saved file when it differs from it only at wy's entries, else
  key-level for `enabledPlugins` and `extraKnownMarketplaces` in settings, and entry-level for
  `installed_plugins.json` and `known_marketplaces.json`, removing only the
  `watchyard@wy-local` / `wy-local` entries; a registry the run created is removed only
  while it holds nothing else), and their `created` paths under the entry's names are
  removed (other created folders only when empty). A registry the run touched is never in
  `created`: it is reverted entry by entry, so plugins registered since stay.
- `link` / `replace_with_link` entries in copy mode: undo also takes the path out of the
  synced copies (5.7), so the restored original is never synced over the primary.
- `service` and `connect` entries: the service/timer is stopped and its unit removed, the
  token file and `config.toml` restored as files (the device token on the hub is not
  revoked; the summary says how: `wy hub token revoke <name>`).
- The set's status becomes `undone` (or `partly-undone`). An undone set is kept until
  `wy uninstall --purge` or `wy setup --prune-backups --keep 10`.

**The guarantee, as tested:** for every path a run touched, if nothing else changed it after
the run, undo restores type, bytes, mode, mtime and link target exactly. Logins are outside
this guarantee because `wy` never touches them in the first place.

### 7.4 Rollback during apply

The same code as undo, run on the in-progress set right after a failure, without asking.

### 7.5 Link guard

Detect records every symlink/junction found under the folders and in the item table (on this
laptop, 48). After each `run` action and at the end of the local steps, `wy` re-reads them.
If any changed (removed, re-pointed, replaced by a file), apply stops, restores those links
from the detect record (the old raw target), rolls back, and exits 1 with the list.

### 7.6 Interrupted runs

If a set is `in-progress` when `wy setup` starts (crash, power loss, kill), setup says so and
offers: undo it (default) or mark it as applied. `--yes` undoes it before planning.

**`setup.lock`.** Every command that changes the setup or its backup sets holds
`<state>/setup.lock` (an OS file lock, released when the process ends) for its whole run:
`wy setup` (from before the recovery above to the end; not `--dry-run` or `--detect`),
`--undo`, `wy account remove` and `wy uninstall`; `wy account add|label|share` hold it while
they apply (their plans go through the same apply). A second such command exits
2 at once. A set counts as interrupted only while the lock is free; a set in progress while it
is held is a live run, and neither setup nor `wy doctor` reports or undoes it. `wy tick`
skips its share sync (MCP and copies) while the lock is held; the next tick runs it.

## 8. Accounts

### 8.1 Commands

```
wy account list [--json]
wy account add [<label>] [--colour <c>] [--no-login] [--share <switches>] [--yes] [--dry-run] [--json]
wy account login <folder|label> [--relay]
wy account label <folder|email|label> <new label> [--colour <c>]
wy account share [<folder|label>] [--share ...] [--conflicts ...] [--yes] [--dry-run] [--json]
wy account remove <folder|label> [--unshare] [--yes] [--dry-run] [--json]
wy account next [--strategy <s>] [--model <m>] [--env | --json | --explain | --exec [-- <cmd>...]] [--no-reserve] [--no-hub]
```

- **`list`:** one row per folder: label, colour, folder, launcher, logged in, email, org,
  plan, items shared (`linked`, `elsewhere`, `separate`, `copy`), MCP sync on/off. `--json`
  is the `folders` array of 6.3.
- **`add`:** plans and applies, as `wy setup`, only the new-folder steps: next free folder
  (5.6), links, plugin root, MCP sync set, launcher `claude-N` (if no existing launcher
  style says otherwise), label and colour in `config.toml`, then login (8.2) unless
  `--no-login` or `--yes`. Writes a backup set.
- **`login`:** 8.2. Not journaled.
- **`label`:** sets `slots.labels` and `slots.colours`, sends a `slot_info` (8.4), renames a
  named launcher `claude-<old>` to `claude-<new>` if `wy` wrote it. Journaled.
- **`share`:** sharing step of setup for one folder or all. Journaled.
- **`remove`:** stops managing a folder: removes the launchers `wy` wrote for it, its label,
  and takes it out of the MCP sync set and the synced copies (5.7; the copies stay as the
  folder's own files). The folder, its login and its links stay. `--unshare` additionally
  converts each link into an independent copy (dirs: a copy of the tree; the plan shows
  sizes); the copies are not synced copies. Undo turns an unchanged copy back into the link;
  a copy with changes made since is kept (exit 5). Nothing is ever deleted from the folder.
  Journaled. An unreadable ledger stops the command before any change.
- **`next`:** 8.5. Reads and recommends; changes no login and no Claude Code file.

### 8.2 Login handoff

`wy account login` (and step 6 of apply) runs `claude auth login --claudeai` with
`CLAUDE_CONFIG_DIR` set to the folder (unset for `~/.claude`), stdin/stdout/stderr inherited
from `wy`, so Claude Code's own flow runs unchanged: it opens a browser, or, on an SSH
server with no browser, prints the URL and waits for the pasted code. `wy` prints one line
before ("Logging in ~/.claude-4 (research). Claude Code will open a link; on a server, open
it on any device and paste the code here.") and, after the command exits, reads
`claude auth status --json` to confirm: "~/.claude-4 is now you@example.com (Max)". If the
login landed in an account another folder already uses, it says so (it is allowed: two
folders, one seat). `wy` never passes `--email`, `--sso` or `--console`.

**Over Claude (no terminal):** `--json` output lists each pending login in `needs` with the
exact command. The skill asks the person to run it in a terminal. `--relay` (experimental,
**uncertain**): keeps `claude auth login` running under a pseudo-terminal in a detached
helper, prints `{"url": "..."}`, and accepts the code with `wy account login <folder>
--code <code>`; if Claude Code does not print a URL or accept a code in that mode within 10 s,
it exits 3 and the `needs` command is the fallback. The relay sees the login URL and the
one-time code only, never a token.

### 8.3 Launchers

Written only for folders with no launcher of any kind (step 1, 5). Never for `~/.claude`
(that is `claude` itself), never named `claude` or `claude-1`.

```sh
#!/bin/sh
# wy-launcher v1 slot=~/.claude-2 label=work
# Claude Code as ~/.claude-2. Written by wy setup; wy uninstall removes it.
CLAUDE_CONFIG_DIR="$HOME/.claude-2"
export CLAUDE_CONFIG_DIR
exec claude "$@"
```

Windows: `claude-2.cmd` with `@set "CLAUDE_CONFIG_DIR=%USERPROFILE%\.claude-2"` and
`@claude %*`. Mode 0755, in `--launcher-dir`. The plugin root is not set here: it comes
from settings (5.2), so every way of starting Claude agrees. A launcher file whose content
hash differs from what `wy` wrote is the person's now: uninstall and undo leave it.

Escaping: every value written into a launcher is kept on one line (control characters
become spaces in comment lines); the sh value is quoted; in a `.cmd` file `%` is doubled
(`%%`) in the folder and in `rem` lines (and `^` in `rem` lines), and the marker parser
undoes that. A launcher name (from the folder or `claude-<label>`) must stay a direct child
of the launcher dir, else the plan is refused. A folder that cannot be written safely gets no
launcher and a plan warning instead: one with control characters, or, on Windows, one that
is not ASCII (the `.cmd` is UTF-8 without a BOM and cmd.exe reads the OEM code page; `wy`
does not `chcp`, which would change the person's console) or contains `"` or `^`.

### 8.4 Labels and colours to the hub

`slot_info` gains two optional fields, `label` and `colour` (record.md 4.7). The hub sets
`seat.label` / `seat.colour` from them only while the seat has no label set in the UI.

### 8.5 `wy account next`

Recommends the folder to start the next Claude Code session on, for a person or for an agent
that launches other agents. It **recommends only**: it never switches a running session, never
changes or moves a login, and never starts anything unless `--exec` is given. Implemented in
`internal/picker` and `cmd/wy/account_next.go`.

**Folders and logins.** The folders of identity.md 3 (`CLAUDE_CONFIG_DIR` of the calling
session is ignored, so `~/.claude` is always its default view), each with its login read as
`wy doctor` reads it: `oauthAccount` through the identity cache, `claude` is not run. A
folder without a login is not eligible.

**Hub data.** When `hub.url` is set and `<state>/board/token.json` holds a read token for it
(`wy board connect`; a machine token has no read scope, tokens.md 3), `wy` reads
`GET /v1/accounts` and, for each seat a local folder matches, `GET /v1/accounts/{seat}/windows`
with `since` = now − 26 h, `samples=true` and the limit keys of that seat. All hub reads share
a 5-second budget. No other endpoint and no schema change is needed.

**Matching folders to seats** is by login, never by label (a seat labelled `claude-1` on the
hub may be `claude` on this machine): (1) the seat with the folder's account and org uuids;
(2) else the seat whose `places` list this folder on this machine (`machine.json` id); (3) else
the only seat with the folder's email (two seats with that email: no match). A folder the hub
lists in any seat's `signed_out` for this machine is not eligible, unless its matched seat
lists it as a place again.

**Local data.** Every `limit_sample` in the outbox from the last 7 days (record.md 4.2; the
outbox keeps sent records for `outbox.keep_days`) is read too. For `five_hour` and
`seven_day`, a local reading of the same seat (account and org, else the same folder) newer
than the hub's replaces it, and local readings newer than the hub's newest sample are added
to the history. Without a hub, or when the hub cannot be read, local readings are all there
is: the output says `local data` and, in `--explain`, that other machines' use is missing.
Live sessions are known only from the hub.

**Windows considered.** `five_hour` and `seven_day` always. With `--model <m>` (or
`next.model`), also the model-scoped windows whose model part matches `m` in either
direction, case-insensitively (`seven_day_opus` and `weekly_opus` for `opus` or
`claude-opus-5`, `session_opus` as a five-hour kind). Other model windows,
`seven_day_oauth_apps`, `seven_day_cowork` and credit windows are shown nowhere and never
limit. A seat with no reading of `five_hour` or `seven_day` gets that window as not started
with unknown usage (below).

**Projection.** Per window, with `now`, `S` = the per-session use (`next.session_5h` for
five-hour kinds, `next.session_7d` for weekly kinds), `T` = `next.session_min`, `p` = the
pending reservations of the seat, `L` = the hub's `live_sessions`:

1. *Reading.* `u0` = `used_pct`, read at `updated_at`. A window whose `resets_at` has passed
   (hub state `reset`, or a local reading from before the reset) has `u0 = 0` read at
   `resets_at`. An idle window (hub state `idle`) or one with no reset time and 0 % has
   `u0 = 0` and has not started. `h` = hours until `resets_at`, or the window's length (5 h,
   7 days) when it has not started or has reset.
2. *Burn rate* `r` (points per hour). The history rate: over the readings of the current
   window (from `resets_at` − length) in the last 2 h (five-hour kinds) or 24 h (weekly), the
   sum of each step's rise divided by the sum of its duration, each step weighted
   `exp(−age/τ)` (age of its midpoint, τ = half the lookback); the last reading before the
   lookback anchors it at the lookback start; the time from the newest reading to now is a
   flat step (the status line writes when the value changes, so no reading means no change);
   drops count as flat. It needs 20 minutes of history. Weekly kinds fall back to the window
   average `u0 / (updated_at − window start)` (needs 30 minutes), then to the mean history
   rate of the other accounts of that kind; five-hour kinds fall back to 0. Windows that
   reset or have not started use 0. Then `r = max(r, L × S / T)`: live sessions set a floor.
3. *Now.* `n = min(100, u0 + r × age)`, age = now − reading time.
4. *Staleness.* A reading older than `next.stale_min` is stale: the band
   `σ = min(100 − n, max(r × age, S × min(1, age / T)))` is added above `n` (at least the part
   of a session that may have run unseen). A window with no reading at all is stale with
   `σ = S`. A stale reading is never treated as 0.
5. *At session end* (eligibility): `E = n + σ + r × min(h, T) + (1 + p) × S × min(1, h / T)`
   (a window that resets before the session ends only takes the share that falls before the
   reset).
6. *At reset* (ranking): `R = n + r × h + (1 + p) × S`; unused room
   `U_w = max(0, cap − R)`.

**Eligibility**, in this order (the first that fails is `excluded_reason`): logged in on this
machine; not signed out on the hub; not `exclude`d; inside its `hours` and `days`; and
`E ≤ cap` for every window considered. Each over-cap account gets `free_at`: the latest
`resets_at` of its over-cap windows (unknown if any has none); an account outside its hours
gets the next allowed minute. With no eligible account, `next_free` is the earliest `free_at`.

**Settings** (paths.md 4, `[next]`; `wy config set next.<key> <value>`):

| Key | Default | Meaning |
| --- | --- | --- |
| `next.strategy` | `smart` | Default strategy. |
| `next.model` | `""` | Default `--model`. |
| `next.session_5h` | `15` | `S` for five-hour windows, in points. |
| `next.session_7d` | `2` | `S` for weekly windows, in points. |
| `next.session_min` | `120` | `T`, minutes. |
| `next.stale_min` | `30` | Staleness threshold, minutes. |
| `next.reserve_min` | `12` | How long a pick counts as a pending session, minutes. |
| `next.sticky_margin` | `10` | `sticky`: points under each cap that end stickiness. |

Per account: `next.accounts.<key>.<field>`, where `<key>` is matched against, in order, the
hub seat id, the email (without case), the folder (`~/.claude-3`), this machine's label, then
the hub's label; the first key present wins. The email is the recommended key: it is the same
on every machine; a label works as an alias (`wy config set next.accounts.claude-3.mode
aggressive`). The field is the part after the last dot, so emails with dots work.

| Field | Meaning |
| --- | --- |
| `mode` | `conservative` (caps 5 h 60, 7 d 70, model 70; preference 0.6), `normal` (90, 90, 90; 1), `aggressive` (98, 98, 98; 1.4). Default `normal`. |
| `cap_5h`, `cap_7d`, `cap_model` | Explicit caps (0 < cap ≤ 100) over the mode's; `cap_model` defaults to `cap_7d` when only that is set. |
| `reserve_pct` | Taken off every cap (room kept for something else). |
| `priority` | `fill` order, lowest first; unset is after every set one. |
| `weight` | Multiplies the `smart` score (default 1). |
| `exclude` | `true`: never recommended. |
| `hours`, `days` | Allowed local times, e.g. `09:00-18:00` (may cross midnight) and `mon-fri` or `sat,sun`. |

Unknown fields are reported by `wy doctor` as unknown keys.

**Strategies** (`--strategy`; higher score first among the eligible, ties by label in natural
order):

- `smart` (default): `weight × preference × safety × confidence × (urgency + 0.02 × headroom)`.
  `urgency` = `U_w / max(h, 1)` of the binding weekly window (the weekly window considered with
  the least unused room): weekly room that would be lost at the reset, per hour left, so room
  that expires soonest unused comes first. `headroom` = the least `cap − E` over the windows.
  `safety` = `clamp((cap − E) / 20, 0.2, 1)` over five-hour windows: an account about to fill
  its 5 hours is ranked down. `confidence` = 0.75 for an account with no usage data at all,
  else 1.
- `expiring`: `urgency` only (ties by headroom): pure use-it-or-lose-it.
- `most-room`: the lowest `max(n + σ + p × S)` over the windows.
- `spread`: the account picked longest ago (never picked first), from the pick log.
- `fill`: the lowest `priority`.
- `sticky`: the last pick while it is eligible with `E ≤ cap − next.sticky_margin` on every
  window; otherwise `smart`.

**Reservations and the pick log.** `<state>/next/picks.json` (mode 0600, written atomically
under the advisory lock `<state>/next/picks.lock`) holds `{"v": 1, "reservations": [{"key",
"folder", "at", "until"}], "last": {key: time}, "last_key"}`. The key is the seat (`acct:<account
uuid>/<org uuid>`, else `seat:<id>`, else `slot:<folder>`), so folders logged in to one seat
share it. Ranking and recording happen under the lock: each pick adds a reservation valid for
`next.reserve_min`, and live reservations count as `p` above, so agents launched in a burst
spread over the accounts and stop at the caps. Expired reservations and log entries older
than 30 days are dropped on each write. A reservation keeps counting after the session shows
up in `live_sessions` until it expires (conservative). `--no-reserve` reads the file and writes
nothing. A file that cannot be parsed is started again empty.

**Output.**

- Default: one line, `<label>  <folder>  (<reason>)`, e.g. `claude-3  ~/.claude-3  (7d 23% →
  ~41% at reset Wed 10:00; 5h fresh; aggressive)`. The reason names the weekly windows
  (`n` → `R` at the reset), the five-hour windows (`fresh` under 1 %), `unknown` for a window
  with no reading, a mode other than
  normal, `+N reserved`, `local data` or `no usage data`, and `data <age> old` when stale.
  When the hub could not be read, `  [hub not read: <why>]` follows.
- `--env`: only `CLAUDE_CONFIG_DIR=<absolute folder>`, or `-u CLAUDE_CONFIG_DIR` for the
  default view of `~/.claude` (with the variable set to `~/.claude`, Claude Code reads
  `~/.claude/.claude.json`, which may be another login: identity.md 3). For
  `env $(wy account next --env) claude`; a folder with spaces needs `--exec`.
- `--json`: `{"v": 1, "time", "strategy", "model", "data": "hub"|"local", "hub_error",
  "reserved", "picked": {"label", "folder", "dir", "env", "reason"} | null, "next_free",
  "accounts": [...], "warnings"}`; each account has `label`, `folder`, `dir`, `default_view`,
  `seat`, `email`, `seat_matched_by`, `logged_in`, `eligible`, `excluded_reason`, `score`,
  `mode`, `settings_key`, `live_sessions`, `pending_reservations`, `data_source`
  (`hub`, `hub+local`, `local`, `none`), `data_at`, `data_stale`, `free_at`, `reason` and
  `windows` (each: `key`, `kind`, `cap_pct`, `used_pct`, `reading_at`, `age_min`, `stale`,
  `now_pct`, `uncertainty_pct`, `at_session_end_pct`, `at_reset_pct`, `resets_at`,
  `hours_to_reset`, `rate_pct_per_hour`, `rate_from` (`history`, `window`, `global`,
  `live_sessions`, `none`), `unused_at_reset_pct`, `over_cap`, `source`). Eligible accounts
  come first, best first. Times are RFC 3339 UTC.
- `--explain`: the same as a table (5 h now→end/cap, 7 d now→reset/cap, other windows, data
  source and age, why), then the pick line.
- `--exec [-- <cmd> [args...]]`: runs `<cmd>` (default `claude`) found on `PATH` with the
  pick's `CLAUDE_CONFIG_DIR` (unset for the default view of `~/.claude`), replacing `wy` (on
  Windows, waiting for it and exiting with its code). It prints the pick line on stderr first.
  It is an explicit action of the caller.
- `--no-hub`: local data only.

`--env`, `--json`, `--explain` and `--exec` do not mix (exit 2).

**Exit codes.** 0: an account was picked. 4: no account is eligible; the line on stderr says
when the earliest one frees up (or that none does at a known time) and points to
`--explain`; `--json` and `--explain` still print their document on stdout. 2: usage. 1:
another error (a hub that cannot be read is not one: the command falls back to local data).

## 9. Status line, ledger, doctor, uninstall

### 9.1 Wrapping rule

Per settings target, with `S` = the absolute path of the installed `wy`:

| `statusLine` before | After |
| --- | --- |
| absent | `{"type": "command", "command": "S statusline"}` |
| `type: command`, command `C` not ours | command = `S statusline --wrap 'C'` (POSIX single quotes; `'` in `C` becomes `'\''`); other keys (`padding`) untouched |
| already ours (`^\S*wy(\.exe)? statusline( --wrap <one shell word>)?$`, any path to `wy`) | unchanged (idempotent; never double-wrapped). If the path to `wy` differs from `S`, only the path is updated |
| another `type` | untouched, warning |

The original command is recorded in the ledger per target. **Unwrap** (uninstall): if the
command still matches the pattern, `--wrap` with its word unquoted becomes the command again;
without `--wrap` the whole `statusLine` object is removed if `wy` created it, else just the
command restored from the ledger. If the person changed the command since (no longer
ours), unwrap leaves it and says so. **Uncertain:** on Windows Claude Code may run the
status line through Git Bash or `cmd`; the wrap uses the quoting of whichever shell runs it,
to be confirmed in the Windows CI job before Windows is supported.

### 9.2 `config.toml` keys added (paths.md 4)

```toml
[share]
primary = "~/.claude"
mcp = true                  # true | false | "external" (a hand-made script syncs it)
mcp_exclude = []            # folders left out of MCP sync

[slots]
labels  = { "~/.claude-2" = "work" }
colours = { "~/.claude-2" = "#f59e0b" }
```

`config.toml` is backed up as `config.toml.bak` before `wy setup` first writes it (paths.md 2)
and journaled in the backup set.

### 9.3 Ledger (`<state>/setup/ledger.json`)

What is installed now: `plugin` (marketplace path, plugin id, roots and settings targets it
was enabled in, adopted or ours), `statusline` (per target: original command, or "created"),
`env_keys` (targets where `CLAUDE_CODE_PLUGIN_CACHE_DIR` was set by `wy`), `launchers`
(path, sha256), `mcp_sync` (set of files), `copies` (Windows), `hub_service`, `timer`,
`terminal` (pane, Warp
plugin, titles, tab configs; terminal.md T10). Updated at the end of every
apply, undo and uninstall. A command that cannot read it changes nothing; uninstall keeps
the record of everything it did not remove (left as is, skipped, or failed).

### 9.4 `wy doctor` additions (the full doctor)

Per folder: logged in; exactly one watchyard plugin enabled and its `wy` path exists;
status line ours; every shared item still linked (baseline from the ledger, not just
`doctor.json`); conflicts left `separate` (info); MCP drift and conflicts; Windows copies in
conflict. Machine: `wy` on `PATH` (warn) and at the path baked into the plugin (fail);
hub reachable and token accepted; timer active; on Linux `Linger=yes` when the hub runs here
or the machine is a server; an `in-progress` backup set while `setup.lock` is free (fail,
with `wy setup --undo`); an MCP pass refused by the guard (warn, 5.4); the latest MCP
servers removed by the sync (info, from `mcp-removed.jsonl`).

### 9.5 `wy uninstall`

```
wy uninstall [--yes] [--dry-run] [--json] [--purge]
```

Plans and applies, from the ledger, with a backup set. The plan shown before the question
lists every step, the timer and the hub service included (`--json`: the `uninstall` object:
`timer`, `hub_service`, `copies`, `purge`, and after the run `failed`); nothing is removed
without `--yes` or a confirmation at a terminal (exit 3), even when the plan has no other
step. It holds `setup.lock` (7.6).

1. Plugin: per root, `claude plugin uninstall watchyard@wy-local --json` then
   `claude plugin marketplace remove wy-local --json` as the root's owning folder; per other
   settings target, the `enabledPlugins` key removed by `claude plugin disable`. Link guard
   applies. If `claude` is missing, the same keys and entries are removed with `jsonedit`.
   An adopted plugin from another marketplace is left installed (it is the person's).
2. Status line unwrapped (9.1). `env.CLAUDE_CODE_PLUGIN_CACHE_DIR` removed where `wy` set
   it, but only after any plugin the person installed into the shared root has been listed
   with a warning that folders will see their own `plugins/` again.
3. Timer and hub service removed (`internal/timer`, hub unit); the synced copies stop being
   synced (5.7; the files stay). The hub database stays. Run after the journaled steps
   applied; a failure is reported (exit 1) and its ledger record kept.
4. Launchers `wy` wrote and that still have `wy`'s hash removed.
5. MCP sync stops (`share.mcp = false`). `mcpServers` keys stay as they are.
6. Kept: every config folder, every login, every link and copy, labels, the
   backups, `config.toml`, the outbox and the device token.

`--purge` also removes `config.toml` and `config.toml.bak` (never the folder: paths.md 2),
the state folder (outbox, token, machine id, backups) after a "this deletes N unsent
records and every backup" confirmation, and the marketplace folder. Since `WY_STATE_DIR`
may point anywhere, it refuses (exit 2, before any change) a state folder without wy's
`machine.json` or `setup/ledger.json`, and one that is or holds the home or config folder. It does not remove the
`wy` binary; the last line prints `rm ~/.local/bin/wy`.

## 10. Hooks already in settings.json

`wy setup` never edits a `hooks` entry. The only pointers it writes in a settings file are the
ones in 9.1, so hook entries that `wy` did not write stay as they are, and undo and uninstall
leave them alone.

## 11. This laptop (the acceptance example)

What step 1 finds on the development laptop (12 October 2026; emails and org names
replaced):

- Claude Code 2.1.296 at `~/.local/bin/claude`.
- Seven folders: `~/.claude` (logged in, Max), `~/.claude-2` (logged in, Max), `~/.claude-3`
  (logged in, Team), `~/.claude-4` … `~/.claude-7` (not logged in). `~/.claude-api-env` is a
  file and is ignored.
- `~/.claude-2` … `~/.claude-7` each hold eight links to `~/.claude`: `settings.json`,
  `CLAUDE.md`, `commands`, `skills`, `plugins`, `projects`, `history.jsonl`, `plans` (48
  links). No folder has `agents/`, `rules/` or `keybindings.json`.
- `~/.claude/settings.json`: `statusLine.command` = `bash /home/u/.claude/statusline-command.sh`;
  three hook entries of another tool; `enabledPlugins` has three plugins, all off; one
  `extraKnownMarketplaces` entry.
- `~/.claude/plugins/installed_plugins.json`: three plugins, one `installPath` through
  `~/.claude-2/plugins/...` (last installer wins; works through the link).
- Launchers: shell functions `claude` and `claude-2` … `claude-7` in `~/.bashrc`, each calling
  `~/bin/claude-launch <N>`, which sets `CLAUDE_CONFIG_DIR`, runs
  `~/.claude/sync-mcp-to-2.sh` for N > 1, and adds per-area `--plugin-dir`, `--settings`
  and `--mcp-config` from `~/projects/<area>/.claude-area/`.
- MCP: `mcpServers` is empty in every state file (MCP servers come from the area
  `mcp.json` files); a hand-made sync exists (`sync-mcp-to-2.sh`).
- `~/.claude/.claude.json` exists next to `~/.claude.json` (from runs with
  `CLAUDE_CONFIG_DIR=~/.claude`); leftover `.claude.json.tmp.*` files exist in some folders.
- No `wy` installed, no `config.toml`, no `<state>` yet. Tailscale installed.

`wy setup --dry-run` with the defaults then prints:

```
wy setup — plan (dry run: nothing changed)                                  p_…

Accounts
  = ~/.claude     claude     logged in · Max     launcher: claude (shell function)
  = ~/.claude-2   claude-2   logged in · Max     launcher: claude-2 (shell function)
  = ~/.claude-3   claude-3   logged in · Team    launcher: claude-3 (shell function)
  ? ~/.claude-4 … ~/.claude-7   not logged in    launchers: claude-4 … claude-7 (shell functions)

Sharing (primary ~/.claude)
  = settings.json, CLAUDE.md, commands/, skills/, plugins/, projects/, history.jsonl, plans/
      linked by hand in 6 of 6 folders (48 links, kept)
  · agents/, rules/, keybindings.json   nothing to share yet
  = MCP servers   synced by your launcher (~/.claude/sync-mcp-to-2.sh)

Plugin
  + write  ~/.local/state/watchyard/plugin/marketplace/   watchyard 0.3.0 (wy-local)
  + run    claude plugin marketplace add … --json   as ~/.claude
             edits ~/.claude/settings.json (extraKnownMarketplaces), ~/.claude/plugins/known_marketplaces.json
  + run    claude plugin install watchyard@wy-local --json   as ~/.claude
             edits ~/.claude/settings.json (enabledPlugins), ~/.claude/plugins/installed_plugins.json
             covers 7 folders (shared settings and plugins)

Status line
  ~ ~/.claude/settings.json  statusLine.command   (shared by 7 folders)
      was  bash /home/u/.claude/statusline-command.sh
      now  /home/u/.local/bin/wy statusline --wrap 'bash /home/u/.claude/statusline-command.sh'

Launchers
  · none written (every folder already has one)

Config
  + ~/.config/watchyard/config.toml   share.primary, share.mcp = "external", labels for 7 folders

Hub
  · later   (wy connect <url> --token-stdin, or wy setup --hub join:<url>)

Needs you
  ? log in ~/.claude-4 … ~/.claude-7 when you want them: claude-4 auth login …

5 changes · 3 files backed up · 0 links changed · 0 logins touched
```

With `--hub join:https://<hub>.<tailnet>.ts.net --token-stdin` the Hub
section becomes `+ connect https://… (token from stdin), timer every minute, linger: yes`.

**Acceptance test on a laptop:**

1. Before: record `find ~/.claude* -maxdepth 1 -printf '%p %y %l %m\n'` and SHA-256 of
   `~/.claude/settings.json`, `~/.claude/plugins/installed_plugins.json`,
   `~/.claude/plugins/known_marketplaces.json`, every `~/.claude*/.claude.json` (for
   comparison only), `~/bin/claude-launch`, `~/.bashrc`.
2. `wy setup --dry-run --json`: exactly the five actions above; `summary.links_changed = 0`.
3. `wy setup --yes --hub later`: exit 0; the 48 links have the same `readlink`; no file
   appears in `~/.local/bin` except `wy`; `~/bin/claude-launch`, `~/.bashrc` and every
   `.claude.json` byte-identical to before as far as `wy` is concerned (Claude Code itself
   may rewrite `~/.claude.json` when `wy` runs `claude plugin`; the test compares
   `oauthAccount` and `mcpServers` there instead); `settings.json` gained only
   `extraKnownMarketplaces.wy-local`, `enabledPlugins["watchyard@wy-local"]` and the
   wrapped command.
4. `wy doctor`: plugin and status line present in the 3 logged-in folders, `~/.claude-4` …
   `~/.claude-7` reported as "not logged in" (their hooks are not judged);
   every link still a link.
5. A new session with `claude-2` writes `wy` records with `~/.claude-2`'s login, and the
   status line still shows the old line first.
6. `wy setup --yes --hub later` again: "0 changes", no new backup set.
7. `wy setup --undo --yes`: `settings.json`, `installed_plugins.json` and
   `known_marketplaces.json` byte-identical to step 1 (if no plugin was changed in between),
   `plugins/cache/wy-local` gone, `config.toml` absent again, links unchanged.

## 12. Risks and open questions

1. **Concurrent `.claude.json` writers.** `wy` takes the `.claude.json.lock` directory lock
   and only writes when no session uses the folder, but it is not known whether every Claude
   Code write path takes that lock or re-reads before writing. A running session could
   write back an old `mcpServers`. Mitigation: sync only on change, verify on the next tick,
   log flapping as a doctor warning; never touch other keys.
2. **`env.CLAUDE_CODE_PLUGIN_CACHE_DIR` in settings** was verified with `claude plugin list`,
   not yet with a full session loading hooks from it. The E2E test with the real
   `claude -p` must confirm it; fallback: plugin-root links (what this laptop already does).
3. **Last-installer-wins `installPath`.** Any `claude plugin install` from a linked folder
   writes a path through that folder's link. Harmless while links exist; breaks if a person
   later removes that link. `wy` always installs as the owning folder and `wy doctor`
   reports paths that go through another folder's link (info).
4. **Uninstall from one folder removes the plugin for all folders sharing the root** (Claude
   Code behaviour). `wy` runs it once per root and also cleans the other settings targets;
   a person running `claude plugin uninstall` by hand in one folder will hit this.
5. **Directory-source marketplaces may load straight from the folder** (`claude plugin
   list` showed `readFromFolder` pointing at the marketplace copy). If so, rewriting
   `<state>/plugin/marketplace` changes live plugins without `claude plugin update`; and if
   `<state>` is deleted, Claude may fall back to the cache copy or fail. To be tested;
   `wy doctor` checks the folder exists.
6. **Logging in without a terminal** (Claude-run setup) depends on `--relay`, whose behaviour
   with Claude Code's login flow under a pseudo-terminal is unverified. Fallback: the person
   runs the printed command.
7. **Shell-function launcher detection** runs an interactive shell (`$SHELL -ic`), which can
   be slow or print noise for some setups; the static scan is the fallback, and a missed
   function only means `wy` writes a `claude-N` file that the function shadows in
   interactive shells (non-interactive uses would then skip the person's area setup).
8. **Windows**: junction detection across Go versions, status line quoting, synced copies of
   `settings.json` racing Claude Code's own writes, and `wy.exe` replacement while hooks run
   are all untested; Windows support is best effort and gets a CI job, not a
   support promise.
9. **macOS Keychain naming** is not verified; anything relying on it is avoided rather than
   implemented.
10. **Undo's byte-for-byte promise** holds only for paths nothing else changed since the run;
    `settings.json` is shared and edited by Claude Code (`/config`, `/plugin`), so in real use
    undo will often be key-level. That is by design, and the summary says which it was.
11. **Other tools' `settings.json.bak.*` files** in each folder and `~/.claude.json`'s
    leftovers are ignored; they are not backups `wy` can use.

## 13. What was checked against Claude Code 2.1.296

Run on 12 October 2026, in a throwaway `HOME` (the real folders were only read):
`claude plugin --help`, `claude plugin marketplace --help`, `claude plugin install --help`,
`claude plugin uninstall --help`, `claude plugin init --help`, `claude auth login --help`;
`marketplace add <dir>` + `install` through a symlinked `settings.json` (link kept, target
replaced, `extraKnownMarketplaces` with `source: directory`); install again from a second
folder sharing `plugins/` (path rewritten, `enabledPlugins` set only in that folder's
settings); `uninstall` from that folder (record gone for all, other settings still enabled);
`CLAUDE_CODE_PLUGIN_CACHE_DIR` in the process and in settings `env` (both honoured by
`plugin list`); a plugin folder in `skills/` (listed as `watchyard@skills-dir`, enabled,
11 hooks); `claude auth status --json` per folder on the laptop (read-only).

Re-checked on 13 October 2026 with the contract test (`WY_E2E_REAL_CLAUDE=1 go test
./test/e2e/setup/fakeclaude -run Contract`, throwaway `HOME` only, 2.1.296). It corrected
three statements above, now fixed in 5.2: a second `install` is a no-op when the plugin is
already enabled for the running folder (it is not always "last installer wins"); the
working directory decides whether `~/.claude/settings.json` also counts as project settings
(`enable` then says "already enabled", exit 1, `alreadyInGoalState`); and `uninstall` keeps
the cache folder (marked `.orphaned_at`). It also recorded the `--json` outcome shapes and
failure codes listed in 5.2, and that `marketplace list --json` is flat.
