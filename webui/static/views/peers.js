// Settings → Peers (peers.md 5, 6.3, 10, 11): the account matrix (rows send, columns receive),
// the matrix version, Pause all peers, and the audit list (metadata only, never text) with
// "Resume thread" on loop-guard stops for an owner session.
// Narrowing works from any sign-in that can write; widening needs the owner session, and the
// hub signs it only then. A control that is not available says why.

import { h } from '../components/dom.js';
import { section, toggle, toast, moreTabs, details, ownerHintNode } from '../components/ui.js';
import { seatIndex, seatName } from '../components/seat.js';
import { stateTag } from '../components/state.js';
import { store, isOwner, canPush, inApp } from '../store.js';
import { getMatrix, editMatrix, pausePeers, getPeerAudit, getPeerMessages, resumePeerThread, getPeers, getTrustedSenders, revokeTrustedSender } from '../api.js';
import { agoLabel, dateTime } from '../format.js';
import {
  peerAccounts, allowAllEdit, blockAllEdit, allowedAll, blockedAll, parseTurns, turnsWidens,
  TURNS_NONE, TURNS_MIN, TURNS_MAX, TURNS_SUGGEST, TURNS_HELP, NO_LIMIT_HELP, turnsLabel, ALLOW_ALL_CONFIRM, BLOCK_ALL_CONFIRM, OWNER_ONLY_ALLOW, cellState, cellLabel, cellEdit, editBody, whyNot, detailText, eventLabel, auditWho,
  messageWho, outcomeLabel, sizeLabel, resumableStops, resumeText, isSetup, CELL_GLYPH, CELL_WORD, LEGEND,
  TRUSTED_EMPTY, NO_TAGS, NO_HOLDER, TRUSTED_INTRO, TAGS_INTRO, revokeConfirm, senderMachine, trustedTitle, trustedFacts, revokedText,
  isRevoked, revokeBody, tagIndex, sentRecently,
} from '../components/peers-fmt.js';

const st = { matrix: null, audit: null, messages: null, peers: null, trusted: null, error: null, pick: null, busy: false, turns: null, noLimit: null };
const again = () => window.dispatchEvent(new Event('wy-rerender'));

export async function load() {
  st.error = null;
  st.pick = null;
  st.turns = null;
  st.noLimit = null;
  const [m, a, msg, pr, tr] = await Promise.allSettled([getMatrix(), getPeerAudit({ limit: '50' }), getPeerMessages({ limit: '50' }), getPeers(), getTrustedSenders()]);
  if (m.status === 'fulfilled') st.matrix = m.value;
  else if (m.reason && m.reason.name !== 'AuthError') st.error = m.reason.message;
  if (a.status === 'fulfilled') st.audit = a.value.entries || [];
  // The messages list is optional: an older hub answers 404 or 501, a read-only sign-in 403.
  st.messages = msg.status === 'fulfilled' ? msg.value.messages || [] : null;
  // The directory and the trusted senders are optional too: a hub without the trusted endpoint
  // answers 404, and the Trusted senders section is then hidden (pwa.md 3.1).
  st.peers = pr.status === 'fulfilled' ? pr.value.sessions || [] : null;
  if (tr.status === 'fulfilled') st.trusted = tr.value.senders || [];
  else if (tr.reason && tr.reason.status === 404) st.trusted = null;
  again();
}

async function refreshAudit() {
  try { st.audit = (await getPeerAudit({ limit: '50' })).entries || []; } catch (_) { /* keep the old list */ }
}

// Sends an edit (or the pause switch); a stale version (409) reloads the matrix.
async function change(widens, send) {
  const why = whyNot({ widens, owner: isOwner(), push: canPush(), offline: !!store.offline, app: inApp() });
  if (why) { toast(why); return; }
  st.busy = true;
  again();
  try {
    st.matrix = await send();
    st.pick = null;
    st.turns = null;
  st.noLimit = null;
    await refreshAudit();
  } catch (ex) {
    if (ex.status === 409) { try { st.matrix = await getMatrix(); } catch (_) { /* shown below */ } toast('The matrix changed meanwhile. Check it and try again.'); }
    else toast(ex.message || 'Could not change the matrix');
  }
  st.busy = false;
  again();
}

