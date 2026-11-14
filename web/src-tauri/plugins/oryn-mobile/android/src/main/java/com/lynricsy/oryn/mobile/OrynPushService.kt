package com.lynricsy.oryn.mobile

import org.unifiedpush.android.connector.FailedReason
import org.unifiedpush.android.connector.PushService
import org.unifiedpush.android.connector.data.PushEndpoint
import org.unifiedpush.android.connector.data.PushMessage

/**
 * UnifiedPush 回调（连接器以绑定服务方式调用，进程可能刚被分发器拉起、没有 Activity）。
 * 状态变化经 [OrynMobilePlugin.pushChanged]：Rust 在跑就交给它，否则排后台登记任务。
 */
class OrynPushService : PushService() {
  override fun onNewEndpoint(endpoint: PushEndpoint, instance: String) {
    PushStore.setEndpoint(this, endpoint.url, endpoint.pubKeySet?.pubKey, endpoint.pubKeySet?.auth)
    EventLog.log(this, "push.endpoint", mapOf("temporary" to endpoint.temporary))
    OrynMobilePlugin.pushChanged(this)
  }

  override fun onMessage(message: PushMessage, instance: String) {
    if (!message.decrypted) {
      EventLog.log(this, "push.dropped", mapOf("reason" to "undecrypted", "via" to "unifiedpush"))
      return
    }
    PushDelivery.deliver(this, message.content, "unifiedpush")
  }

  override fun onRegistrationFailed(reason: FailedReason, instance: String) {
    PushStore.setError(this, reason.name)
    EventLog.log(this, "push.failed", mapOf("reason" to reason.name))
    OrynMobilePlugin.pushChanged(this)
  }

  override fun onUnregistered(instance: String) {
    PushStore.clearEndpoint(this)
    EventLog.log(this, "push.unregistered")
    OrynMobilePlugin.pushChanged(this)
  }
}
