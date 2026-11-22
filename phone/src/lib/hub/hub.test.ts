import { fakeFetch, hangingFetch, manualClock, ORIGIN, response, TOKEN } from '../../../test/hubfake';
import type { FakeFetch } from '../../../test/hubfake';
import { createHubClient, errorText, flushPendingRevokes, HubError, movedOrigin, normalizeOrigin, OWNER_ONLY, revokeDevice } from './index';
import type { HubClient } from './index';

const BODY_SECRET = 'LEAKED-BODY-TEXT-wy_p_ZZZ';

function client(f: FakeFetch, onUnauthorized?: () => void, clock = manualClock()): HubClient {
  return createHubClient({ origin: ORIGIN, token: TOKEN, fetch: f, clock, onUnauthorized });
}

/** Every endpoint, called with plausible arguments. */
const ENDPOINTS: [string, (c: HubClient) => Promise<unknown>][] = [
  ['me', (c) => c.me()],
  ['overview', (c) => c.overview()],
  ['sessions', (c) => c.sessions({ filter: 'needs_you', limit: 50, cursor: 'c1' })],
  ['session', (c) => c.session('s/1')],
  ['accounts', (c) => c.accounts()],
  ['machines', (c) => c.machines()],
  ['alerts', (c) => c.alerts({ limit: 10 })],
  ['alert', (c) => c.alert('al_1')],
  ['rules', (c) => c.rules()],
  ['seen', (c) => c.seen('al_1')],
  ['snooze', (c) => c.snooze('al_1', 30)],
  ['deviceSelf', (c) => c.deviceSelf()],
  ['patchSelf', (c) => c.patchSelf({ private: true })],
  ['testTarget', (c) => c.testTarget('pt_1')],
  ['matrix', (c) => c.matrix()],
  ['editMatrix', (c) => c.editMatrix({ base_version: 2, deny: [['a', 'b']] })],
  ['pausePeers', (c) => c.pausePeers(true)],
  ['peerAudit', (c) => c.peerAudit({ limit: 20 })],
  ['peerMessages', (c) => c.peerMessages()],
  ['peers', (c) => c.peers()],
  ['trustedSenders', (c) => c.trustedSenders()],
  ['revokeTrustedSender', (c) => c.revokeTrustedSender('m1')],
  ['patchSeat', (c) => c.patchSeat('seat-1', { mode: 'protected' })],
  ['relation', (c) => c.relation('seat-1')],
];

