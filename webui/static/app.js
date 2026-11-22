// Watchyard web app: hash router, shell and render loop. The same single-page app is the
// desktop board (sidebar) and the installable phone app (bottom tabs); CSS chooses.
//
// Routes: see route.js. Start-up asks GET /v1/me: a 401 opens the pair screen (installed
// app or phone) or the token login (a computer).

import { store, subscribe, start, stop, setAuthLostHandler, refreshAll, canPush, inApp, forgetOwner } from './store.js';
import { logout, getJSON } from './api.js';
import { renderNav } from './components/nav.js';
import { h } from './components/dom.js';
import { keepLive } from './components/keep.js';
import { pendingText } from './components/alerts.js';
import { parseRoute, PUBLIC_VIEWS } from './route.js';
import { isMobile } from './platform.js';
import { pwa, initPwa, subscribe as onPwa, updateReady, applyUpdate, isStandalone } from './pwa.js';
import { resubscribeIfNeeded } from './push.js';
import * as now from './views/now.js';
import * as sessions from './views/sessions.js';
import * as session from './views/session.js';
import * as accounts from './views/accounts.js';
import * as machines from './views/machines.js';
import * as login from './views/login.js';
import * as more from './views/more.js';
import * as alerts from './views/alerts.js';
import * as settings from './views/settings.js';
import * as devices from './views/devices.js';
import * as peers from './views/peers.js';
import * as keys from './views/keys.js';
import * as analytics from './views/analytics.js';
import * as pair from './views/pair.js';
import * as owner from './views/owner.js';
import * as alertRedirect from './views/alert-redirect.js';
import * as notfound from './views/not-found.js';
import { toast } from './components/ui.js';
import { clock } from './format.js';

const VIEWS = { now, sessions, session, accounts, machines, login, more, alerts, settings, devices, peers, keys, analytics, pair, owner, alertRedirect, notfound };
const navEl = document.getElementById('nav');
const mainEl = document.getElementById('main');
const announceEl = document.getElementById('announce');
const appEl = document.getElementById('app');
const bannersEl = document.getElementById('banners');

let authed = false;
let lastRouteKey = '';
let lastNeeds = null;
let ownerSignedIn = false; // an owner link just worked: land on Now with a toast

export { parseRoute };

function go(hash) {
  if (location.hash === hash) render(); else location.hash = hash;
}

// Where a signed-out visitor lands: the pair screen on a phone or in the installed app, the
// sign-in page on a computer and in the desktop app's Hub view (which shows no CLI there).
const unauthRoute = () => (!inApp() && (isStandalone() || isMobile(navigator.userAgent, navigator.maxTouchPoints, navigator.platform)) ? '#/pair' : '#/login');

function showUnauth() {
  authed = false;
  stop();
  appEl.classList.add('shell-login');
  navEl.replaceChildren();
  const route = parseRoute(location.hash);
  if (PUBLIC_VIEWS.has(route.view)) render(); else go(unauthRoute());
}

let booting = false; // start() emits while loading: do not bounce to the pair screen before it answers

async function boot() {
  booting = true;
  const ok = await start();
  booting = false;
  if (ok) {
    authed = true;
    appEl.classList.remove('shell-login');
    const v = parseRoute(location.hash).view;
    if (v === 'login') go('#/'); else render();
    if (canPush() && !inApp()) resubscribeIfNeeded(); // the desktop app notifies by itself
  } else if (store.unauthorized || !store.error) {
    showUnauth();
  } else {
    // Hub unreachable and nothing stored: show the error where the page would be.
    appEl.classList.add('shell-login');
    mainEl.replaceChildren(errorBox(store.error));
  }
}

function errorBox(msg) {
  return h('div', { class: 'login-wrap' }, h('div', { class: 'card login-card' },
    h('p', {}, msg), h('button', { class: 'btn', type: 'button', onclick: boot }, 'Try again')));
}

let rendering = false, again = false;

