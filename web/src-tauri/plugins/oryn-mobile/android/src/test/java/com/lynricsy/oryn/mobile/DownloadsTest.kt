package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class DownloadsTest {
  @Test
  fun candidateNames() {
    assertEquals("report.txt", Downloads.candidate("report.txt", 0))
    assertEquals("report (1).txt", Downloads.candidate("report.txt", 1))
    assertEquals("a.tar (2).gz", Downloads.candidate("a.tar.gz", 2))
    assertEquals("README (3)", Downloads.candidate("README", 3))
    assertEquals("中文 文件 (1).docx", Downloads.candidate("中文 文件.docx", 1))
  }

  /** 代码审查第 3 条：同名文件已在下载目录 → 取下一个空名字，不覆盖、不删除。 */
  @Test
  fun uniqueSkipsExistingNames() {
    val existing = setOf("report.txt", "report (1).txt")
    assertEquals(0, Downloads.unique("new.txt") { it in existing })
    assertEquals(2, Downloads.unique("report.txt") { it in existing })
    // 1009 重排：从上次用的候选之后接着找
    assertEquals(3, Downloads.unique("report.txt", 3) { it in existing })
    // 100 个全被占：退回一个更大的序号，仍是新名字
    assertTrue(Downloads.unique("x.bin") { true } >= 1000)
  }
}
