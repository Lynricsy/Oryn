package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ActivityLinkTest {
  private val url = "http://localhost:7600/t/T-1"

  @Test
  fun firstBindIsNotARebind() {
    val l = ActivityLink()
    assertTrue(l.attached)
    assertFalse(l.activityCreated())
    assertNull(l.webViewBound())
  }

  /** 字体大小等配置变更：wry 自己重建 WebView，不要 Rust 重建窗口；新 WebView 带上原地址。 */
  @Test
  fun configChangeRebindsWithoutAskingForAWindow() {
    val l = ActivityLink()
    l.webViewBound()
    l.destroyed(changingConfigurations = true, url = url)
    assertTrue("配置变更期间 Activity 马上就有", l.attached)
    assertFalse(l.activityCreated())
    assertEquals(ActivityLink.Rebound(url), l.webViewBound())
  }

  /** 「不保留活动」：进程留着、没有 Activity；新 Activity 要 Rust 重建主窗口，绑定后回到原地址。 */
  @Test
  fun systemDestroyAsksRustForAWindowAndRestoresTheUrl() {
    val l = ActivityLink()
    l.webViewBound()
    l.destroyed(changingConfigurations = false, url = url)
    assertFalse("没有 Activity 时不能调 Rust 的插件命令路径", l.attached)
    assertTrue(l.activityCreated())
    assertEquals(ActivityLink.Rebound(url), l.webViewBound())
    assertTrue(l.attached)
    assertFalse("窗口已重建，后续 Activity 不再要窗口", l.activityCreated())
  }

  /** 销毁时主框架还没有地址（WebView 尚未加载）：照样重建窗口，只是不指定回到哪页。 */
  @Test
  fun destroyWithoutUrlStillAsksForAWindow() {
    val l = ActivityLink()
    l.webViewBound()
    l.destroyed(changingConfigurations = false, url = null)
    assertTrue(l.activityCreated())
    assertEquals(ActivityLink.Rebound(null), l.webViewBound())
  }
}
