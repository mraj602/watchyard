// The sentences, tiles and chart models of "Usage and the picker" (src/lib/format/relation.ts; docs/spec/pwa.md 3.0a).
import type { Calibration, CalibrationFactor, RelationDay, RelationModel, UsageRelation, Verdict } from '../src/contract/api';
import {
  ALERT_NOTE, GAP_MARK, MODEL_COLOURS, NO_RATIO, NO_VERDICT, TAP_HINT, calibrationRestart, capacityModel, heavyNote, howRows, modelOrder, num, ordinal,
  ratioChart, relationStale, sessionLine, tilesModel, usageModel, verdictAge, verdictModel, yTicks,
} from '../src/lib/format/relation';

const NOW = Date.parse('2026-11-12T12:00:00Z'); // a Thursday
const iso = (ms: number) => new Date(ms).toISOString();

const factor = (over: Partial<CalibrationFactor> = {}): CalibrationFactor => ({ value: 2.1, fallback: 2, measured: 2.2, weight: 1, n: 12, days: 9, implausible: false, ...over });

/** The owner's case: measured ratio and meter factor, the session cost still at its default. */
const ownerCal = (): Calibration => ({
  at: iso(NOW), since: null, reset: null,
  f: factor({ value: 1.38, weight: 1, n: 1504, days: 6.7 }),
  ratio: factor({ value: 6.78, weight: 1, n: 1504, days: 6.7 }),
  lane_price: factor({ value: 4, fallback: 4, measured: null, weight: 0, n: 1, days: 0 }),
  lane_charge: 8, lane_units: null, unexplained_pct_per_hour: null, unexplained_hours: 0,
});
const ownerVerdict = (over: Partial<Verdict> = {}): Verdict => ({
  at: iso(NOW - 12 * 60e3), machine: 'm1', machine_name: 'gpu-box', strategy: 'lanes', kind: 'protected', eligible: true, rank: 3,
  weekly_pct: 29, weekly_cap: 52, five_hour_pct: 1, five_hour_cap: 45, burst: 0, burst_cap: 2, mode_from: 'hub', ...over,
});
const days14 = (): RelationDay[] => Array.from({ length: 14 }, (_, i) => {
  const has = i % 3 === 0;
  return { day: iso(Date.UTC(2026, 10, 1 + i)).slice(0, 10), ratio: has ? 5 + (i % 4) : null, d5: has ? 10 : 0, d7: has ? 2 : 0, pairs: has ? 30 : 0, model: has ? 'Opus 5.5' : null, rise: 10, rise_cc: 9 };
});
const model = (name: string, over: Partial<RelationModel> = {}, daysIdx: (number | null)[] = []): RelationModel => ({
  model: name, this_week: null, last_week: null, change: null,
  days: days14().map((d, i) => ({ day: d.day, index: daysIdx[i] ?? null, normalised: null, d5: 6, usd: daysIdx[i] != null ? 0.3 : 0 })), ...over,
});
const owner = (over: Partial<UsageRelation> = {}): UsageRelation => ({
  seat: 's', time: iso(NOW), tz: 'UTC', today: '2026-11-14', days: days14(),
  heavy: { this_week: { heavy: true, rise: 30, cc_share: 0.9 }, last_week: { heavy: true, rise: 30, cc_share: 0.9 } },
  models: [
    model('Opus 5.5', { this_week: 1.67, last_week: null }, [1.5, null, 1.7, 1.67]),
    model('Sonnet 5.5'), model('Haiku 5.5'),
  ],
  calibration: ownerCal(), verdict: ownerVerdict(), ...over,
});

