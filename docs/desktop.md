# 桌面 App（Tauri 2）

Windows / macOS / Linux 的 Oryn 桌面客户端。App 本身只带一个很小的「连接页」，配对完成后主窗口加载**所连服务器自己的 Web UI**，所以界面总是和服务器同一版本，服务器升级不需要同步升级 App。

- 产品名 `Oryn`，应用标识 `com.lynricsy.oryn`，二进制 `oryn-desktop`（`oryn` 是 CLI），深链接协议 `oryn://`。
- 设备令牌只存在于 App 的 Rust 进程和系统凭据库里，网页代码拿不到它：App 用令牌向服务器换一个「属于该设备」的网页会话 Cookie，再在主窗口里打开 Web UI。
- 服务器必须走 HTTPS（见 [deploy.md](deploy.md)）。App 需要联网，连不上时停在连接页自动重连。

## 安装

从 GitHub Releases 的 `v<版本>` 下载（服务端、桌面与移动 App 同一个 Release；`0.1.0` 及更早的预发布在 `desktop-v<版本>`）：

| 平台 | 安装包 |
|---|---|
| Windows x64 | `Oryn_<版本>_x64-setup.exe`（NSIS）或 `.msi`。包没有代码签名，SmartScreen 会提示「未知发布者」，选「仍要运行」。 |
| macOS（Apple Silicon / Intel 分架构） | Apple Silicon 用 `Oryn_<版本>_aarch64.dmg`，Intel 用 `Oryn_<版本>_x64.dmg`。已签名并公证的包可以直接打开；没配 Apple 证书的构建是 ad-hoc 签名，首次需在 Finder 里右键「打开」。 |
| Linux x64 | `.deb`、`.rpm` 或 `.AppImage`。 |

Linux 运行时依赖：WebKitGTK 4.1、GTK 3、librsvg，托盘需要 `libayatana-appindicator`（Debian / Ubuntu：`libayatana-appindicator3-1`；Arch：`libayatana-appindicator`），令牌存储使用 Secret Service（GNOME Keyring、KWallet 或 KeePassXC）。AppImage 需要 FUSE 2；没有 FUSE 时设 `APPIMAGE_EXTRACT_AND_RUN=1` 运行。

## 首次运行与配对

1. 在连接页填服务器地址，例如 `oryn.example.com`。不写协议时按 `https://` 处理，路径部分会被忽略。只有 `localhost`、`127.0.0.1`、`[::1]` 可以用 `http://`，但 Linux 上实际仍需 HTTPS，原因见 [deploy.md](deploy.md)。
2. App 在**系统浏览器**里打开 `<服务器>/pair`，连接页上显示配对码（如 `ABCD-EFGH`，10 分钟内有效）。
3. 在浏览器里用 Passkey 登录，输入配对码并批准。App 自动收到设备令牌，存入系统凭据库（macOS 钥匙串、Windows 凭据管理器、Linux Secret Service），随后进入 Web UI。

也可以从命令行预填地址：`oryn-desktop --server https://oryn.example.com`。

- **Linux 没有 Secret Service 时**：令牌写入 `~/.config/com.lynricsy.oryn/credentials.json`，权限 0600，与 CLI 的 `~/.config/oryn/credentials.json` 是同一种做法。连接页和「设置 → 桌面应用」会提示这一点。之后这台机器一直使用文件存储，不再尝试凭据库。
- **App 里不能批准其它设备的配对**：App 的会话由设备令牌换来，服务器拒绝它签发新设备。要批准配对，请在浏览器里用 Passkey 登录。
- **吊销**：在任何一端的「设置 → 设备」里吊销这台桌面设备后，App 删除本地令牌并回到连接页。
- **更换服务器**：「设置 → 桌面应用 → 更换服务器…」或托盘菜单。确认后 App 会尽量在服务器上吊销自己，删除本地令牌，然后重启。

## 日常使用

