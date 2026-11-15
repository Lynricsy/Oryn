# M13 — Agent 配置 GUI、头像体系、首屏全局助手与助手文件库：实施计划

Goal：把 omp 的技能、子 agent、模型角色、提示词与上下文、命令/规则/提示片段、扩展/hook/工具做成图形化配置，改完自动下发到运行中的 orb；role 与 agent 都能设头像，并用头像加动效展示子 agent 的状态；首屏输入框可以直接和全局助手对话；全局助手获得文件库与读写文件能力。

Approach：控制面 omp-home 是唯一事实来源，GUI 在前端用 `yaml` Document API 改文件，经现有 `agent-config` 文件接口写入。启停沿用 Oryn Core 的 `disabledExtensions` 键。新增一条"只写 omp-home"的下发通道，用于立即应用。模型目录由控制面用钉住的 Core 一次性运行 `omp models --json` 生成，不再依赖 orb。文件库存在控制面，同步进协调宿主沙箱，供 omp 自带的 `read`/`write` 使用。

Requirements：本文「需求」一节。设计决策来自 2026-11-15 的 grilling 讨论，记录见 `.agent-logs/0073`、`0074`、`0076`、`0077`、`0081`。

Rollback point：每个阶段都能单独上线、单独停下。三次数据库迁移只增不改（新增表与列），回退时代码不再读写这些表即可，不需要降级迁移。阶段 6 的"立即应用"可以单独关掉，关掉后回到"orb 新建/恢复时下发"的现状。

> 本计划推翻 `docs/plan-m1-m2.md`「范围约束」中"omp 的配置不做 GUI"一条（用户于 2026-11-15 明确改变）。其余约束不变。

## 需求

以下每条都能由第三方判断真假。

**模型目录**
- R1. 不打开任何线程、不唤醒任何 orb，就能在设置页的模型角色编辑器、首屏 role 胶囊的"指定模型…"、线程内模型对话框三处看到同一份模型列表。列表包含 `models.yml` 自定义 provider 的模型和内置 provider 中有密钥的模型，每个模型带思考档位。
- R2. 修改 `models.yml` 或 `config.yml` 后再打开模型列表，列表反映新配置。列表在 24 小时后过期，"刷新"按钮能强制重拉。

**身份与头像**
- R3. "Agent 身份"在界面上显示为"默认 Agent 身份"。全局助手的名字或头像留空时显示"全局助手"和 sparkle 标记，不再跟随默认 Agent 身份；设置页没有"跟随 Agent 身份"选项。
- R4. 助手对话（`kind=assistant`）的每个回合头都显示全局助手的名字和头像，即使切到某个 role 也一样。
- R5. 普通对话回合头的优先级：切到的 role 有自定义头像就用它；否则如果是内置 role，用该 role 的默认 Nerd 字形；否则用默认 Agent 身份。
- R6. 以下 role 都能设头像：`cycleOrder`、`modelRoles` 的键、内置 role（default、smol、slow、vision、plan、commit、tiny、memory、task、advisor）。不在 `cycleOrder` 里的 role 也能设。
- R7. 以下 agent 都能设头像：内置的 task、sonic、scout、reviewer、security-reviewer，`advisor` 类，以及 omp-home `agents/*.md` 里的每个 agent。没设头像的内置 agent 显示该 agent 的默认字形，没设头像的自定义 agent 显示通用机器人字形。
- R8. role 头像在「Agent › 模型角色」页编辑，agent 头像在「Agent › 子 agent」页编辑。「身份」页只保留默认 Agent 身份、全局助手、用户头像和"新对话默认对象"。
- R9. 公开分享页里，task 工具卡片中出现的 agent 如果设了自定义头像，就显示该头像。没出现在分享范围内的 agent，其头像不出网。

**子 agent 界面**
- R10. 子 agent 总览每行以 agent 头像开头，不再有状态点。头像按状态表现：

  | 状态 | 表现 |
  |---|---|
  | 运行中 | 外圈旋转光弧 |
  | 排队 | 虚线外圈呼吸 |
  | 完成 | 右下角小勾 |
  | 失败、中止 | 头像去饱和，右下角叉 |
  | 只剩保存的转录 | 头像原色，配静止虚线环和存档角标 |

  每种状态都有 tooltip 文字。开启"减少动态效果"时外圈不转，改为实线高亮。总览标题栏的运行汇总改用同一套光环。
- R11. 子 agent 详情弹窗：第一行是头像（带 R10 的动效）、类型 badge、短名（等宽字体），右侧显示所用 role 和模型；第二行是 `description`。不再有灰色说明条。"已保存的转录"与"不是同 id 的在线那一个"两种情况由头像 tooltip 说明，这两种情况下控制按钮仍禁用。
- R12. 转录里的 task 工具卡片显示 agent 头像。

**首屏输入框**
- R13. 首屏不再有模型胶囊和思考强度按钮，改为一个 role 胶囊，显示当前对象的头像和名字。点开后，桌面端是锚在胶囊上的弹层，手机端是底部抽屉，内容依次为：全局助手、`cycleOrder` 里的各 role（头像、名字、selector）、"指定模型…"（模型目录与自由输入）、思考强度。
- R14. 选中全局助手时，执行位置、项目、浏览器 profile 三个胶囊以动画收起；切回普通 role 时以动画展开。附件按钮始终可见。
- R15. 选全局助手与选普通 role 时，输入框的视觉效果明显不同：全局助手是旋转流光描边，背后色场换成助手色相；普通 role 是静态光晕，颜色用该 role 的色相。"减少动态效果"开启时流光改为静止描边。
- R16. 选全局助手后发送，会新建一条助手对话，首条消息、附件和所选模型都带上，并跳转到 `/assistant/:id`。选普通 role 后发送，新线程的首条消息由该 role 的模型回答，转录回合头显示该 role。
- R17. 进入任何线程后，输入框里都没有"全局助手"选项。
- R18. 「身份」页可以设"新对话默认对象"（全局助手或 default role），存在服务端，换设备也一致。每次打开首屏都按它预选，不记上一次的选择。

