# Identity: machines, slots, seats

Status: spec v1. Implemented in `internal/identity` (client) and the hub's ingest
. This file says exactly how identity is derived.

Rule: **an account is a Claude login, never a position.** Folders, tabs and machines are
joined by the login they carry at a point in time.

## 1. Terms

| Term | Key | Made by |
| --- | --- | --- |
| Machine | `m_` + ULID | `wy`, on first run, on that machine. |
| Slot | (machine id, folder) | `wy`. The folder is written with `~` (`~/.claude-2`). |
| Account | account uuid | Claude (`oauthAccount.accountUuid`). |
| Org | org uuid | Claude (`oauthAccount.organizationUuid`). |
| Seat | (account uuid, org uuid) → hub id `seat_…` | Hub, on first record naming that pair. |
| Slot login | (slot, since) → seat, until | Hub, from records. |
| Web seat | (machine, tab id) → seat | Hub, from `web_usage`. |
| Session | Claude's `session_id` | Claude. |

## 2. Machine id

- On first run of any `wy` command that needs it, `wy` creates `<state>/machine.json`
  ([paths.md](paths.md)):

  ```json
  {"id": "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", "name": "laptop", "created": "2026-10-11T13:59:00.000Z"}
  ```

  `id` is `m_` + a new ULID. `name` defaults to the short hostname (lower case, up to the
  first dot). The file is written atomically (temp file, fsync, rename) under
  `<state>/machine.lock` so two hook processes on a fresh machine cannot make two ids.
- The id is never derived from hardware (`/etc/machine-id`, MAC addresses): those are shared
  by cloned VMs and containers, and are personal data. A random id moves with the state
  folder, which is what we want (`wy hub export` / reinstall keep it; a fresh state folder is
  a new machine).
- WSL has its own state folder, so it is its own machine.
- `wy machine rename <name>` changes `name` and sends a `slot_info` and a `machine_info`
  carrying the new `name` (record.md 4.8), so the hub follows with the next flush.
- The hub creates the `machine` row on the first accepted record (or at
  `wy hub token create --machine`).

## 3. Slots (config folders)

A slot is one Claude Code config folder on one machine.

**Finding the folder for a hook or status-line call.** `CLAUDE_CONFIG_DIR` if set and
non-empty, else `~/.claude`. The path is cleaned (`filepath.Clean`), not resolved through
symlinks (the folder the user chose is the identity; `real_path` is sent separately in
`slot_info`).

**Writing it.** If the path is inside the home folder it is written as `~/` + the rest, with
`/` separators on every OS. Otherwise it is written absolute. This keeps slots readable and
stable when the home folder differs between machines.

**Finding all folders** (for `wy agents`, `wy scan`, `wy doctor`): `~/.claude` and every
directory directly in the home folder whose name matches `^\.claude([-_].+)?$` (the
rule `wy` uses everywhere), plus folders listed in `config.toml` (`slots.extra`). A folder counts
only if it is a directory and contains `settings.json`, `.claude.json`, `projects/` or
`sessions/`.

**The state file.** The default-slot rule applies **only when `CLAUDE_CONFIG_DIR` is unset**
(or empty): then `claude` uses `~/.claude` and its state file is `~/.claude.json`. Whenever
the variable is set, even to `~/.claude` itself (or a link to it), Claude Code reads
`<folder>/.claude.json`, so a hook or status line running with the variable set to `~/.claude`
uses `~/.claude/.claude.json` (`testdata/README.md`). For any other folder it is always
`<folder>/.claude.json`. This is Claude Code's behaviour as of 2.1.296.

So `~/.claude` has two views that can hold different logins: the **default view**
(variable unset, state `~/.claude.json`, `Slot.Default`) and the **explicit view** (variable
set to it, state `~/.claude/.claude.json`, `Slot.Explicit`). Both are sent as slot
`~/.claude` (same folder, same sessions and transcripts), but the login cache keeps one
entry per view (`<slot hash>` is taken over `~/.claude#explicit` for the explicit view),
`claude auth status` for the explicit view keeps the variable set, and `wy agents` runs
`claude agents` in both views for `~/.claude` and merges by session id.

## 4. Reading the login, never a token

`wy` reads exactly two things, and only these keys are used to build `seat`:

1. **`oauthAccount` in the state file** (fast path, used by every hook):
   - Read the file, decode JSON, take the `oauthAccount` object.
   - `seat.account` = `accountUuid`, `seat.org` = `organizationUuid`,
     `seat.email` = `emailAddress`, `seat.src` = `claude_json`.
   - The state file is large (per-project data, MCP servers). `wy hook` must stay under 20 ms,
     so the identity package caches the parsed `oauthAccount` in
     `<state>/identity/<slot hash>.json` together with the state file's size and mtime,
     and rereads the state file only when either changes. `<slot hash>` = first 16 hex
     characters of SHA-256 of the slot string.
   - The state file is opened read-only and never written. Claude Code rewrites it
     atomically, so a read sees either the old or the new file.
   - `oauthAccount` holds no tokens. It is still passed through the record.md rule 4 key
     filter when sent whole in `slot_info`.
