// Screen tests on the devserver fixtures. The hub API is a fake
// (test/screen-harness.tsx); fetch stays off.
import { Alert as RNAlert, Linking } from 'react-native';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react-native';
import AccountsScreen from '../src/components/screens/AccountsScreen';
import AlertRedirectScreen from '../src/components/screens/AlertRedirectScreen';
import AlertsScreen from '../src/components/screens/AlertsScreen';
import MachinesScreen from '../src/components/screens/MachinesScreen';
import MoreScreen from '../src/components/screens/MoreScreen';
import NowScreen from '../src/components/screens/NowScreen';
import PeersScreen from '../src/components/screens/PeersScreen';
import SessionScreen from '../src/components/screens/SessionScreen';
import { swipeAction } from '../src/components/needs-card';
import SessionsScreen from '../src/components/screens/SessionsScreen';
import SettingsScreen from '../src/components/screens/SettingsScreen';
import { clock, isLive, mostRoom } from '../src/lib/format';
import { HubError } from '../src/lib/hub';
import type { Seat, Session } from '../src/contract/api';
import { NOW, fakeClient, freshData, fx, makeHarness, routerMock } from './screen-harness';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);
jest.mock('expo-clipboard', () => ({ setStringAsync: jest.fn(async () => true) }));
jest.mock('../src/lib/links', () => ({
  hashToRoute: jest.fn((u: string) => (u.startsWith('/#/sessions/') ? { name: 'session', id: 'x' } : { name: 'now' })),
  routeToHref: jest.fn((r: { name: string; id?: string }) => (r.name === 'session' ? `/sessions/${r.id}` : '/')),
}));

const Clipboard = jest.requireMock('expo-clipboard') as { setStringAsync: jest.Mock };

const ID3 = 'c41e0000-0000-4000-8000-000000000003'; // asked, alert pending, Remote Control link
const ID4 = 'c41e0000-0000-4000-8000-000000000004'; // needs_permission, alert fired
const ID5 = 'c41e0000-0000-4000-8000-000000000005'; // failed (rate limit), alert seen

async function show(ui: React.ReactElement, setup: Parameters<typeof makeHarness>[0] = {}) {
  const h = makeHarness(setup);
  await render(ui, { wrapper: h.Wrapper });
  return h.probe;
}

beforeEach(() => {
  routerMock.router.push.mockClear();
  routerMock.router.replace.mockClear();
  Clipboard.setStringAsync.mockClear();
});

