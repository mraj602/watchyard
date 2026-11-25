// Contract test: the TS validators accept what the Go side really emits.
// - Devserver replies: test/fixtures/hub/ (scripts/dump-fixtures.sh).
// - The P11 snapshot example: test/fixtures/widget-snapshot.v1.json.
import * as fs from 'node:fs';
import * as path from 'node:path';

import {
  PAYLOAD_KEYS,
  validateAlert,
  validateLiveProps,
  validateMatrixInfo,
  validateMe,
  validateOverview,
  validatePairClaimReply,
  validateDevicePrefs,
  validatePayload,
  validatePeerAudit,
  validatePushTarget,
  validateRefresh,
  validateSeat,
  validateSession,
  validateWidgetSnapshot,
} from '../src/contract';

const HUB = path.join(__dirname, 'fixtures/hub');
const read = (file: string): unknown => JSON.parse(fs.readFileSync(file, 'utf8'));
const hub = (name: string) => read(path.join(HUB, name)) as Record<string, unknown>;

describe('validators reject what the contract forbids', () => {
  const alert: Record<string, unknown> = {
    v: 1,
    type: 'alert',
    alert: 'al_01JA3B7Q0001',
    alerts: ['al_01JA3B7Q0001'],
    trigger: 'asked',
    title: 'work · api · asked a question',
    body: 'Should I also update the migration?',
    tag: 'wy-s-sess_01JA3B7Q',
    url: '/#/sessions/sess_01JA3B7Q?alert=al_01JA3B7Q0001',
    remote_url: 'https://claude.ai/code/session_01JA3B7Q',
    tap: 'auto',
    session: 'sess_01JA3B7Q',
    needs_you: 3,
    actions: { snooze: 'a1.YWxf.EIJN', seen: 'a1.YWxf.BUhr' },
    private: false,
    ts: '2026-10-13T14:02:00.000Z',
  };

  it('a payload in Go key order passes', () => {
    expect(validatePayload(alert, 'payload', true)).toEqual([]);
  });

  it('a private payload with remote_url', () => {
    const data: Record<string, unknown> = { ...alert, private: true };
    // Rebuild in Go key order so only the private rule fires.
    const ordered = Object.fromEntries(PAYLOAD_KEYS.filter((k) => k in data).map((k) => [k, data[k]]));
    expect(validatePayload(ordered, 'payload', true).join()).toMatch(/remote_url/);
  });

  it('a payload with keys out of Go order', () => {
    const { v, ...rest } = alert;
    expect(validatePayload({ ...rest, v }, 'payload', true).join()).toMatch(/Go order/);
  });

  it('channel and category are optional strings, anywhere in the order; an unknown category is refused', () => {
    expect(validatePayload({ ...alert, channel: 'wy_drain', category: 'drain' }, 'payload', true)).toEqual([]);
    expect(validatePayload({ channel: 'wy_switch', ...alert, category: 'switch' }, 'payload', true)).toEqual([]);
    expect(validatePayload({ ...alert, channel: 3 }, 'payload', true).join()).toMatch(/channel/);
    expect(validatePayload({ ...alert, category: 'pigeons' }, 'payload', true).join()).toMatch(/category/);
  });

  it('a refresh payload passes, with an extra key it does not', () => {
    const r = { v: 1, type: 'refresh', needs_you: 2, ts: '2026-10-13T14:02:00.000Z' };
    expect(validateRefresh(r)).toEqual([]);
    expect(validateRefresh({ ...r, title: 'x' }).join()).toMatch(/title/);
  });

  it('a push target of an unknown kind', () => {
    const t = { id: 'pt_1', kind: 'pigeon', enabled: true, status: 'ok' };
    expect(validatePushTarget(t).join()).toMatch(/kind/);
    expect(validatePushTarget({ ...t, kind: 'link' })).toEqual([]);
  });

  it('a private snapshot with a top item', () => {
    const s = read(path.join(__dirname, 'fixtures/widget-snapshot.v1.json')) as Record<string, unknown>;
    expect(validateWidgetSnapshot({ ...s, private: true }).join()).toMatch(/top/);
  });
});

