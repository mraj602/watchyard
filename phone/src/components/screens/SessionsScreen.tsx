// Sessions (PhoneSessions): grouped list with a search box (pwa.md 3.0b) and filter chips.
import { useState } from 'react';
import { Pressable, Text, TextInput, View } from 'react-native';
import type { Session } from '../../contract/api';
import type { SessionFilter } from '../../contract/routes';
import {
  ago, alertText, highlight, isLive, MAX_QUERY, pct, plural, projectOf, searchFolder, searchSession, searchTerms, seatIndex, sessionSeat, tagTerms,
  type ExtraMatch, type Segment,
} from '../../lib/format';
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

// The row's last line: the question or the permission request while it waits on you, else the
// last message.
const lastOf = (s: Session): string => (s.state === 'asked' || s.state === 'needs_permission' ? ask(s) : s.last_message || s.question || '');
const subOf = (s: Session): string => s.title || s.name || '';

// The route re-mounts this screen (key) when its filter or grouping params change.
export default function SessionsScreen({ filter: filterParam, by, machine }: { filter?: string; by?: string; machine?: string }) {
  const api = useHubApi();
  const t = useTheme();
  const [filter, setFilter] = useState<SessionFilter>(asFilter(filterParam));
  const [group, setGroup] = useState<string>(by === 'machine' || by === 'project' ? by : 'account');
  // The search is the screen's state, so the polls that refresh the list keep it.
  const [query, setQuery] = useState('');

  const o = api.data.overview;
  const all = api.data.sessions;
  if (!o && !all.length) {
    return <Page title="Sessions"><Calm>{api.data.offline ? 'Offline, and nothing is saved yet.' : 'Loading sessions…'}</Calm></Page>;
  }
  const idx = seatIndex(o?.seats ?? []);
  const f = FILTERS.find((x) => x.id === filter) ?? FILTERS[3]!;
  // Search over what the list holds; found: session id → its extra line (or null).
  const terms = searchTerms(query);
  const found = new Map<string, ExtraMatch | null>();
  for (const s of all) {
    const r = searchSession(s, { folder: searchFolder(s), sub: subOf(s), last: lastOf(s) }, terms);
    if (r) found.set(s.id, r.extra);
  }
  const here = (s: Session) => !machine || s.machine === machine;
  const underChip = all.filter((s) => f.test(s, api.now) && here(s));
  const rows = underChip.filter((s) => found.has(s.id));

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
      <View style={{ flexDirection: 'row', alignItems: 'center', borderWidth: 1, borderColor: t.line, borderRadius: 8, backgroundColor: t.card }}>
        <TextInput
          testID="session-search"
          accessibilityLabel="Search sessions"
          placeholder="Folder, title, name or message"
          placeholderTextColor={t.muted}
          value={query}
          onChangeText={setQuery}
          maxLength={MAX_QUERY}
          autoCapitalize="none"
          autoCorrect={false}
          returnKeyType="search"
          style={{ flex: 1, color: t.text, fontSize: 15, paddingHorizontal: 10, paddingVertical: 8, minHeight: 44 }}
        />
        {query ? (
          <Pressable testID="session-search-clear" accessibilityRole="button" accessibilityLabel="Clear search" onPress={() => setQuery('')} hitSlop={8} style={{ paddingHorizontal: 12, minHeight: 44, justifyContent: 'center' }}>
            <Text style={{ color: t.muted, fontSize: 18 }}>×</Text>
          </Pressable>
        ) : null}
      </View>
      {terms.length ? (
        <Text testID="search-count" accessibilityLiveRegion="polite" style={{ color: t.muted, fontSize: 13 }}>
          {`${rows.length} of ${plural(underChip.length, 'session')} ${rows.length === 1 ? 'matches' : 'match'}`}
        </Text>
      ) : null}
      <View style={{ flexDirection: 'row', flexWrap: 'wrap' }}>
        {/* Counts follow the search, so a 0 says the matches are under another chip. */}
        {FILTERS.map((x) => (
          <Chip key={x.id} testID={`filter-${x.id}`} label={x.label} count={all.filter((s) => x.test(s, api.now) && found.has(s.id)).length} on={x.id === filter} onPress={() => setFilter(x.id === filter ? 'all' : x.id)} />
        ))}
        <Chip testID="by-machine" label="By machine" on={group === 'machine'} onPress={() => setGroup(group === 'machine' ? 'account' : 'machine')} />
      </View>
      {rows.length === 0 ? (
        <Calm>{terms.length ? `No sessions here match “${query.trim()}”.` : filter === 'all' ? 'No live sessions.' : `No ${f.label.toLowerCase()} sessions.`}</Calm>
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
              <SessionRow key={s.id} s={s} now={api.now} terms={terms} extra={found.get(s.id) ?? null} onTag={(tag) => setQuery(`tag:${tag}`)} />
            ))}
          </View>
        ))
      )}
    </Page>
  );
}

