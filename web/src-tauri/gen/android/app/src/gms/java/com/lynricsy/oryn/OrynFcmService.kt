package com.lynricsy.oryn

import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import com.lynricsy.oryn.mobile.EventLog
import com.lynricsy.oryn.mobile.FcmKeys
import com.lynricsy.oryn.mobile.FcmMessage
import com.lynricsy.oryn.mobile.OrynMobilePlugin
import com.lynricsy.oryn.mobile.PushDelivery
import com.lynricsy.oryn.mobile.PushRecovery
import com.lynricsy.oryn.mobile.PushStore

/**
 * FCM 通道（`docs/protocol.md`「官方推送网关」）：orynd 经官方推送网关发 data-only 高优先级消息，`data` 只有
 * `{v:"2", id, e}`，`e` 是用本机登记的 RFC 8291 密钥（[FcmKeys]）加密的推送载荷；解密后交给与 UnifiedPush 同一条
 * PushPayload / Notifier 流程。`v` 不对、缺 `e` 或解不开的消息一律丢弃，不显示任何明文字段。
 * 网关登记时的 challenge 推送（`kind: gateway_challenge`）最先判断，转给 Rust 完成授权。
 * 回调都在 Firebase 的后台线程上。日志不记令牌与 nonce。
 */
class OrynFcmService : FirebaseMessagingService() {
  override fun onNewToken(token: String) {
    // 只有启用了推送才记令牌；Rust 在跑就由它重新授权，否则等下次进入 App（网关通道不做后台登记）
    if (!PushStore.isEnabled(this)) return
    PushStore.setFcmToken(this, token)
    EventLog.log(this, "fcm.token")
    OrynMobilePlugin.pushChanged(this)
  }

  override fun onMessageReceived(message: RemoteMessage) {
    val challenge = FcmMessage.challenge(message.data)
    if (challenge != null) {
      EventLog.log(this, "push.challenge")
      OrynMobilePlugin.pushChallenge(this, challenge.first, challenge.second)
      return
    }
    if (!PushStore.isEnabled(this)) {
      EventLog.log(this, "push.dropped", mapOf("reason" to "disabled", "via" to "fcm"))
      return
    }
    val cipher = FcmMessage.cipherText(message.data)
    if (cipher == null) {
      EventLog.log(this, "push.dropped", mapOf("reason" to "unencrypted", "via" to "fcm"))
      return
    }
    val plain = FcmKeys.decrypt(this, cipher)
    if (plain == null) {
      EventLog.log(this, "push.dropped", mapOf("reason" to "undecrypted", "via" to "fcm"))
      return
    }
    PushDelivery.deliver(this, plain, "fcm")
  }

  /** 离线积压超过上限，FCM 丢弃了消息：补拉未读通知。 */
  override fun onDeletedMessages() {
    if (!PushStore.isEnabled(this)) return
    PushRecovery.recover(this)
  }
}
