package dev.watchyard.tabs

import android.content.Context
import android.content.SharedPreferences
import android.os.Handler
import android.os.Looper
import androidx.webkit.ProfileStore
import androidx.webkit.WebViewFeature
import expo.modules.kotlin.exception.CodedException
import expo.modules.kotlin.functions.Queues
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition

/**
 * phone.md P17.1 and P17.7: the profile list, sign-out (delete a profile), moving a profile to
 * another seat, and the tab view. Every profile call runs on the main thread, as the WebView
 * profile API requires. Nothing here reads cookies or storage: the module only creates,
 * flushes, deletes and reassigns profiles.
 */
class WyTabsModule : Module() {
  override fun definition() = ModuleDefinition {
    Name("WyTabs")

    AsyncFunction("isSupported") { supported() }.runOnQueue(Queues.MAIN)

    OnCreate {
      store = appContext.reactContext?.getSharedPreferences("wy-tabs", Context.MODE_PRIVATE)
      main.post { applyPending() } // before this process has made any WebView
    }

    AsyncFunction("listProfiles") {
      if (!supported()) emptyList()
      else TabPolicy.listed(ProfileStore.getInstance().allProfileNames.toList(), pending(), moves())
    }.runOnQueue(Queues.MAIN)

    AsyncFunction("deleteProfile") { seat: String ->
      if (!TabPolicy.validSeat(seat)) throw CodedException("ERR_WY_TABS_SEAT", "wy-tabs: not a seat id", null)
      if (!supported()) return@AsyncFunction false
      val m = moves()
      val name = TabPolicy.profileFor(seat, existing(), m)!!
      if (name in open) throw CodedException("ERR_WY_TABS_IN_USE", "wy-tabs: close the tab first", null)
      if (name in m) setMoves(m - name) // a moved profile, once signed out, goes back to the seat its name carries
      try {
        ProfileStore.getInstance().deleteProfile(name).also { setPending(name, false) }
      } catch (e: IllegalStateException) {
        // The WebView implementation can keep the profile of a closed (destroyed) tab "in use"
        // until the process ends, so the delete is refused with no tab open. Wipe what a sign-in
        // leaves (cookies, web storage), hide the profile, and delete it when the app next starts.
        wipe(name)
        setPending(name, true)
        true
      }
    }.runOnQueue(Queues.MAIN)

    // P17.7 "Use this tab for <other seat> instead": the profile stays as it is (its sign-in is
    // never read or copied); only which seat it belongs to changes.
    AsyncFunction("moveProfile") { from: String, to: String ->
      if (!supported()) return@AsyncFunction false
      val m = moves()
      val src = TabPolicy.owned(from, existing(), m)
      if (src != null && src in open) throw CodedException("ERR_WY_TABS_IN_USE", "wy-tabs: close the tab first", null)
      val (next, err) = TabPolicy.move(from, to, existing(), m)
      when (err) {
        TabPolicy.MoveError.SEAT -> throw CodedException("ERR_WY_TABS_SEAT", "wy-tabs: not a seat id", null)
        TabPolicy.MoveError.NONE -> throw CodedException("ERR_WY_TABS_NONE", "wy-tabs: that seat has no tab", null)
        TabPolicy.MoveError.TAKEN -> throw CodedException("ERR_WY_TABS_TAKEN", "wy-tabs: the other seat has a tab already", null)
        null -> setMoves(next!!)
      }
      true
    }.runOnQueue(Queues.MAIN)

    OnActivityResult { _, payload -> TabMedia.onResult(payload.requestCode, payload.resultCode, payload.data) }

    // Cookies reach the disk when the app goes to the background (and when a tab closes).
    OnActivityEntersBackground { flushAll() }

    View(WyTabView::class) {
      Events("onTabState")
      Prop("seat") { view: WyTabView, seat: String? -> view.seat = seat }
      Prop("url") { view: WyTabView, url: String? -> view.url = url }
      OnViewDidUpdateProps { view: WyTabView -> view.update() }
      OnViewDestroys { view: WyTabView -> view.destroy() }
    }
  }

  companion object {
    private val main = Handler(Looper.getMainLooper())
    private var store: SharedPreferences? = null

    /** Profiles with a live tab view (main thread only). */
    private val open = HashSet<String>()

    private const val PENDING = "pending"
    private const val MOVES = "moves"

    private fun moves(): Map<String, String> = TabPolicy.decodeMoves(store?.getStringSet(MOVES, emptySet()))

    private fun setMoves(m: Map<String, String>) {
      store?.edit()?.putStringSet(MOVES, TabPolicy.encodeMoves(m))?.apply()
    }

    /** Tab profiles on disk, minus the ones waiting for deletion (main thread, supported only). */
    private fun existing(): List<String> {
      val p = pending()
      return ProfileStore.getInstance().allProfileNames.filter { it !in p && TabPolicy.seatOf(it) != null }
    }

    /** The profile a seat's tab binds now (P17.7), or null for a bad seat id. */
    fun profileFor(seat: String?): String? =
      if (!TabPolicy.validSeat(seat)) null else TabPolicy.profileFor(seat, existing(), moves())

    private fun pending(): Set<String> = store?.getStringSet(PENDING, emptySet())?.toSet() ?: emptySet()

    private fun setPending(name: String, on: Boolean) {
      val next = pending().let { if (on) it + name else it - name }
      store?.edit()?.putStringSet(PENDING, next)?.apply()
    }

    /** A tab view bound the profile: it is in use, and no longer waiting to be deleted. */
    fun opened(name: String) {
      open.add(name)
      setPending(name, false)
    }

    fun closed(name: String) {
      open.remove(name)
    }

    /** Signed-out profiles that could not be deleted live: delete them now, before any WebView exists. */
    private fun applyPending() {
      if (!supported()) return
      for (name in pending()) {
        try {
          ProfileStore.getInstance().deleteProfile(name)
          setPending(name, false)
        } catch (_: Throwable) {
        }
      }
    }

    private fun wipe(name: String) {
      try {
        val p = ProfileStore.getInstance().getProfile(name) ?: return
        p.cookieManager.removeAllCookies(null)
        p.webStorage.deleteAllData()
        p.cookieManager.flush()
      } catch (_: Throwable) {
      }
    }

    fun supported(): Boolean =
      try {
        WebViewFeature.isFeatureSupported(WebViewFeature.MULTI_PROFILE)
      } catch (_: Throwable) {
        false // no WebView package, or one that cannot be loaded
      }

    fun flush(profile: String) {
      try {
        ProfileStore.getInstance().getProfile(profile)?.cookieManager?.flush()
      } catch (_: Throwable) {
      }
    }

    fun flushAll() {
      if (!supported()) return
      try {
        ProfileStore.getInstance().allProfileNames.filter { TabPolicy.seatOf(it) != null }.forEach { flush(it) }
      } catch (_: Throwable) {
      }
    }
  }
}
