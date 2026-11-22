import { CACHE_KEY, POLL_MS } from '../../contract/constants';
import { fakeAppState, fakeFetch, flush, manualClock, memoryKV, ORIGIN, response, TOKEN } from '../../../test/hubfake';
import type { FakeFetch } from '../../../test/hubfake';
import overviewFx from '../../../test/fixtures/hub/overview.json';
import sessionsFx from '../../../test/fixtures/hub/sessions.json';
import accountsFx from '../../../test/fixtures/hub/accounts.json';
import alertsFx from '../../../test/fixtures/hub/alerts.json';
import { createHubClient } from '../hub';
import type { Alert, Overview, Seat, Session } from '../../contract/api';
import { createPoller, initialData, loadCache, reduce, saveCache } from './index';
import type { DataAction, HubData } from './index';

const overview = overviewFx as unknown as Overview;
const sessions = (sessionsFx as unknown as { sessions: Session[] }).sessions;
const accounts = (accountsFx as unknown as { accounts: Seat[] }).accounts;
const alerts = (alertsFx as unknown as { alerts: Alert[] }).alerts;

const loaded = (at = 1000): DataAction => ({ type: 'loaded', at, overview, sessions, accounts, alerts });

function alertId(): string {
  const a = overview.needs_you.find((s) => s.alert)?.alert;
  if (!a) throw new Error('fixture has no needs-you alert');
  return a.id;
}

describe('reduce', () => {
  it('loaded replaces the replies and clears offline', () => {
    const off = reduce(reduce(initialData, loaded(1)), { type: 'offline', at: 2 });
    expect(off.offline).toBe(true);
    const s = reduce(off, loaded(3));
    expect(s).toMatchObject({ fetchedAt: 3, offline: false, overview, sessions, accounts, alerts });
  });

  it("loaded puts the accounts in the owner's order (seat.order), in every list", () => {
    const a = accounts.map((x, i) => ({ ...x, order: accounts.length - i }));
    const o = { ...overview, seats: overview.seats.map((x, i) => ({ ...x, order: overview.seats.length - i })) };
    const s = reduce(initialData, { type: 'loaded', at: 1, overview: o, accounts: a });
    expect(s.accounts.map((x) => x.id)).toEqual([...a].reverse().map((x) => x.id));
    expect(s.overview?.seats.map((x) => x.id)).toEqual([...o.seats].reverse().map((x) => x.id));
    // A reply already in order is kept as it is.
    const same = reduce(initialData, loaded(1));
    expect(same.accounts).toBe(accounts);
    expect(same.overview).toBe(overview);
  });

  it('loaded keeps the pieces it was not given', () => {
    const s = reduce(reduce(initialData, loaded(1)), { type: 'loaded', at: 2, accounts: [] });
    expect(s.accounts).toEqual([]);
    expect(s.overview).toBe(overview);
    expect(s.fetchedAt).toBe(2);
  });

  it('offline keeps the data and the last good time', () => {
    const s = reduce(reduce(initialData, loaded(5)), { type: 'offline', at: 9 });
    expect(s).toMatchObject({ offline: true, fetchedAt: 5, overview });
  });

  it('seen dims the alert everywhere, optimistically, without mutating', () => {
    const id = alertId();
    const before = reduce(initialData, loaded());
    const snapshot = JSON.stringify(before);
    const after = reduce(before, { type: 'seen', alert: id });
    expect(JSON.stringify(before)).toBe(snapshot);
    expect(after.overview!.needs_you.find((s) => s.alert?.id === id)!.alert!.status).toBe('seen');
    expect(after.alerts.find((a) => a.id === id)?.status ?? 'seen').toBe('seen');
    const other = after.overview!.needs_you.filter((s) => s.alert && s.alert.id !== id);
    const otherBefore = before.overview!.needs_you.filter((s) => s.alert && s.alert.id !== id);
    expect(other).toEqual(otherBefore);
  });

  it('snoozed sets the status and the time', () => {
    const id = alertId();
    const after = reduce(reduce(initialData, loaded()), { type: 'snoozed', alert: id, until: '2026-10-13T12:15:00.000Z' });
    const a = after.overview!.needs_you.find((s) => s.alert?.id === id)!.alert!;
    expect(a).toMatchObject({ status: 'snoozed', snoozed_until: '2026-10-13T12:15:00.000Z' });
    const inSessions = after.sessions.find((s) => s.alert?.id === id);
    if (inSessions) expect(inSessions.alert).toMatchObject({ status: 'snoozed' });
  });

  it('seen on an unknown alert changes nothing visible', () => {
    const before = reduce(initialData, loaded());
    expect(reduce(before, { type: 'seen', alert: 'nope' })).toEqual(before);
    expect(reduce(initialData, { type: 'seen', alert: 'x' })).toEqual(initialData);
  });

  it('seenSession marks a Needs-you row with no alert seen, and leaves rows with an alert alone', () => {
    const before = reduce(initialData, loaded());
    const ov = before.overview!;
    const bare = { ...ov.needs_you[0]!, id: 's-none', alert: null };
    const withBare = { ...before, overview: { ...ov, needs_you: [...ov.needs_you, bare] } };
    const after = reduce(withBare, { type: 'seenSession', session: 's-none' });
    expect(after.overview!.needs_you.find((s) => s.id === 's-none')!.alert).toMatchObject({ status: 'seen' });
    const withAlert = ov.needs_you.find((s) => s.alert)!;
    expect(reduce(withBare, { type: 'seenSession', session: withAlert.id }).overview!.needs_you).toEqual(withBare.overview.needs_you);
  });

  it('reset returns the empty state', () => {
    expect(reduce(reduce(initialData, loaded()), { type: 'reset' })).toBe(initialData);
  });
});

