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
    case 'switch_digest': {
      const title = String(alert?.title ?? '').trim();
      return title || SWITCH_NOTICES[trigger];
    }
    default:
      return 'needs you';
  }
}

/** The hub route a click opens (push.md 5.8: the path after the origin, from `#/`). */
export function routeFor(alert, session) {
  const trigger = alert?.trigger ?? '';
  if (trigger === 'limit_80' || trigger === 'limit_95' || trigger === 'reset' || isUsageNotice(trigger)) {
    return alert.seat ? `#/accounts?seat=${encodeURIComponent(alert.seat)}` : '#/accounts';
  }
  if (trigger === 'machine_silent' || isSwitchNotice(trigger)) {
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
  } else if (isSwitchNotice(alert?.trigger)) {
    title = machine_name ?? session?.machine_name ?? alert?.machine ?? 'Smart Account Switch';
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
  if ((isUsageNotice(alert?.trigger) || isSwitchNotice(alert?.trigger)) && body.startsWith(`${title}${SEP}`)) body = body.slice(title.length + SEP.length);
  const question = session?.question ? cut(session.question, NOTIFY_QUESTION_MAX) : '';
  if (question) body = `${what}\n${question}`;
  return { title: cut(title, TITLE_MAX), body: body.length > BODY_MAX ? `${body.slice(0, BODY_MAX - 1)}…` : body, route };
}
