// Recording fakes of the native modules (phone.md P1). test/setup.ts installs them with
// jest.mock; tests import them from here to script answers and read the recorded calls.
// No fake touches the network or the real keychain.
import * as React from 'react';

export interface Call {
  fn: string;
  args: unknown[];
}

/** A list of calls, shared by every fake so tests can check order across modules. */
export const calls: Call[] = [];

function rec(fn: string, args: unknown[]): void {
  calls.push({ fn, args });
}

/** Calls whose name starts with prefix ("notifications." for one module). */
export function callsOf(prefix: string): Call[] {
  return calls.filter((c) => c.fn.startsWith(prefix));
}

/**
 * A module of host components: any capitalised export renders as a host element named
 * `${ns}.${Name}` with its props, so a test can walk the tree. Lower-case names are recorded
 * functions returning a description (for modifier helpers).
 */
function hostModule(ns: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  const cache = new Map<string, unknown>();
  return new Proxy(extra, {
    get(target, prop) {
      if (typeof prop !== 'string') return undefined;
      if (prop in target) return target[prop];
      if (prop === '__esModule') return true;
      if (prop === 'then' || prop === 'default') return undefined;
      if (!cache.has(prop)) {
        if (/^[A-Z]/.test(prop)) {
          const type = `${ns}.${prop}`;
          const C = (props: Record<string, unknown>) => React.createElement(type, props);
          C.displayName = type;
          cache.set(prop, C);
        } else {
          cache.set(prop, (...args: unknown[]) => {
            rec(`${ns}.${prop}`, args);
            return { modifier: prop, args };
          });
        }
      }
      return cache.get(prop);
    },
  });
}

// ---------------------------------------------------------------- wy-notify (modules/wy-notify)
// The module's calls plus the response listeners src/lib/notify can take (the real module has
// none on Android: wy-link's notifications carry their own tap intents).

type Listener = (x: unknown) => void;

export const notificationsState = {
  permission: 'granted' as 'granted' | 'denied' | 'undetermined',
  channels: new Map<string, unknown>(),
  categories: new Map<string, unknown>(),
  badge: 0,
  lastResponse: null as unknown,
  responseListeners: [] as Listener[],
  receivedListeners: [] as Listener[],
  handler: null as unknown,
  presented: [] as { request: { identifier: string; content: { data?: unknown } } }[],
};

export function emitNotificationResponse(response: unknown): void {
  notificationsState.responseListeners.forEach((l) => l(response));
}

export function emitNotificationReceived(notification: unknown): void {
  notificationsState.receivedListeners.forEach((l) => l(notification));
}

const perm = () => ({
  granted: notificationsState.permission === 'granted',
  status: notificationsState.permission,
  canAskAgain: true,
  expires: 'never',
});

export const notifications = {
  __esModule: true,
  available: true,
  AndroidImportance: { NONE: 0, MIN: 1, LOW: 2, DEFAULT: 3, HIGH: 4, MAX: 5 },
  setNotificationHandler(h: unknown) {
    rec('notifications.setNotificationHandler', [h]);
    notificationsState.handler = h;
  },
  async setNotificationChannelAsync(id: string, ch: unknown) {
    rec('notifications.setNotificationChannelAsync', [id, ch]);
    notificationsState.channels.set(id, ch);
    return ch;
  },
  async openChannelSettingsAsync(id: string) {
    rec('notifications.openChannelSettingsAsync', [id]);
    return true;
  },
  async setNotificationCategoryAsync(id: string, actions: unknown, options?: unknown) {
    rec('notifications.setNotificationCategoryAsync', [id, actions, options]);
    notificationsState.categories.set(id, actions);
    return { identifier: id, actions };
  },
  async getPermissionsAsync() {
    rec('notifications.getPermissionsAsync', []);
    return perm();
  },
  async requestPermissionsAsync(req?: unknown) {
    rec('notifications.requestPermissionsAsync', [req]);
    return perm();
  },
  async setBadgeCountAsync(n: number) {
    rec('notifications.setBadgeCountAsync', [n]);
    notificationsState.badge = n;
    return true;
  },
  async getBadgeCountAsync() {
    return notificationsState.badge;
  },
  async dismissAllNotificationsAsync() {
    rec('notifications.dismissAllNotificationsAsync', []);
    notificationsState.presented = [];
  },
  async dismissNotificationAsync(id: string) {
    rec('notifications.dismissNotificationAsync', [id]);
    notificationsState.presented = notificationsState.presented.filter((n) => n.request.identifier !== id);
  },
  async getPresentedNotificationsAsync() {
    return notificationsState.presented;
  },
  async getLastNotificationResponseAsync() {
    rec('notifications.getLastNotificationResponseAsync', []);
    return notificationsState.lastResponse;
  },
  async clearLastNotificationResponseAsync() {
    rec('notifications.clearLastNotificationResponseAsync', []);
    notificationsState.lastResponse = null;
  },
  addNotificationResponseReceivedListener(l: Listener) {
    rec('notifications.addNotificationResponseReceivedListener', []);
    notificationsState.responseListeners.push(l);
    return { remove: () => (notificationsState.responseListeners = notificationsState.responseListeners.filter((x) => x !== l)) };
  },
  addNotificationReceivedListener(l: Listener) {
    rec('notifications.addNotificationReceivedListener', []);
    notificationsState.receivedListeners.push(l);
    return { remove: () => (notificationsState.receivedListeners = notificationsState.receivedListeners.filter((x) => x !== l)) };
  },
};

