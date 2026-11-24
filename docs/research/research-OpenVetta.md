# Open Vetta 调研：哪些能力值得 Oryn 借鉴

> 调研日期：2026-11-24。源码基线：`openvetta/open-vetta` main 分支 commit `2506859`（2026-11-24，稀疏浅克隆 `packages/` 与 `docs/adr/` 到 `/tmp/ov` 只读分析）。文档来源：`https://docs.openvetta.com/llms-full.txt`（中文全量）。
> 标注约定：**[源码]** 读代码确认，**[文档]** 官方文档或 ADR 确认，**[Oryn]** 核对本仓库代码或文档确认，**[推断]** 是判断。

---

## ① 它是什么

- **定位**：Apache-2.0，本地优先的 Electron 桌面 Agent（另有 CLI、SDK、原生 iOS、Go IM 网关）。内核 fork 自 pi，与 Oryn Core（fork 自 oh-my-pi）同源。约 350 星。[文档] README
- **形态差异（决定能借什么）**：Vetta 的执行发生在用户电脑上，调度、IM、手机远控都依赖桌面进程存活；Oryn 是自托管控制面加每线程隔离 orb。凡是 Vetta「受限于桌面进程」的能力，Oryn 已经做得更彻底；值得借的，是那些和执行位置无关的**任务语义**。[推断]
- **工程风格**：158 份 ADR，每份写清备选方案与后果，可以直接当设计评审材料读。[文档] `docs/adr/`

## ② Oryn 已有、不必借的

| Vetta 能力 | Oryn 现状 | 结论 |
|---|---|---|
| 自动化（单次 / 每天 / 间隔，错过不补跑） | 定时任务支持 Cron、线程内与新线程两种模式、运行前闸门、`[SILENT]`、错过计数、重叠跳过（`internal/schedule/loop.go`）[Oryn] | Oryn 更强，且不依赖桌面进程 |
| 智能体团队（负责人 + 常驻成员） | Space 协调者 + 工作线程 + 回报登记（`internal/coord/reports.go`）[Oryn] | 机制等价，只借下文 §③.3 的恢复规则 |
| 选区「问问 AI」旁注 | 选区动作 + 解释卡 + btw 旁路提问 [Oryn] | 已有，§③.4 只补锚点 |
| 输入队列（排队 / steer / 暂停） | `QueueBar` 支持 steering 与 follow-up、编辑、排序、提前发送 [Oryn] | 已有 |
| 设计画布（`.vetd`，真实 React 路由） | Artifacts 画布、版本、评论、交给 Agent、演示 [Oryn] | 已有，只借设计品味规则（§③.6） |
| 浏览器操作（agent-browser + 人工登录） | 远程桌面接管 + M27 本机浏览器中继与非阻塞审批 [Oryn] | Oryn 更强 |
| 手机远控、文件预览、SSH 远程项目、MCP 表单 | 均已有 [Oryn] | 不借 |
| 目标模式（goal） | `/goal` 已接入 [Oryn] | 已有 |

## ③ 值得借鉴（按收益排序）

### 1. 批量任务：一份提示词扇出到 N 个目标，按产物验收 ⭐⭐⭐

**Vetta 怎么做** [文档] 批量任务页、[源码] `packages/capability-sdk/src/domain/batch-task.ts`
- 一份配置 = 提示词 + 模型 + 目录列表 + 并发上限 + 单任务硬超时 + 执行模式 + **产物校验 glob**。
- 每个目录生成一个独立子任务和会话；看板集中显示 pending / running / completed / failed / paused。
- **只有产物校验全部命中才算完成**：模型回复「做完了」但没写出 `release-readiness.md`，照样判失败。
- 「重置失败」只把失败项重新入队；推荐流程是先用 1 个目录、并发 1 跑通，再加目录、提并发。

**为什么适合 Oryn**
- Space 是「LLM 协调者拆解目标」，每一步都花协调者的 token，结果也不确定；批量是**确定性扇出**，不需要协调者参与。两者互补，不重复。[推断]
- Oryn 每线程独占一台 orb，天然满足批量任务的核心不变量（「任一子任务单独执行也能判定成败」），本地 Vetta 反而要靠共享工作区和沙箱来凑。[推断]
- 典型场景：同一条依赖升级打到 12 个仓库；同一份安全审计扫过所有项目；同一个 issue 模板复现 N 个分支。