describe('Now', () => {
  it('lists the needs-you rows in fixture order', async () => {
    await show(<NowScreen />);
    const ids = screen.getAllByTestId(/^needs-/).map((n) => String(n.props.testID).slice('needs-'.length));
    expect(ids).toEqual(fx.overview.needs_you.map((s) => s.id));
    expect(screen.getByText('Needs you · 3')).toBeTruthy();
    // A ring per visible account, machines with their state.
    expect(screen.getAllByLabelText(/of the 5-hour window|no data/).length).toBe(fx.overview.seats.filter((s) => !s.hidden).length);
    expect(screen.getByText('laptop')).toBeTruthy();
    expect(screen.getByText('9 sessions running')).toBeTruthy();
  });

  it('Seen calls the hub and dims the row', async () => {
    const p = await show(<NowScreen />);
    expect(screen.getByTestId(`needs-${ID3}`).props.style.opacity).toBe(1);
    await fireEvent.press(screen.getByTestId(`seen-${ID3}`));
    expect(p.client.seen).toHaveBeenCalledWith('al_pending');
    expect(p.dispatched).toContainEqual({ type: 'seen', alert: 'al_pending' });
    await waitFor(() => expect(screen.getByTestId(`needs-${ID3}`).props.style.opacity).toBe(0.55));
    expect(p.api.kick).toHaveBeenCalled();
  });

  it('Snooze calls the hub with the hub minutes and marks the alert snoozed', async () => {
    const p = await show(<NowScreen />);
    await fireEvent.press(screen.getByTestId(`snooze-${ID4}`));
    expect(p.client.snooze).toHaveBeenCalledWith('al_fired', 15);
    const d = p.dispatched.find((a) => a.type === 'snoozed');
    expect(d).toMatchObject({ type: 'snoozed', alert: 'al_fired' });
    expect(Date.parse((d as { until: string }).until)).toBe(NOW + 15 * 60e3);
    expect(await screen.findByText(/snoozed until/)).toBeTruthy();
  });

  it('a seen alert has no buttons', async () => {
    await show(<NowScreen />);
    expect(screen.queryByTestId(`seen-${ID5}`)).toBeNull();
  });

  it('a 409 toasts and still refreshes', async () => {
    const client = fakeClient({ seen: jest.fn(async () => { throw Object.assign(new Error('conflict'), { status: 409 }); }) });
    const p = await show(<NowScreen />, { client });
    await fireEvent.press(screen.getByTestId(`seen-${ID3}`));
    expect(await screen.findByText('That alert already changed')).toBeTruthy();
    expect(p.api.kick).toHaveBeenCalled();
  });

  it('shows the cache under an offline banner and disables Seen and Snooze', async () => {
    const p = await show(<NowScreen />, { data: { offline: true, fetchedAt: NOW - 120e3 } });
    expect(screen.getByText(`Offline · showing ${clock(new Date(NOW - 120e3).toISOString(), NOW)}`)).toBeTruthy();
    expect(screen.getByText('Needs you · 3')).toBeTruthy();
    expect(screen.getByTestId(`seen-${ID3}`).props.accessibilityState.disabled).toBe(true);
    await fireEvent.press(screen.getByTestId(`seen-${ID3}`));
    expect(p.client.seen).not.toHaveBeenCalled();
  });

  it('Open in Claude and Copy resume command', async () => {
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    await show(<NowScreen />);
    await fireEvent.press(screen.getByTestId(`open-${ID3}`));
    expect(open).toHaveBeenCalledWith('https://claude.ai/code/session_demo');
    expect(screen.queryByTestId(`open-${ID4}`)).toBeNull(); // no remote_url
    await fireEvent.press(screen.getByTestId(`copy-${ID3}`));
    expect(Clipboard.setStringAsync).toHaveBeenCalledWith(`claude-2 --resume ${ID3}`);
    expect(await screen.findByText('Copied')).toBeTruthy();
    open.mockRestore();
  });

  it('swipe right is Seen, left is Snooze, a short drag is nothing', () => {
    expect(swipeAction(120)).toBe('seen');
    expect(swipeAction(88)).toBe('seen');
    expect(swipeAction(-100)).toBe('snooze');
    expect(swipeAction(87)).toBeNull();
    expect(swipeAction(-20)).toBeNull();
  });

  it("never shows the internal detail code as the ask (the board's rule)", async () => {
    const data = freshData();
    const strip = (x: Session) => (x.id === ID4 ? { ...x, question: null, last_message: null, detail: 'permission_request' } : x);
    const ov = data.overview!;
    ov.needs_you = ov.needs_you.map((x) => strip(x as Session)) as typeof ov.needs_you;
    data.sessions = data.sessions.map((x) => (x.state === 'done' ? { ...x, last_message: null, question: null, detail: 'started' } : strip(x)));
    await show(<NowScreen />, { data });
    expect(screen.queryByText('permission_request')).toBeNull();
    // The state's own ask stands in for the missing text, never the detail code.
    expect(screen.getAllByText(/Wants permission for a tool|Waiting for your reply|No details were sent/).length).toBeGreaterThan(0);
    await show(<SessionsScreen />, { data });
    expect(screen.queryByText('permission_request')).toBeNull();
    expect(screen.queryByText('started')).toBeNull();
  });

  it('opens a session when a row is tapped', async () => {
    await show(<NowScreen />);
    await fireEvent.press(screen.getByLabelText(/^Asked a question: api/));
    expect(routerMock.router.push).toHaveBeenCalledWith(`/sessions/${ID3}?alert=al_pending`);
  });
});

