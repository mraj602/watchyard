import * as ExpoNotifications from '../modules/wy-notify';
import type { AppInfo, Clock, FetchLike, RequestInitLike } from '../src/contract/platform';
import { ANDROID_CHANNELS, NOTIFY_CATEGORIES } from '../src/contract/constants';
import {
  actOnToken,
  decideResponse,
  initNotifications,
  performDecision,
  registerForPush,
  registrationPrefs,
  setupChannelsAndCategories,
  syncRegistration,
  type Decision,
  type NotificationResponseLike,
  type NotificationsLike,
} from '../src/lib/notify';
import { calls, callsOf, emitNotificationReceived, emitNotificationResponse, notificationsState } from './fakes';

const N = ExpoNotifications as unknown as NotificationsLike;
const DEFAULT = 'expo.modules.notifications.actions.DEFAULT';
const SNOOZE_TOKEN = 'a1.Zm9v.YmFy';
const SEEN_TOKEN = 'a1.c2Vlbg.c2ln';

const app = (over: Partial<AppInfo> = {}): AppInfo => ({
  version: '1.2.3',
  build: '45',
  model: 'Pixel 8',
  platform: 'android',
  dev: false,
  ...over,
});

function payload(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    v: 1,
    type: 'alert',
    alert: 'al_1',
    title: 'work · api · asked a question',
    body: 'Should I also update the migration?',
    tag: 'al_1',
    url: '/#/sessions/s_1?alert=al_1',
    remote_url: 'https://claude.ai/code/session_abc',
    tap: 'auto',
    needs_you: 2,
    actions: { snooze: SNOOZE_TOKEN, seen: SEEN_TOKEN },
    private: false,
    ts: '2026-10-13T10:00:00Z',
    ...over,
  };
}

const resp = (action: string, data: unknown, id = 'n1'): NotificationResponseLike => ({
  actionIdentifier: action,
  notification: { request: { identifier: id, content: { data } } },
});

