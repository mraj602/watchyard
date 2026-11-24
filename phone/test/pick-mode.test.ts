// The picker's mode wording (src/lib/format/pickMode.ts; docs/spec/pwa.md 3.0a). Must match the board's pick-mode.js.
import type { Verdict } from '../src/contract/api';
import { MODE_OPTIONS, MODE_WORDS, localOverrideLine, modeChoice, modeOption } from '../src/lib/format/pickMode';

const verdict = (over: Partial<Verdict> = {}): Verdict => ({ at: '2026-11-12T11:48:00Z', machine: 'm1', machine_name: 'gpu-box', strategy: 'lanes', eligible: true, ...over });

describe('MODE_WORDS', () => {
  it('is the exact wording of the spec', () => {
    expect(MODE_WORDS.protected).toBe('Kept for you. Used last: new sessions go here only when no aggressive account can take them, or for room that would expire unused anyway.');
    expect(MODE_WORDS.aggressive).toBe('Use it up. Sessions go here until 98% of the week or 5 hours.');
    expect(MODE_WORDS.notChosen).toBe('Not chosen yet; treated as aggressive.');
    expect(MODE_WORDS.question).toBe('How should the picker use this account?');
  });
  it('has three options, each with its definition', () => {
    expect(MODE_OPTIONS.map((o) => [o.name, o.value])).toEqual([['Protected', 'protected'], ['Aggressive', 'aggressive'], ['Not chosen', null]]);
    expect(modeOption('protected').words).toBe(MODE_WORDS.protected);
    expect(modeOption(undefined).words).toBe(MODE_WORDS.notChosen);
    expect(modeChoice('normal')).toBeNull();
  });
});

describe('localOverrideLine', () => {
  it('names a local setting that differs from the hub choice (none counts as aggressive)', () => {
    const v = verdict({ mode_from: 'local', kind: 'protected' });
    expect(localOverrideLine(v, 'aggressive')).toBe('On gpu-box a setting on that machine makes it protected (set on this machine); it wins there over this choice.');
    expect(localOverrideLine(v, null)).toContain('makes it protected');
    expect(localOverrideLine(verdict({ mode_from: 'local', kind: 'aggressive' }), null)).toBe('');
  });
  it('says nothing when the kind agrees, the setting is not local, or there is no verdict', () => {
    expect(localOverrideLine(verdict({ mode_from: 'local', kind: 'protected' }), 'protected')).toBe('');
    expect(localOverrideLine(verdict({ mode_from: 'hub', kind: 'protected' }), 'aggressive')).toBe('');
    expect(localOverrideLine(null, 'protected')).toBe('');
  });
});
