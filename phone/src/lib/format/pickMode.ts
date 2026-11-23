// How the picker uses an account (docs/spec/setup.md 8.5 Kind, pwa.md 3.0a). The one place for
// the words: the board's components/pick-mode.js and the picker's ModeWords say the same.
import type { PickMode, Verdict } from '../../contract/api';

export const MODE_WORDS = {
  protected: 'Kept for you. New sessions only when no aggressive account has room, this week is behind an even pace and the 5-hour window is under 45%.',
  aggressive: 'Use it up. Sessions go here until 98% of the week or 5 hours.',
  notChosen: 'Not chosen yet; treated as aggressive.',
  question: 'How should the picker use this account?',
} as const;

/** What an account's mode is: null (or anything unknown) is not chosen. */
export type ModeChoice = PickMode | null;

export const modeChoice = (m: unknown): ModeChoice => (m === 'protected' || m === 'aggressive' ? m : null);

export const MODE_NAMES: Record<'protected' | 'aggressive' | 'none', string> = { protected: 'Protected', aggressive: 'Aggressive', none: 'Not chosen' };

export interface ModeOption {
  value: ModeChoice;
  name: string;
  words: string;
}

/** The three options in order, each with its definition. */
export const MODE_OPTIONS: readonly ModeOption[] = [
  { value: 'protected', name: MODE_NAMES.protected, words: MODE_WORDS.protected },
  { value: 'aggressive', name: MODE_NAMES.aggressive, words: MODE_WORDS.aggressive },
  { value: null, name: MODE_NAMES.none, words: MODE_WORDS.notChosen },
];

export const modeOption = (m: unknown): ModeOption => MODE_OPTIONS.find((o) => o.value === modeChoice(m)) as ModeOption;

/** Where the verdict's machine got its kind from: a setting on that machine wins there over the hub's choice. */
export function localOverrideLine(v: Verdict | null | undefined, seatMode: unknown): string {
  if (!v || v.mode_from !== 'local' || !v.kind) return '';
  if (v.kind === (modeChoice(seatMode) ?? 'aggressive')) return '';
  const on = v.machine_name || v.machine;
  return `On ${on} a setting on that machine makes it ${v.kind} (set on this machine); it wins there over this choice.`;
}