describe('HubClient requests', () => {
  it.each<[string, (c: HubClient) => Promise<unknown>, string, string]>([
    ['me', (c) => c.me(), 'GET', '/v1/me'],
    ['overview', (c) => c.overview(), 'GET', '/v1/overview'],
    ['sessions', (c) => c.sessions({ filter: 'needs_you', limit: 50, cursor: 'a b' }), 'GET', '/v1/sessions?filter=needs_you&limit=50&cursor=a%20b'],
    ['sessions bare', (c) => c.sessions(), 'GET', '/v1/sessions'],
    ['sessions with ended', (c) => c.sessions({ includeEnded: true, days: 2, limit: 200 }), 'GET', '/v1/sessions?include_ended=true&days=2&limit=200'],
    ['session', (c) => c.session('c41e/0'), 'GET', '/v1/sessions/c41e%2F0'],
    ['accounts', (c) => c.accounts(), 'GET', '/v1/accounts'],
    ['machines', (c) => c.machines(), 'GET', '/v1/machines'],
    ['alerts', (c) => c.alerts({ limit: 10 }), 'GET', '/v1/alerts?limit=10'],
    ['alert', (c) => c.alert('al_1'), 'GET', '/v1/alerts/al_1'],
    ['rules', (c) => c.rules(), 'GET', '/v1/rules'],
    ['seen', (c) => c.seen('al_1'), 'POST', '/v1/alerts/al_1/seen'],
    ['seenSession', (c) => c.seenSession('c41e/0'), 'POST', '/v1/sessions/c41e%2F0/seen'],
    ['snooze default', (c) => c.snooze('al_1'), 'POST', '/v1/alerts/al_1/snooze'],
    ['deviceSelf', (c) => c.deviceSelf(), 'GET', '/v1/devices/self'],
    ['patchSelf', (c) => c.patchSelf({ tap: 'claude' }), 'PATCH', '/v1/devices/self'],
    ['unpairSelf', (c) => c.unpairSelf(), 'DELETE', '/v1/devices/self'],
    ['testTarget', (c) => c.testTarget('pt_1'), 'POST', '/v1/push/targets/pt_1/test'],
    ['matrix', (c) => c.matrix(), 'GET', '/v1/peers/matrix'],
    ['editMatrix', (c) => c.editMatrix({ deny: [['a', 'b']] }), 'PUT', '/v1/peers/matrix'],
    ['pausePeers', (c) => c.pausePeers(true), 'POST', '/v1/peers/pause'],
    ['peerAudit', (c) => c.peerAudit({ limit: 20 }), 'GET', '/v1/peers/audit?limit=20'],
    ['peerAudit bare', (c) => c.peerAudit(), 'GET', '/v1/peers/audit'],
    ['patchSeat', (c) => c.patchSeat('a/b', { mode: null }), 'PATCH', '/v1/accounts/a%2Fb'],
    ['relation', (c) => c.relation('a/b'), 'GET', '/v1/accounts/a%2Fb/relation'],
    ['peerMessages', (c) => c.peerMessages({ limit: 5 }), 'GET', '/v1/peers/messages?limit=5'],
    ['peers', (c) => c.peers(), 'GET', '/v1/peers'],
    ['trustedSenders', (c) => c.trustedSenders(), 'GET', '/v1/peers/trusted'],
    ['revokeTrustedSender', (c) => c.revokeTrustedSender('m/1'), 'POST', '/v1/peers/trusted/revoke'],
  ])('%s sends the bearer to the right path', async (_n, run, method, path) => {
    const f = fakeFetch(() => response(200, {}));
    await run(client(f));
    expect(f.calls).toHaveLength(1);
    const c = f.calls[0]!;
    expect(c.url).toBe(`${ORIGIN}${path}`);
    expect(c.init.method).toBe(method);
    expect(c.init.headers?.Authorization).toBe(`Bearer ${TOKEN}`);
  });

  it('sends JSON bodies', async () => {
    const f = fakeFetch(() => response(200, {}));
    const c = client(f);
    await c.editMatrix({ base_version: 2, deny: [['a', 'b']] });
    await c.pausePeers(true);
    await c.revokeTrustedSender('m1');
    await c.snooze('al_1', 30);
    await c.patchSelf({ private: true, widgets: true });
    expect(f.calls.map((x) => JSON.parse(x.init.body!))).toEqual([
      { edit: { base_version: 2, deny: [['a', 'b']] } },
      { paused: true },
      { machine: 'm1', sender: 'doorbell' },
      { minutes: 30 },
      { prefs: { private: true, widgets: true } },
    ]);
    expect(f.calls.every((x) => x.init.headers?.['Content-Type'] === 'application/json')).toBe(true);
  });

  it('returns the parsed JSON', async () => {
    const f = fakeFetch(() => response(200, { time: 't', seats: [], needs_you: [], machines: [], counts: {} }));
    await expect(client(f).overview()).resolves.toMatchObject({ time: 't' });
  });

  it('health goes to /health without the token, within 1.5 s, and never throws', async () => {
    const f = fakeFetch(() => response(200, { ok: true }));
    expect(await client(f).health()).toBe(true);
    expect(f.calls[0]!.url).toBe(`${ORIGIN}/health`);
    expect(f.calls[0]!.init.headers?.Authorization).toBeUndefined();

    expect(await client(fakeFetch(() => response(503))).health()).toBe(false);
    expect(await client(fakeFetch(() => Promise.reject(new Error('down')))).health()).toBe(false);

    const clock = manualClock();
    const p = client(hangingFetch(), undefined, clock).health();
    await clock.advance(1_499);
    let settled = false;
    void p.then(() => (settled = true));
    await Promise.resolve();
    expect(settled).toBe(false);
    await clock.advance(2);
    expect(await p).toBe(false);
  });

  it('times out after 10 s', async () => {
    const clock = manualClock();
    const p = client(hangingFetch(), undefined, clock)
      .me()
      .catch((e) => e);
    await clock.advance(9_999);
    expect(clock.pending()).toBe(1);
    await clock.advance(2);
    const err = await p;
    expect(err).toBeInstanceOf(HubError);
    expect(err.code).toBe('timeout');
    expect(clock.pending()).toBe(0);
  });

  it('clears its timer after a normal answer', async () => {
    const clock = manualClock();
    await client(fakeFetch(() => response(200, {})), undefined, clock).me();
    expect(clock.pending()).toBe(0);
  });

  it('refuses an answer from another origin (a cross-origin redirect)', async () => {
    const f = fakeFetch(() => response(200, {}, { url: 'https://evil.example/v1/me', redirected: true }));
    const err = await client(f).me().catch((e) => e);
    expect(err).toBeInstanceOf(HubError);
    expect(err.code).toBe('redirect');
    const ok = fakeFetch(() => response(200, {}, { url: `${ORIGIN}/v1/me`, redirected: true }));
    await expect(client(ok).me()).resolves.toEqual({});
    const lookalike = fakeFetch(() => response(200, {}, { url: `${ORIGIN}.evil.example/v1/me` }));
    await expect(client(lookalike).me()).rejects.toMatchObject({ code: 'redirect' });
  });

  it('an explicit default port is the same origin', async () => {
    const f = fakeFetch(() => response(200, {}, { url: `${ORIGIN}/v1/me` }));
    const c = createHubClient({ origin: `${ORIGIN}:443`, token: TOKEN, fetch: f, clock: manualClock() });
    expect(c.origin).toBe(ORIGIN);
    await expect(c.me()).resolves.toEqual({});
    expect(f.calls[0]!.url).toBe(`${ORIGIN}/v1/me`);
    // and the other way round: the platform reports the port the stored origin lacks
    const g = fakeFetch(() => response(200, {}, { url: `${ORIGIN}:443/v1/me` }));
    await expect(client(g).me()).resolves.toEqual({});
    // a different port is a different origin
    const h = fakeFetch(() => response(200, {}, { url: `${ORIGIN}:8443/v1/me` }));
    await expect(client(h).me()).rejects.toMatchObject({ code: 'redirect' });
  });

  it('revokeDevice: done on 2xx, 401 and 404; retry on 5xx, a redirect or no answer', async () => {
    const r = { origin: ORIGIN, token: TOKEN };
    const at = (h: () => ReturnType<typeof response> | Promise<never>) => revokeDevice({ fetch: fakeFetch(h), clock: manualClock() }, r);
    await expect(at(() => response(204))).resolves.toBe('done');
    await expect(at(() => response(401, {}))).resolves.toBe('done');
    await expect(at(() => response(404, {}))).resolves.toBe('done');
    await expect(at(() => response(503, {}))).resolves.toBe('retry');
    await expect(at(() => response(302, ''))).resolves.toBe('retry');
    await expect(at(() => Promise.reject(new Error('down')))).resolves.toBe('retry');
    const f = fakeFetch(() => response(204));
    await revokeDevice({ fetch: f, clock: manualClock() }, r);
    expect(f.calls[0]).toMatchObject({ url: `${ORIGIN}/v1/devices/self`, init: { method: 'DELETE', headers: { Authorization: `Bearer ${TOKEN}` } } });
  });

  it('flushPendingRevokes keeps only what must be retried, honours `only`, and never throws', async () => {
    let list = [
      { origin: 'https://a.ts.net', token: 'ta' },
      { origin: 'https://b.ts.net', token: 'tb' },
    ];
    const secure = { loadRevokes: async () => list, saveRevokes: async (l: typeof list) => void (list = l) };
    const f = fakeFetch((url) => (url.startsWith('https://a.') ? response(204) : Promise.reject(new Error('down'))));
    await flushPendingRevokes(secure, { fetch: f, clock: manualClock() }, (r) => r.origin === 'https://b.ts.net');
    expect(f.calls.map((c) => c.url)).toEqual(['https://b.ts.net/v1/devices/self']);
    expect(list).toHaveLength(2);
    await flushPendingRevokes(secure, { fetch: f, clock: manualClock() });
    expect(list).toEqual([{ origin: 'https://b.ts.net', token: 'tb' }]);
    const locked = { loadRevokes: () => Promise.reject(new Error('locked')), saveRevokes: async () => {} };
    await expect(flushPendingRevokes(locked, { fetch: f, clock: manualClock() })).resolves.toBeUndefined();
  });

  it('normalizeOrigin drops only the scheme default port', () => {
    expect(normalizeOrigin('https://Hub.ts.net:443/')).toBe('https://hub.ts.net');
    expect(normalizeOrigin('http://10.0.0.2:80')).toBe('http://10.0.0.2');
    expect(normalizeOrigin('http://10.0.0.2:443')).toBe('http://10.0.0.2:443');
    expect(normalizeOrigin('https://hub.ts.net:8443')).toBe('https://hub.ts.net:8443');
    expect(normalizeOrigin('https://[::1]:443')).toBe('https://[::1]');
    expect(normalizeOrigin('https://[::1]')).toBe('https://[::1]');
  });

  it('asks for redirect: error and treats any 3xx as fatal, without following it', async () => {
    let unauthorized = 0;
    for (const status of [301, 302, 303, 307, 308]) {
      const f = fakeFetch(() => response(status, '', { headers: { get: (n) => (n.toLowerCase() === 'location' ? 'https://evil.example/' : null) } }));
      const err = await client(f, () => unauthorized++).me().catch((e) => e);
      expect(err).toBeInstanceOf(HubError);
      expect(err.code).toBe('redirect');
      expect(f.calls).toHaveLength(1);
      expect(f.calls[0]!.init.redirect).toBe('error');
    }
    expect(unauthorized).toBe(0);
  });

  it('refuses a redirected answer whose final URL is unknown', async () => {
    const f = fakeFetch(() => response(200, {}, { redirected: true }));
    await expect(client(f).me()).rejects.toMatchObject({ code: 'redirect' });
  });

  it('reads the Error schema code, and only the code', async () => {
    const f = fakeFetch(() => response(409, { error: BODY_SECRET, code: 'conflict' }));
    const err = await client(f).seen('al_1').catch((e) => e);
    expect(err).toMatchObject({ status: 409, code: 'conflict', path: '/v1/alerts/al_1/seen' });
    expect(err.message).toBe('POST /v1/alerts/al_1/seen: 409 conflict');
  });
});

