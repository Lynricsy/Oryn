package com.lynricsy.oryn.mobile

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PushRecoveryTest {
  private val url = "/t/T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

  private fun n(id: String, createdAt: Long) = JSONObject()
    .put("id", id).put("kind", "input").put("title", "t-$id").put("body", "b-$id").put("url", url)
    .put("createdAt", createdAt).put("readAt", JSONObject.NULL).put("resolvedAt", JSONObject.NULL)

  /** 假服务端：未读通知按 createdAt 降序，`before` 只返回更早的，每页 [PushRecovery.LIMIT] 条。 */
  private class Server(val all: List<JSONObject>) {
    val requests = mutableListOf<Long?>()
    fun page(before: Long?): JSONObject {
      requests.add(before)
      val items = all.filter { before == null || it.getLong("createdAt") < before }.take(PushRecovery.LIMIT)
      return JSONObject().put("notifications", JSONArray(items))
    }
  }

  /** 最近 50 条未读都已显示过：更早的那条没显示过的未读仍要拉到并弹出。 */
  @Test
  fun unseenOnSecondPageIsShown() {
    val seen = (0 until PushRecovery.LIMIT).map { n("s$it", 10_000L - it) }
    val server = Server(seen + n("old", 100))
    val shownIds = seen.map { it.getString("id") }.toSet()
    val got = PushRecovery.collect(server::page) { it in shownIds }
    assertEquals(listOf("old"), got.models.map { it.id })
    assertFalse(got.truncated)
    assertEquals(listOf(null, 10_000L - (PushRecovery.LIMIT - 1)), server.requests)
  }

  /** 拉满 200 条且最后一页仍是满页：可能还有更多，要补通用提醒；不再请求第 5 页。 */
  @Test
  fun capWithFullLastPageIsTruncated() {
    val server = Server((0 until 260).map { n("n$it", 100_000L - it) })
    val got = PushRecovery.collect(server::page) { false }
    assertTrue(got.truncated)
    assertEquals(PushRecovery.MAX_PAGES, server.requests.size)
    assertEquals(PushRecovery.LIMIT * PushRecovery.MAX_PAGES, got.models.size)
    // 旧 → 新显示（同一线程后到的覆盖先到的）
    assertEquals("n199", got.models.first().id)
    assertEquals("n0", got.models.last().id)
  }

  /** 恰好 200 条：第 4 页是满页，无法确定后面没有了，同样提醒。 */
  @Test
  fun exactlyAtCapStillReportsTruncated() {
    val server = Server((0 until 200).map { n("n$it", 100_000L - it) })
    assertTrue(PushRecovery.collect(server::page) { false }.truncated)
    assertEquals(PushRecovery.MAX_PAGES, server.requests.size)
  }

  @Test
  fun shortPageRequestsOnce() {
    val server = Server(listOf(n("a", 3), n("b", 2)))
    val got = PushRecovery.collect(server::page) { false }
    assertEquals(listOf<Long?>(null), server.requests)
    assertFalse(got.truncated)
    assertEquals(listOf("b", "a"), got.models.map { it.id })
  }
}
