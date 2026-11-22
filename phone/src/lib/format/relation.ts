// The sentences, tiles and chart models of an account's "Usage and the picker" (docs/spec/pwa.md
// 3.0a, phone.md P4; the same words as the board's components/relation-fmt.js). Written for
// someone who has never read the spec: plain words, no single-letter symbols. Pure: the screen
// only draws what these return.
import type { Calibration, CalibrationFactor, RelationHeavy, RelationModel, UsageRelation, Verdict } from '../../contract/api';
import { shortDay } from './analytics';
import { ago, clock, toMs } from './core';
import { MODE_NAMES, MODE_WORDS } from './pickMode';

/** A disclosure opened again after this long loads the relation again. */
export const RELATION_STALE_MS = 5 * 60e3;

/** Whether a relation loaded at `loadedAt` is old enough to load again when the disclosure opens. */
export const relationStale = (loadedAt: number | null, now: number): boolean => loadedAt == null || now - loadedAt > RELATION_STALE_MS;

/** 1 -> "1st", 2 -> "2nd", 3 -> "3rd", 11 -> "11th", 22 -> "22nd". */
export function ordinal(n: number): string {
  const r100 = n % 100;
  if (r100 >= 11 && r100 <= 13) return `${n}th`;
  return `${n}${({ 1: 'st', 2: 'nd', 3: 'rd' } as Record<number, string>)[n % 10] ?? 'th'}`;
}

/** 7.1 -> "7.1", 0.042 -> "0.042", 12.34 -> "12.3", 1234 -> "1,234". Three significant digits at most. */
export function num(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return '–';
  const a = Math.abs(n);
  const digits = a >= 100 ? 0 : a >= 10 ? 1 : a >= 1 ? 2 : a >= 0.1 ? 3 : 4;
  const [whole = '0', frac = ''] = n.toFixed(digits).split('.');
  const f = frac.replace(/0+$/, '');
  return `${whole.replace(/\B(?=(\d{3})+$)/g, ',')}${f ? `.${f}` : ''}`;
}

/** One decimal, "6.8"; a trailing ".0" stays off ("5"). */
const one = (n: number): string => n.toFixed(1).replace(/\.0$/, '');
const whole = (n: number): string => String(Math.round(n));
const plural = (n: number, a: string, b = `${a}s`): string => `${n} ${n === 1 ? a : b}`;
const dayCount = (d: number): string => plural(Math.max(1, Math.round(d)), 'day');
/** "$1.67", "$0.042". */
export const dollars = (x: number): string => `$${x >= 1 ? x.toFixed(2) : x.toFixed(3)}`;

// ---- the verdict ----------------------------------------------------------------------------

export const NO_VERDICT = 'No verdict yet: wy account next has not reported this account.';

export interface VerdictRule {
  text: string;
  blocking: boolean;
}

export interface VerdictModel {
  /** "Can take a new session: 3rd in line". */
  headline: string;
  eligible: boolean;
  /** "3rd in line: 2 other accounts get a new session before this one; they take turns." */
  place: string;
  /** "Protected" / "Aggressive" / "Not chosen". */
  kindName: string;
  /** The definition from MODE_WORDS. */
  kindWords: string;
  notChosen: boolean;
  rules: VerdictRule[];
  /** Why it is not taking sessions, in words ("" when it is). */
  why: string;
  /** "Frees up Thu 14:00" / "No known time when it frees up" ("" when eligible). */
  frees: string;
  /** "No account was free; the first frees up 11:30" or "". */
  noneFree: string;
  /** "as of 12 min ago on gpu-box". */
  age: string;
}

const WHY: Record<number, string> = {
  0: 'it got its most new sessions for one hour',
  1: 'its newest usage reading is too old to trust',
  2: 'it is protected and is over its pace or 5-hour limit for new sessions',
  3: 'it is at its limit',
};

