// Labels of the reasons an alert was skipped, cancelled or suppressed (push.md 3). Pure, so
// node can test it (alerts-fmt.test.cjs): no imports.

export const REASONS = {
  remote_control: 'the Claude app already notifies', rule_off: 'rule is off', stale: 'too old to send',
  superseded: 'replaced by a later alert', baseline: 'existing at first start', answered: 'you answered first',
  peers: 'talking to another session (peers)', ended: 'session ended', recovered: 'recovered',
  reset_passed: 'window reset', seen: 'seen',
};

export const reasonText = (r) => REASONS[r] || (r ? String(r).replace(/_/g, ' ') : '');

// The alert can still push (pending, fired, snoozed): Snooze applies, and Seen (push.md 4).
export const actionable = (a) => !!a && (a.status === 'pending' || a.status === 'fired' || a.status === 'snoozed');

// What Seen acts on for a session row (push.md 4), or null when it has no Seen: an alert that
// can still push; a skipped one (stale, rule_off, peers, …) while the session needs you; and a
// Needs-you row with no alert at all, through POST /v1/sessions/{id}/seen. A seen row has none.
export function seenTarget(s) {
  const a = s && s.alert;
  if (actionable(a)) return { alert: a.id };
  if (!s || !s.needs_you) return null;
  if (!a) return { session: s.id };
  return a.status === 'skipped' ? { alert: a.id } : null;
}

// Snooze is only for an alert that can still push.
export const canSnoozeRow = (s) => actionable(s && s.alert);
