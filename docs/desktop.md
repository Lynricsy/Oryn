# 桌面 App（Tauri 2）

Windows / macOS / Linux 的 Oryn 桌面客户端。App 本身只带一个很小的「连接页」，配对完成后主窗口加载**所连服务器自己的 Web UI**，所以界面总是和服务器同一版本，服务器升级不需要同步升级 App。

- 产品名 `Oryn`，应用标识 `com.lynricsy.oryn`，二进制 `oryn-desktop`（`oryn` 是 CLI），深链接协议 `oryn://`。
- 设备令牌只存在于 App 的 Rust 进程和系统凭据库里，网页代码拿不到它：App 用令牌向服务器换一个「属于该设备」的网页会话 Cookie，再在主窗口里打开 Web UI。
- 服务器必须走 HTTPS（见 [deploy.md](deploy.md)）。App 需要联网，连不上时停在连接页自动重连。

## 安装

从 GitHub Releases 的 `desktop-v<版本>` 下载：

| 平台 | 安装包 |
|---|---|
| Windows x64 | `Oryn_<版本>_x64-setup.exe`（NSIS）或 `.msi`。包没有代码签名，SmartScreen 会提示「未知发布者」，选「仍要运行」。 |
| macOS（Apple Silicon 与 Intel 通用） | `Oryn_<版本>_universal.dmg`。已签名并公证的包可以直接打开；没配 Apple 证书的构建是 ad-hoc 签名，只能在 Apple Silicon 上运行，首次需在 Finder 里右键「打开」。 |
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

- **托盘**：关闭窗口时默认隐藏到托盘，可在「设置 → 桌面应用」关掉。托盘菜单显示待你处理和未读的线程（最多 5 条），另有「打开 Oryn」「新建线程」「检查更新…」「更换服务器…」「退出 Oryn」。有待处理或未读线程时，托盘图标带一个点。
- **系统通知**：窗口隐藏或最小化时，线程跑完、等你回答或出错会弹系统通知，点击直接打开该线程。窗口在前台时只显示应用内提示。App 没在运行时不会有桌面通知，这时由服务器上配置的其它渠道（Telegram、Web Push 等）送达。
- **深链接**：`oryn://thread/<线程 id>` 打开该线程，`oryn://` 打开首页。App 始终只有一个实例，再次启动或点击深链接都会唤起已运行的窗口。
- **全局快捷键**：默认 `Control+Alt+Space`，用来显示或隐藏主窗口。可在「设置 → 桌面应用」改成其它组合（格式如 `Control+Alt+K`），留空表示不注册。快捷键被别的程序占用时，设置页会显示原因并保留旧值。
- **窗口位置与大小**：退出前自动保存，下次启动时恢复。
- **退出**：托盘「退出 Oryn」，或在命令行运行 `oryn-desktop --quit`，后者让正在运行的实例正常退出。
- **下载**：网页里的下载保存到系统「下载」目录，同名文件自动编号，完成后弹一条通知。

### 已知限制

- **Wayland**：全局快捷键依赖 X11 抓键，在纯 Wayland 会话里不可用（设置页会显示错误），其它功能不受影响。XWayland 下运行的 App 能否抓到键取决于合成器。
- **GNOME 托盘**：GNOME 默认不显示托盘图标，需要装 AppIndicator 扩展。没有托盘时，可以用全局快捷键或再次启动 App 唤回窗口，也可以在设置里关掉「关闭窗口时隐藏到托盘」。
- Linux 上缺少 `libayatana-appindicator` 时托盘不可用。此时 App 本次运行按「关闭窗口即退出」处理，避免窗口藏起来后找不回来。
- 终端面板固定使用 xterm 的 DOM 渲染器（Linux 的 WebKitGTK 上 WebGL 渲染器不稳定）。

## 自动更新

App 启动进入 Web UI 后检查一次更新，之后每 6 小时检查一次；托盘「检查更新…」和设置页可以立即检查。发现新版本后在后台下载，下载完托盘和设置页出现「重启以更新到 v<版本>」。打开「下载后自动安装并重启」时，下载完成后直接安装并重启。

更新源是 GitHub Releases 上滚动发布的 `desktop-updater` 里的 `latest.json`。每个更新包都用 App 内置的公钥校验签名，校验失败就不安装。预发布版本（版本号带 `-`，如 `0.2.0-rc.1`）不进入更新通道。Linux 的更新包只提供 AppImage，用 deb / rpm 安装的请下载新包后用系统包管理器升级。

## 开发

```bash
cd web && bun install
bun run tauri dev          # 连接页走 Vite 开发服务器（5173），改动 Rust 后自动重编
```

开发态和正式包一样，要求服务器是 HTTPS。本机起一套 orynd 时，用 `dev/tlsproxy` 在它前面加 TLS：

```bash
go run ./dev/tlsproxy -init -dir .dev/tls                  # 生成本机 CA 与 localhost 证书（已有则复用）
sudo trust anchor --store .dev/tls/ca.crt                  # 让系统与 WebKitGTK 信任该 CA；撤销：sudo trust anchor --remove .dev/tls/ca.crt
# orynd.yaml：public_url: "https://localhost:7643"，portal_domain 留空
go run ./dev/tlsproxy -listen 127.0.0.1:7643 -target http://127.0.0.1:7600 -dir .dev/tls
```

