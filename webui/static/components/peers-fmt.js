// The sentences and the matrix arithmetic of Settings → Peers (docs/spec/peers.md 5, 10, 11).
// Pure, so node can test it (peers-fmt.test.cjs): no imports.

// The accounts that make the rows and columns: the board's visible seats, then any account the
// matrix names that no seat does (a pair or a label from another time). `nameOf` gives a seat's
// short name (the board's seatName); by default its label or email.
export function peerAccounts(seats, matrix, nameOf = (s) => s.label || s.email) {
  const out = [];
  const seen = new Set();
  const add = (account, label) => {
    if (!account || seen.has(account)) return;
    seen.add(account);
    out.push({ account, label: label || (matrix && matrix.accounts && matrix.accounts[account]) || account.slice(0, 8) });
  };
  for (const s of seats || []) if (!s.hidden) add(s.account, nameOf(s));
  for (const p of (matrix && matrix.allow) || []) { add(p[0]); add(p[1]); }
  return out;
}

export const hasPair = (allow, from, to) => (allow || []).some((p) => p[0] === from && p[1] === to);

// What the cell (row from, column to) holds: none, out (from may start a thread with to),
// in (only to may start one with from) or both.
export function cellState(allow, from, to) {
  const out = hasPair(allow, from, to), back = hasPair(allow, to, from);
  return out && back ? 'both' : out ? 'out' : back ? 'in' : 'none';
}

export const CELL_GLYPH = { none: '—', out: '→', in: '←', both: '⇄' };

// The legend under the grid (PE3), one line each.
export const LEGEND = [
  ['⇄', 'both ways'], ['→', 'the row may start a thread with the column'], ['←', 'only the column may start one'],
  ['—', 'none (the default)'], ['=', 'same account'],
];
export const CELL_WORD = { none: 'none', out: 'one way', in: 'one way, back', both: 'both ways' };

export function cellLabel(state, from, to) {
  if (state === 'none') return `${from} and ${to}: no thread may start`;
  if (state === 'out') return `${from} may start a thread with ${to}`;
  if (state === 'in') return `${to} may start a thread with ${from}`;
  return `${from} and ${to} may start threads with each other`;
}

// The edit that turns the cell into `target`: allow the pairs it adds, deny the pairs it
// removes. widens is true when it adds a pair (keys.md 5.2): the hub signs that only for an
// owner session.
export function cellEdit(allow, from, to, target) {
  const add = [], del = [];
  const pair = (a, b, wanted) => {
    const has = hasPair(allow, a, b);
    if (wanted && !has) add.push([a, b]);
    if (!wanted && has) del.push([a, b]);
  };
  pair(from, to, target === 'out' || target === 'both');
  pair(to, from, target === 'in' || target === 'both');
  return { allow: add, deny: del, widens: add.length > 0 };
}

// Allow all (peers.md 5.4): every ordered pair of the given accounts, and same_account on. The
// edit adds only what is missing; widens is true when it adds anything.
export function allowAllEdit(allow, accounts, sameAccount) {
  const add = [];
  for (const a of accounts) for (const b of accounts) if (a !== b && !hasPair(allow, a, b)) add.push([a, b]);
  const edit = { allow: add, deny: [], widens: add.length > 0 };
  if (!sameAccount) { edit.same_account = true; edit.widens = true; }
  return edit;
}

// Block all: every pair in the matrix removed and same_account off. Always a narrowing.
export function blockAllEdit(allow, sameAccount) {
  const deny = (allow || []).map((p) => [p[0], p[1]]);
  const edit = { allow: [], deny, widens: false };
  if (sameAccount) edit.same_account = false;
  return edit;
}

export const allowedAll = (allow, accounts, sameAccount) => !!sameAccount && !allowAllEdit(allow, accounts, sameAccount).widens;
export const blockedAll = (allow, sameAccount) => !sameAccount && !(allow || []).length;

