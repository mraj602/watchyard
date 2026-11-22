import { reasonText, RULE_GROUPS } from './alerts';

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
