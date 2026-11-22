// TypeScript mirrors of the hub's JSON (docs/spec/api.yaml). Only the fields the app reads are
// typed precisely; the hub may add fields (clients ignore unknown keys). Times are RFC 3339
// UTC strings with milliseconds ("2026-10-13T14:02:00.000Z"), or null.
//
// The contract of docs/spec/phone.md; keep it in step with api.yaml.

export type Iso = string;

/** api.yaml Error. */
export interface ApiError {
  error: string;
  code:
    | 'bad_request'
    | 'unauthorized'
    | 'forbidden'
    | 'machine_mismatch'
    | 'not_found'
    | 'conflict'
    | 'gone'
    | 'too_large'
    | 'rate_limited'
    | 'unavailable'
    | 'not_implemented'
    | 'internal';
}

/** api.yaml Health (GET /health, the 1.5 s reachability probe). */
export interface Health {
  ok: boolean;
  version: string;
  schema: number;
  time?: Iso;
  ui?: string;
}

export type SessionState = 'working' | 'needs_permission' | 'asked' | 'done' | 'failed' | 'ended' | 'gone';

export type AlertStatus = 'pending' | 'fired' | 'snoozed' | 'seen' | 'cancelled' | 'closed' | 'skipped';

export type Trigger =
  | 'needs_permission'
  | 'asked'
  | 'failed'
  | 'limit_80'
  | 'limit_95'
  | 'reset'
  | 'machine_silent'
  | 'limit_change'
  | 'usage_reset'
  | 'seat_change'
  | 'weekly_drop';

/** api.yaml AlertBrief. */
export interface AlertBrief {
  id: string;
  trigger?: Trigger | string;
  status: AlertStatus;
  reason?: string | null;
  due_at?: Iso | null;
  fired_at?: Iso | null;
  fire_count?: number;
  snoozed_until?: Iso | null;
  seen_at?: Iso | null;
}

/** api.yaml Delivery. */
export interface Delivery {
  target: string;
  kind: ChannelKind;
  target_name?: string | null;
  fire: number;
  batch?: string | null;
  status: 'pending' | 'held' | 'sent' | 'failed' | 'skipped' | 'gone';
  reason?: string | null;
  tries?: number;
  http_status?: number | null;
  updated_at?: Iso;
}

/** api.yaml Alert (GET /v1/alerts, /v1/alerts/{id}). */
export interface Alert extends AlertBrief {
  rule?: string | null;
  session?: string | null;
  seat?: string | null;
  machine?: string | null;
  window_key?: string | null;
  title?: string;
  body?: string | null;
  url?: string;
  created_at?: Iso;
  since_at?: Iso | null;
  ended_at?: Iso | null;
  deliveries?: Delivery[];
}

/** api.yaml Session. */
export interface Session {
  id: string;
  state: SessionState;
  detail?: string | null;
  needs_you: boolean;
  state_since: Iso;
  question?: string | null;
  last_message?: string | null;
  seat?: string | null;
  seat_label?: string | null;
  seat_colour?: string | null;
  machine?: string | null;
  machine_name?: string | null;
  slot?: string | null;
  project?: string | null;
  cwd?: string | null;
  name?: string | null;
  title?: string | null;
  kind?: 'interactive' | 'background' | null;
  model?: string | null;
  context_pct?: number | null;
  started_at?: Iso | null;
  last_event_at?: Iso | null;
  ended_at?: Iso | null;
  remote_url?: string | null;
  resume?: string | null;
  stale?: boolean;
  /** Idle or stale long enough that it no longer counts as open (states.md 5). */
  dormant?: boolean;
  /** Last sign of life: event, state change, start or end; never a listing. */
  activity_at?: Iso | null;
  machine_stale?: boolean;
  alert?: AlertBrief | null;
}

/** api.yaml SessionDetail (GET /v1/sessions/{id}). */
export interface SessionDetail extends Session {
  timeline: {
    ts: Iso;
    event: string;
    subtype?: string | null;
    tool_name?: string | null;
    state_after?: string | null;
    last_message?: string | null;
  }[];
  transitions?: { at: Iso; from?: string | null; to: string; detail?: string | null; cause: string }[];
}

