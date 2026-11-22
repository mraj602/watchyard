// Accounts: one calm row per account (the tighter of the 5 h and 7 d limits, and only trouble in
// words); a row opens in place to show every window with its reset time, where Claude Code is
// signed in, details and (owner) the label and colour form.

import { h } from '../components/dom.js';
import { seatIndex, seatName, namesCollide } from '../components/seat.js';
import { windowRow, sortWindows } from '../components/windows.js';
import { kvList, usageMoney, noUsage, dot } from '../components/cards.js';
import { mostRoom, tighter, attention, signedOutOn } from '../components/accounts-fmt.js';
import { liveCounts } from '../components/state.js';
import { agoLabel, pct, clock } from '../format.js';
import { isOwner, ensureSessions, reorderSeats } from '../store.js';
import { moveBy, dropTarget, dropOrder, moveSentence } from '../components/order.js';
import { patchJSON, getRelation } from '../api.js';
import { verdictLine, verdictAge, calibRows, ratioBars, capacityLine, capacityBars, heavyNote, shortDay } from '../components/relation-fmt.js';
import { toast } from '../components/ui.js';

// Owner edit of a seat's label and colour (desktop.md C10): PATCH /v1/accounts/{seat}. The
// hub keeps a UI label over any label `wy` sends from a folder.
const LABEL_RE = /^[a-z0-9][a-z0-9-]{0,23}$/;
const again = () => window.dispatchEvent(new Event('wy-rerender'));
const edit = { id: '', label: '', colour: '', clearColour: false, error: '', busy: false };

function startEdit(seat, colour) {
  setOpen(seat.id, true);
  Object.assign(edit, { id: seat.id, label: seat.label || '', colour: /^#[0-9a-f]{6}$/.test(colour) ? colour : '#0e7c76',
    clearColour: false, error: '', busy: false });
  again();
}

async function saveEdit(seat) {
  const label = edit.label.trim();
  if (label && !LABEL_RE.test(label)) { edit.error = 'Use 1 to 24 characters: a to z, 0 to 9 and -, starting with a letter or digit.'; again(); return; }
  const body = { label: label || null, colour: edit.clearColour ? null : edit.colour };
  // ("Default colour" only marks the form; Save sends it.)
  edit.busy = true; edit.error = ''; again();
  try {
    Object.assign(seat, await patchJSON(`/v1/accounts/${encodeURIComponent(seat.id)}`, body));
    edit.id = '';
  } catch (ex) {
    edit.error = ex.status === 403 ? 'Sign in as owner to edit accounts.' : ex.message;
    if (ex.name === 'AuthError') toast(ex.message);
  }
  edit.busy = false;
  again();
}

function editForm(seat) {
  return h('form', { class: 'seat-edit', 'data-key': 'seat-edit', onsubmit: (e) => { e.preventDefault(); saveEdit(seat); } },
    h('label', { class: 'field' }, 'Label',
      h('input', { type: 'text', value: edit.label, maxlength: 24, autocomplete: 'off', placeholder: 'work', 'data-key': 'seat-label',
        oninput: (e) => { edit.label = e.target.value; } })),
    h('label', { class: 'field' }, 'Colour',
      h('input', { type: 'color', value: edit.colour, 'data-key': 'seat-colour', oninput: (e) => { edit.colour = e.target.value; edit.clearColour = false; again(); } }),
      edit.clearColour ? h('span', { class: 'muted small' }, 'default') : null),
    edit.error ? h('p', { class: 'form-error', role: 'alert' }, edit.error) : null,
    h('div', { class: 'actions' },
      h('button', { type: 'submit', class: 'btn btn-primary', disabled: edit.busy ? true : undefined, 'data-key': 'seat-save' }, 'Save'),
      h('button', { type: 'button', class: 'btn', 'data-key': 'seat-reset-colour', 'aria-pressed': String(edit.clearColour), onclick: () => { edit.clearColour = true; again(); } }, 'Use default colour'),
      h('button', { type: 'button', class: 'linkish', 'data-key': 'seat-cancel', onclick: () => { edit.id = ''; again(); } }, 'Cancel')));
}

