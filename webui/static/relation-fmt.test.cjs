// node --test webui/static/*.test.cjs
// An account's "Usage and the picker" (components/relation-fmt.js, pwa.md 3.0a), with the
// owner's own account as the fixture.

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
    { Number, String, Array, Object, Math, Date, Intl, Set, isNaN: Number.isNaN, isFinite: Number.isFinite });
};

const R = mod('components/relation-fmt.js');
const NOW = Date.parse('2026-11-20T10:00:00Z'); // a Friday
const j = (x) => JSON.parse(JSON.stringify(x));

// The owner's case: one protected account, 3rd in line, a measured ratio of 6.78.
const fac = (value, fallback, weight, n, days) => ({ value, fallback, measured: weight ? value : null, weight, n, days, implausible: false });
const OWNER_CAL = { f: fac(1.38, 1, 1, 1504, 6.7), ratio: fac(6.78, 5, 1, 1480, 6.6), lane_price: fac(4, 4, 0, 1, 0), lane_charge: 8, reset: null };
const OWNER_VERDICT = { at: '2026-11-20T09:48:00Z', machine: 'm_1', machine_name: 'gpu-box', strategy: 'lanes', kind: 'protected', eligible: true, rank: 3,
  expiring: false, free_at: null, next_free: null, weekly_pct: 29, weekly_cap: 52, five_hour_pct: 1, five_hour_cap: 45, burst: 0, burst_cap: 2, stale: false, mode_from: 'hub' };
const dayOf = (back) => new Date(Date.UTC(2026, 10, 20 - back)).toISOString().slice(0, 10);
const OWNER_DAYS = Array.from({ length: 14 }, (_, i) => {
  const ratio = { 5: 7.1, 8: 6.2, 9: 6.9, 11: 7.4, 13: 6.8 }[i];
  return { day: dayOf(13 - i), ratio: ratio == null ? null : ratio, d5: 0, d7: 0, pairs: ratio == null ? 0 : 30, model: ratio == null ? null : 'Opus 5.5', rise: 0, rise_cc: 0 };
});
const mdays = (idx) => Array.from({ length: 14 }, (_, i) => ({ day: dayOf(13 - i), index: idx[i] == null ? null : idx[i], normalised: null, d5: 0, usd: 0 }));
const OWNER_MODELS = [
  { model: 'Opus 5.5', this_week: 1.67, last_week: null, change: null, days: mdays({ 10: 1.6, 12: 1.7, 13: 1.67 }) },
  { model: 'Sonnet 5.5', this_week: null, last_week: null, change: null, days: mdays({}) },
  { model: 'Haiku 5.5', this_week: null, last_week: null, change: null, days: mdays({}) },
];
const HEAVY = { this_week: { heavy: true, rise: 40, cc_share: 0.9 }, last_week: { heavy: true, rise: 40, cc_share: 0.9 } };

// Every sentence the section can show (strings only): nothing in it may use the old symbols.
function strings(v, out = []) {
  if (typeof v === 'string') out.push(v);
  else if (Array.isArray(v)) v.forEach((x) => strings(x, out));
  else if (v && typeof v === 'object') Object.values(v).forEach((x) => strings(x, out));
  return out;
}
const ALL = () => strings(j({
  verdict: R.verdictModel(OWNER_VERDICT, NOW, 'UTC'), tiles: R.tilesModel(OWNER_CAL), session: R.sessionLine(OWNER_CAL),
  ratio: R.ratioChart(OWNER_DAYS), cap: R.capacityModel(OWNER_MODELS, HEAVY), how: R.howRows({ ...OWNER_CAL, reset: { at: '2026-11-18T08:00:00Z', reason: 'under_read' } }, NOW, 'UTC'),
}));

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

