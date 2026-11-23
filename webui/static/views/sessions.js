// Sessions: grouped by account (or machine / project), with state filters and a search box
// (pwa.md 3.0b). Bare imported history (no project, folder or text) folds into one line at the
// end.

import { h } from '../components/dom.js';
import { dot, sessionLink, askText } from '../components/cards.js';
import { seatIndex, sessionSeat } from '../components/seat.js';
import { stateTag, isLive, isHistory, isEnded } from '../components/state.js';
import { pct, ago, plural, projectOf, subtitleOf } from '../format.js';
import { ensureSessions } from '../store.js';
import { alertLine } from '../components/alerts.js';
import { MAX_QUERY, searchTerms, searchSession, searchFolder, highlight, tagTerms, TAG_PREFIX } from '../components/search-fmt.js';

export const load = () => ensureSessions();

const DAYS_LOADED = 2; // api.js getSessions asks for the last 2 days

// `empty`: the sentence when the filter has nothing.
const FILTERS = [
  { id: 'open', label: 'Open', test: (s, now) => isLive(s, now), empty: 'No open sessions.',
    title: 'Sessions that are working, waiting on you, or finished in the last 12 hours' },
  { id: 'needs_you', label: 'Needs you', test: (s) => s.needs_you && !(s.alert && s.alert.status === 'seen'), empty: 'Nothing needs you.' },
  { id: 'working', label: 'Working', test: (s) => s.state === 'working' && isLive(s), empty: 'No sessions are working.' },
  { id: 'done', label: 'Done', test: (s, now) => s.state === 'done' && isLive(s, now), empty: 'No session finished a turn recently.',
    title: 'Claude finished its turn and is waiting for your next prompt' },
  // Not open: ended, or idle for 12 h (a row there says which: Ended, or its state and "idle").
  { id: 'ended', label: 'Ended or idle', test: (s, now) => !isLive(s, now), empty: 'No sessions ended or went idle in the last 2 days.',
    title: 'Sessions that ended, or that have been idle for more than 12 hours' },
  // Not a chip: #/sessions?filter=all shows everything, ended included.
  { id: 'all', label: 'All', test: () => true, empty: 'No sessions in the last 2 days.', hidden: true },
];
const GROUPS = { account: 'Account', machine: 'Machine', project: 'Project' };

// q: the search box's text. Kept for the tab, never in the address (it may quote a message).
const ui = { history: false, q: '' };
const again = () => window.dispatchEvent(new Event('wy-rerender'));

// The search box is kept in place through redraws (components/keep.js), so its handlers only
// read module state.
const searchBox = () => document.getElementById('session-search');
function onSearch(e) { ui.q = e.target.value; again(); }
function onSearchKey(e) {
  if (e.key !== 'Escape' || !ui.q) return;
  e.preventDefault();
  ui.q = '';
  again();
}
// A tag chip: the search becomes `tag:<name>` (what was there is replaced).
function searchTag(tag) {
  ui.q = `${TAG_PREFIX}${tag}`;
  again();
}
function clearSearch() {
  ui.q = '';
  again();
  const box = searchBox();
  if (box) box.focus();
}

// Found terms as <mark>s.
const marked = (text, terms) => highlight(text, terms).map((g) => (g.hit ? h('mark', {}, g.text) : g.text));

// The row's last line: what Claude said or asked; never the hub's internal `detail` code. A
// session that needs you reads like its card on Now (a full limit names its reset, N9).
const lastOf = (s, idx, now) => (s.needs_you ? askText(s, idx, now) : (s.last_message || s.question || ''));

function setQuery(route, patch) {
  const q = new URLSearchParams(route.query);
  for (const [k, v] of Object.entries(patch)) (v ? q.set(k, v) : q.delete(k));
  const s = q.toString();
  location.hash = `#/sessions${s ? '?' + s : ''}`;
}

