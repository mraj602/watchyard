// The link (phone.md P6.3, P6.7, P11, P13; push.md 13-14) on the JS side: the
// registration (the prefs PATCH only), the glue driving the wy-link module, the widgets merging
// the link's refresh frames, the words, the maker guides and Settings → Notifications. The
// module is the recording fake of test/fakes.ts (`linkState`, `emitLink`).
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import * as ExpoNotifications from '../modules/wy-notify';
import { Linking } from 'react-native';
import * as WyLink from '../modules/wy-link';
import type { Device, Overview, Seat } from '../src/contract/api';
import type { LinkState } from '../src/contract/link';
import type { AppInfo } from '../src/contract/platform';
import type { WidgetSnapshot } from '../src/contract/snapshot';
import NotifySettingsScreen from '../src/components/screens/NotifySettingsScreen';
import SettingsScreen from '../src/components/screens/SettingsScreen';
import type { HubClient } from '../src/lib/hub';
import { createGlue, type GluePlatform } from '../src/lib/integrate';
import {
  batteryWords,
  knownMaker,
  linkErrorWords,
  linkStatusWords,
  makerGuide,
  OTHER_GUIDE,
  registerForPush,
  spanWords,
  syncRegistration,
  verifiedWords,
  type NotificationsLike,
} from '../src/lib/notify';
import { applyLinkRefresh, buildSnapshot } from '../src/lib/widget';
import { systemLink } from '../src/state/platform';
import { createTaskHandler } from '../widgets/android/task-handler';
import { calls, callsOf, emitLink, linkState, notificationsState } from './fakes';
import accountsFx from './fixtures/hub/accounts.json';
import meFx from './fixtures/hub/me.json';
import overviewFx from './fixtures/hub/overview.json';
import { APP, fakeClient, fx, makeHarness, NOW, routerMock } from './screen-harness';
import { flush, memoryKV } from './hubfake';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);

const N = ExpoNotifications as unknown as NotificationsLike;
const LINK_APP: AppInfo = { ...APP };
const H = 3_600_000;

const state = (over: Partial<LinkState> = {}): LinkState => ({ ...linkState.state, ...over });

// ------------------------------------------------------------------ registration (P6.3)

describe('registration over the link', () => {
  const deps = () => {
    const patched: unknown[] = [];
    return {
      patched,
      deps: {
        notifications: N,
        app: LINK_APP,
        tz: 'Europe/London',
        patchSelf: async (p: unknown) => {
          patched.push(p);
          return {};
        },
        currentPrefs: async () => ({}),
      },
    };
  };

  it('creates the channels, asks for permission and PATCHes the prefs; no target is registered', async () => {
    notificationsState.permission = 'undetermined';
    const d = deps();
    notificationsState.permission = 'granted';
    await expect(registerForPush(d.deps)).resolves.toBe('registered');
    expect(callsOf('notifications.setNotificationChannelAsync').length).toBeGreaterThan(0);
    expect(d.patched).toEqual([{ tz: 'Europe/London', widgets: true }]);
    const order = calls.map((c) => c.fn);
    expect(order.indexOf('notifications.setNotificationChannelAsync')).toBeLessThan(order.indexOf('notifications.getPermissionsAsync'));
  });

  it('is blocked when the permission is refused', async () => {
    notificationsState.permission = 'denied';
    const d = deps();
    await expect(registerForPush(d.deps)).resolves.toBe('blocked');
    expect(d.patched).toEqual([]);
  });

  it('syncRegistration has nothing to re-register', async () => {
    const d = deps();
    await expect(syncRegistration(d.deps)).resolves.toBe('unchanged');
    notificationsState.permission = 'denied';
    await expect(syncRegistration(d.deps)).resolves.toBe('blocked');
    expect(d.patched).toEqual([]);
  });
});

// ------------------------------------------------------------------ the glue

