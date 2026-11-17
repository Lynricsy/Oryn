# M17 — 全局助手的主人记忆：实施计划

## 修订记录

- **v1（2026-11-17）**：初稿。设计与探查见 `docs/research/research-AssistantMemory.md`（下称「设计文档」）§1–§8；决定过程见 `.agent-logs` 第 0169–0196 条。

## Context

主人原话：「设计并总结下全局 agent 的记忆你打算怎么开发，我们先开始开发它的记忆系统吧。」

全局助手（协调档，`kind=assistant`）目前没有长期记忆：每个对话从零开始，只能用 `list_threads`、`read_thread` 临时查看工作线程。M16 给普通 agent 接入了 Hindsight 工程记忆（bank `oryn`），但明确把协调档排除在外（`internal/orbdrt/coordinator.go` 的覆盖层强制 `memory.backend: off`；`internal/orbdrt/agent.go:551` 拒绝协调宿主的 `memory` 参数）。

本里程碑给全局助手做「主人记忆」：关于主人是谁、在做什么、做了哪些决定、在意什么。写法参照 ChatGPT 的三条读取路径（设计文档 §1.2）：会话开始自动注入主题记忆与近况，需要时用工具检索。

### 确认后的决定

| 编号 | 决定 | 来源 |
|---|---|---|
| D1 | 独立 bank `oryn-owner`，与工程记忆 `oryn` 共用 M16 的 Hindsight 部署、模型中转与管理页 | 设计文档 §2 |
| D2 | Runner 线程纳入写入来源，检索工具可以只读访问；对 Runner 线程的操作仍然禁止 | 设计文档 §8 Q1 |
| D3 | 后台写入（助手对话、普通线程）自动进行，不逐条确认；靠出处、可查看、可遗忘兜底。助手自己发起且原话核验不通过的写入进待确认 | 设计文档 §8 Q2 |
| D4 | 主题：固定种子 `owner-profile` + 每日整理出的动态主题，上限 12 | 设计文档 §8 Q3 |
| D5 | 「最近在做什么」的时间窗 14 天 | 设计文档 §8 Q4 |
| D6 | 首次开启回填最近 **30 天**的对话与线程 | 2026-11-17 表单 |
| D7 | 「Ling」就是主人。管理页提供「主人的名字与别名」，写入时把这些名字统一归为「主人」 | 2026-11-17 表单 |
| D8 | 召回重排用 Google Decisions API（`gemini-decision-1`）。地址、模型和 key 在记忆页单独配置为「判断模型」接口，key 加密保存。不配时按 RRF 顺序给出，标注「未经重排」 | 2026-11-17 表单；设计文档 §7.2 |
| D9 | 注入内容每个对话存一份快照；agent 重新拉起时，快照生成超过 **1 小时**才重新生成 | 2026-11-17 表单 |

### 现状（已核对代码；行号只作提示，动手前按符号重读）

**M16 记忆（`internal/memory`）**

- 装配：`internal/server/server.go:277-294` 构造 `memory.New(Config{Q, Box, HTTP, Restart, Family, Upstream, PublicURL})`，`go s.Memory.RunTombstones(runCtx)`（`server.go:341`）。
- 客户端：`(*Service).Client(ctx) (*Hindsight, error)`；未配置返回 `ErrNotConfigured`。`hindsight.go` 只有通用的 `NewRequest`/`Do`/`call`（非导出）、`Version`、`Ready`、`BankStats(ctx, bank)`、`EnsureBank(ctx, bank)`（mission 写死为 `bankMission`）。响应体读取上限 `maxHindsightBody = 1<<20`。
- **bank 写死**：`service.go:25` `BankID = "oryn"`；`admin.go:38` `adminBank`、`gateway.go:43` `bankRoot` 都由它拼出。`adminClient`（`admin.go:70`）的全部方法（document 列表与删除、`retainGlobal`、mental model 列表/刷新/删除、`ensureUserPrefs`）和 `stale.go` 的 `scopeModels`、`refreshOpStatus` 都绑死 `adminBank`。recall 内联在 `(*Service).Search`（`admin.go:357-400`）。
- 遗忘保护：`memory_stale_models`（0038，`model_id` 主键）、`memory_stale_holds`（0039）；`markStale`、`releaseStale`、`refreshStale`、`RetryStaleModels`、`StaleModelIDs`。删除重试：`memory_tombstones`（`document_id` 主键），`deleteDocuments`、`RetryTombstones`、`RunTombstones`（每分钟）。**这些表都没有 bank 列**。
- 用量：`memory_usage(hour, purpose)`，中转路径里没有 bank 信息，不能按 bank 区分。Hindsight 自带 `GET /v1/default/banks/{id}/llm-requests/stats` 可以按 bank 取。
- 设置：`memory_settings` 单行；`(*Service).Enabled(ctx)`（总开关开且已配置；出错按未启用）。
- 中转取上游：`relayroute.Router.Upstream`（`internal/memory/relayroute/`）只暴露转发 handler，不暴露 origin 与 key。D8 的 key 单独配置，不走它。
- 测试：`memory_test.go` 的 `fakeHindsight`/`newEnv`，`admin_test.go` 的 `admFake`（bank 前缀写死 `oryn`），`relay_test.go` 的 `fakeUpstream`。
- 迁移：最新为 `0039_memory_stale_holds.sql`，下一个是 **0040**（动手时现取）。`internal/store/store_test.go:29-36` 有表名清单要同步。

