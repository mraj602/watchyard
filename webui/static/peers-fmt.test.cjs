// node --test webui/static/*.test.cjs
// The matrix arithmetic and sentences of Settings → Peers (components/peers-fmt.js, peers.md 5, 10).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const mod = (f) => {
  const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
  if (/^import /m.test(src)) throw new Error(`${f} has imports; test only pure modules`);
  const names = [...src.matchAll(/^export (?:async )?(?:function|const|let|class) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  const body = src.replace(/^export /gm, '');
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { Number, String, Array, Object, Set, Map });
};

const P = mod('components/peers-fmt.js');
const A = 'acct-a', B = 'acct-b', C = 'acct-c';
// Objects from the vm have another realm's prototypes: compare through JSON.
const j = (x) => JSON.parse(JSON.stringify(x));

test('cells: none, one way, back and both', () => {
  assert.equal(P.cellState([], A, B), 'none');
  assert.equal(P.cellState([[A, B]], A, B), 'out');
  assert.equal(P.cellState([[A, B]], B, A), 'in');
  assert.equal(P.cellState([[A, B], [B, A]], A, B), 'both');
  assert.equal(P.cellState([[A, C]], A, B), 'none');
  assert.equal(P.CELL_GLYPH.both, '⇄');
  assert.equal(P.CELL_GLYPH.none, '—');
});

test('an edit allows what it adds and denies what it removes; only adding widens', () => {
  const both = [[A, B], [B, A]];
  assert.deepEqual(j(P.cellEdit([], A, B, 'out')), { allow: [[A, B]], deny: [], widens: true });
  assert.deepEqual(j(P.cellEdit([], A, B, 'in')), { allow: [[B, A]], deny: [], widens: true });
  assert.deepEqual(j(P.cellEdit([], A, B, 'both')), { allow: [[A, B], [B, A]], deny: [], widens: true });
  assert.deepEqual(j(P.cellEdit(both, A, B, 'none')), { allow: [], deny: [[A, B], [B, A]], widens: false });
  assert.deepEqual(j(P.cellEdit(both, A, B, 'out')), { allow: [], deny: [[B, A]], widens: false });
  // One way to the other way is a swap: it adds a pair, so it widens.
  assert.deepEqual(j(P.cellEdit([[A, B]], A, B, 'in')), { allow: [[B, A]], deny: [[A, B]], widens: true });
  assert.deepEqual(j(P.cellEdit([[A, B]], A, B, 'out')), { allow: [], deny: [], widens: false });
});

test('edit body carries the base version and only what changes', () => {
  const m = { version: 4 };
  assert.deepEqual(j(P.editBody(m, { allow: [], deny: [[A, B]], widens: false })), { edit: { base_version: 4, deny: [[A, B]] } });
  assert.deepEqual(j(P.editBody(m, { same_account: false })), { edit: { base_version: 4, same_account: false } });
  assert.deepEqual(j(P.editBody(m, { paused: true })), { edit: { base_version: 4, paused: true } });
});

test('why a change is not available', () => {
  const ok = { widens: false, owner: false, push: true, offline: false };
  assert.equal(P.whyNot(ok), '');
  assert.equal(P.whyNot({ ...ok, owner: true, widens: true }), '');
  assert.match(P.whyNot({ ...ok, widens: true }), /owner sign-in/);
  assert.match(P.whyNot({ ...ok, push: false }), /only read/);
  assert.match(P.whyNot({ ...ok, offline: true }), /Offline/);
  // in the desktop app the owner sign-in is the app's own button, not a command on the hub
  assert.match(P.whyNot({ ...ok, widens: true, app: true }), /Sign in as owner on the Hub tab/);
  assert.doesNotMatch(P.whyNot({ ...ok, widens: true, app: true }), /owner-link|owner link/);
  // in a browser: an owner link from an owner device, no command
  assert.match(P.whyNot({ ...ok, widens: true }), /Make another device owner/);
  assert.doesNotMatch(P.whyNot({ ...ok, widens: true }), /wy hub/);
  assert.doesNotMatch(P.whyNot({ ...ok, push: false, app: true }), /paired phone/);
});

