// iOS 的推送状态（插件 JSON 契约 K5 的 PushState），存 UserDefaults（键前缀 `oryn.push.`）；接收端密钥在钥匙串
// （Shared/PushKeyStore.swift）。iOS 只有 APNs 一条通道：启用时 `channel = "apns"`，`fcmAvailable` 恒为 false，
// UnifiedPush / FCM 的字段恒空。`permission` 取自 UNUserNotificationCenter 的授权状态，所以 `state` 是异步的。
// 登记编排（网关的 info → challenges → grants、orynd 订阅）全在 Rust 壳里，这里只记原生侧的事实与 Rust 回写的同步记录。
import Foundation
import UserNotifications

/// `pushState` / `pushEnable` / `pushDisable` 的应答与 `push` 事件里的状态；nil 字段不输出（Rust 侧按缺省为空）。
struct PushStateOut: Encodable {
  let permission: String
  let enabled: Bool
  let channel: String?
  let distributor: String?
  let endpoint: String?
  let p256dh: String?
  let auth: String?
  let fcmToken: String?
  /// 小写十六进制。
  let apnsToken: String?
  let fcmAvailable: Bool
  let fcmProjectId: String?
  let syncedEndpoint: String?
  let subscriptionId: String?
  let error: String?
}

struct PushEvent: Encodable {
  let type = "push"
  let state: PushStateOut
}

struct PushChallengeEvent: Encodable {
  let type = "pushchallenge"
  let challengeId: String
  let nonce: String
}

enum PushStore {
  private static let defaults = UserDefaults.standard
  private static let kEnabled = "oryn.push.enabled"
  private static let kToken = "oryn.push.apnsToken"
  private static let kSynced = "oryn.push.syncedEndpoint"
  private static let kSubscription = "oryn.push.subscriptionId"
  private static let kServer = "oryn.push.server"
  private static let kError = "oryn.push.error"

  static var isEnabled: Bool { defaults.bool(forKey: kEnabled) }

  static func setEnabled(_ enabled: Bool) {
    defaults.set(enabled, forKey: kEnabled)
  }

  static var token: String? { defaults.string(forKey: kToken) }

  static func setToken(_ hex: String) {
    defaults.set(hex, forKey: kToken)
  }

  static func setError(_ code: String?) {
    defaults.set(code, forKey: kError)
  }

  /// Rust 登记成功后回写：端点、订阅 id 与服务器，并清掉错误。
  static func markSynced(endpoint: String, subscriptionId: String, server: String) {
    defaults.set(endpoint, forKey: kSynced)
    defaults.set(subscriptionId, forKey: kSubscription)
    defaults.set(server, forKey: kServer)
    defaults.removeObject(forKey: kError)
  }

  /// 关闭推送：清掉全部状态（密钥由调用方另删）。
  static func clear() {
    for key in [kEnabled, kToken, kSynced, kSubscription, kServer, kError] {
      defaults.removeObject(forKey: key)
    }
  }

  /// 系统通知权限：已授权（含临时授权）= granted，还没问过 = prompt，其余 denied。
  static func permission(_ completion: @escaping (String) -> Void) {
    UNUserNotificationCenter.current().getNotificationSettings { settings in
      switch settings.authorizationStatus {
      case .authorized, .provisional, .ephemeral:
        completion("granted")
      case .notDetermined:
        completion("prompt")
      case .denied:
        completion("denied")
      @unknown default:
        completion("denied")
      }
    }
  }

  /// 当前状态。启用时带上接收端公钥与 auth：钥匙串里没有（如从备份恢复到新机，UserDefaults 回来了而仅本机的钥匙串项没有）
  /// 就在这里补生成，并清掉「已登记」记录，下次同步带着新公钥重新登记。
  static func state(_ completion: @escaping (PushStateOut) -> Void) {
    permission { permission in
      let enabled = isEnabled
      var keys: PushKeys?
      if enabled {
        if let ensured = PushKeyStore.ensure() {
          keys = ensured.keys
          if ensured.fresh {
            defaults.removeObject(forKey: kSynced)
            defaults.removeObject(forKey: kSubscription)
          }
        } else {
          setError("no_keys")
        }
      }
      completion(
        PushStateOut(
          permission: permission,
          enabled: enabled,
          channel: enabled ? "apns" : nil,
          distributor: nil,
          endpoint: nil,
          p256dh: keys?.p256dh,
          auth: keys?.authString,
          fcmToken: nil,
          apnsToken: enabled ? token : nil,
          fcmAvailable: false,
          fcmProjectId: nil,
          syncedEndpoint: defaults.string(forKey: kSynced),
          subscriptionId: defaults.string(forKey: kSubscription),
          error: defaults.string(forKey: kError)))
    }
  }
}
