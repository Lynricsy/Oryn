// 下沉侧栏的原生底栏（iOS 26+）：系统 UITabBar，垫在 WKWebView 之下。
// 为什么是系统 UITabBar：按住冒出的大液珠、按住横滑换项都是 UITabBar 私有的选中透镜，SwiftUI 的 glassEffect 只有材质与按压反馈。
// 为什么垫在下面：卡片、菜单、弹层都是网页画的，原生视图叠在 WebView 上面时只能靠桥送来的揭示边去模拟「在卡片之下」，
// 两个进程各自插值，边缘永远对不齐；垫在下面后由网页合成器决定哪里透出底栏，逐像素对齐，开合不需要原生做任何动画。
// 代价：页面在接管期间要透明（网页把 body / 下沉根节点的底色交给这里的垫底，同一个 --bg），侧栏列表在底栏那一条渐隐，
// 玻璃只折射垫底色，不折射列表。
// 触摸：WebView 在上面，底栏收不到触摸；在 WebView 之上放一块只盖住底栏矩形的转交层，底栏露出且没被网页内容压住时
// 把落在里面的触摸交给 UITabBar（透镜的按住 / 横滑都靠它），其余时候放行给网页。
// 选中项只听网页的（当前分区 = 路由）：系统点选后会先自己换选中，网页回发的 spec 再确认（或改回）。
import UIKit
import WebKit

/// 底栏矩形之上的触摸转交层：开着时把触摸交给 UITabBar，关着时放行给网页。
final class TabBarPortal: UIView {
  weak var target: UIView?
  var enabled = false

  override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
    guard enabled, let target = target, self.point(inside: point, with: event) else { return nil }
    let p = convert(point, to: target)
    return target.hitTest(p, with: event) ?? target
  }
}

@available(iOS 26.0, *)
final class TabBarUnderlay: NSObject, UITabBarDelegate {
  /// 转交层比底栏四周多出的边：透镜按住时会鼓出底栏之外，手指跟着出去也不该断。
  static let portalSlop: CGFloat = 12

  private let base = UIView()
  private let bar = UITabBar()
  private let portal = TabBarPortal()
  private weak var webview: WKWebView?
  private let onTap: (String) -> Void
  private var spec: TabBarSpec?
  /// 已解码的头像：项 id → (base64, 图)；base64 没变就不重新解码。
  private var avatars: [String: (String, UIImage)] = [:]
  /// 卡片左缘（视口 x）与遮挡：只决定转交层开不开。
  private var revealX: CGFloat = 0
  private var covered = false
  /// 接管前 WebView 的不透明设置，撤掉时还原。
  private let wasOpaque: Bool
  private let wasBackground: UIColor?
  private let wasScrollBackground: UIColor?

  init(webview: WKWebView, onTap: @escaping (String) -> Void) {
    self.webview = webview
    self.onTap = onTap
    wasOpaque = webview.isOpaque
    wasBackground = webview.backgroundColor
    wasScrollBackground = webview.scrollView.backgroundColor
    super.init()
    bar.delegate = self
    base.addSubview(bar)
    portal.target = bar
    portal.backgroundColor = .clear
    if let parent = webview.superview {
      base.frame = parent.bounds
      base.autoresizingMask = [.flexibleWidth, .flexibleHeight]
      parent.insertSubview(base, belowSubview: webview)
      parent.insertSubview(portal, aboveSubview: webview)
    }
    // 页面透明的地方要透出垫底
    webview.isOpaque = false
    webview.backgroundColor = .clear
    webview.scrollView.backgroundColor = .clear
  }

  func apply(_ next: TabBarSpec) {
    let style: UIUserInterfaceStyle = next.dark ? .dark : .light
    base.overrideUserInterfaceStyle = style
    base.backgroundColor = hexUIColor(next.bg) ?? .systemBackground
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

  /// 底栏按网页胶囊的矩形摆：同宽、竖直居中，高度取系统给的。
  private func layout(_ slot: CGRect) {
    guard let webview = webview else { return }
    let origin = webview.frame.origin
    let fit = bar.sizeThatFits(CGSize(width: slot.width, height: .greatestFiniteMagnitude))
    let height = max(fit.height, slot.height)
    let rect = CGRect(
      x: origin.x + slot.minX, y: origin.y + slot.midY - height / 2, width: slot.width,
      height: height)
    if bar.frame != rect { bar.frame = rect }
    let hit = rect.insetBy(dx: -Self.portalSlop, dy: -Self.portalSlop)
    if portal.frame != hit { portal.frame = hit }
    updateTouch()
  }

  /// 卡片左缘越过底栏右缘、且网页内容没压在底栏上时，转交层才把触摸交给底栏。
  func reveal(x: CGFloat, covered: Bool) {
    revealX = x
    self.covered = covered
    updateTouch()
  }

  private func updateTouch() {
    let edge = revealX + (webview?.frame.minX ?? 0)
    portal.enabled = spec != nil && !covered && edge >= bar.frame.maxX - 0.5
  }

  func tabBar(_ tabBar: UITabBar, didSelect item: UITabBarItem) {
    guard let spec = spec, spec.items.indices.contains(item.tag) else { return }
    onTap(spec.items[item.tag].id)
  }

  func remove() {
    portal.removeFromSuperview()
    base.removeFromSuperview()
    if let webview = webview {
      webview.isOpaque = wasOpaque
      webview.backgroundColor = wasBackground
      webview.scrollView.backgroundColor = wasScrollBackground
    }
  }
}
