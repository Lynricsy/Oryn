package com.lynricsy.oryn.mobile

import org.junit.Assert.assertEquals
import org.junit.Test
import java.util.Collections

class ShareHandoffTest {
  @Test
  fun queuedBeforeAttachIsDeliveredOnAttach() {
    val out = mutableListOf<String>()
    val h = ShareHandoff<String>({ out.add(it) })
    h.offer("a")
    h.offer("b")
    assertEquals(emptyList<String>(), out)
    h.attach()
    assertEquals(listOf("a", "b"), out)
    h.offer("c")
    assertEquals(listOf("a", "b", "c"), out)
  }

  /**
   * 代码审查第 7 条的交错：工作线程已入队、尚未检查「监听是否就绪」时，注册线程完成注册并取队列。
   * 旧写法（先查 hasListener 再写 pending）在这个窗口里会把分享永远留在 pending；现在恰好发出一次。
   */
  @Test
  fun registrarRunningInsideTheOfferWindowStillDeliversExactlyOnce() {
    val out = mutableListOf<String>()
    lateinit var h: ShareHandoff<String>
    h = ShareHandoff({ out.add(it) }, afterEnqueue = { h.attach() })
    h.offer("share")
    assertEquals(listOf("share"), out)
  }

  /** 两条线程各自按真实顺序跑（用闩锁固定交错点，不靠 sleep）：每条分享恰好一次。 */
  @Test
  fun concurrentOfferAndAttachDeliverEveryItemOnce() {
    repeat(200) {
      val out = Collections.synchronizedList(mutableListOf<Int>())
      val start = java.util.concurrent.CountDownLatch(1)
      val h = ShareHandoff<Int>({ out.add(it) })
      val worker = Thread {
        start.await()
        for (i in 0 until 20) h.offer(i)
      }
      val registrar = Thread {
        start.await()
        h.attach()
      }
      worker.start()
      registrar.start()
      start.countDown()
      worker.join()
      registrar.join()
      assertEquals((0 until 20).toList(), out.sorted())
    }
  }
}