// The loop-guard limit (peers.md 5.1, 6.3): 0 is no limit (the default), else 2 to 500, the
// hub's own bound.
export const TURNS_NONE = 0;
export const TURNS_MIN = 2;
export const TURNS_MAX = 500;
// The number the field offers when the limit is off and someone unticks "No limit".
export const TURNS_SUGGEST = 30;
export const TURNS_HELP = 'When two sessions message each other on their own, with no one typing, Watchyard stops delivering after this many messages, so two agents cannot keep replying to each other forever. Typing in either session (or Resume thread) starts the count again.';
export const NO_LIMIT_HELP = 'No limit lets agents keep talking with no one watching; the other limits still apply.';

// max_turns in words: "no limit" for 0, else the number.
export const turnsLabel = (n) => (n === TURNS_NONE ? 'no limit' : String(n));
export const ALLOW_ALL_CONFIRM = 'Allow every account to message every other account, both ways?';
export const BLOCK_ALL_CONFIRM = 'Stop all peer messages between accounts? Threads in progress stop too.';
export const OWNER_ONLY_ALLOW = 'Only the owner can allow pairs.';

// The whole number in a text field, or null when it is not one inside the range.
export function parseTurns(text) {
  const t = String(text == null ? '' : text).trim();
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return n >= TURNS_MIN && n <= TURNS_MAX ? n : null;
}

// Raising the limit widens (owner only); lowering narrows. No limit (0) counts as the
// highest value: setting it widens, setting any number from it narrows (keys.md 5.2).
export function turnsWidens(current, next) {
  if (current === next || current === TURNS_NONE) return false;
  return next === TURNS_NONE || next > current;
}

// The body of PUT /v1/peers/matrix for an edit; base_version makes a stale edit a 409.
export function editBody(matrix, edit) {
  const b = { base_version: matrix.version };
  if (edit.allow && edit.allow.length) b.allow = edit.allow;
  if (edit.deny && edit.deny.length) b.deny = edit.deny;
  for (const k of ['paused', 'same_account', 'max_turns']) if (edit[k] !== undefined) b[k] = edit[k];
  return { edit: b };
}

// Why a change is not available to this caller, or '' when it is. Widening needs an owner
// session; narrowing needs one too, or a device with the push scope. `app`: the desktop app's
// Hub view, where owner sign-in is a button on the Hub tab.
export function whyNot({ widens, owner, push, offline, app }) {
  if (offline) return 'Offline: the matrix cannot change now.';
  if (widens && !owner) {
    return app ? 'Widening needs an owner sign-in. Use Sign in as owner on the Hub tab.'
      : 'Widening needs an owner sign-in. Sign in as owner: open an owner link from an owner device (More, Devices, Make another device owner).';
  }
  if (!owner && !push) {
    return app ? 'This sign-in can only read. Use Sign in as owner on the Hub tab to narrow.'
      : 'This sign-in can only read. Sign in as owner (an owner link from an owner device: More, Devices, Make another device owner), or use a paired phone, to narrow.';
  }
  return '';
}

// "12 B", "3.4 KB".
export function sizeLabel(n) {
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 1000) return `${n} B`;
  if (n < 1e6) return `${(n / 1000).toFixed(n < 1e4 ? 1 : 0)} KB`;
  return `${(n / 1e6).toFixed(1)} MB`;
}

const EVENTS = {
  matrix: 'Who may message whom changed',
  pause: 'All peers paused',
  resume: 'Peers resumed',
  loop_stop: 'Stopped a thread that went back and forth too often',
  loop_guard: 'Stopped a thread that went back and forth too often',
  thread_resume: 'Thread resumed',
  thread_resume_refused: 'A machine kept the stop: it does not let the hub reopen threads',
  refused: 'Message refused',
  rejected: 'Message rejected by a machine',
  off: 'Peers turned off on a machine',
  on: 'Peers turned on on a machine',
  trust: 'Doorbell approved as a trusted sender on a machine',
  trust_off: 'Doorbell turned off as a trusted sender at a machine',
  trust_revoke: 'Doorbell revoked as a trusted sender',
};

