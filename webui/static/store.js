// The app's data: who is signed in, one overview, the session list (loaded on first use) and
// the open session's detail. The stream patches it in place; polling refetches it.

import {
  AuthError, getMe, getOverview, getSessions, getSession, openStream, setCachedHandler,
  snoozeAlert, seenAlert, putJSON, authReason,
} from './api.js';
import { applyOrder } from './components/order.js';
import { meInApp, markerInApp } from './components/notif-note.js';

const listeners = new Set();

export const store = {
  me: null,            // GET /v1/me: {device, owner, hub}
  overview: null,      // GET /v1/overview
  sessions: null,      // GET /v1/sessions, null until a view asks for it
  detail: null,        // GET /v1/sessions/{id} for the open session page
  detailId: null,
  live: 'connecting',  // connecting | live | polling | offline
  liveAt: 0,           // when `live` last changed
  everLive: false,     // the stream has connected at least once
  offline: null,       // {since: ms} while the service worker answers from its copy
  updatedAt: 0,
  error: null,
  unauthorized: false, // the last start() ended in a 401
};

export const subscribe = (fn) => { listeners.add(fn); return () => listeners.delete(fn); };
const emit = () => listeners.forEach((fn) => fn());

let onAuthLost = () => {};
export const setAuthLostHandler = (fn) => { onAuthLost = fn; };

// What the signed-in caller may do (scopes from /v1/me). A plain board login has `read`
// only: the push and owner controls stay hidden for it.
export const isOwner = () => !!(store.me && store.me.owner);
export const canPush = () => !!(store.me && (store.me.owner || (store.me.device.scopes || []).includes('push')));

// The board is the desktop app's Hub view: /v1/me says this session was opened from a desktop
// device (`via`, api.yaml Me), or the caller is the desktop token itself. The app shows the
// hub's alerts as notifications on its own (desktop.md C9) and cannot install a PWA, so Web
// Push, install, pairing "this browser" and sign-out are not offered there.
// Signed out there is no /v1/me: the app's marker (a "WatchyardDesktop/" user agent token, or
// ?in=desktop in the start URL, kept for this tab) says it too, so the sign-in page never shows
// the browser's CLI wording inside the app.
export const inApp = () => meInApp(store.me) || appMarker();

let marker = null;
export function appMarker() {
  if (marker !== null) return marker;
  let saved = false;
  try { saved = sessionStorage.getItem('wy.in') === 'desktop'; } catch (_) { /* no storage */ }
  marker = saved || markerInApp(navigator.userAgent, location.search);
  if (marker && !saved) { try { sessionStorage.setItem('wy.in', 'desktop'); } catch (_) { /* no storage */ } }
  return marker;
}

// The owner session's end, remembered in this browser (a convenience only): after it passes,
// the cookie is gone and the hub cannot say why, so the sign-in page still says "Your owner
// access ended."
const OWNER_UNTIL = 'wy.ownerUntil';
function rememberOwner(m) {
  try {
    if (m.owner && m.device && m.device.expires_at) localStorage.setItem(OWNER_UNTIL, m.device.expires_at);
    else localStorage.removeItem(OWNER_UNTIL);
  } catch (_) { /* no storage */ }
}
export function forgetOwner() {
  try { localStorage.removeItem(OWNER_UNTIL); } catch (_) { /* no storage */ }
}

// Why this browser is signed out, for the sign-in page (api.yaml Unauthorized reason): the
// last 401's reason, else "owner_expired" when a remembered owner session has ended.
export function lostReason(now = Date.now()) {
  const r = authReason();
  if (r) return r;
  let until = '';
  try { until = localStorage.getItem(OWNER_UNTIL) || ''; } catch (_) { /* no storage */ }
  return until && Date.parse(until) <= now ? 'owner_expired' : '';
}

async function guarded(fn) {
  try {
    const r = await fn();
    store.error = null;
    store.errorStatus = 0;
    return r;
  } catch (e) {
    if (e instanceof AuthError) { store.unauthorized = true; stop(); onAuthLost(); return undefined; }
    store.error = e.message;
    store.errorStatus = e.status || 0;
    // The live badge belongs to the stream alone (openStream's onStatus): a read that failed
    // (a 404 for a deleted session, a 403, even a network error) says nothing about the open
    // stream, and nothing would set the badge back to live while it stays open.
    emit();
    return undefined;
  }
}

// The API client reports whether each read came live or from the worker's copy.
setCachedHandler((at) => {
  const was = store.offline;
  if (at) {
    const since = was ? Math.min(was.since, at) : at; // the oldest copy on screen
    if (!was || was.since !== since) { store.offline = { since }; emit(); }
  } else if (was) {
    store.offline = null;
    emit();
    refreshAll(); // the hub is back: replace every stored copy
  }
});

