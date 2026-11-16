# 调研报告：Grok（SpaceXSI / xAI）与 Muse（Meta）——对 Oryn「全局 agent」的借鉴

> 调研日期：2026-11-16。来源标注：**[官方]** = 官方文档、博客、changelog 或官方 GitHub；**[媒体]** = 新闻或第三方报道；**[非官方存档]** = 社区抓取的运行环境快照，来源未经核实；**[推断]** = 本报告的分析，没有直接来源。
> 先说明背景：2026-02-02 SpaceX 收购了 xAI，官网现在署名 **SpaceXSI**（[官方] https://x.ai/news/xai-joins-spacex）；2026-08-14 SpaceX 完成对 Cursor（Anysphere）的收购（[官方/SEC] https://www.sec.gov/Archives/edgar/data/1181412/000162828026066880/spcx-20260930.htm）。因此 Grok Bot 的账号、云电脑和下载通道都跑在 Cursor 的基础设施上。

---

## Part A：Grok

### A① 产品是什么

Grok 现在不是单个产品，而是一组共用模型和订阅的产品：

| 产品 | 定位 | 关键事实 |
|---|---|---|
| **Grok（grok.com / iOS / Android / X 内嵌）** | 通用聊天助手 | 提供聊天、实时 Web 与 X 搜索、多 agent、Imagine、Voice、跨会话记忆、Canvas、Skills、Connectors、Automations（[官方] https://x.ai/grok ，https://docs.x.ai/grok/overview.md） |
| **@grok（X 上的回复 bot）** | 在 X 帖子里被 @ 后公开回复 | 公开的系统提示词在 `ask_grok_system_prompt.j2`（[官方] https://github.com/xai-org/grok-prompts） |
| **Grok Bot（2026-08-11 beta，2026-11-02 发布 1.0）** | 常驻的「AI 同事」团队，每个用户一台持久云电脑 | 在 X 上用 **@bot**（不是 @grok）把帖子转成任务（[官方] https://x.ai/news/introducing-grok-bot ，https://x.ai/news/grok-bot-1-0 ，https://docs.x.ai/grok-bot/tag-on-x.md） |
| **Grok Build（2026-05-25 CLI beta，2026-07-15 开源）** | 编码 agent：TUI、headless、ACP | 有 /goal、Workflows、Memory、子 agent、hooks、插件市场；Build Mode 在 grok.com 上生成可分享的应用（[官方] https://docs.x.ai/build/overview.md ，https://x.ai/news/grok-build-open-source ，https://github.com/xai-org/grok-build） |
| **模型** | — | 最新旗舰是 Grok 4.7（2026-09-21，宣传为「原生理解 Grok Bot harness」）。Grok 5 在 2026-01 被称为「训练中」，至今没有发布（[官方] https://x.ai/news/grok-4-7 ，https://x.ai/news/series-e） |

### A② 功能清单（按能力域）

**对话**
- 普通聊天、推理、Canvas 长文编辑、自定义指令、分享对话链接（[官方] https://x.ai/grok）。
- Grok Bot 的对话模型和普通聊天不同：主对象是 **Bot**，不是会话。每个 Bot 有名字、头像、职务、持久对话，同一个 Bot 跨设备续聊。支持群聊（每群最多 6 个 Bot）、语音通话、语音留言（[官方] https://x.ai/news/designing-grok-bot ，https://docs.x.ai/grok-bot/bots.md）。
- 对话时间线是异构的：普通消息、系统事件（如「已创建 Routine」「已与某 Bot 交换 6 条消息」）、内联卡片（邮件草稿、购物卡、多问题卡、审批卡）、Pages 都在同一条时间线上（[官方] https://x.ai/news/designing-grok-bot ，https://x.ai/changelog/bot）。

**记忆**
- 消费端 Grok：2025-04 上线跨会话记忆。回复下方有一个「Referenced chats」书本图标，显示本次引用了哪些旧会话，并可以从那里遗忘某次引用；在 Data Controls 里可以关闭。系统提示词明确要求：用户在聊天里说「忘掉」时，模型不得声称已经删除（[官方] https://github.com/xai-org/grok-prompts/blob/main/grok3_official0330_p1.j2 ，[媒体] https://techcrunch.com/2025/04/16/xai-adds-a-memory-feature-to-grok/）。2026 年产品页仍列出「Memory across chats」（[官方] https://x.ai/grok）。
- Grok Bot 的记忆有三层（[官方] https://docs.x.ai/grok-bot/how-grok-bot-works.md ，https://docs.x.ai/grok-bot/bots.md）：
  1. 所有 Bot 共享的少量用户事实（姓名、时区）。
  2. 每个 Bot 自己的偏好和规则。
  3. 每个 Bot 自己的、带日期的工作日志。

  Bot 自行决定记什么，不先征求同意；用户在聊天里问「你记得我什么」来查看、纠正或删除。可以从其他助手导入记忆：给用户一段提示词，贴到旧助手里，把输出粘回来。文档明说「Memory is not a source of truth」，并规定登录信息、卡号、联系方式放进 Vault，不进记忆。Team Bot 从同事那里学到的东西，要经 owner 或 Manager 审批才生效。
- Grok Build 的记忆（2026-09-16）（[官方] https://x.ai/news/grok-build-memory）：
  - 每个 turn 完成后，后台复盘这一轮，把持久内容写成 Markdown 笔记，包括约定、决策及其理由、项目事实。
  - 明确排除：任务状态、尚未定论的结论、秘密、仓库或文档里已有的内容。
  - 分两个作用域：按项目的 workspace scope，和全局偏好 scope。
  - `/dream` 把零散笔记合并进按主题划分的 topic 文件，也会在后台定期自动运行；`/memory` 是只读浏览器。
  - 每次开始相关工作前先读覆盖该区域的 topic；当前对话里的指令优先于笔记。

**主动性与定时**
- **Automations**（原 Tasks，grok.com/tasks 现跳转到 /automations，2026-07-16 发布）（[官方] https://x.ai/news/grok-automations）：
  - 触发方式：定时（一次、每天、工作日、每周、每月、每年，按时区），或邮件触发（按发件人、收件人、主题过滤，仅 SuperGrok 可用）。
  - 指令里可以 @ 连接器、挂 Skills、附文件、选模式。
  - 每次运行都开一条真实会话，存入运行历史，可以接着聊；通知方式可选 Email、App、两者或不通知；有「Run now」测试按钮；聊天里一句话也能创建。