**全局助手（`internal/coord`、`internal/threads`）**

- 系统提示词：`coord/profile.go:62` `SystemPrompt(space, projectName, threadID) string` 是纯函数；`(*Service).AgentProfile(ctx, threadID)`（`profile.go:83`）调用它并带上 `ToolDefs(space != nil)`。每次 `agent.start`（包括静止后重启）都经 `threads/agent.go` 的 `coordinatorProfile` 重新调用 `AgentProfile`。协调宿主把提示词写成 `<coordinatorDir>/prompts/<threadID>.md`，要求非空且含换行（`orbdrt/coordinator.go:65`）。
- `threads.(*Service).RefreshAgent(ctx, threadID)`（`threads/coordinator.go:185`）：静止后停掉 agent，下一条命令时用新 profile 拉起。
- 宿主工具：名字常量在 `profile.go:118` 起，定义在 `ToolDefs`（`profile.go:169`，仅助手可用的工具放在 `if inSpace { return defs }` 之后），分发在 `tools.go:34` `HostToolCall` 的 `switch`；仅助手可用的 case 要检查 `c.Kind != threads.KindAssistant`。不可信包裹 `wrapUntrusted(threadID, v)`（`coord/content.go:100`）。
- `resolveTarget`（`tools.go:127`）只允许 `kind=work` 且非 Runner（`t.Cwd != nil` 或 `orbs.IsRunner`）。`toolReadThread`（`tools.go:269`）只读最近 `tail` 条（默认 6、上限 30），不带 seq 与时间。
- `coord.Service`（`coord.go:90`）构造在 `coord.go:132`，装配在 `server.go:260` 附近。
- 线程：`db.Thread` 有 `Kind`（`work|space|assistant`）、`ProjectID`、`State`、`ArchivedAt`、`ShippedAt`、`LastMessageAt`、`TranscriptRev`、`Cwd`、`Title`。PR 在 `thread_pulls`。
- 转录：只有 `entries` 表（`thread_id, seq, file, entry_id, parent_id, type, ts, payload`）。主会话文件 `transcript.IsMainFile`；活动分支 `transcript.ActiveBranch`；`transcript.ParseMessage` 给出 `Role, Text, Thinking, ToolCalls`；工具结果是 `role=toolResult` 的独立条目。
- **没有字段区分主人亲手输入与自动消息**。自动消息都是 `role=user`，正文以 `[Oryn ` 开头（`coord/reports.go:26` `reportAnchor`、`schedule/loop.go:904`、`github/message.go:39`、`github/triggers.go:462`、`github/release.go:54`、`github/tasks.go:463`）。
- 事件：`threads.Observer`（`StateChanged`、`TurnEnded`、`EntriesAppended`），主人亲手发的回合不触发 `TurnEnded`；归档没有回调。
- 全文搜索：`search.(*Service).Search(ctx, Params{Q, Limit, Cursor})`，**不支持 kind、项目、时间过滤**；SQL 在 `internal/store/queries/search.sql:44-63`（`SearchMessages`）与 `:65` 起（`SearchMessagesShort`），已 JOIN `threads t`。
- 密钥扫描：`share.(*Service).ScanContent`（`internal/share/scan.go:183`）只报告命中，**不能替换**；规则表 `scanRules`、`vaultNeedles` 可复用。

**Hindsight 1.2.1（设计文档 §7 与本次核对）**

