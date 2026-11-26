# Analytics

Status: spec v1. The analytics reports live on the hub. Implemented in `internal/hub/api/analytics.go` and
`internal/hub/store/analytics.go`; shown by the web board (`webui/static/views/analytics.js`,
`#/more/analytics`) and the phone app (More, Analytics). The shapes are in
[api.yaml](api.yaml) (`GET /v1/analytics/{report}`, scope `read`).

## 1. What it measures

Token reports come from `token_row`, the API responses found in transcripts (record.md). They
are exact counts for Claude Code only (no chat or cowork). `api_usd` prices the counters at API
list prices (`internal/scan/pricing.go`, longest model prefix first; cache writes cost 1.25x
(5 minute) and 2x (1 hour) the input rate). It is a common yardstick, not a bill: subscriptions
are not charged in dollars. A model with no known price counts 0 and its replies are in
`unpriced_replies`.

Limit reports (`limits`, `hits`) come from `usage_window` and `limit_hit`, the official numbers
claude.ai and Claude Code gave, only while something was collecting them.

## 2. Query

`GET /v1/analytics/{report}?from=&to=&days=&seat=`

| Param | Meaning |
| --- | --- |
| `from`, `to` | Days, `YYYY-MM-DD`, both inclusive, in the hub's time zone. `to` defaults to today, `from` to the day that makes the range `days` long. At most 366 days. |
| `days` | Length of the range when `from` is not given (1-366, default 30): `?days=7` is the last 7 days ending today in the hub's zone, so a client need not know the zone. |
| `seat` | One seat id, or `unattributed` for rows with no seat (their session's seat was not known). Default: every seat. |

Errors, as everywhere (`{"error": message, "code": ...}`): `404 not_found` for a report not in the
list below or a `seat` that does not exist; `400 bad_request` for a `from` or `to` that is not a
date, `days` not 1-366, `from` after `to`, or a range over 366 days. Nothing else is rejected: unknown query
parameters are ignored.

One report runs at a time. The others wait for their turn without holding a database connection, so
auth, ingest and the board's own reads and settings changes never queue behind a report. A report
that has not finished 45 s after it arrived (waiting included) answers `503 unavailable`.

Every reply starts with `report`, `from`, `to`, `tz` and `seat` (the filter, null for all), so a
client can show the range the hub chose.

## 3. Days and time zone

A day is a day of the hub's time zone: the `hub.tz` setting, else the hub machine's own zone,
else UTC (the zone the alert rules use, push.md 2), or the zone the client names with `?tz=` (an IANA name; the board
passes the viewer's, so "When you work" is in local hours). `tz` in the reply says which. Token rows are summed in 15-minute
buckets and the buckets assigned to days and hours, so zones such as Asia/Kolkata (UTC+5:30) and
daylight saving changes come out right. `rollup_daily` is not used: it fixes the zone when it is
built, and the queries are cheap over `token_row`'s indexes.

## 4. Reports

| Report | Reply | Notes |
| --- | --- | --- |
| `daily` | `totals`, `days`, `seats` | `days` has every day of the range, zero-filled. `seats` is the total per seat, dearest first. |
| `models` | `models` | Per model id with a display label (`Opus 4.6`), dearest first. |
| `projects` | `projects` | Per project path, sessions and last activity, the 50 dearest. |
| `heatmap` | `api_usd`, `replies` | 7 weekdays (0 = Sunday) by 24 hours of the hub's time zone. |
| `sessions` | `sessions` | Per session: seat, project, models, first and last reply, the 50 dearest. |
| `limits` | `windows` | `usage_window` rows that reset inside the range: peak, final, unused percent. |
| `hits` | `hits` | "You've hit your limit" replies, one per seat, type and reset time; `wait_s` is the time to the reset. |

Every counter row is `input`, `output`, `cache_5m`, `cache_1h`, `cache_read`, `replies`,
`api_usd`, `unpriced_replies`. Lists are never null. Ties in cost sort by name or id so the
order is stable. The board's and the phone's account pickers list accounts in the owner's
order (`seat.order`, pwa.md 3.0), not by cost.

**History.** A `daily` day that is before the first `token_row` of the hub (any seat) and has
Claude Code's own totals from `history_day` (the history import) has `source: "history"`: only
`total_tokens` and `replies` (its message count) are set, the cost is 0, as the file has no
split by counter and no usage in dollars. Every other day has `source: "transcripts"`.

## 5. Not here

Pace and "which account next" are the overview's `pace` and the
Accounts screen, not a report here. Per-day limit consumption, credits, surface splits and hook activity. They
are not in these reports.

## 6. The `wy analytics` command

```
wy analytics [daily|models|projects|heatmap|sessions|limits|hits] [--days N] [--from D] [--to D] [--seat S] [--json]
```

Read only; the default report is `daily`. `--days`, `--from`, `--to` and `--seat` are the query
of section 2. Text mode prints the range line (`from`, `to`, `tz`, seat), then plain aligned
tables of the hub's numbers (nothing is recomputed, cost included): `daily` a totals line, one
row per day and one per seat; `heatmap` a 7 by 24 block of shade characters. Times (`LAST`,
`RESETS`, `WHEN`) print as `YYYY-MM-DD HH:MM <zone>` in the range line's `tz`, not raw UTC. `--json` prints
the hub's reply, indented.

It needs the `read` scope. This machine's own token (`wy connect`, kind `machine`) has `write`
only (tokens.md 3) and is never sent to this endpoint; the command uses the board token that
`wy board connect` saves (terminal.md T3), and says so when there is none. Exit codes: 0; 1 any
other failure (a 400 or 404 shows the hub's message); 2 usage; 3 the hub refused the token (401,
403); 4 no `hub.url` or no board token; 5 hub unreachable.
