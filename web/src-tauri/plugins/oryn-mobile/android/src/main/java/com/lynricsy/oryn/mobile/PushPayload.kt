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
  const val AVATAR_THREAD = "thread"
  const val AVATAR_ASSISTANT = "assistant"
  const val AVATAR_SYSTEM = "system"

  /** 通知发送者头像元数据：普通线程用标题卡图标与线程色；全局助手主线与线程用全局助手头像。 */
  data class Avatar(
    val kind: String,
    val icon: String? = null,
    val color: String? = null,
    val image: String? = null,
    val hash: String? = null,
  )

  data class Model(
    val id: String,
    val title: String,
    val body: String,
    val path: String,
    val tag: String,
    val kind: String = "",
    val actions: List<Action> = emptyList(),
    val timestamp: Long = 0L,
    val avatar: Avatar? = null,
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
  private val TITLE_CARD = Regex("^(\\S+) ([A-Z0-9]{1,6}): (\\S.*)$")
  private val VALID_COLORS = setOf("red", "orange", "yellow", "green", "teal", "blue", "purple", "pink")

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
    val timestamp = longValue(obj, "ts")
    val avatar = parseAvatar(obj.optJSONObject("avatar"))
    return Model(
      id = id,
      title = title,
      body = body,
      path = path,
      tag = tag,
      kind = kind,
      actions = actions,
      timestamp = timestamp,
      avatar = avatar,
    )
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
    val root = JSONObject()
      .put("v", 1)
      .put("id", model.id)
      .put("kind", model.kind)
      .put("title", model.title)
      .put("body", model.body)
      .put("url", model.path)
      .put("tag", model.tag)
      .put("actions", acts)
    if (model.timestamp > 0L) root.put("ts", model.timestamp)
    // 刻意不把大体积 image base64 放进 Intent extras（防 Binder TransactionTooLargeException）
    model.avatar?.let { av ->
      val ao = JSONObject().put("kind", av.kind)
      if (av.icon != null) ao.put("icon", av.icon)
      if (av.color != null) ao.put("color", av.color)
      if (av.hash != null) ao.put("hash", av.hash)
      root.put("avatar", ao)
    }
    return root.toString()
  }

  /** 解析 `avatar` 对象；只接受合法的 kind / icon / color / image / hash。 */
  fun parseAvatar(obj: JSONObject?): Avatar? {
    if (obj == null) return null
    val kind = text(obj, "kind") ?: return null
    if (kind != AVATAR_THREAD && kind != AVATAR_ASSISTANT && kind != AVATAR_SYSTEM) return null
    val icon = text(obj, "icon")?.takeIf { it.toByteArray(Charsets.UTF_8).size <= 64 }
    val color = text(obj, "color")?.takeIf { it in VALID_COLORS }
    val image = text(obj, "image")?.takeIf { it.length <= 192 * 1024 }
    val hash = text(obj, "hash")?.takeIf { it.length <= 32 }
    return Avatar(kind = kind, icon = icon, color = color, image = image, hash = hash)
  }

  /** 生效头像：优先使用载荷自带的 `avatar`，缺失（旧服务端）时按路径与标题卡兜底推导。 */
  fun effectiveAvatar(model: Model): Avatar {
    model.avatar?.let { return it }
    return when {
      under(model.path, "/settings/assistant") || under(model.path, "/assistant") ->
        Avatar(kind = AVATAR_ASSISTANT)
      model.path.startsWith("/t/") ->
        Avatar(kind = AVATAR_THREAD, icon = parseTitleCardIcon(model.title))
      else -> Avatar(kind = AVATAR_SYSTEM)
    }
  }

  /** MessagingStyle 的发送者显示名：普通线程头像已展示标题卡图标时，去掉标题开头重复的 `<图标> ` 前缀。 */
  fun displayTitle(title: String, avatar: Avatar?): String {
    if (avatar?.kind == AVATAR_THREAD && !avatar.icon.isNullOrEmpty()) {
      val prefix = "${avatar.icon} "
      if (title.startsWith(prefix) && title.length > prefix.length) {
        return title.substring(prefix.length)
      }
    }
    return title
  }

  /** 从 omp 标题卡（`<图标> <代码>: <标题>`）提取单个非 ASCII 图标字形；不匹配返回 null。 */
  fun parseTitleCardIcon(title: String): String? {
    val m = TITLE_CARD.matchEntire(title) ?: return null
    val icon = m.groupValues[1]
    if (icon.isEmpty() || icon.toByteArray(Charsets.UTF_8).size > 16) return null
    if (icon.any { it.code < 0x80 }) return null
    return icon
  }

  /** 判断图标是否以 Nerd Font 私有区码点开头（BMP PUA 或补充私有区 A/B）。 */
  fun isNerdGlyph(icon: String): Boolean {
    if (icon.isEmpty()) return false
    val cp = icon.codePointAt(0)
    return (cp in 0xE000..0xF8FF) || (cp in 0xF0000..0xFFFFD)
  }

  private fun longValue(obj: JSONObject, key: String): Long {
    if (!obj.has(key) || obj.isNull(key)) return 0L
    val v = obj.get(key)
    return when (v) {
      is Number -> v.toLong().takeIf { it > 0L } ?: 0L
      is String -> v.toLongOrNull()?.takeIf { it > 0L } ?: 0L
      else -> 0L
    }
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
