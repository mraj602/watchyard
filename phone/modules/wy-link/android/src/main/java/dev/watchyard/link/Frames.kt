package dev.watchyard.link

import org.json.JSONObject

// link.md 4 and push.md 14.5: frame parsing, the hub signature checks in link.md 4.3's order,
// `seq` bookkeeping and what each frame does. Pure JVM: the Android side passes a decryptor
// (RFC 8291 with the Keystore key) and a sink (notifications, widgets, events).

/** One hub→device frame (link.md 4.1). `body` is the decoded b64u body. */
data class Frame(val t: String, val seq: Long, val exp: Long, val tag: String, val body: ByteArray, val sig: ByteArray) {
  val queued get() = Frames.isQueued(t, seq)
}

object Frames {
  /** link.md 6.4: queued, sealed, seq > 0. */
  val QUEUED = setOf("alert", "refresh", "peer_msg", "peer_status", "matrix", "revoke", "peer_thread_resume")

  /** link.md 6.3: ephemeral, seq 0, signed with the login nonce as context. */
  val EPHEMERAL = setOf("welcome", "action_result", "peers", "peer_ack", "notice")

  private val TAG = Regex("^[A-Za-z0-9_:.-]{0,64}$")

  /** Queued or ephemeral: the type decides when it is known, the seq otherwise. */
  fun isQueued(t: String, seq: Long): Boolean = when (t) {
    in QUEUED -> true
    in EPHEMERAL -> false
    else -> seq > 0
  }

  /** A frame from one WebSocket text message, or null when it is not one (link.md 4.1). */
  fun parse(text: String): Frame? = try {
    parse(JSONObject(text))
  } catch (_: Exception) {
    null
  }

  fun parse(o: JSONObject): Frame? {
    val t = o.optString("t", "")
    if (t.isEmpty() || t.contains('\n') || !o.has("seq") || !o.has("exp") || !o.has("body") || !o.has("sig")) return null
    val seq = o.optLong("seq", -1)
    val exp = o.optLong("exp", -1)
    val tag = o.optString("tag", "")
    if (seq < 0 || exp < 0 || !TAG.matches(tag)) return null
    val body = Codec.unb64u(o.optString("body")) ?: return null
    val sig = Codec.unb64u(o.optString("sig")) ?: return null
    return Frame(t, seq, exp, tag, body, sig)
  }

  // The "wy-link-v1" labels below are part of the protocol: the hub signs and verifies with
  // them (link.md 4.3).

  /**
   * link.md 4.3: "wy-link-v1\n" ‖ device ‖ "\n" ‖ t ‖ "\n" ‖ decimal(seq) ‖ "\n" ‖ decimal(exp) ‖ "\n" ‖
   * tag ‖ "\n" ‖ ctx ‖ "\n" ‖ payload.
   */
  fun signedInput(device: String, t: String, seq: Long, exp: Long, tag: String, ctx: String, payload: ByteArray): ByteArray =
    Codec.cat(Codec.utf8("wy-link-v1\n$device\n$t\n${Codec.decimal(seq)}\n${Codec.decimal(exp)}\n$tag\n$ctx\n"), payload)

  /** link.md 3.1. */
  fun challengeInput(hubFp: String, nonce: String, exp: Long): ByteArray =
    Codec.lines("wy-link-v1 challenge", hubFp, nonce, Codec.decimal(exp))

  /** link.md 3.2. */
  fun helloInput(hubFp: String, nonce: String, exp: Long, device: String, last: Long): ByteArray =
    Codec.lines("wy-link-v1 hello", hubFp, nonce, Codec.decimal(exp), device, Codec.decimal(last))

  const val QUEUE_CTX = "queue"
}

/** A verified challenge (link.md 3.1). */
data class Challenge(val hub: String, val nonce: String, val exp: Long)

object Login {
  /** A well-formed challenge (link.md 3.1) and its signature, not checked yet; null otherwise. */
  private fun parse(o: JSONObject): Pair<Challenge, ByteArray>? {
    if (o.optString("t") != "challenge" || o.optInt("v", 0) != 1) return null
    val nonce = o.optString("nonce")
    val exp = o.optLong("exp", -1)
    val sig = Codec.unb64u(o.optString("sig")) ?: return null
    if (nonce.contains('\n') || exp < 0 || Codec.unb64u(nonce)?.size != 32) return null
    return Challenge(o.optString("hub"), nonce, exp) to sig
  }

  /** Checks the challenge against the pins; null when it is malformed or not the pinned hub's. */
  fun checkChallenge(o: JSONObject, pinnedFp: String, verifier: HubVerifier): Challenge? {
    val (c, sig) = parse(o) ?: return null
    return c.takeIf { it.hub == pinnedFp && verifier.verify(sig, Frames.challengeInput(c.hub, c.nonce, c.exp)) }
  }

