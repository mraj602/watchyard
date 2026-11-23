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
    protected: { name: 'Protected', definition: 'Kept for you. New sessions only when no aggressive account has room, this week is behind an even pace and the 5-hour window is under 45%.' },
    aggressive: { name: 'Aggressive', definition: 'Use it up. Sessions go here until 98% of the week or 5 hours.' },
    none: { name: 'Not chosen', definition: 'Not chosen yet; treated as aggressive.' },
  });
  assert.equal(P.MODE_QUESTION, 'How should the picker use this account?');
  assert.deepEqual(j(P.MODE_OPTIONS.map((o) => [o.key, o.value])), [['protected', 'protected'], ['aggressive', 'aggressive'], ['none', null]]);
});

test('keys and kinds', () => {
  assert.deepEqual([P.modeKey('protected'), P.modeKey('aggressive'), P.modeKey(null), P.modeKey(undefined), P.modeKey('normal')],
    ['protected', 'aggressive', 'none', 'none', 'none']);
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
