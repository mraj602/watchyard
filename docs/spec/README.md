# Spec

The contract the implementation is built on. A change to the record format, the API or the schema
goes here first, before any code..

| File | What it fixes |
| --- | --- |
| [record.md](record.md) | Upload batch, record envelope, the seven record kinds, content rules. |
| [schema.sql](schema.sql) | The hub's SQLite schema (version 12; the header lists what each version added). |
| [states.md](states.md) | Session state machine, question detection, `gone` and timeouts. |
| [api.yaml](api.yaml) | OpenAPI 3.1 for ingest, the read API, the live stream and reserved paths. |
| [tokens.md](tokens.md) | Token format, hashing, scopes, CLI, pairing codes. |
| [identity.md](identity.md) | Machines, slots, seats, slot logins; how the login is read. |
| [outbox.md](outbox.md) | The local outbox: files, concurrent writers, cursor, cap, crash safety. |
| [paths.md](paths.md) | Config, state and data folders per OS; `config.toml`. |
| [pairing.md](pairing.md) | Pairing codes and QR, claim, rate limits, owner sessions, devices and revoke. |
| [push.md](push.md) | Alert rules, scheduler, delivery, Web Push (VAPID, RFC 8291), ntfy, action tokens, schema v3; section 12 is a tombstone for the removed Expo channel (schema v11); the link channel, widget refresh frames, schema v8 (section 13) and the Android `wy-link` module (section 14). |
| [pwa.md](pwa.md) | The installable web app: manifest, icons, service worker, offline, install and notification flows. |
| [setup.md](setup.md) | Install scripts, `wy setup`, sharing, plugin install, backups and undo, accounts, uninstall. |
| [machines.md](machines.md) | `wy machine add`, reach modes (direct, SSH forward, hub pull) and the drain protocol, direct ntfy, the Machines screen, versions and `wy update`, hub export/import, schema v4. |
| [desktop.md](desktop.md) | The desktop app (`app/`): folders, security baseline, IPC allow-list, `wy desktop`, web usage reading, hub connection and view, shell, tray, notifications, schema v5. |
| [terminal.md](terminal.md) | The session board in any terminal (`wy board`), the board view model and token, the Claude Code pane mod, tab titles, click to focus, Warp tab configs and plugin, `wy ssh`, `wy term setup`. |
| [phone.md](phone.md) | The phone app (`phone/`, Expo): layout and pinned versions, pairing, keychain token and hub client, screens and routes, polling, notifications (channels, categories, responses, link frames), deep links, widgets, the Live Activity, build config, test seams, toolchain facts; Settings → Notifications and the updater. |
| [analytics.md](analytics.md) | `GET /v1/analytics/{report}`: tokens, API-equivalent cost, models, projects, heatmap, sessions, limit windows and hits; days in the hub time zone; the web board and phone Analytics screens. |
| [keys.md](keys.md) | Owner key (passphrase-sealed, recovery code), hub signing key, device keys and certificates, signed documents, revocations, QR seal, the signing guard, schema v7. |
| [link.md](link.md) | `/v1/link`: challenge login, frames, sealing and signatures, the per-device durable queue, long-poll fallback, close codes. |
| [peers.md](peers.md) | `wy agent`, the session directory, `wy peers`, inbox delivery, session tags, the account matrix, threads and the loop guard, limits, audit, kill switch, the skill, the lab experiments, schema v9 (tags v16). |

Checks: `make spec-check` loads `schema.sql` into a temporary SQLite database and validates
`api.yaml` as OpenAPI 3.1. CI runs the same checks.

Implementation notes that apply everywhere:

- Go 1.27, standard library first. SQLite through `modernc.org/sqlite` (pure Go, no cgo), so
  `wy` stays one static binary for Linux, macOS and Windows.
- Times in the text output of `wy` commands (`wy analytics`, `wy hub pull list`, `wy agent
  status`, `wy peers log` and `audit`, `wy direct-push status`, `wy hub token list`, ...) are
  `YYYY-MM-DD HH:MM <zone>` in the local time zone, the zone always written
  (`internal/localtime`); `wy analytics` uses the zone its range line names. `--json` output
  keeps RFC 3339 UTC (record.md 1).
- Words marked **uncertain** are behaviours of Claude Code or claude.ai that were not
  verified against a capture. Each one has a fallback, and the fixtures in `testdata/`
  should settle them.
