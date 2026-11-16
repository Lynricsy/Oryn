# ChatGPT App（OpenAI）调研：面向 Oryn「全局助手 → superagent」的借鉴

> 调研日期：2026-11-16。资料以 OpenAI 官方为主：Help Center 发布说明（release notes）、官方博客、developers.openai.com / learn.chatgpt.com 文档。
> 标注约定：**[官方]** 表示官方文档或博客已确认；**[第三方]** 表示媒体、社区或逆向分析；**[推断]** 表示本文作者的推断。
> 主索引：ChatGPT release notes <https://help.openai.com/en/articles/6825453-chatgpt-release-notes>（下文简称 RN，按日期引用）。

---

## 0. 先看结论：2025→2026 的产品走向（时间线）

ChatGPT 在这两年里一直在试错和合并功能。直接照搬功能清单会抄到已经被它砍掉的东西，所以先看时间线。

| 时间 | 事件 | 现状（2026-11） | 来源 |
|---|---|---|---|
| 2025-01-14 | Scheduled tasks（beta） | 已演进为 Scheduled 页 + 监控任务 + 事件触发任务 | RN 2025-01-14、2026-06-17、2026-08-25 |
| 2025-02 | Deep research | 2026-02 改版：研究计划可编辑、运行中可改方向、可限定站点；2026-03-26 移除旧版模式 | RN 2026-02-10、2026-03-19；<https://help.openai.com/en/articles/10500283-deep-research-in-chatgpt> |
| 2025-04-10 | Memory 可引用全部历史对话（dreaming V0） | 2026-06 升级为 Dreaming V3，后台合成记忆 | <https://openai.com/index/chatgpt-memory-dreaming/> |
| 2025-06-04 | 自定义 MCP connector（深度研究用） | 已并入 Apps，后又并入 Plugins | RN 2025-06-04 |
| 2025-07-17 | ChatGPT agent（Operator + deep research 合体） | 能力并入 **ChatGPT Work**（2026-07-09） | RN 2026-07-09；<https://help.openai.com/en/articles/11752874-chatgpt-agent> |
| 2025-09-25 | **Pulse**（每日主动简报，Pro） | **2026-06-17 宣布下线**，主动更新并入 scheduled tasks | RN 2026-06-17 |
| 2025-09-29 | Instant Checkout + Agentic Commerce Protocol；家长控制 | 2026-11 推出 Wallet（替代卡凭证 + 生物识别授权） | RN 2025-09-29、2026-11-13 |
| 2025-10 | **Apps SDK**（MCP + 内嵌 UI），12 月上线 app directory | 2026-07-09 App Directory 改为 **Plugin Directory**；UI 协议改用开放标准 **MCP Apps** | <https://openai.com/index/introducing-apps-in-chatgpt/>；RN 2025-12-18、2026-07-09 |
| 2025-10 | Atlas 浏览器 | **2026-08-09 停止服务**，浏览器能力并入桌面 app 和 Chrome 扩展 | RN 2026-07-09 |
| 2025-11-13 | Group chats | **2026-07-09 停止新建** | RN 2026-07-09 |
| 2024-10 | Canvas | 2026-05-28 起新模型不再支持，改为 writing blocks / code blocks | RN 2026-05-28 |
| 2026-07-09 | **ChatGPT Work**（长任务 agent）、新桌面 app（Chat / Work / Codex 合一）、Skills GA、ChatGPT Sites | 现行主线 | RN 2026-07-09 |
| 2026-09-11 | 计划下线 custom GPTs，迁移到 plugins | 迁移进行中 | RN 2026-09-11 |
| 2026-09-29 | **dots**：常驻 agent，有自己的云电脑；同日推出 ChatGPT Space | 现行 superagent 形态 | <https://openai.com/index/introducing-dots/>；RN 2026-09-29 |
| 2026-10-07 | GPT-6 + **Intelligent UI**（模型生成可交互界面） | 现行 | <https://openai.com/index/gpt-6-for-everyone/> |

**一句话**：OpenAI 先做了很多独立功能（Pulse feed、Atlas、group chat、canvas、GPTs），后来逐个收敛。截至 2026-11，体系由四部分组成：
1. **Chat**：快速对话，带 Intelligent UI。
2. **Work**：长任务 agent，有云电脑和本地电脑两种运行位置。
3. **Codex**：编码。
4. **dots**：常驻 agent，可以把任务委派给 Work 和 Codex。

另外两层横向基础设施贯穿全部：
- **Plugins** = Skills + MCP + UI + Hooks + Events，Chat、Work、Codex 共用同一个目录。
- **统一 Memory**：后台合成，带来源归因。

**[推断]** 这条收敛路径本身就是最值得 Oryn 借鉴的东西：「全局助手」不应再堆独立功能，应该做成常驻协调者 + 委派执行线程 + 统一记忆、权限与插件层。

---

## ① 产品是什么

ChatGPT App 是 OpenAI 的消费级 + 企业级 AI 助手，覆盖 Web、iOS、Android、macOS、Windows 桌面，Linux 桌面处于公开预览（RN 2026-08-14）。外部入口包括 WhatsApp、Kakao、Viber（RN 2026-07-13），以及 Slack、Teams，并通过 iMessage / RCS 与 dot 互发短信（RN 2026-10-28）。周活约 12 亿（<https://openai.com/index/gpt-6-for-everyone/>）。

2026 年的结构：
- **Chat**：日常问答。GPT-6 Sol / Luna 驱动，带 Intelligent UI，可以边思考边回答。
- **Work**：长任务 agent，做研究、分析，产出文档、表格、幻灯片、Sites。任务运行在云端或本机，可做定时、事件触发和监控（<https://help.openai.com/articles/20001275>）。
- **Codex**：编码 agent，有桌面 app、CLI、IDE、Cloud 和手机远程控制，与 ChatGPT 桌面 app 合一（RN 2026-07-09、2026-09-29）。
- **dots**：「always-on agent」。有自己的云电脑和浏览器，在对话之间持续推进目标，可以把任务委派给 Work 和 Codex，支持多渠道（ChatGPT、Slack、Teams、短信），会做后台「proactive research」（<https://openai.com/index/introducing-dots/>）。
- **横向层**：Memory、Projects / Space / Library、Plugins（apps + skills）、Scheduled、Voice（GPT-Live）、Images、Health / Finances / Shopping / Wallet 等垂直场景。

---

## ② 功能清单（按能力域）

### 2.1 对话
- **模型选择**：按推理档位选择，分 Instant / Medium / High / Extra High / Pro（RN 2026-06-10）。Plus / Pro 不再自动从 Instant 切到 Thinking，只在安全场景下自动切换（RN 2026-09-14）。
- **GPT-6 边想边答**：先给部分答案，再逐步补充（<https://openai.com/index/gpt-6-for-everyone/>）。
- **Intelligent UI**：模型从「原生可流式组件库」中选择组件组合成回答，由编译器渐进渲染（同上）。之前铺垫过的能力：交互式图表（RN 2026-06-08）、交互式 code block 与 mini app 预览（RN 2026-02-19）、70+ 个数理交互模块（RN 2026-03-10）。
- **长回答编辑**：writing blocks 支持全屏写作，取代 canvas（RN 2026-05-28、2026-06-08）。
- **对话操作**：支持分支对话（RN 2025-09-04），可以在长任务运行中打断并补充要求（RN 2025-11-05），长对话自动生成目录（RN 2026-06-08）。
- **大段粘贴转附件**：超过 10k 字符的粘贴自动转为附件，避免撑爆上下文（RN 2026-06-22）。
- **Fast answers**：对高置信度的通用问题走快速路径，不引用记忆（RN 2026-04-22）。

