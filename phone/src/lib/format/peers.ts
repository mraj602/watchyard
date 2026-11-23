// Text and matrix arithmetic of the Peers screen (docs/spec/peers.md 5, 10, 11; a port of
// webui/static/components/peers-fmt.js). The phone only narrows, so `narrowings` is the one
// edit helper it needs.
import type { MatrixEdit, PeerAuditEntry, PeerMatrix, PeerMessage, PeerSession, Seat, TrustedSender } from '../../contract/api';
import { agoLabel, plural } from './core';
import { stateInfo } from './state';

export type CellState = 'none' | 'out' | 'in' | 'both';

export interface PeerAccount {
  account: string;
  label: string;
}

/** The rows and columns: visible seats first, then accounts only the matrix names. */
export function peerAccounts(seats: Pick<Seat, 'account' | 'label' | 'email' | 'hidden'>[], matrix: Pick<PeerMatrix, 'accounts' | 'allow'>): PeerAccount[] {
  const out: PeerAccount[] = [];
  const seen = new Set<string>();
  const add = (account: string | undefined, label?: string | null) => {
    if (!account || seen.has(account)) return;
    seen.add(account);
    out.push({ account, label: label || matrix.accounts?.[account] || account.slice(0, 8) });
  };
  for (const s of seats) if (!s.hidden) add(s.account, s.label || s.email);
  for (const p of matrix.allow) {
    add(p[0]);
    add(p[1]);
  }
  return out;
}

export const hasPair = (allow: [string, string][], from: string, to: string): boolean => allow.some((p) => p[0] === from && p[1] === to);

/** What the cell (row `from`, column `to`) holds. */
export function cellState(allow: [string, string][], from: string, to: string): CellState {
  const out = hasPair(allow, from, to);
  const back = hasPair(allow, to, from);
  return out && back ? 'both' : out ? 'out' : back ? 'in' : 'none';
}

export const CELL_GLYPH: Record<CellState, string> = { none: '·', out: '→', in: '←', both: '⇄' };
export const CELL_WORD: Record<CellState, string> = { none: 'none', out: 'one way', in: 'one way, back', both: 'both ways' };

export function cellLabel(state: CellState, from: string, to: string): string {
  if (state === 'none') return `${from} and ${to}: no thread may start`;
  if (state === 'out') return `${from} may start a thread with ${to}`;
  if (state === 'in') return `${to} may start a thread with ${from}`;
  return `${from} and ${to} may start threads with each other`;
}

export interface Narrowing {
  target: CellState;
  edit: MatrixEdit;
}

/**
 * The ways a tapped cell can be narrowed: each target only removes pairs. Both ways can drop to
 * either one way or to none; one way drops to none; none has nothing to narrow. Turning the
 * other direction on is a widening and is left to the board (peers.md 5.4).
 */
export function narrowings(allow: [string, string][], from: string, to: string, version: number): Narrowing[] {
  const out = hasPair(allow, from, to);
  const back = hasPair(allow, to, from);
  const edit = (...deny: [string, string][]): MatrixEdit => ({ base_version: version, deny });
  const res: Narrowing[] = [];
  if (out && back) {
    res.push({ target: 'out', edit: edit([to, from]) });
    res.push({ target: 'in', edit: edit([from, to]) });
    res.push({ target: 'none', edit: edit([from, to], [to, from]) });
  } else if (out) {
    res.push({ target: 'none', edit: edit([from, to]) });
  } else if (back) {
    res.push({ target: 'none', edit: edit([to, from]) });
  }
  return res;
}

/** The board's confirm for Block all (peers.md 5.4). */
export const BLOCK_ALL_CONFIRM = 'Stop all peer messages between accounts? Threads in progress stop too.';
/** The loop guard's explanation (peers.md 6.3), the same sentence the board shows. */
export const TURNS_HELP =
  'When two sessions message each other on their own, with no one typing, Watchyard stops delivering after this many messages, so two agents cannot keep replying to each other forever. Typing in either session (or Resume thread) starts the count again.';
/** Shown under the turn limit (peers.md 5.4), the same sentence the board shows. */
export const NO_LIMIT_HELP = 'No limit lets agents keep talking with no one watching; the other limits still apply.';
/** max_turns 0 is "no limit" (peers.md 5.1), the default of a new matrix. */
export const TURNS_NONE = 0;
/** max_turns in words: "No limit" for 0, else the number. */
export const turnsLabel = (n: number): string => (n === TURNS_NONE ? 'No limit' : String(n));
export const ALLOW_FROM_COMPUTER = 'Allow pairs from a computer (board, Peers)';

