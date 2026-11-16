// 原生页头的端到端 UI 测试（ios-e2e-check.sh 在底栏测试之后单独驱动，只在 CI 的 iOS 26+ 模拟器里跑）。
// Oryn.app 是 debug 构建：ORYN_E2E_URL 直接打开 mock Web UI 的一个线程页（/t/<MAIN_THREAD>），走与生产相同的
// open_remote（远程能力 + 宿主命令权限）。页面认出 iOS 壳（bridge ≥ 5）后由原生 Liquid Glass 页头接管线程页的三件控件。
// 步骤（测试写 <步骤>.req，脚本断言事件日志并截图后放行，见 E2ESupport.swift）：
//   h-loaded   原生侧栏钮出现：脚本确认 header.set（key = 线程 id）与 header.show（alpha 1），回传三件矩形
//   h-native   无障碍树：三件矩形里都是原生按钮，网页三件已退出无障碍树（aria-hidden）
//   h-menu     点原生标题胶囊：原生面板展开（出现「改名」行），脚本确认 header.event（menu = title）
//   h-sub      点「Orb」行：面板原地换成子页（「返回, Orb」行），不产生 select
//   h-back     点返回行：回到主页
//   h-dialog   点「改名」：脚本确认 header.event（select / title / rename）；网页打开改名对话框（无障碍树里有输入框），
//              遮挡探测让原生页头让位（header.show alpha 0，原生按钮消失）
//   h-return   点对话框上方的遮罩关掉它：原生页头回来
//   h-sidebar  点原生侧栏钮：脚本确认 header.event（lead），网页打开侧栏，原生页头让位、原生底栏露出
//   h-closed   点露出的卡片条关侧栏：原生页头回来
import XCTest

final class HeaderE2ETests: E2ECase {
  private let sidebar = ["Sidebar", "侧边栏"]
  private let rename = ["Rename", "改名"]
  private let orb = ["Orb", "orb"]
  /// 子页返回行的读屏文字是「<common.back>, <父项名>」
  private let back = ["Back, ", "返回, "]

  @MainActor
  func testNativeHeaderEndToEnd() throws {
    let env = ProcessInfo.processInfo.environment
    guard let url = env["E2E_URL"], let log = env["E2E_LOG"] else {
      throw E2EFailure("缺少 TEST_RUNNER_E2E_URL / TEST_RUNNER_E2E_LOG")
    }
    let app = XCUIApplication(bundleIdentifier: "com.lynricsy.oryn")
    app.launchEnvironment = ["ORYN_E2E_URL": url, "ORYN_DESKTOP_EVENT_LOG": log]
    app.launch()

    // 0. 线程页就绪且原生页头已显示：WebView 之外出现侧栏钮
    try until("90 秒内没有出现原生侧栏钮（线程页没打开，或原生页头没接管）", timeout: 90) {
      try self.native(app, self.sidebar) != nil
    }
    let screen = app.frame.size
    let loaded = try checkpoint("h-loaded")
    guard let lead = rect(loaded["lead"]), let title = rect(loaded["title"]) else {
      throw E2EFailure("脚本没有回传页头矩形：\(loaded)")
    }
    // mock 线程有 orb，视图钮一定在；没有时脚本已判失败，这里不再要求
    let slots = [lead, title] + (rect(loaded["trail"]).map { [$0] } ?? [])

    // 1. 原生三件接管，网页三件隐藏
    try expectHeader(app, slots, native: true, "接管")
    _ = try checkpoint("h-native")

    // 2. 点原生标题胶囊：面板展开
    point(app, title.midX, title.midY).tap()
    try until("点胶囊后原生面板没有展开（没有「改名」行）") { try self.native(app, self.rename) != nil }
    _ = try checkpoint("h-menu")

    // 3. 点「Orb」行：原地换成子页
    try tap(app, orb, "Orb 行")
    try until("点「Orb」后没有换成子页") {
      let sub = try self.native(app, self.back, prefix: true)
      let main = try self.native(app, self.rename)
      return sub != nil && main == nil
    }
    _ = try checkpoint("h-sub")

    // 4. 点返回行：回到主页
    try tap(app, back, "返回行", prefix: true)
    try until("点返回后没有回到主页") {
      let main = try self.native(app, self.rename)
      let sub = try self.native(app, self.back, prefix: true)
      return main != nil && sub == nil
    }
    _ = try checkpoint("h-back")

    // 5. 点「改名」：菜单收起，网页打开改名对话框，原生页头让位
    try tap(app, rename, "改名行")
    guard app.webViews.textFields.firstMatch.waitForExistence(timeout: 10) else {
      throw E2EFailure("点「改名」后网页没有打开改名对话框（无障碍树里没有输入框）")
    }
    try expectHeader(app, slots, native: false, "对话框打开后")
    _ = try checkpoint("h-dialog")

    // 6. 点对话框上方的遮罩（页头所在的位置，原生已不截触摸）关掉它：原生页头回来
    point(app, screen.width / 2, title.midY).tap()
    try until("点遮罩后改名对话框没有关掉") { !app.webViews.textFields.firstMatch.exists }
    try expectHeader(app, slots, native: true, "对话框关掉后")
    _ = try checkpoint("h-return")

    // 7. 点原生侧栏钮：网页打开侧栏，原生页头让位
    point(app, lead.midX, lead.midY).tap()
    try expectHeader(app, slots, native: false, "打开侧栏后")
    _ = try checkpoint("h-sidebar")

    // 8. 点露出的卡片条关侧栏：原生页头回来
    point(app, screen.width - 16, screen.height / 2).tap()
    try expectHeader(app, slots, native: true, "关侧栏后")
    _ = try checkpoint("h-closed")
  }
}

@MainActor
extension HeaderE2ETests {
  /// WebView 之外第一个读屏文字匹配的按钮（`prefix` 时按前缀匹配）。
  private func native(_ app: XCUIApplication, _ labels: [String], prefix: Bool = false) throws -> Snap? {
    try allButtons(of: app).native.first { snap in
      labels.contains { prefix ? snap.label.hasPrefix($0) : snap.label == $0 }
    }
  }

  /// 点原生面板里的一行。
  private func tap(_ app: XCUIApplication, _ labels: [String], _ what: String, prefix: Bool = false) throws {
    guard let row = try native(app, labels, prefix: prefix) else {
      throw E2EFailure("原生面板里没有\(what)（\(labels)）")
    }
    point(app, row.frame.midX, row.frame.midY).tap()
  }

  /// 等到原生页头接管（每个矩形里都有原生按钮、没有网页按钮 = 网页三件已 aria-hidden）或让位（矩形里没有原生按钮）。
  private func expectHeader(_ app: XCUIApplication, _ slots: [CGRect], native want: Bool, _ step: String) throws {
    var natives: [[String]] = []
    var webs: [[String]] = []
    let ok = try poll(timeout: 10) {
      natives = []
      webs = []
      for slot in slots {
        let b = try self.buttons(in: slot, of: app)
        natives.append(b.native.map { $0.label })
        webs.append(b.web.map { $0.label })
      }
      if !want { return natives.allSatisfy { $0.isEmpty } }
      return natives.allSatisfy { !$0.isEmpty } && webs.allSatisfy { $0.isEmpty }
    }
    if !ok {
      throw E2EFailure(
        "\(step)：期望原生页头\(want ? "接管且网页三件隐藏" : "让位")；"
          + "各矩形里的原生按钮 \(natives)，网页按钮 \(webs)")
    }
  }
}
