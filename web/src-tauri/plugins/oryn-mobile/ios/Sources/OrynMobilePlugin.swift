// Oryn 移动端原生能力（iOS）：下沉侧栏的根滚动跟随、原生底栏、线程页的原生页头（均 iOS 26+）。
// 命令只给壳的 Rust 调用（build.rs 命令表为空，页面调不到）：sinkSet 载荷 {on}（建 / 拆 SinkFollow：观察 WKWebView 根滚动的
// contentOffset，把卡片左缘交给原生底栏与页头）、sinkShow 载荷 {open, animated}（程序化开合）；
// tabBarSet 载荷 {"spec": <spec|null>}，tabBarCover 载荷 {covered}（只决定底栏接不接触摸）；
// 用户点击经 trigger("event", {type: "tabbar", id}) 回到 Rust，再转成页面事件。
// 原生页头：headerSet 载荷 {"spec": <spec|null>}（挂到窗口上之后才应答）、headerShow 载荷 {alpha, ms}、
// headerMenu 载荷 {open, page?}、headerProbe（调试）；交互经 trigger("event", {type: "header", op, key, menu?, id?}) 回到 Rust。
// 可替换图标：appIconGet 应答 {supported, current}（current = 资源名或 null 表示默认），appIconSet 载荷 {name: <资源名|null>}。
import Tauri
import UIKit
import WebKit

struct AppIconSetArgs: Decodable {
  let name: String?
}

struct AppIconOut: Encodable {
  let supported: Bool
  let current: String?
}

class OrynMobilePlugin: Plugin {
  private weak var webview: WKWebView?
  /// `TabBarUnderlay`（iOS 26+）；按 AnyObject 存，存储属性不能带可用性标注。只在主线程读写。
  private var overlay: AnyObject?
  /// `HeaderOverlay`（iOS 26+）；同上按 AnyObject 存，只在主线程读写。
  private var header: AnyObject?
  /// `SinkFollow`（iOS 26+）；同上按 AnyObject 存，只在主线程读写。
  private var sink: AnyObject?

  @objc public override func load(webview: WKWebView) {
    self.webview = webview
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
      if #available(iOS 26.0, *), let bar = self.overlay as? TabBarUnderlay {
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
    DispatchQueue.main.async { self.ensureSink() }
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

  private func ensureSink() {
    guard #available(iOS 26.0, *), let webview = self.webview, sink == nil else { return }
    let follow = SinkFollow(webview: webview) { [weak self] dx in self?.follow(dx) }
    follow.onTrace = { [weak self] op, x, to in
      let data: JSObject = ["type": "sink", "op": op, "x": Double(x), "to": Double(to)]
      self?.trigger("event", data: data)
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
    (overlay as? TabBarUnderlay)?.follow(dx: dx)
    (header as? HeaderOverlay)?.follow(dx: dx)
  }

  /// 当前卡片左缘（没有跟随时为 0）：新建的底栏 / 页头立刻补一次。
  private var currentDx: CGFloat {
    guard #available(iOS 26.0, *), let follow = sink as? SinkFollow else { return 0 }
    return follow.dx
  }

  private func apply(_ spec: TabBarSpec) {
    guard #available(iOS 26.0, *), let webview = self.webview else { return }
    let bar: TabBarUnderlay
    if let existing = overlay as? TabBarUnderlay {
      bar = existing
    } else {
      bar = TabBarUnderlay(webview: webview) { [weak self] id in
        let data: JSObject = ["type": "tabbar", "id": id]
        self?.trigger("event", data: data)
      }
      overlay = bar
      bar.follow(dx: currentDx)
    }
    bar.apply(spec)
  }

  private func removeOverlay() {
    if #available(iOS 26.0, *), let bar = overlay as? TabBarUnderlay {
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
