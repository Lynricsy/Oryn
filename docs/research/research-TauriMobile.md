# Oryn 客户端：React+TS Web UI + Tauri 2 套壳可行性调研报告

调研日期 2026-11-12。**本沙箱没有 shell，无法执行 `cargo --version` 和 `bun --version`，本机版本「未找到」。**

标记说明：**[官方]** 官方支持，**[社区]** 社区插件，**[自研]** 需自写 Swift/Kotlin 或 Rust，**[推测]** 未亲自读到来源。

---

## 0. 结论摘要

- Tauri 2 稳定线是 2.12.x，最新为 `tauri v2.12.5`（2026-10-21）。
- Tauri 3 仍是 alpha（`3.0.0-alpha.6`，2026-11-06）。它新增可选的 CEF（捆绑 Chromium）运行时，但只支持桌面端，官方建议生产继续用 2.x。
- 桌面三平台和 Android/iOS 的基础能力（通知、深链、托盘、自启动、更新器、生物识别）官方插件都有。
- 硬缺口有四个：
  1. 远程推送（APNs/FCM）没有官方方案，只有社区插件。
  2. 从其它 App 分享进来（iOS Share Extension、Android SEND intent）没有官方方案，需社区插件加自写原生代码。
  3. Linux WebKitGTK 2.54 的 WebRTC 被上游禁用，且 WebAuthn 未实现。
  4. 移动端没有官方 Keychain/Keystore 插件，Stronghold 已被维护者表示「不再推荐，v3 将移除」。
- iOS 后台 WebSocket 会被挂起，所有平台上线程完成通知都必须走「服务端推送」。
- Windows、macOS、iOS 产物不能靠 Linux 服务器原生产出：
  - Windows 只有 NSIS 可用 `cargo-xwin` 交叉编译，官方称「最后手段」。
  - macOS 和 iOS 需要 macOS 宿主。
  - 实际应走 GitHub Actions 多平台 runner。

---

## 1. 版本、移动端成熟度、WebView 差异

### 1.1 版本

- **Tauri 稳定版：** `tauri v2.12.5`，2026-10-21（https://github.com/tauri-apps/tauri/releases.atom）。
- **Tauri 3 alpha：** `tauri v3.0.0-alpha.6`、`tauri-runtime-cef v3.0.0-alpha.7`、`@tauri-apps/cli v3.0.0-alpha.6`，均为 2026-11-06（同上）。
- **插件仓库最新（2026-11-10）：** notification 2.8.1、store 2.5.1、opener 2.7.3、http 2.9.0、log 2.10.2（https://github.com/tauri-apps/plugins-workspace/releases.atom）。
- **Rust 版本要求：** 官方插件页标注 Rust ≥1.90（https://v2.tauri.app/plugin/notification/）。Tauri 3 要求 Rust ≥1.95（https://v3.tauri.app/start/migrate/from-tauri-2/）。
- **Tauri 3 的变化**（https://v3.tauri.app/start/migrate/from-tauri-2/、https://v3.tauri.app/develop/cef/）：
  - 运行时改为直接依赖 `tauri-runtime-wry`（系统 WebView）或 `tauri-runtime-cef`（捆绑 Chromium）。
  - 2.x 插件不能直接注册到 3.0 应用，社区插件需作者适配，`tauri migrate` 暂不自动迁移。
  - CEF 只支持 Windows/macOS/Linux，移动端仍用 wry。
  - CEF 包体「大几百 MB」，首次构建下载约 1 GB。
  - CEF 在 Windows 上目前无进程沙箱（Chromium M138 之后的限制）。
  - Linux 托盘改走 ksni（StatusNotifierItem）。
- **移动端成熟度：**
  - 2.0 于 2024-10-02 发布稳定版并含移动端（https://v2.tauri.app/blog/tauri-20/，来自搜索摘要）。
  - 官方以「Swift/Kotlin 插件」承载原生能力（https://v2.tauri.app/develop/plugins/develop-mobile/）。
  - 前台型应用可用；后台常驻、深度原生集成需要自写插件（搜索摘要，非官方原话）。

### 1.2 各平台 WebView

来源：https://v2.tauri.app/reference/webview-versions/

| 平台 | 引擎 | 要点 |
|---|---|---|
| Windows | WebView2（Chromium） | 可自更新；Win11 预装；Win10 及以下安装器负责补装 |
| macOS / iOS | WKWebView | 随系统更新，旧系统不更新 |
| Linux | WebKitGTK | 版本取决于发行版 |
| Android | 系统 Android WebView（Chromium） | 版本取决于设备的 WebView provider |

文档里的 WebKitGTK 发行版表只到 2.36（Ubuntu 22.04）等旧版本，偏旧，不能代表现状。

### 1.3 Linux WebKitGTK（风险最大）

- **WebRTC：**
  - WebKitGTK 2.54（2026-09-16）官方 highlights 写明：GStreamer WebRTC 后端被替换为 LibWebRTC，预计「下一个周期」可用；以往需实验性构建的 WebRTC 在 2.54 中被**禁用**（https://www.webkitgtk.org/2026/09/16/webkitgtk-2.54-highlights.html）。
  - 下一个系列 2.56 预计 2027 年春。
  - 结论：Linux 上依赖 WebRTC 的功能不可靠。
- **麦克风 `getUserMedia`：**
  - WebKitGTK 的 `permission-request` 信号若应用不处理，`UserMediaPermissionRequest` 默认拒绝（https://webkitgtk.org/reference/webkit2gtk/stable/signal.WebView.permission-request.html，来自搜索摘要，我未直接读全文）。
  - Tauri/WRY 在 Linux 上是否替你处理：「未找到明确文档」。
  - 对应 Tauri 的 `with_webview` 可自写处理。