  /**
   * push.md 14.7 "Hub key changed": a well-formed challenge the pinned key did not sign (another
   * fingerprint or signature). A malformed one is only a bad frame and is retried.
   */
  fun fromAnotherHub(o: JSONObject, pinnedFp: String, verifier: HubVerifier): Boolean =
    parse(o) != null && checkChallenge(o, pinnedFp, verifier) == null

  /** The hello frame (link.md 3.2), signed with the device's login key. */
  fun hello(c: Challenge, device: String, last: Long, client: JSONObject, signer: Signer): JSONObject {
    val sig = signer.sign(Frames.helloInput(c.hub, c.nonce, c.exp, device, last))
    return JSONObject()
      .put("t", "hello").put("v", 1).put("device", device).put("nonce", c.nonce).put("exp", c.exp)
      .put("last", last).put("client", client).put("sig", Codec.b64u(sig))
  }
}

/** An alert to post, from push.md 6.5's payload plus `origin` (13.2). */
data class AlertNote(
  val type: String,
  val tag: String,
  val title: String,
  val body: String,
  val channel: String,
  val whenMs: Long?,
  val url: String,
  val remoteUrl: String?,
  val tap: String,
  val alert: String?,
  val snooze: String?,
  val seen: String?,
  val origin: String?,
  val missed: Int,
  /** The payload's `trigger` (push.md 6.5); null for tests and payloads without one. */
  val trigger: String? = null,
) {
  /** The small icon and large-icon badge for this note. */
  val iconKind get() = Payloads.iconKindFor(type, trigger)
  val id get() = NotifyIds.forTag(tag.ifEmpty { alert ?: "wy" })
}

/** Which notification glyph a note gets (phone.md P6.1a). `CONNECTION` is used by the link service's own notes. */
enum class IconKind { CONNECTION, NEEDS_YOU, FAILED, LIMIT, RESET, MACHINE, SUMMARY, GENERIC }

object Payloads {
  private val ACTION_TOKEN = Regex("^a1\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$")

  /** push.md 3.9's usage notices. */
  private fun isUsageNotice(trigger: String?): Boolean =
    trigger == "limit_change" || trigger == "usage_reset" || trigger == "seat_change" || trigger == "weekly_drop"

  /** P6.1's channel for the trigger; `wy_test` for tests. */
  fun channelFor(type: String, trigger: String?): String = when {
    type == "test" -> Channels.TEST
    type == "summary" -> Channels.NEEDS_YOU
    trigger == "limit_80" || trigger == "limit_95" || trigger == "reset" -> Channels.LIMITS
    trigger == "machine_silent" -> Channels.MACHINES
    isUsageNotice(trigger) -> Channels.USAGE
    else -> Channels.NEEDS_YOU
  }

  /** The glyph for a payload: by type first (summary, test), then by trigger; anything else is the plain mark. */
  fun iconKindFor(type: String, trigger: String?): IconKind = when {
    type == "test" -> IconKind.GENERIC
    type == "summary" -> IconKind.SUMMARY
    trigger == "needs_permission" || trigger == "asked" -> IconKind.NEEDS_YOU
    trigger == "failed" -> IconKind.FAILED
    trigger == "limit_80" || trigger == "limit_95" || isUsageNotice(trigger) -> IconKind.LIMIT
    trigger == "reset" -> IconKind.RESET
    trigger == "machine_silent" -> IconKind.MACHINE
    else -> IconKind.GENERIC
  }

  /**
   * A payload `remote_url` the tap may open in the browser: https, no user info, default port,
   * host claude.ai or a subdomain. Anything else is null, and the tap opens the app instead.
   */
  fun claudeUrl(u: String?): String? {
    if (u == null || u.length > 8192 || u.any { it.isWhitespace() || it == '\\' }) return null
    val uri = try { java.net.URI(u) } catch (_: Exception) { return null }
    if (uri.scheme?.lowercase() != "https" || uri.rawUserInfo != null || !(uri.port == -1 || uri.port == 443)) return null
    val h = uri.host?.lowercase()?.removeSuffix(".") ?: return null
    return if (h == "claude.ai" || h.endsWith(".claude.ai")) u else null
  }

