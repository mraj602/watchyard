// Hash routes (pwa.md 3). Pure, so node can test it.
//
//   #/                       Now
//   #/sessions               Sessions (?filter=open|needs_you|working|done|ended|all, ?by=machine|project, ?machine=<id>)
//   #/sessions/<id>          Session (?alert=<alert id>)
//   #/accounts               Accounts (?seat=<id>)
//   #/machines               Machines (?machine=<id>)
//   #/switches               Switches: the Smart Account Switch log (?machine, ?account, ?session, ?mode=auto|manual, ?result=failed, ?range=24h|7d|30d|all|custom, ?from, ?to)
//   #/more                   More
//   #/more/alerts            Alerts and rules
//   #/more/settings          Settings
//   #/more/devices           Devices (owner)
//   #/more/analytics         Analytics
//   #/more/peers             Settings → Peers: account matrix, Pause all peers, audit
//   #/more/keys              Settings → Keys: hub and owner key, certificates, the next step
//   #/pair?s=<secret>&c=<digits>   Pairing
//   #/owner?s=<secret>       Owner sign-in
//   #/alerts/<id>            Redirects to the alert's deep link
//   #/login                  Token sign-in
//   anything else            Page not found (with a link to Now)

// Routes that work without being signed in.
export const PUBLIC_VIEWS = new Set(['login', 'pair', 'owner']);

// Which bottom-tab (phone) is lit for a page.
export const TAB_OF = {
  now: 'now', sessions: 'sessions', accounts: 'accounts',
  machines: 'more', switches: 'more', alerts: 'more', settings: 'more', devices: 'more', peers: 'more', keys: 'more', analytics: 'more', more: 'more',
  notfound: '',
};

export function parseRoute(hash) {
  const [path, qs = ''] = String(hash || '').replace(/^#/, '').split('?');
  const parts = path.split('/').filter(Boolean).map((p) => {
    try { return decodeURIComponent(p); } catch (_) { return p; }
  });
  const query = new URLSearchParams(qs);
  const [a, b] = parts;
  if (a === 'login') return { page: 'login', view: 'login', query };
  if (a === 'pair') return { page: 'pair', view: 'pair', query };
  if (a === 'owner') return { page: 'owner', view: 'owner', query };
  if (a === 'alerts' && b) return { page: 'alerts', view: 'alertRedirect', id: b, query };
  if (a === 'sessions' && b) return { page: 'sessions', view: 'session', id: b, query };
  const notFound = { page: 'notfound', view: 'notfound', query };
  if (parts.length > (a === 'more' ? 2 : 1)) return notFound;
  if (a === 'sessions') return { page: 'sessions', view: 'sessions', query };
  if (a === 'accounts') return { page: 'accounts', view: 'accounts', query };
  if (a === 'machines') return { page: 'machines', view: 'machines', query };
  if (a === 'switches') return { page: 'switches', view: 'switches', query };
  if (a === 'more') {
    if (b === 'alerts') return { page: 'alerts', view: 'alerts', query };
    if (b === 'settings') return { page: 'settings', view: 'settings', query };
    if (b === 'analytics') return { page: 'analytics', view: 'analytics', query };
    if (b === 'peers') return { page: 'peers', view: 'peers', query };
    if (b === 'keys') return { page: 'keys', view: 'keys', query };
    if (b === 'devices') return { page: 'devices', view: 'devices', query };
    if (!b) return { page: 'more', view: 'more', query };
    return notFound;
  }
  if (!a || a === 'now') return { page: 'now', view: 'now', query };
  return notFound;
}

// A hash route for an app deep link from the hub ("/#/sessions/x?alert=y" or "#/…").
export function hashOfLink(url) {
  if (typeof url !== 'string') return '#/';
  const i = url.indexOf('#');
  return i >= 0 ? url.slice(i) : '#/';
}

// The sessions screen's query: legacy-free filter names from pwa.md 3.
export const SESSION_FILTERS = ['open', 'needs_you', 'working', 'done', 'ended', 'all'];