/** Text with the found pieces tinted; a nested Text, so it stays inside its line clamp. */
function Marked({ segs, testID }: { segs: Segment[]; testID?: string }) {
  const t = useTheme();
  return (
    <>
      {segs.map((g, i) =>
        g.hit ? (
          <Text key={i} testID={testID} style={{ backgroundColor: t.mark }}>{g.text}</Text>
        ) : (
          g.text
        ),
      )}
    </>
  );
}

function SessionRow({ s, now, terms, extra, onTag }: { s: Session; now: number; terms: string[]; extra: ExtraMatch | null; onTag: (tag: string) => void }) {
  const t = useTheme();
  const last = lastOf(s);
  const sub = subOf(s);
  return (
    <Card onPress={() => go.session(s.id, s.alert?.id)} label={`Session ${projectOf(s)}`} dim={s.alert?.status === 'seen'}>
      <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
        <StateTag session={s} />
        <Muted>{ago(s.state_since, now)}</Muted>
      </View>
      <Text numberOfLines={1} style={{ color: t.text, fontWeight: '600' }}>
        {searchFolder(s) ? <Marked segs={highlight(projectOf(s), terms)} testID="search-hit" /> : projectOf(s)}
        {sub ? (
          <Text style={{ color: t.muted, fontWeight: '400' }}>
            {' · '}
            <Marked segs={highlight(sub, terms)} testID="search-hit" />
          </Text>
        ) : null}
      </Text>
      {s.tags?.length ? (
        <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 6 }}>
          {s.tags.map((tag) => (
            <Pressable
              key={tag}
              testID={`session-tag-${tag}`}
              accessibilityRole="button"
              accessibilityLabel={`Show sessions tagged ${tag}`}
              onPress={() => onTag(tag)}
              hitSlop={6}
              style={{ borderWidth: 1, borderColor: t.line, borderRadius: 10, paddingHorizontal: 8, paddingVertical: 2, backgroundColor: t.card }}
            >
              <Text style={{ color: t.muted, fontSize: 12 }}>
                <Marked segs={highlight(tag, tagTerms(tag, terms))} testID="search-hit" />
              </Text>
            </Pressable>
          ))}
        </View>
      ) : null}
      {last ? (
        <Text numberOfLines={2} style={{ color: t.muted, fontSize: 13 }}>
          <Marked segs={highlight(last, terms)} testID="search-hit" />
        </Text>
      ) : null}
      {extra ? (
        <Text testID={`search-extra-${s.id}`} numberOfLines={2} style={{ color: t.muted, fontSize: 13 }}>
          {`${extra.label}: `}
          <Marked segs={extra.segments} testID="search-hit" />
        </Text>
      ) : null}
      <Muted>
        {[s.machine_name, s.stale ? 'stale' : '', s.machine_stale ? 'machine quiet' : '', s.context_pct != null ? `context ${pct(s.context_pct)}` : '', s.alert && s.needs_you ? alertText(s.alert, now) : '']
          .filter(Boolean)
          .join(' · ')}
      </Muted>
    </Card>
  );
}
