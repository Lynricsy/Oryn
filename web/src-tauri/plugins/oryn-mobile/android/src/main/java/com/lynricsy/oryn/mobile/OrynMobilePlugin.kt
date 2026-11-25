package com.lynricsy.oryn.mobile

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.res.Configuration
import android.graphics.Color
import android.os.Build
import android.provider.Settings
import android.view.RoundedCorner
import android.view.View
import android.webkit.CookieManager
import android.webkit.WebView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.graphics.Insets as CoreInsets
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.webkit.WebViewCompat
import app.tauri.annotation.Command
import app.tauri.annotation.InvokeArg
import app.tauri.annotation.Permission
import app.tauri.annotation.PermissionCallback
import app.tauri.annotation.TauriPlugin
import app.tauri.plugin.Invoke
import app.tauri.plugin.JSObject
import app.tauri.plugin.Plugin
import org.json.JSONObject
import org.unifiedpush.android.connector.UnifiedPush

@InvokeArg
class ServerArgs {
  lateinit var server: String
}

@InvokeArg
class VaultSetArgs {
  lateinit var server: String
  lateinit var token: String
}

@InvokeArg
class NotifyArgs {
  var id: String = ""
  lateinit var title: String
  var body: String = ""
  lateinit var path: String
  var lang: String? = null
  var timestamp: Long? = null
  var avatar: JSObject? = null
}

@InvokeArg
class PushEnableArgs {
  var vapid: String? = null
  /** `default`（gms 包走 FCM、foss 包走 UnifiedPush）或 `unifiedpush`（gms 包手动切换）。 */
  var transport: String? = null
}

@InvokeArg
class PushSyncedArgs {
  lateinit var endpoint: String
  lateinit var id: String
  lateinit var server: String
}

@InvokeArg
class ChromeArgs {
  var dark: Boolean = false
  lateinit var color: String
}

@InvokeArg
class IdArgs {
  lateinit var id: String
}

@InvokeArg
class TextArgs {
  lateinit var text: String
}

/**
 * Oryn 的原生能力（只供 Rust 经 `run_mobile_plugin` 调用；插件的 JS 命令表为空，远程页面调不到）。
 * Kotlin → Rust 的事件统一经 `trigger("event", {type, …})`：resume / stop / push / pushchallenge / share / insets / recreated / activity。
 *
 * 插件对象与进程同寿：Tauri 只在第一个 WebView 出现时调一次 [load]。Activity 因未声明的配置变更（字体大小等）
 * 重建时，wry 在新 Activity 里重建 WebView，插件却不会再收到 [load]（代码审查第 1 条）。所以这里不把 Activity /
 * WebView 当构造参数用到底：`MainActivity.onWebViewCreate` 每次建 WebView 都调 [webViewCreated]，
 * 插件改绑到新的 Activity 与 WebView，重新装下载监听、第三方 Cookie、inset 监听与系统栏颜色，并告诉 Rust 重建前所在的页面。
 * Activity 被系统销毁（不是配置变更）时进程保留、Rust 的主窗口没了：`MainActivity.onCreate` 调 [activityCreated]，
 * 插件报 `activity` 让 Rust 为新 Activity 重建主窗口，之后同样走 [webViewCreated]。
 */
@TauriPlugin(
  permissions = [Permission(strings = [Manifest.permission.POST_NOTIFICATIONS], alias = "notifications")],
)
class OrynMobilePlugin(initial: Activity) : Plugin(initial) {
  /** 当前的 Activity（重建后换成新的）。 */
  @Volatile private var activity: Activity = initial
  /** 当前绑定的主 WebView。 */
  private var webView: WebView? = null
  /** 与 Activity 的对应：配置变更 / 系统销毁、销毁前所在页面（见 [ActivityLink]）。 */
  private val link = ActivityLink()
  @Volatile private var insets: ViewportInsets = ViewportInsets.ZERO
  /** 冷启动时 Rust 还没注册监听就到达的分享，注册后补发（见 [ShareHandoff]）。 */
  private val shares = ShareHandoff<JSObject>(emit = { share ->
    val o = JSObject()
    o.put("type", "share")
    o.put("share", share)
    trigger("event", o)
  })

  init {
    instance = this
  }

