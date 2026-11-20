# 区域：shell（应用外壳）

spec：`web/e2e/ui-audit/shell.e2e.ts`。侧栏类界面把侧栏 `nav` 当浮层根登记（断言遮挡并把分区滚动区滚到底再截一张）；手机上先点页头的侧栏开关打开下沉侧栏。

不在本区登记：
- WindowControls / ChromeBar（只在 Tauri 壳里显示）由 `host.e2e.ts` 用假宿主登记，见 `areas/host.md`。
- AuthCard、PairApproval（登录 / 首次设置 / 配对页）由 auth 区域（`auth.e2e.ts`）登记。
- 侧栏「新建」按钮是直接跳首页的按钮（没有菜单）；「新建空间」登记为 `shell.space-new`。

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| `shell.sidebar` | 侧栏·线程分区：品牌行（铃铛 / 收起 / 新建）、搜索入口、收件箱、需要你、空间、已置顶、项目分组、底部分区胶囊 | 启动后默认（手机：页头侧栏开关打开下沉侧栏） | 面板 |
| `shell.sidebar.folds` | 侧栏底部折叠组全部展开：72 小时未活动（全部归档）、稍后提醒、已归档；定时折叠行 nightly 展开 | 侧栏 → 点各折叠组标题 | 面板 |
| `shell.sidebar.collapsed` | 侧栏收起后的线程页（仅桌面：手机侧栏本来就是下沉的） | 侧栏品牌行「收起侧边栏」 | 页面 |
| `shell.sidebar.assistant` | 侧栏·全局助手分区 | 底部胶囊「全局助手」 | 面板 |
| `shell.sidebar.projects` | 侧栏·项目分区 | 底部胶囊「项目」 | 面板 |
| `shell.sidebar.more` | 侧栏·更多分区（设置入口列表） | 底部胶囊「更多」 | 面板 |
| `shell.sidebar.more.deep` | 侧栏·更多分区，当前分区在列表末尾（关于） | 直接打开 `/settings/about`（刷新或从别处跳入） | 面板 |
| `shell.row-menu` | 线程行上下文菜单 | 侧栏右键线程行；手机长按 | 菜单 / 底部动作面板 |
| `shell.space-new` | 新建空间对话框 | 侧栏「空间」组标题的「+」 | 弹窗 |
| `shell.sink` | 手机下沉侧栏（线程页上打开，卡片右移）（仅手机） | 线程页页头侧栏开关 | 抽屉 |
| `shell.notifications` | 通知铃铛弹层（18 条，长标题、各类 kind） | 侧栏品牌行铃铛 | 弹层（桌面）/ 弹窗（手机） |
| `shell.notifications.empty` | 通知空态 | 同上，没有通知时 | 弹层 / 弹窗 |
| `shell.toasts` | 通知 toast（右上，3 条：提问、完成、预算） | 前台收到通知时 | 提示 |
| `shell.toasts.actions` | 操作提示（底部居中：进行中、带撤销的长文案、出错） | 删除 / 导出 / 恢复版本等操作之后 | 提示 |
| `shell.server-errors` | 服务端错误提示（带「重新同步」与线程链接） | 订阅失败、侧通道应答被拒等 | 提示 |
| `shell.palette` | 命令面板（缺省列表） | 侧栏搜索入口；Ctrl/⌘K | 弹窗 |
| `shell.palette.results` | 命令面板长结果（线程、Artifacts、对话内容） | 命令面板输入「重」 | 弹窗 |
| `shell.palette.empty` | 命令面板空结果 | 输入不存在的词 | 弹窗 |

## 问题记录

| # | 界面 id | 问题 | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | shell.notifications | 通知铃铛弹层是侧栏里的 `absolute z-40` 玻璃块：伸出侧栏的部分在命中测试里被主内容区（`div.relative.isolate.flex.flex-1`）压住，25 个采样点里 15 个点不到；打开时淡入截断模糊 | 错乱 | `shots/shell.notifications/d1920-light-before.json` | fe93d86（迁到 ui/Floating：顶层、锚点定位，标题行固定、列表在内层滚动，玻璃外壳不滚） |
| 2 | shell.notifications | mock 只有 6 条通知、标题都不长，弹层滚动与长标题截断暴露不出来 | 设计 | `shots/shell.notifications/d1920-light-before.png` | fe93d86（mock 补 12 条更早的已读通知，含超长标题与各类 kind） |
| 3 | shell.notifications、shell.notifications.empty（m390） | 手机通知对话框自带标题，标题下还有一整行只放「全部已读」（左边空占位）；没有通知时仍显示一个禁用的「全部已读」 | 设计 | `shots/shell.notifications.empty/m390-light-before.png` | 19b1f00（「全部已读」移到对话框底栏，没有通知时不显示；桌面弹层不变） |
| 4 | shell.server-errors、shell.toasts.actions（m390） | 服务端错误提示（右下）与操作提示（底部居中）各自 `fixed` 在同一条底边，窄屏上两组都接近整屏宽，互相压住（16/25 采样点） | 错乱 | `shots/shell.toasts.actions/m390-light-after.png`（修前） | 85569ee（AppShell 新增底部提示栏：错误提示靠右在上、操作提示居中在下） |
| 5 | shell.toasts、shell.toasts.actions、shell.server-errors | 三类提示条都是 `.glass.pop-in`，出现时淡入，淡入期间玻璃没有模糊 | 错乱 | `shots/shell.toasts/d1920-light-before.json` | 1bec702（主 agent：`.glass.pop-in` 只位移不淡入） |
| 6 | shell.row-menu | 线程行上下文菜单的玻璃壳本身是滚动容器，并在打开时淡入 | 错乱 | `shots/shell.row-menu/d1920-light-before.json` | 1bec702（主 agent：ContextMenu） |
| 7 | shell.sidebar 等首页上的用例 | 页面进入时首页托盘（`home-tray`）淡入，托盘玻璃无模糊 | 错乱 | `shots/shell.sidebar/d1920-light-before.json` | 1bec702（归 home 区域，主 agent 已修） |
| 8 | shell.sidebar.more.deep | 直接打开列表末尾的设置分区（关于、设备等）时「更多」侧栏不把高亮行滚进来，1366 与手机上它停在折叠线下，看不出当前在哪个分区（SettingsAudit 转交） | 设计 | `shots/shell.sidebar.more.deep/d1366-light-before.png` | 12d3c47（路由变化时只滚本分区滚动区，底部让出底栏胶囊高度） |
| 9 | shell.sidebar.more | 列表顶端时最后一项「选区快捷动作」从悬浮底栏胶囊下面露出半行 | 设计 | `shots/shell.sidebar.more/d1366-light-before.png` | 不修：底栏是悬浮玻璃胶囊，列表从它下面透过是既定设计；滚动区底部留了胶囊高度 + 1rem，滚到底时最后一项停在胶囊上方（见 `-scrolled` 截图） |
| 10 | shell.sidebar | 桌面侧栏底部同样有列表从胶囊下透出 | 设计 | `shots/shell.sidebar/d1366-light-before.png` | 不修：同 #9 |
