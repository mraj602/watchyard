// Hub client (phone.md P3).
// Pure: fetch and the clock are injected; errors and logs carry the path and status, never
// the token or a response body.
import type {
  AccountsReply,
  AnalyticsQuery,
  AnalyticsReplies,
  Alert,
  AlertBrief,
  AlertsPage,
  Device,
  DevicePrefs,
  MachinesReply,
  MatrixEdit,
  MatrixInfo,
  Me,
  Overview,
  PeerAuditEntry,
  PeerMessage,
  PushTarget,
  PushTestResult,
  RulesReply,
  SessionDetail,
  SessionsPage,
  UsageRelation,
} from '../../contract/api';
import { HEALTH_TIMEOUT_MS, REQUEST_TIMEOUT_MS } from '../../contract/constants';
import type { Clock, FetchLike, RequestInitLike, ResponseLike } from '../../contract/platform';
import type { PendingRevoke, SecureHub } from '../secure';

/** A failed hub call: the path and status only (P3). */
export class HubError extends Error {
  constructor(
    readonly path: string,
    readonly status: number | null,
    readonly code: string | null,
    message: string,
  ) {
    super(message);
    this.name = 'HubError';
  }
}

/** A hub call refused with 403: the action needs an owner session, which a phone never has. */
export const OWNER_ONLY = 'Only the owner can change this, from a computer.';

/** Phone words for a failed hub call: never "GET /path: 403 forbidden". */
export function errorText(e: unknown, fallback: string): string {
  if (e instanceof HubError) {
    if (e.status === 403) return OWNER_ONLY;
    if (e.code === 'timeout') return 'The hub did not answer in time.';
    if (e.code === 'network') return "Can't reach the hub right now.";
    if (e.status != null) return `${fallback} (the hub answered ${e.status}).`;
    return fallback;
  }
  return e instanceof Error && e.message ? e.message : fallback;
}

export interface HubClientDeps {
  origin: string; // https://hub.example.ts.net
  token: string;
  fetch: FetchLike;
  clock: Clock;
  /** Called once on the first 401 (P3): the app clears everything and shows Pair. */
  onUnauthorized?: () => void;
}

export interface SessionsQuery {
  filter?: string;
  limit?: number;
  cursor?: string;
  /** include_ended: ended sessions too (the hub leaves them out by default). */
  includeEnded?: boolean;
  /** days: only sessions with activity in the last N days. */
  days?: number;
}

export interface HubClient {
  readonly origin: string;
  health(): Promise<boolean>; // GET /health within 1.5 s
  /**
   * GET /health: where a hub frozen for a move says it went (machines.md 8.1, `frozen.moved_to`),
   * as an origin; null when the hub is not frozen, gave no address, or could not be asked.
   */
  movedTo(): Promise<string | null>;
  me(): Promise<Me>;
  overview(): Promise<Overview>;
  sessions(q?: SessionsQuery): Promise<SessionsPage>;
  session(id: string): Promise<SessionDetail>;
  accounts(): Promise<AccountsReply>;
  machines(): Promise<MachinesReply>;
  alerts(q?: { limit?: number; cursor?: string }): Promise<AlertsPage>;
  alert(id: string): Promise<Alert>;
  rules(): Promise<RulesReply>;
  seen(alertId: string): Promise<AlertBrief>;
  /** Seen on a Needs-you row with no alert (push.md 4); older hubs answer 404. */
  seenSession(sessionId: string): Promise<AlertBrief>;
  snooze(alertId: string, minutes?: number): Promise<AlertBrief>;
  deviceSelf(): Promise<Device>;
  patchSelf(prefs: Partial<DevicePrefs>): Promise<Device>;
  unpairSelf(): Promise<void>; // DELETE /v1/devices/self, best effort
  testTarget(id: string): Promise<PushTestResult>;
  /** PATCH /v1/push/targets/{id}: the "Instant delivery" switch is the link target's `enabled` (push.md 13.7). */
  patchTarget(id: string, patch: { enabled?: boolean }): Promise<PushTarget>;
  /** GET /v1/peers/matrix (peers.md 5): the newest account matrix. */
  matrix(): Promise<MatrixInfo>;
  /** PUT /v1/peers/matrix with an `edit`; the hub signs it. A widening edit is 403 for a phone. */
  editMatrix(edit: MatrixEdit): Promise<MatrixInfo>;
  /** POST /v1/peers/pause (peers.md 11): pausing works from any device; resuming needs an owner session. */
  pausePeers(paused: boolean): Promise<MatrixInfo>;
  /** GET /v1/peers/audit: matrix changes, pauses, stops and refusals, newest first; metadata only. */
  peerAudit(q?: { limit?: number }): Promise<{ entries: PeerAuditEntry[] }>;
  /** GET /v1/peers/messages: who messaged whom, size and outcome; the hub may not serve it yet (404, 501). */
  peerMessages(q?: { limit?: number }): Promise<{ messages: PeerMessage[] }>;
  /** GET /v1/analytics/{report} (analytics.md): tokens, API-equivalent cost, models, projects, heatmap, sessions, limit windows and hits. */
  analytics<R extends keyof AnalyticsReplies>(report: R, q?: AnalyticsQuery): Promise<AnalyticsReplies[R]>;
  /** GET /v1/accounts/{seat}/relation: the seat's daily 5 h per 7 d ratio, capacity index, calibration and the picker's newest verdict (api.yaml UsageRelation). */
  relation(seat: string): Promise<UsageRelation>;
}

