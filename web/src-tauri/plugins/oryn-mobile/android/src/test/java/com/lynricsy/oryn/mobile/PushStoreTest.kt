package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PushStoreTest {
  @Test
  fun channelPrefersUnifiedPush() {
    assertNull(PushStore.channelFor(enabled = false, distributor = "io.heckel.ntfy", hasFcm = true))
    assertEquals("unifiedpush", PushStore.channelFor(enabled = true, distributor = "io.heckel.ntfy", hasFcm = true))
    assertEquals("unifiedpush", PushStore.channelFor(enabled = true, distributor = "io.heckel.ntfy", hasFcm = false))
    assertEquals("fcm", PushStore.channelFor(enabled = true, distributor = null, hasFcm = true))
    assertNull(PushStore.channelFor(enabled = true, distributor = null, hasFcm = false))
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
    assertEquals("线程通知", Notifier.channelName("zh-CN"))
    assertEquals("Thread updates", Notifier.channelName("en"))
  }
}
