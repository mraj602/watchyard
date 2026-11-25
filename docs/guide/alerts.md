# Alerts and notifications

The hub decides when something deserves your attention and sends it to every place you set
up: the Android app, the web app (Web Push), ntfy and the desktop app. This guide covers what
fires an alert, where it goes and how to tune it. The full rules are in
[push.md](../spec/push.md).

## How an alert travels

```mermaid
sequenceDiagram
    participant CC as Claude Code session
    participant WY as wy hook (machine)
    participant OB as Outbox (machine)
    participant HUB as Hub
    participant PH as Android app / web app
    CC->>WY: Notification hook (permission prompt)
    WY->>OB: append one record
    OB->>HUB: wy flush (POST /v1/ingest)
    HUB->>HUB: session state = needs_permission
    HUB->>HUB: alert due 30 s later (rule delay)
    alt you answer within 30 s
        HUB->>HUB: alert cancelled, nothing sent
    else still waiting
        HUB->>PH: push over the app's link, Web Push or ntfy
        PH->>HUB: Seen or Snooze (optional)
    end
```

1. Claude Code runs the Watchyard plugin's hook on each event. `wy hook` writes one record to
   the local outbox and returns at once; it never blocks or answers for you.
2. `wy flush` sends the outbox to the hub (right after the hook, and every minute from the
   timer).
3. The hub folds the records into each session's state. When a session enters a state that
   needs you, the hub creates one alert for that episode, due after the rule's delay.
4. If the state changes before the alert is due (you answered), the alert is cancelled.
   Otherwise it is sent to every enabled target whose device has not muted that trigger.

Each episode alerts at most once, so restarts and replays never send duplicates.

## Triggers and default rules

| Trigger | When | Default |
| --- | --- | --- |
| `needs_permission` | A session waits on a permission prompt | On, after 30 s |
| `asked` | A session asked a question, opened a dialog or has a plan ready | On, after 60 s |
| `failed` | A turn stopped with an error (rate limit, login, billing) | On, at once, goes through quiet hours |
| `limit_80` | An account's 5-hour or 7-day window reached 80 % | On, at once |
| `limit_95` | The same window reached 95 % | On, at once |
| `reset` | A window that passed 80 % has reset | Off |
| `machine_silent` | A machine with live sessions sent nothing for an hour | On |
| `limit_change` | A model's 5-hour capacity moved more than 1.2× (or under 0.83×) against last week on most accounts that mostly run Claude Code | On, low urgency |
| `usage_reset` | Anthropic or the org owner reset an account's usage: both windows back to about 0 % in several sessions | On, low urgency |
| `seat_change` | A config folder logged in to another account or org | On, low urgency |
| `weekly_drop` | An account's 7-day use fell more than 10 points without a reset: something changed, check | On, low urgency |
| `session_moved` | The Smart Account Switch on a machine moved sessions to another account (at most one push per 5 minutes per machine) | On, low urgency |
| `switch_digest` | A machine's daily Smart Account Switch digest (moves, limit hits, weekly room) | On, low urgency |
| `drain_started` | An account you set to drain started draining (until its end time) | On, low urgency |
| `drain_ended` | The drain ended: its end time came or the account reached 97% of its week | On, low urgency |
| `drain_pulled` | The switch on a machine pulled sessions into a draining account (batched like `session_moved`) | On, low urgency |

"Turn finished" is not an alert: the board shows it. Limit alerts use the highest value a
window reached, so a window that dips back under 80 % does not alert again. See
[usage.md](usage.md) for where limit numbers come from.

### Sessions talking to each other

When sessions coordinate through [peers](peers.md), a turn often ends with a question that
is meant for the other session, not for you. One example is "Want me to pick up the
migration too?" sent back to the session that asked. Another is a session saying "Sent the
schema to api; waiting for its answer." These don't push. The board still shows the session
as asked, and **Recent pushes** lists the alert as skipped with the reason `peers`. A turn
counts as coordination when either of these holds:

- another session's message started the turn (it arrived as a `<cross-session-message>`, or
  within 10 seconds of a delivery into that session);
- the turn ended while the session waits on a reply: it sent the newest message of an open
  peer thread after the last prompt you typed in it, and the loop guard has not stopped that
  thread.

Anything aimed at you still alerts during such a turn: a permission prompt, an
AskUserQuestion dialog, a plan to approve, a dialog from an MCP server, and a failure. Once
you type in the session, its next turn is yours again and alerts as usual. A thread stopped
by the loop guard needs you, so it is not treated as coordination.

To get these pushes anyway, set `skip_peers` to `false` in the hub's push settings
(`PUT /v1/push/settings`; it is on by default). Direct ntfy pushes from a machine
([machines.md](../spec/machines.md) 5) follow the same setting.

## Changing the rules

Rules are global and belong to the owner. Open the board signed in as owner
(`wy hub owner-link` on the hub prints a one-time sign-in link), then **Alerts**:

1. Each trigger has a switch and a delay. Change them in place.
2. Add an override to treat one account, project or machine differently (for example: no
   `asked` alerts for project `scratch`, or a 5-minute delay for one account).
3. Under the list, **Recent pushes** shows every alert with its outcome: pushed, seen,
   snoozed or cancelled. Cancelled means you answered before it was due.

A phone cannot change global rules. Its own Alerts screen has a switch per trigger that mutes
that trigger on that phone only.

