package com.lynricsy.oryn.mobile

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import java.util.Locale

/**
 * 系统通知。前台（壳的 `notify`）与后台推送（UnifiedPush / FCM，进程可能刚被拉起、插件未加载）
 * 共用这里；点击 → `oryn://…` 深链接 → deep-link 插件 → Rust `open_path`。
 */
object Notifier {
  private const val CHANNEL = "threads"

  /** 渠道名跟随 `lang`（壳传入的界面语言），缺省按系统语言。 */
  fun channelName(lang: String?): String {
    val l = (lang?.takeIf { it.isNotBlank() } ?: Locale.getDefault().toLanguageTag()).lowercase(Locale.ROOT)
    return if (l.startsWith("zh")) "线程通知" else "Thread updates"
  }

  fun ensureChannel(context: Context, lang: String? = null) {
    val nm = context.getSystemService(NotificationManager::class.java) ?: return
    // 同 id 重复创建只会更新名称，重要性不变
    nm.createNotificationChannel(NotificationChannel(CHANNEL, channelName(lang), NotificationManager.IMPORTANCE_HIGH))
  }

  /** 发出通知；未授权时返回 `"permission"`，成功返回 null。 */
  fun post(context: Context, model: PushPayload.Model, lang: String? = null): String? {
    ensureChannel(context, lang)
    if (!PushStore.notificationsAllowed(context)) {
      EventLog.log(context, "notify.blocked", mapOf("id" to model.id))
      return "permission"
    }
    val intent = Intent(Intent.ACTION_VIEW, Uri.parse(PushPayload.deepLink(model.path)))
      .setPackage(context.packageName)
      .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
    val pending = PendingIntent.getActivity(
      context,
      model.tag.hashCode(),
      intent,
      PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )
    val notification = NotificationCompat.Builder(context, CHANNEL)
      .setSmallIcon(R.drawable.ic_stat_oryn)
      .setContentTitle(model.title)
      .setContentText(model.body)
      .setStyle(NotificationCompat.BigTextStyle().bigText(model.body))
      .setAutoCancel(true)
      .setOnlyAlertOnce(true)
      .setCategory(NotificationCompat.CATEGORY_MESSAGE)
      .setContentIntent(pending)
      .build()
    try {
      NotificationManagerCompat.from(context).notify(model.tag, 1, notification)
    } catch (_: SecurityException) {
      EventLog.log(context, "notify.blocked", mapOf("id" to model.id))
      return "permission"
    }
    EventLog.log(context, "notify.posted", mapOf("id" to model.id, "tag" to model.tag, "path" to model.path))
    return null
  }

  fun cancelAll(context: Context) {
    NotificationManagerCompat.from(context).cancelAll()
  }
}