**落地建议** [推断]
- 新对象 `batch`：`{prompt, role/model, targets: [{project, branch?}], concurrency, timeout, expect: [glob], gate?}`；子线程复用项目级定时的「新线程 + keepLast」路径与预热池。
- `expect` 由 orbd 在回合结束后对工作目录做 glob 检查，结果写进子线程的终态。同一机制可以直接给**项目级定时**加「产物校验」，解决 Vetta 文档列出的那类「显示成功但没有结果」。
- 看板复用收件箱分诊台的行组件；完成和全部完成两类事件走现有通知渠道。

### 2. 模型侧失败分类 + 「外部条件修好后自动唤醒」 ⭐⭐⭐

**Vetta 怎么做** [源码] `packages/agent-team/src/collaboration.ts` `classifyTeamExecutionIssue`
- 失败按可重试性分四类：`automatic`（网络、限流、上游不可用）/ `after-external-change`（鉴权失效、余额不足）/ `manual`（上下文超长）/ `never`（请求非法）。
- `after-external-change` 的任务进入 `awaiting-resource`，记下 provider 与 modelId；用户在设置里改好密钥后，宿主发 `TeamExternalConditionChange`，**只唤醒同一 provider 的受影响任务**重试。
- 配套 ADR-0057 / 0074：重试结束前不往消息流里发 error（一次 429 曾经刷出 6–7 个相同红块）；失败只持久化一份 `turn.failed`，附带 `retryAttempts`。

**Oryn 缺口** [Oryn]
- `internal/threads/errclass.go` 的 `classifyFailure` 只覆盖 orb / 镜像 / agent 启动类失败（`image_pull_denied`、`orb_connect_timeout` 等），**没有模型调用失败的分类**；回合内的 provider 错误只以 `errorMessage` 原文显示在 `MessageView.tsx`。
- 无人值守路径（定时、触发规则、Space 工作线程、主线）碰到 Key 过期或余额耗尽，现在只能等主人看到原文再手动重发。

**落地建议** [推断]
- 在 `ThreadErrorCause` 旁边加回合级的 `TurnFailureCause`（`rate_limit | auth | credit | context_overflow | provider_down | invalid | unknown`），由 orynd 从 omp 的 `stopReason=error` 消息和出站代理看到的上游状态码共同判定。出站代理握有真实 HTTP 状态，比 Vetta 只靠错误字符串的正则更准。
- 收件箱行按类别写人话（「Anthropic 密钥失效，修好后会自动重试 3 个线程」）。
- 密钥库写入 `secrets set` 时，按上游主机找出 `auth/credit` 类待恢复线程，重发最后一次投递；预算与次数沿用 §③.3。

### 3. Space 的恢复预算、回声切断与环形等待 ⭐⭐

**Vetta 的三条规则** [文档] ADR-0125、ADR-0141、智能体团队页
- **恢复预算**（ADR-0125）：每项任务持久化 `maxAutomaticRetries` / `automaticRetries`，定时重试、宿主中断恢复、模型请求恢复**共用一份预算**，按 revision 原子领取；耗尽进入 `attention-required`，不再无限重试。完成、失败、阻塞都会通知发起者，通知按任务 ID + revision 去重；每 30 秒本地巡检一次「标着 running 却没有执行者」的任务，巡检本身不调模型。
- **回声切断**（ADR-0141）：成员在回答问题或整合完成通知时，若再向触发者发 `question`，投递降级为 `inform`，不启动对方模型。理由：A 完成唤醒 B、B 再问 A，会形成无限异步回声；全局续跑上限会误伤合法长流程，按因果边切断更准。
- **环形等待拒绝**：A 在等 B 时，B 不能再等 A。

**Oryn 现状** [Oryn] `coord.go` 已经禁止协调对话互相操作（防成环）并有限速；`reports.go` 在 `TurnError` 时也会回报。需要核对的是：工作线程失败后协调者是否有**持久化的自动恢复预算**，以及「工作线程回报 → 协调者追问 → 工作线程再回报」会不会循环。建议先写一条 e2e 场景把这两种情况跑出来，再决定要不要加字段。[推断]

### 4. 旁注锚定到原消息 ⭐⭐

**Vetta** [文档] ADR-0132：旁注存在会话 sidecar `<session>.annotations.json`；**保存过旁注的消息在悬停时显示批注标记**，点开可以重看，也能在当前会话里搜索全部旁注。原消息不在当前分支时，仍可从列表读到快照。
**Oryn** [Oryn]：btw 记录由 Core 持久化（`RpcBtwHistoryRecord`），但转录里没有找到回到源消息的标记，只能从 btw 对话框按时间翻。
**建议**：解释卡发起时已经拿到来源（回复 / 工具卡 / 文件行号），把 `entryId` 存进 btw 记录，转录在对应消息上画一个小标记即可。改动小，回看体验提升明显。[推断]

