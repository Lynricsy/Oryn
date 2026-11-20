// 原生底栏的端到端 UI 测试（ios-e2e-check.sh 驱动，只在 CI 的 iOS 26+ 模拟器里跑）。
// Oryn.app 是 debug 构建：ORYN_E2E_URL 让它跳过配对，按远程服务器的正常路径（open_remote：远程能力 + 宿主命令权限）
// 打开本机的 mock Web UI。触摸全部是 XCUITest 合成的 HID 事件，与真机一样过 UIKit 命中测试：
// 网页按钮、露出的卡片条、左缘拖动、原生底栏（浮在 WebView 之上的 UITabBar：点选与按住横滑）。
// 原生底栏与原生侧栏钮（首页左上角的侧栏钮也由原生页头接管、跟着卡片走）接管期间一直在无障碍树里，
// 所以开合看侧栏一侧——侧栏关着时 inert，它的「收起侧边栏」按钮不在无障碍树里，打开后才出现。
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

    // 0. 远程页面就绪：侧栏钮出现（网页钮，或原生页头接管后的原生钮；compact 下沉侧栏布局）
    let toggle = app.buttons
      .matching(NSPredicate(format: "label IN %@", ["Sidebar", "侧边栏"])).firstMatch
    // 开合的判据：侧栏自己的「收起侧边栏」按钮（侧栏关着时 inert，不在无障碍树里）
    let collapse = app.webViews.buttons
      .matching(NSPredicate(format: "label IN %@", ["Collapse sidebar", "收起侧边栏"])).firstMatch
    // 冷启动等 300 秒：CI 的 macOS 机器整机卡顿时 WebView 首次加载曾超过 90 秒
    guard toggle.waitForExistence(timeout: 300) else {
      throw E2EFailure("300 秒内没有出现侧栏钮（远程页面没打开，或没进 compact 布局）")
    }
    let screen = app.frame.size
    let loaded = try checkpoint(
      "loaded", ["width": Double(screen.width), "height": Double(screen.height)])
    guard let f = loaded["frame"] as? [String: Any],
      let fx = f["x"] as? Double, let fy = f["y"] as? Double,
      let fw = f["width"] as? Double, let fh = f["height"] as? Double
    else { throw E2EFailure("脚本没有回传胶囊矩形：\(loaded)") }
    let capsule = CGRect(x: fx, y: fy, width: fw, height: fh)
    // 侧栏宽 = 胶囊左右各让出 12（SectionTabBar 的 px-3）
    let sinkW = capsule.minX * 2 + capsule.width

    // 1. 点侧栏钮打开：胶囊矩形里是 UITabBar 的 4 个原生按钮，网页胶囊退出无障碍树（aria-hidden）
    toggle.tap()
    _ = try expectSink(app, collapse, capsule, open: true, "打开")
    _ = try checkpoint("open")

    // 2. 轻点露出的卡片条关闭：卡片盖回来，原生底栏裁到 0
    point(app, screen.width - 16, screen.height / 2).tap()
    _ = try expectSink(app, collapse, capsule, open: false, "关闭")
    _ = try checkpoint("closed")

    // 3. 从左缘（SINK_EDGE 24 以内）起手拖到约一半并按住：脚本在按住期间连拍截图，断言揭示边两侧。
    //    按住 40 秒；CI 的 macOS 机器会整机卡顿（XCUITest 取快照一次 30-60 秒、截图曾卡 3 分钟），
    //    脚本没赶在按住期间截到停稳的图时回 retry：轻点卡片条收起后重拖（脚本最多给 3 次）。
    //    慢速且过半：松手按位置吸附到打开
    let dx = (sinkW * 0.55).rounded()
    let y = (screen.height / 2).rounded()
    var attempt = 1
    while true {
      let step = "drag\(attempt)"
      try post(step, ["dx": Double(dx)])
      point(app, 4, y).press(
        forDuration: 0.1, thenDragTo: point(app, 4 + dx, y),
        withVelocity: XCUIGestureVelocity(rawValue: 200), thenHoldForDuration: 40)
      let reply = try ack(step, timeout: 600)
      guard reply["retry"] as? Bool == true else { break }
      _ = try expectSink(app, collapse, capsule, open: true, "第 \(attempt) 次拖动松手后")
      point(app, screen.width - 16, screen.height / 2).tap()
      _ = try expectSink(app, collapse, capsule, open: false, "重拖前收起")
      attempt += 1
    }
    let items = try expectSink(app, collapse, capsule, open: true, "拖动松手后")

    // 4. 点原生底栏的「项目」（SECTION_ORDER 第 3 项）：网页切分区、侧栏保持打开，选中态经新的 spec 回到原生
    let projects = items[2]
    point(app, projects.frame.midX, projects.frame.midY).tap()
    try until("点「项目」后原生选中项变成第 3 项") {
      let now = try self.buttons(in: capsule, of: app).native
      return now.count == 4 && now[2].selected
    }
    _ = try checkpoint("projects")

    // 5. 在原生底栏上按住已选中的「项目」横滑到第 1 项（threads）再松手：系统 UITabBar 的选中透镜跟手，
    //    松手换选中，与点选走同一条上报路径；侧栏保持打开
    let threads = items[0]
    point(app, projects.frame.midX, projects.frame.midY).press(
      forDuration: 0.5, thenDragTo: point(app, threads.frame.midX, threads.frame.midY),
      withVelocity: XCUIGestureVelocity(rawValue: 300), thenHoldForDuration: 0.3)
    try until("横滑到第 1 项松手后原生选中项变成第 1 项") {
      let now = try self.buttons(in: capsule, of: app).native
      return now.count == 4 && now[0].selected
    }
    _ = try checkpoint("swipe")

    // 6. 再点已选中的第 1 项：收起侧栏
    point(app, threads.frame.midX, threads.frame.midY).tap()
    _ = try expectSink(app, collapse, capsule, open: false, "再点第 1 项收起")
    _ = try checkpoint("final")
  }
}

@MainActor
extension TabBarE2ETests {
  /// 等下沉侧栏开 / 合到位，返回最后一次看到的原生按钮。打开：侧栏的「收起侧边栏」按钮 `collapse` 进入无障碍树，
  /// 胶囊矩形里是 UITabBar 的 4 个按钮，且网页里没有同名按钮（网页胶囊已 aria-hidden）。
  /// 关上：`collapse` 退出无障碍树（侧栏 inert）。裁到宽 0 的 UITabBar 关着时也在树里，所以关上不看原生按钮。
  /// 卡片的开合是 260ms 过渡（原生接管时另晚 34ms），轮询而不是固定等待。
  private func expectSink(
    _ app: XCUIApplication, _ collapse: XCUIElement, _ capsule: CGRect, open: Bool, _ step: String
  ) throws -> [Snap] {
    var last: (native: [Snap], web: [Snap]) = ([], [])
    var shown = false
    let ok = try poll(timeout: 10) {
      shown = collapse.exists
      last = try self.buttons(in: capsule, of: app)
      if !open { return !shown }
      let labels = Set(last.native.map(\.label))
      return shown && last.native.count == 4 && !last.web.contains { labels.contains($0.label) }
    }
    if !ok {
      throw E2EFailure(
        "\(step)：期望侧栏\(open ? "打开（侧栏可交互、原生底栏 4 项、网页胶囊隐藏）" : "收起（侧栏 inert）")；"
          + "「收起侧边栏」\(shown ? "在" : "不在")无障碍树里，"
          + "原生按钮 \(last.native.map(\.label))，网页按钮 \(last.web.map(\.label))")
    }
    return last.native
  }
}
