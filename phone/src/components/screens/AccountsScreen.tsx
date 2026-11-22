// Accounts (phone.md P4, P17.7): a
// calm list, one row per login: its dot, name and one bar for the tighter of the 5 h and 7 d
// limits with that window's reset time, and one status line only when something needs attention.
// Tapping a row opens it in place: every window with its reset, pace and peak, where the login
// is signed in (Claude Code on each machine, from the hub, and this phone's claude.ai tab, P17.7)
// with the matching action (Open claude.ai, Sign in on this phone, Fix this tab: its
// sheet), the details with "Usage and the picker" (a closed disclosure that loads the seat's usage
// relation: the picker's verdict, calibration and per-day bars), and "Count down on Lock Screen" where the Live Activity qualifies (iOS).
// "+ Add account" is in the header (a tab for a hub account this phone has none for).
import { useRef, useState } from 'react';
import { LayoutAnimation, Modal, Pressable, Text, View } from 'react-native';
import type { Seat, UsageRelation } from '../../contract/api';
import {
  agoLabel, mostRoom, paceInfo, pct, planLabel, plural, resetWords, seatIndex, seatName, shownUse, sortWindows, tighterWindow, windowLabel, windowPhase, worstPace,
} from '../../lib/format';
import {
  calibrationRestart, calibrationRows, capacityBars, capacityLine, heavyNote, legend, NO_VERDICT, NOT_CALIBRATED, ratioBars, relationStale, verdictAge, verdictLine,
} from '../../lib/format/relation';
import { errorText } from '../../lib/hub';
import {
  attention, codeLine, fixChoices, phoneTab, phoneTabLine, TAB_LOGIN, TAB_PRIVACY_NOTE, tabAction, type Line, type PhoneTab,
} from '../../lib/tabs';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { giveTab, profilesChanged, signOutTab, useTabMemos, useTabProfiles } from '../tab-memo';
import { useTabsSupported } from '../tabs';
import { useTheme } from '../theme';
import { Btn, Calm, Card, Dot, Meter, Muted, Page, useToast, useToneColour } from '../ui';

