import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import fsp from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';

import { create } from './autostart.js';
import { fakeElectron } from '../../test/fakes/electron.js';

const homes = [];
after(() => Promise.all(homes.map((h) => fsp.rm(h, { recursive: true, force: true }))));

async function setup(over = {}) {
  const home = await fsp.mkdtemp(path.join(os.tmpdir(), 'wy-autostart-'));
  homes.push(home);
  const fake = fakeElectron({ platform: over.platform ?? 'linux', isPackaged: over.isPackaged ?? true });
  const a = create({
    electron: fake.electron, fs: fsp, path, platform: over.platform ?? 'linux', home,
    execPath: '/opt/Watchyard/watchyard-desktop', ...over,
  });
  return { home, fake, a };
}

const GOLDEN_BACKGROUND = `[Desktop Entry]
Type=Application
Name=Watchyard Desktop
Comment=Usage, limits and alerts for several Claude accounts (unofficial)
Exec="/opt/Watchyard/watchyard-desktop" --background
Icon=watchyard-desktop
Terminal=false
Categories=Utility;Development;
StartupWMClass=watchyard-desktop
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=10
`;

const GOLDEN_PLAIN = `[Desktop Entry]
Type=Application
Name=Watchyard Desktop
Comment=Usage, limits and alerts for several Claude accounts (unofficial)
Exec="/opt/Watchyard/watchyard-desktop"
Icon=watchyard-desktop
Terminal=false
Categories=Utility;Development;
StartupWMClass=watchyard-desktop
`;

test('golden: the desktop entry, background and not', async () => {
  const { a } = await setup();
  assert.equal(a.desktopEntry(true), GOLDEN_BACKGROUND);
  assert.equal(a.desktopEntry(false), GOLDEN_PLAIN);
});

test('quoting: spaces, quotes, dollar, backtick, backslash and percent are escaped', async () => {
  const { a } = await setup({ execPath: '/opt/My "App"/$x`y\\z%d/sw' });
  assert.match(a.desktopEntry(false), /^Exec="\/opt\/My \\"App\\"\/\\\$x\\`y\\\\z%%d\/sw"$/m);
});

test('an AppImage uses $APPIMAGE, and a development run names the app path and the user-data override', async () => {
  const img = await setup({ appImage: '/home/u/Watchyard.AppImage' });
  assert.match(img.a.desktopEntry(true), /^Exec="\/home\/u\/Watchyard\.AppImage" --background$/m);
  const dev = await setup({ isPackaged: false, execPath: '/n/electron', appPath: '/src/app', env: { WY_APP_USER_DATA: '/tmp/x' } });
  // The override is path.resolve()d (drive letter and backslashes on Windows), then quoted
  // as in desktopEntry: backslashes and quotes are escaped.
  const userData = path.resolve('/tmp/x').replace(/(["\\`$])/g, '\\$1');
  assert.equal(
    dev.a.desktopEntry(true).split('\n').find((l) => l.startsWith('Exec=')),
    `Exec=env WY_APP_USER_DATA="${userData}" "/n/electron" "/src/app" --background`,
  );
  const packaged = await setup({ env: { WY_APP_USER_DATA: '/tmp/x' } });
  assert.doesNotMatch(packaged.a.desktopEntry(true), /WY_APP_USER_DATA/);
});

test('linux: set(true) writes both entries, get() reads, set(false) removes only the autostart one', async () => {
  const { home, a } = await setup();
  assert.equal(await a.get(), false);
  await a.set(true);
  assert.equal(await a.get(), true);
  const f = a.files();
  assert.equal(f.autostart, path.join(home, '.config/autostart/watchyard-desktop.desktop'));
  assert.equal(f.launcher, path.join(home, '.local/share/applications/watchyard-desktop.desktop'));
  assert.equal(await fsp.readFile(f.autostart, 'utf8'), GOLDEN_BACKGROUND);
  assert.equal(await fsp.readFile(f.launcher, 'utf8'), GOLDEN_PLAIN);
  await a.set(false);
  assert.equal(await a.get(), false);
  await assert.doesNotReject(fsp.access(f.launcher));
  await a.set(false); // idempotent
});

test('darwin and win32: a login item with --background, nothing written to disk', async () => {
  for (const platform of ['darwin', 'win32']) {
    const { home, fake, a } = await setup({ platform });
    assert.equal(await a.get(), false);
    await a.set(true);
    const [item] = fake.callsOf('app.setLoginItemSettings')[0];
    assert.equal(item.openAtLogin, true);
    assert.deepEqual(item.args, ['--background']);
    assert.equal(item.openAsHidden, platform === 'darwin' ? true : undefined);
    assert.equal(await a.get(), true);
    await a.set(false);
    assert.equal(await a.get(), false);
    assert.deepEqual(await fsp.readdir(home), []);
  }
});

test('linux: turning run at login on also installs the icon the entries name', async () => {
  const src = path.join(await fsp.mkdtemp(path.join(os.tmpdir(), 'wy-icon-')), 'icon.png');
  homes.push(path.dirname(src));
  await fsp.writeFile(src, 'png');
  const { a } = await setup({ iconPath: src });
  await a.set(true);
  assert.equal(await fsp.readFile(a.files().icon, 'utf8'), 'png');
  assert.ok(a.files().icon.endsWith(path.join('.local', 'share', 'icons', 'hicolor', '512x512', 'apps', 'watchyard-desktop.png')));
});

test('linux: ensureIcon adds a missing icon only when an entry exists', async () => {
  const src = path.join(await fsp.mkdtemp(path.join(os.tmpdir(), 'wy-icon-')), 'icon.png');
  homes.push(path.dirname(src));
  await fsp.writeFile(src, 'png');
  const { a } = await setup({ iconPath: src });
  assert.equal(await a.ensureIcon(), false, 'no entry, no icon');
  await assert.rejects(fsp.access(a.files().icon));
  await fsp.mkdir(path.dirname(a.files().launcher), { recursive: true });
  await fsp.writeFile(a.files().launcher, a.desktopEntry(false));
  assert.equal(await a.ensureIcon(), true);
  assert.equal(await fsp.readFile(a.files().icon, 'utf8'), 'png');
  assert.equal(await a.ensureIcon(), false, 'already there');
});

test('ensureIcon does nothing off Linux or without an icon path', async () => {
  const mac = await setup({ platform: 'darwin', iconPath: '/nope.png' });
  assert.equal(await mac.a.ensureIcon(), false);
  const { a } = await setup();
  await fsp.mkdir(path.dirname(a.files().launcher), { recursive: true });
  await fsp.writeFile(a.files().launcher, a.desktopEntry(false));
  assert.equal(await a.ensureIcon(), false);
});
