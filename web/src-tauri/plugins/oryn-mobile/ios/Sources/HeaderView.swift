// 原生页头的 SwiftUI 部分：两枚 44pt 圆形玻璃钮 + 46pt 两行标题胶囊（首页只有侧栏钮）；位置全部用网页量好的矩形。
// 标题菜单是自绘面板（与胶囊同一个玻璃容器，从胶囊里形变长出来）；视图菜单是系统菜单（HeaderSystemMenu.swift）。
// 菜单开着哪一个、停在哪个子页是这里的本地状态（点了立刻有反应），打开 / 关闭 / 选中再上报给网页。
import SwiftUI
import UIKit
import ImageIO

final class HeaderModel: ObservableObject {
  @Published var spec: HeaderSpec?
  /// 开着的菜单："title" / "trail"（视图菜单由系统菜单的回调记账）。
  @Published var open: String?
  /// 子页：父项 id。只存 id，每次从最新的 spec 里取（线程状态变了子页内容跟着变）。
  @Published var page: String?
  var onEvent: (_ op: String, _ key: String, _ menu: String?, _ id: String?) -> Void = { _, _, _, _ in }
  /// 标题菜单从无到有 / 从有到无（宿主据此整屏接管、读屏模态）；系统视图菜单的触摸与读屏由系统接管。
  var onModal: (Bool) -> Void = { _ in }
  /// 收起系统视图菜单（视图钮装好时挂上）。
  var dismissTrail: () -> Void = {}

  /// 标题面板的出现 / 消失与子页换页：玻璃形变用弹簧；减弱动态效果时只淡入淡出。
  static var motion: Animation {
    UIAccessibility.isReduceMotionEnabled ? .linear(duration: 0.1) : .spring(duration: 0.35, bounce: 0.15)
  }

  func menu(_ kind: String?, in spec: HeaderSpec?) -> HeaderMenuSpec? {
    kind == "title" ? spec?.title?.menu : kind == "trail" ? spec?.trail?.menu : nil
  }

  var openMenu: HeaderMenuSpec? { menu(open, in: spec) }

  /// 当前子页的父项；不存在（项没了、不再有子项）时为 nil = 主页。
  var pageItem: HeaderItemSpec? {
    guard let page = page else { return nil }
    return openMenu?.tops.first { $0.id == page && !($0.children ?? []).isEmpty }
  }

  func update(_ next: HeaderSpec) {
    guard next != spec else { return }
    // 换了线程、或开着的菜单没了：收起（网页自己知道，不上报；系统菜单收起时的回调照常上报，网页按状态去重）
    if open != nil && (spec?.key != next.key || menu(open, in: next) == nil) {
      if open == "trail" {
        dismissTrail()
      } else {
        open = nil
        page = nil
        onModal(false)
      }
    }
    spec = next
    // 开着的子页的父项不在了（或不再有子项）：回主页
    if page != nil && pageItem == nil { page = nil }
  }

  func emit(_ op: String, menu: String? = nil, id: String? = nil) {
    guard let key = spec?.key else { return }
    onEvent(op, key, menu, id)
  }

  func setOpen(_ kind: String?, page next: String?, report: Bool) {
    // 视图菜单是系统菜单：没有程序化打开；开着时只能请系统收起，开合由 trailShown 记账上报
    if kind == "trail" { return }
    if open == "trail" {
      if kind == nil { dismissTrail() }
      return
    }
    let kind = menu(kind, in: spec) == nil ? nil : kind
    let next = kind == nil ? nil : next
    guard kind != open || next != page else { return }
    let was = open
    withAnimation(Self.motion) {
      open = kind
      page = next
    }
    if (was != nil) != (kind != nil) { onModal(kind != nil) }
    if report && was != kind { emit("menu", menu: kind) }
  }

  func toggle(_ kind: String) {
    setOpen(open == kind ? nil : kind, page: nil, report: true)
  }

