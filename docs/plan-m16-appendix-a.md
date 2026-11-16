# 附录 A：Hindsight 探查结论（步骤 0.2，2026-11-16 实测）

工作目录 `/tmp/m16-spike`。目录里有 compose、中转脚本、日志和导出包。所有请求日志在 `/tmp/m16-spike/logs/proxy.jsonl`，下文的 `seq=N` 指这个文件里的行。日志里的鉴权值都已脱敏（`<redacted relay-token-ok>` 表示 Hindsight 送来的是中转令牌）。我扫过 logs、compose、脚本，真实密钥的命中数都是 0。

## 总表

| # | 检查项 | 结论 | 一句话 |
|---|---|---|---|
| 1 | A1 标签召回语义 | **成立** | `any`：p1、global、无标签三类都返回，不返回 p2。`any_strict`：只返回 p1 和 global。建议网关用 `any_strict` |
| 2 | A2 mental model 用标签限定提炼范围 | **成立** | `tags=[oryn:global]` 加 `all_strict` 时，refresh 的 `based_on` 只有 global 记忆。和它冲突的 p1 偏好没有被带进去 |
| 3 | A5 各协议的路径、模型字段、鉴权 | **成立（有条件）** | openai、anthropic 的模型名在 body 的 `model` 里，gemini 的在 URL 路径里。**gemini 不认 `*_LLM_BASE_URL`**，只认进程级的 `GOOGLE_GEMINI_BASE_URL`。没设时，中转令牌会被直接发给 Google。所有调用都是非流式 |
| 3b | 各用途变量能否路由到对应操作 | **成立** | retain→`oryn-retain`，consolidation→`oryn-consolidation`，reflect→`oryn-reflect`，mental model→`oryn-mental-model`。`oryn-default` 只在启动自检时用到 |
| 3c | 中转改写模型名 | **成立** | 响应里的 `model` 是真实模型名，和别名不同，Hindsight 照常接受。中转运行中换映射也立即生效，不用重启 Hindsight |
| 4 | retain 应答 | **成立** | 同步和异步都返回 **HTTP 200**（不是 202），body 结构见 §4 |
| 5 | 删除 document 的级联 | **成立** | 派生的事实和 observation 都会消失，多来源 observation 会按剩下的来源重算。mental model **不会**自动更新，只标 `is_stale:true`。`GET /documents` 支持 tags 过滤，不支持 metadata 过滤 |
| 6 | `transfer/export` 流程 | **成立** | 返回 202 和 operation_id，轮询 `operations/{id}`，再从 `result_metadata.download_url` 下载 zip。下载接口要带 Bearer |
| 7 | `/version`、健康检查、DB 断开时的行为 | **成立，另有发现** | 运行中停掉 DB：`/health/ready` 返回 503，不会回退到 pg0。`DATABASE_URL` 未设时会静默改用空 pg0（native 文本检索下 ready=200），pgroonga 配置下则启动失败。1.2.1 **没有**禁用 pg0 的变量 |
| 8 | `HINDSIGHT_API_LLM_OUTPUT_LANGUAGE` | **成立** | 值是自由文本，原样拼进提示词。用 `Simplified Chinese` 时，英文原文抽出的事实也是中文 |
| 9 | 中文质量抽查 | **成立**（嵌入是真的，重排是模拟的） | 10 条查询 hit@3 都是 10/10，而且全部排第 1 |
| 10 | list、recall 返回的字段 | **成立** | world 事实带 `document_id`、`metadata`、`context`、`tags`。observation 的 `document_id` 为 null，`metadata` 为 `{}`，`tags` 继承来源事实 |

## 钉住的版本

| 组件 | 值 |
|---|---|
| Hindsight 镜像 | `ghcr.io/vectorize-io/hindsight-api:1.2-slim@sha256:084af5a6522d67f5905f7992cdb3d910d778496e5dd2566caec7cf82ed2e7d8c`。label 为 `org.opencontainers.image.version=1.2.1-slim`，revision 为 `0eb02d8c418eac6b489bc63883c993b2257dfac4`（即 tag `v1.2.1`），本地 image id `sha256:ca16abf3…`，大小约 1.2 GB |
| `/version` | `api_version: "1.2.1"` |
| Postgres 基础镜像 | `groonga/pgroonga:4.0.8-debian-17@sha256:15ab42f1b92749950da640b6d88f5563498a2ac06b2d0aaee1f9185afe34e376` |
| 自建 DB 镜像 | `m16-hindsight-db:pgroonga-4.0.8-pg17-pgvector-0.8.7`（`sha256:156f4134…`） |
| 扩展（实测 `pg_extension`） | PostgreSQL 17.12；pgroonga 4.0.8；vector 0.8.7；pg_trgm 1.6；btree_gin 1.3 |
| 镜像里的 SDK | openai 2.24.0，anthropic 0.99.0，google-genai 2.18.1，cohere 5.20.1 |
| 源码对照 | `/tmp/m16-spike/repo`，已 checkout `v1.2.1` |

**官方配方的问题**（步骤 4.1 要处理）：
- `docker/docker-compose/pgroonga/Dockerfile` 钉的 `postgresql-17-pgvector=0.8.6-1.pgdg13+1` 已经从 PGDG 源下架，今天构建直接失败（`apt` exit 100）。现在能装的是 `0.8.7-1.pgdg13+2` 和 `0.8.7-1.pgdg13+1`。
- 配方的 init 容器把主机名 `hindsight-db` 写死了。
- 配方用的应用镜像是 `hindsight:latest`，这是带控制台（9999 端口）的完整版，不是 api-slim。