`tlsproxy` 保留原始 `Host`，设置 `X-Forwarded-For` / `-Proto`，透传 WebSocket 且不缓冲。`-static <目录>` 会把该目录挂到 `/updates/`，用于本机测试自动更新。CA 一旦生成就不再改动，叶子证书临近过期时自动重签。

Rust 侧的检查（与 CI 的 `desktop` job 相同）：

```bash
cd web && bun run build:launcher           # generate_context! 在编译期需要 dist-launcher
cd src-tauri && cargo fmt --check && cargo clippy --locked -- -D warnings && cargo test --locked
```

设环境变量 `ORYN_DESKTOP_EVENT_LOG=<文件>` 后，App 把关键事件逐行写成 JSON（配对、会话、窗口、托盘、通知、深链接、快捷键、更新等）。日志不包含令牌、verifier 和一次性码，验收脚本就是靠它观察 App 的行为。`oryn-desktop --smoke` 用临时配置目录启动一遍，连接页成功调用 IPC 后以退出码 0 退出；CI 在三个平台上都跑这一步。

### 本机验收

```bash
sudo pacman -S --needed libayatana-appindicator dunst gnome-keyring patchelf
make web && make build                                     # bin/orynd bin/orbd bin/oryn bin/fakeomp
go run ./dev/tlsproxy -init -dir .dev/tls && sudo trust anchor --store .dev/tls/ca.crt
cd web && bun run tauri build --no-bundle                  # D1 会构建全部安装包
bun run accept:m6 [-- --only D2,D5]
```

`web/scripts/m6-accept.ts` 在 `dbus-run-session` 和 `xvfb-run` 里运行，所需的服务全部在临时目录里自建：orynd（7610）、tlsproxy（7643）、process 驱动的 provider、私有的 gnome-keyring 与 dunst、一个只记录参数的假 `xdg-open`。跑完自动清理，结果和日志保存在 `/tmp/oryn-m6-accept/<run>/`。D11 需要两次签名的 release 构建，耗时较长，构建目录是 `.dev/m6-d11-target`。D14 / D15 只读取 CI 的结果，需要先推送 tag（见下）并登录 `gh`。

## 发版

1. 修改 `web/src-tauri/Cargo.toml` 的 `version`。这是版本号的唯一来源，`tauri.conf.json` 不写版本。
2. 提交并推送到 `main`。
3. `git tag desktop-v<版本> && git push origin desktop-v<版本>`。tag 必须与 `Cargo.toml` 的版本完全一致，否则工作流直接失败。

`.github/workflows/desktop.yml` 依次执行：

- 创建草稿 Release。
- 在 Ubuntu 22.04、Windows、macOS（universal）上构建，跑 `--smoke`，把安装包上传到 Release。
- 生成 `latest.json`（`web/scripts/updater-manifest.ts`），然后发布 Release。
- 不是预发布时，用新的 `latest.json` 覆盖 `desktop-updater`，已安装的 App 随后会收到更新。

PR 只要改动 `web/src-tauri/**`、`web/src/launcher/**`、`web/launcher.html` 或这个工作流本身，就会跑一遍三平台构建和冒烟。构建产物作为 workflow artifacts 上传，不发布。

### Secrets（仓库 Settings → Secrets and variables → Actions）

| 名称 | 内容 | 没有时 |
|---|---|---|
| `TAURI_SIGNING_PRIVATE_KEY`、`TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | 更新签名私钥（`bun run tauri signer generate`）及其口令；公钥在 `tauri.conf.json` 的 `plugins.updater.pubkey` | 不产出 `.sig` 与 `latest.json`，已安装的 App 收不到这一版的更新 |
| `APPLE_CERTIFICATE` | 「Developer ID Application」证书导出的 `.p12`，base64 单行 | macOS 包为 ad-hoc 签名 |
| `APPLE_CERTIFICATE_PASSWORD` | 导出 `.p12` 时设的口令 | 同上 |
| `APPLE_SIGNING_IDENTITY` | `Developer ID Application: <名称> (<TEAMID>)` | 同上 |
| `APPLE_API_ISSUER`、`APPLE_API_KEY`、`APPLE_API_KEY_P8` | App Store Connect API 密钥的 Issuer ID、Key ID、`.p8` 文件内容（推荐） | 若也没有下面一组：已签名但未公证 |
| `APPLE_ID`、`APPLE_PASSWORD`、`APPLE_TEAM_ID` | Apple ID、App 专用密码、Team ID（公证的另一种凭据，两组配一组即可） | 同上 |

每个构建 job 的 Step Summary 都会写明本次的签名状态：「已签名并公证」「已签名未公证」或「未签名（ad-hoc）」。fork 来的 PR 拿不到 secrets，会走未签名路径，所以这条降级路径在每个 PR 上都有覆盖。

更新签名私钥一旦丢失，已安装的 App 就再也收不到更新，只能让用户手动重装带新公钥的版本。私钥文件和口令务必离线备份。