/** "as of 12 min ago on gpu-box"; "older than an hour, from gpu-box" past 60 minutes; "" without a verdict. */
export function verdictAge(v: Verdict | null | undefined, now: number): string {
  if (!v) return '';
  const t = toMs(v.at);
  if (Number.isNaN(t)) return '';
  const on = v.machine_name || v.machine;
  if (now - t > 60 * 60e3) return `older than an hour${on ? `, from ${on}` : ''}`;
  const a = ago(v.at, now);
  return `as of ${a === 'just now' ? a : `${a} ago`}${on ? ` on ${on}` : ''}`;
}

function rulesOf(v: Verdict): VerdictRule[] {
  const rules: VerdictRule[] = [];
  const protectedKind = v.kind === 'protected';
  if (v.five_hour_pct != null) {
    const cap = v.five_hour_cap;
    rules.push({
      text: `5-hour ${whole(v.five_hour_pct)}%${cap != null ? ` (stops at ${whole(cap)}%)` : ''}`,
      blocking: cap != null && v.five_hour_pct >= cap,
    });
  }
  if (v.weekly_pct != null) {
    const cap = v.weekly_cap;
    const text = cap == null ? `Week ${whole(v.weekly_pct)}%`
      : protectedKind ? `Week ${whole(v.weekly_pct)}%, the even pace allows ${whole(cap)}% today`
        : `Week ${whole(v.weekly_pct)}% (stops at ${whole(cap)}%)`;
    rules.push({ text, blocking: cap != null && v.weekly_pct >= cap });
  }
  if (v.burst != null) {
    const cap = v.burst_cap;
    rules.push({
      text: `New sessions in the last hour: ${v.burst}${cap != null ? ` (at most ${cap})` : ''}`,
      blocking: cap != null && v.burst >= cap,
    });
  }
  return rules;
}

/** The verdict in words, or null when nothing has reported (show NO_VERDICT). */
export function verdictModel(v: Verdict | null | undefined, now: number, tz?: string): VerdictModel | null {
  if (!v) return null;
  const notChosen = v.mode_from === 'none' || (v.mode_from == null && !v.kind);
  const kind = notChosen ? 'none' : v.kind === 'protected' ? 'protected' : 'aggressive';
  const others = (v.rank ?? 1) - 1;
  let headline = 'Not taking new sessions now';
  let place = '';
  if (v.eligible) {
    headline = v.rank != null && v.rank > 1 ? `Can take a new session: ${ordinal(v.rank)} in line` : 'Can take the next new session';
    place = v.rank != null && v.rank > 1
      ? `${ordinal(v.rank)} in line: ${others} other ${others === 1 ? 'account gets' : 'accounts get'} a new session before this one; they take turns.`
      : 'It gets the next new session started with wy account next.';
    if (v.expiring) place += ' Its week resets within 2 days, so it goes first.';
  }
  const level = v.skip_level;
  const why = !v.eligible ? (level != null && WHY[level]) || (v.reason ?? '').trim() : '';
  const free = v.free_at ? clock(v.free_at, now, tz) : '';
  const first = v.next_free && v.next_free !== v.free_at ? clock(v.next_free, now, tz) : '';
  return {
    headline,
    eligible: v.eligible,
    place,
    kindName: MODE_NAMES[kind],
    kindWords: kind === 'none' ? MODE_WORDS.notChosen : MODE_WORDS[kind],
    notChosen,
    rules: rulesOf(v),
    why,
    frees: v.eligible ? '' : free ? `Frees up ${free}` : 'No known time when it frees up',
    noneFree: !v.eligible && first ? `No account was free; the first frees up ${first}` : '',
    age: verdictAge(v, now),
  };
}

// ---- tiles and the session line -------------------------------------------------------------

export interface Tiles {
  tiles: [string, string, string];
  /** "measured over 7 days", "partly measured (40%), over 2 days", "default (not measured yet: ...)". */
  label: string;
}

