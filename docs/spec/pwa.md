# The installable web app (phone and browser)

Status: spec v1. Implemented in `webui/` (static files, `webui/serve.go`),
`webui/devserver` (mock hub), `webui/smoke` (browser tests), `scripts/icons/`. Pairing is in
[pairing.md](pairing.md), alerts and the push payload in [push.md](push.md).

## 1. One app, not two

The phone app is the web board, extended: the same single-page app, the same ES modules,
no build step, served by the hub at `/`. CSS already switches between a sidebar (desktop) and
bottom tabs (phone). It adds an install manifest, a service worker, pairing, push
registration and the More / Alerts / Settings screens. Reasons: one code path for the data
(store.js, the stream, the read API), screens that already match the designs, and nothing to
publish. The native phone app (built with Expo, phone.md) is the separate native app.

**Requirement:** install and Web Push need a secure context, so the phone must open the hub at
an `https://` address with a publicly trusted certificate (`tailscale serve --bg 8787` gives
`https://<host>.<tailnet>.ts.net`; Caddy on a VPS). The plain-HTTP tailnet bind in
`deploy/docker-compose.tailscale.yml` still works as a board but cannot install or push; the
deploy README and `wy doctor` say so.

## 2. Manifest and icons

`/manifest.webmanifest` (served as `application/manifest+json`):

```json
{
  "id": "/",
  "name": "Watchyard",
  "short_name": "Watchyard",
  "description": "Your Claude sessions and limits. Unofficial; not affiliated with Anthropic.",
  "start_url": "/#/",
  "scope": "/",
  "display": "standalone",
  "orientation": "portrait",
  "background_color": "#17191B",
  "theme_color": "#17191B",
  "icons": [
    {"src": "icons/icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
    {"src": "icons/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": "icons/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
    {"src": "icons/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"}
  ],
  "shortcuts": [
    {"name": "Needs you", "url": "/#/", "icons": [{"src": "icons/icon-192.png", "sizes": "192x192"}]},
    {"name": "Sessions", "url": "/#/sessions", "icons": [{"src": "icons/icon-192.png", "sizes": "192x192"}]}
  ]
}
```

`index.html` adds: `<link rel="manifest" href="manifest.webmanifest">`,
`<link rel="apple-touch-icon" href="icons/apple-touch-icon-180.png">`,
`<meta name="theme-color" content="#F3F4F1" media="(prefers-color-scheme: light)">` and the
`#17191B` dark one, `<meta name="apple-mobile-web-app-title" content="Watchyard">`,
`<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">` (the
layout already uses `viewport-fit=cover`; tabs pad with `env(safe-area-inset-bottom)`).

**Icons** come from the logo (`components/nav.js`: two nested chevrons, outer `M3 18 L11 4
L19 18`, inner `M7 18 L11 11 L15 18` in a 22-unit box, stroke 2): both chevrons amber
`#E8B04B` on `#17191B`. Source `scripts/icons/icon.svg`:

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
  <rect width="512" height="512" fill="#17191B"/>
  <g transform="translate(80 80) scale(16)" fill="none" stroke="#E8B04B" stroke-width="2"
     stroke-linejoin="miter" stroke-linecap="butt">
    <path d="M3 18 L11 4 L19 18"/>
    <path d="M7 18 L11 11 L15 18"/>
  </g>
