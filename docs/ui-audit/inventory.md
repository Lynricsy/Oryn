# UI 排版审计总清单

由 `web/e2e/ui-audit/inventory.ts` 从 `areas/*.md` 生成，勿手改。状态列依次为 d1920 明/暗、d1366 明/暗、m390 明/暗：
✓ after 跑完全程断言 0 问题且 before / after 截图都在；✗ after 有问题；— 显式跳过（视口不适用，清单里注明）；· 其余（没跑、中途失败、缺截图），不算通过。

界面 327 个，全部视口通过 327 个。问题 96 条：错乱 47、设计 43、冗余 5、设计 + 错乱 1。

覆盖对账（spec 327 个用例 vs 清单 327 个 id）：一致

## fallback（4 个界面，3 条问题，详见 [areas/fallback.md](areas/fallback.md)）

| id | 界面 | 打开路径 | 形态 | 状态 |
|---|---|---|---|---|
| fallback.schedule-role-picker | 定时对话框里的「对象」选择 | 设置 → 自动化 → 创建 → 对话框里的「对象」胶囊 | 对话框内下拉（手机为底部抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| fallback.home-model-picker | 首页指定模型（隔离的托盘层叠上下文里） | 首页「对象」胶囊 → 指定模型…（仅桌面：手机走 Dialog，不经 Floating） | 下拉 | ✓ ✓ ✓ ✓ — — |
| fallback.thread-actions | 线程「更多操作」菜单（桌面）/ 标题胶囊菜单（手机） | 线程页页头「更多操作」；手机点标题胶囊 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| fallback.sink-runner-picker | 下沉卡片平移时的执行位置下拉 | 手机首页「local」胶囊；用例把 `.sink-card` 固定推开 30% 抽屉宽（仅手机） | 下拉 | — — — — ✓ ✓ |

## home（6 个界面，9 条问题，详见 [areas/home.md](areas/home.md)）

| id | 界面 | 打开路径 | 形态 | 状态 |
|---|---|---|---|---|
| home | 首页：品牌标、标题、输入托盘与四个胶囊 | 启动后默认页 `/`；侧栏「新建线程」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| home.role-picker | 对话对象选择（全局助手 / 各 role / 默认 / 指定模型…） | 首页托盘左下「对象」胶囊 | 下拉（桌面）/ 底部抽屉（手机） | ✓ ✓ ✓ ✓ ✓ ✓ |
| home.model-picker | 指定模型：模型目录 + 筛选框 | 对象胶囊 → 「指定模型…」 | 下拉（桌面）/ 底部抽屉（手机） | ✓ ✓ ✓ ✓ ✓ ✓ |
| home.runner-picker | 执行位置（新 orb 规格、Runners） | 托盘「local · medium」胶囊 | 下拉 | ✓ ✓ ✓ ✓ ✓ ✓ |
| home.profile-picker | 浏览器 Profile | 托盘「浏览器 Profile」胶囊 | 下拉 | ✓ ✓ ✓ ✓ ✓ ✓ |
| home.project-picker | 项目 | 托盘「无项目」胶囊 | 下拉 | ✓ ✓ ✓ ✓ ✓ ✓ |

## host（40 个界面，4 条问题，详见 [areas/host.md](areas/host.md)）

