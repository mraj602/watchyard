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
changes or moves a login, and never starts anything unless `--exec` is given. Each pick is
recorded as a **lease** in an append-only ledger on this machine (mirrored to the hub without
waiting for it), so the sessions of a burst spread over the accounts and stop at their limits.
An agent that launches several sessions asks for them in one call (`--n`) and names each one
with the session id it will pass to `claude --session-id` (`--session-id`). Implemented in
`internal/picker`, `internal/leases` and `cmd/wy/account_next.go`.

**Folders and logins.** The folders of identity.md 3 (`CLAUDE_CONFIG_DIR` of the calling
session is ignored, so `~/.claude` is always its default view). A folder is logged in by the
one rule `wy doctor` also uses, `identity.Login.LoggedIn` (identity.md 4): the state file's
`oauthAccount`, overruled by a `claude auth status` that said logged out after
`oauthAccount` took its current value (Claude Code keeps `oauthAccount` after a logout).
When `claude` is on PATH, `claude auth status --json` is asked again for every folder whose
cached answer is older than 1 minute, in parallel with the 10-second timeout of identity.md 4,
as `wy account list` does; a failure keeps the cached answer. A folder without a login is not
eligible.

**Hub data.** When `hub.url` is set, `wy` reads `GET /v1/usage?since=<now − 26 h>` with
this machine's token (`<state>/token`, `wy connect`; tokens.md 3): per seat its ids, label,
limit windows, samples since then (with their source and `resets_at`), live sessions, the
leases other machines recorded in the last 7 days and this machine's own leases of the last 5
hours, each with what the hub measured of its session (below), and this machine's places and
signed-out folders. Without a machine token, or when the hub answers 404 (a hub older than
this `wy`) and `<state>/board/token.json` holds a read token (`wy board connect`), it reads
`GET /v1/accounts` and, for each seat a local folder matches,
`GET /v1/accounts/{seat}/windows` with `since` = now − 26 h, `samples=true` and the limit keys
of that seat (no leases of other machines then). A 404 with no board token is "hub not read:
the hub has no /v1/usage (it is older than this wy); upgrade the hub to give this machine hub
data", and local data is used (below): **upgrade the hub before the machines** to give them
hub data. All hub reads share a 5-second budget. The hub is never waited for beyond it, and
nothing the command writes goes to the hub directly.

**Matching folders to seats** is by login, never by label (a seat labelled `claude-1` on the
hub may be `claude` on this machine): (1) the seat with the folder's account and org uuids;
(2) else the seat whose `places` list this folder on this machine (`machine.json` id); (3) else
the only seat with the folder's email (two seats with that email: no match; `/v1/usage`
carries no email, so this step applies to the board-token read only). A folder the hub
lists in any seat's `signed_out` for this machine is not eligible, unless its matched seat
lists it as a place again. Everything per account (state, leases, burst caps, the round
robin) is keyed by the seat's account and org uuids (`acct:<account>/<org>`), never by label;
two folders logged in to one seat share it. `lanes` ranks such an account once, as the first of
its folders in label order that passes the rules every strategy applies; the others are listed
as "same account as <label> (<folder>), ranked once".

**Local data.** Every `limit_sample` in the outbox from the last 7 days (record.md 4.2; the
outbox keeps sent records for `outbox.keep_days`) is read too. For `five_hour` and
`seven_day`, a local reading of the same seat (account and org, else the same folder) newer
than the hub's replaces it, and local readings newer than the hub's newest sample are added
to the history. Without a hub, or when the hub cannot be read, local readings are all there
is: the output says `local data` and, in `--explain`, that other machines' use is missing
(their leases then come from the kept copy of the last hub read, Leases and the ledger).
Live sessions and claude.ai (web) readings are known only from the hub. The `lanes` strategy
also reads the outbox's `token_rows` and `hook_event` records (record.md 4.1, 4.4) of the same
7 days, in the same pass: the token meter below. Token rows are merged per `key` over their
streaming snapshots (the largest value of each counter), as the hub does.

**Strategies** (`--strategy`, default `next.strategy`): `lanes` (the default) and the
projection strategies `smart`, `expiring`, `most-room`, `spread`, `fill` and `sticky`
(below). They share the folders, logins, hub and local data above, the per-account settings,
the ledger and the output; `lanes` decides eligibility and order by its own rules.

#### Strategy `lanes`

Built for launching agent sessions ("lanes") in bursts. Each account is **aggressive** (its
room is there to be used up) or **protected** (kept for its owner: lanes may use only what
its weekly pace and a 5-hour floor leave over; it gets a lane only when no aggressive account
can take it under the hard limits, *No stalling*). It
decides from the current state only (the newest readings, the token meter since them and the
live leases), never from a forecast of past use.

*Kind.* Each account is protected, aggressive or off by its **mode**, a setting, never derived
from a label. In plain words (the one wording every surface uses: `internal/picker`
`ModeWords`, the board's `components/pick-mode.js`, the phone's `lib/format/pickMode.ts`):

- **Protected**: "Kept for you. Used last: new sessions go here only when no aggressive account can take them, or for room that would expire unused anyway." For the account you use yourself (in Chrome, the app, or
  interactively in a terminal).
- **Aggressive**: "Use it up. Sessions go here until 98% of the week or 5 hours." For
  accounts that only run agents.
- **Off**: "Never picked and nothing is moved into it; sessions already there stay." Stricter
  than protected: `wy account next` never places a lane there (not with `--urgent`, not when it
  is the only account with room: then no account is picked), the switch never moves a session
  into it, a drain is ignored, and the sessions already on it stay where they are (the switch
  does not move them off either, even over its caps). Its usage is still read and shown.
  `--explain` says "off (owner): never picked and nothing is moved into it; sessions already
  there stay", `--plan` "off (owner): never picked, nothing moves in or out", the board "off
  (owner)". On the hub it is `seat.pick_off` (schema v25), `mode: "off"` in the API; locally
  `next.accounts.<key>.mode = "off"` (`wy account mode <label> off`).
- **Not chosen**: "Not chosen yet; treated as aggressive." (the behaviour before modes were
  shared, so nothing changes for an account nobody chose for).

Where the mode comes from, the first that is set wins, per machine:

1. **This machine's `next.accounts.<key>.mode`** (config.toml, below; `mode_from` `local`):
   `protected` and `conservative` are protected, `aggressive` and `normal` aggressive, `off`
   off (`wy account mode <label> aggressive|protected|off|none` sets or deletes it). It
   keeps working as before and overrides the hub on this machine; `--explain` says "set on
   this machine" and, when the hub's setting differs, names it.
2. **The seat's hub setting** (`seat.pick_mode`, `Seat.mode` and `UsageSeat.mode` in
   api.yaml, read with `GET /v1/usage`; `mode_from` `hub`): one setting per account, the
   same on every machine. The owner sets it on the board or the phone (pwa.md 3.0a,
   `PATCH /v1/accounts/{seat}` `mode`), or `wy setup` / `wy account add` propose it (below).
   Each hub read keeps the seats' modes in `<state>/next/modes.json` (0600, by account, not
   with `--no-reserve`); when the hub cannot be read the kept mode applies, with no age
   limit (it is a setting), still `mode_from` `hub` and "(hub, cached)" in `--explain`.
3. **This machine's proposal** `slots.modes.<folder>` (`mode_from` `proposed`): what setup
   chose on a machine with a hub, sent to the hub in `slot_info` (record.md 4.7) and used
   here until the hub has a mode for the seat. The hub takes a proposal only while nobody
   has chosen for the seat (no mode, and the owner has not set Not chosen), so the first
   machine's answer fills it and only the owner changes it after.
4. **None** (`mode_from` `none`): not chosen, aggressive. `--explain` shows "not chosen yet;
   treated as aggressive" for it, and the board and the phone show it highlighted with the
   choice one click away.

Each verdict (record.md 4.11) carries `mode_from`, so the board can say when a machine's
local setting differs from the hub's. Moving an existing local setting to the hub is the
owner's choice on the board; `wy` never rewrites config.toml for it.

