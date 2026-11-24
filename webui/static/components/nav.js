// Left sidebar on desktop, bottom tab bar on a phone (same markup, CSS decides).

import { h } from './dom.js';
import { dot } from './cards.js';
import { seatIndex, seatName } from './seat.js';
import { pct, clock } from '../format.js';
import { TAB_OF } from '../route.js';
import { liveState, inApp } from '../store.js';

const LIVE_TEXT = {
  live: 'Live',
  connecting: 'Connecting…',
  reconnecting: 'Reconnecting…',
  polling: 'Refreshing every 15 s',
};
const LIVE_TITLE = {
  live: 'Changes show up as they happen',
  connecting: 'Opening the live connection to the hub',
  reconnecting: 'The live connection dropped; the board keeps refreshing meanwhile',
  polling: 'The live connection is not available; the board refreshes every 15 s',
};

// The live badge. Only a hub the board cannot reach is shown as a warning; a dropped stream
// is neutral grey, and only after a few seconds (store.liveState).
export function liveBadge(store) {
  const offline = store.offline;
  if (offline) return h('span', { class: 'live live-warn' }, h('span', { class: 'live-dot', 'aria-hidden': 'true' }), `Offline · showing ${clock(new Date(offline.since).toISOString())}`);
  const s = liveState();
  return h('span', { class: `live live-${s === 'live' ? 'ok' : 'idle'}`, title: LIVE_TITLE[s], 'data-key': 'live-badge' }, h('span', { class: 'live-dot', 'aria-hidden': 'true' }), LIVE_TEXT[s] || s);
}

// `where`: phone = bottom tabs only, desk = sidebar only, both = everywhere (CSS hides the
// other, nav-phone / nav-desk). Tabs on a phone: Now, Sessions, Accounts, More; the sidebar
// on a computer: Now, Sessions, Accounts, Machines, Switches, Alerts, Settings, Analytics, Peers (+ Devices for the owner).
const ITEMS = [
  { id: 'now', href: '#/', label: 'Now', icon: 'M3 12h4l3-8 4 16 3-8h4', where: 'both' },
  { id: 'sessions', href: '#/sessions', label: 'Sessions', icon: 'M4 6h16M4 12h16M4 18h16', where: 'both' },
  { id: 'accounts', href: '#/accounts', label: 'Accounts', icon: 'M12 12a4 4 0 100-8 4 4 0 000 8zM4 20c0-4 4-6 8-6s8 2 8 6', where: 'both' },
  { id: 'machines', href: '#/machines', label: 'Machines', icon: 'M3 5h18v10H3zM8 19h8M12 15v4', where: 'desk' },
  { id: 'switches', href: '#/switches', label: 'Switches', icon: 'M7 4v13M4 14l3 3 3-3M17 20V7M14 10l3-3 3 3', where: 'desk' },
  { id: 'alerts', href: '#/more/alerts', label: 'Alerts', icon: 'M6 9a6 6 0 0112 0c0 6 2 7 2 7H4s2-1 2-7zM10 20a2 2 0 004 0', where: 'desk' },
  { id: 'settings', href: '#/more/settings', label: 'Settings', icon: 'M12 15a3 3 0 100-6 3 3 0 000 6zM19 12l2-1-1-3-2 .5-1.5-1.5.5-2-3-1-1 2h-2l-1-2-3 1 .5 2L6.5 8.5 4.5 8l-1 3 2 1v2l-2 1 1 3 2-.5L8 19.5l-.5 2 3 1 1-2h2l1 2 3-1-.5-2 1.5-1.5 2 .5 1-3-2-1z', where: 'desk' },
  { id: 'analytics', href: '#/more/analytics', label: 'Analytics', icon: 'M5 20V10M12 20V4M19 20v-7', where: 'desk' },
  { id: 'peers', href: '#/more/peers', label: 'Peers', icon: 'M4 7h12l-3-3M20 17H8l3 3', where: 'desk' },
  { id: 'keys', href: '#/more/keys', label: 'Keys', icon: 'M15 7a4 4 0 11-3.9 5H4v-2h2v2M6 12v2', where: 'desk' },
  { id: 'devices', href: '#/more/devices', label: 'Devices', icon: 'M8 3h8a1 1 0 011 1v16a1 1 0 01-1 1H8a1 1 0 01-1-1V4a1 1 0 011-1zM11 18h2', where: 'desk', owner: true },
  { id: 'more', href: '#/more', label: 'More', icon: 'M5 12h.01M12 12h.01M19 12h.01', where: 'phone' },
];