function platform(over: Partial<GluePlatform> = {}): GluePlatform {
  return {
    notifications: N,
    app: LINK_APP,
    clock: { now: () => NOW, setTimeout: () => null, clearTimeout: () => {} },
    local: memoryKV(),
    tz: () => 'UTC',
    updateTimelines: jest.fn(),
    updateAndroidWidgets: jest.fn(async () => {}),
    syncCountdown: jest.fn(async () => {}),
    startCountdown: jest.fn(async () => {}),
    clearIos: jest.fn(async () => {}),
    clearAndroidWidgets: jest.fn(async () => {}),
    link: WyLink,
    ...over,
  };
}

const client = (snoozeMin = 20) =>
  fakeClient({ rules: jest.fn(async () => ({ ...fx.rules, settings: { ...fx.rules.settings, snooze_min: snoozeMin } })) }) as unknown as HubClient;

const configure = () =>
  WyLink.configure({ origin: 'https://hub.example.ts.net', device: 'dev_phone', urls: ['wss://hub.example.ts.net/v1/link'], pingS: 180, hubKey: { pub: 'P', fp: 'f'.repeat(24) }, reg: 'qr' });

describe('the glue with the link', () => {
  it('on pairing: registers, sends snooze_min and starts the link', async () => {
    await configure();
    const p = platform();
    const glue = createGlue(p);
    await glue.onPaired(client(20));
    expect(callsOf('link.setSnoozeMinutes').map((c) => c.args[0])).toEqual([20]);
    expect(callsOf('link.start')).toHaveLength(1);
    expect(glue.view().pushState).toBe('registered');
    expect(glue.link).toBe(WyLink);
  });

  it('on start: starts the link only while instant delivery is on', async () => {
    await configure();
    const glue = createGlue(platform());
    await glue.startUp(client());
    expect(callsOf('link.start')).toHaveLength(0); // the switch is off in the fake's state
    linkState.state = state({ enabled: true });
    const g2 = createGlue(platform());
    await g2.startUp(client());
    expect(callsOf('link.start')).toHaveLength(1);
  });

  it('a pairing without a link (configure never ran) still pairs', async () => {
    const glue = createGlue(platform());
    await expect(glue.onPaired(client())).resolves.toBeUndefined();
    expect(callsOf('link.start')).toHaveLength(1); // tried; the module refused quietly
  });

  it('unpair resets the link: keys, pins and storage', async () => {
    await configure();
    const glue = createGlue(platform());
    await glue.onCleared();
    expect(callsOf('link.reset')).toHaveLength(1);
    expect(linkState.config).toBeNull();
  });

  it('the refresh and revoked events refresh now; detaching stops them', async () => {
    const glue = createGlue(platform());
    const kick = jest.fn();
    glue.setContext({ me: null, origin: 'https://hub.example.ts.net', client: null, kick, reloadMe: jest.fn() });
    const off = glue.attachLink();
    emitLink('refresh');
    emitLink('revoked');
    expect(kick).toHaveBeenCalledTimes(2);
    off();
    emitLink('refresh');
    expect(kick).toHaveBeenCalledTimes(2);
  });

  it('without the module there is no link to drive', async () => {
    const glue = createGlue(platform({ link: null }));
    await glue.onPaired(client());
    expect(callsOf('link.')).toHaveLength(0);
    expect(glue.link).toBeNull();
  });

  it('systemLink: the module only on Android', () => {
    expect(systemLink({ ...APP, platform: 'android' })).toBe(WyLink);
    expect(systemLink({ ...APP, platform: 'ios' })).toBeNull();
    linkState.available = false;
    expect(systemLink({ ...APP, platform: 'android' })).toBeNull();
  });
});

// ------------------------------------------------------------------ widgets (push.md 13.5)

