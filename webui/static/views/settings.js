// Settings: who gets notifications (this browser, the Android app), this device's
// preferences, install, hub and app version, sign out or unpair (pwa.md 6). ntfy is under
// Advanced.

import { h } from '../components/dom.js';
import { section, select, toggle, toast, moreTabs, details } from '../components/ui.js';
import { quietHours } from '../components/quiet.js';
import { CATEGORIES, mutedCategories, setCategory } from '../components/notif-cats.js';
import { liveBadge } from '../components/nav.js';
import { notifiable, appPaired, isApp } from '../components/targets.js';
import { store, isOwner, canPush, inApp } from '../store.js';
import { getSelf, getRules, getPushConfig, patchDevice, addTarget, deleteTarget, revokeDevice, listDevices } from '../api.js';
import { pwa, isStandalone, canInstall, promptInstall, checkForUpdate, updateReady, applyUpdate } from '../pwa.js';
import { pushStatus, turnOn, sendTest } from '../push.js';
import { isIOS, isMobile, ntfyLink, installHint } from '../platform.js';
import { day, clock, agoLabel, humanKey, plural, deviceLabel } from '../format.js';
import { notifNote, ownerHow } from '../components/notif-note.js';

const st = { self: null, push: null, config: null, hub: null, devices: null, test: null, ntfy: null, busy: false, updateNote: '', error: null };
const again = () => window.dispatchEvent(new Event('wy-rerender'));

const mobile = () => isMobile(navigator.userAgent, navigator.maxTouchPoints, navigator.platform);
const thisOne = () => notifNote({ app: inApp(), mobile: mobile() }).who;

export async function load({ onLogout }) {
  st.onLogout = onLogout;
  st.error = null;
  const jobs = [
    getSelf().then((d) => { st.self = d; }),
    pushStatus().then((p) => { st.push = p; }),
    getPushConfig().then((c) => { st.config = c; }).catch(() => {}),
    getRules().then((r) => { st.hub = r.settings; }).catch(() => {}),
  ];
  // Only the owner may list devices; anyone else would get a 403.
  if (isOwner()) jobs.push(listDevices().then((r) => { st.devices = r.devices; }).catch(() => {}));
  const res = await Promise.allSettled(jobs);
  const bad = res.find((r) => r.status === 'rejected' && r.reason && r.reason.name !== 'AuthError');
  if (bad) st.error = bad.reason.message;
  again();
}

const row = (k, v) => h('div', { class: 'row kv-row' }, h('span', { class: 'muted' }, k), h('span', { class: 'row-value' }, v));

async function savePrefs(patch) {
  const prefs = { ...(st.self.prefs || {}), ...patch };
  st.self = await patchDevice('self', { prefs });
  again();
}

// ---- notifications ------------------------------------------------------------------------

const MESSAGES = {
  unsupported: 'Off: this browser cannot show notifications. The Android app, or ntfy under Advanced, can.',
  'ios-old': 'Off: needs iOS 16.4 or newer. ntfy under Advanced works on older versions.',
  'install-first': 'Off: install first. Tap Share, then Add to Home Screen, and open Watchyard from your Home Screen. Notifications work only in the installed app.',
  blocked: 'Off: blocked. Allow notifications for this site in the browser or system settings, then come back.',
  off: 'Off. Turn them on to get a notification when a session needs you.',
};

async function onTurnOn(e) {
  const b = e.currentTarget;
  b.disabled = true;
  try {
    await turnOn();
  } catch (ex) {
    toast(ex && ex.name === 'AbortError' ? 'The browser could not reach its push service. Try again on another network.' : (ex.message || 'Could not turn on notifications'));
  }
  st.push = await pushStatus();
  again();
}

async function onTest(target) {
  st.test = { busy: true };
  again();
  try { st.test = await sendTest(target.id); } catch (ex) { st.test = { outcome: 'failed', error: ex.message }; }
  again();
}

