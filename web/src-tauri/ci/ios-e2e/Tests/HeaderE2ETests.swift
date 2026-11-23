// 原生页头的端到端 UI 测试（ios-e2e-check.sh 在底栏测试之后单独驱动，只在 CI 的 iOS 26+ 模拟器里跑）。
// Oryn.app 是 debug 构建：ORYN_E2E_URL 直接打开 mock Web UI 的一个线程页（/t/<MAIN_THREAD>），走与生产相同的
// open_remote（远程能力 + 宿主命令权限）。页面认出 iOS 壳（bridge ≥ 8）、让原生跟随根滚动后，由原生 Liquid Glass 页头
// 接管线程页的三件控件。卡片开合是 WKWebView 根滚动视图的横向滚动，原生页头在同一进程里按 contentOffset 同帧平移
// （打开时整层右移侧栏宽，只显示不接触摸），网页不发位移，只在网页内容盖住页头时让它让位。
// 步骤（测试写 <步骤>.req，脚本断言事件日志并截图后放行，见 E2ESupport.swift）：
//   h-loaded   原生侧栏钮出现：脚本确认 sink.set 先于原生接管、header.set（key = 线程 id）与 header.show（alpha 1），
//              回传三件矩形与侧栏宽
//   h-native   无障碍树：三件矩形里都是原生按钮，网页三件已退出无障碍树（aria-hidden）；记下原生侧栏钮在关闭位的矩形
//   h-menu     点原生标题胶囊：原生面板展开（出现「改名」行），脚本确认 header.event（menu = title）
//   h-sub      点「Orb」行：面板原地换成子页（「返回, Orb」行），不产生 select
//   h-back     点返回行：回到主页
//   h-dialog   点「改名」：脚本确认 header.event（select / title / rename）；网页打开改名对话框（无障碍树里有输入框），
//              遮挡探测让原生页头让位（header.show alpha 0，原生按钮消失）
//   h-return   点对话框上方的遮罩关掉它：原生页头回来
//   h-sidebar  点原生侧栏钮：脚本确认 header.event（lead）与原生推进的开合（sink.show）；原生页头不让位、随卡片右移侧栏宽
//              （原生侧栏钮左缘 = 关闭位 + 侧栏宽 ±1pt，原矩形里没有别的原生按钮，其余两件大部分在屏外）
//   h-closed   点露出的卡片条关侧栏：原生页头随卡片回到原位
//   h-drag     按住拖到 0.7 × 侧栏宽：按住期间测试阻塞在手势里取不了无障碍快照，脚本连拍截图断言原生页头与卡片停在同一位移；
//              松手过半吸附到打开（原生侧栏钮右移侧栏宽），再点卡片条关上
//   h-end      关上之后：原生页头回到原位，脚本确认整个过程没有 header.show
//   h-keyboard 点输入框、等系统软键盘真的弹出：脚本按网页探针的视口采样断言页面没被推走（视觉视口 offsetTop、scrollY 回到 0，
//              scrollX 仍是关闭位），输入框底边在键盘上沿之上（--kb-inset 让位），截图
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

    // 0. 线程页就绪且原生页头已显示：WebView 之外出现侧栏钮。
    //    冷启动等 300 秒：CI 的 macOS 机器整机卡顿时 WebView 首次加载曾超过 90 秒（脚本那头也等 420 秒）
    try until("300 秒内没有出现原生侧栏钮（线程页没打开，或原生页头没接管）", timeout: 300) {
      try self.native(app, self.sidebar) != nil
    }
    let screen = app.frame.size
    let loaded = try checkpoint("h-loaded")
    guard let lead = rect(loaded["lead"]), let title = rect(loaded["title"]),
      let sinkW = (loaded["sinkW"] as? Double).map({ CGFloat($0) })
    else {
      throw E2EFailure("脚本没有回传页头矩形与侧栏宽：\(loaded)")
    }
    // mock 线程有 orb，视图钮一定在；没有时脚本已判失败，这里不再要求
    let slots = [lead, title] + (rect(loaded["trail"]).map { [$0] } ?? [])

    // 1. 原生三件接管，网页三件隐藏；原生侧栏钮在关闭位的矩形是之后开合位移的基准
    try expectHeader(app, slots, native: true, "接管")
    guard let base = try native(app, sidebar)?.frame else {
      throw E2EFailure("接管后找不到原生侧栏钮")
    }
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
    guard app.webViews.textFields.firstMatch.waitForExistence(timeout: 10 * waitScale) else {
      throw E2EFailure("点「改名」后网页没有打开改名对话框（无障碍树里没有输入框）")
    }
    try expectHeader(app, slots, native: false, "对话框打开后")
    _ = try checkpoint("h-dialog")

    // 6. 点对话框上方的遮罩（页头所在的位置，原生已不截触摸）关掉它：原生页头回来
    point(app, screen.width / 2, title.midY).tap()
    try until("点遮罩后改名对话框没有关掉") { !app.webViews.textFields.firstMatch.exists }
    try expectHeader(app, slots, native: true, "对话框关掉后")
    _ = try checkpoint("h-return")

    // 7. 点原生侧栏钮：网页打开侧栏（原生推进根滚动），原生页头不让位，随卡片右移侧栏宽
    point(app, lead.midX, lead.midY).tap()
    try expectLead(app, slots, base, by: sinkW, "打开侧栏后")
    _ = try checkpoint("h-sidebar")

    // 8. 点露出的卡片条关侧栏：原生页头随卡片回到原位
    point(app, screen.width - 16, screen.height / 2).tap()
    try expectHeader(app, slots, native: true, "关侧栏后")
    try expectLead(app, slots, base, by: 0, "关侧栏后")
    _ = try checkpoint("h-closed")

    // 9. 在卡片左缘内（x = 8：转录的消息与代码块都有左右内边距，起手点落在纵向滚动的转录上，横拖沿滚动链交给根）
    //    起手拖到 0.7 × 侧栏宽并按住 40 秒：按住期间本测试阻塞在手势里，原生侧栏钮的位置只能由脚本连拍截图判定
    //    （页头行与卡片内容各自搜出的平移量一致）。没赶上时脚本回 retry，最多 3 次（同底栏测试）。
    //    过半松手：系统吸附到打开，原生侧栏钮右移侧栏宽；再点卡片条关上，回到原位。0.7 给起拖滞后留余量（见底栏测试）
    let dx = (sinkW * 0.7).rounded()
    let y = (screen.height / 2).rounded()
    var attempt = 1
    while true {
      let step = "h-drag\(attempt)"
      try post(step, ["dx": Double(dx)])
      point(app, 8, y).press(
        forDuration: 0.1, thenDragTo: point(app, 8 + dx, y),
        withVelocity: XCUIGestureVelocity(rawValue: 200), thenHoldForDuration: 40)
      let reply = try ack(step, timeout: 600)
      try expectLead(app, slots, base, by: sinkW, "第 \(attempt) 次按住拖动松手后")
      point(app, screen.width - 16, screen.height / 2).tap()
      try expectHeader(app, slots, native: true, "拖动后关侧栏")
      try expectLead(app, slots, base, by: 0, "拖动后关侧栏")
      guard reply["retry"] as? Bool == true else { break }
      attempt += 1
    }
    _ = try checkpoint("h-end")

    // 10. 点输入框弹出软键盘：iOS 会平移视觉视口把整页推上去，网页（lib/viewport.ts 的 settleViewport）应把它拉回，
    //     只让输入区按 --kb-inset 让出键盘。断言在脚本里（网页探针的视口采样），这里只确认键盘真的出来了
    let composer = app.webViews.textViews.firstMatch
    guard composer.waitForExistence(timeout: 10 * waitScale) else {
      throw E2EFailure("线程页的无障碍树里没有输入框")
    }
    composer.tap()
    guard app.keyboards.firstMatch.waitForExistence(timeout: 15 * waitScale) else {
      throw E2EFailure("点输入框后 \(Int(15 * waitScale)) 秒内没有弹出软键盘（模拟器连着硬件键盘？）")
    }
    _ = try checkpoint("h-keyboard")
  }
}

