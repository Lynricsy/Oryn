// 线程页与首页的原生页头（侧栏钮 / 标题胶囊 / 视图钮与两个菜单；首页只有侧栏钮）：一张与 WKWebView 等大的透明宿主视图叠在它上面。
// 平时只截落在现有控件矩形里的触摸，其余全部放行给网页；标题菜单开着时整屏接管（关闭区 + 面板）。
// 视图菜单是系统菜单，由 UIKit 画在窗口最上层并自己处理点外收起，这里不接管。
// 放行不靠 SwiftUI 的命中结果：控件矩形是网页给的，这里直接比（判断错了整屏都点不动，所以要确定性的）。
// 原生页头跟着下沉卡片走：卡片开合是 WKWebView 根滚动视图的横向滚动，SinkFollow 观察 contentOffset 并在同一次回调里
// 调 follow(dx:) 平移整层（同进程同帧，不需要对时）；卡片离开关闭位时只显示不接触摸。网页只管透明度（show）。
import SwiftUI
import UIKit
import WebKit

final class HeaderHostView: UIView {
  /// 现有控件（侧栏钮，有的话再加标题胶囊、视图钮）在本视图坐标里的矩形（= 网页布局视口坐标）。
  var controlFrames: [CGRect] = []
  /// 标题菜单开着：整屏接管。
  var modal = false
  /// 只显示不接触摸。
  var passive = false
  /// 最近一次 show 的目标是可见的（alpha > 0）；淡出一开始就为假，过渡途中也不再截触摸。
  var shown = false
  /// 控件层的水平位移。
  var dx: CGFloat = 0
  var onEscape: () -> Bool = { false }
  /// 仅模拟器端到端启用：观测真实触摸走到宿主后如何命中。
  var onHit: ((CGPoint, String) -> Void)?

  /// 这个点上的触摸归不归原生层。菜单没开时只有控件矩形之内为真。
  func captures(_ point: CGPoint) -> Bool {
    if passive || !shown || isHidden || alpha < 0.01 { return false }
    return modal || controlFrames.contains { $0.offsetBy(dx: dx, dy: 0).contains(point) }
  }

  override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
    // 归原生的点：交给 SwiftUI；它没接住（透明处）也不漏给网页
    let accepts = captures(point)
    let hit = accepts ? super.hitTest(point, with: event) : nil
    if let onHit, event?.type == .touches {
      let target = hit.map { String(describing: type(of: $0)) } ?? "nil"
      let info = "accepts=\(accepts) target=\(target) dx=\(dx) passive=\(passive) "
        + "shown=\(shown) hidden=\(isHidden) alpha=\(alpha) modal=\(modal)"
      // 不在 UIKit 的命中遍历中跨插件边界。
      DispatchQueue.main.async { onHit(point, info) }
    }
    return accepts ? (hit ?? self) : nil
  }

  /// 读屏的「退出」手势（双指搓）：子页 → 主页 → 收起。
  override func accessibilityPerformEscape() -> Bool { onEscape() }
}

@available(iOS 26.0, *)
final class HeaderOverlay {
  private let host = HeaderHostView()
  private let model: HeaderModel
  private let hosting: UIHostingController<HeaderRootView>
  private weak var webview: WKWebView?
  private var animator: UIViewPropertyAnimator?

  init(
    webview: WKWebView, controller: UIViewController?,
    onEvent: @escaping (_ op: String, _ key: String, _ menu: String?, _ id: String?) -> Void
  ) {
    // 先建好 model 再建宿主控制器：所有存储属性初始化之前不能读 self
    let model = HeaderModel()
    self.model = model
    self.webview = webview
    hosting = UIHostingController(rootView: HeaderRootView(model: model))
    hosting.view.backgroundColor = .clear
    // 位置矩形由网页量好（已含安全区），宿主控制器不再自己让安全区
    hosting.safeAreaRegions = []
    host.backgroundColor = .clear
    // set 之后默认不可见，等网页的第一条 show
    host.alpha = 0
    host.isHidden = true
    host.frame = webview.frame
    host.autoresizingMask = [.flexibleWidth, .flexibleHeight]
    hosting.view.frame = host.bounds
    hosting.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
    controller?.addChild(hosting)
    host.addSubview(hosting.view)
    if let controller = controller {
      hosting.didMove(toParent: controller)
    }
    if let parent = webview.superview {
      // 原生底栏（TabBarClipView）也浮在 WebView 之上：页头（含整屏菜单）压在它上面，不论谁先创建
      if let clip = parent.subviews.last(where: { $0 is TabBarClipView }) {
        parent.insertSubview(host, aboveSubview: clip)
      } else {
        parent.insertSubview(host, aboveSubview: webview)
      }
    } else {
      controller?.view.addSubview(host)
    }
    model.onEvent = onEvent
    model.onModal = { [weak self] modal in self?.setModal(modal) }
    host.onEscape = { [weak self] in self?.model.back() ?? false }
  }

