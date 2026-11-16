package com.lynricsy.oryn.mobile

import org.json.JSONArray
import org.json.JSONException
import org.json.JSONObject

/**
 * M5 推送载荷：`{"v":1,"id","kind","title","body","url","tag","actions"?}`；`url` 为站内路径（线程 `/t/<threadId>`，
 * 无线程的系统通知如预算 `/stats/usage`、定时 `/settings/automations`）。可一触作答的 `input` 带
 * `actions: [{i, label, role?}]`（2–3 个，`role` 只有 confirm 的 `yes` / `no`）；撤回消息为
 * `{"v":1,"kind":"withdraw","id","tag"}`（见 [withdrawal]）。
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
    val kind: String = "",
    val actions: List<Action> = emptyList(),
  )

  /** 通知上的作答按钮：`index` 是选项序号（作答时原样回传），`role` 为 `yes` / `no` 时按语言显示「是 / 否」。 */
  data class Action(val index: Int, val label: String, val role: String? = null)

  /** 撤回：只撤掉 `tag` 上仍显示着 `id` 这条通知的那一条。 */
  data class Withdrawal(val id: String, val tag: String)

  /** 与服务端一致：2–3 个按钮。 */
  private const val MIN_ACTIONS = 2
  private const val MAX_ACTIONS = 3

  private val THREAD_PATH = Regex("^/t/([^/?#]+)$")
  private val THREAD_LINK = Regex("^/t/(T-[0-9a-fA-F-]{36})$")

  /** 解析载荷；不是 JSON 对象、缺 `title`、`url` 不以 `/` 开头时返回 null。 */
  fun parse(bytes: ByteArray): Model? {
    val obj = json(bytes) ?: return null
    val title = text(obj, "title") ?: return null
    val path = text(obj, "url") ?: return null
    if (!path.startsWith("/")) return null
    val id = text(obj, "id") ?: ""
    val body = text(obj, "body") ?: ""
    val tag = text(obj, "tag") ?: tagFor(path, id)
    val kind = text(obj, "kind") ?: ""
    val actions = if (kind == "input" && id.isNotEmpty()) actions(obj.optJSONArray("actions")) else emptyList()
    return Model(id = id, title = title, body = body, path = path, tag = tag, kind = kind, actions = actions)
  }

  /** 撤回消息（`kind: "withdraw"`、`id` 与 `tag` 非空）；其它载荷返回 null。 */
  fun withdrawal(bytes: ByteArray): Withdrawal? {
    val obj = json(bytes) ?: return null
    if (text(obj, "kind") != "withdraw") return null
    val id = text(obj, "id") ?: return null
    val tag = text(obj, "tag") ?: return null
    return Withdrawal(id, tag)
  }

  /** 把通知序列化回载荷 JSON（作答按钮的 PendingIntent 带着它，作答后据此重绘通知）；[parse] 可原样解回。 */
  fun encode(model: Model): String {
    val acts = JSONArray()
    for (a in model.actions) {
      val o = JSONObject().put("i", a.index).put("label", a.label)
      if (a.role != null) o.put("role", a.role)
      acts.put(o)
    }
    return JSONObject()
      .put("v", 1)
      .put("id", model.id)
      .put("kind", model.kind)
      .put("title", model.title)
      .put("body", model.body)
      .put("url", model.path)
      .put("tag", model.tag)
      .put("actions", acts)
      .toString()
  }

  /** 按钮须 2–3 个，每个有非负整数 `i` 与非空 `label`；任一不合法则整组不要（只显示普通通知）。 */
  private fun actions(arr: JSONArray?): List<Action> {
    if (arr == null || arr.length() < MIN_ACTIONS || arr.length() > MAX_ACTIONS) return emptyList()
    val out = ArrayList<Action>(arr.length())
    for (k in 0 until arr.length()) {
      val o = arr.optJSONObject(k) ?: return emptyList()
      val i = o.opt("i")
      if (i !is Int || i < 0) return emptyList()
      val label = text(o, "label") ?: return emptyList()
      val role = text(o, "role")?.takeIf { it == "yes" || it == "no" }
      out.add(Action(i, label, role))
    }
    return out
  }

  private fun json(bytes: ByteArray): JSONObject? = try {
    JSONObject(String(bytes, Charsets.UTF_8))
  } catch (_: JSONException) {
    null
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
    if (v is JSONObject || v is JSONArray) return null
    return v.toString().takeIf { it.isNotEmpty() }
  }
}
