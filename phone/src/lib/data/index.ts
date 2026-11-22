// Poller, last-known cache and reducer (phone.md P5). The cache (AsyncStorage
// wy.cache.v1) never holds a token or secret: it stores hub replies only.
import type { Alert, AlertBrief, Overview, Seat, Session } from '../../contract/api';
import { CACHE_KEY, POLL_MS } from '../../contract/constants';
import type { AppStateSource, Clock, KVStore } from '../../contract/platform';
import { orderSeats } from '../format/seat';
import { HubError } from '../hub';
import type { HubClient } from '../hub';

export interface HubData {
  overview: Overview | null;
  sessions: Session[];
  accounts: Seat[];
  alerts: Alert[];
  /** When the last good reply arrived (ms); null before the first. */
  fetchedAt: number | null;
  /** True while the hub cannot be reached and the cache is shown. */
  offline: boolean;
}

export type DataAction =
  | { type: 'loaded'; at: number; overview?: Overview; sessions?: Session[]; accounts?: Seat[]; alerts?: Alert[] }
  | { type: 'offline'; at: number }
  | { type: 'seen'; alert: string }
  | { type: 'seenSession'; session: string }
  | { type: 'snoozed'; alert: string; until: string }
  | { type: 'reset' };

export const initialData: HubData = {
  overview: null,
  sessions: [],
  accounts: [],
  alerts: [],
  fetchedAt: null,
  offline: false,
};

function patchBrief<T extends { id: string }>(a: T, id: string, patch: Partial<AlertBrief>): T {
  return a.id === id ? { ...a, ...patch } : a;
}

function patchSession(s: Session, id: string, patch: Partial<AlertBrief>): Session {
  return s.alert && s.alert.id === id ? { ...s, alert: { ...s.alert, ...patch } } : s;
}

/** Optimistic Seen or Snooze: the alert on every session and in the alerts list. */
function patchAll(state: HubData, id: string, patch: Partial<AlertBrief>): HubData {
  return {
    ...state,
    overview: state.overview
      ? { ...state.overview, needs_you: state.overview.needs_you.map((s) => patchSession(s, id, patch)) }
      : null,
    sessions: state.sessions.map((s) => patchSession(s, id, patch)),
    alerts: state.alerts.map((a) => patchBrief(a, id, patch)),
  };
}

/** Optimistic Seen on a Needs-you row with no alert: a seen brief until the next refresh. */
function seenSessionAll(state: HubData, id: string): HubData {
  const mark = (s: Session): Session => (s.id === id && !s.alert ? { ...s, alert: { id: '', status: 'seen' } as AlertBrief } : s);
  return {
    ...state,
    overview: state.overview ? { ...state.overview, needs_you: state.overview.needs_you.map(mark) } : null,
    sessions: state.sessions.map(mark),
  };
}

/** Every list of accounts follows the owner's order (`seat.order`), whatever the reply's order. */
function inHubOrder(o: Overview): Overview {
  const seats = orderSeats(o.seats ?? []);
  return seats === o.seats ? o : { ...o, seats };
}

export function reduce(state: HubData, action: DataAction): HubData {
  switch (action.type) {
    case 'loaded':
      return {
        overview: action.overview ? inHubOrder(action.overview) : state.overview,
        sessions: action.sessions ?? state.sessions,
        accounts: action.accounts ? orderSeats(action.accounts) : state.accounts,
        alerts: action.alerts ?? state.alerts,
        fetchedAt: action.at,
        offline: false,
      };
    case 'offline':
      return state.offline ? state : { ...state, offline: true };
    case 'seen':
      return patchAll(state, action.alert, { status: 'seen' });
    case 'seenSession':
      return seenSessionAll(state, action.session);
    case 'snoozed':
      return patchAll(state, action.alert, { status: 'snoozed', snoozed_until: action.until });
    case 'reset':
      return initialData;
  }
}

export interface Poller {
  start(): void;
  stop(): void;
  /** Refresh now (foreground, a push arrived, after Seen or Snooze). */
  kick(): Promise<void>;
}

export interface PollerDeps {
  client: HubClient;
  clock: Clock;
  appState: AppStateSource;
  cache: KVStore;
  dispatch: (a: DataAction) => void;
  /** After every good refresh: rebuild the widget snapshot, update the Live Activity (P5). */
  afterRefresh?: (d: HubData) => Promise<void>;
}

const ALERTS_LIMIT = 50;
const SESSIONS_LIMIT = 200;

