package com.lynricsy.oryn.mobile

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.Person
import androidx.core.graphics.drawable.IconCompat
import java.util.Locale

/**
 * 系统通知。前台（壳的 `notify`）与后台推送（UnifiedPush / FCM，进程可能刚被拉起、插件未加载）
 * 共用这里；点击 → `oryn://…` 深链接 → deep-link 插件 → Rust `open_path`。
 * 可一触作答的 input 另带作答按钮（[AnswerReceiver]），作答后按 [State] 重绘同一条通知；撤回见 [withdraw]。
 */
object Notifier {
  private const val CHANNEL = "threads"
  /** 每条通知按 (tag, 1) 定位：同一线程的通知共用 tag，后到的替换先到的。 */
  private const val NOTIFY_ID = 1
  /** 通知 extras 里记下的通知 id：撤回时只撤同一 id 的那条。 */
  private const val EXTRA_ID = "com.lynricsy.oryn.mobile.NOTIFICATION_ID"
  /** 「已作答」短暂显示后自动消失。 */
  private const val ANSWERED_TIMEOUT_MS = 4_000L

  const val ACTION_ANSWER = "com.lynricsy.oryn.mobile.ANSWER"
  const val EXTRA_PAYLOAD = "com.lynricsy.oryn.mobile.PAYLOAD"
  const val EXTRA_OPTION = "com.lynricsy.oryn.mobile.OPTION"

  /**
   * 点了作答按钮之后通知的状态：提交中（去掉按钮防连点）、已作答（4 秒后消失）、已在别处作答、
   * 可重试的失败（保留原文与按钮）、不可重试的失败（保留原文、去掉按钮）。各状态点通知本身都仍打开线程。
   */
  enum class State { SENDING, ANSWERED, ELSEWHERE, RETRY, FAILED }

  /** 渠道名跟随 `lang`（壳传入的界面语言），缺省按系统语言。线程与系统通知（预算、定时）共用这一个渠道。 */
  fun channelName(lang: String?): String = if (isZh(lang)) "通知" else "Notifications"

  /**
   * 锁屏公开版的正文：不含线程标题与 agent 输出（安全审查第 5 条）。`thread` 为假（无线程的系统通知）时用
   * [genericText] 的通用文案。
   */
  fun publicText(lang: String?, thread: Boolean = true): String = when {
    !thread -> genericText(lang)
    isZh(lang) -> "线程有更新"
    else -> "A thread has an update"
  }

  /** FCM 补拉失败时的通用通知正文。 */
  fun genericText(lang: String?): String = if (isZh(lang)) "Oryn 有新通知" else "You have new Oryn notifications"

  private fun isZh(lang: String?): Boolean =
    (lang?.takeIf { it.isNotBlank() } ?: Locale.getDefault().toLanguageTag()).lowercase(Locale.ROOT).startsWith("zh")

  /** 作答状态的说明文字。 */
  fun stateText(state: State, lang: String?): String {
    val zh = isZh(lang)
    return when (state) {
      State.SENDING -> if (zh) "正在提交…" else "Sending…"
      State.ANSWERED -> if (zh) "已作答" else "Answered"
      State.ELSEWHERE -> if (zh) "已在别处作答" else "Already answered elsewhere"
      State.RETRY, State.FAILED -> if (zh) "作答失败，点按打开 Oryn" else "Couldn't answer. Tap to open Oryn."
    }
  }

  /** 按钮文字：confirm 的 `yes` / `no` 按语言显示「是 / 否」，其余用服务端给的选项原文。 */
  fun actionTitle(action: PushPayload.Action, lang: String?): String = when (action.role) {
    "yes" -> if (isZh(lang)) "是" else "Yes"
    "no" -> if (isZh(lang)) "否" else "No"
    else -> action.label
  }

  /** 某状态下的正文：已作答 / 已在别处作答只显示状态；提交中与失败在原文后加一行状态。 */
  fun stateBody(body: String, state: State?, lang: String?): String = when (state) {
    null -> body
    State.ANSWERED, State.ELSEWHERE -> stateText(state, lang)
    else -> listOf(body, stateText(state, lang)).filter { it.isNotEmpty() }.joinToString("\n")
  }

  /** 正文为空时按通知种类回落到简短提示，保证 MessagingStyle 消息行不为空白。 */
  fun fallbackBody(kind: String, title: String, lang: String?): String {
    val zh = isZh(lang)
    return when (kind) {
      "done" -> if (zh) "已完成" else "Done"
      "input" -> if (zh) "等待你的回答" else "Waiting for your input"
      "error" -> if (zh) "任务出错" else "Task failed"
      "budget" -> if (zh) "预算告警" else "Budget alert"
      else -> title
    }
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
    if (!show(context, model, lang, null)) return "permission"
    EventLog.log(context, "notify.posted", mapOf("id" to model.id, "tag" to model.tag, "path" to model.path))
    return null
  }

  /**
   * 作答后按状态重绘同一条通知（同 tag 替换，不再响铃）。只在 tag 上仍显示着同一 id 的通知时重绘：
   * 作答请求在途期间同线程的新通知（id 不同）已替换它，或撤回已把它撤掉时，迟到的结果直接丢弃，
   * 不覆盖新通知、也不复活已撤回的按钮。返回是否重绘了。
   */
  fun update(context: Context, model: PushPayload.Model, state: State, lang: String? = null): Boolean {
    if (!PushStore.notificationsAllowed(context)) return false
    if (shownId(context, model.tag) != model.id) {
      EventLog.log(context, "notify.stale_answer", mapOf("id" to model.id, "state" to state.name))
      return false
    }
    return show(context, model, lang, state)
  }

