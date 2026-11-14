# 移动 App（Android；iOS 预留）

Oryn 的 Android 客户端与桌面 App 是同一个 Tauri 2 工程（`web/src-tauri`）。App 本身只带一个很小的「连接页」，配对完成后加载**所连服务器自己的 Web UI**，所以界面总是和服务器同一版本。窄屏下线程页是单栏 + 顶部同级标签（`Agent | Changes | Files | Terminal …`），600–839dp 双栏，≥840dp 三栏；旋转、改窗口大小、折叠 / 展开都不会重建页面，WebView 138 及以上在折叠屏半开时左右分屏。

- 应用标识 `com.lynricsy.oryn`，最低 Android 13（API 33），目标 API 37；深链接协议 `oryn://`（不启用 App Links）。
- 设备令牌只存在于 App 的 Rust 进程和 Android Keystore 加密的私有存储里，网页代码拿不到它；App 用令牌换一个「属于该设备」的网页会话再打开 Web UI（与桌面相同，见 [protocol.md](protocol.md)「M6：App 会话」）。
- 服务器必须走 HTTPS（见 [deploy.md](deploy.md)）；只有 `localhost` / `127.0.0.1` 允许明文（开发与本机验收经 `adb reverse`）。App 的 Rust 网络栈只信任 Mozilla 根证书，**私有 CA 签发的服务器证书不受支持**。

## 两种安装包

| 包 | 推送通道 | 适合 |
|---|---|---|
| `foss`：`Oryn_<版本>_android-foss.apk` | UnifiedPush（需要在手机上装一个分发器，推荐 ntfy） | 所有设备，App 里没有任何 Google 依赖 |
| `gms`：`Oryn_<版本>_android-gms.apk` | 有 UnifiedPush 分发器时用它；没有时回退到 FCM | 装有 Google Play 服务、不想另装分发器的手机 |

两个包的应用标识相同、用同一把密钥签名，可以互相覆盖安装（覆盖后需在 App 里重新「启用推送」）。

## 安装与校验

