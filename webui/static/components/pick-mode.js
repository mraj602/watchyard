// How `wy account next` uses an account (docs/spec/pwa.md 3.0a, setup.md 8.5 Kind). The one place
// for the words: every mention of protected or aggressive on the board comes from here (the
// same wording as internal/picker ModeWords and the phone's pickMode.ts). No imports, so node
// can test it.

export const MODE_WORDS = {
  protected: {
    name: 'Protected',
    definition: 'Kept for you. Used last: new sessions go here only when no aggressive account can take them, or for room that would expire unused anyway.',
  },
  aggressive: {
    name: 'Aggressive',
    definition: 'Use it up. Sessions go here until 98% of the week or 5 hours.',
  },
  drain: {
    name: 'Drain',
    definition: 'Used up first until the time you set (default: end of today), then back to its own setting by itself, or earlier at 97% of the week.',
  },
  off: {
    name: 'Off',
    definition: 'Stricter than Protected. Never picked and nothing moved in; sessions already there stay, and their usage is still shown.',
  },
  none: {
    name: 'Not chosen',
    definition: 'Not chosen yet; treated as aggressive.',
  },
};

// The options of the owner's choice, in the order shown; `value` is what the hub stores.
// `drain` is not a stored mode: it is `drain_until` on top of the seat's mode, so it has no value.
export const MODE_OPTIONS = [
  { key: 'aggressive', value: 'aggressive' },
  { key: 'protected', value: 'protected' },
  { key: 'drain', value: undefined },
  { key: 'off', value: 'off' },
  { key: 'none', value: null },
];

// The status line for an account that is off.
export const OFF_LINE = 'Off: never picked, nothing moved in; sessions already there stay';

export const MODE_QUESTION = 'How should the picker use this account?';

// A stored mode (protected, aggressive, null or anything else) -> its key in MODE_WORDS.
export const modeKey = (mode) => (mode === 'protected' || mode === 'aggressive' || mode === 'off' ? mode : 'none');

// What the picker does with it: not chosen counts as aggressive.
export const effectiveKind = (mode) => (mode === 'protected' || mode === 'off' ? mode : 'aggressive');

const pad2 = (n) => String(n).padStart(2, '0');
const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

// The seat's drain end as a Date while it is still in the future, else null (a seat with mode
// off never drains).
export function drainEnd(seat, now = Date.now()) {
  if (!seat || !seat.drain_until || seat.mode === 'off') return null;
  const t = Date.parse(seat.drain_until);
  return Number.isNaN(t) || t <= now ? null : new Date(t);
}

// The key of the option that is on for a seat: drain while draining, else its mode's key.
export const seatModeKey = (seat, now = Date.now()) => (drainEnd(seat, now) ? 'drain' : modeKey(seat && seat.mode));

// "Draining until 23:59", with the weekday when the end is not today; '' when not draining.
export function drainText(seat, now = Date.now()) {
  const d = drainEnd(seat, now);
  if (!d) return '';
  const n = new Date(now);
  const same = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate();
  return `Draining until ${same ? '' : `${WEEKDAYS[d.getDay()]} `}${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

// The time input's default (and the hub's): the end of today, local.
export const DRAIN_DEFAULT_TIME = '23:59';

// A Date as the value of a datetime-local input: "YYYY-MM-DDTHH:MM", local time.
export const localInputValue = (d) =>
  `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;

// The Until field's value: the seat's current drain end (date and time, local) while draining,
// else the default, the end of today (tomorrow's once today's has passed).
export function drainInputValue(seat, now = Date.now()) {
  const d = drainEnd(seat, now);
  if (d) return localInputValue(d);
  return localInputValue(new Date(drainUntilISO(DRAIN_DEFAULT_TIME, now)));
}

// The field's value -> the RFC 3339 instant to send; null when it is malformed or not in the
// future. "YYYY-MM-DDTHH:MM" (the field) is taken exactly as shown, in local time: never moved
// to another day. A bare "HH:MM" (the default) is the next time the clock shows it.
export function drainUntilISO(value, now = Date.now()) {
  const v = String(value || '');
  const full = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::\d{2})?$/.exec(v);
  if (full) {
    const [y, mo, da, hh, mi] = full.slice(1).map(Number);
    const d = new Date(y, mo - 1, da, hh, mi, 0, 0);
    if (d.getFullYear() !== y || d.getMonth() !== mo - 1 || d.getDate() !== da || hh > 23 || mi > 59) return null;
    return d.getTime() > now ? d.toISOString() : null;
  }
  const m = /^(\d{1,2}):(\d{2})$/.exec(v);
  if (!m || Number(m[1]) > 23 || Number(m[2]) > 59) return null;
  const d = new Date(now);
  d.setHours(Number(m[1]), Number(m[2]), 0, 0);
  if (d.getTime() <= now) d.setDate(d.getDate() + 1);
  return d.toISOString();
}

// The PATCH /v1/accounts/{seat} body for an option: drain sends only drain_until (value: the
// Until field, see drainUntilISO), every other choice sends its mode and stops any drain.
export function modeBody(option, value, now = Date.now()) {
  if (option === 'drain') {
    const until = drainUntilISO(value || DRAIN_DEFAULT_TIME, now);
    return until ? { drain_until: until } : null;
  }
  const o = MODE_OPTIONS.find((x) => x.key === option);
  return o && o.key !== 'drain' ? { mode: o.value, drain_until: null } : null;
}

export const modeWords = (mode) => MODE_WORDS[modeKey(mode)];

// The line for a verdict from a machine whose own setting decides (mode_from "local") and
// differs from the hub's choice; '' otherwise.
export function localOverrideLine(verdict, seatMode) {
  if (seatMode === 'off') return '';
  if (!verdict || verdict.mode_from !== 'local' || !verdict.kind) return '';
  if (verdict.kind === effectiveKind(seatMode)) return '';
  const on = verdict.machine_name || verdict.machine || 'a machine';
  return `On ${on} a setting on that machine makes it ${verdict.kind} (set on this machine); it wins there over this choice.`;
}
