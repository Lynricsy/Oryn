# Amp 客户端、线程体验、扩展性调研报告（只读调研，截至 2026-11-12）

**阅读范围说明**
- 凡标 `[现行]` 的，都以 2026-10 至 11 月的 `/docs/*`（页面带 `lastModified`）为准。
- 凡标 `[已移除]` 或 `[已被取代→新形态]` 的，都附官方原文给出的日期。
- 团队功能（workspace、groups、multiplayer、公开/他人可见性、service accounts、共享 runner、Huddle、企业管理/SSO/审计、Slack 团队集成）按要求只写一两行，标 `[团队功能·暂不做]`。
- 推测都标了 `[推测]`。读不到的写「未找到」。
- 一个重要前提：公告与文档的 URL 前缀是 `https://ampcode.com/news/`、`/docs/markdown/`、`/notes/`。下文来源只写路径。

---

## 0. 大改版时间线（Oryn 只复刻最新形态）

| 日期 | 事件 | 改了什么 / 删了什么 | 来源 |
|---|---|---|---|
| 2025-10-23 | Handoff 取代 `/compact` 压缩 | 官方理由：压缩有损，且鼓励冗长线程 | `/news/handoff` |
| 2025-10-28 | 命令面板（Ctrl+O）取代斜杠命令 | 面板可在不改 prompt 的情况下执行命令 | `/news/command-palette` |
| 2026-01 | 一轮删功能 | TODO（01-12）、Fork（01-13）、Amp Tab（01-15）、自定义命令→skills（01-29） | 各篇见 §2 |
| 2026-02-19 | 《The Coding Agent Is Dead》 | 宣布 VS Code/Cursor 编辑器扩展 3 月 5 日自毁；Amp 不再是 coding agent，而是"箭头" | `/news/the-coding-agent-is-dead` |
| 2026-05-06 | **Neo 重写** | 可远程控制、compaction-first、插件驱动；队列/steer；权限默认全放行；删 handoff、回滚、主题、用户可调 skill、`$` 手动 bash | `/news/neo` |
| 2026-05-27 | Drop the Neo | Neo 改回叫 Amp；去掉 `--take-me-back`；同日上线 passkey | `/news/drop-the-neo` |
| 2026-06-02 | 公共线程被移除 | 同时移除 Social Coding 公开主页 | `/news/social-coding` 顶部 Update |
| 2026-06-04 | **新 UI + 新侧边栏** | web/移动/CLI 统一；团队线程列表移到 Activity（`/feed`）；对外称"分布式系统 + 持久化执行" | `/news/agents-everywhere` |
| 2026-06-19 | 插件可创建自定义 agent | `amp.createAgent`、`registerAgentMode` | `/news/custom-agents` |
| 2026-06-30 | Agents in Orbs | 线程跑在远程 orb 上（执行环境主题，别人负责） | `/news/agents-in-orbs` |
| 2026-07-09 | **The Dial** | low/medium/high/ultra 取代 smart/deep/rush/large | `/news/the-dial` |
| 2026-07-18 | 订阅制 Beta | | `/news/subscriptions` |
| 2026-07-20 | Puck 与 Slack 集成 | | `/news/slack-integration` |
| 2026-08-10 | A Dial for You | 链接 ChatGPT 订阅后 low/medium/high 全走 OpenAI 模型 | `/news/a-dial-for-you` |
| 2026-08-27 | **TUI 侧边栏被移除** | | `/news/so-long-tui-sidebar`（此篇是别人已读清单里的，这里仅引日期） |
| 2026-09-08 | Steer, Don't Queue | 发消息默认 steer，队列改为显式操作 | 已读清单；`/docs/threads` 现行说明 |
| 2026-09-10 | Customize Your Dial | 可调模型、可自建 dial；10-23 更新为每个 mode 只剩 **Main Agent** 一个可调角色 | `/news/build-your-own-dial` |
| 2026-10-10 | Claude 订阅接入 | 新增 "Claude Code" mode，底层用 Claude Agent SDK | `/news/use-your-claude-plan` |
| 2026-10~11 | 各平台 App、Spaces、Artifacts、GitLab、Boxes 等 | | 见 §6 |

---

## 0.1 「老版功能，不要复刻」清单

| 老功能 | 状态与依据 |
|---|---|
| 编辑器扩展（VS Code/Cursor）、其内的 Review 面板、Agentic Review、Clickable Diagrams、Thread Forking in VS Code | `[已移除]` 2026-03-05 自毁（`/news/the-coding-agent-is-dead`）。Review、Clickable Diagrams 是该扩展里的功能（`/news/review`、`/news/clickable-diagrams`），随之消失。Review 的新形态见 §6 的 Changes 面板 |
| Amp Tab | `[已移除]` 2026-01 结束（`/news/tab-tab-dead`）。理由：Amp 已写了 90% 的代码，补全属于"人写代码、AI 点缀"的旧世界 |
| TODO 列表 | `[已移除]` 2026-01-12（`/news/todos-are-done`）。理由：Opus 4.5 不靠 TODO 也能跟踪自己的工作；写清单耗时又耗 token，还占 UI |
| Fork 命令 | `[已移除]` 2026-01-13（`/news/stick-a-fork-in-it`）。官方推荐新开线程并按 URL/ID 引用原线程。该页后来加注：handoff 也已移除 |
| Handoff 命令和按钮 | `[已移除]` 2026-05（Neo）。**冲突点**：`/docs/threads` 现行文档仍写"线程变长时对 agent 说 'Handoff and …'，它会新开线程并带上相关上下文"。可见 handoff 已从命令/按钮降级为**自然语言请求**，而不是被彻底删除。以现行文档为准 |
| 手动 `/compact`、"New Thread With Summary" | `[已被取代→新形态]` 自动 compaction，上下文 90% 满时触发（`/news/neo`） |
| 编辑/还原消息时回滚文件改动 | `[已移除]` Neo。理由：回滚一直是尽力而为，没跟踪 agent 运行代码生成的文件；现在由模型自己撤销（`/news/neo`）。CLI 的 Tab→`e` 编辑消息仍在（`/docs/prompting`） |
| 旧权限系统（`amp.permissions`、`amp permissions add/test`、delegate 外部 helper） | `[已被取代→新形态]` Neo 起默认**不再询问**。旧系统退化成内置插件，只在用户已配置时才加载；新做法是自写 `tool.call` 插件，返回 allow/reject-and-continue/modify/synthesize。`/notes/permissions` 里的 CLI 示例属于旧版，只有设计理念值得借鉴 |
| 旧模式名 smart/deep/rush/large | `[已移除]` 2026-07-09 被 low/medium/high/ultra 取代（`/news/the-dial`）。官方还把旧模式打包成可安装的插件，"几乎没人装"（`/news/who-cares-about-the-model`） |
| 自定义命令 `.agents/commands` | `[已移除]` 2026-01-29，并入 skills（`/news/slashing-custom-commands`） |
| 用户手动调用的 skill、CLI 内的 skill 管理子命令、自定义主题、`$`/`$$` 手动 bash | `[已移除]` Neo（`/news/neo`）。注意 `amp skill add/import/update` 仍存在（见 §5），它们管的是托管仓库，不是旧的子命令 |
| 公共线程、Social Coding 公开主页 | `[已移除]` 2026-06-02。这属于分享/可见性问题，对单用户本来也不重要 |
| `look_at` 工具 | `[已被取代→新形态]` `view_media`，可返回原图或裁剪/变换后的图（`/news/look-at` 顶部注）。modes 页的 System Models 列有 "View Media"，由 Gemini 3.7 Flash 驱动 |
| 旧的 `read_thread` 单次调用 | `[已被取代→新形态]` 子 agent（2026-07-02，见 §6） |
| `--take-me-back` | `[已移除]` 2026-05-27 |
| `@file` 文件提及 | **web/原生 App 上刻意不支持**，仅 TUI 有。官方理由：模型自己会找文件，你应该描述任务和结果（`/docs/prompting`） |
| Sourcegraph npm 包名 | `[已移除]` 现为 `@ampcode/cli`（2026-05-14）。仅见标题，未读正文 |

---

## 1. 客户端与线程体验演进（按时间，含被删除/反复修改的交互）

**2025-10 至 2026-02：CLI/TUI + 编辑器扩展时代**
- 2025-10-21：CLI 里按 Tab 选中旧消息，`e` 编辑、`r` 还原、`f` 分叉（`/news/cli-tab-navigation`）。现状：`e` 保留，`r` 与 `f` 的回滚/fork 语义已被 Neo 与 01-13 删除。
- 2025-10-23：Handoff 取代压缩，生成的 prompt 作为草稿出现在新线程里，用户可审阅编辑（`/news/handoff`）。反思写在 2025-12 的 `/notes/200k-tokens-is-plenty`：短线程更好，一个功能是一簇线程，用 URL 或 `@@` 引用串起来。
- 2025-10-29 至 12-08：线程成为一等上下文。`read_thread` 工具（`/news/read-threads`）、`find_thread`（关键词搜索，或"哪个线程改了这个文件"，类似 `git blame`，`/news/find-threads`）。
- 2026-01-13：Handoff 升级为"直接对 agent 说"，不再需要按钮或命令（`/news/ask-to-handoff`）。同日 Fork 被删。
- 2026-01-12：TODO 被删。

**2026-02-19：转折。** 编辑器扩展宣告自毁。官方理由：新模型不再需要手把手；把它们关在编辑器侧边栏里是在限制它们；真正的瓶颈是代码库如何为 agent 组织、组织如何使用 agent。

**2026-05-06：Neo。**
- 队列成为默认；steer 用来"尽快发送"：下一次工具结果上行时送达。`↑` 选中排队消息，`⏎` steer，`Esc Esc` 打断并立即发送。
- 默认不再询问权限。
- 上下文自动 compaction。迁移期间曾关闭一天，"所有人都抱怨"，beta 用户反馈"I love having auto-compaction. NOT missing handoff"。
- 5000 条消息线程的对比：CPU 84.1%→17.4%，空闲内存 1814 MB→540 MB。

