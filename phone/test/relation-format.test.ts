// The sentences and bars of "Usage and the picker" (src/lib/format/relation.ts; docs/spec/pwa.md 3.0a).
import type { Calibration, CalibrationFactor, RelationDay, RelationModel, Verdict } from '../src/contract/api';
import {
  calibrationRestart, calibrationRows, capacityBars, capacityLine, factorSource, heavyNote, legend, modelInitial, num, ordinal, ratioBars,
  relationStale, skipWords, verdictAge, verdictLine,
} from '../src/lib/format/relation';

const NOW = Date.parse('2026-11-12T12:00:00Z'); // a Thursday
const iso = (ms: number) => new Date(ms).toISOString();
const verdict = (over: Partial<Verdict> = {}): Verdict => ({ at: iso(NOW - 12 * 60e3), machine: 'm1', machine_name: 'gpu-box', strategy: 'lanes', eligible: true, ...over });

describe('verdictLine', () => {
  it('rank 1 is next in line, with the kind', () => {
    expect(verdictLine(verdict({ rank: 1, kind: 'aggressive' }), NOW, 'UTC')).toBe('Eligible · next in line · aggressive');
  });
  it('a later rank is an ordinal, no kind when null', () => {
    expect(verdictLine(verdict({ rank: 3, kind: null }), NOW, 'UTC')).toBe('Eligible · 3rd in line');
    expect(verdictLine(verdict({ rank: 12 }), NOW, 'UTC')).toBe('Eligible · 12th in line');
    expect(verdictLine(verdict({ rank: null }), NOW, 'UTC')).toBe('Eligible');
  });
  it('not picked names the reason, the skip level and when it frees up', () => {
    const v = verdict({ eligible: false, reason: '7d 92% >= pace 85', skip_level: 2, free_at: '2026-11-13T14:00:00Z' });
    expect(verdictLine(v, NOW, 'UTC')).toBe('Not picked: 7d 92% >= pace 85 (protected rule) · frees up Fri 14:00');
  });
  it('leaves out what is missing', () => {
    expect(verdictLine(verdict({ eligible: false, reason: 'stale', skip_level: null, free_at: null }), NOW, 'UTC')).toBe('Not picked: stale');
    expect(verdictLine(verdict({ eligible: false, reason: null, skip_level: 0 }), NOW, 'UTC')).toBe('Not picked: burst cap');
  });
  it('weekly room expiring, and next_free when another account frees up first', () => {
    expect(verdictLine(verdict({ rank: 2, expiring: true, kind: 'protected' }), NOW, 'UTC')).toBe('Eligible · 2nd in line · weekly room expiring · protected');
    const v = verdict({ eligible: false, reason: null, skip_level: 3, free_at: '2026-11-14T09:00:00Z', next_free: '2026-11-12T13:30:00Z' });
    expect(verdictLine(v, NOW, 'UTC')).toBe('Not picked: limit · frees up Sat 09:00 · no account was free, the first frees up 13:30');
    expect(verdictLine({ ...v, next_free: v.free_at }, NOW, 'UTC')).toBe('Not picked: limit · frees up Sat 09:00');
  });
  it('none: says nothing has reported', () => {
    expect(verdictLine(null, NOW)).toBe('No verdict yet: `wy account next` has not reported this account.');
  });
  it('skip levels in words', () => {
    expect([0, 1, 2, 3, 4, null].map(skipWords)).toEqual(['burst cap', 'stale', 'protected rule', 'limit', '', '']);
  });
  it('ordinals', () => {
    expect([1, 2, 3, 4, 11, 12, 13, 21, 22, 23, 101, 111].map(ordinal)).toEqual(['1st', '2nd', '3rd', '4th', '11th', '12th', '13th', '21st', '22nd', '23rd', '101st', '111th']);
  });
});

