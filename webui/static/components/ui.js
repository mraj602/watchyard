// Small shared controls: switch, select, section card, toast, SVG helper.

import { h } from './dom.js';
import { ownerHint, OWNER_CLI } from './notif-note.js';

const SVG_NS = 'http://www.w3.org/2000/svg';

// An SVG element built with DOM calls (never markup strings).
export function svg(tag, attrs = {}, ...kids) {
  const el = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) if (v != null) el.setAttribute(k, String(v));
  for (const k of kids.flat()) if (k) el.append(k);
  return el;
}

// A labelled on/off switch (a checkbox with role=switch). onChange gets the new value; when
// it rejects, the switch goes back and the error is shown.
export function toggle({ label, sub, checked, disabled, onChange, key }) {
  const input = h('input', { type: 'checkbox', role: 'switch', class: 'sw', 'data-key': key, disabled: disabled ? true : undefined, 'aria-label': label });
  input.checked = !!checked;
  input.addEventListener('change', async () => {
    const want = input.checked;
    input.disabled = true;
    try { await onChange(want); } catch (e) { input.checked = !want; toast(e.message || 'Could not save'); } finally { input.disabled = !!disabled; }
  });
  return h('label', { class: `switch-row${disabled ? ' is-disabled' : ''}` },
    h('span', { class: 'switch-text' }, h('span', {}, label), sub ? h('span', { class: 'muted small' }, sub) : null), input);
}

export function select({ label, value, options, onChange, key, disabled }) {
  const sel = h('select', { 'data-key': key, 'aria-label': label, disabled: disabled ? true : undefined },
    options.map(([v, text]) => h('option', { value: String(v), selected: String(v) === String(value) }, text)));
  sel.addEventListener('change', async () => {
    const prev = value;
    sel.disabled = true;
    try { await onChange(sel.value); value = sel.value; } catch (e) { sel.value = String(prev); toast(e.message || 'Could not save'); } finally { sel.disabled = !!disabled; }
  });
  return sel;
}

// A page section: heading and a panel of rows.
export function section(title, ...kids) {
  return h('section', { class: 'block', 'aria-label': title }, h('h2', {}, title), ...kids);
}

export const panel = (...kids) => h('div', { class: 'panel' }, ...kids);
export const row = (...kids) => h('div', { class: 'row' }, ...kids);

// A short message at the bottom of the screen (errors from actions).
let toastTimer = 0;
// `action` ({label, run}) adds a button to the message (and keeps it up a little longer).
export function toast(msg, action) {
  const el = document.getElementById('toasts');
  if (!el) return;
  el.replaceChildren(h('div', { class: 'toast' }, msg,
    action ? h('button', { type: 'button', class: 'linkish toast-action', 'data-key': 'toast-action', onclick: () => { el.replaceChildren(); action.run(); } }, action.label) : null));
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.replaceChildren(), action ? 10000 : 5000);
}

// One row that scrolls sideways when it is too wide; an edge fade shows which side has more (R12).
export function pillNav(items, current) {
  const nav = h('nav', { class: 'pills phone-only', 'aria-label': 'More' }, items.map(([page, href, label]) =>
    h('a', { class: 'pill', href, 'aria-current': page === current ? 'page' : undefined }, label)));
  const fades = () => {
    nav.classList.toggle('fade-r', nav.scrollLeft + nav.clientWidth < nav.scrollWidth - 2);
    nav.classList.toggle('fade-l', nav.scrollLeft > 2);
  };
  nav.addEventListener('scroll', fades, { passive: true });
  requestAnimationFrame(() => {
    const cur = nav.querySelector('[aria-current]');
    if (cur && nav.scrollWidth > nav.clientWidth) nav.scrollLeft = Math.max(0, cur.offsetLeft - 44);
    fades();
  });
  return nav;
}

// The Watchyard mark and name, for the screens without the sidebar (sign-in, pairing).
export const brandRow = () => h('div', { class: 'brand-row' },
  svg('svg', { viewBox: '0 0 22 22', width: 26, height: 26, 'aria-hidden': 'true' },
    svg('path', { d: 'M3 18 L11 4 L19 18', fill: 'none', stroke: 'currentColor', 'stroke-width': 2 }),
    svg('path', { d: 'M7 18 L11 11 L15 18', fill: 'none', stroke: '#E8B04B', 'stroke-width': 2 })),
  h('span', { class: 'brand-name' }, 'Watchyard'));

// Technical details behind a disclosure: versions, ids, limits.
// `summary` may be a string or {text, key}; a key keeps it open across redraws (app.js).
export const details = (summary, ...kids) => h('details', { class: 'tech', 'data-key': summary.key },
  h('summary', {}, summary.text || summary), h('div', { class: 'tech-body' }, ...kids));

// How a non-owner becomes the owner (notif-note.js ownerHint): lead, then the owner-link hint;
// in a browser the CLI is a fold-out "On the hub's computer"; in the desktop app only the Hub
// tab's button is named (no CLI).
export function ownerHintNode({ app, lead = '', key = 'owner-hint', cls = 'muted small' } = {}) {
  return h('div', { class: 'owner-hint col', 'data-key': key },
    h('p', { class: cls }, lead ? `${lead} ${ownerHint(app)}` : ownerHint(app)),
    app ? null : h('details', { class: 'login-more small', 'data-key': `${key}-cli` }, h('summary', {}, 'On the hub’s computer'),
      h('p', { class: 'muted small' }, 'Run this and open the link it prints in this browser:'),
      h('pre', { class: 'cmd mono' }, OWNER_CLI)));
}

// Pill row shown right under the title of the screens behind the More tab. One row on a phone; it scrolls sideways when it does not fit.
export function moreTabs(current, { owner } = {}) {
  const items = [['alerts', '#/more/alerts', 'Alerts'], ['machines', '#/machines', 'Machines'], ['switches', '#/switches', 'Switches'], ['settings', '#/more/settings', 'Settings'], ['peers', '#/more/peers', 'Peers'], ['keys', '#/more/keys', 'Keys'], ['analytics', '#/more/analytics', 'Analytics']];
  if (owner) items.push(['devices', '#/more/devices', 'Devices']);
  return pillNav(items, current);
}
