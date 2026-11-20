# UI 审计：各分区页面（sections）

范围：项目、空间、全局助手、收件箱、统计、产物（Artifacts）、匿名分享页、认证页。spec 在 `web/e2e/ui-audit/{projects,space,assistant,inbox,stats,artifacts,share,auth}.e2e.ts`。

说明：
- 「仅桌面 / 仅手机」的界面在 spec 里用 `only` 显式跳过另一类视口。
- 「滚到某处」类界面是页面级界面的下半部分（只滚页面自己的滚动区，不动外壳）。
- `assistant.*.mount-notice` 与 `assistant.thread.custom-message` 经线程 store 注入一条与 omp `xdev-mount-notice` 同形的在途消息（前者 `display: false`，后者 `display: true` 看可显示的长注入消息）；mock 的助手对话每轮也会按真实形态发这条消息。
- after 阶段的 mock 数据比基线多 4 个 artifact（长标题、无空格长串、emoji 开头、单字标题，分属另一个项目与无项目），before 截图里没有这些卡片。

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| `projects.list` | 项目列表 | 底栏 / 侧栏「项目」 | 页面 |
| `projects.new` | 新建项目 | 项目分区「新建项目」 | 页面 |
| `projects.page` | 项目页（顶部：项目设置） | 项目分区点某个项目 | 页面 |
| `projects.page.settings-bottom` | 项目设置下半部分（setup 脚本、保存） | 项目页往下滚 | 页面 |
| `projects.page.agent-config` | Agent 配置卡 | 项目页往下滚 | 页面 |
| `projects.page.github` | GitHub 卡（钩子、触发规则） | 项目页往下滚 | 页面 |
| `projects.page.ssh` | SSH 主机卡 | 项目页往下滚 | 页面 |
| `projects.page.accel` | 快照与预热卡 | 项目页往下滚 | 页面 |
| `projects.page.schedules` | 服务卡与定时任务卡（页底） | 项目页滚到底 | 页面 |
| `projects.page.repo-picker` | 仓库选择下拉 | 项目设置「从 GitHub 选择」 | 下拉 |
| `projects.page.trigger-dialog` | 新建触发规则对话框 | GitHub 卡「新建」 | 弹窗（手机为抽屉） |
| `projects.page.trigger-dialog.model` | 触发规则的模型池选择器 | 触发规则对话框「添加模型」 | 下拉 |
| `projects.page.schedule-menu` | 定时任务行菜单 | 定时卡某行「…」 | 菜单 |
| `projects.page.schedule-new` | 新建定时任务对话框 | 定时卡「新建定时任务」 | 弹窗（手机为抽屉） |
| `projects.page.schedule-edit` | 编辑定时任务对话框（含运行记录） | 定时卡某行「…」→ 编辑 | 弹窗（手机为抽屉） |
| `space.page` | Space 页（聊天 + 右列） | 侧栏「空间」下点某个空间 | 页面 |
| `space.side` | Space 右列（进度、定时、线程卡片；手机在「线程」标签） | 宽屏直接可见 / 手机切到「线程」标签 | 页面 |
| `space.menu` | 空间操作菜单 | 页头「…」 | 菜单 |
| `space.edit-dialog` | 编辑空间对话框 | 空间操作 → 编辑空间 | 弹窗 |
| `space.thread-menu` | 线程卡片菜单 | 线程卡片「…」 | 菜单 |
| `space.add-thread` | 加入线程（搜索候选） | 右列「加入线程」 | 面板 |
| `space.schedule-dialog` | 空间定时对话框 | 右列定时行「…」→ 编辑 | 弹窗（手机为抽屉） |
| `space.schedule-menu` | 空间定时行菜单 | 定时行「…」 | 菜单 |
| `assistant.landing` | `/assistant` 落到最近一个助手对话 | 底栏「全局助手」 | 页面 |
| `assistant.thread` | 助手对话 | 助手分区点某个对话 | 页面 |
| `assistant.thread.mount-notice` | 助手对话首轮前的 xd:// 设备挂载提示（只给模型看） | 在助手对话里发消息，模型首个 token 前 | 页面 |
| `assistant.main` | 助手主线 | 助手分区「全局助手」 | 页面 |
| `assistant.main.mount-notice` | 主线首轮前的 xd:// 设备挂载提示 | 在主线发消息，模型首个 token 前 | 页面 |
| `assistant.thread.custom-message` | 可显示的长扩展注入消息（折叠摘要） | 扩展 / 技能注入 `display: true` 的消息时 | 页面 |
| `assistant.thread.menu` | 对话操作菜单 | 页头「…」 | 菜单 |
| `assistant.thread.library` | 文件库 | 页头文件夹按钮 | 弹窗（手机为抽屉） |
| `assistant.main.notes` | 主线备忘 | 主线页头「备忘」 | 弹窗（手机为抽屉） |
| `assistant.main.reminders` | 主线提醒 | 主线页头「提醒」 | 弹窗（手机为抽屉） |
| `assistant.computer` | 助手的电脑（未唤醒空态） | 助手分区「助手的电脑」 | 页面 |
| `inbox.page` | 收件箱（按状态） | 侧栏「收件箱」 | 页面 |
| `inbox.by-project` | 收件箱（按项目） | 页头「按项目」 | 页面 |
| `inbox.bottom` | 收件箱滚到底 | 收件箱往下滚 | 页面 |
| `stats.usage` | 统计 · 用量 | 更多 → 统计 | 页面 |
| `stats.usage.tables` | 用量：按模型 / 项目表 | 用量页往下滚 | 页面 |
| `stats.usage.tables-bottom` | 用量：线程 / 用途 / 工具表（页底） | 用量页滚到底 | 页面 |
| `stats.usage.range-menu` | 范围菜单（窄屏） | 工具行「近 30 天」 | 菜单（仅手机；宽屏是分段按钮） |
| `stats.usage.custom-range` | 自定义范围对话框 | 范围「自定义…」 | 弹窗 |
| `stats.usage.export` | 导出菜单 | 工具行「导出」 | 菜单 |
| `stats.usage.budget` | 设置预算对话框 | 预算卡「设置预算」 | 弹窗 |
| `stats.usage.tooltip` | 主图浮层 | 点主图某根柱子 | 提示 |
| `stats.orbs` | 统计 · Orb | 统计页「Orb」标签 | 页面 |
| `stats.orbs.list` | Orb 列表 | Orb 页往下滚 | 页面 |
| `stats.orbs.drawer` | Orb 详情抽屉 | Orb 列表点某行 | 抽屉 |
| `stats.orbs.tooltip` | Orb 主图浮层 | 点主图某根柱子 | 提示 |
| `stats.output` | 统计 · 产出 | 统计页「产出」标签 | 页面 |
| `stats.output.tables` | 产出表（页底） | 产出页滚到底 | 页面 |
| `stats.output.tooltip` | 产出主图浮层 | 点主图某根柱子 | 提示 |
| `artifacts.list` | Artifacts 总览 | 更多 → Artifacts | 页面 |
| `artifacts.list.filter` | 筛选弹层 | 工具行漏斗 | 下拉（仅桌面） |
| `artifacts.list.display` | 显示弹层（排序、布局） | 工具行滑杆 | 下拉（仅桌面） |
| `artifacts.list.import` | 导入菜单 | 工具行「+」 | 菜单（仅桌面） |
| `artifacts.list.options` | 筛选与显示（窄屏合并菜单） | 工具行「筛选与显示」 | 下拉（仅手机） |
| `artifacts.list.card-menu` | 卡片更多菜单 | 悬停卡片右上「…」 | 菜单 |
| `artifacts.list.search` | 搜索结果 | 工具行放大镜 → 输入 | 页面 |
| `artifacts.list.bottom` | 总览滚到底（最近删除入口） | 总览往下滚 | 页面 |
| `artifacts.trash` | 最近删除 | 总览底部「最近删除」 | 页面 |
| `artifacts.trash.card-menu` | 回收站卡片菜单 | 回收站卡片「…」 | 菜单 |
| `artifacts.web` | 页面查看器 | 点页面类卡片 | 页面 |
| `artifacts.web.menu` | 查看器更多菜单 | 标题旁「…」 | 菜单 |
| `artifacts.web.history` | 版本历史侧栏 | 版本胶囊 / 历史按钮 | 面板 |
| `artifacts.web.comments` | 评论侧栏 | 版本胶囊 → 评论 | 面板 |
| `artifacts.web.comment-mode` | 评论模式 | 顶栏评论按钮 | 页面（仅桌面；手机无此按钮） |
| `artifacts.web.share` | 分享弹层（未发布） | 顶栏「分享」 | 弹窗（桌面为浮层，手机为抽屉） |
| `artifacts.web.presenter` | 演示模式 | 顶栏播放按钮 | 弹窗（全屏） |
| `artifacts.document` | 文档查看器 | 点文档类卡片 | 页面 |
| `artifacts.document.share` | 分享弹层（已发布） | 顶栏「已分享」 | 弹窗（桌面为浮层，手机为抽屉） |
| `artifacts.slides` | 幻灯片查看器 | 点幻灯片类卡片 | 页面 |
| `artifacts.canvas` | 画布编辑器 | 点画布类卡片 | 页面 |
| `artifacts.canvas.zoom-menu` | 画布缩放菜单 | 底部工具栏百分比 | 菜单（仅桌面：手机上画布只读，工具栏换成视图条） |
| `artifacts.canvas.sheet-menu` | 画布页面菜单 | 底部工具栏页面名 | 菜单（仅桌面：手机视图条只在多页时有此菜单，夹具只有一页） |
| `artifacts.canvas.layers` | 图层面板 | 底部工具栏图层按钮 | 面板（仅桌面：手机不显示图层面板） |
| `artifacts.open.gone` | 私有发布页已下线 | 打开已取消发布的 `/artifacts/open/<slug>` | 页面 |
| `share.snapshot` | 匿名分享页（快照） | 打开 `/s/<slug>` | 页面 |
| `share.live` | 匿名分享页（实时） | 打开实时分享链接 | 页面 |
| `share.role-snapshot` | 匿名分享页（role 回合头） | 打开 role 线程的分享链接 | 页面 |
| `share.gone` | 分享不存在 | 打开失效的 `/s/<slug>` | 页面 |
| `auth.login` | 登录 | 未登录时打开任意页 | 页面 |
| `auth.setup` | 首次设置 / 重新注册 Passkey | `/setup` | 页面 |
| `auth.pair` | 批准设备（输入配对码） | `/pair` | 页面 |
| `auth.pair.review` | 批准设备（配对详情） | 输入配对码 →「查看」 | 页面 |

