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
  fun clampGestureNavBottomOnlyTightensGestureNav() {
    // 手势导航（tappableBottom = 0）：系统报 24dp / 32dp 时收敛到 16dp，避免与小白条之间空出大缝
    assertEquals(32, ViewportInsets.clampGestureNavBottomPx(navBottom = 64, tappableBottom = 0, density = 2f))
    assertEquals(20, ViewportInsets.clampGestureNavBottomPx(navBottom = 20, tappableBottom = 0, density = 2f))
    // 三键导航（tappableBottom > 0）：完整保留系统 48dp 导航栏高度，避免遮挡按键
    assertEquals(96, ViewportInsets.clampGestureNavBottomPx(navBottom = 96, tappableBottom = 96, density = 2f))
    assertEquals(0, ViewportInsets.clampGestureNavBottomPx(navBottom = 0, tappableBottom = 0, density = 2f))
  }

  @Test
  fun fromWindowInsetsPreservesBottomCutoutAndMapsCorners() {
    // 仅手势 navigationBars 被钳制后，合并后的底部挖孔（40dp = 80px）必须原样保留给 Web
    val v = ViewportInsets.fromWindowInsets(
      density = 2f,
      safeTop = 64,
      safeRight = 0,
      safeBottom = 80,
      safeLeft = 20,
      imeBottom = 0,
      corners = listOf(80, 80, null, 60),
    )
    assertEquals(
      ViewportInsets(
        top = 0.0,
        right = 0.0,
        bottom = 0.0,
        left = 0.0,
        safeTop = 32.0,
        safeRight = 0.0,
        safeBottom = 40.0,
        safeLeft = 10.0,
        tl = 40.0,
        tr = 40.0,
        bl = 0.0,
        br = 30.0,
      ),
      v,
    )
  }

  @Test
  fun fromWindowInsetsMovesImeToOuterPadding() {
    val v = ViewportInsets.fromWindowInsets(
      density = 2f,
      safeTop = 64,
      safeRight = 0,
      safeBottom = 0,
      safeLeft = 0,
      imeBottom = 600,
      corners = listOf(80, 80, 80, 80),
    )
    assertEquals(0.0, v.top, 0.0)
    assertEquals(300.0, v.bottom, 0.0)
    assertEquals(32.0, v.safeTop, 0.0)
    assertEquals(0.0, v.safeBottom, 0.0)
  }

  @Test
  fun fromWindowInsetsWithoutCornersOrBarsIsZero() {
    val v = ViewportInsets.fromWindowInsets(3f, 0, 0, 0, 0, 0, emptyList())
    assertEquals(ViewportInsets.ZERO, v)
  }

  @Test
  fun equalSnapshotsCompareEqual() {
    // 插件按值比较决定是否重发 insets 事件
    val a = ViewportInsets.fromWindowInsets(2.75f, 66, 0, 132, 0, 0, listOf(110, 110, 110, 110))
    val b = ViewportInsets.fromWindowInsets(2.75f, 66, 0, 132, 0, 0, listOf(110, 110, 110, 110))
    assertEquals(a, b)
  }
}
