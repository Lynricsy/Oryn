package com.lynricsy.oryn.mobile

import android.Manifest
import android.app.Activity
import android.app.DownloadManager
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.res.Configuration
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.provider.Settings
import android.view.RoundedCorner
import android.view.View
import android.webkit.CookieManager
import android.webkit.URLUtil
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
 * Kotlin → Rust 的事件统一经 `trigger("event", {type, …})`：resume / stop / push / share / insets。
 */
@TauriPlugin(
  permissions = [Permission(strings = [Manifest.permission.POST_NOTIFICATIONS], alias = "notifications")],
)
class OrynMobilePlugin(private val activity: Activity) : Plugin(activity) {
  private var webView: WebView? = null
  @Volatile private var insets: ViewportInsets = ViewportInsets.ZERO
  /** Rust 还没注册监听时到达的分享（冷启动），注册后补发。 */
  @Volatile private var pendingShare: JSObject? = null

  companion object {
    private const val INTENT_CONSUMED = "com.lynricsy.oryn.mobile.share.consumed"
    private val COLOR = Regex("^#[0-9a-fA-F]{6}$")

    @Volatile var instance: OrynMobilePlugin? = null
    /** App 是否在前台（onResume ~ onStop），推送据此决定是否发系统通知。 */
    @Volatile var visible = false

    /** 推送状态变了：告诉 Rust 重取 / 同步（插件未加载时无事可做，下次 resume 时 Rust 自己会取）。 */
    fun emitPush() {
      val p = instance ?: return
      val o = JSObject()
      o.put("type", "push")
      o.put("state", PushStore.state(p.activity))
      p.trigger("event", o)
    }
  }

  override fun load(webView: WebView) {
    instance = this
    this.webView = webView
    // portal iframe 与主站不同站点时也要带 Cookie
    CookieManager.getInstance().setAcceptThirdPartyCookies(webView, true)
    webView.setDownloadListener { url, userAgent, contentDisposition, mimeType, _ ->
      download(url, userAgent, contentDisposition, mimeType)
    }
    installInsets()
    applyChrome(initialDark(), initialColor())
    Notifier.ensureChannel(activity)
    handleIntent(activity.intent)
  }

  // ---- 生命周期 ----

  override fun onResume(activity: AppCompatActivity) {
    visible = true
    Notifier.cancelAll(activity)
    trigger("event", JSObject().apply { put("type", "resume") })
  }

  override fun onStop(activity: AppCompatActivity) {
    visible = false
    trigger("event", JSObject().apply { put("type", "stop") })
  }

  override fun onDestroy(activity: AppCompatActivity) {
    if (instance === this) instance = null
    visible = false
  }

  override fun onNewIntent(intent: Intent) {
    handleIntent(intent)
  }

  /** Rust 注册监听后补发冷启动时错过的分享与当前 inset。 */
  @Command
  override fun registerListener(invoke: Invoke) {
    super.registerListener(invoke)
    pendingShare?.let {
      pendingShare = null
      emitShare(it)
    }
    if (insets != ViewportInsets.ZERO) emitInsets(insets)
  }

  // ---- 命令（2.2 的表） ----

  @Command
  fun deviceInfo(invoke: Invoke) {
    val o = JSObject()
    o.put("name", "${Build.MANUFACTURER} ${Build.MODEL}".trim())
    o.put("sdkInt", Build.VERSION.SDK_INT)
    o.put("webviewVersion", WebViewCompat.getCurrentWebViewPackage(activity)?.versionName ?: "")
    o.put("debuggable", activity.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0)
    o.put("eventLogPath", EventLog.path(activity) ?: JSONObject.NULL)
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
    PushStore.setError(activity, null)
    activity.runOnUiThread {
      UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { ok ->
        PushStore.setEnabled(activity, true)
        val fcm = Fcm.bridge
        when {
          ok -> {
            try {
              UnifiedPush.register(activity, messageForDistributor = "Oryn", vapid = vapid)
            } catch (e: Exception) {
              PushStore.setError(activity, "vapid_invalid")
            }
            EventLog.log(activity, "push.enable", mapOf("channel" to "unifiedpush"))
            invoke.resolve(PushStore.state(activity))
          }
          fcm != null -> {
            EventLog.log(activity, "push.enable", mapOf("channel" to "fcm"))
            fcm.token(activity) { token, error ->
              PushStore.setFcmToken(activity, token)
              if (token == null) PushStore.setError(activity, error ?: "fcm_token")
              invoke.resolve(PushStore.state(activity))
            }
          }
          else -> {
            EventLog.log(activity, "push.enable", mapOf("channel" to null))
            invoke.resolve(PushStore.state(activity))
          }
        }
      }
    }
  }

  @Command
  fun pushDisable(invoke: Invoke) {
    try {
      if (UnifiedPush.getSavedDistributor(activity) != null) UnifiedPush.unregister(activity)
    } catch (_: Exception) {
    }
    Fcm.bridge?.delete(activity)
    PushStore.clear(activity)
    EventLog.log(activity, "push.disable")
    invoke.resolve(PushStore.state(activity))
  }

