package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Test

class ViewportInsetsTest {
  @Test
  fun toCssDividesByDensityAndRounds() {
    assertEquals(48.0, ViewportInsets.toCss(126, 2.625f), 0.0)
    assertEquals(24.0, ViewportInsets.toCss(63, 2.625f), 0.0)
    assertEquals(33.33, ViewportInsets.toCss(100, 3f), 0.0)
    assertEquals(17.5, ViewportInsets.toCss(35, 2f), 0.0)
  }

  @Test
  fun toCssGuardsInvalidInput() {
    assertEquals(0.0, ViewportInsets.toCss(0, 2f), 0.0)
    assertEquals(0.0, ViewportInsets.toCss(-5, 2f), 0.0)
    assertEquals(0.0, ViewportInsets.toCss(100, 0f), 0.0)
    assertEquals(0.0, ViewportInsets.toCss(100, Float.NaN), 0.0)
    assertEquals(0.0, ViewportInsets.toCss(100, Float.POSITIVE_INFINITY), 0.0)
  }

  @Test
  fun fromPxMapsCornersInOrderAndNullsToZero() {
    val v = ViewportInsets.fromPx(2f, top = 48, right = 0, bottom = 96, left = 10, corners = listOf(80, 80, null, 60))
    assertEquals(ViewportInsets(24.0, 0.0, 48.0, 5.0, 40.0, 40.0, 0.0, 30.0), v)
  }

  @Test
  fun fromPxWithoutCornersIsZero() {
    val v = ViewportInsets.fromPx(3f, 0, 0, 0, 0, emptyList())
    assertEquals(ViewportInsets.ZERO, v)
  }

  @Test
  fun equalSnapshotsCompareEqual() {
    // 插件按值比较决定是否重发 insets 事件
    val a = ViewportInsets.fromPx(2.75f, 66, 0, 132, 0, listOf(110, 110, 110, 110))
    val b = ViewportInsets.fromPx(2.75f, 66, 0, 132, 0, listOf(110, 110, 110, 110))
    assertEquals(a, b)
  }
}
