// Headless browser smoke test for the web app. Run by TestBrowserSmoke (webui/smoke),
// gated by WY_WEBUI_BROWSER=1. Needs puppeteer-core (install it in a temp dir and point
// NODE_PATH at its node_modules) and a Chrome binary (CHROME, default /usr/bin/google-chrome).
//
// Env: BASE_URL (mock hub), TOKEN (board login token), PAIR_SECRET, PAIR_CODE, OWNER_SECRET,
//      OUT_DIR (screenshots).
//
// Per size (1440 and 390 px wide):
//   board login, every screen, live updates, keyboard, the sessions search
//   a phone claim by QR secret, the phone screens, Seen from the row menu, swipe Snooze
//   an owner sign-in, Alerts/Settings/Devices and the Add phone dialog
//   Switches: the log with its strip, the filters (machine, account, by hand, failed, dates), a row's session and its move path
//   Settings → Peers as the owner (grid, pause/resume, widen, audit) and as a paired phone (narrow only)
//   Settings → Keys: a paired phone reads it; the owner compares and confirms a pending owner key, then sees Certify
//   Resume thread: absent for a paired phone on the unresumed stop; the owner clicks it, sees the toast and the button go
// Once (phone viewport, Android user agent unless noted): manifest and installability,
// service worker control and caches, offline banner, update banner (and its deferral while a dialog is open), push display, push
// registration (subscribe stubbed), iPhone pairing path (no claim, digits shown).
// Any console error, failed request or unexpected HTTP error fails the run.

const puppeteer = require('puppeteer-core');
const path = require('path');
const fs = require('fs');

const BASE = process.env.BASE_URL;
const TOKEN = process.env.TOKEN;
const SECRET = process.env.PAIR_SECRET;
const CODE = process.env.PAIR_CODE;
const OWNER = process.env.OWNER_SECRET;
const OUT = process.env.OUT_DIR;
const CHROME = process.env.CHROME || '/usr/bin/google-chrome';
const SIZES = [['desk', 1440, 900], ['phone', 390, 844]];
const ANDROID = 'Mozilla/5.0 (Linux; Android 14; Phone X) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36';
const IPHONE = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1';
const ASKED = 'c41e0000-0000-4000-8000-000000000003';
const PERM = 'c41e0000-0000-4000-8000-000000000004';

const BOARD_PAGES = [
  ['now', '#/', '.needs-card'],
  ['sessions', '#/sessions', '.sessions-table'],
  ['sessions-needs', '#/sessions?filter=needs_you', '.sessions-table'],
  ['sessions-machine', '#/sessions?by=machine', '.sessions-table'],
  ['session', '#/sessions/' + ASKED, '.timeline'],
  ['accounts', '#/accounts', '.acct'],
  ['machines', '#/machines', '.machine'],
];

const problems = [];
const fail = (m) => { problems.push(m); console.error('FAIL', m); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let browser;

async function newPage(ctx, sizeName, w, hgt, opts = {}) {
  const page = await ctx.newPage();
  const phone = w < 600;
  await page.setViewport({ width: w, height: hgt, deviceScaleFactor: 1, isMobile: phone && opts.mobile !== false, hasTouch: phone });
  if (opts.ua) await page.setUserAgent(opts.ua);
  const tag = (m) => `[${sizeName}${opts.label ? ' ' + opts.label : ''}] ${m}`;
  page.tag = tag;
  page.on('console', (msg) => {
    const t = msg.text() + (msg.location().url || '');
    if (msg.type() === 'error' && !/ERR_|401|409|Failed to load resource: the server responded/.test(t)) fail(tag(`console: ${msg.text()}`));
  });
  page.on('pageerror', (e) => fail(tag(`pageerror: ${e.message}`)));
  await page.setRequestInterception(true);
  page.on('request', (req) => (/^https?:/.test(req.url()) && !req.url().startsWith(BASE) ? (fail(`request to another origin: ${req.url()}`), req.abort()) : req.continue()));
  page.on('requestfailed', (req) => {
    if (page.expectOffline) return;
    if (!/\/v1\/stream|session\/login/.test(req.url()) && !(req.failure() && /ABORTED/.test(req.failure().errorText))) {
      fail(tag(`request failed: ${req.method()} ${req.url()} ${req.failure() && req.failure().errorText}`));
    }
  });
  page.on('response', (res) => {
    if (res.status() >= 400 && !/\/v1\/(overview|me)$/.test(res.url()) && !/session\/login|pair\/claim|session\/owner|devices\/self|\/v1\/devices$/.test(res.url())) {
      fail(tag(`HTTP ${res.status()} ${res.url()}`));
    }
  });
  return page;
}

async function shot(page, name) {
  const vp = page.viewport();
  if (vp.width >= 600) { await page.screenshot({ path: path.join(OUT, `${name}.png`), fullPage: true }); return; }
  // Phone: grow the viewport to the page height so the fixed tab bar sits at the bottom
  // instead of floating mid-page in a full-page capture.
  const h = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewport({ ...vp, height: Math.min(Math.max(h, vp.height), 2600) });
  await sleep(120);
  await page.screenshot({ path: path.join(OUT, `${name}.png`) });
  await page.setViewport(vp);
}

async function visit(page, sizeName, name, hash, sel) {
  await page.evaluate((h) => { location.hash = h; }, hash);
  try { await page.waitForSelector(sel, { timeout: 6000 }); } catch (e) { fail(page.tag(`${name}: ${sel} never appeared`)); }
  await sleep(350);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  if (overflow > 1) fail(page.tag(`${name}: page scrolls sideways by ${overflow}px`));
  await shot(page, `${sizeName}-${name}`);
}

async function checkSidebar(page, sizeName) {
  // The sidebar must run the full height of the page .
  const r = await page.evaluate(() => {
    const nav = document.getElementById('nav').getBoundingClientRect();
    return { nav: Math.round(nav.height), page: document.documentElement.scrollHeight };
  });
  if (r.nav < r.page - 1) fail(page.tag(`sidebar is ${r.nav}px tall, page is ${r.page}px`));
}

async function resetHub() { await fetch(BASE + '/_dev/reset', { method: 'POST' }); }

// ---- not found, imported history, open sessions, live badge ----------

async function auditChecks(page, sizeName) {
  const t = (m) => page.tag(`audit fixes: ${m}`);
  const text = (sel) => page.$eval(sel, (e) => e.textContent).catch(() => '');
  // G6: an unknown route says so.
  await page.evaluate(() => { location.hash = '#/nonsense/page'; });
  try { await page.waitForSelector('[data-key=not-found]', { timeout: 4000 }); } catch (e) { fail(t('unknown route shows no "not found"')); }
  // G14: the Now title.
  await page.evaluate(() => { location.hash = '#/'; });
  await page.waitForSelector('.page-now');
  if (!/^\(\d+\) Now · Watchyard$/.test(await page.title())) fail(t(`Now title is "${await page.title()}"`));
  // G1/G3: imported history folds into one line, and Open leaves it out.
  await fetch(BASE + '/_dev/scenario', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ names: ['history'] }) });
  await page.evaluate(() => { location.hash = '#/sessions?filter=ended'; });
  try { await page.waitForSelector('[data-key=history-toggle]', { timeout: 6000 }); } catch (e) { fail(t('ended sessions show no folded history')); }
  await page.waitForFunction(() => /24 older sessions/.test(document.querySelector('[data-key=history]')?.textContent || ''), { timeout: 6000 }).catch(() => {});
  if (!/24 older sessions/.test(await text('[data-key=history]'))) fail(t(`history line: ${await text('[data-key=history]')}`));
  // The scenario's stream updates can redraw the list under a click, so click the current
  // toggle in the page and retry until the history opens.
  const opened = await page.waitForFunction(() => {
    if (document.querySelector('[data-key=history] .sessions-table')) return true;
    document.querySelector('[data-key=history-toggle]')?.click();
    return false;
  }, { timeout: 6000, polling: 500 }).then(() => true, () => false);
  if (!opened) fail(t('Show did not open the history'));
  await shot(page, `${sizeName}-sessions-history`);
  await page.evaluate(() => { location.hash = '#/sessions'; });
  await page.waitForSelector('[data-key=f-open][aria-pressed=true]');
  if (await page.$('[data-key=history]')) fail(t('Open shows imported history'));
  // 10: the fixtures' stale working session is not open (the hub's dormant rule, states.md 5).
  if (!/Open 10/.test(await text('[data-key=f-open]'))) fail(t(`Open chip: ${await text('[data-key=f-open]')}`));
  // G4: no internal detail code in the last line.
  if (/repaired|permission_prompt|rate_limit/.test(await text('.sessions-table'))) fail(t('an internal detail code shows in the table'));
  await resetHub();
  await page.waitForFunction(() => !document.querySelector('[data-key=history]'), { timeout: 6000 }).catch(() => {});
}

// ---- sessions search (pwa.md 3.0b) -----------------------------------------------------------

async function searchChecks(page, sizeName) {
  const t = (m) => page.tag(`search: ${m}`);
  const text = (sel) => page.$eval(sel, (e) => e.textContent).catch(() => '');
  const box = '[data-key=session-search]';
  const rows = () => page.$$eval('.sessions-table tbody tr.srow .c-session b', (e) => e.map((x) => x.textContent));
  const setQ = async (q) => {
    await page.$eval(box, (e) => { e.value = ''; e.dispatchEvent(new Event('input', { bubbles: true })); });
    await page.focus(box);
    await page.keyboard.type(q);
  };
  await page.evaluate(() => { location.hash = '#/sessions'; });
  await page.waitForSelector(`${box}`);
  // Case and accents are ignored; the match is marked; the count says how many.
  await setQ('CAFE lumiere');
  await page.waitForFunction(() => document.querySelectorAll('.sessions-table tr.srow').length === 1, { timeout: 4000 }).catch(() => {});
  const one = await rows();
  if (one.length !== 1 || one[0] !== 'finance') fail(t(`"CAFE lumiere" shows ${JSON.stringify(one)}`));
  const marks = await page.$$eval('.sessions-table mark', (e) => e.map((x) => x.textContent));
  if (!marks.includes('Café') || !marks.includes('Lumière')) fail(t(`marks: ${JSON.stringify(marks)}`));
  if (!/^1 of \d+ sessions? matches$/.test(await text('[data-key=search-count]'))) fail(t(`count: ${await text('[data-key=search-count]')}`));
  if (!/Open 1\b/.test(await text('[data-key=f-open]'))) fail(t(`Open chip: ${await text('[data-key=f-open]')}`));
  // A redraw and a live update keep the very same box, its text, focus and caret.
  await page.$eval(box, (e) => { e.wyMark = 1; e.setSelectionRange(2, 4); });
  await page.evaluate(() => window.dispatchEvent(new Event('wy-rerender')));
  await page.evaluate(() => fetch('/_dev/promote', { method: 'POST' }));
  await sleep(1200);
  const kept = await page.$eval(box, (e) => ({ same: e.wyMark === 1, value: e.value, focused: document.activeElement === e, sel: [e.selectionStart, e.selectionEnd] }));
  if (!kept.same || kept.value !== 'CAFE lumiere' || !kept.focused || kept.sel.join() !== '2,4') fail(t(`a redraw did not keep the box: ${JSON.stringify(kept)}`));
  if ((await rows()).join() !== 'finance') fail(t('a redraw dropped the search'));
  // A term found only in a field the row does not show adds that field's line.
  await setQ('rapport');
  await page.waitForSelector('.search-extra', { timeout: 4000 }).catch(() => {});
  if (!/^Name: rapport-financier$/.test(await text('.search-extra'))) fail(t(`extra line: ${await text('.search-extra')}`));
  // Matches under another chip: one button searches them all, the query kept.
  await setQ('seo-audit');
  await page.waitForSelector('[data-key=search-all]', { timeout: 4000 }).catch(() => fail(t('no Search all sessions button')));
  if (!/No sessions here match “seo-audit”/.test(await text('[data-key=sessions-empty]'))) fail(t(`empty: ${await text('[data-key=sessions-empty]')}`));
  await shot(page, `${sizeName}-sessions-search-empty`);
  await page.click('[data-key=search-all]');
  await page.waitForFunction(() => /filter=all/.test(location.hash), { timeout: 4000 }).catch(() => {});
  await page.waitForFunction(() => document.querySelectorAll('.sessions-table tr.srow').length === 1, { timeout: 4000 }).catch(() => {});
  if ((await rows()).join() !== 'storefront' || (await page.$eval(box, (e) => e.value)) !== 'seo-audit') fail(t('Search all sessions lost the query or the row'));
  await setQ('lumi');
  await shot(page, `${sizeName}-sessions-search`);
  // Escape clears it; every session is back.
  await page.focus(box);
  await page.keyboard.press('Escape');
  await page.waitForFunction(() => !document.querySelector('[data-key=search-count]'), { timeout: 4000 }).catch(() => {});
  if ((await page.$eval(box, (e) => e.value)) !== '' || (await rows()).length < 5) fail(t('Escape did not clear the search'));
  await resetHub();
}

// ---- the board login flow  -------------------------------------------------

