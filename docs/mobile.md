# 移动 App（Android；iOS 预留）

Oryn 的 Android 客户端与桌面 App 是同一个 Tauri 2 工程（`web/src-tauri`）。App 本身只带一个很小的「连接页」，配对完成后加载**所连服务器自己的 Web UI**，所以界面总是和服务器同一版本。窄屏下线程页是单栏，对话与各面板（`Agent | Changes | Files | Terminal …`）是同级视图，手机宽度（<600dp）由页头右上角的视图菜单切换、不常驻标签条，600–839dp 双栏，≥840dp 三栏；旋转、改窗口大小、折叠 / 展开都不会重建页面，WebView 138 及以上在折叠屏半开时左右分屏。手机线程页头是悬浮的「侧栏钮 / 标题胶囊 / 视图钮」三件，线程操作都在点标题打开的菜单里；往上翻历史时输入坞收成一条信息胶囊，往下翻回来或点它即恢复。

- 应用标识 `com.lynricsy.oryn`，最低 Android 13（API 33），目标 API 37；深链接协议 `oryn://`（不启用 App Links）。
- 设备令牌只存在于 App 的 Rust 进程和 Android Keystore 加密的私有存储里，网页代码拿不到它；App 用令牌换一个「属于该设备」的网页会话再打开 Web UI（与桌面相同，见 [protocol.md](protocol.md)「M6：App 会话」）。
- 服务器必须走 HTTPS（见 [deploy.md](deploy.md)）；只有 `localhost` / `127.0.0.1` 允许明文（开发与本机验收经 `adb reverse`）。App 的 Rust 网络栈只信任 Mozilla 根证书，**私有 CA 签发的服务器证书不受支持**。

## 两种安装包

| 包 | 推送通道 | 适合 |
|---|---|---|
| `foss`：`Oryn_<版本>_android-foss.apk` | UnifiedPush（需要在手机上装一个分发器，推荐 ntfy） | 所有设备，App 里没有任何 Google 依赖 |
| `gms`：`Oryn_<版本>_android-gms.apk` | 默认经官方推送网关走 FCM，配对后自动启用；可在设置里改用 UnifiedPush | 装有 Google Play 服务、不想另装分发器的手机 |

两个包的应用标识相同、用同一把密钥签名，可以互相覆盖安装（覆盖后需在 App 里重新「启用推送」）。

## 安装与校验

