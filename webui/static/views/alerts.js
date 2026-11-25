// Alerts and rules (PhoneAlerts / DeskAlerts): which notifications go out, quiet hours, and the
// log of recent alerts with their outcome. "Alert" is the item on the board; "notification"
// is what reaches a device.
//
// - An owner session edits the hub's rules (switches and waits) and overrides.
// - A push-scoped device (a paired phone) edits only its own mute list and quiet hours.
// - A plain board login sees everything read-only.

import { h } from '../components/dom.js';
import { toggle, select, section, toast, moreTabs } from '../components/ui.js';
import { quietHours } from '../components/quiet.js';
import { seatIndex, seatName } from '../components/seat.js';
import { notifiable, targetKindText } from '../components/targets.js';
import { clock, day, deviceLabel } from '../format.js';
import { hashOfLink } from '../route.js';
import { isOwner, canPush, inApp } from '../store.js';
import { ownerHow } from '../components/notif-note.js';
import {
  getRules, getSelf, patchDevice, putRule, addRule, deleteRule, putPushSettings, getAlerts, listDevices, testTarget,
} from '../api.js';
import { reasonText } from '../components/alerts.js';

const GROUPS = [
  { id: 'needs_permission', label: 'Needs permission', triggers: ['needs_permission'], delay: true },
  { id: 'asked', label: 'Asked a question', triggers: ['asked'], delay: true },
  { id: 'failed', label: 'Failed or rate limited', triggers: ['failed'] },
  { id: 'limits', label: 'Limits at 80% / 95%', triggers: ['limit_80', 'limit_95'] },
  { id: 'reset', label: 'Limit window reset', triggers: ['reset'] },
  { id: 'machine_silent', label: 'Machine silent with live sessions', triggers: ['machine_silent'], delay: true },
  { id: 'usage', label: 'Usage and limit changes', triggers: ['limit_change', 'usage_reset', 'seat_change', 'weekly_drop'] },
  { id: 'switch', label: 'Smart Account Switch moves and digest', triggers: ['session_moved', 'switch_digest'] },
  { id: 'drain', label: 'Drain started, ended or pulled early', triggers: ['drain_started', 'drain_ended', 'drain_pulled'] },
];
const TRIGGER_LABEL = {
  needs_permission: 'Needs permission', asked: 'Asked a question', failed: 'Failed or rate limited',
  limit_80: 'Limit at 80%', limit_95: 'Limit at 95%', reset: 'Limit window reset', machine_silent: 'Machine silent',
  limit_change: 'Limits changed', usage_reset: 'Usage reset', seat_change: 'Changed account', weekly_drop: 'Weekly use dropped',
  session_moved: 'Moved a session', switch_digest: 'Switch digest',
  drain_started: 'Drain started', drain_ended: 'Drain ended', drain_pulled: 'Drain pulled early',
};
const DELAYS = [0, 30, 60, 120, 300, 600, 1800, 3600];

export function delayText(s) {
  if (!s) return '';
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  return `${Math.round(s / 3600)} h`;
}
const waitText = (s) => (s ? `after ${delayText(s)}` : 'at once');
const delayOptions = (cur) => [...new Set([...DELAYS, cur])].sort((a, b) => a - b).map((s) => [s, waitText(s)]);

const OUTCOME = { fired: 'notified', seen: 'seen', snoozed: 'snoozed', cancelled: 'cancelled', closed: 'closed', pending: 'waiting', skipped: 'not sent' };

const state = { rules: null, settings: null, self: null, alerts: null, devices: null, error: null, loaded: false };
const again = () => window.dispatchEvent(new Event('wy-rerender'));

export async function load() {
  state.error = null;
  const tasks = [
    getRules().then((r) => { state.rules = r.rules; state.settings = r.settings; }),
    getSelf().then((d) => { state.self = d; }),
    getAlerts({ limit: '20' }).then((r) => { state.alerts = r.alerts; }),
  ];
  if (isOwner()) tasks.push(listDevices().then((r) => { state.devices = r.devices; }).catch(() => {}));
  const results = await Promise.allSettled(tasks);
  const bad = results.find((r) => r.status === 'rejected' && r.reason && r.reason.name !== 'AuthError');
  if (bad) state.error = bad.reason.message;
  state.loaded = true;
  again();
}

