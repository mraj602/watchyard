# Tokens and devices

Status: spec v1. Implemented in `internal/hub/auth`. Pairing is in pairing.md.

Every device that talks to the hub has its own token. The hub stores only a hash. A token is
revoked on its own, without touching any other device.

## 1. Format

```
wy_<k>_<43 base62 characters>
```

| Part | Meaning |
| --- | --- |
| `wy_` | Fixed prefix, so secret scanners and `grep` can find leaked tokens. |
| `<k>` | One letter for the device kind: `m` machine, `d` desktop, `p` phone, `b` browser, `o` owner session. |
| `_` | Separator. |
| 43 base62 characters | 32 random bytes from the OS cryptographic source, encoded big-endian in base62 (`0-9A-Za-z`), left-padded with `0` to 43 characters. |

Example shape: `wy_m_4fQx0Lr2…` (48 characters in total). Base62 has no `-` or `_`, so a
double click selects the whole token and the kind letter can always be parsed.

The kind letter is a hint for people and for `wy doctor`. The hub never trusts it: scopes
come from the `device` row.

## 2. Storage

- The hub stores `device.token_hash` = SHA-256 of the full token string (including the
  prefix), as 32 raw bytes, with a unique index. Lookup is one indexed read.
- Exception, owner sessions (`wy_o_`, pairing.md 4): `token_hash` = HMAC-SHA256(`pair_pepper`,
  `"token:"` ‖ token), with the pairing pepper that lives outside the database (pairing.md 3).
  A `wy_o_` token is looked up only by that hash, and a device with the `owner` scope (or
  `created_by` `owner-link:…`) is accepted only through a `wy_o_` token. So write access to
  `hub.db` alone cannot mint an owner session (keys.md 5.4): a row it writes either has a
  plain hash (refused for the owner scope) or needs the pepper. Owner sessions made before this
  rule are revoked by migration 7.
- No salt or slow hash: the token has 256 bits of entropy, so a stolen hash cannot be
  brute-forced. (A slow hash is used only for the owner password.)
- Comparison is done by looking up the hash, so no timing-sensitive string compare is
  needed.
- `device.token_hint` = kind prefix + `…` + last 4 characters (`wy_m_…k3Qz`), for lists.
- The token is shown once, when created. It cannot be recovered; a lost token is replaced.

On a client the token lives in `<state>/token`, mode `0600`, one line, no trailing data
(see [paths.md](paths.md)). It is never put in `config.toml`, `settings.json`, hook
commands, environment variables of Claude Code, or records.

## 3. Scopes

| Device kind | Token | Scopes | Can |
| --- | --- | --- | --- |
| machine | `wy_m_` | `write` | `POST /v1/ingest` for its own machine id only. Read `GET /v1/usage`: each seat's limit windows, recent samples and live session count, the places and signed-out folders of its own machine only, and the leases other machines' `wy account next` recorded (times, charges, whether the session started or ended, token units used; no session id, no machine), and each seat's calibration factors (meter factor, ratio, lane price and how far they are measured) (setup.md 8.5), so `wy account next` works on every connected machine and counts the other machines' lanes. This is acceptable for a machine token because it is the owner's own seat usage, percentages, times and counts, with no email, no content, no session ids and nothing naming projects or other machines (the calibration factors are numbers derived from those same readings and token counts). Also, by design (keys.md 3.3, peers.md 5.4): propose an owner key (`POST /v1/keys/owner`; it stays pending until an owner confirms its fingerprint, at most one pending, deleted after 24 hours), and narrow the peer matrix or pause peers (`PUT /v1/peers/matrix`, `POST /v1/peers/pause`; widening and resuming need the owner). |
| desktop | `wy_d_` | `write`, `read` | Ingest for its own machine id (web usage); every read endpoint and `/v1/stream`. |
| phone | `wy_p_` | `read`, `push` | Every read endpoint and `/v1/stream`; register push targets; snooze/seen alerts. |
| browser | `wy_b_` | `read`, `push` | As phone; for the installed web app on a computer. |
| owner session | `wy_o_` | `owner`, `read`, `push` | Everything, including devices, rules and pairing codes. A `browser` device from `wy hub owner-link`, 30 days, cookie only (pairing.md 4). |

Scope checks per endpoint are in [api.yaml](api.yaml) (`x-scopes`). A request whose token is
unknown or revoked gets `401`; a valid token without the scope gets `403`. Both bodies are
`{"error": "...", "code": "unauthorized" | "forbidden"}` and never say whether a token
exists.

`machine` and `desktop` devices are bound to one machine id (`device.machine_id`). Ingest
rejects records whose envelope `machine` differs (`machine_mismatch`).

Pulling over SSH (pull mode) needs **no `relay` scope**
(see [machines.md](machines.md) 3.4): the hub itself runs the pull and ingests in-process, as a
**pull device** (`kind = machine`, bound to the pulled machine, `created_by = pull:<target>`)
whose token is generated, hashed and discarded. No token can relay records for another
machine, and the remote holds no token at all.

`device.last_used_at` is updated at most once a minute per device, to avoid a write per
request.

## 4. CLI

All run on the hub machine, against its database directly (no HTTP), so they work before
any token exists.

```
wy hub token create --kind machine|desktop|phone|browser --name <name> [--machine <machine id>] [--json]
    Creates a device and prints its token once.
    --machine is required for machine and desktop (the machine row is created if missing,
    named after --name).
    --json prints {"device": "dev_…", "token": "wy_m_…", "scopes": [...]}.

wy hub token list [--all] [--json]
    Devices: id, kind, name, machine, hint, scopes, created, last used, revoked.
    Revoked devices only with --all.

wy hub token revoke <device id | token hint | name> [--yes]
    Sets revoked_at. Takes effect on the next request (no cache). Asks unless --yes.
    Ambiguous names are refused with the list of matches.
```

On a client:

```
wy connect <hub url> --token <token>     # paste a token made by `wy hub token create`
wy connect <hub url> --code 482-913      # claim a pairing code instead
wy connect <hub url> --code-stdin        # code or pairing secret on stdin (machines.md 4.2)
wy connect <new hub url> --keep-token    # after a hub move, keep the token (machines.md 8.3)
```

`wy connect` checks the URL (HTTPS required unless the host is loopback, or
`--insecure-lan`), calls `GET /health`, then sends one `slot_info` per slot to prove the
token works, and writes `<state>/token` and `hub.url` in `config.toml`.

In merged mode `wy setup` creates the local machine's device itself.

## 5. Pairing codes and owner sessions

Superseded by [pairing.md](pairing.md): codes are 6 digits plus a 128-bit QR secret, hashed
with a pepper kept outside the database, valid 10 minutes, single use, at most 5 live;
`POST /v1/pair/claim` with per-IP and hub-wide limits on typed codes; the QR is an `https://`
URL (`<hub>/#/pair?s=…&c=…`), not JSON. Owner sessions (`wy_o_`) are `browser` devices with
scopes `owner`, `read`, `push`, made from a one-time link (`wy hub owner-link`) and expiring
after 30 days (`device.expires_at`, schema v3).