| id | 界面 | 打开路径 | 形态 | 状态 |
|---|---|---|---|---|
| host.chrome-mac.thread | macOS 自绘窗口栏：线程页，侧栏展开、面板关 | 桌面 App；打开任一线程 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-mac.thread-panel | macOS 自绘窗口栏：线程页，侧栏展开、面板开 | 桌面 App；线程页头「信息面板」 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-mac.thread-collapsed | macOS 自绘窗口栏：线程页，侧栏收起、面板关 | 桌面 App；侧栏顶部「收起侧边栏」 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-mac.thread-collapsed-panel | macOS 自绘窗口栏：线程页，侧栏收起、面板开 | 桌面 App；收起侧栏 + 打开信息面板 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-mac.home | macOS 自绘窗口栏：首页 | 桌面 App；启动后默认页 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-mac.settings | macOS 自绘窗口栏：设置 · 外观 | 桌面 App；侧栏「更多」→ 设置 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-mac.launcher | macOS 自绘窗口栏：窗口栏里的连接页 | 桌面 App；首次启动 / 断开服务器后 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-mac.narrow | macOS 自绘窗口栏：900 宽三栏（侧栏 256px）+ 面板 | 桌面 App；把窗口拖窄到 840–1023 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.thread | Windows 自绘窗口栏：线程页，侧栏展开、面板关 | 桌面 App；打开任一线程 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.thread-panel | Windows 自绘窗口栏：线程页，侧栏展开、面板开 | 桌面 App；线程页头「信息面板」 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.thread-collapsed | Windows 自绘窗口栏：线程页，侧栏收起、面板关 | 桌面 App；侧栏顶部「收起侧边栏」 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.thread-collapsed-panel | Windows 自绘窗口栏：线程页，侧栏收起、面板开 | 桌面 App；收起侧栏 + 打开信息面板 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.home | Windows 自绘窗口栏：首页 | 桌面 App；启动后默认页 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.settings | Windows 自绘窗口栏：设置 · 外观 | 桌面 App；侧栏「更多」→ 设置 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.launcher | Windows 自绘窗口栏：窗口栏里的连接页 | 桌面 App；首次启动 / 断开服务器后 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.narrow | Windows 自绘窗口栏：900 宽三栏（侧栏 256px）+ 面板 | 桌面 App；把窗口拖窄到 840–1023 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-linux.thread | Linux（按钮在左） 自绘窗口栏：线程页，侧栏展开、面板关 | 桌面 App；打开任一线程 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-linux.thread-panel | Linux（按钮在左） 自绘窗口栏：线程页，侧栏展开、面板开 | 桌面 App；线程页头「信息面板」 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-linux.thread-collapsed | Linux（按钮在左） 自绘窗口栏：线程页，侧栏收起、面板关 | 桌面 App；侧栏顶部「收起侧边栏」 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-linux.thread-collapsed-panel | Linux（按钮在左） 自绘窗口栏：线程页，侧栏收起、面板开 | 桌面 App；收起侧栏 + 打开信息面板 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-linux.home | Linux（按钮在左） 自绘窗口栏：首页 | 桌面 App；启动后默认页 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-linux.settings | Linux（按钮在左） 自绘窗口栏：设置 · 外观 | 桌面 App；侧栏「更多」→ 设置 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-linux.launcher | Linux（按钮在左） 自绘窗口栏：窗口栏里的连接页 | 桌面 App；首次启动 / 断开服务器后 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-linux.narrow | Linux（按钮在左） 自绘窗口栏：900 宽三栏（侧栏 256px）+ 面板 | 桌面 App；把窗口拖窄到 840–1023 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-mac.fullscreen | macOS 全屏：系统收起红绿灯，窗口栏留白收回 | 桌面 App 进入全屏 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.chrome-win.maximized-blur | Windows 最大化且窗口失焦：还原图标、按钮变淡 | 最大化后切到别的窗口 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.launcher.connect | 桌面连接页 · 连接：服务器地址输入 | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.launcher-mobile.connect | 手机连接页 · 连接：服务器地址输入 | 手机 App 首次启动 / 断开后 | 页面（仅手机） | — — — — ✓ ✓ |
| host.launcher.confirm-connect | 桌面连接页 · 连接前确认：大字显示目标主机 | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.launcher-mobile.confirm-connect | 手机连接页 · 连接前确认：大字显示目标主机 | 手机 App 首次启动 / 断开后 | 页面（仅手机） | — — — — ✓ ✓ |
| host.launcher.pairing | 桌面连接页 · 配对：配对码与倒计时 | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.launcher-mobile.pairing | 手机连接页 · 配对：配对码与倒计时 | 手机 App 首次启动 / 断开后 | 页面（仅手机） | — — — — ✓ ✓ |
| host.launcher.error | 桌面连接页 · 错误：无法连接 + 自动重连 | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.launcher-mobile.error | 手机连接页 · 错误：无法连接 + 自动重连 | 手机 App 首次启动 / 断开后 | 页面（仅手机） | — — — — ✓ ✓ |
| host.launcher.confirm-change | 桌面连接页 · 更换服务器确认（会移除本机凭据） | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） | ✓ ✓ ✓ ✓ — — |
| host.launcher-mobile.confirm-change | 手机连接页 · 更换服务器确认（会移除本机凭据） | 手机 App 首次启动 / 断开后 | 页面（仅手机） | — — — — ✓ ✓ |
| host.mobile.sink-closed | 手机壳（屏幕圆角 47）线程页，侧栏收起 | 手机 App 打开线程 | 页面（仅手机） | — — — — ✓ ✓ |
| host.mobile.sink-open | 手机壳下沉侧栏打开：卡片与屏幕圆角同心 | 线程页头「侧边栏」 | 页面（仅手机） | — — — — ✓ ✓ |
| host.mobile.corners-dialog | 手机壳贴边对话框（附件） | 标题胶囊菜单 → 附件 | 弹窗（仅手机） | — — — — ✓ ✓ |
| host.mobile.push-card | 设置 · 通知里的手机推送卡 | 侧栏「更多」→ 设置 → 通知 | 页面（仅手机） | — — — — ✓ ✓ |

## panels（28 个界面，16 条问题，详见 [areas/panels.md](areas/panels.md)）