const base = (trigger) => (state.rules || []).find((r) => r.base && r.trigger === trigger);

async function saveRule(rule, patch) {
  const body = { enabled: rule.enabled, delay_s: rule.delay_s, bypass_quiet: rule.bypass_quiet, scope: rule.scope, ...patch };
  const next = await putRule(rule.id, body);
  Object.assign(rule, next);
}

async function savePrefs(patch) {
  const prefs = { ...(state.self.prefs || {}), ...patch };
  state.self = await patchDevice('self', { prefs });
  again();
}

function ruleRows(mode) {
  return GROUPS.map((g) => {
    const rules = g.triggers.map(base).filter(Boolean);
    if (!rules.length) return null;
    const first = rules[0];
    const delay = g.delay ? waitText(first.delay_s) : '';
    const label = delay && mode !== 'owner' ? `${g.label} · ${delay}` : g.label;
    if (mode === 'owner') {
      return h('div', { class: 'row rule-row' },
        toggle({
          label, checked: rules.every((r) => r.enabled), key: `rule-${g.id}`,
          onChange: async (v) => { for (const r of rules) await saveRule(r, { enabled: v }); again(); },
        }),
        g.delay ? select({
          label: `${g.label}: how long to wait before notifying`, value: first.delay_s, options: delayOptions(first.delay_s), key: `delay-${g.id}`,
          onChange: async (v) => { await saveRule(first, { delay_s: Number(v) }); again(); },
        }) : null);
    }
    const mute = (state.self && state.self.prefs && state.self.prefs.mute) || [];
    const ruleOn = rules.every((r) => r.enabled);
    const muted = g.triggers.some((t) => mute.includes(t));
    return h('div', { class: 'row rule-row' }, toggle({
      label, sub: ruleOn ? '' : 'Off on the hub', checked: ruleOn && !muted, key: `rule-${g.id}`,
      disabled: mode === 'read' || !ruleOn,
      onChange: (v) => savePrefs({ mute: v ? mute.filter((t) => !g.triggers.includes(t)) : [...new Set([...mute, ...g.triggers])] }),
    }));
  });
}

// "Work, api" or "all accounts": what a rule or override covers, in words.
function scopeWords(scope, idx) {
  const s = scope || {};
  const seatLabel = (id) => (idx.get(id) ? seatName(idx.get(id).seat) : id);
  const parts = [...(s.seats || []).map(seatLabel), ...(s.projects || []), ...(s.machines || []), ...(s.windows || [])];
  return parts.length ? parts.join(', ') : 'all accounts';
}

// The override being changed in place (its row shows For / Then / Wait), or null.
let edit = null;

function overrideRow(r, mode, idx, seats) {
  const text = h('span', { class: 'row-text' }, h('span', {}, `${TRIGGER_LABEL[r.trigger] || r.trigger} · ${scopeWords(r.scope, idx)}`),
    h('span', { class: 'muted small' }, r.enabled ? `notify ${waitText(r.delay_s)}` : 'do not notify'));
  if (mode !== 'owner') return h('div', { class: 'row', 'data-key': `ov-${r.id}` }, text);
  if (edit && edit.id === r.id) return h('div', { class: 'row ov-edit', 'data-key': `ov-${r.id}` }, editOverride(r, seats));
  return h('div', { class: 'row', 'data-key': `ov-${r.id}` }, text,
    h('span', { class: 'row-actions' },
      h('button', {
        type: 'button', class: 'btn btn-sm', 'data-key': `edit-${r.id}`, 'aria-label': `Change ${TRIGGER_LABEL[r.trigger] || r.trigger} · ${scopeWords(r.scope, idx)}`,
        onclick: () => {
          const seat = ((r.scope || {}).seats || [])[0] || '';
          edit = { id: r.id, seat, enabled: r.enabled ? 'on' : 'off', delay: r.enabled ? r.delay_s : 300 };
          again();
          requestAnimationFrame(() => { const el = document.querySelector(`[data-key="ov-${r.id}"] select`); if (el) el.focus(); });
        },
      }, 'Change'),
      h('button', {
        type: 'button', class: 'btn btn-sm', 'data-key': `del-${r.id}`,
        onclick: async (e) => {
          e.currentTarget.disabled = true;
          try { await deleteRule(r.id); state.rules = state.rules.filter((x) => x.id !== r.id); again(); } catch (ex) { toast(ex.message); e.currentTarget.disabled = false; }
        },
      }, 'Remove')));
}