- **WebGL / canvas / xterm.js：**
  - Tauri 官方 Linux 图形页（https://v2.tauri.app/develop/debug/linux-graphics/）：
    - NVIDIA 下可能出现白屏、闪烁、Wayland `Error 71`。
    - 处理顺序：`__NV_DISABLE_EXPLICIT_SYNC=1` → `WEBKIT_DISABLE_DMABUF_RENDERER=1` → `WEBKIT_DISABLE_COMPOSITING_MODE=1`（最后手段）。
    - WebGL2 上下文创建成功不代表硬件加速。
    - WebKitGTK 把 `WEBGL_debug_renderer_info` 屏蔽为「Apple GPU」，无法检测真实后端。
    - 官方建议 WebGL 重度视图（含终端模拟器）在 Linux 提供非 WebGL 回退和用户开关。
  - WebKitGTK 2.54 的新 Skia 合成器大幅提升性能；Cairo 2D 渲染已移除。
- **WebAuthn：** WebKit 上游 issue「[WPE][GTK] Support WebAuthn」（https://bugs.webkit.org/show_bug.cgi?id=205350）截至搜索时仍为 NEW，视为不可用。
- **IndexedDB：** 2.54 新增 `getAllRecords()`。
- **Service Worker：** 本次未找到 Tauri 自定义协议（`tauri://` 或 `http://tauri.localhost`）下 Service Worker 的官方说明。不要依赖 SW，同源 `https` 的 Web 版/PWA 才可靠 [推测]。

### 1.4 iOS WKWebView / Android WebView / WebView2

- **iOS `getUserMedia`：** iOS 14.3 起 WKWebView 支持（WWDC21 10032，来自搜索摘要，https://developer.apple.com/videos/play/wwdc2021/10032/）。
  - 需在 `src-tauri/Info.ios.plist` 加 `NSMicrophoneUsageDescription`。
  - wry 的 `WKUIDelegate` 在没有自定义 handler 时默认授权，这一点来自搜索对 wry 源码的引用，我未亲自读到源码。
- **Android `getUserMedia`：** wry 的 `RustWebChromeClient` 把 `RESOURCE_AUDIO_CAPTURE` 映射为 `RECORD_AUDIO` 加 `MODIFY_AUDIO_SETTINGS` 运行时权限请求（搜索摘要，源码 https://raw.githubusercontent.com/tauri-apps/wry/dev/src/android/kotlin/RustWebChromeClient.kt）。
  - 清单里需手工声明两项权限。
  - 以锁定版本的 wry 源码为准。
- **xterm.js：**
  - WebGL 渲染器（WebGL2）自 xterm 5.1 起支持 Safari 16+。
  - Canvas 渲染器在 5.0 拆为独立 addon，6.0 彻底移除，只剩 DOM 和 WebGL 两种。
  - 历史上有 WKWebView 的 UA 检测问题（issue #3575）。
  - 推荐做法：WebGL 加载放 `try/catch`，监听 `onContextLoss` 后 `dispose()` 回落到 DOM 渲染（来自 xterm 官方 addon README，经搜索摘要）。
- **WebCodecs：** 本次未查到各 WebView 的确切结论，Oryn 听写走 MediaRecorder 即可，WebCodecs「未找到」。
- **WebSocket：** 各 WebView 的原生 `WebSocket` 均可用 [推测]。Tauri 另有官方 `websocket` 插件，把 Rust 客户端暴露给 JS，各平台均标 Full（https://v2.tauri.app/plugin/，支持表）。
- **剪贴板 / 拖放：** 官方 `clipboard-manager` 插件存在（https://v2.tauri.app/plugin/，列表）。我只读到支持表的后半截，各平台级别「未完整读到」。
  - 桌面拖入文件：Tauri 窗口有拖放事件，具体行为「未在本次核实」[推测]。
  - 移动端的 `dialog` 与 `fs` 在支持表中标为 Partial（https://v2.tauri.app/plugin/）。

---

## 2. 通知

### 2.1 本地通知 [官方]

- 插件：`tauri-plugin-notification`，最新 2.8.1（2026-11-10）。
- 各平台均 Full（https://v2.tauri.app/plugin/notification/）。
  - Windows 备注：只对已安装的应用有效，开发态显示 PowerShell 的名称和图标。
- 能力：权限请求、动作按钮（含文本输入）、附件、渠道（Android channel）、`onAction` 监听。
- 2.8.1 修复了「Android 13+ 权限已授予时请求永不 resolve」（https://github.com/tauri-apps/plugins-workspace/releases.atom）。

### 2.2 远程推送（APNs / FCM）[社区]，非官方

官方 notification 文档只覆盖本地通知；以下 4 个社区方案来自搜索（https://github.com/spicavi/tauri-plugin-push-notifications 等），我只亲自读了 Choochmeque 的 README。

| 插件 | 方案 | 备注 |
|---|---|---|
| Choochmeque/tauri-plugin-notifications（0.5） | 本地加推送，FCM/APNs，Linux 用 UnifiedPush | `push-notifications` feature 默认关闭；Windows 推送不支持；88 星 24 个 issue |
| spicavi/tauri-plugin-push-notifications | APNs/FCM token，前台接收、点击、冷启动队列 | 要求 iOS 15+；版本 0.1 |
| srod/tauri-plugin-fcm | 两端都用 FCM token | iOS 走 APNs→FCM |
| yanqianglu/tauri-plugin-mobile-push | APNs/FCM token | README 自承 iOS 的 receive/tap/token-refresh 监听注册后不触发事件 |

