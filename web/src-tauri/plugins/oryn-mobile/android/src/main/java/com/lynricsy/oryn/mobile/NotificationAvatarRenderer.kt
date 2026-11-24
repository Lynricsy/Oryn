package com.lynricsy.oryn.mobile

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PorterDuff
import android.graphics.PorterDuffXfermode
import android.graphics.Rect
import android.graphics.RectF
import android.graphics.Typeface
import android.os.Looper
import android.util.Base64
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import kotlin.math.min

/**
 * 通知头像渲染与全局助手头像缓存（供 [Notifier] 的 `MessagingStyle` 使用）：
 * - 普通线程：按标题卡图标（Emoji / Symbols Nerd Font）与线程色绘制圆形头像；无图标时画 Oryn 品牌标；
 * - 全局助手（主线动态与助手对话线程）：优先用自定义图片，其次自定义字形，未设置时画默认 sparkle 标记；
 *   后台推送只携带 8 位 `hash` 时，本地缓存命中即零请求出图，未命中则在后台线程短超时拉一次
 *   `GET /api/v1/settings/assistant-identity` 并落盘缓存；
 * - 无线程系统通知：深墨圆底 + 白色 Oryn 品牌标。
 */
object NotificationAvatarRenderer {
  const val AVATAR_PX = 128
  private const val FETCH_TIMEOUT_MS = 2_500
  private const val CACHE_FILE = "oryn_assistant_avatar.json"
  private const val NERD_FONT_ASSET = "fonts/SymbolsNerdFontMono-Regular.ttf"

  data class Palette(val bg: Int, val fg: Int)

  private val THREAD_PALETTE: Map<String, Palette> = mapOf(
    "red" to Palette(0xFFFCE8E6.toInt(), 0xFFB3261E.toInt()),
    "orange" to Palette(0xFFFEF0E1.toInt(), 0xFFB45309.toInt()),
    "yellow" to Palette(0xFFFEF7DC.toInt(), 0xFF9A6B00.toInt()),
    "green" to Palette(0xFFE6F4EA.toInt(), 0xFF18794E.toInt()),
    "teal" to Palette(0xFFE3F5F5.toInt(), 0xFF0F766E.toInt()),
    "blue" to Palette(0xFFE8EFFC.toInt(), 0xFF2B5CE6.toInt()),
    "purple" to Palette(0xFFF2E8FC.toInt(), 0xFF7C3AED.toInt()),
    "pink" to Palette(0xFFFCE7F3.toInt(), 0xFFBE185D.toInt()),
  )

  private val NEUTRAL_PALETTE = Palette(0xFFEEF0F4.toInt(), 0xFF1F2430.toInt())
  private val ASSISTANT_PALETTE = Palette(0xFFE8EFFC.toInt(), 0xFF2B5CE6.toInt())
  private val SYSTEM_PALETTE = Palette(0xFF1F2430.toInt(), 0xFFFFFFFF.toInt())

  @Volatile private var nerdTypeface: Typeface? = null
  @Volatile private var nerdTypefaceLoaded = false
  private val cacheLock = Any()

  private data class CachedAssistant(
    val hash: String?,
    val icon: String?,
    val image: String?,
  )

  /** 前台收到全局助手头像信息时更新本地缓存，供后续后台推送零网络请求复用。 */
  fun updateAssistantCache(context: Context, avatar: PushPayload.Avatar?) {
    if (avatar == null || avatar.kind != PushPayload.AVATAR_ASSISTANT) return
    val icon = avatar.icon?.takeIf { it.isNotEmpty() }
    val image = avatar.image?.takeIf { it.isNotEmpty() }
    if (icon == null && image == null && avatar.hash == null) {
      clearAssistantCache(context)
      return
    }
    if (icon != null || image != null) {
      val hash = avatar.hash ?: digest(if (image != null) "image" else "icon", image ?: icon!!)
      writeCache(context, CachedAssistant(hash = hash, icon = icon, image = image))
    }
  }