## 上游与中转

- **中转**：`/tmp/m16-spike/proxy.py`，Python 标准库实现，监听在 compose 网络的网关 `172.31.216.1:7899`，只有该 bridge 网络和宿主机能访问。按路径前缀分协议族：`/openai/v1/*`、`/anthropic/*`、`/gemini/*`、`/embed/v1/*`、`/rerank/v1/rerank`。它做这几件事：换别名、去掉 Hindsight 送来的令牌、注入真实密钥，每个请求写一行 JSONL。
- **真实 LLM 上游**（密钥从 `~/.omp/agent/models.yml` 读入 `secret/upstreams.json`，权限 0600，没有打印过）：
  - openai 兼容：`openai` provider 的 base（`https://api.codehub.ling.plus/v1`）。映射为 `oryn-default` 和 `oryn-consolidation` → `gemini-3.5-flash-lite`，`oryn-reflect` 和 `oryn-mental-model` → `gpt-5.6-luna`。`oryn-retain` 起初是 `claude-haiku-5-5`，上游返回 529 `pool_overloaded`（seq=13–15）后，在运行中改成 `gemini-3.5-flash-lite`。
  - anthropic：`claude-pro`（`https://vm2api.ling.plus`），`claude-haiku-5-5`。
  - gemini：`google`（`https://api.codehub.ling.plus/v1beta`），`gemini-3-flash`。
  - 不可用的上游：axonhub 的模型都返回 503（限流）或 401（key 过期）；codehub 的 `deepseek-v4.1-flash` 返回 429。
- **嵌入**：`router.tumuer.me` 今天仍然是 503（返回一个 HTML 页，标题「我跑路了 · 下一站 EMRank」）。不带 UA 时会被 Cloudflare 拦截，返回 1010。models.yml 里各 provider 的 `/embeddings` 都是 404 或 422。最后用的是本机已有的 TEI 容器 `honcho-eval-embed`（`127.0.0.1:18090`，`Qwen/Qwen3-Embedding-0.6B`，1024 维）。这个容器属于 honcho 评测栈，我只读调用，没有改动它。**嵌入是真实的，不是假数据。**
- **重排**：找不到可用的 Cohere 兼容端点。中转用同一个真实嵌入模型计算余弦相似度 `(cos+1)/2`，按 Cohere 格式返回。所以重排这一步**没有独立的质量**，第 9 项的结果主要来自向量检索和 pgroonga 关键词检索。

## 生效的环境变量（密钥已脱敏）

```env
HINDSIGHT_API_DATABASE_URL=postgresql://hindsight_user:<redacted>@db:5432/hindsight_db
HINDSIGHT_API_VECTOR_EXTENSION=pgvector
HINDSIGHT_API_TEXT_SEARCH_EXTENSION=pgroonga
HINDSIGHT_API_WORKER_ID=m16-spike-worker-1
HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension
HINDSIGHT_API_TENANT_API_KEY=<redacted>
HINDSIGHT_API_LLM_OUTPUT_LANGUAGE=Simplified Chinese
HINDSIGHT_API_LLM_MAX_CONCURRENT=4
HINDSIGHT_API_WORKER_MAX_SLOTS=4
HINDSIGHT_API_LLM_PROVIDER=openai
HINDSIGHT_API_LLM_MODEL=oryn-default
HINDSIGHT_API_LLM_BASE_URL=http://172.31.216.1:7899/openai/v1
HINDSIGHT_API_LLM_API_KEY=<relay-token>
HINDSIGHT_API_RETAIN_LLM_{PROVIDER=openai, MODEL=oryn-retain, BASE_URL=http://172.31.216.1:7899/openai/v1, API_KEY=<relay-token>}
HINDSIGHT_API_REFLECT_LLM_{PROVIDER=openai, MODEL=oryn-reflect, BASE_URL=…/openai/v1, API_KEY=<relay-token>}
HINDSIGHT_API_CONSOLIDATION_LLM_{PROVIDER=openai, MODEL=oryn-consolidation, BASE_URL=…/openai/v1, API_KEY=<relay-token>}
HINDSIGHT_API_MENTAL_MODEL_REFRESH_LLM_{PROVIDER=openai, MODEL=oryn-mental-model, BASE_URL=…/openai/v1, API_KEY=<relay-token>}
GOOGLE_GEMINI_BASE_URL=http://172.31.216.1:7899/gemini        # 只要有用途选了 gemini 就必须设，见 §3
HINDSIGHT_API_EMBEDDINGS_PROVIDER=openai
HINDSIGHT_API_EMBEDDINGS_OPENAI_BASE_URL=http://172.31.216.1:7899/embed/v1
HINDSIGHT_API_EMBEDDINGS_OPENAI_MODEL=oryn-embed
HINDSIGHT_API_EMBEDDINGS_OPENAI_DIMENSIONS=1024
HINDSIGHT_API_EMBEDDINGS_OPENAI_API_KEY=<relay-token>
HINDSIGHT_API_RERANKER_PROVIDER=cohere
HINDSIGHT_API_RERANKER_COHERE_BASE_URL=http://172.31.216.1:7899/rerank/v1/rerank   # 完整 URL，原样 POST
HINDSIGHT_API_RERANKER_COHERE_MODEL=oryn-rerank
HINDSIGHT_API_RERANKER_COHERE_API_KEY=<relay-token>
```

