// Accounts (phone.md P4, P17.7): a
// calm list, one row per login: its dot, name and one bar for the tighter of the 5 h and 7 d
// limits with that window's reset time, and one status line only when something needs attention.
// Tapping a row opens it in place: every window with its reset, pace and peak, where the login
// is signed in (Claude Code on each machine, from the hub, and this phone's claude.ai tab, P17.7)
// with the matching action (Open claude.ai, Sign in on this phone, Fix this tab: its
// sheet), "How should the picker use this account?" (the owner picks protected, aggressive or not
// chosen; anyone else reads it), the details with "Usage and the picker" (a closed disclosure that
// loads the seat's usage relation: the verdict in words, tiles, charts, how this works), and
// "Count down on Lock Screen" where the Live Activity qualifies (iOS).
// "+ Add account" is in the header (a tab for a hub account this phone has none for).
import { useRef, useState } from 'react';
import { LayoutAnimation, Modal, Pressable, Text, View } from 'react-native';
import type { PickMode, Seat, UsageRelation } from '../../contract/api';
import {
  agoLabel, mostRoom, paceInfo, pct, planLabel, plural, resetWords, seatIndex, seatName, shownUse, sortWindows, tighterWindow, windowLabel, windowPhase, worstPace,
} from '../../lib/format';
import { localOverrideLine, MODE_OPTIONS, MODE_WORDS, modeChoice, modeOption, type ModeChoice } from '../../lib/format/pickMode';
import {
  MODEL_COLOURS, NO_VERDICT, relationStale, GAP_MARK, TAP_HINT, usageModel, type Chart, type UsageModel,
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
  const relation = useRelation(seat.id);
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
          <ModeSection seat={seat} verdict={relation.state.rel?.verdict} />
          <View style={{ gap: 2 }}>
            <Muted>{[planLabel(seat.plan), seat.org_name].filter(Boolean).join(' · ')}</Muted>
            {seat.email ? <Muted>{seat.email}</Muted> : null}
            <Muted>{seat.live_sessions ? plural(seat.live_sessions, 'live session') : 'No live sessions'}</Muted>
            <Muted>{webAt ? `claude.ai usage read ${agoLabel(webAt, now)}` : 'No claude.ai web usage: figures come from the status line only.'}</Muted>
          </View>
          <UsageDisclosure seat={seat.id} now={now} relation={relation} />
          {canCountdown ? <Btn label="Count down on Lock Screen" testID={`countdown-${seat.id}`} onPress={onCountdown} /> : null}
        </View>
      ) : null}
    </View>
  );
}

/**
 * How should the picker use this account? (pwa.md 3.0a, setup.md 8.5 Kind.) The owner picks
 * protected, aggressive or not chosen, each with its definition, and it saves at once
 * (PATCH /v1/accounts/{seat}); a refusal puts the old choice back and says why. Anyone else
 * reads the current choice. Not chosen is highlighted. `verdict` is the loaded relation's, for
 * the line about a setting on the picker's machine.
 */
