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
    assertEquals(PushPayload.Model("n1", "线程完成", "done", "/t/$thread", "oryn-23456789abcd", kind = "idle"), m)
  }

  @Test
  fun parsesInputActions() {
    val m = parse(
      """{"v":1,"id":"ntf_1","kind":"input","title":"t","body":"Allow tool: bash\nCommand: ls","url":"/t/$thread",""" +
        """"tag":"oryn-23456789abcd","actions":[{"i":0,"label":"Yes","role":"yes"},{"i":1,"label":"No","role":"no"},{"i":2,"label":"Later","role":"x"}]}""",
    )
    assertEquals(
      listOf(
        PushPayload.Action(0, "Yes", "yes"),
        PushPayload.Action(1, "No", "no"),
        PushPayload.Action(2, "Later", null),
      ),
      m?.actions,
    )
    // 序列化回去再解析，按钮与正文原样保留（作答后据此重绘通知）
    assertEquals(m, PushPayload.parse(PushPayload.encode(m!!).toByteArray(Charsets.UTF_8)))
  }

  @Test
  fun invalidActionsAreDroppedAsAGroup() {
    fun acts(actions: String, kind: String = "input", id: String = "ntf_1") =
      parse("""{"id":"$id","kind":"$kind","title":"t","url":"/t/$thread","actions":$actions}""")?.actions
    val two = """[{"i":0,"label":"A"},{"i":1,"label":"B"}]"""
    assertEquals(2, acts(two)?.size)
    assertEquals(emptyList<PushPayload.Action>(), acts(two, kind = "done"))
    assertEquals(emptyList<PushPayload.Action>(), acts(two, id = ""))
    assertEquals(emptyList<PushPayload.Action>(), acts("""[{"i":0,"label":"A"}]"""))
    assertEquals(emptyList<PushPayload.Action>(), acts("""[{"i":0,"label":"A"},{"i":1,"label":"B"},{"i":2,"label":"C"},{"i":3,"label":"D"}]"""))
    assertEquals(emptyList<PushPayload.Action>(), acts("""[{"i":0,"label":"A"},{"i":"1","label":"B"}]"""))
    assertEquals(emptyList<PushPayload.Action>(), acts("""[{"i":0,"label":"A"},{"i":-1,"label":"B"}]"""))
    assertEquals(emptyList<PushPayload.Action>(), acts("""[{"i":0,"label":"A"},{"i":1,"label":""}]"""))
    assertEquals(emptyList<PushPayload.Action>(), acts(""""nope""""))
  }

  @Test
  fun parsesWithdrawal() {
    fun w(json: String) = PushPayload.withdrawal(json.toByteArray(Charsets.UTF_8))
    assertEquals(PushPayload.Withdrawal("ntf_1", "oryn-1"), w("""{"v":1,"kind":"withdraw","id":"ntf_1","tag":"oryn-1"}"""))
    assertNull(w("""{"v":1,"kind":"withdraw","id":"ntf_1"}"""))
    assertNull(w("""{"v":1,"kind":"withdraw","tag":"oryn-1"}"""))
    assertNull(w("""{"v":1,"kind":"input","id":"ntf_1","title":"t","url":"/t/$thread","tag":"oryn-1"}"""))
    assertNull(w("not json"))
    // 撤回消息没有标题，不会被当成通知显示
    assertNull(parse("""{"v":1,"kind":"withdraw","id":"ntf_1","tag":"oryn-1"}"""))
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

  @Test
  fun tagForSystemPaths() {
    assertEquals("oryn-budget", PushPayload.tagFor("/stats/usage", "n1"))
    assertEquals("oryn-budget", PushPayload.tagFor("/stats", "n1"))
    assertEquals("oryn-budget", PushPayload.tagFor("/stats?range=7d", "n1"))
    assertEquals("oryn-automations", PushPayload.tagFor("/settings/automations", "n1"))
    assertEquals("oryn-automations", PushPayload.tagFor("/settings/automations?schedule=x", "n1"))
    assertEquals("oryn-n1", PushPayload.tagFor("/statsx", "n1"))
    assertEquals("oryn-n1", PushPayload.tagFor("/settings/automationsx", "n1"))
  }

  @Test
  fun payloadTagWinsOverPathRule() {
    val m = parse("""{"id":"n1","title":"t","url":"/stats/usage","tag":"custom"}""")
    assertEquals("custom", m?.tag)
    assertEquals("oryn-budget", parse("""{"id":"n1","title":"t","url":"/stats/usage"}""")?.tag)
  }

  @Test
  fun deepLinkForSystemPaths() {
    assertEquals("oryn://stats", PushPayload.deepLink("/stats/usage"))
    assertEquals("oryn://stats", PushPayload.deepLink("/stats"))
    assertEquals("oryn://automations", PushPayload.deepLink("/settings/automations?schedule=x"))
    assertEquals("oryn://automations", PushPayload.deepLink("/settings/automations"))
    assertEquals("oryn://open", PushPayload.deepLink("/settings/notifications"))
    assertEquals("oryn://open", PushPayload.deepLink("/statsx"))
  }

  @Test
  fun parsesAvatarAndTimestampAndDisplayTitle() {
    val m = parse(
      """{"v":1,"id":"n1","kind":"done","title":"🐳 DOCKER: Speed up","body":"ok","url":"/t/$thread","ts":1730000000000,""" +
        """"avatar":{"kind":"thread","icon":"🐳","color":"blue"}}""",
    )!!
    assertEquals(1730000000000L, m.timestamp)
    assertEquals(PushPayload.Avatar(kind = "thread", icon = "🐳", color = "blue"), m.avatar)
    assertEquals("DOCKER: Speed up", PushPayload.displayTitle(m.title, m.avatar))

    // 全局助手对话：头像用全局助手头像，不剥掉标题里的 emoji
    val am = parse(
      """{"v":1,"id":"n2","kind":"done","title":"✨ CHAT: 周报","body":"ok","url":"/t/$thread",""" +
        """"avatar":{"kind":"assistant","icon":"🦊","hash":"a1b2c3d4"}}""",
    )!!
    assertEquals(PushPayload.Avatar(kind = "assistant", icon = "🦊", hash = "a1b2c3d4"), am.avatar)
    assertEquals("✨ CHAT: 周报", PushPayload.displayTitle(am.title, am.avatar))

    // encode 保留轻量 avatar 元数据但不序列化大 image
    val withImg = am.copy(avatar = am.avatar!!.copy(image = "AAAA=="))
    val roundTrip = PushPayload.parse(PushPayload.encode(withImg).toByteArray(Charsets.UTF_8))!!
    assertNull(roundTrip.avatar?.image)
    assertEquals("a1b2c3d4", roundTrip.avatar?.hash)
  }

  @Test
  fun effectiveAvatarFallsBackWhenServerOmitsAvatar() {
    val nerd = "\uD83D\uDC33" // 🐳
    val threadModel = PushPayload.Model("n1", "$nerd DOCKER: Build", "ok", "/t/$thread", "oryn-1")
    assertEquals(PushPayload.Avatar(kind = "thread", icon = nerd), PushPayload.effectiveAvatar(threadModel))

    val assistantModel = PushPayload.Model("n2", "Research paused", "ok", "/settings/assistant", "oryn-budget")
    assertEquals(PushPayload.Avatar(kind = "assistant"), PushPayload.effectiveAvatar(assistantModel))

    val sysModel = PushPayload.Model("n3", "Budget", "ok", "/stats/usage", "oryn-budget")
    assertEquals(PushPayload.Avatar(kind = "system"), PushPayload.effectiveAvatar(sysModel))

    assertEquals(true, PushPayload.isNerdGlyph("\uDB82\uDC68")) // U+F0868 md-docker
    assertEquals(false, PushPayload.isNerdGlyph("🐳"))
  }
}