test('accounts: visible seats first, then accounts only the matrix names', () => {
  const seats = [{ account: A, label: 'work' }, { account: B, label: 'home', hidden: true }, { account: C, email: 'c@x' }, { account: A, label: 'dup' }];
  const m = { accounts: { 'acct-z': 'old' }, allow: [[A, 'acct-z']] };
  assert.deepEqual(j(P.peerAccounts(seats, m).map((a) => [a.account, a.label])), [[A, 'work'], [C, 'c@x'], ['acct-z', 'old']]);
  assert.deepEqual(j(P.peerAccounts(seats, m, (s) => `#${s.label || 'c'}`).map((a) => a.label)), ['#work', '#c', 'old']);
});

test('audit sentences show metadata only', () => {
  assert.equal(P.eventLabel('pause'), 'All peers paused');
  assert.equal(P.eventLabel('some_new_event'), 'some new event');
  assert.equal(P.detailText({ version: 3, widens: true, paused: false, pairs: 1, text: 'secret', nested: { a: 1 } }), 'v3 · widened · not paused · 1 pair');
  assert.equal(P.detailText({ size: 2400, status: 'refused' }), '2.4 KB · refused');
  assert.equal(P.detailText(null), '');
  assert.equal(P.auditWho({ actor: 'phone', detail: {} }), 'phone');
  assert.equal(P.auditWho({ actor: 'hub', detail: { from: 'a', to: 'b' } }, (x) => x.toUpperCase()), 'A → B');
  assert.equal(P.messageWho({ from: { account: A }, to: {} }, (x) => x), 'acct-a → ?');
  assert.equal(P.messageWho({ from: { account: A, device_name: 'laptop' }, to: { account: 'b', device_name: 'gpu-box' } }, (x) => x), 'acct-a on laptop → b on gpu-box');
  assert.equal(P.outcomeLabel('target_gone'), 'target gone');
  assert.equal(P.sizeLabel(12), '12 B');
  assert.equal(P.sizeLabel(34000), '34 KB');
});

test('Resume thread: the newest loop-guard stop of each thread not resumed since', () => {
  const t1 = 't_0123456789abcdef', t2 = 't_fedcba9876543210';
  const entries = [ // newest first
    { id: 9, event: 'loop_guard', thread: t1 },
    { id: 8, event: 'thread_resume', thread: t1 },
    { id: 7, event: 'loop_guard', thread: t1 },
    { id: 6, event: 'loop_guard', thread: t2 },
    { id: 5, event: 'loop_guard', thread: t2 },
    { id: 4, event: 'thread_resume', thread: 't_1111111111111111' },
    { id: 3, event: 'loop_guard', thread: 't_1111111111111111' },
    { id: 2, event: 'loop_guard', thread: 'th_bad' },
    { id: 1, event: 'matrix', thread: null },
  ];
  assert.deepEqual([...P.resumableStops(entries)].sort(), [6, 9]);
  assert.equal(P.resumableStops(null).size, 0);
  assert.equal(P.eventLabel('thread_resume'), 'Thread resumed');
  assert.equal(P.eventLabel('loop_guard'), 'Stopped a thread that went back and forth too often');
  assert.equal(P.eventLabel('matrix', { version: 1 }), 'Peers set up (defaults)');
  assert.equal(P.eventLabel('matrix', { version: 2 }), 'Who may message whom changed');
  assert.match(P.resumeText({ machines: 2 }), /^Thread resumed: 2 machines were told\./);
  assert.match(P.resumeText({ machines: 1 }), /1 machine was told/);
  assert.match(P.resumeText({ machines: 2 }), /the audit names each machine that refuses/);
});