### 2.2 记忆与上下文
- **Memory**：三代演进，即 saved memories → reference chat history → Dreaming V3。配套 **Memory summary**（可编辑）、**Sources**（回答下方显示用到了哪些记忆、历史对话、文件、邮件）和「Don't mention this again」（<https://help.openai.com/en/articles/8590148-memory-faq>）。详见 §3.1。
- **Projects**：项目指令覆盖全局指令；项目来源可以是文件、Slack 频道或 Drive 链接、保存下来的回答、临时文本；可共享，分 chat / edit 两种权限；有 **project-only memory**（<https://help.openai.com/en/articles/10169521-projects-in-chatgpt>；RN 2025-08-22、2026-02-25、2026-08-14）。
- **Library / Space**：上传和生成的文件自动进入 Library，有配额，可 @ 引用，可共享文件夹（RN 2026-03-23、2026-05-14、2026-09-09）。Space 把 Pages、文件和相关工作组织在一起，支持多人协作，对有权限的账号取代 Library（RN 2026-09-29）。
- **Temporary chat**：可选择「带个性化」，即使用记忆、插件和自定义指令，但不产生新记忆；也可以事后保存为普通对话（RN 2026-08-27）。
- **Locked chats**：用 PIN 或生物识别隐藏对话；锁定期间的新消息不进入记忆（RN 2026-10-20；Memory FAQ）。
- **Computer History（macOS）**：把本机交互事件汇总为本地 Markdown 记忆和时间线（<https://developers.openai.com/codex/customization/computer-history>）。详见 §3.1。

### 2.3 主动性与定时
- **Pulse**（2025-09 → 2026-06 下线）：每晚异步研究，次日推送卡片（§3.2）。
- **Scheduled tasks**：统一 Scheduled 页；支持时间窗口（早 / 午 / 晚）；监控任务「有值得报告的才通知」；每小时最多跑 1 次；不活跃任务自动暂停（RN 2026-06-17；<https://help.openai.com/articles/10291617>）。
- **事件触发任务**：由 Gmail、Slack、GitHub PR 的 webhook 触发；每小时最多 30 次、每天最多 720 次；需要审批的动作会让任务暂停（RN 2026-08-25）。插件可以通过 **MCP Events** 提供事件源（RN 2026-09-29；<https://developers.openai.com/plugins/build/mcp-events.md>）。
- **任务分享**：分享的是快照（标题、指令、日程、时区），不带记忆和凭证，接收者自己创建副本（RN 2026-08-25）。
- **dots 主动研究**：后台只读扫描已连接的应用，记私有笔记，再主动提建议（§3.4）。
- **首页个性化建议**：基于对话历史和已连接工具生成（RN 2026-08-14）。

### 2.4 工具与连接器
- **Connectors → Apps → Plugins**：
  - 2025-06 开放自定义 MCP connector；
  - 2025-12 connectors 改名为 apps 并上线目录；
  - 2026-07 目录改为 Plugin Directory，plugin = skills + apps + app templates + extensions（<https://help.openai.com/en/articles/20001256-plugins-in-chatgpt-and-codex>）。
- **同步型连接器**：Pro 用户可把 Notion、Linear、Drive 等预先索引，回答时自动引用（RN 2025-10-09）。
- **多账号连接**：同一插件可连个人和工作两个账号（RN 2026-08-28、2026-09-17）。
- **App 权限四档**：Always ask / Allow read actions / Allow low-risk actions / Allow all actions（<https://help.openai.com/en/articles/11487775-connected-apps-in-chatgpt>）。
- **Site tools（WebMCP）**：桌面内置浏览器可以直接调用网页暴露的工具（RN 2026-08-31）。
- **浏览器扩展**：支持 Chrome、Edge、Brave、Opera、Vivaldi，可 @ 引用标签页、控制浏览器（RN 2026-08-31）。
- **Office 加载项**：Excel、Sheets、PowerPoint、Word（RN 2026-05-05、2026-07-06、2026-09-17）。

### 2.5 多 agent
- **Agent mode**（2025-07）：单 agent，在虚拟电脑里使用可视浏览器、文本浏览器、终端、API 和 connectors（§3.4）。
- **GPT-6 Pro**：Chat 里可以并行跑多个 agent 处理任务的不同部分，每个 agent 的过程可以展开查看（RN 2026-10-16）。
- **dots**：主 dot 把工作分给并行的后台 agent，也可以新建可见的 Work / Codex 线程并继续给它们发指令（<https://learn.chatgpt.com/codex/dots/tasks-and-memory.md>）。
- **规划中**：「teams of dots」；面向企业的 specialist dots，拥有独立身份和凭证，接入 Microsoft Agent 365（<https://openai.com/index/introducing-dots/>）。
- **Codex app**：多个 agent 并行，每个用隔离的 worktree（RN 2026-02-02、2026-03-04）。

### 2.6 多端与消息渠道
- **全平台客户端**：Web、iOS、Android、macOS、Windows、Linux（预览）、CarPlay（RN 2026-04-02）。
- **同一 dot 的多个入口**：ChatGPT、Slack、Teams、iMessage / RCS（通过第三方 Linq 中转，不是端到端加密）（<https://help.openai.com/en/articles/20001530>）。
- **Codex Remote**：手机通过 QR 码与桌面主机一对一配对，在手机上远程审批和继续桌面任务（RN 2026-05-14、2026-06-25）。
- **云端 Work 跨端同步**：web、mobile、desktop 之间同步；本地任务只留在本机（RN 2026-07-16）。

### 2.7 媒体生成
- **ChatGPT Images**：2025-12 新模型，统一放在 My images。2026 年迭代到 2.0、2.5、2.6：
  - images with thinking（生成前先规划）；
  - 模板、草图转图、评论式修改；
  - **保存参考图**（用 @Pepper 复用）；
  - GIF；虚拟试穿；贴纸包。
  （RN 2025-12-16、2026-04-21、2026-09-08、2026-10-01、2026-11-04、2026-08-31）
- **视频**：Sora 以独立 app 和积分形式存在（RN 2025-10-30、2026-03-10）。本次未在 RN 中找到「ChatGPT 内置视频生成」的条目 **[推断：视频仍以 Sora 为主]**。
- **音频**：上传录音做转写和总结（RN 2026-10-06）；macOS 上的 **Record** 做会议转写和总结（<https://help.openai.com/en/articles/11487532-chatgpt-record>）；Meetings 插件只保留笔记、删除音频（RN 2026-09-29）。