export default function AccountsScreen({ seat: focus }: { seat?: string }) {
  const api = useHubApi();
  const t = useTheme();
  const toast = useToast();
  const supported = useTabsSupported();
  const tabs = supported === true;
  const profiles = useTabProfiles(supported);
  const memos = useTabMemos();
  const [fixing, setFixing] = useState<string | null>(null);
  const [open, setOpen] = useState<Set<string>>(() => new Set(focus ? [focus] : []));
  const o = api.data.overview;
  const accounts = api.data.accounts.length ? api.data.accounts : o?.seats ?? [];
  if (!o && !accounts.length) {
    return <Page title="Accounts"><Calm>{api.data.offline ? 'Offline, and nothing is saved yet.' : 'Loading…'}</Calm></Page>;
  }
  const idx = seatIndex(accounts);
  const seats = accounts.filter((s) => !s.hidden);
  const best = mostRoom(seats, api.now);
  const tabOf = (s: Seat) => phoneTab(s, accounts, { supported: tabs, profiles, memo: memos?.[s.id] });
  const fixSeat = fixing ? seats.find((s) => s.id === fixing) ?? null : null;
  const allOpen = seats.length > 0 && seats.every((s) => open.has(s.id));
  const toggle = (ids: string[], on: boolean) => {
    LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut);
    setOpen((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (on) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  };

  return (
    <Page
      title="Accounts"
      toast={toast.node}
      right={tabs ? <AddAccount seats={seats} profiles={profiles} /> : undefined}
    >
      {seats.length > 1 ? (
        <Pressable
          accessibilityRole="button"
          testID="expand-all"
          onPress={() => toggle(seats.map((x) => x.id), !allOpen)}
          style={{ alignSelf: 'flex-start', paddingVertical: 2 }}
        >
          <Text style={{ color: t.muted, fontSize: 14, fontWeight: '600' }}>{allOpen ? 'Collapse all' : 'Expand all'}</Text>
        </Pressable>
      ) : null}
      {best ? (
        <Text testID="most-room" style={{ color: t.text }}>
          {'Most room right now: '}
          <Text style={{ fontWeight: '700' }}>{seatName(best.seat)}</Text>
          {` (5 h ${pct(best.five)} · 7 d ${pct(best.seven)})`}
        </Text>
      ) : null}
      {seats.length === 0 ? (
        <Calm>No accounts yet.</Calm>
      ) : (
        <View style={{ borderTopWidth: 1, borderTopColor: t.line }}>
          {seats.map((s) => (
            <AccountRow
              key={s.id}
              seat={s}
              colour={idx.get(s.id)?.colour ?? '#8A9098'}
              now={api.now}
              highlight={focus === s.id}
              expanded={open.has(s.id)}
              onToggle={() => toggle([s.id], !open.has(s.id))}
              canCountdown={!!api.countdown?.can(s.id)}
              tab={tabOf(s)}
              onTab={() => go.tab(s.id)}
              onFix={() => setFixing(s.id)}
              onCountdown={() => api.countdown?.start(s.id).then(() => toast.show('Counting down on the Lock Screen'), () => toast.show('Could not start the countdown'))}
            />
          ))}
        </View>
      )}
      {seats.length ? <Muted>The bar shows the tighter of the 5 h and 7 d limits. Tap an account for everything else.</Muted> : null}
      {fixSeat ? (
        <FixSheet
          seat={fixSeat}
          tab={tabOf(fixSeat)}
          profiles={profiles}
          colourOf={(id) => idx.get(id)?.colour ?? '#8A9098'}
          onClose={() => setFixing(null)}
          onSignOut={() => {
            setFixing(null);
            signOutTab(fixSeat.id).then(
              () => go.tab(fixSeat.id, TAB_LOGIN),
              (e: unknown) => toast.show((e as { code?: string }).code === 'ERR_WY_TABS_IN_USE' ? 'Close the tab first, then try again' : 'Could not clear the tab'),
            );
          }}
          onUse={(other) => {
            setFixing(null);
            giveTab(fixSeat.id, other.id).then(
              () => toast.show(`This tab is now ${seatName(other)}'s`),
              () => toast.show('Could not move the tab'),
            );
          }}
        />
      ) : null}
    </Page>
  );
}

/**
 * + Add account: hub accounts this phone has no claude.ai tab for yet. Picking one opens that
 * seat's tab at claude.ai (the sign-in is the person's, in the tab); the app never signs in or
 * reads the page. An account the hub does not list has not been used yet on a computer.
 * `profiles` is the screen's useTabProfiles: listed by owner, so a profile moveProfile gave to
 * another seat counts for that seat, and a seat whose profile was given away is offered again.
 */
function AddAccount({ seats, profiles }: { seats: Seat[]; profiles: string[] | null }) {
  const t = useTheme();
  const [open, setOpen] = useState(false);
  const show = () => {
    setOpen(true);
    profilesChanged(); // fresh from the module
  };
  const todo = seats.filter((s) => profiles && !profiles.includes(s.id));
  return (
    <>
      <Btn kind="outline" label="+ Add account" testID="add-account" onPress={show} />
      <Modal visible={open} transparent animationType="slide" onRequestClose={() => setOpen(false)}>
        <Pressable accessibilityLabel="Close" style={{ flex: 1, backgroundColor: 'rgba(0,0,0,0.5)' }} onPress={() => setOpen(false)} />
        <View testID="add-account-sheet" style={{ backgroundColor: t.bg, padding: 16, gap: 10, borderTopLeftRadius: 16, borderTopRightRadius: 16, maxHeight: '75%' }}>
          <Text accessibilityRole="header" style={{ color: t.text, fontSize: 18, fontWeight: '700' }}>Which account?</Text>
          {profiles === null ? (
            <Muted>Loading…</Muted>
          ) : todo.length === 0 ? (
            <Muted>{seats.length === 0 ? 'No accounts on this hub yet.' : 'Every account on this hub already has a tab on this phone. Open it from its card.'}</Muted>
          ) : (
            todo.map((s) => (
              <Card
                key={s.id}
                label={`Add ${seatName(s)}`}
                onPress={() => {
                  setOpen(false);
                  go.tab(s.id);
                }}
              >
                <Text testID={`add-seat-${s.id}`} style={{ color: t.text, fontWeight: '700' }}>{seatName(s)}</Text>
                <Muted>{[s.email, planLabel(s.plan), s.org_name].filter(Boolean).join(' · ')}</Muted>
              </Card>
            ))
          )}
          <Muted>Not listed? An account appears after it is used once in Claude Code or the desktop app.</Muted>
          <Btn label="Close" testID="add-account-close" onPress={() => setOpen(false)} />
        </View>
      </Modal>
    </>
  );
}

/** One status line: an icon by tone, then the words, names in bold. */
function StatusLine({ line, testID }: { line: Line; testID: string }) {
  const t = useTheme();
  const colour = line.tone === 'ok' ? t.ok : line.tone === 'warn' ? t.needs : t.muted;
  return (
    <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: 8 }}>
      <View accessibilityElementsHidden importantForAccessibility="no-hide-descendants" style={{ width: 16, alignItems: 'center', marginTop: 2 }}>
        {line.tone === 'ok' ? (
          <Text style={{ color: colour, fontWeight: '700', fontSize: 14, lineHeight: 16 }}>✓</Text>
        ) : line.tone === 'warn' ? (
          <View style={{ width: 15, height: 15, borderRadius: 8, backgroundColor: colour, alignItems: 'center', justifyContent: 'center' }}>
            <Text style={{ color: '#FFFFFF', fontWeight: '700', fontSize: 11, lineHeight: 13 }}>!</Text>
          </View>
        ) : (
          <View style={{ width: 13, height: 13, borderRadius: 7, borderWidth: 2, borderColor: colour, marginTop: 1 }} />
        )}
      </View>
      <Text testID={testID} style={{ color: line.tone === 'ok' ? t.text : colour, fontSize: 14, flexShrink: 1 }}>
        {line.parts.map((p, i) => (
          <Text key={i} style={p.b ? { fontWeight: '700' } : undefined}>{p.t}</Text>
        ))}
      </Text>
    </View>
  );
}