  fun alert(o: JSONObject, frameTag: String): AlertNote? {
    if (o.optInt("v", 0) != 1) return null
    val type = o.optString("type", "")
    if (type !in setOf("alert", "summary", "test")) return null
    val acts = o.optJSONObject("actions")
    fun token(k: String) = acts?.optString(k)?.takeIf { ACTION_TOKEN.matches(it) }
    return AlertNote(
      type = type,
      tag = o.optString("tag", frameTag).ifEmpty { frameTag },
      title = o.optString("title", ""),
      body = o.optString("body", ""),
      channel = channelFor(type, o.optString("trigger").ifEmpty { null }),
      whenMs = o.optString("ts").takeIf { it.isNotEmpty() && it != "null" }?.let(::parseIso),
      url = o.optString("url", "/#/"),
      remoteUrl = claudeUrl(o.optString("remote_url")),
      tap = o.optString("tap", "auto"),
      alert = o.optString("alert").ifEmpty { null },
      snooze = token("snooze"),
      seen = token("seen"),
      origin = o.optString("origin").ifEmpty { null },
      missed = o.optInt("missed", 0),
      trigger = o.optString("trigger").ifEmpty { null },
    )
  }

  /** RFC 3339 with milliseconds and Z, as the hub writes it; null otherwise. */
  fun parseIso(s: String): Long? = try {
    java.time.Instant.parse(s).toEpochMilli()
  } catch (_: Exception) {
    null
  }

  /** Tapping the persistent connection notification (and "stopped" notes) opens Now, never a settings page. */
  const val CONNECTION_TAP = "watchyard://now"

  /** The connection notification's Settings button. */
  const val CONNECTION_SETTINGS = "watchyard://settings/notifications"

  /**
   * A payload `url` (push.md 5.8: "/#/sessions/<id>?alert=…") as the app's own link
   * (phone.md P7: "watchyard://sessions/<id>?alert=…"). The app's allow-list checks it again.
   */
  fun appLink(url: String): String {
    if (!url.startsWith("/#/") || url.contains('\\')) return "watchyard://now"
    val rest = url.removePrefix("/#/")
    return if (rest.isEmpty() || rest.startsWith("?")) "watchyard://now" else "watchyard://$rest"
  }
}

object NotifyIds {
  /** push.md 14.5: the first 4 bytes of SHA-256(tag), big-endian, as a signed int. */
  fun forTag(tag: String): Int {
    val h = Codec.sha256(Codec.utf8(tag))
    return ((h[0].toInt() and 0xff) shl 24) or ((h[1].toInt() and 0xff) shl 16) or
      ((h[2].toInt() and 0xff) shl 8) or (h[3].toInt() and 0xff)
  }

  /** The persistent connection notification's id: a constant no tag hash is likely to hit. */
  const val CONNECTION = 0x5359_0001
}

/** What the processor tells the platform to do. */
interface Sink {
  fun post(n: AlertNote)
  fun dismiss(tag: String)
  fun refresh(json: String)

  /** An alert said `missed`: the app should refetch (an event to JS). */
  fun missed()
}

/** The outcome of one frame. */
data class Outcome(
  /** Send `{"t":"ack","seq":N}` (cumulative; batched by the client). */
  val ack: Long? = null,
  /** Send `{"t":"reject","seq":N}`. */
  val reject: Long? = null,
  /** A verified ephemeral frame: its type and body object. */
  val ephemeral: Pair<String, JSONObject>? = null,
  /** Why it was dropped or rejected (for the log line and tests); never a body. */
  val note: String? = null,
)

/**
 * push.md 14.5 per frame: check (link.md 4.3's order), open, act, then store `last` and ack.
 * `open` decrypts a sealed body (RFC 8291 with the Keystore key); `nowMs` is the phone's clock,
 * corrected by the welcome's offset here.
 */