test('the verdict of the owner\'s account', () => {
  const m = j(R.verdictModel(OWNER_VERDICT, NOW, 'UTC'));
  assert.equal(m.headline, 'Can take a new session: 3rd in line');
  assert.equal(m.place, '3rd in line: 2 other accounts get a new session before this one; they take turns.');
  assert.equal(m.kindKey, 'protected');
  assert.deepEqual(m.rules.map((r) => r.text), ['5-hour 1% (stops at 45%)', 'Week 29%, the even pace allows 52% today', 'New sessions in the last hour: 0 (at most 2)']);
  assert.ok(m.rules.every((r) => !r.blocking));
  assert.equal(R.verdictAge(OWNER_VERDICT, NOW), 'as of 12 min ago on gpu-box');
});

test('verdict variants: first, aggressive, not chosen, blocked, older without caps, none', () => {
  assert.equal(R.verdictModel(null, NOW).text, 'No verdict yet: wy account next has not reported this account.');
  const first = R.verdictModel({ ...OWNER_VERDICT, rank: 1, kind: 'aggressive', weekly_cap: 98 }, NOW, 'UTC');
  assert.equal(first.headline, 'Can take the next new session');
  assert.equal(first.place, 'It gets the next new session started with wy account next.');
  assert.ok(first.rules.some((r) => r.text === 'Week 29% (stops at 98%)'));
  assert.equal(R.verdictModel({ ...OWNER_VERDICT, rank: 2 }, NOW).place, '2nd in line: 1 other account gets a new session before this one; they take turns.');
  assert.equal(R.verdictModel({ ...OWNER_VERDICT, mode_from: 'none', kind: 'aggressive' }, NOW).kindKey, 'none');
  assert.equal(R.verdictModel({ ...OWNER_VERDICT, expiring: true, rank: 1 }, NOW).place, 'It gets the next new session started with wy account next. Its week resets within 2 days, so it goes first.');
  const old = R.verdictModel({ at: OWNER_VERDICT.at, eligible: true, rank: 1, kind: 'aggressive', weekly_pct: 29.4, five_hour_pct: 1 }, NOW);
  assert.deepEqual(j(old.rules.map((r) => r.text)), ['5-hour 1%', 'Week 29%']);
  assert.equal(old.kindKey, 'aggressive');
  const no = R.verdictModel({ at: OWNER_VERDICT.at, eligible: false, skip_level: 0, burst: 2, burst_cap: 2, five_hour_pct: 50, five_hour_cap: 45,
    free_at: '2026-11-20T14:00:00Z', next_free: '2026-11-20T11:30:00Z', kind: 'protected', weekly_pct: 30, weekly_cap: 70 }, NOW, 'UTC');
  assert.equal(no.headline, 'Not taking new sessions now');
  assert.equal(no.why, 'Not taking sessions because it got its most new sessions for one hour');
  assert.equal(no.frees, 'Frees up 14:00');
  assert.equal(no.noneFree, 'No account was free; the first frees up 11:30');
  assert.deepEqual(j(no.rules.filter((r) => r.blocking).map((r) => r.key)), ['five_hour', 'burst']);
  assert.equal(R.verdictModel({ at: OWNER_VERDICT.at, eligible: false, skip_level: 3 }, NOW).frees, 'No known time when it frees up');
  assert.equal(R.verdictModel({ at: OWNER_VERDICT.at, eligible: false, skip_level: 1 }, NOW).why, 'Not taking sessions because its newest usage reading is too old to trust');
  assert.equal(R.verdictAge({ at: '2026-11-20T08:30:00Z', machine_name: 'gpu-box' }, NOW), 'older than an hour, from gpu-box');
  assert.equal(R.verdictAge({ at: '2026-11-20T09:59:50Z', machine_name: null, machine: 'm_1' }, NOW), 'as of just now on m_1');
});

