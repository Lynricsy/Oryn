// 端到端 UI 测试的公共部分：触点换算、无障碍树快照、轮询，以及与 ios-e2e-check.sh 的交接。
// 交接经共享目录（TEST_RUNNER_E2E_SYNC）：测试写 <步骤>.req（JSON）→ 脚本等到对应事件、截图 →
// 写 <步骤>.ok（JSON）或 <步骤>.fail（原因）。
import XCTest

/// 某个矩形内的一个按钮（无障碍树快照）。
struct Snap {
  let label: String
  let frame: CGRect
  let selected: Bool
}

struct E2EFailure: LocalizedError {
  let message: String
  init(_ message: String) { self.message = message }
  var errorDescription: String? { message }
}

/// 等待预算倍数（ios-e2e-check.sh 经 TEST_RUNNER_E2E_WAIT_SCALE 传入，缺省 1）：poll / until 与等元素出现的超时都乘以它。
/// CI 的 macOS 机器拥挤时整机慢数倍，只放宽等待，不改任何判定。
let waitScale: TimeInterval =
  ProcessInfo.processInfo.environment["E2E_WAIT_SCALE"].flatMap(TimeInterval.init) ?? 1

/// 各端到端测试的基类：本身没有测试方法。
class E2ECase: XCTestCase {
  var sync = URL(fileURLWithPath: "/")

  override func setUpWithError() throws {
    continueAfterFailure = false
    guard let dir = ProcessInfo.processInfo.environment["E2E_SYNC"] else {
      throw E2EFailure("缺少 TEST_RUNNER_E2E_SYNC")
    }
    sync = URL(fileURLWithPath: dir, isDirectory: true)
  }
}

@MainActor
extension E2ECase {
  // MARK: - 触摸

  /// 屏幕坐标（pt）处的触点。
  func point(_ app: XCUIApplication, _ x: CGFloat, _ y: CGFloat) -> XCUICoordinate {
    app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: x, dy: y))
  }

  // MARK: - 无障碍树

  /// 全部按钮，按是否在 WebView 子树里分成原生 / 网页两组；原生按 x 排序。
  func allButtons(of app: XCUIApplication) throws -> (native: [Snap], web: [Snap]) {
    var native: [Snap] = []
    var web: [Snap] = []
    func walk(_ node: XCUIElementSnapshot, inWeb: Bool) {
      let inside = inWeb || node.elementType == .webView
      if node.elementType == .button {
        let snap = Snap(label: node.label, frame: node.frame, selected: node.isSelected)
        if inside { web.append(snap) } else { native.append(snap) }
      }
      for child in node.children { walk(child, inWeb: inside) }
    }
    walk(try app.snapshot(), inWeb: false)
    return (native.sorted { $0.frame.minX < $1.frame.minX }, web)
  }

  /// 中心落在矩形里（四周放宽 2pt）的按钮，分组同 `allButtons`。
  func buttons(in rect: CGRect, of app: XCUIApplication) throws -> (native: [Snap], web: [Snap]) {
    buttons(in: rect, among: try allButtons(of: app))
  }

  /// 同上，从已取的一次快照里筛（XCUITest 取快照在 CI 上很慢，一次轮询只取一次）。
  func buttons(in rect: CGRect, among all: (native: [Snap], web: [Snap])) -> (native: [Snap], web: [Snap]) {
    let area = rect.insetBy(dx: -2, dy: -2)
    let inside = { (s: Snap) in area.contains(CGPoint(x: s.frame.midX, y: s.frame.midY)) }
    return (all.native.filter(inside), all.web.filter(inside))
  }

  func poll(timeout: TimeInterval, _ cond: () throws -> Bool) rethrows -> Bool {
    let deadline = Date().addingTimeInterval(timeout * waitScale)
    while true {
      if try cond() { return true }
      if Date() >= deadline { return false }
      Thread.sleep(forTimeInterval: 0.3)
    }
  }

  func until(_ what: String, timeout: TimeInterval = 10, _ cond: () throws -> Bool) throws {
    let ok = try poll(timeout: timeout, cond)
    if !ok { throw E2EFailure("超时：\(what)") }
  }

  // MARK: - 与脚本交接

  /// 告诉脚本到了某一步（不等）。
  func post(_ step: String, _ payload: [String: Any] = [:]) throws {
    let data = try JSONSerialization.data(withJSONObject: payload)
    try data.write(to: sync.appendingPathComponent("\(step).req"), options: .atomic)
  }

  /// 等脚本处理完某一步，返回它回传的 JSON；脚本报错时带着原因失败。
  /// 默认等 600 秒：CI 机器整机卡顿时脚本一步（等停稳 + 限时截图）曾超过 120 秒。
  func ack(_ step: String, timeout: TimeInterval = 600) throws -> [String: Any] {
    let ok = sync.appendingPathComponent("\(step).ok")
    let fail = sync.appendingPathComponent("\(step).fail")
    var reply: [String: Any]?
    let done = try poll(timeout: timeout) {
      if let reason = try? String(contentsOf: fail, encoding: .utf8) {
        throw E2EFailure("脚本在「\(step)」判定失败：\(reason)")
      }
      guard let data = try? Data(contentsOf: ok),
        let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
      else { return false }
      reply = json
      return true
    }
    guard done, let reply else { throw E2EFailure("等脚本处理「\(step)」超时") }
    return reply
  }

  func checkpoint(_ step: String, _ payload: [String: Any] = [:]) throws -> [String: Any] {
    try post(step, payload)
    return try ack(step)
  }
}

/// 脚本回传的矩形 `{x, y, width, height}`（pt）。
func rect(_ value: Any?) -> CGRect? {
  guard let f = value as? [String: Any],
    let x = f["x"] as? Double, let y = f["y"] as? Double,
    let w = f["width"] as? Double, let h = f["height"] as? Double
  else { return nil }
  return CGRect(x: x, y: y, width: w, height: h)
}