  func choose(_ item: HeaderItemSpec) {
    guard item.disabled != true, let kind = open else { return }
    if let kids = item.children, !kids.isEmpty {
      // 原地换页
      withAnimation(Self.motion) { page = item.id }
      UIAccessibility.post(notification: .layoutChanged, argument: nil)
      return
    }
    // 经事件回到网页时已没有用户手势，剪贴板只能在这里写
    if let text = item.copy { UIPasteboard.general.string = text }
    UISelectionFeedbackGenerator().selectionChanged()
    // 选中即收起；网页收到 select 就知道菜单关了，不再另发 menu
    setOpen(nil, page: nil, report: false)
    emit("select", menu: kind, id: item.id)
  }

  /// 系统视图菜单开合（UIKit 回调）：照常上报网页。标题菜单开着时视图钮在关闭区之下，点不到。
  func trailShown(_ shown: Bool) {
    if shown {
      guard open == nil else { return }
      open = "trail"
      page = nil
      emit("menu", menu: "trail")
    } else {
      guard open == "trail" else { return }
      open = nil
      emit("menu", menu: nil)
    }
  }

  /// 选中系统视图菜单的一项：菜单由系统收起（另有 trailShown(false) 上报）。
  func chooseTrail(_ item: HeaderItemSpec) {
    // 经事件回到网页时已没有用户手势，剪贴板只能在这里写
    if let text = item.copy { UIPasteboard.general.string = text }
    emit("select", menu: "trail", id: item.id)
  }

  /// 子页 → 主页；主页 → 收起（上报）。没有菜单开着返回 false。
  func back() -> Bool {
    guard open != nil else { return false }
    if page != nil {
      withAnimation(Self.motion) { page = nil }
      UIAccessibility.post(notification: .layoutChanged, argument: nil)
    } else {
      setOpen(nil, page: nil, report: true)
    }
    return true
  }
}

/// 网页给的调色板；格式不对时退回系统色。
struct HeaderPalette {
  let accent: Color
  let brand: Color
  let err: Color
  let warn: Color
  let good: Color
  let frost: Color

  init(_ s: HeaderPaletteSpec) {
    accent = hexColor(s.accent) ?? .accentColor
    brand = hexColor(s.brand) ?? .accentColor
    err = hexColor(s.err) ?? .red
    warn = hexColor(s.warn) ?? .orange
    good = hexColor(s.good) ?? .green
    frost = hexColor(s.frost) ?? .blue
  }

  func dot(_ kind: String?) -> Color? {
    kind == "warn" ? warn : kind == "accent" ? accent : nil
  }
}

extension View {
  /// 按网页给的矩形摆放（父容器是与屏幕等大的 ZStack）。
  func place(_ f: HeaderFrameSpec) -> some View {
    frame(width: CGFloat(f.width), height: CGFloat(f.height))
      .position(x: CGFloat(f.x + f.width / 2), y: CGFloat(f.y + f.height / 2))
  }
}

/// SF Symbol 名不存在时退回 circle（网页的映射表写错不至于空白）。
func safeSymbol(_ name: String?) -> String {
  guard let name = name, UIImage(systemName: name) != nil else { return "circle" }
  return name
}

@available(iOS 26.0, *)
struct HeaderRootView: View {
  @ObservedObject var model: HeaderModel
  @Environment(\.accessibilityReduceMotion) private var reduce
  @Namespace private var glass

  /// 面板与胶囊的间距 = 玻璃容器的融合距离：出现 / 消失时从胶囊形变，静止时不粘连。
  private static let gap: CGFloat = 6

  var body: some View {
    GeometryReader { geo in
      if let spec = model.spec {
        root(spec, size: geo.size)
      }
    }
    .ignoresSafeArea()
  }

