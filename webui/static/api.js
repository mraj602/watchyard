// Hub API client: JSON reads, login/logout, pairing, alerts, devices and the live stream.
// The browser sends the session cookie itself (same origin), so no token is ever kept in
// JavaScript.

export class AuthError extends Error {
  constructor(message = 'Not signed in', reason = '') {
    super(message);
    this.name = 'AuthError';
    this.reason = reason; // api.yaml Unauthorized: revoked | expired | owner_expired | ''
  }
}

// Why the last 401 happened (the hub's `reason`), for the sign-in page's reason line.
let lastReason = '';
export const authReason = () => lastReason;

// The service worker answers a read from its copy when the hub cannot be reached and marks
// it with X-Wy-Cached-At. The store listens: a number means "showing data from then", null
// means the hub answered live.
let onCached = () => {};
export const setCachedHandler = (fn) => { onCached = fn; };

// Tell the service worker to drop its stored hub data (a 401, unpair, sign out): that copy
// is the only one of the hub's data on the phone.
export function clearCachedData() {
  try {
    if (navigator.serviceWorker && navigator.serviceWorker.controller) navigator.serviceWorker.controller.postMessage({ type: 'clear-data' });
  } catch (_) { /* no worker */ }
}

// opts.anon: a 401 is an answer ("wrong code"), not a lost session.
async function request(path, init, opts = {}) {
  let res;
  try {
    // Every state change carries Content-Type: application/json (CSRF rule: the hub rejects
    // cookie writes without it), with {} when there is no body.
    const write = init && init.method && init.method !== 'GET' && init.method !== 'HEAD';
    const base = write ? { 'Content-Type': 'application/json', Accept: 'application/json' } : { Accept: 'application/json' };
    const extra = write && init.body === undefined ? { body: '{}' } : {};
    res = await fetch(path, { credentials: 'same-origin', ...init, ...extra, headers: { ...base, ...((init && init.headers) || {}) } });
  } catch (e) {
    throw new Error('Cannot reach the hub');
  }
  if (res.status === 401 && !opts.anon) {
    clearCachedData();
    let reason = '';
    try { reason = (await res.json()).reason || ''; } catch (_) { /* not JSON */ }
    lastReason = reason;
    throw new AuthError(undefined, reason);
  }
  if (!res.ok) {
    let msg = `Hub answered ${res.status}`;
    let code = '', at = null, rule = '', ttlS = 0;
    try { const j = await res.json(); msg = j.error || msg; code = j.code || ''; at = j.at || null; rule = j.rule || ''; ttlS = j.ttl_s || 0; } catch (_) { /* not JSON */ }
    const err = new Error(code === 'offline' ? 'Cannot reach the hub' : msg);
    err.status = res.status;
    err.code = code;
    err.rule = rule; // duplicate_rule: the id of the rule that already matches
    err.at = at; // when a refused owner link was used / expired (api.yaml ownerSignIn)
    err.ttl_s = ttlS; // the lifetime the refused owner link was made with
    throw err;
  }
  if (!init || !init.method || init.method === 'GET') {
    const cached = res.headers.get('X-Wy-Cached-At');
    onCached(cached ? Number(cached) : null);
  }
  return res.status === 204 ? null : res.json();
}

export function getJSON(path, params) {
  const qs = params ? new URLSearchParams(params).toString() : '';
  return request(qs ? `${path}?${qs}` : path);
}

const JSON_HEADERS = { 'Content-Type': 'application/json', Accept: 'application/json' };
const send = (method, path, body, opts) =>
  request(path, { method, headers: JSON_HEADERS, body: body === undefined ? '{}' : JSON.stringify(body) }, opts);
export const postJSON = (path, body, opts) => send('POST', path, body, opts);
export const putJSON = (path, body) => send('PUT', path, body);
export const patchJSON = (path, body) => send('PATCH', path, body);
export const del = (path) => request(path, { method: 'DELETE', headers: JSON_HEADERS });

const enc = encodeURIComponent;

export const getOverview = () => getJSON('/v1/overview');
export const getSessions = () => getJSON('/v1/sessions', { include_ended: 'true', days: '2', limit: '500' });
export const getSession = (id) => getJSON(`/v1/sessions/${enc(id)}`);
export const getRelation = (seat) => getJSON(`/v1/accounts/${enc(seat)}/relation`);

export const login = (token) => postJSON('/v1/session/login', { token }, { anon: true });

// Sign out: the hub revokes this browser's device (a board, owner or cookie-paired session;
// pairing.md 6) and clears the cookie. A chosen sign-out has no reason line.
export async function logout() {
  lastReason = '';
  try { return await postJSON('/v1/session/logout'); } finally { clearCachedData(); }
}

// ---- who am I, pairing, owner sign-in -----------------------------------------

export const getMe = () => getJSON('/v1/me');
export const claimPair = (body) => postJSON('/v1/pair/claim', { cookie: true, ...body }, { anon: true });
export const ownerSignIn = (secret, name) => postJSON('/v1/session/owner', { secret, name }, { anon: true });
export const makePairCode = (body) => postJSON('/v1/pair/codes', body);
export const makeOwnerLink = () => postJSON('/v1/owner-links', {});
export const getPairCode = (id) => getJSON(`/v1/pair/codes/${enc(id)}`);
export const cancelPairCode = (id) => del(`/v1/pair/codes/${enc(id)}`);

// ---- devices and push targets ------------------------------------------------------------

