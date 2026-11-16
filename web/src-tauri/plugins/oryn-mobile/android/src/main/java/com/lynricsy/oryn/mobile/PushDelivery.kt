package com.lynricsy.oryn.mobile

import android.content.Context

/** 推送到达的统一处理：UnifiedPush（[OrynPushService]）与 FCM（应用 gms flavor 的 `OrynFcmService`）都走这里。 */
object PushDelivery {
  /** 解密后的载荷 JSON 字节。撤回消息不弹通知、也不受前台与去重限制：只撤掉仍显示着的那一条。 */
  fun deliver(context: Context, content: ByteArray, channel: String) {
    val w = PushPayload.withdrawal(content)
    if (w != null) {
      val hit = Notifier.withdraw(context, w)
      EventLog.log(context, "push.withdrawn", mapOf("id" to w.id, "hit" to hit, "via" to channel))
      return
    }
    val model = PushPayload.parse(content)
    if (model == null) {
      EventLog.log(context, "push.dropped", mapOf("reason" to "invalid", "via" to channel))
      return
    }
    show(context, model, channel)
  }

  /** 显示一条通知（推送与 FCM 补拉共用）；同一 id 只显示一次。 */
  fun show(context: Context, model: PushPayload.Model, channel: String) {
    // App 在前台时由站内 toast 提示，不重复发系统通知
    if (OrynMobilePlugin.visible) {
      EventLog.log(context, "push.dropped", mapOf("reason" to "foreground", "id" to model.id, "via" to channel))
      return
    }
    if (PushStore.wasShown(context, model.id)) {
      EventLog.log(context, "push.dropped", mapOf("reason" to "duplicate", "id" to model.id, "via" to channel))
      return
    }
    EventLog.log(context, "push.received", mapOf("id" to model.id, "via" to channel))
    if (Notifier.post(context, model) == null) PushStore.markShown(context, model.id)
  }
}