- bank 级配置 `PATCH /banks/{id}/config {"updates": {...}}`：`retain_mission`、`reflect_mission`、`enable_observations`、`enable_reranking`（bank 级，`oryn-owner` 关掉，由 orynd 重排）。
- retain item 字段：`content`、`timestamp`、`context`、`metadata`（`dict[str,str]`）、`document_id`、`entities`（`[{text, type?}]`）、`tags`。
- 单条事实失效：`PATCH /banks/{id}/memories/{memory_id} {"state": "invalidated"|"valid", "reason"}`。
- mental model：`POST /mental-models {id, name, source_query, tags, max_tokens(256–8192), trigger}`，`PATCH`、`/refresh`、`DELETE`。
- reflect 支持 `response_schema`（结构化输出）。
- recall 没有「创建时间之后」的过滤，只有 `created_before` 与时间窗加权。
- Decisions API：`POST {base}/v1beta/decisions`，头 `x-goog-api-key`，body `{model: "models/gemini-decision-1", input, questions}`；`score` 题返回期望分（设计文档 §7.2）。

### 需求（每条都能由第三方判断真假）

**A. 写入**

- **R1** 助手记忆开启后，一个助手对话在最后一条主人消息之后空闲 30 分钟、或被归档时，其内容写入 bank `oryn-owner`：document id 形如 `conv:<线程 id>:<块序号>`，标签含 `src:assistant`；每个 item 是「一条主人消息 + 它回应的那条助手消息」，metadata 含 `seqs` 与 `roles`。
- **R2** 普通线程（`kind=work`，含 Space 子线程与 Runner 线程）在 idle 且空闲 30 分钟、归档或标记交付后写入：document id 形如 `thread:<线程 id>:<块序号>`，标签含 `src:thread` 与 `project:<项目 id>`（有项目时）；item 按约 2,500 字分块，不拆开一个主人回合；块头含线程标题、项目名、状态、PR。Space 聊天线程（`kind=space`）按 R1 的规则写入，document 前缀 `conv:`。
- **R3** 写入的内容不含：工具调用与工具结果、thinking、超过 8 行的代码块（替换为「（代码 N 行，已省略）」）、`<system-reminder>` 等标签块。正文以 `[Oryn ` 开头的 user 消息记为「自动消息」，不当作主人的话。密钥扫描规则（`scanRules` 与密钥库值）命中的片段替换为 `〔已脱敏〕`。
- **R4** 同一来源再次写入时只重写内容变了的块（按块内容哈希判断）；块数变少时删除多出的 document。写入失败按退避重试，管理页能看到失败的来源与错误。
- **R5** 首次开启时回填最近 30 天内有活动的对话与线程（D6），管理页显示回填进度（已完成 / 总数）。
- **R6** 被排除的来源不写入，已写入的随之遗忘：单个助手对话设「不进助手记忆」；项目设「不进助手记忆」时，其线程都不作为来源；助手记忆总开关关闭时，写入、整理、注入、检索全部停止（已有数据保留）。
- **R7** bank 的 `retain_mission` 包含设计文档 §3.2 的要点、§4.1 的三条约束（归属、完成状态、上下文）、「指代词必须换成具体名称」（负例 5），以及主人的名字与别名（D7）；每个 item 带 `entities: [{text: "主人"}]`。主人改名字或别名后，bank 配置随之更新。

**B. 主题与注入**

- **R8** bank 里有 `owner-profile` 主题（固定种子）。每天一次「主题整理」：对 bank 做一次带 `response_schema` 的 reflect，得到主人当前的长期主题；新增主题建 mental model（id 形如 `owner-topic-<短 id>`），长期无新事实的归档；总数不超过 12。整理结果写入审计，管理页可见。
- **R9** 主题刷新：每天对每个活跃主题用它的 `source_query` 做一次 recall（不调 LLM），返回的事实中有上次刷新之后提到的（`mentioned_at`）才刷新；`owner-profile` 每周至少刷新一次；同一主题两次刷新间隔不少于 20 小时。
- **R10** 全局助手 `agent.start` 时，系统提示词末尾附加「记忆」段：
  - 块一「关于主人」：各主题 `content` 按固定顺序拼接，总长不超过 8,000 字；
  - 块二「最近 14 天」：由库生成，不经 Hindsight——主人的助手对话（标题、日期）与工作线程（标题、项目、状态、最后活动、是否交付、PR），按项目分组，不超过 30 行；
  - 外层说明使用设计文档 §4.1 的文字（含归属与「准备中」两句）。
  - Space 协调线程不注入。
- **R11** 注入内容按对话存快照（D9）：快照生成不足 1 小时时，重新拉起沿用快照；超过 1 小时时重新生成。助手记忆关闭时不附加记忆段。

**C. 检索工具（仅全局助手，Space 不可用）**

