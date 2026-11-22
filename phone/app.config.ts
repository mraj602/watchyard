// Expo app config (docs/spec/phone.md P13). The self-hosted build: pushes arrive over the app's
// own link to the hub (push.md 13-14), with no Firebase, no Google push service and no EAS
// project. No proprietary Google code either (Firebase, Play services, ML Kit): notifications and
// the QR scanner are the app's own modules (modules/wy-notify, modules/wy-scan), and
// scripts/check-no-google.mjs fails the build on any of them. Reads only non-secret environment values:
//   WY_APP_ID                bundle id and package name (default io.github.mraj602.watchyard,
//                            or io.github.mraj602.watchyard.dev for a development build)
//   WY_APP_VARIANT           "development" allows cleartext traffic (loopback hubs, P2)
// Never put a credential here (phone.md P13).
import type { ConfigContext, ExpoConfig } from 'expo/config';
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const DEV = process.env.WY_APP_VARIANT === 'development';
/** The published app's id never changes (a new id is a new app); a development build gets its own, so both can be installed. */
export const RELEASE_APP_ID = 'io.github.mraj602.watchyard';
const APP_ID = process.env.WY_APP_ID || (DEV ? `${RELEASE_APP_ID}.dev` : RELEASE_APP_ID);
const GROUP_ID = `group.${APP_ID}`;
/** The app version comes from package.json, so a release bumps it in one place. */
const VERSION: string = JSON.parse(readFileSync(join(__dirname, 'package.json'), 'utf8')).version;
/** The brand colours of the icon (phone/assets/src/icon.svg) and the board (webui base.css). */
export const BRAND_INK = '#17191B';
export const BRAND_ACCENT = '#E8B04B';

/** Android versionCode (the updater's `build`, P16.1): MAJOR*10000 + MINOR*100 + PATCH of VERSION, so it only rises with the version. */
export const VERSION_CODE = (() => {
  const [major = 0, minor = 0, patch = 0] = VERSION.split('.').map(Number);
  return major * 10000 + minor * 100 + patch;
})();

// The config also works without the images in phone/assets/.
const asset = (rel: string): string | undefined =>
  existsSync(join(__dirname, rel)) ? `./${rel}` : undefined;

/** Widgets of P11, shared by expo-widgets (iOS) and react-native-android-widget (Android). */
export const IOS_WIDGETS = [
  {
    name: 'NeedsYouSmall',
    displayName: 'Needs you',
    description: 'Sessions that need you and the next limit reset.',
    supportedFamilies: ['systemSmall'],
    contentMarginsDisabled: false,
    ios: { supportedFamilies: ['systemSmall'] },
    android: null,
  },
  {
    name: 'AccountsMedium',
    displayName: 'Accounts',
    description: 'Usage rings and reset times of up to four accounts.',
    supportedFamilies: ['systemMedium'],
    contentMarginsDisabled: false,
    ios: { supportedFamilies: ['systemMedium'] },
    android: null,
  },
] as const;

export const ANDROID_WIDGETS = [
  {
    name: 'WySessions',
    label: 'Watchyard: sessions',
    description: 'Running, needs input and done, and what needs you',
    minWidth: '180dp',
    minHeight: '70dp',
    targetCellWidth: 3,
    targetCellHeight: 2,
    resizeMode: 'horizontal|vertical',
    updatePeriodMillis: 1800000,
    ...(asset('assets/widget-previews/wy-sessions.png') ? { previewImage: './assets/widget-previews/wy-sessions.png' } : {}),
  },
  {
    name: 'WyLimits',
    label: 'Watchyard: limits',
    description: 'Every account: the tighter of 5 h and 7 d, and when it resets',
    minWidth: '250dp',
    minHeight: '110dp',
    targetCellWidth: 4,
    targetCellHeight: 3,
    resizeMode: 'horizontal|vertical',
    updatePeriodMillis: 1800000,
    ...(asset('assets/widget-previews/wy-limits.png') ? { previewImage: './assets/widget-previews/wy-limits.png' } : {}),
  },
  {
    name: 'WyNeeds',
    label: 'Watchyard: needs you',
    description: 'The sessions waiting on you, oldest first; tap one to open it',
    minWidth: '250dp',
    minHeight: '110dp',
    targetCellWidth: 4,
    targetCellHeight: 2,
    resizeMode: 'horizontal|vertical',
    updatePeriodMillis: 1800000,
    ...(asset('assets/widget-previews/wy-needs.png') ? { previewImage: './assets/widget-previews/wy-needs.png' } : {}),
  },
  {
    name: 'WyRing',
    label: 'Watchyard: account ring',
    description: 'One account: the tighter of 5 h and 7 d as a ring, and when it resets',
    minWidth: '110dp',
    minHeight: '130dp',
    targetCellWidth: 2,
    targetCellHeight: 2,
    resizeMode: 'none',
    // Android asks which account when it is placed, and again from Reconfigure.
    widgetFeatures: 'reconfigurable',
    updatePeriodMillis: 1800000,
    ...(asset('assets/widget-previews/wy-ring.png') ? { previewImage: './assets/widget-previews/wy-ring.png' } : {}),
  },
  {
    name: 'WyNextReset',
    label: 'Watchyard: next reset',
    description: 'The soonest limit reset across your accounts',
    minWidth: '110dp',
    minHeight: '50dp',
    targetCellWidth: 2,
    targetCellHeight: 1,
    resizeMode: 'horizontal',
    updatePeriodMillis: 1800000,
    ...(asset('assets/widget-previews/wy-next-reset.png') ? { previewImage: './assets/widget-previews/wy-next-reset.png' } : {}),
  },
  {
    name: 'WyMachines',
    label: 'Watchyard: machines',
    description: 'Each machine online or silent, and how many sessions are live',
    minWidth: '250dp',
    minHeight: '40dp',
    targetCellWidth: 4,
    targetCellHeight: 1,
    resizeMode: 'horizontal|vertical',
    updatePeriodMillis: 1800000,
    ...(asset('assets/widget-previews/wy-machines.png') ? { previewImage: './assets/widget-previews/wy-machines.png' } : {}),
  },
] as const;

