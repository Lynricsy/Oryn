package com.lynricsy.oryn

import android.content.Context
import com.google.firebase.FirebaseApp
import com.google.firebase.messaging.FirebaseMessaging
import com.lynricsy.oryn.mobile.Fcm
import com.lynricsy.oryn.mobile.FcmBridge

/** gms：把 FCM 装进插件；推送缺省经官方推送网关走 FCM，用户可在设置里手动切到 UnifiedPush。 */
object Flavor {
  fun install(context: Context) {
    Fcm.bridge = FirebaseBridge
  }
}

private object FirebaseBridge : FcmBridge {
  override fun token(context: Context, callback: (token: String?, error: String?) -> Unit) {
    val fm = try {
      FirebaseMessaging.getInstance()
    } catch (e: Exception) {
      callback(null, "fcm_unavailable")
      return
    }
    // 清单里关了自动初始化（未启用推送前不生成令牌）；用户启用后才打开
    fm.isAutoInitEnabled = true
    fm.token.addOnCompleteListener { task ->
      if (task.isSuccessful && !task.result.isNullOrEmpty()) {
        callback(task.result, null)
      } else {
        callback(null, task.exception?.javaClass?.simpleName ?: "fcm_token")
      }
    }
  }

  override fun delete(context: Context) {
    try {
      val fm = FirebaseMessaging.getInstance()
      fm.isAutoInitEnabled = false
      fm.deleteToken()
    } catch (_: Exception) {
    }
  }

  override fun projectId(context: Context): String? = try {
    FirebaseApp.getInstance().options.projectId
  } catch (_: Exception) {
    null
  }
}