// A select bound to a field of obj; changing `enabled` redraws (the Wait field comes and goes).
function fieldSelect(obj, label, opts, key, field) {
  return h('label', { class: 'field' }, label,
    h('select', { 'data-key': key, onchange: (e) => { obj[field] = e.target.value; if (field === 'enabled') again(); } },
      opts.map(([v, t]) => h('option', { value: v, selected: String(v) === String(obj[field]) }, t))));
}

const seatOptions = (seats, cur) => [['', 'All accounts'],
  ...seats.filter((s) => !s.hidden || s.id === cur).map((s) => [s.id, seatName(s)])];

// Change an override in place (For, Then, Wait); its trigger stays (PUT /v1/rules/{id}).
function editOverride(r, seats) {
  const el = h('form', {
    class: 'override-form override-edit',
    onsubmit: async (e) => {
      e.preventDefault();
      const { seat, enabled, delay } = edit;
      const scope = { ...(r.scope || {}) };
      if (seat) scope.seats = [seat]; else delete scope.seats;
      const dup = (state.rules || []).find((x) => x.id !== r.id && x.trigger === r.trigger && sameScope(x.scope, scope));
      if (dup) { duplicateToast(dup.id, seats); return; }
      const b = el.querySelector('button[type=submit]');
      b.disabled = true;
      try {
        await saveRule(r, { enabled: enabled === 'on', delay_s: enabled === 'on' ? Number(delay) : r.delay_s, scope });
        edit = null;
        again();
      } catch (ex) {
        if (ex.status === 409 && ex.code === 'duplicate_rule') duplicateToast(ex.rule, seats);
        else toast(ex.message);
      } finally { b.disabled = false; }
    },
  },
    h('p', { class: 'ov-edit-title' }, TRIGGER_LABEL[r.trigger] || r.trigger),
    h('div', { class: 'form-row' },
      fieldSelect(edit, 'For', seatOptions(seats, edit.seat), `ov-${r.id}-seat`, 'seat'),
      fieldSelect(edit, 'Then', [['on', 'Notify'], ['off', 'Do not notify']], `ov-${r.id}-enabled`, 'enabled'),
      edit.enabled === 'on' ? fieldSelect(edit, 'Wait', delayOptions(Number(edit.delay)), `ov-${r.id}-delay`, 'delay') : null),
    h('div', { class: 'row-actions' },
      h('button', { type: 'submit', class: 'btn btn-sm btn-primary', 'data-key': `save-${r.id}` }, 'Save'),
      h('button', { type: 'button', class: 'btn btn-sm', 'data-key': `cancel-${r.id}`, onclick: () => { edit = null; again(); } }, 'Cancel')));
  return el;
}

function overrides(mode, idx) {
  const list = (state.rules || []).filter((r) => !r.base);
  const seats = [...idx.values()].map((x) => x.seat);
  if (edit && !list.some((r) => r.id === edit.id)) edit = null;
  const rows = list.map((r) => overrideRow(r, mode, idx, seats));
  if (!rows.length && mode !== 'owner') return null;
  const form = mode === 'owner' ? addOverride(seats, list) : null;
  return section('Overrides',
    h('p', { class: 'muted small section-help' }, 'Exceptions to the rules for one account, such as a longer wait or no notification at all. An override wins over its rule.'),
    h('div', { class: 'panel' }, rows.length ? rows : h('p', { class: 'muted pad-s' }, 'No overrides.')), form);
}

// The same trigger for the same accounts (projects, machines, windows) twice would only
// conflict; the hub may refuse it as well.
const keyOf = (scope, k) => JSON.stringify([...(((scope || {})[k]) || [])].sort());
const sameScope = (a, b) => ['seats', 'projects', 'machines', 'windows'].every((k) => keyOf(a, k) === keyOf(b, k));

