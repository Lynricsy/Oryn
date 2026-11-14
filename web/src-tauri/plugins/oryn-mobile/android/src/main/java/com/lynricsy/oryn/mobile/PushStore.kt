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
 * 调 `pushMarkSynced`，或由 [PushSyncWorker]（只有推送服务存活、没有 Activity 时）记下（FCM 时 `syncedEndpoint` 存令牌），
 * 同时记下服务器地址：后台任务用它和保险库里的设备令牌重新登记。
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

  /** 记住最近显示过的通知 id 个数（FCM 补拉时去重）。 */
  const val SHOWN_KEEP = 200

  /** 推送通道：未启用为 null；有 UnifiedPush 分发器优先；否则有 FCM（gms 包）走 FCM。 */
  fun channelFor(enabled: Boolean, distributor: String?, hasFcm: Boolean): String? = when {
    !enabled -> null
    distributor != null -> "unifiedpush"
    hasFcm -> "fcm"
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

  /** 端点失效（取消注册）：清端点与同步记录，保留启用开关。 */
  fun clearEndpoint(context: Context) {
    prefs(context).edit()
      .remove(K_ENDPOINT).remove(K_P256DH).remove(K_AUTH)
      .remove(K_SYNCED).remove(K_SUBSCRIPTION)
      .apply()
  }

  fun setFcmToken(context: Context, token: String?) {
    prefs(context).edit().putString(K_FCM_TOKEN, token).apply()
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
   * 2.2 的 PushState（另带 `channel`、`fcmToken`）。FCM 通道的 `p256dh` / `auth` 是 [FcmKeys] 的接收端密钥：
   * 升级自没有密钥的版本时在这里补生成，并清掉「已登记」记录，下次同步带着密钥重新登记。
   */
  fun state(context: Context): JSObject {
    val p = prefs(context)
    val enabled = p.getBoolean(K_ENABLED, false)
    val distributor = try {
      UnifiedPush.getSavedDistributor(context)
    } catch (_: Exception) {
      null
    }
    val channel = channelFor(enabled, distributor, Fcm.bridge != null)
    val up = channel == "unifiedpush"
    val fcmKeys = if (channel == "fcm") {
      val (keys, fresh) = FcmKeys.ensure(context)
      if (fresh) p.edit().remove(K_SYNCED).remove(K_SUBSCRIPTION).commit()
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
    o.put("syncedEndpoint", p.getString(K_SYNCED, null) ?: JSONObject.NULL)
    o.put("subscriptionId", p.getString(K_SUBSCRIPTION, null) ?: JSONObject.NULL)
    o.put("error", p.getString(K_ERROR, null) ?: JSONObject.NULL)
    return o
  }

  private fun prefs(context: Context) =
    context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
}
