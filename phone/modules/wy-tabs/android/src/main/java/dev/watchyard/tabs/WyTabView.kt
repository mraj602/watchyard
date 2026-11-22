package dev.watchyard.tabs

import android.annotation.SuppressLint
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.view.ViewGroup
import android.webkit.GeolocationPermissions
import android.webkit.PermissionRequest
import android.webkit.RenderProcessGoneDetail
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.webkit.ProfileStore
import androidx.webkit.WebViewCompat
import expo.modules.kotlin.AppContext
import expo.modules.kotlin.viewevent.EventDispatcher
import expo.modules.kotlin.views.ExpoView
import java.security.SecureRandom

/**
 * One claude.ai tab (phone.md P17.1, P17.5, P17.7): a WebView bound to its seat's profile before
 * it loads anything. No JavaScript interface; the one script it runs is the account check
 * ([TabPolicy.accountStart]: claude.ai's /api/account, the uuid only); main-frame navigations go
 * through [TabPolicy.route]; downloads and geolocation are refused; the mic and file chooser go through [TabMedia] (P17.5).
 */
@SuppressLint("ViewConstructor", "SetJavaScriptEnabled")
class WyTabView(context: Context, appContext: AppContext) : ExpoView(context, appContext) {
  private val onTabState by EventDispatcher()
  private val main = Handler(Looper.getMainLooper())

  var seat: String? = null
  var url: String? = null

  private var web: WebView? = null
  private var profile: String? = null
  private var loaded: String? = null

  /** The account check in flight (its nonce), and the last main-frame URL seen (P17.7). */
  private var checking: String? = null
  private var lastUrl: String? = null
  private val random = SecureRandom()

  /** After a props change: (re)bind to the seat's profile, then load the URL when it changed. */
  fun update() {
    if (TabPolicy.profileName(seat) == null) {
      destroy()
      return
    }
    if (!WyTabsModule.supported()) {
      destroy()
      emit("error", null, mapOf("reason" to "unsupported"))
      return
    }
    val name = WyTabsModule.profileFor(seat) ?: return
    if (name != profile) destroy()
    val w = web ?: create(name) ?: return
    val target = TabPolicy.startUrl(url)
    if (target != loaded) {
      loaded = target
      w.loadUrl(target)
    }
  }

  fun destroy() {
    main.removeCallbacksAndMessages(null)
    val w = web ?: return
    web = null
    val closed = profile
    closed?.let { WyTabsModule.flush(it) }
    profile = null
    loaded = null
    checking = null
    lastUrl = null
    TabMedia.cancel()
    removeView(w)
    closed?.let { WyTabsModule.closed(it) }
    w.stopLoading()
    w.destroy()
  }

  override fun onDetachedFromWindow() {
    profile?.let { WyTabsModule.flush(it) }
    super.onDetachedFromWindow()
  }