2. **`claude auth status --json`** (slow path, about 0.5–1 s): run with
   `CLAUDE_CONFIG_DIR` set to the slot (unset only for the default view of `~/.claude`), a 10-second timeout, and
   stdin closed. Used:
   - at SessionStart, in the background after the hook record is written (never inside the
     20 ms budget), refreshing the cache;
   - by `wy doctor`, `wy connect` and `slot_info`;
   - on macOS whenever `oauthAccount` is missing from the state file (the login itself
     lives in the Keychain).
   Fields used: `loggedIn`, `email`, `orgId`, `orgName`, `subscriptionType`. **Uncertain:**
   `auth status` does not print the account uuid (keys seen in 2.1.296: `loggedIn`,
   `authMethod`, `apiProvider`, `analyticsDisabled`, `projectsDirectory`,
   `configDirectory`, `email`, `orgId`, `orgName`, `subscriptionType`). So the account
   uuid comes only from `oauthAccount`. If only `auth status` is available, `seat` is sent
   with `account` = `""`, `email`, `org` = `orgId`, `src` = `auth_status`, and the hub joins
   it by (email, org) to an existing seat, or keeps the record unattributed until a record
   with an account uuid arrives.

`internal/identity` enforces this in its file-system layer (`OSFS` returns `ErrForbidden`
for those names) and a test records every open to prove it.

`wy` never opens `.credentials.json`, the macOS Keychain, `sessions/*.key`, or any file
under `daemon/`, and never runs `claude` with arguments other than `auth status --json`,
`agents --json` and `--version`.

Peers adds one allowance: `wy agent` and `wy peers` read each slot's session registry
files `<config>/sessions/<digits>.json` (pid, session id, cwd, socket path, name, status;
[peers.md](peers.md) 3.1). The file-system layer allows exactly that name pattern in
`sessions/` and still returns `ErrForbidden` for `sessions/*.key` and anything else there.

## 5. How the hub joins records

1. **Seat.** On a record with `seat.account` and `seat.org`: upsert `account` and `org`,
   upsert `seat` on `(account_uuid, org_uuid)`. Two different account uuids are never
   merged, even with the same email.
   **Stray free seat.** A seat whose org plan is `free` is listed as hidden (like `seat.hidden`,
   in the board, the phone, widgets and alert names; `include_hidden` still returns it with
   `hidden: true`) while all of these hold: it has no label, no `slot_login` ever named it, and
   its account has another seat whose org is not free. Every claude.ai account has a personal
   free org beside its Team org, and older desktop apps made a seat for it. A label, or a slot
   logged in to it (Claude Code really uses that org), shows it again. Nothing is deleted.
2. **Slot login.** For records with `slot`, find the slot's open `slot_login` period.
   - If none, open one at the record's `ts` with this seat.
   - If its seat equals this record's seat, nothing changes.
   - If it differs, close it at this record's `ts` (`until_at`) and open a new one. A record
     without `seat` from a slot that `slot_info` reports as logged out opens a period with
     `seat_id` NULL.
   - Late records (`ts` older than the open period's `since_at`) do not open or close
     periods; they are attributed by looking up the period that contains their `ts`.
3. **Session.** A session takes the seat of the first record for it that names a seat, and
   keeps it for life, even if the folder is re-logged later. Its slot and
   machine are those of its first record.
4. **Token rows** take the seat of their session. Rows whose session has no seat are
   attributed later, when a hook event for that session arrives, or by the history
   importer from `session_dirs`-style evidence; otherwise they stay Unattributed
   (`seat_id` NULL).
5. **Web seat.** A `web_usage` record joins `(machine, tab_id)` to its seat. That is how web
   usage and Claude Code tokens land on the same card.
6. **Labels and colours** belong to the seat (`seat.label`, `seat.colour`), set in the UI or
   by `wy account label`. Folder names stay a secondary tag ("laptop · claude-2").

## 6. What `wy` remembers locally

| File | Holds | Rewritten |
| --- | --- | --- |
| `<state>/machine.json` | machine id, name | Once; on rename. |
| `<state>/identity/<slot hash>.json` | `{slot, state_file, size, mtime_ns, oauth: {accountUuid, organizationUuid, emailAddress}, auth_status_at, logged_in, auth: {loggedIn, email, orgId, orgName, subscriptionType}}` (`auth` is the last `claude auth status` result; it is what makes the `src = auth_status` seat possible when `oauth` is missing) | When the state file changes. |
| `<state>/identity/sent.json` | Per slot, what the last `slot_info` from a hook said: seat (account, org, email), `logged_in`, label, colour, and when | When a login, label or colour change is sent, and on the daily resend (record.md 4.7). |

Nothing in these files is secret; they hold uuids and emails.