describe('devserver fixtures', () => {
  it('me', () => expect(validateMe(hub('me.json'))).toEqual([]));
  it('pair claim reply (cookie: false carries the token)', () => {
    const claim = hub('pair-claim.json');
    expect(validatePairClaimReply(claim)).toEqual([]);
    expect(typeof claim.token).toBe('string');
  });
  it('overview', () => expect(validateOverview(hub('overview.json'))).toEqual([]));
  it('sessions', () => {
    const list = hub('sessions.json').sessions as unknown[];
    expect(list.length).toBeGreaterThan(0);
    list.forEach((s, i) => expect(validateSession(s, `sessions[${i}]`)).toEqual([]));
  });
  it('session detail', () => expect(validateSession(hub('session.json'))).toEqual([]));
  it('accounts', () => {
    const list = hub('accounts.json').accounts as unknown[];
    list.forEach((s, i) => expect(validateSeat(s, `accounts[${i}]`)).toEqual([]));
  });
  it('alerts', () => {
    const list = hub('alerts.json').alerts as unknown[];
    list.forEach((a, i) => expect(validateAlert(a, `alerts[${i}]`)).toEqual([]));
  });
  it('alert', () => expect(validateAlert(hub('alert.json'))).toEqual([]));
  it('peers matrix', () => expect(validateMatrixInfo(hub('peers-matrix.json'))).toEqual([]));
  it('peers audit', () => {
    const list = hub('peers-audit.json').entries as unknown[];
    expect(list.length).toBeGreaterThan(0);
    list.forEach((e, i) => expect(validatePeerAudit(e, `entries[${i}]`)).toEqual([]));
  });
  it('snooze_min is in GET /v1/rules settings', () => {
    const settings = hub('rules.json').settings as Record<string, unknown>;
    expect(typeof settings.snooze_min).toBe('number');
    expect(typeof settings.refresh_min).toBe('number');
  });
});

describe('phone.md P11, P12 examples', () => {
  it('widget-snapshot.v1.json', () => {
    expect(validateWidgetSnapshot(read(path.join(__dirname, 'fixtures/widget-snapshot.v1.json')))).toEqual([]);
  });
  it('LiveCountdownProps', () => {
    expect(
      validateLiveProps({
        seat: 'home',
        colour: '#2fb380',
        window: 'five_hour',
        pct: 88,
        resets_at: '2026-10-13T15:30:00.000Z',
        started_at: '2026-10-13T14:02:00.000Z',
      }),
    ).toEqual([]);
  });
});

describe('seat mode, drain and device prefs', () => {
  const seat = { id: 'seat_a', account: 'a', org: 'o' };
  it('accepts mode off and drain_until, null or missing', () => {
    for (const mode of ['protected', 'aggressive', 'off', null]) expect(validateSeat({ ...seat, mode })).toEqual([]);
    expect(validateSeat({ ...seat, mode: 'off', drain_until: null })).toEqual([]);
    expect(validateSeat({ ...seat, drain_until: '2026-11-25T18:29:00.000Z' })).toEqual([]);
    expect(validateSeat({ ...seat, mode: 'normal' }).join()).toMatch(/mode/);
    expect(validateSeat({ ...seat, drain_until: 'tonight' }).join()).toMatch(/drain_until/);
  });
  it('accepts mute_categories as a list of strings', () => {
    expect(validateDevicePrefs({ mute_categories: ['drain', 'usage'] })).toEqual([]);
    expect(validateDevicePrefs({ mute_categories: 'drain' }).join()).toMatch(/mute_categories/);
    expect(validateDevicePrefs({ mute_categories: [3] }).join()).toMatch(/mute_categories/);
  });
});