function ModeSection({ seat, verdict }: { seat: Seat; verdict: UsageRelation['verdict'] | undefined }) {
  const t = useTheme();
  const api = useHubApi();
  const owner = api.me?.owner === true;
  const [pending, setPending] = useState<{ value: ModeChoice } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const shown = pending ? pending.value : modeChoice(seat.mode);
  const current = modeOption(shown);
  const override = localOverrideLine(verdict, seat.mode);
  const choose = (value: ModeChoice) => {
    if (!api.client || pending || value === shown) return;
    setError(null);
    setPending({ value });
    api.client.patchSeat(seat.id, { mode: value satisfies PickMode | null }).then(
      () => api.kick().finally(() => setPending(null)),
      (e: unknown) => {
        setPending(null);
        setError(errorText(e, 'Could not save the choice'));
      },
    );
  };
  return (
    <View testID={`mode-${seat.id}`} style={{ gap: 6 }}>
      <Text accessibilityRole="header" style={{ color: t.text, fontWeight: '700' }}>{MODE_WORDS.question}</Text>
      {shown == null ? (
        <Text testID={`mode-none-${seat.id}`} style={{ color: t.text, fontSize: 14, backgroundColor: t.mark, borderRadius: 6, paddingHorizontal: 8, paddingVertical: 5 }}>
          {MODE_WORDS.notChosen}
        </Text>
      ) : null}
      {owner ? (
        <View accessibilityRole="radiogroup" style={{ gap: 4 }}>
          {MODE_OPTIONS.map((o) => {
            const on = o.value === shown;
            return (
              <Pressable
                key={o.name}
                testID={`mode-${seat.id}-${o.value ?? 'none'}`}
                accessibilityRole="radio"
                accessibilityLabel={`${o.name}. ${o.words}`}
                accessibilityState={{ checked: on, busy: !!pending }}
                onPress={() => choose(o.value)}
                style={{ flexDirection: 'row', alignItems: 'flex-start', gap: 10, paddingVertical: 5 }}
              >
                <View style={{ width: 18, height: 18, borderRadius: 9, borderWidth: 2, borderColor: on ? t.accent : t.muted, alignItems: 'center', justifyContent: 'center', marginTop: 1 }}>
                  {on ? <View style={{ width: 8, height: 8, borderRadius: 4, backgroundColor: t.accent }} /> : null}
                </View>
                <View style={{ flex: 1, gap: 1 }}>
                  <Text style={{ color: t.text, fontWeight: '600', fontSize: 14 }}>{o.name}</Text>
                  <Muted>{o.words}</Muted>
                </View>
              </Pressable>
            );
          })}
        </View>
      ) : shown != null ? (
        <Text testID={`mode-current-${seat.id}`} style={{ color: t.text, fontSize: 14 }}>
          <Text style={{ fontWeight: '700' }}>{`${current.name}. `}</Text>
          {current.words}
        </Text>
      ) : null}
      {error ? <Text testID={`mode-error-${seat.id}`} style={{ color: t.bad, fontSize: 13 }}>{error}</Text> : null}
      {override ? <Text testID={`mode-override-${seat.id}`} style={{ color: t.needs, fontSize: 13 }}>{override}</Text> : null}
    </View>
  );
}

type RelationState = { status: 'idle' | 'loading' | 'ok' | 'error'; rel: UsageRelation | null; error?: string };

interface RelationLoad {
  state: RelationState;
  /** Load now. */
  load: () => void;
  /** Load when never loaded, or loaded more than 5 minutes ago. */
  loadIfStale: () => void;
}

/**
 * GET /v1/accounts/{seat}/relation, kept by the row so the mode section can use the verdict too.
 * Loaded when "Usage and the picker" first opens, and again when it opens more than 5 minutes after the last load.
 */
function useRelation(seat: string): RelationLoad {
  const client = useHubApi().client;
  const [state, setSt] = useState<RelationState>({ status: 'idle', rel: null });
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
  return { state, load, loadIfStale: () => (relationStale(loadedAt.current, Date.now()) ? load() : undefined) };
}

/**
 * "Usage and the picker" (phone.md P4, pwa.md 3.0a): a closed disclosure inside an open row.
 * The words, tiles and chart models are lib/format/relation.ts.
 */
function UsageDisclosure({ seat, now, relation }: { seat: string; now: number; relation: RelationLoad }) {
  const t = useTheme();
  const [open, setOpen] = useState(false);
  const st = relation.state;
  const toggle = () => {
    LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut);
    const next = !open;
    setOpen(next);
    if (next) relation.loadIfStale();
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
            <Btn label="Try again" testID={`usage-retry-${seat}`} onPress={relation.load} />
          </View>
        ) : (
          <Muted>Loading…</Muted>
        )
      ) : null}
      {open && st.rel && st.status === 'error' ? (
        <View style={{ gap: 6 }}>
          <Text style={{ color: t.bad, fontSize: 14 }}>{st.error}</Text>
          <Btn label="Try again" testID={`usage-retry-${seat}`} onPress={relation.load} />
        </View>
      ) : null}
    </View>
  );
}

/** In order: verdict, tiles, session line, charts, capacity, and a closed "How this works". */
function UsageBody({ rel, now }: { rel: UsageRelation; now: number }) {
  const t = useTheme();
  const u = usageModel(rel, now);
  const sub = { color: t.text, fontWeight: '600', fontSize: 14 } as const;
  return (
    <View testID="usage-body" style={{ gap: 14 }}>
      <VerdictBlock v={u.verdict} />
      {u.tiles ? (
        <View style={{ gap: 6 }}>
          <Text accessibilityRole="header" style={sub}>The 5-hour limit and the week</Text>
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 8 }}>
            {u.tiles.tiles.map((text, i) => (
              <View key={i} testID={`usage-tile-${i}`} style={{ flexGrow: 1, flexBasis: 130, backgroundColor: t.card, borderWidth: 1, borderColor: t.line, borderRadius: 10, padding: 10 }}>
                <Text style={{ color: t.text, fontSize: 14, fontWeight: '600' }}>{text}</Text>
              </View>
            ))}
          </View>
          <Muted>{u.tiles.label}</Muted>
        </View>
      ) : null}
      <Text testID="usage-session" style={{ color: t.text, fontSize: 14 }}>{u.session}</Text>
      {u.ratio ? <ChartView chart={u.ratio} testID="usage-ratio" /> : <Muted>{u.noRatio}</Muted>}
      <CapacityBlock u={u} />
      <HowBlock rows={u.how} />
    </View>
  );
}