/** api.yaml Window: one usage window of a seat. */
export interface Window {
  key: string; // five_hour, seven_day, …
  label?: string | null;
  used_pct?: number | null;
  peak_pct?: number | null;
  /** null only for an idle window (state 'idle'): it has not started, so no reset time yet. */
  resets_at: Iso | null;
  started_at?: Iso | null;
  unused_pct?: number | null;
  source?: 'statusline' | 'web' | 'both';
  updated_at?: Iso | null;
  pace?: { projected_pct: number; full_at?: Iso | null; band: 'comfortable' | 'on_track' | 'fast' | 'runaway' } | null;
  /** current: resets ahead; reset: resets_at passed (used 0 since); idle: not started (hubs before it omit it). */
  state?: 'current' | 'reset' | 'idle';
}

/** api.yaml Seat.mode: the picker's use of an account. */
export type PickMode = 'protected' | 'aggressive';

/** api.yaml SeatPatch: what PATCH /v1/accounts/{seat} takes (owner only). A missing key leaves the field; `mode: null` is not chosen. */
export interface SeatPatch {
  label?: string | null;
  colour?: string | null;
  order?: number;
  hidden?: boolean;
  mode?: PickMode | null;
}

/** api.yaml Seat (an account card). */
export interface Seat {
  id: string;
  account: string;
  org: string;
  email?: string | null;
  org_name?: string | null;
  plan?: string | null;
  label?: string | null;
  colour?: string | null;
  order?: number;
  /** How `wy account next` uses the account, the same on every machine (setup.md 8.5 Kind); null or missing: not chosen, treated as aggressive. */
  mode?: PickMode | null;
  hidden?: boolean;
  windows?: Window[];
  live_sessions?: number;
  web_raw_at?: Iso | null;
  /** Where the seat is used: a Claude Code folder logged in to it (`slot`), or a desktop tab (`tab_id`). */
  places?: SeatPlace[];
  /** Claude Code folders that logged out of this seat (phone.md P17.7). */
  signed_out?: { machine: string; machine_name: string; slot: string; since: Iso }[];
}

/** api.yaml Seat.places[]. */
export interface SeatPlace {
  machine: string;
  machine_name: string;
  slot?: string | null;
  tab_id?: string | null;
  stopped_reason?: string | null;
}

/** api.yaml Machine (fields the app shows). */
export interface Machine {
  id: string;
  name: string;
  os?: string | null;
  last_seen?: Iso | null;
  last_record?: Iso | null;
  silent?: boolean;
  live_sessions?: number;
  reach?: 'direct' | 'forward' | 'pull' | null;
  version_status?: 'same' | 'older' | 'newer' | 'unknown';
  alert?: AlertBrief | null;
}

/** api.yaml Overview (GET /v1/overview). */
export interface Overview {
  time: Iso;
  seats: Seat[];
  needs_you: Session[];
  machines: Machine[];
  counts: { working?: number; needs_you?: number; done?: number; live?: number };
  stream_id?: number;
}

export interface SessionsPage {
  sessions: Session[];
  next?: string | null;
}

export interface AccountsReply {
  accounts: Seat[];
}

export interface MachinesReply {
  machines: Machine[];
}

export interface AlertsPage {
  alerts: Alert[];
  next?: string | null;
}

/** api.yaml QuietHours. */
export interface QuietHours {
  enabled: boolean;
  start: string; // "HH:MM"
  end: string;
  tz?: string;
  critical?: boolean;
}

export type TapMode = 'auto' | 'session' | 'claude';

/** api.yaml DevicePrefs (push.md 5.4); `widgets` turns widget refresh frames over the link on. */
export interface DevicePrefs {
  mute?: Trigger[] | string[];
  quiet_hours?: QuietHours | null;
  private?: boolean | null;
  tap?: TapMode;
  tz?: string | null;
  widgets?: boolean;
}

