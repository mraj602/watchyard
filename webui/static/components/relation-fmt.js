// Pure helpers of an account's "Usage and the picker" (docs/spec/pwa.md 3.0a; setup.md 8.5,
// Usage relation): the picker's verdict, the calibration rows, the daily 5 h per 7 d bars and
// the capacity index per model. No imports, so node can test it.

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

// The skip levels of the lanes strategy (setup.md 8.5).
export const SKIP_WORDS = ['burst cap', 'stale', 'protected rule', 'limit'];

// "2026-11-12" -> "12 Nov".
export function shortDay(day) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day || '');
  return m ? `${Number(m[3])} ${MONTHS[Number(m[2]) - 1]}` : String(day || '');
}

// 1 -> "1st", 2 -> "2nd", 11 -> "11th", 23 -> "23rd".
export function ordinal(n) {
  const t = n % 100;
  if (t >= 11 && t <= 13) return `${n}th`;
  return `${n}${({ 1: 'st', 2: 'nd', 3: 'rd' })[n % 10] || 'th'}`;
}

// A time for the board: "14:00" today, "Thu 14:00" within a week, else "12 Nov 14:00". tz is
// an IANA zone (undefined: the browser's).
export function dayClock(iso, nowMs, tz) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const parts = (ms) => {
    const o = {};
    for (const p of new Intl.DateTimeFormat('en-GB', { timeZone: tz, weekday: 'short', day: 'numeric', month: 'short',
      year: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(new Date(ms))) o[p.type] = p.value;
    return o;
  };
  const a = parts(t);
  const n = parts(nowMs);
  const hm = `${a.hour}:${a.minute}`;
  if (a.day === n.day && a.month === n.month && a.year === n.year) return hm;
  if (Math.abs(t - nowMs) < 7 * 86400000) return `${a.weekday} ${hm}`;
  return `${a.day} ${a.month} ${hm}`;
}

// The verdict's main line (pwa.md 3.0a, Picker).
export function verdictLine(v, nowMs, tz) {
  if (!v) return 'No verdict yet: wy account next has not reported this account.';
  if (v.eligible) {
    const parts = ['Eligible'];
    if (v.rank === 1) parts.push('next in line');
    else if (v.rank > 1) parts.push(`${ordinal(v.rank)} in line`);
    if (v.expiring) parts.push('weekly room expiring');
    if (v.kind) parts.push(v.kind);
    return parts.join(' · ');
  }
  let line = 'Not picked';
  const why = (v.reason || '').trim();
  const level = Number.isInteger(v.skip_level) ? SKIP_WORDS[v.skip_level] : '';
  if (why && level) line += `: ${why} (${level})`;
  else if (why || level) line += `: ${why || level}`;
  if (v.free_at) {
    const at = dayClock(v.free_at, nowMs, tz);
    if (at) line += ` · frees up ${at}`;
  }
  // next_free: the run had no eligible account; when the first one of all frees up.
  if (v.next_free && v.next_free !== v.free_at) {
    const at = dayClock(v.next_free, nowMs, tz);
    if (at) line += ` · no account was free, the first frees up ${at}`;
  }
  return line;
}

// "as of 12 min ago on gpu-box"; over an hour: "older than an hour, from gpu-box".
export function verdictAge(v, nowMs) {
  if (!v) return '';
  const t = Date.parse(v.at);
  if (Number.isNaN(t)) return '';
  const on = v.machine_name || v.machine || '';
  const min = Math.max(0, Math.round((nowMs - t) / 60000));
  if (min > 60) return `older than an hour${on ? `, from ${on}` : ''}`;
  const ago = min < 1 ? 'just now' : `${min} min ago`;
  return `as of ${ago}${on ? ` on ${on}` : ''}`;
}

const num = (x, d) => (x == null || Number.isNaN(x) ? '–' : String(Number(x.toFixed(d))));

// measured / blending, 40% measured / fallback, with n and the days.
export function factorState(f) {
  if (!f) return '';
  const w = f.weight || 0;
  let s = w >= 1 ? 'measured' : w <= 0 ? 'fallback' : `blending, ${Math.round(w * 100)}% measured`;
  if (f.n > 0) s += ` · n ${f.n}`;
  if (f.days > 0) s += ` · ${num(f.days, 1)} days`;
  if (f.implausible) s += ' · implausible measurement, not used';
  return s;
}

const RESET_WORDS = { seat_change: 'the account changed', under_read: 'the meter read low', over_read: 'the meter read high' };

// The calibration rows (pwa.md 3.0a, Calibration); null when the seat is not calibrated yet.
export function calibRows(cal, nowMs, tz) {
  if (!cal) return null;
  const rows = [];
  if (cal.f) rows.push({ name: 'Meter factor f', value: `${num(cal.f.value, 2)} points per million units`, state: factorState(cal.f) });
  if (cal.ratio) rows.push({ name: 'Ratio 5 h : 7 d', value: `${num(cal.ratio.value, 2)} 5-hour points per 7-day point`, state: factorState(cal.ratio) });
  if (cal.lane_price) {
    rows.push({ name: 'Lane charge c', value: `${num(cal.lane_charge, 1)} points (lane price ${num(cal.lane_price.value, 1)})`,
      state: factorState(cal.lane_price) });
  }
  let restart = '';
  if (cal.reset && cal.reset.at) {
    restart = `Restarted ${dayClock(cal.reset.at, nowMs, tz)}: ${RESET_WORDS[cal.reset.reason] || cal.reset.reason}`;
  }
  return { rows, restart };
}

// The first letter of a model label ("Opus 4.6" -> "O").
export function modelInitial(label) {
  const s = String(label || '').trim();
  return s ? s[0].toUpperCase() : '';
}

// The 14 daily ratio bars with the model initials and the legend.
export function ratioBars(days) {
  const ds = days || [];
  const max = Math.max(1, ...ds.map((d) => d.ratio || 0));
  const legend = [];
  const seen = new Set();
  const bars = ds.map((d) => {
    const ini = modelInitial(d.model);
    if (d.model && !seen.has(d.model)) { seen.add(d.model); legend.push(`${ini} ${d.model}`); }
    let label = `${shortDay(d.day)}: `;
    if (d.ratio == null) label += d.pairs ? `under 2 weekly points · ${d.pairs} pairs` : 'no ratio';
    else label += `${num(d.ratio, 1)}${d.model ? ` · ${d.model}` : ''} · ${d.pairs} ${d.pairs === 1 ? 'pair' : 'pairs'}`;
    return { day: d.day, ratio: d.ratio, height: d.ratio == null ? 0 : Math.max(2, Math.round((d.ratio / max) * 100)), initial: ini, label };
  });
  return { bars, legend: legend.join(' · '), max };
}

// Dollars per 5-hour point: "$0.042", "$1.25".
export function perPoint(x) {
  if (x == null || Number.isNaN(x)) return '–';
  return `$${x >= 1 ? x.toFixed(2) : x.toFixed(3)}`;
}

// "Opus 4.6 · $0.042 per 1% this week · 1.05× last week" (pwa.md 3.0a, capacity).
export function capacityLine(m) {
  if (m.this_week == null) return `${m.model} · not enough days`;
  let s = `${m.model} · ${perPoint(m.this_week)} per 1% this week`;
  s += m.change != null ? ` · ${num(m.change, 2)}× last week` : ' · last week: not enough days';
  return s;
}

// The normalised daily bars of a model, with the 1.0 line's height (percent of the chart).
export function capacityBars(m) {
  const ds = m.days || [];
  const top = Math.max(1.5, ...ds.map((d) => d.normalised || 0));
  const bars = ds.map((d) => {
    let label = `${shortDay(d.day)}: `;
    if (d.index == null) label += d.d5 > 0 ? `under 5 points (${num(d.d5, 1)})` : 'no index';
    else {
      label += `${perPoint(d.index)} per 1%`;
      if (d.normalised != null) label += ` · ${num(d.normalised, 2)}× last week`;
      label += ` · ${num(d.d5, 1)} points`;
    }
    const v = d.normalised;
    return { day: d.day, value: v, height: v == null ? 0 : Math.max(2, Math.round((v / top) * 100)), label };
  });
  return { bars, line: Math.round((1 / top) * 100) };
}

// The note under a seat that is not Claude-Code-heavy this week, or "" (pwa.md 3.0a).
export function heavyNote(heavy) {
  const w = heavy && heavy.this_week;
  if (!w || w.heavy) return '';
  const tail = 'this index is not a fair measure and does not count toward limit-change alerts';
  if (w.cc_share == null) return `No 5-hour rise this week: ${tail}.`;
  if (w.rise < 20) return `Only ${num(w.rise, 0)} points of 5-hour rise this week: ${tail}.`;
  return `${Math.round(w.cc_share * 100)}% of the 5-hour rise had Claude Code tokens: ${tail}.`;
}