describe('verdictAge', () => {
  it('minutes and machine', () => expect(verdictAge(verdict(), NOW)).toBe('as of 12 min ago on gpu-box'));
  it('falls back to the machine id, and says just now', () => {
    expect(verdictAge(verdict({ machine_name: null, at: iso(NOW - 5e3) }), NOW)).toBe('as of just now on m1');
  });
  it('past an hour it says older than an hour', () => {
    expect(verdictAge(verdict({ at: iso(NOW - 61 * 60e3) }), NOW)).toBe('older than an hour, from gpu-box');
    expect(verdictAge(verdict({ at: iso(NOW - 60 * 60e3) }), NOW)).toBe('as of 1 h ago on gpu-box');
  });
  it('nothing without a verdict', () => expect(verdictAge(null, NOW)).toBe(''));
});

const factor = (over: Partial<CalibrationFactor> = {}): CalibrationFactor => ({ value: 2.1, fallback: 2, measured: 2.2, weight: 1, n: 12, days: 9, implausible: false, ...over });
const cal = (over: Partial<Calibration> = {}): Calibration => ({
  at: iso(NOW), since: iso(NOW - 9 * 86400e3), reset: null, f: factor(), ratio: factor({ value: 6.5, weight: 0.4, n: 5, days: 3 }),
  lane_price: factor({ value: 4.25, weight: 0, n: 0, days: 0, measured: null }), lane_charge: 8.5, lane_units: 1e6, unexplained_pct_per_hour: null, unexplained_hours: 0, ...over,
});

describe('calibration', () => {
  it('weights read as measured, blending or fallback', () => {
    expect(factorSource(factor({ weight: 1 }))).toBe('measured');
    expect(factorSource(factor({ weight: 0.4 }))).toBe('blending, 40% measured');
    expect(factorSource(factor({ weight: 0 }))).toBe('fallback');
  });
  it('one row each for f, ρ and the lane charge', () => {
    const rows = calibrationRows(cal());
    expect(rows.map((r) => r.text)).toEqual([
      'Meter factor f 2.1 points per million units · measured · n 12 · 9 days',
      'Ratio 5 h : 7 d 6.5 5 h points per 7 d point · blending, 40% measured · n 5 · 3 days',
      'Lane charge c 8.5 points (lane price 4.25) · fallback · n 0 · 0 days',
    ]);
  });
  it('says so when a measurement is implausible', () => {
    const rows = calibrationRows(cal({ f: factor({ implausible: true, weight: 0 }) }));
    expect(rows[0]!.implausible).toBe(true);
    expect(rows[0]!.text).toContain('· implausible measurement, not used');
    expect(rows[1]!.implausible).toBe(false);
  });
  it('none: no rows', () => expect(calibrationRows(null)).toEqual([]));
  it('the restart, with its reason', () => {
    expect(calibrationRestart(cal(), NOW, 'UTC')).toBe('');
    expect(calibrationRestart(cal({ reset: { at: '2026-11-10T09:30:00Z', reason: 'under_read' } }), NOW, 'UTC')).toBe('Restarted Tue 09:30: the meter read low');
    expect(calibrationRestart(cal({ reset: { at: '2026-11-12T09:30:00Z', reason: 'seat_change' } }), NOW, 'UTC')).toBe('Restarted 09:30: the account changed');
    expect(calibrationRestart(null, NOW)).toBe('');
  });
  it('numbers keep three significant digits', () => {
    expect([7.1, 0.042, 12.34, 1234.5, 0, null].map(num)).toEqual(['7.1', '0.042', '12.3', '1,235', '0', '–']);
  });
});

const day = (d: string, ratio: number | null, model: string | null, pairs = 30): RelationDay => ({ day: d, ratio, d5: 1, d7: 1, pairs, model, rise: 0, rise_cc: 0 });

