// Alert status text and the Seen / Snooze controls shared by Now, Session and Alerts.

import { h } from './dom.js';
import { toast } from './ui.js';
import { clock } from '../format.js';
import { reasonText, seenTarget, canSnoozeRow } from './alerts-fmt.js';
import { store, markSeen, markSessionSeen, snooze, canPush } from '../store.js';

export { reasonText };

// Seen on a row: the alert's own Seen, or the session's for a Needs-you row with no alert.
export function seenRow(s) {
  const t = seenTarget(s);
  if (!t) return Promise.resolve(null);
  return t.alert ? markSeen(t.alert, s.id) : markSessionSeen(s.id);
}

// "notifies in 40 s" counts down; the app refreshes [data-due] every few seconds.
export function untilShort(ts, now = Date.now()) {
  const d = Math.round((Date.parse(ts) - now) / 1000);
  if (Number.isNaN(d)) return '';
  if (d <= 0) return 'now';
  if (d < 90) return `in ${d} s`;
  return `in ${Math.round(d / 60)} min`;
}

// "Notifies in 19 s", then "Notifying now…" once it is due.
export function pendingText(due, now = Date.now()) {
  if (!due) return 'notification waiting';
  const u = untilShort(due, now);
  return !u || u === 'now' ? 'notifying now…' : `notifies ${u}`;
}

// One line for a session's alert brief, or ''.
export function alertText(a, now = Date.now()) {
  if (!a) return '';
  switch (a.status) {
    case 'pending': return pendingText(a.due_at, now);
    case 'fired': return `notified ${clock(a.fired_at, now)}`;
    case 'snoozed': return `snoozed until ${clock(a.snoozed_until, now)}`;
    case 'seen': return a.seen_at ? `seen ${clock(a.seen_at, now)}` : 'seen';
    case 'skipped': return `not notified · ${reasonText(a.reason) || 'skipped'}`;
    case 'cancelled': return `cancelled · ${reasonText(a.reason) || 'answered'}`;
    default: return a.status;
  }
}

// The text element; `data-due` lets the app keep a pending countdown fresh.
export function alertLine(a, now) {
  if (!a) return null;
  return h('span', { class: `alert-line alert-${a.status}`, 'data-due': a.status === 'pending' && a.due_at ? a.due_at : undefined }, alertText(a, now));
}

const MIN = 15;

// Seen and Snooze buttons. Hidden unless the caller may push; Seen for every row seenTarget
// allows (a skipped alert, or no alert, on a Needs-you row too), Snooze only for an alert that
// can still push; disabled while offline.
export function alertButtons(s, { size = '' } = {}) {
  if (!canPush()) return null;
  const canSeen = !!seenTarget(s);
  const canSnooze = canSnoozeRow(s);
  if (!canSeen && !canSnooze) return null;
  const off = !!store.offline;
  const run = (fn) => async (e) => {
    const b = e.currentTarget;
    b.disabled = true;
    try { await fn(); } catch (ex) { toast(ex.status === 409 ? 'That alert already changed' : ex.message); b.disabled = false; }
  };
  return h('div', { class: 'alert-actions' },
    canSeen ? h('button', { type: 'button', class: `btn ${size}`, 'data-key': `seen-${s.id}`, disabled: off, onclick: run(() => seenRow(s)) }, 'Seen') : null,
    canSnooze ? h('button', { type: 'button', class: `btn ${size}`, 'data-key': `snooze-${s.id}`, disabled: off, onclick: run(() => snooze(s.alert.id, s.id)) }, `Snooze ${MIN} min`) : null);
}

// The row has Seen or Snooze now (the ⋯ menu and the swipe).
export const canAct = (s) => canPush() && (!!seenTarget(s) || canSnoozeRow(s)) && !store.offline;
