// APNs 注册结果的钩子与等令牌。tauri 的 iOS 宿主是 tao 0.37：UIApplicationMain 按类名 "AppDelegate" 建的代理
// 没实现 application:didRegisterForRemoteNotificationsWithDeviceToken: 与
// application:didFailToRegisterForRemoteNotificationsWithError:（tao 的 platform_impl/ios/view.rs），UIKit 拿到结果也无处可交。
// 做法：交换 -[UIApplication setDelegate:]，每次设代理之前先给代理的类 class_addMethod 这两个回调（类已实现时包一层，
// 先交给我们再调原实现）。插件可能在 UIApplicationMain 之前初始化（那时由交换后的 setter 补），也可能在代理设好之后
// （tauri 在 didFinishLaunching 里建插件）：install() 也直接给现有代理的类补方法；这样补上的要等 load(webview:) 时
// install(refresh: true) 把代理置空再设回同一对象，让 UIKit 重新检查代理实现了哪些回调（GoogleUtilities 的
// GULAppDelegateSwizzler 同样做法；这里另持有代理的强引用，置空期间不会被释放）。不在 didFinishLaunching 中途换代理。
// 插件 init() 与 load(webview:) 各调一次，幂等。令牌转小写十六进制；日志只记 apns.registered bytes= / apns.register_failed。
import CryptoKit
import Foundation
import ObjectiveC
import UIKit
import UserNotifications

final class PushRegistrar {
  static let shared = PushRegistrar()

  enum Outcome {
    /// 小写十六进制令牌。
    case token(String)
    /// 系统报注册失败（未签名的包没有 aps-environment 时就是这样）。
    case failed(String)
  }

  private final class Waiter {
    var done = false
    let completion: (Outcome?) -> Void
    init(_ completion: @escaping (Outcome?) -> Void) { self.completion = completion }
  }

  private let lock = NSLock()
  private var waiters: [Waiter] = []
  private var swizzled = false
  private var hooked = Set<ObjectIdentifier>()
  /// 现有代理的类是事后补的方法，还没让 UIKit 重新检查；只在主线程读写。
  private var needsRefresh = false
  /// 置空再设回代理期间（及之后）持有它：UIApplication.delegate 不保留代理。
  private var retainedDelegate: AnyObject?
  /// 收到令牌（含系统自发的回调）时调用，在等令牌的回调之后；见 observeTokens。
  private var onToken: ((String) -> Void)?

  private init() {}

  /// 插件据此比对令牌、更新 PushStore 并发 push 事件。
  func observeTokens(_ handler: @escaping (String) -> Void) {
    lock.lock()
    onToken = handler
    lock.unlock()
  }

  /// 装钩子（任何线程可调，实际在主线程做）；`refresh` 时把事后补了方法的代理重新设一遍。
  func install(refresh: Bool = false) {
    guard Thread.isMainThread else {
      DispatchQueue.main.async { self.install(refresh: refresh) }
      return
    }
    lock.lock()
    let first = !swizzled
    swizzled = true
    lock.unlock()
    if first { swizzleSetDelegate() }
    guard let app = Self.sharedApplication(), let delegate = app.delegate else { return }
    if addHooks(to: object_getClass(delegate)) { needsRefresh = true }
    guard refresh, needsRefresh else { return }
    needsRefresh = false
    retainedDelegate = delegate
    app.delegate = nil
    app.delegate = delegate
    PushLog.info("apns.hook refreshed=true")
  }

  /// registerForRemoteNotifications 并等结果：令牌、失败，或 `timeout` 秒后为 nil。
  func register(timeout: TimeInterval, completion: @escaping (Outcome?) -> Void) {
    let waiter = Waiter(completion)
    lock.lock()
    waiters.append(waiter)
    lock.unlock()
    DispatchQueue.global().asyncAfter(deadline: .now() + timeout) { [weak self] in
      self?.finish(waiter, nil)
    }
    DispatchQueue.main.async {
      self.install()
      UIApplication.shared.registerForRemoteNotifications()
    }
  }

  private func finish(_ waiter: Waiter, _ outcome: Outcome?) {
    lock.lock()
    guard !waiter.done else {
      lock.unlock()
      return
    }
    waiter.done = true
    waiters.removeAll { $0 === waiter }
    lock.unlock()
    waiter.completion(outcome)
  }

  private func received(_ outcome: Outcome) {
    lock.lock()
    let pending = waiters.filter { !$0.done }
    pending.forEach { $0.done = true }
    waiters.removeAll()
    let tokenHandler = onToken
    lock.unlock()
    pending.forEach { $0.completion(outcome) }
    if case .token(let hex) = outcome { tokenHandler?(hex) }
  }

  // MARK: - 运行时钩子

  /// UIApplicationMain 之前 `UIApplication.shared` 还是 nil（Swift 的非可选声明会直接崩），经运行时取。
  private static func sharedApplication() -> UIApplication? {
    let cls: AnyClass = UIApplication.self
    let sel = NSSelectorFromString("sharedApplication")
    guard let method = class_getClassMethod(cls, sel) else { return nil }
    typealias Getter = @convention(c) (AnyClass, Selector) -> UIApplication?
    return unsafeBitCast(method_getImplementation(method), to: Getter.self)(cls, sel)
  }