镜像里自带 `HINDSIGHT_ENABLE_CP=false`，slim 版不启动控制台。另外两组 A5 实验各跑过一次：
- **实验 A**：`RETAIN=anthropic`（base 为 `…/anthropic`），`REFLECT=gemini`，`CONSOLIDATION=openai-responses`，`GOOGLE_GEMINI_BASE_URL` 为空。
- **实验 B**：同 A，但设置了 `GOOGLE_GEMINI_BASE_URL`。

## 运行中的栈（结束时保持运行）

- compose 项目：`m16-hindsight`。
- 容器：`m16-hindsight-db`、`m16-hindsight-api`（`127.0.0.1:18888->8888`）；`m16-hindsight-pgroonga-init` 是一次性容器，已退出。
- 网络：`m16-hindsight-net`（172.31.216.0/24）。
- 数据卷：`m16-hindsight-pg-data`。
- 中转进程：`python3 proxy.py`，用 setsid 脱离会话运行。
- 当前配置：全部用途都走 openai 兼容协议。

```bash
# 启动（db → 中转 → api；中转必须在网络建好之后再绑 172.31.216.1）
/tmp/m16-spike/up.sh
#   等价于：
#   cd /tmp/m16-spike && docker compose --env-file secret/.env -f compose.yaml up -d db pgroonga-init \
#     && (pgrep -f 'python3 proxy.py' || setsid nohup python3 proxy.py >> logs/proxy.out 2>&1 < /dev/null &) \
#     && docker compose --env-file secret/.env -f compose.yaml up -d api
# 停止（保留数据卷；加 -v 连数据一起删）
/tmp/m16-spike/down.sh        # = docker compose --env-file secret/.env -f compose.yaml down -t 75 [-v]; pkill -f 'python3 proxy.py'
# 调用示例
curl -H "Authorization: Bearer $(cat /tmp/m16-spike/secret/hindsight_api_key)" http://127.0.0.1:18888/v1/default/banks/oryn/stats
```

嵌入依赖外部容器 `honcho-eval-embed`（`127.0.0.1:18090`）。它停掉以后，retain 和 recall 都会失败。

---

## 1. A1 标签召回语义：成立

**数据**：bank 为 `oryn`，写入四条记忆：
- `doc-p1-a`：tag `project:p1`，goose。
- `doc-p2-a`：tag `project:p2`，atlas。
- `doc-global-a`：tag `oryn:global`，迁移必须写 Down。
- `doc-untagged-a`：无 tag，周五禁止上线。

consolidation 之后，每条还各派生出一条 observation。

**召回**：`POST /v1/default/banks/oryn/memories/recall {"query":"数据库迁移规范","tags":["project:p1","oryn:global"],"tags_match":<mode>}`，结果（类型, tags, document_id）如下：

| tags_match | 返回 |
|---|---|
| `any` | world `[oryn:global]` doc-global-a；obs `[project:p1]`；world `[project:p1]` doc-p1-a；obs `[oryn:global]`；world `[]` doc-untagged-a；obs `[]`。共 6 条，**不含 p2** |
| `all` | 只有 world `[]` doc-untagged-a 和 obs `[]`。没有记忆同时带两个标签，所以只剩无标签的 |
| `any_strict` | world/obs `[oryn:global]`，world/obs `[project:p1]`。共 4 条，**不含无标签、不含 p2** |
| `all_strict` | 0 条 |
| `exact` | 0 条 |

OpenAPI 中 `RecallRequest.tags_match` 的原文：`'any' (OR, includes untagged), 'all' (AND, includes untagged), 'any_strict' (OR, excludes untagged), 'all_strict' (AND, excludes untagged), 'exact' (set-equality…)`，默认 `any`。

**结论**：计划中 A1 的表述（「any 返回带其中某个标签的记忆，以及不带标签的记忆」）与实测一致。不过网关应该改用 **`any_strict`**：即使 bank 里混进了无标签记忆（比如有人直连 Hindsight 写入），也不会泄漏到各项目。用 `any_strict` 时一次召回就能同时拿到 project 和 global，不需要分两次召回再合并。

每次召回的上游调用：1 次 embed，加上有候选时 1 次 rerank（`all_strict` 没有候选，也就没有 rerank 调用）。

## 2. A2 mental model 只从全局记忆提炼：成立

先另外写入一对互相冲突的偏好：
- `doc-global-pref`（`oryn:global`）：「代码注释和提交信息一律使用简体中文」。
- `doc-p1-pref`（`project:p1`）：「p1 要求代码注释必须使用英文」。

然后创建 mental model：

```json
POST /v1/default/banks/oryn/mental-models
{"id":"user-preferences","name":"主人全局偏好","source_query":"主人的编码与注释偏好有哪些？",
 "tags":["oryn:global"],"max_tokens":1024,"trigger":{"tags_match":"all_strict","keep_trace":true}}
→ 200 {"mental_model_id":"user-preferences","operation_id":"779e04db-…"}
```

