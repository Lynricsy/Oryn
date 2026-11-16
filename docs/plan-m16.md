# M16 — 内置 Hindsight 记忆：同项目与跨项目共享：实施计划

## 修订记录

- **v1（2026-11-16）**：初稿。决定过程见 `.agent-logs` 第 0144–0151 条及其后的决策日志。
- **v2（2026-11-16，执行中修订）**：步骤 0.3 的结论（附录 B，`docs/plan-m16-appendix-b.md`）。A3、A6 成立，Core 不需要第二处改动。据此修改了 2.2 和 2.3：
  - Core 渲染 mental model 时，只显示无标签的模型和带本项目标签的模型（`modelVisibleForTags`）。所以网关在 list/get 响应里，把 `oryn:global` 模型的 `tags` 改写为 `[]`，否则 `user-preferences` 永远进不了 `<mental_models>`。
  - orb 只能创建 `project-conventions-<P>` 和 `project-decisions-<P>` 两种模型，且标签必须恰好是 `[project:P]`。DELETE 请求一律 403。`/documents*` 和 `/memories/list` 这几个端点 Core 没有调用点，一律 403。
  - 候选队列分流时必须返回 2xx：Core 的 outbox 遇到 400/413/422 会隔离事件，遇到其它非 2xx 会持续重试 7 天。
  - document 一律按 URL 里的 threadId 归属线程。原因是子 agent 的 `metadata.session_id` 是子会话的 id，和线程对不上。
  - 判断一次写入是不是全局写入，依据是 `items[].tags` 这个键整体缺失。这只在 `per-project-tagged` 模式下成立，所以 orbd 必须设 `HINDSIGHT_SCOPING=per-project-tagged`。
  - env 增加 `NO_PROXY=127.0.0.1,localhost`：Bun 的 fetch 会读代理变量，不设的话发往 `127.0.0.1:7811` 的记忆请求也会被送进代理。
  - 创建 bank 的 PUT 请求要丢弃 body，不允许任何 orb 改写共享 bank 的 mission。
- **v3（2026-11-16，执行中修订）**：步骤 0.2 的结论（附录 A，`docs/plan-m16-appendix-a.md`，Hindsight 1.2.1 实测）。A2、A5 成立；A1 只部分成立。据此修改了 2.1、2.2、2.5、2.6、4.1：
  - A1：`tags_match=any` 会连无标签记忆一起返回，所以网关改为强制 `any_strict`，一次召回就能拿到本项目和全局两类记忆，且不会漏进无标签记忆。`all_strict` 与 `exact` 对两个标签返回空。
  - gemini 不读各用途的 `*_LLM_BASE_URL`，只认进程级的 `GOOGLE_GEMINI_BASE_URL`，模型名放在 URL 路径里（`/v1beta/models/<别名>:generateContent`）。所以 `.env` 固定多出这一行，中转对 gemini 请求按别名认出用途（`PurposeForAlias`）。重排的 BASE_URL 是完整端点，以 `/rerank/v1/rerank` 结尾。各协议的鉴权头分别是 `Authorization: Bearer`（openai、嵌入、重排）、`x-api-key`（anthropic）、`x-goog-api-key`（gemini）。
  - 删除 document 后，派生的事实与观察会一起消失，但 mental model 只被标为 `is_stale: true`，不会自动更新。所以 2.6 的遗忘、删除和项目级联删除完成后，orynd 要刷新受影响的 mental model。
  - `DATABASE_URL` 缺失时 Hindsight 会静默启动一个空的 pg0 并报告健康，1.2.1 没有禁用它的变量。所以 compose 总是写数据库地址；2.1 的健康检查还要求 `GET /banks/oryn/stats` 不为 404。
  - 每次启动的模型自检会发 5 次计费调用，库不可用导致重启循环时会持续扣费（实测 6 分钟约 150 次）。所以 compose 设 `HINDSIGHT_API_SKIP_LLM_VERIFICATION=true`，已实测启动时零调用。
  - 异步 retain 返回 200，不是 202，应答体为 `{success, bank_id, items_count, async, operation_id, …}`。候选分流时照这个格式返回。
  - 导出的 zip 下载要带 Bearer key，所以下载只能由 orynd 代理。
  - 4.1 的验收改为先把 `.env.example` 复制为 `.env` 再执行 `config`：`env_file` 必须是必需项，否则漏放 `.env` 的 API 会以无鉴权模式启动。
- **v4（2026-11-16，执行中修订）**：reembed 改为停机迁移。Hindsight 按 `provider:model:dimension` 识别嵌入模型，而 Oryn 只暴露一个别名 `oryn-embed`。中转在运行中切到新模型，API 就会用新模型读写旧向量；维度相同时写保护也认不出模型已换。为此契约追加 `MemoryEndpointUpdate.reembed`：只有带上它，才能在 `embedLocked` 下改嵌入模型或维度；Hindsight 仍可连上时返回 409 `hindsight_running`。deploy.md「换嵌入模型」改为「停 API → 切模型 → `compose run --rm --no-deps api hindsight-admin reembed` → 启动 → `--finish`」，已在 spike 栈上完整演练（39 行 `swapped`，`finish` 后召回正常）。
- **v5（2026-11-16，执行中修订，主人决定）**：重排改为可选。本机与 models.yml 里都没有 Cohere 兼容的重排接口，而原来的 `.env` 把重排写死成 cohere，没配接口时每次召回都会因中转返回 409 而失败。改为：管理页没配重排接口时，`.env` 写 `HINDSIGHT_API_RERANKER_PROVIDER=rrf`（Hindsight 1.2.1 自带，不调用模型，只按 RRF 分融合排序）；配了就走 Cohere 中转。cohere 的三行仍然总是写出，保持键集不变，rrf 模式下 Hindsight 会忽略它们。已部署的重排模式与 LLM 协议一样记入 `deployed_families`；配置变化后页面给出 `.env` 片段并提示重启，确认重启沿用 `POST /memory/models/deployed`。重排接口可以删除，嵌入接口不能删。真机验收（5.2）用 rrf，Hindsight 默认模型用 `claude-pro/claude-haiku-5-5`，验收结束后停掉 Hindsight 并删除数据卷。
- **v6（2026-11-16，执行中修订，安全修复）**：遗忘必须覆盖 mental model。原实现在删除之后只是「尽力刷新」一次，失败只记日志；而 Hindsight 列表接口的 `is_stale` 不观测删除（`http.py` 的字段说明：Deletions are not observed），Core 只要 `content` 非空就注入，reflect 默认也会读 mental model。结果是已遗忘的内容会通过旧摘要继续进入 agent。改为：在带遗忘语义的操作（删除 document、按线程遗忘、replace 改全局记忆、项目级联删除、墓碑重试）调用 Hindsight 删除之前，先把受影响的模型持久化进 `memory_stale_models`（迁移 0038），baseline 是 Hindsight 自己时钟上的 `last_full_refreshed_at`。后台循环按退避重刷，直到 `last_full_refreshed_at` 不等于 baseline（或模型已 404）才清除记录。在此之前，网关的 mental-model list/get 保留条目但把 `content` 置空，转发 reflect 时把这些模型并入 `exclude_mental_model_ids`；删除待重试列表也会显示「等待 mental model 刷新」。迁移 0038 要等另一个会话的 0037 合入之后才提交，避免 goose 乱序。
- **v7（2026-11-16，执行中修订，安全修复）**：v6 按 baseline 判断会被「删除之前已开始、之后才完成的刷新」误判，已改为记下删除之后提交的那次 refresh 的 operation id（`refresh_op`），只有它 `completed` 或模型 404 才清除（1b61d8a）。之后又发现三处并发误解封：
  - 后台循环只按 `model_id` 写回或删除，第二次遗忘写下的标记会被前一轮的结果覆盖；
  - 标记后立即到期，删除还没完成，循环就可能提交刷新；
  - 两次遗忘的删除区间重叠、完成顺序反转时，后完成标记的那一方会拿着先完成的一方的刷新结果解除隐藏，而另一方的删除此时还没结束。若用一行里单个代次加 `ready` 来表示状态，还会有 ABA 问题：行被清除后再次插入，代次又回到原值。

  改为（迁移 0039）：每次标记给模型行写入一个从不复用的随机 `token`，同时在 `memory_stale_holds` 里为每个在途删除插入一条 hold（随机 `hold_id`）。删除结束时（无论成败）按 `hold_id` 精确释放，之后才提交刷新。只有模型行存在、且该模型已没有任何 hold 时，才允许提交刷新；循环的写回和清除都要求 `token` 不变，并且仍然没有 hold。orynd 启动时清空 hold 表（在途的删除已随旧进程结束）。另外，两张卡的 `.env` 片段改为覆盖全部待部署的变化，因为「我已重启」会把它们一起确认（7794384）。