describe('401', () => {
  it('calls onUnauthorized once however many calls fail', async () => {
    const f = fakeFetch(() => response(401, { error: 'bad token', code: 'unauthorized' }));
    const cb = jest.fn();
    const c = client(f, cb);
    const results = await Promise.allSettled([c.me(), c.overview(), c.accounts()]);
    await c.alerts().catch(() => {});
    expect(results.every((r) => r.status === 'rejected')).toBe(true);
    expect(cb).toHaveBeenCalledTimes(1);
    for (const r of results) {
      expect((r as PromiseRejectedResult).reason).toMatchObject({ status: 401, code: 'unauthorized' });
    }
  });

  it('does not call it for other statuses', async () => {
    const cb = jest.fn();
    await client(fakeFetch(() => response(403, { code: 'forbidden' })), cb).me().catch(() => {});
    await client(fakeFetch(() => response(500)), cb).me().catch(() => {});
    expect(cb).not.toHaveBeenCalled();
  });

  it('unpairSelf is best effort even on 401, but still reports it', async () => {
    const cb = jest.fn();
    await expect(client(fakeFetch(() => response(401, {})), cb).unpairSelf()).resolves.toBeUndefined();
    await expect(client(fakeFetch(() => Promise.reject(new Error('x')))).unpairSelf()).resolves.toBeUndefined();
    expect(cb).toHaveBeenCalledTimes(1);
  });
});