async function boardFlow(sizeName, w, hgt) {
  const ctx = await browser.createBrowserContext();
  const page = await newPage(ctx, sizeName, w, hgt, { ua: undefined, label: 'board', mobile: false });
  await page.goto(BASE + '/#/login', { waitUntil: 'networkidle2' });
  // "Sign in to this hub": an owner link, a code, the hub-computer fold-out (wy hub owner-link);
  // the token form is behind a disclosure (L1).
  await page.waitForSelector('.login-card [data-key=owner-link-form]');
  await page.waitForSelector('.login-card [data-key=to-pair]');
  await page.waitForSelector('.login-card [data-key=owner-cli] [data-key=owner-link-cmd]');
  // A pasted owner link for another hub is refused with the other origin named.
  await page.type('[data-key=owner-link-input]', 'https://other.example/#/owner?s=abcdefghijklmnopqrstuv');
  // A redraw (a data or PWA update) must not rebuild the sign-in page and drop what was typed.
  await page.evaluate(() => window.dispatchEvent(new Event('wy-rerender')));
  if (!(await page.$eval('[data-key=owner-link-input]', (e) => e.value))) fail(page.tag('a redraw dropped the typed owner link'));
  await page.click('[data-key=owner-link-open]');
  await page.waitForFunction(() => /not this hub/.test((document.querySelector('[data-key=owner-link-error]:not([hidden])') || {}).textContent || ''), { timeout: 10000 })
    .catch(() => fail(page.tag('a foreign owner link was not refused')));
  if (!(await page.$('[data-key=owner-link-error] a[href^="https://other.example/"]'))) fail(page.tag('the refusal has no link to the other hub'));
  await page.evaluate(() => { document.querySelector('[data-key=owner-link-input]').value = ''; });
  await shot(page, `${sizeName}-login`);
  await page.click('[data-key=token-login] summary');
  await page.waitForSelector('#token', { visible: true });
  await page.type('#token', 'wy_b_wrong');
  await page.click('.login-token button[type=submit]');
  await page.waitForSelector('.login-token .form-error:not([hidden])');
  await page.evaluate(() => { document.getElementById('token').value = ''; });
  await page.type('#token', TOKEN);
  // Typing clears the old error (L2).
  if (await page.$('.login-token .form-error:not([hidden])')) fail(page.tag('the login error stayed while the token was retyped'));
  await Promise.all([page.waitForSelector('#nav a[aria-current=page]'), page.click('.login-token button[type=submit]')]);
  for (const [name, hash, sel] of BOARD_PAGES) await visit(page, sizeName, name, hash, sel);
  await auditChecks(page, sizeName);
  await searchChecks(page, sizeName);
  for (const [name, hash, sel] of [['more', '#/more', '.more-list'], ['alerts-readonly', '#/more/alerts', '.log'], ['settings-readonly', '#/more/settings', '[data-key=build]'], ['devices-readonly', '#/more/devices', '.calm']]) {
    await visit(page, sizeName, name, hash, sel);
  }
  await analyticsChecks(page, sizeName, 'board');
  await switchesChecks(page, sizeName);
  // A plain board login has no Rename / Remove / Pull now, but sees reach, version and pull state.
  await visit(page, sizeName, 'machines-readonly', '#/machines', '.machine');
  if (await page.$('.machine-actions')) fail(page.tag('read-only session shows the machine actions'));
  if (!(await page.$('[data-key="reach-m_gpu"]'))) fail(page.tag('machine card has no reach chip'));
  // A plain board login cannot Seen/Snooze: no controls.
  await page.evaluate(() => { location.hash = '#/'; });
  await page.waitForSelector('.needs-card');
  if (await page.$('.alert-actions')) fail(page.tag('read-only session shows Seen/Snooze'));
  if (sizeName === 'desk') await checkSidebar(page, sizeName);

  // Live update: promote a working session on the mock; the needs-you count must grow.
  const before = await page.$$eval('.needs-card', (e) => e.length);
  await page.evaluate(() => fetch('/_dev/promote', { method: 'POST' }));
  try {
    await page.waitForFunction((n) => document.querySelectorAll('.needs-card').length > n, { timeout: 5000 }, before);
  } catch (e) { fail(page.tag('live update did not reach the page via /v1/stream')); }
  await shot(page, `${sizeName}-now-live`);
  await page.keyboard.press('Tab');
  const focused = await page.evaluate(() => document.activeElement && document.activeElement.tagName);
  if (!focused || focused === 'BODY') fail(page.tag('Tab did not move focus'));
  await ctx.close();
  await resetHub();
}

// ---- a paired phone ---------------------------------------------------------------------------