describe('applyLinkRefresh', () => {
  const base = (): WidgetSnapshot =>
    buildSnapshot(overviewFx as unknown as Overview, accountsFx.accounts as unknown as Seat[], NOW, { private: false, origin: 'https://hub.example.ts.net' });

  it('updates needs_you and the shown seats\' windows from a newer frame', () => {
    const snap = base();
    const seat = snap.seats[0]!;
    const resets = new Date(NOW + 2 * H).toISOString();
    const out = applyLinkRefresh(snap, {
      v: 1,
      needs_you: 0,
      seats: [
        { seat: seat.id, window: 'five_hour', used_pct: 91.6, resets_at: resets },
        { seat: 'not_shown', window: 'five_hour', used_pct: 10, resets_at: resets },
      ],
      ts: new Date(NOW + 60_000).toISOString(),
    })!;
    expect(out.needs_you).toBe(0);
    expect(out.top).toBeNull();
    expect(out.seats[0]!.five_hour).toEqual({ pct: 92, resets_at: resets });
    expect(out.seats).toHaveLength(snap.seats.length);
    expect(out.as_of).toBe(new Date(NOW + 60_000).toISOString());
    expect(out.hub).toBe(snap.hub);
  });

  it('ignores an older frame, a bad one, or no snapshot', () => {
    const snap = base();
    const old = { v: 1 as const, needs_you: 9, seats: [], ts: new Date(NOW - 60_000).toISOString() };
    expect(applyLinkRefresh(snap, old)).toBe(snap);
    expect(applyLinkRefresh(snap, { ...old, ts: 'nope' })).toBe(snap);
    expect(applyLinkRefresh(snap, null)).toBe(snap);
    expect(applyLinkRefresh(null, { ...old, ts: new Date(NOW + 1).toISOString() })).toBeNull();
  });

  it('the Android widget handler draws the stored snapshot with the link refresh on top', async () => {
    const store = memoryKV();
    const snap = base();
    await store.setItem('wy.widget.v1', JSON.stringify(snap));
    const linkRefresh = jest.fn(async () => ({ v: 1 as const, needs_you: 7, seats: [], ts: new Date(NOW + 1000).toISOString() }));
    const rendered: unknown[] = [];
    const handler = createTaskHandler({ store, now: () => NOW + 1000, linkRefresh });
    await handler({ widgetInfo: { widgetName: 'WySessions' }, widgetAction: 'WIDGET_UPDATE', renderWidget: (t: unknown) => rendered.push(t) } as never);
    expect(linkRefresh).toHaveBeenCalled();
    expect(JSON.stringify((rendered[0] as { props: unknown }).props)).toContain('7');
  });
});

// ------------------------------------------------------------------ words and makers (P6.7)