export async function refreshMe() {
  const m = await guarded(getMe);
  if (m) { store.me = m; rememberOwner(m); }
  return !!m;
}

// A quiet GET /v1/me: only a 401 matters (sign out, as guarded does); a network error or a
// 5xx while the hub restarts says nothing new and leaves the page as it is.
export async function checkSignIn() {
  try {
    const m = await getMe();
    if (m) { store.me = m; rememberOwner(m); }
  } catch (e) {
    if (e instanceof AuthError && !store.unauthorized) { store.unauthorized = true; stop(); onAuthLost(); }
  }
}

export async function refreshOverview() {
  const o = await guarded(getOverview);
  if (o) { store.overview = o; store.updatedAt = Date.now(); emit(); }
  return !!o;
}

export async function ensureSessions() {
  if (store.sessions) return;
  await refreshSessions();
}

async function refreshSessions() {
  const r = await guarded(getSessions);
  if (r) { store.sessions = r.sessions; store.updatedAt = Date.now(); emit(); }
}

export async function openDetail(id) {
  if (store.detailId !== id) { store.detail = null; store.detailId = id; }
  const d = await guarded(() => getSession(id));
  if (d && store.detailId === id) { store.detail = d; emit(); }
  else if (!d && store.detailId === id && store.error) { emit(); }
}

export function closeDetail() { store.detail = null; store.detailId = null; }

export async function refreshAll() {
  await refreshOverview();
  if (store.sessions) await refreshSessions();
  if (store.detailId) await openDetail(store.detailId);
}

// ---- alerts on sessions ------------------------------------------------------------------

// Statuses where an episode still has an alert row worth showing on its session.
const OPEN = new Set(['pending', 'fired', 'snoozed', 'seen', 'skipped']);

// A seen alert hides its session from Needs you until its state changes (push.md 4). The hub
// leaves such sessions out of the overview; between fetches the board remembers the state_since
// of the episode that was seen, so a stream event for the same episode does not bring it back.
const seenEpisode = new Map(); // session id -> state_since seen

function hiddenSeen(s) { return seenEpisode.get(s.id) === s.state_since; }

function patchSessionAlert(sessionId, brief) {
  const alert = brief && OPEN.has(brief.status) ? brief : null;
  let since = null;
  const set = (s) => { if (s && s.id === sessionId) { s.alert = alert; since = s.state_since; } };
  const o = store.overview;
  if (o) o.needs_you.forEach(set);
  if (store.sessions) store.sessions.forEach(set);
  set(store.detail);
  if (alert && alert.status === 'seen' && since) seenEpisode.set(sessionId, since);
  else seenEpisode.delete(sessionId);
  if (o && seenEpisode.has(sessionId)) {
    o.needs_you = o.needs_you.filter((s) => !hiddenSeen(s));
    o.counts = { ...o.counts, needs_you: o.needs_you.length };
  }
}

// The brief of the alert on a session, only while it can still push or be acknowledged.
export const actionable = (a) => !!a && (a.status === 'pending' || a.status === 'fired' || a.status === 'snoozed');

// Seen and Snooze come back as the whole Alert; keep its brief on the session.
async function act(call, sessionId) {
  const a = await call();
  if (a) { patchSessionAlert(sessionId || a.session, a); emit(); }
  return a;
}
export const markSeen = (alertId, sessionId) => act(() => seenAlert(alertId), sessionId);
export const snooze = (alertId, sessionId, minutes) => act(() => snoozeAlert(alertId, minutes), sessionId);

// ---- stream events -----------------------------------------------------------------------

let overviewTimer = null;
function scheduleOverview() {
  clearTimeout(overviewTimer);
  overviewTimer = setTimeout(refreshOverview, 400);
}


function onEvent(type, data) {
  store.updatedAt = Date.now();
  switch (type) {
    case 'reset': refreshAll(); return;
    case 'session': applySession(data); break;
    case 'usage': applyUsage(data); break;
    case 'machine': applyMachine(data); break;
    case 'seats': applySeatOrder(data); break;
    case 'alert': if (data.session && data.alert) patchSessionAlert(data.session, data.alert); break;
    default: return; // hello, ping: nothing to draw
  }
  emit();
}

function applySession({ session, change }) {
  if (!session) return;
  if (store.sessions) {
    const i = store.sessions.findIndex((s) => s.id === session.id);
    if (i >= 0) store.sessions[i] = { ...store.sessions[i], ...session };
    else store.sessions.unshift(session);
  }
  const o = store.overview;
  if (o) {
    o.needs_you = o.needs_you.filter((s) => s.id !== session.id);
    if (session.needs_you && !hiddenSeen(session)) {
      o.needs_you.push(session);
      o.needs_you.sort((a, b) => Date.parse(a.state_since) - Date.parse(b.state_since));
    }
    o.counts = { ...o.counts, needs_you: o.needs_you.length };
  }
  if (store.detailId === session.id) openDetail(session.id);
  // Counts per state and per-seat live totals come from the hub.
  if (change !== 'update') scheduleOverview();
}

