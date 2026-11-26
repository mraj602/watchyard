// "Usage and the picker" inside an open Accounts row (src/components/screens/AccountsScreen.tsx) on a fake hub client.
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react-native';
import AccountsScreen from '../src/components/screens/AccountsScreen';
import type { UsageRelation } from '../src/contract/api';
import { NOW, fakeClient, fx, makeHarness } from './screen-harness';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);

const iso = (ms: number) => new Date(ms).toISOString();
const factor = (value: number, weight: number, n = 8, days = 6) => ({ value, fallback: value, measured: value, weight, n, days, implausible: false });

const owner = (): UsageRelation => {
  const days = Array.from({ length: 14 }, (_, i) => {
    const has = i % 3 === 0;
    return { day: new Date(Date.UTC(2026, 10, 1 + i)).toISOString().slice(0, 10), ratio: has ? 5 + (i % 4) : null, d5: has ? 10 : 0, d7: has ? 2 : 0, pairs: has ? 30 : 0, model: has ? 'Opus 5.5' : null, rise: 10, rise_cc: 9 };
  });
  const idle = (model: string) => ({ model, this_week: null, last_week: null, change: null, days: days.map((d) => ({ day: d.day, index: null, normalised: null, d5: 0, usd: 0 })) });
  return {
    seat: 'seat_work', time: iso(NOW), tz: 'UTC', today: '2026-11-14', days,
    heavy: { this_week: { heavy: true, rise: 30, cc_share: 0.9 }, last_week: { heavy: true, rise: 40, cc_share: 0.9 } },
    models: [
      { model: 'Opus 5.5', this_week: 1.67, last_week: null, change: null, days: days.map((d, i) => ({ day: d.day, index: i === 0 ? 1.67 : null, normalised: null, d5: 6, usd: i === 0 ? 0.3 : 0 })) },
      idle('Sonnet 5.5'), idle('Haiku 5.5'),
    ],
    calibration: { at: iso(NOW), since: null, reset: null, f: factor(1.38, 1, 1504, 6.7), ratio: factor(6.78, 1, 1504, 6.7), lane_price: factor(4, 0, 1, 0), lane_charge: 8, lane_units: null, unexplained_pct_per_hour: null, unexplained_hours: 0 },
    verdict: {
      at: iso(NOW - 12 * 60e3), machine: 'm1', machine_name: 'gpu-box', strategy: 'lanes', kind: 'protected', eligible: true, rank: 3,
      weekly_pct: 29, weekly_cap: 52, five_hour_pct: 1, five_hour_cap: 45, burst: 0, burst_cap: 2, mode_from: 'hub',
    },
  };
};
const relation = (over: Partial<UsageRelation> = {}): UsageRelation => ({ ...owner(), ...over });

async function openRow(client = fakeClient({ relation: jest.fn(async () => relation()) })) {
  const h = makeHarness({ client });
  await render(<AccountsScreen />, { wrapper: h.Wrapper });
  await fireEvent.press(screen.getByTestId('row-seat_work'));
  return client;
}

