// M20 预案 C2 的验收探针（ci/ios-sink-c2 分支专用，不合入；壳是 feat/ios-native-sink 的 bridge 8，含真实 SinkFollow）。
// 问题：原生 setContentOffset(animated:)（host_sink_show）开合后，根滚动能否到端点且持续不回弹（网页 smooth 会被拉回，第 1–3 轮）。
// 探针页加载时先 host_sink_set {on: true}；读数来自状态按钮的无障碍 label。按顺序发 p01、p02… 交给脚本（ios-e2e-check.sh 的探针段）。
import XCTest

final class SinkSpikeTests: E2ECase {
  private var seq = 0

  @MainActor
  func testSinkSpike() throws {
    let env = ProcessInfo.processInfo.environment
    guard let url = env["E2E_URL"], let log = env["E2E_LOG"] else {
      throw E2EFailure("缺少 TEST_RUNNER_E2E_URL / TEST_RUNNER_E2E_LOG")
    }
    let app = XCUIApplication(bundleIdentifier: "com.lynricsy.oryn")
    app.launchEnvironment = ["ORYN_E2E_URL": url, "ORYN_DESKTOP_EVENT_LOG": log]
    app.launch()

    let status = app.webViews.buttons
      .matching(NSPredicate(format: "label BEGINSWITH %@", "sx=")).firstMatch
    guard status.waitForExistence(timeout: 300) else {
      throw E2EFailure("300 秒内探针页没有出现状态按钮（页面没打开）")
    }
    let screen = app.frame.size

    func read() -> [String: Double] {
      var out: [String: Double] = [:]
      for part in status.label.split(separator: ";") {
        let kv = part.split(separator: "=", maxSplits: 1)
        if kv.count == 2, let v = Double(kv[1]) { out[String(kv[0])] = v }
      }
      return out
    }
    func text(_ r: [String: Double]) -> String {
      ["sx", "sy", "max", "vs", "n", "m", "sk", "sh"]
        .map { "\($0)=\(r[$0].map { String($0) } ?? "?")" }.joined(separator: " ")
        + (status.label.contains("err") ? "；\(status.label.components(separatedBy: "err").last ?? "")" : "")
    }
    func near(_ a: Double?, _ b: Double, _ tol: Double = 0.5) -> Bool {
      guard let a else { return false }
      return abs(a - b) <= tol
    }
    func sx() -> Double { read()["sx"] ?? -1 }
    func web(_ label: String) -> XCUIElement { app.webViews.buttons[label].firstMatch }
    /// 静止：有效的 sx 按单调时钟持续 ≥ 300ms 不变；最多等 timeout 秒，超时返回最后一次读数
    func settle(_ timeout: TimeInterval = 4) -> [String: Double] {
      let clock = { ProcessInfo.processInfo.systemUptime }
      let deadline = clock() + timeout
      var last = read()
      var since = clock()
      while clock() < deadline {
        Thread.sleep(forTimeInterval: 0.1)
        let now = read()
        if now["sx"] == nil || now["sx"] != last["sx"] {
          since = clock()
        } else if clock() - since >= 0.3 {
          return now
        }
        last = now
      }
      return last
    }
    func step(_ payload: [String: Any]) throws -> [String: Any] {
      seq += 1
      return try checkpoint(String(format: "p%02d", seq), payload)
    }
    func report(_ probe: String, _ pass: Bool?, _ detail: String) throws {
      _ = try step(["probe": probe, "pass": pass.map { $0 as Any } ?? NSNull(), "detail": detail])
    }
    func slowDrag(_ x0: CGFloat, _ y0: CGFloat, _ x1: CGFloat, _ y1: CGFloat) {
      point(app, x0, y0).press(
        forDuration: 0.1, thenDragTo: point(app, x1, y1),
        withVelocity: XCUIGestureVelocity(rawValue: 200), thenHoldForDuration: 0.3)
    }
    /// 卡片里的 open 与侧栏里的 close 在各自状态下处在同一屏幕位置：点这里 = 切换
    func toggle() { point(app, 47, 178).tap() }

    // C0：加载完停在关闭位，host_sink_set 生效
    var r = read()
    _ = poll(timeout: 10) {
      r = read()
      return (r["max"] ?? 0) > 0 && near(r["sx"], r["max"] ?? -1) && r["sk"] == 1
    }
    let w = r["max"] ?? 0
    let wPt = CGFloat(w)
    let dy: CGFloat = 640
    _ = try step([
      "probe": "C0", "pass": near(r["sx"], w) && r["vs"] == 1 && r["sk"] == 1,
      "detail": "\(text(r))；屏幕宽 \(screen.width)",
      "width": Double(screen.width), "height": Double(screen.height),
    ])

    func setMode(_ i: Int) {
      for _ in 0..<7 where read()["m"] != Double(i) {
        web("mode").tap()
        _ = poll(timeout: 1) { read()["m"] == Double(i) }
      }
    }
    /// 回到关闭位：先用当前方式的按钮，不行就在侧栏上左拖
    func ensureClosed() {
      if near(sx(), w) { return }
      if near(sx(), 0) {
        toggle()
        _ = poll(timeout: 3) { near(sx(), w) }
      }
      if !near(settle()["sx"], w) {
        slowDrag(0.85 * wPt, dy, 0.05 * wPt, dy)
        _ = settle()
      }
    }

    // 设原生底栏（sink 已接管，门禁放行）；脚本截设了底栏的关闭态
    web("tabbar").tap()
    _ = try step(["tabbar": true])

    /// 一次开或合：点切换，0.4s 与 2.4s 各读一次；2.4s 时仍在目标位才算到位
    func cycle(_ open: Bool) -> (ok: Bool, detail: String) {
      toggle()
      Thread.sleep(forTimeInterval: 0.4)
      let early = sx()
      Thread.sleep(forTimeInterval: 2.0)
      let late = read()
      return (near(late["sx"], open ? 0 : w), "0.4s sx=\(early)；2.4s \(text(late))")
    }

    // C2：原生 UIKit 动画开合（mode 4）、原生无动画（mode 5）、网页 instant（mode 1，对照）各三次往返
    for (i, name) in [(4, "native"), (5, "native-instant"), (1, "instant")] {
      ensureClosed()
      setMode(i)
      for k in 1...3 {
        let o = cycle(true)
        try report("C2-\(name)-open\(k)", o.ok, o.detail)
        guard o.ok else {
          ensureClosed()
          continue
        }
        let c = cycle(false)
        try report("C2-\(name)-close\(k)", c.ok, c.detail)
      }
    }

    // 打开态截图（S9 用）：原生动画打开
    ensureClosed()
    setMode(4)
    toggle()
    let o = settle()
    if near(o["sx"], 0) {
      _ = try step(["shot": "open"])
    } else {
      try report("OPEN", false, "拿不到打开态：\(text(o))")
    }
    ensureClosed()

    // S4：SinkFollow 接管时手指拖动与 CSS 吸附照常：慢拖 0.3W、0.7W；打开态在侧栏上左拖 0.7W
    slowDrag(60, dy, 60 + 0.3 * wPt, dy)
    let a = settle()
    slowDrag(60, dy, 60 + 0.7 * wPt, dy)
    let b = settle()
    slowDrag(0.85 * wPt, dy, 0.15 * wPt, dy)
    let c = settle()
    try report(
      "S4", near(a["sx"], w) && near(b["sx"], 0) && near(c["sx"], w),
      "0.3W → sx \(a["sx"] ?? -1)；0.7W → sx \(b["sx"] ?? -1)；打开态左拖 0.7W → sx \(c["sx"] ?? -1)")

    // C2p：手指拖开之后程序化关闭；手指拖回之后程序化打开（用户拖动留下的吸附索引与程序化落位的先后）。
    // 逐帧原生（4）、逐帧原生 + 期间关吸附（6）、原生一步落位（5）各一遍；sh = 页面发出的 host_sink_show 次数
    for (i, name) in [(4, "native"), (6, "native-snapoff"), (5, "native-instant")] {
      ensureClosed()
      setMode(i)
      slowDrag(60, dy, 60 + 0.7 * wPt, dy)
      let d0 = settle()
      let pc = cycle(false)
      try report("C2p-\(name)-close", pc.ok, "拖开后 sx=\(d0["sx"] ?? -1) sh=\(d0["sh"] ?? -1)；程序化关闭 \(pc.detail)")
      ensureClosed()
      slowDrag(60, dy, 60 + 0.3 * wPt, dy)
      let d1 = settle()
      let po = cycle(true)
      try report("C2p-\(name)-open", po.ok, "慢拖 0.3W 弹回后 sx=\(d1["sx"] ?? -1) sh=\(d1["sh"] ?? -1)；程序化打开 \(po.detail)")
      ensureClosed()
    }

    _ = try step(["end": true])
  }
}
