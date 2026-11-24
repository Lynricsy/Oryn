// RFC 8291 接收端密钥（P-256 私钥 + 16 字节 auth）的钥匙串存取，以及推送相关的系统日志：插件与
// Notification Service Extension 共用。两个进程靠同一个钥匙串访问组共享密钥：service `com.lynricsy.oryn.push`，
// 账户 `p256`（32 字节私钥标量）与 `auth`；访问组 = Info.plist 的 `OrynAppIdentifierPrefix`（构建时展开的
// `$(AppIdentifierPrefix)`，即 `<Team ID>.`）+ `com.lynricsy.oryn.push`，两个 target 的 entitlements 都列了它；
// 可访问性为首次解锁后、仅本机（锁屏时 NSE 也要能读，换机不迁移）。
// 前缀没展开（不签名的构建）时不指定访问组，落在进程的默认组里，日志里记为 group=default。
import CryptoKit
import Foundation
import os.log
import Security

/// 推送链路的系统日志：子系统 `com.lynricsy.oryn.push`，一律 default 级别、公开文本（`log show` 直接可见）。
/// 只记事件名与布尔 / 状态码，不记推送令牌、challenge 的 nonce、密文 `e` 与明文。
enum PushLog {
  private static let log = OSLog(subsystem: "com.lynricsy.oryn.push", category: "push")

  static func info(_ message: String) {
    os_log("%{public}@", log: log, type: .default, message)
  }
}

/// 一套接收端密钥；`p256dh` / `auth` 是登记订阅时交给服务端的 base64url（无填充）。
struct PushKeys {
  let privateKey: P256.KeyAgreement.PrivateKey
  let auth: Data

  var p256dh: String { PushKeyStore.base64url(privateKey.publicKey.x963Representation) }
  var authString: String { PushKeyStore.base64url(auth) }
}

enum PushKeyStore {
  static let service = "com.lynricsy.oryn.push"
  private static let privateAccount = "p256"
  private static let authAccount = "auth"
  private static let authLength = 16

  /// 共享访问组；Info.plist 里没有展开过的前缀时为 nil。
  static var accessGroup: String? {
    guard let prefix = Bundle.main.object(forInfoDictionaryKey: "OrynAppIdentifierPrefix") as? String,
      !prefix.isEmpty, !prefix.contains("$(")
    else { return nil }
    return prefix + service
  }

  /// 日志里的访问组（Team ID 前缀是公开信息）。
  static var accessGroupLabel: String { accessGroup ?? "default" }

  /// 读出已有的密钥；没有或读失败为 nil（失败的状态码记日志）。
  static func load() -> PushKeys? {
    guard let raw = read(privateAccount), let auth = read(authAccount), auth.count == authLength,
      let key = try? P256.KeyAgreement.PrivateKey(rawRepresentation: raw)
    else { return nil }
    return PushKeys(privateKey: key, auth: auth)
  }

  /// 读出已有的密钥，没有就生成并写入；`fresh` 表示这次新生成（之前登记过的订阅用的是旧公钥）。写入失败为 nil。
  static func ensure() -> (keys: PushKeys, fresh: Bool)? {
    if let keys = load() { return (keys, false) }
    var auth = Data(count: authLength)
    let status = auth.withUnsafeMutableBytes { SecRandomCopyBytes(kSecRandomDefault, authLength, $0.baseAddress!) }
    guard status == errSecSuccess else {
      PushLog.info("push.keys op=random status=\(status)")
      return nil
    }
    let key = P256.KeyAgreement.PrivateKey()
    guard save(privateKey: key, auth: auth) else { return nil }
    return (PushKeys(privateKey: key, auth: auth), true)
  }

  /// 覆盖写入一套密钥（两项都写成功才返回 true）。
  @discardableResult
  static func save(privateKey: P256.KeyAgreement.PrivateKey, auth: Data) -> Bool {
    write(privateAccount, privateKey.rawRepresentation) && write(authAccount, auth)
  }

  /// 删除密钥（关闭推送）。
  static func clear() {
    for account in [privateAccount, authAccount] {
      let status = SecItemDelete(query(account) as CFDictionary)
      if status != errSecSuccess && status != errSecItemNotFound {
        PushLog.info("push.keys op=delete status=\(status)")
      }
    }
  }

  static func base64url(_ data: Data) -> String {
    data.base64EncodedString()
      .replacingOccurrences(of: "+", with: "-")
      .replacingOccurrences(of: "/", with: "_")
      .replacingOccurrences(of: "=", with: "")
  }

  /// 无填充 base64url → 字节；含非法字符时为 nil。
  static func fromBase64url(_ text: String) -> Data? {
    var s = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
    let rem = s.count % 4
    if rem == 1 { return nil }
    if rem > 0 { s += String(repeating: "=", count: 4 - rem) }
    return Data(base64Encoded: s)
  }

  private static func query(_ account: String) -> [String: Any] {
    var q: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: service,
      kSecAttrAccount as String: account,
    ]
    if let group = accessGroup { q[kSecAttrAccessGroup as String] = group }
    return q
  }

  private static func read(_ account: String) -> Data? {
    var q = query(account)
    q[kSecReturnData as String] = true
    q[kSecMatchLimit as String] = kSecMatchLimitOne
    var out: CFTypeRef?
    let status = SecItemCopyMatching(q as CFDictionary, &out)
    guard status == errSecSuccess else {
      if status != errSecItemNotFound { PushLog.info("push.keys op=read status=\(status)") }
      return nil
    }
    return out as? Data
  }

  private static func write(_ account: String, _ value: Data) -> Bool {
    let base = query(account)
    SecItemDelete(base as CFDictionary)
    var q = base
    q[kSecValueData as String] = value
    q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
    let status = SecItemAdd(q as CFDictionary, nil)
    if status != errSecSuccess { PushLog.info("push.keys op=write status=\(status)") }
    return status == errSecSuccess
  }
}