describe('cache', () => {
  it('round-trips and holds no token or secret', async () => {
    const kv = memoryKV();
    const data = reduce(initialData, loaded(1234));
    await saveCache(kv, data);
    const raw = kv.items.get(CACHE_KEY)!;
    expect(raw).not.toContain(TOKEN);
    expect(raw).not.toContain('"token"');
    expect(raw).not.toContain('secret');
    expect(await loadCache(kv)).toEqual(data);
  });

  it('does not save before a first good reply', async () => {
    const kv = memoryKV();
    await saveCache(kv, initialData);
    expect(kv.items.size).toBe(0);
  });

  it('ignores a missing, damaged or foreign cache', async () => {
    const kv = memoryKV();
    expect(await loadCache(kv)).toBeNull();
    for (const bad of ['', 'x', '{}', '{"v":2,"at":1}', '{"v":1,"at":"1"}', '{"v":1,"at":1,"sessions":[],"accounts":[],"alerts":[]}', '{"v":1,"at":1,"overview":{},"sessions":[],"accounts":[],"alerts":[]}']) {
      kv.items.set(CACHE_KEY, bad);
      expect(await loadCache(kv)).toBeNull();
    }
    expect(
      await loadCache({ ...kv, getItem: () => Promise.reject(new Error('disk')) }),
    ).toBeNull();
  });
});

