// 叠在 WKWebView 之上的裁切容器：容器的 frame 就是底栏当前可见的那一段（揭示边 x − 栏左缘），
// 容器之外、以及容器里胶囊之外的出血区的触摸都不归它，网页照常交互。
// 「在卡片之下」靠裁切模拟：原生视图只能整体叠在 WebView 上，卡片左缘扫过的那一侧被裁掉，误差只落在被卡片挡住的一侧。
import SwiftUI
import UIKit
import WebKit

/// 只截住落在子视图（胶囊）上的触摸；落在自身（透明的出血区）上的交给下面的 WebView。
final class TabBarClipView: UIView {
  override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
    let hit = super.hitTest(point, with: event)
    return hit === self ? nil : hit
  }
}

@available(iOS 26.0, *)
final class TabBarOverlay {
  /// 胶囊四周留给玻璃投影的出血；等于网页里胶囊右缘到卡片左缘的 12px。
  static let bleed: CGFloat = 12
  /// 网页没给 ease 时的缓动（= `--ease`）。
  static let defaultEase: [Double] = [0.2, 0.8, 0.2, 1]

  private let clip = TabBarClipView()
  private let model: TabBarModel
  private let hosting: UIHostingController<TabBarView>
  private weak var webview: WKWebView?
  /// 胶囊在 webview 父视图坐标里的矩形。
  private var frame: CGRect = .zero
  /// 最近一次揭示的目标值（几何变了时按它重排）。
  private var revealX: CGFloat = 0
  private var revealAlpha: CGFloat = 1
  private var animator: UIViewPropertyAnimator?

  init(webview: WKWebView, controller: UIViewController?, onTap: @escaping (String) -> Void) {
    let model = TabBarModel()
    model.onTap = onTap
    self.model = model
    self.webview = webview
    hosting = UIHostingController(rootView: TabBarView(model: model))
    hosting.view.backgroundColor = .clear
    // 宿主视图默认随父视图伸缩：关掉，裁切容器变窄时胶囊不能跟着挤
    hosting.view.autoresizingMask = []
    // 位置矩形由网页量好（已含安全区），宿主控制器不再自己让安全区
    hosting.safeAreaRegions = []
    // 用父视图 bounds 裁切，不用 layer.mask（mask 会让系统的模糊材质失效）
    clip.clipsToBounds = true
    clip.backgroundColor = .clear
    clip.isHidden = true
    controller?.addChild(hosting)
    clip.addSubview(hosting.view)
    if let controller = controller {
      hosting.didMove(toParent: controller)
    }
    if let parent = webview.superview {
      parent.insertSubview(clip, aboveSubview: webview)
    } else {
      controller?.view.addSubview(clip)
    }
  }

  func apply(_ spec: TabBarSpec) {
    model.update(spec)
    let style: UIUserInterfaceStyle = spec.dark ? .dark : .light
    // 宿主控制器的特征继承自父控制器而不是父视图：两处都设
    hosting.overrideUserInterfaceStyle = style
    clip.overrideUserInterfaceStyle = style
    let origin = webview?.frame.origin ?? .zero
    let next = CGRect(
      x: origin.x + CGFloat(spec.frame.x), y: origin.y + CGFloat(spec.frame.y),
      width: CGFloat(spec.frame.width), height: CGFloat(spec.frame.height))
    // 只有内容变了（选中、头像、配色）时不打断在途的揭示过渡
    guard next != frame else { return }
    frame = next
    hosting.view.frame = CGRect(
      x: Self.bleed, y: Self.bleed, width: frame.width, height: frame.height)
    // 几何变了：按最近的揭示目标立即重排
    reveal(x: revealX, alpha: revealAlpha, ms: 0, delay: 0, ease: nil)
  }

  /// 揭示边 x（视口坐标）对应的容器矩形：可见宽度 = clamp(x − (frame.x − 12), 0, frame.width + 24)。
  private func clipFrame(_ x: CGFloat) -> CGRect {
    let left = frame.minX - Self.bleed
    let edge = x + (webview?.frame.origin.x ?? 0)
    let width = min(max(edge - left, 0), frame.width + 2 * Self.bleed)
    return CGRect(
      x: left, y: frame.minY - Self.bleed, width: width, height: frame.height + 2 * Self.bleed)
  }

  /// 打断在途过渡并停在当前呈现值；`ms == 0` 立即生效，否则等 `delay` 后按 `ease` 过渡 `ms` 毫秒。
  /// 终态宽度为 0 或 alpha 为 0 时立即不可交互，过渡结束后隐藏。
  func reveal(x: CGFloat, alpha: CGFloat, ms: Double, delay: Double, ease: [Double]?) {
    if let running = animator, running.state == .active {
      // 不走完：视图停在此刻的呈现值，下一段从那里出发
      running.stopAnimation(true)
    }
    animator = nil
    revealX = x
    revealAlpha = alpha
    let target = clipFrame(x)
    let visible = target.width > 0 && alpha > 0
    clip.isUserInteractionEnabled = visible
    guard ms > 0 else {
      clip.frame = target
      clip.alpha = alpha
      clip.isHidden = !visible
      return
    }
    clip.isHidden = false
    let e = ease.flatMap { $0.count == 4 ? $0 : nil } ?? Self.defaultEase
    let timing = UICubicTimingParameters(
      controlPoint1: CGPoint(x: e[0], y: e[1]), controlPoint2: CGPoint(x: e[2], y: e[3]))
    let next = UIViewPropertyAnimator(duration: ms / 1000, timingParameters: timing)
    next.addAnimations {
      self.clip.frame = target
      self.clip.alpha = alpha
    }
    next.addCompletion { [weak self, weak next] position in
      guard let self = self else { return }
      if position == .end { self.clip.isHidden = !visible }
      if self.animator === next { self.animator = nil }
    }
    animator = next
    next.startAnimation(afterDelay: delay / 1000)
  }

  func remove() {
    if let running = animator, running.state == .active {
      running.stopAnimation(true)
    }
    animator = nil
    hosting.willMove(toParent: nil)
    hosting.view.removeFromSuperview()
    clip.removeFromSuperview()
    hosting.removeFromParent()
  }
}
