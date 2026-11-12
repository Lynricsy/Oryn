# Amp 客户端界面还原：Oryn 客户端 UI 规格参照

**一句话结论**：最新版 Amp 的 UI 可以按截图还原到控件级，但视频一个都没能看，AVIF 图也没能看（原因见 0.2 / 附录 B），所以 Desktop 面板、Artifacts 面板、内嵌可视化、用量面板没有直接看到截图，只能引用官方 alt 文案。

**简称**：`S` = `https://static.ampcode.com`，`A` = `https://ampcode.com`。所有 `S/…` 链接都是我亲自用 `read` 打开过的图，除非标了「仅 alt」。

---

## 0. 前置

### 0.1 范围与口径
- 团队类只一两行带过，标 `[团队功能·暂不做]`；篇幅给单用户能力。
- 状态标签：`[现行]` / `[已移除]` / `[已被取代→新形态]`，后面带依据日期。
- 旧版 UI 的图单独标 **【旧版】**，不作规格。

### 0.2 取证能力的限制（先看）
| 资源 | 结果 |
|---|---|
| PNG / JPG / WebP | 可看 |
| AVIF（`news/*.avif`，含 less-noise、artifacts、boxes composer、profiles、self-hosted 示意图、android 三张等） | `read` 返回「unsupported」，只能引 HTML 的 alt 文案，标 `仅 alt` |
| MP4 视频 | `read` 只返回「Binary content」，没有预览网格；`:10s` 帧选择器返回 404，应该是本机没有 ffmpeg 可用。所有视频只能引 `aria-label`、页面文字和 poster |
| 个别 poster（`.jpg` 实为 WebP） | 可看，`meet-space-demo-poster.jpg` 看到了 |