/** How a factor stands, for the label under the tiles. `days` falls back to `alt` when the factor has none. */
function stateWords(f: CalibrationFactor, alt = 0): string {
  const days = f.days > 0 ? f.days : alt;
  let s: string;
  if (f.weight >= 1) s = `measured over ${dayCount(days)}`;
  else if (f.weight > 0) s = `partly measured (${whole(f.weight * 100)}%), over ${dayCount(days)}`;
  else s = `default (not measured yet: needs 3 days and 20 readings, ${f.n} so far)`;
  return f.implausible ? `${s}; measured value looked wrong, not used` : s;
}

/** The three tiles of the 5-hour limit against the week, from the calibrated ratio; null without a calibration. */
export function tilesModel(c: Calibration | null | undefined): Tiles | null {
  const r = c?.ratio.value;
  if (!c || r == null || !(r > 0)) return null;
  const window = 100 / r;
  return {
    tiles: [
      `${one(r)}% of the 5-hour limit = 1% of the week`,
      `≈ ${one(r)} full 5-hour windows use up the week`,
      `One full 5-hour window ≈ ${window < 10 ? one(window) : whole(window)}% of the week`,
    ],
    label: stateWords(c.ratio, c.f.days),
  };
}

export const NOT_MEASURED = 'Not measured yet: the hub has no calibration for this account.';

const sessionState = (f: CalibrationFactor): string => {
  if (f.weight >= 1) return `measured from ${plural(f.n, 'session')}`;
  if (f.weight > 0) return `partly measured (${whole(f.weight * 100)}%), from ${plural(f.n, 'session')}`;
  return `default; measured after 20 sessions, ${f.n} so far`;
};

/** "Each new session is set aside 8% of the 5-hour limit until its real use shows up (...)". */
export function sessionLine(c: Calibration | null | undefined): string {
  if (!c) return NOT_MEASURED;
  return `Each new session is set aside ${num(c.lane_charge)}% of the 5-hour limit until its real use shows up (${sessionState(c.lane_price)})`;
}

// ---- charts ---------------------------------------------------------------------------------

/** Colours of the models, legible on the light and the dark background; by order of first appearance. */
export const MODEL_COLOURS = ['#3B82C4', '#D9822B', '#2E9E8F', '#8E6BD1', '#C9508C', '#7A8794'] as const;

/** A model's colour index across both charts: models of the days first, then the others. */
export function modelOrder(rel: Pick<UsageRelation, 'days' | 'models'>): string[] {
  const seen: string[] = [];
  for (const d of rel.days) if (d.model && !seen.includes(d.model)) seen.push(d.model);
  for (const m of rel.models) if (!seen.includes(m.model)) seen.push(m.model);
  return seen;
}

export const colourOfModel = (order: readonly string[], model: string | null | undefined): number => {
  const i = model ? order.indexOf(model) : -1;
  return i < 0 ? MODEL_COLOURS.length - 1 : i % MODEL_COLOURS.length;
};

/** 0 and two round steps that reach `max`: 8 -> [0, 5, 10]; 1.67 -> [0, 1, 2]; 0.044 -> [0, 0.025, 0.05]. */
export function yTicks(max: number): number[] {
  if (!(max > 0)) return [0, 1, 2];
  const raw = max / 2;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw * 0.999999) ?? 10 * mag;
  return [0, step, step * 2].map((x) => Number(x.toPrecision(6)));
}

const tickLabel = (v: number, unit: string): string => `${unit === '$' ? '$' : ''}${num(v)}${unit === '%' ? '%' : ''}`;

export interface ChartBar {
  day: string;
  value: number | null;
  /** Height in percent of the top tick; 0 for a gap. */
  pct: number;
  gap: boolean;
  /** Index into MODEL_COLOURS; -1 when the chart has one colour. */
  colour: number;
  /** The tap label and the accessible name of the day. */
  label: string;
}

