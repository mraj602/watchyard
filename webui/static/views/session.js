// Session detail: state, last message, facts, transition timeline.

import { h } from '../components/dom.js';
import { dot, copyButton, ask } from '../components/cards.js';
import { seatIndex, sessionSeat } from '../components/seat.js';
import { stateTag, stateInfo, isEnded, isHistory } from '../components/state.js';
import { clock, ago, agoLabel, dateTime, timeOfDay, day, pct, projectOf, compact, humanKey, httpsUrl, subtitleOf } from '../format.js';
import { openDetail } from '../store.js';
import { alertButtons, alertLine, alertText } from '../components/alerts.js';
import { getAlert, getSwitches } from '../api.js';
import { movePath, failedCount, failedText } from '../components/switches-fmt.js';

// The alert a push linked to (#/sessions/<id>?alert=<al>), when it is not the session's current one.
let linked = null;
// This session's moves to other accounts (the Switch log), oldest first.
let moves = { id: '', list: [] };

export function load({ route }) {
  linked = null;
  if (moves.id !== route.id) moves = { id: route.id, list: [] };
  const asked = route.id;
  getSwitches({ session: asked, limit: '200' }).then((r) => { moves = { id: asked, list: r.moves }; window.dispatchEvent(new Event('wy-rerender')); }).catch(() => {});
  if (route.id !== showAllFor) { showAll = false; showAllFor = route.id; }
  const id = route.query.get('alert');
  if (id) getAlert(id).then((a) => { linked = a; window.dispatchEvent(new Event('wy-rerender')); }).catch(() => {});
  return openDetail(route.id);
}

const EVENT_NAMES = {
  SessionStart: 'Session started', SessionEnd: 'Session ended', UserPromptSubmit: 'You sent a prompt',
  Stop: 'Claude finished', StopFailure: 'Turn failed', PermissionRequest: 'Permission requested',
  PermissionDenied: 'Permission denied', Notification: 'Notification', PreToolUse: 'Tool starting',
  PostToolUse: 'Tool finished', PreCompact: 'Compacting', PostCompact: 'Compacted',
};

// Keep the events where the state changed (plus the first and last); the rest sit behind a toggle.
function transitions(tl) {
  return tl.filter((e, i) => i === 0 || i === tl.length - 1 || (e.state_after && e.state_after !== tl[i - 1].state_after));
}

// "Show all events" is for the session it was turned on in.
let showAll = false;
let showAllFor = null;

// The session's path through accounts: claude-3 → claude-2 (14:02) → claude-6 (16:40).
function movesCard(s, now) {
  const list = moves.id === s.id ? moves.list : [];
  if (!list.length) return null;
  const path = movePath(list);
  const failed = failedCount(list);
  return h('section', { class: 'card sw-path', 'aria-labelledby': 'h-moves', 'data-key': 'session-moves' },
    h('div', { class: 'block-head' }, h('h2', { id: 'h-moves' }, 'Moved between accounts'),
      h('a', { href: `#/switches?session=${encodeURIComponent(s.id)}&range=all`, 'data-key': 'session-moves-all' }, 'Every move and why')),
    path.length ? h('ol', { class: 'sw-steps', 'aria-label': 'Accounts in order' }, path.map((p, i) => h('li', {},
      i > 0 ? h('span', { class: 'sw-arrow', 'aria-hidden': 'true' }, '→') : null,
      h('span', { class: 'strong' }, p.label),
      p.at ? h('time', { class: 'muted small mono', datetime: p.at, title: dateTime(p.at) }, ` ${clock(p.at, now)}`) : null))) : null,
    failed ? h('p', { class: 'muted small' }, failedText(failed)) : null);
}

