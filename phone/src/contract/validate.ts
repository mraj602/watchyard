// Runtime validators for the contract types. Each returns a list of problems (empty = valid).
// Pure TypeScript, no dependency: used by the contract tests on the Go goldens and the
// devserver fixtures, and by the app on data it did not make (push data, cached replies).

import { PAYLOAD_FLOATING_KEYS, PAYLOAD_KEYS } from './payload';
import { SNAPSHOT_LABEL_MAX, SNAPSHOT_MAX_MACHINES, SNAPSHOT_MAX_SEATS, SNAPSHOT_NEEDS_LIST } from './snapshot';

type Problems = string[];
type Obj = Record<string, unknown>;

const isObj = (x: unknown): x is Obj => typeof x === 'object' && x !== null && !Array.isArray(x);
const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/;

function field(p: Problems, o: Obj, key: string, kind: string, path: string, optional = false): unknown {
  const v = o[key];
  const at = `${path}.${key}`;
  if (v === undefined) {
    if (!optional) p.push(`${at}: missing`);
    return v;
  }
  const nullable = kind.endsWith('?');
  const k = nullable ? kind.slice(0, -1) : kind;
  if (v === null) {
    if (!nullable) p.push(`${at}: null`);
    return v;
  }
  const ok =
    (k === 'string' && typeof v === 'string') ||
    (k === 'number' && typeof v === 'number' && Number.isFinite(v)) ||
    (k === 'int' && Number.isInteger(v)) ||
    (k === 'boolean' && typeof v === 'boolean') ||
    (k === 'iso' && typeof v === 'string' && ISO.test(v)) ||
    (k === 'object' && isObj(v)) ||
    (k === 'array' && Array.isArray(v));
  if (!ok) p.push(`${at}: want ${kind}, got ${JSON.stringify(v)}`);
  return v;
}

function oneOf(p: Problems, v: unknown, allowed: readonly unknown[], at: string) {
  if (v !== undefined && !allowed.includes(v)) p.push(`${at}: ${JSON.stringify(v)} not in ${allowed.join('|')}`);
}

function root(x: unknown, path: string, p: Problems): Obj | null {
  if (!isObj(x)) {
    p.push(`${path}: not an object`);
    return null;
  }
  return x;
}

/** push.md 6.5 Payload; `exact` also requires Go's key order and no unknown keys. */
export function validatePayload(x: unknown, path = 'payload', exact = false): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  if (o.v !== 1) p.push(`${path}.v: want 1`);
  oneOf(p, field(p, o, 'type', 'string', path), ['alert', 'summary', 'test'], `${path}.type`);
  field(p, o, 'alert', 'string', path, true);
  const alerts = field(p, o, 'alerts', 'array', path, true);
  if (Array.isArray(alerts)) {
    if (alerts.length > 20) p.push(`${path}.alerts: more than 20`);
    alerts.forEach((a, i) => typeof a !== 'string' && p.push(`${path}.alerts[${i}]: not a string`));
  }
  field(p, o, 'trigger', 'string', path, true);
  field(p, o, 'channel', 'string', path, true);
  oneOf(p, field(p, o, 'category', 'string', path, true), ['sessions', 'limits', 'switch', 'drain', 'usage', 'machines', 'test'], `${path}.category`);
  field(p, o, 'title', 'string', path);
  field(p, o, 'body', 'string', path);
  field(p, o, 'tag', 'string', path);
  const url = field(p, o, 'url', 'string', path);
  if (typeof url === 'string' && (!url.startsWith('/') || url.startsWith('//') || url.includes('\\'))) {
    p.push(`${path}.url: not a hub-relative path`);
  }
  field(p, o, 'remote_url', 'string', path, true);
  oneOf(p, field(p, o, 'tap', 'string', path), ['auto', 'session', 'claude'], `${path}.tap`);
  field(p, o, 'session', 'string', path, true);
  field(p, o, 'needs_you', 'int', path);
  const actions = field(p, o, 'actions', 'object', path, true);
  if (isObj(actions)) {
    field(p, actions, 'snooze', 'string', `${path}.actions`, true);
    field(p, actions, 'seen', 'string', `${path}.actions`, true);
  }
  const priv = field(p, o, 'private', 'boolean', path);
  if (priv === true && o.remote_url !== undefined) {
    p.push(`${path}.remote_url: present in a private payload (push.md 6.5)`);
  }
  field(p, o, 'ts', 'iso?', path);
  if (exact) {
    const all = Object.keys(o);
    const floating = (k: string) => (PAYLOAD_FLOATING_KEYS as readonly string[]).includes(k);
    const keys = all.filter((k) => !floating(k));
    const order = PAYLOAD_KEYS.filter((k) => keys.includes(k));
    all.forEach((k) => !(PAYLOAD_KEYS as readonly string[]).includes(k) && p.push(`${path}.${k}: unknown key`));
    if (keys.join(',') !== order.join(',')) p.push(`${path}: keys not in Go order (${keys.join(',')})`);
  }
  return p;
}