</svg>
```

At scale 16 the drawing (apex miter included) stays within 193 px of the centre, inside the
205 px safe circle of a maskable icon, so the same art serves `any` and `maskable`.
`scripts/icons/gen.sh` renders, with `rsvg-convert` (librsvg) or, if absent, headless Chrome
through the smoke-test puppeteer setup: `icon-192.png`, `icon-512.png`, `maskable-512.png`,
`apple-touch-icon-180.png` (opaque, square: iOS rounds it), and `badge-96.png` (the chevrons
white on transparent, for Android's monochrome status-bar `badge`). The PNGs are committed
under `webui/static/icons/`; a Go test checks each exists with the right size
(`image.DecodeConfig`) so no tool is needed at build time. The favicon stays the inline SVG.

## 3. Routes and screens

Hash routes:

| Route | Screen | Notes |
| --- | --- | --- |
| `#/` | Now (`PhoneNow`) | Rows get **Seen** and **Snooze** (swipe left/right on touch; two buttons in a row menu for keyboard and screen readers); a seen row leaves Needs you until its session's state changes (push.md 4); alert status ("push in 40 s", "pushed 14:04", "snoozed until 14:20"); offline banner (4) |
| `#/sessions`, `?filter=needs_you\|working\|done\|all`, `?by=machine` | Sessions (`PhoneSessions`) | Filter chips and "By machine" grouping as drawn |
| `#/sessions/<id>?alert=<al>` | Session (`PhoneSession`) | **Open in Claude** (`remote_url`, with the wrong-account note), **Copy resume command**, **Snooze 15 min** (alert), state history. The heading (and the Now and Sessions rows) is the folder, then ` · <title or name>` unless that only repeats the folder ("infra", not "infra · infra") |
| `#/accounts?seat=<id>` | Accounts (`PhoneAccounts`) | "Most room right now" line, then one compact row per account (3.0a); `seat` opens, scrolls to and highlights its row |
| `#/machines?machine=<id>` | Machines | Under More on phones; stays in the desktop sidebar |
| `#/more` | More (new) | Links: Alerts, Machines, Peers, Settings; owner only: Devices, Add phone; Analytics (`#/more/analytics`) |
| `#/more/alerts` | Alerts and rules (`PhoneAlerts`) | Rule list (5 below), quiet hours, recent pushes from `GET /v1/alerts` with outcome (open, seen, snoozed, cancelled + the "cancelled means you answered first" note) |
| `#/more/settings` | Settings | Hub address, version, live status; notifications (6); private pushes, tap behaviour, own quiet hours (device prefs); ntfy topic (push.md 7); install status; app version + "Check for update"; **Unpair this phone** |
| `#/more/peers` | Settings → Peers | The account matrix, **Pause all peers**, the audit list (3.1); under More on phones and a Peers entry in the desktop sidebar; linked from Settings |
| `#/more/keys` | Settings → Keys (3.2) | The next step to take, the hub key, the owner key (pending ones confirmed by an owner session), each device's certificate state, the revocation version; a Keys entry in the desktop sidebar and a pill in the More tabs |
| `#/more/analytics` | Analytics | `GET /v1/analytics/{report}` (analytics.md): range chips (7, 30, 90 days), a seat filter, totals, a daily cost bar chart, by model, by project, a weekday by hour heatmap, limit hits; read scope, so also for a read-only phone |
| `#/more/devices` | Devices (owner; `DeskAlerts` "Paired phones") | `GET /v1/devices`, rename, revoke, test push per target, Add phone / Add machine (QR dialog, pairing.md 2.4) |
| `#/pair?s=…&c=…` | Pairing (`PhonePair`) | pairing.md 5: an editable device name (prefilled), and leaving before **Open the app** starts the app rather than bouncing back here (5.1) |
| `#/owner?s=…` | Owner sign-in | Posts `/v1/session/owner`, then `#/more/settings`; a refused link says why, and an expired one the lifetime it was made with (`ttl_s`, pairing.md 4) |
| `#/alerts/<id>` | — | Loads `GET /v1/alerts/<id>` and replaces the route with the alert's deep link (push.md 5.8) |
| `#/login` | Sign in | Unchanged token login; adds "Pair with a code instead" |

Navigation: phone bottom tabs **Now, Sessions, Accounts, More**; desktop
sidebar Now, Sessions, Accounts, Machines, Alerts, Settings, Peers, Keys (+ Devices for the owner). `nav.js` gets a `phone`/`desk`
flag per item; CSS decides as today.