## Context

主人原话：「不如直接把 hindsight 接入 Oryn。内置进来。在『更多』里面的 Agent 组里面加个对应页面来进行管理。」

### 问题

omp 的 mnemopi、sharpshooter、local 三种记忆后端，都把数据写在 orb 自己的 `~/.omp/agent/memories/` 下：

- 新线程开的是新 orb，记忆从零开始；
- 删除 orb，记忆随之消失；
- `oryn config push` 和 ompHome 下发都会排除 `memories/`。

另外，orb 的工作目录固定是 `~/workspace/repo`（`internal/orbdrt/setup.go` 的 `setup.run`）。所以按 cwd 推出来的 bank 名和项目标签，在所有项目里都相同：mnemopi 是 `repo-<同一哈希>`，Hindsight 是 `project:repo`。

### 确认后的决定（2026-11-16，用表单逐轮确认）

| 编号 | 决定 |
|---|---|
| Q1 | 部署：`deploy/` 附带一份锁定 1.2 小版本的 compose（Hindsight + 带 pgroonga 的 Postgres），可以与 orynd 同机，也可以放在别的主机。orynd 通过地址加 API key 访问它，远端部署必须走 HTTPS。生命周期由主人按文档操作。开发文档要写 Hindsight 的升级说明 |
| Q2 | 同一项目的线程共享该项目的记忆；全局记忆所有项目都能召回；项目之间互相看不到 |
| Q3 | orynd 网关按请求来自哪个 orb（或哪个 runner 线程）强制 bank 和标签，只放行 Core 用到的端点，Hindsight 的 key 不进 orb |
| Q4 | agent 可以写全局记忆，但先进待确认队列，主人批准后才生效 |
| Q5 | 覆盖 docker、sysbox、firecracker 三种 orb 和本机 Runner；全局助手不接入 |
| Q6 | 一个全局总开关，每个项目可以单独关闭；agent 重启后生效，页面提示有哪些会话待重启 |
| Q7 | 沿用 Core 的默认：每 3 轮自动写入本项目记忆，首轮自动召回 |
| Q8 | 删除项目时级联删除它的全部记忆；删除或归档线程时记忆不动，管理页提供「按线程遗忘」 |
| Q9 | 管理页内容：状态与连接、开关、按项目浏览/搜索/遗忘、全局记忆增删改、mental models。不嵌入 Hindsight 自带的控制台 |
| R1 | Hindsight 的 LLM 请求经 orynd 中转，复用 Oryn 已配置的 provider 密钥 |
| R2 | 嵌入和重排走远端接口：嵌入用 OpenAI 兼容接口；重排可选，配了就用 Cohere 兼容接口，没配时用 Hindsight 自带的 `rrf`（v5） |
| R3 | 带 pgroonga 扩展的外部 Postgres，支持中文分词检索 |
| R4 | runner 新开一条只放行记忆接口的出站通道；orynd 按线程 ID（校验该线程属于这个 runner）确定项目；在 agent.start 时注入 env 和配置覆盖；README 里关于单向边界的说明同步修改 |
| R5 | 无项目线程：只召回全局记忆，关闭自动写入，agent 显式写入的内容一律进全局候选队列 |
| R6 | 候选存在 orynd 的库里，不进 Hindsight；批准才写入，拒绝即删，不会过期 |
| R7 | 文档给出定时备份的做法；管理页提供按 bank 导出 |
| R8 | 锁定在 1.2 小版本线，补丁版本自动跟随 |
| T1 | 管理页从中央模型目录里选模型，默认预选 memory 角色当前用的模型 |
| U2 | 五种用途都能单独指定模型：默认、retain、reflect、consolidation、mental model 刷新。未单独指定的用途跟随「默认」 |
| T3、U1 | 别名方案：Hindsight 的 `.env` 里每种用途只写一个固定的别名模型名和协议类型，orynd 中转时替换成真实模型。同一协议内换模型立即生效；换协议类型时页面生成新的 `.env` 片段，提示主人重启 Hindsight |
| T4 | 嵌入和重排的地址、模型、key 都在管理页里配置；key 加密保存，同样经 orynd 中转 |
| T5 | 已有数据之后，页面禁止直接修改嵌入模型，要改必须按文档执行 reembed |
| U3 | 全局的 user-preferences mental model 由 orynd 维护，只从已批准的全局记忆中提炼；orb 不能创建或修改不带项目标签的 mental model |
| T7 | 候选在批准前可以编辑 |
| T8 | 管理页显示最近 24 小时和 7 天的调用次数与 token 数，不并入统计页 |
| V1 | 记忆统一用简体中文输出 |

### 现状（已核对代码；行号只作提示，动手前按符号重读）

- **orynd 的权限**：
  - `deploy/systemd/orynd.service` 规定了 `User=oryn`、`NoNewPrivileges`、`ProtectSystem=strict`，只能写 `/var/lib/oryn`；
  - 能访问 docker 的只有 `oryn-provider.service`（`SupplementaryGroups=docker`）。