### 2.8 搜索与研究
- **Search**：回答内嵌网页图片，高亮实体后可打开侧栏（RN 2025-11-21、2026-01-30）；可共享设备位置，精确位置用完即删（RN 2026-03-26）。
- **Deep research**：先给研究计划，用户可以编辑；运行中可改方向；可限定或优先指定站点；只调用 app 的读动作；最终输出带引用的报告（<https://help.openai.com/en/articles/10500283-deep-research-in-chatgpt>）。
- **Shopping research**：模型是 GPT-5-mini 做 RL 后训练的专用版本，用交互式 UI 收集「不感兴趣 / 更多类似」反馈，最终给出购买指南（<https://openai.com/index/chatgpt-shopping-research/>）。
- **其他垂直场景**：求职（RN 2026-06-01）、餐厅预订（RN 2026-08-10）、Health / Finances（RN 2026-01-07、2026-05-15 等）。

### 2.9 个性化与人格
- **Base style 预设**：Default / Friendly / Efficient / Professional / Candid / Quirky 等（RN 2025-11-12；Nerdy 于 2026-03 下线）。
- **细粒度特征滑杆**：温暖程度、热情程度、标题和列表的多少、emoji 的多少（RN 2025-12-19）。
- **自定义指令**：上限从 1500 提到 5000 字符（RN 2026-07-15）；修改对所有对话（含已有对话）立即生效（RN 2025-11-07）。
- **dot 的人格设置**：可改名字和头像，也可选角色或宠物；有独立于 ChatGPT 的「Behavior」指令（<https://learn.chatgpt.com/codex/dots/controls.md>）。
- **Study mode**：最初用定制系统指令实现苏格拉底式引导，计划之后训练进主模型（<https://openai.com/index/chatgpt-study-mode/>）；后续增加测验、闪卡（RN 2026-08-14、2026-09-22）。

### 2.10 安全与权限
- **Lockdown Mode**：禁用实时浏览、深度研究、agent 和文件下载，降低提示注入导致的数据外泄风险；可以按单个对话关闭（<https://help.openai.com/en/articles/20001061>）。
- **agent 安全机制**：高影响动作需用户确认；takeover 模式（接管期间不截图）；敏感站点用 watch mode；提示注入监控；站点黑名单（agent 帮助页）。
- **dots 权限**：Custom Rules 四档；Auto-review 审查 agent；安全监控可暂停任务；主动研究工具只读（§3.4）。
- **家长控制**：静默时段、关闭语音 / 记忆 / 生图；高风险情况经人工复核后通知家长（<https://openai.com/index/introducing-parental-controls/>）。另有年龄预测（RN 2026-01-20）、Trusted contact（RN 2026-05-07）。
- **账号安全**：Advanced Account Security 用 passkey 和恢复密钥（RN 2026-04-30）；Active sessions、Security history（RN 2026-06-02、2026-09-25）；Privacy Center（RN 2026-09-21）。

### 2.11 扩展生态
- **Plugins**：skills（SKILL.md）+ MCP server + MCP Apps UI + 生命周期 hooks + MCP Events；ChatGPT 与 Codex 共用一个目录（<https://developers.openai.com/plugins/concepts/plugins.md>）。
- **创建与分发**：通过 @plugin-creator 用对话生成插件；marketplace 是 JSON 目录，可从 GitHub 每日同步（Plugins 帮助页）。
- **Sites**：可托管 MCP app（Plugins 帮助页）。
- **Sign in with ChatGPT**：其他产品可以用 ChatGPT 账号登录（RN 2026-07-29），也可以使用用户的 ChatGPT 额度（RN 2026-09-29）。
- **商业化**：Agentic Commerce Protocol、Instant Checkout、Business Hub 的商家 agent（RN 2026-11-10）。

---

## ③ 架构与关键机制

### 3.1 Memory：写入与检索策略

#### 3.1.1 三代演进 [官方]
来源：<https://openai.com/index/chatgpt-memory-dreaming/>

1. **Saved memories（2024-04）**
   - 只在对话中写入，依赖「remember I'm…」这类强触发词；
   - 会过期，内容会变得不准确或不相关；
   - 官方原话：像「记了几笔笔记，但没记下的都忘了」。
2. **Dreaming V0（2025-04）**
   - 在 saved memories 之外，引入后台进程，从多次对话中**自动整理和合成**记忆；
   - 但它「historically was never sufficient as a standalone memory system」，只是补充。
3. **Dreaming V3（2026-06）**
   - 「significantly more capable and compute-efficient memory architecture built on top of dreaming」；
   - 能**随时间自动改写**记忆，例如把「July 要去新加坡」改成「2026 年 7 月去过新加坡」；
   - 给 Free 用户服务的算力约降到原来的 1/5，Plus / Pro 的记忆容量翻倍（RN 2026-06-04）。
   - 官方评估的三个目标：**carry forward context / follow preferences & constraints / stay current over time**。偏好分三类：对回答方式的指令、个人偏好或约束、隐式偏好（如「住在 SF 附近」意味着本地推荐要按这个区域）。

#### 3.1.2 用户可见的控制面 [官方]
来源：<https://help.openai.com/en/articles/8590148-memory-faq>

- **数据源**：past chats、saved memories、custom instructions、Library / Space 文件、connected apps（如 Gmail）。
- **检索时机**：「ChatGPT looks for relevant context when it is likely to improve a response」，并非每轮都检索。记忆还可以用来改写网页搜索的查询（如加上位置或饮食偏好）。
- **Memory summary**：高层摘要，不保证完整。可以直接输入修改、划选某段文字修改、选「Don't mention this again」（不删除来源，只降低提及），或「Delete and turn off memory」。
- **Sources**：回答下方列出用到的 saved memory、过去的对话、自定义指令、文件、邮件；可以逐条纠正、删除或标为不相关。分享对话时不带 Sources（RN 2026-05-05）。
- **删除语义**：
  - 删除对话 ≠ 删除由它衍生的 saved memory；
  - 关闭 reference chat history 后，衍生信息在 30 天内删除；
  - 已删除记忆的日志最多保留 30 天。
- **作用域隔离**：
  - **Project-only memory**：只能引用同一项目内的对话；项目里的信息也不会流到项目外。共享项目强制为 project-only（RN 2026-08-14）。
  - **Temporary chat**：personalized 档可以读记忆，但从不写记忆（RN 2026-08-27）。
  - **Group chat**：不读也不写个人记忆（<https://openai.com/index/group-chats-in-chatgpt/>）。
  - **Health**：对话、记忆、文件与主体隔离（RN 2026-01-07）。
- **退回旧版**：Settings > Memory > Saved memories 可切回显式条目列表（RN 2026-06-04）。
- **自动管理（2025-10）**：按「新近程度 + 话题出现频率」决定哪些记忆排在前面，其余移到后台；用户可以手动调整优先级，也可以查看和恢复历史版本（RN 2025-10-15）。

#### 3.1.3 注入形态 [第三方]
Simon Willison 引用 Johann Rehberger 的逆向分析（<https://simonwillison.net/2025/May/21/chatgpt-new-memory/>、<https://embracethered.com/blog/posts/2025/chatgpt-how-does-chat-history-memory-preferences-work/>）。结论：2025 年版「chat history」不是 RAG，而是**周期性更新的用户档案摘要，每次新对话都注入上下文**。可以导出的小节有：Assistant Response Preferences / Notable Past Conversation Topic Highlights / Helpful User Insights / User Interaction Metadata，另有 Recent Conversation Content。其中每条洞察带时间范围，例如「Discussed between June 2024 and October 2024」。