// The label of an audit event; detail names the machine of a thread_resume_refused (a machine
// that refused the owner's "Resume thread" because hub_may_widen is off, peers.md 6.3).
// The first matrix (version 1, made by the hub) is "Peers set up (defaults)" (PE5).
export function eventLabel(e, detail) {
  const machine = detail && typeof detail.machine === 'string' ? detail.machine.slice(0, 60) : '';
  if (e === 'thread_resume_refused' && machine) return `${machine} kept the stop: it does not let the hub reopen threads`;
  if (e === 'matrix' && isSetup(detail)) return 'Peers set up (defaults)';
  return EVENTS[e] || String(e || '').replace(/_/g, ' ');
}

export const isSetup = (detail) => !!detail && Number(detail.version) === 1;

const THREAD_ID = /^t_[0-9a-f]{16}$/;

// The audit ids (newest first list) of the loop-guard stops that "Resume thread" may lift
// (peers.md 6.3): for each thread, its newest `loop_guard` entry, unless a `thread_resume` of
// that thread came after it. Threads the list does not show resumed may have closed since;
// the hub then answers 409.
export function resumableStops(entries) {
  const out = new Set();
  const seen = new Set();
  for (const e of entries || []) {
    const t = e && e.thread;
    if (typeof t !== 'string' || !THREAD_ID.test(t) || seen.has(t)) continue;
    if (e.event === 'thread_resume') seen.add(t);
    else if (e.event === 'loop_guard') { seen.add(t); out.add(e.id); }
  }
  return out;
}

// What the board says after a resume (POST /v1/peers/threads/{id}/resume's answer).
export function resumeText(r) {
  const n = r && Number.isFinite(r.machines) ? r.machines : 0;
  const told = n === 1 ? '1 machine was told' : `${n} machines were told`;
  return `Thread resumed: ${told}. A machine that does not let the hub reopen threads (hub_may_widen off) keeps its stop until someone types in either session; the audit names each machine that refuses.`;
}

// The audit is metadata only. Only these detail keys are shown, in this order, and only when
// they are plain values; anything else the hub sends is left out.
export function detailText(d) {
  if (!d || typeof d !== 'object') return '';
  const parts = [];
  const plain = (k) => d[k] !== undefined && d[k] !== null && typeof d[k] !== 'object';
  if (plain('version')) parts.push(`v${d.version}`);
  if (d.widens === true) parts.push('widened');
  if (plain('paused')) parts.push(d.paused ? 'paused' : 'not paused');
  if (plain('same_account')) parts.push(`same account ${d.same_account ? 'on' : 'off'}`);
  if (plain('max_turns')) parts.push(d.max_turns === TURNS_NONE ? 'no turn limit' : `${d.max_turns} turns`);
  if (plain('pairs')) parts.push(`${d.pairs} ${Number(d.pairs) === 1 ? 'pair' : 'pairs'}`);
  if (plain('size') && Number.isFinite(Number(d.size))) parts.push(sizeLabel(Number(d.size)));
  for (const k of ['status', 'outcome', 'reason']) if (plain(k)) parts.push(String(d[k]).slice(0, 60));
  return parts.join(' · ');
}

// who → whom of an audit entry whose detail names both accounts, else the actor alone.
export function auditWho(e, label = (x) => x) {
  const d = e.detail || {};
  if (typeof d.from === 'string' && typeof d.to === 'string') return `${label(d.from)} → ${label(d.to)}`;
  return e.actor || '';
}

// One message of GET /v1/peers/messages: who → whom (by account).
// "claude-1 on laptop → claude-6 on gpu-box": the account, and the machine when the hub names it.
export function messageWho(m, label = (x) => x) {
  const side = (x) => {
    const a = (x && x.account) || '', d = (x && x.device_name) || '';
    return (a ? label(a) : '?') + (d ? ` on ${d}` : '');
  };
  return `${side(m.from)} → ${side(m.to)}`;
}

