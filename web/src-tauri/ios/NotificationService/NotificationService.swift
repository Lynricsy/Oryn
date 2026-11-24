// Notification Service Extension：官方推送网关经 APNs 送来的通知（`mutable-content: 1`，载荷 `{v: "2", id, e}`，
// alert 是网关的英文占位）在这里解密后再展示。
// 一启动先把占位换成按系统语言的「有新通知」/「New notification」；取 `e` → RFC 8291 解密（接收端密钥在共享钥匙串，
// 见 PushKeyStore）→ 解析 Web Push 明文 → 用 PushContent 填标题、正文、threadIdentifier 与 userInfo（id、path）。
// 任何一步失败或超时（serviceExtensionTimeWillExpire）都交付占位，绝不显示明文字段以外的东西。
// 日志（子系统 com.lynricsy.oryn.push）：nse.received、nse.keys found=、nse.decrypted ok=、nse.content parsed=；不记 `e`、令牌与明文。
import Foundation
import UserNotifications

final class NotificationService: UNNotificationServiceExtension {
  private let lock = NSLock()
  private var handler: ((UNNotificationContent) -> Void)?
  private var placeholder: UNNotificationContent?

  override func didReceive(
    _ request: UNNotificationRequest,
    withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void
  ) {
    PushLog.info("nse.received")
    let fallback = PushContent.placeholder(from: request.content)
    lock.lock()
    handler = contentHandler
    placeholder = fallback
    lock.unlock()
    deliver(decrypt(request.content.userInfo) ?? fallback)
  }

  override func serviceExtensionTimeWillExpire() {
    PushLog.info("nse.expired")
    lock.lock()
    let fallback = placeholder
    lock.unlock()
    if let fallback { deliver(fallback) }
  }

  /// 解密并构造真实内容；任何一步不成立为 nil（交付占位）。
  private func decrypt(_ info: [AnyHashable: Any]) -> UNNotificationContent? {
    guard info["v"] as? String == "2", let e = info["e"] as? String, let body = PushKeyStore.fromBase64url(e)
    else {
      PushLog.info("nse.payload ok=false")
      return nil
    }
    let keys = PushKeyStore.load()
    PushLog.info("nse.keys found=\(keys != nil) group=\(PushKeyStore.accessGroupLabel)")
    guard let keys else { return nil }
    let plain: Data
    do {
      plain = try WebPushDecrypt.open(body, privateKey: keys.privateKey, auth: keys.auth)
    } catch {
      PushLog.info("nse.decrypted ok=false reason=\(error)")
      return nil
    }
    PushLog.info("nse.decrypted ok=true")
    guard let message = PushContent.parse(plain) else {
      PushLog.info("nse.content parsed=false")
      return nil
    }
    PushLog.info("nse.content parsed=true")
    return PushContent.build(message)
  }

  /// 只交付一次（解密完成与超时可能先后到达）。
  private func deliver(_ content: UNNotificationContent) {
    lock.lock()
    let done = handler
    handler = nil
    lock.unlock()
    done?(content)
  }
}