// The three Needs-you cards seen live on 1.1.0 (two skipped stale, one skipped peers) and a row
// with no alert: every one can be marked seen, none snoozed (push.md 4).
describe('Now: rows whose alert never pushes', () => {
  const liveData = () => {
    const data = freshData();
    const ov = data.overview!;
    const base = ov.needs_you[0] as Session;
    const row = (id: string, state: Session['state'], since: string, alert: Session['alert']): Session =>
      ({ ...base, id, state, state_since: since, needs_you: true, alert, question: null, last_message: null }) as Session;
    const skipped = (id: string, reason: string, trigger: string) => ({ id, trigger, status: 'skipped', reason }) as unknown as Session['alert'];
    ov.needs_you = [
      row('s-peers', 'asked', '2026-11-06T09:00:00.000Z', skipped('al_c', 'peers', 'asked')),
      row('s-stale-asked', 'asked', '2026-11-18T09:00:00.000Z', skipped('al_a', 'stale', 'asked')),
      row('s-stale-perm', 'needs_permission', '2026-11-19T09:00:00.000Z', skipped('al_b', 'stale', 'needs_permission')),
      row('s-none', 'asked', '2026-11-20T09:00:00.000Z', null),
    ] as typeof ov.needs_you;
    return data;
  };

  it.each([['s-peers', 'al_c'], ['s-stale-asked', 'al_a'], ['s-stale-perm', 'al_b']])('%s has Seen and no Snooze; Seen calls the hub', async (id, al) => {
    const p = await show(<NowScreen />, { data: liveData() });
    expect(screen.queryByTestId(`snooze-${id}`)).toBeNull();
    await fireEvent.press(screen.getByTestId(`seen-${id}`));
    expect(p.client.seen).toHaveBeenCalledWith(al);
    expect(p.client.snooze).not.toHaveBeenCalled();
    expect(p.dispatched).toContainEqual({ type: 'seen', alert: al });
    await waitFor(() => expect(screen.getByTestId(`needs-${id}`).props.style.opacity).toBe(0.55));
  });

  it('a row with no alert is seen through the session', async () => {
    const p = await show(<NowScreen />, { data: liveData() });
    expect(screen.queryByTestId('snooze-s-none')).toBeNull();
    await fireEvent.press(screen.getByTestId('seen-s-none'));
    expect(p.client.seenSession).toHaveBeenCalledWith('s-none');
    expect(p.client.seen).not.toHaveBeenCalled();
    expect(p.dispatched).toContainEqual({ type: 'seenSession', session: 's-none' });
    await waitFor(() => expect(screen.getByTestId('needs-s-none').props.style.opacity).toBe(0.55));
  });

  it('the session page has Seen for a skipped alert', async () => {
    const data = liveData();
    const s = data.overview!.needs_you[0] as Session;
    const client = fakeClient({ session: jest.fn(async () => ({ ...structuredClone(fx.session), ...s, timeline: [] })) });
    const p = await show(<SessionScreen id="s-peers" />, { client, data });
    await fireEvent.press(await screen.findByTestId('seen-s-peers'));
    expect(p.client.seen).toHaveBeenCalledWith('al_c');
    expect(screen.queryByTestId('snooze-s-peers')).toBeNull();
  });
});

describe('Sessions', () => {
  const rows = () => screen.queryAllByLabelText(/^Session /);

  it('shows live sessions, grouped by account, and filters', async () => {
    await show(<SessionsScreen />);
    expect(rows().length).toBe(fx.sessions.sessions.filter((s) => isLive(s as Session, NOW)).length);
    await fireEvent.press(screen.getByTestId('filter-needs_you'));
    // Three sessions need you in the fixture; one has a seen alert, which hides it (push.md 4).
    expect(fx.sessions.sessions.filter((s) => s.needs_you).length).toBe(3);
    expect(rows().length).toBe(2);
    await fireEvent.press(screen.getByTestId('filter-working'));
    expect(rows().length).toBe(fx.sessions.sessions.filter((s) => s.state === 'working').length);
    await fireEvent.press(screen.getByTestId('filter-done'));
    expect(rows().length).toBe(fx.sessions.sessions.filter((s) => s.state === 'done').length);
    await fireEvent.press(screen.getByTestId('filter-ended'));
    // Idle done sessions (12 h without activity at the fixture's clock) sit here with the ended ones.
    const quiet = fx.sessions.sessions.filter((s) => !isLive(s as Session, NOW)).length;
    expect(quiet).toBeGreaterThan(0);
    expect(rows().length).toBe(quiet);
  });

  it('starts on the filter and grouping of its route params', async () => {
    await show(<SessionsScreen filter="needs_you" by="machine" />);
    expect(rows().length).toBe(2); // the third, seen one is hidden
    expect(screen.getAllByRole('header').map((h) => h.props.children).length).toBeGreaterThan(1);
  });

  it('filters by machine', async () => {
    await show(<SessionsScreen machine="m_gpu" />);
    const want = fx.sessions.sessions.filter((s) => s.machine === 'm_gpu' && isLive(s as Session, NOW)).length;
    expect(rows().length).toBe(want);
  });

  it('opens a session', async () => {
    await show(<SessionsScreen filter="needs_you" />);
    await fireEvent.press(rows()[0]!);
    expect(routerMock.router.push).toHaveBeenCalledWith(expect.stringMatching(/^\/sessions\/c41e/));
  });
});