- **R12** `recall_memory(query)`：在 `oryn-owner` 召回（Hindsight 重排关闭）前 50 条，经判断模型打分重排（设计文档 §7.2 的省 token 版提示词），按分数分档：≥2.0 正常给出；1.5–2.0 标注「部分相关，可能是旧状态或属于别的对象」；<1.5 不给；最多 20 条；一条 ≥2.0 的都没有时，结果开头写明「记忆里没有能直接回答的条目」。判断模型未配置或调用失败时，按 RRF 顺序给出前 20 条并标注「未经重排」。结果用 `wrapUntrusted` 包裹。
- **R13** `recall_memory` 的每条结果带时间与出处。来源 item 只含主人消息时标「主人陈述」；含助手或 agent 消息时只标「出自 <对话|线程> <id> 第 a–b 条，角色未确认」；observation 沿 `source_fact_ids` 取来源事实的出处；无法回溯时标「来源未确认」。
- **R14** `search_history(query, kind?, projectId?, before?, limit?)`：全文搜索助手对话与工作线程原文，支持按种类、项目、时间过滤；结果带线程 id、标题、种类、seq、时间、片段。
- **R15** `read_thread` 新增 `aroundSeq`、`beforeSeq` 参数与 seq 字段；可以只读访问主人的其他助手对话与 Runner 线程。`send_message`、`archive_thread`、`set_schedule` 等写操作的目标限制不变。
- **R16** `recall_engineering(query, projectId?)`：只读召回工程记忆 bank `oryn`（给了项目时标签为 `[project:<P>, oryn:global]`，否则 `[oryn:global]`，`tags_match=any_strict`）。M16 未启用时返回 `memory_disabled`。
- **R17** `remember(content, ownerQuote?)`：`ownerQuote` 在本对话主人亲手输入的消息里逐字出现时，直接写入 document `explicit:<id>`（标签 `src:explicit`）；否则进入待确认，主人在管理页批准后写入。

**D. 管理（「更多 › Agent › 记忆」新增「助手记忆」分区）**

- **R18** 设置：助手记忆开关、主人的名字与别名、判断模型接口（地址、模型、key；key 只写不读）。M16 记忆未启用或未配置时分区只显示提示。
- **R19** 查看：各主题当前内容与最近刷新时间、最近一次主题整理的结果、「注入预览」（指定对话将得到的记忆段）、回填与写入状态、按 bank 统计的 24 小时 / 7 天 LLM 用量（来自 Hindsight `llm-requests/stats`）。
- **R20** 事实：可搜索浏览 world 事实与 observation，每条带出处；world 事实可以设为失效或恢复（R13 的出处同样显示）。失效后，受影响的主题在刷新完成前不进入注入（复用 M16 的遗忘保护）。
- **R21** 来源：列出已写入的对话与线程，可以按来源遗忘（删除其全部 document，受影响主题走遗忘保护）；可以设置对话与项目的排除（R6）。
- **R22** 明确记忆：列表、新建、编辑、删除；待确认项可以编辑后批准，或拒绝。

**E. 质量回归与文档**

- **R23** 仓库里有一组来自真实数据的回归用例（设计文档 §7.1 的 5 条负例、P8 的 10 个召回问题、§7.2 的 4 个陷阱问题），可以对真实 Hindsight 与判断模型运行，输出每题的结果与通过与否。默认测试不运行它。
- **R24** `README.md` 的功能列表、`docs/deploy.md` 的记忆一节说明助手记忆、判断模型接口与成本；设计文档标注已落地的部分。

### 非目标

- 普通 agent 读取主人记忆（方向单向：主人记忆可以摘取普通线程，普通线程读不到主人记忆）。
- 直接编辑主题内容（主题是生成物；纠正方式是让事实失效、遗忘来源或补一条明确记忆）。
- 把工程记忆的内容迁入主人记忆。
- 判断模型以外的重排供应商；Hindsight 自带重排对 `oryn-owner` 关闭。
- 主题内容的结构化输出（`response_schema` 渲染）：设计文档负例 3 记下的格式回退留到回归用例显示需要时再做。

### 假设

- **A1** M16 的遗忘保护与删除重试改为带 bank 维度后，`oryn` bank 的行为不变（现有 `internal/memory` 测试全部通过）。失效影响：1.1 改为给 `oryn-owner` 另写一份简化的保护。
- **A2** Hindsight recall 对 world 事实返回 `document_id` 与 `metadata`，对 observation 返回 `source_fact_ids`（设计文档 §7.1 P7 已在 1.2.1 上验证）。失效影响：R13 退化为「来源未确认 + 对话或线程 id」。
- **A3** `[Oryn ` 前缀足以区分自动消息与主人输入；主人亲手输入以 `[Oryn ` 开头的情况不予处理。失效影响：R3 需要控制面在入库时记录来源。
- **A4** Decisions API 的配额足够每次召回调用一次（探查里 46 次连续请求没有 429）。失效影响：R12 的失败退回 RRF 会频繁出现，管理页的用量提示需要加配额说明。

