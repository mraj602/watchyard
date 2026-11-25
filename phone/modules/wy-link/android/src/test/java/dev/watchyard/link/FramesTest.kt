package dev.watchyard.link

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class FramesTest {
  private val alertJson = JSONObject()
    .put("v", 1).put("type", "alert").put("alert", "al_01").put("alerts", listOf("al_01")).put("trigger", "asked")
    .put("title", "work · api · asked a question").put("body", "Should I also update the migration?")
    .put("tag", "wy-s-c41e").put("url", "/#/sessions/c41e?alert=al_01").put("tap", "auto").put("needs_you", 1)
    .put("actions", JSONObject().put("snooze", "a1.abc.def").put("seen", "a1.ghi.jkl")).put("private", false)
    .put("ts", "2026-10-15T09:00:00.000Z").put("origin", "https://hub.example.ts.net").toString()

  private fun alertFrame(p: TestPhone, seq: Long, json: String = alertJson, tag: String = "wy-s-c41e", exp: Long = p.nowS + 3600) =
    p.hub.frame(p.device, "alert", seq, exp, tag, p.seal(json))

  @Test fun aGoodAlertIsPostedThenAckedAndLastMoves() {
    val p = TestPhone()
    val out = p.processor().onText(alertFrame(p, 1), "nonce")
    assertEquals(1L, out.ack)
    assertNull(out.reject)
    assertEquals(1L, p.store.last)
    assertEquals(1, p.sink.posted.size)
    val n = p.sink.posted[0]
    assertEquals(Channels.NEEDS_YOU, n.channel)
    assertEquals("a1.abc.def", n.snooze)
    assertEquals(NotifyIds.forTag("wy-s-c41e"), n.id)
    assertEquals(1L, p.store.received)
  }

  @Test fun everySignedFieldChangedIsRejectedAndLastStays() {
    val p = TestPhone()
    p.processor().onText(alertFrame(p, 1), "n")
    val good = JSONObject(alertFrame(p, 2))
    val changes = listOf<(JSONObject) -> Unit>(
      { it.put("t", "refresh") },
      { it.put("seq", 3) },
      { it.put("exp", it.getLong("exp") + 1) },
      { it.put("tag", "wy-s-other") },
      { o -> val b = Codec.unb64u(o.getString("body"))!!; b[b.size - 1] = (b[b.size - 1].toInt() xor 1).toByte(); o.put("body", Codec.b64u(b)) },
      { o -> val s = Codec.unb64u(o.getString("sig"))!!; s[0] = (s[0].toInt() xor 1).toByte(); o.put("sig", Codec.b64u(s)) },
    )
    for ((i, change) in changes.withIndex()) {
      val f = JSONObject(good.toString()).also(change)
      val out = p.processor().onText(f.toString(), "n")
      assertNull("change $i acked", out.ack)
      assertNotNull("change $i not rejected", out.reject)
      assertEquals("change $i moved last", 1L, p.store.last)
    }
    assertEquals(1, p.sink.posted.size)
    assertEquals(changes.size.toLong(), p.store.rejected)
  }

  @Test fun aFrameForAnotherDeviceIsRejected() {
    val p = TestPhone()
    val f = p.hub.frame("dev_OTHER", "alert", 1, p.nowS + 60, "wy-s-c41e", p.seal(alertJson))
    val out = p.processor().onText(f, "n")
    assertEquals(1L, out.reject)
    assertEquals(0L, p.store.last)
  }

  @Test fun aSignedFrameFromAnotherHubKeyIsRejected() {
    val p = TestPhone()
    val other = HubKey(ByteArray(32) { 2 })
    val out = p.processor().onText(other.frame(p.device, "alert", 1, p.nowS + 60, "", p.seal(alertJson)), "n")
    assertEquals(1L, out.reject)
  }

  @Test fun seqZeroOnAQueuedTypeIsRejected() {
    val p = TestPhone()
    // signed as an ephemeral frame would be (ctx = nonce), but t is queued
    val f = p.hub.frame(p.device, "alert", 0, p.nowS + 60, "", p.seal(alertJson), ctx = "n")
    val out = p.processor().onText(f, "n")
    assertNull(out.ack)
    assertNull(out.reject) // seq 0 is not a row the hub could delete
    assertEquals(1L, p.store.rejected)
  }

  @Test fun anInsertedRowWithAHugeSeqAndABadSignatureDoesNotMoveLast() {
    val p = TestPhone()
    val f = JSONObject(alertFrame(p, 1_000_000_000)).put("sig", Codec.b64u(ByteArray(64))).toString()
    val out = p.processor().onText(f, "n")
    assertEquals(1_000_000_000L, out.reject)
    assertEquals(0L, p.store.last)
    // the next real frame still goes through
    assertEquals(1L, p.processor().onText(alertFrame(p, 1), "n").ack)
  }

  @Test fun expiredFramesAreAckedAndDroppedByTheHubClock() {
    val p = TestPhone()
    // the hub's clock is 2 h ahead of the phone's: an exp 1 h ahead of the phone is past for the hub
    p.store.setOffset(2 * 3600 * 1000L)
    val out = p.processor().onText(alertFrame(p, 1, exp = p.nowS + 3600), "n")
    assertEquals(1L, out.ack)
    assertEquals("expired", out.note)
    assertTrue(p.sink.posted.isEmpty())
    assertEquals(1L, p.store.last)
  }

  @Test fun dismissCancelsTheTagAndRefreshGoesToTheWidgets() {
    val p = TestPhone()
    val dismiss = JSONObject().put("v", 1).put("type", "dismiss").put("tag", "wy-s-c41e").put("alerts", listOf("al_01")).toString()
    assertEquals(1L, p.processor().onText(alertFrame(p, 1, dismiss), "n").ack)
    assertEquals(listOf("wy-s-c41e"), p.sink.dismissed)
    val refresh = JSONObject().put("v", 1).put("needs_you", 2).put("seats", listOf<Any>()).put("ts", "2026-10-15T09:00:00.000Z").toString()
    val f = p.hub.frame(p.device, "refresh", 2, p.nowS + 900, "refresh", p.seal(refresh))
    assertEquals(2L, p.processor().onText(f, "n").ack)
    assertEquals(2, JSONObject(p.sink.refreshed.single()).getInt("needs_you"))
    assertTrue(p.sink.posted.isEmpty())
  }

  @Test fun testAndLimitsGoToTheirChannelsAndMissedEmitsRefresh() {
    val p = TestPhone()
    val test = JSONObject(alertJson).put("type", "test")
    test.remove("actions")
    p.processor().onText(alertFrame(p, 1, test.toString(), tag = "wy-test"), "n")
    val limit = JSONObject(alertJson).put("trigger", "limit_95").put("tag", "wy-l-seat-five_hour").put("missed", 3)
    p.processor().onText(alertFrame(p, 2, limit.toString(), tag = "wy-l-seat-five_hour"), "n")
    assertEquals(Channels.TEST, p.sink.posted[0].channel)
    assertNull(p.sink.posted[0].snooze)
    assertEquals(Channels.LIMITS, p.sink.posted[1].channel)
    assertEquals(1, p.sink.missed)
  }

  @Test fun unknownTypesAreAckedAndIgnored() {
    val p = TestPhone()
    val f = p.hub.frame(p.device, "matrix", 1, p.nowS + 60, "matrix", p.seal("{}"))
    assertEquals(1L, p.processor().onText(f, "n").ack)
    val g = alertFrame(p, 2, JSONObject(alertJson).put("type", "future").toString())
    assertEquals(2L, p.processor().onText(g, "n").ack)
    assertTrue(p.sink.posted.isEmpty())
  }

  @Test fun anUndecryptableBodyIsRejected() {
    val p = TestPhone()
    val other = SoftwareAgreement.fresh()
    val body = WebPushSeal.encrypt(Codec.utf8(alertJson), other.publicKey, p.auth)
    val out = p.processor().onText(p.hub.frame(p.device, "alert", 1, p.nowS + 60, "", body), "n")
    assertEquals(1L, out.reject)
    assertEquals(0L, p.store.last)
  }

  @Test fun welcomeIsBoundToTheLoginNonceAndSetsTheClockOffset() {
    val p = TestPhone()
    val body = Codec.utf8(JSONObject().put("v", 1).put("device", p.device).put("next", 5).put("server_time", "2025-10-15T09:00:30.000Z").toString())
    val serverMs = Payloads.parseIso("2025-10-15T09:00:30.000Z")!!
    val w = p.hub.frame(p.device, "welcome", 0, serverMs / 1000 + 60, "", body, ctx = "nonce-A")
    // replayed into another login: rejected
    assertNull(p.processor().onText(w, "nonce-B").ephemeral)
    val out = p.processor().onText(w, "nonce-A")
    assertEquals("welcome", out.ephemeral?.first)
    assertEquals(serverMs - p.now, p.store.offsetMs)
  }

  @Test fun challengeAndHelloSignatures() {
    val hub = HubKey()
    val nonce = Codec.b64u(ByteArray(32) { 4 })
    val c = Login.checkChallenge(hub.challenge(nonce, 1760518860), hub.fp, hub.verifier())
    assertNotNull(c)
    // another hub, or a fingerprint that is not the pin: no hello
    val other = HubKey(ByteArray(32) { 9 })
    assertNull(Login.checkChallenge(other.challenge(nonce, 1760518860), hub.fp, hub.verifier()))
    assertNull(Login.checkChallenge(hub.challenge(nonce, 1760518860), other.fp, hub.verifier()))
    val tampered = hub.challenge(nonce, 1760518860).put("exp", 1760518861)
    assertNull(Login.checkChallenge(tampered, hub.fp, hub.verifier()))

    val signer = SoftwareSigner()
    val hello = Login.hello(c!!, "dev_X", 41, JSONObject().put("kind", "android"), signer)
    assertEquals("hello", hello.getString("t"))
    assertEquals(41, hello.getLong("last"))
    val input = Codec.utf8("wy-link-v1 hello\n${hub.fp}\n$nonce\n1760518860\ndev_X\n41")
    assertTrue(signer.verify(Codec.unb64u(hello.getString("sig"))!!, input))
  }

  @Test fun onlyAChallengeThePinnedKeyDidNotSignIsAChangedHubKey() {
    val hub = HubKey()
    val other = HubKey(ByteArray(32) { 9 })
    val nonce = Codec.b64u(ByteArray(32) { 4 })
    val v = hub.verifier()
    // 14.7 "Hub key changed" (the service stops and asks to pair again): another key signed it
    assertTrue(Login.fromAnotherHub(other.challenge(nonce, 1760518860), hub.fp, v))
    assertTrue(Login.fromAnotherHub(hub.challenge(nonce, 1760518860).put("exp", 1760518861), hub.fp, v))
    // the pinned hub's own challenge is not
    assertFalse(Login.fromAnotherHub(hub.challenge(nonce, 1760518860), hub.fp, v))
    // a malformed challenge is a bad frame (retry), not a reason to give up the pairing
    assertFalse(Login.fromAnotherHub(hub.challenge(nonce, 1760518860).put("v", 2), hub.fp, v))
    assertFalse(Login.fromAnotherHub(hub.challenge(nonce, 1760518860).put("sig", "%%"), hub.fp, v))
    assertFalse(Login.fromAnotherHub(hub.challenge("short", 1760518860), hub.fp, v))
    assertFalse(Login.fromAnotherHub(JSONObject().put("t", "challenge"), hub.fp, v))
  }

  @Test fun signedInputIsByteExact() {
    val got = Frames.signedInput("dev_1", "alert", 57, 1760540400, "wy-s-c41e", "queue", byteArrayOf(1, 2))
    val want = Codec.cat(Codec.utf8("wy-link-v1\ndev_1\nalert\n57\n1760540400\nwy-s-c41e\nqueue\n"), byteArrayOf(1, 2))
    assertEquals(Codec.hex(want), Codec.hex(got))
  }

  @Test fun offersTheSubprotocol() {
    assertEquals("wy-link.1", LinkClient.SUBPROTOCOL)
  }

  @Test fun connectionTapOpensNowNotSettings() {
    assertEquals("watchyard://now", Payloads.CONNECTION_TAP)
    assertEquals("watchyard://settings/notifications", Payloads.CONNECTION_SETTINGS)
    assertEquals("watchyard://now", Payloads.appLink(""))
    assertEquals("watchyard://now", Payloads.appLink("/#/?x=1"))
  }

  @Test fun appLinksFollowTheAllowList() {
    assertEquals("watchyard://sessions/c41e?alert=al_01", Payloads.appLink("/#/sessions/c41e?alert=al_01"))
    assertEquals("watchyard://now", Payloads.appLink("/#/"))
    assertEquals("watchyard://now", Payloads.appLink("//evil.example/#/x"))
    assertEquals("watchyard://now", Payloads.appLink("/#/\\evil"))
  }

  @Test fun payloadChannelWinsWhenKnownAndOtherwiseTheTriggerMapsIt() {
    assertEquals(Channels.DRAIN, Payloads.channelFor("alert", "limit_80", "wy_drain"))
    assertEquals(Channels.SWITCH, Payloads.channelFor("alert", "failed", "wy_switch"))
    assertEquals(Channels.TEST, Payloads.channelFor("alert", "failed", "wy_test"))
    assertEquals(Channels.LIMITS, Payloads.channelFor("alert", "limit_80", "wy_nonsense"))
    assertEquals(Channels.NEEDS_YOU, Payloads.channelFor("alert", "failed", Channels.CONNECTION))
    assertEquals(Channels.MACHINES, Payloads.channelFor("alert", "machine_silent", null))
    assertEquals(Channels.NEEDS_YOU, Payloads.channelFor("summary", null, ""))
    assertEquals(Channels.TEST, Payloads.channelFor("test", null, "wy_nonsense"))
  }

  @Test fun everyKindMapsToItsIcon() {
    fun k(type: String, trigger: String?) = Payloads.iconKindFor(type, trigger)
    assertEquals(IconKind.NEEDS_YOU, k("alert", "needs_permission"))
    assertEquals(IconKind.NEEDS_YOU, k("alert", "asked"))
    assertEquals(IconKind.FAILED, k("alert", "failed"))
    assertEquals(IconKind.LIMIT, k("alert", "limit_80"))
    assertEquals(IconKind.LIMIT, k("alert", "limit_95"))
    assertEquals(IconKind.RESET, k("alert", "reset"))
    assertEquals(IconKind.MACHINE, k("alert", "machine_silent"))
    for (t in listOf("limit_change", "usage_reset", "seat_change", "weekly_drop")) {
      assertEquals(IconKind.LIMIT, k("alert", t))
      assertEquals(Channels.USAGE, Payloads.channelFor("alert", t))
    }
    for (t in listOf("session_moved", "switch_digest")) {
      assertEquals(IconKind.MACHINE, k("alert", t))
      assertEquals(Channels.SWITCH, Payloads.channelFor("alert", t))
    }
    for (t in listOf("drain_started", "drain_ended", "drain_pulled")) {
      assertEquals(IconKind.LIMIT, k("alert", t))
      assertEquals(Channels.DRAIN, Payloads.channelFor("alert", t))
    }
    assertEquals(IconKind.SUMMARY, k("summary", null))
    assertEquals(IconKind.SUMMARY, k("summary", "asked"))
    assertEquals(IconKind.GENERIC, k("test", null))
    assertEquals(IconKind.GENERIC, k("test", "failed"))
    assertEquals(IconKind.GENERIC, k("alert", null))
    assertEquals(IconKind.GENERIC, k("alert", "something_new"))
  }

  @Test fun aParsedAlertCarriesItsTriggerIntoTheIcon() {
    val p = TestPhone()
    p.processor().onText(alertFrame(p, 1), "nonce")
    assertEquals("asked", p.sink.posted[0].trigger)
    assertEquals(IconKind.NEEDS_YOU, p.sink.posted[0].iconKind)
  }

  @Test fun everyBadgedKindHasBadgePathData() {
    for (k in listOf("needs_you", "failed", "limit", "reset", "machine", "summary")) {
      assertTrue(k, NotifyIconPaths.BADGES[k]!!.startsWith("M"))
    }
    assertTrue(NotifyIconPaths.MARK.startsWith("M"))
  }

  @Test fun theConnectionChannelIsNamedAlwaysOn() {
    assertEquals("Connection (always on)", Channels.ALL.first { it.first == Channels.CONNECTION }.second)
    assertEquals(1, Channels.ALL.first { it.first == Channels.CONNECTION }.third)
  }
}