注意：2026 年 1 月官方又加入了「更可靠地从过去对话中**查找**具体细节，并把用到的对话列为来源」（RN 2026-01-15）；dots 文档也写明它「can search your past ChatGPT conversations」。**[推断]** 现行架构是两路并用：**常驻注入的合成档案**（dreaming 产物）+ **按需检索过去对话的工具**（search past chats）。

Simon 的批评值得 Oryn 注意：自动记忆会让用户「失去对上下文的控制」，他举的例子是生图里莫名出现了家乡的路牌。OpenAI 后来补上的 Sources、Don't mention、unpersonalized temporary chat，正是回应这类问题。

#### 3.1.4 Codex 本地记忆 [官方]
来源：<https://learn.chatgpt.com/docs/customization/memories.md>

这是对 Oryn 最可直接复用的写入策略：
- **合成方式**：从「合格的过往对话」异步生成本地 Markdown 记忆文件。会**跳过进行中或很短的会话**，等对话**空闲足够久**才总结；生成的字段会**脱敏 secrets**；剩余额度低于阈值时跳过后台合成（`memories.min_rate_limit_remaining_percent`）。
- **两阶段模型**：`memories.extract_model`（逐对话抽取）+ `memories.consolidation_model`（全局合并）。
- **防注入**：`memories.disable_on_external_context = true` 时，用过 MCP 工具或网页搜索的对话**不参与**记忆生成，防止外部内容被写成记忆。
- **逐对话开关**：`/memories` 控制当前对话「能否读记忆」和「能否成为未来记忆的输入」。
- **定位**：官方明确说团队必须遵守的规则放在 `AGENTS.md`，记忆只是「helpful recall layer」。

#### 3.1.5 Computer History [官方]
来源：<https://developers.openai.com/codex/customization/computer-history>

- 采集 macOS 辅助功能层的交互事件（点击、输入、切换 app），不截图、不录音、不含隐私浏览；
- 事件流本地最多存 48 小时，然后**周期性起一个临时 Codex 会话**把事件总结成 `~/.codex/memories/extensions/skysight/` 下的 Markdown 记忆；
- 时间线条目可以建议把「重复工作」转成 skill 或 automation；
- 官方明确提示它会放大提示注入风险。

#### 3.1.6 dots 的记忆分层 [官方]
来源：<https://learn.chatgpt.com/codex/dots/tasks-and-memory.md>；<https://learn.chatgpt.com/docs/enterprise/dots-security>

- **三层**：
  1. 对话上下文：选择性装入；
  2. ChatGPT Memory：dot 启动时获取相关部分；
  3. **dot 私有笔记**：偏好、决策、进行中的工作；不是完整转录；与 ChatGPT 记忆分开。
- 每个委派任务有**独立对话**，只拿到 dot 交给它的指令和上下文，不会自动拿到全部历史。
- **跨渠道**：Slack、Teams、ChatGPT 共享同一套笔记，但对话彼此可见性独立；「能使用」某条信息不代表「能向另一个受众披露」它。
- **删除**：私有笔记不能逐条删，只能删除整个 dot；断开某个 app 不会删除已经衍生出的笔记。这一点受到企业侧质疑，属于设计代价。

### 3.2 Pulse：异步研究流程，以及它为什么被下线

**流程 [官方]**（<https://openai.com/index/introducing-chatgpt-pulse/>；RN 2025-09-25、2025-10-29）：
1. **输入**：memory + chat history + 直接反馈（点赞 / 点踩、Curate 指令）+ 可选的 Gmail / Calendar（默认关闭）。必须开启 saved memory 和 chat history。
2. **每晚异步研究**：「Each night, it synthesizes information… then delivers personalized, focused updates the next day」。晚上提出的新需求「力争」第二天早上就包含进去，但时间不保证。
3. **输出**：每天一组可视化卡片，**只当天有效**。保存为对话或追问后才会进入历史。
4. **反馈闭环**：Curate 可以要求「明天聚焦网球」之类；反馈历史可以查看和删除。话题会经过**安全过滤**。
5. **扩展**：后来加入 shopping research 主动生成的购买指南（<https://openai.com/index/chatgpt-shopping-research/>）；2025-12 把 Tasks 并入 Pulse 统一管理（RN 2025-12-17）；设置里有「Pulse memory suggestions」开关（dreaming 博客的配图）。
6. **未公开的部分 [第三方]**：话题排序算法、每张卡片的检索次数、所用模型都没有公开。

**下线 [官方]**：2026-06-17 宣布「Pulse is being sunset as proactive updates move into scheduled tasks」，官方替代方案是让 ChatGPT「schedule a daily briefing based on your interests and past chats」。同时上线的监控任务「notify users only when there is something worth reporting」（RN 2026-06-17）。3 个月后，dots 的「proactive research」接手了「无人要求时的主动性」，但改成**持续后台 + 只读工具 + 私有笔记 + 有价值才打扰**（§3.4）。

**[推断] 教训**：
- 每天固定一组的 feed 是**推送节奏驱动**，不是**价值驱动**，容易推「已经做完的项目」这类无关内容（官方在 Limitations 里自承过）。
- 主动性最终落在两处：① 用户可见、可编辑的**任务**（定时 / 监控 / 事件）；② 有持续目标的**常驻 agent**。
- 独立的 feed 页面没有保留下来。

### 3.3 Scheduled tasks / 事件触发：调度与数据结构

**[官方]**（<https://help.openai.com/articles/10291617>；<https://help.openai.com/articles/20001275>）

- **任务类型**：一次性 / 循环 / 监控 / 事件触发。监控任务「can use information from previous runs and stop when a defined end condition is met」，即**跨运行状态**加**终止条件**。
- **调度**：
  - 精确时间（付费）或模糊窗口（Free 用户分早、午、晚）；
  - 循环任务最高每小时 1 次；事件任务全局每小时 30 次、每天 720 次，**多个事件可能合并处理**；
  - 不活跃任务自动暂停；关联的对话被归档或删除时任务也暂停。
- **事件任务三元组**：创建后展示 **Trigger / Condition / Prompt** 供审阅。只能在 Work 里运行，Slack 频道需要先 @ChatGPT 加入。
- **审批与计费**：遇到需要审批的动作，任务暂停等人处理。**后台运行即使没有发更新也会消耗额度**。
- **分享**：分享链接只含快照（标题、指令、日程、原时区，可能还有模式或模型），不含记忆、文件、凭证、历史结果；接收者用自己的权限创建独立副本。
- **不支持的场景**：语音对话、GPTs、locked chats。

