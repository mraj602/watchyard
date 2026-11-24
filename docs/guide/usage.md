# Usage limits and analytics

Watchyard shows every account's plan limits in one place (the board's Accounts screen, the
Android app, the desktop app's strips and the widgets) and keeps their history on the hub. It
also counts Claude Code tokens per account, model and project. This guide explains where the
numbers come from and how to read them. Specs: [analytics.md](../spec/analytics.md),
[record.md](../spec/record.md).

## Where limit numbers come from

There are two sources. Neither is required; with both, the hub merges them per account.

| Source | How | Calls claude.ai? | On by default |
| --- | --- | --- | --- |
| Claude Code status line | `wy statusline` reads the `rate_limits` that Claude Code itself hands to the status line command | No | Yes, after `wy setup` |
| Desktop app usage reading | A script in each signed-in claude.ai tab reads the numbers claude.ai shows under Settings, Usage | Yes, undocumented endpoints | No: off per account until you accept a warning |

### The status line (local)

`wy setup` wraps your status line: Claude Code runs `wy statusline --wrap '<your old
command>'`, which prints your old status line unchanged and records the limits Claude Code
passes in (at most one sample a minute per session). Nothing extra is sent to Anthropic. This
only updates while a Claude Code session of that account is running. `wy setup
--no-statusline` skips it; `wy uninstall` unwraps it.

### The desktop app (opt-in, may break)

The desktop app can read usage inside each claude.ai tab every 5 minutes, which also covers
accounts you use only on claude.ai (chat, Cowork). It calls claude.ai's internal endpoints
(`/api/organizations` and `/api/organizations/{uuid}/usage`) from the signed-in tab. Before it
does, it shows this warning, once per account:

> **Read usage for this account?**
> Every 5 minutes, Watchyard runs a small script inside this tab that reads the same numbers
> claude.ai shows under Settings → Usage. It reads numbers only: no chats, no cookies. They go
> to your own hub.
> This uses an internal claude.ai endpoint, not a public API. It may stop working, and
> automated access may not be allowed by Anthropic's consumer terms, so it could get this
> account flagged. Watchyard is unofficial and not affiliated with Anthropic.
> You can turn reading off for this account at any time in its usage bar or in Settings.

The endpoints are undocumented and can change or disappear at any time. A 401, 403 or 429
answer stops reading for that account until you resume it; other errors back off up to an
hour. You decide whether to enable it; the status line alone is enough for Claude Code use.
Details: [desktop.md](desktop.md#usage-strip-and-usage-reading).

## Windows

| Window | Meaning |
| --- | --- |
| 5-hour | The rolling session window; resets 5 hours after it started |
| 7-day | The weekly window across all models |
| Model weeks | Weekly windows that apply to one model family, when your plan has them |

Each window shows its percentage and reset time. A window claude.ai has not started yet
reads "0% · not started"; one whose reset time has passed reads "0% · reset at ..." until a
new sample arrives. The hub keeps every window instance with its peak and what was unused at
reset.

## The Accounts screen

The board's Accounts screen (and the Android app's) lists every account, in an order the
owner can drag. For each: its windows, reset times and pace. Pace compares use so far with
even use over the window, and projects where the window will be at reset. The projection
follows how fast you are burning now (the slope of the last hour's readings, when there are
enough of them), so a burst late in a window shows up; with too few recent readings it uses the
window's average rate. The line says which: "at the last hour's pace" or "at this week's
average pace".

The line **Most room right now** names the account with the most room left. It is a
suggestion only. Watchyard never switches or rotates accounts on its own, and never moves a
session to another account to get around a limit: you choose. Use only your own,
legitimately held accounts, within the terms of your plan; advertised limits assume ordinary
individual use. See [faq.md](faq.md).

## Limit alerts

The hub alerts when a 5-hour or 7-day window reaches 80 % and 95 %, and optionally when a
window that passed 80 % resets. A rate-limit failure in a session alerts at once with the
reset time and the account that has room. Rules and channels: [alerts.md](alerts.md).

## Analytics

The hub also counts tokens from Claude Code transcripts (`wy scan` reads them on each
machine, from the timer). Reports cover tokens, an API-equivalent cost, models, projects, a
weekday-by-hour heatmap, the biggest sessions, every limit window and every "you've hit your
limit" reply.

The **API-equivalent $** prices tokens at Anthropic's API list prices. It is a yardstick for
comparing accounts and days, not a bill: subscription plans are not charged in dollars.
Token counts cover Claude Code only, not chat or Cowork. Days follow the hub's time zone.

### On the board

More, **Analytics** (`#/more/analytics`): range chips (7, 30, 90 days), an account filter,
totals, daily cost, by model, by project, the heatmap and limit hits. Any signed-in device
can read it, including a read-only phone.

### In a terminal

`wy analytics` needs a read token. A machine's own token can only send records, so first pair
the terminal board once:

```sh
wy board connect            # one click on a local or admin-reachable hub
# or, with a desktop pairing code from the board (More, Devices) or `wy hub pair --kind desktop`:
wy board connect --code-stdin
```

Then:

```sh
wy analytics                          # daily (the default)
wy analytics models --days 7
wy analytics projects --from 2026-10-01 --to 2026-10-21
wy analytics heatmap --seat work
wy analytics limits --json
```

Reports: `daily`, `models`, `projects`, `heatmap`, `sessions`, `limits`, `hits`. Flags:
`--days N`, `--from D`, `--to D`, `--seat S` (an account), `--json`. Times print in the hub's
time zone, which the range line names.

## Keeping or trimming history

The hub keeps raw records by default. To keep less, on the hub:

```sh
wy hub prune --older-than 90d --dry-run     # count first
wy hub prune --older-than 90d
```

Backups and moving the hub are in [maintenance.md](maintenance.md).