- **启动画面**：已配对时启动不显示连接卡片，只在窗口中央显示一枚大品牌标（右侧竖线闪烁），底部点阵上有光扫过；Web UI 就绪后品牌标平移缩小，落到首页输入框上方的品牌标位置。连不上服务器或需要配对时，品牌标飞进连接卡片。系统开启「减少动态效果」时竖线不闪、光不扫，就绪后整体淡出。
- **托盘**：关闭窗口时默认隐藏到托盘，可在「设置 → 桌面应用」关掉。托盘菜单显示待你处理和未读的线程（最多 5 条），另有「打开 Oryn」「新建线程」「检查更新…」「更换服务器…」「退出 Oryn」。有待处理或未读线程时，托盘图标带一个点。
- **系统通知**：窗口隐藏或最小化时，线程跑完、等你回答或出错会弹系统通知，点击直接打开该线程。窗口在前台时只显示应用内提示。App 没在运行时不会有桌面通知，这时由服务器上配置的其它渠道（Telegram、Web Push 等）送达。
- **深链接**：`oryn://thread/<线程 id>` 打开该线程，`oryn://` 打开首页。App 始终只有一个实例，再次启动或点击深链接都会唤起已运行的窗口。
- **全局快捷键**：默认 `Control+Alt+Space`，用来显示或隐藏主窗口。可在「设置 → 桌面应用」改成其它组合（格式如 `Control+Alt+K`），留空表示不注册。快捷键被别的程序占用时，设置页会显示原因并保留旧值。
- **窗口位置与大小**：退出前自动保存，下次启动时恢复。
- **退出**：托盘「退出 Oryn」，或在命令行运行 `oryn-desktop --quit`，后者让正在运行的实例正常退出。
- **下载**：网页里的下载保存到系统「下载」目录，同名文件自动编号，完成后弹一条通知。

### 窗口与标题栏

主窗口没有系统标题栏，窗口控件放进 Oryn 自己的侧栏 / 面板顶部。三个平台的做法不同：

| 平台 | 窗口控件 | 位置 |
|---|---|---|
| macOS | 保留系统红绿灯（`titleBarStyle: Overlay`，标题隐藏），不自绘 | 窗口左上 (22, 24)，与侧栏品牌行同一行：红绿灯右侧先是一条发丝分隔线（两侧各 10px），然后是品牌标、字标和通知 / 侧栏开关 / 新建，行高 48px、中线对齐红绿灯，按钮间距收紧。字标按品牌区剩余宽度显示：放得下完整字标才显示，否则只留品牌标（红绿灯让出的宽度随系统版本变化，比如 macOS 26 的红绿灯更宽，所以不按侧栏宽度写死）。侧栏收起时红绿灯落在页头左端，页头让出 84px |
| Windows | 自绘 最小化 / 最大化（最大化后变「还原」）/ 关闭，系统装饰关闭（`decorations: false`） | 右上：右面板展开时在面板标签条右侧、同一行，替掉原来关闭面板的 ×（关面板用页头的面板开关）；面板收起或没有面板的页面（首页、设置）在页头右端；登录、配对卡与连接页在页顶一条透明标题行的右端 |
| Linux | 同 Windows | 默认右上，位置同 Windows。启动时读 GTK 的 `gtk-decoration-layout`（GNOME 由「窗口标题栏按钮」设置同步过来，例如 `appmenu:close`、`close,minimize,maximize:`）：按它决定放左边还是右边、显示哪几个、按什么顺序；放左边时侧栏展开时在侧栏顶部单独一行（三个按钮和品牌行排不进 256px），侧栏收起时在页头左端，右面板保留 ×。读不到或一个按钮都没有时默认右侧三个 |

