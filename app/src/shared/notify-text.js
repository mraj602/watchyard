// The text of a desktop notification (C9) from a fired alert and its session. Pure and
// golden-tested. The desktop token is never "private", so this names the account and the
// project, as the board does for a non-private target (push.md 5.5). Cut lengths: the question
// at NOTIFY_QUESTION_MAX, the title at 80 and the body at 180 characters.
import { NOTIFY_QUESTION_MAX } from './constants.js';

const TITLE_MAX = 80;
const BODY_MAX = 180;
const SEP = ' · ';

// push.md 3.9's usage notices: low urgency, no session.
const USAGE_NOTICES = {
  usage_reset: 'usage reset',
  seat_change: 'changed account',
  weekly_drop: 'weekly use dropped: something changed, check',
  limit_change: 'limits changed',
};

/** Whether the trigger is one of the usage notices (push.md 3.9). */
export const isUsageNotice = (trigger) => Object.hasOwn(USAGE_NOTICES, String(trigger ?? ''));

// push.md 3.9's switch notices: a machine's automatic moves and its daily digest.
const SWITCH_NOTICES = {
  session_moved: 'moved a session',
  switch_digest: 'switch digest',
};

/** Whether the trigger is one of the Smart Account Switch notices (push.md 3.9). */
export const isSwitchNotice = (trigger) => Object.hasOwn(SWITCH_NOTICES, String(trigger ?? ''));

// push.md 3.9's drain notices: a seat's drain started or ended (subject: the seat), or a
// machine's switch pulled sessions into a draining account (subject: the machine).
const DRAIN_NOTICES = {
  drain_started: 'draining',
  drain_ended: 'drain ended',
  drain_pulled: 'pulled sessions into a draining account',
};

/** Whether the trigger is one of the drain notices (push.md 3.9). */
export const isDrainNotice = (trigger) => Object.hasOwn(DRAIN_NOTICES, String(trigger ?? ''));

// The notices whose subject is a machine: named by the machine, routed to Machines.
const byMachine = (trigger) => isSwitchNotice(trigger) || trigger === 'drain_pulled';

/** The part of the hub's title before " · " (the machine or seat name it rendered), or ''. */
function titleHead(alert) {
  const t = String(alert?.title ?? '').trim();
  const i = t.indexOf(SEP);
  return i > 0 ? t.slice(0, i) : '';
}

const WINDOW_NAMES = { five_hour: '5-hour', seven_day: '7-day', seven_day_opus: '7-day Opus', seven_day_sonnet: '7-day Sonnet' };

/** Collapse whitespace, then cut to `max` characters with an ellipsis. */
export function cut(text, max) {
  const s = String(text ?? '').replace(/\s+/g, ' ').trim();
  if (s.length <= max) return s;
  return `${s.slice(0, Math.max(0, max - 1)).trimEnd()}…`;
}

export function windowName(key) {
  if (!key) return 'usage';
  return WINDOW_NAMES[key] ?? String(key).replace(/_/g, ' ');
}

/** Seat label, else the email's local part, else "Unattributed". */
export function seatName(seat) {
  const label = seat?.label ?? seat?.seat_label;
  if (label) return String(label);
  const email = seat?.email ?? seat?.account_email;
  if (email) return String(email).split('@')[0] || 'Unattributed';
  return 'Unattributed';
}

function percentIn(alert) {
  const m = /(\d{1,3})\s*%/.exec(String(alert?.title ?? ''));
  if (m) return Number(m[1]);
  const t = String(alert?.trigger ?? '');
  if (t === 'limit_95') return 95;
  if (t === 'limit_80') return 80;
  return null;
}

/**
 * The trigger text: "asked a question", "needs permission", "5-hour at 82 %".
 * @param {{trigger?: string, window_key?: string|null, title?: string}} alert
 * @param {{detail?: string|null}|null} [session]
 */