**全局助手文件库**
- R19. 在助手对话里上传的附件都进入文件库，默认属于该对话。文件库面板可以把文件切换为"库内共享"。
- R20. 助手能用 omp 的 `read` 读取所属对话的文件和共享文件，支持 PDF、docx、pptx、xlsx、epub、图片、压缩包、SQLite、notebook。
- R21. 助手能用 `write` 在自己的输出目录里写文件，再用宿主工具把它存进文件库。存进来的文件出现在面板里，可以下载。
- R22. 助手用 `create_thread` 开线程时，可以指定带上哪些库文件。新线程首条消息的附件条里有这些文件，orb 里能读到。
- R23. orb 里的 agent 执行 `oryn library put <文件>` 后，文件以"库内共享"的状态进入文件库，所有助手对话都能读。
- R24. 文件库面板在全局助手页，可以列表、预览、下载、删除、切换共享/私有，并查看文件来源（所属对话、来源线程）。文件库没有总量上限。
- R25. 助手 agent 每次启动前，控制面都会重写它的系统提示、overlay 和下发的配置文件。

**Agent 配置 GUI**
- R26. 设置页新增"Agent"分组，下分六个子页：技能、子 agent、模型角色、提示词与上下文、命令/规则/提示片段、扩展/hook/工具。原始文件浏览器保留为"高级"入口。
- R27. 每个子页都能新建、编辑、删除对应文件，并能启停条目：技能、命令、规则、提示片段、instruction、hook、扩展模块、自定义工具、单个上下文文件写 `disabledExtensions`，子 agent 写 `task.disabledAgents`。编辑 `config.yml` 后原有注释保留。
- R28. 内置 agent 在子 agent 页只读展示名字、简介和头像，只有头像可改。
- R29. 保存后，配置在 10 秒内写到所有 `ready` 状态的 orb。空闲的会话随即生效：技能、命令、规则、上下文文件、MCP 都能热生效，`config.yml` 由 omp 自带的文件监听生效。正在跑回合的会话，在回合结束后生效。
- R30. 只有重启才能生效的改动，页面顶部显示"N 个会话需重启后生效"，并给一个"重启这些会话"按钮。这类改动包括：hook、扩展模块、自定义工具、`SYSTEM.md`、`APPEND_SYSTEM.md`、标记为需重启的设置项。点按钮后，各会话空闲时停掉 omp 进程，下次发消息时带着原会话自动拉起；正在跑的回合不受影响。
- R31. 各子页都标明"不作用于全局助手"。`config.yml` 和 `models.yml` 例外，这两个文件也会下发给全局助手。

## 非目标

- MCP 配置（`mcp.json`）、通用设置表单（`config.yml` 其余约 420 项）、LSP、`secrets.yml`、插件市场：本里程碑不做，留给后续里程碑。
- 仓库内项目级 `.omp/` 配置：属于仓库内容，经提交修改，不归设置页管。
- 覆盖内置 agent：需要时，用户自己新建同名 `agents/<name>.md`。
- 让全局助手使用技能、子 agent、命令、上下文文件、扩展。
- 助手对话之间的安全隔离：所有助手对话共用一个沙箱，文件的"所属对话"只是可见性边界。
- 普通线程的附件进入文件库（R19 只针对助手对话）。

## 已定决策与被否方案

| 决策 | 选定 | 被否方案及理由 |
|---|---|---|
| 配置事实来源 | 控制面 omp-home | orb 内的 `set_setting`、`manage_skill` 等 RPC：写的是 orb 本地文件；改到受托管路径会在下次 setup/resume 时被覆盖；其他本地改动不会写回控制面，不会同步到别的 orb，orb 重建即丢失 |
| 启停写法 | `disabledExtensions`（`skill:`、`hook:<type>:<tool>:<name>`、`extension-module:`、`tool:`、`rule:`、`prompt:`、`instruction:`、`slash-command:`、`mcp:`、`context-file:<level>:<basename>`）；子 agent 用 `task.disabledAgents` | 自定一套键：会和 Core 的 `rpc-actions.ts` 及 `capability/*.ts` 分叉 |
| YAML 读写 | 前端 `yaml` Document API，保留注释，走通用文件接口与 etag | 后端新增结构化接口：重复造一套 YAML 编辑能力 |
| 模型目录 | 控制面一次性运行 `omp models --json --kind all` | 常驻目录进程：多一个常驻进程，还要改协调宿主"单实例"的生命周期；打包静态目录：看不到动态发现的模型 |
| 头像默认值 | Nerd Font 字形，作为前端常量 | 新增 SVG 图标头像类型：需要第二套渲染路径 |
| agent 头像存储 | 新表 `agent_avatars` | 写进 frontmatter：128KB 图片会随配置下发到每个 orb，内置 agent 也没有文件可写 |
| 子 agent 显示的头像 | 按 agent 定义取 | 按实际所用 role 取：同一个 scout 会随模型变脸 |
| 分享页 agent 头像 | 按名字明文对应 | role 那种不透明键：task 卡片本来就公开显示 agent 名字，加密没有意义 |
| 生效方式 | 保存即下发；需要重启的会话懒重启 | 显式"立即应用"按钮：用户已否 |
| 文件库读取 | 沙箱内 omp `read` | 控制面宿主工具：只能返回文本，PDF 等格式要另写解析 |
| 助手写文件 | omp `write` 加宿主工具回传 | 只用宿主工具提交文本：用户要求能写任意文件 |
| 阶段顺序 | 文件库排在首屏胶囊之前 | 用户最初的顺序是首屏在前，但首屏选全局助手时要保留附件，附件依赖文件库；先做首屏就只能先上一个不可用的附件按钮 |

## 假设

- A1. 控制面能为模型目录进程起一个本机回环 egress 监听，复用 `internal/egress` 按策略注入密钥。若不成立：步骤 1.1 改为把真实密钥渲染进临时 HOME（0700，用完即删），并在本节记录这一风险。受影响步骤：1.1、1.2。
- A2. `omp models --json --kind all` 的输出结构（`cli/models-cli.ts:68-86`）在钉住的 Core 版本内稳定。若不成立：步骤 1.2 的解析改为宽松解析。受影响步骤：1.2。
- A3. 协调宿主的 omp 加 `--tools read,write` 后，内置工具只有 read 和 write，不会自动追加其他工具。若不成立：步骤 4.3 在 overlay 里显式关掉多出来的工具。受影响步骤：4.3。
- A4. 在 omp 会话空闲时执行 `reload_plugins`，会刷新技能、斜杠命令、MCP，并重建系统提示（含 AGENTS.md 与规则）（`rpc-session-actions.ts:83-125`，`session-tools.ts:2316-2323`）。若不成立：步骤 6.3 把这些改动也归为"需重启"。受影响步骤：6.3。

