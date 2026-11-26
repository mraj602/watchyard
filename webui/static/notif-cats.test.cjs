// node --test webui/static/*.test.cjs
// The notification categories (components/notif-cats.js, push.md mute_categories).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, 'components/notif-cats.js'), 'utf8');
if (/^import /m.test(src)) throw new Error('notif-cats.js must stay pure');
const names = [...src.matchAll(/^export (?:function|const) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
const C = vm.runInNewContext(`(function () {\n${src.replace(/^export /gm, '')}\nreturn { ${names.join(', ')} };\n})()`, {});
const j = (x) => JSON.parse(JSON.stringify(x));

test('the six categories, in the hub\'s names', () => {
  assert.deepEqual(j(C.CATEGORIES.map((c) => c.id)), ['sessions', 'limits', 'switch', 'drain', 'usage', 'machines']);
  assert.equal(C.CATEGORIES.find((c) => c.id === 'switch').label, 'Account switch');
  assert.equal(C.CATEGORIES.find((c) => c.id === 'usage').label, 'Usage notices');
});

test('muted categories: missing means none; unknown names are ignored', () => {
  assert.deepEqual(j(C.mutedCategories(undefined)), []);
  assert.deepEqual(j(C.mutedCategories({})), []);
  assert.deepEqual(j(C.mutedCategories({ mute_categories: 'x' })), []);
  assert.deepEqual(j(C.mutedCategories({ mute_categories: ['drain', 'future', 'sessions'] })), ['sessions', 'drain']);
});

test('setCategory turns one on or off and keeps the rest', () => {
  assert.deepEqual(j(C.setCategory({}, 'drain', false)), ['drain']);
  assert.deepEqual(j(C.setCategory({ mute_categories: ['drain'] }, 'drain', false)), ['drain']);
  assert.deepEqual(j(C.setCategory({ mute_categories: ['drain', 'future'] }, 'drain', true)), ['future']);
  assert.deepEqual(j(C.setCategory(null, 'usage', true)), []);
});