// ---------------------------------------------------------------- expo-secure-store

export const secureState = { items: new Map<string, string>(), locked: false };

export const secureStore = {
  __esModule: true,
  AFTER_FIRST_UNLOCK: 0,
  AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY: 1,
  ALWAYS: 2,
  WHEN_PASSCODE_SET_THIS_DEVICE_ONLY: 3,
  ALWAYS_THIS_DEVICE_ONLY: 4,
  WHEN_UNLOCKED: 5,
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 6,
  async getItemAsync(key: string, options?: unknown) {
    rec('secure.getItemAsync', [key, options]);
    if (secureState.locked) throw new Error('User interaction is not allowed.');
    return secureState.items.get(key) ?? null;
  },
  async setItemAsync(key: string, value: string, options?: unknown) {
    rec('secure.setItemAsync', [key, '<value>', options]); // never record the value
    if (secureState.locked) throw new Error('User interaction is not allowed.');
    secureState.items.set(key, value);
  },
  async deleteItemAsync(key: string, options?: unknown) {
    rec('secure.deleteItemAsync', [key, options]);
    secureState.items.delete(key);
  },
  async isAvailableAsync() {
    return true;
  },
};

// ---------------------------------------------------------------- wy-scan (modules/wy-scan)

/**
 * `granted`/`canAskAgain`: the camera permission. `results`: what the next scans return, in
 * order (a scan with none queued is "cancelled", as when the person presses Back).
 */
export const scanState = {
  granted: true,
  canAskAgain: true,
  results: [] as ({ status: 'scanned'; text: string } | { status: 'cancelled' } | { status: 'unavailable' })[],
};

export const wyScan = {
  __esModule: true,
  available: true,
  async cameraPermission() {
    rec('scan.cameraPermission', []);
    return { granted: scanState.granted, canAskAgain: scanState.canAskAgain };
  },
  async scanQr(prompt: string) {
    rec('scan.scanQr', [prompt]);
    if (!scanState.granted) return { status: 'denied', canAskAgain: scanState.canAskAgain };
    return scanState.results.shift() ?? { status: 'cancelled' };
  },
};

// ---------------------------------------------------------------- expo-widgets

export interface FakeLiveActivity {
  id: string;
  props: unknown;
  url?: string;
  ended: unknown;
}

export const widgetsState = {
  widgets: new Map<string, { component: unknown; timeline: unknown[]; snapshot: unknown }>(),
  liveComponents: new Map<string, unknown>(),
  activities: [] as FakeLiveActivity[],
};