  /** `tag` 上当前显示着的通知的 id；没有显示或不是我们发的为 null。 */
  private fun shownId(context: Context, tag: String): String? {
    val nm = context.getSystemService(NotificationManager::class.java) ?: return null
    return nm.activeNotifications
      .firstOrNull { it.tag == tag && it.id == NOTIFY_ID }
      ?.notification?.extras?.getString(EXTRA_ID)
  }

  /**
   * 撤回：`tag` 上仍显示着同一 id 的通知才撤掉（同一线程后来的通知 id 不同，不受影响）。返回是否撤掉了。
   */
  fun withdraw(context: Context, w: PushPayload.Withdrawal): Boolean {
    val nm = context.getSystemService(NotificationManager::class.java) ?: return false
    val hit = nm.activeNotifications.any {
      it.tag == w.tag && it.id == NOTIFY_ID && it.notification.extras.getString(EXTRA_ID) == w.id
    }
    if (hit) nm.cancel(w.tag, NOTIFY_ID)
    return hit
  }

  private fun show(context: Context, model: PushPayload.Model, lang: String?, state: State?): Boolean {
    val intent = Intent(Intent.ACTION_VIEW, Uri.parse(PushPayload.deepLink(model.path)))
      .setPackage(context.packageName)
      .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
    val pending = PendingIntent.getActivity(
      context,
      model.tag.hashCode(),
      intent,
      PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )
    // 锁屏只显示公开版（「Oryn · 线程有更新」或「Oryn · Oryn 有新通知」），解锁后才看到标题与正文；
    // 公开版不带按钮
    val public = NotificationCompat.Builder(context, CHANNEL)
      .setSmallIcon(R.drawable.ic_stat_oryn)
      .setContentTitle("Oryn")
      .setContentText(publicText(lang, thread = model.path.startsWith("/t/")))
      .build()
    val avatar = PushPayload.effectiveAvatar(model)
    val senderName = PushPayload.displayTitle(model.title, avatar)
    val rawBody = stateBody(model.body, state, lang)
    val body = rawBody.ifEmpty { fallbackBody(model.kind, senderName, lang) }
    val avatarBitmap = NotificationAvatarRenderer.render(context, avatar)
    val selfUser = Person.Builder()
      .setKey("oryn-self")
      .setName(if (isZh(lang)) "我" else "Me")
      .build()
    val senderKey = if (avatar.kind == PushPayload.AVATAR_ASSISTANT && !model.path.startsWith("/t/")) {
      "oryn-assistant"
    } else {
      model.tag
    }
    val sender = Person.Builder()
      .setKey(senderKey)
      .setName(senderName)
      .setIcon(IconCompat.createWithBitmap(avatarBitmap))
      .build()
    val ts = model.timestamp.takeIf { it > 0L } ?: System.currentTimeMillis()
    val style = NotificationCompat.MessagingStyle(selfUser)
      .setGroupConversation(false)
      .addMessage(body, ts, sender)
    val builder = NotificationCompat.Builder(context, CHANNEL)
      .setSmallIcon(R.drawable.ic_stat_oryn)
      .setContentTitle(senderName)
      .setContentText(body)
      .setStyle(style)
      .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
      .setPublicVersion(public)
      .setAutoCancel(true)
      .setOnlyAlertOnce(true)
      .setSilent(state != null)
      .setCategory(NotificationCompat.CATEGORY_MESSAGE)
      .setContentIntent(pending)
      .addExtras(Bundle().apply { putString(EXTRA_ID, model.id) })
    if (state == State.ANSWERED) builder.setTimeoutAfter(ANSWERED_TIMEOUT_MS)
    if (state == null || state == State.RETRY) {
      for (a in model.actions) builder.addAction(answerAction(context, model, a, lang))
    }
    return try {
      NotificationManagerCompat.from(context).notify(model.tag, NOTIFY_ID, builder.build())
      true
    } catch (_: SecurityException) {
      EventLog.log(context, "notify.blocked", mapOf("id" to model.id))
      false
    }
  }

  /**
   * 作答按钮：显式广播给 [AnswerReceiver]，不拉起界面。PendingIntent 不可变；data 带 (通知 id, 选项) 使每个按钮的
   * PendingIntent 互不相同（extras 不参与 PendingIntent 的区分），请求码同样按二者求得。
   * 要求解锁（API 31+ 的 `setAuthenticationRequired`）：锁屏公开版看不到问题内容，且批准工具命令等于让持机者
   * 在设备上执行操作，不能不解锁就在锁屏上点掉。
   */
  private fun answerAction(context: Context, model: PushPayload.Model, a: PushPayload.Action, lang: String?): NotificationCompat.Action {
    val intent = Intent(context, AnswerReceiver::class.java)
      .setAction(ACTION_ANSWER)
      .setData(Uri.parse("oryn-answer://${Uri.encode(model.id)}/${a.index}"))
      .putExtra(EXTRA_PAYLOAD, PushPayload.encode(model))
      .putExtra(EXTRA_OPTION, a.index)
    val pi = PendingIntent.getBroadcast(
      context,
      "${model.id}#${a.index}".hashCode(),
      intent,
      PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )
    return NotificationCompat.Action.Builder(null as IconCompat?, actionTitle(a, lang), pi)
      .setAuthenticationRequired(true)
      .setShowsUserInterface(false)
      .setAllowGeneratedReplies(false)
      .build()
  }

  /** 不含任何内容的「Oryn 有新通知」，点击回首页。 */
  fun postGeneric(context: Context, lang: String? = null): String? =
    post(context, PushPayload.Model(id = "", title = "Oryn", body = genericText(lang), path = "/", tag = "oryn-new"), lang)

  fun cancelAll(context: Context) {
    NotificationManagerCompat.from(context).cancelAll()
  }
}
