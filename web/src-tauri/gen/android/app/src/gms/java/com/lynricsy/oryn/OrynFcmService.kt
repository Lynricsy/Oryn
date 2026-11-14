package com.lynricsy.oryn

import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import com.lynricsy.oryn.mobile.EventLog
import com.lynricsy.oryn.mobile.OrynMobilePlugin
import com.lynricsy.oryn.mobile.PushDelivery
import com.lynricsy.oryn.mobile.PushStore
import org.json.JSONObject

/**
 * FCM 通道：服务端发 data-only 高优先级消息，`data` 的键与 M5 推送载荷同名（值都是字符串），
 * 转成同样的 JSON 字节后交给插件的 PushPayload / Notifier，与 UnifiedPush 同一条路。
 */
class OrynFcmService : FirebaseMessagingService() {
  override fun onNewToken(token: String) {
    // 只有启用了推送才记令牌；之后由 Rust 同步到服务端
    if (!PushStore.isEnabled(this)) return
    PushStore.setFcmToken(this, token)
    EventLog.log(this, "fcm.token")
    OrynMobilePlugin.emitPush()
  }

  override fun onMessageReceived(message: RemoteMessage) {
    if (!PushStore.isEnabled(this)) {
      EventLog.log(this, "push.dropped", mapOf("reason" to "disabled", "via" to "fcm"))
      return
    }
    PushDelivery.deliver(this, dataToJson(message.data), "fcm")
  }

  companion object {
    fun dataToJson(data: Map<String, String>): ByteArray =
      JSONObject(data as Map<*, *>).toString().toByteArray(Charsets.UTF_8)
  }
}
