// Run at login (C1): Linux ~/.config/autostart/watchyard-desktop.desktop and
// ~/.local/share/applications/watchyard-desktop.desktop; macOS and Windows
// app.setLoginItemSettings with --background. Never reads or writes watchyard.desktop.
// The entries name the icon (Icon=watchyard-desktop); a deb or rpm installs it, an AppImage
// does not, so on Linux the app copies its icon into ~/.local/share/icons as well. GNOME on
// Wayland takes the window and notification icon from the entry, so without it both are blank.
import { DESKTOP_ENTRY, FLAG_BACKGROUND, TEST_ENV } from '../shared/constants.js';

/**
 * @param {{electron: object, fs: typeof import('node:fs/promises'), path: object, platform: string, home: string,
 *          execPath: string, appImage?: string|null, appPath?: string, isPackaged?: boolean,
 *          env?: Record<string,string|undefined>, iconName?: string, iconPath?: string}} deps
 *   fs: the promise API (mkdir, writeFile, rm, access). appImage: $APPIMAGE, used instead of
 *   execPath (which sits in a temporary mount). appPath + !isPackaged: a development run
 *   (`electron <appPath>`). env: only WY_APP_USER_DATA is read, and only when !isPackaged.
 *   iconPath: the app's 512 px icon (assets/icon.png), copied to files().icon.
 * @returns {{get(): Promise<boolean>, set(on: boolean): Promise<void>, desktopEntry(background: boolean): string,
 *            files(): {autostart: string, launcher: string, icon: string}, ensureIcon(): Promise<boolean>}}
 */
export function create(deps) {
  const { electron, fs, path, platform, home, execPath } = deps;
  const { app } = electron;
  const isPackaged = deps.isPackaged ?? app.isPackaged;
  const iconName = deps.iconName ?? 'watchyard-desktop';

  const files = () => ({
    autostart: path.join(home, '.config', 'autostart', DESKTOP_ENTRY),
    launcher: path.join(home, '.local', 'share', 'applications', DESKTOP_ENTRY),
    icon: path.join(home, '.local', 'share', 'icons', 'hicolor', '512x512', 'apps', `${iconName}.png`),
  });
  const exists = async (f) => {
    try {
      await fs.access(f);
      return true;
    } catch {
      return false;
    }
  };
  // Quote for the Exec line: backslash-escape " \ ` $, and double `%` (a field code in Exec).
  const q = (x) => `"${String(x).replace(/(["\\`$])/g, '\\$1').replace(/%/g, '%%')}"`;

  function desktopEntry(background) {
    const userData = !isPackaged && deps.env?.[TEST_ENV.USER_DATA] ? path.resolve(deps.env[TEST_ENV.USER_DATA]) : null;
    const env = userData ? `env ${TEST_ENV.USER_DATA}=${q(userData)} ` : '';
    const bin = deps.appImage || execPath;
    const exec = isPackaged || deps.appImage ? q(bin) : `${q(bin)} ${q(deps.appPath ?? app.getAppPath())}`;
    return [
      '[Desktop Entry]',
      'Type=Application',
      'Name=Watchyard Desktop',
      'Comment=Usage, limits and alerts for several Claude accounts (unofficial)',
      `Exec=${env}${exec}${background ? ` ${FLAG_BACKGROUND}` : ''}`,
      `Icon=${iconName}`,
      'Terminal=false',
      'Categories=Utility;Development;',
      'StartupWMClass=watchyard-desktop',
      ...(background ? ['X-GNOME-Autostart-enabled=true', 'X-GNOME-Autostart-Delay=10'] : []),
      '',
    ].join('\n');
  }

  /** Copy the icon to files().icon. A failure only costs the icon, so it is not thrown. */
  async function installIcon() {
    if (!deps.iconPath) return false;
    try {
      const f = files().icon;
      await fs.mkdir(path.dirname(f), { recursive: true });
      await fs.copyFile(deps.iconPath, f);
      return true;
    } catch {
      return false;
    }
  }

  const api = {
    desktopEntry,
    files,
    /** At start (Linux): an entry written earlier but no icon yet gets one. Reports a copy. */
    async ensureIcon() {
      if (platform !== 'linux') return false;
      const f = files();
      if (!(await exists(f.launcher)) && !(await exists(f.autostart))) return false;
      if (await exists(f.icon)) return false;
      return installIcon();
    },
    async get() {
      if (platform === 'linux') {
        return exists(files().autostart);
      }
      return Boolean(app.getLoginItemSettings({ args: [FLAG_BACKGROUND] }).openAtLogin);
    },
    async set(on) {
      if (platform === 'linux') {
        const f = files();
        if (on) {
          await fs.mkdir(path.dirname(f.autostart), { recursive: true });
          await fs.writeFile(f.autostart, desktopEntry(true), { mode: 0o644 });
          await fs.mkdir(path.dirname(f.launcher), { recursive: true });
          await fs.writeFile(f.launcher, desktopEntry(false), { mode: 0o644 }); // so it opens from the app grid
          await installIcon();
        } else {
          await fs.rm(f.autostart, { force: true });
        }
        return;
      }
      const item = { openAtLogin: Boolean(on), args: [FLAG_BACKGROUND] };
      if (platform === 'darwin') item.openAsHidden = true;
      app.setLoginItemSettings(item);
    },
  };
  return api;
}