- 来源：Choochmeque 的 README（https://github.com/Choochmeque/tauri-plugin-notifications）；其余为搜索摘要。
- 是否需要自写 Swift/Kotlin：
  - 用社区插件时，插件已内含 Swift/Kotlin（它们自带 `TauriFirebaseMessagingService.kt` 等）。
  - 业务侧仍要自己做：
    - Firebase 项目和 `google-services.json`。
    - APNs 证书/Key。
    - iOS 的 `aps-environment` 能力配置。
    - 服务端用 Go 推送（见下文）。
  - 若这些插件质量不够，则回落到**自研**。
- 推送服务端建议（Go）[推测]：
  - 存设备 token。
  - 线程完成事件触发时：iOS 走 APNs HTTP/2，Android 走 FCM HTTP v1。
  - 点击通知的载荷带 `threadId`，客户端用深链打开。

### 2.3 桌面后台常驻与通知

- 托盘 API 官方内置（`tauri` 的 `tray-icon` feature，https://v2.tauri.app/learn/system-tray/）。
- 关窗隐藏到托盘后，进程继续跑，WebSocket 可继续收，再用本地通知提醒 [推测，合理但本次未专门验证]。
- Linux 托盘在 Tauri 3 默认改用 ksni（见 1.1）。

### 2.4 Web Push

- iOS：只有「添加到主屏幕」的 Web App 支持，需 iOS 16.4+，用 Push API + Notifications API + Service Worker 组合；普通 Safari 标签和内嵌 `WKWebView` 都不支持（https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/）。
- 所以 Tauri 的 iOS WebView 内不能用 Web Push，必须用 APNs。
- Android WebView、Linux WebKitGTK、WKWebView(macOS) 内的 Web Push：本次**未找到**依据，按「不可用」设计。
- Web Push 对 PWA 形态才有意义（见第 10 节）。

---

## 3. 深链与单实例

### 3.1 deep-link [官方]

来源：https://v2.tauri.app/plugin/deep-linking/（Rust ≥1.90）

| 平台 | 支持级别 | 备注 |
|---|---|---|
| Windows / Linux | Full | 通过命令行参数传入新进程 |
| macOS / Android / iOS | Partial | 必须在配置中静态注册，不支持运行时动态注册 |

- **自定义 scheme（如 `oryn://`）：** 移动端用 `"appLink": false`，无需服务器；桌面用 `plugins.deep-link.desktop.schemes`。
- **HTTPS 通用链接：**
  - Android App Links：服务器需 `/.well-known/assetlinks.json`，含包名和签名证书 SHA256；`package_name` 来自 `identifier`，`-` 换成 `_`。
  - iOS Universal Links：服务器需 `/.well-known/apple-app-site-association`，`appIDs` 为 `TEAMID.bundleID`，可用 `curl https://app-site-association.cdn-apple.com/a/v1/<host>` 校验。
  - 桌面端 HTTPS 链接：文档示例**只给了自定义 scheme**，桌面 HTTPS 通用链接「未找到」。
- **只读分享链接的启示：** 分享页的域名可以同时是 AASA/assetlinks 的承载域。已装 App 的设备点链接可直接进入原生应用，未装则落到 Web。

### 3.2 单实例 [官方]

- 插件：`tauri-plugin-single-instance`，仅桌面（https://v2.tauri.app/plugin/，支持表）。
- 带 `deep-link` feature 时，深链会转发给已有实例；必须是第一个注册的插件（https://v2.tauri.app/plugin/deep-linking/）。
- Tauri 3 CEF 下的深链：`RunEvent::Opened` 仍会触发，由运行时还原命令行（https://v3.tauri.app/develop/cef/）。

---

## 4. 系统分享入口、文件、相机、拖放、粘贴

- **iOS Share Extension：** [自研加社区] 官方无支持。
  - 需新增 Share Extension target，并用 App Group 共享存储，主 App 启动或恢复时导入。
  - Apple 对 Share Extension 自动拉起主 App 有限制。搜索摘要称「Apple DTS 警告绕过扩展限制访问 `UIApplication` 不受支持且可能被拒审」，来源是 Apple 开发者论坛，我未亲自读到。
  - Tauri 无 Xcode 扩展 target 的集成方案，需要手改生成的 Xcode 工程。
  - 社区插件 `tauri-plugin-sharehub 0.1.3`（队列 API `getPendingShares()`/`readSharedItem(id)`，含 iOS 扩展模板，https://docs.rs/crate/tauri-plugin-sharehub/latest）和 `tauri-plugin-mobile-sharetarget`（FIFO 队列，https://github.com/IT-ess/tauri-plugin-mobile-sharetarget）。
  - 官方插件页收录了 `mobile-sharetarget` 和 `sharesheet`（后者仅是「向外分享」）（https://v2.tauri.app/plugin/）。
- **Android intent filter：** 改 `src-tauri/gen/android/app/src/main/AndroidManifest.xml`，在主 Activity 里加 `ACTION_SEND` / `ACTION_SEND_MULTIPLE` 的 `intent-filter`。
  - 官方模板的主 Activity 已是 `exported="true"`、`launchMode="singleTask"`（https://raw.githubusercontent.com/tauri-apps/tauri/dev/crates/tauri-cli/templates/mobile/android/app/src/main/AndroidManifest.xml）。
  - 收到的是 `content://` URI，必须经 `ContentResolver` 读取，不能当文件路径（搜索摘要，来源 Android 开发者文档）。
  - Tauri 的 Kotlin 插件有 `onNewIntent` 钩子，可用来接收新的 intent（https://v2.tauri.app/develop/plugins/develop-mobile/）。
  - `gen/android` 由 `tauri android init` 生成，可能被覆盖。需要把这些改动做成插件或模板脚本固化，避免丢失 [推测]。
