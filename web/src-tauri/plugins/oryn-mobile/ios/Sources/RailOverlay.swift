// iPhone Duo 原生竖栏（iOS 27.1+）：合上的外屏、展开横放的内屏上，系统把状态栏与相机竖排在一侧，Oryn 的页头按钮与分区
// 也竖排在同一列（HIG「Designing for iPhone Duo → Vertical controls」）。这里用 SwiftUI 的 Liquid Glass 画两根胶囊：
// 上面是页头按钮（侧栏开关 + 当前页的操作，有菜单的用系统菜单），下面是竖排的分区。
// 位置不经网页：RailProbe 算出竖栏矩形（以相机为中线、顶端让开状态栏）直接交给 `place`；网页只给内容（`apply`）。
// 宿主视图与竖栏矩形等大，只接落在两根胶囊上的触摸，胶囊之间与下面的空白放行给网页。
// 点按经 onTap 回传（part = lead / action / item / tab），选中什么由网页决定，回来的 spec 再确认（分区先在本地换上，免得等一个来回）。
import SwiftUI
import UIKit
import WebKit

/// 竖栏宿主：只截两根胶囊（本视图坐标）里的触摸。
final class RailHostView: UIView {
  var hitRects: [CGRect] = []
  /// 最近一次显隐的目标是可见；淡出一开始就为假，过渡途中不再截触摸。
  var shown = false

  override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
    guard shown, !isHidden, hitRects.contains(where: { $0.insetBy(dx: -4, dy: -4).contains(point) })
    else { return nil }
    return super.hitTest(point, with: event) ?? self
  }
}

@available(iOS 26.0, *)
final class RailModel: ObservableObject {
  @Published var spec: RailSpec?
  /// 本地先换上的分区（点下去立刻有反应）；下一份 spec 到了就以它为准。
  @Published var pendingTab: String?
  var onTap: (_ key: String, _ part: String, _ id: String?, _ item: String?) -> Void = { _, _, _, _ in }
  var onRects: ([CGRect]) -> Void = { _ in }
  /// 两根胶囊在宿主坐标里的矩形（上 / 下）。
  var topRect: CGRect = .zero { didSet { if topRect != oldValue { onRects([topRect, tabsRect]) } } }
  var tabsRect: CGRect = .zero { didSet { if tabsRect != oldValue { onRects([topRect, tabsRect]) } } }
  /// 菜单钮（弱引用）：换页或隐藏时收起开着的系统菜单。
  let menuButtons = NSHashTable<UIButton>.weakObjects()
  /// 已解码的头像：项 id → (base64, 图)；base64 没变就不重新解码。
  private var avatars: [String: (String, UIImage)] = [:]

  func update(_ next: RailSpec) {
    if spec?.key != next.key { dismissMenus() }
    pendingTab = nil
    if next != spec { spec = next }
  }

  func dismissMenus() {
    for button in menuButtons.allObjects { button.contextMenuInteraction?.dismissMenu() }
  }

  var selectedTab: String? { pendingTab ?? spec?.tabs.selected }

  func tap(_ part: String, id: String? = nil) {
    guard let key = spec?.key else { return }
    onTap(key, part, id, nil)
  }

  func tapTab(_ id: String) {
    guard let key = spec?.key else { return }
    if selectedTab != id {
      UISelectionFeedbackGenerator().selectionChanged()
      withAnimation(.spring(duration: 0.3, bounce: 0.15)) { pendingTab = id }
    }
    onTap(key, "tab", id, nil)
  }

  /// 系统菜单里选了一项。`key` 是菜单构建时的页面：已换页（收起后系统迟到的选中）时丢弃。
  func choose(action: String, item: HeaderItemSpec, key: String) {
    guard key == spec?.key else { return }
    // 经事件回到网页时已没有用户手势，剪贴板只能在这里写
    if let text = item.copy { UIPasteboard.general.string = text }
    onTap(key, "item", action, item.id)
  }

  func avatar(_ id: String, _ b64: String) -> UIImage? {
    if let hit = avatars[id], hit.0 == b64 { return hit.1 }
    guard let data = Data(base64Encoded: b64), let image = UIImage(data: data) else { return nil }
    avatars[id] = (b64, image)
    return image
  }
}

@available(iOS 26.0, *)
final class RailOverlay {
  static let fade: TimeInterval = 0.12