export function triggerText(alert, session) {
  const trigger = alert?.trigger ?? '';
  switch (trigger) {
    case 'needs_permission':
      return 'needs permission';
    case 'asked':
      return session?.detail === 'elicitation' ? 'is waiting for input' : 'asked a question';
    case 'failed': {
      const d = session?.detail;
      if (d === 'rate_limit') return 'rate limited';
      if (d === 'authentication_failed') return 'login needed';
      if (d === 'billing_error') return 'billing problem';
      return d ? `stopped: ${d}` : 'stopped';
    }
    case 'limit_80':
    case 'limit_95': {
      const name = windowName(alert.window_key);
      const pct = percentIn(alert);
      return pct !== null && pct >= 100 ? `${name} limit reached` : `${name} at ${pct} %`;
    }
    case 'reset':
      return `${windowName(alert.window_key)} reset`;
    case 'machine_silent':
      return 'went quiet';
    case 'usage_reset':
    case 'seat_change':
    case 'weekly_drop':
    case 'limit_change': {
      // The hub's own push title ("7-day dropped 64% → 31%") says more than the trigger.
      const title = String(alert?.title ?? '').trim();
      return title || USAGE_NOTICES[trigger];
    }
    case 'session_moved':
    case 'switch_digest':
    case 'drain_started':
    case 'drain_ended':
    case 'drain_pulled': {
      const title = String(alert?.title ?? '').trim();
      return title || SWITCH_NOTICES[trigger] || DRAIN_NOTICES[trigger];
    }
    default:
      return 'needs you';
  }
}

/** The hub route a click opens (push.md 5.8: the path after the origin, from `#/`). */
export function routeFor(alert, session) {
  const trigger = alert?.trigger ?? '';
  if (trigger === 'limit_80' || trigger === 'limit_95' || trigger === 'reset' || isUsageNotice(trigger) || trigger === 'drain_started' || trigger === 'drain_ended') {
    return alert.seat ? `#/accounts?seat=${encodeURIComponent(alert.seat)}` : '#/accounts';
  }
  if (trigger === 'machine_silent' || byMachine(trigger)) {
    return alert.machine ? `#/machines?machine=${encodeURIComponent(alert.machine)}` : '#/machines';
  }
  const sid = session?.id ?? alert?.session;
  return sid ? `#/sessions/${encodeURIComponent(sid)}?alert=${encodeURIComponent(alert.id)}` : '#/';
}

/**
 * @param {{alert: object, session?: object|null, seat?: object|null, machine_name?: string|null}} input
 *   seat: the matching /v1/accounts seat, used when the session carries no label
 * @returns {{title: string, body: string, route: string}}
 */
export function notifyText({ alert, session = null, seat = null, machine_name = null }) {
  const what = triggerText(alert, session);
  const route = routeFor(alert, session);
  let title;
  if (alert?.trigger === 'machine_silent') {
    const name = machine_name ?? session?.machine_name ?? alert?.machine ?? 'A machine';
    title = `${name}${SEP}silent`;
  } else if (byMachine(alert?.trigger)) {
    // The machine's name, never its id: the hub's title starts with it ("gpu-box · Moved …").
    title = machine_name ?? session?.machine_name ?? (titleHead(alert) || 'Smart Account Switch');
  } else if (isDrainNotice(alert?.trigger)) {
    title = seat ? seatName(seat) : titleHead(alert) || 'Drain';
  } else if (isUsageNotice(alert?.trigger)) {
    // limit_change has no seat: it is about a model across the accounts.
    title = seat ? seatName(seat) : 'Usage and limit changes';
  } else if (session) {
    const who = seatName({ label: session.seat_label ?? seat?.label, email: seat?.email });
    title = [who, session.project].filter(Boolean).join(SEP);
  } else {
    title = seatName(seat);
  }
  let body = what;
  // The hub's title of a seat's notice starts with the seat ("work · usage reset"): said once.
  const notice = isUsageNotice(alert?.trigger) || isSwitchNotice(alert?.trigger) || isDrainNotice(alert?.trigger);
  if (notice && body.startsWith(`${title}${SEP}`)) body = body.slice(title.length + SEP.length);
  // Switch and drain notices: the hub's message (the moves, the digest's summary) is the point.
  const message = (isSwitchNotice(alert?.trigger) || isDrainNotice(alert?.trigger)) ? cut(alert?.body, BODY_MAX) : '';
  if (message) body = `${body}\n${message}`;
  const question = session?.question ? cut(session.question, NOTIFY_QUESTION_MAX) : '';
  if (question) body = `${what}\n${question}`;
  return { title: cut(title, TITLE_MAX), body: body.length > BODY_MAX ? `${body.slice(0, BODY_MAX - 1)}…` : body, route };
}