- **拖动**：侧栏顶部标题行与品牌行、各页头的空白处、右面板顶部标题行、整页卡片的页顶标题行都能拖动窗口；按住移动超过 3px 才开始移动窗口（单纯点击不交给窗口管理器，所以双击认得出来），双击这些地方切换最大化。按钮、链接、输入框、标签等可交互元素不会被拖动。
- **拉伸**：Windows / Linux 去掉系统装饰后仍可拖窗口四边与四角改变大小（Tauri 在窗口边缘保留了约 5px 的拉伸区）。
- **关闭**：自绘的关闭按钮与系统关闭完全一样，仍遵守「关闭窗口时隐藏到托盘」。
- **全屏时**自绘按钮隐藏；**窗口失焦时**自绘按钮变淡。macOS 全屏时系统收起红绿灯，分隔线立即淡出，侧栏品牌区用 transform 平移到红绿灯原来的位置（260ms、`--ease`），退出全屏时平移回来：进入全屏延后 180ms 再走（壳报告全屏时系统的全屏过渡刚开始，先让红绿灯随标题栏淡出），退出不延后（红绿灯在系统退出过渡结束时才出现，品牌区早已让出位置）。退出全屏时分隔线等品牌区回位后（260ms）再淡入。减少动效时直接切换。
- **macOS 全屏的红绿灯**：壳在进入全屏完成后（`NSWindowDidEnterFullScreen`）藏起原生红绿灯与工具栏、退出全屏完成后再显示（事件日志 `window.controls`）。不在「将要进入」时藏，是因为系统拒绝进入全屏时不会发「已退出」，窗口模式会丢掉原生红绿灯；代价是进入过渡期间原生红绿灯仍随标题栏显示。页面在原位置自绘一组红绿灯：平时隐藏，指针移到左上角（红绿灯让位宽 × 标题行）或键盘聚焦时淡入，离开 300ms 后淡出；唤出期间侧栏品牌区平移回来让位。红灯 = 关闭（同原生，遵守「隐藏到托盘」），黄灯置灰（全屏里不能最小化，与系统一致），绿灯 = 退出全屏（`host_window{toggleFullscreen}`）。侧栏收起时页头在全屏里仍按红绿灯让位，红绿灯不会盖住页头按钮。系统在指针贴到屏幕顶边时滑出的菜单栏不受影响。
- 起因：macOS 26 起主窗口挂了一个空的 unified 工具栏（换上大圆角外框），全屏后菜单栏下方曾出现一条白带、盖住页面顶部；最可能的原因是工具栏连同标题栏在全屏时被放进不透明的全屏工具栏窗口（未在真机对照验证）。若藏起后白带仍在，应转查 Overlay 标题栏本身。
- 只在桌面 App 里生效：浏览器和手机 App 里没有窗口按钮，留白也不变。页面靠壳注入的 `__ORYN_HOST__.chrome`（`platform`、`controls` = `native-left` / `custom-left` / `custom-right` / `none`、`buttons`、`inset`、`radius`、`fullscreenLights`）判断，宿主协议版本（bridge）为 3；没有 `fullscreenLights` 的旧壳全屏时页面不画红绿灯。最大化、全屏、聚焦变化由壳经 `window` 事件推给页面。窗口操作只有一个宿主命令 `host_window`（`state` / `minimize` / `toggleMaximize` / `close` / `startDrag` / `toggleFullscreen`），连接页（`capabilities/launcher-desktop.json`，只在桌面平台）与远程页面都只多这一项权限，没有开放任何 `core:window:*`。
- 窗口状态插件不再保存「是否有装饰」：老版本存下的设置不会把系统标题栏带回来。

### 已知限制

- **Wayland**：全局快捷键依赖 X11 抓键，在纯 Wayland 会话里不可用（设置页会显示错误），其它功能不受影响。XWayland 下运行的 App 能否抓到键取决于合成器。
- **GNOME 托盘**：GNOME 默认不显示托盘图标，需要装 AppIndicator 扩展。没有托盘时，可以用全局快捷键或再次启动 App 唤回窗口，也可以在设置里关掉「关闭窗口时隐藏到托盘」。
- Linux 上缺少 `libayatana-appindicator` 时托盘不可用。此时 App 本次运行按「关闭窗口即退出」处理，避免窗口藏起来后找不回来。
- 终端面板固定使用 xterm 的 DOM 渲染器（Linux 的 WebKitGTK 上 WebGL 渲染器不稳定）。

## 自动更新

App 启动进入 Web UI 后检查一次更新，之后每 6 小时检查一次；托盘「检查更新…」和设置页可以立即检查。发现新版本后在后台下载，下载完托盘和设置页出现「重启以更新到 v<版本>」。打开「下载后自动安装并重启」时，下载完成后直接安装并重启。

更新源是 GitHub Releases 上滚动发布的 `desktop-updater` 里的 `latest.json`。每个更新包都用 App 内置的公钥校验签名，校验失败就不安装。预发布版本（tag 带后缀，如 `v0.2.0-rc.1`）不进入更新通道。Linux 的更新包只提供 AppImage，用 deb / rpm 安装的请下载新包后用系统包管理器升级。

## 开发

```bash
cd web && bun install
bun run tauri dev          # 连接页走 Vite 开发服务器（5173），改动 Rust 后自动重编
```

