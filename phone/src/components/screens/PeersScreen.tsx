// Peers (PE3; peers.md 5, 10, 11): the matrix, Pause all peers and the audit list. Read-only
// except narrowing: a tapped cell can only lose pairs, Block all removes every pair, and the pause
// switch only pauses. The turn limit (6.3) is shown, not edited.
// Below them, as on the board: the trusted senders (Doorbell; Revoke narrows, so the paired phone
// may) and the tags sessions gave themselves (peers.md 3.4, 4.1a).
// Allowing, and resuming after a pause, need the owner on the board or at a terminal.
import { useEffect, useState } from 'react';
import { Alert, Pressable, ScrollView, Switch, Text, View } from 'react-native';
import type { MatrixInfo, PeerAuditEntry, PeerMessage, PeerSession, TrustedSender } from '../../contract/api';
import {
  agoLabel, auditWho, ALLOW_FROM_COMPUTER, BLOCK_ALL_CONFIRM, blockAllEdit, blockedAll, TURNS_HELP, TURNS_NONE, NO_LIMIT_HELP, turnsLabel, cellLabel, cellState, CELL_GLYPH, CELL_WORD, detailText, eventLabel, messageWho, narrowings,
  outcomeLabel, peerAccounts, sizeLabel, NO_TAGS, NO_HOLDER, NO_TRUSTED, TRUSTED_UNREPORTED, REVOKE_NOT_FOUND, revokeConfirm, tagGroups, trustedSenderLines,
} from '../../lib/format';
import { HubError, OWNER_ONLY, errorText } from '../../lib/hub';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { switchColors, useTheme } from '../theme';
import { Btn, Calm, Card, Muted, Page, Section, useToast } from '../ui';

const CELL = 44;