## 全局约束

- 每个步骤完成后，按仓库规则原子提交：`<type>(<scope>): <gitmoji> <subject>`，正文附 `Co-authored-by: Wine Fox <fox@ling.plus>`。
- 改动 `schema/openapi.yaml` 或 SQL 的步骤，验收里都要包括 `make check-gen` 和 `make lint-spec` 通过。
- 前端每个步骤的验收都要包括 `cd web && bun run check` 通过；新增文案同时写入 `web/src/i18n/locales/zh-CN.json` 与 `en.json`，`locales.test.ts` 通过。
- 动效全部用 CSS keyframes，不引入动画库。必须遵守 `index.css` 中全局的 reduced-motion 规则，任何逻辑都不能依赖 `transitionend`。
- 颜色只用现有令牌（`--role-h-*`、`--accent-*` 等）。按 `docs/design/web-guidelines.md §2`，accent 色只留给需要用户处理的事项。
- 协议变化写进 `docs/protocol.md` 对应章节；阶段完成后更新 README 的功能列表。

---

## 阶段 1：中央模型目录

### 步骤 1.1：探查 egress 能否给控制面本机进程用（探查步骤，不产出功能）
Depends on：无。这是本计划风险最高的未知（规则 18），所以最先确认。
Files：读 `internal/egress/egress.go`、`internal/orbs/egress.go`；把结论写进本文「假设」A1。
Do：确认 `egress` 包能否以 `net.Listener` 加 `egress.Policy` 的方式在控制面本机提供代理（现在只经 link 流服务 orb）。手动渲染一个只含占位密钥的临时 HOME，跑 `build/core/x64/omp models --json --kind all`，观察经代理的发现请求是否成功。
Accept：在本文 A1 下写明"成立"或"不成立 + 采用的替代方案"，并附上手动运行时 `omp models --json` 输出的模型数量。

### 步骤 1.2：Go 包 `internal/modelcat`
Depends on：1.1 的结论。
Files：新建 `internal/modelcat/modelcat.go`、`modelcat_test.go`。修改 `cmd/orynd`（装配）、`internal/config/config.go`（目录缓存路径，默认 `<data_dir>/model-catalog/`）。
Produces：
- `type Model struct { Provider, ID, Selector, Name, Kind string; ContextWindow, MaxTokens int; Reasoning bool; Thinking []string; Input []string; Cost *Cost }`
- `type Result struct { Models []Model; FetchedAt time.Time; Stale bool; Warnings []string }`
- `func New(cfg Config, home *agentcfg.Home, vault *secrets.Vault) *Catalog`
- `func (c *Catalog) Get(ctx context.Context, force bool) (Result, error)`
- `func (c *Catalog) Invalidate()`
Do：
- 渲染 omp-home 中的 `models.yml` 和 `config.yml`（`agentcfg.RenderAt`，egress 地址取 1.1 的监听）到临时 HOME。
- 运行 `<coordinator.omp_path> models --json --kind all`，环境变量从零构造：`HOME`、`PI_CODING_AGENT_DIR`。
- stderr 收进 `Warnings`。
- 缓存键为 `sha256(渲染后的 models.yml ‖ config.yml ‖ core.Pin()["linux-x64"])`，TTL 24h，用 singleflight 合并并发请求。
- 运行失败时返回上一份结果并标 `Stale=true`。
Accept：`go test ./internal/modelcat/...` 通过，用例覆盖：
- 用假 omp 脚本输出固定 JSON 时能解析；
- 缓存键随 models.yml 变化；
- 并发 10 次调用只运行 1 次；
- 运行失败时返回旧结果并标 Stale。

### 步骤 1.3：REST `GET /api/v1/models/catalog`
Depends on：1.2（`Catalog.Get`）。
Files：`schema/openapi.yaml`（operationId `getModelCatalog`，query `refresh: boolean`，schema `ModelCatalog`、`CatalogModel`），`internal/api/modelcat.go`，`internal/api/agentconfig.go`。
Do：
- 新增 handler。
- `PutAgentConfigFile`、`DeleteAgentConfigFile`、`ImportAgentConfig` 改动了 `models.yml` 或 `config.yml` 时，调用 `Catalog.Invalidate()`。
- 鉴权与其它 settings 接口一致。
Accept：`make check-gen && make lint-spec && go test ./internal/api/...` 通过；新用例断言写入 `models.yml` 后下一次 `GET` 触发重新运行。

### 步骤 1.4：前端改用中央目录
Depends on：1.3（`getModelCatalog`）。
Files：
- 新建 `web/src/api/model-catalog-queries.ts`（`useModelCatalog()`、`modelCatalogKeys`）；
- 修改 `components/thread/Dialogs.tsx`（ModelDialog）、`components/composer/Composer.tsx`（`/model`）、`components/composer/RoleTrack.tsx`（`MODELS_FRAME`）、`components/ui/ModelSelect.tsx`、`state/model-caps.ts`、`mock/server.ts`；
- 修改 `pages/SettingsPage.tsx`（保存 config 后让目录失效）。
Do：
- 三处模型列表改读 `useModelCatalog`，删除线程级 `get_available_models` 查询。
- 思考档位改读目录的 `thinking` 字段，`model-caps` 的 localStorage 学习逻辑一并删除。
- 列表页脚给出"刷新"（调用 `refresh=true`）。
Accept：`cd web && bun run check && bun x vitest run src/components/thread src/components/composer src/components/ui` 通过；`rg -n get_available_models web/src --glob '!**/rpc-types.d.ts' --glob '!**/mock/**'` 无输出。

---

## 阶段 2：头像体系与身份脱钩