  companion object {
    private const val INTENT_CONSUMED = "com.lynricsy.oryn.mobile.share.consumed"
    private val COLOR = Regex("^#[0-9a-fA-F]{6}$")

    @Volatile var instance: OrynMobilePlugin? = null
      private set
    /** App 是否在前台（onResume ~ onStop），推送据此决定是否发系统通知。 */
    @Volatile var visible = false

    /**
     * 推送状态变了（新令牌 / 新端点 / 注册失败）：Rust 在跑就交给它重取并同步；
     * 进程里只有推送服务（没有 Activity，也就没有 Rust 的监听）时排一个后台登记任务（代码审查第 6 条）。
     */
    fun pushChanged(context: Context) {
      val p = instance
      // 没有 Activity 时 Rust 调不了插件命令（重取推送状态会失败）：同样走后台任务
      if (p != null && p.link.attached && p.hasListener("event")) {
        val o = JSObject()
        o.put("type", "push")
        o.put("state", PushStore.state(context))
        p.trigger("event", o)
      } else {
        PushSync.schedule(context)
      }
    }

    /** 官方推送网关的 challenge 推送到了（gms 的 `OrynFcmService`）：交给 Rust 完成授权。Rust 不在跑时丢弃（登记只在前台进行）。 */
    fun pushChallenge(context: Context, challengeId: String, nonce: String) {
      val p = instance
      if (p == null || !p.hasListener("event")) {
        EventLog.log(context, "push.challenge_dropped")
        return
      }
      val o = JSObject()
      o.put("type", "pushchallenge")
      o.put("challengeId", challengeId)
      o.put("nonce", nonce)
      p.trigger("event", o)
    }

    /** `MainActivity.onWebViewCreate`：每个新建的主 WebView（含 Activity 重建后的）都走这里。 */
    fun webViewCreated(activity: AppCompatActivity, webView: WebView) {
      instance?.bind(activity, webView)
    }

    /**
     * `MainActivity.onCreate`（`super.onCreate` 之后：tao / wry 已登记这个 Activity）。先前的 Activity 被系统销毁、
     * 进程留着时，Rust 那边已经没有主窗口：报 `activity` 让它为这个 Activity 重建，WebView 建好后照常走 [webViewCreated]。
     * 冷启动时插件还没创建，配置变更重建时窗口还在，都不报。
     */
    fun activityCreated(activity: AppCompatActivity) {
      val p = instance ?: return
      if (!p.link.activityCreated()) return
      p.activity = activity
      EventLog.log(activity, "plugin.activity")
      p.trigger("event", JSObject().apply { put("type", "activity") })
    }
  }

  override fun load(webView: WebView) {
    bind(activity, webView)
    migrateToFcm(activity)
  }

  /**
   * 用户决定 D5：此前启用了 UnifiedPush 的 gms 用户升级后改走网关 FCM（[PushStore.channelFor]）。这些用户从没取过 FCM 令牌，
   * 这里补取一次并注销 UnifiedPush；令牌到了经 [pushChanged] 交给 Rust，Rust 先删掉服务器上旧的 Web Push 订阅再走网关登记。
   */
  private fun migrateToFcm(act: Activity) {
    val fcm = Fcm.bridge ?: return
    if (!PushStore.isEnabled(act) || PushStore.preferUp(act) || PushStore.fcmToken(act) != null) return
    EventLog.log(act, "push.migrate", mapOf("to" to "fcm"))
    unregisterUnifiedPush(act)
    val app = act.applicationContext
    fcm.token(app) { token, error ->
      PushStore.setFcmToken(app, token)
      if (token == null) PushStore.setError(app, error ?: "fcm_token")
      pushChanged(app)
    }
  }

  /** 绑定到（新的）Activity 与 WebView；同一个 WebView 重复调用无副作用。在 UI 线程上调用。 */
  private fun bind(act: Activity, wv: WebView) {
    if (wv === webView) return
    activity = act
    webView = wv
    // portal iframe 与主站不同站点时也要带 Cookie
    CookieManager.getInstance().setAcceptThirdPartyCookies(wv, true)
    wv.setDownloadListener { url, userAgent, contentDisposition, mimeType, _ ->
      Downloads.start(act.applicationContext, url, userAgent, contentDisposition, mimeType)
    }
    installInsets(act)
    applyChrome(initialDark(), initialColor())
    Notifier.ensureChannel(act)
    handleIntent(act.intent)
    val rebound = link.webViewBound() ?: return
    EventLog.log(act, "plugin.rebound", mapOf("recreated" to true))
    val o = JSObject()
    o.put("type", "recreated")
    o.put("url", rebound.url ?: JSONObject.NULL)
    trigger("event", o)
  }

  // ---- 生命周期 ----

  override fun onResume(activity: AppCompatActivity) {
    this.activity = activity
    visible = true
    Notifier.cancelAll(activity)
    trigger("event", JSObject().apply { put("type", "resume") })
  }

