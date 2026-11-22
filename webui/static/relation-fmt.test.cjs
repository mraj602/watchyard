// node --test webui/static/*.test.cjs
// An account's "Usage and the picker" (components/relation-fmt.js, pwa.md 3.0a).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const mod = (f) => {
  const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
  if (/^import /m.test(src)) throw new Error(`${f} has imports; test only pure modules`);
  const names = [...src.matchAll(/^export (?:async )?(?:function|const|let|class) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  const body = src.replace(/^export /gm, '');
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`,
    { Number, String, Array, Object, Math, Date, Intl, Set, isNaN: Number.isNaN });
};

const R = mod('components/relation-fmt.js');
const NOW = Date.parse('2026-11-20T10:00:00Z'); // a Friday
const j = (x) => JSON.parse(JSON.stringify(x));

test('days, ordinals and times', () => {
  assert.equal(R.shortDay('2026-11-07'), '7 Nov');
  assert.deepEqual([1, 2, 3, 4, 11, 12, 13, 21, 22, 23, 101].map(R.ordinal),
    ['1st', '2nd', '3rd', '4th', '11th', '12th', '13th', '21st', '22nd', '23rd', '101st']);
  assert.equal(R.dayClock('2026-11-20T14:00:00Z', NOW, 'UTC'), '14:00');
  assert.equal(R.dayClock('2026-11-19T09:05:00Z', NOW, 'UTC'), 'Thu 09:05');
  assert.equal(R.dayClock('2026-11-02T09:05:00Z', NOW, 'UTC'), '2 Nov 09:05');
  assert.equal(R.dayClock('2026-11-20T20:00:00Z', NOW, 'Asia/Kolkata'), 'Sat 01:30');
  assert.equal(R.dayClock('nonsense', NOW, 'UTC'), '');
});

test('the picker verdict', () => {
  assert.equal(R.verdictLine(null, NOW, 'UTC'), 'No verdict yet: wy account next has not reported this account.');
  assert.equal(R.verdictLine({ eligible: true, rank: 1, kind: 'aggressive' }, NOW, 'UTC'), 'Eligible · next in line · aggressive');
  assert.equal(R.verdictLine({ eligible: true, rank: 3 }, NOW, 'UTC'), 'Eligible · 3rd in line');
  assert.equal(R.verdictLine({ eligible: true, rank: 2, expiring: true, kind: 'protected' }, NOW, 'UTC'),
    'Eligible · 2nd in line · weekly room expiring · protected');
  assert.equal(R.verdictLine({ eligible: false, reason: '7d 92% >= pace 85', skip_level: 2, free_at: '2026-11-20T14:00:00Z' }, NOW, 'UTC'),
    'Not picked: 7d 92% >= pace 85 (protected rule) · frees up 14:00');
  assert.equal(R.verdictLine({ eligible: false, skip_level: 0 }, NOW, 'UTC'), 'Not picked: burst cap');
  assert.equal(R.verdictLine({ eligible: false, skip_level: 3, free_at: '2026-11-21T09:00:00Z', next_free: '2026-11-20T11:30:00Z' }, NOW, 'UTC'),
    'Not picked: limit · frees up Sat 09:00 · no account was free, the first frees up 11:30');
  assert.equal(R.verdictLine({ eligible: false, skip_level: 3, free_at: '2026-11-20T11:30:00Z', next_free: '2026-11-20T11:30:00Z' }, NOW, 'UTC'),
    'Not picked: limit · frees up 11:30');
  assert.equal(R.verdictLine({ eligible: false, reason: 'logged out' }, NOW, 'UTC'), 'Not picked: logged out');
  assert.equal(R.verdictAge({ at: '2026-11-20T09:48:00Z', machine: 'm_1', machine_name: 'gpu-box' }, NOW), 'as of 12 min ago on gpu-box');
  assert.equal(R.verdictAge({ at: '2026-11-20T09:59:50Z', machine: 'm_1', machine_name: null }, NOW), 'as of just now on m_1');
  assert.equal(R.verdictAge({ at: '2026-11-20T08:30:00Z', machine_name: 'gpu-box' }, NOW), 'older than an hour, from gpu-box');
});

test('calibration rows', () => {
  assert.equal(R.calibRows(null, NOW, 'UTC'), null);
  const f = (value, weight, n, days, implausible = false) => ({ value, fallback: 1, measured: value, weight, n, days, implausible });
  const c = R.calibRows({ f: f(1.234, 1, 40, 6.5), ratio: f(7.2, 0, 0, 0), lane_price: f(4.25, 0.4, 3, 2, true), lane_charge: 8.5,
    reset: { at: '2026-11-18T08:00:00Z', reason: 'under_read' } }, NOW, 'UTC');
  assert.deepEqual(j(c.rows), [
    { name: 'Meter factor f', value: '1.23 points per million units', state: 'measured · n 40 · 6.5 days' },
    { name: 'Ratio 5 h : 7 d', value: '7.2 5-hour points per 7-day point', state: 'fallback' },
    { name: 'Lane charge c', value: '8.5 points (lane price 4.3)', state: 'blending, 40% measured · n 3 · 2 days · implausible measurement, not used' },
  ]);
  assert.equal(c.restart, 'Restarted Wed 08:00: the meter read low');
  assert.equal(R.calibRows({ f: f(1, 1, 1, 1), reset: null }, NOW, 'UTC').restart, '');
});

test('daily ratio bars: gaps, initials, legend and tap text', () => {
  const days = [
    { day: '2026-11-18', ratio: 7.14, d5: 50, d7: 7, pairs: 30, model: 'Opus 4.6' },
    { day: '2026-11-19', ratio: null, d5: 3, d7: 1, pairs: 2, model: 'Sonnet 4.5' },
    { day: '2026-11-20', ratio: 3.5, d5: 7, d7: 2, pairs: 1, model: 'Opus 4.6' },
    { day: '2026-11-21', ratio: null, d5: 0, d7: 0, pairs: 0, model: null },
  ];
  const b = R.ratioBars(days);
  assert.equal(b.legend, 'O Opus 4.6 · S Sonnet 4.5');
  assert.deepEqual(b.bars.map((x) => [x.height, x.initial]), [[100, 'O'], [0, 'S'], [49, 'O'], [0, '']]);
  assert.deepEqual(b.bars.map((x) => x.label), ['18 Nov: 7.1 · Opus 4.6 · 30 pairs', '19 Nov: under 2 weekly points · 2 pairs',
    '20 Nov: 3.5 · Opus 4.6 · 1 pair', '21 Nov: no ratio']);
  assert.equal(R.ratioBars([]).legend, '');
});

test('capacity per model: line, bars with the 1.0 line, the heavy note', () => {
  const m = { model: 'Opus 4.6', this_week: 0.042, last_week: 0.04, change: 1.05, days: [
    { day: '2026-11-19', index: 0.044, normalised: 1.1, d5: 12, usd: 0.53 },
    { day: '2026-11-20', index: null, normalised: null, d5: 3, usd: 0.1 },
    { day: '2026-11-21', index: null, normalised: null, d5: 0, usd: 0 },
    { day: '2026-11-22', index: 0.09, normalised: 2.25, d5: 6, usd: 0.54 },
  ] };
  assert.equal(R.capacityLine(m), 'Opus 4.6 · $0.042 per 1% this week · 1.05× last week');
  assert.equal(R.capacityLine({ ...m, change: null, last_week: null }), 'Opus 4.6 · $0.042 per 1% this week · last week: not enough days');
  assert.equal(R.capacityLine({ ...m, this_week: null }), 'Opus 4.6 · not enough days');
  assert.equal(R.perPoint(1.234), '$1.23');
  const b = R.capacityBars(m);
  assert.equal(b.line, 44);
  assert.deepEqual(b.bars.map((x) => x.height), [49, 0, 0, 100]);
  assert.deepEqual(b.bars.map((x) => x.label), ['19 Nov: $0.044 per 1% · 1.1× last week · 12 points', '20 Nov: under 5 points (3)',
    '21 Nov: no index', '22 Nov: $0.090 per 1% · 2.25× last week · 6 points']);
  assert.equal(R.capacityBars({ days: [] }).line, 67);
  assert.equal(R.heavyNote({ this_week: { heavy: true, rise: 40, cc_share: 0.9 } }), '');
  assert.equal(R.heavyNote({ this_week: { heavy: false, rise: 40, cc_share: 0.62 } }),
    '62% of the 5-hour rise had Claude Code tokens: this index is not a fair measure and does not count toward limit-change alerts.');
  assert.equal(R.heavyNote({ this_week: { heavy: false, rise: 0, cc_share: null } }),
    'No 5-hour rise this week: this index is not a fair measure and does not count toward limit-change alerts.');
  assert.equal(R.heavyNote({ this_week: { heavy: false, rise: 12, cc_share: 1 } }),
    'Only 12 points of 5-hour rise this week: this index is not a fair measure and does not count toward limit-change alerts.');
});