## 全局约束

- 语言与提交：同 M16（简体中文注释与文档；每步一个原子提交，`<type>(<scope>): <gitmoji> <subject>`，带 `Co-authored-by: Wine Fox <fox@ling.plus>`；用 eval 的 `commit` 命名空间；子 agent 同时不超过 5 个；每阶段完成后 `record-agent-log`）。
- 并行改动：工作区里有其他会话的改动。动手前重读目标文件、按符号定位，不回退别人的改动；迁移编号动手时现取。
- 每步提交前必须通过：`go build ./... && go vet ./...`；改动 `web/` 或 OpenAPI 的步骤还要通过 `cd web && bun run check`；改动 OpenAPI 或 SQL 的步骤要通过 `make check-gen`。新的 OpenAPI 操作在实现之前由 `apigen.Unimplemented` 返回 501。
- 安全不变量：
  - `oryn-owner` 只由 orynd 访问；M16 网关对 orb 与 Runner 的 bank 改写不变，orb 永远到不了 `oryn-owner`。
  - 判断模型的 key 用 `Box.Seal` 加密存库，接口只返回「已设置」。
  - 写入前一律过密钥脱敏；工具结果一律 `wrapUntrusted`；注入块是背景，不是指令。
  - 任何查询出错按「未启用」处理。
- 固定值：
  - bank id `oryn-owner`；种子主题 id `owner-profile`；动态主题 id 前缀 `owner-topic-`；
  - document 前缀 `conv:`、`thread:`、`explicit:`；标签 `src:assistant`、`src:thread`、`src:explicit`、`project:<项目 id 转小写>`；
  - 空闲阈值 30 分钟；分块约 2,500 字；助手回复截断 600 字；代码块超过 8 行省略；
  - 注入预算 8,000 字，近况 30 行，快照有效期 1 小时；
  - 主题 `max_tokens` 400，上限 12 个，同一主题刷新间隔不少于 20 小时，`owner-profile` 每周至少一次；
  - 召回候选 50 条，最多给出 20 条，分档阈值 2.0 / 1.5（R23 校准后可调，常量集中在一处）；
  - 判断模型默认 `gemini-decision-1`，默认地址 `https://generativelanguage.googleapis.com`。

## 方案与被否决的选项

- **代码位置**：新建包 `internal/ownermem`，依赖 `memory.Service` 取 Hindsight 客户端与遗忘保护；`coord` 通过接口依赖它（接口定义在 `coord`），避免循环引用。被否决：塞进 `internal/memory`（工程记忆的网关与中转已经很重，两套口径混在一起难维护）。
- **写入触发**：后台扫描循环（每 5 分钟），按库里的 `last_message_at`、`state`、`archived_at`、`shipped_at` 与写入进度表判断到期。被否决：订阅 `threads.Observer`（主人回合不触发 `TurnEnded`、归档没有回调；重启后还要补扫，最终仍需要扫描）。
- **分块文档**：每块一个 document，只重写变了的块。被否决：整个对话一个 document（`update_mode: replace` 每次都重新抽取全文，长对话的成本随长度线性增长）。
- **遗忘保护**：给 M16 的 `memory_stale_models`、`memory_tombstones` 加 `bank` 列（默认 `oryn`），`adminClient` 参数化 bank；主人记忆的模型 id 带 `owner-` 前缀，不与工程记忆冲突，主键不变、不用重建表。被否决：另写一套（v6/v7 的并发问题会再遇到一遍）。
- **用量**：读 Hindsight 的 `llm-requests/stats`。被否决：改中转路径带 bank（要动 `.env` 与已部署的 Hindsight）。
- **重排**：orynd 侧调用 Decisions API（Hindsight 1.2.1 不支持它，cross-encoder 接口也带不了自定义评分档位，设计文档 §7.2）。
- **注入位置**：在 `coord.(*Service).AgentProfile` 里拼接，`SystemPrompt` 保持纯函数。

**回滚点**：迁移只加表和列；助手记忆总开关关闭时，助手的系统提示词与工具和 M16 完全相同（新工具不出现在 `ToolDefs` 里）。`oryn-owner` 的数据在 Hindsight 里，不随 Oryn 回滚。

## 阶段 0：契约

### 步骤 0.1：计划入库