interface Call {
  method: string;
  /** Path as sent (with query). */
  path: string;
  /** Path as shown in errors: no query, no secrets. */
  label?: string;
  body?: unknown;
  auth?: string;
  timeoutMs: number;
  /** Origin the answer must come from (a redirect elsewhere is refused). */
  origin: string;
}

const seg = encodeURIComponent;

function query(q: Record<string, string | number | undefined>): string {
  const parts = Object.entries(q)
    .filter(([, v]) => v !== undefined && v !== '')
    .map(([k, v]) => `${seg(k)}=${seg(String(v))}`);
  return parts.length ? `?${parts.join('&')}` : '';
}

const DEFAULT_PORT: Record<string, string> = { https: '443', http: '80' };

/**
 * "HTTPS://Hub.ts.net:443/" to "https://hub.ts.net": scheme and host lower-cased, the default
 * port (443 for https, 80 for http) and any trailing slash dropped. Anything that is not
 * scheme://authority comes back trimmed of trailing slashes only.
 */
export function normalizeOrigin(origin: string): string {
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)\/*$/.exec(origin.trim());
  if (!m) return origin.trim().replace(/\/+$/, '');
  const scheme = m[1]!.toLowerCase();
  let authority = m[2]!.toLowerCase();
  // "host:443" or "[::1]:443" (a bare "[::1]" ends in "]", so it never matches)
  const pm = /^(.*):(\d{1,5})$/.exec(authority);
  if (pm && DEFAULT_PORT[scheme] === String(Number(pm[2]))) authority = pm[1]!;
  return `${scheme}://${authority}`;
}

/**
 * A `moved_to` from /health as an https origin other than `current`; null for anything else
 * (nothing, junk, plain http, the hub's own address). Only scheme and host[:port] are kept.
 */
