# 区域：panels（线程页右侧面板）

spec：`web/e2e/ui-audit/panels.e2e.ts`。面板本身按「浮层」登记（根 = 右侧面板 `aside`），这样会断言遮挡 / 玻璃，并把面板里的滚动区滚到底再截一张。宽屏点页头「信息面板」再点 `[data-panel-tab]`；手机经页头视图菜单 `thread-view-menu` → `[data-view]`。

未单独登记：
- CommitPicker（改动范围）是原生 `<select>`，展开的选项列表由浏览器绘制，截不到也不受页面样式影响；它触发的「与分支比较…」输入行登记为 `panels.changes.compare-branch`。
- ArtifactCard 出现在转录与 Artifacts 页，归 thread / sections 区域；面板里是查看器（`panels.artifacts*`）。
- TerminalKeyBar（手机终端键栏）在 `panels.terminal` / `panels.terminal.tabs` 的 m390 截图里。

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| `panels.info` | 信息面板（orb 卡、CPU / 内存、上下文、待办、服务 / 环境 / 能力 / SSH 入口行） | 线程页 → 信息面板 → 信息；手机：页头视图菜单 → 信息 | 面板 |
| `panels.info.narrow` | 信息面板，900×860 窄桌面窗口（侧栏 + 转录 + 面板三栏）（仅桌面） | 同上，窗口拉窄到 900 宽 | 面板 |
| `panels.info.service-logs` | 服务日志对话框 | 信息 → orb 卡故障行「日志」 | 弹窗 |
| `panels.changes` | 改动面板（范围、摘要、暂存 / 推送 / 同步、文件列表） | 信息面板 → 改动 | 面板 |
| `panels.changes.diff` | 单文件差异 FileDiff | 改动 → 点 `main.ts` | 面板（二级） |
| `panels.changes.sync-menu` | 同步菜单 | 改动 → 「同步」 | 菜单 |
| `panels.changes.row-menu` | 改动行上下文菜单 | 改动 → 右键文件行；手机长按 | 菜单 / 底部动作面板 |
| `panels.changes.compare-branch` | 与分支比较：分支名输入行 | 改动 → 范围下拉选「与分支比较…」 | 面板内联表单 |
| `panels.pr-chip` | PR 胶囊菜单 | 线程页头「PR #128」（仅桌面：手机页头没有 PR 胶囊） | 菜单 |
| `panels.files` | 文件面板（根标签、工具钮、FileTree） | 信息面板 → 文件 | 面板 |
| `panels.files.row-menu` | 文件行操作菜单 | 文件 → 行尾「src 的操作」 | 菜单 / 底部动作面板 |
| `panels.files.viewer` | 文件查看器 FileViewer（预览 / 源码 / 保存 / 下载） | 文件 → 点 `README.md` | 面板（二级） |
| `panels.files.home` | 主目录根 | 文件 → 根标签「主目录」 | 面板 |
| `panels.files.upload` | 上传队列 UploadQueue（6 个长短不一的文件名） | 文件 → 上传 → 选多个文件 | 面板 |
| `panels.terminal` | 终端（标签条、xterm、手机键栏） | 信息面板 → 终端 | 面板 |
| `panels.terminal.tabs` | 多终端标签（5 个） | 终端 → 「+」新建 4 次 | 面板 |
| `panels.portal` | Portal（工具条、地址、iframe） | 信息面板 → Portal | 面板 |
| `panels.portal.access` | 访问权限 AccessPopover | Portal → 齿轮 | 弹层（桌面）/ 弹窗（手机） |
| `panels.desktop` | 桌面（工具条、舞台） | 信息面板 → 桌面 | 面板 |
| `panels.desktop.profile-menu` | ProfileMenu | 桌面 → 左上 Profile 名 | 菜单 |
| `panels.desktop.clipboard` | 剪贴板 ClipboardPopover | 桌面 → 剪贴板钮 | 弹层（桌面）/ 弹窗（手机） |
| `panels.desktop.more` | 桌面「更多」菜单 | 桌面 → 「…」 | 菜单 |
| `panels.desktop.dock` | 任务栏 DesktopDock（应用图标、运行圆点、「其它窗口」） | 信息面板 → 桌面，舞台下方 | 面板 |
| `panels.desktop.dock.menu` | 任务栏窗口菜单（多窗口：切到前台、关闭、新建窗口） | 桌面 → 任务栏点有多个窗口的应用（终端） | 菜单 |
| `panels.desktop.keyboard` | 软键盘 SoftKeyboard（仅手机：只在粗指针设备上出现键盘钮） | 桌面 → 键盘钮 | 面板 |
| `panels.desktop.switch-confirm` | 切换 Profile 确认对话框 | 桌面 → Profile 菜单 → 选另一个 Profile（浏览器在跑或已装 Profile 时） | 弹窗 |
| `panels.desktop.conflict` | Profile 冲突对话框 ProfileConflictDialog | 被占用线程的桌面 → Profile 菜单 → 选另一个被占用的 Profile | 弹窗 |
| `panels.artifacts` | Artifacts 面板（查看器） | 信息面板 → Artifacts | 面板 |
| `panels.artifacts.switch` | Artifact 切换菜单 | Artifacts → 标题下拉 | 菜单 |
| `panels.artifacts.menu` | ArtifactMenu | Artifacts → 「…」 | 菜单 |