### 步骤 2.1：存储与 role 范围
Depends on：无。阶段 2 与阶段 1 不共享文件，可并行。
Files：
- 新建 `internal/store/migrations/0024_agent_avatars.sql`：`agent_avatars(name TEXT PK, avatar_icon, avatar_image, updated_at, CHECK((avatar_icon IS NULL) <> (avatar_image IS NULL)))`；`owner` 加列 `new_thread_target TEXT NOT NULL DEFAULT 'role'`。
- 修改 `internal/store/queries/avatar_identity.sql`、`internal/agentcfg/roles.go`。
Produces：
- `agentcfg.BuiltinRoles = []string{"default","smol","slow","vision","plan","commit","tiny","memory","task","advisor"}`
- `func (h *Home) KnownRoles() (roles []KnownRole, configErr error)`：返回 `cycleOrder`、`modelRoles` 的键、`BuiltinRoles` 的并集，每项带 `InCycle`、`Name`、`Selector`。
- `func (h *Home) Agents() ([]AgentDef, error)`：读取 `agents/*.md` frontmatter 的 `name` 与 `description`。只读 frontmatter，解析失败的文件作为警告返回。
- `agentcfg.BundledAgents = []string{"task","sonic","scout","reviewer","security-reviewer","advisor"}`
Accept：`make check-gen && go test ./internal/agentcfg/... ./internal/store/...` 通过。用例覆盖：
- `KnownRoles` 的并集与去重；
- frontmatter 缺 `name` 时报警告；
- 迁移后 `owner.new_thread_target` 默认为 `role`。

### 步骤 2.2：头像与偏好 API
Depends on：2.1。
Files：`schema/openapi.yaml`、`internal/api/role_identity.go`、新建 `internal/api/agent_identity_list.go`、`internal/api/composer_prefs.go`。
Produces：
- `listRoleIdentities` 改为返回 `KnownRoles`。`PutRoleIdentity` 删除"必须在 cycleOrder 内"的检查，改为"必须在 `KnownRoles` 内"。
- `GET/PUT /api/v1/settings/agent-avatars`（`listAgentAvatars`、`putAgentAvatar`）：`AgentAvatarList{agents: AgentAvatar{name, source: bundled|user|orphan, description?, avatarIcon?, avatarImage?}}`。头像格式校验复用 `validAvatar`。
- `GET/PUT /api/v1/settings/composer`（`getComposerPrefs`、`putComposerPrefs`）：`ComposerPrefs{defaultTarget: "assistant"|"role"}`。
Accept：`make check-gen && make lint-spec && go test ./internal/api/...` 通过。新用例：
- 给不在 cycleOrder 但在 `modelRoles` 里的 role 设头像返回 200；
- 给 `scout` 设头像后列表 `source=bundled` 且带头像；
- 删掉 `agents/foo.md` 后 foo 显示为 `orphan`；
- ComposerPrefs 往返读写一致。

### 步骤 2.3：前端身份解析
Depends on：2.2。
Files：
- 新建 `web/src/lib/avatar-defaults.ts`：`ROLE_DEFAULT_GLYPHS: Record<BuiltinRole, string>`、`AGENT_DEFAULT_GLYPHS: Record<string, string>`、`GENERIC_AGENT_GLYPH`。
- 修改 `web/src/api/identity-queries.ts`（`effectiveAssistant` 不再回落到 agent 身份；新增 `useAgentAvatars`、`saveAgentAvatar`、`useComposerPrefs`）、`web/src/lib/turn-role.ts`、`components/transcript/Transcript.tsx`（`useTurnIdentities`）、`components/ui/IdentityAvatar.tsx`。
Do：
- 助手线程：`resolveTurnRole` 不再参与，回合头始终显示助手身份（R4）。
- 普通线程：faces 包含内置 role 的默认字形，优先级按 R5。
- 新增 `AgentAvatar` 解析：`useAgentFace(name)`，依次取自定义头像、内置默认字形、`GENERIC_AGENT_GLYPH`。
Accept：`cd web && bun run check && bun x vitest run src/lib src/components/transcript src/api` 通过。新用例：
- 助手线程切到有头像的 role 后，回合头仍是助手身份；
- 普通线程切到没设头像的 `smol` 时，回合头是 smol 的默认字形；
- 自定义 role 没设头像时回合头是默认 Agent 身份；
- 未知 agent 显示通用字形。

### 步骤 2.4：身份设置页改版
Depends on：2.3。
Files：`web/src/pages/settings/IdentitySettings.tsx`（及其测试），zh-CN/en 文案。
Do：
- 标题改为"默认 Agent 身份"。
- 全局助手卡去掉"跟随 Agent 身份"，空值预览为"全局助手"加 sparkle。
- 删除 `RoleAvatarsCard`，它在 6.6 移到模型角色页。在 6.6 落地之前，本页保留一个指向「Agent › 模型角色」的链接卡。
- 新增"新对话默认对象"单选（`useComposerPrefs`）。
Accept：`cd web && bun x vitest run src/pages/settings/IdentitySettings.test.tsx` 通过，断言页面不含 `followAgent` 文案，且默认对象单选能写回 `putComposerPrefs`。

### 步骤 2.5：分享页 agent 头像
Depends on：2.2。
Files：`internal/share/identity.go`、`internal/transcript`（新增 `UsedAgents(entries)`：从主文件中 `task` toolCall 的参数里取出 agent 名）、`schema/openapi.yaml`（`PublicShareIdentity.agents: PublicAgentIdentity[]{name, avatarIcon?, avatarImage?}`）、`web/src/share/identity.ts`、`web/src/pages/SharePage.tsx`。
Do：只下发分享范围内实际用到、且有自定义头像的 agent，按名字明文对应。快照分享照常在创建时固化。
Accept：`make check-gen && go test ./internal/share/... ./internal/transcript/...` 通过。用例：
- 分享里用过 `scout`（有头像）和 `reviewer`（无头像），结果只含 scout；
- 未用到的 `foo`（有头像）不出现。

---

## 阶段 3：子 agent 头像与动效

### 步骤 3.1：`AgentStatusAvatar` 组件
Depends on：2.3（`useAgentFace`）。
Files：新建 `web/src/components/ui/AgentStatusAvatar.tsx` 与测试；`web/src/index.css` 新增 `@keyframes agent-ring-spin`、`agent-ring-breathe` 及 `.agent-ring-*` 类。
Produces：
- `type AgentVisualState = "running" | "pending" | "done" | "failed" | "aborted" | "archived" | "unknown"`
- `<AgentStatusAvatar agent={string} state={AgentVisualState} size={number} title={string} />`
Do：按 R10 实现各状态。存档状态保持原色，配静止虚线环和存档角标。`title` 作为 tooltip 与 `aria-label`。reduced-motion 下外圈改为实线。
Accept：`cd web && bun x vitest run src/components/ui/AgentStatusAvatar.test.tsx` 通过，每种状态断言 `data-state` 与角标元素，archived 状态下头像元素没有灰度类。

