package com.lynricsy.oryn.mobile

import org.json.JSONException
import org.json.JSONObject

/**
 * M5 推送载荷：`{"v":1,"id","kind","title","body","url","tag"}`；`url` 为站内路径（线程 `/t/<threadId>`，
 * 无线程的系统通知如预算 `/stats/usage`、定时 `/settings/automations`）。
 * UnifiedPush 送来的是解密后的 JSON 字节；FCM 的 data 字段同名、值全为字符串，
 * 由 [OrynFcmService] 转成同样的 JSON 字节再交给这里。纯函数，不依赖 Android。
 */
object PushPayload {
  data class Model(
    val id: String,
    val title: String,
    val body: String,
    val path: String,
    val tag: String,
  )

  private val THREAD_PATH = Regex("^/t/([^/?#]+)$")
  private val THREAD_LINK = Regex("^/t/(T-[0-9a-fA-F-]{36})$")

  /** 解析载荷；不是 JSON 对象、缺 `title`、`url` 不以 `/` 开头时返回 null。 */
  fun parse(bytes: ByteArray): Model? {
    val obj = try {
      JSONObject(String(bytes, Charsets.UTF_8))
    } catch (_: JSONException) {
      return null
    }
    val title = text(obj, "title") ?: return null
    val path = text(obj, "url") ?: return null
    if (!path.startsWith("/")) return null
    val id = text(obj, "id") ?: ""
    val body = text(obj, "body") ?: ""
    val tag = text(obj, "tag") ?: tagFor(path, id)
    return Model(id = id, title = title, body = body, path = path, tag = tag)
  }

  /**
   * 与服务端同规则：线程路径取线程 id 末 12 位；`/stats…` → `oryn-budget`；`/settings/automations…` →
   * `oryn-automations`；其余用通知 id。`host_notify` 与推送因此落在同一条通知上（载荷自带 `tag` 时以载荷为准）。
   */
  fun tagFor(path: String, id: String): String {
    val thread = THREAD_PATH.find(path)?.groupValues?.get(1)
    return when {
      thread != null -> "oryn-" + thread.takeLast(12)
      under(path, STATS) -> "oryn-budget"
      under(path, AUTOMATIONS) -> "oryn-automations"
      else -> "oryn-$id"
    }
  }

  /**
   * 通知点击的深链接：合法线程路径 → `oryn://thread/<id>`；`/stats…` → `oryn://stats`；
   * `/settings/automations…` → `oryn://automations`；其余回首页。深链接来源不可信，落点是封闭集合，不透传任意路径。
   */
  fun deepLink(path: String): String {
    val thread = THREAD_LINK.find(path)?.groupValues?.get(1)
    return when {
      thread != null -> "oryn://thread/$thread"
      under(path, STATS) -> "oryn://stats"
      under(path, AUTOMATIONS) -> "oryn://automations"
      else -> "oryn://open"
    }
  }

  private const val STATS = "/stats"
  private const val AUTOMATIONS = "/settings/automations"

  /** `path` 等于 `prefix`，或在其后紧跟 `/`、`?`、`#`（`/statsx` 不算 `/stats`）。 */
  private fun under(path: String, prefix: String): Boolean {
    if (!path.startsWith(prefix)) return false
    return path.length == prefix.length || path[prefix.length] in "/?#"
  }

  /** 取非空字符串字段；数字等其它标量按字符串处理，null / 缺失 / 空串为 null。 */
  private fun text(obj: JSONObject, key: String): String? {
    if (!obj.has(key) || obj.isNull(key)) return null
    val v = obj.get(key)
    if (v is JSONObject || v is org.json.JSONArray) return null
    return v.toString().takeIf { it.isNotEmpty() }
  }
}
