# Oryn 协议索引

唯一协议源是 [`schema/openapi.yaml`](../schema/openapi.yaml)（OpenAPI 3.0.3）。本文只做导航，细节以规格为准。

## 生成与校验

|命令|作用|
|---|---|
|`make gen-go`|`go tool oapi-codegen` → `internal/apigen/apigen.gen.go`（models + chi server：`ServerInterface`、`Unimplemented`、`HandlerFromMux`）|
|`make gen-web`|`openapi-typescript` → `web/src/api/schema.d.ts`|
|`make gen`|以上两项 + `sqlc generate`|
|`make check-gen`|`gen` 后要求 `internal/apigen`、`internal/store/db`、`web/src/api` 无 diff|
|`make lint-spec`|Redocly 推荐规则集校验（跳过 `operation-4xx-response` 与 `no-unused-components`，原因见 Makefile）|

生成配置：`internal/apigen/cfg.yaml`。要点：`skip-prune`（WS / link / 任务参数 schema 不被 HTTP 操作引用也要生成）、`exclude-tags: [websocket]`（`/api/v1/ws` 与 `/orb/v1/link` 由手写处理器服务）、`name-normalizer: ToCamelCaseWithInitialisms`（Go 字段为 `ThreadID`、`OrbID` 等）。

## 通用约定

- 时间：UTC 毫秒整数（`TimestampMs`）。错误体：`Error{code, message}`，每个操作以 `default` 响应描述。
- 鉴权：Cookie `oryn_session`（变更类请求另需 `X-Oryn-CSRF: 1`，规格中为 `cookieAuth + csrfHeader` 组合）或 `Authorization: Bearer odt_…`；provider 用 `opv_…`，orb 用 `oot_…`。
- 路径参数中的 `/` 编码为 `%2F`：`/api/v1/agent-config/files/{path}`、`/api/v1/secrets/{name}`（chi 按 RawPath 路由，生成代码自动解码，见 `internal/apigen/apigen_test.go`）。
- omp RPC 命令与帧对控制面不透明：`OmpFrame`（任意对象），权威类型在 agent 仓库。

## 分区

|分区|规格位置|说明|
|---|---|---|
|C1 provider 协议|`/provider/v1/{hello,jobs/claim,jobs/{id}/complete,bye}`；`Provider*Request/Response`、`ProviderJob`、`Job{Spawn,Pause,Snapshot,Delete,DeleteSnapshot}Params`、`JobOp`、`JobState`|`ProviderJob.params` 是任意对象，按 `op` 解析为对应 `Job*Params`|
|C2 orb link|`/orb/v1/link`；`LinkStreamHeader`（按 `t` 判别：`ctl / agent / egress / blob`）；`LinkCtlRequest / LinkCtlResponse / LinkCtlNotification`；`LinkCtlMethod`；`LinkCtl<Method>Params / Result`|`fs.copyTree`、`fs.move`、`shutdown` 共用 `LinkCtlOkResult`；`resume.run` 的 params / result 与 `setup.run` 相同|
|C3 REST|`/api/v1/...` 全部端点|列表统一包一层对象（`{threads}`、`{orbs}`…）|
|C3 WebSocket|`/api/v1/ws?ticket=`；`WsClientMessage`、`WsServerMessage`（按 `type` 判别）；`AgentLiveState`|Go：`msg.ValueByDiscriminator()` 后类型分派；构造用 `FromWsServerXxx` 自动带 `type`|
|状态枚举|`ThreadState`（C4）、`OrbState`（E）、`AgentStatus`、`PauseMode`、`DeviceKind`、`SecretMode`、`ProviderDriver`|—|

## 相对计划的补充与偏差

- `entries.append` 定为 **orbd→orynd 请求**（计划表写作通知）：结果 `LinkCtlEntriesAppendResult{accepted, offset}`，`accepted=false` 时 orbd 从 `offset` 重发。这样「`baseOffset` 与游标不符时要求重发」才有应答通道。
- `agent.status` 增加可选 `restartsLeft`；`LinkStreamEgress` 不带 `threadId`（egress 监听按 orb 而非线程）。
- WebAuthn 仪式：`begin` 返回 `{ceremonyId, options}`，`finish` 回传 `ceremonyId` 与浏览器凭据 JSON。恢复：`auth/recovery` 发放一次性会话后，以该会话调用 `auth/setup/*`（省略 `token`）重新注册 Passkey。
- `ThreadPatch` / `ProjectPatch`：只改出现的字段；可选字符串传空串清除，`snoozedUntil` 传 0 清除（不使用 nullable 包装类型）。
- `PUT agent-config/files/{path}` 请求体为原始字节（`application/octet-stream`），权限经查询参数 `mode`；`POST agent-config/import` 为 `application/x-tar`，整体替换 omp-home。