/** The phone tab line is what the tab showed when it was last open: say when that was. */
const tabSeen = (v: PhoneTab, now: number): string | null => ('at' in v ? `as of the tab's last visit, ${agoLabel(new Date(v.at).toISOString(), now)}` : null);

function AccountRow({ seat, colour, now, highlight, expanded, onToggle, canCountdown, onCountdown, tab, onTab, onFix }: {
  seat: Seat; colour: string; now: number; highlight: boolean; expanded: boolean; onToggle: () => void; canCountdown: boolean;
  onCountdown: () => void; tab: PhoneTab; onTab: () => void; onFix: () => void;
}) {
  const t = useTheme();
  const tone = useToneColour();
  const ws = sortWindows(seat.windows ?? []);
  const name = seatName(seat);
  const worst = worstPace(ws, now);
  const tight = tighterWindow(seat, now);
  const resetLine = tight?.w ? resetWords(tight.w, now) : '';
  const att = attention(seat, tab, now);
  const attColour = tab.kind === 'other' || tab.kind === 'stranger' ? t.needs : tight && tight.used >= 90 ? t.bad : t.needs;
  const webAt = seat.web_raw_at;
  const tabLine = phoneTabLine(tab);
  const action = tabAction(tab);
  const seen = tabSeen(tab, now);
  const hot = !!tight && tight.used >= 90;
  return (
    <View
      testID={`seat-${seat.id}`}
      style={{ borderBottomWidth: 1, borderBottomColor: t.line, backgroundColor: highlight ? t.card : undefined }}
    >
      <Pressable
        testID={`row-${seat.id}`}
        accessibilityRole="button"
        accessibilityLabel={name}
        accessibilityHint={expanded ? 'Hides the details' : 'Shows every limit and where it is signed in'}
        accessibilityState={{ expanded }}
        onPress={onToggle}
        style={{ paddingVertical: 11, gap: 6 }}
      >
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: 10 }}>
          <Dot colour={colour} />
          <Text accessibilityRole="header" style={{ color: t.text, fontSize: 16, fontWeight: '700', flexShrink: 1 }}>{name}</Text>
          <View style={{ marginLeft: 'auto', flexDirection: 'row', alignItems: 'baseline', gap: 8 }}>
            {tight ? <Text style={{ color: t.muted, fontSize: 13 }}>{tight.short}</Text> : null}
            <Text testID={`pct-${seat.id}`} style={{ color: hot ? t.bad : t.text, fontSize: 15, fontWeight: '600' }}>{tight ? pct(tight.used) : 'no data'}</Text>
            <Text accessibilityElementsHidden style={{ color: t.muted, fontSize: 16, width: 12 }}>{expanded ? '⌄' : '›'}</Text>
          </View>
        </View>
        {tight ? (
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 10 }}>
            <View style={{ flex: 1 }}>
              <Meter value={tight.used} colour={colour} label={`${name} ${tight.short} used`} />
            </View>
            {resetLine ? <Text testID={`reset-${seat.id}`} style={{ color: t.muted, fontSize: 12 }}>{resetLine}</Text> : null}
          </View>
        ) : null}
        {att ? (
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
            <Text accessibilityElementsHidden style={{ color: attColour, fontWeight: '700', fontSize: 13 }}>!</Text>
            <Text testID={`attention-${seat.id}`} style={{ color: attColour, fontSize: 13, flexShrink: 1 }}>
              {att.line.parts.map((p, i) => (
                <Text key={i} style={p.b ? { fontWeight: '700' } : undefined}>{p.t}</Text>
              ))}
              {att.more ? <Text style={{ color: t.muted }}>{`  +${att.more}`}</Text> : null}
            </Text>
          </View>
        ) : null}
      </Pressable>
      {expanded ? (
        <View testID={`detail-${seat.id}`} style={{ paddingBottom: 14, paddingLeft: 18, gap: 10 }}>
          {ws.length ? (
            ws.map((w) => {
              const p = paceInfo(w, now);
              const used = shownUse(w, now);
              const idle = windowPhase(w, now) === 'idle';
              const when = idle ? 'not started · starts when you next use this account' : resetWords(w, now, { long: true });
              return (
                <View key={w.key} style={{ gap: 4 }}>
                  <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
                    <Text style={{ color: t.text }}>{windowLabel(w)}</Text>
                    <Text style={{ color: used != null && used >= 90 ? t.bad : t.text }}>
                      {used == null ? 'no data' : `${pct(used)} used`}
                    </Text>
                  </View>
                  <Meter value={used} colour={colour} label={`${name} ${windowLabel(w)} used`} />
                  {when ? (
                    <Muted>{`${when}${w.peak_pct != null ? ` · peak ${pct(w.peak_pct)}` : ''}`}</Muted>
                  ) : null}
                  {p ? <Text style={{ color: tone(p.cls), fontSize: 13 }}>{p.text}</Text> : null}
                </View>
              );
            })
          ) : (
            <Muted>No limit data yet.</Muted>
          )}
          {worst && worst.p.band === 'runaway' ? <Text style={{ color: t.bad, fontWeight: '600' }}>{worst.p.text}</Text> : null}
          <View style={{ gap: 6 }}>
            <Text style={{ color: t.text, fontWeight: '700' }}>Signed in</Text>
            <StatusLine line={codeLine(seat)} testID={`code-${seat.id}`} />
            {tabLine ? <StatusLine line={tabLine} testID={`phonetab-${seat.id}`} /> : null}
            {seen ? <Muted style={{ marginLeft: 24 }}>{seen}</Muted> : null}
            {action === 'open' ? <Btn label="Open claude.ai" testID={`tab-${seat.id}`} onPress={onTab} /> : null}
            {action === 'sign_in' ? <Btn kind="primary" label="Sign in on this phone" testID={`tab-${seat.id}`} onPress={onTab} /> : null}
            {action === 'fix' ? <Btn kind="danger" label="Fix this tab" testID={`fix-${seat.id}`} onPress={onFix} /> : null}
          </View>
          <View style={{ gap: 2 }}>
            <Muted>{[planLabel(seat.plan), seat.org_name].filter(Boolean).join(' · ')}</Muted>
            {seat.email ? <Muted>{seat.email}</Muted> : null}
            <Muted>{seat.live_sessions ? plural(seat.live_sessions, 'live session') : 'No live sessions'}</Muted>
            <Muted>{webAt ? `claude.ai usage read ${agoLabel(webAt, now)}` : 'No claude.ai web usage: figures come from the status line only.'}</Muted>
          </View>
          <UsageDisclosure seat={seat.id} now={now} />
          {canCountdown ? <Btn label="Count down on Lock Screen" testID={`countdown-${seat.id}`} onPress={onCountdown} /> : null}
        </View>
      ) : null}
    </View>
  );
}