- **orb 的出站通道**：`127.0.0.1:7811`（`internal/orbdrt/egress.go`）→ yamux 流 `link.StreamEgress` → `internal/orbs/link.go` 的 `attach` → `internal/orbs/egress.go` 的 `serveEgress`，为每个 orb 建一个 `egress.NewHandler(orbID, egressResolver{s, orbID})`。
  - `internal/egress/egress.go` 的 `Handler.resolve` 只认 `/llm/`、`/git/`、`/gh/` 三个前缀。
  - 占位符形如 `oryn-ph-<id>`，只在 `Authorization`、`X-Api-Key`、`X-Goog-Api-Key`、`Api-Key` 这几个请求头和 `key` 查询参数里替换。
- **orb 归属的确定方式**：
  - orb 用令牌 `oot_…` 建立 link，orynd 从令牌解析出 orb，并沿用到 egress。orb 发来的任何内容都不作为归属依据。
  - 普通 orb 的项目取 `db.Orb.ProjectID`。
  - runner 是常驻 orb（`KindRunner`），项目按线程区分：`threads.project_id`。
- **runner 的现状**：
  - `EgressAddr()` 返回空串，orbd 不监听出站端口；`link.go` 的 `StreamEgress` 分支对 runner 直接关闭流；
  - ctl 允许清单见 `internal/orbs/runner.go`；
  - `internal/orbdrt/runner.go` 开头的注释写明「控制面的密钥不来这里」。
- **env 与配置怎么到达 omp**：
  - `agent.start` 的 env 由 orbd 拼出：`baseEnv`，再叠加 `setupEnv`，再叠加 `p.Env`；
  - 控制面发送时 `Env` 为空，见 `internal/threads/agent.go`；
  - 配置覆盖文件的先例：桌面覆盖通过 `--config r.desktopOverlayPath()` 传入（`internal/orbdrt/agent.go`）；
  - `omphome.apply` 不带 env。
- **重启待生效的提示**：`internal/threads/apply.go` 的 `RestartSessions`、`GET /api/v1/agent-config/apply-status`，前端组件 `ApplyStatusBanner`。
- **Core 中的 Hindsight**：
  - 代码在 `/root/Projects/Ling/oryn-core/packages/coding-agent/src/hindsight/`。
  - `bank.ts` 的 `projectLabel()` 取仓库主检出目录的 basename 并转小写，目前没有任何覆盖口。
  - `HINDSIGHT_*` 环境变量优先于所有配置层。
  - 客户端只调用 `/v1/default/banks/{bank_id}` 下的端点，有 token 时发送 `Authorization: Bearer`。
  - `/memory clear` 不删除服务端数据。
  - 种子 `user-preferences` 不带标签。
  - Core 的改动规则见 `ORYN.md`：只新增 `oryn-*` 文件，改动现有文件要登记进例外清单。发版方式：打 tag `oryn-v<omp 版本>-<n>`，由 `oryn-release.yml` 构建，之后更新 Oryn 的 `core/source.json` 和 `web/src/omp/rpc-types.d.ts`。
- **前端**：
  - 「更多」菜单的注册表是 `web/src/components/layout/more-entries.ts` 的 `MORE_ENTRIES`，Agent 组的页面放在 `web/src/pages/settings/agent/`；
  - 新增一个分区要改六处：`MORE_ENTRIES`、`SettingsSection`、`App.tsx` 的 `SETTINGS`、`SettingsPage` 的渲染分支、两个语言包、图标；
  - 非文件类 REST 资源的写法参照 `SharesSettings.tsx` 与 `api/share-queries.ts`。
- **最新的迁移**是 `0035_github_watch_drop_pulls_etag.sql`，下一个可用编号是 **0036**。动手时重新确认，M15 可能还会再加迁移。
- **中央模型目录**：`internal/modelcat`，其中已有供一次性 omp 用的 `egress.go`。

### 外部事实（Hindsight 1.2.1，2026-11-10 发布）

- **镜像**：`ghcr.io/vectorize-io/hindsight-api:{版本}`，另有 `-slim` 版。slim 约 500 MB，不带本地模型。tag 有 `1.2.1` 和 `1.2` 两种写法，后者跟随补丁版本。
- **鉴权**：设置 `HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension` 和 `HINDSIGHT_API_TENANT_API_KEY` 之后，请求要带 `Authorization: Bearer`。没有按 bank 区分的访问控制。
- **LLM**：
  - 全局默认配置：`HINDSIGHT_API_LLM_{PROVIDER,MODEL,BASE_URL,API_KEY}`；
  - 每种用途可以单独覆盖：`HINDSIGHT_API_{RETAIN,REFLECT,CONSOLIDATION,MENTAL_MODEL_REFRESH}_LLM_*`；
  - 输出语言：`HINDSIGHT_API_LLM_OUTPUT_LANGUAGE`；
  - 并发：`HINDSIGHT_API_LLM_MAX_CONCURRENT`、`HINDSIGHT_API_WORKER_MAX_SLOTS`。
- **嵌入与重排**：
  - `HINDSIGHT_API_EMBEDDINGS_PROVIDER=openai` 加 `_EMBEDDINGS_OPENAI_{BASE_URL,API_KEY,MODEL,DIMENSIONS}`；
  - 重排可选（v5）：配了 Cohere 兼容接口时写 `HINDSIGHT_API_RERANKER_PROVIDER=cohere` 加 `_RERANKER_COHERE_BASE_URL`，没配时写 `rrf`；
  - 换嵌入模型必须执行 `hindsight-admin reembed`，向量维度不一致时服务拒绝启动。
- **中文检索**：
  - 设置 `HINDSIGHT_API_TEXT_SEARCH_EXTENSION=pgroonga`；
  - Postgres 要求 14 以上，并带 pgvector 和 pgroonga（官方配方 `docker/docker-compose/pgroonga/`）。
- **接口**：
  - 不能删除单条记忆，只能按 document 删除；
  - 按 bank 导出用 `POST /v1/default/banks/{id}/transfer/export`，是异步操作；
  - 健康检查：`/health/live`、`/health/ready`，版本：`/version`；
  - 整库备份用 `hindsight-admin backup`。
- **已知坑**：
  - 必须设置固定的 `HINDSIGHT_API_WORKER_ID`（issue #2165）；
  - 容器停止宽限期要 75 秒；
  - 外部数据库连不上时，服务会悄悄改用一个全新的空内嵌库，健康检查却仍报正常（issue #6485，1.2.0 仍未修复）。compose 里要显式禁用内嵌库，orynd 的健康检查要比对 bank 的统计数据。

### 需求（每条都能由第三方判断真假）

**A. 连接与部署**

- **R1** 在管理页填写 Hindsight 地址后，页面能显示以下信息：服务是否可达、`/version` 返回的版本、`/health/ready` 的状态、bank `oryn` 的记忆数。版本的小版本号与 Oryn 已验证的不同时（例如服务端 1.3.x，而 Oryn 验证的是 1.2），页面显示警告。
- **R2** 远端地址（主机不是 `127.0.0.1`、`localhost`，也不是 docker 网关地址）只能用 `https://`，否则拒绝保存。
- **R3** 管理页可以生成一份完整的 `.env`：Hindsight API key、中转令牌、每种用途的协议类型、别名模型和中转地址、嵌入/重排的中转地址、输出语言、并发上限、worker ID。其中两个密钥只在生成的那一次显示。重新生成会让旧的立即失效。
- **R4** 执行 `deploy/hindsight/compose.yaml` 配合生成的 `.env`，执行 `docker compose up -d` 之后，R1 的状态变成「正常」。Hindsight 的 API 默认只发布在 `127.0.0.1`。