function testText(t) {
  if (!t || t.busy) return t && t.busy ? 'Sending…' : '';
  if (t.outcome === 'sent') return 'Sent. It should arrive in a moment.';
  return `The push service said: ${t.error || t.outcome}${t.http_status ? ' (' + t.http_status + ')' : ''}`;
}

// This browser's own notifications (Web Push).
function browserRow() {
  const p = st.push;
  // In the desktop app even an owner session gets no Web Push (Electron has no push service):
  // the app shows the hub's alerts itself (desktop.md C9).
  if (!canPush() || inApp()) {
    // SE5: say what to do on this kind of device, with a link.
    const n = notifNote({ app: inApp(), mobile: mobile() });
    const how = {
      'app-settings': 'Turn them on or off in the app’s App settings, Notifications.',
      'pair-phone': ['To get notifications here, ', h('a', { href: '#/pair', 'data-key': 'notif-pair' }, 'pair this phone with a code'), '.'],
      'owner-or-pair': ['To get notifications here, sign in as the owner (', ownerHow(false), ') or ', h('a', { href: '#/pair', 'data-key': 'notif-pair' }, 'pair this browser'), '.'],
    }[n.how];
    return h('div', { class: 'row status-row' }, h('span', { class: 'row-text' }, h('b', {}, n.who),
      h('span', { 'data-key': 'notif-state' }, n.state), h('span', { class: 'muted small' }, how)));
  }
  if (!p) return h('div', { class: 'row status-row' }, h('span', { class: 'row-text' }, h('b', {}, thisOne()), h('span', { class: 'muted' }, st.error || 'Checking…')));
  if (p.state === 'on') {
    return h('div', { class: 'row status-row' },
      h('span', { class: 'row-text' }, h('b', {}, thisOne()),
        h('span', { class: 'status-ok', 'data-key': 'notif-state' }, 'On: notifications come to this browser.'),
        st.test ? h('span', { class: 'small', role: 'status', 'data-key': 'test-result' }, testText(st.test)) : null),
      p.target ? h('button', { type: 'button', class: 'btn btn-sm', 'data-key': 'test-push', disabled: !!store.offline, onclick: () => onTest(p.target) }, 'Send a test') : null);
  }
  return h('div', { class: 'row status-row' },
    h('span', { class: 'row-text' }, h('b', {}, thisOne()), h('span', { 'data-key': 'notif-state' }, MESSAGES[p.state] || '')),
    p.state === 'off' ? h('button', { type: 'button', class: 'btn btn-primary btn-sm', 'data-key': 'turn-on', onclick: onTurnOn }, 'Turn on') : null);
}

// The Android app's own connection, and every other device (owner only: it needs the list).
function appRows() {
  if (!isOwner() || !st.devices) return null;
  const app = st.devices.filter(isApp);
  let text;
  if (!appPaired(st.devices)) text = ['Not paired. ', h('a', { href: '#/more/devices?add=phone', 'data-key': 'pair-app' }, 'Pair the app'), ' to get notifications over its own connection to your hub.'];
  else {
    text = app.map((d) => {
      const l = d.link;
      const when = l && (l.last_seen_at || l.connected_at);
      return `${deviceLabel(d, st.devices)}: ${l && l.connected ? 'connected' : `paired, not connected now${when ? ` (last seen ${agoLabel(when)})` : ''}`}`;
    }).join(' · ');
  }
  const others = notifiable(st.devices).filter((x) => !x.device.current && !x.targets.some((t) => t.kind === 'link'));
  return [
    h('div', { class: 'row status-row', 'data-key': 'app-status' }, h('span', { class: 'row-text' }, h('b', {}, 'Android app'), h('span', {}, text))),
    others.length ? h('div', { class: 'row status-row' }, h('span', { class: 'row-text' }, h('b', {}, 'Other devices'),
      h('span', {}, `${plural(others.length, 'device')} with notifications on: ${others.map((x) => deviceLabel(x.device, st.devices)).join(', ')}`))) : null,
  ];
}