/** Block all (peers.md 5.4): every allowed pair removed and same_account off. Only ever narrows. */
export function blockAllEdit(allow: [string, string][], sameAccount: boolean, version: number): MatrixEdit {
  const edit: MatrixEdit = { base_version: version };
  if (allow.length) edit.deny = allow.map((p): [string, string] => [p[0], p[1]]);
  if (sameAccount) edit.same_account = false;
  return edit;
}

/** True when Block all would change nothing. */
export const blockedAll = (allow: [string, string][], sameAccount: boolean): boolean => !sameAccount && allow.length === 0;

export function sizeLabel(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 1000) return `${n} B`;
  if (n < 1e6) return `${(n / 1000).toFixed(n < 1e4 ? 1 : 0)} KB`;
  return `${(n / 1e6).toFixed(1)} MB`;
}

const EVENTS: Record<string, string> = {
  matrix: 'Matrix changed',
  pause: 'All peers paused',
  resume: 'Peers resumed',
  loop_stop: 'Loop guard stopped a thread',
  loop_guard: 'Loop guard stopped a thread',
  thread_resume: 'Thread resumed',
  thread_resume_refused: 'A machine kept the stop: hub_may_widen is off',
  refused: 'Message refused',
  rejected: 'Message rejected by a machine',
  off: 'Peers turned off on a machine',
  on: 'Peers turned on on a machine',
  trust: 'Doorbell approved as a trusted sender on a machine',
  trust_off: 'Doorbell turned off as a trusted sender at a machine',
  trust_revoke: 'Doorbell revoked as a trusted sender',
};

/**
 * The label of an audit event; detail names the machine of a thread_resume_refused (a machine
 * that refused the owner's "Resume thread" because hub_may_widen is off, peers.md 6.3).
 */
export function eventLabel(e: string, detail?: Record<string, unknown> | null): string {
  const m = detail && typeof detail === 'object' ? detail.machine : undefined;
  if (e === 'thread_resume_refused' && typeof m === 'string' && m) return `${m.slice(0, 60)} kept the stop: hub_may_widen is off`;
  return EVENTS[e] ?? String(e || '').replace(/_/g, ' ');
}

/** Plain metadata of an audit entry's detail, never anything else the hub might send. */
export function detailText(d: Record<string, unknown> | null | undefined): string {
  if (!d || typeof d !== 'object') return '';
  const parts: string[] = [];
  const plain = (k: string) => d[k] !== undefined && d[k] !== null && typeof d[k] !== 'object';
  if (plain('version')) parts.push(`v${String(d.version)}`);
  if (d.widens === true) parts.push('widened');
  if (plain('paused')) parts.push(d.paused ? 'paused' : 'not paused');
  if (plain('same_account')) parts.push(`same account ${d.same_account ? 'on' : 'off'}`);
  if (plain('max_turns')) parts.push(d.max_turns === TURNS_NONE ? 'no turn limit' : `${String(d.max_turns)} turns`);
  if (plain('pairs')) parts.push(`${String(d.pairs)} ${Number(d.pairs) === 1 ? 'pair' : 'pairs'}`);
  if (plain('size') && Number.isFinite(Number(d.size))) parts.push(sizeLabel(Number(d.size)));
  for (const k of ['status', 'outcome', 'reason']) if (plain(k)) parts.push(String(d[k]).slice(0, 60));
  return parts.join(' · ');
}

/** who → whom when the detail names both accounts, else the actor. */
export function auditWho(e: Pick<PeerAuditEntry, 'actor' | 'detail'>, label: (id: string) => string = (x) => x): string {
  const d = e.detail ?? {};
  if (typeof d.from === 'string' && typeof d.to === 'string') return `${label(d.from)} → ${label(d.to)}`;
  return e.actor || '';
}

/** "claude-1 on laptop → claude-6 on server": the account, and the machine when the hub names it. */
export function messageWho(m: Pick<PeerMessage, 'from' | 'to'>, label: (id: string) => string = (x) => x): string {
  const side = (x: PeerMessage['from'] | undefined): string => {
    const a = x?.account;
    const d = x?.device_name;
    return (a ? label(a) : '?') + (d ? ` on ${d}` : '');
  };
  return `${side(m.from)} → ${side(m.to)}`;
}

const OUTCOME: Record<string, string> = {
  queued: 'queued', delivered: 'delivered', refused: 'refused', rejected: 'rejected',
  expired: 'expired', target_gone: 'target gone', failed: 'failed',
};
export const outcomeLabel = (s: string): string => OUTCOME[s] ?? String(s || '');

