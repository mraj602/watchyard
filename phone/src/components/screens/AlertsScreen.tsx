// Alerts (PhoneAlerts): rule switches as this phone's prefs.mute, and the recent pushes.
import { useEffect, useState } from 'react';
import { Pressable, Switch, Text, View } from 'react-native';
import type { Alert, Rule } from '../../contract/api';
import { OUTCOME, RULE_GROUPS, clock, delayText, reasonText, recentAlerts } from '../../lib/format';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { switchColors, useTheme } from '../theme';
import { useSelf } from '../use-self';
import { Calm, Card, Muted, Page, Section, useToast } from '../ui';

export default function AlertsScreen() {
  const api = useHubApi();
  const t = useTheme();
  const toast = useToast();
  const { self, savePrefs } = useSelf(toast.show);
  const [rules, setRules] = useState<Rule[] | null>(null);
  const [alerts, setAlerts] = useState<Alert[] | null>(null);
  const client = api.client;
  const stamp = api.data.fetchedAt;

  useEffect(() => {
    if (!client) return;
    let live = true;
    client.rules().then((r) => live && setRules(r.rules), () => live && toast.show('Could not load the rules'));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client]);

  useEffect(() => {
    if (!client) return;
    let live = true;
    client.alerts({ limit: 50 }).then((r) => live && setAlerts(r.alerts), () => live && setAlerts(api.data.alerts));
    return () => {
      live = false;
    };
    // Refetch after each good refresh so a Seen shows here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, stamp]);

  const mute: string[] = (self?.prefs?.mute as string[] | undefined) ?? [];
  const offline = api.data.offline;
  const all = alerts ?? (api.data.alerts.length ? api.data.alerts : null);
  const list = all ? recentAlerts(all, api.now) : null;

  return (
    <Page back title="Alerts" meta="Switches choose what this phone gets; the hub sets the delays." toast={toast.node}>
      <Section title="Rules">
        {!rules ? (
          <Calm>Loading…</Calm>
        ) : (
          <Card>
            {RULE_GROUPS.map((g) => {
              const rs = rules.filter((r) => r.base && g.triggers.includes(r.trigger));
              if (!rs.length) return null;
              const ruleOn = rs.every((r) => r.enabled);
              const muted = g.triggers.some((tr) => mute.includes(tr));
              const delay = g.delay ? delayText(rs[0]?.delay_s) : '';
              const label = delay ? `${g.label} · ${delay}` : g.label;
              return (
                <View key={g.id} style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', paddingVertical: 6 }}>
                  <View style={{ flexShrink: 1, paddingRight: 8 }}>
                    <Text style={{ color: t.text }}>{label}</Text>
                    {!ruleOn ? <Muted>Off on the hub</Muted> : null}
                  </View>
                  <Switch
                    {...switchColors(t)}
                    accessibilityLabel={label}
                    testID={`rule-${g.id}`}
                    value={ruleOn && !muted}
                    disabled={!ruleOn || !self || offline}
                    onValueChange={(on) =>
                      void savePrefs({ mute: on ? mute.filter((x) => !g.triggers.includes(x)) : [...new Set([...mute, ...g.triggers])] })
                    }
                  />
                </View>
              );
            })}
          </Card>
        )}
      </Section>

      <Section title="Recent pushes">
        {!list ? (
          <Calm>Loading…</Calm>
        ) : list.length === 0 ? (
          <Calm>No alerts yet. They appear here when something needs you.</Calm>
        ) : (
          <Card>
            {list.map((a) => (
              <Pressable
                key={a.id}
                accessibilityRole="button"
                disabled={!a.session}
                onPress={() => a.session && go.session(a.session, a.id)}
                style={{ paddingVertical: 6 }}
              >
                <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
                  <Text style={{ color: t.text, flexShrink: 1 }}>{a.title || a.trigger}</Text>
                  <Text style={{ color: t.muted }}>{OUTCOME[a.status] ?? a.status}</Text>
                </View>
                <Muted>{`${clock(a.fired_at || a.created_at, api.now)}${a.reason ? ' · ' + reasonText(a.reason) : ''}`}</Muted>
              </Pressable>
            ))}
            <Muted>{'"Cancelled": you answered in the terminal before the delay ran out, so nothing was sent.'}</Muted>
          </Card>
        )}
      </Section>
    </Page>
  );
}
