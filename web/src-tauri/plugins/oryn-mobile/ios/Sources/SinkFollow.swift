// 下沉侧栏的根滚动跟随（iOS 26+，bridge 8）。卡片开合 = WKWebView 自带滚动视图的横向滚动：网页把文档做成可横滚
// （侧栏 position: fixed 垫底，卡片在右侧 in-flow，吸附用根元素的 CSS scroll-snap），手指拖动、吸附与回弹都是系统的。
// 这里只观察滚动视图的 contentOffset，把卡片左缘 dx = 关闭位 − contentOffset.x 交给原生页头与底栏，
// 在同一次 KVO 回调里改它们的位置：与卡片同进程、同一帧，不需要任何对时。
// 程序化的平滑开合也在这里做：CADisplayLink 逐帧用不带动画的 setContentOffset 推进。网页自己发起的 smooth 滚动与
// setContentOffset(animated: true) 在 WKWebView 主框架上收尾时都会被吸附逻辑拉回原处（M20 探针，C2 第 1 轮），
// 不带动画的落位则可靠；逐帧推进的每一步都是这种落位，KVO 照常逐帧驱动页头与底栏。
// 不设 scrollView.delegate（WKWebView 自己的吸附与键盘处理都挂在内部代理上）。
import UIKit
import WebKit

/// `sinkSet` 的载荷。`trace`：装根滚动轨迹回调（只有 debug 包为真；缺省按假）。
struct SinkSetArgs: Decodable {
  let on: Bool
  let trace: Bool?
}

/// `sinkShow` 的载荷：滚到打开位（`open`）或关闭位；`animated` = 逐帧平滑推进，否则一步落位。
struct SinkShowArgs: Decodable {
  let open: Bool
  let animated: Bool
}

/// CADisplayLink 会强引用 target：经这层弱引用转发，免得跟随器被它留住。
@available(iOS 26.0, *)
private final class SinkTicker: NSObject {
  weak var owner: SinkFollow?
  init(_ owner: SinkFollow) { self.owner = owner }
  @objc func tick(_ link: CADisplayLink) { owner?.tick(link) }
}

@available(iOS 26.0, *)
final class SinkFollow {
  private weak var scroll: UIScrollView?
  private let onMove: (CGFloat) -> Void
  /// 根滚动的轨迹（op, 当时的 contentOffset.x, 目的地, 调用栈）：Rust 只在 debug 包写进事件日志，供 CI 判断原生动画
  /// 有没有跑、何时跑，以及卡片被谁挪走。op：start / jump（不带动画时一步落位）/ noop（已在目的地）/ busy（手指按着，不动）/
  /// done / cancel（手指接管、被新请求替换或撤掉）/ move（不是本类、也不是手指与惯性造成的位移：网页请求的滚动或 WebKit
  /// 自己的吸附；`x` → `to` 是这一阵位移的第一步，带调用栈，300ms 内连续的位移只记第一条）。
  var onTrace: ((String, CGFloat, CGFloat, String?) -> Void)?
  /// 上一次观察到的 contentOffset.x、本类正在落位、上一条 move 的时刻（只供轨迹）。
  private var lastX = CGFloat.nan
  private var owning = false
  private var lastMove: CFTimeInterval = 0
  private var observations: [NSKeyValueObservation] = []
  private let hadIndicator: Bool
  /// 卡片左缘（= 卡片相对关闭位右移的距离，pt）：关闭 0，打开 = 侧栏宽。
  private(set) var dx: CGFloat = 0
  /// 程序化平滑开合（时长与网页 `--ease` 开合一致的 260ms，ease-out 三次曲线）。
  private static let duration: CFTimeInterval = 0.26
  private var link: CADisplayLink?
  private var anim: (from: CGFloat, to: CGFloat, start: CFTimeInterval)?

  init(webview: WKWebView, onMove: @escaping (CGFloat) -> Void) {
    let scroll = webview.scrollView
    self.scroll = scroll
    self.onMove = onMove
    hadIndicator = scroll.showsHorizontalScrollIndicator
    scroll.showsHorizontalScrollIndicator = false
    observations = [
      scroll.observe(\.contentOffset) { [weak self] _, _ in self?.update() },
      scroll.observe(\.contentSize) { [weak self] _, _ in self?.update() },
      scroll.observe(\.bounds) { [weak self] _, _ in self?.update() },
    ]
    update()
  }

