// 原生底栏的端到端 UI 测试（ios-e2e-check.sh 驱动，只在 CI 的 iOS 26+ 模拟器里跑）。
// Oryn.app 是 debug 构建：ORYN_E2E_URL 让它跳过配对，按远程服务器的正常路径（open_remote：远程能力 + 宿主命令权限）
// 打开本机的 mock Web UI。触摸全部是 XCUITest 合成的 HID 事件，与真机一样过 UIKit 命中测试：
// 网页按钮、露出的卡片条、卡片上的横向拖动、原生底栏（经 WebView 之上的触摸转交层交给 UITabBar：点选与按住横滑）。
// 下沉侧栏（bridge 8）：卡片开合是 WKWebView 根滚动视图的横向滚动——拖动与松手吸附是系统滚动，按钮、卡片条、
// 再点已选分区这类程序化开合由原生逐帧推进；原生底栏按 contentOffset 决定接不接触摸。
// 原生底栏是系统 UITabBar，垫在 WebView 之下：接管期间一直在无障碍树里（关着时只是被卡片盖住），
// 所以开合到位看网页一侧的无障碍矩形——打开：卡片内容 inert（页头侧栏按钮退出无障碍树），露出的卡片条（铺满卡片的关闭按钮）
// 左缘在侧栏宽处；关上：页头侧栏按钮回到无障碍树、回到起始位置。
// 分工：本测试做触摸与无障碍树断言；截图、像素比较与事件日志断言在脚本里。每一步经共享目录交接：
// 测试写 <步骤>.req（JSON）→ 脚本等到对应事件、截图 → 写 <步骤>.ok（JSON）或 <步骤>.fail（原因）。
import XCTest

final class TabBarE2ETests: E2ECase {

  // XCUITest 的 API 都在主 actor 上（同 Xcode 16+ 的 UI 测试模板）
  @MainActor
  func testNativeTabBarEndToEnd() throws {
    let env = ProcessInfo.processInfo.environment
    guard let url = env["E2E_URL"], let log = env["E2E_LOG"] else {
      throw E2EFailure("缺少 TEST_RUNNER_E2E_URL / TEST_RUNNER_E2E_LOG")
    }
    let app = XCUIApplication(bundleIdentifier: "com.lynricsy.oryn")
    app.launchEnvironment = ["ORYN_E2E_URL": url, "ORYN_DESKTOP_EVENT_LOG": log]
    app.launch()

    // 0. 远程页面就绪：页头的侧栏按钮出现（compact 下沉侧栏布局；关闭时侧栏 inert，不在无障碍树里）
    let toggle = app.webViews.buttons
      .matching(NSPredicate(format: "label IN %@", ["Sidebar", "侧边栏"])).firstMatch
    // 冷启动等 300 秒：CI 的 macOS 机器整机卡顿时 WebView 首次加载曾超过 90 秒
    guard toggle.waitForExistence(timeout: 300) else {
      throw E2EFailure("300 秒内网页没有出现侧栏按钮（远程页面没打开，或没进 compact 布局）")
    }
    let screen = app.frame.size
    let loaded = try checkpoint(
      "loaded", ["width": Double(screen.width), "height": Double(screen.height)])
    guard let capsule = rect(loaded["frame"]) else {
      throw E2EFailure("脚本没有回传胶囊矩形：\(loaded)")
    }
    // 侧栏宽 = 胶囊左右各让出 12（SectionTabBar 的 px-3）= 根滚动的可滚范围。脚本已确认 sink 接管并停稳在关闭位，
    // 此刻页头侧栏按钮的左缘就是之后每次收起要回到的位置
    let sink = Sink(
      toggle: toggle, capsule: capsule, width: capsule.minX * 2 + capsule.width, home: toggle.frame.minX)

    // 1. 点页头的侧栏按钮打开：胶囊矩形里是 UITabBar 的 4 个原生按钮，网页胶囊退出无障碍树（aria-hidden）。
    //    CI 上偶有一次轻点完全没有效果（网页没发 sink.show，run 44736012739）：8 秒后按钮仍在无障碍树里、且仍在起始位置
    //    （卡片根本没动；动了说明第一下生效了只是慢，再点会把它关回去）就再点一次。两次点击前的按钮矩形连同重试回传给脚本，
    //    脚本记 ::warning::，不让偶发吞点击静默通过
    let before = sink.toggle.frame
    sink.toggle.tap()
    var retry: [String: Any] = [:]
    _ = poll(timeout: 8) { !sink.toggle.exists }
    if sink.toggle.exists, abs(sink.toggle.frame.minX - sink.home) <= 1 {
      retry = ["retry": true, "first": "\(before)", "second": "\(sink.toggle.frame)"]
      sink.toggle.tap()
    }
    _ = try expectSink(app, sink, open: true, "打开")
    _ = try checkpoint("open", retry)

    // 2. 轻点露出的卡片条关闭：卡片重新盖住垫底的原生底栏
    point(app, screen.width - 16, screen.height / 2).tap()
    _ = try expectSink(app, sink, open: false, "关闭")
    _ = try checkpoint("closed")

    // 3. 在卡片左缘内（x = 8：首页输入框左右各让出 12pt，不落在可编辑元素上）起手拖到 0.55 × 侧栏宽并按住：
    //    脚本在按住期间连拍截图，断言揭示边两侧。按住期间测试本身阻塞在手势里，取不了无障碍快照，卡片位置全看截图。
    //    按住 40 秒；CI 的 macOS 机器会整机卡顿（XCUITest 取快照一次 30-60 秒、截图曾卡 3 分钟），
    //    脚本没赶在按住期间截到停稳的图时回 retry：轻点卡片条收起后重拖（脚本最多给 3 次）。
    //    过半松手：系统按 scroll-snap 吸附到打开
    let dx = (sink.width * 0.55).rounded()
    let y = (screen.height / 2).rounded()
    var attempt = 1
    while true {
      let step = "drag\(attempt)"
      try post(step, ["dx": Double(dx)])
      point(app, 8, y).press(
        forDuration: 0.1, thenDragTo: point(app, 8 + dx, y),
        withVelocity: XCUIGestureVelocity(rawValue: 200), thenHoldForDuration: 40)
      let reply = try ack(step, timeout: 600)
      guard reply["retry"] as? Bool == true else { break }
      _ = try expectSink(app, sink, open: true, "第 \(attempt) 次拖动松手后")
      point(app, screen.width - 16, screen.height / 2).tap()
      _ = try expectSink(app, sink, open: false, "重拖前收起")
      attempt += 1
    }
    let items = try expectSink(app, sink, open: true, "拖动松手后")

    // 4. 点原生底栏的「项目」（SECTION_ORDER 第 3 项）：网页切分区、侧栏保持打开，选中态经新的 spec 回到原生
    let projects = items[2]
    point(app, projects.frame.midX, projects.frame.midY).tap()
    try until("点「项目」后原生选中项变成第 3 项") {
      let now = try self.buttons(in: capsule, of: app).native
      return now.count == 4 && now[2].selected
    }
    _ = try expectSink(app, sink, open: true, "点「项目」后")
    _ = try checkpoint("projects")

    // 5. 在原生底栏上按住已选中的「项目」横滑到第 1 项（threads）再松手：系统 UITabBar 的选中透镜跟手，
    //    松手换选中，与点选走同一条上报路径；横滑由底栏的触摸转交层接走，不带动根滚动，侧栏保持打开
    let threads = items[0]
    point(app, projects.frame.midX, projects.frame.midY).press(
      forDuration: 0.5, thenDragTo: point(app, threads.frame.midX, threads.frame.midY),
      withVelocity: XCUIGestureVelocity(rawValue: 300), thenHoldForDuration: 0.3)
    try until("横滑到第 1 项松手后原生选中项变成第 1 项") {
      let now = try self.buttons(in: capsule, of: app).native
      return now.count == 4 && now[0].selected
    }
    _ = try expectSink(app, sink, open: true, "横滑换项后")
    _ = try checkpoint("swipe")

    // 6. 再点已选中的第 1 项：收起侧栏
    point(app, threads.frame.midX, threads.frame.midY).tap()
    _ = try expectSink(app, sink, open: false, "再点第 1 项收起")
    _ = try checkpoint("final")
  }
}

