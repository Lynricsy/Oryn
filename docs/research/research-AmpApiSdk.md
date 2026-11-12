# Amp 机器可读接口调研报告(面向 Oryn 单用户 API 设计)

范围说明:
- 本报告只依据 2026-11-12 时点能读到的当前文档和公告。每条结论标 `[现行]`、`[已移除]` 或 `[已被取代→新形态]`,并附依据日期。
- 团队/多人相关能力只用一两行带过,标 `[团队功能·暂不做]`。
- 外部 API 的字段表取自 `https://ampcode.com/api/v2/openapi.json`(OpenAPI 3.0.0,18353 行)。`/api/external` 的 HTML 页面只读了 1–400 行,其余由 openapi.json 对应。该页面本身就是 openapi.json 渲染的。
- 没有亲自读到的写「未找到」。推测标 `[推测]`。

---

## 0. 大改版时间线与「不要复刻」清单

### 0.1 大改版时间线(来源 https://ampcode.com/chronicle 列表,以及下列公告正文)

| 日期 | 事件 | 改了什么 / 删了什么 |
|---|---|---|
| 2025-05-08 | No More BYOK | 删除 Isolated Mode/BYOK。后来以「Model Routing」形态回归,见 2026-09 条。 |
| 2025-10-23 | Handoff | 加入 handoff(替代手动压缩)。 |
| 2026-01-12 | TODOs Are Done | 删除 TODO 列表功能(https://ampcode.com/news/todos-are-done)。 |
| 2026-01-13 | Fork 被删 | 删除 Fork 命令,改用线程提及 `@@` 和 handoff(https://ampcode.com/news/stick-a-fork-in-it)。同日 "Handoff, Please" 让 agent 自己 handoff。 |
| 2026-01-15 | Tab, Tab, Dead | 删除 Amp Tab 补全,2026-01 底停服(https://ampcode.com/news/tab-tab-dead)。 |
| 2026-01-29 | Slashing Custom Commands | 删除自定义命令,改用 skills(https://ampcode.com/news/slashing-custom-commands)。 |
| 2026-02-19 | The Coding Agent Is Dead | 编辑器扩展(VS Code/Cursor)于 2026-03-05 20:00 PT 自毁,保留 CLI(https://ampcode.com/news/the-coding-agent-is-dead)。 |
| 2026-05-06 | **Neo 重写**(「Amp, Rebuilt」) | 新架构:可远程控制、compaction-first(上下文 90% 自动压缩)、插件驱动。排队/steer 成为默认。**默认不再询问工具权限**。详见 0.2。 |
| 2026-05-14 | npm 包改名 | `@sourcegraph/amp` 改为 `@ampcode/cli`。 |
| 2026-05-27 | Drop the Neo | Neo 对所有人开放并改回叫 Amp。`--take-me-back`(回旧版)被删。同日 Proof of Human(passkey 保护远程控制)。 |
| 2026-05-28 | Plugins, Everywhere | 插件 UI 上网页,与 TUI 同步。 |
| 2026-06-02 | End of Public Threads | 取消公开线程。 |
| 2026-06-04 | Agents, Everywhere | 在 web/CLI/mobile 观看并驱动 agent。 |
| 2026-06-16 | Diffs | 在 Amp 内审查并暂存变更。 |
| 2026-06-19 | Custom Agents | 插件可创建 agent。 |
| 2026-06-30 | Agents in Orbs | 远程 orb 里跑 agent。 |
| 2026-07-08 | Agents, Anywhere | 任何能跑 `amp` 的地方都能远程启动 agent。 |
| 2026-07-09 | **The Dial** | 四个新模式 `low/medium/high/ultra`。**smart/deep/rush/large 标记弃用**(https://ampcode.com/news/the-dial)。 |
| 2026-07-14 | Secrets of the Orb | orb 支持 OIDC 工作负载身份。 |
| 2026-07-17 | From Agent to Agent | agent 之间可建线程、发消息、交换文件。 |
| 2026-07-20 | Meet Puck;Slack 集成 | Puck 元 agent。Slack 集成属团队功能。 |
| 2026-07-21 | Right on Schedule | agent 自己定时唤醒。 |
| 2026-07-22 | Multiplayer | `[团队功能·暂不做]` |
| 2026-07-23 | Event Driven Orbs | orb 可接收请求和外部事件。 |
| 2026-08-04 | Attach Anything | 上传任意文件。 |
| 2026-08-06 | Portals into Orbs | portal。 |
| 2026-08-10 | A Dial for You | 链接 ChatGPT 订阅后,Dial 在订阅上跑。 |
| 2026-08-19 | MCP in Orbs | orb 内可用 MCP。 |
| 2026-08-25 | Orbs, Explained | 概念文。 |
| 2026-08-27 | So Long, TUI Sidebar | 删除 TUI 侧边栏。 |
| 2026-09-04 | Desktop | orb 桌面。 |
| 2026-09-08 | Steer, Don't Queue | 发消息默认 steer。注意:Neo 当时是「排队为默认」,此处推翻,见 0.2 冲突。 |
| 2026-09-10 | Customize Your Dial | 自选模式背后的模型,可放自有 agent。 |
| 2026-09-13 | Free Agent | 自带算力和模型订阅/密钥时免费。 |
| 2026-09-17/22/24 | One Runner Is Now Enough / Many Worktrees / Mac App Is Your Runner / Shared Runners | runner 可服务多目录,可建 worktree,Mac app 内置 runner。Shared Runners 属团队。 |
| 2026-10-06 | Many, Many Pucks | 多个独立 Puck 会话。 |
| 2026-10-12 | GitHub listeners | GitHub 事件订阅。 |
| 2026-10-13 | Take Puck with You | Puck 以 MCP 形式供其他 agent 用。 |
| 2026-10-16 | Runner Desktop | 看并控制 runner(macOS)桌面。 |
| 2026-10-22/28 | Self-Hosted Orbs;Personal Self-Hosted Orbs | 自有 orb provider(个人版无需管理员)。 |
| 2026-10-27/08-28 | Amp on Android / iOS & macOS | 移动和桌面客户端。 |
| 2026-10-29 | macOS Satellites;Visualize This | orb 起 Mac;线程内出图表和原型。 |
| 2026-11-02 | Artifacts | 线程附属文档。 |
| 2026-11-05 | GitLab | GitLab 集成。 |
| 2026-11-06 | Spaces;Close the Loop | Space;网站反馈部件。 |
| 2026-11-09 | Runner Portals | 本机 runner 也支持 portal。 |
| 2026-11-10 | Boxes | 常驻机器(Featured)。 |

### 0.2 Neo 重写(2026-05-06)详情(https://ampcode.com/news/neo)

新增:
- 远程控制:可从 ampcode.com 发消息、排队、出队、取消。
- 自动压缩:上下文 90% 时自动压缩。
- Plugin API 正式发布。
- 排队与 steer:steer 在下一次 tool result 上行时送达。`Esc Esc` 中断并立即发送。
- 性能:5000 条消息的线程,CPU 84.1% 降到 17.4%,空闲内存 1814 MB 降到 540 MB。

删除:
- handoff(被 compaction 取代)。
- 编辑/恢复消息时的文件回滚。
- skill 管理命令。
- 用户可调用 skill。
- 主题。
- 手动 bash(`$`/`$$`)。

权限:
- 默认不再询问权限。
- 旧权限系统降级为「内置插件」。只有设置里写了 `amp.permissions`、`amp.dangerouslyAllowAll:false` 或 `amp.guardedFiles.allowlist` 才加载。

2026-09 公告:`ask_user_choice` 已成内置工具。

**新旧冲突:**
- Neo 说「排队为默认,steer 需手动」。
- 2026-09-08 的 Steer, Don't Queue 说发送即 steer。streaming-json 输入里仍有 `steer?: boolean`(2026-10-23 更新),SDK `appendUserMessage({steer:true})` 也在。
- 以最新为准:有 steer 语义,且需要显式标记才「尽早送达」。

### 0.3 「老版功能,不要复刻」清单

| 老功能 | 状态与依据 |
|---|---|
| handoff 与 handoff 工具 | `[已移除]` 2026-05-06 Neo。**仅 plugin-api 的 `getBuiltinAgent` 注释还提到 smart/deep/rush 兼容,线程引用 `@@` 保留。** |
| Fork 线程 | `[已移除]` 2026-01-13 |
| TODO 列表 | `[已移除]` 2026-01-12 |
| Amp Tab 补全 | `[已移除]` 2026-01 |
| 编辑器扩展 | `[已移除]` 2026-03-05 |
| 自定义命令(`.agents/commands`) | `[已被取代→新形态]` skills,2026-01-29 |
| 用户可调用 skill、skill 管理子命令 | `[已移除]` 2026-05 |
| 主题、手动 bash | `[已移除]` 2026-05 |
| 编辑/恢复消息回滚文件 | `[已移除]` 2026-05 |
| TUI 侧边栏 | `[已移除]` 2026-08-27 |
| 旧模式 smart/deep/rush/large | `[已被取代→新形态]` low/medium/high/ultra,2026-07-09。`rush→low`,`smart/deep→medium`(plugin-api 注释)。 |
| 旧权限系统(`amp.permissions` 等) | `[已被取代→新形态]` 内置插件,默认全放行,2026-05-06 |
| 公开线程 | `[已移除]` 2026-06-02 |
| `--take-me-back`、旧 SDK 0.1.x | `[已移除]` 2026-05-27(SDK 文档仍给 `@ampcode/sdk@0.1.0-20260528…` 与 `amp-sdk==0.1.5` 作为「Neo 之前」的遗留安装) |
| `@sourcegraph/amp` 包名 | `[已被取代→新形态]` `@ampcode/cli` |
| Bitbucket Enterprise Librarian、`amp.bitbucketToken` | `[已移除]`(github 页 2026-11-09 明写) |
| 数值型 message id(旧 TUI 线程) | `[已被取代→新形态]` Neo thread-actor 线程使用稳定字符串 id(plugin-api 2026-11-04) |
| 截图与视频 | 本次读到的页面里,2025–2026-05 之前的截图均属旧 UI,不得作规格。 |

---

## 1. External API v2

来源:https://ampcode.com/api/external,等价于 https://ampcode.com/api/v2/openapi.json。状态:`[现行]`(页面渲染的是当日 spec)。

### 1.1 基本信息、认证、错误、限流

- 基地址:`https://ampcode.com/api/v2`。有必选自定义域的 workspace 用 `https://<workspace-domain>/api/v2`。
- 所有数据按凭据所属 workspace 作用域。
- 创建应用:`https://ampcode.com/workspace/applications`。目前只能建 M2M 应用,Sign in with Amp 应用未普遍开放。

认证方案(`components.securitySchemes`):

| 方案 | 说明 |
|---|---|
| `v2.OAuth2AppAuth`(默认) | M2M `clientCredentials`,token URL `https://auth.ampcode.com/oauth2/token`。scope 见下表。 |
| `v2.PersonalAccessToken` | HTTP Bearer。需 PAT scope(如 `amp.api:threads:view`、`amp.api:threads:create`)加所有者当前权限。只有声明了此方案的操作才接受 PAT。 |
| `v2.SignInWithAmpOAuth` | authorizationCode,authorize URL `https://auth.ampcode.com/oauth2/authorize`。授权请求必须带 `resource=https://ampcode.com/api/v2`。该 resource 作为 token audience。 |
| `v2.SignInWithAmpOIDC` | 仅用于 actor 身份端点。OIDC 发现地址 `https://auth.ampcode.com/.well-known/openid-configuration`。ID token 不能授权数据 scope。 |

M2M scope(13 项):

| scope | 含义 |
|---|---|
| `amp.api:workspace.threads.meta:view` | 看线程基础元数据 |
| `amp.api:workspace.threads.contents:view` | 看内容与高级字段 |
| `amp.api:workspace.threads:manage` | 管理线程 |
| `amp.api:workspace.analytics:view` | 分析 |
| `amp.api:workspace.entitlements:view` / `:manage` | 配额 |
| `amp.api:workspace.groups:view` / `:manage` | 分组 |
| `amp.api:workspace.members:view` | 成员 |
| `amp.api:workspace.model-provider-keys:view` / `:manage` | BYOK 密钥 |
| `amp.api:workspace.projects:view` | 项目 |
| `amp.git:read` | 读 Amp 托管仓库 |

Sign in with Amp 另有:`openid profile email offline_access amp.git:write amp.inference:run`。

- 错误格式:`{"error": "string"}`(`v2.ErrorResponse`)。
- 限流:
  - `POST /threads`:同一 token 所有者每分钟超过 100 个线程返回 429。
  - inference 三个端点:429 `Rate limited`。
  - 其余端点未写。
  - Retry-After 头只在 plugin webhook 里提到,External API 未找到。
- 分页:`limit`(默认 50,最大 100,messages/user-threads 限 1–100)加 `cursor`(最长 4096 字符),响应 `nextCursor`。
- 无 webhook:`"webhooks": {}`。无 SSE/WebSocket。**只能轮询**,线程元数据文档写明「`agentState` 可滞后约 10 秒」。
- **能创建线程**:`POST /threads`(只能 PAT,见下)。
- **不能**通过 External API 向既有线程发消息或订阅事件。该能力靠 SDK/CLI/插件(见第 3、4 节)。

### 1.2 端点清单(`paths`,行号为 openapi.json)

| 方法 路径 | 用途 | 认证 / 备注 |
|---|---|---|
| GET `/api/v2/actor` | 返回认证主体 `{actor:{type:"m2m"|"user", id, scopes[]}}` | 5302 |
| GET `/api/v2/threads` | 列出 workspace 线程 | M2M `threads.meta:view`;5475 |
| POST `/api/v2/threads` | 创建线程并发首条消息 | **仅 PAT `amp.api:threads:create`**;5854。线程归 token 所有者,在 orb 里以其身份和积分运行;返回时线程已被接受,agent 继续工作。 |
| GET `/api/v2/user/threads` | PAT 所有者自己创建的线程,按更新时间降序 | PAT `threads:view`;6118。不含已归档与空线程,不含他人分享的线程,是 offset 式分页。 |
| DELETE `/api/v2/threads/{threadID}` | 删除线程 | 6376 |
| GET `/api/v2/threads/{threadID}/metadata` | 单线程元数据(同列表对象) | 6487 |
| GET `/api/v2/threads/{threadID}/messages` | 消息(旧→新) | M2M `threads.contents:view` 或 PAT;6737 |
| GET `/api/v2/threads/{threadID}/usage` | 线程用量/成本 | 6917 |
| GET `/api/v2/workspace/members` | 成员 | `[团队功能·暂不做]` |
| GET `/api/v2/workspace/projects`、`/workspaces/{workspaceID}/projects` | 项目列表 | 7375 / 7617 |
| GET `/workspace/projects/{projectID}` 或 `@{namespace}/{name}` 下的 `repository/{commits/search, content/search, paths/search, diff, contents, contents/{path}}` | 仓库只读 API | 7889–11566;见 1.5 |
| `/workspace/groups`、`/{groupID}`、`/{groupID}/members` | 分组 CRUD | `[团队功能·暂不做]` |
| `/workspace/entitlements` 与 `/{id}` | 配额 CRUD | `[团队功能·暂不做]` |
| `/workspace/model-provider-keys` 与 `/{keyID}` | BYOK 密钥 CRUD | 见 1.6 |
| GET `/workspace/analytics/daily-usage` | 日用量分析 | 见第 2 节 |
| GET `/inference/models`、POST `/inference`、`/inference/embeddings`、`/inference/decisions` | 实验性推理(需 `amp.inference:run`,须联系 Amp) | 17038–18300 |

### 1.3 线程资源 `v2.Thread`(openapi.json:308)

| 字段 | 类型 | 说明 |
|---|---|---|
| `id`* | string | `T-…` |
| `title` | string | 需 `contents:view`(PAT 总有) |
| `createdAt` / `updatedAt` / `firstSyncedAt` | date-time | |
| `creatorUserID`* | string | |
| `agentState` | `{state, updatedAt}` | 首次汇报前为空;**可滞后约 10 秒**。 |
| `agentState.state` | `working` \| `idle` \| `awaiting_approval` \| `error` | `working`=在跑一轮;`idle`=跑完等下一条;`awaiting_approval`=等人批准工具调用;`error`=因错误停止(Amp 可能自行重试)。 |
| `hostname` | string | 本地 CLI 或 runner 的主机名 |
| `mainThreadID` | string | 子线程指向主线程 |
| `parentThreadID` | string | 创建它的线程(`create_thread`、Puck 或插件);父线程被删后清空 |
| `repositories[]` | `{url, dir?}` | `url` 为归一化仓库 URL;`dir` 为子目录。 |
| `labels[]` | string[] | 已规范化、字母序 |
| `subThreads[]` | `v2.Thread[]` | |

列表响应:`{threads[], nextCursor?}`。

`GET /threads` 过滤参数:

| 参数 | 说明 |
|---|---|
| `limit` / `cursor` | 分页 |
| `query` | 内容搜索(OAuth 需 `contents:view`) |
| `project` | 项目 UUID 或 `namespace/name`(大小写不敏感),找不到返回 400 |
| `after` / `before` | 首次同步时间窗口;**与 `orderBy=updatedAt` 互斥** |
| `updatedAfter` | 需 `orderBy=updatedAt` |
| `orderBy` | `firstSyncedAt`(默认,翻页稳定)\| `updatedAt`(按更新时间翻页时线程会移动,需按 `id` 去重) |
| `sort` | `ASC`(默认)\| `DESC` |
| `visibility` | `private` \| `group_shared` \| `workspace_shared` \| `public_unlisted` |
| `userEmail` / `userID` | 按创建者过滤 |

### 1.4 创建线程与消息(单用户最相关)

**`v2.CreateThreadRequest`**(`additionalProperties:false`):

| 字段 | 约束 |
|---|---|
| `project`* | UUID 或 `namespace/name` |
| `message`* | 1–1,000,000 字符 |
| `agentMode` | 1–128;缺省用默认模式 |
| `title` | 1–256;缺省由 Amp 自动生成 |
| `labels[]` | ≤20 个;小写,字母数字加连字符 |

响应 201:`{thread:{id, url, agentMode}}`。

**消息**:`GET /threads/{id}/messages` 返回 `{messages[], nextCursor?}`。
- 只有 `messageID`(稳定)、`messageVersion`(数值)、`createdAt` 有保证。
- **其余字段「不稳定,随时可变」**,建议扫描整条消息。
- 稳定的插件视图见第 4 节 `ThreadMessage`。

### 1.5 项目与仓库只读 API

`v2.WorkspaceProject`:

| 字段 | 说明 |
|---|---|
| `id` (uuid)、`name`、`namespace` | |
| `owner` | `{type:"workspace", id, name}` |
| `repository` | `{mode:"amp-hosted"|"github"|"mapped", url|null}`,`url` 无凭据 |

仓库端点全部基于不可变 commit 的游标分页,游标绑定项目、查询和 commit:
- `commits/search`:大小写不敏感,按历史顺序,每次最多扫描 2000 个 commit,支持 path 过滤。
- `content/search`:正则;每文件最多 50 个匹配、500 行;结果可能不完整,`incomplete` 始终为真。
- `paths/search`:区分大小写的 glob,`*` `?` 在一段内,`**` 跨段,无花括号与字符类;每请求最多扫描 20,000 条或 2,000,000 次比较,单路径超预算返回 422。
- `diff`:commit 对父,或对显式 base 的 merge-base。超过 500KB、超过 2000 变更行、二进制和 lockfile 无内联 patch,看 `filteredFiles`/`incomplete`。
- `contents[/{path}]`:读文件(base64,≤1 MiB)或列目录(稳定分页,`recursive=true` 列子树)。

### 1.6 BYOK 密钥(`v2.WorkspaceModelProviderKey`,oneOf 按 provider)

| 字段 | 说明 |
|---|---|
| `provider` | 如 `meta`、`anthropic`、`openai` 等 |
| `id` (uuid)、`name` (1–256,可重复) | |
| `redactedKey` | 形如 `sk-ant-api••••1234`,**密钥明文从不返回** |
| `active` | 可多个同时激活,由模型映射与连接优先级决定用谁 |
| `lastUsedAt` | 约 30 秒粒度 |
| `createdAt` / `updatedAt` | |
| OpenAI 附加 `openAIRegion` | `global`(默认)\| `us \| eu \| au \| ca \| jp \| in \| sg \| kr \| gb \| ae`。非 global 仅 Enterprise。 |

要点:
- 创建默认 inactive(除非 `active:true`)。
- 密钥不能原地更新。轮换方式是新建一个 active 的再删旧的。
- 删除后密钥立即销毁。

这是 Oryn 密钥保管(只返回脱敏、不可回读、轮换靠新建)的直接参照。

### 1.7 实验性推理端点

**`v2.InferenceModel`**(GET `/inference/models`):

| 字段 | 说明 |
|---|---|
| `id` | 传给对应端点的 `model` |
| `kind` | `text` \| `embedding` \| `decision` |
| `displayName`、`providerName` | |
| `contextWindow` | |
| `maxOutputTokens` | `null` 表示非文本模型 |
| `dimensions` | 仅 embedding |
| `capabilities` | `{vision, reasoning, reasoningEfforts[]}` |

**`POST /inference`**(无工具、不流式):

| 字段 | 约束 |
|---|---|
| `model`* | ≤200 |
| `instructions` | ≤200,000 |
| `input[]`* | `{role:"user"|"assistant", content}`,1–200 条,每条 ≤1,000,000 |
| `maxOutputTokens` | 16–64000,默认 4000 |
| `reasoningEffort` | |
| `responseFormat` | `{type:"text"}` 或 `{type:"json_schema", name, schema}`(OpenAI strict 形式) |

`/inference/decisions`:同插件 `amp.ai.decide`,见第 4 节。

---

## 2. 用量与统计(Oryn「统计与分析」直接参照)

### 2.1 线程用量 `GET /threads/{id}/usage` → `v2.ThreadCostResponse`
来源 openapi.json:741。`[现行]`

保留期:Enterprise workspace 线程创建后 **180 天内**可查,其他 workspace **90 天内**可查。

| 字段 | 类型 / 说明 |
|---|---|
| `threadID` | |
| `subThreadIDs[]` | **恒为空**,仅兼容保留;用量只覆盖本线程。 |
| `usage` | USD。线程整个生命周期总成本,含 orb 与 tool 用量。计入任何积分(含免费/促销/订阅),**不含 free tier**。 |
| `orbUsage` | USD,已包含在 `usage` 内,**不含**在 `models` 内 |
| `toolUsage` | USD,同上 |
| `estimatedProviderCostAtListPriceUSD` | **实验性**:BYOK 推理按提供商公开标价的估算。不由 Amp 计费,不含在 `usage`。路由器连接(如 OpenRouter)记 0,订阅连接排除,2026-08-26 之前的请求不含。 |
| `orbs[]` (`v2.OrbUsage`) | 按 orb 规格聚合,用量降序 |
| `tools[]` (`v2.ToolUsage`) | 按工具聚合,用量降序 |
| `models[]` | 按 provider/model 聚合 |

`v2.OrbUsage`:

| 字段 | 说明 |
|---|---|
| `size` | 如 `a1.medium`,或 null(CPU/内存不匹配任何现行规格时) |
| `cpuCount` | vCPU,可 null |
| `memoryMB` | 可 null |
| `runtimeMs` | 计费运行毫秒数,与由哪种积分支付无关。个人订阅积分支付的那部分计入 `runtimeMs` 但不计入 `usage`。 |
| `usage` | USD,口径同外层 |

`v2.ToolUsage`:

| 字段 | 说明 |
|---|---|
| `tool` | 如 `web_search`、`read_web_page` |
| `calls` | 计费调用数,含 0 成本的 |
| `usage` | USD |

`models[]` 每项:

| 字段 |
|---|
| `provider`、`model` |
| `requests` |
| `inputTokens`(未缓存输入) |
| `outputTokens` |
| `cacheReadInputTokens` |
| `cacheCreationInputTokens` |
| `usage`(USD) |
| `estimatedProviderCostAtListPriceUSD` |

### 2.2 日用量 `GET /workspace/analytics/daily-usage`
scope `amp.api:workspace.analytics:view`。`[现行]`

参数:

| 参数 | 说明 |
|---|---|
| `endDate` | `YYYY-MM-DD`,默认今天 |
| `lookbackDays` | 1–365,默认 1 |
| `userID` | 只含该用户 |

响应:`{ metadata, data[] }`。

`metadata`(`v2.DailyUsageMetadata`):

| 字段 | 说明 |
|---|---|
| `startDate`、`endDate` | |
| `activeUsers` | 区间内用量非零的用户数 |
| `totalUsers` | 区间内涉及的不同用户数(含已离开者) |
| `totalUsage` | USD,含 orb 与 tool |
| `totalOrbUsage`、`totalToolUsage` | USD |

`data[]` 每天:`{date, users[]}`。每个用户条目(`v2.DailyUsageUserEntry`):

| 字段 | 说明 |
|---|---|
| `user` | `{id, email?}`,email 需 `members:view` scope |
| `metrics` | `{linesAdded, linesDeleted, linesModified, usage, orbUsage, toolUsage, estimatedProviderCostAtListPriceUSD}`。`usage` 仅计「归属 workspace 且用付费积分」的部分,不含免费/促销/订阅/free tier。 |
| `models` | `Record<"provider/model", {requests, inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens, totalTokens, usage, estimatedProviderCostAtListPriceUSD}>` |
| `orbs[]` | 同 `OrbUsage`,按规格,降序 |
| `tools[]` | 同 `ToolUsage` |

### 2.3 聚合维度小结

| 维度 | 是否有 | 说明 |
|---|---|---|
| 按线程 | 有 | `/threads/{id}/usage` |
| 按用户 × 日 | 有 | daily-usage |
| 按日期 | 有 | daily-usage 窗口 ≤365 天 |
| 按模型(provider/model) | 有 | 线程和日用量都有 |
| 按 orb 规格 | 有 | `size/cpuCount/memoryMB/runtimeMs/usage` |
| 按工具 | 有 | `tool/calls/usage` |
| 按项目 | **无专用端点** | 只能先 `GET /threads?project=` 再逐线程拉 usage |
| 代码行指标 | 有 | `linesAdded/Deleted/Modified`,仅日用量,按用户 |
| 保留期 | 线程用量 90 天/180 天 | daily-usage 窗口 ≤365 天,保留期未写 |

「Explain Usage」(2026-08-21):可问 Puck 钱花到哪了。依据 chronicle 条目,正文未读。

> Oryn 启示见第 9 节。

---

## 3. SDK 与 streaming-json

### 3.1 入口与选项(TS https://ampcode.com/docs/markdown/sdk/typescript,lastModified 2026-11-04;Python 同,lastModified 2026-09-18)`[现行]`

- 包名:`@ampcode/sdk`(npm),`amp-sdk`(pip)。底层调用 `amp` CLI。
  - 环境变量:`AMP_API_KEY`(形如 `sgamp_…`)、`AMP_CLI_PATH`。
  - TS 要求 Node ≥18,CLI 版本不得低于 SDK 固定版本。
- 函数:

| 函数 | 说明 |
|---|---|
| `execute({prompt, options?, signal?})` → `AsyncIterable<StreamMessage>` | `prompt` 可为 string 或 `AsyncIterable<UserInputMessage>` |
| `createUserMessage(text, {requestId?})` | `requestId` 为 1–256 字符的幂等键:重试时复用同一个,不会重复创建用户消息。 |
| `threads.new({visibility?})` → threadId | |
| `threads.markdown({threadId})` | 线程渲染成 markdown |
| `threads.setMultiplayer({threadId, enabled?, minutes/hours/days/weeks})` | 总时长 5 分钟–7 天,默认 1 周。`[团队功能·暂不做]` |

- Python:`execute(prompt, AmpOptions)`、`create_user_message(text, request_id)`。

**`AmpOptions`** 全字段:

| 字段 | 值 / 默认 |
|---|---|
| `cwd` | 仅本地 CLI 子进程目录;**不是**远程 runner 目录。 |
| `mode` | 默认 `medium`;`low/medium/high/ultra`,或插件自定义模式字符串。 |
| `effort` | `none \| minimal \| low \| medium \| high \| xhigh \| max` |
| `noArchiveAfterExecute` | 默认 false(execute 线程默认执行后归档) |
| `visibility` | `private \| unlisted \| workspace \| group`,默认 `workspace` |
| `settingsFile`、`logLevel`(`debug/info/warn/error/audit`)、`logFile` | |
| `mcpConfig` | JSON 字符串或对象 |
| `env`、`skills`(目录)、`enabledTools`(对应 `amp.tools.enable`)、`labels`、`thinking`(流里带 thinking 块)、`title`(≤256,仅新线程) | |
| `continue` | `true`=最近线程,或线程 ID 字符串 |
| **`executor`** | `local`(默认) \| `orb` \| `runner` |
| `runnerId`、`runnerDir` | 需 `executor:'runner'`;`runnerDir` 为 runner 所服务的某一个目录的绝对路径,缺省为 runner 启动目录;**一个线程一个工作目录** |
| `project` | 需 `executor:'orb'`;缺省则由 `cwd` 下的 git remote 推断,无匹配则不带仓库启动 orb |

远程 executor 的限制:
- `prompt` 只能是字符串,不能是流式输入。
- `enabledTools/skills/mcpConfig` 等本地选项会被忽略并告警,须在项目或 runner 上配置。
- 插件 agent 模式须已加载在 runner 上。
- 续接线程时忽略 executor 选项,沿用原目录。

SDK 概览页(https://ampcode.com/docs/markdown/sdk,lastModified 2026-09-18):
- 没有 `space`、`attach`、`stream` 选项。Space/attach 在 SDK 里**未找到**,流式靠 `execute` 的 AsyncIterable 输入。
- 概览页只读到 1–620 行,其余(约 540 行)未读;Python 页只读到 334 行,TS 页读全。

### 3.2 streaming-json(https://ampcode.com/docs/markdown/cli/streaming-json,lastModified 2026-10-23,读 1–380 全文)`[现行]`

CLI 用法:`amp --execute "prompt" --stream-json`,可加 `--stream-json-thinking`、`--stream-json-input`。
- `--stream-json` 要求 `--execute`。`--stream-json-input` 要求 `--stream-json`。
- thinking 块扩展与 Claude Code 不兼容。输出尽量兼容 Claude Code 格式。
- 每行一个 JSON。首条 `system/init`,最后一条 `result`。**`result` 是唯一的运行结束信号。**

输出消息联合类型:

| `type` / `subtype` | 字段 |
|---|---|
| `assistant` | `message:{type:"message", role:"assistant", content[], stop_reason, usage?}`、`parent_tool_use_id`、`session_id` |
| `assistant.content[]` | `text{text}` \| `tool_use{id,name,input}` \| `thinking{thinking}` \| `redacted_thinking{data}` |
| `stop_reason` | `end_turn \| max_tokens \| stop_sequence \| tool_use \| pause_turn \| refusal \| null` |
| `usage` | `input_tokens`、`cache_creation_input_tokens?`、`cache_read_input_tokens?`、`cache_creation?{ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}`、`output_tokens`、`max_tokens?`、`service_tier?: standard|enterprise` |
| `user` | `message:{role:"user", content:[text | tool_result{tool_use_id, content:string, is_error}]}`、`parent_tool_use_id`、`subagent_tool_use_id?`(报告一个后台子 agent 结束的 user 消息上,为启动它的 tool_use 的 id)、`session_id` |
| `system/init` | `cwd`、`session_id`(即 `T-…` 线程 ID)、`tools[]`、`mcp_servers[{name,status}]`、`agent_mode?`、`subagents[{tool_use_id, description}]`(进程挂接线程时仍在跑的后台子 agent) |
| `mcp_servers[].status` | `awaiting-approval \| authenticating \| connecting \| reconnecting \| connected \| denied \| failed \| blocked-by-registry` |
| `system/subagent_finished` | `tool_use_id`、`status: completed|failed|cancelled`、`session_id`。后台子 agent 是 `Task` 调用且 `mode:"async"`。 |
| `result/success` | `duration_ms`、`duration_api_ms?`、`is_error:false`、`num_turns`、`result`、`session_id`、`usage?`、`permission_denials?: string[]` |
| `result/error_during_execution` 或 `error_max_turns` | 同上但 `is_error:true` 且有 `error` |
| `system/error_*` | `error`、`session_id` |

注意:
- 示例里 tool_use 的 `name` 是小写 `read`,而 init 的 `tools` 里是 `Read`/`Grep`/`Bash`,大小写存在不一致。
- SDK 的 TS 类型里 `AssistantMessage` 多了 `id/model/stop_sequence`。

**输入消息**(`--stream-json-input`,stdin 一行一个):

```ts
{ type:'user', steer?: boolean,
  message:{ role:'user', content: Array<
    {type:'text', text} |
    {type:'image', source_path?, source:{type:'base64', media_type:'image/jpeg'|'image/png'|'image/gif'|'image/webp', data}} > } }
```

行为:
- `source_path` 可省,省略时 Amp 生成。声明的 `media_type` 必须与解码后字节一致。
- 输入里的图片块不会回显到 stdout 的 user 消息。
- Amp 在助手空闲且 stdin 关闭后才退出,并**等待后台子 agent 结束**。
- 助手忙时来的消息会排队,其 `user` 消息在开始处理时才出现在 stdout。
- `steer:true`:忙时在下一个打断点处理。
- SDK 的 `requestId`(见 3.1)在原生 streaming-json 页面**未找到**,只在 SDK 类型里。

---

## 4. Plugin API(https://ampcode.com/docs/markdown/plugin-api,lastModified 未显示,全文 1–3300 读完)`[现行]`

运行时:Bun;插件放 `.amp/plugins/`(项目)或 `~/.config/amp/plugins/`(系统);长生命周期进程,可同时服务多个线程。导出 `default function(amp: PluginAPI)`,可选 `export const description`(静态字符串,≤300 字符)。

### 4.1 `PluginAPI` 顶层

| 成员 | 说明 |
|---|---|
| `logger.log` | |
| `system` | `open(url)`、`workspaceRoot: URI|null`、`cacheDirectory`、`ampURL`、`user: User|null`、`executor: {kind:'local'|'remote'|'unknown', keepAlive()}`。`keepAlive()` 租约保持 orb 清醒,会消耗 orb 运行积分,不在 orb 内拒绝。 |
| `configuration` | Observable;`get()`、`update(partial, target?: 'workspace'|'global')`、`delete(key,target?)` |
| `$` | 标签模板 shell,返回 `{exitCode, stdout, stderr}`(非零不抛) |
| `helpers` | `shellCommandFromToolCall`、`toolCallsInMessages`、`filesModifiedByToolCall`、`filePathFromURI`、`isPluginUINotAvailableError` |
| `ui` | `PluginUI`,见 4.4 |
| `on(event, handler)` | 事件,见 4.2 |
| `registerCommand(id, {title, category?, description?, availability?}, handler)` | 返回的订阅带 `setAvailability({type:'enabled'} | {type:'disabled', reason} | {type:'hidden'})` |
| `registerTool(def)` | 见 4.3 |
| `registerSkill({path, listed?})` | 路径相对插件目录或位于 `cacheDirectory` 内;技能被命名为 `<plugin>:<skill>`;frontmatter 可含 `builtin-tools` |
| `registerLinkPattern({id, pattern, href})` | 转录文本里把如 `ENG-\d{1,6}` 的 token 变成链接。`pattern` 带 `u` 标志,≤200 字符,不许反向引用和后行断言,不许匹配空串;`href` 模板支持 `{{match}}` 与命名分组;`id` ≤64 字符。 |
| `onDispose(cb)` | 卸载和重载时约 3 秒预算;崩溃或 SIGKILL 不运行 |
| `createWebhook({key, headers?, handler})` | 见 4.6 |
| `ai` | 见 4.5 |
| `attachments.upload({data: Uint8Array|string(base64), mimeType})` → `{url}` | 图片 ≤4.9MB、≤8000px |
| `createAgent(config)`、`getBuiltinAgent(mode)`、`registerAgentMode(def)` | 见 4.7 |
| `activeThread` | Observable,`{id}|null`,有 `.current` |
| `threads.get(threadID)` | `PluginThread` |
| `experimental` | `createStatusItem(initial?)` → `StatusItem{update({text, url?})}`(`url` 可用 `command:<id>`),另有 createAgent 等别名 |

### 4.2 事件(`PluginEventMap` / `PluginRequestResultMap`)

| 事件 | payload | 返回 |
|---|---|---|
| `session.start` | `{thread:{id}}` | void |
| `tool.call` | `{toolUseID, tool, input, thread}` | `{action:'allow'}` \| `{action:'reject-and-continue', message}` \| `{action:'modify', input}` \| `{action:'synthesize', result:{output, exitCode?}}` \| `{action:'error', message}`(后者会停掉线程 worker) |
| `tool.result` | `{toolUseID, tool, input, status:'done'|'error'|'cancelled', error?, output?, thread}` | `{status, output?/error?}` \| void(可改写结果) |
| `agent.start` | `{thread, message, id, changesWorkflow?: 'ship'|'push_branch'}` | `{message?:{content, display?}}` |
| `agent.end` | `{thread, message, id, status:'done'|'error'|'cancelled', messages: ThreadMessage[]}` | `{action:'continue', userMessage, maxContinuations?}` \| void。`maxContinuations` 默认 5;用户发任一消息即重置计数。 |
| `changes.prompt` | `{thread, workflow:'ship'|'push_branch'}` | `{append?: string}`。所有插件的 append 按加载顺序拼接,Ship 对话框会显示拼好的 prompt。 |

事件上下文 `PluginEventContext`:`tools.call(name, input)`(不经模型直接调工具,不写入转录、不自动重试;不支持子 agent 工具与 durable wait)、`logger`、`$`、`ui`、`ai`、`system`、`span?`、`thread: PluginThread`。

多个插件监听同一事件时,执行顺序**未定义**。

### 4.3 工具注册 `PluginToolDefinition`

| 字段 | 说明 |
|---|---|
| `name` | `^[a-zA-Z0-9_-]+$` |
| `defer` | `false` 则直接暴露给模型,不走 `tool_search`/`code_exec`;默认 true。 |
| `title` | 客户端显示名(不发给 LLM) |
| `transcriptGroup` | `{active, complete}`:相邻调用合并成一行的标签(不发给 LLM) |
| `description` | |
| `inputSchema` | JSON Schema,`type:'object'` |
| `execute(input, ctx)` | `ctx = {tools, ui, logger, thread, signal}`;`signal` 在用户点 Stop 时中止。返回 `string` 或内容块数组(`{type:'text',text}` \| `{type:'image', mimeType, data}` \| `{type:'image', mimeType, url}`);返回空会显示为 `(no tool output)`;抛错即工具错误。 |

MCP 工具不是逐个发给模型,而是通过 `tool_search` + `code_exec` 作为延迟加载的代码模式模块访问。

### 4.4 UI 与「对话框多端镜像」所需数据结构

`PluginUI` 方法:

| 方法 | 返回 |
|---|---|
| `notify(message)` | void |
| `input(PluginInputOptions)` | `string|undefined`(取消) |
| `confirm(PluginConfirmOptions)` | `boolean` |
| `confirm(PluginEditableConfirmOptions)`(带 `fields`) | `Record<string,string>|undefined` |
| `select(PluginSelectOptions)` | `string|undefined` |
| `perform(UIAction)` | void。让线程的网页观看者执行 UI 动作。只到达线程所有者**已打开的浏览器标签**,不排队给以后才打开的人;不抢焦点,手机上不打开面板;无当前线程时抛错。 |

**所有对话框共用 `PluginDialogOptions`**(这是镜像语义的核心):
- `requireHuman`:要求人而非 agent 回答。所有者可答;多人模式下贡献者也能答交互选择/确认/非密输入。**`secret` 输入和带 secret 字段的确认仅所有者**,且总是要求人。针对特定连接的对话框不共享给贡献者。
- `skipIfUnwatched`:没有任何能回答的人打开了该线程(如定时或 Slack 启动、无人观看)时,对话框立刻按「取消」(`undefined`)返回,而不是等待并把线程标为「需要操作」。终端 app 里用户始终在场,总是显示。
- 无 UI 客户端(如 `amp -x`)或从无法等人的 `code_exec`(如子 agent 内)调用时抛「UI 不可用」错误,可用 `isPluginUINotAvailableError` 判别;**被取消或关闭从不抛错**,只返回 `undefined/false`。

`PluginInputOptions`:`title?`、`helpText?`、`initialValue?`(与 `secret` 互斥)、`submitButtonText?`(默认 "Submit")、`placeholder?`、`secret?`、`fieldName?`(用作 input 的 `name/id/placeholder/data-secret-name`,供 1Password/Bitwarden 识别)。

`secret` 输入的值**只返回给请求方**,不回显在已解决请求状态、线程历史和模型转录中。

`PluginConfirmOptions`:`title`、`message?`(Markdown)、`confirmButtonText?`(默认 "Yes")、`expandButtonText?`(对话框先折叠,点此展开字段和按钮)、`collapsedMessage?`(折叠时显示的 Markdown,仅配合 `expandButtonText`)、`iconURL?`(小方图)、`endTurnOnDecline?`(在工具 `execute` 内拒绝时,等同批所有调用完成后结束本轮;不要用 `ctx.thread.cancel()`,它会停掉同轮其他调用;hook 与命令中忽略)。

`PluginConfirmField`:

| 字段 | 说明 |
|---|---|
| `type` | `text`(默认) \| `image` \| `link` \| `file` \| `attachment` \| `checkbox` |
| `name`、`label`、`value` | |
| `multiline`、`inlineLabel`、`placeholder`、`optional`、`collapsed`、`readOnly`、`removable`、`secret` | |
| `accept[]` | file 字段的扩展名建议 |
| `mimeType`、`sizeBytes` | attachment 字段 |

字段语义:
- 文本字段默认必填。
- image/link/attachment 默认不出现在返回值里(除非 `removable`;`removable` 时保留则返回 `value`,移除则返回空串)。
- file 字段返回文本,≤64 KiB 且必须是 UTF-8。
- checkbox 返回 `'true'|'false'`,客户端无法显示时为 `'false'`,只应把 `'true'` 当成用户选择。
- `readOnly` 不出现在返回值里,也不阻塞确认。
- secret 字段单行且 `value` 必须为空,否则请求失败。
- link 字段排在可编辑字段前时,客户端把字段显示为带编号的步骤;若唯一字段是 link,必须先打开才能确认,主按钮先打开链接再变成确认按钮。

`PluginSelectOptions`:`title`、`message?`、`options: string[]`、`initialValue?`、`allowOther?`(末尾追加一个 "Other" 行内文本框,非空值 trim 后按普通选项返回)。

`UIAction` / `QuickAction`:
- `UIPane = 'changes'|'files'|'terminal'|'desktop'|'portal'`。
- `UIActionArgs`:`open-pane{pane}`、`open-portal{url}`(在 Portal 面板中打开 orb 内服务的 http(s) URL)。
- `QuickActionArgs` 额外:`send-message{message ≤10,000}`、`open-url{url}`、`copy{text ≤100,000}`、`run-command{command}`(插件已注册命令的 ID,需已连接的 executor)。
- 图标:`activity archive bug check copy external-link eye flask-conical git-pull-request message-square play refresh-cw rocket send sparkles terminal`。
- 按钮文字 1–40 字符。
- 每线程最多 5 个快捷操作(跨所有插件,最老优先)。
- 通过 `ctx.thread.createQuickAction(def)` 创建,返回 `{update(def), unsubscribe()}`。
- 在不能显示该面板的线程里(如 orb 外的 `desktop`)会隐藏该动作。

### 4.5 `ctx.thread` / `amp.threads.get(id)` — `PluginThread`

| 成员 | 说明 |
|---|---|
| `id` | `T-…` |
| `agent()` | 当前线程用的 Agent,可用于派生相关线程 |
| `parentThreadID()` | 仅支持当前线程 |
| `title` | Observable<string|null> + `get()` |
| `state` | Observable<ThreadState> + `get()`。**`ThreadState = 'idle'|'running'|'awaiting-approval'|'error'`**。与 External API 的 `agentState`(`working/awaiting_approval`)命名不同。有待重试的失败推理报 `running`。 |
| `waitForResponse({timeoutMs?})` | 默认 10 分钟;线程进入 `error` 则拒绝 |
| `cancel()` | 在 `agent.start` 期间调用可阻止本轮开始 |
| `setVisibility('private'|'workspace')`、`setMultiplayer({ttlSeconds|null})` | `[团队功能·暂不做]`(多人窗口 5 分钟–7 天) |
| `labels()`、`setLabels`、`addLabels` | 小写、去重;字母数字加连字符,首字符为字母数字,≤32 字符,≤20 个 |
| `messages({full?, from?: 'start'|'end', offset?, limit?, roles?})` | 默认 `{from:'end', limit:10}`,`limit` 最大 20;默认读「新一轮推理会看到的消息」(压缩后为最新摘要加切点后的消息),`full:true` 读全部 |
| `append(UserMessage[])`、`appendUserMessage(msg, {steer?})` | `UserMessage={type:'user-message', content:string}`;`steer` 在忙时优先出队 |
| `createQuickAction(def)` | 见 4.4 |

**`ThreadMessage`(插件看到的稳定视图)**:

| 类型 | 字段 |
|---|---|
| `ThreadUserMessage` | `role:'user'`、`id: number|string`、`createdAt?`、`content: (text | tool_result{toolUseID, output?: PluginToolResult, status:'done'|'error'|'cancelled'|'running'|'pending'})[]` |
| `ThreadAssistantMessage` | `role:'assistant'`、`id`、`createdAt?`、`stopReason?: 'end_turn'|'tool_use'|'cancelled'`、`usage?:{model?, inputTokens, outputTokens, cacheCreationInputTokens|null, cacheReadInputTokens|null}`、`content: (text | thinking{thinking} | tool_use{id,name,input})[]` |
| `ThreadInfoMessage` | `role:'info'`、`id`、`createdAt?`、`content: text[]` |

`ThreadMessageID`:旧 TUI 线程是数值,Neo thread-actor 线程是稳定字符串 id。

### 4.6 Webhook(`createWebhook`)

Orb 内持久的通用 webhook:
- 项目线程按「同用户 + 插件 + key」共享一个注册。无项目的线程每线程各一个。重复注册返回同一个 capability URL,**URL 即凭据**。
- 至少一次投递,用 `event.id` 做幂等键。处理器 30 秒超时(`ctx.signal` 中止)并重试:失败 5 秒后指数退避,封顶 5 分钟,**失败满 1 小时后丢弃事件**,后续事件可在前序事件等待重试时运行。
- 对未归档所有者的请求会唤醒或恢复 orb。归档所属线程使 URL 不可用,并暂停投递直到恢复。
- 限流:每个 webhook 突发 10 个新事件,每分钟补充 10 个。限流返回 429 + `Retry-After`;与仍待投递事件的 `Idempotency-Key` 匹配的重试不消耗配额。

`CreateWebhookOptions`:`key`(1–128)、`headers?`(要保留的头名,小写投递;默认不保留任何头)、`handler(event, ctx)`。

`WebhookEvent`:`id`、`body: Uint8Array`(原始字节)、`headers`(小写)、`receivedAt`(ISO8601);`payload` 与 `metadata` 已弃用。

`WebhookHandlerContext` = 事件上下文基类 + `thread` + `signal`。返回 `{url}`。

### 4.7 AI 与 agent

**`amp.ai`**:

| 方法 | 说明 |
|---|---|
| `generate({prompt, schema?, threadID?, model?, reasoningEffort?, system?, maxTokens?})` | 给 schema 返回对象,否则返回字符串 |
| `ask(question, opts)` | `{result:'yes'|'no'|'uncertain', probability, reason}` |
| `decide({state, questions, model?, threadID?})` | 见下 |
| `choice` / `noul` / `score` | 单问题简写 |

调用都经当前线程路由和计费;在线程绑定之外需传 `threadID`。`model` 格式是 `provider/model`,provider 取自 `alibaba amp anthropic baseten deepseek fireworks-ai google-vertex meta minimax moonshotai nvidia ollama-cloud openai thinkingmachines xai zhipuai`。

**`decide`** 数据模型(https://ampcode.com/docs/markdown/customize/decision-models,lastModified 2026-10-30):

| 项 | 内容 |
|---|---|
| 请求 | `DecisionRequest{threadID?, model?, state, questions: Record<name, DecisionQuestion>}`,`state` 是 string 或 JSON,`questions` 1–20 个 |
| 问题类型 | `noul{instructions, criteria?:{true?,false?}}`、`score{instructions, criteria: 2–10 级,低到高}`、`choice{instructions, criteria: Record<option, desc|null>, 2–20 选项}` |
| 回答 | `NoulAnswer{noul}`(0–1);`ScoreAnswer{score, legend, probabilities, confidence}`;`ChoiceAnswer{choice, probabilities, confidence}` |
| 结果 | `DecisionResult{model: 实际版本如 jev-1.13.0, answers}` |
| 模型 | `DecisionModel = 'typesafe/jev'`(默认,`jev-latest`,$0.042/百万输入 token)\| `'openai/gpt-6-luna'` |

决策模型要点:
- 两个模型校准不同,阈值不互通。
- 指定的模型即回答者,**绝不回退到另一个**:未配置或被 workspace 策略拒绝则调用拒绝,且不向任何一方发送 state。
- OpenAI 侧:JSON 以文本发送,`noul` 的 true/false criteria 会被追加进 instructions,OpenAI 拒答任一题则整个调用拒绝。
- 限制:`state` ≤50,000 字符;`instructions` 与每条 criteria ≤10,000;问题名 ≤100 字符。
- 没有 workspace 级或用户级的全局决策模型选择,选择写在插件请求里。
- Amp 自己用 Jev 排 diff 文件顺序和建议标签。

**`createAgent(config)`**(`CreateAgentConfig`):

| 字段 | 说明 |
|---|---|
| `owner` | `{type:'user'}` \| `{type:'service-account', userID}` |
| `name` | 注入 "You are <name>, a custom agent running in Amp." |
| `extends` | `low/medium/high/ultra`(及已弃用 smart/deep/rush) |
| `model` | `provider/model` |
| `instructions` | |
| `omitDefaultSystemPrompt` | |
| `tools` | `'all'` \| 名称数组(通配 `*`) \| `{include?, add?, exclude?}` |
| `reasoningEffort` | `none … max` |
| `compactionThresholdTokens` | 默认按 90% 百分比 |
| `features` | 如 `fast`、`plaid`、`pro` |
| `display` | `{label ≤24 字符, color?}` |

`Agent`:
- `createThread({parentThreadID?, show?, features?, visibility?, multiplayerTTLSeconds?, executor?, project?})` → `PluginThread`。
- `run(message, {timeoutMs? 默认 10 分钟, parentThreadID?, …})` → `{threadID, text}`。
- `AgentThreadExecutor = 'local' | 'orb' | {type:'runner', id}`。

`registerAgentMode({key, label?, description?, color?, agent})`:
- 外部插件须带 `// @amp-agent-mode {"key":"…","label":"…"}` 注释,供静态发现。
- key 和 label 必须唯一、非空、≤24 字符,不得与内置冲突。

声明式 agent:`defineAgent`(`agent.ts`,静态字面量,字段含 `serverOnly`、`draft`、`mcpServers`)与 `defineTool`(`tools/<name>.ts`,`execution: 'server'|'executor'`)。

---

## 5. MCP(https://ampcode.com/docs/markdown/customize/mcp,lastModified 2026-11-04,读 1–460 全文)`[现行]`

### 5.1 两种配置与执行位置

| | 本地 MCP | 远程 MCP 定义 |
|---|---|---|
| 存储 | `amp.mcpServers`(设置文件);带 URL 的条目仍算本地 | ampcode.com 保存(个人/workspace/项目三层) |
| 谁连 | 执行器(CLI、runner、orb)的进程 | **Amp 服务器连**,所以对方服务器需放行 egress IP(见 5.5) |
| 配置字段 | `command/args/env` 或 `url/headers`,支持 `${VAR}` | 同上加认证类型 |

本地配置优先级(高到低):CLI `--mcp-config` > workspace `.amp/settings.json` > 用户 `~/.config/amp/settings.json` > skills(skill 内 `mcp.json`)。

- 推荐把 MCP 打包在 skill 的 `mcp.json` 里,按需加载。
- workspace 设置里的 MCP 需显式批准:`amp mcp approve <name>`。CLI 里首次发现会弹窗。
- orb 内遇到 workspace 级 MCP 时弹窗,提供 Allow Once / Always Allow for Me / Always Allow for Workspace(后者仅管理员)。

orb 与 runner 上的 MCP:
- orb 不读用户本机设置文件。浏览器 OAuth 流在 orb 内不可用,须用 secrets 里的 header、`${amp:id-token}` 或远程定义。
- runner(`amp --no-tui`)遇到需要 OAuth 的服务器,在已挂接线程的对话框里请求登录。同机打开链接则自动完成;异地则浏览器最终落在无法加载的 `localhost` 页,需把完整 URL 贴回对话框。token 保存在 runner 自己的密钥存储。

### 5.2 CLI

```
amp mcp add <name> -- <cmd…>   |   amp mcp add <name> <url>
amp mcp doctor | amp mcp approve <name>
amp mcp oauth login <name> --server-url … --client-id … --client-secret … --scopes "openid,…"
amp mcp oauth logout <name>
amp mcp remote --personal|--workspace <ws>|--project <p>|--current-project  add|update|remove|list|check|tools [--refresh]|login|logout
   add 选项: --auth none|bearer|oauth|workload-identity, --bearer-token-file, --oauth-client-id, --oauth-client-secret-file, --headers '[["k","v"]]', --secret-headers, --disabled, --json
```

- 密钥只能通过文件或 stdin(`-`)传,**不接受命令行参数**。
- 名称按作用域内大小写不敏感匹配,UUID 是 `list --json` 返回的 `id`。
- agent 通过 `manage_amp` 工具、topic `remote_mcp_servers` 做同样的事。

### 5.3 OAuth

- 本地:支持 CIMD(Client ID Metadata Documents),不支持则回退动态客户端注册。手动注册的重定向 URI 是 `http://localhost:8976/oauth/callback`。token 存 Amp 密钥存储并自动刷新。
- 远程定义:OAuth 由 ampcode.com 代为处理。自动检测认证类型。Amp 服务器标记为不支持的提供商会拒绝添加并打印提示。

### 5.4 工作负载身份与 ID token

**`${amp:id-token}`**(orb 内、HTTPS、无 OAuth 配置的服务器的 header):
- 等同 `amp orb id-token --audience <origin>`。
- audience 是服务器 URL 的 HTTPS origin(含非默认端口,不含路径与查询)。
- token 只发往该 origin,拒绝重定向,过期前自动替换。
- `token_use:"exchanged"`。
- 401 则连接失败且不重试;token 交换失败则重试。
- 在 URL、本地服务器的命令/参数/环境中使用会被拒绝。orb 外使用会失败并提示「需要 Amp orb」。

**Amp Workload Identity**(需 Amp 员工授权):
- 仅远程定义可用。配置者和每个线程所有者都需要有权限。
- 发 RS256 JWT,有效期 5 分钟,issuer `https://ampcode.com/api/workload-identity`,公钥在 `/jwks.json`。
- audience 是 MCP URL 的归一化 HTTPS origin(含非默认端口),**无 audience 覆盖**。拒绝重定向。
- 声明:
  - `user_id`、`thread_id`,以及存在时的 `workspace_id`、`project_id`。
  - 协作类:`thread_visibility`(`private`/`thread_group_shared`/`thread_workspace_shared`/`public_unlisted`)、`thread_multiplayer`、`thread_non_owner_can_influence`。
  - `sub` 形如 `workspace:<id>:project:<id>:user:<id>:thread:<id>`。
  - `token_use:"mcp"`、唯一 `jti`。不含 email。
- 身份始终是**线程所有者**,即使在共享线程中。
- 无需 OAuth 登录或存储凭据。禁用服务器或移除访问后阻止新签发,已签发的最多再有效 5 分钟。
- MCP 服务器必须校验签名、issuer、精确 audience 和过期时间,并授权身份声明。**origin 是信任边界**,不要在同一 origin 不同路径上托管互不信任的租户。

### 5.5 远程服务器 ID 与覆盖、工具策略

- 每个远程服务器有 ID(初始由名称生成),agent 通过 `tool_search` 以模块名发现、`code_exec` 里 import。
- ID 在个人/各 workspace/各项目内唯一。覆盖顺序:项目覆盖 workspace 覆盖个人。禁用的服务器不覆盖其他。需有缓存工具才参与隐藏。workload identity 服务器在线程内优先。
- 改名不改 ID。
- 工具列表按线程发现,超过 1 天的缓存会后台刷新。刷新触发点:线程 actor 启动、服务器增删改、刷新发现变更、`reload_mcp` 工具或「Reload MCP」命令。
- 运行中的 `code_exec` 保持其所选服务器 UUID。

工具策略(**每个工具一个**,个人服务器本人可设,其他层级的设置方见 `[团队功能·暂不做]`):

| 策略 | 语义 |
|---|---|
| **Allow**(默认,含服务器日后新增的工具) | agent 可直接调 |
| **Ask First** | 每次调用都请线程所有者批准;`code_exec` 中的调用会让代码等待、线程可能休眠;**子 agent 无法等回答,因此不能调**;MCP Apps 仍可调。 |
| **Disabled** | 从 `tool_search` 隐藏,agent 和 MCP Apps 都不能调 |

- 服务器可在工具 `_meta` 里设 `"anthropic/requiresUserInteraction": true`,Amp 把它当 Ask First,只能禁用不能放行。
- 每次调用时检查策略,改动立即生效,包括已在运行的线程。
- 每次回答都被记录在 `code_exec` 调用下方:工具、批准或拒绝、回答者 user ID、时间。agent 看不到该记录。
- `amp.tools.disable` 不适用于远程定义。

### 5.6 其他

- **Multiplayer 限制** `[团队功能·暂不做]`:多人线程里服务器的「Use in Multiplayer Threads」关闭时,该线程内谁也用不了其工具(含所有者)。默认开启,但 Slack/Gmail/Granola 等个人数据服务的新服务器默认关。
- 提问(elicitation,MCP 规范 2025-11-25):对话框里支持 text/number/yes-no/单选,多选会被取消,文本里的链接不可点。服务器也可要求用户去其网页完成一步(Amp 只打开 `https` 或 `localhost` 的 `http`)。10 分钟无应答或 Amp 重启则取消,agent 得知「重启后结果未知」。子 agent 内不能提问。
- 结构化结果:`outputSchema` 的返回类型进工具发现。`await fn(input)` 返回 `structuredContent`,否则返回文本(含对象或数组则解析为 JSON)。出错时抛错,完整响应在 `error.response`。`.raw()` 返回完整 MCP 结果(`isError` 也不抛)。
- 远程服务器 header 可标 Secret,自动检测像凭据的名字或值(如 `X-Api-Key`、`Bearer …`),加密保存,不再回显,空值保持原值,更换 URL 需重新输入。
- 本地 `amp.mcpPermissions`:数组,按首条匹配生效,无匹配则允许。远程服务器用 `matches.url`,本地服务器用 `matches.command`/`matches.args`,`action` 为 `allow`/`reject`。
- 本机放行 egress:Amp 服务器 IP 见 https://ampcode.com/docs/egress-ips(lastModified 2026-11-04,16 个 IPv4,同页还有 `/docs/egress-ips/server.txt`)。orb 与自托管 orb/runner 不从这些 IP 出口。
- MCP Registry Allowlist(企业)`[团队功能·暂不做]`;`amp.mcpTrustedServers` 是单机本地状态。

---

## 6. Settings、AGENTS.md、Model Routing

### 6.1 Settings 全部键(https://ampcode.com/docs/markdown/cli/settings,lastModified 2026-11-02,读 1–约 190 行全文)`[现行]`

存放位置与优先级:
- 用户:`~/.config/amp/settings.json` 或 `.jsonc`(Windows 为 `%USERPROFILE%\.config\amp\…`)。
- Workspace:从 cwd 向上到仓库根的最近 `.amp/settings.json[c]`。非 Git 目录只找当前目录。
- `--settings-file <path>` 替换用户文件。
- `amp config edit [--workspace]` 用 `$EDITOR` 打开。
- 同一设置 workspace 覆盖用户。
- 所有键以 `amp.` 开头。
- JSON Schema:`https://ampcode.com/cli-settings.schema.json`。

| 键 | 类型 / 默认 | 作用 |
|---|---|---|
| `amp.fuzzy.alwaysIncludePaths` | array, `[]` | 模糊文件搜索始终包含的 glob |
| `amp.showCosts` | bool, true | CLI 显示线程成本(管理员可在 workspace 关闭) |
| `amp.git.commit.ampThread.enabled` | bool, true | 提交加 `Amp-Thread-ID: <url>` trailer。**Amp 仅靠此 trailer 把已推送的提交关联到线程并标记线程已 shipped**;关闭后 Amp 还会拒绝手写该 trailer 的 `git commit`。环境变量 `AMP_DISABLE_AMP_THREAD_TRAILER=1` 等价。 |
| `amp.git.commit.coauthor.enabled` | bool, true | 加 `Co-authored-by: Amp <amp@ampcode.com>`。orb 内改用项目设置。 |
| `amp.keymap` | object, `{}` | CLI 快捷键 |
| `amp.mcpServers` | object | 见第 5 节 |
| `amp.notifications.enabled` | bool, true | 完成或阻塞时的提示音;SSH 或设置了 `AMP_FORCE_BEL` 时发终端 BEL |
| `amp.remoteThreadCreation.enabled` | bool, **false** | 允许 ampcode.com 创建在本机交互式 TUI 打开的线程(目录是 TUI 启动目录)。命令面板:`amp: enable remote creation of threads`。 |
| `amp.runner.autoUpdate.enabled` | bool, true | `amp --no-tui` runner 自动更新并重启:无线程运行时,最多每 12 小时一次,保留命令行参数。`amp.updates.mode="disabled"` 也会关闭。 |
| `amp.runner.env.enabled` | bool, **false** | runner 把 ampcode.com 上配置的 Secrets & Env Vars 注入线程的 shell、MCP 和插件。等同 `--amp-env`。 |
| `amp.skills.disableClaudeCodeSkills` | bool, false | 不从 `.claude/skills/` 等加载 |
| `amp.skills.disableGlobalAgentsSkills` | bool, false | 不加载 `~/.config/agents/skills/`、`~/.agents/skills/` |
| `amp.skills.path` | string | 额外 skill 目录(冒号分隔,Windows 分号;`~`;相对路径相对 workspace 根) |
| `amp.terminal.copyOnSelect` | bool, true | |
| `amp.terminal.detailsExpandedByDefault` | bool, false | Alt+T 切换 |
| `amp.thread.autoArchiveOnQuit` | bool, false | |
| `amp.tools.disable` | array, `[]` | 按名字禁用工具;`builtin:toolname` 只禁内置而允许同名 MCP 工具;支持 `*` glob |
| `amp.mcpPermissions` | array, `[]` | 见 5.6 |
| `amp.updates.mode` | string, `"auto"` | `auto`/`warn`/`disabled`;`AMP_SKIP_UPDATE_CHECK=1` 覆盖 |
| `amp.admin.compatibilityDate` | string `YYYY-MM-DD` | 仅托管设置:决定向后兼容迁移 |

页面里提到但未单独列条目的键:`amp.tools.enable`(SDK 与托管设置提及)、`amp.url`、`amp.proxy`、`amp.workspaces`、`amp.mcpTrustedServers`,以及 Neo 公告里的 `amp.permissions`、`amp.dangerouslyAllowAll`、`amp.guardedFiles.allowlist`(旧权限插件的开关)。

环境变量:`AMP_API_KEY`、`AMP_CLI_PATH`、`AMP_URL`、`AMP_SKIP_UPDATE_CHECK`、`AMP_DISABLE_AMP_THREAD_TRAILER`、`AMP_FORCE_BEL`、`AMP_IGNORE_GUIDANCE_FILES`、`AMP_NO_GIT_SETUP`、`HTTP_PROXY`、`HTTPS_PROXY`、`NODE_EXTRA_CA_CERTS`。

托管设置 `[团队功能·暂不做]`:workspace 管理页 JSON 文档(每次保存产生一个带作者与时间的修订版本)、本机 `managed-settings.json`(`/Library/Application Support/ampcode/`、`/etc/ampcode/`、`%ProgramData%\ampcode\`)。合并规则:标量以 workspace 为准,列表 workspace 条目在前合并,对象逐键合并。

### 6.2 AGENTS.md(https://ampcode.com/docs/markdown/customize/agents-md,lastModified 2026-10-20)`[现行]`

| 位置 | 加载 |
|---|---|
| cwd(或编辑器 workspace 根)和父目录(到 `$HOME`)里的 `AGENTS.md` | 总是包含 |
| 子树里的 `AGENTS.md` | 当 agent 读取该子树里的文件时包含 |
| `$HOME/.config/amp/AGENTS.md`、`$HOME/.config/AGENTS.md` | 总是包含 |
| 系统级:`/etc/ampcode/AGENTS.md`、`/Library/Application Support/ampcode/AGENTS.md`、`%ProgramData%\ampcode\AGENTS.md` | 总是包含 |
| 网页 Settings → Instructions → Global AGENTS.md(个人) | 对顶层 agent |

回退:某目录没有 `AGENTS.md` 但有 `AGENT.md` 或 `CLAUDE.md` 则包含这个。

- Workspace 管理员的 Global AGENTS.md 先于个人和仓库的指引;不作用于子 agent、Puck、raw 自定义 agent。`[团队功能·暂不做]`
- 没有 orb 专属的 AGENTS.md 规则。orb 只是照常读仓库里的文件,GitHub 页建议在 AGENTS.md 加一行让 agent 在 PR 描述里写 `Amp-Thread-ID:`。
- `AMP_IGNORE_GUIDANCE_FILES`:冒号分隔的 glob,匹配绝对路径(含隐藏目录,`/` 分隔,`**`)。`*` 表示全忽略。被忽略的文件仍可被工具显式读取。
- `@` 引用:
  - 相对路径相对包含该提及的 agent 文件。
  - `@~/path` 指家目录。
  - **不支持绝对路径**。
  - 代码块里的 `@` 被忽略。
  - 支持 glob(`@doc/*.md`、`@.agent/**/*.md`)。
- 细粒度:被提及文件的 YAML front matter 可写 `globs:`。只有 Amp 读过匹配的文件才包含它。无 `globs` 则总是包含。glob 隐式加前缀 `**/`,除非以 `../` 或 `./` 开头(此时相对被提及文件)。

### 6.3 Model Routing(https://ampcode.com/docs/markdown/customize/model-routing,lastModified 2026-10-29,全文 289 行)`[现行]`

数据模型:

| 概念 | 内容 |
|---|---|
| **connection** | 一个凭据加其设置。四类:**API key**(单提供商;Anthropic/OpenAI/OpenRouter 可用 workload identity federation)、**订阅**(ChatGPT、𝕏 Premium+/SuperGrok;浏览器登录)、**云平台**(Amazon Bedrock、Google Cloud Agent Platform/Vertex、Microsoft Foundry)、**路由器/网关**(OpenRouter、Vercel AI Gateway、Cloudflare AI Gateway、Ollama Cloud、OpenCode Go、Custom URL)。另有 GitHub Copilot 订阅(设备授权)。 |
| 范围 | 个人连接只作用于自己的线程。Workspace 连接作用于所有成员 `[团队功能·暂不做]`。 |
| **priority** | 整数,**0 最先尝试**。设置某优先级会把已占用者及其后的连接下移一位。 |
| **active** | inactive 的连接被跳过但保留设置。API key 与路由器可同时多个 active;订阅每类每用户只允许一个 active(激活某个 ChatGPT 订阅会停用另一个)。 |
| **路由规则** | 按每个模型:依次试个人连接(自上而下),然后 workspace 连接,最后 Amp 自己。用第一个 active 且其模型映射包含该模型者。没有任何连接服务的由 Amp 服务并扣 Amp 积分。 |
| **model mapping** | 每连接一份,每行一个模式,使用 canonical `provider/model`。语法:`*` 只能在开头或结尾;前导 `-` 表示排除;**最后匹配的行胜出**;未匹配任何行的模型不服务;`#` 注释;精确包含行可加 ` -> providerModelID` 覆盖发给提供商的模型 ID(通配和排除行不允许箭头)。空映射使用连接类型默认值(多数为提供商支持的全部模型)。Bedrock/Vertex/Foundry 在选择模型之前什么都不服务。 |
| Model Metadata Overrides | 对 Amp 不认识的模型(如 Custom URL),需设 Input Tokens、Output Tokens、Tools=Yes 才出现在 Dial 的模型菜单;仅 Web 设置可设,CLI 只保留不能设。 |
| Custom URL | 4 种 API 格式:`chat-completions`(默认,`<base>/chat/completions`)、`responses`(`<base>/responses`)、`anthropic-messages`(`<base>/v1/messages`,基址已以 `/v1` 结尾则不重复)、`google-genai`(`<base>/models/<model>:streamGenerateContent?alt=sse`)。鉴权 `Authorization: Bearer <key>`;google-genai 默认 `x-goog-api-key`,在 key 里填 `Bearer <token>` 则只发 Authorization。支持自定义 Headers(可标 Secret)和 Query Parameters(google-genai 除外)。 |
| 请求头 | **每个请求带 `x-amp-thread-id: T-…`**,用于网关按线程分组日志,会替换同名配置头。 |
| 其他 | Amp 服务器代发请求,提供方有 IP 白名单时需放行 egress IP。Check Access 按钮及 `amp config model-providers test <id>` 验证凭据。`check-access --provider-model <p/m>` 做一次短推理并报告实际由哪个连接服务。 |

CLI:`amp config model-providers list|set-priority <id> <n>|activate|deactivate|test|check-access|edit-router <id> --model-mapping "*/*,-openai/gpt-5.4"|edit-key <id> --model-mapping …|add-chatgpt-subscription [--legacy]|add-github-copilot-subscription`,`--clear-model-mapping`,`--headers`,`--secret-headers`。Agent 用 `manage_amp` 的 topic `model_providers`,Puck 也能做。

附带:
- **Painter**:Google Painter 开启时可在 Mode Dial 选 Nano Banana 2,必须有 Google AI Studio API key 连接,否则报错而不回退到 Amp 积分。
- **Daybreak**:仅 OpenAI API key(项目开启 Daybreak Blue),不走 ChatGPT 订阅。
- **Enterprise** 可对连接收 Amp 费用并禁止个人连接 `[团队功能·暂不做]`。

### 6.4 `amp clone`(https://ampcode.com/docs/markdown/cli/clone,2026-10-27)`[现行]`

```
amp clone owner/project   |   amp clone https://ampcode.com/@owner/project   [dir]
amp clone user-plugins|user-skills|workspace-plugins|workspace-skills ;  owner/-/plugins|skills
amp plugins repositories | amp skills repositories
```

- 仅用于 Amp 托管仓库;GitHub 等用 `git clone`。
- 私有仓库先 `amp login`。
- `amp clone` 把 Amp 装成全局 Git credential helper,每条 Git 命令从你的 Amp 登录取**短期凭据**,机器上不留长期 token。`--no-git-setup` 或 `AMP_NO_GIT_SETUP=1` 跳过。
- 只拉代码的机器用只读 Git 凭据(见 projects 文档,本报告未读)加普通 `git clone`。
- `amp sync --apply` 把 orb 的工作拷到本机(出自该页链接,orbs 文档第一波已读)。

---

## 7. GitHub / GitLab / Google 集成

### 7.1 GitHub(https://ampcode.com/docs/markdown/integrations/github,lastModified 2026-11-09)`[现行]`

授权模型(单用户可见部分):
- **GitHub App 连接**,两步:① Authorize Amp(账号关联,Amp 以你的身份行动);② Install Amp app(选账号/组织,All 或 Only select repositories)。Amp 能访问的 = App 安装所含仓库 ∩ 你的 GitHub 账号可访问的仓库。**GitHub Enterprise Server 不支持。**
- 支持连接多个 GitHub 账号。Disconnect 只删 Amp 持有的 token,不卸载 App。Reconnect 在过期或被撤销时出现。
- 默认 orb 以线程创建者的身份 push 和开 PR。项目可改成以 Amp GitHub App 的 bot 身份行动。bot 只用于该项目的 GitHub 仓库,且仅在创建者本人也能 push 的地方生效。失去 push 权限最多 10 分钟传到运行中的 orb。
- 公开仓库无需连接即可克隆进 orb。

**orb 内如何拿到 Git 凭据**:
- orb 启动时克隆到 `/home/user/workspace/repo`,该克隆用的凭据只用于克隆命令并随后丢弃。
- 之后每条访问 GitHub 的 git 命令都通过 **credential helper 向 Amp 要短期 token**(账户级)。token **从不写盘**,也不进入项目快照。
- 因此 agent 可 fetch/push/clone 任何你的连接能访问的 github.com 仓库,不止项目仓库。
- `gh` 预装并用同一机制取 token。SSH URL 自动改写为 HTTPS。
- GitHub 不接受 App token 上传图片和视频,所以 `gh … --attach` 在 orb 里会失败。Amp 的做法是把媒体发布到公开 URL 后在 Markdown 里嵌入。
- 分支保护等规则与本机推送一致。
- runner 或本地 CLI 线程用的是**那台机器**自己的 Git 配置,Amp 不注入。

提交身份与签名:
- 个人设置 → Signing Keys → Git Identities,可加多个邮箱(需邮件验证)。
- 个人项目的提交作者策略三选一:**Amp**(作者 `Amp <amp@ampcode.com>` 加你为共同作者)、**You + Amp**、**You**。新 Amp 托管项目默认你为作者,其他新项目默认 Amp 为作者。
- Agent 通过 shell 的提交带 `Amp-Thread-ID:` trailer。
- **签名提交**:Amp 为你生成 SSH 密钥对,私钥加密保存在 Amp 服务器,orb 内 git 调 Amp 来签名,**私钥从不进入 orb**。开关「Sign Git Commits in Orbs」。只有所选身份的邮箱已验证时才用你的钥签。Amp 身份提交用 Amp 自己的钥签。要在 GitHub 显示 Verified,须把公钥作为 Signing Key 加到 GitHub,且 committer 邮箱已在 GitHub 验证。
- PR 描述含 `Amp-Thread-ID: <url|T-…>` 时,线程标题栏列出该 PR 和状态(open/draft/queued/merged/closed),在 PR 打开或编辑时读取。

**事件订阅**(GitHub Webhooks,出自 2026-10-12 "You've Got Comments"):
- 事件经 Amp GitHub App 到达,**不在你的仓库里加 webhook**,也不需要仓库 webhook 权限。Amp 校验 GitHub 签名,无需保持专用 orb 存活。
- 订阅对象:
  - Issue:新评论、关闭/重开。
  - PR:加上提交的 review、行内评论、进出 merge queue(merge group commit 事件)。
  - CI:订阅**某个确切 commit**,可选整个 check rollup、特定 workflow 或命名 check/status。匹配的 CI webhook 立刻触发刷新,另有定期轮询兜底;失败一经确认立即送达;CI 消息会 steer 活动的 agent。
- 事件继续已订阅的线程。订阅不存自定义指令,也不授予 push、部署或执行 GitHub 评论里指令的权限。
- **Orb spawner**:每个新开 issue/PR 或收到你的 review 请求时启动新线程与 orb。可按「打开时已有的标签」过滤,PR spawner 可限定开 PR 的 GitHub 用户。评论和 CI 不能触发 spawner。
  - 个人账号下的全部仓库可用 installation-wide 选项。
  - review request spawner 只监视一个仓库、总是以你身份运行、需通过 Amp/Puck 创建。
  - 已生成的线程自动订阅其源 issue/PR 的评论。
  - 自动跟进订阅在 issue/PR 关闭时停止,重开时恢复;跳过你自己发的评论,除非提及 `@ampagent`。
- 管理:Settings → Integrations & MCP → GitHub → Event Subscriptions。每条订阅有 Pause/Resume/Edit,展开可看 Deliveries(状态、时间、错误)和 Spawned Orbs。「Delivered」只表示线程接受了事件,不代表 agent 完成。归档接收线程会停止投递;spawner 要手动暂停或删除,或到过期时间。
- Workspace 级 spawner 以 service account 运行 `[团队功能·暂不做]`。
- 非 GitHub 主机(GitLab/Bitbucket/其他 HTTPS):仅 Git 访问,GitHub 专属的 PR 预览、CI 工具、issue/PR webhook 不会随之启用。私有仓库需自行配置凭据(见 orbs/customizing 的 Configure Git)。Ship 和 Push to Branch 对其他主机也可用,但只有 GitHub 仓库会额外要求报告 PR URL。

### 7.2 GitLab(https://ampcode.com/docs/markdown/integrations/gitlab,lastModified 2026-11-10;读到约 380 行,基本读全)`[现行,2026-11-05 上线]`

- 连接:gitlab.com 用 Amp 自己的 OAuth 应用;自管实例需 workspace 管理员注册实例并建 OAuth 应用。自管实例的注册与 OAuth 应用 `[团队功能·暂不做]`,但单用户使用 gitlab.com 即可。
- scope 必须**同时**授予 `api` 和 `write_repository`,GitLab 给的少则 Amp 不保存连接。每实例一个连接。
- orb 认证:Git 命令经 Amp 的 credential helper 取 token(覆盖 gitlab.com 和已连接实例)。`glab` 预装并走同一 helper,只对对应主机发送 token;orb 内若设置了 `GITLAB_TOKEN`、`GITLAB_ACCESS_TOKEN`、`OAUTH_TOKEN` 或 `GLAB_CONFIG_DIR`,`glab` 会改用它们而不用连接。orb 里没有 GitLab 用的 SSH key,用 HTTPS。
- MR、issue、pipeline 的读取由 Amp 服务端用你的连接完成,**不把 token 交给 orb**。
- Amp 服务器不可达的实例(私网):用自建 token 加 pre-clone 脚本里配置 `credential.https://<host>.helper`。不要用 `glab auth login`,它会把 token 写进配置文件并进入项目快照。
- 网络:Amp 服务器必须能通过公网 HTTPS 到你的实例(从不连私网地址),必要时放行 egress IP。实例的 webhook 回 `https://ampcode.com`。orb 从自己的主机地址出站(用 Tailscale 或自托管 orb 解决白名单)。
- **Push Updates**(GitLab webhook,名为 `Amp`):触发 push/MR/issue/confidential issue/评论/confidential 评论/pipeline。一键安装用 GitLab 签名 token(GitLab 19.0 新增);签名用 HMAC-SHA256,拒绝与当前时间相差超过 10 分钟的投递。手动配置用 secret token 比对。Amp 对每个已验证的投递都返回成功(即使无法应用),避免 GitLab 禁用该 webhook。webhook 属 workspace,**个人线程不能订阅 GitLab 事件**(「需要 workspace 内的线程」)。
- 订阅:MR 评论(含 merge/close/reopen/approval/changes requested/target branch change)、issue 评论(含 close/reopen)、CI(按 commit,失败立刻报,全部完成再发一条汇总,**创建 24 小时后到期**)。机密 issue 和内部评论仅在你至少是 Reporter 角色时送达。
- 仍有 spawner(新 MR/issue 触发,可按标签过滤),同样依赖 Push Updates 与 workspace 线程。

### 7.3 Google(https://ampcode.com/docs/markdown/integrations/google,lastModified 2026-10-30)`[现行]`

- 授权:通过 **workspace 自己的 Google Cloud OAuth 客户端**连接(Web 应用类型,重定向 `https://ampcode.com/auth/gmail/callback`)。因此需先由 workspace 管理员设置客户端 `[团队功能·暂不做]`,单用户 Oryn 可直接用用户自己的 OAuth 客户端(`[推测]`)。
- 每个服务分级,创建连接时才请求对应权限:
  - Gmail:Read / Read, Organize, and Draft(新连接默认) / Read, Organize, and Send。
  - Calendar:Read / Read and Write with Approval(默认) / Read and Write。
  - Docs、Sheets、Drive:Read(默认)。
- 「询问优先」级别(Gmail Draft、Calendar with Approval)在每封邮件发送前、每个日历变更前弹窗让你审阅批准。这两对级别的 Google 权限相同,切换只改 Amp 内保存的选择,不回到 Google。
- 降低访问级别时 Amp 移除整个 Google 连接并请你重新授权较小权限集合(Google 不允许应用只退还部分权限)。提升级别要回 Google 卡片重新批准。
- 在线程里问 Amp 当前权限、降级、改多人设置,每次都弹窗确认。
- 多人线程中 Google 工具会被关闭,含所有者 `[团队功能·暂不做]`。
- 外部观众(External)的未验证应用:Testing 状态最多 100 个测试用户且 7 天后断开;In production 未验证最多 100 人。Internal 无限制。
- 事件订阅:**未找到** Google 的事件订阅。

---

## 8. 其他读过但未归入上述的内容

- `llms.txt`(https://ampcode.com/llms.txt)是站点索引。除 `/api/v2/openapi.json` 和 `https://ampcode.com/cli-settings.schema.json` 外,**未找到**其他 OpenAPI/JSON schema。
- 站点另有 `https://ampcode.com/docs/egress-ips/server.txt`(纯文本 IP 列表)。
- 站点有 RSS:`/news.rss`、`/notes.rss`。
- 页面的 `lastModified` 汇总:sdk 2026-09-18、sdk/typescript 2026-11-04、sdk/python 2026-09-18、streaming-json 2026-10-23、customize/mcp 2026-11-04、decision-models 2026-10-30、agents-md 2026-10-20、model-routing 2026-10-29、cli/settings 2026-11-02、cli/clone 2026-10-27、egress-ips 2026-11-04、github 2026-11-09、gitlab 2026-11-10、google 2026-10-30。plugin-api 页面未显示 lastModified。

---

## 9. 对 Oryn 的启示(每条一行)

1. 线程状态枚举至少 `idle/working/awaiting_approval/error`,同一个状态用同一套命名;Amp 的 External API(`working`、`awaiting_approval`)与插件(`running`、`awaiting-approval`)命名不一致,Oryn 不要重蹈。
2. External API 只做轮询、不做 SSE 是 Amp 的取舍,Oryn 既然要实时多端同步,应在 REST 之外原生提供事件流(WS 或 SSE),但保留 `agentState` 这类低成本轮询字段。
3. 状态字段允许「首次汇报前为空」且文档说明会滞后(约 10 秒),Oryn 的 agentState 应带 `updatedAt` 以便客户端判断新鲜度。
4. 线程 ID 用 `T-<uuid>`;消息只对 `messageID`、`messageVersion`、`createdAt` 做稳定承诺,其余字段先声明不稳定;同时提供另一套稳定的「插件视图」`ThreadMessage`。
5. 消息 ID 从数值升级成稳定字符串(Neo thread-actor),Oryn 第一天就用不透明字符串 ID。
6. 用量模型应把 `usage` 拆成「总额 = 模型 + orb + tool」,且 orb/tool 总额不计入 models 的合计,否则会重复计费。
7. 为 orb 保存 `size/cpuCount/memoryMB/runtimeMs/usage` 五元组,并允许规格为空(规格下线后仍可追溯),这是 orb 统计的直接模板。
8. 用量要区分「谁付的钱」(付费积分、促销、订阅、free tier),`runtimeMs` 与 `usage` 口径可以不同;Oryn 无计费,但应保留 `calls`/`runtimeMs` 这类原始量,再把金额作为可选字段。
9. 统计窗口需要上限(日用量 ≤365 天、线程用量 90/180 天保留),Oryn 的保留策略要提前写进 API 文档。
10. Amp 没有按项目聚合的端点,Oryn 可以直接加 `groupBy=project|thread|model|orbSize|tool|day` 的统一聚合端点。
11. `estimatedProviderCostAtListPriceUSD` 对 BYOK 估算「按公开标价应付多少」,并明确路由器连接记 0、订阅排除、早于上线日期的不含;Oryn 做自有 provider 统计可借鉴这套免责口径。
12. 分页统一 `limit`+不透明 `cursor`,按 `firstSyncedAt` 排序翻页稳定,按 `updatedAt` 翻页会漂移,需按 id 去重;Oryn 应把两种排序的语义写清。
13. 创建线程只对个人 token 开放、限流 100/分钟、响应即返回(agent 异步继续),是「创建=受理」语义的好样板。
14. 密钥 API 永不回显明文,只给 `redactedKey` 与 `lastUsedAt`;轮换靠「新建 active 再删旧」,不做原地更新。
15. `requestId`(1–256 字符)做用户消息幂等,Oryn 多端同步和断线重发必须有这个。
16. steer 与排队是两种语义:忙时来的消息排队,`steer:true` 在下一个打断点处理;`Esc Esc` 是中断并立即发送;Oryn 的消息协议要有这个标志位。
17. streaming-json 把 `result` 当作唯一结束信号,并要等后台子 agent 结束;Oryn 的协议要定义「回合结束」与「运行结束」的区别。
18. 对话框(`input/confirm/select`)是最适合多端镜像的数据结构:请求有稳定 `fields[]`、`name` 作返回键,答复是 `undefined|false|Record`,取消不抛错;Oryn 应把它建模为「待处理请求」资源,带 `requireHuman`、`skipIfUnwatched`、定向到连接的字段。
19. 密钥类输入(`secret:true`)只返回给请求方,不进历史、不进转录、不镜像到其他端;Oryn 的多端同步需要对这类对话框做「不复制」处理。
20. `skipIfUnwatched` 解决了「定时/无人观看线程里弹窗卡死」的问题,Oryn 的自动化(定时唤醒、webhook)必须有同等机制。
21. UI 动作(`open-pane`/`open-portal`)只投递给已打开的标签、不排队,这是「瞬时 UI 事件」与「持久状态」的清晰边界,Oryn 事件总线也应区分。
22. 快捷操作(每线程 ≤5 个、图标白名单、文字 1–40 字符)说明插件 UI 贡献点要有硬上限。
23. 插件事件 `tool.call` 的五种返回(allow/reject-and-continue/modify/synthesize/error)覆盖了权限、改参、桩实现的全部需求;Oryn 的权限系统可以全部建在这套 hook 上,而不是做内置权限(Amp 的做法)。
24. 默认不询问权限、权限作为可选插件是 Neo 的取舍;Oryn 单用户场景可选择默认放行加可装策略插件。
25. `agent.end` 返回 `continue` 并带 `maxContinuations`(默认 5,用户消息重置),是安全的自驱动循环机制。
26. 插件 webhook:capability URL 即凭据、至少一次投递加 `event.id` 幂等、30 秒超时、指数退避封顶 5 分钟、1 小时丢弃、每 webhook 10 事件/分钟突发限流;Oryn 的自动化入口可整套照搬。
27. 模型调用「按线程计费并路由」(`ai.*` 都带 `threadID`),让所有 AI 辅助调用都能计入线程统计;Oryn 统计要求每次调用都能归属到线程。
28. 决策模型(`noul/choice/score`)返回概率而非文本,适合权限判断、标签、diff 排序;Oryn 可作为可选能力,且「指定模型即回答者,不回退」是隐私上的重要约束。
29. MCP 默认延迟加载(`tool_search`+`code_exec`),不把全部工具塞进上下文;远程 MCP 由服务端连接;工具策略三态 Allow/Ask First/Disabled 且每次调用时检查,改动立即生效。
30. 子 agent 不能等待人回答,因此不能调 Ask First 工具;Oryn 的子 agent 设计要有「无人可问」的语义。
31. 工作负载身份:短期(5 分钟)JWT、audience 为服务 origin、声明含 `user_id/thread_id/project_id`,是 orb 访问内部服务的标准做法;Oryn 自有 orb provider 可发同类 token。
32. orb 里的 Git 凭据永远是「按次向服务端要短期 token」,不落盘、不进快照;签名私钥留在服务端,orb 只调用签名接口;这是 Oryn orb 凭据设计的核心约束。
33. 提交归属靠 `Amp-Thread-ID:` trailer 把提交关联到线程并标记已交付;Oryn 可用同样的 trailer 实现「线程⇄提交⇄PR」关联,并提供关闭开关。
34. Model Routing 的数据模型简单可抄:connection(凭据+设置+priority+active+mapping),按模型自上而下取第一个 active 且 mapping 命中的连接,兜底才是官方;映射语法「最后匹配的行胜出、`->` 改写上游模型 ID」。
35. 每个路由请求带 `x-amp-thread-id` 头,让网关/代理按线程聚合日志,Oryn 的 provider 也应如此。
36. 设置里只有约 20 个键,大量能力移到了网页设置或 `manage_amp` 工具;Oryn 既然「omp 配置不做 GUI」,可以用同样思路:配置文件加 CLI 子命令加 agent 自管工具。
37. AGENTS.md:向上到 `$HOME`、子树按需、`@` 引用(相对路径、`~`、无绝对路径、代码块内忽略、glob)、front matter `globs` 条件加载;这些规则本身与 omp 的上下文文件机制兼容,可直接对齐。
38. 事件订阅按「具体 commit/PR/issue」建订阅,不按分支订阅,且有 Pause/Resume/过期时间与 Deliveries 历史;Oryn 的自动化订阅应有同样的可观察性。
39. 把「已删除功能」写进 SDK/CLI 的兼容说明(如旧 SDK 版本固定、`--take-me-back` 一个版本后删)是 Amp 的发布方式;Oryn 作为新项目不需要背这些包袱,直接用最新语义。
40. 团队类(workspace、groups、entitlements、service accounts、多人线程、企业托管设置)占 API 近一半端点,Oryn 单用户阶段全部可删;但线程 `visibility` 等字段可以先预留。

---

## 10. 实际读过的页面与行号范围

| 页面 | 读到的范围 | 备注 |
|---|---|---|
| https://ampcode.com/api/external | 1–400 / 约 11322 | HTML 渲染页。其余用 openapi.json 代替 |
| https://ampcode.com/api/v2/openapi.json | 全部路径与 schema 名通过 grep 列出;细读 1–300、548–1146、1648–1953、2509–2640、3592–3935、4133–4200、4462–4540、4630–4740、5854–5960、6118–6240、6737–6830、16614–16720 | 总 18353 行 |
| https://ampcode.com/docs/markdown/sdk | 1–620 / 1157 | 余下约 540 行未读 |
| .../sdk/typescript | 1–602 全部 | |
| .../sdk/python | 1–334 / 约 515 | 余下约 180 行未读(多为 ToolResult 与 Usage 类型,与 TS 对应) |
| .../cli/streaming-json | 1–380 全部 | |
| .../plugin-api | 1–3300 全部 | |
| .../customize/mcp | 1–460 全部 | |
| .../customize/decision-models | 全部 | |
| .../customize/agents-md | 全部 | |
| .../customize/model-routing | 1–289 全部 | |
| .../cli/settings | 全部(约 190 行) | |
| .../cli/clone | 全部 | |
| .../egress-ips | 全部 | |
| .../integrations/github | 1–500 全部 | |
| .../integrations/gitlab | 1–380 | 读到订阅与 spawner 部分结束,基本读全 |
| .../integrations/google | 1–250 全部 | |
| https://ampcode.com/llms.txt | 全部 | |
| https://ampcode.com/chronicle | 1–300 / 308 | 含 News 全表 |
| https://ampcode.com/news/ | neo、drop-the-neo、the-coding-agent-is-dead、stick-a-fork-in-it、ask-to-handoff、slashing-custom-commands、tab-tab-dead、todos-are-done | 全文 |

### 未读清单(本次)

- openapi.json 的以下细节:groups/entitlements 的创建与更新请求体(团队功能)、model-provider-keys 各 provider 的创建请求体、`/inference/decisions` 的完整请求与响应、仓库端点的逐个查询参数、`/actor` 响应之外的错误码细节。
- sdk 概览页 620 行之后、Python SDK 页 334 行之后。
- `https://ampcode.com/cli-settings.schema.json`(未读,可能列出比文档页更多的键)。
- 第一波读过的公告里和本次主题无关的内容(未重读),以及 2026-08 以后若干公告(GitLab、service-accounts、artifacts 等)的正文;本报告关于它们的日期取自 chronicle 列表。
- 同时存在但未读的文档:`/docs/projects#read-only-git-credentials`、`/docs/orbs/customizing#configure-git`、`/docs/orbs/handling-secrets#oidc`、`/docs/enterprise/*`。

## Critical Files for Implementation

- `https://ampcode.com/api/v2/openapi.json` — External API v2 的权威 spec,线程、用量、BYOK、推理、仓库端点的字段表全部来自这里。
- `https://ampcode.com/docs/markdown/plugin-api` — 事件、工具、对话框(多端镜像)、webhook、agent、`ai.decide` 的完整类型定义。
- `https://ampcode.com/docs/markdown/cli/streaming-json` — CLI/SDK 的流式消息类型与 steer/图片输入格式。
- `https://ampcode.com/docs/markdown/customize/model-routing` — connection/priority/mapping 数据模型与路由规则。
- `https://ampcode.com/docs/markdown/customize/mcp` — 本地/远程 MCP 的执行位置、工作负载身份、工具策略。
## 公开分享深挖:对公开入口的调研结果(追加到前述报告)

**范围与取证说明**
- 页面按 2026-11-12 当前文档读取。状态标签沿用 `[现行]`、`[已移除]`、`[已被取代→新形态]`。
- 团队/企业项只一两行带过,标 `[团队功能·暂不做]`。
- 文档没写的细节一律写「未找到」,不推测;少量 `[推测]` 已单独标出。
- 一个关键事实:**官方文档没有描述未登录访客打开 Unlisted 线程页时的具体 UI。** 下面第 A 节如实写出已知部分和未知部分。

---

### A. 线程公开分享(Unlisted)

来源:https://ampcode.com/docs/markdown/threads(lastModified 2026-11-10,Share a Thread 一节)、https://ampcode.com/news/end-of-public-threads(2026-06-02)、https://ampcode.com/docs/markdown/prompting。

#### A1. 可见性模型 `[现行]`

| 取值 | 谁能打开 | 备注 |
|---|---|---|
| **Private** | 你本人;加上 workspace 管理员经「审计访问」 | 无 workspace 时新线程默认 private |
| **Workspace** | workspace 全体成员 | `[团队功能·暂不做]` |
| **Group** | 你选的分组成员,加管理员审计访问 | 仅 Enterprise,`[团队功能·暂不做]` |
| **Unlisted** | 「Anyone on the internet who has the link」 | **这就是公开分享。** |

- 设置入口:
  - 网页:线程标题栏 **Share** → **General Access**。
  - 命令面板:`Share Thread...`。
  - CLI:`amp threads share T-… --visibility unlisted`,或 `Ctrl+O` 后运行 `set visibility`。
- 搜索过滤:`visibility:private|workspace|group|unlisted`(Activity、线程搜索、问 Amp/Puck 都可用)。**这些过滤只缩小你本来就能找到的线程,不属于你账号或 workspace 的 Unlisted 线程「仍然无法发现」。**
- SDK 里 `visibility` 取值是 `private | unlisted | workspace | group`,与上表一致。
- 外部 API 的 `visibility` 过滤取值是 `private | group_shared | workspace_shared | public_unlisted`,命名与 UI 不同。

#### A2. URL 形态

- 线程页:`https://ampcode.com/threads/T-<uuid>`,例如 `T-01a1863c-2c0a-7230-9266-9b5de08e4c8f`。
- 因此 URL 靠 **UUID 不可猜测**,不靠签名或 token。官方公告把 Unlisted 定义为「share them with anyone by unguessable URL」。
- Markdown 导出:URL 末尾加 `.md`,即 `https://ampcode.com/threads/T-….md`。
- **Export** 菜单提供 Markdown;**仅线程创建者**还能导出完整 JSON。
- CLI:`amp threads markdown T-…`、`amp threads export T-…`。

#### A3. 访客看到什么

| 问题 | 文档答案 |
|---|---|
| 是否只读 | **是,不可协作。** 文档:「Multiplayer is not available while anyone with the link can view the thread, and making a thread viewable by link turns it off」。Unlisted 线程不能开多人模式,把线程改成 Unlisted 会自动关掉多人。 |
| 是否需要登录 | 线程文档只说「anyone on the internet who has the link」。**未找到**对「未登录访客」的明确承诺。对照:同一份文档对**制品**的 Anyone with the Link 明确写了「without signing in」。线程页没写,不能视为同样保证。 |
| 是否含工具调用与 diff | **未找到。** 线程的定义是「your prompts, the agent's replies, every tool call it made, and the files it changed」,但没说访客页是否原样渲染这些内容。 |
| 是否实时更新 | **未找到。** |
| 访客能否看终端/文件/Changes 面板 | **未找到。** 面板控制(终端、文件、桌面)属于有写权限者。 |
| 访客能否访问 orb 的 portal | 否。公开 portal 是单独机制(见 B 节):「The public link does not grant access to the thread or its review tools」。 |

#### A4. 撤销与访问控制

- 撤销方式:把可见性改回 Private 等。
- **文档没有说撤销后已打开页面或缓存会怎样。** 旧 URL 失效这一点是推论,文档没写,标 `[推测]`。
- 对制品有一条确定的行为:workspace 管理员关闭「Allow Users to Share Threads and Portals Outside the Workspace」或「Allow Public Artifact Uploads」后,「existing public links then stop working」。这说明公开链接在服务端按开关实时判定。`[团队功能·暂不做]`(开关本身是 workspace 级)。
- 管理员审计访问:查看非公开线程时要重新验证身份,页面显示审计提示,查看被记录审计日志,制品以只读方式打开。`[团队功能·暂不做]`。

#### A5. 设计理由与踩过的坑

来自 https://ampcode.com/news/end-of-public-threads(2026-06-02):
- **已移除**「public, internet-wide, discoverable thread sharing」,即可被发现的公开线程。
- 理由:「It's getting too hard to review a thread to ensure it doesn't contain any snippets of sensitive files. Each model release means the agents get better, and as they get better they read more files into context.」agent 越强,读进上下文的文件越多,人工审查线程里是否含敏感片段越来越不可行。
- 并明确说「proactive and isn't prompted by any incident」。
- 迁移:所有原 `Public (Discoverable)` 线程自动变成 `Unlisted`,以免博客里的线程链接失效。
- 公开用户资料页(如 `ampcode.com/@sqs`)仍显示活动,但**不再显示任何线程**。

安全页补充(https://ampcode.com/security,2026-11-10):
- 密钥自动脱敏:检测到的密钥在进入线程前替换成 `[REDACTED:amp]` 之类的标记,发生在最底层,所以不会出现在 LLM、本地缓存、ampcode.com 上。
- 脱敏是「best-effort」。万一漏掉:编辑前一条消息重发(覆盖 ampcode.com 上的线程内容)、改成 private(不会删除已存的密钥)、轮换密钥。
- 删除线程后 30 天内清除全部数据;删除线程同时删除其 orb。

---

### B. Portal 公开访问

来源:https://ampcode.com/docs/markdown/orbs/portals(lastModified 2026-11-10,639 行全读)、https://ampcode.com/news/portals(2026-08-06)、https://ampcode.com/news/friendly-urls-for-sharing-orbs(2026-08-23)、https://ampcode.com/docs/markdown/orbs/self-hosted、https://ampcode.com/docs/markdown/cli/runners。

#### B1. 默认访问模型 `[现行]`

- Portal 是「authenticated URLs that expose web servers running inside an orb」。
- 默认只有「能查看该线程的人」可访问,与应用自身的认证无关。
- 多人线程的参与者在其访问有效期间可用。`[团队功能·暂不做]`

#### B2. 公开访问(限时)

- 入口:Portal 工具栏 **Portal Options (⋯)** → **Access** → **Make Public For**。
- 时长只有四档:**1 小时、3 小时、1 天、7 天**。
- 范围:**只针对该端口**。「The public link does not grant access to the thread or its review tools」。
- 公开期间,**Access** 面板显示到期时间。可在 **Keep Public For** 选新时长续期,或点 **End Public Access** 提前终止。
- 谁能改:
  - 也可让 agent 改,走 `manage_amp` 工具,topic `portals`,operation `set-public-access`;`list` 操作列出状态。
  - **每次让 portal 变公开或延长公开,agent 都要先弹窗让你批准。**
  - **`amp` CLI 不能改 portal 访问级别**,只有 `amp orb portal list` 能看哪些 portal 是公开的以及到何时。
- 不可用的情形:
  - 线程跑在**自托管 orb**上,且 provider 关掉了 **Public Portals**。
  - workspace 关了「Allow Users to Share Threads and Portals Outside the Workspace」。`[团队功能·暂不做]`
- **本机 runner 的 portal 不能公开**(runners 文档:「Runner portals cannot be made public」),也没有评审按钮。只有你自己能开,或共享给 workspace。`[团队功能·暂不做]`
- 自托管 orb 默认关闭公开 portal。理由:「a public portal is an anonymous tunnel into your network: whoever opens the page talks to the service in the orb, and the orb reaches whatever your network lets it reach」。关闭 provider 的 Public Portals 开关会**立即结束该 provider 所有 orb 上全部 portal 的公开访问**。

#### B3. 访客身份头

Amp 在经由外部 portal 代理的请求上设置 `X-Amp-Authenticated`,值是逗号分隔的 token:

| token | 含义 |
|---|---|
| `amp-user=yes\|no` | 访客是否已注册的 Amp 用户 |
| `workspace-member=yes\|no` | 访客是否属于拥有该线程的 workspace |
| `collaborator=yes\|no` | 是否线程创建者(或多人线程中当前可贡献者,通行密钥要求除外) |

典型取值:
- 未登录访客:`amp-user=no, workspace-member=no, collaborator=no`。
- 个人线程的创建者:`amp-user=yes, workspace-member=no, collaborator=yes`。

当访客是 workspace 成员或协作者时,还会发送:
- `X-Amp-User-ID`
- `X-Amp-User-Display-Name`
- `X-Amp-User-Email`
- `X-Amp-User-URL`

非可见 ASCII 的值整体按 UTF-8 百分号编码。应用可据此做访问控制:公开访客没有身份,不会收到这些用户头。

#### B4. 友好主机名与自定义域名

- 默认主机名:`t-<thread>-p<port>.onamp.dev`。例如 `t-…-p3000.onamp.dev`。其中 `p<number>` 是 Amp portal 代理的标识,**不一定等于应用内部端口**。
- **托管友好主机名**:`<name>--<namespace>.onamp.dev`,例如 `foo--yourname.onamp.dev`。
- 设置方式:
  - `amp orb portal <port> --hostname <hostname>`。
  - 在 Portal 设置里改主机名。
  - 在 Puck 等 orb 外环境里,agent 用 `manage_amp` 的 topic `portals`、operation `set-hostname`。
- 自定义域名:
  - 先在个人或 workspace 的 **Domains** 设置页添加域名,按页面给的 DNS 记录配置,等待状态变为 **Active**。
  - 主机名只作用于**那一个端口**。
  - **个人域名只用于你用 `--hostname` 显式指定的主机名。** 只有 workspace 可以把某个域名设为 portal 的默认域名(Make Default for Portals),之后新 portal 用 `t-<thread>-p<port>.portal.example.com`。
- 复用主机名:归档线程后,自定义主机名**仍保持分配**。要换到别的线程的 portal,在新线程的 orb 里运行 `amp orb portal <port> --hostname <hostname> --take-over`。
- 迁移到 Box:`amp boxes deploy --domain <hostname> --take-over`。**portal 无法把主机名从 box 应用手里夺回。**
- 自定义域名上的 portal 通过**把域名提到 Active 之后 Amp 在该域名子域上提供服务**。

#### B5. 生命周期、限制

- 打开 portal 会唤醒已暂停的 orb。orb 在请求和服务活跃期间计费,空闲后自动暂停。归档线程也会暂停 orb。
- 在 orb 内部访问线程 portal URL 会直接走本地服务,跳过 Amp 的 portal 认证。这只绕过 portal 认证,不绕过应用自身认证。
- portal 承载 WebSocket(如 dev server 热更新),但 **Amp 服务器每小时可能多次替换,WebSocket 会在没有 close 帧的情况下断开(客户端报 1006),客户端必须重连。** 不要用 portal 承载必须长期保持的连接(如数据库隧道),改用 Tailscale。
- 跨 portal 主机的浏览器请求不受支持,因为 CORS 预检请求不携带 portal 认证。官方建议前端 dev server 代理 API。
- `.amp/portals/*.json` 是自动生成的状态,应加入 `.gitignore`。

#### B6. 服务声明(`.amp/services.yaml`)

| 字段 | 作用 |
|---|---|
| `command`* | 必填;经登录 shell 运行;有 portal 的服务必须监听 `$PORT` |
| `cwd` | 相对仓库根 |
| `port` | 1–65535;省略则 Amp 选空闲端口且重启后保持不变 |
| `env` | 字符串环境变量,可引用 `${services.<name>.publicURL}` |
| `health` | HTTP 路径;2xx/3xx 视为就绪 |
| `portal` | 主 portal 链接;`true` 表示 `/` 加服务名 |
| `portals[]` | 更多链接,每项需要 `url`、`title`;可用 `folder` 分组 |
| `review` | 是否注入评审小部件,默认 true |
| `platforms` | `linux`/`darwin` 非空列表 |

- 服务名:小写字母、数字、连字符,以字母或数字开头,最长 32 字符。
- Amp 管理的变量:`PORT`、`PUBLIC_URL`、`AMP_THREAD_ID`;不得在 `env` 里设置。有 portal 链接的服务才会收到 `PUBLIC_URL`。
- 命令:`amp orb services ensure [--json]`、`amp orb service list|status|logs|restart|stop|start`、`amp orb portal <port> [--title --description --hostname --take-over]`、`amp orb portal list`。
- 不要用 `nohup`/`setsid`/`&`/tmux 保活,应使用受监管的服务。
- 评审小部件可用响应头 `x-amp-review-widget: off` 关闭。
- `target="_amp"` 链接可让人从 Portal 面板跳出到 Amp 页面。

---

### C. 文件/附件发布为公开 URL

来源:https://ampcode.com/docs/markdown/orbs/files-and-attachments(lastModified 2026-10-30)。

#### C1. 默认私有 vs 发布

| 项 | 行为 |
|---|---|
| 普通附件 | 「remain private to the thread and are not automatically copied into the repository」,受 Amp 访问检查保护 |
| 发布 | 让 agent 把文件**发布到公开 URL**。「Only publish a file when it is safe for anyone with the URL to access it.」 |

- 触发方式:对话里要求,例如「Publish the finished demo video so I can share it outside this thread」;或要求「attach it to your final reply」(受线程保护的附件)。
- 附件列表:线程菜单 **Attachments**。
  - **Public** 徽标:该文件被 Amp 发布过。
  - **Temporary** 徽标:该文件在添加后**数小时**过期;具体小时数未找到。
  - 能删除线程的人可逐个或全部删除附件。
  - 附件是线程的**副本**,删除不影响 orb 文件系统里的文件。
- 上传限制:默认每文件 100 MB,有更高限额的用户 512 MB。
- **未找到:** 公开 URL 的具体形态、是否可撤销或有过期时间(除 Temporary 徽标外)、访问日志。URL 不可猜测也只是推断,文档没写。
- 用途举例:GitHub 不接受 Amp 的 App token 上传图片和视频(`gh ... --attach` 在 orb 里失败),所以 Amp 把媒体**发布到公开 URL 后嵌入 Markdown**。AGENTS.md 里若写了 `--attach`,应让 agent 改为发布(https://ampcode.com/docs/markdown/integrations/github,2026-11-09)。
- 项目文档(Ship Behavior)建议在 AGENTS.md 里写「embedding public artifact URLs of screenshots or videos in the PR description」。

---

### D. 制品(Artifact)的 Anyone with the Link

来源:https://ampcode.com/docs/markdown/threads(Artifacts 一节)、https://ampcode.com/news/artifacts(2026-11-02)。

#### D1. 数据模型 `[现行]`

- 制品是「a document attached to a thread」,属于单个线程,一个线程可以有多个,可分文件夹。
- 内容:标题、列表、清单、表格、代码、图片、交互视图。
- agent 通过 `artifact` 工具以 **Markdown** 读写。
- 每次保存生成新**修订版(revision)**。若有人在 agent 工作期间保存了更新的版本:仍匹配的编辑会被合并应用,否则 agent 的更新被拒绝,agent 须自行在新版本上合并。
- 其他线程的 agent(如子线程)在你本人也能编辑时可以编辑它,并给制品所属线程发一条消息说明改了什么、为什么。
- agent 可以把你能打开的制品**复制**到自己的线程,副本是不带评论的新制品。
- **只有制品自己的线程能删除它。** 删除线程会删除其全部制品。
- 单个制品最大 **2 MB**。
- 制品里加入的图片存在线程里,所以只有能打开线程的人能看到;通过 workspace 或公开链接进入的读者「see a broken image」。删除制品时图片保留到线程被删除。

#### D2. 分享 General Access 三档

| 选项 | 含义 |
|---|---|
| **Same Access as Thread**(默认) | 能打开线程的人;跟随线程可见性的后续变化 |
| **Workspace Members** | 线程所在 workspace 的成员,即使他们打不开线程;只能读不能编辑 `[团队功能·暂不做]` |
| **Anyone with the Link** | 任何人,**无需登录** |

- 可编辑者:能打开线程且属于其 workspace 的人,以及个人线程的所有者。
- 导出:**Export** 标签页可下载 Markdown 或 HTML,或打印成 PDF。
- **未找到:** 制品公开链接的 URL 形态(是否独立于线程 URL)。
- 与「线程公开」的差异:制品的 Anyone with the Link 明确写了不登录即可读;线程的 Unlisted 没有这句承诺。

---

### E. 公开只读代码访问、Amp Store、公开项目

#### E1. 公开只读代码访问(Amp 托管仓库)

来源:https://ampcode.com/docs/markdown/projects(lastModified 2026-11-05)。

- 项目设置 Overview 里的 **Public Read-Only Code Access**,**仅 Amp 托管仓库出现**:「lets anyone on the internet clone the repository and read its files. Threads stay private.」
- 即公开的是代码,线程仍保持私有。
- 项目页:`https://ampcode.com/@owner/project-name`。克隆:`amp clone owner/project-name` 或 `git clone https://ampcode.com/@owner/project-name`。**公开项目无需登录即可 `amp clone`**(clone 文档:「Public projects can be cloned without signing in」)。
- **只读 Git 凭据**(用于私有仓库给 CI/构建机):
  - 项目设置创建,有效期选 **7、30、90、180 天或 Never**。
  - 只显示一次 Authenticated Clone URL:`https://amp:<credential>@ampcode.com/@owner/project-name`。HTTP Basic,用户名被忽略。
  - 只能读这个项目的仓库,不能 push。删除即撤销。个人项目转到 workspace 时凭据被删除。
- Post-receive Webhook(Amp 托管仓库):每次成功 push 后向你选的 URL 发 JSON POST。
- 在 External API 里,project `repository.mode` 取 `amp-hosted | github | mapped`,仓库只读端点见前文第 1.5 节。

#### E2. 公开项目 / Amp Store

- **未找到「Amp Store」。** sitemap 和 llms.txt 里没有 store/市场页面。
- 最接近的公开面:
  - https://ampcode.com/modes 的「Modes & Models」集合。该页列出 Low/Medium/High/Ultra 与 Puck 的模型,并在全局插件文档中被引用为可导入的插件集合,命令形如 `amp plugins add --auto-update @amp/grok-46-mode`。
  - 插件/技能的 **share URL**:个人 Settings → Plugins/Skills → Share → 「Make available to your workspace」→ 复制 share URL;在 Slack 里会展开成详情卡片。这个共享范围是 **workspace**,不是互联网公开。`[团队功能·暂不做]`
- 公开用户资料:`ampcode.com/@<name>` 仍存在,但自 2026-06-02 起**不再显示任何线程**。2025-11 的 Social Coding(公开资料和线程)已被取代。`[已被取代→新形态]`

#### E3. Feedback Widget(公开站点上的反馈入口)

来源:https://ampcode.com/docs/markdown/feedback-widget(2026-11-06 上线,Close the Loop)。

- 加一个 script tag 到网站,团队成员用 `?amp` 打开站点并登录 Amp,指点页面元素写评论,发送后 Amp 在项目里启动线程,agent 在 orb 里改。
- 个人项目是 **widget key**(以 `pw_` 开头);workspace 项目是为项目创建的 Sign in with Amp 应用。
- **Allowed Origins**:必须 `https://`(`http://localhost`、`127.0.0.1`、`[::1]` 可带任意端口);不在列表的站点上面板不打开,评论也被拒绝。
- 通配来源规则:
  - 必须 `https://`。
  - `*` 必须是整个第一段标签,且只能有一个。
  - 主机至少 3 段标签。
  - 拒绝公共后缀(用 Public Suffix List 判断,如 `*.co.uk`、`*.github.io`)。
  - 拒绝 Amp 自己提供站点的域名(如 `*.onamp.dev`),但允许一个精确来源,例如你的 Box 应用。
  - 拒绝已激活的自定义域名(如 `*.portal.example.com`),但可以加某个 portal 的精确来源。
  - 不要把 portal 地址加进来:portal 页面有自己的评审按钮。
- **公开站点上的行为:** script 对**所有访客**加载,组件对访客隐藏,但「anyone who presses the keys can show the button」。**只有能在该项目启动线程的人才能发送评论**,其他人登录后看到 **Access Required**。个人项目只有你自己能发。官方建议:不想让脚本出现在公开站点,就只加到 staging。
- 授权寿命:登录 12 小时后需重新 **Continue with Amp**。
- 吊销:Security 设置 → Authorized Apps(workspace 项目);个人项目用 **Turn Off** 再 **Enable** 并更换 script tag。
- CLI:`amp projects feedback-widget get|enable|remove-origin|disable <project>`;加来源要经 UI 或 Amp,Amp 加之前要弹窗批准。
- 限制:agent 在 orb 里工作,看不到线上站点的数据。若别的站点用 iframe 嵌入你的站,面板不会打开。

---

### F. Box 公开应用与预览

来源:https://ampcode.com/docs/markdown/boxes(lastModified 2026-11-11,579 行全读)、https://ampcode.com/news/boxes(2026-11-10)。

#### F1. 概念

- Box 是常驻机器:Ubuntu 24.04,20 GB 磁盘,一台机器可跑多个应用。
- 每个应用有自己的 URL:`https://<app>--<namespace>.onamp.dev`,例如 `https://bug-tracker--acme.onamp.dev`。
- 规格(30 天价):

| 规格 | 机器 | 价格 |
|---|---|---|
| `b1.tiny` | 0.5 vCPU(突发到 2),2 GB | $18 |
| `b1.small`(默认) | 1 vCPU(突发到 2),4 GB | $30 |
| `b1.medium` | 2 vCPU,8 GB | $50 |
| `b1.large` | 4 vCPU,16 GB | $100 |

- 计费细节:停止的 box 不计费;`b1.medium/large` 新建需约 6 分钟等机器启动;创建 box 至少需要 $2 积分;每个 box 每月 100 GB 流量,80 GB 发邮件提醒,达到上限则停机到次月(UTC)。规格不可变更。

#### F2. 公开与私有

- **应用 URL 默认公开**,任何有 URL 的人都能打开。
- `amp boxes deploy --private`:仅 box 所有者可访问,或 workspace 成员(box 属于 workspace 时)。访客需先在浏览器里登录 Amp。**没有这种登录的请求(如 webhook)无法到达私有应用。**
- 之后的部署保持该设置,并保存在 `.amp/box-deploy.json` 对应 target 里;`--public` 改回公开。
- Amp 不会告诉你的应用谁打开了它;公开应用需要自己处理登录。
- 应用间隔离:每个应用的服务以独立 Linux 用户和 systemd 沙箱运行,不能 sudo,只能写自己的数据目录和家目录。**但这不是应用之间的安全边界**(共享 CPU/内存/磁盘/网络,可连其他应用端口),不信任的应用要用单独 box。
- Workspace 开关「Allow Public Box Apps」关闭时,所有应用变私有。`[团队功能·暂不做]`

#### F3. 自定义域名与接管

- 域名先在 Domains 设置页添加并达到 Active,再 `amp boxes deploy --domain portal.example.com`(可以是域名本身或其直接子域)。
- `onamp.dev` URL 会重定向到新主机名。私有应用、反馈组件和 Amp 内的应用面板在新主机名上同样工作。
- `--no-domain` 回到 onamp.dev。
- 把 portal 用的主机名搬到 box 应用:先不带 `--domain` 部署并检查,再带 `--take-over` 部署;主机名在部署开始时立即切换,用户不会遇到空应用。

#### F4. 预览(Previews in the Rail)

- 把 box 应用固定到导航栏后,鼠标悬停图标时可显示实时预览(触屏不显示)。
- 提供方式:在固定 URL 页面 `<head>` 放 `<meta name="amp-preview" content="v1; enabled; iframe" />`。
  - `v1` 是契约版本,后面是分号分隔的标志:`enabled` 打开预览,`iframe` 要求显示实时页面。未知标志忽略。
  - Amp 从其服务器**不带 cookie** 地请求并检查该标签,所以标签必须在服务器发给所有访客的 HTML 里,不能用脚本注入。只跟随同源重定向。每隔几分钟重新检查。
- 加载:`amp-preview=light|dark` 加在查询字符串里,框架尺寸 **384×240 像素**;应用应渲染不滚动、无导航、无登录的紧凑页面。
- 预览**不可交互**,不接收点击、焦点或键盘;点击卡片打开应用。
- 沙箱:可运行脚本,但不能开弹窗/对话框、提交表单、导航 Amp、使用摄像头或麦克风。
- 私有应用的预览用查看者的 box 登录打开;应用自己的 cookie 除非 `SameSite=None` 且分区,否则在 Amp 内的 iframe 里不会发送。
- 每次悬停都会加载,所以请求要快且无副作用。反馈组件不会出现在预览加载里。

#### F5. 滥用举报与封禁

- Box 应用必须遵守 Acceptable Use Policy(https://ampcode.com/terms/aup)。
- 举报:向 **security@ampcode.com** 发邮件并附 URL(`onamp.dev` 或自定义域名上的钓鱼、恶意软件等)。安全页同样写了此渠道。
- Amp 对违规 box 的处置:**封禁(block)**。被封 box 停止运行;其所有 URL(含应用、私有应用、自定义域名)显示「应用不可用」页面;磁盘和数据保留;即使到了计划开机时间也保持停止,所有者不能启动,直到 Amp 解除。Amp 发邮件给所有者(workspace box 则发给管理员和创建者)说明原因,box 页面也会显示。修好后回复邮件,解除后 box 重新启动。
- AUP 第 5 条使用限制:不得生成滥用/欺诈内容、儿童性剥削内容、非法内容、色情内容、暴力/仇恨/威胁内容、自动化的受法律保护事项判定(如住房、移民、金融资格、医疗建议)。第 6 条:合作产品的使用还要遵守对方的 AUP(如 Anthropic)。

---

### G. 公开入口的安全措施汇总

| 措施 | 线程 Unlisted | Portal 公开 | 已发布文件 | 制品公开 | 公开代码 | Box 应用 | 来源 |
|---|---|---|---|---|---|---|---|
| **不可猜测 URL** | `T-<uuid>` | 默认 `t-<thread>-p<port>.onamp.dev`;友好主机名可被人选 | 未找到 | 未找到 | 项目名可被猜 | 应用名可被选 | threads、portals、files |
| **过期** | 无 | **1h/3h/1d/7d** | 仅「Temporary」文件几小时过期 | 无 | 无(只读凭据 7/30/90/180 天/Never) | 无 | portals、files、projects |
| **撤销** | 改回 private(行为细节未找到) | **End Public Access** 立即终止 | 删除附件 | 改为 Same Access as Thread | 关开关;删凭据 | `--private` 重新部署;`undeploy` | 各自页面 |
| **范围限制** | 只读,不能多人 | 仅一个端口;不含线程与评审工具 | 单文件 | 单制品;图片不随公开链接 | 只读,线程保持私有 | 单应用 | portals、projects |
| **需用户确认** | — | agent 变公开/延长要弹窗批准 | 官方警告「only publish when safe」 | — | — | — | portals |
| **全局开关** | workspace 级 `[团队功能·暂不做]` | provider 级 Public Portals;workspace 级 | — | Allow Public Artifact Uploads | — | Allow Public Box Apps | portals、self-hosted、boxes |
| **举报/审核** | AUP | AUP | AUP | AUP | AUP | 邮件举报;封禁整个 box | boxes、security |
| **内容安全** | 密钥脱敏(best-effort);不再做可发现公开线程 | — | 官方提示 | — | — | — | security、end-of-public-threads |

补充:
- **没有内容审查或举报按钮的文档。** 未找到线程页或 portal 页上的「举报」入口。唯一明文渠道是 security@ampcode.com 邮件和 AUP 条款。
- `https://ampcode.com/security`(2026-11-10)里说:不可被发现的公开线程之外,所有这些都是 SOC 2 Type II 范围内的服务,每年做渗透测试;应用审计日志保留至少 30 天。
- `*.onamp.dev` 是公开入口所在域(portal、box 应用),客户端安全页要求出站白名单放行 `*.onamp.dev`、`tunnel.ampcode.com`。Terminal 和 Desktop 也走 Amp Tunnel。
- `/api/revoke`:泄露的 Amp 访问令牌可 `POST https://ampcode.com/api/revoke`,`Authorization: Bearer sgamp_…`,服务端作废并生成新令牌。

---

### H. 对 Oryn 的启示(公开分享部分,每条 1 行)

1. 公开线程只做 **Unlisted + 不可猜测 ID**,不做可发现/可搜索的公开线程;Amp 已因「线程里残留敏感文件片段无法人工审查」把它删掉了(2026-06-02)。
2. 线程可见性枚举最小化为 `private | unlisted`,单用户阶段不需要 workspace/group。
3. Unlisted 线程**强制只读、强制关闭多人**,在数据模型上互斥,别靠 UI 约束。
4. 公开线程页该渲染什么(工具调用、diff、思考块)Amp 文档没说清,Oryn 需要自己定:建议默认隐藏 thinking、折叠工具输出、diff 只读,并允许创建者按分享粒度选择。
5. 提供 `.md` 导出(URL 后缀 `.md`)作为公开线程的机器可读形式,JSON 完整导出只给创建者。
6. 分享链接应可一键撤销,并写清「撤销后旧链接返回 404」这类行为;Amp 文档没写,Oryn 要补上。
7. Portal 公开必须有时限(1h/3h/1d/7d),过期自动失效;并且**按端口**授权,不给线程或评审工具权限。
8. 让 agent 自己把 portal 变公开,必须有人工批准弹窗(`requireHuman`);CLI 不能提升公开级别,只能查看。
9. 自托管 orb 默认关闭公开 portal:公开 portal 等于一条进入你网络的匿名隧道,Oryn 的自有 orb provider 应有同样的默认关闭开关。
10. 给应用传递访客身份用一个头(`X-Amp-Authenticated: amp-user=…, workspace-member=…, collaborator=…`),公开访客不带用户信息;Oryn 可简化为 `owner=yes|no`。
11. 友好主机名 `<name>--<namespace>.<base>` 加自定义域名:域名需验证 Active 后才可用;归档线程后主机名仍保持分配,需要 `--take-over` 才能转移。
12. portal 的 WebSocket 会在服务端发布时断开(1006),Oryn 的 portal 层要么支持无损重连,要么在文档里明确不承诺长连接。
13. 发布文件为公开 URL 与普通附件必须是两种状态,用 **Public** 徽标标出,还有 **Temporary** 过期徽标;公开 URL 的形态和撤销 Amp 没写,Oryn 需要设计成「不可猜测 + 可撤销 + 可过期」。
14. 制品的三档 General Access 里只留 `Same Access as Thread` 与 `Anyone with the Link`;制品内图片存在线程里,公开读者看不到——Oryn 应在公开导出时内联或另发图片。
15. 公开代码只读访问只对「自己托管的仓库」提供,并配以只读 Git 凭据(有效期 7/30/90/180/永久,只显示一次,可撤销);Oryn 如果自带 Git 托管,可直接照搬。
16. Box 应用默认公开,私有靠 Amp 登录门;公开应用自己负责登录,平台不传递用户身份——Oryn 要选择一个并写明。
17. Box 预览的契约(`<meta name="amp-preview" content="v1; enabled; iframe">`、无 cookie 拉取、384×240、不可交互、沙箱)可作为 Oryn 导航栏小部件的参考,尤其是「必须写在服务器返回的 HTML 里而非脚本」。
18. 对被举报的公开应用,处置用「封禁整个 box」而不是删内容:停机、保留数据、所有 URL 显示不可用页、通知所有者、修复后解封;Oryn 的单用户版可简化为本地「一键下线所有公开入口」。
19. 反馈组件允许来源要严格:必须 https、不接受公共后缀与平台自有域名的通配、不接受自己的 portal 域;值得 Oryn 复制这套规则。
20. 所有公开入口的开关与时效集中在一处列表(类似 `amp orb portal list`),让用户随时看到「现在有哪些东西公开、到何时」。

---

### I. 本节实际读过的页面与行号

| 页面 | 范围 |
|---|---|
| https://ampcode.com/docs/markdown/threads | 1–314 全读(Share a Thread、Export、Artifacts 完整) |
| https://ampcode.com/docs/markdown/orbs/portals | 1–639 全读 |
| https://ampcode.com/docs/markdown/orbs/files-and-attachments | 全读 |
| https://ampcode.com/docs/markdown/boxes | 1–579 全读 |
| https://ampcode.com/docs/markdown/projects | 1–300 / 313(Permissions 一节余下约 13 行未读) |
| https://ampcode.com/docs/markdown/enterprise/workspace-thread-visibility-controls | 全读 |
| https://ampcode.com/docs/markdown/feedback-widget | 通过 grep 读到约 1–216 行的关键段落(非逐行全读) |
| https://ampcode.com/docs/markdown/orbs/self-hosted | grep(Public Portals 一节完整) |
| https://ampcode.com/docs/markdown/cli/runners | grep(Portals on Your Runner、Share a Runner 完整) |
| https://ampcode.com/docs/markdown/customize/global-plugins-and-skills、plugins | grep(share URL 相关) |
| https://ampcode.com/security | 1–300 / 375(余下为漏洞赏金细节) |
| https://ampcode.com/terms/aup | grep(Use Restrictions 条款) |
| https://ampcode.com/modes | 全读 |
| https://ampcode.com/news/end-of-public-threads、portals、friendly-urls-for-sharing-orbs、artifacts | 全读 |
| https://ampcode.com/news/runner-portals | grep |
| https://ampcode.com/news/attach-anything | 仅 grep,正文未读到 |

**未读/未找到清单(本节)**
- 未登录访客在 Unlisted 线程页看到的具体内容:工具调用、diff、是否实时更新、撤销后的行为。文档没写;如要精确规格,需要在真实站点上打开一个 Unlisted 线程页取证。
- 已发布文件的公开 URL 形态、是否可撤销。
- 制品公开链接的 URL 形态。
- Amp Store:sitemap 与 llms.txt 均无。
- 内容举报入口(页面上的按钮):未找到,只有邮件渠道。
- `/terms`、`/privacy-policy`:未读。
- `/docs/orbs/handling-secrets`、`/docs/enterprise/allowed-models` 等:未读(与公开分享无直接关系)。

### Critical Files for Implementation

- `https://ampcode.com/docs/markdown/threads` — 线程可见性(Unlisted)、`.md` 导出、制品三档 General Access 的权威描述。
- `https://ampcode.com/docs/markdown/orbs/portals` — portal 公开时限、按端口授权、主机名与自定义域名、访客身份头、`.amp/services.yaml`。
- `https://ampcode.com/docs/markdown/boxes` — Box 公开/私有应用、自定义域名与接管、预览契约、滥用封禁流程。
- `https://ampcode.com/docs/markdown/orbs/files-and-attachments` — 附件私有与发布为公开 URL 的区分、Public/Temporary 徽标。
- `https://ampcode.com/news/end-of-public-threads` — 取消可发现公开线程的设计理由,决定 Oryn 公开分享的安全边界。
