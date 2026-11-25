package com.lynricsy.oryn.mobile

import app.tauri.plugin.JSObject
import kotlin.math.min
import kotlin.math.roundToInt
import kotlin.math.roundToLong

/**
 * 原生容器的几何与安全区快照（CSS px）：
 * - `insets` = 加在 WebView 外的原生 padding（当前仅软键盘底距，供同心圆角减扣）；
 * - `safeArea` = WebView 内部需避让的系统安全区（系统栏 + 刘海/挖孔，底部扣除已作为原生 padding 的软键盘高度）；
 * - `cornerRadii` = 屏幕物理圆角（取不到为 0）。Web 侧据此让圆角与屏幕同心：
 *   WebView 某角的同心半径 = max(0, r − max(相邻两边 inset))。纯数据与换算，不依赖 Android。
 */
data class ViewportInsets(
  val top: Double,
  val right: Double,
  val bottom: Double,
  val left: Double,
  val safeTop: Double,
  val safeRight: Double,
  val safeBottom: Double,
  val safeLeft: Double,
  val tl: Double,
  val tr: Double,
  val bl: Double,
  val br: Double,
) {
  fun toJson(): JSObject {
    val insets = JSObject()
    insets.put("top", top)
    insets.put("right", right)
    insets.put("bottom", bottom)
    insets.put("left", left)
    val safeArea = JSObject()
    safeArea.put("top", safeTop)
    safeArea.put("right", safeRight)
    safeArea.put("bottom", safeBottom)
    safeArea.put("left", safeLeft)
    val corners = JSObject()
    corners.put("tl", tl)
    corners.put("tr", tr)
    corners.put("bl", bl)
    corners.put("br", br)
    val o = JSObject()
    o.put("insets", insets)
    o.put("safeArea", safeArea)
    o.put("cornerRadii", corners)
    return o
  }

  companion object {
    val ZERO = ViewportInsets(0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0)

    /**
     * 手势导航（小白条，tappableElement.bottom == 0）底部视觉仅占 ~11-12dp，但 Android 在手机 / 折叠屏展开态
     * 常把防误触区报到 24-32dp，导致内容与小白条之间空出巨大缝隙；收敛到 16dp 既完全避开小白条又与顶部视觉对称。
     * 三键导航（tappableElement.bottom > 0）完整保留系统原始高度。
     */
    const val GESTURE_NAV_MAX_DP = 16f

    /** 物理 px → CSS px（= dp），保留两位小数；负数、非有限值或 density 非正时为 0。 */
    fun toCss(px: Int, density: Float): Double {
      if (px <= 0 || !(density > 0f) || density.isInfinite()) return 0.0
      return (px / density.toDouble() * 100.0).roundToLong() / 100.0
    }

    /**
     * 仅对手势导航的 `navigationBars().bottom`（`tappableBottom <= 0`）收紧上限至 16dp；
     * 三键导航（`tappableBottom > 0`）原样保留。不触碰 `displayCutout` / `statusBars` / `captionBar`。
     */
    fun clampGestureNavBottomPx(
      navBottom: Int,
      tappableBottom: Int,
      density: Float,
    ): Int {
      val nb = navBottom.coerceAtLeast(0)
      return if (tappableBottom <= 0 && nb > 0 && density > 0f && !density.isInfinite()) {
        min(nb, (GESTURE_NAV_MAX_DP * density).roundToInt())
      } else {
        nb
      }
    }

    /**
     * 由窗口原生 px 构造：WebView 铺满屏幕，仅底部软键盘 `imeBottom` 作为容器外 padding；
     * `safe*` 必须取自扣除 `imeBottom` 且仅钳制手势 `navigationBars().bottom` 后的最终 insets，
     * 从而完整保留底部挖孔与其余系统遮挡。
     */
    fun fromWindowInsets(
      density: Float,
      safeTop: Int,
      safeRight: Int,
      safeBottom: Int,
      safeLeft: Int,
      imeBottom: Int,
      corners: List<Int?>,
    ): ViewportInsets {
      val kb = imeBottom.coerceAtLeast(0)
      fun c(i: Int) = toCss(corners.getOrNull(i) ?: 0, density)
      return ViewportInsets(
        top = 0.0,
        right = 0.0,
        bottom = toCss(kb, density),
        left = 0.0,
        safeTop = toCss(safeTop, density),
        safeRight = toCss(safeRight, density),
        safeBottom = toCss(safeBottom, density),
        safeLeft = toCss(safeLeft, density),
        tl = c(0),
        tr = c(1),
        bl = c(2),
        br = c(3),
      )
    }
  }
}
