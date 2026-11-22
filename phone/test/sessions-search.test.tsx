// The Sessions screen's search (pwa.md 3.0b, phone.md P4) on the devserver fixtures: matching
// on folder, title, name and the last message, case and accents ignored, marked hits, the
// extra line, counts, and a query that survives the list refreshing.
import { act, fireEvent, render, screen } from '@testing-library/react-native';
import SessionsScreen from '../src/components/screens/SessionsScreen';
import type { Session } from '../src/contract/api';
import { isLive } from '../src/lib/format';
import { NOW, fx, makeHarness } from './screen-harness';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);

const FINANCE = 'c41e0000-0000-4000-8000-00000000000b';
const live = () => fx.sessions.sessions.filter((s) => isLive(s as Session, NOW));
const rows = () => screen.queryAllByLabelText(/^Session /).map((n) => String(n.props.accessibilityLabel).slice('Session '.length));
const hits = () => screen.queryAllByTestId('search-hit').map((n) => n.props.children as string);
const type = async (q: string) => fireEvent.changeText(screen.getByTestId('session-search'), q);

async function show() {
  const h = makeHarness();
  await render(<SessionsScreen />, { wrapper: h.Wrapper });
  return h.probe;
}

describe('Sessions search', () => {
  it('ignores case and accents and marks what it found', async () => {
    await show();
    expect(rows().length).toBe(live().length);
    await type('CAFE lumiere');
    expect(rows()).toEqual(['finance']);
    expect(hits()).toEqual(expect.arrayContaining(['Café', 'Lumière']));
    expect(screen.getByTestId('search-count').props.children).toBe(`1 of ${live().length} sessions matches`);
  });

  it('finds titles, folders and names; a name not on the row gets its own line', async () => {
    await show();
    await type('terraform');
    expect(rows()).toEqual(['infra']);
    await type('API');
    expect(rows().sort()).toEqual(['api', 'api']);
    await type('rapport');
    expect(rows()).toEqual(['finance']);
    const extra = screen.getByTestId(`search-extra-${FINANCE}`);
    expect(extra.props.children[0]).toBe('Name: ');
    expect(hits()).toContain('rapport');
  });

  it('every term must be found; counts follow the search; nothing found says so', async () => {
    await show();
    await type('api flaky');
    expect(rows()).toEqual(['api']);
    // The chips count the matches only.
    expect(screen.getByText('Working 1')).toBeTruthy();
    expect(screen.getByText('Needs you 0')).toBeTruthy();
    await type('cron-jobs');
    expect(rows()).toEqual([]);
    expect(screen.getByText('No sessions here match “cron-jobs”.')).toBeTruthy();
    // The stale session is under Ended or idle, whose count says so.
    expect(screen.getByText('Ended or idle 1')).toBeTruthy();
    await fireEvent.press(screen.getByTestId('filter-ended'));
    expect(rows()).toEqual(['cron-jobs']);
  });

  it('keeps the query while the list refreshes, and Clear shows everything again', async () => {
    const p = await show();
    await type('lumi');
    expect(rows()).toEqual(['finance']);
    // A poll brings a new reply: the finance session's message changed, another session arrived.
    const next = structuredClone(fx.sessions.sessions) as unknown as Session[];
    const fin = next.find((s) => s.id === FINANCE)!;
    fin.last_message = 'Sent the Lumière invoices.';
    next.push({ ...fin, id: 'c41e0000-0000-4000-8000-0000000000ff', project: 'lumiere-site', title: 'landing', name: null, last_message: 'Done.' });
    await act(async () => p.api.dispatch({ type: 'loaded', at: NOW + 15e3, sessions: next }));
    expect(screen.getByTestId('session-search').props.value).toBe('lumi');
    expect(rows().sort()).toEqual(['finance', 'lumiere-site']);
    expect(screen.getByText(/Sent the/)).toBeTruthy();
    await fireEvent.press(screen.getByTestId('session-search-clear'));
    expect(screen.getByTestId('session-search').props.value).toBe('');
    expect(screen.queryByTestId('search-count')).toBeNull();
    expect(rows().length).toBe(live().length + 1);
  });
});
