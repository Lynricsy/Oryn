// Oryn 移动端原生能力（iOS）：下沉侧栏的原生底栏、线程页的原生页头（均 iOS 26+）。
// 命令只给壳的 Rust 调用（build.rs 命令表为空，页面调不到）：tabBarSet 载荷 {"spec": <spec|null>}，tabBarReveal 载荷即揭示对象；
// 用户点击经 trigger("event", {type: "tabbar", id}) 回到 Rust，再转成页面事件。
// 原生页头：headerSet 载荷 {"spec": <spec|null>}（挂到窗口上之后才应答）、headerShow 载荷即呈现状态、
// headerMenu 载荷 {open, page?}、headerProbe（调试）；交互经 trigger("event", {type: "header", op, key, menu?, id?}) 回到 Rust。
// 可替换图标：appIconGet 应答 {supported, current}（current = 资源名或 null 表示默认），appIconSet 载荷 {name: <资源名|null>}。
import Tauri
import UIKit
import WebKit

struct TabBarSetArgs: Decodable {
  let spec: TabBarSpec?
}

struct TabBarRevealArgs: Decodable {
  let x: Double
  let alpha: Double
  let ms: Double?
  let delay: Double?
  let ease: [Double]?
}

struct AppIconSetArgs: Decodable {
  let name: String?
}

struct AppIconOut: Encodable {
  let supported: Bool
  let current: String?
}

class OrynMobilePlugin: Plugin {
  private weak var webview: WKWebView?
  /// `TabBarOverlay`（iOS 26+）；按 AnyObject 存，存储属性不能带可用性标注。只在主线程读写。
  private var overlay: AnyObject?
  /// `HeaderOverlay`（iOS 26+）；同上按 AnyObject 存，只在主线程读写。
  private var header: AnyObject?

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
  @objc public func tabBarReveal(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(TabBarRevealArgs.self)
    DispatchQueue.main.async {
      if #available(iOS 26.0, *), let bar = self.overlay as? TabBarOverlay {
        bar.reveal(
          x: CGFloat(args.x), alpha: CGFloat(args.alpha), ms: args.ms ?? 0,
          delay: args.delay ?? 0, ease: args.ease)
      }
    }
    invoke.resolve()
  }

  private func apply(_ spec: TabBarSpec) {
    guard #available(iOS 26.0, *), let webview = self.webview else { return }
    let bar: TabBarOverlay
    if let existing = overlay as? TabBarOverlay {
      bar = existing
    } else {
      bar = TabBarOverlay(webview: webview, controller: manager.viewController) { [weak self] id in
        let data: JSObject = ["type": "tabbar", "id": id]
        self?.trigger("event", data: data)
      }
      overlay = bar
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
        bar.show(
          alpha: CGFloat(args.alpha), dx: CGFloat(args.dx), passive: args.passive,
          ms: args.ms ?? 0, delay: args.delay ?? 0, ease: args.ease)
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
