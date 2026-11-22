# The link (`/v1/link`)

Status: spec v1. Implemented in `internal/link` (shared frame code), `internal/hub/link` (the hub
side) and `internal/agent` (the machine client) and the Android client (push.md 14). Keys and
documents: [keys.md](keys.md). What travels on it: alerts (push.md 13), peer messages
([peers.md](peers.md)), the matrix and revocations. Schema v7 (`link_state`, `link_queue`).
Unofficial; not affiliated with Anthropic.

The link is the one path from the hub **to** a device that is not a browser: the Android app's
`WyLinkService` and `wy agent` on each machine. `/v1/stream` stays the viewers' SSE
(api.md); nothing here changes it. Devices still call the HTTP API with their bearer for
everything else.

```
hub ── link_queue(device) ── /v1/link (WebSocket, or /v1/link/poll) ──► device
          ▲ enqueue: seal to the device, sign with the hub key, store       │
          └──────────────────────── ack(seq) ◄─────────────────────────────┘
```

## 1. Properties

| Property | How |
| --- | --- |
| No reusable credential on the wire | Login is a challenge signed by the device key (3). The bearer is never sent on `/v1/link`, so a Funnel node or proxy that sees the traffic cannot replay anything. |
| Nothing lost | Every queued frame stays in `link_queue` until the device acks its `seq` (5). Reconnect replays from the last ack. |
| Nothing forged | Every frame is signed by the hub key the device pinned (keys.md 3.5), over the device id, type, `seq`, expiry, tag and body (4.3). A database-only attacker cannot add a frame a device accepts, and cannot ack for the device: the hub takes acks only on a connection that logged in with the device key (3). |
| Nothing readable at rest or in transit | Queued bodies are sealed to the device's key (4.2) before they are stored. |
| Small | Latest state wins: frames with the same tag collapse (5.3); old frames expire (5.4); each queue is capped (5.5). |

