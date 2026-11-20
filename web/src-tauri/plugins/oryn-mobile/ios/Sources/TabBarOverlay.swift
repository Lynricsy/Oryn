// 下沉侧栏的原生底栏（iOS 26+）：系统 UITabBar，浮在 WKWebView 之上。
// 为什么是系统 UITabBar：按住冒出的大液珠、按住横滑换项都是 UITabBar 私有的选中透镜，SwiftUI 的 glassEffect 只有材质与按压反馈。
// 为什么浮在上面：玻璃只折射它下面真实的内容；浮在 WebView 之上，侧栏列表一直画到屏幕底边、从玻璃后面透过来。
// 底栏属于侧栏层，卡片盖过来的部分要藏起来：底栏装在一个裁切视图里，裁切视图的右缘 = 卡片左缘（网页给的 `x`）。
// 吸附时按网页给的时长、缓动与绝对起步时刻自己插值（与卡片的 CSS 过渡同一时刻出发），拖动时逐帧直接就位；
// 网页内容（菜单、弹层、提示、软键盘）压在底栏位置上时整块隐藏、不接触摸，网页胶囊顶上。
// 选中项只听网页的（当前分区 = 路由）：系统点选后会先自己换选中，网页回发的 spec 再确认（或改回）。
import UIKit
import WebKit

/// 裁切视图：只把落在底栏（及按住时鼓出的透镜）上的触摸留下，空白处放行给网页。
final class TabBarClipView: UIView {
  override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
    let hit = super.hitTest(point, with: event)
    return hit === self ? nil : hit
  }
}

@available(iOS 26.0, *)
final class TabBarOverlay: NSObject, UITabBarDelegate {
  /// 裁切视图比底栏上下多出的边：按住时透镜会鼓出底栏，不该被切掉。
  static let slop: CGFloat = 12
  /// 缺省缓动（= 网页 `--ease`）。
  static let defaultEase: [Double] = [0.2, 0.8, 0.2, 1]
  /// 让开遮挡后淡回的时长（= 网页 `UNCOVER_MS`）。
  static let fadeIn: TimeInterval = 0.12

  private let clip = TabBarClipView()
  private let bar = UITabBar()
  private weak var webview: WKWebView?
  private let onTap: (String) -> Void
  private var spec: TabBarSpec?
  /// 已解码的头像：项 id → (base64, 图)；base64 没变就不重新解码。
  private var avatars: [String: (String, UIImage)] = [:]
  /// 底栏在 WebView 坐标里的矩形（高度取系统的）。
  private var slot: CGRect = .zero
  /// 裁切右缘的目标（WebView 坐标 x = 卡片左缘）。
  private var edge: CGFloat = 0
  private var covered = false
  private var animator: UIViewPropertyAnimator?

  init(webview: WKWebView, onTap: @escaping (String) -> Void) {
    self.webview = webview
    self.onTap = onTap
    super.init()
    bar.delegate = self
    // 四项撑满网页给的宽度：系统缺省按内容收窄成居中的小胶囊
    bar.itemPositioning = .fill
    clip.clipsToBounds = true
    clip.backgroundColor = .clear
    clip.addSubview(bar)
    if let parent = webview.superview {
      // 紧贴在 WebView 之上、原生页头之下（页头的菜单要整屏压住底栏）
      if let header = parent.subviews.first(where: { $0 is HeaderHostView }) {
        parent.insertSubview(clip, belowSubview: header)
      } else {
        parent.insertSubview(clip, aboveSubview: webview)
      }
    }
  }

  func apply(_ next: TabBarSpec) {
    let style: UIUserInterfaceStyle = next.dark ? .dark : .light
    clip.overrideUserInterfaceStyle = style
    bar.tintColor = hexUIColor(next.accent)
    syncItems(next)
    spec = next
    layout(next.frame.rect)
  }

  /// 同一组项只改内容（不重建，选中透镜不会跳回）；项的组成变了才整组换。
  private func syncItems(_ next: TabBarSpec) {
    let accent = hexUIColor(next.accent) ?? .tintColor
    let current = bar.items ?? []
    let sameIds = current.count == next.items.count && spec?.items.map(\.id) == next.items.map(\.id)
    let items: [UITabBarItem]
    if sameIds {
      items = current
    } else {
      items = next.items.indices.map { i in
        let item = UITabBarItem()
        item.tag = i
        return item
      }
    }
    for (item, s) in zip(items, next.items) {
      if item.title != s.label { item.title = s.label }
      let (image, selected) = images(s, accent: accent)
      item.image = image
      item.selectedImage = selected
      item.accessibilityLabel = s.label
      if let kind = s.badge {
        // 空串 = 不带数字的小圆点
        item.badgeValue = ""
        item.badgeColor = kind == "err" ? .systemRed : kind == "warn" ? .systemOrange : accent
      } else {
        item.badgeValue = nil
      }
    }
    if !sameIds { bar.setItems(items, animated: false) }
    let target = next.items.firstIndex { $0.id == next.selected }.map { items[$0] }
    if bar.selectedItem !== target { bar.selectedItem = target }
  }

  /// 未选中 / 选中两张图：SF Symbol（选中用 .fill 变体，没有就同一张）、圆形头像、带底色的单个字形。
  private func images(_ s: TabBarItemSpec, accent: UIColor) -> (UIImage?, UIImage?) {
    if let b64 = s.icon.image, let avatar = avatar(s.id, b64) {
      return (avatar, avatar)
    }
    if let glyph = s.icon.glyph {
      let image = glyphImage(glyph, tint: accent)
      return (image, image)
    }
    let name = safeSymbol(s.icon.symbol)
    let plain = UIImage(systemName: name)
    return (plain, UIImage(systemName: name + ".fill") ?? plain)
  }