/** The `refresh` payload of a widget refresh frame over the link (push.md 12.9). */
export function validateRefresh(x: unknown, path = 'refresh'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  if (o.v !== 1) p.push(`${path}.v: want 1`);
  if (o.type !== 'refresh') p.push(`${path}.type: want refresh`);
  field(p, o, 'needs_you', 'int', path);
  field(p, o, 'ts', 'iso', path);
  const extra = Object.keys(o).filter((k) => !['v', 'type', 'needs_you', 'ts'].includes(k));
  if (extra.length) p.push(`${path}: unexpected keys ${extra.join(',')}`);
  return p;
}

/** phone.md P11 WidgetSnapshot v1. */
export function validateWidgetSnapshot(x: unknown, path = 'snapshot'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  if (o.v !== 1) p.push(`${path}.v: want 1`);
  field(p, o, 'as_of', 'iso', path);
  const hub = field(p, o, 'hub', 'string', path);
  if (typeof hub === 'string' && /[/:@]/.test(hub)) p.push(`${path}.hub: host name only`);
  field(p, o, 'needs_you', 'int', path);
  const priv = field(p, o, 'private', 'boolean', path);
  const top = field(p, o, 'top', 'object?', path);
  if (priv === true && top !== null) p.push(`${path}.top: must be null in private mode`);
  if (isObj(top)) {
    for (const k of ['seat', 'project', 'what']) field(p, top, k, 'string', `${path}.top`);
    field(p, top, 'since', 'iso', `${path}.top`);
  }
  const seats = field(p, o, 'seats', 'array', path);
  if (Array.isArray(seats)) {
    if (seats.length > SNAPSHOT_MAX_SEATS) p.push(`${path}.seats: more than ${SNAPSHOT_MAX_SEATS}`);
    seats.forEach((s, i) => {
      const at = `${path}.seats[${i}]`;
      if (!isObj(s)) return p.push(`${at}: not an object`);
      field(p, s, 'id', 'string', at);
      const label = field(p, s, 'label', 'string', at);
      if (typeof label === 'string' && [...label].length > SNAPSHOT_LABEL_MAX) p.push(`${at}.label: over ${SNAPSHOT_LABEL_MAX}`);
      field(p, s, 'colour', 'string', at);
      for (const w of ['five_hour', 'seven_day']) {
        const win = field(p, s, w, 'object?', at, true);
        if (isObj(win)) {
          field(p, win, 'pct', 'number', `${at}.${w}`);
          field(p, win, 'resets_at', 'iso?', `${at}.${w}`);
        }
      }
    });
  }
  const needs = field(p, o, 'needs_list', 'array', path, true);
  if (Array.isArray(needs)) {
    if (needs.length > SNAPSHOT_NEEDS_LIST) p.push(`${path}.needs_list: more than ${SNAPSHOT_NEEDS_LIST}`);
    if (priv === true && needs.length > 0) p.push(`${path}.needs_list: must be empty in private mode`);
    needs.forEach((n, i) => {
      const at = `${path}.needs_list[${i}]`;
      if (!isObj(n)) return p.push(`${at}: not an object`);
      for (const k of ['id', 'seat', 'colour', 'project', 'what']) field(p, n, k, 'string', at);
      field(p, n, 'since', 'iso', at);
    });
  }
  const machines = field(p, o, 'machines', 'array', path, true);
  if (Array.isArray(machines)) {
    if (machines.length > SNAPSHOT_MAX_MACHINES) p.push(`${path}.machines: more than ${SNAPSHOT_MAX_MACHINES}`);
    machines.forEach((m, i) => {
      const at = `${path}.machines[${i}]`;
      if (!isObj(m)) return p.push(`${at}: not an object`);
      field(p, m, 'id', 'string', at);
      field(p, m, 'name', 'string', at);
      field(p, m, 'online', 'boolean', at);
      field(p, m, 'live', 'int', at);
      field(p, m, 'last_seen', 'iso?', at);
    });
  }
  const counts = field(p, o, 'counts', 'object?', path, true);
  if (isObj(counts)) for (const k of ['working', 'needs_you', 'done']) field(p, counts, k, 'int', `${path}.counts`);
  const nr = field(p, o, 'next_reset', 'object?', path);
  if (isObj(nr)) {
    field(p, nr, 'seat', 'string', `${path}.next_reset`);
    oneOf(p, field(p, nr, 'window', 'string', `${path}.next_reset`), ['five_hour', 'seven_day'], `${path}.next_reset.window`);
    field(p, nr, 'resets_at', 'iso', `${path}.next_reset`);
    field(p, nr, 'pct', 'number', `${path}.next_reset`);
  }
  return p;
}