const edit = (patch, widens) => change(widens, () => editMatrix(editBody(st.matrix.matrix, patch)));

// ---- Pause all peers ----------------------------------------------------------------------

function killSwitch() {
  const m = st.matrix.matrix;
  const owner = isOwner();
  const off = whyNot({ widens: m.paused, owner, push: canPush(), offline: !!store.offline, app: inApp() });
  // While nothing can pass (no pair allowed, no message yet) the switch is a small control (PE4).
  const quiet = !m.paused && !(m.allow || []).length && !(st.messages && st.messages.length);
  return h('section', { class: `block kill${m.paused ? ' kill-on' : ''}`, 'aria-label': 'Pause all peers' },
    h('div', { class: 'panel pad-s col' },
      h('p', { class: m.paused ? 'strong status-warn' : 'strong status-ok', role: 'status', 'data-key': 'peers-state' },
        m.paused ? 'All peers are paused' : 'Peers are on'),
      h('p', { class: 'muted small' }, m.paused
        ? 'No message passes between sessions on any machine until the owner resumes.'
        : 'Pausing stops every machine at once: new messages are refused and queued ones dropped.'),
      h('button', {
        type: 'button', class: m.paused ? 'btn' : quiet ? 'btn btn-sm' : 'btn btn-danger btn-big', 'data-key': 'peers-pause',
        disabled: st.busy || !!off, title: off || undefined,
        onclick: () => change(m.paused, () => pausePeers(!m.paused)),
      }, m.paused ? 'Resume peers' : 'Pause all peers'),
      off ? h('p', { class: 'muted small', 'data-key': 'peers-pause-why' }, off) : null));
}

// Seat names as the rest of the board shows them, told apart when they collide (PE2, G7).
function accounts(seats, m) {
  seatIndex(seats);
  return peerAccounts(seats, m, seatName);
}

// ---- the grid -----------------------------------------------------------------------------

function grid(accts, m) {
  const pick = st.pick;
  const head = h('tr', {}, h('th', { scope: 'col', class: 'mx-corner' }, h('span', { class: 'sr-only' }, 'From, to'), h('span', { 'aria-hidden': 'true' }, 'from \\ to')),
    accts.map((a) => h('th', { scope: 'col', class: 'mx-col', title: a.label }, h('span', { class: 'mx-name' }, a.label))));
  const rows = accts.map((r) => h('tr', {}, h('th', { scope: 'row', class: 'mx-row', title: r.label }, h('span', { class: 'mx-name' }, r.label)),
    accts.map((c) => {
      if (r.account === c.account) {
        return h('td', { class: 'mx-cell mx-self', title: `Same account: ${m.same_account ? 'on' : 'off'}` },
          h('span', { 'aria-label': `${r.label} to itself: same account ${m.same_account ? 'on' : 'off'}` }, '='));
      }
      const state = cellState(m.allow, r.account, c.account);
      const on = pick && pick.from === r.account && pick.to === c.account;
      return h('td', { class: 'mx-cell' }, h('button', {
        type: 'button', class: `mx-btn mx-${state}`, 'aria-pressed': String(!!on), 'data-key': `cell-${r.account}-${c.account}`,
        'aria-label': cellLabel(state, r.label, c.label), title: CELL_WORD[state],
        onclick: () => { st.pick = on ? null : { from: r.account, to: c.account }; again(); },
      }, CELL_GLYPH[state]));
    })));
  const wrap = h('div', { class: 'mx-wrap', tabindex: '0', role: 'region', 'aria-label': 'Account matrix, scrolls sideways' }, h('table', { class: 'mx', 'data-key': 'matrix' }, h('thead', {}, head), h('tbody', {}, rows)));
  const hint = h('p', { class: 'muted small mx-hint', hidden: true, 'data-key': 'mx-hint' }, 'Scroll sideways to see every account →');
  // Show the hint only when the table is wider than its card (R5).
  requestAnimationFrame(() => { hint.hidden = wrap.scrollWidth <= wrap.clientWidth + 1; });
  return h('div', { class: 'mx-box' }, wrap, hint);
}

