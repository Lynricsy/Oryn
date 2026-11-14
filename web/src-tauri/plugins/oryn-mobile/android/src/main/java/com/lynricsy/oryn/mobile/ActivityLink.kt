package com.lynricsy.oryn.mobile

/**
 * 插件与 Activity / 主 WebView 的对应关系（纯逻辑，JVM 单测）。
 *
 * Activity 有两种销毁：
 * - 配置变更（字体大小等未声明的变更）：wry 保留 Rust 窗口，在新 Activity 里自己重建 WebView；
 * - 其它（「不保留活动」、系统回收 Activity 但保留进程、返回键结束 Activity）：Tauri 删掉唯一的窗口，
 *   Rust 拦下随之而来的退出，进程留着。之后同一进程里再建的 Activity 没有窗口，要由插件报 `activity` 事件
 *   让 Rust 重建主窗口（见 Rust 的 `MainWindowLife`）。
 *
 * 两种情况下新 WebView 绑定时都报 `recreated`，带上销毁前主框架所在的地址，Rust 恢复会话后回到那里。
 * 不区分 `isFinishing`（返回键结束时为真）：下次打开同样回到原页面，和系统销毁后切回来一致；
 * 从最近任务划掉或系统回收进程则是整个进程被杀，走不到这里，下次是冷启动。
 *
 * 状态在 UI 线程上改；[attached] 也会被推送服务的线程读。
 */
class ActivityLink {
  /** 新 WebView 绑定时的结果：`url` 为销毁前所在地址。 */
  data class Rebound(val url: String?)

  private var bound = false
  @Volatile private var detached = false
  private var urlBefore: String? = null

  /** 进程里有活着的 Activity（Rust 能调插件命令）。 */
  val attached: Boolean
    get() = !detached

  /** 当前绑定的 Activity 正在销毁；`url` 是主框架此刻所在地址。 */
  fun destroyed(changingConfigurations: Boolean, url: String?) {
    urlBefore = url
    if (!changingConfigurations) detached = true
  }

  /** 新 Activity 的 `onCreate`。返回 `true` = 先前的 Activity 已被销毁（不是配置变更）、主窗口没了，要让 Rust 重建。 */
  fun activityCreated(): Boolean = detached

  /** 新 Activity 的主 WebView 已建好并绑定。第一次绑定返回 `null`，之后返回 [Rebound]。 */
  fun webViewBound(): Rebound? {
    detached = false
    val first = !bound
    bound = true
    val url = urlBefore
    urlBefore = null
    return if (first) null else Rebound(url)
  }
}