### 0.3 大改版时间线（依据 A/chronicle 的发布日期，页面正文已核对的项另注）
| 日期 | 事件 | 改了什么 / 删了什么 |
|---|---|---|
| 2026-01-09 | Agents Panel | 多线程管理面板，后来被新侧边栏取代 |
| 2026-01-12 | TODOs Are Done | TODO 列表删除 |
| 2026-01-13 | Stick a Fork in It | Fork 命令删除；同日 Handoff 变成「对话里说 Handoff and …」 |
| 2026-01-15 | Tab, Tab, Dead | 编辑器内补全 Amp Tab 删除 |
| 2026-01-29 | Slashing Custom Commands | 自定义命令删除，改用 skills |
| 2026-02-19 | The Coding Agent Is Dead | 编辑器扩展（VS Code 等）下线 |
| **2026-05-06** | **Amp, Rebuilt（Neo）** | 重写 CLI：分布式 + 持久化 agent 循环 + Plugin API；Esc Esc 中断；排队/steer。删除 Handoff、编辑回滚、skill 管理命令、用户可调用 skill、主题、`$`/`$$` 手动 bash。权限默认放开，旧权限系统降为内置插件（来源 `A/news/neo`，我读了 "What's Gone" 段） |
| 2026-05-14 | npm 包改名 | `@sourcegraph/amp` → `@ampcode/cli` |
| 2026-05-27 | Drop the Neo | Neo 成为正式 Amp |
| 2026-06-02 | End of Public Threads | 公开线程结束 |
| **2026-06-04** | **Agents, Everywhere（新 UI）** | 新 UI 与新侧边栏，监控和驱动所有 agent；web、移动 web、`amp app`、CLI（`Ctrl+\` 侧栏）；团队线程列表搬到 **Activity**（来源 `A/news/agents-everywhere`） |
| 2026-06-16 | Diffs | 应用内审阅与逐块 stage |
| 2026-06-30 | Agents in Orbs | orb 登场 |
| 2026-07-08 | Agents, Anywhere | runner |
| **2026-07-09** | **The Dial** | low / medium / high / ultra 取代 smart / deep / rush / large（后者 deprecated） |
| 2026-07-17 / 07-20 / 07-21 / 07-22 | agent-to-agent / Puck + Slack / Schedule / Multiplayer | |
| 2026-08-04 / 08-06 | Attach Anything / Portals | |
| 2026-08-27 | So Long, TUI Sidebar | TUI 侧边栏删除，多线程管理交给 web 和原生 app（来源 `A/news/so-long-tui-sidebar`） |
| 2026-08-28 | iOS / macOS 原生 app | |
| 2026-08-31 | Space to Talk（Huddle） | |
| 2026-09-01 | Intelligently Ordered Diffs | |
| 2026-09-04 | Desktop | |
| 2026-09-08 | Steer, Don't Queue | 发送即 steer |
| 2026-09-11 | Restack | |
| 2026-09-17 / 22 / 24 | Runner 演进 | 一个 runner 服务多目录 / worktree / Mac app 即 runner |
| 2026-09-25 | Less Noise | 工具调用折叠 |
| 2026-10-06 | Many Many Pucks | 多个独立 Puck 对话 |
| 2026-10-16 | Runner Desktop | |
| 2026-10-22 / 28 | 自托管 orb | |
| 2026-10-27 | Android | |
| 2026-10-29 | Visualize This / macOS Satellites | |
| 2026-11-02 | Artifacts | |
| **2026-11-06** | **Spaces** | |
| 2026-11-09 | Runner Portals | |
| 2026-11-10 | Boxes | |

### 0.4 老版功能，不要复刻
| 功能 | 状态与依据 |
|---|---|
| Fork 命令 | `[已移除]` 2026-01-13 |
| Handoff 命令 / 按钮 | `[已移除]` 2026-05-06 Neo。新形态 `[已被取代→新形态]`：线程文档（2026-11-10）仍写「ask the agent to 'Handoff and …'」。**冲突**：Neo 说「gone」，现行文档只保留对话式。Oryn 若做，只做对话式，不做命令 / 按钮 |
| TODO 列表 | `[已移除]` 2026-01-12 |
| Amp Tab 补全、编辑器扩展 | `[已移除]` 2026-01-15 / 02-19 |
| 自定义 slash commands | `[已移除]` 2026-01-29，改为 skills / 命令面板 |
| 旧权限系统（`amp.permissions`） | `[已被取代→新形态]` 默认全放开，策略写成插件，2026-05-06 |
| TUI 主题、`$`/`$$` 手动 bash、编辑 / 恢复消息回滚文件 | `[已移除]` 2026-05-06 |
| TUI 侧边栏 | `[已移除]` 2026-08-27 |
| Agents Panel、Thread Map | `[已被取代→新形态]` 新侧边栏 + 子线程树。有截图佐证：`S/docs/orbs-test-agent-friendliness-thumbnail-v2.jpg` 里，侧边栏父线程 `Server startup/login` 带 `3/3` 计数，下面缩进挂 `↳` 子线程 |
| smart / deep / rush / large 模式名 | `[已被取代→新形态]` Dial 四档 2026-07-09 |
| 公开线程 / 社交主页 | `[已移除]` 2026-06-02 |
| 旧 CLI 的 Esc 以外的排队语义（Shift+Enter 排队） | `[已被取代→新形态]` 默认 steer，⌘/Ctrl+Enter 才排队（`A/docs/threads`，2026-11-10） |

**旧版 UI 图（仅供对照）**：
- 【旧版】`S/news/neo-ask-user-choice.png`：Neo 时代 TUI，已被新 UI 取代。
- 【旧版】`S/news/from-agent-to-agent-handoff.png`：TUI，2026-07-17。
- 【旧版】`S/news/diffs/open-in-browser.png`：TUI 命令面板。

---

## 1. Web / 桌面主界面

**依据图**：`S/docs/threads-thread-view.png`（浅色 web）、`S/docs/portals.png`（macOS 原生壳，浅色）、`S/docs/apps-mac-dark.webp`（macOS 深色）、`S/marketing/apps-linux-dark.webp`、`S/docs/apps-surface-light.webp`（Windows）、`S/docs/threads-activity.png`、`S/news/meet-space-demo-poster.jpg`。`[现行]` 2026-06-04 新 UI，后续迭代到 2026-11。

### 1.1 三栏分区
```
┌─侧边栏(~19% 宽)──┬─对话区(居中窄栏,max≈730/1568)──┬─右侧面板(~38%)────────┐
│ 工具条            │ 标题栏(…/Share/项目/模式)         │ 标签:Changes Portals   │
│ 线程列表(分组)    │ 转录(正文流,无 agent 气泡)         │       Files Terminal ⤢ │
│ 底部 Tab: Puck    │ 浮动小条(目录⇣)                   │ 工具条 + 内容           │
│ Activity Projects │ 建议回复 chips                    │                        │
│ ▦  账号行         │ 输入框                            │                        │
└───────────────────┴───────────────────────────────────┴────────────────────────┘
```
- 右面板**只在有执行环境（orb / runner）的线程出现**，用标题栏最右侧「面板开关」图标切换（`threads-thread-view.png` 的标题栏最右 ◧）。
- 对话区约 730px 的阅读列，两边留白。设置里有 **Thread Width** = Default / Wide / Full，命令面板有 *Set Width to Wide / Full*（`A/docs/markdown/threads`，2026-11-10）。

### 1.2 侧边栏
**顶部工具条**
- web / macOS（`apps-mac-dark.webp`）：红黄绿窗口按钮 → 账号头像堆叠（多账号）→ `@`（提及）→ 搜索 → 黑色圆形「新建」按钮。
- web 版（`threads-activity.png`）：侧栏开关 → `amp` 字标 → 搜索 → 过滤 → 黑色圆形新建。
- 原生壳（`portals.png`）：窗口按钮 → 侧栏开关 → 后退 / 前进 → 搜索 → 过滤 → 新建。
- Windows / Linux（`apps-surface-light.webp`、`apps-linux-dark.webp`）：左侧多出一条**窄图标栏**，从上到下依次是 Threads、Puck、(Spaces)、Activity、Projects，底部是 ▦ 应用网格与账号字母圆标 `A` `S`，每个账号一个。右边列标题为 `Threads`，带 `@`、搜索、新建。

**线程列表**
- **分组标题**：弱色小字 + 一条细线引导 + 右侧计数，如 `calc ———— 3`、`bugs ● 1`、`experiment 3`、`wait-for-reply 1`。
  - 标题后可带蓝点（该组有未读）。
  - 分组按 project（`threads-activity.png` 的 `calc` 前有项目图标）；`portals.png` 里的 `bugs / experiment / todo` 很可能是标签或自定义分组 `[推测]`。
- **线程行**：左 = **每线程一枚彩色圆形标记**，右上角叠一颗小蓝点表示**未读**。
  - 标记色用的是线程「Color」属性（More Actions → Color，「Color marks the thread in the sidebar and tints its title bar」，`A/docs/markdown/threads`）。
  - 状态用图标表达：旋转 spinner = 运行中；`apps-mac-dark.webp` 里是菱形图标，选中行高亮；Linux 图里 orb 线程是圆形 orb 图标，失败 / 错误是红色带叉图标（`Hello reply`）。
  - 中间是标题，超长末端渐隐；右侧是 hover 时出现的 **Pin / Archive** 图标（`S/docs/orbs-test-agent-friendliness-thumbnail-v2.jpg`）。
  - 多人参与时右侧挂参与者头像，有自动化的线程挂时钟 / 火箭小图标（`meet-space-demo-poster.jpg`）。
- **子线程**：缩进 `↳` + 图标，父行右侧显示 `3/3` 与折叠箭头（`orbs-test-agent-friendliness-thumbnail-v2.jpg`）。
- **底部固定 section**（列表末尾）：
  - `Inactive Last 72h  N [归档按钮]`，折叠；
  - `Snoozed  N`；
  - `Archived`。
  - 依据 `apps-mac-dark.webp`、`threads-activity.png`、`threads` 文档。
- **排序规则**：Pinned 在最前 → 按 project 分组的活跃线程 → 72h 无活动折入 Inactive。

**侧栏底部**
- **四个 Tab 按钮**：`Puck`（圆脸图标，有通知时右上蓝点，`portals.png`）、`Activity`（报纸图标）、`Projects`（文件夹堆）、`▦`（应用 / 视图网格）。当前 Tab 有圆角浅底高亮。
- **账号行**：头像 + `用户名 邮箱` + 上下 chevron（账号切换，`threads-activity.png`）。
- 在线 / 多人行：`Online ● 头像堆` 与 `Multiplayer` 入口，`[团队功能·暂不做]`（`meet-space-demo-poster.jpg`、`threads-activity.png`）。

### 1.3 顶部标题栏（对话列）
依据 `threads-thread-view.png` 与 `portals.png`，从左到右：
1. 侧栏开关（带蓝点 = 别处有未读）；
2. **状态图标 + 标题**（例如橄榄色旋转圆点 + `Orb Thread with Changes`）；
3. `…` **More Actions**；
4. 偏右：`Share`、**项目**（文件夹图标 + `calc`）、**模式文字**（`Medium` / `low`，弱色，是模式选择入口）、`Private` 小标签（可见性）、右面板开关。
- Win / Linux 额外有：🎧 Huddle 图标、`Views` 菜单（含 Artifacts，见 4.8；`apps-surface-light.webp`）。
- 线程文档说明：标题栏显示「title、More Actions、Share、project、agent mode」。

**More Actions 菜单**（`S/docs/threads-more-actions-menu.png`，窄浮层，圆角、行高大、右侧快捷键徽标）
- Copy Thread URL `Ctrl+Shift+C`
- Export ▸
- **Orb  Running ▸**（orb 状态 + 子菜单）
- **Cost & Usage**
- Pin `Ctrl+G i`
- Snooze Until Morning `Ctrl+G z`
- Archive `Ctrl+Shift+E`
- Rename
- Labels
- Color ▸
- Artifacts and Attachments
- Delete（红色）
- Report Bug

`Cost & Usage` 点开后的界面我没有找到截图，见 7.3。

### 1.4 Activity 页（个人线程总览）
`S/docs/threads-activity.png`，URL 同 `A/feed`，筛选写进 URL（如 `/feed?time=7d&q=label:bug`）。
- 左列过滤器，用大写小标签分段：
  - SEARCH（搜索框，查询语法同 agent 的 find threads）
  - TIME：Last 24h / 72h / 7d / All time
  - STATUS：All / Active / Archived / Snoozed / **Committed**
  - USERS：All 6 / thorsten 6（计数右对齐）
  - PROJECTS：All 6 / `@amp/calc` 6
- 右列 THREADS：日期分组（`Yesterday`），行 = 头像 + 标题 + `用户 · 23h ago · 项目`。
- 选项上右键 / 长按 → **Use … as Default** 存本浏览器；`Reset Default`（`A/docs/markdown/threads`）。

### 1.5 项目页
`S/docs/projects-project-page.png`：面包屑 `Projects / [A]amp / [C]calc`，右上 `Clone`、齿轮、新建。
- 一行描述。
- 两列卡片：`Commits to main`（头像、标题、作者 · sha、`4h`）与 `Recent Threads`（头像、标题、作者 + `16 messages`、时间），各带 `View All`。
- 下面 `Files`：`Commits / Branches / Tags` 三个文字按钮；最新 commit 行 + 文件树（文件夹 / 文件图标 + 最近一次 commit 信息 + 时间）。

**项目设置对话框**（`S/docs/projects-settings-overview.png`）
- 左 Tab：Overview / Webhooks / **Orb** / Secrets & Env Vars。
- Overview 字段：
  - Name（`amp / calc`）
  - Project Icon（`Set Icon`）
  - Description
  - Git Repository（只读 URL + 复制 + Clone + ⋯）
  - **Ship Behavior**（下拉，`Ship`；"Choose the main action shown in the changes sidebar"）
  - **Commit Author**（`Thread Creator`）
  - Public Read-Only Code Access（开关）
  - `Save`（未改时灰）

---

## 2. 新建线程输入区 / 对话框

**依据图**：`S/docs/orbs-new-thread.png`、`S/docs/orb-size-picker.png`、`S/docs/projects-composer-picker.png`、`S/docs/the-dial.png`、`S/docs/the-dial-raw-models.png`、`S/docs/runners-*-light.png`、`S/docs/macos-and-ios-runner-picker-dark.png`。`[现行]`

### 2.1 组成
- **全屏式 / 居中大卡片 composer**。左上 `✕ Close`，右上有时出现 `Insert Current Thread ^I`（`the-dial.png`）。
- **文本区**：占位符 `Write prompt…`，左下回形针 📎 附件。
- **底部「选项条」**（比卡片本体略深一层的灰条）：从左到右是一排**胶囊（pill）**，每个都带快捷键徽标，然后是 `⋮` 更多，最右是 🎤 听写 + **黑色圆形发送按钮**（`→`；空时灰色）。

### 2.2 三枚核心胶囊（带快捷键）
| 胶囊 | 内容 | 快捷键 | 视觉 |
|---|---|---|---|
| 执行位置 | `New Orb`（spinner 图标）/ `Orb` / runner 名如 `mac-mini`（显示器图标） | `^M` / Ctrl+M | 白底描边 |
| 项目 / 目录 | `calc`（文件夹图标）或带项目图标的 `amp` | `^J` / Ctrl+J | 白底描边 |
| 模式 | `medium`（仪表图标） | `^S` | **按档位着色**：medium 绿底绿字、high 蓝底蓝字、ultra 紫底紫字（`orbs-new-thread.png`、`the-dial.png`、`macos-and-ios-runner-picker-dark.png`） |

### 2.3 `⋮` 更多菜单
（`orb-size-picker.png`）
- `Labels  ^L`
- `Orb Size  a1.xxlarge ▸` → 子菜单：
  - `a1.tiny` 1 CPU·2 GB
  - `a1.small` 2 CPU·4 GB
  - `a1.medium` 4·8
  - `a1.large` 8·16
  - `a1.xxlarge` 16·32，带「Project Default」标签；当前选中是实心圆点。

### 2.4 执行位置选择器
（`runners-shared-picker-dark.png`，标题 `Choose where to run…  Ctrl+M`）
- 分段：`Orbs`（行 `Orb`，✓）/ `Runners`（`devbox  ~/amp · 0 threads`）/ `Shared Runners`（头像 + 名 + 属主 + 目录 + `2 threads`，`[团队功能·暂不做]`）。
- 本机 runner 打 `This Mac`（MacBook 图标）徽章，副行 `Hamishs-MacBook-Pro.local · 3 projects · 0 threads`（`macos-and-ios-runner-picker-dark.png`）。
- 自托管 orb provider 出现在 `Self-Hosted` 分组（仅 alt：`S/news/self-hosted-orbs-editor-light.avif`）。
- Box 的 app 勾选（仅 alt：`S/news/boxes-composer-v2-light.avif`）：Executor picker 里 `Boxes` 下面有 `acme/workbench`、其 app `bug-tracker` 勾选，下面是 `Machine directory` 与其他 app。

### 2.5 项目 / 目录选择器
- 项目模式（`projects-composer-picker.png`）：搜索 `Choose a project…  Ctrl+J`；`No Project`；`Projects` 组里每行 = 名称 + `Amp`（归属）徽标 + `amp/calc` + ✓；`Actions` 组 → `Create Project`。
- runner 模式（`runners-directory-picker-light.png`，`Choose a project or folder…`）：
  - `Projects`：名称 + 等宽路径 `~/code/amp` + 来源徽标 `GitHub` / `Amp`；
  - `Folders`：`code / dotfiles / scratch`。
- 新建目录 / 项目：输入 `playing-with-jev` 会出现 `New Directory`、`New Project` 两行（`runners-create-composer-light.png`，仅 alt）。
- **Worktree**（`runners-worktree-picker-light.png`、`runners-worktree-form-light.png`）：
  - 高亮目录行右侧出现**分段控件 `Current | New Worktree`（Tab 切换）**；
  - 已存在的 worktree 目录名后有分支图标（`amp-fix-login ⑂`）；
  - 选 New Worktree 后进入表单页：`← New worktree of amp` → `Name`（建议名取自 prompt）→ `Directory ~/code/amp-fix-flaky-login-test` / `Branch` / `From Current HEAD` →「Uncommitted changes stay in the original checkout.」→ 黑色 `Create Worktree` 按钮。
  - 注意：创建线程时才在 runner 上执行 `git worktree add`。

### 2.6 模式选择器（The Dial）
`S/docs/the-dial.png`：
- 搜索 `Choose a Mode…`；右上分段 `Amp Modes | Raw Models`（Tab 切换）+ ⇥ 图标。
- 上半是**拟物 Dial 面板**（深色金属拉丝）：
  - 旋钮：LOW / MED / HIGH / ULTRA；
  - 右上 `FAST` 拨动开关；
  - `$` 小窗口；
  - 点阵 LCD：`AGENT GPT-5.6 SOL X-HIGH / ORACLE GPT-5.6 SOL HIGH`；
  - 底部 LED + `CHATGPT SUBSCRIPTION CONNECTED`。
- 下半是列表：`ultra`「The most capable mode for hard, open-ended tasks」/ `high`「Deep reasoning for hard tasks」✓ / `medium`「Balanced…」/ `low`「Fast, low-cost…」。
- `Raw Models`（`the-dial-raw-models.png`）：模型列表（标题 + 说明小字），选中行右侧 `Normal | Fast · 2x  Alt+R` 分段。CLI 里同样用 `Ctrl+S` 开拨盘（`S/docs/the-dial-cli-dial.png` 为 CLI 图，未单独看）。
- 注意：Dial 的模式名、型号会变，Oryn 应做成可配置。

### 2.7 其余
- **听写**：🎤 在选项条右侧、发送钮左侧。
- **附件**：📎 在文本区左下。
- **Profile**（浏览器 profile）：不在 composer 本体上；见 4.5 Desktop 工具栏 profile 菜单，以及「让 agent 选 profile」（`A/docs/orbs/profiles`）。
- **Mode / Fast**：见上。
- **Set Up Orbs 横幅**（`orbs-new-thread.png`，外框是紫蓝渐变的营销装饰）：卡片下方一行「**Set Up Orbs** · Amp can optimize this project to run in orbs · `Dismiss` `Set Up with Amp`」。
- 创建项目对话框（`S/docs/orbs-new-project.png`）：
  - `Git Repository` 两张大选择卡：`Start From Scratch`（A blank repository managed by Amp）/ `Use an Existing Repository`（Connect code from GitHub or another remote）；
  - Repository：黑色 `Connect GitHub` 按钮 `or` URL 输入框；⚠ 提示「If this repository is private, connect GitHub first…」；
  - Project Name；Owner 两张卡（Workspace / Private）；`Add Description`、`Add Additional Repositories` 次级按钮；`Cancel` / 黑色 `Create Project`。

---

## 3. 对话区

**依据图**：`threads-thread-view.png`、`portals.png`、`apps-mac-dark.webp`、`apps-iphone-dark.webp`、`apps-android-dark.webp`、`apps-linux-dark.webp`、`meet-space-demo-poster.jpg`、`orbs-portals-in-orbs-thumbnail.jpg`、`orbs-add-a-dev-sign-in-thumbnail-v2.jpg`。

### 3.1 消息
- **用户消息**：右对齐圆角气泡，浅色主题为淡绿底（`portals.png`），深色为灰深底（`apps-mac-dark.webp`）；宽度随内容，最大约 60%。
- **Agent 消息**：**没有气泡**，直接是正文流，包含标题、段落、列表、表格（带细线）、行内 code、代码块（浅灰 / 深灰圆角块，等宽字体，圆角约 6px）。
- 多人 / @ 提及消息：蓝紫淡底气泡，含 `@lewis-metcalf` 高亮；消息上方有 `Tim Lucas 6h ago` 小字（`meet-space-demo-poster.jpg`，`[团队功能·暂不做]`）。

### 3.2 工具调用折叠摘要
依据 `S/news/less-noise-dark.avif`（仅 alt：「Ten separate rows of commands, reads, searches, and edits become one expandable summary. Both are followed by the same assistant reply, fading at the second line.」，2026-09-25）与实际截图：
- **一轮的多条工具调用合并成若干条弱色摘要行**，不是逐个展开（`portals.png`）：
  - `Explored 2 files, 3 searches`（灰）
  - `$ printf '%s\n' '--- .agents/setup ---'; cat .agents/setup; printf…`（等宽，单行截断，`…`）
  - `Edited 2 files +55`（`+` 绿 `-` 红）
  - `Ran 4 commands`
  - `Edited directory_server.py +21 -4`
- 摘要行**无边框、无图标**，只有文字 + 彩色增删计数，点击展开。展开后的样子没有从截图直接看到，`[推测]` 是命令输出 / 文件改动明细。
- **Show Work ›**：`apps-mac-dark.webp`、`apps-android-dark.webp` 里，一轮工作被折成居中的横线 + 文案 `Show Work ›`，在用户消息和最终回答之间。
- `Worked for 9 minutes ›`：`meet-space-demo-poster.jpg` 里同一概念带耗时。
- 工具名称在深色 / 浅色里都保持**弱灰**，最终答案是**正色**——视觉层级靠「弱化过程、强化结论」。

### 3.3 Thinking / diff / 终端 / 可视化
- **Thinking**：截图里没有出现独立的 thinking 块，未找到。
- **Diff**：两处。
  - Changes 面板里的内联 diff（见 4.1）；
  - 转录里内嵌 diff 的形态没有截图，未找到。
  - 参考 `S/news/diffs/amp-diff.png`（June 2026，仍是现行算法）：行号列 + 红 / 绿整行底色 + 左侧彩条；**重复块检测**（被移动的代码块用灰底而不是红 / 绿）；顶部有展开上下文的 ⌃⌄ 箭头；函数签名作为 sticky 上下文行 `async function createThreadDependencies(`。
- **内嵌终端**：独立在 Terminal 面板；转录里只显示命令摘要。未找到内嵌终端的截图。
- **内嵌可视化**（Visualize This，2026-10-29）：仅有文字（"charts, timelines, diagrams, and mockups right in the thread"），截图未看到。Artifacts 的内嵌图表：仅 alt（`S/news/artifacts-blocking-waits.avif`：右侧 Artifacts 面板里一个柱状图，「3.9k for polling today down to 978 for an async watch」）。
- **行内链接 chip**：`Open it here: [▲ Portal ↗]`（`portals.png`）；`[▲ Open Bulletin ↗]`、`**Open Bulletin as admin ↗**`（`orbs-add-a-dev-sign-in-thumbnail-v2.jpg`）。Portal 链接是带小图标的圆角 chip，外链箭头。
- **建议回复 chips**（`apps-mac-dark.webp`）：输入框正上方一行，从左到右：
  1. `[文件图标 +38 −20]` 变更摘要 chip；
  2. `[🚀 Ship ▾]` 分体按钮（默认动作 + 下拉）；
  3. `[⬆ "Ship it"]`；
  4. `[⬆ "Add a test for the sort order"]`；
  5. `[⬆ "Show me the diff"]`。
  - 带 ⬆ 圆形图标的 chip 点击即发送；移动端窄屏横向滚动，并在两端各留一个图标钮：左边变更图标（带蓝点），右边 `☰` 目录。
  - Win / Linux 变体里第一个 chip 是 `Archive`（`apps-linux-dark.webp`），说明首个 chip 是「**线程收尾动作**」，随状态变（有改动 → 变更摘要 + Ship；没改动 → Archive）。

### 3.4 队列 / steer 指示
- 文档（`A/docs/markdown/threads`，2026-11-10）：agent 工作时发送的消息**直接 steer**；`⌘/Ctrl+Enter` 改为排队；`Esc Esc` 中断。
- 视觉上：未找到队列 / steer 的 UI 截图，视频 `neo-queueing-1.mp4` / `neo-steering-1.mp4` 是 TUI 旧版，也没看。

### 3.5 浮动导航
- 输入框上方右侧一个小浮条：`☰`（目录 / TOC）+ `⬇`（回到底部，**带蓝点 = 有新内容**）（`threads-thread-view.png`）。`[推测]` 目录列出用户消息。
- 多人状态下的 `You're in chat mode. Tag @amp to get back to hacking.` 是输入框内的灰色提示条（Space 里，见 5.1）。

### 3.6 Orb 状态条（启动中 / 排队 / 暂停）
- 状态入口只有：标题栏左侧的状态图标（旋转 / 完成 / 错误）+ More Actions 的 `Orb  Running ▸`。
- 启动中、排队、暂停的专用状态条：**未找到截图**。`[推测]` 在转录里是一条 `Started orb` 之类的系统行（`apps-linux-dark.webp` 里转录开头有 `Started orb` 标签）。

---

## 4. 右侧面板

所有面板共享同一个标签栏：`[Changes] [Portals / Portal] [Files] [Terminal]`，右端 `⤢` 全屏。标签有**小蓝点**表示有新内容（`portals.png` 的 Changes 标签）。Space 与 Desktop 是额外的标签（`orbs-portals-in-orbs-thumbnail.jpg` 标签栏为 `Changes ▾ | Portals | Files | Terminal | Space | Desktop`，`meet-space-demo-poster.jpg` 为 `Changes Portals Files Terminal Space`）。手机上它们变成顶部可滚动标签（见 6）。

### 4.1 Changes `[现行]`
**依据**：`threads-thread-view.png`、`S/docs/orbs-setup-files-changes.png`。
- **工具条**：`🚀 Ship`、`☑ Review`、`⟳ Sync ▾`；右侧总计 `+763 -40 ~10`（绿 / 红 / 黄）、**文件排序按钮**（`A↓Z` 图标，有未应用的新排序时带蓝点）、`⋮`。
  - Ship 的默认动作由项目 `Ship Behavior` 决定（`projects-settings-overview.png`）。
- **文件列表**：分组标题 `UNSTAGED`（大写小字灰色）。每行：`›` 折叠箭头 + **文件名（粗）+ 目录（灰）** + 增删计数（`+66`、`+30 -5 ~5`）+ **状态字母徽标**（方框描边：`A` 绿、`M` 琥珀、`D` 红）+ `…` + **⊕ stage 圆钮**。
- **展开后**：行号列 + 整行绿底（新增文件）+ 左侧 2px 彩条，行内换行；多个文件上下排列（`orbs-setup-files-changes.png`）。
- **智能排序**（`A/docs/markdown/threads`）：把最能解释改动的文件放顶部，生成代码等变灰；>500 文件或只读时按字母序；排序变化后不自动重排，而是在排序按钮上点一颗蓝点，点 `Apply Updated File Order` 才应用。
- **commit 选择器**（文档描述，无截图）：面板**底部**一个按钮，文案如 `All Changes · 3 commits ahead of origin/main`；弹出：`All Changes` / `Uncommitted` / 每个 commit（短 SHA + 标题，新的在上，悬停或 → 键看完整消息）/ `Compare With Branch…`。
- **Restack**：见视频 `S/docs/orbs-shipping-restack-working-thread.mp4`（aria-label「Restacking changes, then reviewing the resulting commits」），我没能看视频，入口位置未确认。
- **Sync**：`Sync ▾` 下拉；命令行等价 `amp sync`；More Actions 里 `Orb → Copy Sync CLI Command`。

### 4.2 Files `[现行]`
- 标签：`Files`（树图标）。
- 文档：浏览 orb / runner 文件系统，拖放电脑文件到面板即上传（`A/docs/markdown/threads`）。
- **树、预览、上传按钮的截图：未找到**。

### 4.3 Terminal `[现行]`
- 标签：`Terminal`。文档：在执行环境里开 shell；粘贴 / 拖入文件会上传并插入路径；orb 内是共享 tmux 会话，agent 能读其输出（`A/docs/markdown/orbs`）。
- 截图未找到；视频 `S/docs/agents-in-orbs-demo-terminal.mp4` 未看。

### 4.4 Portal `[现行]`
**依据**：`S/docs/portals.png`。
- 标签名在单个 portal 时是 `Portal`，多个时 `Portals`。
- **地址栏**：`‹ › ⟳` + 盾 / 锁图标 + URL 文本（`https://t-03gptwp2habyuyd86e5dummew-p21349.onamp.dev/`，主机名 `t-<thread>-p<port>.onamp.dev`），右侧三个图标：复制 / 书签类、在新窗口打开 ↗、齿轮（`⚙` 里应是 Access 等，文档说 Access 菜单，具体项 `[推测]`）。
- 内容区是被代理的页面本身（示例是 Python 目录列表，保持原站样式，不套壳）。
- 右下角悬浮圆形聊天按钮 = 反馈小组件（`A/docs/markdown/feedback-widget`）。
- **审阅 widget（标注发给 agent）**：只有 `S/orbs-portals-in-orbs-thumbnail.jpg`：被选中元素有**橙色描边**，旁边弹出小卡片：灰字 CSS 路径 `html.dark > body.min-h-screen.bg-white > ui-s…`，文本框，`Cancel` / 🎤 / `+ Add`。顶部另有书签图标，`close the loop` 的反馈入口（`A/news/close-the-loop`，2026-11-06）。视频 `S/docs/portal-review-comments.mp4` 未看。
- 地址栏之外的 portal 链接列表：未找到截图。

### 4.5 Desktop `[现行]`
- 2026-09-04 上线，2026-10-16 加入 runner 桌面共享。
- **依据**：`S/news/runner-desktop-settings-dark.png`（macOS 设置页，非面板本体）、`S/news/let-the-orb-browse-profiles-silkcards-orders-dark.avif`（仅 alt）。
- 面板内容（仅 alt）：Desktop 标签展示 orb 的浏览器页面，**profile 菜单**弹出：`Ordering Swag is signed in to 2 sites`，下面列 `Work`、`Booking Travel`、`Office Supplies`、`New Profile`、`No Profile`。
- 工具栏的其他按钮、接管提示：未找到截图。

### 4.6 Space 标签 / Huddle
- Huddle 面板：`[团队功能·暂不做]`。`meet-space-demo-poster.jpg` 仅作布局参考：右侧面板变成视频网格，每个瓦片左下角名字标签，底部居中浮动控制条（🎤 ⌃ / 📷 ⌃ / 屏幕共享 / 红色退出）。

### 4.7 Space 面板（非 Space 页）
- 线程里的 `Space` 标签是线程所属 Space 的聊天入口，见 5。

### 4.8 Artifacts / Views `[现行]` 2026-11-02
- 入口：标题栏 `Views`（Win / Linux 截图可见；web 为 `Views → Artifacts`），More Actions 也有 `Artifacts and Attachments`。
- 面板（仅 alt，`S/news/artifacts-*.avif`）：线程右侧 `Artifacts` 面板，文档可编辑（列表、勾选、表格、代码、图片、交互视图），面板内 `Ask Amp` 让 agent 改。
- 无截图可看，细节未找到。

---

## 5. Space 页与 Puck

### 5.1 Space（单人形态 = Shared with「Only You」）`[现行]` 2026-11-06
**依据**：`S/news/meet-space-demo-poster.jpg`（已看，注意这张是 2026-08-31 Huddle 公告的 poster，Space 标签已在其中）；`S/news/spaces-overview-v3.mp4`（未看，aria-label 描述：聊天里出现 3 个**实时线程卡片**）；`S/news/spaces-*.avif`（仅 alt）；`A/docs/markdown/spaces`（2026-11-11）。
- **布局**：左侧边栏 `Spaces` 分组（`+` 新建）→ 中间 **Space 聊天** → 点线程卡片后，**线程在聊天旁边打开**（分栏）。
- **聊天**：消息 + agent 回复 + **线程卡片**（实时状态、是否需要人）+ 顶部 Goal 下的进度图。
- **进度图**（`A/docs/markdown/spaces` 的 Track progress）：最近 30 天；点某天列出当天线程；每个线程计入 `Shipped`（commit 到默认分支 / PR 合并 / Ship 归档）/ `Archived` / `Open`（折线显示每天结束时的 open 数）；仓库托管方无法检测 commit 时只显示 `Done`。图的具体视觉（柱 / 折线 / 颜色）只有 alt 级描述（`spaces-v1-space-light.avif`：「side panel lists the space's threads by status」），**没有截图可看**。
- **创建表单**字段：Name / Goal / Project（可选 `No Project`）/ Shared with / Slack Channels / `Create Space`；CLI `amp spaces create`。
- 聊天与「hacking」两种模式：输入框上方灰条 `You're in chat mode. Tag @amp to get back to hacking.`（`meet-space-demo-poster.jpg`）。
- 团队相关（评论 / @ 提及 / 转移所有权 / Slack 频道 / Huddle）：`[团队功能·暂不做]`。

### 5.2 Puck
- 入口：侧栏底部第一个 Tab `Puck`（圆形双眼图标，`threads-activity.png`），有通知时右上蓝点（`portals.png`）；移动端侧边栏里有 `Puck` 按钮。
- **Puck 面板的截图：未找到**。
- 视频 `S/docs/amp-puck-demo.mp4`（aria-label「Opening Puck from ampcode.com」）、`S/news/amp-puck-demo.mp4` 没能看。
- 图标：`S/news/puck-icon-light.webp?v=2` 我没打开；`S/news/puck-floating.jpg`（我看了）是 Meet Puck 的**题图**，一个灰色圆形小角色漂浮在黑色圆球与橙绿烟雾里，是品牌插画，不是 UI。
- 文档层面（`A/docs/markdown/apps/ios`、`apps/android`）：iOS 的 `Dictate to Puck` / `Call Puck` 快捷指令；Android 长按图标的 `Dictate to Puck` / `Call Puck` / `New thread` + Quick Settings tile；分享到 Puck（图片 / 文本 / 文件）。

---

## 6. 移动端（iOS / Android）

**依据**：`S/docs/apps-iphone-dark.webp`、`S/docs/apps-android-dark.webp`；Android 三张 `S/news/amp-on-android-{thread,portal,changes}-{light,dark}.avif` 仅 alt；文档 `A/docs/markdown/apps/ios`、`apps/android`。`[现行]`

### 6.1 iPhone
- **顶栏**（iOS 26 液态玻璃风格的胶囊分组）：左 ◧ 侧栏按钮；中间标题胶囊（标题 + 下面一行 `📁 amp` 项目名）；右 `分享` 与 `▦` 视图 / 面板网格。
- 转录：用户气泡（深灰圆角）、agent 正文、表格（带细线圆角外框）。
- **底部**：建议 chips 一行（`[变更图标·蓝点] [🚀 Ship ▾] [⬆"Ship it"] [⬆"Add a test for the sort o…"] … [☰]`，可横滚），其下输入条：`+`（附件 / 更多）圆钮、`Message` 胶囊输入框、🎤 圆钮。
- 面板（Changes / Files / Terminal / Portals 等）：iPhone 上入口是顶栏 `▦`，具体呈现形态无截图 `[推测]`。

### 6.2 Android
- **顶栏**：◧ 侧栏、标题（单行）、orb 状态图标、分享、`⋯`。
- **紧贴顶栏下面一条可横滚的标签条**：`[Agent] [Changes · 蓝点 ▾] [Files] [Terminal] [🖥 Desktop…]`——**桌面端的右侧面板在手机上被压成「同级标签」**，`Agent`（对话）是第一个标签。
- 文中还有 `Show Work ›` 折叠、同款 chips 与 `+ / 输入 / 🎤`。
- Android 三张图 alt：线程页、`Portals` 标签里的幻灯片、`Changes` 标签里的 `present.js` diff，所以 Portals / Changes 在手机上都是整屏标签。

### 6.3 通知（文档）
- 「thread is ready for your next message」时推送，点开线程；正在看的线程不推送；iOS 在 App Settings（侧栏里）选推送类型，可在前台时静默；Android 在 `/settings/notifications` 与系统设置里控制。
- 通知的视觉样式（标题 / 正文格式）：未找到。
- `ampcode.com/threads/...` 链接用 universal link 直接打开 app。
- iOS 通话类 Puck 以系统电话形态出现在状态栏和锁屏。

---

## 7. 设置与用量统计

### 7.1 设置页
- **个人 vs 工作区**：设置弹窗顶部分段 `Personal Settings | Workspace Settings`（`S/docs/workspaces-overview.png`，工作区部分 `[团队功能·暂不做]`）。
- 工作区设置左栏条目（可作为设置项分区的参考，个人设置 `[推测]` 是子集）：Workspace / Members / **Usage** / Billing / Model Routing / MCP / Domains / **Secrets & Env Vars** / External API Applications / Plugins / Skills / Agents / Integrations / Advanced ▸；底部 `Help & Community`（Docs / @AmpCode / Email Support）。同一张图里有 `Balance $100 USD`、`Subscriptions`、`Allow Users to Use Orbs` 开关（计费相关，Oryn 不做）。
- 从 HTML 抓到的外观设置键（`A/docs:raw` 的 `<html data-*>`）：
  - `data-amp-theme` = theme-0…theme-6（7 套底色，见 8）
  - `data-amp-color-scheme` = system / light / dark
  - `data-amp-terminal-theme` = app
  - `data-amp-terminal-palette` = gruvbox
  - `data-amp-text-size` = default
  - `data-amp-ui-sounds` = on
  - `data-amp-logo-motion` = on
  - `data-amp-color-filters` = off
  - 加上 `Thread Width`（Settings > Appearance）。
- 其他设置路由（出现在文档中）：`/settings/notifications`、`/settings/experimental-features`（含 `desktop-fun-games`，`A/news/desktop`）、Settings → Mode Dial（含 Spaces 的模型）、Model Routing。
- **macOS app 设置窗**
  - 2026-10-16 版（`S/news/runner-desktop-settings-dark.png`）：Tab = General / Notifications / Updates / **Runner** / About。Runner 页：
    - `Use This Mac as a Runner`（总开关）
    - 状态点 `Add directories to get started`
    - `Runner Name  quinns-mac ✎`
    - **`Share This Mac's Display`**（开关；说明「Threads running here can show and control this Mac's screen in their Desktop tab.」；打开后出现绿点 `Sharing`「Threads running here show this Mac in their Desktop tab.」，被蓝框高亮）
    - `Allow Terminal Access`
  - 较新版（`S/docs/macos-runner-settings-per-account.png`，2026-11）：Tab = General / **Runner** / **Imports** / About。每账号一张卡片：
    - `Run Threads on This Mac for <email>` 开关；
    - `Status  Running · 3 folders · idle ⟳`；
    - `Runner Name`；
    - `Share This Mac's Display` / `Allow Terminal Access` / `Share With Workspace`（`[团队功能·暂不做]`）/ `Share This Mac's Network Connection`（"Orbs browse the web through this Mac."）；
    - `Folders  +`（列表：文件夹图标 + 名 + 路径 + `⋯`）；`New Project Folder  Change…`；
    - `Logs  [Show Runner Log] [Show CLI Log]`；
    - 全局区 `All Runners` → `Keep This Mac Awake`（While plugged in and the runner is on）。
  - 注意两版 Tab 不同，以后者（较新）为准。
- **自托管 orb provider**：`Add Orb Provider` 对话框（名称，如 `our-k8s-orbs`；token `aop_live_…` 只显示一次），`Edit Orb Provider` 里有 `Repository Setup` 开关（仅 alt：`S/docs/self-hosted-orbs-*.png`）。这类配置对 Oryn 的「自有 orb provider」很相关，但**没有看图**（`S/docs/self-hosted-orbs-add-provider-light.png?v=3` 我没打开）。

### 7.2 用量 / 状态类图表
- **Box 页**（`S/news/boxes-box-page-v2-light.png`，**已看，2026-11-10，`[现行]`**）：
  - 右侧信息栏 `LAST 21 DAYS`：**绿色竖条柱图**（每天一根 = 线程数），柱图下方一条**细分段线 = 在线率**；图例右侧 `■ 40 threads`、`▬ 75% up`。
  - `ABOUT` 键值行：Status `● Running` / Schedule `05:00–23:00 Europe/Berlin` / Size `b1.small  1 vCPU (bursts to 2) · 4 GB` / This Month `$7.34` / Projected `$22.55` / Total `$15.59` / Billed To / Owner / Created；页脚「Files and data stay on the machine between deploys…」。
  - 主区：`APPS · 4`（图标 + 名 + 蓝色 `onamp.dev` 链接 + `Private` 灰标 + `repo · 分支 · sha · deployed 41 minutes ago` + 右侧黑色圆形「新线程」钮）；`ACTIVITY` 时间线（头像首字母圆标 + 「Priya Shah started …」/ 「bug-tracker deployed · a3f9c21」+ 相对时间）。
  - 左栏 `Overview` + `Workspace 1` 分组，条目 `workbench  P J +2 / 4 apps · bug-tracker deployed`。面包屑 `Boxes / workbench`，右上头像堆 + 黑色新线程钮 + `⋮`（Start / Stop / Delete Box）。
  - 金额是 box 计费，Oryn 不做计费，但「**成本 / 预计 / 在线率**」这套展示形态对「orb 统计与分析」有参考价值。
- **线程 Cost & Usage**：入口见 More Actions；面板截图**未找到**。
- **orb 状态**：只有 `Orb  Running ▸`，子菜单内容未找到。
- **Space 进度图**：见 5.1，只有文字。
- **Workspace Usage 页**：只有侧栏条目名（`S/docs/workspaces-overview.png`），内容未找到。`[团队功能·暂不做]`

---

## 8. 视觉语言

**依据**：全部截图 + `A/docs:raw` 内联 CSS（官方 token 级证据）。

- **深浅色**：`color-scheme: light dark`；跟随系统，也可强制。
- **底色 token**（`A/docs:raw` 的 `<style>`）：

  | 主题 | shell 浅 | shell 深 | surface 浅 | surface 深 |
  |---|---|---|---|---|
  | theme-0（默认） | `#fafaf8` | `#0b0d0b` | `#f9fdf6` | `#0c1516` |
  | theme-1 | `#fafafa` | `#0c0c0c` | `#fcfcfc` | `#121212` |
  | theme-4 | `#fcfbf6` | `#090d12` | `#fcfbf6` | `#0f161e` |
  | theme-5 | `#f8fafc` | `#0b121a` | `#f8f9fc` | `#111a25` |
  | theme-6（纯黑白） | `#ffffff` | `#000000` | `#ffffff` | `#000000` |

  theme-2、3 的值我没在前 300 行里看到，`[未找到]`。
- **整体印象**：
  - 浅色是带一点**暖绿的米白**（截图里 composer 与菜单是淡绿白），深色是**偏青的近黑**（`#0c1516`），都是低饱和。
  - 强调色很克制：绿（medium / 新增 / 在线）、蓝（high / 未读点 / 链接）、紫（ultra）、琥珀（修改 / 警告）、红（删除 / 危险）。
  - 主按钮是**黑色圆形 / 黑底白字**；次要是白底描边胶囊。
- **字体感觉**：UI 正文是**偏圆润的几何无衬线**（接近 SF / Inter 一类，iOS 用 Dynamic Type、`font: -apple-system-body`，HTML 注释），代码等宽（带斜体注释，Linux 截图里的 TS 代码有语法高亮）。
  - 字阶是 rem 基础的 type scale，随系统字号缩放（`data-type-scale`、`<meta name="text-scale">`）。
  - 字体具体名：未找到。
- **密度**：对话列宽松（行高大、段距足）；侧栏中等密度（行高约 28–32px，标题单行渐隐）；Changes 文件列表紧凑。浮层菜单行高偏大，快捷键用描边小徽标。
- **圆角**：胶囊 / 圆形按钮（发送钮、新建钮、选项胶囊）、大圆角卡片（composer、对话框约 16–20px）、分段控件。
- **动效线索**（来自 HTML 内联 CSS）：
  - 侧栏开关加载态：图标缩放到 0.4 隐去，中间一个圆点以 1.2s `ease-in-out` 呼吸（不透明度 0.4↔0.8）；`transform 120ms cubic-bezier(0.2,0.8,0.2,1)`，opacity 120ms。
  - `prefers-reduced-motion` 全部关闭。
  - 设置里有 UI sounds、logo motion 开关。
  - 首次绘制等待 `#amp-app-rendered`（避免侧栏 → 空主区 → 转录分段闪现）；macOS 子窗口（thread-in-own-window）预涂 surface 色防闪，且侧栏默认收起。
- **品牌化元素**：Dial 拟物硬件面板（螺丝、LED、LCD 点阵字）是少数「玩具化」点，其余界面很素。

---

## 对 Oryn 的启示（每条 1 行）
1. 线程页固定为「侧栏 / 窄阅读列 / 右侧环境面板」三栏，右栏只在有执行环境时出现。
2. 侧栏按 project 分组，列表尾部固定 `Inactive Last 72h / Snoozed / Archived` 三个折叠区，计数在右侧，这套排序值得直接复刻。
3. 每个线程配一个颜色标记 + 未读小蓝点 + 状态图标，颜色是用户可设的属性。
4. 侧栏底部放 Puck / Activity / Projects / 应用网格四个 Tab + 账号行，替代传统顶部导航。
5. 新建线程的 composer 用「执行位置 ^M / 项目 ^J / 模式 ^S」三枚带快捷键的胶囊，加 `⋮` 放次要项（Labels、Orb Size），选项一律用搜索式弹层。
6. 模式胶囊按档位着色（low / medium / high / ultra），并提供 Amp Modes 与 Raw Models 两个标签，Oryn 对应 omp 的角色 / 模型配置，配置本身不做 GUI。
7. runner 目录选择器里内置「Current | New Worktree」分段与 worktree 表单，创建线程时才真正执行 `git worktree add`。
8. 转录里 agent 回复不加气泡，过程折叠成弱色摘要行（`Explored N files`、`Edited … +a -b`、`Show Work ›`），只突出最终回答。
9. 输入框上方放建议回复 chips，首个 chip 随状态变（变更摘要 + Ship / Archive），点击即发送。
10. Changes 面板：`Ship / Review / Sync ▾` + 文件级 `A/M/D` 徽标 + 逐文件 stage + 底部 commit 选择器，智能排序用蓝点提示而不是自动重排。
11. Portal 面板做成「地址栏 + 被代理页面 + 元素选取式标注浮卡」，URL 形态 `t-<thread>-p<port>.<域>`，标注内容直接发给 agent。
12. 手机端把右侧面板压成顶部可横滚标签（Agent / Changes / Files / Terminal / Desktop），底部固定 chips + `+` / 输入框 / 🎤。
13. macOS 壳的设置页按「每账号一张 Runner 卡片」组织，开关粒度是 显示共享 / 终端 / 网络出口 / 文件夹列表 / 防休眠；Oryn 的本机 runner 设置可照此拆分。
14. 用量 / 统计的视觉形态可参考 Box 页：每日柱图（线程数）+ 一条在线率细线 + 键值行（状态、规格、本月 / 预计 / 累计）；Oryn 去掉金额，保留 orb 时长、数量、状态。
15. 主题用「shell / surface 两层底色 × 7 套 theme × 系统 / 深 / 浅」三个正交维度，加 Thread Width、UI sounds、减少动效，设置项都很少。
16. 不要复刻：Fork、Handoff 命令按钮、TODO 面板、Tab 补全、编辑器扩展、TUI 侧边栏、自定义 slash 命令、旧权限弹窗、旧模式名。
17. 视频是主要信息缺口：Terminal、Files、Desktop 面板和 Puck 面板都没有静态截图，实现前需要有 ffmpeg 的环境补抽帧。

---

## 附录 A：实际看过的图片 / 视频清单
**已打开并看到内容的图**（均 `S/` 前缀）
- `docs/threads-thread-view.png`、`docs/threads-activity.png`、`docs/threads-more-actions-menu.png`
- `docs/orbs-new-thread.png`、`docs/orbs-new-project.png`、`docs/orbs-setup-files-changes.png`
- `docs/portals.png`、`docs/orb-size-picker.png`
- `docs/apps-mac-dark.webp`、`docs/apps-iphone-dark.webp`、`docs/apps-android-dark.webp`、`docs/apps-surface-light.webp`、`marketing/apps-linux-dark.webp`
- `docs/projects-project-page.png`、`docs/projects-composer-picker.png`、`docs/projects-settings-overview.png`
- `docs/the-dial.png`、`docs/the-dial-raw-models.png`
- `docs/multiplayer-participants-menu.png`、`docs/multiplayer-activate-button.png`、`docs/multiplayer-activate-dialog.png`（`[团队功能·暂不做]`，仅记录）
- `docs/runners-shared-picker-dark.png?v=2`、`docs/runners-worktree-picker-light.png`、`docs/runners-worktree-form-light.png`、`docs/runners-directory-picker-light.png`、`docs/macos-and-ios-runner-picker-dark.png`
- `docs/macos-runner-settings-per-account.png`、`news/runner-desktop-settings-dark.png`
- `docs/workspaces-overview.png`（`[团队功能·暂不做]`，只用设置栏目名）
- `news/boxes-box-page-v2-light.png`
- `news/meet-space-demo-poster.jpg?v=3`（Space 标签 + Huddle 布局）
- `docs/orbs-get-started-in-orbs-thumbnail-v2.jpg`、`docs/orbs-portals-in-orbs-thumbnail.jpg`、`docs/orbs-add-a-dev-sign-in-thumbnail-v2.jpg`、`docs/orbs-test-agent-friendliness-thumbnail-v2.jpg`
- `news/diffs/amp-diff.png?v=2`
- 品牌插画（无 UI 信息）：`news/puck-floating.jpg`、`news/multiplayer.png`、`news/slack/resolve-incident-magical.png`（Slack 截图）
- 【旧版 UI】`news/neo-ask-user-choice.png`、`news/from-agent-to-agent-handoff.png`、`news/diffs/open-in-browser.png?v=2`

**文本层面读过的页**：`A/sitemap.xml`、`A/chronicle`、`A/docs/markdown/{threads(前 300 行/314),apps/ios,apps/android,spaces}`、`A/docs:raw`（`<html>` 头与内联 CSS）、`A/news/{neo(150–260 行),so-long-tui-sidebar,agents-everywhere}`、`A/docs/using-amp/screencasts/orbs/get-started-in-orbs`。另对下列页面只用 `grep` 抽取了媒体 URL 与 alt，没通读：`docs/{orbs,orbs/getting-started,orbs/portals,orbs/shipping,orbs/sizes-and-costs,orbs/profiles,orbs/self-hosted,orbs/mac,apps,apps/mac,apps/windows,apps/linux,projects,boxes,the-dial,cli,cli/runners,collaborate/multiplayer,collaborate/huddle,collaborate/workspaces,puck,puck/slack-integration,orbs/agent-to-agent}` 与 `news/{spaces,desktop,diffs,less-noise,artifacts,multiplayer,amp-on-ios-and-macos,amp-on-android,neo,schedule,runner-desktop,meet-puck}`、`A/what-are-orbs`、`A/app`、`A/morbs`、`A/modes`、`A/docs/using-amp`。

## 附录 B：打不开 / 没看到的资源
**AVIF（`read` 返回「unsupported」，只用 alt）**：
`news/less-noise-dark.avif`、`news/artifacts-blocking-waits.avif`、`news/artifacts-markdown-preview-variants.avif`、`news/spaces-cover.avif`、`news/spaces-v1-space-light.avif`、`news/spaces-service-accounts-light-v2.avif`、`news/spaces-security-review-light-v2.avif`、`news/spaces-overview-v3-poster.avif`、`news/boxes-composer-v2-light.avif`、`news/boxes-diagram-light.avif`、`news/let-the-orb-browse-profiles-silkcards-orders-{light,dark}.avif`、`news/self-hosted-orbs-diagram-light.avif`、`news/self-hosted-orbs-editor-light.avif`、`news/amp-on-android-{thread,portal,changes}-{light,dark}.avif`、`news/use-your-claude-plan-dial-{light,dark}.avif`、`news/orbs-now-have-a-mac-*.avif`、`docs/runners-desktop-ios-{light,dark}-poster.avif`。

**把 `.avif` 换成 `.jpg` / `.webp` 试访**：`artifacts-blocking-waits.jpg`、`less-noise-dark.webp` 返回 404。

**视频（`read` 只返回二进制信息，抽帧 404）**：
`docs/welcome-demo.mp4`、`docs/agents-in-orbs-demo-{main,rush,terminal,sync,new-in-orb}.mp4`、`docs/portal-review-comments.mp4`、`docs/orbs-shipping-restack-working-thread.mp4`、`docs/amp-puck-demo.mp4`、`docs/pass-the-orb-reproduction-v6.mp4`、`docs/cli-getting-started-tui.mp4`、`docs/agents-anywhere-demo-*.mp4`、`docs/agent-to-agent.mp4`、`news/spaces-overview-v3.mp4`、`news/meet-space-demo.mp4`、`news/multiplayer-demo.mp4`、`news/schedule.mp4`、`news/desktop-demo-modibo.mp4`、`news/desktop-fun-games-tux-racer-full.mp4`、`news/diffs/{diffs,diffs-mobile,staging}.mp4`、`news/native-apps-hero-2560.mp4`、`news/new-look-tour1.mp4`、`news/new-look-ios3.mp4`、`news/neo-*.mp4`。

**没有静态图或没找到图**：
- 面板：Files（树 / 预览 / 上传）、Terminal、Desktop 工具栏与接管提示、Artifacts 面板、Puck 面板、Huddle 面板（团队，不做）。
- 线程：Cost & Usage 面板、orb 启动 / 排队 / 暂停状态条、thinking 块、转录内嵌 diff / 终端 / 可视化、队列与 steer 指示、commit 选择器、Restack 入口。
- 其他：通知样式、移动端面板详细形态、设置页中个人设置（只看到工作区设置与 macOS 设置）、Space 进度图实图、`docs/self-hosted-orbs-add-provider-light.png?v=3` 等自托管对话框图（仅 alt）、theme-2 / theme-3 色值。
- `A/docs/markdown/using-amp` 返回 404（该页没有 Markdown 版）；sitemap 后 37 行未翻页。
# 补充报告 §9：公开分享与公开入口（深挖）

本节新增，并修正前文两处。前文 §0–§8 与附录继续有效，只做一处订正：Portal 地址栏的齿轮图标是「设置主机名 / 查看访问权限」，不是前文猜的 Access 菜单。依据是 `A/news/portals`（2026-08-06）："Use the gear icon in the Portal address bar to change the hostname or check who has access"。后续文档（2026-11-10）把公开访问入口写成 Portal 工具栏 **⋯ → Portal Options → Access**。

团队类（workspace 开关、groups、multiplayer、service accounts）只在需要对照时带一句，标 `[团队功能·暂不做]`。

简称同前：`A` = `https://ampcode.com`，`S` = `https://static.ampcode.com`。

---

## 9.0 公开入口总览

| 入口 | 谁能访问 | 有效期 | 撤销方式 | 状态与依据 |
|---|---|---|---|---|
| 线程 Unlisted 链接 | 持链接者，无需登录 | 长期，直到改回别的可见性 | 改可见性（撤销后的具体行为文档未写） | `[现行]` `A/docs/markdown/threads`（2026-11-10） |
| Portal 公开 | 持链接者，无需登录，仅该端口 | 1h / 3h / 1d / 7d | `End Public Access`，或到期自动失效 | `[现行]` `A/docs/markdown/orbs/portals`（2026-11-10） |
| 已发布文件（agent 发布） | 持 URL 者 | 文档未写 | 文档未写 | `[现行]` `A/docs/markdown/orbs/files-and-attachments`（2026-10-30） |
| Artifact「Anyone with the Link」 | 持链接者，只读 | 长期 | 改回 Same Access as Thread | `[现行]` `A/docs/markdown/threads`（2026-11-10） |
| 公开只读代码访问 | 互联网任何人，clone + 读文件 | 长期 | 关开关 | `[现行]` 仅 Amp 托管仓库，`A/docs/markdown/projects` |
| Amp Store 公开项目 | 任何人浏览，`Get` 需登录 | 长期 | 未找到 | `[现行]` `A/store`（我读了实页） |
| Box 应用 | 默认公开，持 URL 即可 | 长期 | `--private` 重新部署 | `[现行]` `A/docs/markdown/boxes`（2026-11-11） |
| 公开可发现线程（Public Discoverable） | 全网可搜 | — | — | `[已移除]` 2026-06-02，`A/news/end-of-public-threads` |

---

## 9.1 线程公开分享（Unlisted）

### 9.1.1 入口与 UI
- 入口：标题栏 `Share` 按钮，或命令面板 **Share Thread…**（`A/docs/markdown/threads`）。
- 标题栏的 `Private` 小标签是当前可见性的显示位。`S/docs/portals.png` 与 `S/marketing/apps-linux-dark.webp` 里都能看到它紧挨在 `Share` 旁。标签换成别的值时的样子没有截图，未找到。
- Share 对话框内容（文档描述，**没有对话框截图**）：
  - 复制线程 URL；
  - **General Access** 选择三档：只有 People with Access 里列出的组 / 整个工作区 / **任何拿到链接的人**；
  - **People with Access** 列表，每项有菜单可移除；
  - **Turn On Multiplayer**。
- CLI：`amp threads share T-… --visibility unlisted`；在 CLI 里按 `Ctrl+O` 运行 `set visibility`。

### 9.1.2 可见性枚举（现行文档的四档）
| 值 | 谁能打开 |
|---|---|
| Private | 本人，加经审计访问的工作区管理员 |
| Workspace | 工作区全员 |
| Group | 指定组，仅 Enterprise |
| **Unlisted** | 互联网上任何持链接者 |

- Oryn 单用户只需要 `Private` 和 `Unlisted` 两档。
- 没有工作区时，新线程默认私有（`A/docs/markdown/threads`）。
- 过滤语法：`visibility:private | workspace | group | unlisted`。该过滤只收窄你已能看到的线程，「Unlisted threads outside your account and workspaces remain undiscoverable」（`A/docs/markdown/prompting`）。

### 9.1.3 访客是只读的
- 「Public unlisted viewers cannot influence the thread」，来自 MCP 文档对 `thread_non_owner_can_influence` 字段的说明（`A/docs/markdown/customize/mcp`）。
- Multiplayer 与 Unlisted 互斥：「Multiplayer is not available while anyone with the link can view the thread, and making a thread viewable by link turns it off.」（`A/docs/markdown/threads`）。

### 9.1.4 访客页面里有什么
- **未找到**官方描述。包括是否含工具调用、是否含 Changes / diff、是否实时更新、是否展示 Portal 与 Terminal，文档都没写。
- 我的探测：匿名 GET 一条 2025 年的旧公开线程 `A/threads/T-4e6a3890-fc01-416b-acc9-40fa6da60028`（链接来自 `A/notes/how-i-use-amp`），被 302 到 `A/auth/sign-in?returnTo=/threads/T-…/view`；对 `….md` 同样被拦到登录页。这个结果**不能证明**现行 Unlisted 线程的访客体验，因为那条线程可能已被改私有或删除。我也没找到一条确认处于 Unlisted 状态的现行线程做对照。
- 值得记下的线索：登录重定向用的是 `/threads/T-…/view` 这个路径形态，Sign-in 页提供 ChatGPT / Passkey / Google / Apple 四种登录。

### 9.1.5 导出 `.md`
- 线程 URL 加 `.md` 即得 Markdown：`https://ampcode.com/threads/T-….md`。
- More Actions → **Export ▸** 提供 Markdown；**创建者**还能导出完整 JSON。
- CLI：`amp threads markdown T-…`、`amp threads export T-…`。
- 匿名访客能否用 `.md` 读 Unlisted 线程：未找到（上一条探测未得出结论）。

### 9.1.6 URL 形态与不可猜测性
- 形态：`https://ampcode.com/threads/T-<UUID>`，例如 `T-019f6fc7-8d3f-734c-9c7e-51b69d42c74a`（`S/news/from-agent-to-agent-handoff.png`，【旧版 TUI 图，ID 格式仍有效】）。第三段以 7 开头，是 UUIDv7 `[推测]`。
- 官方措辞是「share them with anyone by unguessable URL」（`A/news/end-of-public-threads`），所以不可猜测性靠的是随机 ID 本身，没有额外口令、过期或密码。

### 9.1.7 撤销、过期
- 过期：没有，Unlisted 是长期状态。
- 撤销：改回 Private / Workspace 即失效，这是由可见性枚举推出的 `[推测]`。线程文档只写了「making a thread viewable by link turns [multiplayer] off」，没有单独描述撤销后访客的体验。
- 管理员总开关 `Allow Users to Share Threads and Portals Outside the Workspace`：关闭后已有公开链接停止工作（对 Artifact 有明文，对线程是否同样，文档只对 Artifact 写了） `[团队功能·暂不做]`。

### 9.1.7b 历史与设计理由 `[已移除]` 2026-06-02
`A/news/end-of-public-threads` 原文要点：
- 删除了「public, internet-wide, discoverable」线程分享，保留工作区分享与 `Unlisted`（拿到不可猜 URL 即可看）。
- **理由**：「It's getting too hard to review a thread to ensure it doesn't contain any snippets of sensitive files. Each model release means the agents get better, and as they get better they read more files into context.」并声明决定是主动做的，不是出于事故。
- 公开用户主页（如 `A/@sqs`）仍显示活动，但**不再显示任何线程**；原有 Public (Discoverable) 线程一律变成 Unlisted，所以博客里的线程链接不会失效。

对 Oryn：做 Unlisted，**不做**可发现列表和公开主页的线程列表。

### 9.1.8 举报 / 内容审查
- 线程分享这一块**没找到**内容审查或举报入口。More Actions 里的 `Report Bug` 是产品 bug 上报，不是内容举报（`S/docs/threads-more-actions-menu.png`）。
- 滥用举报只写在 onamp.dev 托管应用（见 9.7）。

---

## 9.2 Portal 公开访问

### 9.2.1 默认与公开
- 默认：「only people who can view the thread can access its portals」。
- Portal 访问与应用自身的认证是**两件事**（文档标题 Access and Lifecycle）。

### 9.2.2 公开 UI（按文档）
1. Portal 工具栏 **⋯ Portal Options** → **Access**。
2. 在 **Make Public For** 下选 **1 hour / 3 hours / 1 day / 7 days**。
3. 之后任何人持链接无需登录 Amp 即可打开，直到到期。
4. **只对该端口公开**；公开链接不授予线程或审阅工具（review widget）的访问权。
5. 公开期间 **Access** 里显示结束时间；可在 **Keep Public For** 下重选时长（续期），或点 **End Public Access**（终止）。
6. 这个菜单界面的截图未找到。

### 9.2.3 通过 agent 公开
- 也可以让 agent 来做：它调用 `manage_amp` 工具，topic = `portals`，operation = `set-public-access`，并且**弹确认对话框**，批准后 portal 才会变公开或延长公开。
- `amp` CLI **不能**修改 portal 访问权限；`amp orb portal list` 能看哪些公开及到期时间（agent 用 operation `list`）。

### 9.2.4 主机名与自定义域名
| 形态 | 示例 | 依据 |
|---|---|---|
| 自动 | `t-<thread>-p<port>.onamp.dev`，如 `t-03gqavrgaqr1rey4jgbmyll59-p21228.onamp.dev` | `S/news/friendly-orb-hostname-dialog-20260823.png?v=2` |
| 友好（托管） | `<name>--<namespace>.onamp.dev`，如 `full-width-navbar--amp.onamp.dev` | 同上；`A/news/friendly-urls-for-sharing-orbs`（2026-08-23） |
| 自定义域名 | `park.mixfox.org`；`.mixfox.org` 作为选项 | 同上 |
| 工作区默认域 | `t-<thread>-p<port>.portal.example.com`，由 workspace Domains 页的 **Make Default for Portals** 启用 | `A/docs/markdown/orbs/portals` `[团队功能·暂不做]` |

- 命令：`amp orb portal <port> --hostname <hostname>`；在 orb 之外由 agent 用 `manage_amp` topic `portals` operation `set-hostname`。
- 自定义域需先在个人 `A/settings/domains`（或工作区 `/workspace/domains`）添加，按页面提示建 DNS 记录，等到 **Active**。
- 主机名**只绑定该端口**；归档线程后主机名仍保留；用 `--take-over` 把它转给另一个线程的 portal，或者用 `amp boxes deploy --domain <hostname> --take-over` 移给 box 应用；portal 不能从 box 应用手里抢回主机名。
- **Portal Hostname 对话框**（`S/news/friendly-orb-hostname-dialog-20260823.png?v=2`，深色，已看）：
  - 标题 `Portal Hostname`，右上 ✕；
  - 第一行：自动主机名，带 `Auto` 标签，右侧单选圆；
  - 第二块 `Custom`：左侧一个可编辑前缀框（显示 ✓ 与 `full-width-navbar`），右侧后缀 `--amp.onamp.dev`（已选中，实心圆）；下一行 `.mixfox.org`（未选）；其下灰字链接 `Manage Custom Domains`；
  - 底部 `Cancel` / 亮色 `Change Hostname`。
- **Slack 链接展开**（`S/news/friendly-orb-slack-preview-20260823.png`，已看）：消息里贴 `full-width-navbar--amp.onamp.dev/storybook/project-owner-nav`，展开成一张卡：标题 `Quinn's Orb Portal`、「Added by Amp Agent (368 kB)」、一张深绿渐变预览图（含地址栏 `full-width-navbar--amp.onamp.dev` 和 `Open ↗`）、两个按钮 **`Open Portal`**（绿）与 **`Open Thread`**（描边）。说明公开 portal 页自带 OG 预览图。

### 9.2.5 安全措施（portal）
- 不可猜测：默认主机名含线程 ID 与端口；友好主机名则是人类可读名，靠 7 天上限和限定端口来控风险。
- 过期：只有四档，最长 7 天。
- 范围：只暴露一个端口。
- 需要确认：agent 要公开或延长公开必须经用户批准弹窗。
- 自托管 orb：**Public Portals** 默认**关闭**，理由是「a public portal is an anonymous tunnel into your network」；provider 的 Edit 对话框里关掉它，会一次性终止该 provider 下所有 portal 的公开访问（`A/docs/markdown/orbs/self-hosted`）。关闭时 Access 菜单置灰并有 tooltip。
- 本机 runner 的 portal：**不能公开**，页面也没有 review 按钮（`A/docs/markdown/cli/runners`，2026-11-08）。与 2026-11-09「Runner Portals」公告配合读：本机 portal 存在，但仅限已授权访问。
- 访客身份头：Amp 在外部 portal 代理的请求上加 `X-Amp-Authenticated`，值为逗号分隔的 `amp-user=yes|no, workspace-member=yes|no, collaborator=yes|no`；对工作区成员还带 `X-Amp-User-ID / Display-Name / Email / URL`。匿名访客是 `amp-user=no, workspace-member=no, collaborator=no`。应用据此自己做访问控制。
- 访问会唤醒休眠 orb：「Opening a portal wakes a paused orb」，期间计费，空闲后自动再暂停。归档线程同样暂停 orb。归档是否同时让公开 portal 失效，文档没写。
- WebSocket 经过单台 Amp 服务器，部署时会断（客户端报 1006）；不要用 portal 承载数据库隧道。
- 两个 portal 主机之间的浏览器请求不支持（CORS 预检不带 portal 认证），应让前端 dev server 代理 API。
- Review widget 可按服务关闭（`review: false`），或按响应加 `x-amp-review-widget: off`。
- `.amp/services.yaml` **不支持** `access` 字段，访问由 Amp 单独管。
- Vite 6.1+ 通过 `__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS` 自动放行 `.onamp.dev`。

---

## 9.3 文件 / 附件发布为公开 URL

依据 `A/docs/markdown/orbs/files-and-attachments`（2026-10-30）。
- 普通附件**只属于线程**，不会自动写入仓库，受 Amp 访问检查保护。
- **发布**：用户让 agent 发布（例：「Publish the finished demo video so I can share it outside this thread.」）；发布后文件有 **Public** 徽标。
- 同一个 Attachments 列表还有 **Temporary** 徽标：几小时后过期的文件。
- 文档警告：「Only publish a file when it is safe for anyone with the URL to access it.」
- 在线程菜单的 **Attachments**（More Actions 里叫 `Artifacts and Attachments`）可看全部文件；能删线程的人可逐个或全部删除附件。附件是线程里的副本，与 orb 的 Files 面板无关。
- 上传限额：普通 100 MB / 文件，高上传额度用户 512 MB。
- **URL 形态**：`https://ampcode.com/user-content/artifacts/<64位十六进制>-file.<ext>`，例如 `…/artifacts/74870045bd52eefff18b14946e52054a2aa5b2a0c135110090c9b8ebf137636e-file.mp4`（`A/news/attach-anything`）。这些 URL 能被 Store 页和公告页匿名加载。64 位十六进制很可能是内容哈希 `[推测]`，所以靠哈希不可猜测，不靠签名。
- 公开 artifact URL 也被用在 PR 描述里（`A/docs/markdown/projects`：AGENTS.md 里写「embedding public artifact URLs of screenshots or videos in the PR description」）。
- 未找到：发布文件的过期策略、撤销方法、删除后 URL 是否立即失效、内容审查。

---

## 9.4 Artifact「Anyone with the Link」

依据 `A/docs/markdown/threads`（2026-11-10）与 `A/news/artifacts`（2026-11-02）。
- 位置：artifact 内 **Share → General Access**，三档：
  - **Same Access as Thread**（默认，随线程可见性变化）；
  - **Workspace Members**（`[团队功能·暂不做]`）；
  - **Anyone with the Link**：任何人，无需登录，**只读**。
- 编辑权：只有能打开线程且属于其工作区的人，或个人线程的所有者。
- **Export 标签页**：下载 Markdown 或 HTML，或打印成 PDF。
- 限制：每个 artifact 最大 2 MB。
- **已知坑**：artifact 里加的图片存放在线程里，只有能打开线程的人看得到，通过工作区或公开链接进来的读者会看到**破图**。这是 Oryn 必须规避的设计缺陷，公开 artifact 应把图片一起发布或内联。
- 删除 artifact 会保留其图片直到线程被删；删线程会删除其所有 artifact；只有 artifact 所属线程能删它。
- 管理员关掉 `Allow Public Artifact Uploads` 后，已有公开链接停止工作；管理员审计访问时 artifact 只读 `[团队功能·暂不做]`。
- 截图：没有 Share 对话框截图，未找到。

---

## 9.5 公开只读代码访问与公开项目页

- **开关**：Project Settings → Overview 里的 **Public Read-Only Code Access**（`S/docs/projects-settings-overview.png`，已看）：开关，说明文字「Allow anyone on the internet to clone this repository and read all files and branches. Threads are not exposed.」只对 **Amp 托管仓库**显示。
- 范围：别人能 clone、读代码，但**不能**开线程、看不到线程（`A/docs/markdown/projects`）。
- Git 地址形态：`https://ampcode.com/@amp/calc`（设置页的 Git Repository 字段）。
- **只读 Git 凭据**（给 CI / 构建机，不需要用你的身份登录）：项目设置里 **Create Credential** → 命名 → 过期期限 **7 / 30 / 90 / 180 天或 Never** → 复制 **Authenticated Clone URL**；**只显示一次**。
- 公开项目页（匿名读到的 `A/projects/@amp/mixmath`）：
  - 面包屑 `@amp / mixmath`，右上 `Clone`；
  - 头像、`By amp`、项目名、**`Get`** 按钮（匿名点击跳到 `A/auth/sign-in?returnTo=/@amp/mixmath?get=1`）；
  - 描述、画廊图（3–6 张）；
  - **Files** 表：文件夹 / 文件行，每行 `Open <name>` 链接 + 最近一次 commit 标题 + 相对时间；
  - **Commits to main**：作者 · 短 sha · 时间；
  - 页面**没有线程列表**，与「Threads are not exposed」一致。
- URL 形态：`/@owner/name`（重定向到 `/projects/@owner/name`）、`/projects/@amp/mixmath/{tree,blob}/main/<path>`、`/projects/@amp/mixmath/commit/<sha>`、`/projects/@amp/mixmath/commits/main`。
- 项目页自己的（已登录）形态见前文 §1.5 与 `S/docs/projects-project-page.png`。

---

## 9.6 Amp Store / 公开项目 `[现行]`

- 页面：`A/store`，描述「Get public projects built by the Amp community from the Amp Store.」（实页已读）。
- 卡片：头像、项目名（链到 `/@amp/<name>`）、`By amp`、一两句描述、3–6 张画廊图、**`Get`** 按钮。
- 当前 4 个条目：`mixmath`、`mixpark`、`mix-spellray`、`orbin-slide-show`，作者都是 `amp`。
- `Get` 对匿名访客跳登录：`/auth/sign-in?returnTo=/store?focus=<project-uuid>`；在项目页是 `?get=1`。`Get` 做什么（我推测是复制 / 克隆到自己的账号），文档未明说 `[推测]`。
- 画廊图用的是 9.3 的 `user-content/artifacts/<hash>-file.png|webp|jpeg` 公开 URL。
- 项目文件里有 `.agents/` 目录（orb setup 脚本），说明 Store 项目就是普通 Amp 托管项目 + 公开开关 + 画廊。
- 一条 commit 信息提到「Amp Sites」（「Preserve offline worker on Amp Sites」），可能指 box / 站点托管，文档没有对应章节 `[推测]`。
- **未找到**：如何把自己的项目提交到 Store、审核流程、下架方式、画廊图如何设置。

---

## 9.7 Box 公开应用与预览

依据 `A/docs/markdown/boxes`（2026-11-11）。
- 每个应用一个 URL，形如 `https://bug-tracker--acme.onamp.dev`（`<app>--<namespace>.onamp.dev`）。**默认公开**：「anyone with the URL can open it」。
- **私有应用**：部署时加 `--private`，访问者必须先在浏览器登录 Amp（box 所有者或所属工作区成员）；不带这种登录的请求（如 webhook）到不了私有应用。后续部署保持该设置，并写进 `.amp/box-deploy.json`；`--public` 改回公开。Box 页的应用行用灰色 `Private` 小标显示（`S/news/boxes-box-page-v2-light.png`，已看）。
- Amp **不会**告诉应用谁打开了它：公开应用要自己做登录。
- **自定义域名**：`amp boxes deploy --domain portal.example.com`；先在 Domains 页添加并等 Active；`onamp.dev` 地址随后重定向到新主机名；`--no-domain` 退回；与 portal 主机名冲突时用 `--take-over`（此时主机名一开始部署就切换，持链接者不会碰到空应用）。
- **反馈小组件**：`amp boxes deploy --widget`。私有应用对所有能打开它的人显示；**公开应用默认隐藏**，按 `Cmd+.`（Win/Linux 是 `Ctrl+.`）或在手机上快速点三下才显示；发送评论始终需要以 box 所有者或工作区成员身份登录 Amp。`--no-widget` 移除。
- **应用预览（Previews in the Rail）**：
  - 应用在其固定 URL 的 `<head>` 放 `<meta name="amp-preview" content="v1; enabled; iframe">`；
  - Amp 用**不带 cookie 的服务端请求**读取该标签（跟随同源重定向），每隔几分钟重查；
  - 预览是一个 **384×240** 的 iframe，URL 上附加 `amp-preview=light|dark`，应用应渲染紧凑页、去掉导航与登录；
  - 预览**不可交互**，点卡片整体才会打开应用；iframe **沙箱化**：不能弹窗、提交表单、导航 Amp、使用摄像头麦克风；
  - 私有应用的预览用查看者的 box 登录；应用自己的 cookie 除非 `SameSite=None` 且分区，否则不会带进框内；
  - 每次悬停都会重新加载，所以请求要快且无副作用；反馈小组件在预览加载时被省略。
- **滥用处理**：box 应用必须遵守 `A/terms/aup`；举报钓鱼、恶意软件等，发邮件到 `security@ampcode.com` 并附 URL（`A/security` 也写了同样的话）。违规时 Amp 可以**封禁 box**：封禁后 box 停止，它所有 URL（含私有应用与自定义域名）显示「应用不可用」页，磁盘与数据保留，即使有运行计划也保持停止，直到 Amp 解封，所有者无法自行启动；Amp 会邮件通知所有者或工作区管理员和创建者，box 页也显示原因。
- 工作区开关 `Allow Public Box Apps`：关闭则所有应用强制私有；重新开启后应用保持私有，直到各自用 `--public` 重新部署 `[团队功能·暂不做]`。
- 多应用共享一个 box 时的隔离：每个应用独立 Linux 用户 + systemd 沙箱，**不是**安全边界（共享 CPU、内存、网络，能连其他应用端口），官方建议不互信的应用用不同 box。
- Box 页本身的截图见前文 §7.2。

---

## 9.8 公开入口的安全措施汇总

| 措施 | 线程 Unlisted | Portal | 已发布文件 | Artifact | 公开代码 | Box 应用 |
|---|---|---|---|---|---|---|
| 不可猜测 URL | ✓ 随机 ID | ✓ 默认主机名含线程 ID；友好 / 自定义主机名为人类可读，靠其他手段 | ✓ 疑似内容哈希 `[推测]` | ✓ 链接形态未读到 | ✗ 公开 | ✗ 默认公开 |
| 过期 | ✗ | ✓ 1h–7d | 未找到 | ✗ | ✗ | ✗ |
| 撤销 | 改可见性 `[推测]` | `End Public Access` | 未找到 | 改回线程同级 | 关开关 | `--private` 重新部署 |
| 范围收窄 | 只读、不能影响线程 | 只对一个端口，不授予线程与 review | 单文件 | 只读、2 MB | 不含线程 | 每应用独立沙箱 |
| 需用户确认 | 未找到 | agent 公开需确认弹窗 | 未找到 | 未找到 | 未找到 | 未找到 |
| 内容审查 / 举报 | 未找到 | 未找到 | 未找到 | 未找到 | 未找到 | `security@ampcode.com`，Amp 可封禁 |
| 默认关闭 | ✓（默认私有） | ✓（默认仅线程可见者） | ✓（默认私有附件） | ✓（默认同线程） | ✓（默认关） | ✗（默认公开） |
| 自托管默认 | — | **Public Portals 默认关** | — | — | — | — |

其他与公开面相关的安全细节：
- **设计教训**：2026-06-02 取消可发现线程的理由，是难以人工保证线程里没有敏感文件片段（见 9.1.7b）。公开形态都应默认关闭，并有清晰的二次确认。
- **webhook URL**：「Treat the webhook URL as a password」，任何持有者无需登录即可投递事件；归档线程会暂停 orb 并让该 URL 返回 HTTP 404（`A/docs/markdown/orbs/automations`）。
- **反馈小组件的来源限制**（`A/docs/markdown/feedback-widget`）：允许的网站来源是精确来源或 `https://*.example.com` 通配；通配符后必须是别人可拥有的域，**不能是公共后缀**（用 Public Suffix List 校验，拒绝 `https://*.co.uk`、`https://*.github.io`）；`https://*.onamp.dev` 这种 Amp 自己托管站点的通配也被拒绝，只能加一个精确来源（如 box 应用的）。授权会出现在 `A/settings/security#authorized-apps`，撤销后立即失效；授权 12 小时后需要再点 `Continue with Amp`。
- **泄露令牌吊销**：`POST /api/revoke`，把泄露的令牌放在 `Authorization: Bearer`（`A/security`）。
- **Passkey**：`Use Passkey Authentication` 可要求敏感操作使用 passkey 会话（`A/security`）。它是否覆盖「改为公开」没有写，未找到。
- **管理员审计**：私有线程对工作区管理员可见，但访问需再次验证身份、页面显示审计提示并记入审计日志 `[团队功能·暂不做]`。

---

## 9.9 对 Oryn 的启示（公开分享，每条 1 行）
1. 可见性只做 `Private | Unlisted` 两档，Unlisted 用随机 ID 链接，不做可发现列表，也不做公开主页的线程列表。
2. Unlisted 访客一律只读，且不与多人 / 远程控制能力共存。
3. 线程 `.md` / JSON 导出作为独立命令存在；对 Unlisted 访客是否开放导出要自行决定，Amp 文档未写。
4. Portal 公开用「按端口 + 限时（1h / 3h / 1d / 7d）+ 续期 / 终止」，公开期间在 Access 里显示剩余时间。
5. agent 要把 portal 变公开或延长公开，必须弹窗让用户批准；CLI 不应能绕过这个确认。
6. 自有 orb provider 里把「允许公开 portal」做成默认关闭的 provider 级开关，关闭时一次性收回该 provider 下所有公开 portal。
7. Portal 主机名三层：自动 `t-<thread>-p<port>.<域>`、友好 `<name>--<namespace>.<域>`、自定义域名（需 DNS 验证到 Active）；主机名只绑一个端口，线程归档后仍保留，可 `--take-over`。
8. 公开 portal 页要带 OG 预览（标题、预览图、`Open Portal` / `Open Thread`），便于贴进聊天工具。
9. 公开 portal 的访客身份用请求头（`amp-user` / `collaborator` 等）传给应用，由应用自己做权限判断；Oryn 单用户版可简化为「是否已登录主人」。
10. 发布文件与普通附件分开：默认私有，发布是显式动作，列表上用 `Public` / `Temporary` 徽标区分；发布前提示「任何持有 URL 的人都能访问」。
11. 公开文件 URL 用内容哈希命名（`<64hex>-file.<ext>`），并补上 Amp 缺的东西：过期与撤销。
12. 公开 Artifact 必须把图片一起发布或内联，避免 Amp 那种「公开读者看到破图」的坑；每个 artifact 加大小上限（Amp 是 2 MB）。
13. 公开只读代码访问是项目级开关，只对自己托管的仓库开放；同时提供带过期期限的只读 Git 凭据（7 / 30 / 90 / 180 天或永不），令牌只显示一次。
14. 公开项目页只展示文件、提交、画廊，不暴露线程；`Get` 之类的「复制到我的账号」动作要登录。
15. Store 先不做；若要做，补上 Amp 缺失的提交、审核、下架流程。
16. Box 式公开应用：默认公开但一键 `--private`；预览用 `amp-preview` meta + 384×240 沙箱 iframe（不可交互、禁用弹窗 / 表单 / 摄像头）；Oryn 还应补上封禁与举报入口，Amp 目前只有邮件。
17. 反馈小组件的来源白名单要用公共后缀表校验，禁止对自家托管域名做通配。
18. 所有公开入口都应在统计页留痕：创建时间、到期时间、被访问次数，以及一键「撤销所有公开入口」。Amp 文档没有这样的统一视图（只有 `amp orb portal list` 能看 portal 公开状态）。

---

## 9.10 本节新增取证清单与缺口

**新看到的图**（`S/` 前缀）
- `news/friendly-orb-hostname-dialog-20260823.png?v=2`（Portal Hostname 对话框）
- `news/friendly-orb-slack-preview-20260823.png`（公开 portal 在 Slack 的展开卡）

**新读到的页**
- `A/docs/markdown/{threads(后半)、orbs/portals(全文)、orbs/files-and-attachments、boxes(前 420 行)、enterprise/workspace-thread-visibility-controls、projects(关键段)}`
- `A/news/{friendly-urls-for-sharing-orbs, artifacts, end-of-public-threads}`
- `A/store`、`A/projects/@amp/mixmath`
- `A/security`、`A/terms/aup`、`A/docs/markdown/feedback-widget` 的相关段
- 对 `A/docs/markdown/{cli/runners, orbs/self-hosted, orbs/automations, customize/mcp, prompting}` 做了关键字检索

**仍然缺失**
- 访客所见：Unlisted 线程匿名页的实际内容（是否含工具调用与 diff、是否实时更新）。我用匿名 GET 探测了一条 2025 年旧线程，被拦到登录页，结论不确定。
- 撤销后的行为：线程改回私有后访客看到什么；已发布文件如何撤销；artifact 链接撤销的页面表现。
- 界面：Share 对话框、Portal Options → Access 菜单、Artifact Share 面板、线程 Attachments 列表（Public / Temporary 徽标）的**截图一张都没找到**。
- Store：提交 / 审核 / 下架流程；`Get` 的确切行为；「Amp Sites」是什么。
- 举报：线程、artifact、已发布文件没有内容举报入口的描述；只有 box 应用有邮件举报。
- `A/docs/markdown/boxes` 第 420 行之后（Box 删除与计费段）没再读，与公开面无关。
- AVIF 图与 MP4 视频的限制同前文附录 B。

---

### Critical Files for Implementation
- `A/docs/markdown/threads` — 线程可见性（Private/Unlisted）、Share 对话框、`.md` / JSON 导出、artifact 的 Anyone with the Link 与 2 MB 限制
- `A/docs/markdown/orbs/portals` — Portal 的 Make Public For（1h/3h/1d/7d）、续期与终止、`manage_amp` 流程、主机名与自定义域名、访客身份头
- `A/docs/markdown/boxes` — 应用默认公开、`--private`、自定义域名、`amp-preview` 预览契约、反馈小组件、封禁与举报
- `A/docs/markdown/orbs/files-and-attachments` — 附件与已发布文件的 `Public` / `Temporary` 徽标和发布约束
- `A/news/end-of-public-threads` — 取消可发现公开线程的设计理由，可直接作为 Oryn 公开面的默认关闭依据