function editor(accts, m) {
  const p = st.pick;
  if (!p) return h('p', { class: 'muted small' }, 'Pick a cell to change it. A row starts threads with a column.');
  const from = accts.find((a) => a.account === p.from), to = accts.find((a) => a.account === p.to);
  if (!from || !to) return null;
  const cur = cellState(m.allow, p.from, p.to);
  const owner = isOwner(), push = canPush(), offline = !!store.offline;
  const opts = [['none', 'None'], ['out', `${from.label} → ${to.label}`], ['in', `${to.label} → ${from.label}`], ['both', 'Both ways']];
  const whys = [];
  const btns = opts.map(([target, text]) => {
    const e = cellEdit(m.allow, p.from, p.to, target);
    const why = target === cur ? '' : whyNot({ widens: e.widens, owner, push, offline, app: inApp() });
    if (why && !whys.includes(why)) whys.push(why);
    return h('button', {
      type: 'button', class: `btn btn-sm${target === cur ? ' btn-primary' : ''}`, 'data-key': `set-${target}`,
      'aria-pressed': String(target === cur), disabled: st.busy || target === cur || !!why, title: why || undefined,
      onclick: () => edit(e, e.widens),
    }, text);
  });
  return h('div', { class: 'col mx-edit', 'data-key': 'cell-editor' },
    h('p', { class: 'strong' }, `${from.label} and ${to.label}`),
    h('div', { class: 'actions' }, btns),
    whys.map((w) => h('p', { class: 'muted small', 'data-key': 'cell-why' }, w)));
}

// ---- bulk controls and the turn limit ------------------------------------------------------

// Allow all widens (owner session only); Block all narrows (any paired device). Both go through
// the same matrix edit as a single cell.
function bulkBlock(accts, m) {
  const owner = isOwner(), offline = !!store.offline;
  const ids = accts.map((a) => a.account);
  const allowOff = !owner ? OWNER_ONLY_ALLOW : whyNot({ widens: true, owner, push: canPush(), offline, app: inApp() });
  const blockOff = whyNot({ widens: false, owner, push: canPush(), offline, app: inApp() });
  const allDone = allowedAll(m.allow, ids, m.same_account), noneLeft = blockedAll(m.allow, m.same_account);
  return h('div', { class: 'col', 'data-key': 'peers-bulk' },
    h('div', { class: 'actions' },
      owner ? h('button', {
        type: 'button', class: 'btn btn-sm', 'data-key': 'allow-all', disabled: st.busy || !!allowOff || allDone,
        title: allowOff || undefined,
        onclick: () => { if (confirm(ALLOW_ALL_CONFIRM)) edit(allowAllEdit(m.allow, ids, m.same_account), true); },
      }, 'Allow all') : null,
      h('button', {
        type: 'button', class: 'btn btn-sm btn-danger', 'data-key': 'block-all', disabled: st.busy || !!blockOff || noneLeft,
        title: blockOff || undefined,
        onclick: () => { if (confirm(BLOCK_ALL_CONFIRM)) edit(blockAllEdit(m.allow, m.same_account), false); },
      }, 'Block all')),
    !owner ? h('p', { class: 'muted small', 'data-key': 'allow-all-why' }, OWNER_ONLY_ALLOW) : null,
    blockOff ? h('p', { class: 'muted small', 'data-key': 'block-all-why' }, blockOff) : null);
}

