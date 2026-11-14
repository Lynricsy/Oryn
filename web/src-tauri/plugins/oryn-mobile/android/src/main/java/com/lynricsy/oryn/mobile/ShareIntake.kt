package com.lynricsy.oryn.mobile

import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Process
import android.provider.OpenableColumns
import android.webkit.MimeTypeMap
import app.tauri.plugin.JSArray
import app.tauri.plugin.JSObject
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.util.Locale
import java.util.concurrent.atomic.AtomicLong

/**
 * 系统分享（`ACTION_SEND` / `SEND_MULTIPLE`）的接收：文本 + 至多 [MAX_FILES] 个文件，
 * 文件拷到 `cacheDir/share/<id>/<序号>-<文件名>` 后交给 Rust 上传；Rust 确认后 [ack] 删除目录。
 * 必须在后台线程调用 [intake]。
 */
object ShareIntake {
  const val MAX_FILES = 20
  const val MAX_BYTES = 100L * 1024 * 1024
  private const val MAX_NAME = 120
  private val seq = AtomicLong(0)
  private val ID = Regex("^[0-9]+-[0-9]+$")

  /** 文件名净化：去路径分隔符前的部分、控制字符、前导点与空白；空名 → `file`；超长时保留扩展名截断。 */
  fun sanitizeFileName(name: String?): String {
    var n = (name ?: "").substringAfterLast('/').substringAfterLast('\\')
    n = n.filter { it >= ' ' && it != '\u007f' && it != ':' && it != '*' && it != '?' && it != '"' && it != '<' && it != '>' && it != '|' }
    n = n.trim().trimStart('.', ' ').trimEnd(' ')
    if (n.isEmpty()) return "file"
    if (n.length > MAX_NAME) {
      val dot = n.lastIndexOf('.')
      val ext = if (dot > 0 && n.length - dot <= 16) n.substring(dot) else ""
      n = n.substring(0, MAX_NAME - ext.length) + ext
    }
    return n
  }

  /** 主题与正文：有主题且正文不含它时前置一行；都为空返回 null。 */
  fun composeText(subject: String?, text: String?): String? {
    val s = subject?.trim()?.takeIf { it.isNotEmpty() }
    val t = text?.takeIf { it.isNotBlank() }
    return when {
      s == null -> t
      t == null -> s
      t.contains(s) -> t
      else -> "$s\n$t"
    }
  }

  /** 解析分享 intent；不是分享或什么都没有时返回 null。 */
  fun intake(context: Context, intent: Intent): JSObject? {
    if (intent.action != Intent.ACTION_SEND && intent.action != Intent.ACTION_SEND_MULTIPLE) return null
    val text = composeText(
      intent.getCharSequenceExtra(Intent.EXTRA_SUBJECT)?.toString(),
      intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString(),
    )
    val uris = streams(intent)
    if (text == null && uris.isEmpty()) return null

    val id = "${System.currentTimeMillis()}-${seq.incrementAndGet()}"
    val dir = File(File(context.cacheDir, "share"), id)
    val files = JSArray()
    val rejected = JSArray()
    uris.forEachIndexed { index, uri ->
      val meta = meta(context.contentResolver, uri)
      val name = sanitizeFileName(meta.first ?: uri.lastPathSegment)
      if (index >= MAX_FILES) {
        rejected.put(reject(name, "too_many"))
        return@forEachIndexed
      }
      if (!readable(context, uri)) {
        rejected.put(reject(name, "unreadable"))
        return@forEachIndexed
      }
      val declared = meta.second
      if (declared != null && declared > MAX_BYTES) {
        rejected.put(reject(name, "too_large"))
        return@forEachIndexed
      }
      val target = File(dir, "$index-$name")
      when (val r = copy(context.contentResolver, uri, target)) {
        null -> rejected.put(reject(name, "unreadable"))
        -1L -> rejected.put(reject(name, "too_large"))
        else -> {
          val f = JSObject()
          f.put("path", target.absolutePath)
          f.put("name", name)
          f.put("mime", mime(context.contentResolver, uri, name))
          f.put("size", r)
          files.put(f)
        }
      }
    }
    val share = JSObject()
    share.put("id", id)
    share.put("text", text ?: JSONObject.NULL)
    share.put("files", files)
    share.put("rejected", rejected)
    return share
  }