- **文件 / 相册 / 相机：**
  - `dialog` 插件，移动端 Partial（https://v2.tauri.app/plugin/）。
  - `barcode-scanner`（扫码）官方有；通用相机拍照官方无插件，社区有 `tauri-plugin-camera`（kessdev，搜索摘要）。
  - 浏览器原生 `<input type=file accept=image/*>` 在移动 WebView 里通常可调起系统选择器/相机 [推测]。
- **粘贴图片：** Web 端 `paste` 事件加 `clipboardData.files`。各 WebView 行为「本次未核实」。

---

## 5. 托盘、全局快捷键、自启动、更新、多窗口、窗口状态

| 能力 | 状态 | 平台 | 来源 |
|---|---|---|---|
| 托盘 | [官方] 核心 API，`tray-icon` feature | 仅桌面 | https://v2.tauri.app/learn/system-tray/ |
| 全局快捷键 | [官方] `global-shortcut` | 仅桌面 | https://v2.tauri.app/plugin/ 支持表 |
| 自启动 | [官方] `autostart` | 桌面 Full，移动 No | 同上（列表和 autostart 行） |
| 单实例 | [官方] | 仅桌面 | 同上 |
| 窗口状态 | [官方] `window-state` | 仅桌面（Windows/Linux/macOS Full，Android/iOS No） | https://v2.tauri.app/plugin/window-state/ |
| Updater | [官方] | 仅桌面 | https://v2.tauri.app/plugin/updater/ |
| 多窗口 | [官方] 核心 API | 桌面可用；移动端为单 Activity，多窗口行为「未找到」 | — |

- **Updater 细节**（https://v2.tauri.app/plugin/updater/）：
  - 签名强制，不能关闭；私钥丢失则已安装用户无法再更新。
  - `tauri signer generate` 生成密钥，环境变量 `TAURI_SIGNING_PRIVATE_KEY`，`.env` 无效。
  - 产物：Linux `.AppImage` + `.sig`；macOS `.app.tar.gz` + `.sig`；Windows `-setup.exe`/`.msi` + `.sig`。
  - 静态 JSON（如 GitHub Release 的 `latest.json`）或动态服务器两种；生产强制 TLS。
  - Windows `installMode` 有 `passive`（默认）/`basicUi`/`quiet`。
  - `createUpdaterArtifacts: "v1Compatible"` 将在 v3 移除。
  - Linux 只有 AppImage 能自更新，`.deb`/`.rpm` 通过包管理器。
- **移动端更新：** Updater 不支持 Android/iOS，走商店/TestFlight/APK 分发（官方 plugin 页 Support Table 与 updater 页均为 No support）。
- **窗口状态恢复：** 移动端没有 `window-state`，Android 的状态恢复靠 Activity 重建和 Web 层自身持久化（Zustand/IndexedDB 等）[推测]。

---

## 6. 安全存储、生物识别、Passkey

### 6.1 安全存储

- **Stronghold [官方，不推荐]：**
  - 全平台 Full（https://v2.tauri.app/plugin/stronghold/）。
  - 维护者在 discussion #7846 说「stronghold is no longer recommended and will be deprecated and therefore removed in v3」（https://github.com/orgs/tauri-apps/discussions/7846，经搜索摘要）。
  - 官方文档仍提供安装步骤，要求用密码做 hash，并带一个 scrypt 上游 bug 的 `opt-level=3` 提示。
  - **新项目不建议采用。**
- **移动端 Keychain / Keystore [社区]：**
  - `impierce/tauri-plugin-keystore`：Android Keystore 与 iOS Keychain，仅移动端，假定已录入生物识别，需 Tauri ≥2.8.0（https://github.com/impierce/tauri-plugin-keystore）。
  - `Abdullah-5603/tauri-plugin-secure-keystore`：版本 0.0.2、较新、未审计，不建议无审查使用（https://github.com/Abdullah-5603/tauri-plugin-secure-keystore）。
  - 桌面端系统凭据库可通过 Rust 的 keyring crate 自行封装 [推测，本次未核实具体 crate 版本]。
- **官方 `tauri-plugin-store`**（2.5.1）是明文 KV，不适合存令牌。

### 6.2 生物识别 [官方]

- `biometric` 插件：macOS/Android/iOS Full，Windows/Linux No（https://v2.tauri.app/plugin/biometric/）。
- iOS 需在 `Info.ios.plist` 加 `NSFaceIDUsageDescription`。
- 提供 `checkStatus()` / `authenticate()`；还有 `biometric_cipher`（加解密）需显式授权 `biometric:allow-biometric-cipher`。
- 单用户场景：把长期令牌用生物识别门控的方式存取，可考虑 `biometric_cipher` 或 impierce 的 keystore 插件。

### 6.3 Passkey / WebAuthn

来源：搜索摘要，涉及的原始文档已列出。

