// Session search (pwa.md 3.0b): folding, query terms, matching, highlight ranges and excerpts.
// Pure, with no imports: node's tests and the phone's port (phone/src/lib/format/search.ts,
// checked on one case table) load it as is. Nothing here leaves the device.

export const MAX_QUERY = 200; // characters the box takes
export const MAX_TERMS = 8;
export const MAX_TERM = 64; // characters per term
export const EXCERPT = 80; // characters of an extra match line

// Combining marks dropped after canonical decomposition. Explicit ranges rather than \p{M}, so
// every JavaScript engine the apps run on reads it the same way.
const MARKS = /[\u0300-\u036f\u1ab0-\u1aff\u1dc0-\u1dff\u20d0-\u20ff\ufe20-\ufe2f]/g;
const ASCII = /^[\x00-\x7f]*$/;

function foldChar(c) {
  let d = c;
  try { d = c.normalize('NFD'); } catch (_) { /* no normalize: keep the character */ }
  return d.replace(MARKS, '').toLowerCase().replace(MARKS, '');
}

const str = (v) => (v == null ? '' : String(v));

// foldMap folds text one character (code point) at a time and says where each folded unit came
// from: unit k of `f` was made by text[start[k], end[k]) (with any combining marks after it).
// Plain ASCII needs no map (null).
export function foldMap(text) {
  const s = str(text);
  if (ASCII.test(s)) return { f: s.toLowerCase(), start: null, end: null };
  let f = '';
  const start = [];
  const end = [];
  let i = 0;
  let prev = 0; // first unit of the last character that folded to something
  for (const c of s) {
    const x = foldChar(c);
    if (!x.length) {
      // A lone combining mark belongs to the character before it: a highlight takes it along.
      for (let k = prev; k < end.length; k++) end[k] = i + c.length;
    } else {
      prev = f.length;
      for (let k = 0; k < x.length; k++) { start.push(i); end.push(i + c.length); }
    }
    f += x;
    i += c.length;
  }
  return { f, start, end };
}

// Folded text, cached: a redraw folds the same few hundred strings again.
const cache = new Map();
export function fold(text) {
  const s = str(text);
  if (ASCII.test(s)) return s.toLowerCase();
  let f = cache.get(s);
  if (f === undefined) {
    if (cache.size >= 5000) cache.clear();
    f = foldMap(s).f;
    cache.set(s, f);
  }
  return f;
}

// The query's terms: folded, split on white space, each cut to MAX_TERM characters, without
// repeats, at most MAX_TERMS. An empty list means "show everything".
export function searchTerms(query) {
  const words = fold(str(query).slice(0, MAX_QUERY)).split(/\s+/).filter(Boolean)
    .map((w) => [...w].slice(0, MAX_TERM).join(''));
  return [...new Set(words)].slice(0, MAX_TERMS);
}

// A term `tag:<name>` (the prefix in any case; terms are folded) asks for exactly that tag and
// is never matched against other fields. `tag:` alone has no name and matches nothing.
export const TAG_PREFIX = 'tag:';
export const isTagTerm = (t) => t.startsWith(TAG_PREFIX);
const tagName = (t) => t.slice(TAG_PREFIX.length);

// matches: every term is found in at least one of the texts, or in a tag (a plain term by
// substring, a `tag:<name>` term only by equality).
export function matches(texts, terms, tags = []) {
  if (!terms.length) return true;
  const fs = texts.map(fold);
  const ts = tags.map(fold);
  return terms.every((t) => {
    if (isTagTerm(t)) return tagName(t) !== '' && ts.includes(tagName(t));
    return fs.some((f) => f.includes(t)) || ts.some((f) => f.includes(t));
  });
}

// The terms to highlight in one tag chip: the plain terms, and the tag itself when a
// `tag:<name>` term names it.
export function tagTerms(tag, terms) {
  const f = fold(tag);
  return terms.filter((t) => !isTagTerm(t) || tagName(t) === f).map((t) => (isTagTerm(t) ? f : t));
}

// hitRanges: where the terms are in text, as sorted, merged [start, end) ranges of the
// original text.
export function hitRanges(text, terms) {
  const s = str(text);
  if (!s || !terms.length) return [];
  const { f, start, end } = foldMap(s);
  const raw = [];
  for (const t of terms) {
    if (!t || isTagTerm(t)) continue;
    for (let i = f.indexOf(t); i >= 0; i = f.indexOf(t, i + 1)) {
      raw.push(start ? [start[i], end[i + t.length - 1]] : [i, i + t.length]);
    }
  }
  raw.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const out = [];
  for (const r of raw) {
    const last = out[out.length - 1];
    if (last && r[0] <= last[1]) last[1] = Math.max(last[1], r[1]);
    else out.push([r[0], r[1]]);
  }
  return out;
}