function notifications() {
  return section('Notifications',
    h('div', { class: 'panel', 'data-key': 'notif-status' }, browserRow(), appRows()),
    h('p', { class: 'muted small' }, 'What sends a notification, and when, is set in ', h('a', { href: '#/more/alerts' }, 'Alerts'), '.'));
}

function prefs() {
  if (!canPush() || inApp() || !st.self) return null;
  const pr = st.self.prefs || {};
  const priv = pr.private == null ? 'default' : pr.private ? 'on' : 'off';
  return section('This device',
    h('div', { class: 'panel' },
      h('div', { class: 'row row-ctl' }, h('span', { class: 'row-text' }, h('span', {}, 'Private notifications'), h('span', { class: 'muted small' }, 'The lock screen says only “1 session needs you”; the details load from your hub when you open it.')),
        select({ label: 'Private notifications', value: priv, key: 'pref-private', options: [['default', `Hub default${st.hub ? (st.hub.private ? ' (on)' : ' (off)') : ''}`], ['on', 'On'], ['off', 'Off']],
          onChange: (v) => savePrefs({ private: v === 'default' ? null : v === 'on' }) })),
      h('div', { class: 'row row-ctl' }, h('span', { class: 'row-text' }, h('span', {}, 'When you tap a notification'), h('span', { class: 'muted small' }, 'Automatic opens the Claude app when the session supports it, otherwise the session page here.')),
        select({ label: 'When you tap a notification', value: pr.tap || 'auto', key: 'pref-tap', options: [['auto', 'Automatic'], ['session', 'Open the session page'], ['claude', 'Open in the Claude app']],
          onChange: (v) => savePrefs({ tap: v }) })),
      h('div', { class: 'row' }, quietHours({
        device: true, value: pr.quiet_hours, hubValue: st.hub && st.hub.quiet_hours, onSave: (q) => savePrefs({ quiet_hours: q }),
      }))),
    h('p', { class: 'muted small' }, 'Turn a kind of notification off for this device. The hub then sends it none of that kind.'),
    h('div', { class: 'panel', 'data-key': 'notif-cats' }, CATEGORIES.map((c) => h('div', { class: 'row' }, toggle({
      label: c.label, sub: c.sub, checked: !mutedCategories(pr).includes(c.id), key: `cat-${c.id}`,
      onChange: (v) => savePrefs({ mute_categories: setCategory(pr, c.id, v) }),
    })))));
}

// ---- Advanced: ntfy ------------------------------------------------------------------------

