// iOS 本地系统通知（Communication Notifications / INSendMessageIntent）：
// 与 Android 的 NotificationCompat.MessagingStyle 对齐：
// - 普通线程：用线程标题卡图标与线程色；
// - 全局助手动态（主线）与助手对话线程：用全局助手头像；
// - 无线程系统通知：深墨底 Oryn 品牌标。
// iOS 15+ 经 `INSendMessageIntent` + `UNNotificationContent.updating(from:)` 展示通信消息样式与大圆头像；
// 若系统环境不支持或更新失败，自动回落到标准通知，绝不丢消息。
import Foundation
import Intents
import UIKit
import UserNotifications

struct NotifyAvatarArgs: Decodable {
  let kind: String
  let icon: String?
  let color: String?
  let image: String?
  let hash: String?
}

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

  private struct MessageDescriptor {
    let senderKey: String
    let senderName: String
    let body: String
    let conversationId: String
    let avatarPNG: Data?
  }

  private let lock = NSLock()
  private var onTap: ((String, String) -> Void)?
  private var pendingTaps: [(String, String)] = []

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
    guard response.actionIdentifier == UNNotificationDefaultActionIdentifier else { return }
    let info = response.notification.request.content.userInfo
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

  // MARK: - 构建通信通知

  private func enqueue(
    center: UNUserNotificationCenter,
    args: NotifyArgs,
    completion: @escaping (NotifyOut) -> Void
  ) {
    let tag = Self.tagFor(path: args.path, id: args.id)
    let avatar = Self.effectiveAvatar(args)
    let senderName = Self.displayTitle(args.title, avatar: avatar)
    let body = args.body.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
      ? senderName
      : args.body

    let mutable = UNMutableNotificationContent()
    mutable.title = senderName
    mutable.body = body
    mutable.sound = .default
    mutable.threadIdentifier = tag
    mutable.userInfo = [
      "id": args.id,
      "path": args.path
    ]

    let pngData = NotificationAvatarRenderer.renderPNG(avatar)
    let content: UNNotificationContent
    if #available(iOS 15.0, *) {
      let descriptor = MessageDescriptor(
        senderKey: Self.senderKey(tag: tag, path: args.path, avatar: avatar),
        senderName: senderName,
        body: body,
        conversationId: tag,
        avatarPNG: pngData
      )
      content = Self.makeCommunicationContent(base: mutable, descriptor: descriptor)
    } else {
      content = mutable
    }

    let request = UNNotificationRequest(identifier: tag, content: content, trigger: nil)
    center.add(request) { error in
      if let error = error {
        completion(NotifyOut(posted: false, reason: error.localizedDescription))
      } else {
        completion(NotifyOut(posted: true, reason: nil))
      }
    }
  }

  @available(iOS 15.0, *)
  private static func makeCommunicationContent(
    base: UNMutableNotificationContent,
    descriptor: MessageDescriptor
  ) -> UNNotificationContent {
    let image = descriptor.avatarPNG.map { INImage(imageData: $0) }
    let handle = INPersonHandle(value: descriptor.senderKey, type: .unknown)
    let sender = INPerson(
      personHandle: handle,
      nameComponents: nil,
      displayName: descriptor.senderName,
      image: image,
      contactIdentifier: nil,
      customIdentifier: descriptor.senderKey,
      isMe: false,
      suggestionType: .none
    )
    let intent = INSendMessageIntent(
      recipients: nil,
      outgoingMessageType: .outgoingMessageText,
      content: descriptor.body,
      speakableGroupName: nil,
      conversationIdentifier: descriptor.conversationId,
      serviceName: "Oryn",
      sender: sender,
      attachments: nil
    )
    if let image = image {
      intent.setImage(image, forParameterNamed: \.sender)
    }
    let interaction = INInteraction(intent: intent, response: nil)
    interaction.direction = .incoming
    interaction.donate(completion: nil)

    if let updated = try? base.updating(from: intent) {
      return updated
    }
    return base
  }

  private static func senderKey(tag: String, path: String, avatar: NotifyAvatarArgs) -> String {
    if avatar.kind == "assistant" && !path.hasPrefix("/t/") {
      return "oryn-assistant"
    }
    return tag
  }

  private static func effectiveAvatar(_ args: NotifyArgs) -> NotifyAvatarArgs {
    if let avatar = args.avatar { return avatar }
    if under(args.path, prefix: "/settings/assistant") || under(args.path, prefix: "/assistant") {
      return NotifyAvatarArgs(kind: "assistant", icon: nil, color: nil, image: nil, hash: nil)
    }
    if args.path.hasPrefix("/t/") {
      return NotifyAvatarArgs(kind: "thread", icon: parseTitleCardIcon(args.title), color: nil, image: nil, hash: nil)
    }
    return NotifyAvatarArgs(kind: "system", icon: nil, color: nil, image: nil, hash: nil)
  }

  private static func displayTitle(_ title: String, avatar: NotifyAvatarArgs) -> String {
    if avatar.kind == "thread", let icon = avatar.icon, !icon.isEmpty {
      let prefix = icon + " "
      if title.hasPrefix(prefix), title.count > prefix.count {
        return String(title.dropFirst(prefix.count))
      }
    }
    return title
  }

  private static func parseTitleCardIcon(_ title: String) -> String? {
    guard let space = title.firstIndex(of: " ") else { return nil }
    let icon = String(title[..<space])
    guard !icon.isEmpty, icon.utf8.count <= 16 else { return nil }
    if icon.unicodeScalars.contains(where: { $0.value < 0x80 }) { return nil }
    let rest = title[title.index(after: space)...]
    guard let colon = rest.range(of: ": ") else { return nil }
    let code = rest[..<colon.lowerBound]
    guard (1...6).contains(code.count),
      code.unicodeScalars.allSatisfy({
        ($0.value >= 0x30 && $0.value <= 0x39) || ($0.value >= 0x41 && $0.value <= 0x5A)
      })
    else { return nil }
    let text = rest[colon.upperBound...]
    guard let first = text.first, !first.isWhitespace else { return nil }
    return icon
  }

  private static func tagFor(path: String, id: String) -> String {
    if path.hasPrefix("/t/") {
      let rest = String(path.dropFirst(3))
      if !rest.isEmpty && !rest.contains("/") && !rest.contains("?") && !rest.contains("#") {
        return "oryn-" + String(rest.suffix(12))
      }
    }
    if under(path, prefix: "/stats") { return "oryn-budget" }
    if under(path, prefix: "/settings/automations") { return "oryn-automations" }
    return "oryn-" + id
  }

  private static func under(_ path: String, prefix: String) -> Bool {
    guard path.hasPrefix(prefix) else { return false }
    if path.count == prefix.count { return true }
    let next = path[path.index(path.startIndex, offsetBy: prefix.count)]
    return next == "/" || next == "?" || next == "#"
  }
}