export const listDevices = () => getJSON('/v1/devices');
export const getSelf = () => getJSON('/v1/devices/self');
export const patchDevice = (id, body) => patchJSON(`/v1/devices/${enc(id)}`, body);
export async function revokeDevice(id) {
  try { return await del(`/v1/devices/${enc(id)}`); } finally { if (id === 'self') clearCachedData(); }
}
export const getPushConfig = () => getJSON('/v1/push/config');
export const listTargets = () => getJSON('/v1/push/targets');
export const addTarget = (body) => postJSON('/v1/push/targets', body);
export const deleteTarget = (id) => del(`/v1/push/targets/${enc(id)}`);
export const testTarget = (id) => postJSON(`/v1/push/targets/${enc(id)}/test`);
export const putPushChannel = (kind, body) => putJSON(`/v1/push/channels/${enc(kind)}`, body);
export const putPushSettings = (body) => putJSON('/v1/push/settings', body);

// ---- rules and alerts --------------------------------------------------------------------

export const getRules = () => getJSON('/v1/rules');
export const putRule = (id, body) => putJSON(`/v1/rules/${enc(id)}`, body);
export const addRule = (body) => postJSON('/v1/rules', body);
export const deleteRule = (id) => del(`/v1/rules/${enc(id)}`);
export const getAnalytics = (report, params) => getJSON(`/v1/analytics/${enc(report)}`, params);
export const getMatrix = () => getJSON('/v1/peers/matrix');
export const editMatrix = (body) => putJSON('/v1/peers/matrix', body);
export const pausePeers = (paused) => postJSON('/v1/peers/pause', { paused });
export const resumePeerThread = (thread) => postJSON(`/v1/peers/threads/${enc(thread)}/resume`);
export const getPeerAudit = (params) => getJSON('/v1/peers/audit', params);
export const getPeers = () => getJSON('/v1/peers');
export const getTrustedSenders = () => getJSON('/v1/peers/trusted');
export const revokeTrustedSender = (body) => postJSON('/v1/peers/trusted/revoke', body);
export const getPeerMessages = (params) => getJSON('/v1/peers/messages', params);
export const getAlerts = (params) => getJSON('/v1/alerts', params);
export const getAlert = (id) => getJSON(`/v1/alerts/${enc(id)}`);
export const snoozeAlert = (id, minutes) => postJSON(`/v1/alerts/${enc(id)}/snooze`, minutes ? { minutes } : {});
export const seenAlert = (id) => postJSON(`/v1/alerts/${enc(id)}/seen`);
export const seenSession = (id) => postJSON(`/v1/sessions/${enc(id)}/seen`);

// /health is outside the API and public; used to compare the running build with the hub's.
export async function getHealth() {
  const res = await fetch('/health', { cache: 'no-store' });
  if (!res.ok) throw new Error(`health ${res.status}`);
  return res.json();
}

// ---- the live stream ---------------------------------------------------------------------

const EVENT_TYPES = ['hello', 'reset', 'ping', 'session', 'usage', 'machine', 'alert', 'seats'];

// Opens /v1/stream with EventSource. The browser reconnects by itself and sends
// Last-Event-ID. When it gives up (the connection was refused or closed for good) we
// reconnect with backoff; that new EventSource cannot carry the old id, so `onResync`
// tells the caller to refetch. A silent connection (no ping for 70 s) is recycled.
//
// onStatus gets 'connecting' | 'live' | 'polling' ('polling' after repeated failures; the
// caller then polls while this keeps trying to reconnect).
//
// initialId is the overview's stream_id: the first connect passes it as ?last_event_id= so
// events published between the overview fetch and the subscription are replayed.
export function openStream({ onEvent, onStatus, onResync, initialId }) {
  let es = null, retry = null, failures = 0, lastSeen = 0, stopped = false, manual = false;

  const watchdog = setInterval(() => {
    if (es && lastSeen && Date.now() - lastSeen > 70e3) { fail(); }
  }, 10e3);

  function fail() {
    if (es) es.close();
    es = null;
    failures++;
    onStatus(failures >= 3 ? 'polling' : 'connecting');
    clearTimeout(retry);
    retry = setTimeout(connect, Math.min(30e3, 1e3 * 2 ** failures));
    manual = true;
  }

  function connect() {
    if (stopped) return;
    const first = initialId != null;
    es = new EventSource(first ? `/v1/stream?last_event_id=${encodeURIComponent(initialId)}` : '/v1/stream');
    initialId = null;
    lastSeen = Date.now();
    es.onopen = () => {
      failures = 0;
      lastSeen = Date.now();
      onStatus('live');
      if (manual) { manual = false; onResync(); }
    };
    for (const type of EVENT_TYPES) {
      es.addEventListener(type, (ev) => {
        lastSeen = Date.now();
        let data = {};
        try { data = JSON.parse(ev.data || '{}'); } catch (_) { return; }
        onEvent(type, data, ev.lastEventId);
      });
    }
    es.onerror = () => {
      if (es && es.readyState === EventSource.CLOSED) fail();
      else {
        // The browser is retrying on its own.
        failures++;
        onStatus(failures >= 3 ? 'polling' : 'connecting');
      }
    };
  }

  connect();
  return () => {
    stopped = true;
    clearInterval(watchdog);
    clearTimeout(retry);
    if (es) es.close();
  };
}

// ---- keys (keys.md 3.3) ---------------------------------------------------------------------

export const getKeys = () => getJSON('/v1/keys');
export const confirmOwnerKey = (fp) => postJSON('/v1/keys/owner/confirm', { fp });
