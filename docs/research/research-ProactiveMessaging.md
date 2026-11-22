# 调研报告：纯主线式个人助理的「主动发消息」是怎么实现的（Muse、ChatGPT dots、Hark Pro，附 OpenClaw）

> 调研日期：2026-11-22。来源分三档：**[官方]** 产品方文档或博客；**[媒体]** 第三方报道；**[非官方]** 社区抓取的运行时快照，内容未经验证。
> 已有的相关报告：[research-GrokMuse.md](research-GrokMuse.md)（Muse 的整体）、[research-ChatGPT.md](research-ChatGPT.md)（dots 与 Pulse 的整体）。本文只看「主动消息」这一条链路。

## 0. 结论

几家都没有「模型一直开着、自己想到就说」的机制。主动消息由三部分拼成：

1. **唤醒源**：让 agent 在没有用户消息时跑一轮。共有五类：用户或 agent 建的定时、agent 自己定的下次唤醒（dots 官方）、外部事件（Muse 快照明说事件型自动化也是轮询；其余几家没有披露）、固定间隔的「主动检查」心跳（OpenClaw 官方；Muse 快照里只有一个 `is_heartbeat` 字段），以及与主 agent 分开跑的后台研究（dots 官方）。
2. **说不说的闸门**：跑完一轮后由模型判断「值不值得打扰」，不值得就静默结束（Muse、OpenClaw 官方；dots 官方让用户在任务里写明哪些变化值得通知；Hark 未披露）。偏好写成独立文件（想听什么、别提什么、什么时段、什么形式），只见于 Muse 快照。
3. **投递策略**：几家都把说出口的内容落在主线里（Hark 另有 Home）。「只有用户不在前台才推送」「普通事项等清醒时段、最多每天一次，紧急事项立即发」都出自 **Muse 快照**，不是几家统一的做法。低优先级内容放到安静的旁路界面，见于 Muse（Feed，快照写明不推送、不进主线）和 Hark（Home 卡片，官方没说是否推送）。

后台研究的工具在技术上只读、不能直接发消息、只能把发现交给主 agent，这是 **dots 官方**披露的设计；其余几家没有公开同等的约束。

## 1. ChatGPT dots（OpenAI，2026-09）

产品名是 **dots**，每个用户的那个叫「your dot」。

**唤醒源 [官方]**（<https://learn.chatgpt.com/docs/dots/tasks-and-memory.md>、<https://help.openai.com/en/articles/20001530-getting-started-with-your-dot>）：

| 来源 | 说明 |
|---|---|
| 定时任务 | 让 dot「set a reminder or run a recurring check」。官方建议在建任务时写清：查什么、何时跑（含时区和结束日期）、**哪些变化值得通知**、结果投到哪里。Scheduled 页可以查看、暂停、改时间，并单独开关**完成通知** |
| 自我唤醒 | 「It can decide when to pause and wake up to continue, so you don't need to put every follow-up on a fixed schedule」 |
| 事件监控 | 只在连接的服务支持时可用（例如盯 Slack 频道里的新 bug 报告）；只连接服务不会自动建监控 |
| 主动研究 | 没有新请求时，后台 agent 读取有权限读的信息，写**私有笔记**。研究本身「doesn't send messages, change connected apps, or control a browser or computer」；后台 agent 把发现报给 dot，由 dot 决定要不要拿来提建议或提问。企业管理员不能单独关掉主动研究 |

**投递 [官方]**：

- 「Scheduled runs and proactive updates appear in the conversation」，也就是都落在主对话里。
- 短信渠道（beta，限美国、加拿大、墨西哥的部分 Pro 用户）：经第三方 Linq 走 iMessage 或 RCS，不支持普通 SMS；回复 STOP 即停。dot 还不能主动打电话。
- 用户可以在 profile 里**暂停** dot，暂停后不再做后台工作。但独立的定时任务和 Work、Codex 任务可能继续跑。

**前车之鉴 [官方]**：2025-09 上线的 Pulse（每晚异步研究、次日推送一组卡片）在 2026-06 宣布下线，主动更新并入定时任务，同期新增「只在有值得报告的内容时才通知」的监控任务。3 个月后，「无人要求时的主动性」改由 dots 的主动研究承担（详见 research-ChatGPT.md §3.2）。可以看出，独立的每日 feed 被放弃了，主动性改落在两处：用户看得见、能编辑的任务，以及带持续目标的常驻 agent。