async function phoneFlow(sizeName, w, hgt) {
  const ctx = await browser.createBrowserContext();
  const page = await newPage(ctx, sizeName, w, hgt, { ua: ANDROID, label: 'phone' });
  await page.goto(`${BASE}/#/pair?s=${SECRET}&c=${CODE}`, { waitUntil: 'networkidle2' });
  try { await page.waitForSelector('[data-key=paired-as]', { timeout: 6000 }); } catch (e) { fail(page.tag('pairing: never showed "Paired as"')); }
  const addr = await page.evaluate(() => location.hash);
  if (/s=|c=/.test(addr)) fail(page.tag(`pairing left the secret in the address bar: ${addr}`));
  await shot(page, `${sizeName}-pair-done`);
  if (!(await page.cookies()).some((c) => c.name === 'wy_session')) fail(page.tag('claim did not set the wy_session cookie'));
  // The name is editable once paired (two Android phones both guess "Phone X · Chrome").
  const typed = `Phone X · Chrome (${sizeName})`;
  await page.$eval('[data-key=paired-name]', (e) => { e.value = ''; e.dispatchEvent(new Event('input')); });
  await page.type('[data-key=paired-name]', typed);
  await page.click('[data-key=save-name]');
  await page.waitForFunction(async (n) => (await (await fetch('/v1/devices/self')).json()).name === n, { timeout: 10000 }, typed)
    .catch(() => fail(page.tag('the Paired screen did not save the device name')));
  await page.waitForFunction((n) => (document.querySelector('[data-key=paired-as]') || {}).textContent === `Paired as ${n}`, { timeout: 4000 }, typed)
    .catch(() => fail(page.tag('"Paired as" did not follow the new name')));
  if (w < 600) {
    // Leaving before "Open the app" starts the app where asked, not back on #/pair.
    await page.evaluate(() => { location.hash = '#/sessions'; });
    try { await page.waitForSelector('.sessions-table', { timeout: 6000 }); } catch (e) { fail(page.tag('leaving the Paired screen bounced back to pairing')); }
    if (!/^#\/sessions/.test(await page.evaluate(() => location.hash))) fail(page.tag('not on #/sessions after leaving the Paired screen'));
    await page.evaluate(() => { location.hash = '#/'; });
  } else {
    await page.click('[data-key=pair-continue]');
  }
  await page.waitForSelector('.needs-card');
  const who = await page.evaluate(async () => (await (await fetch('/v1/me')).json()));
  if (who.owner || !who.device.scopes.includes('push')) fail(page.tag(`claimed device has wrong scopes: ${JSON.stringify(who.device.scopes)}`));

  await visit(page, sizeName, 'p-now', '#/', '.needs-card');
  await visit(page, sizeName, 'p-sessions', '#/sessions', '.sessions-table');
  await visit(page, sizeName, 'p-sessions-machine', '#/sessions?by=machine', '.sessions-table');
  await visit(page, sizeName, 'p-session', `#/sessions/${ASKED}?alert=al_pending`, '.session-actions');
  await visit(page, sizeName, 'p-accounts', '#/accounts?seat=seat_work', '.room');
  if (await page.$('.seat-grip')) fail(page.tag('a paired phone (not the owner) sees the drag grips on Accounts'));
  await visit(page, sizeName, 'p-more', '#/more', '.more-list');
  await analyticsChecks(page, sizeName, 'phone');
  await visit(page, sizeName, 'p-alerts', '#/more/alerts', '.log');
  await visit(page, sizeName, 'p-settings', '#/more/settings', '[data-key=notif-state]');

  // Rules on a phone edit this device's mute list.
  await page.evaluate(() => { location.hash = '#/more/alerts'; });
  await page.waitForSelector('[data-key=rule-limits]');
  await page.click('[data-key=rule-limits]');
  await page.waitForFunction(async () => ((await (await fetch('/v1/devices/self')).json()).prefs.mute || []).includes('limit_80'), { timeout: 4000 }).catch(() => fail(page.tag('rule switch did not save to prefs.mute')));

  // Seen from the row menu.
  await page.evaluate(() => { location.hash = '#/'; });
  await page.waitForSelector('.needs-card');
  if (w < 600) await page.click(`[data-key="menu-${PERM}"]`);
  await page.click(`[data-key="seen-${PERM}"]`);
  // A seen session leaves Needs you until its state changes (push.md 4, states.md 1).
  try { await page.waitForSelector(`.needs-card[data-session="${PERM}"]`, { hidden: true, timeout: 4000 }); } catch (e) { fail(page.tag('Seen did not take the row out of Needs you')); }
  await shot(page, `${sizeName}-p-now-seen`);

  // Snooze by swiping left (touch) or the button.
  if (w < 600) {
    const box = await (await page.$(`[data-session="${ASKED}"]`)).boundingBox();
    const cdp = await page.createCDPSession();
    const y = box.y + box.height / 2;
    const send = (type, x) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints: type === 'touchEnd' ? [] : [{ x, y }] });
    await send('touchStart', box.x + box.width - 30);
    for (let i = 1; i <= 8; i++) { await send('touchMove', box.x + box.width - 30 - i * 16); await sleep(16); }
    await shot(page, `${sizeName}-p-now-swipe`);
    await send('touchEnd', 0);
  } else {
    await page.click(`[data-key="snooze-${ASKED}"]`);
  }
  try { await page.waitForFunction((id) => /snoozed/.test((document.querySelector(`[data-session="${id}"]`) || {}).textContent || ''), { timeout: 4000 }, ASKED); } catch (e) { fail(page.tag('Snooze did not show "snoozed until"')); }
  await sleep(300);
  await shot(page, `${sizeName}-p-now-snoozed`);

  if (w < 600) {
    // Revoked elsewhere (here: a bare DELETE, as the owner's Devices page would): the hub ends
    // the stream, the board asks /v1/me at once and opens the pair screen well before its
    // 15 s poll.
    await page.evaluate(() => { location.hash = '#/'; });
    await page.waitForSelector('.needs-card');
    await page.evaluate(() => fetch('/v1/devices/self', { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: '{}' }));
    try { await page.waitForSelector('.pair-screen', { timeout: 4000 }); } catch (e) { fail(page.tag('a revoke did not sign the board out at once')); }
    await shot(page, `${sizeName}-pair`);
    await ctx.close();
    await resetHub();
    return;
  }
  // Unpair returns to the pair screen.
  await page.evaluate(() => { window.confirm = () => true; location.hash = '#/more/settings'; });
  await page.waitForSelector('[data-key=unpair]');
  await page.click('[data-key=unpair]');
  try { await page.waitForSelector('.pair-screen', { timeout: 5000 }); } catch (e) { fail(page.tag('Unpair did not open the pair screen')); }
  await shot(page, `${sizeName}-pair`);
  await ctx.close();
  await resetHub();
  // The desktop app's Hub view marks itself in the user agent (works signed out): no CLI on the
  // sign-in page; signed in, the sidebar says Hub settings.
  const appCtx = await browser.createBrowserContext();
  const app = await newPage(appCtx, sizeName, w, hgt, { label: 'in-app', mobile: false, ua: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Electron/37.10.3 Safari/537.36 WatchyardDesktop/0.6.0' });
  await app.goto(BASE + '/#/login', { waitUntil: 'networkidle2' });
  try { await app.waitForSelector('[data-key=login-app]', { timeout: 4000 }); } catch (e) { fail(app.tag('the app sign-in page is not the in-app one')); }
  if (/wy hub/.test(await app.evaluate(() => document.body.textContent || ''))) fail(app.tag('the app sign-in page names a CLI command'));
  const signedIn = await app.evaluate(async (t) => { const r = await fetch('/v1/session/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: t }) }); return r.status + ' ' + await r.text(); }, TOKEN);
  if (!/^2/.test(signedIn)) fail(app.tag('app sign-in: ' + signedIn));
  await app.evaluate(() => { location.hash = '#/'; });
  await app.reload({ waitUntil: 'networkidle2' }); // a hash change alone keeps the signed-out page
  await app.waitForFunction(() => ((document.querySelector('[data-key=nav-settings]') || {}).textContent || '') === 'Hub settings', { timeout: 6000 })
    .catch(async () => fail(app.tag('the app sidebar does not say Hub settings: ' + await app.evaluate(() => location.hash + ' | ' + ((document.querySelector('[data-key=nav-settings]') || {}).textContent) + ' | ' + navigator.userAgent.slice(-30)))));
  await appCtx.close();
  await resetHub();
}

// ---- the owner -------------------------------------------------------------------------------

async function ownerFlow(sizeName, w, hgt) {
  const ctx = await browser.createBrowserContext();
  const page = await newPage(ctx, sizeName, w, hgt, { label: 'owner', mobile: false });
  // An expired link made with --ttl 1m says 1 minute, not a fixed 10 (pairing.md 4).
  await page.goto(`${BASE}/#/owner?s=${process.env.EXPIRED_OWNER_SECRET}`, { waitUntil: 'networkidle2' });
  await page.waitForFunction(() => /expired.*it worked for 1 minute after it was made/.test(document.body.textContent || ''), { timeout: 4000 })
    .catch(() => fail(page.tag('expired owner link does not say its 1 minute lifetime')));
  await shot(page, `${sizeName}-o-expired`);
  await page.goto(`${BASE}/#/owner?s=${OWNER}`, { waitUntil: 'networkidle2' });
  // An owner link lands on Now with a toast.
  try { await page.waitForSelector('.page-now', { timeout: 6000 }); } catch (e) { fail(page.tag('owner link did not land on Now')); }
  if (!/signed in as the owner/.test(await page.evaluate(() => (document.getElementById('toasts') || {}).textContent || ''))) fail(page.tag('owner sign-in showed no toast'));
  if (/s=/.test(await page.evaluate(() => location.hash))) fail(page.tag('owner secret left in the address'));
  await visit(page, sizeName, 'o-settings', '#/more/settings', '[data-key=hub-address]');
  await visit(page, sizeName, 'o-alerts', '#/more/alerts', '[data-key=rule-asked]');
  await visit(page, sizeName, 'o-devices', '#/more/devices', '.dev-list');
  await accountsFlow(page, sizeName);
  await orderFlow(page, sizeName);
  await machinesFlow(page, sizeName);
  // Owner switches edit the global rule.
  await page.click('[data-key=nav-alerts], [data-key=nav-more]').catch(() => {});
  await page.evaluate(() => { location.hash = '#/more/alerts'; });
  await page.waitForSelector('[data-key=delay-asked]');
  await page.select('[data-key=delay-asked]', '120');
  await page.waitForFunction(async () => (await (await fetch('/v1/rules')).json()).rules.find((r) => r.id === 'rule_asked').delay_s === 120, { timeout: 4000 }).catch(() => fail(page.tag('owner delay change did not save')));
  // An override changes in place: Change, pick Wait 10 min, Save (PUT /v1/rules/{id}).
  await page.click('[data-key=edit-rule_o1]');
  await page.waitForSelector('[data-key=ov-rule_o1-delay]');
  await page.select('[data-key=ov-rule_o1-delay]', '600');
  await page.click('[data-key=save-rule_o1]');
  await page.waitForFunction(async () => (await (await fetch('/v1/rules')).json()).rules.find((r) => r.id === 'rule_o1').delay_s === 600, { timeout: 4000 }).catch(() => fail(page.tag('override change did not save')));
  await page.waitForFunction(() => /after 10 min/.test((document.querySelector('[data-key=ov-rule_o1]') || {}).textContent || ''), { timeout: 4000 }).catch(() => fail(page.tag('override row did not show the new wait')));
  // Make another device owner: a one-time owner link with its QR and a copy button (pairing.md 4.1).
  await page.evaluate(() => { location.hash = '#/more/devices'; });
  await page.waitForSelector('[data-key=make-owner]');
  await page.click('[data-key=make-owner]');
  try {
    await page.waitForFunction(() => /#\/owner\?s=/.test((document.querySelector('dialog[open] [data-key=owner-link-url]') || {}).textContent || ''), { timeout: 4000 });
    await page.waitForSelector('dialog[open] svg.qr rect', { timeout: 4000 });
    await page.waitForSelector('dialog[open] [data-key=copy-owner-link]', { timeout: 4000 });
  } catch (e) { fail(page.tag('Make another device owner did not show the link, its QR and Copy')); }
  await shot(page, `${sizeName}-o-make-owner`);
  await page.click('dialog[open] [data-key=pair-cancel]').catch(() => fail(page.tag('owner link dialog has no Cancel')));
  await page.waitForFunction(() => !document.querySelector('dialog[open]'), { timeout: 4000 }).catch(() => fail(page.tag('owner link dialog did not close')));
  // Add phone dialog.
  await page.waitForSelector('[data-key=add-phone]');
  await page.click('[data-key=add-phone]');
  try { await page.waitForSelector('dialog[open] svg.qr rect', { timeout: 4000 }); } catch (e) { fail(page.tag('Add phone did not draw a QR code')); }
  await shot(page, `${sizeName}-o-add-phone`);
  // Another browser claims the code: the dialog flips to Paired.
  await fetch(BASE + '/v1/pair/claim', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ secret: SECRET, name: 'Test phone' }) });
  try { await page.waitForSelector('[data-key=pair-claimed]', { timeout: 6000 }); } catch (e) { fail(page.tag('dialog never showed Paired after the claim')); }
  await ctx.close();
  await resetHub();
}

// ---- the order of the accounts (owner): drag, keyboard, another device, a refusal ---------------

const sideShown = (page) => page.evaluate(() => { const n = document.querySelector('.nav-accounts'); return !!n && n.offsetParent !== null; });
const sideOrder = (page) => page.$$eval('.nav-accounts [data-key^="nav-seat-"]', (els) => els.map((a) => a.dataset.key.replace('nav-seat-', '')));
const cardOrder = (page) => page.$$eval('.acct-list > .seat-card', (els) => els.map((a) => a.id.replace('seat-', '')));
const hubOrder = (page) => page.evaluate(async () => (await (await fetch('/v1/accounts')).json()).accounts.map((a) => a.id));
// The board moves a card at once and tells the hub afterwards (PUT /v1/seats/order), so the hub
// is asked until it reports the wanted order; a single read right after the DOM changed races
// the request. The condition is the real one, not a sleep.
const waitHub = (page, want, what) => page.waitForFunction(async (w2) => {
  const ids = (await (await fetch('/v1/accounts', { cache: 'no-store' })).json()).accounts.map((a) => a.id);
  return JSON.stringify(ids) === JSON.stringify(w2);
}, { timeout: 5000, polling: 100 }, want).catch(async () => fail(page.tag(`order: ${what} (hub has ${await hubOrder(page)})`)));
const sameList = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const waitCards = (page, want, what) => page.waitForFunction((w2) => JSON.stringify([...document.querySelectorAll('.acct-list > .seat-card')].map((a) => a.id.replace('seat-', ''))) === JSON.stringify(w2), { timeout: 4000 }, want)
  .catch(() => fail(page.tag(`order: ${what}`)));
const waitSide = async (page, want, what, timeout = 4000) => !(await sideShown(page)) || page.waitForFunction((w2) => JSON.stringify([...document.querySelectorAll('.nav-accounts [data-key^="nav-seat-"]')].map((a) => a.dataset.key.replace('nav-seat-', ''))) === JSON.stringify(w2), { timeout }, want)
  .catch(() => fail(page.tag(`order: ${what}`)));

// ---- the Accounts list: compact rows, expand in place, expand all, signed out, most room ----------
async function accountsFlow(page, sizeName) {
  const t = (m) => page.tag(`accounts: ${m}`);
  await page.evaluate(() => { try { localStorage.removeItem('wy-accounts-open'); } catch (_) {} });
  await visit(page, sizeName, 'o-accounts-collapsed', '#/accounts', '.acct');
  const rows = await page.$$eval('.acct', (els) => els.map((e) => ({ id: e.id, h: Math.round(e.getBoundingClientRect().height), meters: e.querySelectorAll('.acct-row .meter').length,
    win: (e.querySelector('.acct-win') || {}).textContent || '', pct: (e.querySelector('.acct-pct') || {}).textContent || '', details: e.querySelectorAll('.acct-detail').length })));
  if (rows.length !== 4) fail(t(`${rows.length} rows, want 4`));
  for (const r of rows) {
    if (r.details) fail(t(`${r.id} starts open`));
    if (r.meters !== 1) fail(t(`${r.id} has ${r.meters} bars in its row`));
    if (!/^(5 h|7 d)$/.test(r.win) || !/^\d+%$/.test(r.pct)) fail(t(`${r.id} bar label "${r.win}" "${r.pct}"`));
    if (r.h > (sizeName === 'phone' ? 150 : 80)) fail(t(`${r.id} row is ${r.h}px tall`));
  }
  const clientRow = rows.find((r) => r.id === 'seat-seat_client');
  if (!clientRow || clientRow.win !== '5 h' || clientRow.pct !== '100%') fail(t(`client row ${JSON.stringify(clientRow)}`));
  const hot = await page.$eval('#seat-seat_client .acct-meter', (e) => e.classList.contains('hot'));
  if (!hot) fail(t('the bar of a 100% account is not red'));
  const reset = await page.$eval('[data-key=reset-seat_personal]', (e) => e.textContent);
  if (!/^resets \d{2}:\d{2}$|^resets \w{3} \d{2}:\d{2}$|^resets \d+ \w{3} \d{2}:\d{2}$/.test(reset)) fail(t(`reset text "${reset}"`));
  const so = await page.$eval('[data-key=issue-seat_research]', (e) => e.textContent);
  if (!/Signed out on gpu-box/.test(so)) fail(t(`signed-out line "${so}"`));
  const quiet = await page.$eval('[data-key=issue-seat_work]', (e) => e.textContent);
  if (quiet) fail(t(`a calm account shows "${quiet}"`));
  const room = await page.$eval('[data-key=most-room]', (e) => e.textContent);
  if (room !== 'Most room right now: research (5 h 8% · 7 d 12%)') fail(t(`most room says "${room}"`));
  if (await page.$('.page > p.muted.small:not([data-key])')) fail(t('an explanatory paragraph is back'));
  const note = await page.$eval('[data-key=accounts-note]', (e) => e.textContent);
  if (note !== 'The bar shows the tighter of the 5 h and 7 d limits. Select an account for everything else.') fail(t(`note "${note}"`));
  await shot(page, `${sizeName}-o-accounts-collapsed`);

  // Open one row with its chevron: every window with "resets in ... (clock)".
  await page.click('[data-key=seat-toggle-seat_personal]');
  await page.waitForSelector('#seat-seat_personal .acct-detail');
  if ((await page.$eval('[data-key=seat-toggle-seat_personal]', (e) => e.getAttribute('aria-expanded'))) !== 'true') fail(t('aria-expanded not true'));
  const det = await page.$eval('#seat-seat_personal .acct-detail', (e) => e.textContent);
  if ((det.match(/resets in [^()]+\(/g) || []).length < 4) fail(t(`detail shows few reset times: ${det.slice(0, 300)}`));
  if (!/Claude Code signed in/.test(det) || !/Edit label and colour/.test(det) || !/claude\.ai usage read/.test(det)) fail(t('detail lacks sign-in, edit or usage-read lines'));
  // It stays open after a reload (localStorage), and a click on the row closes it.
  await page.reload({ waitUntil: 'networkidle2' });
  await page.waitForSelector('.acct');
  if (!(await page.$('#seat-seat_personal .acct-detail'))) fail(t('the open row was forgotten after a reload'));
  await page.click('#seat-seat_personal .acct-who');
  await page.waitForFunction(() => !document.querySelector('#seat-seat_personal .acct-detail'), { timeout: 3000 }).catch(() => fail(t('clicking the row did not close it')));
  // The owner's edit form is inside the open row.
  await page.click('[data-key=seat-toggle-seat_research]');
  await page.waitForSelector('[data-key=seat-edit-seat_research]');
  const sout = await page.$eval('[data-key=where-seat_research]', (e) => e.textContent);
  if (!/gpu-box.*signed out/.test(sout)) fail(t(`where signed in: "${sout}"`));
  await page.click('[data-key=seat-toggle-seat_research]');

  // Expand all / Collapse all.
  await page.click('[data-key=accounts-expand-all]');
  await page.waitForFunction(() => document.querySelectorAll('.acct-detail').length === 4, { timeout: 3000 }).catch(() => fail(t('Expand all did not open every row')));
  if ((await page.$eval('[data-key=accounts-expand-all]', (e) => e.textContent)) !== 'Collapse all') fail(t('button does not say Collapse all'));
  await shot(page, `${sizeName}-o-accounts-expanded`);
  await page.click('[data-key=accounts-expand-all]');
  await page.waitForFunction(() => document.querySelectorAll('.acct-detail').length === 0, { timeout: 3000 }).catch(() => fail(t('Collapse all did not close every row')));
  await usageFlow(page, sizeName);
}

// "Usage and the picker" and the mode control (pwa.md 3.0a), with the owner's account (seat_personal).
async function usageFlow(page, sizeName) {
  const t = (m) => page.tag(`usage: ${m}`);
  const text = (sel) => page.$eval(sel, (e) => e.textContent).catch(() => null);
  // A choice redraws the row twice (at once, then with the hub's answer): click once it is quiet.
  const choose = async (mode) => {
    await page.waitForFunction(() => !document.querySelector('fieldset.mode-pick[disabled]'), { timeout: 4000 });
    await sleep(150);
    await page.evaluate((m) => document.querySelector(`label[for=mode-seat_personal-${m}]`).click(), mode);
  };
  await page.evaluate(() => { try { localStorage.removeItem('wy-accounts-open'); } catch (_) {} });
  await visit(page, sizeName, 'o-usage-closed', '#/accounts?seat=seat_personal', '[data-key=mode-seat_personal]');

  // The choice: protected is on, each option has its definition right under it.
  const sect = await text('[data-key=mode-seat_personal]');
  if (!/How should the picker use this account\?/.test(sect)) fail(t('no question'));
  for (const d of ['Kept for you. Used last: new sessions go here only when no aggressive account can take them, or for room that would expire unused anyway.',
    'Use it up. Sessions go here until 98% of the week or 5 hours.', 'Not chosen yet; treated as aggressive.']) if (!sect.includes(d)) fail(t(`definition missing: ${d}`));
  if (!(await page.$eval('[data-key=mode-opt-seat_personal-protected]', (e) => e.checked))) fail(t('protected is not selected'));
  if (await page.$('[data-key=mode-unset-seat_personal]')) fail(t('a protected account shows the not-chosen line'));
  // A change saves at once and survives a reload; another change puts it back.
  await choose('aggressive');
  await page.waitForFunction(async () => (await (await fetch('/v1/accounts', { cache: 'no-store' })).json()).accounts.find((a) => a.id === 'seat_personal').mode === 'aggressive', { timeout: 4000 })
    .catch(() => fail(t('choosing Aggressive did not reach the hub')));
  await page.waitForSelector('[data-key=mode-opt-seat_personal-aggressive]:checked', { timeout: 3000 }).catch(() => fail(t('Aggressive is not selected after saving')));
  await choose('none');
  await page.waitForSelector('[data-key=mode-unset-seat_personal]', { timeout: 4000 }).catch(() => fail(t('Not chosen shows no highlighted line')));
  await choose('protected');
  await page.waitForFunction(() => !document.querySelector('[data-key=mode-unset-seat_personal]'), { timeout: 4000 }).catch(() => fail(t('the highlighted line stayed after choosing Protected')));
  if (!(await page.$eval('[data-key=mode-opt-seat_personal-protected]', (e) => e.checked))) fail(t('protected is not selected again'));
  // A refusal puts the choice back and says why.
  await page.evaluate(() => {
    window.__fetch = window.fetch;
    window.fetch = (u, o) => (o && o.method === 'PATCH' ? Promise.resolve(new Response(JSON.stringify({ error: 'the hub said no', code: 'bad_request' }), { status: 400, headers: { 'Content-Type': 'application/json' } })) : window.__fetch(u, o));
  });
  await choose('aggressive');
  await page.waitForSelector('[data-key=mode-error-seat_personal]', { timeout: 4000 }).catch(() => fail(t('a refused change shows no reason')));
  if (!(await page.$eval('[data-key=mode-opt-seat_personal-protected]', (e) => e.checked))) fail(t('a refused change did not restore the old choice'));
  if (!/the hub said no/.test((await text('[data-key=mode-error-seat_personal]')) || '')) fail(t('the refusal reason is not shown'));
  await page.evaluate(() => { window.fetch = window.__fetch; });
  await choose('aggressive'); // a good save clears the reason
  await page.waitForFunction(() => !document.querySelector('[data-key=mode-error-seat_personal]'), { timeout: 4000 }).catch(() => fail(t('the reason stayed after a good save')));
  await choose('protected');
  await page.waitForSelector('[data-key=mode-opt-seat_personal-protected]:checked', { timeout: 3000 }).catch(() => fail(t('protected is not back')));

  // The work account was never chosen: the highlighted line and the read of its local override.
  // Scrolled to the middle first: with the five mode choices open above it, the row lands under
  // the phone's fixed tab bar, which would take the click.
  const workToggle = async () => {
    await page.$eval('[data-key=seat-toggle-seat_work]', (e) => e.scrollIntoView({ block: 'center' }));
    await page.click('[data-key=seat-toggle-seat_work]');
  };
  await workToggle();
  await page.waitForSelector('[data-key=mode-unset-seat_work]', { timeout: 3000 }).catch(() => fail(t('an unchosen account has no highlighted line')));
  await workToggle();

  // Usage and the picker, opened.
  await page.click('[data-key=rel-seat_personal] > summary');
  await page.waitForSelector('[data-key=rel-tiles-seat_personal]', { timeout: 5000 }).catch(() => fail(t('the tiles never appeared')));
  await sleep(200);
  const body = await text('[data-key=rel-seat_personal] .rel-body') || '';
  const want = ['Can take a new session: 3rd in line', '3rd in line: 2 other accounts get a new session before this one; they take turns.',
    '5-hour 1% (stops at 45%)', 'Week 29%, the even pace allows 52% today', 'New sessions in the last hour: 0 (at most 2)',
    '6.8% of the 5-hour limit = 1% of the week', '≈ 6.8 full 5-hour windows use up the week', 'One full 5-hour window ≈ 15% of the week', 'measured over 7 days',
    'Each new session is set aside 8% of the 5-hour limit until its real use shows up (default; measured after 20 sessions, 1 so far)',
    'This week 1% of the 5-hour limit bought about $1.67 of Opus 5.5 work at API prices. Last week: not enough data yet.',
    'A change of more than 20% on most accounts raises an alert.', 'Sonnet 5.5: not used in the last 2 weeks.', 'Haiku 5.5: not used in the last 2 weeks.',
    'Hover or tap a day to see its value.', 'Kept for you.'];
  for (const w of want) if (!body.includes(w)) fail(t(`missing "${w}"`));
  if ((body.match(/A change of more than 20%/g) || []).length !== 1) fail(t('the alert line is not shown exactly once'));
  if ((body.match(/Hover or tap a day/g) || []).length !== 1) fail(t('the hover hint is not shown exactly once'));
  for (const bad of [/(^|\s)[fcn]\s/, /lane/i, /normalised/i, /Tap a bar/]) if (bad.test(body)) fail(t(`the section matches ${bad}`));
  // The chart: a picture with a summary, a table, a y axis, a gap mark; a day shows its value on one line.
  const chart = await page.$eval('[data-key=rel-chart-seat_personal-ratio]', (e) => ({ role: e.querySelector('svg').getAttribute('role'), label: e.querySelector('svg').getAttribute('aria-label'),
    rows: e.querySelectorAll('table.sr-only tr').length, gaps: e.querySelectorAll('.rel-gap').length, bars: e.querySelectorAll('.rel-bar').length, ticks: e.querySelectorAll('.rel-ytick').length,
    days: e.querySelectorAll('button.rel-day').length, legend: e.querySelector('.rel-legend').textContent })).catch(() => null);
  if (!chart) fail(t('no ratio chart'));
  else {
    if (chart.role !== 'img' || !/each day/.test(chart.label)) fail(t(`chart role/label ${chart.role} ${chart.label}`));
    if (chart.rows !== 14 || chart.days !== 14 || chart.ticks !== 3 || chart.gaps < 1 || chart.bars < 1) fail(t(`chart parts ${JSON.stringify(chart)}`));
    if (!/Opus 5\.5/.test(chart.legend) || !/no data/.test(chart.legend)) fail(t(`legend "${chart.legend}"`));
  }
  const fill = await page.$eval('.rel-bar', (e) => getComputedStyle(e).fill);
  if (!fill || fill === 'none' || fill === 'rgb(0, 0, 0)') fail(t(`bars have no colour: ${fill}`));
  await page.evaluate(() => document.querySelector('[data-key=rel-chart-seat_personal-ratio] button.rel-day:nth-of-type(13)').click());
  const status = await text('[data-key=rel-status-seat_personal-ratio]');
  if (!/^\d+ \w{3}: \d+(\.\d)?% of the 5-hour limit per 1% of the week · mostly Opus 5\.5 · 30 readings$/.test(status || '')) fail(t(`day status "${status}"`));
  await shot(page, `${sizeName}-o-usage-open`);
  await page.click('[data-key=rel-how-seat_personal] > summary');
  const how = await text('[data-key=rel-how-seat_personal]');
  if (!/How many % of the 5-hour limit a million tokens of Claude Code use cost: 1\.38, measured from 1504 readings over 7 days/.test(how || '')) fail(t('How this works lacks the meter factor sentence'));
  await shot(page, `${sizeName}-o-usage-how`);
  // The disclosure stays open across the page's redraws, and the other account shows its chart.
  await sleep(300);
  if (!(await page.$eval('[data-key=rel-seat_personal]', (e) => e.open))) fail(t('Usage and the picker closed by itself'));
  await page.click('[data-key=seat-toggle-seat_work]');
  await page.click('[data-key=rel-seat_work] > summary');
  await page.waitForSelector('[data-key=rel-chart-seat_work-cap-Opus\\ 5\\.5]', { timeout: 5000 }).catch(() => fail(t('no capacity chart for an account with two weeks')));
  const wt = await text('[data-key=rel-seat_work] .rel-body') || '';
  if (!/Not taking new sessions now/.test(wt) || !/blocking/.test(wt) || !/Frees up /.test(wt)) fail(t('a skipped account does not say so'));
  if (!/hub says|set on this machine|On laptop a setting on that machine makes it protected/.test(await text('[data-key=mode-seat_work]') || '')) fail(t('no local-override line for work'));
  await shot(page, `${sizeName}-o-usage-work`);
  // Dark mode: the same section, same colours from variables.
  await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'dark' }]);
  await sleep(200);
  await shot(page, `${sizeName}-o-usage-work-dark`);
  await page.click('[data-key=seat-toggle-seat_work]');
  await sleep(200);
  await shot(page, `${sizeName}-o-usage-dark`);
  await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'light' }]);
  await page.evaluate(() => { try { localStorage.removeItem('wy-accounts-open'); } catch (_) {} });
}