describe('errors never carry the token or a body (phone.md P3)', () => {
  const failures: [string, () => FakeFetch][] = [
    ['network error naming the token', () => fakeFetch(() => Promise.reject(new Error(`failed Bearer ${TOKEN}`)))],
    ['401 with the token in the body', () => fakeFetch(() => response(401, { error: `bad ${TOKEN} ${BODY_SECRET}`, code: 'unauthorized' }))],
    ['403', () => fakeFetch(() => response(403, { error: BODY_SECRET, code: 'forbidden' }))],
    ['404 text body', () => fakeFetch(() => response(404, BODY_SECRET))],
    ['500 with an odd code', () => fakeFetch(() => response(500, { error: BODY_SECRET, code: BODY_SECRET }))],
    ['200 with unreadable JSON', () => fakeFetch(() => response(200, `${BODY_SECRET} not json ${TOKEN}`))],
    ['redirect', () => fakeFetch(() => response(200, {}, { url: `https://evil.example/?t=${TOKEN}` }))],
    ['timeout', () => hangingFetch()],
  ];

  for (const [fname, make] of failures) {
    it.each(ENDPOINTS)(`${fname}: %s`, async (_n, run) => {
      const clock = manualClock();
      const c = client(make(), undefined, clock);
      const p = run(c).then(
        () => null,
        (e: unknown) => e,
      );
      await clock.advance(11_000);
      const err = (await p) as Error | null;
      if (err === null) return; // a failure the endpoint tolerates (none expected)
      expect(err).toBeInstanceOf(HubError);
      const dump = [err.message, err.stack ?? '', JSON.stringify(err), String(err), (err as HubError).path].join('\n');
      expect(dump).not.toContain(TOKEN);
      expect(dump).not.toContain('wy_p_');
      expect(dump).not.toContain('LEAKED');
      expect(dump).not.toContain('Bearer');
    });
  }
});