- 创建时会自动跑一次 refresh，operation 类型是 `refresh_mental_model`，完成后 `result_metadata.based_on_counts={"world":2,"observation":1,…}`。
- `GET …/mental-models/user-preferences` 返回的 `reflect_response.based_on` 中，world 只有两条：「主人偏好代码注释和提交信息一律使用简体中文」和「所有项目的数据库迁移都必须是可回滚的…」；observation 只有一条「主人偏好代码注释和提交信息一律使用简体中文。」。**p1 的英文注释偏好没有出现。** content 里也只写了简体中文这一条偏好（完整 JSON 见 `/tmp/m16-spike/logs/a2_mm.json`）。
- 手动刷新 `POST …/mental-models/user-preferences/refresh {}` 返回 `200 {"operation_id":"35ab…","status":"queued"}`。完成后 `based_on` 是 world 2 条、observation 2 条，仍然全部是 global。
- trigger 的 `tags_match` 不填时，OpenAPI 说明的默认值是：模型带 tags 时用 `all_strict`，不带时用 `any`。所以 orynd 显式写上 `all_strict` 只是保险。
- refresh 期间的上游调用只出现 `oryn-mental-model`（6 次 LLM 调用）、`oryn-embed` 和 `oryn-rerank`。
- **注意**：mental model 的内容不会因为来源 document 被删除而更新，详见 §5。

## 3. A5 各协议族的请求格式：成立（gemini 有条件）

所有调用都是**非流式**，请求体里没有 `stream` 字段，响应都是 `application/json`，日志里 `stream=false`。

| 协议族 | Hindsight 配置 | 实际请求 | 鉴权头 | 模型名位置 | 响应中的用量字段 |
|---|---|---|---|---|---|
| LLM `openai` | `*_LLM_BASE_URL=…/openai/v1` | `POST {base}/chat/completions`（seq=44）；UA `hindsight/openai-compatible/1.2.1` | `Authorization: Bearer <token>` | body `model` | `usage.prompt_tokens`、`completion_tokens`、`total_tokens`、`prompt_tokens_details.cached_tokens`、`completion_tokens_details.reasoning_tokens` |
| LLM `openai-responses`（provider 名字不同） | 同上 | `POST {base}/responses`（seq=342）；body 键为 `input`、`model`、`store:false`、`temperature`、`text` | 同上 | body `model` | `usage.input_tokens`、`output_tokens`、`total_tokens`、`input_tokens_details.cached_tokens`、`output_tokens_details.reasoning_tokens` |
| LLM `anthropic` | `*_LLM_BASE_URL=…/anthropic` | `POST {base}/v1/messages`（seq=339）；UA `AsyncAnthropic/Python 0.99.0`；body 键为 `max_tokens:64000`、`messages`、`model`、`system`（system 是 block 数组，用了 prompt caching） | `x-api-key: <token>`、`anthropic-version: 2023-06-01` | body `model` | `usage.input_tokens`、`output_tokens`、`cache_creation_input_tokens`、`cache_read_input_tokens`、`output_tokens_details.thinking_tokens` |
| LLM `gemini` | **`GOOGLE_GEMINI_BASE_URL=…/gemini`**（`*_LLM_BASE_URL` 被忽略） | `POST {GOOGLE_GEMINI_BASE_URL}/v1beta/models/oryn-reflect:generateContent`（seq=334、344）；body 键为 `contents`、`generationConfig`（`responseMimeType: application/json`、`responseJsonSchema`）、`systemInstruction` | `x-goog-api-key: <token>`，不用 `?key=`；另有 `x-goog-api-client: google-genai-sdk/2.18.1` | **URL 路径** `models/{model}:generateContent` | `usageMetadata.promptTokenCount`、`candidatesTokenCount`、`totalTokenCount`；模型名在 `modelVersion` |
| 嵌入 `openai` | `EMBEDDINGS_OPENAI_BASE_URL=…/embed/v1` | `POST {base}/embeddings`（seq=9、340）；body 为 `{"input":[…],"model":"oryn-embed","dimensions":1024,"encoding_format":"base64"}` | `Authorization: Bearer` | body `model` | `usage.prompt_tokens`、`total_tokens` |
| 重排 `cohere` | `RERANKER_COHERE_BASE_URL=<完整 URL>` | `POST <完整 URL>`，不拼路径（seq=348）；body 为 `{"model":"oryn-rerank","query":…,"documents":[str…],"return_documents":false}`，**没有 `top_n`**；UA `hindsight/1.2.1` | `Authorization: Bearer` | body `model` | 读取 `results[].index` 和 `results[].relevance_score`；`meta.billed_units.search_units` 是中转模拟的 |

**gemini 的关键发现（影响 2.5 和 3.2）**：

- 源码 `hindsight_api/engine/providers/gemini_llm.py:258` 是 `self._client = genai.Client(api_key=self.api_key)`，没有传 base_url；google-genai 只读进程级环境变量，见 `google/genai/_base_url.py:50` 的 `os.getenv('GOOGLE_GEMINI_BASE_URL')`。
- 实验 A（`REFLECT_LLM_PROVIDER=gemini`，`REFLECT_LLM_BASE_URL=http://172.31.216.1:7899/gemini`，`GOOGLE_GEMINI_BASE_URL` 为空）：中转**完全没有收到** gemini 请求。Hindsight 日志为 `[LLM_4XX_DUMP] provider=gemini model=oryn-reflect scope=verification code=400 … 'API key not valid. Please pass a valid API key.'`，说明**中转令牌被直接发到了 Google 公网**。启动仍然继续，只记一条 WARNING：`LLM connection verification failed for 'reflect' config`。之后 `/health/ready` 仍是 200。
- 实验 B：设置 `GOOGLE_GEMINI_BASE_URL` 后，seq=334 是 `GEMINI connection verification successfully`，reflect 的全部调用（seq=344–354）都经过中转，被改写到 `…/v1beta/models/gemini-3-flash:generateContent`。
- **对 orynd 的要求**：
  - 只要有任何用途选了 gemini，生成的 `.env` 必须带 `GOOGLE_GEMINI_BASE_URL=<中转>/gemini`。这个变量是全局的，所有 gemini 用途共用一个地址，各用途靠路径里的别名区分，这一点满足需要。
  - 中转令牌应当视为可能已泄漏：一旦配错，它会被发给第三方。所以中转令牌必须只能用于中转，不能等同于任何真实密钥（计划已经这样设计）。
  - 健康检查不能用来判断 LLM 配置是否正确。`/version` 的 `features.bank_llm_health=false`。