  override fun onStop(activity: AppCompatActivity) {
    visible = false
    trigger("event", JSObject().apply { put("type", "stop") })
  }

  override fun onDestroy(activity: AppCompatActivity) {
    visible = false
    // 记下主框架所在页面，新 WebView 绑定时让 Rust 恢复到这里；不是配置变更时 Rust 的主窗口也随之没了（见 [ActivityLink]）
    if (activity === this.activity) {
      val changing = activity.isChangingConfigurations
      EventLog.log(activity, "plugin.destroy", mapOf("changingConfigurations" to changing, "finishing" to activity.isFinishing))
      link.destroyed(changing, webView?.url)
    }
  }

  override fun onNewIntent(intent: Intent) {
    handleIntent(intent)
  }

  /** Rust 注册监听后补发冷启动时错过的分享与当前 inset。 */
  @Command
  override fun registerListener(invoke: Invoke) {
    super.registerListener(invoke)
    shares.attach()
    if (insets != ViewportInsets.ZERO) emitInsets(insets)
  }

  // ---- 命令（2.2 的表） ----

  @Command
  fun deviceInfo(invoke: Invoke) {
    val act = activity
    val o = JSObject()
    o.put("name", "${Build.MANUFACTURER} ${Build.MODEL}".trim())
    o.put("sdkInt", Build.VERSION.SDK_INT)
    o.put("webviewVersion", WebViewCompat.getCurrentWebViewPackage(act)?.versionName ?: "")
    o.put("debuggable", act.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0)
    o.put("eventLogPath", EventLog.path(act) ?: JSONObject.NULL)
    val snap = insets.toJson()
    o.put("insets", snap.getJSONObject("insets"))
    o.put("safeArea", snap.getJSONObject("safeArea"))
    o.put("cornerRadii", snap.getJSONObject("cornerRadii"))
    invoke.resolve(o)
  }

  @Command
  fun vaultGet(invoke: Invoke) {
    val args = invoke.parseArgs(ServerArgs::class.java)
    invoke.resolve(JSObject().apply { put("token", Vault.get(activity, args.server) ?: JSONObject.NULL) })
  }

  @Command
  fun vaultSet(invoke: Invoke) {
    val args = invoke.parseArgs(VaultSetArgs::class.java)
    Vault.set(activity, args.server, args.token)
    invoke.resolve()
  }

  @Command
  fun vaultDelete(invoke: Invoke) {
    val args = invoke.parseArgs(ServerArgs::class.java)
    Vault.delete(activity, args.server)
    invoke.resolve()
  }

  @Command
  fun notify(invoke: Invoke) {
    val a = invoke.parseArgs(NotifyArgs::class.java)
    val avatar = PushPayload.parseAvatar(a.avatar)
    NotificationAvatarRenderer.updateAssistantCache(activity, avatar)
    val model = PushPayload.Model(
      id = a.id,
      title = a.title,
      body = a.body,
      path = a.path,
      tag = PushPayload.tagFor(a.path, a.id),
      timestamp = a.timestamp?.takeIf { it > 0L } ?: 0L,
      avatar = avatar,
    )
    val reason = Notifier.post(activity, model, a.lang)
    val o = JSObject()
    o.put("posted", reason == null)
    if (reason != null) o.put("reason", reason)
    invoke.resolve(o)
  }

  @Command
  fun pushState(invoke: Invoke) {
    invoke.resolve(PushStore.state(activity))
  }

  @Command
  fun pushEnable(invoke: Invoke) {
    if (!PushStore.notificationsAllowed(activity)) {
      PushStore.markAsked(activity)
      requestPermissionForAlias("notifications", invoke, "pushPermissionCallback")
      return
    }
    enablePush(invoke)
  }

  /** 授权框关闭后（无论结果）继续启用；没授权时状态里的 permission 会如实反映。 */
  @PermissionCallback
  fun pushPermissionCallback(invoke: Invoke) {
    enablePush(invoke)
  }

