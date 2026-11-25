// Watchyard service worker (pwa.md 4 and 5). A classic script, scope "/".
//
// - App shell: precached on install, served cache-first. The hub replaces __WY_BUILD__
//   with a hash of every embedded file, so any change to the app changes this file and the
//   browser installs a new worker.
// - Last known data: GETs of the read API are network first and kept in `wy-data`; when
//   the hub cannot be reached the stored copy is returned with `X-Wy-Cached-At`.
// - A new worker waits until the page asks it to take over (no skipWaiting on its own), so
//   a running page never mixes old and new files.
// - Push display, notification buttons and taps, and re-subscribing.

importScripts('sw-lib.js');

const BUILD = '__WY_BUILD__';
const SHELL = WyLib.shellCacheName(BUILD);
const DATA = WyLib.DATA_CACHE;

// Every embedded file except this one and the tests (a Go test keeps this list in sync).
const ASSETS = [
  'api.js',
  'app.js',
  'components/accounts-fmt.js',
  'components/alerts.js',
  'components/alerts-fmt.js',
  'components/analytics-fmt.js',
  'components/cards.js',
  'components/dom.js',
  'components/live.js',
  'components/machines-fmt.js',
  'components/nav.js',
  'components/switches-fmt.js',
  'components/pair-error.js',
  'components/notif-cats.js',
  'components/notif-note.js',
  'components/order.js',
  'components/peers-fmt.js',
  'components/pending.js',
  'components/keep.js',
  'components/keys-fmt.js',
  'components/qr.js',
  'components/quiet.js',
  'components/pick-mode.js',
  'components/rel-chart.js',
  'components/relation-fmt.js',
  'components/rings.js',
  'components/search-fmt.js',
  'components/seat.js',
  'components/state.js',
  'components/swipe.js',
  'components/targets.js',
  'components/ui.js',
  'components/update-policy.js',
  'components/windows.js',
  'fonts/OFL.txt',
  'fonts/ibm-plex-mono-latin-400-normal.woff2',
  'fonts/ibm-plex-mono-latin-500-normal.woff2',
  'fonts/ibm-plex-mono-latin-600-normal.woff2',
  'fonts/ibm-plex-sans-latin-400-normal.woff2',
  'fonts/ibm-plex-sans-latin-500-normal.woff2',
  'fonts/ibm-plex-sans-latin-600-normal.woff2',
  'fonts/ibm-plex-sans-latin-700-normal.woff2',
  'fonts/noto-sans-math-symbols.woff2',
  'fonts/noto-sans-symbols-2-braille.woff2',
  'fonts/noto-sans-math-approx.woff2',
  'fonts/noto-sans-symbols-info.woff2',
  'format.js',
  'icons/apple-touch-icon-180.png',
  'icons/badge-96.png',
  'icons/favicon-32.png',
  'icons/favicon.ico',
  'icons/icon-192.png',
  'icons/icon-512.png',
  'icons/icon.svg',
  'icons/maskable-512.png',
  'index.html',
  'manifest.webmanifest',
  'platform.js',
  'push.js',
  'pwa.js',
  'route.js',
  'store.js',
  'styles/base.css',
  'styles/components.css',
  'styles/fonts.css',
  'styles/layout.css',
  'styles/phone.css',
  'sw-lib.js',
  'views/accounts.js',
  'views/alert-redirect.js',
  'views/analytics.js',
  'views/alerts.js',
  'views/devices.js',
  'views/login.js',
  'views/machines.js',
  'views/more.js',
  'views/not-found.js',
  'views/now.js',
  'views/owner.js',
  'views/pair.js',
  'views/peers.js',
  'views/keys.js',
  'views/session.js',
  'views/switches.js',
  'views/sessions.js',
  'views/settings.js',
];

const NAV_TIMEOUT = 4000;
const DATA_TIMEOUT = 6000;

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(SHELL).then((c) => c.addAll(ASSETS.map((p) => new Request(p, { cache: 'reload' })))));
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    for (const name of await caches.keys()) {
      if (!WyLib.isCurrentCache(name, BUILD)) await caches.delete(name);
    }
    await self.clients.claim();
  })());
});

self.addEventListener('message', (event) => {
  const m = event.data || {};
  if (m.type === 'skip-waiting') self.skipWaiting();
  else if (m.type === 'clear-data') event.waitUntil(caches.delete(DATA));
  else if (m.type === 'build') event.source && event.source.postMessage({ type: 'build', build: BUILD });
});

// ---- fetch ---------------------------------------------------------------------------

self.addEventListener('fetch', (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== 'GET' || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/v1/stream') || url.pathname.startsWith('/v1/actions/')) return;

  if (req.mode === 'navigate' || WyLib.isNavigation(req.url)) {
    if (WyLib.isNavigation(req.url)) event.respondWith(navigation(req));
    return;
  }
  if (WyLib.isDataRequest(req.url, req.method)) { event.respondWith(data(req, event)); return; }
  if (url.pathname.startsWith('/v1/') || url.pathname === '/health') return;
  event.respondWith(shell(req));
});

