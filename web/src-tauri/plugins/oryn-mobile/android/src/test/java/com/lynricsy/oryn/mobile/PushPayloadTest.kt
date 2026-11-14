package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PushPayloadTest {
  private val thread = "T-0192f3a4-5b6c-7d8e-9f01-23456789abcd"

  private fun parse(json: String) = PushPayload.parse(json.toByteArray(Charsets.UTF_8))

  @Test
  fun parsesM5Payload() {
    val m = parse(
      """{"v":1,"id":"n1","kind":"idle","title":"线程完成","body":"done","url":"/t/$thread","tag":"oryn-23456789abcd"}""",
    )
    assertEquals(PushPayload.Model("n1", "线程完成", "done", "/t/$thread", "oryn-23456789abcd"), m)
  }

  @Test
  fun parsesFcmDataWithStringValues() {
    // FCM data 的值全是字符串（v 也是 "1"），缺 tag 时按同规则推出
    val m = parse("""{"v":"1","id":"n2","kind":"error","title":"t","body":"b","url":"/t/$thread"}""")
    assertEquals("oryn-23456789abcd", m?.tag)
    assertEquals("n2", m?.id)
  }

  @Test
  fun missingOptionalFieldsDefaultToEmpty() {
    val m = parse("""{"title":"t","url":"/settings/notifications"}""")
    assertEquals(PushPayload.Model("", "t", "", "/settings/notifications", "oryn-"), m)
  }

  @Test
  fun rejectsMissingTitle() {
    assertNull(parse("""{"id":"n1","body":"b","url":"/t/$thread"}"""))
    assertNull(parse("""{"id":"n1","title":"","url":"/t/$thread"}"""))
    assertNull(parse("""{"id":"n1","title":null,"url":"/t/$thread"}"""))
  }

  @Test
  fun rejectsMissingOrRelativeUrl() {
    assertNull(parse("""{"id":"n1","title":"t"}"""))
    assertNull(parse("""{"id":"n1","title":"t","url":"https://evil.example/t/$thread"}"""))
    assertNull(parse("""{"id":"n1","title":"t","url":"t/$thread"}"""))
  }

  @Test
  fun rejectsNonObject() {
    assertNull(parse("not json"))
    assertNull(parse("[1,2]"))
    assertNull(PushPayload.parse(ByteArray(0)))
  }

  @Test
  fun tagForThreadPathUsesLast12() {
    assertEquals("oryn-23456789abcd", PushPayload.tagFor("/t/$thread", "n1"))
  }

  @Test
  fun tagForOtherPathUsesId() {
    assertEquals("oryn-n1", PushPayload.tagFor("/settings/notifications", "n1"))
    assertEquals("oryn-n1", PushPayload.tagFor("/t/$thread/extra", "n1"))
  }

  @Test
  fun deepLinkForValidThread() {
    assertEquals("oryn://thread/$thread", PushPayload.deepLink("/t/$thread"))
  }

  @Test
  fun deepLinkRejectsInvalidThreadId() {
    assertEquals("oryn://open", PushPayload.deepLink("/t/../../settings"))
    assertEquals("oryn://open", PushPayload.deepLink("/t/T-123"))
    assertEquals("oryn://open", PushPayload.deepLink("/t/$thread?x=1"))
  }

  @Test
  fun deepLinkForSettingsPathOpensApp() {
    assertEquals("oryn://open", PushPayload.deepLink("/settings/notifications"))
  }
}