  private func root(_ spec: HeaderSpec, size: CGSize) -> some View {
    let palette = HeaderPalette(spec.palette)
    let maxHeight: CGFloat = max(120, min(480, size.height - 128))
    let titleOpen = model.open == "title"
    return ZStack(alignment: .topLeading) {
      // 1. 两枚圆钮：标题菜单开着时在关闭区之下（被压暗，读屏不可达）
      ZStack(alignment: .topLeading) {
        RoundGlassButton(symbol: spec.lead.symbol, label: spec.lead.label, dot: nil) {
          model.emit("lead")
        }
        .place(spec.lead.frame)
        if let trail = spec.trail {
          TrailMenuButton(
            model: model, trail: trail, palette: palette, paletteSpec: spec.palette, dark: spec.dark
          )
          .place(trail.frame)
        }
      }
      .accessibilityHidden(titleOpen)
      // 2. 标题菜单的关闭区：点一下收起并上报（系统视图菜单的点外收起由系统负责）
      if titleOpen, let menu = model.openMenu {
        Color.black.opacity(menu.dim ? spec.tune.scrim : 0.001)
          .contentShape(Rectangle())
          .onTapGesture { model.setOpen(nil, page: nil, report: true) }
          .accessibilityElement()
          .accessibilityLabel(Text(spec.text.close))
          .accessibilityAddTraits(.isButton)
          .transition(.opacity)
      }
      // 3. 关闭区之上：标题胶囊与面板同在一个玻璃容器，面板挂在胶囊正下方、与它等宽
      if let title = spec.title {
        GlassEffectContainer(spacing: Self.gap) {
          ZStack(alignment: .topLeading) {
            TitleCapsule(title: title, palette: palette, glass: glass) { model.toggle("title") }
              .place(title.frame)
            if titleOpen {
              MenuPanel(
                model: model, menu: title.menu, spec: spec, palette: palette, maxHeight: maxHeight,
                glass: glass, reduce: reduce
              )
              .frame(width: CGFloat(title.frame.width))
              .padding(.leading, CGFloat(title.frame.x))
              .padding(.top, CGFloat(title.frame.y + title.frame.height) + Self.gap)
              .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            }
          }
        }
      }
    }
    .frame(width: size.width, height: size.height, alignment: .topLeading)
  }
}

/// 44pt 圆形玻璃钮：19pt 图标，右上角可带 8pt 提醒点（距上、右各 8）。
@available(iOS 26.0, *)
struct RoundGlassButton: View {
  let symbol: String
  let label: String
  let dot: Color?
  let action: () -> Void

  var body: some View {
    Button(action: action) {
      Image(systemName: safeSymbol(symbol))
        .font(.system(size: 19))
        .foregroundStyle(.primary)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .overlay(alignment: .topTrailing) {
          if let dot = dot {
            Circle().fill(dot).frame(width: 8, height: 8).padding(8)
          }
        }
        .contentShape(Circle())
        .glassEffect(.regular.interactive(), in: Circle())
    }
    .buttonStyle(.plain)
    .accessibilityLabel(Text(label))
  }
}

/// 标题胶囊：第一行标题（15pt 半粗，左侧标题卡图标 / 线程色点），第二行状态（11.5pt，各段以「·」隔开）或错误。
@available(iOS 26.0, *)
struct TitleCapsule: View {
  let title: HeaderTitleSpec
  let palette: HeaderPalette
  /// 与标题面板共用的玻璃命名空间（面板从胶囊里形变长出来）。
  let glass: Namespace.ID
  let action: () -> Void