const timeout = (ms) => new Promise((resolve) => setTimeout(() => resolve('timeout'), ms));

async function navigation(req) {
  const live = fetch(req).catch(() => null);
  const first = await Promise.race([live, timeout(NAV_TIMEOUT)]);
  if (first && first !== 'timeout' && first.ok) return first;
  const cached = await caches.match('index.html', { cacheName: SHELL });
  if (cached) return cached;
  const late = first === 'timeout' ? await live : first;
  return late || new Response('Offline', { status: 503, headers: { 'Content-Type': 'text/plain' } });
}

async function shell(req) {
  const cache = await caches.open(SHELL);
  const hit = await cache.match(req, { ignoreSearch: true });
  if (hit) return hit;
  return fetch(req);
}

// Keeps a copy of a good response with the time it was stored.
async function store(req, res) {
  const headers = new Headers(res.headers);
  headers.set('X-Wy-Cached-At', String(Date.now()));
  const copy = new Response(await res.clone().blob(), { status: res.status, statusText: res.statusText, headers });
  const cache = await caches.open(DATA);
  await cache.put(req.url, copy);
  const evict = WyLib.sessionsToEvict((await cache.keys()).map((r) => r.url));
  await Promise.all(evict.map((u) => cache.delete(u)));
}

async function data(req, event) {
  const live = fetch(req).then((res) => {
    if (res.ok) event.waitUntil(store(req, res.clone()).catch(() => {}));
    return res;
  }, () => null);
  const first = await Promise.race([live, timeout(DATA_TIMEOUT)]);
  if (first && first !== 'timeout') return first;
  const cached = await caches.match(req.url, { cacheName: DATA });
  if (cached) return cached;
  const late = first === 'timeout' ? await live : null;
  if (late) return late;
  return new Response(JSON.stringify({ error: 'Cannot reach the hub and nothing is stored yet', code: 'offline' }),
    { status: 503, headers: { 'Content-Type': 'application/json' } });
}

// ---- push ----------------------------------------------------------------------------

self.addEventListener('push', (event) => {
  event.waitUntil((async () => {
    let text = '';
    try { text = event.data ? event.data.text() : ''; } catch (_) { /* unreadable */ }
    const p = WyLib.parsePayload(text);
    await self.registration.showNotification(p.title, WyLib.notificationOptions(p, ''));
    if (self.navigator && self.navigator.setAppBadge && Number.isInteger(p.needs_you)) {
      try { await self.navigator.setAppBadge(WyLib.badgeCount(p)); } catch (_) { /* badges are optional; 0 clears */ }
    }
    for (const c of await self.clients.matchAll({ type: 'window', includeUncontrolled: true })) {
      c.postMessage({ type: 'push', payload: p });
    }
  })());
});

async function reachable() {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), 1500);
  try {
    const res = await fetch('/health', { signal: ctl.signal, cache: 'no-store' });
    return res.ok;
  } catch (_) {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

async function openRoute(url) {
  const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
  const mine = wins.find((c) => new URL(c.url).origin === self.location.origin);
  if (mine) {
    await mine.focus();
    mine.postMessage({ type: 'route', hash: WyLib.hashOf(url) });
    return;
  }
  await self.clients.openWindow(url);
}

self.addEventListener('notificationclick', (event) => {
  const p = (event.notification.data && typeof event.notification.data === 'object') ? event.notification.data : WyLib.parsePayload('');
  event.notification.close();
  event.waitUntil((async () => {
    if (event.action === 'snooze' || event.action === 'seen') {
      const token = WyLib.actionToken(p, event.action);
      if (!token) return;
      try {
        await fetch(`/v1/actions/${token}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      } catch (_) { /* hub unreachable: the alert is simply not snoozed */ }
      return;
    }
    const target = WyLib.chooseTarget(p, WyLib.needsProbe(p) ? await reachable() : true);
    if (target.kind === 'claude') await self.clients.openWindow(target.url);
    else await openRoute(target.url);
  })());
});

// Chrome and Firefox end a subscription now and then; subscribe again with the same key and
// tell the hub (Safari never fires this: the app re-checks at start instead).
self.addEventListener('pushsubscriptionchange', (event) => {
  event.waitUntil((async () => {
    try {
      const key = event.oldSubscription && event.oldSubscription.options && event.oldSubscription.options.applicationServerKey;
      const sub = event.newSubscription
        || (key ? await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key }) : null);
      if (!sub) return;
      await fetch('/v1/push/targets', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(WyLib.targetBody(sub)),
      });
    } catch (_) { /* the app repeats this at its next start */ }
  })());
});
