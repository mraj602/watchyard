import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join, relative } from 'node:path';

import { boot, START_ORDER, SHUTDOWN_ORDER, MODULES, late } from './boot.js';
import { fakeElectron } from '../../test/fakes/electron.js';

const silent = { debug() {}, info() {}, warn() {}, error() {} };

// Fake modules that record each create() and method call, and whether 'ready' had fired.
function recordingModules(app, over = {}) {
  const log = [];
  const handlers = new Map();
  const note = (name, extra = {}) => log.push({ name, ready: app.isReady(), ...extra });
  const methods = {
    security: { applyGlobal: () => note('security.applyGlobal') },
    protocol: { registerSchemes: () => note('protocol.registerSchemes'), handle: () => note('protocol.handle') },
    ipc: {
      register: (ch, fn) => handlers.set(ch, fn),
      start: () => note('ipc.start'),
      send: (ch, p) => note('ipc.send', { ch, p }),
    },
    store: { load: async () => note('store.load'), get: () => ({ tabs: [], settings: {} }), flush: async () => note('store.flush') },
    background: {
      claim: () => (note('background.claim'), true),
      applied: async () => note('background.applied'),
      startHidden: () => (note('background.startHidden'), false),
      start: () => note('background.start'),
      setSettings: async (p) => ({ ...p, set: true }),
    },
    autostart: { ensureIcon: async () => (note('autostart.ensureIcon'), false) },
    wy: { flushNow: async () => note('wy.flushNow') },
    hub: { start: async () => note('hub.start'), stop: async () => note('hub.stop'), view: () => null, ensureView: async () => note('hub.ensureView') },
    window: { selected: () => 'hub', onSelect: () => () => {}, shell: null },
    tabs: { start: async () => note('tabs.start'), stop: async () => note('tabs.stop'), openLink: () => true },
    usage: { start: () => note('usage.start'), stop: async () => note('usage.stop') },
    tray: { start: () => note('tray.start'), stop: () => note('tray.stop') },
    notify: { start: () => note('notify.start'), stop: () => note('notify.stop') },
    menu: { build: async () => note('menu.build') },
    state: { stop: () => note('state.stop') },
  };
  const inst = (name, extra = {}) => ({
    create(deps) {
      note(name, { deps });
      return { ...(methods[name] ?? {}), ...extra, ...(over[name] ?? {}) };
    },
  });
  const modules = {};
  for (const name of Object.keys(MODULES)) modules[name] = inst(name);
  modules.paths = inst('paths', { appStateFile: '/u/app-state.json', hubTokenFile: '/u/hub-token.bin', userData: '/u' });
  return { modules, log, handlers };
}

async function run(over = {}, opts = {}) {
  const fake = fakeElectron();
  const { app } = fake.electron;
  const rec = recordingModules(app, over);
  const out = [];
  const p = boot(fake.electron, {
    modules: rec.modules, env: {}, testHooks: async () => null, log: silent, argv: [],
    stdout: { write: (s) => out.push(s) }, stderr: { write: (s) => out.push(s) }, ...opts,
  });
  setImmediate(() => app._ready());
  const made = await p;
  await new Promise((r) => setImmediate(r));
  const names = rec.log.map((e) => e.name);
  const at = (name) => rec.log.find((e) => e.name === name);
  const idx = (name) => names.indexOf(name);
  return { fake, app, made, ...rec, names, at, idx, out };
}

test('boot creates the modules in START_ORDER, with paths, security, schemes, ipc and the instance lock before ready', async () => {
  const { made, log, at, idx } = await run();
  const order = log.filter((e) => START_ORDER.includes(e.name) && e.deps).map((e) => e.name);
  assert.deepEqual(order, [...START_ORDER]);
  for (const early of ['paths', 'security', 'security.applyGlobal', 'protocol', 'protocol.registerSchemes', 'ipc', 'store', 'autostart', 'background', 'background.claim']) {
    assert.equal(at(early).ready, false, `${early} must run before ready`);
  }
  for (const later of ['protocol.handle', 'store.load', 'wy', 'hub', 'window', 'state', 'tabs', 'usage', 'tray', 'notify', 'menu', 'background.start']) {
    assert.equal(at(later).ready, true, `${later} must run after ready`);
  }
  // The start order documented in boot.js.
  const before = (a, b) => assert.ok(idx(a) >= 0 && idx(b) >= 0 && idx(a) < idx(b), `${a} before ${b}`);
  before('paths', 'background.claim');
  before('background.claim', 'store.load');
  before('store.load', 'tabs.start');
  before('tabs.start', 'usage.start');
  before('ipc.start', 'window');
  before('background.startHidden', 'window');
  before('window', 'hub.start');
  before('window', 'state');
  before('hub.start', 'tray.start');
  before('tray.start', 'notify.start');
  before('notify.start', 'menu.build');
  before('menu.build', 'background.start');
  assert.ok(made.bus && made.background && typeof made.shutdown === 'function');
  assert.equal(made.stopped, undefined);
});