| 平台 | 状态 |
|---|---|
| Windows WebView2 | 有 Windows Hello 失败的 bug 报告（MicrosoftEdge/WebView2Feedback#5663），不能假设与 Edge 一致 |
| macOS/iOS WKWebView | 需 Associated Domains 把 RP ID 与 App 关联；第三方登录应改用 `ASWebAuthenticationSession`（https://developer.apple.com/documentation/authenticationservices/supporting-passkeys/） |
| Linux WebKitGTK | 上游未实现，视为不可用（https://bugs.webkit.org/show_bug.cgi?id=205350） |
| Android WebView | 支持，但需原生启用 `WebSettingsCompat.setWebAuthenticationSupport(...FOR_APP)`、Digital Asset Links，且不支持 `mediation: "conditional"`（https://developer.android.com/identity/sign-in/credential-manager-webview） |

- **RP ID 与源不匹配：** Tauri 应用的 `location.origin` 是 `tauri://localhost` 或 `http://tauri.localhost`，与服务端域名的 RP ID 不满足 WebAuthn 的关联规则。
- **推荐方案：** 登录用系统浏览器或 `ASWebAuthenticationSession`/Custom Tabs 走 OAuth/OIDC+PKCE（RFC 8252），回调通过深链回到 App，再把令牌存入 Keychain/Keystore。单用户场景也可用「口令加 TOTP」或「设备配对码」。

---

## 7. 音频录制、麦克风、后台

- **录制：** WebView 的 `MediaRecorder`/`getUserMedia`，配权限（见 1.4）。
- **权限汇总：**
  - Android：`RECORD_AUDIO` + `MODIFY_AUDIO_SETTINGS`。
  - iOS：`NSMicrophoneUsageDescription`。
  - Linux：见 1.3，需自处理权限请求。
  - macOS：应用沙箱/签名权限（Hardened Runtime 的 audio-input entitlement）[推测，未专门查]。
- **后台限制：**
  - iOS：App 进入后台后会被挂起，后台任务不是常驻联网权限；后台推送唤醒不保证送达（Apple 文档，经搜索摘要：https://developer.apple.com/documentation/uikit/about-the-background-execution-sequence）。
  - Android：Doze/App Standby 限制后台网络，官方建议用 FCM；Android 15 起后台 `dataSync` 等前台服务有每 24 小时 6 小时的配额（https://developer.android.com/develop/background-work/services/fgs，经搜索摘要）。
- **`backgroundThrottling`：** Tauri 配置里 iOS 17.0+ 支持，Android 不支持。它与 OS 挂起是两回事，不能指望它保活。
- **Oryn 的设计：**
  - 前台：WebSocket（或 SSE）加持久化游标。
  - 后台：APNs/FCM 推「线程完成」，点击通知经深链打开具体线程。
  - 恢复：用游标拉取错过的事件，去重。
  - Android 如需实时，可另评估前台服务，但这是自写原生代码 [推测]。

---

## 8. 多形态设备适配

### 8.1 窗口尺寸类（Window size classes）

按 Material/Android 的 window size class，宽度断点为 compact <600dp、medium 600–839dp、expanded ≥840dp。这是我的既有知识，**本次未亲自读到官方页面**，请对照 https://developer.android.com/develop/ui/compose/layouts/adaptive/use-window-size-classes 校验。Android 的 Large/Extra-large 宽度新类（1200/1600dp）通过 `supportLargeAndXLargeWidth = true` 启用，来自我读到的 Android 文档示例，但其具体数值我这次没读到。

- Android 官方原则：布局决策基于分配给 App 的实际窗口，而不是设备屏幕。
- 对 Oryn 的 CSS 推荐（Web 层）：
  - 用 `@container` 查询处理组件级自适应。
  - 用 `@media (min-width:600px)`/`(min-width:840px)` 处理应用级布局切换，即 compact 单栏、medium 双栏、expanded 三栏。
  - 用 `ResizeObserver` 或 `window.matchMedia` 驱动 JS 状态。
- **Android 16 (API 36) 目标版本：** 系统在 smallest width ≥600dp 的设备上忽略方向、宽高比和可调整大小的限制（Android 文档备注，https://developer.android.com/develop/ui/compose/layouts/adaptive/support-different-display-sizes）。
  - 因此 App 必须本来就能适配任意窗口尺寸。

### 8.2 安全区与软键盘

- **Android WebView：** 官方兼容表（https://developer.android.com/develop/ui/views/layout/webapps/understand-window-insets）：

  | WebView 版本 | 行为 |
  |---|---|
  | M136 | 刘海/系统栏通过 `safe-area-inset-*` 暴露，仅全屏 WebView |
  | M139 | 键盘通过 visual viewport 缩放，所有 WebView |
  | M144 | 刘海/系统栏对所有 WebView 生效 |

  - 键盘缩放只作用于**底部**，左/右/上不触发。
  - 键盘导致的 resize 不得清除焦点，否则键盘开关死循环。
  - 原生容器已加 padding 时，用 `Insets.NONE` 归零而不是 `CONSUMED`，避免双重 padding 和残留 padding。
  - Tauri issue #14240（2025-10-02）记录「Android webview 不应用 safe-area insets」（https://github.com/tauri-apps/tauri/issues/14240，经搜索摘要）。
  - 社区有 `tauri-plugin-safe-area-insets-css`（https://github.com/saurL/tauri-plugin-safe-area-insets-css）可作参考。
