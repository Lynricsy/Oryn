package com.lynricsy.oryn.mobile

import android.content.Context
import org.json.JSONObject
import java.io.IOException

/**
 * FCM 离线期间积压超过 100 条非折叠消息时，FCM 全部丢弃并在设备上线后回调 `onDeletedMessages()`。
 * 这时用设备令牌补拉 `GET /api/v1/notifications?limit=50`，把未读（`input` 另要求未作答）且没显示过的通知
 * 按推送同一套流程显示（同一线程靠本地 tag 合并）；拉不到就显示一条不含内容的「Oryn 有新通知」。
 */
object PushRecovery {
  const val LIMIT = 50

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
    val models = try {
      fetch(context)
    } catch (e: Exception) {
      EventLog.log(context, "push.recover_error", mapOf("error" to e.javaClass.simpleName))
      null
    }
    if (models == null) {
      if (!OrynMobilePlugin.visible) Notifier.postGeneric(context)
      return
    }
    for (m in models) PushDelivery.show(context, m, "fcm-recover")
  }

  @Throws(IOException::class)
  private fun fetch(context: Context): List<PushPayload.Model>? {
    val server = PushStore.server(context) ?: return null
    val token = Vault.get(context, server) ?: return null
    val resp = OrynHttp.call(context, "GET", "$server/api/v1/notifications?limit=$LIMIT", token)
    if (resp.status !in 200..299) throw IOException("http ${resp.status}")
    return pending(JSONObject(resp.body)) { PushStore.wasShown(context, it) }
  }
}