**B. 共享与隔离（网关强制）**

- **R5** 项目 P 的 orb 里写入的记忆，在 P 的其他线程里能召回；在项目 Q 的线程里召回不到，即使 Q 的请求伪造了 `project:P` 标签或换了 bank 名，也召回不到。
- **R6** 已批准的全局记忆，在所有项目和无项目线程里都能召回。
- **R7** orb 发出的请求中，凡是不在放行清单里的路径，一律返回 403。放行清单是步骤 0.3 记下的 Core 端点。orb 发出的请求里看不到真实的 Hindsight key：响应里不含 key，orb 的环境里也没有。
- **R8** orb 写入不带项目标签的记忆（Core 的 `scope:"global"`），或者无项目线程写入任何记忆，内容都进入候选队列，不写入 Hindsight，Core 收到的是写入成功的应答。
- **R9** orb 创建、修改、刷新或删除不属于本项目的 mental model，返回 403。orb 可以读取 `user-preferences`，但不能写它。

**C. 开关与生效**

- **R10** 全局开关关闭时，新启动的 agent 不再带 `memory.backend: hindsight`，网关拒绝一切记忆请求（返回 503，code 为 `memory_disabled`）。
- **R11** 关闭某个项目后，该项目新启动的 agent 不再启用记忆，其他项目不受影响。
- **R12** 开关变化时，正在运行、需要重启的线程出现在 apply-status 的待重启列表里，点「重启」后在会话静止时重启。

**D. Runner**

- **R13** runner 上属于项目 P 的线程，能读写 P 的记忆，语义与 R5–R9 相同。runner 的 orbd 只在记忆启用时监听记忆专用端口，这个通道只放行记忆路径，访问 `/llm/`、`/git/`、`/gh/` 一律返回 403。
- **R14** 记忆请求路径里携带的线程如果不属于发出请求的 orb 或 runner，返回 403。

**E. 模型中转**

- **R15** Hindsight 发往中转入口的 LLM 请求，orynd 会：
  - 校验中转令牌；
  - 按用途查出管理页选定的模型；
  - 把请求里的别名换成真实模型名；
  - 转发到该模型所属 provider 的上游，并注入 Oryn 加密库里的真实密钥。

  响应里出现的真实密钥会被替换掉。
- **R16** 同一协议类型内换模型，下一条请求就会使用新模型，Hindsight 不需要重启。选中的模型协议类型与 `.env` 里写的不一致时，页面显示新的 `.env` 片段和「需要重启 Hindsight」；在主人点「已重启」之前，该用途的中转请求返回 409，code 为 `family_mismatch`。
- **R17** 嵌入和重排请求按同样的方式中转到管理页配置的远端接口。bank 里已有记忆之后，嵌入模型和维度变为只读，页面给出 reembed 文档的链接。
- **R18** 管理页显示每种用途最近 24 小时和 7 天的调用次数、输入 token、输出 token。数据来自中转时的计数。

**F. 管理**

- **R19** 可以按项目浏览记忆列表（分页），可以按关键词召回搜索。可以按线程遗忘：删除该线程产生的全部 document。也可以删除单个 document。
- **R20** 全局记忆可以新建、编辑、删除；候选可以编辑后批准，也可以直接拒绝。批准之后，R6 立即成立。
- **R21** mental models：按项目列出，可以手动刷新。全局的 `user-preferences` 由 orynd 创建，只从全局记忆中提炼；每次批准或编辑全局记忆后，它会自动刷新。
- **R22** 可以导出 bank：页面触发导出，完成后可以下载归档文件。
- **R23** 删除项目后，该项目的全部 document 从 Hindsight 中删除，它的 mental models 也一并删除。删除失败会进入重试队列，页面上可以看到失败的条目。

**G. 文档**

- **R24** `docs/deploy.md` 新增「Hindsight 记忆服务」一节，内容包括：同机和远端两种部署、生成 `.env`、定时备份（`hindsight-admin backup` 或 `pg_dump` 的 cron 示例）、补丁升级、小版本升级的检查清单、reembed 流程。`README.md` 的功能列表和 runner 单向边界的说明同步修改。

### 非目标

- 全局助手（协调档）接入记忆：它的 overlay 强制 `memory.backend: off`，另有 EverOS / 近况路线。
- 把宿主机上已有的 mnemopi bank 迁移进来。
- orynd 托管 Hindsight 的生命周期（启动、升级、重启）。被否决的原因见「方案与被否决的选项」。
- 嵌入 Hindsight 自带的控制台 UI。
- 把项目记忆「提升为全局」（未选；可以在全局记忆里手动新建）。
- 并入现有统计页（T8）。
- OpenAI ⇄ Anthropic / Gemini 之间的协议转换（U1 未选）。
- 全局候选的自动过期（R6）。

### 假设（每条注明失效时受影响的步骤）

- **A1** Hindsight 召回时设 `tags=[project:P, oryn:global]`、`tags_match=any`，只会返回带其中某个标签的记忆，以及不带标签的记忆。全局记忆一律打上 `oryn:global` 标签，不存无标签记忆。由步骤 0.2 验证。
  - 失效影响：2.2 的召回改写改为「按 project 和 global 各召回一次再合并」，同时影响 R6 的延迟。
- **A2** mental model 可以用 `tags=[oryn:global]` 配合严格匹配，限定只从全局记忆中提炼。由 0.2 验证。
  - 失效影响：2.6 改为由 orynd 定期用 reflect 生成全局偏好摘要，写成一个 document；U3 的实现方式随之改变。
- **A3** Core 的 Hindsight 客户端拼接 URL 时保留 `HINDSIGHT_API_URL` 里的路径前缀（例如 `/memory`）。由 0.3 验证。
  - 失效影响：orb 改为用独立端口 `127.0.0.1:7812` 访问记忆，不加前缀；2.2 和 2.4 的监听设计随之改变。
- **A4** 管理页选的模型都来自中央模型目录，每条带 provider key 和协议类型；中转能复用 `modelcat/egress.go` 或 `agentcfg.Render` 产出的路由（上游 origin 和密钥 id）。由 0.4 确认。
  - 失效影响：2.5 自行从 omp-home 的 `models.yml` 解析路由。
- **A5** Hindsight 对三种协议（openai 兼容、anthropic、gemini）各自使用固定的请求路径，请求体里有可以替换的模型字段（gemini 的模型名在 URL 路径里）。由 0.2 用假上游抓包验证。
  - 失效影响：2.5 删掉不满足条件的协议，页面上把该协议的模型标为不可选。