export default function PeersScreen() {
  const api = useHubApi();
  const t = useTheme();
  const toast = useToast();
  const client = api.client;
  const [info, setInfo] = useState<MatrixInfo | null>(null);
  const [audit, setAudit] = useState<PeerAuditEntry[] | null>(null);
  const [messages, setMessages] = useState<PeerMessage[] | null>(null);
  // null: the hub does not serve it (404), or it could not be read; the section is then hidden.
  const [revokeNote, setRevokeNote] = useState<Record<string, string>>({});
  const [trusted, setTrusted] = useState<TrustedSender[] | null>(null);
  const [live, setLive] = useState<PeerSession[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pick, setPick] = useState<{ from: string; to: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const offline = api.data.offline;

  const [tick, setTick] = useState(0); // bumped to load again
  const load = () => setTick((n) => n + 1);

  useEffect(() => {
    if (!client) return;
    let alive = true;
    void Promise.allSettled([client.matrix(), client.peerAudit({ limit: 50 }), client.peerMessages({ limit: 50 }), client.trustedSenders(), client.peers()]).then(([m, a, msg, tr, ps]) => {
      if (!alive) return;
      if (m.status === 'fulfilled') {
        setInfo(m.value);
        setError(null);
      } else {
        setError(errorText(m.reason, 'Could not load the matrix'));
      }
      if (a.status === 'fulfilled') setAudit(a.value.entries ?? []);
      // Optional: a hub that does not serve it answers 404 or 501.
      setMessages(msg.status === 'fulfilled' ? msg.value.messages ?? [] : null);
      // A 404 from a hub without the endpoint hides the section.
      setTrusted(tr.status === 'fulfilled' ? tr.value.senders ?? [] : null);
      setLive(ps.status === 'fulfilled' ? ps.value.sessions ?? [] : null);
    });
    return () => {
      alive = false;
    };
  }, [client, tick]);

  const revoke = async (x: TrustedSender) => {
    setBusy(true);
    setRevokeNote(({ [x.machine]: _gone, ...rest }) => rest);
    try {
      const next = await client!.revokeTrustedSender(x.machine);
      setTrusted((cur) => (cur ? cur.map((y) => (y.machine === next.machine ? next : y)) : cur));
      load();
    } catch (e) {
      if (e instanceof HubError && e.status === 404) {
        setRevokeNote((cur) => ({ ...cur, [x.machine]: e.hubMessage ?? REVOKE_NOT_FOUND }));
      } else {
        toast.show(errorText(e, 'Could not revoke Doorbell'));
      }
    }
    setBusy(false);
  };

  const run = async (send: () => Promise<MatrixInfo>) => {
    setBusy(true);
    try {
      setInfo(await send());
      setPick(null);
      load();
    } catch (e) {
      if (e instanceof HubError && e.status === 409) {
        toast.show('The matrix changed meanwhile. Check it and try again.');
        load();
      } else if (e instanceof HubError && e.status === 403) {
        toast.show(OWNER_ONLY);
      } else {
        toast.show(errorText(e, 'Could not change the matrix'));
      }
    }
    setBusy(false);
  };

  if (!info) {
    return (
      <Page back title="Peers" toast={toast.node}>
        <Calm>{error ?? (offline ? 'Offline.' : 'Loading…')}</Calm>
      </Page>
    );
  }

  const m = info.matrix;
  const accounts = peerAccounts(api.data.overview?.seats ?? [], m);
  const names = new Map(accounts.map((a) => [a.account, a.label]));
  const label = (id: string) => names.get(id) ?? id.slice(0, 8);
  const off = busy || offline || !client;
  const owner = api.me?.owner === true;
  // Revoke narrows, so the paired phone (push scope) may; a read-only sign-in may not.
  const canRevoke = !api.me || api.me.device.scopes.includes('push');
  const tags = tagGroups(live ?? [], trusted ?? []);
  const picked = pick ? { from: accounts.find((a) => a.account === pick.from), to: accounts.find((a) => a.account === pick.to) } : null;
  const options = pick ? narrowings(m.allow, pick.from, pick.to, m.version) : [];
  const word = (target: 'none' | 'out' | 'in' | 'both', from: string, to: string) =>
    target === 'none' ? 'None' : target === 'out' ? `${from} → ${to}` : target === 'in' ? `${to} → ${from}` : 'Both ways';

  return (
    <Page back title="Peers" meta={`Matrix version ${m.version} · signed by ${info.signer} · ${m.max_turns === TURNS_NONE ? 'no turn limit' : `max ${m.max_turns} turns`}`} toast={toast.node}>
      <Section title="Pause all peers">
        <Card accent={m.paused ? t.bad : undefined}>
          <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
            <View style={{ flexShrink: 1, paddingRight: 8 }}>
              <Text testID="peers-state" style={{ color: m.paused ? t.bad : t.text, fontWeight: '700', fontSize: 16 }}>
                {m.paused ? 'All peers are paused' : 'Peers are on'}
              </Text>
              <Muted>
                {m.paused
                  ? 'No message passes between sessions on any machine until the owner resumes.'
                  : 'Pausing stops every machine at once: new messages are refused and queued ones dropped.'}
              </Muted>
            </View>
            <Switch
              {...switchColors(t)}
              accessibilityLabel="Pause all peers"
              testID="peers-pause"
              value={m.paused}
              disabled={off || (m.paused && !owner)}
              onValueChange={(on) => void run(() => client!.pausePeers(on))}
            />
          </View>
          {m.paused && !owner ? <Muted>Resume peers from a computer (board Peers page).</Muted> : null}
        </Card>
      </Section>

      <Section title="Accounts">
        <Card>
          <View testID="peers-bulk" style={{ gap: 6 }}>
            <Btn
              testID="block-all"
              kind="danger"
              label="Block all"
              disabled={off || blockedAll(m.allow, m.same_account)}
              onPress={() =>
                Alert.alert('Block all?', BLOCK_ALL_CONFIRM, [
                  { text: 'Cancel', style: 'cancel' },
                  { text: 'Block all', style: 'destructive', onPress: () => void run(() => client!.editMatrix(blockAllEdit(m.allow, m.same_account, m.version))) },
                ])
              }
            />
            <Muted>{ALLOW_FROM_COMPUTER}</Muted>
          </View>
          {accounts.length < 2 ? (
            <Muted>Peers between accounts need at least two accounts on this hub.</Muted>
          ) : (
            <ScrollView horizontal>
              <View testID="matrix">
                <View style={{ flexDirection: 'row' }}>
                  <View style={{ width: 84 }} />
                  {accounts.map((c) => (
                    <Text key={c.account} numberOfLines={1} style={{ width: CELL, color: t.muted, fontSize: 11, textAlign: 'center' }}>{c.label}</Text>
                  ))}
                </View>
                {accounts.map((r) => (
                  <View key={r.account} style={{ flexDirection: 'row', alignItems: 'center' }}>
                    <Text numberOfLines={1} style={{ width: 84, color: t.text, fontSize: 13 }}>{r.label}</Text>
                    {accounts.map((c) => {
                      if (r.account === c.account) {
                        return (
                          <Text key={c.account} accessibilityLabel={`${r.label} to itself: same account ${m.same_account ? 'on' : 'off'}`} style={{ width: CELL, textAlign: 'center', color: t.muted }}>
                            =
                          </Text>
                        );
                      }
                      const st = cellState(m.allow, r.account, c.account);
                      const on = pick?.from === r.account && pick?.to === c.account;
                      return (
                        <Pressable
                          key={c.account}
                          accessibilityRole="button"
                          accessibilityLabel={cellLabel(st, r.label, c.label)}
                          accessibilityState={{ selected: on }}
                          testID={`cell-${r.account}-${c.account}`}
                          onPress={() => setPick(on ? null : { from: r.account, to: c.account })}
                          style={{
                            width: CELL - 4, height: CELL - 4, margin: 2, borderRadius: 8, borderWidth: on ? 2 : 1,
                            borderColor: on ? t.accent : t.line, alignItems: 'center', justifyContent: 'center',
                          }}
                        >
                          <Text style={{ color: st === 'none' ? t.muted : t.ok, fontSize: 18 }}>{CELL_GLYPH[st]}</Text>
                        </Pressable>
                      );
                    })}
                  </View>
                ))}
              </View>
            </ScrollView>
          )}
          <Muted>⇄ both ways · → may start a thread · ← only the other may start one · · none · = same account{m.same_account ? '' : ' (off)'}. Replies inside a thread are always allowed.</Muted>
          <View testID="peers-turns" style={{ gap: 2, marginTop: 4 }}>
            <Text style={{ color: t.text, fontWeight: '600' }}>{`Replies between two sessions without you: ${turnsLabel(m.max_turns)}`}</Text>
            <Muted>{TURNS_HELP}</Muted>
            <Muted>{NO_LIMIT_HELP}</Muted>
            <Muted>Change it on the board (Peers).</Muted>
          </View>
          {picked?.from && picked.to ? (
            <View testID="cell-editor" style={{ gap: 6, marginTop: 4 }}>
              <Text style={{ color: t.text, fontWeight: '600' }}>{`${picked.from.label} and ${picked.to.label}: ${CELL_WORD[cellState(m.allow, pick!.from, pick!.to)]}`}</Text>
              {options.length === 0 ? (
                <Muted>Nothing to narrow here. Allowing a pair needs the owner on the board or at a terminal.</Muted>
              ) : (
                options.map((o) => (
                  <Btn
                    key={o.target}
                    testID={`narrow-${o.target}`}
                    label={word(o.target, picked.from!.label, picked.to!.label)}
                    disabled={off}
                    onPress={() => void run(() => client!.editMatrix(o.edit))}
                  />
                ))
              )}
            </View>
          ) : (
            <Muted>Tap a cell to narrow it. Allowing a pair is done on the board.</Muted>
          )}
        </Card>
      </Section>

      <Section title="Audit">
        <Muted>Who changed what, and when. Metadata only: no message text is ever stored or shown.</Muted>
        {!audit ? (
          <Calm>Loading…</Calm>
        ) : audit.length === 0 ? (
          <Calm>Nothing yet.</Calm>
        ) : (
          <Card>
            {audit.map((e) => (
              <View key={e.id} testID={`audit-${e.id}`} style={{ flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 4 }}>
                <View style={{ flexShrink: 1, paddingRight: 8 }}>
                  <Text style={{ color: t.text }}>{eventLabel(e.event, e.detail)}</Text>
                  <Muted>{[auditWho(e, label), detailText(e.detail)].filter(Boolean).join(' · ')}</Muted>
                </View>
                <Muted>{agoLabel(e.at, api.now)}</Muted>
              </View>
            ))}
          </Card>
        )}
      </Section>

      {messages ? (
        <Section title="Messages">
          <Muted>Who messaged whom: when, size and outcome. Never the text.</Muted>
          {messages.length === 0 ? (
            <Calm>No messages yet.</Calm>
          ) : (
            <Card>
              {messages.map((x) => (
                <View key={x.id} testID={`msg-${x.id}`} style={{ flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 4 }}>
                  <View style={{ flexShrink: 1, paddingRight: 8 }}>
                    <Text style={{ color: t.text }}>{messageWho(x, label)}</Text>
                    <Muted>{[x.kind, sizeLabel(x.size), outcomeLabel(x.status)].filter(Boolean).join(' · ')}</Muted>
                  </View>
                  <Muted>{agoLabel(x.created_at, api.now)}</Muted>
                </View>
              ))}
            </Card>
          )}
        </Section>
      ) : null}

      {trusted ? (
        <Section title="Trusted senders">
          {trusted.length === 0 ? (
            <View style={{ gap: 6 }}>
              <Calm>{NO_TRUSTED}</Calm>
              <Muted>{TRUSTED_UNREPORTED}</Muted>
            </View>
          ) : (
            <Card>
              {trusted.map((x) => {
                const l = trustedSenderLines(x, api.now);
                return (
                  <View key={x.machine} testID={`trusted-${x.machine}`} style={{ gap: 2, paddingVertical: 4 }}>
                    <Text style={{ color: l.revoked ? t.muted : t.text, fontWeight: '600' }}>{l.title}</Text>
                    {l.lines.map((line) => (
                      <Muted key={line}>{line}</Muted>
                    ))}
                    {revokeNote[x.machine] ? (
                      <Text testID={`revoke-error-${x.machine}`} accessibilityRole="alert" style={{ color: t.text }}>
                        {revokeNote[x.machine]}
                      </Text>
                    ) : null}
                    {!l.revoked && canRevoke ? (
                      <Btn
                        testID={`revoke-${x.machine}`}
                        kind="danger"
                        label="Revoke"
                        disabled={off}
                        onPress={() =>
                          Alert.alert('Revoke?', revokeConfirm(l.machine), [
                            { text: 'Cancel', style: 'cancel' },
                            { text: 'Revoke', style: 'destructive', onPress: () => void revoke(x) },
                          ])
                        }
                      />
                    ) : null}
                  </View>
                );
              })}
            </Card>
          )}
        </Section>
      ) : null}

      {live ? (
        <Section title="Tags">
          {tags.length === 0 ? (
            <Calm>{NO_TAGS}</Calm>
          ) : (
            <Card>
              {tags.map((g) => (
                <View key={g.tag} testID={`tag-${g.tag}`} style={{ gap: 2, paddingVertical: 4 }}>
                  <Text style={{ color: t.text, fontWeight: '600' }}>{g.tag}</Text>
                  {g.holders.map((h) => (
                    <Pressable key={h.id} accessibilityRole="button" accessibilityLabel={`Open session ${h.name}`} testID={`tag-${g.tag}-holder-${h.id}`} onPress={() => go.session(h.id)} style={{ minHeight: 44, justifyContent: 'center' }}>
                      <Text style={{ color: t.accent }}>{h.name}</Text>
                      <Muted>{h.detail}</Muted>
                    </Pressable>
                  ))}
                  {g.noHolder ? <Muted>{NO_HOLDER}</Muted> : null}
                  {g.recent.map((r) => (
                    <Muted key={r}>{r}</Muted>
                  ))}
                </View>
              ))}
            </Card>
          )}
        </Section>
      ) : null}
    </Page>
  );
}