开发态和正式包一样，要求服务器是 HTTPS。本机起一套 orynd 时，用 `dev/tlsproxy` 在它前面加 TLS：

```bash
go run ./dev/tlsproxy -init -dir .dev/tls                  # 生成本机 CA 与 localhost 证书（已有则复用）
sudo trust anchor --store .dev/tls/ca.crt                  # 让系统与 WebKitGTK 信任该 CA
# orynd.yaml：public_url: "https://localhost:7643"，portal_domain 留空
go run ./dev/tlsproxy -listen 127.0.0.1:7643 -target http://127.0.0.1:7600 -dir .dev/tls
```

`tlsproxy` 保留原始 `Host`，设置 `X-Forwarded-For` / `-Proto`，透传 WebSocket 且不缓冲。`-static <目录>` 会把该目录挂到 `/updates/`，用于本机测试自动更新。CA 一旦生成就不再改动，叶子证书临近过期时自动重签。

关于这个本机 CA：

- CA 带 Critical 的 X.509 名称约束，只能为 `localhost`、`127.0.0.0/8`、`::1` 签发有效证书。即使 `.dev/tls/ca.key` 泄露，也不能用它冒充其它域名。
- 它只供本机使用。`ca.key` 不得复制到别处，也不得提交（`.dev/` 已在 `.gitignore` 中）。
- 不用时撤销信任：`sudo trust anchor --remove .dev/tls/ca.crt`。跑完本机验收后建议立即撤销，下次需要时再 `--store`。
- 旧版 `tlsproxy` 生成的 CA 没有名称约束，现在会被拒绝，启动时报错。处理方法：先 `sudo trust anchor --remove .dev/tls/ca.crt`，再 `rm -r .dev/tls`，然后重新 `-init` 并 `--store`。

Rust 侧的检查（与 CI 的 `desktop` job 相同）：

```bash
cd web && bun run build:launcher           # generate_context! 在编译期需要 dist-launcher
cd src-tauri && cargo fmt --check && cargo clippy --locked -- -D warnings && cargo test --locked
```

设环境变量 `ORYN_DESKTOP_EVENT_LOG=<文件>` 后，App 把关键事件逐行写成 JSON（配对、会话、窗口、托盘、通知、深链接、快捷键、更新等）。日志不包含令牌、verifier 和一次性码，验收脚本就是靠它观察 App 的行为。`oryn-desktop --smoke` 用临时配置目录启动一遍，连接页成功调用 IPC 后以退出码 0 退出；CI 在三个平台上都跑这一步。

### App 图标

各端图标都由 `web/scripts/gen-icons.py` 从同一份字形（与界面里的 `BrandMark` 相同）生成，产物直接入库；改设计后在 `web/` 下重跑 `python3 scripts/gen-icons.py --preview /tmp/icons.png`（需要 `rsvg-convert` 与 Pillow），看一眼总览图再提交。默认图标只用黑白两色。

| 平台 | 文件 | 浅色 / 深色 |
|---|---|---|
| macOS 26+ | `icons/macos/AppIcon.icon`（Icon Composer 包），CI 用 `ci/macos-appicon.sh` 编进 `Contents/Resources/Assets.car`，`Info.plist` 的 `CFBundleIconName` 指向它 | 浅色、深色、着色随系统外观切换 |
| macOS 26 之前、`tauri dev` | `icons/icon.icns`（macOS 图标网格 + 投影） | 只有浅色 |
| Windows / Linux | `icons/icon.ico`、`icons/*.png`（浅色圆角方块） | 系统不支持按深浅切换应用图标 |

本地在 Mac 上打正式包时，先跑 `bash src-tauri/ci/macos-appicon.sh <目录>`，再用 `--config` 把 `bundle.macOS.files` 的 `Resources/Assets.car` 指到 `<目录>/Assets.car`（CI 的「签名配置」步骤就是这么做的）；不加也能打包，只是回退到 icns。CI 的「断言 App 图标（macOS）」检查包里的 `CFBundleIconName` 与 `Assets.car` 里有带深色外观的 AppIcon。

### 本机验收