export interface Chart {
  id: string;
  title: string;
  /** "Higher: ..." under the title. */
  higher: string;
  ticks: { value: number; label: string; pct: number }[];
  bars: ChartBar[];
  /** First, middle and last day. */
  axis: [string, string, string];
  /** Where the dashed reference line sits (percent of the height) and its label. */
  ref: { pct: number; label: string } | null;
  /** A model's colours and full names. */
  legend: { model: string; colour: number }[];
  hasGaps: boolean;
  muted: boolean;
  /** The hint "Tap a day to see its value." under this chart (the first only). */
  hint: boolean;
  /** One sentence for the accessibilityLabel. */
  summary: string;
}

export const TAP_HINT = 'Tap a day to see its value.';
export const GAP_MARK = 'no data';
export const RATIO_TITLE = '5-hour % per 1% of the week, each day';
export const RATIO_HIGHER = 'Higher: the week lasts longer compared with the 5-hour limit.';
export const NO_RATIO = 'Not enough readings in the last 2 weeks to show this.';
export const ALERT_NOTE = 'A change of more than 20% on most accounts raises an alert.';

const readings = (n: number): string => plural(n, 'reading');

function axisOf(days: readonly { day: string }[]): [string, string, string] {
  const at = (i: number): string => shortDay(days[i]?.day ?? '');
  return [at(0), at(Math.floor((days.length - 1) / 2)), at(days.length - 1)];
}

function summarise(title: string, bars: ChartBar[], unit: (v: number) => string): string {
  const have = bars.filter((b) => b.value != null);
  if (!have.length) return `${title}. No data.`;
  const top = have.reduce((a, b) => ((b.value as number) > (a.value as number) ? b : a));
  return `${title}. ${have.length} of ${bars.length} days have data; the highest is ${unit(top.value as number)} on ${shortDay(top.day)}.`;
}

/** The daily ratio chart, or null when no day has a ratio (show NO_RATIO). */
export function ratioChart(rel: Pick<UsageRelation, 'days' | 'models'>): Chart | null {
  const ds = rel.days;
  if (!ds.some((d) => d.ratio != null)) return null;
  const order = modelOrder(rel);
  const ticks = yTicks(Math.max(0, ...ds.map((d) => d.ratio ?? 0)));
  const top = ticks[2] as number;
  const bars: ChartBar[] = ds.map((d) => ({
    day: d.day,
    value: d.ratio,
    pct: d.ratio != null && d.ratio > 0 ? Math.max(2, Math.min(100, (d.ratio / top) * 100)) : 0,
    gap: d.ratio == null,
    colour: colourOfModel(order, d.model),
    label: d.ratio == null
      ? `${shortDay(d.day)}: ${GAP_MARK}`
      : [`${shortDay(d.day)}: ${one(d.ratio)}% of the 5-hour limit per 1% of the week`, d.model ? `mostly ${d.model}` : '', readings(d.pairs)].filter(Boolean).join(' · '),
  }));
  const used = ds.map((d) => d.model).filter((m): m is string => !!m);
  return {
    id: 'ratio',
    title: RATIO_TITLE,
    higher: RATIO_HIGHER,
    ticks: ticks.map((v) => ({ value: v, label: tickLabel(v, '%'), pct: (v / top) * 100 })),
    bars,
    axis: axisOf(ds),
    ref: null,
    legend: order.filter((m) => used.includes(m)).map((m) => ({ model: m, colour: colourOfModel(order, m) })),
    hasGaps: bars.some((b) => b.gap),
    muted: false,
    hint: false,
    summary: summarise(RATIO_TITLE, bars, (v) => `${one(v)}%`),
  };
}

