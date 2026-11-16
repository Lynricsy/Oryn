// UI 测试包的空宿主：测试驱动的是另行安装的 Oryn.app，这里什么都不做。
import SwiftUI

@main
struct HostApp: App {
  var body: some Scene {
    WindowGroup { Text("Oryn E2E host") }
  }
}