- **A6** Core 自动写入时有可以识别的 `document_id`，并在 metadata 里带 `session_id`。网关因此能把 document 归到线程。由 0.3 验证。
  - 失效影响：2.2 的 document 索引改为记录「首次出现的 document_id → 当时的线程」。

## 全局约束

- **语言与提交**：
  - 代码注释、文档、提交信息用简体中文；
  - 每个步骤一个原子提交，格式 `<type>(<scope>): <gitmoji> <subject>`，带 `Co-authored-by: Wine Fox <fox@ling.plus>`；
  - 用 eval 的 `commit` 命名空间提交；
  - 子 agent 同时不超过 5 个；
  - 每个阶段完成后用 `record-agent-log` 记录。
- **并行改动**：工作区里有其他人的改动，M15 正在进行。每步动手前重读目标文件，按符号定位，不回退别人的改动。迁移编号在动手时现取。
- **每步提交前必须通过**：`go build ./... && go vet ./...`。改动 `web/` 或 OpenAPI 的步骤还要通过 `cd web && bun run check`。改动 OpenAPI 的步骤要通过 `make check-gen`。新的 OpenAPI 操作在实现之前由 `apigen.Unimplemented` 返回 501。
- **Core 改动**：在 `/root/Projects/Ling/oryn-core` 分支 `oryn` 上进行，遵守 `ORYN.md`：只新增 `oryn-*` 文件，改动现有文件要登记进例外清单，使用仓库自带的 oxfmt/oxlint，不提交 `bun.lock`。
- **安全不变量**：
  - 真实的 Hindsight key、中转令牌、provider 密钥都不进 orb 或 runner；
  - 网关从不采信请求里的 bank 名、标签和项目；
  - 失败一律按拒绝处理；
  - 任何查询出错都按「未启用」处理，不按「放行」处理。
- **固定值**：
  - bank id：`oryn`；
  - 项目标签：`project:<项目 ID 转小写>`；
  - 全局标签：`oryn:global`；
  - 已验证的小版本：`1.2`，常量 `memory.TestedMinor`；
  - 别名模型：`oryn-default`、`oryn-retain`、`oryn-reflect`、`oryn-consolidation`、`oryn-mental-model`、`oryn-embed`、`oryn-rerank`；
  - 输出语言：简体中文（`HINDSIGHT_API_LLM_OUTPUT_LANGUAGE` 的取值格式由 0.2 确认）；
  - 默认并发：`LLM_MAX_CONCURRENT=4`、`WORKER_MAX_SLOTS=4`。

## 方案与被否决的选项

- **部署**：采用 compose 附带在仓库里、主人自行运行、orynd 只负责连接（Q1）。被否决的方案：
  - 由 orynd 拉起原生 `hindsight-api`：要在 `ProtectSystem=strict` 的限制下维护 Python 环境、Postgres 和模型下载。
  - 给 orynd 加 docker 权限：docker 组等同于 root，而 orynd 正是暴露在公网的那个进程。
  - 新增 provider「托管服务容器」任务：主人认为工作量大，还要处理 provider 下线时的迁移。
- **隔离**：采用 orynd 网关强制执行（Q3）。被否决的方案：只靠 Core 在客户端打标签。Hindsight 没有按 bank 区分的访问控制，一次提示词注入就能跨项目读写。
- **全局写入**：采用 orynd 侧的候选队列（Q4、R6）。被否决的方案：agent 直接写全局（全局记忆注入所有项目，被污染的代价最大）；只允许主人写（主人选择保留 agent 的提议能力）。
- **模型**：采用别名加中转，同协议内换模型、按用途单独指定模型（R1、U1、U2、T3）。被否决的方案：
  - 在 `.env` 里写真实模型名：每次换模型都要重启 Hindsight。
  - 协议转换：工作量大，转换层本身容易出错。
- **项目标识**：Core 增加环境变量 `ORYN_HINDSIGHT_PROJECT`，网关同时强制标签。被否决的方案：只在网关改写标签。Core 会按 `project:repo` 生成 mental model 的 id（例如 `project-conventions-repo`），多个项目共用一个 bank 时 id 会冲突。
- **启用方式**：`agent.start` 携带记忆参数，orbd 写配置覆盖文件并注入 env，orb 和 runner 走同一条路径（R4、Q6）。被否决的方案：经 `setup.run` 的 env 和 `omphome.apply` 下发。前者只在 setup/resume 时生效，后者不带 env，而且 runner 两者都不接收。

**回滚点**：全部改动都只增不删。全局开关关闭时，系统行为与 M15 相同。迁移 0036 的 Down 会删除新增的表。Hindsight 的数据保存在外部服务里，不随 Oryn 回滚。

## 阶段 0：契约与风险前置

### 步骤 0.1：计划入库

Depends on：无。
Files：`docs/plan-m16.md`。
Accept：`git log -1 --name-only` 的输出包含 `docs/plan-m16.md`。

### 步骤 0.2：Hindsight 探查（不产出功能）

Depends on：无，因为它只用本地容器和一次性脚本。
Do：在本机用官方 pgroonga 配方起 `hindsight-api:1.2-slim` 和 Postgres。准备一个假上游 HTTP 服务，负责记录请求并返回固定的 LLM、嵌入、重排响应。依次验证下列各项，结论写进附录 A：

1. A1：标签召回的语义，包括 `any`、`all_strict` 对无标签记忆的处理。
2. A2：mental model 能否用标签限定提炼范围，以及 refresh 的行为。
3. A5：三种 LLM 协议、嵌入、重排各自的请求路径、模型字段所在的位置、鉴权头、是否使用流式。
4. 异步 retain 的应答格式。候选队列要伪造这份应答。
5. 删除 document 之后，派生出的记忆和观察是否一并删除。
6. `transfer/export` 的整个流程。
7. `/version` 返回的格式。
8. `HINDSIGHT_API_LLM_OUTPUT_LANGUAGE` 的取值格式。
9. 禁用内嵌 pg0 用哪个变量，以及数据库断开时 `/health/ready` 的表现（issue #6485）。
10. 中文写入、召回的基本正确性：抽查 10 条。

Accept：附录 A 里每一项都写明「成立 / 不成立 + 证据（请求日志或响应片段）」，A1、A2、A5 已标注是否成立。

### 步骤 0.3：Core 探查（只读代码）

Depends on：无。
Do：阅读 `oryn-core/packages/coding-agent/src/hindsight/{client,backend,state,mental-models,outbox}.ts`，确认以下各项，写进附录 B：

- A3：URL 拼接方式。
- A6：自动写入时的 `document_id` 和 metadata。
- Core 实际调用的端点全表，以及每个端点的请求体字段。这份表就是网关的放行清单。
- `<mental_models>` 块怎样选出要渲染的模型：看 id 还是看列表。
- `scope:"global"` 写入时请求体是什么样。
- `hindsight.globalScope` 和 `memory.backend` 能否经 `--config` 覆盖文件设置。