/** The note under the capacity sentences when this week is not Claude-Code-heavy, else "". */
export function heavyNote(h: RelationHeavy | null | undefined): string {
  if (!h || h.heavy) return '';
  const tail = 'this is not a fair measure this week and does not count toward limit-change alerts';
  if (h.cc_share == null) return `No 5-hour rise this week: ${tail}.`;
  if (h.rise < 20) return `Only ${Math.round(h.rise)} points of 5-hour rise this week: ${tail}.`;
  return `${Math.round(h.cc_share * 100)}% of the 5-hour rise had Claude Code tokens: ${tail}.`;
}

export interface CapacityModel {
  model: string;
  /** The one sentence (or the one "not used" line). */
  text: string;
  /** Only when both weeks have a value. */
  chart: Chart | null;
}

const hasIndex = (m: RelationModel): boolean => m.this_week != null || m.days.some((d) => d.index != null);

/** "10% more now", "20% less now", "the same". */
function change(m: RelationModel): string {
  const ratio = m.change ?? (m.this_week != null && m.last_week ? m.this_week / m.last_week : null);
  if (ratio == null) return '';
  const p = Math.round((ratio - 1) * 100);
  return p === 0 ? 'the same' : `${Math.abs(p)}% ${p > 0 ? 'more' : 'less'} now`;
}

function capacityChart(m: RelationModel, order: readonly string[], muted: boolean): Chart | null {
  if (m.this_week == null || m.last_week == null) return null;
  const ticks = yTicks(Math.max(m.last_week, ...m.days.map((d) => d.index ?? 0)));
  const top = ticks[2] as number;
  const title = `${m.model}: API dollars per 1% of the 5-hour limit, each day`;
  const bars: ChartBar[] = m.days.map((d) => ({
    day: d.day,
    value: d.index,
    pct: d.index != null && d.index > 0 ? Math.max(2, Math.min(100, (d.index / top) * 100)) : 0,
    gap: d.index == null,
    colour: colourOfModel(order, m.model),
    label: d.index == null
      ? `${shortDay(d.day)}: ${GAP_MARK}${d.d5 > 0 ? ` (under 5 points of 5-hour use)` : ''}`
      : `${shortDay(d.day)}: ${dollars(d.index)} of ${m.model} work per 1% of the 5-hour limit`,
  }));
  return {
    id: `capacity-${m.model}`,
    title,
    higher: 'Higher: the same 5-hour limit buys more work.',
    ticks: ticks.map((v) => ({ value: v, label: tickLabel(v, '$'), pct: (v / top) * 100 })),
    bars,
    axis: axisOf(m.days),
    ref: { pct: Math.min(100, (m.last_week / top) * 100), label: `Last week: ${dollars(m.last_week)}` },
    legend: [],
    hasGaps: bars.some((b) => b.gap),
    muted,
    hint: false,
    summary: `${summarise(title, bars, dollars)} Last week's level is ${dollars(m.last_week)}.`,
  };
}

export interface Capacity {
  models: CapacityModel[];
  /** The alert sentence, once, when any model has a sentence about this week. */
  alertNote: string;
  /** The not-Claude-Code-heavy note, or "". */
  note: string;
}

/** The capacity sentence per model (most dollars first) and a chart only when both weeks have a value. */
export function capacityModel(rel: Pick<UsageRelation, 'days' | 'models' | 'heavy'>): Capacity {
  const order = modelOrder(rel);
  const note = heavyNote(rel.heavy.this_week);
  const spent = (m: RelationModel): number => m.days.reduce((s, d) => s + (d.usd || 0), 0);
  const sorted = rel.models.map((m, i) => ({ m, i })).sort((a, b) => spent(b.m) - spent(a.m) || a.i - b.i).map((x) => x.m);
  let sentences = 0;
  const models = sorted.map((m): CapacityModel => {
    if (!hasIndex(m)) return { model: m.model, text: `${m.model}: not used in the last 2 weeks.`, chart: null };
    if (m.this_week == null) return { model: m.model, text: `${m.model}: not enough data this week yet.`, chart: null };
    sentences += 1;
    const last = m.last_week != null ? `Last week: ${dollars(m.last_week)}${change(m) ? ` (${change(m)})` : ''}.` : 'Last week: not enough data yet.';
    return {
      model: m.model,
      text: `This week 1% of the 5-hour limit bought about ${dollars(m.this_week)} of ${m.model} work at API prices. ${last}`,
      chart: capacityChart(m, order, !!note),
    };
  });
  return { models, alertNote: sentences ? ALERT_NOTE : '', note };
}

