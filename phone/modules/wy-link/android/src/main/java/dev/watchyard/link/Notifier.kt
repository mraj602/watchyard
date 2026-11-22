package dev.watchyard.link

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat

/**
 * push.md 14.5 and 14.6 on the platform: P6.1's channels, posting with the tag-derived id
 * (a new alert with the same tag replaces the old one), dismissals, Open / Snooze / Seen, the
 * persistent connection notification, and the widget redraw of a `refresh` frame.
 */
class Notifier(private val ctx: Context, private val store: Store) : Sink {
  private val nm get() = NotificationManagerCompat.from(ctx)

  /**
   * Called by the service and before any post; idempotent (the app's JS makes the same ids). A
   * channel that already exists is created again under the same id with its current importance:
   * Android then updates only the name (and description) and keeps everything the user chose, so a
   * rename reaches existing installs without touching importance.
   */
  fun ensureChannels() {
    val m = ctx.getSystemService(NotificationManager::class.java) ?: return
    for ((id, name, importance) in Channels.ALL) {
      val existing = m.getNotificationChannel(id)
      if (existing != null) {
        if (existing.name?.toString() != name) m.createNotificationChannel(NotificationChannel(id, name, existing.importance))
        continue
      }
      val ch = NotificationChannel(id, name, importance)
      if (id == Channels.CONNECTION) {
        ch.setShowBadge(false)
        ch.setSound(null, null)
        ch.enableVibration(false)
      }
      m.createNotificationChannel(ch)
    }
  }

  /** The small icon for a kind (phone.md P6.1a); each is one white path that Android tints with [color]. */
  private fun icon(kind: IconKind): Int = iconRes(kind)

  /** The accent behind the small icon: gold for alerts, grey for the always-on connection note. */
  private fun color(kind: IconKind): Int = if (kind == IconKind.CONNECTION) CONNECTION_GREY else alertColor()

  /** The brand accent: the icon gold. */
  private fun alertColor(): Int = BRAND_ACCENT

  /**
   * What the lock screen shows instead of the alert: no session, project or machine names. The
   * alert itself is VISIBILITY_PRIVATE, so Android swaps in this one while the phone is locked
   * (and the person's setting for sensitive notifications asks to hide them).
   */
  private fun publicVersion(channel: String, iconKind: IconKind): Notification =
    NotificationCompat.Builder(ctx, channel)
      .setSmallIcon(icon(iconKind)).setColor(color(iconKind))
      .setContentTitle("Watchyard")
      .setContentText(PUBLIC_TEXT)
      .build()

  private fun flags() = PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT

  /** Tap and Open: the app at the payload's route, or the Claude link when the tap rule says so. */
  private fun openIntent(n: AlertNote): PendingIntent {
    val i = if (n.tap == "claude" && n.remoteUrl != null) {
      Intent(Intent.ACTION_VIEW, Uri.parse(n.remoteUrl))
    } else {
      appIntent(Payloads.appLink(n.url))
    }
    i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    return PendingIntent.getActivity(ctx, n.id, i, flags())
  }

  /**
   * The app's own launcher activity at a watchyard:// link. Naming the component (not just the
   * package) means the tap always starts the app, never a chooser or a fall back to the app's
   * notification settings.
   */
  private fun appIntent(link: String): Intent = WidgetOpen.appIntent(ctx, link)

  private fun appPending(code: Int, link: String): PendingIntent =
    PendingIntent.getActivity(ctx, code, appIntent(link), flags())

  private fun actionIntent(n: AlertNote, action: String, token: String): PendingIntent {
    val i = Intent(ctx, ActionReceiver::class.java)
      .setAction("dev.watchyard.link.ACTION.$action")
      .putExtra(ActionReceiver.EXTRA_ACTION, action)
      .putExtra(ActionReceiver.EXTRA_TOKEN, token)
      .putExtra(ActionReceiver.EXTRA_ALERT, n.alert ?: "")
      .putExtra(ActionReceiver.EXTRA_TAG, n.tag)
      .putExtra(ActionReceiver.EXTRA_ORIGIN, n.origin ?: store.origin ?: "")
    return PendingIntent.getBroadcast(ctx, n.id * 31 + action.hashCode(), i, flags())
  }