const clock: Clock = {
  now: () => Date.now(),
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

interface Sent {
  url: string;
  init?: RequestInitLike;
}
function fakeFetch(status = 200, body: unknown = { applied: true }): { fetch: FetchLike; sent: Sent[] } {
  const sent: Sent[] = [];
  const fetch: FetchLike = async (url, init) => {
    sent.push({ url, init });
    return {
      status,
      ok: status >= 200 && status < 300,
      headers: { get: () => null },
      text: async () => JSON.stringify(body),
      json: async () => body,
    };
  };
  return { fetch, sent };
}

describe('channels: JS and Kotlin lists', () => {
  it('Channels.kt has the same ids, names and importances as ANDROID_CHANNELS (plus the connection channel)', () => {
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const fs = require('fs') as typeof import('fs');
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const path = require('path') as typeof import('path');
    const kt = fs.readFileSync(path.join(__dirname, '../modules/wy-link/android/src/main/java/dev/watchyard/link/Channels.kt'), 'utf8');
    const consts = Object.fromEntries([...kt.matchAll(/const val (\w+) = "([^"]+)"/g)].map((m) => [m[1]!, m[2]!]));
    const rows = [...kt.matchAll(/Triple\((\w+), "([^"]+)", (\d)\)/g)].map((m) => ({ id: consts[m[1]!], name: m[2]!, importance: Number(m[3]) }));
    const imp = { HIGH: 4, DEFAULT: 3, LOW: 2 } as const;
    expect(rows.filter((r) => r.id !== 'wy_connection')).toEqual(ANDROID_CHANNELS.map((c) => ({ id: c.id, name: c.name, importance: imp[c.importance] })));
    expect(rows.at(-1)?.id).toBe('wy_connection');
  });
  it('every category names a channel that exists', () => {
    const ids = ANDROID_CHANNELS.map((c) => c.id);
    NOTIFY_CATEGORIES.forEach((c) => expect(ids).toContain(c.channel));
    expect(NOTIFY_CATEGORIES.map((c) => c.id)).toEqual(['sessions', 'limits', 'switch', 'drain', 'usage', 'machines']);
  });
});

describe('channels and categories', () => {
  it('creates the seven Android channels with the mapped importance, and no channels on iOS', async () => {
    await setupChannelsAndCategories(N, 'android', 15);
    const ch = callsOf('notifications.setNotificationChannelAsync');
    expect(ch.map((c) => c.args[0])).toEqual(['wy_needs_you', 'wy_limits', 'wy_switch', 'wy_drain', 'wy_usage', 'wy_machines', 'wy_test']);
    const imp = ExpoNotifications.AndroidImportance;
    expect(ch.map((c) => (c.args[1] as { importance: number }).importance)).toEqual([imp.HIGH, imp.DEFAULT, imp.LOW, imp.DEFAULT, imp.LOW, imp.LOW, imp.DEFAULT]);
    expect(ch.map((c) => (c.args[1] as { name: string }).name)).toEqual(['Sessions', 'Limits', 'Account switch', 'Drain', 'Usage notices', 'Machines', 'Test']);

    calls.length = 0;
    await setupChannelsAndCategories(N, 'ios', 15);
    expect(callsOf('notifications.setNotificationChannelAsync')).toHaveLength(0);
  });

  it('has one category, wy_alert, without - or : and three actions with the P6.2 options', async () => {
    await setupChannelsAndCategories(N, 'ios', 30);
    const cats = callsOf('notifications.setNotificationCategoryAsync');
    expect(cats).toHaveLength(1);
    const [id, actions] = cats[0]!.args as [string, { identifier: string; buttonTitle: string; options: unknown }[]];
    expect(id).toBe('wy_alert');
    expect(id).not.toMatch(/[-:]/);
    expect(actions).toEqual([
      { identifier: 'open', buttonTitle: 'Open', options: { opensAppToForeground: true } },
      { identifier: 'snooze', buttonTitle: 'Snooze 30 min', options: { opensAppToForeground: false } },
      { identifier: 'seen', buttonTitle: 'Seen', options: { opensAppToForeground: false } },
    ]);
    actions.forEach((a) => expect(a.identifier).not.toMatch(/[-:]/));
  });
});

describe('registerForPush', () => {
  function deps(over: Partial<Parameters<typeof registerForPush>[0]> = {}) {
    const patched: unknown[] = [];
    return {
      patched,
      deps: {
        notifications: N,
        app: app(),
        tz: 'Europe/London',
        patchSelf: async (p: unknown) => {
          patched.push(p);
          return {};
        },
        ...over,
      },
    };
  }

  it('creates channels before the permission request, then PATCHes the prefs (no target is registered)', async () => {
    const d = deps({ currentPrefs: async () => ({}) }); // a new device: prefs.widgets never set
    await expect(registerForPush(d.deps)).resolves.toBe('registered');
    const order = calls.map((c) => c.fn);
    const lastChannel = order.lastIndexOf('notifications.setNotificationChannelAsync');
    const perm = order.findIndex((f) => f === 'notifications.getPermissionsAsync' || f === 'notifications.requestPermissionsAsync');
    expect(lastChannel).toBeGreaterThanOrEqual(0);
    expect(perm).toBeGreaterThan(lastChannel);
    expect(d.patched).toEqual([{ tz: 'Europe/London', widgets: true }]);
  });

  it('sends widgets: true only when the device never had prefs.widgets', async () => {
    for (const [currentPrefs, want] of [
      [async () => ({ widgets: false }), { tz: 'Europe/London' }],
      [async () => ({ widgets: true }), { tz: 'Europe/London' }],
      [
        async () => {
          throw new Error('offline');
        },
        { tz: 'Europe/London' },
      ],
      [undefined, { tz: 'Europe/London' }],
      [async () => ({ private: true }), { tz: 'Europe/London', widgets: true }],
    ] as const) {
      const d = deps(currentPrefs ? { currentPrefs } : {});
      await expect(registerForPush(d.deps)).resolves.toBe('registered');
      expect(d.patched).toEqual([want]);
    }
    expect(registrationPrefs('UTC', undefined)).toEqual({ tz: 'UTC' });
  });

  it('asks for permission only when not granted, with the iOS options', async () => {
    notificationsState.permission = 'denied';
    const d = deps({ app: app({ platform: 'ios' }) });
    await expect(registerForPush(d.deps)).resolves.toBe('blocked');
    expect(callsOf('notifications.requestPermissionsAsync')[0]!.args).toEqual([
      { ios: { allowAlert: true, allowBadge: true, allowSound: true } },
    ]);
    expect(d.patched).toHaveLength(0);
  });

  it('reports an error state, without the error text, when the hub call fails', async () => {
    const d = deps({
      patchSelf: async () => {
        throw new Error('boom SECRET');
      },
    });
    await expect(registerForPush(d.deps)).resolves.toBe('error');
  });
});

describe('syncRegistration', () => {
  const deps = () => ({ notifications: N, app: app(), tz: 'UTC', patchSelf: async () => ({}) });

  it("has nothing to re-register: the link target is the hub's", async () => {
    await expect(syncRegistration(deps())).resolves.toBe('unchanged');
    expect(callsOf('notifications.requestPermissionsAsync')).toHaveLength(0);
  });

  it('never asks for permission, and does nothing when it is not granted', async () => {
    notificationsState.permission = 'denied';
    await expect(syncRegistration(deps())).resolves.toBe('blocked');
    expect(callsOf('notifications.requestPermissionsAsync')).toHaveLength(0);
  });
});

describe('decideResponse', () => {
  const route = (r: object): Decision => ({ kind: 'route', route: r as never });
  const link: Decision = { kind: 'open-link', url: 'https://claude.ai/code/session_abc' };
  const sessionRoute = route({ name: 'session', id: 's_1', alert: 'al_1' });

  it('snooze and seen act with the token of that button', () => {
    expect(decideResponse(resp('snooze', payload()), null)).toEqual({ kind: 'act', token: SNOOZE_TOKEN, action: 'snooze' });
    expect(decideResponse(resp('seen', payload()), true)).toEqual({ kind: 'act', token: SEEN_TOKEN, action: 'seen' });
  });

  it('a button without a usable token does nothing', () => {
    expect(decideResponse(resp('snooze', payload({ actions: { seen: SEEN_TOKEN } })), null)).toEqual({ kind: 'none' });
    expect(decideResponse(resp('seen', payload({ actions: { seen: 'not a token' } })), null)).toEqual({ kind: 'none' });
    expect(decideResponse(resp('seen', payload({ actions: { seen: 'a1.x/y.z' } })), null)).toEqual({ kind: 'none' });
    expect(decideResponse(resp('snooze', payload({ actions: undefined })), null)).toEqual({ kind: 'none' });
    expect(decideResponse(resp('seen', undefined), null)).toEqual({ kind: 'none' });
  });

  it('tap = auto with a remote link asks for the probe; its answer picks link or route', () => {
    for (const id of [DEFAULT, 'open']) {
      expect(decideResponse(resp(id, payload()), null)).toEqual({
        kind: 'probe',
        then: { reachable: sessionRoute, unreachable: link },
      });
      expect(decideResponse(resp(id, payload()), true)).toEqual(sessionRoute);
      expect(decideResponse(resp(id, payload()), false)).toEqual(link);
    }
  });

  it('tap = auto without a remote link never probes', () => {
    const p = payload({ remote_url: undefined });
    expect(decideResponse(resp(DEFAULT, p), null)).toEqual(sessionRoute);
    expect(decideResponse(resp(DEFAULT, p), false)).toEqual(sessionRoute);
  });

  it('tap = claude opens the link; without one it opens the route', () => {
    expect(decideResponse(resp(DEFAULT, payload({ tap: 'claude' })), null)).toEqual(link);
    expect(decideResponse(resp(DEFAULT, payload({ tap: 'claude', remote_url: undefined })), null)).toEqual(sessionRoute);
  });

  it('tap = session always opens the route', () => {
    expect(decideResponse(resp(DEFAULT, payload({ tap: 'session' })), false)).toEqual(sessionRoute);
  });

  it('a private payload (no remote_url) goes to the alert lookup route', () => {
    const p = payload({ private: true, remote_url: undefined, url: '/#/alerts/al_1' });
    expect(decideResponse(resp(DEFAULT, p), null)).toEqual(route({ name: 'alert', id: 'al_1' }));
  });

  it('only https remote links are ever opened', () => {
    for (const remote of ['http://claude.ai/x', 'javascript:alert(1)', 'https://u:p@evil.example/', 'https://a b/', 'file:///etc/passwd', 42]) {
      expect(decideResponse(resp(DEFAULT, payload({ tap: 'claude', remote_url: remote })), null)).toEqual(sessionRoute);
    }
  });

  it('malformed or foreign data opens Now', () => {
    const now = route({ name: 'now' });
    for (const data of [undefined, null, 'garbage', 42, [], {}, { v: 2 }, { v: '1' }, JSON.stringify([1])]) {
      expect(decideResponse(resp(DEFAULT, data), null)).toEqual(now);
    }
    expect(decideResponse(resp('unknown_action', payload()), null)).toEqual(now);
  });

  it('a hostile url falls back to Now; a JSON string payload is read', () => {
    const now = route({ name: 'now' });
    for (const url of ['//evil', '/\\evil', 'javascript:x', 5, undefined, '/#/sessions/../x']) {
      expect(decideResponse(resp(DEFAULT, payload({ url, remote_url: undefined })), null)).toEqual(now);
    }
    expect(decideResponse(resp(DEFAULT, JSON.stringify(payload())), true)).toEqual(sessionRoute);
  });

  it('data of a refresh push opens Now (it has no version-1 alert fields to act on)', () => {
    expect(decideResponse(resp(DEFAULT, { v: 1, type: 'refresh', needs_you: 1, ts: 'x' }), null)).toEqual(route({ name: 'now' }));
  });
});

describe('the act call', () => {
  it('POSTs {} to /v1/actions/<token> with no Authorization', async () => {
    const f = fakeFetch();
    const r = await actOnToken({ fetch: f.fetch, clock }, 'https://hub.ts.net/', SNOOZE_TOKEN);
    expect(r).toEqual({ ok: true, status: 200, applied: true });
    expect(f.sent).toHaveLength(1);
    const s = f.sent[0]!;
    expect(s.url).toBe(`https://hub.ts.net/v1/actions/${SNOOZE_TOKEN}`);
    expect(s.init!.method).toBe('POST');
    expect(s.init!.body).toBe('{}');
    expect(s.init!.headers).toEqual({ 'Content-Type': 'application/json' });
    expect(Object.keys(s.init!.headers!).map((k) => k.toLowerCase())).not.toContain('authorization');
  });

  it('is not retried, never throws and never returns the token', async () => {
    const down: FetchLike = async () => {
      throw new Error(`connect ECONNREFUSED ${SNOOZE_TOKEN}`);
    };
    const r = await actOnToken({ fetch: down, clock }, 'https://hub.ts.net', SNOOZE_TOKEN);
    expect(r).toEqual({ ok: false, status: null });
    expect(JSON.stringify(r)).not.toContain(SNOOZE_TOKEN);
    const f = fakeFetch(410, { error: 'gone' });
    expect(await actOnToken({ fetch: f.fetch, clock }, 'https://hub.ts.net', SNOOZE_TOKEN)).toEqual({ ok: false, status: 410 });
    expect(f.sent).toHaveLength(1);
  });

  it('refuses something that is not an action token without sending', async () => {
    const f = fakeFetch();
    await actOnToken({ fetch: f.fetch, clock }, 'https://hub.ts.net', '../v1/devices/self');
    expect(f.sent).toHaveLength(0);
  });
});

describe('initNotifications and performDecision', () => {
  function setup(opts: { reachable?: boolean } = {}) {
    const f = fakeFetch();
    const navigated: unknown[] = [];
    const opened: string[] = [];
    const acted: string[] = [];
    const ran: Decision[] = [];
    const perform = {
      notifications: N,
      fetch: f.fetch,
      clock,
      origin: async () => 'https://hub.ts.net' as string | null,
      openUrl: async (u: string) => void opened.push(u),
      navigate: (r: unknown) => void navigated.push(r),
      onActed: (a: string) => void acted.push(a),
    };
    let received = 0;
    const dispose = initNotifications({
      notifications: N,
      app: app(),
      probe: async () => opts.reachable ?? true,
      onReceived: () => void received++,
      run: async (d, r) => {
        ran.push(d);
        await performDecision(perform, d, r.notification.request.identifier);
      },
    });
    return { f, navigated, opened, acted, ran, dispose, perform, received: () => received };
  }
  const flush = () => new Promise((r) => setImmediate(r));

  it('shows foreground pushes (banner, list, badge) but not silent refreshes, and refreshes', async () => {
    const s = setup();
    const h = notificationsState.handler as { handleNotification(n: unknown): Promise<Record<string, boolean>> };
    const alert = await h.handleNotification({ request: { content: { data: payload() } } });
    expect(alert).toMatchObject({ shouldShowBanner: true, shouldShowList: true, shouldSetBadge: true });
    const refresh = await h.handleNotification({ request: { content: { data: { v: 1, type: 'refresh' } } } });
    expect(refresh.shouldShowBanner).toBe(false);
    emitNotificationReceived({ request: { content: { data: payload() } } });
    expect(s.received()).toBe(1);
    s.dispose();
  });

  it('a Snooze button acts, then dismisses that notification', async () => {
    const s = setup();
    emitNotificationResponse(resp('snooze', payload(), 'notif-7'));
    await flush();
    expect(s.f.sent.map((x) => x.url)).toEqual([`https://hub.ts.net/v1/actions/${SNOOZE_TOKEN}`]);
    expect(callsOf('notifications.dismissNotificationAsync').map((c) => c.args[0])).toEqual(['notif-7']);
    expect(s.acted).toEqual(['snooze']);
    s.dispose();
  });

  it('a tap with auto and an unreachable hub opens the link; reachable opens the route', async () => {
    const down = setup({ reachable: false });
    emitNotificationResponse(resp(DEFAULT, payload(), 'a'));
    await flush();
    expect(down.opened).toEqual(['https://claude.ai/code/session_abc']);
    expect(down.navigated).toEqual([]);
    down.dispose();

    const up = setup({ reachable: true });
    emitNotificationResponse(resp(DEFAULT, payload(), 'b'));
    await flush();
    expect(up.navigated).toEqual([{ name: 'session', id: 's_1', alert: 'al_1' }]);
    expect(up.opened).toEqual([]);
    up.dispose();
  });

  it('a cold-start response is handled exactly once, even if the listener also delivers it', async () => {
    const r = resp('seen', payload(), 'cold-1');
    notificationsState.lastResponse = r;
    const s = setup();
    emitNotificationResponse(r);
    await flush();
    await flush();
    expect(s.f.sent).toHaveLength(1);
    expect(s.ran).toHaveLength(1);
    expect(callsOf('notifications.clearLastNotificationResponseAsync')).toHaveLength(1);
    // a later response of the same notification and action is the same one
    emitNotificationResponse(r);
    await flush();
    expect(s.f.sent).toHaveLength(1);
    s.dispose();
  });

  it('a different notification afterwards is handled', async () => {
    notificationsState.lastResponse = resp('seen', payload(), 'cold-1');
    const s = setup();
    await flush();
    emitNotificationResponse(resp('seen', payload(), 'later'));
    await flush();
    expect(s.f.sent).toHaveLength(2);
    s.dispose();
  });

  it('does nothing when unpaired, and an unreachable hub does not throw', async () => {
    const s = setup();
    s.perform.origin = async () => null;
    emitNotificationResponse(resp('seen', payload()));
    await flush();
    expect(s.f.sent).toHaveLength(0);
    s.dispose();
  });

  it('the disposer removes the listeners', async () => {
    const s = setup();
    s.dispose();
    emitNotificationResponse(resp('seen', payload()));
    await flush();
    expect(s.ran).toHaveLength(0);
  });
});
