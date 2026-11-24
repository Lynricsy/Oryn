// 推送探针的 UI 测试（ci/ios-push-check.sh 驱动，只在 CI 的模拟器里跑）：带 ORYN_PUSH_PROBE=1 冷启动 debug 包，
// 探针（PushRegistrar.probe，仅 debug 构建）把 RFC 8291 附录 A 的接收端密钥写进共享钥匙串、请求通知授权并注册远程通知。
// 系统授权框由 SpringBoard 弹出（simctl 不能预先授予通知权限），这里像用户一样点「允许」；没弹框（之前已授权）也照常继续。
// 之后两步交给脚本：
//   p-launched  App 在前台：脚本等 AppDelegate 钩子的注册回调日志，simctl push challenge.apns，等 apns.challenge
//   p-done      脚本 simctl terminate 后 push notify.apns，等 NSE 的 nse.* 日志
// 断言全在脚本里（系统日志），测试只负责启动与点授权框。
import XCTest

final class PushE2ETests: E2ECase {

  @MainActor
  func testPushProbe() throws {
    let env = ProcessInfo.processInfo.environment
    guard let log = env["E2E_LOG"] else {
      throw E2EFailure("缺少 TEST_RUNNER_E2E_LOG")
    }
    let app = XCUIApplication(bundleIdentifier: "com.lynricsy.oryn")
    app.launchEnvironment = ["ORYN_PUSH_PROBE": "1", "ORYN_DESKTOP_EVENT_LOG": log]
    app.launch()
    let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")

    // 页面语言随模拟器区域，两种文案都认；授权框可能挂在 SpringBoard 或 App 上
    let allow = NSPredicate(format: "label IN %@", ["Allow", "允许"])
    var tapped = false
    _ = poll(timeout: 30) {
      for alert in [springboard.alerts.firstMatch, app.alerts.firstMatch] where alert.exists {
        let button = alert.buttons.matching(allow).firstMatch
        if button.exists && button.isHittable {
          button.tap()
          tapped = true
          return true
        }
      }
      return false
    }
    _ = try checkpoint("p-launched", ["alert": tapped])
    _ = try checkpoint("p-done")
  }
}