Start-up: `GET /v1/me`. 401 → pair screen when running installed (`display-mode:
standalone`) or on a phone, else the login screen. When the live stream (`/v1/stream`) drops,
the board asks `GET /v1/me` at once: the hub ends a revoked device's streams at revoke, so a
revoked board signs out (pair or login screen) at once instead of on its next poll; any other
answer (a network error while the hub restarts) changes nothing. A `read`-only board session shows every
screen; controls that need `push` or `owner` are hidden or show "Sign in as owner on the hub
(`wy hub owner-link`)".

Rules on the phone (`PhoneAlerts`): one row per base trigger with its global delay as text
("· 30 s") and a switch. For a push-scoped device the switch edits `prefs.mute`
(`PATCH /v1/devices/self`); for an owner session it edits the global rule (`PUT
/v1/rules/{id}`), and the delay is editable. Overrides are listed (owner: add/remove with a
seat/project picker and on/off + delay). The owner changes an override in place: "Change"
turns its row into For (account), Then (notify / do not notify) and Wait, saved with `PUT
/v1/rules/{id}`; its trigger stays. A choice that matches another rule of the trigger is
caught before saving, and a `409 duplicate_rule` from the hub names that rule (push.md 3.7).

### 3.0 The order of the accounts

One order, kept by the hub and the same on every device: `seat.sort_order`, shown as `order` on
`Seat` and set only by the owner with `PUT /v1/seats/order` (api.yaml `putSeatOrder`; a seat
nobody has ordered sorts after the ordered ones, then by first sight). `GET /v1/overview`,
`GET /v1/accounts` and the stream list seats by it, so every list of accounts on the board
(the sidebar, Accounts, the Now cards and rings, Sessions grouped by account, the account
filters and the Analytics account picker) reads `store.overview.seats` and follows it.

- **Reorder** is on Accounts, for the owner only (a plain board or phone sign-in sees no grip).
  Each card has a grip: drag it (pointer events: mouse, pen and touch; `touch-action: none` on
  the grip only, so the page still scrolls), with the dragged card faded and a bar in the accent
  colour where it will land; the page scrolls near its top and bottom edge. Escape cancels.
  Keyboard: focus the grip and press the arrow keys, or press Alt+arrow anywhere in a card; focus
  stays on the grip and a polite status line says "work moved to 2 of 4".
- The drop logic is `components/order.js` (pure: `applyOrder`, `moveTo`, `moveBy`, `dropTarget`,
  `dropOrder`), tested in `order.test.cjs`. The list sent is the shown (not hidden) accounts in
  their new order; the hub puts hidden ones after them.
- **Optimistic**: `store.reorderSeats` puts the order in `store.overview.seats` at once and sends
  the PUT. A refusal (or no network) puts the old order back and shows the reason on the page and
  as a toast.
- **Live**: the hub publishes a `seats` stream event `{order: [every seat id]}`. A board that did
  not make the change applies it in place, with no reload (an event is ignored while this board's
  own save is in flight; the answer settles the order). The terminal board and the desktop app
  refetch their accounts on it; the phone app (no stream) picks it up on its next 15 s poll.

### 3.0a The Accounts list

`views/accounts.js`, with its pure logic in `components/accounts-fmt.js` (tested in `accounts-fmt.test.cjs`).
The desktop app's Hub tab shows this same page.

- **One row per account**: grip (owner only; drag and arrow keys as in 3.0), colour dot, name, the
  plan in muted text; one bar for the tighter of the 5 h and 7 d limits (label "5 h" or "7 d", the
  percentage, red from 90%) with the reset time of that window beside it ("resets 15:48",
  "resets Sun 09:00"). A window whose reset has passed counts as 0 and says when it reset ("reset
  at Sat 10:10"), never "resets now". An **idle** window (api.yaml `Window.state` `idle`, or 0 %
  with no `resets_at` from an older hub: claude.ai has not started it, record.md 4.3) is 0 % with
  a muted "not started", never "no data" or "unknown". On a tie the bar shows 5 h.