describe('ratio bars', () => {
  const days = [day('2026-11-11', 3.5, 'Sonnet 4.5', 1), day('2026-11-12', 7.1, 'Opus 4.6'), day('2026-11-13', null, null, 0)];
  it('sizes against the tallest, null is a gap, with initials and tap labels', () => {
    const bars = ratioBars(days);
    expect(bars.map((b) => b.pct)).toEqual([49, 100, 0]);
    expect(bars.map((b) => b.value)).toEqual([3.5, 7.1, null]);
    expect(bars.map((b) => b.initial)).toEqual(['S', 'O', '']);
    expect(bars[1]!.label).toBe('12 Nov: 7.1 · Opus 4.6 · 30 pairs');
    expect(bars[0]!.label).toBe('11 Nov: 3.5 · Sonnet 4.5 · 1 pair');
    expect(bars[2]!.label).toBe('13 Nov: no ratio');
  });
  it('a day with pairs but no ratio says why', () => expect(ratioBars([day('2026-11-11', null, 'Opus 4.6', 2)])[0]!.label).toBe('11 Nov: under 2 weekly points · 2 pairs'));
  it('all gaps give empty bars', () => expect(ratioBars([day('2026-11-11', null, null, 0)]).map((b) => b.pct)).toEqual([0]));
  it('initials: the first letter of the label', () => {
    expect(['Opus 4.6', 'Sonnet 4.5', 'Haiku 4.5', 'Fable 5', 'Mythos', 'mystery-9', '', null].map(modelInitial)).toEqual(['O', 'S', 'H', 'F', 'M', 'M', '', '']);
  });
  it('the legend lists the models present once, in order', () => {
    expect(legend([...days, day('2026-11-14', 2, 'Opus 4.6')])).toBe('S Sonnet 4.5 · O Opus 4.6');
    expect(legend([day('2026-11-14', null, null)])).toBe('');
  });
});

const model = (over: Partial<RelationModel> = {}): RelationModel => ({
  model: 'Opus 4.6', this_week: 0.042, last_week: 0.04, change: 1.05,
  days: [
    { day: '2026-11-11', index: 0.04, normalised: 1, d5: 6, usd: 0.24 },
    { day: '2026-11-12', index: 0.05, normalised: 1.25, d5: 8, usd: 0.4 },
    { day: '2026-11-13', index: null, normalised: null, d5: 1, usd: 0.04 },
  ], ...over,
});

describe('capacity', () => {
  it('the per-model line', () => expect(capacityLine(model())).toBe('Opus 4.6 · $0.042 per 1% this week · 1.05× last week'));
  it('not enough days when a week is missing', () => {
    expect(capacityLine(model({ this_week: null }))).toBe('Opus 4.6 · not enough days');
    expect(capacityLine(model({ last_week: null, change: null }))).toBe('Opus 4.6 · $0.042 per 1% this week · last week: not enough days');
  });
  it('bars reach 1.0, with the reference line where 1.0 sits', () => {
    const { bars, ref } = capacityBars(model());
    expect(bars.map((b) => b.pct)).toEqual([80, 100, 0]);
    expect(ref).toBe(80);
    expect(bars[1]!.label).toBe('12 Nov: $0.050 per 1% · 1.25× last week · 8 points');
    expect(bars[2]!.label).toBe('13 Nov: under 5 points (1)');
  });
  it('a scale under 1.0 still puts the line at the top', () => {
    const { bars, ref } = capacityBars(model({ days: [{ day: '2026-11-11', index: 0.02, normalised: 0.5, d5: 6, usd: 1 }] }));
    expect(ref).toBe(100);
    expect(bars[0]!.pct).toBe(50);
  });
});

describe('heavyNote', () => {
  const tail = 'this index is not a fair measure and does not count toward limit-change alerts';
  it('says how much of the rise had Claude Code tokens', () => {
    expect(heavyNote({ heavy: false, rise: 30, cc_share: 0.62 })).toBe(`62% of the 5-hour rise had Claude Code tokens: ${tail}.`);
  });
  it('no rise at all', () => expect(heavyNote({ heavy: false, rise: 0, cc_share: null })).toBe(`No 5-hour rise this week: ${tail}.`));
  it('too little rise', () => expect(heavyNote({ heavy: false, rise: 12, cc_share: 1 })).toBe(`Only 12 points of 5-hour rise this week: ${tail}.`));
  it('nothing when heavy', () => expect(heavyNote({ heavy: true, rise: 40, cc_share: 0.9 })).toBe(''));
});

describe('relationStale', () => {
  it('loads when never loaded or older than 5 minutes', () => {
    expect(relationStale(null, NOW)).toBe(true);
    expect(relationStale(NOW - 5 * 60e3, NOW)).toBe(false);
    expect(relationStale(NOW - 5 * 60e3 - 1, NOW)).toBe(true);
  });
});
