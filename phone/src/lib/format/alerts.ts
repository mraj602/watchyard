// Alert status text (webui components/alerts.js) and the rule groups of the Alerts screen.
import type { AlertBrief } from '../../contract/api';
import { clock } from './core';

const REASONS: Record<string, string> = {
  remote_control: 'Claude app already notifies', rule_off: 'rule is off', stale: 'too old to push',
  superseded: 'replaced by a later alert', baseline: 'existing at first start', answered: 'you answered first',
  peers: 'talking to another session (peers)', ended: 'session ended', recovered: 'recovered', reset_passed: 'window reset', seen: 'seen',
};
export const reasonText = (r: string | null | undefined): string =>
  (r && REASONS[r]) || (r ? String(r).replace(/_/g, ' ') : '');

/** "in 40 s", "in 3 min", "now". */
export function untilShort(ts: string | null | undefined, now: number): string {
  const d = Math.round((Date.parse(ts ?? '') - now) / 1000);
  if (Number.isNaN(d)) return '';
  if (d <= 0) return 'now';
  if (d < 90) return `in ${d} s`;
  return `in ${Math.round(d / 60)} min`;
}

/** One line for an alert brief, or "". */
export function alertText(a: AlertBrief | null | undefined, now: number, tz?: string): string {
  if (!a) return '';
  switch (a.status) {
    case 'pending': return a.due_at ? `push ${untilShort(a.due_at, now)}` : 'push waiting';
    case 'fired': return `pushed ${clock(a.fired_at, now, tz)}`;
    case 'snoozed': return `snoozed until ${clock(a.snoozed_until, now, tz)}`;
    case 'seen': return a.seen_at ? `seen ${clock(a.seen_at, now, tz)}` : 'seen';
    case 'skipped': return `no push · ${reasonText(a.reason) || 'skipped'}`;
    case 'cancelled': return `cancelled · ${reasonText(a.reason) || 'answered'}`;
    default: return a.status;
  }
}

/** An alert the person can still Seen or Snooze (store.js actionable). */
export const actionable = (a: AlertBrief | null | undefined): boolean =>
  !!a && (a.status === 'pending' || a.status === 'fired' || a.status === 'snoozed');

export const OUTCOME: Record<string, string> = {
  fired: 'open', seen: 'seen', snoozed: 'snoozed', cancelled: 'cancelled', closed: 'closed', pending: 'waiting', skipped: 'skipped',
};

/** Rule groups of the Alerts screen (the phone edits prefs.mute per group). */
export const RULE_GROUPS: readonly { id: string; label: string; triggers: readonly string[]; delay?: boolean }[] = [
  { id: 'needs_permission', label: 'Needs permission', triggers: ['needs_permission'], delay: true },
  { id: 'asked', label: 'Asked a question', triggers: ['asked'], delay: true },
  { id: 'failed', label: 'Failed or rate limited', triggers: ['failed'] },
  { id: 'limits', label: 'Limits at 80% / 95%', triggers: ['limit_80', 'limit_95'] },
  { id: 'reset', label: 'Limit window reset', triggers: ['reset'] },
  { id: 'machine_silent', label: 'Machine silent with live sessions', triggers: ['machine_silent'], delay: true },
  { id: 'usage', label: 'Usage and limit changes', triggers: ['limit_change', 'usage_reset', 'seat_change', 'weekly_drop'] },
];

export function delayText(s: number | null | undefined): string {
  if (!s) return '';
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  return `${Math.round(s / 3600)} h`;
}