Accept：附录 B 里列出放行清单（方法 + 路径模式 + 需要强制的字段），A3、A6 已标注是否成立。

### 步骤 0.4：契约冻结

Depends on：0.2、0.3，因为放行清单和应答格式来自这两步的探查结论。
Files：

- 新建 `internal/store/migrations/0036_memory.sql`（编号现取），包括下列表：
  - `memory_settings`：单行，`CHECK (id = 1)`。列：`enabled`、`hindsight_url`、用 `Box.Seal` 加密的 `hindsight_key`、`relay_token_hash`、`deployed_families`（JSON：用途 → 协议类型）、`embed_locked`、`updated_at`。
  - `memory_models`：`purpose`（主键）、`provider_key`、`model_id`。
  - `memory_endpoints`：`kind`（embed/rerank）、`base_url`、`model`、`dimensions`、用 `Box.Seal` 加密的 key。
  - `memory_projects`：`project_id`（主键）、`enabled`。
  - `memory_candidates`：`id`、`content`、`context`、`source_thread_id`、`source_project_id`、`created_at`。
  - `memory_documents`：`document_id`（主键）、`project_id`（可为 NULL，NULL 表示全局）、`thread_id`、`created_at`。
  - `memory_usage`：`hour`、`purpose`、`calls`、`input_tokens`、`output_tokens`。
  - `memory_tombstones`：删除重试队列。
- 新建 `internal/store/queries/memory.sql`。
- `schema/openapi.yaml` 中：
  - 新增 `/api/v1/memory/*` 下的全部操作（见步骤 2.1 和 2.6 的 Produces）；
  - `LinkCtlAgentStartParams` 新增可选字段 `memory: LinkCtlAgentMemory{enabled, projectTag?, autoRetain, overlay}`；
  - runner 的 hello 新增能力 `memoryEgress`。
- `make gen`。

Produces：上面列出的表、sqlc 查询，以及 `apigen` 中的类型 `MemorySettings`、`MemoryStatus`、`MemoryModelChoice`、`MemoryEndpoint`、`MemoryCandidate`、`MemoryDocument`、`MemoryMentalModel`、`MemoryUsage`、`LinkCtlAgentMemory`。
Accept：`make check-gen && go build ./... && go vet ./...` 通过；`goose` 的 up 和 down 在临时库上各执行一次都成功（`go test ./internal/store/... -run Migrat` 输出 `ok`）。

## 阶段 1：Core 改动（oryn-core 仓库）

### 步骤 1.1：`ORYN_HINDSIGHT_PROJECT`

Depends on：0.3，因为要先确认 `projectLabel` 的调用点。可与阶段 2 并行：两边只共用环境变量名这一个契约，它在本步骤固定为 `ORYN_HINDSIGHT_PROJECT`。
Files：

- 新建 `packages/coding-agent/src/hindsight/oryn-project.ts`，导出 `orynProjectLabel(): string | undefined`：读取环境变量，去掉首尾空白后转小写，为空时返回 `undefined`；
- 改 `hindsight/bank.ts` 的 `projectLabel()`：先用 `orynProjectLabel()` 的结果；
- 新建 `packages/coding-agent/test/oryn-hindsight-project.test.ts`；
- 在 `ORYN.md` 的例外清单里登记这次修改。

Accept：在 `/root/Projects/Ling/oryn-core` 执行 `bun test packages/coding-agent/test/oryn-hindsight-project.test.ts` 输出通过。测试覆盖三种情况：设置了环境变量时，标签和 mental model 的 id 后缀都取它；没设置时行为不变；值为空白时视为未设置。

### 步骤 1.2：Core 发版与钉版本

Depends on：1.1。
Do：推送 `oryn` 分支，打 tag `oryn-v18.19.0-10`（序号现取），等 `oryn-release.yml` 构建成功；然后在 Oryn 仓库更新 `core/source.json`，并替换 `web/src/omp/rpc-types.d.ts`。
Accept：`gh run list --workflow oryn-release.yml -L 1` 显示成功；Oryn 的 `scripts/fetch-core.sh` 拉下来的二进制校验通过；`go build ./...` 通过。

## 阶段 2：orynd 后端

### 步骤 2.1：记忆服务骨架、设置与状态

Depends on：0.4。
Files：

- 新建 `internal/memory/`：`service.go`、`settings.go`、`hindsight.go`（访问 Hindsight 的客户端，只包含 orynd 用到的端点）、`version.go`（`TestedMinor = "1.2"`）、`envfile.go`（生成 `.env`）；
- 改 `internal/server/server.go`：装配记忆服务，注入 `Box`；
- 新建 `internal/api/memory.go`：`GetMemorySettings`、`PutMemorySettings`、`GetMemoryStatus`、`PostMemoryEnvFile`（生成 `.env`、轮换两个密钥）、`PutMemoryProjects`。

Produces：

- `memory.Service`：
  - `Enabled(ctx) bool`
  - `ProjectEnabled(ctx, projectID string) bool`
  - `Status(ctx) (apigen.MemoryStatus, error)`
  - `Client() *memory.Hindsight`
- `ensureBank`：bank 不存在时，orynd 自己创建 `oryn` 并写入 bank mission（中文）。

健康检查要比对 bank 的统计数据：上一次记忆数大于 0、这一次却变成 0 时，状态为 `suspect_empty`，针对 issue #6485。
Accept：`go test ./internal/memory/... -run 'Settings|Status|EnvFile|Version'` 输出 `ok`。测试用 httptest 假 Hindsight，覆盖以下情况：

- R1 的小版本警告；
- R2 拒绝明文地址；
- R3 重新生成后旧令牌失效；
- 统计数据突然归零时状态为 `suspect_empty`。

### 步骤 2.2：记忆网关（强制隔离）

Depends on：2.1。
Files：

- 新建 `internal/memory/gateway.go`：实现 `http.Handler`，输入是可信的调用方身份 `Caller{OrbID, ThreadID, ProjectID *string, Runner bool}`；
- 改 `internal/egress/egress.go`：`Handler.resolve` 新增前缀 `/memory/`，交给 `Resolver.Memory()` 返回的处理器。`Resolver` 接口新增 `Memory() http.Handler` 和 `Caller(threadHint string) (memory.Caller, error)`；
- 改 `internal/orbs/egress.go`：`egressResolver` 实现上面两个方法。线程号一律从路径 `/memory/t/<threadId>/…` 取得，orb 和 runner 都一样，用于按线程归属 document；校验 `threads.orb_id` 等于当前 orb，不符返回 403。普通 orb 的项目取 `orb.ProjectID`；runner 的项目取该线程的 `threads.project_id`。

Do：

1. 按附录 B 的放行清单逐条匹配，其余返回 403。路径里的 bank 一律改写为 `oryn`。
2. retain：
   - 项目线程：每个 item 的 `tags` 和 `document_tags` 强制改为 `[project:P]`，并把 `document_id → (P, thread)` 记进 `memory_documents`；
   - 不带项目标签的写入和无项目线程的写入：转进 `memory_candidates`，返回附录 A 里记下的成功应答格式（R8）。
