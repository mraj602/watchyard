#!/usr/bin/env node
// Prebuild sanity check (docs/spec/phone.md P14). Copies phone/ to a temp
// directory (node_modules symlinked), runs `expo prebuild --platform <p> --no-install --clean`
// there, and asserts the generated native config. The real tree is never touched, nothing
// is signed or uploaded, and the copy is deleted afterwards (set WY_KEEP_PREBUILD=1 to keep it).
// Usage: node scripts/prebuild-check.mjs android|ios
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

const phone = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const platform = process.argv[2];
if (platform !== 'android' && platform !== 'ios') {
  console.error('usage: node scripts/prebuild-check.mjs android|ios');
  process.exit(2);
}

const SKIP = new Set(['node_modules', 'android', 'ios', '.expo', '.git', 'coverage', 'dist']);
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'wy-prebuild-'));
const app = path.join(work, 'phone');
// Top-level only: the local modules keep their own android/ sources (modules/wy-link/android),
// minus their build outputs.
const MODULE_SKIP = new Set(['build', '.cxx', '.gradle']);
fs.cpSync(phone, app, {
  recursive: true,
  filter: (src) => src === phone || (path.dirname(src) === phone ? !SKIP.has(path.basename(src)) : !MODULE_SKIP.has(path.basename(src)) && path.basename(src) !== 'node_modules'),
});
fs.symlinkSync(path.join(phone, 'node_modules'), path.join(app, 'node_modules'), 'dir');

const failures = [];
const check = (ok, what) => {
  (ok ? console.log : console.error)(`${ok ? 'ok  ' : 'FAIL'} ${what}`);
  if (!ok) failures.push(what);
};
const read = (...p) => fs.readFileSync(path.join(app, ...p), 'utf8');
const findFiles = (dir, re, out = []) => {
  if (!fs.existsSync(dir)) return out;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory() && e.name !== 'Pods' && e.name !== 'build') findFiles(p, re, out);
    else if (re.test(e.name)) out.push(p);
  }
  return out;
};