@MainActor
extension HeaderE2ETests {
  /// WebView 之外第一个读屏文字匹配的按钮（`prefix` 时按前缀匹配）。
  private func native(_ app: XCUIApplication, _ labels: [String], prefix: Bool = false) throws -> Snap? {
    native(try allButtons(of: app), labels, prefix: prefix)
  }

  private func native(
    _ all: (native: [Snap], web: [Snap]), _ labels: [String], prefix: Bool = false
  ) -> Snap? {
    all.native.first { snap in
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
      let all = try self.allButtons(of: app)
      for slot in slots {
        let b = self.buttons(in: slot, among: all)
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

  /// 等到原生侧栏钮停在关闭位矩形 `base` 右移 `dx` 处：左缘、竖直中心各 ±1pt（位移由原生按根滚动的 contentOffset 算，
  /// 静止在打开位时恰为侧栏宽）。`dx > 0` 时三件原矩形里不能再有别的原生按钮（侧栏钮右移后可能正好落在视图钮的原位上，
  /// 不算；标题与视图钮随之右移，大部分在屏外，不作要求）。
  private func expectLead(
    _ app: XCUIApplication, _ slots: [CGRect], _ base: CGRect, by dx: CGFloat, _ step: String
  ) throws {
    var others: [[String]] = []
    var seen: Snap?
    let ok = try poll(timeout: 20) {
      let all = try self.allButtons(of: app)
      seen = self.native(all, self.sidebar)
      others =
        dx > 0
        ? slots.map { slot in
          self.buttons(in: slot, among: all).native.map(\.label).filter { !self.sidebar.contains($0) }
        } : []
      guard let snap = seen else { return false }
      return others.allSatisfy { $0.isEmpty }
        && abs(snap.frame.minX - (base.minX + dx)) <= 1
        && abs(snap.frame.midY - base.midY) <= 1
    }
    if !ok {
      let at = seen.map { "\($0.frame)" } ?? "没有"
      throw E2EFailure(
        "\(step)：期望原生侧栏钮停在关闭位右移 \(dx)pt 处（左缘 \(base.minX + dx)）；"
          + "原生侧栏钮 \(at)，原矩形里的其它原生按钮 \(others)")
    }
  }
}