export type ChannelKind = 'webpush' | 'ntfy' | 'link';

/** api.yaml PushTarget. */
export interface PushTarget {
  id: string;
  kind: ChannelKind;
  device?: string | null;
  name?: string | null;
  endpoint_host?: string;
  endpoint?: string | null;
  server?: string | null;
  topic?: string | null;
  token_set?: boolean;
  enabled: boolean;
  status: 'ok' | 'failing' | 'gone' | 'disabled' | 'new';
  last_ok_at?: Iso | null;
  last_error?: string | null;
  last_error_at?: Iso | null;
  prefs?: DevicePrefs;
  created_at?: Iso;
  /** push.md 13.7: link targets only (link.md 9). */
  link?: { connected: boolean; last_seen_at?: Iso | null; queued?: number } | null;
}

/** api.yaml Device. */
export interface Device {
  id: string;
  kind: 'machine' | 'desktop' | 'phone' | 'browser';
  name: string;
  hint?: string;
  scopes: string[];
  owner_session?: boolean;
  machine?: string | null;
  created_at: Iso;
  created_by?: string;
  last_used_at?: Iso | null;
  expires_at?: Iso | null;
  revoked_at?: Iso | null;
  current?: boolean;
  prefs?: DevicePrefs;
  targets?: PushTarget[];
}

/** api.yaml Me (GET /v1/me). */
export interface Me {
  device: Device;
  owner: boolean;
  hub: { version?: string; public_url?: string | null };
}

/** api.yaml PairClaim, as the app sends it (P2). Exactly one of secret or code. */
export interface PairClaim {
  secret?: string;
  code?: string;
  name: string;
  expect_kind: 'phone';
  cookie: false;
  client: string; // "app/<version>+<build>"
  /** keys.md 7.1: typed-code claims only (never with a secret). */
  keys?: DeviceKeysNew;
  /** keys.md 7.1: QR claims with `k`: the keys sealed with it. */
  keys_sealed?: string;
}

/** api.yaml DeviceKeysNew (keys.md 7.1), as a phone sends it. */
export interface DeviceKeysNew {
  sign: { alg: 'p256'; pub: string };
  box: { alg: 'p256'; pub: string };
  auth?: string;
}

/** api.yaml PublicKey. */
export interface PublicKey {
  alg: string;
  pub: string;
  fp: string;
}

/** api.yaml SignedDoc (keys.md 4.1). */
export interface SignedDoc {
  type: string;
  body: string;
  sigs: { by: string; fp?: string; sig: string }[];
}

/** api.yaml PairClaimReply (200 of POST /v1/pair/claim). */
export interface PairClaimReply {
  device: string;
  kind: string;
  name: string;
  scopes: string[];
  token?: string; // shown once; kept only in the keychain (P3)
  machine?: string | null;
  hub: { url?: string; version?: string };
  /** keys.md 7.3, from F1 on. */
  hub_key?: PublicKey;
  owner_key?: SignedDoc | null;
  cert?: SignedDoc;
  link?: { urls?: string[]; ping_s?: number };
  reg?: 'qr' | 'tofu' | 'ssh' | 'local';
}

/** api.yaml PushSettings (in GET /v1/rules `settings`). */
export interface PushSettings {
  quiet_hours?: QuietHours;
  private?: boolean;
  skip_remote?: boolean;
  snooze_min?: number;
  max_age_s?: number;
  batch_s?: number;
  /** Least minutes between two widget refresh frames to one link device (10-240, default 15). */
  refresh_min?: number;
}

/** api.yaml Rule (the app shows rules read-only; mute is a device pref). */
export interface Rule {
  id: string;
  trigger: Trigger | string;
  base?: boolean;
  enabled: boolean;
  delay_s: number;
  bypass_quiet?: boolean;
}

/** GET /v1/rules. */
export interface RulesReply {
  rules: Rule[];
  settings: PushSettings;
}