```bash
sudo pacman -S --needed libayatana-appindicator dunst gnome-keyring patchelf
make web && make build                                     # bin/orynd bin/orbd bin/oryn bin/fakeomp
go run ./dev/tlsproxy -init -dir .dev/tls && sudo trust anchor --store .dev/tls/ca.crt
cd web && bun run tauri build --no-bundle                  # D1 会构建全部安装包
bun run accept:m6 [-- --only D2,D5]
M6_WM=openbox bun run accept:m6 -- --only D16             # 无系统标题栏：按钮、拖动、拉伸、关闭（需要窗口管理器与 ImageMagick 的 import 截图）
sudo trust anchor --remove .dev/tls/ca.crt                 # 验收结束后撤销对本机 CA 的信任
```

`web/scripts/m6-accept.ts` 在 `dbus-run-session` 和 `xvfb-run` 里运行，所需的服务全部在临时目录里自建：orynd（7610）、tlsproxy（7643）、process 驱动的 provider、私有的 gnome-keyring 与 dunst、一个只记录参数的假 `xdg-open`。跑完自动清理，结果和日志保存在 `/tmp/oryn-m6-accept/<run>/`。D11 需要两次签名的 release 构建，耗时较长，构建目录是 `.dev/m6-d11-target`。D14 / D15 只读取 CI 的结果，需要先推送 tag（见下）并登录 `gh`。D16 必须带 `M6_WM=openbox`：先用一个未配对的 App 在连接页上检查没有窗口管理器的标题栏框（`_NET_FRAME_EXTENTS` 全 0）、点最大化 / 还原后窗口尺寸变化、双击拖动区切换最大化、拖动顶栏能移动窗口、在按钮上按下拖动不会移动窗口、拖右缘能拉宽、关闭按钮把窗口藏到托盘，再在已配对的主 App 首页点一次最大化 / 还原；截图在 `<结果目录>/D16-*.png`。

D1（全部安装包）和 D11（两次签名的更新包）用的都是**一次性**更新密钥：每次运行用 `tauri signer generate` 在临时目录里新生成一把，再用 `--config` 把 `plugins.updater.pubkey` 换成它的公钥，运行结束随临时目录一起删除。验收不读取、也不需要正式发版密钥。因此 D1 产出的安装包内置的是一次性公钥，只能用于验收，不能对外分发。

## 发版

服务端、桌面与移动 App 一起发版：一个 tag、一个 Release，同时包含服务端二进制、三平台安装包、Android APK / AAB，并上传 iOS TestFlight。

1. 修改 `web/src-tauri/Cargo.toml` 的 `version`。这是全产品版本号的唯一来源，`tauri.conf.json` 不写版本，服务端的版本号由 tag 注入。
2. 提交并推送到 `main`。
3. `git tag v<版本> && git push origin v<版本>`；预发布打 `v<版本>-rc.N`。`Cargo.toml` 的版本只写 `x.y.z`（rpm 不接受 `-`，msi 只接受纯数字预发布号），tag 去掉后缀后必须与它一致，否则工作流直接失败。

`.github/workflows/release.yml` 依次执行：

- 校验 tag，创建草稿 Release（标题 `Oryn <版本>`）。
- 并行三路：
  - `server`：在 Linux 上构建 Web UI 并交叉编译（静态链接），上传 `oryn-server_<版本>_linux-{x64,arm64}.tar.gz`（`orynd`、`orbd`、`oryn`、`gemini-rerank`）、`orbd_<版本>_darwin-arm64.tar.gz`（macOS Runner）与 `oryn-server_<版本>_SHA256SUMS`。二进制的版本号就是 tag（`orynd --version` 输出 `v<版本>`）。上传前用临时数据目录启动 linux-x64 的 `orynd`，确认 `/healthz` 的版本、首页与入口脚本来自内嵌的 Web UI（没嵌入前端时 orynd 也能编译，但只返回占位页）。orb 镜像不随版本发布，部署方 checkout 同一 tag 后 `make image`（见 [deploy.md](deploy.md) 第 1 节）。
  - 调用 `desktop.yml`：分别构建 Linux x64（Ubuntu 22.04）、Windows x64、macOS arm64、macOS x64（独立架构，不打 universal），原生架构跑 `--smoke`，把安装包上传到 Release。
  - 调用 `mobile.yml`：见 [mobile.md](mobile.md)「发版」。
