// iOS 系统通知的代理与本地通知投递（Communication Notifications / INSendMessageIntent）：
// 通知内容（tag、标题、发送者头像、通信通知）由 Shared/PushContent.swift 构造，与 Notification Service Extension 共用。
// 前台展示规则：官方推送网关的 challenge（`kind: gateway_challenge`）不展示，转给插件发 `pushchallenge` 事件；
// 其余远程推送也不展示（前台提示由页面的 host_notify 负责，否则会出两条，与 Android 一致）；本地通知照常展示。
import Foundation
import UIKit
import UserNotifications

struct NotifyArgs: Decodable {
  let id: String
  let title: String
  let body: String
  let path: String
  let lang: String
  let timestamp: Int64?
  let avatar: NotifyAvatarArgs?
}

struct NotifyOut: Encodable {
  let posted: Bool
  let reason: String?
}

final class NotificationManager: NSObject, UNUserNotificationCenterDelegate {
  static let shared = NotificationManager()

  private let lock = NSLock()
  private var onTap: ((String, String) -> Void)?
  private var pendingTaps: [(String, String)] = []
  private var onChallenge: ((String, String) -> Void)?

  private override init() {
    super.init()
  }

  /// 安装为系统通知中心代理（应在启动早期调用，保证冷启动点击通知不会丢失）。
  func installDelegate() {
    let center = UNUserNotificationCenter.current()
    if center.delegate == nil {
      center.delegate = self
    }
  }

  /// 绑定点击回调并补发冷启动期间暂存的通知点击。
  func attach(onTap: @escaping (String, String) -> Void) {
    let flushed: [(String, String)]
    lock.lock()
    self.onTap = onTap
    flushed = pendingTaps
    pendingTaps.removeAll()
    lock.unlock()
    for (id, path) in flushed {
      onTap(id, path)
    }
  }

  /// 绑定网关 challenge 回调（challengeId, nonce）。challenge 只在 Rust 发起登记之后到达，那时插件早已绑定，不暂存。
  func attachChallenges(_ onChallenge: @escaping (String, String) -> Void) {
    lock.lock()
    self.onChallenge = onChallenge
    lock.unlock()
  }

  /// 投递一条本地通知：未决定权限时先向用户请求授权，拒绝时返回 `reason = "permission"`。
  func post(_ args: NotifyArgs, completion: @escaping (NotifyOut) -> Void) {
    installDelegate()
    let center = UNUserNotificationCenter.current()
    center.getNotificationSettings { settings in
      switch settings.authorizationStatus {
      case .authorized, .provisional, .ephemeral:
        self.enqueue(center: center, args: args, completion: completion)
      case .notDetermined:
        center.requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
          if granted {
            self.enqueue(center: center, args: args, completion: completion)
          } else {
            completion(NotifyOut(posted: false, reason: "permission"))
          }
        }
      case .denied:
        completion(NotifyOut(posted: false, reason: "permission"))
      @unknown default:
        completion(NotifyOut(posted: false, reason: "permission"))
      }
    }
  }

  /// 打开系统设置中的本应用通知设置页。
  func openSettings() {
    DispatchQueue.main.async {
      let urlString: String
      if #available(iOS 16.0, *) {
        urlString = UIApplication.openNotificationSettingsURLString
      } else {
        urlString = UIApplication.openSettingsURLString
      }
      guard let url = URL(string: urlString) else { return }
      UIApplication.shared.open(url, options: [:], completionHandler: nil)
    }
  }

  // MARK: - UNUserNotificationCenterDelegate

  func userNotificationCenter(
    _ center: UNUserNotificationCenter,
    willPresent notification: UNNotification,
    withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
  ) {
    if case let (id, nonce)? = Self.challenge(notification.request.content.userInfo) {
      PushLog.info("apns.challenge foreground=true listener=\(deliverChallenge(id, nonce))")
      completionHandler([])
      return
    }
    if notification.request.trigger is UNPushNotificationTrigger {
      completionHandler([])
      return
    }
    if #available(iOS 14.0, *) {
      completionHandler([.banner, .list, .sound])
    } else {
      completionHandler([.alert, .sound])
    }
  }

  func userNotificationCenter(
    _ center: UNUserNotificationCenter,
    didReceive response: UNNotificationResponse,
    withCompletionHandler completionHandler: @escaping () -> Void
  ) {
    defer { completionHandler() }
    let info = response.notification.request.content.userInfo
    // App 在后台时 challenge 以「正在验证推送通知」展示；用户点开它时同样交给登记流程（5 分钟内仍有效），不当作通知点击
    if case let (id, nonce)? = Self.challenge(info) {
      PushLog.info("apns.challenge foreground=false listener=\(deliverChallenge(id, nonce))")
      return
    }
    guard response.actionIdentifier == UNNotificationDefaultActionIdentifier else { return }
    let id = (info["id"] as? String) ?? ""
    let path = (info["path"] as? String) ?? "/"
    let handler: ((String, String) -> Void)?
    lock.lock()
    if let callback = onTap {
      handler = callback
    } else {
      pendingTaps.append((id, path))
      handler = nil
    }
    lock.unlock()
    handler?(id, path)
  }

  // MARK: - 网关 challenge

  /// `{kind: "gateway_challenge", challengeId, nonce}`；其它通知为 nil。
  private static func challenge(_ info: [AnyHashable: Any]) -> (String, String)? {
    guard info["kind"] as? String == "gateway_challenge",
      let id = info["challengeId"] as? String, !id.isEmpty,
      let nonce = info["nonce"] as? String, !nonce.isEmpty
    else { return nil }
    return (id, nonce)
  }

  /// 交给插件；还没绑定时丢弃（返回 false）。
  private func deliverChallenge(_ id: String, _ nonce: String) -> Bool {
    lock.lock()
    let handler = onChallenge
    lock.unlock()
    handler?(id, nonce)
    return handler != nil
  }

  // MARK: - 本地通知

  private func enqueue(
    center: UNUserNotificationCenter,
    args: NotifyArgs,
    completion: @escaping (NotifyOut) -> Void
  ) {
    let tag = PushContent.tagFor(path: args.path, id: args.id)
    let content = PushContent.build(
      PushMessage(id: args.id, title: args.title, body: args.body, path: args.path, tag: tag, avatar: args.avatar))
    let request = UNNotificationRequest(identifier: tag, content: content, trigger: nil)
    center.add(request) { error in
      if let error = error {
        completion(NotifyOut(posted: false, reason: error.localizedDescription))
      } else {
        completion(NotifyOut(posted: true, reason: nil))
      }
    }
  }
}