export function render({ store, route, now }) {
  const o = store.overview;
  const idx = seatIndex(o.seats);
  const all = store.sessions;
  const filterId = FILTERS.some((f) => f.id === route.query.get('filter')) ? route.query.get('filter') : 'open';
  const group = GROUPS[route.query.get('by')] ? route.query.get('by') : 'account';
  const machineFilter = route.query.get('machine') || '';

  // Search over what the list holds; found: session id → its match (extra line or null).
  const terms = searchTerms(ui.q);
  const found = new Map();
  for (const s of all || []) {
    const folder = projectOf(s);
    const r = searchSession(s, { folder: searchFolder(s), sub: subtitleOf(s, folder), last: lastOf(s, idx, now) }, terms);
    if (r) found.set(s.id, r);
  }
  const here = (s) => !machineFilter || s.machine === machineFilter;

  const node = h('div', { class: 'page' });
  // Chip counts follow the machine filter and the search, like the rows under them.
  const counts = (f) => (all || []).filter((s) => f.test(s, now) && here(s) && found.has(s.id)).length;
  const f = FILTERS.find((x) => x.id === filterId);
  const underChip = (all || []).filter((s) => f.test(s, now) && here(s));
  const matching = underChip.filter((s) => found.has(s.id));

  // No listeners on the box's ancestors: they are kept through redraws too.
  node.append(h('header', { class: 'page-head' },
    h('h1', {}, 'Sessions'),
    h('div', { class: 'search-box', role: 'search' },
      h('div', { class: 'search-field' },
        h('input', {
          type: 'search', id: 'session-search', 'data-key': 'session-search', 'data-keep': 'session-search', value: ui.q,
          maxlength: MAX_QUERY, placeholder: 'Folder, title, name or message', 'aria-label': 'Search sessions',
          autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', enterkeyhint: 'search',
          'aria-describedby': terms.length && all ? 'session-search-count' : null,
          oninput: onSearch, onkeydown: onSearchKey,
        }),
        ui.q ? h('button', { type: 'button', class: 'search-clear', 'aria-label': 'Clear search', title: 'Clear search', 'data-key': 'session-search-clear', onclick: clearSearch }, '×') : null),
      terms.length && all ? h('span', { id: 'session-search-count', class: 'muted small', role: 'status', 'data-key': 'search-count' },
        `${matching.length} of ${plural(underChip.length, 'session')} ${matching.length === 1 ? 'matches' : 'match'}`) : null),
    h('div', { class: 'filters' },
      h('div', { class: 'chips', role: 'group', 'aria-label': 'Show' }, FILTERS.filter((f) => !f.hidden).map((f) => h('button', {
        type: 'button', class: `chip-btn${f.id === 'needs_you' ? ' chip-needs' : ''}`, 'aria-pressed': String(f.id === filterId), 'data-key': `f-${f.id}`,
        title: f.title, onclick: () => setQuery(route, { filter: f.id === 'open' ? '' : f.id }),
      }, f.label, all ? h('span', { class: 'chip-n' }, ` ${counts(f)}`) : null)),
        // On a phone this chip stands in for the Group by select.
        h('button', { type: 'button', class: 'chip-btn phone-only', 'aria-pressed': String(group === 'machine'), 'data-key': 'by-machine',
          onclick: () => setQuery(route, { by: group === 'machine' ? '' : 'machine' }) }, 'By machine')),
      h('label', { class: 'field desk-only' }, 'Group by ',
        h('select', { 'data-key': 'group', onchange: (e) => setQuery(route, { by: e.target.value === 'account' ? '' : e.target.value }) },
          Object.entries(GROUPS).map(([v, l]) => h('option', { value: v, selected: v === group }, l)))),
      h('label', { class: 'field desk-only' }, 'Machine ',
        h('select', { 'data-key': 'machine', onchange: (e) => setQuery(route, { machine: e.target.value }) },
          h('option', { value: '', selected: !machineFilter }, 'All'),
          o.machines.map((m) => h('option', { value: m.id, selected: m.id === machineFilter }, m.name)))))));

  const foot = h('p', { class: 'muted small foot-note' },
    `Showing sessions active in the last ${DAYS_LOADED} days. “Asked” means Claude stopped with a question for you; “Done” means it finished its turn.`);

  if (!all) { node.append(h('p', { class: 'muted pad' }, store.error || 'Loading sessions…')); return { title: 'Sessions · Watchyard', node }; }

  const history = matching.filter(isHistory);
  const rows = matching.filter((s) => !isHistory(s));
  if (!matching.length) {
    if (!terms.length) node.append(h('p', { class: 'calm', 'data-key': 'sessions-empty' }, f.empty), foot);
    else {
      // Matches under another chip: one button shows them all, the search kept.
      const elsewhere = filterId === 'all' ? 0 : all.filter((s) => here(s) && found.has(s.id)).length;
      node.append(h('p', { class: 'calm', 'data-key': 'sessions-empty' }, `No sessions here match “${ui.q.trim()}”.`,
        elsewhere ? [' ', h('button', { type: 'button', class: 'btn btn-sm', 'data-key': 'search-all', onclick: () => setQuery(route, { filter: 'all' }) }, 'Search all sessions')] : null), foot);
    }
    return { title: 'Sessions · Watchyard', node };
  }

  const look = { now, idx, terms, found };
  if (rows.length) node.append(h('div', { class: 'table-wrap' }, table(groupRows(rows, group, idx), look)));
  else node.append(h('p', { class: 'calm' }, f.empty));

  if (history.length) {
    node.append(h('section', { class: 'history', 'aria-label': 'Older sessions without details', 'data-key': 'history' },
      h('div', { class: 'history-head' },
        h('span', {}, h('b', {}, plural(history.length, 'older session')), h('span', { class: 'muted' }, ' with no project or messages, most likely imported from history')),
        h('button', { type: 'button', class: 'linkish', 'aria-expanded': String(ui.history), 'data-key': 'history-toggle',
          onclick: () => { ui.history = !ui.history; again(); } }, ui.history ? 'Hide' : 'Show')),
      ui.history ? h('div', { class: 'table-wrap' }, table(groupRows(history, group, idx), look)) : null));
  }
  node.append(foot);
  return { title: 'Sessions · Watchyard', node };
}