### 步骤 3.2：状态映射
Depends on：3.1（`AgentVisualState`）。
Files：`web/src/state/subagent-tree.ts`（及测试）、`state/thread-reducer.ts`。
Produces：`function subagentVisualState(row: SubagentRow): AgentVisualState`。`SubagentRow` 增加 `progress?: { status?: string; modelRole?: string; resolvedModel?: string }`，归档行增加 `archiveReason?: "offline" | "superseded"`。
Do：
- `started`/`running` 映射为 running；
- `progress.status === "pending"` 映射为 pending；
- `completed`/`done` 映射为 done；
- `failed`/`error` 映射为 failed；
- `aborted` 映射为 aborted；
- 只在归档里出现，或 `!current`，映射为 archived。
Accept：`cd web && bun x vitest run src/state/subagent-tree.test.ts` 通过，覆盖上面每个分支。

### 步骤 3.3：总览与详情改版
Depends on：3.2。
Files：`web/src/components/thread/InfoPanel.tsx`（`SubagentSection`、`SubagentBranch`、标题汇总）、`web/src/components/thread/Dialogs.tsx`（`SubagentDialog`、`SubagentLink`）及相关测试，文案。
Do：
- 行首 `StatusDot` 换成 `AgentStatusAvatar`。缩进辅助线的 `DOT_X` 按头像中心重新计算。
- 详情按 R11 布局：右侧是 `progress.modelRole` 与 `resolvedModel` 的短名。
- 删除 `subagent.offline`/`subagent.archived` 的 Callout，两段文案改作头像 tooltip。
Accept：`cd web && bun x vitest run src/components/thread` 通过。`SubagentDialog.test.tsx` 断言：
- 不存在 `role="status"` 的 Callout；
- 第一行含类型 badge 与短名；
- 归档态头像的 `title` 含"已保存的转录"。

### 步骤 3.4：task 工具卡片
Depends on：3.1、2.5（分享页数据）。
Files：`web/src/tool-render/tools/task.tsx` 及测试。
Do：`Summary`、`Body`、`AgentResult` 中的 agent badge 前加 `AgentStatusAvatar`（结果卡按结果状态取状态，参数卡用 `unknown`）。分享模式从 `PublicShareIdentity.agents` 取自定义头像。
Accept：`cd web && bun x vitest run src/tool-render` 通过，用例断言分享上下文里设过头像的 agent 渲染为 `<img data-avatar="image">`。

---

## 阶段 4：全局助手文件库

### 步骤 4.1：存储与 REST
Depends on：无。可与阶段 2、3 并行。
Files：
- 新建 `internal/store/migrations/0025_assistant_files.sql`：`assistant_files(id TEXT PK, name, mime, size, sha256, scope TEXT CHECK(scope IN ('conversation','shared')), thread_id NULL REFERENCES threads ON DELETE SET NULL, source TEXT CHECK(source IN ('upload','assistant','orb')), source_thread_id NULL, created_at)`；
- 新建 `internal/store/queries/assistant_files.sql`、`internal/library/library.go` 及测试、`internal/api/library.go`；
- 修改 `schema/openapi.yaml`。
Produces：
- `library.Service`：`Upload(ctx, name, threadID string, r io.Reader) (File, error)`、`Put(ctx, PutParams) (File, error)`、`List(ctx, Filter) ([]File, error)`、`Open(ctx, id) (io.ReadCloser, File, error)`、`SetScope(ctx, id, scope) error`、`Delete(ctx, id) error`、`CopyToAttachment(ctx, id, threadID string) (attachmentID string, err error)`。
- REST：`POST /api/v1/assistant/files?name=&threadId=`、`GET /api/v1/assistant/files`、`GET /api/v1/assistant/files/{id}/content`、`PATCH /api/v1/assistant/files/{id}`（`{scope}`）、`DELETE /api/v1/assistant/files/{id}`。
Do：
- 字节存在 `<data_dir>/assistant-files/<id>`，单文件上限沿用 100MiB，无总量上限。
- 上传了但没发送（`thread_id` 为空）的文件，24 小时后由 GC 回收；其余文件永不自动回收。
Accept：`make check-gen && make lint-spec && go test ./internal/library/... ./internal/api/...` 通过。用例：
- 上传、列出、改 scope、删除；
- `CopyToAttachment` 生成一个绑定到目标线程的新 `att_` 行，且 sha256 一致；
- 未绑定文件超过 24 小时被回收。

### 步骤 4.2：助手对话接收附件
Depends on：4.1。
Files：`internal/threads/agent.go`（`command()` 中的 `coordinator_restricted`）、`internal/threads/coordinator.go`、`docs/protocol.md`、`web/src/components/composer/Composer.tsx`、`web/src/components/composer/attachments.tsx`。
Do：
- 助手线程的 `prompt`/`steer`/`follow_up` 接受 `orynFiles: string[]`（库文件 id，最多 20 个）。控制面把这些文件绑定到本对话，并追加 `<oryn-attachments>` 块，路径为沙箱内路径 `~/library/threads/<threadId>/<id>/<name>`（共享文件为 `~/library/shared/<id>/<name>`）。
- `orynAttachments` 在助手线程上仍然拒绝。
- 前端在助手对话里显示附件按钮，上传走 `POST /assistant/files`，图片照常走内联 `images`。
Accept：`go test ./internal/threads/...` 通过。新用例：助手线程带 `orynFiles` 的 prompt 不再返回 `coordinator_restricted`，且消息里出现沙箱路径块。`cd web && bun x vitest run src/components/composer` 通过。

