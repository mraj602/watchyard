// The push payload (push.md 6.5): exactly the Go model.Payload JSON, the same object the
// service worker reads, minus `remote_url` when `private` is true. Plus the widget refresh
// payload the hub sends over the link (push.md 12.9).

import type { TapMode, Trigger } from './api';

export const PAYLOAD_VERSION = 1;

export type PayloadType = 'alert' | 'summary' | 'test';

/** Action tokens (push.md 8); absent for tests and summaries. */
export interface PayloadActions {
  snooze?: string;
  seen?: string;
}

/**
 * model.Payload. Key order of the Go encoder: v, type, alert, alerts, trigger, title, body, tag,
 * url, remote_url, tap, session, needs_you, actions, private, ts. Optional keys are omitted
 * (Go omitempty), never null.
 */
export interface Payload {
  v: 1;
  type: PayloadType;
  alert?: string;
  alerts?: string[];
  trigger?: Trigger;
  /** The Android channel id (P6.1) and the category it belongs to; hubs before categories omit both. */
  channel?: string;
  category?: string;
  title: string;
  body: string;
  tag: string;
  url: string;
  remote_url?: string;
  tap: TapMode;
  session?: string;
  needs_you: number;
  actions?: PayloadActions;
  private: boolean;
  ts: string | null;
}

/** Keys of Payload in Go's order (the contract test checks goldens against it). */
export const PAYLOAD_KEYS = [
  'v',
  'type',
  'alert',
  'alerts',
  'trigger',
  'title',
  'body',
  'tag',
  'url',
  'remote_url',
  'tap',
  'session',
  'needs_you',
  'actions',
  'private',
  'ts',
  'channel',
  'category',
] as const;

/** Payload keys whose place in Go's order is not pinned (`channel`, `category`): they may sit anywhere. */
export const PAYLOAD_FLOATING_KEYS = ['channel', 'category'] as const;

/** The widget refresh payload (push.md 12.9): never shown, never an alert. */
export interface RefreshData {
  v: 1;
  type: 'refresh';
  needs_you: number;
  ts: string;
}

/** Android channels (phone.md P6.1). */
export type AndroidChannelId =
  | 'wy_needs_you'
  | 'wy_limits'
  | 'wy_switch'
  | 'wy_drain'
  | 'wy_usage'
  | 'wy_machines'
  | 'wy_test';
