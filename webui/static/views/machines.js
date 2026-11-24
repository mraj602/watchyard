// Machines (docs/spec/machines.md 6.1): reach, last seen, backlog and its age, version against
// the hub, pull health and the open silent alert, with Rename / Remove / Pull now for the owner.

import { h } from '../components/dom.js';
import { seatIndex, seatName } from '../components/seat.js';
import { ago, agoLabel, clock, plural, num } from '../format.js';
import { moreTabs, toast, details } from '../components/ui.js';
import { isOwner, refreshOverview, ensureSessions } from '../store.js';
import { liveCounts } from '../components/state.js';
import { getJSON, patchJSON, postJSON, del } from '../api.js';
import { reachChip, statusOf, backlogText, versionLine, pullLine, sortMachines, alertText } from '../components/machines-fmt.js';

const fmt = { ago, agoLabel, clock };
const enc = encodeURIComponent;
const again = () => window.dispatchEvent(new Event('wy-rerender'));

// Removed machines are fetched only when asked for (GET /v1/machines?all=true).
const ui = { showRemoved: false, removed: [], busy: {} };

export const load = () => { focused = ''; if (ui.showRemoved) loadRemoved(); return ensureSessions(); };

async function loadRemoved() {
  try {
    const r = await getJSON('/v1/machines', { all: 'true' });
    ui.removed = (r.machines || []).filter((m) => m.removed_at);
  } catch (e) {
    if (e.name !== 'AuthError') toast(e.message);
  }
  again();
}

function toggleRemoved() {
  ui.showRemoved = !ui.showRemoved;
  if (ui.showRemoved) loadRemoved(); else again();
}

async function act(m, key, fn, done) {
  if (ui.busy[m.id]) return;
  ui.busy[m.id] = key; again();
  try {
    await fn();
    if (done) toast(done);
    await refreshOverview();
    if (ui.showRemoved) await loadRemoved();
  } catch (ex) {
    toast(ex.status === 409 ? `Not possible now: ${ex.message}` : ex.message);
  } finally {
    delete ui.busy[m.id];
    again();
  }
}

const rename = (m) => {
  const name = prompt('Name for this machine', m.name);
  if (!name || !name.trim() || name.trim() === m.name) return;
  act(m, 'rename', () => patchJSON(`/v1/machines/${enc(m.id)}`, { name: name.trim() }));
};

const remove = (m) => {
  if (!confirm(`Remove ${m.name}? Its history stays, its devices stop working and its alerts close. It can come back by connecting again.`)) return;
  act(m, 'remove', () => del(`/v1/machines/${enc(m.id)}`), `${m.name} removed`);
};

const pullNow = (m) => act(m, 'pull', () => postJSON(`/v1/machines/${enc(m.id)}/pull`), `Pulling ${m.name} now`);

function actions(m, pl) {
  const busy = ui.busy[m.id];
  return h('div', { class: 'machine-actions' },
    pl ? h('button', {
      type: 'button', class: 'btn btn-sm', 'data-key': `pull-${m.id}`, disabled: !pl.canRun || !!busy,
      title: pl.canRun ? 'Pull this machine now' : 'Fix the pull target first (wy hub pull)', onclick: () => pullNow(m),
    }, busy === 'pull' ? 'Pulling…' : 'Pull now') : null,
    h('button', { type: 'button', class: 'linkish', 'data-key': `rename-${m.id}`, disabled: !!busy, onclick: () => rename(m) }, 'Rename'),
    h('button', { type: 'button', class: 'linkish danger', 'data-key': `remove-${m.id}`, disabled: !!busy, onclick: () => remove(m) }, 'Remove'));
}

