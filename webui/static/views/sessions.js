// Sessions: grouped by account (or machine / project), with state filters. Bare imported
// history (no project, folder or text) folds into one line at the end.

import { h } from '../components/dom.js';
import { dot, sessionLink, askText } from '../components/cards.js';
import { seatIndex, sessionSeat } from '../components/seat.js';
import { stateTag, isLive, isHistory, isEnded } from '../components/state.js';
import { pct, ago, plural, projectOf, subtitleOf } from '../format.js';
import { ensureSessions } from '../store.js';
import { alertLine } from '../components/alerts.js';

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

const ui = { history: false };
const again = () => window.dispatchEvent(new Event('wy-rerender'));

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

  const node = h('div', { class: 'page' });
  // Chip counts follow the machine filter, like the rows under them.
  const counts = (f) => (all || []).filter((s) => f.test(s, now) && (!machineFilter || s.machine === machineFilter)).length;

  node.append(h('header', { class: 'page-head' },
    h('h1', {}, 'Sessions'),
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

  const f = FILTERS.find((x) => x.id === filterId);
  const matching = all.filter((s) => f.test(s, now) && (!machineFilter || s.machine === machineFilter));
  const history = matching.filter(isHistory);
  const rows = matching.filter((s) => !isHistory(s));
  if (!matching.length) {
    node.append(h('p', { class: 'calm', 'data-key': 'sessions-empty' }, f.empty), foot);
    return { title: 'Sessions · Watchyard', node };
  }

  if (rows.length) node.append(h('div', { class: 'table-wrap' }, table(groupRows(rows, group, idx), now, idx)));
  else node.append(h('p', { class: 'calm' }, f.empty));

  if (history.length) {
    node.append(h('section', { class: 'history', 'aria-label': 'Older sessions without details', 'data-key': 'history' },
      h('div', { class: 'history-head' },
        h('span', {}, h('b', {}, plural(history.length, 'older session')), h('span', { class: 'muted' }, ' with no project or messages, most likely imported from history')),
        h('button', { type: 'button', class: 'linkish', 'aria-expanded': String(ui.history), 'data-key': 'history-toggle',
          onclick: () => { ui.history = !ui.history; again(); } }, ui.history ? 'Hide' : 'Show')),
      ui.history ? h('div', { class: 'table-wrap' }, table(groupRows(history, group, idx), now, idx)) : null));
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

function table(ordered, now, idx) {
  const t = h('table', { class: 'sessions-table' },
    h('thead', {}, h('tr', {}, ['State', 'Session', 'Machine', 'Last line', 'Context', 'In state'].map((x) => h('th', { scope: 'col' }, x)))));
  for (const g of ordered) {
    const tb = h('tbody', {},
      h('tr', { class: 'group-row' }, h('th', { colspan: 6, scope: 'colgroup', title: g.title },
        g.colour ? dot(g.colour) : null, g.label, g.title ? h('span', { class: 'muted normal help', 'aria-hidden': 'true' }, ' ⓘ') : null, ' ',
        h('span', { class: 'muted normal' }, `· ${[g.sub, plural(g.rows.length, 'session')].filter(Boolean).join(' · ')}`),
        g.title ? h('span', { class: 'sr-only' }, ` (${g.title})`) : null)));
    for (const s of g.rows) tb.append(sessionRow(s, now, idx));
    t.append(tb);
  }
  return t;
}

function sessionRow(s, now, idx) {
  // The last line is what Claude said or asked; never the hub's internal `detail` code.
  // A session that needs you reads like its card on Now (a full limit names its reset, N9).
  const last = s.needs_you ? askText(s, idx, now) : (s.last_message || s.question || '');
  const ctx = s.context_pct;
  const href = `#/sessions/${encodeURIComponent(s.id)}`;
  const age = ago(s.state_since, now);
  const empty = (v) => (v ? '' : ' is-empty');
  return h('tr', { class: `srow${s.needs_you ? ' srow-needs' : ''}${s.alert && s.alert.status === 'seen' ? ' is-seen' : ''}`, onclick: (e) => { if (!e.target.closest('a')) location.hash = href; } },
    h('td', { class: 'c-state', 'data-label': 'State' }, stateTag(s), s.stale ? h('span', { class: 'flag', title: 'No events for 12 hours' }, ' stale')
      : !isEnded(s) && !isLive(s, now) ? h('span', { class: 'flag', title: 'Not used for 12 hours' }, ' idle') : null, s.machine_stale ? h('span', { class: 'flag' }, ' machine quiet') : null),
    h('td', { class: 'c-session', 'data-label': 'Session' }, sessionLink(s, h('b', {}, projectOf(s))), subtitleOf(s, projectOf(s)) ? h('span', { class: 'muted' }, ` · ${subtitleOf(s, projectOf(s))}`) : null),
    h('td', { class: `c-machine${empty(s.machine_name)}`, 'data-label': 'Machine' }, h('span', { class: 'machine-chip', title: s.machine_name || undefined }, s.machine_name || '–')),
    h('td', { class: `c-last${s.state === 'needs_permission' ? ' mono' : ''}${empty(last || (s.alert && s.needs_you))}`, 'data-label': 'Last line' }, h('span', { class: 'clamp' }, last || '–'), s.alert && s.needs_you ? h('span', { class: 'alert-meta' }, alertLine(s.alert, now)) : null),
    h('td', { class: `c-ctx${empty(ctx != null)}`, 'data-label': 'Context' }, ctx == null ? h('span', { class: 'muted' }, '–') : h('span', { class: `ctx${ctx >= 85 ? ' hot' : ''}` },
      h('span', { class: 'meter meter-s', role: 'meter', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': Math.round(ctx), 'aria-label': 'Context used' }, h('span', { class: 'meter-fill', style: { width: `${Math.min(100, ctx)}%` } })),
      h('span', { class: 'mono small' }, pct(ctx)))),
    h('td', { class: 'c-age muted', 'data-label': 'In state' }, age || '–'));
}