| id | 界面 | 打开路径 | 形态 | 状态 |
|---|---|---|---|---|
| `panels.info` | 信息面板（orb 卡、CPU / 内存、上下文、待办、服务 / 环境 / 能力 / SSH 入口行） | 线程页 → 信息面板 → 信息；手机：页头视图菜单 → 信息 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.info.narrow` | 信息面板，900×860 窄桌面窗口（侧栏 + 转录 + 面板三栏）（仅桌面） | 同上，窗口拉窄到 900 宽 | 面板 | ✓ ✓ ✓ ✓ — — |
| `panels.info.service-logs` | 服务日志对话框 | 信息 → orb 卡故障行「日志」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.changes` | 改动面板（范围、摘要、暂存 / 推送 / 同步、文件列表） | 信息面板 → 改动 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.changes.diff` | 单文件差异 FileDiff | 改动 → 点 `main.ts` | 面板（二级） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.changes.sync-menu` | 同步菜单 | 改动 → 「同步」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.changes.row-menu` | 改动行上下文菜单 | 改动 → 右键文件行；手机长按 | 菜单 / 底部动作面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.changes.compare-branch` | 与分支比较：分支名输入行 | 改动 → 范围下拉选「与分支比较…」 | 面板内联表单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.pr-chip` | PR 胶囊菜单 | 线程页头「PR #128」（仅桌面：手机页头没有 PR 胶囊） | 菜单 | ✓ ✓ ✓ ✓ — — |
| `panels.files` | 文件面板（根标签、工具钮、FileTree） | 信息面板 → 文件 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.files.row-menu` | 文件行操作菜单 | 文件 → 行尾「src 的操作」 | 菜单 / 底部动作面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.files.viewer` | 文件查看器 FileViewer（预览 / 源码 / 保存 / 下载） | 文件 → 点 `README.md` | 面板（二级） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.files.home` | 主目录根 | 文件 → 根标签「主目录」 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.files.upload` | 上传队列 UploadQueue（6 个长短不一的文件名） | 文件 → 上传 → 选多个文件 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.terminal` | 终端（标签条、xterm、手机键栏） | 信息面板 → 终端 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.terminal.tabs` | 多终端标签（5 个） | 终端 → 「+」新建 4 次 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.portal` | Portal（工具条、地址、iframe） | 信息面板 → Portal | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.portal.access` | 访问权限 AccessPopover | Portal → 齿轮 | 弹层（桌面）/ 弹窗（手机） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.desktop` | 桌面（工具条、舞台） | 信息面板 → 桌面 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.desktop.profile-menu` | ProfileMenu | 桌面 → 左上 Profile 名 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.desktop.clipboard` | 剪贴板 ClipboardPopover | 桌面 → 剪贴板钮 | 弹层（桌面）/ 弹窗（手机） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.desktop.more` | 桌面「更多」菜单 | 桌面 → 「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.desktop.keyboard` | 软键盘 SoftKeyboard（仅手机：只在粗指针设备上出现键盘钮） | 桌面 → 键盘钮 | 面板 | — — — — ✓ ✓ |
| `panels.desktop.switch-confirm` | 切换 Profile 确认对话框 | 桌面 → Profile 菜单 → 选另一个 Profile（浏览器在跑或已装 Profile 时） | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.desktop.conflict` | Profile 冲突对话框 ProfileConflictDialog | 被占用线程的桌面 → Profile 菜单 → 选另一个被占用的 Profile | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.artifacts` | Artifacts 面板（查看器） | 信息面板 → Artifacts | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.artifacts.switch` | Artifact 切换菜单 | Artifacts → 标题下拉 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `panels.artifacts.menu` | ArtifactMenu | Artifacts → 「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |

## sections（85 个界面，18 条问题，详见 [areas/sections.md](areas/sections.md)）

| id | 界面 | 打开路径 | 形态 | 状态 |
|---|---|---|---|---|
| `projects.list` | 项目列表 | 底栏 / 侧栏「项目」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.new` | 新建项目 | 项目分区「新建项目」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page` | 项目页（顶部：项目设置） | 项目分区点某个项目 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.settings-bottom` | 项目设置下半部分（setup 脚本、保存） | 项目页往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.agent-config` | Agent 配置卡 | 项目页往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.github` | GitHub 卡（钩子、触发规则） | 项目页往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.ssh` | SSH 主机卡 | 项目页往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.accel` | 快照与预热卡 | 项目页往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.schedules` | 服务卡与定时任务卡（页底） | 项目页滚到底 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.repo-picker` | 仓库选择下拉 | 项目设置「从 GitHub 选择」 | 下拉 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.trigger-dialog` | 新建触发规则对话框 | GitHub 卡「新建」 | 弹窗（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.trigger-dialog.model` | 触发规则的模型池选择器 | 触发规则对话框「添加模型」 | 下拉 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.schedule-menu` | 定时任务行菜单 | 定时卡某行「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.schedule-new` | 新建定时任务对话框 | 定时卡「新建定时任务」 | 弹窗（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `projects.page.schedule-edit` | 编辑定时任务对话框（含运行记录） | 定时卡某行「…」→ 编辑 | 弹窗（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `space.page` | Space 页（聊天 + 右列） | 侧栏「空间」下点某个空间 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `space.side` | Space 右列（进度、定时、线程卡片；手机在「线程」标签） | 宽屏直接可见 / 手机切到「线程」标签 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `space.menu` | 空间操作菜单 | 页头「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `space.edit-dialog` | 编辑空间对话框 | 空间操作 → 编辑空间 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `space.thread-menu` | 线程卡片菜单 | 线程卡片「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `space.add-thread` | 加入线程（搜索候选） | 右列「加入线程」 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `space.schedule-dialog` | 空间定时对话框 | 右列定时行「…」→ 编辑 | 弹窗（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `space.schedule-menu` | 空间定时行菜单 | 定时行「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.landing` | `/assistant` 落到最近一个助手对话 | 底栏「全局助手」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.thread` | 助手对话 | 助手分区点某个对话 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.thread.mount-notice` | 助手对话首轮前的 xd:// 设备挂载提示（只给模型看） | 在助手对话里发消息，模型首个 token 前 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.main` | 助手主线 | 助手分区「全局助手」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.main.mount-notice` | 主线首轮前的 xd:// 设备挂载提示 | 在主线发消息，模型首个 token 前 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.thread.custom-message` | 可显示的长扩展注入消息（折叠摘要） | 扩展 / 技能注入 `display: true` 的消息时 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.thread.menu` | 对话操作菜单 | 页头「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.thread.library` | 文件库 | 页头文件夹按钮 | 弹窗（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.main.notes` | 主线备忘 | 主线页头「备忘」 | 弹窗（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.main.reminders` | 主线提醒 | 主线页头「提醒」 | 弹窗（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `assistant.computer` | 助手的电脑（未唤醒空态） | 助手分区「助手的电脑」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `inbox.page` | 收件箱（按状态） | 侧栏「收件箱」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `inbox.by-project` | 收件箱（按项目） | 页头「按项目」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `inbox.bottom` | 收件箱滚到底 | 收件箱往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.usage` | 统计 · 用量 | 更多 → 统计 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.usage.tables` | 用量：按模型 / 项目表 | 用量页往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.usage.tables-bottom` | 用量：线程 / 用途 / 工具表（页底） | 用量页滚到底 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.usage.range-menu` | 范围菜单（窄屏） | 工具行「近 30 天」 | 菜单（仅手机；宽屏是分段按钮） | — — — — ✓ ✓ |
| `stats.usage.custom-range` | 自定义范围对话框 | 范围「自定义…」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.usage.export` | 导出菜单 | 工具行「导出」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.usage.budget` | 设置预算对话框 | 预算卡「设置预算」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.usage.tooltip` | 主图浮层 | 点主图某根柱子 | 提示 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.orbs` | 统计 · Orb | 统计页「Orb」标签 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.orbs.list` | Orb 列表 | Orb 页往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.orbs.drawer` | Orb 详情抽屉 | Orb 列表点某行 | 抽屉 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.orbs.tooltip` | Orb 主图浮层 | 点主图某根柱子 | 提示 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.output` | 统计 · 产出 | 统计页「产出」标签 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.output.tables` | 产出表（页底） | 产出页滚到底 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `stats.output.tooltip` | 产出主图浮层 | 点主图某根柱子 | 提示 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.list` | Artifacts 总览 | 更多 → Artifacts | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.list.filter` | 筛选弹层 | 工具行漏斗 | 下拉（仅桌面） | ✓ ✓ ✓ ✓ — — |
| `artifacts.list.display` | 显示弹层（排序、布局） | 工具行滑杆 | 下拉（仅桌面） | ✓ ✓ ✓ ✓ — — |
| `artifacts.list.import` | 导入菜单 | 工具行「+」 | 菜单（仅桌面） | ✓ ✓ ✓ ✓ — — |
| `artifacts.list.options` | 筛选与显示（窄屏合并菜单） | 工具行「筛选与显示」 | 下拉（仅手机） | — — — — ✓ ✓ |
| `artifacts.list.card-menu` | 卡片更多菜单 | 悬停卡片右上「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.list.search` | 搜索结果 | 工具行放大镜 → 输入 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.list.bottom` | 总览滚到底（最近删除入口） | 总览往下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.trash` | 最近删除 | 总览底部「最近删除」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.trash.card-menu` | 回收站卡片菜单 | 回收站卡片「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.web` | 页面查看器 | 点页面类卡片 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.web.menu` | 查看器更多菜单 | 标题旁「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.web.history` | 版本历史侧栏 | 版本胶囊 / 历史按钮 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.web.comments` | 评论侧栏 | 版本胶囊 → 评论 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.web.comment-mode` | 评论模式 | 顶栏评论按钮 | 页面（仅桌面；手机无此按钮） | ✓ ✓ ✓ ✓ — — |
| `artifacts.web.share` | 分享弹层（未发布） | 顶栏「分享」 | 弹窗（桌面为浮层，手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.web.presenter` | 演示模式 | 顶栏播放按钮 | 弹窗（全屏） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.document` | 文档查看器 | 点文档类卡片 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.document.share` | 分享弹层（已发布） | 顶栏「已分享」 | 弹窗（桌面为浮层，手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.slides` | 幻灯片查看器 | 点幻灯片类卡片 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.canvas` | 画布编辑器 | 点画布类卡片 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `artifacts.canvas.zoom-menu` | 画布缩放菜单 | 底部工具栏百分比 | 菜单（仅桌面：手机上画布只读，工具栏换成视图条） | ✓ ✓ ✓ ✓ — — |
| `artifacts.canvas.sheet-menu` | 画布页面菜单 | 底部工具栏页面名 | 菜单（仅桌面：手机视图条只在多页时有此菜单，夹具只有一页） | ✓ ✓ ✓ ✓ — — |
| `artifacts.canvas.layers` | 图层面板 | 底部工具栏图层按钮 | 面板（仅桌面：手机不显示图层面板） | ✓ ✓ ✓ ✓ — — |
| `artifacts.open.gone` | 私有发布页已下线 | 打开已取消发布的 `/artifacts/open/<slug>` | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `share.snapshot` | 匿名分享页（快照） | 打开 `/s/<slug>` | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `share.live` | 匿名分享页（实时） | 打开实时分享链接 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `share.role-snapshot` | 匿名分享页（role 回合头） | 打开 role 线程的分享链接 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `share.gone` | 分享不存在 | 打开失效的 `/s/<slug>` | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `auth.login` | 登录 | 未登录时打开任意页 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `auth.setup` | 首次设置 / 重新注册 Passkey | `/setup` | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `auth.pair` | 批准设备（输入配对码） | `/pair` | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `auth.pair.review` | 批准设备（配对详情） | 输入配对码 →「查看」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |

## settings（89 个界面，18 条问题，详见 [areas/settings.md](areas/settings.md)）

| id | 界面 | 打开路径 | 形态 | 状态 |
|---|---|---|---|---|
| `settings.devices` | 设备：批准配对、设备列表、扫码连接 | 侧栏「更多」→ 设备（或 Ctrl K「设备」） | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.devices.pair` | 批准配对：输入配对码后的设备信息卡 | 设备 → 输入配对码 → 查看 | 页面内卡片 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.providers` | Provider 列表与新建 | 更多 → Provider | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.providers.created` | 新建 provider 后一次性显示令牌 | Provider → 填名称 → 创建 | 页面内提示 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.runners` | Runner 列表与新建 | 更多 → Runner | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.runners.created` | 新建 runner 后显示令牌与启动命令 | Runner → 填名字 → 创建 | 页面内提示 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.secrets` | 密钥：写入表单与列表 | 更多 → 密钥 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.browser-profiles` | 浏览器 Profile | 更多 → 浏览器 Profile | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.git-identity` | Git 提交身份 | 更多 → Git 身份 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.github` | GitHub：OAuth App、账号 | 更多 → GitHub | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.github.automation` | GitHub：自动化、Webhook、审计（下半页） | GitHub 页向下滚到「自动化」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.github.connect` | 连接 Agent 账号（设备码） | GitHub → Agent 账号「连接」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-skills` | 技能 | 更多 → Agent → 技能 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-skills.new` | 新建技能 | 技能 → 新建 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-skills.edit` | 编辑技能 | 技能 → 行尾铅笔 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-skills.delete` | 删除技能确认 | 技能 → 行尾垃圾桶 | 确认框 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-subagents` | 子 agent：内置与自定义 | 更多 → 子 agent | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-subagents.expanded` | 内置 agent 展开：模型覆盖、头像 | 子 agent → task「设置」 | 页面内面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-subagents.new` | 新建子 agent | 子 agent → 自定义 agent「新建」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-roles` | 模型角色：轮换顺序 | 更多 → 模型角色 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-roles.others` | 模型角色：其他角色（下半页） | 模型角色页向下滚 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-roles.expanded` | 角色展开：模型、显示名、头像 | 模型角色 → 默认「设置」 | 页面内面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-roles.new` | 新建自定义角色 | 模型角色 → 新建角色 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-prompts` | 提示词与上下文文件 | 更多 → 提示词与上下文 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-prompts.edit` | 编辑 AGENTS.md | 提示词 → AGENTS.md 铅笔 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-prompts.delete` | 删除 AGENTS.md 确认 | 提示词 → AGENTS.md 垃圾桶 | 确认框 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-commands` | 命令、规则、提示片段（空态） | 更多 → 命令与规则 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-commands.new` | 新建斜杠命令 | 命令与规则 → 斜杠命令「新建」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-extensions` | 扩展、Hook、自定义工具（空态） | 更多 → 扩展与工具 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-extensions.new` | 在「扩展模块」中新建文件 | 扩展与工具 → 扩展模块「新建」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory` | 记忆：页内目录、状态、连接、项目开关 | 更多 → 记忆 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.models` | 记忆：模型、嵌入与重排 | 记忆页向下滚到「模型」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.browse` | 记忆：浏览 | 记忆页向下滚到「浏览」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.candidates` | 记忆：全局记忆、待批准、mental models | 记忆页向下滚到「全局记忆」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.usage` | 记忆：用量、导出 | 记忆页向下滚到「用量」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.owner` | 助手记忆：设置、状态 | 记忆页向下滚到「助手记忆」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.owner-facts` | 助手记忆：事实 | 记忆页向下滚到「事实」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.toc` | 记忆：页内目录跳到「全局记忆」 | 记忆页顶部目录 →「全局记忆」 | 页面（吸顶目录） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.owner-sources` | 助手记忆：来源与排除、明确记忆 | 记忆页向下滚到「来源」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.env-file` | 重新生成 Hindsight .env | 记忆 → 连接「重新生成 .env」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.owner-brief` | 助手记忆注入预览 | 记忆 → 助手记忆状态「注入预览」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-memory.owner-forget` | 按来源遗忘确认 | 记忆 → 来源「按来源遗忘」 | 确认框 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh` | SSH 主机：主机、钥匙 | 更多 → SSH 主机 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.audit` | SSH 网关审计（下半页） | SSH 主机页向下滚到「网关审计」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.test-result` | 主机测试结果卡 | SSH 主机 → bastion「测试」 | 页面内面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.host-new` | 添加主机（含开关、下拉） | SSH 主机 → 添加主机 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.host-menu` | 主机行「更多」菜单 | SSH 主机 → bastion 行「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.host-edit` | 编辑主机 | 主机行「…」→ 编辑 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.host-delete` | 删除主机确认 | 主机行「…」→ 删除 | 确认框 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.host-files` | 主机文件浏览 | 主机行「…」→ 文件 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.key-generate` | 生成钥匙 | 钥匙卡「生成钥匙」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.key-import` | 导入私钥 | 钥匙卡「导入私钥」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.key-menu` | 钥匙行「更多」菜单 | 钥匙行「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.key-rename` | 重命名钥匙 | 钥匙行「…」→ 重命名 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-ssh.key-delete` | 删除钥匙确认 | 钥匙行「…」→ 删除 | 确认框 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-composer` | 输入框：下一条消息预测 | 更多 → 输入框 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.selection-actions` | 选区快捷动作（空态） | 更多 → 选区快捷动作 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.selection-actions.new` | 添加选区动作 | 选区快捷动作 → 添加动作 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-config` | 高级：agent 配置文件浏览器 | 更多 → 高级 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-config.file-view` | 打开 config.yml | 高级 → 点 config.yml | 页面内面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-config.file-menu` | 文件行「…」菜单 | 高级 → config.yml 行「…」 | 菜单（上下文菜单） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.agent-config.new-file` | 新建文件 | 高级 → 新建文件图标 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.identity` | 身份与头像：默认 Agent 身份 | 更多 → 身份与头像 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.identity.assistant` | 身份与头像：全局助手（下半页） | 身份页向下滚到「全局助手」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.identity.crop` | 头像裁剪 | 身份 → 上传图片 → 选图 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.assistant` | 全局助手：联网、MCP、助手的电脑 | 更多 → 全局助手 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.appearance` | 外观：明暗、主题色、壁纸 | 更多 → 外观 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.notifications` | 通知：此设备、渠道 | 更多 → 通知 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.notifications.recent` | 通知：最近通知（下半页） | 通知页向下滚到「最近通知」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.notifications.add-menu` | 添加渠道菜单 | 通知 → 渠道「添加渠道」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.notifications.channel-new` | 添加 Webhook 渠道 | 添加渠道 → Webhook | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.notifications.channel-edit` | 编辑渠道 | 渠道行铅笔 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.shares` | 公开分享 | 更多 → 公开分享 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.artifacts` | 已授权的 Artifacts | 更多 → Artifacts 授权 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.automations` | 自动化：定时任务 | 更多 → 自动化 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.automations.actions` | 自动化：操作记录（下半页） | 自动化页向下滚到「操作记录」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.automations.row-menu` | 定时行「…」菜单 | 自动化 → nightly 行「…」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.automations.new` | 新建定时任务 | 自动化 → 创建 | 弹窗（手机为底部抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.automations.new.role-picker` | 定时对话框里的「对象」选择 | 新建定时任务 → 对象胶囊 | 对话框内下拉（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.automations.new.model-picker` | 定时对话框里的「指定模型」 | 对象胶囊 → 指定模型… | 对话框内下拉（手机为抽屉） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.automations.edit` | 编辑定时（含运行记录） | 自动化 → 点 nightly | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.about` | 关于、协调宿主、退出登录 | 更多 → 关于 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `settings.desktop` | 桌面应用（仅桌面壳；只跑桌面视口） | 桌面 App → 更多 → 桌面应用 | 页面 | ✓ ✓ ✓ ✓ — — |
| `settings.desktop.notifications` | 桌面壳的通知页（桌面通知卡；只跑桌面视口） | 桌面 App → 通知 | 页面 | ✓ ✓ ✓ ✓ — — |
| `settings.desktop.about` | 桌面壳的关于页（无退出登录；只跑桌面视口） | 桌面 App → 关于 | 页面 | ✓ ✓ ✓ ✓ — — |
| `settings.mobile.notifications` | 移动壳的通知页（推送卡；只跑手机视口） | iOS App → 通知 | 页面 | — — — — ✓ ✓ |
| `settings.mobile.appearance` | 移动壳的外观页（App 图标；只跑手机视口） | iOS App → 外观 → 滚到壁纸 | 页面 | — — — — ✓ ✓ |
| `settings.mobile.about` | 移动壳的关于页（App 版本、更换服务器；只跑手机视口） | iOS App → 关于 | 页面 | — — — — ✓ ✓ |
| `settings.nav` | 手机上的分区导航（侧栏抽屉；只跑手机视口） | 手机设置页左上角菜单 | 抽屉 | — — — — ✓ ✓ |