从 GitHub Releases 的 `v<版本>` 下载 APK（服务端、桌面与移动 App 同一个 Release；`.aab` 只作留档，供将来上架）。可以用 [Obtainium](https://github.com/ImranR98/Obtainium) 订阅本仓库、只匹配 `android-foss.apk` 或 `android-gms.apk` 资产，自动提示更新；App 内没有自更新。`0.1.0` 及更早的预发布在 `mobile-v<版本>`。

安装前建议校验签名证书（Android SDK 的 build-tools 里有 `apksigner`）：

```bash
apksigner verify --print-certs Oryn_<版本>_android-foss.apk | grep 'SHA-256'
```

输出必须与下面记录的发布证书指纹一致。指纹不同说明不是本仓库 CI 用发布密钥签出的包，不要安装。

发布证书 SHA-256：`BA:70:32:16:4A:27:BE:B9:AB:D6:AE:53:D2:19:E7:36:F9:37:A7:DF:46:96:AE:23:67:28:A4:95:1A:B8:0B:31`

## 首次运行与配对

1. 在连接页填服务器地址（如 `oryn.example.com`，不写协议按 `https://`），或点「扫码」扫网页「设置 → 设备」里的二维码（内容是 `oryn://pair?server=<服务器>`；用系统相机扫也会打开 App）。扫码与链接**只预填**：连接页显示「连接到这台服务器？」和大字主机名，点「连接」才发起配对（链接谁都能发，恶意服务器可以直接批准配对，所以必须由你看清地址后确认）。批准仍要输配对码。
2. 连接页显示配对码（如 `ABCD-EFGH`，10 分钟内有效）。App **不会**自动打开浏览器，免得盖住配对码。两种批准方式任选：
   - 手机浏览器里有 Passkey：点「复制配对码并打开浏览器」，登录后粘贴配对码并批准，再点「返回 Oryn App」；
   - 手机上没有 Passkey：到已登录的电脑「设置 → 设备」输入同一个配对码批准。
3. App 收到设备令牌后存入 Keystore，随后进入 Web UI。

- **吊销**：在任何一端的「设置 → 设备」吊销这台手机后，App 关闭推送、删除本地令牌并回到连接页。
- **更换服务器**：「设置 → 关于 → 更换服务器…」。
- App 里不能批准其它设备的配对（与桌面相同）。

## 推送

服务器只在**没有任何设备正在看该线程**时生成通知；App 在前台时只显示应用内提示，不弹系统通知。通知点击经 `oryn://thread/<id>` 直达线程。

### 官方推送网关 FCM（gms 包默认）

gms 包配对后自动启用推送：弹出系统通知授权框，允许后经官方推送网关（`https://push.oryn.best`）走 Firebase Cloud Messaging，状态显示「经官方推送网关与 Firebase 云消息（FCM）接收推送」。不需要装分发器，服务器也不需要任何 Google 凭据：orynd 把 RFC 8291 密文交给网关，网关转给 FCM；Firebase 服务账号只在网关上。协议见 [protocol.md](protocol.md)「官方推送网关」，自建网关见 [push-gateway.md](push-gateway.md)。

- **授权**：App 先向网关证明持有这台手机的 FCM 令牌（网关发一条 challenge 推送），再用设备密钥与 orynd 的主机公钥换一份授权，最后把授权登记到 orynd（`kind: gateway`）。陌生 orynd 只能给明确授权过它的手机推送；关闭推送或改用 UnifiedPush 时 App 直接在网关上吊销授权，不依赖 orynd。
- **端到端加密**：启用时 App 生成自己的 RFC 8291 接收端密钥（P-256 + 16 字节 auth；私钥用 Keystore 里的 AES-GCM 密钥加密保存），随授权一起登记；FCM 的 `data` 只有 `{v:"2", id, e:<密文>}`，网关与 Google 都只见密文。App 用 UnifiedPush 连接器同一份解密代码解开后走同一套通知流程；`v` 不对、缺 `e` 或解不开的消息直接丢弃。
- **自部署的 orynd** 默认就用官方网关（`orynd.yaml` 的 `push.gateway.url`，见 [deploy.md](deploy.md)「移动 App 推送」）；管理员写空串关闭后，gms 包的推送卡片提示服务器关闭了官方推送网关，可改用 UnifiedPush。
- **从旧版升级**：gms 包里原先用 UnifiedPush 的会自动改走网关 FCM（旧订阅先删再登记，期间不会两条通道都收）；想继续用 UnifiedPush，在卡片上点「改用 UnifiedPush」。
- **后台刷新令牌**：FCM 刷新令牌或换授权时，进程里可能只有推送服务、没有界面；App 在下次进入或回到前台时重新登记。UnifiedPush 换端点时 App 排一个 WorkManager 任务（要联网、指数退避，最多 8 次），用上次登记到的服务器和 Keystore 里的设备令牌直接重新登记，不用等你打开 App。
- **离线积压**：FCM 对离线设备最多存 100 条消息，超出时全部丢弃并在上线后通知 App；App 随即补拉最近 50 条通知，把未读（待答的还要未作答）且没显示过的逐条弹出，同一线程合并成一条；补拉失败时只弹一条不含内容的「Oryn 有新通知」。

### UnifiedPush（foss 包；gms 包可选）

1. 在手机上安装 ntfy（F-Droid / Google Play / GitHub 均可），打开一次并允许其后台运行与通知。
2. 可选：自建 ntfy 服务器（手机与 orynd 都要能访问），在 ntfy App 的「设置 → 默认服务器」填它；不自建就用 `ntfy.sh`，分发服务器只看得到密文（消息是 RFC 8291 端到端加密的 Web Push）。私有 ntfy 开了鉴权时要放行匿名发布到 UnifiedPush 主题：`ntfy access '*' 'up*' write-only`。
3. Oryn App 里「设置 → 通知 → 启用推送」（gms 包点「改用 UnifiedPush」），按系统提示允许通知。状态显示「经 ntfy 接收推送（<服务器>）」即可。

服务端不需要任何配置：UnifiedPush 端点按 `kind: webpush` 登记，复用 Web Push 发送器。

### 在通知上作答

Agent 的提问只需点一个选项时（工具审批的「批准 / 拒绝」、是 / 否确认、2–3 个短选项的单选），通知下方直接带按钮，正文是完整的问题（工具审批含要执行的命令）；问题太长会被截断时不带按钮，点通知打开线程作答。规则见 [protocol.md](protocol.md)「推送上的作答按钮与撤回」。

- 点按钮不打开 App：后台用设备令牌调 `POST /api/v1/notifications/{id}/answer`，期间通知显示「正在提交…」并收起按钮（防连点）。成功显示「已作答」，4 秒后消失；已在别处作答显示「已在别处作答」；网络错误、超时或服务器 5xx 时保留原文与按钮并加一行「作答失败，点按打开 Oryn」，可以再点；令牌失效等其它错误加同样一行但收起按钮。各种状态下点通知本身都打开该线程。
- 请求在网页、另一台设备或 Telegram 上作答（或被取消）后，服务器发一条撤回推送，App 撤掉这条仍带按钮的通知；同一线程后来的通知不受影响。撤回没送到时，点过期的按钮得到「已在别处作答」。
- **锁屏上必须先解锁**：按钮设了 `setAuthenticationRequired(true)`（Android 12 起由系统执行，本 App 最低 Android 13）。锁屏只显示不含内容的公开版，看不到要批准的是什么；而批准工具命令等于在你的设备上执行操作，不能让拿到手机的人不解锁就点掉。

### 通知样式与头像

Android 通知采用 `NotificationCompat.MessagingStyle`，iOS 15+ 采用对等的 Communication Notifications（`INSendMessageIntent` + `UNNotificationContent.updating(from:)`）。iOS 端需两项配置齐备：`web/src-tauri/Info.ios.plist` 声明 `NSUserActivityTypes: ["INSendMessageIntent"]`，以及 `web/src-tauri/ci/ios-entitlements.sh` 在 `tauri ios init` 后把 `com.apple.developer.usernotifications.communication = true` 合并进 target entitlements（`ios-release` 会用 `codesign -d --entitlements - --xml` 断言该权限已签入 IPA）。在真机 / TestFlight 发版前，仍须在 Apple Developer Portal 为 `com.lynricsy.oryn` 启用 Communication Notifications 并更新 `IOS_MOBILE_PROVISION`：

- **普通线程**：发送者头像取线程标题卡图标（Emoji 或内置 Symbols Nerd Font 字形）与线程色，无标题卡图标时回落为 Oryn 品牌标；标题开头与头像重复的图标自动省略。
- **全局助手动态（主线）与助手对话线程**：发送者头像统一取全局助手头像（自定义图片 / 字形，未设置时为默认 sparkle 标记；不跟随默认 Agent 身份）。Android 后台推送只携带 8 位头像哈希，本地缓存命中即零请求绘制，变更时按需拉取 `GET /api/v1/settings/assistant-identity` 更新缓存。
- **无线程系统通知**：`/settings/assistant` 归属全局助手头像，其余（预算告警、定时）使用 Oryn 品牌标。

### 锁屏

通知设为「私密」：锁屏上只显示公开版「Oryn · 线程有更新」，解锁后才看到线程标题与回复摘要（系统设置里选了「锁屏时隐藏敏感内容」时连公开版也按系统规则处理）。

### 国产 ROM 与省电

MIUI / HyperOS、ColorOS、OriginOS、EMUI / HarmonyOS 等会在后台杀掉分发器。请对 ntfy（FCM 时对 Oryn 本身影响较小）开启「自启动」、把电池策略设为「无限制」、在多任务界面锁定。Oryn App 本身不常驻后台、不持有唤醒锁：转到后台 30 秒后断开实时连接，回到前台立即重连并补齐。清单里的 `WAKE_LOCK` 权限来自 UnifiedPush connector：它在处理每条推送的那一刻短暂持有、处理完即释放，去掉这条权限会让收推送时抛 `SecurityException`。

## 其它能力

- **深链接**：`oryn://thread/<线程 id>` 打开该线程，`oryn://open` 打开首页，`oryn://pair?server=…` 预填服务器地址并等你确认（见「首次运行与配对」）。
- **主框架导航**：App 主窗口只会停在连接页、所连服务器的页面或 `about:blank`。`blob:` 只认服务器自己 origin 下的，`data:` 一律拦截；其它 http(s) 链接交给系统浏览器。portal 页面因此不能用 `window.open(blob:…)` 把整个 App 换成无地址栏的伪造界面。
- **系统分享**：在任意 App 里「分享 → Oryn」，文本进入新建线程框，图片与文件（单个 ≤100MiB，最多 20 个）先上传为附件再预填。
- **附件**：输入区的回形针选文件，相机图标直接拍照（拉起系统相机）。
- **下载**：附件与文件面板的下载交给系统下载管理器，保存到「下载」目录。同名文件已存在时另存为「名字 (1).扩展名」，从不覆盖或删除已有文件。
- **分享接收的边界**：只收其它应用 provider 给的 `content://`，按 provider 的归属 uid 判断（`content://0@<本包>…` 这类带用户前缀的地址也认得出是本应用自己的）；本应用的 FileProvider 只开放拍照目录 `Android/data/<包>/files/Pictures/`。
- **配置变更**：旋转、改窗口大小、折叠、深色模式、显示大小都在 `configChanges` 里，不重建页面。改字体大小等其它变更会让系统重建 Activity：插件改绑到新的 WebView（下载、inset、系统栏照常），连接页发现会话还在就重新换取并回到原来的页面。
- **Activity 被销毁、进程保留**（开发者选项「不保留活动」，或系统为省内存只回收 Activity）：Tauri 会删掉唯一的窗口并请求退出，壳把这次退出拦下（`ExitRequested{code: None}` → `prevent_exit`），进程留在后台、会话状态不丢。若放行，tao 走 `std::process::exit`，在部分设备 / 模拟器上会死在 EGL 的静态析构里（`FORTIFY: pthread_mutex_lock called on a destroyed mutex`）。切回来时 Android 在同一进程里新建 Activity，插件报 `activity` 事件，壳为它重建主窗口，之后与改字体大小一样：插件改绑、连接页按设备令牌换取会话并回到销毁前的页面，不重新配对。没有 Activity 的这段时间里壳不调用原生插件（推送令牌变化改由后台任务登记）。
- **退出**：返回键在根页面结束 Activity，进程同样留在后台（同上一条：没有窗口、不发请求），下次打开在同一进程里重建并回到原来的页面；从「最近任务」划掉或系统回收进程时整个进程被杀，下次打开是冷启动，按已存的令牌直接进入应用。
- **外链**：其它站点的链接在系统浏览器里打开。
- **终端**：触屏按键条有 Esc / Tab / 粘滞 Ctrl / 方向键；「输入行」开关打开一个普通输入框（输入法、联想与系统粘贴都可用），回车发送整行。

### 已知限制

- 离线不可用：界面来自服务器。
- 不启用 App Links：浏览器里的 `https://<服务器>/t/<id>` 不会直接进 App，用 `oryn://` 或通知点击。
- 系统 WebView 低于 111 时连接页提示「系统 WebView 版本过旧」，请在应用商店更新 Android System WebView。
- 私有 CA 的服务器证书不受支持（见开头）。
- iOS 只经 TestFlight 内部测试分发（附录 A），尚未完成真机验收。

## 开发

一次性环境见 M7 计划步骤 0，之后每个终端先 `source .dev/m7-env.sh`（`ANDROID_HOME`、`NDK_HOME`、`JAVA_HOME`、NDK 交叉编译变量）。

```bash
cd web
bun run tauri android dev                                   # 连模拟器 / 真机热重载（debug 包允许明文连局域网 dev server）
bun run tauri android build --debug --apk --target x86_64   # foss debug 包（模拟器）
ORG_GRADLE_PROJECT_orynDistribution=gms bun run tauri android build --debug --apk --target x86_64   # gms 包
make android DIST=foss                                      # 仓库根目录：foss 通用 APK（release，未配签名时不签名）
```

- flavor 由 gradle 属性 `orynDistribution=foss|gms` 选择（缺省 foss），一次只构建一个；两个都要时分两次构建，中间把产物挪走（输出路径相同，`app/build/outputs/apk/universal/<debug|release>/`）。
- gms 需要 `web/src-tauri/gen/android/app/google-services.json`（已 gitignore，不入库）：本机从 `.dev/m7/google-services.json` 复制，CI 从 secret `ANDROID_GOOGLE_SERVICES_JSON` 写入。缺它时 gms 构建在配置阶段报错。
- 插件 JVM 单测：`cd web/src-tauri/gen/android && ./gradlew :tauri-plugin-oryn-mobile:testDebugUnitTest`。
- debug 包可用 `chrome://inspect` 调试 WebView，并把事件写到 App 缓存目录的 `events.jsonl`：`adb shell run-as com.lynricsy.oryn cat cache/events.jsonl`。release 包没有事件日志、不能调试。
- App 图标由 `web/scripts/gen-icons.py` 生成（各端总览见 [desktop.md 的「App 图标」](desktop.md#app-图标)），默认只用黑白两色：
  - Android：`res/mipmap-anydpi/ic_launcher*.xml` 自适应图标，前景是矢量字形，背景与字形颜色在 `values/ic_launcher.xml`，`values-night/` 给深色（启动器按深色配置解析资源时生效，取决于启动器）；单色层供 Android 13+ 的主题图标按壁纸着色。
  - iOS：`icons/ios/AppIcon.appiconset` 带 Any / Dark / Tinted 三种外观（iOS 18+）。`gen/apple` 不入库，CI 在 `tauri ios init` 之后用 `ci/ios-appicon.sh` 换掉模板里的 tauri 默认图标；本机生成工程后也要跑一次。`ios-check` 与 `ios-release` 会断言包里的 `Assets.car` 有带深色外观的 AppIcon。
  - iOS 可替换图标：目前有一个「品牌色」（`AppIconAccent`，光标竖条固定用默认强调色的色相，不随主题强调色变），在「设置 → 外观 → App 图标」里切换（壳 bridge ≥ 6）。iOS 会先弹出「已更改图标」提示，用户点掉后 `setAlternateIconName` 才回调，页面在此之前保持「切换中」。设置页缩略图的配色由 `gen-icons.py` 一并写进 `web/src/pages/settings/app-icon-palette.gen.ts`，与主屏图标同源，只按页面浅 / 深切换。新增一个图标时要改三处：`gen-icons.py` 的 `ALTERNATES`（资源名、页面 id、颜色）、壳的 `mobile_logic::APP_ICONS`（页面 id ↔ 资源名）、两份语言文件的 `appearance.appIcons`。`ci/ios-appicon.sh` 会把全部 `.appiconset` 装进工程，并打开 `ASSETCATALOG_COMPILER_INCLUDE_ALL_APPICON_ASSETS`；CI 断言每个可替换图标都编进了 `Assets.car`、登记在 `CFBundleAlternateIcons` 里，端到端测试（`ci/ios-e2e/Tests/AppIconE2ETests.swift`）在模拟器的设置页上真点一遍「品牌色 → 黑白」并点掉系统提示。Android 与桌面端不提供切换。

### 本机验收

`cd web && bun run accept:m7 [-- --only A2,A6] [--avd oryn-m7-fold]`，场景与前置写在 `web/scripts/m7-accept.ts` 的文件头。要点：

- 依赖现有 dev 部署（`make dev`、provider、至少一个可用模型），App 经 `adb reverse tcp:7600` 连 `http://localhost:7600`。
- 设备只按 AVD 名选择（默认 `oryn-m7`）：脚本用 `adb -s emulator-N emu avd name` 找到对应的模拟器，没有就以空闲端口启动它；本机其它模拟器（如无关的 `emulator-5554`）不会被安装、改动或关闭。
- UnifiedPush 场景用本机 ntfy 容器（`binwiederhier/ntfy:v2.37.2`，`127.0.0.1:7680`）与 `.dev/m7/` 里的 ntfy APK。
- A11（Viewport Segments）只能在折叠 AVD 上验证：默认 AVD 上它报 SKIP，另跑 `bun run accept:m7 -- --only A11 --avd oryn-m7-fold`。
- 官方推送网关场景（A23）在隔离栈上跑，不动 dev 库（0058 迁移会删掉旧的 FCM 订阅）：先停 `make dev`，`eval "$(bash web/scripts/m28-gateway-stack.sh up)"` 起本机网关（`127.0.0.1:7700`，`.dev/m7/fcm-service-account.json`）与数据目录为 `.dev/m28/data` 的 orynd + process provider，再以 `ORYN_PUSH_GATEWAYS=http://localhost:7700 ORG_GRADLE_PROJECT_orynDistribution=gms bun run tauri android build --debug --apk --target x86_64` 构建 gms 包（`M7_GMS_APK` 指向它）。模拟器镜像要带 Google Play 服务且能出网。结束后 `bash web/scripts/m28-gateway-stack.sh down`。
- release 包场景（A19）需要 `.dev/android-release.jks` 与 `M7_KEYSTORE_PASSWORD`，并核对上文记录的证书指纹。

## 发版

移动端与服务端、桌面端一起发版，流程与 tag 规则见 [desktop.md](desktop.md)「发版」。

1. 改 `web/src-tauri/Cargo.toml` 的 `version`（只写 `x.y.z`；`versionCode` = `major*1000000 + minor*1000 + patch`，预发布与正式版相同）。
2. 打 tag `v<版本>` 或预发布 `v<版本>-rc.N` 并推送。
3. `.github/workflows/release.yml` 校验 tag 并建草稿 Release，然后以 `workflow_call` 调用 `.github/workflows/mobile.yml`：`android-release`（foss / gms 各一套：分架构 debug APK、插件单测、签名的分架构 release APK（`arm64-v8a` / `armeabi-v7a` / `x86_64`）与 Google Play 多 ABI AAB、`apksigner` / `zipalign` / `aapt2` 校验单 ABI 与包元数据并写进 Step Summary、上传 Release 资产）→ `android-smoke`（API 34 模拟器里装 foss `x86_64` debug 包，90 秒内出现 `launcher.ready`）→ `ios-check`（macOS 上不签名编译模拟器包）→ `ios-release`（Apple Distribution 签名导出 IPA，build number 为 `(run_number + 1000) × 100 + run_attempt`，重跑同一次运行也不会与已上传的构建撞号，+1000 让它高过旧 `mobile.yml` 时期上传过的号；校验包名与签名者后用 `xcrun altool` 上传 TestFlight；IPA 不进 Release）。服务端、桌面端与移动端全部成功后，`release.yml` 的 `publish` 才发布 Release。
4. Release 里移动端的资产只有 release 签名的分架构 APK（`Oryn_<版本>_android-{foss,gms}-{arm64-v8a,armeabi-v7a,x86_64}.apk`）与 Play 商店用的 `.aab`（`Oryn_<版本>_android-{foss,gms}.aab`）。debug APK（可调试、允许任意明文请求）只作为 workflow artifact，从 Actions 运行页下载，不进 Release。
PR 与手动触发只产出 debug APK（workflow artifacts）并跑冒烟与 iOS 编译，拿不到签名密钥；来自 fork 的 PR 没有 `ANDROID_GOOGLE_SERVICES_JSON`，跳过 gms。手动触发可勾选 `export_ios_project`，把 `tauri ios init` 生成的 `gen/apple` 作为产物导出以便入库。

### 工作流的安全约束

- 第三方 action 一律钉到完整提交 SHA；checkout 不保留凭据；workflow 级权限为空，只有建 / 发 Release 与上传资产的 job 有 `contents: write`（`release.yml` 调用 `mobile.yml` 时把令牌权限上限设为 `contents: write`）。
- secret 只出现在用到它的那一步的 `env` 里：`ANDROID_GOOGLE_SERVICES_JSON` 只给「写入 google-services.json」，三个签名 secret 只给 tag 发布时的「构建签名的 release APK + AAB」；密钥解码到 `$RUNNER_TEMP` 下的临时目录，`keystore.properties` 与 `.jks` 在该步骤结束时删除。iOS 的三个签名 secret 只给「签名构建 IPA」（tauri 建临时钥匙串），推送扩展的描述文件 `IOS_NSE_MOBILE_PROVISION` 只给「推送扩展的版本号与签名」（`ci/ios-nse.sh`），App Store Connect 的 `.p8` 只在「上传 TestFlight」期间写进 `~/.appstoreconnect/private_keys`，步骤结束即删除。

### Secrets

Android 与 iOS 的签名 secret 放在受保护的 environment `mobile-release` 里（部署规则只允许 tag `v*`），不是仓库级 secret；`ANDROID_GOOGLE_SERVICES_JSON` 仍是仓库级（PR 的 gms debug 构建要用，它不是签名材料）；上传 TestFlight 用的 App Store Connect API 密钥与桌面公证共用仓库级 `APPLE_API_ISSUER` / `APPLE_API_KEY` / `APPLE_API_KEY_P8`（见 [desktop.md](desktop.md)「Secrets」）。

| 名称 | 内容 |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | 发布密钥库（PKCS12）的 base64：`base64 -w0 android-release.jks` |
| `ANDROID_KEYSTORE_PASSWORD` | 密钥库与密钥的口令（两者相同） |
| `ANDROID_KEY_ALIAS` | 密钥别名（`oryn`） |
| `ANDROID_GOOGLE_SERVICES_JSON` | Firebase 控制台下载的 `google-services.json` 全文（须含 `com.lynricsy.oryn` 客户端） |
| `IOS_CERTIFICATE` | 「Apple Distribution」证书从钥匙串导出的 `.p12`，base64 单行：`base64 -i dist.p12` |
| `IOS_CERTIFICATE_PASSWORD` | 导出 `.p12` 时设的口令 |
| `IOS_MOBILE_PROVISION` | `com.lynricsy.oryn` 的「App Store Connect」描述文件（`.mobileprovision`）的 base64 |
| `IOS_NSE_MOBILE_PROVISION` | `com.lynricsy.oryn.NotificationService`（推送通知服务扩展）的「App Store Connect」描述文件的 base64；缺它 `ios-release` 在凭据检查处失败 |

Firebase 服务账号与 APNs key 都不进 CI：它们只在推送网关上（[push-gateway.md](push-gateway.md)）。

### 发布密钥的保管

```bash
PW=$(openssl rand -base64 24)
keytool -genkeypair -v -keystore .dev/android-release.jks -storetype PKCS12 -alias oryn -keyalg RSA -keysize 4096 \
  -validity 10950 -storepass "$PW" -keypass "$PW" -dname "CN=Oryn, O=Lynricsy"
gh api -X PUT repos/<owner>/<repo>/environments/mobile-release \
  -F 'deployment_branch_policy[protected_branches]=false' -F 'deployment_branch_policy[custom_branch_policies]=true'
gh api -X POST repos/<owner>/<repo>/environments/mobile-release/deployment-branch-policies -f name='v*' -f type=tag
gh secret set ANDROID_KEYSTORE_BASE64 --env mobile-release --body "$(base64 -w0 .dev/android-release.jks)"
gh secret set ANDROID_KEYSTORE_PASSWORD --env mobile-release --body "$PW"
gh secret set ANDROID_KEY_ALIAS --env mobile-release --body oryn
keytool -list -v -keystore .dev/android-release.jks -storepass "$PW" | grep 'SHA256:'   # 填进上文「发布证书 SHA-256」
```

- `.jks` 与口令**离线备份两份**（例如密码管理器 + 离线介质），不要放进仓库或云盘明文目录。丢失后已安装的 App 无法覆盖升级，只能卸载重装（会丢掉本机的配对与推送设置）；泄露则任何人都能签出可覆盖安装的包，需换新密钥并让所有用户卸载重装。
- 不要把同一把密钥用于其它应用；轮换后更新上文指纹与三个 secret。

## 自托管构建

想用自己的签名或自己的 Firebase 项目时：

1. **foss 包**（无 Google 依赖）：按「开发」准备环境，生成自己的发布密钥，在 `web/src-tauri/gen/android/keystore.properties` 写
   `keyAlias=…`、`password=…`、`storeFile=<.jks 的绝对路径>`（该文件已 gitignore），然后 `cd web && bun run tauri android build --apk --target aarch64 armv7 x86_64`。
2. **gms 包**：在自己的 Firebase 项目里添加包名 `com.lynricsy.oryn` 的 Android 应用，下载 `google-services.json` 放到 `web/src-tauri/gen/android/app/`，以 `ORG_GRADLE_PROJECT_orynDistribution=gms` 构建。官方网关只服务官方包的 Firebase 项目，所以还要用**同一个 Firebase 项目**的服务账号自建网关（[push-gateway.md](push-gateway.md)「自建网关」），构建时以 `ORYN_PUSH_GATEWAYS=https://<你的网关>` 写入受信网关，并把 orynd 的 `push.gateway.url` 指向它。
3. 在 fork 的仓库里配好上表的 secrets（桌面端的 secrets 都是可选的，缺了按 [desktop.md](desktop.md)「Secrets」降级），推送 `v*` tag 即由 CI 构建发布。
4. 自签的包与官方包证书不同，不能互相覆盖安装；改应用标识需要同时改 `tauri.conf.json` 的 `identifier`、`gen/android` 的 `applicationId` / `namespace` 与 Firebase 里的包名。

## 附录 A：iOS

Team ID（`XDPWT9ABRD`）写在 `tauri.conf.json` 的 `bundle.iOS.developmentTeam`。App ID `com.lynricsy.oryn` 应开启 Push Notifications、Associated Domains 与 Communication Notifications（供下面的 APNs、Universal Links 与通信通知头像用；变更能力后须重新导出 `IOS_MOBILE_PROVISION`）；App Store Connect 里需先建好同一套装 ID 的 App 记录，`ios-release` 才能上传。

首次上传后，App Store Connect 的 TestFlight 页会要求回答出口合规问题（构建显示「缺少合规证明」）。iOS 包里的 Rust 网络栈用 rustls 自带的 TLS 实现，不只是系统加密，所以 `ITSAppUsesNonExemptEncryption` 没有预先写死，由发布者按实际情况作答。

提交 App Store 审核所需的审核备注模板、「App 隐私」问卷答案、出口合规作答依据、隐私政策与支持地址，以及提交前检查见 [app-store.md](app-store.md)。

iOS 专用的 Info.plist 键写在 `web/src-tauri/Info.ios.plist`，`tauri ios build` 会合并进生成的 Info.plist。目前含 `NSCameraUsageDescription`（连接页扫配对二维码、输入区拍照附件；缺了它 App Store Connect 会以 ITMS-90683 退回构建）与 `NSUserActivityTypes`（`INSendMessageIntent`，配合 `ci/ios-entitlements.sh` 合并的 `com.apple.developer.usernotifications.communication = true` 启用通信通知）。`ios-check` 与 `ios-release` 都会断言这些 Info.plist 键在、且 `CFBundleShortVersionString` 等于 Cargo.toml 的版本；`ios-release` 另用 `codesign -d --entitlements - --xml` 断言通信通知权限已签入 IPA。以后用到其它受保护能力（麦克风、定位、相册写入等）时，同样把用途说明加在这个文件里。

iOS 26+ 的手机布局（下沉侧栏）用原生的底栏与页头（线程页三件；新建线程页只有左上角的侧栏钮，spec 的 `title`、`trail` 为 null、key 为 `home`），协议是壳 bridge 8（`web/src/lib/host.ts` 与 `web/src-tauri/src/host.rs` 的 `BRIDGE`；设计与探针结论见 `docs/plan-m20.md`）：

- **卡片开合是 WKWebView 根滚动视图的横向滚动**（`web/src-tauri/plugins/oryn-mobile/ios/Sources/SinkFollow.swift`、`web/src/components/layout/SinkLayout.tsx`、`web/src/lib/native-sink.ts`）。网页经 `host_sink_set {on: true}` 接管后给 `<html>` 加 `data-sink-native`：文档可横滚，侧栏 `position: fixed` 垫底，卡片在右侧。UIKit 处理拖动与减速，`SinkFollow` 只设置松手后的横向落点：超过 0.3 pt/ms 按方向，其余取近端。根不使用 CSS 吸附，避免 WebKit 按上次手指松手的索引把程序化落位拉回旧端。根停在一端静止 100ms 后网页才提交 `sidebarOpen`；未经请求的位移（如 `scrollIntoView`）立即拉回已提交的位置。接管期间 `history.scrollRestoration = "manual"`，关侧栏时弹历史标记不会把卡片恢复回打开位。
- **程序化开合**（按钮、卡片条、Esc、返回键、再点已选分区、减少动效）一律经 `host_sink_show {open, animated}` 交给原生：`animated` 时 `SinkFollow` 用 CADisplayLink 逐帧做不带动画的 `setContentOffset`（260ms 三次缓出，终点按当前尺寸计算），否则一步落位。原生跟手拖动时不执行该命令。1 秒内没到目的地且没有正在进行的触摸，则再发一次一步落位并宽限 1 秒；若仍没到，或超时时手指仍按着，就放弃过期目的地，按实际静止位提交。
- **底栏**是系统 `UITabBar`（`TabBarOverlay.swift`，点选、按住冒出的大液珠与横滑换项都是系统行为），浮在 WKWebView 之上，玻璃折射下面真实的侧栏列表（列表一直画到屏幕底边）。底栏属于侧栏层，装在一个裁切视图里，右缘裁到卡片左缘：卡片左缘来自 `SinkFollow`（`contentOffset`），在同一次 KVO 回调里改裁切宽度，与卡片同进程同帧，不需要对时；卡片完全露出底栏胶囊时底栏才接触摸。菜单、弹层、提示或软键盘盖到底栏位置时网页经 `host_tab_bar_cover` 告诉原生，原生隐藏、网页胶囊顶上，让开后原生 120ms 淡回、网页胶囊同时长后再撤。下沉侧栏宽 `min(87vw, 24rem)`（440pt 的大屏也是 87%，卡片露出的关闭条至少 44px），底栏左右各让 12px，四项撑满（`itemPositioning = .fill`）；iOS 26 的 `UITabBar` 把玻璃胶囊画在自己边界内左右各缩进约 21pt，所以原生底栏左右各放出 21pt，看得见的胶囊与网页胶囊同宽，起手触摸只认网页胶囊的矩形。
- **页头**（侧栏钮 / 标题胶囊 / 视图钮）在 `SinkFollow` 的同一次 KVO 回调里随 `contentOffset` 平移，与卡片同进程同帧，不需要对时；卡片离开关闭位时只显示不接触摸。网页的 `host_header_show` 只剩透明度（`{alpha, ms}`）：只有网页内容（对话框、弹层）盖住静止关闭的页头时原生才让位（`alpha 0`），侧栏被要求打开的过程中不做遮挡判定。**视图菜单**是系统菜单（`HeaderSystemMenu.swift`）：视图钮是 UIKit 玻璃钮（`UIButton.Configuration.glass`），点开的 `UIMenu` 本身就是 Liquid Glass，形变、行距、按压、长按滑选都由系统负责，提醒点画进图标（静态）；开合经 `UIContextMenuInteraction` 的回调上报网页。系统菜单没有公开的程序化打开，`host_header_menu` 对它只能收起。**标题菜单**是自绘面板（`HeaderMenuView.swift`），与胶囊同在一个 `GlassEffectContainer`，出现时从胶囊里形变长出来（`glassEffectID` + `matchedGeometry`）；玻璃上只铺一层薄底色（`HEADER_TUNE.tint` = 0.1），背后不压暗。面板高度贴合内容，上限 `min(480, 屏高 − 128)` pt，超出上限才在面板内滚动；限高用 `CapHeight` 布局（只封顶不撑开），不能用 `.frame(maxHeight:)`，否则父级提议整屏高度时面板会撑到上限、内容居中留白。
- `host_sink_set` 生效前，非空的 `host_tab_bar_set` / `host_header_set` 一律返回 `unsupported`。bridge ≤ 7 的壳（页头靠跨进程对时跟随卡片）网页一律用网页页头与网页底栏，卡片用网页手势。

CI 的 `ios-check` 在 iOS 26+ 模拟器里跑 `ci/ios-header-check.sh`（原生页头演示：插件直接摆出页头与菜单，比像素并探测触摸归属）与 `ci/ios-e2e-check.sh`（端到端：mock Web UI + 远程能力 + XCUITest 真实触摸，底栏的开合、按住拖动、原生点选与横滑，首页原生侧栏钮的接管，页头的接管、菜单、对话框让位与随卡片开合；事件日志断言、无障碍树断言与逐区域像素比较，见脚本文件头）。底栏的裁切是否跟上卡片要和真实网页一起看，所以只在端到端里检查。网页侧的行为在浏览器里由 `bun run accept:m20`（本机 Chromium 模拟 iOS 壳）验收。

按住拖动期间不能出现 `sink.show`（网页发起的程序化开合）。程序化开合到位后的观测窗口内，从别处重新发起原生 `jump` / `start`，或从终点位置离开超过 1pt 的 `move`，均判为回弹；后者覆盖网页直接 `scrollTo` 拉回、未再次调用原生的情况。`sink.trace` 是 debug 包记下的根滚动轨迹：程序化开合的 `start` / `jump` / `noop` / `busy` / `done` / `cancel`，以及外来位移 `move`（带调用栈，不含手指拖动与惯性）。

验收以 `ios-check` 成功和下载后的 `ios-screenshot/e2e/.complete` 为准：该标记只在底栏、页头与图标检查全部跑完且通过后写入。产物还包含 `e2e/summary.md`（与 Actions 步骤摘要相同）与 `e2e/e2e-h-drag.png`；图标检查要求先切到品牌色、再切回默认。

CI 不覆盖运动途中的逐帧对齐：按住拖动只在卡片停住后取样比对页头与卡片的位置，开合只看到位后的截图，拖动、甩动、开合动画过程中页头 / 底栏与卡片是否每一帧都对齐，留给真机验收（`docs/plan-m20.md` 步骤 4.3）。

### 隐私清单

主包与 NSE 各带一份 `PrivacyInfo.xcprivacy`（源文件 `web/src-tauri/ios/PrivacyInfo.xcprivacy` 与 `web/src-tauri/ios/NotificationService/PrivacyInfo.xcprivacy`），由 XcodeGen 模板 `web/src-tauri/ios/project.yml` 显式放进各自 target 的 resources 阶段，落在 `Oryn.app/` 与 `Oryn.app/PlugIns/NotificationService.appex/` 根目录。`ios-check`（模拟器包）与 `ios-release`（IPA）都断言两份清单在包里且通过 `plutil -lint`；缺清单或漏报 required reason API 时 App Store Connect 以 ITMS-91053 退回。

收集与追踪：

- 不追踪（`NSPrivacyTracking` 为 false、无追踪域名），包里没有分析、崩溃上报或广告 SDK。
- 开发者收集的数据只有设备 ID（`NSPrivacyCollectedDataTypeDeviceID`）：APNs 设备令牌经官方推送网关 `https://push.oryn.best` 登记（网关另存设备与主机公钥、App ID 与时间戳，推送内容端到端加密，网关看不到；见 [push-gateway.md](push-gateway.md)）。不关联用户身份、不用于追踪，用途为 App 功能。NSE 只在本机解密，清单里没有收集项。
- 用户发给自己服务器（orynd）的对话、附件等不算开发者收集，所以不写进清单：服务器由用户自托管，开发者收不到这些内容。服务器把内容转给第三方模型提供商，由服务器所有者在 `models.yml` 里配置，由 App 内的同意提示与隐私政策（<https://docs.oryn.best/privacy/>）披露。App Store Connect 的「App 隐私」问卷按同一口径填写。

主包声明的 required reason API（NSE 的源码 `ios/NotificationService` 与 `Sources/Shared` 不调用这些 API，`NSPrivacyAccessedAPITypes` 为空）：

| 类别 | 理由 | 依据 |
| --- | --- | --- |
| UserDefaults | `CA92.1`（只有本 App 读写） | `PushStore.swift` 用 `UserDefaults.standard` 存推送状态；没有 App Group 套件 |
| FileTimestamp | `C617.1`（App 容器内的文件）、`3B52.1`（用户分享进来的文件） | Rust 壳的 `std::fs` / `tokio::fs` 读写容器里的配置、事件日志与推送登记（`config.rs`、`eventlog.rs`、`pushgw.rs`），上传分享文件时取文件大小（`api.rs` 的 `upload_attachment`）。同一份 Rust 代码的 macOS 发布包 `llvm-nm -u` 引用了 `_stat`、`_fstat`、`_lstat` |
| SystemBootTime | `35F9.1`（测 App 内事件的间隔） | `SinkFollow.swift` 用 `CACurrentMediaTime()`（以 `mach_absolute_time` 为时基）算开合动画的逐帧进度；Rust 的 `Instant` 在 Apple 平台用同一时钟（`clock_gettime(CLOCK_UPTIME_RAW)`）。二者都不在 Apple 列出的符号里，macOS 包也没有引用 `_mach_absolute_time`，这一项是保守声明 |

没有声明磁盘空间（`statfs`、`statvfs`、`getattrlist` 系列与 `volumeAvailableCapacity*` 都没有用到）与活动键盘。

以后要更新清单的情况：

- 新增 Swift 代码、tauri 插件或 Rust 依赖后，对照 Apple 的 [required reason API 列表](https://developer.apple.com/documentation/bundleresources/describing-use-of-required-reason-api) 复核：Swift 源码搜 `UserDefaults`、`systemUptime`、`mach_absolute_time`、`attributesOfItem`、`modificationDate`、`creationDate`、`volumeAvailableCapacity`、`systemFreeSize`、`activeInputModes`；Rust 一侧在 macOS 上对构建出的 `Oryn.app/Oryn` 与 `PlugIns/NotificationService.appex/NotificationService` 跑 `nm -u`，找 `_stat`、`_fstat`、`_fstatat`、`_lstat`、`_getattrlist*`、`_fgetattrlist`、`_statfs`、`_statvfs`、`_fstatfs`、`_fstatvfs`、`_mach_absolute_time`。新出现的类别写进对应 target 的清单，理由取 Apple 文档里与实际用途相符的代码。
- 引入自带二进制的第三方 SDK（xcframework 等）时，SDK 应带自己的隐私清单；没有的话把它的用法并进主包清单。
- App 开始向开发者运营的服务发送新的数据（例如新增崩溃上报或分析）时，在 `NSPrivacyCollectedDataTypes` 里补上对应类型，同步更新 App Store Connect 的「App 隐私」问卷与隐私政策。

### APNs 推送与 Notification Service Extension

iOS 只有一条推送通道：官方推送网关（`https://push.oryn.best`）转 APNs，协议见 [protocol.md](protocol.md)「官方推送网关」。配对后 App 自动启用推送（弹系统授权框）；登记编排（网关的 `info → challenges → grants`、orynd 订阅）全在 Rust 壳里，原生只提供 APNs 令牌、RFC 8291 接收端密钥与 challenge 事件：

- **令牌**：tauri 的 iOS 宿主（tao）的 `AppDelegate` 不实现远程通知注册的两个回调。插件交换 `-[UIApplication setDelegate:]`，给代理的类补上 `didRegisterForRemoteNotificationsWithDeviceToken` / `didFailToRegisterForRemoteNotificationsWithError`（`plugins/oryn-mobile/ios/Sources/PushRegistrar.swift`）。令牌以小写十六进制交给 Rust；回到前台时重新注册，令牌或权限有变就发 `push` 事件。
- **密钥**：CryptoKit 的 P-256 私钥与 16 字节 auth 存钥匙串（service `com.lynricsy.oryn.push`，首次解锁后、仅本机），访问组 `<Team ID>.com.lynricsy.oryn.push` 由 App 与扩展共享（`ci/ios-entitlements.sh` 给主包写 `keychain-access-groups`：先默认组 `…com.lynricsy.oryn`，旧设备令牌留在原处，再 push 组；扩展的 entitlements 在 XcodeGen 模板里）。
- **challenge**：网关的 challenge 推送（`kind: gateway_challenge`）App 在前台时不展示、直接交给登记流程；其它远程推送在前台也不展示（前台提示由页面负责）。
- **NSE**：网关发的 APNs 通知带 `mutable-content`，载荷只有密文 `e`。Notification Service Extension（`web/src-tauri/ios/NotificationService`）先把占位换成按系统语言的「有新通知」，再用共享钥匙串里的密钥解开 `e`，以与本地通知同一套构造（`Sources/Shared/PushContent.swift`）填上真实标题、正文、线程归组与发送者头像；任何一步失败或超时只显示占位。App 离线期间被 APNs 丢弃的通知不补发。
- **工程**：NSE 是 tauri 生成工程之外的 target，靠自定义 XcodeGen 模板加进来：`tauri.conf.json` 的 `bundle.iOS.template` 指向 `web/src-tauri/ios/project.yml`（tauri-cli 2.12.6 内置模板原样复制，只加 `NotificationService` target 与主 target 对它的依赖；target 名不能含 `_iOS`，tauri 按这个子串找主 target 同步签名）。升级 `@tauri-apps/cli` 时要把新版内置模板的改动合进来。`ci/ios-nse.sh` 在 `tauri ios init` 之后把扩展的两个版本号设成与主包相同，发版时另装扩展的描述文件、把 `ios/NotificationService/Signing.xcconfig` 写成手动签名并把扩展并进 `ExportOptions.plist`。
- **CI**：`ios-check` 断言包里有 `PlugIns/NotificationService.appex`，并跑 `ci/ios-push-check.sh`（推送探针：debug 包带 `ORYN_PUSH_PROBE=1` 启动，确认注册回调钩子生效、前台 challenge 不展示、App 进程里的 `WebPushDecrypt` 解开 RFC 8291 附录 A），产物在 `ios-screenshot/push/`（`summary.md` 含日志原文，`.complete` 表示全部通过）。模拟器的 `simctl push` 是本地投递、不调起 NSE，所以 NSE 一项只记录（`PUSH_NSE_GATE=soft`）；NSE 的处理链改由 `ci/ios-push-unit.sh` 硬门禁：把 `Sources/Shared/*.swift` 编进独立的 XCTest 工程（`ci/ios-push-unit/`），在模拟器上跑附录 A 向量、`ci/ios-push/notify.apns` 解密 → 解析 → 构造通知、篡改与错 auth 失败、钥匙串读写，产物在 `ios-screenshot/push-unit/`。NSE 被系统调起与共享钥匙串访问组在 TestFlight 真机上验收。`ios-release` 断言主包 `aps-environment` 为 `production`、扩展在 IPA 里且包名、两个版本号与 push 组都对。

Apple 门户（一次性）：

1. Certificates, IDs & Profiles → Identifiers → `com.lynricsy.oryn`：勾选 **Push Notifications**（不需要建证书，网关用 APNs key）。保存后到 Profiles 重新生成 `com.lynricsy.oryn` 的 App Store Connect 描述文件，下载后 `base64 -i Oryn.mobileprovision | gh secret set IOS_MOBILE_PROVISION --env mobile-release`。
2. Identifiers → 加号 → App IDs → App：显式 Bundle ID `com.lynricsy.oryn.NotificationService`，描述随意，不勾任何能力（也不需要 App Group）。
3. Profiles → 加号 → Distribution → App Store Connect → 选 `com.lynricsy.oryn.NotificationService` 与同一张 Apple Distribution 证书，下载后 `base64 -i NotificationService.mobileprovision | gh secret set IOS_NSE_MOBILE_PROVISION --env mobile-release`。
4. APNs key（Keys → 加号 → Apple Push Notifications service，Sandbox & Production）只放在网关上，见 [push-gateway.md](push-gateway.md)；不进 CI。

没做 1–3 时：`ios-check` 照常通过（模拟器包不签名），但 `ios-release` 在「检查签名与上传凭据」失败、整次 Release 不发布；只做了 3 没做 1 时，签名导出会因描述文件缺 `aps-environment` 失败。

尚未完成：APNs 推送与 NSE 的真机验收（TestFlight：锁屏收到带真实标题与头像的通知、前台不重复弹、关闭后不再收到）；Share Extension（App Group 容器 + 同一条上传预填流程）；Universal Links（`/.well-known/apple-app-site-association`）；Keychain 令牌存储的真机验证；iOS 通信通知头像的真机验收；iPhone / iPad 真机验收。
