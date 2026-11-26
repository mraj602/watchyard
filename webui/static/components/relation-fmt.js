// The plain-language model of an account's "Usage and the picker" (docs/spec/pwa.md 3.0a; setup.md
// 8.5, Usage relation): the picker's verdict, the three tiles, the session cost line, the chart
// models and the "How this works" rows. Pure functions, no imports, so node can test it. The
// words for protected and aggressive come from pick-mode.js (the view joins them); this file
// only says which kind applies.

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

export const ALERT_LINE = 'A change of more than 20% on most accounts raises an alert.';
export const CHART_HINT = 'Hover or tap a day to see its value.';

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

// A number with at most d decimals and no trailing zeros: 6.78 -> "6.8", 8 -> "8".
export const num = (x, d = 0) => (x == null || Number.isNaN(x) ? '–' : String(Number(Number(x).toFixed(d))));
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;

// ---- verdict ----------------------------------------------------------------------------------

const SKIP_REASONS = [
  'it got its most new sessions for one hour',
  'its newest usage reading is too old to trust',
  'it is protected and is over its pace or 5-hour limit for new sessions',
  'it is at its limit',
];

// The pin reserve line (setup.md 8.6, Pinned sessions): "2 pinned sessions keep 20% of the 5-hour
// window and 2.1% of the week free until Thu 03:30, scaled to time left (full 10%)"; '' without
// pins. The caps above are already less it.
export function pinRule(v, nowMs, tz) {
  if (!v || !v.pins) return '';
  const pct = (x) => `${num(x, 1)}%`;
  const scaled = (r, full) => full != null && r != null && full > r + 0.05;
  const who = v.pins === 1 ? '1 pinned session keeps' : `${v.pins} pinned sessions keep`;
  const parts = [];
  let tail = '';
  if (v.pin_reserve_5h_pct != null) parts.push(`${pct(v.pin_reserve_5h_pct)} of the 5-hour window`);
  if (v.pin_reserve_7d_pct != null) {
    parts.push(`${pct(v.pin_reserve_7d_pct)} of the week`);
    if (scaled(v.pin_reserve_7d_pct, v.pin_reserve_full_7d_pct)) {
      const at = v.weekly_resets_at ? dayClock(v.weekly_resets_at, nowMs, tz) : '';
      tail = `${at ? ` until ${at}` : ''}, scaled to time left (full ${pct(v.pin_reserve_full_7d_pct)})`;
    }
  }
  if (!parts.length) return '';
  let s = `${who} ${parts.join(' and ')} free${tail}`;
  if (scaled(v.pin_reserve_5h_pct, v.pin_reserve_full_5h_pct) && !scaled(v.pin_reserve_7d_pct, v.pin_reserve_full_7d_pct)) {
    s += `; the 5-hour part is scaled to the time left in its window (full ${pct(v.pin_reserve_full_5h_pct)})`;
  }
  return s;
}