type RelationState = { status: 'idle' | 'loading' | 'ok' | 'error'; rel: UsageRelation | null; error?: string };

/**
 * "Usage and the picker" (phone.md P17.8, pwa.md 3.0a): a closed disclosure inside an open row.
 * GET /v1/accounts/{seat}/relation when first opened, and again when opened more than 5 minutes
 * after the last load. The words and bar sizes are lib/format/relation.ts.
 */
function UsageDisclosure({ seat, now }: { seat: string; now: number }) {
  const t = useTheme();
  const api = useHubApi();
  const client = api.client;
  const [open, setOpen] = useState(false);
  const [st, setSt] = useState<RelationState>({ status: 'idle', rel: null });
  const loadedAt = useRef<number | null>(null);
  const seq = useRef(0);
  const busy = useRef(false);

  const load = () => {
    if (!client || busy.current) return;
    busy.current = true;
    const n = ++seq.current;
    setSt((p) => ({ status: 'loading', rel: p.rel }));
    client.relation(seat).then(
      (rel) => {
        busy.current = false;
        if (n !== seq.current) return;
        loadedAt.current = Date.now();
        setSt({ status: 'ok', rel });
      },
      (e: unknown) => {
        busy.current = false;
        if (n !== seq.current) return;
        setSt((p) => ({ status: 'error', rel: p.rel, error: errorText(e, 'Could not load the usage figures') }));
      },
    );
  };
  const toggle = () => {
    LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut);
    const next = !open;
    setOpen(next);
    if (next && relationStale(loadedAt.current, Date.now())) load();
  };

  return (
    <View testID={`usage-${seat}`} style={{ gap: 8 }}>
      <Pressable
        testID={`usage-toggle-${seat}`}
        accessibilityRole="button"
        accessibilityLabel="Usage and the picker"
        accessibilityState={{ expanded: open }}
        onPress={toggle}
        style={{ flexDirection: 'row', alignItems: 'center', gap: 6, paddingVertical: 4 }}
      >
        <Text accessibilityElementsHidden style={{ color: t.muted, fontSize: 16, width: 12 }}>{open ? '⌄' : '›'}</Text>
        <Text style={{ color: t.text, fontWeight: '700' }}>Usage and the picker</Text>
      </Pressable>
      {open ? (
        st.rel ? (
          <UsageBody rel={st.rel} now={now} />
        ) : st.status === 'error' ? (
          <View style={{ gap: 6 }}>
            <Text testID={`usage-error-${seat}`} style={{ color: t.bad, fontSize: 14 }}>{st.error}</Text>
            <Btn label="Try again" testID={`usage-retry-${seat}`} onPress={load} />
          </View>
        ) : (
          <Muted>Loading…</Muted>
        )
      ) : null}
      {open && st.rel && st.status === 'error' ? (
        <View style={{ gap: 6 }}>
          <Text style={{ color: t.bad, fontSize: 14 }}>{st.error}</Text>
          <Btn label="Try again" testID={`usage-retry-${seat}`} onPress={load} />
        </View>
      ) : null}
    </View>
  );
}

