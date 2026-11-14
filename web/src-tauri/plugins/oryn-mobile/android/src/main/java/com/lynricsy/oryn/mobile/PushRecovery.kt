package com.lynricsy.oryn.mobile

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException

/**
 * FCM 离线期间积压超过 100 条非折叠消息时，FCM 全部丢弃并在设备上线后回调 `onDeletedMessages()`。
 * 这时用设备令牌补拉未读通知 `GET /api/v1/notifications?unread=true&limit=50`，按 `before` 翻页（见 [collect]），
 * 把未作答（`input`）且没显示过的通知按推送同一套流程显示（同一线程靠本地 tag 合并）；
 * 拉不到、或到了 [MAX_PAGES] 页上限还可能有更多时，显示一条不含内容的「Oryn 有新通知」。
 */
object PushRecovery {
  const val LIMIT = 50
  /** 最多翻几页（共 200 条）。 */
  const val MAX_PAGES = 4

  /** 补拉结果：要显示的通知，以及是否因页数上限没拉完（最后一页仍是满页）。 */
  data class Recovered(val models: List<PushPayload.Model>, val truncated: Boolean)

  /**
   * 逐页取未读通知直到某页不足 [LIMIT] 条或满 [MAX_PAGES] 页。`page(before)` 取一页（第一页 `before` 为 null，
   * 之后为上一页最后一条的 `createdAt`；服务端按 `createdAt` 降序），失败时抛异常。纯函数（页面获取由参数注入）。
   */
  fun collect(page: (Long?) -> JSONObject, shown: (String) -> Boolean): Recovered {
    val all = JSONArray()
    var before: Long? = null
    var truncated = true
    for (i in 0 until MAX_PAGES) {
      val items = page(before).optJSONArray("notifications") ?: JSONArray()
      for (j in 0 until items.length()) all.put(items.get(j))
      val last = items.optJSONObject(items.length() - 1)
      if (items.length() < LIMIT || last == null || !last.has("createdAt")) {
        truncated = items.length() >= LIMIT
        break
      }
      before = last.getLong("createdAt")
    }
    return Recovered(pending(JSONObject().put("notifications", all), shown), truncated)
  }

  /** 列表响应 → 要显示的通知（旧 → 新，同一线程后到的覆盖先到的）。纯函数。 */
  fun pending(list: JSONObject, shown: (String) -> Boolean): List<PushPayload.Model> {
    val items = list.optJSONArray("notifications") ?: return emptyList()
    val out = ArrayList<PushPayload.Model>()
    for (i in items.length() - 1 downTo 0) {
      val n = items.optJSONObject(i) ?: continue
      if (!n.isNull("readAt")) continue
      if (n.optString("kind") == "input" && !n.isNull("resolvedAt")) continue
      val payload = JSONObject()
        .put("v", 1)
        .put("id", n.optString("id"))
        .put("kind", n.optString("kind"))
        .put("title", n.optString("title"))
        .put("body", n.optString("body"))
        .put("url", n.optString("url"))
      val model = PushPayload.parse(payload.toString().toByteArray(Charsets.UTF_8)) ?: continue
      if (model.id.isEmpty() || shown(model.id)) continue
      out.add(model)
    }
    return out
  }

  /** 在推送服务的后台线程上调用。 */
  fun recover(context: Context) {
    EventLog.log(context, "push.recover")
    val got = try {
      fetch(context)
    } catch (e: Exception) {
      EventLog.log(context, "push.recover_error", mapOf("error" to e.javaClass.simpleName))
      null
    }
    if (got == null) {
      if (!OrynMobilePlugin.visible) Notifier.postGeneric(context)
      return
    }
    for (m in got.models) PushDelivery.show(context, m, "fcm-recover")
    // 到了页数上限还是满页：可能还有更早的未读，补一条不含内容的提醒
    if (got.truncated) {
      EventLog.log(context, "push.recover_truncated", mapOf("shown" to got.models.size))
      if (!OrynMobilePlugin.visible) Notifier.postGeneric(context)
    }
  }

  @Throws(IOException::class)
  private fun fetch(context: Context): Recovered? {
    val server = PushStore.server(context) ?: return null
    val token = Vault.get(context, server) ?: return null
    return collect({ before ->
      val cursor = if (before == null) "" else "&before=$before"
      val resp = OrynHttp.call(context, "GET", "$server/api/v1/notifications?unread=true&limit=$LIMIT$cursor", token)
      if (resp.status !in 200..299) throw IOException("http ${resp.status}")
      JSONObject(resp.body)
    }) { PushStore.wasShown(context, it) }
  }
}
