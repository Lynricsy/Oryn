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
|C2 orb link|`/orb/v1/link`；`LinkStreamHeader`（按 `t` 判别：`ctl / agent / egress / blob / fs`）；`LinkCtlRequest / LinkCtlResponse / LinkCtlNotification`；`LinkCtlMethod`；`LinkCtl<Method>Params / Result`|`fs.copyTree`、`fs.move`、`shutdown` 共用 `LinkCtlOkResult`；`resume.run` 的 params / result 与 `setup.run` 相同|
|C3 REST|`/api/v1/...` 全部端点|列表统一包一层对象（`{threads}`、`{orbs}`…）|
|C3 WebSocket|`/api/v1/ws?ticket=`；`WsClientMessage`、`WsServerMessage`（按 `type` 判别）；`AgentLiveState`|Go：`msg.ValueByDiscriminator()` 后类型分派；构造用 `FromWsServerXxx` 自动带 `type`|
|状态枚举|`ThreadState`（C4）、`OrbState`（E）、`AgentStatus`、`PauseMode`、`DeviceKind`、`SecretMode`、`ProviderDriver`|—|

## 相对计划的补充与偏差

- `entries.append` 定为 **orbd→orynd 请求**（计划表写作通知）：结果 `LinkCtlEntriesAppendResult{accepted, offset}`，`accepted=false` 时 orbd 从 `offset` 重发。这样「`baseOffset` 与游标不符时要求重发」才有应答通道。
- `agent.status` 增加可选 `restartsLeft`；`LinkStreamEgress` 不带 `threadId`（egress 监听按 orb 而非线程）。
- WebAuthn 仪式：`begin` 返回 `{ceremonyId, options}`，`finish` 回传 `ceremonyId` 与浏览器凭据 JSON。恢复：`auth/recovery` 发放一次性会话后，以该会话调用 `auth/setup/*`（省略 `token`）重新注册 Passkey。
- `ThreadPatch` / `ProjectPatch`：只改出现的字段；可选字符串传空串清除，`snoozedUntil` 传 0 清除（不使用 nullable 包装类型）。
- `PUT agent-config/files/{path}` 请求体为原始字节（`application/octet-stream`），权限经查询参数 `mode`；`POST agent-config/import` 为 `application/x-tar`，整体替换 omp-home。

## M3：终端 / 文件 / 改动 / 附件

M3 只新增两条通道：终端走现有 agent 流（WS `cmd` / `frame` 透传，控制面不保存终端状态），文件、改动与附件投递共用新的 `fs` 流。

### 流与消息语义

|项|规格位置|语义|
|---|---|---|
|`fs` 流|`LinkStreamFs`|**由 orynd 打开**（与 `egress` 方向相反）。流头之后是 HTTP/1.1，一条流一个请求（控制面 `http.Transport` 禁用 keep-alive）。orbd 把流交给本机 API 的 `http.Server`（`link.StreamListener` 虚拟监听），写方向带 60s 滚动截止时间；控制面开的其它类型流仍被 orbd 直接关闭|
|hello `features`|`LinkCtlHelloParams.features`|M3 的 orbd 发 `["fs","git","attachments"]`。缺省 = 旧版 orbd：控制面对其 `fs/*`、`git/*`、附件投递回 409 `orbd_outdated`；终端不受影响|
|`terminal.gap`|`LinkCtlMethod`、`LinkCtlTerminalGapParams`|orbd→orynd 通知。orbd 帧缓冲 ≥8MiB 时丢弃该线程的 `terminal_output` 帧（其它帧照常排队），缓冲回落到 ≤1MiB 后发一次；控制面转为 `term.lagged`|
|`term.watch`|`WsClientTermWatch{threadId, on}`|C→S。须先 `sub`（否则 `error{code:"not_subscribed"}`）。`terminal_output` 只发给开启的连接，线上仍是普通 `frame` 消息；`terminal_opened / exit / closed` 照常广播|
|`term.lagged`|`WsServerTermLagged{threadId}`|S→C。本连接待发超过 1MiB 时丢弃 `terminal_output`、关闭该连接的 `term.watch` 并发送（不断开连接）；或收到 orbd 的 `terminal.gap` 时发给全部开启者（不改开关）。客户端以 `terminal_list` + `terminal_snapshot` 重同步后重新 `term.watch{on:true}`|
|`term.resized`|`WsServerTermResized{threadId, terminalId, cols, rows}`|S→C。某连接的 `terminal_resize` 入队后发给该线程全部订阅者（含发起方）；omp 自身不为 resize 发帧|
|终端输出与空闲|—|`terminal_output` 不进实时缓冲、不算 agent 活动、不触发状态重算（开着 dev server 的 orb 仍能冻结）|

