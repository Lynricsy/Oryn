package com.lynricsy.oryn.mobile

import android.app.DownloadManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.Uri
import android.os.Environment
import android.webkit.CookieManager
import android.webkit.URLUtil
import java.io.File
import java.util.concurrent.ConcurrentHashMap

/**
 * 下载（wry 在 Android 上没有下载钩子，插件给 WebView 装下载监听后交给这里）：系统 DownloadManager 存进公共下载目录。
 *
 * 同名文件（代码审查第 3 条）：DownloadManager 不覆盖已有文件，而是异步失败为 `ERROR_FILE_ALREADY_EXISTS`，
 * `enqueue` 成功并不代表下载成功。这里先挑一个本应用看得见的空名字（`报告.txt` → `报告 (1).txt`）；
 * 别的应用留下的同名文件本应用看不见（分区存储），就监听完成广播，遇到 1009 换下一个名字重排，最多 [MAX_TRIES] 次。
 * 从不删除或覆盖已有文件。
 */
object Downloads {
  const val MAX_TRIES = 8

  private class Pending(
    val url: String,
    val cookie: String?,
    val userAgent: String?,
    val mimeType: String?,
    val name: String,
    /** 第几次排队（0 起）。 */
    val attempt: Int,
    /** 从第几个候选名开始找（上次用的候选 + 1）。 */
    val from: Int,
  )

  private val pending = ConcurrentHashMap<Long, Pending>()
  @Volatile private var receiver: BroadcastReceiver? = null

  /** `report.txt` 的第 n 个候选：0 → 原名，n → `report (n).txt`（扩展名按最后一个点；点开头的名字整体当主名）。纯函数。 */
  fun candidate(name: String, n: Int): String {
    if (n == 0) return name
    val dot = name.lastIndexOf('.')
    return if (dot > 0) "${name.substring(0, dot)} ($n)${name.substring(dot)}" else "$name ($n)"
  }

  /** 从第 `from` 个候选起第一个不存在的候选序号；100 个全占了就退回一个随机大序号。纯函数。 */
  fun unique(name: String, from: Int = 0, exists: (String) -> Boolean): Int {
    for (n in from until from + 100) {
      if (!exists(candidate(name, n))) return n
    }
    return 1000 + (System.nanoTime() and 0xffff).toInt()
  }

  fun start(context: Context, url: String, userAgent: String?, contentDisposition: String?, mimeType: String?) {
    val uri = Uri.parse(url)
    val scheme = uri.scheme?.lowercase()
    if (scheme != "http" && scheme != "https") {
      EventLog.log(context, "download.skipped", mapOf("scheme" to scheme))
      return
    }
    // URLUtil 不认 RFC 6266 的 filename*（orynd 附件只发它），先自己解析
    val name = ShareIntake.sanitizeFileName(
      ContentDisposition.fileName(contentDisposition) ?: URLUtil.guessFileName(url, contentDisposition, mimeType),
    )
    ensureReceiver(context)
    enqueue(context, Pending(url, CookieManager.getInstance().getCookie(url), userAgent, mimeType, name, 0, 0))
  }

  private fun enqueue(context: Context, p: Pending) {
    val dir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
    val index = unique(p.name, p.from) { File(dir, it).exists() }
    val target = candidate(p.name, index)
    val queued = Pending(p.url, p.cookie, p.userAgent, p.mimeType, p.name, p.attempt, index + 1)
    try {
      val req = DownloadManager.Request(Uri.parse(p.url))
      p.cookie?.let { req.addRequestHeader("Cookie", it) }
      if (!p.userAgent.isNullOrEmpty()) req.addRequestHeader("User-Agent", p.userAgent)
      if (!p.mimeType.isNullOrEmpty()) req.setMimeType(p.mimeType)
      req.setTitle(target)
      req.setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
      // 公共下载目录：minSdk 33，不需要任何存储权限
      req.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, target)
      val id = context.getSystemService(DownloadManager::class.java).enqueue(req)
      pending[id] = queued
      EventLog.log(context, "download.enqueued", mapOf("name" to target, "attempt" to p.attempt))
    } catch (e: Exception) {
      EventLog.log(context, "download.failed", mapOf("name" to target, "error" to e.javaClass.simpleName))
    }
  }

  /** 完成广播：成功记日志；同名冲突换下一个名字重排；其它失败记原因。 */
  private fun completed(context: Context, id: Long) {
    val p = pending.remove(id) ?: return
    val dm = context.getSystemService(DownloadManager::class.java)
    val (status, reason, local) = dm.query(DownloadManager.Query().setFilterById(id))?.use { c ->
      if (!c.moveToFirst()) return@use null
      Triple(
        c.getInt(c.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS)),
        c.getInt(c.getColumnIndexOrThrow(DownloadManager.COLUMN_REASON)),
        c.getString(c.getColumnIndexOrThrow(DownloadManager.COLUMN_LOCAL_URI)),
      )
    } ?: return
    when {
      status == DownloadManager.STATUS_SUCCESSFUL ->
        EventLog.log(context, "download.done", mapOf("name" to Uri.parse(local ?: "").lastPathSegment))
      reason == DownloadManager.ERROR_FILE_ALREADY_EXISTS && p.attempt + 1 < MAX_TRIES -> {
        // 不调 DownloadManager.remove：它会删记录指向的文件，而那正是用户已有的同名文件
        EventLog.log(context, "download.retry", mapOf("name" to p.name, "reason" to "exists"))
        enqueue(context, Pending(p.url, p.cookie, p.userAgent, p.mimeType, p.name, p.attempt + 1, p.from))
      }
      else -> EventLog.log(context, "download.failed", mapOf("name" to p.name, "reason" to reason))
    }
  }

  private fun ensureReceiver(context: Context) {
    if (receiver != null) return
    synchronized(this) {
      if (receiver != null) return
      val app = context.applicationContext
      val r = object : BroadcastReceiver() {
        override fun onReceive(c: Context, intent: Intent) {
          val id = intent.getLongExtra(DownloadManager.EXTRA_DOWNLOAD_ID, -1)
          // 广播只用来提醒「去查一下」：状态一律以 DownloadManager 的记录为准，且只认本进程排过的 id
          if (id >= 0) Thread { completed(app, id) }.start()
        }
      }
      // 发送方是系统下载服务（另一个 uid），必须导出
      app.registerReceiver(r, IntentFilter(DownloadManager.ACTION_DOWNLOAD_COMPLETE), Context.RECEIVER_EXPORTED)
      receiver = r
    }
  }
}
