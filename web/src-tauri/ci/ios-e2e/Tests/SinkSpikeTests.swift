// M20 阶段 1 探针（ci/ios-sink-spike 分支专用，不合入；ios-e2e-check.sh 在本分支上只跑这一段）。
// 问题：WKWebView 的根 UIScrollView 能不能承载下沉卡片（plan-m20 假设 A1–A6）。
// 探针页 spike/sink-spike.html：根元素横向可滚并强制吸附，侧栏 position: fixed 垫底，卡片在右侧 in-flow。
// 读数来自状态按钮的无障碍 label（sx=…;sy=…;…）。每个探针把 {probe, pass, detail} 交给脚本写进 Step Summary；
// 探针不过不抛错（要拿到全部读数），由脚本汇总判定。S5（按住拖动）与 S9（底栏透出）由脚本按截图判定。
import XCTest

final class SinkSpikeTests: E2ECase {
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
      ["sx", "sy", "iw", "max", "vs", "vy", "kb", "vst", "pre", "n", "g"]
        .map { "\($0)=\(r[$0].map { String($0) } ?? "?")" }.joined(separator: " ")
    }
    func near(_ a: Double?, _ b: Double, _ tol: Double = 0.5) -> Bool {
      guard let a else { return false }
      return abs(a - b) <= tol
    }
    func web(_ label: String) -> XCUIElement { app.webViews.buttons[label].firstMatch }
    /// sx 连续两次读数（间隔 0.3s）不变即视为静止；最多等 timeout 秒
    func settle(_ timeout: TimeInterval = 4) -> [String: Double] {
      var prev = read()
      _ = poll(timeout: timeout) {
        let now = read()
        defer { prev = now }
        return now["sx"] == prev["sx"]
      }
      return prev
    }
    func report(_ probe: String, _ pass: Bool?, _ detail: String) throws {
      _ = try checkpoint(
        probe.lowercased(),
        ["probe": probe, "pass": pass.map { $0 as Any } ?? NSNull(), "detail": detail])
    }
    func slowDrag(_ x0: CGFloat, _ y0: CGFloat, _ x1: CGFloat, _ y1: CGFloat) {
      point(app, x0, y0).press(
        forDuration: 0.1, thenDragTo: point(app, x1, y1),
        withVelocity: XCUIGestureVelocity(rawValue: 200), thenHoldForDuration: 0.3)
    }

    // S1：加载完停在关闭位，无缩放、无纵向位移
    var r = read()
    _ = poll(timeout: 10) {
      r = read()
      return (r["max"] ?? 0) > 0 && near(r["sx"], r["max"] ?? -1)
    }
    let w = r["max"] ?? 0
    let wPt = CGFloat(w)
    func close() {
      if !near(read()["sx"], w) {
        web("close").tap()
        _ = poll(timeout: 3) { near(read()["sx"], w) }
      }
    }
    _ = try checkpoint(
      "s1",
      [
        "probe": "S1",
        "pass": near(r["sx"], w) && r["sy"] == 0 && r["vs"] == 1
          && near(r["iw"], Double(screen.width), 0),
        "detail": "\(text(r))；屏幕宽 \(screen.width)",
        "width": Double(screen.width), "height": Double(screen.height),
      ])

    // S9 准备：设原生底栏（脚本先截了未设底栏的关闭态，等 tabbar.set 后再截一张关闭态）
    web("tabbar").tap()
    _ = try checkpoint("s9set")

    // S2：点 open → 2s 内 sx = 0；卡片内按钮与卡片内 fixed 徽标右移 W，状态按钮与侧栏按钮不动
    let names = ["open", "badge", "close"]
    let f0 = Dictionary(uniqueKeysWithValues: names.map { ($0, web($0).frame) })
    let s0 = status.frame
    web("open").tap()
    var f1 = f0
    var s1 = s0
    let opened = poll(timeout: 2) {
      r = read()
      f1 = Dictionary(uniqueKeysWithValues: names.map { ($0, web($0).frame) })
      s1 = status.frame
      return near(r["sx"], 0)
        && abs(f1["open"]!.minX - f0["open"]!.minX - wPt) <= 1
    }
    let shift = { (k: String) in Double(f1[k]!.minX - f0[k]!.minX) }
    let statusShift = Double(s1.minX - s0.minX)
    try report(
      "S2",
      opened && near(shift("open"), w, 1) && near(shift("badge"), w, 1)
        && near(shift("close"), 0, 1) && near(statusShift, 0, 1),
      "\(text(r))；open +\(shift("open")) badge +\(shift("badge")) close +\(shift("close")) status +\(statusShift)"
    )

    // S3：点 close → 2s 内 sx = W
    web("close").tap()
    let closed = poll(timeout: 2) {
      r = read()
      return near(r["sx"], w)
    }
    try report("S3", closed, text(r))

    // S4：关闭态卡片中部慢拖 0.3W、0.7W；打开态在侧栏上左拖 0.7W
    let dy: CGFloat = 640
    slowDrag(60, dy, 60 + 0.3 * wPt, dy)
    let a = settle()
    slowDrag(60, dy, 60 + 0.7 * wPt, dy)
    let b = settle()
    slowDrag(0.85 * wPt, dy, 0.15 * wPt, dy)
    let c = settle()
    try report(
      "S4", near(a["sx"], w) && near(b["sx"], 0) && near(c["sx"], w),
      "0.3W → sx \(a["sx"] ?? -1)；0.7W → sx \(b["sx"] ?? -1)；打开态左拖 0.7W → sx \(c["sx"] ?? -1)")
    close()

    // S5：按住拖到 0.55W 不放，脚本在按住期间连拍截图并从事件日志 tabbar.reveal 读 sx；没赶上就收起重拖（最多 3 次）
    let dx = (wPt * 0.55).rounded()
    var attempt = 1
    while true {
      let step = "s5_\(attempt)"
      try post(step, ["dx": Double(dx)])
      point(app, 40, dy).press(
        forDuration: 0.1, thenDragTo: point(app, 40 + dx, dy),
        withVelocity: XCUIGestureVelocity(rawValue: 200), thenHoldForDuration: 40)
      let reply = try ack(step, timeout: 600)
      _ = settle()
      close()
      guard reply["retry"] as? Bool == true else { break }
      attempt += 1
    }

    // S6：纵向滚动区（y 220…370）上滑 300pt：内层滚了，根不动、根没有 scroll 事件
    let n0 = read()["n"] ?? -1
    point(app, screen.width / 2, 360).press(
      forDuration: 0.05, thenDragTo: point(app, screen.width / 2, 60),
      withVelocity: XCUIGestureVelocity(rawValue: 600), thenHoldForDuration: 0.2)
    r = settle()
    _ = poll(timeout: 2) {
      r = read()
      return (r["vst"] ?? 0) > 0
    }
    try report(
      "S6", (r["vst"] ?? 0) > 0 && near(r["sx"], w) && r["n"] == n0, "\(text(r))；之前 n=\(n0)")

    // S7：pre（y 390…454）上左滑：只滚 pre；再复位到最左后右滑：只记录 sx
    let py: CGFloat = 422
    slowDrag(300, py, 100, py)
    r = settle()
    try report("S7a", (r["pre"] ?? 0) > 0 && near(r["sx"], w), text(r))
    web("reset").tap()
    slowDrag(100, py, 300, py)
    r = settle()
    try report("S7b", nil, "最左处右滑后 \(text(r))")
    close()

    // S8：点 textarea 等键盘，再收起；先不开守卫，再开守卫重做一次
    func keyboardRound(_ name: String) throws {
      point(app, 100, 504).tap()
      let shown = app.keyboards.firstMatch.waitForExistence(timeout: 10)
      Thread.sleep(forTimeInterval: 1)
      let up = read()
      web("reset").tap()
      _ = poll(timeout: 10) { !app.keyboards.firstMatch.exists }
      Thread.sleep(forTimeInterval: 1)
      let down = read()
      let detail = "键盘\(shown ? "出现" : "没出现")；在时 \(text(up))；收起后 \(text(down))"
      if !shown {
        try report(name, nil, detail)
        return
      }
      try report(
        name,
        near(up["sx"], w) && near(down["sx"], w) && down["sy"] == 0 && down["vy"] == 0,
        detail)
      close()
    }
    try keyboardRound("S8")
    web("guard").tap()
    _ = poll(timeout: 2) { read()["g"] == 1 }
    try keyboardRound("S8g")
    web("guard").tap()

    _ = try checkpoint("end")
  }
}