终端恢复的权威是 omp：`terminal_snapshot` 先 flush，此前发出的 `terminal_output` 的 `seq` 都 ≤ 快照 `seq` 且已含在 `data` 中；`seq` = 累计输出字符数；回滚 256K 字符。客户端规则：`start = seq − data.length`；`seq ≤ lastSeq` 丢弃；`start = lastSeq` 全写；`start < lastSeq` 写 `data[lastSeq−start:]`；`start > lastSeq` 视为缺口并重同步。

### REST（经 `fs` 流代理到 orbd）

全部沿用 C3 鉴权（变更类在 Cookie 鉴权下要 `X-Oryn-CSRF: 1`）与 `Error{code, message}`；路径参数一律放查询串（避开 `%2F` 的 RawPath 问题）。布尔查询参数（`bg`、`overwrite`、`inline`）由生成代码以 `strconv.ParseBool` 绑定，`1` 与 `true` 等价。

|方法与路径|operationId|参数 / 请求体|响应|
|---|---|---|---|
|`GET /api/v1/orbs/{id}/fs/roots`|`getOrbFsRoots`|`bg?`|`FsRoots`|
|`GET /api/v1/orbs/{id}/fs/list`|`listOrbFs`|`path`, `bg?`|`FsListing`|
|`GET /api/v1/orbs/{id}/fs/stat`|`statOrbFs`|`path`, `bg?`|`FsEntry`|
|`GET /api/v1/orbs/{id}/fs/file`|`readOrbFile`|`path`；头 `Range?`|`200` / `206` 字节（强制附件形式）|
|`PUT /api/v1/orbs/{id}/fs/file`|`writeOrbFile`|`path`, `overwrite?`(默认 true)；头 `If-Match?`；体 `application/octet-stream`|`FsEntry`|
|`POST /api/v1/orbs/{id}/fs/mkdir`|`mkdirOrbFs`|`FsPathRequest{path}`|`FsEntry`|
|`POST /api/v1/orbs/{id}/fs/rename`|`renameOrbFs`|`FsRenameRequest{from, to, overwrite?}`|`FsEntry`（目标）|
|`POST /api/v1/orbs/{id}/fs/delete`|`deleteOrbFs`|`FsPathRequest`|`204`|
|`GET /api/v1/orbs/{id}/git/status`|`getOrbGitStatus`|`root?`, `scope?`, `commit?`, `base?`, `bg?`|`GitStatus`|
|`GET /api/v1/orbs/{id}/git/diff`|`getOrbGitDiff`|`root?`, `scope?`, `commit?`, `base?`, `path`, `oldPath?`|`GitFileDiff`|
|`GET /api/v1/orbs/{id}/git/commits`|`listOrbGitCommits`|`root?`, `base?`, `limit?`(默认 100，≤200)|`GitCommitList`|
|`POST /api/v1/orbs/{id}/git/stage`|`stageOrbGit`|`GitPathsRequest{root?, paths[]}`|`204`|
|`POST /api/v1/orbs/{id}/git/unstage`|`unstageOrbGit`|同上|`204`|
|`POST /api/v1/attachments`|`uploadAttachment`|`name`(必填), `threadId?`；体 `application/octet-stream`|`201 Attachment`|
|`GET /api/v1/threads/{id}/attachments`|`listThreadAttachments`|—|`AttachmentList{attachments[]}`|
|`GET /api/v1/attachments/{id}/content`|`getAttachmentContent`|`inline?`；头 `Range?`|字节|
|`DELETE /api/v1/attachments/{id}`|`deleteAttachment`|—|`204`|

- `bg=1`：orb 不可用时直接 409 `orb_not_ready`，不唤醒、不计用户交互（Changes 面板的 10s 轮询用）。缺省：记一次用户交互并唤醒，至多等 25s。
- 代理不向 orbd 转发 `Cookie`、`Authorization`、`X-Oryn-CSRF`、`X-Forwarded-*`，删除 orbd 响应中的 `Set-Cookie`；`fs/file` 的 GET 强制 `Content-Disposition: attachment`、`X-Content-Type-Options: nosniff`、`Content-Security-Policy: sandbox`、`Cache-Control: no-store`，其余强制 `Content-Type: application/json`。
- `git/*` 的 `base` 缺省时控制面以 orb 所属项目的 `baseBranch` 补上。
- `CreateThreadRequest.attachments`（≤20 个 `att_…`）：建线程的事务中绑定并投递；`prompt`、`images`、`attachments` 至少其一非空。WS `cmd` 的 `prompt` / `steer` / `follow_up` / `abort_and_prompt` 帧可带 Oryn 扩展字段 `orynAttachments: string[]`，控制面投递后从帧里删除该字段，并在 `message` 末尾追加 `<oryn-attachments>` 块。
- `FsEntry.etag` = `"<mtimeUnixNano>-<size>"`（含引号，直接用作 `If-Match`），仅文件；`mime` / `text` 仅 `stat` 返回。