describe('Session', () => {
  it('shows the question, the history and the links; Seen works from here', async () => {
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    const p = await show(<SessionScreen id={ID3} alert="al_pending" />);
    expect(await screen.findByText('Waiting on you')).toBeTruthy();
    expect(screen.getAllByText(/Should I also update the migration/).length).toBeGreaterThan(0);
    expect(screen.getByText('State history')).toBeTruthy();
    expect(screen.getByText(/signed in to one account at a time/)).toBeTruthy();
    await fireEvent.press(screen.getByTestId(`open-${ID3}`));
    expect(open).toHaveBeenCalledWith('https://claude.ai/code/session_demo');
    await fireEvent.press(screen.getByTestId(`copy-${ID3}`));
    expect(Clipboard.setStringAsync).toHaveBeenCalledWith(`claude-2 --resume ${ID3}`);
    await fireEvent.press(screen.getByTestId(`seen-${ID3}`));
    expect(p.client.seen).toHaveBeenCalledWith('al_pending');
    open.mockRestore();
  });

  it('never opens a link that is not https', async () => {
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    const detail = { ...fx.session, remote_url: 'javascript:alert(1)' };
    await show(<SessionScreen id={ID3} />, { client: fakeClient({ session: jest.fn(async () => detail) }) });
    await screen.findByText('State history');
    expect(screen.queryByTestId(`open-${ID3}`)).toBeNull();
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it('says so when the session is gone', async () => {
    const client = fakeClient({ session: jest.fn(async () => { throw Object.assign(new Error('nf'), { status: 404 }); }) });
    await show(<SessionScreen id="nope" />, { client });
    expect(await screen.findByText('That session no longer exists.')).toBeTruthy();
  });
});

describe('Accounts', () => {
  it('names the account with the most room, with both windows', async () => {
    await show(<AccountsScreen />);
    const best = mostRoom(fx.accounts.accounts as never, NOW);
    expect(best).not.toBeNull();
    expect(screen.getByTestId('most-room')).toHaveTextContent(new RegExp(`^Most room right now: ${best!.seat.label} \\(5 h \\d+% · 7 d \\d+%\\)$`));
  });

  it('shows one compact row per account, every window only after a tap', async () => {
    await show(<AccountsScreen />);
    for (const s of fx.accounts.accounts.filter((x) => !x.hidden)) expect(screen.getByTestId(`seat-${s.id}`)).toBeTruthy();
    expect(screen.queryByText('5-hour')).toBeNull();
    expect(screen.queryByText(/^resets in /)).toBeNull();
    expect(screen.getByText('The bar shows the tighter of the 5 h and 7 d limits. Tap an account for everything else.')).toBeTruthy();
    await fireEvent.press(screen.getByTestId('row-seat_work'));
    expect(within(screen.getByTestId('detail-seat_work')).getAllByText('5-hour').length).toBeGreaterThan(0);
    expect(within(screen.getByTestId('detail-seat_work')).getAllByText(/^resets in /).length).toBeGreaterThan(0);
    await fireEvent.press(screen.getByTestId('row-seat_work'));
    expect(screen.queryByTestId('detail-seat_work')).toBeNull();
  });

  it('shows an idle window as 0 % not started, and a passed reset as "reset at", never "resets now"', async () => {
    const accounts = structuredClone(fx.accounts.accounts) as unknown as Seat[];
    const work = accounts.find((s) => s.id === 'seat_work')!;
    const passed = new Date(NOW - 3 * 86_400_000).toISOString();
    work.windows = [
      { key: 'five_hour', used_pct: 0, resets_at: null, state: 'idle', updated_at: new Date(NOW - 60_000).toISOString(), source: 'web' },
      { key: 'weekly_fable', label: '7-day · Fable', used_pct: 0, resets_at: passed, state: 'reset', source: 'web' },
    ];
    await show(<AccountsScreen seat="seat_work" />, { data: { accounts } });
    expect(screen.getByTestId('reset-seat_work')).toHaveTextContent('not started');
    expect(screen.getByTestId('pct-seat_work')).toHaveTextContent('0%');
    const detail = within(screen.getByTestId('detail-seat_work'));
    expect(detail.getByText('not started · starts when you next use this account')).toBeTruthy();
    expect(detail.getByText(`reset at ${clock(passed, NOW)}`)).toBeTruthy();
    expect(detail.queryByText(/resets now/)).toBeNull();
    expect(detail.queryByText('no data')).toBeNull();
  });

  it('opens the account asked for, and Expand all / Collapse all toggles every row', async () => {
    await show(<AccountsScreen seat="seat_work" />);
    expect(screen.getByTestId('detail-seat_work')).toBeTruthy();
    await fireEvent.press(screen.getByText('Expand all'));
    const ids = fx.accounts.accounts.filter((x) => !x.hidden).map((x) => x.id);
    for (const id of ids) expect(screen.getByTestId(`detail-${id}`)).toBeTruthy();
    await fireEvent.press(screen.getByText('Collapse all'));
    for (const id of ids) expect(screen.queryByTestId(`detail-${id}`)).toBeNull();
  });

  it('offers the Lock Screen countdown only when the app core says a card qualifies', async () => {
    const start = jest.fn(async () => undefined);
    await show(<AccountsScreen />, { api: { countdown: { can: (id) => id === 'seat_work', start } } });
    await fireEvent.press(screen.getByText('Expand all'));
    expect(screen.getAllByText('Count down on Lock Screen')).toHaveLength(1);
    await fireEvent.press(screen.getByTestId('countdown-seat_work'));
    expect(start).toHaveBeenCalledWith('seat_work');
  });

  it('has no countdown button by default', async () => {
    await show(<AccountsScreen />);
    expect(screen.queryByText('Count down on Lock Screen')).toBeNull();
  });
});

describe('More, Machines', () => {
  it('More links to Alerts, Machines, Peers, Analytics and Settings', async () => {
    await show(<MoreScreen />);
    await fireEvent.press(screen.getByTestId('more-alerts'));
    await fireEvent.press(screen.getByTestId('more-machines'));
    await fireEvent.press(screen.getByTestId('more-peers'));
    await fireEvent.press(screen.getByTestId('more-analytics'));
    await fireEvent.press(screen.getByTestId('more-settings'));
    expect(routerMock.router.push.mock.calls.map((c) => c[0])).toEqual(['/more/alerts', '/more/machines', '/more/peers', '/more/analytics', '/more/settings']);
    expect(screen.queryByText('Coming later')).toBeNull();
  });

  it('Machines shows reach, backlog and version', async () => {
    await show(<MachinesScreen machine="m_gpu" />);
    expect(screen.getByText('laptop')).toBeTruthy();
    expect(screen.getByText('gpu-box')).toBeTruthy();
    expect(screen.getByText(/^1 240 queued, oldest /)).toBeTruthy();
    expect(screen.getAllByText(/same as hub/).length).toBeGreaterThan(0);
  });
});

describe('Alerts', () => {
  it('a rule switch writes prefs.mute, and a rule that is off on the hub is disabled', async () => {
    const p = await show(<AlertsScreen />);
    const sw = await screen.findByTestId('rule-asked');
    await waitFor(() => expect(screen.getByTestId('rule-asked').props.disabled).toBe(false));
    expect(screen.getByTestId('rule-reset').props.disabled).toBe(true); // rule_reset is off on the hub
    expect(screen.getByText('Off on the hub')).toBeTruthy();
    expect(sw.props.value).toBe(true);
    await fireEvent(screen.getByTestId('rule-asked'), 'valueChange', false);
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalled());
    expect(p.client.patchSelf.mock.calls[0]![0]).toMatchObject({ mute: ['asked'] });
    await waitFor(() => expect(screen.getByTestId('rule-asked').props.value).toBe(false));
    // Limits group mutes both triggers.
    await fireEvent(screen.getByTestId('rule-limits'), 'valueChange', false);
    expect(p.client.patchSelf.mock.calls.at(-1)![0]).toMatchObject({ mute: ['asked', 'limit_80', 'limit_95'] });
  });

  it('lists the recent pushes with their outcome', async () => {
    await show(<AlertsScreen />);
    expect(await screen.findByText('work · api · asked a question')).toBeTruthy();
    expect(screen.getAllByText(/^(open|seen|waiting|snoozed|cancelled|closed|skipped)$/).length).toBeGreaterThan(1);
  });
});

describe('Settings', () => {
  it('private, tap and widgets switches PATCH the right prefs', async () => {
    const p = await show(<SettingsScreen />);
    await screen.findByTestId('private-on');
    await waitFor(() => expect(screen.getByTestId('private-on').props.accessibilityState.selected).toBe(false));
    await fireEvent.press(screen.getByTestId('private-on'));
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(1));
    expect(p.client.patchSelf.mock.calls[0]![0]).toMatchObject({ private: true, tap: 'auto' });
    await waitFor(() => expect(screen.getByTestId('private-on').props.accessibilityState.selected).toBe(true));

    await fireEvent.press(screen.getByTestId('tap-claude'));
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(2));
    expect(p.client.patchSelf.mock.calls[1]![0]).toMatchObject({ private: true, tap: 'claude' });

    await fireEvent(screen.getByTestId('pref-widgets'), 'valueChange', true);
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(3));
    expect(p.client.patchSelf.mock.calls[2]![0]).toMatchObject({ widgets: true, tap: 'claude' });

    await fireEvent.press(screen.getByTestId('private-default'));
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(4));
    expect(p.client.patchSelf.mock.calls[3]![0]).toMatchObject({ private: null });
  });

  it('own quiet hours: on writes an object, off writes null, bad times are refused', async () => {
    const p = await show(<SettingsScreen />);
    await screen.findByTestId('pref-quiet');
    await waitFor(() => expect(screen.getByTestId('pref-quiet').props.disabled).toBe(false));
    await fireEvent(screen.getByTestId('pref-quiet'), 'valueChange', true);
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(1));
    expect(p.client.patchSelf.mock.calls[0]![0].quiet_hours).toMatchObject({ enabled: true, start: '23:00', end: '08:00' });
    await fireEvent.changeText(await screen.findByTestId('quiet-start'), '25:99');
    await act(async () => fireEvent(screen.getByTestId('quiet-start'), 'endEditing'));
    expect(await screen.findByText('Use 24-hour times like 23:00')).toBeTruthy();
    expect(p.client.patchSelf).toHaveBeenCalledTimes(1);
    await fireEvent(screen.getByTestId('pref-quiet'), 'valueChange', false);
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(2));
    expect(p.client.patchSelf.mock.calls[1]![0].quiet_hours).toBeNull();
  });

  it('says why notifications are off, and offers to turn them on', async () => {
    const p = await show(<SettingsScreen />, { api: { pushState: null, link: {} as never } });
    await fireEvent.press(screen.getByTestId('turn-on'));
    expect(p.api.enablePush).toHaveBeenCalled();
  });

  it('without the link module there is no push path to turn on', async () => {
    await show(<SettingsScreen />, { api: { pushState: null } });
    expect(screen.queryByTestId('turn-on')).toBeNull();
    expect(screen.getByText(/own connection to your hub/)).toBeTruthy();
  });

  it('Auto countdown (iOS): a local switch, shown only when the app core offers it', async () => {
    await show(<SettingsScreen />);
    expect(screen.queryByTestId('auto-countdown')).toBeNull();
    const set = jest.fn(async () => undefined);
    const p = await show(<SettingsScreen />, { api: { autoCountdown: { on: true, set } } });
    expect(screen.getByText('Count down on the Lock Screen when a limit push is opened.')).toBeTruthy();
    expect(screen.getByTestId('auto-countdown').props.value).toBe(true);
    await fireEvent(screen.getByTestId('auto-countdown'), 'valueChange', false);
    expect(set).toHaveBeenCalledWith(false);
    expect(p.client.patchSelf).not.toHaveBeenCalled(); // not a hub pref
  });

  it('the blocked state', async () => {
    await show(<SettingsScreen />, { api: { pushState: 'blocked' } });
    expect(screen.getByTestId('notif-state').props.children).toMatch(/^Blocked in system settings/);
    expect(screen.queryByTestId('turn-on')).toBeNull();
  });

  it('shows the version and unpairs after a confirmation', async () => {
    const alert = jest.spyOn(RNAlert, 'alert').mockImplementation((_t, _m, buttons) => {
      buttons?.find((b) => b.style === 'destructive')?.onPress?.();
    });
    const p = await show(<SettingsScreen />);
    expect(screen.getByText('0.8.0 (12)')).toBeTruthy();
    expect(screen.getByText('hub.example.ts.net')).toBeTruthy();
    await fireEvent.press(screen.getByTestId('unpair'));
    expect(alert).toHaveBeenCalledWith('Unpair from hub.example.ts.net?', expect.any(String), expect.any(Array));
    expect(p.api.unpair).toHaveBeenCalled();
    alert.mockRestore();
  });

  it('keeps data when loaded fresh', () => {
    expect(freshData().overview?.needs_you.length).toBe(3);
  });
});