Files：`docs/plan-m17.md`。
Accept：`git log -1 --name-only` 包含 `docs/plan-m17.md`。

### 步骤 0.2：契约冻结

Depends on：0.1。
Files：

- 新建 `internal/store/migrations/0040_owner_memory.sql`（编号现取）：
  - `owner_memory_settings`：单行，`enabled`、`owner_names`（JSON 数组）、`decide_base_url`、`decide_model`、`decide_key_enc`（AAD `owner-memory/decide-key`）、`backfill_since`（回填进度按 `owner_memory_sources` 与 `threads` 现算，不存计数）、`config_hash`（已写入 bank 的配置哈希）、`last_curated_at`、`last_curation`（JSON）、`updated_at`；
  - `owner_memory_sources`：`thread_id` 主键、`kind`（`conv|thread`）、`transcript_rev`（已写入时的版本）、`chunks`、`retained_at`、`next_attempt_at`、`attempts`、`last_error`；
  - `owner_memory_chunks`：`(thread_id, idx)` 主键、`document_id`、`hash`、`seq_from`、`seq_to`、`retained_at`；
  - `owner_memory_exclusions`：`(kind thread|project, target_id)` 主键、`created_at`；
  - `owner_memory_topics`：`model_id` 主键、`name`、`scope`、`seed`、`status`（`active|archived`）、`last_refresh_at`、`last_hit_at`、`created_at`；
  - `owner_memory_explicit`：`id` 主键、`content`、`owner_quote`、`source_thread_id`、`status`（`pending|active`）、`created_at`、`updated_at`；
  - `owner_memory_snapshots`：`thread_id` 主键、`content`、`generated_at`；
  - `memory_stale_models`、`memory_tombstones` 各加 `bank TEXT NOT NULL DEFAULT 'oryn'`。
- 新建 `internal/store/queries/owner_memory.sql`；同步 `internal/store/store_test.go` 的表名清单。
- `schema/openapi.yaml` 新增 `/api/v1/owner-memory/*`：
  - `getOwnerMemorySettings` / `putOwnerMemorySettings`（`enabled`、`ownerNames`）；
  - `putOwnerMemoryDecideEndpoint` / `deleteOwnerMemoryDecideEndpoint`；
  - `getOwnerMemoryStatus`（bank 统计、回填进度、待重试来源数、最近整理、用量）；
  - `listOwnerMemoryTopics`、`refreshOwnerMemoryTopic`、`curateOwnerMemoryTopics`；
  - `getOwnerMemoryBrief`（`threadId?`，注入预览）；
  - `listOwnerMemoryFacts`（`q?`、`type?`、分页，带出处）、`setOwnerMemoryFactState`；
  - `listOwnerMemorySources`、`forgetOwnerMemorySource`；
  - `listOwnerMemoryExclusions`、`putOwnerMemoryExclusion`、`deleteOwnerMemoryExclusion`；
  - `listOwnerMemoryExplicit`、`createOwnerMemoryExplicit`、`updateOwnerMemoryExplicit`、`deleteOwnerMemoryExplicit`、`approveOwnerMemoryExplicit`。
- `make gen`；新操作接 `apigen.Unimplemented`。

Accept：`make check-gen && go build ./... && go vet ./...` 通过；`go test ./internal/store/...` 通过（含迁移 up / down）。

## 阶段 1：后端基础

### 步骤 1.1：M16 记忆参数化 bank

Depends on：0.2。
Files：`internal/memory/{admin,stale,purge,hindsight,service}.go` 及其测试。
Do：

1. `adminClient` 加 `bank` 字段，路径由 `bankPath(bank)` 拼出；现有调用一律传 `BankID`。
2. `EnsureBank(ctx, bank)` 只负责创建；新增 `PatchBankConfig(ctx, bank, updates map[string]any)`。
3. `markStale`、`releaseStale`、`refreshStale`、`RetryStaleModels`、`deleteDocuments`、`RetryTombstones` 带 bank；`StaleModelIDs(ctx)` 只返回 `oryn` 的（网关用），新增 `StaleModelIDsFor(ctx, bank)`。
4. 导出给 `ownermem` 用的窄接口：`Owner() *memory.BankOps`（retain、recall、list memories、get memory、set memory state、mental model CRUD 与刷新、reflect、llm-requests stats、按 bank 的遗忘保护与删除重试）。
5. `maxHindsightBody` 对 recall 与 memories list 放宽到 8 MiB。

Accept：`go test ./internal/memory/...` 通过（M16 原有用例不改断言）；新增用例覆盖 `oryn-owner` 的删除重试与遗忘保护不影响 `oryn`。

