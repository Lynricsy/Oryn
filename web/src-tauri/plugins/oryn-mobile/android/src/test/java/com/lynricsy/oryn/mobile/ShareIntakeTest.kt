package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ShareIntakeTest {
  @Test
  fun sanitizeFileNameTable() {
    val cases = listOf(
      "photo.png" to "photo.png",
      "dir/sub/report.pdf" to "report.pdf",
      "C:\\Users\\me\\notes.txt" to "notes.txt",
      "../../etc/passwd" to "passwd",
      ".hidden" to "hidden",
      "...env" to "env",
      "  spaced name.txt  " to "spaced name.txt",
      "a\u0000b\nc.txt" to "abc.txt",
      "what?<>|*\".md" to "what.md",
      "" to "file",
      "/" to "file",
      "..." to "file",
      "中文 文件.docx" to "中文 文件.docx",
    )
    for ((input, expected) in cases) {
      assertEquals("input=$input", expected, ShareIntake.sanitizeFileName(input))
    }
    assertEquals("file", ShareIntake.sanitizeFileName(null))
  }

  @Test
  fun sanitizeFileNameTruncatesKeepingExtension() {
    val long = "x".repeat(300) + ".tar.gz"
    val out = ShareIntake.sanitizeFileName(long)
    assertEquals(120, out.length)
    assertTrue(out.endsWith(".gz"))
  }

  @Test
  fun composeTextTable() {
    assertNull(ShareIntake.composeText(null, null))
    assertNull(ShareIntake.composeText("  ", " "))
    assertEquals("body", ShareIntake.composeText(null, "body"))
    assertEquals("Subject", ShareIntake.composeText("Subject", null))
    assertEquals("Subject\nhttps://x", ShareIntake.composeText("Subject", "https://x"))
    // 正文已含主题时不重复
    assertEquals("Subject — https://x", ShareIntake.composeText("Subject", "Subject — https://x"))
  }

  /** 安全审查第 4 条：`userId@` 前缀绕不过归属判断；按 uid（appId）而不是包名前缀判断。 */
  @Test
  fun ownProviderIsRecognisedByOwnerUid() {
    val me = 10_123
    val owners = mapOf(
      "com.lynricsy.oryn.fileprovider" to 10_123,
      "com.lynricsy.oryn.androidx-startup" to 10_123,
      "com.android.providers.media.documents" to 10_050,
      // 名字以本包名开头、其实属于别的应用
      "com.lynricsy.oryn.evil.provider" to 10_777,
    )
    val own = { a: String -> ShareIntake.ownAuthority(a, me) { owners[it] } }
    assertTrue(own("com.lynricsy.oryn.fileprovider"))
    assertTrue(own("0@com.lynricsy.oryn.fileprovider"))
    assertTrue(own("10@com.lynricsy.oryn.fileprovider"))
    assertTrue(own("com.lynricsy.oryn.androidx-startup"))
    assertTrue(!own("com.android.providers.media.documents"))
    assertTrue(!own("0@com.android.providers.media.documents"))
    assertTrue(!own("com.lynricsy.oryn.evil.provider"))
    // 解析不到的 provider：打开时同样失败，这里不算本应用
    assertTrue(!own("unknown.authority"))
    // 工作资料里的本应用：uid 不同、appId 相同
    assertTrue(ShareIntake.ownAuthority("10@com.lynricsy.oryn.fileprovider", me) { 1_010_123 })
    assertEquals("com.lynricsy.oryn.fileprovider", ShareIntake.authorityHost("0@com.lynricsy.oryn.fileprovider"))
  }

  /** 安全审查第 5 条：锁屏公开版不含任何线程内容。 */
  @Test
  fun lockScreenPublicTextHasNoContent() {
    assertEquals("线程有更新", Notifier.publicText("zh-CN"))
    assertEquals("A thread has an update", Notifier.publicText("en"))
    assertEquals("Oryn 有新通知", Notifier.genericText("zh-CN"))
  }
}
