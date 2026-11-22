// Cards shared by several pages: needs-you, account, machine.

import { h } from './dom.js';
import { ago, agoLabel, clock, plural, projectOf, humanKey, httpsUrl, num } from '../format.js';
import { statusOf, backlogText, reachChip } from './machines-fmt.js';
import { sessionSeat, seatName } from './seat.js';
import { stateInfo } from './state.js';
import { windowRow, sortWindows, worstPace, paceChip, windowLabel } from './windows.js';
import { alertLine, alertButtons, canAct, seenRow } from './alerts.js';
import { canSnoozeRow } from './alerts-fmt.js';
import { swipeable } from './swipe.js';
import { toast } from './ui.js';
import { snooze } from '../store.js';

export const dot = (colour) => h('span', { class: 'dot', 'aria-hidden': 'true', style: { background: colour } });

export const sessionLink = (s, text) => h('a', { href: `#/sessions/${encodeURIComponent(s.id)}`, 'data-key': `s-${s.id}` }, text);

async function copy(text, button) {
  const old = button.textContent;
  try {
    await navigator.clipboard.writeText(text);
    button.textContent = 'Copied';
  } catch (_) {
    button.textContent = 'Copy failed';
  }
  setTimeout(() => { button.textContent = old; }, 1600);
}

export function copyButton(text, label, key) {
  return h('button', { type: 'button', class: 'btn', 'data-key': key, onclick: (e) => copy(text, e.currentTarget) }, label);
}

// What the session wants from you, as one line of text. Never the internal `detail` code.
// A session waiting on a permission says what Claude Code asked (its notification text) or at
// least which tool; its last Stop message is older than the wait, so it is not used.
export function ask(s) {
  if (s.state === 'needs_permission') return s.notification || (s.tool_name ? `Permission to use ${s.tool_name}` : '');
  if (s.state === 'asked' && !s.question && s.tool_name === 'ExitPlanMode') return s.notification || 'A plan is ready for your review.';
  return s.question || s.last_message || '';
}

// A rate-limited session names the window that is full and when it resets (N9), from the
// account's own windows; otherwise its last line.
export function askText(s, seatIdx, now = Date.now()) {
  if (s.state === 'failed' && s.detail === 'rate_limit' && seatIdx) {
    const known = s.seat && seatIdx.get(s.seat);
    const full = known && sortWindows(known.seat.windows || []).find((w) => w.used_pct != null && w.used_pct >= 100);
    if (full) return `${windowLabel(full)} limit reached${full.resets_at ? ` · resets ${clock(full.resets_at, now)}` : ''}`;
  }
  return ask(s);
}

// Which needs-you card has its Seen / Snooze menu open (phone), kept across redraws.
export const ui = { menu: null };

export function needsCard(s, seatIdx, now) {
  const info = stateInfo(s);
  const seat = sessionSeat(s, seatIdx);
  const mono = s.state === 'needs_permission';
  const a = s.alert;
  const acts = canAct(s);
  const href = `#/sessions/${encodeURIComponent(s.id)}`;
  const open = ui.menu === s.id;
  const card = h('article', {
    class: `card needs-card needs-${info.cls}${a && a.status === 'seen' ? ' is-seen' : ''}${open ? ' menu-open' : ''}`,
    'data-session': s.id,
    onclick: (e) => { if (!e.target.closest('a, button')) location.hash = href; },
  },
    h('div', { class: 'card-top' },
      h('span', { class: `badge badge-${info.cls === 'failed' ? 'failed' : 'needs'}` }, info.long),
      a && a.status === 'seen' ? h('span', { class: 'seen-tag' }, 'seen') : null,
      h('span', { class: 'muted card-age' }, ago(s.state_since, now) || '–', h('span', { class: 'desk-only' }, ' waiting')),
      acts ? h('button', {
        type: 'button', class: 'menu-btn', 'aria-label': 'Seen and snooze', 'aria-expanded': String(open), 'data-key': `menu-${s.id}`,
        onclick: () => { ui.menu = ui.menu === s.id ? null : s.id; window.dispatchEvent(new Event('wy-rerender')); },
      }, '⋯') : null),
    h('p', { class: `ask${mono ? ' mono' : ''}` }, askText(s, seatIdx, now) || 'No details were sent.'),
    h('div', { class: 'meta-line' }, dot(seat.colour), `${seat.label} · ${projectOf(s)}${s.machine_name ? ' · ' + s.machine_name : ''}`),
    a ? h('div', { class: 'meta-line alert-meta' }, alertLine(a, now)) : null,
    acts ? alertButtons(s) : null,
    h('div', { class: 'actions card-links' },
      httpsUrl(s.remote_url) ? h('a', { class: 'btn btn-primary', href: httpsUrl(s.remote_url), target: '_blank', rel: 'noopener noreferrer' }, 'Open in Claude') : null,
      s.resume ? copyButton(s.resume, 'Copy resume command', `copy-${s.id}`) : null,
      h('a', { class: httpsUrl(s.remote_url) || s.resume ? 'btn' : 'btn btn-primary', href, 'data-key': `view-${s.id}` }, 'View session')));
  if (!acts) return card;
  // Swipe right = Seen, left = Snooze, on touch screens; the buttons above do the same.
  // A row whose alert cannot push (skipped, or none) swipes to Seen only.
  const snoozable = canSnoozeRow(s);
  const wrap = h('div', { class: 'swipe' },
    h('span', { class: 'swipe-hint swipe-hint-l', 'aria-hidden': 'true' }, 'Seen'),
    snoozable ? h('span', { class: 'swipe-hint swipe-hint-r', 'aria-hidden': 'true' }, 'Snooze 15 min') : null,
    card);
  const guard = (fn) => () => fn().catch((ex) => toast(ex.status === 409 ? 'That alert already changed' : ex.message));
  swipeable(card, {
    onRight: guard(() => seenRow(s)),
    onLeft: snoozable ? guard(() => snooze(a.id, s.id)) : null,
  });
  return wrap;
}