// ---- the order of the accounts ---------------------------------------------------------------
// The owner drags a card by its grip (pointer events: mouse, pen and touch), or focuses the
// grip and presses the arrow keys (Alt+arrow anywhere on a card works too). The hub keeps the
// order for every device (PUT /v1/seats/order); the board shows it at once (store.reorderSeats).
// The pure parts are in components/order.js.
const drag = { id: '', drop: null };
const move = { error: '', said: '' };

let store0 = () => ({ overview: null }); // the store of the last render
const shownIds = () => (store0().overview ? store0().overview.seats.filter((s) => !s.hidden).map((s) => s.id) : []);
const cardRects = () => [...document.querySelectorAll('.acct-list > .seat-card')].map((el) => {
  const r = el.getBoundingClientRect();
  return { id: el.id.replace(/^seat-/, ''), left: r.left, top: r.top, width: r.width, height: r.height };
});

// Marks the dragged card and the drop place straight in the DOM (a redraw reads `drag` too).
function paintDrag() {
  const grid = document.querySelector('.acct-list');
  if (!grid) return;
  for (const el of grid.querySelectorAll('.seat-card')) {
    const id = el.id.replace(/^seat-/, '');
    el.classList.toggle('seat-dragging', id === drag.id);
    el.classList.toggle('drop-before', !!drag.drop && drag.drop.target === id && !drag.drop.after && id !== drag.id);
    el.classList.toggle('drop-after', !!drag.drop && drag.drop.target === id && drag.drop.after && id !== drag.id);
  }
  const tops = new Set(cardRects().map((r) => Math.round(r.top)));
  grid.dataset.cols = tops.size < grid.querySelectorAll('.seat-card').length ? 'n' : '1';
}

async function commit(ids, id, name) {
  move.error = '';
  const err = await reorderSeats(ids);
  if (err) { move.error = err; toast(err); move.said = ''; } else move.said = moveSentence(name, ids, id);
  again();
}

function endDrag(drop) {
  window.removeEventListener('pointermove', onMove);
  window.removeEventListener('pointerup', onUp);
  window.removeEventListener('pointercancel', onCancel);
  window.removeEventListener('keydown', onEsc, true);
  document.body.classList.remove('dragging-seat');
  const id = drag.id;
  drag.id = ''; drag.drop = null;
  paintDrag();
  if (!drop || !id) return;
  const next = dropOrder(shownIds(), id, drop);
  if (next) commit(next, id, nameOf(id));
}

const nameOf = (id) => {
  const s = store0().overview && store0().overview.seats.find((x) => x.id === id);
  return s ? seatName(s) : '';
};

function onMove(e) {
  if (!drag.id) return;
  drag.drop = dropTarget(cardRects(), e.clientX, e.clientY);
  paintDrag();
  // Near the top or bottom of the window the page scrolls under the pointer.
  if (e.clientY < 60) window.scrollBy(0, -14); else if (e.clientY > window.innerHeight - 60) window.scrollBy(0, 14);
}
function onUp(e) { drag.drop = dropTarget(cardRects(), e.clientX, e.clientY); endDrag(drag.drop); }
function onCancel() { endDrag(null); }
function onEsc(e) { if (e.key === 'Escape') { e.preventDefault(); endDrag(null); } }

function startDrag(e, seat) {
  if (e.button !== undefined && e.button !== 0) return;
  e.preventDefault();
  drag.id = seat.id; drag.drop = null;
  document.body.classList.add('dragging-seat');
  window.addEventListener('pointermove', onMove);
  window.addEventListener('pointerup', onUp);
  window.addEventListener('pointercancel', onCancel);
  window.addEventListener('keydown', onEsc, true);
  paintDrag();
}