  var body: some View {
    Button(action: action) {
      VStack(spacing: 0) {
        HStack(spacing: 6) {
          if let icon = title.icon { HeaderIconView(icon: icon, box: 16, palette: palette) }
          Text(title.text)
            .font(.system(size: 15, weight: .semibold))
            .tracking(-0.375)
            .lineLimit(1)
            .truncationMode(.tail)
        }
        .frame(height: 20)
        if let error = title.error {
          Text(error).font(.system(size: 11.5)).foregroundStyle(palette.err).lineLimit(1)
            .frame(height: 16)
        } else if !title.meta.isEmpty || !title.marks.isEmpty {
          metaRow
        }
      }
      .padding(.horizontal, 16)
      .frame(maxWidth: .infinity, maxHeight: .infinity)
      .contentShape(Capsule())
      .glassEffect(.regular.interactive(), in: Capsule())
      .glassEffectID("title", in: glass)
    }
    .buttonStyle(.plain)
    .accessibilityElement(children: .ignore)
    .accessibilityLabel(Text(title.error.map { "\(title.label), \($0)" } ?? title.label))
    .accessibilityHint(Text(title.menu.label))
    .accessibilityAddTraits(.isButton)
    .onChange(of: title.error) { _, error in
      // 没有对话框的操作失败时错误显示在胶囊第二行：读屏也播报
      if let error = error { UIAccessibility.post(notification: .announcement, argument: error) }
    }
  }

  private var metaRow: some View {
    HStack(spacing: 6) {
      ForEach(Array(title.meta.enumerated()), id: \.offset) { index, seg in
        if index > 0 { Text("·").foregroundStyle(.tertiary) }
        let env = seg.icon?.orb != nil
        if env {
          // 运行环境那一段不让挤（文字最多 96pt），按内容宽度占位；
          // 不能用 .frame(maxWidth:)：它会撑满到上限，把其余各段挤成「…」
          CapWidth(max: 96 + 13) { SegView(seg: seg, palette: palette, box: 11) }
            .layoutPriority(1)
        } else {
          SegView(seg: seg, palette: palette, box: 11)
        }
      }
      if !title.marks.isEmpty {
        if !title.meta.isEmpty { Text("·").foregroundStyle(.tertiary) }
        HStack(spacing: 4) {
          ForEach(Array(title.marks.enumerated()), id: \.offset) { _, mark in
            Image(systemName: safeSymbol(mark.symbol))
              .font(.system(size: 11))
              .opacity(mark.dim == true ? 0.45 : 1)
          }
        }
        .layoutPriority(1)
        .accessibilityHidden(true)
      }
    }
    .font(.system(size: 11.5))
    .foregroundStyle(.secondary)
    .frame(height: 16)
  }
}

/// 只封顶不撑开的宽度：向子视图提议 min(提议, max)，自身取子视图的实际尺寸。
@available(iOS 26.0, *)
struct CapWidth: Layout {
  let max: CGFloat

  func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
    guard let child = subviews.first else { return .zero }
    let width = proposal.width.map { Swift.min($0, max) } ?? max
    return child.sizeThatFits(ProposedViewSize(width: width, height: proposal.height))
  }

  func placeSubviews(
    in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()
  ) {
    subviews.first?.place(at: bounds.origin, proposal: ProposedViewSize(bounds.size))
  }
}

/// 只封顶不撑开的高度：向子视图提议 min(提议, max)，自身取子视图的实际尺寸（菜单面板贴合内容高度）。
@available(iOS 26.0, *)
struct CapHeight: Layout {
  let max: CGFloat

  private func capped(_ proposal: ProposedViewSize, width: CGFloat?) -> ProposedViewSize {
    ProposedViewSize(width: width, height: proposal.height.map { Swift.min($0, max) } ?? max)
  }

  func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
    guard let child = subviews.first else { return .zero }
    return child.sizeThatFits(capped(proposal, width: proposal.width))
  }

  func placeSubviews(
    in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()
  ) {
    // 摆放时沿用测量时的高度提议，ViewThatFits 才会选回同一个分支
    subviews.first?.place(at: bounds.origin, proposal: capped(proposal, width: bounds.width))
  }
}

/// 一段「图形 + 文字」：图形与文字间距 4；字形徽标比其它图形大 2pt（网页的项目徽标 13 / Space 图标 11）。
@available(iOS 26.0, *)
struct SegView: View {
  let seg: HeaderSegSpec
  let palette: HeaderPalette
  let box: CGFloat