*Choosing in `wy setup` and `wy account add`.* Interactive runs ask once per account that has
no mode yet (no local key, no hub setting, no proposal), after the labels: "How should `wy
account next` use claude-3 (me@example.com)?" with the three answers **p** protected, **a**
aggressive and **s** skip, each with its one-line definition (skip, the default, leaves it
not chosen). A non-interactive run (`--yes`, no terminal) asks nothing. The answer goes:

- with a hub connected (`hub.url` and a machine token): to `slots.modes.<folder>` in
  config.toml, and a `slot_info` record with `mode` is queued for a logged-in folder (a
  folder that logs in later sends it with its next `slot_info`, on `SessionStart`);
- without a hub: to `next.accounts.<email>.mode` (the email of the folder's login, else its
  label).

`wy account add` asks the same for the new folder (it has no login yet: the proposal waits in
`slots.modes` for its first `slot_info`; without a hub it is keyed by the new label).

*Parameters.* Fixed, except the three marked *calibrated*: each account measures those from its
own readings and token use and moves from the value shown (its fallback) to the measured one
(Calibration, below).

| Parameter | Value | Meaning |
| --- | --- | --- |
| aggressive limit | 98 | An aggressive account takes no new lane at weekly or 5-hour ≥ this (`cap_7d`, `cap_5h` override). |
| protected target `T` | 85 | Weekly level a protected account must stay under, lanes included (`cap_7d` overrides). |
| head start | 10 | Points the pace line allows above the even share of the week. |
| protected 5-hour floor | 45 | A protected account takes no new lane at 5-hour ≥ this: its 80 % 5-hour cap less one lane's 35-point budget (`cap_5h` overrides). |
| burst cap | 6 aggressive, 2 protected | New leases one account may get in any 60 minutes. |
| lane charge `c` | 8 | *Calibrated.* 5-hour points a new lane is charged for its first 5 hours: the lane price `c₀` (fallback 4 points) times a safety factor of 2, one price for every model. Its expected full cost `cf` is the same. |
| ratio `ρ` | 7.2 | *Calibrated.* 5-hour points per weekly point. |
| meter factor `f` | 1.4 | *Calibrated.* 5-hour points per million weighted token units. |
| lease life | 5 h | How long a launched lease charges its account. |
| settle | 10 min | A reading this long after a lease shows the lane's start. |
| stale guard | 60 min | Age of the newest reading after which an account that ran is stale. |
| stale lease | 6 h | Age after which an account with a lease since its reading is stale. |
| unlaunched | 10 min | A lease whose session never started is dropped after this. |
| drain floor | 24 h | An aggressive account with room left drains within this many hours of its weekly reset. |
| drain slack | 2 (protected 1.5, no floor) | ... or when the hours to its reset are at most this times `need` (Drain). |
| drain minimum room | 0.5 | Weekly points of room under which an account never drains. |
| drain margin `M` | 1 (2 stale or hub down) | Weekly points a drain keeps under the limit: it aims at 97. |
| drain 5-hour margin | 2 | 5-hour points a drain's 5-hour capacity keeps under the limit: it aims at 96 (many small lanes together cost more than their mean now and then). |
| guard | 10 min | `finish_by` = the weekly reset − guard; no drain closer to the reset than this. |
| at risk | slack 4 | An aggressive account not draining whose reset is within 4 × `need` goes before the other aggressive ones (ordering only). |
| lane cost `E`, mean, tail | 8 | *Measured.* The p75, the mean and the p95 (`E95`, the tail; `E` when unmeasured) of a finished lane's 5-hour points (Drain, Lane statistics). |
| lane length | 15 min | *Measured.* The median lane length. |
| burn window | 15 min | A running lane's burn rate is measured over this. |
| fresh soon | 15 min | A 5-hour window resetting this soon still bounds what lanes burn before it resets, but the 5-hour backstop does not apply to it. |
| 5-hour backstop | 95 | No new drain lane at a measured 5-hour level at or over this, unless the window resets within fresh soon. |
| spill max | 2 | Lanes per account that may run past its weekly reset. |
| landing guard | 99 | No aggressive lane when `w + E95/ρ` is over this. |
| short wait | 15 min | Rather than use a protected account, wait for an aggressive one whose 5-hour window frees this soon (only when no account can take the lane under the hard limits: No stalling). |
| lane classes | short 7 min / 1.2, medium 48 min / 3.2, long 3 h / 14 | Wall time and 5-hour points of a lane of each `--expect` class (Estimates; from the worker's sessions, below). |
| class cut points | 25 min, 2 h | A wall time up to 25 minutes is short, up to 2 hours medium, longer long. |
| units per token | 0.19 | Weighted units per raw token (input + output + cache), for `--expect-tokens`. |
| hub-down factor | 0.5 | Drain capacities times this when the hub could not be read. |
| web gap | 15 min | Age of the newest reading after which claude.ai use the meter cannot see is added. |

`cap_5h`, `cap_7d` and `reserve_pct` (taken off both) are the only per-account caps `lanes`
reads; `cap_model`, `priority`, `weight` and `--model`'s model windows are not used by it
(`--model` is recorded on the lease).

*Readings.* For `five_hour` and `seven_day` separately, from every reading of the seat (hub
samples of every machine and source, and this machine's outbox): a reading whose `used_pct`
is not a number from 0 to 100, or whose time is before 2020 or more than a day ahead, is
dropped; a time after now counts as now. The reading used is the newest of those whose
`resets_at` is within 5 minutes of the newest `resets_at` seen (store `ResetTolerance`): a
session that sat idle through a reset keeps sending its old window's value with the old
`resets_at`, and must not undo the reset. An idle reading (0 % with no `resets_at`) newer than
it replaces it: the window has not started. When no reading has a `resets_at`, the newest
reading is used. Within one window the level never falls: a reading more than 1 point under
the reading used before it (same window, oldest first) is held, and skipped unless the next
reading of that window is also more than 1 point under it (a usage reset, confirmed: that next
reading is used), as in Calibration, Used readings. A session can send an old level with the
current `resets_at` (live: claude-7's week read 12 % between readings of 26 %). The account's
reading time `ts` is that of its `five_hour` reading, else of its `seven_day` reading; an
account with neither has no reading time.

*Effective state*, per account, at `now`:

1. *Base.* `w` = the weekly reading, `h` = the 5-hour reading. A weekly window whose
   `resets_at` has passed gives `w = 0` and its reset moves on by whole weeks; an unknown
   weekly reset counts as a week away. A 5-hour window whose `resets_at` has passed, or a
   5-hour reading over 5 hours old, gives `h = 0`; no 5-hour reading gives `h = 0`. No weekly
   reading leaves the weekly level **unknown**.
2. *Token meter.* Weighted units `u = input + 5 × output + 1.25 × cache_5m + 2 × cache_1h +
   0.1 × cache_read` of the token rows of the sessions of that seat: this machine's token rows
   (outbox), except that a leased session whose burned units come from the hub (3) is counted
   from the hub's per-minute units of it (`burn`) instead of this machine's rows of the lease's
   life. A row's seat is that of the session's lease begun by the row's time (a session moved
   from A to B counts its rows before the move to A: counting them all to its last lease put B's
   5-hour level at 100% right after moves in while its reading was ~60%); a row from before the
   session's first lease is the login of its `slot`; an unleased session's is that of its hook
   events, else the login of the row's `slot`. Other machines' sessions without a recent lease are not in the meter:
   only readings show their use. `m5` = units since the later of `ts` and the start of the current 5-hour
   window: while the reading's window is open that is `resets_at − 5 h`; when it has ended,
   the next window starts at the first token after its reset (and so on, 5 hours each), and
   the reading's `h` no longer counts. `m7` = units since the later of the weekly reading's
   time and the last weekly reset. `h += f × m5 + g5`, `w += f × m7 / ρ + g7`, with the
   account's calibrated `f` and `ρ` and its meter margins `g5`, `g7` (Calibration).
3. *Leases* (the ledger, below, and other machines' leases from the hub). A lease's
   *burned* units are the weighted units its session used since the pick, taken from one
   source, the larger of: *local*, this machine's token rows of the session (another
   machine's lease has a local session when its `session_hash` is that of a session in this
   machine's outbox); and *hub*, the per-minute units the hub measured for a lease younger than
   5 hours (`burn`, from the token rows of every machine). With neither it is *none* (0), and so is a
   lease without a session id. `b = f × burned`. The meter (2) counts that session from the
   same source, so each token of a leased session counts once: a token before the reading is
   in the reading, one after it is in the meter, and the lease adds only the part of its
   expected cost not burned yet. A lease without its own `c5`/`cf` is charged the account's
   current `c`. A lease counts as launched when the ledger has its `launched` line, this
   machine's hook events or token rows show its session since the pick, or the hub has its
   session (`launched`; for this machine's leases from `own_leases`, matched by lease id). Its
   session has *ended* when the ledger has its `ended` line, this machine's hook events end it
   (`SessionEnd`), or the hub says so (`ended`: `SessionEnd`, or gone from the session list).
   A lease is *active* while it is younger than 5 hours and its session has not ended. Another
   machine's lease whose burned units nothing can measure (no `burn`, because the hub could
   not be read and the kept copy below is used, or the hub is older than this `wy`; and no
   local session) is also *settled*, and no longer active, once the account has a reading at
   least 10 minutes after the pick: that reading includes the lane's use so far, and charging
   it as well would count it twice. An active lease adds `max(0, c − b)` to `h`, the same
   divided by `ρ` to `w`, and `max(0, cf − b)` to the reserve. A lease that is no longer active
   and whose session neither source ever saw keeps its weekly charge `max(0, c − b) / ρ` until
   there is a reading at least 10 minutes after it or a weekly reset after it (so a settled
   lease keeps none). A session that never sends a token stays charged in full until its lease
   ends or settles: a deliberate double count on the safe side.
4. *Unexplained use* (only for an account with claude.ai readings, source `web`, in the hub's
   history). The rate `r` is measured on the web `five_hour` readings of the current window in
   the last 60 minutes of web readings. *Clean* intervals come first: two consecutive web
   readings with no token row of the seat in this machine's meter and no status-line reading
   between them (no Claude Code session ran, so the whole rise is use the meter cannot see);
   when they add up to at least 10 minutes, `r` = their summed rise over their summed time,
   per hour. Otherwise `r` = the rise from the oldest to the newest of those readings (at least
   10 minutes apart) less the meter's points over the same interval, per hour, at least 0. It
   is kept for 5 hours after the last web reading. When the newest reading (any source) is older than 15 minutes, `h +=
   r ×` its age (at most 5 h) and `w` gets that divided by `ρ`. A **protected** account with
   web readings whose newest reading is older than 15 minutes and that has no rate is stale.
5. *Stale.* The newest reading is older than 60 minutes and the account ran since it (a live
   session: the hub's `live_sessions`, or a hook event on this machine in the last 10 minutes
   of a session of the seat that has not ended; a token row since the reading; or a lease
   since it); or it is older than 6 hours with a lease since it; or there is no reading time.
6. `w` and `h` are kept within 0–100. The *reserve level* is `w` before the active leases'
   5-hour charges plus the active leases' reserve divided by `ρ`.

*What leases and the meter cannot see.* A leased session's tokens are counted twice only while
they are in a reading but not yet on the hub (another machine's flush delay), and a minute of
`burn` that straddles the reading counts as after it. A settled lease stops charging the rest
of its expected cost, which only later readings show. Other machines' sessions without a live
lease, and a leased session's tokens after its lease's 5 hours, are seen only through readings
(`claude -p` sends none). With the hub down, the kept copy has no lease taken after the last
hub read, and a lease it lists as not launched is dropped 10 minutes after its pick.

*Eligibility*, after the rules every strategy applies first (logged in on this machine, not
signed out on the hub, not `exclude`d, inside its `hours` and `days`):

- **aggressive**: not when `w ≥ 98` or `h ≥ 98`, or when one costly lane alone would take it
  over the landing guard (`w + E95/ρ > 99`), or, with a 5-hour window open, when the measured `h`
  plus the past use and what its running lanes will still burn in the window (`F(t5)`, *Drain*,
  except that a lane nothing measures is taken to run to the median length, without the
  `rem ≥ age` rule) plus one costly lane (`min(E95, r × t5)`) would pass 98. An unknown weekly level does not exclude it: it is
  ranked after every known aggressive account and before every protected one. A stale aggressive
  account keeps its estimate (accounts used only by `claude -p` never send readings).
- **protected**: not when `w ≥ min(T, T × (1 − hours_to_reset / 168) + 10)` (the pace line),
  `h ≥ 45`, the reserve level `≥ T` (the running lanes' expected cost would take it over its
  target), it is stale, or its weekly level is unknown (no valid reading).
- **both**: not when it got burst-cap leases or more in the last 60 minutes.
- **draining** accounts (Drain, below) follow the drain rules instead: no burst cap, no lease
  charges, no pace line or target; their drain capacity, the 5-hour backstop, the hard limits and
  the landing guard on their measured levels.

A protected account outside drain gets a lane only when no aggressive account can take it under
the hard limits (*Order*, *No stalling*); with no account that can, nothing is picked (exit 4).

*Order* (`drain`, owner 2026-11-24). Every eligible account is in exactly one class, and the
classes go first to last:

1. **Aggressive drain** accounts with capacity: in a batch, those not yet picked in it first (every
   draining account gets a lane before any gets a second); then the largest `deficit` (`needed − running`,
   `needed = ⌈R5 / (r × (H − guard))⌉` the lanes that must run without pause to use the room);
   then the earliest `finish_by`; then the newest lease oldest first; then label. So several
   draining accounts share a batch in proportion to their room, and one draining account takes
   every lane its capacity allows.
2. **Protected drain** accounts with capacity (owner: only when even full speed cannot use their
   room), in the same order.
3. **At risk**: aggressive accounts not draining with `H ≤ 4 × need`, the lowest `H / need` first,
   then turns.
4. **Aggressive**: turns (the newest lease oldest first, never leased first), then fewer leases in
   the burst window (so a batch alternates), then the earliest weekly reset, then label.
5. **Unknown**: aggressive accounts with an unknown weekly level (owner: before protected).
6. **Protected**, only when no aggressive account can take the lane: every aggressive account is
   at a hard limit, behind the landing guard, unreadable or excluded (*No stalling*: one held only
   by its burst cap or its drain capacity takes the lane first, marked over).

A draining account with no capacity is skipped and does not fall into a later class (except as an
over pick, *No stalling*).

*No stalling* (owner change 2, 2026-11-24). A lane never waits while some account can take it
under the **hard limits**: weekly 98, 5-hour 98 (with the running lanes' forward burn and one
more lane), the 5-hour backstop 95 for drain lanes, the landing guard 99, and a readable state (not
stale, weekly level known, for a protected account). The **soft limits** only set the order:
the burst cap, the protected gate (the 5-hour floor 45 and the pace line, the reserve target) and
drain capacity. When the order above finds no eligible account, the lane goes, in this order, to:

1. an aggressive account held only by soft limits (its burst cap, or a draining account full by
   its capacity whose next lane stays under the hard limits: `w + (F + N1)/ρ ≤ 98`, the 5-hour
   window holds it, `h < 95`), in table order;
2. an eligible protected account;
3. a protected account held only by its gate (under the hard limits and the backstop).

Such a pick is **over** (`"over": true`, "over (no account free under its soft limits)" in the
pick line); it never counts against drain capacity or the spill limit. `--urgent` still comes
first for a burst-capped aggressive account. Exit 4 only when every account is at a hard limit or
unreadable. Under no stalling a protected account's 5-hour check is the forward check against 98
(not its floor) once it is past its gate.

*Estimates* (`--expect`, owner change 2). The agent says how big each new lane will be: a class
(`short`, `medium`, `long`), a wall time (`--expect 90m` or `--expect-minutes 90`; its class by
the cut points) and/or its tokens (`--expect-tokens N`). The estimate is the class's benchmark
(table above), the wall time given (the class's cost scaled to it) and, with tokens, the cost
`N × 0.19 × f`. No estimate: the measured median lane (length `L`, cost `Ē`; class `measured`), or
medium without lane statistics. The cost is never below `Ē` (*cost floor*: agents underestimate;
a lane floored up keeps its stated burn rate and runs longer). The estimate replaces `N1`, `N1₅`
and the lane rate `r` for the new lane in *Capacity*, the landing guard (when given and over
`E95`) and the 5-hour forward check, and the pick's lease carries its wall time and rate, so the
next picks of the batch and later calls see the lane as it was announced (a lane measured
burning uses its measured rate).

*Runway.* A lane whose estimated wall time is longer than an account's hours to its weekly reset
less the guard is **late** for it: a late aggressive drain account goes after the aggressive
accounts that are not late (before unknown ones), a late at-risk, aggressive or unknown account
after every aggressive and unknown one, a late protected drain account with the protected ones.
So long lanes go to accounts with a week ahead of them and short lanes drain the room that
expires. A drain lane is a spill lane when its estimated wall time exceeds the hours to the
reset.

*Benchmarks.* From the worker's ledger and session logs (read only, 2026-11-24; 600 distinct
sessions, `f` 1.4): wall time p50 25 min, p90 121 min; 5-hour points p50 1.4, p75 3.1, p95 11.1,
mean 3.3 (opus 5.2, sonnet 1.3). By class at the cut points: short (≤ 25 min) median 6.4 min,
mean 1.2 points (p95 3.8); medium median 48 min, mean 3.2 (p95 8.6); long (> 2 h) median 3 h,
mean 13.7 (p95 43). Weighted units per raw token: median 0.19. The ledger's own lanes (15): median
18 min, mean 5.7 points. Measured lane statistics replace the class benchmarks when the ledger
has them.

`w` and `h` here are the levels with the lease charges (Effective state); a draining account is
judged on its *measured* levels instead. `score` is shown but does not order: `(1 − h/100) ×
(100 − w) / max(hours, 0.5)` for aggressive, `(1 − h/100) × 0.85 × (T − w) / max(hours, 24)` for
protected.

Two older orders are kept in `internal/picker` for comparison only: the reference `rr_exp`
(`ReferenceLaneParams`, the golden cases: an expiring turn for aggressive accounts resetting
within 48 hours while `h < 30`, then one round robin over every other eligible account, protected
ones included), which on 2026-11-23 sent lanes to protected accounts while an aggressive account
about to reset had room; and the draft `fill_exp` (`DraftLaneParams`: last-day and at-risk groups
with the fixed lease charges), which the replay and the simulator compare against.

*Drain.* Weekly room that would expire unused before the weekly reset is used first, up to what
it can absorb. All levels are *measured*: the reading, the token meter with its margins and the
unexplained use, never the fixed lease charges (so lanes already started cannot push a draining
account out of the drain; they only limit how many more it takes).

- *Quantities.* `ρ` the account's ratio; `M` the drain margin (1 weekly point, 2 when stale or the
  hub could not be read); `W` and `S` the account's hard weekly and 5-hour limits: 98, no higher
  than Smart Account Switch's caps while it is on (8.6: aggressive 98 and 95, protected 80 and
  80 by default), less the pin reserve (8.6, Pinned sessions); `R5 = (W − M − w) × ρ`, the room
  this week in 5-hour points; `A0 = max(0, S − h)` the room in the current 5-hour window; `t5`
  the hours to the 5-hour reset (5 when no window is open); `H` the hours to the weekly reset;
  `need` the hours to use `R5` at full 5-hour speed (`5 × R5 / S` when `R5 ≤ A0`, else `t5 + 5
  × (R5 − A0) / S`), for every account. An aggressive account's weekly and 5-hour limits are no
  higher than `W` and `S` either, so the drain, the at-risk test (its `need` is the room to `W`
  at `S` per window) and the rank aim at the level the switch keeps (live on pre4.1: claude-3
  at 82 % with 2 pins had 6 points of room to its cap 88, not 16 to 98).
  For a protected account's capacity `A0` is `max(0, 45 − h)`: its 5-hour floor stays.
- *Entry* (all of): the weekly level and its `resets_at` are known from a reading; `98 − w > 0.5`;
  `H > guard` (10 minutes); and `H ≤ max(24, S × need)` for an aggressive account (`account.drain_slack` S, default 2), `H ≤ P × need`
  for a protected one that is not stale (`account.drain_slack_protected` P, default 1.5; it was 1
  until owner 2026-11-26: "make it 1.5x times instead of 1x"; only room that would be wasted
  even at 1.5 × full speed). `--explain` shows the slack used ("entry 1.5 × need"). It leaves the drain as soon as one fails (no state is kept between calls). A protected
  account in drain may use its room up to `W − M` (not its target or pace line), with its 5-hour
  floor kept.
- *Owner drain* (owner 2026-11-25). `wy account drain <label> [--until <time>]` sets
  `next.accounts.<label>.drain_until` (RFC 3339; `--until` takes `15:04` today, `2006-01-02 15:04`
  or RFC 3339; none is 23:59 today) in this machine's config; `wy account drain <label> off`
  deletes it. With a hub it also writes the seat's hub drain (`PUT /v1/accounts/{seat}/drain`,
  machine token, scope `write`; `seat.drain_until`, schema v25), which every machine reads from
  `GET /v1/usage` (`drain_until`), so the drain is the same everywhere; the board and the phone set
  it too (`PATCH /v1/accounts/{seat}` `drain_until`, at most 8 days ahead). `--local` sets only
  this machine's key; when the hub cannot be reached the command says "on this machine only".
  This machine's key overrides the hub's on this machine. An account that is off cannot drain
  (the hub answers 400; locally the drain is ignored). The hub ends its drain at `drain_until` or
  when the seat's weekly peak reaches the drain aim as it will be at `drain_until` (*Drain aim*
  below, at most 99 %), and pushes `drain_started` / `drain_ended` on the
  Drain channel (push.md 3.9). While `until` is in the future the account drains as
  an **aggressive** drain account (a protected one too) whatever its hours to the weekly reset:
  `H = min(H, until − now + guard)`, so `finish_by = until`, with the same drain aims
  (*Drain aim* below) and capacities. It ends by itself at `until`, or once its weekly level
  reaches the drain aim as it will be at `until`, less the pin reserve (the account is then as
  configured again).
- *Drain aim* (owner 2026-11-25: "drain slowly till 95-96%, and as time comes reach 98-100%").
  The far aim (96) and the switch's weekly cap ceiling (99) are `account.drain_aim_7d` and
  `account.drain_cap_max_7d`, each up to 100: an owner who drains an account to fill its week
  now (a manual full reset follows) sets both to 100, and the aim is then 100 at every hour to
  the reset (still less the pin reserve and the 1-point drain margin, so lanes stop at about
  99 projected); the ramp rises from `drain_aim_7d` to 100 over the last 6 hours, and an aim at
  or over the ramp's value is used as it is. The switch's weekly cap for the draining account
  follows the same two keys, so it does not move sessions off below the aim.
  A draining account (automatic or the owner's) aims its week at

      aim7 = 96 + 4 × (1 − h/6)    for h < 6 hours to its weekly reset, else 96

  less the pin reserve, never over 100 less the reserve and the 1-point drain margin (so at
  most 99): far from the reset a point kept costs nothing (it can still be used later) and a
  burst to the limit stops lanes for days; in the last 6 hours every point not used is lost at
  the reset and a stop lasts only until it. Its weekly hard limit for new lanes is
  max(98, the aim plus the margin) less the reserve. In a draining 5-hour window the ceiling is
  98, rising to 100 over the window's last 2 hours, and the 5-hour drain aim (and the backstop)
  that less `DrainMargin5h` 2 (96 rising to 98); one more lane must still fit under the
  ceiling, and the landing guard (`LandMax` 99) still refuses a lane whose expected cost alone
  would land over 99, so a lane is not stopped by a limit before the reset. A lane estimated to
  outlast the week goes to other accounts first (runway), so the last stretch takes short
  lanes. Smart Account Switch's caps for a draining account ramp the same way (5h: the 5-hour
  aim; 7d: the weekly aim, at most `drain_cap_max_7d`, 99). Those caps are the aim itself: the switch's 5-point margin
  (`move_hysteresis`) is not kept under them, neither by the picker's switch-projection check
  nor by the switch's moves off it or onto it (a pull onto an owner drain keeps `PullMargin`);
  with it a drain could never come within 5 points of its aim (live pre15: claude-3 at 89%,
  lanes refused at a projected 91% against "cap 95% less the switch margin"). An owner's own lower weekly limit and the switch's
  protected caps are not raised. `--explain` ends the drain line with "aim 96.4% now, 98% at
  Thu 03:30" (`--json` `drain.aim_pct`, `aim_at_reset_pct`, `weekly_resets_at`). `--explain`, the table,
  `--plan` and the board show "draining (owner, until 23:59)" ("Thu 07:00" when not today).
  The board shows a drain set only on one machine for that machine's accounts, over the hub's.
- *Lane statistics.* From the leases read (this machine's ledger, 8 days; other machines' leases
  with a hub burn): `E` the p75, `Ē` the mean and `E95` the p95 of the 5-hour points (`f ×` units) of lanes that
  ended or are 5 hours old, and `L` the median length of this machine's ended lanes (launched to
  ended); with `--model`, that model's own lanes when there are at least 10. Fewer than 10:
  the fallbacks 8 points (all three) and 15 minutes. A new lane burns `r = Ē / L` points per hour.
  The **lane burn** `R` is the p75 of the lanes' 5-hour points per hour of wall time: lanes that
  ended (this machine's, 20 minutes or more) and lanes running for 30 minutes or more (their
  burn so far over their age; a younger lane's first minutes burn far above its steady rate),
  with `--model` that model's own when there are at least 10; fewer than 10: not measured.
  A lane is a picker lease's session over its whole life, across moves: from its launch to the
  end of its session's last lease (or now), the burn its session used since. Leases written by
  moves (strategy `move` or `handoff`, or `moved_from`) are not lanes, for `E`, `L` or `R`, and
  other machines' leases count for `E` only (the hub does not say which are moves). Live pre8:
  a moved session keeps its id, its lease ends at the move and a new lease starts, each lease's
  burn being its session's since; the move leases (minutes of wall time, the resume's cache
  write) and the picker leases ended at their first move made `R` 57 points/h against ~9-14,
  and every account was refused.
- *Forward burn* of the running lanes before a time `T`: `F(T) = Σ rate × min(rem, T)` over the
  account's leases that are not ended and younger than 5 hours (settled and not yet launched ones
  included), where `rate` = the units the session used in the last 15 minutes `× f` per hour when
  something measures it and it is at least 5 minutes old, else `r`; `rem = max(L − age, age, 10
  min)` (a lane that has run 3 hours is likely to run about 3 more), at least `--expect − age`,
  at most the lease's life left. A lane nothing measures (no local session, no hub burn) has
  also used `r ×` the hours since the later of its pick and the reading (an ended one at most one
  lane length of it), which neither the reading nor the meter holds: that *past* use is added to
  `F` (it replaces the fixed weekly charge kept for unseen lanes).
- *Capacity* (new lanes the room absorbs now): with `N1 = min(Ē, r × (H − guard))`, weekly `Cw =
  ⌊(R5 − F(H − guard) − (E − Ē)) / N1⌋`; with `t5' = min(t5, H)` and `N1₅ = min(Ē, r × t5')`, 5-hour
  `C5 = ⌊(A0 − 2 − F(t5') − max(E − Ē, min(E95, r × t5') − N1₅)) / N1₅⌋` (2: the drain 5-hour margin; the 5-hour room also holds one costly lane), not counted when no window is open, and 0 at a measured
  `h ≥ 95` (the backstop) unless the window resets within 15 minutes, or at `h ≥ 98` always. A window
  that resets within 15 minutes still counts for its last minutes (`N1₅ = r × t5'` is small), and
  the spread kept is at most `r × t5'`; such an account is free at the window's reset. Many lanes cost about `Ē` each; the spread `E − Ē` is kept once. *End game*: `Cw = 0`,
  `R5 − F ≥ N1 / 2` and no lane running gives `Cw = 1`. With the hub down both are halved (rounded up) and this
  machine's lanes of the last 60 minutes count twice in `F` (another machine may be draining the
  same room as fast), so a batch places about half. `C = min(Cw, C5)`; then *spill*: a new lane is a spill lane when
  `H − guard < L` or `--expect` is longer, and at most 2 lanes expected to run past `finish_by` may
  be running per account; *stale*: at most 1 new drain lane per reading (none when a lease came
  after the reading); and the landing guard. With `C ≤ 0` the account takes no lane now (unless as an over pick, *No stalling*): "drain
  full: N lanes running use the room left before HH:MM" or "drain: 5h full until HH:MM".