const FORBIDDEN = [/\bf\b/, /\bc\b/, /\bn\b/, /ρ/, /lane/i, /normalised/i, /Tap a bar/];
function strings(u: ReturnType<typeof usageModel>): string[] {
  const out: string[] = [u.session, u.noRatio, u.capacity.alertNote, u.capacity.note];
  if (u.verdict) out.push(u.verdict.headline, u.verdict.place, u.verdict.why, u.verdict.frees, u.verdict.noneFree, u.verdict.kindWords, ...u.verdict.rules.map((r) => r.text));
  if (u.tiles) out.push(...u.tiles.tiles, u.tiles.label);
  for (const c of [u.ratio, ...u.capacity.models.map((m) => m.chart)]) if (c) out.push(c.title, c.higher, c.summary, ...c.legend.map((l) => l.model), ...c.bars.map((b) => b.label));
  for (const m of u.capacity.models) out.push(m.text);
  for (const r of u.how) out.push(r.title, r.text);
  return out.filter(Boolean);
}

describe('the owner\'s case', () => {
  const u = usageModel(owner(), NOW);

  it('verdict: headline, place, kind and the rules against their thresholds', () => {
    const v = u.verdict!;
    expect(v.headline).toBe('Can take a new session: 3rd in line');
    expect(v.place).toBe('3rd in line: 2 other accounts get a new session before this one; they take turns.');
    expect(v.kindName).toBe('Protected');
    expect(v.kindWords).toMatch(/^Kept for you\./);
    expect(v.notChosen).toBe(false);
    expect(v.rules.map((r) => r.text)).toEqual([
      '5-hour 1% (stops at 45%)', 'Week 29%, the even pace allows 52% today', 'New sessions in the last hour: 0 (at most 2)',
    ]);
    expect(v.rules.some((r) => r.blocking)).toBe(false);
    expect(v.age).toBe('as of 12 min ago on gpu-box');
    expect(v.frees).toBe('');
  });

  it('tiles and their label', () => {
    expect(u.tiles!.tiles).toEqual([
      '6.8% of the 5-hour limit = 1% of the week',
      '≈ 6.8 full 5-hour windows use up the week',
      'One full 5-hour window ≈ 15% of the week',
    ]);
    expect(u.tiles!.label).toBe('measured over 7 days');
  });

  it('session cost line', () => {
    expect(u.session).toBe('Each new session is set aside 8% of the 5-hour limit until its real use shows up (default; measured after 20 sessions, 1 so far)');
  });

  it('ratio chart: title, what higher means, ticks, gaps, colours and the one hint', () => {
    const c = u.ratio!;
    expect(c.title).toBe('5-hour % per 1% of the week, each day');
    expect(c.higher).toBe('Higher: the week lasts longer compared with the 5-hour limit.');
    expect(c.ticks.map((t) => t.label)).toEqual(['0%', '5%', '10%']);
    expect(c.bars).toHaveLength(14);
    expect(c.bars.filter((b) => b.gap)).toHaveLength(9);
    expect(c.bars[1]!.label).toBe(`2 Nov: ${GAP_MARK}`);
    expect(c.bars[0]!.label).toBe('1 Nov: 5% of the 5-hour limit per 1% of the week · mostly Opus 5.5 · 30 readings');
    expect(c.legend).toEqual([{ model: 'Opus 5.5', colour: 0 }]);
    expect(c.hasGaps).toBe(true);
    expect(c.axis).toEqual(['1 Nov', '7 Nov', '14 Nov']);
    expect(c.summary).toContain('5 of 14 days have data');
    expect(c.hint).toBe(true);
    expect(TAP_HINT).toBe('Tap a day to see its value.');
  });

  it('capacity: one sentence for the model with data, a line for each unused model, the alert note once, no chart', () => {
    const cap = u.capacity;
    expect(cap.models.map((m) => m.text)).toEqual([
      'This week 1% of the 5-hour limit bought about $1.67 of Opus 5.5 work at API prices. Last week: not enough data yet.',
      'Sonnet 5.5: not used in the last 2 weeks.',
      'Haiku 5.5: not used in the last 2 weeks.',
    ]);
    expect(cap.models.every((m) => m.chart === null)).toBe(true);
    expect(cap.alertNote).toBe('A change of more than 20% on most accounts raises an alert.');
    expect(cap.alertNote).toBe(ALERT_NOTE);
  });

  it('no string uses a symbol, "lane", "normalised" or "Tap a bar"', () => {
    for (const s of strings(u)) for (const re of FORBIDDEN) expect(s).not.toMatch(re);
  });

  it('the how-this-works rows speak in words', () => {
    const rows = howRows(ownerCal(), NOW, 'UTC');
    expect(rows[0]!.text).toBe('How many % of the 5-hour limit a million tokens of Claude Code use cost: 1.38, measured from 1504 readings over 7 days.');
    expect(rows.map((r) => r.title)).not.toContain('Last restart');
  });
});