/** The board's confirm for Revoke (pwa.md 3.1). */
export const revokeConfirm = (machine: string): string =>
  `Stop Doorbell on ${machine} from sending to sessions? It can be approved again only from that machine's terminal.`;
/** Shown when no machine approved Doorbell (pwa.md 3.1). */
export const NO_TRUSTED =
  'Doorbell is not approved on any machine. To let it wake sessions by tag, run `wy peers trust doorbell` in a terminal on the machine it runs on (Linux).';
/** Shown below NO_TRUSTED (pwa.md 3.1). */
export const TRUSTED_UNREPORTED =
  'A machine whose agent has not reported its approval is not listed here; to revoke it, run `wy peers trust doorbell --off` on that machine.';
/** Shown on the row when Revoke answers 404 without a message of its own. */
export const REVOKE_NOT_FOUND =
  "The hub has no record of this machine's approval. Revoke it on that machine with `wy peers trust doorbell --off`.";
/** Shown when no session holds a tag and no trusted sender used one (pwa.md 3.1). */
export const NO_TAGS = 'No session has a tag. A session gives itself one with `wy peers tag add <tag>`.';
/** A tag only a trusted sender used, with no holder now. */
export const NO_HOLDER = 'No session holds it now.';

const machineOf = (x: { machine: string; machine_name?: string }): string => x.machine_name || x.machine;

export interface TrustedLines {
  /** "Doorbell on <machine>". */
  title: string;
  machine: string;
  revoked: boolean;
  /** The detail lines under the title, in order. */
  lines: string[];
}

/**
 * One trusted sender as the board words it (pwa.md 3.1): the program, who it sends as, when it
 * was approved and last sent, and how many it sent today; a revoked one says when and how to
 * approve it again instead.
 */
export function trustedSenderLines(s: TrustedSender, now: number): TrustedLines {
  const machine = machineOf(s);
  const account = s.account_label || s.account.slice(0, 8);
  const lines = [s.program, `sends as ${account}`];
  const revoked = !!s.revoked_at;
  if (revoked) {
    lines.push(`Revoked ${agoLabel(s.revoked_at, now)}. To approve it again, run \`wy peers trust doorbell\` in a terminal on that machine.`);
  } else {
    lines.push(`approved ${agoLabel(s.approved_at, now)}`.trim());
    lines.push(s.last_send_at ? `last sent ${agoLabel(s.last_send_at, now)}`.trim() : 'has not sent yet');
    lines.push(`${plural(s.sent_today, 'message')} today`);
    if (s.test_build) lines.push('approved by a test build');
  }
  return { title: `Doorbell on ${machine}`, machine, revoked, lines };
}

export interface TagHolder {
  /** The session id (`/sessions/<id>`). */
  id: string;
  name: string;
  /** Account, machine and state, as one line. */
  detail: string;
  /** What holders are ordered by: name, machine, session id (the board's order). */
  key: string;
}

export interface TagGroup {
  tag: string;
  holders: TagHolder[];
  /** "Sent to recently by Doorbell on <machine>", one per trusted sender that used the tag. */
  recent: string[];
  /** Only a trusted sender used it: nobody holds it now. */
  noHolder: boolean;
}

/**
 * Every tag a live session holds, sorted, with its holders; a tag only a trusted sender used in
 * the last 7 days is listed too (pwa.md 3.1).
 */
export function tagGroups(sessions: PeerSession[], senders: TrustedSender[]): TagGroup[] {
  const by = new Map<string, TagGroup>();
  const group = (tag: string): TagGroup => {
    let g = by.get(tag);
    if (!g) by.set(tag, (g = { tag, holders: [], recent: [], noHolder: true }));
    return g;
  };
  for (const p of sessions) {
    for (const tag of new Set(p.tags ?? [])) {
      const g = group(tag);
      g.noHolder = false;
      g.holders.push({
        id: p.session,
        key: `${p.name || ''}\u0000${machineOf(p)}\u0000${p.session}`,
        name: p.name || p.project || p.handle,
        detail: [p.account_label || p.account.slice(0, 8), machineOf(p), stateInfo({ state: p.state, detail: null, needs_you: false }).label].join(' · '),
      });
    }
  }
  for (const x of senders) {
    for (const tag of new Set(x.recent_tags.map((r) => r.tag))) {
      const line = `Sent to recently by Doorbell on ${machineOf(x)}`;
      const g = group(tag);
      if (!g.recent.includes(line)) g.recent.push(line);
    }
  }
  for (const g of by.values()) g.holders.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
  return [...by.values()].sort((a, b) => (a.tag < b.tag ? -1 : a.tag > b.tag ? 1 : 0));
}
