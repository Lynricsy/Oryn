package com.lynricsy.oryn.mobile

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import androidx.core.app.NotificationManagerCompat
import app.tauri.plugin.JSObject
import org.json.JSONArray
import org.json.JSONObject
import org.unifiedpush.android.connector.UnifiedPush

/**
 * 推送状态，存 `SharedPreferences("oryn_push")`。UnifiedPush 的端点与密钥由 [OrynPushService] 写入，
 * FCM 令牌由应用 gms flavor 的 `OrynFcmService` 写入、FCM 的接收端密钥见 [FcmKeys]。登记到服务端后由 Rust
 * 调 `pushMarkSynced`（网关通道时 `syncedEndpoint` 是 `<网关>/v1/grants/<grant>`），或由 [PushSyncWorker]
 * （只有推送服务存活、没有 Activity 时，只管 UnifiedPush）记下，同时记下服务器地址：后台任务用它和保险库里的设备令牌重新登记。
 */
object PushStore {
  private const val PREFS = "oryn_push"
  private const val K_ENABLED = "enabled"
  private const val K_ASKED = "asked"
  private const val K_ENDPOINT = "endpoint"
  private const val K_P256DH = "p256dh"
  private const val K_AUTH = "auth"
  private const val K_SYNCED = "syncedEndpoint"
  private const val K_SUBSCRIPTION = "subscriptionId"
  private const val K_SERVER = "server"
  private const val K_ERROR = "error"
  private const val K_FCM_TOKEN = "fcmToken"
  private const val K_SHOWN = "shownIds"
  private const val K_PREFER_UP = "preferUp"

  /** 记住最近显示过的通知 id 个数（FCM 补拉时去重）。 */
  const val SHOWN_KEEP = 200

  /**
   * 推送通道：未启用为 null；带 FCM（gms 包）且用户没有手动切到 UnifiedPush → `fcm`（经官方推送网关）；
   * 有 UnifiedPush 分发器 → `unifiedpush`；否则 null。
   * 升级上来、此前用 UnifiedPush 的 gms 用户没有 `preferUp`，同样按 `fcm`（用户决定 D5）：Rust 下次同步时删掉旧的 Web Push 订阅。
   */
  fun channelFor(enabled: Boolean, distributor: String?, hasFcm: Boolean, preferUp: Boolean): String? = when {
    !enabled -> null
    hasFcm && !preferUp -> "fcm"
    distributor != null -> "unifiedpush"
    else -> null
  }

  /** minSdk 33：系统通知开着 = granted；还没问过 = prompt（可以弹授权框）；其余 denied。 */
  fun permissionFor(notificationsEnabled: Boolean, asked: Boolean): String = when {
    notificationsEnabled -> "granted"
    !asked -> "prompt"
    else -> "denied"
  }

  /** 最近显示过的 id 表追加一个（去重，超出 [SHOWN_KEEP] 丢最旧的）。 */
  fun appendShown(ids: List<String>, id: String): List<String> =
    (ids.filter { it != id } + id).takeLast(SHOWN_KEEP)

  fun notificationsAllowed(context: Context): Boolean =
    context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED &&
      NotificationManagerCompat.from(context).areNotificationsEnabled()

  fun isEnabled(context: Context): Boolean = prefs(context).getBoolean(K_ENABLED, false)

  fun setEnabled(context: Context, enabled: Boolean) {
    prefs(context).edit().putBoolean(K_ENABLED, enabled).apply()
  }

  fun markAsked(context: Context) {
    prefs(context).edit().putBoolean(K_ASKED, true).apply()
  }

  fun setEndpoint(context: Context, url: String, p256dh: String?, auth: String?) {
    prefs(context).edit()
      .putString(K_ENDPOINT, url)
      .putString(K_P256DH, p256dh)
      .putString(K_AUTH, auth)
      .putString(K_ERROR, if (p256dh == null || auth == null) "no_keys" else null)
      .apply()
  }

  /**
   * 端点失效（取消注册）：清端点；当前走 UnifiedPush 时一并清同步记录。走 FCM 时（切到网关、或 D5 迁移后分发器迟到的
   * 取消注册回调）同步记录属于网关订阅、或是 Rust 还要据此删掉的旧订阅，不能动。保留启用开关。
   */
  fun clearEndpoint(context: Context) {
    val p = prefs(context)
    val e = p.edit().remove(K_ENDPOINT).remove(K_P256DH).remove(K_AUTH)
    if (Fcm.bridge == null || p.getBoolean(K_PREFER_UP, false)) e.remove(K_SYNCED).remove(K_SUBSCRIPTION)
    e.apply()
  }

  fun setFcmToken(context: Context, token: String?) {
    prefs(context).edit().putString(K_FCM_TOKEN, token).apply()
  }

  fun fcmToken(context: Context): String? = prefs(context).getString(K_FCM_TOKEN, null)