**MCP Events 协议 [官方]**（<https://developers.openai.com/plugins/build/mcp-events.md>）：
- **能力声明**：server 在 `initialize` 返回（或 MCP 2.0 `2026-07-28` 的 `server/discover` 返回）中声明 `capabilities.events`。
- **三个方法**：`events/list`（含 `inputSchema` 过滤参数、`payloadSchema`、`delivery: ["webhook"]`）、`events/subscribe`、`events/unsubscribe`。
- **订阅请求**：ChatGPT 订阅时提供 `delivery.url` 和 `delivery.secret`（`whsec_…`，base64 解码后 24–64 字节）。
- **订阅 ID 与续期**：server 按「主体 + 回调 URL + 事件名 + 参数」派生**确定性订阅 ID**，以保证幂等（参数比较用 canonical JSON）；返回 `refreshBefore` 表示续期期限。
- **回调校验与签名**：发真实数据前先发 `{"type":"verification","challenge":…}` 做回调校验；请求头带 `webhook-id`、`webhook-timestamp`、`webhook-signature`、`X-MCP-Subscription-Id`。
- **范围**：只支持 webhook，不支持轮询或流式。基于 MCP 实验扩展草案（<https://github.com/modelcontextprotocol/experimental-ext-triggers-events>）。

### 3.4 Agent mode → ChatGPT Work → dots：执行与控制循环

**Agent mode（2025-07）[官方]**（<https://openai.com/index/introducing-chatgpt-agent/>；<https://deploymentsafety.openai.com/chatgpt-agent>；<https://help.openai.com/en/articles/11752874-chatgpt-agent>）
- **模型与环境**：o3 家族的 agentic 模型，运行在自己的**虚拟电脑**上。
- **工具**：visual browser（截图 + GUI 操作）、text browser、terminal（上线时网络受限）、direct API access、connectors；切换工具时保留任务上下文。
- **控制**：
  - 可以随时打断或接管；
  - 需要登录时暂停，用户「Take over browser」，接管期间**不截图**；
  - cookies 跨会话保留；
  - 高影响动作需要确认；敏感站点用 **watch mode**（必须有人看着）；有提示注入监控和全局站点黑名单。
- **计费**：只有用户主动发起的那条请求计数（Plus 40 次 / 月，Pro 400 次 / 月）；中途的澄清和认证不计；定时任务里每次调用 agent 都计一次。
- **Operator 背景**：监督学习负责屏幕感知，RL 负责高层推理和纠错（<https://openai.com/index/operator-system-card/>）。

**ChatGPT Work（2026-07）[官方]**（<https://help.openai.com/articles/20001275>）
- **定位**：「agent for longer, more involved tasks」，产出成品文档、表格、幻灯片、报告、Sites。可以跟进进度、回答问题、改方向、审批。
- **运行位置**：Cloud（OpenAI 托管的电脑，跨端同步）或 Local（桌面 app 在授权后操作本地文件和桌面应用；消息和上下文仍可能存在云端）。
- **与 Chat、Codex 的分工**：Chat、Work、Codex 三者**历史分开**、模型分开、默认值分开（管理员可以分别给 Chat 和 Work & Codex 设置起始模型）。
- **企业控制**：Work Cloud、Work Local、Codex Local、浏览器 / 网络是**独立开关**。
- **云端浏览器登录**：凭证不经过模型；支持密码管理器；可以按站点清除浏览器数据（RN 2026-08-25）。

**dots（2026-09）[官方]**（<https://openai.com/index/introducing-dots/>；<https://help.openai.com/en/articles/20001530>；<https://learn.chatgpt.com/docs/enterprise/dots-security>；<https://learn.chatgpt.com/codex/dots/tasks-and-memory.md>；<https://learn.chatgpt.com/codex/dots/controls.md>）

dots 是 OpenAI 目前的 superagent 形态。

- **身份**：每个成员一个个人 dot，有 handle（@name-dot）、名字和头像；入口包括 ChatGPT、Slack、Teams、短信，也可以语音通话（dot 还不能主动打电话）。
- **电脑**：
  - 自己的**云电脑**，与 Work Cloud 是同一套基础设施；可以**跨会话保留快照**；随时可以打开查看或接管。
  - 本机访问默认关闭，需要用户在桌面 app 里「Allow access」；本地任务以单独的线程运行，受本地 Agent Security 策略约束。
  - 可以在 Codex Cloud 环境里开编码任务。
  - **[第三方]** Tom's Hardware 根据 Geekbench 推测是 9 核 EPYC、约 10GB 内存的 VM（<https://www.tomshardware.com/tech-industry/artificial-intelligence/geekbench-7-results-suggest-openais-dots-agent-runs-on-nine-core-amd-epyc-vms-with-nearly-10gb-of-memory-newest-runs-score-about-six-times-meta-muse-in-multi-core>）。
- **工作循环**：
  - 主对话里可以同时有多个职责，随时插话改优先级；
  - dot「can decide when to pause and wake up to continue」，即**自我调度唤醒**，不需要每个跟进都排成固定日程；
  - 可以拆给**并行后台 agent**，它们向 dot 汇报；也可以建**可见线程**（Work / Codex）并继续发指令；
  - **Activity View** 列出进行中、已排程、已完成的工作，以及待处理的决定、登录、审批请求。
- **proactive research**：
  - 没有新请求时，dot 在后台**只读**访问已连接的 app，记**私有笔记**，发现关联后提出建议，例如「发布决定与上周的草稿冲突」；
  - 研究工具在技术上**不能**发消息、改 app 内容、操作浏览器或电脑；后续动作走正常审批；
  - 官方不直接用主动研究和私有笔记训练模型。
- **权限模型（四层叠加，只能收紧）**：
  1. **连接权限**：继承 owner 已授权的账号权限，再受工作区 app / action 设置约束。
  2. **Custom Rules**：自然语言描述一类动作，选「Take action without asking / Take action when you say so（pre-approved = 你在 prompt 里明确要求过）/ Ask before / Hand off to you」。规则**不能授予连接本身没有的权限**，也不能覆盖内置安全要求。
  3. **Standing authorization**：例如「每周五把周报发给项目负责人」，只覆盖指定的收件人、内容、时间；超出范围就要重新授权。「授权起草 ≠ 授权发送」。
  4. **Auto-review**：对标记为 write 的工具调用，由一个**独立的审查 agent** 对照 owner 指令、Custom Rules 和安全要求来判定（放行 / 要求确认 / 阻断），dot 不能关闭必需的检查；另有**安全监控**可以暂停或停止任务。改密码这类操作永远要用户本人完成。
- **Auto-review 细节 [官方]**（<https://learn.chatgpt.com/codex/sandboxing/auto-review.md>）：
  - 审查者看到的是精简转录 + 精确的审批请求，**不看主 agent 的隐藏推理**，可以做只读检查；
  - 被拒后，主 agent 被要求「不得绕路达成同一结果，只能走实质更安全的路径或停下来问用户」；
  - 连续 3 次拒绝，或 50 次审查内累计 10 次拒绝，会中断本轮（安全类模型首次拒绝即中断）；
  - 超时与拒绝分开处理；
  - 用户可以用 `/approve` 对**某一条被拒动作**单次放行，放行后仍要再过一次审查；
  - 策略是可替换的 `policy.md`，企业可以用 managed 配置强制。
- **审计**：企业 Compliance API 用 `client_id = DOTS` 标记 dot 相关记录，主要类型有 `TOOL_CALL_SUGGESTED`、`APP_MCP_CALL` / `APP_MCP_RESULT`；官方承认云端动作的细粒度审计仍在完善中。
- **删除、暂停与吊销语义**：
  - Pause 只停主任务；已委派的 Work / Codex 任务和独立日程不受影响。
  - Delete 会在 30 天内删除 dot 的对话、私有笔记、后台研究线程、云电脑和快照；但它产出的 Space 文件、Codex 线程、已写入 ChatGPT Memory 的内容**不随之删除**。
  - 管理员可以 pause 或 delete；吊销权限后，正在运行的工作可以跑完。