### 限额

|项|值|
|---|---|
|Files 单文件写入 / 上传|512MiB（`MaxFileBytes = 512<<20`，控制面与 orbd 各校验一次）|
|文本编辑器可打开|`text=true` 且 ≤2MiB；更大只读预览前 256KiB|
|目录列表|≤5000 条|
|diff patch|单文件 ≤1MiB（按行截断，`truncated`）；status ≤2000 个文件；commits ≤200|
|附件|单文件 100MiB；每条消息 ≤20 个；文件名净化后 ≤200 字节|
|唤醒等待|25s|
|hub 终端背压阈值|单连接待发 >1MiB 时丢弃 `terminal_output`|
|orbd 终端背压阈值|帧缓冲 ≥8MiB 丢弃 `terminal_output`，回落到 ≤1MiB 时发 `terminal.gap`|

### 错误码

|code|HTTP|来源|
|---|---|---|
|`invalid_path` / `invalid_request`|400|orbd|
|`outside_home` / `protected_path`|403|orbd|
|`not_found`|404|orbd / 控制面|
|`exists` / `is_a_directory` / `not_a_directory` / `not_a_repo` / `git_busy`|409|orbd|
|`modified`（`If-Match` 不符）|412|orbd|
|`too_large`|413|两端|
|`checksum_mismatch`|422|orbd（附件）|
|`git_failed`（附 stderr 尾部）|500|orbd|
|`no_space`|507|orbd|
|`orb_not_ready`（`bg=1` 且 orb 不可用）|409|控制面|
|`orbd_outdated`（hello 无对应特性）|409|控制面|
|`orb_deleted` / `no_orb`|410 / 409|控制面|
|`orb_unavailable`（唤醒超时，带 `Retry-After: 3`）|503|控制面|
|`orbd_unreachable`（流打开或传输失败）|502|控制面|
|`attachment_not_found`（附件不存在或已绑定到其它线程）|404|控制面|

### orbd 本机 API（`fs` 流上的 HTTP 路由）

`http.ServeMux`，错误体 `{code, message}`。路径接受 `~/…` 与绝对路径，响应一律为绝对路径；跟随符号链接后必须位于 `$HOME` 之下（`outside_home`），`$HOME` 本身只允许 list / stat；变更类操作拒绝 `$HOME/.oryn/**`（`protected_path`），`delete` / `rename` 拒绝当前工作目录本身。查询参数与 REST 同名（控制面已删去 `bg`）。

文件（`fs` 特性）：

|路由|实现要点|
|---|---|
|`GET /fs/roots`|`{home, workdir}`|
|`GET /fs/list?path=`|`os.ReadDir` + `Lstat`；符号链接带 `target` 与 `targetType`；目录在前、按名不分大小写排序；≤5000 条|
|`GET /fs/stat?path=`|`FsEntry` + `mime`（`mime.TypeByExtension`，缺省 `http.DetectContentType`）+ `text`（前 8KiB 无 NUL 且为合法 UTF-8，允许末尾截断的码点）|
|`GET /fs/file?path=`|目录 → 409 `is_a_directory`；`http.ServeContent`（Range），头 `ETag`|
|`PUT /fs/file?path=&overwrite=`|父目录须存在；已存在且 `overwrite=false` → 409 `exists`；`If-Match` 不符（含文件不存在）→ 412 `modified`；`http.MaxBytesReader(512MiB)`；写到 `$HOME/.oryn/tmp/up-<rand>`，`Sync`，沿用原文件权限（新文件 0644），`rename`（跨设备时复制后删除）；目标是符号链接时写入其指向的文件；任何失败删除临时文件；`ENOSPC` → 507。启动时清空 `$HOME/.oryn/tmp`；不做断点续传|
|`POST /fs/mkdir`|`FsPathRequest`；`os.MkdirAll(0o755)`|
|`POST /fs/rename`|`FsRenameRequest`；目标存在且未 `overwrite` → 409 `exists`|
|`POST /fs/delete`|`FsPathRequest`；`os.RemoveAll`|