  /**
   * 启用推送，两条通道互斥：`transport` 为 `unifiedpush` 或本包不带 FCM 时走 UnifiedPush，并先删掉 FCM 令牌与接收端密钥；
   * 否则走 FCM（经官方推送网关），并先注销 UnifiedPush。只有手动切到 UnifiedPush 才记下 `preferUp`。
   */
  private fun enablePush(invoke: Invoke) {
    val args = invoke.parseArgs(PushEnableArgs::class.java)
    val act = activity
    val fcm = Fcm.bridge
    val wantUp = args.transport == "unifiedpush" || fcm == null
    PushStore.setError(act, null)
    PushStore.setPreferUp(act, fcm != null && wantUp)
    if (!wantUp) {
      unregisterUnifiedPush(act)
      PushStore.setEnabled(act, true)
      EventLog.log(act, "push.enable", mapOf("channel" to "fcm"))
      fcm.token(act) { token, error ->
        PushStore.setFcmToken(act, token)
        if (token == null) PushStore.setError(act, error ?: "fcm_token")
        // state() 顺带生成 FCM 的接收端密钥（随授权一起登记）
        invoke.resolve(PushStore.state(act))
      }
      return
    }
    if (fcm != null) {
      fcm.delete(act)
      PushStore.setFcmToken(act, null)
      FcmKeys.clear(act)
    }
    act.runOnUiThread {
      UnifiedPush.tryUseCurrentOrDefaultDistributor(act) { ok ->
        PushStore.setEnabled(act, true)
        if (ok) {
          try {
            UnifiedPush.register(act, messageForDistributor = "Oryn", vapid = args.vapid)
          } catch (e: Exception) {
            PushStore.setError(act, "vapid_invalid")
          }
        }
        EventLog.log(act, "push.enable", mapOf("channel" to if (ok) "unifiedpush" else null))
        invoke.resolve(PushStore.state(act))
      }
    }
  }

  /** 注销 UnifiedPush（尽力而为；没有分发器时什么也不做）。 */
  private fun unregisterUnifiedPush(context: Context) {
    try {
      if (UnifiedPush.getSavedDistributor(context) != null) UnifiedPush.unregister(context)
    } catch (_: Exception) {
    }
  }

  @Command
  fun pushDisable(invoke: Invoke) {
    val act = activity
    try {
      if (UnifiedPush.getSavedDistributor(act) != null) UnifiedPush.unregister(act)
    } catch (_: Exception) {
    }
    Fcm.bridge?.delete(act)
    PushStore.clear(act)
    FcmKeys.clear(act)
    EventLog.log(act, "push.disable")
    invoke.resolve(PushStore.state(act))
  }

  @Command
  fun pushMarkSynced(invoke: Invoke) {
    val args = invoke.parseArgs(PushSyncedArgs::class.java)
    PushStore.markSynced(activity, args.endpoint, args.id, args.server)
    invoke.resolve()
  }

  @Command
  fun openNotificationSettings(invoke: Invoke) {
    val act = activity
    val intent = Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
      .putExtra(Settings.EXTRA_APP_PACKAGE, act.packageName)
      .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    act.runOnUiThread { act.startActivity(intent) }
    invoke.resolve()
  }

  @Command
  fun setChrome(invoke: Invoke) {
    val args = invoke.parseArgs(ChromeArgs::class.java)
    if (!COLOR.matches(args.color)) {
      invoke.reject("invalid color")
      return
    }
    applyChrome(args.dark, Color.parseColor(args.color))
    invoke.resolve()
  }

  @Command
  fun clearHistory(invoke: Invoke) {
    activity.runOnUiThread { webView?.clearHistory() }
    invoke.resolve()
  }

  @Command
  fun shareAck(invoke: Invoke) {
    val args = invoke.parseArgs(IdArgs::class.java)
    val app = activity.applicationContext
    Thread { ShareIntake.ack(app, args.id) }.start()
    invoke.resolve()
  }

  @Command
  fun toast(invoke: Invoke) {
    val args = invoke.parseArgs(TextArgs::class.java)
    val act = activity
    act.runOnUiThread { Toast.makeText(act, args.text, Toast.LENGTH_SHORT).show() }
    invoke.resolve()
  }

  // ---- 分享 ----

  private fun handleIntent(intent: Intent?) {
    if (intent == null) return
    if (intent.action != Intent.ACTION_SEND && intent.action != Intent.ACTION_SEND_MULTIPLE) return
    // Activity 重建时 activity.intent 仍是同一个分享，打标记避免重复导入
    if (intent.getBooleanExtra(INTENT_CONSUMED, false)) return
    intent.putExtra(INTENT_CONSUMED, true)
    val app = activity.applicationContext
    Thread {
      val share = try {
        ShareIntake.intake(app, intent)
      } catch (_: Exception) {
        null
      }
      EventLog.log(
        app,
        "share.intent",
        mapOf("files" to (share?.getJSONArray("files")?.length() ?: 0), "text" to (share?.isNull("text") == false)),
      )
      if (share != null) shares.offer(share)
    }.start()
  }

