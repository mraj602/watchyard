// Screen test harness: the devserver fixtures behind a fake HubApi, a fake HubClient of
// jest mocks, and a small reducer standing in for the provider's (the real one is not needed to test
// screens, only the actions the screens dispatch). Nothing here touches the network.
import { useReducer, type ReactNode } from 'react';
import type { Alert, AlertBrief, Device, Me, Overview, Seat, Session } from '../src/contract/api';
import type { AppInfo } from '../src/contract/platform';
import { HubApiProvider, type HubApi } from '../src/components/hub-api';
import { reduce, type DataAction, type HubData } from '../src/lib/data';
import { HubError, type HubClient } from '../src/lib/hub';
import accounts from './fixtures/hub/accounts.json';
import alert from './fixtures/hub/alert.json';
import alerts from './fixtures/hub/alerts.json';
import machines from './fixtures/hub/machines.json';
import me from './fixtures/hub/me.json';
import meta from './fixtures/hub/_meta.json';
import overview from './fixtures/hub/overview.json';
import peersAudit from './fixtures/hub/peers-audit.json';
import peersMatrix from './fixtures/hub/peers-matrix.json';
import rules from './fixtures/hub/rules.json';
import session from './fixtures/hub/session.json';
import sessions from './fixtures/hub/sessions.json';

export const NOW = meta.now_ms;
export const fx = { accounts, alert, alerts, machines, me, overview, peersAudit, peersMatrix, rules, session, sessions };

export const APP: AppInfo = { version: '0.8.0', build: '12', model: 'Pixel 9', platform: 'android', dev: false };

export function freshData(patch: Partial<HubData> = {}): HubData {
  return {
    overview: structuredClone(overview) as unknown as Overview,
    sessions: structuredClone(sessions.sessions) as unknown as Session[],
    accounts: structuredClone(accounts.accounts) as unknown as Seat[],
    alerts: structuredClone(alerts.alerts) as unknown as Alert[],
    fetchedAt: NOW,
    offline: false,
    ...patch,
  };
}

const mark = (a: AlertBrief | null | undefined, id: string, patch: Partial<AlertBrief>): AlertBrief | null | undefined =>
  a && a.id === id ? { ...a, ...patch } : a;

/** What the screens dispatch: Seen and Snooze land on every copy of the alert; `loaded` is a poll. */
export function testReduce(state: HubData, action: DataAction): HubData {
  const patchAlert = (id: string, patch: Partial<AlertBrief>): HubData => ({
    ...state,
    overview: state.overview && { ...state.overview, needs_you: state.overview.needs_you.map((s) => ({ ...s, alert: mark(s.alert, id, patch) })) },
    sessions: state.sessions.map((s) => ({ ...s, alert: mark(s.alert, id, patch) })),
  });
  switch (action.type) {
    case 'seen':
      return patchAlert(action.alert, { status: 'seen' });
    case 'snoozed':
      return patchAlert(action.alert, { status: 'snoozed', snoozed_until: action.until });
    case 'seenSession':
      return reduce(state, action);
    case 'offline':
      return { ...state, offline: true };
    case 'loaded':
      // A poll's reply (only what it carries), as the provider's reducer takes it.
      return {
        ...state,
        ...(action.overview ? { overview: action.overview } : {}),
        ...(action.sessions ? { sessions: action.sessions } : {}),
        fetchedAt: action.at,
        offline: false,
      };
    default:
      return state;
  }
}

export type FakeClient = { [K in keyof HubClient]: HubClient[K] extends (...a: infer A) => infer R ? jest.Mock<R, A> : HubClient[K] };

