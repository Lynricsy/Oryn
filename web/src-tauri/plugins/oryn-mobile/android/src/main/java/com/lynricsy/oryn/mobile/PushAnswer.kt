package com.lynricsy.oryn.mobile

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import org.json.JSONObject
import java.io.IOException
import kotlin.concurrent.thread

/**
 * 通知上的作答按钮（`docs/mobile.md`「推送 / 在通知上作答」）：点按钮 → [AnswerReceiver]（不拉起界面）→
 * 用设备令牌调 `POST <服务器>/api/v1/notifications/{id}/answer {option}` → 按结果重绘通知（见 [Notifier.State]）。
 * 服务器与令牌只来自 [PushStore.server] 与 [Vault]，与后台登记订阅、FCM 补拉同一来源。
 */
object PushAnswer {
  /**
   * 单次请求的连接 / 读取超时。广播接收器 goAsync 之后只有约 10 秒，超时会被判 ANR；两段各 4 秒留出余量。
   */
  const val TIMEOUT_MS = 4_000

  private val NOTIFICATION_ID = Regex("^ntf_[0-9A-Za-z]{1,64}$")

  /** 作答地址；通知 id 不是 `ntf_<字母数字>` 时返回 null（id 来自推送载荷，不拼进路径以外的任何东西）。 */
  fun url(server: String, id: String): String? =
    if (NOTIFICATION_ID.matches(id)) "${server.trimEnd('/')}/api/v1/notifications/$id/answer" else null

  /**
   * HTTP 状态 → 通知的新状态：2xx 已作答；409 已在别处作答；408 / 429 / 5xx 可重试（保留按钮）；其余 4xx
   * （令牌失效、请求已不可作答等）不可重试。纯函数。
   */
  fun stateFor(status: Int): Notifier.State = when {
    status in 200..299 -> Notifier.State.ANSWERED
    status == 409 -> Notifier.State.ELSEWHERE
    status == 408 || status == 429 || status >= 500 -> Notifier.State.RETRY
    else -> Notifier.State.FAILED
  }

  /** 在后台线程上调用；网络错误按可重试处理。 */
  fun answer(context: Context, id: String, option: Int): Notifier.State {
    val server = PushStore.server(context) ?: return Notifier.State.FAILED
    val token = Vault.get(context, server) ?: return Notifier.State.FAILED
    val url = url(server, id) ?: return Notifier.State.FAILED
    return try {
      val resp = OrynHttp.call(context, "POST", url, token, JSONObject().put("option", option), TIMEOUT_MS)
      EventLog.log(context, "push.answer", mapOf("id" to id, "status" to resp.status))
      stateFor(resp.status)
    } catch (e: IOException) {
      EventLog.log(context, "push.answer_error", mapOf("id" to id, "error" to e.javaClass.simpleName))
      Notifier.State.RETRY
    }
  }
}

/** 作答按钮的广播接收器（清单里 `exported=false`，只收本应用 PendingIntent 发来的显式广播）。 */
class AnswerReceiver : BroadcastReceiver() {
  override fun onReceive(context: Context, intent: Intent) {
    if (intent.action != Notifier.ACTION_ANSWER) return
    val payload = intent.getStringExtra(Notifier.EXTRA_PAYLOAD) ?: return
    val model = PushPayload.parse(payload.toByteArray(Charsets.UTF_8)) ?: return
    val option = intent.getIntExtra(Notifier.EXTRA_OPTION, -1)
    if (option < 0 || model.actions.none { it.index == option }) return
    val app = context.applicationContext
    // 先去掉按钮并显示「正在提交」，防止连点重复作答
    Notifier.update(app, model, Notifier.State.SENDING)
    val pending = goAsync()
    thread(name = "oryn-answer") {
      try {
        Notifier.update(app, model, PushAnswer.answer(app, model.id, option))
      } finally {
        pending.finish()
      }
    }
  }
}