class RemoteUrlTest {
  @Test fun onlyClaudeAiHttpsLinksSurvive() {
    for (ok in listOf("https://claude.ai/code/session_1", "https://claude.ai", "https://CLAUDE.AI:443/x", "https://www.claude.ai/a?b=c")) {
      org.junit.Assert.assertEquals(ok, Payloads.claudeUrl(ok))
    }
    for (bad in listOf(
      null, "", "http://claude.ai/x", "https://evil.example/x", "https://claude.ai.evil.example/x", "https://evilclaude.ai/x",
      "https://user@claude.ai/x", "https://claude.ai:8443/x", "https://claude.ai\\@evil.example/", "https://claude.ai/a b",
      "intent://claude.ai/#Intent;scheme=https;end", "javascript://claude.ai/%0aalert(1)", "//claude.ai/x",
    )) {
      org.junit.Assert.assertNull(bad, Payloads.claudeUrl(bad))
    }
  }

  @Test fun anAlertWithAForeignRemoteUrlKeepsNoLink() {
    val o = org.json.JSONObject().put("v", 1).put("type", "alert").put("tap", "claude").put("remote_url", "https://evil.example/x")
    org.junit.Assert.assertNull(Payloads.alert(o, "t")!!.remoteUrl)
    o.put("remote_url", "https://claude.ai/code/s")
    org.junit.Assert.assertEquals("https://claude.ai/code/s", Payloads.alert(o, "t")!!.remoteUrl)
  }
}
