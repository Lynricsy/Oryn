// 原生底栏的端到端 UI 测试（ios-e2e-check.sh 驱动，只在 CI 的 iOS 26+ 模拟器里跑）。
// Oryn.app 是 debug 构建：ORYN_E2E_URL 让它跳过配对，按远程服务器的正常路径（open_remote：远程能力 + 宿主命令权限）
// 打开本机的 mock Web UI。触摸全部是 XCUITest 合成的 HID 事件，与真机一样过 UIKit 命中测试：
// 网页按钮、露出的卡片条、左缘拖动、原生胶囊。
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
    guard toggle.waitForExistence(timeout: 90) else {
      throw E2EFailure("90 秒内网页没有出现侧栏按钮（远程页面没打开，或没进 compact 布局）")
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

    // 1. 点页头的侧栏按钮打开：原生接管（4 个原生按钮），网页胶囊退出无障碍树（aria-hidden）
    toggle.tap()
    _ = try expectNative(app, capsule, visible: true, "打开")
    _ = try checkpoint("open")

    // 2. 轻点露出的卡片条关闭：原生底栏隐藏
    point(app, screen.width - 16, screen.height / 2).tap()
    _ = try expectNative(app, capsule, visible: false, "关闭")
    _ = try checkpoint("closed")

    // 3. 从左缘（SINK_EDGE 24 以内）起手拖到约一半并按住：脚本在按住期间截图断言揭示边。
    //    慢速且过半：松手按位置吸附到打开
    let dx = (sinkW * 0.55).rounded()
    let y = (screen.height / 2).rounded()
    try post("drag", ["dx": Double(dx)])
    point(app, 4, y).press(
      forDuration: 0.1, thenDragTo: point(app, 4 + dx, y),
      withVelocity: XCUIGestureVelocity(rawValue: 200), thenHoldForDuration: 8)
    _ = try ack("drag")
    let items = try expectNative(app, capsule, visible: true, "拖动松手后")

    // 4. 点原生底栏的「项目」（SECTION_ORDER 第 3 项）：网页切分区、侧栏保持打开，选中态经新的 spec 回到原生
    let projects = items[2]
    point(app, projects.frame.midX, projects.frame.midY).tap()
    try until("点「项目」后原生选中项变成第 3 项") {
      let now = try self.buttons(in: capsule, of: app).native
      return now.count == 4 && now[2].selected
    }
    _ = try checkpoint("projects")

    // 5. 再点已选中的「项目」：收起侧栏，原生底栏隐藏
    point(app, projects.frame.midX, projects.frame.midY).tap()
    _ = try expectNative(app, capsule, visible: false, "再点「项目」收起")
    _ = try checkpoint("final")
  }
}

@MainActor
extension TabBarE2ETests {
  /// 等到原生底栏可见（4 个原生按钮，且网页里没有同名按钮 = 网页胶囊已隐藏）或不可见（没有原生按钮）。
  /// 返回最后一次看到的原生按钮。原生的显隐跟随 260ms 吸附（打开另晚 34ms），轮询而不是固定等待。
  private func expectNative(
    _ app: XCUIApplication, _ capsule: CGRect, visible: Bool, _ step: String
  ) throws -> [Snap] {
    var last: (native: [Snap], web: [Snap]) = ([], [])
    let ok = try poll(timeout: 10) {
      last = try self.buttons(in: capsule, of: app)
      if !visible { return last.native.isEmpty }
      let labels = Set(last.native.map(\.label))
      return last.native.count == 4 && !last.web.contains { labels.contains($0.label) }
    }
    if !ok {
      throw E2EFailure(
        "\(step)：期望原生底栏\(visible ? "可见且网页胶囊隐藏" : "不可见")；"
          + "原生按钮 \(last.native.map(\.label))，网页按钮 \(last.web.map(\.label))")
    }
    return last.native
  }
}