// Home-screen account card: first windows, pace chip, where it is used.
export function seatCard(seat, colour, machinesText, now) {
  const ws = sortWindows(seat.windows || []);
  const main = ws.filter((w) => w.key === 'five_hour' || w.key === 'seven_day');
  const rest = ws.filter((w) => !main.includes(w));
  const shownRest = rest.slice(0, 2);
  const worst = worstPace(ws, now);
  const name = seatName(seat);
  return h('article', { class: 'card seat-card', style: { '--seat': colour } },
    h('div', { class: 'seat-head' },
      h('span', { class: 'seat-name' }, name),
      h('span', { class: 'muted small' }, planText(seat))),
    h('div', { class: 'muted small seat-sub' }, [seat.email, machinesText].filter(Boolean).join(' · ')),
    ws.length === 0 ? noUsage() : null,
    [...(main.length ? main : ws.slice(0, 2)), ...shownRest].map((w) => windowRow(w, { seatName: name, now })),
    rest.length > shownRest.length ? h('div', { class: 'muted small' }, `+ ${plural(rest.length - shownRest.length, 'more window')}`) : null,
    worst ? paceChip(worst.p, worst.w.key === 'five_hour' ? '' : windowRowLabel(worst.w)) : null);
}

// Why a card has no meters, and what makes them appear (N3).
export const noUsage = () => h('p', { class: 'muted small no-usage' }, 'No usage yet. It appears after Claude Code runs in a profile set up with ',
  h('code', {}, 'wy setup'), ' (it adds the status line that reports limits).');

function windowRowLabel(w) {
  return w.key === 'seven_day' ? '7-day' : (w.label || humanKey(w.key));
}

export function planText(seat) {
  return [seat.plan, seat.org_name].filter(Boolean).join(' · ');
}

// Reads like "laptop, gpu-box" from the seat's places.
export function placesText(seat) {
  const names = [...new Set((seat.places || []).map((p) => p.machine_name).filter(Boolean))];
  return names.join(', ');
}

export function machineStatus(m, now = Date.now()) {
  return statusOf(m, now, { ago, agoLabel });
}

// live: open sessions on the machine (from the session list), else the hub's count.
export function machineSummary(m, now, live) {
  const st = machineStatus(m, now);
  const backlog = backlogText(m, now, { ago, clock });
  const n = live == null ? m.live_sessions || 0 : live;
  return h('a', { class: 'card machine-mini', href: `#/machines?machine=${encodeURIComponent(m.id)}`, 'data-key': `mini-${m.id}` },
    h('div', { class: 'mini-text' },
      h('div', { class: 'strong mini-name', title: m.name }, m.name),
      h('div', { class: 'muted small' }, [`${plural(n, 'open session')}`, backlog].filter(Boolean).join(' · '))),
    h('span', { class: `small status-${st.cls}` }, st.text));
}

// Extra usage and spend from claude.ai, as sentences (A4): credits are in cents.
export function usageMoney(seat) {
  const out = [];
  const money = (cents, cur) => {
    const c = String(cur || 'USD').toUpperCase();
    const v = (Number(cents) / 100).toFixed(2);
    return c === 'USD' ? `$${num(Math.floor(v))}.${v.split('.')[1]}` : `${v} ${c}`;
  };
  const x = seat.extra_usage;
  if (x && typeof x === 'object') {
    if (x.is_enabled === false) out.push('Extra usage is off.');
    else if (x.used_credits != null && x.monthly_limit != null) out.push(`Extra usage: ${money(x.used_credits, x.currency)} of ${money(x.monthly_limit, x.currency)} used this month.`);
    else if (x.used_credits != null) out.push(`Extra usage: ${money(x.used_credits, x.currency)} used this month.`);
    else if (x.is_enabled) out.push('Extra usage is on.');
  }
  const sp = seat.spend;
  // One sentence when both say the same amount (R4).
  const same = x && typeof x === 'object' && x.used_credits != null && String(x.currency || 'USD').toUpperCase() === 'USD' && sp && Math.round(sp.month_to_date_usd * 100) === Math.round(Number(x.used_credits));
  if (sp && typeof sp === 'object' && sp.month_to_date_usd != null && !same) out.push(`${money(Math.round(sp.month_to_date_usd * 100), 'USD')} spent this month.`);
  return out;
}

// Primitive fields of a raw web-usage object (extra_usage, spend) as a definition list.
export function kvList(obj) {
  if (!obj || typeof obj !== 'object') return null;
  const rows = [];
  const walk = (o, prefix) => {
    for (const [k, v] of Object.entries(o)) {
      if (v == null) continue;
      if (typeof v === 'object' && !Array.isArray(v)) walk(v, `${prefix}${humanKey(k)} · `);
      else rows.push([prefix + humanKey(k), Array.isArray(v) ? v.join(', ') : typeof v === 'boolean' ? (v ? 'yes' : 'no') : String(v)]);
    }
  };
  walk(obj, '');
  if (!rows.length) return null;
  return h('dl', { class: 'kv' }, rows.flatMap(([k, v]) => [h('dt', {}, k), h('dd', { class: 'mono' }, v)]));
}