- **计费**：与 dot 对话不计入 ChatGPT 用量；它委派出去的 Work / Codex 任务照常计费。

### 3.5 Apps SDK → MCP Apps → Plugins：内嵌 UI 协议

**整体结构 [官方]**（<https://developers.openai.com/plugins/build/chatgpt-ui>；<https://developers.openai.com/plugins/reference>；<https://modelcontextprotocol.io/docs/extensions/apps>）

1. **注册 UI 资源**：MCP server 注册 `ui://…` 资源，MIME 为 `text/html;profile=mcp-app`，一般是打包好的单页 HTML 加 JS。
2. **工具描述符关联 UI**：
   - `_meta.ui.resourceUri`（标准字段；`_meta["openai/outputTemplate"]` 是兼容别名）；
   - `_meta.ui.visibility: ["model","app"]`：控制工具只给模型、只给 UI 还是都给；
   - `openai/toolInvocation/invoking|invoked`（≤64 字的状态文案）；
   - `openai/fileParams`（文件入参，形状为 `{download_url, file_id, mime_type?, file_name?}`）。
3. **工具结果三分**：
   - `structuredContent`：模型和 UI 都可见，必须符合 `outputSchema`；
   - `content`：模型可见的文本；
   - `_meta`：**只给 UI、不给模型**，用来做水合（hydration）数据。
   - 只有前两者进入转录。
4. **宿主预加载**：宿主可以在工具被调用之前预取 UI 资源，从而把工具入参流式推给 UI。
5. **沙箱渲染**：用**沙箱 iframe** 渲染。
   - `_meta.ui.csp` 声明 `connectDomains`、`resourceDomains`、`frameDomains`，默认禁止子 frame；
   - `_meta.ui.domain` 给每个插件分配独立 origin，默认 `web-sandbox.oaiusercontent.com`。
6. **通信桥**：**JSON-RPC over postMessage**（MCP 的一个方言）：
   - `ui/initialize`
   - `ui/notifications/tool-input`（需审批的工具在用户批准后才下发入参）
   - `ui/notifications/tool-result`
   - `tools/call`：UI 直接调用工具，不重新挂载
   - `ui/message`：以 UI 名义发一条后续消息
   - `ui/update-model-context`：把 UI 中的选择告诉模型
7. **ChatGPT 专有扩展 `window.openai`**（需要特性检测）：
   - `widgetState` / `setWidgetState`（同步，按 widget 实例持久化；结构化形状 `modelContent` / `privateContent` / `imageIds` 决定模型能看到什么）；
   - `requestDisplayMode`（inline / pip / fullscreen）、`requestModal`、`requestClose`；
   - `uploadFile` / `selectFiles` / `getFileDownloadUrl`；
   - `openExternal`（重定向目标白名单）；
   - `requestCheckout`（Instant Checkout）；
   - `notifyIntrinsicHeight`；
   - 环境信息：`theme`、`locale`、`safeArea`、`maxHeight`、`displayMode`；
   - 宿主通过 `openai:set_globals` 事件推送全局变量变化；iframe 的路由历史会同步到宿主导航。
8. **宿主传给 server 的 `_meta`**：`openai/subject`（匿名用户 id，用于限流）、`openai/session`（匿名会话 id）、`openai/organization`、`openai/locale`、`openai/userLocation`（粗略位置）、`openai/userAgent`。全部是 hint，**不能用于授权**。
   - 工具出错时返回 `_meta["mcp/www_authenticate"]`，用来触发 OAuth。
9. **工具注解**：`readOnlyHint`、`destructiveHint`、`openWorldHint` 必填，`idempotentHint` 可选。注解只影响宿主**怎么向用户呈现和审批**，服务端仍需自己鉴权。这与 App 权限四档（read / low-risk / all）对应 **[推断]**。

**两条关键设计准则 [官方]**：
- **数据工具与渲染工具分离（decoupled pattern）**：数据工具只返回 `structuredContent`；模型筛选和推理之后，再调用只负责渲染的工具，iframe 只渲染一次。这样避免频繁重新渲染，也让「模型先思考再呈现」。
- **三类状态各归其主**：
  - 业务数据：归 MCP server，是权威来源；UI 的每个动作都走工具调用，server 返回新快照；
  - UI 临时状态：归 iframe；
  - 跨会话状态：归开发者自己的存储，**不要用 localStorage**。
- **另外一条**：工具在没有 UI 时也必须可用，便于在不渲染组件的宿主里完成流程。

**Plugins 打包 [官方]**（<https://developers.openai.com/plugins/concepts/plugins.md>；<https://developers.openai.com/plugins/build/plugins.md>；<https://help.openai.com/en/articles/20001256-plugins-in-chatgpt-and-codex>）
- **组成**：Skills（`SKILL.md` 文件夹）+ 可选 MCP server（工具、鉴权、UI）+ **生命周期 hooks**（在 Work / Codex 运行时的指定节点执行命令）+ extensions（侧栏、文件查看器 / 编辑器、表单）+ events。
- **清单**：根目录 `plugin.json`（Agent Plugins 可移植格式，OpenAI 扩展放在 `extensions.com.openai` 下），兼容 `.codex-plugin/plugin.json` + `.mcp.json` + `skills/`。
- **Marketplace**：一个 JSON 目录（`~/.agents/plugins/marketplace.json` 或仓库内 `.agents/plugins/marketplace.json`），可以 `codex plugin marketplace add owner/repo`，企业版可从 GitHub 每日同步。
- **权限分离**：plugin 设置管「可见和安装」，app 设置管「账号、动作、provider 限制」，安装 plugin 不会绕过 app 授权。

**数据与路径**：
- Apps SDK 刚发布时用自家的 `window.openai` 协议（<https://openai.com/index/introducing-apps-in-chatgpt/>），后来与 Anthropic 等一起推动 MCP Apps 成为开放标准，ChatGPT 把自己的协议降级为兼容层 [官方：reference 页「Start with the open standard」]。
- 迁移指南：<https://github.com/modelcontextprotocol/ext-apps/blob/main/docs/migrate_from_openai_apps.md>。
- 做宿主可以直接用 `@mcp-ui/client` 或 `@modelcontextprotocol/ext-apps` 的 AppBridge（MCP Apps 文档）。

### 3.6 Intelligent UI：模型生成界面

**[官方]**（<https://openai.com/index/gpt-6-for-everyone/>）：「a library of native, streamable components, along with a compiler that processes the interface as the model generates it」。训练时评估生成界面的清晰度、有用性和完整性，让模型学会何时交互、何时纯文本。此外训练模型「interleave thinking with answering」：考虑用户等待时间，分多段渐进作答。

**[推断]** 与 MCP Apps 的区别：Intelligent UI 是**第一方受控组件 DSL**，由模型直接输出；MCP Apps 是**第三方 iframe**。前者依赖专门训练，自托管、多模型的 Oryn 很难复制其质量；但「受控组件库 + 流式编译」这一工程形态可以借鉴：用 JSON 或标记格式描述组件，由客户端渲染。

