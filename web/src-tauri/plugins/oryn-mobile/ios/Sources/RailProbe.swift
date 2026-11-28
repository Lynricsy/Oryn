// iPhone Duo 竖栏（iOS 27.1+）：合上时的外屏、展开后横放的内屏上，系统把状态栏、相机与工具栏、标签栏竖排在一侧
// （HIG「Designing for iPhone Duo → Vertical controls」）。Oryn 的页头与底栏是自己画的，系统不会替它们竖排，只把那一条当安全区
// 空着；内屏横放时还把竖排的状态栏算成整条顶边的安全区，内容顶上空出一大条、右上角又被状态栏压住。
// 这里读系统给的竖栏一侧（traitCollection.verticalBarEdge）与该侧的 bar 布局指引（layoutGuide(for: .bar(onEdge:extent:))），
// 算出 Oryn 竖栏的矩形（顶端让开状态栏与相机）与竖栏之外内容区的安全区（横向收角：状态栏那一角从侧面让开，不再占整条顶边），
// 经 trigger("event", {type: "rail", side?, frame?, safeArea, probe}) 交给 Rust → 网页（lib/rail.ts：页头按钮与底栏放进竖栏）。
// 装在 WebView 的父视图上、与之同大、透明且不接触摸：折叠 / 展开 / 转屏会改它的边界、安全区或 trait，任一变化重算，结果变了才上报。
// 重算不在布局回调里同步做：读 bar 指引、reservedRegions 可能让 UIKit 再排一轮布局（同步递归会把主线程卡死在启动里），
// 上报又会调进 Rust → WebView；这里只排一次主队列任务（合并同一轮的多次请求），重算期间引起的布局不再排新任务。
// probe 是原始量测（系统安全区、收角安全区、两侧 bar 指引、遮挡区、状态栏），只进事件日志，供 CI 在 Duo 模拟器上核对。
import Tauri
import UIKit
import WebKit

@available(iOS 27.1, *)
final class RailProbe: UIView {
  /// 竖栏宽（pt）：44pt 的圆钮两侧各留 10pt。
  static let extent: CGFloat = 64

  private struct State: Equatable {
    let bounds: CGRect
    let side: String?
    let rail: CGRect?
    let safe: UIEdgeInsets
  }

  private weak var webview: WKWebView?
  private let onChange: (JSObject) -> Void
  private lazy var leadingBar = layoutGuide(for: .bar(onEdge: .leading, extent: Self.extent))
  private lazy var trailingBar = layoutGuide(for: .bar(onEdge: .trailing, extent: Self.extent))
  private lazy var cornerSafe = layoutGuide(for: .safeArea(cornerAdaptation: .horizontal))
  private var last: State?
  /// 已排了一次重算（主队列）还没跑
  private var queued = false
  /// 正在重算：期间读指引引起的布局回调不再排新任务（否则每一帧都重算一次）
  private var computing = false

  init(webview: WKWebView, onChange: @escaping (JSObject) -> Void) {
    self.webview = webview
    self.onChange = onChange
    super.init(frame: .zero)
    isUserInteractionEnabled = false
    backgroundColor = .clear
    autoresizingMask = [.flexibleWidth, .flexibleHeight]
    // 布局指引先建好：第一轮布局就能求解出它们的 layoutFrame
    _ = (leadingBar, trailingBar, cornerSafe)
    // 折叠 / 展开 / 转屏都会改边界（layoutSubviews 排重算）；尺寸类变化另外再排一次（竖栏的一侧随之变）
    registerForTraitChanges([UITraitHorizontalSizeClass.self, UITraitVerticalSizeClass.self]) {
      (probe: RailProbe, _: UITraitCollection) in
      probe.schedule()
    }
  }

  @available(*, unavailable)
  required init?(coder: NSCoder) {
    fatalError("init(coder:) has not been implemented")
  }

  override func safeAreaInsetsDidChange() {
    super.safeAreaInsetsDidChange()
    schedule()
  }

