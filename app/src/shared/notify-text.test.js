import { test } from 'node:test';
import assert from 'node:assert/strict';

import { notifyText, cut, routeFor } from './notify-text.js';
import { badgeText, normalizeCount } from './badge.js';

const session = (over = {}) => ({ id: 's1', seat: 'st1', seat_label: 'work', project: 'api', detail: 'question', question: null, ...over });

test('golden: needs_permission', () => {
  assert.deepEqual(
    notifyText({ alert: { id: 'al1', trigger: 'needs_permission', session: 's1' }, session: session({ detail: 'tool' }) }),
    { title: 'work · api', body: 'needs permission', route: '#/sessions/s1?alert=al1' },
  );
});

test('golden: asked with a question', () => {
  assert.deepEqual(
    notifyText({ alert: { id: 'al2', trigger: 'asked' }, session: session({ question: 'Shall I   drop\nthe table?' }) }),
    { title: 'work · api', body: 'asked a question\nShall I drop the table?', route: '#/sessions/s1?alert=al2' },
  );
});

test('golden: asked by elicitation', () => {
  assert.equal(notifyText({ alert: { id: 'a', trigger: 'asked' }, session: session({ detail: 'elicitation' }) }).body, 'is waiting for input');
});

test('the question is cut to 140 characters with an ellipsis', () => {
  const q = 'x'.repeat(300);
  const { body } = notifyText({ alert: { id: 'a', trigger: 'asked' }, session: session({ question: q }) });
  assert.equal(body, `asked a question\n${'x'.repeat(139)}…`);
  assert.equal(body.split('\n')[1].length, 140);
  assert.equal(cut('short', 140), 'short');
});

test('golden: failed variants', () => {
  const body = (detail) => notifyText({ alert: { id: 'a', trigger: 'failed' }, session: session({ detail }) }).body;
  assert.equal(body('rate_limit'), 'rate limited');
  assert.equal(body('authentication_failed'), 'login needed');
  assert.equal(body('billing_error'), 'billing problem');
  assert.equal(body('max_output'), 'stopped: max_output');
});

test('golden: limit triggers go to the accounts route', () => {
  const seat = { id: 'st1', label: 'client' };
  assert.deepEqual(
    notifyText({ alert: { id: 'l1', trigger: 'limit_80', seat: 'st1', window_key: 'five_hour', title: 'client · 5-hour at 82%' }, seat }),
    { title: 'client', body: '5-hour at 82 %', route: '#/accounts?seat=st1' },
  );
  assert.equal(
    notifyText({ alert: { id: 'l2', trigger: 'limit_95', seat: 'st1', window_key: 'seven_day', title: 'client · 7-day limit reached at 100%' }, seat }).body,
    '7-day limit reached',
  );
  assert.equal(notifyText({ alert: { id: 'l3', trigger: 'limit_95', seat: 'st1', window_key: 'five_hour' }, seat }).body, '5-hour at 95 %');
  assert.equal(notifyText({ alert: { id: 'r', trigger: 'reset', seat: 'st1', window_key: 'five_hour' }, seat }).body, '5-hour reset');
});

test('golden: machine_silent', () => {
  assert.deepEqual(
    notifyText({ alert: { id: 'm1', trigger: 'machine_silent', machine: 'mc1' }, machine_name: 'old-vps' }),
    { title: 'old-vps · silent', body: 'went quiet', route: '#/machines?machine=mc1' },
  );
});

test('golden: usage notices (push.md 3.9): the hub title, else the trigger in words; the accounts route', () => {
  const seat = { id: 'st1', label: 'work' };
  assert.deepEqual(
    notifyText({ alert: { id: 'u1', trigger: 'usage_reset', seat: 'st1', title: 'work · usage reset' }, seat }),
    { title: 'work', body: 'usage reset', route: '#/accounts?seat=st1' },
  );
  assert.deepEqual(
    notifyText({ alert: { id: 'u2', trigger: 'weekly_drop', seat: 'st1', title: 'work · 7-day dropped 64% → 31%' }, seat }),
    { title: 'work', body: '7-day dropped 64% → 31%', route: '#/accounts?seat=st1' },
  );
  assert.equal(notifyText({ alert: { id: 'u3', trigger: 'weekly_drop', seat: 'st1' }, seat }).body,
    'weekly use dropped: something changed, check');
  assert.equal(notifyText({ alert: { id: 'u4', trigger: 'seat_change', seat: 'st1' }, seat }).body, 'changed account');
  assert.deepEqual(
    notifyText({ alert: { id: 'u5', trigger: 'limit_change', title: 'Opus 4.6 limits look higher' } }),
    { title: 'Usage and limit changes', body: 'Opus 4.6 limits look higher', route: '#/accounts' },
  );
  assert.equal(notifyText({ alert: { id: 'u6', trigger: 'limit_change' } }).body, 'limits changed');
  assert.equal(routeFor({ id: 'u7', trigger: 'seat_change', seat: 's/1' }, null), '#/accounts?seat=s%2F1');
});

