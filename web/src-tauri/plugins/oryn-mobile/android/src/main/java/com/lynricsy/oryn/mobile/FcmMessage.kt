package com.lynricsy.oryn.mobile

/**
 * FCM 消息（`docs/protocol.md`「M7：移动推送」与「官方推送网关」）：通知的 data 只有 `{v:"2", id, e}`，`e` 是 RFC 8291
 * aes128gcm 报文体的 base64url；网关登记时的 challenge 是 `{kind:"gateway_challenge", challengeId, nonce}`。
 * 纯函数，不依赖 Android。
 */
object FcmMessage {
  const val VERSION = "2"
  const val CHALLENGE_KIND = "gateway_challenge"

  /** 网关的 `challengeId` / `nonce`：22 个 base64url 字符。 */
  private val ID = Regex("^[A-Za-z0-9_-]{22}$")

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

  /** 网关的 challenge 推送 → `(challengeId, nonce)`；不是 challenge 或形状不对时 null。 */
  fun challenge(data: Map<String, String>): Pair<String, String>? {
    if (data["kind"] != CHALLENGE_KIND) return null
    val id = data["challengeId"]?.takeIf { ID.matches(it) } ?: return null
    val nonce = data["nonce"]?.takeIf { ID.matches(it) } ?: return null
    return id to nonce
  }
}