**各用途的路由**（全部用途都配置时，统计 proxy.jsonl 中别名与路径的组合次数）：

| 操作 | 观察到的别名 | 证据 |
|---|---|---|
| 启动自检（每个配置组发一次 `"Say 'ok'"`，`max_tokens:512`） | `oryn-default`、`oryn-retain`、`oryn-reflect`、`oryn-consolidation`、`oryn-mental-model` | seq=3–7 |
| retain 抽取事实（scope `retain_extract_facts`） | `oryn-retain` | seq=8、44–46、339 |
| consolidation（生成 observation） | `oryn-consolidation` | seq=11、28–31、342 |
| `POST /reflect` | `oryn-reflect` | reflect 期间只出现 `oryn-reflect`（3 次），gemini 实验中是 seq=344–354 |
| mental model 创建与刷新 | `oryn-mental-model` | §2 |
| `oryn-default` | 34 次调用**全部**是启动自检 | `jq 'select(.alias=="oryn-default")'`，第一条用户消息都是 `Say 'ok'` |

补充说明：
- 源码 `engine/memory_engine.py:6936` 的注释说明：没有配置 `MENTAL_MODEL_REFRESH_LLM_*` 时，mental model refresh 会共用 reflect 的 provider。
- 没有配置 VLM 时，VLM 回退到 retain 的配置（`memory_engine.py:3325`）。
- `oryn-default` 仍要映射：自检要用它；`provider=none` 的判断也基于它（`memory_engine.py:24633`）。

**别名改写**：
- 全部响应的 `model` 都是真实模型名（例如 seq=3 中别名 `oryn-default`，响应 `model` 为 `gemini-3.5-flash-lite`；anthropic 返回 `claude-haiku-5-5`；gemini 返回 `modelVersion: gemini-3-flash`），Hindsight 全部正常处理。
- Hindsight 自己的 `GET /v1/default/banks/oryn/llm-requests` 里记录的 `model` 是别名（例如 `oryn-embed`）。它也有用量统计 `llm-requests/stats`，但 `retention_days: 1`。
- 中转在运行中把 `oryn-retain` 从 `claude-haiku-5-5` 改成 `gemini-3.5-flash-lite`（重启的只是中转进程），正在重试的异步 retain 用新模型完成了（op `7092ff73…` 状态 `completed`）。这证明 R16「同协议换模型不用重启 Hindsight」可行。

**中转实现需要注意**：
- 嵌入请求带 `encoding_format:"base64"`。中转返回 float 数组时 SDK 也能接受（本次就是这样）。
- 嵌入请求带 `dimensions`，上游必须支持这个参数，或者由中转去掉。
- retain 的提示词里会带上 metadata，例如 `Metadata:\n  session_id: thread-p1-002`（seq=44 的 user 消息）。metadata 会发给 LLM 上游。

**附带发现：自检的成本**：
- DB 宕机期间 api 容器崩溃重启了 28 次（`restart: unless-stopped`），每次启动都先做 5 个 LLM 自检，然后才因为 migration 失败退出。约 6 分钟里产生了 150 多次真实计费调用（seq=111–261）。
- 可以用 `HINDSIGHT_API_SKIP_LLM_VERIFICATION=true`（`config.py:1031`、`config.py:5546`）关掉自检，我没有实测这个变量。另一个办法是给 compose 配重启退避。

## 4. retain 应答：成立

**异步**（orynd 需要伪造的就是这一份）：

```
POST /v1/default/banks/oryn/memories  {"items":[…3 条…],"async":true}
→ HTTP/1.1 200  Content-Type: application/json
{"success":true,"bank_id":"oryn","items_count":3,"async":true,"operation_id":"7092ff73-b1f4-4840-ae7e-423524107ff8","operation_ids":null,"usage":null}
```

- 可以由客户端提供 `operation_id`（UUID）。返回的 `operation_id` 和传入的一致，重复提交返回同一个 id，也不会产生新任务。OpenAPI 说明：同一个 id 如果属于另一个操作，返回 409。
- 后续轮询 `GET /v1/default/banks/oryn/operations/{id}`。处理中的样子：`{"operation_id":…,"status":"pending","operation_type":"batch_retain",…,"result_metadata":{"is_parent":true,"items_count":3,…},"child_operations":[{"operation_id":"9558…","status":"processing",…}]}`。完成后：`"status":"completed","result_metadata":{…,"unit_ids_count":3,"extraction_errors_count":0}`。
- **查询不存在的 id 返回 HTTP 200**，body 是 `{"operation_id":"<id>","status":"not_found","operation_type":null,…}`。如果 Core 会轮询 operation，orynd 的伪造 id 也要按这个格式返回（返回 `completed` 或 `not_found`），具体看 0.3 对 Core 的结论。

**同步**：