- 三路全部成功后，生成 `latest.json`（`web/scripts/updater-manifest.ts`），然后发布 Release。任一路失败时 Release 停在草稿，修好后在 Actions 里「Re-run failed jobs」续跑。
- 不是预发布时，用新的 `latest.json` 覆盖 `desktop-updater`，已安装的 App 随后会收到更新。

PR 只要改动 `web/src-tauri/**`、`web/src/launcher/**`、`web/launcher.html` 或 `desktop.yml` 本身，就会跑一遍三平台构建和冒烟。构建产物作为 workflow artifacts 上传，不发布。

### 工作流的安全约束

- 构建分成两个 job，共用同一份步骤（YAML 锚点）：`build` 处理 PR、分支和手动触发，令牌只读，不签名；`build-release` 只在 `release.yml` 调用时（推送 `v*` tag）运行，才有 `contents: write` 和签名 secrets（调用方以 `secrets: inherit` 传入）。所有 PR 都拿不到签名 secrets，同仓库分支发起的也一样。
- 签名 secrets 不写进 `$GITHUB_ENV`，也不放在 job 级 `env`。它们只出现在 `tauri-action` 那一步的 `env` 里，而且只取「签名配置」步骤判定要用的那一组，其余为空。由一个包装脚本把非空的值以 tauri 认识的变量名交给 `tauri build`。App Store Connect 的 `.p8` 只在构建期间写进临时目录，构建结束即删除。
- 第三方 action 一律钉到完整的提交 SHA，行尾注释写对应版本；所有 checkout 都设 `persist-credentials: false`；每个 job 单独声明最小的 `permissions`。升级某个 action 时，先用 `gh api repos/<owner>/<repo>/commits/<tag> -q .sha` 查出新 SHA，再一并更新注释，然后跑 `actionlint`。

### Secrets（仓库 Settings → Secrets and variables → Actions）

| 名称 | 内容 | 没有时 |
|---|---|---|
| `TAURI_SIGNING_PRIVATE_KEY`、`TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | 更新签名私钥（`bun run tauri signer generate`）及其口令；公钥在 `tauri.conf.json` 的 `plugins.updater.pubkey` | 不产出 `.sig` 与 `latest.json`，已安装的 App 收不到这一版的更新 |
| `APPLE_CERTIFICATE` | 「Developer ID Application」证书导出的 `.p12`，base64 单行 | macOS 包为 ad-hoc 签名 |
| `APPLE_CERTIFICATE_PASSWORD` | 导出 `.p12` 时设的口令 | 同上 |
| `APPLE_SIGNING_IDENTITY` | `Developer ID Application: <名称> (<TEAMID>)` | 同上 |
| `APPLE_API_ISSUER`、`APPLE_API_KEY`、`APPLE_API_KEY_P8` | App Store Connect API 密钥的 Issuer ID、Key ID、`.p8` 文件内容（推荐） | 若也没有下面一组：已签名但未公证 |
| `APPLE_ID`、`APPLE_PASSWORD`、`APPLE_TEAM_ID` | Apple ID、App 专用密码、Team ID（公证的另一种凭据，两组配一组即可） | 同上 |

每个构建 job 的 Step Summary 都会写明本次的签名状态：「已签名并公证」「已签名未公证」或「未签名（ad-hoc）」，以及更新包签名的有无。PR 运行一律显示「更新包签名 无」并走未签名路径，所以这条降级路径在每个 PR 上都有覆盖。

### 更新签名私钥的保管

更新签名私钥是自动更新通道唯一的完整性保障：谁拿到它，再加上 Release 的写权限，就能给所有已安装的 App 推送会被接受的更新。

- 私钥和口令**只放在两处**：离线备份（如密码管理器或加密的离线介质），以及上面的 CI secret。不要放进工作区（包括被 `.gitignore` 忽略的 `.dev/`），也不要把口令和私钥放在一起。
- 本机验收不需要私钥，D1 / D11 每次运行都用一次性密钥，见「本机验收」。
- 私钥一旦丢失，已安装的 App 就再也收不到更新，只能让用户手动重装带新公钥的版本。所以离线备份必不可少。
- 怀疑私钥泄露时（例如曾经放在工作区里），应生成新密钥对，更新 `tauri.conf.json` 的 `pubkey` 和 CI secret，并发一个新版本。还没有对外发布安装量时轮换最省事。