describe('verdictModel', () => {
  it('none', () => {
    expect(verdictModel(null, NOW)).toBeNull();
    expect(NO_VERDICT).toBe('No verdict yet: wy account next has not reported this account.');
  });
  it('first in line, and weekly room expiring', () => {
    const v = verdictModel(ownerVerdict({ rank: 1, expiring: true }), NOW)!;
    expect(v.headline).toBe('Can take the next new session');
    expect(v.place).toBe('It gets the next new session started with wy account next. Its week resets within 2 days, so it goes first.');
    expect(verdictModel(ownerVerdict({ rank: 2 }), NOW)!.place).toBe('2nd in line: 1 other account gets a new session before this one; they take turns.');
  });
  it('aggressive: the week rule is a stop level', () => {
    const v = verdictModel(ownerVerdict({ kind: 'aggressive', weekly_cap: 98 }), NOW)!;
    expect(v.rules[1]!.text).toBe('Week 29% (stops at 98%)');
    expect(v.kindName).toBe('Aggressive');
  });
  it('not chosen shows its words; an older verdict without caps shows the levels alone', () => {
    const v = verdictModel(ownerVerdict({ kind: 'aggressive', mode_from: 'none', weekly_cap: null, five_hour_cap: null, burst_cap: null }), NOW)!;
    expect(v.notChosen).toBe(true);
    expect(v.kindName).toBe('Not chosen');
    expect(v.kindWords).toBe('Not chosen yet; treated as aggressive.');
    expect(v.rules.map((r) => r.text)).toEqual(['5-hour 1%', 'Week 29%', 'New sessions in the last hour: 0']);
    expect(verdictModel({ ...ownerVerdict(), mode_from: undefined, kind: null }, NOW)!.notChosen).toBe(true);
  });
  it('not taking sessions: the blocking rule, the reason and when it frees up', () => {
    const v = verdictModel(ownerVerdict({
      eligible: false, rank: null, skip_level: 2, five_hour_pct: 50, free_at: '2026-11-13T14:00:00Z', next_free: '2026-11-12T13:30:00Z',
    }), NOW, 'UTC')!;
    expect(v.headline).toBe('Not taking new sessions now');
    expect(v.place).toBe('');
    expect(v.rules[0]).toEqual({ text: '5-hour 50% (stops at 45%)', blocking: true });
    expect(v.why).toBe('it is protected and is over its pace or 5-hour limit for new sessions');
    expect(v.frees).toBe('Frees up Fri 14:00');
    expect(v.noneFree).toBe('No account was free; the first frees up 13:30');
  });
  it('the reasons of the other levels, and no known time', () => {
    const w = (level: number) => verdictModel(ownerVerdict({ eligible: false, skip_level: level, free_at: null }), NOW)!;
    expect(w(0).why).toBe('it got its most new sessions for one hour');
    expect(w(1).why).toBe('its newest usage reading is too old to trust');
    expect(w(3).why).toBe('it is at its limit');
    expect(w(3).frees).toBe('No known time when it frees up');
  });
});

