// Oryn 移动端原生能力（iOS）：下沉侧栏的原生底栏。
// 命令只给壳的 Rust 调用（build.rs 命令表为空，页面调不到）：tabBarSet 载荷 {"spec": <spec|null>}，tabBarReveal 载荷即揭示对象；
// 用户点击经 trigger("event", {type: "tabbar", id}) 回到 Rust，再转成页面事件。
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

class OrynMobilePlugin: Plugin {
  private weak var webview: WKWebView?
  /// `TabBarOverlay`（iOS 26+）；按 AnyObject 存，存储属性不能带可用性标注。只在主线程读写。
  private var overlay: AnyObject?

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
}

@_cdecl("init_plugin_oryn_mobile")
public func initPlugin() -> Plugin {
  return OrynMobilePlugin()
}