function UsageBody({ rel, now }: { rel: UsageRelation; now: number }) {
  const t = useTheme();
  const v = rel.verdict;
  const age = verdictAge(v, now);
  const cal = calibrationRows(rel.calibration);
  const restart = calibrationRestart(rel.calibration, now, rel.tz);
  const ratio = ratioBars(rel.days);
  const lg = legend(rel.days);
  const note = heavyNote(rel.heavy.this_week);
  const sub = { color: t.text, fontWeight: '600', fontSize: 14 } as const;
  return (
    <View testID="usage-body" style={{ gap: 12 }}>
      <View style={{ gap: 2 }}>
        <Text style={sub}>Picker</Text>
        <Text testID="usage-verdict" style={{ color: t.text, fontSize: 14 }}>{v ? verdictLine(v, now, rel.tz) : NO_VERDICT}</Text>
        {age ? <Muted>{age}</Muted> : null}
      </View>
      <View style={{ gap: 2 }}>
        <Text style={sub}>Calibration</Text>
        {cal.length ? (
          <>
            {cal.map((r) => (
              <Text key={r.id} testID={`usage-cal-${r.id}`} style={{ color: r.implausible ? t.needs : t.text, fontSize: 14 }}>{r.text}</Text>
            ))}
            {restart ? <Muted>{restart}</Muted> : null}
          </>
        ) : (
          <Muted>{NOT_CALIBRATED}</Muted>
        )}
      </View>
      <View style={{ gap: 4 }}>
        <Text style={sub}>5 h per 7 d point, per day</Text>
        <Bars
          testID="usage-ratio"
          bars={ratio.map((b) => ({ key: b.day, pct: b.pct, label: b.label, under: b.initial }))}
          a11y="Daily 5-hour points per weekly point, last 14 days"
        />
        {lg ? <Muted>{lg}</Muted> : null}
      </View>
      <View style={{ gap: 6 }}>
        <Text style={sub}>5 h capacity, normalised to last week</Text>
        {rel.models.length ? (
          rel.models.map((m) => {
            const cb = capacityBars(m);
            return (
              <View key={m.model} style={{ gap: 4 }}>
                <Text testID="usage-capacity" style={{ color: t.text, fontSize: 14 }}>{capacityLine(m)}</Text>
                <Bars
                  testID={`usage-capacity-${m.model}`}
                  bars={cb.bars.map((b) => ({ key: b.day, pct: b.pct, label: b.label }))}
                  ref1={cb.ref}
                  muted={!!note}
                  a11y={`${m.model} capacity index, last 14 days`}
                />
              </View>
            );
          })
        ) : (
          <Muted>No capacity index yet: it needs Claude Code use with token rows.</Muted>
        )}
        {note ? <Muted>{note}</Muted> : null}
      </View>
    </View>
  );
}