  private let host = RailHostView()
  private let model = RailModel()
  private let hosting: UIHostingController<RailRootView>
  private weak var webview: WKWebView?
  /// 竖栏矩形（WebView 坐标）；当前不竖排时为 nil。
  private var rect: CGRect?
  private var animator: UIViewPropertyAnimator?

  init(
    webview: WKWebView, controller: UIViewController?,
    onTap: @escaping (_ key: String, _ part: String, _ id: String?, _ item: String?) -> Void
  ) {
    let model = self.model
    self.webview = webview
    hosting = UIHostingController(rootView: RailRootView(model: model))
    hosting.view.backgroundColor = .clear
    // 矩形由 RailProbe 算好（已让开状态栏与相机），宿主控制器不再自己让安全区
    hosting.safeAreaRegions = []
    host.backgroundColor = .clear
    host.alpha = 0
    host.isHidden = true
    hosting.view.frame = host.bounds
    hosting.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
    controller?.addChild(hosting)
    host.addSubview(hosting.view)
    if let controller { hosting.didMove(toParent: controller) }
    if let parent = webview.superview {
      // 在 WebView 与原生底栏之上、原生页头之下（页头的标题菜单要整屏压住竖栏）
      if let header = parent.subviews.first(where: { $0 is HeaderHostView }) {
        parent.insertSubview(host, belowSubview: header)
      } else {
        parent.insertSubview(host, aboveSubview: parent.subviews.last(where: { $0 is TabBarClipView }) ?? webview)
      }
    } else {
      controller?.view.addSubview(host)
    }
    model.onTap = onTap
    model.onRects = { [weak host] rects in host?.hitRects = rects }
  }

  /// 换内容，返回宿主视图是否已在窗口里（网页据此才撤掉自己画的竖栏）。
  func apply(_ spec: RailSpec) -> Bool {
    let style: UIUserInterfaceStyle = spec.dark ? .dark : .light
    // 宿主控制器的特征继承自父控制器而不是父视图：两处都设
    hosting.overrideUserInterfaceStyle = style
    host.overrideUserInterfaceStyle = style
    model.update(spec)
    sync()
    return host.window != nil
  }

  /// RailProbe 的竖栏矩形（WebView 坐标）；nil = 当前不竖排。
  func place(_ next: CGRect?) {
    rect = next
    sync()
  }

  private func sync() {
    if let rect, let webview {
      let frame = rect.offsetBy(dx: webview.frame.minX, dy: webview.frame.minY)
      if host.frame != frame { host.frame = frame }
    }
    let visible = rect != nil && model.spec.map { !$0.covered } == true
    if !visible { model.dismissMenus() }
    show(visible)
  }

  /// 显隐：出现时淡入、消失时立即不接触摸再淡出；打断在途过渡，从当前呈现值出发。
  private func show(_ visible: Bool) {
    guard host.shown != visible || (visible && host.isHidden) else { return }
    if let running = animator, running.state == .active { running.stopAnimation(true) }
    animator = nil
    host.shown = visible
    host.isHidden = false
    let next = UIViewPropertyAnimator(duration: Self.fade, curve: .easeOut) {
      self.host.alpha = visible ? 1 : 0
    }
    next.addCompletion { [weak self, weak next] position in
      guard let self else { return }
      if position == .end { self.host.isHidden = !visible }
      if self.animator === next { self.animator = nil }
    }
    animator = next
    next.startAnimation()
  }

  func remove() {
    if let running = animator, running.state == .active { running.stopAnimation(true) }
    animator = nil
    model.dismissMenus()
    hosting.willMove(toParent: nil)
    hosting.view.removeFromSuperview()
    host.removeFromSuperview()
    hosting.removeFromParent()
  }
}

@available(iOS 26.0, *)
struct RailRootView: View {
  @ObservedObject var model: RailModel
  @Namespace private var selection

  /// 胶囊宽 = 48pt 的钮 + 两侧各 4pt；竖栏宽 64，两边各留 4。
  static let button: CGFloat = 48
  static let inset: CGFloat = 4
  static let space = "rail"

  var body: some View {
    GeometryReader { geo in
      if let spec = model.spec {
        content(spec)
          .frame(width: geo.size.width, height: geo.size.height, alignment: .top)
      }
    }
    .ignoresSafeArea()
    .coordinateSpace(.named(Self.space))
  }