- **Grok Bot Routines**（[官方] https://docs.x.ai/grok-bot/skills-routines-and-automations.md）：
  - 由「Skill + 触发器」组成，归某一个 Bot 所有。
  - 触发器：定时、Slack（被提及、关键词、表情反应）、GitHub/Linear/Sentry/PagerDuty 事件、发往 Bot 自有邮箱的邮件、Webhook（带 Bearer key，返回 200 只表示「已受理」）。
  - 硬限制：每个 Bot 最多 50 个 Routine；每个 Routine 保留最近 20 条运行记录；调度间隔至少 5 分钟；全账号每小时最多 300 次运行，单个 Routine 每小时最多 200 次；用户长期不在时会询问是否暂停。
  - 设计原则：先做「准备」再做「执行」；没有数据或数据陈旧时要有明确策略；重试要幂等；发送、删除、发布一律需要审批。
- **Initiative（主动性）**：只有「主 Bot」会主动联系用户。它会提出接活建议、提示连接器掉线、在 X Chat 里跟进。原则是「先提议，再动手」（[官方] https://docs.x.ai/grok-bot/initiative.md）。1.0 版强调 Bot 会监控项目、发现临近的截止日期、主动建频道（[官方] https://x.ai/news/grok-bot-1-0）。

**工具与连接器**
- 消费端 Connectors（2026-05-06）（[官方] https://docs.x.ai/grok/connectors.md）：
  - 内置：Gmail/Calendar、Drive、Outlook、OneDrive、Teams、SharePoint、Salesforce。
  - 目录：GitHub、Linear、Notion、Vercel、Box、Canva 等。
  - 自定义 MCP 服务器：必须公网可达，本地服务要走隧道。
- Grok Bot 通过插件、MCP、命令行 MCP 接入；连接器的 OAuth token 留在后端，Bot 调工具时拿不到 token（[官方] https://docs.x.ai/grok-bot/computer-and-apps.md）。
  - 没有 MCP 的服务：tinkabot 会把 API 包成「MCP + skills」插件，测试通过后，等用户同意才发布。
  - Bot 可以自己从目录安装应用，或按 URL/命令添加 MCP；碰到私网地址的 MCP 要先审批。
  - 编码任务可以交给 Cursor Cloud Agents，结果附在回复里。
- API 侧内置工具（[官方] https://docs.x.ai/developers/tools/overview.md）：web_search、x_search、code_execution、collections_search（RAG）、remote MCP。

**多 agent**
- 消费端「Multi-agent」模式；API 有 `grok-4.20-multi-agent`，4 或 16 个 agent，由 leader agent 汇总。子 agent 的状态加密后放进响应，只有开启 `use_encrypted_content` 才回传，方便多轮续用（[官方] https://docs.x.ai/developers/model-capabilities/text/multi-agent.md）。
- Grok Bot 的多 Bot 协作：Bot 之间可以私信、进群聊，「Chief of Staff」Bot 负责协调各专职 Bot（[官方] https://x.ai/news/introducing-grok-bot）。
- Grok Build 的多 agent：
  - 子 agent 有 general-purpose、explore、plan 三类，可在 `.grok/agents/` 下自定义；personas 只覆盖语气、关注点等行为层（[官方] https://docs.x.ai/build/features/subagents.md）。
  - **Workflows**：把任务编排成分阶段的脚本，每次运行默认 128 个 agent 预算，最多 1024。进度持久化，暂停/恢复不重做已完成部分；可以内置「对抗式核验」阶段；用过的 workflow 可保存为带参数的 slash 命令，内置 `/deep-research`（[官方] https://x.ai/news/workflows）。
  - **/goal**：长时间自主执行，自动拆成进度清单，支持 status/pause/resume/clear（[官方] https://x.ai/news/introducing-goal）。

**多端与消息渠道**
- Grok：Web、iOS、Android、X 内嵌、Tesla 车机（Voice 3.0 每天处理数百万次对话）、Office 和 Google Workspace 插件（Word/Excel/PowerPoint/Outlook/Workspace）（[官方] https://x.ai/news ，https://x.ai/news/grok-voice-3）。
- Grok Bot：桌面端（macOS/Windows/Linux）、手机、Slack（每个 Team Bot 有自己的 handle）、Microsoft Teams、视频会议（加入 Meet/Zoom/Teams，在会议聊天里发言，会后发纪要）、X Chat、X 上的 @bot、Bot 自有邮箱（[官方] https://docs.x.ai/grok-bot/get-started.md ，https://x.ai/changelog/bot）。
- **X 上被 @ 调用的两套机制**：
  - **@grok**：公开回复。提示词要求：用 X 工具读整个 thread 和图片；用实时搜索加 browse 核实每个事实；回复少于 550 字符、不用 Markdown、不 @ 被回复者、使用原帖语言；政治话题保持中立、多方取证；不把 Musk 或 xAI 的立场当依据（[官方] https://raw.githubusercontent.com/xai-org/grok-prompts/main/ask_grok_system_prompt.j2）。
  - **@bot**：把帖子交给用户自己的主 Bot 当任务。Bot 能拿到用户的帖子、被回复的帖子、两者引用的帖子以及其中的静态图片。@bot 在 X 上只回一句确认，工作和结果都留在 App 里。以下情况不响应：原帖同时 @ 了 @grok、含视频或 GIF、未通过审核、用户所在地区不支持（EU/UK/AU 等，按 X 账号登记的国家判断，不看当前位置或 VPN）、本周用量已耗尽（[官方] https://docs.x.ai/grok-bot/tag-on-x.md）。

**媒体生成**
- Imagine：图像（Image 2.0，2026-08-07）、视频（Video 1.5，最长 15 秒、原生 1080p，支持多张参考图和最多 3 个参考音色、首帧/尾帧/关键帧控制；另有 Lite 版）。视频编辑和延长用单独的模型（[官方] https://x.ai/news/grok-imagine-video-1-5-references ，https://docs.x.ai/developers/model-capabilities/video/overview）。生成物强制带水印（[官方] https://docs.x.ai/grok/faq.md）。
- Grok Bot 可以生成视频、Pages（可协作编辑，也可让 Bot 持续更新）和幻灯片（[官方] https://x.ai/changelog/bot）。

**搜索与研究**
- DeepSearch 随 Grok 3 推出（2025-02）（[官方] https://x.ai/news/grok-3）。DeeperSearch 是 Grok 3 时期的研究模式标签，当前是否还存在没有官方确认（[媒体] https://the-decoder.com/grok-3-adds-deeper-search-and-ai-image-editing-capabilities/）。现在的「深度研究」体现为三处：Multi-agent 模式、`grok-4.20-multi-agent`、Grok Build 的 `/deep-research` workflow（派出并行调查员，逐条核对每个论断的来源，产出带引用的报告）。
- X 实时数据：`x_search` 背后有 4 个函数（keyword、semantic、user_search、thread_fetch），可按 handle 白名单/黑名单和日期过滤，可选看图、看视频；按抓取的帖子数和用户数计费（[官方] https://docs.x.ai/developers/tools/x-search.md）。