### 步骤 4.3：同步进协调宿主，开 read/write
Depends on：4.2。
Files：`internal/orbdrt/coordinator.go`（`coordinatorMethods` 增加 `library.sync`、`library.fetch`；omp 参数从 `--no-tools` 改为 `--no-tools --tools read,write`）、`internal/orbdrt/runtime.go`（协调宿主模式下 `acceptLoop` 接受 `library` 流）、`internal/link`（新流类型 `StreamLibrary`）、`schema/openapi.yaml`（`LinkCtlLibrarySync*`、`LinkCtlLibraryFetch*`）、`internal/library/sync.go`、`internal/coord/profile.go`（系统提示）。
Do：
- 控制面在文件变化时，以及每次 `agent.start` 之前，经 `library` 流把所需文件写到沙箱 `~/library/{shared,threads/<threadId>}/…`，并按清单清理多余文件。
- `library.fetch{path}` 读取沙箱 `~/library/out/` 下的文件并经流回传，路径越界一律拒绝。
- 系统提示写明三点：可读目录、唯一可写目录 `~/library/out/`、存库方法。
Accept：`go test ./internal/orbdrt/... ./internal/library/...` 通过。用例：
- sync 后沙箱目录内容与清单一致；
- 删除库文件后再次 sync 时文件被清理；
- `library.fetch` 拒绝 `../` 及 `~/library/out/` 之外的路径。

### 步骤 4.4：宿主工具
Depends on：4.3。
Files：`internal/coord/tools.go`、`internal/coord/profile.go`（`ToolDefs`）、`internal/coord/coord_test.go`。
Produces：
- `save_file{path, name?, shared?}`：经 `library.fetch` 取回文件，`Put` 进库（`source=assistant`）。
- `list_files{scope?}`。
- `share_file{id, shared}`。
- `create_thread` 增加 `files: string[]`：对每个 id 调 `CopyToAttachment`，再把生成的附件 id 填进 `CreateParams.Attachments`。
Accept：`go test ./internal/coord/...` 通过。用例：
- `save_file` 后库里多一条 `source=assistant` 的文件；
- `create_thread{files:[x]}` 建出的线程首条 prompt 含 `orynAttachments`，对应附件 sha256 与库文件一致；
- 越界路径报错。

### 步骤 4.5：启动前重写受管文件
Depends on：4.3。
Files：`internal/threads/agent.go`（`startAgent` 的协调分支）、`internal/orbs/setup.go`。
Do：每次协调线程 `agent.start` 之前，先对协调宿主执行一次 `setup.run`（只含 `coordinatorFiles`），再写入系统提示与 overlay。
Accept：`go test ./internal/threads/...` 通过，新用例断言连续两次启动之间沙箱里被改动的 `config.yml` 会被还原。

### 步骤 4.6：orb 内 `oryn library put`
Depends on：4.1。
Files：`cmd/oryn/library.go` 及测试、`internal/orbsock`、`internal/orbdrt`（转发到 ctl，字节走 `StreamLibrary`）、`internal/coord/orbmethods.go`（`library.put`，scope=shared，`source=orb`，`source_thread_id=ORYN_THREAD_ID`）、新建技能 `internal/orbdrt/assets/oryn-library/SKILL.md`（与 `oryn-setup` 一样放到 `~/.agents/skills/`）。
Accept：`go test ./cmd/oryn/... ./internal/coord/... ./internal/orbdrt/...` 通过，用例断言 put 一个文件后库里出现 `scope=shared`、`source=orb` 的记录。

### 步骤 4.7：文件库面板
Depends on：4.1、4.4。
Files：新建 `web/src/components/assistant/LibraryPanel.tsx` 及测试，`web/src/api/library-queries.ts`，`web/src/pages/AssistantPage.tsx`，文案。
Do：按 R24 实现，预览复用现有的文件预览组件。
Accept：`cd web && bun x vitest run src/components/assistant` 通过，断言切换共享会调用 `PATCH` 且列表行的 scope 标记随之更新。

---

## 阶段 5：首屏 role 胶囊与全局助手入口

### 步骤 5.1：新建线程带 role
Depends on：无（只依赖现有 `oryn_select_model`）。
Files：`schema/openapi.yaml`（`CreateThreadRequest.role?: string`）、`internal/threads/threads.go`、`internal/threads/agent.go`（`initFrames`）。
Do：`role` 有值时，在 `open_session` 之后、首条 `prompt` 之前插入 `oryn_select_model{role}`。若遇到旧 Core 返回 `Unknown command`，按 `lib/model-select.ts` 的同样逻辑，回退为 `set_model` 加 `set_thinking_level`，模型取 `KnownRoles` 的 selector。
Accept：`make check-gen && go test ./internal/threads/...` 通过，用例断言 fakeomp 收到的帧顺序是 `open_session → oryn_select_model → prompt`。

### 步骤 5.2：新建助手对话带首条消息
Depends on：4.2（`orynFiles`）。
Files：`schema/openapi.yaml`（`createAssistantConversation` 增加可选请求体 `{prompt?, files?, images?, model?, thinkingLevel?}`）、`internal/api/coordinator.go`、`internal/threads/coordinator.go`（`CreateCoordinator`）。
Accept：`make check-gen && make lint-spec && go test ./internal/api/... ./internal/threads/...` 通过。用例：
- 不带请求体时行为不变；
- 带 prompt 时线程第一条 user 消息就是该 prompt，附件块为库文件路径。

### 步骤 5.3：`RoleCapsule` 组件
Depends on：1.4（`useModelCatalog`）、2.3（头像解析）。
Files：新建 `web/src/components/composer/RoleCapsule.tsx` 及测试；从 `components/thread/Dialogs.tsx` 的 ModelDialog 抽出 `ModelPickerBody`（role 列表加模型列表），供两处共用。
Produces：`<RoleCapsule value={Target} onChange={(t: Target) => void} allowAssistant={boolean} />`，其中 `type Target = { kind: "assistant"; model?: ModelRef; thinking?: string } | { kind: "role"; role: string; thinking?: string } | { kind: "model"; model: ModelRef; thinking?: string }`。
Do：按 R13 实现，`sm` 以下用底部抽屉。`allowAssistant=false` 时不渲染全局助手项（R17）。
Accept：`cd web && bun x vitest run src/components/composer/RoleCapsule.test.tsx` 通过，覆盖：
- `allowAssistant=false` 时没有全局助手项；
- 选 role、选全局助手、选指定模型三种 onChange 的取值；
- 窄视口下渲染为抽屉。