## 2. Muse（Meta，2026-09）

**官方口径 [官方]**（<https://muse.ai/blog/how-we-designed-muse>）：

- 「Once you've set a goal or a task, Muse continues to work on a schedule and in response to relevant events … When it completes its background work, it evaluates whether the result is worth surfacing, and notifies you only when something is meaningfully new or needs your input.」
- 「The bar for sending one has to be high … you can always tell your Muse to turn it off, dial it down, or dial it up.」
- 主线不是一问一答式的。因为消息可能在用户没发言时到达，所以改用**气泡**来区分每一条独立的消息。
- 底层是每个用户一台 VM，算力要够跑并发子 agent 和 cron（<https://muse.ai/blog/security-and-safety-for-ai-agents-our-approach-with-muse>）。

**运行时快照 [非官方]**（<https://github.com/win4r/MuseAI-Skills>，Muse 内部代号 Hatch）。从中能看到四条互相独立的管线：

| 管线 | 触发 | 去向 | 打扰程度 |
|---|---|---|---|
| 定时与监视（`scheduler.jobs` / `scheduler.job_runs`） | cron；事件型自动化实际也是**轮询**，文档禁止承诺「一发生就通知」 | 主线消息；App 没开时推送 | 用户自己定 |
| 主动消息（proactive editions） | 系统管线，内容和时机都由它自己决定 | 推送加主线 | 每条只带一个事项并注明来源；普通事项等清醒时段和安静时刻，**最多每天一次**；时效性事项限额略宽；紧急事项尽快发 |
| 目标简报（goal briefings） | 后台进程，「The system controls the schedule」，不能按需触发 | Goals 页签，以信件形式附在目标上 | 可以按目标静音推送 |
| Feed | 系统排程，用户改不了频率；读得越多写得越多 | Feed 页签 | **不推送，也不进主线** |

关键细节，均出自快照里的 `home/hatch/docs/*.md`：

- **偏好文件**：`PROACTIVE_PREFERENCES.md`，分 Tell me about / Never tell me about / When / How 四节，模板默认 09:00–21:30。「Muse reads this whole file before composing a message」。用户说想少收某类消息时，Muse 应**改写这个文件**，而不是承诺管线会安静下来。
- **心跳**：`scheduler.jobs` 表里有 `is_heartbeat` 列。仅凭 schema 无法确认它的触发频率和提示词。
- **推送规则**：「A scheduled or proactive message sends a push only when no app is open」；审批立即推送。没有面向用户的免打扰设置，靠管线自己控制节奏。
- **分工**：「A specific promised reminder still belongs in a scheduled job you own. The proactive pipeline chooses its own content and timing, so never promise it will carry a particular item.」意思是，用户明确要求的提醒走定时，主动消息管线只承担锦上添花的部分。
- **判断「跑没跑」**：以 `cron.runs` 的运行历史为准，不看调度配置。
- 定时任务只通知用户本人；如果任务内容要给别人发消息，那是另一次连接器调用，默认需要审批。

## 3. Hark Pro（Hark，2026-10-06）

**[官方]**（<https://hark.com/articles/introducing-hark-pro>、<https://hark.com/using-hark>）：

- 「All handled in a single, persistent chat thread」，另有 Projects 用于专题。界面分三块：Home、Chat、Projects。
- 主动性被列为三大组件之一：电脑（Handoff）、记忆、**proactive thinking**。
- **Action Buttons**：「Notifications give you problems. Action Buttons give you solutions」。卡片放在 **Home**；根据邮件内容和对话记忆生成，点一下就能执行（Pay、Cancel）。官方没说这些卡片是否同时推送或进聊天。
- **节奏控制**：买完电影票后，它会主动提出订停车位，并在临近时再把入场二维码发过来；「Hark won't pile up all of this at once in your chat; it will pace these steps out so they arrive at just the right time」。
- 另有 Scheduled Tasks（用户口头创建）和 Specialties（自动记住重复流程）。
- 官方说追踪外卖等状态会有几分钟延迟，所以第三方推送可能先到。这句话分不出延迟来自轮询、上游 webhook 还是内部排队，不能据此推断实现方式。