describe('Usage and the picker', () => {
  it('is closed inside the details and loads nothing until opened', async () => {
    const client = await openRow();
    const detail = screen.getByTestId('detail-seat_work');
    expect(within(detail).getByText('Usage and the picker')).toBeTruthy();
    expect(screen.queryByTestId('usage-body')).toBeNull();
    expect(client.relation).not.toHaveBeenCalled();
  });

  it('opens, fetches once for that seat, and shows the sections in order', async () => {
    const client = await openRow();
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(await screen.findByTestId('usage-headline')).toHaveTextContent('Can take a new session: 3rd in line');
    expect(client.relation).toHaveBeenCalledTimes(1);
    expect(client.relation).toHaveBeenCalledWith('seat_work');
    const body = screen.getByTestId('usage-body');
    expect(within(body).getByText('3rd in line: 2 other accounts get a new session before this one; they take turns.')).toBeTruthy();
    expect(within(body).getByText('as of 12 min ago on gpu-box')).toBeTruthy();
    const rules = within(body).getAllByTestId('usage-rule').map((r) => r.props.children);
    expect(rules.join('|')).toContain('5-hour 1% (stops at 45%)');
    expect(within(body).getByText(/Week 29%, the even pace allows 52% today/)).toBeTruthy();
    expect(within(body).getByText('6.8% of the 5-hour limit = 1% of the week')).toBeTruthy();
    expect(within(body).getByText('≈ 6.8 full 5-hour windows use up the week')).toBeTruthy();
    expect(within(body).getByText('One full 5-hour window ≈ 15% of the week')).toBeTruthy();
    expect(within(body).getByText('measured over 7 days')).toBeTruthy();
    expect(within(body).getByText('Each new session is set aside 8% of the 5-hour limit until its real use shows up (default; measured after 20 sessions, 1 so far)')).toBeTruthy();
    expect(within(body).getByText('5-hour % per 1% of the week, each day')).toBeTruthy();
    expect(within(body).getByText('Higher: the week lasts longer compared with the 5-hour limit.')).toBeTruthy();
    expect(within(body).getByText('0%', { includeHiddenElements: true })).toBeTruthy();
    expect(within(body).getByText('– no data')).toBeTruthy();
    expect(within(screen.getByTestId('usage-ratio-legend')).getByText('Opus 5.5')).toBeTruthy();
    expect(within(body).getByText('This week 1% of the 5-hour limit bought about $1.67 of Opus 5.5 work at API prices. Last week: not enough data yet.')).toBeTruthy();
    expect(within(body).getByText('Sonnet 5.5: not used in the last 2 weeks.')).toBeTruthy();
    expect(within(body).getAllByText('A change of more than 20% on most accounts raises an alert.')).toHaveLength(1);
    // one hint, under the first chart; no capacity chart (last week has no value)
    expect(within(body).getAllByText('Tap a day to see its value.')).toHaveLength(1);
    expect(screen.queryByTestId('usage-capacity-Opus 5.5')).toBeNull();
    // How this works is closed until opened
    expect(screen.queryByTestId('usage-how')).toBeNull();
    await fireEvent.press(screen.getByTestId('usage-how-toggle'));
    expect(within(screen.getByTestId('usage-how')).getByText(/1.38, measured from 1504 readings over 7 days/)).toBeTruthy();
    // closing and opening again soon does not fetch again
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(screen.queryByTestId('usage-body')).toBeNull();
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(screen.getByTestId('usage-body')).toBeTruthy();
    expect(client.relation).toHaveBeenCalledTimes(1);
  });

  it('shows a day value on tap, and a gap says no data', async () => {
    await openRow();
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    await screen.findByTestId('usage-body');
    await fireEvent.press(screen.getByTestId('usage-ratio-2026-11-01'));
    expect(screen.getByTestId('usage-ratio-label')).toHaveTextContent('1 Nov: 5% of the 5-hour limit per 1% of the week · mostly Opus 5.5 · 30 readings');
    await fireEvent.press(screen.getByTestId('usage-ratio-2026-11-02'));
    expect(screen.getByTestId('usage-ratio-label')).toHaveTextContent('2 Nov: no data');
  });

  it('a chart with no data is one line', async () => {
    const days = owner().days.map((d) => ({ ...d, ratio: null, model: null }));
    await openRow(fakeClient({ relation: jest.fn(async () => relation({ days })) }));
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    await screen.findByTestId('usage-body');
    expect(screen.getByText('Not enough readings in the last 2 weeks to show this.')).toBeTruthy();
    expect(screen.queryByTestId('usage-ratio')).toBeNull();
    expect(screen.queryByText('Tap a day to see its value.')).toBeNull();
  });

  it('says so with no verdict and no calibration', async () => {
    await openRow(fakeClient({ relation: jest.fn(async () => relation({ verdict: null, calibration: null, models: [] })) }));
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(await screen.findByTestId('usage-verdict')).toHaveTextContent('No verdict yet: wy account next has not reported this account.');
    expect(screen.getByText('Not measured yet: the hub has no calibration for this account.')).toBeTruthy();
  });

  it('opened again after more than 5 minutes loads again', async () => {
    const now = jest.spyOn(Date, 'now');
    const t0 = Date.parse('2026-11-14T10:00:00Z');
    now.mockReturnValue(t0);
    const client = await openRow();
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    await screen.findByTestId('usage-body');
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    now.mockReturnValue(t0 + 5 * 60e3 + 1000);
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    await waitFor(() => expect(client.relation).toHaveBeenCalledTimes(2));
    now.mockRestore();
  });

  it('shows the error with Try again, which loads again', async () => {
    const relationFn = jest.fn()
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce(relation());
    const client = await openRow(fakeClient({ relation: relationFn }));
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(await screen.findByTestId('usage-error-seat_work')).toHaveTextContent('boom');
    expect(screen.queryByTestId('usage-body')).toBeNull();
    await fireEvent.press(screen.getByText('Try again'));
    expect(await screen.findByTestId('usage-headline')).toHaveTextContent('Can take a new session: 3rd in line');
    expect(client.relation).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('Try again')).toBeNull();
  });
});

