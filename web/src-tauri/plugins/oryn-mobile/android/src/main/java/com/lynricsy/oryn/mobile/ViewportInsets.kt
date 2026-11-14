package com.lynricsy.oryn.mobile

import app.tauri.plugin.JSObject
import kotlin.math.roundToLong

/**
 * 原生容器的 inset 快照（CSS px）：`insets` = 加在 WebView 外的 padding（系统栏 + 刘海 + 输入法），
 * `cornerRadii` = 屏幕物理圆角（取不到为 0）。Web 侧据此让圆角与屏幕同心：
 * WebView 某角的同心半径 = max(0, r − max(相邻两边 inset))。纯数据与换算，不依赖 Android。
 */
data class ViewportInsets(
  val top: Double,
  val right: Double,
  val bottom: Double,
  val left: Double,
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
    val corners = JSObject()
    corners.put("tl", tl)
    corners.put("tr", tr)
    corners.put("bl", bl)
    corners.put("br", br)
    val o = JSObject()
    o.put("insets", insets)
    o.put("cornerRadii", corners)
    return o
  }

  companion object {
    val ZERO = ViewportInsets(0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0)

    /** 物理 px → CSS px（= dp），保留两位小数；负数、非有限值或 density 非正时为 0。 */
    fun toCss(px: Int, density: Float): Double {
      if (px <= 0 || !(density > 0f) || density.isInfinite()) return 0.0
      return (px / density.toDouble() * 100.0).roundToLong() / 100.0
    }

    /** 由物理 px 构造；`corners` 依次为左上、右上、左下、右下，null 视为 0。 */
    fun fromPx(
      density: Float,
      top: Int,
      right: Int,
      bottom: Int,
      left: Int,
      corners: List<Int?>,
    ): ViewportInsets {
      fun c(i: Int) = toCss(corners.getOrNull(i) ?: 0, density)
      return ViewportInsets(
        top = toCss(top, density),
        right = toCss(right, density),
        bottom = toCss(bottom, density),
        left = toCss(left, density),
        tl = c(0),
        tr = c(1),
        bl = c(2),
        br = c(3),
      )
    }
  }
}