/// 下沉侧栏的几何：页头侧栏按钮、原生底栏胶囊、侧栏宽（= 打开位的卡片平移）、关闭位时侧栏按钮的左缘。
private struct Sink {
  let toggle: XCUIElement
  let capsule: CGRect
  let width: CGFloat
  let home: CGFloat
}

@MainActor
extension TabBarE2ETests {
  /// 露出的卡片条（SinkLayout 的 sink-close，铺满卡片）的读屏文字（common.close）。
  private var close: [String] { ["Close", "关闭"] }

  /// 等下沉侧栏开 / 合到位，返回最后一次看到的原生按钮（关上时为空）。卡片位置是根滚动，网页元素的无障碍矩形随之平移：
  /// 打开：卡片内容 inert（页头侧栏按钮不在无障碍树里，网页在根滚动停稳后才提交开合，所以它一消失卡片就已静止），
  /// 卡片条左缘在侧栏宽处（±1pt），胶囊矩形里是 UITabBar 的 4 个按钮，且网页里没有同名按钮（网页胶囊已 aria-hidden）。
  /// 关上：页头侧栏按钮回到无障碍树、左缘回到起始位置（±1pt；收起时它一开始就可交互，要等平移走完）。
  /// 垫底的 UITabBar 关着时也在树里，所以关上不看原生按钮。轮询而不是固定等待。
  private func expectSink(
    _ app: XCUIApplication, _ sink: Sink, open: Bool, _ step: String
  ) throws -> [Snap] {
    var last: (native: [Snap], web: [Snap]) = ([], [])
    var inert = false
    var edge: CGFloat?
    let ok = try poll(timeout: 20) {
      inert = !sink.toggle.exists
      if !open {
        edge = inert ? nil : sink.toggle.frame.minX
        guard let x = edge else { return false }
        return abs(x - sink.home) <= 1
      }
      let all = try self.allButtons(of: app)
      last = self.buttons(in: sink.capsule, among: all)
      // 同名按钮可能不止一个（侧栏里也可能有「关闭」）：取左缘离侧栏宽最近的那个
      edge = all.web.filter { self.close.contains($0.label) }.map(\.frame.minX)
        .min { abs($0 - sink.width) < abs($1 - sink.width) }
      guard inert, let x = edge, abs(x - sink.width) <= 1 else { return false }
      let labels = Set(last.native.map(\.label))
      return last.native.count == 4 && !last.web.contains { labels.contains($0.label) }
    }
    if !ok {
      let seen = edge.map { "\($0)" } ?? "没有"
      throw E2EFailure(
        "\(step)：期望侧栏"
          + (open
            ? "打开（卡片 inert、卡片条左缘 \(sink.width)、原生底栏 4 项、网页胶囊隐藏）；卡片条左缘 \(seen)，"
            : "收起（页头侧栏按钮回到左缘 \(sink.home)）；侧栏按钮左缘 \(seen)，")
          + "页头侧栏按钮\(inert ? "不在" : "在")无障碍树里，"
          + "原生按钮 \(last.native.map(\.label))，网页按钮 \(last.web.map(\.label))")
    }
    return open ? last.native : []
  }
}