**Categories.** Each trigger belongs to a category: Sessions (needs you, asked, permission,
failed), Limits (80%, 95%, reset), Account switch (moves and the digest), Drain (started, ended,
pulled), Usage notices and Machines (silent machine). In the Android app's Notifications screen,
and in the web app's Settings, each category has an on/off switch for that device: off means the
hub sends nothing of it there. In the Android app each category is its own notification channel,
so its sound, vibration and lock-screen display are set in Android's settings; the **Sound…**
button next to a category opens that channel's page. ntfy has no such channels: only the on/off
switch applies.

## Channels

| Channel | Who reads the text | Set up in |
| --- | --- | --- |
| Android app (its own link to the hub) | Only the app: sealed to a key on the phone | [android.md](android.md) |
| Web Push (the web app on a phone or desktop browser) | Only the subscribed browser; the push service sees ciphertext | Below |
| ntfy | Your ntfy server, or ntfy.sh and anyone who knows the topic | Below |
| Desktop app | Local OS notifications from the hub's live stream | [desktop.md](desktop.md) |

The hub sends Web Push and ntfy as outbound HTTPS requests; no inbound port is needed for
pushes. The Android app uses no push service at all.

### Web Push (the web app)

The board installs as a web app on a phone. This is also the way to get notifications on an
iPhone, since there is no native iOS app.

1. Give the hub an `https://` address (see [install.md](install.md)); browsers allow push
   only in a secure context.
2. On the hub: `wy phone pair --url https://laptop.example.ts.net`. It prints a QR and a
   6-digit code.
3. Scan the QR with the phone's camera.
   - Android (Chrome): the page offers **Install app**.
   - iPhone or iPad (iOS 16.4 or later): Safari, Share, **Add to Home Screen**, then open the
     installed app and enter the code. Notifications work only in the installed app.
4. In the web app: Settings, **Turn on notifications**, then **Send test push**.

On iOS the notification has no action buttons; Snooze and Seen are inside the app.

### ntfy

ntfy suits machines and phones where you already use the ntfy app.

1. Signed in as owner, open Settings on the board and add an ntfy target. Leave the topic
   empty and the hub makes a long random one (`wy-` and 24 characters).
2. Subscribe to that server and topic in the ntfy app.
3. Set the hub's public URL first; without it ntfy alerts have no link and no Snooze or Seen
   buttons.

On ntfy.sh, topics are public: anyone who learns the name can read the messages. For that
reason **private pushes are on by default for ntfy.sh targets** (see below). A self-hosted
ntfy server avoids this.

## Snooze, Seen and batching

- **Seen** marks the alert read on every device. It does not answer the session.
- **Snooze** re-sends the alert after 15 minutes (the owner can set 5 to 240 minutes) if the
  session still needs you. The re-push starts with "Still waiting".
- Session alerts due within 2 seconds of each other go out as one push ("3 sessions need
  you").
- A flood cap allows at most 6 pushes per target per 10 minutes; the rest are merged into one
  summary.
- An alert that would arrive more than 15 minutes late (a laptop that slept, records flushed
  hours later) is not pushed. The board still shows the session.

## Quiet hours

Quiet hours hold pushes until the window ends. Rules marked as bypassing quiet hours still
go out; only `failed` is marked that way by default. Quiet hours are off until you turn them
on, and the default window is 23:00 to 08:00.

There are two places to set them:

- Hub-wide: on the board, Alerts, owner only. This is the hub's own setting.
- Per device: in that device's Settings (the phone app or the board's Settings). With "Own
  quiet hours" on, the device uses its own window. With it off, it follows the hub.

The hours are read in a time zone. If none is set, the hub's zone is used, then UTC.

When the window ends, held alerts that are still open go out as one push if there are
several ("3 sessions need you"), or as the alert itself if there is one. The settings API
also has a `critical: false` flag that holds `failed` too. The board and the phone app have
no control for it.

## Private pushes

With private pushes on, the lock screen shows only "Watchyard" and "1 session needs you",
"An account is near its limit" and similar. No project, account or session name appears in
the text or the link; the details load when you open the app. Turn it on per device in
Settings, or as the hub default (owner).

What a non-private push contains: the account label, the project folder name, the trigger and,
when last messages are allowed, the question Claude asked (cut short). See
[security.md](security.md) for what the hub stores.

## Direct push from a machine

A server that cannot reach the hub (the hub is on a laptop that sleeps, or the server is only
reachable over SSH) can send ntfy pushes itself when a session needs you and the hub has not
heard from it.

```sh
wy direct-push on --server https://ntfy.sh --after 2m --secret-stdin
# stdin: {"topic": "wy-...", "token": "tk_..."}   (token optional)
wy direct-push test        # sends a test message
wy direct-push status
wy direct-push off
```

Use the same topic as a hub ntfy target so your phone already listens. The topic is read from
stdin (or prompted at a terminal) and stored in the state folder with mode 0600; it never goes
on the command line. When the hub catches up it sees that the push was already sent and does
not repeat it. Only ntfy works this way: Web Push and the Android link need keys that stay on
the hub. Details: [machines.md, section 5](../spec/machines.md#5-direct-ntfy-from-wy).

## Checking delivery

- The board's **Alerts** page lists recent pushes and their outcome per target.
- `wy doctor` on the hub checks the public URL, the Web Push keys, that at least one device
  can receive pushes, and the last delivery error.
- On Android, Settings, Notifications shows the link state; see [android.md](android.md).
- More in [troubleshooting.md](troubleshooting.md).