test('a machine that refused the resume: named when the hub says which', () => {
  assert.equal(P.eventLabel('thread_resume_refused', { machine: 'gpu-box' }), 'gpu-box kept the stop: it does not let the hub reopen threads');
  assert.equal(P.eventLabel('thread_resume_refused', {}), 'A machine kept the stop: it does not let the hub reopen threads');
  assert.equal(P.eventLabel('thread_resume_refused', { machine: { x: 1 } }), 'A machine kept the stop: it does not let the hub reopen threads');
  assert.equal(P.eventLabel('thread_resume_refused'), 'A machine kept the stop: it does not let the hub reopen threads');
  assert.equal(P.eventLabel('thread_resume', { machine: 'gpu-box' }), 'Thread resumed');
  assert.equal(P.detailText({ machine: 'gpu-box' }), '');
  // The refusal does not bring the button back: a resume would be refused again until a prompt.
  const t1 = 't_0123456789abcdef';
  assert.equal(P.resumableStops([
    { id: 3, event: 'thread_resume_refused', thread: t1 },
    { id: 2, event: 'thread_resume', thread: t1 },
    { id: 1, event: 'loop_guard', thread: t1 },
  ]).size, 0);
});

test('Allow all adds every missing pair both ways and turns same account on; only that widens', () => {
  assert.deepEqual(j(P.allowAllEdit([], [A, B], true)), { allow: [[A, B], [B, A]], deny: [], widens: true });
  assert.deepEqual(j(P.allowAllEdit([[A, B]], [A, B, C], true)).allow, [[A, C], [B, A], [B, C], [C, A], [C, B]]);
  assert.deepEqual(j(P.allowAllEdit([[A, B], [B, A]], [A, B], false)), { allow: [], deny: [], widens: true, same_account: true });
  assert.equal(P.allowedAll([[A, B], [B, A]], [A, B], true), true);
  assert.equal(P.allowedAll([[A, B]], [A, B], true), false);
  assert.equal(P.allowedAll([[A, B], [B, A]], [A, B], false), false);
});

test('Block all removes every pair and turns same account off; never widens', () => {
  assert.deepEqual(j(P.blockAllEdit([[A, B], [B, A]], true)), { allow: [], deny: [[A, B], [B, A]], widens: false, same_account: false });
  assert.deepEqual(j(P.blockAllEdit([], false)), { allow: [], deny: [], widens: false });
  assert.equal(P.blockedAll([], false), true);
  assert.equal(P.blockedAll([], true), false);
  assert.equal(P.blockedAll([[A, B]], false), false);
  assert.deepEqual(j(P.editBody({ version: 4 }, P.blockAllEdit([[A, B]], true))), { edit: { base_version: 4, deny: [[A, B]], same_account: false } });
});

test('turn limit: whole numbers 2 to 500; raising widens, lowering narrows', () => {
  assert.equal(P.parseTurns('30'), 30);
  assert.equal(P.parseTurns(' 2 '), 2);
  assert.equal(P.parseTurns('500'), 500);
  for (const bad of ['1', '0', '501', '', '3.5', '-4', 'abc', '1e2', null]) assert.equal(P.parseTurns(bad), null, String(bad));
  assert.equal(P.turnsWidens(30, 31), true);
  assert.equal(P.turnsWidens(30, 30), false);
  assert.equal(P.turnsWidens(30, 10), false);
  // No limit (0) is the highest value.
  assert.equal(P.TURNS_NONE, 0);
  assert.equal(P.turnsWidens(30, 0), true);
  assert.equal(P.turnsWidens(500, 0), true);
  assert.equal(P.turnsWidens(0, 500), false);
  assert.equal(P.turnsWidens(0, 2), false);
  assert.equal(P.turnsWidens(0, 0), false);
  assert.equal(P.turnsLabel(0), 'no limit');
  assert.equal(P.turnsLabel(30), '30');
  assert.equal(P.detailText({ max_turns: 0 }), 'no turn limit');
  assert.equal(P.detailText({ max_turns: 30 }), '30 turns');
  assert.match(P.NO_LIMIT_HELP, /^No limit lets agents keep talking with no one watching; the other limits still apply\.$/);
  assert.deepEqual(j(P.editBody({ version: 4 }, { max_turns: 0 })), { edit: { base_version: 4, max_turns: 0 } });
  assert.deepEqual(j(P.editBody({ version: 4 }, { max_turns: 10 })), { edit: { base_version: 4, max_turns: 10 } });
});