### 3.7 Deep research

**[官方]**（<https://help.openai.com/en/articles/10500283-deep-research-in-chatgpt>）：
- **循环**：描述目标 → 选择来源（站点 / 文件 / 自动可用的 app）→ **模型提出研究计划，用户可以改** → 运行，可以查看进度、随时打断改方向（包括改来源）→ 输出带引用的结构化报告（可导出 PDF，RN 2025-05-12）。
- **来源控制**：「Prioritize these sites, but allow full-web search」与「only these sites」两种模式；只调用 app 的**读动作**。

### 3.8 商业：ACP / Instant Checkout / Wallet

**[官方]**（<https://openai.com/index/buy-it-in-chatgpt/>；<https://developers.openai.com/commerce>；RN 2026-11-13）：
- **结果排序**：商品结果是自然结果，不受 Instant Checkout 影响。
- **下单流程**：用户确认后，ChatGPT 经 ACP 把订单发给商家后端，商家决定接受或拒绝，用自己的 PSP 扣款（Stripe Shared Payment Token：限定金额和商家的加密令牌），商家仍是记录方（merchant of record）。
- **Wallet（2026-11）**：给 agent 用的是**替代支付凭证**，真实卡号对 agent 和商家都不可见；每笔购买需要生物识别批准，批准前展示商家、商品、总额和卡片。

---

## ④ 对「自托管 coding-agent 工作台的全局 agent」的借鉴

先把 Oryn 现状（来自 `docs/protocol.md` 的 M9/M13 章节）与 ChatGPT 的结构对照：

| Oryn 现有 | ChatGPT 对应 |
|---|---|
| 全局助手 = `kind = assistant` 的协调对话，跑在 `@host` 协调宿主里；`omp --no-tools`，只有宿主工具 + 文件库 read/write | dot 的「主对话」，但还没有云电脑，也没有委派线程的能力 |
| Space 协调者（可以 create / send 线程）、`thread.create` / `thread.send` 父子线程 | dot → Work / Codex 委派线程 |
| schedules（`thread` / `new_thread` 两种动作）、`AutomationAction` 审计表 | Scheduled tasks / Compliance 记录 |
| 文件库（conversation / shared scope）、Artifacts | Library / Space / Pages / Sites |
| 推送 + Telegram 通知 | 推送 + iMessage / Slack / Teams 渠道 |
| Hindsight 长期记忆（M16 规划中，且明确「全局助手不接入」） | Memory / dreaming / dot 私有笔记 |

按价值和可行性排序：

### P0：结构性借鉴（决定 superagent 骨架）

**1. 把全局助手升级为「dot 式常驻协调者」：主对话 + 委派可见线程 + Activity View**
- **为什么**：dots 的核心不是更强的单 agent，而是**常驻主体 + 把执行下放到带完整工具的子线程**（Work / Codex），主对话保持轻量、持续可对话。这和 Oryn 的架构天然吻合：协调宿主是轻量常驻的主体，orb 线程是有完整工具的执行体。
- **怎么做 [推断]**：
  - 让全局助手获得 Space 协调者已有的 `create_thread` / `send_message` 宿主工具，可以跨项目建 work 线程；
  - 新增「我委派出去的线程」视图，相当于 Activity View，汇总进行中、已排程、待审批；
  - 子线程只拿到委派指令和必要上下文（dots 明确规定委派任务不继承全部历史），可复用 `[Oryn message from thread …]` 来源头行。
- **自我唤醒**：dot「can decide when to pause and wake up」。Oryn 可以给助手一个 `wake_me(at|after, reason)` 宿主工具，实现上写一条一次性的 `thread` 型 schedule。比 Pulse 式固定日报更有价值。
- **冲突与代价**：
  - 协调宿主现在刻意 `--no-tools`、受 landlock / seccomp 约束，这是安全优势。保持它不执行代码，执行全部下放到 orb，正好对应 dots「主体在云端协调，命令在被授权的机器上执行」；
  - 需要给助手建线程加并发和总量限额（ctl 已有 `rate_limited`）。

**2. 分层记忆 + 来源归因 + 作用域隔离（与 M16 Hindsight 对齐）**
- **为什么**：ChatGPT 最终收敛的是「后台合成档案 + 按需检索 + 显式条目 + agent 私有笔记」四层，配上 Sources 与「Don't mention again」。
- **建议分层 [推断]**：

  | 层 | 内容 | 写入方式 | 读取方式 |
  |---|---|---|---|
  | L1 显式记忆 | 用户说「记住…」 | 同步写，可编辑，对应 saved memories | 常驻注入 |
  | L2 合成档案 | 偏好、进行中项目、约束，**带时间范围** | 后台 consolidation，按 dreaming 的「stay current」改写时态 | 常驻注入，有预算 |
  | L3 历史检索 | 过往线程和转录 | 不写入 | 工具调用（Oryn 已有全文搜索） |
  | L4 助手私有笔记 | 进行中职责、决策 | 助手自写 | 只给助手 |

- **直接照搬 Codex 本地记忆的写入纪律**：
  - 只在线程**空闲足够久**后抽取，跳过短会话和进行中的会话；
  - 抽取模型与合并模型分开；
  - secrets 脱敏；
  - **用过外部内容（网页、MCP、未受信文件）的线程默认不参与记忆生成**（`disable_on_external_context`），这是防记忆投毒的低成本手段；
  - 额度低于阈值时跳过；
  - 逐线程设置 `/memories` 读写开关。
- **作用域**：
  - 对应 project-only memory：项目内的线程只读写本项目的记忆；
  - 对应 temporary chat：可以读、不能写；
  - 对应 group chat 隔离：Space 共享对话不读个人记忆。
- **UI**：每条回答的「Sources」能显示用了哪条记忆或哪条历史线程，并允许纠正、删除或标为不相关。这是抵消 Simon Willison 所说「失去上下文控制」的关键。
- **冲突与代价**：
  - M16 规划里「全局助手不接入」需要重新评估；
  - 后台合成会持续消耗 token。社区有人称 dot 一天烧掉 16B token（<https://www.reddit.com/r/OpenAI/comments/1wyoaw7/>，[第三方]，未核实），自托管用户对成本敏感，必须有预算和开关；
  - dots 的「私有笔记不能逐条删」受到企业质疑，Oryn 应该让 L4 也可以逐条查看和删除。

**3. 权限与审批：工具注解 → 审批档 → 规则 → 独立审查 agent**
- **为什么**：superagent 一旦能跨项目建线程、发消息、动 GitHub，最大的风险就是提示注入和越权。ChatGPT 的做法分层清晰，可以直接作为 Oryn 的统一模型：
  - 工具一律带 `readOnly / destructive / openWorld` 注解（MCP 标准）；
  - 用户级审批档，参照 Always ask / Allow read / Allow low-risk / Allow all；
  - **Custom Rules 四档**，用自然语言描述某类动作；
  - **Standing authorization**：「每周五发周报给 X」限定收件人、内容、时间，超出范围就重新授权；起草 ≠ 发送；
  - **Auto-review**：一个独立的审查 agent 只看精简转录和动作，不看主 agent 推理。拒绝后禁止绕路，连续 N 次拒绝中断，用户可以单次放行。Oryn 运行的是 fork 自 oh-my-pi 的 omp，可以在 omp 或控制面层面实现，策略做成可替换的 `policy.md`；
  - **只读的主动研究工具集**：后台扫描只能用技术上只读的工具。
