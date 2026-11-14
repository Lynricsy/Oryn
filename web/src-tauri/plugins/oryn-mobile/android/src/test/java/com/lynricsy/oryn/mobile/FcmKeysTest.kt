package com.lynricsy.oryn.mobile

import com.google.crypto.tink.apps.fixed_webpush.WebPushHybridEncrypt
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test
import java.security.GeneralSecurityException

class FcmKeysTest {
  private fun b(s: String) = FcmKeys.unb64(s.replace(" ", ""))

  /** RFC 8291 附录 A 的完整向量：接收端私钥 + auth 解出 "When I grow up, I want to be a watermelon"。 */
  @Test
  fun decryptsRfc8291AppendixA() {
    val header = b("DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z 9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml mlMoZIIgDll6e3vCYLocInmYWAmS6Tlz AC8wEqKK6PBru3jl7A8")
    val cipher = b("8pfeW0KbunFT06SuDKoJH9Ql87S1QUrd irN6GcG7sFz1y1sqLgVi1VhjVkHsUoEs bI_0LpXMuGvnzQ")
    assertEquals(86, header.size)
    val pub = b("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcx aOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
    val priv = FcmKeys.privateFromScalar(b("q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
    val auth = b("BTBZMqHH6r4Tts7J_aSIgg")
    val plain = FcmKeys.open(header + cipher, pub, auth, priv)
    assertEquals("When I grow up, I want to be a watermelon", String(plain, Charsets.UTF_8))
    // 换一个 auth 就解不开（AEAD 校验失败），不会吐出半截明文
    val wrong = auth.copyOf().also { it[0] = (it[0].toInt() xor 1).toByte() }
    assertThrows(GeneralSecurityException::class.java) { FcmKeys.open(header + cipher, pub, wrong, priv) }
  }

  /** 生成的密钥：未压缩点 65 字节、0x04 开头，auth 16 字节；服务端同款加密（aes128gcm, rs 4096）能被解开；PKCS#8 往返不变。 */
  @Test
  fun generatedKeysRoundTrip() {
    val g = FcmKeys.generate()
    assertEquals(65, g.publicKey.size)
    assertEquals(0x04.toByte(), g.publicKey[0])
    assertEquals(16, g.auth.size)
    val payload = """{"v":1,"id":"ntf_1","kind":"done","title":"m7","body":"ok","url":"/t/T-x","tag":"oryn-x"}"""
    val body = WebPushHybridEncrypt.Builder()
      .withAuthSecret(g.auth)
      .withRecipientPublicKey(g.publicKey)
      .build()
      .encrypt(payload.toByteArray(), null)
    val restored = FcmKeys.privateFromPkcs8(g.privateKey.encoded)
    assertEquals(payload, String(FcmKeys.open(body, g.publicKey, g.auth, restored), Charsets.UTF_8))
    assertEquals(g.publicKey.toList(), FcmKeys.unb64(FcmKeys.b64(g.publicKey)).toList())
  }

  @Test
  fun messageNeedsVersion2AndCipherText() {
    val e = FcmKeys.b64(byteArrayOf(1, 2, 3))
    assertArrayEquals(byteArrayOf(1, 2, 3), FcmMessage.cipherText(mapOf("v" to "2", "id" to "ntf_1", "e" to e)))
    // 明文格式（v1）、缺 e、坏 base64：一律丢弃
    assertNull(FcmMessage.cipherText(mapOf("v" to "1", "title" to "secret", "body" to "secret", "url" to "/t/x")))
    assertNull(FcmMessage.cipherText(mapOf("v" to "2", "id" to "ntf_1")))
    assertNull(FcmMessage.cipherText(mapOf("e" to e)))
    assertNull(FcmMessage.cipherText(mapOf("v" to "2", "e" to "!!!")))
  }
}
