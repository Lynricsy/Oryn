package com.lynricsy.oryn.mobile

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class PushSyncTest {
  private fun state(vararg kv: Pair<String, Any?>) =
    JSONObject().apply { for ((k, v) in kv) put(k, v ?: JSONObject.NULL) }

  @Test
  fun fcmRegistersTokenWithKeys() {
    val r = PushSync.request(
      state("enabled" to true, "channel" to "fcm", "fcmToken" to "tok", "p256dh" to "pk", "auth" to "au"),
    )!!
    assertEquals("tok", r.key)
    assertEquals(
      JSONObject("""{"kind":"fcm","token":"tok","keys":{"p256dh":"pk","auth":"au"}}""").toString(),
      JSONObject(r.body.toString()).toString(),
    )
  }

  @Test
  fun unifiedPushRegistersEndpoint() {
    val r = PushSync.request(
      state("enabled" to true, "channel" to "unifiedpush", "endpoint" to "https://up/x", "p256dh" to "pk", "auth" to "au"),
    )!!
    assertEquals("https://up/x", r.key)
    assertEquals("https://up/x", r.body.getString("endpoint"))
    assertEquals("pk", r.body.getJSONObject("keys").getString("p256dh"))
    assertTrue(!r.body.has("kind"))
  }

  @Test
  fun incompleteOrDisabledRegistersNothing() {
    val full = arrayOf<Pair<String, Any?>>("enabled" to true, "channel" to "fcm", "fcmToken" to "tok", "p256dh" to "pk", "auth" to "au")
    assertNull(PushSync.request(state(*full.map { if (it.first == "enabled") it.first to false else it }.toTypedArray())))
    // FCM 没有密钥：绝不登记明文订阅
    assertNull(PushSync.request(state(*full.map { if (it.first == "p256dh") it.first to null else it }.toTypedArray())))
    assertNull(PushSync.request(state(*full.map { if (it.first == "fcmToken") it.first to null else it }.toTypedArray())))
    assertNull(PushSync.request(state(*full.map { if (it.first == "channel") it.first to null else it }.toTypedArray())))
  }

  @Test
  fun recoveryShowsUnreadUnansweredUnseenOldestFirst() {
    fun n(id: String, kind: String, read: Boolean = false, resolved: Boolean = false) = JSONObject()
      .put("id", id).put("kind", kind).put("title", "t-$id").put("body", "b-$id").put("url", "/t/T-01932f6e-8d1a-7c3b-9f2e-0123456789ab")
      .put("createdAt", 1).put("readAt", if (read) 5 else JSONObject.NULL).put("resolvedAt", if (resolved) 5 else JSONObject.NULL)
      .put("deliveries", JSONArray())
    // 服务端按创建时间降序
    val list = JSONObject().put("unread", 3).put(
      "notifications",
      JSONArray()
        .put(n("n5", "done"))
        .put(n("n4", "input", resolved = true))
        .put(n("n3", "error", read = true))
        .put(n("n2", "input"))
        .put(n("n1", "done")),
    )
    val out = PushRecovery.pending(list) { it == "n1" }
    assertEquals(listOf("n2", "n5"), out.map { it.id })
    assertEquals("t-n2", out[0].title)
    assertEquals("oryn-0123456789ab", out[0].tag)
    assertEquals(emptyList<PushPayload.Model>(), PushRecovery.pending(JSONObject("{}")) { false })
  }

  @Test
  fun shownIdsKeepTheNewestWithoutDuplicates() {
    var ids = emptyList<String>()
    for (i in 0 until PushStore.SHOWN_KEEP + 5) ids = PushStore.appendShown(ids, "n$i")
    assertEquals(PushStore.SHOWN_KEEP, ids.size)
    assertEquals("n5", ids.first())
    ids = PushStore.appendShown(ids, "n5")
    assertEquals("n5", ids.last())
    assertEquals(PushStore.SHOWN_KEEP, ids.size)
  }
}
