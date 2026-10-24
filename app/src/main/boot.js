// Start-up wiring and shutdown of the main process (docs/spec/desktop.md C1, C2). index.js imports
// Electron and calls boot(); everything here takes Electron and the modules as arguments, so
// a unit test runs it on plain Node with the fakes. Modules talk only through bus.js and their
// create() interfaces; none imports another module's internals.
//
// Start order (START_ORDER is the order of the create() calls):
//
//   before 'ready'  test hooks → paths (userData) → security (+ applyGlobal) → protocol
//                   (+ registerSchemes) → ipc → store (created, not loaded) → autostart →
//                   background → background.claim() (single instance; a second launch or a
//                   --autostart=on|off launch stops here)
//   after 'ready'   protocol.handle → store.load → wy → hub → ipc.start() (before the shell can call) → window (hidden with
//                   --background only) → state → tabs.start() → usage.start() →
//                   hub.start() (the hub view is made when Hub is selected) → tray, notify,
//                   menu.build() → background.start()
//
// tabs.start() needs window.addView, so it runs right after the window; the shell's first
// state:get comes after its page has loaded, by when the tabs and usage events are on the bus.
//
// Shutdown (background's before-quit → onQuit): usage.stop() (waits ≤ 5 s for the last
// wy outbox append) → tabs.stop() (cookies flushed, lastUrl written) → hub.stop() (the stream)
// → wy.flushNow() (a pending flush, capped) → store.flush() → notify, tray, state.

import * as nodePath from 'node:path';
import * as nodeFs from 'node:fs/promises';
import * as nodeOs from 'node:os';
import { fileURLToPath } from 'node:url';

import { createBus } from './bus.js';
import * as paths from './paths.js';
import * as security from './security.js';
import * as protocol from './protocol.js';
import * as store from './store.js';
import * as ipc from './ipc.js';
import * as state from './state.js';
import { missingAccounts } from './state.js';
import * as wy from './wy/index.js';
import * as hub from './hub/index.js';
import * as window from './window.js';
import { HUB_STRIP_PX } from './window.js';
import * as tabs from './tabs/index.js';
import * as usage from './usage/index.js';
import * as tray from './tray.js';
import * as notify from './notify.js';
import * as menu from './menu.js';
import * as autostart from './autostart.js';
import * as background from './background.js';

/** The order of the create() calls (see the header). */
export const START_ORDER = Object.freeze([
  'paths', 'security', 'protocol', 'ipc', 'store', 'autostart', 'background',
  'wy', 'hub', 'window', 'state', 'tabs', 'usage', 'tray', 'notify', 'menu',
]);

/** The order of the shutdown steps (onQuit). */
export const SHUTDOWN_ORDER = Object.freeze(['usage.stop', 'tabs.stop', 'hub.stop', 'wy.flushNow', 'store.flush']);

export const MODULES = Object.freeze({
  paths, security, protocol, store, ipc, state, wy, hub, window, tabs, usage, tray, notify, menu, autostart, background,
});

const here = nodePath.dirname(fileURLToPath(import.meta.url));
export const SRC_DIR = nodePath.resolve(here, '..');
export const APP_DIR = nodePath.resolve(SRC_DIR, '..');
export const PRELOAD_PATH = nodePath.join(SRC_DIR, 'preload', 'shell.cjs');
export const ASSETS_DIR = nodePath.join(APP_DIR, 'assets');
/** The app icon for the window and notifications (Linux, Windows; a macOS bundle has its own). */
export const ICON_PATH = nodePath.join(ASSETS_DIR, 'icon.png');

/** A pending `wy flush` at quit may wait this long (at most 5 s); the outbox keeps the records anyway. */
export const QUIT_FLUSH_MAX_MS = 5_000;

/** test-hooks.js is imported only when unpackaged; null when packaged or absent. */
async function defaultTestHooks(app) {
  if (app.isPackaged) return null;
  try {
    return await import('./test-hooks.js');
  } catch (err) {
    if (err?.code === 'ERR_MODULE_NOT_FOUND') return null;
    throw err;
  }
}

/**
 * An object whose properties are read from get() at use time: for modules made later than the
 * module that needs them (background is made before the window and the tray).
 */
export function late(get) {
  return new Proxy(Object.create(null), {
    get(_t, key) {
      const o = get();
      if (o === null || o === undefined) return undefined;
      const v = o[key];
      return typeof v === 'function' ? v.bind(o) : v;
    },
    has: (_t, key) => {
      const o = get();
      return o !== null && o !== undefined && key in o;
    },
  });
}