// The verdict as sentences. `empty` is the "no verdict yet" case. kindKey is protected,
// aggressive or none (not chosen: treated as aggressive); the view adds the definition.
export function verdictModel(v, nowMs, tz) {
  if (!v) return { empty: true, text: 'No verdict yet: wy account next has not reported this account.' };
  const aggressiveKind = v.kind !== 'protected';
  const kindKey = v.mode_from === 'none' ? 'none' : v.kind === 'protected' ? 'protected' : v.kind === 'aggressive' ? 'aggressive' : null;
  let headline;
  let place = '';
  if (v.eligible) {
    if (v.rank > 1) {
      headline = `Can take a new session: ${ordinal(v.rank)} in line`;
      const others = v.rank - 1;
      place = `${ordinal(v.rank)} in line: ${others} other ${others === 1 ? 'account gets' : 'accounts get'} a new session before this one; they take turns.`;
    } else {
      headline = 'Can take the next new session';
      place = 'It gets the next new session started with wy account next.';
    }
  } else {
    headline = 'Not taking new sessions now';
  }
  if (v.eligible && v.expiring) place += ' Its weekly room would otherwise expire unused, so it goes first.';

  const rules = [];
  const lvl = (x) => (x == null ? null : Math.round(x));
  const five = lvl(v.five_hour_pct);
  const fiveCap = lvl(v.five_hour_cap);
  const week = lvl(v.weekly_pct);
  const weekCap = lvl(v.weekly_cap);
  if (five != null) rules.push({ key: 'five_hour', blocking: fiveCap != null && five >= fiveCap,
    text: fiveCap != null ? `5-hour ${five}% (stops at ${fiveCap}%)` : `5-hour ${five}%` });
  if (week != null) {
    let text = `Week ${week}%`;
    if (weekCap != null) text += aggressiveKind ? ` (stops at ${weekCap}%)` : `, the even pace allows ${weekCap}% today`;
    rules.push({ key: 'week', blocking: weekCap != null && week >= weekCap, text });
  }
  const pins = pinRule(v, nowMs, tz);
  if (pins) rules.push({ key: 'pins', blocking: false, text: pins });
  if (v.burst != null) rules.push({ key: 'burst', blocking: v.burst_cap != null && v.burst >= v.burst_cap,
    text: `New sessions in the last hour: ${v.burst}${v.burst_cap != null ? ` (at most ${v.burst_cap})` : ''}` });

  let why = '';
  let frees = '';
  let noneFree = '';
  if (!v.eligible) {
    const level = Number.isInteger(v.skip_level) ? SKIP_REASONS[v.skip_level] : '';
    const reason = level || (v.reason ? String(v.reason).trim() : '');
    if (reason) why = `Not taking sessions because ${reason}`;
    const at = v.free_at ? dayClock(v.free_at, nowMs, tz) : '';
    frees = at ? `Frees up ${at}` : 'No known time when it frees up';
    const nf = v.next_free && v.next_free !== v.free_at ? dayClock(v.next_free, nowMs, tz) : '';
    if (nf) noneFree = `No account was free; the first frees up ${nf}`;
  }
  return { empty: false, headline, place, kindKey, rules, why, frees, noneFree, eligible: !!v.eligible };
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

// ---- tiles, session cost ----------------------------------------------------------------------

const daysWord = (d) => plural(Math.max(1, Math.round(d)), 'day', 'days');

// How far a factor is measured, in a clause: "measured over 7 days", "partly measured (40%),
// over 2 days", "default (not measured yet: needs 3 days and 20 readings, 4 so far)".
export function measuredLabel(f, altDays = 0) {
  if (!f) return '';
  const w = f.weight || 0;
  const days = f.days > 0 ? f.days : altDays;
  let s;
  if (w >= 1) s = `measured over ${daysWord(days)}`;
  else if (w <= 0) s = `default (not measured yet: needs 3 days and 20 readings, ${f.n || 0} so far)`;
  else s = `partly measured (${Math.round(w * 100)}%), over ${daysWord(days)}`;
  return f.implausible ? `${s}; measured value looked wrong, not used` : s;
}

// The three tiles from the ratio (5-hour points per weekly point); null without one.
export function tilesModel(cal) {
  const f = cal && cal.ratio;
  if (!f || !(f.value > 0)) return null;
  const r = f.value;
  const win = 100 / r;
  const mk = (lead, value, rest) => ({ lead, value, rest, text: `${lead}${value}${rest}` });
  return {
    tiles: [
      mk('', `${num(r, 1)}%`, ' of the 5-hour limit = 1% of the week'),
      mk('', `≈ ${num(r, 1)}`, ' full 5-hour windows use up the week'),
      mk('One full 5-hour window ≈ ', `${num(win, win < 10 ? 1 : 0)}%`, ' of the week'),
    ],
    label: measuredLabel(f, cal.f ? cal.f.days : 0),
  };
}

// The session cost line from the lane charge.
export function sessionLine(cal) {
  if (!cal || cal.lane_charge == null || !cal.lane_price) return 'Not measured yet: the hub has no calibration for this account.';
  const p = cal.lane_price;
  const head = `Each new session is set aside ${num(cal.lane_charge, 1)}% of the 5-hour limit until its real use shows up`;
  const w = p.weight || 0;
  if (p.implausible) return `${head} (default; the measured value looked wrong, not used)`;
  if (w >= 1) return `${head} (measured from ${plural(p.n || 0, 'session', 'sessions')})`;
  if (w <= 0) return `${head} (default; measured after 20 sessions, ${p.n || 0} so far)`;
  return `${head} (partly measured, ${Math.round(w * 100)}%, from ${plural(p.n || 0, 'session', 'sessions')})`;
}

// ---- charts -----------------------------------------------------------------------------------

// Round axis ticks: 0 and two more steps of 1, 2, 2.5 or 5 times a power of ten that cover max.
export function niceTicks(max) {
  const m = max > 0 && Number.isFinite(max) ? max : 1;
  const raw = m / 2;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((k) => k * mag).find((s) => s >= raw - 1e-9);
  const top = step * 2;
  return { top, ticks: [0, step, top] };
}

// The model colours: a slot per model family (1..4), the rest take the next free slot by order.
export function modelSlots(labels) {
  const out = {};
  let next = 4;
  for (const l of labels) {
    if (!l || out[l]) continue;
    const s = /opus/i.test(l) ? 1 : /sonnet/i.test(l) ? 2 : /haiku/i.test(l) ? 3 : null;
    out[l] = s || (next > 6 ? 6 : next++);
  }
  return out;
}

const axisLabels = (days) => (days.length ? [{ at: 0, text: shortDay(days[0].day) }, { at: Math.floor((days.length - 1) / 2), text: shortDay(days[Math.floor((days.length - 1) / 2)].day) },
  { at: days.length - 1, text: shortDay(days[days.length - 1].day) }] : []);

// The daily ratio chart. Returns { hidden, hiddenText } when no day has a ratio.
export function ratioChart(days) {
  const ds = days || [];
  if (!ds.some((d) => d.ratio != null)) return { hidden: true, hiddenText: 'Not enough readings in the last 2 weeks to show this.' };
  const slots = modelSlots(ds.map((d) => d.model));
  const max = Math.max(...ds.map((d) => d.ratio || 0));
  const { top, ticks } = niceTicks(max);
  const bars = ds.map((d) => {
    const has = d.ratio != null;
    const text = has
      ? `${shortDay(d.day)}: ${num(d.ratio, 1)}% of the 5-hour limit per 1% of the week${d.model ? ` · mostly ${d.model}` : ''} · ${plural(d.pairs || 0, 'reading', 'readings')}`
      : `${shortDay(d.day)}: no data`;
    return { day: d.day, value: has ? d.ratio : null, frac: has ? Math.max(0.02, d.ratio / top) : 0, slot: has && d.model ? slots[d.model] : 0, text };
  });
  const legend = [];
  for (const d of ds) if (d.ratio != null && d.model && !legend.some((l) => l.model === d.model)) legend.push({ model: d.model, slot: slots[d.model] });
  const withData = bars.filter((b) => b.value != null);
  return {
    hidden: false,
    title: '5-hour % per 1% of the week, each day',
    sub: 'Higher: the week lasts longer compared with the 5-hour limit.',
    unit: '%',
    ticks: ticks.map((t) => ({ value: t, label: `${num(t, 1)}%`, frac: t / top })),
    xLabels: axisLabels(ds),
    bars,
    legend,
    line: null,
    summary: `5-hour % per 1% of the week, each day: ${withData.length} of ${bars.length} days have a value, from ${num(Math.min(...withData.map((b) => b.value)), 1)}% to ${num(Math.max(...withData.map((b) => b.value)), 1)}%.`,
  };
}

// Dollars: "$1.67", "$0.042".
export function perPoint(x) {
  if (x == null || Number.isNaN(x)) return '–';
  return `$${x >= 1 ? x.toFixed(2) : x.toFixed(3)}`;
}

const tick$ = (x) => (x >= 1 ? `$${num(x, 2)}` : `$${num(x, 3)}`);

// "10% more now", "5% less now", "about the same".
export function changeWords(change) {
  if (change == null) return '';
  const p = Math.round((change - 1) * 100);
  return p > 0 ? `${p}% more now` : p < 0 ? `${-p}% less now` : 'the same';
}

// The note under a seat that is not Claude-Code-heavy this week, or "" (pwa.md 3.0a).
export function heavyNote(heavy) {
  const w = heavy && heavy.this_week;
  if (!w || w.heavy) return '';
  const tail = 'this is not a fair measure this week and does not count toward limit-change alerts';
  if (w.cc_share == null) return `No 5-hour rise this week: ${tail}.`;
  if (w.rise < 20) return `Only ${num(w.rise, 0)} points of 5-hour rise this week: ${tail}.`;
  return `${Math.round(w.cc_share * 100)}% of the 5-hour rise had Claude Code tokens: ${tail}.`;
}

// The capacity part: a sentence per model with an index, the alert line once, a chart only
// where both weeks have a value, and one line for each model that had no index at all.
export function capacityModel(models, heavy) {
  const ms = models || [];
  const used = [];
  const unused = [];
  for (const m of ms) {
    const any = (m.days || []).some((d) => d.index != null) || m.this_week != null;
    if (any) used.push(m); else unused.push(`${m.model}: not used in the last 2 weeks.`);
  }
  const slots = modelSlots(used.map((m) => m.model));
  const muted = !!heavyNote(heavy);
  const items = used.map((m) => {
    let sentence;
    if (m.this_week == null) sentence = `${m.model}: not enough data this week yet.`;
    else {
      sentence = `This week 1% of the 5-hour limit bought about ${perPoint(m.this_week)} of ${m.model} work at API prices. `;
      sentence += m.last_week == null ? 'Last week: not enough data yet.'
        : `Last week: ${perPoint(m.last_week)}${changeWords(m.change != null ? m.change : m.last_week ? m.this_week / m.last_week : null) ? ` (${changeWords(m.change != null ? m.change : m.last_week ? m.this_week / m.last_week : null)})` : ''}.`;
    }
    let chart = null;
    if (m.this_week != null && m.last_week != null) {
      const ds = m.days || [];
      const max = Math.max(m.last_week, ...ds.map((d) => d.index || 0));
      const { top, ticks } = niceTicks(max);
      const bars = ds.map((d) => ({ day: d.day, value: d.index, frac: d.index == null ? 0 : Math.max(0.02, d.index / top), slot: d.index == null ? 0 : slots[m.model],
        text: d.index == null ? `${shortDay(d.day)}: no data` : `${shortDay(d.day)}: ${perPoint(d.index)} of ${m.model} work per 1% of the 5-hour limit` }));
      chart = { hidden: false, title: `${m.model}: API dollars per 1% of the 5-hour limit, each day`,
        sub: 'Higher: the same 5-hour limit buys more work.',
        unit: '$', ticks: ticks.map((t) => ({ value: t, label: tick$(t), frac: t / top })), xLabels: axisLabels(ds), bars, legend: [],
        line: { frac: m.last_week / top, label: `Last week: ${perPoint(m.last_week)}` }, muted,
        summary: `${m.model}: dollars of work per 1% of the 5-hour limit, each day, with last week's level of ${perPoint(m.last_week)}.` };
    }
    return { model: m.model, sentence, chart };
  });
  return { items, unused, alertLine: items.some((i) => i.sentence.startsWith('This week')) ? ALERT_LINE : '', note: heavyNote(heavy), muted };
}

// ---- how this works ---------------------------------------------------------------------------

const RESET_WORDS = { seat_change: 'the account changed', under_read: 'the meter read low', over_read: 'the meter read high' };

// Measured-from words for a factor: "measured from 1504 readings over 7 days".
function stateWords(f) {
  if (!f) return '';
  if (f.implausible) return 'the measured value looked wrong, so it is not used';
  const w = f.weight || 0;
  const n = plural(f.n || 0, 'reading', 'readings');
  if (w >= 1) return `measured from ${n} over ${daysWord(f.days || 0)}`;
  if (w <= 0) return `default, not measured yet (${n} so far)`;
  return `partly measured (${Math.round(w * 100)}%) from ${n} over ${daysWord(f.days || 0)}`;
}

// The "How this works" rows: [{ name, text }]. Always the two general rows; the measured
// ones only with a calibration.
export function howRows(cal, nowMs, tz) {
  const rows = [];
  if (cal && cal.f) rows.push({ name: 'Cost of Claude Code use',
    text: `How many % of the 5-hour limit a million tokens of Claude Code use cost: ${num(cal.f.value, 2)}, ${stateWords(cal.f)}.` });
  if (cal && cal.ratio) rows.push({ name: '5-hour limit against the week',
    text: `5-hour % per 1% of the week: ${num(cal.ratio.value, 2)}, ${stateWords(cal.ratio)}.` });
  if (cal && cal.lane_price && cal.lane_charge != null) rows.push({ name: 'Cost of a new session',
    text: `${num(cal.lane_charge, 1)}% of the 5-hour limit set aside per new session, ${stateWords(cal.lane_price).replace('readings', 'sessions').replace('reading', 'session')}.` });
  rows.push({ name: 'Where the numbers come from',
    text: 'The hub works them out from every machine\'s readings. Until it has about 3 days and 20 readings it uses safe defaults, then it moves to the measured values.' });
  if (cal && cal.reset && cal.reset.at) rows.push({ name: 'Last restart',
    text: `${dayClock(cal.reset.at, nowMs, tz)}: ${RESET_WORDS[cal.reset.reason] || cal.reset.reason}. Measuring started again from the defaults.` });
  rows.push({ name: 'The capacity figure',
    text: 'The dollars of work, at API prices, that 1% of the 5-hour limit buys, worked out per model from each day\'s token use. A big change from last week usually means Anthropic changed the limit.' });
  return rows;
}
