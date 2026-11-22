// Session search parity (pwa.md 3.0b): src/lib/format/search.ts and the board's
// webui/static/components/search-fmt.js give the same output on one case table, and a few of
// the cases are pinned to their expected output.
import * as fs from 'fs';
import * as path from 'path';
import * as port from '../src/lib/format/search';

// The board's module as the browser runs it: its source with `export` dropped, not passed
// through Babel (which would add helper imports the webui folder cannot resolve).
function loadBoard(): Record<string, unknown> {
  const src = fs.readFileSync(path.join(__dirname, '../../webui/static/components/search-fmt.js'), 'utf8');
  if (/^import /m.test(src)) throw new Error('search-fmt.js must stay free of imports');
  const names = [...src.matchAll(/^export (?:function|const) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  return new Function(`${src.replace(/^export /gm, '')}\nreturn { ${names.join(', ')} };`)() as Record<string, unknown>;
}
const webui = loadBoard();

type Fn = (...a: unknown[]) => unknown;
const run = (m: Record<string, unknown>, fn: string, args: unknown[]) => (m[fn] as Fn)(...args);

const LONG = `${'word '.repeat(40)}the needle is here ${'tail '.repeat(40)}`;
const ses = {
  project: 'api',
  cwd: '/home/me/api',
  title: 'Fix the login',
  name: 'login-fix',
  last_message: 'I pushed the Café menu change.\n\nShould I deploy?',
  question: 'Should I deploy?',
};
const shown = { folder: 'api', sub: 'Fix the login', last: 'Claude needs your permission to use Bash' };

// [function, arguments]
const CASES: [string, unknown[]][] = [
  ['fold', ['Café CRÈME']],
  ['fold', ['e\u0301']],
  ['fold', ['İstanbul Straße Ñandú']],
  ['fold', [null]],
  ['fold', [42]],
  ['foldMap', ['aé😀b']],
  ['foldMap', ['e\u0301te\u0301']],
  ['foldMap', ['plain']],
  ['searchTerms', ['  Café   cafe\tLOGIN ']],
  ['searchTerms', ['']],
  ['searchTerms', [null]],
  ['searchTerms', ['a b c d e f g h i j']],
  ['searchTerms', ['x'.repeat(100)]],
  ['searchTerms', [`${'a'.repeat(199)} zebra`]],
  ['matches', [['deploy the api', 'café'], ['deploy', 'cafe']]],
  ['matches', [['deploy the api'], ['deploy', 'cafe']]],
  ['matches', [[], []]],
  ['hitRanges', ['Café and CAFE', ['cafe']]],
  ['hitRanges', ['abcd', ['ab', 'bc']]],
  ['hitRanges', ['aaa', ['aa']]],
  ['hitRanges', ['e\u0301te\u0301', ['te']]],
  ['hitRanges', ['cafe\u0301', ['cafe']]],
  ['hitRanges', ['😀 needle 😀', ['needle']]],
  ['highlight', ['Fix the Café login', ['cafe', 'log']]],
  ['highlight', ['', ['a']]],
  ['highlight', ['abc', []]],
  ['excerpt', [LONG, ['needle']]],
  ['excerpt', ['one\n\ntwo  needle', ['needle']]],
  ['excerpt', [`${'a'.repeat(200)}needle`, ['needle']]],
  ['excerpt', ['nothing here', ['needle']]],
  ['excerpt', ['alpha beta gamma', ['alpha', 'gamma'], 80, ['gamma']]],
  ['excerpt', [`${'😀'.repeat(60)}needle`, ['needle'], 10]],
  ['searchFolder', [{}]],
  ['searchFolder', [{ cwd: '/home/me/src/web/' }]],
  ['searchFolder', [{ project: 'api', cwd: '/x/y' }]],
  ['sessionFields', [ses, shown]],
  ['searchSession', [ses, shown, []]],
  ['searchSession', [ses, shown, ['api']]],
  ['searchSession', [ses, shown, ['login-fix']]],
  ['searchSession', [ses, shown, ['menu']]],
  ['searchSession', [ses, shown, ['deploy']]],
  ['searchSession', [ses, shown, ['api', 'nowhere']]],
  ['searchSession', [{}, { folder: '' }, ['project']]],
];

describe('search port equals the board module', () => {
  it('exports the same names', () => {
    expect(Object.keys(port).sort()).toEqual(Object.keys(webui).sort());
  });

  it.each(CASES.map(([fn, args]) => [`${fn}(${JSON.stringify(args).slice(0, 80)})`, fn, args] as const))('%s', (_n, fn, args) => {
    expect(run(port as unknown as Record<string, unknown>, fn, args)).toEqual(run(webui, fn, args));
  });
});

describe('search, pinned', () => {
  it('folds case and accents only', () => {
    expect(port.fold('Café CRÈME')).toBe('cafe creme');
    expect(port.fold('İstanbul Straße Ñandú')).toBe('istanbul straße nandu');
    expect(port.searchTerms('  Café   cafe\tLOGIN ')).toEqual(['cafe', 'login']);
  });

  it('marks matches in the original text', () => {
    const segs = port.highlight('Fix the Café login', port.searchTerms('CAFE log'));
    expect(segs.map((g) => g.text).join('')).toBe('Fix the Café login');
    expect(segs.filter((g) => g.hit).map((g) => g.text)).toEqual(['Café', 'log']);
    expect(port.hitRanges('cafe\u0301', ['cafe'])).toEqual([[0, 5]]);
  });

  it('adds the line of a field the row does not show', () => {
    expect(port.searchSession(ses, shown, ['api'])).toEqual({ extra: null });
    expect(port.searchSession(ses, shown, ['api', 'nowhere'])).toBeNull();
    const r = port.searchSession(ses, shown, ['menu']);
    expect(r?.extra?.label).toBe('Message');
    expect(r?.extra?.segments.map((g) => g.text).join('')).toBe('I pushed the Café menu change. Should I deploy?');
    expect(port.searchSession(ses, shown, ['login-fix'])?.extra?.label).toBe('Name');
  });

  it('cuts a long message around the match', () => {
    const e = port.excerpt(LONG, ['needle'])!;
    const text = e.map((g) => g.text).join('');
    expect(text.startsWith('…') && text.endsWith('…')).toBe(true);
    expect(text.length).toBeLessThanOrEqual(84);
  });
});
