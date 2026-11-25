// The picker's mode wording (src/lib/format/pickMode.ts; docs/spec/pwa.md 3.0a). Must match the board's pick-mode.js.
import type { Verdict } from '../src/contract/api';
import { MODE_OPTIONS, MODE_WORDS, drainChoices, drainEnd, drainLine, drainPatch, localOverrideLine, modeChoice, modeOption, modePatch } from '../src/lib/format/pickMode';

const verdict = (over: Partial<Verdict> = {}): Verdict => ({ at: '2026-11-12T11:48:00Z', machine: 'm1', machine_name: 'gpu-box', strategy: 'lanes', eligible: true, ...over });

describe('MODE_WORDS', () => {
  it('is the exact wording of the spec', () => {
    expect(MODE_WORDS.protected).toBe('Kept for you. Used last: new sessions go here only when no aggressive account can take them, or for room that would expire unused anyway.');
    expect(MODE_WORDS.aggressive).toBe('Use it up. Sessions go here until 98% of the week or 5 hours.');
    expect(MODE_WORDS.notChosen).toBe('Not chosen yet; treated as aggressive.');
    expect(MODE_WORDS.question).toBe('How should the picker use this account?');
  });
  it('has the options in order, each with its definition', () => {
    expect(MODE_OPTIONS.map((o) => [o.name, o.value])).toEqual([['Aggressive', 'aggressive'], ['Protected', 'protected'], ['Drain', 'drain'], ['Off', 'off'], ['Not chosen', null]]);
    expect(modeChoice('off')).toBe('off');
    expect(modeOption('off').words).toBe(MODE_WORDS.off);
    expect(MODE_WORDS.off).toMatch(/^Never picked, nothing moved in; sessions already there stay/);
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

describe('drain', () => {
  // Local times (the helpers read local hours), so the test holds in any TZ.
  const at = (day: number, h: number, m = 0) => new Date(2026, 10, day, h, m, 0, 0).getTime();
  const iso = (ms: number) => new Date(ms).toISOString();

  it('says until when, with the day name when it is not today', () => {
    const now = at(25, 10);
    expect(drainLine(iso(at(25, 23, 59)), now)).toBe('Draining until 23:59');
    expect(drainLine(iso(at(25, 14, 5)), now)).toBe('Draining until 14:05');
    expect(drainLine(iso(at(26, 8)), now)).toBe('Draining until Thu 08:00');
  });
  it('says nothing for no drain, a past end or garbage', () => {
    const now = at(25, 10);
    expect(drainLine(null, now)).toBe('');
    expect(drainLine(undefined, now)).toBe('');
    expect(drainLine(iso(at(25, 9)), now)).toBe('');
    expect(drainEnd('soon', now)).toBeNull();
    expect(drainEnd(iso(at(25, 11)), now)).toBe(at(25, 11));
  });
  it('offers end of today (23:59 local), 2, 4 and 8 hours', () => {
    const now = at(25, 10);
    const c = drainChoices(now);
    expect(c.map((x) => [x.id, x.label])).toEqual([['today', 'End of today'], ['2h', 'In 2 h'], ['4h', 'In 4 h'], ['8h', 'In 8 h']]);
    expect(c[0]!.until).toBe(iso(at(25, 23, 59)));
    expect(c[2]!.until).toBe(iso(now + 4 * 3_600_000));
  });
  it('leaves out end of today in its last 15 minutes', () => {
    expect(drainChoices(at(25, 23, 50)).map((x) => x.id)).toEqual(['2h', '4h', '8h']);
  });
  it('patches: modes clear the drain (Off lets the hub do it), a drain leaves Off', () => {
    expect(modePatch('aggressive')).toEqual({ mode: 'aggressive', drain_until: null });
    expect(modePatch('protected')).toEqual({ mode: 'protected', drain_until: null });
    expect(modePatch('off')).toEqual({ mode: 'off' });
    expect(modePatch(null)).toEqual({ mode: null, drain_until: null });
    expect(drainPatch('2026-11-25T18:29:00.000Z', 'protected')).toEqual({ drain_until: '2026-11-25T18:29:00.000Z' });
    expect(drainPatch('2026-11-25T18:29:00.000Z', 'off')).toEqual({ mode: null, drain_until: '2026-11-25T18:29:00.000Z' });
  });
});