**Honest limits.** Someone who can **write** the database can still delete queued rows or set
`delivery.delivered_at` (loss and a false delivery log, never a forged frame), and can mint
tokens other than owner sessions (tokens.md 2 peppers only `wy_o_` hashes), so not the
owner-session rights of keys.md 5.4. TLS is the only
channel binding: a party that terminates TLS between the device and the hub can relay a live
login, then drop frames, inject device→hub frames (acks, actions, `peer_result`) and read the
inline fields of ephemeral frames while that connection lasts. It still cannot read a sealed
body, forge a hub frame or log in again later. Every supported path terminates TLS on the hub
machine (`tailscale serve`, and Funnel, whose TLS ends in the hub's own `tailscaled`).

**Byte strings.** In every signed or sealed input below, `‖` is byte concatenation, text is
UTF-8, `"\n"` is the byte 0x0A, `decimal(n)` is base-10 ASCII with no sign, padding or leading
zeros, and nonces, fingerprints and ids appear as the text that travels in the JSON (b64u for
the nonce). No field placed before the last one may contain `\n`: ids, types and fingerprints
cannot, and tags are limited to `[A-Za-z0-9_:.-]{0,64}` (5.3).

## 2. Transport

- **URL.** `wss://<hub host>[:port]/v1/link`, the hub's own listener (TLS from `tailscale serve`
  or the hub's certificate, as for the rest of the API). A device gets the list of URLs in the
  claim reply (`link.urls`, keys.md 7.3) and in `welcome`; it tries them in order. A machine
  (`wy agent run`) instead uses its configured `hub.url` (`http`→`ws`, `https`→`wss`, plus
  `/v1/link`) through `hub.dial` like every other `wy` call, and ignores `urls`.
- **Subprotocol** `wy-link.1` (`Sec-WebSocket-Protocol`). The hub refuses others with
  HTTP 426.
- **Messages** are WebSocket text messages, one JSON object each, at most 1 MiB (the library's
  read limit; larger → close 4413). Binary messages → close 4400.
- **Library.** `github.com/coder/websocket` (ISC, no dependencies of its own), the newest
  release at least two weeks old when it was adopted. It is the one WebSocket dependency; the standard library has no WebSocket server. Its `Accept` checks `Origin`: the hub
  accepts no `Origin` (native clients) or its own origin only.
- **Pings.** The device sends a WebSocket ping every `ping_s` seconds (default **180**, from
  `welcome`; the app may raise it to 300–600 on the tailnet, push.md 14.4); the hub answers
  with pong (the library does this). The hub closes a connection that has sent nothing, not
  even a ping, for `2 × ping_s + 30` seconds (close 4408), where `ping_s` is the larger of the
  channel's and the device's own `hello.client.ping_s` (60–900, when sent), so a phone that
  raised its interval is not cut off. The hub sends no pings of its own:
  the device owns the radio.
- **One connection per device.** A new authenticated connection closes the old one with 4409
  `superseded`.
- **Load.** At most 256 connections in the challenge phase hub-wide, and at most 8 from one
  client address (`auth.ClientIP`, so it follows `--trusted-proxy` behind a proxy). From one
  address, more are refused with HTTP 429; past the hub-wide cap, HTTP 503. A connection that
  has not sent a valid `hello` within 5 s of the challenge is closed with 4401, so silent
  connections free their slot quickly. Challenges are stateless (3.1), so `op: challenge`
  pins nothing. At most 10 000 unexpired spent nonces are remembered (only hellos with a valid
  device signature add to them); past that a login is closed with 4429 `busy`.

### 2.1 Long-poll fallback

For networks that break WebSockets (some captive portals and corporate proxies), the same
frames go over plain HTTPS: `POST /v1/link/poll`, JSON in and out, no bearer.

| Request `op` | Body | Reply |
| --- | --- | --- |
| `challenge` | `{"op":"challenge"}` | the `challenge` frame (3.1) |
| `poll` | `{"op":"poll","hello": <hello frame, 3.2>,"up"?: [frames]}`: `hello.last` is the cumulative ack (it is signed); `up` holds device→hub frames other than `ack` (6.2), at most 64 | `{"welcome", "frames": [ … ], "results": [ … ], "next": <challenge frame>}` |

- The hub holds a `poll` open up to 25 s while the queue has nothing past the device's ack, and
  answers as soon as something is queued. Each reply carries the next challenge (`next`), so
  each request is a fresh login with a single-use nonce.
- Ephemeral frames (6.3) are returned in the reply (`results`) of the request that caused them;
  they and `welcome` are signed with that request's nonce as context (4.3).
- Poll logins count against their own limit (120 a minute per device), not 3.2 step 5's.
- `link_state.transport` is `poll`. The app uses it after 3 failed WebSocket handshakes in a
  row where plain HTTPS to the hub works, and tries the WebSocket again every 30 minutes.

### 2.2 Exposure

On a hub reachable through Tailscale Funnel, Funnel forwards **only**
the `/v1/link` prefix (`/v1/link` and `/v1/link/poll`) to the hub; the board and the rest of the
API stay on the tailnet. `wy doctor` on the hub checks this from the outside (`/v1/health` must
not answer on the Funnel name). The hub itself does not distinguish the two paths: the
challenge login is what protects the link.

## 3. Login

### 3.1 Challenge (hub → device, first message)

```json
{"t": "challenge", "v": 1, "hub": "<hub fp>", "nonce": "<b64u, 32 bytes>", "exp": 1760518860, "sig": "<b64u>"}
```

`sig` = hub Ed25519 over `"wy-link-v1 challenge\n" ‖ hub fp ‖ "\n" ‖ nonce ‖ "\n" ‖
decimal(exp)`. `exp` is the hub's clock + 60 s. The device checks the signature against its
pinned hub key (keys.md 3.5) and `hub` against the pinned fingerprint, and closes the
connection (no `hello`) if either fails: it never signs a nonce for a hub it does not trust.
It does not compare `exp` with its own clock (phones' clocks drift); the hub enforces it.
A nonce is 16 random bytes followed by 16 bytes of HMAC-SHA-256 (a per-process hub secret) over
those bytes and `exp`, so the hub checks it without storing it; a hub restart voids open
challenges. A nonce is single-use: the hub remembers it as spent until `exp` once a hello with a
valid signature has used it.

### 3.2 Hello (device → hub)

```json
{
  "t": "hello", "v": 1,
  "device": "dev_01JA4…",
  "nonce": "<the challenge nonce>",
  "exp": 1760518860,
  "last": 41,
  "client": {"kind": "android", "app": "0.9.0", "build": "14", "sdk": 35, "ping_s": 300},
  "known": {"matrix": 3, "revocations": 2},
  "sig": "<b64u>"
}
```

- `"exp"`: copied from the challenge.
- `sig` = the device's sign key over
  `"wy-link-v1 hello\n" ‖ hub fp ‖ "\n" ‖ nonce ‖ "\n" ‖ decimal(exp) ‖ "\n" ‖ device ‖ "\n" ‖ decimal(last)`:
  Ed25519 (machines) or ECDSA P-256 SHA-256, DER (phones; keys.md 2). The signature binds the
  login to this hub's key, this device, this single-use nonce and its expiry; it carries no
  reusable secret.
- `last`: the highest `seq` the device has processed (its own durable copy, 6.1).
- `client.kind`: `android` or `agent`; `wy` sends `{"kind":"agent","wy":"0.9.0","os":"linux"}`. `client.ping_s` (optional): the device's own ping interval (2).
- `known`: the newest matrix and revocation versions the device holds (machines only).

The hub, in order:

1. Nonce issued by this hub for this `exp` (HMAC checks), not past `exp` → else 4401. The nonce
   is marked used after the signature (step 4) and the rate limit (step 5) pass, so a caller
   without a device key cannot use up nonces; a replay of a used nonce → 4401.
2. Device exists, not revoked → else 4403. Has a `device_key` row → else 4401 with reason
   `no_keys` (the device must `PUT /v1/devices/{id}/keys` first).
3. The certificate's hub signature (`device_key.hub_sig` over `cert`) verifies with the hub key,
   and the body's `device` and `hub` are this device and this hub → else 4401 `bad_cert` and a
   warning in the hub log ("device_key row for dev_… does not match its certificate"). From
   here on the hub uses the keys **from the verified certificate body** (to check `sig` and to
   seal, 4.2), never the `sign_pub` / `box_pub` columns, which are copies for queries.
4. `sig` verifies with the certificate's sign key → else 4401.
5. Rate: at most 10 logins a minute per device (4429).
6. Any open connection of the device is closed with 4409.
7. **Resync.** When `last ≥ link_state.next_seq` the device is ahead of the hub, which happens
   after the hub was restored from a backup (machines.md). The device is not wrong (it signed
   `last`), so the hub does not refuse it: in one transaction it renumbers the device's
   queued rows to `last + 1, last + 2, …` in their order, re-signs them (4.3; the sealed
   bodies do not change), sets `next_seq` past them and `acked_seq = last`, and logs a warning
   ("link resync for dev_…: device at 41, hub at 12"). Nothing queued is lost.
8. Otherwise `last` is a signed, cumulative ack: `acked_seq = max(acked_seq, last)`, and rows
   with `seq ≤ last` are deleted exactly as for an `ack` (5.6; an implicit ack of frames
   processed before the connection dropped).

### 3.3 Welcome (hub → device)

```json
{"t": "welcome", "seq": 0, "exp": 1760518920, "tag": "", "body": "<b64u of the JSON below>", "sig": "<b64u>"}
```

```json
{
  "v": 1,
  "device": "dev_01JA4…",
  "next": 57,
  "ping_s": 180,
  "urls": ["wss://hub.example.ts.net/v1/link"],
  "owner": {"type": "owner_key", "body": "…", "sigs": [ … ]},
  "server_time": "2026-10-15T09:00:01.000Z"
}
```

An ephemeral frame (4.1, 6.3): signed with the nonce as context, so it cannot be replayed into
another login. `next` is the hub's `next_seq` (informational). `server_time` lets the device
judge `exp` with the hub's clock: it keeps `offset = server_time − its clock` and drops a frame
only when its clock + `offset` is past `exp`. `owner` is the active owner key document with the
owner's self-signature and the hub's confirmation signature (keys.md 3.3), or null; a machine
checks both and pins it on first sight (keys.md 3.5), and compares it afterwards (a difference
is logged and shown by `wy agent status`, never acted on: only `wy keys trust-owner` at a
terminal changes the pin). Then the hub replays: every row with `seq > acked_seq`, in order,
within the in-flight window (5.6). When `known` is behind, the hub queues the current `matrix` and
`revocations` documents for that device (if not already queued).

## 4. Frames

### 4.1 Shape

Every frame after login is one JSON object:

```json
{"t": "alert", "seq": 57, "exp": 1760540400, "tag": "wy-s-c41e…", "body": "<b64u>", "sig": "<b64u>"}
```

Hub→device frames always have these six fields (`tag` may be `""`), and their content is
always in `body`; `challenge` (3.1) is the one exception. Device→hub frames are plain JSON
objects with their fields inline and no `sig` (6.2).

| Field | Rule |
| --- | --- |
| `t` | Frame type (6). |
| `seq` | Queued frames: the queue position, > 0, increasing per device (gaps allowed, 5.3). Ephemeral frames: 0. |
| `exp` | Unix seconds after which the device drops the frame unprocessed (and still acks it). Ephemeral frames: the hub's clock + 60 s. |
| `tag` | Collapse key (5.3); `""` for none. |
| `body` | b64u. Queued frames: the sealed body (4.2). Ephemeral frames: the frame's JSON object, **not** sealed (6.3 says why), as the exact bytes that were signed; the device verifies before it parses. |
| `sig` | The hub signature (4.3). |

### 4.2 Sealing

The plaintext of a queued body is the type's JSON (6). Sealing depends on the device's
`box_alg` (keys.md 4.2):

**Phones (`p256`): RFC 8291**, exactly the Web Push code (push.md 6.3,
`internal/hub/push/webpush.Encrypt(rand, plaintext, p256dh, auth, 128)`) with the phone's
`box_pub` as `ua_public` and its 16-byte auth secret (registered with the keys, push.md 13.7) as
`auth_secret`. The body is the RFC 8188 `aes128gcm` record (salt ‖ rs ‖ idlen ‖ as_public ‖
ciphertext). The record limit (3 993 bytes of plaintext) applies; 6's bodies stay under 3 000
bytes, as for Web Push. The phone decrypts with the same steps, its Keystore `wy_link_agree`
key doing the ECDH (push.md 14.3).

**Machines (`x25519`): HPKE** (RFC 9180, Go `crypto/hpke`, base mode, single shot):

```
suite      = DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / ChaCha20-Poly1305     (0x0020 / 0x0001 / 0x0003)
pk         = hpke.NewDHKEMPublicKey(ecdh.X25519().NewPublicKey(box_pub))
info       = "wy-link-v1 seal\n" ‖ device id ‖ "\n" ‖ t
body       = hpke.Seal(pk, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), info, plaintext)
           = enc (32 bytes) ‖ ciphertext (plaintext + 16)
open       = hpke.Open(sk, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), info, body)
```

No size limit beyond the queue's (a `peer_msg` is up to 64 KB of text, peers.md 9).
`info` binds the body to the device and the frame type; the signature (4.3) binds the rest.

Sealing happens **at enqueue**, once; the row stores the sealed bytes. A replay sends the
same bytes.

### 4.3 Signature

```
sig = Ed25519(hub key,
  "wy-link-v1\n" ‖ device id ‖ "\n" ‖ t ‖ "\n" ‖ decimal(seq) ‖ "\n" ‖ decimal(exp) ‖ "\n" ‖
  tag ‖ "\n" ‖ ctx ‖ "\n" ‖ payload)
```

- Queued frames: `ctx = "queue"`, `payload` = the sealed body bytes. Made at enqueue and stored
  in `link_queue.sig`, so the hub key is not needed again to replay.
- Ephemeral frames (seq 0): `ctx` = the login nonce (b64u) of this connection (for long-poll:
  of the request), `payload` = the decoded `body` bytes. This makes an ephemeral frame valid
  for one connection only.
- `t` is one of 6.3 / 6.4's hub frame types, so each signed input has exactly one parse.
- The device checks, in order:
  1. `sig` over its own device id → else **reject** (below);
  2. `seq` = 0 for an ephemeral type and > 0 for a queued type → else reject;
  3. queued: `seq` > its `last` → else ack and drop (a duplicate);
  4. `exp` (with the welcome's clock offset; `welcome` itself uses the offset it carries) → past:
     ack and drop;
  5. open the body (4.2) and parse it → else reject.
- **Reject** means: drop, log, count it (`wy agent status` / the app's Settings → Notifications
  show "rejected frames: n") and, for a queued frame, send `{"t":"reject","seq":N}` (6.2). The
  device never acks a frame whose signature failed, because its `seq` is not trustworthy: an
  inserted row with seq 10^9 must not move `last`. The hub deletes that one row and sends on,
  so a poisoned row cannot block the queue either.

## 5. The queue

### 5.1 Tables (schema v7; `type` gained `peer_thread_resume` in v10)

- `link_state`: per device `next_seq`, `acked_seq`, the current transport and connection time,
  last seen, last close, last IP and the last `hello` (client versions and `known`).
- `link_queue`: `(device_id, seq)` primary key; `type`, `tag`, the sealed `body`, `sig`, `exp`,
  `ref` (`d:<delivery id>` for alerts, `pm:<peer message id>` for peer messages) and
  `created_at`.

### 5.2 Enqueue

`link.Enqueue(device, type, tag, ttl, plaintext, ref)`, one write transaction:

1. Device not revoked and has `device_key`; else no-op with `ErrNoLink`.
2. Collapse (5.3).
3. `seq = next_seq`, `next_seq += 1` (the row is created with `next_seq = 1` on first use).
4. Seal (4.2), sign (4.3), insert.
5. Cap (5.5).
6. After commit: wake the device's connection (or pending poll), if any.

### 5.3 Collapse

When `tag ≠ ""`, the hub deletes the device's unacked rows with the same `type` and `tag` before
inserting the new one. The device sees a gap in `seq`, which is normal. Tags match
`[A-Za-z0-9_:.-]{0,64}` (a schema CHECK); `Enqueue` refuses others. Tags:

| Type | Tag |
| --- | --- |
| `alert` | the push tag (push.md 6.5: `wy-s-<session>`, `wy-l-…`, `wy-m-…`, `wy-summary`, `wy-test`) |
| `alert` dismissals | the same tag as the alert they clear |
| `refresh` | `refresh` (only the newest snapshot matters) |
| `matrix`, `revoke` | `matrix`, `revoke` (the newest version contains everything) |
| `peer_status` | `ps:<msg id>` |
| `peer_thread_resume` | `tr:<thread id>` (a second resume of a thread replaces an unsent one) |
| `peer_msg` | `""`: never collapsed |

A collapsed alert's `delivery` row gets `last_error = "collapsed"` and no `delivered_at`.

### 5.4 Expiry

`exp` = enqueue time + the TTL of the type: alerts use push.md 6.4's TTL table; `refresh` 900 s;
`peer_msg` the message's TTL (peers.md 9.3, default 1 h, at most 24 h); `peer_status` and
`peer_thread_resume` 24 h;
`matrix` and `revoke` 30 days. A sweeper (every minute, with the scheduler) deletes rows past
`exp`; a peer message's row expiring sets its `peer_message.status = 'expired'` and queues a
`peer_status` to the sender's machine. Expired rows are never sent.

### 5.5 Caps

Per device: at most **500 rows** and **4 MiB** of `body`. When an enqueue goes over either:

- `refresh`, `matrix`, `revoke`, `peer_status`, `peer_thread_resume`: never dropped (they
  collapse to one per tag).
- `alert`: the oldest alert rows are deleted until it fits, and the next alert carries
  `"missed": n` so the app refetches `/v1/alerts`.
- `peer_msg`: the new message is refused (`peer_message.status = 'failed'`, `detail =
  'queue_full'`), not queued: the sender learns at once.

Hub-wide: 50 000 rows; above that, enqueues of `alert` and `peer_msg` fail with `ErrQueueFull`
and the hub doctor shows it.

### 5.6 Sending and acks

- The hub sends rows in `seq` order, at most **64** past the device's ack (in flight), and
  sends more as acks arrive.
- `{"t":"ack","seq":N}` from the device is cumulative: the hub sets `acked_seq = max(acked_seq,
  N)` (N above the last sent → 4400 `bad_seq`), deletes rows with `seq ≤ N`, and for each
  deleted row with `ref = d:<id>[,<id>…]` sets `delivery.delivered_at` on each (push.md 13.4); `pm:<id>`
  updates are driven by `peer_result`, not by the ack (peers.md 8.4).
- `{"t":"reject","seq":N}` deletes the one row `(device, N)` if it exists and was sent on this
  connection, counts it on the device's link status, and logs a warning; it never changes
  `acked_seq`.
- A device acks a frame only **after** it has processed it durably: the phone after posting
  the notification and storing `last`; the agent after the inbox write or its refusal is
  recorded. A crash between processing and ack means a replay, which the device drops by
  `seq` (4.3) and the notification id dedupes visually (push.md 14.5).
- Devices batch acks: at most one per 500 ms, or one per 16 frames.

## 6. Frame types

### 6.1 Device state

Each device keeps `last` (the highest processed `seq`) durably: the phone in the module's
SharedPreferences (push.md 14), the agent in `<state>/agent/link.json`. It is reset only by
re-pairing.

### 6.2 Device → hub

| `t` | Fields | Meaning |
| --- | --- | --- |
| `hello` | 3.2 | Login. |
| `ack` | `seq` | Cumulative ack (5.6). |
| `reject` | `seq` | A queued frame that failed its checks (4.3); the hub deletes that row only. |
| `action` | `id`, `token` | A notification action tapped on the phone: `token` is the action token from the alert (push.md 8). The hub applies it exactly as `POST /v1/actions/{token}` (same checks, same rate limit) and answers `action_result` with the same `id`. |
| `peer_msg` | `doc` (peers.md 8.2) | A signed envelope, body sealed to the target machine, from a session on this machine to a session on another machine; the hub answers `peer_ack`. |
| `peer_result` | `msg`, `status`, `detail?`, `human_at?` | The receiving agent's verdict on a `peer_msg` it got (peers.md 8.4). |
| `peer_log` | `msg`, `thread`, `from_session`, `to_session`, `kind`, `status`, `detail?`, `size`, `at`, `from_kind?` and `tag?` (a trusted send, peers.md 4.1a); or `event`, `thread`, `detail`, `at` | Metadata of a same-machine delivery, for the audit (peers.md 10). With `event` it is instead this machine's event about a thread: `thread_resume_refused` (`detail` `hub_may_widen`): it refused a `peer_thread_resume` and keeps its stop (peers.md 6.3). The hub records it only when a session of the thread runs on this device's machine. |
| `peers_list` | `id` | Ask for the session directory (peers.md 3); the hub answers `peers` with the same `id`. |
| `peer_tag` | `id`, `session`, `op` (`add` or `rm`), `tag` | Add or remove one tag of a session on this device's machine (peers.md 3.4). The hub answers `peers` with the same `id`: the directory after the change, with `tag` set. |
| `peer_trust` | `op` (`set` or `off`), `sender` (`doorbell`), and with `set`: `account`, `program`, `interpreter`, `venv`, `approved_at` | This machine's trusted-sender record (peers.md 4.1a): `set` on approval and whenever the hub's `trust` answer (6.3) lacks it or holds an older `approved_at`; `off` after `wy peers trust doorbell --off`. The hub keeps one `peer_trust` row per machine and sender (only a strictly newer `approved_at` replaces the row and clears a revocation, so a late report never undoes one) and answers nothing. From a phone, or a revoked device, it is dropped. |

Unknown `t` → the hub answers `notice` `{"code":"unknown_type"}` and carries on (forward
compatibility). A device→hub frame that names a session not on that device's machine is
refused (`notice` `forbidden`).

### 6.3 Hub → device, ephemeral (seq 0, not queued)

The fields below are the JSON object inside `body` (4.1), each with `"v":1`.

| `t` | Fields |
| --- | --- |
| `challenge` | 3.1 (the one frame not in the 4.1 shape: it comes before the device is known) |
| `welcome` | 3.3 |
| `action_result` | `id`, `status` (HTTP-like: 200, 400, 401, 404, 410, 429), `applied`, `alert` (AlertBrief) |
| `peers` | `id`, `sessions` (the other machines' sessions, peers.md 3.2 without `can`, with `machine_id`; each with its canonical `handle` and, when it has any, `aliases`), `mine` (the asking machine's own sessions in the same form, so its agent shows and signs the canonical handles; omitted when none), `certs` (device id → `device_cert` document of each machine's agent that a listed session runs on, and of the asking device itself, so its agent can tell that receivers will refuse it while its certificate has no owner signature; peers.md 3.2 `blocked`, 8.2). Each session may carry `tags` (peers.md 3.4, omitted when none). `trust` (omitted when the hub has no row): `{"doorbell":{"approved_at","revoked_at?"}}`, the asking machine's trusted-sender rows (peers.md 4.1a); the agent deletes its record when the row is revoked and its `approved_at` is the record's or newer, and reports a record the hub lacks or holds an older approval of. The answer to a `peer_tag` also carries `tag`: `{"session","tags","changed","error?"}`, the session's tags after the change, whether it changed anything, and a refusal word (`bad_tag`, `too_many_tags`, `forbidden`: not a session of the asking machine, `unknown_session`: not a live session the hub knows) |
| `peer_ack` | `msg`, `status` (`queued` or `refused`), `detail?` (the reason word, or `offline`), `exp?` (peers.md 8.2, 8.3) |
| `notice` | `code`, `message`: `unknown_type`, `forbidden`, `rate_limited`, `queue_full`, `draining` (hub shutting down; reconnect in 5–15 s) |

Ephemeral frames are signed (4.3) like queued ones; their body is not sealed (they answer the
device that asked, on its TLS connection, at once, and hold nothing a session record does not).

### 6.4 Hub → device, queued (sealed body)

| `t` | Devices | Body (plaintext JSON) |
| --- | --- | --- |
| `alert` | phones | The push.md 6.5 payload, as built for the device's `link` target (push.md 13.2). `type` inside is `alert`, `summary`, `test` or `dismiss` (`{"v":1,"type":"dismiss","tag","alerts":[…]}`: clear the notification; sent when an alert is seen or resolved, push.md 13.3). |
| `refresh` | phones with widgets | `{"v":1,"needs_you","seats":[{"seat","window","used_pct","resets_at"}],"ts"}`: the widget snapshot (push.md 13.5). |
| `peer_msg` | machines | `{"v":1,"doc","cert","accounts"}` (peers.md 8.3): the sender's envelope, as the sender signed it (its own seal to this machine inside), the sender's certificate, and the sender's owner-signed `machine_accounts` document (keys.md 4.3). |
| `peer_status` | machines | `{"v":1,"msg","status","detail","at"}`: the final state of a message this machine sent. |
| `peer_thread_resume` | machines | `{"v":1,"thread","by","at"}`: the owner resumed a thread the loop guard stopped (peers.md 6.3); `by` is the device that asked. |
| `matrix` | machines (including phones) | The newest matrix document, wire form (keys.md 4.1). |
| `revoke` | machines | The newest revocation document, wire form (keys.md 6). |

A signed document inside a frame is verified twice: the frame by the hub key, the document by
its own signer (keys.md 5). A `matrix` frame whose document is hub-signed and widens is
accepted only under `hub_may_widen` (keys.md 5.4); so is a `peer_thread_resume`, which carries no
signature but the frame's (the hub alone makes it).

## 7. Revocation and timing

- `DELETE /v1/devices/{id}` (or an owner-signed revocation) deletes the device's queue and
  `link_state`, closes its link at once with **4403**, and queues `revoke` to every machine.
  `wy hub token revoke` runs in its own process and only writes the database: the running hub
  closes that link at its next sweep (within a minute, so still inside one ping). The phone, on 4403, stops the service and shows "This phone was removed from
  the hub" (push.md 14.4). An agent on 4403 stops and `wy agent status` says why.
- A connected machine receives new `matrix` and `revoke` frames immediately; one that is
  reconnecting receives them when it logs in, and `known` (3.2) catches anything missed.
  "Within one ping" is the upper bound for a live connection that dropped
  silently: the device notices within `ping_s` and reconnects.
- The hub, while revoking, also refuses any link login by the device (3.2 step 2): there is no
  window where a revoked device can reconnect and receive.

## 8. Close codes

| Code | Word | Sent when | Device does |
| --- | --- | --- | --- |
| 1000 | normal | The device closed (app stopped, agent stopping). | — |
| 1001 | going_away | Hub shutting down. | Reconnect with backoff. |
| 4400 | bad_frame | Malformed JSON, binary message, `bad_seq` (an `ack` above the last sent). | Reconnect with backoff; repeated → stop and report. |
| 4401 | unauthorized | Bad or missing `hello`, unknown nonce, signature, `no_keys`, `bad_cert`, 5 s login timeout. | Reconnect with backoff; three in a row → stop, show "Hub does not recognise this device's key". |
| 4403 | revoked | The device is revoked. | Stop; do not reconnect. |
| 4408 | idle | Nothing from the device for `2 × ping_s + 30` s. | Reconnect. |
| 4409 | superseded | Another connection of the same device logged in. | Do not reconnect from this connection. |
| 4413 | too_big | A message over 1 MiB. | Reconnect with backoff. |
| 4426 | upgrade | The hub needs a newer client (`hello.v` or `client` too old). | Stop; show "Update the app" / "Update wy". |
| 4429 | rate_limited | Too many logins. | Wait at least 60 s. |
| 4500 | internal | Hub error. | Reconnect with backoff. |
| 4503 | frozen | The hub is frozen (`hub.frozen`, machines.md 8). | Reconnect after 60 s. |

Backoff for every reconnect: 1, 2, 5, 10, 20, 30, 60 s, then 120 s, each ±20 % jitter; reset
after a connection that lasted 60 s. A network change resets it to zero (push.md 14.4).

## 9. Hub side

- `internal/hub/link`: the handler, one goroutine per connection (reader) plus a writer fed by
  a channel; `Enqueue`; the sweeper. Uses the hub key loaded at start from
  `<state>/hub_sign.key`.
- Connection state for the board: `GET /v1/devices` gains `link: {connected, transport,
  connected_at, last_seen_at, queued, acked_seq, last_close}` per device.
- Logs one line per login and close (device, transport, code), never frame bodies.
- `wy hub doctor`: a device disconnected for more than 1 h with queued alerts (warning); a
  queue at more than 80 % of its cap; `hub_sign.key` missing or not 0600.

## 10. Tests

- **Goldens** in `internal/link/testdata/` (shared with the Kotlin tests, push.md 14.10), made
  by `go test ./internal/link -update` from fixed seeds: the hub Ed25519 seed `0x01…01`, a
  machine's Ed25519/X25519 seeds `0x02…`/`0x03…`, a phone's P-256 key from RFC 8291 appendix A
  (`ua_private`) and its auth secret. One JSON file per case,
  `{"name", "keys": {…public keys…}, "fields": {…}, "signed_hex", "frame"}`:
  `challenge`, `hello-machine`, `hello-phone` (ECDSA is randomised: the file holds one valid
  signature to verify, not to reproduce), `welcome`, `alert-phone` (RFC 8291 with the
  appendix's fixed salt and `as_private`, so byte-exact), `matrix-machine` (HPKE is
  randomised: a ciphertext to open), `action-result`, plus `bad-*` variants (one changed byte in
  each signed field, a wrong device id, `seq` 0 on a queued type) that must be rejected.
- Signature inputs: every `signed_hex` rebuilt from `fields` byte for byte.
- HPKE seal/open round trip, wrong device → fail; RFC 8291 path reuses webpush's vectors.
- Queue: seq monotonic; collapse by tag (gap visible); expiry sweeper; caps (alert eviction
  with `missed`, `peer_msg` refused); cumulative ack; replay after reconnect from `last`;
  implicit ack via `hello.last`; `bad_seq` on an ack above the last sent; resync after a
  restored backup (renumbered, re-signed, nothing lost); `reject` deletes one row only.
- Tamper: a `link_queue` row with a changed body, tag, seq or exp → the test client rejects
  it, sends `reject`, and its `last` does not move; a row inserted with seq 10^9 and a bad
  signature does not block later frames; a `device_key.sign_pub` or `box_pub` changed in the
  database → login refused (`bad_cert`) or ignored (the certificate's key is used); a
  challenge replayed after `exp`, or a hello replayed with a used nonce → 4401; no ack is
  accepted on a connection that has not logged in.
- Revocation: the open link closes with 4403 within the test's clock step; the queue is gone;
  machines receive `revoke`.
- Long-poll parity: the same scripted session over WebSocket and over poll gives the same
  frames and the same final state.
- Hub restart mid-stream: the client replays from its `last` with no loss and no duplicate
  processing.
- Only the two link routes (`/v1/link`, `/v1/link/poll`) answer without a bearer, checked
  against the route table.