```
POST …/memories {"items":[1 条],"async":false}
→ 200 {"success":true,"bank_id":"oryn","items_count":1,"async":false,"operation_id":null,"operation_ids":null,
       "usage":{"input_tokens":4540,"output_tokens":214,"total_tokens":4754,"cached_tokens":0,"thoughts_tokens":119}}
```

- 同步请求要等 LLM 返回（实测 4.8–5.9 s）。
- 请求校验：`metadata` 的值必须是字符串，`{"n":1}` 会返回 422 `string_type`，位置 `["body","items",0,"metadata","n"]`。

## 5. 删除 document：成立；mental model 需要另行处理

- `DELETE /v1/default/banks/oryn/documents/doc-p2-a` → `200 {"success":true,"message":"Document 'doc-p2-a' and 1 associated memory units deleted successfully","document_id":"doc-p2-a","memory_units_deleted":1}`。
  - 删除前，`memories/list?tags=project:p2&tags_match=any_strict` 有 world `50e81c59`（doc-p2-a）和 observation `5a63914e`（`source_memory_ids:[50e81c59]`）。
  - 删除后 list 为空，召回（带标签和不带标签都试了）不再出现 atlas。`GET /documents/doc-p2-a` 返回 404 `Document not found`。
- **多来源 observation**：
  - 删除前，p3 的 `doc-p3-a` 和 `doc-p3-b` 合并出 observation `2173e9dd`（`proof_count:2`，`source:[4fab1132, eff8b816]`，内容含 shadcn/ui）。
  - 删除 `doc-p3-b` 后，这条 observation 消失，新生成了 `90e3833c`（`source:[4fab1132]`，内容只剩 React 19 和 Zustand，不再含 shadcn），召回结果也随之更新。
- **mental model 不会跟着变**：删除 `doc-global-pref` 之后，`user-preferences` 的 content 仍然写着「简体中文」，`based_on` 里仍然引用已删除的事实，只是 `is_stale` 变成 `true`。所以 R20 和 R23（批准、编辑、删除全局记忆，或删除项目）之后，orynd 必须主动调用 refresh，或者删除对应的 mental model。
- **`GET /documents` 的过滤能力**：
  - 参数有 `q, tags, tags_match(any|all|any_strict|all_strict|exact), time_field, start_date, end_date, limit, offset`，**没有 metadata 或 session 过滤**。
  - 实测 `tags=project:p1&tags_match=any_strict` 返回 3 个 p1 文档；用 `any` 会多出 `doc-untagged-a`。
  - 文档条目包含 `retain_params.metadata` 和 `document_metadata`（例如 `{"session_id":"thread-p1-003"}`）以及 `tags`。所以按线程遗忘只能二选一：orynd 自己维护 document 到线程的索引，或者分页拉 `/documents` 后在客户端按 `document_metadata.session_id` 过滤。
  - `memories/list` 有 `document_id` 参数，也有 tags 过滤。
- 另外：`DELETE /v1/default/banks/{id}/memories?type=` 是按类型清空整个 bank 的接口，网关必须拦住。`PATCH /memories/{id}` 可以编辑单条记忆。

## 6. `transfer/export`：成立

```
POST /v1/default/banks/oryn/transfer/export          （可选 query：include_data、include_bank_config、include_history、document_id[]）
→ 202 {"operation_id":"e87d7390-2c07-4091-ac1d-e48050f28d23","status":"pending"}
GET  /v1/default/banks/oryn/operations/e87d7390-…   （约 2 s 后）
→ 200 {"status":"completed","operation_type":"export_bank",
       "result_metadata":{"filename":"oryn-bank.zip","byte_size":14325,
         "storage_key":"tenants/public/banks/oryn/exports/ffd53824-…/transfer.zip",
         "download_url":"/v1/default/files/download/tenants/public/banks/oryn/exports/ffd53824-102d-4829-a1c0-907f6795dc9f/transfer.zip"}}
GET  <download_url>   （带 Bearer）
→ 200 Content-Type: application/zip, Content-Length: 14325, Content-Disposition: attachment; filename="oryn-documents.zip"
```

- 不带鉴权下载返回 401，所以 **orynd 必须代理下载**，不能把 URL 直接交给浏览器。
- key 中的 `/` 做 URL 编码后也能下载。
- 响应头里的文件名（`oryn-documents.zip`）和 `result_metadata.filename`（`oryn-bank.zip`）不一致，orynd 应该自己定文件名。
- 归档内容，共 14 KB：
  - `documents/000000.json`…`000006.json`：每个文档一份，含 `original_text`、`retain_params`、`tags`、`chunks`、`facts`。
  - `observations.json`、`mental_models.json`、`mental_model_history.json`、`banks.json`、`directives.json`、`webhooks.json`、`knowledge_pages.json`。
  - `data/async_operations.json` 等。
  - `manifest.json`：`schema_version:1`，`document_count:7`，`fact_count:7`，`observation_count:7`，`mental_model_count:1`，`scope:{data:true,bank_config:true,history:false}`。
- 存储：导出包默认存在 Postgres 的 `file_storage` 表里（BYTEA，`DEFAULT_FILE_STORAGE_TYPE="native"`，`config.py:1949`），会让数据库变大。
- 样本：`/tmp/m16-spike/logs/oryn-bank.zip`。

## 7. `/version`、健康检查与 pg0 回退：成立，另有发现

以下接口**不需要鉴权**；`/v1/*` 不带 key 时返回 `401 {"detail":"Authentication failed: Invalid API key"}`。

