// How the picker uses an account (docs/spec/setup.md 8.5 Kind, pwa.md 3.0a). The one place for
// the words: the board's components/pick-mode.js and the picker's ModeWords say the same.
import type { PickMode, SeatPatch, Verdict } from '../../contract/api';

export const MODE_WORDS = {
  protected: 'Kept for you. Used last: new sessions go here only when no aggressive account can take them, or for room that would expire unused anyway.',
  aggressive: 'Use it up. Sessions go here until 98% of the week or 5 hours.',
  off: 'Never picked, nothing moved in; sessions already there stay, and their usage is still shown.',
  drain: 'Use it up now: new sessions go here first and other accounts\' sessions are pulled in, until the time you choose. Then it goes back to its mode, or earlier at 97% of the week.',
  notChosen: 'Not chosen yet; treated as aggressive.',
  question: 'How should the picker use this account?',
} as const;

/** What an account's mode is: null (or anything unknown) is not chosen. */
export type ModeChoice = PickMode | null;

export const modeChoice = (m: unknown): ModeChoice => (m === 'protected' || m === 'aggressive' || m === 'off' ? m : null);

export const MODE_NAMES: Record<'protected' | 'aggressive' | 'off' | 'drain' | 'none', string> = {
  protected: 'Protected',
  aggressive: 'Aggressive',
  off: 'Off',
  drain: 'Drain',
  none: 'Not chosen',
};

/** What a picker row stands for: a mode, Drain (a time-boxed aggressive), or not chosen. */
export type ModeValue = ModeChoice | 'drain';

export interface ModeOption {
  value: ModeValue;
  name: string;
  words: string;
}

/** The options in order, each with its definition. */
export const MODE_OPTIONS: readonly ModeOption[] = [
  { value: 'aggressive', name: MODE_NAMES.aggressive, words: MODE_WORDS.aggressive },
  { value: 'protected', name: MODE_NAMES.protected, words: MODE_WORDS.protected },
  { value: 'drain', name: MODE_NAMES.drain, words: MODE_WORDS.drain },
  { value: 'off', name: MODE_NAMES.off, words: MODE_WORDS.off },
  { value: null, name: MODE_NAMES.none, words: MODE_WORDS.notChosen },
];

export const modeOption = (m: unknown): ModeOption => MODE_OPTIONS.find((o) => o.value === modeChoice(m)) as ModeOption;

/** The Seat patch a pick sends (drain goes through drainPatch): drain_until is cleared except for Off, which the hub clears itself. */
export function modePatch(value: ModeChoice): SeatPatch {
  return value === 'off' ? { mode: 'off' } : { mode: value, drain_until: null };
}

/** A drain's patch: just the end; an account that is Off also leaves Off (a drain with Off is a 400). */
export function drainPatch(untilIso: string, currentMode: unknown): SeatPatch {
  return modeChoice(currentMode) === 'off' ? { mode: null, drain_until: untilIso } : { drain_until: untilIso };
}

/** The drain end, when it is still ahead of now (the hub sends null otherwise, but a stale card may not). */
export function drainEnd(until: unknown, now: number): number | null {
  if (typeof until !== 'string') return null;
  const t = Date.parse(until);
  return Number.isFinite(t) && t > now ? t : null;
}

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const two = (n: number) => String(n).padStart(2, '0');

/** "Draining until 23:59", with the day name when it is not today (local time); '' when no drain is running. */
export function drainLine(until: unknown, now: number): string {
  const end = drainEnd(until, now);
  if (end == null) return '';
  const d = new Date(end);
  const today = new Date(now);
  const same = d.getFullYear() === today.getFullYear() && d.getMonth() === today.getMonth() && d.getDate() === today.getDate();
  return `Draining until ${same ? '' : `${DAYS[d.getDay()]} `}${two(d.getHours())}:${two(d.getMinutes())}`;
}

export interface DrainChoice {
  id: 'today' | '2h' | '4h' | '8h';
  label: string;
  /** RFC 3339 (UTC). */
  until: string;
}

/** The end times the picker offers: end of today (23:59 local; left out in the last 15 minutes of the day), 2, 4 and 8 hours. */
export function drainChoices(now: number): DrainChoice[] {
  const eod = new Date(now);
  eod.setHours(23, 59, 0, 0);
  const out: DrainChoice[] = [];
  if (eod.getTime() - now > 15 * 60_000) out.push({ id: 'today', label: 'End of today', until: eod.toISOString() });
  for (const h of [2, 4, 8] as const) out.push({ id: `${h}h`, label: `In ${h} h`, until: new Date(now + h * 3_600_000).toISOString() });
  return out;
}

/** Where the verdict's machine got its kind from: a setting on that machine wins there over the hub's choice. */
export function localOverrideLine(v: Verdict | null | undefined, seatMode: unknown): string {
  if (!v || v.mode_from !== 'local' || !v.kind) return '';
  if (v.kind === (modeChoice(seatMode) ?? 'aggressive')) return '';
  const on = v.machine_name || v.machine;
  return `On ${on} a setting on that machine makes it ${v.kind} (set on this machine); it wins there over this choice.`;
}
