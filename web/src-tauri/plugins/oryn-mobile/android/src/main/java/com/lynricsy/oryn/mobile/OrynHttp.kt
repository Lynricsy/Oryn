package com.lynricsy.oryn.mobile

import android.content.Context
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL

/**
 * 推送服务在没有 Rust 时自己发的少量请求（后台登记订阅、FCM 补拉通知）：Bearer 设备令牌，不跟随重定向，15 秒超时。
 * 地址只来自 [PushStore.server]（Rust 登记成功时记下的已配对服务器）。
 */
internal object OrynHttp {
  class Response(val status: Int, val body: String)

  private const val TIMEOUT_MS = 15_000
  private const val MAX_BODY = 1 shl 20

  @Throws(IOException::class)
  fun call(context: Context, method: String, url: String, token: String, body: JSONObject? = null): Response {
    val conn = URL(url).openConnection() as HttpURLConnection
    try {
      conn.instanceFollowRedirects = false
      conn.connectTimeout = TIMEOUT_MS
      conn.readTimeout = TIMEOUT_MS
      conn.requestMethod = method
      conn.setRequestProperty("Authorization", "Bearer $token")
      conn.setRequestProperty("Accept", "application/json")
      conn.setRequestProperty("User-Agent", "Oryn-Android/${version(context)}")
      if (body != null) {
        val bytes = body.toString().toByteArray(Charsets.UTF_8)
        conn.doOutput = true
        conn.setRequestProperty("Content-Type", "application/json")
        conn.setFixedLengthStreamingMode(bytes.size)
        conn.outputStream.use { it.write(bytes) }
      }
      val status = conn.responseCode
      val stream = if (status >= 400) conn.errorStream else conn.inputStream
      val text = stream?.use { s ->
        val bytes = s.readNBytes(MAX_BODY)
        String(bytes, Charsets.UTF_8)
      } ?: ""
      return Response(status, text)
    } finally {
      conn.disconnect()
    }
  }

  private fun version(context: Context): String = try {
    context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: ""
  } catch (_: Exception) {
    ""
  }
}
