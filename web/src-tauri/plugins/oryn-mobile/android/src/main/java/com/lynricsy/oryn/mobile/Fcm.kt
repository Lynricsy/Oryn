package com.lynricsy.oryn.mobile

import android.content.Context

/**
 * FCM 通道的接口。插件库本身不依赖 Firebase：只有应用的 `gms` flavor 在启动时
 * 把实现装进 [Fcm.bridge]；`foss` 包里它恒为 null，推送只走 UnifiedPush。
 * gms 包的 FCM 经官方推送网关（`docs/protocol.md`「官方推送网关」），登记由 Rust 完成。
 */
interface FcmBridge {
  /** 打开自动初始化并取当前 FCM 令牌；回调恰好一次（主线程或 Firebase 的线程）。 */
  fun token(context: Context, callback: (token: String?, error: String?) -> Unit)

  /** 关闭自动初始化并删除令牌（尽力而为）。 */
  fun delete(context: Context)

  /** 本包编进的 Firebase 项目 id（与网关 `/v1/info` 的 `fcm.projectId` 比对）；取不到为 null。 */
  fun projectId(context: Context): String?
}

object Fcm {
  @Volatile var bridge: FcmBridge? = null
}