  override fun post(n: AlertNote) {
    // An action meant for another hub is dropped (phone.md P6.4): its buttons are not offered.
    val sameHub = n.origin == null || store.origin == null || sameOrigin(n.origin, store.origin!!)
    val b = NotificationCompat.Builder(ctx, n.channel)
      .setSmallIcon(icon(n.iconKind)).setColor(color(n.iconKind))
      .setContentTitle(n.title)
      .setContentText(n.body)
      .setStyle(NotificationCompat.BigTextStyle().bigText(n.body))
      .setGroup(Channels.GROUP)
      .setAutoCancel(true)
      // Backstop if the hub's `dismiss` frame (24h TTL) is missed: the OS removes it after a day.
      .setTimeoutAfter(DISMISS_AFTER_MS)
      .setContentIntent(openIntent(n))
      .setCategory(NotificationCompat.CATEGORY_MESSAGE)
      .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
      .setPublicVersion(publicVersion(n.channel, n.iconKind))
    NotifyIcons.large(n.iconKind)?.let { b.setLargeIcon(it) }
    n.whenMs?.let { b.setWhen(it).setShowWhen(true) }
    if (sameHub && (n.snooze != null || n.seen != null)) {
      b.addAction(0, "Open", openIntent(n))
      n.snooze?.let { b.addAction(0, "Snooze ${store.snoozeMin} min", actionIntent(n, "snooze", it)) }
      n.seen?.let { b.addAction(0, "Seen", actionIntent(n, "seen", it)) }
    }
    notify(n.id, b.build())
  }

  override fun dismiss(tag: String) = nm.cancel(NotifyIds.forTag(tag))

  override fun missed() = WyLinkModule.emit("refresh", null)

  /**
   * A `refresh` frame (push.md 13.5): keep the snapshot and ask the home-screen widgets to
   * redraw. react-native-android-widget's providers start their headless JS task on
   * APPWIDGET_UPDATE; that task merges this snapshot (WyLink.getRefresh) into the last one
   * (phone.md P11) with no network call. A running app also gets the `refresh` event.
   */
  override fun refresh(json: String) {
    store.setRefresh(json)
    WyLinkModule.emit("refresh", null)
    try {
      val awm = AppWidgetManager.getInstance(ctx)
      for (p in awm.getInstalledProvidersForPackage(ctx.packageName, null)) {
        val ids = awm.getAppWidgetIds(p.provider)
        if (ids.isEmpty()) continue
        ctx.sendBroadcast(
          Intent(AppWidgetManager.ACTION_APPWIDGET_UPDATE).setComponent(ComponentName(ctx, p.provider.className))
            .putExtra(AppWidgetManager.EXTRA_APPWIDGET_IDS, ids),
        )
      }
    } catch (e: Exception) {
      Log.w(LinkClient.TAG, "widget redraw: ${e.javaClass.simpleName}")
    }
  }