// Groups in a stable order: those with something that needs you first, then accounts in the
// sidebar's order (machines and projects by name); inside a group, the hub's order.
function groupRows(rows, group, idx) {
  const groups = new Map();
  for (const s of rows) {
    let key, label, colour, sub = '', order = 0, title;
    if (group === 'account') {
      const seat = sessionSeat(s, idx);
      key = s.seat || '_none'; label = seat.label; colour = seat.colour;
      const known = idx.get(s.seat);
      if (known) {
        order = known.order;
        const five = (known.seat.windows || []).find((w) => w.key === 'five_hour');
        sub = five && five.used_pct != null ? `${pct(five.used_pct)} of the 5-hour limit used` : '';
      } else {
        order = 1e6;
        if (!s.seat) title = 'Sessions whose Claude login could not be identified';
      }
    } else if (group === 'machine') { key = s.machine || '_none'; label = s.machine_name || 'Unknown machine'; }
    else { key = projectOf(s); label = key; }
    if (!groups.has(key)) groups.set(key, { label, colour, sub, order, title, rows: [] });
    groups.get(key).rows.push(s);
  }
  const needs = (g) => Number(g.rows.some((s) => s.needs_you));
  return [...groups.values()].sort((a, b) => (needs(b) - needs(a)) || (a.order - b.order) || String(a.label).localeCompare(String(b.label)));
}

function table(ordered, look) {
  const t = h('table', { class: 'sessions-table' },
    h('thead', {}, h('tr', {}, ['State', 'Session', 'Machine', 'Last line', 'Context', 'In state'].map((x) => h('th', { scope: 'col' }, x)))));
  for (const g of ordered) {
    const tb = h('tbody', {},
      h('tr', { class: 'group-row' }, h('th', { colspan: 6, scope: 'colgroup', title: g.title },
        g.colour ? dot(g.colour) : null, g.label, g.title ? h('span', { class: 'muted normal help', 'aria-hidden': 'true' }, ' ⓘ') : null, ' ',
        h('span', { class: 'muted normal' }, `· ${[g.sub, plural(g.rows.length, 'session')].filter(Boolean).join(' · ')}`),
        g.title ? h('span', { class: 'sr-only' }, ` (${g.title})`) : null)));
    for (const s of g.rows) tb.append(sessionRow(s, look));
    t.append(tb);
  }
  return t;
}