```
GET /version      → 200 {"api_version":"1.2.1","features":{"observations":true,"mcp":true,"worker":true,"bank_config_api":true,"bank_llm_health":false,"file_upload_api":true,"document_export_api":true,"document_import_api":true,"audit_log":false,"llm_trace":true,"store_document_text":true}}
GET /health/ready → 200 {"status":"healthy","database":"connected","db_acquire_ms":0.5,"db_pool_waiting":0,"db_pool_in_use":0,"db_pool_max":100,"db_pool_idle":5}
GET /health       → 与 ready 相同
GET /health/live  → 200 {"status":"alive","version":"1.2.1","uptime_seconds":68.5}
```

**运行中停掉 Postgres**（`docker stop m16-hindsight-db`）：
- `/health` 和 `/health/ready` 返回 **503** `{"status":"unhealthy","database":"error","error":"gaierror(-2, 'Name or service not known')"}`，耗时 3.7–5.4 s。
- `/health/live` 仍是 200，`/version` 仍是 200。
- `GET /v1/default/banks` 和 recall 在约 37 s 后返回 **500** `{"detail":"[Errno -2] Name or service not known"}`。
- 整个过程**没有**回退到 pg0。

**DB 停着时重启 api**：启动失败，报 `RuntimeError: Database migration failed` 和 `Application startup failed. Exiting.`，容器进入崩溃循环（对自检成本的影响见 §3）。DB 恢复后 api 自动恢复，bank 统计不变（`total_documents:7`）。

**没有设置 `HINDSIGHT_API_DATABASE_URL`**（issue #6485 的情形）：
- `config.py:4775` 是 `os.getenv(ENV_DATABASE_URL, DEFAULT_DATABASE_URL)`，`config.py:1235` 定义 `DEFAULT_DATABASE_URL = "pg0"`。注意：这里只在变量**未设置**时回退，设成空串不会触发。
- 实测 + `TEXT_SEARCH_EXTENSION=native`：日志显示 `Database: pg0` 和 `Starting embedded PostgreSQL (name=hindsight, port=auto)`，没有任何 WARNING。`/health/ready` 返回 **200 healthy/connected**，`/banks` 返回 `{"banks":[],"total":0}`，`GET /banks/oryn/stats` 返回 **404 `{"detail":"Bank 'oryn' not found"}`**。这就是 #6485 描述的静默空库。
- 实测 + `TEXT_SEARCH_EXTENSION=pgroonga`：pg0 没有 pgroonga，启动失败，报 `psycopg2.errors.FeatureNotSupported: extension "pgroonga" is not available`，相当于意外地拒绝了启动。
- **禁用开关**：1.2.1 没有禁用内嵌 pg0 的变量。`config.py` 的 545 个 `ENV_*` 中搜不到 embedded、pg0 相关的禁用项。`start-all.sh` 只做 pg0 数据目录检查（#675、#1483）。
- **替代做法**：
  1. compose 里写 `HINDSIGHT_API_DATABASE_URL: postgresql://…${HINDSIGHT_DB_PASSWORD:?}…`，用 `:?` 让变量缺失时 compose 直接拒绝启动（本栈就是这样写的）；
  2. 使用 pgroonga 本身就能挡住回退；
  3. orynd 的健康检查在 ready=200 之外，还要核对 `GET /v1/default/banks/oryn/stats`。它返回 404，或者 `total_documents` 比 orynd 已知的少很多时，应判为异常。

## 8. `HINDSIGHT_API_LLM_OUTPUT_LANGUAGE`：成立

- **取值格式**：自由文本，不做校验。`config.py:4805` 是 `llm_output_language=(os.getenv(ENV_LLM_OUTPUT_LANGUAGE) or None)`，`engine/prompt_utils.py:38-53` 把它原样拼成提示词：`IMPORTANT: Respond exclusively in {language}. Translate any source content into {language}. All output text — including fact text, observations, entity names, and the final response — must be in {language}.`。retain、consolidation、reflect 都会用到。
- **实测**：设为 `Simplified Chinese`，retain 提示词的第 128 行出现上面这句（seq=46 的 system 消息）。英文原文 `In project p1 we decided to use Redis Streams … because ops already runs Redis.`（doc-p1-en）抽出的事实是：`"what": "在项目 p1 中，决定使用 Redis Streams 代替 RabbitMQ 作为作业队列，因为运维团队已经在运行 Redis。"`，entities 也是中文或原名。observation、reflect 的回答和 mental model 的内容都是中文。
- **建议的固定值**：`Simplified Chinese`。拼进去的是英文提示词，所以用英文语言名最稳；写成 `简体中文` 理论上也能用，我没有测。

## 9. 中文质量抽查：成立（嵌入是真实的，重排是模拟的）

- **数据**：在 p1 写入 10 条中文工程约定，document_id 为 zh-01 到 zh-10，一次同步 retain，返回 200，消耗 38312 token。
- **查询方式**：用的是改写过的问法，不是原文关键词。参数为 `tags=[project:p1, oryn:global]`、`any_strict`。只要前 3 条结果中有一条是该文档的 world 事实，或者是由它派生的 observation，就算命中。