export function render({ store, route, now }) {
  const back = h('a', { href: '#/sessions', class: 'back' }, '← Sessions');
  const s = store.detail && store.detail.id === route.id ? store.detail : null;
  if (!s) {
    const gone = store.errorStatus === 404;
    return { title: 'Session · Watchyard', node: h('div', { class: 'page' }, back,
      gone ? h('p', { class: 'calm', role: 'status', 'data-key': 'session-missing' }, 'This session no longer exists, or was never on this hub. ', h('a', { href: '#/sessions' }, 'Back to Sessions'))
        : h('p', { class: store.error ? 'calm' : 'muted pad' }, store.error || 'Loading session…')) };
  }
  const idx = seatIndex(store.overview.seats);
  const seat = sessionSeat(s, idx);
  const info = stateInfo(s);
  const tl = s.timeline || [];
  const events = showAll ? tl : transitions(tl);
  const text = ask(s);
  // The text is what it waits on (a question, a permission or a plan), not its last message.
  const waits = !!s.question || s.state === 'needs_permission' || (s.state === 'asked' && s.tool_name === 'ExitPlanMode');
  // Imported from Claude Code's history: ended with detail "history" (D3), or a bare row.
  const fromHistory = s.state === 'ended' && s.detail === 'history';
  const imported = isHistory(s) || fromHistory;
  const name = s.project || s.cwd ? projectOf(s) : imported ? 'Imported session' : 'Session';
  const since = ago(s.state_since, now);
  // An imported session cannot be reopened in the Claude app (D3).
  const remote = imported ? null : httpsUrl(s.remote_url);
  // Open but never sent a hook event: Claude Code loads hooks when a session starts, so a
  // session that was already running when Watchyard was set up only shows up in listings.
  const noHooks = !imported && !isEnded(s) && tl.length === 0 && !s.last_event_at;

  const facts = [
    ['Account', h('span', {}, dot(seat.colour), seat.label)],
    ['Machine', s.machine_name],
    ['Claude profile', s.slot && h('span', { class: 'mono', title: 'The Claude Code config folder this session runs in' }, s.slot)],
    ['Folder', s.cwd && h('span', { class: 'mono' }, s.cwd)],
    ['Model', s.model],
    ['Type', s.kind],
    ['Context used', s.context_pct != null && pct(s.context_pct)],
    ['Started', s.started_at && `${dateTime(s.started_at)} (${agoLabel(s.started_at, now)})`],
    ['Last event', s.last_event_at && agoLabel(s.last_event_at, now)],
    ['Ended', s.ended_at && dateTime(s.ended_at)],
    ['Session ID', h('span', { class: 'mono small' }, s.id)],
  ].filter(([, v]) => v);

  // No replies counted (hooks never sent usage, or imported): no token line rather than zeros.
  const t = s.tokens && s.tokens.replies ? s.tokens : null;
  const node = h('div', { class: 'page' }, back,
    h('header', { class: 'page-head col' },
      h('h1', {}, name, subtitleOf(s, name) ? h('span', { class: 'muted' }, ` · ${subtitleOf(s, name)}`) : null),
      h('div', { class: 'head-meta' }, stateTag(s, true),
        since ? h('span', { class: 'muted' }, isEnded(s) ? (since === 'just now' ? ' just now' : ` ${since} ago`) : ` for ${since}`) : null,
        s.stale ? h('span', { class: 'flag' }, ' · stale: no events for 12 h') : null,
        s.machine_stale ? h('span', { class: 'flag' }, ' · machine quiet') : null)),

    h('div', { class: 'actions session-actions' },
      remote ? h('a', { class: 'btn btn-primary', href: remote, target: '_blank', rel: 'noopener noreferrer', title: 'Opens this session in the Claude app or claude.ai' }, 'Open in Claude') : null,
      s.resume ? copyButton(s.resume, 'Copy resume command', 'copy-resume') : null,
      alertButtons(s)),
    s.alert ? h('p', { class: 'muted small alert-meta' }, 'Alert: ', alertLine(s.alert, now)) : null,
    linked && (!s.alert || s.alert.id !== linked.id) ? h('p', { class: 'muted small alert-meta' }, `The alert you opened: ${alertText(linked, now)}`) : null,
    imported ? h('p', { class: 'calm small', 'data-key': 'imported-note' }, isHistory(s) ? 'Imported from history: the hub has no project, folder or messages for this session.' : 'Imported from history: the hub never saw this session live, so it cannot be reopened in the Claude app.') : null,
    s.resume ? h('pre', { class: 'cmd mono small' }, s.resume) : null,
    remote ? h('p', { class: 'muted small' }, 'The Claude app is signed in to one account at a time: if it shows another account, use the resume command.') : null,

    text ? h('section', { class: `card quote needs-${info.cls}`, 'aria-label': waits ? 'Question' : 'Last message' },
      h('div', { class: 'quote-label' }, waits ? 'Waiting on you' : 'Last message'),
      h('p', { class: s.state === 'needs_permission' ? 'mono' : '' }, text)) : null,

    movesCard(s, now),

    h('div', { class: 'cols' },
      h('section', { class: 'card', 'aria-labelledby': 'h-facts' }, h('h2', { id: 'h-facts' }, 'Details'),
        h('dl', { class: 'kv' }, facts.flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
        t ? h('p', { class: 'muted small tok' }, `Tokens: ${compact(t.input)} in · ${compact(t.output)} out · ${compact(t.cache_read)} cache read`,
          t.api_usd != null ? h('span', { title: 'What the same tokens would cost at API list prices: a yardstick, not a bill' }, ` · about $${t.api_usd.toFixed(2)} at API prices`) : '') : null),

      h('section', { class: 'card', 'aria-labelledby': 'h-tl' },
        h('div', { class: 'block-head' }, h('h2', { id: 'h-tl' }, 'Timeline'),
          tl.length > events.length || showAll ? h('button', { type: 'button', class: 'linkish', 'data-key': 'tl-toggle',
            onclick: () => { showAll = !showAll; window.dispatchEvent(new Event('wy-rerender')); } },
            showAll ? 'State changes only' : `Show all ${tl.length} events`) : null),
        events.length === 0 ? h('p', { class: 'muted', 'data-key': noHooks ? 'no-hooks' : null }, noHooks
          ? 'This session started before Watchyard was set up, so the hub only sees that it is running: no messages or events. Start a new session, or resume this one (claude --resume), to see its timeline and last message.'
          : imported ? 'No events: the hub only has this session\'s history (times, tokens), not what happened in it.' : 'No events yet.')
          : h('ol', { class: 'timeline' }, [...events].reverse().map((e) => h('li', { class: 'tl-item' },
            h('time', { datetime: e.ts, class: 'tl-time mono small', title: dateTime(e.ts) }, `${day(e.ts, now)} ${timeOfDay(e.ts)}`),
            h('span', { class: 'tl-what' }, EVENT_NAMES[e.event] || humanKey(e.event),
              e.tool_name ? h('span', { class: 'mono small' }, ` ${e.tool_name}`) : null,
              e.subtype ? h('span', { class: 'muted small' }, ` (${e.subtype})`) : null),
            e.state_after ? h('span', { class: 'muted small' }, `· now ${stateInfo({ state: e.state_after }).label}`) : null,
            e.last_message ? h('div', { class: 'tl-msg muted small' }, e.last_message) : null))))));

  return { title: `${name} · Watchyard`, node };
}
