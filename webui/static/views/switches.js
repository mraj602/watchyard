// Switches: the log of every Smart Account Switch move on every machine (setup.md 8.6, Switch
// log), newest first, with filters, a summary of the chosen range and pages. A session's own
// path is on its page; a click here opens it.

import { h } from '../components/dom.js';
import { moreTabs } from '../components/ui.js';
import { seatName } from '../components/seat.js';
import { clock, dateTime, plural } from '../format.js';
import { isOwner } from '../store.js';
import { getSwitches } from '../api.js';
import {
  RANGES, parseFilters, filtersQuery, apiParams, rangeText, stripTiles, accountLine, kindText, modeText,
  projectedParts, resultText, moveNoText, sessionLabel,
} from '../components/switches-fmt.js';

// What was asked for and what came back. key: the filters and page of `data`; stack: the cursors
// of the pages before this one (Newer pops one).
const state = { key: '', data: null, error: null, loading: false, stack: [], cursor: '', route: '' };
const again = () => window.dispatchEvent(new Event('wy-rerender'));

export function load() {
  state.key = ''; state.route = ''; // fetch again when the page is opened
}

async function fetchPage(filters, key) {
  state.loading = true;
  try {
    const d = await getSwitches(apiParams(filters, state.cursor));
    if (state.key !== key) return;
    state.data = d; state.error = null;
  } catch (e) {
    if (state.key === key && !(e && e.name === 'AuthError')) state.error = e.message || 'Could not load the switch log';
  } finally {
    if (state.key === key) { state.loading = false; again(); }
  }
}

const go = (f) => { const q = filtersQuery(f); location.hash = `#/switches${q ? `?${q}` : ''}`; };
const change = (f, patch) => go({ ...f, ...patch });

function accountOptions(store, data, current) {
  const names = new Set((store.overview.seats || []).filter((s) => !s.hidden).map((s) => seatName(s)));
  for (const a of (data && data.summary.by_account) || []) names.add(a.account);
  if (current) names.add(current);
  return [...names].sort((a, b) => a.localeCompare(b));
}

function filterBar(store, f, data) {
  const sel = (label, key, value, options, patch) => h('label', { class: 'sw-field' }, h('span', { class: 'muted small' }, label),
    h('select', { 'data-key': key, 'aria-label': label, onchange: (e) => change(f, patch(e.target.value)) },
      options.map(([v, text]) => h('option', { value: v, selected: v === value }, text))));
  const machines = (store.overview.machines || []).filter((m) => !m.removed_at);
  const range = h('div', { class: 'sw-dates' },
    sel('Dates', 'sw-range', f.range, RANGES, (v) => ({ range: v })),
    f.range === 'custom' ? [
      h('label', { class: 'sw-field' }, h('span', { class: 'muted small' }, 'From'),
        h('input', { type: 'date', 'data-key': 'sw-from', 'aria-label': 'From date', value: f.from, onchange: (e) => change(f, { from: e.target.value }) })),
      h('label', { class: 'sw-field' }, h('span', { class: 'muted small' }, 'To (included)'),
        h('input', { type: 'date', 'data-key': 'sw-to', 'aria-label': 'To date', value: f.to, onchange: (e) => change(f, { to: e.target.value }) }))] : null);
  return h('div', { class: 'filters sw-filters', 'data-key': 'sw-filters' },
    sel('Machine', 'sw-machine', f.machine, [['', 'All machines'], ...machines.map((m) => [m.id, m.name])], (v) => ({ machine: v })),
    sel('Account', 'sw-account', f.account, [['', 'All accounts'], ...accountOptions(store, data, f.account).map((a) => [a, a])], (v) => ({ account: v })),
    sel('Moved by', 'sw-mode', f.mode, [['', 'Automatic and by hand'], ['auto', 'Automatic'], ['manual', 'By hand']], (v) => ({ mode: v })),
    sel('Result', 'sw-result', f.result, [['', 'All moves'], ['failed', 'Failed only']], (v) => ({ result: v })),
    range,
    f.session ? h('button', { type: 'button', class: 'chip-btn', 'data-key': 'sw-session-clear', title: 'Show every session again',
      onclick: () => change(f, { session: '' }) }, `Session ${f.session.slice(0, 8)} ×`) : null);
}

function strip(data, f) {
  const s = data.summary;
  const accounts = s.by_account.filter((a) => a.in + a.out > 0);
  return h('section', { class: 'sw-strip card', 'aria-label': `Summary, ${rangeText(f)}`, 'data-key': 'sw-strip' },
    h('div', { class: 'sw-tiles' }, stripTiles(s).map(([label, value, hint]) => h('div', { class: 'sw-tile', title: hint || null },
      h('div', { class: 'sw-n' }, value), h('div', { class: 'muted small' }, label)))),
    accounts.length ? h('p', { class: 'muted small sw-accounts', 'data-key': 'sw-accounts' }, 'By account: ', accounts.map(accountLine).join(' · ')) : null);
}