/** Small bar chart of tappable bars; the tapped bar's label shows below. `ref1` draws the 1.0 line (percent of the height). */
function Bars({ bars, ref1, muted, a11y, testID }: {
  bars: { key: string; pct: number; label: string; under?: string }[]; ref1?: number; muted?: boolean; a11y: string; testID: string;
}) {
  const t = useTheme();
  const [sel, setSel] = useState<string | null>(null);
  const picked = bars.find((b) => b.key === sel);
  return (
    <View style={{ gap: 4 }}>
      <View testID={testID} accessibilityLabel={a11y} style={{ flexDirection: 'row', alignItems: 'flex-end', height: 64, gap: 2 }}>
        {bars.map((b) => (
          <Pressable
            key={b.key}
            testID={`${testID}-${b.key}`}
            accessibilityRole="button"
            accessibilityLabel={b.label}
            onPress={() => setSel(sel === b.key ? null : b.key)}
            style={{ flex: 1, height: '100%', justifyContent: 'flex-end' }}
          >
            <View
              style={{
                height: `${b.pct}%`,
                backgroundColor: muted ? t.muted : t.accent,
                opacity: muted ? 0.45 : sel === b.key || !sel ? 1 : 0.6,
                borderTopLeftRadius: 2,
                borderTopRightRadius: 2,
              }}
            />
          </Pressable>
        ))}
        {ref1 != null ? (
          <View pointerEvents="none" style={{ position: 'absolute', left: 0, right: 0, bottom: `${ref1}%`, borderTopWidth: 1, borderTopColor: t.muted, borderStyle: 'dashed' }} />
        ) : null}
      </View>
      {bars.some((b) => b.under) ? (
        <View style={{ flexDirection: 'row', gap: 2 }}>
          {bars.map((b) => (
            <Text key={b.key} accessibilityElementsHidden style={{ flex: 1, textAlign: 'center', color: t.muted, fontSize: 10 }}>{b.under ?? ''}</Text>
          ))}
        </View>
      ) : null}
      <Text testID={`${testID}-label`} style={{ color: t.muted, fontSize: 12, minHeight: 16 }}>{picked ? picked.label : ' '}</Text>
    </View>
  );
}

