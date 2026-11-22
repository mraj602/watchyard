// Seen and Snooze, shared by Now, Session and the swipe gesture. Optimistic: the reducer is told
// first (the row dims), then the hub; a failure toasts and a refresh puts the truth back.
import { useCallback } from 'react';
import { View } from 'react-native';
import type { Session } from '../contract/api';
import { canSnoozeRow, seenTarget } from '../lib/format';
import { useHubApi } from './hub-api';
import { Btn } from './ui';

export const DEFAULT_SNOOZE_MIN = 15;

/** The parts of a session row that Seen and Snooze look at. */
export type ActRow = Pick<Session, 'id' | 'alert' | 'needs_you'>;
export type ActFn = (kind: 'seen' | 'snooze', s: ActRow) => void;

export function useAlertActions(toast: (msg: string) => void) {
  const api = useHubApi();
  const snoozeMin = api.snoozeMin ?? DEFAULT_SNOOZE_MIN;

  // Seen for every row seenTarget allows (a skipped alert, or none, on a Needs-you row too);
  // Snooze only for an alert that can still push (push.md 4). Needs the push scope.
  const canAct = useCallback(
    (s: ActRow): boolean => {
      if (!api.client) return false;
      if (api.me && !api.me.device.scopes.includes('push')) return false;
      return !!seenTarget(s) || canSnoozeRow(s);
    },
    [api.client, api.me],
  );

  const act = useCallback<ActFn>(
    async (kind, s) => {
      const client = api.client;
      if (!client || api.data.offline) return;
      const seen = kind === 'seen' ? seenTarget(s) : null;
      if (kind === 'seen' && !seen) return;
      if (kind === 'snooze' && (!s.alert || !canSnoozeRow(s))) return;
      if (seen && 'alert' in seen) api.dispatch({ type: 'seen', alert: seen.alert });
      else if (seen) api.dispatch({ type: 'seenSession', session: seen.session });
      else api.dispatch({ type: 'snoozed', alert: s.alert!.id, until: new Date(api.now + snoozeMin * 60e3).toISOString() });
      try {
        if (seen && 'alert' in seen) await client.seen(seen.alert);
        else if (seen) await client.seenSession(seen.session);
        else await client.snooze(s.alert!.id, snoozeMin);
      } catch (e) {
        const status = (e as { status?: number | null }).status;
        toast(status === 409 ? 'That alert already changed' : e instanceof Error ? e.message : 'Could not reach the hub');
      }
      try {
        await api.kick();
      } catch {
        // Offline is the poller's to show.
      }
    },
    [api, snoozeMin, toast],
  );

  return { act, canAct, snoozeMin };
}

export function AlertButtons({ session, act, canAct, snoozeMin }: {
  session: Session;
  act: ActFn;
  canAct: (s: Session) => boolean;
  snoozeMin: number;
}) {
  const offline = useHubApi().data.offline;
  if (!canAct(session)) return null;
  const canSeen = !!seenTarget(session);
  const canSnooze = canSnoozeRow(session);
  // Offline: the buttons stay but are disabled, as in the PWA (P5).
  return (
    <View style={{ flexDirection: 'row', gap: 8 }}>
      {canSeen ? <Btn label="Seen" testID={`seen-${session.id}`} disabled={offline} onPress={() => act('seen', session)} /> : null}
      {canSnooze ? <Btn label={`Snooze ${snoozeMin} min`} testID={`snooze-${session.id}`} disabled={offline} onPress={() => act('snooze', session)} /> : null}
    </View>
  );
}