export function movedOrigin(v: unknown, current: string): string | null {
  if (typeof v !== 'string' || v.length > 300) return null;
  const m = /^https:\/\/([^\s/?#@]+)(?:[/?#].*)?$/i.exec(v.trim());
  if (!m) return null;
  const o = normalizeOrigin(`https://${m[1]}`);
  return o === normalizeOrigin(current) ? null : o;
}

/** The answer came from `origin` (compared after normalising both, so ":443" is the same). */
function sameOrigin(res: ResponseLike, origin: string): boolean {
  if (!res.url) return !res.redirected; // a redirect we cannot place is refused
  const m = /^([A-Za-z][A-Za-z0-9+.-]*:\/\/[^/?#]*)/.exec(res.url);
  return !!m && normalizeOrigin(m[1]!) === normalizeOrigin(origin);
}

/** A redirect status: never followed with a bearer token (P3). */
function isRedirect(res: ResponseLike): boolean {
  return res.status >= 300 && res.status < 400;
}

/** Never throws anything but HubError; the message holds the method, path and status only. */
async function send(fetch: FetchLike, clock: Clock, c: Call): Promise<ResponseLike> {
  const label = c.label ?? c.path.split('?')[0] ?? c.path;
  const what = `${c.method} ${label}`;
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (c.auth) headers.Authorization = `Bearer ${c.auth}`;
  // 'error' where the platform honours it; React Native's fetch may follow redirects anyway, so
  // the answer is also checked below (a 3xx, or an answer from elsewhere, is fatal). That check
  // is after the fact: see phone.md P3 on why the pairing probe guards the first contact.
  const init: RequestInitLike = { method: c.method, headers, redirect: 'error' };
  if (c.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(c.body);
  }
  const ctl = new AbortController();
  let timedOut = false;
  const timer = clock.setTimeout(() => {
    timedOut = true;
    ctl.abort();
  }, c.timeoutMs);
  init.signal = ctl.signal;
  try {
    const res = await fetch(`${c.origin}${c.path}`, init);
    if (isRedirect(res)) throw new HubError(label, res.status, 'redirect', `${what}: refused a redirect (${res.status})`);
    if (!sameOrigin(res, c.origin)) throw new HubError(label, res.status, 'redirect', `${what}: redirected to another origin`);
    return res;
  } catch (e) {
    if (e instanceof HubError) throw e;
    if (timedOut) throw new HubError(label, null, 'timeout', `${what}: timed out`);
    throw new HubError(label, null, 'network', `${what}: network error`);
  } finally {
    clock.clearTimeout(timer);
  }
}

/** The Error schema's `code`, if the body has one in the expected shape; nothing else is kept. */
async function errorCode(res: ResponseLike): Promise<string | null> {
  try {
    const v = (await res.json()) as { code?: unknown };
    return typeof v?.code === 'string' && /^[a-z_]{1,30}$/.test(v.code) ? v.code : null;
  } catch {
    return null;
  }
}

async function readJson<T>(res: ResponseLike, label: string, what: string): Promise<T> {
  try {
    return (await res.json()) as T;
  } catch {
    throw new HubError(label, res.status, 'bad_response', `${what}: unreadable reply (${res.status})`);
  }
}

export function createHubClient(deps: HubClientDeps): HubClient {
  const { token, fetch, clock } = deps;
  const origin = normalizeOrigin(deps.origin);
  let unauthorized = false;

  async function call<T>(method: string, path: string, body?: unknown, label?: string): Promise<T> {
    const lab = label ?? path.split('?')[0] ?? path;
    const what = `${method} ${lab}`;
    const res = await send(fetch, clock, { method, path, label: lab, body, auth: token, timeoutMs: REQUEST_TIMEOUT_MS, origin });
    if (res.status === 401) {
      if (!unauthorized) {
        unauthorized = true;
        deps.onUnauthorized?.();
      }
      throw new HubError(lab, 401, 'unauthorized', `${what}: 401 unauthorized`);
    }
    if (!res.ok) {
      const code = await errorCode(res);
      throw new HubError(lab, res.status, code, `${what}: ${res.status}${code ? ` ${code}` : ''}`);
    }
    if (res.status === 204) return undefined as T;
    return readJson<T>(res, lab, what);
  }

  return {
    origin,
    async health() {
      try {
        const res = await send(fetch, clock, { method: 'GET', path: '/health', timeoutMs: HEALTH_TIMEOUT_MS, origin });
        return res.ok;
      } catch {
        return false;
      }
    },
    async movedTo() {
      try {
        const res = await send(fetch, clock, { method: 'GET', path: '/health', timeoutMs: HEALTH_TIMEOUT_MS, origin });
        const body = (await res.json()) as { frozen?: { moved_to?: unknown } };
        return movedOrigin(body?.frozen?.moved_to, origin);
      } catch {
        return null;
      }
    },
    me: () => call<Me>('GET', '/v1/me'),
    overview: () => call<Overview>('GET', '/v1/overview'),
    sessions: (q = {}) => call<SessionsPage>('GET', `/v1/sessions${query({ filter: q.filter, include_ended: q.includeEnded ? 'true' : undefined, days: q.days, limit: q.limit, cursor: q.cursor })}`),
    session: (id) => call<SessionDetail>('GET', `/v1/sessions/${seg(id)}`),
    accounts: () => call<AccountsReply>('GET', '/v1/accounts'),
    machines: () => call<MachinesReply>('GET', '/v1/machines'),
    alerts: (q = {}) => call<AlertsPage>('GET', `/v1/alerts${query({ limit: q.limit, cursor: q.cursor })}`),
    alert: (id) => call<Alert>('GET', `/v1/alerts/${seg(id)}`),
    rules: () => call<RulesReply>('GET', '/v1/rules'),
    seen: (id) => call<AlertBrief>('POST', `/v1/alerts/${seg(id)}/seen`),
    seenSession: (id) => call<AlertBrief>('POST', `/v1/sessions/${seg(id)}/seen`),
    snooze: (id, minutes) => call<AlertBrief>('POST', `/v1/alerts/${seg(id)}/snooze`, minutes === undefined ? {} : { minutes }),
    deviceSelf: () => call<Device>('GET', '/v1/devices/self'),
    patchSelf: (prefs) => call<Device>('PATCH', '/v1/devices/self', { prefs }),
    async unpairSelf() {
      try {
        await call<unknown>('DELETE', '/v1/devices/self');
      } catch {
        // best effort (P3): the caller clears local state either way
      }
    },
    testTarget: (id) => call<PushTestResult>('POST', `/v1/push/targets/${seg(id)}/test`),
    patchTarget: (id, patch) => call<PushTarget>('PATCH', `/v1/push/targets/${seg(id)}`, patch),
    matrix: () => call<MatrixInfo>('GET', '/v1/peers/matrix'),
    editMatrix: (edit) => call<MatrixInfo>('PUT', '/v1/peers/matrix', { edit }),
    pausePeers: (paused) => call<MatrixInfo>('POST', '/v1/peers/pause', { paused }),
    peerAudit: (q = {}) => call<{ entries: PeerAuditEntry[] }>('GET', `/v1/peers/audit${query({ limit: q.limit })}`),
    peerMessages: (q = {}) => call<{ messages: PeerMessage[] }>('GET', `/v1/peers/messages${query({ limit: q.limit })}`),
    relation: (seat) => call<UsageRelation>('GET', `/v1/accounts/${seg(seat)}/relation`),
    analytics: (report, q = {}) => call<AnalyticsReplies[typeof report]>('GET', `/v1/analytics/${seg(report)}${query({ days: q.days, seat: q.seat })}`),
  };
}

// ------------------------------------------------------------------ unpairing (P3)

/**
 * DELETE <origin>/v1/devices/self with that pairing's token. 'done' when the hub answered 2xx,
 * 401 (already revoked) or 404 (already gone); 'retry' for anything else (unreachable, a 5xx,
 * a redirect). Never throws; nothing it returns carries the token.
 */
export async function revokeDevice(deps: { fetch: FetchLike; clock: Clock }, r: PendingRevoke): Promise<'done' | 'retry'> {
  const origin = normalizeOrigin(r.origin);
  try {
    const res = await send(deps.fetch, deps.clock, { method: 'DELETE', path: '/v1/devices/self', auth: r.token, timeoutMs: REQUEST_TIMEOUT_MS, origin });
    return res.ok || res.status === 401 || res.status === 404 ? 'done' : 'retry';
  } catch {
    return 'retry';
  }
}

const sameRevoke = (a: PendingRevoke, b: PendingRevoke) => a.origin === b.origin && a.token === b.token;

/**
 * Retries the unconfirmed unpairings in the keychain (SECURE_REVOKE_KEY), one DELETE each, and
 * keeps only those still to retry. `only` limits the attempt to some (the others stay as they
 * are). Never throws: a locked keychain leaves everything for the next try.
 */
export async function flushPendingRevokes(
  secure: Pick<SecureHub, 'loadRevokes' | 'saveRevokes'>,
  deps: { fetch: FetchLike; clock: Clock },
  only?: (r: PendingRevoke) => boolean,
): Promise<void> {
  let list: PendingRevoke[];
  try {
    list = await secure.loadRevokes();
  } catch {
    return;
  }
  const done: PendingRevoke[] = [];
  for (const r of list) {
    if (only && !only(r)) continue;
    if ((await revokeDevice(deps, r)) === 'done') done.push(r);
  }
  if (done.length === 0) return;
  try {
    // Re-read: an unpairing recorded while these were out is kept.
    const now = await secure.loadRevokes();
    await secure.saveRevokes(now.filter((x) => !done.some((d) => sameRevoke(d, x))));
  } catch {
    // the next try sends them again; a 401 or 404 then ends them
  }
}

/** Records an unpairing to confirm (the newest last; a duplicate collapses). */
export async function addPendingRevoke(secure: Pick<SecureHub, 'loadRevokes' | 'saveRevokes'>, r: PendingRevoke): Promise<void> {
  let list: PendingRevoke[];
  try {
    list = await secure.loadRevokes();
  } catch {
    list = [];
  }
  await secure.saveRevokes([...list.filter((x) => !sameRevoke(x, r)), { origin: r.origin, token: r.token }]);
}
