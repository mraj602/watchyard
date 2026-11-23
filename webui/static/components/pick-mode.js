// How `wy account next` uses an account (docs/spec/pwa.md 3.0a, setup.md 8.5 Kind). The one place
// for the words: every mention of protected or aggressive on the board comes from here (the
// same wording as internal/picker ModeWords and the phone's pickMode.ts). No imports, so node
// can test it.

export const MODE_WORDS = {
  protected: {
    name: 'Protected',
    definition: 'Kept for you. New sessions only when no aggressive account has room, this week is behind an even pace and the 5-hour window is under 45%.',
  },
  aggressive: {
    name: 'Aggressive',
    definition: 'Use it up. Sessions go here until 98% of the week or 5 hours.',
  },
  none: {
    name: 'Not chosen',
    definition: 'Not chosen yet; treated as aggressive.',
  },
};

// The options of the owner's choice, in the order shown; `value` is what the hub stores.
export const MODE_OPTIONS = [
  { key: 'protected', value: 'protected' },
  { key: 'aggressive', value: 'aggressive' },
  { key: 'none', value: null },
];

export const MODE_QUESTION = 'How should the picker use this account?';

// A stored mode (protected, aggressive, null or anything else) -> its key in MODE_WORDS.
export const modeKey = (mode) => (mode === 'protected' || mode === 'aggressive' ? mode : 'none');

// What the picker does with it: not chosen counts as aggressive.
export const effectiveKind = (mode) => (mode === 'protected' ? 'protected' : 'aggressive');

export const modeWords = (mode) => MODE_WORDS[modeKey(mode)];

// The line for a verdict from a machine whose own setting decides (mode_from "local") and
// differs from the hub's choice; '' otherwise.
export function localOverrideLine(verdict, seatMode) {
  if (!verdict || verdict.mode_from !== 'local' || !verdict.kind) return '';
  if (verdict.kind === effectiveKind(seatMode)) return '';
  const on = verdict.machine_name || verdict.machine || 'a machine';
  return `On ${on} a setting on that machine makes it ${verdict.kind} (set on this machine); it wins there over this choice.`;
}