function fakeActivity(a: FakeLiveActivity) {
  return {
    getId: () => a.id,
    async update(props: unknown) {
      rec('widgets.activity.update', [a.id, props]);
      a.props = props;
    },
    async end(policy?: unknown, props?: unknown) {
      rec('widgets.activity.end', [a.id, policy, props]);
      a.ended = policy ?? 'default';
      widgetsState.activities = widgetsState.activities.filter((x) => x !== a);
    },
    async getPushToken() {
      return null;
    },
    addPushTokenListener() {
      return { remove() {} };
    },
  };
}

export const widgets = {
  __esModule: true,
  createWidget(name: string, component: unknown) {
    rec('widgets.createWidget', [name]);
    const w = { component, timeline: [] as unknown[], snapshot: null as unknown };
    widgetsState.widgets.set(name, w);
    return {
      reload: () => rec('widgets.reload', [name]),
      updateTimeline(entries: unknown[]) {
        rec('widgets.updateTimeline', [name, entries]);
        w.timeline = entries;
      },
      updateSnapshot(props: unknown) {
        rec('widgets.updateSnapshot', [name, props]);
        w.snapshot = props;
      },
      getTimeline: async () => w.timeline,
    };
  },
  createLiveActivity(name: string, component: unknown) {
    rec('widgets.createLiveActivity', [name]);
    widgetsState.liveComponents.set(name, component);
    return {
      start(props: unknown, url?: string) {
        rec('widgets.activity.start', [name, props, url]);
        const a: FakeLiveActivity = { id: `la_${widgetsState.activities.length + 1}`, props, url, ended: null };
        widgetsState.activities.push(a);
        return fakeActivity(a);
      },
      getInstances() {
        return widgetsState.activities.map(fakeActivity);
      },
    };
  },
  after(date: Date) {
    return { after: date };
  },
  addUserInteractionListener() {
    return { remove() {} };
  },
  addPushToStartTokenListener() {
    return { remove() {} };
  },
  widgetsDirectory: 'file:///fake/app-group/widgets/',
};

// ---------------------------------------------------------------- @expo/ui/swift-ui

export const swiftUI = hostModule('SwiftUI');
export const swiftUIModifiers = hostModule('SwiftUI.modifier');

// ---------------------------------------------------------------- react-native-android-widget

export const androidWidgetState = {
  taskHandler: null as null | ((props: unknown) => Promise<void>),
};

export const androidWidget = hostModule('Android', {
  __esModule: true,
  registerWidgetTaskHandler(fn: (props: unknown) => Promise<void>) {
    rec('androidWidget.registerWidgetTaskHandler', []);
    androidWidgetState.taskHandler = fn;
  },
  async requestWidgetUpdate(opts: unknown) {
    rec('androidWidget.requestWidgetUpdate', [opts]);
  },
  async getWidgetInfo(name: string) {
    rec('androidWidget.getWidgetInfo', [name]);
    return [];
  },
});

// ---------------------------------------------------------------- wy-link (push.md 14.8)

type LinkStateT = import('../modules/wy-link').LinkState;
type LinkConfigT = import('../modules/wy-link').LinkConfig;

const linkInitial = (): LinkStateT => ({
  enabled: false,
  status: 'off',
  transport: null,
  connectedSince: null,
  nextRetryAt: null,
  lastError: null,
  lastSeq: 0,
  received: 0,
  rejected: 0,
  batteryExempt: false,
  notificationsAllowed: true,
  verified: 'qr',
  pingS: 180,
  checkCode: null,
});

/** Scriptable state of the wy-link fake; `emitLink` plays the native side's events. */
export const linkState = {
  available: true,
  state: linkInitial(),
  keys: {
    sign: { alg: 'p256' as const, pub: 'BFAKESIGNPUBLICKEY' },
    box: { alg: 'p256' as const, pub: 'BFAKEBOXPUBLICKEY' },
    auth: 'FAKEAUTHSECRET0000000A',
  },
  sealed: 'SEALEDKEYSFAKE',
  config: null as LinkConfigT | null,
  /** A code configure() rejects with (ERR_WY_HUB_KEY, ERR_WY_TAMPERED, ...), or null. */
  configureError: null as string | null,
  prepareError: false,
  /** What requestBatteryExemption() turns batteryExempt into. */
  exemptAfterRequest: true,
  refresh: null as import('../modules/wy-link').LinkRefresh | null,
  /** What checkUpdate() finds (null: up to date), what it rejects with, and installUpdate()'s answer or rejection. */
  update: null as import('../modules/wy-link').UpdateInfo | null,
  updateError: null as string | null,
  installResult: 'started' as 'started' | 'refused',
  installError: null as string | null,
  /** The module's in-memory (origin, token) of the last checkUpdate that found a hub release (push.md 14.8). */
  hubAuth: false,
  listeners: new Map<string, Set<(p: unknown) => void>>(),
};

