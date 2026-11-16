package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PushAnswerTest {
  @Test
  fun urlOnlyForNotificationIds() {
    assertEquals(
      "https://oryn.example/api/v1/notifications/ntf_01ABC/answer",
      PushAnswer.url("https://oryn.example/", "ntf_01ABC"),
    )
    assertNull(PushAnswer.url("https://oryn.example", "../../auth"))
    assertNull(PushAnswer.url("https://oryn.example", "ntf_1/../x"))
    assertNull(PushAnswer.url("https://oryn.example", ""))
  }

  @Test
  fun stateForStatus() {
    assertEquals(Notifier.State.ANSWERED, PushAnswer.stateFor(200))
    assertEquals(Notifier.State.ELSEWHERE, PushAnswer.stateFor(409))
    assertEquals(Notifier.State.RETRY, PushAnswer.stateFor(503))
    assertEquals(Notifier.State.RETRY, PushAnswer.stateFor(429))
    assertEquals(Notifier.State.FAILED, PushAnswer.stateFor(401))
    assertEquals(Notifier.State.FAILED, PushAnswer.stateFor(422))
  }

  @Test
  fun stateBodyAndLabels() {
    assertEquals("问题", Notifier.stateBody("问题", null, "zh-CN"))
    assertEquals("问题\n正在提交…", Notifier.stateBody("问题", Notifier.State.SENDING, "zh-CN"))
    assertEquals("已作答", Notifier.stateBody("问题", Notifier.State.ANSWERED, "zh-CN"))
    assertEquals("Already answered elsewhere", Notifier.stateBody("q", Notifier.State.ELSEWHERE, "en"))
    assertEquals("q\nCouldn't answer. Tap to open Oryn.", Notifier.stateBody("q", Notifier.State.RETRY, "en"))
    assertEquals("是", Notifier.actionTitle(PushPayload.Action(0, "Yes", "yes"), "zh-CN"))
    assertEquals("No", Notifier.actionTitle(PushPayload.Action(1, "No", "no"), "en"))
    assertEquals("Approve", Notifier.actionTitle(PushPayload.Action(0, "Approve"), "zh-CN"))
  }
}