  private func avatar(_ id: String, _ b64: String) -> UIImage? {
    if let hit = avatars[id], hit.0 == b64 { return hit.1 }
    guard let data = Data(base64Encoded: b64), let source = UIImage(data: data) else { return nil }
    let side: CGFloat = 24
    let image = UIGraphicsImageRenderer(size: CGSize(width: side, height: side)).image { _ in
      UIBezierPath(ovalIn: CGRect(x: 0, y: 0, width: side, height: side)).addClip()
      source.draw(in: CGRect(x: 0, y: 0, width: side, height: side))
    }.withRenderingMode(.alwaysOriginal)
    avatars[id] = (b64, image)
    return image
  }

  private func glyphImage(_ glyph: String, tint: UIColor) -> UIImage {
    let side: CGFloat = 24
    return UIGraphicsImageRenderer(size: CGSize(width: side, height: side)).image { _ in
      tint.withAlphaComponent(0.14).setFill()
      UIBezierPath(ovalIn: CGRect(x: 0, y: 0, width: side, height: side)).fill()
      let text = NSAttributedString(string: glyph, attributes: [.font: UIFont.systemFont(ofSize: 14)])
      let size = text.size()
      text.draw(at: CGPoint(x: (side - size.width) / 2, y: (side - size.height) / 2))
    }.withRenderingMode(.alwaysOriginal)
  }

  /// 底栏按网页胶囊的矩形摆：同左右、竖直居中，高度取系统给的。
  private func layout(_ rect: CGRect) {
    let fit = bar.sizeThatFits(CGSize(width: rect.width, height: .greatestFiniteMagnitude))
    let height = max(fit.height, rect.height)
    slot = CGRect(x: rect.minX, y: rect.midY - height / 2, width: rect.width, height: height)
    let inClip = CGRect(x: slot.minX, y: Self.slop, width: slot.width, height: slot.height)
    if bar.frame != inClip { bar.frame = inClip }
    // 在途的裁切过渡不打断：只有静止时才把裁切视图挪到新位置
    if animator == nil { clip.frame = clipFrame(edge) }
  }

  /// 裁切视图：从 WebView 左缘到卡片左缘，上下各多出 `slop`。
  private func clipFrame(_ x: CGFloat) -> CGRect {
    let origin = webview?.frame.origin ?? .zero
    return CGRect(
      x: origin.x, y: origin.y + slot.minY - Self.slop, width: max(0, x),
      height: slot.height + Self.slop * 2)
  }

  /// 卡片左缘与遮挡。`ms == 0` 立即就位（拖动逐帧、减少动效）；否则等 `delay` 后按 `ease` 过渡 `ms` 毫秒。
  /// 只换遮挡（左缘目标没变、`ms == 0`）时不打断在途的过渡。
  /// `overdue`：消息到时已过了起步时刻。收窄（关侧栏）时卡片已经盖过来一段，再从头走完整过渡会让底栏画到卡片上，
  /// 所以直接裁到终点（宁可早藏）；放宽（开侧栏）晚起步只会多藏一点，照常过渡。
  func reveal(
    x: CGFloat, covered: Bool, ms rawMs: Double, delay: Double, overdue: Bool, ease: [Double]?
  ) {
    setCovered(covered)
    let ms = overdue && x < edge - 0.5 ? 0 : rawMs
    let sameTarget = abs(x - edge) < 0.5
    if sameTarget && ms == 0 && animator != nil { return }
    edge = x
    if let running = animator, running.state == .active {
      // 不走完：停在此刻的呈现值，下一段从那里出发
      running.stopAnimation(true)
    }
    animator = nil
    guard ms > 0 else {
      clip.frame = clipFrame(x)
      updateTouch()
      return
    }
    let e = ease.flatMap { $0.count == 4 ? $0 : nil } ?? Self.defaultEase
    let timing = UICubicTimingParameters(
      controlPoint1: CGPoint(x: e[0], y: e[1]), controlPoint2: CGPoint(x: e[2], y: e[3]))
    let next = UIViewPropertyAnimator(duration: ms / 1000, timingParameters: timing)
    let target = clipFrame(x)
    next.addAnimations { self.clip.frame = target }
    next.addCompletion { [weak self, weak next] _ in
      guard let self = self, self.animator === next else { return }
      self.animator = nil
      self.updateTouch()
    }
    animator = next
    // 过渡途中不接触摸：命中按终点算，卡片还压在底栏上的那段会被底栏吃掉点击
    updateTouch()
    next.startAnimation(afterDelay: delay / 1000)
  }

  private func setCovered(_ next: Bool) {
    guard next != covered else { return }
    covered = next
    if next {
      clip.alpha = 0
    } else {
      UIView.animate(withDuration: Self.fadeIn) { self.clip.alpha = 1 }
    }
    updateTouch()
  }

  private func updateTouch() {
    clip.isUserInteractionEnabled = spec != nil && !covered && animator == nil
  }

  func tabBar(_ tabBar: UITabBar, didSelect item: UITabBarItem) {
    guard let spec = spec, spec.items.indices.contains(item.tag) else { return }
    onTap(spec.items[item.tag].id)
  }

  func remove() {
    animator?.stopAnimation(true)
    animator = nil
    clip.removeFromSuperview()
  }
}
