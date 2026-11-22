import type { Alert } from '../../contract/api';
import { reasonText, recentAlerts, RULE_GROUPS } from './alerts';

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
