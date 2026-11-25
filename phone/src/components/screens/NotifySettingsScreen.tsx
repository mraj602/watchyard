// Settings → Notifications (phone.md P6.7, N1): the wy-link module's switch and state, the
// battery check with the maker guide, the permission, the verification state, the ping
// interval, the per-category switches (hub-side mute) with a button to each channel's system
// settings (sound), the test notification and the Web Push backup.
import { useCallback, useEffect, useState } from 'react';
import { Linking, Switch, Text, View } from 'react-native';
import type { LinkState } from '../../contract/link';
import type { PushTestResult } from '../../contract/api';
import * as Notifications from '../../../modules/wy-notify';
import { NOTIFY_CATEGORIES } from '../../contract/constants';
import { batteryWords, knownMaker, linkErrorWords, linkStatusWords, makerGuide, PING_CHOICES_S, verifiedWords } from '../../lib/notify';
import { useHubApi } from '../hub-api';
import { switchColors, useTheme } from '../theme';
import { useSelf } from '../use-self';
import { Btn, Calm, Card, Chip, KV, Muted, Page, Section, useToast } from '../ui';
import { testText } from './SettingsScreen';

/** "3 min" for 180. */
const minutes = (s: number) => `${Math.round(s / 60)} min`;

export default function NotifySettingsScreen() {
  const api = useHubApi();
  const t = useTheme();
  const toast = useToast();
  const { self, savePrefs } = useSelf(toast.show);
  const link = api.link;
  const [s, setS] = useState<LinkState | null>(null);
  const [test, setTest] = useState<PushTestResult | null>(null);
  const [testing, setTesting] = useState(false);

  const reload = useCallback(async () => {
    if (!link) return;
    try {
      setS(await link.getState());
    } catch {
      // keep the last one
    }
  }, [link]);

  useEffect(() => {
    if (!link) return;
    const sub = link.addListener('state', setS);
    return () => sub.remove();
  }, [link]);

  // Read on open and on the 30 s tick (the battery exemption and permission change outside the app).
  useEffect(() => {
    if (!link) return;
    let live = true;
    link.getState().then(
      (x) => live && setS(x),
      () => {},
    );
    return () => {
      live = false;
    };
  }, [link, api.now]);

  if (!link) {
    return (
      <Page back title="Notifications">
        <Calm>Instant delivery is part of the Android app built without Google services. This build uses the system push service.</Calm>
      </Page>
    );
  }

  const target = (self?.targets ?? []).find((x) => x.kind === 'link' && x.status !== 'gone');
  const noTarget = !!self && !target;

  const act = async (what: string, f: () => Promise<unknown>) => {
    try {
      await f();
    } catch (e) {
      toast.show(`${what}: ${e instanceof Error ? e.message.replace(/^wy-link: /, '') : 'failed'}`);
    }
    await reload();
  };

  const setInstant = (on: boolean) =>
    act(on ? 'Could not start' : 'Could not stop', async () => {
      if (on) {
        await link.start();
        // The target stays enabled when turned off (P6.7); turning on re-enables one turned off on the board.
        if (target && api.client && !target.enabled) await api.client.patchTarget(target.id, { enabled: true });
      } else {
        await link.stop();
      }
    });

  const fixBattery = () =>
    act('Could not ask', async () => {
      const exempt = await link.requestBatteryExemption();
      if (!exempt) toast.show('Still optimised: follow the steps below');
    });

  const sendTest = async () => {
    if (!api.client || !target) return;
    setTesting(true);
    setTest(null);
    try {
      setTest(await api.client.testTarget(target.id));
    } catch (e) {
      setTest({ outcome: 'failed', error: e instanceof Error ? e.message : 'failed' });
    }
    setTesting(false);
  };

  const localTest = async () => {
    try {
      await link.sendLocalTest();
      toast.show('Posted a test notification from this phone');
    } catch {
      toast.show('Could not post a test notification');
    }
  };

  const muted = new Set<string>(self?.prefs?.mute_categories ?? []);
  const setCategory = (id: string, on: boolean) => {
    const next = new Set(muted);
    if (on) next.delete(id);
    else next.add(id);
    // The catalogue's order first; values this app does not know are kept as they were.
    const known: string[] = NOTIFY_CATEGORIES.map((c) => c.id).filter((c) => next.has(c));
    const other = [...next].filter((c) => !known.includes(c));
    return savePrefs({ mute_categories: [...known, ...other] });
  };
  const openSound = (channel: string) => act('Could not open settings', () => Notifications.openChannelSettingsAsync(channel));

  const guide = makerGuide(api.app.maker);
  const showGuide = !!s && (!s.batteryExempt || knownMaker(api.app.maker));
  const origin = api.client?.origin;
  const err = s ? linkErrorWords(s.lastError) : null;

  return (
    <Page back title="Notifications" toast={toast.node}>
      {noTarget ? (
        <Card>
          <Text testID="link-no-target" style={{ color: t.text }}>
            This phone has no link on the hub yet. Unpair and pair it again to turn on instant delivery.
          </Text>
        </Card>
      ) : null}

      <Section title="Delivery">
        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
            <View style={{ flexShrink: 1, paddingRight: 8 }}>
              <Text style={{ color: t.text, fontWeight: '600' }}>Instant delivery</Text>
              <Muted>Keeps a connection to your hub. Uses a small, constant notification.</Muted>
            </View>
            <Switch
              {...switchColors(t)}
              accessibilityLabel="Instant delivery"
              testID="link-enabled"
              value={!!s?.enabled}
              disabled={!s || s.status === 'revoked'}
              onValueChange={(on) => void setInstant(on)}
            />
          </View>
        </Card>
      </Section>

      <Section title="Connection">
        {!s ? (
          <Calm>Loading…</Calm>
        ) : (
          <Card>
            <Text testID="link-status" style={{ color: t.text }}>
              {linkStatusWords(s, api.now)}
            </Text>
            {err && s.status !== 'error' ? <Muted>{`Last error: ${err}`}</Muted> : null}
            <KV k="Received" v={String(s.received)} />
            <KV k="Rejected" v={String(s.rejected)} />
            <Btn label="Reconnect now" testID="link-reconnect" disabled={!s.enabled || s.status === 'revoked'} onPress={() => void act('Could not reconnect', () => link.reconnect())} />
          </Card>
        )}
      </Section>

      {s ? (
        <Section title="Battery">
          <Card>
            <Text testID="link-battery" style={{ color: t.text }}>
              {batteryWords(s.batteryExempt)}
            </Text>
            {!s.batteryExempt ? <Btn kind="primary" label="Fix" testID="link-battery-fix" onPress={() => void fixBattery()} /> : null}
            {showGuide ? (
              <>
                <Muted>{guide.text}</Muted>
                <Btn label="Open settings" testID="link-battery-settings" onPress={() => void act('Could not open settings', () => link.openBatterySettings())} />
              </>
            ) : null}
          </Card>
        </Section>
      ) : null}

      {s ? (
        <Section title="This phone">
          <Card>
            <KV k="Notifications" v={s.notificationsAllowed ? 'Allowed' : 'Blocked in system settings'} />
            {!s.notificationsAllowed ? <Btn label="Open settings" testID="link-perm-settings" onPress={() => void act('Could not open settings', () => Linking.openSettings())} /> : null}
            <Text testID="link-verified" style={{ color: s.verified === 'changed' ? t.bad : t.text, marginTop: 8 }}>
              {verifiedWords(s)}
            </Text>
            <Text style={{ color: t.text, fontWeight: '600', marginTop: 8 }}>{`Ping interval: every ${minutes(s.pingS)}`}</Text>
            <Muted>Longer intervals save battery; use 5 or 10 minutes only on the tailnet.</Muted>
            <View style={{ flexDirection: 'row', flexWrap: 'wrap' }}>
              {PING_CHOICES_S.map((v) => (
                <Chip key={v} testID={`link-ping-${v}`} label={minutes(v)} on={s.pingS === v} onPress={() => void act('Could not change', () => link.setPingInterval(v))} />
              ))}
            </View>
          </Card>
        </Section>
      ) : null}

      <Section title="Categories">
        <Card>
          {NOTIFY_CATEGORIES.map((c) => (
            <View key={c.id} style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', paddingVertical: 6 }}>
              <View style={{ flexShrink: 1, paddingRight: 8 }}>
                <Text style={{ color: t.text, fontWeight: '600' }}>{c.name}</Text>
                <Muted>{c.words}</Muted>
                <View style={{ flexDirection: 'row' }}>
                  <Btn label="Sound…" testID={`cat-sound-${c.id}`} onPress={() => void openSound(c.channel)} />
                </View>
              </View>
              <Switch
                {...switchColors(t)}
                accessibilityLabel={c.name}
                testID={`cat-${c.id}`}
                value={!muted.has(c.id)}
                disabled={!self}
                onValueChange={(on) => void setCategory(c.id, on)}
              />
            </View>
          ))}
          <Muted>{"Off: your hub does not send them to this phone. Sound and importance: your phone's settings for each channel."}</Muted>
        </Card>
      </Section>

      <Section title="Test">
        <Card>
          <Btn
            label="Send test notification"
            testID="link-test"
            disabled={api.data.offline || !target || testing}
            onPress={() => void sendTest()}
            onLongPress={() => void localTest()}
          />
          <Muted>{test || testing ? testText(test, testing) : 'The hub sends it over the link. Long press: a local one, no hub.'}</Muted>
        </Card>
      </Section>

      <Section title="Backup: Web Push">
        <Card>
          <Muted>{"Also get alerts in the installed web app (uses Google's push service, encrypted, no account)."}</Muted>
          <Btn label="Open the web app" testID="link-webpush" disabled={!origin} onPress={() => origin && void act('Could not open the web app', () => Linking.openURL(`${origin}/#/pair`))} />
        </Card>
      </Section>
    </Page>
  );
}
