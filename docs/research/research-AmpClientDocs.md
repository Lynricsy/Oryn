# Amp 官方文档与公告调研报告:客户端、线程、协作、文件(不含 orb 底层机制)

调研日期 2026-11-12。

**来源约定**
- 文档页给出的是人类可读 URL `https://ampcode.com/docs/...`。我实际读取的是它的 Markdown 版本 `https://ampcode.com/docs/markdown/...`(由 llms.txt 指出)。
- 公告 URL 为 `https://ampcode.com/news/<slug>`。
- 标注 `[推测]` 的是推测;「未找到」表示我读过相关页面但没有该信息。

## 0. 总览

**一句话架构**
- Amp 是「Web + 原生 App + CLI/TUI」共用同一套线程的系统。线程用 URL `https://ampcode.com/threads/T-<uuid>` 标识,同一线程可在所有客户端打开(来源:[threads](https://ampcode.com/docs/markdown/threads))。
- 执行位置与查看位置解耦。线程可在 orb、runner 或本地 CLI 会话里跑(同上)。
- 2026-05 的 CLI 重写「Neo」说明了架构:「remote-controllable, compaction-first, plugin-powered」。([news/neo](https://ampcode.com/news/neo))
- Neo 公告把新架构称为「distributed system with durable execution for the agent loop」(见 [news/agents-everywhere](https://ampcode.com/news/agents-everywhere))。
- 实时同步的底层协议(WebSocket/SSE 等)在文档中**未找到**。能确认的只有「live updates」「queue/dequeue/cancel」等用户可见行为。

**文档完整导航**
- 来源:[llms.txt](https://ampcode.com/llms.txt) 与 [sitemap.xml](https://ampcode.com/sitemap.xml)。
- `/docs/*` 的分组:
  - Get Started:Introduction、The Dial、Modes & Models、Prompting、Designing UI、Tools、Threads、Projects、Feedback Widget、Spaces、Integrations(+github/gitlab/google)、Pricing。
  - Orbs:16 页(本任务不展开)。
  - Boxes。
  - Apps:overview、mac、ios、android、windows、linux。
  - Puck:puck、slack-integration。
  - Collaborate:workspaces、multiplayer、huddle。
  - Customize:plugins、decision-models、global-plugins-and-skills、skills、mcp、agents-md、model-routing。
  - CLI:cli、keybindings、execute-mode、spawning-orbs、streaming-json、clone、settings、runners、remote-control。
  - Reference:sdk(+typescript/python)、plugin-api、egress-ips。
  - Support、Enterprise、`using-amp/*`。
- `/manual` 现在直接返回 `/docs` 的 Introduction,是旧入口([manual](https://ampcode.com/manual))。
- 旧公告里的 `/manual#plugins`、`/manual/plugin-api` 锚点现已对应 `/docs/customize/plugins` 与 `/docs/plugin-api`。

---

## 1. 客户端矩阵

**共通点**
- 所有 App「give you the same threads, orbs, and Puck as ampcode.com, and add notifications, dictation, and sharing from other apps」([apps](https://ampcode.com/docs/markdown/apps))。
- 最多同一设备保留 5 个账号,通过用户菜单 **Switch Account** 切换(同上)。
- Windows 例外:每个窗口绑定一个账号([apps/windows](https://ampcode.com/docs/markdown/apps/windows))。
- 无需在设备上安装别的东西,agent 跑在 orb 里(同上)。

**能力对照表**

| 客户端 | 系统要求 / 分发 | 通知 | 听写 | 分享进 App | 深链接 | 内置 runner | 其它特有功能 |
|---|---|---|---|---|---|---|---|
| Web (ampcode.com) | 无需安装 | 无(App 才有) | 有:`Cmd/Ctrl+Shift+D`,按住可「听写并发送」 | — | — | 否 | 全部功能入口 |
| Mac | Apple silicon + macOS 26+;dmg 直下或 `brew install --cask ampcode/tap/amp@beta`;Sparkle 每小时检查更新 | 线程等待下一条消息时通知;当前正在查看的线程不通知 | Shortcuts 里的「Dictate to Puck」「Call Puck」 | 任意 App 的分享表单分享**图片**给 Puck | Slack/Mail/Notes 点击 `ampcode.com/threads/...` 直接开 App;Safari 跨站也可;Chrome 先到浏览器,页面再询问「Open in Mac App / Always」 | 有(设置里 Runner 页) | Puck 可开独立窗口、多窗口、Keep Awake、Share This Mac's Display、Show in Finder / Open in Editor、Share With Workspace |
| iPhone / iPad | iOS/iPadOS 26+;仅 TestFlight 公开链接,「An App Store release will come later」 | 同上;App Settings 可关或前台抑制 | 同上 Shortcuts,可挂 widget / Action Button / Siri;Call Puck 在 iPhone 上表现为系统电话 | 分享图片;附件按钮可直接拍照/录像 | `ampcode.com/threads/...` 直接在 App 里打开 | 否 | 来电式 Puck 通话 |
| Android | Android 10+;APK 直下,自动更新,支持 Obtainium | 同上 | 长按图标:Dictate to Puck / Call Puck / New thread;Quick Settings 可加 Call Puck 瓦片 | **Share to Puck**:文本、图片、文件 | 同 iOS | 否 | 通话在息屏/后台保持 |
| Windows | Win10 1809+ 或 Win11;`.appinstaller`、MSIX、便携 zip(x64/Arm) | Windows 通知;含被 @ / 申请访问;multiplayer 申请带 **Share with Workspace / Allow Multiplayer / Decline** 按钮;任务栏未读数与闪烁 | 无 Puck 听写快捷方式 | File Explorer「Share」、Edge 等分享文本/链接/图片/文件(便携 zip 收不到) | Slack 里点链接直接开 App;浏览器里的链接先到浏览器 | 有(每账号一个) | 托盘、Jump List、全局热键(Ctrl+Alt+A/N/P、Ctrl+Alt+Shift+P)、Passkeys、Intune/ADMX 管理 |
| Linux | Debian12+/Ubuntu22.04+/Fedora/Arch,x64/ARM64;apt/dnf/AUR(`amp-desktop`),无 AppImage | 桌面通知,需 mako/dunst 等服务;否则转发到其它设备 | 无 | 未找到 | 链接先到浏览器,再「Open in Linux App」 | 有(systemd user service `amp-desktop-runner.service`) | `amp-desktop --new-thread / --puck / --call-puck`、托盘(StatusNotifierItem)、Omarchy 主题;单窗口,无独立 Puck 窗口 |
| CLI / TUI | macOS、Linux、Windows(推荐 WSL);`curl -fsSL https://ampcode.com/install.sh \| bash`、`install.ps1`、`brew install ampcode/tap/ampcode` | 声音/终端 bell(`amp.notifications.enabled`,SSH 下发 BEL) | 无;图片 `Ctrl+V` | — | — | 是(`amp --no-tui`) | 见下文 |

**来源**
- Mac:[apps/mac](https://ampcode.com/docs/markdown/apps/mac)。
- iOS:[apps/ios](https://ampcode.com/docs/markdown/apps/ios)。
- Android:[apps/android](https://ampcode.com/docs/markdown/apps/android)。
- Windows:[apps/windows](https://ampcode.com/docs/markdown/apps/windows)。
- Linux:[apps/linux](https://ampcode.com/docs/markdown/apps/linux)。
- CLI:[cli](https://ampcode.com/docs/markdown/cli)。

**技术形态**
- 文档只写「native app」。
- Linux 页提到「The web app's keyboard shortcuts work in the app」,Windows 页提到「Amp pages open in the app's window」「turns off the web developer tools」。
- `[推测]` 桌面端大概率是 Web 内容外壳加原生能力,例如托盘、runner、分享、Bluetooth passkey 桥接。
- 原因是 Windows 端提示「a passkey saved only in a browser … is not available in the app」。
- iOS/Android 的 UI 是否原生,文档图注称「native composer」([apps](https://ampcode.com/docs/markdown/apps) 的图片 alt)。

**深链接**
- 形态仅为 `https://ampcode.com/threads/...`,没有自定义 scheme。Universal Link / App Links / 协议处理由 OS 完成。
- 桌面端的 Web→App 交接流程:
  1. 线程页询问「Open in Mac App / Always Open in Mac App」。
  2. 选择**按账号保存**,且 Mac/Windows/Linux 分开。设置项:**Settings > Appearance > Open Links in the Mac App**。
  3. 选「总是」后,新标签页显示「Opening in the Amp App」,App 收到后自动关闭标签页;几秒内没打开则显示 **Open in Browser / 下载 App / Stop Opening in App**。
  4. 线程头部另有 **Open in Mac App** 按钮,单次发送、不改设置。
- 来源:[apps#open-threads-from-links](https://ampcode.com/docs/markdown/apps)。
- Chrome 限制:一次点击只允许一次打开外部 App,连续打开多个链接时后面的会卡在 Opening 页([apps/mac](https://ampcode.com/docs/markdown/apps/mac))。
- Portal 里可用 `target="_amp"` 链接让用户从 Portal 回到 Amp App([orbs/portals](https://ampcode.com/docs/markdown/orbs/portals))。
- 非线程的深链接(项目、Space、Puck)未找到。仅 Linux 有命令行 `amp-desktop --new-thread/--puck/--call-puck`。

**听写与词表**
- 默认快捷键 `Cmd+Shift+D`(mac)/ `Ctrl+Shift+D`(其它),再按一次停止,也可点麦克风图标。
- 按住快捷键或图标可「听写并松手即发送」。
- **自定义词表**:Settings → Composer → Dictation vocabulary,或 CLI `amp config vocabulary add <term>`。
- 工作区管理员可通过 **Enable Dictation**(Member Settings)整体关闭。
- 听写是语音→文字消息,回复仍是文字。双向语音要用 **Call Puck**。
- Portal 评论控件的听写只发送录音,不带线程上下文,也不带词表。
- 来源:[prompting#voice-input](https://ampcode.com/docs/markdown/prompting)。

**CLI/TUI 能力差异**
- 保留:TUI 内的本地交互线程、`@` 文件提及、编辑历史消息(Tab 选中后按 `e`)、`Ctrl+O` 命令面板、IDE 连接(VS Code/Cursor/Windsurf/Zed/Neovim)。
- 不支持:Web/App 端**不支持**文件 `@` 提及,官方称是「intentional」([prompting](https://ampcode.com/docs/markdown/prompting))。
- TUI 侧边栏已被移除([news/so-long-tui-sidebar](https://ampcode.com/news/so-long-tui-sidebar))。理由:线程多了之后,Web/原生 App 的侧边栏更适合管理。
- CLI 可通过 `amp app` 打开 Web 客户端(2026-06 新 UI 公告提到)。
- CLI 另有 `Ctrl+\` 在 2026-06 公告时用于显示侧边栏,后被移除,以 so-long-tui-sidebar 为准。
- 命令面板里有 `thread: new in orb`、`project: select/create`、`puck: open`(或 `Ctrl+X` 然后 `P`)。
- 「Web 版 Changes 窗格里的 diff」可从终端通过命令面板在浏览器打开([news/diffs](https://ampcode.com/news/diffs))。

---

## 2. 线程(thread)模型

### 2.1 executor 类型

**三类**
- **local**:本机 CLI 会话。
- **orb**:Amp 为线程新建的云端机器。
- **runner:`<id>`**:用户自己的机器上的 `amp --no-tui` 实例。

**选择方式**
- `amp --executor local | orb | runner:<id>` 用于打开 TUI 并预选。
- 加 `-x` 则非交互,例如 `amp -x "..." --executor runner:mac-mini`。
- `-ox` 是 `--orb-execute -x` 的简写。
- `--runner-dir <abs-path>` 选择 runner 服务的某个目录。
- 来源:[cli](https://ampcode.com/docs/markdown/cli)、[spawning-orbs](https://ampcode.com/docs/markdown/cli/spawning-orbs)、[execute-mode](https://ampcode.com/docs/markdown/cli/execute-mode)。

**其它位置概念**
- Boxes:常开机器,用 `Machine` 或具体 app 作为执行位置([boxes](https://ampcode.com/docs/markdown/boxes))。
- 自托管 orb provider(`--orb-provider`,`--orb-size` 取其 orb type)。
- Mac satellites:orb 额外起一台 Mac。
- 本任务不展开这三项,仅作线索。

**插件视角的 executor**
- `PluginExecutorKind = 'local' | 'remote' | 'unknown'`。
- `PluginExecutor.keepAlive(): Promise<Subscription>`:租约会自动续期并消耗 orb 运行时间,不在 Amp 管理的 orb 内会 reject,是尽力而为。
- 来源:[plugin-api](https://ampcode.com/docs/markdown/plugin-api) 的类型段。

**没有 executor 的对象**
- Puck 与 Space 没有 executor,因此**插件不加载**,命令也不可用([plugins](https://ampcode.com/docs/markdown/customize/plugins))。
- Space 启动的线程里插件会加载。

### 2.2 runner:`amp --no-tui`

来源:[cli/runners](https://ampcode.com/docs/markdown/cli/runners)。

**启动与标识**
- `amp --no-tui [--runner-id <id>]`:仅等待并运行远程创建的线程。
- Runner ID 必须是合法主机名,大小写不敏感,但保留输入的大小写。
- runner 的唯一标识最初是「host + 工作目录」([news/agents-anywhere](https://ampcode.com/news/agents-anywhere));后来改为 runner-id 加多目录。
- 无服务目录时 runner 不向 Amp 注册,不出现在选择器里;加目录后立刻注册。

**服务目录相关 flag**

| flag | 作用 |
|---|---|
| `--dir <path>` | 多服务一个目录,任意目录,不必是 Git |
| `--discover-dirs` / `=<path>` | 自动发现 Git checkout,默认当前目录往下 2 层,可重复 |
| `--discover-depth <1..10>` | 加深扫描 |
| `--discover-exclude <pattern>` | 按 `.gitignore` 语法跳过 |
| `--no-serve-cwd` | 不再自动服务当前目录 |

**目录发现行为**
- macOS/Linux 用文件系统监听,其它平台每分钟重扫。
- 跳过隐藏目录、`node_modules`、符号链接,最多服务 500 个 checkout。
- 超限时主 checkout 优先于 linked worktree。
- bare clone 本身不服务。
- 运行中调整:`amp runner dirs add|list|remove <path>`,重启后依然生效(同一启动目录下)。

**其它 flag**

| flag | 作用 |
|---|---|
| `--share` | 与工作区共享 runner |
| `--desktop` | 给线程加 Desktop 标签 |
| `--share-network` | Orb Network Routing:让 orb 经你的网络出网;orb 线程菜单选 `Orb › Network Routing`;CLI `amp net route <runner>` |
| `--amp-env` | 注入 ampcode.com 上的 Secrets & Env Vars;等价设置 `amp.runner.env.enabled: true` |
| `--remote-control-terminal` | 允许 Web 终端 |
| `--remote-control-portals` | 给线程加 Portal 标签 |
| `--runner-account-email`(Windows App 使用) | 指定账号 |

- 终端开关的环境变量:`AMP_REMOTE_CONTROL_TERMINAL=1|0`。显式 flag(`--remote-control-terminal` / `--no-remote-control-terminal`)优先于环境变量([cli/remote-control](https://ampcode.com/docs/markdown/cli/remote-control))。

**`--share` 细节**
- 启动时打印警告:工作区成员将以你的 OS 用户身份在此机器运行代码。
- 共享期间有效;去掉 `--share` 重启即取消,并**同时关闭该 runner 上所有线程的 multiplayer**。
- 其他成员在位置选择器的 **Shared Runners** 下看到它,带你的头像和运行线程数。
- 他人创建的线程**归他人所有**,runner 只获得该线程的访问;这些线程始终对整个工作区可见(要求工作区默认线程可见性为 workspace)。
- 共享 runner **不支持**创建目录/项目/worktree。
- 工作区管理员可关闭 **Allow Members to Share Runners**。关闭后立即取消共享,多人线程 multiplayer 关闭,运行中线程在 5 分钟内失去访问。

**Secrets & Env Vars 合并优先级**
- 个人 > 项目 > 工作区 > runner 进程环境。
- 共享 runner 只带工作区与项目变量,不带个人变量。
- 每次线程启动时拉取。
- 按目录生效,同目录内最新启动的线程决定该目录环境。
- 目录的 MCP 服务器与插件在变量变化时重启。

**worktree**
- 目录选择器里每个 Git checkout 提供 **New Worktree**(Tab 键切换)。
- 实际执行 `git worktree add`,同级目录名 `<repo>-<name>`,新分支同名。
- 创建前会 fetch 上游。
- 失败则不创建线程。
- 重名自动加 `-2`、`-3`。
- 「**Archive and Remove Worktree**」:先归档线程,再让 runner 执行 `git worktree remove` 并删分支。有未提交/未合并内容则**不强制**。
- Puck 和 `create_thread` 工具可传 `executor: "runner"`、`runner_id`、`worktree`。
- New Directory / New Project:`New Project` 会建 Amp-hosted project,并在 runner 机器上 `git init` 且设置 `origin`。

**自动更新**
- 约每小时检查一次。
- 无线程运行时重启,至少间隔 12 小时。
- 线程一直占用 1 天后,在无进行中回合时重启。
- 重启保持 runner-id、目录与其它 flag。
- 开关:`amp.runner.autoUpdate.enabled`,`amp.updates.mode: "disabled"` 也会关闭。
- Homebrew 安装的 runner 只报告新版本,不自行安装。

**Desktop**
- macOS:Apple Silicon、macOS 15+、arm64 CLI;Amp 安装 `Amp Desktop Helper.app` 到 `~/.amp/bin/amp-desktop-helper/<version>`。需要 Screen Recording + Accessibility。命令:`amp runner desktop status|allow screen-recording|allow accessibility`。
- Linux:headless **labwc**(1280x720)加 **Waymote**(`rockorager/waymote`)串流;需要 `labwc`、`wlr-randr`、`ffmpeg`,可选 dbus、pulseaudio;状态放在 `~/.cache/amp/runner-desktop/<runner-id>`。
- Desktop/Terminal 经 **WebRTC**:能直连则直连,否则 TURN 中继;5 秒内连不上则回退到 Amp 中继。
- 环境变量:`AMP_DESKTOP_WLR_RENDERER`。
- 共享既有 GNOME/KDE/X11 桌面「not yet supported」。

**TUI 作为 runner**
- 设置 `"amp.remoteThreadCreation.enabled": true`(或命令 `amp: enable remote creation of threads`)。
- 之后每个交互式 TUI 都接受网页创建的线程。

**runner vs orb 功能矩阵**(文档原表)

| 功能 | runner 上的情况 |
|---|---|
| Lifecycle | Amp 不启动/暂停机器 |
| Changes | Git/Jujutsu checkout 自动可用 |
| Files | 连接时自动可用 |
| Terminal | 需要 `--remote-control-terminal` |
| Portal | 需要 `--remote-control-portals` |
| Desktop | 需要 `--desktop`(macOS 和 Linux) |
| Multiplayer | 仅共享 runner |

### 2.3 多客户端实时同步

**已证实的行为**
- 在 CLI 启动的线程可从 ampcode.com 继续发送消息,即 **Cross-Client Access**:Web 端可「live updates、send messages、queue 与 dequeue、cancel」([news/neo](https://ampcode.com/news/neo)、[cli/remote-control](https://ampcode.com/docs/markdown/cli/remote-control))。
- orb 线程可在手机/另一台电脑上继续提示([threads#continue-a-thread-anywhere](https://ampcode.com/docs/markdown/threads))。
- `amp threads continue T-… -ox 'msg'`:向 orb 线程发一条消息但**不 attach**,消息在线程自己的 orb 上运行。
- `amp threads continue T-…`:把 CLI 挂到线程上。
- `amp sync <thread>`:把 orb 的改动镜像到本地 checkout。要求本地 checkout 无未提交修改;停止时恢复原状。`amp sync --apply` 一次性复制。
- 插件 UI(通知、确认框、输入框、选择框)在 TUI 与 Web **镜像**,同一个对话框,两端都能应答([plugins](https://ampcode.com/docs/markdown/customize/plugins)、[news/plugins-everywhere](https://ampcode.com/news/plugins-everywhere))。

**未找到**
- 文档中没有同步协议、冲突解决或 CRDT 的说明。

**离线与重连(仅限文档明确提到的)**
- runner 所在机器睡眠:约 90 秒内从选择器消失;已运行线程「pause」,命令与文件编辑等待 runner,唤醒后通常 1 分钟内重连并继续([apps/mac](https://ampcode.com/docs/markdown/apps/mac))。
- Portal 里的 WebSocket 会因 Amp 服务器每小时多次部署而无 close frame 断开(code 1006),客户端必须重连([orbs/portals](https://ampcode.com/docs/markdown/orbs/portals))。
- Webhook 事件先落盘再 200,至少一次投递,退避 5s 翻倍到 5min,1 小时后丢弃([orbs/automations](https://ampcode.com/docs/markdown/orbs/automations))。
- 插件 `ctx.tools.call`:连接丢失时不重试,报告「结果未知」。
- CLI 执行模式的 `--stream-json` 初始消息里 `subagents` 字段列出「attach 时仍在运行的后台子 agent」,说明线程可被不同进程先后挂载([cli/streaming-json](https://ampcode.com/docs/markdown/cli/streaming-json))。

### 2.4 消息队列 / steer

- 现状(2026-09-08 起):agent 忙时发送的消息默认 **steer**,即在当前步骤(命令或思考块)结束后送达,不等整轮结束([news/steer-dont-queue](https://ampcode.com/news/steer-dont-queue)、[prompting#queueing-messages](https://ampcode.com/docs/markdown/prompting))。
- 排队(等整轮结束):
  - Web `Cmd/Ctrl+Enter`。
  - iOS 长按发送键。
  - CLI `Ctrl+O` → `prompt: queue message`。
  - 默认可在 Settings → Composer & Dictation → Default Message Send Behavior 改。
- 已排队消息可 `Enter Enter` 提前 steer;`↑/↓` 选中编辑。
- `Esc Esc` 强制中断并立刻发送。
- **Ship / Review 等内置动作始终排队**。
- 历史:2026-05 的 Neo 把「排队」设为默认,2026-09 又改回 steer。可见该交互还在演化。
- streaming-json 输入消息有 `steer?: boolean` 字段。
- Space 或线程里被 @ 的行为见第 4 节。

### 2.5 归档、标签、颜色、搜索、固定、休眠

来源:[threads#organize-threads](https://ampcode.com/docs/markdown/threads)。

- **Pin**:置顶到侧边栏顶部。
- **Color**:给线程着色并染标题栏。
- **Labels**:`label:<name>` 搜索;点击 label 可查看同标签线程。([news/thread-labels](https://ampcode.com/news/thread-labels))
- **Rename**。
- **Snooze**:隐藏线程并静音通知。详见下方。
- **Archive**:URL 仍可用,agent 仍可引用;可取消归档,**但项目被删后线程永久归档**。
- 归档不连带子线程;**Archive N Threads** 可连父带子一起归档。
- **Delete**:永久删除;企业工作区可限制仅管理员可删。
- **Mark as Unread**。
- 侧边栏分区:Pinned 在前,然后按项目分组的活跃线程;72 小时无活动进入折叠的 **Inactive Last 72h**(表头有批量归档按钮);Snoozed 与 Archived 在底部。
- CLI:`amp threads list|search|label|rename|archive|delete|share|markdown|export|continue`。TUI:`Ctrl+C Ctrl+N` 归档当前线程并新建;`amp.thread.autoArchiveOnQuit`。
- 归档与 Execute 模式:`-ox --stream-json` 结束时自动归档,`--no-archive-after-execute` 保留。

**Snooze 细节**
- 若有机制能自动唤回线程(agent 在运行、自动化可触发、multiplayer 开启),菜单出现 **Snooze Until I'm Needed**。
- 否则是 **Snooze Until Tomorrow**,或午夜到 8 点之间显示 **Until Morning**。
- 也可以让 agent 或 Puck 设置任意 snooze,例如「snooze this until Monday at 9am」。
- Snooze 不会停止 agent 或自动化,定时运行继续并计费,只是静默。
- 等待审批的 snoozed 线程仍会出现在侧边栏并通知,回答后恢复 snooze。

**Activity 页(`/feed`)**
- 过滤维度:时间、状态、作者、项目;URL 参数驱动,例如 `/feed?time=7d&q=label:bug`,可书签/分享。
- 个人默认:右键/长按选项 → **Use … as Default**,保存在当前浏览器。
- 时间窗口取值:`24h`、`72h`、`7d`、`all`。
- 搜索语法过滤器(prompting 页):`id:`、`label:`、`parent:`、`project:`、`repo:`、`author:`、`visibility:`、`archived:`、`snoozed:`、`pinned:`、`commit:`、`pr:`、`after:`、`before:`、`updated_after/before`、Puck 专用 `puck:true`。
- `parent:` 只返回直接子线程。
- 时间过滤接受 ISO 日期、相对天数、相对周数。
- 取子集:`visibility:private|workspace|group|unlisted`。

### 2.6 线程引用、导出与追溯

- 引用:粘贴线程 URL 或 `@T-<id>`;CLI 用 `@@` 搜索线程。
- 被引用线程由 **Read Thread 子 agent** 读取并抽取相关内容;Amp 能读任意大小的线程([news/read-bigger-threads](https://ampcode.com/news/read-bigger-threads) 仅见于 sitemap,未读)。
- 导出:URL 加 `.md` 读 Markdown;More Actions → Export 支持 Markdown 和(创建者可用)完整 JSON;CLI `amp threads markdown|export T-…`。
- 提交追溯:agent 提交带 `Amp-Thread-ID: <thread-url>` 尾注;`amp.git.commit.ampThread.enabled=false` 或 `AMP_DISABLE_AMP_THREAD_TRAILER=1` 可关,关闭后该线程不会被识别为「已发货」。
- 提交共作者尾注 `Co-authored-by: Amp <amp@ampcode.com>`,由 `amp.git.commit.coauthor.enabled` 控制。

### 2.7 导入其它 agent 会话

- **未找到**专门的「导入 Claude Code / Codex 会话」功能。
- 能找到的相关能力:
  - skills 兼容 `.claude/skills/`、`~/.claude/skills/`、`~/.claude/plugins/cache/`([skills](https://ampcode.com/docs/markdown/customize/skills))。
  - Claude Code 模式:通过 Claude Agent SDK 在 Amp 服务器端运行,使用你的 Claude 订阅(`--mode external-agent:claude-code`),这不是会话导入([the-dial](https://ampcode.com/docs/markdown/the-dial))。
  - 旧版有 `AGENT.md`→`AGENTS.md` 迁移(news 列表)。
- `streaming-json` 与 Claude Code 格式兼容(`--stream-json-thinking` 不兼容)([streaming-json](https://ampcode.com/docs/markdown/cli/streaming-json))。

### 2.8 其它线程特性

- 线程模式固定:首条消息发出后不能再改 mode。理由:跨模型续写效果差,且各 mode 的 system prompt/工具不同,切换会使 prompt cache 失效([the-dial](https://ampcode.com/docs/markdown/the-dial))。
- Handoff 已于 2026-05 随 Neo 被移除,换成自动压缩(上下文约 90% 满时自动 compaction)(news/neo)。2026-01 另有「Ask to Handoff」(news 列表)。
- Fork 命令已于 2026-01 删除(news 列表标题:Stick a Fork in It)。
- Thread Map 仅 CLI 的 `threads: map`,展示由 mention/handoff/fork 连成的线程图([news/thread-map](https://ampcode.com/news/thread-map))。
- 子线程:`parent:` 过滤、Archive N Threads、线程由 agent 创建时进入侧边栏顶层。
- 线程宽度:Settings → Appearance → Thread Width = Wide/Full;命令面板也有 Set Width。
- 线程「ActionBar」中还有:Copy Thread URL、orb 状态、Cost & Usage、Report Bug、Artifacts & Attachments(图见 threads 页)。

---

## 3. Spaces

来源:[spaces](https://ampcode.com/docs/markdown/spaces)、[news/spaces](https://ampcode.com/news/spaces)。

**定义**
- 「A Space is where your team plans and directs work, usually in one Amp project.」
- 与 agent 的**一个对话**,该 agent 的工作是创建并协调线程。
- 线程以**实时卡片**显示在聊天中(状态与是否需要人介入)。

**数据模型(从文档字段推出)**
- Space 字段:name、goal(可选)、project(可选,可以是 **No Project**)、shared with(`workspace` / 特定 group / `only-me`)、Slack Channels(可选)。
- 归属于当前工作区(无项目时「belongs to your current workspace」)。
- owner:Space「acts as its owner: it uses their models and connections」。
- 成员 / follower:共享的人加入聊天;所有权转让(**Transfer Ownership**)对方接受后才生效,原 owner 变为 follower。
- 线程:Space 启动的线程是启动者本人的线程(跑在启动者账号、连接与额度上),但与 Space 共享并开启 multiplayer。
- 评论:消息可被评论,**Amp 在有人 `@amp` 之前不读评论**。

**创建**
- Web:侧栏 Spaces 区的 **New Space**,填 Name/Goal/Project/Shared with/Slack Channels,点 Create Space。
- CLI:`amp spaces create "Checkout v2" --project acme/web --goal "..." --shared-with workspace|only-me [--json]`。
- 在 Space 内发起线程:`amp -ox --project acme/web --space <url|id> "..."`;`amp threads continue <thread> -x --space ...` 把已有线程加入 Space。
- 项目可由 owner 在 Edit Space 中更换;之后启动的线程用新项目,已有线程不变。

**发帖与交互**
- 在聊天里发请求、bug 或想法,Amp 回答或为每块工作启动线程并发出卡片。
- 点击卡片可在聊天旁打开该线程。
- 把外部线程加入 Space:提供链接或 ID,线程必须是你的,且共享范围不小于 Space;已在其它 Space 的线程会移动。Space 也可以把线程移出,线程回到你个人名下。
- Slack:把公共频道链接到 Space(只有共享给整个工作区的 Space 才能关联,且只能公共频道、Amp 已加入的频道);在这些频道里 `@Amp` 的请求去到 Space。Space 对 Slack 请求仍在 Slack 线程里回答。
- Huddle:每个 Space 都有 Huddle(语音/视频/共享屏幕通话)([collaborate/huddle](https://ampcode.com/docs/markdown/collaborate/huddle))。

**可见性与权限**
- 「Shared with」选项:workspace、groups、**Only You**。
- 共享给工作区或 groups 的 Space **自动开启 multiplayer**,「Only You」则不开。
- 共享 Space 的限制:
  - 只能在 orb 或**工作区共享 runner** 上启动线程,不能用个人 runner。
  - 只能给「所有 Space 成员都可见且已开 multiplayer」的线程发消息或收取报告。
  - 只能加入共享范围不小于 Space 的线程。
- Space 没有 executor,所以不加载插件,也没有 `plugins: reload`。

**与 workspace / project / group 的关系**
- Space ↔ workspace:Space 属于工作区(没有项目时同样属于当前工作区)。
- Space ↔ project:通常对应一个项目,也可以无项目(此时线程在无仓库的 orb 里跑;提到具体项目时,会在被点名的项目里启动)。
- Space ↔ group:Enterprise 工作区的 groups 可作为共享对象;groups 本身的限制见第 4 节。
- 项目级「Space instructions」只对该项目的 Space 生效;无项目的 Space 用工作区的 Puck instructions。

**模型与计费**
- Space 的 agent 跑在 **owner 的账号**上,用 owner 的 Model Routing 与额度,无论谁在发帖;模型在 Settings → Mode Dial → **Spaces** 设置。
- 它启动的线程跑在**发起者**账号上,mode 由请求指定(「use high」「start it on Opus 5.5」),否则用工作区默认 mode 或 `medium`;支持 Fast。

**进度图**
- Goal 下方有近 30 天图表。
- 线程分三类:
  - **Shipped**:commit 已进默认分支 / PR 已合并 / 被 Ship 归档。
  - **Archived**:未发货就归档。
  - **Open**。
- 通过 `Amp-Thread-ID` 尾注找提交。能看见合并到默认分支的只有:Amp-hosted 仓库、装了 Amp GitHub App 的 GitHub 仓库、装了 Push Updates 的 GitLab 项目。
- Bitbucket、自托管 Forgejo/Gitea 等无法判断,图表改显示 **Done**。

**公告补充**
- Spaces 于 2026-11-06 发布,团队自己的内部使用场景:协作开发新功能(「Spaces v1」)、发布与支持(Service Accounts 上线)、定期安全审查(每日、拆 16 个区域、每个区域一个线程、另有线程复核,并维护 summary artifact)([news/spaces](https://ampcode.com/news/spaces))。
- 注意同名混淆:2026-08-31 的 [news/space](https://ampcode.com/news/space)(「Space to Talk」)讲的是线程内的语音视频通话,后来改名为 **Huddle**,与 2026-11 的 Spaces 不是一回事。

**未找到**
- 「private / unlisted / workspace / group」四种可见性是**线程**的可见性(见第 4 节)。Space 自身的可见性只有「Shared with: workspace / groups / Only You」。

---

## 4. 多人协作

### 4.1 Workspace 基础

来源:[collaborate/workspaces](https://ampcode.com/docs/markdown/collaborate/workspaces)。

- 一个用户**最多属于一个**工作区。
- 创建:Workspace Settings → Create a Workspace,需要 display name 和 short name,URL 为 `ampcode.com/workspaces/<short-name>`。创建时会问是否分享已有线程。
- 角色:Member、Admin(可看成员私有线程);Enterprise 另有 Billing Manager、Auditor、Group Manager、Usage Viewer 等。
- 加入方式:邮件邀请、带有效期和次数上限的邀请链接、SSO 自动加入。
- 个人/工作区「两层」配置规则:
  - 「add」类资源(plugins、skills、agents、secrets、MCP、模型 key、domains)两层合并,同名时个人优先。
  - 远程 MCP 同 ID 时:项目 > 工作区 > 个人。
  - 策略类设置工作区覆盖个人。
- 工作区设置分区:Workspace、Members、Groups、Integrations & MCP、Billing、Usage、Security、Instructions、Model Routing、Secrets & Env Vars、Plugins/Skills/Prompts/Agents、Service Accounts、Projects、Domains。

### 4.2 线程分享

来源:[threads#sharing](https://ampcode.com/docs/markdown/threads)。

**入口与对话框**
- 点击 **Share**,或命令面板 **Share Thread...**。
- 对话框内容:复制线程 URL、**General Access**、**People with Access**。
- CLI:`Ctrl+O` → `set visibility`,或 `amp threads share T-… --visibility unlisted`。

**可见性表**

| 可见性 | 谁能打开 |
|---|---|
| Private | 本人 + 通过「审计访问」的工作区管理员 |
| Workspace | 工作区全体成员 |
| Group | 所选 group 的成员 + 审计访问的管理员(仅 Enterprise) |
| Unlisted | 拿到链接的任何人(无需登录) |

**默认值**
- 无工作区:新线程默认 private。
- 有工作区:工作区所有项目里的线程默认分享给工作区;个人项目或无项目的线程默认 private。管理员可改默认、关闭对外分享、设置 groups(见 [workspace-thread-visibility-controls](https://ampcode.com/docs/markdown/enterprise/workspace-thread-visibility-controls),备选项含 private-by-default)。
- 审计访问:管理员重新验证身份、页面显示审计提示、访问被记入审计日志;通过此方式打开的 artifact 只读。
- 2026-06-02 起移除「公开可发现」线程;原公开线程自动变为 Unlisted;个人资料页不再展示线程([news/end-of-public-threads](https://ampcode.com/news/end-of-public-threads))。

**视图/贡献者**
- 分享给整个工作区时可选 **Viewer** 或 **Contributor**。
- 「Anyone with the link」不能开 multiplayer;一旦设为链接可见,multiplayer 自动关闭。

### 4.3 Multiplayer

来源:[collaborate/multiplayer](https://ampcode.com/docs/markdown/collaborate/multiplayer)、[news/multiplayer](https://ampcode.com/news/multiplayer)、[news/pass-the-orb-to-the-left-hand-side](https://ampcode.com/news/pass-the-orb-to-the-left-hand-side)。

**开关与有效期**
- 开启:线程顶部栏的**摇杆**按钮 → 确认对话框,默认 **7 天**。
- 提前结束:摇杆菜单 **End Now**。
- 延长:剩余不足 5 天时出现 **Extend Multiplayer**(重新计 7 天)。
- 精确设置:`amp threads share multiplayer ttl <thread> --hours 3`,范围 5 分钟到 7 天。
- 到期自动结束。
- 可勾选「Don't ask me again」跳过确认。

**适用范围**
- 仅 **orb** 或**工作区共享 runner**上的线程。
- 未共享的 runner 线程不行,因为那等于把你从未共享的机器开放给同事。
- 非私有线程才可。`@` 标记会自动开启 multiplayer。
- 在 private 线程上开启 multiplayer 会把线程分享给整个工作区。
- 使用了 Profile 的线程不能开(见 [orbs/profiles](https://ampcode.com/docs/markdown/orbs/profiles))。
- 开启期间线程只能在 orb 或共享 runner 上启动新线程。

**谁能发消息**
- 工作区队友可以在你的线程里向 agent 发提示,并访问 orb 或共享 runner 的 **portals、terminal、文件浏览器**。
- 所有 agent 与 orb 费用记在**线程所有者**账下。
- 线程所有者可以回答插件对话框(含 `requireHuman: true`),multiplayer 期间人类贡献者也可回答交互式选择、确认和非 secret 输入;secret 输入仍仅所有者可答。
- 可交互选择「Viewer / Contributor」(见上)。
- 「Tagging someone automatically invites them to the thread and activates multiplayer」(注意:被标记的人必须是工作区成员)。

**multiplayer 下的安全限制**
- agent 在 multiplayer 线程里**拒绝**修改你的 Amp 账号设置/密钥等,包括对你本人。
- 对工作区的 boxes、其 secrets/env vars、Sign in with Amp clients:先弹窗询问,**只有所有者或管理员**能批准。可勾选 **Don't ask again in this thread**。
- 远程 MCP 服务器有 **Use in Multiplayer Threads**(Privacy)开关,默认开,但涉及个人数据的新服务(Slack、Gmail、Granola 等)默认关。
- Google、你的个人 Linear、你自己的 App Store Connect key 默认在 multiplayer 线程里不可用,需在 Settings → Integrations 对应卡片打开 **Use in Multiplayer Threads**。
- Slack:公共频道可读;私有频道/DM/发消息需要 **Allow Private Channels, DMs, and Sending in Multiplayer Threads**。
- 文档承认这些「guards against accidental use, not a determined contributor」。
- 官方安全提示:能贡献的人可以接触线程、orb/共享 runner、secrets、文件、终端。

**通知**
- Windows App 通知含 multiplayer 请求的 **Allow Multiplayer / Decline** 按钮,可不进入 Amp 直接处理。
- 若要求 passkey,从 Slack 或推送按钮里这类操作不可用,需要打开线程完成([cli/remote-control](https://ampcode.com/docs/markdown/cli/remote-control))。

**Passkey**
- 个人安全设置里有 **Require Passkey Authentication for Web & App Interaction**。
- 工作区管理员可强制全员要求,且仅有 passkey 认证过的管理员能再关掉([news/proof-of-human](https://ampcode.com/news/proof-of-human))。

**Cross-Client Access**
- 工作区开关(Member Settings),默认开。
- 关闭时,成员无法从 Web 向 CLI 线程发消息,也不能从 CLI 以外启动 runner 线程:命令面板隐藏 runner,`create_thread`、Puck、自动化都不能指向 runner。你自己的 orb 线程不受影响;他人线程保持只读。

### 4.4 Huddle

来源:[collaborate/huddle](https://ampcode.com/docs/markdown/collaborate/huddle)、[news/space](https://ampcode.com/news/space)。

- 每个线程和每个 Space 都有 Huddle:语音+视频+屏幕共享。
- 入口:线程右侧窗格的 **Huddle** 标签 → Start Huddle,或标题栏的耳机按钮。
- 一次只能在一个 Huddle 里;切线程会离开当前通话。
- 切换窗格或线程时通话进入角落浮窗继续。
- 不需要 multiplayer,「Anyone who can view the thread can join」;**已归档线程没有 Huddle**。
- 工作区里会弹出「Huddle is live」的 Join/Decline 提示,侧边栏 Multiplayer 区显示参与者。
- 管理员可用 **Enable Huddle** 整体关闭。
- 一个人时会播放等待音乐(可静音)。
- 底层技术未提及。`[推测]` 浏览器端 WebRTC。

### 4.5 Service Accounts 与 Groups

**Service Accounts**
- 工作区拥有的、不绑定个人用户的账号,用于 CI、机器人、GitHub 事件订阅;仅管理员可创建。
- 要 Enterprise,或有付款方式的 Team 工作区。
- 可生成 API key,设为 `AMP_API_KEY`;线程归该账号所有。
- 可连接 GitHub 账号,也可作为 Slack 公共频道请求的执行身份(见 [integrations](https://ampcode.com/docs/markdown/integrations))。
- **Members Can Drive Threads**:开启后成员可在侧栏看到其线程,并通过新线程输入区 **⋮ › Run As** 以它的身份启动;它拥有的每个新 orb 线程自动与工作区共享并开启 multiplayer。
- 关闭该开关会终止其所有线程的 multiplayer。

**Groups**(Enterprise)
- 每个成员**同时只属于一个 group**。
- 命名:3–32 位字母数字和连字符,头尾为字母或数字;同工作区内大小写不敏感去重。
- 角色:Workspace admin 与 Group Manager 管所有 group;**Group Captain** 可增删本组成员、提升他人为 Captain;普通成员可查看/离开本组。
- 线程可见性可选 **Group**;管理员可设置 group 默认线程可见性。
- 删除 group 后其成员失去仅共享给该 group 的线程访问,线程创建者和管理员保留访问。

---

## 5. Puck

来源:[puck](https://ampcode.com/docs/markdown/puck)、[puck/slack-integration](https://ampcode.com/docs/markdown/puck/slack-integration)、[news/meet-puck](https://ampcode.com/news/meet-puck)、[news/many-many-pucks](https://ampcode.com/news/many-many-pucks)、[news/talk-to-puck](https://ampcode.com/news/talk-to-puck)、[news/take-puck-with-you](https://ampcode.com/news/take-puck-with-you)。

**是什么**
- 「your personal Amp assistant that helps you manage everything you do in Amp: start agents, monitor them, send messages to them, create projects」。
- 公告称其为「meta-agent」「home base for launching and coordinating other agents」;实验性质。
- 没有自己的 orb 或 executor,所以**不加载插件,不能运行 skill 的脚本**,只能读取 skill 的指令和文件;Puck 载入个人和工作区 skills,发布后约 1 分钟生效,哪怕对话已开。
- 外观有多种样式,见 [what-the-puck](https://ampcode.com/what-the-puck)(sitemap 有,未读)。

**打开方式**
- Web `Ctrl+/`(取代侧栏线程列表);通话 `Ctrl+Shift+/`。
- TUI:`Ctrl+O` → `puck: open`,或 `Ctrl+X` 然后 `P`。
- App:iOS/Android 侧栏 Puck;Mac 可独立窗口(设置 **Open Puck → In Separate Window**,仍能读取主窗口当前页面作为上下文,可说「this thread」);Windows 任务栏 Jump List 与 `Ctrl+Alt+P`;Linux 托盘或 `amp-desktop --puck`。
- Puck 的对话:可开多个独立对话(`+` 新建,点标题切换),**每个对话有自己的 Puck**(颜色不同,见公告),72 小时不用进入 Inactive;可 Archive;Slack 发起的对话不在列表里。
- 线程搜索 `puck:true` 匹配「与 Puck 的对话」,**不**匹配 Puck 创建的线程。

**如何启动并协调多个 agent**
- 请求示例:
  - 「Start an agent in ampcode/amp to investigate why CI is failing.」
  - 「Start one agent for each script in this repository, then summarize what they found.」
  - 「Once this thread is done, archive it.」
- 能力:启动并 steer agent、查找管理线程(含归档)、发送保存的 prompt、管理项目(新建、改仓库 URL/别名、base branch、Ship Behavior、commit author、orb 大小、setup 脚本、删除快照,**不能删项目**)、协调并行工作并回收各自报告、使用已连接服务(GitHub 检查、Slack 搜索)。
- 创建 runner 线程时 Puck 可传 `executor`、`runner_id`、`worktree`。
- 使用你的权限:只能改你自己有权限改的项目。
- Prompts:设置里的 **Prompts** 页,内置 `ship`、`update-base`、`refactor-commits`、`review`;个人 prompt 优先于同名工作区 prompt;可勾选「Show as a button in the Changes tab」。Puck 说「ship it」发送的是与该线程 Ship 按钮完全相同的文本。
- 通话:**Call Puck**,实时语音,由 `gpt-realtime-2.1` 驱动,把工作委托给「已有的 Puck agent」(当时由 GPT-5.6 Sol 驱动),语音只摘要,完整回复文字同步出现在聊天里。可在 Settings > Puck 写 **Voice Call Instructions**,仅对语音生效。
- 速度:Settings > Puck 选 Fast 或 Plaid,更贵;也可让 Puck 自己切模型或推理强度。
- Slack:`@Amp` 创建对应 Slack 线程的 Puck 对话;每个人说话的是**自己的 Puck**;长任务回复线程链接后沉默,完成/受阻/需要你时再发一条。

**agent-to-agent(用户侧)**
- 来源:[orbs/agent-to-agent](https://ampcode.com/docs/markdown/orbs/agent-to-agent)、[news/from-agent-to-agent](https://ampcode.com/news/from-agent-to-agent)。
- agent 可以:启动其它线程(orb、本地、runner、其它项目),发送指令,交换文件,把结果带回当前线程。
- 新线程「gets its own conversation, context window, working copy, and orb」,**不自动共享未提交文件**,需明确要求传文件;消息本身不传文件或提交。
- 典型模式:side quest、fan-out(「Run four low-mode threads in parallel」)、续做旧工作并拉取旧线程里的文件、跨机器/跨项目委托。
- 建议:需要持续协调或编辑同一批文件的任务放在当前线程;结果明确且可独立进行的任务才委托。
- agent 之间互发消息的技术协议:**未找到**。

**Puck in other apps(MCP)**
- 把 `https://ampcode.com/mcp` 加为远程 MCP 服务器。
- 客户端支持 CIMD(Client ID Metadata Documents)时自动连接。**不支持动态客户端注册**,其它客户端要在 **Advanced** 里取 client ID,且 redirect URI 必须在列表内。
- Claude:**Add to Claude** 按钮;ChatGPT:开启开发者模式后粘贴 URL。
- 三个工具:`puck`(发消息并返回回复)、`new_puck_conversation`、`orbs`(显示运行中的 orb 及 portal 链接)。
- 消息进入你最近的 Puck 对话。
- 管理员可在 Workspace Settings > Integrations & MCP 关闭 Puck MCP;工作区自有域名时会显示该域名下的服务 URL。

---

## 6. 文件

### 6.1 消息附件

来源:[orbs/files-and-attachments](https://ampcode.com/docs/markdown/orbs/files-and-attachments)、[news/attach-anything](https://ampcode.com/news/attach-anything)、[orbs](https://ampcode.com/docs/markdown/orbs)。

**上传方式**
- 输入框的附件按钮、拖放到输入框、从剪贴板粘贴。
- CLI:`--attach <path>`(可重复),上传本地文件并把附件 URL 加入消息;对 `-ox` 与 `--executor runner:<id>` 都有效。
- CLI TUI:`Ctrl+V` 粘贴图片(**不是 Cmd+V**),或拖放图片文件到终端。
- iOS 附件按钮可直接拍照/录像;macOS、iOS、Windows 的分享表单把图片发给 Puck。

**大小与类型**
- 常规上限 **100 MB**/文件,「higher upload limit」用户 **512 MB**(谁有该权限:未找到)。
- 类型:截图、视频、音频、PDF、日志、表格、数据集、压缩包等。
- 用途示例:录屏复现 bug、日志分析、PPT/表格转交互网页、转录音视频、生成视频/CAD。

**存储与访问**
- 附件「remain private to the thread」,不自动写入仓库,走 Amp 的访问检查。
- 它们是线程里存储的**副本**,与 orb 文件系统是分开的,删除附件不影响 orb。
- agent 可直接检视图片、PDF、音频、视频;其它类型会下载到 orb 里用本地工具处理。
- 线程菜单 **Attachments**(More Actions 里 「Artifacts & Attachments」)列出所有附件,含消息附件和 agent 上传的截图/录屏;可单个或全部删除(需有删除线程的权限)。

**徽章**
- **Public**:被 Amp 「published」的文件。
- **Temporary**:添加几小时后过期。

### 6.2 私有附件 vs 公开发布文件

- 「Files created in the orb stay in its workspace unless Amp attaches or publishes them.」
- 例:「Create a CSV … and attach it to your final reply.」「Publish the finished demo video so I can share it outside this thread.」
- 发布后的文件「safe for anyone with the URL to access」;常规附件走访问检查。
- 公开文件的 URL 形态:Amp Store 页里的图片是 `https://ampcode.com/user-content/artifacts/<64位十六进制>-file.<ext>`,公告 attach-anything 里的视频同形([store](https://ampcode.com/store)、[news/attach-anything](https://ampcode.com/news/attach-anything))。可以推断这就是「公开发布文件 / 公开 artifact URL」的格式,但文档没有正式说明。
- 项目文档也建议 PR 描述里嵌入「public artifact URLs of screenshots or videos」([projects](https://ampcode.com/docs/markdown/projects))。
- 工作区可关闭 **Allow Public Artifact Uploads** / **Allow Users to Share Threads and Portals Outside the Workspace**;关闭后已有公开链接失效(threads Artifacts 小节)。
- **`amp files get` / `amp files delete`:未找到**。我在 threads、files-and-attachments、orbs、projects、cli/settings、cli/runners、customizing、automations、profiles 等页搜索过,没有该命令。删除公开文件的唯一文档路径是 **线程菜单 → Attachments → 删除**。Web 搜索也无官方说明。需要此命令的话,得在实际安装的 CLI 上跑 `amp files --help` 核实。

### 6.3 Artifact(线程文档)

来源:[threads#artifacts](https://ampcode.com/docs/markdown/threads)、[news/artifacts](https://ampcode.com/news/artifacts)。

- 「A document attached to a thread that you, your teammates, and the agent edit together」,不是附件文件。
- 入口:线程内 **Views → Artifacts**;transcript 里的链接在 Artifacts 标签打开;**Ask Amp** 让 agent 编辑当前 artifact。
- 内容:标题、列表、清单、表格、代码、图片、交互视图。一个线程可有多个,按文件夹组织。
- agent 通过 `artifact` 工具读写 **Markdown**,所有用户可选 mode 与每个 Space 都有该工具;每次保存产生新 revision;有并发新版本时,仍匹配的编辑会合并,否则拒绝并让 agent 基于新版本重新合并。
- 其它线程的 agent(如子线程)在你能编辑时也能改;改完向 artifact 所在线程发消息说明改了什么;可复制别人的 artifact 到自己线程(新副本没有评论);只有 artifact 自己的线程能删除它。
- 分享:Share → General Access = **Same Access as Thread**(默认,跟随线程可见性)/ **Workspace Members**(只读)/ **Anyone with the Link**(无需登录)。
- 编辑权限:能打开线程且属于其工作区的人,或个人线程的所有者。
- 导出:Markdown / HTML / PDF 打印;**单个 artifact 上限 2 MB**。
- artifact 里的图片「stored with the thread」,通过工作区/公开链接访问的读者会看到破图。
- 删除线程会删其 artifacts。

### 6.4 Files 面板(浏览/编辑/上传/下载)

来源:[threads#inside-a-thread](https://ampcode.com/docs/markdown/threads)、[orbs](https://ampcode.com/docs/markdown/orbs)、[cli/runners](https://ampcode.com/docs/markdown/cli/runners)、[apps/mac](https://ampcode.com/docs/markdown/apps/mac)。

**浏览与上传**
- **Files** 浏览 orb 或 runner 文件系统。runner 上自动可用(只要连着),不需要 flag。
- 浏览 orb 文件无需先下载仓库。
- 上传三种方式:
  1. 输入框附件(给线程)。
  2. 拖文件到 **Files** 面板直接放入 orb 文件系统:落在目录上则进该目录,其它位置进仓库根;工具栏上传按钮;右键目录 **Upload Files…**。
  3. 往 **Terminal** 粘贴/拖入文件:上传到 orb 并在光标处插入绝对路径。仅当剪贴板有文件且无文字时才上传。
- Files 面板/终端上传单文件上限 **512 MB**。

**编辑/下载**
- 文件查看器存在;在 Mac App 且线程跑在本机时,文本文件的 ⋮ 菜单有 **Open in Editor**(用系统该类型的默认编辑器)和 **Show in Finder**(可拖入 Slack 等,无需下载副本)。
- 对「面板内编辑文件」的内置编辑器与「下载」按钮:文档**未明确提及**。`[推测]` 面板支持读取与查看,不一定有在线编辑。
- 用户侧「Files」之外,还有 `amp boxes cp` / `amp sync` 的命令行路径。

**其它文件相关**
- `amp sync <thread>` 镜像 orb 修改到本地;`amp sync --apply` 一次性复制;`--checkout` / `--skip-checkout`。
- 只读 Git 凭据:Amp-hosted 项目设置里创建(7/30/90/180 天或 Never),URL 形如 `git clone https://amp:<credential>@ampcode.com/@owner/project-name`,仅读、不能 push,只显示一次。
- 项目页有仓库文件浏览器。

### 6.5 图片/视频

- 图片生成/编辑:**Painter** 工具(GPT Image 2.5 Sunburst),可 @ 提及最多 3 张参考图,支持透明 PNG;设计 UI 时 Amp 构建交互原型而非图片([tools](https://ampcode.com/docs/markdown/tools))。
- 分析 PDF/图片:`look_at` 工具(2025-12 公告,未读)。
- 插件工具可返回图片块,大图先 `amp.attachments.upload(...)` 得到 URL;图片校验上限 4.9 MB(解码后)、单边 8000px。
- agent 的屏幕录像:Desktop 里让 Amp 操作网页时会录制并带截图汇报。
- CLI 的 `--stream-json-input` 支持 base64 图片(jpeg/png/gif/webp)。
- 视频附件:能作为附件上传并由 Amp 直接检视,没有更多细节。

---

## 7. Projects

来源:[projects](https://ampcode.com/docs/markdown/projects)、[feedback-widget](https://ampcode.com/docs/markdown/feedback-widget)、[orbs/shipping](https://ampcode.com/docs/markdown/orbs/shipping)、[store](https://ampcode.com/store)、[news/close-the-loop](https://ampcode.com/news/close-the-loop)。

**定义**
- 一个 project 保存一个代码库的全部:主仓库、相关线程、该代码所有 orb 共享的设置。
- 每个线程至多属于一个项目;「No Project」时是干净的 orb,无仓库。

**四种仓库来源**
- **GitHub**:需要连接 GitHub(私有),按仓库的稳定 GitHub ID 跟踪,仓库改名/转移后项目自动跟随。
- **GitLab**:gitlab.com 或自托管,可粘贴 HTTPS clone URL。
- **Bitbucket 或其它 HTTPS Git**:粘贴 URL,私有仓库需在 Secrets & Env Vars 配凭据。
- **Amp-hosted**:选 **Start From Scratch**,Amp 创建并托管空仓库;`amp clone owner/project-name`(owner 是用户名或工作区名)。

**创建**
- Web:Projects → New Project。
- CLI:`amp projects create acme/widgets`、`amp projects create https://git.example.com/acme/widgets`、`amp projects create --amp-hosted --name widgets`。
- 已有本地仓库迁移:`cd myproject && amp projects create --push`(创建项目、添加 `origin`、推送当前分支;`--remote <name>` 指定远程名)。
- 归属:`--personal` 或 `--workspace`。

**仓库链接三种方式**
- GitHub ID 链接、Amp-hosted、普通 Git URL。
- 可转换:GitHub → 标准 Git 要在设置菜单选 **Treat as Standard Git Repository**;Amp-hosted 项目可以通过 `--repository-url <url>` 迁到外部仓库(先自己 push),`--repository-mode github|mapped` 指定链接方式。
- **不能**改回 Amp-hosted;更改仓库不迁移线程、快照或 Git 数据。
- **Git Remote Aliases**:本地 CLI 用来把 checkout 映射到项目(镜像、SSH remote)。每次 `amp projects update` 会替换整个列表;`--clear-remote-urls` 清空。

**映射已有 vs Amp-hosted 的差别**
- 映射已有:Amp 仅克隆,不托管。
- Amp-hosted:项目设置里额外有「Public Read-Only Code Access」(任何人可 clone 读代码,但线程仍私有)、只读 Git 凭据、**Post-receive Webhook**(每次成功 push 后 POST JSON)、读写软配额(Megawatt 1 GB 存储/10 GB 传输;Gigawatt 10 GB/100 GB,见 [pricing](https://ampcode.com/docs/markdown/pricing))。

**Ship Behavior**(项目设置 Overview)
- **Ship**(默认,trunk-based):commit 未提交内容 → fetch 并 rebase 到 `origin/main` → 跑完整测试 → push 到 base branch → 若 push 被拒则重新 rebase 并 push。
- **Push to Branch**:commit 并 push 当前分支(在 base 分支上则先建短名分支);GitHub 上会给 PR 链接;按钮文案改为 **Push**。
- **Custom Ship**:你写的 prompt(最多 10,000 字符,不能为空,无模板变量),CLI `--ship-behavior custom --custom-ship-prompt-file ./ship.md`(`-` 读 stdin);提示词文本被原样发给 agent;切回 Ship 后自定义文本仍保留。
- Ship 完成后线程保持开放,出现 **Archive** 按钮。
- **Restack**:把大 diff 或中间提交重整成干净提交,不 push;在线程或 runner 线程的 Changes 窗格点 Restack ([news/restack-your-changes](https://ampcode.com/news/restack-your-changes) 仅见标题)。
- 默认值可在 Settings → Projects → Ship Behavior 为所有新项目设置。
- 插件可介入:`agent.start` 的 `event.changesWorkflow` 为 `'ship'` / `'push_branch'`;`changes.prompt` 事件可向 Ship 提示词追加文本,用户在 Ship 对话框里可见并可编辑(plugins 页)。

**Additional repositories**
- 其它与主仓库并排克隆到 `../repos/` 的仓库;上限一次更新 **20 个**(`--additional-repository <https-url>` 重复传入,`--clear-additional-repositories` 清空);agent 在线程中克隆到 `../repos/` 的仓库也会出现在 Changes 里([news/multi-repo-projects](https://ampcode.com/news/multi-repo-projects) 仅见标题)。

**项目设置其它项**
- Overview:Name、Icon、Description、Base Branch(`--base-branch`/`--clear-base-branch`)、Commit Author(个人项目可选;工作区管理员可强制 **Amp** / **Amp Account** / **User Choice**)、Change Owner(私有→工作区,不可反向)、Delete Project(线程保留为只读且永不能取消归档;`amp projects delete` 总是这么做)。
- Orb:Default Orb、Pre-clone/Pre-setup Script、快照管理(`amp projects snapshots list|delete`)、Desktop resume script(`--desktop-resume-script-file`)。
- Secrets & Env Vars(项目值覆盖工作区值,个人值覆盖二者)。
- Webhooks(仅 Amp-hosted)、Feedback Widget(仅工作区项目)。
- 权限:工作区每个成员都可在工作区项目里启动线程;改设置需要项目创建者或工作区管理员;私有项目仅所有者;公开只读代码访问者不能开线程、也看不到线程。
- Puck/agent 通过 `manage_amp` 工具(topic `projects`,operation `update` / `delete-snapshots`)改项目设置。

**Feedback widget**
- 流程:
  1. 项目设置 Feedback Widget 标签 → Enable。Amp 为项目创建一个 Sign in with Amp 应用。
  2. 拿到 `<script src="https://ampcode.com/w/client_01J….js" async>`,加到网站 `<head>`。
  3. 在 **Allowed Origins** 里添加站点来源。
- 来源规则:必须 `https://`(本地开发允许 `http://localhost`、`127.0.0.1`、`[::1]` 任意端口);支持通配符如 `https://*.previews.example.com`,但有严格限制:只匹配**恰好一个标签**,至少三段,不能是公共后缀或 `onamp.dev`/自定义 Portal 域。
- 使用:打开 `https://site/?amp`,点 widget 按钮 → 点页面元素写评论 → **Send** → 首次 **Continue with Amp** → **Start in an orb**,新建标题为 `Website: <host>` 的线程。线程**归提交者本人**,agent 在 orb 里的副本上工作,不碰线上站点。
- 显隐:`?amp` 显示;`Cmd+.`/`Ctrl+.`、右键、触屏三连击切换。
- 权限:启用/改来源/关闭 = 项目创建者或工作区管理员;发送评论 = 任何能在项目里开线程的人(工作区项目 = 所有成员)。
- 限制:仅工作区项目;每次发送新开线程,不能续发到已有线程;来源只能手动添加;被他站 iframe 嵌入时面板不打开。
- 安全:站点被攻陷会向评论注入隐藏指令,agent 能使用项目的仓库和密钥;「Turn Off」会删除该应用,所有登录失效,Allowed Origins 保留,重新 Enable 会生成**新的 client ID**。
- CLI:`amp projects feedback-widget get|enable|remove-origin|disable --yes`。
- Boxes 的 app 用 `amp boxes deploy --widget` 即可,无需 script tag。
- 与 Portal 的区别:在 Portal 页面上 Portal 自带的 review widget 取代 feedback widget,评论发往 Portal 对应的线程。

**Gallery / Amp Store**
- `https://ampcode.com/store`:「Get public projects built by the Amp community from the Amp Store.」目前列有 mixmath、mixpark、mix-spellray、orbin-slide-show 四个,作者均为 `amp`(@amp)。每项有名称、作者、描述、3–6 张画廊媒体和 **Get** 按钮(未登录会跳到 `/auth/sign-in?returnTo=/store?focus=<project-uuid>`)。
- 项目页路径形如 `/@amp/mixmath`。
- 「Get」之后的行为(例如 fork 成自己的项目)、发布条件、审核流程:**未找到**。`[推测]` 大致是把公开项目复制为自己的 Amp-hosted project(因为「made to be remade」),但文档未写。
- 另一个相近的「展示」入口是 Boxes 的 app(每个 app 一个 URL),以及导航栏里的「Previews in the Rail」:app 页面里放 `<meta name="amp-preview" content="v1; enabled; iframe">`,悬停导航栏图标时显示 384x240 的 iframe 预览([boxes](https://ampcode.com/docs/markdown/boxes))。

---

## 8. 线程 UI 面板

来源:[threads#inside-a-thread](https://ampcode.com/docs/markdown/threads)、[news/less-noise](https://ampcode.com/news/less-noise)、[news/diffs](https://ampcode.com/news/diffs)、[news/intelligently-ordered-diffs](https://ampcode.com/news/intelligently-ordered-diffs)、[orbs/shipping](https://ampcode.com/docs/markdown/orbs/shipping)。

**标题栏**
- 线程标题、**More Actions** 菜单(⋯)、**Share**、项目、agent 模式。
- 外加摇杆(multiplayer)与耳机(Huddle)按钮。

**右侧窗格**(orb 或 runner 线程)

| 窗格 | 内容 |
|---|---|
| **Changes** | 已提交与未提交改动;按钮 **Ship** / **Review** / **Sync** / **Restack**;底部 commit 选择器 |
| **Portals** | orb(或 runner)里运行的 web 服务 |
| **Files** | 浏览 orb/runner 文件系统,拖放上传 |
| **Terminal** | 在执行环境里开 shell;粘贴/拖入文件会上传并插入路径 |
| **Desktop** | orb 的 Linux 桌面;runner 在 macOS/Linux 上也可有 |
| **Huddle** | 所有线程都有 |
| **Views → Artifacts** | 线程文档;Views 也是行内「visualize」产物所在 |

**对话区展示内容**
- 线程是「your prompts, the agent's replies, every tool call it made, and the files it changed」。
- **折叠**:2026-09 起 Amp 更激进地折叠步骤。十行命令/读取/搜索/编辑合并成一个可展开的摘要,可手动展开([news/less-noise](https://ampcode.com/news/less-noise))。官方立场:「If you have the patience to watch your agents work step-by-step, you're giving them too short a leash.」
- TUI 里 `amp.terminal.detailsExpandedByDefault`(默认 false)控制思考和工具调用详情是否默认展开;`Alt+T` 本会话切换。
- 插件工具可设置 `transcriptGroup: { active, complete }`(例如 'Using kitchen sink' / 'Used kitchen sink'),用于折叠组的显示文案。
- 流式 JSON 中 `thinking` / `redacted_thinking` 默认不输出,需 `--stream-json-thinking`。
- 其它在对话里的内容:行内图表/时间线/示意图/可点击原型(「Visualize This」,2026-10-29)、可嵌入对话的终端(Amp 可「embed the terminal live in its reply」)、Slack 风格的富内容(仅在 Slack 里)、Linear session 行(线程顶部可展开看起始指令)、Mermaid 图可点击跳到代码(2025-12)。
- 把线程放到更宽:Settings → Appearance → Thread Width(Wide/Full)。

**Changes 窗格**
- 智能文件排序:Amp 读 diff,把最能解释变化的文件放顶部,测试/fixtures/生成代码灰显;文件序按钮可切换字母序;diff 在 agent 工作中变化时,按钮上出现蓝点,点击 **Apply Updated File Order**;超过 500 个文件或线程对你只读时回退到字母序。
- 提交选择器:**All Changes**(自 merge base 起)/ **Uncommitted** / 逐个提交(按 short SHA 和标题,最新在前,悬停或按右箭头查看完整提交信息)/ **Compare With Branch…**。按钮文案类似 `All Changes · 3 commits ahead of origin/main`。无需 push 或开 PR。
- Diff 算法带「duplicate block detection」,比 GitHub 更清楚地显示只是移除了某个分支([news/diffs](https://ampcode.com/news/diffs))。
- 能在 Web 与移动端直接 review,可对具体片段请求修改、交互式 stage(2026-06)。
- 按钮:**Ship**(项目 Ship Behavior)、**Review**(逐步审阅 diff)、**Sync**(拉取到本机)。Ship / Review 以及 Amp 自己写的 prompt 一律排队。
- Ship 对话框展示最终 prompt(含插件追加内容),按住 Ship 可直接按原样发送。

**撰写区**
- `Enter` 发送,`Shift+Enter` 换行;可改为 `Cmd/Ctrl+Enter` 发送(Settings → Composer & Dictation)。
- 输入区上方有建议回复(图片 alt 提到「suggested replies above the composer」)。
- `@` 提及工作区成员(会开启 multiplayer)。
- 新线程输入区显示:执行位置(New Orb / runner / 共享 runner)、项目、mode、Fast 切换、Profile(Chrome 配置,实验功能)、Run As(service account)、runner 路径选择器及 New Worktree。
- 键盘:`Ctrl+G Z` 休眠;`Ctrl+/` Puck;命令面板操作。

**Terminal 细节**
- orb 的终端运行在**共享 tmux 会话**里,Amp 可检查其输出并处理长时间运行的命令([orbs](https://ampcode.com/docs/markdown/orbs))。
- 终端与 agent 共用文件系统与工作副本。

**Desktop 细节**
- orb 的 Desktop:高分辨率可交互 Linux 桌面;Amp 知道如何使用;让它操作网站时驱动同一个 Chrome 窗口,需要你登录或过验证码时会请你接管。
- 内置应用安装:`sudo -n /opt/amp-desktop/install-app <name>`,支持 chrome、firefox、code、blender、terminal(Foot)。
- 启动脚本:`.agents/desktop/resume`(优先于项目设置里的 Desktop resume script)。
- 无法在 orb 里跑 Android 模拟器(没有硬件虚拟化),需用有模拟器或真机的 runner。
- Settings 里有「Fun Games in Desktop」实验开关。

**Portal 标签**
- 见 [orbs/portals](https://ampcode.com/docs/markdown/orbs/portals):URL 默认 `t-<thread>-p<port>.onamp.dev`;可设友好主机名 `<name>--<namespace>.onamp.dev` 或自有域名;可「Make Public For」1h/3h/1d/7d;可用 `X-Amp-Authenticated`、`X-Amp-User-*` 头在应用里区分访问者;内置 review widget(可选元素并向线程发评论,服务端 `review: false` 或头 `x-amp-review-widget: off` 可关)。
- runner 上的 Portal:只你自己可见,共享 runner 后工作区成员也可开,**不能设为公开,也没有 review 按钮**。

---

## 9. 插件系统 / skills / 模式 / MCP / permissions

### 9.1 插件(Plugins)

来源:[customize/plugins](https://ampcode.com/docs/markdown/customize/plugins)、[plugin-api](https://ampcode.com/docs/markdown/plugin-api)。

**形态**
- TypeScript/JavaScript 模块,单文件或目录(`<name>/index.ts|js`,`index.ts` 优先),默认导出 `function (amp: PluginAPI)`,类型来自 `@ampcode/plugin`。
- 「Plugins run code in your environment, so only load plugins you trust.」

**加载位置与优先级**
- Personal(Personal Settings,托管 Git 仓库)、Workspace(管理员)、Project(`.amp/plugins/`)、System(`$XDG_CONFIG_HOME/amp/plugins/`、`~/.config/amp/plugins/`、Windows `%USERPROFILE%\.config\amp\plugins\`)。
- 同名时优先级:project > system > personal > workspace。
- 安装:`amp plugins add <url>`(默认系统插件;`--target workspace` 是当前项目的 `.amp/plugins/`,**不是**托管的 Workspace Plugins 仓库);`amp plugins list|repositories|import|update|show-docs|show-agent-options`;`amp clone user-plugins|workspace-plugins`。

**托管仓库限制**
- 每仓库最多 50 个插件。
- 目录插件最多 500 个文件。
- 单文件 10 MiB,单插件总计 25 MiB,仓库总计 25 MiB。
- 超限则**不加载**,即使 push 成功也不会提示。
- 可要求签名提交。

**API 能力**
- **事件**:`session.start`(无 `session.end`)、`agent.start`、`tool.call`、`tool.result`、`agent.end`、`changes.prompt`。
  - `tool.call` 可返回 `allow` / `reject-and-continue` / `modify` / `synthesize`。
  - `agent.end` 返回 `continue` 追加用户消息,最多连续 5 次(可用 `maxContinuations` 提高)。
- `amp.registerTool`(含 `transcriptGroup`)、`amp.registerCommand`(命令面板,可设 availability: enabled/disabled/hidden)、`amp.registerSkill`、`amp.registerLinkPattern`(在 transcript 里把正则匹配文本变成链接)。
- 建议:自定义 Agent 模式与子 agent 通过 `amp.createAgent(...)` 定义(见 models-and-subagents)。
- UI:`ctx.ui.notify/confirm/input/select`。
  - `confirm` 可带 fields:文本、文件、图片预览、attachment 卡片、链接按钮、复选框。
  - `requireHuman: true` 要求人类应答。
- 直接调用工具:`ctx.tools.call(name, input)`,5 分钟超时,不进 transcript,不启动 agent 回合。
- AI 辅助:`amp.ai.ask`(线程级是否判断)、`amp.ai.decide`(带校准概率的快速类型化决策,见 decision-models 页)。
- 线程:`ctx.thread.addLabels/setLabels/append/appendUserMessage`,`amp.threads.get(id)`。
- 配置:`amp.configuration.get/update/subscribe`。
- Shell:`amp.$`;`amp.system.{ampURL,user,executor,cacheDirectory,open}`;`amp.attachments.upload`;`amp.logger`;`amp.onDispose`。
- **`amp.system.executor.keepAlive()`**:让当前 orb 保持唤醒,返回 Subscription,取消订阅或插件进程退出后释放;租约自动续期并消耗 orb 运行时间;不在 Amp 管理的 orb 内会 reject;尽力而为(手动暂停、额度不足、provider 运行时限制仍会暂停)。
- **Webhook**:`amp.createWebhook({ key, headers, handler })`,只在 Amp 托管 orb 里的插件可用;至少一次投递,处理器 30 秒超时,每端点突发 10 条、每分钟补 10 条,队列满 100 条返回 429,正文最大 1 MB,支持 `Idempotency-Key`;URL 视同密码。

**Web 与 TUI**
- 插件 UI 在 Web 与 TUI 镜像显示([news/plugins-everywhere](https://ampcode.com/news/plugins-everywhere))。
- 执行模式需 `--plugin-ready-timeout [秒]`(裸 flag 等 10 秒,最大 300,0 关闭)才能确保 `agent.start/end` 生效。
- Puck 与 Space 不加载插件。
- 更新:新线程自动取最新,运行中线程需 `plugins: reload`。
- 内置技能「building plugins」:让 Amp 自己写插件。
- 内置插件示例:`@amp/oracle`、`@amp/librarian`、`@amp/finder`(把原本内置的子 agent 做成可选插件,可自选模型)。

### 9.2 Skills

来源:[customize/skills](https://ampcode.com/docs/markdown/customize/skills)、[global-plugins-and-skills](https://ampcode.com/docs/markdown/customize/global-plugins-and-skills)。

- 目录 + `SKILL.md`(YAML frontmatter:`name`、`description`、可选 `mcpServers`),`name` 必须与目录名一致;模型只看到 name/description,调用时才加载正文。用户**无需斜杠命令**,直接在提示里点名即可;Neo 起移除了「用户可调用技能」与技能管理子命令。
- 查找顺序(第一个同名者胜出):
  1. `~/.config/agents/skills/`
  2. `~/.agents/skills/`
  3. `~/.config/amp/skills/`
  4. 项目 `.agents/skills/`(含向上查找的父目录)
  5. `.claude/skills/`
  6. `~/.claude/skills/`
  7. `~/.claude/plugins/cache/`
  8. `amp.skills.path`
  9. 个人技能仓库
  10. 工作区技能仓库
  11. Amp 官方技能
- 本地目录最多搜索到 5 层深。
- 开关:`amp.skills.disableClaudeCodeSkills`、`amp.skills.disableGlobalAgentsSkills`、`amp.skills.path`。
- 托管仓库限制:每仓库 200 个技能(超出按字母序取前 200,push 仍成功)、单技能 200 个文件、单文件 10 MiB、总 25 MiB、仓库总 25 MiB;仅文本文件。
- 技能自带 MCP:`mcp.json` 或 frontmatter `mcpServers`(后者优先),`includeTools` 过滤;技能加载前这些工具对模型隐藏;`AMP_SKILL_DIR` 环境变量。
- 管理:`amp skill add <source> [--global]`、`amp skills list [--json]`、`amp skills repositories`、`amp clone user-skills|workspace-skills`、`amp skill import|update`、工具 `reload_skills`。
- 分享:Personal Settings → Skills → Share → 复制 URL(Slack 里会展开卡片),同事可粘贴 URL 导入。

### 9.3 模式(The Dial)

来源:[the-dial](https://ampcode.com/docs/markdown/the-dial)、[models-and-subagents](https://ampcode.com/docs/markdown/models-and-subagents)。

| mode | 用途 |
|---|---|
| `low` | 小而明确的任务 |
| `medium` | 默认,混乱或多步骤 |
| `high` | 困难、出错代价高 |
| `ultra` | 开放式、路径不明,如迁移、架构 |

- 2026-07-09 起取代 smart/deep/rush/large(已弃用)。
- 模式在首条消息时固定,不能线程内切换(原因见 2.8)。
- CLI:发送首条消息前 `Ctrl+S` 旋转 Dial;`Alt+R` 切换 fast。
- 自定义:Settings → Mode Dial,可选模型预设(如 **ChatGPT Only**)、每模式的模型和推理强度、Puck 与 Spaces 的模型、以及用插件加自定义 agent 模式。
- 「Claude Code」模式:通过 Claude Agent SDK 在 Amp 服务器端运行,使用你的 Claude 订阅,按线程选择模型和 effort(`--mode external-agent:claude-code --features model:...,effort:...`)。
- 工作区可设默认 mode(**Default Mode for New Threads**)。
- 子 agent:**Task**(通用,含只读角色技能:搜索、研究、评审、规划)与 **Read Thread**,与线程用同模型同推理强度;可通过 Task 后台异步运行(`mode: "async"`,streaming-json 里有 `subagent_finished`)。
- 当前每个 mode 背后是什么模型:在 [modes](https://ampcode.com/modes) 页,**我没有读**(本次范围外)。

### 9.4 MCP

来源:[customize/mcp](https://ampcode.com/docs/markdown/customize/mcp)(只读到前 300 行)。

- 官方推荐:把 MCP 服务器**打包进 skill**(`mcp.json`),而不是放在用户设置里,避免工具列表膨胀。
- 本地配置:`amp.mcpServers`(`command`/`args`/`env` 或 `url`/`headers`,支持 `${VAR}`);CLI `amp mcp add <name> -- <cmd>` 或 `amp mcp add <name> <url>`;`--mcp-config '<json>'` 在 `-x` 里临时指定。
- 远程 MCP 定义由 ampcode.com 存储,Amp 服务器代为连接(所以服务器要允许 [Amp egress IP](https://ampcode.com/docs/markdown/egress-ips) 列表)。`amp mcp remote --personal|--workspace <ws>|--project <p>|--current-project add|update|check|tools|login|logout|remove|list`,`--auth none|bearer|oauth|workload-identity`。
- 优先级:project > workspace > personal(同 ID 时)。
- 工具策略:每个工具 **Allow / Ask First / Disabled**;Ask First 在 `code_exec` 里调用时代码等待所有者批准,子 agent 不能调用。
- 其它:`${amp:id-token}`(orb 内 OIDC),Workload Identity JWT(带 thread_visibility / thread_multiplayer / thread_non_owner_can_influence 声明,需 Amp 员工授权),MCP Apps,服务器可向用户发起提问。
- 工具列表缓存超 1 天会后台刷新。
- 企业 **MCP Registry Allowlist**(见 enterprise 页,未读)。

### 9.5 Permissions

- 默认**不询问工具权限**:这是 2026-05 Neo 的有意改变——旧的 `--dangerously-allow-all` 成为默认([news/neo](https://ampcode.com/news/neo)、[tools](https://ampcode.com/docs/markdown/tools))。
- 旧权限系统仍在,变成**内置插件**;若设置里有 `amp.permissions`、`amp.dangerouslyAllowAll: false` 或 `amp.guardedFiles.allowlist`,则加载该插件。
- 官方立场:模型会写一次性脚本,静态检测 `rm -rf` 这类字符串给人虚假的安全感;若需要策略,用 Plugin API 自建(`example-plugin-permissions`),管理员可作为全局工作区插件分发。
- 仍存在的静态控制:`amp.tools.disable`(支持 `builtin:`、通配符)、`amp.mcpPermissions`(按 `url` / `command`+`args` 匹配,首条匹配生效,`allow` / `reject`)、受管设置 `managed-settings.json`(macOS `/Library/Application Support/ampcode/managed-settings.json`、Linux `/etc/ampcode/managed-settings.json`、Windows `%ProgramData%\ampcode\managed-settings.json`)和工作区 Managed Settings(带修订历史,约 10 分钟内同步)。
- 管理员侧还有:Allowed Models、Entitlements、Minimal Data Retention 等(仅在 llms.txt 里见到页名,**未读**)。

---

## 10. 其它「高级但好用」功能清单

| 功能 | 一句话 | 来源 |
|---|---|---|
| Automations / Schedule | 线程保存「时间+提示」,到点唤醒 agent 延续上下文;一次或循环,每个线程一个计划;有指示器、可暂停/删除 | [orbs/automations](https://ampcode.com/docs/markdown/orbs/automations)、[news/schedule](https://ampcode.com/news/schedule) |
| Snooze Until I'm Needed | 静默运行自动化,只有「有新信息」时才浮上来 | threads、automations |
| Webhooks / Event-driven orbs | 插件创建公共 HTTP 端点,存储事件并唤醒 orb | automations |
| GitHub / Linear / GitLab 监听 | issue/PR/CI 变化继续线程或为每个新 issue 起 orb;在 Linear 里 assign/@ Amp 起会话 | [integrations](https://ampcode.com/docs/markdown/integrations)、news github-listeners、assign-it-to-amp(仅见标题) |
| Slack 集成 | `@Amp` 起 Puck 对话;可发 Slack 原生卡片(表格、图表、按钮);发消息默认先问你 | [puck/slack-integration](https://ampcode.com/docs/markdown/puck/slack-integration) |
| Service Accounts | 团队拥有的机器账号,API key + Run As | workspaces |
| Orb Network Routing | 让 orb 经你的网络出网,绕开封云 IP 的站点 | runners |
| 直接看/控 runner 桌面 | WebRTC 下操作 Mac/Linux runner 的桌面,调试原生 App | runners、[news/runner-desktop](https://ampcode.com/news/runner-desktop) |
| Profiles | 跨 orb 保留 Chrome 的登录态/密码/localStorage;可让 agent 自选;支持借助 Mac App 蓝牙使用手机 passkey | [orbs/profiles](https://ampcode.com/docs/markdown/orbs/profiles)(实验功能,Linux orb 才有) |
| Boxes | 常开机器部署 app,每个 app 一个 URL;私有 app、自定义域名、定时开关、导航栏预览、反馈 widget | [boxes](https://ampcode.com/docs/markdown/boxes) |
| Huddle | 线程内语音视频屏幕共享 | huddle |
| Artifacts | 线程内多人+agent 共编文档 | threads |
| Visualize / Views | 在线程里直接渲染图表、时间线、原型 | [news/visualize-this](https://ampcode.com/news/visualize-this) |
| Restack | 整理 agent 的中间提交 | shipping |
| Intelligent diff ordering | 重要文件优先 | [news/intelligently-ordered-diffs](https://ampcode.com/news/intelligently-ordered-diffs) |
| Agents Panel | 编辑器扩展里的多线程管理面板(j/k、Space、Enter、e) | [news/agents-panel](https://ampcode.com/news/agents-panel)(注意:2026-02 起编辑器扩展已被移除,见 news the-coding-agent-is-dead 标题) |
| 全局键盘/托盘/Jump List | Windows/Linux/Mac 的菜单栏/托盘入口与全局热键 | apps/* |
| Puck in Claude/ChatGPT | MCP 暴露同一个 Puck | puck |
| Model Routing | 自带 key/订阅/网关,按顺序路由;BYOK 提供商含 OpenRouter、Bedrock、Vertex、Azure Foundry、Vercel AI Gateway、Cloudflare AI Gateway、Ollama Cloud、OpenCode Go、自定义端点 | [news/free-agent](https://ampcode.com/news/free-agent) |
| 使用 ChatGPT / Claude 订阅 | 把订阅计入 Amp 模型调用 | the-dial |
| SDK(TS/Python)、Streaming JSON | 程序化调用 CLI;`--stream-json` / `--stream-json-input` | streaming-json(sdk 页未读) |
| Explain Usage | 问 Puck 钱花哪了 | news/explain-usage(仅见标题) |
| Secrets & env on runner | 通过 `--amp-env` 管理远程机器的环境变量 | runners |
| Proof-of-human / passkey | Web/App 交互、远程控制、multiplayer 批准等可强制 passkey | news/proof-of-human |
| OIDC / ID token | orb 内向外部服务证明线程身份 | news/secrets-of-the-orb(仅见标题)、mcp 页 |
| Windows 企业部署 | Intune、MSIX、ADMX、`Software\Policies\Amp` 注册表值 | apps/windows |
| 多仓库项目 / 自定义 Portal 域名 / Domains | 工作区可设 Portal 默认域 | portals、workspaces |
| 设计 UI | 从截图探索设计、迭代,agent 构建交互原型 | [designing-ui](https://ampcode.com/docs/markdown/designing-ui)(未读) |

---

## 11. 定价 / 套餐里与上述能力绑定的限制

来源:[docs/pricing](https://ampcode.com/docs/markdown/pricing)、[pricing](https://ampcode.com/pricing)、[news/free-agent](https://ampcode.com/news/free-agent)。

| 套餐 | 价格 | 包含 | orb |
|---|---|---|---|
| Hobby | 免费 | 不含用量;Amp 提供的模型按标价买 credits | Amp 托管 orb 按量付费;自托管 orb 免费不限量;runner 免费 |
| Megawatt(页面里叫 Individual) | $20/月(教育优惠 $10) | $20 的 agent 用量(此后按标价买 credits) | 750 小时(45,000 分钟)a1.small orb;更大的 orb 消耗更快 |
| Gigawatt | $200/月 | $200 agent 用量 | 1,000 小时 a1.xxlarge orb |
| Teams | 工作区本身不加价 | 成员各自的 Hobby/Megawatt/Gigawatt;**包含的 orb 时间按人计,不合并**;购买的 credits 共享工作区池 | 同成员档位,另加自托管 orb |
| Enterprise | 定制 | 仅池化 credits | 自托管 orb 按标准价,可议价 |

**和本报告主题绑定的限制**
- 各档**功能无差别**:付费档 「include … every Amp product feature」。Hobby 也是「All product features」。差别主要在 orb 与 agent 用量,以及部分团队特性。
- 使用自带订阅/自带 key(BYOK)时,除 Enterprise 外**没有 Amp 费用或限制**;自托管 orb 免费且不限量。
- 工作区 SAML/OIDC SSO、自定义 Portal 域名:团队需要**至少一名成员在付费档**。Enterprise 工作区无需付费档也可 SSO。
- Teams 共享 BYOK 与 model routing。
- Groups、按组线程可见性、SCIM、审计日志、IP 白名单、区域端点、MCP registry allowlist、分用户成本控制、受管用户设置、可配置线程保留期:**Enterprise**。
- 「Group」线程可见性:仅 Enterprise。
- Service Accounts:Enterprise,或有付款方式的 Team 工作区。
- 工作区 Boxes:Enterprise,或有付款方式的 Teams。
- Amp-hosted 仓库软限制:Megawatt 1 GB 存储/10 GB 传输,Gigawatt 10 GB/100 GB。
- 上传上限:常规 100 MB;「higher upload limit」512 MB(授予条件未找到)。
- Orb 启动节流:每用户可突发 20 个计量 orb,之后每 5 分钟起 1 个,超出则排队而非失败;可联系支持提高。
- orb 暂停规则:agent 停止工作后 **5 分钟**,或你最后一次交互后 **20 分钟**,取较晚者;暂停的 orb 不计费(pricing 页)。
- 购买的 credits **12 个月后过期**;每月包含用量**不结转**。
- Boxes 另有独立价格:`b1.tiny` $18/30 天,`b1.small` $30,`b1.medium` $50,`b1.large` $100;每个 box 月流量 100 GB;创建 box 至少需要 $2 credits;用付费档时先扣包含的 orb 用量([boxes](https://ampcode.com/docs/markdown/boxes))。
- Amp Free(每日 credits)不支付 box 费用。
- 数据保留:零/最小数据保留政策已扩展到**所有用户**,不再只限 Enterprise([news/free-agent](https://ampcode.com/news/free-agent))。
- Windows/Linux/Mac/iOS/Android App 本身不收费。
- App 上的 Profiles 为实验功能(需在 Settings → Experimental Features 开启)。

---

## 附:页面清单

**实际读过(部分只读到分页截断前)**
- 基础页:docs 首页、llms.txt、sitemap.xml(含尾部)、`/manual`(重定向到 docs 首页)、chronicle、store、pricing。
- 文档页:
  - apps:apps、apps/mac、apps/ios、apps/android、apps/windows、apps/linux。
  - 线程与协作:threads、spaces、puck、puck/slack-integration、collaborate/workspaces(读到约 300/378 行)、multiplayer、huddle。
  - 项目与 Ship:projects、feedback-widget、orbs/shipping、orbs/files-and-attachments、orbs/portals、orbs/agent-to-agent、orbs/automations、orbs/profiles、orbs/getting-started、orbs(overview)。
  - 定制:customize/plugins、plugin-api(约 1700/3215 行)、customize/skills、customize/global-plugins-and-skills、customize/mcp(约 300/463 行)。
  - CLI 与其它:cli、cli/runners、cli/remote-control、cli/spawning-orbs、cli/execute-mode、cli/streaming-json(约 300/380 行)、cli/settings、cli/keybindings、the-dial(约 300/419 行)、models-and-subagents、tools、prompting、boxes、integrations、pricing、enterprise/workspace-thread-visibility-controls。
- 新闻/公告:
  - 2026:spaces、space、agents-anywhere、amp-on-ios-and-macos、amp-on-android、steer-dont-queue、multiplayer、so-long-tui-sidebar、artifacts、less-noise、shared-runners、the-mac-app-is-your-runner、one-runner-many-worktrees、one-runner-is-now-enough、runner-desktop、runner-portals、attach-anything、agents-everywhere、meet-puck、many-many-pucks、from-agent-to-agent、end-of-public-threads、plugins-everywhere、pass-the-orb-to-the-left-hand-side、talk-to-puck、neo、desktop、diffs、visualize-this、proof-of-human、schedule、take-puck-with-you、free-agent、close-the-loop、intelligently-ordered-diffs、friendly-urls-for-sharing-orbs。
  - 2025:agents-panel、thread-labels、thread-map。
- 一次 web 搜索:关于 `amp files get/delete`,无官方答案。

**没有读到或没读完**
- 命令 `amp files get/delete`:任何官方页面都**未找到**。
- 导入其它 agent(Claude Code/Codex)会话功能:**未找到**。
- 实时同步协议、离线队列/重连机制的技术细节:**未找到**。
- Files 面板内置编辑器与下载按钮:**未明确**。
- 未读(超出本任务范围或排期之外):
  - Orbs 子页:customizing、optimizing、sizes-and-costs、handling-secrets、mac、self-hosted(+kubernetes)。
  - 集成与定制:integrations/github|gitlab|google、customize/decision-models|agents-md|model-routing、designing-ui。
  - 参考与支持:cli/clone、sdk(+typescript/python)、egress-ips、support/*。
  - Enterprise:mcp-registry-allowlist、workspace-entitlements、allowed-models、minimal-data-retention。
  - 页面续读:collaborate/workspaces 尾部 78 行、plugin-api 第 1700 行以后、customize/mcp 第 300 行以后、the-dial 第 300 行以后、cli/streaming-json 第 300 行以后。
  - 站点页:`/modes`、`/what-are-orbs`、`/what-the-puck`、`/security`、`/docs/using-amp/*`。
  - 约 100 篇其它 news(只在 chronicle 列表里见到标题),含 boxes、service-accounts、gitlab、macos-satellites、take-puck、github-listeners、assign-it-to-amp、restack-your-changes、multi-repo-projects、setup-without-a-commit、explain-usage、mcp-in-orbs、global-plugins-and-skills、portals、event-driven-orbs、secrets-of-the-orb、the-dial 等。
  - 旧公告:thread-forking、handoff、find-threads 等。

---

## 对 Oryn 的启示

1. **执行位置与查看位置彻底解耦**:线程是一等公民(`T-<uuid>` URL),executor 可为 local / orb / runner:<id>;Oryn 的数据模型第一天就该把 `thread` 与 `executor` 分开建模。
2. **一个线程多端同步 = 每个线程一个有状态的「actor」**:Amp 说「thread actor」休眠后唤醒时再加载远程工具列表(MCP 页);Oryn 也宜让线程状态由服务端 actor 持有,客户端只做订阅与发命令。
3. **客户端能力靠「标签页由执行环境声明」驱动**:Changes / Files / Terminal / Portal / Desktop 是否显示,取决于 runner 是否带 `--remote-control-terminal|portals|--desktop`,Changes 看是否 Git/Jujutsu;值得复刻这种能力协商(capability flags),而不是硬编码 UI。
4. **默认安全开关很多,逐一设计**:Terminal、Portal、Desktop 对 runner 默认关闭;共享 runner 需要显式确认并给出 OS 用户身份警告;取消共享时立刻终止 multiplayer——建议 Oryn 同样默认关闭并让撤销**立即**生效。
5. **steer 与 queue 是两个语义**:默认 steer(步骤之间插入)、显式 queue(整轮结束后)、`Esc Esc` 强制中断;Ship/Review 这类系统生成的 prompt 一律 queue。Amp 在 2026-05 与 09 间来回改过默认,Oryn 应把默认值做成可配置项。
6. **对话区默认折叠步骤**:Amp 的取向是「看结果不看过程」,但提供展开能力和 CLI 配置(`detailsExpandedByDefault`)。Oryn 要求「像 omp TUI 一样完整展示」,所以应默认展开并保留折叠开关,同时给插件工具提供 `transcriptGroup` 式的分组文案。
7. **multiplayer 有 TTL 且强制到期**(默认 7 天,5 分钟到 7 天可调,不足 5 天可延期),这比永久共享更安全;计费归线程所有者。
8. **Multiplayer 下的凭据隔离是难点**:agent 会用线程所有者的凭据(MCP、Google、Slack、Linear、App Store Connect)。Amp 的做法是每个集成一个「Use in Multiplayer Threads」开关,默认对个人数据类服务关闭,并承认只防误用不防恶意。Oryn 需要从一开始就为「谁在提示、用谁的凭据」建模,而不是事后补开关。
9. **深链接用普通 HTTPS 链接 + 账号级「总是用 App 打开」偏好**,而不是自定义 scheme;并处理 Chrome「一次点击只允许一次打开外部 App」的限制。
10. **App 分享入口统一发给 Puck**(图片/文本/文件),由 Puck 决定是开线程还是回答;这比让每个平台各做一套分享逻辑简单。
11. **Puck 本身没有机器**:不能跑脚本、不加载插件,只读 skill;这是个很干净的边界——「协调者」与「执行者」分离。Oryn 的 Space/Puck 等价物也应无 executor。
12. **Space = 一个有目标的聊天 + 实时线程卡片**:线程仍归发起者所有、用发起者的额度;Space agent 用 owner 的账号。要小心「Space 的 owner 离职/账号被禁」「Space 里线程可见范围不能比 Space 窄」这类一致性规则。
13. **Space 进度图依赖提交里的 `Amp-Thread-ID` 尾注**:没有这条链路(例如 Bitbucket)就退化为「Done」。Oryn 的「已交付」判定要有降级路径。
14. **线程可见性四档(private/workspace/group/unlisted)+ 管理员「审计访问」**是个值得复刻的折中:管理员能看私有线程,但要重新认证、显示审计提示并记日志,artifact 只读。
15. **公开发现式分享已被 Amp 删除**(2026-06),只留 Unlisted(不可猜 URL)。不要做「公开可发现」。
16. **文件要分层**:线程附件(受访问控制的副本)、orb/runner 文件系统(Files 面板)、公开发布文件(`user-content/artifacts/<hash>-file.<ext>` 之类的哈希 URL,任何人可访问)、线程 artifact(带 revision 的 Markdown 文档)。四者生命周期和权限不同,不要混在一个「附件」概念里;附件还有 Public / Temporary 徽章和 100 MB / 512 MB 两档上限。
17. **Artifact 用 Markdown + revision + 乐观合并**:agent 保存遇到新版本时,仍匹配的编辑会被应用,否则拒绝并让 agent 重新合并——比锁更适合人机共编。
18. **runner 是 Oryn 自家 orb provider 的最简形态**:`--dir/--discover-dirs`、worktree 创建、`amp-env` 注入、自更新、睡眠时 90 秒内从选择器消失——这套行为清单可直接当需求。**坑**:共享 runner 里的线程以同一 OS 用户运行,互相覆盖文件,文档明说「only share with people you'd trust with a shell」。
19. **Portal/Desktop 的传输**:Desktop 用 WebRTC(直连优先,TURN 次之,5 秒后回退到 Amp 中继);Portal 的 WebSocket 会因服务器部署被断(1006)。Oryn 要为长连接设计重连,并明确不承诺「必须常开」的隧道。
20. **runner 上的 Portal 不允许公开且没有 review 按钮**:本机端口暴露 = 暴露用户机器,默认只对自己或明确共享的工作区成员开放。
21. **Mac 睡眠是真问题**:提供 Keep Awake(仅插电)、睡眠期间线程暂停并在唤醒后继续、选择器里自动下线。Oryn 需要把「设备离线」作为线程状态展示。
22. **插件接口设计**:事件(`tool.call` 可 allow/reject/modify/synthesize)、`ctx.ui.*` 在 Web 与 TUI 镜像、`requireHuman`、`keepAlive()` 租约(带成本)、托管仓库的硬上限(50 插件、25 MiB)且超限静默不加载——Oryn 若复刻,**超限应显式报错**,这是 Amp 的坑。
23. **权限系统被降级成插件**,默认不询问;理由是静态匹配对「模型写脚本」无效。Oryn 若坚持 omp 风格的审批,需要把策略放在**执行环境隔离**(orb/沙箱)而不是命令匹配上。
24. **Mode 在首条消息固定**:避免跨模型续写差、保持 prompt cache;切换要开新线程。Oryn 的线程模型应记录创建时的 mode/model 快照。
25. **Cross-Client Access 在工作区级可关闭**,且关闭时同时隐藏 runner 创建入口(命令面板、`create_thread`、Puck、自动化);passkey 要求只在 Web/App 交互上强制,集成与推送按钮里的操作要退回到打开线程。
26. **自动化与 snooze 的组合**:`Snooze Until I'm Needed` + 定时任务,只有回复里「有新信息」才唤醒用户;「没变化」的回复被判定为噪声。这是一个可复刻的 UX 点,但需要判定逻辑。
27. **Webhook 投递语义写得很清楚**(先落盘再 200、至少一次、30 秒超时、退避到 5 分钟、1 小时丢弃、归档线程即 404);Oryn 若做事件驱动,应照这套写进 API 文档。
28. **Feedback widget 的安全模型**:站点被攻陷会向评论注入指令,而 agent 有项目密钥;Amp 的缓解是 Allowed Origins、通配符限制(单标签、至少三段、不是公共后缀)、`?amp` 才显示、只有能开线程的人才能发送。
29. **Amp Store 是「公开项目 + Get」**,Get 的语义文档没写清楚,Oryn 如做类似功能需要自己定义(fork 为自己的项目?)。
30. **文档与产品演进很快、功能删得也快**:handoff、fork、todo、themes、user-invokable skills、Amp Tab、编辑器扩展都被移除。Oryn 的规划要把「依赖某个具体 UX 机制」的部分与稳定的数据模型分开,并记住 `docs` 里的措辞(「If we don't use and love a feature, we kill it」)。
31. **坑:`amp files get/delete` 在官方文档里找不到**——如果上游调研计划依赖它,需要在装好的 CLI 上用 `amp files --help` 核实,不要按本报告假设其参数。

### Critical Files for Implementation

Oryn 目前没有代码,下面是后续设计时最值得反复查阅的官方来源:
- `https://ampcode.com/docs/markdown/cli/runners` — runner 的 flag、目录发现、worktree、共享、Desktop/Portal/Terminal 能力矩阵,是复刻 Oryn「自有 provider」的最直接蓝本。
- `https://ampcode.com/docs/markdown/threads` — 线程 UI、可见性表、整理/snooze/归档、artifact、分享与 multiplayer 的入口。
- `https://ampcode.com/docs/markdown/collaborate/multiplayer` — TTL、凭据隔离、集成在 multiplayer 里的限制。
- `https://ampcode.com/docs/markdown/spaces` — Space 的数据与权限模型、进度统计、Slack 联动。
- `https://ampcode.com/docs/markdown/plugin-api` — 插件类型参考(`PluginExecutor.keepAlive`、事件、`ctx.ui`、`createWebhook`),设计 Oryn 插件接口时的参照。
