package com.lynricsy.oryn.mobile

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import androidx.core.app.NotificationManagerCompat
import app.tauri.plugin.JSObject
import org.json.JSONObject
import org.unifiedpush.android.connector.UnifiedPush

/**
 * 推送状态，存 `SharedPreferences("oryn_push")`。UnifiedPush 的端点与密钥由 [OrynPushService] 写入，
 * FCM 令牌由应用 gms flavor 的 `OrynFcmService` 写入；同步到服务端后 Rust 调 `pushMarkSynced`
 * （FCM 时 `syncedEndpoint` 存的就是令牌）。
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
  private const val K_ERROR = "error"
  private const val K_FCM_TOKEN = "fcmToken"

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

  fun markSynced(context: Context, endpoint: String, subscriptionId: String) {
    prefs(context).edit()
      .putString(K_SYNCED, endpoint)
      .putString(K_SUBSCRIPTION, subscriptionId)
      .remove(K_ERROR)
      .apply()
  }

  /** 关闭推送：清掉除「问过权限」外的全部状态。 */
  fun clear(context: Context) {
    val asked = prefs(context).getBoolean(K_ASKED, false)
    prefs(context).edit().clear().putBoolean(K_ASKED, asked).apply()
  }

  /** 2.2 的 PushState（另带 `channel`、`fcmToken`）。 */
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
    val o = JSObject()
    o.put("permission", permissionFor(notificationsAllowed(context), p.getBoolean(K_ASKED, false)))
    o.put("enabled", enabled)
    o.put("channel", channel ?: JSONObject.NULL)
    o.put("distributor", distributor ?: JSONObject.NULL)
    o.put("endpoint", (if (up) p.getString(K_ENDPOINT, null) else null) ?: JSONObject.NULL)
    o.put("p256dh", (if (up) p.getString(K_P256DH, null) else null) ?: JSONObject.NULL)
    o.put("auth", (if (up) p.getString(K_AUTH, null) else null) ?: JSONObject.NULL)
    o.put("fcmToken", (if (channel == "fcm") p.getString(K_FCM_TOKEN, null) else null) ?: JSONObject.NULL)
    o.put("syncedEndpoint", p.getString(K_SYNCED, null) ?: JSONObject.NULL)
    o.put("subscriptionId", p.getString(K_SUBSCRIPTION, null) ?: JSONObject.NULL)
    o.put("error", p.getString(K_ERROR, null) ?: JSONObject.NULL)
    return o
  }

  private fun prefs(context: Context) =
    context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
}