describe('Alert redirect', () => {
  it('replaces itself with the alert deep link', async () => {
    await show(<AlertRedirectScreen id="al_pending" />);
    await waitFor(() => expect(routerMock.router.replace).toHaveBeenCalledWith('/sessions/x'));
  });

  it('never loops back to itself', async () => {
    const client = fakeClient({ alert: jest.fn(async () => ({ ...fx.alert, url: '/#/alerts/al_x' })) });
    await show(<AlertRedirectScreen id="al_x" />, { client });
    await waitFor(() => expect(routerMock.router.replace).toHaveBeenCalledWith('/'));
  });

  it('says so when the alert is gone', async () => {
    const client = fakeClient({ alert: jest.fn(async () => { throw Object.assign(new Error('nf'), { status: 404 }); }) });
    await show(<AlertRedirectScreen id="al_gone" />, { client });
    expect(await screen.findByText('That alert no longer exists.')).toBeTruthy();
    await fireEvent.press(screen.getByText('Go to Now'));
    expect(routerMock.router.replace).toHaveBeenCalledWith('/');
  });
});

describe('Peers', () => {
  const P = '3f1c6a2e-0000-4000-8000-000000000001'; // personal
  const W = '3f1c6a2e-0000-4000-8000-000000000011'; // work
  const C = '3f1c6a2e-0000-4000-8000-000000000021'; // client
  const withMatrix = (patch: Record<string, unknown>) => ({
    ...fx.peersMatrix,
    matrix: { ...fx.peersMatrix.matrix, version: 4, ...patch },
  });
  const clientWith = (m: object) => fakeClient({ matrix: jest.fn(async () => structuredClone(m)) });

  it('shows the version, the grid and the audit list', async () => {
    await show(<PeersScreen />);
    expect(await screen.findByText(/Matrix version 1 · signed by hub · max 30 turns/)).toBeTruthy();
    expect(screen.getByTestId('peers-state')).toHaveTextContent('Peers are on');
    // Four accounts: 12 off-diagonal cells, all empty by default.
    expect(screen.getAllByTestId(/^cell-/).length).toBe(12);
    expect(screen.getByText('Matrix changed')).toBeTruthy();
    expect(screen.queryByText('Messages')).toBeNull(); // the fake hub does not serve them
  });

  it('a cell with no pair has nothing to narrow', async () => {
    await show(<PeersScreen />);
    await fireEvent.press(await screen.findByTestId(`cell-${P}-${W}`));
    expect(screen.getByText(/Nothing to narrow here/)).toBeTruthy();
    expect(screen.queryByTestId('narrow-none')).toBeNull();
  });

  it('a tapped cell can only narrow, with the base version', async () => {
    const m = withMatrix({ allow: [[P, W], [W, P], [P, C]] });
    const client = clientWith(m);
    await show(<PeersScreen />, { client });
    await fireEvent.press(await screen.findByTestId(`cell-${P}-${W}`));
    // Both ways can become either one way, or none; never anything wider.
    expect(screen.getAllByTestId(/^narrow-/).map((n) => n.props.testID)).toEqual(['narrow-out', 'narrow-in', 'narrow-none']);
    await fireEvent.press(screen.getByTestId('narrow-none'));
    await waitFor(() => expect(client.editMatrix).toHaveBeenCalledWith({ base_version: 4, deny: [[P, W], [W, P]] }));
    // Only deny entries are ever sent.
    expect(client.editMatrix.mock.calls.every(([e]) => !('allow' in e))).toBe(true);
  });

  it('one way can only be removed', async () => {
    const client = clientWith(withMatrix({ allow: [[P, C]] }));
    await show(<PeersScreen />, { client });
    await fireEvent.press(await screen.findByTestId(`cell-${C}-${P}`)); // the other side of the same pair
    expect(screen.getAllByTestId(/^narrow-/).length).toBe(1);
    await fireEvent.press(screen.getByTestId('narrow-none'));
    await waitFor(() => expect(client.editMatrix).toHaveBeenCalledWith({ base_version: 4, deny: [[P, C]] }));
  });

  it('a stale version reloads the matrix and says so', async () => {
    const client = fakeClient({
      matrix: jest.fn(async () => structuredClone(withMatrix({ allow: [[P, W]] }))),
      editMatrix: jest.fn(async () => { throw new HubError('/v1/peers/matrix', 409, 'conflict', 'PUT /v1/peers/matrix: 409 conflict'); }),
    });
    await show(<PeersScreen />, { client });
    await fireEvent.press(await screen.findByTestId(`cell-${P}-${W}`));
    await fireEvent.press(screen.getByTestId('narrow-none'));
    expect(await screen.findByText(/changed meanwhile/)).toBeTruthy();
    expect(client.matrix.mock.calls.length).toBeGreaterThan(1);
  });

  it('the pause switch pauses with one tap', async () => {
    let paused = false;
    const client = fakeClient({
      matrix: jest.fn(async () => structuredClone(withMatrix({ paused }))),
      pausePeers: jest.fn(async (on: boolean) => {
        paused = on;
        return structuredClone(withMatrix({ paused }));
      }),
    });
    await show(<PeersScreen />, { client });
    await fireEvent(await screen.findByTestId('peers-pause'), 'valueChange', true);
    await waitFor(() => expect(client.pausePeers).toHaveBeenCalledWith(true));
    expect(await screen.findByText('All peers are paused')).toBeTruthy();
  });

  it('resuming is not offered to a phone', async () => {
    const client = clientWith(withMatrix({ paused: true }));
    await show(<PeersScreen />, { client });
    expect(await screen.findByText('All peers are paused')).toBeTruthy();
    expect(screen.getByTestId('peers-pause').props.disabled).toBe(true);
    expect(screen.getByText('Resume peers from a computer (board Peers page).')).toBeTruthy();
    expect(client.pausePeers).not.toHaveBeenCalled();
  });

  it('is read-only while offline', async () => {
    await show(<PeersScreen />, { data: { offline: true } });
    expect((await screen.findByTestId('peers-pause')).props.disabled).toBe(true);
  });

  it('lists messages (who to whom, size, outcome) when the hub serves them', async () => {
    const client = fakeClient({
      peerMessages: jest.fn(async () => ({
        messages: [{ id: 'pm_1', thread: 't_0', from: { account: P }, to: { account: W }, kind: 'start', size: 2400, status: 'delivered', created_at: '2026-10-13T06:20:00.000Z' }],
      })),
    });
    await show(<PeersScreen />, { client });
    expect(await screen.findByText('personal → work')).toBeTruthy();
    expect(screen.getByText('start · 2.4 KB · delivered')).toBeTruthy();
  });
});
