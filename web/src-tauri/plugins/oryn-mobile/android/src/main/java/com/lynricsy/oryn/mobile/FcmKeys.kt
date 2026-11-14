package com.lynricsy.oryn.mobile

import android.content.Context
import com.google.crypto.tink.apps.fixed_webpush.WebPushHybridDecrypt
import java.math.BigInteger
import java.security.AlgorithmParameters
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.SecureRandom
import java.security.interfaces.ECPrivateKey
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPrivateKeySpec
import java.security.spec.PKCS8EncodedKeySpec
import java.util.Base64

/**
 * FCM 通道的 RFC 8291 接收端密钥（安全审查第 2 条）：P-256 密钥对 + 16 字节 auth，随 FCM 令牌一起登记给服务端；
 * 服务端把通知加密进 FCM data 的 `e`，Google 只见密文。解密用 UnifiedPush 连接器自带的那份
 * `WebPushHybridDecrypt`（与 UnifiedPush 消息同一套代码）。
 *
 * 公钥与 auth 明文存 `SharedPreferences("oryn_fcm_keys")`（本来就要交给服务端）；私钥（PKCS#8）经 [Vault.seal]
 * 用 AndroidKeyStore 里的 AES-GCM 密钥加密后保存。禁备份，换机后解不开 → 重新生成并重新登记。
 */
object FcmKeys {
  private const val PREFS = "oryn_fcm_keys"
  private const val K_P256DH = "p256dh"
  private const val K_AUTH = "auth"
  private const val K_PRIVATE = "private"
  private const val AUTH_LEN = 16
  private const val POINT_LEN = 65

  /** 登记给服务端的公开部分（base64url 无填充）。 */
  data class Public(val p256dh: String, val auth: String)

  /** 新生成的一套密钥。 */
  class Generated(val publicKey: ByteArray, val auth: ByteArray, val privateKey: ECPrivateKey)

  /** 取现有密钥；没有（首次启用、升级自没有密钥的版本、私钥解不开）就生成一套，返回 `(公开部分, 是否新生成)`。 */
  @Synchronized
  fun ensure(context: Context): Pair<Public, Boolean> {
    current(context)?.let { return it to false }
    val g = generate()
    val sealed = Vault.seal(g.privateKey.encoded)
    val pub = Public(b64(g.publicKey), b64(g.auth))
    check(
      prefs(context).edit()
        .putString(K_P256DH, pub.p256dh)
        .putString(K_AUTH, pub.auth)
        .putString(K_PRIVATE, b64(sealed))
        .commit(),
    ) { "fcm keys write failed" }
    return pub to true
  }

  /** 关闭推送时删掉（下次启用重新生成）。 */
  @Synchronized
  fun clear(context: Context) {
    prefs(context).edit().clear().commit()
  }

  /** 解密 FCM 的 `e`；没有密钥或解不开返回 null。 */
  fun decrypt(context: Context, body: ByteArray): ByteArray? {
    val (pub, auth, priv) = synchronized(this) {
      val p = prefs(context)
      val pub = p.getString(K_P256DH, null) ?: return null
      val auth = p.getString(K_AUTH, null) ?: return null
      val priv = p.getString(K_PRIVATE, null) ?: return null
      Triple(pub, auth, priv)
    }
    return try {
      val pkcs8 = Vault.open(unb64(priv)) ?: return null
      open(body, unb64(pub), unb64(auth), privateFromPkcs8(pkcs8))
    } catch (_: Exception) {
      null
    }
  }

  private fun current(context: Context): Public? {
    val p = prefs(context)
    val pub = p.getString(K_P256DH, null) ?: return null
    val auth = p.getString(K_AUTH, null) ?: return null
    val priv = p.getString(K_PRIVATE, null) ?: return null
    // 私钥解不开（Keystore 密钥失效）= 这套密钥没用了
    return try {
      Vault.open(unb64(priv))?.let { Public(pub, auth) }
    } catch (_: Exception) {
      null
    }
  }

  // ---- 纯函数（JVM 单测） ----

  fun generate(random: SecureRandom = SecureRandom()): Generated {
    val gen = KeyPairGenerator.getInstance("EC")
    gen.initialize(ECGenParameterSpec("secp256r1"), random)
    val pair = gen.generateKeyPair()
    val auth = ByteArray(AUTH_LEN).also { random.nextBytes(it) }
    return Generated(encodePoint((pair.public as ECPublicKey).w), auth, pair.private as ECPrivateKey)
  }

  /** RFC 8291 aes128gcm 单记录报文体（salt | rs | idlen | keyid | 密文）→ 明文。 */
  fun open(body: ByteArray, publicKey: ByteArray, auth: ByteArray, privateKey: ECPrivateKey): ByteArray =
    WebPushHybridDecrypt.Builder()
      .withAuthSecret(auth)
      .withRecipientPublicKey(publicKey)
      .withRecipientPrivateKey(privateKey)
      .build()
      .decrypt(body, null)

  /** 未压缩点 `0x04 | X(32) | Y(32)`。 */
  fun encodePoint(w: ECPoint): ByteArray {
    val out = ByteArray(POINT_LEN)
    out[0] = 0x04
    fixed32(w.affineX).copyInto(out, 1)
    fixed32(w.affineY).copyInto(out, 33)
    return out
  }

  fun privateFromPkcs8(bytes: ByteArray): ECPrivateKey =
    KeyFactory.getInstance("EC").generatePrivate(PKCS8EncodedKeySpec(bytes)) as ECPrivateKey

  /** 32 字节标量 → 私钥（RFC 8291 附录 A 的测试向量就是这种形式）。 */
  fun privateFromScalar(scalar: ByteArray): ECPrivateKey {
    val params = AlgorithmParameters.getInstance("EC")
    params.init(ECGenParameterSpec("secp256r1"))
    val spec = ECPrivateKeySpec(BigInteger(1, scalar), params.getParameterSpec(ECParameterSpec::class.java))
    return KeyFactory.getInstance("EC").generatePrivate(spec) as ECPrivateKey
  }

  fun b64(bytes: ByteArray): String = Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)

  fun unb64(s: String): ByteArray = Base64.getUrlDecoder().decode(s.trim().trimEnd('='))

  private fun fixed32(v: BigInteger): ByteArray {
    val raw = v.toByteArray()
    return when {
      raw.size == 32 -> raw
      raw.size > 32 -> raw.copyOfRange(raw.size - 32, raw.size)
      else -> ByteArray(32 - raw.size) + raw
    }
  }

  private fun prefs(context: Context) =
    context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
}