export function emitLink(event: 'state' | 'refresh' | 'revoked' | 'upgrade', payload?: unknown): void {
  if (event === 'state' && payload) linkState.state = { ...linkState.state, ...(payload as Partial<LinkStateT>) };
  linkState.listeners.get(event)?.forEach((l) => l(event === 'state' ? linkState.state : payload));
}

const codedError = (code: string, message: string) => Object.assign(new Error(message), { code });

export const wyLink = {
  __esModule: true,
  get available() {
    return linkState.available;
  },
  CONFIGURE_ERRORS: { hubKey: 'ERR_WY_HUB_KEY', tampered: 'ERR_WY_TAMPERED' },
  async prepareKeys() {
    rec('link.prepareKeys', []);
    if (linkState.prepareError) throw codedError('ERR_WY_KEYS', 'wy-link: keystore');
    return linkState.keys;
  },
  async sealKeys(k: string, secret: string) {
    rec('link.sealKeys', [k, secret.length]); // never the secret itself
    return linkState.sealed;
  },
  async configure(cfg: LinkConfigT) {
    rec('link.configure', [cfg]);
    if (linkState.configureError) throw codedError(linkState.configureError, 'wy-link: refused');
    linkState.config = cfg;
    linkState.state = { ...linkState.state, verified: cfg.reg, pingS: cfg.pingS, checkCode: 'k7q2-m4xa' };
  },
  async start() {
    rec('link.start', []);
    if (!linkState.config) throw codedError('ERR_WY_NOT_CONFIGURED', 'wy-link: configure first');
    linkState.state = { ...linkState.state, enabled: true, status: 'connecting' };
  },
  async stop() {
    rec('link.stop', []);
    linkState.state = { ...linkState.state, enabled: false, status: 'off', transport: null, connectedSince: null };
  },
  async reconnect() {
    rec('link.reconnect', []);
  },
  async getState() {
    rec('link.getState', []);
    return { ...linkState.state };
  },
  async setPingInterval(seconds: number) {
    rec('link.setPingInterval', [seconds]);
    if (seconds < 60 || seconds > 900) throw codedError('ERR_WY_RANGE', 'wy-link: range');
    linkState.state = { ...linkState.state, pingS: seconds };
  },
  async setSnoozeMinutes(minutes: number) {
    rec('link.setSnoozeMinutes', [minutes]);
  },
  async requestBatteryExemption() {
    rec('link.requestBatteryExemption', []);
    linkState.state = { ...linkState.state, batteryExempt: linkState.exemptAfterRequest };
    return linkState.exemptAfterRequest;
  },
  async openBatterySettings() {
    rec('link.openBatterySettings', []);
  },
  async sendLocalTest() {
    rec('link.sendLocalTest', []);
  },
  async scheduleWidgetRefresh(atMs: number) {
    rec('link.scheduleWidgetRefresh', [atMs]);
  },
  async getRefresh() {
    rec('link.getRefresh', []);
    return linkState.refresh;
  },
  async reset() {
    rec('link.reset', []);
    linkState.config = null;
    linkState.state = linkInitial();
    linkState.hubAuth = false;
  },
  addListener(event: string, cb: (p: unknown) => void) {
    rec('link.addListener', [event]);
    const set = linkState.listeners.get(event) ?? new Set();
    set.add(cb);
    linkState.listeners.set(event, set);
    return { remove: () => set.delete(cb) };
  },
  async checkUpdate(origin: string, token: string) {
    // The token is a secret: only its presence is recorded.
    rec('link.checkUpdate', [origin, token ? '<token>' : '']);
    if (linkState.updateError) throw codedError('ERR_WY_UPDATE', `wy-link: ${linkState.updateError}`);
    linkState.hubAuth = linkState.update?.source === 'hub';
    return linkState.update;
  },
  async installUpdate(info: { version: string; source?: string }) {
    rec('link.installUpdate', [info.version]);
    if (info.source === 'hub' && !linkState.hubAuth) throw codedError('ERR_WY_UPDATE', 'wy-link: check for updates first');
    if (linkState.installError) throw codedError('ERR_WY_UPDATE', `wy-link: ${linkState.installError}`);
    return linkState.installResult;
  },
};