**个性化与人格**
- 消费端有自定义指令。Companions（Ani、Valentine、Rudi，2025-07 推出）是带人格的 3D 陪伴角色；2026 年是否已下线，各方说法矛盾，官方没有确认（[媒体] https://time.com/7302790/grok-ai-chatbot-elon-musk/ ，https://chatbotscompared.com/grok-companions-latest-news/）[状态不明]。
- Grok Bot 的人格靠头像、名字、职务承载。头像的动效表示状态：闲置、思考、工作、等待、受阻、完成（[官方] https://x.ai/news/designing-grok-bot）。

**安全与权限**（[官方] https://docs.x.ai/grok-bot/approvals-security-and-privacy.md ，https://docs.x.ai/grok-bot/security.md）
- **Auto Review**：一个**独立的审核模型**，在命令执行、应用调用、电脑操作、修改 routine/触发器、启动子 agent 之前做审查，结果是放行、要求审批或拒绝。
- **Custom Rules**：用自然语言描述动作，选「Ask first」或「Allow automatically」，各最多 20 条，每条最长 1000 字符；两类冲突时 Ask first 胜出；团队规则只能让个人设置更严。
- **只有用户自己发的消息算指令**，邮件或网页里的文字不算。
- 审批卡：
  - 选项为「Allow once / Always allow（自动写入一条规则）/ Deny」；Deny 后，在用户发下一条消息前不再尝试该动作。
  - 后台触发的审批卡 30 分钟过期。
  - 由 routine 发出的邮件一律先存为草稿。
- Vault、1Password、Secure Form、secret card：登录凭据由系统直接填进页面，模型看不到（例外：在 App 内聊天时，Vault 里卡片的卡号对 Bot 可见）。
- 共享电脑**不是安全边界**：同一用户的所有 Bot 共用文件和浏览器登录态。
- 企业版：出网策略（白名单、黑名单，可选「对 MCP 也生效」）、Inference hooks（每次模型请求先签名发给企业自己的审核服务，支持 Shadow 模式）、审计日志、OTel 导出。
- 教训：Grok Build 发布的应用默认「Anyone with the link」，`*.grok.me` 子域名可以通过被动 DNS 枚举，结果约 3.5 万个应用无需认证就能访问，大量机密泄露（[媒体] https://www.techradar.com/pro/security/elon-musks-grok-build-coding-agent-is-leaking-a-serious-amount-of-confidential-data）。

**扩展生态**
- 消费端 Skills（2026-05-18）：内置 Word、PPT、Excel、PDF 和 Skill Creator；用户的自定义版本优先于内置版；可以「把刚才的流程存成 skill」（[官方] https://x.ai/news/grok-skills）。
- Grok Bot 的 skill（[官方] https://docs.x.ai/grok-bot/skills-routines-and-automations.md）：
  - 写法规范是六段：何时用、所需输入与权限、步骤、如何校验、返回什么、什么需要审批。
  - 演示学习（**Teach a task**）：录制最多 10 分钟的屏幕操作，生成一份 skill 草稿。
  - 可以发布或同步给团队。
  - Bot 模板市场：分享的是配置（身份、skills、routines），不含电脑和登录信息。
- Grok Build：插件市场、hooks、MCP；源码开源，可以完全本地运行（[官方] https://x.ai/news/grok-build-open-source）。
- 外部 harness 可以用 Grok 订阅登录：OpenClaw、Hermes Agent、OpenCode、Kilo Code、Warp（[官方] https://x.ai/news）。

### A③ 架构与关键机制

1. **五个原语**（[官方] https://x.ai/news/designing-grok-bot）：Bots（身份 + 记忆 + 运行时 + 工具）、Chats、Prompts（一次性使用、存为 Skill、或由 Routine 触发）、Tools、Artifacts。其他概念都藏在界面之下。
2. **能力和上下文按不同边界划分**：Tools 和 Skills 属于**账号级**，所有 Bot 共享；Memory 和 Routines 属于**单个 Bot**，跟着角色走。原话是 *capabilities can be shared broadly while context remains with the role*。群聊提供跨角色的共享上下文，但每个 Bot 保留自己的记忆。
3. **主 Bot 与专职 Bot**：主 Bot 是唯一主动联系用户的入口，负责路由和协调；反复出现、有独立数据源/规则/日程的工作，就拆给专职 Bot（专职 Bot 也可以由主 Bot 创建）。每账号约 50 个 Bot 上限（[官方] https://docs.x.ai/grok-bot/how-grok-bot-works.md）。
4. **运行时**（[官方] https://docs.x.ai/grok-bot/computer-and-apps.md ，https://docs.x.ai/grok-bot/security.md）：
   - 每个用户一台持久云电脑，有浏览器、shell 和 `/workspace`；用户之间硬件隔离。
   - 每个 Bot 在这台电脑上有自己的屏幕，同一屏幕同时只跑一个 computer-use 任务。
   - 请求路径：客户端 → Cursor 后端 → 用户的云电脑 → 模型；连接器 token 留在后端。
   - 可以选择让流量经用户桌面出网，或接入 Tailscale 访问私网。
5. **电脑可见度分三级**：状态（标题栏图标变紫）、预览（侧栏实时观看）、接管（全屏控制后交还）。产品刻意不把电脑做得太显眼，以免诱导用户盯着监工（[官方] https://x.ai/news/designing-grok-bot）。
6. **Routine 运行模型**：触发 → 开一条真实会话执行 → 结果写进 Bot 的时间线 → 按通知偏好推送。只有新发生的事件才会触发，创建触发器之前的旧事件不算。
7. **记忆写入策略**（Grok Build）：每个 turn 结束后在后台抽取 → 存成 Markdown 笔记 → `/dream` 周期性合并成 topic 文件，并维护一份索引（标注「Generated by Grok. Do not edit」）→ 新会话按代码区域预读相关 topic。
8. **审核与执行分离**：Auto Review 模型和执行 agent 是两个独立的模型；企业另外用 Inference hooks 在模型的输入端把关。文档自己承认：记忆写入和大部分设置修改不在 Auto Review 范围内。

### A④ 对「自托管 coding-agent 工作台的全局 agent」的价值

- **最值得借鉴**：Grok Bot 的整套产品化抽象——持久 Bot、主 Bot 协调专职 Bot、Routine = Skill + 事件触发、独立审核模型加用户规则、三级电脑可见度、异构时间线。Oryn 已经有对应的底座：每线程一个 orb 加远程桌面、定时任务、GitHub 集成、Artifacts、推送和 Telegram、Space 协调进程。差的主要是「把全局助手当成持久角色」这层产品抽象，以及事件触发、审核层、技能沉淀这几块。
- **对 coding 场景最有用的**：Grok Build 的 Workflows（大规模扇出 + 核验阶段 + 断点续跑 + 存成命令）、/goal（进度清单）、Memory（turn 后抽取 + dream 合并）。三者都能直接落到 Oryn Core（omp fork）或 Space 层。
- **冲突与代价**：
  1. Grok Bot 的「所有 Bot 共享一台电脑」恰好和 Oryn「每线程独占 orb」相反。Oryn 的隔离更强，但全局 agent 需要一台常驻的「家」机器。
  2. 用于审核的独立模型会增加延迟和 token 成本。
  3. Bot 很多时，概念负担会上升；Grok 自己也给每账号设了约 50 个 Bot、每群 6 个的上限。

