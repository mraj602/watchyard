import type { Alert } from '../../contract/api';
import type { AlertBrief } from '../../contract/api';
import { canSnoozeRow, reasonText, recentAlerts, RULE_GROUPS, seenTarget } from './alerts';

describe('reasonText', () => {
  it('labels the peers skip reason', () => {
    expect(reasonText('peers')).toBe('talking to another session (peers)');
  });
  it('falls back to spaced words and empty', () => {
    expect(reasonText('rule_off')).toBe('rule is off');
    expect(reasonText('some_new_reason')).toBe('some new reason');
    expect(reasonText(null)).toBe('');
  });
});

describe('RULE_GROUPS usage notices', () => {
  it('the four usage notices share one row', () => {
    const g = RULE_GROUPS.find((x) => x.id === 'usage');
    expect(g?.label).toBe('Usage and limit changes');
    expect(g?.triggers).toEqual(['limit_change', 'usage_reset', 'seat_change', 'weekly_drop']);
    expect(RULE_GROUPS.filter((x) => x.triggers.includes('limit_change'))).toHaveLength(1);
  });
  it('the switch notices share their own row', () => {
    const g = RULE_GROUPS.find((x) => x.id === 'switch');
    expect(g?.triggers).toEqual(['session_moved', 'switch_digest']);
  });
});

describe('recentAlerts', () => {
  const now = Date.parse('2026-11-20T12:00:00Z');
  const a = (id: string, status: Alert['status'], at: string): Alert => ({ id, status, created_at: at });
  it('keeps the last 48 h and anything open, drops older history', () => {
    const list = [
      a('new', 'seen', '2026-11-20T08:00:00Z'),
      a('old', 'seen', '2026-11-07T08:00:00Z'),
      a('open', 'snoozed', '2026-11-07T08:00:00Z'),
      a('oldfired', 'cancelled', '2026-11-01T00:00:00Z'),
    ];
    expect(recentAlerts(list, now).map((x) => x.id)).toEqual(['new', 'open']);
  });
});

describe('seenTarget and canSnoozeRow (push.md 4)', () => {
  const skipped = (id: string, reason: string): AlertBrief => ({ id, status: 'skipped', reason } as AlertBrief);
  // The three Needs-you cards seen live on 1.1.0 with neither Seen nor the menu.
  const live = [
    { id: 's-stale-asked', needs_you: true, alert: skipped('al_a', 'stale') },
    { id: 's-stale-perm', needs_you: true, alert: skipped('al_b', 'stale') },
    { id: 's-peers', needs_you: true, alert: skipped('al_c', 'peers') },
  ];
  it.each(live)('$id: Seen on its skipped alert, no Snooze', (s) => {
    expect(seenTarget(s)).toEqual({ alert: s.alert.id });
    expect(canSnoozeRow(s)).toBe(false);
  });
  it('a Needs-you row with no alert is seen through the session', () => {
    expect(seenTarget({ id: 's1', needs_you: true, alert: null })).toEqual({ session: 's1' });
    expect(canSnoozeRow({ alert: null })).toBe(false);
  });
  it('alerts that can push get both; seen rows and rows that do not need you get none', () => {
    for (const status of ['pending', 'fired', 'snoozed'] as const) {
      const s = { id: 's1', needs_you: true, alert: { id: 'al_1', status } as AlertBrief };
      expect(seenTarget(s)).toEqual({ alert: 'al_1' });
      expect(canSnoozeRow(s)).toBe(true);
    }
    expect(seenTarget({ id: 's1', needs_you: true, alert: { id: 'al_1', status: 'seen' } as AlertBrief })).toBeNull();
    expect(seenTarget({ id: 's1', needs_you: false, alert: skipped('al_1', 'stale') })).toBeNull();
    expect(seenTarget({ id: 's1', needs_you: false, alert: null })).toBeNull();
  });
});
