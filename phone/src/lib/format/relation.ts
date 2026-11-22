// The sentences and bar sizes of an account's "Usage and the picker" (docs/spec/pwa.md 3.0a,
// phone.md P17.8; the same wording as the board). Pure: the screen only draws what these return.
import type { Calibration, CalibrationFactor, RelationDay, RelationHeavy, RelationModel, Verdict } from '../../contract/api';
import { shortDay } from './analytics';
import { ago, clock, toMs } from './core';

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

/** The skip level of a verdict in words (record.md 4.11): 0 burst cap, 1 stale, 2 protected rule, 3 limit. */
export function skipWords(level: number | null | undefined): string {
  switch (level) {
    case 0: return 'burst cap';
    case 1: return 'stale';
    case 2: return 'protected rule';
    case 3: return 'limit';
    default: return '';
  }
}

export const NO_VERDICT = 'No verdict yet: `wy account next` has not reported this account.';

/** "Eligible · next in line · aggressive", "Eligible · 3rd in line", "Not picked: <reason> (<level>) · frees up Thu 14:00". */
export function verdictLine(v: Verdict | null | undefined, now: number, tz?: string): string {
  if (!v) return NO_VERDICT;
  if (v.eligible) {
    const parts = ['Eligible'];
    if (v.rank === 1) parts.push('next in line');
    else if (v.rank != null && v.rank > 1) parts.push(`${ordinal(v.rank)} in line`);
    if (v.expiring) parts.push('weekly room expiring');
    if (v.kind) parts.push(v.kind);
    return parts.join(' · ');
  }
  const level = skipWords(v.skip_level);
  const why = (v.reason ?? '').trim();
  let text = 'Not picked';
  if (why && level) text += `: ${why} (${level})`;
  else if (why || level) text += `: ${why || level}`;
  const free = v.free_at ? clock(v.free_at, now, tz) : '';
  if (free) text += ` · frees up ${free}`;
  // next_free: the run had no eligible account; when the first one of all frees up.
  const first = v.next_free && v.next_free !== v.free_at ? clock(v.next_free, now, tz) : '';
  if (first) text += ` · no account was free, the first frees up ${first}`;
  return text;
}

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

/** 7.1 -> "7.1", 0.042 -> "0.042", 12.34 -> "12.3", 1234 -> "1,234". Three significant digits at most. */
export function num(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return '–';
  const a = Math.abs(n);
  const digits = a >= 100 ? 0 : a >= 10 ? 1 : a >= 1 ? 2 : a >= 0.1 ? 3 : 4;
  const [whole = '0', frac = ''] = n.toFixed(digits).split('.');
  const f = frac.replace(/0+$/, '');
  return `${whole.replace(/\B(?=(\d{3})+$)/g, ',')}${f ? `.${f}` : ''}`;
}

/** "measured", "blending, 40% measured" or "fallback", by the factor's weight. */
export function factorSource(f: CalibrationFactor): string {
  if (f.weight >= 1) return 'measured';
  if (f.weight > 0) return `blending, ${Math.round(f.weight * 100)}% measured`;
  return 'fallback';
}

export interface CalibrationRow {
  id: 'f' | 'ratio' | 'lane';
  label: string;
  /** The value in use, with its unit. */
  value: string;
  /** "measured", "blending, 40% measured", "fallback". */
  source: string;
  /** "n 12 · 9 days". */
  basis: string;
  implausible: boolean;
  /** The row as one line. */
  text: string;
}

const dayCount = (d: number): string => `${Math.round(d)} ${Math.round(d) === 1 ? 'day' : 'days'}`;

function row(id: CalibrationRow['id'], label: string, value: string, f: CalibrationFactor): CalibrationRow {
  const source = factorSource(f);
  const basis = `n ${f.n} · ${dayCount(f.days)}`;
  const text = [`${label} ${value}`, source, basis, f.implausible ? 'implausible measurement, not used' : ''].filter(Boolean).join(' · ');
  return { id, label, value, source, basis, implausible: f.implausible, text };
}

/** The f, ρ ("Ratio 5 h : 7 d", as the board) and lane charge rows; empty without a calibration. */
export function calibrationRows(c: Calibration | null | undefined): CalibrationRow[] {
  if (!c) return [];
  return [
    row('f', 'Meter factor f', `${num(c.f.value)} points per million units`, c.f),
    row('ratio', 'Ratio 5 h : 7 d', `${num(c.ratio.value)} 5 h points per 7 d point`, c.ratio),
    row('lane', 'Lane charge c', `${num(c.lane_charge)} points (lane price ${num(c.lane_price.value)})`, c.lane_price),
  ];
}