### 步骤 5.4：HomePage 接入与动画
Depends on：5.1、5.2、5.3、2.2（`useComposerPrefs`）。
Files：`web/src/pages/HomePage.tsx` 及测试、`web/src/index.css`（`.home-tray[data-target="assistant"]` 的 `@property --tray-angle` 旋转描边、`.home-field` 色相变量，以及 reduced-motion 下的静止描边）、文案。
Do：
- 删除 `model` 胶囊与 `ThinkingMenu`，加入 `RoleCapsule`。
- 初始 target 取 `ComposerPrefs.defaultTarget`（`role` 指 `default` role）。
- 执行位置、项目、profile 三个胶囊用 `hidden → shown → leaving` 状态机加 180ms 过渡收起或展开，与 RoleTrack 的 `FADE_MS` 一致。
- 发送时，助手走 `createAssistantConversation(body)` 后 `navigate('/assistant/:id')`；普通走 `createThread({…, role})`。
- `Ctrl+S` 快捷键改为打开 role 胶囊。
Accept：`cd web && bun x vitest run src/pages/HomePage` 通过。新用例：
- 默认对象为 assistant 时三胶囊不在 DOM 中；
- 切到 role 后出现；
- 助手发送调用了 `createAssistantConversation` 且请求体含 prompt；
- tray 的 `data-target` 随选择变化。

---

## 阶段 6：Agent 配置 GUI 与立即应用

### 步骤 6.1：orbd `omphome.apply`
Depends on：无。
Files：`internal/orbdrt/runtime.go`（新 case）、`internal/orbdrt/setup.go`（抽出 `writeOmpHome` 供复用）、`internal/orbdrt/coordinator.go`（允许列表）、`schema/openapi.yaml`（`LinkCtlOmpHomeApplyParams{ompHome}`）、orbd hello 能力标志 `omphome-apply`。
Do：只做"写入并清理"，不碰 git、不跑脚本，与 `acquireSetup` 互斥。
Accept：`make check-gen && go test ./internal/orbdrt/...` 通过，用例断言 apply 后文件更新，被删的托管文件被清理，非托管文件保留。

### 步骤 6.2：控制面下发器
Depends on：6.1。
Files：新建 `internal/orbs/apply.go` 及测试；修改 `internal/api/agentconfig.go`。
Produces：`func (s *Service) ApplyOmpHome(ctx context.Context) (ApplyReport, error)`，其中 `ApplyReport{Orbs []OrbApply{OrbID string; OK bool; Err string}; Changed []string; ConfigBefore, ConfigAfter []byte}`。`ConfigBefore` 是上一次成功下发的 `config.yml`，`ConfigAfter` 是本次下发的 `config.yml`。
Do：
- 对每个 `ready` 且带 `omphome-apply` 能力的 orb：生成 `Plan`，下发 `omphome.apply`，然后 `setPolicy(orbID, plan.Egress)` 刷新模型路由。
- 协调宿主只下发 `coordinatorFiles`。
- agent-config 的 Put、Delete、Import 之后防抖 1 秒触发，计算本次 `Changed` 路径集合，并带上前后两版 `config.yml`。上一次下发的内容存在 `<data_dir>/omp-home-applied/config.yml`。
- 冻结或停止的 orb 跳过，它们在 resume 时照常同步。
Accept：`go test ./internal/orbs/...` 通过。用例：
- 两个 ready orb 和一个 frozen orb，只有前两个收到 apply；
- models.yml 变化后 `policyFor` 返回新路由。

### 步骤 6.3：会话级生效与懒重启
Depends on：6.2（`Changed`）、步骤 6.4 的设置 schema。
Files：`internal/threads/apply.go`（新建）及测试、`internal/threads/coordinator.go`（把 `RefreshAgent` 泛化为 `refreshLocked(reason)`，work 线程也可用）、`schema/openapi.yaml`（`GET /api/v1/agent-config/apply-status`、`POST /api/v1/agent-config/restart-sessions`）。
Do：
- 分类不能只看改了哪些文件：GUI 里所有启停操作都只改 `config.yml`。所以 `config.yml` 先做键级差分，其余文件再按路径分类，最后取并集：
  - `config.yml` 的 `disabledExtensions`：比较前后两个集合，得到新增和移除的条目，再按条目前缀分类：
    - `skill:`、`slash-command:`、`rule:`、`prompt:`、`instruction:`、`context-file:`、`mcp:` 有变化：会话空闲时发送 `reload_plugins`（A4）；
    - `hook:`、`extension-module:`、`tool:` 有变化：把线程标为需重启，原因记为对应条目。
  - `config.yml` 的 `task.disabledAgents`、`task.agentModelOverrides` 等 `task.*` 子 agent 键：什么都不做，task 发起前会重新读取。
  - `config.yml` 的其他键：先按 6.4 的 schema 查是否 `restartRequired`。是则标为需重启，原因记为键名；否则什么都不做，omp 的文件监听会让它生效。
  - 文件 `skills/`、`commands/`、`rules/`、`prompts/`、`instructions/`、`AGENTS.md`、`RULES.md`：会话空闲时发送 `reload_plugins`。
  - 文件 `agents/`：什么都不做。
  - 文件 `hooks/`、`extensions/`、`tools/`、`SYSTEM.md`、`APPEND_SYSTEM.md`、`PERSONALITY.md`、`models.yml`：标为需重启，原因记为路径。
- `restart-sessions` 对被标记的线程调用 `refreshLocked`：等 settled 后 `agent.stop`，下次命令走 wake 路径并以 `open_session` 拉起。
Accept：`go test ./internal/threads/...` 通过。用例：
- 改 `skills/x/SKILL.md` 后空闲线程收到 `reload_plugins`，运行中的线程在 `session_settled` 之后才收到；
- `config.yml` 只往 `disabledExtensions` 里加 `skill:x`，线程收到 `reload_plugins`，且没有被标为需重启；
- `config.yml` 只往 `disabledExtensions` 里加 `hook:pre:bash:guard`，apply-status 列出该线程，原因含该条目；
- `config.yml` 只改了一个非 `restartRequired` 的键，线程既不收到 reload 也不被标记；
- 改 `hooks/pre/x.ts` 后 apply-status 列出该线程；
- 调用 restart-sessions 后线程在空闲时 stop，且下一条消息自动 start。