function ntfy() {
  if (!canPush() || inApp() || !st.self) return null;
  const targets = (st.self.targets || []).filter((t) => t.kind === 'ntfy');
  const server = st.config && st.config.ntfy_default_server || 'https://ntfy.sh';
  let srv = server, topic = '', token = '';
  const form = h('form', {
    class: 'panel pad-s col',
    onsubmit: async (e) => {
      e.preventDefault();
      const b = form.querySelector('button[type=submit]');
      b.disabled = true;
      try {
        const body = { kind: 'ntfy', server: srv };
        if (topic) body.topic = topic;
        if (token) body.token = token;
        st.ntfy = await addTarget(body);
        st.self = await getSelf();
        again();
      } catch (ex) { toast(ex.message); } finally { b.disabled = false; }
    },
  },
    h('p', { class: 'muted small' }, 'ntfy is an optional extra: a separate app that can receive notifications where this browser cannot. Leave the topic empty to get a random one.'),
    h('label', { class: 'field col-field' }, 'Server', h('input', { type: 'text', value: srv, 'data-key': 'ntfy-server', autocomplete: 'off', oninput: (e) => { srv = e.target.value.trim(); } })),
    h('label', { class: 'field col-field' }, 'Topic (optional)', h('input', { type: 'text', value: '', 'data-key': 'ntfy-topic', autocomplete: 'off', spellcheck: 'false', oninput: (e) => { topic = e.target.value.trim(); } })),
    h('label', { class: 'field col-field' }, 'Access token (optional)', h('input', { type: 'password', value: '', 'data-key': 'ntfy-token', autocomplete: 'off', oninput: (e) => { token = e.target.value.trim(); } })),
    h('button', { type: 'submit', class: 'btn', 'data-key': 'ntfy-add' }, 'Add ntfy topic'));
  const how = (t) => h('p', { class: 'small' }, 'Subscribe in the ntfy app: server ', h('code', {}, t.server || server), ', topic ', h('code', {}, t.topic || ''),
    t.server && t.topic ? h('span', {}, ' · ', h('a', { href: ntfyLink(t.server, t.topic) }, 'open in ntfy')) : null);
  return h('div', { class: 'sub-block' }, h('h3', {}, 'ntfy'),
    targets.length ? h('div', { class: 'panel' }, targets.map((t) => h('div', { class: 'row ntfy-row' },
      h('span', { class: 'row-text' }, h('span', {}, `${t.server || server} · ${t.topic}`), how(t)),
      h('button', {
        type: 'button', class: 'btn btn-sm', 'data-key': `ntfy-del-${t.id}`,
        onclick: async () => { try { await deleteTarget(t.id); st.self = await getSelf(); again(); } catch (ex) { toast(ex.message); } },
      }, 'Remove')))) : null,
    st.ntfy ? h('p', { class: 'status-ok small', role: 'status' }, `Added topic ${st.ntfy.topic}`) : null,
    form);
}

function advanced() {
  const n = ntfy();
  if (!n) return null;
  const d = h('details', { class: 'tech advanced', 'data-key': 'advanced' },
    h('summary', {}, 'Advanced: ntfy'),
    h('div', { class: 'tech-body' }, n));
  return h('section', { class: 'block', 'aria-label': 'Advanced' }, d);
}

// ---- install, version, sign out -----------------------------------------------------------

function installBlock() {
  if (inApp()) return null; // the desktop app is already installed; there is no PWA to add
  const ua = navigator.userAgent;
  const how = canInstall() && !isStandalone() ? 'Not installed. Install it for its own window and notifications.'
    : installHint(ua, { ios: isIOS(ua, navigator.maxTouchPoints, navigator.platform), standalone: isStandalone() });
  return section('Install',
    h('div', { class: 'panel pad-s col' },
      h('p', { 'data-key': 'install-state' }, how),
      canInstall() ? h('button', { type: 'button', class: 'btn btn-primary', 'data-key': 'install-app', onclick: promptInstall }, 'Install app') : null,
      !pwa.supported ? h('p', { class: 'muted small' }, 'No offline support here: the page needs to be opened over https.') : null));
}

// SE4: one version line; the build hashes are in Details.
function boardState() {
  if (inApp()) return 'board served by the hub';
  if (updateReady()) return 'a new board version is ready';
  if (pwa.build && pwa.hubBuild && pwa.build !== pwa.hubBuild) return 'board update on its way';
  return pwa.build || !pwa.supported ? 'board up to date' : 'checking the board version…';
}