const KEY_DELTA = { ArrowUp: -1, ArrowLeft: -1, ArrowDown: 1, ArrowRight: 1 };
function keyMove(e, seat, needAlt) {
  const delta = KEY_DELTA[e.key];
  if (!delta || (needAlt && !e.altKey)) return;
  e.preventDefault();
  e.stopPropagation(); // the grip and the card both listen: one move per key
  const ids = shownIds();
  const next = moveBy(ids, seat.id, delta);
  if (next !== ids) commit(next, seat.id, seatName(seat));
}

const grip = (seat, name) => h('button', { type: 'button', class: 'seat-grip', 'data-key': `seat-grip-${seat.id}`,
  'aria-label': `Move ${name}: drag, or press the arrow keys`, title: 'Drag to reorder (or use the arrow keys)',
  onpointerdown: (e) => startDrag(e, seat), onkeydown: (e) => keyMove(e, seat, false) },
  h('span', { 'aria-hidden': 'true' }, '⠿'));


// ---- which rows are open ---------------------------------------------------------------------
// Several rows can be open; the open ones are remembered in this browser (localStorage, which may
// be missing or blocked: then rows start closed and stay as clicked).
const OPEN_KEY = 'wy-accounts-open';
const open = new Set((() => {
  try { const v = JSON.parse(localStorage.getItem(OPEN_KEY) || '[]'); return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : []; } catch (_) { return []; }
})());
const saveOpen = () => { try { localStorage.setItem(OPEN_KEY, JSON.stringify([...open])); } catch (_) { /* private mode */ } };
function setOpen(id, on) {
  if (on) open.add(id); else open.delete(id);
  saveOpen();
}
const toggle = (id) => { setOpen(id, !open.has(id)); again(); };
function setAll(seats, on) {
  for (const s of seats) { if (on) open.add(s.id); else open.delete(s.id); }
  saveOpen();
  again();
}

let focused = '';
export const load = () => { focused = ''; return ensureSessions(); };

export function render({ store, route, now }) {
  store0 = () => store;
  const o = store.overview;
  const idx = seatIndex(o.seats);
  const seats = o.seats.filter((s) => !s.hidden);
  const counts = liveCounts(store.sessions, now);
  const want = route.query.get('seat');
  if (want && focused !== want && seats.some((s) => s.id === want)) setOpen(want, true);
  const allOpen = seats.length > 0 && seats.every((s) => open.has(s.id));

  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head' }, h('h1', {}, 'Accounts'),
      seats.length ? h('button', { type: 'button', class: 'linkish', 'data-key': 'accounts-expand-all', onclick: () => setAll(seats, !allOpen) },
        allOpen ? 'Collapse all' : 'Expand all') : null),
    namesCollide(seats) && isOwner() ? h('p', { class: 'calm small', 'data-key': 'label-hint' }, 'Some accounts have nearly the same name. Use Edit to give each one a short label.') : null,
    roomLine(seats, now),
    move.error ? h('p', { class: 'form-error', role: 'alert', 'data-key': 'order-error' }, move.error) : null,
    h('p', { class: 'sr-only', role: 'status', 'aria-live': 'polite', 'data-key': 'order-said' }, move.said),
    seats.length === 0 ? h('p', { class: 'calm' }, 'No accounts yet.') : [
      h('div', { class: 'acct-list', 'data-key': 'accounts-grid' }, seats.map((s) => accountRow(s, idx.get(s.id).colour, now, want === s.id, counts ? counts.seat.get(s.id) || 0 : s.live_sessions, isOwner() && seats.length > 1))),
      h('p', { class: 'muted small', 'data-key': 'accounts-note' }, 'The bar shows the tighter of the 5 h and 7 d limits. Select an account for everything else.')]);
  if (want && focused !== want) {
    focused = want;
    requestAnimationFrame(() => { const el = document.getElementById(`seat-${want}`); if (el) el.scrollIntoView({ block: 'center' }); });
  }
  return { title: 'Accounts · Watchyard', node };
}

function roomLine(seats, now) {
  const best = mostRoom(seats, now);
  if (!best) return null;
  return h('p', { class: 'room', 'data-key': 'most-room' }, 'Most room right now: ', h('b', {}, seatName(best.seat)),
    ` (5 h ${pct(best.five)} · 7 d ${pct(best.seven)})`);
}

