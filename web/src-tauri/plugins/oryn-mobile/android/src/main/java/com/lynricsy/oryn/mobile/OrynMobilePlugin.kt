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
}

@InvokeArg
class PushEnableArgs {
  var vapid: String? = null
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
 * Kotlin → Rust 的事件统一经 `trigger("event", {type, …})`：resume / stop / push / share / insets / recreated。
 *
 * 插件对象与进程同寿：Tauri 只在第一个 WebView 出现时调一次 [load]。Activity 因未声明的配置变更（字体大小等）
 * 重建时，wry 在新 Activity 里重建 WebView，插件却不会再收到 [load]（代码审查第 1 条）。所以这里不把 Activity /
 * WebView 当构造参数用到底：`MainActivity.onWebViewCreate` 每次建 WebView 都调 [webViewCreated]，
 * 插件改绑到新的 Activity 与 WebView，重新装下载监听、第三方 Cookie、inset 监听与系统栏颜色，并告诉 Rust 重建前所在的页面。
 */
@TauriPlugin(
  permissions = [Permission(strings = [Manifest.permission.POST_NOTIFICATIONS], alias = "notifications")],
)
class OrynMobilePlugin(initial: Activity) : Plugin(initial) {
  /** 当前的 Activity（重建后换成新的）。 */
  @Volatile private var activity: Activity = initial
  /** 当前绑定的主 WebView。 */
  private var webView: WebView? = null
  /** 配置变更销毁 Activity 时主框架所在的地址（UI 线程读写）。 */
  private var urlBeforeRecreate: String? = null
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
      if (p != null && p.hasListener("event")) {
        val o = JSObject()
        o.put("type", "push")
        o.put("state", PushStore.state(context))
        p.trigger("event", o)
      } else {
        PushSync.schedule(context)
      }
    }

    /** `MainActivity.onWebViewCreate`：每个新建的主 WebView（含 Activity 重建后的）都走这里。 */
    fun webViewCreated(activity: AppCompatActivity, webView: WebView) {
      instance?.bind(activity, webView)
    }
  }

  override fun load(webView: WebView) {
    bind(activity, webView)
  }

  /** 绑定到（新的）Activity 与 WebView；同一个 WebView 重复调用无副作用。在 UI 线程上调用。 */
  private fun bind(act: Activity, wv: WebView) {
    if (wv === webView) return
    val replaced = webView != null
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
    if (replaced) {
      val url = urlBeforeRecreate
      urlBeforeRecreate = null
      EventLog.log(act, "plugin.rebound", mapOf("recreated" to true))
      val o = JSObject()
      o.put("type", "recreated")
      o.put("url", url ?: JSONObject.NULL)
      trigger("event", o)
    }
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
    // 配置变更：马上会有新 Activity 与新 WebView（webViewCreated）；记下主框架所在页面，好让 Rust 恢复到这里
    if (activity === this.activity && activity.isChangingConfigurations) {
      urlBeforeRecreate = webView?.url
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
    val model = PushPayload.Model(a.id, a.title, a.body, a.path, PushPayload.tagFor(a.path, a.id))
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

  private fun enablePush(invoke: Invoke) {
    val vapid = invoke.parseArgs(PushEnableArgs::class.java).vapid
    val act = activity
    PushStore.setError(act, null)
    act.runOnUiThread {
      UnifiedPush.tryUseCurrentOrDefaultDistributor(act) { ok ->
        PushStore.setEnabled(act, true)
        val fcm = Fcm.bridge
        when {
          ok -> {
            try {
              UnifiedPush.register(act, messageForDistributor = "Oryn", vapid = vapid)
            } catch (e: Exception) {
              PushStore.setError(act, "vapid_invalid")
            }
            EventLog.log(act, "push.enable", mapOf("channel" to "unifiedpush"))
            invoke.resolve(PushStore.state(act))
          }
          fcm != null -> {
            EventLog.log(act, "push.enable", mapOf("channel" to "fcm"))
            fcm.token(act) { token, error ->
              PushStore.setFcmToken(act, token)
              if (token == null) PushStore.setError(act, error ?: "fcm_token")
              // state() 顺带生成 FCM 的接收端密钥（随令牌一起登记）
              invoke.resolve(PushStore.state(act))
            }
          }
          else -> {
            EventLog.log(act, "push.enable", mapOf("channel" to null))
            invoke.resolve(PushStore.state(act))
          }
        }
      }
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

  /** 模板默认 edge-to-edge：给内容容器加「系统栏 + 刘海 + 输入法」的 padding 并消费 inset，WebView 始终落在安全区内。 */
  private fun installInsets(act: Activity) {
    val content = act.findViewById<View>(android.R.id.content) ?: return
    ViewCompat.setOnApplyWindowInsetsListener(content) { v, wi ->
      val bars = wi.getInsets(
        WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout() or WindowInsetsCompat.Type.ime(),
      )
      v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
      val platform = wi.toWindowInsets()
      val corners = listOf(
        RoundedCorner.POSITION_TOP_LEFT,
        RoundedCorner.POSITION_TOP_RIGHT,
        RoundedCorner.POSITION_BOTTOM_LEFT,
        RoundedCorner.POSITION_BOTTOM_RIGHT,
      ).map { platform?.getRoundedCorner(it)?.radius }
      val next = ViewportInsets.fromPx(
        v.resources.displayMetrics.density,
        bars.top,
        bars.right,
        bars.bottom,
        bars.left,
        corners,
      )
      if (next != insets) {
        insets = next
        emitInsets(next)
      }
      WindowInsetsCompat.CONSUMED
    }
    ViewCompat.requestApplyInsets(content)
  }

  private fun emitInsets(snap: ViewportInsets) {
    val o = snap.toJson()
    EventLog.log(activity, "insets", mapOf("insets" to o.getJSONObject("insets")))
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
