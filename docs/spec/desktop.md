# Desktop app

Status: spec v1. Implemented in `app/` (Electron) and `wy desktop` (`cmd/wy/desktop.go`,
`internal/desktop`). Unofficial; not affiliated with Anthropic.

The desktop app is the Electron app in `app/`. It holds one tab per claude.ai account, reads web usage into
`web_usage` records (record.md 4.3) through `wy`, and shows the hub board. Everything else is
done by `wy` and the hub.

Related: record.md 4.3 (`web_usage`), paths.md 2 (folders), api.yaml (`PATCH /v1/accounts/{seat}`,
`Seat.places[].stopped_reason`), schema.sql v5 (`seat.look_src`), tokens.md (desktop tokens),
pairing.md (desktop codes, owner links), machines.md 4.4 (admin channel).

## C1. Folders and identity

- User data: `<app.getPath('appData')>/watchyard-desktop`, i.e. `~/.config/watchyard-desktop`,
  `~/Library/Application Support/watchyard-desktop` or `%APPDATA%\watchyard-desktop`.
  It is overridden by `WY_APP_USER_DATA` (an absolute path) only when `!app.isPackaged`.
- Files the app owns there: `app-state.json`, `hub-token.bin`, `direct.json` (the hub address
  remembered without wy, this app's machine id and the certificate pins of C6),
  `direct-outbox.ndjson` and Chromium's own folders (`Partitions/…`). `app-state.json` is written to a temp file, fsynced and renamed, mode 0600:

  ```json
  {"v": 1,
   "tabs": [{"id": "t_9c1e5f0a7b2d4e68", "name": "work", "last_url": "https://claude.ai/new",
             "reading": "on", "warning_accepted": 1, "send_to_hub": true,
             "stopped": null, "seat": {"account": "3f1c…", "org": "9b20…"}},
            {"id": "t_2b7d0e41c9a35f68", "name": "claude-6", "last_url": "https://claude.ai/new",
             "reading": "never_asked", "warning_accepted": 0, "send_to_hub": true, "stopped": null,
             "seat": null,
             "expect": {"seat": "seat_01J…", "account": "5a2e…", "org": "c41b…", "label": "claude-6"}}],
   "settings": {"keep_running": true, "notifications": true, "start_hidden": false,
                "close_notice_shown": false}}
  ```

  `reading` is one of `"on"`, `"off"` or `"never_asked"`. `stopped` is `null` or
  `{"status": 429, "at": "…"}`. `warning_accepted` is the warning version that was accepted,
  or `0`.
- Tab id: `t_` + 16 lowercase hex characters from `crypto.randomBytes(8)`. Tab ids of the
  earlier 8-hex form (tab ids from earlier versions) are still accepted. Pattern:
  `^(t_[0-9a-f]{16}|[0-9a-f]{8})$`. Partition: `persist:tab-<tab id>`.
- `client` in records: `watchyard-desktop/<app version>`.
- Single-instance lock (per user-data folder). `--background` starts hidden. A second launch
  shows the window, or applies `--autostart=on|off` and exits.
- Run at login: on Linux, `~/.config/autostart/watchyard-desktop.desktop` and
  `~/.local/share/applications/watchyard-desktop.desktop`. On macOS and Windows,
  `app.setLoginItemSettings` with `--background`.
- Linux icon: the entries say `Icon=watchyard-desktop`. Writing them also copies the app icon to
  `~/.local/share/icons/hicolor/512x512/apps/watchyard-desktop.png`, and each start copies it when
  an entry exists and the icon does not (an AppImage installs no icon; GNOME on Wayland takes the
  window and notification icon from the entry).

## C2. Security baseline

Applied in `main/security.js` before any window exists. A unit test and `scripts/lint-security.mjs`
both enforce it.

- `app.enableSandbox()`. Every `BrowserWindow` and `WebContentsView` gets
  `{contextIsolation: true, sandbox: true, nodeIntegration: false, nodeIntegrationInWorker: false,
  webSecurity: true, allowRunningInsecureContent: false, webviewTag: false, spellcheck: false}`.
  Only the shell has a preload.
- `app.on('web-contents-created')`: `will-attach-webview` → `preventDefault`. A default
  `setWindowOpenHandler` denies everything; each view type then installs its own policy, which
  is narrower. `will-navigate`, `will-redirect` and `will-frame-navigate` (subframes) are
  guarded per view type:

  | View | May navigate to | `window.open` / other links |
  | --- | --- | --- |
  | Shell (`app://shell/`) | nothing (any navigation is prevented) | denied |
  | Account tab | `https://claude.ai`, `https://*.claude.ai`, sign-in hops (`anthropic.com`, `claude.com`, `challenges.cloudflare.com` and their subdomains), `https:` only | Allowed hosts load in the same tab. `https:` and `mailto:` go to `shell.openExternal`. Anything else is dropped. |
  | Hub view | The hub origin only (scheme + host + port from `wy desktop info`) | `https://claude.ai/code/session_…` opens in the account tab of the session's account (see C7), else in a new tab for that account; with no known seat, the first signed-in tab, else the system browser. Other `https:` links go to the system browser. |

- Subframes (`will-frame-navigate` with `isMainFrame` false, and `will-redirect` of a subframe):
  an iframe may load, navigate or be redirected only to its view's frame allow-list
  (`shared/urls.js` `tabFrameAllowed`, `hubFrameAllowed`); anything else is prevented and its
  host (never the full URL) is logged at debug level. `about:blank`, `about:srcdoc` and `blob:`
  URLs of an allowed origin are always allowed.

  | View | Frames may load |
  | --- | --- |
  | Account tab | `https:` only, no credentials in the URL, on `TAB_FRAME_HOSTS` or a subdomain: `claude.ai`, `anthropic.com`, `claude.com` (and `claudeusercontent.com`, where claude.ai renders artifacts), `challenges.cloudflare.com` (Turnstile), `js.stripe.com`, `hooks.stripe.com`, `m.stripe.network` (Stripe checkout and its 3-D Secure wrapper), `accounts.google.com` (Google sign-in), `hcaptcha.com` (the sign-in page's invisible hCaptcha check). In an unpackaged test run, also `WY_APP_TEST_CLAUDE_ORIGIN`. |
  | Hub view | The hub origin only |
  | Shell | nothing |

  A bank's own 3-D Secure page, which Stripe frames from the bank's domain, is therefore
  blocked: such a payment is done in a normal browser.

- Permissions are handled per session (`setPermissionRequestHandler`, `setPermissionCheckHandler`,
  `setDevicePermissionHandler` → deny, `setDisplayMediaRequestHandler` → deny):

  | Session | Allowed |
  | --- | --- |
  | Account tabs | `clipboard-sanitized-write`, `fullscreen`, `notifications`, `media` (audio only, for voice mode) |
  | Hub view | `clipboard-sanitized-write` |
  | Shell (default session) | none |

- Certificate errors are never overridden (no `certificate-error` handler). The one
  certificate decision the app makes is the hub pin of C6: `main/hub/pin.js` installs a
  `setCertificateVerifyProc` on the hub view's session `persist:wy-hub` only, and the lint
  allows that call in that file only. Account tabs, the shell and every other origin use
  Chromium's normal validation.
  Downloads from account tabs go through the default save dialog; downloads from the hub view
  and the shell are cancelled.
- The shell's CSP is sent as a response header by the `app://` handler:
  `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self';
  connect-src 'none'; media-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none';
  manifest-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`.
  The `<meta>` tag repeats it without `frame-ancestors 'none'`, which a meta tag cannot carry
  (Chromium ignores it there and logs a console error); `SHELL_CSP_META` is the meta text and
  `SHELL_CSP` the header.
  The response also carries `X-Content-Type-Options: nosniff` and
  `Cross-Origin-Opener-Policy: same-origin`. There is no inline script, no inline style, no
  `eval`, and no `innerHTML` with data (the lint checks this in `src/renderer/`).
- `app://` is registered with `protocol.registerSchemesAsPrivileged([{scheme: 'app',
  privileges: {standard: true, secure: true}}])`. `app://shell/<path>` serves only files under
  `src/renderer/` and `src/shared/`. The path is normalised and refused if it leaves those
  folders (403), and only an extension allow-list (`.html .js .css .png .svg .woff2`) is served.
  Mapping: `app://shell/shared/<p>` → `src/shared/<p>`, every other
  `app://shell/<p>` → `src/renderer/<p>`; the shell is `app://shell/index.html`, and the
  renderer imports shared code as `./shared/<file>.js`.
- `shell.openExternal` has exactly one caller, `openExternalSafe(url)` in `main/security.js`,
  which allows `https:` and `mailto:` only (the URL rules live in `shared/urls.js`). A
  `mailto:` URL is rebuilt with its addresses and only the `to`, `subject`, `cc` and `body`
  fields (no `attach=` or other client-specific fields).
- A page (the hub board or a claude.ai tab) can trigger external opens and session-link
  handoffs by itself, so each webContents has a budget of 3 per 10 seconds; more are dropped
  and logged. Opens a page triggered pass `activate: false` (the browser does not come to the
  front on macOS), and a handed-over session link selects its tab but never raises or focuses
  the window (`openLink(..., {quiet: true})`). A person's own click (Open in browser, the update
  notice) is not limited.
- The crash reporter is not started. No telemetry of any kind is sent.
- Packaged fuses: `RunAsNode` off, `EnableNodeOptionsEnvironmentVariable` off,
  `EnableNodeCliInspectArguments` off, `EnableEmbeddedAsarIntegrityValidation` on,
  `OnlyLoadAppFromAsar` on, `GrantFileProtocolExtraPrivileges` off, `EnableCookieEncryption`
  on (the tabs' sign-in cookies are encrypted on disk with the OS key store). The fuse is one
  way. A tab signed in by an earlier build may ask for a new sign-in; that is expected.
  On Linux without a keyring, Chromium falls back to a weak built-in key.
- A site that asks for a client certificate gets none: `applyGlobal` answers
  `select-client-certificate` with no certificate.
- Remote debugging (`main/cdp.js`): a packaged app started with `--remote-debugging-port`,
  `--remote-debugging-pipe` or `--remote-debugging-address` (in argv or on Chromium's command
  line) exits with code 1 before `ready`, unless its asar's `package.json` has
  `watchyard.allowCdp: true`. Only the e2e build sets that, with electron-builder
  `-c.extraMetadata.watchyard.allowCdp=true` (`scripts/dist-e2e.sh`, output `dist/e2e/`);
  release builds (`npm run dist`, CI's package job) never do. Unpackaged runs are not checked.
- Test-only overrides (`WY_APP_USER_DATA`, `WY_APP_TEST_CLAUDE_ORIGIN`, `WY_APP_TEST_CADENCE_MS`,
  `WY_BIN`) live in `main/test-hooks.js`. It is imported only when `!app.isPackaged`, and
  `electron-builder.yml` excludes it from the asar (a packaging test checks that it is absent).

## C3. IPC allow-list

`src/shared/ipc.js` is the one table: a channel, its argument schema (types, string lengths,
enums, patterns) and its direction. `src/preload/shell.cjs` exposes `window.wy` with exactly
one function per invoke channel and one `on(event, cb)` for the push channels. It never
exposes `ipcRenderer`, event objects or `require`. A unit test loads the preload in a `vm` with
a fake `electron` and checks that its channel list equals the table.

`main/ipc.js` registers every channel with `ipcMain.handle` (`ipcMain.on` and `sendSync` are
not used). Before calling a handler it checks the following.

1. `event.sender` is the shell's `webContents`, and `event.senderFrame` is its main frame with
   a URL starting `app://shell/`. Any other sender, including account tabs and the hub view,
   gets an error, and the attempt is logged once.
2. The arguments match the schema. Unknown keys are rejected, strings are capped, and ids
   must match the tab id pattern.
3. Handlers are registered by their owning module through `ipc.register(channel, fn)` from
   the start. Registering a channel that is not in the table throws at start-up.

| Channel | Args | Returns | Owner |
| --- | --- | --- | --- |
| `state:get` | — | `ShellState` | A |
| `view:select` | `{view: "hub" \| "settings" \| <tab id>}` | — | A |
| `hub:route` | `{route: string}` (must start `#/`, ≤ 200 chars) | — | C |
| `tab:add` | `{name: string ≤ 24, seat?: seat id}` | `{id}` | B |
| `tab:update` | `{id, name?}` | — | B |
| `tab:remove` | `{id}` (clears that partition's storage) | — | B |
| `tab:reorder` | `{ids: [id…]}` (account tab ids in the new display order; ids not listed keep their order after the listed ones, unknown ids are dropped) | — | B |
| `tab:reload` / `tab:home` / `tab:external` | `{id}` | — | B |
| `tab:adopt` | `{id}` (make the tab the account it is signed in as) | `{name, label, offer}` | B |
| `tab:logout` | `{id}` (load claude.ai's logout in the tab) | — | B |
| `usage:accept-warning` | `{id, version: int}` | — | B |
| `usage:set-reading` | `{id, on: bool}` | — | B |
| `usage:set-send` | `{id, on: bool}` | — | B |
| `usage:resume` | `{id}` | — | B |
| `usage:refresh` | `{id?}` | — | B |
| `hub:pair` | `{code: "^\d{3}-?\d{3}$"}` or `{}` (local or admin channel) | `{ok, error?}` | C |
| `hub:owner-sign-in` | `{}` (local hub or admin channel) or `{link}` (a pasted owner link to the paired hub's own origin) | `{ok, error?}` | C |
| `hub:owner-sign-out` | — | `{ok}` | C |
| `hub:connect` | `{url, code?}` (checks `/health`, remembers the address without wy, pairs with the code; an https hub whose certificate fails normal validation, or is not its pinned one, returns `cert` instead, see C6) | `{ok, error?, cert?}` | C |
| `hub:trust-cert` | `{url, fingerprint}` (C6: the hub must still present the certificate with this SHA-256 fingerprint; it is pinned with the hub config, or unpinned when it passes normal validation) | `{ok, pinned?, error?, cert?}` | C |
| `hub:unpair` | `{forget?: bool}` (`DELETE /v1/devices/self`, then the token and the board session are dropped) | `{ok, revoked, warning?}` | C |
| `hub:retry` | — | `{ok}` | C |
| `hub:service` | `{action: "status" \| "start" \| "stop"}` | `{service, health}` | C |
| `seat:update` | `{seat, label?, colour?}` (owner; `PATCH /v1/accounts/{seat}`) | `{ok, error?}` | C |
| `accounts:local` | — | `wy account list --json` result | C |
| `accounts:share` | `{folder, share: string}` | `{ok, error?}` | C |
| `settings:set` | `{keep_running?, notifications?, autostart?, start_hidden?}` | `Settings` | E |
| `app:open-release` | — (opens the release page of the update notice, C8, through `openExternalSafe`) | `{ok}` | C |
| push `state` | `ShellState` (debounced 100 ms) | | A (state is assembled from every module through `bus.js`) |
| push `toast` | `{level, text}` | | A |

`ShellState` holds the tabs (id, name, colour, label and seat id from the seat when one is known,
reading state, the local strip windows, last read, the stop reason), the selected view, the hub
(url, connected, owner, version, service state, needs-you count), settings and, for Settings, About,
the app, Electron and Chromium versions. It never holds
tokens or secrets. The field-level shapes (`ShellState`, `TabState`, `HubState`, `Settings`) are the JSDoc types at the end of `src/shared/ipc.js`.

Details fixed by `src/shared/ipc.js`:

- `window.wy` function names, in table order: `state`, `selectView`, `hubRoute`, `addTab`,
  `updateTab`, `removeTab`, `reorderTabs`, `reloadTab`, `homeTab`, `openTabExternal`, `adoptTab`, `logoutTab`,
  `acceptWarning`, `setReading`, `setSend`, `resumeReading`, `refreshUsage`, `pair`,
  `ownerSignIn`, `ownerSignOut`, `hubConnect`, `trustCert`, `hubUnpair`, `hubRetry`, `hubService`, `updateSeat`, `localAccounts`, `shareAccount`, `setSettings`, `openRelease`;
  plus `on(event, cb)`, which returns an unsubscribe function. Each
  function takes one argument object (or none, for the channels without arguments) and
  returns the handler's promise.
- Push channels go over the wire under their own names (`state`, `toast`).
- Caps beyond the table: a tab name is 1 to 24 characters; `tab:reorder` takes at most 100
  unique ids; `usage:accept-warning.version` is 1 to 1000; `hub:route` has no spaces or
  control characters; `seat:update.seat` matches `^seat_[0-9A-HJKMNP-TV-Z]{26}$` (a hub seat
  id), `label` and `colour` the `SeatPatch` patterns or `null`; `accounts:share` uses the C11
  patterns; `toast.text` is 1 to 500 characters. Only plain objects are accepted.

Reordering account tabs (shell tab bar). The Hub tab is always first and "+" always last: only
account tabs are draggable and drop targets, and the order sent is account tabs only. Three ways
to move a tab, all ending in `tab:reorder`: drag a tab and drop it on the left or right half of
another tab (a bar marks the insertion side); focus a tab and press Alt+Left or Alt+Right
(Alt+Shift+Left/Right works the same); or use the up/down buttons of a tab's row in Settings.
Main stores the order in `app-state.json` (the order of `tabs`), which is also the order of the
Ctrl+1..9 shortcuts and of the strip, so it survives restarts.

## C4. `wy desktop`

Every subcommand prints one JSON object on stdout with `--json`. Secrets travel only on stdout
and stdin, never in argv. Exit codes: 0 ok, 2 usage, 3 refused by the hub (expired code,
revoked), 4 not possible here (no local hub, no admin channel, wy not connected), 5 hub
unreachable. Any other failure (an admin command that cannot start, an argument the admin
channel refuses) is 1 with its own message.

```
wy desktop info --json
{"v": 1, "wy": "0.6.0", "machine": {"id": "m_…", "name": "laptop"},
 "hub": {"url": "http://127.0.0.1:8787", "connected": true, "reachable": true, "version": "0.6.0",
         "local": true, "dial": null, "service": "running"},
 "admin": "local", "admin_error": null, "outbox": {"pending": 3, "last_error": null}}

wy desktop token [--code-stdin] [--name N] --json
{"device": "dev_…", "token": "wy_d_…", "scopes": ["write", "read"], "hub": {"url": "…"}}

wy desktop owner-link --json
{"url": "https://hub…/#/owner?s=…", "expires_at": "…"}

wy desktop hub status|start|stop --json
{"service": "running" | "stopped" | "missing", "unit": "…", "health": true}
```

- `info`: `hub.reachable` is `GET /health` with an 8 s timeout and one retry (the hub is often a
  tiny VM that answers slowly now and then); `hub status` uses 5 s and the polls after
  `hub start|stop` 3 s. The app adds its own debounce: a hub that was reachable is shown as
  Unreachable only after two info checks in a row fail (the board stays meanwhile and the next
  check comes in 30 s, not 5 min). `hub.service` is `null` when the hub is not on this machine. `local` = `<data>/hub.db`
  exists and `hub.url` is a loopback URL. `admin` is the admin-channel resolution of
  machines.md 4.4 (flag none, `machines.hub_admin`, else `local` when `hub.db` exists, else
  `null`). `admin_error` is why `machines.hub_admin` cannot be used (a refused command prefix),
  else `null`; `info` and `hub` still work then, and only `token` and `owner-link` exit 4 with
  that message.
- `token`: without `--code-stdin`, it runs through the admin channel
  (`hub token create --kind desktop --machine <id> --name <N> --json`). With `--code-stdin`, it
  claims a desktop pairing code or secret read from stdin, with `expect_kind: desktop` and this
  machine's id (pairing.md 3, through `internal/client`'s claim). The token is printed once and
  never stored by `wy`.
- `owner-link`: the admin channel's `hub owner-link --json`. A command-prefix channel runs ssh
  with `-o BatchMode=yes`, so it never waits on a prompt hidden from the person.
- `hub start|stop`: the hub user service from setup.md 3 (`internal/service`, new `Start` and
  `Stop` next to `Status`). The exit code is 4 when the service is not installed; the app then
  shows `wy setup --hub here`.
- `wy desktop` refuses `hub.dial` setups for the app (`info` reports `dial`). The app shows
  "forward mode is not supported in the desktop app".

## C5. Web usage reading

**Reader.** `webContents.executeJavaScriptInIsolatedWorld(1001, [{code}])`, the reader
script, made to return the raw objects:
`{orgs: [{org (the six keys of record.md 4.3), usage | {__status}}], account: {uuid, email_address,
full_name, display_name} | {__status}, active_org, fetch_ms}`.

- If the isolated world throws (not an HTTP error), there is one retry in the main world, and
  `reader: "main"` is logged at debug level. The script never throws once it runs (a network
  error or a non-JSON reply is `{__status: 0}`), so that retry never repeats requests; a
  timeout is not retried. The main world is the page's own, so a page can forge its result: it
  is used only when the tab's seat account is known and the result's account uuid is that
  account. A tab with no seat yet does not run the main world at all (the read fails and is
  tried again). Each result is checked in main before use: shapes as above, total ≤ 512 KiB,
  uuids matching `^[0-9a-f-]{36}$`. Each org's `usage` must be plain JSON data: at most six
  levels deep, 64 entries per object or array, strings up to 256 characters, 32 KiB in all, no
  `__proto__`; otherwise the read is retried and nothing is forwarded.
- `active_org` is read in main from the `lastActiveOrg` cookie of the tab's own session.
- A tab is **readable** when the following all hold: `reading === "on"`, `warning_accepted >=
  WARNING_VERSION`, `stopped === null`, its URL is a claude.ai URL, and its path is not
  `/login` or `/logout`.

**Cadence.**

| Trigger | Rule |
| --- | --- |
| Periodic | Every 300 s per tab, first one offset by `index × 20 s`, ±15 s jitter |
| Tab opened | `did-finish-load` on claude.ai + 1.5 s |
| In-page navigation | 3 s debounce, only if the last read is older than 60 s |
| Manual | `usage:refresh` (still ≥ 10 s since the last read of that tab) |
| Floor | Never more than one read in flight per tab, never more than one per 60 s except manual |

`WY_APP_TEST_CADENCE_MS` (unpackaged only) replaces 300 s, 60 s and 10 s for the smoke test.

**Errors.**

| Outcome | Then |
| --- | --- |
| Every call 2xx | Reset the backoff; build the records; update the strip |
| Network error, 5xx, script error, bad shape | Back off 5, 10, 20, 40, then 60 min (cap), plus jitter; the strip says "retrying at 14:20" |
| 401 on any call (or a redirect to `/login`) | `stopped = {status}`; append one `web_usage` with `error: {status, message}` for each known seat of the tab (none if the seat is unknown); the strip says "Stopped: signed out (401). Resume" |
| 403 on `/api/organizations` or `/api/account` | The same stop with status 403; the strip says "Usage not available for this account (403)." (not "signed out": a 403 is a refusal, a 401 is a sign-out) |
| 403 on one org's `/usage` | That org is dropped from the read; the other orgs are read (a Team member belongs to several orgs, and not every one serves usage to a member). Only if every org 403s is it the stop above |
| 429 | The same, with "Stopped: rate limited (429). Resume" |
| `usage:resume` | Clear `stopped`, read now |
| `usage:set-reading {on: false}` | Clear the timers; mark any read in flight as cancelled, so its result is dropped before the outbox (a sequence number checked after `await`); nothing more is read or appended for that tab |

**Which orgs.** Every claude.ai account has a personal free org beside its Team or paid orgs,
and the `lastActiveOrg` cookie often points at it. A record makes a seat on the hub, so
reporting it made a second, free seat for the same person. The app reports (`chooseOrgs`,
`shared/usage.js`): every org that is not free when the account has one (Team, Enterprise, Max,
Pro; plan as `planOf`); when all its orgs are free, exactly one: the tab's own seat org if still
there, else the active org, else the one with the most windows. The org the strip and `tab.seat`
follow is the active one when reported, else the tab's seat org, else the one with the most
windows; so a tab whose seat was a stray free org moves to the paid one. A free org that is
really used in Claude Code gets its seat from the slot's own records, not from the app.

**Records.** One `web_usage` per org chosen above, exactly as in record.md
4.3:
`seat = {account, org, email, src: "claude_ai"}`, `payload = {tab_id, org: <six keys>, account:
<four keys>, active, usage: <raw, whole>, fetch_ms}`. `client`, `machine` (cached from
`wy machine show --json` at start) and `ts` (the read's start time) are set; `id` and `v` are
left to `wy outbox append`. A tab with `send_to_hub: false` updates its local strip only and
appends nothing.

**Publish.** Records are written as JSON lines to the stdin of `wy outbox append`, one process
per read. The returned ids are checked: their count must equal the number of records, else the
read is logged as a failure and not retried, because the outbox may hold some of them. Then
`wy flush --quiet` runs, debounced to 2 s and coalesced across tabs.

**Warning (versioned, `WARNING_VERSION = 1`).** It is shown as a dialog in the shell the first
time a tab could read (after sign-in), and again whenever the version is raised. The text is
final unless the maintainers change it:

> **Read usage for this account?**
> Every 5 minutes, Watchyard runs a small script inside this tab that reads the same numbers
> claude.ai shows under Settings → Usage. It reads numbers only: no chats, no cookies. They go
> to your own hub.
> This uses claude.ai’s own undocumented usage endpoint, not an official API, and nothing is
> requested until you allow it. It may stop working, and
> automated access may not be allowed by Anthropic's consumer terms, so it could get this
> account flagged. Watchyard is unofficial and not affiliated with Anthropic. Whether to turn this on is your choice.
> You can turn reading off for this account at any time in its usage bar or in Settings.
> **[Read usage for this account]  [Not now]**

"Not now" sets `reading: "off"`, and the tab works normally. The dialog is never shown for an
account in a hidden or background start until the window is shown.

## C6. Hub connection

- At start, run `wy desktop info --json`. With no `wy` (C11 resolution fails), or with `wy`
  not connected, the shell shows a banner with the exact command (`wy setup`), and the tabs
  still work. Reads still append to the outbox if `wy` exists, so they flush later.
- Desktop token: `hub-token.bin` = `safeStorage.encryptString(JSON.stringify({url, device, token}))`.
  If the URL in the file differs from `hub.url`, or the file decrypts to something that is not
  such a record, the token is dropped and pairing starts again. A file that cannot be decrypted
  (the keyring locked or not up yet) is kept and tried again later; pairing replaces it.
  Pairing (`hub:pair`): with `admin` set, it is one click. Otherwise the person types the code
  from the board (More → Devices → Add desktop app) or from
  `wy hub pair --kind desktop`.
- Requests use Node's `fetch` in main with `Authorization: Bearer`, a 10 s timeout and no
  redirects. Plain `http:` is allowed only for loopback hosts, or when `wy desktop info`
  reports a URL that wy itself accepted (`--insecure-lan`). Replies are capped at 2 MiB.
- A typed hub address (direct mode, `main/hub/direct.js`) may use `http:` only for loopback
  hosts and `.ts.net` names. A private-network address, `.local` or a single-label name needs
  the explicit `insecureLan` choice (the "Allow plain http" box, like `wy connect
  --insecure-lan`), which is stored with the address. A public host is refused even with it. A
  remembered address that fails this rule is dropped when the app starts.
- Plain `http:` to a host that is not loopback: `HubState.insecure` is true, and the shell
  shows, for as long as it stays so, "Not encrypted: …the hub token and everything the app
  exchanges with it cross the network unencrypted" in the Hub tab's top strip, in Settings →
  This computer, and under the Connect form while such an address is typed.
- Self-signed https (trust on first use). An https hub whose certificate fails normal
  validation is never used silently:
  - `hub:connect` first probes the address (`pin.probe`: a TLS handshake with validation off
    that sends nothing) and returns `{ok: false, error, cert}`, `cert` =
    `{url, origin, fingerprint, pinned, changed, trusted, reason, subject, issuer, valid_to}`.
    `fingerprint` is the leaf certificate's SHA-256 in uppercase colon hex (what
    `openssl x509 -fingerprint -sha256` prints). For a hub already in use, a failed request
    re-probes (at most every 10 s) and sets `HubState.cert`; the status reads unreachable, the
    stream stops and the Hub view is hidden until it is decided.
  - The shell shows the fingerprint in groups of four and the command that prints it on the
    computer serving https for the hub:
    `openssl s_client -connect <host>:<port> [-servername <host>] </dev/null 2>/dev/null |
    openssl x509 -noout -fingerprint -sha256`. The person confirms that they match.
  - `hub:trust-cert {url, fingerprint}` probes again and refuses unless the hub still presents
    exactly that fingerprint. It then stores the pin with the hub config: `pins` in
    `direct.json`, `{"https://host:port": {"sha256": "…", "at": "…"}}`, at most 8 origins. A
    certificate that now passes normal validation removes the pin instead.
  - With a pin, the app's own requests to that origin (API calls, the stream, pairing) go
    through `https.request` with a connection that is handed over only after the TLS handshake
    showed the pinned fingerprint: any other certificate, even one a public CA signed, fails
    with `CertificateChangedError` before a request byte or the token is written. The Hub view
    session gets a `setCertificateVerifyProc` that accepts the pinned certificate for the hub's
    host, refuses any other certificate for it, and leaves every other host to Chromium's
    verdict. Chromium gives the proc the host but not the port, so the Hub view pin is per
    host; the app's own requests pin per origin.
  - A different certificate later is "certificate changed": `cert.changed` true, an error
    banner, nothing sent; the person compares the new fingerprint and re-pins with the same
    `hub:trust-cert`. Unpairing with "forget" drops the pin.
  - Neither `wy` nor the hub can print this fingerprint: the hub serves plain http and TLS is
    ended by whatever serves https for it (Caddy, nginx, `tailscale serve`), whose
    certificate the hub never sees. Printing it from wy would need a command run on the hub
    machine that dials the public https address and prints the presented leaf's SHA-256
    (for example `wy hub tls-fingerprint`); until then the openssl line above is the way.
- Stream: `GET /v1/stream` with `Last-Event-ID`. It is parsed by `shared/sse.js` and reconnects
  after 1, 2, 4 … 60 s. On `reset` it refetches `/v1/overview`. If the hub sends no `ping` for
  60 s, it reconnects. A `401` stops the stream and asks to pair again. Only a `401` (here or on
  any desktop-token request) means the token is dead: a `403` is shown as a hub error and the
  stream keeps retrying with its backoff.
- The needs-you count is `overview.counts.needs_you`. It is refetched (debounced 1 s) after
  `session` events and published on the bus for the tray.

## C7. Hub view

- A `WebContentsView` with the session `persist:wy-hub` and the hub's URL. It is created when
  first selected and kept afterwards.
- Board sign-in: before the first load, and whenever the view lands on `#/login` or `#/pair`,
  main calls `session.fromPartition('persist:wy-hub').fetch(<hub>/v1/session/login, {method:
  'POST', body: {token}, headers: {'content-type': 'application/json'}})`. The cookie is then
  set in that partition, the token never enters the page, and the cookie is read-only
  (api.yaml).
- Owner sign-in: `wy desktop owner-link --json` → `loadURL(<link>)`. The board's `#/owner`
  route does the rest. When the 30-day owner session expires, the board drops back to its
  read cookie on the next login, and Settings shows "Sign in as owner".
- Keys: Settings → Keys (keys.md 3.3, pwa.md 3.2) is the board's `#/more/keys` page, shown in this view (Hub settings); the app has no keys screen of its own. Confirming a pending owner key needs the owner sign-in above; until then the page is read-only and says so.
- Peers: Settings → Peers (peers.md 5, 11) is the board's `#/more/peers` page (pwa.md 3.1), shown
  in this view; the app has no peers screen of its own. The desktop token is read-only, so the
  page enables its controls (matrix edits, Pause all peers, resume) only after the owner
  sign-in above; until then it shows the matrix and the audit and says why.
- `hub:route` loads `<hub>/<route>` (for example `#/sessions/<id>`). Notification clicks and
  tray items use it.
- Account-tab handoff: a `https://claude.ai/code/session_[A-Za-z0-9]+` link from the board goes
  to the tab of the session's seat (from `/v1/sessions/{id}`), matched by account uuid: the tab
  whose learned `seat.account` is that account, else the tab made for it (`expect.account`). It
  never goes to a tab of another account: claude.ai answers "session does not exist" there. If
  no tab has that account, the app makes one (named by the seat's label, `expect` set, starting
  at the link) and opens the link there; the person signs in there if the tab is signed out.
  Only a link whose seat is unknown (no session record, no hub) keeps the old fallback: the
  first signed-in tab, else the system browser. `openLink(url, seat)` is therefore async.
- Accounts with no tab: the hub's accounts (`/v1/accounts`, one per account uuid, hidden ones
  left out) that no tab is signed in as or was made for are `ShellState.missing`
  (`[{seat, label}]`). While any exist, the Hub tab's top line (the same 44 px strip as the
  read-only line, also shown to an owner) says "claude-6 and claude-7 have no tab here. Add
  tabs", and so does the "More tabs" menu. **+** then offers "Add a tab for claude-6" for each of
  them above the blank "New tab" form. `tab:add` with `seat` makes the tab named by the label
  with `expect = {seat, account, org, label}`; a second add for an account that already has a
  tab returns that tab. Tabs are only ever made by that click (or by a handoff above), never
  silently.
- Wrong account in a tab: `TabState.expected` is the label in `expect`; `TabState.mismatch`
  (`{expected, actual, shared}`) is set when the tab is signed in and the account uuid its usage reader
  learned (`tab.seat.account`) is not `expect.account` (never by email; `actual` is the hub's
  label for that account, else null; `shared` is true when another tab is already signed in as
  that account). The strip then says "This tab is signed in as claude-1, not claude-6." and the
  tab keeps its own name (not the other account's label, colour or seat). It offers two ways out:
  - **Use this tab for claude-1** (the person signed in as claude-1 on purpose; "Use this tab for
    this account" when the hub does not know the account). `tab:adopt` makes the tab that
    account's: for a hub account its name is the hub label and `expect` becomes that seat (colour
    and seat follow it); for an unknown account `expect` is removed (the tab follows whatever is
    signed in) and the name is the usage reader's account display name (`account_name` in the tab
    record, from `/api/account`, never the email), else "account". The notice goes away. If
    `shared` is true a confirm first says "Another tab is already signed in as claude-1. Use this
    one too?" (two tabs of one account are fine). The account the tab was made for then has no tab, so the
    "has no tab here" line and the **+** offer show it, and a toast says "This tab is now
    claude-1. Add a tab for claude-6 to sign in to it." with an **Add tab for claude-6** action
    that runs the add-for-account flow above (`offer` in the `tab:adopt` result is
    `{seat, label}` for that account, or null when another tab already stands for it).
  - **Sign out and sign in as claude-6**, a text button that loads `<claude.ai>/logout` in the
    tab (`tab:logout`).

  The phone has the same check (phone.md P17.7).

## C8. Shell layout

- Top bar: **Hub** (badge = needs-you count), one tab per account (colour dot and label from the
  seat when known, else the tab name), **+**, **Settings**. Ctrl/Cmd+0 opens Hub, 1…9 the tabs,
  and Ctrl/Cmd+, opens Settings.
- Under an account tab sits a 40 px strip: 5h and 7d bars, per-model weeks, reset times, the
  reading state (on, off, retrying, stopped + Resume) and "last read 14:05". Numbers come from
  the hub seat (`/v1/accounts`) when known, else from the local parse marked "local"
  (`shared/usage.js` `windowsOf`, the same top-level and model-scoped rules as the hub,
  record.md 4.3). An idle window (0 % with no reset time: claude.ai has not started it) reads
  "0% · not started"; a window whose reset time has passed reads "0% · reset at Sat 10:10"
  (the window reset; nothing is known to be used since), never a countdown to a passed time.
- Settings (native, `DeskAccounts`):
  - **Account tabs:** name, seat label and colour (editable with an owner session, through
    `seat:update`), reading switch, send-to-hub switch, warning status, Remove.
  - **This computer's Claude Code folders:** `wy account list` with the sharing switches
    (`wy account share … --yes --json`). Add and Log in show the `wy account add` and
    `wy account login` commands to copy, because they need a terminal.
  - **Hub:** URL, connected, version, owner yes/no ("Sign in as owner"), pairing, and for a
    local hub the service state with Start and Stop.
  - **Notifications** and **Background:** run at login, start hidden, keep running when
    closed, tray.
  - **Hub settings** links: Machines, Alerts and rules, Devices, Phone pairing, the hub's own
    Settings. Each opens the Hub view at its route (pwa.md 3).
  - **About:** this app's version, wy's (`wy desktop info` → `wy`), the hub's, Electron,
    Chromium and the platform.
- Update notice (`shared/version.js`): the app asks no update server and calls no third party.
  It compares its own version with wy's and the hub's (both already known from `wy desktop
  info` and `/v1/overview`; versions such as `dev` are ignored). `ShellState.update` is
  `{kind, app, version, from}` or null: `update` when the hub or wy runs a newer release ("Update
  available: Your hub runs Watchyard 0.7.0, this app is 0.6.0."), `older` when this app is newer
  ("… older than this app. Update it with wy update."). It is shown at the top of Settings and
  in About, with **Open the release page** (`app:open-release`), which opens
  `https://github.com/mraj602/watchyard/releases/tag/v<version>` (or the release list) through
  `openExternalSafe`. The URL is built in main, never taken from the renderer.

## C9. Notifications and tray

- On stream `alert` with `change: "fired"`: fetch the alert and its session, then show the
  notification. The title is `<seat label or email> · <project>`. The body is the trigger text
  ("asked a question", "needs permission", "5-hour at 82 %") and, when present, the session's
  question cut to 140 characters (`shared/notify-text.js`, golden tests). Clicking it shows
  the window and runs `hub:route #/sessions/<id>?alert=<al>`, or `#/accounts?seat=<id>` for
  limit triggers. An alert id is notified once per run. No notification is shown when
  `settings.notifications` is false, or when the window is focused and showing the Hub view
  on that session.
- There are no Snooze or Seen buttons, because the desktop token has no `push` scope. The
  board's buttons work with an owner session.
- Tray: a monochrome chevron glyph (no tile) from committed PNGs under `assets/tray/{dark,light}/`
  (16, 22, 24, 32 px with `@2x`; the dark ink for light bars, the light ink for dark bars. Linux
  always uses the light ink: GNOME, KDE and most panels are dark even under a light system theme
  (black on black was invisible), and `WATCHYARD_TRAY_ICON=dark|light` overrides it for an odd
  panel. Windows follows `nativeTheme.shouldUseDarkColors` and swaps on theme change; macOS uses
  the `trayTemplate` template images). "Needs you" is a small dot at the top-right of the glyph; the
  count is in the tooltip ("2 sessions need you") and the first menu line. macOS also sets
  `app.dock.setBadge`; Windows uses `win.setOverlayIcon` (a dot); Linux uses `app.setBadgeCount`
  where supported. Icons are rendered by `scripts/gen-icons.sh` from `build/icons-src/`. Menu: Open Watchyard;
  up to 5 needs-you sessions ("work · api · asked"), each opening its route; Refresh usage;
  Pause all reading (sets every tab off, with a confirm); Quit. With no tray (GNOME without
  AppIndicator), closing the window quits unless `keep_running` is set, and the close notice
  explains this.

## C9b. App menu

Watchyard menu: Hub, Account 1…9, Refresh usage, Run at login, Keep running, Quit.
`editMenu`. `toggleDevTools` appears only when unpackaged.

## C10. API and schema changes

- `PATCH /v1/accounts/{seat}`: `x-scopes: [owner]`, `x-reserved`. Body `SeatPatch`
  `{label?: ^[a-z0-9][a-z0-9-]{0,23}$, colour?: ^#[0-9a-f]{6}$, order?: int, hidden?: bool}`
  (null clears label and colour). Replies: 200 `Seat`, 400, 401, 403, 404. It sets
  `seat.look_src = 'ui'`. Ingest's `slot_info` label and colour apply only while `look_src` is
  `NULL` or `'slot'`, and set it to `'slot'`. The importer sets `'import'`. Publishes a `usage`
  stream event.
- `Seat.places[]` gains `stopped_reason: string | null` (from `web_seat.stopped_reason`).
  Additive, no version bump.
- `0005_desktop.sql`: `ALTER TABLE seat ADD COLUMN look_src TEXT CHECK (look_src IS NULL OR
  look_src IN ('ui', 'slot', 'import'))`, with a backfill: `'import'` where the label came
  from the importer of earlier versions (device `dev_import`), else `'slot'` where a label is set. Then
  `schema.sql` is v5.
- No change to `/v1/ingest`, to record kinds or to `v`. `web_usage` from a machine device is
  already valid (tokens.md 3: write for its own machine id).

## C11. Running `wy`

- Resolution: `WY_BIN` (unpackaged only), else `wy` on `PATH` and then `~/.local/bin/wy`
  (`%USERPROFILE%\.local\bin\wy.exe`), if `wy --version` is ≥ the app's `minWy` (package.json
  `watchyard.minWy`, `0.6.0`). Otherwise the bundled copy at
  `process.resourcesPath/wy/wy[.exe]`. The bundled copy is never installed onto `PATH`.
- `child_process.execFile` (never a shell), with these environment changes: drop
  `ELECTRON_*`, `NODE_OPTIONS`, `NODE_*` and `WY_APP_*`; keep `HOME`, `PATH`, `WY_*` and the
  XDG and Windows profile variables. Timeouts: 15 s, except 60 s for `flush`, `desktop token`
  and `desktop owner-link`. stdout is capped at 1 MiB and stderr at 64 KiB. A non-zero exit
  becomes `{ok: false, exit, error: <first stderr line>}`.
- **Argv allow-list** (`main/wy/commands.js`, the only builder; any other argv throws):

  | Purpose | argv | stdin |
  | --- | --- | --- |
  | version | `--version` | — |
  | machine id | `machine show --json` | — |
  | append | `outbox append` | JSON lines |
  | flush | `flush --quiet` | — |
  | info | `desktop info --json` | — |
  | token | `desktop token --json --name <^[A-Za-z0-9._-]{1,40}$>` [`--code-stdin`] | code |
  | owner | `desktop owner-link --json` | — |
  | service | `desktop hub <status\|start\|stop> --json` | — |
  | local accounts | `account list --json` | — |
  | share | `account share <folder ^~?[A-Za-z0-9._/-]{1,200}$> --share <^[a-z,=:+-]{1,200}$> --yes --json` | — |

## C12. Module interfaces

Every main-process module exports `create(deps)` and takes Electron and Node as arguments;
only `main/index.js` imports `electron`, and only `main/wy/` imports `child_process` (a unit
test in `main/boot.test.js` checks both). `main/boot.js` creates the modules in this order
(`START_ORDER`, as built):
`paths` → `security` → `protocol` → `ipc` → `store` → `autostart` → `background` →
`wy` → `hub` → `window` → `state` → `tabs` → `usage` → `tray` → `notify`
→ `menu`. Before `ready`: `paths`, `security.applyGlobal()`, `protocol.registerSchemes()`,
`ipc` (right after the bus), `store`, `autostart` and `background`, whose `claim()` (the
single-instance lock) ends a second launch there. `background` reaches the window and tray
through late-bound getters, since they exist only after `ready`. After `ready`: the store
loads, `ipc.start()` precedes the window,
`hub.start()` follows it, `tabs.start()` then `usage.start()` run once the window exists,
and the hub view is made when the Hub view is selected and the hub status is `ok`. Quit runs
`SHUTDOWN_ORDER`: `usage.stop` → `tabs.stop` (cookies flushed) → `hub.stop` (the stream) →
`wy.flushNow` (at most 5 s; the outbox keeps the records) → `store.flush`, each step in its
own `try`, once. The JSDoc of each module fixes what its `create()` returns; the parts other
modules call are:

| Module (owner) | Used by others |
| --- | --- |
| `bus.js` (0) | `on(event, fn) → off`, `once`, `emit(event, object)`, `last(event)`. Events: `tabs`, `usage`, `hub`, `needs_you`, `alert`, `settings`; payloads in the file header. |
| `ipc.js` (A) | `register(channel, fn)` (throws for a channel not in the table), `send('state' \| 'toast', payload)`. |
| `security.js` (A) | `webPreferences(kind, extra)`, `guardView(webContents, kind, opts)`, `guardSession(session, kind)`, `openExternalSafe(url)`; kinds `shell`, `tab`, `hub`. |
| `store.js` (A) | `get()` (a copy), `update(mutate) → Promise<state>` (serialised, atomic), `flush()`. B owns `tabs[]`, E owns `settings`. |
| `window.js` (A) | `addView(id, view, kind)`, `removeView(id)`, `select(id)`, `selected()`, `showWindow()`, `isShowing(id)`, `shell`. |
| `wy/index.js` (C) | `info()`, `append(records)`, `flushSoon()`, `token({code?, name})`, `ownerLink()`, `service(action)`, `accountsList()`, `share(folder, share)`, `machineId()`, `run(purpose, params, stdin)`. |
| `hub/index.js` (C) | `route(route)`, `alert(id)`, `session(id)`, `seats()`, `view()`. |
| `tabs/index.js` (B) | `list()`, `view(id)`, `openLink(url, seat)`, `flushCookies()`. |
| `usage/index.js` (B) | `refresh(id?)`, `pauseAll()`, `stop()`. |
| `autostart.js` (E) | `get()`, `set(on)`. |

Shared constants (folder names, partitions, CSP, web preferences, permissions, cadence,
backoff, timeouts, caps, test-only variable names) are in `src/shared/constants.js`; tests use
the fakes of `app/test/fakes/electron.js`.

## C13. Packaging and release

- Targets (`app/electron-builder.yml`): Linux AppImage, deb and rpm; macOS dmg; Windows NSIS
  installer. File names are `Watchyard-<version>-<os>-<arch>.<ext>`. Nothing is signed or
  notarized (no paid certificates), and the app has no updater.
- The bundled `wy` is built, not downloaded: `scripts/fetch-wy.mjs <os> <arch>` runs
  `go build ./cmd/wy` on the checkout with `CGO_ENABLED=0`, `-trimpath` and the ldflags of
  `scripts/release/build.sh` (`Version` from `WY_VERSION`, else `app/package.json`; `Commit` is
  `git rev-parse --short HEAD`). It is placed in `resources/wy/<os>-<arch>/`.
- A tag `v<semver>` (`.github/workflows/release.yml`) runs a `desktop` matrix job (ubuntu,
  macos, windows) after `check`. Each job fails unless `app/package.json`'s version equals the
  tag without `v`, bundles `wy` with `WY_VERSION=<tag>` and checks that `wy --version` reports
  that version, runs `npm run dist`, checks it produced exactly the expected package types for
  that OS, and uploads them as an artifact. `phone/package.json` is not checked: the Android
  app is released by `phone-v*` tags.
- The `release` job needs `check` and every `desktop` job, so one failed build publishes
  nothing. It downloads the packages, writes `SHA256SUMS-desktop` (`sha256sum` format, one line
  per package), attests build provenance for the `wy` archives and every package, and runs one
  `gh release create` with all files. `SHA256SUMS` stays the `wy` archives' file only, because
  the install scripts read it.