## shell（18 个界面，10 条问题，详见 [areas/shell.md](areas/shell.md)）

| id | 界面 | 打开路径 | 形态 | 状态 |
|---|---|---|---|---|
| `shell.sidebar` | 侧栏·线程分区：品牌行（铃铛 / 收起 / 新建）、搜索入口、收件箱、需要你、空间、已置顶、项目分组、底部分区胶囊 | 启动后默认（手机：页头侧栏开关打开下沉侧栏） | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.sidebar.folds` | 侧栏底部折叠组全部展开：72 小时未活动（全部归档）、稍后提醒、已归档；定时折叠行 nightly 展开 | 侧栏 → 点各折叠组标题 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.sidebar.collapsed` | 侧栏收起后的线程页（仅桌面：手机侧栏本来就是下沉的） | 侧栏品牌行「收起侧边栏」 | 页面 | ✓ ✓ ✓ ✓ — — |
| `shell.sidebar.assistant` | 侧栏·全局助手分区 | 底部胶囊「全局助手」 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.sidebar.projects` | 侧栏·项目分区 | 底部胶囊「项目」 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.sidebar.more` | 侧栏·更多分区（设置入口列表） | 底部胶囊「更多」 | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.sidebar.more.deep` | 侧栏·更多分区，当前分区在列表末尾（关于） | 直接打开 `/settings/about`（刷新或从别处跳入） | 面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.row-menu` | 线程行上下文菜单 | 侧栏右键线程行；手机长按 | 菜单 / 底部动作面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.space-new` | 新建空间对话框 | 侧栏「空间」组标题的「+」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.sink` | 手机下沉侧栏（线程页上打开，卡片右移）（仅手机） | 线程页页头侧栏开关 | 抽屉 | — — — — ✓ ✓ |
| `shell.notifications` | 通知铃铛弹层（18 条，长标题、各类 kind） | 侧栏品牌行铃铛 | 弹层（桌面）/ 弹窗（手机） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.notifications.empty` | 通知空态 | 同上，没有通知时 | 弹层 / 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.toasts` | 通知 toast（右上，3 条：提问、完成、预算） | 前台收到通知时 | 提示 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.toasts.actions` | 操作提示（底部居中：进行中、带撤销的长文案、出错） | 删除 / 导出 / 恢复版本等操作之后 | 提示 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.server-errors` | 服务端错误提示（带「重新同步」与线程链接） | 订阅失败、侧通道应答被拒等 | 提示 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.palette` | 命令面板（缺省列表） | 侧栏搜索入口；Ctrl/⌘K | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.palette.results` | 命令面板长结果（线程、Artifacts、对话内容） | 命令面板输入「重」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `shell.palette.empty` | 命令面板空结果 | 输入不存在的词 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |

## thread（57 个界面，18 条问题，详见 [areas/thread.md](areas/thread.md)）

| id | 界面 | 打开路径 | 形态 | 状态 |
|---|---|---|---|---|
| `thread.top` | 主线程转录顶部（首条用户消息、回合头、工具卡收起态） | 侧栏点线程，往上翻到顶 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.mid` | 主线程转录中段（表格、时序图、公式、用量胶囊） | 同上，翻到中间 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.table-narrow` | 900 宽窄桌面窗口下的 Markdown 表格（中栏放不下，横向滚动；仅桌面，手机表格列按词换行能放下） | 把窗口拉窄到 900，翻到回复里的表格 | 页面 | ✓ ✓ ✓ ✓ — — |
| `thread.bottom` | 主线程转录底部 + 输入坞 | 打开线程（默认贴底） | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.tools` | 工具样例线程顶部（各工具卡收起） | 侧栏「工具样例」 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.tools.mid` | 工具样例中段 | 同上，翻到中间 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.tools.expanded` | 工具卡全部展开（diff、patch、代码） | 点开每张工具卡 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.tools.expanded-end` | 工具卡全部展开，翻到底（artifact、bash 输出） | 同上，翻到底 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.env.setupFailed` | 环境 setup 失败横幅 | 打开 setup 失败的线程 | 页面（托盘内横幅） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.env.slowSetup` | setup 进行中横幅 | 打开 setup 慢的线程 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.env.missingConfig` | 缺少环境配置横幅 | 打开缺配置的线程 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.env.orbFailed` | orb 故障横幅 | 打开 orb 故障的线程 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.runner` | runner 线程（页头 runner · cwd 胶囊） | 打开 runner 上的线程 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.runner-offline` | runner 离线提示 | 打开离线 runner 上的线程 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.scheduled` | 定时线程（页头定时胶囊） | 打开带定时的线程 | 页面 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.ask` | agent 提问卡（UiRequestCard，托盘内） | agent 发起 ask | 面板（托盘内） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.plan` | 计划提案（PlanProposalDialog） | agent 提交计划 | 弹窗 / 手机底部抽屉 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.jump-end` | 「回到底部」小圆钮（仅桌面；手机往上翻时输入坞收成胶囊，见下一项） | 往上翻转录 | 浮钮 | ✓ ✓ ✓ ✓ — — |
| `thread.dock-pill` | 输入坞收起成信息胶囊（仅手机） | 手机往上翻转录 | 浮动胶囊 | — — — — ✓ ✓ |
| `thread.quote` | 选区引用胶囊（桌面贴选区）/ 引用条（触屏贴托盘） | 选中助手正文 | 浮动胶囊 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.side-ask` | 解释浮卡（SideAskCard；手机是对话框） | 选区胶囊 →「解释」 | 浮卡 / 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.message-menu` | 消息操作菜单（复制 / 引用 / 分叉 / 加标签） | 悬停用户消息 →「⋯」 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.context-menu` | 转录右键菜单（仅桌面；正文没有 `data-ctx-touch`，手机长按正文留给系统选字） | 右键助手正文 | 菜单 | ✓ ✓ ✓ ✓ — — |
| `thread.tool-context-menu` | 工具卡右键菜单（桌面）/ 长按动作面板（手机，`context-sheet`） | 右键工具卡 / 长按工具卡头 | 菜单 / 动作面板 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.slash` | 斜杠命令菜单（CommandMenu） | 输入框键入 `/` | 下拉（坞上方） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.slash.usage` | 命令用法卡（`/goal ` 之后） | 键入 `/goal ` | 下拉（坞上方） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.ref-menu` | GitHub issue / PR 引用菜单（RefMenu） | 键入 `#log`（用例以 Esc 关闭，重开时先删一个字再敲回） | 下拉（坞上方） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.thinking` | 思考级别菜单（ThinkingDial） | 输入坞模型胶囊右侧圆圈 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.speed` | 速度档位菜单 | 输入坞页脚速度读数 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.modes-more` | 「更多模式」菜单（仅手机：窄托盘放不下六个模式开关） | 输入坞「⋯」 | 菜单 | — — — — ✓ ✓ |
| `thread.role-track` | 角色模型轨道（RoleTrack，仅桌面：Ctrl+P 快捷键） | Ctrl+P 切换角色 | 浮动胶囊 | ✓ ✓ ✓ ✓ — — |
| `thread.attachments-tray` | 附件托盘（长文件名、多文件） | 输入坞回形针 / 拖入文件 | 面板（托盘内） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.actions` | 线程操作菜单（宽页头「更多操作」/ 手机标题菜单） | 页头「⋯」/ 手机点标题胶囊 | 菜单 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.actions.snooze` | 子菜单「稍后提醒」 | 操作菜单 →「稍后提醒」 | 菜单（换页） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.actions.color` | 子菜单「颜色」 | 操作菜单 →「颜色」 | 菜单（换页） | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.pr-menu` | PR 胶囊菜单（仅桌面；手机 PR 在标题菜单里） | 页头「#128」 | 菜单 | ✓ ✓ ✓ ✓ — — |
| `thread.view-menu` | 视图菜单（仅手机：宽屏是标签条） | 页头右上角视图钮 | 菜单 | — — — — ✓ ✓ |
| `thread.dlg.tree` | 会话树 | 页头树图标 / `/tree` | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.fork` | 分叉线程 | 操作菜单「分叉」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.model` | 模型（角色 + 全部模型） | 输入坞模型胶囊 / `/model` | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.goal` | 目标 | 模式开关「目标」/ `/goal` | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.btw` | 旁路提问（空） | `/btw` | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.btw-topic` | 旁路提问（带话题、问答详情） | `/btw <问题>` | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.rename` | 改名 | 操作菜单「改名」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.labels` | 线程标签 | 操作菜单「标签」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.label` | 消息标签 | 消息菜单「加标签」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.debug` | 调试面板 | 操作菜单「调试面板」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.attachments` | 附件 | 操作菜单「附件」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.share` | 分享线程 | 操作菜单「分享…」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.share-findings` | 分享：扫描发现疑似密钥 | 分享 →「创建链接」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.schedule` | 定时（从线程打开） | 操作菜单「定时…」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.archive` | 归档（runner worktree 线程） | 操作菜单「归档」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.delete-confirm` | 删除线程确认（共享确认框 ConfirmHost） | 操作菜单「删除」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.context` | 上下文报告（ContextBreakdown） | 输入坞页脚上下文圆环 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.subagent` | 子 agent 转录 | 工具卡里的子 agent 链接 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.dlg.setup-log` | setup 日志 | setup 失败横幅「查看日志」 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |
| `thread.home.dir-in-use` | 目录被占用（DirInUseDialog） | 首页选 runner 已占用目录后发送 | 弹窗 | ✓ ✓ ✓ ✓ ✓ ✓ |