| doc | 查询 | 命中排名（全部类型） | 命中排名（只看 world） |
|---|---|---|---|
| zh-01 RFC 7807 错误格式 | 后端接口报错时应该返回什么格式？ | 1 | 1 |
| zh-02 SQLite WAL / busy_timeout | 数据库被锁住的报错怎么规避？ | 1 | 1 |
| zh-03 bun 装依赖 | 前端装包用什么工具？ | 1 | 1 |
| zh-04 go build / vet | 提交代码之前要做哪些检查？ | 1 | 1 |
| zh-05 slog JSON | 后端日志该用哪个库、什么格式？ | 1 | 1 |
| zh-06 密钥进加密库 | API key 之类的敏感信息应该怎么保存？ | 1 | 1 |
| zh-07 UTC 存储 | 时间戳要用哪个时区入库？ | 1 | 1 |
| zh-08 游标分页 | 列表接口怎么做翻页？ | 1 | 1 |
| zh-09 镜像钉 digest | 部署时镜像版本怎么固定？ | 1 | 1 |
| zh-10 深色主题 | 管理界面默认是什么主题风格？ | 1 | 1 |

- **结果**：hit@3 为 10/10，hit@1 也是 10/10。
- **pgroonga 中文关键词检索有效**：§1 的召回结果中，`scores.keyword=15.0`，`semantic=0.674`，`reranker=0.864`。
- **局限**：
  - 嵌入用的是 0.6B 模型（Qwen3-Embedding-0.6B），不是计划里的 8B；
  - 重排是用余弦相似度模拟的，没有真实 Cohere 或 Qwen reranker 的数据；
  - 样本只有 10 条，而且主题互不重叠，比真实场景容易。

## 10. list 与 recall 返回的字段：成立

`GET /v1/default/banks/oryn/memories/list` 返回 `{items,total,limit,offset}`。world 条目示例：

```json
{"id":"02e0164c-…","text":"项目 p1 的数据库迁移统一使用 goose 工具，迁移文件放在 migrations 目录下。 | 这是项目 p1 的工程约定。",
 "context":"p1 工程约定","fact_type":"world","document_id":"doc-p1-a","chunk_id":"oryn_doc-p1-a_0",
 "tags":["project:p1"],"metadata":{"source":"spike","session_id":"thread-p1-001"},"entities":"goose, migrations, p1",
 "proof_count":1,"state":"valid","source_memory_ids":[],"mentioned_at":"…","consolidated_at":"…"}
```

observation 条目：`"fact_type":"observation","document_id":null,"chunk_id":null,"context":"","metadata":{},"tags":["project:p1"],"source_memory_ids":["02e0164c-…"]`。

- **document_id**：原样保存客户端传入的值。world 的 `chunk_id` 格式为 `<bank>_<document_id>_<chunk_index>`。
- **metadata**：值必须是字符串（§4），只在 world 事实和 document（`document_metadata`）上保留；**observation 不带 metadata，也不带 document_id**。
- **tags**：list 和 recall 的每条结果都返回 `tags`，observation 继承来源事实的 tags。所以网关可以根据结果的 `tags` 做二次过滤，这是防御纵深的一层；observation 的归属判断要靠 `tags`，不能靠 `document_id`。
- **recall 结果**：每条含 `id, text, type, entities[], context, document_id, metadata, chunk_id, tags, source_fact_ids, scores{final,reranker,semantic,keyword}, mentioned_at`。顶层键为 `results, trace, entities, chunks, source_facts, source_facts_truncated`。
- **其他接口**：`GET /v1/default/banks/oryn/tags` 返回 `{"items":[{"tag":"project:p1","count":26},{"tag":"oryn:global","count":4},…]}`，可以按项目统计数量。`/stats` 返回 `total_documents`、`nodes_by_fact_type`、`pending_operations` 等。
- **鉴权**：所有 `/v1/*` 都只认 `Authorization: Bearer <HINDSIGHT_API_TENANT_API_KEY>`，没有其他必需的头或 ID。

---

## 给后续步骤的要点

1. **2.2 召回改写**：网关用 `tags=[project:<id>, oryn:global]` 加 `tags_match=any_strict`，一次请求就够。拿到结果后再按 `tags` 过滤一遍，不认识的标签一律丢弃。
2. **2.2 候选队列**：伪造的应答是 `200 {"success":true,"bank_id":"oryn","items_count":N,"async":true,"operation_id":"<uuid>","operation_ids":null,"usage":null}`。如果 Core 带了 `operation_id` 就原样返回。如果 Core 会轮询，还要伪造 `operations/{id}` 的应答。
3. **2.5 中转**：
   - 按路径识别协议族：`/chat/completions`、`/responses`、`/v1/messages`、`/v1beta/models/{alias}:generateContent`、`/embeddings`、Cohere 完整 URL。
   - 别名所在位置：openai、anthropic、嵌入、重排在 body 的 `model`，gemini 在 URL 路径。
   - 鉴权头依次是：Bearer、`x-api-key`、`x-goog-api-key`、Bearer、Bearer。
   - 全部是非流式调用，所以记录用量时直接读响应 JSON 即可，各协议的字段见 §3 的表。
   - `openai-responses` 是独立的 provider 名，和 `openai` 不同，如果要支持，协议族里要单独列出。
4. **3.1 生成 `.env`**：有用途选 gemini 时必须写 `GOOGLE_GEMINI_BASE_URL`。DB URL 用 `${…:?}` 写法。建议加上 `HINDSIGHT_API_SKIP_LLM_VERIFICATION=true` 或重启退避，这一条未测。
5. **2.6 删除联动**：删除 document 时，记忆和 observation 会自动级联。mental model 要主动 refresh 或删除，看 `is_stale` 就能判断。
6. **4.1 compose**：官方 pgroonga 配方今天构建失败，pgvector 要钉 `0.8.7-1.pgdg13+2`，并附上 digest。