  override func layoutSubviews() {
    super.layoutSubviews()
    schedule()
  }

  private func schedule() {
    guard !queued, !computing else { return }
    queued = true
    DispatchQueue.main.async { [weak self] in
      guard let self else { return }
      self.queued = false
      self.computing = true
      defer { self.computing = false }
      self.report()
    }
  }

  private func report() {
    guard let webview, bounds.width > 0, bounds.height > 0 else { return }
    let web = webview.bounds
    let toWeb = { (r: CGRect) in self.convert(r, to: webview) }
    let edge = traitCollection.verticalBarEdge
    let bar: CGRect?
    switch edge {
    case .leading: bar = toWeb(leadingBar.layoutFrame)
    case .trailing: bar = toWeb(trailingBar.layoutFrame)
    default: bar = nil
    }
    let occlusions = reservedRegions(kind: .occlusion).map { toWeb($0.frame) }
    // statusBarFrame 是场景坐标（UIKit 文档）：按场景的坐标空间换到 WebView
    var statusBar: CGRect?
    if let scene = window?.windowScene, let manager = scene.statusBarManager {
      statusBar = webview.convert(manager.statusBarFrame, from: scene.coordinateSpace)
    }
    let corner = toWeb(cornerSafe.layoutFrame)

    // 内容区的安全区：优先取横向收角的安全区指引，没求解出来（空矩形）时退回系统安全区
    var safe =
      corner.width > 0 && corner.height > 0
      ? UIEdgeInsets(
        top: corner.minY - web.minY, left: corner.minX - web.minX,
        bottom: web.maxY - corner.maxY, right: web.maxX - corner.maxX)
      : webview.safeAreaInsets
    var side: String?
    var rail: CGRect?
    if let bar, bar.width > 0, bar.height > 0 {
      // 竖栏顶端让开同一列里的状态栏与遮挡区（相机、灵动岛）
      let column = CGRect(x: bar.minX, y: web.minY, width: bar.width, height: web.height)
      let blockers = occlusions + [statusBar].compactMap { $0 }
      let top = blockers.filter { $0.intersects(column) }.map(\.maxY).reduce(bar.minY) { max($0, $1) }
      rail = CGRect(x: bar.minX, y: top, width: bar.width, height: max(0, bar.maxY - top))
      if bar.midX > web.midX {
        side = "right"
        safe.right = max(safe.right, web.maxX - bar.minX)
      } else {
        side = "left"
        safe.left = max(safe.left, bar.maxX - web.minX)
      }
    }
    safe = UIEdgeInsets(
      top: max(0, safe.top), left: max(0, safe.left), bottom: max(0, safe.bottom),
      right: max(0, safe.right))

    let state = State(bounds: web, side: side, rail: rail, safe: safe)
    guard state != last else { return }
    last = state

    var probe: JSObject = [
      "edge": edge.rawValue,
      "bounds": Self.json(web),
      "safeArea": Self.json(webview.safeAreaInsets),
      "cornerSafe": Self.json(corner),
      "leadingBar": Self.json(toWeb(leadingBar.layoutFrame)),
      "trailingBar": Self.json(toWeb(trailingBar.layoutFrame)),
      "occlusions": occlusions.map { Self.json($0) as JSValue } as JSArray,
    ]
    if let statusBar { probe["statusBar"] = Self.json(statusBar) }
    var data: JSObject = ["type": "rail", "safeArea": Self.json(safe), "probe": probe]
    if let side, let rail {
      data["side"] = side
      data["frame"] = Self.json(rail)
    }
    onChange(data)
  }

  private static func json(_ r: CGRect) -> JSObject {
    ["x": Double(r.minX), "y": Double(r.minY), "width": Double(r.width), "height": Double(r.height)]
  }

  private static func json(_ i: UIEdgeInsets) -> JSObject {
    ["top": Double(i.top), "right": Double(i.right), "bottom": Double(i.bottom), "left": Double(i.left)]
  }
}
