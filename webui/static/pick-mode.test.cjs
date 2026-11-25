// node --test webui/static/*.test.cjs
// The picker's mode words and helpers (components/pick-mode.js, pwa.md 3.0a).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, 'components/pick-mode.js'), 'utf8');
if (/^import /m.test(src)) throw new Error('pick-mode.js must stay pure');
const names = [...src.matchAll(/^export (?:function|const) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
const P = vm.runInNewContext(`(function () {\n${src.replace(/^export /gm, '')}\nreturn { ${names.join(', ')} };\n})()`, {});
const j = (x) => JSON.parse(JSON.stringify(x));

test('the words are the spec words', () => {
  assert.deepEqual(j(P.MODE_WORDS), {
    protected: { name: 'Protected', definition: 'Kept for you. Used last: new sessions go here only when no aggressive account can take them, or for room that would expire unused anyway.' },
    aggressive: { name: 'Aggressive', definition: 'Use it up. Sessions go here until 98% of the week or 5 hours.' },
    drain: { name: 'Drain', definition: 'Used up first until the time you set (default: end of today), then back to its own setting by itself, or earlier at 97% of the week.' },
    off: { name: 'Off', definition: 'Stricter than Protected. Never picked and nothing moved in; sessions already there stay, and their usage is still shown.' },
    none: { name: 'Not chosen', definition: 'Not chosen yet; treated as aggressive.' },
  });
  assert.equal(P.MODE_QUESTION, 'How should the picker use this account?');
  assert.deepEqual(j(P.MODE_OPTIONS.map((o) => [o.key, o.value])), [['aggressive', 'aggressive'], ['protected', 'protected'], ['drain', null], ['off', 'off'], ['none', null]]);
});

test('keys and kinds', () => {
  assert.deepEqual([P.modeKey('protected'), P.modeKey('aggressive'), P.modeKey(null), P.modeKey(undefined), P.modeKey('normal')],
    ['protected', 'aggressive', 'none', 'none', 'none']);
  assert.equal(P.modeKey('off'), 'off');
  assert.equal(P.effectiveKind('off'), 'off');
  assert.deepEqual([P.effectiveKind('protected'), P.effectiveKind(null), P.effectiveKind('aggressive')], ['protected', 'aggressive', 'aggressive']);
  assert.equal(P.modeWords(null).name, 'Not chosen');
});

test('the local-override line', () => {
  const v = { mode_from: 'local', kind: 'protected', machine_name: 'gpu-box', machine: 'm_1' };
  assert.equal(P.localOverrideLine(v, 'aggressive'),
    'On gpu-box a setting on that machine makes it protected (set on this machine); it wins there over this choice.');
  assert.equal(P.localOverrideLine(v, null), P.localOverrideLine(v, 'aggressive'));
  assert.equal(P.localOverrideLine(v, 'protected'), '');
  assert.equal(P.localOverrideLine({ ...v, kind: 'aggressive' }, null), '');
  assert.equal(P.localOverrideLine({ ...v, mode_from: 'hub' }, null), '');
  assert.equal(P.localOverrideLine({ ...v, machine_name: null }, null).slice(0, 8), 'On m_1 a');
  assert.equal(P.localOverrideLine(null, null), '');
});

test('the off line and the local-override line for an off seat', () => {
  assert.equal(P.OFF_LINE, 'Off: never picked, nothing moved in; sessions already there stay');
  assert.equal(P.localOverrideLine({ mode_from: 'local', kind: 'aggressive', machine_name: 'x' }, 'off'), '');
});

test('drain: end, text, key', () => {
  const now = new Date(2026, 10, 25, 14, 0).getTime(); // Wed 25 Nov 2026 14:00 local
  const at = (d, h, m) => new Date(2026, 10, d, h, m).toISOString();
  const today = { mode: 'protected', drain_until: at(25, 23, 59) };
  assert.equal(P.drainText(today, now), 'Draining until 23:59');
  assert.equal(P.drainText({ drain_until: at(27, 9, 5) }, now), 'Draining until Fri 09:05');
  assert.equal(P.seatModeKey(today, now), 'drain');
  assert.equal(P.seatModeKey({ mode: 'protected', drain_until: at(25, 13, 0) }, now), 'protected');
  assert.equal(P.seatModeKey({ mode: 'protected', drain_until: null }, now), 'protected');
  assert.equal(P.seatModeKey({ mode: null }, now), 'none');
  assert.equal(P.seatModeKey({ mode: 'off', drain_until: at(25, 23, 59) }, now), 'off');
  assert.equal(P.drainText({ mode: 'off', drain_until: at(25, 23, 59) }, now), '');
  assert.equal(P.drainText({}, now), '');
  assert.equal(P.drainInputValue(today, now), '2026-11-25T23:59');
  assert.equal(P.drainInputValue({ drain_until: at(26, 6, 30) }, now), '2026-11-26T06:30');
  assert.equal(P.drainInputValue({}, now), '2026-11-25T23:59');
  // 2026-11-25: the CLI set Sun 07:20; the field showed only "07:20" and Change sent Thu 07:20.
  // The field now carries the date, and sending it gives back the same instant.
  const cli = { mode: 'protected', drain_until: at(29, 7, 20) };
  assert.equal(P.drainInputValue(cli, now), '2026-11-29T07:20');
  assert.equal(P.drainUntilISO(P.drainInputValue(cli, now), now), at(29, 7, 20));
  assert.deepEqual(JSON.parse(JSON.stringify(P.modeBody('drain', P.drainInputValue(cli, now), now))), { drain_until: at(29, 7, 20) });
  // After 23:59 has passed, the default is tomorrow's.
  assert.equal(P.drainInputValue({}, new Date(2026, 10, 25, 23, 59, 30).getTime()), '2026-11-26T23:59');
});

test('drain: the time field to an instant, and the PATCH bodies', () => {
  const now = new Date(2026, 10, 25, 14, 0).getTime();
  assert.equal(P.drainUntilISO('23:59', now), new Date(2026, 10, 25, 23, 59).toISOString());
  assert.equal(P.drainUntilISO('09:00', now), new Date(2026, 10, 26, 9, 0).toISOString()); // passed: tomorrow
  assert.equal(P.drainUntilISO('14:00', now), new Date(2026, 10, 26, 14, 0).toISOString());
  assert.equal(P.drainUntilISO('', now), null);
  assert.equal(P.drainUntilISO('25:00', now), null);
  // The datetime field is sent exactly as shown, never moved to another day.
  assert.equal(P.drainUntilISO('2026-11-27T09:00', now), new Date(2026, 10, 27, 9, 0).toISOString());
  assert.equal(P.drainUntilISO('2026-11-25T09:00', now), null); // past: refused, not tomorrow
  assert.equal(P.drainUntilISO('2026-11-25T14:00', now), null);
  assert.equal(P.drainUntilISO('2026-02-30T09:00', now), null);
  assert.equal(P.drainUntilISO('2026-11-27T24:00', now), null);
  assert.deepEqual(j(P.modeBody('drain', '', now)), { drain_until: new Date(2026, 10, 25, 23, 59).toISOString() });
  assert.deepEqual(j(P.modeBody('drain', 'zz', now)), null);
  assert.deepEqual(j(P.modeBody('off')), { mode: 'off', drain_until: null });
  assert.deepEqual(j(P.modeBody('protected')), { mode: 'protected', drain_until: null });
  assert.deepEqual(j(P.modeBody('none')), { mode: null, drain_until: null });
  assert.equal(P.modeBody('bogus'), null);
});
