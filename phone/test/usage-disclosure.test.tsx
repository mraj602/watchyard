// "Usage and the picker" inside an open Accounts row (src/components/screens/AccountsScreen.tsx) on a fake hub client.
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react-native';
import AccountsScreen from '../src/components/screens/AccountsScreen';
import type { UsageRelation } from '../src/contract/api';
import { NOW, fakeClient, makeHarness } from './screen-harness';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);

const iso = (ms: number) => new Date(ms).toISOString();
const factor = (value: number, weight: number) => ({ value, fallback: value, measured: value, weight, n: 8, days: 6, implausible: false });

function relation(over: Partial<UsageRelation> = {}): UsageRelation {
  const days = Array.from({ length: 14 }, (_, i) => {
    const d = new Date(Date.UTC(2026, 10, 1 + i)).toISOString().slice(0, 10);
    return { day: d, ratio: i % 5 === 4 ? null : 4 + (i % 4), d5: 10, d7: 2, pairs: 30, model: i % 2 ? 'Opus 4.6' : 'Sonnet 4.5', rise: 10, rise_cc: 9 };
  });
  return {
    seat: 'seat_work', time: iso(NOW), tz: 'UTC', today: '2026-11-14', days,
    heavy: { this_week: { heavy: false, rise: 30, cc_share: 0.62 }, last_week: { heavy: true, rise: 40, cc_share: 0.9 } },
    models: [{
      model: 'Opus 4.6', this_week: 0.042, last_week: 0.04, change: 1.05,
      days: days.map((d) => ({ day: d.day, index: 0.04, normalised: 1, d5: 6, usd: 0.24 })),
    }],
    calibration: { at: iso(NOW), since: null, reset: null, f: factor(2.1, 1), ratio: factor(6.5, 0.4), lane_price: factor(4.25, 0), lane_charge: 8.5, lane_units: null, unexplained_pct_per_hour: null, unexplained_hours: 0 },
    verdict: { at: iso(NOW - 12 * 60e3), machine: 'm1', machine_name: 'gpu-box', strategy: 'lanes', kind: 'aggressive', eligible: true, rank: 1 },
    ...over,
  };
}

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

  it('opens, fetches once for that seat, and shows the sections', async () => {
    const client = await openRow();
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(await screen.findByTestId('usage-verdict')).toHaveTextContent('Eligible · next in line · aggressive');
    expect(client.relation).toHaveBeenCalledTimes(1);
    expect(client.relation).toHaveBeenCalledWith('seat_work');
    const body = screen.getByTestId('usage-body');
    expect(within(body).getByText('as of 12 min ago on gpu-box')).toBeTruthy();
    expect(within(body).getByText('Picker')).toBeTruthy();
    expect(within(body).getByText('Calibration')).toBeTruthy();
    expect(within(body).getByTestId('usage-cal-ratio')).toHaveTextContent(/· blending, 40% measured · n 8 · 6 days$/);
    expect(within(body).getByText('5 h per 7 d point, per day')).toBeTruthy();
    expect(within(body).getByText('S Sonnet 4.5 · O Opus 4.6')).toBeTruthy();
    expect(within(body).getByText('5 h capacity, normalised to last week')).toBeTruthy();
    expect(within(body).getByText('Opus 4.6 · $0.042 per 1% this week · 1.05× last week')).toBeTruthy();
    expect(within(body).getByText(/^62% of the 5-hour rise had Claude Code tokens: /)).toBeTruthy();
    // closing and opening again soon does not fetch again
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(screen.queryByTestId('usage-body')).toBeNull();
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(screen.getByTestId('usage-body')).toBeTruthy();
    expect(client.relation).toHaveBeenCalledTimes(1);
  });

  it('shows a bar value on tap', async () => {
    await openRow();
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    await screen.findByTestId('usage-body');
    await fireEvent.press(screen.getByTestId('usage-ratio-2026-11-02'));
    expect(screen.getByTestId('usage-ratio-label')).toHaveTextContent('2 Nov: 5 · Opus 4.6 · 30 pairs');
  });

  it('says so with no verdict and no calibration', async () => {
    await openRow(fakeClient({ relation: jest.fn(async () => relation({ verdict: null, calibration: null, models: [] })) }));
    await fireEvent.press(screen.getByTestId('usage-toggle-seat_work'));
    expect(await screen.findByTestId('usage-verdict')).toHaveTextContent('No verdict yet: `wy account next` has not reported this account.');
    expect(screen.getByText('Not calibrated yet.')).toBeTruthy();
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
    expect(await screen.findByTestId('usage-verdict')).toHaveTextContent('Eligible · next in line · aggressive');
    expect(client.relation).toHaveBeenCalledTimes(2);
    expect(screen.queryByText('Try again')).toBeNull();
  });
});