**[媒体]**（<https://techcrunch.com/2026/10/06/hark-releases-an-ai-personal-assistant-with-a-focus-on-privacy/>）：演示中它主动问起待批报销、待回邮件、和记者的会面，还提醒订机票。

**没公开的部分**：扫描频率、判断阈值，以及一件事最终落到 Home 卡片、聊天消息还是推送的规则。

## 4. 对照：OpenClaw 的开源实现

TechCrunch 报道 Meta 承认 Muse 与 OpenClaw 的相似「不是巧合」。OpenClaw 的文档是这类机制里写得最具体的（<https://docs.openclaw.ai/gateway/heartbeat>）：

- 新安装会建一个**默认关闭**的「Proactive check」自动化任务。开启后每 30 分钟跑一次，跑在 agent 的**主对话**里，等 agent 空闲才执行，结果投到 owner 的私聊。
- 任务带一份私有的 **scratch** 清单，每次运行都带上；agent 运行时可以改自己的 scratch。
- 没什么可说时回复 `NO_REPLY`，或用 `record_result` 记下结论但不发消息（`no_change` 抑制投递）。
- 可选策略：`activeHours`（按时区限定执行窗口）、`idleOnly`（让前台工作优先）、`skipIfScratchEmpty`。
- 早期的 `HEARTBEAT.md` 加 `HEARTBEAT_OK` 已经退役，统一迁移成普通自动化任务。迁移后去掉了心跳专用的「重复内容抑制」。
- 后台命令完成、任务卡住之类的即时后续走普通会话执行，不依赖心跳。

## 5. 横向对比

| | 唤醒源 | 说不说的闸门 | 静默方式 | 投递与节奏 | 低优先级去向 |
|---|---|---|---|---|---|
| dots | 定时、自我唤醒、事件、只读的主动研究 | 用户在任务里写明「哪些值得通知」；研究结果交给 dot 判断 | 不发 | 主线；短信 beta；可暂停 | Activity 视图 |
| Muse | cron（含 heartbeat 标记）、轮询型事件、主动消息管线、目标简报 | 「evaluate whether worth surfacing」加 `PROACTIVE_PREFERENCES.md` | 不发 | App 不在前台才推送；最多每天一次，等清醒时段与安静时刻；紧急事项尽快 | Feed、Goals 简报、Ideas |
| Hark | 定时、邮件与记忆驱动的主动思考 | 未公开 | 未公开 | 分步按时机投递 | Home 的 Action Buttons 与 Panels |
| OpenClaw | 每 30 分钟的 Proactive check、定时、会话事件 | scratch 清单加模型判断 | `NO_REPLY` / `record_result` | `activeHours`、`idleOnly`、owner 私聊 | — |
| **Oryn 现状** | 交给助手的定时与触发规则、主线派出线程的回报、主线提醒（线程内定时） | 模型判断加 `mainGuide` | `stay_silent` | 不在前台才推送；15 秒合并窗口 | 无 |

## 6. 对 Oryn 的启示

Oryn 已经有的：唤醒源只有事件驱动和定时；说不说的闸门（`stay_silent`）；合并窗口；推送前判断是否在前台；主线分段加备忘（对应 OpenClaw 的 scratch、dots 的私有笔记）。

### 6.1 最有价值的是只读后台研究

其余各项都只是管道：偏好文件和投递节奏决定**过滤掉什么**，心跳和自我唤醒决定**何时醒来**。它们都不产生新内容，醒来时没有新信息，助手就无话可说。真正产生「主人没问、但想知道」的内容的，只有后台研究：把新出现的信息和已有的工作、决定、记忆联系起来，例如 dots 举的「发布决定与上周的草稿冲突」。主人在 M19 立项时举的例子也正是这一条：「自动读取邮件之后发现有什么东西要处理」。

初版把它排在最后，理由有两条，都不成立：

