// node --test webui/static/*.test.cjs
// The labels of alert reasons on the board (components/alerts-fmt.js, push.md 3).

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
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { String });
};

const { reasonText, REASONS, actionable, seenTarget, canSnoozeRow } = mod('components/alerts-fmt.js');

test('peers has its own label', () => {
  assert.equal(reasonText('peers'), 'talking to another session (peers)');
});

test('every known reason has a label, an unknown one reads as spaced words, none as empty', () => {
  for (const r of ['remote_control', 'rule_off', 'stale', 'superseded', 'baseline', 'answered', 'peers', 'ended', 'recovered', 'reset_passed', 'seen']) {
    assert.ok(REASONS[r], r);
    assert.equal(reasonText(r), REASONS[r]);
  }
  assert.equal(reasonText('some_new_reason'), 'some new reason');
  assert.equal(reasonText(null), '');
  assert.equal(reasonText(''), '');
});

// The three Needs-you cards seen live on 1.1.0 with neither Seen nor the ⋯ menu (push.md 4).
const skipped = (id, reason) => ({ id: `al_${id}`, trigger: 'asked', status: 'skipped', reason });
const live = [
  { id: 's-stale-asked', state: 'asked', needs_you: true, state_since: '2026-11-18T09:00:00.000Z', alert: skipped('a', 'stale') },
  { id: 's-stale-perm', state: 'needs_permission', needs_you: true, state_since: '2026-11-19T09:00:00.000Z', alert: { ...skipped('b', 'stale'), trigger: 'needs_permission' } },
  { id: 's-peers', state: 'asked', needs_you: true, state_since: '2026-11-06T09:00:00.000Z', alert: skipped('c', 'peers') },
];

test('every live Needs-you card with a skipped alert can be marked seen, none snoozed', () => {
  for (const s of live) {
    assert.deepEqual({ ...seenTarget(s) }, { alert: s.alert.id }, s.id);
    assert.equal(canSnoozeRow(s), false, s.id);
  }
});

test('a Needs-you row with no alert is seen through the session', () => {
  assert.deepEqual({ ...seenTarget({ id: 's1', state: 'asked', needs_you: true, alert: null }) }, { session: 's1' });
  assert.deepEqual({ ...seenTarget({ id: 's1', state: 'asked', needs_you: true }) }, { session: 's1' });
});

test('alerts that can still push get Seen and Snooze; seen rows and rows that do not need you get neither', () => {
  for (const status of ['pending', 'fired', 'snoozed']) {
    const s = { id: 's1', needs_you: true, alert: { id: 'al_1', status } };
    assert.ok(actionable(s.alert));
    assert.deepEqual({ ...seenTarget(s) }, { alert: 'al_1' });
    assert.equal(canSnoozeRow(s), true);
  }
  assert.equal(seenTarget({ id: 's1', needs_you: true, alert: { id: 'al_1', status: 'seen' } }), null);
  assert.equal(seenTarget({ id: 's1', needs_you: false, alert: skipped('d', 'stale') }), null);
  assert.equal(seenTarget({ id: 's1', needs_you: false, alert: null }), null);
  assert.equal(seenTarget({ id: 's1', needs_you: true, alert: { id: 'al_1', status: 'cancelled' } }), null);
  assert.equal(canSnoozeRow({ id: 's1', needs_you: true, alert: null }), false);
});