// segments cuts text into [{text, hit}] pieces along ranges; joined, they are the text again.
export function segments(text, ranges) {
  const s = str(text);
  const out = [];
  let at = 0;
  for (const [a, b] of ranges) {
    if (a > at) out.push({ text: s.slice(at, a), hit: false });
    if (b > a) out.push({ text: s.slice(a, b), hit: true });
    at = Math.max(at, b);
  }
  if (at < s.length) out.push({ text: s.slice(at), hit: false });
  return out;
}

export const highlight = (text, terms) => segments(text, hitRanges(text, terms));

// excerpt: about `width` characters of text around the first match of `around` (default: the
// terms), white space collapsed, `…` where it was cut, every term highlighted. Null when
// nothing in `around` is found.
export function excerpt(text, terms, width = EXCERPT, around = terms) {
  const s = str(text);
  const first = hitRanges(s, around)[0];
  if (!first) return null;
  const [a, b] = first;
  let from = Math.max(0, a - Math.floor(Math.max(0, width - (b - a)) / 2));
  let to = Math.min(s.length, Math.max(b, from + width));
  from = Math.max(0, Math.min(from, to - width, a));
  // Never cut a surrogate pair in half.
  if (from > 0 && /[\udc00-\udfff]/.test(s[from])) from -= 1;
  if (to < s.length && /[\udc00-\udfff]/.test(s[to])) to += 1;
  const part = s.slice(from, to);
  const ranges = hitRanges(part, terms);
  const out = segments(part, ranges).map((g) => ({ text: g.text.replace(/\s+/g, ' '), hit: g.hit }));
  if (out.length && !out[0].hit) out[0].text = out[0].text.replace(/^ /, '');
  if (out.length && !out[out.length - 1].hit) out[out.length - 1].text = out[out.length - 1].text.replace(/ $/, '');
  if (from > 0) out.unshift({ text: '…', hit: false });
  if (to < s.length) out.push({ text: '…', hit: false });
  return out.filter((g) => g.text);
}

// The folder a session is searched by: what the row shows, but never the "(no project)"
// placeholder.
export function searchFolder(s) {
  return str(s && (s.project || (s.cwd ? s.cwd.split('/').filter(Boolean).pop() : '')));
}

// The fields a session is searched by. `shown` holds what its row shows: folder, sub (the
// title or name after the folder) and last (the last line).
export function sessionFields(s, shown) {
  return [
    ['folder', str(shown && shown.folder)],
    ['sub', str(shown && shown.sub)],
    ['last', str(shown && shown.last)],
    ['title', str(s && s.title)],
    ['name', str(s && s.name)],
    ['message', str(s && s.last_message)],
    ['question', str(s && s.question)],
  ];
}

const SHOWN = new Set(['folder', 'sub', 'last']);
export const EXTRA_LABELS = { title: 'Title', name: 'Name', message: 'Message', question: 'Question' };

// searchSession: null when the session does not match; else {extra}, where extra is null or
// {key, label, segments}: the first field the row does not show that holds a term found
// nowhere on the row, as a highlighted excerpt.
export function searchSession(s, shown, terms) {
  const fields = sessionFields(s, shown);
  const tags = (s && Array.isArray(s.tags) ? s.tags : []).map(str);
  if (!matches(fields.map((f) => f[1]), terms, tags)) return null;
  if (!terms.length) return { extra: null };
  // The chips are on the row too: a term found there needs no extra line.
  const onRow = fields.filter(([k]) => SHOWN.has(k)).map(([, t]) => fold(t)).concat(tags.map(fold));
  const unseen = terms.filter((t) => !isTagTerm(t) && !onRow.some((f) => f.includes(t)));
  if (!unseen.length) return { extra: null };
  for (const [k, t] of fields) {
    if (SHOWN.has(k)) continue;
    const segs = excerpt(t, terms, EXCERPT, unseen);
    if (segs) return { extra: { key: k, label: EXTRA_LABELS[k], segments: segs } };
  }
  return { extra: null };
}