- **「与 C2 冲突」是误读。** C2（research-SuperAgent.md §3.0 第 2 条）禁止的是第三方**入口**，即经 Telegram、邮件、IM 和助手对话；它不禁止把邮件、日历当作**数据源**读取。
- **「要先接外部数据源」也不完全对。** 有三类数据源现在就能用：
  - **内部数据**，不需要任何连接器：线程转录与状态、GitHub 触发规则与 PR、CI、定时运行记录、工程记忆与主人记忆。对编码工作台来说，最常见的关联发现都在这里：PR 评论与另一线程里的决定矛盾、主分支 CI 挂了而正在跑的线程依赖它、两个线程在改同一处、某线程等输入已经几个小时。
  - **外部数据**：M19 已经能把全局 `mcp.json` 里的 HTTP / SSE MCP 服务器（含 Gmail、日历类）交给助手，鉴权头由 orynd 注入。
  - **公网**：跟进主人问过的问题。例如主人此前问过一项政策、一组测评，后台定期搜索，发现新的公开信息或此前回答有误时主动更正。这一类离不开搜索和读网页。

### 6.2 要做对的地方

研究 agent 同时接触私有数据和不可信内容（邮件正文、PR 评论、网页），所以只读和不外泄都必须由技术手段保证，不能只靠提示词（dots 官方的做法）：

1. **只读由凭据与白名单保证。** MCP 的 `annotations.readOnlyHint` 是服务器自报的提示（Oryn Core 已经读取它，见 `packages/coding-agent/src/mcp/tool-bridge.ts`），不能当作授权边界。可靠的做法是给研究用单独的只读凭据（例如 Gmail 的只读 OAuth scope），再加一份工具白名单。Oryn 自己的操作只能拿 `internal/coord/apipolicy.go` 的 `opRead` 分级**筛候选**，不能直接当授权：分级是按「是否需要确认」定的，不等于没有副作用，例如 `getAssistantMain` 归在 `opRead`，调用时却会 `EnsureMain` 建出主线。白名单里的每个操作都要逐个核对。
2. **能上网，但不让网页读到私有数据。** 一刀切禁掉 `web_search` 和读 URL，等于砍掉公网这一整类研究。要防的其实不是「读网页」，而是**攻击者看得到的出站请求里带上私有数据**：外泄要求私有数据和这样的出口出现在同一个上下文里。据此拆三层：
   - **拆上下文（双 LLM 模式）**：上网的研究员只拿到一条主题（例如「哈工大 浦江 2027 级博士补贴」），拿不到记忆、备忘、此前的回答和其他私有数据；它即使被网页注入、出口全开，手里也没有可泄露的东西。拥有私有上下文的研判方不联网，只读研究员交回的、按不可信数据包装的结果，再和此前的回答、记忆比对，决定要不要更正、要不要说。主题本身会发给搜索服务，所以哪些主题可以后台跟进，要做成主人看得见、能改的清单（相当于 `PROACTIVE_PREFERENCES.md` 的 Tell me about）。
   - **搜索查询要有外发授权**：查询词会披露给配置好的搜索服务商（grounded 搜索走 `/llm` 路由到模型服务商，其他搜索 API 也是固定服务商）。这不是「没人看得到」：即使查的是公开政策，查询本身也会暴露主人在关心什么。所以查询只能由**主人明确授权外发**的主题生成；「主人关心这件事」不等于「主人同意把它发给服务商」，清单里的每个主题都要单独标明可以外发。
   - **读 URL 只读「公开已知」的地址**：仿 OpenAI 的做法（<https://openai.com/index/ai-agent-link-safety/>，2026-01-28）：只自动抓取独立爬虫此前见过的**精确 URL**，对不上的就换来源或交给用户确认，重定向的每一跳都要重新检查。Oryn 没有自己的爬虫，只能退一步：只允许抓取**原样出现在搜索结果或已抓取页面里**的 URL，不允许模型自己拼出来的地址。这**不等价于**独立索引的验证：页面里的链接由页面作者决定，攻击者页面可以给出任意一组链接，让模型按私有数据挑着点。所以这条规则只适用于上面那种**手里没有私有上下文**的公网研究员，靠的是「没东西可漏」，URL 规则只是辅助；不能拿它当持有私有数据的上下文（包括今天全权限的主线与助手对话）的防外泄边界。这个检查必须在宿主侧做：现在的出站代理对 HTTPS 只看得到 CONNECT 的主机和端口，看不到路径和参数，所以读 URL 要改成 orynd 代为抓取的宿主工具，而不是放行沙箱里 omp 自带的读 URL。

   研究员仍然不给 `send_message`、`create_thread` 和任何写类工具。今天的全局助手同时拿着私有数据、读网页的能力和不受路径检查的出站，三要素已经齐了；上面的 URL 规则对它最多是降低风险，真正的边界需要独立索引式的验证、拆上下文，或者对读网页之后的出站要求确认（类似 Muse 的污点出网），属于另一项工作。