// ---- how this works -------------------------------------------------------------------------

export interface HowRow {
  title: string;
  text: string;
}

const RESTART_REASONS: Record<string, string> = {
  seat_change: 'the account changed',
  under_read: 'the meter read low',
  over_read: 'the meter read high',
};

/** "Restarted Thu 14:00: the account changed", or "" when there was no restart. */
export function calibrationRestart(c: Calibration | null | undefined, now: number, tz?: string): string {
  if (!c?.reset) return '';
  const reason = RESTART_REASONS[c.reset.reason] ?? c.reset.reason.replace(/_/g, ' ');
  return `Restarted ${clock(c.reset.at, now, tz)}: ${reason}`;
}

const basis = (f: CalibrationFactor): string => {
  if (f.weight >= 1) return `measured from ${readings(f.n)} over ${dayCount(f.days)}`;
  if (f.weight > 0) return `partly measured (${whole(f.weight * 100)}%), from ${readings(f.n)} over ${dayCount(f.days)}`;
  return `default value, not measured yet (${readings(f.n)} so far)`;
};

/** The closed "How this works" rows. */
export function howRows(c: Calibration | null | undefined, now: number, tz?: string): HowRow[] {
  const rows: HowRow[] = [];
  if (c) {
    rows.push(
      { title: 'Cost of Claude Code use', text: `How many % of the 5-hour limit a million tokens of Claude Code use cost: ${num(c.f.value)}, ${basis(c.f)}.` },
      { title: 'The 5-hour limit and the week', text: `How many % of the 5-hour limit use up 1% of the week: ${num(c.ratio.value)}, ${basis(c.ratio)}.` },
      { title: 'Cost of a new session', text: `What the picker sets aside for each new session: ${num(c.lane_charge)}% of the 5-hour limit, ${sessionState(c.lane_price)}.` },
    );
  }
  rows.push({
    title: 'Where the numbers come from',
    text: 'The hub works them out from every machine\'s readings. It starts from safe defaults and moves to the measured values after about 3 days and 20 readings.',
  });
  const restart = calibrationRestart(c, now, tz);
  if (restart) rows.push({ title: 'Last restart', text: restart });
  rows.push({
    title: 'The capacity index',
    text: 'For each model, the API price of the tokens it used for each 1% of the 5-hour limit. If it changes by more than 20% on most accounts, the limit has probably changed.',
  });
  return rows;
}

// ---- everything for the screen --------------------------------------------------------------

export interface UsageModel {
  verdict: VerdictModel | null;
  tiles: Tiles | null;
  session: string;
  /** The ratio chart, or the one line saying why there is none. */
  ratio: Chart | null;
  noRatio: string;
  capacity: Capacity;
  how: HowRow[];
}

/** The whole section in order; the tap hint is on the first chart that shows. */
export function usageModel(rel: UsageRelation, now: number): UsageModel {
  const ratio = ratioChart(rel);
  const capacity = capacityModel(rel);
  const first = ratio ?? capacity.models.find((m) => m.chart)?.chart ?? null;
  if (first) first.hint = true;
  return {
    verdict: verdictModel(rel.verdict, now, rel.tz),
    tiles: tilesModel(rel.calibration),
    session: sessionLine(rel.calibration),
    ratio,
    noRatio: ratio ? '' : NO_RATIO,
    capacity,
    how: howRows(rel.calibration, now, rel.tz),
  };
}