  private func swizzleSetDelegate() {
    let cls: AnyClass = UIApplication.self
    let sel = #selector(setter: UIApplication.delegate)
    guard let method = class_getInstanceMethod(cls, sel) else {
      PushLog.info("apns.hook swizzled=false")
      return
    }
    typealias Setter = @convention(c) (UIApplication, Selector, AnyObject?) -> Void
    let original = unsafeBitCast(method_getImplementation(method), to: Setter.self)
    let replacement: @convention(block) (UIApplication, AnyObject?) -> Void = { app, delegate in
      if let delegate { PushRegistrar.shared.addHooks(to: object_getClass(delegate)) }
      original(app, sel, delegate)
    }
    method_setImplementation(method, imp_implementationWithBlock(replacement))
  }

  /// 给代理的类补两个回调；这个类已经处理过返回 false。
  @discardableResult
  private func addHooks(to cls: AnyClass?) -> Bool {
    guard let cls else { return false }
    lock.lock()
    let fresh = hooked.insert(ObjectIdentifier(cls)).inserted
    lock.unlock()
    guard fresh else { return false }
    let ok = hook(
      cls, #selector(UIApplicationDelegate.application(_:didRegisterForRemoteNotificationsWithDeviceToken:))
    ) { arg in
      guard let token = arg as? Data else { return }
      PushLog.info("apns.registered bytes=\(token.count)")
      PushRegistrar.shared.received(.token(token.map { String(format: "%02x", $0) }.joined()))
    }
    let fail = hook(
      cls, #selector(UIApplicationDelegate.application(_:didFailToRegisterForRemoteNotificationsWithError:))
    ) { arg in
      let error = arg as? NSError
      let detail = error.map { "\($0.domain) \($0.code): \($0.localizedDescription)" } ?? "unknown"
      PushLog.info("apns.register_failed error=\(detail)")
      PushRegistrar.shared.received(.failed(detail))
    }
    PushLog.info("apns.hook class=\(NSStringFromClass(cls)) registered=\(ok) failed=\(fail)")
    return true
  }

  /// `- (void)application:(UIApplication *)app <selector>:(id)arg`：没有就加，有就包一层（先 ours 再原实现）。
  /// 返回 true 表示新加，false 表示包了已有实现。
  private func hook(_ cls: AnyClass, _ sel: Selector, _ ours: @escaping (AnyObject) -> Void) -> Bool {
    typealias Callback = @convention(c) (AnyObject, Selector, UIApplication, AnyObject) -> Void
    var original: Callback?
    let block: @convention(block) (AnyObject, UIApplication, AnyObject) -> Void = { obj, app, arg in
      ours(arg)
      original?(obj, sel, app, arg)
    }
    let imp = imp_implementationWithBlock(block)
    if class_addMethod(cls, sel, imp, "v@:@@") { return true }
    if let method = class_getInstanceMethod(cls, sel) {
      original = unsafeBitCast(method_setImplementation(method, imp), to: Callback.self)
    }
    return false
  }
}

#if DEBUG
  // CI 的推送探针（仅 debug 构建，环境变量 ORYN_PUSH_PROBE=1，由 ci/ios-push-check.sh 经 PushE2ETests 启动）：
  // 把 RFC 8291 附录 A 的接收端私钥与 auth 写进共享钥匙串（ci/ios-push/notify.apns 的密文就是加密给这把密钥的，
  // 由 NSE 解开）；在 App 进程里先用 WebPushDecrypt 解附录 A 的报文自检；再请求通知授权并注册远程通知，
  // 注册结果由上面的钩子记 apns.registered / apns.register_failed。
  extension PushRegistrar {
    private static let vectorPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
    private static let vectorAuth = "BTBZMqHH6r4Tts7J_aSIgg"
    private static let vectorBody =
      "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6Tlz"
      + "AC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
    private static let vectorPlain = "When I grow up, I want to be a watermelon"

    static func probe() {
      PushLog.info("probe.start")
      guard let raw = PushKeyStore.fromBase64url(vectorPrivate), let auth = PushKeyStore.fromBase64url(vectorAuth),
        let body = PushKeyStore.fromBase64url(vectorBody),
        let key = try? P256.KeyAgreement.PrivateKey(rawRepresentation: raw)
      else {
        PushLog.info("probe.keys saved=false reason=vector")
        return
      }
      let saved = PushKeyStore.save(privateKey: key, auth: auth)
      PushLog.info("probe.keys saved=\(saved) group=\(PushKeyStore.accessGroupLabel)")
      if #available(iOS 14.0, *) {
        let plain = try? WebPushDecrypt.open(body, privateKey: key, auth: auth)
        PushLog.info("probe.vector ok=\(plain.map { String(decoding: $0, as: UTF8.self) == vectorPlain } ?? false)")
      }
      UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
        PushLog.info("probe.permission granted=\(granted)")
        // 注册不依赖通知授权；钩子是否生效只看有没有回调
        shared.register(timeout: 60) { outcome in
          if outcome == nil { PushLog.info("probe.register timeout=true") }
        }
      }
    }
  }
#endif