test('the three tiles and their label', () => {
  const t = j(R.tilesModel(OWNER_CAL));
  assert.deepEqual(t.tiles.map((x) => x.text), ['6.8% of the 5-hour limit = 1% of the week', '≈ 6.8 full 5-hour windows use up the week', 'One full 5-hour window ≈ 15% of the week']);
  assert.equal(t.label, 'measured over 7 days');
  assert.equal(R.tilesModel({ ...OWNER_CAL, ratio: fac(12, 12, 0.4, 100, 2) }).tiles[2].text, 'One full 5-hour window ≈ 8.3% of the week');
  assert.equal(R.tilesModel({ ...OWNER_CAL, ratio: fac(12, 12, 0.4, 100, 2) }).label, 'partly measured (40%), over 2 days');
  assert.equal(R.tilesModel({ ...OWNER_CAL, ratio: fac(5, 5, 0, 4, 0) }).label, 'default (not measured yet: needs 3 days and 20 readings, 4 so far)');
  assert.equal(R.tilesModel({ ...OWNER_CAL, ratio: { ...fac(5, 5, 0, 4, 0), implausible: true } }).label, 'default (not measured yet: needs 3 days and 20 readings, 4 so far); measured value looked wrong, not used');
  assert.equal(R.tilesModel(null), null);
});

test('the session cost line', () => {
  assert.equal(R.sessionLine(OWNER_CAL), 'Each new session is set aside 8% of the 5-hour limit until its real use shows up (default; measured after 20 sessions, 1 so far)');
  assert.equal(R.sessionLine({ ...OWNER_CAL, lane_price: fac(5.5, 4, 1, 34, 7), lane_charge: 11 }),
    'Each new session is set aside 11% of the 5-hour limit until its real use shows up (measured from 34 sessions)');
  assert.equal(R.sessionLine(null), 'Not measured yet: the hub has no calibration for this account.');
});

test('round axis ticks and model colours', () => {
  assert.deepEqual(j(R.niceTicks(7.4)), { top: 10, ticks: [0, 5, 10] });
  assert.deepEqual(j(R.niceTicks(1.7)), { top: 2, ticks: [0, 1, 2] });
  assert.deepEqual(j(R.niceTicks(23)), { top: 40, ticks: [0, 20, 40] });
  assert.deepEqual(j(R.niceTicks(0)), { top: 1, ticks: [0, 0.5, 1] });
  assert.deepEqual(j(R.modelSlots(['Opus 5.5', 'Sonnet 5.5', null, 'Haiku 5.5', 'Gemini', 'Opus 5.5'])), { 'Opus 5.5': 1, 'Sonnet 5.5': 2, 'Haiku 5.5': 3, Gemini: 4 });
});

test('the daily ratio chart: gaps, colours, legend, day text', () => {
  const c = j(R.ratioChart(OWNER_DAYS));
  assert.equal(c.hidden, false);
  assert.equal(c.bars.length, 14);
  assert.equal(c.bars.filter((b) => b.value == null).length, 9);
  assert.equal(c.bars[0].text, `${R.shortDay(OWNER_DAYS[0].day)}: no data`);
  assert.equal(c.bars[5].text, `${R.shortDay(OWNER_DAYS[5].day)}: 7.1% of the 5-hour limit per 1% of the week · mostly Opus 5.5 · 30 readings`);
  assert.deepEqual(c.legend, [{ model: 'Opus 5.5', slot: 1 }]);
  assert.deepEqual(c.ticks.map((t) => t.label), ['0%', '5%', '10%']);
  assert.deepEqual(c.xLabels.map((x) => x.at), [0, 6, 13]);
  assert.equal(c.title, '5-hour % per 1% of the week, each day');
  assert.ok(c.bars[5].frac > 0.7 && c.bars[5].frac < 0.72);
  const none = R.ratioChart(OWNER_DAYS.map((d) => ({ ...d, ratio: null })));
  assert.deepEqual(j(none), { hidden: true, hiddenText: 'Not enough readings in the last 2 weeks to show this.' });
});