describe('How should the picker use this account?', () => {
  const ME_OWNER = { ...fx.me, owner: true } as never;
  const openWith = async (client: ReturnType<typeof fakeClient>, owner: boolean, seatMode?: 'protected' | 'aggressive' | 'off' | null, extra: object = {}, seatExtra: object = {}) => {
    const accounts = fx.accounts.accounts.map((x) => (x.id === 'seat_work' ? { ...x, mode: seatMode, ...seatExtra } : x));
    const h = makeHarness({ client, data: { accounts: accounts as never }, api: { ...(owner ? { me: ME_OWNER } : {}), ...extra } });
    await render(<AccountsScreen />, { wrapper: h.Wrapper });
    await fireEvent.press(screen.getByTestId('row-seat_work'));
    return h;
  };

  it('is above the details, and someone who is not the owner reads the choice', async () => {
    await openWith(fakeClient(), false, 'protected');
    const mode = screen.getByTestId('mode-seat_work');
    expect(within(mode).getByText('How should the picker use this account?')).toBeTruthy();
    expect(screen.getByTestId('mode-current-seat_work')).toHaveTextContent(/^Protected\. Kept for you\. Used last/);
    expect(screen.queryByTestId('mode-seat_work-aggressive')).toBeNull();
    expect(screen.queryByTestId('mode-none-seat_work')).toBeNull();
  });

  it('no mode: the highlighted line, and read-only for a non-owner', async () => {
    await openWith(fakeClient(), false, null);
    expect(screen.getByTestId('mode-none-seat_work')).toHaveTextContent('Not chosen yet; treated as aggressive.');
    expect(screen.queryByTestId('mode-current-seat_work')).toBeNull();
  });

  it('the owner has the options with definitions and a choice saves at once', async () => {
    const client = fakeClient();
    const kick = jest.fn(() => new Promise<void>(() => undefined)); // the refresh is still on its way: the choice shows meanwhile
    await openWith(client, true, null, { kick });
    expect(screen.getByTestId('mode-seat_work-protected')).toHaveTextContent(/^Protected.*Kept for you\./);
    expect(screen.getByTestId('mode-seat_work-aggressive')).toHaveTextContent(/^AggressiveUse it up. Sessions go here until 98% of the week or 5 hours.$/);
    expect(screen.getByTestId('mode-seat_work-none')).toHaveTextContent(/^Not chosenNot chosen yet; treated as aggressive.$/);
    expect(screen.getByTestId('mode-seat_work-drain')).toHaveTextContent(/^DrainUse it up now/);
    expect(screen.getByTestId('mode-seat_work-off')).toHaveTextContent(/^OffNever picked, nothing moved in; sessions already there stay/);
    await fireEvent.press(screen.getByTestId('mode-seat_work-protected'));
    expect(client.patchSeat).toHaveBeenCalledWith('seat_work', { mode: 'protected', drain_until: null });
    await waitFor(() => expect(kick).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByTestId('mode-seat_work-protected').props.accessibilityState.checked).toBe(true));
  });

  it('Off sends only the mode, and shows what it means', async () => {
    const client = fakeClient();
    await openWith(client, true, 'protected', { kick: jest.fn(async () => undefined) });
    await fireEvent.press(screen.getByTestId('mode-seat_work-off'));
    expect(client.patchSeat).toHaveBeenCalledWith('seat_work', { mode: 'off' });
  });

  it('shows Off to a reader in words', async () => {
    await openWith(fakeClient(), false, 'off');
    expect(screen.getByTestId('mode-current-seat_work')).toHaveTextContent(/^Off\. Never picked, nothing moved in; sessions already there stay/);
  });

  it('Drain asks for an end time and sends drain_until; from Off it also leaves Off', async () => {
    const client = fakeClient();
    await openWith(client, true, 'off', { kick: jest.fn(async () => undefined) });
    expect(screen.queryByTestId('drain-choices-seat_work')).toBeNull();
    await fireEvent.press(screen.getByTestId('mode-seat_work-drain'));
    expect(screen.getByTestId('drain-choices-seat_work')).toBeTruthy();
    await fireEvent.press(screen.getByTestId('drain-seat_work-4h'));
    expect(client.patchSeat).toHaveBeenCalledWith('seat_work', { mode: null, drain_until: new Date(NOW + 4 * 3_600_000).toISOString() });
  });

  it('a drain from a mode sends just the end; a draining account shows when it ends and a mode pick stops it', async () => {
    const client = fakeClient();
    const until = new Date(NOW + 3_600_000);
    await openWith(client, true, 'protected', { kick: jest.fn(async () => undefined) }, { drain_until: until.toISOString() });
    const hh = String(until.getHours()).padStart(2, '0');
    const mm = String(until.getMinutes()).padStart(2, '0');
    expect(screen.getByTestId('mode-drain-seat_work')).toHaveTextContent(`Draining until ${hh}:${mm}`);
    expect(screen.getByTestId('mode-seat_work-drain').props.accessibilityState.checked).toBe(true);
    expect(screen.getByTestId('mode-seat_work-protected').props.accessibilityState.checked).toBe(false);
    await fireEvent.press(screen.getByTestId('mode-seat_work-protected'));
    expect(client.patchSeat).toHaveBeenCalledWith('seat_work', { mode: 'protected', drain_until: null });
  });

  it('a refusal puts the old choice back and says why', async () => {
    const { HubError } = jest.requireActual('../src/lib/hub');
    const client = fakeClient({ patchSeat: jest.fn(async () => { throw new HubError('/v1/accounts/seat_work', 403, 'forbidden', 'PATCH: 403'); }) });
    await openWith(client, true, 'aggressive');
    await fireEvent.press(screen.getByTestId('mode-seat_work-protected'));
    expect(await screen.findByTestId('mode-error-seat_work')).toHaveTextContent('Only the owner can change this, from a computer.');
    expect(screen.getByTestId('mode-seat_work-aggressive').props.accessibilityState.checked).toBe(true);
    expect(screen.getByTestId('mode-seat_work-protected').props.accessibilityState.checked).toBe(false);
  });

  it('names a setting on the picker machine that differs, once the verdict is loaded', async () => {
    const rel = relation({ verdict: { ...owner().verdict!, mode_from: 'local', kind: 'protected' } });
    await openWith(fakeClient({ relation: jest.fn(async () => rel) }), false, 'aggressive');
    expect(screen.queryByTestId('mode-override-seat_work')).toBeNull();
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(await screen.findByTestId('mode-override-seat_work')).toHaveTextContent('On gpu-box a setting on that machine makes it protected (set on this machine); it wins there over this choice.');
  });
});