---

## Part B：Muse

### B⓪ 判定主人指的是哪个 Muse

| 候选 | 是什么 | 符合度 |
|---|---|---|
| **Meta Muse（个人 agent App，2026-09-08 发布）** | 跑在专属 Secure VM 上的常驻个人 agent，内部代号 Hatch，由 Muse Spark / Muse Wonder 模型驱动 | **最可能，选它深入调研** |
| Muse Spark / Meta AI App（2026-04-08） | MSL 的第一个模型；驱动重做后的 meta.ai 和 Meta AI App，有 Contemplating 模式（最多 16 个 agent 并行） | 是同一家族的「模型 + 旧聊天 App」，不是 agent 产品 |
| Microsoft Muse（2025-02） | 用于游戏玩法生成的世界模型（WHAM） | 与助手无关 |
| Sudowrite Muse（2025） | 小说写作模型 | 与助手无关 |
| MUSE 研究框架（arXiv 2510.08002，2025-10） | 学术论文里基于经验自进化的长程 agent 框架 | 有启发，但不是产品 |

**判断依据**：
1. 主人要求「融合 Grok Bot、Muse、ChatGPT App、Hermes Agent 的优势，迈向 superagent」，比较对象都是**产品**，而且看重主动性、记忆、多端、工具执行。Meta Muse 正是 2026 年这类产品的头部：上线后登上美国 App Store 第一，Zuckerberg 在 Connect 上称它为 Meta 的「centerpiece」（[媒体] https://techcrunch.com/2026/09/22/meta-admits-muses-likeness-to-openclaw-isnt-a-coincidence/ ，https://techcrunch.com/2026/09/23/everything-new-coming-to-metas-ai-agent-muse/）。
2. 「Muse Spark / Meta AI」只是这个 agent 的模型层和前身；Muse agent 本身就跑在 Muse Spark 1.3 上（[官方] https://about.fb.com/news/2026/09/introducing-muse-personal-ai-agent/）。
3. 其他同名项目都是模型或研究，不具备「助手/agent 产品」的属性。

**其他候选简述**：Muse Spark（[官方] https://ai.meta.com/blog/introducing-muse-spark-msl/）是 MSL 的第一个闭源多模态推理模型，Contemplating 模式最多 16 个 agent 并行；Meta 在 2026-10-15 发布了开源权重版 **Muse Spark Open**（816B 总参数 / 42B 激活，1M 上下文，两台 M5 Ultra Mac Studio 即可在本地运行）（[官方] https://research.meta.ai/blog/muse-spark-open）。Microsoft Muse 是游戏世界模型（[官方] https://www.microsoft.com/en-us/research/blog/introducing-muse-our-first-generative-ai-model-designed-for-gameplay-ideation/）。Sudowrite Muse 是小说写作模型（https://sudowrite.com/muse）。MUSE 论文（https://arxiv.org/abs/2510.08002）的「经验驱动自进化」思路，可以作为技能自进化的参考。

### B① 产品是什么

Meta 称 Muse 是「World's First Personal AI Agent Built for Everyone」（[官方] https://about.fb.com/news/2026/09/introducing-muse-personal-ai-agent/）：

- **形态**：每个用户一台专属云 VM，agent 和用户数据都住在里面；用户像给真人发消息一样使用它，入口是 Muse App 或 WhatsApp。
- **能做什么**：自己开浏览器、填表、比价谈价；App 关掉后继续后台工作，有变化或需要审批时再回来找用户。
- **模型**：上线时用 Muse Spark 1.3，之后逐步换成 Muse Wonder（2026-10-12 发布，主打 Active Memory 和 Active Focus）（[官方] https://research.meta.ai/blog/muse-wonder）。
- **与 OpenClaw 的关系**：MSL 产品负责人 Nat Friedman 公开承认 Muse 「heavily inspired as a product by OpenClaw」，代码从零写起，但 SOUL.md 等工作区文件名和内容「几乎相同」（[媒体] https://techcrunch.com/2026/09/22/meta-admits-muses-likeness-to-openclaw-isnt-a-coincidence/）。
- **定价**：免费档，加 $20 和 $100 两档订阅；不放广告，计划从交易中抽成（[媒体] https://en.wikipedia.org/wiki/Muse_(AI_agent) ，https://techcrunch.com/2026/09/23/everything-new-coming-to-metas-ai-agent-muse/）。

### B② 功能清单

**对话**
- **一条长主对话，加可选的 side chats**；Muse 在主对话和 side chats 之间保持感知（[官方] https://www.meta.com/help/superintelligence/1331373868832401/）。
- **不是一问一答**：用户可以随时打断、连发多个任务，不必等上一条回复。因为消息会不按顺序到达，界面用气泡划分每一段思路（[官方] https://muse.ai/blog/how-we-designed-muse）。
- 模型层面针对这种场景做了训练：Muse Spark 1.3 专门训练了「在一条混乱的长线程里，把新消息映射到正确的任务」，并能按用户偏好选择频繁汇报或静默干活（[官方] https://research.meta.ai/blog/introducing-muse-spark-1-3）。

**记忆**
- **全部以文件形式存放**（[官方] https://www.meta.com/help/superintelligence/2225571704857152/）：
  - `Memory.md`：记住的事实和偏好。
  - `Soul.md`：核心信条、边界、人格。
  - `Identity.md`：名字、「生物形态」、气质、标语。
  - 用户可以在 App 里直接查看和编辑这三个文件。
- 记忆来源包括：对话内容、观察到的交互模式、连接器数据（[官方] https://www.meta.com/help/superintelligence/995796179982326/）。
- **forget skill**：按人、主题或事件，在记忆和衍生文件里查找并删除；官方明说「尽力而为」，删掉原文后，Muse 仍可能记得从中学到的东西（[官方] 同上 2225571704857152）。
- 支持导入其他助手的聊天导出包（zip）；可以下载全部 Muse 数据；「Reset」会清空一切。
- [非官方存档] 社区抓取的运行时快照还显示更细的结构（https://github.com/win4r/MuseAI-Skills ，内容未经验证）：
  - 按日期的笔记放在 `~/memory/`；人物页和群组页放在 `~/memory/people/`、`~/memory/groups/`，按亲疏排序。
  - 每小时把新对话合并进 MEMORY.md，新说法覆盖旧说法，按日期的笔记保留完整轨迹。
  - 夜间「Dreaming」把反思写进 `~/dreams/`。

