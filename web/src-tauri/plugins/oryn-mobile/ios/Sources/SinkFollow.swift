// 下沉侧栏的根滚动跟随（iOS 26+，bridge 8）。卡片开合 = WKWebView 自带滚动视图的横向滚动：网页把文档做成可横滚
// （侧栏 position: fixed 垫底，卡片在右侧 in-flow，吸附用根元素的 CSS scroll-snap），手指拖动、吸附与回弹都是系统的。
// 这里只观察滚动视图的 contentOffset，把卡片左缘 dx = 关闭位 − contentOffset.x 交给原生页头与底栏，
// 在同一次 KVO 回调里改它们的位置：与卡片同进程、同一帧，不需要任何对时。
// 程序化的平滑开合也在这里做（setContentOffset(animated:)）：网页自己发起的 smooth 滚动在 WKWebView 主框架上
// 会被拉回（M20 探针），网页只用 instant 落位。
// 不设 scrollView.delegate（WKWebView 自己的吸附与键盘处理都挂在内部代理上）。
import UIKit
import WebKit

/// `sinkSet` 的载荷。
struct SinkSetArgs: Decodable {
  let on: Bool
}

/// `sinkShow` 的载荷：滚到打开位（`open`）或关闭位；`animated` = UIKit 滚动动画。
struct SinkShowArgs: Decodable {
  let open: Bool
  let animated: Bool
}

@available(iOS 26.0, *)
final class SinkFollow {
  private weak var scroll: UIScrollView?
  private let onMove: (CGFloat) -> Void
  private var observations: [NSKeyValueObservation] = []
  private let hadIndicator: Bool
  /// 卡片左缘（= 卡片相对关闭位右移的距离，pt）：关闭 0，打开 = 侧栏宽。
  private(set) var dx: CGFloat = 0

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
    let (open, closed) = ends(s)
    let next = min(max(closed - s.contentOffset.x, 0), closed - open)
    guard abs(next - dx) > 0.01 else { return }
    dx = next
    onMove(next)
  }

  /// 程序化开合：滚到打开位或关闭位（纵向位置不动）。
  func show(open: Bool, animated: Bool) {
    guard let s = scroll else { return }
    let x = open ? ends(s).open : ends(s).closed
    let target = CGPoint(x: x, y: s.contentOffset.y)
    guard s.contentOffset != target else { return }
    s.setContentOffset(target, animated: animated)
  }

  /// 撤掉跟随：注销观察、还原滚动条，原生部件回到关闭位。
  func remove() {
    observations.forEach { $0.invalidate() }
    observations = []
    scroll?.showsHorizontalScrollIndicator = hadIndicator
    dx = 0
    onMove(0)
  }
}