function VerdictBlock({ v }: { v: UsageModel['verdict'] }) {
  const t = useTheme();
  if (!v) return <Text testID="usage-verdict" style={{ color: t.text, fontSize: 14 }}>{NO_VERDICT}</Text>;
  return (
    <View testID="usage-verdict" style={{ gap: 4 }}>
      <Text accessibilityRole="header" testID="usage-headline" style={{ color: v.eligible ? t.ok : t.needs, fontWeight: '700', fontSize: 15 }}>{v.headline}</Text>
      {v.place ? <Text style={{ color: t.text, fontSize: 14 }}>{v.place}</Text> : null}
      <Text testID="usage-kind" style={[{ color: t.text, fontSize: 14 }, v.notChosen ? { backgroundColor: t.mark } : null]}>
        <Text style={{ fontWeight: '700' }}>{`${v.kindName}. `}</Text>
        {v.kindWords}
      </Text>
      {v.rules.map((r) => (
        <Text key={r.text} testID="usage-rule" style={{ color: r.blocking ? t.bad : t.text, fontSize: 14 }}>
          {`• ${r.text}`}
          {r.blocking ? <Text style={{ fontWeight: '700' }}>{' (blocking)'}</Text> : null}
        </Text>
      ))}
      {v.why ? <Text style={{ color: t.text, fontSize: 14 }}>{`Not taking sessions because ${v.why}.`}</Text> : null}
      {v.frees ? <Text style={{ color: t.text, fontSize: 14 }}>{v.frees}</Text> : null}
      {v.noneFree ? <Text style={{ color: t.text, fontSize: 14 }}>{v.noneFree}</Text> : null}
      {v.age ? <Muted>{v.age}</Muted> : null}
    </View>
  );
}

function CapacityBlock({ u }: { u: UsageModel }) {
  const t = useTheme();
  const cap = u.capacity;
  if (!cap.models.length) return null;
  return (
    <View style={{ gap: 8 }}>
      {cap.models.map((m) => (
        <View key={m.model} style={{ gap: 6 }}>
          <Text testID="usage-capacity" style={{ color: t.text, fontSize: 14 }}>{m.text}</Text>
          {m.chart ? <ChartView chart={m.chart} testID={`usage-capacity-${m.model}`} /> : null}
        </View>
      ))}
      {cap.alertNote ? <Muted>{cap.alertNote}</Muted> : null}
      {cap.note ? <Muted>{cap.note}</Muted> : null}
    </View>
  );
}

function HowBlock({ rows }: { rows: UsageModel['how'] }) {
  const t = useTheme();
  const [open, setOpen] = useState(false);
  return (
    <View style={{ gap: 6 }}>
      <Pressable
        testID="usage-how-toggle"
        accessibilityRole="button"
        accessibilityLabel="How this works"
        accessibilityState={{ expanded: open }}
        onPress={() => {
          LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut);
          setOpen(!open);
        }}
        style={{ flexDirection: 'row', alignItems: 'center', gap: 6, paddingVertical: 4 }}
      >
        <Text accessibilityElementsHidden style={{ color: t.muted, fontSize: 16, width: 12 }}>{open ? '⌄' : '›'}</Text>
        <Text style={{ color: t.text, fontWeight: '600', fontSize: 14 }}>How this works</Text>
      </Pressable>
      {open ? (
        <View testID="usage-how" style={{ gap: 6 }}>
          {rows.map((r) => (
            <Text key={r.title} style={{ color: t.text, fontSize: 14 }}>
              <Text style={{ fontWeight: '700' }}>{`${r.title}. `}</Text>
              {r.text}
            </Text>
          ))}
        </View>
      ) : null}
    </View>
  );
}