// "Replies between two sessions without you" (peers.md 5.1, 6.3): a number, or "No limit" (0,
// the default). Raising widens (owner only), lowering narrows; no limit is the highest value.
function turnsBlock(m) {
  const owner = isOwner(), offline = !!store.offline;
  const cur = m.max_turns;
  const noLimit = st.noLimit === null ? cur === TURNS_NONE : st.noLimit;
  const draft = st.turns === null ? String(cur === TURNS_NONE ? TURNS_SUGGEST : cur) : st.turns;
  const nextOf = (none, text) => (none ? TURNS_NONE : parseTurns(text));
  const whyFor = (n) => (n === null || n === cur ? ''
    : whyNot({ widens: turnsWidens(cur, n), owner, push: canPush(), offline, app: inApp() }));
  const next = nextOf(noLimit, draft);
  const why = whyFor(next);
  const input = h('input', {
    type: 'number', inputmode: 'numeric', min: String(TURNS_MIN), max: String(TURNS_MAX), step: '1', value: draft,
    'data-key': 'max-turns', 'aria-label': 'Replies between two sessions without you', 'aria-describedby': 'max-turns-help',
    disabled: st.busy || noLimit ? true : undefined,
  });
  const none = h('input', {
    type: 'checkbox', 'data-key': 'max-turns-none', checked: noLimit ? true : undefined, disabled: st.busy ? true : undefined,
    'aria-describedby': 'max-turns-none-help',
  });
  const apply = h('button', {
    type: 'button', class: 'btn btn-sm', 'data-key': 'max-turns-set', disabled: st.busy || next === null || next === cur || !!why,
    title: why || undefined,
    onclick: () => { const n = nextOf(none.checked, input.value); if (n !== null) edit({ max_turns: n }, turnsWidens(cur, n)); },
  }, 'Set');
  // Typing updates the button in place (a re-render would drop the focus); the reasons refresh on change.
  input.addEventListener('input', () => {
    st.turns = input.value;
    const n = nextOf(none.checked, input.value);
    apply.disabled = st.busy || n === null || n === cur || !!whyFor(n);
  });
  input.addEventListener('change', () => { st.turns = input.value; again(); });
  none.addEventListener('change', () => { st.noLimit = none.checked; st.turns = input.value; again(); });
  return h('div', { class: 'col', 'data-key': 'peers-turns' },
    h('div', { class: 'actions' },
      h('label', { class: 'actions' }, h('span', { class: 'strong' }, 'Replies between two sessions without you:'), input),
      h('label', { class: 'actions' }, none, h('span', {}, 'No limit')),
      apply),
    h('p', { class: 'muted small', id: 'max-turns-help' }, TURNS_HELP),
    h('p', { class: 'muted small', id: 'max-turns-none-help' }, NO_LIMIT_HELP),
    next === null ? h('p', { class: 'muted small', 'data-key': 'max-turns-why' }, `Use a whole number from ${TURNS_MIN} to ${TURNS_MAX}, or tick No limit.`) : null,
    why ? h('p', { class: 'muted small', 'data-key': 'max-turns-why' }, why) : null);
}

function matrixBlock() {
  const info = st.matrix;
  const m = info.matrix;
  const seats = (store.overview && store.overview.seats) || [];
  const accts = accounts(seats, m);
  const owner = isOwner();
  const sameOff = whyNot({ widens: !m.same_account, owner, push: canPush(), offline: !!store.offline, app: inApp() });
  return section('Accounts',
    h('div', { class: 'panel pad-s col' },
      accts.length < 2
        ? h('p', { class: 'muted' }, 'Peers between accounts need at least two accounts on this hub.')
        : [bulkBlock(accts, m), grid(accts, m), editor(accts, m)],
      h('ul', { class: 'mx-legend muted small', 'aria-label': 'Legend' }, LEGEND.map(([g, t]) => h('li', {}, h('span', { class: 'mx-glyph', 'aria-hidden': 'true' }, g), ` ${t}`))),
      h('p', { class: 'muted small' }, 'Replies inside a thread are always allowed.'),
      toggle({
        label: 'Same account', sub: 'Sessions of one account may message each other.', checked: m.same_account,
        disabled: st.busy || !!sameOff, key: 'same-account',
        onChange: (v) => edit({ same_account: v }, v),
      }),
      sameOff ? h('p', { class: 'muted small' }, sameOff) : null,
      turnsBlock(m),
      details({ text: 'Details', key: 'peers-details' },
        h('p', { class: 'muted small', 'data-key': 'matrix-version' },
          `Version ${m.version} · signed by ${info.signer}${info.created_at ? ` · ${agoLabel(info.created_at, Date.now())}` : ''} · ${m.max_turns === TURNS_NONE ? 'no turn limit' : `at most ${turnsLabel(m.max_turns)} turns per thread`}`))));
}