describe('verdictAge, ordinal, num, relationStale', () => {
  it('ages', () => {
    expect(verdictAge(ownerVerdict(), NOW)).toBe('as of 12 min ago on gpu-box');
    expect(verdictAge(ownerVerdict({ machine_name: null, at: iso(NOW - 5e3) }), NOW)).toBe('as of just now on m1');
    expect(verdictAge(ownerVerdict({ at: iso(NOW - 61 * 60e3) }), NOW)).toBe('older than an hour, from gpu-box');
    expect(verdictAge(null, NOW)).toBe('');
  });
  it('ordinals and numbers', () => {
    expect([1, 2, 3, 4, 11, 12, 13, 21, 22, 23, 101, 111].map(ordinal)).toEqual(['1st', '2nd', '3rd', '4th', '11th', '12th', '13th', '21st', '22nd', '23rd', '101st', '111th']);
    expect([7.1, 0.042, 12.34, 1234, null].map(num)).toEqual(['7.1', '0.042', '12.3', '1,234', '–']);
  });
  it('stale after 5 minutes', () => {
    expect(relationStale(null, NOW)).toBe(true);
    expect(relationStale(NOW - 5 * 60e3, NOW)).toBe(false);
    expect(relationStale(NOW - 5 * 60e3 - 1, NOW)).toBe(true);
  });
});

describe('tiles and the session line', () => {
  it('whole percents, and the ratio as given when round', () => {
    const c = ownerCal();
    c.ratio.value = 12.5;
    expect(tilesModel(c)!.tiles[2]).toBe('One full 5-hour window ≈ 8% of the week');
    c.ratio.value = 4;
    expect(tilesModel({ ...c, ratio: { ...c.ratio, value: 15 } })!.tiles[2]).toBe('One full 5-hour window ≈ 6.7% of the week');
    expect(tilesModel(c)!.tiles).toEqual(['4% of the 5-hour limit = 1% of the week', '≈ 4 full 5-hour windows use up the week', 'One full 5-hour window ≈ 25% of the week']);
  });
  it('the label: blending, default and implausible', () => {
    const c = ownerCal();
    c.ratio = factor({ value: 6.5, weight: 0.4, n: 5, days: 2 });
    expect(tilesModel(c)!.label).toBe('partly measured (40%), over 2 days');
    c.ratio = factor({ value: 6.5, weight: 0, n: 4, days: 0 });
    expect(tilesModel(c)!.label).toBe('default (not measured yet: needs 3 days and 20 readings, 4 so far)');
    c.ratio = factor({ value: 6.5, weight: 0, n: 4, days: 0, implausible: true });
    expect(tilesModel(c)!.label).toMatch(/; measured value looked wrong, not used$/);
    expect(tilesModel(null)).toBeNull();
  });
  it('the session line when measured, and with no calibration', () => {
    const c = ownerCal();
    c.lane_price = factor({ weight: 1, n: 34 });
    c.lane_charge = 8.5;
    expect(sessionLine(c)).toBe('Each new session is set aside 8.5% of the 5-hour limit until its real use shows up (measured from 34 sessions)');
    expect(sessionLine(null)).toBe('Not measured yet: the hub has no calibration for this account.');
  });
});

describe('yTicks', () => {
  it('0 and two round steps that reach the maximum', () => {
    expect(yTicks(8)).toEqual([0, 5, 10]);
    expect(yTicks(7.1)).toEqual([0, 5, 10]);
    expect(yTicks(1.67)).toEqual([0, 1, 2]);
    expect(yTicks(0.044)).toEqual([0, 0.025, 0.05]);
    expect(yTicks(100)).toEqual([0, 50, 100]);
    expect(yTicks(0)).toEqual([0, 1, 2]);
  });
});

