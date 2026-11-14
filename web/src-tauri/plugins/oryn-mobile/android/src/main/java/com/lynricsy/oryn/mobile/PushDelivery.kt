package com.lynricsy.oryn.mobile

import android.content.Context

/** 推送到达的统一处理：UnifiedPush（[OrynPushService]）与 FCM（应用 gms flavor 的 `OrynFcmService`）都走这里。 */
object PushDelivery {
  fun deliver(context: Context, content: ByteArray, channel: String) {
    val model = PushPayload.parse(content)
    if (model == null) {
      EventLog.log(context, "push.dropped", mapOf("reason" to "invalid", "via" to channel))
      return
    }
    // App 在前台时由站内 toast 提示，不重复发系统通知
    if (OrynMobilePlugin.visible) {
      EventLog.log(context, "push.dropped", mapOf("reason" to "foreground", "id" to model.id, "via" to channel))
      return
    }
    EventLog.log(context, "push.received", mapOf("id" to model.id, "via" to channel))
    Notifier.post(context, model)
  }
}