async function render() {
  if (rendering) { again = true; return; }
  rendering = true;
  try {
    const route = parseRoute(location.hash);
    renderBanners();
    if (!authed && booting && !PUBLIC_VIEWS.has(route.view)) return;
    if (!authed && !PUBLIC_VIEWS.has(route.view)) {
      // Paired on the pair screen, then went elsewhere before "Open the app": the cookie is
      // set, so start the app on the page asked for rather than bouncing back to #/pair.
      if (pair.paired()) { boot(); return; }
      go(unauthRoute());
      return;
    }
    if (authed && route.view === 'login') { go('#/'); return; }
    // The owner view opens Settings after a sign-in; the board lands on Now instead.
    if (authed && ownerSignedIn && route.view === 'settings') {
      ownerSignedIn = false;
      location.replace('#/');
      toast('You are signed in as the owner.');
      return;
    }
    const view = VIEWS[route.view];
    const key = routeKey(route, lastRouteKey);
    const changed = key !== lastRouteKey;
    // The sign-in page keeps no state of its own: rebuilding it on a data or PWA update would
    // drop a half-typed token or owner link, and the node the reader is about to press.
    if (!changed && route.view === 'login' && mainEl.querySelector('.login-card')) return;
    const onLoggedIn = route.view === 'owner' ? () => { ownerSignedIn = true; return boot(); } : boot;
    const ctx = { store, route, now: Date.now(), onLoggedIn, onLogout: doLogout, rerender: render };

    if (changed) lastRouteKey = key;
    if (changed && view.load) view.load(ctx);

    if (PUBLIC_VIEWS.has(route.view)) {
      appEl.classList.add('shell-login');
    } else {
      appEl.classList.remove('shell-login');
      if (!store.overview) return;
      renderNav(navEl, { page: route.page, store, onLogout: doLogout });
    }

    const focusKey = document.activeElement && document.activeElement.dataset && document.activeElement.dataset.key;
    // A redraw must not close a disclosure the reader opened (keyed <details>).
    const opened = changed ? [] : [...mainEl.querySelectorAll('details[data-key][open]')].map((d) => d.dataset.key);
    const { title, node } = view.render(ctx);
    for (const k of opened) { const d = node.querySelector(`details[data-key="${CSS.escape(k)}"]`); if (d) d.open = true; }
    // A kept element (the sessions search box) stays in place through a redraw of its page.
    if (changed || !keepLive(mainEl.firstElementChild, node)) mainEl.replaceChildren(node);
    document.title = title;
    appEl.dataset.page = route.page;
    if (changed) { window.scrollTo(0, 0); if (!PUBLIC_VIEWS.has(route.view)) mainEl.focus({ preventScroll: true }); }
    else if (focusKey) {
      const el = document.querySelector(`[data-key="${CSS.escape(focusKey)}"]`);
      if (el) el.focus({ preventScroll: true });
    }
    if (view.after) view.after(ctx, changed);
    announceNeeds();
  } finally {
    rendering = false;
    if (again) { again = false; render(); }
  }
}

// What makes a page load again. A one-time link (#/owner?s=…, #/pair?s=…&c=…) loads again
// for every new secret or code, even in the same tab; once the page drops the secret
// from the address bar the key stays, so the result stays on screen.
function routeKey(route, last = '') {
  const base = `${route.view}/${route.id || ''}`;
  if (route.view !== 'owner' && route.view !== 'pair') return base;
  const s = route.query.get('s') || '', c = route.query.get('c') || '';
  if (!s && !c) return last.startsWith(`${base}?`) ? last : base;
  return `${base}?${s}&${c}`;
}

function announceNeeds() {
  if (!store.overview) return;
  const n = store.overview.counts.needs_you;
  if (lastNeeds !== null && n !== lastNeeds) announceEl.textContent = n ? `${n} sessions need you` : 'Nothing needs you';
  lastNeeds = n;
  if ('setAppBadge' in navigator && authed && canPush() && !inApp()) {
    (n ? navigator.setAppBadge(n) : navigator.clearAppBadge()).catch(() => {});
  }
}

// ---- banners: offline and "update ready" --------------------------------------------

function renderBanners() {
  const kids = [];
  if (store.offline) {
    const hm = clock(new Date(store.offline.since).toISOString());
    kids.push(h('div', { class: 'banner banner-offline', role: 'status' }, `Offline · showing ${hm}`));
  }
  if (updateReady()) {
    kids.push(h('div', { class: 'banner banner-update', role: 'status' }, h('span', {}, 'Update ready'),
      h('button', { type: 'button', class: 'btn btn-primary btn-sm', 'data-key': 'update-reload', onclick: applyUpdate }, 'Reload')));
  }
  bannersEl.replaceChildren(...kids);
}

// Sign out revokes this browser's device on the hub (pairing.md 6). A browser paired with a
// code needs a new code to come back, so it asks first; an owner or board session just ends.
async function doLogout() {
  const d = store.me && store.me.device;
  if (d && String(d.created_by || '').startsWith('pair:') && !store.me.owner
    && !confirm('Sign out? This browser is removed from the hub. To come back, pair it again with a code or open an owner link.')) return;
  try { await logout(); } catch (_) { /* cookie may already be gone */ }
  forgetOwner();
  showUnauth();
}

setAuthLostHandler(showUnauth);

// Redraw on data changes (coalesced to one frame) and on route changes.
let frame = 0;
const schedule = () => { if (!frame) frame = requestAnimationFrame(() => { frame = 0; render(); }); };
subscribe(schedule);
onPwa(schedule);
window.addEventListener('hashchange', render);
// The skip links move focus; following their href would change the hash route.
for (const a of document.querySelectorAll('a.skip')) {
  a.addEventListener('click', (e) => {
    e.preventDefault();
    const target = document.getElementById(a.dataset.target);
    const el = target && (a.dataset.target === 'nav' ? target.querySelector('a') : target);
    if (el) el.focus();
  });
}
window.addEventListener('wy-rerender', render);
// "5 min ago" labels move even when no data arrives.
setInterval(() => { if (authed) render(); }, 30e3);
// A pending alert's "notifies in 40 s" counts down between redraws.
setInterval(() => {
  for (const el of document.querySelectorAll('[data-due]')) el.textContent = pendingText(el.dataset.due);
}, 5e3);

// The service worker forwards pushes and routes taps to an open window.
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.addEventListener('message', (e) => {
    const m = e.data || {};
    if (m.type === 'push' && authed) refreshAll();
    if (m.type === 'route' && typeof m.hash === 'string' && m.hash.startsWith('#/')) location.hash = m.hash;
  });
}

initPwa();
boot();

// Exposed for the smoke test and debugging.
window.__wy = { store, getJSON, pwa };