/**
 * Foreground polling every 15 s plus an immediate refresh when the app becomes active.
 * Nothing runs in the background. Overlapping
 * refreshes share one request. A 401 is the client's business (onUnauthorized), not "offline".
 */
export function createPoller(deps: PollerDeps): Poller {
  const { client, clock, appState, cache, dispatch } = deps;
  let timer: unknown = null;
  let unsub: (() => void) | null = null;
  let running = false;
  let inflight: Promise<void> | null = null;
  let last: HubData = initialData;
  let epoch = 0; // bumped by stop(): a refresh that finishes after it must not write anything

  const clearTimer = () => {
    if (timer !== null) clock.clearTimeout(timer);
    timer = null;
  };

  const schedule = () => {
    clearTimer();
    if (!running || appState.current() !== 'active') return;
    timer = clock.setTimeout(() => {
      timer = null;
      void kick().finally(schedule);
    }, POLL_MS);
  };

  async function refresh(): Promise<void> {
    const at = clock.now();
    const mine = epoch;
    const results = await Promise.allSettled([
      client.overview(),
      // Ended sessions of the last 2 days too, for Sessions > Ended or idle (as the board's getSessions).
      client.sessions({ includeEnded: true, days: 2, limit: SESSIONS_LIMIT }),
      client.accounts(),
      client.alerts({ limit: ALERTS_LIMIT }),
    ]);
    if (mine !== epoch) return; // stopped (unpaired) while the request was out
    const [o, s, a, al] = results;
    if (o.status === 'fulfilled' && s.status === 'fulfilled' && a.status === 'fulfilled' && al.status === 'fulfilled') {
      const action: DataAction = {
        type: 'loaded',
        at,
        overview: o.value,
        sessions: s.value.sessions,
        accounts: a.value.accounts,
        alerts: al.value.alerts,
      };
      last = reduce(last, action);
      dispatch(action);
      try {
        await saveCache(cache, last);
      } catch {
        // a full disk must not break the screens
      }
      // Stopped (unpaired) while the cache was written: the widget, badge and Live Activity
      // steps of the old hub must not run after the clearing. (The write itself was issued
      // before stop(), so the clearing's removeItem, issued after, still wins.)
      if (mine !== epoch) return;
      try {
        await deps.afterRefresh?.(last);
      } catch {
        // widgets are best effort
      }
      return;
    }
    const failed = results.find((r): r is PromiseRejectedResult => r.status === 'rejected');
    if (failed?.reason instanceof HubError && failed.reason.status === 401) return;
    dispatch({ type: 'offline', at });
  }

  function kick(): Promise<void> {
    if (!inflight) {
      inflight = refresh().finally(() => {
        inflight = null;
      });
    }
    return inflight;
  }

  return {
    start() {
      if (running) return;
      running = true;
      unsub = appState.subscribe((st) => {
        if (st === 'active') {
          void kick();
          schedule();
        } else {
          clearTimer();
        }
      });
      if (appState.current() === 'active') void kick();
      schedule();
    },
    stop() {
      running = false;
      epoch++;
      clearTimer();
      unsub?.();
      unsub = null;
    },
    kick() {
      return kick().finally(() => {
        if (running) schedule();
      });
    },
  };
}

interface CacheFile {
  v: 1;
  at: number;
  overview: Overview | null;
  sessions: Session[];
  accounts: Seat[];
  alerts: Alert[];
}

/** The last good replies, or null when there are none or they are unreadable. */
export async function loadCache(cache: KVStore): Promise<HubData | null> {
  let text: string | null;
  try {
    text = await cache.getItem(CACHE_KEY);
  } catch {
    return null;
  }
  if (!text) return null;
  try {
    const f = JSON.parse(text) as Partial<CacheFile>;
    if (f.v !== 1 || typeof f.at !== 'number') return null;
    if (!Array.isArray(f.sessions) || !Array.isArray(f.accounts) || !Array.isArray(f.alerts)) return null;
    if (f.overview !== null && (typeof f.overview !== 'object' || !Array.isArray(f.overview.needs_you))) return null;
    return { overview: f.overview ?? null, sessions: f.sessions, accounts: f.accounts, alerts: f.alerts, fetchedAt: f.at, offline: false };
  } catch {
    return null;
  }
}

export async function saveCache(cache: KVStore, data: HubData): Promise<void> {
  if (data.fetchedAt === null) return;
  const f: CacheFile = {
    v: 1,
    at: data.fetchedAt,
    overview: data.overview,
    sessions: data.sessions,
    accounts: data.accounts,
    alerts: data.alerts,
  };
  await cache.setItem(CACHE_KEY, JSON.stringify(f));
}
