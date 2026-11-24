// Limit windows: meters, labels, reset times and pace text.

import { h } from './dom.js';
import { clock, until, pct, humanKey } from '../format.js';
import { windowPhase } from './accounts-fmt.js';

export { windowPhase };

const LABELS = {
  five_hour: '5-hour',
  seven_day: '7-day',
  seven_day_opus: '7-day · Opus',
  seven_day_sonnet: '7-day · Sonnet',
  iguana_necktie: 'Cloud session credits',
};

export function windowLabel(w) {
  if (LABELS[w.key]) return LABELS[w.key];
  if (w.label) return w.label;
  const m = /^seven_day_(.+)$/.exec(w.key);
  return m ? `7-day · ${humanKey(m[1])}` : humanKey(w.key);
}

// five_hour and seven_day first, then the rest in the order the hub sent them.
export function sortWindows(ws) {
  const rank = (w) => (w.key === 'five_hour' ? 0 : w.key === 'seven_day' ? 1 : 2);
  return [...ws].sort((a, b) => rank(a) - rank(b));
}

const BAND = {
  comfortable: { cls: 'ok', text: 'Comfortable' },
  on_track: { cls: 'ok', text: 'On pace' },
  fast: { cls: 'warn', text: 'Fast' },
  runaway: { cls: 'bad', text: 'Runaway' },
};

// null when the window has no projection.
// Which rate a projection used: the last hour's, or the whole window's average ('' from a hub
// that does not say).
export function paceBasis(basis, key) {
  if (basis === 'recent') return "at the last hour's pace";
  if (basis === 'average') return key === 'seven_day' ? "at this week's average pace" : "at this window's average pace";
  return '';
}

export function paceInfo(w, now = Date.now()) {
  const p = w.pace;
  if (!p || !p.band) return null;
  const b = BAND[p.band] || { cls: 'ok', text: humanKey(p.band) };
  const proj = p.projected_pct != null ? Math.round(p.projected_pct) : null;
  const basis = paceBasis(p.basis, w.key);
  let text;
  if (p.band === 'runaway' && p.full_at) text = `At ${basis ? basis.replace(/^at /, '') : 'this pace'}: 100% by ${clock(p.full_at, now)}`;
  else if (proj != null) text = `${b.text}: ~${proj}% at reset${basis ? ` ${basis}` : ''}`;
  else text = b.text;
  return { cls: b.cls, text, band: p.band, projected: p.projected_pct || 0 };
}

// The worst pace among a seat's windows, for the one-line summary on cards.
export function worstPace(windows, now) {
  const order = ['comfortable', 'on_track', 'fast', 'runaway'];
  let best = null;
  for (const w of windows) {
    const p = paceInfo(w, now);
    if (p && (!best || order.indexOf(p.band) > order.indexOf(best.p.band))) best = { p, w };
  }
  return best;
}

export function paceChip(info, prefix) {
  return h('div', { class: `chip chip-${info.cls}` }, prefix ? `${prefix} · ` : '', info.text);
}

// The use shown for a window: 0 when idle (not started) or reset, else used_pct; null with no reading.
export function shownUse(w, now = Date.now()) {
  if (!w || w.used_pct == null) return null;
  return windowPhase(w, now) === 'current' ? w.used_pct : 0;
}

// The reset words of a window: "not started", "reset at Sat 10:10" (passed), "resets 15:48"
// or '' with no reset time. `long`: "resets in 2 h 10 min (15:48)" and the idle explanation.
export function resetWords(w, now = Date.now(), long = false) {
  const phase = windowPhase(w, now);
  if (phase === 'idle') return long ? 'not started · starts when you next use this account' : 'not started';
  if (!w.resets_at) return '';
  if (phase === 'reset') return `reset at ${clock(w.resets_at, now)}`;
  return long ? `resets ${until(w.resets_at, now)} (${clock(w.resets_at, now)})` : `resets ${clock(w.resets_at, now)}`;
}

// One meter row. `full` adds the reset countdown and pace line (Accounts page).
export function windowRow(w, { seatName, full = false, now = Date.now() } = {}) {
  const used = shownUse(w, now);
  const hot = used != null && used >= 90;
  const label = windowLabel(w);
  const words = used == null ? '' : resetWords(w, now, full);
  const value = used == null ? 'no data' : pct(used);
  const pace = full ? paceInfo(w, now) : null;
  return h('div', { class: 'win' },
    h('div', { class: 'win-head' },
      h('span', {}, label),
      h('span', { class: `mono${hot ? ' hot' : ''}` }, value, hot ? h('span', { class: 'hot-tag' }, ' high') : null,
        words && !full ? h('span', { class: 'muted win-when' }, ` · ${words}`) : null)),
    h('div', {
      class: 'meter', role: 'meter', 'aria-valuemin': 0, 'aria-valuemax': 100,
      'aria-valuenow': used == null ? undefined : Math.round(used),
      'aria-label': `${seatName ? seatName + ' ' : ''}${label} used`,
    }, h('div', { class: 'meter-fill', style: { width: `${Math.min(100, Math.max(0, used || 0))}%` } })),
    full && words ? h('div', { class: 'win-sub' },
      words,
      w.peak_pct != null ? ` · peak ${pct(w.peak_pct)}` : '',
      w.source ? ` · from ${w.source === 'both' ? 'the status line and claude.ai' : w.source === 'web' ? 'claude.ai' : 'the status line'}` : '') : null,
    pace ? h('div', { class: `win-pace pace-${pace.cls}` }, pace.text) : null);
}