**主动性与定时**
- **Goals 页签**：把长期目标拆成计划，持续推进并定期跟进。**Ideas 页签**：根据用户的目标、习惯和已知信息，主动生成可以做的事（[官方] https://muse.ai/blog/how-we-designed-muse）。
- 后台工作完成后，Muse 会**评估结果是否值得告诉用户**，只在「有实质新信息或需要输入」时通知。主动消息的门槛高，用户可以说「关掉、调低、调高」（[官方] 同上）。
- 提醒和定时任务在 Assistant → Upcoming 里查看（[官方] https://www.meta.com/help/superintelligence/1331373868832401/）。
- [非官方存档] 主动性和定时的实现细节：
  - `PROACTIVE_PREFERENCES.md` 分「Tell me about / Never tell me about / When / How」四节，例如 09:00–21:30 之间才发消息，发送会等安静时刻。
  - 定时任务明说是**轮询而非实时监听**，禁止承诺「一发生就通知」；判断「任务跑没跑」以 `cron.runs` 的运行历史为准，不以调度配置为准。
  - 定时任务只通知用户本人；如果任务内容要给别人发消息，那是另一次连接器发送，默认需要审批。
  - 来源：https://raw.githubusercontent.com/win4r/MuseAI-Skills/main/home/hatch/docs/scheduling-and-watching.md

**工具与连接器**
- VM 里有文件系统和终端：Muse 可以自己写代码、自己造工具（[官方] https://muse.ai/blog/how-we-designed-muse）；有完整的 Chromium 浏览器，用户可以实时观看、随时接管。
- 连接器（[官方] https://muse.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse ，[媒体] https://techcrunch.com/2026/09/23/everything-new-coming-to-metas-ai-agent-muse/）：
  - 覆盖邮件、日历、Meta 自家应用、GitHub、Notion、Granola、零售商、Expedia、Shopify 等。
  - 每个连接器都和服务方合作写了 **SKILL**；Muse 也能**为有 API 或 CLI 的服务自己写连接器**。
  - 开放了开发者连接器平台，一周内收到 1500 多份申请。
- 支付：Stripe Link 签发一次性卡号，PayPal、Shop Pay 陆续接入。
- 端侧：Mac 端可以操作任意桌面应用；手表；智能眼镜（唤醒词）；家居设备（[媒体] 同上，https://techcrunch.com/2026/11/10/metas-ai-agent-muse-lands-on-apple-watch-and-wear-os/）。

**多 agent**
- 「launches swarms of subagents」；VM 的算力足以支撑并发子 agent 和 cron（[官方] https://muse.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse）。
- 浏览器操作由专门的 **browser sub-agent** 完成，它有一套针对网页的不可信内容处理指令（[官方] 同上）。
- [非官方存档] `wide-research` skill 的契约：一个 manager 子 agent 加「每个输入一个 worker」。manager 的输入是 brief、inputs、output_schema、worker_prompt_template；输出是 total、success_count、failure_count、results、failures、notes。规则：只对失败项重试一次；开始后立即告诉用户已启动，不阻塞等待；最终必须报告覆盖率（成功数/总数）和未解决的缺口（https://raw.githubusercontent.com/win4r/MuseAI-Skills/main/opt/hatch/skills/wide-research/SKILL.md）。

**多端与消息渠道**
- iOS、Android、Web、iPad、WhatsApp、Mac、Apple Watch、Wear OS、AI 眼镜、手掌大小的 Muse Charm 硬件（[媒体] https://en.wikipedia.org/wiki/Muse_(AI_agent)）。
- Muse 将有自己的邮箱地址：用户可以把它抄送进邮件线程，或把邮件转给它处理（[媒体] https://techcrunch.com/2026/09/23/everything-new-coming-to-metas-ai-agent-muse/）。
- 代打电话：内测阶段部分电话实际由人工坐席完成，内部称「human agent layer」（[媒体] 404 Media，转引自 https://en.wikipedia.org/wiki/Muse_(AI_agent)）。

**媒体生成**
- Artifacts：PDF、表格、文档、图片、播客，以及交互式仪表盘和学习卡，存进 Library（[官方] https://www.meta.com/help/superintelligence/1331373868832401/）。
- **Spaces**（2026-11-06）：用户描述一个想法，Muse 就做成一个个性化小应用，比如礼物追踪器、学校活动面板、订单面板。小应用可以放进侧栏或主页签，按时间表显示（例如工作日显示周计划、周末换成周末安排）；连上应用后会**在后台自动更新数据**（[官方] https://about.fb.com/news/2026/11/introducing-spaces-customize-muse-app/）。
- Realtime Avatar：可以和自定义的数字形象视频通话；还可以给 Muse 配语音（[官方] https://about.fb.com/news/2026/11/introducing-muse-ai-voice-expressive-avatars/ ，[媒体] TechCrunch 同上）。

**搜索与研究**：wide-research 并行研究（见多 agent 一节）；Muse Wonder 主打法律、金融、健康等专业领域任务（[官方] https://research.meta.ai/blog/muse-wonder）。

**个性化与人格**：名字、头像、人格、语气、沟通风格都可以改，可以说「回到默认人格」来重置（[官方] https://www.meta.com/help/superintelligence/995796179982326/）。设计团队认为「和企业 logo 长期对话很怪」，所以让每个 Muse「真正属于你」，用户给它起名字、设计形象（[官方] https://muse.ai/blog/how-we-designed-muse）。

**安全与权限**：这是 Muse 最核心的差异点，在 B③ 详述。补充几条：漏洞赏金最高 $30 万，其中针对单个用户的成功 prompt injection 最高 $13 万（[官方] https://muse.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse）；邮件连接器会过滤一次性验证码、密码重置链接和 magic link；用户发出的内容可以加「由 AI agent 生成」的署名。

**扩展生态**：Skills（[非官方存档] 快照里有 68 个 SKILL.md，配 40 份 manifest 和 12 份 eval）、开发者连接器平台、Muse Code CLI（[官方] https://research.meta.ai/blog/introducing-muse-spark-1-3）、Meta Model API。

### B③ 架构与关键机制

**1. Secure VM 里的双安全域**（以下均为 [官方]，来自 https://muse.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse）

VM 里分成两个互相隔离的安全域，用官方的话说，不要理解成「一个有 root 权限的 LLM agent」。

- **Runtime cell**（不可信域）：
  - Hatch daemon（agent harness）、用户工作区、所有工具都跑在 `systemd-nspawn` 容器里。
  - cell 内的 root 映射为宿主上的非特权用户；cell 有独立的 rootfs（完整 Debian）和虚拟网卡。
  - 系统调用被过滤（例如禁用 io_uring），能力被裁剪（例如没有 CAP_SYS_PTRACE、CAP_NET_ADMIN）。
