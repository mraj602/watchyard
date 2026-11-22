package dev.watchyard.tabs

import java.net.URI

/**
 * The pure rules of phone.md P17.1, P17.5 and P17.7: profile names from seat ids (and profiles
 * moved to another seat), where a main-frame URL may go, and the account check's script and
 * answer. No Android types, so the JVM tests cover all of it.
 */
object TabPolicy {
  /** A hub seat id (identity.md 1): `seat_` + a ULID. */
  private val SEAT = Regex("^seat_[0-9A-HJKMNP-TV-Z]{26}$")
  const val PREFIX = "wy-tab-"
  const val HOME = "https://claude.ai/"

  /** claude.ai and the sign-in hops (desktop.md C2), each with its subdomains. */
  val HOSTS = listOf("claude.ai", "anthropic.com", "claude.com", "challenges.cloudflare.com")

  enum class Route { TAB, EXTERNAL, DROP }

  fun validSeat(seat: String?): Boolean = seat != null && SEAT.matches(seat)

  /** The profile of a seat, or null for anything that is not a seat id (never an email). */
  fun profileName(seat: String?): String? = if (validSeat(seat)) PREFIX + seat else null

  /** A further profile of a seat whose first one was moved to another seat: `wy-tab-<seat>.<n>`. */
  private val SUFFIX = Regex("\\.[1-9][0-9]{0,2}$")

  /** The seat a profile's name carries, or null for any other profile (`Default` included). */
  fun seatOf(profile: String?): String? {
    if (profile == null || !profile.startsWith(PREFIX)) return null
    val seat = profile.removePrefix(PREFIX).replace(SUFFIX, "")
    return if (validSeat(seat)) seat else null
  }

  /** The seat a profile belongs to: the one it was moved to (P17.7), else the one its name carries. */
  fun ownerOf(profile: String?, moves: Map<String, String>): String? {
    if (seatOf(profile) == null) return null
    return moves[profile]?.takeIf(::validSeat) ?: seatOf(profile)
  }

  /** The seat ids to list: tab profiles only, by owner, minus the ones signed out and waiting for deletion. */
  fun listed(profiles: List<String>, pending: Set<String>, moves: Map<String, String> = emptyMap()): List<String> =
    profiles.filter { it !in pending }.mapNotNull { ownerOf(it, moves) }.distinct().sorted()

  /** The profile a seat has now among `existing` (pending ones left out by the caller), or null. */
  fun owned(seat: String?, existing: List<String>, moves: Map<String, String>): String? =
    if (!validSeat(seat)) null else existing.filter { ownerOf(it, moves) == seat }.sorted().firstOrNull()

  /**
   * The profile a seat's tab binds: the one it has, else its own name unless that name was moved
   * to another seat, else the first free `<name>.<n>`. Never a name another seat owns.
   */
  fun profileFor(seat: String?, existing: List<String>, moves: Map<String, String>): String? {
    val base = profileName(seat) ?: return null
    owned(seat, existing, moves)?.let { return it }
    if (base !in moves) return base
    var n = 1
    while ("$base.$n" in moves && n < 999) n++
    return "$base.$n"
  }

  enum class MoveError { SEAT, NONE, TAKEN }

  /**
   * "Use this tab for <other seat> instead" (P17.7): the profile `from` has now belongs to `to`.
   * Refused when `from` has no profile or `to` already has one. Returns the new moves.
   */
  fun move(from: String?, to: String?, existing: List<String>, moves: Map<String, String>): Pair<Map<String, String>?, MoveError?> {
    if (!validSeat(from) || !validSeat(to) || from == to) return null to MoveError.SEAT
    val src = owned(from, existing, moves) ?: return null to MoveError.NONE
    if (owned(to, existing, moves) != null) return null to MoveError.TAKEN
    val next = moves.toMutableMap()
    if (seatOf(src) == to) next.remove(src) else next[src] = to!!
    return next to null
  }

  /** Moves as stored: a string set of `profile=seat`. */
  fun encodeMoves(moves: Map<String, String>): Set<String> = moves.map { (p, s) -> "$p=$s" }.toSet()

  /** The stored moves; bad entries are dropped. */
  fun decodeMoves(set: Set<String>?): Map<String, String> =
    (set ?: emptySet()).mapNotNull { e ->
      val i = e.indexOf('=')
      if (i <= 0) return@mapNotNull null
      val p = e.substring(0, i)
      val s = e.substring(i + 1)
      if (seatOf(p) != null && validSeat(s)) p to s else null
    }.toMap()

  fun allowedHost(host: String?): Boolean {
    if (host.isNullOrEmpty()) return false
    val h = host.lowercase().removeSuffix(".")
    return HOSTS.any { h == it || h.endsWith(".$it") }
  }

  private fun parse(url: String?): URI? =
    if (url == null || url.length > 8192 || url.any { it.isWhitespace() || it == '\\' }) null
    else try { URI(url) } catch (_: Exception) { null }

  /** Where a main-frame navigation goes: the tab, the browser, or nowhere. */
  fun route(url: String?): Route {
    val u = parse(url) ?: return Route.DROP
    return when (u.scheme?.lowercase()) {
      "https" -> when {
        u.rawUserInfo != null || u.host.isNullOrEmpty() -> Route.DROP
        allowedHost(u.host) && (u.port == -1 || u.port == 443) -> Route.TAB
        else -> Route.EXTERNAL
      }
      "mailto" -> Route.EXTERNAL
      else -> Route.DROP
    }
  }