3. **发现只交给主线，交接后同样受限。** 研究结果走现成的 `assistant_inbox` → `flushInbox` → 主线 → `stay_silent` 闸门（新增一种 `kind`），由主线助手决定说不说；研究 agent 自己永远不发消息。但只限制研究 agent 不够：主线的工具表里有 `oryn` 和 `computer_*`（`internal/coord/profile.go:383-390`），助手的电脑是一台能联网的普通 orb。研究发现里夹带的邮件、PR 原文一旦进了主线，注入就能借主线的工具生效。所以**由研究发现触发的自动研判回合**也要由宿主限制工具：只留读类工具和 `stay_silent`，禁写、禁电脑、禁联网，**也不给 `update_main_notes`**（见下一条）。主人亲自回复之后再恢复完整能力。现有「交给助手」的运行结果也是不可信内容进主线，目前只靠 `wrapUntrusted` 与提示词规则，同样适用这条。
4. **不留跨回合的后门。** 只在那一轮禁写还不够，因为有两条路会把不可信内容带进以后拥有完整能力的回合：
   - **备忘**：`update_main_notes` 经 `PutNotes` 持久写库（`internal/coord/main.go:90-100`），`mainBrief` 再把备忘原文写进以后每一段的**系统提示词**（`:219-224`、`mainProfile` `:317-326`）。自动研判回合里写进备忘的注入，会在主人回复、能力恢复之后以系统提示词的身份生效。
   - **最近的对话**：近况里的「最近的对话」收录每个回合说出口的助手消息（`:225-230`），研究触发的回合说的话也在其中，同样进系统提示词。

   做法：研究笔记放在独立的数据区，每条带来源（数据源、条目 id、时间），不进备忘；研究触发的回合产出的主线消息，在近况里按不可信数据包装（`wrapUntrusted`）或干脆不收录，不能以系统提示词的身份进入完整能力的回合。现有「交给助手」的自动回合今天就能调用 `update_main_notes`，同样存在这个缺口。
5. **推送只带最小摘要。** 主线回合结束后，完成通知的正文是最后一条助手消息（`internal/notify/notify.go` 的 `emit`），会排给所有已启用的通知渠道与推送订阅（同文件 `:538-565`）。研究触发的回合，通知正文应改为宿主生成的最小摘要（来源类型、条数），不转发助手复述的不可信原文。
6. **私有笔记与游标。** 每个数据源记一个「上次看到哪里」的游标，只处理增量；值得记住但暂时不必说的内容写进第 4 条所说的独立研究笔记区（带来源、不进备忘），留给下次关联。
7. **先便宜地看一眼，再叫醒模型。** 先用确定性的检查判断有没有新东西，没有就不调用模型。定时已有的运行前闸门（`internal/schedule/gate.go`，提交 `8f5389e`）正是这个思路，可以直接复用。
8. **成本上限。** 后台研究会持续消耗 token，需要间隔、单次预算和每日上限，用量计入「自动化」统计。

### 6.3 其余各项（为后台研究服务）

1. **偏好文件**：仿 `PROACTIVE_PREFERENCES.md`，分 Tell / Never / When / How，放进主线近况里，主人与助手都能改。研究多起来以后，主线靠它决定说什么。
2. **投递节奏**：清醒时段、每日上限、紧急事项绕过。主线目前一说出口就可能推送，没有时段限制。
3. **心跳式主动检查**：默认关闭；开启后按间隔在主线静止时跑一轮，只带备忘和一份检查清单，默认 `stay_silent`。注意这会突破 M19 的 C1（「触发器只有普通 agent 的触发规则与定时」）。如果不想破约束，可以退一步，用主线上的一条普通提醒来实现。研究管线本身的唤醒也面临同样的 C1 取舍。
4. **自我唤醒工具**：`wake_me(at|after, reason)`，即在主线上建一条一次性提醒（research-ChatGPT.md 已提出过）。
5. **安静的旁路界面**：低优先级产出不进主线、不推送。参照 Pulse 下线的教训，**不做独立的每日 feed**；如果要做，也应该像 Muse 的 Feed 那样完全不推送。