describe('moved hub (machines.md 8.1)', () => {
  it('movedTo reads /health frozen.moved_to without a token', async () => {
    const f = fakeFetch(() => response(200, { ok: true, frozen: { since: 't', moved_to: 'https://new.example.ts.net/' } }));
    expect(await client(f).movedTo()).toBe('https://new.example.ts.net');
    expect(f.calls[0]!.url).toBe(`${ORIGIN}/health`);
    expect(f.calls[0]!.init.headers?.Authorization).toBeUndefined();
  });

  it('is null for a live hub, a frozen one without an address, plain http, itself, junk and errors', async () => {
    expect(await client(fakeFetch(() => response(200, { ok: true }))).movedTo()).toBeNull();
    expect(await client(fakeFetch(() => response(200, { frozen: { moved_to: null } }))).movedTo()).toBeNull();
    expect(await client(fakeFetch(() => response(200, { frozen: { moved_to: 'http://new.example.ts.net' } }))).movedTo()).toBeNull();
    expect(await client(fakeFetch(() => response(200, { frozen: { moved_to: ORIGIN } }))).movedTo()).toBeNull();
    expect(await client(fakeFetch(() => response(200, 'not json'))).movedTo()).toBeNull();
    expect(await client(fakeFetch(() => Promise.reject(new Error('down')))).movedTo()).toBeNull();
    expect(movedOrigin(42, ORIGIN)).toBeNull();
    expect(movedOrigin('https://user@evil.example', ORIGIN)).toBeNull();
    expect(movedOrigin('https://New.Example.com:443/x?y', ORIGIN)).toBe('https://new.example.com');
  });

  it('errorText maps a 403 to the owner-only sentence and never shows "GET /path"', () => {
    expect(errorText(new HubError('/v1/peers/matrix', 403, 'forbidden', 'GET /v1/peers/matrix: 403 forbidden'), 'x')).toBe(OWNER_ONLY);
    expect(OWNER_ONLY).toBe('Only the owner can change this, from a computer.');
    expect(errorText(new HubError('/v1/peers/matrix', 500, null, 'GET /v1/peers/matrix: 500'), 'Could not load')).toBe('Could not load (the hub answered 500).');
    expect(errorText(new HubError('/x', null, 'timeout', 'GET /x: timed out'), 'x')).toBe('The hub did not answer in time.');
  });
});