3. recall 和 reflect：项目线程强制 `tags=[project:P, oryn:global]`、`tags_match=any_strict`（附录 A 第 1 项）；无项目线程强制 `tags=[oryn:global]`、`tags_match=any_strict`。
4. orb 不能直接读、改、删 document（见第 6 条）。按线程遗忘、删除 document 只能从管理页经 orynd 操作（2.6）。
5. mental model：
   - 列表和单个读取只返回 P 的模型和 `oryn:global` 模型，并把后者的 `tags` 改写为 `[]`。`user-preferences` 必须留在列表里，Core 看到它已存在就不会去创建；
   - 创建只允许 id 为 `project-conventions-<P>`、`project-decisions-<P>`，且 `tags` 恰为 `[project:P]`；
   - 刷新只允许对 P 的模型操作，DELETE 一律 403，`user-preferences` 只能读（R9）；
   - `detail=full` 降级为 `content`。
6. 创建 bank 的 `PUT` 请求丢弃 body，直接返回 200 `{}`，不转发。`/documents*` 和 `/memories/list` 一律 403。
7. 未启用时返回 503，code 为 `memory_disabled`。
8. 转发时注入 Hindsight key，响应里出现的 key 替换为 `oryn-redacted`。

Accept：`go test ./internal/memory/... ./internal/egress/... -run 'Gateway|Memory'` 输出 `ok`。测试用假 Hindsight 覆盖：

- R5，包括伪造标签、伪造 bank 名的情况；
- R6、R7、R8、R9、R14；
- 无项目线程；
- 未启用时返回 503。

### 步骤 2.3：在 `agent.start` 时启用

Depends on：2.1、0.4、1.2（需要包含 `ORYN_HINDSIGHT_PROJECT` 的 Core 版本）。
Files：

- 改 `internal/threads/agent.go`：组装 `agent.start` 参数时，向 `memory.Service` 查询当前线程是否启用记忆，填 `Memory`；
- 改 `internal/orbdrt/agent.go`：`Memory` 为启用时，写 `~/.oryn/memory-overlay.yml`，内容为 `memory.backend: hindsight`、`hindsight.globalScope: true`，以及附录 B 确认过的其他键；在参数里追加 `--config <该文件>`；注入以下环境变量：
  - `HINDSIGHT_API_URL=http://127.0.0.1:7811/memory/t/<threadId>`（A3 不成立时按其失效影响改用独立端口）；
  - `HINDSIGHT_BANK_ID=oryn`；
  - `HINDSIGHT_SCOPING=per-project-tagged`；
  - 有项目时设 `ORYN_HINDSIGHT_PROJECT=<项目 ID 转小写>`；
  - 无项目时设 `HINDSIGHT_AUTO_RETAIN=false`；
  - `NO_PROXY=127.0.0.1,localhost`（同时设小写的 `no_proxy`）；
  - 不设 `HINDSIGHT_API_TOKEN`；
- 改 `internal/threads/apply.go`：开关变化时，把受影响的线程标为需重启，原因写 `memory`；
- 改 `internal/memory/service.go`：开关变化时调用重启标记。

Accept：`go test ./internal/threads/... ./internal/orbdrt/... -run 'Memory|AgentStart'` 输出 `ok`。测试覆盖 R10、R11、R12：未启用时不出现 `--config` 覆盖，环境变量里也没有 `HINDSIGHT_*`。

### 步骤 2.4：runner 记忆专用出站通道

Depends on：2.2、2.3。
Files：

- 改 `internal/orbdrt/runtime.go` 和 `egress.go`：runner 在 hello 中声明 `memoryEgress`；`agent.start` 带启用的记忆参数时，在 `127.0.0.1:7811` 上监听，所有线程共用一个端口。线程号经路径 `/memory/t/<threadId>/…` 传递（与 orb 相同），orbd 原样转发；
- 改 `internal/orbs/link.go`：`StreamEgress` 分支在 runner 上不再直接关闭流，改为交给一个只认 `/memory/` 前缀的处理器，其余路径返回 403；
- 改 `internal/orbs/runner.go`：允许清单不变，因为记忆不走 ctl。

Accept：`go test ./internal/orbs/... ./internal/orbdrt/... -run 'Runner.*Memory'` 输出 `ok`。测试覆盖 R13，以及「别的 runner 的线程号返回 403」（R14）。

### 步骤 2.5：模型中转与用量

Depends on：2.1，以及 0.2（A5 的结论）。
Files：

- 新建 `internal/memory/relay.go`：实现 `http.Handler`，挂在 `/memory-relay/v1/{purpose}/…` 上，不进 `/api/v1` 鉴权组。它：
  - 校验中转令牌，令牌可以放在附录 A 记下的任何一个鉴权头里；
  - 从 `memory_models` 查出该用途选定的模型。未单独指定的用途跟随 `default`；
  - 协议类型与 `deployed_families` 不一致时返回 409，code 为 `family_mismatch`；
  - 把别名替换成真实模型；
  - 用 `modelcat` 或 `agentcfg` 的路由信息查出上游 origin 和密钥，按 egress 的注入与脱敏规则转发；
  - 嵌入和重排按 `memory_endpoints` 转发；
  - 从响应里解析 usage，写入 `memory_usage`；
- 改 `internal/server/server.go`：挂载中转入口；
- 改 `internal/api/memory.go`：新增 `GetMemoryModels`、`PutMemoryModels`、`PutMemoryEndpoints`、`GetMemoryUsage`。`PutMemoryEndpoints` 在 `embed_locked` 为真时拒绝修改嵌入模型和维度（R17）。

Accept：`go test ./internal/memory/... -run 'Relay|Usage|Models'` 输出 `ok`。测试用假上游覆盖：

- R15，包括响应中的密钥被脱敏；
- R16：同协议换模型立即生效，协议不一致返回 409；
- R17、R18；
- 错误令牌返回 401。

### 步骤 2.6：管理接口与删除联动

Depends on：2.2。
Files：

- 改 `internal/api/memory.go`，新增以下接口：
  - `ListMemoryDocuments(project|global, page)`
  - `SearchMemory(project|global, q)`
  - `DeleteMemoryDocument`
  - `ForgetMemoryThread`
  - `ListMemoryCandidates`
  - `ApproveMemoryCandidate(id, editedContent?)`
  - `RejectMemoryCandidate`
  - `CreateGlobalMemory`
  - `UpdateGlobalMemory`（以同一 `document_id` 和 `update_mode=replace` 重写）
  - `DeleteGlobalMemory`
  - `ListMemoryMentalModels(project|global)`
  - `RefreshMemoryMentalModel`
  - `PostMemoryExport`
  - `GetMemoryExport`
- 新建 `internal/memory/admin.go`。对 `user-preferences`：启用时创建它（`source_query` 取 Core 种子，标签为 `oryn:global`，严格匹配），全局记忆有变动后自动刷新；
- 改 `internal/api/projects.go` 的 `DeleteProject`：项目删除成功后调用 `memory.Service.PurgeProject`，按 `memory_documents` 逐个删除该项目的 document，失败的写入 `memory_tombstones`，后台重试；同时删除该项目的 mental models。