  /** 删除某次分享的缓存目录（id 不合法时什么也不做）。 */
  fun ack(context: Context, id: String) {
    if (!ID.matches(id)) return
    File(File(context.cacheDir, "share"), id).deleteRecursively()
  }

  @Suppress("DEPRECATION")
  private fun streams(intent: Intent): List<Uri> {
    val out = mutableListOf<Uri>()
    if (intent.action == Intent.ACTION_SEND_MULTIPLE) {
      intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java)?.let { out.addAll(it.filterNotNull()) }
    } else {
      intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java)?.let { out.add(it) }
    }
    if (out.isEmpty()) {
      val clip = intent.clipData
      if (clip != null) for (i in 0 until clip.itemCount) clip.getItemAt(i).uri?.let { out.add(it) }
    }
    return out.distinct()
  }

  /**
   * 只收别人的 content:// URI：file:// 与本应用自己的 provider 会让外部 App 借分享读出本应用私有文件。
   * 按 provider 的归属 uid 判断（安全审查第 4 条）：authority 可以带 `userId@` 前缀（`content://0@<本包>.fileprovider/…`），
   * 字符串前缀比较会被绕过，而 ContentResolver 打开时照样剥掉前缀、在本进程里打开本应用的 provider。
   */
  private fun readable(context: Context, uri: Uri): Boolean {
    if (uri.scheme != ContentResolver.SCHEME_CONTENT) return false
    val authority = uri.authority ?: return false
    return !ownAuthority(authority, Process.myUid()) { host ->
      @Suppress("DEPRECATION")
      context.packageManager.resolveContentProvider(host, 0)?.applicationInfo?.uid
    }
  }

  /** 去掉 `userId@` 前缀后的 provider authority。 */
  fun authorityHost(authority: String): String = authority.substringAfterLast('@')

  /**
   * authority 是否属于本应用（含其它用户 / 工作资料里的本应用：uid 不同但 appId 相同）。解析不到 provider 时按「不是」处理
   * （打开时同样会失败，记为 unreadable）。纯函数，`ownerUid` 由调用方查 PackageManager。
   */
  fun ownAuthority(authority: String, myUid: Int, ownerUid: (String) -> Int?): Boolean {
    val owner = ownerUid(authorityHost(authority)) ?: return false
    return owner % PER_USER_RANGE == myUid % PER_USER_RANGE
  }

  /** `UserHandle.PER_USER_RANGE`：uid = userId × 100000 + appId。 */
  private const val PER_USER_RANGE = 100_000

  private fun meta(resolver: ContentResolver, uri: Uri): Pair<String?, Long?> {
    if (uri.scheme != ContentResolver.SCHEME_CONTENT) return null to null
    return try {
      resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
        if (!c.moveToFirst()) return null to null
        val ni = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
        val si = c.getColumnIndex(OpenableColumns.SIZE)
        val name = if (ni >= 0 && !c.isNull(ni)) c.getString(ni) else null
        val size = if (si >= 0 && !c.isNull(si)) c.getLong(si) else null
        name to size
      } ?: (null to null)
    } catch (_: Exception) {
      null to null
    }
  }

  private fun mime(resolver: ContentResolver, uri: Uri, name: String): String {
    val t = try {
      resolver.getType(uri)
    } catch (_: Exception) {
      null
    }
    if (!t.isNullOrBlank()) return t
    val ext = name.substringAfterLast('.', "").lowercase(Locale.ROOT)
    return MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
  }

  /** 拷贝并计数；超过上限返回 -1（删掉半成品），读写失败返回 null，成功返回字节数。 */
  private fun copy(resolver: ContentResolver, uri: Uri, target: File): Long? {
    return try {
      target.parentFile?.mkdirs()
      val input = resolver.openInputStream(uri) ?: return null
      var total = 0L
      var tooLarge = false
      input.use { src ->
        target.outputStream().use { dst ->
          val buf = ByteArray(64 * 1024)
          while (true) {
            val n = src.read(buf)
            if (n < 0) break
            total += n
            if (total > MAX_BYTES) {
              tooLarge = true
              break
            }
            dst.write(buf, 0, n)
          }
        }
      }
      if (tooLarge) {
        target.delete()
        -1L
      } else {
        total
      }
    } catch (_: IOException) {
      target.delete()
      null
    } catch (_: SecurityException) {
      target.delete()
      null
    }
  }

  private fun reject(name: String, reason: String): JSObject {
    val o = JSObject()
    o.put("name", name)
    o.put("reason", reason)
    return o
  }
}
