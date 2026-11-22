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

缺的，按价值排序：

1. **偏好文件**：仿 `PROACTIVE_PREFERENCES.md`，分 Tell / Never / When / How，放进主线近况里，主人与助手都能改。投入最小，效果最直接。
2. **投递节奏**：清醒时段、每日上限、紧急事项绕过。主线目前一说出口就可能推送，没有时段限制。
3. **心跳式主动检查**：默认关闭；开启后按间隔在主线静止时跑一轮，只带备忘和一份检查清单，默认 `stay_silent`。注意这会突破 M19 的 C1（「触发器只有普通 agent 的触发规则与定时」）。如果不想破约束，可以退一步，用主线上的一条普通提醒来实现。
4. **自我唤醒工具**：`wake_me(at|after, reason)`，即在主线上建一条一次性提醒（research-ChatGPT.md 已提出过）。
5. **安静的旁路界面**：低优先级产出不进主线、不推送。参照 Pulse 下线的教训，**不做独立的每日 feed**；如果要做，也应该像 Muse 的 Feed 那样完全不推送。
6. **只读的后台研究**：需要先有外部数据源（邮件、日历），而这与 M19 的 C2（不接外部渠道）冲突，排在最后。