// ---------------------------------------------------------------- wy-tabs (phone.md P17.1)

type TabViewPropsT = import('../modules/wy-tabs').TabViewProps;

/** Scriptable state of the wy-tabs fake; `tabsState.view` holds the last TabView's props. */
export const tabsState = {
  available: true,
  supported: true,
  profiles: [] as string[],
  /** Seats whose tab is open (deleteProfile refuses them, as ProfileStore does). */
  inUse: new Set<string>(),
  view: null as TabViewPropsT | null,
};

export const wyTabs = {
  __esModule: true,
  get available() {
    return tabsState.available;
  },
  async isSupported() {
    rec('tabs.isSupported', []);
    return tabsState.available && tabsState.supported;
  },
  async listProfiles() {
    rec('tabs.listProfiles', []);
    return [...tabsState.profiles];
  },
  async deleteProfile(seat: string) {
    rec('tabs.deleteProfile', [seat]);
    if (tabsState.inUse.has(seat)) throw codedError('ERR_WY_TABS_IN_USE', 'wy-tabs: close the tab first');
    const had = tabsState.profiles.includes(seat);
    tabsState.profiles = tabsState.profiles.filter((x) => x !== seat);
    return had;
  },
  async moveProfile(from: string, to: string) {
    rec('tabs.moveProfile', [from, to]);
    if (tabsState.inUse.has(from)) throw codedError('ERR_WY_TABS_IN_USE', 'wy-tabs: close the tab first');
    if (!tabsState.profiles.includes(from)) throw codedError('ERR_WY_TABS_NONE', 'wy-tabs: that seat has no tab');
    if (tabsState.profiles.includes(to)) throw codedError('ERR_WY_TABS_TAKEN', 'wy-tabs: the other seat has a tab already');
    tabsState.profiles = [...tabsState.profiles.filter((x) => x !== from), to];
    return true;
  },
  TabView(props: TabViewPropsT) {
    tabsState.view = props;
    if (!tabsState.profiles.includes(props.seat)) tabsState.profiles.push(props.seat);
    return React.createElement('WyTabView', { testID: props.testID ?? 'tab-view', seat: props.seat, url: props.url });
  },
};

// ---------------------------------------------------------------- reset

/** Clears every fake (test/setup.ts runs it before each test). */
export function resetFakes(): void {
  calls.length = 0;
  notificationsState.permission = 'granted';
  notificationsState.channels.clear();
  notificationsState.categories.clear();
  notificationsState.badge = 0;
  notificationsState.lastResponse = null;
  notificationsState.responseListeners = [];
  notificationsState.receivedListeners = [];
  notificationsState.handler = null;
  notificationsState.presented = [];
  secureState.items.clear();
  secureState.locked = false;
  scanState.granted = true;
  scanState.canAskAgain = true;
  scanState.results = [];
  widgetsState.widgets.clear();
  widgetsState.liveComponents.clear();
  widgetsState.activities = [];
  androidWidgetState.taskHandler = null;
  linkState.available = true;
  linkState.state = linkInitial();
  linkState.config = null;
  linkState.configureError = null;
  linkState.prepareError = false;
  linkState.exemptAfterRequest = true;
  linkState.refresh = null;
  linkState.update = null;
  linkState.updateError = null;
  linkState.installResult = 'started';
  linkState.installError = null;
  linkState.hubAuth = false;
  linkState.listeners.clear();
  tabsState.available = true;
  tabsState.supported = true;
  tabsState.profiles = [];
  tabsState.inUse.clear();
  tabsState.view = null;
}