describe('charts', () => {
  it('a ratio chart with no data is replaced by one line, and the hint goes to the next chart', () => {
    const rel = owner({ days: days14().map((d) => ({ ...d, ratio: null, model: null })) });
    expect(ratioChart(rel)).toBeNull();
    const u = usageModel(rel, NOW);
    expect(u.noRatio).toBe(NO_RATIO);
    expect(u.ratio).toBeNull();
  });
  it('colours go by first appearance, one per model, with full names in the legend', () => {
    const ds = days14();
    ds[3] = { ...ds[3]!, model: 'Sonnet 5.5' };
    const rel = owner({ days: ds });
    expect(modelOrder(rel).slice(0, 2)).toEqual(['Opus 5.5', 'Sonnet 5.5']);
    const c = ratioChart(rel)!;
    expect(c.legend.map((l) => l.model)).toEqual(['Opus 5.5', 'Sonnet 5.5']);
    expect(c.bars[3]!.colour).toBe(1);
    expect(MODEL_COLOURS.length).toBeGreaterThanOrEqual(5);
  });
  it('the capacity chart only when both weeks have a value, with last week as a dashed line', () => {
    const idx = [1.5, 1.6, null, 1.7, 1.8, 1.7];
    const rel = owner({ models: [model('Opus 5.5', { this_week: 1.67, last_week: 1.52, change: 1.1 }, idx)] });
    const cap = capacityModel(rel);
    expect(cap.models[0]!.text).toBe('This week 1% of the 5-hour limit bought about $1.67 of Opus 5.5 work at API prices. Last week: $1.52 (10% more now).');
    const c = cap.models[0]!.chart!;
    expect(c.ref!.label).toBe('Last week: $1.52');
    expect(c.ticks.map((t) => t.label)).toEqual(['$0', '$1', '$2']);
    expect(c.bars[2]!.gap).toBe(true);
    expect(c.bars[2]!.label).toContain('no data');
    const u = usageModel(owner({ days: days14().map((d) => ({ ...d, ratio: null })), models: rel.models }), NOW);
    expect(u.capacity.models[0]!.chart!.hint).toBe(true);
    expect(u.ratio).toBeNull();
    const withRatio = usageModel(rel, NOW);
    expect(withRatio.ratio!.hint).toBe(true);
    expect(withRatio.capacity.models[0]!.chart!.hint).toBe(false);
  });
  it('most dollars first, and less than last week', () => {
    const rel = owner({ models: [model('Haiku 5.5', { this_week: 0.3, last_week: 0.4, change: 0.75 }, [0.1]), model('Opus 5.5', { this_week: 2, last_week: 2 }, [1, 1, 1])] });
    const cap = capacityModel(rel);
    expect(cap.models.map((m) => m.model)).toEqual(['Opus 5.5', 'Haiku 5.5']);
    expect(cap.models[0]!.text).toContain('Last week: $2.00 (the same).');
    expect(cap.models[1]!.text).toContain('(25% less now)');
  });
  it('a model with a few days but no weekly value, and no alert note when nothing was said about this week', () => {
    const cap = capacityModel(owner({ models: [model('Opus 5.5', {}, [1])] }));
    expect(cap.models[0]!.text).toBe('Opus 5.5: not enough data this week yet.');
    expect(cap.alertNote).toBe('');
  });
  it('the not-heavy note and a muted chart', () => {
    expect(heavyNote({ heavy: false, rise: 30, cc_share: 0.62 })).toBe('62% of the 5-hour rise had Claude Code tokens: this is not a fair measure this week and does not count toward limit-change alerts.');
    expect(heavyNote({ heavy: false, rise: 8, cc_share: 0.5 })).toMatch(/^Only 8 points of 5-hour rise this week: /);
    expect(heavyNote({ heavy: false, rise: 0, cc_share: null })).toMatch(/^No 5-hour rise this week: /);
    expect(heavyNote({ heavy: true, rise: 30, cc_share: 0.9 })).toBe('');
    const rel = owner({ heavy: { this_week: { heavy: false, rise: 30, cc_share: 0.62 }, last_week: { heavy: true, rise: 1, cc_share: 1 } }, models: [model('Opus 5.5', { this_week: 1, last_week: 1 }, [1])] });
    expect(capacityModel(rel).models[0]!.chart!.muted).toBe(true);
  });
});

describe('howRows and the restart', () => {
  it('a restart is named with its reason', () => {
    const c = { ...ownerCal(), reset: { at: '2026-11-12T10:00:00Z', reason: 'seat_change' as const } };
    expect(calibrationRestart(c, NOW, 'UTC')).toBe('Restarted 10:00: the account changed');
    expect(howRows(c, NOW, 'UTC').map((r) => r.title)).toContain('Last restart');
    expect(calibrationRestart(ownerCal(), NOW)).toBe('');
  });
  it('without a calibration only the general rows remain', () => {
    expect(howRows(null, NOW).map((r) => r.title)).toEqual(['Where the numbers come from', 'The capacity index']);
  });
});
