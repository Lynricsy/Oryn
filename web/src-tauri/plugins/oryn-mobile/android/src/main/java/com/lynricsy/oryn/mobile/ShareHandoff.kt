package com.lynricsy.oryn.mobile

import java.util.concurrent.ConcurrentLinkedQueue

/**
 * 冷启动分享的交接：解析分享的工作线程可能早于 Rust 注册监听（原生命令线程）完成。
 * 旧写法「先看有没有监听、没有就写进 pending」与「注册后读一次 pending」不是原子的：工作线程判断完、
 * 写入前，注册线程恰好注册并读到空的 pending，分享就永远留在 pending 里（代码审查第 7 条）。
 *
 * 这里两边都是「先写自己的标记、再看对方的」：工作线程先入队再看 [attached]，注册线程先置 [attached] 再取队列。
 * volatile 写读之间是顺序一致的，任何交错下后到的一方都能看见先到一方的写入，队列的 poll 保证每条恰好发出一次。
 * 多条分享依次发给 Rust，旧的由 Rust 那边作废并清缓存（只保留最新一次）。
 */
class ShareHandoff<T>(
  private val emit: (T) -> Unit,
  /** 测试钩子：入队之后、检查 [attached] 之前执行（用来把注册线程插进旧写法出错的那个窗口）。 */
  private val afterEnqueue: () -> Unit = {},
) {
  private val queue = ConcurrentLinkedQueue<T>()
  @Volatile private var attached = false

  /** 工作线程：收到一次分享。 */
  fun offer(item: T) {
    queue.add(item)
    afterEnqueue()
    if (attached) drain()
  }

  /** Rust 的监听已注册：之后的分享直接发出，之前排队的现在补发。 */
  fun attach() {
    attached = true
    drain()
  }

  private fun drain() {
    while (true) {
      val item = queue.poll() ?: return
      emit(item)
    }
  }
}
