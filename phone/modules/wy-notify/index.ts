// The wy-notify module's JS API (docs/spec/phone.md P6): channels, the notification permission
// and its prompt, the badge and dismissals, in the shape src/lib/notify's `NotificationsLike`
// takes (the names expo-notifications used, which this module replaces). Android only: where the
// native module is missing (iOS, Expo Go) the permission reads "undetermined" and the rest does
// nothing.
//
// There are no response listeners: the notifications are posted by wy-link, natively, and a tap
// or Open carries its own watchyard:// intent to the app (P6.4, "Native taps"); Snooze and Seen
// run in wy-link's ActionReceiver. So no notification response ever reaches JS on Android.
import { requireOptionalNativeModule } from 'expo';
import { Platform } from 'react-native';
import { ANDROID_CHANNELS } from '../../src/contract/constants';

type Permission = { granted: boolean; status: 'granted' | 'denied' | 'undetermined'; canAskAgain: boolean };

type Native = {
  setChannel(id: string, name: string, importance: number): Promise<void>;
  openChannelSettings(channelId: string): Promise<void>;
  getPermission(): Promise<Permission>;
  requestPermission(): Promise<Permission>;
  setBadgeCount(n: number): Promise<boolean>;
  dismiss(identifier: string): Promise<void>;
  dismissAll(): Promise<void>;
};

const native: Native | null = Platform.OS === 'android' ? requireOptionalNativeModule<Native>('WyNotify') : null;

/** True where the module exists (an Android build that includes it). */
export const available = native !== null;

/** Android's NotificationManager.IMPORTANCE_* values. */
export const AndroidImportance = { NONE: 0, MIN: 1, LOW: 2, DEFAULT: 3, HIGH: 4, MAX: 5 } as const;

const UNKNOWN: Permission = { granted: false, status: 'undetermined', canAskAgain: true };

/** Creates (or renames) an Android channel (P6.1). */
export async function setNotificationChannelAsync(id: string, channel: { name: string; importance: number }): Promise<null> {
  await native?.setChannel(id, channel.name, channel.importance);
  return null;
}

/**
 * Opens Android's settings page for one of the P6.1 channels (sound, importance), creating the
 * channel first when it does not exist yet. False where there is no native module (iOS, Expo Go).
 */
export async function openChannelSettingsAsync(id: string): Promise<boolean> {
  if (!native) return false;
  const ch = ANDROID_CHANNELS.find((c) => c.id === id);
  if (ch) await native.setChannel(ch.id, ch.name, AndroidImportance[ch.importance]);
  await native.openChannelSettings(id);
  return true;
}

/**
 * Android has no categories: the Open, Snooze and Seen buttons are wy-link's own (with the
 * hub's snooze_min, `setSnoozeMinutes`). Kept so src/lib/notify stays platform-neutral.
 */
export async function setNotificationCategoryAsync(): Promise<null> {
  return null;
}

export async function getPermissionsAsync(): Promise<Permission> {
  return native ? native.getPermission() : UNKNOWN;
}

/** Android 13+: the system prompt (once it was refused for good, the answer without asking). */
export async function requestPermissionsAsync(): Promise<Permission> {
  return native ? native.requestPermission() : UNKNOWN;
}

/** The launcher badge (P6.6); false where the launcher has none. */
export async function setBadgeCountAsync(n: number): Promise<boolean> {
  return native ? native.setBadgeCount(n) : false;
}

export async function dismissNotificationAsync(identifier: string): Promise<void> {
  await native?.dismiss(identifier);
}

export async function dismissAllNotificationsAsync(): Promise<void> {
  await native?.dismissAll();
}

/** Always null: no notification response reaches JS (see the header). */
export async function getLastNotificationResponseAsync(): Promise<null> {
  return null;
}