- **Windows everywhere** (`components/windows.js`: `windowPhase` from `accounts-fmt.js`,
  `shownUse`, `resetWords`; the Now cards, the rings and the expanded rows): idle → "0% · not
  started" (expanded: "not started · starts when you next use this account"; ring: "not
  started"); reset passed → "0% · reset at Sat 10:10" (ring: "reset Sat 10:10"); else "41% ·
  resets 15:48" (expanded: "resets in 2 h 10 min (15:48)").
- **Needs a look**: a line only when something does. Priority: near the limit (runaway pace on 5 h:
  "Near the 5-hour limit: full by 15:48"; or 7 d at 90% or more: "7-day limit nearly used, resets
  Sun 09:00") > **Signed out on \<machine>** (`Seat.signed_out`, api.yaml; a machine where the
  account is also signed in is not listed) > other (claude.ai usage reading stopped). More than one
  shows "+N".
- **Expand in place**: a click on the row, or its chevron button (`aria-expanded`), opens the row
  below it: every limit window (label, %, meter, "resets in 2 h 10 min (15:48)", peak, source, pace
  line), where Claude Code is signed in or out per machine, details (plan, organisation, email,
  extra usage and spend, open sessions, "claude.ai usage read N min ago") and, for the owner, **Edit
  label and colour**. Several rows can be open; the open rows are kept per browser in
  `localStorage` (`wy-accounts-open`, optional). **Expand all / Collapse all** sits at the top.
- Below the list one muted line: "The bar shows the tighter of the 5 h and 7 d limits. Select an
  account for everything else."
- **Most room right now** ("Most room right now: claude-2 (5 h 0% · 7 d 3%)"): among non-hidden
  accounts, the effective use of a window is 0 when its `resets_at` has passed or it is idle, or
  when the account has no reading for that window but has one for another; otherwise its `used_pct` (a reading older
  than 6 hours counts until its reset). An account with no reading at all is skipped. Score =
  max(effective 5 h, effective 7 d); the lowest wins; ties go to the lower 7 d, then the lower 5 h,
  then list order; only when the score is under 80.

### 3.1 Peers (`#/more/peers`)

`views/peers.js`, with its pure arithmetic and sentences in `components/peers-fmt.js`. It reads
`GET /v1/peers/matrix`, `GET /v1/peers/audit` and, when the hub serves it,
`GET /v1/peers/messages` (peers.md 5, 10, 11).

