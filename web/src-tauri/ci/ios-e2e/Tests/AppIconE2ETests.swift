// 可替换图标的端到端 UI 测试（ios-e2e-check.sh 驱动，只在 CI 的模拟器里跑）。
// Oryn.app 是 debug 构建：ORYN_E2E_URL 直接打开 mock Web UI 的「设置 → 外观」，走与生产相同的
// open_remote（远程能力 + 宿主命令权限）。测试像用户一样点「品牌色」、点掉系统的「已更改图标」提示，
// 再点回「黑白」：页面 → IPC → host_app_icon_set → Swift → UIApplication.setAlternateIconName → 完成回调 → 应答。
// 壳只在完成回调成功后才写事件日志 appicon.set，所以等到这条事件就证明整条链路（含回调）都走通了；
// 先后顺序由脚本在测试结束后再核对一遍。
import XCTest

final class AppIconE2ETests: E2ECase {

  @MainActor
  func testAlternateIconEndToEnd() throws {
    let env = ProcessInfo.processInfo.environment
    guard let url = env["E2E_URL"], let log = env["E2E_LOG"] else {
      throw E2EFailure("缺少 TEST_RUNNER_E2E_URL / TEST_RUNNER_E2E_LOG")
    }
    let app = XCUIApplication(bundleIdentifier: "com.lynricsy.oryn")
    app.launchEnvironment = ["ORYN_E2E_URL": url, "ORYN_DESKTOP_EVENT_LOG": log]
    app.launch()

    // 页面语言随模拟器区域，两种文案都认
    let steps: [(id: String, labels: [String])] = [
      ("accent", ["Brand color", "品牌色"]),
      ("default", ["Mono", "黑白"]),
    ]
    for (index, step) in steps.enumerated() {
      let radio = app.webViews.descendants(matching: .any)
        .matching(NSPredicate(format: "label IN %@", step.labels)).firstMatch
      // 冷启动等 300 秒：CI 的 macOS 机器整机卡顿时 WebView 首次加载曾超过 90 秒
      guard radio.waitForExistence(timeout: index == 0 ? 300 : 30) else {
        throw E2EFailure("设置页没有出现「\(step.labels[0])」（宿主没报出可替换图标，或页面没打开）")
      }
      var swipes = 0
      while !radio.isHittable && swipes < 8 {
        app.swipeUp()
        swipes += 1
      }
      radio.tap()
      try dismissIconAlert(app)
      try until("appicon.set（\(step.id)）", timeout: 60) {
        logHas(log, event: "appicon.set", id: step.id)
      }
    }
  }

  /// 系统的「已更改图标」提示：CI 里观察到 iOS 要等它被点掉才调 setAlternateIconName 的完成回调。
  /// 提示可能挂在 App 或 SpringBoard 上；15 秒内都没出现就不管（以 appicon.set 为准）。
  @MainActor
  private func dismissIconAlert(_ app: XCUIApplication) throws {
    let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
    var alert: XCUIElement?
    _ = poll(timeout: 15) {
      for candidate in [app.alerts.firstMatch, springboard.alerts.firstMatch] where candidate.exists {
        alert = candidate
        return true
      }
      return false
    }
    alert?.buttons.firstMatch.tap()
  }

  private func logHas(_ path: String, event: String, id: String) -> Bool {
    guard let text = try? String(contentsOfFile: path, encoding: .utf8) else { return false }
    return text.split(separator: "\n").contains { line in
      line.contains("\"event\":\"\(event)\"") && line.contains("\"id\":\"\(id)\"")
    }
  }
}