  private func content(_ spec: RailSpec) -> some View {
    let palette = HeaderPalette(spec.palette)
    return ScrollView(.vertical, showsIndicators: false) {
      VStack(spacing: 12) {
        top(spec, palette: palette)
          .onGeometryChange(for: CGRect.self) { $0.frame(in: .named(Self.space)) } action: {
            model.topRect = $0
          }
        tabs(spec, palette: palette)
          .onGeometryChange(for: CGRect.self) { $0.frame(in: .named(Self.space)) } action: {
            model.tabsRect = $0
          }
      }
      .frame(maxWidth: .infinity)
    }
    .scrollBounceBehavior(.basedOnSize)
  }

  /// 页头按钮：侧栏开关 + 当前页的操作，同一根玻璃胶囊。
  private func top(_ spec: RailSpec, palette: HeaderPalette) -> some View {
    VStack(spacing: 0) {
      RailButton(
        symbol: "sidebar.left", label: spec.lead.label, active: spec.lead.active, dot: nil,
        accent: palette.accent
      ) { model.tap("lead") }
      ForEach(spec.actions) { action in
        if let menu = action.menu {
          RailMenuButton(
            model: model, key: spec.key, action: action, menu: menu, palette: palette,
            paletteSpec: spec.palette, dark: spec.dark
          )
          .frame(width: Self.button, height: Self.button)
        } else {
          RailButton(
            symbol: action.symbol, label: action.label, active: action.active == true,
            dot: palette.dot(action.dot), accent: palette.accent
          ) { model.tap("action", id: action.id) }
        }
      }
    }
    .padding(Self.inset)
    .glassEffect(.regular.interactive(), in: Capsule())
  }

  /// 竖排的分区：选中项垫一块浅色药丸（换项时滑过去），图标与文字取强调色。
  private func tabs(_ spec: RailSpec, palette: HeaderPalette) -> some View {
    let selected = model.selectedTab
    return VStack(spacing: 2) {
      ForEach(spec.tabs.items, id: \.id) { item in
        RailTab(
          model: model, item: item, picked: item.id == selected, palette: palette, dark: spec.dark,
          selection: selection)
      }
    }
    .padding(Self.inset)
    .glassEffect(.regular.interactive(), in: RoundedRectangle(cornerRadius: 26, style: .continuous))
    .accessibilityElement(children: .contain)
    .accessibilityLabel(Text(spec.tabs.label))
  }
}

/// 一项分区：图标 + 10pt 文字；选中项垫一块浅色药丸（matchedGeometryEffect：换项时滑过去），图标与文字取强调色。
@available(iOS 26.0, *)
private struct RailTab: View {
  let model: RailModel
  let item: TabBarItemSpec
  let picked: Bool
  let palette: HeaderPalette
  let dark: Bool
  let selection: Namespace.ID

  var body: some View {
    Button { model.tapTab(item.id) } label: {
      VStack(spacing: 3) {
        RailTabIcon(model: model, item: item, selected: picked, accent: palette.accent)
          .overlay(alignment: .topTrailing) { badge }
        Text(item.label)
          .font(.system(size: 10, weight: picked ? .semibold : .medium))
          .lineLimit(1)
          .minimumScaleFactor(0.8)
      }
      .foregroundStyle(tint)
      .frame(width: RailRootView.button, height: 54)
      .background { pill }
      .contentShape(RoundedRectangle(cornerRadius: 22, style: .continuous))
    }
    .buttonStyle(RailPressStyle())
    .accessibilityLabel(Text(item.label))
    .accessibilityAddTraits(picked ? .isSelected : [])
  }

  private var tint: AnyShapeStyle {
    picked ? AnyShapeStyle(palette.accent) : AnyShapeStyle(HierarchicalShapeStyle.primary)
  }

  @ViewBuilder private var badge: some View {
    if let kind = item.badge {
      Circle()
        .fill(kind == "err" ? palette.err : kind == "warn" ? palette.warn : palette.accent)
        .frame(width: 8, height: 8)
        .offset(x: 3, y: -2)
    }
  }

  @ViewBuilder private var pill: some View {
    if picked {
      RoundedRectangle(cornerRadius: 22, style: .continuous)
        .fill(Color.primary.opacity(dark ? 0.14 : 0.07))
        .matchedGeometryEffect(id: "selected", in: selection)
    }
  }
}

/// 按下时缩一点（玻璃胶囊本身的交互高光由 glassEffect 负责）。
@available(iOS 26.0, *)
private struct RailPressStyle: ButtonStyle {
  func makeBody(configuration: Configuration) -> some View {
    configuration.label
      .scaleEffect(configuration.isPressed ? 0.9 : 1)
      .animation(.spring(duration: 0.2), value: configuration.isPressed)
  }
}