export function fakeClient(over: Partial<Record<keyof HubClient, unknown>> = {}): FakeClient {
  const device = structuredClone(me.device) as unknown as Device;
  const base: Record<string, unknown> = {
    origin: 'https://hub.example.ts.net',
    health: jest.fn(async () => true),
    movedTo: jest.fn(async () => null),
    me: jest.fn(async () => structuredClone(me) as unknown as Me),
    overview: jest.fn(async () => fx.overview),
    sessions: jest.fn(async () => fx.sessions),
    session: jest.fn(async () => structuredClone(session)),
    accounts: jest.fn(async () => fx.accounts),
    machines: jest.fn(async () => fx.machines),
    alerts: jest.fn(async () => structuredClone(alerts)),
    alert: jest.fn(async () => structuredClone(alert)),
    rules: jest.fn(async () => structuredClone(rules)),
    seen: jest.fn(async (id: string) => ({ id, status: 'seen' })),
    seenSession: jest.fn(async () => ({ id: 'al_new', status: 'seen' })),
    snooze: jest.fn(async (id: string) => ({ id, status: 'snoozed' })),
    deviceSelf: jest.fn(async () => structuredClone(device)),
    patchSelf: jest.fn(async (prefs: object) => ({ ...device, prefs })),
    unpairSelf: jest.fn(async () => undefined),
    testTarget: jest.fn(async () => ({ outcome: 'sent' })),
    patchSeat: jest.fn(async (seat: string, patch: object) => ({ id: seat, account: 'a', org: 'o', ...patch })),
    patchTarget: jest.fn(async (id: string, patch: object) => ({ id, kind: 'link', status: 'ok', enabled: true, ...patch })),
    matrix: jest.fn(async () => structuredClone(peersMatrix)),
    editMatrix: jest.fn(async () => structuredClone(peersMatrix)),
    pausePeers: jest.fn(async () => structuredClone(peersMatrix)),
    peerAudit: jest.fn(async () => structuredClone(peersAudit)),
    peerMessages: jest.fn(async () => {
      throw new Error('GET /v1/peers/messages: 501');
    }),
    // An older hub: no trusted senders (404, the section is hidden); no live sessions to list.
    trustedSenders: jest.fn(async () => {
      throw new HubError('/v1/peers/trusted', 404, 'not_found', 'GET /v1/peers/trusted: 404 not_found');
    }),
    peers: jest.fn(async () => ({ sessions: [] })),
    revokeTrustedSender: jest.fn(async (machine: string) => ({ machine })),
  };
  return { ...base, ...over } as unknown as FakeClient;
}

export interface Setup {
  client?: FakeClient;
  data?: Partial<HubData>;
  api?: Partial<HubApi>;
}

export interface Probe {
  dispatched: DataAction[];
  api: HubApi;
  client: FakeClient;
  state: { current: HubData };
}

/** Wraps screens in a provider; `probe` records what the screens dispatch. */
export function makeHarness(setup: Setup = {}): { Wrapper: (p: { children: ReactNode }) => ReactNode; probe: Probe } {
  const client = setup.client ?? fakeClient();
  const initial = freshData(setup.data);
  const state = { current: initial };
  const dispatched: DataAction[] = [];
  const probe = { dispatched, client, state } as Probe;
  // Stable across renders, so tests can assert on them through probe.api.
  const kick = jest.fn(async () => undefined);
  const enablePush = jest.fn(async () => undefined);
  const unpair = jest.fn(async () => undefined);

  function Wrapper({ children }: { children: ReactNode }) {
    const [data, dispatch] = useReducer(testReduce, initial);
    state.current = data;
    const api: HubApi = {
      data,
      dispatch: (a) => {
        dispatched.push(a);
        dispatch(a);
      },
      kick,
      now: NOW,
      host: 'hub.example.ts.net',
      client: client as unknown as HubClient,
      me: structuredClone(me) as unknown as Me,
      app: APP,
      pushState: 'registered',
      enablePush,
      unpair,
      ...setup.api,
    };
    probe.api = api;
    return <HubApiProvider value={api}>{children}</HubApiProvider>;
  }
  return { Wrapper, probe };
}

export const routerMock = {
  router: { push: jest.fn(), replace: jest.fn() },
  useLocalSearchParams: jest.fn(() => ({})),
};