改动（`git` 特性）：`exec git -C <top> -c core.quotepath=off -c color.ui=false …`，环境追加 `GIT_OPTIONAL_LOCKS=0 GIT_TERMINAL_PROMPT=0 LC_ALL=C`，每次调用 20s 超时。路径参数一律放在 `--` 之后并加 `:(literal)` 前缀；`commit` 须匹配 `^[0-9a-f]{7,40}$`，`base` 须匹配 `^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$`；`paths[]` 拒绝空串、NUL、绝对路径与 `..` 段。

- 仓库定位：`root` 缺省为当前工作目录；`git rev-parse --show-toplevel` 失败 → `GitStatus{repo:false, candidates}`（`root` 下一层含 `.git` 的子目录，≤20）。
- 基线：名称取 `base`；否则 `git symbolic-ref --quiet --short refs/remotes/origin/HEAD` 去掉 `origin/`；否则 `main`、`master` 中先存在者。引用取 `refs/remotes/origin/<name>`、`refs/heads/<name>` 中先能解析者；都没有或 HEAD 未出生 → 省略 `base`。`mergeBase = git merge-base HEAD <ref>`；失败 → 用 `<ref>` 的 sha 并置 `approximate:true`。

|`scope`|from|to|
|---|---|---|
|`all`（默认；无基线时等同 `uncommitted`）|`mergeBase`|工作区|
|`uncommitted`|`HEAD`（未出生用空树 `4b825dc642cb6eb9a060e54bf8d69288fbee4904`）|工作区|
|`commit`|`<sha>^1`（无父提交用空树）|`<sha>`|

|路由|实现要点|
|---|---|
|`GET /git/status`|`git diff --no-ext-diff -M --name-status -z <from> [<to>]` 与同参数 `--numstat -z`；to 为工作区时并入 `git ls-files --others --exclude-standard -z`（`untracked`；≤1MiB 的文本文件给出行数）与 `git status --porcelain=v2 -z --untracked-files=no` 得出的 `staged`（X≠`.` 且 Y=`.` → `full`；二者都≠`.` → `partial`；否则 `none`；`u` 记录 → `conflicted`）。numstat 两列都是 `-` → `binary:true`。超过 2000 个文件截断|
|`GET /git/diff`|`git diff --no-ext-diff -M -U3 <from> [<to>] -- <path> [<oldPath>]`；未跟踪文件用 `git diff --no-index -U3 -- /dev/null <abs>`（退出码 1 视为成功）。读到 1MiB 即终止进程、按最后一个换行截断并置 `truncated`。二进制 → `kind:"binary"`、`patch:""`；未跟踪且 >4MiB → `kind:"large"`|
|`GET /git/commits`|`git log --format=%H%x1f%h%x1f%an%x1f%ae%x1f%at%x1f%s%x1e -n <limit+1> <mergeBase>..HEAD`（无基线时为 `HEAD`）；`ts` 换算为毫秒|
|`POST /git/stage`|`GitPathsRequest`；`git add -A -- <paths>`|
|`POST /git/unstage`|`GitPathsRequest`；`git reset -q HEAD -- <paths>`（HEAD 未出生时 `git rm -r -q --cached -- <paths>`）。stderr 含 `index.lock` → 409 `git_busy`|

附件（`attachments` 特性；不经 REST 暴露）：

|路由|行为|
|---|---|
|`POST /attachments/ensure`|体 `OrbAttachmentEnsureRequest`；`path = $HOME/.oryn/attachments/<threadId>/<id>/<safeName>`；`present` = 该路径是大小相同的普通文件；结果 `OrbAttachmentEnsureResult`|
|`PUT /attachments/file?threadId=&id=&name=&sha256=`|`MaxBytesReader(100MiB)`；边写临时文件边算 sha256，不符 → 422 `checksum_mismatch`；`rename` 到位；返回 `OrbAttachmentEnsureResult{present:true, path}`|
|`DELETE /attachments?threadId=[&id=]`|删除单个附件目录或整个线程目录；`204`|

`threadId` 按 `agent.start` 的规则校验，`id` 须匹配 `^att_[0-9A-Z]{26}$`；`safeName`：取基名，`/ \ NUL` 与控制字符换成 `_`，前导 `.` 前加 `_`，保留扩展名截到 200 字节。该目录在仓库之外，附件不会进仓库。

### 存储

`0007_attachments.sql`：`attachments(id, thread_id → threads ON DELETE CASCADE, name, mime, size, sha256, created_at)`，索引 `(thread_id, created_at)`；`thread_id` 为 NULL 表示尚未绑定。查询见 `internal/store/queries/attachments.sql`（`BindAttachment` 为 `:execrows`：未绑定或已绑定同一线程时影响 1 行）。附件字节存 `<data_dir>/attachments/<id>`，不与转录 blob 共用 `blobs/`。