## 问题记录

| # | 界面 id | 问题 | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | panels.portal.access | 访问权限弹层是手写的 `absolute z-40` 玻璃块：开在面板工具条里，靠面板的层叠上下文定位；打开时 `.pop-in` 淡入，壳 opacity<1 期间自己成了 Backdrop Root，模糊先缺后补（「透明度时有时无」） | 错乱 | `shots/panels.portal.access/d1366-light-before.json` | fe93d86（迁到 ui/Floating：顶层、锚点定位、外壳不滚动） |
| 2 | panels.desktop.clipboard | 同 #1：剪贴板弹层手写 `absolute top-full right-0 z-40`，打开淡入截断模糊 | 错乱 | `shots/panels.desktop.clipboard/d1920-light-before.json` | fe93d86 |
| 3 | panels.changes.sync-menu、panels.desktop.profile-menu、panels.desktop.more、panels.artifacts.switch、panels.artifacts.menu、panels.pr-chip | 这些菜单都走旧 `Menu`（`absolute z-40` + 淡入），同 #1 的模糊截断 | 错乱 | `shots/panels.desktop.profile-menu/d1366-light-before.json` | 1bec702（主 agent：Menu 改用 Floating） |
| 4 | panels.artifacts.menu（m390） | 手机上 Artifact「…」菜单右缘超出视口（198,88 → 406,290，视口 390 宽），「导出…」的子菜单箭头被切掉 | 错乱 | `shots/panels.artifacts.menu/m390-light-before.png` | 1bec702（Floating 横向夹在视口内） |
| 5 | panels.changes.row-menu、panels.files.row-menu（桌面） | 上下文菜单的玻璃壳本身是滚动容器，内容一滚 `::before` 模糊底跟着滚走；打开时淡入 | 错乱 | `shots/panels.files.row-menu/d1920-light-before.json` | 1bec702（主 agent：ContextMenu 外壳不滚动） |
| 6 | panels.terminal.tabs | 终端标签多了以后整条横向滚动，「新建终端」按钮跟着被挤到可视区外；新建的标签也停在右侧看不见 | 错乱 | `shots/panels.terminal.tabs/m390-light-before.png` | 8e31274（只让标签区滚动，「+」钉在右侧；选中的标签滚进来）、b12b273（只滚标签条自己，不带动页面） |
| 7 | panels.files.viewer | 文件查看器头部路径拆段时目录与文件名一起收缩，手机上成了「/h…README.…」，文件名反而看不全 | 设计 | `shots/panels.files.viewer/m390-light-before.png` | 8e31274（文件名不收缩、目录先截断） |
| 8 | panels.files（m390、窄面板） | 根标签（工作区 / 主目录 / 项目 Agent 配置 / Agent 配置）放不下时在右缘被硬切成「A」，看不出还能横向滑 | 设计 | `shots/panels.files/m390-light-before.png` | 8e31274（隐藏滚动条，哪侧还有标签就在哪侧渐隐；选中的根滚进来）、b12b273 |
| 9 | panels.changes | 面板头部三行：摘要（分支名 + 计数）被右侧「推送更新 / 同步」挤成两行，暂存按钮又单占一行 | 设计 | `shots/panels.changes/d1366-light-before.png` | 571b9de（摘要独占一行；暂存在左、推送 / 同步在右合成一行） |
| 10 | panels.info | 同一个服务故障在信息面板出现三次：orb 卡副标题「· 1 项故障」、紧挨着的故障提示行「worker 失败 · 重启 5 次 重启 日志」、下方「服务」入口行的红色标签 | 冗余 | `shots/panels.info/m390-light-before.png` | 9f1211c（三处改两处：只有一项故障时去掉副标题里的计数；多项时提示行只列第一项，计数保留。「服务」入口行标签保留：docs/design/web-guidelines.md 的 B2 守则规定入口行右侧淡底标签是进入详情前的状态摘要） |
| 11 | panels.info.narrow | 900 宽三栏（侧栏 + 转录 + 面板）时「服务」入口行的故障标签从左边被裁掉，只剩「败 · 重启 5 次」：标签与外层都是 `shrink-0`，状态区 `justify-end + overflow-hidden`，放不下时整枚标签向左溢出 | 错乱 | `shots/host.chrome-mac.narrow/d1366-light-after.png`、`shots/panels.info.narrow/d1366-light-before.png` | 1cfb2dc（标签可收缩、内容在标签里从右截断显示省略号）；新增 900 宽用例覆盖 |
| 12 | panels.desktop.more | 「分辨率跟随面板 / 缩放以适应」两项没有图标，文字左缘与下面带图标的「关闭浏览器 / 重新连接」对不齐 | 设计 | `shots/panels.desktop.more/d1366-light-before.png` | 5a61435（补 monitor / minimize 图标） |
| 13 | panels.info.service-logs（m390） | 手机上服务日志不折行，报错行后半截（模块路径）要横向滚才看得到 | 设计 | `shots/panels.info.service-logs/m390-dark-before.png` | fa9ed53（sm 以下折行） |
| 14 | panels.desktop.conflict | 冲突对话框本身无问题；但切换 Profile 前的「当前浏览器会被关闭」是原生 `window.confirm`，样式不受控 | 设计 | `shots/panels.desktop.switch-confirm/d1366-light-after.png` | 961f44c（主人决定：换成应用内确认对话框，标题 + 说明 + 取消 / 切换；被占用的 Profile 确认后照常进冲突对话框）；dc1dfb4 改用全站共享的 `confirmAction`，其余 21 处删除 / 清除类确认一并迁移（见 thread #18） |
| 15 | panels.desktop.switch-confirm | 用键盘在 Profile 菜单里选中后弹确认框，按 Esc 或取消，焦点掉到 body：菜单项选中后随菜单卸载，Dialog 记下的打开者已不在页面上 | 错乱 | — | 1f7f99f（弹确认框前先聚焦常驻的 Profile 触发钮）；dc1dfb4 把这一步收进共享 Menu（选中 / Esc 后焦点回触发钮），ProfileMenu 不再自己聚焦。单测与审计用例都用键盘走一遍，断言 Esc 后触发钮获得焦点且没有 PUT |
| 16 | panels.portal.access | 单测「剩余时间每 30s 刷新」偶发超时（5.5–6 秒，上限 5 秒）：fe93d86 让访问弹层走 Floating，开着时逐帧比对锚点；假时钟推进 1 小时同步跑约 22 万帧。生产环境只是每帧读一次锚点位置，不受影响 | 错乱（测试） | — | fbd7c8d（该用例在 useFakeTimers 之后把 requestAnimationFrame 换成空操作，约 15ms） |
