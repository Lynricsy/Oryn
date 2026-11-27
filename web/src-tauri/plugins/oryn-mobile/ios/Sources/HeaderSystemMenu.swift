// 视图菜单（右上角视图钮）用系统菜单：iOS 26 的 UIMenu 本身就是 Liquid Glass，从按钮形变展开，行距、按压、
// 长按滑选都是系统的。按钮是 UIKit 的玻璃钮（UIButton.Configuration.glass），开合经 UIContextMenuInteraction 的回调
// 交给 HeaderModel 记账并上报网页。系统菜单没有公开的「程序化打开」：网页 / 调试钩子只能收起它。
import SwiftUI
import UIKit

/// 视图钮：菜单开合回调 + 右上角 8pt 提醒点（距上、右各 8，与 SwiftUI 的圆钮同款）。
@available(iOS 26.0, *)
final class HeaderMenuButton: UIButton {
  var onShown: (Bool) -> Void = { _ in }
  private let dotView = UIView()

  override init(frame: CGRect) {
    super.init(frame: frame)
    dotView.isUserInteractionEnabled = false
    dotView.layer.cornerRadius = 4
    dotView.isHidden = true
    addSubview(dotView)
  }

  required init?(coder: NSCoder) { nil }

  var dot: UIColor? {
    didSet {
      dotView.backgroundColor = dot
      dotView.isHidden = dot == nil
    }
  }

  override func layoutSubviews() {
    super.layoutSubviews()
    dotView.frame = CGRect(x: bounds.maxX - 16, y: 8, width: 8, height: 8)
    bringSubviewToFront(dotView)
  }

  override func contextMenuInteraction(
    _ interaction: UIContextMenuInteraction,
    willDisplayMenuFor configuration: UIContextMenuConfiguration,
    animator: (any UIContextMenuInteractionAnimating)?
  ) {
    super.contextMenuInteraction(interaction, willDisplayMenuFor: configuration, animator: animator)
    onShown(true)
  }

  override func contextMenuInteraction(
    _ interaction: UIContextMenuInteraction,
    willEndFor configuration: UIContextMenuConfiguration,
    animator: (any UIContextMenuInteractionAnimating)?
  ) {
    super.contextMenuInteraction(interaction, willEndFor: configuration, animator: animator)
    onShown(false)
  }
}

/// SwiftUI 里摆放视图钮；菜单只在内容（或线程、明暗、调色板）变了时重建，开着时让系统原地换。
@available(iOS 26.0, *)
struct TrailMenuButton: UIViewRepresentable {
  let model: HeaderModel
  /// 菜单所属线程（spec.key）：构建时捕获进每个 action，换线程后迟到的选中不会算到新线程头上。
  let threadKey: String
  let trail: HeaderTrailSpec
  let palette: HeaderPalette
  let paletteSpec: HeaderPaletteSpec
  let dark: Bool

  struct MenuKey: Equatable {
    let thread: String
    let menu: HeaderMenuSpec
    let palette: HeaderPaletteSpec
    let dark: Bool
    /// 位图图标的倍率（SwiftUI 环境的 displayScale；换屏或倍率变了要重画）
    let scale: CGFloat
  }

  final class Coordinator {
    var key: MenuKey?
    var symbol: String?
  }

  func makeCoordinator() -> Coordinator { Coordinator() }

  func makeUIView(context: Context) -> HeaderMenuButton {
    let button = HeaderMenuButton(frame: .zero)
    button.showsMenuAsPrimaryAction = true
    let model = self.model
    button.onShown = { [weak model] shown in model?.trailShown(shown) }
    model.dismissTrail = { [weak button] in button?.contextMenuInteraction?.dismissMenu() }
    return button
  }

  func updateUIView(_ button: HeaderMenuButton, context: Context) {
    let coordinator = context.coordinator
    if coordinator.symbol != trail.symbol {
      coordinator.symbol = trail.symbol
      var config = UIButton.Configuration.glass()
      config.image = UIImage(systemName: safeSymbol(trail.symbol))
      config.preferredSymbolConfigurationForImage = UIImage.SymbolConfiguration(pointSize: 19)
      config.baseForegroundColor = .label
      config.cornerStyle = .capsule
      config.contentInsets = .zero
      button.configuration = config
    }
    button.accessibilityLabel = trail.label
    button.dot = palette.dot(trail.dot).map { UIColor($0) }

    let scale = context.environment.displayScale
    let key = MenuKey(
      thread: threadKey, menu: trail.menu, palette: paletteSpec, dark: dark, scale: scale)
    guard coordinator.key != key else { return }
    coordinator.key = key
    let model = self.model
    let thread = threadKey
    let menu = HeaderSystemMenu.build(trail.menu, palette: palette, dark: dark, scale: scale) {
      [weak model] item in
      model?.chooseTrail(item, key: thread)
    }
    button.menu = menu
    if model.open == "trail" {
      button.contextMenuInteraction?.updateVisibleMenu { _ in menu }
    }
  }
}

