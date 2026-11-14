package com.lynricsy.oryn.mobile

import java.io.ByteArrayOutputStream
import java.nio.charset.Charset
import java.util.Locale

/**
 * `Content-Disposition` 里的文件名（RFC 6266）：优先 `filename*`（RFC 8187 的 `charset'lang'百分号编码`），其次 `filename`。
 * `URLUtil.guessFileName` 只认 `filename=`，而 orynd 的附件下载只发 `filename*=UTF-8''…`（非 ASCII 名也能表达），
 * 交给它只会得到 `content.bin`。取不出时返回 null，由调用方回退。
 */
object ContentDisposition {
  fun fileName(header: String?): String? {
    if (header.isNullOrBlank()) return null
    val params = parameters(header)
    params["filename*"]?.let { decodeExtValue(it) }?.takeIf { it.isNotEmpty() }?.let { return it }
    return params["filename"]?.takeIf { it.isNotEmpty() }
  }

  /** 第一个 `;` 之后的参数（名小写；带引号的值去引号与反斜杠转义；同名取第一个）。 */
  private fun parameters(header: String): Map<String, String> {
    val out = HashMap<String, String>()
    val n = header.length
    var i = header.indexOf(';')
    if (i < 0) return out
    while (i < n) {
      while (i < n && (header[i] == ';' || header[i] == ' ' || header[i] == '\t')) i++
      val nameStart = i
      while (i < n && header[i] != '=' && header[i] != ';') i++
      val name = header.substring(nameStart, i).trim().lowercase(Locale.ROOT)
      if (i >= n || header[i] == ';') continue
      i++ // '='
      while (i < n && (header[i] == ' ' || header[i] == '\t')) i++
      val value: String
      if (i < n && header[i] == '"') {
        i++
        val sb = StringBuilder()
        while (i < n && header[i] != '"') {
          if (header[i] == '\\' && i + 1 < n) i++
          sb.append(header[i])
          i++
        }
        value = sb.toString()
        while (i < n && header[i] != ';') i++
      } else {
        val start = i
        while (i < n && header[i] != ';') i++
        value = header.substring(start, i).trim()
      }
      if (name.isNotEmpty() && name !in out) out[name] = value
    }
    return out
  }

  /** `charset'lang'百分号编码` → 文本；格式或编码不对时 null。 */
  private fun decodeExtValue(value: String): String? {
    val parts = value.split('\'', limit = 3)
    if (parts.size != 3) return null
    val charset = try {
      Charset.forName(parts[0].trim())
    } catch (_: Exception) {
      return null
    }
    val s = parts[2]
    val bytes = ByteArrayOutputStream()
    var i = 0
    while (i < s.length) {
      val c = s[i]
      if (c == '%') {
        if (i + 3 > s.length) return null
        val b = s.substring(i + 1, i + 3).toIntOrNull(16) ?: return null
        bytes.write(b)
        i += 3
      } else {
        val raw = c.toString().toByteArray(charset)
        bytes.write(raw, 0, raw.size)
        i++
      }
    }
    return String(bytes.toByteArray(), charset)
  }
}
