// Limit windows: labels, order, pace and "most room" (webui components/windows.js, views/accounts.js).
import type { Seat, Window } from '../../contract/api';
import { clock, humanKey, until } from './core';

const LABELS: Record<string, string> = {
  five_hour: '5-hour',
  seven_day: '7-day',
  seven_day_opus: '7-day · Opus',
  seven_day_sonnet: '7-day · Sonnet',
  iguana_necktie: 'Cloud session credits',
};

export function windowLabel(w: Pick<Window, 'key' | 'label'>): string {
  if (LABELS[w.key]) return LABELS[w.key]!;
  if (w.label) return w.label;
  const m = /^seven_day_(.+)$/.exec(w.key);
  return m ? `7-day · ${humanKey(m[1])}` : humanKey(w.key);
}

/** five_hour and seven_day first, then the rest in the order the hub sent them. */
export function sortWindows(ws: Window[]): Window[] {
  const rank = (w: Window) => (w.key === 'five_hour' ? 0 : w.key === 'seven_day' ? 1 : 2);
  return [...ws].sort((a, b) => rank(a) - rank(b));
}

export type PaceClass = 'ok' | 'warn' | 'bad';

const BAND: Record<string, { cls: PaceClass; text: string }> = {
  comfortable: { cls: 'ok', text: 'Comfortable' },
  on_track: { cls: 'ok', text: 'On pace' },
  fast: { cls: 'warn', text: 'Fast' },
  runaway: { cls: 'bad', text: 'Runaway' },
};

export interface PaceInfo {
  cls: PaceClass;
  text: string;
  band: string;
}

/** Which rate a projection used; '' from a hub that does not say. */
export function paceBasis(basis: string | undefined, key: string | undefined): string {
  if (basis === 'recent') return "at the last hour's pace";
  if (basis === 'average') return key === 'seven_day' ? "at this week's average pace" : "at this window's average pace";
  return '';
}

/** null when the window has no projection. */
export function paceInfo(w: Window, now: number, tz?: string): PaceInfo | null {
  const p = w.pace;
  if (!p || !p.band) return null;
  const b = BAND[p.band] ?? { cls: 'ok' as const, text: humanKey(p.band) };
  const proj = p.projected_pct != null ? Math.round(p.projected_pct) : null;
  const basis = paceBasis(p.basis, w.key);
  let text: string;
  if (p.band === 'runaway' && p.full_at) text = `At ${basis ? basis.replace(/^at /, '') : 'this pace'}: 100% ${clock(p.full_at, now, tz)}`;
  else if (proj != null) text = `${b.text}: ~${proj}% at reset${basis ? ` ${basis}` : ''}`;
  else text = b.text;
  return { cls: b.cls, text, band: p.band };
}

/** The worst pace among a seat's windows. */
export function worstPace(ws: Window[], now: number, tz?: string): { p: PaceInfo; w: Window } | null {
  const order = ['comfortable', 'on_track', 'fast', 'runaway'];
  let best: { p: PaceInfo; w: Window } | null = null;
  for (const w of ws) {
    const p = paceInfo(w, now, tz);
    if (p && (!best || order.indexOf(p.band) > order.indexOf(best.p.band))) best = { p, w };
  }
  return best;
}

export type WindowPhase = 'current' | 'reset' | 'idle';

/**
 * How a window reads now (api.yaml Window.state): idle (claude.ai says it has not started: 0 %, no
 * reset time), reset (its resets_at has passed; nothing used since) or current.
 */
export function windowPhase(w: Pick<Window, 'used_pct' | 'resets_at' | 'state'>, now: number): WindowPhase {
  if (w.state === 'idle' || (!w.resets_at && w.used_pct === 0)) return 'idle';
  const reset = Date.parse(w.resets_at || '');
  return !Number.isNaN(reset) && reset <= now ? 'reset' : 'current';
}

/** The use shown for a window: 0 when idle or reset, else its used_pct (null without a reading). */
export function shownUse(w: Window | undefined, now: number): number | null {
  if (!w || w.used_pct == null) return null;
  return windowPhase(w, now) === 'current' ? w.used_pct : 0;
}

