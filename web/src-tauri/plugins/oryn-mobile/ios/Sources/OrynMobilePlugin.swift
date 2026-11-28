// Oryn 移动端原生能力（iOS）：下沉侧栏的根滚动跟随、原生底栏、线程页的原生页头（均 iOS 26+）。
// 命令只给壳的 Rust 调用（build.rs 命令表为空，页面调不到）：sinkSet 载荷 {on, trace}（建 / 拆 SinkFollow：观察 WKWebView
// 根滚动的 contentOffset，把卡片左缘交给原生底栏与页头；trace 时经 trigger("event", {type: "sink", …}) 回传根滚动轨迹，
// 只有 debug 包会开）、sinkShow 载荷 {open, animated}（程序化开合）；
// tabBarSet 载荷 {"spec": <spec|null>}（底栏浮在 WebView 之上，右缘随 SinkFollow 裁到卡片左缘），
// tabBarCover 载荷 {covered}（网页内容压在底栏上：隐藏且不接触摸）；
// 用户点击经 trigger("event", {type: "tabbar", id}) 回到 Rust，再转成页面事件。
// 原生页头：headerSet 载荷 {"spec": <spec|null>}（挂到窗口上之后才应答）、headerShow 载荷 {alpha, ms}、
// headerMenu 载荷 {open, page?}、headerProbe（调试）；交互经 trigger("event", {type: "header", op, key, menu?, id?}) 回到 Rust。
// 可替换图标：appIconGet 应答 {supported, current}（current = 资源名或 null 表示默认），appIconSet 载荷 {name: <资源名|null>}。
// 系统通知（Communication Notifications / INSendMessageIntent）：notify 载荷 {id, title, body, path, lang, timestamp?, avatar?}
// 应答 {posted, reason?}；openNotificationSettings 打开系统通知设置；点击经 trigger("event", {type: "notificationtap", id, path}) 回传。
// 推送（APNs，经官方推送网关；插件 JSON 契约 K5，登记编排在 Rust）：pushState 应答 PushState；pushEnable（异步，忽略 vapid /
// transport）请求通知授权 → 已授权则注册远程通知并等令牌（≤ 15 秒，超时或失败 error = "apns_token"）→ 确保接收端密钥 →
// 应答 PushState（未授权也置 enabled）；pushDisable 注销远程通知、清密钥与本地状态；pushMarkSynced 载荷 {endpoint, id, server}。
// 事件：{type: "push", state}（回到前台复查后权限有变、或系统给了新令牌）与 {type: "pushchallenge", challengeId, nonce}
// （收到官方推送网关的 challenge，见 NotificationManager）。AppDelegate 的注册回调钩子见 PushRegistrar。
// iPhone Duo 竖栏（iOS 27.1+，RailProbe）：{type: "rail", side?, frame?, safeArea, probe}，几何变了就发，没有命令。
import Tauri
import UIKit
import UserNotifications
import WebKit

struct AppIconSetArgs: Decodable {
  let name: String?
}

struct PushSyncedArgs: Decodable {
  let endpoint: String
  let id: String
  let server: String
}

struct AppIconOut: Encodable {
  let supported: Bool
  let current: String?
}

struct AssistantAvatarSyncArgs: Decodable {
  let icon: String?
  let image: String?
}

class OrynMobilePlugin: Plugin {
  private weak var webview: WKWebView?
  /// `TabBarOverlay`（iOS 26+）；按 AnyObject 存，存储属性不能带可用性标注。只在主线程读写。
  private var overlay: AnyObject?
  /// `HeaderOverlay`（iOS 26+）；同上按 AnyObject 存，只在主线程读写。
  private var header: AnyObject?
  /// `SinkFollow`（iOS 26+）；同上按 AnyObject 存，只在主线程读写。
  private var sink: AnyObject?
  /// `RailProbe`（iOS 27.1+，iPhone Duo 竖栏）；同上按 AnyObject 存，只在主线程读写。
  private var rail: AnyObject?
  /// `RailOverlay`（原生竖栏）；同上。
  private var railOverlay: AnyObject?
  /// RailProbe 最近一次给的竖栏矩形（WebView 坐标）；新建的原生竖栏立刻用上。只在主线程读写。
  private var railFrame: CGRect?
  /// 只在 debug 的端到端启动中记录实际触摸；主线程配置。
  private var traceHeaderHits = false
  /// 上次应答 / 通知 Rust 时的通知权限（回到前台复查用）；只在主线程读写。
  private var lastPermission: String?