  var body: some View {
    HStack(spacing: 4) {
      if let icon = seg.icon {
        HeaderIconView(icon: icon, box: icon.glyph != nil ? box + 2 : box, palette: palette)
      }
      Text(seg.text).lineLimit(1).truncationMode(.tail)
    }
    .foregroundStyle(
      seg.tone == "err"
        ? AnyShapeStyle(palette.err) : AnyShapeStyle(HierarchicalShapeStyle.secondary))
  }
}

/// 图标五选一；`box` 是它占的方格边长。
@available(iOS 26.0, *)
struct HeaderIconView: View {
  let icon: HeaderIconSpec
  let box: CGFloat
  let palette: HeaderPalette

  var body: some View {
    if let orb = icon.orb {
      OrbBeadView(state: orb, palette: palette)
        .frame(width: max(9, box * 0.8), height: max(9, box * 0.8))
        .frame(width: max(9, box), height: max(9, box))
    } else if let dot = icon.dot {
      Circle().fill(hexColor(dot) ?? Color.secondary)
        .frame(width: box * 0.5, height: box * 0.5)
        .frame(width: box, height: box)
        .accessibilityHidden(true)
    } else if let mask = icon.mask, let image = HeaderMaskCache.image(mask) {
      // 网页画好的模板图已按方格留了边（字形占 0.68），与 glyph 分支同尺寸
      Image(uiImage: image)
        .renderingMode(.template)
        .resizable()
        .foregroundStyle(hexColor(icon.fg) ?? Color.primary)
        .frame(width: box, height: box)
        .background(
          hexColor(icon.bg) ?? Color.primary.opacity(0.08),
          in: RoundedRectangle(cornerRadius: box * 0.375, style: .continuous)
        )
        .accessibilityHidden(true)
    } else if let glyph = icon.glyph {
      Text(glyph)
        .font(.system(size: box * 0.68, weight: .semibold))
        .foregroundStyle(hexColor(icon.fg) ?? Color.primary)
        .frame(width: box, height: box)
        .background(
          hexColor(icon.bg) ?? Color.primary.opacity(0.08),
          in: RoundedRectangle(cornerRadius: box * 0.375, style: .continuous)
        )
        .accessibilityHidden(true)
    } else {
      Image(systemName: safeSymbol(icon.symbol))
        .font(.system(size: box * 0.9))
        .frame(width: box, height: box)
        .accessibilityHidden(true)
    }
  }
}

/// mask 图标的解码缓存：同一字形每次渲染都是同一串 base64，只解一次。
/// 壳已核对过 PNG 签名与 IHDR（`mobile_logic::valid_mask`）；这里解码前再用 ImageIO 确认类型是 PNG、
/// 宽高恰为 `px`，不是的一律不解（不交给其它图像解码器，也不按声明的大尺寸分配内存）。
enum HeaderMaskCache {
  /// 与网页 `NERD_MASK_PX`、壳的 `MASK_PX` 一致。
  static let px = 54
  private static let cache = NSCache<NSString, UIImage>()

  static func image(_ b64: String) -> UIImage? {
    let key = b64 as NSString
    if let hit = cache.object(forKey: key) { return hit }
    guard let data = Data(base64Encoded: b64),
      let source = CGImageSourceCreateWithData(data as CFData, nil),
      (CGImageSourceGetType(source) as String?) == "public.png",
      let props = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
      (props[kCGImagePropertyPixelWidth] as? Int) == px,
      (props[kCGImagePropertyPixelHeight] as? Int) == px,
      let cg = CGImageSourceCreateImageAtIndex(source, 0, nil)
    else { return nil }
    let image = UIImage(cgImage: cg)
    cache.setObject(image, forKey: key)
    return image
  }
}
