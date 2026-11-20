// Sessions (PhoneSessions): grouped list with filter chips.
import { useState } from 'react';
import { Text, View } from 'react-native';
import type { Session } from '../../contract/api';
import type { SessionFilter } from '../../contract/routes';
import { ago, alertText, isLive, pct, plural, projectOf, seatIndex, sessionSeat } from '../../lib/format';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { ask } from '../needs-card';
import { useTheme } from '../theme';
import { Calm, Card, Chip, Dot, Muted, Page, StateTag } from '../ui';

const FILTERS: { id: SessionFilter; label: string; test: (s: Session, now: number) => boolean }[] = [
  { id: 'needs_you', label: 'Needs you', test: (s) => s.needs_you && s.alert?.status !== 'seen' },
  { id: 'working', label: 'Working', test: (s) => s.state === 'working' },
  { id: 'done', label: 'Done', test: (s) => s.state === 'done' },
  { id: 'all', label: 'All', test: (s, now) => isLive(s, now) },
  { id: 'ended', label: 'Ended or idle', test: (s, now) => !isLive(s, now) },
];

const asFilter = (v?: string): SessionFilter => (FILTERS.some((f) => f.id === v) ? (v as SessionFilter) : 'all');

// The route re-mounts this screen (key) when its filter or grouping params change.
export default function SessionsScreen({ filter: filterParam, by, machine }: { filter?: string; by?: string; machine?: string }) {
  const api = useHubApi();
  const t = useTheme();
  const [filter, setFilter] = useState<SessionFilter>(asFilter(filterParam));
  const [group, setGroup] = useState<string>(by === 'machine' || by === 'project' ? by : 'account');

  const o = api.data.overview;
  const all = api.data.sessions;
  if (!o && !all.length) {
    return <Page title="Sessions"><Calm>{api.data.offline ? 'Offline, and nothing is saved yet.' : 'Loading sessions…'}</Calm></Page>;
  }
  const idx = seatIndex(o?.seats ?? []);
  const f = FILTERS.find((x) => x.id === filter) ?? FILTERS[3]!;
  const rows = all.filter((s) => f.test(s, api.now) && (!machine || s.machine === machine));

  const groups = new Map<string, { label: string; colour?: string; sub: string; rows: Session[] }>();
  for (const s of rows) {
    let key: string;
    let label: string;
    let colour: string | undefined;
    let sub = '';
    if (group === 'account') {
      const seat = sessionSeat(s, idx);
      key = s.seat || '_none';
      label = seat.label;
      colour = seat.colour;
      const known = s.seat ? idx.get(s.seat) : undefined;
      if (known) {
        const five = (known.seat.windows ?? []).find((w) => w.key === 'five_hour');
        sub = [known.seat.email, five && five.used_pct != null ? `${pct(five.used_pct)} of 5h` : ''].filter(Boolean).join(' · ');
      }
    } else if (group === 'machine') {
      key = s.machine || '_none';
      label = s.machine_name || 'Unknown machine';
    } else {
      key = projectOf(s);
      label = key;
    }
    const g = groups.get(key) ?? { label, colour, sub, rows: [] };
    g.rows.push(s);
    groups.set(key, g);
  }
  // Needs-you groups first; the hub's order inside each group.
  const ordered = [...groups.values()].sort((a, b) => Number(b.rows.some((s) => s.needs_you)) - Number(a.rows.some((s) => s.needs_you)));

  return (
    <Page title="Sessions">
      <View style={{ flexDirection: 'row', flexWrap: 'wrap' }}>
        {FILTERS.map((x) => (
          <Chip key={x.id} testID={`filter-${x.id}`} label={x.label} count={all.filter((s) => x.test(s, api.now)).length} on={x.id === filter} onPress={() => setFilter(x.id === filter ? 'all' : x.id)} />
        ))}
        <Chip testID="by-machine" label="By machine" on={group === 'machine'} onPress={() => setGroup(group === 'machine' ? 'account' : 'machine')} />
      </View>
      {rows.length === 0 ? (
        <Calm>{filter === 'all' ? 'No live sessions.' : `No ${f.label.toLowerCase()} sessions.`}</Calm>
      ) : (
        ordered.map((g) => (
          <View key={g.label} style={{ gap: 8 }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', marginTop: 8 }}>
              {g.colour ? <Dot colour={g.colour} /> : null}
              {/* One line: the long email or "85% of 5h" tail ellipsises instead of running off screen. */}
              <Text numberOfLines={1} style={{ flexShrink: 1, color: t.muted, fontSize: 13 }}>
                <Text accessibilityRole="header" style={{ color: t.text, fontWeight: '700', fontSize: 14 }}>{`${g.label} `}</Text>
                {[g.sub, plural(g.rows.length, 'session')].filter(Boolean).join(' · ')}
              </Text>
            </View>
            {g.rows.map((s) => (
              <SessionRow key={s.id} s={s} now={api.now} />
            ))}
          </View>
        ))
      )}
    </Page>
  );
}

function SessionRow({ s, now }: { s: Session; now: number }) {
  const t = useTheme();
  const last = s.state === 'asked' || s.state === 'needs_permission' ? ask(s) : s.last_message || s.question || '';
  return (
    <Card onPress={() => go.session(s.id, s.alert?.id)} label={`Session ${projectOf(s)}`} dim={s.alert?.status === 'seen'}>
      <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
        <StateTag session={s} />
        <Muted>{ago(s.state_since, now)}</Muted>
      </View>
      <Text numberOfLines={1} style={{ color: t.text, fontWeight: '600' }}>
        {projectOf(s)}
        {s.title || s.name ? <Text style={{ color: t.muted, fontWeight: '400' }}>{` · ${s.title || s.name}`}</Text> : null}
      </Text>
      {last ? <Text numberOfLines={2} style={{ color: t.muted, fontSize: 13 }}>{last}</Text> : null}
      <Muted>
        {[s.machine_name, s.stale ? 'stale' : '', s.machine_stale ? 'machine quiet' : '', s.context_pct != null ? `context ${pct(s.context_pct)}` : '', s.alert && s.needs_you ? alertText(s.alert, now) : '']
          .filter(Boolean)
          .join(' · ')}
      </Muted>
    </Card>
  );
}