/** api.yaml PushTestResult. */
export interface PushTestResult {
  outcome: 'sent' | 'retry' | 'gone' | 'failed';
  http_status?: number | null;
  error?: string | null;
}

/** POST /v1/actions/{token} reply (push.md 8). */
export interface ActionReply {
  applied: boolean;
  alert?: AlertBrief;
}

/** api.yaml Matrix (peers.md 5.1): the account matrix. `allow` holds directed [from, to] account pairs. */
export interface PeerMatrix {
  v: number;
  version: number;
  paused: boolean;
  same_account: boolean;
  max_turns: number;
  issued_at?: Iso;
  accounts?: Record<string, string>;
  allow: [string, string][];
}

/** GET /v1/peers/matrix and what PUT /v1/peers/matrix and POST /v1/peers/pause answer (api.yaml MatrixInfo). */
export interface MatrixInfo {
  matrix: PeerMatrix;
  signer: 'owner' | 'hub';
  widens?: boolean;
  made_by?: string;
  created_at?: Iso;
}

/** PUT /v1/peers/matrix body for a phone: `edit` (the hub signs it; a widening edit is 403 for a phone). */
export interface MatrixEdit {
  base_version?: number;
  allow?: [string, string][];
  deny?: [string, string][];
  paused?: boolean;
  same_account?: boolean;
  max_turns?: number;
}

/** api.yaml PeerAuditEntry: metadata only. */
export interface PeerAuditEntry {
  id: number;
  at: Iso;
  actor: string;
  event: string;
  thread?: string | null;
  msg?: string | null;
  detail?: Record<string, unknown>;
}

/** api.yaml PeerMessage: who, when, size and outcome; never text. */
export interface PeerMessage {
  id: string;
  thread: string;
  from: { session?: string; device?: string; device_name?: string; account?: string };
  to: { session?: string; device?: string; device_name?: string; account?: string };
  kind: 'start' | 'reply';
  size: number;
  local?: boolean;
  status: 'queued' | 'delivered' | 'refused' | 'rejected' | 'expired' | 'target_gone' | 'failed';
  detail?: string | null;
  created_at: Iso;
  final_at?: Iso | null;
}

/** api.yaml AnalyticsTokens: counters and the API-equivalent price (analytics.md). */
export interface AnalyticsTokens {
  input: number;
  output: number;
  cache_5m: number;
  cache_1h: number;
  cache_read: number;
  replies: number;
  api_usd: number;
  unpriced_replies: number;
}

export interface AnalyticsHead {
  report: string;
  from: string;
  to: string;
  tz: string;
  seat: string | null;
}

/** GET /v1/analytics/daily. */
export interface AnalyticsDaily extends AnalyticsHead {
  totals: AnalyticsTokens;
  days: (AnalyticsTokens & { day: string; source: 'transcripts' | 'history'; total_tokens: number })[];
  seats: (AnalyticsTokens & { seat: string | null; name: string })[];
}

/** GET /v1/analytics/models. */
export interface AnalyticsModels extends AnalyticsHead {
  models: (AnalyticsTokens & { model: string; label: string; priced: boolean })[];
}

/** GET /v1/analytics/projects. */
export interface AnalyticsProjects extends AnalyticsHead {
  projects: (AnalyticsTokens & { project: string; name: string; sessions: number; last_ts: Iso | null })[];
}

/** GET /v1/analytics/hits. */
export interface AnalyticsHits extends AnalyticsHead {
  hits: { ts: Iso; seat: string | null; seat_name: string; session_id: string; type: string | null; status: string | null; resets_at: Iso | null; wait_s: number | null }[];
}

/** GET /v1/analytics/heatmap: 7 weekdays (0 = Sunday) by 24 hours of the hub's zone. */
export interface AnalyticsHeatmap extends AnalyticsHead {
  api_usd: number[][];
  replies: number[][];
}

/** GET /v1/analytics/sessions: the 50 dearest. */
export interface AnalyticsSessions extends AnalyticsHead {
  sessions: (AnalyticsTokens & { session_id: string; seat: string | null; seat_name: string; project: string; models: string[]; first_ts: Iso; last_ts: Iso })[];
}