async function orderFlow(page, sizeName) {
  const t = (m) => page.tag(`order: ${m}`);
  await visit(page, sizeName, 'o-accounts', '#/accounts', '.seat-grip');
  const start = await cardOrder(page);
  if (start.length < 3) fail(t(`only ${start.length} cards`));
  if ((await sideShown(page)) && !sameList(await sideOrder(page), start)) fail(t(`sidebar ${await sideOrder(page)} differs from the cards ${start}`));
  if ((await page.$$('.seat-grip')).length !== start.length) fail(t('not one grip per card'));

  // Drag the last card to just before the one above it with the pointer (scrolled so both show).
  const last = start[start.length - 1];
  await page.evaluate((id) => window.scrollTo(0, document.getElementById(`seat-${id}`).getBoundingClientRect().top + window.scrollY - 70), start[start.length - 2]);
  await sleep(150);
  const g = await (await page.$(`[data-key="seat-grip-${last}"]`)).boundingBox();
  const second = await (await page.$(`#seat-${start[start.length - 2]}`)).boundingBox();
  await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
  await page.mouse.down();
  await page.mouse.move(second.x + 20, second.y + 20, { steps: 8 });
  if (!(await page.$('.seat-dragging')) || !(await page.$('.drop-before'))) fail(t('no drag state or drop bar while dragging'));
  await shot(page, `${sizeName}-o-accounts-dragging`);
  // (A full-page screenshot resets the scroll: put the pointer over the target again.)
  await page.evaluate((id) => window.scrollTo(0, document.getElementById(`seat-${id}`).getBoundingClientRect().top + window.scrollY - 70), start[start.length - 2]);
  await sleep(150);
  const again = await (await page.$(`#seat-${start[start.length - 2]}`)).boundingBox();
  await page.mouse.move(again.x + 20, again.y + 20, { steps: 3 });
  await page.mouse.up();
  const want = [...start.slice(0, -2), last, start[start.length - 2]];
  await waitCards(page, want, 'dropping the last card first did not reorder the cards');
  await waitSide(page, want, 'the sidebar did not follow the drop');
  await waitHub(page, want, 'the hub did not keep the order');
  await shot(page, `${sizeName}-o-accounts-dropped`);

  // Keyboard: ArrowDown on the focused grip moves that card one place; focus stays on it.
  await page.focus(`[data-key="seat-grip-${want[0]}"]`);
  await page.keyboard.press('ArrowDown');
  const kb = [want[1], want[0], ...want.slice(2)];
  await waitCards(page, kb, 'ArrowDown on the grip did not move the card');
  await waitHub(page, kb, 'the hub did not keep the keyboard move');
  if ((await page.evaluate(() => document.activeElement && document.activeElement.dataset.key)) !== `seat-grip-${want[0]}`) fail(t('focus left the grip after a move'));
  // Alt+ArrowUp inside the card moves it back (once, not twice).
  await page.keyboard.down('Alt'); await page.keyboard.press('ArrowUp'); await page.keyboard.up('Alt');
  await waitCards(page, want, 'Alt+ArrowUp did not move the card back one place');
  await waitHub(page, want, 'the hub did not keep the Alt+Arrow move back');

  // Another device reorders: this board follows on the stream with no reload, on any page.
  await page.evaluate(() => { location.hash = '#/'; });
  await page.waitForSelector('.page-now');
  const other = [...want].reverse();
  await page.evaluate(async (ids) => { await fetch('/v1/seats/order', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ids }) }); }, other);
  await waitSide(page, other, 'the sidebar did not follow an order set from another device', 5000);
  // The rings show at phone width, where there is no sidebar to wait on: wait for them too.
  const ringIds = () => page.$$eval('a.ring', (els) => els.map((e) => decodeURIComponent(e.getAttribute('href').split('seat=')[1])));
  await page.waitForFunction((w2) => {
    const r = [...document.querySelectorAll('a.ring')].map((e) => decodeURIComponent(e.getAttribute('href').split('seat=')[1]));
    return !r.length || JSON.stringify(r) === JSON.stringify(w2.filter((id) => r.includes(id)));
  }, { timeout: 5000 }, other).catch(() => {});
  const rings = await ringIds();
  if (rings.length && !sameList(rings, other.filter((id) => rings.includes(id)))) fail(t(`Now rings ${rings} do not follow ${other}`));

  // A refused save puts the order back and says so.
  await page.evaluate(() => { location.hash = '#/accounts'; });
  await page.waitForSelector('.seat-grip');
  await page.evaluate(() => {
    const real = window.fetch;
    window.__realFetch = real;
    window.fetch = (u, o) => (o && o.method === 'PUT' && /\/v1\/seats\/order$/.test(String(u))
      ? Promise.resolve(new Response(JSON.stringify({ error: 'invalid seat order: nope' }), { status: 400, headers: { 'Content-Type': 'application/json' } }))
      : real(u, o));
  });
  const before = await cardOrder(page);
  await page.focus(`[data-key="seat-grip-${before[0]}"]`);
  await page.keyboard.press('ArrowDown');
  await page.waitForSelector('[data-key=order-error]', { timeout: 4000 }).catch(() => fail(t('a refused save showed no error')));
  if (!sameList(await cardOrder(page), before)) fail(t('a refused save did not put the cards back'));
  await page.evaluate(() => { window.fetch = window.__realFetch; });
}