- **iOS：**
  - `env(safe-area-inset-*)` 在 WKWebView 可用 [推测]，需要 `viewport-fit=cover`。
  - 键盘：用 `visualViewport` 的 resize/scroll 事件处理。
  - `interactive-widget=resizes-content`：Safari iOS 到 27.2 不支持；WebKit 在 2026-11-06 的评论称 iOS 27.3 beta 开始实现，但有人报告该值在 beta 上有问题（https://bugs.webkit.org/show_bug.cgi?id=259770）。
  - Android Chrome 从 108 起支持，但 Chrome 公告称默认行为变更不适用于 WebView（https://developer.chrome.com/blog/viewport-resize-behavior）。
  - 结论：键盘适配要用 `visualViewport` 的 JS 方案，把 `interactive-widget` 当渐进增强。

### 8.3 折叠屏

来源：MDN browser-compat-data 及 Chrome 博客（搜索摘要，https://developer.chrome.com/blog/viewport-segments-api-shipped）。

| API | Chrome / Android | Android WebView | Safari / iOS |
|---|---|---|---|
| Viewport Segments（`window.viewport.segments`、`env(viewport-segment-*)`、`horizontal-viewport-segments` 媒体特性） | 138+ | 138+（镜像 Chrome） | 不支持 |
| Device Posture（`navigator.devicePosture`） | 132+ | **不支持** | iOS 27 在偏好开关后，非默认 |

- 旧示例的 `visualViewport.segments` 已改为 `window.viewport.segments`（2025-06-09 公告）。
- 做法：当渐进增强，正常响应式布局为主；检测到两个分段时才切换为双窗格布局。
- 折叠与展开会触发窗口尺寸变化，Tauri 的 Android 模板已在 `configChanges` 里包含 `orientation|keyboardHidden|keyboard|screenSize|locale|smallestScreenSize|screenLayout|uiMode`，Activity 不会因此重建，WebView 收到 `resize` 即可（模板文件 https://raw.githubusercontent.com/tauri-apps/tauri/dev/crates/tauri-cli/templates/mobile/android/app/src/main/AndroidManifest.xml）。

### 8.4 指针、悬停与键盘

- 媒体查询 `(pointer: coarse|fine)`、`(hover: hover|none)`、`(any-pointer: …)` 是通用 Web 标准。
- 本次**未专门核实**各 WebView 的支持差异，请按 Oryn 目标设备实测。
- 外接键盘快捷键：用 `keydown` 加 `Ctrl/Cmd` 判断。桌面另有全局快捷键插件。
- iPadOS 的 Stage Manager、分屏：WKWebView 会收到窗口尺寸变化；Tauri 对 iPad 多窗口场景支持「未找到」。
- Android 多窗口：模板主 Activity 没有显式声明 `resizeableActivity`，默认值随 targetSdk 而定，需要在生成的 manifest 中显式设置。

### 8.5 Tauri 移动端需要的设置

- **Android：**
  - `configChanges` 已由模板提供；`windowSoftInputMode="adjustResize"` 可显式加。
  - `enableEdgeToEdge()` 需要在 `MainActivity.kt` 自行加（搜索摘要里的 Option A/B，来自 Android 与 Chromium 文档的组合，不是 Tauri 官方 API）。
  - 在 `onCreate` 里 `enableEdgeToEdge()` 加 `ViewCompat.setOnApplyWindowInsetsListener`，处理后用 `WindowInsetsCompat.Builder` 把已处理类型置 `Insets.NONE`。
  - 显式设置 `android:resizeableActivity="true"` [推测，属于标准 Android 做法]。
- **iOS：**
  - `Info.ios.plist` 增加需要的权限键与 `UISupportedInterfaceOrientations~ipad`（含四个方向以支持 iPad 多任务）[推测，标准做法]。
- 这些改动都在 `src-tauri/gen/**`，应纳入版本控制，并写脚本保证重新生成后可复现。

---

## 9. 构建与分发

### 9.1 在 Linux 服务器上能构建什么

| 目标 | 能否在 Linux 构建 | 依据 |
|---|---|---|
| Linux (deb/rpm/AppImage) | 能 | 官方流程 |
| Windows NSIS (`-setup.exe`) | 可，用 `cargo-xwin` + LLVM/lld + NSIS，官方称「最后手段」「测试不充分」 | https://v2.tauri.app/distribute/windows-installer/ |
| Windows MSI | **不能**，WiX 只能在 Windows 运行 | 同上 |
| macOS (.app/.dmg) | **不能**原生，需 macOS 宿主 | 前置条件页要求 Xcode 或命令行工具 |
| iOS | **不能**，需 macOS+Xcode；签名还需一台 Apple 设备并加入 Apple Developer Program（99 美元/年） | https://v2.tauri.app/distribute/sign/ios/ |
| Android | 能（Android Studio/SDK/NDK/JDK，Linux 可用） | 前置条件页 Android 节 |

- Linux 构建依赖：`libwebkit2gtk-4.1-dev`、`libayatana-appindicator3-dev`、`librsvg2-dev`、`patchelf`、`xdg-utils` 等（https://v2.tauri.app/start/prerequisites/）。
- 前置条件页的 Android/iOS 节只读到了前 300 行，NDK、JDK 的具体版本「未完整读到」。

### 9.2 CI

- 官方 `tauri-apps/tauri-action@v1` 示例矩阵含：macOS arm64/x64、`ubuntu-22.04`、`ubuntu-22.04-arm`（仅公开仓库可用）、`windows-latest`（https://v2.tauri.app/distribute/pipelines/github/）。
- 该示例**不含**移动端；Android/iOS 的 CI 配置需自写：
  - Android 的 `keystore.properties` 在 CI 里用 base64 的 secret 还原（https://v2.tauri.app/distribute/sign/android/）。
  - iOS 用 `IOS_CERTIFICATE`、`IOS_CERTIFICATE_PASSWORD`、`IOS_MOBILE_PROVISION`，或 App Store Connect API key（`APPLE_API_ISSUER`/`APPLE_API_KEY`/`APPLE_API_KEY_PATH`）自动签名。