const icon = (d) => {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('width', '20'); svg.setAttribute('height', '20');
  svg.setAttribute('aria-hidden', 'true'); svg.setAttribute('class', 'nav-icon');
  const p = document.createElementNS(ns, 'path');
  p.setAttribute('d', d); p.setAttribute('fill', 'none'); p.setAttribute('stroke', 'currentColor');
  p.setAttribute('stroke-width', '1.8'); p.setAttribute('stroke-linecap', 'round'); p.setAttribute('stroke-linejoin', 'round');
  svg.append(p);
  return svg;
};

const logo = () => {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 22 22'); svg.setAttribute('width', '22'); svg.setAttribute('height', '22'); svg.setAttribute('aria-hidden', 'true');
  const mk = (tag, attrs) => {
    const el = document.createElementNS(ns, tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    return el;
  };
  // The same tile mark as the icon (app/build/icons-src/mark-small.svg).
  const g = mk('g', { transform: 'translate(2.5 2.5) scale(0.7727)', fill: 'none', stroke: '#E8B04B', 'stroke-width': '2.6' });
  g.append(mk('path', { d: 'M3 18 L11 4 L19 18' }), mk('path', { d: 'M7 18 L11 11 L15 18' }));
  svg.append(mk('rect', { width: '22', height: '22', rx: '5', fill: '#17191B' }), g);
  return svg;
};

export function renderNav(navEl, { page, store, onLogout }) {
  const o = store.overview;
  const needs = o ? o.counts.needs_you : 0;
  const idx = o ? seatIndex(o.seats) : new Map();
  const owner = !!(store.me && store.me.owner);
  // In the desktop app the app's own bar carries the name, and the app signs the board in.
  const app = inApp();
  const lit = (it) => it.id === page || (it.id === 'more' && TAB_OF[page] === 'more');
  navEl.replaceChildren(h('div', { class: 'nav-inner' },
    app ? null : h('div', { class: 'brand' }, logo(), h('span', {}, 'Watchyard')),
    h('ul', { class: 'nav-list' }, ITEMS.filter((it) => !it.owner || owner).map((it) => h('li', { class: `nav-${it.where}` },
      h('a', { href: it.href, 'data-key': `nav-${it.id}`, 'aria-current': lit(it) ? 'page' : undefined },
        icon(it.icon),
        h('span', { class: 'nav-label' }, app && it.id === 'settings' ? 'Hub settings' : it.label),
        it.id === 'sessions' && needs > 0 ? h('span', { class: 'count', 'aria-label': `${needs} need you` }, needs) : null)))),
    o && o.seats.length ? h('div', { class: 'nav-accounts' },
      h('div', { class: 'nav-heading' }, 'Accounts'),
      h('ul', {}, o.seats.filter((s) => !s.hidden).map((s) => {
        const five = (s.windows || []).find((w) => w.key === 'five_hour');
        const used = five ? five.used_pct : null;
        return h('li', {}, h('a', { href: `#/accounts?seat=${encodeURIComponent(s.id)}`, 'data-key': `nav-seat-${s.id}`, title: seatName(s) },
          dot(idx.get(s.id).colour), h('span', { class: 'grow nav-seat-name' }, seatName(s)),
          h('span', { class: `mono small${used >= 90 ? ' hot' : ' muted'}` }, used == null ? '' : pct(used))));
      }))) : null,
    h('div', { class: 'nav-foot' },
      liveBadge(store),
      app ? null : h('button', { type: 'button', class: 'linkish', onclick: onLogout, 'data-key': 'logout' }, 'Sign out'))));
}