  override init() {
    super.init()
    NotificationManager.shared.installDelegate()
    // 可能早于 UIApplicationMain：先交换 setDelegate:，代理一设好就补上 APNs 注册回调
    PushRegistrar.shared.install()
  }

  @objc public override func load(webview: WKWebView) {
    self.webview = webview
    // 页面都是 viewport-fit=cover：WebKit 要等网页进程提交了视口参数（模拟器上约 1 秒）才把根滚动视图的内边距调整改成 never，
    // 在那之前按安全区内缩排版，开屏画面先挤在安全区里（Duo 外屏：382×644 落在左上角）再跳满。一开始就设成稳态值；
    // 显式设过之后 WebKit 不再改它。env(safe-area-inset-*) 取自视图的安全区，不受影响。
    webview.scrollView.contentInsetAdjustmentBehavior = .never
    NotificationManager.shared.installDelegate()
    // Rust 的 listen_ios 在主线程同步完成 registerListener 后，主队列下一轮再绑定点击回调并补发冷启动点击
    DispatchQueue.main.async { [weak self] in
      NotificationManager.shared.attach { [weak self] id, path in
        self?.trigger("event", data: ["type": "notificationtap", "id": id, "path": path])
      }
      self?.startPush()
      self?.attachRail(attempt: 0)
    }
  }