### 5. 截图提问（App Snapshot） ⭐⭐

**Vetta** [文档] 应用快照页：按住左右同一修饰键约 0.25 秒，抓取**前台窗口**的截图、无障碍文字和正在编辑的文件路径，作为附件挂进输入框，不会自动发送。只抓一次，不常驻录屏。
**Oryn**：桌面端已有全局快捷键（`desktop.md`），没有「抓当前窗口」的入口。[Oryn]
**建议**：Tauri 端加「截取前台窗口到新线程 / 当前输入框」。macOS 用 ScreenCaptureKit 加 AX，Windows 用 UIA；Linux 在 X11 下可行，Wayland 只能走 portal 交互式截图，需要在设置页写明限制。属于中等成本的体验项。[推断]

### 6. 设计品味规则作为内置技能 ⭐

**Vetta** [文档] v0.5.66–0.5.69 发布说明：设计插件引入 [taste-skill](https://github.com/Leonxlnx/taste-skill)（MIT）的方向库、App 界面规则和 Unsplash 照片索引。新建设计时随机发三个方向，必须先选一个再开画；截图检查会点名「AI 味」痕迹（靛蓝紫、成排图标方块、小号大写标签、超过两种字体等）。
**建议**：Oryn 的 artifact 页面和画布同样面临「千篇一律」的问题。把 taste-skill 的规则打包成内置技能、随 artifact 工具一起下发，成本很低；许可要求保留 MIT 声明。[推断]

### 7. 用 Oryn 自己的统计数据检验 `edit` 失败率 ⭐（学习项）

**Vetta** [文档] ADR-0157：他们统计到逐行哈希锚点的 `edit` 失败率在 13.6%–32% 之间（malformed 与 stale 合计占失败的 76%），于是改成「首行 + 可选末行」的内容定位，并加了相对的结构回归检测（编辑后括号开合只许持平或变好）。文中特别提到 oh-my-pi 的 hashline 对 kimi / deepseek / qwen 会自动降级到 `replace` 模式。
**对 Oryn 的意义**：Oryn Core 用的正是 hashline。控制面的 `tool_calls` 表已经记录每次调用的 `tool` 与 `is_error` [Oryn] `internal/store/migrations/0001_init.sql`，但表里没有模型列。要按模型拆分，需要用 `thread_id` 加最近的 `entry_seq` 关联 `usage_records` 里的模型。本机开发库样本太小，不能下结论（`edit` 7 次失败 1 次，`write` 15 次失败 4 次）[Oryn] `.dev/data/oryn.db`，应该到生产库上跑同一条查询。只需要一条 SQL，就能判断是否要对部分模型默认切到 replace。[推断]

## ④ 不建议借鉴

| 能力 | 原因 |
|---|---|
| 插件系统（renderer 内执行） | Vetta 自己在 ADR-0023 / 0091 里承认插件跑在共享 renderer realm、不是沙箱。Oryn 的核心卖点是「凭据不进环境」，在控制面 Web UI 里跑第三方代码会直接击穿这条边界。扩展需求继续走技能、MCP 与 artifact `oryn.use()` 能力门 |
| IM 网关（飞书 / 微信 / Slack / Discord 等 8 种） | `plan-m19.md` 的约束 C2 已明确不接外部渠道；Telegram 目前只承担通知与作答。要不要反转这条决定由主人定；如果要做，Vetta 的 ADR-0004 / 0005 / 0009（IM 会话与桌面会话分家、按日滚动记忆）值得参考 |
| 知识库（后台 LLM 整理成 wiki） | Oryn 已有 Hindsight 记忆加文件库；再加一套「后台持续烧 token 整理」的管线，和现有的记忆整理重叠 |
| 桌宠、主题 SDK、Work / Coding 模式 | 与 Oryn 定位无关；Vetta 自己的模式硬闸也已被 ADR-0046 修订推翻 |
| 端到端 P2P 手机中继 | 适合「电脑即服务器」的模型；Oryn 有公网控制面，没有这个需求 |

## ⑤ 建议的落地顺序

1. **§③.2 失败分类 + 修好后唤醒**：所有无人值守路径都受益，并且是 §③.1 与 §③.3 的前置依赖。
2. **§③.1 批量任务 + 产物校验**：产物校验同时回馈项目级定时。
3. **§③.4 旁注锚点**、**§③.6 品味技能**：小改动，可以穿插进行。
4. **§③.3 Space 恢复**：先用 e2e 场景确认问题确实存在，再动手。
5. **§③.7**：随时查一次统计；**§③.5** 视桌面端排期再定。