test('boot wires the deps each module asked for', async () => {
  const { at, handlers } = await run({}, {
    env: { WY_APP_TEST_CLAUDE_ORIGIN: 'http://127.0.0.1:9', WY_APP_TEST_CADENCE_MS: '2000', HOME: '/h' },
    testHooks: async () => ({ claudeOrigin: () => 'http://127.0.0.1:9', cadenceMs: () => 2000, wyBin: () => '/x/wy', userData: () => null }),
  });
  assert.equal(at('security').deps.claudeOrigin, 'http://127.0.0.1:9');
  assert.equal(typeof at('store').deps.toast, 'function');
  const auto = at('autostart').deps;
  for (const k of ['electron', 'fs', 'path', 'platform', 'home', 'execPath', 'appImage', 'appPath', 'isPackaged', 'env', 'iconPath']) assert.ok(k in auto, `autostart.${k}`);
  assert.equal(auto.home, '/h');
  const bg = at('background').deps;
  assert.ok(bg.autostart && bg.bus && typeof bg.onQuit === 'function');
  assert.equal(at('tabs').deps.claudeOrigin, 'http://127.0.0.1:9');
  assert.equal(at('usage').deps.claudeOrigin, 'http://127.0.0.1:9');
  assert.equal(at('usage').deps.cadenceMs, 2000);
  assert.equal(at('wy').deps.wyBin, '/x/wy');
  assert.ok(at('state').deps.hub && at('state').deps.store);
  assert.match(at('tray').deps.assetsDir, /assets$/);
  const menu = at('menu').deps;
  assert.ok(menu.autostart && menu.bus && typeof menu.setSettings === 'function');
  assert.deepEqual(await menu.setSettings({ keep_running: false }), { keep_running: false, set: true });
  assert.deepEqual(await handlers.get('settings:set')({ notifications: true }), { notifications: true, set: true });
  // hub's handoff is true only when a tab took the link
  assert.equal(await at('hub').deps.openSessionLink('https://claude.ai/code/session_x', null), true);
});

test('a second instance stops before ready and creates nothing else', async () => {
  const { made, names } = await run({ background: { claim: () => false } });
  assert.equal(made.stopped, true);
  assert.ok(names.includes('background.applied'));
  for (const n of ['store.load', 'window', 'tabs', 'hub']) assert.ok(!names.includes(n), `${n} must not run`);
});

test('the hub view is made on start when Hub is selected and the board can show', async () => {
  const { made, names, log } = await run();
  assert.ok(!names.includes('hub.ensureView'), 'not before the hub says ok');
  made.bus.emit('hub', { status: 'ok' });
  assert.ok(log.some((e) => e.name === 'hub.ensureView'), 'made once the hub is ok');
});

test('shutdown runs in SHUTDOWN_ORDER and once', async () => {
  const { made, log } = await run();
  await Promise.all([made.shutdown(), made.shutdown()]);
  const steps = log.map((e) => e.name).filter((n) => SHUTDOWN_ORDER.includes(n));
  assert.deepEqual(steps, [...SHUTDOWN_ORDER]);
});

test('a failing shutdown step does not block the rest', async () => {
  const { made, log } = await run({ tabs: { stop: async () => { throw new Error('boom'); } } });
  await made.shutdown();
  const steps = log.map((e) => e.name).filter((n) => SHUTDOWN_ORDER.includes(n));
  assert.deepEqual(steps, SHUTDOWN_ORDER.filter((s) => s !== 'tabs.stop'));
});

test('late() reads its target at use time', () => {
  let target = null;
  const l = late(() => target);
  assert.equal(l.win, undefined);
  target = { win: 1, show() { return this.win; } };
  assert.equal(l.win, 1);
  assert.equal(l.show(), 1);
  assert.equal(l.available?.(), undefined);
});

