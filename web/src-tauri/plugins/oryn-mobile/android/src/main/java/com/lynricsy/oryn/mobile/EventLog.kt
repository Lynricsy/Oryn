package com.lynricsy.oryn.mobile

import android.content.Context
import android.content.pm.ApplicationInfo
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream

/**
 * 调试事件日志：仅 debuggable 包写 `cacheDir/events.jsonl`，与 Rust 侧（eventlog.rs）同一个文件、同一行格式
 * `{"ts": <毫秒>, "event": "...", ...字段}`。每行一次 write 追加，release 包为空操作。
 * 不写令牌、一次性码、推送密钥。
 */
object EventLog {
  @Volatile private var resolved = false
  @Volatile private var file: File? = null

  /** debuggable 时的日志路径，否则 null。 */
  fun path(context: Context): String? = target(context)?.absolutePath

  fun log(context: Context, event: String, fields: Map<String, Any?> = emptyMap()) {
    val f = target(context) ?: return
    val obj = JSONObject()
    obj.put("ts", System.currentTimeMillis())
    obj.put("event", event)
    for ((k, v) in fields) {
      if (k != "ts" && k != "event") obj.put(k, v ?: JSONObject.NULL)
    }
    val line = (obj.toString() + "\n").toByteArray(Charsets.UTF_8)
    synchronized(this) {
      try {
        FileOutputStream(f, true).use { it.write(line) }
      } catch (_: Exception) {
        // 日志写失败不影响应用本身
      }
    }
  }

  private fun target(context: Context): File? {
    if (!resolved) {
      synchronized(this) {
        if (!resolved) {
          val app = context.applicationContext ?: context
          val debuggable = app.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0
          file = if (debuggable) File(app.cacheDir, "events.jsonl") else null
          resolved = true
        }
      }
    }
    return file
  }
}