/**
 * The muted reset words of a window: "not started" (idle), "reset at Sat 10:10" (its reset has
 * passed), "resets 15:48", or '' with no reset time. `long` uses "resets in 2 h (15:48)".
 */
export function resetWords(w: Window, now: number, opts: { long?: boolean; tz?: string } = {}): string {
  const phase = windowPhase(w, now);
  if (phase === 'idle') return 'not started';
  if (!w.resets_at) return '';
  const at = clock(w.resets_at, now, opts.tz);
  if (phase === 'reset') return `reset at ${at}`;
  return opts.long ? `resets ${until(w.resets_at, now)} (${at})` : `resets ${at}`;
}

const reading = (w: Window | undefined): w is Window => !!w && w.used_pct != null;

/**
 * The use of one window that counts for "room": 0 once its `resets_at` has passed or while it is
 * idle (not started), or when the seat has no reading for it but has one for the other window;
 * else its used_pct (a reading older than 6 hours still counts until its reset). null when the
 * seat has no reading at all.
 */
export function effectiveUse(w: Window | undefined, other: Window | undefined, now: number): number | null {
  if (!reading(w)) return reading(other) ? 0 : null;
  if (windowPhase(w, now) === 'idle') return 0;
  const reset = Date.parse(w.resets_at || '');
  if (!Number.isNaN(reset) && reset <= now) return 0;
  return w.used_pct as number;
}

export interface SeatUse {
  five: number;
  seven: number;
  fiveW?: Window;
  sevenW?: Window;
}

/** The effective 5-hour and 7-day use of a seat (see effectiveUse); null with no reading at all. */
export function seatUse(s: Pick<Seat, 'windows'>, now: number): SeatUse | null {
  const five = (s.windows ?? []).find((w) => w.key === 'five_hour');
  const seven = (s.windows ?? []).find((w) => w.key === 'seven_day');
  const f = effectiveUse(five, seven, now);
  const v = effectiveUse(seven, five, now);
  if (f == null || v == null) return null;
  return { five: f, seven: v, ...(five ? { fiveW: five } : {}), ...(seven ? { sevenW: seven } : {}) };
}

/**
 * Who has the most room right now (the board's and the hub's rule): among visible accounts with
 * any reading, score = the larger of the effective 5-hour and 7-day use; lowest wins, ties go to
 * the lower 7-day, then the lower 5-hour use, then list order; only when the score is under 80.
 */
export function mostRoom(seats: Seat[], now: number): { seat: Seat; five: number; seven: number } | null {
  let best: { seat: Seat; five: number; seven: number } | null = null;
  const score = (x: { five: number; seven: number }) => Math.max(x.five, x.seven);
  for (const s of seats.filter((x) => !x.hidden)) {
    const u = seatUse(s, now);
    if (!u) continue;
    const c = { seat: s, five: u.five, seven: u.seven };
    if (!best || score(c) < score(best) || (score(c) === score(best) && (c.seven < best.seven || (c.seven === best.seven && c.five < best.five)))) best = c;
  }
  return best && score(best) < 80 ? best : null;
}

/**
 * The window the Accounts row shows: the tighter of 5 h and 7 d (a tie shows 7 d, or 5 h when the
 * seat has no 7 d window, as an idle account often has not); null without a reading.
 */
export function tighterWindow(s: Pick<Seat, 'windows'>, now: number): { key: 'five_hour' | 'seven_day'; short: '5 h' | '7 d'; used: number; w?: Window } | null {
  const u = seatUse(s, now);
  if (!u) return null;
  return u.five > u.seven || (u.five === u.seven && !u.sevenW && !!u.fiveW)
    ? { key: 'five_hour', short: '5 h', used: u.five, ...(u.fiveW ? { w: u.fiveW } : {}) }
    : { key: 'seven_day', short: '7 d', used: u.seven, ...(u.sevenW ? { w: u.sevenW } : {}) };
}