  /** The persistent notification of 14.4 (channel wy_connection). Tapping it opens Now; its Settings button opens Settings → Notifications. */
  fun connection(): Notification {
    val text = when (store.status) {
      "connected" -> "Connected to hub" + (store.connectedSince?.let { " · ${ago(it)}" } ?: "")
      "backoff" -> "Reconnecting…" + (store.nextRetryAt?.let { " (next try in ${maxOf(0, (it - System.currentTimeMillis()) / 1000)} s)" } ?: "")
      "connecting" -> "Connecting…"
      else -> "Not connected" + (store.lastError?.let { ": $it" } ?: "")
    }
    val open = appPending(NotifyIds.CONNECTION, Payloads.CONNECTION_TAP)
    val settings = appPending(NotifyIds.CONNECTION + 1, Payloads.CONNECTION_SETTINGS)
    return NotificationCompat.Builder(ctx, Channels.CONNECTION)
      .setSmallIcon(icon(IconKind.CONNECTION)).setColor(color(IconKind.CONNECTION))
      .setContentTitle("Watchyard")
      .setContentText(text)
      .setOngoing(true)
      .setOnlyAlertOnce(true)
      .setShowWhen(false)
      .setPriority(NotificationCompat.PRIORITY_MIN)
      .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
      .setContentIntent(open)
      .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
      .setPublicVersion(publicVersion(Channels.CONNECTION, IconKind.CONNECTION))
      .addAction(0, "Settings", settings)
      .build()
  }

  /** 4403 and the other stops: one plain notification saying why. */
  fun stopped(why: String) {
    ensureChannels()
    notify(
      NotifyIds.forTag("wy-link-stopped"),
      NotificationCompat.Builder(ctx, Channels.NEEDS_YOU).setSmallIcon(icon(IconKind.CONNECTION)).setColor(alertColor()).setContentTitle("Watchyard").setContentText(why)
        .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
        .setPublicVersion(publicVersion(Channels.NEEDS_YOU, IconKind.CONNECTION))
        .setContentIntent(appPending(NotifyIds.forTag("wy-link-stopped"), Payloads.CONNECTION_TAP))
        .setAutoCancel(true).build(),
    )
  }

  /** sendLocalTest(): a notification on wy_test without the hub. */
  fun localTest() {
    ensureChannels()
    post(
      AlertNote(
        "test", "wy-test-local", "Watchyard test", "This phone can show notifications.", Channels.TEST,
        System.currentTimeMillis(), "/#/", null, "session", null, null, null, null, 0,
      ),
    )
  }

  private fun notify(id: Int, n: Notification) {
    try {
      nm.notify(id, n)
    } catch (e: SecurityException) {
      Log.w(LinkClient.TAG, "notifications not allowed")
    }
  }

  companion object {
    const val DISMISS_AFTER_MS = 24L * 3600_000

    /** The lock-screen text of every notification: no names, nothing to learn from it. */
    const val PUBLIC_TEXT = "Open Watchyard to see this"

    /** #E8B04B, the gold of phone/assets/src/icon.svg (app.config.ts BRAND_ACCENT). */
    const val BRAND_ACCENT: Int = 0xFFE8B04B.toInt()

    /** The shade circle of the always-on connection note: grey, so it never reads as an alert. */
    const val CONNECTION_GREY: Int = 0xFF8A9099.toInt()

    /** The drawable (res/drawable/wy_notify_*.xml, shipped by this module) for a kind. */
    fun iconRes(kind: IconKind): Int = when (kind) {
      IconKind.CONNECTION -> R.drawable.wy_notify_connection
      IconKind.NEEDS_YOU -> R.drawable.wy_notify_needs_you
      IconKind.FAILED -> R.drawable.wy_notify_failed
      IconKind.LIMIT -> R.drawable.wy_notify_limit
      IconKind.RESET -> R.drawable.wy_notify_reset
      IconKind.MACHINE -> R.drawable.wy_notify_machine
      IconKind.SUMMARY -> R.drawable.wy_notify_summary
      IconKind.GENERIC -> R.drawable.wy_notify_generic
    }

    fun sameOrigin(a: String, b: String): Boolean = norm(a) == norm(b)

    private fun norm(o: String) = o.lowercase().trimEnd('/').removeSuffix(":443")

    fun ago(since: Long): String {
      val m = (System.currentTimeMillis() - since) / 60_000
      return when {
        m < 1 -> "just now"
        m < 60 -> "$m min"
        else -> "${m / 60} h ${m % 60} min"
      }
    }
  }
}