### 步骤 1.2：`ownermem` 骨架、设置与 bank 配置

Depends on：1.1。
Files：新建 `internal/ownermem/{service,settings,bankcfg}.go`；`internal/api/owner_memory.go`；`internal/server/server.go` 装配。
Do：`Service.Enabled(ctx)` = M16 已启用且已配置 && 助手记忆开关开（出错按未启用）。`ensureBank`：创建 `oryn-owner`，PATCH 配置（R7 的 mission、`enable_observations: true`、`enable_reranking: false`），配置哈希写入 `config_hash`，变化时重写；创建种子主题 `owner-profile`。实现 `get/putOwnerMemorySettings`、`put/deleteOwnerMemoryDecideEndpoint`、`getOwnerMemoryStatus`。
Accept：`go test ./internal/ownermem/... -run 'Settings|Bank|Status'` 通过：用 httptest 假 Hindsight 断言 mission 含主人别名与三条约束、改别名后重新 PATCH、M16 未启用时 `Enabled` 为假、decide key 不回显。

### 步骤 1.3：输入构造器

Depends on：0.2。可与 1.1、1.2 并行。
Files：新建 `internal/ownermem/build.go`、`build_test.go`；`internal/share/scan.go` 新增 `RedactText(ctx, text string) (string, int)`；`internal/transcript` 新增 `IsAutomatedUserText(text string) bool`。
Do：从活动分支构造「回合」：主人消息（逐字，去标签块）+ 它之后到下一条主人消息之前的最后一条助手文本（截断 600 字，代码块超过 8 行省略）；自动消息并入上下文、标注「自动消息」不作为主人回合；排除工具调用与结果、thinking、子 agent 文件。按 2,500 字切块（不拆回合）；每块渲染为 item（`content` 以「主人：」「助手：」/「agent：」「自动消息：」开头的行），metadata `seqs`、`roles`，`timestamp` 取块内首条时间，`entities: [{text: "主人"}]`；线程块头含标题、项目、状态、PR。全部文本先过 `RedactText`。
Accept：`go test ./internal/ownermem/... -run Build` 与 `go test ./internal/share/... -run Redact` 通过：用构造的 entries 覆盖 R3 每一条、回合不被拆开、块哈希稳定（追加消息只改变最后一块）、密钥与密钥库值被替换。

### 步骤 1.4：写入循环与回填

Depends on：1.2、1.3。
Files：新建 `internal/ownermem/writer.go`、`writer_test.go`；`server.go` 启动循环。
Do：每 5 分钟扫描：开启时首次设定 `backfill_since = now - 30d`；到期条件按 R1、R2；跳过排除项（R6）；对每个来源构造块、对比 `owner_memory_chunks` 的哈希，只 retain 变了的块（`async: true`，`update_mode: replace`），删除多余块的 document；失败退避（30 s 起，上限 6 h）。排除或关闭某对话时删除其 document（走 1.1 的遗忘保护）。
Accept：`go test ./internal/ownermem/... -run Writer` 通过：假 Hindsight 记录请求，覆盖空闲阈值、归档触发、只重写变化的块、块数变少时删除、排除即遗忘、失败重试、回填只取 30 天内。

## 阶段 2：主题

### 步骤 2.1：主题整理与刷新

Depends on：1.2。
Files：新建 `internal/ownermem/topics.go`、`topics_test.go`。
Do：每日一次（以及 `curateOwnerMemoryTopics` 手动触发）：reflect（`response_schema`：`[{name, scope, keep_existing_ids}]`）→ 与 `owner_memory_topics` 比对 → 新建 / 归档（上限 12，`owner-profile` 不归档）；每个主题的 `source_query` = 范围说明 + 设计文档 §4.1 的格式要求与三条约束；刷新按 R9；`listOwnerMemoryTopics`、`refreshOwnerMemoryTopic`。
Accept：`go test ./internal/ownermem/... -run Topic` 通过：覆盖新建、归档、上限 12、无新事实不刷新、20 小时间隔、`owner-profile` 每周刷新。

### 步骤 2.2：事实、来源与遗忘

Depends on：1.1、1.4。
Files：新建 `internal/ownermem/facts.go`；`internal/api/owner_memory.go`。
Do：`listOwnerMemoryFacts`（带 R13 的出处）、`setOwnerMemoryFactState`（失效前标记全部活跃主题为 stale，完成后刷新）、`listOwnerMemorySources`、`forgetOwnerMemorySource`、排除项接口、明确记忆接口（R22）。
Accept：`go test ./internal/ownermem/... ./internal/api/... -run 'OwnerMemory'` 通过：覆盖失效与恢复、按来源遗忘后主题在刷新完成前不可注入、明确记忆批准后写入 `explicit:`。