### 步骤 6.4：设置 schema 来源
Depends on：1.2（同一套"一次性运行钉住的 Core"机制）。
Files：`internal/modelcat/schema.go` 及测试。
Produces：`func (c *Catalog) SettingsSchema(ctx context.Context) (RpcSettingsSchemaResult, error)`，按 Core 钉缓存。
Do：一次性运行 `omp --mode rpc --no-session`，发送 `get_settings_schema`，读到响应后退出。供 6.3 判断哪些键需要重启。
Accept：`go test ./internal/modelcat/...` 通过，用例用假 omp 回放已记录的响应，断言能列出 `restartRequired` 的键。

### 步骤 6.5：前端配置工具层
Depends on：无（只依赖现有文件接口）。
Files：新建 `web/src/lib/omp-config.ts`、`web/src/lib/frontmatter.ts` 及测试；`web/package.json` 增加依赖 `yaml`。
Produces：
- `readConfigDoc(text): Document`、`toggleDisabled(doc, id, enabled)`、`setDisabledAgent(doc, name, disabled)`、`setModelRole(doc, role, selector|null)`、`setCycleOrder(doc, roles)`、`setModelTagName(doc, role, name|null)`、`setAgentModelOverride(doc, agent, selector|null)`；
- `parseFrontmatter(text): {data, body, error?}`、`stringifyFrontmatter(data, body)`；
- 启停键构造函数：`skillKey`、`hookKey(type, tool, name)`、`extensionModuleKey`、`toolKey`、`ruleKey`、`promptKey`、`instructionKey`、`slashCommandKey`、`contextFileKey(level, basename)`。
Do：hook 键从文件路径推导时，必须先对照 `oryn-core/packages/coding-agent/src/capability/hook.ts` 与 `discovery/builtin.ts` 的 hook 解析逻辑，把对应关系写进函数注释并配用例。
Accept：`cd web && bun x vitest run src/lib/omp-config.test.ts src/lib/frontmatter.test.ts` 通过。用例：
- 带注释的 config.yml 改一项后，其他注释原样保留；
- `toggleDisabled` 重复调用是幂等的；
- frontmatter 往返一致。

### 步骤 6.6：六个子页
Depends on：6.5、2.2（头像接口）、1.4（模型目录）、6.3（apply-status）。
Files：新建 `web/src/pages/settings/agent/{SkillsPage,SubagentsPage,RolesPage,PromptsPage,CollectionsPage,ExtensionsPage}.tsx` 及测试；新建共用组件 `components/settings/MarkdownCollection.tsx`（列表、启停、frontmatter 表单、正文编辑器）和 `ApplyStatusBanner.tsx`；修改 `pages/SettingsPage.tsx`（`SettingsSection` 新增 `agent-skills`、`agent-subagents`、`agent-roles`、`agent-prompts`、`agent-commands`、`agent-extensions`，原 `agent-config` 改名"高级"）、`components/layout/more-entries.ts`、文案。
Do：各页内容如下。
- **技能**：`skills/<name>/SKILL.md` 的增删改，字段为 name、description、正文，启停用 `skill:`。
- **子 agent**：
  - 内置 agent 只读（R28）；
  - 自定义 agent 表单字段为 name、description、model（可多选，带 `@role` 补全）、thinking-level、tools、spawns、blocking、autoloadSkills、advisor、prewalk 加正文；
  - 启停写 `task.disabledAgents`；
  - 每行可编辑头像；
  - 可设 `task.agentModelOverrides`。
- **模型角色**：
  - `cycleOrder` 拖拽排序，`modelRoles` 用模型目录选择，`modelTags.<role>.name` 改名；
  - 可增删自定义 role；
  - 每行编辑头像（`RoleAvatarsCard` 的能力迁到这里）。
- **提示词与上下文**：`AGENTS.md`、`RULES.md`、`APPEND_SYSTEM.md`、`PERSONALITY.md`、`TITLE_SYSTEM.md`、`SYSTEM.md` 的 Markdown 编辑。`SYSTEM.md` 带"会整段替换内置提示词"的警示，上下文文件可单独启停。
- **命令/规则/提示片段**：`commands/`、`rules/`、`prompts/`、`instructions/` 四个 `MarkdownCollection`。
- **扩展/hook/工具**：列出 `extensions/`、`hooks/pre|post/`、`tools/` 的文件，提供代码编辑与启停，并标注"需重启会话后生效"。
- 每页顶部都有 `ApplyStatusBanner`（R29、R30），并标注"不作用于全局助手"（R31）。
Accept：`cd web && bun run check && bun x vitest run src/pages/settings` 通过。每页至少一个用例：保存后经 `putAgentConfigFile` 写出的内容符合预期（例如停用技能 x 后 `config.yml` 的 `disabledExtensions` 含 `skill:x`）。

### 步骤 6.7：端到端验收脚本与文档
Depends on：阶段 1 到 6 全部完成。
Files：新建 `web/scripts/m13-accept.ts`，`web/package.json` 增加 `accept:m13`；修改 `docs/protocol.md`（新接口、新 ctl 方法、新流类型、文件库、身份规则）、`README.md`。
Do：脚本在 mock 模式与真实 `orynd`（`dev/orynd.yaml` 加 fakeomp）下逐条走 R1–R31 中能在界面上观察到的项，截图存进 `.dev/m13/`，并逐项断言。
Accept：`cd web && bun run accept:m13` 退出码为 0，输出中每条 R 编号都标"PASS"。

---

## 并行性

- 阶段 1、2、4 互不共享文件，可以并行。
- 阶段 3 依赖 2.3；阶段 5 依赖 1.4、2.3、4.2；阶段 6 依赖 1.2、2.2。
- 并行开工前，先冻结三份契约：步骤 1.3、2.2、4.1 的 OpenAPI 片段。冻结方式是先单独提交 `schema/openapi.yaml` 并跑通 `make check-gen`。

## 风险

- 协调宿主开了 `write` 后，它能改沙箱 HOME 里的任何文件。缓解：4.5 每次启动前还原受管文件，系统提示写明唯一可写目录。残余风险：同一次运行期间，它改过的配置会一直生效到下次启动。
- 文件库没有总量上限。磁盘满时，上传返回 507，面板提示清理。
- 自动下发会在 omp 运行中改写 `config.yml`。omp 的监听做了 keep-last-good，坏的配置不会生效，但下发前不做语义校验。6.6 的表单只产出合法结构，原始文件浏览器（"高级"）不在保护范围内。