export const NOT_CALIBRATED = 'Not calibrated yet.';

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

/** A model's initial for a bar: Opus -> O, Sonnet -> S, Haiku -> H, Fable -> F, Mythos -> M, anything else its first character. */
export const modelInitial = (label: string | null | undefined): string => (label ?? '').trim().charAt(0).toUpperCase();

/** "O Opus 4.6 · S Sonnet 4.5" for the models of these days, in order of first appearance. */
export function legend(ds: readonly Pick<RelationDay, 'model'>[]): string {
  const seen: string[] = [];
  for (const d of ds) if (d.model && !seen.includes(d.model)) seen.push(d.model);
  return seen.map((m) => `${modelInitial(m)} ${m}`).join(' · ');
}

export interface RatioBar {
  day: string;
  value: number | null;
  /** Height in percent of the tallest bar; 0 for a gap. */
  pct: number;
  initial: string;
  /** The tap label: "12 Nov: 7.1 · Opus 4.6 · 30 pairs". */
  label: string;
}

const pairs = (n: number): string => `${n} ${n === 1 ? 'pair' : 'pairs'}`;

/** The daily 5 h per 7 d bars: a null ratio is a gap. */
export function ratioBars(ds: readonly RelationDay[]): RatioBar[] {
  const max = Math.max(0, ...ds.map((d) => d.ratio ?? 0));
  return ds.map((d) => ({
    day: d.day,
    value: d.ratio,
    pct: d.ratio != null && max > 0 && d.ratio > 0 ? Math.max(2, Math.round((d.ratio / max) * 100)) : 0,
    initial: modelInitial(d.model),
    label: d.ratio == null
      ? `${shortDay(d.day)}: ${d.pairs > 0 ? `under 2 weekly points · ${pairs(d.pairs)}` : 'no ratio'}`
      : [`${shortDay(d.day)}: ${num(d.ratio)}`, d.model, pairs(d.pairs)].filter(Boolean).join(' · '),
  }));
}

/** "Opus 4.6 · $0.042 per 1% this week · 1.05× last week", or "Opus 4.6 · not enough days". */
export function capacityLine(m: RelationModel): string {
  if (m.this_week == null) return `${m.model} · not enough days`;
  const parts = [m.model, `${perPoint(m.this_week)} per 1% this week`];
  parts.push(m.change != null ? `${num(m.change)}× last week` : 'last week: not enough days');
  return parts.join(' · ');
}

export interface CapacityBar {
  day: string;
  value: number | null;
  pct: number;
  label: string;
}

const perPoint = (x: number): string => `$${x >= 1 ? x.toFixed(2) : x.toFixed(3)}`;

/** A capacity bar's tap label, as the board: "19 Nov: $0.044 per 1% · 1.1× last week · 12 points". */
function capacityLabel(d: RelationModel['days'][number]): string {
  if (d.index == null) return `${shortDay(d.day)}: ${d.d5 > 0 ? `under 5 points (${num(d.d5)})` : 'no index'}`;
  const parts = [`${shortDay(d.day)}: ${perPoint(d.index)} per 1%`];
  if (d.normalised != null) parts.push(`${num(d.normalised)}× last week`);
  parts.push(`${num(d.d5)} points`);
  return parts.join(' · ');
}

/** Normalised bars on a scale that always reaches 1.0; `ref` is where the 1.0 line sits, in percent of the height. */
export function capacityBars(m: RelationModel): { bars: CapacityBar[]; ref: number } {
  const scale = Math.max(1, ...m.days.map((d) => d.normalised ?? 0));
  return {
    ref: (1 / scale) * 100,
    bars: m.days.map((d) => ({
      day: d.day,
      value: d.normalised,
      pct: d.normalised != null && d.normalised > 0 ? Math.max(2, Math.round((d.normalised / scale) * 100)) : 0,
      label: capacityLabel(d),
    })),
  };
}

/** The note under the capacity bars when this week is not Claude-Code-heavy, else "". */
export function heavyNote(h: RelationHeavy | null | undefined): string {
  if (!h || h.heavy) return '';
  const tail = 'this index is not a fair measure and does not count toward limit-change alerts';
  if (h.cc_share == null) return `No 5-hour rise this week: ${tail}.`;
  if (h.rise < 20) return `Only ${Math.round(h.rise)} points of 5-hour rise this week: ${tail}.`;
  return `${Math.round(h.cc_share * 100)}% of the 5-hour rise had Claude Code tokens: ${tail}.`;
}
