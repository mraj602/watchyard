# FAQ

## Is Watchyard made by Anthropic?

No. Watchyard is an independent, unofficial, community project. It is not affiliated with,
endorsed by, sponsored by or connected to Anthropic. "Claude" and "Claude Code" are trademarks
of Anthropic, PBC, used here only to describe compatibility. Watchyard uses no Anthropic
logo.

## Is it allowed to use it?

That depends on your plan's terms, and you are responsible for that decision. Not legal
advice; what Watchyard does:

- **Claude Code integration** uses documented extension points only: hooks, the status line,
  `CLAUDE_CONFIG_DIR`, plugins, `claude auth status` and the session list Claude Code itself
  provides. Logging in is always Claude Code's own `claude auth login`.
- **Account tabs** (desktop app, and claude.ai tabs in the Android app) load claude.ai's own
  pages. You sign in on Anthropic's page; the sign-in stays in a local browser session on that
  device. Watchyard never reads, exports or sends cookies or tokens, and the hub never sees
  them.
- **Usage reading in the desktop app** is the exception: it reads claude.ai's undocumented
  usage endpoints from the signed-in tab every 5 minutes. Anthropic's consumer terms restrict
  automated access, so this could get an account flagged, and the endpoints can change or
  break at any time. It is off for each account until you accept a warning, it stops on
  401, 403 or 429, and you can turn it off at any time. See [claude-ai.md](claude-ai.md).

Use Watchyard only with your own accounts, each within the terms of its plan
([Consumer Terms](https://www.anthropic.com/legal/consumer-terms),
[Usage Policy](https://www.anthropic.com/legal/aup)). Do not share accounts or credentials
with other people. Watchyard never switches or rotates accounts to get around limits; the
"most room right now" line and `wy account next` are suggestions for where to start your next
session, and you (or the script you wrote) choose. Neither moves a running session or touches
a login.

## Why do the desktop app's claude.ai tabs look like Chrome to claude.ai?

claude.ai's bot protection rejects Electron's default user agent, which names Electron and the
app, so the tabs would not load. The account tabs send a plain Chrome user agent for your
operating system instead. They run Chromium, so the string describes the engine, but claude.ai
cannot tell the browser is Watchyard. Nothing else changes: the tabs send what a browser
sends. If you prefer not to, skip account tabs and use the board in any browser. Details:
[claude-ai.md](claude-ai.md#the-browser-identity-of-the-tabs).

## Does the hub see my claude.ai cookies, prompts or code?

No. The hub never receives cookies, tokens, prompts, code, file contents or tool output.
It receives counts, models, times, session ids, states, project folder names, account emails
and limit percentages. If you leave `privacy.last_message` on (the default), it also receives
the last few hundred characters of Claude's final message in a turn, which makes question
detection and notification text work. Turn it off with
`wy config set privacy.last_message false`. Peer messages between sessions are end-to-end
encrypted; the hub relays ciphertext and keeps metadata only. Details:
[security.md](security.md).

## Do I need a server?

No. The hub can run on your laptop (`wy setup`, "Just this computer"). A server helps when you
have several machines and your laptop is often asleep: alerts for sessions on other machines
then keep working.

## Do I need Tailscale?

No, but it is the simplest way to give the hub an `https://` address for your phone
(`tailscale serve`) and to let machines reach it privately. A reverse proxy with HTTPS, such
as the Caddy file in `deploy/`, works too.

## Is there an iPhone app?

No. The mobile app is Android only for now. On an iPhone, install the board as a web app from
Safari (iOS 16.4 or newer for notifications) or subscribe to an ntfy topic
([alerts.md](alerts.md)).

## Does it use Google or Firebase for Android notifications?

No. The Android app keeps its own encrypted connection to your hub and posts notifications
itself. No Firebase project, no Google push service and no third-party account are involved,
and the APK contains no Google Play services or Firebase code at all (CI checks every build).
The installable web app, if you use it, does use the browser's own push service, with the
payload encrypted end to end.

## Why not just use ccusage or a usage monitor?

They are good at reading local usage logs for one machine. Watchyard is for a different case:
several accounts on several machines, with session states (needs you, asked, done), the
official limit percentages per account, alerts on your phone and messaging between your own
sessions. See the comparison in the [README](../../README.md#similar-tools).

## Why not wait for an official feature?

If Anthropic ships something that covers your needs, use it. Watchyard is built around
your own self-hosted hub, so you keep the data either way.

## Will my accounts be logged out or changed?

`wy` never reads, moves or copies a login. Setup changes only what its plan lists, backs up
everything first, and `wy setup --undo` reverses a run byte for byte.

## Where do I get binaries?

From [GitHub Releases](https://github.com/mraj602/watchyard/releases). Each `vX.Y.Z` release
has `wy` archives with `SHA256SUMS`, the install scripts, the desktop packages with
`SHA256SUMS-desktop`, and build attestations. The Android APK is in a separate `phone-v*`
release, signed with the maintainer's key. The one-line install is in
[install.md](install.md#1-install-wy). You can still build everything from source.

## Where do I ask questions or report bugs?

Questions: GitHub Discussions. Bugs: GitHub issues, using the bug template. Security
problems: privately, as [SECURITY.md](../../SECURITY.md) explains.