test('capacity: the owner\'s sentence, the alert line once, no chart without two weeks, unused models', () => {
  const c = j(R.capacityModel(OWNER_MODELS, HEAVY));
  assert.deepEqual(c.items.map((i) => i.sentence), ['This week 1% of the 5-hour limit bought about $1.67 of Opus 5.5 work at API prices. Last week: not enough data yet.']);
  assert.equal(c.items[0].chart, null);
  assert.deepEqual(c.unused, ['Sonnet 5.5: not used in the last 2 weeks.', 'Haiku 5.5: not used in the last 2 weeks.']);
  assert.equal(c.alertLine, 'A change of more than 20% on most accounts raises an alert.');
  assert.equal(c.note, '');

  const both = [{ model: 'Opus 4.6', this_week: 1.67, last_week: 1.52, change: 1.1, days: mdays({ 8: 1.5, 9: 1.55, 12: 1.7, 13: 1.67 }) }];
  const b = j(R.capacityModel(both, { this_week: { heavy: false, rise: 40, cc_share: 0.62 }, last_week: null }));
  assert.equal(b.items[0].sentence, 'This week 1% of the 5-hour limit bought about $1.67 of Opus 4.6 work at API prices. Last week: $1.52 (10% more now).');
  assert.equal(b.items[0].chart.line.label, 'Last week: $1.52');
  assert.equal(b.items[0].chart.bars.filter((x) => x.value != null).length, 4);
  assert.equal(b.items[0].chart.muted, true);
  assert.equal(b.note, '62% of the 5-hour rise had Claude Code tokens: this is not a fair measure this week and does not count toward limit-change alerts.');
  assert.match(j(R.capacityModel([{ ...both[0], change: 0.9, last_week: 1.85 }], HEAVY)).items[0].sentence, /\(10% less now\)\.$/);
  assert.equal(j(R.capacityModel([], HEAVY)).alertLine, '');
  assert.equal(R.perPoint(0.042), '$0.042');
  assert.equal(R.heavyNote({ this_week: { heavy: false, rise: 0, cc_share: null } }),
    'No 5-hour rise this week: this is not a fair measure this week and does not count toward limit-change alerts.');
  assert.equal(R.heavyNote({ this_week: { heavy: false, rise: 12, cc_share: 1 } }),
    'Only 12 points of 5-hour rise this week: this is not a fair measure this week and does not count toward limit-change alerts.');
});

test('how this works', () => {
  const rows = j(R.howRows({ ...OWNER_CAL, reset: { at: '2026-11-18T08:00:00Z', reason: 'under_read' } }, NOW, 'UTC'));
  assert.equal(rows[0].text, 'How many % of the 5-hour limit a million tokens of Claude Code use cost: 1.38, measured from 1504 readings over 7 days.');
  assert.ok(rows.some((r) => r.text === '8% of the 5-hour limit set aside per new session, default, not measured yet (1 session so far).'));
  assert.ok(rows.some((r) => /about 3 days and 20 readings/.test(r.text)));
  assert.ok(rows.some((r) => r.name === 'Last restart' && r.text.startsWith('Wed 08:00: the meter read low')));
  assert.equal(R.howRows(null, NOW, 'UTC').length, 2);
});

test('no symbols, no jargon anywhere in the section', () => {
  const all = ALL();
  assert.ok(all.length > 20);
  for (const s of all) {
    assert.doesNotMatch(s, /(^|\s)[fcn]\s/, s);
    assert.doesNotMatch(s, /lane|normalised|Tap a bar|ρ/i, s);
  }
  assert.equal(R.CHART_HINT, 'Hover or tap a day to see its value.');
});

test('wording shared with the phone: same, no-data-this-week, chart title, days fallback', () => {
  const m = { model: 'Opus 4.6', this_week: 1.5, last_week: 1.5, change: null, days: mdays({ 12: 1.5, 13: 1.5 }) };
  const c = j(R.capacityModel([m], HEAVY));
  assert.match(c.items[0].sentence, /Last week: \$1\.50 \(the same\)\.$/);
  assert.equal(c.items[0].chart.title, 'Opus 4.6: API dollars per 1% of the 5-hour limit, each day');
  assert.equal(c.items[0].chart.sub, 'Higher: the same 5-hour limit buys more work.');
  assert.equal(j(R.capacityModel([{ ...m, this_week: null }], HEAVY)).items[0].sentence, 'Opus 4.6: not enough data this week yet.');
  assert.equal(R.tilesModel({ ...OWNER_CAL, ratio: fac(7, 5, 1, 10, 0) }).label, 'measured over 7 days');
});