/// 48pt 圆钮：19pt 图标；开着的（侧栏开着、面板开着）垫强调色淡底；右上角可带 8pt 提醒点。
@available(iOS 26.0, *)
private struct RailButton: View {
  let symbol: String
  let label: String
  let active: Bool
  let dot: Color?
  let accent: Color
  let action: () -> Void

  var body: some View {
    Button(action: action) {
      Image(systemName: safeSymbol(symbol))
        .font(.system(size: 19))
        .foregroundStyle(
          active ? AnyShapeStyle(accent) : AnyShapeStyle(HierarchicalShapeStyle.primary))
        .frame(width: RailRootView.button, height: RailRootView.button)
        .background {
          if active { Circle().fill(accent.opacity(0.16)) }
        }
        .overlay(alignment: .topTrailing) {
          if let dot { Circle().fill(dot).frame(width: 8, height: 8).padding(9) }
        }
        .contentShape(Circle())
    }
    .buttonStyle(RailPressStyle())
    .accessibilityLabel(Text(label))
    .accessibilityAddTraits(active ? .isSelected : [])
  }
}

/// 分区图标：头像（圆形）、带底色的单个字形、SF Symbol（选中用 .fill 变体，没有就同一个）。
@available(iOS 26.0, *)
private struct RailTabIcon: View {
  let model: RailModel
  let item: TabBarItemSpec
  let selected: Bool
  let accent: Color

  var body: some View {
    Group {
      if let b64 = item.icon.image, let image = model.avatar(item.id, b64) {
        Image(uiImage: image).resizable().scaledToFill().clipShape(Circle())
      } else if let glyph = item.icon.glyph {
        Text(glyph)
          .font(.system(size: 14))
          .frame(maxWidth: .infinity, maxHeight: .infinity)
          .background(Circle().fill(accent.opacity(0.14)))
      } else {
        let name = safeSymbol(item.icon.symbol)
        let fill = name + ".fill"
        Image(systemName: selected && UIImage(systemName: fill) != nil ? fill : name)
          .font(.system(size: 20))
      }
    }
    .frame(width: 24, height: 24)
  }
}

/// 有菜单的操作钮：系统菜单（UIMenu 本身是 Liquid Glass，从钮里形变展开）。钮本身是无底的 UIButton，
/// 玻璃由外面的胶囊给（不叠第二层玻璃）。菜单只在内容（或页面、明暗、调色板、倍率）变了时重建。
@available(iOS 26.0, *)
private struct RailMenuButton: UIViewRepresentable {
  let model: RailModel
  let key: String
  let action: RailActionSpec
  let menu: HeaderMenuSpec
  let palette: HeaderPalette
  let paletteSpec: HeaderPaletteSpec
  let dark: Bool

  struct MenuKey: Equatable {
    let key: String
    let action: String
    let menu: HeaderMenuSpec
    let palette: HeaderPaletteSpec
    let dark: Bool
    let scale: CGFloat
  }

  final class Coordinator {
    var key: MenuKey?
  }

  func makeCoordinator() -> Coordinator { Coordinator() }

  func makeUIView(context: Context) -> HeaderMenuButton {
    let button = HeaderMenuButton(frame: .zero)
    button.showsMenuAsPrimaryAction = true
    model.menuButtons.add(button)
    return button
  }

  func updateUIView(_ button: HeaderMenuButton, context: Context) {
    var config = UIButton.Configuration.plain()
    config.image = UIImage(systemName: safeSymbol(action.symbol))
    config.preferredSymbolConfigurationForImage = UIImage.SymbolConfiguration(pointSize: 19)
    config.baseForegroundColor = action.active == true ? UIColor(palette.accent) : .label
    config.cornerStyle = .capsule
    config.contentInsets = .zero
    if action.active == true {
      config.background.backgroundColor = UIColor(palette.accent).withAlphaComponent(0.16)
    }
    button.configuration = config
    button.accessibilityLabel = action.label
    button.dot = palette.dot(action.dot).map { UIColor($0) }

    let scale = context.environment.displayScale
    let next = MenuKey(
      key: key, action: action.id, menu: menu, palette: paletteSpec, dark: dark, scale: scale)
    let coordinator = context.coordinator
    guard coordinator.key != next else { return }
    coordinator.key = next
    let model = self.model
    let page = key
    let id = action.id
    button.menu = HeaderSystemMenu.build(menu, palette: palette, dark: dark, scale: scale) {
      [weak model] item in
      model?.choose(action: id, item: item, key: page)
    }
  }
}
