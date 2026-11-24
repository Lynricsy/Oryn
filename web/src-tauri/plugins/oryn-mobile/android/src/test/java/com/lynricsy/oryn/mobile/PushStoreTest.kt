package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PushStoreTest {
  /** 通道表的全部组合（enabled × 分发器 × 带 FCM × preferUp）。 */
  @Test
  fun channelTable() {
    val up = "io.heckel.ntfy"
    for (distributor in listOf(up, null)) {
      for (hasFcm in listOf(true, false)) {
        for (preferUp in listOf(true, false)) {
          assertNull(PushStore.channelFor(enabled = false, distributor = distributor, hasFcm = hasFcm, preferUp = preferUp))
        }
      }
    }
    // gms 包缺省走 FCM（经网关），有没有分发器都一样
    assertEquals("fcm", PushStore.channelFor(enabled = true, distributor = null, hasFcm = true, preferUp = false))
    // D5 迁移：已有 UnifiedPush 分发器（与端点）、没有 preferUp 的 gms 老用户 → fcm
    assertEquals("fcm", PushStore.channelFor(enabled = true, distributor = up, hasFcm = true, preferUp = false))
    // 手动切到 UnifiedPush
    assertEquals("unifiedpush", PushStore.channelFor(enabled = true, distributor = up, hasFcm = true, preferUp = true))
    // 切到 UnifiedPush 但还没有分发器
    assertNull(PushStore.channelFor(enabled = true, distributor = null, hasFcm = true, preferUp = true))
    // foss 包：只看分发器；preferUp 不影响
    for (preferUp in listOf(true, false)) {
      assertEquals("unifiedpush", PushStore.channelFor(enabled = true, distributor = up, hasFcm = false, preferUp = preferUp))
      assertNull(PushStore.channelFor(enabled = true, distributor = null, hasFcm = false, preferUp = preferUp))
    }
  }

  @Test
  fun permissionTable() {
    assertEquals("granted", PushStore.permissionFor(notificationsEnabled = true, asked = false))
    assertEquals("granted", PushStore.permissionFor(notificationsEnabled = true, asked = true))
    assertEquals("prompt", PushStore.permissionFor(notificationsEnabled = false, asked = false))
    assertEquals("denied", PushStore.permissionFor(notificationsEnabled = false, asked = true))
  }

  @Test
  fun notifierChannelNameFollowsLang() {
    assertEquals("通知", Notifier.channelName("zh-CN"))
    assertEquals("Notifications", Notifier.channelName("en"))
  }
}
