# 附录 B：Core 端点与放行清单（步骤 0.3）

约定：`CA/` = `/root/Projects/Ling/oryn-core/packages/coding-agent/src/`；`H/` = `CA/hindsight/`。行号为 2026-11-16 读到的工作区版本。全文只读代码，没有发起任何网络请求。

## 结论速览

| 假设 | 结论 | 一句话证据 |
|---|---|---|
| A3 路径前缀保留 | **成立** | `${baseUrl}${path}` 直接字符串拼接，只剥掉尾部 `/`（`H/client.ts:286`、`H/client.ts:582`） |
| A6 document_id + metadata.session_id | **成立，带限定** | 自动写入 `document_id = sessionId`、`metadata.session_id`（`H/state.ts:409,440`）；工具写入的 document_id 是哈希，只能靠 URL 里的 threadId 归属 |
| `hindsight.globalScope` / `memory.backend` 经 `--config` 设置 | **都可以** | 都是普通注册项，overlay 参与分层（`CA/config/registry.ts:455-461` 注释、`CA/config/settings.ts:756-758,2871`）；`memory.backend` 没有 env |

---

## 1. A3：URL 构造与代理

**1.1 拼接代码**

- `H/client.ts:286`：`this.#baseUrl = options.baseUrl.replace(/\/+$/, "");` ——只去掉尾部斜杠，没有 `new URL()` 归一化、没有 `URL.pathname` 覆盖。
- `H/client.ts:581-587`：
  ```ts
  async #request(method, path, operation, opts) {
      let url = `${this.#baseUrl}${path}`;
      if (opts?.query) { const qs = buildQueryString(opts.query); if (qs) url += `?${qs}`; }
  ```
- 所有 `path` 都以 `/v1/default/banks/${encodeURIComponent(bankId)}` 开头（见 §3 表中行号）。
- 因此 `HINDSIGHT_API_URL=http://127.0.0.1:7811/memory/t/T1` 对**每个**端点都保留前缀，最终请求为 `http://127.0.0.1:7811/memory/t/T1/v1/default/banks/<bank>/...`。
- bank 做了 `encodeURIComponent`，threadId 只出现在前缀里，客户端不会改写它。注意 baseUrl 里**不能带 query**（拼接会把路径接到 `?` 之后），Oryn 的 URL 没有 query，无影响。
- 派生影响（均无害，记录供 2.x 参考）：
  - `H/outbox.ts:352-355` `destinationKey(apiUrl, bankId)` 把 `url.pathname` 纳入 outbox 目录哈希 → 不同 threadId 的 outbox 互相独立（每个 orb 的 agentDir 也独立）。
  - `H/state.ts:553-556` `#recallScope()` 把 `hindsightApiUrl` 纳入持久化 recall 的复用键 → 同一会话文件换了 threadId 后不会复用旧 recall，会重新召回一次。

**1.2 鉴权头**

- `H/client.ts:289-293`：固定头只有 `User-Agent`、`Content-Type: application/json`，以及（仅当配置了 token）`Authorization: Bearer <token>`。token 来自 `HINDSIGHT_API_TOKEN` / `hindsight.apiToken`（`H/settings.ts:30-33`）。orb 里**不应设置**真实 key；网关不依赖该头（设了也应忽略/丢弃）。
- outbox 落盘前会把 token 字面量替换为 `[REDACTED]`（`H/outbox.ts` `#clean`，约 :191-194）。

**1.3 代理**

- 客户端用裸 `fetch(url, init)`（`H/client.ts:600`），`init` 只有 `method/headers/signal/body`，**没有 `proxy` 选项**，Core 自己没有任何 HINDSIGHT 专用的代理处理。
- 走 Bun 原生行为：`CA/tools/browser/attach.ts:56-58` 注释写明 "Both `fetch` and Bun's `node:http` honor `HTTP_PROXY`/`HTTPS_PROXY` and forward even `127.0.0.1` requests to the proxy unless `NO_PROXY` covers them"；`CA/utils/fetch-timeout.ts:16-42` 里 `PROXY_ENV_VARS = HTTPS_PROXY/https_proxy/ALL_PROXY/all_proxy/HTTP_PROXY/http_proxy`，并对 SOCKS 报 `UnsupportedProxyProtocol`。
- 结论 [INFERENCE，基于上述两处代码注释，未实跑]：orb 环境里若有 `HTTP_PROXY`/`HTTPS_PROXY`，发往 `127.0.0.1:7811` 的记忆请求也会被送进代理。orbd 注入 env 时应同时设置 `NO_PROXY=127.0.0.1,localhost`（或保证 orb 无代理变量），否则 R5–R9 的整条链路会被代理截走。

---

## 2. A6：写入路径的请求体

三条写入路径，全部落到同一个端点 `POST /v1/default/banks/{bank}/memories`。

### 2.1 自动写入（transcript）

触发：`agent_end` 事件（`H/state.ts:686` → `maybeRetainOnAgentEnd`，`H/state.ts:453`），每 `retainEveryNTurns`（默认 3，`H/settings.ts:174-178`）个用户轮次一次；`/memory enqueue|rebuild|sync` 也会强制写一次（`H/state.ts:481`、`H/backend.ts:130-134`）。**只有主 agent 做，subagent 别名状态不做**（`H/backend.ts:38-41` 注释、`H/backend.ts:46-63`）。

调用：`H/state.ts:436-444` → `client.retain(bankId, transcript, {...})`，客户端按 `H/client.ts:303-321` 组包：

```jsonc
POST /v1/default/banks/{bank}/memories
{
  "items": [{
    "content":   "<transcript 全文或窗口>",
    "timestamp": "<会话头时间 ISO，带本地时区偏移>",        // state.ts:442
    "context":   "omp",                                    // config.retainContext，默认 "omp"（settings.ts:186）
    "metadata":  { "session_id": "<sessionId>" },          // state.ts:440
    "document_id": "<见下>",                               // state.ts:438
    "tags":      ["project:<label>"]                       // state.ts:441，= retainTags，仅 per-project-tagged 才有
  }],
  "async": true                                            // state.ts:443
}
```

- `document_id`：`retainMode=full-session`（默认，`H/settings.ts:151-`）→ **等于 `sessionId`**（`H/state.ts:409`）；`last-turn` → `` `${sessionId}-${毫秒时间戳}` ``（`H/state.ts:427`）。同一 session 反复写同一个 document_id（upsert 整篇 transcript，增量缓存只影响拼接，不影响请求体）。
- **没有** `update_mode`、`document_tags`、`observation_scopes`、`strategy`（`buildMemoryItem` 只在字段 !== undefined 时输出，`H/client.ts` 的 `buildMemoryItem`，约 :640-660；`retain()` 不传这些）。
- `project:<label>` 的 `<label>`：`ORYN_HINDSIGHT_PROJECT` 非空时用其小写值（`H/oryn-project.ts:13-16`、`H/bank.ts:84-87`），否则回退到 `basename(仓库根)` 小写（orb 里恒为 `repo`）。tag 只在 `scoping=per-project-tagged`（默认）时存在（`H/bank.ts:105-114`）。
- 失败处理：`H/state.ts:461-475` 捕获后只 `logger.warn`，`lastRetainedTurn` 不推进，下次 agent_end 重试；不弹 notice。

### 2.2 工具写入：`retain` / `learn`（经 outbox）

- 工具：`CA/tools/memory-retain.ts:127-137`、`CA/tools/learn.ts:129-136` → `state.enqueueRetain(toolCallId, items)`（`H/state.ts:346`）→ `HindsightRetainQueue.enqueue`（`H/state.ts:~128-141`）→ `HindsightOutbox.enqueue`（落盘）。
- 工具入参：`{ content, context?, scope?: "project"|"global" }`（`H/outbox.ts` `RetainInput`；`CA/tools/memory-retain.ts:27-28` scope 说明）。`scope` 只有 `hindsight.globalScope=true` 且 scoping≠per-project 才会出现在 schema / 不被拒（`H/state.ts:331-343`、`CA/memory-backend/global-scope.ts:22-33`）。
- 落盘事件 `OutboxEvent`（`H/outbox.ts:~176-195`）：`id = "omp-retain-" + sha256(JSON[sessionId, toolCallId, index, scope, context, content]).hex[0:32]`（`H/outbox.ts:181-184`），`tags = scope==="global" ? [] : (call.tags ?? [])`（`H/outbox.ts:186`），`context = item.context ?? retainContext`，`sessionId`。
- 投递（`H/outbox.ts:224-282`）：按 `tags` 集合分组，**每个请求只含同一 tag 集合的条目**（:224-232），每批最多 16 条（`BATCH_SIZE`，:16）。`retainBatch` 请求体（`H/client.ts:329-353`）：

```jsonc
POST /v1/default/banks/{bank}/memories
{
  "items": [{
    "content":   "<已脱敏文本>",
    "context":   "<item.context 或 omp>",
    "timestamp": "<入队时间 ISO>",
    "metadata":  { "session_id": "<event.sessionId>" },     // outbox.ts:277
    "tags":      ["project:<label>"],                      // 仅 project 项；global 项该键整体缺失（outbox.ts:279 → undefined → 被 pruneUndefined 去掉）
    "document_id": "omp-retain-<32 hex>",                  // outbox.ts:278
    "update_mode": "replace"                               // outbox.ts:280
  }],
  "async": true                                            // outbox.ts:282
  // 不含 document_tags（RetainBatchOptions.documentTags 在 Core 内从未被传入）
}
```

- `sessionId`：subagent 别名状态有**自己的** sessionId（`H/backend.ts:46-47` 的 `new HindsightSessionState({ sessionId, ... })`），所以子 agent 的 `retain`/`learn` 写出的 `metadata.session_id` 是子会话 id，不等于线程主会话 id。→ 网关归属 document 必须用 URL 中的 threadId，不能用 `metadata.session_id`。
- 失败语义（`H/outbox.ts:19,286` `REJECTED_PAYLOAD_STATUSES = {400,413,422}`）：这三个状态 → 隔离（quarantine，移到 `hindsight-outbox/quarantine`，弹 notice，`H/state.ts:~208-212`）；**其它任何非 2xx（含 403/409/503）都按可重试处理**：事件留在磁盘，指数退避（5 s 起，封顶 30 min，`H/state.ts:~190-235`），7 天后隔离（`EVENT_TTL_MS`）。且首次失败弹一条 "Memory retention failed" notice。→ 对 `retain`/`learn` 的候选队列分流必须返回 **2xx**，否则会无限重试并刷 notice；对 R14/R10（403/503）场景要接受 Core 侧这个重试行为。
- 单项被 400/413/422 拒绝会拆成单条重发（`H/outbox.ts` `#deliverBatch`，约 :243-262）。

### 2.3 `scope:"global"` 与 project 写入的请求体差异

| 项 | project | global |
|---|---|---|
| `items[].tags` | `["project:<label>"]` | **键缺失**（不是空数组，`pruneUndefined`/`buildMemoryItem` 丢掉；`H/outbox.ts:279`） |
| `document_tags` | 无 | 无 |
| `items[].document_id` | `omp-retain-…` | `omp-retain-…`（identity 含 scope，故同内容 project/global 的 id 不同，`H/outbox.ts:181`） |
| `update_mode` / `metadata` / `context` / `async` | 相同 | 相同 |
| 是否与 project 项同一请求 | 否 | 否（按 tag 集合分组，`H/outbox.ts:224-232`） |

要点与风险：
- 网关判定"global 写入"的可靠依据：**请求体 `items[]` 全部无 `tags`**。但 `scoping=global` 或 `per-project` 时 project 写入也无 tag（`H/bank.ts:99-103`）。→ Oryn 必须让 orb 保持 `hindsight.scoping=per-project-tagged`：`HINDSIGHT_SCOPING` 有 env，**env 优先于 overlay**（§5），所以 orbd 注入 env 时不得设置它或必须设为 `per-project-tagged`。
- 判定建议（[INFERENCE]）：网关以 URL 的 threadId→项目为准；无项目线程的一切写入进候选；有项目线程里 `items[].tags` 缺失/为空 → 候选（global）；`tags` 恰为 `[project:P]` → 强制改写成本项目标签后放行；其它任何 tags 取值 → 一律强制覆盖为 `[project:P]`（不采信），或拒绝。

---

## 3. 全部端点（网关放行清单依据）

所有路径前缀 `/v1/default/banks/{bank}`，`{bank}` = `encodeURIComponent(bankId)`，bankId = `HINDSIGHT_BANK_ID`（`H/settings.ts:46-47`）或默认 `omp`，若设了 `hindsight.bankIdPrefix` 则为 `<prefix>-<id>`（`H/bank.ts:60-64`）；per-project 时还会拼 `-<project>`（:100-103，Oryn 不用）。网关必须忽略路径里的 bank 并强制为 `oryn`。

客户端共 16 个方法，其中 Core 内**实际有调用点**的标 ✅，只在 client/protocol 里声明、全仓无调用的标 ❌（`grep` 全 `CA/` 排除 test 与 client.ts：`listMemories/listDocuments/getDocument/updateDocument/deleteDocument` 仅出现在 `CA/thread/services/memory.ts:41-45`、`memory-protocol.ts:67-71`、`memory-remote.ts` 的透传表里）。

| # | 方法 | 路径（相对 bank 根） | client.ts 行 | 请求体 / query 字段 | Core 调用点 | 用 |
|---|---|---|---|---|---|---|
| 1 | PUT | `/v1/default/banks/{bank}` | 398 | body: `reflect_mission?`、`retain_mission?`（来自 `hindsight.bankMission`/`retainMission`，默认都不设 → body 为 `{}`；`H/bank.ts:151-154`） | `ensureBankExists`：首次 retain 前（`H/state.ts:436`）、reflect 前（`CA/tools/memory-reflect.ts:79`）、mental model 引导（`H/state.ts:585`）、outbox 投递前（`H/outbox.ts:235`） | ✅ |
| 2 | POST | `…/memories`（retain/retainBatch） | 314 / 342 | body: `items[]{content,timestamp,context,metadata{session_id},document_id,tags?,update_mode?}`、`async:true`；**无 bank 字段，无 `document_tags`** | §2 | ✅ |
| 3 | POST | `…/memories/recall` | 359 | body: `query`、`types`（默认 `["world","experience"]`，`H/settings.ts:218`）、`max_tokens`（默认 1024）、`budget`（默认 mid）、`tags`（= `[project:<label>]`）、`tags_match`（= `"any"`，`H/bank.ts:113`） | 首轮自动 recall（`H/state.ts:357-365`）、压缩前 recall（`H/backend.ts` `preCompactionContext`→`recallForCompaction`，`H/state.ts` 同函数）、工具 `recall`（`CA/tools/memory-recall.ts:80-86`） | ✅ |
| 4 | POST | `…/reflect` | 379 | body: `query`、`context?`、`budget`（默认 recallBudget）、`tags`、`tags_match`（同上） | 工具 `reflect`（`CA/tools/memory-reflect.ts:80-85`） | ✅ |
| 5 | GET | `…/mental-models?detail=metadata\|content&limit=1000&offset=N` | 485-500 | query: `detail`、`limit=1000`（`MENTAL_MODEL_PAGE_SIZE`，`H/client.ts:22`）、`offset`。**无 tags 过滤参数** | 自动引导 `ensureMentalModels`（`H/mental-models.ts:152`，detail=metadata）、渲染 `tryLoadMentalModelsBlock`（:216，detail=content）、`/memory mm list|refresh|seed`（`command-controller.ts:1163,1211,1299`） | ✅ |
| 6 | GET | `…/mental-models/{id}?detail=content` | 509-511 | path id；query `detail`（404→null） | `/memory mm show|history`（`command-controller.ts:1181,1253`） | ✅（仅手动命令） |
| 7 | POST | `…/mental-models` | 528 | body: `id?`、`name`、`source_query`、`tags?`、`max_tokens?`、`trigger?{mode,refresh_after_consolidation}`（`H/client.ts:531-538`） | 自动 seed（`H/mental-models.ts:162-168`，仅 `mentalModelAutoSeed=true`）、`/memory mm seed`（`command-controller.ts:1309`） | ✅ |
| 8 | POST | `…/mental-models/{id}/refresh` | 548 | 无 body | `/memory mm refresh [id]`（`command-controller.ts:1202,1228`） | ✅（仅手动命令） |
| 9 | DELETE | `…/mental-models/{id}` | 558 | 无 body（404→false） | `/memory mm delete`（`command-controller.ts:1339`） | ✅（仅手动命令） |
| 10 | GET | `…/mental-models/{id}/history` | 573 | 无 | `/memory mm history`（`command-controller.ts:1254`） | ✅（仅手动命令） |
| 11 | GET | `…/memories/list` | 417 | query: `type,q,consolidation_state,limit,offset` | **无调用点** | ❌ |
| 12 | GET | `…/documents` | 436 | query: `limit,offset` | **无调用点** | ❌ |
| 13 | GET | `…/documents/{docId}` | 446 | — | **无调用点** | ❌ |
| 14 | PATCH | `…/documents/{docId}` | 456 | body: `tags` | **无调用点** | ❌ |
| 15 | DELETE | `…/documents/{docId}` | 469 | — | **无调用点** | ❌ |

（表头 5–10 的 `…` = `/v1/default/banks/{bank}`。#11–15 虽无调用点，但 subagent worker 的 `RemoteHindsightClient` 把它们透传给主线程 `HindsightApi`（`CA/thread/services/memory.ts:35-52`）；一旦 Core 将来增加调用即会出现，网关默认 403 即可。）

### 3.1 子 agent / worker 的网络出口

subagent worker 里的 `RemoteHindsightClient` 不自己发 HTTP：所有调用经 RPC 交给**主线程的同一个 `HindsightApi`**（`CA/thread/services/memory.ts:35-52,104-134`）。因此 orb 内所有 Hindsight HTTP 都出自同一个进程、同一个 baseUrl（含同一 threadId），不会出现第二个 URL。

### 3.2 Core 对响应的依赖（网关可伪造/过滤的范围）

`#request`（`H/client.ts:581-640`）：
- 成功 = `response.ok`（2xx）；body 经 `tryParseJson`，空/非 JSON → `{}`（`return (parsed ?? {})`）。非 2xx → `HindsightError(statusCode, details)`；`details` 取 body 的 `detail` ?? `message` ?? 全文，截断 2000 字（`MAX_ERROR_DETAIL_CHARS`）。`allow404` 时 404 → `null`，不抛错（用于 #6、#9、#13、#15）。
- 注意：**Core 不检查** `Content-Type`；伪造应答只需 2xx + 可选 JSON。

按端点：

| 端点 | Core 读取的响应字段 | 缺失/失败时 |
|---|---|---|
| retain / retainBatch（sync 与 async） | **不读任何字段**；2xx 即成功。`RetainResponse` 是 `{[k:string]:unknown}`（`H/client.ts` 类型）。自动写入 `await client.retain(...)` 返回后不使用返回值（`H/state.ts:437-444`）；outbox `#send` 返回 `"accepted"` 仅凭不抛错（`H/outbox.ts:270-285`） | 4xx/5xx 见 §2.2 重试/隔离规则 |
| recall | `response.results ?? []`（`H/state.ts:~361-366`、`memory-recall.ts:87`）；每项读 `text`（必需）、`type`（可选，` [type]`）、`mentioned_at`（可选，` (date)`）（`H/content.ts:66-72`、`H/client.ts` `RecallResult`）；`id` 不使用 | 失败 → 自动 recall 弹 notice 并本轮无 recall；工具 recall 抛错给模型 |
| reflect | `response.text?.trim()`；为空 → "No relevant information found to reflect on."（`CA/tools/memory-reflect.ts:86-87`） | 工具抛错 |
| createBank (PUT) | 不读；异常被吞（`H/bank.ts:166-170` 只 `logger.debug`），`banksSet.add` 只在成功后 | 失败无副作用，会在下次再 PUT |
| list mental-models | `items[]`：`id`、`name`、`tags`、`content`（须为非空字符串才渲染，`H/mental-models.ts:223`）、`trigger.refresh_after_consolidation`（bulk refresh，`command-controller.ts:1226-1227`）、`last_refreshed_at`/`source_query`（仅 mm show/list）；`total` 仅用于分页提前结束（`H/client.ts:495-496`：`pageItems.length < 1000` 先 break）。`bank_id` 字段不用 | 引导/渲染时 list 失败：`ensureMentalModels` 直接 return（`:154-156`）；渲染返回 `{ok:false}`，保留旧快照 |
| get mental-model | `id,name,tags,last_refreshed_at,source_query,content` | 404→null→"Mental model not found" |
| create mental-model | 不读返回值（`operation_id` 无人使用） | 失败仅 debug 日志（自动 seed）；`/memory mm seed` 里 showWarning |
| refresh | 不读 | `/memory mm refresh` showError/Warning |
| delete mental-model | 仅看 404 vs 其它（404→"not found"） | showError |
| history | 数组或 `{items:[]}`；每项 `previous_content`、`changed_at` | showError |

---

## 4. Mental models

**4.1 渲染选择机制：按"list 结果 + 本地 tag 过滤"，不按 seed id。**

- `H/state.ts:651-656` `tryLoadMentalModelsBlock(client, bankId, budget, this.recallTags)`；`H/mental-models.ts:216` 调 `listMentalModels(bankId,{detail:"content"})` 拉**整库**所有 model（翻页，每页 1000，`H/client.ts:485-500`）。
- 过滤（`H/mental-models.ts:222-224` + `modelVisibleForTags` `:257-262`）：保留 `content` 为非空字符串且满足
  - `visibleTags` 为空 → 全保留；
  - model **无 tags**（`tags` 缺失或 `[]`）→ 对所有 scope 可见；
  - model 有 tags → 必须 `tags.some(t => visibleTags.includes(t))`，其中 `visibleTags = recallTags = ["project:<label>"]`（`H/bank.ts:108-113`）。
- 然后按 `name` 排序渲染（`:231`），受 `hindsight.mentalModelMaxRenderChars`（默认 16000）预算裁剪。
- **关键后果（对 R6/R21）**：计划里的全局 `user-preferences` 若由 orynd 创建并打上 `tags:["oryn:global"]`，Core 的 `visibleTags` 里**没有** `oryn:global`（Core 从不把它加进去），该 model 会被 `modelVisibleForTags` **判为不可见，永远不进 `<mental_models>`**。可选方案：
  1. 网关在 list/get 响应里把 `oryn:global` model 的 `tags` 改写为 `[]`（Core 视为"无 tag，处处可见"）；同时过滤掉其它项目的 model（见 4.4）；
  2. 或 orynd 创建 `user-preferences` 时不打 tag（但 A2 要求 `tags=[oryn:global]` + strict 才能限定提炼范围，故只能在网关侧响应改写）；
  3. 或在 Core 加 `oryn-*` 小补丁，把 `oryn:global` 并入 `visibleTags`（需登记例外清单）。
  推荐 1：零 Core 改动。
- 网关改写 list 响应时，别忘 `total` 与分页：Core 只在 `pageItems.length >= 1000` 才继续翻页（`H/client.ts:495`），所以过滤后少于 1000 条时直接不再请求第二页；网关可自行翻上游全量再过滤，再按请求的 `limit/offset` 返回，或简化为忽略 offset≥1 的请求（返回空 `items`）。

**4.2 自动 seed 与 `user-preferences` 创建 403**

- seed 来自 `H/seeds.json`：`user-preferences`（`projectTagged:false`，无 `extra_tags` → **tags 为空 → `createMentalModel` 的 body 不含 `tags`**，`H/mental-models.ts:164` `tags: seed.tags.length > 0 ? seed.tags : undefined`）、`project-conventions`、`project-decisions`（`projectTagged:true`，tags = `retainTags` = `["project:<label>"]`，id 变为 `<seed.id>-<sanitized-label>`，`H/mental-models.ts:110-128`；sanitize = `[^A-Za-z0-9._-]+`→`-`，去首尾 `-`）。三者 `trigger: {mode:"delta", refresh_after_consolidation:true}`，`max_tokens` 600/800/800。
- 流程（`H/state.ts:585-598`）：`ensureBankExists` → 若 `hindsight.mentalModelAutoSeed`（默认 true，`H/settings.ts:273`）→ `ensureMentalModels`：先 `GET list detail=metadata`；对每个 seed 用 `seedAlreadyExists`（`:178-188`：`model.id === seed.id`，或 legacy 裸 id 且 tags 集合相同）判断，**已存在就不发 POST**；不存在才 `POST /mental-models`。
- **创建失败不影响后续**：`H/mental-models.ts:161-174` 每个 `createMentalModel` 包在 `try/catch`，只 `logger.debug("Hindsight: createMentalModel failed")`，无 notice、不抛；整个 `ensureMentalModels` 本身也不抛（list 失败时直接 return，:151-156）。随后 `#loadAndPublishMentalModels` 照常渲染（`H/state.ts:598`）。
- 所以场景"`user-preferences` 已存在但 POST 被 403"：只有当 **list(metadata) 里没有它** 才会尝试 POST。若网关 list 过滤策略保留了 `user-preferences`（id 命中）→ Core 不会发 POST；若把它从 list 里滤掉 → Core 会 POST，网关返回 403，Core 静默吞掉（仅 debug 日志）。两种情形 Core 都正常运行。→ 网关 list 响应必须保留 `user-preferences`（见 4.1）以免每次会话启动都多一次无谓 POST。
- 想彻底不发 POST：overlay 设 `hindsight.mentalModelAutoSeed: false`（默认 true；该项无 env，只能 overlay）。但这样项目的 `project-conventions-<P>` / `project-decisions-<P>` 也不会由 Core 创建，需 orynd 代建（计划 R21 的"按项目列出"依赖谁创建，需 2.6 步决定）。
- 无项目标识回退：`ORYN_HINDSIGHT_PROJECT` 未设置时 label=`repo`，seed id 会是 `project-conventions-repo`（计划已识别该冲突，`H/oryn-project.ts:1-6`）。

**4.3 Core 是否创建除 seed 之外的无 tag model**：不会。`createMentalModel` 全仓仅两处调用点：自动 seed（`H/mental-models.ts:162`）与 `/memory mm seed`（`command-controller.ts:1309`），内容都来自 `resolveSeedsForScope`。内置 seed 里唯一无 tag 的是 `user-preferences`。**没有**让 agent 自由创建 model 的工具（工具层只有 retain/recall/reflect/learn）。

**4.4 手动命令里的 model 写操作**（仅用户在 TUI/RPC 手敲，agent 工具不可达）：`/memory mm refresh [id]`（POST refresh；无 id 时先 list(content) 再对 `trigger.refresh_after_consolidation===true` 的全部 model 逐个 refresh，单个失败 showWarning 继续，`command-controller.ts:1196-1245`）、`/memory mm delete <id>`（`:1337-1339`）、`/memory mm seed`（`:1283-1326`，单个失败 showWarning）。→ 网关对"不属于本项目"的 id 返回 403 时 Core 会优雅展示错误，无崩溃；bulk refresh 会在 `user-preferences` 上得到 403 并 warning。

**4.5 list/get 的 query 参数**：`detail`（`metadata|content`，list 默认 content，`H/client.ts:486`；get 默认 content，:511）、`limit=1000`、`offset`。**无 tags / 过滤参数**，因此"只给本项目 + 全局 model"必须由网关在响应里过滤（Core 的本地过滤只是展示层，不能当安全边界：`/memory mm list/show/history` 会原样展示 list 返回的所有项，`command-controller.ts:1160-1177`）。

---

## 5. `--config` overlay 与 env

**5.1 overlay 加载机制**

- CLI：`--config <file>` 可重复（`CA/cli/flag-tables.ts:137-139`）；另有 env `PI_CONFIG_FILES`（路径分隔符分隔，`CA/config/settings.ts:756-758`）。文件按顺序加载并 deep merge（`settings.ts:2868-2875` `#readConfigOverlays`），严格模式：文件不存在/YAML 非法/顶层非 mapping → **启动报错**（`settings.ts:2890-2915` `#loadOverlayYaml`），不会静默回退。
- overlay 的 YAML 是嵌套映射，键对应点号路径：`memory: {backend: hindsight}`、`hindsight: {globalScope: true, mentalModelAutoSeed: false}`。`#migrateRawSettings` 会处理旧键（含 `memories.enabled`→`memory.backend`，仅当 `memory.backend` 未设，`settings.ts:3336-3345`；以及 `mnemosyne` 改名）。
- 优先级（`CA/config/registry.ts:455-461`）：运行时 override（CLI flag/会话内写入）> **env** > `--config` overlay > 项目 > 全局 > 默认。env 变量默认**压过所有 layer**（`registry.ts:68-69` 注释；`#effectiveEnv` `:546-556`：`envFallback` 为 false 时仅被 runtime override 压过；`get()` `:558-559`）。Hindsight 设置全部没有 `fallback`，所以 **env 一旦设置就盖掉 overlay**。

**5.2 各设置能否经 overlay 设置（`H/settings.ts`）**

| 设置 id | env 变量 | overlay 可设 | 默认 | 备注 |
|---|---|---|---|---|
| `memory.backend`（`CA/memory-backend/settings.ts:13`） | **无** | **是**（唯一途径） | `off` | 见 5.3 |
| `hindsight.apiUrl` (:17) | `HINDSIGHT_API_URL` | 是（但 env 盖掉） | `http://localhost:8888` | 必须配置，否则后端 inert（`H/backend.ts:70-75`） |
| `hindsight.apiToken` (:31) | `HINDSIGHT_API_TOKEN` | 是 | — | orb 不应设 |
| `hindsight.bankId` (:46) | `HINDSIGHT_BANK_ID` | 是 | 未设→`omp` | 网关强制 `oryn`，建议仍设 `oryn` |
| `hindsight.bankIdPrefix` (:59) | 无 | 是 | 空 | 不要设 |
| `hindsight.scoping` (:62) | `HINDSIGHT_SCOPING` | 是 | `per-project-tagged` | **必须保持 per-project-tagged**（§2.3） |
| `hindsight.globalScope` (:96) | **无** | **是**（唯一途径） | `false` | 开启后 `retain`/`learn` 才暴露 `scope:"global"`；需 scoping≠per-project（`H/state.ts:331-343`） |
| `hindsight.bankMission` (:110) | `HINDSIGHT_BANK_MISSION` | 是 | 空 | 会进 PUT bank 的 `reflect_mission` |
| `hindsight.retainMission` (:117) | 无 | 是 | — | 会进 PUT bank 的 `retain_mission` |
| `hindsight.autoRecall` (:123) | `HINDSIGHT_AUTO_RECALL` | 是 | true | |
| `hindsight.autoRetain` (:137) | `HINDSIGHT_AUTO_RETAIN` | 是 | true | |
| `hindsight.retainMode` (:151) | `HINDSIGHT_RETAIN_MODE` | 是 | `full-session` | |
| `hindsight.retainEveryNTurns` (:174) | `HINDSIGHT_RETAIN_EVERY_N_TURNS` | 是 | 3 | |
| `hindsight.retainOverlapTurns` (:181) | 无 | 是 | 2 | 仅 last-turn |
| `hindsight.retainContext` (:186) | 无 | 是 | `omp` | `context` 字段 |
| `hindsight.recallBudget/MaxTokens/ContextTurns/MaxQueryChars/Types` (:189-221) | 前四项有 `HINDSIGHT_RECALL_*`；`recallTypes` 无 | 是 | mid/1024/1/800/`["world","experience"]` | |
| `hindsight.debug` (:224) | `HINDSIGHT_DEBUG` | 是 | false | |
| `hindsight.{request,reflect,recall,retain}TimeoutMs` (:231-253) | `HINDSIGHT_*_TIMEOUT_MS` | 是 | 30s/120s/30s/60s | |
| `hindsight.mentalModelsEnabled` (:259) | **无** | 是 | true | 控制"读/渲染" |
| `hindsight.mentalModelAutoSeed` (:273) | **无** | **是**（唯一途径） | true | 控制"自动 POST seed" |
| `hindsight.mentalModelMaxRenderChars` (:287) | 无 | 是 | 16000 | |

- env 布尔解析：`#parseEnvText`（`registry.ts:645-647`）→ `parseFlag`（`CA/../utils/src/env.ts:560-563`）：空串 = 未设置；`1/y/Y/true/TRUE/yes/YES/on/ON` = true，**其余任何文本（含 `false`、`0`、`no`）= false**。
- 值非法时（如 enum 取值错）回退默认并告警（`registry.ts` `compute`，约 :656-668），不会报错。

**5.3 `memory.backend` 没有 env，只能由 overlay/配置写入**：`CA/memory-backend/settings.ts:13-` 的 `register({ id: "memory.backend", protocolDefault: ["rpc","acp"], type:"enum", values: [off, local, hindsight, mnemopi, sharpshooter], default:"off" })` **没有 `env` 字段**（对照 `H/settings.ts` 里带 `env:` 的项）。

`protocolDefault: ["rpc","acp"]` 的影响：rpc/acp 宿主启动时对该项调用 `pinDefault`（`CA/main.ts:299-302`、`CA/main.ts:2030-2032`），但 `pinDefault` 只在**没有任何持久层（全局、项目、`--config` overlay）配置它且 env 未设置**时才把默认值（`off`）钉为运行时 override（`registry.ts:779-788` 的注释与实现；`pinDefault` 在 :786-787）。→ 即使 orb 里的 omp 以 rpc/acp 模式运行，overlay 里写 `memory: {backend: hindsight}` 仍然生效，不会被钉成 `off`。（[INFERENCE]：未确认 orbd 具体用什么 mode 启动 omp；若发现 overlay 被忽略，应先查 `pinDefaultValue` 对 overlay 的判定 `settings.ts:1265`。）

**5.4 `HINDSIGHT_AUTO_RETAIN=false` 的效果与仍会写入的路径**

- 关闭：`H/state.ts:453-454` `maybeRetainOnAgentEnd` 开头 `if (!this.config.autoRetain) return;` → agent_end 不再写 transcript。
- **仍会写入 Hindsight 的路径**（均不看 `autoRetain`）：
  1. 工具 `retain` / `learn`（`CA/tools/memory-retain.ts:137`、`learn.ts:135`；`learn` 另需 `autolearn.enabled`，`learn.ts:78-79`）→ outbox → `POST /memories`；
  2. `/memory enqueue|rebuild|sync`（TUI `command-controller.ts:1054,1081`；RPC/ACP `CA/slash-commands/builtin-lifecycle.ts:640,654`）→ `H/backend.ts:130-134` → `forceRetainCurrentSession`（`H/state.ts:481-497`）：**会强制重发整篇 transcript，不受 autoRetain 约束**；
  3. 启动时 `void state.retainQueue.flush()`（`H/backend.ts:316`）与 agent_end/会话切换/dispose 时 flush（`H/state.ts:690`、`session-memory.ts:273`、`agent-session.ts:7104`）：投递此前落盘的 outbox 事件（含上次进程遗留的）。
- 另：`ensureBankExists` 的 PUT、mental-model 的 list/seed 引导、recall 与 reflect 都与 autoRetain 无关（`autoRecall=false` 才会关首轮 recall，`H/state.ts:~528`）。

---

## 6. `/memory stats` / `/memory diagnose` / `/memory view` 等

入口 `CommandController.handleMemoryCommand`（`command-controller.ts:1040-1110`，action 缺省为 `view`）；RPC/ACP 走 `slash-commands/builtin-lifecycle.ts`（同一批 hook）。

| 命令 | 实际行为 | 访问 Hindsight？ |
|---|---|---|
| `/memory view`（默认） | `backend.buildDeveloperInstructions`（`H/backend.ts:80-97`）：渲染静态指令 + **缓存**的 `mentalModelsSnippet` + `lastRecallSnippet`；全部来自内存缓存，无网络 | **否** |
| `/memory stats` | 取 `backend.stats`；`hindsightBackend` 对象只实现了 `start/buildDeveloperInstructions/beforeAgentStartPrompt/clear/enqueue/preCompactionContext/applySettings`（`H/backend.ts:46-160` 整个对象），**没有 `stats`、`diagnose`、`queuePreview`**（`CA/memory-backend/types.ts:151-157` 仅为可选声明；grep 全 `CA/memory-backend` 无 hindsight 实现） | **否**。显示 `Memory stats is not available for the hindsight backend.`（`CA/memory-backend/messages.ts:16-19`，`command-controller.ts:1094`） |
| `/memory diagnose` | 同上 | **否**，同样的提示 |
| `/memory queue` | `backend.queuePreview?.()` 未实现 → `Memory queue is not available for the hindsight backend.`（`command-controller.ts:1063-1069`） | 否 |
| `/memory reset|clear` | `hindsightBackend.clear`（`H/backend.ts:114-128`）：只 flush outbox + 丢本地状态，**不删服务端数据**（日志写明） | flush 时可能 POST retain |
| `/memory enqueue|rebuild|sync` | flush outbox + 强制整篇 transcript 重发 | **是**：`POST /memories`（retain、retainBatch）、PUT bank |
| `/memory mm list|show|refresh|history|seed|reload|delete` | 见 §3 #5-#10 | **是** |

结论：计划里"网关要对 stats/diagnose 放行或优雅失败"——**Core 对 hindsight 后端根本不为 stats/diagnose 发任何请求**，无需放行。R1 的"记忆数"必须由 orynd 自己查（与 Core 无关）。

---

## 7. 请求中 bank/tags 是否出现在 URL path/body 之外

逐个核对 `H/client.ts` 所有 16 个方法与 `#request`：

- **请求头**：只有 `User-Agent`（`USER_AGENT` 来自 `@oh-my-pi/pi-utils`）、`Content-Type`、可选 `Authorization`（`H/client.ts:289-293`）。**无任何自定义 header 携带 bank/tags/project/thread**。
- **Query string**：仅 `listMentalModels`（`detail,limit,offset`）、`getMentalModel`（`detail`）、`listMemories`（`type,q,consolidation_state,limit,offset`，无调用）、`listDocuments`（`limit,offset`，无调用）。**无 bank、无 tags**。
- **bank**：只出现在 URL path（`/v1/default/banks/{bank}`）。body 里不含 `bank_id`（`createBank` 的 body 只有 missions；`createMentalModel` 的 body 无 bank）。
- **tags**：只出现在 body：retain items 的 `items[].tags`（project 项）、recall/reflect 的 `tags`+`tags_match`、createMentalModel 的 `tags`、updateDocument 的 `tags`（无调用）。`document_tags` 字段 Core 从不发送。
- **响应里的 bank/tag 回显**：Core 不依赖 `bank_id`（mental model 的 `bank_id` 字段只在类型里声明），网关改写响应时不必保留。
- **非 HTTP 通道**：`ORYN_HINDSIGHT_PROJECT` 只影响本地生成的 tag/seed id（`H/oryn-project.ts`、`H/bank.ts:84-87`），不会以 header 形式发送；网关应从 URL 的 threadId 得到项目，不能期待请求里带项目头。

---

## 网关放行清单

路径"相对 bank 根"即 `/v1/default/banks/{bank}` 之后的部分；`{bank}` 一律忽略并强制为 `oryn`。其它方法/路径一律 403（含 Core 无调用点的 #11–15）。

| 方法 | 路径模式（相对 bank 根） | 需强制/改写的字段 | 响应需过滤的字段 | Core 是否依赖成功 |
|---|---|---|---|---|
| PUT | `` （bank 根本身） | 路径 bank → `oryn`；**丢弃 body**（`reflect_mission`/`retain_mission`，避免任一 orb 改写共享 bank 的 mission），可直接合成 200 `{}` 而不转发 | 无（Core 不读） | 否：失败被吞（`H/bank.ts:166-170`），但每次会重发 |
| POST | `/memories` | `items[].tags` 强制为 `["project:<P>"]`（不采信请求值）；**`items[]` 无 tags 或无项目线程 → 整个请求改走候选队列，不转发**；强制 `async:true` 不必；丢弃/忽略 `document_tags`（Core 不发）；`items[].document_id` 保持，同时登记 `document_id→项目/线程`（threadId 取自 URL，不取 `metadata.session_id`，subagent 的 session id 与线程不同）；`metadata.session_id` 可保留 | 无需过滤；**候选分流时必须返回 2xx 且为合法 JSON 或空 body**（Core 不读字段；Appendix A 记录的 async 应答可原样伪造） | **是（弱）**：非 2xx → 自动写入下轮重试；outbox 对非 400/413/422 无限退避重试 7 天并弹 notice；400/413/422 → 事件被隔离 |
| POST | `/memories/recall` | `tags` → `["project:<P>","oryn:global"]`；`tags_match` → 按 A1 结论（`any`/`any_strict`）；`types/budget/max_tokens` 可限上界 | 无需过滤（服务端已按 tag 限定）；`results[].text` 必需，`type`/`mentioned_at` 可选 | 是：自动首轮 recall 失败 → 弹 notice 但会话继续；工具 `recall` 抛错 |
| POST | `/reflect` | 同上：`tags`、`tags_match` 强制 | `text` | 否（工具层报错给模型） |
| GET | `/mental-models`（query: `detail`、`limit`、`offset`） | 无请求字段；`detail` 可放行 metadata/content，**拒绝/降级 `full`**（含 200KB 溯源，`H/client.ts:480-484` 注释） | **必须按项目过滤 `items[]`**：保留 `tags` 含 `project:<P>`、`oryn:global`；其它项目的 model 删掉；`oryn:global` model 的 `tags` 改写为 `[]`（否则 Core 的 `modelVisibleForTags` 会因 `visibleTags=[project:P]` 把它隐藏，`H/mental-models.ts:257-262`）；保留 `user-preferences` 的 id（避免 Core 去 POST 创建）；`total` 与 `items.length` 保持一致；去掉 `reflect_response` | 否：失败 → 保留旧快照 / 不渲染；`ensureMentalModels` 吞错 |
| GET | `/mental-models/{id}` | id 属于本项目或 `oryn:global` 才放行，否则 403/404 | 同上（tags 改写、不返回他项目 model） | 否（仅 `/memory mm show|history`） |
| POST | `/mental-models` | R9：`id` 必须等于 `project-conventions-<sanitize(P)>` 或 `project-decisions-<sanitize(P)>`，且 `tags` 恰为 `["project:<P>"]`；`user-preferences` 或任何无 tag/他项目 → **403**；`trigger/max_tokens` 可钉死成 seed 值 | 无（Core 不读 `operation_id`） | **否**：403 被静默吞（`H/mental-models.ts:171-173`），仅 debug 日志；`/memory mm seed` 会 showWarning |
| POST | `/mental-models/{id}/refresh` | id 必须属于本项目（`oryn:global`/他项目 → 403） | 无 | 否（仅手动命令，bulk refresh 里 403 只 warning） |
| DELETE | `/mental-models/{id}` | R9：始终 403（或仅允许本项目 id，计划 R9 只要求非本项目 403；建议 orb 一律 403） | 无 | 否（仅 `/memory mm delete`） |
| GET | `/mental-models/{id}/history` | id 属于本项目或 `oryn:global` | 数组/`{items}`；每项 `previous_content`、`changed_at` | 否（仅 `/memory mm history`） |
| GET/PATCH/DELETE | `/documents*`、`/memories/list` | **默认 403**：Core 无调用点（客户端有方法但全仓无调用，§3） | — | 否 |

补充约束（网关实现时）：
1. **服务端地址里没有 bank 之外的任何身份信息**：threadId 只在 `/memory/t/<threadId>` 前缀（§1）；网关据此校验"线程属于发出请求的 orb"（R14）并推出项目（R5）。
2. **请求头里的 `Authorization` 一律丢弃**，转发时换成真实 Hindsight key（R7）；响应体里若出现 key 要替换。
3. orb 的环境至少需要：`HINDSIGHT_API_URL=http://127.0.0.1:7811/memory/t/<threadId>`、`ORYN_HINDSIGHT_PROJECT=<项目ID>`（有项目线程）、`NO_PROXY=127.0.0.1,localhost`；`HINDSIGHT_BANK_ID=oryn`（可选，网关反正强制）；**不要**设置 `HINDSIGHT_SCOPING`（或设为 `per-project-tagged`）、`HINDSIGHT_API_TOKEN`。overlay：`memory: {backend: hindsight}`、`hindsight: {globalScope: true}`（若启用 R8 的 agent 提议全局记忆；不启用则 `scope` 参数不会暴露给模型）、可选 `hindsight: {mentalModelAutoSeed: false}`；`HINDSIGHT_AUTO_RETAIN=false` 可由 R11 的"项目关闭自动写入"使用（仍不能阻止 `/memory enqueue` 与工具写入，§5.4）。
4. R10（全局开关关闭返回 503 `memory_disabled`）与 R14（403）对 Core 的影响：recall/reflect 失败会弹 notice 但不中断；写入路径对 503 会无限退避重试，用户会看到 "Memory retention failed … kept in the Hindsight outbox" 一次（每次停电一条）。若不想 outbox 里堆积，应让 orbd 在关闭时不再给新 agent 启用 `memory.backend`（R10 本身已规定），对已运行 agent 的 outbox 事件，将在重启后因 `memory.backend` 不再 hindsight 而不再投递。
