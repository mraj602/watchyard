# Phone app

Status: spec v1. Implemented in `phone/` (Expo SDK 57, React Native), Android only. Unofficial;
not affiliated with Anthropic.

The phone app is a `phone` device (tokens.md 3: scopes `read` and `push`) with the PWA's
screens, native notifications with **Open**, **Snooze** and **Seen** (Android, over the app's
own link to the hub), home-screen widgets on Android (the iOS widget and Lock Screen countdown sources stay in the
tree, are kept compiling and are not shipped), and an optional Lock Screen countdown source
for iOS. It talks only to its hub's existing API; the hub side (the link channel and
widget refresh frames) is [push.md 13](push.md#13-the-link-channel).

Related: pairing.md 2.2, 3.1 (the QR, `cookie: false`), tokens.md 3 (`phone` scopes),
push.md 5.4 (`prefs`), 5.5 (texts), 5.8 (deep links), 6.5 (the payload), 8 (action tokens),
13 (the link channel), pwa.md 3 and 5 (screens and the service worker's tap rule), api.yaml
(`DevicePrefs.widgets`, `PushSettings.refresh_min`), schema.sql v12.

Section numbers start at P1; P8 to P10 (the hub side) were push.md 12, which
is removed; the hub side of widget refresh is push.md 13.5.

**Notifications.** Android only. Native notifications
come over the app's own link to the hub (push.md 13, the `wy-link` module of push.md 14); the
app uses no push service, Expo's, Google's or any other. P2 adds keys to the claim, P6.3
registers nothing with the hub (the link target comes from pairing), P6.7 is the Settings →
Notifications screen, P13 has no EAS or Firebase config, no proprietary Google code and
`minSdkVersion` is 31, and P16 is the in-app updater. iOS sections (P6.6's iOS badge, P12, the iOS widgets of P11) stay in the
code and are not worked on; iOS gets no native notifications (use Web Push in the browser).

Code map: `phone/src/contract/` is the TypeScript side of this spec:

| File | Holds |
| --- | --- |
| `api.ts` | Mirrors of the api.yaml schemas the app reads or sends (`Overview`, `Session`, `Seat`, `Window`, `AlertBrief`, `Alert`, `Me`, `Device`, `DevicePrefs`, `PushTarget`, `PairClaim`, `PairClaimReply`, `PushSettings`, `ActionReply`, …) |
| `payload.ts` | `Payload` (exactly Go `model.Payload`, `PAYLOAD_KEYS` in Go's key order), `RefreshData`, `AndroidChannelId` |
| `snapshot.ts` | `WidgetSnapshot` v1 (P11), `TimelineEntry`, `SNAPSHOT_MAX_SEATS` 8 (`SNAPSHOT_IOS_SEATS` 4), `SNAPSHOT_LABEL_MAX` 12, `TIMELINE_MAX` 8 |
| `live.ts` | `LiveCountdownProps` (P12), `LIVE_MAX_H` 8, `LIVE_MIN_PCT` 80, `LIVE_END_AFTER_MS`, `LIVE_ACTIVITY_NAME` |
| `routes.ts` | `SCHEME`, `AppRoute`, route paths, the hash routes and incoming hosts of P7 |
| `constants.ts` | Storage keys (`wy.hub.v1`, `wy.cache.v1`, `wy.widget.v1`, `wy.local.v1`), category and action ids, channels (P6.1), timeouts, the `ACTION_TOKEN` pattern |
| `platform.ts` | The service interfaces `src/lib` takes as arguments (`FetchLike`, `Clock`, `KVStore`, `SecureStoreLike`, `AppStateSource`, `AppInfo`) |
| `validate.ts` | Runtime validators for all of the above (empty list = valid), used by the contract test and by the app on data it did not make |

## P1. The app: layout and versions

```
phone/
  package.json  package-lock.json  app.config.ts  tsconfig.json
  babel.config.js  jest.config.js  eslint.config.js  .gitignore  README.md
  app/                      expo-router routes (P4); thin screens only
  src/contract/             TypeScript mirrors of hub JSON (api.ts, payload.ts), WidgetSnapshot,
                            LiveCountdownProps, route constants. Keep in step with api.yaml
  src/lib/hub/              hub client: base URL, bearer, timeouts, errors, endpoints
  src/lib/pair/             QR/typed input parsing, claim
  src/lib/secure/           keychain wrapper (SecureStore behind an interface + in-memory fake)
  src/lib/data/             poller, last-known cache (AsyncStorage, never secrets), reducer
  src/lib/notify/           channels, categories, response decisions, link frames
  src/lib/links/            hash route ↔ app route, incoming URL allow-list
  src/lib/format/           ages, reset times, labels, state words (port of webui format.js)
  src/lib/widget/           WidgetSnapshot builder, timeline, Android render props
  src/lib/live/             countdown choice, Live Activity props, end policy
  src/lib/integrate/        the glue (pure): after-refresh widget/live/badge steps, registration
                            on pairing and start, link `refresh` frames, auto countdown
  src/state/                provider (the one source of truth), notification bridge, and glue.tsx:
                            binds the real modules and derives the screens' HubApi from useHub()
  src/components/           shared UI (rows, rings, chips, banners)
  widgets/ios/              expo-widgets components ('widget' directive): NeedsYouSmall,
                            AccountsMedium, ResetCountdown (Live Activity)
  widgets/android/          react-native-android-widget components + task handler
  plugins/                  local config plugins (with-wy-native.js)
  modules/                  local Expo modules, Android only: wy-link (the link, push.md 14),
                            wy-tabs (P17), wy-notify (channels, permission, badge, dismissals;
                            P6) and wy-scan (the pairing QR scanner, P2)
  scripts/                  check-no-secrets.mjs, check-no-google.mjs, prebuild-check.mjs,
                            dump-fixtures.sh
  test/                     setup.ts (installs the mocks), fakes.ts (recording fakes of the
                            native modules), render-widget.ts, fixtures/hub/*.json,
                            fixtures/widget-snapshot.v1.json
```

Rule: `src/lib/**` imports no `react-native`, no `expo-*` module and no React. It takes
platform services as arguments (`fetch`, clock, storage, a `Notifications`-shaped object). A
jest test walks the import graph and fails on a forbidden import (like the Go side's
lint test).

Pinned versions (exact; all released on or before 2026-09-29; the native modules are the
versions `expo@57.0.26`'s `bundledNativeModules.json` names):

| Package | Version | Released | Why |
| --- | --- | --- | --- |
| `expo` | 57.0.26 | 2026-09-29 | SDK 57 |
| `react` / `react-native` | 19.2.3 / 0.86.3 | — | SDK 57 pins |
| `expo-router` | 57.0.24 | 2026-09-29 | Routes, deep links |
| `expo-secure-store` | 57.0.4 | 2026-09-11 | Token in Keychain / Android Keystore |
| `expo-linking` | 57.0.11 | 2026-09-24 | Scheme URLs, opening `remote_url` |
| `expo-constants` / `expo-device` | 57.0.20 / 57.0.2 | ≤ 2026-09-29 | App version and versionCode (the embedded app config), device name |
| `expo-widgets` | 57.0.22 | 2026-09-29 | iOS widgets and Live Activity |
| `@expo/ui` | 57.0.21 | 2026-09-29 | SwiftUI components for widgets (`expo-widgets@57.0.22` depends on `~57.0.21`) |
| `expo-clipboard` | 57.0.2 | 2026-09-11 | Copy resume command |
| `react-native-android-widget` | 0.22.1 | 2026-08-17 | Android widgets |
| `expo-build-properties` | 57.0.22 | 2026-09-24 | Deployment targets |
| `expo-dev-client` | 57.0.19 | ≤ 2026-09-29 | Development builds (native modules need a development build) |
| `@react-native-async-storage/async-storage` | 2.2.0 | — | Last-known cache only |
| `react-native-safe-area-context` / `react-native-screens` | 5.7.0 / 4.26.2 | 2026-02-24 / 2026-07-16 | expo-router |
| `typescript` | 6.0.3 | 2026-04-16 | SDK 57 template |
| `jest-expo` | 57.0.5 | 2026-08-26 | Test preset |
| `@testing-library/react-native` | 14.0.1 | 2026-06-23 | Screen tests (14.1.0 is too new) |
| `eslint-config-expo` | 57.0.2 | 2026-08-26 | Lint |
| `react-dom` | 19.2.3 | 2025-12-11 | expo-router and `@expo/metro-runtime` pull a newer `react-dom` (19.3, which wants React 19.3) unless it is pinned to SDK 57's `bundledNativeModules` value |
| `babel-preset-expo` | 57.0.13 | 2026-09-24 | `babel.config.js` needs it at the top level. 57.0.15 (what `expo` nests) declares `expo-widgets ^57.0.24`, newer than our pin; 57.0.13 wants `^57.0.21` |
| `test-renderer` | 1.2.0 | 2026-04-16 | RNTL 14's renderer peer; 1.3 uses `react-reconciler` 0.34, which wants React 19.3 |
| `jest` / `@react-native/jest-preset` | 29.7.0 / 0.86.3 | 2023-09-12 / 2026-08-24 | jest-expo 57's runner and preset |
| `eslint` | 9.39.5 | 2026-07-10 | eslint-config-expo 57's flat config (npm marks 9.x end of life; 10 is not supported by the config yet) |
| `@types/react` / `@types/jest` / `@types/node` | 19.2.18 / 29.5.14 / 22.19.1 | ≤ 2026-07-30 | Types (`tsconfig` `types: ["jest", "node"]`: TypeScript 6 no longer loads every `@types` package) |

No `expo-updates` (no over-the-air updates: decision in "Defaults").

No proprietary Google code ships (P13): `expo-notifications` (it links `firebase-messaging`),
`expo-camera`'s barcode scanner (Play services code scanner and ML Kit) and `expo-application`
(Play Install Referrer) are not dependencies. Their parts the app used are the local modules
`wy-notify` and `wy-scan`, whose native dependencies are pinned in their `build.gradle`:

| Module | Native dependency | Version | License |
| --- | --- | --- | --- |
| `wy-notify` | `me.leolin:ShortcutBadger` (the launcher badge, as expo-notifications used it) | 1.1.22 | Apache-2.0 |
| `wy-scan` | `com.journeyapps:zxing-android-embedded` (with `com.google.zxing:core` 3.4.1) | 4.3.0 | Apache-2.0 |

Check it with `make phone-check` (`npm ci`, `tsc`, `eslint`, `jest --ci`, the secrets check and
both prebuild checks). `npm ci` prints peer-override warnings (`react-native-worklets`, the
optional React 19.3 peers); they are expected and do not fail the install.

## P2. Pairing

- **Scan.** The Pair screen's **Scan QR code** calls `wy-scan`'s `scanQr(prompt)`: it asks
  for the camera when needed (the button reads **Allow camera** until it is granted, and the
  box says "Camera access is off in system settings." once Android will not ask again), then
  opens ZXing's full-screen scanner (`zxing-android-embedded`, portrait, QR codes only, no
  beep) and resolves with the first code's text, which goes to `parsePairInput`. Back
  resolves `cancelled`; a refused code is reported and **Scan QR code** opens it again. Where
  the module is missing (iOS, Expo Go) the screen switches to typed input. Typed input is
  unchanged.
- **`parsePairInput(text)`** (pure) accepts:
  - `https://<host>[:port]/#/pair?s=<22 base64url>&c=<6 digits>[&h=<24 base32>&k=<22 base64url>]`,
    the QR of pairing.md 2.2 with keys.md 7's hub fingerprint and registration seal;
  - `watchyard://pair?u=<https origin>&s=…&c=…[&h=…&k=…]`, the "Open in app" link the web pair
    page offers. On Android the page emits it as a Chrome intent URL pinned to the release
    package (`intent://pair?…#Intent;scheme=watchyard;package=io.github.mraj602.watchyard;end`),
    because any app can register the plain scheme and this link carries the secret and seal key.
    The page emits nothing off Android, and a development build (`.dev` package) is paired with
    the QR or typed code. When the app is already paired with another hub, the link card names
    both hubs and the person must confirm twice (the link card, then "Unpair and pair"). `h` and `k` are the QR's pins and are carried through the link route
    to the Pair screen (without them the claim sends no keys and the phone gets no link);
  - typed input: an address plus `482-913`, `482913` or `482 913`.

  It returns `{origin, secret?, code?}` or a reason. The origin must be `https://`, with no
  user info, path, query or fragment kept. Only `__DEV__` builds, and bundled builds made with
  `WY_APP_VARIANT=development` (`extra.cleartext`, the same switch as cleartext traffic in the
  manifest), accept `http://` for loopback, RFC 1918 and Tailscale (100.64.0.0/10) addresses (pairing.md 2.3's
  `--insecure` hubs, for local testing, `test/e2e/android`).
  It refuses a `javascript:` URL, an origin with a backslash and a host over 253 characters.
- **Claim:** `POST <origin>/v1/pair/claim` with `{secret | code, name: "<model> · app",
  expect_kind: "phone", cookie: false, client: "app/<version>+<build>"}`. On 200 it keeps
  `{origin, token, device, name}` (P3). The error mapping follows pairing.md 3: 401 →
  "Invalid or expired code", 409 → "That code is for a \<kind>, not a phone", 429 → "Too many
  tries, wait 10 minutes", network → "Can't reach \<host>. Is Tailscale on?"
- **Keys in the claim.** Before claiming, the app calls `WyLink.prepareKeys()` (push.md
  14.3). With `k` from the QR it sends `keys_sealed` (keys.md 7.1); with a typed code, `keys`
  in the clear (never with a QR secret: the hub refuses that). The reply's `hub_key.fp` must
  equal `h` when the QR had one, else the app refuses: "This hub's key does not match the QR
  code". It then makes keys.md 7.3's checks (certificate signed by `hub_key`, holding its own
  keys, `reg = qr` after a sealed claim) and on any mismatch deletes the new device and
  refuses. On success it calls
  `WyLink.configure({origin, device, urls: link.urls, pingS: link.ping_s, hubKey, reg, cert,
  qrFp: h})`, which makes the certificate checks natively and rejects `ERR_WY_HUB_KEY` /
  `ERR_WY_TAMPERED` (push.md 14.8), then `WyLink.start()` (from the glue's `onPaired`).
  As built: the QR's `k` is sealed by `WyLink.sealKeys(k, secret)`; a QR with a secret but no
  `k` (a hub without the link) sends no keys at all; a damaged `h` or `k` is refused like a damaged
  secret. A sealed claim whose reply has no `hub_key` or `cert` is refused as tampered; a
  typed-code claim to a hub without the link (no `hub_key`) pairs without instant delivery. A
  `configure` failure other than the two refusals pairs without the link (Settings →
  Notifications shows it). The refusal deletes the device with `DELETE /v1/devices/self` and
  the new token, then shows the message; nothing is kept.
- After pairing: `GET /v1/me` (to check it), then the notifications screen (P6), then Now.
- A second pairing replaces the first, after confirming "Unpair from \<host>?". There is one
  hub per app install.

## P3. Token storage and the hub client

- SecureStore key `wy.hub.v1` holds JSON `{origin, token, device, name, paired_at}`, with
  `keychainAccessible: AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY`. That way the background
  notification task and widget refresh can read it while the phone is locked after its first
  unlock, and it is never restored onto another device. Android: Keystore-backed, and the app
  sets `allowBackup=false` (P13).
- `HubClient` (pure, `fetch` injected): it prefixes the origin, sends `Authorization: Bearer`,
  times out after 10 s (6 s for pairing's `/health` probe, 1.5 s for route checks), follows no cross-origin redirect and
  parses the `Error` schema. On 401 it calls `onUnauthorized` once: the app clears the
  keychain entry, the cache and the widget data, ends Live Activities and shows Pair ("This
  phone was unpaired on the hub"). A 401 from a client of an earlier pairing (after "Unpair
  and pair") is ignored. Its errors and logs carry the path and status, never the token or
  response bodies.
- **Origins and redirects.** An origin is kept normalised: lower-case, no trailing slash, no
  default port (`https://hub:443` is `https://hub`), so every same-origin check compares like
  with like. Every request asks for `redirect: "error"`, and any 3xx answer, or an answer
  whose final URL is on another origin, is fatal. React Native's `fetch` may follow a
  redirect whatever `redirect` says, and then that check is after the fact: the request has
  already gone out with its bearer. So the first contact with a new origin is a
  `GET /health` without credentials (a claim sends the pairing secret or code only after it)
  that must answer from that origin with no redirect; a 503 still counts as an answer. A
  stored origin never changes except by pairing again, which probes again. If a paired
  hub starts redirecting later, and the platform follows it carrying the header, the target
  can see a bearer request before the app refuses the answer. That is the native limit,
  accepted for a hub the person runs on their own tailnet.
- Unpair (Settings): stop polling, record `{origin, token}` in SecureStore key
  `wy.revoke.v1` (a short list), do the same local clearing (which also dismisses the old
  hub's delivered notifications), then `DELETE /v1/devices/self`. An unpairing the hub has
  not confirmed is retried on every start and foreground until it answers 2xx, 401 or 404;
  pairing again with the same hub confirms it first, so its old link target is gone before the
  new claim makes its own.
- A keychain that cannot be read at start (locked before the first unlock) is state
  `locked`, not unpaired: nothing is cleared, notification decisions stay queued, and the
  entry is read again when the app becomes active.

## P4. Screens and routes

The same screens as the PWA (pwa.md 3), with phone tabs **Now, Sessions,
Accounts, More**:

| App route (expo-router) | Hash route it mirrors | Screen |
| --- | --- | --- |
| `/` (tab Now) | `#/` | Needs-you inbox with swipe Seen / Snooze (`POST /v1/alerts/{id}/seen`, `/snooze`; Seen on every row, also a skipped alert, and `POST /v1/sessions/{id}/seen` for a row with no alert; Snooze only while the alert can push, push.md 4), alert status words, rings per account, running count per machine |
| `/sessions?filter=&by=` | `#/sessions` | Grouped list with a search box (pwa.md 3.0b) and filter chips |
| `/sessions/[id]?alert=` | `#/sessions/<id>` | State history, last message, **Open in Claude** (`remote_url` in the seat's claude.ai tab on Android, P17; elsewhere with the wrong-account note), **Copy resume command**, Snooze |
| `/accounts?seat=` | `#/accounts` | One compact row per account that opens in place (P17.8), "most room", **Count down on Lock Screen** (iOS, P12) |
| `/more` | `#/more` | Alerts, Machines, Peers, Settings |
| `/more/alerts` | `#/more/alerts` | Rule switches as `prefs.mute` (`PATCH /v1/devices/self`), recent pushes (`GET /v1/alerts?limit=50`, filtered on the phone to open alerts (`pending`, `fired`, `snoozed`) plus anything fired or created in the last 48 hours) |
| `/more/machines?machine=` | `#/machines` | Machines list |
| `/more/settings` | `#/more/settings` | Hub and status, notifications state, private pushes, tap behaviour, own quiet hours, widgets refresh switch (`prefs.widgets`), app version and **Check for update** (P16), **Unpair** |
| `/more/settings/notifications` | — | P6.7 |
| `/more/peers` | `#/more/peers` | PE3: **Pause all peers**, the matrix (view; a tapped cell and **Block all** can only narrow; the turn limit is read-only) and the audit list (peers.md 5, 10, 11); see below |
| `/alerts/[id]` | `#/alerts/<id>` | Loads the alert and replaces itself with its deep link (push.md 5.8) |
| `/pair` | `#/pair` | P2 |
| `/tabs/[seat]?url=` | — | P17: the seat's claude.ai tab (Android) |

**Peers** (`PeersScreen`) reads `GET /v1/peers/matrix`, `/v1/peers/audit` and, when the hub
serves it, `/v1/peers/messages` through the hub client (`matrix`, `editMatrix`, `pausePeers`,
`peerAudit`, `peerMessages`). Top to bottom: the **Pause all peers** switch (`POST
/v1/peers/pause`; one tap pauses; turning it off is disabled with a note, because resuming widens
and a phone is never an owner session); the matrix as a grid of senders (rows) and receivers
(columns) with the version, signer and `max_turns` in the header; the audit list (what, who, a
metadata line, age) and the message list when served. **A tapped cell can only narrow**: both
ways offers one way either way or none, one way offers none, none offers nothing; each is a
`PUT /v1/peers/matrix` `edit` with `base_version` and `deny` entries only, never `allow`
(`narrowings` in `src/lib/format/peers.ts`). Allowing a pair, same account on and resume are on
the board or at a terminal (peers.md 5.4). A 409 reloads the matrix, a 403 says the change needs
the owner. Everything is disabled while offline. A **Block all** button above the grid (confirm:
"Stop all peer messages between accounts? Threads in progress stop too.") sends one `edit` with
every pair in `deny` and `same_account: false` (`blockAllEdit`); Allow all is not offered
(owner-only; the screen says "Allow pairs from a computer (board, Peers)"). The turn limit
(`max_turns`) is shown read-only as "Replies between two sessions without you: N" ("No limit" for 0)
with the loop-guard explanation and the no-limit line (peers.md 6.3, 5.4).
Below the audit, as on the board (pwa.md 3.1): **Trusted senders** (`trustedSenders`, `GET
/v1/peers/trusted`; each row as the board words it, with **Revoke**, which narrows and so is
offered to the paired phone: confirm first, `revokeTrustedSender`, `POST
/v1/peers/trusted/revoke`, whose 404 shows the hub's message (run `wy peers trust doorbell
--off` on that machine); "approved by a test build" for a `test_build` record; a 404 on the list
from an older hub hides the section) and **Tags** (`peers`,
`GET /v1/peers`, plus the trusted senders' recent tags; a tapped holder opens
`/sessions/[id]`). The words come from `src/lib/format/peers.ts` (`trustedSenderLines`,
`tagGroups`), ported from `webui/static/components/peers-fmt.js` on a shared case table.

**Session search** (`SessionsScreen`) is the board's (pwa.md 3.0b): a `TextInput` above the
chips (`testID` `session-search`, clear button `session-search-clear`), matched on the device
over `data.sessions` with `src/lib/format/search.ts`, the port of
`webui/static/components/search-fmt.js`. The query is the screen's state, so the 15 s polls
keep it. Found terms are tinted spans inside the row's `Text`; a term found only in a field the
row does not show adds the "Name: …" / "Message: …" line. Each row shows its tags as chips
(`testID` `session-tag-<tag>`); tapping one sets the query to `tag:<tag>` (pwa.md 3.0b), which
matches only that exact tag.

Owner-only PWA screens (Devices, Add phone, rule editing) are not in the app. Analytics (More, Analytics; `GET /v1/analytics/{report}`, analytics.md) is: a range (7, 30, 90 days), a seat filter, totals, daily cost bars, a compact weekday by hour heatmap, models, projects, the five dearest sessions, limit windows (peak and final percent as bars) and limit hits.
Wording, ages and state words come from `src/lib/format` and match `webui/static/format.js`
on a shared table.

## P5. Data refresh

- Foreground (`AppState` `active`): poll every 15 s, plus at once on foreground, on a
  received push and after a Seen or Snooze. Background: no polling; the `wy-link` module's `refresh` frames
  (P6.5) update the widgets.
- The last good replies go to AsyncStorage key `wy.cache.v1` (no token, no secret). When the
  hub cannot be reached, the screens show them under "Offline · showing 14:02", and Seen,
  Snooze and test push are disabled, as in the PWA.
- After every refresh the app rebuilds the widget snapshot (P11) and updates a running Live
  Activity (P12). It writes the widgets only while the device's `prefs.widgets` is on, and
  only once `GET /v1/me` has answered (private mode comes from `prefs.private`, else the hub's
  `settings.private`); the Live Activity is kept up to date either way, and the badge is set
  from the overview's needs-you count (P6.6). `GET /v1/rules` (`snooze_min`) and `GET /v1/me`
  are read again at most every 5 minutes while polling.
- **Order of the accounts.** Every list of seats (Accounts, Now, Analytics, Peers, Settings →
  Claude tabs, the widget snapshot) follows `seat.order`, the owner's order from the board's
  drag-and-drop (pwa.md 3.0; read-only here, no dragging on the phone): `reduce` puts the
  overview's and the accounts reply's seats in that order (`orderSeats`: ties and seats without
  an `order` keep the reply's order, the latter last). The phone has no stream, so a reorder
  shows on the next poll (15 s in the foreground).

## P6. Notifications

Android alert notifications also carry `setTimeoutAfter(24 h)`, so one lingers at most a day if the hub's `dismiss` frame (24 h TTL) never arrives.

**P6.1 Android channels** (created before any permission or token call; Android 13+ shows
the permission prompt only once a channel exists):

| Channel id | Name | Importance | Used for |
| --- | --- | --- | --- |
| `wy_needs_you` | Needs you | HIGH | `needs_permission`, `asked`, `failed`, summaries |
| `wy_limits` | Limits | DEFAULT | `limit_80`, `limit_95`, `reset` |
| `wy_machines` | Machines | LOW | `machine_silent` |
| `wy_usage` | Usage changes | LOW | `limit_change`, `usage_reset`, `seat_change`, `weekly_drop` (push.md 3.9) |
| `wy_test` | Test | DEFAULT | test pushes |
| `wy_connection` | Connection (always on) | MIN | The link service's persistent notification (push.md 14.4) |

The `wy-link` module creates the same channels natively (it posts link notifications itself),
with the same ids, so either side may create them first. A channel that already exists keeps
its importance and every setting the user changed; the module re-creates it under the same id
only to bring its name up to date (Android applies just the name and description), so the
rename of `wy_connection` from "Connection" to "Connection (always on)" reaches existing installs.

**P6.1a Notification icons** (the `wy-link` module's own notes; direction A, "the mark with a
corner badge"). Every small icon is the Watchyard double chevron with a kind badge in the
top-right corner, one white filled path as a 24 dp vector (`wy_notify_<kind>.xml` in the
module's `res/drawable`, so it ships through autolinking and survives `expo prebuild`). The
alert colour behind it is the brand gold `#E8B04B`; the always-on connection note is grey
`#8A9099`.

| Kind | Small icon (badge) | Large icon badge | Used for |
| --- | --- | --- | --- |
| `connection` | hairline mark, no badge | none | the persistent note (14.4) and the "stopped" note |
| `needs_you` | dot | gold | `needs_permission`, `asked` |
| `failed` | exclamation | red `#E5534B` | `failed` |
| `limit` | gauge | orange `#F0883E` | `limit_80`, `limit_95`, the usage notices |
| `reset` | return arrow | green `#3FB68B` | `reset` |
| `machine` | crescent | grey `#9AA4AE` | `machine_silent` |
| `summary` | stacked ticks | gold | summaries |
| `generic` | mark only | none | tests and any trigger the app does not know |

The large icon is the app tile (ink disc, gold mark) with the kind badge bottom-right behind a
cut-out ring; the module draws it as a bitmap at runtime from the same path data (cached per
kind). The connection note has no large icon. The app's JS posts no notification.

**P6.2 Categories:**

| Category | Actions (`identifier` · `buttonTitle` · options) | Used when |
| --- | --- | --- |
| `wy_alert` | `open` · "Open" · `opensAppToForeground: true`; `snooze` · "Snooze 15 min" · `opensAppToForeground: false`; `seen` · "Seen" · `opensAppToForeground: false` | The payload has `actions` |
| none | Tap only | Summaries, `test`, private pushes still have actions (tokens reveal nothing) |

The Snooze title uses `snooze_min` from `GET /v1/rules` (`settings.snooze_min`; a phone
device may read it), and categories are re-registered when it changes. (Not `GET /v1/push/config`, which has no settings.) Whether a non-foreground action runs the JS response handler
on iOS while the app is killed is **unverified**. If it does not, one constant,
`BACKGROUND_ACTIONS_IOS`, flips Snooze and Seen to `opensAppToForeground: true`: the app
opens briefly, acts and shows a toast.

**P6.3 Registration.** The app registers no push target: the `link` target comes from pairing
(push.md 13.7). On start `wy-notify` creates the channels (P6.1) and asks for the
permission (`requestPermissionsAsync`: Android 13's POST_NOTIFICATIONS prompt through Expo's
permissions manager, on Android 12 whether notifications are on; anything but granted →
Settings shows "Blocked in system settings"), then `PATCH /v1/devices/self {prefs: {tz, widgets: true}}`. `widgets: true` goes only
on the first registration, when the device's `prefs.widgets` has never been set (read from
`GET /v1/me` or `GET /v1/devices/self`); later registrations send `{tz}` only, so a person who
turned widget refresh off keeps it off. `widgets` means widget refresh frames over the link.

**P6.4 Responses** (`decideResponse(response, reachable) → Decision`, pure):

| Input | Decision |
| --- | --- |
| `actionIdentifier` `snooze` / `seen` with a token in `data.actions` | `{kind: "act", token}` → `POST <origin>/v1/actions/<token>` with `Content-Type: application/json` and body `{}`, no bearer; then dismiss that notification. The result is not retried, as in pwa.md 5 |
| Default tap or `open` | The `tap` rule of `sw-lib.js` `chooseTarget`, where on Android a `remote_url` counts only when it is an `https://` link on claude.ai or a subdomain (default port, no user info; the native side drops any other, so the tap opens the app): `claude` with `remote_url` → open the link; `auto` with `remote_url` → probe `GET /health` (1.5 s), the link if no answer, else the route; otherwise the route from `data.url` (P7) |
| `data` missing or `v` not 1 | Open Now |

An act goes only to the hub that sent the notification. The payload has no hub identity
today, so the app drops (and dismisses) an act on a notification the OS delivered before the
current pairing was made (`notification.date` against `paired_at`, both on the phone's
clock); when a payload names an `origin`, an act whose origin is not the paired one is
dropped too. Each notification's Snooze or Seen runs once: the `wy-link` module's action handling and
the response listener share the AsyncStorage key `wy.acted.v1` (`"<id>|<action>"`, the last
50, never a token).

**Lock screen.** Every notification the module posts is `VISIBILITY_PRIVATE` with a public
version that reads "Watchyard" and "Open Watchyard to see this", so a locked phone shows no
project, account or machine name (the persistent connection note and the "stopped" note too).

**Native taps.** The notifications the `wy-link` module posts itself carry their own content
intent: an `ACTION_VIEW` of `Payloads.appLink(url)` (the payload's route as `watchyard://…`, Now
when it has none or it is refused) aimed at the app's launcher activity by component name, or
the Claude link when `tap` says so. The persistent connection notification and the "stopped"
note open `watchyard://now`; only the connection notification's **Settings** button opens
`watchyard://settings/notifications`. No notification is posted without a content intent
(Android 14 on Samsung answers a tap on one by opening the app's notification settings), and
no tap leads to a settings page except that button. `test/links.test.ts` checks that each of
these links resolves to the item or Now; the JUnit `FramesTest` checks the native side's link.

`initNotifications` (src/lib/notify) is called at module scope in `app/_layout.tsx` with
`wy-notify`. On Android no notification response reaches JS: every notification is wy-link's,
its tap and **Open** are the intents above and Snooze and Seen run in wy-link's
`ActionReceiver`. So `wy-notify` has no response listeners and `getLastNotificationResponseAsync()`
is always null; `decideResponse` and `performDecision` stay platform-neutral (and tested) for a
notifications object that has them.

**P6.5 Link frames:** there is no JS background task and no silent push. The `wy-link` module
handles link frames, `refresh` frames (push.md 13.5) and actions natively (push.md 14.5, 14.6) and
emits `refresh` to JS when the app is running; JS then rebuilds the widget timeline, updates the
Live Activity and sets the badge from `needs_you`. Action handling is P6.4's `act`.

**P6.6 Badge:** set after each refresh with `setBadgeCountAsync(needs_you)` (`wy-notify`:
ShortcutBadger on launchers that support it; 0 clears the badge and, as expo-notifications did,
cancels the app's notifications).

**P6.7 Settings → Notifications** (route `/more/settings/notifications`; built as
`src/components/screens/NotifySettingsScreen.tsx`, reached from a "Notification settings"
button in Settings' Notifications card):

| Row | Content | Action |
| --- | --- | --- |
| **Instant delivery** | Switch, on by default after pairing. Below it: "Keeps a connection to your hub. Uses a small, constant notification." | On: `WyLink.start()` and `PATCH /v1/push/targets/{link id} {enabled: true}`. Off: `WyLink.stop()` (15-minute checks, push.md 14.9) and the target stays enabled. |
| Connection | `LinkState.status` in words: "Connected · 2 h 14 min · WebSocket", "Reconnecting · next try in 20 s", "Not connected: hub unreachable", "Removed from hub". The last error (close code word or network class). Received / rejected counts. | **Reconnect now** |
| Battery | "Battery optimisation: off (good)" or "on: notifications may be late". | **Fix**: `requestBatteryExemption()`; then the maker guide |
| Phone-maker guide | Shown when the exemption is on or `Build.MANUFACTURER` is in the table below. One short paragraph per maker. | **Open settings**: `openBatterySettings()` |
| Notifications permission | "Allowed" or "Blocked in system settings". | **Open settings** |
| Encryption | push.md 14.7: "Verified by QR", or "Not verified: compare check code k7q2-m4xa with the board", or "Hub key changed". | — |
| Ping interval | "Every 3 min" (180 s). Choices 3, 5, 10 min; 5 and 10 only advised on the tailnet. | `setPingInterval` |
| **Send test notification** | — | `POST /v1/push/targets/{link id}/test`; the result arrives over the link. Long press: `sendLocalTest()` (no hub) |
| Backup: Web Push | "Also get alerts in the installed web app (uses Google's push service, encrypted, no account)." | Opens the hub's web app pairing page |

Maker guides (text in `src/lib/notify/makers.ts`, matched on lower-case manufacturer):

| Maker | Guide |
| --- | --- |
| samsung | Settings → Battery → Background usage limits → Never sleeping apps → add Watchyard |
| xiaomi, redmi, poco | Settings → Apps → Watchyard → Autostart on; Battery saver → No restrictions |
| oneplus, oppo, realme | Settings → Battery → Watchyard → Don't optimize; allow background activity |
| huawei, honor | Settings → Battery → App launch → Watchyard → Manage manually, all three on |
| others | Settings → Apps → Watchyard → Battery → Unrestricted |

Doctor rows come from the same state: connection age, battery exemption and
verification.

The words are in `src/lib/notify/link.ts` (`linkStatusWords`, `linkErrorWords`,
`verifiedWords`, `batteryWords`); the maker comes from `expo-device`'s `manufacturer`
(`AppInfo.maker`). A paired device with no `link` target (paired before the link existed, or by a hub
without it) shows "This phone has no link on the hub yet" and asks to pair again: the app does not call
`PUT /v1/devices/self/keys`, whose reply carries only `cert`, not the `hub_key` and `link.urls`
`configure` needs. The `revoked` and `refresh` events refresh the data at once (a revoked phone
then gets the 401 that shows Pair, P3).

## P7. Deep links

- Scheme `watchyard`. Accepted incoming URLs (an allow-list in `src/lib/links`; everything
  else opens Now):
  - `watchyard://now`
  - `watchyard://sessions/<id>[?alert=<al_…>]`
  - `watchyard://accounts[?seat=<id>]`
  - `watchyard://machines[?machine=<id>]`
  - `watchyard://alerts/<al_…>`
  - `watchyard://pair?u=…&s=…&c=…[&h=…&k=…]`
  - `watchyard://settings/notifications` (the **Settings** button of the link's persistent
    notification, push.md 14.4; the notification's own tap opens `watchyard://now`;
    `watchyard://settings` alone stays off the list)

  Ids must match `^[A-Za-z0-9_.:-]{1,80}$`.
- `hashToRoute(url)` maps a payload `url` (push.md 5.8: `/#/sessions/<id>?alert=…`,
  `/#/accounts?seat=…`, `/#/machines?machine=…`, `/#/`, `/#/alerts/<id>`) to the app route.
  Like `sw.js`, it rejects anything that does not start with a single `/` or that contains a
  backslash.
- `app/+native-intent.ts` (`redirectSystemPath`) runs every incoming URL through the
  allow-list. A pair link never claims by itself: it opens the Pair screen pre-filled, and
  the person taps **Pair with \<host>**. The link's `u`, `s`, `c` become the route params
  `origin`, `secret`, `code` (`/pair?origin=…&secret=…&code=…`), which the Pair screen reads;
  it shows a confirm card for them and claims only on that tap (and, when already paired,
  asks "Unpair from \<host>?" after it).
- Widgets link to `watchyard://now`, `watchyard://sessions[?filter=working|done]` and
  `watchyard://accounts?seat=<id>`. The Live
  Activity links to `watchyard://accounts?seat=<id>`.

## P11. Widgets

**`WidgetSnapshot` v1** (`src/contract/snapshot.ts`; the jest golden is
`test/fixtures/widget-snapshot.v1.json`):

```json
{
  "v": 1,
  "as_of": "2026-10-13T14:02:00.000Z",
  "hub": "hub.example.ts.net",
  "needs_you": 2,
  "top": {"seat": "work", "project": "api", "what": "asked", "since": "2026-10-13T13:58:00.000Z"},
  "seats": [
    {"id": "seat_…", "label": "work", "colour": "#7c5cff",
     "five_hour": {"pct": 62, "resets_at": "2026-10-13T17:00:00.000Z"},
     "seven_day": {"pct": 41, "resets_at": "2026-10-16T09:00:00.000Z"}}
  ],
  "next_reset": {"seat": "work", "window": "five_hour", "resets_at": "2026-10-13T17:00:00.000Z", "pct": 62},
  "private": false
}
```

- `buildSnapshot(overview, accounts, now, prefs)` (pure). There are at most 8 seats (iOS draws 4), ordered
  as the Accounts screen orders them: the hub's order (`seat.order`, set by the owner's
  drag-and-drop on the board, pwa.md 3.0; seats without one last). `top` is null in private mode. Labels are cut to 12
  characters.
- `buildTimeline(snapshot, now)` → entries `{date, props}`: one now, one at each `resets_at`
  in the next 24 h (that ring drops to 0 and reads "reset"), and one at `as_of + 60 min`
  that adds "as of 14:02" (stale). That makes at most 8 entries.
- **iOS** (`expo-widgets`, App Group `group.<bundle id>`): `NeedsYouSmall` (`systemSmall`:
  the needs-you count, the top item, and the next reset as `Text` `date` with `dateStyle:
  "timer"`) and `AccountsMedium` (`systemMedium`: up to 4 rings with pct and reset time).
  The app calls `updateTimeline(entries)` after every refresh. Widget components are pure
  props → `@expo/ui/swift-ui` trees, with no hooks or state (an expo-widgets rule).
- **Android** (`react-native-android-widget`), six widgets, all resizable except the ring (`resizeMode: horizontal|vertical`),
  `updatePeriodMillis: 1800000` (the library's minimum). They replace the earlier `WyNeedsYou`
  (2×2) and `WyAccounts` (4×2); iOS keeps `NeedsYouSmall` and `AccountsMedium`.
  - `WyLimits` (target 4×3): every visible account (up to 8) in Accounts order, each as its
    colour, the tighter of 5 h and 7 d (`tighterWindow`, a tie shows 7 d) as a bar and %, and
    that window's reset as a clock time ("5 h · 17:00"). An account with no reading, or a
    window whose reset has passed, reads 0% ("idle" / "reset"). It draws as many rows as the
    height allows (`rowsThatFit`, about 30 dp a row). A row opens `watchyard://accounts?seat=<id>`,
    the rest `watchyard://accounts`.
  - `WySessions` (target 3×2): **Running** (`counts.working`), **Needs input**
    (`counts.needs_you`: needs_permission, asked, failed) and **Done** (`counts.done`), as the
    Sessions screen's filters count them, and, when something needs you, a highlight line
    ("Needs you: api · asked"; no project in private mode). A cell opens Sessions with its
    filter (`?filter=working`, `?filter=done`); Needs input and the highlight open Now; the
    rest opens Sessions.
  - Four more widgets (same dark card, drawn from the same snapshot, no network):
    - `WyNeeds` (4×2, resizable): header "Needs you · N waiting" (N = `counts.needs_you`), then the
      top 3 waiting sessions oldest first from `needs_list`: account colour dot, "account ·
      project · state" (asked, needs permission, failed) and its age ("4 min"). A row opens
      `watchyard://sessions/<id>`; the rest opens Now. Nothing waiting: "Nothing needs you".
      Private mode: `needs_list` is empty, so it shows the count and "Open Watchyard to see
      which".
    - `WyRing` (2×2, `widgetFeatures: 'reconfigurable'`): one account, picked on the widget's
      configuration screen (`RingConfig`, registered with `registerWidgetConfigurationScreen`;
      Android opens it on placement and from Reconfigure). The pick is stored per widget id
      under `wy.widget.ring.v1` ({widgetId: seatId}, never a secret; dropped on
      `WIDGET_DELETED` and on unpair). It draws a ring (`SvgWidget`) of the tighter of 5 h and
      7 d with the %, the window and the reset words from `resetWords` (format/windows):
      "resets 15:48", "reset at 10:10" or, for an idle account, "0 %" and "not started". Tap:
      Accounts with that seat open (`?seat=`). An account that no longer exists asks to
      Reconfigure.
    - `WyNextReset` (2×1, resizable wider): "Next reset · claude-2 · 5 h" and the soonest reset
      across accounts as a clock time ("15:48", "Thu 09:00" when not today). Idle windows (no
      reset time) are ignored. Tap: Accounts.
    - `WyMachines` (4×1, resizable): header "Machines · 2 of 3 online", each machine with an
      online dot and "2 live", "idle" or "silent 1 h" (age of `last_seen`); as many per line
      as the width fits (about 104 dp each) and as many lines as the height fits. Tap: Machines.
    - **No ticking countdown.** react-native-android-widget draws RemoteViews from a fixed set
      of widgets and has no Chronometer or timer view, and a widget cannot redraw itself every
      second. So the reset is shown as a clock time, and an alarm redraws the widgets when it
      passes: after every render the handler asks the wy-link module `scheduleWidgetRefresh(at)`
      for the soonest reset still ahead plus 5 s (`nextRefreshAt`); `WidgetTick.kt` sets one
      `AlarmManager.setAndAllowWhileIdle` alarm (rough: it may be minutes late, and it needs no
      exact-alarm permission) whose receiver sends `ACTION_APPWIDGET_UPDATE` to every widget of
      the app, which runs the task handler (no network) and schedules the next one. A reboot
      loses the alarm; the widgets' first redraw after boot sets it again.
    - The snapshot gains `needs_list` (up to 3 `{id, seat, colour, project, what, since}`,
      oldest first, `[]` in private mode, no message text: only what `top` already allows plus
      the session id for the tap) and `machines` (up to 6 `{id, name, online, live,
      last_seen}`, from `overview.machines`, names cut to 12). Both are optional, so an older
      snapshot still validates. A seat window may now be `{pct: 0, resets_at: null}` (an idle
      window: not started); every widget reads it as 0 % "not started" and the Limits widget
      shows "not started" instead of skipping it.
  - **Brand header and the window toggle (all six).** Every widget's header row starts with the
    gold double-chevron mark (`assets/src/mark.svg`, inline in `widgets/android/Brand.tsx`,
    drawn with `SvgWidget`) and "Watchyard", or "Watchyard · Limits" / "· Sessions" / "·
    Needs you" where there is room; the 2×1 and 2×2 widgets show the mark and "Watchyard"
    only. `WyLimits` and `WyRing` have a tappable **5 h | 7 d** segmented toggle in the header
    (default 5 h): the widget then shows that window's % and reset words for every account (the
    ring: for its account), "resets 15:48" or, when idle, "0% · not started" (`windowRows`,
    from `windows.ts` `shownUse` and `resetWords`; an account without that window reads
    "not started"). The choice is per widget id, key `wy.widget.window.v1`
    ({widgetId: "5 h"|"7 d"}, dropped on `WIDGET_DELETED`). A segment's `clickAction` is
    `WY_TOGGLE_WINDOW` with `{window}`: the task handler gets `WIDGET_CLICK`, stores it and calls
    `renderWidget` with no app start; any other tap is an `OPEN_URI`, opened by an explicit intent (P13).
  - **Previews.** `app.config.ts` gives each widget `previewImage` (`assets/widget-previews/
    wy-*.png`, 2× PNG, under 40 KB each). They are rendered from an HTML replica of each
    widget's layout with sample data at the widget's dp size
    (`scripts/gen-widget-previews.py`: headless Chrome, no sandbox, transparent corners); keep
    the replica in step with the widgets by hand. The library has no `previewLayout`.
  - Both older widgets show "updated 3 min ago" (`agoWords(as_of, now)`) and, only when no snapshot has been
    written yet (a fresh install, or after unpair), "Open Watchyard once to load"; a tap then
    opens Now.
  - The snapshot (`wy.widget.v1`, no secrets) is written locally after **every** refresh,
    whatever `prefs.widgets` says: that pref only asks the hub for `refresh` frames over the
    link and gates the iOS timelines. (Writing it only when the pref was true left a widget
    with nothing to show.) Before `GET /v1/me` has answered, the snapshot is written as private.
    `buildSnapshot` adds `counts` {working, needs_you, done} from `overview.counts` and keeps up
    to 8 seats (`SNAPSHOT_MAX_SEATS`; iOS draws the first 4); `counts` is optional, so an older
    snapshot still validates and reads 0 for running and done.
  - The task handler reads the last snapshot (plus the link's newest `refresh` frame,
    `applyLinkRefresh`, which also moves `counts.needs_you`) and renders it on add, update and
    resize; the widget never calls the hub. It is registered in `phone/index.js`, the app's
    entry (`package.json` `main`), because Android runs a widget update in a headless JS task
    with no screen: a handler registered in `app/_layout.tsx` is not there when the app is not
    open. The app calls `requestWidgetUpdate` after each refresh; the wy-link service redraws
    on a `refresh` frame; otherwise the 30-minute period redraws (no network), so "updated N
    min ago" can lag by up to 30 min between refreshes. There is no background fetch.
    Android shows reset as a clock time, not a ticking timer (the library has no timer view; see the reset alarm above).
- Neither side reads the keychain or calls the hub. Widgets show only what the app last
  wrote.

## P12. Live Activity (iOS)

- `chooseCountdown(snapshot, now)` (pure) returns the seat × window with the highest pct
  among windows resetting within `LIVE_MAX_H = 8` hours and at ≥ 80%, else null. 8 hours is
  the commonly cited limit for an active Live Activity and is **unverified**. So the
  5-hour window qualifies and the 7-day window only in its last 8 hours.
- Props: `{seat, colour, window, pct, resets_at, started_at}`. The `ResetCountdown`
  component renders `Text` with `timerInterval: {lower: started_at, upper: resets_at}` (`@expo/ui`
  `ClosedRangeDate`) and `countsDown`
  (lock-screen banner, Dynamic Island compact, minimal and expanded regions).
- Started by the app in the foreground: **Count down on Lock Screen** on a qualifying card,
  or automatically when the app opens from a `limit_95` or rate-limit `failed` push, if
  Settings "Auto countdown" is on (default on). At most one runs at a time (`getInstances()`
  at start).
- "Auto countdown" (iOS only, Settings: "Count down on the Lock Screen when a limit push is
  opened") is a local app setting, not a hub pref: AsyncStorage key `wy.local.v1`
  (`LOCAL_PREFS_KEY`) holds `{auto_countdown: boolean}`, never a secret, and survives an unpair.
  Settings → Appearance picks the theme: Device default (the default), Light or Dark. It is kept
  under `wy.theme.v1` on this phone only and applied app-wide with `Appearance.setColorScheme`.
  Only a tap or **Open** counts (not Snooze or Seen). The payload carries no failure detail, so
  `isLimitPush` (`src/lib/integrate`) takes a `failed` push as a rate limit when its title ends
  in "rate limited" or its body is the "Resets 17:00 · …" line (push.md 5.5, 5.7); a private
  `failed` push never counts. The response sets a one-shot flag and kicks a refresh; the next
  refresh calls `syncCountdown(snapshot, now, true)`. It is updated after each refresh with the new pct. It ends with
  `end(after(resets_at + 15 min))` (`after` from `expo-widgets`; not the
  `end("after", …)` form) at the reset, and immediately on unpair.
- The activity is `createLiveActivity('ResetCountdown', component)` (`LIVE_ACTIVITY_NAME`).
  It needs no plugin option: the generated widget bundle always includes
  `WidgetLiveActivity()` (checked by the iOS prebuild check).
- No push-to-start and no push updates.

## P13. Build configuration

- `app.config.ts` reads only non-secret environment values:
  - `WY_APP_ID`: bundle id and package name, default `io.github.mraj602.watchyard` (the
    published app; never change it) or `io.github.mraj602.watchyard.dev` for a development build;
  - `WY_APP_VARIANT`: `development` allows cleartext traffic (P2's loopback hubs); any
    other value, or none, is a release config.
- `scheme: "watchyard"`; iOS `ios.deploymentTarget` 16.4 (the app config field: the
  `expo-build-properties` iOS option is deprecated since SDK 56, and `expo-widgets` reads
  the same field for its extension), Android `minSdkVersion` 24 (through
  `expo-build-properties`; 31 in the Android build below).
- Android `permissions: [POST_NOTIFICATIONS, CAMERA, RECORD_AUDIO, MODIFY_AUDIO_SETTINGS]` and `blockedPermissions:
  [SYSTEM_ALERT_WINDOW, READ_EXTERNAL_STORAGE, WRITE_EXTERNAL_STORAGE]`.
  `RECORD_AUDIO` is for claude.ai's microphone in a tab (P17.5); it is asked at run time, only
  when the page asks. `MODIFY_AUDIO_SETTINGS` is a normal permission granted at install (no
  prompt); the WebView needs it besides `RECORD_AUDIO`, or Chromium finds no recording device
  and the page reports the microphone as unavailable. Both lists are shown so the prebuilt manifest shows them (`wy-notify` and `wy-scan` also declare
  them, merged only at Gradle time).
- Plugins: `expo-router`; `expo-secure-store`; `expo-widgets` (`groupIdentifier: group.<WY_APP_ID>`, `bundleIdentifier:
  <WY_APP_ID>.widgets` for the extension, the two widgets, `enablePushNotifications: false`,
  `enableAndroid: false`); `react-native-android-widget` (the two widgets);
  `./plugins/with-wy-native.js`.
- `with-wy-native.js` (ours) sets:
  - iOS `NSSupportsLiveActivities: true`;
  - Android `allowBackup="false"`, plus `dataExtractionRules` and `fullBackupContent`
    (`res/xml/wy_data_extraction_rules.xml`, `wy_backup_rules.xml`) that exclude every backup
    domain from cloud backup and from device-to-device transfer;
  - the widget providers that `react-native-android-widget` writes get an `onReceive` that hands
    an `OPEN_URI` tap to `dev.watchyard.link.WidgetOpen` first, which starts the app's own
    launcher activity by component name, for `watchyard://` links only (the library's own
    tap is an implicit `ACTION_VIEW` any app with that scheme could answer). This plugin is
    listed before the widget plugin in `app.config.ts` so it runs last on those files;
  - removes `SYSTEM_ALERT_WINDOW` if a library adds it;
  - `usesCleartextTraffic` only in development builds;
  - Android Gradle: every `androidx.work` artifact is forced to 2.8.1 in `app/build.gradle`
    (`resolutionStrategy.eachDependency`). `react-native-android-widget` declares
    `work-runtime` 2.8.1, while `expo-widgets`' `androidx.glance` 1.2.0-rc01 pulls
    `work-runtime-ktx` 2.7.1; work 2.8 merged the ktx classes into `work-runtime`, so both
    together fail `:app:checkDebugDuplicateClasses`. Forcing 2.8.1 makes ktx an empty artifact.
    Revisit when Glance or the widget library moves to a common version.

**Android build.** Nothing in it needs an Expo, Google or store account: no EAS
profiles, no `extra.eas.projectId`, no `googleServicesFile` and no push credentials.

- Android `minSdkVersion` **31** (push.md 14.2: Keystore key agreement), `targetSdkVersion`
  the SDK 57 default.
- The `wy-link` module adds its permissions and components through its own
  `AndroidManifest.xml` (push.md 14.2); P13's `permissions` list stays as it is.
  `prebuild-check.mjs android` also asserts the `specialUse` service, its subtype property,
  `RECEIVE_BOOT_COMPLETED`, `REQUEST_INSTALL_PACKAGES`, that no
  `com.google.firebase` / `google-services` entry is present, that `expo-notifications`,
  `expo-camera` and `expo-application` are not dependencies, and runs
  `check-no-google.mjs static`. The module's manifest is merged
  by Gradle, not by prebuild, so the check reads it from `modules/wy-link` (and copies the
  local modules' `android/` sources into its temporary tree).
- **No proprietary Google code.** No Firebase, Google Play services, ML Kit, Play Install
  Referrer or other closed Google/Play library is in the APK, so it can go to F-Droid and
  IzzyOnDroid (android.md). `expo-notifications` (its Kotlin links `firebase-messaging`, and
  with it the Firebase, datatransport and installations classes) is replaced by `wy-notify`;
  `expo-camera`'s scanner (`play-services-code-scanner`, ML Kit) by `wy-scan`; and
  `expo-application` (Play Install Referrer) by the app config `expo-constants` embeds.
  `scripts/check-no-google.mjs` enforces it in three ways: `static` (every installed package's
  and local module's `build.gradle`, test, androidTest and debug-only lines aside; run by
  `prebuild-check.mjs android`), `gradle` (`./gradlew :app:dependencies --configuration
  releaseRuntimeClasspath` in a prebuilt `android/`; CI's `no-google` job on every change) and
  `apk` (the classes defined in the APK's `classes*.dex`; CI's `android-debug` job on a release
  build, and `release-apk.sh` before it publishes). The groups: `com.google.firebase`,
  `com.google.android.gms`, `com.google.mlkit`, `com.google.android.play`,
  `com.google.android.datatransport`, `com.android.installreferrer`,
  `com.android.billingclient`. (`com.google.zxing` and `com.google.crypto.tink` are open
  source and allowed.) `expo-dev-launcher` declares the Play services code scanner as
  `debugOnly`: it is in debug builds, never in a release.
- There is no background notification task: there are no silent pushes.
- The glue uses the `wy-link` module only on Android, when the module is present (`systemLink`).
- **Build:** `make phone-apk-release` (run it by hand): `expo prebuild --platform
  android --clean` with `WY_APP_VARIANT=production`, then `./gradlew
  assembleRelease` in `phone/android`. Output `phone/dist/watchyard-<version>.apk`, its
  `SHA256SUMS` (the APK's line) and `manifest.json` (P16.1). A development build for a manual check is
  `expo prebuild` + `./gradlew assembleDebug`.
- **Signing:** your own release keystore, outside the repo. The make target refuses to start
  without it: `WY_ANDROID_KEYSTORE` (path), `WY_ANDROID_KEY_ALIAS` and
  `WY_ANDROID_KEYSTORE_PASSWORD` (and `WY_ANDROID_KEY_PASSWORD` when it differs), or the same
  four values as `storeFile`, `keyAlias`, `storePassword`, `keyPassword` in a properties file
  named by `WY_ANDROID_SIGNING_PROPERTIES` (also outside the repo; an environment variable
  wins). The passwords reach Gradle (`scripts/wy-release-signing.init.gradle`, an init script,
  because `expo prebuild --clean` regenerates `android/`) and keytool only through the
  environment, are never printed and never written into `phone/`. `check-no-secrets.mjs`
  already refuses keystores in the tree. Keep two offline copies: losing it means every phone
  uninstalls and reinstalls. The certificate's SHA-256 fingerprint (read from the keystore and
  checked against the built APK with `apksigner verify --print-certs`) is printed by the make
  target and goes into `manifest.json` as `cert_sha256`. No `make` target creates a keystore;
  `phone/README.md` documents the `keytool` command.
- **Release gate:** after the build, `phone-apk-release` runs `aapt2 dump xmltree` on the APK and
  `scripts/check-apk-manifest.mjs`, and stops when the package id is not
  `io.github.mraj602.watchyard` (or `WY_APP_ID`), cleartext traffic is allowed, the APK is
  debuggable, or `allowBackup` is not `false`. It also stops when `aapt2` is missing.
- **Version:** `app.config.ts` sets Android's `versionCode` (the manifest's `build`) to
  `MAJOR*10000 + MINOR*100 + PATCH` of `version`, so it only rises with the version.
- **Release:** a GitHub release tagged `phone-vX.Y.Z` with the APK, `SHA256SUMS` and
  `manifest.json`, made by you.

## P14. Tests and fixtures

- `test/with-wy-native.test.ts` unit-tests the plugin's Gradle edit (`alignWorkVersions`:
  forces the `androidx.work` group, idempotent). `make phone-android-debug` is the real proof.
- `test/setup.ts` (jest `setupFilesAfterEnv`) replaces `modules/wy-notify`,
  `expo-secure-store`, `modules/wy-scan`, `modules/wy-link`, `modules/wy-tabs`, `expo-widgets`, `@expo/ui/swift-ui` (and `/modifiers`),
  `react-native-android-widget`, `expo-task-manager` and AsyncStorage with the recording fakes
  of `test/fakes.ts`, makes global `fetch` reject, and resets every fake before each test.
  Tests script the fakes through their exported state (`notificationsState.permission`,
  `secureState.locked`, …) and read `calls` / `callsOf(prefix)`. The secure-store fake never
  records a stored value. `emitNotificationResponse` and `runTask` play the OS's part.
- Widget components render with `test/render-widget.ts` (`renderWidget`, `allOf`, `textOf`),
  which uses `test-renderer` directly with `SwiftUI.Text` as the text host. RNTL's `render`
  does not fit widget trees: it only lets React Native's `Text` hold strings. Every
  `@expo/ui/swift-ui` component renders as a host element `SwiftUI.<Name>` and every
  `react-native-android-widget` component as `Android.<Name>`; modifier helpers return
  `{modifier, args}` and are recorded.
- `test/fixtures/hub/*.json` are `webui/devserver` replies (`phone/scripts/dump-fixtures.sh`,
  paired as a phone with `cookie: false`), with `_meta.json` giving the time they were taken.
  The devserver's data is relative to its start, so tests that format times use
  `_meta.json`'s `now_ms`. `test/fixtures/widget-snapshot.v1.json` is the P11 example.
- `peers-matrix.json` and `peers-audit.json` in the same folder are the devserver's fresh matrix
  (version 1, nothing allowed) and audit (`GET /v1/peers/matrix`, `/v1/peers/audit`);
  `validateMatrixInfo` and `validatePeerAudit` check them, and the Peers screen tests start from
  them (`test/screens.test.tsx`, `test/peers-format.test.ts`).
- `test/contract.test.ts` checks the Go payload goldens with
  `validatePayload(…, exact)` (Go key order, no unknown keys), and every fixture with its
  validator. `placeholder-alert.json` and `placeholder-refresh.json` stand in until the `message-*.json`
  goldens replace them.
- `test/search.test.ts` runs `src/lib/format/search.ts` and `webui/static/components/search-fmt.js`
  on one case table (same terms, matches, ranges and excerpts); `test/sessions-search.test.tsx`
  renders the Sessions screen, types a query, refreshes the data and checks the query, the
  rows, the counts and the extra match line.
- `test/imports.test.ts` walks the import graph from `src/lib` and `src/contract` and fails on
  any package import or a relative import that leaves them (P1). `eslint.config.js` also
  forbids `react`, `react-native*`, `expo*`, `@expo/*` and `@react-native*` in `src/lib/**`
  and `console` in `src/**`.
- `scripts/check-no-secrets.mjs` fails on a credential-shaped file in `phone/` (outside
  `node_modules`, `android/`, `ios/`) or anywhere in `git ls-files`, and on a private key block
  or service-account JSON in a tracked `phone/` file. `.env.example` / `.env.sample` are allowed.
- `scripts/prebuild-check.mjs android|ios` copies `phone/` to a temp directory (with
  `node_modules` symlinked), runs `expo prebuild --platform <p> --no-install --clean` there
  with `WY_APP_VARIANT=production`, asserts the generated native config (P13) and deletes
  the copy (`WY_KEEP_PREBUILD=1` keeps it for a look).

## P15. Facts checked about the toolchain

Checked on 2026-10-13 on Fedora Linux (Node 22.22.2, no Xcode, no Android build), with the
pins of P1:

| # | Fact | Result |
| --- | --- | --- |
| 1 | `expo prebuild --platform android --no-install --clean` succeeds with every P13 plugin; the manifest has `POST_NOTIFICATIONS`, the `watchyard` intent filter, two `AppWidgetProvider` receivers (`.widget.WySessions`, `.widget.WyLimits`) and `allowBackup="false"` | **Checked.** Needed one change: POST_NOTIFICATIONS is only merged from expo-notifications' library manifest at Gradle time, so P13 now lists it in `android.permissions`. Two `appwidget-provider` XML files are generated; react-native-android-widget's plugin writes to the right path (no doubled `android/android`). Cleartext is off in release. |
| 2 | `expo prebuild --platform ios --no-install --clean` succeeds on Linux, with `Info.plist`, entitlements and the widget extension target | **Checked.** `ios/ExpoWidgetsTarget/` holds `NeedsYouSmall.swift`, `AccountsMedium.swift`, `index.swift` (the bundle, which includes `WidgetLiveActivity()`), `Info.plist` and its entitlements. The App Group is on the app and the extension, `aps-environment` on the app, the app-extension target is in `project.pbxproj`, and every target is iOS 16.4. Pods are not installed (`--no-install`), so this proves config, not compilation (the native compile is not covered). The iOS prebuild check stays in `make phone-check` and CI's `check` job. |
| 3 | `expo-widgets@57.0.22` and `react-native-android-widget@0.22.1` coexist | **Checked.** Both plugins run in the same config without duplicate keys; each platform's prebuild generates its own widget set and the other plugin leaves it alone (`expo-widgets` has `enableAndroid: false`). No fallback needed. |
| 4 | `jest-expo` renders `@expo/ui/swift-ui` widget trees through the mock | **Checked**, with `test/render-widget.ts` rather than RNTL's `render` (P14). `test/widget-mock.test.tsx` renders a `VStack` with a timer `Text` and an Android `FlexWidget`. |

Notes from the same checks:

- `babel-preset-expo` adds a widgets transform whenever `expo-widgets` is installed: widget
  components are functions marked with the `'widget'` directive, serialised for the native
  side. Components must stay pure (props in, tree out; no hooks, no closures over module
  state).
- `expo-widgets` warns about `userInterfaceStyle` on Android without `expo-system-ui`. That is
  harmless; adding `expo-system-ui` is the way if it wants the Android dark-mode setting.

## P16. Updater

The app is sideloaded (no store), so it updates itself, with your confirmation each time.

### P16.1 Release manifest

`manifest.json`, published with each `phone-vX.Y.Z` release and mirrored by the hub:

```json
{"v":1,"version":"0.9.0","build":14,"min_sdk":31,"apk":"watchyard-0.9.0.apk","size":48213004,
 "sha256":"<64 hex>","cert_sha256":"<64 hex, signing certificate>","notes":"…","published_at":"…"}
```

### P16.2 Hub mirror

- Once a day (and at `wy hub serve` start), the hub asks GitHub for the project's releases
  (`GET https://api.github.com/repos/<owner>/<repo>/releases?per_page=20`, no token,
  `netguard` client), takes the newest non-draft, non-prerelease release whose tag matches
  `^phone-v\d+\.\d+\.\d+$`, downloads `manifest.json`, `SHA256SUMS` and the APK, checks the
  APK's SHA-256 against both, and keeps it in `<data>/app/android/` (the current and the
  previous version only). The build pins the release signing certificate
  (`appmirror.ReleaseCertSHA256`, set in `pin.go`; a fork may build it empty). With no pin the
  mirror makes no request and logs that it is off, once per run. With a pin, a release is
  published only when `manifest.json`'s `cert_sha256` and the first certificate of every
  signer in the APK's v3 (or v2) signing block all equal the pin. The mirror reads the
  certificate from the file; it does not verify the signature, which Android and the phone
  do. The repository comes from the build (`version.Repo`); setting
  `app.android_mirror = false` turns the mirror off.
- The off switch and the repository also exist as `wy hub serve` flags (`--no-app-mirror`,
  `--app-repo owner/name`) and as the settings `app.android_mirror` (JSON bool) and
  `app.android_repo`; a flag wins. Downloads follow at most 5 https redirects (GitHub's assets
  redirect to a CDN) and still refuse non-public addresses (`netguard`). A release that does
  not verify is never published, and what is already mirrored stays.
- `GET /v1/app/android` (any device token with `read`) → the manifest plus `"url":
  "/v1/app/android/apk"`, or 404 when nothing is mirrored. `GET /v1/app/android/apk` streams
  the APK (`Content-Type: application/vnd.android.package-archive`, `ETag` = sha256).
- The hub never re-signs or changes the APK; it only serves what the release holds.

### P16.3 In the app

- `WyLink.checkUpdate(origin, token)` at most once a day (and from **Check for update**):
  first the hub (`GET /v1/app/android`), then, if the hub has none or is unreachable, GitHub
  directly (the same release filter as P16.2). An update is offered when `build` is higher
  than the installed `versionCode` and `min_sdk` fits.
  `origin` must be the paired hub's stored origin (`ERR_WY_UPDATE` otherwise), so the bearer
  token can go nowhere else; with no stored hub the check uses GitHub only.
- The app (`src/lib/update`) calls it on start when paired and at most every 24 hours, and the
  offer is remembered until installed. A failed check does not count as a check.
- Settings → App version has an **Updates** row (Android): the state ("Not
  checked yet", "Checking…", "Up to date", the error), **Check for update** (never throttled)
  and, when one is offered, the notes, the source ("your hub" or "GitHub") and **Install**.
- Settings shows "Update 0.9.0 available" with the notes. **Install**: download to the app's
  cache, check the SHA-256 against the manifest, read the APK's signing certificate
  (`PackageManager.getPackageArchiveInfo(…, GET_SIGNING_CERTIFICATES)`) and require it to
  equal **both** the manifest's `cert_sha256` and the installed app's own certificate, then
  hand it to `PackageInstaller` (session install). Android asks you to confirm; the first time
  it also asks to allow installs from this app.
- A wrong checksum or a different signing certificate is refused ("This update is not signed
  by your key") and the file is deleted. Android itself also refuses an update signed by
  another key.
- Data is kept across updates (same package, same key). After `MY_PACKAGE_REPLACED` the link
  service restarts (push.md 14.4).
- Google's developer verification for sideloaded apps (from 2027) does not change this flow;
  if a phone requires it, install once with Android's "advanced flow" or `adb install` ).

## P17. claude.ai account tabs (8b)

Android only (`minSdkVersion` 31). One claude.ai web tab per seat, each its
own WebView profile, so every account stays signed in at once. **Open in Claude** opens a
session's Remote Control link in its seat's tab. Watchyard asks claude.ai only for the
account id in the tab, as the desktop does (desktop.md C5, P17.7); it never reads the tabs'
chats, page content, cookies, storage or usage, and sends nothing from them to the hub.

### P17.1 The `wy-tabs` module

A local Expo module, `phone/modules/wy-tabs` (Kotlin, `dev.watchyard.tabs`), with one
dependency, `androidx.webkit:webkit` (the profile API).

| JS | Native |
| --- | --- |
| `available` | The module is in this build (Android) |
| `isSupported(): Promise<boolean>` | `WebViewFeature.isFeatureSupported(MULTI_PROFILE)` (WebView 110 or later) |
| `listProfiles(): Promise<string[]>` | The seat ids of `ProfileStore.getAllProfileNames()` that are tab profiles |
| `deleteProfile(seat): Promise<boolean>` | `ProfileStore.deleteProfile(name)`: cookies, storage and cache of that tab. Rejects `ERR_WY_TABS_IN_USE` while the tab is open, `ERR_WY_TABS_SEAT` on a bad seat id. WebView keeps the profile of a tab that was open in this process "in use" even after the tab is closed and its WebView destroyed, so `deleteProfile` then wipes its cookies and storage, hides it from `listProfiles` and deletes it for real at the next app start (before any WebView exists); opening the tab again cancels that |
| `moveProfile(from, to): Promise<boolean>` | P17.7 "Use this tab for … instead": the profile `from` has becomes `to`'s (a `moves` entry in the module's preferences; the profile, its sign-in and cookies are not touched, read or copied). Rejects `ERR_WY_TABS_TAKEN` when `to` has a tab, `ERR_WY_TABS_NONE` when `from` has none, `ERR_WY_TABS_IN_USE` while that tab is open, `ERR_WY_TABS_SEAT` on a bad seat id |
| `<TabView seat url onTabState>` | A `WebView` bound with `WebViewCompat.setProfile` to the seat's profile (made on first use) before anything loads, then `loadUrl(url)` |

- **Profile name:** `wy-tab-` + the hub seat id (`^seat_[0-9A-HJKMNP-TV-Z]{26}$`), never an
  email or label. Other names (and `Default`) are never listed or deleted. A profile belongs
  to the seat it was moved to (`moves`), else to the seat its name carries; a seat whose own
  name was moved away gets `wy-tab-<seat>.<n>` (the first free `n`) for its next tab. Deleting
  a moved profile drops its `moves` entry.
- **`onTabState`** `{state, url?, status?, height?, code?, reason?, account?}`: `loading` (main frame started), `loaded`
  (main frame finished, `height` = content height 3 s later), `http` (main-frame HTTP error,
  `status`), `error` (main-frame network error, `code`; or `reason` `unsupported` / `profile` when no tab could be made), `crashed` (renderer gone; the view drops its
  WebView), `download` (a download was refused), `external` (a link went to the browser),
  `url` (an in-page navigation, `doUpdateVisitedHistory`: claude.ai is a single-page app),
  `account` (P17.7: `account` = the account uuid claude.ai named, or `status` 401 signed out /
  0 no answer / another HTTP status).
- **Lifecycle:** the screen mounts one `TabView` at a time. Cookies are flushed when the view
  is detached and when the app pauses; profiles are on disk in the app's data folder, so they
  survive restarts and updates (P16.3: same package, same key) and go with an uninstall or
  "Clear data". A seat change on a mounted view rebuilds the WebView.

### P17.2 Tab model and routing

There is no tab list of our own: a seat has a tab once its profile exists. `src/lib/tabs`
(pure) decides where a link opens, `openTarget(session, {supported})`:

1. `remote_url` not an `https://claude.ai/…` link → browser (or nothing when not https).
2. Tabs not supported (no module, not Android, WebView without `MULTI_PROFILE`) → browser,
   reason `unsupported`.
3. No seat, or a seat id that fails the pattern → browser, reason `unattributed`.
4. Else the tab of `session.seat`, at `remote_url`, even if that tab was never opened (it then
   shows the sign-in).

The seat comes from the hub (`Session.seat`, identity.md 1), so a session from account B always
opens in B's tab. **Open claude.ai** on an Accounts card opens the seat's tab at
`https://claude.ai/`.

### P17.3 Screens

- `/tabs/[seat]?url=` (`ClaudeTabScreen`): a header with the account's dot and name,
  **Open in browser** and **Close**, then the `TabView`. The sign-in hint ("Sign in with your email
  and the code claude.ai sends. Google sign-in does not work inside apps.") shows with a **×** only
  while the tab is on a sign-in page (claude.ai `/login`, `/logout`, a `anthropic.com` or
  `claude.com` hop, Google's sign-in) or is known signed out (the account check answered 401),
  using the P17.7 rule over this visit's events. Once the tab is signed in it stays hidden across
  navigation; `/magic-link` and `/oauth` pages show it only while the tab is not known to be
  signed in. Nothing loaded yet shows nothing. **×** hides it for that account for good (AsyncStorage
  `wy.tabs.hint.v1`, seat ids only); Settings → Sign out (clear this tab) brings it back.
- Accounts (P17.8): a compact list; a row opens in place and shows where its login is signed in
  and offers **Open claude.ai**, **Sign in on this phone** or **Fix this tab** (P17.7).
- Settings → **Claude tabs**: one row per profile (seat name, or "Removed account" for a seat
  the hub no longer lists) with **Sign out (clear this tab)**, which confirms, then
  `deleteProfile` (and forgets the tab's memo, P17.7). Shown only when supported; otherwise
  "This phone's WebView cannot keep separate claude.ai sign-ins; links open in the browser."
  It carries the privacy note of P17.7.

### P17.4 Fallback

claude.ai may refuse embedded WebViews. `assessTab(events, elapsed)` (pure) turns the events
into `ok`, `waiting`, `refused` (a main-frame `http` 401, 403, 429 or 451 not followed by a
`loaded` page with content within 10 s: Cloudflare's challenge also starts with a 403),
`blank` (`loaded` with `height` 0, or `about:blank`), `failed` (`error`, `crashed`) or
`timeout` (no `loaded` within 20 s). On anything but `ok` and `waiting` the screen shows
"claude.ai did not load in this tab" with the reason and **Open in browser** (the tab's current
claude.ai URL through `Linking.openURL`). It does not switch on its own, so a slow page is not lost.

### P17.5 Security

- No `addJavascriptInterface` and no script on page load. The one script is the account
  check of P17.7 (`evaluateJavascript`, on claude.ai itself only): it asks claude.ai only for
  the account id in the tab, as the desktop does (desktop.md C5), and never reads chats, the
  page, cookies or storage. JavaScript and DOM storage are on (claude.ai needs them); file and
  content access, geolocation and mixed content are off; Safe Browsing stays on;
  `setSupportMultipleWindows(false)`.
- Watchyard never calls the profile's `CookieManager.getCookie` or `WebStorage` reads; the
  module only flushes, deletes and reassigns (P17.7) profiles.
- **URL allow-list** (main frame, `shouldOverrideUrlLoading` and the first `loadUrl`):
  `https:` only, host `claude.ai`, `anthropic.com`, `claude.com`,
  `challenges.cloudflare.com` or a subdomain of one (desktop.md C2's list) → in the tab. Any
  other `https:` or `mailto:` link → the browser (`ACTION_VIEW`, `external` event). Anything
  else (`http:`, `intent:`, `javascript:`, `file:`, `data:`, custom schemes) is dropped.
  Subframes are not filtered (sign-in and payment widgets live there).
- Downloads are refused (`setDownloadListener` reports `download`, saves nothing).
  Geolocation and every permission request other than the two below are denied.
- **Microphone and camera** (`onPermissionRequest`, `TabMedia`): only `AUDIO_CAPTURE` and
  `VIDEO_CAPTURE`, and only when the requesting origin is `https://claude.ai` or a subdomain
  (default port; `TabPolicy.grantable`). The Android runtime permission (`RECORD_AUDIO`,
  and `CAMERA` for video; `MODIFY_AUDIO_SETTINGS` is declared and granted at install) is asked first; the page's request is granted only if it is granted
  and denied otherwise.
- **File chooser** (`onShowFileChooser`): launches `FileChooserParams.createIntent()` (so the
  page's accept types and multiple-selection apply). When the accept types include images
  (`image/*`, `*/*` or an image extension), a camera option (`ACTION_IMAGE_CAPTURE`, written
  to a `FileProvider` URI in the app's private cache `wy-tabs-camera/`, declared in the
  module's manifest, authority `<WY_APP_ID>.wytabs.files`, per-URI grants only) is offered in the
  chooser, after asking for `CAMERA` (without it only files are offered). The result is
  `FileChooserParams.parseResult` or the photo URI; the page's callback is answered exactly
  once (null on Cancel, a new chooser or a closed tab). Earlier photos are deleted at the
  next chooser. The module reads nothing of what the page shows or what is chosen.
- Pasting a screenshot from the clipboard into the page is not handled by the app (handing it
  over would need script in the page, which P17.5 forbids); attach a photo or file through the
  chooser.
- Certificate errors are never overridden. WebView debugging stays off.

### P17.6 Limits

- Needs WebView 110+ with `MULTI_PROFILE`; older phones open links in the browser.
- Sign-in is email and code only; Google sign-in is blocked in WebViews by Google.
- Attachments go through the system file chooser (and the camera, P17.5); pasting an image from the clipboard is not supported.
- A refusal that claude.ai serves as a normal page with content cannot be told apart without
  reading the page, which the module never does; **Open in browser** is always in the header.
- The phone tab line of P17.7 is what the tab showed when it was last open; a sign-out made
  elsewhere (claude.ai's "log out of all devices") shows only at the next visit.

### P17.7 Which account is signed in where

The same rules as the desktop app (desktop.md C5): matched by the **Anthropic account uuid**,
never by email.

1. **Signed in or not**, from the tab's URL only: a claude.ai page (or a subdomain) whose path
   is not `/login` or `/logout` is signed in (the desktop's `isReadableUrl`); claude.ai's login
   or logout page, or a sign-in hop (`anthropic.com`, `claude.com`), is not. Taken from `loaded`
   (with content) and `url` events.
2. **Which account.** On a signed-in page of `claude.ai` itself (once per page load, and when an
   in-page navigation leaves the login page), the view runs one script with
   `evaluateJavascript`: a same-origin `GET /api/account` with the tab's own credentials,
   keeping only `uuid` (`^[0-9a-f-]{36}$`); a redirect to the login page counts as 401. The
   answer waits in a nonce-named `window` property that the view polls (every 300 ms, up to
   15 s) and deletes; a new page cancels the check. No organizations call is needed: the seat
   is matched by its account. Only the uuid or the status leaves the page, as an `account`
   event. Nothing from it is sent to the hub (the desktop's account read sends nothing either;
   its usage records are a separate feature the phone does not have).
3. **Memo.** The tab screen keeps, per seat, `{signed_in, account, at}` from those events in
   AsyncStorage `wy.tabs.v1` (local, never sent; no cookie, email or page content). An
   `account` answer wins; 401 means signed out; other statuses change nothing. The Accounts
   screen reads the memo, so it says the state of every tab without opening one. Chosen over a
   hidden WebView check when Accounts opens: that would load claude.ai in every profile on each
   visit, and a WebView that has used a profile keeps it "in use" for the rest of the process,
   which would turn every later sign-out into the deferred delete of P17.1. The line therefore
   carries "as of the tab's last visit, \<ago>".
4. **Phone tab line** and action:

   | State | Line | Action |
   | --- | --- | --- |
   | No profile for the seat | Phone tab: not signed in | **Sign in on this phone** |
   | Profile, no memo yet | Phone tab: not checked yet. Open it to check. | **Open claude.ai** |
   | Signed out | Phone tab: not signed in | **Sign in on this phone** |
   | Signed in, account not read | Phone tab: signed in | **Open claude.ai** |
   | Account = the seat's | Phone tab: signed in, matches this account | **Open claude.ai** |
   | Account = another seat's | Phone tab: signed in as **\<seat>**, not this account | **Fix this tab** |
   | Account no seat has | Phone tab: signed in as an account no seat has | **Fix this tab** |

   No line and no action where tabs are not supported.
5. **Claude Code line**, from the hub (`Seat.places` with a `slot`: signed in; `Seat.signed_out`:
   a folder that logged out of this seat, api.yaml): "Claude Code: signed in on **laptop**,
   **worker** · signed out on **x**" (machine names, each once; a machine where it is also
   signed in is not listed as signed out), or "Claude Code: not signed in on any machine".
6. **Fix this tab**, a bottom sheet: "\<seat>'s tab is signed in as \<other>",
   the account-id note, the two rows, then:
   - **Sign out here and sign in as \<seat>**: `deleteProfile(seat)`, forget the memo, open the
     tab at `https://claude.ai/login`.
   - **Use this tab for \<other> instead**, only when the other seat has no tab on this phone:
     `moveProfile(seat, other)` and the memo moves with it. Not offered for an account no seat
     has.
   - The note: 'Watchyard asks claude.ai only "which account is this?" inside the tab. It never
     reads your chats or sends the page anywhere.' (also in Settings → Claude tabs).
7. **Never** typed into a login page, never a cookie read into JS or Kotlin, never page content
   stored or sent.

### P17.8 The Accounts list

Calm and compact: seven accounts fit on one screen.

- **Header:** "Accounts" with **+ Add account** (gold outline, P17.3, tabs only) at the right;
  under it **Expand all** / **Collapse all** (text button, from two accounts up).
- **Most room right now:** "Most room right now: claude-2 (5 h 0% · 7 d 3%)", the board's and
  the hub's rule. Among visible accounts the *effective use* of a window is 0 when its
  `resets_at` has passed or it is idle (api.yaml `Window.state` `idle`: claude.ai has not
  started it, 0 % with no reset time), or when the account has no reading for it but has one for the other
  window; otherwise its `used_pct` (a reading older than 6 hours counts until its reset). An
  account with no reading at all is skipped. Score = the larger of the two; lowest wins, ties go
  to the lower 7 d, then the lower 5 h use, then list order; named only under 80%. An idle
  account therefore has the most room.
- **One row per account:** colour dot, name, one bar for the tighter of the 5 h and 7 d windows
  (effective use; a tie shows 7 d, or 5 h when the account has no 7 d window) with "5 h" or "7 d"
  and the percentage (red from 90%), and that window's reset words beside the bar ("resets
  15:48", "resets Sun 09:00"; "reset at Sat 10:10" once that time has passed, never "resets now";
  "not started" for an idle window, which shows 0 %). No reading: "no data". The words are
  `resetWords` in `lib/format/windows.ts` (with `windowPhase` and `shownUse`); the Now rings use
  the same phases ("not started", "reset 10:10").
- **One status line, only when something needs attention**, the first of: the phone tab is
  signed in as another account (**claude-6**, or an account no seat has) > near its limit (a
  runaway 5 h pace or 5 h at 90% or more: "Near the 5-hour limit: full by 15:48"; 7 d at 90% or
  more: "7-day limit nearly used, resets Sun 09:00") > "Claude Code signed out on **machine**".
  Others are counted as "+N".
- **Tap to open in place** (`LayoutAnimation`; `?seat=` opens that row): every window (label,
  % used, meter, "resets in 2 h 10 min (15:48)", "reset at Sat 10:10" or "not started · starts
  when you next use this account", peak, pace); **Signed in** (the Claude Code
  line per machine, the phone tab line with "as of the tab's last visit", and its action: **Open
  claude.ai**, **Sign in on this phone** or **Fix this tab**, which opens the sheet of
  P17.7); then the details: the plan and organisation, email, live sessions and when claude.ai
  usage was read, and under them **Usage and the picker**, a closed disclosure (the only place
  these show; no screen or tab of its own) that loads `GET /v1/accounts/{seat}/relation` when
  first opened (again when opened more than 5 minutes later) and shows what the board shows,
  in the same words and order (pwa.md 3.0a): the verdict in words (its place in line
  explained, the kind with its definition, each rule with its value against its threshold,
  when it frees up), the three tiles of the 5-hour limit and the week (stacked), the session
  cost line, the charts (a title saying what a higher bar means, a y axis with values, "no
  data" gaps, model colours with a legend of full names, a tapped day's value under the chart,
  one "Tap a day to see its value." hint under the first chart only, an `accessibilityLabel`
  summing each chart up; a chart with no data is replaced by one line), the capacity sentence
  per model (a chart only once both weeks have a value) and **How this works** at the end. Its
  pure logic
  is in `lib/format/relation.ts` (jest), the same functions and strings as the board's
  `relation-fmt.js`. Above the details, **How should the picker use this account?** (pwa.md
  3.0a; setup.md 8.5 *Kind*): for the owner three options (Protected, Aggressive, Not chosen)
  with their one-line definitions under each, saved at once with `PATCH /v1/accounts/{seat}`
  `{"mode": …}` (a refusal puts the old choice back and says why); for anyone else the
  current choice with its definition. "Not chosen yet; treated as aggressive." is highlighted
  when no mode is set, and a local override on the verdict's machine is named as on the board.
  The wording comes from one place, `lib/format/pickMode.ts` (`MODE_WORDS`), also used by the
  verdict; and **Count down on Lock Screen** where the Live
  Activity qualifies (iOS, P12).
- **Under the list**, one muted line: "The bar shows the tighter of the 5 h and 7 d limits. Tap
  an account for everything else."