  /** 为一条通知绘制 128×128 圆形头像 Bitmap。 */
  fun render(context: Context, avatar: PushPayload.Avatar?, allowNetwork: Boolean = !isMainThread()): Bitmap {
    val av = avatar ?: PushPayload.Avatar(kind = PushPayload.AVATAR_SYSTEM)
    // 1. 前台预栅格化的 PNG 优先（已处理好裁剪 / Nerd Font）
    av.image?.let { raw ->
      decodeCircularBitmap(raw)?.let { return it }
    }
    return when (av.kind) {
      PushPayload.AVATAR_ASSISTANT -> renderAssistant(context, av, allowNetwork)
      PushPayload.AVATAR_THREAD -> renderThread(context, av)
      else -> renderBrandMark(SYSTEM_PALETTE)
    }
  }

  private fun renderThread(context: Context, av: PushPayload.Avatar): Bitmap {
    val palette = av.color?.let { THREAD_PALETTE[it] } ?: NEUTRAL_PALETTE
    val icon = av.icon?.takeIf { it.isNotEmpty() }
    return if (icon != null) {
      renderGlyph(context, icon, palette)
    } else {
      renderBrandMark(palette)
    }
  }

  private fun renderAssistant(context: Context, av: PushPayload.Avatar, allowNetwork: Boolean): Bitmap {
    // 服务端直接给了字形头像
    av.icon?.takeIf { it.isNotEmpty() }?.let { icon ->
      return renderGlyph(context, icon, ASSISTANT_PALETTE)
    }
    // 服务端有 hash 且没有 icon → 自定义图片头像（或旧缓存），比对缓存并按需后台拉取
    val wantedHash = av.hash?.takeIf { it.isNotEmpty() }
    if (wantedHash != null) {
      var cached = readCache(context)
      if (cached == null || cached.hash != wantedHash) {
        if (allowNetwork) {
          fetchAssistantIdentity(context)?.let { fetched ->
            cached = fetched
          }
        }
      }
      cached?.image?.let { img ->
        decodeCircularBitmap(img)?.let { return it }
      }
      cached?.icon?.takeIf { it.isNotEmpty() }?.let { icon ->
        return renderGlyph(context, icon, ASSISTANT_PALETTE)
      }
    } else if (av.hash == null) {
      // 明确无自定义头像时显示默认 sparkle，不误用过期的本地缓存
      clearAssistantCache(context)
    }
    return renderSparkle(ASSISTANT_PALETTE)
  }

  private fun fetchAssistantIdentity(context: Context): CachedAssistant? {
    val server = PushStore.server(context) ?: return null
    val token = Vault.get(context, server) ?: return null
    return try {
      val resp = OrynHttp.call(
        context,
        "GET",
        "${server.trimEnd('/')}/api/v1/settings/assistant-identity",
        token,
        timeoutMs = FETCH_TIMEOUT_MS,
      )
      if (resp.status !in 200..299 || resp.body.isEmpty()) return null
      val obj = JSONObject(resp.body)
      val icon = obj.optString("avatarIcon", "").takeIf { it.isNotEmpty() }
      val image = obj.optString("avatarImage", "").takeIf { it.isNotEmpty() }
      if (icon == null && image == null) {
        clearAssistantCache(context)
        return CachedAssistant(hash = null, icon = null, image = null)
      }
      val hash = if (image != null) digest("image", image) else digest("icon", icon!!)
      val entry = CachedAssistant(hash = hash, icon = icon, image = image)
      writeCache(context, entry)
      entry
    } catch (_: Exception) {
      null
    }
  }

  private fun readCache(context: Context): CachedAssistant? = synchronized(cacheLock) {
    val file = File(context.cacheDir, CACHE_FILE)
    if (!file.isFile) return null
    try {
      val obj = JSONObject(file.readText(Charsets.UTF_8))
      CachedAssistant(
        hash = obj.optString("hash", "").takeIf { it.isNotEmpty() },
        icon = obj.optString("icon", "").takeIf { it.isNotEmpty() },
        image = obj.optString("image", "").takeIf { it.isNotEmpty() },
      )
    } catch (_: Exception) {
      null
    }
  }