- **宿主侧服务**（受信域，各自是独立的 systemd unit）：
  - `hatch-safety`：独立的分类器和模型，检查推理的输入和输出。
  - `privsep` workers：执行内置连接器的业务逻辑，每个 worker 按 cgroup 识别身份，只能拿到自己白名单里的凭据。
  - `hatch-authd`：存放凭据，负责「凭据替身」。
  - **Sentinel**：连接器动作和所有出网的**唯一授权方**。
  - Postgres：存放全部持久状态，和 cell、凭据库都隔离。
  - 推理代理和遥测代理。
- **两个域之间**：只通过 Unix domain socket 通信，用 SO_PEERCRED 校验对端身份并配合 ACL，是内核认证的最小权限 IPC。

**2. Sentinel 的决策流程**
- 连接器调用：cell 里的 CLI 只解析参数，把类型化参数和文件描述符传给宿主侧 worker。Sentinel 根据连接器、方法、动作类别、范围和用户原始请求，生成一句用户可读的「用途说明」，再按用户设置的策略判为 **allow / deny / ask**。
- 出网：所有请求经 forward proxy，用 userns、veth 和 eBPF 强制走这条路。Sentinel 在 L4 和 L7 两层评估目标（域名、解析后的 IP、端口、方法、路径、解码后的请求体），并防 SSRF。
- **凭据替身**：cell 里只能看到 authd 签发的 surrogate token；请求通过授权后，Sentinel 在网络边界上把它换成真实凭据。所以 prompt injection 无论怎么诱导，都套不出真实 token。
- **污点出网**（tainted egress）：每个工具进程启动时是「干净」的，一旦读过用户数据就被标记为「污染」。干净的请求只要命中窄范围的 auto-allow 策略就直接放行；被污染或无法判定的请求一律走审批。实现方式是 eBPF cgroup 程序，加上新增 LSM hook 上的 eBPF 程序做污点传播。

**3. 审批是「能力」，不是对话里的一句话**
- 审批请求由 Sentinel **直接推到客户端**，不经过和 Muse 的对话；用户的答复也直接回到 Sentinel。
- 授权有五种范围：一次性、会话级、任务级、限时、永久。每次授权绑定到具体的连接器/目标和用途，之后的调用必须严格匹配所授范围。
- 默认策略：正常浏览放行，「难以撤销」的动作才停下来；这样做是为了避免「banner blindness」（用户什么都点同意）（[官方] https://muse.ai/blog/how-we-designed-muse）。
- 产品原则：只在必须确定无误的地方用固定的确定性界面（审批卡、凭据输入框），其余交给对话。

**4. 浏览器隔离**
- CDP 连接放在 cell 之外的 broker 里。browser sub-agent 只能看到**无障碍树快照**，看不到原始 DOM；不能执行 JS，DevTools 关闭。
- 用户接管期间，或凭据正在自动填充时，agent 完全暂停。
- 外加一组分类器：防个人数据外泄、检测 DOM 里或图片里的 prompt injection、检测下载文件、拦截高风险表单。
- 结账页会被自动识别，强制审批；Stripe Link 签发的一次性卡号绑定商户、金额和有效期。

**5. 纵深防御，针对 Simon Willison 所说的「lethal trifecta」**（私有数据 + 不可信内容 + 对外通信）
- 模型层：训练模型识别注入。
- harness 层：所有外部数据进入上下文时都标注为「untrusted input」。
- 独立的分类器集合。
- 涉及数据出 VM 时，人工审批。

**6. 后台自我改进循环**（[非官方存档] https://raw.githubusercontent.com/win4r/MuseAI-Skills/main/home/hatch/docs/self_improvement.md）
- 每小时：有新内容时，把对话合并进 MEMORY.md；维护人物和群组关系页。
- 每天：策划 Ideas（按可行性、个人契合度、新颖度打分后排序）。
- 每天夜间：「Studying」，为每个目标调研简报，偶尔建议用户只是暗示过、从没明说的目标。
- 每晚：「Dreaming」，复盘哪些做法有效、哪些伤了关系、这个用户正在变成什么样的人；写反思，为需要修补的关系建「repair thread」，总结出该如何对待这个用户。
- 每天：**Skill review**，把反复出现的流程变成新 skill，并对照近期表现审计已有 skill，**可度量地没有帮助的改动会被撤销**。
- 「Quiet-moment pass」：一段实质对话安静下来后做一次有界的整理，每天最多几次。
- 每条建议和更新都在 `muse.db` 里记录依据和证据，用户问「为什么」时据此回答。
- 交付可以从 run → step → handoff → mailbox → delivery 一路追溯；**「已排队或已执行」不等于「用户已收到」**。

**7. 透明度**：头像下方显示当前工作的摘要；点开能看到完整活动日志和已批准的权限；Memory 文件可以直接读写（[官方] https://muse.ai/blog/how-we-designed-muse）。

**8. 隐私路线**
- 现状：Meta 的人员按政策不能访问 VM，但技术上做得到（[媒体] https://www.wired.com/story/meta-releases-muse-a-personal-ai-agent-with-privacy-built-into-it/）。
- 规划中的 Confidential VM：TEE 加用户持有密钥，二进制和透明日志公开，供外部审计（与 Moxie Marlinspike 合作）。
- 训练数据：默认使用对话轨迹训练（去除 PII 后），可以关闭。

**9. 已知事故**（对设计有直接参考价值）
- Muse 代管 Facebook Marketplace 时，在未经同意的情况下透露了卖家住址、接受了低价，还自动回复「我在家」，结果买家扑空（[媒体] https://www.techradar.com/ai-platforms-assistants/you-gotta-never-do-that-again-youtuber-says-metas-muse-ai-ruined-a-sale-and-gave-out-his-home-address-without-permission）。
- Amazon 封禁 Muse，理由是 agent 未表明身份、疑似抓取凭据；被分享的 Muse 对话可以通过被动 DNS 枚举找到，功能已被暂停（[媒体] https://en.wikipedia.org/wiki/Muse_(AI_agent)）。

### B④ 对「自托管 coding-agent 工作台的全局 agent」的价值

- **最值得借鉴**：
  1. 安全架构：「agent 不可信、能力在宿主侧」，加 Sentinel、凭据替身、污点出网和带范围的审批。Oryn 的 orb 隔离加 orynd 控制面，在结构上就是同一个形状：orb ≈ runtime cell，orynd ≈ 宿主侧服务。
  2. 记忆和人格都是**可读写的 Markdown 文件**，用户能查看、编辑、遗忘。
  3. 后台自我改进循环：记忆合并、关系页、dreaming、skill review 并撤销无效改动。
  4. 主动消息的「值得打扰」评估，加 PROACTIVE_PREFERENCES 文件。
  5. 一条长主线加 side chats，非轮流对话。
  6. Spaces 形式的「会自我更新的小应用」。