export default ({ config }: ConfigContext): ExpoConfig => {
  const icon = asset('assets/icon.png');
  const foreground = asset('assets/adaptive-foreground.png');
  const monochrome = asset('assets/adaptive-monochrome.png');

  return {
    ...config,
    name: 'Watchyard',
    slug: 'watchyard',
    version: VERSION,
    scheme: 'watchyard',
    orientation: 'portrait',
    userInterfaceStyle: 'automatic',
    ...(icon ? { icon } : {}),
    ios: {
      bundleIdentifier: APP_ID,
      deploymentTarget: '16.4',
      supportsTablet: false,
      infoPlist: { ITSAppUsesNonExemptEncryption: false },
    },
    android: {
      package: APP_ID,
      versionCode: VERSION_CODE,
      // The launcher icon (phone/assets/src/*.svg, scripts/gen-icons.sh): the gold mark on ink,
      // plus the Android 13 themed-icon layer. The splash reuses the foreground (with-wy-native.js).
      ...(foreground
        ? { adaptiveIcon: { foregroundImage: foreground, backgroundColor: BRAND_INK, ...(monochrome ? { monochromeImage: monochrome } : {}) } }
        : {}),
      // Never back up the app's data (the keychain token, P3); with-wy-native.js also sets it and
      // adds dataExtractionRules and fullBackupContent that exclude every domain from cloud
      // backup and device transfer.
      allowBackup: false,
      // Listed explicitly so the prebuilt manifest shows them (modules/wy-notify and wy-scan also
      // merge them at Gradle time); RECORD_AUDIO is for claude.ai's mic in a tab (wy-tabs, P17.5).
      permissions: ['android.permission.POST_NOTIFICATIONS', 'android.permission.CAMERA', 'android.permission.RECORD_AUDIO'],
      blockedPermissions: [
        'android.permission.SYSTEM_ALERT_WINDOW',
        'android.permission.READ_EXTERNAL_STORAGE',
        'android.permission.WRITE_EXTERNAL_STORAGE',
      ],
    },
    extra: {
      appGroup: GROUP_ID,
      // WY_APP_VARIANT=development: the pair screen also accepts http:// loopback / LAN hubs in a
      // bundled (non-__DEV__) build, matching usesCleartextTraffic above (phone.md P2).
      cleartext: DEV,
    },
    plugins: [
      'expo-router',
      ['expo-secure-store', { faceIDPermission: false, configureAndroidBackup: false }],
      [
        'expo-build-properties',
        {
          // 31: Keystore key agreement (push.md 14.2).
          android: { minSdkVersion: 31, usesCleartextTraffic: DEV },
        },
      ],
      [
        'expo-widgets',
        {
          groupIdentifier: GROUP_ID,
          // The widget extension's bundle id (register it with Apple next to WY_APP_ID).
          bundleIdentifier: `${APP_ID}.widgets`,
          enablePushNotifications: false,
          enableAndroid: false,
          widgets: IOS_WIDGETS,
        },
      ],
      // Order matters: Expo runs mods of the last-listed plugin first, and with-wy-native.js patches
      // the widget provider classes that react-native-android-widget writes, so it is listed first.
      ['./plugins/with-wy-native.js', { development: DEV }],
      ['react-native-android-widget', { widgets: ANDROID_WIDGETS }],
    ],
    experiments: { typedRoutes: false },
  };
};