- 公开仓库的 macOS runner 免费额度、闭源仓库的计费，「未找到」。

### 9.3 签名要求

- iOS：Apple Developer Program、Apple Distribution 证书、App Store Connect 描述文件。
- Android：上传密钥（keytool 生成 jks），Play App Signing 另算。
- macOS：公证和开发者签名的细节页本次**没读**，「未找到」。
- Windows：代码签名证书页本次也没读，「未找到」。
- 更新器签名：独立的 Tauri 签名密钥，见第 5 节。

### 9.4 包体量级

- Tauri 官方称最小应用 <600 KB（https://v2.tauri.app/start/），这对 Oryn 没有参考意义。
- Windows 安装器额外体积（https://v2.tauri.app/distribute/windows-installer/）：

  | WebView2 方式 | 额外体积 |
  |---|---|
  | `downloadBootstrapper`（默认） | 0 |
  | `embedBootstrapper` | ~1.8 MB |
  | `offlineInstaller` | ~127 MB |
  | `fixedVersion` | ~180 MB |

- CEF（Tauri 3 alpha）会加几百 MB（https://v3.tauri.app/develop/cef/）。
- 其它平台的实际 Oryn 包体需要原型测得，「未找到」可参考的数据。
- 减小体积的 Cargo 配置：`lto`、`opt-level = "s"`、`panic = "abort"`、`strip`，以及 `build.removeUnusedCommands`（https://v2.tauri.app/concept/size/）。

---

## 10. 硬障碍、规避方案与 PWA 覆盖度

### 10.1 需求点与方案对照

| 需求 | 状态 | 规避方案 |
|---|---|---|
| 远程推送 | [社区] 4 个插件，成熟度各异 | 优先评估 Choochmeque（0.5，本地加推送合一）或 spicavi；服务端 Go 自发 APNs/FCM；不满意则自写 Swift/Kotlin 插件（Tauri 插件机制支持） |
| 系统分享入口 | [自研加社区] | iOS 需 Share Extension+App Group；Android 改 manifest；先用 sharehub/mobile-sharetarget，iOS 部分很可能要自己补 |
| Linux WebRTC | **硬障碍**（2.54 禁用，2.56 才可能恢复） | Oryn 若不需要浏览器内 WebRTC，则无影响；听写用 MediaRecorder 加服务端转写；必要时 Tauri 3 CEF（alpha，仅桌面） |
| Linux 麦克风 | 需自处理 `permission-request` | 用 `with_webview` 写 WebKitGTK 权限处理 [自研]；或用 Rust 原生录音再传给前端 |
| Linux/WebKitGTK 的 WebGL/xterm | 有 NVIDIA/Wayland 风险 | xterm 默认 DOM 渲染，WebGL 做可选开关；必要时设 `WEBKIT_DISABLE_DMABUF_RENDERER=1` |
| Passkey | 各 WebView 不一致 | 用系统浏览器/认证会话+PKCE，或口令+TOTP |
| 移动端安全存储 | [社区] | impierce/keystore（需生物识别）；Stronghold 不建议 |
| 折叠屏 Device Posture | WebView 不支持 | 以尺寸响应式为主，Viewport Segments 渐进增强（Android WebView 138+） |
| 移动端自更新 | 不存在 | 商店/TestFlight/APK |
| 后台常驻 | 平台限制 | 推送加游标补拉 |
| Windows/macOS/iOS 构建 | 需各自 runner | GitHub Actions 矩阵 |

### 10.2 PWA 作为移动端过渡形态

- **能覆盖：**
  - 响应式布局、触摸、WebSocket/SSE、IndexedDB、MediaRecorder 听写、HTTPS 同源的 Service Worker、公开只读分享页。
  - Android Chrome 的 Web Push 和「安装」；通知点击回到站内路由。
  - iOS：添加到主屏幕后可用 Web Push（iOS 16.4+），但必须用户主动点击订阅（https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/）。
- **无法覆盖：**
  - iOS 自定义 scheme 深链，以及 Share Extension 级别的分享入口。Web Share Target（manifest）的 iOS 支持「未找到」。
  - Keychain/Keystore 与生物识别的深度集成（WebAuthn 在 PWA 内另当别论）。
  - 后台长连接。
  - 桌面托盘、全局快捷键、自启动。
- **建议：**
  - 先把 React Web UI 作为 PWA 在移动端上线，用 Web Push 解决通知。
  - 用 Tauri 先出桌面三端。
  - 再用同一份 UI 做 Tauri 移动壳，补推送、分享、深链、生物识别。
  - 这样 Web 层就是唯一的 UI 来源，各端只多出原生桥接。

### 10.3 Tauri 2 vs Tauri 3 alpha 的选型

- 现在以 **Tauri 2.12.x** 为准。
- 观察 Tauri 3 的 CEF 路线：它能彻底解决 Linux/Windows/macOS 的 WebView 差异，但移动端没有，且是 alpha，社区插件要等作者适配。
- 现在写插件和 capability 时，尽量使用官方插件的稳定 API，减少迁移成本。

---

## 对 Oryn 的启示