test('trusted senders: sentences and facts', () => {
  const t = { machine: 'm_1', machine_name: 'laptop', account: 'a1', program: '/home/me/.venv/bin/doorbell', approved_at: 'A', last_send_at: null, sent_today: 0, recent_tags: [] };
  const age = (x) => `<${x}>`;
  assert.equal(P.trustedTitle(t), 'Doorbell on laptop');
  assert.equal(P.trustedTitle({ machine: 'm_2' }), 'Doorbell on m_2');
  assert.deepEqual(j(P.trustedFacts(t, 'work', age)), ['sends as work', 'approved <A>', 'has not sent yet', '0 messages today']);
  assert.deepEqual(j(P.trustedFacts({ ...t, last_send_at: 'L', sent_today: 1 }, 'work', age)), ['sends as work', 'approved <A>', 'last sent <L>', '1 message today']);
  assert.deepEqual(j(P.trustedFacts({ ...t, test_build: true }, 'work', age)), ['sends as work', 'approved <A>', 'has not sent yet', '0 messages today', 'approved by a test build']);
  assert.equal(P.trustedFacts({ ...t, test_build: false }, 'work', age).length, 4);
  assert.equal(P.revokeNotFoundText({ status: 404, message: 'The hub has no trusted sender.' }), 'The hub has no trusted sender.');
  assert.equal(P.revokeNotFoundText({ status: 404, message: 'Hub answered 404' }), "The hub has no record of this machine's approval. Revoke it on that machine with `wy peers trust doorbell --off`.");
  assert.equal(P.revokeNotFoundText({ status: 404, message: '' }), P.REVOKE_NOT_FOUND);
  assert.equal(P.TRUSTED_UNREPORTED, 'A machine whose agent has not reported its approval is not listed here; to revoke it, run `wy peers trust doorbell --off` on that machine.');
  assert.equal(P.revokedText({ revoked_at: 'R' }, age), 'Revoked <R>. To approve it again, run `wy peers trust doorbell` in a terminal on that machine.');
  assert.equal(P.isRevoked(t), false);
  assert.equal(P.isRevoked({ ...t, revoked_at: 'R' }), true);
  assert.equal(P.revokeConfirm('laptop'), "Stop Doorbell on laptop from sending to sessions? It can be approved again only from that machine's terminal.");
  assert.deepEqual(j(P.revokeBody(t)), { machine: 'm_1', sender: 'doorbell' });
  assert.equal(P.TRUSTED_EMPTY, 'Doorbell is not approved on any machine. To let it wake sessions by tag, run `wy peers trust doorbell` in a terminal on the machine it runs on (Linux).');
  assert.equal(P.NO_TAGS, 'No session has a tag. A session gives itself one with `wy peers tag add <tag>`.');
  assert.equal(P.NO_HOLDER, 'No session holds it now.');
});

test('tags: holders sorted, trusted senders that sent recently, tags only a sender used', () => {
  const peers = [
    { session: 's2', name: 'b', machine: 'm_1', tags: ['review', 'deploy'] },
    { session: 's1', name: 'a', machine: 'm_1', tags: ['deploy'] },
    { session: 's3', name: 'c', machine: 'm_2' },
  ];
  const trusted = [
    { machine: 'm_1', machine_name: 'laptop', recent_tags: [{ tag: 'deploy', at: 'T2' }, { tag: 'deploy', at: 'T1' }, { tag: 'gone', at: 'T0' }] },
    { machine: 'm_2', recent_tags: [] },
  ];
  const idx = j(P.tagIndex(peers, trusted));
  assert.deepEqual(idx.map((e) => e.tag), ['deploy', 'gone', 'review']);
  assert.deepEqual(idx[0].holders.map((p) => p.session), ['s1', 's2']);
  assert.deepEqual(idx[0].senders, [{ machine: 'm_1', name: 'laptop', at: 'T2' }]);
  assert.deepEqual(idx[1].holders, []);
  assert.deepEqual(idx[1].senders.map((x) => x.name), ['laptop']);
  assert.deepEqual(idx[2].senders, []);
  assert.equal(P.sentRecently({ name: 'laptop' }), 'Sent to recently by Doorbell on laptop');
  assert.deepEqual(j(P.tagIndex(null, null)), []);
  assert.deepEqual(j(P.tagIndex([{ session: 'x' }], [])), []);
});