// The hub refuses a second rule with the same trigger and scope (409 duplicate_rule, with the id of
// the one that exists). Say which one in words, and offer to go to it (R3).
function duplicateToast(id, seats) {
  const r = (state.rules || []).find((x) => x.id === id);
  if (!r) { toast('A rule for that already exists. Change it instead of adding another.'); return; }
  const trig = TRIGGER_LABEL[r.trigger] || r.trigger;
  const sc = scopeWords(r.scope, new Map(seats.map((x) => [x.id, { seat: x }])));
  const group = GROUPS.find((g) => g.triggers.includes(r.trigger));
  const where = r.base ? `the rule “${trig}” (${sc}) in Rules` : `the override “${trig} · ${sc}”`;
  toast(`That is already covered by ${where}. Change that one instead.`, {
    label: r.base ? 'Edit that rule' : 'Show it',
    run: () => {
      const el = document.querySelector(r.base ? `[data-key="rule-${group ? group.id : ''}"]` : `[data-key="ov-${r.id}"]`);
      const row = el && (el.closest('.row') || el);
      if (!row) return;
      row.scrollIntoView({ block: 'center', behavior: 'smooth' });
      row.classList.add('flash');
      setTimeout(() => row.classList.remove('flash'), 2500);
      const ctl = row.querySelector('select') || row.querySelector('input,button');
      if (ctl) ctl.focus({ preventScroll: true });
    },
  });
}

// The form keeps its choices across re-renders (the Wait field comes and goes).
const form = { trigger: 'asked', seat: '', enabled: 'on', delay: 300 };

function addOverride(seats, existing) {
  const sel = (label, opts, key, field) => fieldSelect(form, label, opts, key, field);
  const el = h('form', {
    class: 'panel override-form',
    onsubmit: async (e) => {
      e.preventDefault();
      const { trigger, seat, enabled, delay } = form;
      const scope = seat ? { seats: [seat] } : {};
      const dup = existing.find((r) => r.trigger === trigger && sameScope(r.scope, scope));
      if (dup) { duplicateToast(dup.id, seats); return; }
      const b = el.querySelector('button[type=submit]');
      b.disabled = true;
      try {
        const r = await addRule({ trigger, enabled: enabled === 'on', delay_s: enabled === 'on' ? Number(delay) : 0, scope });
        state.rules = [...state.rules, r];
        again();
      } catch (ex) {
        if (ex.status === 409 && ex.code === 'duplicate_rule') duplicateToast(ex.rule, seats);
        else toast(ex.message);
      } finally { b.disabled = false; }
    },
  },
    h('div', { class: 'form-row' },
      sel('When', Object.entries(TRIGGER_LABEL), 'ov-trigger', 'trigger'),
      sel('For', seatOptions(seats, ''), 'ov-seat', 'seat'),
      sel('Then', [['on', 'Notify'], ['off', 'Do not notify']], 'ov-enabled', 'enabled'),
      // A wait means nothing when nothing is sent.
      form.enabled === 'on' ? sel('Wait', delayOptions(300), 'ov-delay', 'delay') : null),
    h('button', { type: 'submit', class: 'btn', 'data-key': 'ov-add' }, 'Add override'));
  return el;
}

function recent() {
  const list = state.alerts;
  const cancelled = (list || []).some((a) => a.status === 'cancelled');
  return section('Recent alerts',
    !list ? h('p', { class: 'muted' }, state.error || 'Loading…')
      : list.length === 0 ? h('p', { class: 'calm' }, 'No alerts yet. They appear here when something needs you.')
      : h('ul', { class: 'panel log' }, list.map((a) => h('li', { class: 'log-row' },
        h('time', { class: 'mono small log-time', datetime: a.fired_at || a.created_at }, clock(a.fired_at || a.created_at)),
        h('a', { class: 'log-title', href: hashOfLink(a.url) }, a.title || TRIGGER_LABEL[a.trigger] || a.trigger),
        h('span', { class: `log-outcome out-${a.status}`, title: reasonText(a.reason) }, OUTCOME[a.status] || a.status)))),
    cancelled ? h('p', { class: 'muted small' }, '“Cancelled”: you answered in the terminal before the wait ran out, so no notification was sent.') : null);
}

// Who can get a notification right now: the owner sees every device, a paired device
// only itself. Null while that is not known yet.
function receivers() {
  if (isOwner()) return state.devices ? notifiable(state.devices) : null;
  if (state.self) return notifiable([state.self]);
  return null;
}