// "Near the 5-hour limit: full by 15:48", "7-day limit nearly used, resets Sun 09:00", "Signed out on x".
function issueText(i, now) {
  if (i.kind === 'near') {
    return i.key === 'five_hour'
      ? `Near the 5-hour limit${i.at ? `: full by ${clock(i.at, now)}` : ''}`
      : `7-day limit nearly used${i.at ? `, resets ${clock(i.at, now)}` : ''}`;
  }
  if (i.kind === 'signed_out') return `Signed out on ${i.machines.join(', ')}`;
  return i.text;
}

function accountRow(seat, colour, now, highlight, live, canMove) {
  const name = seatName(seat);
  const t = tighter(seat, now);
  const issues = attention(seat, now);
  const isOpen = open.has(seat.id);
  const detailId = `seat-detail-${seat.id}`;
  const hot = !!t && t.used >= 90;
  const dropClass = drag.id && drag.drop && drag.drop.target === seat.id && drag.id !== seat.id ? (drag.drop.after ? ' drop-after' : ' drop-before') : '';
  const bar = t ? h('div', { class: 'acct-bar' },
    h('span', { class: 'acct-win muted small' }, t.label),
    h('div', { class: `meter acct-meter${hot ? ' hot' : ''}`, role: 'meter', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': Math.round(t.used), 'aria-label': `${name} ${t.label} used` },
      h('div', { class: 'meter-fill', style: { width: `${Math.min(100, Math.max(0, t.used))}%` } })),
    h('span', { class: `mono acct-pct${hot ? ' hot' : ''}` }, pct(t.used)),
    t.phase === 'idle' ? h('span', { class: 'muted small acct-reset', 'data-key': `reset-${seat.id}` }, 'not started')
      : t.resets_at ? h('span', { class: 'muted small acct-reset', 'data-key': `reset-${seat.id}` }, `${t.phase === 'reset' ? 'reset at' : 'resets'} ${clock(t.resets_at, now)}`) : null)
    : h('div', { class: 'acct-bar muted small' }, 'No usage yet');
  return h('article', { class: `seat-card acct${isOpen ? ' is-open' : ''}${highlight ? ' highlight' : ''}${drag.id === seat.id ? ' seat-dragging' : ''}${dropClass}`, id: `seat-${seat.id}`, style: { '--seat': colour }, 'aria-label': name,
    onkeydown: canMove ? (e) => { if (e.altKey) keyMove(e, seat, true); } : undefined },
    h('div', { class: 'acct-row', onclick: (e) => { if (!e.target.closest('a, button, input, label, form')) toggle(seat.id); } },
      canMove ? grip(seat, name) : h('span', { class: 'acct-nogrip' }),
      h('div', { class: 'acct-who' }, dot(colour), h('h2', { class: 'seat-name' }, name), h('span', { class: 'muted small acct-plan' }, seat.plan || '')),
      bar,
      h('div', { class: 'acct-issue small', 'data-key': `issue-${seat.id}` },
        issues.length ? [h('span', { class: `acct-issue-text issue-${issues[0].kind}` }, issueText(issues[0], now)),
          issues.length > 1 ? h('span', { class: 'muted', title: issues.slice(1).map((i) => issueText(i, now)).join('; ') }, ` +${issues.length - 1}`) : null] : null),
      h('button', { type: 'button', class: 'acct-chev', 'data-key': `seat-toggle-${seat.id}`, 'aria-expanded': String(isOpen), 'aria-controls': detailId,
        'aria-label': `${isOpen ? 'Hide' : 'Show'} everything for ${name}`, onclick: () => toggle(seat.id) },
        h('span', { 'aria-hidden': 'true' }, isOpen ? '▾' : '▸'))),
    isOpen ? accountDetail(seat, colour, now, live, name, detailId) : null);
}

function accountDetail(seat, colour, now, live, name, detailId) {
  const ws = sortWindows(seat.windows || []);
  const money = usageMoney(seat);
  // Fields the board has no sentence for still show, as a list.
  const extra = money.length ? null : kvList(seat.extra_usage);
  const spend = money.length ? null : kvList(seat.spend);
  const signedIn = [...new Set((seat.places || []).filter((p) => p.slot).map((p) => p.machine_name || p.machine))];
  const signedOut = signedOutOn(seat);
  const rows = [['Plan', seat.plan || '–'], ['Email', seat.email || '–']];
  if (seat.org_name) rows.splice(1, 0, ['Organisation', seat.org_name]);
  if (money.length) rows.push(['Extra usage', money.join(' ')]);
  rows.push(['Open sessions', String(live || 0)]);
  return h('div', { class: 'acct-detail', id: detailId, 'data-key': `detail-${seat.id}` },
    isOwner() && edit.id !== seat.id ? h('div', {}, h('button', { type: 'button', class: 'linkish', 'data-key': `seat-edit-${seat.id}`,
      'aria-label': `Edit label and colour for ${name}`, onclick: () => startEdit(seat, colour) }, 'Edit label and colour')) : null,
    edit.id === seat.id ? editForm(seat) : null,
    h('section', { class: 'acct-sec' }, h('h3', {}, 'Limits'),
      ws.length ? ws.map((w) => windowRow(w, { seatName: name, full: true, now })) : noUsage()),
    h('section', { class: 'acct-sec', 'data-key': `where-${seat.id}` }, h('h3', {}, 'Signed in'),
      signedIn.length || signedOut.length ? h('ul', { class: 'acct-where' },
        signedIn.map((m) => h('li', {}, h('b', {}, m), ' · Claude Code signed in')),
        signedOut.map((m) => h('li', { class: 'issue-signed_out' }, h('b', {}, m), ' · Claude Code signed out')))
        : h('p', { class: 'muted small' }, 'Claude Code is not signed in on any machine.')),
    h('section', { class: 'acct-sec' }, h('h3', {}, 'Details'),
      h('dl', { class: 'kv' }, rows.flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
      extra ? h('details', { class: 'extra', open: true }, h('summary', {}, 'Extra usage'), extra) : null,
      spend ? h('details', { class: 'extra' }, h('summary', {}, 'Spend'), spend) : null,
      seat.web_raw_at ? h('p', { class: 'muted small' }, `claude.ai usage read ${agoLabel(seat.web_raw_at, now)}`) : null,
      relationBox(seat, now)));
}

// ---- usage and the picker (pwa.md 3.0a) -----------------------------------------------------
// A closed disclosure inside Details: it reads GET /v1/accounts/{seat}/relation when first
// opened, and again when opened more than 5 minutes later. A redraw re-opens it (app.js), which
// fires `toggle` again: the age check keeps that from reading the hub every time.
const REL_FRESH = 5 * 60 * 1000;
const rel = new Map(); // seat id -> { at, data, error, busy, bar: { ratio, [model]: index } }

function relState(id) {
  if (!rel.has(id)) rel.set(id, { at: 0, data: null, error: '', busy: false, bar: {} });
  return rel.get(id);
}

async function loadRelation(id) {
  const st = relState(id);
  if (st.busy) return;
  st.busy = true; st.error = ''; again();
  try {
    st.data = await getRelation(id);
    st.at = Date.now();
  } catch (ex) {
    st.error = ex.status === 404 ? 'This account is not on the hub any more.' : (ex.message || 'Could not load.');
    if (ex.name === 'AuthError') toast(ex.message);
  }
  st.busy = false;
  again();
}

function onRelToggle(e, id) {
  if (!e.target.open) return;
  const st = relState(id);
  if (!st.busy && (!st.data || Date.now() - st.at > REL_FRESH) && !st.error) loadRelation(id);
}

// bars draws tap-to-show bars (as the Analytics bars, AN7): items of { height, label, initial? }.
function relBars(st, key, items, opts = {}) {
  const picked = st.bar[key];
  const chosen = picked != null && items[picked] ? items[picked] : null;
  return h('div', { class: `rel-chart${opts.muted ? ' is-muted' : ''}` },
    h('div', { class: 'ana-bars rel-bars', role: 'group', 'aria-label': opts.label, 'data-key': `rel-bars-${key}` },
      opts.line != null ? h('span', { class: 'rel-line', style: { bottom: `${opts.line}%` }, 'aria-hidden': 'true' }) : null,
      items.map((x, i) => h('button', {
        type: 'button', class: `ana-bar${picked === i ? ' is-picked' : ''}${x.height ? '' : ' is-empty'}`,
        style: { height: `${x.height}%` }, title: x.label, 'aria-label': x.label, 'aria-pressed': String(picked === i),
        onclick: () => { st.bar[key] = picked === i ? null : i; again(); } }))),
    opts.initials ? h('div', { class: 'rel-initials muted small', 'aria-hidden': 'true' }, items.map((x) => h('span', {}, x.initial || ''))) : null,
    h('div', { class: 'ana-axis muted small' }, h('span', {}, shortDay(items.length ? items[0].day : '')),
      h('span', {}, shortDay(items.length ? items[items.length - 1].day : ''))),
    h('p', { class: 'small ana-picked', role: 'status' }, chosen ? chosen.label : h('span', { class: 'muted' }, 'Tap a bar to see its day.')));
}

function relationBody(seat, st, now) {
  const d = st.data;
  const nowMs = now instanceof Date ? now.getTime() : Date.now();
  const v = d.verdict;
  const cal = calibRows(d.calibration, nowMs);
  const ratio = ratioBars(d.days);
  const note = heavyNote(d.heavy);
  return [
    h('h4', {}, 'Picker'),
    h('p', { 'data-key': `rel-verdict-${seat.id}` }, verdictLine(v, nowMs)),
    v ? h('p', { class: 'muted small' }, verdictAge(v, nowMs)) : null,
    h('h4', {}, 'Calibration'),
    cal ? h('dl', { class: 'kv' }, cal.rows.flatMap((r) => [h('dt', {}, r.name),
      h('dd', {}, r.value, h('span', { class: 'muted small rel-state' }, r.state))])) : h('p', { class: 'muted small' }, 'Not calibrated yet.'),
    cal && cal.restart ? h('p', { class: 'muted small' }, cal.restart) : null,
    h('h4', {}, '5 h per 7 d point, per day'),
    relBars(st, 'ratio', ratio.bars, { initials: true, label: `5-hour points per 7-day point, per day, for ${seatName(seat)}` }),
    ratio.legend ? h('p', { class: 'muted small' }, ratio.legend) : null,
    h('h4', {}, '5 h capacity, normalised to last week'),
    note ? h('p', { class: 'muted small' }, note) : null,
    d.models.length ? d.models.map((m) => {
      const b = capacityBars(m);
      return h('div', { class: 'rel-model' }, h('p', { class: 'small' }, capacityLine(m)),
        relBars(st, `cap-${m.model}`, b.bars, { line: b.line, muted: !!note, label: `${m.model}: 5-hour capacity per day, normalised to last week` }));
    }) : h('p', { class: 'muted small' }, 'No capacity index yet: it needs Claude Code use with token rows.'),
  ];
}

function relationBox(seat, now) {
  const st = relState(seat.id);
  let body;
  if (st.error) {
    body = [h('p', { class: 'form-error', role: 'alert' }, st.error),
      h('button', { type: 'button', class: 'btn btn-sm', 'data-key': `rel-retry-${seat.id}`, onclick: () => loadRelation(seat.id) }, 'Try again')];
  } else if (!st.data) {
    body = h('p', { class: 'muted small' }, 'Loading…');
  } else {
    body = relationBody(seat, st, now);
  }
  return h('details', { class: 'extra rel', 'data-key': `rel-${seat.id}`, ontoggle: (e) => onRelToggle(e, seat.id) },
    h('summary', {}, 'Usage and the picker'), h('div', { class: 'rel-body' }, body));
}
