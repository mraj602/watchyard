// Electron smoke S1–S11, S14, S15 (docs/spec/desktop.md): one app, a real hub, the fake
// claude.ai and the real wy, all in a temp sandbox. The scenarios build on each other, so they
// run in order inside one test. Run: `npm run smoke` (Linux: under xvfb-run).

import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

import {
  sandbox, wyBinary, startHub, launch, main, inTab, tabUrl, shellState, waitFor, sleep, freshFixture, outboxRecords, outDir,
} from './harness.mjs';
import { startFakeClaude, uuidOf, USAGE_DIR } from '../fakes/claude/server.js';
import { FUNCTIONS } from '../../src/shared/ipc.js';
import { buildRecords, clientOf } from '../../src/shared/record.js';
import { SHELL_CSP, WARNING_VERSION } from '../../src/shared/constants.js';

const CADENCE_MS = 2000;
const WARNING_TITLE = 'Read usage for this account?';
const ACCEPT = 'Read usage for this account';

test('Electron smoke S1–S11, S14, S15', { timeout: 15 * 60_000 }, async (t) => {
  const sb = sandbox('smoke');
  const out = outDir(sb.root);
  const bin = wyBinary(sb.root);
  const hub = await startHub(sb, bin);
  const fixture = freshFixture(sb.root, { five: 42, week: 61 });
  const claude = await startFakeClaude({ fixtures: [path.relative(USAGE_DIR, fixture)] });
  const env = { WY_BIN: bin, WY_APP_TEST_CLAUDE_ORIGIN: claude.origin, WY_APP_TEST_CADENCE_MS: String(CADENCE_MS) };
  let run = null;
  const ids = {};
  const stats = async () => (await fetch(`${claude.origin}/_fake/stats`)).json();
  const usageCount = async () => (await stats()).paths.usage ?? 0;
  const script = (body) => fetch(`${claude.origin}/_fake/script`, { method: 'POST', body: JSON.stringify(body) });

  t.after(async () => {
    if (run) {
      fs.writeFileSync(path.join(out, 'main.log'), run.mainLog.join(''));
      await run.close();
    }
    await claude.close();
    await hub.stop();
    if (!process.env.WY_SMOKE_KEEP) sb.cleanup();
  });

  const signIn = async (id) => {
    await waitFor(async () => /\/login$/.test(await tabUrl(run.app, id)), 20_000, `tab ${id} on the fake login page`);
    await inTab(run.app, id, "document.getElementById('go').click()");
    await waitFor(async () => /\/new$/.test(await tabUrl(run.app, id)), 20_000, `tab ${id} signed in`);
  };
  const tabOf = async (id) => (await shellState(run.shell)).tabs.find((x) => x.id === id);
  const warningOpen = () => run.shell.evaluate((title) => [...document.querySelectorAll('dialog[open]')].some((d) => d.textContent.includes(title)), WARNING_TITLE);

  await t.test('S1 first launch', async () => {
    run = await launch(sb, { env });
    const { app, shell } = run;
    assert.ok(shell.url().startsWith('app://shell/'), shell.url());
    // The CSP header (a reload, so the response is seen).
    const resp = await shell.reload();
    assert.equal(resp.headers()['content-security-policy'], SHELL_CSP);
    await shell.waitForLoadState('load');
    const keys = await shell.evaluate(() => Object.keys(window.wy).sort());
    assert.deepEqual(keys, [...Object.keys(FUNCTIONS), 'on'].sort());
    assert.deepEqual(await shell.evaluate(() => [typeof require, typeof process]), ['undefined', 'undefined']);
    assert.equal(await app.evaluate(({ app: a }) => a.getPath('userData')), sb.userData);
    const st = await shellState(shell);
    assert.equal(st.view, 'hub');
    assert.equal(st.hub.status, 'needs_pairing', JSON.stringify(st.hub));
    await sleep(500);
    assert.deepEqual(run.errors, [], 'no console errors');
  });

  await t.test('pairing (one click, local admin channel)', async () => {
    const r = await run.shell.evaluate(() => window.wy.pair({}));
    assert.equal(r.ok, true, JSON.stringify(r));
    await waitFor(async () => (await shellState(run.shell)).hub.status === 'ok', 20_000, 'hub status ok');
    // basic_text (no keyring in the sandbox): the file is written with mode 0600 and Settings warns.
    const st = fs.statSync(path.join(sb.userData, 'hub-token.bin'));
    if (process.platform !== 'win32') assert.equal(st.mode & 0o777, 0o600);
    assert.doesNotMatch(fs.readFileSync(path.join(sb.userData, 'hub-token.bin'), 'latin1'), /wy_d_/);
    if (process.platform === 'linux') assert.equal((await shellState(run.shell)).settings.storage_backend, 'basic_text');
  });

  await t.test('S2 two tabs, isolated partitions', async () => {
    ids.work = (await run.shell.evaluate(() => window.wy.addTab({ name: 'work' }))).id;
    ids.home = (await run.shell.evaluate(() => window.wy.addTab({ name: 'home' }))).id;
    // Stay in Settings, so the warning of a tab is not opened by selecting it.
    await run.shell.evaluate(() => window.wy.selectView({ view: 'settings' }));
    await signIn(ids.work);
    await signIn(ids.home);
    await inTab(run.app, ids.work, "document.cookie = 'probe=work; path=/; max-age=86400'");
    assert.match(await inTab(run.app, ids.work, 'document.cookie'), /probe=work/);
    assert.doesNotMatch(await inTab(run.app, ids.home, 'document.cookie'), /probe=/);
    const keys = await main(run.app, async (m, e, a) => {
      const get = async (id) => (await e.session.fromPartition(`persist:tab-${id}`).cookies.get({ name: 'sessionKey' })).map((c) => c.value);
      return [await get(a.work), await get(a.home)];
    }, ids);
    assert.equal(keys[0].length, 1);
    assert.equal(keys[1].length, 1);
    assert.notEqual(keys[0][0], keys[1][0], 'each tab has its own session');
  });

  await t.test('S3 warning once per tab', async () => {
    await waitFor(async () => (await tabOf(ids.work))?.needs_warning, 10_000, 'work needs the warning');
    assert.equal(await usageCount(), 0, 'no usage request before the warning is accepted');
    await run.shell.evaluate((id) => window.wy.selectView({ view: id }), ids.work);
    await waitFor(warningOpen, 10_000, 'the warning dialog');
    await sleep(2 * CADENCE_MS);
    assert.equal(await usageCount(), 0, 'still none while the dialog is open');
    await run.shell.getByRole('button', { name: ACCEPT, exact: true }).click();
    await waitFor(async () => (await usageCount()) > 0, 20_000, 'reads after accepting');
    const work = await tabOf(ids.work);
    assert.equal(work.needs_warning, false);
    assert.equal(work.reading, 'on');
    // The second tab asks for itself.
    assert.equal((await tabOf(ids.home)).needs_warning, true);
    const stored = JSON.parse(fs.readFileSync(path.join(sb.userData, 'app-state.json'), 'utf8'));
    assert.equal(stored.tabs.find((x) => x.id === ids.work).warning_accepted, WARNING_VERSION);
  });

  await t.test('S4 reading reaches the hub, and the board shows the same numbers', async () => {
    const acct = uuidOf('acct', 1);
    const seat = await waitFor(async () => {
      const { accounts } = await hub.get('/v1/accounts');
      const s = accounts.find((a) => a.account === acct && (a.places ?? []).some((p) => p.tab_id === ids.work));
      return s && (s.windows ?? []).some((w) => w.key === 'five_hour' && w.used_pct === 42) ? s : null;
    }, 30_000, 'the seat with the fixture numbers on the hub');
    const byKey = Object.fromEntries(seat.windows.map((w) => [w.key, w.used_pct]));
    assert.equal(byKey.five_hour, 42);
    assert.equal(byKey.seven_day, 61);
    ids.seat = seat.id;

    await run.shell.evaluate(() => window.wy.hubRoute({ route: '#/accounts' }));
    const board = await waitFor(() => run.app.windows().find((p) => p.url().startsWith(hub.url)), 20_000, 'the hub view page');
    // The board was loaded at pairing, before the seat existed; its stream only updates seats
    // it already has (webui store.js applyUsage), so load it again for the new seat.
    await board.reload();
    // The list row shows only the tighter window; every window is in the row's detail, so open it.
    await waitFor(async () => board.evaluate((id) => {
      const b = document.querySelector(`[data-key="seat-toggle-${id}"]`);
      if (b && b.getAttribute('aria-expanded') !== 'true') b.click();
      return !!document.getElementById(`seat-detail-${id}`);
    }, seat.id), 20_000, 'the seat row opened on the board');
    const values = await waitFor(async () => {
      const v = await board.evaluate((id) => [...document.querySelectorAll(`#seat-detail-${id} [role=meter]`)].map((m) => m.getAttribute('aria-valuenow')), seat.id);
      return v.length ? v : null;
    }, 20_000, 'the seat card on the board').catch(async (err) => {
      fs.writeFileSync(path.join(out, 's4-board.html'), `${board.url()}\n${await board.content()}`);
      await board.screenshot({ path: path.join(out, 's4-board.png') }).catch(() => {});
      throw err;
    });
    const expected = seat.windows.filter((w) => w.used_pct != null).map((w) => String(Math.round(w.used_pct)));
    assert.deepEqual([...values].filter((x) => x !== null).sort(), [...expected].sort());
    await run.shell.screenshot({ path: path.join(out, 's4-shell.png') });
  });

  const outboxOf = async (pred) => outboxRecords(await hub.outboxDir(), (r) => r.kind === 'web_usage' && pred(r));
  const ofTab = (id) => (r) => r.payload?.tab_id === id;
  // On a failure: what main knew (written next to the screenshots).
  const dump = async (name) => {
    try {
      const d = await main(run.app, (m) => ({
        hub: m.bus.last('hub'), needs_you: m.bus.last('needs_you'), selected: m.window.selected(),
        view: m.hub.view()?.webContents.getURL() ?? null, pages: null,
      }));
      d.pages = run.app.windows().map((p) => p.url());
      fs.writeFileSync(path.join(out, `${name}.json`), JSON.stringify(d, null, 2));
      fs.writeFileSync(path.join(out, `${name}-main.log`), run.mainLog.join(''));
    } catch (err) {
      fs.writeFileSync(path.join(out, `${name}.json`), String(err));
    }
  };
  const step = (name, fn) => t.test(name, async (tt) => {
    try {
      await fn(tt);
    } catch (err) {
      await dump(name.split(' ')[0]);
      throw err;
    }
  });

  await step('S5 turning reading off stops a read in flight and every later one', async () => {
    await script({ path: 'usage', delay: 3000 });
    const before = await usageCount();
    await run.shell.evaluate((id) => window.wy.refreshUsage({ id }), ids.work);
    await waitFor(async () => (await usageCount()) > before, 10_000, 'a read in flight');
    // The delayed request is held for 3 s; a read that finished just before it may still be
    // appending, so let that settle before counting.
    await sleep(400);
    const recs0 = (await outboxOf(ofTab(ids.work))).length;
    await run.shell.evaluate((id) => window.wy.setReading({ id, on: false }), ids.work);
    const n0 = await usageCount();
    await sleep(3 * CADENCE_MS + 3500);
    assert.equal(await usageCount(), n0, 'no new usage request over 3 cadences');
    assert.equal((await outboxOf(ofTab(ids.work))).length, recs0, 'no new record in the outbox (the delayed read was dropped)');
    assert.equal((await tabOf(ids.work)).read_state, 'off');
  });

  await step('S6 a 429 stops reading with one error record per seat; Resume reads again', async () => {
    await run.shell.evaluate((id) => window.wy.setReading({ id, on: true }), ids.work);
    await waitFor(async () => (await tabOf(ids.work)).read_state !== 'off', 10_000, 'reading on again');
    await script({ path: 'usage', status: 429 });
    const errs = async () => (await outboxOf((r) => ofTab(ids.work)(r) && r.payload.error)).length;
    const e0 = await errs();
    await waitFor(async () => (await tabOf(ids.work)).read_state === 'stopped', 20_000, 'the tab stopped');
    const tab = await tabOf(ids.work);
    assert.equal(tab.stopped.status, 429);
    await waitFor(async () => (await errs()) === e0 + 1, 10_000, 'one error record');
    const n0 = await usageCount();
    await sleep(3 * CADENCE_MS + 500);
    assert.equal(await usageCount(), n0, 'no further requests while stopped');
    assert.equal(await errs(), e0 + 1, 'exactly one error record for the one seat');
    // The strip says so.
    await run.shell.evaluate((id) => window.wy.selectView({ view: id }), ids.work);
    await waitFor(() => run.shell.evaluate(() => document.body.innerText.includes('Stopped')), 10_000, 'the strip shows Stopped');
    // The hub knows why.
    await hub.wy(['flush', '--quiet']);
    await waitFor(async () => {
      const { accounts } = await hub.get('/v1/accounts');
      const place = accounts.flatMap((a) => a.places ?? []).find((p) => p.tab_id === ids.work);
      return place?.stopped_reason === '429';
    }, 15_000, 'the hub place with stopped_reason 429');
    await run.shell.evaluate((id) => window.wy.resumeReading({ id }), ids.work);
    await waitFor(async () => (await usageCount()) > n0, 15_000, 'Resume reads again');
    await waitFor(async () => (await tabOf(ids.work)).stopped === null, 10_000, 'not stopped');
  });

  await step('S7 sign-ins and isolation survive a restart', async () => {
    const before = await stats();
    const sessionsBefore = Object.keys(before.sessions).filter((k) => k !== '(none)').sort();
    await run.close();
    run = await launch(sb, { env });
    await waitFor(async () => {
      const st = await shellState(run.shell);
      return st.tabs.length === 2 && st.tabs.every((x) => x.signed_in);
    }, 30_000, 'both tabs signed in after the restart');
    const key = (await main(run.app, async (m, e, id) => (await e.session.fromPartition(`persist:tab-${id}`).cookies.get({ name: 'sessionKey' }))[0]?.value, ids.work));
    const acct0 = before.sessions[key]?.account ?? 0;
    await waitFor(async () => ((await stats()).sessions[key]?.account ?? 0) > acct0, 20_000, '/api/account read with the old session');
    const sessionsAfter = Object.keys((await stats()).sessions).filter((k) => k !== '(none)').sort();
    assert.deepEqual(sessionsAfter, sessionsBefore, 'no new login');
    assert.match(await inTab(run.app, ids.work, 'document.cookie'), /probe=work/);
    assert.doesNotMatch(await inTab(run.app, ids.home, 'document.cookie'), /probe=/);
    // The warning is not asked again for the accepted tab, and still asked for the other one.
    assert.equal((await tabOf(ids.work)).needs_warning, false);
    assert.equal((await tabOf(ids.home)).needs_warning, true);
  });

  await step('S8 hub view: cookie login, owner sign-in, seat label edit', async () => {
    await run.shell.evaluate(() => window.wy.selectView({ view: 'hub' }));
    // Through main, not run.app.windows(): after the restart the hub view exists before Playwright attached, and
    // Playwright then never lists it (the page is fine: loaded, visible, signed in).
    const hubPage = (fn) => main(run.app, (m, _e, code) => m.hub.view()?.webContents.executeJavaScript(code), `(${fn})()`);
    await waitFor(() => main(run.app, (m, e, url) => m.hub.view()?.webContents.getURL().startsWith(url) && !m.hub.view().webContents.isLoading(), hub.url), 20_000, 'the hub view page');
    const leaks = await hubPage(() => {
      const all = [document.cookie, JSON.stringify({ ...localStorage }), JSON.stringify({ ...sessionStorage }), document.documentElement.outerHTML].join('\n');
      return /wy_d_[A-Za-z0-9]{10,}/.test(all);
    });
    assert.equal(leaks, false, 'the desktop token is never in the page');
    const me0 = await main(run.app, async (m, e, url) => (await e.session.fromPartition('persist:wy-hub').fetch(`${url}/v1/me`)).json(), hub.url);
    assert.equal(me0.owner ?? false, false);
    const r = await run.shell.evaluate(() => window.wy.ownerSignIn({}));
    assert.equal(r.ok, true, JSON.stringify(r));
    await waitFor(async () => (await main(run.app, async (m, e, url) => (await e.session.fromPartition('persist:wy-hub').fetch(`${url}/v1/me`)).json(), hub.url)).owner === true,
      20_000, 'an owner session in persist:wy-hub');
    await waitFor(async () => (await shellState(run.shell)).hub.owner === true, 10_000, 'ShellState hub.owner');
    const work = await waitFor(async () => (await tabOf(ids.work))?.seat, 10_000, 'the work tab knows its seat');
    assert.equal(work, ids.seat);
    const u = await run.shell.evaluate((seat) => window.wy.updateSeat({ seat, label: 'smoke-work', colour: '#0e7c76' }), ids.seat);
    assert.equal(u.ok, true, JSON.stringify(u));
    await waitFor(async () => (await tabOf(ids.work)).label === 'smoke-work', 15_000, 'the tab shows the new label');
    assert.equal((await tabOf(ids.work)).colour, '#0e7c76');
  });

  await step('S10 navigation and IPC guards of an account tab', async () => {
    await main(run.app, (m, e) => {
      globalThis.__ext = [];
      e.shell.openExternal = async (u) => {
        globalThis.__ext.push(u);
      };
    });
    const at = await tabUrl(run.app, ids.work);
    await inTab(run.app, ids.work, "document.getElementById('ext').click()");
    await waitFor(() => main(run.app, () => globalThis.__ext.length === 1), 10_000, 'openExternal called');
    await sleep(500);
    assert.equal(await tabUrl(run.app, ids.work), at, 'the tab stayed put');
    assert.deepEqual(await main(run.app, () => globalThis.__ext), ['https://example.com/']);
    assert.equal(await inTab(run.app, ids.work, "window.open('javascript:alert(1)') === null"), true, 'javascript: window.open denied');
    await sleep(300);
    assert.equal(await main(run.app, () => globalThis.__ext.length), 1, 'nothing else opened');
    assert.deepEqual(await inTab(run.app, ids.work, '[typeof window.wy, typeof require, typeof process]'), ['undefined', 'undefined', 'undefined']);
    const refused = await main(run.app, async (m, e, id) => {
      const handlers = e.ipcMain._invokeHandlers;
      const fn = handlers?.get?.('state:get');
      if (!fn) return 'no-access';
      const wc = m.tabs.view(id).webContents;
      try {
        await fn({ sender: wc, senderFrame: wc.mainFrame, frameId: wc.mainFrame.routingId, processId: wc.mainFrame.processId });
        return 'allowed';
      } catch (err) {
        return String(err?.message ?? err);
      }
    }, ids.work);
    if (refused !== 'no-access') assert.match(refused, /refused/);

    // Subframes (C2): one on the allow-list loads; one elsewhere (the hub's origin) is blocked.
    const frames = () => main(run.app, (m, _e, id) => m.tabs.view(id).webContents.mainFrame.frames.map((f) => f.url), ids.work);
    await inTab(run.app, ids.work, `(() => { for (const [k, u] of [['ok', ${JSON.stringify(`${claude.origin}/_fake/stats`)}], ['bad', ${JSON.stringify(`${hub.url}/health`)}]]) { const f = document.createElement('iframe'); f.id = 'sf-' + k; f.src = u; document.body.append(f); } return true; })()`);
    await waitFor(async () => (await frames()).includes(`${claude.origin}/_fake/stats`), 10_000, 'the allowed subframe loaded');
    await sleep(1000);
    assert.ok(!(await frames()).some((u) => u.startsWith(hub.url)), 'the subframe to another host was blocked');
    await waitFor(() => /subframe to 127\.0\.0\.1:\d+ blocked in a tab view/.test(run.mainLog.join('')), 5000, 'the blocked host logged');
    await inTab(run.app, ids.work, "document.querySelectorAll('iframe[id^=sf-]').forEach((f) => f.remove()), true");
  });

  await step('S9 a hub alert gives one OS notification; a click routes the hub view', async () => {
    await main(run.app, (m, e) => {
      globalThis.__notes = [];
      const N = e.Notification;
      N.prototype.show = function show() {
        globalThis.__notes.push(this);
      };
      try {
        N.isSupported = () => true;
      } catch {
        // already true
      }
    });
    const cookie = await hub.ownerCookie();
    const { rules } = await hub.ownerFetch(cookie, 'GET', '/v1/rules');
    const asked = rules.find((r) => r.trigger === 'asked' && r.base);
    await hub.ownerFetch(cookie, 'PUT', `/v1/rules/${asked.id}`, { enabled: true, delay_s: 1 });
    const sid = '7d3f0c4e-1b2a-4c5d-9e8f-0a1b2c3d4e5f';
    const hookIn = {
      session_id: sid, hook_event_name: 'Stop', cwd: path.join(sb.home, 'api'), transcript_path: path.join(sb.home, '.claude', 'projects', 'x', `${sid}.jsonl`),
      last_assistant_message: 'Done with the schema.\n\nShould I also update the migration?',
    };
    const h = await hub.wy(['hook', 'Stop'], { stdin: JSON.stringify(hookIn) });
    assert.equal(h.code, 0, h.stderr);
    const f = await hub.wy(['flush']);
    assert.equal(f.code, 0, f.stderr);
    const notes = await waitFor(async () => {
      const n = await main(run.app, () => globalThis.__notes.map((x) => ({ title: x.title, body: x.body })));
      return n.length ? n : null;
    }, 30_000, 'a notification');
    assert.equal(notes.length, 1);
    const { alerts } = await hub.get('/v1/alerts');
    const alert = alerts.find((a) => a.trigger === 'asked');
    assert.ok(alert, JSON.stringify(alerts));
    const session = await hub.get(`/v1/sessions/${alert.session}`);
    const { notifyText } = await import('../../src/shared/notify-text.js');
    const golden = notifyText({ alert: await hub.get(`/v1/alerts/${alert.id}`), session, seat: null });
    assert.deepEqual(notes[0], { title: golden.title, body: golden.body });
    ids.session = alert.session;
    // A second `fired` for the same alert id gives no second notification (dedupe per run).
    await main(run.app, (m, _e, a) => m.bus.emit('alert', { alert: { id: a }, session: null }), alert.id);
    await sleep(300);
    assert.equal(await main(run.app, () => globalThis.__notes.length), 1);
    // Click → the window shows the hub view at the session.
    await main(run.app, () => globalThis.__notes[0].emit('click'));
    await waitFor(async () => {
      const st = await shellState(run.shell);
      const url = await main(run.app, (m) => m.hub.view()?.webContents.getURL() ?? '');
      return st.view === 'hub' && url.includes(`#/sessions/${ids.session}`);
    }, 15_000, 'the hub view on the session');
  });

  await step('S11 tray badge and needs-you menu', async () => {
    const tray = await waitFor(async () => {
      const r = await main(run.app, (m) => ({
        available: m.tray.available(),
        count: m.tray.count(),
        icon: m.tray.iconName(),
        labels: m.tray.buildMenu().items.map((i) => i.label),
      }));
      return r.count === 1 ? r : null;
    }, 20_000, 'needs-you count 1');
    assert.match(tray.icon, /^(tray-dot-\d+\.png|trayDotTemplate\.png)$/);
    assert.ok(tray.labels.some((l) => typeof l === 'string' && l.includes(' · api · ')), JSON.stringify(tray.labels));
    assert.equal((await shellState(run.shell)).hub.needs_you, 1);
    if (!tray.available) t.diagnostic('no tray icon on this display (the count and menu still follow needs_you)');
  });

  await step('S14 a hub account with no tab: the notice, the offer in "+", a tab made for it', async () => {
    // A second account on the hub that this computer has no tab for (a record from "another machine").
    const acct2 = uuidOf('acct', 2);
    const org2 = uuidOf('org', 7);
    const machine = await main(run.app, (m) => m.wy.machineId());
    const usage = JSON.parse(fs.readFileSync(fixture, 'utf8'));
    const recs = buildRecords({
      orgs: [{ org: { uuid: org2, name: 'seven', capabilities: ['chat'], rate_limit_tier: 't', billing_type: 'b', raven_type: null }, usage }],
      account: { uuid: acct2, email_address: 'seven@example.com', full_name: 'Seven', display_name: 'Seven' },
      activeOrg: org2, fetchMs: 5, tabId: 't_00000000000000a7', machine, client: clientOf('0.0.0'), ts: new Date().toISOString(),
    });
    await main(run.app, (m, _e, r) => m.wy.append(r), recs);
    await hub.wy(['flush', '--quiet']);
    const seven = await waitFor(async () => (await hub.get('/v1/accounts')).accounts.find((a) => a.account === acct2), 30_000, 'the second account on the hub');
    const cookie = await hub.ownerCookie();
    await hub.ownerFetch(cookie, 'PATCH', `/v1/accounts/${seven.id}`, { label: 'claude-7' });
    ids.seven = seven.id;
    await waitFor(async () => ((await shellState(run.shell)).missing ?? []).some((x) => x.label === 'claude-7'), 30_000, 'claude-7 listed as having no tab');
    await run.shell.evaluate(() => window.wy.selectView({ view: 'hub' }));
    await waitFor(() => run.shell.evaluate(() => document.body.innerText.includes('claude-7 has no tab here.')), 10_000, 'the notice in the Hub tab');
    await run.shell.screenshot({ path: path.join(out, 's14-hub-notice.png') });
    // "+" offers the account next to the blank tab.
    await run.shell.locator('[data-fk="add-tab"]').click();
    await run.shell.locator('[data-fk="add-for-claude-7"]').waitFor({ timeout: 10_000 });
    assert.equal(await run.shell.evaluate(() => !!document.querySelector('#new-tab-name')), true, 'the blank "New tab" form stays');
    await run.shell.screenshot({ path: path.join(out, 's14-add-dialog.png') });
    await run.shell.locator('[data-fk="add-for-claude-7"]').click();
    const made = await waitFor(async () => (await shellState(run.shell)).tabs.find((x) => x.expected === 'claude-7'), 10_000, 'the tab made for claude-7');
    ids.seventab = made.id;
    assert.equal(made.name, 'claude-7');
    const stored = JSON.parse(fs.readFileSync(path.join(sb.userData, 'app-state.json'), 'utf8')).tabs.find((x) => x.id === made.id);
    assert.deepEqual(stored.expect, { seat: ids.seven, account: acct2, org: org2, label: 'claude-7' });
    assert.deepEqual((await shellState(run.shell)).missing, [], 'nothing missing any more');
    // Adding again for the same account makes no second tab.
    const again = await run.shell.evaluate((seat) => window.wy.addTab({ name: 'claude-7', seat }), ids.seven);
    assert.equal(again.id, made.id);
    assert.equal((await shellState(run.shell)).tabs.filter((x) => x.expected === 'claude-7').length, 1);
  });

  await step('S15 a tab signed in as another account says so; a seat link never opens in another account\'s tab', async () => {
    const acct2 = uuidOf('acct', 2);
    // The fake claude.ai answers as account 1 only: signing in here is the wrong account for claude-7.
    await run.shell.evaluate(() => window.wy.selectView({ view: 'settings' }));
    await signIn(ids.seventab);
    await run.shell.evaluate((v) => window.wy.acceptWarning({ id: v.id, version: v.version }), { id: ids.seventab, version: WARNING_VERSION });
    await run.shell.evaluate((id) => window.wy.setReading({ id, on: true }), ids.seventab);
    const wrong = await waitFor(async () => (await tabOf(ids.seventab))?.mismatch, 30_000, 'the tab sees it is signed in as another account');
    assert.equal(wrong.expected, 'claude-7');
    await run.shell.evaluate((id) => window.wy.selectView({ view: id }), ids.seventab);
    await waitFor(() => run.shell.evaluate(() => document.body.innerText.includes('Sign out and sign in as claude-7')), 10_000, 'the notice and the sign-out option on the tab');
    assert.equal(await run.shell.evaluate(() => document.querySelector('[data-fk="strip-adopt"]')?.textContent), `Use this tab for ${wrong.actual}`);
    await run.shell.screenshot({ path: path.join(out, 's15-mismatch.png') });

    // Links: loads are recorded, never sent (the link is on the real claude.ai).
    // A tab made for a link loads it as its first page, so pages made from here on are wrapped
    // as they are created (before their first load), not only the tabs that exist now.
    await main(run.app, (m, e) => {
      globalThis.__loads = [];
      const wrap = (wc) => {
        if (wc && !wc.__wrapped) {
          wc.__wrapped = true;
          wc.loadURL = async (u) => { globalThis.__loads.push([wc, u]); };
        }
      };
      for (const t of m.tabs.list()) wrap(m.tabs.view(t.id)?.webContents);
      globalThis.__wrapNew = (_ev, wc) => wrap(wc);
      e.app.on('web-contents-created', globalThis.__wrapNew);
    });
    const link = 'https://claude.ai/code/session_01AbCdEf';
    const open = (seat) => main(run.app, (m, _e, a) => m.tabs.openLink(a.link, a.seat), { link, seat });
    const loads = () => main(run.app, (m) => globalThis.__loads.map(([wc, u]) => [m.tabs.list().find((t) => m.tabs.view(t.id)?.webContents === wc)?.id ?? null, u]));
    // Account 2's link goes to the tab made for it (its signed-in account is another one, but it is the tab for 2)...
    assert.equal(await open({ id: ids.seven, account: acct2, org: uuidOf('org', 7) }), true);
    assert.deepEqual(await loads(), [[ids.seventab, link]]);
    // ...account 1's link to the tab signed in as account 1 (the first such tab), never to the claude-7 tab.
    assert.equal(await open({ id: ids.seat, account: uuidOf('acct', 1), org: uuidOf('org', 1) }), true);
    assert.equal((await loads())[1][0], ids.work);
    // An account with no tab at all gets one, named by its label when the hub knows it.
    const before = (await shellState(run.shell)).tabs.length;
    assert.equal(await open({ id: 'seat_X', account: uuidOf('acct', 3), org: uuidOf('org', 3) }), true);
    const st = await shellState(run.shell);
    assert.equal(st.tabs.length, before + 1);
    assert.equal(st.view, st.tabs.at(-1).id, 'the new tab is shown');
    await waitFor(async () => (await loads()).some(([id, u]) => id === st.tabs.at(-1).id && u === link), 10_000, 'the new tab opens the link');
    await main(run.app, (_m, e) => e.app.off('web-contents-created', globalThis.__wrapNew));
    await waitFor(() => run.shell.evaluate((id) => document.querySelector(`[data-fk="tab-${id}"]`)?.getAttribute('aria-selected') === 'true', st.tabs.at(-1).id), 10_000, 'the new tab selected in the bar');
    await run.shell.screenshot({ path: path.join(out, 's15-new-tab-for-link.png') });

    // "Use this tab for ...": another tab is already signed in as this account, so a confirm comes first.
    const actual = wrong.actual;
    assert.ok(actual, 'the hub knows the account the tab is signed in as');
    assert.equal(wrong.shared, true);
    await run.shell.evaluate((id) => window.wy.selectView({ view: id }), ids.seventab);
    await run.shell.locator('[data-fk="strip-adopt"]').click();
    await run.shell.locator('[data-fk="adopt-confirm"]').waitFor({ timeout: 10_000 });
    assert.equal(await run.shell.locator('[data-fk="adopt-confirm"]').textContent(), `Another tab is already signed in as ${actual}. Use this one too?`);
    await run.shell.screenshot({ path: path.join(out, 's15-adopt-confirm.png') });
    await run.shell.locator('dialog[open] button', { hasText: 'Cancel' }).click();
    assert.equal((await tabOf(ids.seventab)).name, 'claude-7', 'Cancel changes nothing');
    await run.shell.locator('[data-fk="strip-adopt"]').click();
    await run.shell.locator('[data-fk="adopt-yes"]').click();
    const adopted = await waitFor(async () => { const x = await tabOf(ids.seventab); return x && !x.mismatch ? x : null; }, 10_000, 'the notice gone');
    assert.equal(adopted.name, actual);
    assert.equal(adopted.expected, actual);
    const rec = JSON.parse(fs.readFileSync(path.join(sb.userData, 'app-state.json'), 'utf8')).tabs.find((x) => x.id === ids.seventab);
    assert.equal(rec.expect.account, uuidOf('acct', 1));
    assert.equal(rec.name, actual);
    await run.shell.locator('[data-fk="toast-action"]').waitFor({ timeout: 10_000 });
    assert.equal(await run.shell.evaluate(() => document.querySelector('.toast-text').textContent), `This tab is now ${actual}. Add a tab for claude-7 to sign in to it.`);
    await waitFor(() => run.shell.evaluate(() => !document.querySelector('[data-fk="strip-mismatch"]')), 10_000, 'the strip notice gone');
    assert.ok(((await shellState(run.shell)).missing ?? []).some((x) => x.label === 'claude-7'), 'claude-7 has no tab now');
    await run.shell.screenshot({ path: path.join(out, 's15-adopted-toast.png') });
    const nTabs = (await shellState(run.shell)).tabs.length;
    await run.shell.locator('[data-fk="toast-action"]').click();
    const fresh = await waitFor(async () => { const st = await shellState(run.shell); return st.tabs.length === nTabs + 1 ? st.tabs.at(-1) : null; }, 10_000, 'a fresh tab for claude-7');
    assert.equal(fresh.name, 'claude-7');
    assert.equal(fresh.expected, 'claude-7');
    assert.equal((await shellState(run.shell)).view, fresh.id, 'the fresh tab is shown');
    assert.deepEqual(((await shellState(run.shell)).missing ?? []).filter((x) => x.label === 'claude-7'), []);
    await waitFor(() => run.shell.evaluate((id) => document.querySelector(`[data-fk="tab-${id}"]`)?.getAttribute('aria-selected') === 'true', fresh.id), 10_000, 'the fresh tab selected in the bar');
    await run.shell.screenshot({ path: path.join(out, 's15-fresh-tab-for-claude-7.png') });
  });

  await step('no console errors in any page', () => {
    const real = run.errors.filter((e) => !/fonts\.(googleapis|gstatic)\.com/.test(e));
    assert.deepEqual(real, []);
  });
});