- *Batches.* Each placed pick is a running lane of age 0 for the next pick of the batch, so `F`
  and `C` count it.
- `finish_by` = the weekly reset − 10 minutes: the drain pick tells the agent to finish or
  checkpoint by then (Flags, output).
- *Shown only*: the room usable before the reset at full speed, and the weekly points that will
  expire whatever happens (`unavoidable_waste_pct`); the projected weekly level at the reset, `w
  + F / ρ`.

*Batches.* `--n K` makes K picks one after another under the ledger lock; each adds its lease
before the next is ranked, so the burst fills the accounts evenly and stops at their limits
and burst caps. It stops at the first lane no account can take; the picks made before it are
leased and printed (exit 4).

*`--urgent`.* When no aggressive account is eligible (draining accounts have no burst cap), it
may pick an aggressive account whose **only** reason is its burst cap: the one with the lowest
`h`, then `w`, before a lane would wait or go to a protected account. It never passes a drain
capacity, a weekly or 5-hour limit or the landing guard, never picks a protected account and has
no rate limit. The lease is marked
urgent (in the ledger and on the hub) and stderr names the cap it passed.

*When an account frees up* (`free_at`, and `next_free` = the earliest): the latest of: when
the burst cap is reached, the oldest lease in the window plus 60 minutes; when `h` is over its
limit, the 5-hour reset (or 5 hours from now when unknown); a protected account at or over
`T` (or its reserve level at or over `T`), the weekly reset; a protected account over its
pace line, the time the line reaches `w`; an aggressive account at its weekly limit, the
weekly reset; an aggressive account held by its running lanes' 5-hour burn, the expected end of
its first running lane to finish, at the latest the 5-hour reset; a draining account with no
capacity, the earliest of the end its oldest running lane is expected at and (when its 5-hour
window limits it) 15 minutes before its 5-hour reset, or the reset itself when that is sooner. A stale
account or one with an unknown level frees up at no known time.

#### Calibration

Three factors of each account adjust themselves to its measured use: the meter factor `f`, the
ratio `ρ` and the lane price `c₀` (the lane charge is `c = cf = 2 × c₀`). A new account uses
the fallbacks of the Parameters table until it has about 3 days and 20 usable measurements,
then moves to its measured values. Everything is keyed by the seat (account and org uuids).

*Where it runs.* The **hub** calibrates every seat, because it has the readings and token rows
of every machine (`wy hub serve`, below), and returns the result per seat in `GET /v1/usage`
(`calibration`, api.yaml `Calibration`). `wy account next` uses the hub's factors when they are
under 7 days old. It keeps the last ones it read in `<state>/next/calibration.json` (written
after each hub read, except with `--no-reserve`, which writes nothing) and uses them, while
under 7 days old, when the hub cannot be read. Otherwise it calibrates the account
itself, by the same rules, from this machine's outbox (7 days) and ledger (8 days): source
`local`. The meter margins and the unexplained rate (Effective state 2 and 4) are always
worked out by `wy account next` at the pick, from the newest readings and this machine's meter,
because they correct the meter it uses.

*Used readings.* Per window (`five_hour`, `seven_day`), the seat's readings of every source,
oldest first. A reading is used when it is valid (as in Readings), its `resets_at` is within 5
minutes of the newest `resets_at` used so far for that window (an older one is a session that
sat idle through a reset: skipped), and it is not more than 1 point under the last used
reading of the same window. A reading more than 1 point under it is held: when the next
reading of that window is also more than 1 point under, the drop is confirmed (a server or
owner reset of the usage): that next reading is used, and no pair spans the drop; otherwise
the held reading is skipped (a session that reported an old value). A reading without a
`resets_at` (an idle window) is not used. On the hub, readings inside the **mask** of a
confirmed server or owner reset (push.md 3.9, `usage_reset`) are not used either, and no pair
spans a mask: the last used readings are forgotten (their `resets_at` kept) before the first
reading after it.

*Pairs.* A pair is two consecutive used `five_hour` readings of the same 5-hour window
(`resets_at` within 5 minutes): `d5` = the rise, `dt` = the time between them, `u` = the weighted
units of the seat's token rows after the first and up to the second. When both readings come
with a used `seven_day` reading of the same weekly window (same time and source), `d7` = its
rise. A pair whose second reading confirms a drop is not a pair: server and owner resets never
count. A pair is *clean* when both readings are claude.ai readings (source `web`), `u = 0` and
there is no status-line reading of the seat between them: no Claude Code session ran, so its
rise is use the meter cannot see.

*Meter factor `f`* (points per million units), from the pairs of the last 7 days with `u > 0`:
per 5-hour window, the window ratio `Σ d5 / Σ u` of windows with `Σ u ≥ 2` million units; the
reference `f̂` = the median of the window ratios weighted by their units. A pair with a large
unexplained rise, `d5 − f̂ × u > max(3, 2 × f̂ × u)` (claude.ai use beside the session), is left
out; measured `f` = `Σ d5 / Σ u` over the pairs left, when their `Σ u ≥ 2` million units. Usable
measurements: the pairs left.

*Ratio `ρ`*, from the pairs of the last 7 days with a `d7`: measured `ρ` = `Σ d5 / Σ d7`, when
`Σ d7 ≥ 5` points. Usable measurements: those pairs with a rise in either window.