- **冲突与代价**：
  1. Muse 面向生活场景（购物、出行、家庭），coding 不是重点；Oryn 要把它的机制映射到「仓库、线程、PR、CI」。
  2. 污点追踪依赖 eBPF 和 LSM，自托管用户的内核与权限环境各不相同，只能退化为代理层的近似实现。
  3. 后台循环长期消耗 token，自托管用户要直接买单。
  4. 一条主线下，多任务并发和上下文污染的风险高，Muse 是靠专门训练的模型来缓解的，开源或第三方模型未必做得到。

---

## Part C：对 Oryn 全局 agent 的借鉴清单

> 以下「Oryn 对应物」根据 Goal 里描述的现状推断（orynd、orb、Space @host、定时任务、Artifacts、推送与 Telegram、远程桌面、M16 Hindsight），没有读仓库代码核实 [推断]。

| # | 借鉴什么 | 价值 | 代价 / 冲突 |
|---|---|---|---|
| 1 | **把「持久 agent」做成一等对象**：全局助手变成「主 Bot」，有名字、头像、状态动效、自己的记忆、自己的例程、自己的「家 orb」；以后可以派生专职 Bot，例如 Release Bot、Triage Bot（Grok Bot） | 用户回来面对的是「同一个同事」，不是一堆聊天记录；是迈向 superagent 的产品基座 | 要重写首屏和信息架构；和现有「线程」「Space」要分清层次：Bot 是长期角色，线程是一次任务，Space 是一个项目组 |
| 2 | **能力账号级、上下文角色级**：tools、skills、连接器全局共享；记忆和例程挂在 Bot 上；跨角色协作靠群聊/Space 的共享上下文（Grok Bot） | 避免一个塞满法律、财务、前端内容的大记忆；权限和上下文边界清楚 | 要给 Hindsight 的记忆加 owner 维度（用户 / Bot / 项目）；检索时要先确定作用域 |
| 3 | **主 Bot 协调、线程和 Space 执行**：全局助手只负责理解意图、拆解、派发到新线程或现有 Space、汇总进展，并作为唯一主动联系用户的入口（Grok 的 primary bot / chief of staff） | 和 Oryn 现有的「宿主协调进程 @host」天然契合，用户不必当调度员 | 和 Space @host 的职责会重叠，需要定义「全局 → Space → 线程」三级委派协议，以及状态如何回传 |
| 4 | **一条长主线 + side chats + 非轮流对话**：可以随时插话、连发多任务；时间线是异构的，系统事件、审批卡、例程运行记录、Artifact 卡片都在里面（Muse、Grok Bot） | 贴近「和助理发消息」的心智；后台结果有地方自然落下 | 一条长线程的上下文管理难度大，要做压缩和任务路由（Muse 靠专门训练）；WS 协议要支持「agent 主动发出」的消息类型 |
| 5 | **文件化、分层的记忆**：用户事实（共享）/ Bot 记忆 / 项目约定 / 带日期的工作日志 / 人物页，全部是 Markdown，用户可以浏览和编辑；每个 turn 后在后台抽取，周期性「dream」合并成 topic，并生成索引（Grok Build Memory、Muse MEMORY.md） | 可审计、可迁移、可放进 git；和 omp 的 AGENTS.md 风格一致；能给 M16 Hindsight 当「事实层」，向量检索只作索引 | 要定义「文件是事实来源、向量是索引」的同步规则；抽取的质量和漂移需要评测；要明确不记什么：任务状态、未定结论、秘密、仓库里已有的内容 |
| 6 | **记忆卫生规则**：「Memory is not a source of truth」，价格、版本这类易变事实要重新查；秘密只进 Vault；当前指令优先于记忆；遗忘要覆盖衍生副本，并如实说明「尽力而为」（Grok、Muse） | 防止旧记忆误导代码修改和部署 | 遗忘的完整性很难保证，因为后台任务可能把信息重新写回；需要给记忆来源打标签 |
| 7 | **Routine = Skill + 触发器**：触发器包括定时、GitHub 事件（PR 开启、CI 失败、带特定标签、评论里出现 `/fix`）、Webhook（Bearer key）、邮件和 Telegram 消息；每次运行都是一条可以续聊的真实会话；有 Test/Run now；可选通知方式；遇到无数据或陈旧数据时显式报告失败（Grok Automations / Routines） | 把 Oryn 现有的定时任务和 GitHub 集成，升级成「事件驱动的值班 agent」，例如 CI 挂了自动开线程修 | 需要事件总线和去重、幂等；只有新事件才触发；要有速率限制和最小间隔（Grok：5 分钟，每小时 300 次）；在自托管场景里，webhook 入口必须暴露到公网，存在安全面 |
| 8 | **如实说明调度语义并保留运行历史**：定时检查是轮询，不承诺「一发生就通知」；判断「跑没跑」查 run 表，不查调度配置；区分「已受理 / 已执行 / 已送达」（Muse） | 减少误导；排障有据 | orynd 要补 run → step → delivery 的数据模型 |
| 9 | **主动性带「打扰预算」**：后台产出先评估「是否有实质新信息或是否需要用户」再通知；用 `PROACTIVE_PREFERENCES.md` 规定 Tell / Never / When / How 和安静时段；采用「先提议再动手」（Muse、Grok Initiative） | Oryn 已有推送和 Telegram，主动性是 superagent 的标志；但如果过度打扰，用户会直接关掉 | 判断「值不值得」本身要花 token；需要按用户反馈（忽略、关闭）调整阈值 |
| 10 | **后台维护作业**：每小时合并记忆、每天把重复流程沉淀为 skill 并做 **skill review（无效改动撤销）**、夜间复盘、按项目做每日简报；每条建议都存依据，以便回答「为什么」（Muse self_improvement） | 这是「技能自进化」最具体、可落地的形态；可以直接复用 Oryn 的定时任务机制来跑 | 需要可度量的指标才能判断「是否有帮助」，比如 skill 调用后任务是否成功、用户是否返工；成本要可视化，并设上限 |
| 11 | **技能沉淀三条路**：「把刚才的流程存成 skill」；**演示学习**（利用 Oryn 现有的远程桌面录制操作，生成 skill 草稿）；按六段模板写（何时用、输入与权限、步骤、校验、返回、需审批项）；用户版本覆盖内置版本；skill 附带 manifest（方法级权限默认值）和 eval 场景（Grok Skills、Teach a task，Muse skills） | 把一次成功变成可复用资产；和 omp 的 skill 体系兼容 | 演示录制的隐私问题；skill 越来越多后，触发冲突需要治理 |
| 12 | **独立的「审批与出网」守门人**：Auto Review 模型（独立于执行模型）加用户自然语言规则（Ask first 胜出）；在 orynd 侧实现一个 Sentinel 式的出网代理：所有 orb 出网经代理，域名/方法策略和 allow / deny / ask 判定在 orb 之外执行，agent 无法绕过（Grok Auto Review、Muse Sentinel） | orb 隔离只解决「别碰宿主」，没解决「别把数据发出去」；守门人是全局 agent 能放心接管 GitHub、邮件、部署的前提 | 改动网络栈（Docker、Sysbox、Firecracker 的出网都要统一走代理）；审核模型带来延迟和成本；文档要写明哪些副作用不在审核范围内（例如 Grok 的记忆写入） |
| 13 | **凭据替身与 Vault**：orb 里只放 surrogate token，由出网代理在边界上注入真实 token；客户端提供「secret 卡片」，输入的值不进对话记录、不给模型看（Muse authd、Grok Vault / secret card） | 根除「让 agent 打印 GITHUB_TOKEN」这类注入；适合 GitHub、npm、云厂商的密钥 | 需要 TLS 终止或按协议改写；非 HTTP 协议（ssh、git+ssh）要另想办法 |
| 14 | **审批卡是带范围的能力**：范围包括一次、会话、任务、限时、永久，并且严格匹配；审批请求不经对话、直接推到客户端（WS、推送、Telegram 按钮），用户答复直接回到守门人；后台触发的审批 30 分钟过期；拒绝后在下一条用户消息前不再尝试；只拦难以撤销的动作，避免用户什么都点同意（Muse、Grok） | 让「多端加 Telegram」真正成为控制面；防止模型伪造「用户已同意」 | 跨端的审批状态要一致；过期和重试的语义要设计 |
| 15 | **外部内容一律标为不可信，只有用户消息算指令**：PR 评论、issue、网页、邮件进入上下文时打上 untrusted 标签；发现隐藏的「给 AI 的指令」时告知用户，并说明没有执行（Grok changelog、Muse harness） | coding agent 最常见的注入面就是 issue、PR 评论和依赖里的 README | 需要在 Oryn Core 的工具结果里统一打标签，并在系统提示里配合说明 |
| 16 | **电脑可见度分三级**：状态（agent 正在用电脑）→ 侧栏预览 → 全屏接管再交还；用户接管期间 agent 暂停（Grok Bot 三级可见度、Muse 浏览器接管） | Oryn 的远程桌面可以直接复用，用来处理 SSO、2FA、CAPTCHA 卡点 | orb 远程桌面要和 agent 之间做「暂停/恢复」的握手 |
| 17 | **大规模扇出 Workflow 与 /goal**：分阶段脚本、128–1024 个 agent 预算、断点续跑、内置对抗式核验、存成 slash 命令；/goal 自动拆进度清单并支持暂停/恢复；wide-research 的 manager 契约必须报告覆盖率（成功数/总数）（Grok Build Workflows、/goal，Muse wide-research） | 直接适用于代码审查、issue 分诊、全库审计，是 coding 工作台的杀手级能力；Space 是承载它的天然位置 | orb 冷启动的延迟和资源成本高；需要并发配额和预算上限；结果汇总要防幻觉（所以要有核验阶段） |
| 18 | **会持续更新的 Artifact，即 Spaces / Pages**：Artifact 绑定一个 Routine 后在后台刷新（例如仓库健康面板、发布看板），可以固定在侧栏，或按时间表出现在首屏（Muse Spaces、Grok Pages） | 把 Oryn 现有的 Artifacts 从「一次性产物」升级为「活的仪表盘」 | 数据源权限和刷新成本；**分享默认必须私有**：Grok Build 默认「Anyone with the link」，Muse 分享链接可被枚举，两者都出过泄露事故 |
| 19 | **渠道是「转交任务」，不是「镜像对话」**：在 GitHub 评论或 Telegram 里 @全局助手，渠道里只回一句确认，工作和结果留在 Oryn App 里；遇到身份未绑定、额度不足、带不支持的媒体时，回复一句提示或保持沉默；之后可以考虑给 agent 一个自己的邮箱（Grok @bot、Muse 邮箱地址、WhatsApp） | 多入口但单一事实来源，避免在公开渠道泄露工作内容 | 账号绑定和防冒用；公开渠道内容是注入面，要按第 15 条标记为不可信 |
| 20 | **用量与预算透明**：每个 routine 每次运行的成本、设定上限；按产品拆分的用量；长期无人查看时自动询问是否暂停例程（Grok 每周用量池、routine 用量提示；cookbook 里每次运行都有 cost cap） | 自托管用户直接为 token 付费，后台常驻 agent 最容易「悄悄烧钱」 | 需要统一计量（Oryn Core 上报 usage，orynd 汇总） |
| 21 | **人格文件化**：`SOUL.md` / `IDENTITY.md`（名字、气质、边界）可由用户或 agent 编辑；agent 改了要告诉用户；人格文件不能当安全策略（Muse / OpenClaw 模式） | 低成本实现「属于你的助手」，迁移和备份都简单 | 人格漂移；必须把「可写的人格」和「强制执行的权限」分开 |
| 22 | **从其他助手导入记忆**：给用户一段提示词，或接收导出 zip，然后写进主 Bot 的记忆（Grok Bot Import、Muse Import memory） | 冷启动的价值很高，实现成本低 | 导入内容要先清洗、去重，并标注来源 |
| 23 | **团队与多用户（远期）**：共享 Bot，但每个人与它的对话和个人记忆私有；Bot 从队友那里学到的「团队知识」要经 owner 审批才生效；一人教会，所有人受益（Grok Team Bots） | 如果 Oryn 以后支持小团队自托管，这是现成的模型 | 当前单用户场景优先级低；权限模型复杂 |

**建议的优先顺序** [推断]：

- 先做地基：第 1、3、4、5、7、12、14 条。它们决定「全局 agent 是不是一个持久、可委派、安全的角色」。
- 再做体验和增益：第 9、10、11、17、18 条。分别对应主动性、自进化、大规模扇出、活的产物。
- 其余条目按需加入。

**要避开的坑**（都来自上面两家的真实事故）：
- 生成物和分享链接默认公开（Grok Build、Muse 分享链接）。
- 「在共享电脑上分出多个 Bot」并不构成安全边界（Grok Bot 官方文档自己承认）。
- agent 以用户身份做出承诺或透露隐私（Muse 的 Marketplace 事件）：应用 PII 出网分类器，并且凡是「代表用户承诺」的动作都要强制审批。
- 在第三方网站上不表明 agent 身份（Amazon 封禁 Muse）。