## 阶段 3：注入

### 步骤 3.1：记忆段与快照

Depends on：2.1。
Files：新建 `internal/ownermem/brief.go`、`brief_test.go`；改 `internal/coord/profile.go`（`AgentProfile` 在全局助手时追加记忆段）、`internal/coord/coord.go`（依赖接口 `OwnerMemory`）、`server.go`；`getOwnerMemoryBrief`。
Do：按 R10、R11 生成；stale 主题不进入；Hindsight 不可达时只给块二。
Accept：`go test ./internal/ownermem/... ./internal/coord/... -run 'Brief|Profile'` 通过：覆盖 8,000 字预算、30 行近况按项目分组、1 小时快照、关闭时提示词与改动前逐字相同、Space 线程不注入。

## 阶段 4：检索工具

### 步骤 4.1：判断模型重排

Depends on：1.2。
Files：新建 `internal/ownermem/decide.go`、`decide_test.go`。
Do：设计文档 §7.2 省 token 版提示词（常量）；一次请求打分全部候选；分档按 R12；失败退回 RRF。
Accept：`go test ./internal/ownermem/... -run Decide` 通过：假 Decisions 服务断言请求格式（`x-goog-api-key`、`models/gemini-decision-1`、每候选一个 `score` 题），覆盖分档、全部 <2.0 时的提示、超时与 429 退回 RRF。

### 步骤 4.2：工具

Depends on：4.1、2.2、3.1。
Files：`internal/coord/{profile,tools,content}.go`；`internal/search/search.go` 与 `internal/store/queries/search.sql`（`Params` 加 `Kinds`、`ProjectID`、`Before`）；`internal/ownermem/recall.go`。
Do：按 R12–R17 新增 `recall_memory`、`search_history`、`recall_engineering`、`remember`，扩展 `read_thread`（新校验 `resolveReadTarget`：允许 `assistant`、Runner，只读）；仅全局助手可用；系统提示词「How to work」补用法；助手记忆关闭时新工具不出现。
Accept：`go test ./internal/coord/... ./internal/search/... -run 'Recall|SearchHistory|ReadThread|Remember'` 通过：覆盖 Space 调用被拒、Runner 线程可读不可写、`ownerQuote` 核验、搜索过滤、结果被 `wrapUntrusted` 包裹。

## 阶段 5：Web 管理

### 步骤 5.1：「助手记忆」分区

Depends on：0.2（可先对 mock 开发）、阶段 2–4 的接口实现。
Files：`web/src/api/owner-memory-queries.ts`；`web/src/pages/settings/agent/memory/` 下新增 `OwnerSettingsCard`、`OwnerStatusCard`、`OwnerTopicsCard`、`OwnerFactsCard`、`OwnerSourcesCard`、`OwnerExplicitCard`、`OwnerBriefDialog`；`MemoryPage.tsx`；两个语言包；`web/src/mock/` 的对应 mock；助手对话菜单加「不进助手记忆」。
Accept：`cd web && bun run check && bun run test -- memory` 通过；新增测试覆盖开关、别名保存、decide key 只写、事实失效、来源遗忘、明确记忆批准。

## 阶段 6：回归与文档

### 步骤 6.1：真实数据回归用例

Depends on：4.2。
Files：`internal/ownermem/eval_test.go`（构建标签 `ownermem_eval`）+ `internal/ownermem/testdata/eval/cases.json`。放在 `ownermem` 包内而不是单独的 `eval/` 包：回归要走生产代码里未导出的重排与分档（`rerank`），不为测试新增导出接口。
Do：按 R23 收录用例；连接 `ORYN_OWNERMEM_EVAL_URL` / `ORYN_OWNERMEM_EVAL_KEY` 指向的 Hindsight 与 `ORYN_OWNERMEM_EVAL_DECIDE_KEY` 的判断模型；每题输出排名、分数、通过与否。
Accept：`go test -tags ownermem_eval ./internal/ownermem/ -run Eval -v` 在探查栈上运行，输出每题结果；不带标签时 `go test ./...` 不运行它。

### 步骤 6.2：文档与验收

Depends on：5.1、6.1。
Files：`README.md`、`docs/deploy.md`、设计文档。
Do：R24；在真实栈上开启助手记忆，跑完回填与一次主题整理，用新对话问设计文档 P6 的 7 个问题，记录结果。
Accept：验收记录写进 `.agent-logs`；6 道真实题答对，时区题答「不知道」。
