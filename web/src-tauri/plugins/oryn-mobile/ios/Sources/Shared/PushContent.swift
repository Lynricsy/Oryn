// 系统通知内容的构造：本地通知（插件的 `notify` 命令）与 Notification Service Extension（解密后的推送）共用一套，
// 两条路径落在同一条线程通知上、长得一样。包括 Web Push 明文载荷 `{v, id, kind, title, body, url, tag, ts?, avatar?}`
// 的解析、按路径取 tag（与服务端、Android 的 PushPayload.tagFor 同规则）、发送者头像的兜底推导，以及 iOS 15+ 的
// Communication Notifications（INSendMessageIntent + `UNNotificationContent.updating(from:)`，失败时回落到标准通知，绝不丢消息）。
// 与 Android 的 NotificationCompat.MessagingStyle 对齐：
// - 普通线程：用线程标题卡图标与线程色；
// - 全局助手动态（主线）与助手对话线程：用全局助手头像；
// - 无线程系统通知：深墨底 Oryn 品牌标。
// 不引用 Tauri 与 UIApplication（扩展里不可用）。
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

/// 一条要展示的通知。
struct PushMessage {
  let id: String
  let title: String
  let body: String
  /// 站内路径（线程 `/t/<threadId>`，无线程的如 `/stats/usage`）。
  let path: String
  let tag: String
  let avatar: NotifyAvatarArgs?
}

enum PushContent {
  private struct MessageDescriptor {
    let senderKey: String
    let senderName: String
    let body: String
    let conversationId: String
    let avatarPNG: Data?
  }

  private static let avatarKinds: Set<String> = ["thread", "assistant", "system"]
  private static let avatarColors: Set<String> = ["red", "orange", "yellow", "green", "teal", "blue", "purple", "pink"]

  /// 解析 Web Push 明文载荷；不是 JSON 对象、缺 `title`、`url` 不以 `/` 开头时为 nil（撤回消息也是 nil：它没有标题）。
  static func parse(_ data: Data) -> PushMessage? {
    guard let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
      let title = text(obj, "title"),
      let path = text(obj, "url"), path.hasPrefix("/")
    else { return nil }
    let id = text(obj, "id") ?? ""
    return PushMessage(
      id: id,
      title: title,
      body: text(obj, "body") ?? "",
      path: path,
      tag: text(obj, "tag") ?? tagFor(path: path, id: id),
      avatar: parseAvatar(obj["avatar"]))
  }

  /// 构造完整的通知内容：标题、正文、声音、按 tag 归组（threadIdentifier），`userInfo` 只带点击要用的 `id` 与 `path`；
  /// iOS 15+ 再换成带发送者圆形头像的通信通知。
  static func build(_ message: PushMessage) -> UNNotificationContent {
    let avatar = effectiveAvatar(message)
    let senderName = displayTitle(message.title, avatar: avatar)
    let body = message.body.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? senderName : message.body

    let mutable = UNMutableNotificationContent()
    mutable.title = senderName
    mutable.body = body
    mutable.sound = .default
    mutable.threadIdentifier = message.tag
    mutable.userInfo = [
      "id": message.id,
      "path": message.path,
    ]

    guard #available(iOS 15.0, *) else { return mutable }
    let descriptor = MessageDescriptor(
      senderKey: senderKey(tag: message.tag, path: message.path, avatar: avatar),
      senderName: senderName,
      body: body,
      conversationId: message.tag,
      avatarPNG: NotificationAvatarRenderer.renderPNG(avatar))
    return makeCommunicationContent(base: mutable, descriptor: descriptor)
  }

  /// 解不开时交付的占位：保留原通知的其余字段，标题 Oryn，正文按系统语言（网关给的是英文占位）。
  static func placeholder(from content: UNNotificationContent) -> UNMutableNotificationContent {
    let mutable = (content.mutableCopy() as? UNMutableNotificationContent) ?? UNMutableNotificationContent()
    mutable.title = "Oryn"
    mutable.body = prefersChinese ? "有新通知" : "New notification"
    return mutable
  }

  /// 与服务端同规则：线程路径取线程 id 末 12 位；`/stats…` → `oryn-budget`；`/settings/automations…` →
  /// `oryn-automations`；其余用通知 id。
  static func tagFor(path: String, id: String) -> String {
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

  // MARK: - 头像与标题

  private static var prefersChinese: Bool {
    Locale.preferredLanguages.first?.lowercased().hasPrefix("zh") ?? false
  }

  /// 载荷里的 `avatar`：只接受合法的 kind / icon / color / image / hash（同 Android 的 PushPayload.parseAvatar）。
  private static func parseAvatar(_ value: Any?) -> NotifyAvatarArgs? {
    guard let obj = value as? [String: Any], let kind = text(obj, "kind"), avatarKinds.contains(kind) else {
      return nil
    }
    return NotifyAvatarArgs(
      kind: kind,
      icon: text(obj, "icon").flatMap { $0.utf8.count <= 64 ? $0 : nil },
      color: text(obj, "color").flatMap { avatarColors.contains($0) ? $0 : nil },
      image: text(obj, "image").flatMap { $0.utf8.count <= 192 * 1024 ? $0 : nil },
      hash: text(obj, "hash").flatMap { $0.count <= 32 ? $0 : nil })
  }

  /// 非空字符串字段。
  private static func text(_ obj: [String: Any], _ key: String) -> String? {
    guard let s = obj[key] as? String, !s.isEmpty else { return nil }
    return s
  }

  /// 生效头像：优先用载荷 / 命令自带的 `avatar`，缺失（旧服务端）时按路径与标题卡兜底推导。
  private static func effectiveAvatar(_ message: PushMessage) -> NotifyAvatarArgs {
    if let avatar = message.avatar { return avatar }
    if under(message.path, prefix: "/settings/assistant") || under(message.path, prefix: "/assistant") {
      return NotifyAvatarArgs(kind: "assistant", icon: nil, color: nil, image: nil, hash: nil)
    }
    if message.path.hasPrefix("/t/") {
      return NotifyAvatarArgs(
        kind: "thread", icon: parseTitleCardIcon(message.title), color: nil, image: nil, hash: nil)
    }
    return NotifyAvatarArgs(kind: "system", icon: nil, color: nil, image: nil, hash: nil)
  }

  /// 发送者显示名：普通线程头像已展示标题卡图标时，去掉标题开头重复的 `<图标> ` 前缀。
  private static func displayTitle(_ title: String, avatar: NotifyAvatarArgs) -> String {
    if avatar.kind == "thread", let icon = avatar.icon, !icon.isEmpty {
      let prefix = icon + " "
      if title.hasPrefix(prefix), title.count > prefix.count {
        return String(title.dropFirst(prefix.count))
      }
    }
    return title
  }

  /// 从 omp 标题卡（`<图标> <代码>: <标题>`）提取单个非 ASCII 图标字形；不匹配返回 nil。
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

  private static func senderKey(tag: String, path: String, avatar: NotifyAvatarArgs) -> String {
    if avatar.kind == "assistant" && !path.hasPrefix("/t/") {
      return "oryn-assistant"
    }
    return tag
  }

  private static func under(_ path: String, prefix: String) -> Bool {
    guard path.hasPrefix(prefix) else { return false }
    if path.count == prefix.count { return true }
    let next = path[path.index(path.startIndex, offsetBy: prefix.count)]
    return next == "/" || next == "?" || next == "#"
  }

  // MARK: - 通信通知

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
}