/// 菜单 spec → UIMenu：宫格为中号三格横排，状态块与各组为内联分节；子项为系统子菜单。
/// 说明行（sub）与提示（hint）并作副标题；打勾 = state .on；危险 = destructive。
@available(iOS 26.0, *)
@MainActor
enum HeaderSystemMenu {
  static func build(
    _ menu: HeaderMenuSpec, palette: HeaderPalette, dark: Bool, scale: CGFloat,
    choose: @escaping (HeaderItemSpec) -> Void
  ) -> UIMenu {
    func element(_ item: HeaderItemSpec) -> UIMenuElement {
      let image = icon(item, palette: palette, dark: dark, scale: scale)
      let subtitle = [item.sub?.text, item.hint].compactMap { $0 }.joined(separator: " · ")
      if let kids = item.children, !kids.isEmpty {
        let sub = UIMenu(title: item.label, image: image, children: kids.map(element))
        sub.subtitle = subtitle.isEmpty ? nil : subtitle
        return sub
      }
      let action = UIAction(title: item.label, image: image) { _ in choose(item) }
      action.subtitle = subtitle.isEmpty ? nil : subtitle
      if item.checked == true { action.state = .on }
      var attributes: UIMenuElement.Attributes = []
      if item.danger == true { attributes.insert(.destructive) }
      if item.disabled == true { attributes.insert(.disabled) }
      action.attributes = attributes
      return action
    }
    func section(_ items: [HeaderItemSpec], size: UIMenu.ElementSize = .large) -> UIMenu {
      let section = UIMenu(options: .displayInline, children: items.map(element))
      section.preferredElementSize = size
      return section
    }
    var sections: [UIMenu] = []
    if let quick = menu.quick, !quick.isEmpty { sections.append(section(quick, size: .medium)) }
    if let status = menu.status, !status.isEmpty { sections.append(section(status)) }
    sections += menu.groups.filter { !$0.isEmpty }.map { section($0) }
    let root = UIMenu(children: sections)
    root.accessibilityLabel = menu.label
    return root
  }

  /// 纯 SF Symbol 交给系统按菜单的字色着色；带提醒点、或是字形 / 模板图 / 状态珠 / 色点的，
  /// 用 SwiftUI 画成位图（按传入的明暗解析颜色、按传入的倍率出图，保留原色）。
  /// 倍率由调用方从 SwiftUI 环境显式给：UITraitCollection.current 在 UIKit 指定回调之外未定义。
  static func icon(
    _ item: HeaderItemSpec, palette: HeaderPalette, dark: Bool, scale: CGFloat
  ) -> UIImage? {
    let dot = palette.dot(item.dot)
    if dot == nil {
      guard let icon = item.icon else { return nil }
      if icon.orb == nil, icon.dot == nil, icon.glyph == nil, icon.mask == nil {
        return UIImage(systemName: safeSymbol(icon.symbol))
      }
    }
    let renderer = ImageRenderer(
      content: MenuIconImage(icon: item.icon, dot: dot, palette: palette)
        .environment(\.colorScheme, dark ? .dark : .light)
    )
    renderer.scale = scale
    return renderer.uiImage?.withRenderingMode(.alwaysOriginal)
  }
}

/// 菜单项图标的位图版：24pt 方格，图形 20pt 居中，提醒点 7pt 贴右上角。
@available(iOS 26.0, *)
private struct MenuIconImage: View {
  let icon: HeaderIconSpec?
  let dot: Color?
  let palette: HeaderPalette

  var body: some View {
    ZStack(alignment: .topTrailing) {
      Group {
        if let icon = icon {
          HeaderIconView(icon: icon, box: 20, palette: palette).foregroundStyle(.primary)
        } else {
          Color.clear.frame(width: 20, height: 20)
        }
      }
      .frame(width: 24, height: 24)
      if let dot = dot {
        Circle().fill(dot).frame(width: 7, height: 7)
      }
    }
    .frame(width: 24, height: 24)
  }
}