function applyUsage({ seat, windows }) {
  const s = store.overview && store.overview.seats.find((x) => x.id === seat);
  if (s && windows) s.windows = windows;
}

// ---- the order of the accounts -------------------------------------------------------------

// Every list of accounts (the sidebar, Accounts, Now, Sessions groups, the filters) reads
// store.overview.seats, so putting that array in the new order redraws them all.
let seatOrderBusy = 0;

function applySeatOrder(data) {
  // Our own PUT is in flight: its answer settles the order, an event may be an older one.
  if (seatOrderBusy > 0 || !store.overview || !data || !Array.isArray(data.order)) return;
  store.overview.seats = applyOrder(store.overview.seats, data.order);
}

// Owner: the shown accounts in a new order (PUT /v1/seats/order). The board takes the order at
// once and puts it back, saying why, when the hub refuses. Resolves to '' or the error text.
export async function reorderSeats(ids) {
  const o = store.overview;
  if (!o) return 'Not connected yet.';
  const before = o.seats;
  o.seats = applyOrder(before, ids);
  seatOrderBusy++;
  emit();
  let err = '';
  try {
    const r = await putJSON('/v1/seats/order', { ids });
    if (r && Array.isArray(r.accounts) && store.overview) {
      store.overview.seats = applyOrder(store.overview.seats, r.accounts.map((a) => a.id));
    }
  } catch (e) {
    if (e instanceof AuthError) { store.unauthorized = true; stop(); onAuthLost(); return ''; }
    err = e.status === 403 ? 'Sign in as owner to reorder accounts.' : `Could not save the order: ${e.message}`;
    if (store.overview) store.overview.seats = applyOrder(store.overview.seats, before.map((s) => s.id));
  } finally {
    seatOrderBusy--;
  }
  emit();
  return err;
}

function applyMachine({ machine }) {
  const o = store.overview;
  if (!o || !machine) return;
  const i = o.machines.findIndex((m) => m.id === machine.id);
  if (i >= 0) o.machines[i] = machine;
  else o.machines.push(machine);
}

// ---- lifecycle ---------------------------------------------------------------------------

let closeStream = null, pollTimer = null, reconnectTimer = null;

// How long the stream may be down before the board says so.
export const RECONNECT_GRACE = 6000;

// What the live badge should say: 'live', 'connecting' (first connect), 'reconnecting' (down
// for longer than the grace), or 'polling'. A drop shorter than the grace still reads 'live'.
export function liveState(now = Date.now()) {
  const s = store.live;
  if (s === 'live') return 'live';
  if (s === 'polling') return 'polling';
  if (!store.everLive) return 'connecting';
  return now - store.liveAt < RECONNECT_GRACE ? 'live' : 'reconnecting';
}

// Resolves true when signed in and the overview loaded. On false, `store.unauthorized`
// says the hub answered 401 (pair or sign in); otherwise `store.error` holds the reason.
export async function start() {
  stop();
  store.unauthorized = false;
  store.error = null;
  if (!(await refreshMe())) return false;
  const ok = await refreshOverview();
  if (!ok) return false;
  closeStream = openStream({
    initialId: store.overview && Number.isInteger(store.overview.stream_id) ? store.overview.stream_id : null,
    onEvent,
    onResync: refreshAll,
    onStatus: (s) => {
      if (store.live === s) return;
      if (s !== 'live' && store.live === 'live') {
        // A short drop is normal: the badge says "Reconnecting…" only once it lasts.
        clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(emit, RECONNECT_GRACE + 100);
        // The hub ends a revoked device's stream at once: ask now whether this sign-in still
        // works, so a revoked board signs out at once rather than on its next poll.
        checkSignIn();
      }
      if (s === 'live') store.everLive = true;
      store.live = s;
      store.liveAt = Date.now();
      emit();
      if (s === 'live' && store.offline) refreshAll(); // the hub is back: drop the stored copies
    },
  });
  // Poll slowly even while live (a safety net) and quickly when the stream is down.
  let tick = 0;
  pollTimer = setInterval(() => {
    tick++;
    if (store.live !== 'live' || tick % 4 === 0) refreshAll();
  }, 15e3);
  return true;
}

export function stop() {
  if (closeStream) closeStream();
  closeStream = null;
  clearInterval(pollTimer);
  pollTimer = null;
  store.me = null;
  store.overview = null;
  store.sessions = null;
  store.detail = null;
  store.detailId = null;
  store.offline = null;
}
