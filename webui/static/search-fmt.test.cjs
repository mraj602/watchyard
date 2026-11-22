// node --test webui/static/*.test.cjs
// Session search (components/search-fmt.js, pwa.md 3.0b).

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
    { Number, String, Array, Object, Math, Map, Set });
};

const S = mod('components/search-fmt.js');
const j = (x) => JSON.parse(JSON.stringify(x));
const join = (segs) => segs.map((g) => g.text).join('');
const hits = (segs) => j(segs.filter((g) => g.hit).map((g) => g.text));

test('folding ignores case and accents, nothing else', () => {
  assert.equal(S.fold('Café CRÈME'), 'cafe creme');
  assert.equal(S.fold('Ångström naïve Ñandú'), 'angstrom naive nandu');
  assert.equal(S.fold('İstanbul'), 'istanbul');
  assert.equal(S.fold('Straße'), 'straße'); // no ß → ss
  assert.equal(S.fold('e\u0301'), 'e'); // already decomposed
  assert.equal(S.fold(null), '');
  assert.equal(S.fold(42), '42');
});

test('foldMap points back at the original characters', () => {
  const m = S.foldMap('aé😀b');
  assert.equal(m.f, 'ae😀b');
  assert.deepEqual(j(m.start), [0, 1, 2, 2, 4]);
  assert.deepEqual(j(m.end), [1, 2, 4, 4, 5]);
  assert.deepEqual(j(S.foldMap('Plain')), { f: 'plain', start: null, end: null });
});

test('terms: folded, split, cut, without repeats, at most 8', () => {
  assert.deepEqual(j(S.searchTerms('  Café   cafe\tLOGIN ')), ['cafe', 'login']);
  assert.deepEqual(j(S.searchTerms('')), []);
  assert.deepEqual(j(S.searchTerms('   ')), []);
  assert.deepEqual(j(S.searchTerms(null)), []);
  assert.equal(S.searchTerms('a b c d e f g h i j').length, 8);
  assert.equal(S.searchTerms('x'.repeat(100))[0].length, 64);
  // The box takes 200 characters; what is past them is not searched.
  assert.deepEqual(j(S.searchTerms('a'.repeat(199) + ' zebra')), ['a'.repeat(64)]);
});

test('every term must be found, in any field', () => {
  const t = S.searchTerms('deploy CAFÉ');
  assert.equal(S.matches(['deploy the api', 'café'], t), true);
  assert.equal(S.matches(['deploy the api cafe'], t), true);
  assert.equal(S.matches(['deploy the api'], t), false);
  assert.equal(S.matches([], []), true);
  assert.equal(S.matches([null, undefined], S.searchTerms('x')), false);
});

test('highlight ranges map to the original text and merge', () => {
  assert.deepEqual(j(S.hitRanges('Café and CAFE', ['cafe'])), [[0, 4], [9, 13]]);
  assert.deepEqual(j(S.hitRanges('abcd', ['ab', 'bc'])), [[0, 3]]);
  assert.deepEqual(j(S.hitRanges('aaa', ['aa'])), [[0, 3]]);
  assert.deepEqual(j(S.hitRanges('e\u0301te\u0301', ['et'])), [[0, 3]]);
  assert.deepEqual(j(S.hitRanges('e\u0301te\u0301', ['te'])), [[2, 5]]); // the mark goes with its letter
  assert.deepEqual(j(S.hitRanges('cafe\u0301', ['cafe'])), [[0, 5]]);
  assert.deepEqual(j(S.hitRanges('x', [])), []);
  const segs = S.highlight('Fix the Café login', S.searchTerms('cafe LOG'));
  assert.equal(join(segs), 'Fix the Café login');
  assert.deepEqual(hits(segs), ['Café', 'log']);
  assert.deepEqual(j(S.highlight('', ['a'])), []);
  assert.deepEqual(j(S.highlight('abc', [])), [{ text: 'abc', hit: false }]);
});

test('excerpt: about 80 characters around the first match', () => {
  const long = `${'word '.repeat(40)}the needle is here ${'tail '.repeat(40)}`;
  const e = S.excerpt(long, ['needle']);
  const text = join(e);
  assert.ok(text.startsWith('…') && text.endsWith('…'));
  assert.ok(text.length <= 84, text.length);
  assert.deepEqual(hits(e), ['needle']);
  // Short text: whole, no ellipsis; white space collapsed.
  assert.deepEqual(j(S.excerpt('one\n\ntwo  needle', ['needle'])), [{ text: 'one two ', hit: false }, { text: 'needle', hit: true }]);
  // A match near the end keeps the window full width.
  const end = S.excerpt(`${'a'.repeat(200)}needle`, ['needle']);
  assert.equal(end[0].text, '…');
  assert.equal(join(end).length, 81);
  assert.equal(S.excerpt('nothing here', ['needle']), null);
  // Centred on `around`, every term highlighted.
  const both = S.excerpt('alpha beta gamma', ['alpha', 'gamma'], 80, ['gamma']);
  assert.deepEqual(hits(both), ['alpha', 'gamma']);
  // A surrogate pair at the cut is kept whole.
  const emoji = S.excerpt(`${'😀'.repeat(60)}needle`, ['needle'], 10);
  assert.ok(!/^[\udc00-\udfff]/.test(join(emoji).slice(1)));
});

test('a session matches on folder, title, name and the messages', () => {
  const s = { project: 'api', title: 'Fix the login', name: 'login-fix', last_message: 'I pushed the café menu change.', question: null };
  const shown = { folder: 'api', sub: 'Fix the login', last: 'I pushed the café menu change.' };
  assert.deepEqual(j(S.searchSession(s, shown, [])), { extra: null });
  assert.deepEqual(j(S.searchSession(s, shown, S.searchTerms('API cafe'))), { extra: null });
  assert.equal(S.searchSession(s, shown, S.searchTerms('api deploy')), null);
  // The name is not on the row (the title is): one extra line.
  const r = S.searchSession(s, shown, S.searchTerms('login-fix'));
  assert.equal(r.extra.key, 'name');
  assert.equal(r.extra.label, 'Name');
  assert.deepEqual(hits(r.extra.segments), ['login-fix']);
  // The row shows a permission request; the last message is searched and shown as an extra line.
  const p = { ...s, state: 'needs_permission' };
  const r2 = S.searchSession(p, { folder: 'api', sub: 'Fix the login', last: 'Claude needs your permission to use Bash' }, S.searchTerms('menu'));
  assert.equal(r2.extra.key, 'message');
  assert.equal(r2.extra.label, 'Message');
  assert.deepEqual(hits(r2.extra.segments), ['menu']);
});

test('the folder placeholder is never searched', () => {
  assert.equal(S.searchFolder({}), '');
  assert.equal(S.searchFolder({ cwd: '/home/me/src/web/' }), 'web');
  assert.equal(S.searchFolder({ project: 'api', cwd: '/x/y' }), 'api');
  assert.equal(S.searchSession({}, { folder: S.searchFolder({}) }, S.searchTerms('project')), null);
});