const PLOT_H = 96;
const AXIS_W = 44;

/**
 * One chart: a title saying what is plotted and what higher means, y values, tappable days (the
 * value shows under the chart), "no data" gaps, a legend of model names. Drawn with Views, like the
 * Analytics bars; the app has no SVG library.
 */
function ChartView({ chart, testID }: { chart: Chart; testID: string }) {
  const t = useTheme();
  const [sel, setSel] = useState<string | null>(null);
  const picked = chart.bars.find((b) => b.day === sel);
  return (
    <View testID={testID} accessibilityLabel={chart.summary} style={{ gap: 4 }}>
      <Text accessibilityLabel={chart.summary} style={{ color: t.text, fontSize: 14, fontWeight: '600' }}>{chart.title}</Text>
      <Muted>{chart.higher}</Muted>
      <View style={{ flexDirection: 'row' }}>
        <View accessibilityElementsHidden importantForAccessibility="no-hide-descendants" style={{ width: AXIS_W, height: PLOT_H }}>
          {chart.ticks.map((k) => (
            <Text key={k.value} style={{ position: 'absolute', right: 6, bottom: `${k.pct}%`, marginBottom: -7, color: t.muted, fontSize: 11 }}>{k.label}</Text>
          ))}
        </View>
        <View style={{ flex: 1, height: PLOT_H }}>
          {chart.ticks.map((k) => (
            <View key={k.value} pointerEvents="none" style={{ position: 'absolute', left: 0, right: 0, bottom: `${k.pct}%`, borderTopWidth: 1, borderTopColor: k.value === 0 ? t.muted : t.line }} />
          ))}
          <View style={{ flex: 1, flexDirection: 'row', alignItems: 'flex-end', gap: 2 }}>
            {chart.bars.map((b) => (
              <Pressable
                key={b.day}
                testID={`${testID}-${b.day}`}
                accessibilityRole="button"
                accessibilityLabel={b.label}
                onPress={() => setSel(sel === b.day ? null : b.day)}
                style={{ flex: 1, height: '100%', justifyContent: 'flex-end' }}
              >
                {b.gap ? (
                  <View style={{ height: 3, marginBottom: 1, backgroundColor: t.muted, opacity: 0.5, borderRadius: 1 }} />
                ) : (
                  <View
                    style={{
                      height: `${b.pct}%`,
                      backgroundColor: chart.muted ? t.muted : b.colour >= 0 ? MODEL_COLOURS[b.colour] : t.accent,
                      opacity: chart.muted ? 0.45 : sel && sel !== b.day ? 0.55 : 1,
                      borderTopLeftRadius: 2,
                      borderTopRightRadius: 2,
                    }}
                  />
                )}
              </Pressable>
            ))}
          </View>
          {chart.ref ? (
            <View pointerEvents="none" style={{ position: 'absolute', left: 0, right: 0, bottom: `${chart.ref.pct}%` }}>
              <View style={{ borderTopWidth: 1, borderTopColor: t.text, borderStyle: 'dashed' }} />
              <Text style={{ position: 'absolute', right: 0, bottom: 2, color: t.text, fontSize: 11, backgroundColor: t.bg }}>{chart.ref.label}</Text>
            </View>
          ) : null}
        </View>
      </View>
      <View accessibilityElementsHidden style={{ flexDirection: 'row', justifyContent: 'space-between', marginLeft: AXIS_W }}>
        {chart.axis.map((a, i) => (
          <Text key={i} style={{ color: t.muted, fontSize: 11 }}>{a}</Text>
        ))}
      </View>
      {chart.hasGaps ? <Muted>{`– ${GAP_MARK}`}</Muted> : null}
      {chart.legend.length ? (
        <View testID={`${testID}-legend`} style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 12 }}>
          {chart.legend.map((l) => (
            <View key={l.model} style={{ flexDirection: 'row', alignItems: 'center', gap: 5 }}>
              <View accessibilityElementsHidden style={{ width: 10, height: 10, borderRadius: 2, backgroundColor: MODEL_COLOURS[l.colour] }} />
              <Text style={{ color: t.text, fontSize: 12 }}>{l.model}</Text>
            </View>
          ))}
        </View>
      ) : null}
      <Text testID={`${testID}-label`} style={{ color: t.text, fontSize: 12, minHeight: 16 }}>{picked ? picked.label : ' '}</Text>
      {chart.hint ? <Muted>{TAP_HINT}</Muted> : null}
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