  /// 打开位 / 关闭位的 contentOffset.x（含系统调整的左右内边距：横屏时安全区可能不为 0）。
  private func ends(_ s: UIScrollView) -> (open: CGFloat, closed: CGFloat) {
    let inset = s.adjustedContentInset
    let open = -inset.left
    let closed = max(open, s.contentSize.width + inset.right - s.bounds.width)
    return (open, closed)
  }

  private func update() {
    guard let s = scroll else { return }
    let x = s.contentOffset.x
    if !lastX.isNaN, abs(x - lastX) > 0.5, anim == nil, !owning, !s.isTracking, !s.isDragging,
      !s.isDecelerating, let onTrace
    {
      let now = CACurrentMediaTime()
      if now - lastMove > 0.3 { onTrace("move", lastX, x, Self.stack()) }
      lastMove = now
    }
    lastX = x
    let (open, closed) = ends(s)
    let next = min(max(closed - x, 0), closed - open)
    guard abs(next - dx) > 0.01 else { return }
    dx = next
    onMove(next)
  }

  /// 当前调用栈的精简形式（模块:符号，去掉偏移），只在罕见的外来位移时取。
  private static func stack() -> String {
    Thread.callStackSymbols.dropFirst(3).prefix(28).map { line in
      let parts = line.split(separator: " ", omittingEmptySubsequences: true)
      guard parts.count >= 4 else { return line }
      let symbol = parts[3...].joined(separator: " ")
        .replacingOccurrences(of: #" \+ \d+$"#, with: "", options: .regularExpression)
      return "\(parts[1]):\(symbol)"
    }.joined(separator: " | ")
  }

  /// 程序化开合：滚到打开位或关闭位（纵向位置不动）。手指按着（isTracking，或拖动已开始的 isDragging：
  /// 按住不动时 isTracking 可能已为假，PR #3 drag1）时什么都不做（busy），逐帧推进中途遇到也立即停下让给手势。
  func show(open: Bool, animated: Bool) {
    stop(cancelled: true)
    guard let s = scroll else { return }
    let x = open ? ends(s).open : ends(s).closed
    guard abs(s.contentOffset.x - x) > 0.5 else {
      onTrace?("noop", s.contentOffset.x, x, nil)
      return
    }
    guard !s.isTracking, !s.isDragging else {
      onTrace?("busy", s.contentOffset.x, x, nil)
      return
    }
    guard animated else {
      onTrace?("jump", s.contentOffset.x, x, nil)
      owning = true
      s.setContentOffset(CGPoint(x: x, y: s.contentOffset.y), animated: false)
      owning = false
      return
    }
    onTrace?("start", s.contentOffset.x, x, nil)
    anim = (s.contentOffset.x, x, CACurrentMediaTime())
    let link = CADisplayLink(target: SinkTicker(self), selector: #selector(SinkTicker.tick(_:)))
    link.preferredFrameRateRange = CAFrameRateRange(minimum: 60, maximum: 120, preferred: 120)
    link.add(to: .main, forMode: .common)
    self.link = link
  }

  fileprivate func tick(_ link: CADisplayLink) {
    guard let s = scroll, let a = anim, !s.isTracking, !s.isDragging else {
      stop(cancelled: true)
      return
    }
    let t = min((link.targetTimestamp - a.start) / Self.duration, 1)
    let eased = 1 - pow(1 - t, 3)
    s.setContentOffset(CGPoint(x: a.from + (a.to - a.from) * eased, y: s.contentOffset.y), animated: false)
    if t >= 1 { stop(cancelled: false) }
  }

  private func stop(cancelled: Bool) {
    if let a = anim {
      onTrace?(cancelled ? "cancel" : "done", scroll?.contentOffset.x ?? -1, a.to, nil)
    }
    link?.invalidate()
    link = nil
    anim = nil
  }

  /// 撤掉跟随：注销观察、还原滚动条，原生部件回到关闭位。
  func remove() {
    stop(cancelled: true)
    observations.forEach { $0.invalidate() }
    observations = []
    scroll?.showsHorizontalScrollIndicator = hadIndicator
    dx = 0
    onMove(0)
  }
}
