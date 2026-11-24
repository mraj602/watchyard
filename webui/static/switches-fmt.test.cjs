// node --test webui/static/*.test.cjs
// The Switches page's words, filters and session path (components/switches-fmt.js, setup.md 8.6).

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
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { String, Math, Date, URLSearchParams, Number, RegExp, Array });
};

const j = (x) => JSON.parse(JSON.stringify(x));
const S = mod('components/switches-fmt.js');

const mv = (over) => ({ at: '2026-11-24T10:00:00.000Z', session_id: '5e55a0a0-1111', session_name: 'api', kind: 'background', from: 'claude-3', to: 'claude-2',
  auto: true, result: 'resumed', error: null, p5: null, p7: null, cap5: null, cap7: null, burn5: null, move_no: 1, ...over });

test('kinds and modes in plain words', () => {
  assert.equal(S.kindText('tmux'), 'interactive, in a tmux pane');
  assert.equal(S.kindShort('wy-run'), 'interactive');
  assert.equal(S.kindShort('headless'), 'headless');
  assert.equal(S.kindShort('background'), 'background');
  assert.equal(S.kindText('weird'), 'weird');
  assert.equal(S.modeText({ auto: true }), 'Automatic');
  assert.equal(S.modeText({ auto: false }), 'By hand');
});

test('projected numbers: what the machine knew, nothing for a move by hand', () => {
  assert.deepEqual(j(S.projectedParts(mv({}))), []);
  assert.deepEqual(j(S.projectedParts(mv({ p5: 96.6, cap5: 95, burn5: 12.34 }))), ['5 h projected 97% (cap 95%)', 'burning 12.3 points an hour']);
  assert.deepEqual(j(S.projectedParts(mv({ p7: 91, cap7: 90, burn5: 0 }))), ['week projected 91% (cap 90%)']);
  assert.deepEqual(j(S.projectedParts(mv({ p5: 80 }))), ['5 h projected 80%']);
});

test('result, move number and session label', () => {
  assert.equal(S.resultText(mv({})), 'Resumed on claude-2');
  assert.equal(S.resultText(mv({ result: 'failed', error: 'claude not found' })), 'Failed: claude not found');
  assert.equal(S.resultText(mv({ result: 'failed', error: null })), 'Failed: no reason given');
  assert.equal(S.moveNoText(mv({ move_no: 1 })), 'first move');
  assert.equal(S.moveNoText(mv({ move_no: 3 })), 'move 3 of this session');
  assert.equal(S.moveNoText(mv({ move_no: null })), '');
  assert.equal(S.sessionLabel(mv({})), 'api');
  assert.equal(S.sessionLabel(mv({ session_name: '' })), '5e55a0a0');
});

test('filters round trip through the address; unknown values are dropped', () => {
  const f = S.parseFilters(new URLSearchParams('machine=m_1&account=claude-3&mode=auto&result=failed&range=30d'));
  assert.deepEqual(j(f), { machine: 'm_1', account: 'claude-3', session: '', mode: 'auto', result: 'failed', range: '30d', from: '', to: '' });
  assert.equal(S.filtersQuery(f), 'machine=m_1&account=claude-3&mode=auto&result=failed&range=30d');
  assert.equal(S.filtersQuery(S.parseFilters(new URLSearchParams(''))), '');
  const bad = S.parseFilters(new URLSearchParams('mode=both&result=x&range=forever&from=yesterday'));
  assert.equal(bad.mode, ''); assert.equal(bad.result, ''); assert.equal(bad.range, '7d'); assert.equal(bad.from, '');
  // "custom" with no dates is the default range.
  assert.equal(S.parseFilters(new URLSearchParams('range=custom')).range, '7d');
  const c = S.parseFilters(new URLSearchParams('range=custom&from=2026-11-01&to=2026-11-03'));
  assert.equal(S.filtersQuery(c), 'range=custom&from=2026-11-01&to=2026-11-03');
});

test('range bounds: presets count back from now, custom dates are whole local days', () => {
  const now = Date.parse('2026-11-25T12:00:00Z');
  assert.equal(S.rangeBounds({ range: '24h' }, now).since, '2026-11-24T12:00:00.000Z');
  assert.equal(S.rangeBounds({ range: '7d' }, now).since, '2026-11-18T12:00:00.000Z');
  assert.deepEqual(j(S.rangeBounds({ range: 'all' }, now)), {});
  const b = S.rangeBounds({ range: 'custom', from: '2026-11-01', to: '2026-11-03' }, now);
  assert.equal(b.since, new Date(2026, 10, 1).toISOString());
  assert.equal(b.until, new Date(2026, 10, 4).toISOString()); // the last day is included
  assert.deepEqual(j(S.rangeBounds({ range: 'custom', from: '2026-11-01', to: '' }, now)), { since: new Date(2026, 10, 1).toISOString() });
});

test('API parameters carry the filters, the page size and the cursor', () => {
  const now = Date.parse('2026-11-25T12:00:00Z');
  const p = S.apiParams({ machine: 'm_1', account: 'claude-3', session: 's1', mode: 'manual', result: 'failed', range: 'all' }, 'cur', now);
  assert.deepEqual(j(p), { limit: '25', machine: 'm_1', account: 'claude-3', session: 's1', mode: 'manual', result: 'failed', cursor: 'cur' });
  assert.deepEqual(j(S.apiParams({ range: '24h' }, '', now)), { limit: '25', since: '2026-11-24T12:00:00.000Z' });
});

test('the strip and the account lines', () => {
  const t = S.stripTiles({ moves: 7, failed: 2, auto: 5, manual: 2, sessions: 4, sessions_twice: 1, by_account: [] });
  assert.deepEqual(j(t.map((x) => [x[0], x[1]])), [['Moves', '7'], ['Sessions moved', '4'], ['Moved 2 or more times', '1'], ['Failed moves', '2']]);
  assert.equal(t[0][2], '5 automatic, 2 by hand');
  assert.equal(S.stripTiles(null).length, 0);
  assert.equal(S.accountLine({ account: 'claude-3', out: 4, in: 1 }), 'claude-3: 4 out, 1 in');
});

test('a session path: accounts in order with the time of each move; failed moves are no step', () => {
  const moves = [
    mv({ at: '2026-11-24T12:00:00Z', from: 'claude-2', to: 'claude-6' }),
    mv({ at: '2026-11-24T11:00:00Z', from: 'claude-3', to: 'claude-2' }),
    mv({ at: '2026-11-24T13:00:00Z', from: 'claude-6', to: 'claude-3', result: 'failed', error: 'x' }),
  ];
  const p = S.movePath(moves);
  assert.deepEqual(j(p.map((x) => x.label)), ['claude-3', 'claude-2', 'claude-6']);
  assert.equal(p[0].at, null);
  assert.equal(S.pathText(p, (t) => t.slice(11, 16)), 'claude-3 → claude-2 (11:00) → claude-6 (12:00)');
  assert.equal(S.failedCount(moves), 1);
  assert.match(S.failedText(1), /^1 move to another account failed/);
  assert.equal(S.failedText(0), '');
  assert.deepEqual(j(S.movePath([])), []);
  // A gap (a move the hub never heard of) starts the path again from where the next one began.
  const gap = S.movePath([mv({ at: '2026-11-24T11:00:00Z', from: 'a', to: 'b' }), mv({ at: '2026-11-24T12:00:00Z', from: 'c', to: 'd' })]);
  assert.deepEqual(j(gap.map((x) => x.label)), ['a', 'b', 'c', 'd']);
});