/** phone.md P12 LiveCountdownProps. */
export function validateLiveProps(x: unknown, path = 'live'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  field(p, o, 'seat', 'string', path);
  field(p, o, 'colour', 'string', path);
  oneOf(p, field(p, o, 'window', 'string', path), ['five_hour', 'seven_day'], `${path}.window`);
  field(p, o, 'pct', 'number', path);
  field(p, o, 'resets_at', 'iso', path);
  field(p, o, 'started_at', 'iso', path);
  return p;
}

function validateAlertBrief(o: Obj, path: string, p: Problems) {
  field(p, o, 'id', 'string', path);
  oneOf(p, field(p, o, 'status', 'string', path), ['pending', 'fired', 'snoozed', 'seen', 'cancelled', 'closed', 'skipped'], `${path}.status`);
  for (const k of ['due_at', 'fired_at', 'snoozed_until', 'seen_at']) field(p, o, k, 'iso?', path, true);
}

/** api.yaml Session. */
export function validateSession(x: unknown, path = 'session'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  field(p, o, 'id', 'string', path);
  oneOf(p, field(p, o, 'state', 'string', path), ['working', 'needs_permission', 'asked', 'done', 'failed', 'ended', 'gone'], `${path}.state`);
  field(p, o, 'needs_you', 'boolean', path);
  field(p, o, 'state_since', 'iso', path);
  for (const k of ['seat', 'seat_label', 'project', 'machine_name', 'remote_url', 'resume', 'question', 'last_message']) {
    field(p, o, k, 'string?', path, true);
  }
  const a = field(p, o, 'alert', 'object?', path, true);
  if (isObj(a)) validateAlertBrief(a, `${path}.alert`, p);
  return p;
}

/** api.yaml Window. */
export function validateWindow(x: unknown, path = 'window'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  field(p, o, 'key', 'string', path);
  // null only for an idle window (not started); an older hub always sends a time.
  field(p, o, 'resets_at', 'iso?', path);
  field(p, o, 'used_pct', 'number?', path, true);
  oneOf(p, field(p, o, 'state', 'string', path, true), ['current', 'reset', 'idle'], `${path}.state`);
  return p;
}

/** api.yaml Seat. */
export function validateSeat(x: unknown, path = 'seat'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  for (const k of ['id', 'account', 'org']) field(p, o, k, 'string', path);
  for (const k of ['label', 'colour', 'email']) field(p, o, k, 'string?', path, true);
  oneOf(p, field(p, o, 'mode', 'string?', path, true), ['protected', 'aggressive', 'off', null], `${path}.mode`);
  field(p, o, 'drain_until', 'iso?', path, true);
  const ws = field(p, o, 'windows', 'array', path, true);
  if (Array.isArray(ws)) ws.forEach((w, i) => p.push(...validateWindow(w, `${path}.windows[${i}]`)));
  return p;
}

/** api.yaml Overview. */
export function validateOverview(x: unknown, path = 'overview'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  field(p, o, 'time', 'iso', path);
  const seats = field(p, o, 'seats', 'array', path);
  if (Array.isArray(seats)) seats.forEach((s, i) => p.push(...validateSeat(s, `${path}.seats[${i}]`)));
  const ny = field(p, o, 'needs_you', 'array', path);
  if (Array.isArray(ny)) ny.forEach((s, i) => p.push(...validateSession(s, `${path}.needs_you[${i}]`)));
  field(p, o, 'machines', 'array', path);
  field(p, o, 'counts', 'object', path);
  return p;
}