  // ---- inset、系统栏 ----

  /**
   * 真正 edge-to-edge：WebView 铺满整屏（状态栏、刘海与导航栏之下均可见），原生容器只为软键盘加底部 padding；
   * 扣除键盘后的剩余 insets 透传给 WebView（供现代 Chromium 填充 CSS `env(safe-area-inset-*)`），
   * 同时将物理圆角与安全区上报给 Rust / Web 侧兜底（覆盖不支持 systemBars env() 的旧版或定制 WebView）。
   */
  private fun installInsets(act: Activity) {
    val content = act.findViewById<View>(android.R.id.content) ?: return
    ViewCompat.setOnApplyWindowInsetsListener(content) { v, wi ->
      val bars = wi.getInsets(
        WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout(),
      )
      val tappable = wi.getInsets(WindowInsetsCompat.Type.tappableElement())
      val ime = wi.getInsets(WindowInsetsCompat.Type.ime())
      val density = v.resources.displayMetrics.density
      v.setPadding(0, 0, 0, ime.bottom)
      val platform = wi.toWindowInsets()
      val corners = listOf(
        RoundedCorner.POSITION_TOP_LEFT,
        RoundedCorner.POSITION_TOP_RIGHT,
        RoundedCorner.POSITION_BOTTOM_LEFT,
        RoundedCorner.POSITION_BOTTOM_RIGHT,
      ).map { platform?.getRoundedCorner(it)?.radius }
      val next = ViewportInsets.fromWindowInsets(
        density = density,
        safeTop = bars.top,
        safeRight = bars.right,
        safeBottom = bars.bottom,
        safeLeft = bars.left,
        imeBottom = ime.bottom,
        corners = corners,
        tappableBottom = tappable.bottom,
      )
      if (next != insets) {
        insets = next
        emitInsets(next)
      }
      val effectiveBottom = ViewportInsets.effectiveSafeBottomPx(
        barsBottom = bars.bottom,
        tappableBottom = tappable.bottom,
        imeBottom = ime.bottom,
        density = density,
      )
      val remaining = wi.inset(0, 0, 0, ime.bottom)
      if (tappable.bottom <= 0 && ime.bottom <= 0 && effectiveBottom < bars.bottom) {
        val nav = remaining.getInsets(WindowInsetsCompat.Type.navigationBars())
        val sys = remaining.getInsets(WindowInsetsCompat.Type.systemBars())
        WindowInsetsCompat.Builder(remaining)
          .setInsets(
            WindowInsetsCompat.Type.navigationBars(),
            CoreInsets.of(nav.left, nav.top, nav.right, effectiveBottom),
          )
          .setInsets(
            WindowInsetsCompat.Type.systemBars(),
            CoreInsets.of(sys.left, sys.top, sys.right, effectiveBottom),
          )
          .build()
      } else {
        remaining
      }
    }
    ViewCompat.requestApplyInsets(content)
  }

  private fun emitInsets(snap: ViewportInsets) {
    val o = snap.toJson()
    EventLog.log(
      activity,
      "insets",
      mapOf(
        "insets" to o.getJSONObject("insets"),
        "safeArea" to o.getJSONObject("safeArea"),
      ),
    )
    o.put("type", "insets")
    trigger("event", o)
  }

  override fun onConfigurationChanged(newConfig: Configuration) {
    // 旋转、折叠展开时 inset 通常会自己重新分发；这里再请求一次兜底
    activity.findViewById<View>(android.R.id.content)?.let { ViewCompat.requestApplyInsets(it) }
  }

  private fun applyChrome(dark: Boolean, color: Int) {
    val act = activity
    act.runOnUiThread {
      val window = act.window
      act.findViewById<View>(android.R.id.content)?.setBackgroundColor(color)
      window.decorView.setBackgroundColor(color)
      // 禁止系统在透明导航栏下方叠半透明对比度遮罩，由网页自身背景延伸铺满
      window.isNavigationBarContrastEnforced = false
      val controller = WindowCompat.getInsetsController(window, window.decorView)
      controller.isAppearanceLightStatusBars = !dark
      controller.isAppearanceLightNavigationBars = !dark
    }
  }

  private fun initialDark(): Boolean =
    activity.resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK == Configuration.UI_MODE_NIGHT_YES

  private fun initialColor(): Int {
    val act = activity
    val id = act.resources.getIdentifier("oryn_bg", "color", act.packageName)
    return if (id != 0) act.getColor(id) else if (initialDark()) Color.BLACK else Color.WHITE
  }
}