// ---- the Machines screen as the owner -----------------------------------------------------------

// The board redraws on stream events; click through the DOM so a redraw between lookup and click cannot detach the target.
const press = (page, key) => page.evaluate((k) => document.querySelector(`[data-key="${k}"]`).click(), key);

async function machinesFlow(page, sizeName) {
  const text = (sel) => page.$eval(sel, (e) => e.textContent.trim()).catch(() => null);
  await visit(page, sizeName, 'o-machines', '#/machines', '[data-key="pull-m_vps"]');
  const want = async (sel, re, what) => { const t = await text(sel); if (!t || !re.test(t)) fail(page.tag(`machines: ${what}: got ${JSON.stringify(t)}`)); };
  await want('[data-key="reach-m_laptop"]', /^Connects directly$/, 'direct chip');
  await want('[data-key="reach-m_gpu"]', /^Through your laptop$/, 'forward chip');
  await want('[data-key="reach-m_vps"]', /^Collected by the hub every 1 min$/, 'pull chip');
  await want('#machine-m_gpu .kv', /1,240 waiting to send \(oldest \d+ h\)/, 'backlog with its age');
  await want('#machine-m_gpu', /0\.4\.2[\s\S]*older than hub 0\.5\.0/i, 'older version');
  await want('#machine-m_cloud', /holding 37 updates because the hub is too old/i, 'held records');
  await want('[data-key="pull-state-m_vps"]', /^Collecting · failing: ssh: connect/, 'failing pull');
  await want('[data-key="alert-m_vps"]', /^Quiet-machine notification sent/, 'silent alert');
  await want('[data-key="status-m_vps"]', /^Quiet /, 'quiet status');
  // Silent machines sort first.
  const first = await page.$eval('.grid-machines .machine', (e) => e.dataset.machine);
  if (first !== 'm_vps') fail(page.tag(`machines: silent machine is not first (${first})`));

  // Pull now (accepted), Rename, Show removed.
  await press(page, 'pull-m_cloud');
  try { await page.waitForFunction(() => /Pulling cloud-box now/.test(document.body.textContent), { timeout: 4000 }); } catch (e) { fail(page.tag('Pull now showed no confirmation')); }
  await page.evaluate(() => { window.prompt = () => 'gpu-renamed'; window.confirm = () => true; });
  await press(page, 'rename-m_gpu');
  try { await page.waitForFunction(() => /gpu-renamed/.test((document.querySelector('#machine-m_gpu h2') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(page.tag('Rename did not show the new name')); }
  await press(page, 'show-removed');
  try { await page.waitForSelector('.removed-machines .machine.is-removed', { timeout: 4000 }); } catch (e) { fail(page.tag('Show removed listed no removed machine')); }
  if (await page.$('.removed-machines .machine-actions')) fail(page.tag('a removed machine offers actions'));
  await visit(page, sizeName, 'o-machines-removed', '#/machines', '.removed-machines .machine');

  // Every pull state on one screen.
  await page.evaluate(() => fetch('/_dev/pull-states', { method: 'POST' }));
  try { await page.waitForSelector('[data-key="pull-state-m_pull-host_key"]', { timeout: 4000 }); } catch (e) { fail(page.tag('pull state fixtures did not arrive on the stream')); }
  for (const st of ['new', 'ok', 'failing', 'host_key', 'auth', 'refused', 'disabled', 'frozen']) {
    if (!(await page.$(`[data-key="pull-state-m_pull-${st}"]`))) fail(page.tag(`no card for pull state ${st}`));
  }
  if (!(await page.$eval('[data-key="pull-m_pull-host_key"]', (b) => b.disabled))) fail(page.tag('Pull now is enabled for a host_key target'));
  await visit(page, sizeName, 'o-machines-states', '#/machines', '[data-key="pull-state-m_pull-frozen"]');

  // Remove: the card leaves the list.
  await press(page, 'remove-m_cloud');
  try { await page.waitForFunction(() => !document.querySelector('.page > .grid-machines #machine-m_cloud'), { timeout: 4000 }); } catch (e) { fail(page.tag('Remove left the card on the list')); }

  // Add machine dialog: the two commands.
  await page.evaluate(() => { location.hash = '#/more/devices'; });
  await page.waitForSelector('[data-key=add-machine]');
  await press(page, 'add-machine');
  try { await page.waitForSelector('dialog[open] [data-key=cmd-machine-add]', { timeout: 4000 }); } catch (e) { fail(page.tag('Add machine did not show the wy machine add command')); }
  await want('[data-key=cmd-machine-add]', /^wy machine add user@host$/, 'machine add command');
  await want('[data-key=cmd-install]', /install\.sh \| sh -s -- -- --hub join:http.*--code 482-913/, 'install command');
  await shot(page, `${sizeName}-o-add-machine`);
  await page.click('[data-key=dialog-close]');
  await page.evaluate(() => { location.hash = '#/more/devices'; });
}

// ---- Settings → Peers (owner and paired phone) ----------------------------------------------------

const ACCT = (n) => `3f1c6a2e-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`;
const CELL = (a, b) => `[data-key="cell-${ACCT(a)}-${ACCT(b)}"]`;

// ---- Analytics (analytics.md): totals, bars, lists, heatmap, range and seat controls ---------------

async function analyticsChecks(page, sizeName, who) {
  const t = (m) => page.tag(`analytics ${who}: ${m}`);
  const txt = (sel) => page.$eval(sel, (e) => e.textContent.trim()).catch(() => '');
  await visit(page, sizeName, `${who}-analytics`, '#/more/analytics', '[data-key=ana-bars] .ana-bar');
  const bars = () => page.$$eval('[data-key=ana-bars] .ana-bar', (e) => e.length);
  if ((await bars()) !== 30) fail(t(`expected 30 bars, got ${await bars()}`));
  if (!/\$\d/.test(await txt('.ana-tiles'))) fail(t('no dollar total in the tiles'));
  for (const h of ['Cost per day', 'By model', 'By project', 'When you work', 'Limit hits']) {
    if (!(await page.$$eval('section h2', (hs, want) => hs.some((x) => x.textContent === want), h))) fail(t(`no "${h}" section`));
  }
  if (!/Opus 4\.6/.test(await txt('.ana-list'))) fail(t('no model row'));
  if ((await page.$$eval('[data-key=ana-heat] .ana-cell', (e) => e.length)) !== 168) fail(t('heatmap is not 7 x 24'));
  if (!(await page.$('[data-key=ana-heat] .ana-cell.lvl-4'))) fail(t('heatmap has no hottest cell'));
  await page.click('[data-key=ana-days-7]');
  try { await page.waitForFunction(() => document.querySelectorAll('[data-key=ana-bars] .ana-bar').length === 7, { timeout: 4000 }); } catch (e) { fail(t('7 days did not give 7 bars')); }
  await page.select('[data-key=ana-seat]', 'seat_work');
  try { await page.waitForFunction(() => document.querySelector('[data-key=ana-seat]').value === 'seat_work', { timeout: 4000 }); } catch (e) { fail(t('seat choice did not stick')); }
  await sleep(400);
  await shot(page, `${sizeName}-${who}-analytics-7d`);
  await page.evaluate(() => { location.hash = '#/more'; });
}

// ---- Switches: the Smart Account Switch log and a session's path ------------------------------
async function switchesChecks(page, sizeName) {
  const t = (m) => page.tag(`switches: ${m}`);
  const txt = async (sel) => page.$eval(sel, (e) => e.textContent).catch(() => null);
  await visit(page, sizeName, 'switches', '#/switches', '[data-key=sw-list]');
  if ((await page.$$('.sw-row')).length !== 6) fail(t('the default range does not list the 6 moves'));
  if (!/5\s*Moves/.test((await txt('[data-key=sw-strip]')) || '') || !/1\s*Failed moves/.test((await txt('[data-key=sw-strip]')) || '')) fail(t('the strip does not say 5 moves, 1 failed'));
  if (!/client: \d+ out/.test((await txt('[data-key=sw-accounts]')) || '')) fail(t('the strip has no per-account line'));
  if (!/Failed: claude --resume failed/.test((await txt('.sw-failed')) || '')) fail(t('a failed move does not show its error'));
  if (!(await page.$('.sw-row .sw-org'))) fail(t('the cross-organisation move is not marked'));
  // Filter by hand: one row. The address keeps the filter.
  await page.select('[data-key=sw-mode]', 'manual');
  try { await page.waitForFunction(() => document.querySelectorAll('.sw-row').length === 1, { timeout: 4000 }); } catch (e) { fail(t('the by-hand filter did not leave one move')); }
  if (!/mode=manual/.test(await page.evaluate(() => location.hash))) fail(t('the filter is not in the address'));
  await shot(page, `${sizeName}-switches-filtered`);
  await page.select('[data-key=sw-mode]', '');
  await page.select('[data-key=sw-machine]', 'm_gpu');
  try { await page.waitForFunction(() => document.querySelectorAll('.sw-row').length === 2, { timeout: 4000 }); } catch (e) { fail(t('the machine filter did not leave gpu-box\'s 2 moves')); }
  await page.evaluate(() => { location.hash = '#/switches?account=work&range=all'; });
  try { await page.waitForFunction(() => document.querySelectorAll('.sw-row').length === 3, { timeout: 4000 }); } catch (e) { fail(t('the account filter (from or to) did not leave 3 moves')); }
  await page.evaluate(() => { location.hash = '#/switches?mode=auto&result=failed'; });
  try { await page.waitForFunction(() => document.querySelectorAll('.sw-row').length === 1, { timeout: 4000 }); } catch (e) { fail(t('failed only did not leave 1 move')); }
  await page.evaluate(() => { location.hash = '#/switches?range=custom&from=2020-01-01&to=2020-01-02'; });
  try { await page.waitForSelector('[data-key=sw-empty]', { timeout: 4000 }); } catch (e) { fail(t('an empty date range has no explanation')); }
  // A row's session opens its page, which shows the path.
  await visit(page, sizeName, 'switches-again', '#/switches?range=all', '[data-key=sw-list]');
  await page.click(`.sw-row a[href="#/sessions/${PERM}"]`);
  try { await page.waitForSelector('[data-key=session-moves]', { timeout: 6000 }); } catch (e) { fail(t('the session page has no move path')); return; }
  const path1 = (await txt('[data-key=session-moves] .sw-steps')) || '';
  if (!/client.*personal.*work/.test(path1) || /gpu/.test(path1)) fail(t(`the session path reads "${path1}"`));
  await shot(page, `${sizeName}-session-moves`);
  await page.click('[data-key=session-moves-all]');
  try { await page.waitForFunction(() => document.querySelectorAll('.sw-row').length === 3 && /session=/.test(location.hash), { timeout: 4000 }); } catch (e) { fail(t('"Every move and why" did not list the session\'s 3 moves')); }
  await visit(page, sizeName, 'machines-switches', '#/machines', '.machine');
  if (!(await page.$('[data-key=machine-switches-m_laptop]'))) fail(t('the machine card has no link to its switches'));
  if (sizeName === 'desk' && !(await page.$('#nav [data-key=nav-switches]'))) fail(t('the sidebar has no Switches link'));
  await visit(page, sizeName, 'more-switches', '#/more', '.more-list');
  if (!(await page.$('.more-list a[href="#/switches"]'))) fail(t('More has no Switches link'));
}

async function peersChecks(page, sizeName, who, owner) {
  const t = (m) => page.tag(`peers ${who}: ${m}`);
  const txt = (sel) => page.$eval(sel, (e) => e.textContent.trim()).catch(() => null);
  const pressSel = (sel) => page.evaluate((k) => document.querySelector(k).click(), sel);
  const settle = () => sleep(350);
  const cellIs = (sel, cls) => page.waitForFunction((k, c) => document.querySelector(k) && document.querySelector(k).classList.contains(c), { timeout: 4000 }, sel, cls);
  await visit(page, sizeName, `${who}-peers`, '#/more/peers', '[data-key=matrix]');

  // The grid renders with the mock matrix: four accounts, 12 cells and 4 self cells.
  const g = await page.evaluate(() => ({
    cols: document.querySelectorAll('[data-key=matrix] thead th.mx-col').length,
    rows: document.querySelectorAll('[data-key=matrix] tbody th.mx-row').length,
    btns: document.querySelectorAll('[data-key=matrix] .mx-btn').length,
    self: document.querySelectorAll('[data-key=matrix] .mx-self').length,
  }));
  if (g.cols !== 4 || g.rows !== 4 || g.btns !== 12 || g.self !== 4) fail(t(`grid is ${JSON.stringify(g)}`));
  if (!/^Version \d+ · signed by hub/.test((await page.$eval('[data-key=matrix-version]', (e) => e.textContent).catch(() => '')) || '')) fail(t('matrix version line missing'));
  // The page must not scroll sideways (visit checks); the grid may scroll inside its own container.
  const wrap = await page.$eval('.mx-wrap', (e) => e.offsetWidth);
  const vw = await page.evaluate(() => document.documentElement.clientWidth);
  if (wrap > vw) fail(t(`grid container (${wrap}px) is wider than the viewport (${vw}px)`));

  // Audit: metadata only.
  if (!(await page.$('.audit-list .row'))) fail(t('audit list is empty'));
  const audit = await page.evaluate(async () => (await (await fetch('/v1/peers/audit')).json()).entries);
  if (audit.some((e) => e.msg)) fail(t('mock audit carries message text'));

  // Trusted senders and Tags (pwa.md 3.1): the mock has Doorbell on laptop (the owner revokes it,
  // so the device pass later finds it revoked) and on old-vps (already revoked); sessions carry tags.
  const trustedRow = '[data-key=trusted-m_laptop]';
  if (!(await page.$(trustedRow))) fail(t('no Trusted senders row for laptop'));
  else {
    if (!/^Doorbell on laptop/.test((await txt(`${trustedRow} .row-text .strong`)) || '')) fail(t('trusted row has the wrong title'));
    if (!/messages today/.test((await txt(trustedRow)) || '')) fail(t('trusted row has no messages today'));
  }
  if (!/Revoked .* ago\. To approve it again, run wy peers trust doorbell/.test((await txt('[data-key=trusted-revoked-m_vps]')) || '')) fail(t('revoked row does not say how to approve again'));
  if (await page.$('[data-key=revoke-m_vps]')) fail(t('a revoked sender has a Revoke button'));
  if (!(await page.$('[data-key=tag-deploy] a[href^="#/sessions/"]'))) fail(t('tag deploy shows no holder link'));
  if (!/Sent to recently by Doorbell on laptop/.test((await txt('[data-key=tag-deploy]')) || '')) fail(t('tag deploy does not say who sent to it'));
  if (!/No session holds it now\./.test((await txt('[data-key=tag-nightly]')) || '')) fail(t('a tag with no holder does not say so'));
  if (owner) {
    if (!(await page.$('[data-key=revoke-m_laptop]'))) fail(t('no Revoke button for the owner'));
    else {
      await shot(page, `${sizeName}-${who}-peers-trusted`);
      // Revoke asks with window.confirm, a native dialog that would block the page.
      await page.evaluate(() => { window.confirm = () => true; });
      await pressSel('[data-key=revoke-m_laptop]');
      try { await page.waitForFunction(() => /Revoked/.test((document.querySelector('[data-key=trusted-m_laptop]') || {}).textContent || '') && !document.querySelector('[data-key=revoke-m_laptop]'), { timeout: 4000 }); } catch (e) { fail(t('revoking Doorbell did not mark it revoked')); }
    }
  } else if (await page.$('[data-key^=revoke-]')) fail(t('a paired device sees Revoke'));

  // Resume thread (peers.md 6.3): the owner sees it on the newest loop_guard stop and clicks it; a device never sees it.
  const resumeSel = '[data-key^=resume-]';
  if (owner) {
    if (!(await page.$(resumeSel))) fail(t('no Resume thread button on the loop-guard stop'));
    else {
      if (!/^Resume thread$/.test((await txt(resumeSel)) || '')) fail(t('resume button has the wrong label'));
      await shot(page, `${sizeName}-${who}-peers-resume`);
      await pressSel(resumeSel);
      try { await page.waitForFunction(() => /Thread resumed: 2 machines were told/.test((document.getElementById('toasts') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(t('resume showed no success toast')); }
      try { await page.waitForFunction((k) => !document.querySelector(k), { timeout: 4000 }, resumeSel); } catch (e) { fail(t('Resume thread button is still there after resuming')); }
      if (!(await page.evaluate(async () => (await (await fetch('/v1/peers/audit')).json()).entries.some((e) => e.event === 'thread_resume')))) fail(t('audit has no thread_resume entry'));
      try { await page.waitForFunction(() => /gpu-box kept the stop: it does not let the hub reopen threads/.test((document.querySelector('.audit-list') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(t('audit list does not show the machine that kept the stop')); }
      await settle();
      await shot(page, `${sizeName}-${who}-peers-resumed`);
    }
  } else if (await page.$(resumeSel)) fail(t('a paired device sees Resume thread'));

  // Pause, then resume (owner) or see why not (device).
  if (await page.$eval('[data-key=peers-pause]', (b) => b.disabled)) fail(t('pause button is disabled'));
  await pressSel('[data-key=peers-pause]');
  try { await page.waitForFunction(() => /All peers are paused/.test((document.querySelector('[data-key=peers-state]') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(t('pause did not show "All peers are paused"')); }
  await settle();
  await shot(page, `${sizeName}-${who}-peers-paused`);
  if (owner) {
    if (await page.$eval('[data-key=peers-pause]', (b) => b.disabled)) fail(t('resume is disabled for the owner'));
    await pressSel('[data-key=peers-pause]');
    try { await page.waitForFunction(() => /Peers are on/.test((document.querySelector('[data-key=peers-state]') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(t('resume did not show "Peers are on"')); }
  } else {
    // Resuming widens: a device cannot, and the page says why.
    if (!(await page.$eval('[data-key=peers-pause]', (b) => b.disabled)) || !/owner/.test((await txt('[data-key=peers-pause-why]')) || '')) fail(t('device resume is not disabled with a reason'));
  }
  const events = await page.evaluate(async () => (await (await fetch('/v1/peers/audit')).json()).entries.map((e) => e.event));
  if (!events.includes('pause')) fail(t('audit has no pause entry'));

  await page.evaluate(() => { location.hash = '#/more/peers'; });
  await page.waitForSelector(CELL(1, 11));
  // Bulk controls and the turn limit (peers.md 5.4): confirm is auto-accepted.
  await page.evaluate(() => { window.confirm = () => true; });
  const allCells = (cls) => page.evaluate((c) => { const b = [...document.querySelectorAll('[data-key=matrix] .mx-btn')]; return b.length === 12 && b.every((x) => x.classList.contains(c)); }, cls);
  const waitAll = (cls, what) => page.waitForFunction((c) => { const b = [...document.querySelectorAll('[data-key=matrix] .mx-btn')]; return b.length === 12 && b.every((x) => x.classList.contains(c)); }, { timeout: 4000 }, cls).catch(() => fail(t(what)));
  if (!/^Replies between two sessions without you/.test((await txt('[data-key=peers-turns] label')) || '') || !/When two sessions message each other on their own/.test((await txt('[data-key=peers-turns]')) || '') || !/starts the count again\./.test((await txt('[data-key=peers-turns]')) || '')) fail(t('turn limit label or explanation missing'));
  if (!/No limit lets agents keep talking with no one watching; the other limits still apply\./.test((await txt('[data-key=peers-turns]')) || '')) fail(t('no-limit explanation missing'));
  const setTurns = async (n) => {
    await page.$eval('[data-key=max-turns]', (i, v) => { i.value = v; i.dispatchEvent(new Event('input', { bubbles: true })); i.dispatchEvent(new Event('change', { bubbles: true })); }, String(n));
    await settle();
  };
  const noLimit = () => page.$eval('[data-key=max-turns-none]', (c) => c.checked);
  const setNone = async (on) => {
    await page.$eval('[data-key=max-turns-none]', (c, v) => { c.checked = v; c.dispatchEvent(new Event('change', { bubbles: true })); }, on);
    await settle();
  };
  const turnsDisabled = () => page.$eval('[data-key=max-turns]', (i) => i.disabled);
  const turnsLine = () => page.evaluate(() => { const d = document.querySelector('[data-key=peers-details]'); if (d) d.open = true; return (document.querySelector('[data-key=matrix-version]') || {}).textContent || ''; });
  if (owner) {
    // A new matrix has no turn limit: the box is ticked and the number field is off.
    if (!(await noLimit()) || !(await turnsDisabled())) fail(t('a new matrix does not show No limit with the number disabled'));
    if (!/no turn limit/.test(await turnsLine())) fail(t(`details line does not say no turn limit: ${await turnsLine()}`));
    if (!(await page.$eval('[data-key=max-turns-set]', (b) => b.disabled))) fail(t('Set is enabled with nothing changed'));
  }
  if (await noLimit()) await setNone(false);
  if (await turnsDisabled()) fail(t('the number field stays disabled with No limit off'));
  await setTurns(1);
  if (!(await page.$eval('[data-key=max-turns-set]', (b) => b.disabled)) || !/2 to 500/.test((await txt('[data-key=max-turns-why]')) || '')) fail(t('turn limit 1 is not refused with the range'));
  await setTurns(501);
  if (!(await page.$eval('[data-key=max-turns-set]', (b) => b.disabled))) fail(t('turn limit 501 is not refused'));
  if (owner) {
    if (!(await page.$('[data-key=allow-all]')) || (await page.$eval('[data-key=allow-all]', (b) => b.disabled))) fail(t('Allow all is missing or disabled for the owner'));
    await pressSel('[data-key=allow-all]');
    await waitAll('mx-both', 'Allow all did not turn every cell to both ways');
    await settle();
    await shot(page, `${sizeName}-${who}-peers-allow-all`);
    await pressSel('[data-key=block-all]');
    await waitAll('mx-none', 'Block all did not clear every cell');
    if (!(await noLimit())) fail(t('No limit is not ticked again after a matrix change while the matrix has none'));
    await setNone(false);
    await setTurns(40);
    if (await page.$eval('[data-key=max-turns-set]', (b) => b.disabled)) fail(t('setting the turn limit to 40 is disabled for the owner'));
    await pressSel('[data-key=max-turns-set]');
    try { await page.waitForFunction(() => /at most 40 turns/.test((document.querySelector('[data-key=matrix-version]') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(t(`setting the turn limit to 40 did not stick: ${await turnsLine()}`)); }
    const ev = await page.evaluate(async () => (await (await fetch('/v1/peers/matrix')).json()).matrix);
    if (ev.max_turns !== 40 || ev.same_account !== false || ev.allow.length !== 0) fail(t(`matrix after bulk edits: ${JSON.stringify(ev)}`));
    // No limit is the highest value; the owner may set it.
    await setNone(true);
    if (!(await turnsDisabled())) fail(t('the number field is not disabled with No limit on'));
    if (await page.$eval('[data-key=max-turns-set]', (b) => b.disabled)) fail(t('setting no limit is disabled for the owner'));
    await pressSel('[data-key=max-turns-set]');
    try { await page.waitForFunction(() => /no turn limit/.test((document.querySelector('[data-key=matrix-version]') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(t(`setting no limit did not stick: ${await turnsLine()}`)); }
    const nl = await page.evaluate(async () => (await (await fetch('/v1/peers/matrix')).json()).matrix);
    if (nl.max_turns !== 0) fail(t(`matrix after No limit: ${JSON.stringify(nl)}`));
    await settle();
    await shot(page, `${sizeName}-${who}-peers-no-limit`);
    await setNone(false);
    await setTurns(30);
    await pressSel('[data-key=max-turns-set]');
    try { await page.waitForFunction(() => /at most 30 turns/.test((document.querySelector('[data-key=matrix-version]') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(t('lowering the turn limit to 30 did not stick')); }
    await pressSel('[data-key=allow-all]');
    await waitAll('mx-both', 'second Allow all did not turn every cell to both ways');
    await pressSel('[data-key=block-all]');
    await waitAll('mx-none', 'second Block all did not clear every cell');
    await pressSel('[data-key=same-account]');
    await sleep(500);
    if (!(await allCells('mx-none'))) fail(t('cells are not clear after the bulk checks'));
  } else {
    if (await page.$('[data-key=allow-all]')) fail(t('a device sees Allow all'));
    if (!/Only the owner can allow pairs\./.test((await txt('[data-key=allow-all-why]')) || '')) fail(t('device has no "Only the owner can allow pairs." line'));
    await setTurns(45);
    if (!(await page.$eval('[data-key=max-turns-set]', (b) => b.disabled)) || !/owner sign-in/.test((await txt('[data-key=max-turns-why]')) || '')) fail(t('device may raise the turn limit, or is not told why not'));
    await setNone(true);
    if (!(await page.$eval('[data-key=max-turns-set]', (b) => b.disabled)) || !/owner sign-in/.test((await txt('[data-key=max-turns-why]')) || '')) fail(t('device may set no limit, or is not told why not'));
    await setNone(false);
    await setTurns(20);
    if (await page.$eval('[data-key=max-turns-set]', (b) => b.disabled)) fail(t('device cannot lower the turn limit'));
    await pressSel('[data-key=max-turns-set]');
    try { await page.waitForFunction(() => /at most 20 turns/.test((document.querySelector('[data-key=matrix-version]') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail(t('lowering the turn limit to 20 did not stick')); }
  }
  if (owner) {
    // Widen: personal and work both ways, personal to client one way.
    await pressSel(CELL(1, 11));
    await page.waitForSelector('[data-key=cell-editor]');
    if (await page.$eval('[data-key=set-both]', (b) => b.disabled)) fail(t('widening button is disabled for the owner'));
    await pressSel('[data-key=set-both]');
    try { await cellIs(CELL(1, 11), 'mx-both'); } catch (e) { fail(t('widening did not turn the cell to both ways')); }
    await pressSel(CELL(1, 21));
    await page.waitForSelector('[data-key=set-out]');
    await pressSel('[data-key=set-out]');
    try { await cellIs(CELL(1, 21), 'mx-out'); } catch (e) { fail(t('widening did not turn the cell one way')); }
    await settle();
    await shot(page, `${sizeName}-${who}-peers-widened`);
    const ev = await page.evaluate(async () => (await (await fetch('/v1/peers/audit')).json()).entries.slice(0, 3).map((e) => `${e.event}:${e.detail.widens}`));
    if (!ev.includes('matrix:true')) fail(t(`audit shows no widened matrix entry: ${ev}`));
  } else {
    // Device: personal/work is both ways, personal/client one way (left by the owner part).
    await pressSel(CELL(1, 11));
    await page.waitForSelector('[data-key=cell-editor]');
    const dis = await page.evaluate(() => Object.fromEntries(['none', 'out', 'in', 'both'].map((k) => [k, document.querySelector(`[data-key=set-${k}]`).disabled])));
    // both is the current state; none, out and in only remove pairs.
    if (dis.none || dis.out || dis.in || !dis.both) fail(t(`device buttons on a both-ways cell: ${JSON.stringify(dis)}`));
    await pressSel(CELL(1, 21));
    await page.waitForSelector('[data-key=set-both]');
    const d2 = await page.evaluate(() => ({ both: document.querySelector('[data-key=set-both]').disabled, in: document.querySelector('[data-key=set-in]').disabled, none: document.querySelector('[data-key=set-none]').disabled, why: (document.querySelector('[data-key=cell-why]') || {}).textContent }));
    if (!d2.both || !d2.in || d2.none || !/owner sign-in/.test(d2.why || '')) fail(t(`device widen not disabled with a reason: ${JSON.stringify(d2)}`));
    await settle();
    await shot(page, `${sizeName}-${who}-peers-editor`);
    await pressSel('[data-key=set-none]');
    try { await cellIs(CELL(1, 21), 'mx-none'); } catch (e) { fail(t('narrowing did not clear the cell')); }
    await settle();
    await shot(page, `${sizeName}-${who}-peers-narrowed`);
    // Dark mode follows prefers-color-scheme; the paused panel and the cells must stay readable.
    await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'dark' }]);
    await settle();
    await shot(page, `${sizeName}-${who}-peers-dark`);
    await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'light' }]);
    // Block all is a narrowing: any paired device may do it.
    if (await page.$eval('[data-key=block-all]', (b) => b.disabled)) fail(t('Block all is disabled for a device'));
    await pressSel('[data-key=block-all]');
    await page.waitForFunction(() => { const b = [...document.querySelectorAll('[data-key=matrix] .mx-btn')]; return b.length === 12 && b.every((x) => x.classList.contains('mx-none')); }, { timeout: 4000 }).catch(() => fail(t('Block all did not clear every cell')));
    const mx = await page.evaluate(async () => (await (await fetch('/v1/peers/matrix')).json()).matrix);
    if (mx.allow.length !== 0 || mx.same_account !== false || mx.max_turns !== 20) fail(t(`matrix after Block all: ${JSON.stringify(mx)}`));
    await settle();
    await shot(page, `${sizeName}-${who}-peers-blocked`);
  }
}

async function peersFlow(sizeName, w, hgt) {
  // The paired phone pairs first, and while the stop is still unresumed must not see "Resume thread".
  const pctx = await browser.createBrowserContext();
  const ppage = await newPage(pctx, sizeName, w, hgt, { ua: ANDROID, label: 'peers-phone' });
  await ppage.goto(`${BASE}/#/pair?s=${SECRET}&c=${CODE}`, { waitUntil: 'networkidle2' });
  await ppage.waitForSelector('[data-key=paired-as]');
  await ppage.click('[data-key=pair-continue]');
  await ppage.waitForSelector('.needs-card');
  await visit(ppage, sizeName, 'device-peers-stop', '#/more/peers', '[data-key=matrix]');
  if (!(await ppage.$('.audit-list [data-key^=audit-]'))) fail(ppage.tag('peers device: audit has no rows'));
  if (!(await ppage.evaluate(async () => (await (await fetch('/v1/peers/audit')).json()).entries.some((e) => e.event === 'loop_guard')))) fail(ppage.tag('peers device: no loop_guard stop to hide the button on'));
  if (await ppage.$('[data-key^=resume-]')) fail(ppage.tag('peers device: sees Resume thread on an unresumed stop'));
  await ppage.evaluate(() => { location.hash = '#/more'; });

  const octx = await browser.createBrowserContext();
  const opage = await newPage(octx, sizeName, w, hgt, { label: 'peers-owner', mobile: false });
  await opage.goto(`${BASE}/#/owner?s=${OWNER}`, { waitUntil: 'networkidle2' });
  await opage.waitForSelector('.page-now');
  await peersChecks(opage, sizeName, 'owner', true);
  await octx.close();

  // The owner's edits stay in the mock: the paired phone (read + push) now narrows them.
  await peersChecks(ppage, sizeName, 'device', false);
  await pctx.close();
  await resetHub();
}

// ---- Settings → Keys (keys.md 3.3): confirm a pending owner key, then the certify step ----------------

async function keysFlow(sizeName, w, hgt) {
  const t = (p, m) => p.tag(`keys: ${m}`);
  const txt = (p, sel) => p.$eval(sel, (e) => e.textContent.trim()).catch(() => null);
  const grouped = 'k7q2-m4xa-pz3c-ufh6-2wbe-q5no';
  // A paired phone (read + push) sees a read-only page: no device list, no Confirm, a note.
  const pctx = await browser.createBrowserContext();
  const ppage = await newPage(pctx, sizeName, w, hgt, { ua: ANDROID, label: 'keys-phone' });
  await ppage.goto(`${BASE}/#/pair?s=${SECRET}&c=${CODE}`, { waitUntil: 'networkidle2' });
  await ppage.waitForSelector('[data-key=paired-as]');
  await ppage.click('[data-key=pair-continue]');
  await ppage.waitForSelector('.needs-card');
  await visit(ppage, sizeName, 'device-keys', '#/more/keys', '[data-key=keys-page]');
  if (!/Hub key/.test(await ppage.evaluate(() => document.body.innerText))) fail(t(ppage, 'no Hub key section'));
  if (await ppage.$('[data-key=confirm-owner-key]')) fail(t(ppage, 'a device sees Confirm'));
  if (await ppage.$('[data-key=key-devices]')) fail(t(ppage, 'a read-only device sees the device list'));
  if (!(await ppage.$('[data-key=keys-read-only]'))) fail(t(ppage, 'no read-only note'));
  await pctx.close();

  // The owner sees the pending key, compares and confirms; the next step becomes Certify.
  const octx = await browser.createBrowserContext();
  const opage = await newPage(octx, sizeName, w, hgt, { label: 'keys-owner', mobile: false });
  await opage.goto(`${BASE}/#/owner?s=${OWNER}`, { waitUntil: 'networkidle2' });
  await opage.waitForSelector('.page-now');
  await visit(opage, sizeName, 'owner-keys-pending', '#/more/keys', '[data-key=key-pending]');
  if ((await txt(opage, '[data-key=pending-fp]')) !== grouped) fail(t(opage, `pending fingerprint is ${await txt(opage, '[data-key=pending-fp]')}`));
  const pend = await txt(opage, '[data-key=key-pending]');
  if (!/proposed by laptop/.test(pend) || !/Is this the fingerprint your terminal printed\?/.test(pend)) fail(t(opage, `pending text: ${pend}`));
  if (!/it is deleted/.test((await txt(opage, '[data-key=pending-expiry]')) || '')) fail(t(opage, 'no expiry line'));
  await opage.click('[data-key=confirm-owner-key]');
  try { await opage.waitForSelector('[data-key=keys-step-certify]', { timeout: 6000 }); } catch (e) { fail(t(opage, 'no Certify step after Confirm')); }
  await sleep(350);
  if ((await txt(opage, '[data-key=owner-fp]')) !== grouped) fail(t(opage, 'active owner key not shown'));
  if (!/wy keys certify --all/.test((await txt(opage, '[data-key=cmd-keys-certify]')) || '')) fail(t(opage, 'certify command missing'));
  const states = await opage.$$eval('[data-key^=key-dev-] [data-state]', (e) => e.map((x) => x.dataset.state));
  if (states.join() !== 'certified,hub-signed') fail(t(opage, `device states ${states}`));
  if (!(await opage.$('[data-key=certify-hint]'))) fail(t(opage, 'no certify hint'));
  await shot(opage, `${sizeName}-owner-keys-certify`);
  await octx.close();
  await resetHub();
}

// ---- installability, worker, offline, update, push, iPhone ---------------------------------------

async function pwaFlow() {
  const ctx = await browser.createBrowserContext();
  await ctx.overridePermissions(BASE, ['notifications']);
  const page = await newPage(ctx, 'pwa', 390, 844, { ua: ANDROID, label: 'pwa' });
  // Subscribing for real needs Google's push service: stub it with a subscription whose
  // keys were generated here, and check the app posts it.
  await page.evaluateOnNewDocument(() => {
    const b64 = (a) => btoa(String.fromCharCode(...new Uint8Array(a))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const point = new Uint8Array(65); point[0] = 4; for (let i = 1; i < 65; i++) point[i] = (i * 13) % 251;
    const fake = { endpoint: 'https://fcm.googleapis.com/fcm/send/smoke-endpoint', options: { applicationServerKey: null },
      toJSON() { return { endpoint: this.endpoint, keys: { p256dh: b64(point), auth: b64(new Uint8Array(16).fill(7)) } }; }, unsubscribe: async () => true };
    let current = null;
    if (window.PushManager) {
      PushManager.prototype.subscribe = async function (o) { fake.options = { applicationServerKey: o.applicationServerKey.buffer || o.applicationServerKey }; current = fake; return fake; };
      PushManager.prototype.getSubscription = async function () { return current; };
    }
  });
  const posted = [];
  page.on('request', (r) => { if (r.method() === 'POST' && /\/v1\/push\/targets$/.test(r.url())) posted.push(JSON.parse(r.postData() || '{}')); });
  const cdp = await page.createCDPSession();
  const trace = [];
  page.on('response', (r) => { if (/\/v1\/(me|overview|pair)/.test(r.url())) trace.push(`${r.status()} ${r.url().replace(BASE, '')} sw=${r.fromServiceWorker()}`); });

  await page.goto(`${BASE}/#/pair?s=${SECRET}`, { waitUntil: 'networkidle2' });
  await page.waitForSelector('[data-key=paired-as]');
  await page.click('[data-key=pair-continue]');
  await page.waitForSelector('.needs-card');

  // manifest and installability
  await cdp.send('Page.enable');
  const m = await cdp.send('Page.getAppManifest');
  if (m.errors && m.errors.length) fail(`manifest errors: ${JSON.stringify(m.errors)}`);
  if (!m.data || !m.data.includes('"Watchyard"')) fail('manifest did not load');
  const inst = await cdp.send('Page.getInstallabilityErrors');
  const instErrs = (inst.installabilityErrors || []).filter((x) => x.errorId !== 'in-incognito'); // the test browser context is incognito
  if (instErrs.length) fail(`installability errors: ${JSON.stringify(instErrs)}`);

  // worker controls the page after one reload; the shell cache exists
  await page.evaluate(() => navigator.serviceWorker.ready);
  await page.reload({ waitUntil: 'networkidle2' });
  await page.waitForSelector('.needs-card', { timeout: 8000 }).catch(async () => { fail('after SW reload: ' + await page.evaluate(() => document.body.innerText.slice(0, 60) + ' | ' + location.href) + ' ' + trace.join('; ') + ' cookies=' + (await page.cookies()).map((c) => c.name) + ' st=' + await page.evaluate(() => JSON.stringify({ u: __wy.store.unauthorized, e: __wy.store.error, me: !!__wy.store.me })) + ' me=' + await page.evaluate(async () => { const r = await fetch('/v1/me'); return r.status + ' ' + (await r.text()).slice(0, 120); })); });
  const ctl = await page.evaluate(() => !!navigator.serviceWorker.controller);
  if (!ctl) fail('service worker does not control the page after a reload');
  const keys = await page.evaluate(() => caches.keys());
  if (!keys.some((k) => k.startsWith('wy-shell-'))) fail(`no shell cache: ${keys}`);
  if (!keys.includes('wy-data')) fail(`no data cache: ${keys}`);
  const build0 = await page.evaluate(() => new Promise((res) => { navigator.serviceWorker.addEventListener('message', (e) => e.data.type === 'build' && res(e.data.build)); navigator.serviceWorker.controller.postMessage({ type: 'build' }); }));

  // offline: the last known state with the banner, controls off
  page.expectOffline = true;
  // Page emulation does not reach the worker's own requests: cut the worker's network too.
  const swTarget = await browser.waitForTarget((t) => t.type() === 'service_worker' && t.url().startsWith(BASE), { timeout: 5000 });
  const swNet = await swTarget.createCDPSession();
  await swNet.send('Network.enable');
  const cond = (offline) => ({ offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  await swNet.send('Network.emulateNetworkConditions', cond(true));
  await page.setOfflineMode(true);
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.banner-offline', { timeout: 15000 }).catch(() => fail('no offline banner after an offline reload'));
  await page.waitForSelector('.needs-card', { timeout: 15000 }).catch(async () => fail('Now did not render from the stored copy while offline: ' + await page.evaluate(() => location.hash + ' ' + document.body.innerText.slice(0, 300) + JSON.stringify(window.__wy.store.error)).catch(String)));
  const bannerText = await page.$eval('.banner-offline', (e) => e.textContent).catch(() => '');
  if (!/Offline · showing \d\d:\d\d/.test(bannerText)) fail(`offline banner text: ${bannerText}`);
  await shot(page, 'phone-offline');
  await swNet.send('Network.emulateNetworkConditions', cond(false)).catch(() => {});
  for (const t of browser.targets().filter((x) => x.type() === 'service_worker')) { const c = await t.createCDPSession(); await c.send('Network.enable'); await c.send('Network.emulateNetworkConditions', cond(false)).catch(() => {}); }
  await page.setOfflineMode(false);
  page.expectOffline = false;
  try { await page.waitForFunction(() => !document.querySelector('.banner-offline'), { timeout: 30000 }); } catch (e) { fail('offline banner stayed after coming back online: ' + await page.evaluate(async () => { const r = await fetch('/v1/overview'); return r.status + ' ' + r.headers.get('x-wy-cached-at') + ' ' + JSON.stringify(window.__wy.store.live); }).catch(String)); }

  // notifications: turn on from Settings
  await page.evaluate(() => { location.hash = '#/more/settings'; });
  // The mock already lists a target for this device, so the app may be On straight away (and
  // re-subscribed at start); otherwise turn it on here.
  await page.waitForSelector('[data-key=turn-on], [data-key=test-push]', { timeout: 8000 });
  if (await page.$('[data-key=turn-on]')) await page.click('[data-key=turn-on]');
  try { await page.waitForFunction(() => /^On/.test((document.querySelector('[data-key=notif-state]') || {}).textContent || ''), { timeout: 6000 }); } catch (e) { fail('Turn on did not reach the On state'); }
  if (!posted.some((b) => b.kind === 'webpush' && /smoke-endpoint/.test(b.endpoint || '') && b.keys && b.keys.p256dh)) fail(`app did not post the subscription: ${JSON.stringify(posted)}`);
  await page.click('[data-key=test-push]');
  try { await page.waitForFunction(() => /Sent/.test((document.querySelector('[data-key=test-result]') || {}).textContent || ''), { timeout: 4000 }); } catch (e) { fail('Send test push showed no result'); }
  await shot(page, 'phone-settings-on');

  // push display through the service worker
  const regs = new Map();
  await cdp.send('ServiceWorker.enable');
  cdp.on('ServiceWorker.workerRegistrationUpdated', (e) => e.registrations.forEach((r) => regs.set(r.registrationId, r)));
  await page.reload({ waitUntil: 'networkidle2' });
  await sleep(500);
  const reg = [...regs.values()].find((r) => r.scopeURL.startsWith(BASE));
  if (!reg) fail('no service worker registration seen over CDP');
  else {
    await page.evaluate(() => { window.__badges = []; });
    const payload = { v: 1, type: 'alert', alert: 'al_x', alerts: ['al_x'], trigger: 'asked', title: 'work · api · asked a question', body: 'Should I also update the migration?', tag: 'wy-s-smoke', url: '/#/sessions/' + ASKED, tap: 'session', session: ASKED, needs_you: 3, actions: { snooze: 'a1.dev.snooze', seen: 'a1.dev.seen' }, private: false, ts: new Date().toISOString() };
    await cdp.send('ServiceWorker.deliverPushMessage', { origin: BASE, registrationId: reg.registrationId, data: JSON.stringify(payload) });
    await sleep(800);
    const shown = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).getNotifications()).map((n) => ({ title: n.title, tag: n.tag, actions: (n.actions || []).map((a) => a.action) })));
    const n = shown.find((x) => x.tag === 'wy-s-smoke');
    if (!n) fail(`pushed notification not shown: ${JSON.stringify(shown)}`);
    else {
      if (n.title !== payload.title) fail(`notification title ${n.title}`);
      if (n.actions.length && n.actions.join() !== 'snooze,seen') fail(`notification actions ${n.actions}`);
    }
    // an unreadable payload still shows something
    await cdp.send('ServiceWorker.deliverPushMessage', { origin: BASE, registrationId: reg.registrationId, data: 'not json' });
    await sleep(500);
    const after = await page.evaluate(async () => (await (await navigator.serviceWorker.ready).getNotifications()).length);
    if (after < 2) fail('a push with a bad payload showed no notification');
  }

  // fonts: the symbols face loads from the hub for the arrows and shapes Plex lacks
  const symFaces = await page.evaluate(async () => (await document.fonts.load('16px "Watchyard Symbols"', '\u2192\u25CF\u283F\u24D8')).length);
  if (symFaces < 3) fail(`the Watchyard Symbols faces did not load (${symFaces} of 3)`);

  // update: the hub switches to another build
  await fetch(BASE + '/_dev/build', { method: 'POST', body: JSON.stringify({ hash: 'ffffffffffff' }), headers: { 'Content-Type': 'application/json' } });
  await page.evaluate(() => { location.hash = '#/'; });
  // A page this young would take the new build by itself (checked below); stop that here so the
  // banner and its Reload button are what is tested.
  await page.evaluate(() => sessionStorage.setItem('wy.autoUpdateAt', String(Date.now())));
  await page.evaluate(async () => { await (await navigator.serviceWorker.getRegistration()).update(); });
  try { await page.waitForSelector('.banner-update', { timeout: 8000 }); } catch (e) { fail('"Update ready" never appeared after the build changed'); }
  await shot(page, 'phone-update-banner');
  await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle2' }).catch(() => {}), page.click('[data-key=update-reload]')]);
  await page.waitForSelector('.needs-card');
  await sleep(500);
  const build1 = await page.evaluate(() => new Promise((res) => { navigator.serviceWorker.addEventListener('message', (e) => e.data.type === 'build' && res(e.data.build)); navigator.serviceWorker.controller.postMessage({ type: 'build' }); }));
  if (build1 !== 'ffffffffffff' || build1 === build0) fail(`reloaded page runs build ${build1}, expected ffffffffffff (was ${build0})`);
  // A page that has only just started applies the next build without a tap, but not with a
  // dialog open. The policy's "just started" is FRESH_PAGE_MS (15 s) of page age, and the
  // install of the new build can take most of that on a slow runner, so the build is
  // installed first (on this page, held back by a recent auto-update) and the checks then run
  // on a page loaded afterwards, whose age the waits below no longer depend on.
  await fetch(BASE + '/_dev/build', { method: 'POST', body: JSON.stringify({ hash: 'eeeeeeeeeeee' }), headers: { 'Content-Type': 'application/json' } });
  await page.evaluate(() => sessionStorage.setItem('wy.autoUpdateAt', String(Date.now())));
  await page.evaluate(async () => { await (await navigator.serviceWorker.getRegistration()).update(); }).catch(() => {});
  try {
    await page.waitForFunction(async () => { const r = await navigator.serviceWorker.getRegistration(); return !!(r && r.waiting); }, { timeout: 20000, polling: 250 });
  } catch (e) { fail('the next build did not install'); }
  // The reloaded page opens with a dialog (added before the app's scripts run): the banner
  // shows, the page is not reloaded, and the build is taken once the dialog is closed.
  // (wy.autoUpdateAt is cleared there too: cleared here, this page could take the build first.)
  await page.evaluate(() => sessionStorage.setItem('smoke.modal', '1'));
  await page.evaluateOnNewDocument(() => {
    if (sessionStorage.getItem('smoke.modal') !== '1') return;
    sessionStorage.removeItem('smoke.modal');
    sessionStorage.removeItem('wy.autoUpdateAt');
    // The document is still empty here; the app's module scripts run once it is parsed.
    const add = () => {
      if (!document.documentElement || document.getElementById('smoke-modal')) return !!document.documentElement;
      const d = document.createElement('dialog'); d.id = 'smoke-modal'; d.setAttribute('open', '');
      document.documentElement.append(d);
      return true;
    };
    if (!add()) new MutationObserver((_, o) => { if (add()) o.disconnect(); }).observe(document, { childList: true });
  });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { window.__alive = true; });
  try { await page.waitForSelector('.banner-update', { timeout: 8000 }); } catch (e) { fail('"Update ready" did not show while a dialog was open'); }
  await sleep(3500); // two re-checks of the guard: asserting that nothing happens takes a wait
  if (!(await page.evaluate(() => window.__alive === true && !!document.getElementById('smoke-modal')).catch(() => false))) fail('the page reloaded by itself while a dialog was open');
  await page.evaluate(() => document.getElementById('smoke-modal') && document.getElementById('smoke-modal').remove());
  let build2 = '';
  for (let i = 0; i < 40 && build2 !== 'eeeeeeeeeeee'; i++) {
    await sleep(250);
    build2 = await page.evaluate(() => new Promise((res) => {
      const c = navigator.serviceWorker.controller;
      if (!c) { res(''); return; }
      navigator.serviceWorker.addEventListener('message', (e) => e.data.type === 'build' && res(e.data.build));
      c.postMessage({ type: 'build' });
      setTimeout(() => res(''), 500);
    })).catch(() => '');
  }
  if (build2 !== 'eeeeeeeeeeee') fail(`a freshly started page did not take the new build by itself (running ${build2 || 'unknown'})`);
  await fetch(BASE + '/_dev/build', { method: 'POST', body: JSON.stringify({ hash: '' }), headers: { 'Content-Type': 'application/json' } });
  await ctx.close();

  // iPhone: Safari does not claim; it shows the steps and the digits.
  const ictx = await browser.createBrowserContext();
  const ipage = await newPage(ictx, 'pwa', 390, 844, { ua: IPHONE, label: 'iphone' });
  await ipage.goto(`${BASE}/#/pair?s=${SECRET}&c=${CODE}`, { waitUntil: 'networkidle2' });
  try { await ipage.waitForSelector('.steps', { timeout: 5000 }); } catch (e) { fail('iPhone pairing did not show the install steps'); }
  const digits = await ipage.$eval('[data-key=pair-digits]', (e) => e.textContent).catch(() => '');
  if (digits.trim() !== '482-913') fail(`iPhone pair digits: ${digits}`);
  if ((await ipage.cookies()).some((c) => c.name === 'wy_session')) fail('iPhone Safari path claimed the code');
  await shot(ipage, 'phone-pair-ios');
  // The installed app (standalone) has no secret: typed code.
  // The installed app opens standalone (iOS reports navigator.standalone) with no secret in the link.
  await ipage.evaluateOnNewDocument(() => { Object.defineProperty(navigator, 'standalone', { get: () => true }); });
  await ipage.goto('about:blank');
  await ipage.goto(`${BASE}/#/pair`, { waitUntil: 'networkidle2' });
  await ipage.waitForSelector('[data-key=pair-code]');
  await ipage.type('[data-key=pair-code]', '000000');
  await ipage.click('[data-key=pair-submit]');
  try { await ipage.waitForSelector('[data-key=pair-error]', { timeout: 4000 }); } catch (e) { fail('a wrong typed code showed no error'); }
  await ipage.$eval('[data-key=pair-code]', (e) => { e.value = ''; });
  await ipage.type('[data-key=pair-code]', CODE);
  await ipage.click('[data-key=pair-submit]');
  try { await ipage.waitForSelector('[data-key=paired-as]', { timeout: 5000 }); } catch (e) { fail('typed code did not pair'); }
  await ictx.close();
  await resetHub();
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  browser = await puppeteer.launch({ executablePath: CHROME, headless: true, args: ['--no-sandbox', '--disable-gpu'] });
  try {
    for (const [sizeName, w, hgt] of SIZES) {
      await boardFlow(sizeName, w, hgt);
      await phoneFlow(sizeName, w, hgt);
      await ownerFlow(sizeName, w, hgt);
      await peersFlow(sizeName, w, hgt);
      await keysFlow(sizeName, w, hgt);
    }
    await pwaFlow();
  } finally {
    await browser.close();
  }
  if (problems.length) { console.error(`${problems.length} problem(s)`); process.exit(1); }
  console.log('smoke ok; screenshots in', OUT);
})().catch((e) => { console.error(e); process.exit(1); });