  /** The first URL a tab loads must be one the tab may show; anything else starts at claude.ai. */
  fun startUrl(url: String?): String = if (route(url) == Route.TAB) url!! else HOME

  // ---- P17.5: microphone, camera and file chooser

  const val AUDIO_CAPTURE = "android.webkit.resource.AUDIO_CAPTURE"
  const val VIDEO_CAPTURE = "android.webkit.resource.VIDEO_CAPTURE"

  /** Only claude.ai itself and its subdomains (https, default port) may use the mic or camera. */
  fun mediaOrigin(origin: String?): Boolean = parse(origin)?.let(::claudeHost) != null

  /** The resources of a page's permission request to grant: audio and video capture from a claude.ai origin; nothing else. */
  fun grantable(origin: String?, resources: List<String>): List<String> =
    if (!mediaOrigin(origin)) emptyList() else resources.filter { it == AUDIO_CAPTURE || it == VIDEO_CAPTURE }.distinct()

  private val IMAGE_EXT = Regex("\\.(png|jpe?g|gif|webp|heic|heif)$", RegexOption.IGNORE_CASE)

  /** Whether a file chooser's accept types include images, so a camera photo is offered next to the files. */
  fun acceptsImages(types: List<String>): Boolean =
    types.any { val t = it.trim(); t.startsWith("image/", ignoreCase = true) || t == "*/*" || IMAGE_EXT.containsMatchIn(t) }

  // ---- P17.7: which account the tab is signed in to

  /** claude.ai paths where a tab is not signed in (desktop.md C5, NOT_READABLE_PATHS). */
  private val SIGNED_OUT_PATHS = listOf("/login", "/logout")

  private fun claudeHost(u: URI): String? {
    if (u.scheme?.lowercase() != "https" || u.rawUserInfo != null || !(u.port == -1 || u.port == 443)) return null
    val h = u.host?.lowercase()?.removeSuffix(".") ?: return null
    return if (h == "claude.ai" || h.endsWith(".claude.ai")) h else null
  }

  /**
   * A signed-in claude.ai page: https, claude.ai or a subdomain, default port, and a path that
   * is not /login or /logout (the desktop's isReadableUrl).
   */
  fun readable(url: String?): Boolean {
    val u = parse(url) ?: return false
    claudeHost(u) ?: return false
    val p = (u.rawPath ?: "").trimEnd('/')
    return SIGNED_OUT_PATHS.none { p == it || p.startsWith("$it/") }
  }

  /** Where the account check may run: a readable page on claude.ai itself (for its /api/account). */
  fun checkable(url: String?): Boolean = readable(url) && parse(url)?.let(::claudeHost) == "claude.ai"

  private val NONCE = Regex("^[0-9a-f]{16}$")

  /**
   * Starts the check in the page: one same-origin GET of claude.ai's own /api/account (a call
   * the desktop's reader makes, desktop.md C5), keeping only the account uuid. The answer waits
   * in `window.__wy_acct_<nonce>` as `u:<uuid>` or `s:<status>` (401 for a redirect to the login
   * page, 0 for a network error or a reply without a uuid). Nothing else of the page is read.
   */
  fun accountStart(nonce: String): String {
    require(NONCE.matches(nonce)) { "bad nonce" }
    return "(function(){var k='__wy_acct_$nonce';window[k]='';try{" +
      "fetch('/api/account',{credentials:'include',headers:{accept:'application/json'}}).then(function(r){" +
      "if(r.redirected&&/\\/(login|logout)(\\/|\\?|$)/.test(new URL(r.url).pathname))return 's:401';" +
      "if(!r.ok)return 's:'+r.status;" +
      "return r.json().then(function(a){var u=a&&a.uuid;return typeof u==='string'&&/^[0-9a-f-]{36}$/.test(u)?'u:'+u:'s:0';});" +
      "}).catch(function(){return 's:0';}).then(function(v){window[k]=v;});" +
      "}catch(e){window[k]='s:0';}return 1;})()"
  }

  /** Reads (and clears) the answer: null while it is not there yet. */
  fun accountPoll(nonce: String): String {
    require(NONCE.matches(nonce)) { "bad nonce" }
    return "(function(){var k='__wy_acct_$nonce',v=window[k];if(v){try{delete window[k];}catch(e){}}return v||null;})()"
  }

  const val POLL_MS = 300L
  const val POLLS = 50 // 15 s

  /** The check's answer: the account uuid, or the HTTP status (401 signed out, 0 no answer). */
  data class Account(val account: String?, val status: Int?)

  private val ANSWER = Regex("^\"(u:[0-9a-f-]{36}|s:[0-9]{1,3})\"$")

  /** evaluateJavascript's JSON-encoded answer, or null for anything else (still waiting included). */
  fun parseAccount(raw: String?): Account? {
    val v = ANSWER.matchEntire(raw ?: return null)?.groupValues?.get(1) ?: return null
    return if (v.startsWith("u:")) Account(v.substring(2), null) else Account(null, v.substring(2).toInt())
  }
}