**2026-06-04：新 UI。**
- 一个 UI 同时监看和驱动所有 agent，覆盖桌面 web、移动 web 和 CLI。CLI 里 `Ctrl+\` 显示侧边栏。
- 团队线程列表迁移到 Activity（`/feed`）。
- 官方原文强调目标：同时跑多个长任务 agent，不需要在终端标签页之间来回切（`/news/agents-everywhere`）。

**2026-06 至 08：线程内能力扩张。**
- `read_thread` 子 agent 化（07-02）。
- 插件 UI 同步到 web（05-28）。
- Agent to Agent（07-17）。
- Puck、Slack、定时、语音（07-20 至 08-18）。
- TUI 侧边栏被删（08-27）。

**2026-09 至 11：多端成熟。**
- Steer 成为默认（09-08）：现行规则是"忙时发消息默认 steer；⌘/Ctrl+Enter 排队；Esc Esc 强停"（`/docs/threads`、`/docs/prompting`）。**反复修改点**：Neo 时默认排队，9 月改成默认 steer，改动也体现在 iOS 长按发送按钮和 CLI 的 `prompt: queue message`。
- Amp 原生 App：macOS、iOS 为 08-28，Android 10-27，Windows、Linux 在 `/docs/apps` 中；Spaces（11-06）；Artifacts（11-02）；Boxes（11-10）。

---

## 2. 逐篇阅读要点（本轮新读）

**消息队列/steer 的现行规则** `[现行]`（`/docs/markdown/threads`、`/docs/markdown/prompting`）
- 忙时发送默认 steer，即当前一步（命令或 thinking 块）结束后送达。想等整轮结束再发，就用 ⌘/Ctrl+Enter、iOS 长按发送，或 CLI 的 `prompt: queue message`。
- 默认行为可在 *Settings → Composer & Dictation → Default Message Send Behavior* 改。
- 对排队消息按 `Enter Enter` 可提前 steer。Ship/Review 等系统代写的 prompt 始终排队。

**Drop the Neo**（2026-05-27）
- Neo 对所有人开放，改回叫 Amp。
- 称"基础设施更稳定，客户端更有韧性"。
- 去掉 `--take-me-back`，旧版只能 `npx` 钉死版本运行。

**Amp, Rebuilt（Neo）**（2026-05-06，细节见 §3）
- 架构关键词：remote-controllable、compaction-first、plugin-powered。
- 插件 API 正式发布：`amp.on`、`registerTool`、`registerCommand`、`ctx.ui.notify/confirm/input/select`、`amp.ai.ask`。
- 官方哲学："为 2026 年及以后模型的能力而建，不为过去而建。"功能一旦把你绑在旧用法上就删。

**Agents, Everywhere**（2026-06-04）
- 新 UI 和侧边栏，覆盖 web、移动、CLI。
- 明确说 Amp 现在是"带持久化执行的分布式系统"。
- 团队线程列表迁到 Activity。

**Proof of Human**（2026-05-27，6-25 更新）
- 对远程控制等敏感操作要求 passkey。
- 6-25 起不再用 "sudo" 这个词，统一叫 passkey authentication。
- 用户可自己开启（Settings → Security）。`[团队功能·暂不做]` 管理员强制和部分管理员操作必须 passkey。

**Read Bigger Threads**（2026-07-02）
- `read_thread` 重写为子 agent。原因：线程因 compaction 可持续数周，最长的已压缩 68 次以上，不压缩将超过 2100 万 token。单次大 prompt 让 LLM 抽取，会偏重头尾而忽略中间。
- 子 agent 搜线程、读消息，并核对后续是否修订或回滚。第一版"遇到第一个相关命中就答"，常常答成后来被回滚的尝试。
- 模型换成 GLM 5.2 并调 prompt：不止步于第一个命中；工具调用只记录"尝试"，不代表结果；用 compaction 定向，但对精确要求回查原始消息。
- 在当前线程里也能回看三周前的决定，而不是信任 compaction 摘要。新线程里 Enter 两次可引用刚离开的线程。

**Find Threads**（2025-12-08）
- `find_thread` 两种搜索：关键词，或"哪个线程改了某文件"。与 `read_thread` 配对（先找再读）。
- 现行查询语法见 §6。

**Explain Usage**（2026-08-21）`[现行]`（单用户，值得做）
- 向 Puck 提问 token/credit/orb 用量，如"今天哪些线程最耗 token/orb 时间""最贵的 5 个线程用了哪些模型"。
- Usage 页有 *Explain Usage* 按钮。
- CLI：`amp usage --details`、`amp threads usage <id> --details`。

**Service Accounts**（2025-11-03）`[团队功能·暂不做]`
- 工作区拥有的账号，有自己的 API key 和第三方凭证，创建的线程自动对工作区开放且 multiplayer。
- 典型用法：CI 里 `AMP_API_KEY=<key> amp -ox "…"`；PR 评审 bot。
- 官方警告：只给它你愿意授予全体成员的凭证。

**GitLab**（2026-11-05）、**GitHub listeners**（10-12）、**Assign It To Amp**（Linear，10-19）、**Slack**（07-20）
- GitHub：agent 开 PR 后可订阅该 PR 的评论、评审和最新 commit 的 CI，结果回到**同一线程**。基于集中的 GitHub Webhook，由 Amp 侧统一接收，再决定转发给单个 orb 还是每次新建 orb。
- 可建"orb spawner"：每个新 issue/PR（可按 label 过滤）新开一个 orb，打开线程时摘要已备好，后续评论进同一线程。设置在 *Settings → Integrations & MCP → GitHub/GitLab → Event Subscriptions*，可暂停、编辑、查看已启动的 orb。设置线程不必保留。
- GitLab：orb 以你的身份 clone/push，自带已登录的 `glab`，支持 gitlab.com 和自托管。
- Linear：把 issue 指派或 @Amp，即在 Linear 创建 Agent Session 并启动 orb；回复回到同一 session；该 session 也出现在 Amp 侧边栏，可完整查看。
- Slack：@Amp 的消息发给你**个人的 Puck**。Puck 先回复线程链接，之后沉默，直到完成、阻塞或需要你时才再发一条。个人 Puck 用你自己的权限和连接。Slack 里可发原生卡片（表格、图表、按钮）。发 Slack 消息默认先审批。（`/docs/markdown/puck/slack-integration`）
- `[团队功能·暂不做]` 工作区级 Slack 连接、服务账号 Puck。

**Use Your Claude Plan**（2026-10-10）
- 新建线程选 *Mode > Claude Code* 并绑定 Claude 订阅。
- 底层用 Claude Agent SDK，替代 Amp 自己的 agent。
- 官方强调仍支持 orb/runner、portals、线程互发消息与编排。

**A Dial for You**（2026-08-10）
- 链接 ChatGPT 订阅后，low/medium/high 的主 agent、oracle、线程阅读器、代码评审**全部**走 OpenAI。
- 此前混用其他模型，部分仍计 Amp credits，造成困惑。ultra 不变，仍是 Claude Fable 走 credits。
- 官方理由："前沿模型已趋同"。

**Customize Your Dial**（2026-09-10，10-23 更新）
- *Tune Modes*：为内置模式选主 agent 的模型和推理强度。10-23 起只剩 Main Agent，一个角色。Task 和 Read Thread 子 agent 与发起它们的线程同模型同强度。
- *Build Dial*：把 2 至 4 个模式（内置或插件自定义 agent）拖到转盘上排序，长按保存。web 用模式选择器切换，CLI 用 `Ctrl+S`。个人选择覆盖工作区共享 dial。
- 图里的 Oracle/Subagents 行是旧 UI，别照抄。

**Who Cares About the Model?**（2026-07-29）
- 默认模型从 Opus 4.8（smart，占 55% 新线程）换成 GPT-5.6 Sol（medium），无人抱怨。上线一周后 smart 归零，四个 Dial 档占 93%，medium 单独占三分之二，69% 的用户只用 medium。
- 官方认为真正影响产出的是任务难度、给的上下文、审查力度，而不是模型。

**MCP in Orbs**（2026-08-19）
- 在 ampcode.com 的设置里连接远程 MCP（Streamable HTTP + OAuth 或 Bearer），orb、TUI、Puck 都能用。
- 明确**不支持** MCP Apps、Resources、Prompts。

**Efficient MCP Tool Loading**（2026-01-08）
- MCP 工具与 skill 绑定：skill 目录里放 `mcp.json`，调用 skill 时才把工具定义加入上下文。
- 例：chrome-devtools 26 个工具占 17k token，用 `includeTools` 只载 4 个，约 1.5k。
- `[现行]`，见 §5。

**Custom Agents**（2026-06-19）
- 插件 `amp.createAgent({name, model, instructions, tools, display:{label,color}})`。
- 可当子 agent 用（`agent.run`）、`registerAgentMode` 注册为可选主模式，也可 `createThread({show:true, parentThreadID})` 新建线程。
- `thread.appendUserMessage` 在 Amp 接受消息后立即返回；`thread.waitForResponse()` 才等待回复。每个自定义 agent 有自己的 orb 颜色。

**Agents in Orbs**（2026-06-30，仅取体验点）
- 在同一界面、用同一套控制管理本地和远程 agent。
- `amp sync <thread>` 把 orb 的改动同步回本地。
- `amp -ox` 从终端直接启动 orb 线程。

**Subscriptions**（2026-07-18）、**Restack**（09-11）、**No Mailmap**（08-28）、**Setup Without a Commit**（08-25）、**Multi-repo**（08-27）
- 订阅：包含的 orb 用量大到不必数分钟。设计理由是让用户"杀掉单例本地开发环境"。
- Restack：一键把线程的所有改动（含已提交和未提交）整理成更干净的 commit 序列，最终文件内容不变。
- 独立 Git 身份：与 Amp 登录邮箱分离，需要验证邮箱；设置 `GIT_AUTHOR_*` 和 `GIT_COMMITTER_*`；只为已验证的邮箱签名。个人默认 + 项目覆盖。
- 无需提交的 setup：把 pre-clone 和 pre-setup 脚本存在项目设置里，而不是仓库里。
- 多仓库项目：最多 20 个附加仓库，放在 orb 的相邻目录；Changes 面板跨仓库显示 diff；只有主仓库的 `.agents/setup` 会自动运行。

**The Coding Agent Is Dead**（2026-02-19）、**Tab, Tab, Dead**（01-15）、**TODOs Are Done**（01-12）、**Slashing Custom Commands**（01-29）、**Stick a Fork**（01-13）：见 §0.1，理由已逐条列出。补充：Fork 删除时官方给出的替代是 `thread: switch to previous/parent` 与 Thread Map，Thread Map 当时仅 CLI。

**Handoff, Please**（2026-01-13）
- 对 agent 说"Handoff and 实现这个计划"即可新开线程并携带相关上下文。
- 现行文档仍保留这一请求语义。

**Painter / Look At / Clickable Diagrams / Liberating Code Review**
- Painter：生成和编辑图片。2026-01 用 Gemini 3 Pro Image；2026-05 改为 GPT Image 2；现行 modes 页是 GPT Image 2.5 Sunburst。`/docs/model-routing` 里还有"个人 Google Painter + Nano Banana 2"选项。
- Look At：见 §0.1。
- Liberating Code Review（2026-02-04）：评审 agent 与 UI 解耦。`amp review`，或在任意线程说"review the outstanding changes"。Checks 放在 `.agents/checks/*.md`，目录范围内每条 check 起一个独立 agent。
- Clickable Diagrams：随编辑器扩展移除。

**Command Palette / Review / CLI Tab / Agentic Review / Go Deep / Look Ma No Flicker / Toolboxes / Social Coding / Enterprise Managed Settings**（均为 2025 的旧版文章，仅用于理解演进）
- 命令面板 `Ctrl+O` 沿用至今。
- Review 面板设计理由："瓶颈是读，而不是写"；逐项批准太细，PR 太晚；流程是选 commit 范围 → AI 摘要 → 阅读顺序导览 → 可编辑 diff → 自动 commit message。其思想后来变成 Changes 面板的智能排序和 Ship（见 §6）。
- Agentic Review 已随扩展移除；其"评审 agent 输出可操作清单，再回喂主 agent"的思路由 `amp review` 继承。
- Go Deep：`deep` 先静读 5 至 15 分钟再改，与 `smart` 互补。已被 Dial 取代。
- Look Ma No Flicker：全屏 TUI，自研框架，支持鼠标、overlay、按钮。
- Toolboxes：`AMP_TOOLBOX` 目录里的可执行文件作为工具，以 `TOOLBOX_ACTION=describe/execute` 通信，参数走 stdin。老机制；现行取向是插件（见 `/docs/markdown/customize/plugins`），我没读到 toolbox 在现行文档里的状态，`[未找到]`。
- Enterprise Managed Settings：见 §5 `[团队功能·暂不做]`。

**What the Puck?**（`/what-the-puck`）
- 一个 138 个 Puck 形象的画廊页，社区也可贡献。
- 与功能无关，只能说明 Puck 有多种外观，可作为可换肤的头像素材。

---

## 3. Neo 架构的全部技术描述

**公告原文给出的架构**（`/news/neo`、`/news/agents-everywhere`）
- 三个关键词：remote-controllable、compaction-first、plugin-powered，外加"快得多"。
- Remote control：CLI 起的线程可从 ampcode.com 实时查看、发消息、排队/取消排队、取消当前任务。"这个架构是我们重写的原因，远程控制只是开始。"
- Compaction：上下文 90% 满时自动压缩，摘要后开新窗口继续。**它是新架构里最先加的功能**。
- 6-04 的描述：Amp 是"分布式系统，对 agent 循环做持久化执行（durable execution），另有插件 API"。

**文档给出的客户端/服务端职责划分**（`/security`，`[现行]`）
- **Client**（主要是 CLI）：本地代码与上下文管理、本地设置、本地线程历史，并执行服务端请求的工具。
- **Server**（ampcode.com，GCP 多租户）：认证、账户、线程同步与存储、用量跟踪。它还运行 **thread actor**：服务端授权推理并下发**请求范围凭证**后，由 thread actor 去连各 LLM provider。
- 文中写线程存在 PostgreSQL。播客则提到"storage nodes"，说明存储层有自建分布式部分。两者的关系 `[推测]`：Postgres 之外还有一层自研存储，官方未写明。
- 每个线程客户端都保持一条 **WebSocket**，代理必须放行 WebSocket 升级。
- Amp 客户端不索引、不上传整个代码库，只发被选作上下文的片段。

**执行位置与 actor**（`/docs/plugin-api`，经 grep 取得）
- 执行位置（executor）分 `local`（当前客户端）、`orb`（云沙箱）、runner（运行中的 `amp --no-tui` 进程）。
- `serverOnly` 的 agent 完全跑在 thread actor 上，不挂 executor。这类 agent 的工具 `execution` 默认为 `server`：在小型、无仓库 checkout、每线程保持热的独立沙箱里运行。
- `Agent.createThread()` 建立的后台线程独立于调用者的生命周期。
- 插件事件 handler 是**至少一次**（at-least-once）投递：用 `event.id` 做幂等键；handler 有 30 秒，超时 `ctx.signal` 被 abort 并重试，失败按指数退避重试。
- 对已有 orb 的请求会唤醒或恢复 orb；归档线程会让对应 URL 不可用，并暂停排队投递。
- 直接的插件工具调用（`ctx.tools.call`）**不会**在连接丢失后自动重试，也不进 transcript。

**重连与离线行为**
- MCP 文档：线程的 actor 启动时加载远程工具列表；actor 休眠后被唤醒时重新加载；`code_exec` 进行中的调用保留已选的 server UUID，actor 重启后重试的调用用当时最新的目录。
- Mac 作为 runner 睡眠时（`/docs/apps/mac`）：约 90 秒内从 runner 选择器消失；已在其上运行的线程**暂停**，命令和文件编辑等待 runner；唤醒后 runner 通常一分钟内重连，线程从停下的地方继续。App 提供 *Keep This Mac Awake*（仅插电时有效）。
- 播客（S2E4，2026-09-11）：持久化执行意味着"agent 在思考时你可以合上笔记本，甚至 orb 挂了，agent 循环依然继续"。官方同时承认：沙箱各家能力不同（有的能内存快照，有的不能恢复），所以不做跨提供商的"最低公约数"冗余。
- 一个线程仍在 CLI 里时，web 端发消息依赖 Cross-Client Access（`/docs/cli/remote-control`）。禁用时 web 端显示"Cross-client access is disabled for your workspace"，代替输入框。
- 官方没有披露持久化执行的具体框架、checkpoint/replay 算法或 exactly-once 保证 `[未找到]`。

**性能数据**（`/news/neo`）：5000 消息线程，CPU 均值 84.1%→17.4%，峰值 86.3%→25.8%，内存 1814→540 MB。

---

## 4. 多端同步与通知的用户可见规则

**同步**
- 同一线程 URL（`/threads/T-…`）在 web、CLI、macOS/Windows/Linux/iOS/Android 打开是同一个对象。线程在哪里运行与在哪里查看相互独立（`/docs/threads`）。
- 可以在手机或另一台电脑继续对一个 orb 线程发消息。`amp threads continue T-…` 把 CLI 接到线程上，`amp threads continue T-… -ox '消息'` 无需挂载就可向 orb 线程投递一条消息。
- 一个在 CLI 里启动的线程在 web 上保持开放，通过 Cross-Client Access 发消息。
- App 支持最多 5 个账号，*Switch Account* 切换（`/docs/apps`）。
- 点击 `ampcode.com/threads/...`：手机和平板直接进 App；桌面首次点击会询问"是否总是用 App 打开"，选择按账号保存、按平台分开（Mac/Windows/Linux），在 *Settings > Appearance* 修改。

**通知规则**（`/docs/apps/{mac,ios,android}`，`[现行]`）
- 触发时机：**线程准备好接收你的下一条消息时**。点击即进入该线程。
- 正在查看的线程**永不**发通知。
- 账号级：*Settings → Notifications* 选择"你的账号收哪些通知"，以及"正在 Amp 里活跃时是否跳过"。
- 设备级：macOS 的 *Allow Notifications*；iOS 的 *App Settings* 可关闭或在前台抑制；Android 用系统设置关闭。
- 哪个设备响铃：官方文档没有明确写出多设备间的路由或去重规则 `[未找到]`。能确认的只是：账号级开关加设备级开关，加"当前正在查看的线程不通知"。具体是否所有设备同时推送 `[推测]`：看起来是都推送。
- 通知按钮：推送通知上有操作按钮。当要求 passkey 时，需要 passkey 的动作（如应对他人请求开启 multiplayer）在推送按钮和 Slack 里不可用，只能打开线程处理（`/docs/cli/remote-control`）。
- **未读**：侧边栏有未读圆点，*Mark as Unread* 可手动恢复圆点。Puck 对话列表里"有未读回复"的对话会被标记（`/docs/puck`）。
- **Snooze**：隐藏线程并**静音其通知**。快捷键 `Ctrl+G Z`，手机在 More Actions 里。
  - 线程能自己回来时（agent 在工作、自动化可运行、multiplayer 开着），菜单提供 *Snooze Until I'm Needed*。它在以下情况结束：有人发消息；agent 对一条消息回复完毕；定时运行的回复带来新信息（仅"无变化"不算）；线程的自动化都已结束。工具调用不算。
  - 否则提供 *Until Tomorrow*，午夜至 8 点之间显示 *Until Morning*（到下一个 8 点）。
  - 也可对 agent 或 Puck 自然语言说"snooze 到周一 9 点""snooze 两周""等你需要我时"。
  - 被 snooze 的线程正等待审批或回答问题时，会在侧边栏**显示**；答完后重新 snooze。Snooze 不停 agent，也不暂停自动化（定时运行继续，且继续花 credits，只是"安静"）。
- **Slack**：Puck 回复链接后保持安静，完成、阻塞或需要你时才发一条；Slack 的 working indicator 短暂显示每一步，等待输入时显示"在 Amp 里等待输入"。
- **Inactive Last 72h**：72 小时没动静的线程折叠进该分组，分组头的按钮可一键全部归档；Snoozed 和 Archived 在底部各有分区。置顶（Pin）优先，其次按项目分组的活动线程。

---

## 5. 插件 / skills / MCP 的分发、共享与安全模型（全部 `[现行]`）

**分发层级**（`/docs/markdown/customize/plugins`、`.../skills`、`.../global-plugins-and-skills`）
- 插件来源与优先级（同名时）：**project（`.amp/plugins/`）> system（`~/.config/amp/plugins/`）> personal > workspace**。
- skill 的名称查找顺序，先到先得：`~/.config/agents/skills` → `~/.agents/skills` → `~/.config/amp/skills` → 项目及父目录 `.agents/skills` → `.claude/skills` → `~/.claude/skills` → `~/.claude/plugins/cache` → `amp.skills.path` → 个人 skill 仓库 → 工作区 skill 仓库 → 官方 skills。本地同名 skill 遮蔽仓库里的，个人遮蔽工作区，二者都遮蔽官方。
- 兼容 Claude：读 `.claude/skills`，可用 `amp.skills.disableClaudeCodeSkills` 关掉。
- 本地 skill 目录递归搜索到 5 层。插件也能打包 skill，限定名是 `<plugin>:<skill>`，不与裸名竞争。

**共享模型**
- 个人和工作区的插件、skills 都存在**托管 Git 仓库**里（"global"，不绑定项目或机器）。
- 管理方式首选"对 Amp 说话"：它找到正确的仓库，修改、提交，再**询问是否 push**；push 即发布。新线程自动加载；`plugins: reload` 或"Reload my skills"在当前线程生效；Puck 约一分钟内拿到新 skill。
- 分享：*Personal Settings → Plugins/Skills → Share*，设置为"工作区可用"，复制 URL，粘到线程里让 Amp "Import this shared plugin into my personal plugins"。导入时**记录来源**以便更新（`amp plugins update`、`amp skill update`），不会静默覆盖本地修改。Slack 里粘链接会展开出详情。
- CLI：`amp plugins repositories`、`amp clone user-plugins|workspace-plugins|user-skills|workspace-skills`、`amp plugins add <url>`（单文件，装到本机）、`amp skill add <source> [--global]`、`amp plugins add --auto-update @amp/grok-46-mode`。
- 仓库**可要求签名 commit**（仓库 Advanced 设置）。
- 单用户版可只做 personal + project + system 三层；`[团队功能·暂不做]` workspace 层与分享给同事。

**硬限制**
- skills 仓库：每仓库 200 个 skill（push 超出时按字母序取前 200，其余静默不可用）；每 skill 最多 200 个文件、单文件 10 MiB、单 skill 25 MiB、仓库总量 25 MiB；托管 skill 必须是文本文件。
- 插件仓库：每仓库 50 个插件；目录插件最多 500 个文件；单文件 10 MiB；单插件和仓库总量 25 MiB。**超限的插件直接不加载，也不在设置里出现，即使 push 成功**。限制不适用于项目和 system 插件。
- Puck 没有 executor：只能读 skill 的说明和文件，不能执行脚本，**插件不会在 Puck 里加载**。Spaces 同理。

**MCP**（`/docs/markdown/customize/mcp`）
- 两类：本地 `amp.mcpServers`（含 URL 型）；**远程定义存于 ampcode.com**，由 Amp 服务器连接（要放行 Amp 的出口 IP），用 `amp mcp remote --personal|--workspace|--project add …` 管理。
- 官方推荐把 MCP 打包进 skill 的 `mcp.json`，或写在 `SKILL.md` frontmatter 的 `mcpServers`（后者优先）。线程开始时只暴露 skill 描述，调用 skill 时才追加工具；用 `includeTools` 限制并支持 glob。本地服务在线程工作目录启动，并设置 `AMP_SKILL_DIR`。
- 凭证：OAuth、Bearer（`--bearer-token-file`，**不接受命令行参数传密钥**）、自定义 headers；标为 Secret 的 header 加密存储，之后再也不显示。orb 里可用 `${amp:id-token}` 做工作负载身份，且只发往同一 origin、拒绝重定向。
- 远程服务的**逐工具策略**：Allow / Ask First / Disabled。Ask First 在 `code_exec` 里会让调用等待线程所有者批准，子 agent 无法等待，所以不能调用。批准记录写在 transcript 里，但 agent 看不到。服务端可设 `_meta["anthropic/requiresUserInteraction"]=true` 强制 Ask First，此时只能禁用，不能放行。
- 工具发现用 `tool_search` 与 `code_exec`：远程服务器作为模块导入，模块名是其 ID。远程工具列表缓存，超过一天的在后台重取。
- 同 ID 覆盖：project > workspace > personal。
- 单用户相关：`[团队功能·暂不做]` MCP 注册表白名单（企业）；`Use in Multiplayer Threads` 开关。

**插件安全模型**
- 官方一句话：**插件在你的环境里运行代码，只加载你信任的**。分享页让你在导入前检查来源。
- 权限不再是内置系统：改由 `tool.call` 事件拦截（allow / reject-and-continue / modify / synthesize），可用 `ctx.ui.confirm` 弹窗，可用决策模型做风险判断。
- 决策模型 `amp.ai.decide`（`/docs/markdown/customize/decision-models`）：默认 TypeSafe **Jev**，可选 `openai/gpt-6-luna`。输入是 `state` 加 1 至 20 个问题，问题类型为 `noul`（概率）、`choice`、`score`，亚秒级返回、带校准概率。**不会自动回退到另一个模型**，被拒则抛错且不发送任何数据。Amp 自己用 Jev 给 diff 文件排序、建议线程标签。
- 密钥脱敏：在最底层识别并替换为 `[REDACTED:amp]`，不进入 LLM、本地缓存和服务器。这是尽力而为，漏掉时可编辑发出的消息覆盖线程内容，必要时轮换密钥（`/security`）。
- 凭证存放：CLI 在 `~/.local/share/amp/secrets.json`；"尽力避免读取 `.env` 等凭证文件"。
- 泄露 token 可通过 `POST /api/revoke` 吊销（`/security`）。

**Decision Models 的另一个相关事实**：`/docs/plugin-api` 说明 `amp.system.executor.kind` 为 `local|remote|unknown`，可据此在插件里判断环境。

---

## 6. 「高级但好用」功能补充清单（均为单用户可用，与已读清单不重复）

**线程与上下文**
- 线程查询语法：`id: label: parent: project: repo: author: visibility: archived: snoozed: pinned: commit: pr: after: before:`（及 `updated_after/updated_before`），日期支持 ISO 与相对天/周；Activity 过滤器存在 URL 里（如 `/feed?time=7d&q=label:bug`）。`[现行]` `/docs/prompting`
- Activity 过滤器的默认值可在浏览器本地保存（右键选项 → *Use … as Default*）。`[现行]` `/docs/threads`
- `parent:` 过滤器只返回直接子线程。`[现行]`
- 线程 More Actions：Pin、Color（侧边栏和标题栏着色）、Labels、Rename、Snooze、Archive、Mark as Unread、Cost & Usage、Export。`[现行]`
- *Archive N Threads* 一次归档父线程及全部子线程；归档**不会**级联归档被它创建的线程。`[现行]`
- 导出：线程 URL 加 `.md`；*Export* 提供 Markdown；创建者可导出完整 JSON；CLI 的 `amp threads markdown|export`。`[现行]`
- 线程宽度：Settings > Appearance 设 Wide/Full，或命令面板切换。`[现行]`
- 提交携带 `Amp-Thread-ID` trailer，`git log` 可回溯到对话。`[现行]`
- Puck 线程搜索：`puck:true` 匹配与 Puck 本身的对话。`[现行]`

**Changes 面板（评审）**
- 智能文件排序：把最能解释改动的文件放前面，生成物等次要文件淡化。diff 变化后保持已应用的顺序，一轮结束重新计算；若新顺序会重排，按钮上出现蓝点，点 *Apply Updated File Order* 才应用。超过 500 个文件或只读时，退回字母序。`[现行]`
- 排序由 Jev 驱动（10-12 "First Things First, Faster"，只见标题）。
- commit 选择器：All Changes / Uncommitted / 每个 commit（短 SHA，逐条看）/ Compare With Branch；无需 push 或开 PR 即可逐 commit 审阅。`[现行]`
- 按钮：Ship（按项目的 Ship Behavior 提交并推送）、Review、Sync、Restack。`[现行]`
- 内置保存的 prompt：`ship`、`update-base`、`refactor-commits`、`review`，可在 *Prompts* 页编辑，也可添加自己的并设为 Changes 面板按钮；Puck 可按名称发送。`[现行]` `/docs/puck`
- 插件钩子 `changes.prompt` 可往 Ship prompt 追加内容，用户在弹窗里可见可改；`agent.start` 事件带 `changesWorkflow`。`[现行]`

**输入与语音**
- 听写：默认 `Cmd/Ctrl+Shift+D`，再按停止；长按则松开即发送。自定义词汇表（Settings 或 `amp config vocabulary add`）。门户评审组件也支持听写，且只发送录音音频，不带线程消息或评论草稿。`[现行]`
- Call Puck：`Ctrl+Shift+/`；macOS 全局 `⌃⌥⇧P`；Windows 任务栏跳转列表；Linux `amp-desktop --call-puck`。回答用语音，全文同时出现在聊天里。可写"语音通话专用指令"。Puck 的速度（Normal/Fast/Plaid）与模型可调。`[现行]`
- iOS：Dictate to Puck、Call Puck 快捷指令；通话在锁屏和状态栏像电话一样显示。Android：Quick Settings 磁贴。`[现行]`
- Share to Puck：从任意 App 的分享菜单把图片（Android 还可文本和文件）发给 Puck。`[现行]`
- Enter 行为可在 Composer 设置中互换（Enter 换行、⌘Enter 发送）。`[现行]`
- 提及 skill：直接在 prompt 里按名字提，不需要斜杠。`[现行]`
- `ask_user_choice` 已成为内置工具（2026-09 更新），不再需要插件。`[现行]`

**Artifacts（单人也有用）**
- 附着于线程的文档（计划、报告、会议记录、跨线程调研）。agent 通过 `artifact` 工具读写 Markdown，每次保存新增 revision。若人在 agent 工作期间保存了更新版本，匹配的编辑会应用到新版，否则 agent 的更新被拒绝，并让它合并而不是覆盖。
- 子线程的 agent 也能编辑父线程的 artifact，并给父线程发消息说明改了什么。每个 artifact 上限 2 MB，图片随线程存放。可导出 Markdown、HTML 或 PDF。`[现行]`

**自动化与 agent 协作**
- Agent to Agent：agent 可开启其他线程（orb、其他项目或 runner），发指令、传文件、取回结果。注意：线程有各自独立的工作区，消息不会自动传文件或 commit。官方建议：相互耦合的工作留在当前线程，独立且有明确产出的才外包。并可用"一批 fresh orb 且不给提示"测试代码库对 agent 的友好度。`[现行]` `/docs/orbs/agent-to-agent`
- Right on Schedule（07-21）和 Automations：agent 自己设置计划并唤醒自己（已读）。
- 流式 JSON：`--stream-json` 与 `--stream-json-input`，输入可含 `steer: true`、base64 图片；输出消息 `system/init`（含 tools、mcp_servers 状态、`agent_mode`、仍在运行的后台子 agent）、`assistant`、`user`、`result`（含 `usage`、`permission_denials`）、`subagent_finished`。`--stream-json-thinking` 增加 thinking 块，与 Claude Code 格式不兼容。MCP 状态枚举：`awaiting-approval/authenticating/connecting/reconnecting/connected/denied/failed/blocked-by-registry`。`[现行]` `/docs/cli/streaming-json`
- SDK：TypeScript `@ampcode/sdk`，Python `amp-sdk`，`execute()` 流式迭代。`AMP_API_KEY` 在 Security Settings 获取。有 Neo 之前的旧版钉死版本。`[现行]`
- Slack 卡片：`slack_write` 的 `card{title, subtitle, blocks}`，block 类型有 text/fields/table/chart/pie/image/links/buttons/divider，一张卡最多 1 表 2 图。点回复按钮等同于以你的身份回复 agent。`[现行]`

**模型与成本**
- Model Routing：连接分四类（API key、订阅、云平台、路由器/网关，含 Custom URL 的 4 种 API 格式）；个人连接按**从上到下**的优先级，其后才是工作区，最后是 Amp。模型映射用 `*/*`、`-anthropic/*`、`a -> b` 语法，**后匹配的行优先**。`amp config model-providers` 全套命令，带 `check-access`。每个请求带 `x-amp-thread-id` 头，便于网关按线程分组日志。支持 GitHub Copilot 订阅。`[现行]` `/docs/customize/model-routing`
- Modes 页（`/modes`，`[现行]`）：Low = GLM-5.3-Flash；Medium = Opus 5.5；High = GPT-6 Astra；Ultra = Fable 5.2；Puck = Opus 5.5 或 GPT-6.1 Sol。系统模型：Dictation = GPT Transcribe；Call Puck = GPT Live 1；View Media = Gemini 3.7 Flash；Painter = GPT Image 2.5 Sunburst；Titling 与 Compaction = 同线程所用模型。
- Plaid Speed（09-29，只见标题）与 Puck/模式的 Fast/Plaid 速度档（Puck 页）。Fast/Plaid 更贵，菜单显示价格差。

**安全与隐私（单用户相关项）**
- 最小数据保留（Minimal Data Retention）对所有用户开放；自带 key 则适用你自己账号的条款。`[现行]` `/security`
- 线程删除后 30 天内清除全部数据；orb 的生命周期与线程绑定，删线程会删 orb；项目快照最长复用 72 小时。
- 个人 ChatGPT/Grok 订阅走消费者账号条款，**需自行在提供商那边关闭训练**，Amp 无法替你设置。
- 防火墙需放行：`ampcode.com`（含 WebSocket）、`auth.ampcode.com`、`static.ampcode.com`、`*.onamp.dev`、`tunnel.ampcode.com`。
- 2026-10-22 的安全更新：移除对被入侵 CA 的信任，要求 `amp update`。
- `[团队功能·暂不做]`：SSO、SCIM、审计日志、workspace 实体配额、Allowed Models、MCP 注册表。

---

## 7. 对话区呈现规则演进（第 4 问）

**证据范围**：我只读到以下几条，折叠、分组、thinking 展示的细则在 `/news/less-noise` 等篇里，上一轮已有人读过，本轮未复读，这里不重复，请合并时参考他们的结果。

- **TODO**：2026-01-12 删除，理由是占空间且耗 token。
- **thinking**：CLI 流式 JSON 默认不输出 thinking，需 `--stream-json-thinking`。
- **工具调用**：按线程保留审计轨迹（prompt、回复、工具调用、结果、附件），用于事后判断是否发生提示注入（`/security`）。
- **审批记录**：Ask First 的批准或拒绝记录写在 `code_exec` 调用下方，含工具、结果、操作者用户 ID 和时间，**agent 看不到**。
- **diff**：不再逐项批准或逐条阅读，改为在 Changes 面板按智能顺序集中评审（`/news/review` 的设计理由 → 现行 Changes 面板）。大 diff（>500 文件）退回字母序。
- **图表**：Mermaid 图可点击跳转代码，仅编辑器扩展时代有（`[已移除]`）。现行替代是 Visualize This（10-29，在线程里直接展示图表、时间线、示意图、原型，已读）和 Artifacts。
- **宽度**：Wide/Full 可选。
- **Slack 卡片**可在 Slack 里渲染表格、图表。
- **web/App 输入区**：没有文件 `@` 提及（刻意）；CLI 的 `@@` 用于搜索线程。

---

## 8. 对 Oryn 的启示（每条一行）

1. 先做"线程 = 一等对象 + 稳定 URL + 多端同一份"，再做 UI；官方多个功能（read/find/mention/handoff）都建立在这点上。
2. 架构上把 **agent 循环（thread actor）、执行环境（executor）、客户端**三者拆开；执行位置可选 local/orb/runner/serverOnly。
3. 自动 compaction（90% 触发）是默认，不暴露手动 compact；同时保留"回查原始消息"的子 agent，避免被摘要误导。
4. 压缩之外保留自然语言的 handoff 请求，不做按钮和命令。
5. 消息发送默认 steer，排队是显式操作；Esc Esc 强停。这是已多次改动的交互，别照搬 Neo 时的默认排队。
6. 通知只在"线程准备好接收下一条消息"时触发；正在看的线程永不通知；Snooze 同时静音通知且不停 agent。
7. 通知设置做成账号级加设备级两层；多设备路由规则官方没写，Oryn 需自己定。
8. 未读圆点加 *Mark as Unread*，Puck 对话同样有未读标记。
9. 删功能要有"模型已不再需要"的理由，并保留迁移路径：删 TODO、回滚、自定义命令都有理由与替代品。
10. 权限不要做成内置规则引擎：给 `tool.call` 钩子加决策模型，默认放行；这是 Neo 的明确立场。
11. 决策模型（亚秒、带概率）值得作为插件能力引入，用于风险判断、diff 排序和标签建议。
12. 插件存成 Git 仓库，push 即发布，导入时记录来源，更新不静默覆盖。
13. skill 与 MCP 绑定、按需加载工具；`includeTools` 加 glob；同名遮蔽顺序要文档化。
14. 托管仓库的限制要在**加载时**报错，而不是 push 成功后静默忽略（官方这点是坑）。
15. 远程 MCP 工具做 Allow/Ask First/Disabled 三态，审批记录写入 transcript 但对 agent 不可见。
16. 密钥只允许从文件或 stdin 传入，命令行参数永远不收；已存的 secret header 不再回显。
17. 秘密脱敏放在最底层，落盘前完成，并明确它是"尽力而为"。
18. 模型路由用"连接 + 映射模式 + 优先级"三件套，请求带 `x-amp-thread-id` 方便你自己的网关归因。
19. Dial 式模式选择（难度档位）比让用户选模型更省心；官方数据：69% 用户只用 medium。
20. Changes 面板：智能排序、commit 选择器、Restack、Ship 是单用户评审闭环的核心。
21. 用量分析做成"问 Puck"加 CLI（`usage --details`）两种入口，数据按线程、模型、orb 时间切分。
22. 流式 JSON 作为一等集成面，输入支持 `steer`，输出含后台子 agent 状态。
23. 活动列表过滤器写进 URL，可书签化；"72 小时不活跃"自动折叠。
24. 持久化执行不等于 exactly-once：事件投递用 at-least-once 加幂等键，工具调用丢连接不自动重试，这个契约要对插件作者说清楚。
25. Mac/本机 runner 睡眠时要有"约 90 秒消失、线程暂停、醒来自动续跑"的明确状态，不要静默失败。

---

## 9. 实际读过与未读清单

**本轮实际读过（均得到正文）**
- 索引：`/chronicle`（含 news 全表）、`/llms.txt`。`/news` 页被重定向到同一份 chronicle 内容。
- 公告：drop-the-neo、neo、service-accounts、gitlab、assign-it-to-amp、github-listeners、slack-integration、use-your-claude-plan、a-dial-for-you、build-your-own-dial、mcp-in-orbs、explain-usage、read-bigger-threads、subscriptions、custom-agents、agents-in-orbs、restack-your-changes、no-mailmap-required、setup-without-a-commit、multi-repo-projects、who-cares-about-the-model、the-coding-agent-is-dead、slashing-custom-commands、tab-tab-dead、stick-a-fork-in-it、ask-to-handoff、todos-are-done、lazy-load-mcp-with-skills、find-threads、painter、look-at、clickable-diagrams、liberating-code-review、handoff、enterprise-managed-settings、social-coding、command-palette、review、cli-tab-navigation、agentic-code-review、deep-mode、read-threads、look-ma-no-flicker、toolboxes、agents-everywhere、proof-of-human。
- Notes：200k-tokens-is-plenty、permissions；播客 S2E4 前 300 行。
- 页面：`/what-the-puck`（前 300 行，其余是图片列表）、`/security`（前 300 行，其余主要是 bug bounty）、`/modes`。
- 文档：customize/{skills, mcp 前 300 行, model-routing, decision-models, plugins 前 300 行, global-plugins-and-skills}、cli/{streaming-json 前 300 行, remote-control}、enterprise/{workspace-thread-visibility-controls, mcp-registry-allowlist, workspace-entitlements, allowed-models}、puck、puck/slack-integration、apps、apps/{ios, mac, android}、orbs/agent-to-agent、threads、prompting、collaborate/{workspaces 前 300 行, multiplayer}、sdk 前 300 行；plugin-api 只做了 grep。
- 搜索：用 web_search 核对了 thread actor 与持久化执行的出处。

**未读或未读完（需要时请补）**
- 公告：less-noise 的折叠/分组细节本轮未复读；plaid-mode、fable-5.1、opus-5.5、edu-discount、npm-package-changes、amp-frontier-corporation、model/定价类（gpt-5.x、opus-x、gemini-x、rush-2.0、amp-free*、try-opus 等）；event-driven-orbs、secrets-of-the-orb、more-orb-sizes、size-the-orbs-of-production、macos-satellites、personal-self-hosted-orbs、self-hosted-orbs（均为执行环境主题）；agent-skills、python-sdk、typescript-sdk、streaming-json（2025 公告）、mcp-permissions、tool-level-permissions、cli-workspace-settings、workspace-entitlements（公告）、thread-forking、queued-messages 等 2025 年早期文章。
- 文档：plugins 页第 300 行之后（约 1243 行，含 Ship、Label the Thread、Thread-Scoped AI 等）、plugin-api 全文（仅 grep）、mcp 页第 300 行之后（Questions From Remote Servers 等）、streaming-json 第 300 行之后、sdk 第 300 行之后、collaborate/workspaces 第 300 行之后、threads 页第 300 行之后只读了尾部 14 行、apps/windows、apps/linux、customize/agents-md、integrations/{github, gitlab, google}、cli/{clone, runners}、orbs/automations、orbs/files-and-attachments、designing-ui、egress-ips、model-routing/* 子页、enterprise/minimal-data-retention。
- 页面：设置页 `/settings/notifications` 需要登录，抓到的是登录页，**通知设置项的具体选项未读到**。`/docs/using-amp/a-day-in-amp` 返回 404。
- 官方没写、搜索也没找到的：多设备通知的路由或去重规则；持久化执行的具体实现；`/news` 与 `/chronicle` 以外是否还有未收录的公告。

### Critical Files for Implementation
- `docs/research/amp/client-and-threads.md` — 建议用来存放本报告（只读阶段不创建，仅供上级决定位置）。
- `https://ampcode.com/docs/markdown/threads` — 线程现行规则的最权威来源：侧边栏分组、Snooze、分享、Changes、导出。
- `https://ampcode.com/docs/markdown/customize/plugins` — 插件 API 全貌，需补读 300 行之后的 Ship、标签、AI 部分。
- `https://ampcode.com/docs/plugin-api` — thread actor、executor、`serverOnly`、事件投递语义的唯一技术出处。
- `https://ampcode.com/news/neo` — 新架构设计理由与删功能清单的主文档。
# 补充报告：Amp 的公开分享与公开访问（单用户视角，截至 2026-11-12）

状态标注沿用之前的约定。团队功能一两行带过，标 `[团队功能·暂不做]`。
证据等级：**[已读]** 是我亲自读到正文；**[推测]** 是推断；**[未找到]** 是官方没写。

---

## 0. 结论速览

| 公开入口 | 状态 | 核心规则 | 来源 |
|---|---|---|---|
| 线程 Unlisted | `[现行]` | "互联网上拿到链接的任何人"都能打开。公开可发现线程已于 2026-06-02 取消，旧的全部转成 Unlisted | `/docs/markdown/threads`、`/news/end-of-public-threads` |
| 线程公共主页列表 | `[已移除]` | 公开主页仍显示活动，但**不再显示线程** | `/news/end-of-public-threads` |
| Portal 公开 | `[现行]` | 按端口开启，限时 1h/3h/1d/7d，可续期、可提前终止 | `/docs/markdown/orbs/portals` |
| Runner 上的 Portal | `[现行]` | **不能公开** | `/docs/markdown/cli/runners` |
| 友好主机名 / 自定义域名 | `[现行]` | `<name>--<namespace>.onamp.dev`，或自有域名 | `/news/friendly-urls-for-sharing-orbs`、portals 文档 |
| 已发布文件（公开 URL） | `[现行]` | 附件列表带 **Public** / **Temporary** 徽章 | `/docs/markdown/orbs/files-and-attachments` |
| Artifact "Anyone with the Link" | `[现行]` | 无需登录；可导出 Markdown/HTML/PDF | `/docs/markdown/threads#artifacts` |
| Amp 托管仓库的公开只读访问 | `[现行]` | 任何人可 clone 并读文件，线程仍私有 | `/docs/markdown/projects` |
| Amp Store | `[现行，但仅见展示页]` | 展示"公开项目"；提交流程 `[未找到]` | `/store` |
| Box 公开应用 | `[现行]` | 应用 URL **默认公开**；`--private` 才限制 | `/docs/markdown/boxes` |

---

## 1. 线程公开分享（Unlisted）

### 1.1 可见性模型 `[现行]`（`/docs/markdown/threads#sharing`，2026-11-10）

分享入口：标题栏 **Share**，或命令面板 **Share Thread...**。CLI：`Ctrl+O` → `set visibility`，或 `amp threads share T-… --visibility unlisted`。

**General Access** 有三档，加上 People with Access 列表：

| 可见性 | 谁能打开 |
|---|---|
| **Private** | 只有你；工作区管理员可经**审计访问** |
| **Workspace** | 工作区全体成员 |
| **Group** | 指定组成员（仅 Enterprise） |
| **Unlisted** | 互联网上任何拿到链接的人 |

- `[团队功能·暂不做]` Workspace / Group 两档，以及管理员的审计访问：重新验证身份、页面显示审计提示、视图被记入审计日志；经此打开的 artifact 只读。
- 没有工作区时，新线程默认私有，Share 对话框会提示 **Create Workspace**。
- 个人项目或无项目的线程，默认私有。

### 1.2 单用户要问的几个问题

| 问题 | 官方说法 | 状态 |
|---|---|---|
| 未登录访客看到什么 | 文档只说"anyone on the internet who has the link"，没有描述访客页的具体布局 | **[未找到]**；唯一线索是 Security 页说线程存储包含"用户消息、LLM 回复、文件片段、工具调用结果和附件"，所以**可推测**访客能看到这些 `[推测]` |
| 是否只读 | **Multiplayer 在"拿到链接的人可看"时不可用，且"设为链接可见会关闭 multiplayer"** [已读]。所以访客不能向 agent 发消息 | 只读是**由此推出** `[推测]`，没有一句明说 |
| 是否含工具调用与 diff | 未明说 | **[未找到]** |
| 是否实时更新 | 未明说 | **[未找到]** |
| `.md` 导出 | 线程 URL 加 `.md` 即得 Markdown；**Export** 菜单提供 Markdown，**线程创建者**可导出完整 JSON；CLI：`amp threads markdown T-…`、`amp threads export T-…` | `[现行]`；匿名访客能否访问 `.md` **[未找到]** |
| 撤销分享后的行为 | 官方没有专门段落描述。可确认的是：把可见性改回非 Unlisted 就不再满足"拿到链接的人"这一条件，因此链接对匿名者应失效 `[推测]`；而对**Artifact** 与**Portal** 有明确的失效规则（见下） | **[未找到]** |
| URL 形态 | `https://ampcode.com/threads/T-<uuid>`，ID 形如 `T-019bb777-afe9-74ba-aacd-1dc6ec9017ad`（UUIDv7 风格）。官方称之为 "unguessable URL" | `[现行]` |
| 是否可被搜索发现 | **否**。"Unlisted threads outside your account and workspaces remain undiscoverable"；`visibility:unlisted` 过滤器只在你**已能找到**的线程内缩小范围 | `[现行]` `/docs/markdown/prompting` |

### 1.3 为什么取消"公开可发现"线程 `[已移除]`（2026-06-02）

官方原话要点（`/news/end-of-public-threads`）：
- 理由：**越来越难逐条审查线程里有没有敏感文件的片段**。模型每次升级都会读进更多文件，公开可发现线程"太冒险了"。
- 强调这是**主动决定，不是因为发生了事故**。
- 迁移：所有 `Public (Discoverable)` 线程被改成 `Unlisted`，博客里的线程链接不会失效。
- 公开主页（如 `ampcode.com/@sqs`）仍展示活动，但**不展示任何线程**。
- 历史价值：Mitchell 的 Ghostty 线程等曾帮助传播"coding agent 真的有用"。

**对 Oryn**：只做"不可猜链接"这一档，不做目录、列表或搜索收录，也不做公开主页里的线程列表。

### 1.4 公开链接的内容风险（官方给出的缓解手段，`/security`）

- 线程数据含用户消息、LLM 回复、被选作上下文的文件片段、工具调用结果、附件。
- 密钥会在落盘前被替换成 `[REDACTED:amp]`，但**尽力而为**。
- 若某密钥漏网：编辑它之前的那条消息并重发，会覆盖 ampcode.com 上的线程内容；标私有"不能从 ampcode.com 移除它"；还应轮换密钥。
- 这里值得借鉴的是：**"改写历史即覆盖云端副本"**，而不是只靠删除。

---

## 2. Portal 公开访问

### 2.1 定义与默认访问 `[现行]`（`/docs/markdown/orbs/portals`，2026-11-10）

- Portal 是**已认证的 URL**，把 orb 里的 web 服务暴露出来，在线程的 **Portal** 标签页里打开。
- **默认**：只有能查看该线程的人才能访问。
- Portal 访问与应用自己的认证**是分开的**。

### 2.2 公开操作

路径：Portal 工具栏 **⋯ → Portal Options → Access → Make Public For**。

| 项 | 规则 |
|---|---|
| 时长选项 | **1 小时 / 3 小时 / 1 天 / 7 天** |
| 访客 | 链接有效期内**无需登录 Amp** |
| 作用范围 | **仅该端口**。公开链接**不授予**线程或其评审工具的访问权 |
| 公开期间 | **Access** 面板显示结束时间 |
| 续期 | 在 **Keep Public For** 重新选时长 |
| 提前终止 | **End Public Access** |
| 不可用的场景 | 自托管 orb 且 provider 关了 **Public Portals**；或工作区关了 **Allow Users to Share Threads and Portals Outside the Workspace** `[团队功能·暂不做]`（开关本身可忽略）；tooltip 会说明原因 |
| agent 能否操作 | 能。`manage_amp` 工具、topic `portals`、operation `set-public-access`，并且**弹窗让你批准**后才会变公开或延长公开 |
| CLI | **不能**改访问权；`amp orb portal list` 只显示哪些 portal 是公开的、到何时 |
| 唤醒 | 打开 portal 会唤醒已暂停的 orb；空闲后自动暂停并停止计费；归档线程也会暂停 orb |

### 2.3 自托管的额外限制 `[现行]`（`/docs/markdown/orbs/self-hosted#public-portals`）

- 默认**关闭**，理由原文：公开 portal 是"进入你网络的匿名隧道"，访客在和 orb 里的服务对话，而 orb 能到达你网络允许它到达的一切。
- 关闭 **Public Portals** 会**立刻结束该 provider 上所有 portal 的公开访问**。
- Runner 上的 portal 一律**不能**公开 `[现行]`（`/docs/markdown/cli/runners`）。

### 2.4 应用侧可识别身份：`X-Amp-Authenticated` 头

Amp 的外部 portal 代理会加这个请求头，值是逗号分隔的三个标记：
- `amp-user=yes|no`：访客是否注册用户。
- `workspace-member=yes|no`：是否属于线程所属工作区。
- `collaborator=yes|no`：线程创建者为 `yes`；multiplayer 线程里当前可贡献者也为 `yes`。

访客取值示例：
- 未登录访客：`amp-user=no, workspace-member=no, collaborator=no`
- 个人线程的创建者：`amp-user=yes, workspace-member=no, collaborator=yes`

另有 `X-Amp-User-ID`、`X-Amp-User-Display-Name`、`X-Amp-User-Email`、`X-Amp-User-URL`，**只**在访客是工作区成员或协作者时才发送；非 ASCII 值会做 UTF-8 百分号编码。

> 官方建议：做访问控制时只检查字符串是否包含 `workspace-member=yes`。

### 2.5 主机名、自定义域名与 URL 形态

| 形态 | 示例 | 说明 |
|---|---|---|
| 默认 | `t-<thread>-p<port>.onamp.dev` | 公告中举例 `t-01a0095e-9568-whatever-p1234.onamp.dev`；`p<number>` 标识 Amp 的 Portal 代理，**不一定等于应用内部端口** |
| 友好主机名 | `foo--yourname.onamp.dev` | 形如 `<name>--<namespace>.onamp.dev`；Portal 标签页里设置，或让 Amp 代办（`/news/friendly-urls-for-sharing-orbs`，2026-08-23） |
| 自定义域名 | `park.mixfox.org` | 在个人 **Domains** 设置页添加域名，按提示建 DNS 记录，等状态变 **Active** |
| CLI | `amp orb portal <port> --hostname <hostname>` | 主机名**只对这个端口生效** |
| agent 在 orb 外 | `manage_amp` topic `portals` op `set-hostname` | |
| 复用主机名 | `amp orb portal <port> --hostname <h> --take-over` | 线程归档后主机名**仍保留**，要移到别的线程就用 `--take-over` |
| 迁到 Box | `amp boxes deploy --domain <h> --take-over` | **Portal 不能从 Box 应用手里把主机名拿回来** |
| 工作区默认域 | `t-<thread>-p<port>.portal.example.com` | **只有工作区**能设为 portal 默认域，个人域名只对你用 `--hostname` 指定的主机名生效 `[团队功能·暂不做]` |

### 2.6 稳定性与坑（官方原文）

- WebSocket 会断：每条 WebSocket 经过一台 Amp 服务器，而 Amp 部署时会替换服务器，"常常一小时数次"。连接会**不带 close frame 地关闭**，客户端报 **1006**，必须重连。
- 所以**不要用 portal 做必须长连的东西**（如数据库隧道）；私有连接用 Tailscale。
- 两个 portal 主机之间的浏览器请求**不被支持**，因为 CORS 预检请求不带 portal 认证。前后端分离时，应通过前端 dev server 的代理转发 API。
- 在所属 orb 内部访问线程的 portal URL 会**直接走本地服务**，不经 portal 认证，也**不能**用来测试外部访客登录流程。

---

## 3. 文件 / 附件发布为公开 URL

`[现行]`（`/docs/markdown/orbs/files-and-attachments`，2026-10-30）

- 线程附件**默认私有**：受 Amp 的访问检查保护，且**不会自动复制进仓库**。
- 发布：对 agent 说"Publish the finished demo video so I can share it outside this thread"。
- 官方警告原文：**只在"任何拿到 URL 的人都可以访问"是安全的前提下才发布文件**。
- 附件列表（线程菜单 **Attachments**）：**Public** 徽章表示 Amp 发布过的文件；**Temporary** 徽章表示几小时后过期。
- 附件是线程里的**副本**，与 orb 的 Files 面板相互独立，删除附件不影响 orb。
- 上传上限：普通 100 MB / 文件，高额度用户 512 MB；经 Files 面板或终端上传的可到 512 MB。
- 使用场景佐证：GitHub 不允许 `gh --attach` 把媒体放进 issue/PR/评论（需要写权限），所以 Amp 会**发布成公开 URL**再把 URL 嵌进 Markdown（`/docs/markdown/integrations/github`）。项目文档也建议让 AGENTS.md 要求"在 PR 描述里嵌截图或视频的公开 artifact URL"。
- 这些公开 URL 的确切形态：从 `/store` 页能看到资源路径形如 `https://ampcode.com/user-content/artifacts/<64位十六进制哈希>-file.png`，即**内容寻址的不可猜路径** `[推测]`：哈希大概基于内容，官方没有明说算法。

---

## 4. Artifact 的 "Anyone with the Link"

`[现行]`（`/docs/markdown/threads#artifacts`）

- Share → **General Access** 三档：**Same Access as Thread**（默认，随线程可见性变化而变化）、**Workspace Members**（只读，即使不能打开线程）`[团队功能·暂不做]`、**Anyone with the Link**（任何人，无需登录）。
- 可编辑者：能打开线程且属于其工作区的人，或个人线程的所有者。
- 撤销：工作区管理员关闭 **Allow Users to Share Threads and Portals Outside the Workspace** 或 **Allow Public Artifact Uploads` 后，已有公开链接**立即失效** `[团队功能·暂不做]`（开关本身可忽略）；对个人，改回 Same Access as Thread 即可（推论 `[推测]`）。
- 导出：**Export** 标签页可下载 Markdown 或 HTML，或打印为 PDF。
- 限制：每个 artifact 最大 **2 MB**；图片存在线程里，**通过工作区链接或公开链接访问的读者会看到破图**（因为图片只对能打开线程的人可见）。
- 删除 artifact 保留其图片直到线程删除；删线程会删它的 artifacts；只有 artifact 自己所在的线程能删它。
- 评论：复制到另一个线程的 artifact 是新文件，**不带原评论**。

**对 Oryn**：artifact 的图片权限与正文权限分开是一个已知坑，公开分享时应把图片内联打包，或为图片单独生成不可猜 URL。

---

## 5. 公开只读代码访问、Amp Store、公开项目

### 5.1 Amp 托管仓库 `[现行]`（`/docs/markdown/projects`）

- 创建 Amp 托管项目：**Start From Scratch**，或 `amp projects create --amp-hosted --name widgets`。
- 把本地仓库推上去：`cd myproject && amp projects create --push`。
  - 创建项目，把克隆地址加为 `origin`（可用 `--remote <name>` 换名），并推送当前分支。
- 克隆：`amp clone owner/project-name`。
- **Public Read-Only Code Access**：项目设置 Overview 里仅 **Amp 托管仓库**可见的选项。开启后"互联网上任何人都可以 clone 并读文件，**线程保持私有**"。
- **Read-only Git Credentials**（给 CI、构建机、GPU 机用）：
  - 可选过期：7、30、90、180 天或 Never；凭证**只显示一次**。
  - 用法：`git clone https://amp:<credential>@ampcode.com/@owner/project-name`，用户名被忽略。
  - 只读该项目，**不能 push**；在同一区域删除即撤销；个人项目转移到工作区时凭证会被删除。
  - 注意：凭证会写进 `.git/config`。
  - 只能拉取已推送的提交；未推送的 orb 内容用 `amp sync --apply`。
- 公开仓库 URL 形态：`ampcode.com/@owner/project-name`。
- 有 Post-receive Webhook：每次成功推送后向你指定的 URL 发送 JSON POST。

### 5.2 Amp Store `[现行]`（`/store`，只读到展示页）

- meta 描述："Get public projects built by the Amp community from the Amp Store."
- 展示 4 个项目：`mixmath`、`mixpark`、`mix-spellray`、`orbin-slide-show`，全部署名 "By amp"（官方账号）；每个有描述、3 至 6 张图或 webp 动图、项目页链接 `/@amp/<name>` 和 **Get** 按钮。
- **Get** 对未登录者跳转到 `auth/sign-in?returnTo=/store?focus=<project-uuid>`，说明"获取"需要登录，之后大概率是把项目复制到你的账户 `[推测]`。
- 项目文案 "Free, ad-free, and made to be remade"（`mixpark`）暗示 Store 的目的是**可被再创作的公开项目**，而不是成品分发 `[推测]`。
- 提交/发布流程、审核规则、举报入口：**[未找到]**。web_search 也确认"公开页能证明存在 storefront，但没有文档说明用户如何发布"。

### 5.3 无工作区时的"团队"含义

- 自托管 orb 在所有计划都能用，"独自工作可以免费创建工作区"（`/docs/markdown/orbs/self-hosted`）。这说明 Amp 的很多单人功能其实**挂在"工作区"这个壳上**，Oryn 若不做团队，就要把这些功能挂在"用户"上。

---

## 6. Box 公开应用与预览

### 6.1 公开 / 私有 `[现行]`（`/docs/markdown/boxes`，2026-11-11）

- Box 是"永远在线的电脑"，每个应用有自己的 URL，如 `https://bug-tracker--acme.onamp.dev`（`<app>--<namespace>.onamp.dev`）。
- **应用 URL 默认公开**，"拿到 URL 的人都能打开"。
- 限制为仅 Box 所有者（或所属工作区成员）：`amp boxes deploy --private`。访客需先在浏览器里登录 Amp，而**没有该登录的请求（例如 webhook）到不了私有应用**。
- 之后的部署保留该设置，并记在 `.amp/box-deploy.json` 的 target 上，所以新 checkout 重建应用时也保持私有；`--public` 改回公开。
- **Amp 不会告诉你的应用是谁在访问**。如果应用需要知道用户身份，或者它是公开的，应用得**自己处理登录**。
- `[团队功能·暂不做]` 工作区管理员可关 **Allow Public Box Apps**，关后所有应用变私有，重新打开后也保持私有，直到逐个 `--public` 重新部署。

### 6.2 自定义域名

- 个人 **Domains** 设置页加域名并建 DNS 记录，等 **Active**；然后 `amp boxes deploy --domain portal.example.com`（域名本身或其直接子域）。
- 此后应用的 `onamp.dev` URL **重定向**到新主机名，应用在 `AMP_DEPLOYMENT_URL` 里看到新主机名；`--no-domain` 恢复。
- 若应用用了 Sign in with Amp，要给 OAuth 客户端加新主机名的重定向 URL。
- 从 portal 迁到 Box 应用：先不带 `--domain` 部署并验证，再 `--take-over`。主机名在部署开始时就切换，所以拿着链接的人不会看到空应用。

### 6.3 应用预览（侧栏图标悬停）

- 应用可通过 `<meta name="amp-preview" content="v1; enabled; iframe">` 开启预览。
- Amp 用**不带 cookie 的服务器请求**检测该标签，所以必须在服务端发给所有访客的 HTML 里，不能靠脚本注入；跟随同源重定向；每几分钟重查一次。
- 加载的 URL 会追加 `amp-preview=light|dark`，在 **384 × 240 px** 的 iframe 里显示。
- 不可交互（点击整张卡才打开应用）；沙箱：不能开弹窗、提交表单、导航 Amp、用摄像头/麦克风。
- 私有应用的预览用查看者的 Box 登录；应用**自己**的 cookie 不会发给 Amp 里的 iframe（除非是 `SameSite=None` 且 partitioned），所以自己做登录的应用在预览里会看到"未登录访客"。
- 每次悬停都会加载，所以请求要**快且无副作用**；预览加载时不注入反馈组件。

### 6.4 反馈组件（`--widget`）与公开站点

- `amp boxes deploy --widget` 自动给应用页面加反馈组件，无需 script 标签。
- **公开应用**上它默认**隐藏**，按 `Cmd+.`（Win/Linux 用 `Ctrl+.`）或手机上快速三连点才出现；**发送评论始终需要以 Box 所有者身份登录 Amp**。`--no-widget` 移除。
- 独立使用的反馈组件（`/docs/markdown/feedback-widget`）：
  - 脚本对**所有访客**加载，组件默认隐藏，"任何人按键都能显示按钮"，但**只有能在该项目里开线程的人才能发送**；其他人看到 **Access Required**。
  - 官方建议：不想让公开站点带这个脚本，就只加到 staging 站点。
  - 个人项目用 **widget key**；撤销办法是 **Turn Off → Enable → 替换 script 标签**。
  - 登录 12 小时后需重新 **Continue with Amp**。
  - 允许的来源可用 `https://*.example.com` 通配，但**拒绝** `*.com`、`*.co.uk`、`*.github.io` 这类公共后缀（用 Public Suffix List 判断），并**拒绝** `https://*.onamp.dev`；只能加一个精确来源，如你的 Box 应用的来源。

### 6.5 Box 的网络与滥用

- 每个 Box 每月 **100 GB** 出站流量，80 GB 时发邮件，到上限就停机到下月（UTC）。
- Box 没有公网 IP，不能 SSH；用线程、`amp boxes exec`、`amp boxes cp`。
- 同一 Box 上的多个应用**共享 CPU/内存/磁盘/网络**，每个应用有自己的 Linux 用户和 systemd 沙箱，但**不是应用之间的安全边界**。官方建议：只有你信任它们互相访问数据时才放同一 Box，不可信的应用单独一台。

---

## 7. 公开入口的安全措施汇总

| 措施 | 做法 | 来源 |
|---|---|---|
| 不可猜 URL | 线程 `T-<uuid>`；portal 主机名含线程 ID 和端口；上传物是内容寻址路径 | threads、portals、`/store` |
| 限时 | Portal 1h/3h/1d/7d；附件有 **Temporary** 徽章（几小时后过期）；只读 Git 凭证 7/30/90/180 天或 Never | portals、files、projects |
| 可撤销 | Portal **End Public Access**；只读凭证可删除；widget key 可重置；自托管关 **Public Portals** 一键全撤；`POST /api/revoke` 吊销泄露的 Amp token | portals、projects、`/security` |
| 范围最小化 | Portal 公开**只限单个端口**，不给线程与评审权限；artifact 公开不含图片；公开 Git 访问不含线程 | portals、threads、projects |
| 需用户确认 | agent 要把 portal 变公开或延长公开时**弹窗批准**；发布文件前提示要确认"安全可公开" | portals、files |
| 内容审查/滥用 | 滥用投诉发 `security@ampcode.com`（含 `onamp.dev` 上的托管应用）；违反 AUP 的 Box 可被**封锁**：所有 URL（含私有应用、自定义域名）显示"应用不可用"页，磁盘与数据保留，即使有运行计划也保持停止，所有者无法启动，Amp 发邮件说明理由，回复邮件后解封 | `/security`、boxes、`/terms/aup` |
| 内容策略 | AUP 禁止：滥用/欺诈、CSAM、违法、色情、暴力仇恨、自动化的法律/医疗等受保护事项判定；一人一个免费账号；免费额度只能通过 Amp 产品使用 | `/terms/aup`（2026-09-10 更新） |
| 防钓鱼 | 反馈组件拒绝 `*.onamp.dev` 通配来源、拒绝公共后缀通配 | feedback-widget |
| 域名信任 | 工作区可把自有域名设为 portal 默认域，"让你的人只需要信任你自己域名上的链接" | portals `[团队功能·暂不做]` |
| 开发安全 | 官方给了开发期"魔法登录"路由的安全范式（见下） | portals |
| 内网保护 | 自托管 orb 默认关闭公开 portal；把"匿名隧道进内网"当作风险写明 | self-hosted |
| 线程内容 | 公开可发现线程被取消；密钥脱敏；改写消息覆盖云端副本 | end-of-public-threads、`/security` |
| 访客识别 | portal 代理加 `X-Amp-Authenticated` 头，后端用 `workspace-member=yes` 做授权判断 | portals |

**开发期魔法登录的安全范式**（可直接抄给 Oryn 的 agent 提示词，`/docs/markdown/orbs/portals`）：
- 路由形如 `GET /__dev/log-me-in/<email>?returnTo=<path>`。
- 找不到用户就创建，设应用的**正常会话 cookie**（同名同选项），只重定向到同源**相对路径**，拒绝绝对 URL 与协议相对 URL（防开放重定向），`returnTo` 缺失或不安全就回 `/`。
- 同时检查框架的开发标志和 `NODE_ENV != production`，否则返回 **404**；生产构建里不包含该路由。
- 示例 URL 和测试账号要写进 `AGENTS.md`。
- `.amp/services.yaml` 里 `$AMP_USER_EMAIL` 是 portal 条目中**唯一**支持的占位符，在 `url` 里会做 URL 编码。

---

## 8. 与公开入口相关的服务端契约（可实现的最小面）

### 8.1 Portal 的服务声明（`.amp/services.yaml`）`[现行]`

```yaml
services:
  web:
    command: pnpm dev -- --host 0.0.0.0 --port "$PORT"
    cwd: app
    health: /healthz
    env: { API_MODE: development }
    review: true        # 是否注入评审组件，默认 true
    portal: { url: /, title: App, description: ... }
    portals: [ { url: /admin, title: Admin } ]
    platforms: [linux]  # 或 darwin
```

- 服务名：小写字母、数字、连字符，以字母或数字开头，最长 32 字符。
- Amp 管理的环境变量：`PORT`、`PUBLIC_URL`、`AMP_THREAD_ID`；不能在 `env` 里设置。
- 命令：`amp orb services ensure [--json]`、`amp orb service {list,status,logs,restart,stop,start}`、`amp orb portal <port> [--title --description --hostname]`、`amp orb portal list`。
- 服务引用：`FRONTEND_URL: ${services.web.publicURL}`，被引用服务必须有 portal，缺失或循环会被拒绝。
- 生成状态写在 `.amp/portals/*.json`，**应加入 gitignore**。
- 就绪判断：配了 `health` 就请求该路径，2xx/3xx 通过；否则端口能连即算就绪。**URL 已打印不代表应用响应了**。
- 不要用 `nohup`、`setsid`、`&` 或 tmux 保活服务，更新或恢复 orb 时它们会被杀。
- 评审组件：`review: false` 关闭；或响应头 `x-amp-review-widget: off` 关闭单个响应。
- 链接回 Amp：`<a target="_amp" href="https://ampcode.com/threads/T-…">`。

### 8.2 Box 的发布契约（`.amp/box.yaml`）

```yaml
build: pnpm install --frozen-lockfile && pnpm build
source: false
artifacts: [build, package.json]
services:
  web:
    command: node build
```

- 部署只带 `artifacts`（`source: false` 时），保留当前版本加前两个版本；新版本不监听端口则**自动回滚**并让部署失败。
- 应用环境变量：`PORT`、`AMP_DEPLOYMENT_URL`、`AMP_DEPLOYMENT_NAME`、`AMP_DEPLOYMENT_BOX`、`AMP_DEPLOYMENT_DATA_DIRECTORY`（持久数据目录）。
- 密钥：`amp secrets set STRIPE_KEY --box alice/workbench --deployment bug-tracker --secret --data-file ./key.txt`，改后需重新部署；**构建时看不到密钥**，只有运行中的应用看得到。
- 数据：`amp boxes cp alice/workbench:app.db ./backup/ --deployment bug-tracker`；数据随 Box 删除而删除，自己备份。
- 价格（可作为 Oryn 做统计面板的参照）：`b1.tiny` $18/30 天，`b1.small` $30，`b1.medium` $50，`b1.large` $100；运行计划按分钟计费，停止的 Box 不计费；创建需至少 $2 余额；余额耗尽 24 小时后停机。Oryn 不做计费，但这些维度可作为**统计指标**：运行分钟、每月出站流量、每月预计花费。

---

## 9. 本补充轮的"对 Oryn 的启示"

1. 线程公开分享只做 **Unlisted + 不可猜 ID**，不做目录或搜索收录；官方因敏感内容泄露风险取消了可发现线程。
2. 分享前提供"哪些内容会被看到"的预览，并提示脱敏是尽力而为；官方没有这个预览，这是可以超越之处。
3. 链接可见时自动关闭任何"他人可写"能力，只读为默认。
4. 分享状态变更要有**明确的撤销语义**：官方对线程撤销行为没写，Oryn 要自己规定"改回私有 = 立即对匿名访客返回 404"。
5. Portal 公开用**固定档位**（1h/3h/1d/7d），默认关闭，**按端口**开启，必须**弹窗确认**，到期自动失效，并显示剩余时间。
6. 公开 portal 不给线程与评审权限；这是一个很干净的**能力分离**。
7. 本机 runner 上的 portal 默认**不允许公开**；公开等同于把本机变成匿名隧道，官方在自托管上也默认关。
8. 提供"终止全部公开访问"的总开关，一键撤掉所有 portal 的公开状态。
9. 对公开服务注入 `X-Amp-Authenticated` 这类**可信身份头**，让应用能区分"所有者/已登录/匿名"，不必自建账号体系。
10. portal 的 WebSocket 会因服务端部署而断开（1006），客户端必须自动重连，并在文档里写明"不要用它做长连隧道"。
11. 主机名分三种：自动生成、友好名、自定义域名；自定义域名要有 **Active 状态检查**和 `--take-over` 语义。
12. 不要让两个 portal 之间依赖跨域浏览器请求；教 agent 用前端 dev server 代理。
13. 发布文件 = 生成**内容寻址的不可猜 URL**，在附件列表里用 Public / Temporary 徽章区分；发布前必须提示"任何拿到 URL 的人都能访问"。
14. 把发布能力暴露为 agent 工具，因为 GitHub 评论里嵌媒体正需要它。
15. Artifact 分享时**图片权限要和正文一致**，否则公开读者会看到破图（官方的坑）。
16. Artifact 公开链接要有大小上限（官方 2 MB）和多格式导出（Markdown / HTML / PDF）。
17. 公开只读仓库访问与线程可见性**完全独立**；只读 Git 凭证做成**一次性显示、可过期、可撤销、只读**。
18. Store 可作为"公开项目目录"，但发布流程官方没有文档；Oryn 若要做，应补上**审核与举报入口**。
19. Box 类"常驻应用"默认公开是官方选择；Oryn 若做，建议反过来**默认私有**，因为官方自己也要求公开应用自己做登录。
20. 应用预览用约定的 `meta` 标签 + 无 cookie 抓取 + 沙箱 iframe，是一套值得复用的**低风险预览协议**。
21. 公开站点上的反馈组件：脚本对所有人加载但默认隐藏，发送必须登录，且来源通配要拒绝公共后缀；这是一套成熟的防滥用清单。
22. 滥用处理要有**封锁但保留数据**的状态：停机、保留磁盘、所有 URL 显示统一"不可用"页、通知所有者、可申诉解封。
23. 用"每 Box 100 GB/月"这类硬配额防止公开应用被刷流量，并在 80% 时通知。
24. 开发期魔法登录路由要有 404 兜底和同源相对路径限制，写进 AGENTS.md，让 agent 和评审者都能一键登录。

---

## 10. 本补充轮读过 / 未读

**读过（取得正文）**
- `/docs/markdown/threads`（含 255–314 行）、`/docs/markdown/orbs/portals`（含 262–639 行）、`/docs/markdown/orbs/files-and-attachments`、`/docs/markdown/boxes`（含 268–579 行）、`/docs/markdown/projects`（前 313 行）、`/docs/markdown/orbs/self-hosted`（前 300 行 + grep）。
- 公告：`/news/end-of-public-threads`、`/news/friendly-urls-for-sharing-orbs`、`/news/artifacts`。
- 页面：`/store`、`/terms/aup`。
- grep：`/docs/markdown/cli/runners`、`/docs/markdown/orbs`、`/docs/markdown/tools`、`/docs/markdown/integrations/github`、`/docs/markdown/orbs/customizing`、`/docs/markdown/feedback-widget`、`/docs/markdown/orbs/shipping`。

**未读 / 未找到**
- 匿名访客的线程页具体内容（是否含工具调用与 diff、是否实时、`.md` 是否对匿名可用）：**官方文档没写**，需要用真实公开线程实测。
- 撤销线程分享后链接的具体行为：**未找到**。
- Amp Store 的提交与审核流程、"Get"之后发生什么：**未找到**。
- 公开文件 URL 的过期时长（只说"几小时"）、签名机制：**未找到**。
- `/docs/markdown/projects` 第 300 行之后（Permissions 小节）、`/docs/markdown/cli/runners` 的完整正文（只做了 grep）、`/docs/markdown/orbs/handling-secrets`、`/docs/markdown/orbs/automations`（Webhook 公开入口）、`/docs/markdown/feedback-widget` 全文（只做了 grep）、`/docs/markdown/orbs/self-hosted` 300 行之后。
- `/news/portals`、`/news/runner-portals`、`/news/close-the-loop`、`/news/event-driven-orbs`、`/news/boxes` 本轮没复读（上一轮的已读清单里有部分）。

### Critical Files for Implementation
- `https://ampcode.com/docs/markdown/orbs/portals` — 公开访问时长档位、`X-Amp-Authenticated` 头、`.amp/services.yaml` 与 WebSocket 坑的唯一权威出处。
- `https://ampcode.com/docs/markdown/threads` — Unlisted 可见性表、Artifact 公开分享、`.md` 导出与 Snooze/归档规则。
- `https://ampcode.com/docs/markdown/boxes` — 公开/私有应用、自定义域名、预览协议、滥用封锁与配额。
- `https://ampcode.com/docs/markdown/projects` — 公开只读代码访问与只读 Git 凭证。
- `https://ampcode.com/news/end-of-public-threads` — 取消公开可发现线程的设计理由，决定 Oryn 只做 Unlisted。
