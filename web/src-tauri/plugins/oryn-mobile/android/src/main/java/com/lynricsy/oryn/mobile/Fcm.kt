package com.lynricsy.oryn.mobile

import android.content.Context

/**
 * FCM 通道的接口。插件库本身不依赖 Firebase：只有应用的 `gms` flavor 在启动时
 * 把实现装进 [Fcm.bridge]；`foss` 包里它恒为 null，推送只走 UnifiedPush。
 */
interface FcmBridge {
  /** 打开自动初始化并取当前 FCM 令牌；回调恰好一次（主线程或 Firebase 的线程）。 */
  fun token(context: Context, callback: (token: String?, error: String?) -> Unit)

  /** 关闭自动初始化并删除令牌（尽力而为）。 */
  fun delete(context: Context)
}

object Fcm {
  @Volatile var bridge: FcmBridge? = null
}