// ---- audit --------------------------------------------------------------------------------

// "Resume thread" (peers.md 6.3): owner session only; the hub resets its loop guard and tells
// both machines, which take it only with hub_may_widen on; one that refuses reports it, and
// its thread_resume_refused entry shows in the audit.
async function resumeThread(thread) {
  st.busy = true;
  again();
  try {
    toast(resumeText(await resumePeerThread(thread)));
    await refreshAudit();
  } catch (ex) {
    toast(ex.status === 409 ? 'That thread has closed; a new message starts a new one.' : ex.message || 'Could not resume the thread');
  }
  st.busy = false;
  again();
}

function auditBlock(label) {
  const list = st.audit;
  const rows = [];
  if (list) {
    const owner = isOwner(), offline = !!store.offline;
    const stops = resumableStops(list);
    for (const e of list) {
      const resume = stops.has(e.id) && owner
        ? h('button', {
          type: 'button', class: 'btn btn-sm', 'data-key': `resume-${e.thread}`, disabled: st.busy || offline,
          title: offline ? 'Offline: the thread cannot be resumed now.' : undefined,
          onclick: () => resumeThread(e.thread),
        }, 'Resume thread')
        : null;
      rows.push(h('li', { class: 'row audit-row', 'data-key': `audit-${e.id}` },
        h('span', { class: 'row-text' },
          h('span', {}, eventLabel(e.event, e.detail)),
          h('span', { class: 'muted small' }, [auditWho(e, label), e.event === 'matrix' && isSetup(e.detail) ? '' : detailText(e.detail)].filter(Boolean).join(' · '))),
        resume,
        h('time', { class: 'muted small', datetime: e.at, title: dateTime(e.at) }, agoLabel(e.at, Date.now()))));
    }
  }
  return section('Audit',
    h('p', { class: 'muted small' }, 'Who changed what, and when. Metadata only: no message text is ever stored or shown.'),
    !list ? h('p', { class: 'muted' }, 'Loading…') : !rows.length ? h('p', { class: 'muted' }, 'Nothing yet.') : h('ul', { class: 'panel audit-list' }, rows));
}

function messagesBlock(label) {
  const list = st.messages;
  if (!list) return null;
  return section('Messages',
    h('p', { class: 'muted small' }, 'Who messaged whom: when, size and outcome. Never the text.'),
    !list.length ? h('p', { class: 'muted' }, 'No messages yet.') : h('ul', { class: 'panel audit-list' }, list.map((x) =>
      h('li', { class: 'row audit-row', 'data-key': `msg-${x.id}` },
        h('span', { class: 'row-text' },
          h('span', {}, messageWho(x, label)),
          h('span', { class: 'muted small' }, [x.kind, sizeLabel(x.size), outcomeLabel(x.status)].filter(Boolean).join(' · '))),
        h('time', { class: 'muted small', datetime: x.created_at, title: dateTime(x.created_at) }, agoLabel(x.created_at, Date.now()))))));
}

// ---- trusted senders and tags (peers.md 4.1a) ------------------------------------------------

// Text with `code` parts.
const withCode = (text) => text.split('`').map((x, i) => (i % 2 ? h('code', { class: 'mono' }, x) : x));

// Revoke (peers.md 4.1a): owner session only; the hub keeps the entry, marked revoked.
async function revokeSender(t) {
  if (!confirm(revokeConfirm(senderMachine(t)))) return;
  st.busy = true;
  again();
  try {
    const next = await revokeTrustedSender(revokeBody(t));
    st.trusted = (st.trusted || []).map((x) => (x.machine === next.machine ? next : x));
  } catch (ex) {
    toast(ex.message || 'Could not revoke Doorbell');
  }
  st.busy = false;
  again();
}