- **Pause all peers** comes first and is prominent: a status line ("Peers are on" / "All peers
  are paused") and one large danger button, `POST /v1/peers/pause`. Resume widens (peers.md 5.4),
  so it is offered only to an owner session.
- **Accounts**: a grid with one row per sender and one column per receiver (the board's visible
  seats, plus any account the matrix names). A cell is none `·`, one way `→` (the row may start a
  thread), back `←` (only the column may), or both `⇄`; the diagonal is `=`, the same-account
  switch of the matrix (`same_account`). The matrix version, its signer and `max_turns` ("no
  turn limit" for 0) are shown with the grid; the turn limit is a number field with a **No limit**
  checkbox (peers.md 5.4). A cell opens an editor under the grid: None, row to column, column to
  row, Both ways. Each choice is a `PUT /v1/peers/matrix` `edit` with `base_version` (a stale one
  is 409: the page reloads the matrix and says so): added pairs go in `allow`, removed pairs in
  `deny`.
- **Allow all / Block all** sit above the grid: Allow all (owner session only: every account
  pair both ways, same account on; hidden for others with "Only the owner can allow pairs.")
  and Block all (any sign-in that may narrow: every pair removed, same account off). Each asks
  with `confirm()` first and is one `PUT /v1/peers/matrix` `edit` (peers.md 5.4); neither is the
  pause switch.
- **Replies between two sessions without you** is a number field (2 to 500) with a Set button and the loop-guard
  explanation (peers.md 6.3) under it. Raising it needs an owner session, lowering it does not.
- **Who may do what** (api.yaml, keys.md 5.2): a choice that adds a pair, and the same-account
  switch turned on, widen, so they are enabled only for an owner session (`/v1/me` `owner`). A
  choice that only removes pairs, and the pause button, need an owner session or a device with
  the `push` scope; a read-only sign-in sees the grid with every control off. A control that is
  off says why, in place ("Widening needs an owner sign-in: run `wy hub owner-link` on the hub").
  The hub refuses anything the page lets through wrongly; the page never signs.
- **Audit**: newest first, one row per entry: what happened ("Matrix changed", "All peers
  paused"), who (or who to whom when the detail names both accounts), the plain metadata of its
  detail (version, widened, pairs, size, status) and the age. **Messages** (when served): who to
  whom by account, kind, size and outcome. Never message text: the page shows only the keys it
  knows and ignores the rest.
- Desktop: the app's Hub view (desktop.md C7) is this page; the app adds nothing of its own.

### 3.2 Keys (`#/more/keys`)

`views/keys.js`, with its sentences and the step logic in `components/keys-fmt.js`. It reads `GET
/v1/keys` (and, for an owner, `GET /v1/devices` to name the machine that proposed a key) and
confirms with `POST /v1/keys/owner/confirm` (keys.md 3.3). It is also what the desktop app's Hub
settings shows (the app embeds the board; it has no keys screen of its own). The page never signs.

- **Next step** comes first and shows only the current step: no owner key (`wy keys init`, in a
  normal terminal, with a copy button); a proposal waiting (the fingerprint in groups of four,
  who proposed it and when, "Is this the fingerprint your terminal printed?", the 24-hour expiry,
  and **Yes, it matches: confirm** for an owner session); owner key active but machines
  hub-signed only (`wy keys certify --all` with a copy button, and which machines are waiting; a phone is never waiting, as its hub signature is enough, keys.md 4.2);
  all certified ("Peers between machines can now message each other"). The API does not say which
  machines have pinned the owner key, so there is no pin step. A replacement proposal while a key
  is active is shown under "A new owner key is waiting to replace this one".
- **Owner key** (one line on what it is for, the active fingerprint, when it was confirmed, and a
  "Replace the owner key" fold-out: `wy keys init --replace`), **Devices** (certified / hub-signed
  only, from `devices`, which owner and write sign-ins get; others see a note) and **Hub key**
  (fingerprint, revocations version).
- A sign-in that is not an owner sees the same page read-only, with "confirming an owner key
  needs an owner session"; it never gets `pending`, so it sees "No owner key is active yet".

## 4. Service worker and offline

`/sw.js`, a classic script (module service workers are not reliable in every browser the
app targets), scope `/`. Logic that can be tested without a browser lives in `/sw-lib.js`,
loaded with `importScripts` and also loaded by `node --test` through `vm`
(`webui/static/sw-lib.test.cjs`).

**Versioning.** `sw.js` contains the literal `__WY_BUILD__`. `webui.Handler()`
(`webui/serve.go`) replaces it, when serving, with the first 12 hex digits of SHA-256 over
all embedded file names and contents (computed once at start), and serves it with
`Content-Type: text/javascript` and `Cache-Control: no-cache`. Any change to any file
therefore changes `sw.js`, which is how browsers notice an update. `/health` gains
`ui` (the same hash).

**Caches.**

| Requests | Strategy | Cache |
| --- | --- | --- |
| App shell: every file in `ASSETS` (a constant list in `sw.js`; a Go test checks it equals the embedded files minus tests and dev files) | Precached on `install`; served cache-first | `wy-shell-<build>` |
| Navigations (`/`, `/index.html`) | Network first, 4 s timeout, then the cached `index.html` | shell |
| `GET` of `/v1/me`, `/v1/overview`, `/v1/sessions`, `/v1/sessions/<id>` (last 20), `/v1/accounts`, `/v1/machines`, `/v1/alerts`, `/v1/rules`, `/v1/devices/self`, `/v1/push/config` | Network first, 6 s timeout; a 200 is stored; on failure the stored copy is returned with header `X-Wy-Cached-At: <ms>`; nothing stored → 503 JSON | `wy-data` |
| `/v1/stream`, every non-GET, `/v1/actions/*`, cross-origin | Not intercepted (`respondWith` is not called) | — |

- `api.js` sees `X-Wy-Cached-At` and sets `store.offline = {since}`; the shell shows
  "Offline · showing 14:02" and disables Seen/Snooze/test buttons. The stream's existing
  backoff keeps retrying; on the first success the banner goes and everything refetches.
- A 401 from any API call makes the page post `{type: "clear-data"}` to the worker, which
  deletes `wy-data`; unpair and logout do the same. Cached data is the only copy of hub data
  on the phone, and it holds no token.
- `activate` deletes caches whose names are not the current shell or `wy-data`, then
  `clients.claim()`, so the first visit is already offline-capable.

**Update flow.**

1. The browser fetches `sw.js` on navigation (the worker is registered with
   `updateViaCache: "none"`, so neither it nor `sw-lib.js` comes from the HTTP cache); the
   page also calls `registration.update()` at start and on `visibilitychange` → visible when
   the last check is over 15 minutes old (an installed iOS app is often resumed, not reloaded).
2. A new worker installs (precaches the new shell) and waits; it does **not** call
   `skipWaiting` on its own, so a running page never mixes old and new files.
3. The page sees `registration.waiting` and shows "Update ready · Reload". Tapping it
   posts `{type: "skip-waiting"}`; on `controllerchange` the page reloads once (every change
   of worker after the page had one, including the first update of a page that was opened
   before any worker existed).
4. The page also applies a waiting worker without a tap when that costs nothing
   (`components/update-policy.js`): within 15 s of its own start, or while it is in the
   background (`visibilitychange` → hidden); never while a field has focus, while a dialog or
   modal is open (`dialog[open]`, `[aria-modal="true"]`), or while a form has unsaved edits
   (`components/pending.js`: an `input`/`change` inside a form marks it, `submit`, `reset` or
   the form leaving the page clears it; a field with `data-nodirty` or outside a form does
   not count). The same guard holds back the reload on a `controllerchange` the page did not
   ask for (another tab applied the update): it reloads once nothing is pending, checked
   every second. Pressing Reload in the banner or in Settings reloads at once. While an
   update waits and something is pending the banner stays and the page re-checks every 2 s.
   The page never reloads twice within a minute (`sessionStorage`), so a build that cannot take over causes no reload
   loop. An installed app that is never closed therefore moves to the new build the next
   time it is backgrounded.
5. If `/health`'s `ui` differs from the running build and no worker is waiting (a browser that
   checked too early), the banner offers "Reload" too. That Reload first asks the browser to
   update the worker and waits for it to install (at most 10 s) before taking over; a plain
   reload would still be answered from the old worker's copy of the files.

## 5. Notifications in the service worker

`push` event: parse the payload (push.md 6.5); if it does not parse, show "Watchyard ·
something needs you" (iOS revokes subscriptions that receive pushes without a notification,
and Chrome shows a generic one). Then:

```js
registration.showNotification(p.title, {
  body: p.body, tag: p.tag, renotify: true, timestamp: Date.parse(p.ts),
  icon: 'icons/icon-192.png', badge: 'icons/badge-96.png',
  data: p,                                         // the whole payload, for clicks
  actions: p.actions ? [{action: 'snooze', title: 'Snooze 15 min'}, {action: 'seen', title: 'Seen'}] : [],
  requireInteraction: false,
});
if (navigator.setAppBadge) navigator.setAppBadge(p.needs_you || 0);   // 0 clears
```

Open windows get `postMessage({type: "push", payload})` so the Now screen refreshes at once.

`notificationclick`:

- `snooze` / `seen`: `fetch('/v1/actions/' + token, {method: 'POST', headers: {'Content-Type':
  'application/json'}, body: '{}'})`; close the notification either way (no retry: if the hub
  is unreachable the alert simply is not snoozed, and the Now screen offers it again).
- Body tap: choose the URL with `chooseTarget(payload, reachable)` from `sw-lib.js`:
  `tap = claude` and a `remote_url` → the Remote Control link; `tap = auto` and a
  `remote_url` → `GET /health` with a 1.5 s timeout, the link if the hub does not answer,
  else the deep link; otherwise the deep link. Then focus an existing app window and
  `navigate` it (or post the route), else `clients.openWindow`.
- **Uncertain:** whether `clients.openWindow` with a `claude.ai` link from an installed iOS app
  hands off to the Claude app (universal link) or opens Safari; the manual checklist covers it.

`pushsubscriptionchange` (Chrome, Firefox; Safari does not fire it): subscribe again with the
same VAPID key and `POST /v1/push/targets`.

## 6. Turning notifications on

Settings → Notifications shows one of: "Not supported here", "Install first" (iOS, not
standalone), "Off" with **Turn on**, "Blocked in system settings" (permission `denied`), or
"On · \<n> targets" with **Send test push**.

**Turn on** (always inside a tap handler, required by iOS):

1. `Notification.requestPermission()`; anything but `granted` stops with the matching state.
2. `GET /v1/push/config` → `vapid_public_key`.
3. `registration.pushManager.subscribe({userVisibleOnly: true, applicationServerKey})`. If an
   existing subscription has a different key (the hub moved and made new keys), unsubscribe
   it first.
4. `POST /v1/push/targets {kind: "webpush", endpoint, keys: {p256dh, auth}, name}` and
   `PATCH /v1/devices/self {prefs: {tz}}`.
5. Offer **Send test push** (`POST /v1/push/targets/{id}/test`), showing the hub's result
   (sent, or the push service's error).

On every start (installed, permission granted): `pushManager.getSubscription()`; if missing,
or `GET /v1/devices/self` shows no live webpush target with that endpoint (the hub marked it
gone, push.md 5.6), repeat steps 2–4 silently.

## 7. Installing

| Platform | Install | Push | Notes |
| --- | --- | --- | --- |
| iPhone / iPad, iOS 16.4+ | Safari → Share → **Add to Home Screen** (iOS 16.4+ also from other browsers' share sheets) | Only in the installed app; permission only from a tap inside it | No notification action buttons (Snooze/Seen live in the app); badge supported; Focus modes apply. Apple withdrew the change that would have removed web push support in the EU before iOS 17.4 shipped (March 2024), so EU users should work (**uncertain**, to be confirmed on a device). iOS 16.3 and older: the app works as a board, Settings says "needs iOS 16.4 for notifications", ntfy is offered instead |
| Android, Chrome | The page keeps `beforeinstallprompt` and shows **Install app** in the pair and Settings screens; also Chrome menu → Install app | In the browser or installed (WebAPK); shares Chrome's cookies | Two action buttons shown; links into the hub's scope open in the installed app |
| Android, Firefox / Samsung Internet | Browser menu | Works in principle | Not tested |
| Desktop Chrome / Edge / Safari 17+ | Install icon in the address bar / File → Add to Dock | Yes (`browser` pairing code) | Optional; the desktop app is the main desktop path |

The pair page and Settings show the right instructions for the platform they detect
(`navigator.userAgentData`, else the user agent string; `matchMedia('(display-mode:
standalone)')` and `navigator.standalone` for "already installed").

## 8. Hub side

- `webui.Handler(fs.FS) http.Handler` (new, `webui/serve.go`) replaces the bare
  `http.FileServer` in `internal/hub/api/server.go`: content types for `.webmanifest`,
  `sw.js` templating, `Cache-Control: no-cache` everywhere (files are not fingerprinted) and
  the existing `nosniff` / `no-referrer` headers.
- Every response carries `webui.CSP()`: `default-src 'self'; script-src 'self'; connect-src
  'self'; worker-src 'self'; manifest-src 'self'; img-src 'self' data:; style-src 'self';
  font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`. There is no
  `'unsafe-inline'`: no inline script, no `<style>` block and no `style=""` attribute (in
  markup or through `setAttribute`). Dynamic values (a seat colour as `--seat`, a meter
  width, a swipe offset) are set with `el.style.setProperty` / `el.style.transform`, which
  the CSSOM allows; `h()` takes `style` only as an object. `TestNoInlineStyleAndStrictStyleSrc`
  fails on any new inline style. The desktop app needs no change: its Hub view loads this
  page under this policy (the app injects no CSS or markup into it), and the app's own shell
  already runs with `style-src 'self'` (desktop.md).
- Fonts are served from `webui/static/fonts` (OFL, `NOTICE`): IBM Plex Sans and Mono, latin
  subsets, plus `Watchyard Symbols`, three subsets (3 KB in all) of Noto Sans Math, Symbols and
  Symbols 2 for the glyphs Plex lacks: ← → ⇄ ⋯ ⓘ ■ ▸ ▾ ○ ◌ ● ⠿ ✓. `--sans` and `--mono` list it
  after Plex; each `@font-face` has the exact `unicode-range`.
  `TestNonLatinGlyphsAreInAFontRange` fails when the code writes a character above U+00FF that
  no range covers.

## 9. Tests

- `go test ./webui/...`: every file the app references is embedded (the test covers `manifest.webmanifest`, `sw.js`, `sw-lib.js`, icons); `ASSETS` equals the embedded
  file list; icon sizes; `sw.js` is served with the build hash and no-cache; the devserver's
  new fixtures validate against `api.yaml`.
- `node --test webui/static/*.test.cjs` (Node 20+, no packages): `chooseTarget`, payload
  parsing fallbacks, the data-cache URL matcher; the Peers cells, edits (only an added pair
  widens), "why not" sentences and audit text (`peers-fmt.test.cjs`). The devserver serves the
  matrix, pause and audit endpoints with the hub's who-may-do-what (`devserver/peers_test.go`).
- Headless Chrome (extends `webui/smoke`, `WY_WEBUI_BROWSER=1`, puppeteer-core), against the
  devserver at `http://127.0.0.1` (a secure context):
  - manifest: CDP `Page.getAppManifest` has no errors; `Page.getInstallabilityErrors` is
    empty;
  - the worker registers and controls the page after one reload; `caches.keys()` has the
    shell cache;
  - offline: `page.setOfflineMode(true)` then reload → Now renders from `wy-data` with the
    offline banner; back online → banner gone;
  - update: the devserver switches to another build hash → "Update ready" → click →
    reloaded page runs the new build;
  - pairing: open `/#/pair?s=<devserver secret>` with a phone viewport and an Android UA →
    claim → cookie set → Now renders; with an iPhone UA → no claim, instructions with the
    digits;
  - notifications: `browserContext.overridePermissions(origin, ['notifications'])`. Real
    `pushManager.subscribe` needs Google's push service and usually fails in headless or
    sandboxed CI (`AbortError`), so the smoke test stubs `PushManager.prototype.subscribe`
    (`evaluateOnNewDocument`) to return a subscription whose keys the test generated, and
    checks the app posts it to `/v1/push/targets`. A real subscribe is attempted once and
    only logged;
  - push display: CDP `ServiceWorker.deliverPushMessage` with a payload from push.md 6.5,
    then `registration.getNotifications()` shows the title, tag and actions, and the badge
    call was made (spied).
  - Not testable headless: tapping a notification, real delivery through a push service,
    iOS. Those are in the manual checklist.