  private fun create(name: String): WebView? {
    val w = WebView(context)
    try {
      ProfileStore.getInstance().getOrCreateProfile(name)
      WebViewCompat.setProfile(w, name) // before anything else touches the WebView's state
    } catch (e: Exception) {
      w.destroy()
      emit("error", null, mapOf("reason" to "profile"))
      return null
    }
    w.settings.apply {
      javaScriptEnabled = true
      domStorageEnabled = true
      allowFileAccess = false
      allowContentAccess = false
      setGeolocationEnabled(false)
      mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
      setSupportMultipleWindows(false)
      javaScriptCanOpenWindowsAutomatically = false
      safeBrowsingEnabled = true
    }
    w.webViewClient = Client()
    w.webChromeClient = Chrome()
    w.setDownloadListener { _, _, _, _, _ -> emit("download", null) }
    addView(w, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
    web = w
    profile = name
    WyTabsModule.opened(name)
    return w
  }

  private fun emit(state: String, url: String?, extra: Map<String, Any> = emptyMap()) {
    val m = mutableMapOf<String, Any>("state" to state)
    if (url != null) m["url"] = url
    m.putAll(extra)
    onTabState(m)
  }

  /**
   * P17.7: ask claude.ai, inside this tab's own session, which account it is signed in to (the
   * uuid only; TabPolicy.accountStart), then report it as an `account` event. Runs on a readable
   * claude.ai page, once per page load and when the page moves from the login page to a
   * signed-in one. A new page cancels a check in flight. Nothing is stored or sent here.
   */
  private fun checkAccount(view: WebView, url: String?) {
    if (web !== view || checking != null || !TabPolicy.checkable(url)) return
    val nonce = ByteArray(8).also(random::nextBytes).joinToString("") { "%02x".format(it) }
    checking = nonce
    view.evaluateJavascript(TabPolicy.accountStart(nonce)) { pollAccount(view, nonce, 0) }
  }

  private fun pollAccount(view: WebView, nonce: String, n: Int) {
    main.postDelayed({
      if (web === view && checking == nonce) {
        view.evaluateJavascript(TabPolicy.accountPoll(nonce)) { raw ->
          if (web === view && checking == nonce) {
            val r = TabPolicy.parseAccount(raw)
            when {
              r != null -> {
                checking = null
                val extra = if (r.account != null) mapOf<String, Any>("account" to r.account) else mapOf<String, Any>("status" to (r.status ?: 0))
                emit("account", view.url, extra)
              }
              n + 1 >= TabPolicy.POLLS -> {
                checking = null
                emit("account", view.url, mapOf("status" to 0))
              }
              else -> pollAccount(view, nonce, n + 1)
            }
          }
        }
      }
    }, TabPolicy.POLL_MS)
  }

  private fun openExternal(url: String) {
    try {
      context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
      emit("external", url)
    } catch (_: ActivityNotFoundException) {
    }
  }

  private inner class Client : WebViewClient() {
    override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
      if (!request.isForMainFrame) return false // sign-in and payment widgets live in subframes
      val u = request.url.toString()
      return when (TabPolicy.route(u)) {
        TabPolicy.Route.TAB -> false
        TabPolicy.Route.EXTERNAL -> { openExternal(u); true }
        TabPolicy.Route.DROP -> true
      }
    }

    override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) {
      // Defence in depth: a main frame that got past shouldOverrideUrlLoading off the list stops.
      if (url != null && url != "about:blank" && TabPolicy.route(url) != TabPolicy.Route.TAB) {
        view.stopLoading()
        return
      }
      if (web === view) checking = null // a new document: the old check's answer is gone with it
      emit("loading", url)
    }

    // In-page navigations (claude.ai is a single-page app: sign-in ends on /new without a load).
    override fun doUpdateVisitedHistory(view: WebView, url: String?, isReload: Boolean) {
      if (web !== view || url == null || url == lastUrl) return
      val was = lastUrl
      lastUrl = url
      emit("url", url)
      // Only for a page that has finished loading; a full load is checked in onPageFinished.
      if (view.progress == 100 && !TabPolicy.checkable(was)) checkAccount(view, url)
    }

    override fun onPageFinished(view: WebView, url: String?) {
      if (web === view) {
        lastUrl = view.url ?: url
        checkAccount(view, view.url ?: url)
      }
      // The content height 3 s later: 0 means a blank page (P17.4). Read from the view, not the page.
      main.postDelayed({
        if (web === view) emit("loaded", view.url ?: url, mapOf("height" to view.contentHeight))
      }, 3000)
    }

    override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
      if (request.isForMainFrame) emit("error", request.url.toString(), mapOf("code" to error.errorCode))
    }

    override fun onReceivedHttpError(view: WebView, request: WebResourceRequest, response: WebResourceResponse) {
      if (request.isForMainFrame) emit("http", request.url.toString(), mapOf("status" to response.statusCode))
    }

    override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
      if (web === view) destroy()
      emit("crashed", null)
      return true
    }
  }

  private inner class Chrome : WebChromeClient() {
    // P17.5: the mic (and camera) for claude.ai's own origin after the Android prompt; all else is denied.
    override fun onPermissionRequest(request: PermissionRequest) = TabMedia.permission(appContext, request)

    override fun onGeolocationPermissionsShowPrompt(origin: String?, callback: GeolocationPermissions.Callback) =
      callback.invoke(origin, false, false)

    override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
      return TabMedia.chooseFile(appContext, callback, params)
    }
  }
}