function hubBlock(s) {
  const v = (s.me.hub && s.me.hub.version) || '';
  return section('Hub',
    h('div', { class: 'panel pad-s col' },
      row('Address', h('span', { class: 'mono', 'data-key': 'hub-address' }, location.origin)),
      row('Version', h('span', { 'data-key': 'version-line' }, `${v ? `Hub ${v}` : 'Hub version unknown'} · ${boardState()}`)),
      row('Status', liveBadge(s)),
      // AGPL-3.0 section 13: the people who use the board over the network can get its source.
      row('Source code', h('a', { href: 'https://github.com/mraj602/watchyard', target: '_blank', rel: 'noopener noreferrer', 'data-key': 'source-link' }, 'AGPL-3.0, on GitHub')),
      s.offline ? row('Last update', clock(new Date(s.offline.since).toISOString())) : null,
      inApp() ? null : h('div', { class: 'actions' },
        h('button', {
          type: 'button', class: 'btn btn-sm', 'data-key': 'check-update',
          onclick: async (e) => { e.currentTarget.disabled = true; const r = await checkForUpdate(); st.updateNote = r ? '' : 'You are up to date.'; again(); },
        }, 'Check for update'),
        updateReady() ? h('button', { type: 'button', class: 'btn btn-primary btn-sm', onclick: applyUpdate }, 'Reload to update') : null),
      st.updateNote ? h('p', { class: 'muted small', role: 'status' }, st.updateNote) : null,
      details({ text: 'Details', key: 'hub-details' },
        row('Board build', h('span', { class: 'mono', 'data-key': 'build' }, pwa.build || (pwa.supported ? 'checking…' : 'no service worker'))),
        row('Build the hub serves', h('span', { class: 'mono' }, pwa.hubBuild || '–')))));
}

// An owner session slides (pairing.md 4): every use in its last week renews it for 30 days,
// so there is no warning banner, only the date here.
function ownerUntilRow(at) {
  return row('Owner access until', h('span', { class: 'col' },
    h('span', { title: new Date(at).toString(), 'data-key': 'owner-until' }, day(at)),
    h('span', { class: 'muted small' }, 'Renews when you use the board.')));
}

function deviceBlock() {
  const me = store.me;
  const d = me.device;
  if (inApp()) {
    // The app signs this board in by itself with its desktop pairing: a sign-out here would
    // just be undone. Unpairing the app is in its App settings, This computer.
    const via = me.via || d;
    return section('Sign-in',
      h('div', { class: 'panel pad-s col' },
        row('Name', via.name),
        row('Kind', `Desktop app${me.owner ? ' · owner' : ''}`),
        me.owner && d.expires_at ? ownerUntilRow(d.expires_at) : null,
        h('p', { class: 'muted small', 'data-key': 'app-signin' }, 'The app signs this board in with its own pairing. To unpair it, use App settings, This computer, Unpair this computer.')));
  }
  const unpair = d.kind === 'phone';
  return section('Sign-in',
    h('div', { class: 'panel pad-s col' },
      row('Name', d.name),
      row('Kind', `${humanKey(d.kind)}${me.owner ? ' · owner' : canPush() ? '' : ' · read-only'}`),
      d.expires_at ? (me.owner ? ownerUntilRow(d.expires_at)
        : row('Signed in until', h('span', { title: new Date(d.expires_at).toString() }, day(d.expires_at)))) : null,
      h('div', { class: 'actions' },
        unpair ? h('button', {
          type: 'button', class: 'btn btn-danger', 'data-key': 'unpair',
          onclick: async () => {
            if (!confirm('Unpair this phone? It will stop getting notifications and must be paired again.')) return;
            try { await revokeDevice('self'); } catch (ex) { toast(ex.message); return; }
            st.onLogout();
          },
        }, 'Unpair this phone') : h('button', { type: 'button', class: 'btn', 'data-key': 'signout', onclick: () => st.onLogout() }, 'Sign out'))));
}

export function render({ store: s }) {
  const owner = isOwner();
  const node = h('div', { class: 'page' },
    h('header', { class: 'page-head col' }, h('h1', {}, inApp() ? 'Hub settings' : 'Settings'), moreTabs('settings', { owner })),
    notifications(),
    prefs(),
    installBlock(),
    hubBlock(s),
    deviceBlock(),
    advanced(),
    h('p', { class: 'muted small' }, h('a', { href: '#/more/peers', 'data-key': 'peers-link' }, 'Peers: which accounts may message each other'),
      owner ? [' · ', h('a', { href: '#/more/devices' }, 'Manage paired devices')] : null));
  return { title: 'Settings · Watchyard', node };
}