test('golden: switch notices (push.md 3.9): the machine, the hub title without it, the machine route', () => {
  assert.deepEqual(
    notifyText({ alert: { id: 'w1', trigger: 'session_moved', machine: 'mc1', title: 'gpu-box · Moved 2 sessions' }, machine_name: 'gpu-box' }),
    { title: 'gpu-box', body: 'Moved 2 sessions', route: '#/machines?machine=mc1' },
  );
  assert.deepEqual(
    notifyText({ alert: { id: 'w2', trigger: 'switch_digest', machine: 'mc1' } }),
    { title: 'Smart Account Switch', body: 'switch digest', route: '#/machines?machine=mc1' },
  );
  assert.equal(notifyText({ alert: { id: 'w3', trigger: 'session_moved' } }).title, 'Smart Account Switch');
});

// 2026-11-25 21:58: the title showed the machine id and the body only "mraj602 · Switch digest".
test('switch and drain notices: the machine name from the hub title, never the id, and the hub message', () => {
  const id = 'm_01M4ZZ1035J482MZDTAFRX0NX1';
  assert.deepEqual(
    notifyText({ alert: { id: 'w4', trigger: 'switch_digest', machine: id, title: 'mraj602 · Switch digest',
      body: '  Last 24h: 4 moves, 1 limit hits\n weekly room: claude-4 12% ' } }),
    { title: 'mraj602', body: 'Switch digest\nLast 24h: 4 moves, 1 limit hits weekly room: claude-4 12%', route: `#/machines?machine=${id}` },
  );
  const moved = notifyText({ alert: { id: 'w5', trigger: 'session_moved', machine: id, title: 'worker · Moved a session', body: 'api claude-2 -> claude-4: 5h 96% >= 95' } });
  assert.equal(moved.title, 'worker');
  assert.equal(moved.body, 'Moved a session\napi claude-2 -> claude-4: 5h 96% >= 95');
  const pulled = notifyText({ alert: { id: 'd1', trigger: 'drain_pulled', machine: id, title: 'worker · Pulled 2 sessions into a draining account', body: '2 moves: a x->y; b x->y' } });
  assert.deepEqual(pulled, { title: 'worker', body: 'Pulled 2 sessions into a draining account\n2 moves: a x->y; b x->y', route: `#/machines?machine=${id}` });
  const started = notifyText({ alert: { id: 'd2', trigger: 'drain_started', seat: 'st4', title: 'claude-4 · draining until 18:00', body: 'New sessions go to it first' }, seat: { id: 'st4', label: 'claude-4' } });
  assert.deepEqual(started, { title: 'claude-4', body: 'draining until 18:00\nNew sessions go to it first', route: '#/accounts?seat=st4' });
  const ended = notifyText({ alert: { id: 'd3', trigger: 'drain_ended', seat: 'st4', title: 'claude-4 · drain ended', body: 'Its week reached 97%; back to its usual mode' } });
  assert.equal(ended.title, 'claude-4');
  assert.equal(ended.body, 'drain ended\nIts week reached 97%; back to its usual mode');
  for (const n of [moved, pulled, started, ended]) assert.ok(!n.title.includes('m_01'), n.title);
  // A long message is cut to the body limit.
  assert.ok(notifyText({ alert: { id: 'w6', trigger: 'switch_digest', title: 'a · Switch digest', body: 'x'.repeat(400) } }).body.length <= 180);
});

test('title falls back from label to the email local part to Unattributed', () => {
  const t = (s, seat) => notifyText({ alert: { id: 'a', trigger: 'asked' }, session: s, seat }).title;
  assert.equal(t(session({ seat_label: null }), { email: 'alex@example.com' }), 'alex · api');
  assert.equal(t(session({ seat_label: null, seat: null }), null), 'Unattributed · api');
  assert.equal(t(session({ project: null }), null), 'work');
});

test('text carries nothing a private push would hide behind (no generic placeholder)', () => {
  const { title, body } = notifyText({ alert: { id: 'a', trigger: 'asked' }, session: session() });
  assert.doesNotMatch(`${title} ${body}`, /1 session needs you/);
});

test('routes: a session alert without a session id falls back to the hub root', () => {
  assert.equal(routeFor({ id: 'a', trigger: 'asked' }, null), '#/');
  assert.match(routeFor({ id: 'a b', trigger: 'asked', session: 's/1' }, null), /^#\/sessions\/s%2F1\?alert=a%20b$/);
});

test('badge text', () => {
  assert.equal(badgeText(0), '');
  assert.equal(badgeText(7), '7');
  assert.equal(badgeText(30), '9+');
  assert.equal(normalizeCount(undefined), 0);
});
