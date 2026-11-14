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
}