- **冲突与代价**：
  - 审查 agent 会增加延迟和费用，只对 write 类动作启用；
  - Oryn 的 orb 本身是隔离沙箱，「orb 内的破坏」风险低于 ChatGPT，审批重点应放在**跨出沙箱**的动作（GitHub 写操作、通知外发、egress、创建线程）上；
  - 现有 `AutomationAction` 表可以扩展字段：authorization 来源（owner / standing / rule / auto-review）、审查结论和理由。

### P1：高价值能力

**4. 主动性：吸取 Pulse 的教训，不做独立 feed**
- 把「主动」落到三处：
  1. **监控任务**：跨运行状态 + 终止条件 + 「有值得报告的才通知」。Oryn 的 schedule 需要加 `notifyPolicy: always|on_change|never` 和上次运行的摘要状态。
  2. **事件触发**：Oryn 已有 GitHub 集成，第一批事件就做 PR / issue / CI 失败，用 Trigger / Condition / Prompt 三元组展示和审阅。
  3. **助手的后台只读巡检**：例如「昨晚 3 个线程卡在审批」「某 PR 的 CI 红了」，把结果写进私有笔记，在下次对话或推送时提出建议。
- 后台运行即使无产出也会耗额度，要在 UI 上提示（ChatGPT 帮助页明确写了这点）。
- **冲突与代价**：自托管单用户场景下，主动推送过多会让人厌烦。默认应该是摘要式推送（Telegram 或推送）。

**5. 事件订阅协议：采用 MCP Events 的形态**
- 订阅形态（`events/list|subscribe|unsubscribe`、HMAC 签名 webhook、challenge 校验、确定性订阅 ID、`refreshBefore` 续期）适合作为 orynd 对外的统一事件入口：GitHub、自定义 webhook、未来的 MCP server 都走一套机制。
- 控制面已经有 schedules 调度器，可以把事件当成另一种触发源，写进同一张表。
- **代价**：需要公网可达的回调地址（自托管常在 NAT 后面，可复用现有 egress 或隧道方案），以及签名密钥管理。

**6. 内嵌交互 UI：Oryn 作为 MCP Apps 宿主**
- **为什么**：Oryn 已有 Artifacts（页面 / 文档 / 幻灯片）。如果实现 MCP Apps 宿主（沙箱 iframe + `ui/*` JSON-RPC + CSP + 独立 origin），就能：
  - 让 omp 或 MCP 工具返回可交互组件，例如线程看板、PR diff 审阅卡、定时配置表单、审批卡；
  - 直接复用生态里现成的 MCP App（标准是开放的，Claude 等宿主也支持）。
- **直接采用的设计准则**：数据工具 / 渲染工具分离；业务状态归 server，UI 状态归 iframe；`_meta` 只给 UI，不进模型上下文，以节省 token；`ui/update-model-context` 把用户在 UI 里的选择回传给模型。
- **代价**：iframe 沙箱和独立 origin 在自托管单域名部署下需要子域或 `sandbox` 属性配合；移动端（Android / iOS）要用 WebView 实现同一套桥。
- 可以先用 `@mcp-ui/client` 或 ext-apps 的 AppBridge 验证。

**7. 多渠道统一身份：同一个助手，多个入口，可见性独立**
- Oryn 已有 Telegram 通知，可以升级为**双向渠道**。按 dots 的规则：各渠道共享同一套笔记和记忆，但对话彼此独立；「能使用」不等于「能向该渠道披露」。在群或频道里发言前要做披露检查，只有 owner 能驱动助手，别人 @ 它不会触发工作。
- **代价**：渠道消息属于不受信的输入，需要标注来源（复用 `[Oryn message …]` 头行）。

### P2：可选或长期

**8. 插件 = Skills + MCP + Hooks + UI，统一 marketplace JSON**
- omp 本身有 skills、extensions、MCP。可以把 Oryn 的「项目级 / 用户级能力包」对齐到 Agent Plugins 的 `plugin.json` + `skills/` + `mcp.json` 结构，marketplace 用 GitHub 仓库里的 JSON 目录。
- **[推断]** 与 Codex 的 `.codex-plugin` 兼容可以让用户复用现成插件。
- **代价**：格式仍在演进（OpenAI 自己就同时兼容两套清单），过早绑定有迁移成本。

**9. 从行为中沉淀 skill**
- Computer History 和 Codex Record & Replay（RN 2026-06-18）会建议「把重复工作变成 skill 或 automation」。
- Oryn 的对应做法：助手定期回看线程历史，发现重复的 prompt 模式，就建议生成 skill 或定时任务，由用户确认后落地。与 dsh-superagent 的「技能自进化」方向一致 **[推断]**。

**10. 研究模式：可编辑的计划**
- 借鉴 deep research 的「先出计划 → 用户编辑 → 运行中可插话 → 带引用报告」，产出落到 Artifacts。成本低，体验收益明确。

**11. 语音 / Intelligent UI / 商业支付**：对 coding 工作台的优先级低。
- Intelligent UI 依赖专门训练的模型，自托管多模型环境下质量难以保证；可以先做受控组件 DSL（JSON 组件树 + 客户端渲染），由模型选择使用。
- Wallet 的「替代凭证 + 生物识别逐笔批准」思路可以迁移到 Oryn 的 **secrets / egress 凭证**：agent 只拿到占位符，由控制面在出口处替换。这与 Codex Cloud 文档中「network secrets 传占位符、只对允许的 HTTPS 目标替换真实凭证」的设计相同（dots 安全文档）。

### 不建议照搬（OpenAI 已撤回或不适用）
- **独立的每日 Pulse feed**：已下线，见 §3.2。
- **Group chats 的多人 + AI 社交形态**：已停止新建；Oryn 的 Space 协作应聚焦工作产物，参照 Space / Pages 的协作方式。
- **单独的浏览器产品（Atlas）**：已并入桌面 app。Oryn 已有远程桌面和浏览器 Profile，继续沿用 orb 内浏览器即可。
- **Canvas 这类独立编辑面板**：已被内联的 writing blocks 和 code blocks 取代。Oryn 的 Artifacts 应保持内联优先、可全屏的模式。

---

## 附：未能确认 / 需谨慎的点
- 2025 年 Pulse 的话题排序、每张卡片的检索预算和模型均未公开。
- Memory 的具体注入格式和预算只有 2025 年的第三方逆向资料（Simon Willison / Embrace The Red）；2026 年 Dreaming V3 的内部数据结构没有公开。
- 「ChatGPT 内置视频生成」在 2025–2026 年的 RN 中没有找到对应条目；Sora 以独立 app 和积分形式存在。
- openai.com 的「Introducing ChatGPT agent」页面本次抓取返回 500；其工具清单来自 system card 页（<https://deploymentsafety.openai.com/chatgpt-agent>）和检索摘要。