*Lane price `c₀`*, from the leases of the last 14 days that name a session that started (the
hub has the session; locally, the ledger's `launched` line, hook events or token rows) and
are at least 5 hours old: the units of that session's token rows in the 5 hours after the
pick. The mean over them, leaving out the largest ⌊n / 100⌋, is pooled over every seat (a lane
is the same work whichever account runs it; locally, over this machine's ledger); a seat's
measured `c₀` = that mean × the seat's final `f`. Usable measurements: those leases.

*Unexplained rate* (shown only): `Σ d5 / Σ dt` of the clean pairs of the last 7 days, and their
hours.

*Blending.* For each factor, with `n` usable measurements and `d` days from the first of them to
now (both counted from the account's last restart, below): weight `α` = 0 when `n < 20` or
`d < 3`, else `min(1, min(n / 20, d / 3) / 2)`; value = fallback + `α` × (measured − fallback).
So it moves halfway once both thresholds are met and fully at twice them. A measured value
outside its plausible range (`f` 0.25 to 8 points per million units, `ρ` 3 to 15, `c₀` 0.5 to
25 points) is not used (`α` = 0, shown as implausible).

*Restarts.* An account's calibration restarts (its pairs are dropped and `n`, `d` count again
from the restart, so it goes back to its fallbacks for about 3 days):

- when the seat's account or org uuid is not the one it was calibrated for (reason
  `seat_change`);
- after **6 one-sided misses in a row** while the measured `f` is in use (`α > 0`): for a
  pair with expected rise `e = f × u ≥ 1` point (`f` the value in use), a miss is `|d5 − e| ≥
  max(2, e / 2)`, an under-read when `d5 > e` (reason `under_read`), else an over-read
  (`over_read`). A pair with `e ≥ 1` that is not a miss, or a miss on the other side, ends the
  run; other pairs neither count nor end it. The restart is at the run's first pair, so the
  run's pairs count for the new calibration.

The lanes of the seat stay in the pooled lane mean; the seat's `c₀` follows its new `f`.

*Meter margins* (the reference's online corrector, mode `ewmax`). At the pick, per account,
over its used readings of the last 26 hours (hub and this machine) and this machine's meter,
oldest first, with the account's `f` and `ρ`: for each pair of consecutive used `five_hour`
readings, `e5 = d5 − f × u` and the 5-hour factor `k5 = max(min(3, max(0, e5 / max(f × u, 2))),
0.7 × k5)`; consecutive used readings of different 5-hour windows (a reset between them) leave
`k5` as it was. The same for the `seven_day` readings: `e7 = d7 − f × u / ρ`, floor `2 / 7`,
factor `k7`, a weekly reset leaving it. Both start at 0. The margins of Effective state 2 are
`g5 = k5 × f × m5` and `g7 = k7 × f × m7 / ρ`: the meter's recent under-reading, applied to
what it has counted since the reading.

**On the hub** (`wy hub serve`, package `internal/calib` and the hub's calibration job): every
15 minutes, and 1 minute after the start, each seat's calibration is brought up to date on data
at least 30 minutes old (token rows arrive after the readings they explain). A run is
incremental and cheap (the hub is a small machine): per seat it reads only the `five_hour` and
`seven_day` samples and the token rows with times after the seat's cursor (by the
`limit_sample` key and the `token_row_seat_ts` index), and the seat's leases after its lease
cursor that are at least 5 hours 30 minutes old with their sessions' token rows of those 5
hours (`account_lease_seat_ts`, `token_row_session`); nothing is scanned per request. It keeps
per seat, in `seat_calibration` (schema.sql), a state (cursors, the last used readings, the
pairs of the last 7 days that rose or used tokens, the clean pairs' sums, the lanes of the
last 14 days, the miss run, the restart) and the computed factors, which `/v1/usage` returns
as they are. Token rows that arrive more than 30 minutes after their time miss the pairs
already folded. A seat with no row yet has `calibration: null`. Before it folds a seat, the job
watches the seat's newest readings for usage notices (push.md 3.9); while a drop to about 0
waits for its decision there, the calibration folds only readings before it, and a confirmed
reset masks its readings (above).

#### Usage relation

What the board shows in an account's Details (pwa.md 3.0a, phone.md P4) about how its 5-hour
use, its weekly use and its API cost relate, worked out by the hub's calibration job from the
same pairs as calibration (so a masked reset is left out of it too) and kept per seat in
`seat_watch` (schema.sql), for 15 days of the hub's time zone (push.md 2). `GET
/v1/accounts/{seat}/relation` (api.yaml `UsageRelation`) returns it, with the seat's
calibration factors and the picker's latest verdict (below); nothing is recomputed per request
but the weekly medians over those 15 stored days.

- **Daily ratio.** Per day, from the pairs whose second reading is on that day and that have a
  `d7`: `Σ d5 / Σ d7` when `Σ d7 ≥ 2` points (else none), with the number of pairs. Each day is
  labelled with its **most-used model**: the model with the most weighted units in the seat's
  token rows of that day (every row, not only those inside pairs), as its display label
  (`Opus 4.6`: dated ids of one model count together; analytics.md 4).
- **Capacity index.** The 5-hour capacity in API dollars per 5-hour point. Per day and model,
  over the day's pairs with `u > 0` and `d5 ≥ 0` in which that model has at least 80 % of the
  pair's API dollars (`api_usd` at list prices, analytics.md 1; a model with no price is left
  out): `Σ dollars / Σ d5` (each pair's dollars of every priced model), when `Σ d5 ≥ 5` points
  (else none).
- **Weeks.** *This week* is the 7 days ending today, *last week* the 7 before. Per model, a
  week's median is the median of its daily indexes, when it has at least 3. The index of a day
  **normalised** to last week is the day's index / last week's median; `change` = this week's
  median / last week's.
- **Claude-Code-heavy.** A seat is Claude-Code-heavy in a week when its pairs of that week rose
  at least 20 five-hour points in all and at least 80 % of that rise is in pairs with `u > 0`
  (its Claude Code sessions sent tokens; the rest is claude.ai, the app, or another machine's
  use without token rows). Only a heavy seat's index is a fair measure of the limit: the
  board shows the index of a seat that is not heavy muted, and it never counts toward
  `limit_change` (push.md 3.9).

The job folds the pairs it makes and the seat's token rows after its cursor (their model and
counters are read in the same index range as their units), and prices them with the analytics
price table.

**Verdicts to the hub.** Each run of `wy account next` records what it decided about every
account it ranked, so the board can show why an account is or is not being picked:
one `account_verdicts` record (record.md 4.11) per run, appended to the outbox with the same
background flush as leases (never waited for), with per account (account and org uuids) its
kind, whether it is eligible, the reason when it is not and its skip level, `free_at`, its rank
among the eligible (1 = next), whether it is expiring, its effective weekly and 5-hour levels and
its burst count and cap, plus the run's strategy and `next_free`; only accounts whose login is
known. To keep the hub's load small the record is skipped when it says the same as the last one
this machine wrote (levels rounded to whole points, times to the minute) less than 15 minutes
ago; `<state>/next/verdict.json` (mode 0600) keeps that last one's digest and time. `--no-reserve`
writes no record, nor does a machine with no hub set up. The hub keeps the newest verdict per seat (`seat_verdict`), whichever machine
sent it, and `/relation` returns it with its time and machine.

#### Leases and the ledger

A lease is one pick. The **ledger** is the record of them, and the marker of what this
machine launched: day files `<state>/next/leases/<YYYY-MM-DD>.jsonl` (UTC day of the line,
mode 0600, folder 0700), **append-only**, one JSON object per line, each line written with a
single write and flushed to disk (`fsync`, and the folder when a day file is created) before
the next. Lines:

- `{"v": 1, "op": "lease", "id", "at", "key", "account", "org", "folder", "label", "kind",
  "session_id", "c5", "cf", "model", "strategy", "urgent", "batch", "drain", "finish_by", "moved_from", "parent",
  "expect", "expect_min", "expect_end"}`: `id` a ULID, `at` the
  pick time (RFC 3339 UTC), `key` the seat key, `kind` `aggressive` or `protected`,
  `session_id` from `--session-id` (absent without it), `c5` and `cf` the lane charge and full
  cost in 5-hour points, `model` from `--model` (absent without it), `strategy` the strategy
  that picked, `urgent` true when `--urgent` passed a burst cap, `batch` the id shared by the
  picks of one call, `drain` true and `finish_by` (RFC 3339 UTC) for a drain pick (both absent
  otherwise). A move (8.6) writes a `lease` line with `strategy` `move` and `moved_from`, the
  label of the account the session left, and the old lease's `parent` and `expect*`. `parent`:
  the session that asked for the lease (`--parent`, 8.5 flags). `expect` (`short`, `medium`,
  `long`), `expect_min` and `expect_end` (RFC 3339 UTC): the lane's length as the
  switch-projection check counted it (a medium lane's without `--expect`; absent with
  estimates off); the switch projects the lane's burn to `expect_end` (8.6).
- `{"v": 1, "op": "bind", "id", "at", "session_id"}`: the lease `id` names session `session_id`
  from now on (a `claude --bg` lane, whose id is known only after its launch).
- `{"v": 1, "op": "launched", "at", "session_id"}`: written by `wy hook` on `SessionStart`.
- `{"v": 1, "op": "ended", "at", "session_id"}`: written by `wy hook` on `SessionEnd`.

The files of the last 8 days are read. A line that cannot be read (bad JSON, unknown `op`,
no time, a lease without `key`, a `launched`, `ended` or `bind` line without `session_id`, a `bind` without `id`, a `v`
other than 1) is skipped and counted (`skipped_ledger_lines`). A crash in the middle of a write
can leave a torn last line: the next write to that file starts a new line first, so only the
torn line is lost. When a pick is
written, day files older than 8 days are deleted. Ranking reads the ledger, ranks and appends
the new leases while holding the advisory lock `<state>/next/leases.lock`, so pickers on the
machine see each other's leases. A session id already leased (by a lease that has not been
dropped) or given twice is refused before anything is written (exit 3).

**`wy hook`** on `SessionStart` and `SessionEnd`: when the payload's `session_id` has a lease
line in the day files of today and yesterday (UTC) and no line of the same `op` yet, it
appends a `launched` or `ended` line. It takes no lock (a line is one write, and readers skip a
torn last line), stays inside the hook's time budget and never fails the hook. `SessionStart`
of a resumed or cleared session finds `launched` already there and writes nothing.

A lease **with no `launched` line** older than 10 minutes is dropped: its launch never
happened (a crashed launcher, or a pick without `--session-id`, which therefore counts for 10
minutes like a short reservation); its session id may be leased again. A lease ends when its
session ends (`ended`) or 5 hours after the pick; its weekly charge may stay longer (above).

**Mirror to the hub.** Every lease written for a folder whose login (account and org) is
known is also appended to the outbox as an `account_lease` record (record.md 4.10) and, when
`hub.url` is set, a background `wy flush` is started (debounced like the hook's);
`wy account next` never waits for it, and works the same with the hub down.
The hub returns per seat in `/v1/usage` the leases other machines recorded in the last 7 days
(`leases`) and this machine's own leases of the last 5 hours (`own_leases`), each (api.yaml
`UsageLease`) with its lease id, time, urgent, charges, whether its session started and
whether it ended, whether the hub has a token row of the session since the pick (`seen`), a
hash of the session id (`session_hash`, the SHA-256 of the id in hex: it names the session
only to a machine that already has it) and, for a *recent* lease (younger than 5 hours, with a
session id, whether or not its session ended), the session's weighted token units from the
pick to 5 hours after it, per minute (`burn`: pairs of the seconds after the pick of the minute's newest
row, rounded up, and the minute's units) and their sum (`burned_units`); no session id and no
machine. To stay cheap, the hub measures `burn` only for recent leases, at most 200 per seat
(the newest; the others get `burn: null`), each with one read of that session's token rows by
the `token_row_session` index, cached for the request; an older lease costs one existence
probe (`seen`). `lanes` counts other machines' leases like its own (above), so two machines
launching at once spread over the accounts once their records arrive, and nothing is counted
both in a reading and as a lease's charge.

**Kept copy.** After every hub read (not with `--no-reserve`), the other machines' leases
that can still charge (younger than 5 hours, or never seen) are written, without `burn`, to
`<state>/next/hub_leases.json` (mode 0600) by account key, with the read time. When the hub
cannot be read (not with `--no-hub`, which uses local data only), a copy under 7 days old
gives the other machines' leases; having no `burn`,
they settle (above).

**The other strategies** read the same ledger: the pending reservations `p` of an account are
its leases younger than `next.reserve_min`, `spread` uses each account's newest lease and
`sticky` the account of the newest lease. They write a lease per pick too (its `kind` from
the account's mode as `lanes` reads it, `c5` and `cf` the lane charge; without a session
id). `--n` above 1, `--session-id` and `--urgent` are for `lanes` only (exit 2 with another
strategy). `--no-reserve` reads the ledger and writes nothing, with any strategy.

#### The projection strategies

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

**Scores** (higher first among the eligible, ties by label in natural
order):

- `smart`: `weight × preference × safety × confidence × (urgency + 0.02 × headroom)`.
  `urgency` = `U_w / max(h, 1)` of the binding weekly window (the weekly window considered with
  the least unused room): weekly room that would be lost at the reset, per hour left, so room
  that expires soonest unused comes first. `headroom` = the least `cap − E` over the windows.
  `safety` = `clamp((cap − E) / 20, 0.2, 1)` over five-hour windows: an account about to fill
  its 5 hours is ranked down. `confidence` = 0.75 for an account with no usage data at all,
  else 1.
- `expiring`: `urgency` only (ties by headroom): pure use-it-or-lose-it.
- `most-room`: the lowest `max(n + σ + p × S)` over the windows.
- `spread`: the account picked longest ago (never picked first), from the ledger.
- `fill`: the lowest `priority`.
- `sticky`: the last pick while it is eligible with `E ≤ cap − next.sticky_margin` on every
  window; otherwise `smart`.

#### Settings

`wy config set next.<key> <value>` (paths.md 4, `[next]`):

| Key | Default | Meaning |
| --- | --- | --- |
| `next.strategy` | `lanes` | Default strategy. |
| `next.model` | `""` | Default `--model`. |
| `next.session_5h` | `15` | Projection strategies: `S` for five-hour windows, in points. |
| `next.session_7d` | `2` | Projection strategies: `S` for weekly windows, in points. |
| `next.session_min` | `120` | Projection strategies: `T`, minutes. |
| `next.stale_min` | `30` | Projection strategies: staleness threshold, minutes. |
| `next.reserve_min` | `12` | Projection strategies: how long a lease counts as a pending session, minutes. |
| `next.sticky_margin` | `10` | `sticky`: points under each cap that end stickiness. |

Per account: `next.accounts.<key>.<field>`, where `<key>` is matched against, in order, the
hub seat id, the email (without case), the folder (`~/.claude-3`), this machine's label, then
the hub's label; the first key present wins. The email is the recommended key: it is the same
on every machine; a label works as an alias (`wy config set next.accounts.claude-3.mode
aggressive`). The field is the part after the last dot, so emails with dots work.

| Field | Meaning |
| --- | --- |
| `mode` | Overrides the hub setting on this machine (*Kind* above). `lanes`: `protected` or `conservative` make the account protected; `aggressive`, `normal` or no mode (and no hub setting) make it aggressive. Projection strategies: `conservative` and `protected` (caps 5 h 60, 7 d 70, model 70; preference 0.6), `normal` (90, 90, 90; 1), `aggressive` (98, 98, 98; 1.4). Default `normal`. |
| `cap_5h`, `cap_7d`, `cap_model` | Explicit caps (0 < cap ≤ 100) over the mode's; `cap_model` defaults to `cap_7d` when only that is set. `lanes`: `cap_7d` is the aggressive weekly limit or the protected target `T`, `cap_5h` the aggressive 5-hour limit or the protected 5-hour floor. |
| `reserve_pct` | Taken off every cap (room kept for something else). |
| `priority` | `fill` order, lowest first; unset is after every set one. |
| `weight` | Multiplies the `smart` score (default 1). |
| `exclude` | `true`: never recommended. |
| `pin_reserve_5h`, `pin_reserve_7d` | This account's pin reserve per live pinned session (8.6) instead of `account.pin_reserve_5h` / `_7d`; 0 or unset: the global one. Both are scaled to the time left in their window the same way. |
| `hours`, `days` | Allowed local times, e.g. `09:00-18:00` (may cross midnight) and `mon-fri` or `sat,sun`. |

Unknown fields are reported by `wy doctor` as unknown keys.

#### Flags, output and exit codes

`wy account next [--strategy <s>] [--model <m>] [--n <K>] [--session-id <uuid>]...
[--urgent] [--expect short|medium|long|<duration>] [--expect-minutes <N>] [--expect-tokens <N>]
[--env | --json | --explain] [--no-reserve] [--no-hub] [--exec [-- <cmd> [args...]]]`

- `--n K` (1 to 50): picks for K sessions. Default: the number of `--session-id` values, else 1.
- `--session-id <uuid>`, repeatable: the id each session will get (`claude --session-id`),
  in pick order; given at all, there must be exactly K. Each must be a UUID (exit 2).
- `--urgent`: above.
- `--expect short|medium|long|<duration>` (lanes; e.g. `long` or `3h`), `--expect-minutes N`,
  `--expect-tokens N`: how big the caller expects each new session to be (*Estimates*); the
  class is case-insensitive, a wall time must be positive and given once (`--expect <duration>`
  or `--expect-minutes`), and tokens are input + output + cache. Other values: exit 2. They
  set drain capacity, the runway order, the landing guard and the 5-hour forward check for the
  lanes of this call.
- `--env` and `--exec` need K = 1 (exit 2). With `--exec` (lanes, not `--no-reserve`), a
  `claude` command that has no `--session-id`, `--resume`, `-r`, `--continue` or `-c` argument
  gets `--session-id <uuid>` added (the one given, else a new random one), so the lease names
  the session from the start and is marked launched. Except `claude --bg`: claude ignores
  `--session-id` there and its supervisor names the session, so wy runs it as a child (its
  output through a file, not a pipe the supervisor would inherit), reads the id from its
  `backgrounded · <id>` line and the folder's registry, and appends a `bind` line (`id` the
  lease's, `session_id` the session's) and `launched` to the ledger.
- `--parent <session id>`: the session that asks for the lease, recorded on it (`parent`) so
  Watchyard can tell it when the lane moves (8.6, Who is told). Default: the Claude Code
  session the command runs inside (found as `wy peers` finds the caller), none outside one. The
  command of `--exec` gets this environment without the calling session's variables
  (`CLAUDECODE`, `CLAUDE_CODE_SESSION_ID`, the inbox token, `CLAUDE_PID` ...): the lane is a
  session of its own.

**Output.**

- Default: one line per pick, `<label>  <folder>  (<reason>)`. For `lanes`, e.g.
  `claude-3  ~/.claude-3  (aggressive; 7d 23%, resets Wed 10:00; 5h 12%; 2 leases in 60 min)`:
  the kind, the weekly and 5-hour levels the pick was judged on, `unknown` for a weekly level
  with no reading, `urgent` for a pick
  past its burst cap, `over (no account free under its soft limits)` for an over pick
  (*No stalling*), `expect <class>` (the class used: `short`, `medium`, `long`, `measured`),
  `local data` or `no usage data`, `data <age> old` when the newest reading
  is over 60 minutes old, and `session <uuid>` when given. A drain pick says instead, e.g.
  `claude-2  ~/.claude-2  (aggressive; drain: 9% of week left, resets 09:30, finish by 09:20; 5h
  50%; 2 of 5 drain lanes running)` (its measured levels; lanes running now and with its capacity;
  `runs past reset` for a spill lane). For the projection strategies, e.g.
  `claude-3  ~/.claude-3  (7d 23% → ~41% at reset Wed 10:00; 5h fresh; aggressive)`: the
  weekly windows (`n` → `R` at the reset), the five-hour windows (`fresh` under 1 %),
  `unknown` for a window with no reading, a mode other than normal, `+N reserved`, `local data`
  or `no usage data`, and `data <age> old` when stale. When the hub could not be read,
  `  [hub not read: <why>]` follows.
- `--env`: only `CLAUDE_CONFIG_DIR=<absolute folder>`, or `-u CLAUDE_CONFIG_DIR` for the
  default view of `~/.claude` (with the variable set to `~/.claude`, Claude Code reads
  `~/.claude/.claude.json`, which may be another login: identity.md 3). For
  `env $(wy account next --env) claude`; a folder with spaces needs `--exec`.
- `--json`: `{"v": 1, "time", "strategy", "model", "data": "hub"|"local", "hub_error",
  "reserved", "picked": {"label", "folder", "dir", "env", "reason", "session_id", "urgent",
  "drain", "finish_by", "weekly_reset", "spill", "why", "hint", "expect", "over"} | null, "picks": [<as picked>...], "waiting", "next_free", "skipped_ledger_lines",
  "accounts": [...], "warnings"}`. `picked` is the first pick, `picks` every pick in order,
  `waiting` the number of the K sessions not placed. Each account has `label`, `folder`,
  `dir`, `default_view`, `seat`, `email`, `seat_matched_by`, `logged_in`, `eligible`,
  `excluded_reason`, `score`, `mode`, `mode_from` (*Kind*: `local`, `hub`, `proposed`, `none`),
  `settings_key`, `live_sessions`,
  `pending_reservations`, `data_source` (`hub`, `hub+local`, `local`, `none`), `data_at`,
  `data_stale`, `free_at`, `reason` and `windows` (each: `key`, `kind`, `cap_pct`,
  `used_pct`, `reading_at`, `age_min`, `stale`, `now_pct`, `uncertainty_pct`,
  `at_session_end_pct`, `at_reset_pct`, `resets_at`, `hours_to_reset`, `rate_pct_per_hour`,
  `rate_from` (`history`, `window`, `global`, `live_sessions`, `none`),
  `unused_at_reset_pct`, `over_cap`, `source`; empty for `lanes`). For `lanes` each account
  also has `kind` and `lanes`: `{"weekly_pct", "weekly_measured_pct", "five_hour_pct", "weekly_reserve_pct",
  "hours_to_reset", "reading_at", "reading_age_min", "five_hour_resets_at", "meter_5h_pct",
  "meter_7d_pct", "unexplained_5h_pct", "leases", "burst", "burst_cap", "capped",
  "last_lease", "expiring", "stale", "unknown": [...], "skip_level", "weekly_cap",
  "five_hour_cap", "pins", "pin_reserve_5h_pct", "pin_reserve_7d_pct", "calibration",
  "lease_charges": [...], "class", "drain"}`
  (`leases` = active leases, `burst` = leases in the last 60 minutes, `meter_*` the token
  meter's points with its margins, `skip_level` 0 for a burst cap only, 1 stale, 2 a protected
  rule, 3 a hard limit; `weekly_cap` and `five_hour_cap` the levels at which it takes no new
  session now, as in record.md 4.11, already less the pin reserve; `pins` the account's live
  pinned sessions on this machine and `pin_reserve_*_pct` the points kept free for them (8.6,
  Pinned sessions); `class` one of `drain`, `at_risk`, `aggressive`, `unknown`,
  `protected`, and `expiring` true for `drain` (kept for one release); `drain` `{"on", "room_pct",
  "room_pts", "need_hours", "horizon_hours", "capacity", "capacity_weekly", "capacity_5h",
  "limited_by": "weekly"|"5h"|"spill"|"stale"|"landing", "running", "forward_burn_pts",
  "projected_at_reset_pct", "unavoidable_waste_pct", "finish_by"}`, Drain: the capacities,
  projection and `finish_by` null when not draining, `capacity_5h` null when the 5-hour window
  does not limit it). A drain pick has `drain` true, `finish_by` and `weekly_reset`, `spill`,
  `why` ("drain: 9.3% of the week resets in 8.3 h and would expire unused") and `hint`, the
  one-line instruction for the agent: "Weekly room on claude-2 ends at 09:30 (finish by 09:20):
  plan work to finish or checkpoint by then; if you hit a usage limit before that, stop and
  start a new session with `wy account next` instead of waiting."; any other pick has `drain`
  false and `finish_by` and `weekly_reset` null. `lease_charges` lists the account's leases younger than 5 hours and
  older ones that still keep a weekly charge, oldest first: `{"at", "machine": "this"|"other",
  "session_id" (this machine's leases only), "copy" (true: from the kept copy), "launched",
  "state": "active"|"settled"|"ended"|"expired", "burned_from": "local"|"hub"|"none",
  "burned_units", "burned_pct", "charge_5h_pct", "reserve_pct", "weekly_kept_pct"}` (`burned_pct`
  = `b`, `charge_5h_pct` = `max(0, c − b)` while active, `reserve_pct` = `max(0, cf − b)` while
  active, `weekly_kept_pct` the weekly charge kept after it). `calibration` is `{"source": "hub"|"cached"|"local", "at", "since",
  "reset_reason", "f", "ratio", "lane_price": {"value", "fallback", "measured", "weight", "n",
  "days", "implausible"}, "lane_charge", "lane_units", "unexplained_pct_per_hour",
  "unexplained_hours", "corrector_5h", "corrector_7d", "margin_5h_pct", "margin_7d_pct",
  "unexplained_from": "clean"|"net"|null}`: the factors the account was judged on
  (Calibration; `f` in points per million units, `measured` null when there is none,
  `reset_reason` null or the reason of the last restart), `lane_charge` = `c`, `lane_units` the
  pooled mean lane units, `corrector_*` the margin factors `k5`, `k7` and `margin_*` the margins
  `g5`, `g7` in points, `unexplained_from` how the unexplained rate was measured (null: none). Eligible
  accounts come first, in pick order for the first pick. Times are RFC 3339 UTC.
- `--explain`: the same as a table, then the pick lines. For `lanes`, the kind column says
  where it came from: `protected (hub)`, `protected (set on this machine)` (and `; hub says
  aggressive` when the hub's setting differs), `aggressive (proposed here)`, or `not chosen
  (treated as aggressive)`; under the table one line per kind shown gives its definition
  (*Kind*), and an account that is not chosen adds "Choose on the board (Accounts, the account,
  How should the picker use this account?) or: wy config set next.accounts.<email>.mode
  protected". Then kind, weekly level
  (reserve level for protected) and reset, 5-hour level, leases and burst, data source and
  age, why; then one calibration line per account: `f`, `ρ`, the lane charge, their source and
  weights, the margins and the unexplained rate; one line per account with live pinned sessions
  ("Pins claude-3: 2 live pinned sessions keep 30% of the 5-hour window and 6% of the week free
  (caps now 5h 15, 7d 79)"); then one line per lease of `lease_charges`:
  account, pick time, this or another machine (and the kept copy), state, the burned units,
  where they came from (`local`, `hub` or `none`) and `b`, and the lease's 5-hour charge, reserve
  and kept weekly charge; and per draining account a line "Drain claude-2: room 9.3% (71 pts), need
  3.6 h at full 5h speed, horizon 24 h; capacity 3 (weekly 6, 5h 3); 2 running, burn 12 pts before
  reset; projected 96% at reset; unavoidable waste 0.0%; finish by 09:20". For the projection strategies: 5 h
  now→end/cap, 7 d now→reset/cap, other windows, data source and age, why.
- `--exec [-- <cmd> [args...]]`: runs `<cmd>` (default `claude`) found on `PATH` with the
  pick's `CLAUDE_CONFIG_DIR` (unset for the default view of `~/.claude`), replacing `wy` (on
  Windows, waiting for it and exiting with its code). It prints the pick line on stderr first. A
  drain pick also sets `WY_FINISH_BY` and `WY_ACCOUNT_RESET` (RFC 3339 UTC) for the command and
  prints the `hint` line on stderr; any other pick sets neither (an inherited one is removed).
  It is an explicit action of the caller.
- `--no-hub`: local data only.

`--env`, `--json`, `--explain` and `--exec` do not mix (exit 2).

**Exit codes.** 0: every session asked for was placed. 4: fewer than K were placed (none,
for K = 1): no account can take the rest now (`lanes`: every account at a hard limit or unreadable). The picks that were placed are printed and
leased (launch only those); the line on stderr says when the earliest account frees up
(`next_free`, or that none does at a known time; after a partial batch, `lanes` ranks once more
with the placed picks leased to find it) and points to `--explain`; `--json` and
`--explain` still print their document on stdout. 3: a `--session-id` is already leased or
given twice; nothing was written (make new ids and ask again). 2: usage. 1: another error (a
hub that cannot be read is not one: the command falls back to local data; a ledger that
cannot be written is one, since the pick could not be recorded).

### 8.6 Smart Account Switch (`wy move`)

The picker (8.5) hands out lanes on estimates, and estimates are wrong: some lanes run half an
hour, some several; some burn a lot, some little. Smart Account Switch is the correction. When
an account's measured burn now would take its 5-hour or weekly level over a cap, it moves the
sessions that are **burning** (in the middle of a turn too) to an account with room. Moving an
idle session frees nothing, so idle sessions with no burn never move. Interactive sessions (a
person at them) move the same way, and the person sees a message where the session ran.
It is off by default; `wy move` by hand works either way.

**Setting.** `wy config set account.smart_switch on|off` (default `off`). Off: nothing moves by
itself. On: `wy tick` (the timer, once a minute) runs one pass (`wy move --auto`) each tick. Per
session opt-out: pin the session in Claude Code itself (below, Pinned sessions); there is no other
pin. Every number below is a setting `account.move_*` with the default shown (`wy config set account.move_cap_5h 92`).

**Scope.** `wy config set account.smart_switch_scope <scope>` limits which sessions the switch
(and `wy move` by hand) may move: `all` (the default), `tag:<tag>` (sessions with that peers
tag; `[a-z0-9-]`, up to 40 characters) or `sessions:<id>,<id>...` (ids or prefixes of at least 8
characters). Every other session counts in the projections but is pinned. For gradual rollout
and for tests. The scope tag is a marker tag: adding it sends no tag notice (peers.md 3.4). A `claude -p` session has no inbox, so it cannot carry a tag: `sessions:` is the
only scope that reaches it. Planned, not in this version: the board's Settings toggle and the
phone toggle, through an owner-scope hub field `account.smart_switch` (boolean) that machines
copy into their `config.toml`.

**Organisations.** `account.move_orgs`: `any` (the default: an account of another organisation
is as good as one of the session's own), `same-first` (the session's own organisation first,
another only when none of its own has room) or `same` (never another organisation). A session
resumed under another organisation goes on with its conversation intact, but Claude Code drops
its earlier thinking blocks ("Claude can't use thinking from another organization"); its move
note says so.

**Inputs.** Per account, the lanes picker's own measured levels now (the reading, the token
meter since it with its margins and the unexplained use; no lease charges), the resets, the
calibrated ratio ρ (5-hour points per weekly point) and f (5-hour points per token unit), read
the way `wy account next --no-reserve` reads them (hub when it can be read, else local), and
whether it is **logged in on this machine** (its folder exists here and the login check of
`wy account next` says so; credential files are never read). Per session, from Claude Code's
session registry in every config folder (`<folder>/sessions/<pid>.json`: pid, session id, cwd,
name, kind, status `idle`/`busy`/`shell` (idle with background shells running), a Remote
Control bridge), the process table (its command line; its start time, so a reused pid is never
touched), for a background session its supervisor's job record (`<folder>/jobs/<job id>/
state.json`, only its `respawnFlags`) and `tmux list-panes`: its **burn** (its token rows of
the last `move_burn_min` = 10 minutes, helpers included, × f, in 5-hour points per hour; the
per-session numbers of `wy burn`), whether it is busy (mid-turn), whether a limit stopped it,
whether it is interactive (below), its context (the newest `context_pct` × 200k tokens), its
`--expect` if it was leased with one, and its last move (`<state>/move/moves.jsonl`). The
environment of another process is never read (keys.md 8).

**Projections.** Per account, every pass:

- R5, the account's burn: its sessions' burn here (the token meter × f) plus the burn of its
  sessions on other machines that the hub measured (their leases' per-minute units in the burn
  window × f; `--plan` says "hub: other machines' sessions"). The 5-hour readings are a
  cross-check for the rest (claude.ai, a machine without a hub lease): their **slope** (least
  squares) over the newest 15 to 30 minutes of one 5-hour window, readings at least 5 minutes
  apart (a reading 1-2 minutes after the last one is skipped; a window reset starts over). When
  the slope is over twice that burn (and over 1 point per hour) on **two passes in a row**, R5 is
  the slope; on the first such pass `--plan` says "used if it holds on the next pass". Readings
  are whole percents: one point of rise read over 10 minutes is 6 points per hour, which is why
  a slope over a longer span, held over passes, replaced the rise of the last two readings
  (1.3.3-pre3 moved sessions off accounts whose sessions ran on another machine on that alone).
- **P5** = the 5-hour level now + R5 × hours to the 5-hour reset (a window not started: 0 + R5 ×
  5 h).
- **P7** = the weekly level now + R5 / ρ × min(hours to the weekly reset, `move_week_span_h` =
  5) (the burn of the next few hours says little about later days).
- Caps: aggressive `move_cap_5h` = 95 and `move_cap_7d` = 98; protected `move_cap_5h_protected` =
  80 and `move_cap_7d_protected` = 80.
- **Expiring room.** Within `move_expiring_h` (3) hours of a protected account's weekly reset its
  weekly cap is `move_cap_7d` (98): the room left would expire unused. Its 5-hour cap stays 80.
  This holds for the switch (trigger, destinations) and the picker (its drain may use the room;
  `--explain` prints "Expiring room claude-5: protected, its week resets in 1.1 h: it may be
  drained to 98% of its week until then", `--json` `expiring_room_cap_7d`; `--plan` says
  "expiring room"). After the reset the protected caps hold again (live: protected claude-5 at
  85% an hour before its reset took nothing, its protected cap 80 bounding its drain).
- **Stale data.** An account whose levels rest on a reading older than `move_stale_min` (10)
  minutes (the newest usage reading of its seat, from this machine or the hub) is neither a
  trigger source nor a destination: an old reading plus hours of token meter is not evidence of
  the level (live, pre6 05:37, the hub migrating: claude-4 read "5h 100%" from a reading 3 h 37
  min old, the meter since then ~87 points, while the account was at ~68%). Only its sessions
  stopped at a limit still move (their limit reply is fresh evidence). `--plan` says "data stale
  (3 h 37 min), no moves".

  *Where readings come from.* An account's level is read from Claude Code's status line (each
  render of an interactive session carries `rate_limits`; `wy statusline` keeps it locally and
  sends it to the hub) and from claude.ai's usage page (`web`, read by the desktop app for the
  accounts signed in there, about every 5 minutes). Headless sessions (`claude -p`, agents) have
  no status line, so an account that only runs them, or runs nothing, is read only through
  claude.ai; an account signed in to neither goes stale while idle (live 2026-11-25: claude-5's
  newest reading 108 hours old). There is no supported way to read a level without a session's
  status line or claude.ai (the usage endpoint needs the OAuth token, which Watchyard never reads).

  *Quiet accounts.* With the hub read in the pass, an account on which nothing ran since its
  reading (no live session on any machine, no running lane, no tokens since: `Account.Quiet`) is
  not stale: its levels can only have held or fallen (a window that reset since counts as 0), so
  the old reading is an upper bound, and it is a destination like any other.

  *The hub not read.* A switch pass reads the hub within 20 seconds (`wy account next`: 5). When
  that fails, it plans on this machine's own readings, usually hours old for accounts that run
  only headless sessions (live 2026-11-25 20:06: the hub's hourly backup, 20:02:57 to 20:08:03,
  slowed `GET /v1/usage` past 5 s and every pass in it saw claude-4 "7 h 38 min old").

  *Alert.* The owner is told only when stale data held back a move this pass
  (`Result.StaleBlocks`): a source over its cap with sessions on it, an account left out as the
  destination of a session that found no other, or a draining account whose pull of a busy
  burner was skipped; and only with data over 30 minutes old, at most once per account per 6
  hours (`<state>/move/stale-alerts.json`). It is a line in the pass's log and a push as the
  digest's (the hub's `switch_digest` alert, or direct push when the hub is away): "Smart Account
  Switch held back a move on stale data: claude-5 (data 108 h 16 min old: left out as the
  destination of a session that found no other). no new usage reading came in for it (…)"; it
  says "the hub could not be read this pass (…)" only when this pass's hub read failed. An account
  with no reading time is not stale by this rule (the picker's own stale and unknown rules
  hold). For the picker with the switch on, an old sample's level neither blocks an aggressive
  account nor draws a lane: such an account is ranked after the accounts with fresh data, and
  `--explain` says "data stale (217 min old): its level (5h 100%, 7d 30%) neither blocks nor
  draws a lane; ranked after accounts with fresh data". A protected account keeps its blocks.
- **A slow pass moves nothing.** A pass whose state (config, levels, sessions) took over 2
  minutes to read moves nothing: the next pass reads it again (live 05:40:42: a pass of the old
  binary that read its config before claude-5 was set protected moved a session minutes later,
  at 0.0 points/h).

**Decision (the defaults).** An account **triggers** when P5 is over its 5-hour cap or P7 over its
weekly cap; **hard now** when its 5-hour or weekly level already is at its cap.

| Rule | Default | Why |
| --- | --- | --- |
| Pass | every 60 s while the switch is on (each `wy tick`) | the burn changes in minutes; a pass reads local files and the hub once |
| Burn window | `move_burn_min` 10 min | long enough to smooth one turn, short enough to see a lane start burning hot |
| Young sessions | a session under `move_young_min` = 15 min old (since its first lease in the ledger, across moves, else unknown: old; a session moved onto its account within 15 min is never young: its burn counts at once, at the burn it moved with; live pre9 10:47: 54c4e1e8, moved onto claude-6 at 10:35 at 21.9 points/h, "under 15 min old" while claude-6 was over its cap) has its burn left out of its account's R5 (not of a rate from the readings, which are still compared with the full meter) | a lane's first minutes (reading the codebase, cache writes) burn several times its steady rate, and the burn window then holds only those minutes: live 2026-11-25 two lanes moved 3 minutes after launch. Simulator (300 runs × 7 patterns, a start-up burst of 3-6× for 3-8 minutes): moves of lanes under 15 minutes whose source was under its caps at its steady burn 3.65 → 0.23 a day, all stopped minutes 1172 → 1164, protected minutes over 80 +0.9%; 10 and 12 minutes left 1.85 and 1.32, the rate × min(1, age/10) 2.89, cache writes weighted 0.25 for 10 minutes 2.68, the picker's estimate while young 3.52 |
| Burn floor | `move_min_burn` = 1 point/h: a running session is a candidate only when its own measured burn over the burn window is at least this; a session under `move_young_min` is never one (its burn does not count yet); the greedy stops at a pick that would not lower the source's projection | moving a session that burns nothing frees nothing and costs a re-read: live 2026-11-25 a mid-turn headless lane at 0.0 points/h was moved with a 4.6 points/h one |
| Trigger | P5 > `move_cap_5h` (95; protected 80) or P7 > `move_cap_7d` (98; protected 80) | owner rule 1: the projected 5-hour level must stay at 95-98 or under; rule 2: the weekly 98 (aggressive) or 80 (protected); a protected account's 5-hour level never over 80 |
| Hard now | the level is at its cap now: trigger at once | the projection cannot be under the cap any more |
| Spike filter | a trigger must hold on `move_spike_passes` = 2 passes in a row, counted per account and window (a 5-hour trigger and a weekly one are two streaks), unless the projected hit of 100 is within `move_spike_near_min` = 30 min or it is hard now | one hot minute (a helper panel) should not move anything; a near hit cannot wait a minute |
| Readings only | a trigger that only the readings' slope makes (at the measured burn the account is under its caps, and it is not hard now) must hold on 3 passes, near hit or not | readings are whole percents and lag; the measured burn is exact |
| Candidates | every session on a triggered account that **burned** in the burn window (busy, or tokens in it), whatever its turn state: **mid-turn moves are allowed** | owner: "we move the agent that is working, not the agent that is paused"; moving an idle session frees nothing |
| Never moved | idle sessions with no burn; pinned in Claude Code or outside the scope; claude.ai `--sdk-url` sessions | |
| Pin reserve | `pin_reserve_5h` 10 and `pin_reserve_7d` 5 points per live pinned session: the account's caps for every other session (trigger, enough, destination) are its caps less pins × these | long-running pinned sessions (a Slack watcher, an orchestrator) must not stop; the simulator's best row (below) |
| Stopped by a limit | a session whose last turn a limit ended (`StopFailure` `rate_limit`) is a candidate too, whatever its burn (it wants to burn): moved when that window resets `move_stopped_wait_min` = 20 min or more from now, else it waits for the reset | a move takes a minute or two and re-reads the context; a near reset is cheaper |
| Choice | greedy, recomputed after each pick: first a session whose move alone brings both projections under cap − hysteresis (the fewest moves); among those, the most points freed per re-read (burn ÷ (context, at least 20k tokens, × model factor: opus 5, sonnet 3, haiku 1, else 3)), then the highest burn, then the youngest; among the others the highest burn, then the smaller context, then the longest expected remaining time | the highest burner frees the most; when several would each do, the cheapest re-read (simulator: small gain, below) |
| Enough | stop when P5 and P7 of the source are at most cap − `move_hysteresis` (5); for a window whose level now is already at or over cap − 5, at most the cap (no move takes a projection under the level now; live: claude-3 at 84.1 of cap 88 had every unpinned session moved off, down to 0.2 points/h). Still over with nothing left a move would lower: `--plan` says "only pinned burn left (x points/h pinned, y unpinned)" | hysteresis: no move back and forth around the cap; a target that can be met |
| Destination | logged in on this machine (never an account only another machine has); not its own, not stale, not excluded; organisation per `move_orgs`; its P5 and P7 as the trigger will project them after the move (to its resets, with the moves already planned in this pass, this session's burn until its expected end when known, else to the resets) at most its caps − 5, so the next trigger does not undo the move (live pre7: 3b2611ed moved onto claude-6 at 06:12 on a 90-minute horizon, 39 + 27 × 1.5 ≈ 80, and off it at 06:35, "5h projected 111%"; `move_dest_horizon_min` is retired and ignored); a protected account only when no aggressive account fits; a session moved onto an account within `move_young_min` counts there at the burn it moved with (its re-read is left out), not 0, and a lane under `move_young_min` (its burn left out of the trigger's projection) at the picker's new-lane burn (else its measured burn), not 0: the trigger counts it 15 minutes in (live pre9: ai-lane-E, 27.1 points/h, moved onto claude-2 at 10:00:58 while its fresh lanes' 6.5-8.7 points/h were left out; they burned 60-90 points/h by 10:10 and the trigger fired at 10:11, "5h projected 95% (cap 85%)"); order: aggressive accounts first, draining ones (the picker's `drain` class: room that expires unused) first, then the soonest 5-hour reset, then the most room; protected accounts last and only within their 80/80 caps | the session must be able to run there; room that expires is worth most |
| No account fits | no move; `--plan` says why ("no logged-in account on this machine has room for its burn", "no account of its organisation has room (account.move_orgs = same)", "account rate limit") | |
| Cooldown | `move_cooldown_min` 20 min per session | |
| No return | never back to the account it left within `move_no_return_min` 60 min, unless that account's 5-hour window reset since | no ping-pong |
| Account rate limit | at most `move_account_moves` 5 moves of sessions burning at least the burn floor, and at most `move_account_burn` 40 points/h of moved burn (one move always fits), into one account per `move_account_window_min` 15 min. Soft for aggressive accounts: an aggressive account with room that only these limits hold back takes the move before any protected account does (live: claude-2 at 13%/18% had taken 3 moves, two of them 0.2-0.5 points/h, and the next went to protected claude-7, projected 74% against its cap 60) | a burst of moves must not overfill one account; the destination's projection check is the real guard |
| Near hit | the source reaches 100% before its 5-hour (or weekly) reset within 60 min by its first projection in the pass, or is hard now: (1) the account rate limit does not hold the move back (it applies to soft triggers only); (2) the greedy takes the highest burner first after the sessions whose move alone fixes the source; (3) when no destination stays under its caps − 5 until its own reset, the switch takes the aggressive account (not one this session may not return to) that stays under its caps the longest, if that is at least as long as the source has left and at least 30 min: `--plan` says "best available: claude-6 fits for 2h15m". Protected accounts never take a best-available move. A soft trigger (no hit within 60 min) whose sessions in their cooldown (or with a move under way) burn more than all the movable ones together, and those cannot bring it under its caps, waits ("waiting: moving the others (1.7 points/h) would not bring claude-6 under its caps while 21.9 points/h held back for now burns more"); the owner grace does not count (it moves another burner on purpose) | live pre9: home-18 (2.3 points/h) moved off claude-2 at 10:12 while ai-lane-E (27.1) cooled down, and home-00 (1.7) off claude-6 at 10:37 while 54c4e1e8 (21.9) did; live 2026-11-25 10:31: claude-4 at 78%, 43.8 points/h, P5 99%: the switch moved only a 2.7 points/h session; 25.9 points/h stayed ("account rate limit") and 16.9 points/h ("no logged-in account has room") while claude-6 was at 16% and claude-2 at 34%. Two hours on claude-6 beat a hit in 20 minutes |
| Once per window | none: a long session may move many times | owner: long sessions may need several moves |
| Between steps | a mid-turn session is stopped once no step runs, waiting up to `move_step_wait_min` 3 min in all, never past one minute before the hit; a pass's waits run side by side, at most `move_pass_wait_s` 90 s, the rest wait on in the next pass | a step stopped half-way is often run again after the resume |
| Off | an account that is off (8.5, Kind): its sessions are no trigger ("off (owner): its sessions stay, and nothing is moved into it", even over its caps), it is never a destination, and a drain pulls nothing from it | owner 2026-11-25: stricter than protected; its usage is still shown |
| Owner drain | an account the owner drains (8.5, Owner drain): (1) nothing moves off it unless its 5-hour window is at its cap or reaches 100% within 60 min ("draining (owner, until 23:59): nothing moves off it before ..."); (2) while its 5-hour (and weekly) projection to its reset is under its cap − `move_pull_margin` 10 (weekly: 10 ÷ its ratio), and nothing moved off it this pass or in the last `move_pull_quiet_min` 15 min (a near hit's move off it: a pull then ping-pongs), the pass pulls in the biggest movable burners of other accounts (busy, not pinned, not interactive, not young, out of cooldown, burning at least `move_pull_min_burn` 3 points/h: a mid-turn restart re-reads the context, not worth it for a 1.1 points/h lane), each counted at max(its burn, the lane rate the destination check counts a young lane at): a lane in a low phase burns more later; the biggest that fits first, each only when it stays `move_pull_margin` 10 under the 5-hour cap and the same burn's weekly share (10 ÷ its ratio, about 1.1) under the weekly cap until the resets after the ones before (one too big is passed over for the next biggest), the account rate limit applying; with several owner drains, the one whose room is lost first (its weekly reset, else its until) pulls first ("pulled in: claude-4 is draining (owner, until 18:00) at 20.0 points/h of burn") | owner 2026-11-25: claude-4 should finish its weekly room today, before a free full reset; live pre12: ed2c70e0 moved off claude-4 on a near hit at 15:08:22, 08911aa8 (29 min old, 3.3 points/h in a low phase) was pulled in at 15:09:57 and burned 14.8 by 15:28, claude-4 at P5 101% again. Live pre16: claude-3 at 89%, aim 96, its week resetting in 4 h, pulled nothing (89 over 96 − 10), and 4f4ab07c went to claude-4, draining until Sunday The digest's weekly room marks a drained account "(draining)" |
| Drain destination margin | a move INTO a draining account leaves its P5 and P7 at most its caps − `move_drain_dest_margin` 2 (a pull keeps its own `move_pull_margin` 10); the source side is unchanged: a drain triggers at its (ramped) cap with no 5-point margin | with no margin a move filled the drain to exactly its cap and the next pass's drift moved a lane out: live 2026-11-26 01:00 and 01:03 two lanes moved claude-2 → claude-4, 01:04 ai-lane-T moved off it ("5h projected 98% (cap 97%)") |
| Near-reset skip | a 5-hour trigger (soft or near hit) is not acted on when its projected 100% hit is within `move_reset_skip_min` 10 min of that window's reset (skip: "5h hit N min before its reset: a stall costs less than a move"); a limit reached now (level at 100, `Hard`) or a session actually stopped follows the existing rules unchanged; the weekly window is not affected | the stall costs at most 10 min, a mid-turn move a kill and an uncached re-read: the 01:04 move was for a hit at 01:55 with claude-4's window resetting 01:59 |
| Re-check | a mid-turn move carried out 5 s or more after its pass began to read its state is re-chosen on the state read again just before it is carried out: the pass is planned again (its own move lock left out); no move for it now (its source no longer needs it, or another session's move is enough) drops it ("not moved to claude-7: re-checked after its step wait: ..."); another destination takes it (aggressive before protected, as always), the move record's `recheck` saying so ("re-checked after its step wait: claude-4, not claude-7 as planned"). Each change is kept in `move/rechecks.jsonl` and `--plan` lists those of the last hour ("re-check 11:58: 441d209b ... was planned claude-2 -> claude-7, moved to claude-4 instead"); `--list` shows the note. A failed read keeps the move as planned | a step wait is up to 3 minutes; live pre10: 441d209b was planned onto protected claude-7 at 11:57:35 (claude-4 projected 90.2 of its 90) and moved at 11:58:41, when claude-4 had room (its burn fell from 11.3 to 9.6 points/h) |
| Owner grace | `move_owner_grace_min`, default 0 (off): an interactive session the owner typed into within it stays | the simulator shows no clear gain (below); off |

**Pinned sessions.** A pinned session never moves. Pins come from Claude Code only (the owner's
decision: no manual pinning in Watchyard). The account's config folder holds `jobs/pins.json`, a
JSON array of short session ids (`["48d8d877","2abbe6d1"]`) that Claude Code keeps; an entry pins
the session of that folder whose id starts with it. Watchyard only reads the file (each pass, read
again when its modification time or size changes) and never writes it. A missing file is no pins;
one that cannot be read or parsed is no pins and a warning (`--plan`, `--list`; the switch's log
has it once, until it changes). A pin counts only for the folder whose list has it: after a move
the session is not pinned on its new account until it is pinned there.

Retired: `wy move --pin-account` and `--unpin` (they print where to pin instead and change
nothing), wy's own `<state>/move/pins.json` (left in place, not read; `--plan` and `--list` warn
while it lists sessions) and the peers tag `no-move` as a pin (it pins nothing; it stays a marker
tag, so adding it notifies no holder).

`wy move --plan` and `wy move --list` print one line per pinned session (`pinned: 2abbe6d1 planner
on claude-3 (in Claude Code; holds a reserve)`); `--json` adds `pins` (`session`, `name`,
`account`, `from` = `claude`, `live`; `--list --json` is `{"moves": [...], "pins": [...],
"warnings": [...]}`). A skip says `pinned (in Claude Code)`. A by-hand `wy move` of a pinned
session needs `--force` and warns that the pin stays on the old account's Claude Code list.
Pin the long-running sessions that burn little but must not stop: a Slack watcher, an
orchestrator, a CI watcher. Each *live* pinned session (running, and not idle for 6 hours) keeps a
reserve free on its account: `account.pin_reserve_5h` (default 10) points of the 5-hour window
and `account.pin_reserve_7d` (default 5) points of the week; an account with n live pins keeps
n × these, scaled to the time left in each window (owner 2026-11-25: "it will never increase
more than 88%. That's a huge waste of almost one 5h session"): a flat reserve throws the
reserved room away at every reset, so each window keeps only

    min(n × reserve, max(1, the pins' burn × hours to that window's reset × 2))

the pins' burn being their measured 5-hour points per hour (at least 1 point per hour per pin,
a coordinator's awake base; for the week divided by the account's ratio). Far from a reset that
is the full reserve; in the last hours it shrinks to the pins' real need, so a drain can use
the rest of the week (claude-3 at 88% with 2 pins at 5.7 points per hour, ratio 9.1, 5 hours
from its reset: 6.3 of the week instead of 10, 2.5 two hours out, 1 at the end). With it:

- the picker (8.5) hands out new lanes as if every limit of the account were that much lower
  (`--explain` prints a `Pins` line, `--json` the fields `pins`, `pin_reserve_5h_pct`,
  `pin_reserve_7d_pct`; the caps shown are after it);
- the switch's trigger and *enough* test for the other sessions on the account use its caps less
  the reserve, so it moves the other burners off before they eat into the reserve; a pinned
  session's own burn counts in the projection as before;
- a destination keeps its own reserve free (its caps less its reserve);
- a protected account counts the reserve inside its 80/80 caps;
- `wy move --plan` shows it per account (`PIN RESERVE`, "1 pinned: 10/5"; the `CAPS` column is
  after it), and a scaled one in the state ("pin reserve 7d 2.5% until Thu 03:30, scaled to time
  left (full 20/10)"); `--explain`'s `Pins` line says the same ("... and 2.5% of the week free; the
  weekly part until Thu 03:30, scaled to time left (full 10)"); the board's verdict says it too; `--json` adds `pin_reserve_full_5h_pct`,
  `pin_reserve_full_7d_pct`.

A pinned session that alone (its own burn, nothing else) takes its account's 5-hour or weekly
level to 100% within 2 hours, before that window resets, is not moved: the pass records one alert
per session and window until the reset (`<state>/move/alerts.json`), printed in the switch's log
and by `wy status` ("alert: pinned session ... alone takes claude-2's 5h window to 100% at 14:20
(...): move it by hand: wy move 5e55a0b0 claude-3 --force"), the command naming the account the switch
would pick for it.

Simulator (`internal/mover/pins_sim_test.go`; 5 coordinators on every pattern of 8.6 placed like
the owner's: claude ci-lv1, claude-2 Slack watcher, claude-3 orchestrator and planner, claude-7
OTA planner; each awake about 17 hours a day at 1-5% of a 5-hour window per 5 hours with dispatch
bursts plus 1-2 heavy windows a day at 10-20%; 9 patterns × 300 runs, per day per run):

| Strategy | Pinned stopped min | Pinned hits (weekly) | All stopped min | Moves (of pins) | Owner alerts |
| --- | --- | --- | --- | --- | --- |
| Pins movable (before) | 289 | 2.20 (0.32) | 1020 | 87.5 (46.5) | 0 |
| Pins never move, no reserve | 293 | 1.22 (0.27) | 871 | 42.7 (0) | 1.33 |
| Reserve 10/3 | 280 | 1.20 (0.24) | 939 | 41.4 (0) | 1.29 |
| **Reserve 10/5 (default)** | **268** | 1.20 (0.22) | 974 | 40.6 (0) | 1.29 |
| Reserve 15/3 | 288 | 1.23 (0.24) | 957 | 41.2 (0) | 1.31 |
| Reserve 15/5 | 273 | 1.22 (0.22) | 981 | 40.4 (0) | 1.30 |
| Reserve 20/5 | 287 | 1.26 (0.23) | 1007 | 40.0 (0) | 1.31 |
| Reserve 10/3, owner moves on alert in 30 min | 229 | 1.23 (0.21) | 895 | 42.0 (0.5) | 1.45 |

10/5 has the fewest pinned stopped minutes in 7 of the 9 patterns. Not moving pins halves their
hits and saves 46 moves and 23 Mtok of re-reads a day. The reserve's gain is small (-9% pinned
stopped minutes, -18% weekly hits) because most pinned stops (about 60%) come when no account
has room at all; it costs the other sessions stopped minutes (+12% all stopped minutes), since
the reserve is room they cannot use. A bigger 5-hour reserve (15, 20) does worse: it moves more
lanes and fills the destinations. Acting on the alert helps most (-17%). On an abnormal day (the
pins at 4-5× their burn) every strategy stops the pins more than twice as long; the alerts rise
to 5.6 a day. The modelled pins use about 27% of a week each, more than the 3-10% they use
normally, so these numbers are on the heavy side.

**Simulator, scaled reserve and drain ramp** (2026-11-25; the same coordinators layer, reserve
10/5, `WY_SIM_PINS="reserve 10/5"`, 10 patterns × 60 runs × 2 days, per day per run; *waste* is
the weekly room left at a reset, *weekly stops* lanes stopped at the weekly limit):

| Strategy | Pinned stopped min | Pinned hits | All stopped min | Waste % | Weekly stops |
| --- | --- | --- | --- | --- | --- |
| reserve 10/5 (flat) | 286.9 | 0.92 | 993 | 20.4 | 4.89 |
| scaled | 269.0 | 0.83 | 941 | 20.6 | 4.68 |
| drain ramp | 284.0 | 0.89 | 976 | 20.2 | 5.02 |
| **scaled + drain ramp (this release)** | **269.7** | 0.86 | 949 | 20.3 | 4.80 |

Scaling does not stop the pins more: their stopped minutes fall 6% (the reserve near a reset
was room the other sessions needed) and all stopped minutes 4%. The weekly waste barely moves in
this model (-0.1 points): its simplified picker sends few lanes to an account in its last hours,
so most of the 20% is on accounts nobody drains; the gain is the live case (an owner drain the
flat reserve held at 88%). The weekly stops are not zero in any strategy (lanes the model runs
past the limit); the ramp alone adds 0.13 a day, with the scaled reserve 0.09 fewer than flat.

**The picker and the switch.** While the switch is on, `wy account next` (lanes strategy, 8.5)
places no new lane on an account where the switch would move a session off: the switch's
projection (`internal/projection`, the same function on both sides) of the account's measured
burn now (this machine's sessions and other machines' sessions the hub measured, as above) plus
the new lane's burn, against the switch's caps less the pin reserve. The new lane's burn is the
measured lane burn `R` of its model (8.5 Lane statistics; with `--model`, that model's own lanes
only, at least 10) and else its estimate's (cost over wall time, or `r`): a size class sets how
long a lane runs, not how fast it burns (live: a "medium" lane counted as 4 points per hour
while Opus lanes here burn 12-26). It is kept within [0.5×, 3×] its class's burn (cost over
time; medium without `--expect`), and `--explain` prints the line "Lane burn (switch check)"
with the burn, where it came from, whether it was clamped and for how long. A lane without
`--expect` counts as a medium lane, for the medium class's time (48 minutes). The account's own burn is
projected to the resets. One rule counts a session's burn on all three sides (the picker's
acceptance, the switch's trigger, the switch's destination check): inside its lease's expected
duration (`--expect`) it burns for max(expected remaining, 30 minutes), at most to each
window's horizon; unknown or overrun, to the horizons (the 5-hour reset, and min(weekly reset,
5 hours)). A short lane cannot burn 5 hours (live: an idle account refused a 25-minute lane at
23 points/h, "projection 115%"). The lease line keeps the lane's length as the picker counted
it: `expect` (the class; `medium` without `--expect`), `expect_min` and `expect_end` (the pick
plus its time); a move lease keeps its session's. The switch takes a lane's expected time left
from it (`expect_end` − now; a move lease written without it, its session's latest), and once
the lane overruns it projects it to the horizons; lease lines without it (older) as before:
unknown, to the horizons. So the switch counts a running lane as the picker did when it placed
it (live, pre7: 2de58a08, no `--expect`, placed on claude-3 at 7d 85% and moved off 17 minutes
later, "7d projected 95% (cap 88%) at 16.4 points/h": the picker had counted it for 90 minutes,
the switch to the horizons).
Lanes leased on the account in the last `move_young_min` (15) minutes, this call's earlier picks
included, count at the new-lane burn less what the meter measured of them, each for its
expected time: their burn is not in the meter yet (live pre9: four lanes went onto claude-2
within minutes, each checked against its idle meter).
A placement is a destination too: the picker lets a lane in only while the projection stays
`move_hysteresis` (5) points under the caps less the pin reserve, as a moved session must.
Simulator (`internal/mover/sim_churn_test.go`, 30 runs × 10 patterns, per run-day): moves the
trigger undid within 30 minutes (no new session there, burn not up 25%) 3.92 → 1.81; placements
the switch undid within 30 minutes (not forced, no new session, burn within 50% of the
estimate) 1.02 → 0.79; moves 41.8 → 34.9, all stopped minutes 1438 → 1396, pinned stopped
413 → 408, protected minutes over 80 619 → 628. Of the placements still undone, 0.48 a day are
the simulator's readings slope (30 minutes) holding a lane's start-up burst its 10-minute meter
has dropped; `wy move` takes both over the one burn window. The switch reads the
accounts from the same readings and effective state as the picker (8.5, Effective state; the
picker's measured level, without lease charges, is `--json` `weekly_measured_pct`), and each
pass logs the weekly reading and what the meter adds to it in `passes.jsonl` (`l7_read`,
`l7_read_age_s`, `l7_meter`), and the same for the 5-hour level from the same reading (`l5_read`,
`l5_read_age_s`, `l5_meter`; `l5 = l5_read + l5_meter`) with `l5_leases`, the lease charges the
picker adds on top: `--explain`'s 5-hour level is `l5 + l5_leases` (live: claude-4 at 35.3 for the
switch and 47 in `--explain`, the difference its young lanes' charges). Such an account is skipped like a hard limit ("the switch's projection is 97%, over
its cap 95% (it would move a session off)" in `--explain`). Off, the picker is as before.

**Simulator, 1.3.3-pre3 against this release** (`internal/mover/sim_v3_test.go`; every pattern
plus *remote-integer*: four accounts carry 2-9 points per hour of the owner's sessions on
another machine, the hub measures two of them, readings are whole percents; all with the 5
pinned coordinators at 10/5; 10 patterns × 300 runs × 2 days; per day per run; a *false move*
is a move whose source was under its caps at its true burn):

| Strategy | False moves (remote-integer) | Moves | All stopped min | Pinned stopped min | Protected min over 80 | Re-read Mtok |
| --- | --- | --- | --- | --- | --- | --- |
| 1.3.3-pre3 | 10.8 (3.47) | 39.6 | 905 | 276 | 522 | 15.2 |
| + readings fix | 4.33 (1.69) | 32.9 | 905 | 278 | 516 | 12.6 |
| + picker uses the switch's projection | 4.32 (1.73) | 31.7 | 899 | 279 | 515 | 12.4 |
| + destination horizon 90 | 3.94 (1.19) | 43.3 | 908 | 276 | 506 | 16.8 |
| + cheap moves | 4.37 (1.70) | 32.9 | 906 | 276 | 513 | 12.5 |
| **all (this release)** | **3.99 (1.13)** | 42.0 | 906 | 278 | 498 | 16.6 |
| all, horizon 60 | 4.02 (1.13) | 44.6 | 912 | 273 | 497 | 17.8 |

The readings fix removes 60% of the false moves (51% on remote-integer) and 17% of all moves,
at no cost in stopped minutes. The picker's check and cheap moves change little in this model
(each within 1%): most new lanes already go where the switch has room, and a cheap-first choice
only matters when several sessions would each fix an account. The destination horizon keeps
protected accounts lower (minutes over 80 -4%, highest 5-hour level 84.6 → 82.0) and stops the
pinned sessions as long as before (-0.7% pinned stopped minutes, +0.3% all stopped minutes,
inside the 5% the owner allowed), but moves 32% more (aggressive accounts with a little room
now take a short session), so 34% more re-read. It is retired: the trigger projects to the
resets, so a destination let in on 90 minutes was moved off again within the cooldown (live
pre7, 3b2611ed).

**Interactive sessions.** A session is *interactive* (a person uses it) when it is: a plain
terminal session; a `wy run` session; a tmux pane with Remote Control on; or a tmux pane that got
a prompt from its owner at any time in its life (a `UserPromptSubmit` that is not a peer message
(`peer_prompt`) and not a move note (`move_prompt`, a prompt starting "Watchyard moved this
session"); the first prompt of a lane leased by `wy account next`, or of a session started with
a prompt on its command line, is its task, not its owner). A session is an *agent*, which the
switch moves, when it is a background session (`--bg`, registry kind `bg`), a headless `claude
-p`, or a tmux pane that runs an agent lane: launched by `wy account next --exec` or a lane
script, with no owner prompt in its life. Interactive sessions are moved exactly like agents (the
same trigger and selection, mid-turn too); what differs is what the person sees:

| Kind | Where it goes on | The person sees |
| --- | --- | --- |
| tmux pane | resumed in the same pane under the new account (mid-turn: with the note as its prompt, so it carries on) | the message printed in the pane for 4 s before claude takes the screen, and again when claude ends (it stays on the pane's main screen) |
| `wy run` | relaunched in place in the same terminal by the wrapper | the message, printed by the wrapper before it relaunches |
| plain terminal (no tmux, no `wy run`) | SIGKILL, then resumed as a background session on the new account (`--resume <id> --bg "<note>"`) | the message written to the session's controlling terminal (its `/dev/pts/N`, from the process's standard input, output or error; only a terminal device of the same user) |

The message (commands from `account.move_launch`, else `env CLAUDE_CONFIG_DIR=<dir> claude`):

> [Watchyard] This session moved from claude-3 to claude-2 at 14:05 (claude-3's 5h would hit
> 100% at 14:40). It continues as a background agent on claude-2. See it: ~/bin/claude-launch 2
> agents. Continue it in this terminal: ~/bin/claude-launch 2 attach 5e55a0a0.

(In a tmux pane or `wy run` terminal: "It continues here under claude-2.") `attach`, not
`--resume`: a second `--resume` would run the session twice while the background one goes on. A
session with Remote Control gets one more sentence: "Remote Control comes back on by itself (it
was started with --remote-control); if it does not, turn it on again with /remote-control.", or,
when it was turned on in the session, "Remote Control is off now: turn it on again with
/remote-control (unless it comes on by itself under claude-2)." An optional owner grace
(`account.move_owner_grace_min`, default 0) leaves an interactive session alone for that many
minutes after the owner typed into it.

**Strategies compared** (`internal/mover` simulator; the table and verdict are in the PR and
`move-v2-sim.md`): M0 no moves; M5 the decision shipped in 1.3.3-pre (forecast hit before the
reset within 90 minutes, turn boundaries only, cold-cache and benefit rules, once per window;
kept frozen in `internal/mover/internal/m5` for the comparison only); V2 the defaults above; V2
with 5-hour caps 98 and 92; V2 without the spike filter; V2 that also moves idle sessions.
Patterns: lanes whose true burn is 0.3x-5x the estimate the picker placed them on; spiky helper
panels; long Opus lanes; a pile-up of 5 lanes on one expiring account, 2 of them hot; a
protected account with an interactive owner session and lanes; a weekly level near 95 with a hot
lane; two organisations (2 + 5 accounts) under each `move_orgs`; a laptop where only 3 of 7
accounts are logged in; readings lagging 1-10 minutes; noise. Invariants checked on every pass:
no move to an account not logged in, stale or its own; every moved interactive session gets its
message and runs on the new account; under an owner grace, none typed into within it moves; no idle
session with no burn moved; a destination's projections with the planned burn within its caps −
5; the cooldown, no-return and account rate limits; the same seed gives the same result.
Result (300 runs per pattern, per day per run): stopped lane-minutes M0 4,532, M5 381, V2 394;
limit hits 13.96, 7.25, 3.41; moves 0, 12.9, 43.8 (28.7 of them mid-turn); context re-read 0,
5.1, 16.8 Mtok; protected 5h max 71.8, 93.8, 85.8; interactive sessions moved 0, 0.18, 1.72 (each
with its message delivered, on the new account); moves to an account not logged in 0 in all.
Steps (tool calls of 2 s to 10 min, model gaps of 5-30 s): stopping sessions inside steps
(StepWait 0) repeats 18.8 steps a day, waiting between steps (3 min) 6.8, with slightly fewer
stopped minutes (394 against 409) and no session stopped by a limit while it waited. In the
protected-owner pattern the owner's own stopped minutes are 1.8 a day with V2; a 2-min owner
grace raises them to 3.8 and spares about 3% of owner moves: off. Caps 98 or 92 change little
(about 1%); without the spike filter 33% more moves; moving idle sessions only adds cost;
`move_orgs` any 489, same-first 515, same 995 stopped lane-minutes in the two-organisation
pattern. **Picker pairing:** with V2 the picker's forward hold at 100 (as in 1.3.2), no hold
while the switch is on, and a hold at 110 are within 0.5% (394, 396, 396 stopped lane-minutes):
the picker is unchanged.

**Mechanics, by kind.** Every move first checks the pid is still the same process, then stops
claude with SIGKILL (a clean exit would stop its background shells; they keep running and the
resumed session no longer tracks them), except a background session (below), waits up to 2 s
for it to be gone, carries what the new account lacks, then starts `claude --resume <id>` with
the session's own flags (cwd, `--model`, `--permission-mode`, `--settings`, `--mcp-config`,
`--plugin-dir`, `--add-dir`, `--remote-control` ...; without `--resume`/`-c`/`--session-id`/
`--fork-session`/`--bg-spare` and its first prompt) under the new account. Resume keeps the
session id. **Carried:** the transcript (`projects/<project>/<id>.jsonl` and its `<id>/` folder)
when the new folder's `projects/` is not the same folder (one that `wy account add` did not
link); and folder trust, for every kind but `-p`: when the old account trusts the cwd (or a
folder above it) and the new one does not, `projects["<cwd>"].hasTrustDialogAccepted` is set in
the new folder's `.claude.json` (that one value, through the same JSON editor as `wy setup`).
Without it a resumed interactive session waits at "Do you trust this folder?" (default "No,
exit") and `claude --bg` refuses with "Workspace not trusted" (e2e). The command is `account.move_launch` with `{dir}`,
`{n}` (the N of `~/.claude-N`) and `{label}` filled and the arguments appended (e.g.
`~/bin/claude-launch {n}`; a leading `~/` or `$HOME/` is expanded), else `env
CLAUDE_CONFIG_DIR=<dir> claude`; started processes get this environment without
`CLAUDE_CONFIG_DIR`, `CLAUDECODE` and `CLAUDE_CODE_*`. The arguments are the session's own
flags (all but the ones above: model, permission mode, `--settings`, `--mcp-config`,
`--plugin-dir`... ; a flag repeated with the same values is passed once) and the resume, so a
launcher that adds config flags of its own passes them twice. Such a launcher gets the token
`{launcher-config}` in the template (`~/bin/claude-launch {n} {launcher-config}`): the
session's `--settings`, `--mcp-config`, `--strict-mcp-config`, `--plugin-dir` and
`--setting-sources` are then left out and the launcher's are the only ones.

**Finding claude.** The timer runs with the service manager's PATH, which often lacks
`~/.local/bin`. Every `claude` the switch runs (`claude stop`, `claude agents`, the default
resume command) is `claude` when it is on PATH, else the first of `~/.local/bin/claude`,
`~/.claude/local/claude`, `/usr/local/bin/claude`, `/opt/homebrew/bin/claude` and the session's
own executable (a native install's `~/.local/share/claude/versions/<v>`; not node) that exists.
A launcher from `move_launch` finds claude itself. With the switch on, `wy doctor` warns when
the timer can find none of them.

| Kind | Detected by | Restart |
| --- | --- | --- |
| tmux | a tmux pane's process is the session's or above it | `remain-on-exit on`, SIGKILL, `respawn-pane -k -c <cwd> "bash -ic '<command>'"`, `remain-on-exit` unset |
| `wy run` | a `wy run` wrapper within 4 levels above it (`<state>/move/runs/<pid>.json`) | an order in `<state>/move/orders/<wrapper pid>.json`, SIGKILL; the wrapper starts the command in the same terminal |
| background | registry kind `bg` or `--bg` | `claude stop <job id>` under the old folder (the short id `claude agents --json` lists; the session id matches nothing) (its supervisor ends the job and keeps its background shells running), SIGKILL only if the process outlives 10 s (mid-turn 4 min: there `claude stop` ends the turn and the process exits on its own, and a killed job is restarted by its supervisor; Between steps, below), then `claude stop` again; then for 5 s no copy may run there (the supervisor restarts a job whose process just died: a copy is stopped again, then killed; one that stays blocks the move, nothing is resumed); then `<command> --resume <id> --bg "<note>"`, detached (`--bg` needs a prompt: the note is it), and for 60 s more any copy the old supervisor brings back is stopped (e2e v2: one came back 6 s after the resume); every pass for 30 min after a background move stops such a copy too ("stopped a copy of ... that the supervisor of ... restarted"). The resumed job shows in the new account's `claude agents` |
| headless | `-p`/`--print` | SIGKILL, then `<command> -p ... --resume <id> "<note>"`, detached, its output appended to `<state>/move/logs/<id>.log` (whoever read the old one's output does not get it) |
| plain terminal | none of the above | SIGKILL, then `<command> --resume <id> --bg "<note>"`, detached; the message is written to its terminal (Interactive, above) |
| claude.ai (`--sdk-url`) | its command line | not movable from the machine |

`wy move <session> --bg` restarts a tmux, `wy run` or plain-terminal session as a background
session instead (it then leaves its terminal). Remote Control: the session keeps its kind; the
resumed process is a new Remote Control session only if Remote Control comes on by itself
(settings) under the new account; otherwise the owner turns it on again with `/remote-control`,
and the claude.ai list shows the old entry as ended. Windows: not supported (no SIGKILL, no tmux).

**New and old sessions.** `wy run [--] claude ...` (and `wy account next --exec -- wy run claude
...`) makes a plain-terminal session movable in place: it runs claude as its child, records
itself in `<state>/move/runs/`, and when claude ends with a relaunch order waiting it starts the
ordered command, else exits with claude's code; Ctrl-C reaches claude only. `wy run
--install-shim <dir>` writes an opt-in `<dir>/claude` that runs the real claude through `wy run`
(put `<dir>` first on PATH; `--remove-shim` removes only a file it wrote). Sessions started
before the switch was turned on need nothing: they are movable as soon as their launch spec can
be rebuilt from the registry, the command line and tmux.

**Background lanes and the environment.** A `claude --bg` session runs in its account
supervisor's environment, not the launcher's (e2e: a variable set for `claude --bg` did not
reach the session's hooks): only `CLAUDE_CONFIG_DIR` (which supervisor) matters, so
`WY_FINISH_BY` and the like do not reach a background lane.

**The moved session.** It gets one line, once. **Mid-turn** (the usual case: the switch moves
the sessions that burn), in every kind the line is the resume's prompt, so the session goes on
by itself: background `--resume <id> --bg "<note>"`, headless `-p --resume <id> "<note>"`, an
agent tmux pane or `wy run` session `claude --resume <id> "<note>"` (a positional prompt starts
a turn). The hook marks such a prompt (it starts "Watchyard moved this session") as
`move_prompt`, so it never counts as the owner typing. The line:

> Watchyard moved this session from claude-3 to claude-2 mid-turn at 14:05: claude-3's 5h would
> hit 100% at 14:40 at its current burn (P5 104%). Your last step finished before the move.
> Helper agents that were running were stopped: restart them if you
> still need them. Background shells keep running (listed as stopped; check their output files).
> Carry on with your task.

The hit time (here, in the reason of the move log and the phone notice, and in the human's
message) is now + (100 − level)/R5 from the account's current level and burn, the sooner of
the two windows; it is left out when that is not after the pass (an account already at 100%;
live: "100% at 04:24" in a pass at 04:25).

Only when the organisation changed, "(different organisation: earlier thinking blocks are not
visible to you; the conversation is intact)" follows the reason (e2e: a cross-organisation
resume drops the other organisation's thinking blocks and goes on). A session moved while
**not** mid-turn (a stopped one, a move by hand) gets the line as SessionStart context instead:
`wy hook --peers-context SessionStart` prints it as `additionalContext` when
`<state>/move/notes/<session>.json` exists (written before the kill, dropped after an hour), with
peers on or off; it ends "Background shells started before keep running (Claude Code lists them
as stopped; they are not) but are no longer tracked here: check their output files." (A session
a limit stopped: "(stopped at the 5h limit, which resets at 13:59)".) A move by hand (`wy move
<session>`) is the owner's call and makes no projection claim: its note starts "This session was
moved by hand by the owner from claude-6 to claude-2 at 14:05", with the path and the
organisation note as above, and its pane or terminal message "[Watchyard] This session was moved
by hand by the owner from ...". The hook treats both starts as a move note, not the owner typing. A stopped background or headless session gets it as its prompt, ending
"Nothing to do about the move: carry on with your task, or stay idle if it was done.". From its
second move in 7 days the line carries the whole path once, "(move 3: claude-6 → claude-2 →
claude-5 → claude-3)", so the latest note alone tells the history and nothing stacks. Nothing
about the feature is added to any other session's SessionStart text.

**Between steps.** A mid-turn move does not stop a session inside a step (one tool call).
The plugin's step hooks (`wy hook --step` on `PreToolUse`, `PostToolUse` and
`PostToolUseFailure`, every tool; states.md 2) record each call's start and end in
`<state>/move/steps/<session>/<tool_use_id>`; a call started with `run_in_background` is not a
step (its shell keeps running after a move). Before it stops a mid-turn session, the pass waits
until no step is running, up to `move_step_wait_min` (default 3 min) or until one minute before
the projected hit, whichever is sooner, then stops it at once. The waits of one pass run side by
side and the moves follow one at a time, each as soon as its session is between steps; a pass
waits at most `move_pass_wait_s` (default 90 s) in all, and a session still inside its step then
is left for the next pass, which waits on (the wait counts from its first pass,
`<state>/move/waits/<session>`) until the 3 minutes in all are up (live: three moves waited in
turn and no pass ran for 6 minutes while another account went from P5 76 to 155). The note then says "Your last step
finished before the move."; a session still inside a step when the wait runs out is stopped
anyway, and its note names the step, "It was stopped inside a step (Bash: npm test) that may
still be running: check its result before redoing it." (the tool's command, path or pattern,
at most 80 characters, left out when the secret filter of peers.md matches it). A session with
no step hooks (an older plugin) is stopped as before, with "Your last step may not have finished:
check its result before redoing it." The wait runs in the pass, which `wy tick` starts detached, so
the tick is never held up. `<state>/move/pass.lock` is held only while a pass plans (one plans at
a time); each move holds its session's lock (`<state>/move/locks/<session>.lock`) while it waits
and runs, so the next pass runs and leaves that session alone ("a move of it is under way"), and
`wy move` by hand refuses it. `wy move --plan` is read-only and takes no lock: it tests a
session's lock with a shared, non-blocking probe of the existing file (it creates nothing), and
taking a session's lock retries for one second, so a probe never makes a move refuse. A
mid-turn `wy move --now` by hand waits for the step as a pass does, but at most
`move_pass_wait_s` (90 s; the owner is at the prompt: at the full 3 minutes each move by hand
took 3.5), and says so first ("inside a step (Bash): waiting up to 1m30s"). The timer's
systemd service has `KillMode=process`: with the default (`control-group`) systemd kills the
detached pass when `wy tick` exits, and no timer pass ever finishes (live run, 1.3.3-pre3). A
unit an older wy wrote is rewritten by the next `wy update`; `wy doctor` warns about one without
it (a drop-in `watchyard-flush.service.d/*.conf` with `KillMode=process` counts).

Why: SIGKILL ends claude, not the foreground command of its running tool call, which finishes
on its own a little later; a resumed session that checks at once may not see its result and runs
the step again (e2e v2: one step logged twice in 3 of 4 mid-turn moves). With the wait, every
step of the e2e (agent tmux, headless, interactive tmux, background; 60 s Bash steps) ran
exactly once; the simulator (300 runs of 9 patterns) puts repeated steps at 6.8 a day against 18.8
when the switch stops sessions inside steps, with no more stopped minutes or limit hits.

A background session is never killed first: its old account's supervisor restarts a job whose
process died, and the copy redoes steps (e2e v2: one step ran four times). The move runs
`claude stop` (it ends the turn; between steps nothing is lost) and waits for the process to exit,
which takes seconds between steps and one to three minutes inside one, up to 4 minutes; only a
process that outlives that is killed, and the job stopped again.

**Who is told.** *The session that started it* (its parent): `wy account next` records the
calling session's id on the lease (`parent`: `--parent <id>`, else the nearest Claude Code
session above the process), and a move copies it to the new lease. After each pass (or a move
by hand) every parent gets one Watchyard notice (peers.md 8.7) for all its lanes that moved,
once the moved sessions show again under their new handles (at most 30 s). Only that
recorded parent is told: a lane without one tells nobody, and no other session ever gets the
notice:

> Your lane lane-a (67ce5986) moved claude-3 -> claude-2 at 12:11 (5h would have hit at 12:23);
> its handle is now lane-a@claude-2.box (the old handle lane-a@claude-3.box still works). Its
> conversation goes on; messages sent to it before the move still reach it.

Several lanes: "N of the sessions you started moved to other accounts:" and one such line each.
No polling on the parent's side; it needs peers on (else the line "parent not told: peers are
off" is printed). *Callers*: a send to an old handle is delivered to the session and the send
line says so (peers.md 4.3). *Everyone*: `wy peers list` shows the current handle with the old
ones as aliases (`moved_from`, the last 7 days of moves), and the hub keeps each move
(identity.md 5.3).

**Testing (`WY_MOVE_TEST`).** A test-only fault injection, compiled in but refused while the
scope is `all`: JSON (or `@<file>`) with `accounts` by label (`five_hour`, `five_reset_min`,
`weekly`, `weekly_reset_min`, `rate`: the account's 5-hour points per hour), `rates` by session
id or 8+ character prefix (5-hour points per hour; a session with a rate burns, so it is a
candidate), and the rule settings `cooldown_min`, `no_return_min`, `account_window_min`,
`account_moves`, `owner_grace_min`, `spike_passes`. Injected accounts are never stale.
`wy move --plan` warns while it is set.

**Notices and digest.** Each automatic move is pushed to the owner's phone: one line, "<session
name or short id> <from> -> <to>: <reason>". With a hub (`hub.url` set) it goes through the hub's
alert pipeline: the machine writes a `switch_notice` record (record.md 4.12) and flushes it, and
the hub makes a `session_moved` alert sent to every enabled target (the Android app, Web Push,
ntfy, desktop) and shown on the board and the phone like other alerts, with its own rule and a
per-device mute (push.md 3.9). Direct push (push.md 7, machines.md 5) is the fallback: when the
hub has not answered for direct push's `after` (default 2 minutes, condition 4) or there is no
hub, and `wy direct-push on` has a topic, the machine pushes it itself (in private mode only how
many sessions moved), and the record it still writes says `direct` so the hub does not push it
again. With neither a hub nor direct push, moves wait (see below). A machine pushes at most once
per 5 minutes; moves inside that window wait in
`<state>/move/notify.json` and go out as one push ("3 moves: a x->y; b ..."). A move that could
not be pushed for an hour is dropped. `wy tick` does this, so a move is pushed within a minute of
its pass. Pulls into a draining account (reason `pulled in: …`, Owner drain) go out as their own
batch, trigger `drain_pulled` ("Pulled 2 sessions into a draining account", the Drain channel),
apart from the other moves of the same window (`session_moved`, the Account switch channel).
`account.move_notify` (default `on`) turns it off; moves made while it is off are never
pushed later. `wy move --digest [--since 24h] [--json]` prints a digest of the period: the moves,
the limit hits (sessions stopped by a `StopFailure` `rate_limit` hook event), each account's
projected weekly room at its reset, the minutes each protected account was projected over its
cap, and the moves that may not have been needed (the plan used the readings over the meter, or
the source was back under its cap within 15 minutes with its burn not lower). The last three come
from `<state>/move/passes.jsonl`, one line per auto pass (trimmed to 3 days); without it the
digest says so. `wy tick` pushes the digest once a day from 09:00 local (missed: until 12:00),
when `account.move_digest` (default `on`) is on and the switch is on or moved something in the
last 24 hours, the same way as the moves: a `switch_notice` record for a `switch_digest` alert,
direct push only as the fallback.

**Switch log.** Every move, automatic or by hand, failed ones included, is also sent to the hub
as a `switch_move` record (record.md 4.13) when `hub.url` is set, so the owner sees all moves of
all machines in one place: the board's **Switches** page (`#/switches`, pwa.md 3). The record
carries the time, session id and name, how it ran (`tmux`, `wy-run`, `terminal`, `background`,
`headless`; the first three are interactive and told whoever was at the terminal), the labels of
the account it left and the one it went to, automatic or by hand, the reason, the window that
triggered it, the projections of the account it left (`p5`, `p7`, caps, `burn5`; none for a move
by hand), whether the destination is another organisation, and the error when the move failed.
The hub keeps it in `switch_move` (schema v24) and numbers each session's moves that worked
(move 1, 2, ...) when it reads. `GET /v1/switches` lists them newest first (filters `machine`,
`account` (the label it left or went to), `session`, `mode` auto or manual, `result` failed,
`since`, `until`; pages of up to 200) with a summary of the whole filter: moves that worked,
failed moves, automatic and by hand, sessions moved, sessions moved 2 or more times, and moves
out of and into each account. The page shows the summary strip above the rows, each row in
words (time, `from -> to`, automatic or by hand, the session as a link to its page, machine,
how it ran, move number, reason, projections, result or error), and filters for machine, account,
automatic or by hand, failed only and dates (last 24 hours, 7 days, 30 days, all, or from/to
days), 25 rows a page. A session's page shows its path through accounts with the time of each
move. A machine that does not send the record (older than this release) shows nothing there; its
own log is `wy move --list`. On its first tick after the update the machine also sends the moves of the last 14 days from
`moves.jsonl` (older lines have no projections, window or reason: the board shows them without
those numbers); `<state>/move/hub_sent.json` is the high-water mark, so later ticks send only
what is new, and the hub keeps one row per machine, session and time.

**Records.** `<state>/move/alerts.json`: the pin alerts (`at`, `session`, `name`, `account`,
`window`, `hit_at`, `reset_at`, `rate`, `command`), each kept until its window resets.
`<state>/move/moves.jsonl`: `{"at", "session_id", "from", "to", "from_dir", "to_dir",
"kind", "auto", "reason", "argv", "error", "resumed_as", "warn"}` per move (failed ones with
`error`; `name` is the session's name when it has one). A background move waits up to 30 s for
its `claude --bg` launch to end (output in `<state>/move/logs/<session>.log`); a non-zero exit is
the move's `error`. It then looks up to 15 s for the resumed session in the new account's
registry: `resumed_as` is its session id when Claude Code resumed it under a new id (a fork:
the transcript and job go on under that id), `warn` says none was seen. The digest shows both
and lists the failed moves. A session recorded with `resumed_as` is one session across the fork: its cooldown, no-return, move count and "moved sessions keep their age" follow the new id. `<state>/move/notify.json`: the push cursor and the moves waiting
for the next push. `<state>/move/digest.stamp`: the day the digest was last sent.
`<state>/move/passes.jsonl`: each auto pass's projections per account (`at`, `accounts`: `key`,
`label`, `protected`, `l5`, `l7`, `p5`, `p7`, `cap5`, `cap7`, `r5`, `reset7_s`, `window`,
`note`). The lease
ledger (8.5): an `ended` line for the session's lease, then a `lease` line on the new account
with `strategy` `move` and `moved_from` (the old account's label), and `launched`. The hub and
`wy status` follow the session to the new account from its next hook event (identity.md 5.3).

**Commands.** `wy move --plan [--json]`: each account's forecast (with its pin reserve: `PIN
RESERVE` "1 pinned: 15/3", CAPS already less it; JSON `Pins`, `Reserve5`, `Reserve7`), the pin
alerts (JSON `alerts`), the moves a pass would make
(with the steps for a session it cannot restart) and, for an account that hits, each session it
leaves and why ("moved 4m ago (cooldown)", "no logged-in account on this machine has room for
its burn", "the owner typed into it 40s ago (owner grace)" ...; JSON `skipped`); an interactive
move says how it goes on ("interactive: goes on in place; it gets a message there", "interactive:
goes on as a background session; its terminal gets a message"). `wy move <session>
[<account>] [--dry-run] [--bg] [--now] [--force]`: one move by hand (the session by id, id
prefix of at least 4 characters or name; the account by label, folder or number, else the
decision's destination); a session in the middle of a turn is refused without `--now`, an
account at 98% or more of a window or a session pinned in Claude Code without `--force`, a
session outside the scope always. Flags may stand anywhere (`wy move 2abbe6d1 claude-3
--dry-run` is a dry run); a flag-like word left unread (after `--`) or an unknown flag stops the
command before anything happens. Never a second copy: a session with two live processes (the old
one and a resumed copy) is refused, by hand and by the switch ("2 copies of it are running: stop
the extra one first"), and so is a by-hand move of a session moved in the last 5 minutes (its
resumed copy may not be registered yet; `--force` overrides). `wy move --auto`: one pass, only when
`account.smart_switch` is on. `wy move --list [--since 24h] [--json]`: the moves of the last day, or of `--since` (a duration such as `12h` or whole days such as `7d`), failed ones marked `FAILED: <error>` (accounts by label), read from this machine's `moves.jsonl`.
`wy move --digest [--since 24h] [--json]`: the digest of the period (see Notices and digest).
`wy move --selftest`: the self-test, below.

**Self-test and timer checks.** The pass runs from the timer's environment (a systemd user
service or a LaunchAgent), which is not the shell's: its PATH often lacks `~/.local/bin`, and its
default KillMode kills a pass `wy tick` started in the background. `wy move --selftest` runs
`wy move --plan` (never `--auto`: nothing moves) the way the timer would. Linux with the unit
installed: `systemd-run --user --wait --pipe --quiet --collect -p KillMode=process
--setenv=NAME=value ... <wy from ExecStart> move --plan`, with the unit's and its drop-ins'
`Environment=` values (PATH, the `WY_*` folders; `%h` expanded). Without `systemd-run` (or not
Linux, or no user manager) it runs the same command directly with a cleared environment holding
only those values and HOME, and says so. It prints the environment used (unit, wy, PATH,
KillMode), the plan, then a verdict: whether claude is found (on the timer's PATH, else the
`~/.local/bin` fallback), whether the program of `account.move_launch` is found on that PATH, and
the exit status; exit 1 when any of them is wrong. Needs the timer installed (`wy setup`).
`wy doctor` checks the same things without running anything: KillMode=process (systemd), claude
findable from the timer's PATH (switch on), the `account.move_launch` program an executable file
on that PATH or absolute (switch on), and, for a folder whose settings carry `wy hook` by hand
(not through the plugin), the step hooks `wy hook --step` on `PreToolUse` and `PostToolUse`
(switch on; "Between steps", 8.6) as a `mover-hooks` warning.

**`wy burn [--minutes N] [--json] [--no-hub]`: who is burning.** Per account: its 5-hour and
weekly levels (as the picker reads them), its 5-hour burn in points per hour and in token units
per minute, its sessions, and when its 5-hour window hits at that burn. Per live session
(registry, any account): units per minute and 5-hour points per hour over the last N minutes
(default 15; weighted token units, its helper agents' rows included), account (with "from X"
after a move), id, name, kind, state, helpers seen, calls per model, folder. Ranked by burn.
The mover's forecast uses these same per-session numbers, over `account.move_burn_min` (10 minutes). Exit codes: 0
done, 4 no account can take it, 2 usage, 1 any other error (a kind it cannot restart, after
printing the steps).

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

[account]                   # Smart Account Switch (8.6)
smart_switch = false        # on: wy tick moves sessions predicted to hit a limit before its reset
move_launch = ""            # command a moved session starts with ({dir}, {n}, {label}); "": env CLAUDE_CONFIG_DIR={dir} claude
                            # {launcher-config}: the launcher adds the config flags; the session's are left out
move_notify = true          # push each automatic move to the phone, at most one push per 5 minutes (8.6)
move_digest = true          # push a daily digest at 09:00 local (8.6)
move_orgs = "any"           # any | same-first | same: may a session move to another organisation
move_cap_5h = 95            # every number of the decision (8.6, "Decision table") is a key:
move_cap_7d = 98            #   move_cap_5h_protected 80, move_cap_7d_protected 80, move_hysteresis 5,
                            #   move_burn_min 10, move_week_span_h 5, move_spike_passes 2,
                            #   move_spike_near_min 30, move_cooldown_min 20, move_no_return_min 60,
                            #   move_account_moves 5, move_account_burn 40, move_account_window_min 15,
                            #   move_stopped_wait_min 20, move_owner_grace_min 0,
                            #   move_step_wait_min 3, move_pass_wait_s 90
move_dest_horizon_min = 0   # retired, ignored: a destination must fit as the trigger projects it (to the resets)
move_expiring_h = 3         # within this many hours of a protected account's weekly reset its weekly cap is move_cap_7d (expiring room); 0: never
move_stale_min = 10         # an account whose reading is older is neither a source nor a destination; the picker ranks it last (stale data); 0: off
move_young_min = 15         # a session's burn counts for the switch once it is this old (its first minutes burn hot); 0: at once
move_min_burn = 1           # a session is moved only when its own burn is at least this many points/h (busy alone is not enough)
move_pull_min_burn = 3      # an owner-drained account pulls in only sessions burning at least this many points/h
move_pull_margin = 10       # a pull keeps the drained account this many points under its caps until its resets
move_pull_quiet_min = 15    # no pull within this many minutes after a move off the drained account
move_drain_dest_margin = 2  # a move into a drained account leaves it this many points under its caps (a filled drain does not re-trigger on drift)
drain_slack = 2             # aggressive: drain when the hours to the weekly reset are within max(24, this × need)
drain_slack_protected = 1.5 # protected: drain when within this × need (no floor)
drain_aim_7d = 96           # a draining account's far weekly aim (rises to 100 over the last 6 h before its reset); up to 100: fill the week now
drain_cap_max_7d = 99       # the most the switch's weekly cap for a draining account is raised to; up to 100
move_reset_skip_min = 10    # no 5-hour move when the projected 100% hit is within this many minutes of that window's reset; 0: off
pin_reserve_5h = 10         # points of the 5-hour window kept free per live pinned session (8.6)
pin_reserve_7d = 5          # points of the week kept free per live pinned session
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