  /// 换内容与矩形，不动可见性。返回宿主视图是否已在窗口里（网页据此才撤掉自己的页头）。
  func apply(_ spec: HeaderSpec) -> Bool {
    if let webview = webview, host.superview != nil, host.superview === webview.superview {
      host.frame = webview.frame
    }
    model.update(spec)
    let style: UIUserInterfaceStyle = spec.dark ? .dark : .light
    // 宿主控制器的特征继承自父控制器而不是父视图：两处都设
    hosting.overrideUserInterfaceStyle = style
    host.overrideUserInterfaceStyle = style
    var frames = [spec.lead.frame.rect]
    if let title = spec.title { frames.append(title.frame.rect) }
    if let trail = spec.trail { frames.append(trail.frame.rect) }
    host.controlFrames = frames
    return host.window != nil
  }

  /// 跟随卡片左缘（SinkFollow 的 dx）：整层平移；离开关闭位（dx > 0.5）时只显示不接触摸。
  func follow(dx: CGFloat) {
    host.dx = dx
    host.passive = dx > 0.5
    hosting.view.transform = CGAffineTransform(translationX: dx, y: 0)
  }

  /// 透明度：打断在途过渡并停在当前呈现值；`ms == 0` 立即生效，否则淡入淡出 `ms` 毫秒。
  /// alpha 为 0 时立即不可交互并收起菜单（不上报），过渡结束后隐藏。
  func show(alpha: CGFloat, ms: Double) {
    if let running = animator, running.state == .active {
      // 不走完：视图停在此刻的呈现值，下一段从那里出发
      running.stopAnimation(true)
    }
    animator = nil
    let visible = alpha > 0
    host.shown = visible
    if !visible { model.setOpen(nil, page: nil, report: false) }
    guard ms > 0 else {
      host.alpha = alpha
      host.isHidden = !visible
      return
    }
    host.isHidden = false
    let next = UIViewPropertyAnimator(duration: ms / 1000, curve: .easeOut) {
      self.host.alpha = alpha
    }
    next.addCompletion { [weak self, weak next] position in
      guard let self = self else { return }
      if position == .end { self.host.isHidden = !visible }
      if self.animator === next { self.animator = nil }
    }
    animator = next
    next.startAnimation()
  }

  /// 网页 / 调试钩子要求打开或收起菜单：不产生 menu 事件。系统视图菜单只能收起（没有程序化打开）。
  func menu(open: String?, page: String?) {
    // 不可见时不开（alpha 0 的页头上挂出菜单会整屏挡住网页）
    guard open == nil || host.shown else { return }
    model.setOpen(open, page: page, report: false)
  }

  func traceHits(_ onHit: @escaping (CGPoint, String) -> Void) {
    host.onHit = onHit
  }

  /// 屏幕中心与各件控件中心的触摸是否归原生层（与 hitTest 同一条规则；没有的件为 false）。
  func probe() -> HeaderProbeOut {
    func mid(_ r: CGRect) -> CGPoint { CGPoint(x: r.midX, y: r.midY) }
    guard let spec = model.spec else {
      return HeaderProbeOut(center: false, lead: false, title: false, trail: false)
    }
    return HeaderProbeOut(
      center: host.captures(mid(host.bounds)),
      lead: host.captures(mid(spec.lead.frame.rect.offsetBy(dx: host.dx, dy: 0))),
      title: spec.title.map { host.captures(mid($0.frame.rect.offsetBy(dx: host.dx, dy: 0))) }
        ?? false,
      trail: spec.trail.map { host.captures(mid($0.frame.rect.offsetBy(dx: host.dx, dy: 0))) }
        ?? false)
  }

  private func setModal(_ modal: Bool) {
    host.modal = modal
    // 菜单开着时读屏只看这一层（网页与底栏都不可达）
    host.accessibilityViewIsModal = modal
    if modal {
      host.superview?.bringSubviewToFront(host)
      UIImpactFeedbackGenerator(style: .light).impactOccurred()
    }
    UIAccessibility.post(notification: .screenChanged, argument: nil)
  }

  func remove() {
    if let running = animator, running.state == .active {
      running.stopAnimation(true)
    }
    animator = nil
    hosting.willMove(toParent: nil)
    hosting.view.removeFromSuperview()
    host.removeFromSuperview()
    hosting.removeFromParent()
  }
}