describe('link words', () => {
  it('connection states', () => {
    expect(linkStatusWords(state({ status: 'connected', transport: 'ws', connectedSince: NOW - (2 * H + 14 * 60_000) }), NOW)).toBe('Connected · 2 h 14 min · WebSocket');
    expect(linkStatusWords(state({ status: 'connected', transport: 'poll', connectedSince: NOW - 5 * 60_000 }), NOW)).toBe('Connected · 5 min · long-poll');
    expect(linkStatusWords(state({ status: 'backoff', nextRetryAt: NOW + 20_000 }), NOW)).toBe('Reconnecting · next try in 20 s');
    expect(linkStatusWords(state({ status: 'error', lastError: 'UnknownHostException' }), NOW)).toBe('Not connected: hub unreachable');
    expect(linkStatusWords(state({ status: 'revoked' }), NOW)).toBe('Removed from hub');
    expect(linkStatusWords(state({ status: 'off', enabled: false }), NOW)).toBe('Off: checks every 15 minutes');
  });

  it('errors: close codes, HTTP statuses and network classes, never a body', () => {
    expect(linkErrorWords('4429 rate_limited')).toBe('closed by the hub (4429 rate limited)');
    expect(linkErrorWords('http_502')).toBe('the hub answered 502');
    expect(linkErrorWords('SSLHandshakeException')).toBe('TLS certificate refused');
    expect(linkErrorWords('background_start_blocked')).toMatch(/^Android blocked a background start/);
    expect(linkErrorWords('WeirdException')).toBe('Weird');
    expect(linkErrorWords(null)).toBeNull();
  });

  it('verification, battery and spans', () => {
    expect(verifiedWords({ verified: 'qr', checkCode: 'k7q2-m4xa' })).toBe('Verified by QR');
    expect(verifiedWords({ verified: 'tofu', checkCode: 'k7q2-m4xa' })).toBe('Not verified: compare check code k7q2-m4xa with the board');
    expect(verifiedWords({ verified: 'changed', checkCode: null })).toMatch(/^Hub key changed/);
    expect(batteryWords(true)).toBe('Battery optimisation: off (good)');
    expect(batteryWords(false)).toBe('Battery optimisation: on: notifications may be late');
    expect(spanWords(40_000)).toBe('40 s');
    expect(spanWords(3 * H)).toBe('3 h');
    expect(spanWords(72 * H)).toBe('3 d');
  });

  it('maker guides match on the lower-case manufacturer', () => {
    expect(makerGuide('samsung').text).toMatch(/Never sleeping apps/);
    expect(makerGuide('Xiaomi').maker).toBe('xiaomi');
    expect(makerGuide('POCO').maker).toBe('xiaomi');
    expect(makerGuide('OnePlus').text).toMatch(/Don't optimize/);
    expect(makerGuide('HONOR').maker).toBe('huawei');
    expect(makerGuide('Google')).toEqual({ maker: 'other', text: OTHER_GUIDE });
    expect(makerGuide(undefined).maker).toBe('other');
    expect(knownMaker('realme')).toBe(true);
    expect(knownMaker('Google')).toBe(false);
  });
});

// ------------------------------------------------------------------ Settings → Notifications

describe('Settings → Notifications', () => {
  const linkDevice = (over: Record<string, unknown> = {}): Device => ({
    ...(structuredClone(meFx.device) as unknown as Device),
    targets: [{ id: 'pt_link', kind: 'link', enabled: true, status: 'ok', name: 'Pixel · app', ...over }],
  });

  async function show(setup: Parameters<typeof makeHarness>[0] = {}) {
    const h = makeHarness({
      client: fakeClient({ deviceSelf: jest.fn(async () => linkDevice()) }),
      ...setup,
      api: { link: WyLink, app: { ...LINK_APP, maker: 'samsung' }, ...setup.api },
    });
    await render(<NotifySettingsScreen />, { wrapper: h.Wrapper });
    await act(async () => {
      await flush(20);
    });
    return h.probe;
  }

  beforeEach(async () => {
    routerMock.router.push.mockClear();
    await configure();
  });

  it('shows the state: connection, battery, guide, permission, verification, ping', async () => {
    linkState.state = state({ enabled: true, status: 'connected', transport: 'ws', connectedSince: NOW - 2 * H, received: 12, rejected: 1, batteryExempt: false, verified: 'qr' });
    await show();
    expect(await screen.findByText('Connected · 2 h · WebSocket')).toBeTruthy();
    expect(screen.getByTestId('link-enabled').props.value).toBe(true);
    expect(screen.getByText('12')).toBeTruthy();
    expect(screen.getByText('Battery optimisation: on: notifications may be late')).toBeTruthy();
    expect(screen.getByText(makerGuide('samsung').text)).toBeTruthy();
    expect(screen.getByText('Allowed')).toBeTruthy();
    expect(screen.getByText('Verified by QR')).toBeTruthy();
    expect(screen.getByText('Ping interval: every 3 min')).toBeTruthy();
    expect(screen.getByTestId('link-ping-180').props.accessibilityState.selected).toBe(true);
  });

  it('the switch starts and stops the link, and re-enables a disabled target', async () => {
    const client = fakeClient({ deviceSelf: jest.fn(async () => linkDevice({ enabled: false })) });
    const p = await show({ client });
    await waitFor(() => expect(screen.getByTestId('link-enabled').props.disabled).toBe(false));
    await fireEvent(screen.getByTestId('link-enabled'), 'valueChange', true);
    await waitFor(() => expect(callsOf('link.start')).toHaveLength(1));
    await waitFor(() => expect(p.client.patchTarget).toHaveBeenCalledWith('pt_link', { enabled: true }));
    await waitFor(() => expect(screen.getByTestId('link-enabled').props.value).toBe(true));
    await fireEvent(screen.getByTestId('link-enabled'), 'valueChange', false);
    await waitFor(() => expect(callsOf('link.stop')).toHaveLength(1));
    expect(p.client.patchTarget).toHaveBeenCalledTimes(1); // off keeps the target enabled
  });

  it('state events redraw the screen; Reconnect now', async () => {
    linkState.state = state({ enabled: true, status: 'connecting' });
    await show();
    await screen.findByText('Connecting…');
    await act(async () => emitLink('state', { status: 'backoff', nextRetryAt: NOW + 20_000, lastError: '4429 rate_limited' }));
    expect(await screen.findByText('Reconnecting · next try in 20 s')).toBeTruthy();
    expect(screen.getByText('Last error: closed by the hub (4429 rate limited)')).toBeTruthy();
    await fireEvent.press(screen.getByTestId('link-reconnect'));
    expect(callsOf('link.reconnect')).toHaveLength(1);
  });

  it('Fix asks for the exemption; Open settings opens the maker screen', async () => {
    await show();
    await fireEvent.press(await screen.findByTestId('link-battery-fix'));
    await waitFor(() => expect(callsOf('link.requestBatteryExemption')).toHaveLength(1));
    expect(await screen.findByText('Battery optimisation: off (good)')).toBeTruthy();
    expect(screen.queryByTestId('link-battery-fix')).toBeNull();
    // samsung is in the table: the guide stays
    await fireEvent.press(screen.getByTestId('link-battery-settings'));
    expect(callsOf('link.openBatterySettings')).toHaveLength(1);
  });

  it('hides the guide for an unknown maker once exempt', async () => {
    linkState.state = state({ batteryExempt: true });
    await show({ api: { app: { ...LINK_APP, maker: 'Google' } } });
    await screen.findByText('Battery optimisation: off (good)');
    expect(screen.queryByTestId('link-battery-settings')).toBeNull();
  });

  it('typed-code pairing shows the check code; a changed hub key says so', async () => {
    linkState.state = state({ verified: 'tofu', checkCode: 'k7q2-m4xa' });
    await show();
    expect(await screen.findByText('Not verified: compare check code k7q2-m4xa with the board')).toBeTruthy();
    await act(async () => emitLink('state', { verified: 'changed' }));
    expect(await screen.findByText('Hub key changed: pair this phone again')).toBeTruthy();
  });

  it('ping chips set the interval', async () => {
    await show();
    await fireEvent.press(await screen.findByTestId('link-ping-600'));
    await waitFor(() => expect(callsOf('link.setPingInterval').map((c) => c.args[0])).toEqual([600]));
    expect(await screen.findByText('Ping interval: every 10 min')).toBeTruthy();
  });

  it('Send test notification posts to the link target; a long press is local', async () => {
    const p = await show();
    await waitFor(() => expect(screen.getByTestId('link-test').props.accessibilityState.disabled).toBe(false));
    await fireEvent.press(screen.getByTestId('link-test'));
    expect(p.client.testTarget).toHaveBeenCalledWith('pt_link');
    expect(await screen.findByText('Sent. It should arrive in a moment.')).toBeTruthy();
    await fireEvent(screen.getByTestId('link-test'), 'longPress');
    await waitFor(() => expect(callsOf('link.sendLocalTest')).toHaveLength(1));
    expect(p.client.testTarget).toHaveBeenCalledTimes(1);
  });

  it('blocked notifications offer the system settings; the Web Push backup opens the web app', async () => {
    linkState.state = state({ notificationsAllowed: false });
    const openSettings = jest.spyOn(Linking, 'openSettings').mockResolvedValue(undefined);
    const openURL = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    await show();
    expect(await screen.findByText('Blocked in system settings')).toBeTruthy();
    await fireEvent.press(screen.getByTestId('link-perm-settings'));
    expect(openSettings).toHaveBeenCalled();
    await fireEvent.press(screen.getByTestId('link-webpush'));
    expect(openURL).toHaveBeenCalledWith('https://hub.example.ts.net/#/pair');
    openSettings.mockRestore();
    openURL.mockRestore();
  });

  it('a system screen that will not open is said, not an unhandled rejection', async () => {
    linkState.state = state({ notificationsAllowed: false });
    const openSettings = jest.spyOn(Linking, 'openSettings').mockRejectedValue(new Error('No Activity found'));
    const openURL = jest.spyOn(Linking, 'openURL').mockRejectedValue(new Error('No Activity found'));
    await show();
    await fireEvent.press(await screen.findByTestId('link-perm-settings'));
    expect(await screen.findByText('Could not open settings: No Activity found')).toBeTruthy();
    await fireEvent.press(screen.getByTestId('link-webpush'));
    expect(await screen.findByText('Could not open the web app: No Activity found')).toBeTruthy();
    openSettings.mockRestore();
    openURL.mockRestore();
  });

  it('Categories: one switch per category, on unless muted; a switch writes mute_categories (merged with the other prefs)', async () => {
    const dev = { ...linkDevice(), prefs: { tap: 'claude', mute_categories: ['usage'] } } as unknown as Device;
    const client = fakeClient({ deviceSelf: jest.fn(async () => dev) });
    await show({ client });
    expect(await screen.findByText('Categories')).toBeTruthy();
    expect(screen.getByText('Off: your hub does not send them to this phone. Sound and importance: your phone\'s settings for each channel.')).toBeTruthy();
    for (const id of ['sessions', 'limits', 'switch', 'drain', 'machines']) expect(screen.getByTestId(`cat-${id}`).props.value).toBe(true);
    expect(screen.getByTestId('cat-usage').props.value).toBe(false);
    await fireEvent(screen.getByTestId('cat-drain'), 'valueChange', false);
    expect(client.patchSelf).toHaveBeenCalledWith({ tap: 'claude', mute_categories: ['drain', 'usage'] });
    await waitFor(() => expect(screen.getByTestId('cat-drain').props.value).toBe(false));
    await fireEvent(screen.getByTestId('cat-usage'), 'valueChange', true);
    expect(client.patchSelf).toHaveBeenLastCalledWith({ tap: 'claude', mute_categories: ['drain'] });
  });

  it('Categories: a refused save puts the switch back and says why', async () => {
    const client = fakeClient({ deviceSelf: jest.fn(async () => linkDevice()), patchSelf: jest.fn(async () => { throw new Error('nope'); }) });
    await show({ client });
    await fireEvent(await screen.findByTestId('cat-switch'), 'valueChange', false);
    expect(await screen.findByText('nope')).toBeTruthy();
    expect(screen.getByTestId('cat-switch').props.value).toBe(true);
  });

  it('Categories: Sound… opens the system settings of that category\'s channel', async () => {
    await show();
    await fireEvent.press(await screen.findByTestId('cat-sound-switch'));
    await fireEvent.press(screen.getByTestId('cat-sound-sessions'));
    expect(callsOf('notifications.openChannelSettingsAsync').map((c) => c.args[0])).toEqual(['wy_switch', 'wy_needs_you']);
  });

  it('a phone without a link target is told to pair again', async () => {
    const client = fakeClient({ deviceSelf: jest.fn(async () => ({ ...linkDevice(), targets: [] })) });
    await show({ client });
    expect(await screen.findByTestId('link-no-target')).toBeTruthy();
    expect(screen.getByTestId('link-test').props.accessibilityState.disabled).toBe(true);
  });

  it('without the module it explains this build', async () => {
    const h = makeHarness({ api: { link: undefined } });
    await render(<NotifySettingsScreen />, { wrapper: h.Wrapper });
    expect(screen.getByText(/Instant delivery is part of the Android app/)).toBeTruthy();
  });

  it('Settings links to it, and offers no test push of its own', async () => {
    const h = makeHarness({ api: { link: WyLink, app: LINK_APP } });
    await render(<SettingsScreen />, { wrapper: h.Wrapper });
    await act(async () => {
      await flush(10);
    });
    expect(screen.queryByTestId('test-push')).toBeNull();
    await fireEvent.press(screen.getByTestId('notify-settings'));
    expect(routerMock.router.push).toHaveBeenCalledWith('/more/settings/notifications');
  });
});