/** The tab of `seat` is signed in as another account. */
function FixSheet({ seat, tab, profiles, colourOf, onClose, onSignOut, onUse }: {
  seat: Seat; tab: PhoneTab; profiles: string[] | null; colourOf: (id: string) => string;
  onClose: () => void; onSignOut: () => void; onUse: (other: Seat) => void;
}) {
  const t = useTheme();
  const choices = fixChoices(tab, profiles);
  if (!choices) return null;
  const name = seatName(seat);
  const other = tab.kind === 'other' ? tab.seat : null;
  const otherName = other ? seatName(other) : null;
  return (
    <Modal transparent animationType="slide" visible onRequestClose={onClose}>
      <Pressable testID="fix-backdrop" accessibilityLabel="Close" style={{ flex: 1, backgroundColor: 'rgba(23,25,27,0.35)' }} onPress={onClose} />
      <View testID="fix-sheet" style={{ backgroundColor: t.card, borderTopLeftRadius: 16, borderTopRightRadius: 16, paddingHorizontal: 18, paddingTop: 12, paddingBottom: 24, gap: 14 }}>
        <View style={{ width: 40, height: 4, borderRadius: 2, backgroundColor: t.line, alignSelf: 'center' }} />
        <Text accessibilityRole="header" style={{ color: t.text, fontSize: 19, fontWeight: '600' }}>
          {otherName ? `${name}'s tab is signed in as ${otherName}` : `${name}'s tab is signed in as another account`}
        </Text>
        <Text style={{ color: t.text, fontSize: 15, lineHeight: 22 }}>
          {otherName ? (
            <>
              {'The claude.ai account in this tab is the one Claude Code uses as '}
              <Text style={{ fontWeight: '700' }}>{otherName}</Text>
              {". Watchyard matched them by the account's ID, not by email."}
            </>
          ) : (
            "The claude.ai account in this tab is not one of the accounts on your hub. Watchyard matched them by the account's ID, not by email."
          )}
        </Text>
        <View style={{ backgroundColor: t.bg, borderRadius: 10, padding: 12, gap: 8 }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <Dot colour={colourOf(seat.id)} />
            <Text style={{ color: t.text }}>{'This tab is for '}<Text style={{ fontWeight: '700' }}>{name}</Text></Text>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <Dot colour={other ? colourOf(other.id) : '#8A9098'} />
            <Text style={{ color: t.text }}>
              {'It is signed in as '}
              {otherName ? <Text style={{ fontWeight: '700' }}>{otherName}</Text> : 'an account no seat has'}
            </Text>
          </View>
        </View>
        <Btn kind="primary" label={`Sign out here and sign in as ${name}`} testID="fix-signout" onPress={onSignOut} />
        {choices.use ? <Btn label={`Use this tab for ${seatName(choices.use)} instead`} testID="fix-use" onPress={() => onUse(choices.use!)} /> : null}
        <Muted>{TAB_PRIVACY_NOTE}</Muted>
      </View>
    </Modal>
  );
}