从 GitHub Releases 的 `mobile-v<版本>` 下载 APK（`.aab` 只作留档，供将来上架）。可以用 [Obtainium](https://github.com/ImranR98/Obtainium) 订阅本仓库、过滤 `mobile-v` 开头的 Release，自动提示更新；App 内没有自更新。

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

### UnifiedPush（两种包都支持，推荐）

1. 在手机上安装 ntfy（F-Droid / Google Play / GitHub 均可），打开一次并允许其后台运行与通知。
2. 可选：自建 ntfy 服务器（手机与 orynd 都要能访问），在 ntfy App 的「设置 → 默认服务器」填它；不自建就用 `ntfy.sh`，分发服务器只看得到密文（消息是 RFC 8291 端到端加密的 Web Push）。私有 ntfy 开了鉴权时要放行匿名发布到 UnifiedPush 主题：`ntfy access '*' 'up*' write-only`。
3. Oryn App 里「设置 → 通知 → 启用推送」，按系统提示允许通知。状态显示「经 ntfy 接收推送（<服务器>）」即可。

服务端不需要任何配置：UnifiedPush 端点按 `kind: webpush` 登记，复用 Web Push 发送器。

### FCM（仅 gms 包）

没有安装 UnifiedPush 分发器时，gms 包在「启用推送」后改用 Firebase Cloud Messaging，状态显示「经 FCM 接收推送」。需要服务器管理员先启用 FCM（[deploy.md](deploy.md)「移动 App 推送」）：

1. 服务账号 JSON 只放在控制面密钥库里，**proxy 模式**：`oryn secrets set fcm/service-account < service-account.json`；
2. `orynd.yaml`：`push: { fcm: { service_account_secret: "fcm/service-account" } }`，重启 orynd。

服务账号必须属于构建该 APK 时 `google-services.json` 所在的同一个 Firebase 项目，否则 FCM 拒绝发送（`SENDER_ID_MISMATCH`）。服务器未启用 FCM 时 App 显示推送错误，UnifiedPush 不受影响。令牌失效（App 卸载、数据被清除）时服务端收到 `UNREGISTERED` 会自动删除该订阅。

FCM 消息同样端到端加密（协议见 [protocol.md](protocol.md)「M7：移动推送」）：启用时 App 生成自己的 RFC 8291 接收端密钥（P-256 + 16 字节 auth；私钥用 Keystore 里的 AES-GCM 密钥加密保存），随令牌一起登记；FCM 的 `data` 只有 `{v:"2", id, e:<密文>}`，Google 只见密文。App 用 UnifiedPush 连接器同一份解密代码解开后走同一套通知流程；`v` 不对、缺 `e` 或解不开的消息直接丢弃。旧版 App 登记的无密钥订阅会被服务端删除，新版每次进入应用都重新登记一次订阅（按令牌 / 端点覆盖），届时带密钥重建。

- **后台刷新令牌**：FCM 刷新令牌、UnifiedPush 换端点时，进程里可能只有推送服务、没有界面。这时 App 排一个 WorkManager 任务（要联网、指数退避，最多 8 次），用上次登记到的服务器和 Keystore 里的设备令牌直接重新登记，不用等你打开 App。
- **离线积压**：FCM 对离线设备最多存 100 条消息，超出时全部丢弃并在上线后通知 App；App 随即补拉最近 50 条通知，把未读（待答的还要未作答）且没显示过的逐条弹出，同一线程合并成一条；补拉失败时只弹一条不含内容的「Oryn 有新通知」。

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
- **配置变更**：旋转、改窗口大小、折叠、深色模式、显示大小都在 `configChanges` 里，不重建页面。改字体大小等其它变更会让系统重建 Activity：插件改绑到新的 WebView（下载、inset、系统栏照常），连接页发现会话还在就重新换取并回到原来的页面。开发者选项「不保留活动」或系统回收进程时，切回来是一次冷启动（Tauri 的唯一窗口销毁后进程退出），按已存的令牌直接进入应用。
- **外链**：其它站点的链接在系统浏览器里打开。
- **终端**：触屏按键条有 Esc / Tab / 粘滞 Ctrl / 方向键；「输入行」开关打开一个普通输入框（输入法、联想与系统粘贴都可用），回车发送整行。

### 已知限制

- 离线不可用：界面来自服务器。
- 不启用 App Links：浏览器里的 `https://<服务器>/t/<id>` 不会直接进 App，用 `oryn://` 或通知点击。
- 系统 WebView 低于 111 时连接页提示「系统 WebView 版本过旧」，请在应用商店更新 Android System WebView。
- 私有 CA 的服务器证书不受支持（见开头）。
- iOS 目前只保证在 CI 上不签名编译（附录 A），没有可安装的包。

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

### 本机验收

`cd web && bun run accept:m7 [-- --only A2,A6] [--avd oryn-m7-fold]`，场景与前置写在 `web/scripts/m7-accept.ts` 的文件头。要点：

- 依赖现有 dev 部署（`make dev`、provider、至少一个可用模型），App 经 `adb reverse tcp:7600` 连 `http://localhost:7600`。
- 设备只按 AVD 名选择（默认 `oryn-m7`）：脚本用 `adb -s emulator-N emu avd name` 找到对应的模拟器，没有就以空闲端口启动它；本机其它模拟器（如无关的 `emulator-5554`）不会被安装、改动或关闭。
- UnifiedPush 场景用本机 ntfy 容器（`binwiederhier/ntfy:v2.37.2`，`127.0.0.1:7680`）与 `.dev/m7/` 里的 ntfy APK。
- A11（Viewport Segments）只能在折叠 AVD 上验证：默认 AVD 上它报 SKIP，另跑 `bun run accept:m7 -- --only A11 --avd oryn-m7-fold`。
- FCM 场景（A23）需要 gms debug 包（`M7_GMS_APK`，默认 `.dev/m7/oryn-gms-debug.apk`）、带 Google Play 服务且能出网的模拟器镜像，以及 dev 部署里已写入 `fcm/service-account` 密钥（`dev/orynd.yaml` 已引用该名字）：`./bin/oryn secrets set fcm/service-account < .dev/m7/fcm-service-account.json`。
- release 包场景（A19）需要 `.dev/android-release.jks` 与 `M7_KEYSTORE_PASSWORD`，并核对上文记录的证书指纹。

## 发版

1. 改 `web/src-tauri/Cargo.toml` 的 `version`（只写 `x.y.z`；`versionCode` = `major*1000000 + minor*1000 + patch`，预发布与正式版相同）。
2. 打 tag `mobile-v<版本>` 或预发布 `mobile-v<版本>-rc.N` 并推送。
3. `.github/workflows/mobile.yml`：`prepare` 校验 tag 与版本 → `release` 建草稿 Release → `android-release`（foss / gms 各一套：debug APK、插件单测、签名的 release APK 与 AAB、`apksigner` / `zipalign` / `aapt2` 校验并把包名、版本号、SDK 与证书指纹写进 Step Summary、上传 Release 资产）→ `android-smoke`（API 34 模拟器里装 foss debug 包，90 秒内出现 `launcher.ready`）→ `ios-check`（macOS 上不签名编译模拟器包）→ `publish` 发布 Release。
4. Release 资产只有 release 签名的 `Oryn_<版本>_android-{foss,gms}.apk` 与 `.aab`。debug APK（可调试、允许任意明文请求）只作为 workflow artifact，从 Actions 运行页下载，不进 Release。

PR 与手动触发只产出 debug APK（workflow artifacts）并跑冒烟与 iOS 编译，拿不到签名密钥；来自 fork 的 PR 没有 `ANDROID_GOOGLE_SERVICES_JSON`，跳过 gms。手动触发可勾选 `export_ios_project`，把 `tauri ios init` 生成的 `gen/apple` 作为产物导出以便入库。

### 工作流的安全约束

- 第三方 action 一律钉到完整提交 SHA；checkout 不保留凭据；workflow 级权限为空，只有建 / 发 Release 与上传资产的 job 有 `contents: write`。
- secret 只出现在用到它的那一步的 `env` 里：`ANDROID_GOOGLE_SERVICES_JSON` 只给「写入 google-services.json」，三个签名 secret 只给 tag 发布时的「构建签名的 release APK + AAB」；密钥解码到 `$RUNNER_TEMP` 下的临时目录，`keystore.properties` 与 `.jks` 在该步骤结束时删除。

### Secrets

三个签名 secret 放在受保护的 environment `mobile-release` 里（部署规则只允许 tag `mobile-v*`），不是仓库级 secret；`ANDROID_GOOGLE_SERVICES_JSON` 仍是仓库级（PR 的 gms debug 构建要用，它不是签名材料）。

| 名称 | 内容 |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | 发布密钥库（PKCS12）的 base64：`base64 -w0 android-release.jks` |
| `ANDROID_KEYSTORE_PASSWORD` | 密钥库与密钥的口令（两者相同） |
| `ANDROID_KEY_ALIAS` | 密钥别名（`oryn`） |
| `ANDROID_GOOGLE_SERVICES_JSON` | Firebase 控制台下载的 `google-services.json` 全文（须含 `com.lynricsy.oryn` 客户端） |

FCM 的**服务账号** JSON 不进 CI：它只用于服务端发送，存在控制面的密钥库里。

### 发布密钥的保管

```bash
PW=$(openssl rand -base64 24)
keytool -genkeypair -v -keystore .dev/android-release.jks -storetype PKCS12 -alias oryn -keyalg RSA -keysize 4096 \
  -validity 10950 -storepass "$PW" -keypass "$PW" -dname "CN=Oryn, O=Lynricsy"
gh api -X PUT repos/<owner>/<repo>/environments/mobile-release \
  -F 'deployment_branch_policy[protected_branches]=false' -F 'deployment_branch_policy[custom_branch_policies]=true'
gh api -X POST repos/<owner>/<repo>/environments/mobile-release/deployment-branch-policies -f name='mobile-v*' -f type=tag
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
2. **gms 包**：在自己的 Firebase 项目里添加包名 `com.lynricsy.oryn` 的 Android 应用，下载 `google-services.json` 放到 `web/src-tauri/gen/android/app/`，以 `ORG_GRADLE_PROJECT_orynDistribution=gms` 构建；服务器端用**同一个 Firebase 项目**的服务账号启用 FCM。
3. 在 fork 的仓库里配好上表的 secrets，推送 `mobile-v*` tag 即由 CI 构建发布。
4. 自签的包与官方包证书不同，不能互相覆盖安装；改应用标识需要同时改 `tauri.conf.json` 的 `identifier`、`gen/android` 的 `applicationId` / `namespace` 与 Firebase 里的包名。

## 附录 A：iOS（待 Apple Developer Program）

当前 `ios-check` 只验证 iOS 目标能在 macOS 上不签名编译。等 ADP 就绪后再做：`bundle.iOS.developmentTeam` 与 TestFlight 上传 job；APNs 推送（服务端 `pushSenders["apns"]`、`PushSubscriptionInput` 增加 `kind: apns`）；Share Extension（App Group 容器 + 同一条上传预填流程）；Universal Links（`/.well-known/apple-app-site-association`）；Keychain 令牌存储的真机验证；iPhone / iPad 真机验收。