/** api.yaml Alert. */
export function validateAlert(x: unknown, path = 'alert'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  validateAlertBrief(o, path, p);
  field(p, o, 'title', 'string', path, true);
  const url = field(p, o, 'url', 'string', path, true);
  if (typeof url === 'string' && !url.startsWith('/#/')) p.push(`${path}.url: not a hash deep link`);
  return p;
}

/** api.yaml DevicePrefs. */
export function validateDevicePrefs(x: unknown, path = 'prefs'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  field(p, o, 'mute', 'array', path, true);
  const cats = field(p, o, 'mute_categories', 'array', path, true);
  if (Array.isArray(cats)) {
    cats.forEach((c, i) => {
      if (typeof c !== 'string') p.push(`${path}.mute_categories[${i}]: not a string`);
    });
  }
  field(p, o, 'private', 'boolean?', path, true);
  oneOf(p, field(p, o, 'tap', 'string', path, true), ['auto', 'session', 'claude'], `${path}.tap`);
  field(p, o, 'tz', 'string?', path, true);
  field(p, o, 'widgets', 'boolean', path, true);
  return p;
}

/** api.yaml PushTarget. */
export function validatePushTarget(x: unknown, path = 'target'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  field(p, o, 'id', 'string', path);
  oneOf(p, field(p, o, 'kind', 'string', path), ['webpush', 'ntfy', 'link'], `${path}.kind`);
  field(p, o, 'enabled', 'boolean', path);
  oneOf(p, field(p, o, 'status', 'string', path), ['ok', 'failing', 'gone', 'disabled', 'new'], `${path}.status`);
  return p;
}

/** api.yaml Me. */
export function validateMe(x: unknown, path = 'me'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  const d = field(p, o, 'device', 'object', path);
  if (isObj(d)) {
    field(p, d, 'id', 'string', `${path}.device`);
    oneOf(p, field(p, d, 'kind', 'string', `${path}.device`), ['machine', 'desktop', 'phone', 'browser'], `${path}.device.kind`);
    field(p, d, 'scopes', 'array', `${path}.device`);
    const prefs = field(p, d, 'prefs', 'object', `${path}.device`, true);
    if (isObj(prefs)) p.push(...validateDevicePrefs(prefs, `${path}.device.prefs`));
  }
  field(p, o, 'owner', 'boolean', path);
  field(p, o, 'hub', 'object', path);
  return p;
}

/** api.yaml PairClaimReply. */
export function validatePairClaimReply(x: unknown, path = 'claim'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  for (const k of ['device', 'kind', 'name']) field(p, o, k, 'string', path);
  field(p, o, 'scopes', 'array', path);
  field(p, o, 'token', 'string', path, true);
  field(p, o, 'hub', 'object', path);
  return p;
}

/** api.yaml MatrixInfo (GET /v1/peers/matrix, peers.md 5). */
export function validateMatrixInfo(x: unknown, path = 'matrix'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  oneOf(p, field(p, o, 'signer', 'string', path), ['owner', 'hub'], `${path}.signer`);
  field(p, o, 'widens', 'boolean', path, true);
  field(p, o, 'created_at', 'iso', path, true);
  field(p, o, 'doc', 'object', path);
  const m = field(p, o, 'matrix', 'object', path);
  if (isObj(m)) {
    const at = `${path}.matrix`;
    field(p, m, 'version', 'int', at);
    field(p, m, 'paused', 'boolean', at);
    field(p, m, 'same_account', 'boolean', at);
    field(p, m, 'max_turns', 'int', at);
    const allow = field(p, m, 'allow', 'array', at);
    if (Array.isArray(allow)) {
      allow.forEach((pair, i) => {
        if (!Array.isArray(pair) || pair.length !== 2 || pair.some((a) => typeof a !== 'string')) p.push(`${at}.allow[${i}]: want [from, to]`);
      });
    }
  }
  return p;
}

/** api.yaml PeerAuditEntry: metadata only. */
export function validatePeerAudit(x: unknown, path = 'audit'): Problems {
  const p: Problems = [];
  const o = root(x, path, p);
  if (!o) return p;
  field(p, o, 'id', 'int', path);
  field(p, o, 'at', 'iso', path);
  field(p, o, 'actor', 'string', path);
  field(p, o, 'event', 'string', path);
  field(p, o, 'thread', 'string?', path, true);
  field(p, o, 'msg', 'string?', path, true);
  field(p, o, 'detail', 'object', path, true);
  return p;
}