- 以 Tauri 2.12.x 起步，CEF/Tauri 3 先观望，不进生产。
- 移动端线程完成通知一律走服务端推送（APNs/FCM），前台再用 WebSocket。
- 推送插件先做 spike：Choochmeque 与 spicavi 各跑一遍真机，并预留自写插件的预算。
- Share Extension（iOS）是真正的自研工作量，Android 只要改 manifest 加社区插件。
- 认证走系统浏览器 OAuth/PKCE，不要依赖 WebView 内的 Passkey，Linux 尤其不行。
- 令牌存储用 Keychain/Keystore 社区插件，不用 Stronghold；桌面用系统凭据库。
- Linux 端把 WebRTC、WebGL（xterm）、麦克风都当降级路径设计，xterm 默认 DOM 渲染。
- 响应式用 window-size-class 断点（600/840dp）、容器查询、`visualViewport`；折叠屏 Viewport Segments 只做增强。
- Android 要自己写 `MainActivity.kt` 的 edge-to-edge 与 insets 处理，并要求 WebView ≥M139/M144 的行为差异覆盖测试。
- `src-tauri/gen/**` 的原生改动要入库并脚本化，避免重新生成时丢失。
- CI：桌面用 `tauri-action` 矩阵；Windows 优先用 `windows-latest`，不依赖 cargo-xwin 交叉编译。
- iOS/macOS 构建只能在 macOS runner，签名需 Apple Developer 账号（99 美元/年）。
- 公开只读分享链接的域名同时承担 AASA/assetlinks，让已装 App 的设备直接打开。
- 先把 React UI 作为 PWA 上线，Web Push 补移动端过渡，再叠加 Tauri 原生壳。
- 更新器私钥要备份，丢了老用户无法升级。

---

## 实际读过的文件/页面清单

**亲自读到的页面（全文或主要部分）：**
- https://github.com/tauri-apps/tauri/releases.atom
- https://github.com/tauri-apps/plugins-workspace/releases.atom
- https://v2.tauri.app/plugin/notification/（前 300/438 行）
- https://v2.tauri.app/plugin/deep-linking/（前 300/513 行）
- https://v2.tauri.app/plugin/updater/（前 300/663 行）
- https://v2.tauri.app/plugin/（列表和支持表，前 300 行加 301–338 行；中段支持表部分行（如 autostart 之后到 dialog 之前）未完整读到）
- https://v2.tauri.app/plugin/biometric/
- https://v2.tauri.app/plugin/stronghold/（前 300/323 行）
- https://v2.tauri.app/plugin/window-state/
- https://v2.tauri.app/learn/system-tray/
- https://v2.tauri.app/reference/webview-versions/
- https://v2.tauri.app/develop/debug/linux-graphics/
- https://v2.tauri.app/develop/plugins/develop-mobile/（前 300/609 行）
- https://v2.tauri.app/start/、https://v2.tauri.app/start/prerequisites/（前 300/405 行）
- https://v2.tauri.app/concept/size/
- https://v2.tauri.app/distribute/pipelines/github/
- https://v2.tauri.app/distribute/windows-installer/（前 300/832 行）
- https://v2.tauri.app/distribute/sign/ios/、https://v2.tauri.app/distribute/sign/android/
- https://v3.tauri.app/develop/cef/（前 300/338 行）
- https://v3.tauri.app/start/migrate/from-tauri-2/（前 300/421 行）
- https://www.webkitgtk.org/2026/09/16/webkitgtk-2.54-highlights.html
- https://developer.android.com/develop/ui/views/layout/webapps/understand-window-insets
- https://developer.android.com/develop/ui/compose/layouts/adaptive/support-different-display-sizes（前 300/310 行）
- https://raw.githubusercontent.com/tauri-apps/tauri/dev/crates/tauri-cli/templates/mobile/android/app/src/main/AndroidManifest.xml
- https://github.com/Choochmeque/tauri-plugin-notifications（README 前 300/880 行）

**仅通过 `web_search` 摘要获得、未亲自打开原文的结论：**
- 推送插件 spicavi、srod、yanqianglu 的细节。
- 分享插件 sharehub、mobile-sharetarget 的细节。
- Passkey 在各平台的状态。
- xterm.js 的渲染器历史。
- Viewport Segments / Device Posture 的支持表。
- `interactive-widget` 的支持。
- iOS Web Push 的限制。
- 麦克风在 wry 里的权限流程。
- Tauri issue #14240。
- Stronghold 弃用讨论 #7846。
- Tauri 2.0 稳定版发布日期。
- iOS/Android 后台执行限制。

这些结论请在实现前对照原始链接复核。

**明确未找到或未核实的点：**
- 本机 `cargo`/`bun` 版本。
- 各 WebView 的 WebCodecs 与 Service Worker 支持。
- 桌面 HTTPS 通用链接。
- macOS 公证与 Windows 代码签名页。
- 移动端具体 NDK/JDK 版本。
- 剪贴板与拖放的逐平台行为。
- `pointer`/`hover` 媒体查询的各 WebView 差异。
- iPadOS 多窗口在 Tauri 下的行为。

### Critical Files for Implementation

- `src-tauri/tauri.conf.json` — identifier、deep-link、updater、`bundle.iOS` 与 `plugins` 配置的中心
- `src-tauri/capabilities/*.json` — 各插件权限（notification、biometric、deep-link 等）
- `src-tauri/gen/android/app/src/main/AndroidManifest.xml` + `MainActivity.kt` — intent-filter、`resizeableActivity`、edge-to-edge 与 insets 处理
- `src-tauri/Info.ios.plist` 与 iOS Xcode 工程中的 Share Extension target — 麦克风/Face ID 权限、App Group、aps-environment
- `.github/workflows/*.yml` — `tauri-action` 桌面矩阵，加自写的 Android/iOS 流水线