  private fun writeCache(context: Context, entry: CachedAssistant) = synchronized(cacheLock) {
    try {
      val obj = JSONObject()
      if (entry.hash != null) obj.put("hash", entry.hash)
      if (entry.icon != null) obj.put("icon", entry.icon)
      if (entry.image != null) obj.put("image", entry.image)
      val tmp = File(context.cacheDir, "$CACHE_FILE.tmp")
      val target = File(context.cacheDir, CACHE_FILE)
      tmp.writeText(obj.toString(), Charsets.UTF_8)
      if (!tmp.renameTo(target)) {
        target.writeText(obj.toString(), Charsets.UTF_8)
        tmp.delete()
      }
    } catch (_: Exception) {
    }
  }

  private fun clearAssistantCache(context: Context) = synchronized(cacheLock) {
    try {
      File(context.cacheDir, CACHE_FILE).delete()
    } catch (_: Exception) {
    }
  }

  private fun digest(kind: String, value: String): String {
    val md = MessageDigest.getInstance("SHA-256")
    md.update(kind.toByteArray(Charsets.UTF_8))
    md.update(0)
    md.update(value.toByteArray(Charsets.UTF_8))
    return md.digest().take(4).joinToString("") { "%02x".format(it) }
  }

  private fun decodeCircularBitmap(raw: String): Bitmap? {
    val b64 = raw.substringAfter("base64,", raw)
    val bytes = try {
      Base64.decode(b64, Base64.DEFAULT)
    } catch (_: Exception) {
      return null
    }
    if (bytes.isEmpty()) return null
    val src = BitmapFactory.decodeByteArray(bytes, 0, bytes.size) ?: return null
    return try {
      cropToCircle(src, AVATAR_PX)
    } finally {
      src.recycle()
    }
  }

  private fun cropToCircle(src: Bitmap, px: Int): Bitmap {
    val out = Bitmap.createBitmap(px, px, Bitmap.Config.ARGB_8888)
    val canvas = Canvas(out)
    val paint = Paint(Paint.ANTI_ALIAS_FLAG or Paint.FILTER_BITMAP_FLAG)
    val radius = px / 2f
    canvas.drawCircle(radius, radius, radius, paint)
    paint.xfermode = PorterDuffXfermode(PorterDuff.Mode.SRC_IN)
    val side = min(src.width, src.height)
    val left = (src.width - side) / 2
    val top = (src.height - side) / 2
    canvas.drawBitmap(src, Rect(left, top, left + side, top + side), Rect(0, 0, px, px), paint)
    return out
  }