function card(m, ctx, idx) {
  const { route, now, owner, hubVersion, counts } = ctx;
  const live = counts ? counts.machine.get(m.id) || 0 : m.live_sessions ?? 0;
  const st = statusOf(m, now, fmt);
  const chip = reachChip(m);
  const ver = versionLine(m, hubVersion);
  const backlog = backlogText(m, now, fmt);
  const pl = pullLine(m.pull, now, fmt);
  const alert = alertText(m.alert, now, fmt);
  const slots = m.slots || [];
  const seatText = (id) => (id && idx.get(id) ? seatName(idx.get(id).seat) : id || 'not signed in');
  const removed = !!m.removed_at;
  const row = (k, v, cls) => [h('dt', {}, k), h('dd', { class: cls || '' }, v)];
  return h('article', { class: `card machine${route.query.get('machine') === m.id ? ' highlight' : ''}${removed ? ' is-removed' : ''}`, id: `machine-${m.id}`, 'data-machine': m.id },
    h('div', { class: 'seat-head' },
      h('h2', { class: 'seat-name machine-name', title: m.name }, m.name),
      h('span', { class: `small status-${removed ? 'muted' : st.cls}`, role: 'status', 'data-key': `status-${m.id}` }, removed ? `Removed ${agoLabel(m.removed_at, now)}` : st.text)),
    h('div', { class: 'machine-chips' },
      chip ? h('span', { class: `reach-chip ${chip.cls}`, title: chip.title, 'data-key': `reach-${m.id}` }, chip.text) : null,
      m.held > 0 ? h('span', { class: 'chip chip-bad', 'data-key': `held-${m.id}`, title: 'This machine runs a newer version than the hub; update the hub so it can take them' }, `${plural(m.held, 'update')} waiting for a newer hub`) : null,
      m.direct_push ? h('span', { class: 'chip chip-ok', title: 'When the hub cannot be reached, this machine sends notifications to ntfy itself' }, 'Notifies on its own') : null),
    alert && !removed ? h('p', { class: 'machine-alert status-warn small', 'data-key': `alert-${m.id}` }, alert) : null,
    h('dl', { class: 'kv' },
      row('Open sessions', live),
      row('Last update', agoLabel(m.last_record, now) || '–'),
      backlog ? row('Waiting to send', backlog, 'strong') : null,
      ver.cls ? row('Watchyard version', ver.version) : null),
    // The warning on its own line, plain text (R10).
    ver.cls && ver.note ? h('p', { class: `ver-note small status-${ver.cls === 'bad' ? 'warn strong' : 'warn'}`, 'data-key': `ver-${m.id}` }, `${ver.note.charAt(0).toUpperCase()}${ver.note.slice(1)}.`) : null,
    pl ? h('p', { class: `pull-line small status-${pl.cls}`, 'data-key': `pull-state-${m.id}` }, `Collecting · ${pl.text.replace(/^[A-Z](?=[a-z])/, (c) => c.toLowerCase())}`) : null,
    slots.length ? h('div', {},
      h('h3', { title: 'Claude Code config folders (~/.claude, ~/.claude-2, …) on this machine' }, 'Claude profiles'),
      h('ul', { class: 'slots' }, slots.map((sl) => h('li', {},
        h('span', { class: 'mono' }, sl.folder),
        h('span', { class: 'muted small' }, ` · ${sl.logged_in ? seatText(sl.seat) : 'not signed in'}`),
        sl.launcher || sl.claude_version ? h('span', { class: 'muted small', title: 'Command that starts it, and the Claude Code version' }, [sl.launcher ? ` · runs as ${sl.launcher}` : '', sl.claude_version ? ` · Claude Code ${sl.claude_version}` : ''].join('')) : null)))) : null,
    details({ text: 'Details', key: `machine-details-${m.id}` },
      h('dl', { class: 'kv' },
        row('System', [m.os, m.arch].filter(Boolean).join(' · ') || '–'),
        ver.cls ? null : row('Watchyard version', ver.text),
        row('Last seen', agoLabel(m.last_seen, now) || '–'),
        row('Updates waiting', m.backlog == null ? 'not reported' : num(m.backlog)),
        row('Notifies on its own', m.direct_push ? 'on' : 'off'))),
    h('p', { class: 'small' }, h('a', { href: `#/switches?machine=${encodeURIComponent(m.id)}`, 'data-key': `machine-switches-${m.id}` }, 'Account switches on this machine')),
    owner && !removed ? actions(m, pl) : null);
}

export function render({ store, route, now }) {
  focusSoon(route, 'machine');
  const o = store.overview;
  const owner = isOwner();
  const idx = seatIndex(o.seats);
  const ctx = { route, now, owner, hubVersion: store.me && store.me.hub ? store.me.hub.version : '', counts: liveCounts(store.sessions, now) };
  const live = sortMachines(o.machines.filter((m) => !m.removed_at));
  const removed = ui.showRemoved ? sortMachines(ui.removed) : [];

  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head' },
      h('div', { class: 'head-title' }, h('h1', {}, 'Machines'), moreTabs('machines', { owner }),
        h('p', { class: 'muted small' }, `${plural(live.length, 'machine')} reporting to this hub`)),
      h('div', { class: 'actions' },
        owner ? h('a', { class: 'btn', href: '#/more/devices?add=machine', 'data-key': 'add-machine' }, 'Add machine') : null,
        h('button', { type: 'button', class: 'chip-btn', 'aria-pressed': String(ui.showRemoved), 'data-key': 'show-removed', onclick: toggleRemoved }, 'Show removed'))),
    live.length === 0 ? h('p', { class: 'calm' }, owner
      ? 'No machines have connected yet. Run wy machine add user@host on your laptop, or use Add machine.'
      : 'No machines have connected yet. Run wy connect on one to add it.') :
      h('div', { class: 'grid grid-machines' }, live.map((m) => card(m, ctx, idx))),
    ui.showRemoved ? h('section', { class: 'removed-machines', 'aria-label': 'Removed machines' },
      h('h2', { class: 'section-title' }, 'Removed'),
      removed.length === 0 ? h('p', { class: 'muted small' }, 'No machines were removed.')
        : h('div', { class: 'grid grid-machines' }, removed.map((m) => card(m, ctx, idx)))) : null);
  return { title: 'Machines · Watchyard', node };
}

// A deep link (?machine=<id>) scrolls to its card once per visit.
let focused = '';
function focusSoon(route, param) {
  const id = route.query.get(param);
  if (!id || focused === id) return;
  focused = id;
  requestAnimationFrame(() => { const el = document.getElementById(`${param}-${id}`); if (el) el.scrollIntoView({ block: 'center' }); });
}