function row(m, now) {
  const projected = projectedParts(m);
  return h('li', { class: `sw-row${m.result === 'failed' ? ' sw-failed' : ''}`, 'data-key': `sw-${m.id}` },
    h('div', { class: 'sw-head' },
      h('time', { class: 'mono small sw-time', datetime: m.at, title: dateTime(m.at) }, clock(m.at, now)),
      h('span', { class: 'strong sw-move' }, `${m.from} → ${m.to}`),
      h('span', { class: `sw-tag ${m.auto ? 'sw-auto' : 'sw-hand'}` }, modeText(m)),
      m.cross_org ? h('span', { class: 'sw-tag sw-org', title: 'The destination account is in another organisation' }, 'other organisation') : null,
      m.result === 'failed' ? h('span', { class: 'flag' }, 'Failed') : null),
    h('div', { class: 'sw-who small' },
      m.session_known
        ? h('a', { href: `#/sessions/${encodeURIComponent(m.session_id)}`, 'data-key': `sw-session-${m.id}` }, sessionLabel(m))
        : h('span', { title: 'The hub has no record of this session' }, sessionLabel(m)),
      h('span', { class: 'muted' }, ` · ${m.machine_name || 'a machine'} · ${kindText(m.kind)}`),
      moveNoText(m) ? h('span', { class: 'muted' }, ` · ${moveNoText(m)}`) : null,
      h('span', { class: 'muted' }, ' · '),
      h('a', { class: 'sw-only muted', href: `#/switches?session=${encodeURIComponent(m.session_id)}&range=all`, title: 'Show only this session’s moves' }, 'its moves')),
    m.reason ? h('div', { class: 'small sw-reason' }, m.reason) : null,
    projected.length ? h('div', { class: 'muted small sw-proj' }, `${m.window ? `${m.window === '7d' ? 'Week' : '5 h'} window: ` : ''}${projected.join(', ')}`) : null,
    h('div', { class: `small sw-result${m.result === 'failed' ? ' sw-error' : ' muted'}` },
      resultText(m), m.interactive && m.result !== 'failed' ? ' (a message told whoever was at the terminal)' : ''));
}

function pager(data) {
  const older = data.next, newer = state.stack.length > 0;
  if (!older && !newer) return null;
  return h('nav', { class: 'sw-pager', 'aria-label': 'Pages' },
    h('button', { type: 'button', class: 'btn btn-sm', 'data-key': 'sw-newer', disabled: newer ? null : true,
      onclick: () => { state.cursor = state.stack.pop() || ''; again(); } }, '← Newer'),
    h('span', { class: 'muted small' }, `Page ${state.stack.length + 1}`),
    h('button', { type: 'button', class: 'btn btn-sm', 'data-key': 'sw-older', disabled: older ? null : true,
      onclick: () => { state.stack.push(state.cursor); state.cursor = older; again(); } }, 'Older →'));
}

export function render({ store, route, now }) {
  const f = parseFilters(route.query);
  const fq = filtersQuery(f);
  // A new filter starts again from the newest page.
  if (state.route !== fq) { state.route = fq; state.stack = []; state.cursor = ''; state.key = ''; state.data = null; }
  const key = `${fq}|${state.cursor}`;
  if (state.key !== key) { state.key = key; fetchPage(f, key); }
  const owner = isOwner();
  const d = state.data;
  let body;
  if (state.error) body = h('p', { class: 'calm', role: 'alert', 'data-key': 'sw-error' }, state.error);
  else if (!d) body = h('p', { class: 'muted pad' }, 'Loading the switch log…');
  else {
    const filtered = f.machine || f.account || f.session || f.mode || f.result;
    body = [
      strip(d, f),
      d.moves.length === 0
        ? h('p', { class: 'calm', 'data-key': 'sw-empty' }, filtered || f.range !== 'all'
          ? 'No moves match these filters. Widen the dates, or clear a filter.'
          : 'No session has moved yet. Moves appear here once Smart Account Switch is on (wy config set account.smart_switch on) or you run wy move.')
        : h('ul', { class: 'panel sw-list', 'data-key': 'sw-list' }, d.moves.map((m) => row(m, now))),
      pager(d)];
  }
  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head col' },
      h('h1', {}, 'Switches'), moreTabs('switches', { owner }),
      h('p', { class: 'muted small' }, 'Every time a session moved to another account, on every machine: what triggered it, and how it went. ',
        d ? `${plural(d.summary.moves + d.summary.failed, 'move')} ${rangeText(f)}.` : ''),
      filterBar(store, f, d)),
    body);
  return { title: 'Switches · Watchyard', node };
}
