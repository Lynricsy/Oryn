package com.lynricsy.oryn.mobile

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * 设备令牌保险库：AndroidKeyStore 里的 AES-256-GCM 密钥（不要求用户认证）加密后存
 * `SharedPreferences("oryn_vault")`，键 `token:<server>`，值 `base64(iv(12) + 密文)`。
 * 解密失败（换机恢复、密钥失效）→ 删除该条并返回 null，壳随之走重新配对。
 */
object Vault {
  private const val KEY_ALIAS = "oryn.device-token.v1"
  private const val PREFS = "oryn_vault"
  private const val TRANSFORM = "AES/GCM/NoPadding"
  private const val IV_LEN = 12
  private const val TAG_BITS = 128

  @Synchronized
  fun get(context: Context, server: String): String? {
    val prefs = prefs(context)
    val stored = prefs.getString(prefKey(server), null) ?: return null
    return try {
      val raw = Base64.decode(stored, Base64.NO_WRAP)
      require(raw.size > IV_LEN)
      val cipher = Cipher.getInstance(TRANSFORM)
      cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, raw, 0, IV_LEN))
      String(cipher.doFinal(raw, IV_LEN, raw.size - IV_LEN), Charsets.UTF_8)
    } catch (_: Exception) {
      prefs.edit().remove(prefKey(server)).apply()
      null
    }
  }

  @Synchronized
  fun set(context: Context, server: String, token: String) {
    val cipher = Cipher.getInstance(TRANSFORM)
    cipher.init(Cipher.ENCRYPT_MODE, key())
    val iv = cipher.iv
    check(iv.size == IV_LEN) { "unexpected iv length ${iv.size}" }
    val sealed = cipher.doFinal(token.toByteArray(Charsets.UTF_8))
    val value = Base64.encodeToString(iv + sealed, Base64.NO_WRAP)
    // commit：令牌落盘后才算存好，壳紧接着就用它换会话
    check(prefs(context).edit().putString(prefKey(server), value).commit()) { "vault write failed" }
  }

  @Synchronized
  fun delete(context: Context, server: String) {
    prefs(context).edit().remove(prefKey(server)).commit()
  }

  /** 用同一把 Keystore 密钥加密任意字节（FCM 接收端私钥用）：`iv(12) + 密文`。 */
  @Synchronized
  fun seal(plain: ByteArray): ByteArray {
    val cipher = Cipher.getInstance(TRANSFORM)
    cipher.init(Cipher.ENCRYPT_MODE, key())
    val iv = cipher.iv
    check(iv.size == IV_LEN) { "unexpected iv length ${iv.size}" }
    return iv + cipher.doFinal(plain)
  }

  /** [seal] 的逆；解不开返回 null。 */
  @Synchronized
  fun open(sealed: ByteArray): ByteArray? = try {
    require(sealed.size > IV_LEN)
    val cipher = Cipher.getInstance(TRANSFORM)
    cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, sealed, 0, IV_LEN))
    cipher.doFinal(sealed, IV_LEN, sealed.size - IV_LEN)
  } catch (_: Exception) {
    null
  }

  private fun prefKey(server: String) = "token:$server"

  private fun prefs(context: Context) =
    context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

  private fun key(): SecretKey {
    val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    (ks.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
    val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
    gen.init(
      KeyGenParameterSpec.Builder(
        KEY_ALIAS,
        KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
      )
        .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
        .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
        .setKeySize(256)
        .setUserAuthenticationRequired(false)
        .build(),
    )
    return gen.generateKey()
  }
}
