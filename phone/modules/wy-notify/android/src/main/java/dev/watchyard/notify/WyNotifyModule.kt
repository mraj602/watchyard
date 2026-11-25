package dev.watchyard.notify

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.core.app.NotificationManagerCompat
import expo.modules.interfaces.permissions.Permissions
import expo.modules.interfaces.permissions.PermissionsResponse
import expo.modules.interfaces.permissions.PermissionsStatus
import expo.modules.kotlin.Promise
import expo.modules.kotlin.exception.CodedException
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition
import me.leolin.shortcutbadger.ShortcutBadgeException
import me.leolin.shortcutbadger.ShortcutBadger

/**
 * The notification calls the app's JS makes (phone.md P6): channels, the permission and its
 * prompt, the badge and dismissals. The alerts themselves are posted by wy-link, natively, with
 * their own tap intents; this module never posts one. Each call does what expo-notifications'
 * Android code did, so replacing it changes nothing a person sees.
 */
class WyNotifyModule : Module() {
  private val ctx: Context
    get() = appContext.reactContext?.applicationContext ?: throw CodedException("ERR_WY_CONTEXT", "wy-notify: no Android context", null)

  private val permissions: Permissions
    get() = appContext.permissions ?: throw CodedException("ERR_WY_PERMISSIONS", "wy-notify: no permissions manager", null)

  /** Android 13+ with a 33+ target: POST_NOTIFICATIONS is a runtime permission. */
  private fun runtimePermission(): Boolean =
    Build.VERSION.SDK_INT >= 33 && ctx.applicationInfo.targetSdkVersion >= 33

  override fun definition() = ModuleDefinition {
    Name("WyNotify")

    /**
     * Creates the channel. For one that exists Android updates only the name (and lowers, never
     * raises, the importance); what the person chose for it is kept.
     */
    AsyncFunction("setChannel") { id: String, name: String, importance: Int ->
      ctx.getSystemService(NotificationManager::class.java)?.createNotificationChannel(NotificationChannel(id, name, importance))
      Unit
    }

    /**
     * Opens Android's own settings page for one channel (sound, importance, vibration). The
     * channel must exist (the caller creates it with setChannel first); Android 8+ only.
     */
    AsyncFunction("openChannelSettings") { channelId: String ->
      if (Build.VERSION.SDK_INT < 26) throw CodedException("ERR_WY_CHANNELS", "wy-notify: channels need Android 8", null)
      val intent = Intent(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
        .putExtra(Settings.EXTRA_APP_PACKAGE, ctx.packageName)
        .putExtra(Settings.EXTRA_CHANNEL_ID, channelId)
        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
      ctx.startActivity(intent)
      Unit
    }

    AsyncFunction("getPermission") { promise: Promise ->
      if (runtimePermission()) {
        permissions.getPermissions({ resolvePermission(promise, it) }, Manifest.permission.POST_NOTIFICATIONS)
      } else {
        promise.resolve(classic())
      }
    }

    /** The system prompt on Android 13+; before that, notifications are on unless turned off. */
    AsyncFunction("requestPermission") { promise: Promise ->
      if (runtimePermission()) {
        permissions.askForPermissions({ _ ->
          permissions.getPermissions({ resolvePermission(promise, it) }, Manifest.permission.POST_NOTIFICATIONS)
        }, Manifest.permission.POST_NOTIFICATIONS)
      } else {
        promise.resolve(classic())
      }
    }

    /**
     * The launcher badge (P6.6) through ShortcutBadger, on launchers that have one. 0 clears it,
     * which (as in expo-notifications) also clears the app's notifications.
     */
    AsyncFunction("setBadgeCount") { count: Int ->
      try {
        if (count <= 0) NotificationManagerCompat.from(ctx).cancelAll()
        else ShortcutBadger.applyCountOrThrow(ctx, count)
        true
      } catch (e: ShortcutBadgeException) {
        Log.d(TAG, "no badge on this launcher")
        false
      }
    }

    /** One notification by its identifier (tag = identifier, id 0, as expo-notifications posted them). */
    AsyncFunction("dismiss") { identifier: String ->
      NotificationManagerCompat.from(ctx).cancel(identifier, 0)
    }

    /** Every notification of the app (unpairing, P6.4). A foreground service's note stays. */
    AsyncFunction("dismissAll") {
      NotificationManagerCompat.from(ctx).cancelAll()
    }
  }

  private fun resolvePermission(promise: Promise, map: Map<String, PermissionsResponse>) {
    val r = map[Manifest.permission.POST_NOTIFICATIONS]
    val enabled = NotificationManagerCompat.from(ctx).areNotificationsEnabled()
    val granted = r?.status == PermissionsStatus.GRANTED
    val status = when {
      r?.status == PermissionsStatus.DENIED -> "denied"
      !enabled -> "denied"
      granted -> "granted"
      else -> "undetermined"
    }
    promise.resolve(mapOf("granted" to granted, "status" to status, "canAskAgain" to (r?.canAskAgain ?: true)))
  }

  private fun classic(): Map<String, Any> {
    val enabled = NotificationManagerCompat.from(ctx).areNotificationsEnabled()
    return mapOf("granted" to enabled, "status" to if (enabled) "granted" else "denied", "canAskAgain" to enabled)
  }

  companion object {
    const val TAG = "WyNotify"
  }
}
