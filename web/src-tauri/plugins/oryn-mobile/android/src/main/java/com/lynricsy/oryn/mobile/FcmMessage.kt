package com.lynricsy.oryn.mobile

/**
 * FCM 消息（`docs/protocol.md`「M7：移动推送 / FCM 发送」）：data 只有 `{v:"2", id, e}`，`e` 是 RFC 8291
 * aes128gcm 报文体的 base64url。纯函数，不依赖 Android。
 */
object FcmMessage {
  const val VERSION = "2"

  /** 取出密文；`v` 不是 "2"、缺 `e` 或解不了 base64 时返回 null——这类消息一律丢弃，绝不显示明文字段。 */
  fun cipherText(data: Map<String, String>): ByteArray? {
    if (data["v"] != VERSION) return null
    val e = data["e"]?.takeIf { it.isNotBlank() } ?: return null
    return try {
      FcmKeys.unb64(e)
    } catch (_: IllegalArgumentException) {
      null
    }
  }
}