function withTimeout(p, ms) {
  let h;
  const limit = new Promise((resolve) => {
    h = setTimeout(resolve, ms);
    h.unref?.();
  });
  return Promise.race([Promise.resolve(p), limit]).finally(() => clearTimeout(h));
}

/**
 * @param {object} electron  the electron module (or test/fakes/electron.js's `electron`)
 * @param {{modules?: object, env?: object, fs?: object, path?: object, log?: object, os?: object,
 *          testHooks?: (app: object) => Promise<object|null>, argv?: string[], fetch?: Function,
 *          platform?: string, minWy?: string, stdout?: {write: Function}, stderr?: {write: Function}}} [opts]
 * @returns {Promise<object>} the created modules by name, plus `shutdown()`; `stopped: true`
 *   when this launch handed over to another instance (or applied --autostart) and quits
 */
export async function boot(electron, opts = {}) {
  const m = opts.modules ?? MODULES;
  const env = opts.env ?? process.env;
  const fs = opts.fs ?? nodeFs;
  const path = opts.path ?? nodePath;
  const os = opts.os ?? nodeOs;
  const log = opts.log ?? console;
  const platform = opts.platform ?? process.platform;
  const argv = opts.argv ?? process.argv;
  const stdout = opts.stdout ?? process.stdout;
  const stderr = opts.stderr ?? process.stderr;
  const { app } = electron;
  const made = {};
  const bus = createBus({ onError: (err, ev) => log.error(`bus ${ev}:`, err) });
  made.bus = bus;

  // ---- before 'ready'

  const testHooks = await (opts.testHooks ?? defaultTestHooks)(app);
  const claudeOrigin = testHooks?.claudeOrigin?.(env) ?? null;
  const cadenceMs = testHooks?.cadenceMs?.(env) ?? null;

  made.paths = m.paths.create({ app, path, env, testHooks });
  made.security = m.security.create({ electron, log, env, testHooks, claudeOrigin });
  made.security.applyGlobal();
  made.protocol = m.protocol.create({ electron, fs, path, srcDir: SRC_DIR, log });
  made.protocol.registerSchemes();
  made.ipc = m.ipc.create({ electron, log, getShell: () => made.window?.shell ?? null });

  const toast = (level, text) => {
    try {
      made.ipc.send('toast', { level, text: String(text).slice(0, 500) || '…' });
    } catch (err) {
      log.warn?.(`toast: ${err?.message ?? err}`);
    }
  };

  made.store = m.store.create({ fs, path, file: made.paths.appStateFile, log, toast });
  made.autostart = m.autostart.create({
    electron, fs, path, platform,
    home: env.HOME || os.homedir(),
    execPath: process.execPath,
    appImage: env.APPIMAGE || null,
    appPath: app.getAppPath(),
    isPackaged: app.isPackaged,
    env,
    iconPath: ICON_PATH,
  });
  made.background = m.background.create({
    electron,
    window: late(() => made.window),
    tray: late(() => made.tray),
    store: made.store,
    autostart: made.autostart,
    argv,
    log,
    bus,
    platform,
    onQuit: () => made.shutdown(),
  });
  made.shutdown = () => shutdown(made, log);

  if (!made.background.claim()) {
    // Another instance has the lock (it shows its window or applies our --autostart), or this
    // launch carried --autostart=on|off: applied() resolves once it is written (and exits).
    await made.background.applied();
    app.exit(0);
    made.stopped = true;
    return made;
  }

  await made.autostart.ensureIcon();
  await app.whenReady();

  // ---- after 'ready'

  made.protocol.handle();
  await made.store.load();

  made.wy = m.wy.create({
    env, fs, path, log, platform, resourcesPath: process.resourcesPath ?? '',
    minWy: opts.minWy ?? '0.6.0', wyBin: testHooks?.wyBin?.(env) ?? null,
  });
  made.hub = m.hub.create({
    electron, wy: made.wy, security: made.security, bus, ipc: made.ipc, fs, path, log, platform,
    tokenFile: made.paths.hubTokenFile, fetch: opts.fetch ?? globalThis.fetch, clientVersion: app.getVersion(), hostname: nodeOs.hostname(),
    get window() {
      return made.window;
    },
    // C7 handoff: true when an account tab took the link; else the hub opens it externally.
    openSessionLink: async (url, seat) => (await made.tabs?.openLink(url, seat, { quiet: true })) === true,
  });

  made.ipc.register('settings:set', (patch) => made.background.setSettings(patch));
  made.ipc.start();

  const icon = platform === 'darwin' ? undefined : ICON_PATH;
  made.window = m.window.create({
    electron, security: made.security, bus, log, preloadPath: PRELOAD_PATH, startHidden: made.background.startHidden(), icon,
  });
  made.state = m.state.create({
    bus, ipc: made.ipc, window: made.window, hub: made.hub, store: made.store, log,
    about: { app: app.getVersion(), electron: process.versions.electron, chrome: process.versions.chrome, platform: `${process.platform} ${process.arch}` },
  });

  made.tabs = m.tabs.create({
    electron, security: made.security, window: made.window, store: made.store, bus, ipc: made.ipc, log, platform,
    claudeOrigin, userData: made.paths.userData, seats: () => made.hub.seats(),
  });
  await made.tabs.start();
  // Usage records go to the hub through wy; without wy (or with a wy that has no hub) the app
  // uploads them itself with its desktop token (hub/direct.js).
  const recordSink = {
    machineId: () => (useDirect() ? made.hub.direct.machineId() : made.wy.machineId()),
    append: (records) => (useDirect() ? made.hub.direct.append(records) : made.wy.append(records)),
    flushSoon: () => (useDirect() ? made.hub.direct.flushSoon() : made.wy.flushSoon()),
  };
  const useDirect = () => made.hub.state().direct === true;
  made.usage = m.usage.create({
    tabs: made.tabs, wy: recordSink, store: made.store, bus, ipc: made.ipc, log, clientVersion: app.getVersion(),
    claudeOrigin, cadenceMs,
  });
  made.usage.start();

  // The hub view is made when Hub is first selected (C7), and only once the board can show.
  const ensureHubView = () => {
    if (made.window.selected() !== 'hub' || made.hub.view()) return;
    if (bus.last('hub')?.status !== 'ok') return;
    Promise.resolve(made.hub.ensureView()).catch((err) => log.warn?.(`hub view: ${err?.message ?? err}`));
  };
  made.window.onSelect((id) => {
    if (id === 'hub') ensureHubView();
  });
  bus.on('hub', ensureHubView);
  Promise.resolve(made.hub.start())
    .then(ensureHubView)
    .catch((err) => log.warn?.(`hub start: ${err?.message ?? err}`));

  // The shell draws a read-only strip above the board; the board view starts below it.
  // It also carries the "no tab here" notice when hub accounts have no tab on this computer.
  const insetNow = () => {
    const h = bus.last('hub');
    const missing = missingAccounts(made.hub.seats(), bus.last('tabs')?.tabs).length > 0;
    return h?.status === 'ok' && !h.board_blocked && (!h.owner || missing || h.insecure) ? HUB_STRIP_PX : 0;
  };
  bus.on('hub', () => made.window.setHubInset?.(insetNow()));
  bus.on('tabs', () => made.window.setHubInset?.(insetNow()));

  made.tray = m.tray.create({
    electron, bus, window: made.window, usage: made.usage, hub: made.hub, platform, assetsDir: ASSETS_DIR, log,
  });
  made.tray.start();
  made.notify = m.notify.create({ electron, bus, hub: made.hub, window: made.window, store: made.store, log, icon });
  made.notify.start();
  made.menu = m.menu.create({
    electron, window: made.window, tabs: made.tabs, usage: made.usage, autostart: made.autostart, store: made.store,
    isPackaged: app.isPackaged, bus, platform, log,
    setSettings: (patch) => made.background.setSettings(patch),
  });
  await made.menu.build();
  made.background.start();

  if (testHooks?.expose?.(env) && !app.isPackaged) globalThis.__watchyard = { modules: made, electron };
  return made;
}

/** The before-quit shutdown, in SHUTDOWN_ORDER; a failing step never blocks the next. */
async function shutdown(made, log) {
  if (made.shuttingDown) return made.shuttingDown;
  const step = async (name, fn) => {
    try {
      await fn();
    } catch (err) {
      log.error?.(`shutdown: ${name}:`, err);
    }
  };
  made.shuttingDown = (async () => {
    await step('usage.stop', () => made.usage?.stop());
    await step('tabs.stop', () => made.tabs?.stop());
    await step('hub.stop', () => made.hub?.stop());
    await step('wy.flushNow', () => withTimeout(made.wy?.flushNow(), QUIT_FLUSH_MAX_MS));
    await step('store.flush', () => made.store?.flush());
    await step('notify.stop', () => made.notify?.stop());
    await step('tray.stop', () => made.tray?.stop());
    await step('state.stop', () => made.state?.stop());
  })();
  return made.shuttingDown;
}
