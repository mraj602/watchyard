// The Switches page's words and filters (setup.md 8.6 Switch log). No imports, so node can
// test it (switches-fmt.test.cjs): times are formatted by the caller.

export const PAGE_SIZE = 25;

// How the session ran. The three interactive kinds got a message where their owner sees it.
const KINDS = {
  tmux: 'interactive, in a tmux pane',
  'wy-run': 'interactive, under wy run',
  terminal: 'interactive, in a terminal',
  background: 'background session',
  headless: 'headless (claude -p)',
};
export const kindText = (k) => KINDS[k] || String(k || 'session');
export const kindShort = (k) => (k === 'tmux' || k === 'wy-run' || k === 'terminal' ? 'interactive' : k === 'headless' ? 'headless' : 'background');

export const modeText = (m) => (m.auto ? 'Automatic' : 'By hand');

const whole = (n) => `${Math.round(n)}%`;
const points = (n) => `${Math.round(n * 10) / 10}`;

// "5 h projected 97% (cap 95%)", "week projected 91% (cap 90%)", "burning 12 points an hour": what
// the machine knew when it moved the session. Empty for a move by hand with no projection.
export function projectedParts(m) {
  const out = [];
  const one = (label, p, cap) => {
    if (p == null) return;
    out.push(`${label} projected ${whole(p)}${cap != null ? ` (cap ${whole(cap)})` : ''}`);
  };
  one('5 h', m.p5, m.cap5);
  one('week', m.p7, m.cap7);
  if (m.burn5 != null && m.burn5 > 0) out.push(`burning ${points(m.burn5)} points an hour`);
  return out;
}

export const resultText = (m) => (m.result === 'failed' ? `Failed: ${m.error || 'no reason given'}` : `Resumed on ${m.to}`);

export function moveNoText(m) {
  if (m.move_no == null) return '';
  return m.move_no === 1 ? 'first move' : `move ${m.move_no} of this session`;
}

// The session's own label: its name, else the first 8 characters of its id.
export const sessionLabel = (m) => m.session_name || String(m.session_id || '').slice(0, 8);

// ---- filters ------------------------------------------------------------------------------

export const RANGES = [['24h', 'Last 24 hours'], ['7d', 'Last 7 days'], ['30d', 'Last 30 days'], ['all', 'All time'], ['custom', 'Dates…']];
const RANGE_MS = { '24h': 864e5, '7d': 7 * 864e5, '30d': 30 * 864e5 };
const DATE_RE = /^(\d{4})-(\d{2})-(\d{2})$/;

// The page's filters from the address's query (a URLSearchParams); unknown values are dropped.
export function parseFilters(q) {
  const get = (k) => (q && q.get ? q.get(k) || '' : '');
  const range = RANGES.some(([id]) => id === get('range')) ? get('range') : '7d';
  const from = DATE_RE.test(get('from')) ? get('from') : '';
  const to = DATE_RE.test(get('to')) ? get('to') : '';
  return {
    machine: get('machine'), account: get('account'), session: get('session'),
    mode: get('mode') === 'auto' || get('mode') === 'manual' ? get('mode') : '',
    result: get('result') === 'failed' ? 'failed' : '',
    range: range === 'custom' && !from && !to ? '7d' : range, from, to,
  };
}

// The inverse: the filters as a query string for the address (defaults left out).
export function filtersQuery(f) {
  const q = new URLSearchParams();
  for (const k of ['machine', 'account', 'session', 'mode', 'result']) if (f[k]) q.set(k, f[k]);
  if (f.range && f.range !== '7d') q.set('range', f.range);
  if (f.range === 'custom') { if (f.from) q.set('from', f.from); if (f.to) q.set('to', f.to); }
  return q.toString();
}

const localDay = (s, plus = 0) => {
  const [, y, m, d] = DATE_RE.exec(s);
  return new Date(Number(y), Number(m) - 1, Number(d) + plus);
};

// {since, until} as RFC 3339 for the API: a preset counts back from now; custom dates are
// whole local days, the last one included.
export function rangeBounds(f, now = Date.now()) {
  const out = {};
  if (f.range === 'custom') {
    if (f.from) out.since = localDay(f.from).toISOString();
    if (f.to) out.until = localDay(f.to, 1).toISOString();
  } else if (RANGE_MS[f.range]) {
    out.since = new Date(now - RANGE_MS[f.range]).toISOString();
  }
  return out;
}

// GET /v1/switches parameters for a filter and a page cursor.
export function apiParams(f, cursor, now = Date.now()) {
  const p = { limit: String(PAGE_SIZE), ...rangeBounds(f, now) };
  if (f.machine) p.machine = f.machine;
  if (f.account) p.account = f.account;
  if (f.session) p.session = f.session;
  if (f.mode) p.mode = f.mode;
  if (f.result) p.result = f.result;
  if (cursor) p.cursor = cursor;
  return p;
}

export const rangeText = (f) => (RANGES.find(([id]) => id === f.range) || RANGES[1])[1].toLowerCase()
  .replace('dates…', f.from || f.to ? `${f.from || 'the start'} to ${f.to || 'today'}` : 'all time');

// ---- the strip and the path ----------------------------------------------------------------

const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// The summary strip's tiles: [label, value, hint].
export function stripTiles(s) {
  if (!s) return [];
  return [
    ['Moves', String(s.moves), s.auto || s.manual ? `${s.auto} automatic, ${s.manual} by hand` : ''],
    ['Sessions moved', String(s.sessions), ''],
    ['Moved 2 or more times', String(s.sessions_twice), s.sessions_twice ? 'a session that keeps moving may point at too little room' : ''],
    ['Failed moves', String(s.failed), s.failed ? 'the session stayed where it was' : ''],
  ];
}

// "claude-3: 4 out, 1 in" for each account, busiest first.
export const accountLine = (a) => `${a.account}: ${a.out} out, ${a.in} in`;

// A session's path from its moves (any order): the account it started on, then each one it went to,
// with the time of the move. A move that failed is not a step. A move that does not start where the
// last ended (a move the hub did not hear of) starts a new, unlabelled-time step.
export function movePath(moves) {
  const ok = (moves || []).filter((m) => m.result !== 'failed').sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
  const out = [];
  for (const m of ok) {
    if (!out.length || out[out.length - 1].label.toLowerCase() !== String(m.from).toLowerCase()) out.push({ label: m.from, at: null });
    out.push({ label: m.to, at: m.at });
  }
  return out;
}

// "claude-3 → claude-2 (14:02) → claude-6 (16:40)": fmt turns a time into text.
export const pathText = (path, fmt) => path.map((p) => (p.at ? `${p.label} (${fmt(p.at)})` : p.label)).join(' → ');

export const failedCount = (moves) => (moves || []).filter((m) => m.result === 'failed').length;
export const failedText = (n) => (n ? `${plural(n, 'move')} to another account failed and left it where it was` : '');