  @Command
  fun pushMarkSynced(invoke: Invoke) {
    val args = invoke.parseArgs(PushSyncedArgs::class.java)
    PushStore.markSynced(activity, args.endpoint, args.id)
    invoke.resolve()
  }

  @Command
  fun openNotificationSettings(invoke: Invoke) {
    val intent = Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
      .putExtra(Settings.EXTRA_APP_PACKAGE, activity.packageName)
      .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    activity.runOnUiThread { activity.startActivity(intent) }
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
    Thread { ShareIntake.ack(activity, args.id) }.start()
    invoke.resolve()
  }

  @Command
  fun toast(invoke: Invoke) {
    val args = invoke.parseArgs(TextArgs::class.java)
    activity.runOnUiThread { Toast.makeText(activity, args.text, Toast.LENGTH_SHORT).show() }
    invoke.resolve()
  }

  // ---- 分享 ----

  private fun handleIntent(intent: Intent?) {
    if (intent == null) return
    if (intent.action != Intent.ACTION_SEND && intent.action != Intent.ACTION_SEND_MULTIPLE) return
    // Activity 重建时 activity.intent 仍是同一个分享，打标记避免重复导入
    if (intent.getBooleanExtra(INTENT_CONSUMED, false)) return
    intent.putExtra(INTENT_CONSUMED, true)
    Thread {
      val share = try {
        ShareIntake.intake(activity, intent)
      } catch (_: Exception) {
        null
      }
      EventLog.log(
        activity,
        "share.intent",
        mapOf("files" to (share?.getJSONArray("files")?.length() ?: 0), "text" to (share?.isNull("text") == false)),
      )
      if (share != null) emitShare(share)
    }.start()
  }

  private fun emitShare(share: JSObject) {
    if (!hasListener("event")) {
      pendingShare = share
      return
    }
    val o = JSObject()
    o.put("type", "share")
    o.put("share", share)
    trigger("event", o)
  }

  // ---- inset、系统栏 ----

  /** 模板默认 edge-to-edge：给内容容器加「系统栏 + 刘海 + 输入法」的 padding 并消费 inset，WebView 始终落在安全区内。 */
  private fun installInsets() {
    val content = activity.findViewById<View>(android.R.id.content) ?: return
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
    o.put("type", "insets")
    trigger("event", o)
  }

  override fun onConfigurationChanged(newConfig: Configuration) {
    // 旋转、折叠展开时 inset 通常会自己重新分发；这里再请求一次兜底
    activity.findViewById<View>(android.R.id.content)?.let { ViewCompat.requestApplyInsets(it) }
  }

  private fun applyChrome(dark: Boolean, color: Int) {
    activity.runOnUiThread {
      val window = activity.window
      activity.findViewById<View>(android.R.id.content)?.setBackgroundColor(color)
      window.decorView.setBackgroundColor(color)
      val controller = WindowCompat.getInsetsController(window, window.decorView)
      controller.isAppearanceLightStatusBars = !dark
      controller.isAppearanceLightNavigationBars = !dark
    }
  }

  private fun initialDark(): Boolean =
    activity.resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK == Configuration.UI_MODE_NIGHT_YES

  private fun initialColor(): Int {
    val id = activity.resources.getIdentifier("oryn_bg", "color", activity.packageName)
    return if (id != 0) activity.getColor(id) else if (initialDark()) Color.BLACK else Color.WHITE
  }

  // ---- 下载（wry 在 Android 上没有下载钩子） ----

  private fun download(url: String, userAgent: String?, contentDisposition: String?, mimeType: String?) {
    val uri = Uri.parse(url)
    val scheme = uri.scheme?.lowercase()
    if (scheme != "http" && scheme != "https") {
      EventLog.log(activity, "download.skipped", mapOf("scheme" to scheme))
      return
    }
    // URLUtil 不认 RFC 6266 的 filename*（orynd 附件只发它），先自己解析
    val name = ShareIntake.sanitizeFileName(
      ContentDisposition.fileName(contentDisposition) ?: URLUtil.guessFileName(url, contentDisposition, mimeType),
    )
    try {
      val req = DownloadManager.Request(uri)
      CookieManager.getInstance().getCookie(url)?.let { req.addRequestHeader("Cookie", it) }
      if (!userAgent.isNullOrEmpty()) req.addRequestHeader("User-Agent", userAgent)
      if (!mimeType.isNullOrEmpty()) req.setMimeType(mimeType)
      req.setTitle(name)
      req.setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
      // 公共下载目录：minSdk 33，不需要任何存储权限
      req.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name)
      activity.getSystemService(DownloadManager::class.java).enqueue(req)
      EventLog.log(activity, "download.enqueued", mapOf("name" to name))
    } catch (e: Exception) {
      EventLog.log(activity, "download.failed", mapOf("name" to name, "error" to e.javaClass.simpleName))
    }
  }
}