test('every module of the skeleton exports create()', () => {
  for (const [name, mod] of Object.entries(MODULES)) assert.equal(typeof mod.create, 'function', name);
});

test('only main/index.js imports electron, and only main/wy/ imports child_process', () => {
  const src = fileURLToPath(new URL('..', import.meta.url));
  const files = [];
  const walk = (d) => {
    for (const n of readdirSync(d)) {
      const p = join(d, n);
      if (statSync(p).isDirectory()) walk(p);
      else if (/\.(c|m)?js$/.test(n) && !/\.test\.(c|m)?js$/.test(n)) files.push(p);
    }
  };
  walk(src);
  for (const f of files) {
    const rel = relative(src, f).split('\\').join('/');
    const text = readFileSync(f, 'utf8');
    const importsElectron = /from\s+['"]electron['"]|import\(\s*['"]electron['"]\s*\)|require\(\s*['"]electron['"]\s*\)/.test(text);
    if (importsElectron) assert.ok(rel === 'main/index.js' || rel === 'preload/shell.cjs', `${rel} imports electron`);
    const importsCp = /['"](node:)?child_process['"]/.test(text);
    if (importsCp) assert.ok(rel.startsWith('main/wy/'), `${rel} imports child_process`);
  }
});

// The shared fake (test/fakes/electron.js) behaves as the module tests rely on.
test('fake electron: safeStorage, ipcMain, protocol, sessions and recording', async () => {
  const a = fakeElectron({ safeStorageKey: 'k1' });
  const b = fakeElectron({ safeStorageKey: 'k2' });
  const enc = a.electron.safeStorage.encryptString('{"token":"wy_d_x"}');
  assert.equal(a.electron.safeStorage.decryptString(enc), '{"token":"wy_d_x"}');
  assert.ok(!enc.toString('latin1').includes('wy_d_x'));
  assert.throws(() => b.electron.safeStorage.decryptString(enc), /decrypt/);
  assert.equal(fakeElectron({ platform: 'linux', storageBackend: 'basic_text' }).electron.safeStorage.getSelectedStorageBackend(), 'basic_text');

  const { ipcMain, protocol, session, WebContentsView, app } = a.electron;
  ipcMain.handle('x:y', (event, args) => ({ from: event.sender.id, args }));
  assert.throws(() => ipcMain.handle('x:y', () => {}), /second handler/);
  const view = new WebContentsView({ webPreferences: { partition: 'persist:tab-t_9c1e5f0a7b2d4e68' } });
  assert.deepEqual(await ipcMain._invoke('x:y', view.webContents, { a: 1 }), { from: view.webContents.id, args: { a: 1 } });
  assert.equal(view.webContents.session, session.fromPartition('persist:tab-t_9c1e5f0a7b2d4e68'));
  assert.notEqual(view.webContents.session, session.defaultSession);

  protocol.handle('app', (req) => new Response(new URL(req.url).pathname, { status: 200 }));
  assert.equal(await (await protocol._request('app://shell/index.html')).text(), '/index.html');

  const s = session.fromPartition('persist:wy-hub');
  s.setPermissionRequestHandler((wc, perm, cb) => cb(perm === 'clipboard-sanitized-write'));
  assert.equal(await s.requestPermission('clipboard-sanitized-write'), true);
  assert.equal(await s.requestPermission('geolocation'), false);
  await s.cookies.set({ url: 'https://hub.example', name: 'wy_session', value: 'v' });
  assert.equal((await s.cookies.get({ url: 'https://hub.example' })).length, 1);
  assert.equal((await session.fromPartition('persist:other').cookies.get({})).length, 0);

  assert.equal(view.webContents._cancellable('will-navigate', 'https://example.com'), false);
  view.webContents.on('will-navigate', (e) => e.preventDefault());
  assert.equal(view.webContents._cancellable('will-navigate', 'https://example.com'), true);

  assert.equal(app.getPath('userData'), '/fake/appData/Watchyard');
  app.setPath('userData', '/fake/appData/watchyard-desktop');
  app._ready();
  assert.throws(() => app.setPath('userData', '/elsewhere'));
  assert.deepEqual(a.callsOf('app.setPath')[0], ['userData', '/fake/appData/watchyard-desktop']);
  assert.equal(a.callsOf('safeStorage.encryptString')[0].length, 0, 'secrets are never recorded');
});