/** GET /v1/analytics/limits: limit windows that reset inside the range, oldest first. */
export interface AnalyticsLimits extends AnalyticsHead {
  windows: { seat: string; seat_name: string; key: string; label: string; started_at: Iso | null; resets_at: Iso; peak_pct: number | null; final_pct: number | null; unused_pct: number | null; samples: number }[];
}

/** The reports the phone shows. */
export interface AnalyticsReplies {
  daily: AnalyticsDaily;
  models: AnalyticsModels;
  projects: AnalyticsProjects;
  hits: AnalyticsHits;
  heatmap: AnalyticsHeatmap;
  sessions: AnalyticsSessions;
  limits: AnalyticsLimits;
}

export interface AnalyticsQuery {
  /** The last N days ending today in the hub's zone (1-366, default 30). */
  days?: number;
  /** One seat id, or "unattributed". */
  seat?: string;
}

/** api.yaml CalibrationFactor: `f` in 5-hour points per million weighted units, `ratio` in 5-hour points per weekly point, `lane_price` in 5-hour points. */
export interface CalibrationFactor {
  value: number;
  fallback: number;
  measured: number | null;
  weight: number;
  n: number;
  days: number;
  implausible: boolean;
}

/** api.yaml Calibration (setup.md 8.5). */
export interface Calibration {
  at: Iso;
  since: Iso | null;
  reset: { at: Iso; reason: 'seat_change' | 'under_read' | 'over_read' } | null;
  f: CalibrationFactor;
  ratio: CalibrationFactor;
  lane_price: CalibrationFactor;
  lane_charge: number;
  lane_units: number | null;
  unexplained_pct_per_hour: number | null;
  unexplained_hours: number;
}

/** api.yaml RelationDay: one hub day's 5-hour per weekly point. */
export interface RelationDay {
  day: string;
  ratio: number | null;
  d5: number;
  d7: number;
  pairs: number;
  model: string | null;
  rise: number;
  rise_cc: number;
}

/** api.yaml RelationHeavy: whether a week's 5-hour rise was Claude Code tokens. */
export interface RelationHeavy {
  heavy: boolean;
  rise: number;
  cc_share: number | null;
}

/** api.yaml RelationModel: a model's 5-hour capacity index (API dollars per 5-hour point). */
export interface RelationModel {
  model: string;
  this_week: number | null;
  last_week: number | null;
  change: number | null;
  days: { day: string; index: number | null; normalised: number | null; d5: number; usd: number }[];
}

/** api.yaml Verdict: what one run of `wy account next` decided about the seat (record.md 4.11). */
export interface Verdict {
  at: Iso;
  machine: string;
  machine_name: string | null;
  strategy: string;
  kind?: 'aggressive' | 'protected' | null;
  eligible: boolean;
  reason?: string | null;
  skip_level?: number | null;
  rank?: number | null;
  expiring?: boolean;
  free_at?: Iso | null;
  next_free?: Iso | null;
  weekly_pct?: number | null;
  five_hour_pct?: number | null;
  burst?: number | null;
  burst_cap?: number | null;
  stale?: boolean;
  /** Where the kind came from on that machine (record.md 4.11); missing or null from an older wy. */
  mode_from?: 'local' | 'hub' | 'proposed' | 'none' | null;
  /** The weekly level at which it takes no new session now (protected: the pace line); null from an older wy. */
  weekly_cap?: number | null;
  /** The 5-hour level at which it takes no new session; null from an older wy. */
  five_hour_cap?: number | null;
}

/** GET /v1/accounts/{seat}/relation (api.yaml UsageRelation). */
export interface UsageRelation {
  seat: string;
  time: Iso;
  tz: string;
  today: string;
  days: RelationDay[];
  heavy: { this_week: RelationHeavy; last_week: RelationHeavy };
  models: RelationModel[];
  calibration: Calibration | null;
  verdict: Verdict | null;
}
