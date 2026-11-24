// 推送单元测试的宿主：测试包注入这个进程跑（钥匙串要在 App 进程里才能用），这里什么都不做。
import SwiftUI

@main
struct PushUnitHost: App {
  var body: some Scene {
    WindowGroup { Text("Oryn push unit tests") }
  }
}