  /// iPhone Duo 竖栏探针（iOS 27.1+）：装到 WebView 的父视图上，几何变了经 {type: "rail", …} 上报；
  /// WebView 还没挂进视图层级就稍后再试（每 0.25 秒一次，最多约 5 秒）。
  private func attachRail(attempt: Int) {
    guard #available(iOS 27.1, *), rail == nil, let webview else { return }
    guard let host = webview.superview else {
      if attempt < 20 {
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) { [weak self] in
          self?.attachRail(attempt: attempt + 1)
        }
      }
      return
    }
    let probe = RailProbe(webview: webview) { [weak self] data in
      self?.trigger("event", data: data)
    } onFrame: { [weak self] rect in
      self?.railFrame = rect
      (self?.railOverlay as? RailOverlay)?.place(rect)
    }
    probe.frame = host.bounds
    host.insertSubview(probe, belowSubview: webview)
    rail = probe
  }

  /// 原生竖栏的内容。撤掉任何系统版本都成功；系统 < 27.1 以 `unsupported` 拒绝。
  /// 同 headerSet：画上了（宿主视图在窗口里）才应答，网页收到应答才撤掉自己画的竖栏；应答在主线程块里发出。
  @objc public func railSet(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(RailSetArgs.self)
    guard let spec = args.spec else {
      DispatchQueue.main.async { self.removeRail() }
      invoke.resolve()
      return
    }
    guard #available(iOS 27.1, *) else {
      invoke.reject("unsupported")
      return
    }
    DispatchQueue.main.async {
      if self.applyRail(spec) { invoke.resolve() } else { invoke.reject("unsupported") }
    }
  }

  private func applyRail(_ spec: RailSpec) -> Bool {
    guard #available(iOS 27.1, *), let webview = self.webview else { return false }
    let bar: RailOverlay
    if let existing = railOverlay as? RailOverlay {
      bar = existing
    } else {
      bar = RailOverlay(webview: webview, controller: manager.viewController) {
        [weak self] key, part, id, item in
        var data: JSObject = ["type": "railtap", "key": key, "part": part]
        if let id { data["id"] = id }
        if let item { data["item"] = item }
        self?.trigger("event", data: data)
      }
      railOverlay = bar
      bar.place(railFrame)
    }
    return bar.apply(spec)
  }

  private func removeRail() {
    if #available(iOS 26.0, *), let bar = railOverlay as? RailOverlay {
      bar.remove()
    }
    railOverlay = nil
  }

  /// 命令跑在 Tauri 的后台串行队列上：界面操作转主线程后立即应答，绝不等主线程（Rust 那边可能正阻塞着它）。
  @objc public func tabBarSet(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(TabBarSetArgs.self)
    guard let spec = args.spec else {
      // 撤栏：任何系统版本都成功
      DispatchQueue.main.async { self.removeOverlay() }
      invoke.resolve()
      return
    }
    guard #available(iOS 26.0, *) else {
      invoke.reject("unsupported")
      return
    }
    DispatchQueue.main.async { self.apply(spec) }
    invoke.resolve()
  }

  /// 还没 set 过（或系统 < 26）时空操作并成功。
  @objc public func tabBarCover(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(TabBarCoverArgs.self)
    DispatchQueue.main.async {
      if #available(iOS 26.0, *), let bar = self.overlay as? TabBarOverlay {
        bar.cover(args.covered)
      }
    }
    invoke.resolve()
  }

  /// 建 / 拆根滚动跟随。撤掉任何系统版本都成功；系统 < 26 时 `on` 以 `unsupported` 拒绝。
  @objc public func sinkSet(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(SinkSetArgs.self)
    guard args.on else {
      DispatchQueue.main.async { self.removeSink() }
      invoke.resolve()
      return
    }
    guard #available(iOS 26.0, *) else {
      invoke.reject("unsupported")
      return
    }
    let trace = args.trace ?? false
    DispatchQueue.main.async { self.ensureSink(trace: trace) }
    invoke.resolve()
  }

  /// 程序化开合；还没 sinkSet(true)（或系统 < 26）时空操作并成功。
  @objc public func sinkShow(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(SinkShowArgs.self)
    DispatchQueue.main.async {
      if #available(iOS 26.0, *), let follow = self.sink as? SinkFollow {
        follow.show(open: args.open, animated: args.animated)
      }
    }
    invoke.resolve()
  }

  /// 建跟随；`trace` 时才装轨迹回调（debug 包）：没装时 SinkFollow 的 KVO 不取调用栈、不发事件。
  private func ensureSink(trace: Bool) {
    guard #available(iOS 26.0, *), let webview = self.webview, sink == nil else { return }
    traceHeaderHits = trace && ProcessInfo.processInfo.environment["ORYN_E2E_URL"] != nil
    let follow = SinkFollow(webview: webview) { [weak self] dx in self?.follow(dx) }
    if trace {
      follow.onTrace = { [weak self] op, x, to, stack in
        var data: JSObject = ["type": "sink", "op": op, "x": Double(x), "to": Double(to)]
        if let stack { data["stack"] = stack }
        self?.trigger("event", data: data)
      }
    }
    sink = follow
  }

  private func removeSink() {
    if #available(iOS 26.0, *), let follow = sink as? SinkFollow {
      follow.remove()
    }
    sink = nil
  }

  /// 卡片左缘交给原生底栏与页头（SinkFollow 的 KVO 回调里，与卡片同一帧）。
  private func follow(_ dx: CGFloat) {
    guard #available(iOS 26.0, *) else { return }
    (overlay as? TabBarOverlay)?.follow(dx: dx)
    (header as? HeaderOverlay)?.follow(dx: dx)
  }

  /// 当前卡片左缘（没有跟随时为 0）：新建的底栏 / 页头立刻补一次。
  private var currentDx: CGFloat {
    guard #available(iOS 26.0, *), let follow = sink as? SinkFollow else { return 0 }
    return follow.dx
  }

  private func apply(_ spec: TabBarSpec) {
    guard #available(iOS 26.0, *), let webview = self.webview else { return }
    let bar: TabBarOverlay
    if let existing = overlay as? TabBarOverlay {
      bar = existing
    } else {
      bar = TabBarOverlay(webview: webview) { [weak self] id in
        let data: JSObject = ["type": "tabbar", "id": id]
        self?.trigger("event", data: data)
      }
      overlay = bar
      bar.follow(dx: currentDx)
    }
    bar.apply(spec)
  }

  private func removeOverlay() {
    if #available(iOS 26.0, *), let bar = overlay as? TabBarOverlay {
      bar.remove()
    }
    overlay = nil
  }

  /// 与 tabBarSet 不同：画上了才应答（网页收到应答就撤掉自己的页头，没画上会变成没有页头）。
  /// 应答在主线程块里发出（main.async，绝不 main.sync）；Rust 侧只在异步任务里等它，不占主线程，所以不会死锁。
  @objc public func headerSet(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(HeaderSetArgs.self)
    guard let spec = args.spec else {
      // 撤掉：任何系统版本都成功
      DispatchQueue.main.async { self.removeHeader() }
      invoke.resolve()
      return
    }
    guard #available(iOS 26.0, *) else {
      invoke.reject("unsupported")
      return
    }
    DispatchQueue.main.async {
      if self.applyHeader(spec) { invoke.resolve() } else { invoke.reject("unsupported") }
    }
  }

  /// 还没 set 过（或系统 < 26）时空操作并成功。
  @objc public func headerShow(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(HeaderShowArgs.self)
    DispatchQueue.main.async {
      if #available(iOS 26.0, *), let bar = self.header as? HeaderOverlay {
        bar.show(alpha: CGFloat(args.alpha), ms: args.ms ?? 0)
      }
    }
    invoke.resolve()
  }

  /// 网页强制打开 / 收起菜单（不产生 menu 事件）；还没 set 过时空操作并成功。
  @objc public func headerMenu(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(HeaderMenuArgs.self)
    DispatchQueue.main.async {
      if #available(iOS 26.0, *), let bar = self.header as? HeaderOverlay {
        bar.menu(open: args.open, page: args.page)
      }
    }
    invoke.resolve()
  }

  /// 调试（CI 演示）：各采样点的触摸是否被原生层截住；在主线程上取样后应答。
  @objc public func headerProbe(_ invoke: Invoke) {
    DispatchQueue.main.async {
      if #available(iOS 26.0, *), let bar = self.header as? HeaderOverlay {
        invoke.resolve(bar.probe())
      } else {
        invoke.resolve(HeaderProbeOut(center: false, lead: false, title: false, trail: false))
      }
    }
  }

  /// 可替换图标的当前状态；UIApplication 只能在主线程读。
  @objc public func appIconGet(_ invoke: Invoke) {
    DispatchQueue.main.async {
      let app = UIApplication.shared
      invoke.resolve(AppIconOut(supported: app.supportsAlternateIcons, current: app.alternateIconName))
    }
  }

  /// 换图标：系统改完（并弹出「已更改图标」提示）后在完成回调里应答；Rust 侧异步等待，不占线程。
  @objc public func appIconSet(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(AppIconSetArgs.self)
    DispatchQueue.main.async {
      let app = UIApplication.shared
      guard app.supportsAlternateIcons else {
        invoke.reject("unsupported")
        return
      }
      if app.alternateIconName == args.name {
        invoke.resolve()
        return
      }
      app.setAlternateIconName(args.name) { error in
        if let error { invoke.reject(error.localizedDescription) } else { invoke.resolve() }
      }
    }
  }

  /// 投递通信样式本地通知（INSendMessageIntent + 发送者圆形头像）。
  @objc public func notify(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(NotifyArgs.self)
    NotificationManager.shared.post(args) { out in
      invoke.resolve(out)
    }
  }

  /// 打开系统设置里的本应用通知设置页。
  @objc public func openNotificationSettings(_ invoke: Invoke) {
    NotificationManager.shared.openSettings()
    invoke.resolve()
  }

  /// 同步全局助手头像到共享 Keychain 缓存，供 NSE 后台推送按 hash 命中显示自定义头像。
  @objc public func syncAssistantAvatar(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(AssistantAvatarSyncArgs.self)
    NotificationAvatarRenderer.syncAssistantAvatar(icon: args.icon, image: args.image)
    invoke.resolve()
  }

  // MARK: - 推送（APNs）

  @objc public func pushState(_ invoke: Invoke) {
    PushStore.state { invoke.resolve($0) }
  }

  /// 未授权也置 enabled（与 Android 一致）：之后在系统设置里打开通知，回到前台复查时补注册并发 push 事件。
  @objc public func pushEnable(_ invoke: Invoke) {
    PushStore.setEnabled(true)
    PushStore.setError(nil)
    UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
      PushLog.info("push.enable granted=\(granted)")
      guard granted else {
        self.finishEnable(invoke)
        return
      }
      PushRegistrar.shared.register(timeout: 15) { outcome in
        switch outcome {
        case .token(let hex)?:
          PushStore.setToken(hex)
        case .failed?, nil:
          PushStore.setError("apns_token")
        }
        self.finishEnable(invoke)
      }
    }
  }

  @objc public func pushDisable(_ invoke: Invoke) {
    DispatchQueue.main.async { UIApplication.shared.unregisterForRemoteNotifications() }
    PushKeyStore.clear()
    PushStore.clear()
    PushLog.info("push.disable")
    PushStore.state { invoke.resolve($0) }
  }

  @objc public func pushMarkSynced(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(PushSyncedArgs.self)
    PushStore.markSynced(endpoint: args.endpoint, subscriptionId: args.id, server: args.server)
    invoke.resolve()
  }

  /// 状态里会确保接收端密钥存在（PushStore.state），然后应答。
  private func finishEnable(_ invoke: Invoke) {
    PushStore.state { state in
      DispatchQueue.main.async { self.lastPermission = state.permission }
      invoke.resolve(state)
    }
  }

  /// load 之后在主线程：让 UIKit 重新检查补上的注册回调，绑定 challenge 与新令牌的回调、监听回到前台，并做一次启动复查；
  /// debug 包另跑 CI 的推送探针。
  private func startPush() {
    PushRegistrar.shared.install(refresh: true)
    NotificationManager.shared.attachChallenges { [weak self] id, nonce in
      DispatchQueue.main.async {
        try? self?.trigger("event", data: PushChallengeEvent(challengeId: id, nonce: nonce))
      }
    }
    PushRegistrar.shared.observeTokens { [weak self] hex in
      guard PushStore.isEnabled, hex != PushStore.token else { return }
      PushStore.setToken(hex)
      self?.emitPush()
    }
    NotificationCenter.default.addObserver(
      self, selector: #selector(willEnterForeground), name: UIApplication.willEnterForegroundNotification,
      object: nil)
    refreshPush()
    #if DEBUG
      if ProcessInfo.processInfo.environment["ORYN_PUSH_PROBE"] == "1" { PushRegistrar.probe() }
    #endif
  }

  @objc private func willEnterForeground() {
    refreshPush()
  }

  /// 已启用时复查权限并重新注册远程通知（Apple 建议每次启动都注册）：权限有变立即发 push 事件；
  /// 令牌有变由 PushRegistrar.observeTokens 的回调发。第一次复查只记下基线。
  private func refreshPush() {
    guard PushStore.isEnabled else { return }
    PushStore.permission { permission in
      DispatchQueue.main.async {
        let changed = self.lastPermission != nil && self.lastPermission != permission
        self.lastPermission = permission
        if changed { self.emitPush() }
        if permission == "granted" {
          PushRegistrar.shared.register(timeout: 15) { _ in }
        }
      }
    }
  }

  private func emitPush() {
    PushStore.state { state in
      DispatchQueue.main.async {
        try? self.trigger("event", data: PushEvent(state: state))
      }
    }
  }

  /// 返回宿主视图是否已挂到窗口上。
  private func applyHeader(_ spec: HeaderSpec) -> Bool {
    guard #available(iOS 26.0, *), let webview = self.webview else { return false }
    let bar: HeaderOverlay
    if let existing = header as? HeaderOverlay {
      bar = existing
    } else {
      bar = HeaderOverlay(webview: webview, controller: manager.viewController) {
        [weak self] op, key, menu, id in
        var data: JSObject = ["type": "header", "op": op, "key": key]
        if let menu = menu { data["menu"] = menu }
        if let id = id { data["id"] = id }
        self?.trigger("event", data: data)
      }
      if traceHeaderHits {
        bar.traceHits { [weak self] point, info in
          self?.trigger("event", data: [
            "type": "headerhit", "x": Double(point.x), "y": Double(point.y), "info": info,
          ])
        }
      }
      header = bar
      bar.follow(dx: currentDx)
    }
    return bar.apply(spec)
  }

  private func removeHeader() {
    if #available(iOS 26.0, *), let bar = header as? HeaderOverlay {
      bar.remove()
    }
    header = nil
  }
}

@_cdecl("init_plugin_oryn_mobile")
public func initPlugin() -> Plugin {
  return OrynMobilePlugin()
}