  /** 用户手动切到了 UnifiedPush（gms 包）。 */
  fun preferUp(context: Context): Boolean = prefs(context).getBoolean(K_PREFER_UP, false)

  /** 用户在 gms 包里手动切到 UnifiedPush（`pushEnable` 的 `transport: "unifiedpush"`）为真；缺省走 FCM。 */
  fun setPreferUp(context: Context, preferUp: Boolean) {
    prefs(context).edit().putBoolean(K_PREFER_UP, preferUp).apply()
  }

  fun setError(context: Context, error: String?) {
    prefs(context).edit().putString(K_ERROR, error).apply()
  }

  fun markSynced(context: Context, endpoint: String, subscriptionId: String, server: String) {
    prefs(context).edit()
      .putString(K_SYNCED, endpoint)
      .putString(K_SUBSCRIPTION, subscriptionId)
      .putString(K_SERVER, server)
      .remove(K_ERROR)
      .apply()
  }

  /** 上次登记到的服务器（后台重新登记、补拉通知用）；从未登记过为 null。 */
  fun server(context: Context): String? = prefs(context).getString(K_SERVER, null)

  /** 这条通知显示过了吗（推送与补拉都会显示，避免重复）。 */
  fun wasShown(context: Context, id: String): Boolean = id.isNotEmpty() && shown(context).contains(id)

  @Synchronized
  fun markShown(context: Context, id: String) {
    if (id.isEmpty()) return
    val next = appendShown(shown(context), id)
    prefs(context).edit().putString(K_SHOWN, JSONArray(next).toString()).apply()
  }

  private fun shown(context: Context): List<String> {
    val raw = prefs(context).getString(K_SHOWN, null) ?: return emptyList()
    return try {
      val a = JSONArray(raw)
      List(a.length()) { a.getString(it) }
    } catch (_: Exception) {
      emptyList()
    }
  }

  /** 关闭推送：清掉除「问过权限」外的全部状态。 */
  fun clear(context: Context) {
    val asked = prefs(context).getBoolean(K_ASKED, false)
    prefs(context).edit().clear().putBoolean(K_ASKED, asked).apply()
  }

  /**
   * K5 的 PushState。FCM 通道的 `p256dh` / `auth` 是 [FcmKeys] 的接收端密钥：首次走 FCM（新启用、或 D5 从 UnifiedPush
   * 迁来）时在这里生成，并清掉「已同步端点」让 Rust 带着新密钥重新登记；订阅 id 留着，Rust 据此先删掉服务器上的旧订阅。
   * `apnsToken` 只有 iOS 有；`fcmAvailable` / `fcmProjectId` 告诉 Rust 本包能否走网关 FCM。
   */
  fun state(context: Context): JSObject {
    val p = prefs(context)
    val enabled = p.getBoolean(K_ENABLED, false)
    val distributor = try {
      UnifiedPush.getSavedDistributor(context)
    } catch (_: Exception) {
      null
    }
    val fcm = Fcm.bridge
    val channel = channelFor(enabled, distributor, fcm != null, p.getBoolean(K_PREFER_UP, false))
    val up = channel == "unifiedpush"
    val fcmKeys = if (channel == "fcm") {
      val (keys, fresh) = FcmKeys.ensure(context)
      if (fresh) p.edit().remove(K_SYNCED).commit()
      keys
    } else {
      null
    }
    val o = JSObject()
    o.put("permission", permissionFor(notificationsAllowed(context), p.getBoolean(K_ASKED, false)))
    o.put("enabled", enabled)
    o.put("channel", channel ?: JSONObject.NULL)
    o.put("distributor", distributor ?: JSONObject.NULL)
    o.put("endpoint", (if (up) p.getString(K_ENDPOINT, null) else null) ?: JSONObject.NULL)
    o.put("p256dh", (if (up) p.getString(K_P256DH, null) else fcmKeys?.p256dh) ?: JSONObject.NULL)
    o.put("auth", (if (up) p.getString(K_AUTH, null) else fcmKeys?.auth) ?: JSONObject.NULL)
    o.put("fcmToken", (if (channel == "fcm") p.getString(K_FCM_TOKEN, null) else null) ?: JSONObject.NULL)
    o.put("apnsToken", JSONObject.NULL)
    o.put("fcmAvailable", fcm != null)
    o.put("fcmProjectId", fcm?.projectId(context) ?: JSONObject.NULL)
    o.put("syncedEndpoint", p.getString(K_SYNCED, null) ?: JSONObject.NULL)
    o.put("subscriptionId", p.getString(K_SUBSCRIPTION, null) ?: JSONObject.NULL)
    o.put("error", p.getString(K_ERROR, null) ?: JSONObject.NULL)
    return o
  }

  private fun prefs(context: Context) =
    context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
}