  private fun renderGlyph(context: Context, glyph: String, palette: Palette): Bitmap {
    val out = Bitmap.createBitmap(AVATAR_PX, AVATAR_PX, Bitmap.Config.ARGB_8888)
    val canvas = Canvas(out)
    val bgPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply { color = palette.bg }
    val half = AVATAR_PX / 2f
    canvas.drawCircle(half, half, half, bgPaint)

    val nerd = PushPayload.isNerdGlyph(glyph)
    val tf = if (nerd) loadNerdTypeface(context) else null
    if (nerd && tf == null) {
      // 字体不可用时回落到品牌标，绝不画出豆腐块
      out.recycle()
      return renderBrandMark(palette)
    }
    val textPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
      color = palette.fg
      textSize = AVATAR_PX * (if (nerd) 0.56f else 0.54f)
      typeface = tf ?: Typeface.DEFAULT
      textAlign = Paint.Align.CENTER
    }
    val bounds = Rect()
    textPaint.getTextBounds(glyph, 0, glyph.length, bounds)
    val x = if (bounds.width() > 0) half - (bounds.left + bounds.right) / 2f + bounds.exactCenterX() else half
    val y = if (bounds.height() > 0) {
      half - bounds.exactCenterY()
    } else {
      val fm = textPaint.fontMetrics
      half - (fm.ascent + fm.descent) / 2f
    }
    canvas.drawText(glyph, if (bounds.width() > 0) half else x, y, textPaint)
    return out
  }

  /** 绘制默认全局助手的 sparkle 四芒星头像（大星 + 右上小星，与 Web/iOS 一致）。 */
  private fun renderSparkle(palette: Palette): Bitmap {
    val out = Bitmap.createBitmap(AVATAR_PX, AVATAR_PX, Bitmap.Config.ARGB_8888)
    val canvas = Canvas(out)
    val half = AVATAR_PX / 2f
    canvas.drawCircle(half, half, half, Paint(Paint.ANTI_ALIAS_FLAG).apply { color = palette.bg })

    val fg = Paint(Paint.ANTI_ALIAS_FLAG).apply {
      color = palette.fg
      style = Paint.Style.FILL
    }
    // 主四芒星（微左下方居中，留出右上角给副星）
    canvas.drawPath(fourPointStar(cx = AVATAR_PX * 0.46f, cy = AVATAR_PX * 0.54f, r = AVATAR_PX * 0.27f), fg)
    // 右上小四芒星
    canvas.drawPath(fourPointStar(cx = AVATAR_PX * 0.72f, cy = AVATAR_PX * 0.28f, r = AVATAR_PX * 0.115f), fg)
    return out
  }

  private fun fourPointStar(cx: Float, cy: Float, r: Float): Path {
    val k = r * 0.24f
    return Path().apply {
      moveTo(cx, cy - r)
      quadTo(cx + k, cy - k, cx + r, cy)
      quadTo(cx + k, cy + k, cx, cy + r)
      quadTo(cx - k, cy + k, cx - r, cy)
      quadTo(cx - k, cy - k, cx, cy - r)
      close()
    }
  }

  /** 绘制 Oryn 品牌标「O|」（与 ic_stat_oryn.xml / favicon.svg 同几何比例）。 */
  private fun renderBrandMark(palette: Palette): Bitmap {
    val out = Bitmap.createBitmap(AVATAR_PX, AVATAR_PX, Bitmap.Config.ARGB_8888)
    val canvas = Canvas(out)
    val half = AVATAR_PX / 2f
    canvas.drawCircle(half, half, half, Paint(Paint.ANTI_ALIAS_FLAG).apply { color = palette.bg })

    val fg = Paint(Paint.ANTI_ALIAS_FLAG).apply {
      color = palette.fg
      style = Paint.Style.FILL
    }
    // 将 32×32 视口缩放到 74×74 居中（占直径 58%）
    val iconSize = AVATAR_PX * 0.58f
    val scale = iconSize / 32f
    val offset = (AVATAR_PX - iconSize) / 2f
    canvas.save()
    canvas.translate(offset, offset)
    canvas.scale(scale, scale)

    // 左半个 orb：圆心 (16, 16) 半径 12，弧与竖弦 x=20.1 围合
    val orb = Path().apply {
      val arcRect = RectF(4f, 4f, 28f, 28f)
      // acos((20.1 - 16) / 12) ≈ 70.02°
      arcTo(arcRect, 70.02f, 219.96f, true)
      close()
    }
    canvas.drawPath(orb, fg)

    // 右侧竖光标条：x=[23.4, 27.0], y=[4.0, 28.0], 圆角 1.1
    canvas.drawRoundRect(RectF(23.4f, 4f, 27f, 28f), 1.1f, 1.1f, fg)
    canvas.restore()
    return out
  }

  private fun loadNerdTypeface(context: Context): Typeface? {
    if (!nerdTypefaceLoaded) {
      synchronized(this) {
        if (!nerdTypefaceLoaded) {
          nerdTypeface = try {
            Typeface.createFromAsset(context.assets, NERD_FONT_ASSET)
          } catch (_: Exception) {
            null
          }
          nerdTypefaceLoaded = true
        }
      }
    }
    return nerdTypeface
  }

  private fun isMainThread(): Boolean = try {
    Looper.myLooper() == Looper.getMainLooper()
  } catch (_: Exception) {
    false
  }
}
