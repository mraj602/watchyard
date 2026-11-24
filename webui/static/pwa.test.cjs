// node --test webui/static/*.test.cjs
// Pure helpers of the app (route.js, platform.js, format.js): hash routes, platform guesses,
// notification state, pairing codes.

const test = require('node:test');
const assert = require('node:assert/strict');

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// The helpers are ES modules with no imports. Load them without Node's ESM loader (so this
// runs on Node 20 too, where a .js file without "type": "module" is CommonJS): drop the
// `export` keywords and return the exported names from a function wrapper.
const mod = async (f) => {
  const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
  if (/^import /m.test(src)) throw new Error(`${f} has imports; test only pure modules`);
  const names = [...src.matchAll(/^export (?:async )?(?:function|const|let|class) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  const body = src.replace(/^export /gm, '');
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { URL, URLSearchParams, atob, btoa, Uint8Array, Intl, Date, Number, String, Math, JSON, Set, Map, Array, Object, Promise, isNaN: Number.isNaN });
};

const UA = {
  android: 'Mozilla/5.0 (Linux; Android 14; Phone X) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36',
  reduced: 'Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36',
  iphone: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1',
  iphoneOld: 'Mozilla/5.0 (iPhone; CPU iPhone OS 16_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.3 Mobile/15E148 Safari/604.1',
  ipad: 'Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
  mac: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15',
  linuxChrome: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36',
  firefoxAndroid: 'Mozilla/5.0 (Android 14; Mobile; rv:131.0) Gecko/131.0 Firefox/131.0',
};

test('parseRoute covers every screen', async () => {
  const { parseRoute } = await mod('route.js');
  const r = (h) => { const x = parseRoute(h); return [x.view, x.page, x.id || '']; };
  assert.deepEqual(r(''), ['now', 'now', '']);
  assert.deepEqual(r('#/'), ['now', 'now', '']);
  assert.deepEqual(r('#/sessions'), ['sessions', 'sessions', '']);
  assert.deepEqual(r('#/sessions/abc%20d?alert=al_1'), ['session', 'sessions', 'abc d']);
  assert.equal(parseRoute('#/sessions/abc?alert=al_1').query.get('alert'), 'al_1');
  assert.deepEqual(r('#/accounts?seat=s1'), ['accounts', 'accounts', '']);
  assert.deepEqual(r('#/machines'), ['machines', 'machines', '']);
  assert.deepEqual(r('#/switches?machine=m_1&mode=auto'), ['switches', 'switches', '']);
  assert.equal(parseRoute('#/switches?machine=m_1&mode=auto').query.get('mode'), 'auto');
  assert.deepEqual(r('#/more'), ['more', 'more', '']);
  assert.deepEqual(r('#/more/alerts'), ['alerts', 'alerts', '']);
  assert.deepEqual(r('#/more/settings'), ['settings', 'settings', '']);
  assert.deepEqual(r('#/more/devices'), ['devices', 'devices', '']);
  assert.deepEqual(r('#/more/peers'), ['peers', 'peers', '']);
  assert.deepEqual(r('#/pair?s=abc&c=482913'), ['pair', 'pair', '']);
  assert.equal(parseRoute('#/pair?s=abc&c=482913').query.get('c'), '482913');
  assert.deepEqual(r('#/owner?s=xyz'), ['owner', 'owner', '']);
  assert.deepEqual(r('#/alerts/al_9'), ['alertRedirect', 'alerts', 'al_9']);
  assert.deepEqual(r('#/login'), ['login', 'login', '']);
  // Unknown routes show "page not found" instead of quietly showing Now.
  assert.deepEqual(r('#/nonsense'), ['notfound', 'notfound', '']);
  assert.deepEqual(r('#/nonsense/page'), ['notfound', 'notfound', '']);
  assert.deepEqual(r('#/more/nonsense'), ['notfound', 'notfound', '']);
});

test('public views and tabs', async () => {
  const { PUBLIC_VIEWS, TAB_OF, hashOfLink } = await mod('route.js');
  assert.deepEqual([...PUBLIC_VIEWS].sort(), ['login', 'owner', 'pair']);
  for (const p of ['machines', 'switches', 'alerts', 'settings', 'devices', 'peers', 'more']) assert.equal(TAB_OF[p], 'more');
  assert.equal(hashOfLink('/#/sessions/1?alert=a'), '#/sessions/1?alert=a');
  assert.equal(hashOfLink('/'), '#/');
  assert.equal(hashOfLink(null), '#/');
});

test('deviceName guesses a readable name', async () => {
  const { deviceName } = await mod('platform.js');
  assert.equal(deviceName(UA.android), 'Phone X · Chrome');
  assert.equal(deviceName(UA.reduced), 'Android · Chrome');
  assert.equal(deviceName(UA.iphone), 'iPhone · Safari');
  assert.equal(deviceName('Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/140.0.0.0 Safari/537.36'), 'Headless Chrome');
  assert.equal(deviceName(UA.ipad), 'iPad · Safari');
  assert.equal(deviceName(UA.mac), 'Mac · Safari');
  assert.equal(deviceName(UA.linuxChrome), 'Linux · Chrome');
  assert.equal(deviceName(UA.firefoxAndroid), 'Android · Firefox');
  assert.equal(deviceName(''), 'This device · Browser');
  assert.ok(deviceName('x'.repeat(200)).length <= 60);
});

test('platform checks', async () => {
  const p = await mod('platform.js');
  assert.equal(p.isIOS(UA.iphone), true);
  assert.equal(p.isIOS(UA.android), false);
  assert.equal(p.isIOS(UA.mac, 5, 'MacIntel'), true); // iPadOS posing as a Mac
  assert.equal(p.isIOS(UA.mac, 0, 'MacIntel'), false);
  assert.equal(p.isMobile(UA.android), true);
  assert.equal(p.isMobile(UA.linuxChrome), false);
  assert.equal(JSON.stringify(p.iosVersion(UA.iphone)), '[17,4]');
  assert.equal(p.iosVersion(UA.android), null);
  assert.equal(p.iosNeedsUpgrade(UA.iphoneOld), true);
  assert.equal(p.iosNeedsUpgrade(UA.iphone), false);
});

test('notificationState follows pwa.md 6', async () => {
  const { notificationState } = await mod('platform.js');
  const base = { ua: UA.android, standalone: false, hasPush: true, hasNotification: true, permission: 'default', subscribed: false };
  assert.equal(notificationState(base), 'off');
  assert.equal(notificationState({ ...base, permission: 'denied' }), 'blocked');
  assert.equal(notificationState({ ...base, permission: 'granted', subscribed: true }), 'on');
  assert.equal(notificationState({ ...base, permission: 'granted', subscribed: false }), 'off');
  assert.equal(notificationState({ ...base, hasPush: false }), 'unsupported');
  assert.equal(notificationState({ ...base, ua: UA.iphone, standalone: false, hasPush: false, hasNotification: false }), 'install-first');
  assert.equal(notificationState({ ...base, ua: UA.iphone, standalone: true }), 'off');
  assert.equal(notificationState({ ...base, ua: UA.iphoneOld, standalone: true }), 'ios-old');
});

test('pairing code helpers', async () => {
  const p = await mod('platform.js');
  assert.equal(p.normalizeCode('482913'), '482-913');
  assert.equal(p.normalizeCode('482 913'), '482-913');
  assert.equal(p.normalizeCode('48-2'), '482');
  assert.equal(p.normalizeCode('4829139999'), '482-913');
  assert.equal(p.codeDigits('482-913'), '482913');
  assert.equal(p.validCode('482-913'), true);
  assert.equal(p.validCode('482-91'), false);
});

test('VAPID key helpers round-trip', async () => {
  const p = await mod('platform.js');
  const bytes = Uint8Array.from({ length: 65 }, (_, i) => (i * 7 + 3) % 256);
  const b64 = p.bytesToUrlBase64(bytes.buffer);
  assert.ok(!/[+/=]/.test(b64));
  assert.deepEqual([...p.urlBase64ToBytes(b64)], [...bytes]);
  assert.equal(p.sameKey(bytes.buffer, b64), true);
  assert.equal(p.sameKey(null, b64), false);
  assert.equal(p.sameKey(bytes.buffer, 'AAAA'), false);
});

test('ntfyLink', async () => {
  const { ntfyLink } = await mod('platform.js');
  assert.equal(ntfyLink('https://ntfy.sh', 'wy-abc'), 'ntfy://ntfy.sh/wy-abc');
  assert.equal(ntfyLink('not a url', 'x'), '');
  assert.equal(ntfyLink('https://ntfy.sh', 'a/b?c'), 'ntfy://ntfy.sh/a%2Fb%3Fc');
});

test('format: untilShort style countdown helpers stay stable', async () => {
  const f = await mod('format.js');
  assert.equal(f.ago('2026-10-12T14:00:00Z', Date.parse('2026-10-12T14:04:00Z')), '4 min');
  assert.equal(f.pct(61.6), '62%');
});

test('platform: app pair link is an Android intent URL pinned to the app package', async () => {
  const p = await mod('platform.js');
  const ua = 'Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 Chrome/120.0 Mobile Safari/537.36';
  const tail = '#Intent;scheme=watchyard;package=io.github.mraj602.watchyard;end';
  assert.equal(p.appPairLink('https://hub.example.ts.net', 'sec/ret+1', '482-913', '', '', ua),
    'intent://pair?u=https%3A%2F%2Fhub.example.ts.net&s=sec%2Fret%2B1&c=482913' + tail);
  assert.equal(p.appPairLink('http://192.0.2.7:7777', 'abc', '', '', '', ua), 'intent://pair?u=http%3A%2F%2F192.0.2.7%3A7777&s=abc' + tail);
  assert.equal(p.appPairLink('https://h', '', '482913', '', '', ua), '');
  // the QR's keys.md 7 pins ride along, so the app can seal its keys into the claim
  assert.equal(p.appPairLink('https://h', 'abc', '482913', 'a2b3c4d5e6f7g2h3i4j5k6l7', 'k/ey+1', ua),
    'intent://pair?u=https%3A%2F%2Fh&s=abc&c=482913&h=a2b3c4d5e6f7g2h3i4j5k6l7&k=k%2Fey%2B1' + tail);
  // never the hijackable custom scheme, and nothing off Android
  assert.equal(p.appPairLink('https://h', 'abc', '482913', '', '', 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0)'), '');
  assert.equal(p.appPairLink('https://h', 'abc', '482913'), '');
  assert.equal(p.appPairLink('https://h', 'abc', '', '', '', ua, 'x;end'), '');
  assert.ok(!p.appPairLink('https://h', 'abc', '', '', '', ua).startsWith('watchyard://'));
});

test('format: one style for numbers and dates', async () => {
  const f = await mod('format.js');
  assert.equal(f.num(141816), '141,816');
  assert.equal(f.num(null), '–');
  assert.equal(f.plural(1240, 'record'), '1,240 records');
  assert.equal(f.plural(1, 'reply', 'replies'), '1 reply');
  assert.equal(f.compact(24_100_000_000), '24.1B');
  const now = new Date(2026, 9, 15, 20, 0).getTime(); // Thu 15 Oct 2026, local time
  assert.equal(f.clock(new Date(2026, 9, 15, 9, 5).toISOString(), now), '09:05');
  assert.equal(f.clock(new Date(2026, 9, 13, 9, 5).toISOString(), now), 'Tue 09:05');
  assert.equal(f.clock(new Date(2026, 10, 14, 9, 5).toISOString(), now), '14 Nov 09:05');
  assert.equal(f.dateTime(new Date(2026, 9, 15, 19, 51, 54).toISOString()), 'Thu 15 Oct, 19:51');
  assert.equal(f.day(new Date(2026, 10, 14, 20, 33).toISOString(), now), '14 Nov');
  assert.equal(f.day(new Date(2027, 0, 2).toISOString(), now), '2 Jan 2027');
  assert.equal(f.timeOfDay(new Date(2026, 9, 15, 19, 51, 54).toISOString()), '19:51:54');
  assert.equal(f.agoLabel(new Date(now - 5 * 36e5).toISOString(), now), '5 h ago');
  assert.equal(f.until(new Date(now + 2 * 36e5 + 10 * 6e4).toISOString(), now), 'in 2 h 10 min');
  assert.equal(f.projectOf({ cwd: '/home/u/watchyard/' }), 'watchyard');
  assert.equal(f.projectOf({}), '(no project)');
});

test('format: time zones for people', async () => {
  const f = await mod('format.js');
  assert.equal(f.tzLabel('UTC'), 'UTC');
  assert.equal(f.tzLabel('Etc/UTC'), 'UTC');
  assert.equal(f.tzLabel('Asia/Kolkata'), 'Kolkata (GMT+5:30)');
  assert.equal(f.tzLabel(''), '');
  assert.equal(f.sameTz('UTC', 'Etc/UTC'), true);
  assert.equal(f.sameTz('UTC', 'Asia/Kolkata'), false);
  assert.equal(f.sameTz('', 'Asia/Kolkata'), true);
});

test('live: old done, ended and bare imported sessions are not open', async () => {
  const L = await mod('components/live.js');
  const now = Date.parse('2026-10-15T12:00:00Z');
  const at = (h) => new Date(now - h * 36e5).toISOString();
  const s = (x) => ({ project: 'p', state: 'done', last_event_at: at(1), ...x });
  assert.equal(L.isLive(s({}), now), true);
  assert.equal(L.isLive(s({ state: 'working', last_event_at: at(40) }), now), true);
  assert.equal(L.isLive(s({ last_event_at: at(13) }), now), false);
  assert.equal(L.isLive(s({ stale: true }), now), true);
  // A stale working session is not open (the hub's dormant), however its state reads.
  assert.equal(L.isLive(s({ state: 'working', stale: true, last_event_at: at(13) }), now), false);
  // activity_at wins: a listed but idle session whose turn ended 3 days ago is not open, and
  // a done one with a recent state change is.
  assert.equal(L.isLive(s({ last_event_at: null, state_since: at(1), activity_at: at(72) }), now), false);
  assert.equal(L.isLive(s({ last_event_at: at(40), activity_at: at(2) }), now), true);
  assert.equal(L.isLive(s({ state: 'ended' }), now), false);
  assert.equal(L.isLive(s({ state: 'gone' }), now), false);
  assert.equal(L.isLive(s({ state: 'asked', needs_you: true, last_event_at: at(30) }), now), true);
  const bare = { state: 'done', last_event_at: at(1) };
  assert.equal(L.isHistory(bare), true);
  assert.equal(L.isLive(bare, now), false);
  assert.equal(L.isHistory({ ...bare, cwd: '/x' }), false);
  assert.equal(L.isHistory({ state: 'working' }), false);
  const c = L.liveCounts([s({ machine: 'm1', seat: 'a' }), s({ machine: 'm1', seat: 'b' }), s({ machine: 'm2', state: 'ended' })], now);
  assert.equal(c.total, 2);
  assert.equal(c.machine.get('m1'), 2);
  assert.equal(c.machine.get('m2'), undefined);
  assert.equal(L.liveCounts(null), null);
});

test('seat names: short, and told apart when they collide', async () => {
  const S = await mod('components/seat.js');
  const seats = [
    { id: 'a', email: 'alex.kim@work.example' },
    { id: 'b', email: 'alex.kim@mail.example' },
    { id: 'c', email: 'akim@mail.example' },
    { id: 'd', label: 'work', email: 'w@x.io' },
  ];
  const names = S.displayNames(seats);
  assert.equal(names.get('c'), 'akim');
  assert.equal(names.get('d'), 'work');
  assert.notEqual(names.get('a'), names.get('b'));
  assert.match(names.get('a'), /work/);
  assert.match(names.get('b'), /mail/);
});

test('targets: who can get a notification', async () => {
  const T = await mod('components/targets.js');
  const devs = [
    { id: 'd1', name: 'Phone', targets: [{ id: 't1', kind: 'link', status: 'ok', enabled: true }] },
    { id: 'd2', name: 'iPad', targets: [{ id: 't2', kind: 'webpush', status: 'gone', enabled: true }] },
    { id: 'd3', name: 'Old', revoked_at: '2026-10-01T00:00:00Z', targets: [{ id: 't3', kind: 'webpush', status: 'ok' }] },
    { id: 'd4', name: 'Laptop', targets: [] },
  ];
  assert.deepEqual(T.notifiable(devs).map((x) => x.device.id), ['d1']);
  assert.equal(T.appPaired(devs), true);
  assert.equal(T.appPaired(devs.slice(1)), false);
  // A machine paired by code keeps a link too, but it is not the Android app.
  assert.equal(T.appPaired([{ id: 'm1', kind: 'machine', link: { connected: false }, targets: [] }]), false);
  assert.equal(T.appPaired([{ id: 'p1', kind: 'phone', link: { connected: true }, targets: [] }]), true);
  assert.equal(T.notifiable(null).length, 0);
  assert.equal(T.targetKindText({ kind: 'link' }), 'Android app');
  assert.equal(T.targetKindText({ kind: 'webpush' }), 'browser notifications');
});

test('platform: install copy per browser', async () => {
  const { installHint } = await mod('platform.js');
  assert.match(installHint(UA.linuxChrome), /install icon/);
  assert.doesNotMatch(installHint(UA.linuxChrome), /Dock/);
  assert.match(installHint(UA.mac), /Add to Dock/);
  assert.match(installHint(UA.android), /Install app/);
  assert.match(installHint(UA.iphone, { ios: true }), /Add to Home Screen/);
  assert.match(installHint('Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0'), /Firefox/);
  assert.match(installHint(UA.android, { standalone: true }), /^Installed/);
});

test('autoApplyOk: a waiting build applies by itself only when that costs nothing', async () => {
  const { autoApplyOk } = await mod('components/update-policy.js');
  const base = { waiting: true, editing: false, hidden: false, pageAgeMs: 2000, sinceLastAutoMs: null };
  assert.equal(autoApplyOk(base), true, 'just started');
  assert.equal(autoApplyOk({ ...base, pageAgeMs: 10 * 60e3 }), false, 'in use: the banner asks');
  assert.equal(autoApplyOk({ ...base, pageAgeMs: 10 * 60e3, hidden: true }), true, 'in the background');
  assert.equal(autoApplyOk({ ...base, waiting: false }), false, 'nothing waiting');
  assert.equal(autoApplyOk({ ...base, editing: true }), false, 'a field has focus');
  assert.equal(autoApplyOk({ ...base, sinceLastAutoMs: 5000 }), false, 'no reload loop');
  assert.equal(autoApplyOk({ ...base, sinceLastAutoMs: 120e3 }), true);
});

test('autoApplyOk: an open dialog or an unsaved form holds the reload back, even in the background', async () => {
  const { autoApplyOk } = await mod('components/update-policy.js');
  const base = { waiting: true, editing: false, hidden: true, pageAgeMs: 10 * 60e3, sinceLastAutoMs: null };
  assert.equal(autoApplyOk({ ...base, pending: false }), true, 'nothing pending');
  assert.equal(autoApplyOk({ ...base, pending: true }), false, 'pending, hidden');
  assert.equal(autoApplyOk({ ...base, pending: true, hidden: false, pageAgeMs: 1000 }), false, 'pending, fresh page');
});

// A fake document: listeners by type, and whether a modal is open.
function fakeDoc() {
  const listeners = {};
  const doc = {
    modals: 0,
    addEventListener: (t, fn) => { (listeners[t] = listeners[t] || []).push(fn); },
    querySelector: (sel) => (doc.modals && /dialog\[open\]/.test(sel) ? {} : null),
    fire: (t, target) => (listeners[t] || []).forEach((fn) => fn({ target })),
  };
  return doc;
}
const field = (form, attrs = {}) => ({ form, hasAttribute: (a) => a in attrs });

test('createPending: a dialog or modal is pending work', async () => {
  const { createPending } = await mod('components/pending.js');
  const doc = fakeDoc();
  const p = createPending(doc);
  assert.equal(p.reason(), null);
  assert.equal(p.pending(), false);
  doc.modals = 1;
  assert.equal(p.reason(), 'modal');
  doc.modals = 0;
  assert.equal(p.reason(), null, 'closed again');
});

test('createPending: an edited form is pending until submit or reset, a lone field is not', async () => {
  const { createPending } = await mod('components/pending.js');
  const doc = fakeDoc();
  const p = createPending(doc);
  const form = { isConnected: true };
  doc.fire('input', field(null));
  assert.equal(p.pending(), false, 'a search box outside a form');
  doc.fire('input', field(form, { 'data-nodirty': '' }));
  assert.equal(p.pending(), false, 'opted out');
  doc.fire('input', field(form));
  assert.equal(p.reason(), 'unsaved');
  doc.fire('submit', form);
  assert.equal(p.pending(), false, 'submitted');
  doc.fire('change', field(form));
  assert.equal(p.pending(), true);
  doc.fire('reset', form);
  assert.equal(p.pending(), false, 'reset');
  doc.fire('input', field(form));
  form.isConnected = false;
  assert.equal(p.pending(), false, 'the view replaced the form');
});

test('pwa.js reloads by itself only through the pending guard', () => {
  const src = fs.readFileSync(path.join(__dirname, 'pwa.js'), 'utf8');
  const others = src.split('\n').filter((l) => !l.startsWith('function reloadNow')).join('\n');
  assert.doesNotMatch(others, /location\.reload\(\)/, 'every reload goes through reloadNow');
  assert.match(src, /pending: guard\.pending\(\)/, 'applyIfIdle asks the guard');
  assert.match(src, /if \(was && !reloading\) reloadWhenIdle\(\)/, 'controllerchange waits for the guard');
  const app = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
  assert.match(app, /'Update ready'/);
});