class FrameProcessor(
  private val store: Store,
  private val verifier: HubVerifier,
  private val open: (ByteArray) -> ByteArray,
  private val sink: Sink,
  private val nowMs: () -> Long,
) {
  /** One text message on a logged-in connection whose login nonce is `nonce`. */
  fun onText(text: String, nonce: String): Outcome {
    val f = Frames.parse(text) ?: return rejected(null, "malformed")
    return onFrame(f, nonce)
  }

  fun onFrame(f: Frame, nonce: String): Outcome {
    val device = store.device ?: return Outcome(note = "not configured")
    val queued = f.queued
    // 1. the signature over our own device id, with the context of the frame's kind
    val ctx = if (queued) Frames.QUEUE_CTX else nonce
    if (!verifier.verify(f.sig, Frames.signedInput(device, f.t, f.seq, f.exp, f.tag, ctx, f.body))) {
      return rejected(if (f.seq > 0) f.seq else null, "bad signature")
    }
    // 2. seq = 0 for an ephemeral type, > 0 for a queued one
    if (queued != (f.seq > 0)) return rejected(if (f.seq > 0) f.seq else null, "bad seq")
    if (!queued) return ephemeral(f)
    // 3. a duplicate (a replay after a reconnect or a crash before the ack)
    val last = store.last
    if (f.seq <= last) return Outcome(ack = last, note = "duplicate")
    // 4. expired, by the hub's clock (welcome's offset)
    if (expired(f.exp)) {
      store.setLast(f.seq)
      return Outcome(ack = f.seq, note = "expired")
    }
    // 5. open and parse, 6. act
    when (f.t) {
      "alert", "refresh" -> {
        val plain = try {
          open(f.body)
        } catch (_: Exception) {
          return rejected(f.seq, "does not open")
        }
        val o = try {
          JSONObject(String(plain, Charsets.UTF_8))
        } catch (_: Exception) {
          return rejected(f.seq, "not json")
        }
        if (f.t == "refresh") {
          if (o.optInt("v", 0) != 1) return rejected(f.seq, "bad refresh")
          sink.refresh(o.toString())
        } else if (o.optString("type") == "dismiss") {
          sink.dismiss(o.optString("tag", f.tag).ifEmpty { f.tag })
        } else {
          val type = o.optString("type")
          if (type in setOf("alert", "summary", "test")) {
            val n = Payloads.alert(o, f.tag) ?: return rejected(f.seq, "bad payload")
            sink.post(n)
            if (n.missed > 0) sink.missed()
          }
          // another `type`: ignored (forward compatibility), still acked
        }
      }
      else -> Unit // matrix, revoke, peer frames: not for phones (PE3); acked and ignored
    }
    // 7. last, then the ack (link.md 5.6)
    store.setLast(f.seq)
    store.countReceived()
    return Outcome(ack = f.seq)
  }

  private fun ephemeral(f: Frame): Outcome {
    val o = try {
      JSONObject(String(f.body, Charsets.UTF_8))
    } catch (_: Exception) {
      return rejected(null, "ephemeral not json")
    }
    if (f.t == "welcome") {
      // welcome uses the offset it carries (link.md 4.3 step 4)
      Payloads.parseIso(o.optString("server_time"))?.let { store.setOffset(it - nowMs()) }
    }
    if (expired(f.exp)) return Outcome(note = "expired")
    return Outcome(ephemeral = f.t to o)
  }

  private fun expired(exp: Long): Boolean = (nowMs() + store.offsetMs) / 1000 > exp

  private fun rejected(seq: Long?, why: String): Outcome {
    store.countRejected()
    return Outcome(reject = seq, note = why)
  }
}

/** link.md 8: what a close code makes the device do. */
sealed class CloseAction {
  /** Reconnect after the next backoff delay. */
  object Retry : CloseAction()

  /** Reconnect after at least this long. */
  data class After(val ms: Long) : CloseAction()

  /** Stop the service (and the watchdog for revoked); `why` is the state word. */
  data class Stop(val status: String, val why: String) : CloseAction()

  /** Another connection took over: nothing from this one. */
  object Superseded : CloseAction()
}

object CloseCodes {
  val WORDS = mapOf(
    1000 to "normal", 1001 to "going_away", 4400 to "bad_frame", 4401 to "unauthorized", 4403 to "revoked",
    4408 to "idle", 4409 to "superseded", 4413 to "too_big", 4426 to "upgrade", 4429 to "rate_limited",
    4500 to "internal", 4503 to "frozen",
  )

  fun word(code: Int): String = WORDS[code] ?: "closed"

  /** The notice a revoked phone keeps showing after the app unpairs and clears its notifications. */
  const val REMOVED_NOTICE = "This phone was removed from the hub"

  /**
   * `reset()` runs after the JS side dismissed every notification (the old pairing's tokens are
   * dead), which also wiped the 4403 notice the service had just posted: the one message that
   * says why alerts stopped. A reset that follows a revocation posts it again; any other
   * (the user's own Unpair, a re-pair) posts nothing.
   */
  fun noticeAfterReset(statusBeforeReset: String): String? = if (statusBeforeReset == "revoked") REMOVED_NOTICE else null

  /** `streak`: closes with this same code in a row, this one included. */
  fun decide(code: Int, streak: Int): CloseAction = when (code) {
    4403 -> CloseAction.Stop("revoked", REMOVED_NOTICE)
    4426 -> CloseAction.Stop("upgrade", "Update the app")
    4409 -> CloseAction.Superseded
    4401 -> if (streak >= 3) CloseAction.Stop("error", "Hub does not recognise this device's key") else CloseAction.Retry
    4400 -> if (streak >= 3) CloseAction.Stop("error", "The hub refused this app's frames") else CloseAction.Retry
    4429, 4503 -> CloseAction.After(60_000)
    else -> CloseAction.Retry
  }
}
