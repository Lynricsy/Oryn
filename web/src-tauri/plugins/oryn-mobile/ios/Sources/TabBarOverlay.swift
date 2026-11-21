// 下沉侧栏的原生底栏（iOS 26+）：系统 UITabBar，浮在 WKWebView 之上。
// 为什么是系统 UITabBar：按住冒出的大液珠、按住横滑换项都是 UITabBar 私有的选中透镜，SwiftUI 的 glassEffect 只有材质与按压反馈。
// 为什么浮在上面：玻璃只折射它下面真实的内容；浮在 WebView 之上，侧栏列表一直画到屏幕底边、从玻璃后面透过来。
// 底栏属于侧栏层，卡片盖过来的部分要藏起来：底栏装在一个裁切视图里，裁切视图的右缘 = 卡片左缘。
// 卡片左缘来自 SinkFollow（根滚动的 contentOffset，follow）：在同一次 KVO 回调里直接改裁切宽度，与卡片同进程、同一帧，
// 不需要插值或对时。遮挡来自网页（cover）：网页内容（菜单、弹层、提示、软键盘）压在底栏位置上时整块隐藏、不接触摸，网页胶囊顶上。
// 选中项只听网页的（当前分区 = 路由）：系统点选后会先自己换选中，网页回发的 spec 再确认（或改回）。
import UIKit
import WebKit

/// 裁切视图：只把落在底栏胶囊（及按住时鼓出的透镜）上的触摸留下，空白处与胶囊两侧的留白放行给网页。
final class TabBarClipView: UIView {
  /// 起手能落在这里面（本视图坐标）：网页胶囊的矩形，上下含透镜的余量。
  var touchRect: CGRect = .zero

  override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
    guard touchRect.contains(point) else { return nil }
    let hit = super.hitTest(point, with: event)
    return hit === self ? nil : hit
  }
}

@available(iOS 26.0, *)
final class TabBarOverlay: NSObject, UITabBarDelegate {
  /// 裁切视图比底栏上下多出的边：按住时透镜会鼓出底栏，不该被切掉。
  static let slop: CGFloat = 12
  /// iOS 26 的 UITabBar 把玻璃胶囊画在自己边界内左右各缩进这么多（模拟器实测 21pt）；
  /// 底栏左右各放出这么多，看得见的胶囊才与网页胶囊同宽（四项撑满侧栏）。
  static let platterInset: CGFloat = 21
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
  /// 裁切右缘（WebView 坐标 x = 卡片左缘，SinkFollow 给）。
  private var edge: CGFloat = 0
  /// 网页内容压在底栏位置上（网页给）。
  private var covered = false

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

  /// 底栏按网页胶囊的矩形摆：看得见的玻璃胶囊与网页胶囊同左右（底栏本身左右各放出 `platterInset`），竖直居中，高度取系统给的。
  private func layout(_ rect: CGRect) {
    let fit = bar.sizeThatFits(CGSize(width: rect.width, height: .greatestFiniteMagnitude))
    let height = max(fit.height, rect.height)
    slot = CGRect(x: rect.minX, y: rect.midY - height / 2, width: rect.width, height: height)
    let inClip = CGRect(
      x: slot.minX - Self.platterInset, y: Self.slop, width: slot.width + Self.platterInset * 2,
      height: slot.height)
    if bar.frame != inClip { bar.frame = inClip }
    clip.touchRect = CGRect(x: slot.minX, y: 0, width: slot.width, height: slot.height + Self.slop * 2)
    clip.frame = clipFrame(edge)
    updateTouch()
  }

  /// 裁切视图：从 WebView 左缘到卡片左缘，上下各多出 `slop`。
  private func clipFrame(_ x: CGFloat) -> CGRect {
    let origin = webview?.frame.origin ?? .zero
    return CGRect(
      x: origin.x, y: origin.y + slot.minY - Self.slop, width: max(0, x),
      height: slot.height + Self.slop * 2)
  }

  /// 跟随卡片左缘（SinkFollow 的 dx）：裁切视图同步改宽，不带动画。
  func follow(dx: CGFloat) {
    edge = dx
    let frame = clipFrame(dx)
    if clip.frame != frame { clip.frame = frame }
    updateTouch()
  }

  /// 网页内容压没压在底栏上：压上立即隐藏，让开后淡回。
  func cover(_ next: Bool) {
    guard next != covered else { return }
    covered = next
    if next {
      clip.alpha = 0
    } else {
      UIView.animate(withDuration: Self.fadeIn) { self.clip.alpha = 1 }
    }
    updateTouch()
  }

  /// 卡片完全露出底栏胶囊、且网页内容没压在底栏上时才接触摸。
  private func updateTouch() {
    clip.isUserInteractionEnabled = spec != nil && !covered && edge >= slot.maxX - 0.5
  }

  func tabBar(_ tabBar: UITabBar, didSelect item: UITabBarItem) {
    guard let spec = spec, spec.items.indices.contains(item.tag) else { return }
    onTap(spec.items[item.tag].id)
  }

  func remove() {
    clip.removeFromSuperview()
  }
}