function trustedBlock(account) {
  const list = st.trusted;
  if (!list) return null;
  const owner = isOwner(), offline = !!store.offline;
  const age = (ts) => agoLabel(ts, Date.now());
  return section('Trusted senders',
    h('p', { class: 'muted small' }, TRUSTED_INTRO),
    !list.length ? h('p', { class: 'muted', 'data-key': 'trusted-empty' }, withCode(TRUSTED_EMPTY)) : h('ul', { class: 'panel audit-list' }, list.map((t) => {
      const revoked = isRevoked(t);
      return h('li', { class: 'row audit-row', 'data-key': `trusted-${t.machine}` },
        h('span', { class: 'row-text' },
          h('span', { class: 'strong' }, trustedTitle(t)),
          h('span', { class: 'muted small mono' }, t.program),
          h('span', { class: 'muted small' }, trustedFacts(t, account(t), age).join(' · ')),
          revoked ? h('span', { class: 'muted small', 'data-key': `trusted-revoked-${t.machine}` }, withCode(revokedText(t, age))) : null),
        owner && !revoked ? h('button', {
          type: 'button', class: 'btn btn-sm btn-danger', 'data-key': `revoke-${t.machine}`, disabled: st.busy || offline,
          title: offline ? 'Offline: Doorbell cannot be revoked now.' : undefined,
          onclick: () => revokeSender(t),
        }, 'Revoke') : null);
    })));
}

function tagsBlock(acctName) {
  if (!st.peers && !st.trusted) return null;
  const tags = tagIndex(st.peers, st.trusted);
  return section('Tags',
    h('p', { class: 'muted small' }, TAGS_INTRO),
    !tags.length ? h('p', { class: 'muted', 'data-key': 'tags-empty' }, withCode(NO_TAGS)) : h('ul', { class: 'panel audit-list' }, tags.map((e) =>
      h('li', { class: 'row audit-row', 'data-key': `tag-${e.tag}` },
        h('span', { class: 'row-text' },
          h('span', { class: 'strong mono' }, e.tag),
          e.holders.length ? e.holders.map((p) => h('span', { class: 'small' },
            h('a', { href: `#/sessions/${encodeURIComponent(p.session)}`, 'data-key': `holder-${p.session}` }, p.name || p.handle || p.session), ' ',
            h('span', { class: 'muted' }, [acctName(p.account, p.account_label), p.machine_name || p.machine].filter(Boolean).join(' · ')), ' ',
            stateTag(p)))
            : h('span', { class: 'muted small' }, NO_HOLDER),
          e.senders.map((x) => h('span', { class: 'muted small' }, sentRecently(x))))))));
}

export function render() {
  const owner = isOwner();
  const head = h('header', { class: 'page-head col' }, h('h1', {}, 'Peers'), moreTabs('peers', { owner }),
    h('p', { class: 'muted small', 'data-key': 'peers-intro' }, 'Lets Claude sessions on different accounts send each other messages. Off by default: nothing passes between two accounts until you allow it below.'));
  if (!st.matrix) {
    return { title: 'Peers · Watchyard', node: h('div', { class: 'page' }, head, h('p', { class: 'muted' }, st.error || 'Loading…')) };
  }
  const seats = (store.overview && store.overview.seats) || [];
  const names = new Map(accounts(seats, st.matrix.matrix).map((a) => [a.account, a.label]));
  const label = (id) => names.get(id) || String(id).slice(0, 8);
  // An account as the board names it, else the hub's label for it.
  const acctName = (id, hubLabel) => names.get(id) || hubLabel || label(id);
  const node = h('div', { class: 'page' }, head,
    killSwitch(),
    matrixBlock(),
    auditBlock(label),
    messagesBlock(label),
    trustedBlock((t) => acctName(t.account, t.account_label)),
    tagsBlock(acctName),
    !owner ? ownerHintNode({ app: inApp(), lead: 'Widening, and resuming after a pause or a loop-guard stop, need an owner sign-in.', key: 'peers-owner-hint' }) : null);
  return { title: 'Peers · Watchyard', node };
}