// look: {now, idx, terms, found}; found terms are marked, and a term found only in a field the
// row does not show adds that field's excerpt (pwa.md 3.0b).
function sessionRow(s, { now, idx, terms, found }) {
  const last = lastOf(s, idx, now);
  const folder = projectOf(s);
  const sub = subtitleOf(s, folder);
  const extra = (found.get(s.id) || {}).extra;
  const extraLine = extra ? h('span', { class: 'search-extra muted small', 'data-key': `extra-${s.id}` }, `${extra.label}: `,
    extra.segments.map((g) => (g.hit ? h('mark', {}, g.text) : g.text))) : null;
  const tags = Array.isArray(s.tags) ? s.tags : [];
  const chips = tags.length ? h('span', { class: 'tag-chips', 'data-key': `tags-${s.id}` }, tags.map((tag) => h('button', {
    type: 'button', class: 'tag-chip', 'aria-label': `Show sessions tagged ${tag}`, 'data-key': `tag-${s.id}-${tag}`,
    onclick: () => searchTag(tag),
  }, marked(tag, tagTerms(tag, terms))))) : null;
  const inSession = extra && (extra.key === 'title' || extra.key === 'name');
  const ctx = s.context_pct;
  const href = `#/sessions/${encodeURIComponent(s.id)}`;
  const age = ago(s.state_since, now);
  const empty = (v) => (v ? '' : ' is-empty');
  return h('tr', { class: `srow${s.needs_you ? ' srow-needs' : ''}${s.alert && s.alert.status === 'seen' ? ' is-seen' : ''}`, onclick: (e) => { if (!e.target.closest('a, button')) location.hash = href; } },
    h('td', { class: 'c-state', 'data-label': 'State' }, stateTag(s), s.stale ? h('span', { class: 'flag', title: 'No events for 12 hours' }, ' stale')
      : !isEnded(s) && !isLive(s, now) ? h('span', { class: 'flag', title: 'Not used for 12 hours' }, ' idle') : null, s.machine_stale ? h('span', { class: 'flag' }, ' machine quiet') : null),
    h('td', { class: 'c-session', 'data-label': 'Session' }, sessionLink(s, h('b', {}, searchFolder(s) ? marked(folder, terms) : folder)),
      sub ? h('span', { class: 'muted' }, ' · ', marked(sub, terms)) : null, chips, inSession ? extraLine : null),
    h('td', { class: `c-machine${empty(s.machine_name)}`, 'data-label': 'Machine' }, h('span', { class: 'machine-chip', title: s.machine_name || undefined }, s.machine_name || '–')),
    h('td', { class: `c-last${s.state === 'needs_permission' ? ' mono' : ''}${empty(last || (extra && !inSession) || (s.alert && s.needs_you))}`, 'data-label': 'Last line' }, h('span', { class: 'clamp' }, last ? marked(last, terms) : '–'), extra && !inSession ? extraLine : null, s.alert && s.needs_you ? h('span', { class: 'alert-meta' }, alertLine(s.alert, now)) : null),
    h('td', { class: `c-ctx${empty(ctx != null)}`, 'data-label': 'Context' }, ctx == null ? h('span', { class: 'muted' }, '–') : h('span', { class: `ctx${ctx >= 85 ? ' hot' : ''}` },
      h('span', { class: 'meter meter-s', role: 'meter', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': Math.round(ctx), 'aria-label': 'Context used' }, h('span', { class: 'meter-fill', style: { width: `${Math.min(100, ctx)}%` } })),
      h('span', { class: 'mono small' }, pct(ctx)))),
    h('td', { class: 'c-age muted', 'data-label': 'In state' }, age || '–'));
}