Accept：`go test ./internal/memory/... ./internal/api/... -run 'MemoryAdmin|Candidate|Purge|Export'` 输出 `ok`。测试覆盖 R19–R23。

## 阶段 3：Web 管理页

所有子步骤的共同约束：

- 遵守 `docs/design/web-guidelines.md`：只用 token，只用 Phosphor 图标，两个语言包都要更新；
- 失败信息要说明哪一步失败、为什么、下一步怎么做；
- 宽度 390 和 360 下都要可用；
- `web/src/mock/server.ts` 和 `mock/fixtures.ts` 要补上真实格式的数据。

### 步骤 3.1：页面骨架：状态、连接、`.env`、开关

Depends on：0.4。前端只依赖已冻结的 OpenAPI 和 mock，可与阶段 2 并行。
Files：

- `more-entries.ts`：Agent 组新增 `agent-memory`，图标 `brain`，路由 `/settings/agent-memory`，放在 `agent-extensions` 之后、`agent-config` 之前；
- `SettingsSection`、`App.tsx` 的 `SETTINGS`、`SettingsPage` 的渲染分支、`zh-CN.json` 和 `en.json` 的 `settings.agent-memory`；
- 新建 `web/src/pages/settings/agent/MemoryPage.tsx`，以及 `memory/StatusCard.tsx`、`memory/EnvFileDialog.tsx`、`memory/ToggleCard.tsx`；
- 新建 `web/src/api/memory-queries.ts`。

页面复用 `ApplyStatusBanner`，说明文字写「作用于 orb 与 runner，不作用于全局助手」。
Accept：`cd web && bun run check && bun run test` 通过。新增的 `MemoryPage.test.tsx` 覆盖：状态的各种显示（正常、不可达、版本警告、`suspect_empty`）；`.env` 生成后只显示一次；开关变化后出现待重启提示。`lib/sections.test.ts` 通过。

### 步骤 3.2：模型与接口配置

Depends on：3.1。
Files：新建 `memory/ModelsCard.tsx`、`memory/EndpointsCard.tsx`。
Do：

- 五种用途各有一个选择器，「跟随默认」是第一项。候选模型来自中央模型目录，协议类型不受支持的模型置灰，并说明原因。
- 选中的模型与已部署的协议类型不同时，显示 `.env` 片段、「需要重启 Hindsight」和「我已重启」按钮。
- 嵌入和重排各有一张配置卡片；嵌入锁定时，相关字段为只读，并附 reembed 文档的链接。

Accept：`cd web && bun run test -- memory` 通过，覆盖 R16 的提示、R17 的只读状态。

### 步骤 3.3：浏览、遗忘、全局记忆与候选

Depends on：3.1。
Files：新建 `memory/BrowseCard.tsx`（选择项目、列表、搜索、按线程遗忘、删除）、`memory/GlobalCard.tsx`（增删改）、`memory/CandidatesCard.tsx`（编辑后批准、拒绝）。破坏性操作用 `ConfirmButton`。
Accept：`cd web && bun run test -- memory` 通过，覆盖 R19 和 R20 的交互。

### 步骤 3.4：mental models、用量、导出

Depends on：3.1。
Files：新建 `memory/MentalModelsCard.tsx`、`memory/UsageCard.tsx`（24 小时 / 7 天，计数用 `CountPill`）、`memory/ExportCard.tsx`。
Accept：`cd web && bun run test -- memory` 通过；执行 `ORYN_SHOTS_DIR=/tmp/m16-shots bun run shots:all --only agent-memory`，生成亮色和暗色、1280 宽和 390 宽各一张截图，人工查看后无溢出、无错位。

## 阶段 4：部署与文档

### 步骤 4.1：compose 与文档

Depends on：0.2（变量取值）、2.1（`.env` 生成格式）。
Files：

- 新建 `deploy/hindsight/compose.yaml`，包含两个服务：
  - `hindsight-api:1.2-slim`：固定 `WORKER_ID`，`stop_grace_period: 75s`，端口发布为 `127.0.0.1:8888:8888`，禁用内嵌 pg0，`env_file: .env`；
  - pgroonga Postgres：固定镜像版本，使用命名卷；
- 新建 `deploy/hindsight/.env.example`，与 `envfile.go` 的输出键集一致，由测试保证；
- `docs/deploy.md` 新增「Hindsight 记忆服务」一节，内容见 R24；
- `README.md`：功能列表新增一条，runner 单向边界一句改为「仅开放记忆专用通道，不下发密钥」；
- `docs/protocol.md`：`agent.start.memory` 字段、`/memory/` 出站路径、`/memory-relay/` 中转入口、runner 的 `memoryEgress`。

Accept：

- `cp deploy/hindsight/.env.example deploy/hindsight/.env && docker compose -f deploy/hindsight/compose.yaml config` 退出 0（随后删除 `.env`）；
- `go test ./internal/memory/... -run EnvExample` 输出 `ok`，确认 `.env.example` 的键集与生成器一致；
- `grep -n 'Hindsight' docs/deploy.md README.md docs/protocol.md` 三个文件都有命中。

## 阶段 5：验收

### 步骤 5.1：Go 端到端

Depends on：2.2–2.6。
Files：新建 `internal/server/e2e_m16_test.go`。
Do：用 httptest 假 Hindsight 和假上游，从 egress handler 和 REST 两个入口驱动。覆盖以下场景：

- 两个项目，以及一个无项目线程；
- runner 线程；
- 候选的批准和拒绝；
- 别名换模型；
- 项目删除时的级联删除；
- 开关与待重启提示。

Accept：`go test ./internal/server/... -run E2EM16` 输出 `ok`；`make test` 通过。

### 步骤 5.2：真机验收

Depends on：5.1、3.4、4.1、1.2。
Files：新建 `web/scripts/m16-accept.ts`；在 `web/package.json` 中加入 `accept:m16`。
Do：

1. 用 4.1 的 compose 起真实的 Hindsight。
2. 在管理页生成 `.env`，填入真实模型和嵌入接口，启动后确认状态正常。
3. 在项目 P 的线程 1 里让 agent 记下一条中文约定，在线程 2（新 orb）里能召回；项目 Q 的线程召回不到。
4. 在 runner 上属于 P 的线程里也能召回。
5. agent 写一条全局记忆，它出现在候选队列里；批准之后，Q 能召回。
6. 删除 P 之后，P 的 document 数为 0。

Accept：`cd web && bun run accept:m16` 退出 0，输出逐项记录 R1、R4、R5、R6、R8、R13、R20、R23 的结果。

## 附录 A：Hindsight 探查结论（步骤 0.2）

完整内容见 `docs/plan-m16-appendix-a.md`。

## 附录 B：Core 端点与放行清单（步骤 0.3）

完整内容见 `docs/plan-m16-appendix-b.md`。