const OUTCOME = {
  queued: 'queued', delivered: 'delivered', refused: 'refused', rejected: 'rejected',
  expired: 'expired', target_gone: 'target gone', failed: 'failed',
};
export const outcomeLabel = (s) => OUTCOME[s] || String(s || '');

// ---- trusted senders and tags (peers.md 3.4, 4.1a; pwa.md 3.1) ------------------------------

export const TRUSTED_EMPTY = 'Doorbell is not approved on any machine. To let it wake sessions by tag, run `wy peers trust doorbell` in a terminal on the machine it runs on (Linux).';
export const NO_TAGS = 'No session has a tag. A session gives itself one with `wy peers tag add <tag>`.';
export const NO_HOLDER = 'No session holds it now.';
export const TRUSTED_INTRO = 'A trusted sender may message sessions by tag without a pair in the matrix. Metadata only: never the text.';
export const TAGS_INTRO = 'The tags sessions gave themselves, and who sent to them recently.';

export const revokeConfirm = (machine) => `Stop Doorbell on ${machine} from sending to sessions? It can be approved again only from that machine's terminal.`;

// The machine's name, else its id.
export const senderMachine = (t) => (t && (t.machine_name || t.machine)) || '';

export const trustedTitle = (t) => `Doorbell on ${senderMachine(t)}`;

// "sends as work", "approved 3 h ago", "last sent 5 min ago" (or "has not sent yet") and
// "4 messages today". `account` names the account (the board's seat name), `age` turns a
// time into "3 h ago".
export function trustedFacts(t, account, age) {
  const n = Number.isFinite(t.sent_today) ? t.sent_today : 0;
  return [
    `sends as ${account}`,
    `approved ${age(t.approved_at)}`,
    t.last_send_at ? `last sent ${age(t.last_send_at)}` : 'has not sent yet',
    `${n} ${n === 1 ? 'message' : 'messages'} today`,
  ];
}

// A revoked sender says when, and how to approve it again.
export const revokedText = (t, age) => `Revoked ${age(t.revoked_at)}. To approve it again, run \`wy peers trust doorbell\` in a terminal on that machine.`;

export const isRevoked = (t) => !!(t && t.revoked_at);

// The body of POST /v1/peers/trusted/revoke.
export const revokeBody = (t) => ({ machine: t.machine, sender: 'doorbell' });

// Every tag a live session holds, sorted, with its holders (GET /v1/peers) and the trusted
// senders that sent to it in the last 7 days (TrustedSender.recent_tags); a tag only a trusted
// sender used is listed with no holder.
export function tagIndex(peers, trusted) {
  const byTag = new Map();
  const entry = (tag) => {
    if (!byTag.has(tag)) byTag.set(tag, { tag, holders: [], senders: [] });
    return byTag.get(tag);
  };
  for (const p of peers || []) for (const tag of (p && p.tags) || []) entry(tag).holders.push(p);
  for (const t of trusted || []) {
    const seen = new Set();
    for (const r of t.recent_tags || []) {
      if (!r || typeof r.tag !== 'string' || seen.has(r.tag)) continue;
      seen.add(r.tag);
      entry(r.tag).senders.push({ machine: t.machine, name: senderMachine(t), at: r.at });
    }
  }
  const text = (p) => `${p.name || ''}\u0000${p.machine_name || p.machine || ''}\u0000${p.session || ''}`;
  const out = [...byTag.values()].sort((a, b) => (a.tag < b.tag ? -1 : a.tag > b.tag ? 1 : 0));
  for (const e of out) e.holders.sort((a, b) => (text(a) < text(b) ? -1 : text(a) > text(b) ? 1 : 0));
  return out;
}

export const sentRecently = (s) => `Sent to recently by Doorbell on ${s.name}`;