## 问题记录

| # | 界面 id | 问题（具体到视口/主题/元素） | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | `assistant.thread.mount-notice`、`assistant.main.mount-notice` | 全局助手（主线与普通对话）每段第一轮前，omp 注入的 `xdev-mount-notice`（把挂载的二十多台 xd:// 设备连同内联文档一次列给模型，`display: false`）在模型首个 token 前作为在途消息渲染成一张大卡片；落盘后才被隐藏。全部视口、两种主题 | 冗余 | `shots/assistant.thread.mount-notice/d1920-light-before.png` | 1a3ebb0：在途消息同样按 `display: false` 过滤（信息只给模型，用户可在 `/tools` 报告与设备卡里看到设备清单）；mock 每轮按真实形态发这条消息 |
| 2 | `assistant.thread.custom-message` | 可显示的扩展注入消息（技能 / 扩展 `display: true` 的长清单）整段铺开在转录里，几十行占满一屏 | 冗余 | `shots/assistant.thread.custom-message/d1920-light-before.png` | 1a3ebb0、cb3d774：超过 6 行或 480 字收成一行摘要（类型徽标 + 第一行正文，跳过 `<system-notice>` 包裹行），点开看全文 |
| 3 | `artifacts.document.share`、`artifacts.web.share` | 桌面三个视口：查看器顶栏的分享弹层是手写 `fixed` 玻璃，外壳自己 `overflow-y-auto`：内容一滚，`::before` 的模糊底跟着滚走，下方页面文字透出；位置只在 resize 时重算 | 错乱 | `shots/artifacts.document.share/d1366-light-before.png` | 1e0c382：迁移到 ui/Floating（锚在分享按钮下右对齐、外壳不滚动、内层滚动、顶层不被面板裁切） |
| 4 | `stats.usage.export` | 导出菜单是手写 `absolute z-40` 玻璃菜单，挂在工具行里：不进顶层，靠近窗口底部时不会翻面 | 错乱 | `shots/stats.usage.export/d1366-light-before.png` | 1e0c382：迁移到 ui/Floating（右对齐、自动翻面、夹视口） |
| 5 | `artifacts.list.card-menu`、`artifacts.trash.card-menu` | 卡片右上角「…」的底座带 `backdrop-blur-sm`，成了 Backdrop Root：进顶层的菜单玻璃只取得到底座内部，菜单基本不模糊；全部视口 | 错乱 | `shots/artifacts.list.card-menu/d1920-light-after.png`（修前 after 结果 JSON 报「模糊被 Backdrop Root 截断」） | addfed1：底座换成不透明度更高的纯色底，去掉为旧式菜单抬层级的 z-20 |
| 6 | `space.menu`、`space.thread-menu`、`space.schedule-menu`、`assistant.thread.menu`、`projects.page.schedule-menu`、`artifacts.web.menu`、`artifacts.list.import` | 基线里 `Menu` 是 `absolute z-40` 的 `.glass.pop-in`，打开时自身在做 opacity 动画（模糊时有时无），宽屏菜单也可能被祖先 overflow 裁切 | 错乱 | `shots/space.menu/d1920-light-before.png` | 1bec702（主 agent：Menu / Popover 统一迁移到 ui/Floating，pop-in 只位移不淡入）；本区域 after 全部通过 |
| 7 | `stats.usage.tables-bottom`、`stats.orbs.list`、`stats.output.tables` | 宽屏两列明细卡等高拉伸：短表（按用途、按类型、按空间）被旁边的长表撑出 300～450px 的空白卡底 | 设计 | `shots/stats.usage.tables-bottom/d1920-dark-after.png`（修前） | fcef682：两列网格 `items-start`，卡片按内容高度顶对齐 |
| 8 | `projects.page` | 项目图标「自动」按钮旁的说明以「自动：」开头，同一行把「自动」写了两遍 | 设计 | `shots/projects.page/d1366-light-before.png` | 7cce431：说明去掉前缀（新增 `projects.iconAutoHintShort`） |
| 9 | `projects.list`、`projects.page`、`projects.page.*` | 打开项目页时主滚动区被拉到 ~900px（Agent 配置卡里的文件浏览器把选中的根标签 `scrollIntoView`，连带滚动纵向祖先）；1366 宽时连 overflow:hidden 的应用外壳都被推走，整页只剩侧栏 | 错乱 | `shots/projects.list/d1366-light-after.png`（修前） | b12b273（ThreadPanelsShellAudit：根标签只滚标签条自己）；属于本区域 after 期间引入的回归，基线没有 |
| 10 | `artifacts.list` 等 | mock 只有 4 个 artifact，标题都很短，看不出卡片标题截断、长串折行与 emoji 开头的对齐 | 设计 | `shots/artifacts.list/d1920-light-before.png` | cb3d774：补 4 个边界标题的 artifact（超长中英混排、无空格长串、emoji 开头、单字）；卡片标题单行截断、悬停可见全名，排版正常 |
| 11 | `stats.usage.tooltip`、`stats.orbs.tooltip`、`stats.output.tooltip` | 审计报「被 rect 遮挡 25/25」：图表浮层是 `pointer-events-none`，elementFromPoint 穿过它命中柱子；截图里浮层在最上层，显示正常 | 错乱 | `shots/stats.usage.tooltip/d1920-light-after.png` | 不修：工具误报，已在 harness 修正（主 agent：采样时临时给浮层打开 pointer-events） |
| 12 | `assistant.main.reminders`、`projects.page.schedules` | 非预设 cron（如 `0 8 * * *`）在定时行里原样显示表达式 + 时区（「0 8 * * * · Asia/Shanghai」），不如预设短语好读 | 设计 | `shots/assistant.main.reminders/d1920-light-before.png` | 不修：只有四个预设能翻成短语是既有设计（`lib/schedule.ts` 的 `describeSchedule`），任意 cron 转自然语言属于新功能而非排版 |
| 13 | `projects.page.trigger-dialog.model` | 对话框进入动画（约 200ms 淡入）期间点「添加模型」，模型弹层的玻璃祖先在做 opacity 动画，这一瞬模糊被截断 | 错乱 | `shots/projects.page.trigger-dialog.model/d1920-dark-after.png`（首轮报错） | 不修：只在对话框淡入的 200ms 内出现，用户不会在这段时间点到；Dialog 的进入动画归 ui/primitives（主 agent），spec 改为等对话框动画结束再点 |
| 14 | `assistant.thread`、`assistant.main`、`assistant.thread.*`（m390 最明显） | 助手回合右上角消息菜单的浮动槽：opacity 挪到「⋯」按钮后，槽位自己的描边 / 底色 / 阴影一直可见，留下一个空的描边小方块压在正文上（「再催一下」右侧）；基线没有 | 错乱 | `shots/assistant.thread/m390-light-after.png`（修前） | 9681c55（ThreadCoreAudit：底色、描边、阴影挪到按钮上随它显隐） |
| 15 | `projects.page.repo-picker`、`projects.page.trigger-dialog.model`、`artifacts.list.filter`、`artifacts.list.display`、`artifacts.list.options` | 基线的 `Popover` 是 `absolute` 的 `.glass.pop-in` 且自身 `overflow-y-auto`：列表一滚模糊底跟着滚走；打开时自身做 opacity 动画；在对话框里还会被对话框正文的 overflow 裁切 | 错乱 | `shots/projects.page.repo-picker/d1920-light-before.png` | 1bec702（主 agent：Popover 迁移到 ui/Floating，外壳不滚动、内层滚动） |
| 16 | `projects.page.schedule-menu` | 1920 宽：页底定时行的「…」菜单向下展开，超出视口（980 → 1118）并被项目页滚动区的 overflow 裁掉下半截 | 错乱 | `shots/projects.page.schedule-menu/d1920-light-before.png` | 1bec702（主 agent：Menu 进顶层，放不下时自动翻到上方） |
| 17 | `auth.login`、`auth.setup`、`auth.pair`、`auth.pair.review` | 认证卡 `AuthCard` 是 `.glass.pop-in`，进入时自身做 opacity 动画，淡入期间卡片背后的毛玻璃时有时无 | 错乱 | `shots/auth.login/d1920-light-before.png` | 1bec702（主 agent：`.glass.pop-in` 只位移不淡入）；认证卡本身无需改动 |
