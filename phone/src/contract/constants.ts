// Names and limits shared by the app, its tasks, its widgets and the hub (docs/spec/phone.md
// and push.md 12). A change here is a contract change.

import type { AndroidChannelId } from './payload';

/** SecureStore key of the paired hub: {origin, token, device, name, paired_at} (P3). */
export const SECURE_HUB_KEY = 'wy.hub.v1';

/**
 * SecureStore key of unpairings the hub has not confirmed yet (P3): a JSON list of
 * `{origin, token}`, retried with DELETE /v1/devices/self until the hub answers 2xx, 401 or 404.
 */
export const SECURE_REVOKE_KEY = 'wy.revoke.v1';

/**
 * AsyncStorage key of the notification actions already carried out (P6.4): the
 * "<notification id>|<action>" keys of the last few, so two JS runtimes never both act on one
 * tap. Never a token.
 */
export const ACTED_KEY = 'wy.acted.v1';

/** AsyncStorage key of the last good replies (P5). Never a token or secret. */
export const CACHE_KEY = 'wy.cache.v1';

/** AsyncStorage key of the last WidgetSnapshot for Android widgets (P11). */
export const WIDGET_KEY = 'wy.widget.v1';

/** AsyncStorage key of the account each Account-ring widget shows: {[widgetId]: seatId} (P11; ids only, never a secret). */
export const WIDGET_RING_KEY = 'wy.widget.ring.v1';

/** AsyncStorage key of the window ("5 h" or "7 d") each Limits and ring widget shows: {[widgetId]: window} (P11). */
export const WIDGET_WINDOW_KEY = 'wy.widget.window.v1';

/**
 * AsyncStorage key of the app's own settings (P12): `{auto_countdown: boolean}`. Local to this
 * install, not a hub pref; never a token or secret.
 */
export const LOCAL_PREFS_KEY = 'wy.local.v1';

/**
 * AsyncStorage key of what each claude.ai tab showed when it was last open (P17.7):
 * `{<seat>: {signed_in, account, at}}`, `account` being the Anthropic account uuid claude.ai
 * named in the tab. Never a cookie, an email or page content; local to this install, never sent.
 */
export const TAB_MEMO_KEY = 'wy.tabs.v1';

/** Notification category with Open, Snooze and Seen (P6.2). No '-' or ':' (an Expo rule). */
export const ALERT_CATEGORY = 'wy_alert';

/** Action identifiers of ALERT_CATEGORY. */
export const ACTION_OPEN = 'open';
export const ACTION_SNOOZE = 'snooze';
export const ACTION_SEEN = 'seen';

/** Android channels (P6.1), in creation order. Importance names are the keys of NotificationManager's IMPORTANCE_* (modules/wy-notify AndroidImportance). */
export const ANDROID_CHANNELS: readonly {
  id: AndroidChannelId;
  name: string;
  importance: 'HIGH' | 'DEFAULT' | 'LOW';
}[] = [
  { id: 'wy_needs_you', name: 'Needs you', importance: 'HIGH' },
  { id: 'wy_limits', name: 'Limits', importance: 'DEFAULT' },
  { id: 'wy_machines', name: 'Machines', importance: 'LOW' },
  { id: 'wy_usage', name: 'Usage changes', importance: 'LOW' },
  { id: 'wy_test', name: 'Test', importance: 'DEFAULT' },
];

/** Foreground poll period (P5). */
export const POLL_MS = 15_000;

/** Hub request timeout, and the /health probe's (P3). */
export const REQUEST_TIMEOUT_MS = 10_000;
export const HEALTH_TIMEOUT_MS = 1_500;
/**
 * Pairing's /health probe: the first contact with a hub, often on a cold or busy phone, where a
 * hub that answers in 2-3 s must not read as unreachable. Route checks keep HEALTH_TIMEOUT_MS.
 */
export const PAIR_PROBE_TIMEOUT_MS = 6_000;

/** Action tokens (push.md 8; the same check as sw-lib.js actionToken). */
export const ACTION_TOKEN = /^a1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/;

/** expo-secure-store keychainAccessible for SECURE_HUB_KEY (P3). */
export const KEYCHAIN_ACCESSIBLE = 'AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY';

/** Device kind the app claims as (P2). */
export const DEVICE_KIND = 'phone';