function statusLine(mode) {
  const list = receivers();
  if (!list || mode === 'read') return null;
  if (!list.length) {
    return h('p', { class: 'notice notice-warn', role: 'status', 'data-key': 'no-receivers' },
      mode === 'owner'
        ? ['No device can receive notifications yet, so alerts only show here on the board. ',
          h('a', { href: '#/more/devices?add=phone', 'data-key': 'setup-receiver' }, 'Pair the Android app'),
          inApp() ? ', or turn on notifications in the app’s App settings, Notifications.' : [' or ', h('a', { href: '#/more/settings' }, 'turn them on in this browser'), '.']]
        : ['This device gets no notifications yet. ', h('a', { href: '#/more/settings', 'data-key': 'setup-receiver' }, 'Turn them on in Settings'), '.']);
  }
  const names = list.map((x) => deviceLabel(x.device, state.devices));
  return h('p', { class: 'notice', role: 'status', 'data-key': 'receivers' },
    mode === 'owner' ? `Notifications go to ${list.length === 1 ? names[0] : `${list.length} devices: ${names.join(', ')}`}.` : 'This device gets notifications.');
}

function phones() {
  if (!isOwner() || !state.devices) return null;
  const list = notifiable(state.devices);
  return h('div', { class: 'cards-2 desk-only' },
    h('div', { class: 'card' }, h('h2', {}, 'Add a phone'),
      h('p', { class: 'muted' }, 'Pair the Watchyard Android app, or a phone’s browser: scan the QR code or type the 6-digit code.'),
      h('a', { class: 'btn btn-primary', href: '#/more/devices?add=phone', 'data-key': 'add-phone' }, 'Add phone')),
    h('div', { class: 'card' }, h('h2', {}, 'Devices that can get notifications'),
      list.length ? h('ul', { class: 'plain receivers' }, list.map(({ device: d, targets }) => h('li', { class: 'row', 'data-key': `receiver-${d.id}` },
        h('span', { class: 'row-text' }, h('span', {}, deviceLabel(d, state.devices)),
          h('span', { class: 'muted small' }, [[...new Set(targets.map(targetKindText))].join(', '), d.created_at ? `added ${day(d.created_at)}` : ''].filter(Boolean).join(' · '))),
        h('button', {
          type: 'button', class: 'linkish', 'data-key': `test-${targets[0].id}`,
          onclick: async () => { try { const r = await testTarget(targets[0].id); toast(r.outcome === 'sent' ? 'Test notification sent' : `Test notification: ${r.outcome}${r.error ? ' · ' + r.error : ''}`); } catch (ex) { toast(ex.message); } },
        }, 'Send a test'))))
        : h('p', { class: 'muted' }, 'None yet.'),
      h('a', { href: '#/more/devices' }, 'All devices')));
}

export function render({ store }) {
  const owner = isOwner();
  const mode = owner ? 'owner' : canPush() ? 'device' : 'read';
  const idx = seatIndex(store.overview.seats);
  const quiet = state.settings && state.self
    ? (owner
      ? quietHours({
        value: state.settings.quiet_hours, hubValue: null, key: 'hub',
        onSave: async (q) => { state.settings = await putPushSettings({ ...state.settings, quiet_hours: q }); again(); },
      })
      : quietHours({
        device: true, value: state.self.prefs && state.self.prefs.quiet_hours, hubValue: state.settings.quiet_hours, disabled: mode === 'read',
        onSave: (q) => savePrefs({ quiet_hours: q }),
      }))
    : null;

  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head col' }, h('h1', {}, 'Alerts'), moreTabs('alerts', { owner }),
      h('p', { class: 'muted small' }, 'An alert is something that needs you. It shows on the board, and a notification can go to your devices.')),
    statusLine(mode),
    phones(),
    section('Rules',
      h('p', { class: 'muted small section-help' }, mode === 'owner'
        ? 'What sends a notification, for every device, and how long to wait first so you can answer at the terminal before anything is sent.'
        : mode === 'device'
          ? 'What sends a notification. The switches choose what this device gets; the hub sets the waits.'
          : `What sends a notification. Read-only: to change them, sign in as the owner (${ownerHow(inApp())}).`),
      !state.rules ? h('p', { class: 'muted' }, state.error || 'Loading…')
        : h('div', { class: 'panel' }, ruleRows(mode), quiet ? h('div', { class: 'row' }, quiet) : null)),
    state.rules ? overrides(mode, idx) : null,
    recent());
  return { title: 'Alerts · Watchyard', node };
}