describe('poller', () => {
  function setup(opts: { state?: string; handler?: (url: string) => ReturnType<typeof response> | Promise<never> } = {}) {
    const clock = manualClock();
    const appState = fakeAppState(opts.state ?? 'active');
    const cache = memoryKV();
    const dispatched: DataAction[] = [];
    let n = 0;
    const route = (url: string) => {
      n++;
      if (url.endsWith('/v1/overview')) return response(200, overview);
      if (url.includes('/v1/sessions')) return response(200, { sessions, next: null });
      if (url.endsWith('/v1/accounts')) return response(200, { accounts });
      if (url.includes('/v1/alerts')) return response(200, { alerts, next: null });
      return response(404, {});
    };
    const fetch: FakeFetch = fakeFetch((url) => (opts.handler ? opts.handler(url) : route(url)));
    const onUnauthorized = jest.fn();
    const client = createHubClient({ origin: ORIGIN, token: TOKEN, fetch, clock, onUnauthorized });
    const afterRefresh = jest.fn(async (_d: HubData) => {});
    const poller = createPoller({ client, clock, appState, cache, dispatch: (a) => dispatched.push(a), afterRefresh });
    return { clock, appState, cache, dispatched, fetch, poller, afterRefresh, onUnauthorized, requests: () => fetch.calls.length, n: () => n };
  }

  it('refreshes at once on start, then every 15 s while active', async () => {
    const t = setup();
    t.poller.start();
    await flush();
    expect(t.dispatched.map((a) => a.type)).toEqual(['loaded']);
    expect(t.requests()).toBe(4); // overview, sessions, accounts, alerts
    // Ended sessions come too, or Sessions > Ended or idle could never list one (T3).
    expect(t.fetch.calls.map((c) => c.url).find((u) => u.includes('/v1/sessions'))).toContain('include_ended=true&days=2');

    await t.clock.advance(POLL_MS - 1);
    expect(t.dispatched).toHaveLength(1);
    await t.clock.advance(1);
    await flush();
    expect(t.dispatched).toHaveLength(2);
    await t.clock.advance(POLL_MS);
    await flush();
    expect(t.dispatched).toHaveLength(3);
    t.poller.stop();
    await t.clock.advance(POLL_MS * 4);
    await flush();
    expect(t.dispatched).toHaveLength(3);
    expect(t.clock.pending()).toBe(0);
  });

  it('saves the cache and calls afterRefresh after a good refresh', async () => {
    const t = setup();
    t.poller.start();
    await flush();
    expect(t.cache.items.has(CACHE_KEY)).toBe(true);
    expect(t.afterRefresh).toHaveBeenCalledTimes(1);
    expect(t.afterRefresh.mock.calls[0]![0]).toMatchObject({ offline: false, overview });
    t.poller.stop();
  });

  it('does not poll in the background, and refreshes at once on foreground', async () => {
    const t = setup({ state: 'background' });
    t.poller.start();
    await flush();
    expect(t.requests()).toBe(0);
    await t.clock.advance(POLL_MS * 3);
    expect(t.requests()).toBe(0);

    t.appState.set('active');
    await flush();
    expect(t.requests()).toBe(4);
    await t.clock.advance(POLL_MS);
    await flush();
    expect(t.requests()).toBe(8);

    t.appState.set('background');
    const at = t.requests();
    await t.clock.advance(POLL_MS * 3);
    await flush();
    expect(t.requests()).toBe(at);
    expect(t.clock.pending()).toBe(0);
    t.poller.stop();
  });

  it('kick refreshes now, shares an in-flight refresh, and restarts the 15 s wait', async () => {
    const t = setup();
    t.poller.start();
    await flush();
    await t.clock.advance(10_000);
    const a = t.poller.kick();
    const b = t.poller.kick();
    await Promise.all([a, b]);
    expect(t.dispatched).toHaveLength(2); // one start-up refresh and one shared kick
    await t.clock.advance(POLL_MS - 1);
    expect(t.dispatched).toHaveLength(2);
    await t.clock.advance(1);
    await flush();
    expect(t.dispatched).toHaveLength(3);
    t.poller.stop();
  });

  it('goes offline when the hub cannot be reached, then recovers', async () => {
    let down = true;
    const t = setup({
      handler: (url) => {
        if (down) return Promise.reject(new Error('Network request failed'));
        if (url.endsWith('/v1/overview')) return response(200, overview);
        if (url.includes('/v1/sessions')) return response(200, { sessions });
        if (url.endsWith('/v1/accounts')) return response(200, { accounts });
        return response(200, { alerts });
      },
    });
    t.poller.start();
    await flush();
    expect(t.dispatched.map((a) => a.type)).toEqual(['offline']);
    expect(t.cache.items.size).toBe(0);
    expect(t.afterRefresh).not.toHaveBeenCalled();
    down = false;
    await t.clock.advance(POLL_MS);
    await flush();
    expect(t.dispatched.map((a) => a.type)).toEqual(['offline', 'loaded']);
    t.poller.stop();
  });

  it('a 5xx on one reply counts as offline, not as a partial load', async () => {
    const t = setup({ handler: (url) => (url.endsWith('/v1/accounts') ? response(500, 'x') : response(200, url.includes('sessions') ? { sessions } : url.includes('alerts') ? { alerts } : overview)) });
    t.poller.start();
    await flush();
    expect(t.dispatched.map((a) => a.type)).toEqual(['offline']);
    t.poller.stop();
  });

  it('a 401 is the client callback, not an offline banner', async () => {
    const t = setup({ handler: () => response(401, { code: 'unauthorized' }) });
    t.poller.start();
    await flush();
    expect(t.dispatched).toEqual([]);
    expect(t.onUnauthorized).toHaveBeenCalledTimes(1);
    t.poller.stop();
  });

  it('a failing cache or afterRefresh does not break the refresh', async () => {
    const t = setup();
    t.cache.setItem = () => Promise.reject(new Error('disk full'));
    t.afterRefresh.mockRejectedValue(new Error('widget'));
    t.poller.start();
    await flush();
    expect(t.dispatched.map((a) => a.type)).toEqual(['loaded']);
    t.poller.stop();
  });

  it('a refresh that finishes after stop() writes nothing (unpair during a request)', async () => {
    let release: (r: ReturnType<typeof response>) => void = () => {};
    const gate = new Promise<ReturnType<typeof response>>((r) => (release = r));
    const t = setup({ handler: (url) => (url.endsWith('/v1/overview') ? (gate as never) : response(200, url.includes('sessions') ? { sessions } : url.includes('alerts') ? { alerts } : { accounts }) ) });
    t.poller.start();
    await flush();
    t.poller.stop();
    release(response(200, overview));
    await flush();
    expect(t.dispatched).toEqual([]);
    expect(t.cache.items.size).toBe(0);
    expect(t.afterRefresh).not.toHaveBeenCalled();
  });

  it('start twice does not double the timers', async () => {
    const t = setup();
    t.poller.start();
    t.poller.start();
    await flush();
    expect(t.requests()).toBe(4);
    expect(t.clock.pending()).toBe(1);
    t.poller.stop();
  });
});
