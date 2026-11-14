package com.lynricsy.oryn.mobile

import android.content.Context
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequest
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.TimeUnit

/**
 * 推送订阅的后台登记（代码审查第 6 条）：FCM 刷新令牌、UnifiedPush 换端点时进程里可能只有推送服务、没有 Activity，
 * 也就没有 Rust 那条登记路径。这时排一个 WorkManager 任务（要求联网、指数退避、进程被杀后系统照样拉起），
 * 用上次登记到的服务器与保险库里的设备令牌直接调 `POST /api/v1/push/subscriptions`。
 * 有 Activity 时仍交给 Rust（[OrynMobilePlugin.pushChanged]）。
 */
object PushSync {
  private const val WORK = "oryn-push-sync"

  /** 要登记的订阅：`key` 是登记成功后记作已同步的端点 / 令牌，`body` 是请求体（与 Rust `PushSubscription` 同形）。 */
  data class Request(val key: String, val body: JSONObject)

  /** 由 [PushStore.state] 得出要登记的订阅；未启用或材料不全时 null。纯函数。 */
  fun request(state: JSONObject): Request? {
    if (!state.optBoolean("enabled")) return null
    val p256dh = text(state, "p256dh") ?: return null
    val auth = text(state, "auth") ?: return null
    val keys = JSONObject().put("p256dh", p256dh).put("auth", auth)
    return when (text(state, "channel")) {
      "fcm" -> {
        val token = text(state, "fcmToken") ?: return null
        Request(token, JSONObject().put("kind", "fcm").put("token", token).put("keys", keys))
      }
      "unifiedpush" -> {
        val endpoint = text(state, "endpoint") ?: return null
        Request(endpoint, JSONObject().put("endpoint", endpoint).put("keys", keys))
      }
      else -> null
    }
  }

  /** 排一次后台登记（同名任务只保留最新一个）。 */
  fun schedule(context: Context) {
    val work = OneTimeWorkRequest.Builder(PushSyncWorker::class.java)
      .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
      .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 30, TimeUnit.SECONDS)
      .build()
    WorkManager.getInstance(context.applicationContext)
      .enqueueUniqueWork(WORK, ExistingWorkPolicy.REPLACE, work)
    EventLog.log(context, "push.sync_scheduled")
  }

  private fun text(o: JSONObject, key: String): String? =
    if (o.isNull(key)) null else o.optString(key).takeIf { it.isNotEmpty() }
}

class PushSyncWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
  private companion object {
    const val MAX_ATTEMPTS = 8
  }

  override fun doWork(): Result {
    val ctx = applicationContext
    val server = PushStore.server(ctx) ?: return done("no_server")
    val token = Vault.get(ctx, server) ?: return done("no_token")
    val req = PushSync.request(PushStore.state(ctx)) ?: return done("nothing")
    return try {
      val resp = OrynHttp.call(ctx, "POST", "$server/api/v1/push/subscriptions", token, req.body)
      when {
        resp.status in 200..299 -> {
          val id = JSONObject(resp.body).optString("id")
          PushStore.markSynced(ctx, req.key, id, server)
          EventLog.log(ctx, "push.synced", mapOf("id" to id, "via" to "worker"))
          Result.success()
        }
        // 设备被吊销 / 订阅被拒：重试也没用；页面下次打开时 Rust 会给出原因
        resp.status in 400..499 -> {
          val code = try {
            JSONObject(resp.body).optString("code")
          } catch (_: Exception) {
            ""
          }
          if (resp.status != 401) PushStore.setError(ctx, code.ifEmpty { "http_${resp.status}" })
          EventLog.log(ctx, "push.sync_error", mapOf("status" to resp.status, "code" to code, "via" to "worker"))
          Result.success()
        }
        else -> retry("http_${resp.status}")
      }
    } catch (e: IOException) {
      retry(e.javaClass.simpleName)
    }
  }

  private fun done(reason: String): Result {
    EventLog.log(applicationContext, "push.sync_skipped", mapOf("reason" to reason, "via" to "worker"))
    return Result.success()
  }

  private fun retry(reason: String): Result {
    EventLog.log(applicationContext, "push.sync_error", mapOf("error" to reason, "via" to "worker"))
    return if (runAttemptCount + 1 >= MAX_ATTEMPTS) Result.failure() else Result.retry()
  }
}
