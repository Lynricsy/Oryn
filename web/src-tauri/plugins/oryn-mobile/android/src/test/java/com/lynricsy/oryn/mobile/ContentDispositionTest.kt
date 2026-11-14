package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Test

class ContentDispositionTest {
  @Test
  fun fileNameTable() {
    val cases = listOf(
      // orynd 附件下载的实际形态
      "attachment; filename*=UTF-8''m7-dl-0x0y.txt" to "m7-dl-0x0y.txt",
      "attachment; filename*=UTF-8''%E4%B8%AD%E6%96%87%20%E6%96%87%E4%BB%B6.txt" to "中文 文件.txt",
      "attachment; filename*=utf-8'zh'report.pdf" to "report.pdf",
      "attachment; filename*=ISO-8859-1'en'%A3%20rates.txt" to "£ rates.txt",
      // filename* 优先于 filename，与先后无关
      "attachment; filename=\"fallback.txt\"; filename*=UTF-8''real.txt" to "real.txt",
      "attachment; filename*=UTF-8''real.txt; filename=\"fallback.txt\"" to "real.txt",
      // filename* 解不出时回退 filename
      "attachment; filename*=UTF-8''%ZZ.txt; filename=ok.txt" to "ok.txt",
      "attachment; filename*=no-such-charset''a.txt; filename=\"b.txt\"" to "b.txt",
      "attachment; filename*=a.txt; filename=\"c.txt\"" to "c.txt",
      // 只有 filename
      "attachment; filename=\"a b.txt\"" to "a b.txt",
      "attachment; filename=plain.txt" to "plain.txt",
      "attachment;filename=\"semi;colon.txt\"" to "semi;colon.txt",
      "attachment; filename=\"q\\\"uote.txt\"" to "q\"uote.txt",
      "ATTACHMENT; FILENAME=Up.TXT" to "Up.TXT",
      "inline; name=x; filename = spaced.txt " to "spaced.txt",
    )
    for ((header, expected) in cases) {
      assertEquals("header=$header", expected, ContentDisposition.fileName(header))
    }
  }

  @Test
  fun fileNameAbsent() {
    for (header in listOf(null, "", "attachment", "inline", "attachment; size=3", "attachment; filename=\"\"", "attachment; filename")) {
      assertEquals("header=$header", null, ContentDisposition.fileName(header))
    }
  }
}
