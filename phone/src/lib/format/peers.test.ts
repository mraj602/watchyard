import type { PeerSession, TrustedSender } from '../../contract/api';
import { blockAllEdit, blockedAll, detailText, eventLabel, revokeConfirm, tagGroups, trustedSenderLines, turnsLabel, NO_LIMIT_HELP, NO_TAGS, NO_TRUSTED } from './peers';

describe('Block all (the phone only narrows)', () => {
  it('denies every pair and turns same account off', () => {
    expect(blockAllEdit([['a', 'b'], ['b', 'a'], ['a', 'c']], true, 7)).toEqual({
      base_version: 7,
      deny: [['a', 'b'], ['b', 'a'], ['a', 'c']],
      same_account: false,
    });
  });

  it('leaves same account alone when it is already off, and omits an empty deny list', () => {
    expect(blockAllEdit([['a', 'b']], false, 2)).toEqual({ base_version: 2, deny: [['a', 'b']] });
    expect(blockAllEdit([], true, 2)).toEqual({ base_version: 2, same_account: false });
  });

  it('knows when there is nothing left to block', () => {
    expect(blockedAll([], false)).toBe(true);
    expect(blockedAll([], true)).toBe(false);
    expect(blockedAll([['a', 'b']], false)).toBe(false);
  });
});

describe('the turn limit (read-only on the phone)', () => {
  it('shows 0 as No limit and a number as itself', () => {
    expect(turnsLabel(0)).toBe('No limit');
    expect(turnsLabel(30)).toBe('30');
  });

  it('says no turn limit in the audit detail', () => {
    expect(detailText({ max_turns: 0 })).toBe('no turn limit');
    expect(detailText({ max_turns: 40 })).toBe('40 turns');
  });

  it('explains no limit with the sentence the board shows', () => {
    expect(NO_LIMIT_HELP).toBe('No limit lets agents keep talking with no one watching; the other limits still apply.');
  });
});

const NOW = Date.parse('2026-10-13T12:00:00.000Z');
const ago = (min: number) => new Date(NOW - min * 60e3).toISOString();
const sender = (over: Partial<TrustedSender> = {}): TrustedSender => ({
  sender: 'doorbell', machine: 'm-laptop-0001', machine_name: 'laptop', account: 'acc-1111-2222', account_label: 'personal',
  program: '/home/me/.venv/bin/doorbell', approved_at: ago(180), revoked_at: null, last_send_at: ago(5), sent_today: 3, recent_tags: [], ...over,
});

describe('trustedSenderLines', () => {
  it('words a live sender as the board does', () => {
    expect(trustedSenderLines(sender(), NOW)).toEqual({
      title: 'Doorbell on laptop',
      machine: 'laptop',
      revoked: false,
      lines: ['/home/me/.venv/bin/doorbell', 'sends as personal', 'approved 3 h ago', 'last sent 5 min ago', '3 messages today'],
    });
  });

  it('says it has not sent yet, and counts one message in the singular', () => {
    const l = trustedSenderLines(sender({ last_send_at: null, sent_today: 1 }), NOW).lines;
    expect(l).toContain('has not sent yet');
    expect(l).toContain('1 message today');
    expect(trustedSenderLines(sender({ sent_today: 0 }), NOW).lines).toContain('0 messages today');
  });

  it('a revoked one says when, and how to approve it again', () => {
    const l = trustedSenderLines(sender({ revoked_at: ago(10), revoked_by: 'dev_phone' }), NOW);
    expect(l.revoked).toBe(true);
    expect(l.lines).toEqual([
      '/home/me/.venv/bin/doorbell',
      'sends as personal',
      'Revoked 10 min ago. To approve it again, run `wy peers trust doorbell` in a terminal on that machine.',
    ]);
  });

  it('falls back to short ids without names', () => {
    const l = trustedSenderLines(sender({ machine_name: undefined, account_label: null }), NOW);
    expect(l.title).toBe('Doorbell on m-laptop-0001');
    expect(l.lines[1]).toBe('sends as acc-1111');
  });
});

describe('the Trusted senders and Tags words', () => {
  it('match the board', () => {
    expect(revokeConfirm('laptop')).toBe("Stop Doorbell on laptop from sending to sessions? It can be approved again only from that machine's terminal.");
    expect(NO_TRUSTED).toBe('Doorbell is not approved on any machine. To let it wake sessions by tag, run `wy peers trust doorbell` in a terminal on the machine it runs on (Linux).');
    expect(NO_TAGS).toBe('No session has a tag. A session gives itself one with `wy peers tag add <tag>`.');
  });
});

describe('tagGroups', () => {
  const ps = (id: string, name: string, tags: string[] | undefined, over: Partial<PeerSession> = {}): PeerSession => ({
    session: id, handle: `h-${id}`, name, account: 'acc-1111-2222', account_label: 'personal', machine: 'm-laptop-0001', machine_name: 'laptop', state: 'working', tags, ...over,
  });

  it('is empty with no tags', () => {
    expect(tagGroups([ps('s1', 'a', []), ps('s2', 'b', undefined)], [])).toEqual([]);
  });

  it('lists every held tag, sorted, with its holders', () => {
    const g = tagGroups([ps('s2', 'beta', ['web', 'api']), ps('s1', 'alpha', ['api'], { state: 'done', machine_name: undefined, machine: 'm-server-0002' })], []);
    expect(g.map((x) => x.tag)).toEqual(['api', 'web']);
    expect(g[0]!.holders.map(({ key: _k, ...h }) => h)).toEqual([
      { id: 's1', name: 'alpha', detail: 'personal · m-server-0002 · Done' },
      { id: 's2', name: 'beta', detail: 'personal · laptop · Working' },
    ]);
    expect(g[0]!.noHolder).toBe(false);
    expect(g[1]!.holders.map((h) => h.id)).toEqual(['s2']);
  });

  it('adds the trusted senders that sent to a tag, and lists a tag only they used', () => {
    const a = sender({ recent_tags: [{ tag: 'api', at: ago(5) }, { tag: 'api', at: ago(50) }, { tag: 'ops', at: ago(60) }] });
    const g = tagGroups([ps('s1', 'alpha', ['api'])], [a, sender({ machine: 'm2', machine_name: 'server', recent_tags: [{ tag: 'api', at: ago(9) }] })]);
    expect(g.map((x) => x.tag)).toEqual(['api', 'ops']);
    expect(g[0]!.recent).toEqual(['Sent to recently by Doorbell on laptop', 'Sent to recently by Doorbell on server']);
    expect(g[1]).toEqual({ tag: 'ops', holders: [], recent: ['Sent to recently by Doorbell on laptop'], noHolder: true });
  });
});

describe('trust audit events', () => {
  it('are worded as the board words them', () => {
    expect(eventLabel('trust')).toBe('Doorbell approved as a trusted sender on a machine');
    expect(eventLabel('trust_off')).toBe('Doorbell turned off as a trusted sender at a machine');
    expect(eventLabel('trust_revoke')).toBe('Doorbell revoked as a trusted sender');
  });
});