try {
  const env = { ...process.env, CI: '1', EXPO_NO_TELEMETRY: '1', EXPO_NO_GIT_STATUS: '1', WY_APP_VARIANT: 'production' };
  delete env.CLAUDE_CONFIG_DIR;
  execFileSync('npx', ['expo', 'prebuild', '--platform', platform, '--no-install', '--clean'], { cwd: app, env, stdio: 'inherit' });

  if (platform === 'android') {
    const manifest = read('android', 'app', 'src', 'main', 'AndroidManifest.xml');
    check(manifest.includes('android.permission.POST_NOTIFICATIONS'), 'POST_NOTIFICATIONS permission');
    check(manifest.includes('android.permission.CAMERA'), 'CAMERA permission');
    check(manifest.includes('android.permission.RECORD_AUDIO'), 'RECORD_AUDIO permission (claude.ai mic in a tab, P17.5)');
    check(manifest.includes('android.permission.MODIFY_AUDIO_SETTINGS'), 'MODIFY_AUDIO_SETTINGS permission (the WebView needs it for the mic, P17.5)');
    for (const p of ['SYSTEM_ALERT_WINDOW']) {
      const re = new RegExp(`<uses-permission[^>]*android\\.permission\\.${p}"(?![^>]*tools:node="remove")[^>]*>`);
      check(!re.test(manifest), `no ${p} (or removed with tools:node="remove")`);
    }
    check(/android:scheme="watchyard"/.test(manifest), 'watchyard intent filter');
    const receivers = [...manifest.matchAll(/<receiver[\s\S]*?<\/receiver>/g)].map((m) => m[0]).filter((r) => r.includes('android.appwidget.action.APPWIDGET_UPDATE'));
    // The widget list is read from app.config.ts (ANDROID_WIDGETS), so it cannot drift from this check.
    const cfg = fs.readFileSync(path.join(phone, 'app.config.ts'), 'utf8');
    const block = cfg.slice(cfg.indexOf('export const ANDROID_WIDGETS'));
    const widgetNames = [...block.slice(0, block.indexOf('] as const')).matchAll(/^\s{4}name: '([A-Za-z0-9]+)'/gm)].map((m) => m[1]);
    check(widgetNames.length > 0, `app.config.ts lists Android widgets (${widgetNames.join(', ')})`);
    check(receivers.length === widgetNames.length, `${widgetNames.length} AppWidgetProvider receivers (found ${receivers.length})`);
    for (const name of widgetNames) check(receivers.some((r) => r.includes(`.widget.${name}"`)), `widget receiver ${name}`);
    check(/<application[^>]*android:allowBackup="false"/.test(manifest), 'allowBackup="false"');
    check(!/<application[^>]*android:usesCleartextTraffic="true"/.test(manifest), 'no cleartext in release');
    check(/<application[^>]*android:dataExtractionRules="@xml\/wy_data_extraction_rules"/.test(manifest), 'dataExtractionRules set');
    check(/<application[^>]*android:fullBackupContent="@xml\/wy_backup_rules"/.test(manifest), 'fullBackupContent set');
    for (const f of ['wy_data_extraction_rules.xml', 'wy_backup_rules.xml']) {
      const p = path.join(app, 'android', 'app', 'src', 'main', 'res', 'xml', f);
      check(fs.existsSync(p) && !/<include/.test(fs.readFileSync(p, 'utf8')) && fs.readFileSync(p, 'utf8').includes('<exclude domain="sharedpref" path="." />'), `${f} excludes everything`);
    }
    const xml = findFiles(path.join(app, 'android', 'app', 'src', 'main', 'res'), /\.xml$/);
    const infos = xml.filter((f) => fs.readFileSync(f, 'utf8').includes('<appwidget-provider'));
    check(infos.length === widgetNames.length, `${widgetNames.length} appwidget-provider infos (found ${infos.length})`);
    check(!fs.existsSync(path.join(app, 'android', 'android')), 'no doubled android/android path');
    // Widget taps open links with an explicit intent (with-wy-native.js patches the providers).
    const providers = findFiles(path.join(app, 'android', 'app', 'src', 'main', 'java'), /\.java$/).filter((f) => fs.readFileSync(f, 'utf8').includes('extends RNWidgetProvider'));
    check(providers.length === widgetNames.length, `${widgetNames.length} widget provider classes (found ${providers.length})`);
    check(providers.every((f) => fs.readFileSync(f, 'utf8').includes('WidgetOpen.handle(context, intent)')), 'every widget provider hands taps to WidgetOpen (explicit intents)');
    // The link build (P13, push.md 14.2). The wy-link module's own manifest is merged
    // by Gradle, so its entries are read there; the app's must carry no FCM entry.
    const link = fs.readFileSync(path.join(app, 'modules', 'wy-link', 'android', 'src', 'main', 'AndroidManifest.xml'), 'utf8');
    const both = manifest + link;
    check(/<service[^>]*WyLinkService[^>]*android:foregroundServiceType="specialUse"/.test(link), 'wy-link service with foregroundServiceType specialUse');
    check(/android\.app\.PROPERTY_SPECIAL_USE_FGS_SUBTYPE/.test(link), 'specialUse subtype property');
    check(both.includes('android.permission.FOREGROUND_SERVICE_SPECIAL_USE'), 'FOREGROUND_SERVICE_SPECIAL_USE permission');
    check(both.includes('android.permission.RECEIVE_BOOT_COMPLETED'), 'RECEIVE_BOOT_COMPLETED permission');
    check(both.includes('android.permission.REQUEST_INSTALL_PACKAGES'), 'REQUEST_INSTALL_PACKAGES permission');
    check(!manifest.includes('com.google.firebase'), 'no com.google.firebase entry in the app manifest');
    check(!/Firebase|com\.google\.android\.gms|mlkit/i.test(manifest), 'no Firebase, Play services or ML Kit entry in the app manifest');
    // No proprietary Google code (P13): the packages that pulled it in are gone, their
    // replacements are the local modules, and no installed package declares it for release.
    const pkg = JSON.parse(fs.readFileSync(path.join(phone, 'package.json'), 'utf8'));
    for (const dep of ['expo-notifications', 'expo-camera', 'expo-application']) {
      check(!(dep in { ...pkg.dependencies, ...pkg.devDependencies }), `no ${dep} dependency`);
    }
    for (const mod of ['wy-notify', 'wy-scan']) {
      check(fs.existsSync(path.join(app, 'modules', mod, 'android', 'build.gradle')), `local module ${mod} present`);
    }
    let staticOk = true;
    try {
      execFileSync(process.execPath, [path.join(phone, 'scripts', 'check-no-google.mjs'), 'static'], { stdio: 'inherit' });
    } catch {
      staticOk = false;
    }
    check(staticOk, 'no Firebase, Play services or ML Kit in any Android build file (check-no-google.mjs static)');
    const gradle = ['build.gradle', path.join('app', 'build.gradle')].map((f) => read('android', f)).join('\n');
    check(!/google-services|com\.google\.gms/.test(gradle), 'no google-services Gradle plugin');
    check(!fs.existsSync(path.join(app, 'android', 'app', 'google-services.json')), 'no google-services.json');
    const props = read('android', 'gradle.properties');
    const minSdk = /android\.minSdkVersion=(\d+)/.exec(props);
    check(!!minSdk && Number(minSdk[1]) >= 31, `minSdkVersion 31 (found ${minSdk ? minSdk[1] : 'none'})`);
    // The notification glyphs ship from the wy-link module's res (autolinked into the app).
    const kinds = ['connection', 'needs_you', 'failed', 'limit', 'reset', 'machine', 'summary', 'generic'];
    const missing = kinds.filter((k) => !fs.existsSync(path.join(app, 'modules', 'wy-link', 'android', 'src', 'main', 'res', 'drawable', `wy_notify_${k}.xml`)));
    check(missing.length === 0, `wy-link notification icons present${missing.length ? ` (missing ${missing.join(', ')})` : ''}`);
  } else {
    const plists = findFiles(path.join(app, 'ios'), /^Info\.plist$/);
    const mainPlist = plists.find((f) => !/Widget|Extension|ExpoWidgetsTarget/i.test(path.relative(path.join(app, 'ios'), f)));
    const plist = mainPlist ? fs.readFileSync(mainPlist, 'utf8') : '';
    check(Boolean(mainPlist), 'app Info.plist');
    check(/<key>NSSupportsLiveActivities<\/key>\s*<true\/>/.test(plist), 'NSSupportsLiveActivities');
    // The QR scanner (modules/wy-scan) is Android only: the iOS app asks for no camera.
    check(!plist.includes('<key>NSCameraUsageDescription</key>'), 'no NSCameraUsageDescription (no scanner on iOS)');
    check(/<key>CFBundleURLSchemes<\/key>\s*<array>[\s\S]*?<string>watchyard<\/string>/.test(plist), 'watchyard URL scheme');
    const ents = findFiles(path.join(app, 'ios'), /\.entitlements$/);
    const withGroup = ents.filter((f) => /com\.apple\.security\.application-groups[\s\S]*?group\./.test(fs.readFileSync(f, 'utf8')));
    check(withGroup.length >= 2, `App Group entitlement on the app and the widget extension (${withGroup.map((f) => path.relative(app, f)).join(', ') || 'none'})`);
    const pbx = findFiles(path.join(app, 'ios'), /^project\.pbxproj$/).map((f) => fs.readFileSync(f, 'utf8')).join('\n');
    check(/com\.apple\.product-type\.app-extension/.test(pbx), 'widget extension target in project.pbxproj');
    check(/IPHONEOS_DEPLOYMENT_TARGET = 16\.4;/.test(pbx), 'deployment target 16.4');
    const podfileProps = path.join(app, 'ios', 'Podfile.properties.json');
    if (fs.existsSync(podfileProps)) check(!/"ios\.deploymentTarget"\s*:\s*"(?!16\.4)/.test(fs.readFileSync(podfileProps, 'utf8')), 'Podfile deployment target 16.4');
  }
} catch (e) {
  failures.push(`prebuild failed: ${e instanceof Error ? e.message : String(e)}`);
  console.error(failures.at(-1));
} finally {
  if (process.env.WY_KEEP_PREBUILD === '1') console.log(`kept ${app}`);
  else fs.rmSync(work, { recursive: true, force: true });
}

if (failures.length) {
  console.error(`prebuild-check ${platform}: ${failures.length} failure(s)`);
  process.exit(1);
}
console.log(`prebuild-check ${platform}: ok`);
