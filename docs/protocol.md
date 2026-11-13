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
- 鉴权：Cookie `__Host-oryn_session`（变更类请求另需 `X-Oryn-CSRF: 1`，规格中为 `cookieAuth + csrfHeader` 组合）或 `Authorization: Bearer odt_…`；provider 用 `opv_…`，orb 用 `oot_…`。
- 路径参数中的 `/` 编码为 `%2F`：`/api/v1/agent-config/files/{path}`、`/api/v1/secrets/{name}`（chi 按 RawPath 路由，生成代码自动解码，见 `internal/apigen/apigen_test.go`）。
- omp RPC 命令与帧对控制面不透明：`OmpFrame`（任意对象），权威类型在 agent 仓库。

## 分区

|分区|规格位置|说明|
|---|---|---|
|C1 provider 协议|`/provider/v1/{hello,jobs/claim,jobs/{id}/complete,bye}`；`Provider*Request/Response`、`ProviderJob`、`Job{Spawn,Pause,Snapshot,Delete,DeleteSnapshot}Params`、`JobOp`、`JobState`|`ProviderJob.params` 是任意对象，按 `op` 解析为对应 `Job*Params`|
|C2 orb link|`/orb/v1/link`；`LinkStreamHeader`（按 `t` 判别：`ctl / agent / egress / blob / fs / portal`）；`LinkCtlRequest / LinkCtlResponse / LinkCtlNotification`；`LinkCtlMethod`；`LinkCtl<Method>Params / Result`|`fs.copyTree`、`fs.move`、`shutdown`、`portal.configure` 共用 `LinkCtlOkResult`；`resume.run` 的 params / result 与 `setup.run` 相同|
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
- 代理只向 orbd 转发白名单内的请求头（`Content-Type`、`Content-Length`、`Range`、`If-Range`、`If-Match`、`If-None-Match`、`If-Modified-Since`、`Accept`），`Cookie`、`Authorization`、`X-Oryn-CSRF`、`X-Forwarded-*`、前置代理加的身份头与浏览器指纹头一律丢弃；orbd 响应只放行 `Content-Type`、`Content-Length`、`Content-Range`、`Accept-Ranges`、`Etag`、`Last-Modified`（`Set-Cookie` 等一律删除）；`fs/file` 的 GET 强制 `Content-Disposition: attachment`、`X-Content-Type-Options: nosniff`、`Content-Security-Policy: sandbox`、`Cache-Control: no-store`，其余强制 `Content-Type: application/json`。
- orbd 一律视为不可信：只接受 orbd 本机 API 会返回的状态码（200、204、206、304、400、403、404、409、412、413、416、422、500、507），其余（1xx / 101、3xx、401 等）回 502 `orbd_bad_response`；1xx 中间响应不转发；JSON 响应体上限 32MiB（声明长度超限回 502 `orbd_bad_response`，未声明长度的读到上限即中止响应）。
- 截止时间：JSON 路由一次 orbd 调用（含响应体）45s，`fs/rename` / `fs/delete` 5min；`fs/file` 的 GET 等响应头 30s，之后下载不限时；`fs/file` 的 PUT（上传）不设截止时间，由客户端取消。超时回 504 `orbd_timeout`。
- `git/*` 的 `base` 缺省时控制面以 orb 所属项目的 `baseBranch` 补上。
- `CreateThreadRequest.attachments`（≤20 个 `att_…`）：建线程的事务中绑定并投递；`prompt`、`images`、`attachments` 至少其一非空。WS `cmd` 的 `prompt` / `steer` / `follow_up` / `abort_and_prompt` 帧可带 Oryn 扩展字段 `orynAttachments: string[]`，控制面投递后从帧里删除该字段，并在 `message` 末尾追加 `<oryn-attachments>` 块。这四类消息命令按受理顺序下发：带附件的消息受理时即占位，投递完成后才下发，其后受理的消息不越过它；其它命令（终端输入、`abort` 等）不等投递。客户端消息投递失败以失败应答结束该命令；控制面关闭打断的投递不算失败，新建线程的首条提示词留待重启后重新投递。
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
|`orbd_bad_response`（状态码不在白名单、JSON 响应体超长）|502|控制面|
|`orbd_timeout`（orbd 未在截止时间内应答）|504|控制面|
|`attachment_not_found`（附件不存在或已绑定到其它线程）|404|控制面|

### orbd 本机 API（`fs` 流上的 HTTP 路由）

`http.ServeMux`，错误体 `{code, message}`。路径接受 `~/…` 与绝对路径，响应一律为绝对路径；跟随符号链接后必须位于 `$HOME` 之下（`outside_home`），`$HOME` 本身只允许 list / stat；变更类操作拒绝 `$HOME/.oryn/**`（`protected_path`；展开后的目标、词法路径与目录项本身任一落在其下即拒绝，`~/.oryn/foo -> ~/work` 这样的链接也不能删除或移走），`delete` / `rename` 拒绝当前工作目录本身。查询参数与 REST 同名（控制面已删去 `bg`）。

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

## M4：Portal 与受管服务

orbd 监督 `.oryn/services.yaml`（回退 `.amp/services.yaml`）里的服务与 `oryn orb service start --command` 起的临时服务；有 portal 的服务端口经 `<scheme>://t-<线程短码>-p<端口>.<portal_domain>[:<端口>]/` 从浏览器访问。数据面只新增一种流（`portal`，透明 TCP），控制面不解析被代理的 HTTP 以外的任何东西。服务定义与运行状态的真相在 orbd，控制面只存镜像；公开分享与 portal 会话的真相在控制面。

### 主机名

- **线程短码** = 线程 id（`T-<uuidv7>`）末 12 位十六进制（UUIDv7 随机段），orbd 可离线算出；控制面存 `threads.short_code`（唯一索引）反查。portal 实体是 `(orb, 端口)`，短码只是找 orb 的别名：共用 orb 的多个线程各自的主机名指向同一端口。
- 标签 `t-<code>-p<port>`：`^t-([0-9a-f]{12})-p([1-9][0-9]{0,4})$` 且端口 ≤65535。portal 与主域名（`public_url`）**同协议、同端口**（`public_url` 的端口非默认时原样带上）。主机匹配：去端口、转小写、去尾点后恰为 `<单标签>.<portal_domain>`，不比较端口。允许 `portal_domain` 等于主域名本身（开发：`localhost` + `*.localhost`）。
- 纯函数在 `internal/portalhost`（orbd 与 orynd 共用）：`Code`、`Label`、`ParseLabel`、`Expand`、`NewBase`、`Base.{Enabled,Match,URL,Template,FrameSrc}`。地址模板形如 `https://t-{code}-p{port}.orb.example.com/`；未配置 `portal_domain` 时为空串。

### 流与 ctl 消息

|项|规格位置|语义|
|---|---|---|
|`portal` 流|`LinkStreamPortal{t, port}`、`LinkPortalReply`|**由 orynd 打开**，每个代理连接一条。orbd 先回一行 `{"ok":true}` 或 `{"ok":false,"code":…,"message":…}` + `\n`；`ok=true` 之后流上是与 `127.0.0.1:<port>` 之间的原始字节（被拒绝时再试 `[::1]:<port>`）。拒绝码：`forbidden_port`（egress 端口）、`not_exposed`（端口不属于有 portal 的服务）、`connection_refused`、`timeout`（连本机端口 5s）、`failed`。一个方向结束后对另一端半关闭，剩下的方向 30s 截止后两边都关。控制面读应答行用 `(*link.Stream).ReadLine(4096)`，开流 + 应答行合计 10s|
|hello `features`|`LinkCtlHelloParams.features`|M4 的 orbd 发 `["fs","git","attachments","services","portal"]`。缺 `services` → 服务接口 409 `orbd_outdated`；缺 `portal` → 不发 `portal.configure`，portal 请求 409 `orbd_outdated`|
|`services.state`|`LinkCtlServicesStateParams{services: OrbService[]}`|orbd→orynd 通知，**全量快照**。发送时机：每次定义或状态变化、link 建立后、收到 `portal.configure` 之后（脏标记 + 单发送者，断连时丢弃、重连必补发）。控制面逐项校验清洗（名字正则、≤32 个、`command` ≤4096 字节、`links` ≤20 且 `path` 以 `/` 开头、端口范围；不合规项丢弃），在一个事务里整体替换镜像并撤掉不再声明 portal 的端口的分享，随后经 WS `orb` 消息推送|
|`portal.configure`|`LinkCtlPortalConfigureParams{urlTemplate, primaryThreadId?}` → `LinkCtlOkResult`|orynd→orbd 请求，hello 之后发送（仅对 `features` 含 `portal` 的 orbd，10s，失败只记日志）。`primaryThreadId` = 本 orb 最早创建的线程。orbd 保存在内存；已在运行的服务不因模板变化而重启|

### 服务模型

- `OrbService`：`name`（`^[a-z0-9][a-z0-9-]{0,31}$`）、`source`（`file` / `dynamic`）、`command`、`cwd?`、`port?`、`health?`、`links: OrbPortalLink[]`（非空 = 有 portal）、`threadId?`、`publicUrl?`、`status`、`pid?`、`restarts`、`exitCode?`、`error?`、`startedAt?`。
- `OrbServiceStatus`：`starting`（已拉起、未通过就绪探测）→ `running`；`unhealthy`（60s 未就绪，继续每 5s 探测）；`backoff`（退出后等待重启）；`stopped`；`failed`（定义无效、`port_conflict`、`forbidden_port`，或连续 5 次存活 <60s）。
- `OrbPortalLink{title ≤80, path 以 / 开头 ≤512, folder? ≤80}`：Amp 语义的「链接」（`portal: true|<路径>|{url,title}` 与 `portals[{url,title,folder?}]`），不是额外端口；一个服务只暴露它自己的 `PORT`。
- `OrbPortal{port, service, links, public?: OrbPortalPublic{expiresAt}}`；`Orb.services` / `Orb.portals` 为可选字段（orb 已删除时省略），`Thread.portalUrlTemplate`（短码已代入、保留 `{port}`；未配置 `portal_domain` 时省略）。服务 / portal / 分享的变化都经现有 WS `orb` 消息推送，不新增 WS 消息类型。
- 服务进程环境：`PORT`、`ORYN_THREAD_ID`（所属线程）、`PUBLIC_URL`（仅有 portal 且模板非空时；值 = 模板展开后去掉结尾 `/`）；`env` 中 `${services.<name>.publicURL}` 启动前替换。

### REST

鉴权同 C3（变更类在 Cookie 鉴权下要 `X-Oryn-CSRF: 1`）。服务操作经 `fs` 流转发到 orbd 本机 API（同 M3 的代理规则；特性名 `services`，截止 90s）。

|方法与路径|operationId|参数 / 请求体|响应|
|---|---|---|---|
|`GET /api/v1/orbs/{id}/services`|`listOrbServices`|—|`OrbServiceList`（读库镜像，不唤醒、不计交互）|
|`POST /api/v1/orbs/{id}/services/{name}/start`|`startOrbService`|—|`OrbService`|
|`POST /api/v1/orbs/{id}/services/{name}/stop`|`stopOrbService`|—|`OrbService`|
|`POST /api/v1/orbs/{id}/services/{name}/restart`|`restartOrbService`|—|`OrbService`|
|`GET /api/v1/orbs/{id}/services/{name}/log`|`getOrbServiceLog`|`tail?`（字节，默认 65536，≤1048576）、`bg?`|`text/plain; charset=utf-8`|
|`PUT /api/v1/orbs/{id}/portals/{port}/share`|`setOrbPortalShare`|`PortalShareRequest{duration: 1h\|3h\|1d\|7d}`|`OrbPortal`（首次公开与续期同一接口：`expiresAt = now + duration`）|
|`DELETE /api/v1/orbs/{id}/portals/{port}/share`|`endOrbPortalShare`|—|`204`|

路径参数：`ServiceNamePath`（`^[a-z0-9][a-z0-9-]{0,31}$`）、`PortalPortPath`（1–65535）。未配置 `portal_domain` 时两个分享接口回 409 `portal_disabled`。

### orbd 本机 API（`services` 特性，`fs` 流上）

|路由|行为|
|---|---|
|`POST /services/{name}/start`|启动已声明的服务并等待至多 60s 离开 `starting` → `OrbService`|
|`POST /services/{name}/stop`|停止（进程组 SIGTERM，10s 后 SIGKILL），之后不再自动拉起 → `OrbService`|
|`POST /services/{name}/restart`|重读 services.yaml 后重启，等待至多 60s → `OrbService`|
|`GET /services/{name}/log?tail=`|日志文件 `$HOME/.cache/oryn/logs/services/<name>.log` 末尾 `tail` 字节（从整行开始），`text/plain; charset=utf-8`；无日志返回空体|

orb 内对应 `orbd.sock` 方法 `service.ensure` / `service.start` / `service.stop` / `service.restart` / `service.list` 与 CLI `oryn orb service start|stop|restart|ls|logs`（`start --portal` 在 stdout 打印 `PUBLIC_URL` + `/`）。

### 数据面（portal 主机上的请求）

portal 主机的请求在路由最前面被分流，**永远到不了** `/api/v1`、link 与 SPA。**导航请求** = `GET` 且（`Sec-Fetch-Mode: navigate`，或没有 `Sec-Fetch-Mode` 而 `Accept` 含 `text/html`）；导航请求的错误收到同状态码的极简 HTML 页，其余收到 `{code, message}`。

1. `Host` 不匹配 / 标签非法 → 404 `portal_not_found`。
2. 短码无对应线程或线程无 orb → 404；orb 为 `deleting` / `deleted` → 410 `orb_deleted`。
3. `/__oryn/` 前缀是保留端点（见握手），其余保留路径 404；应用永远收不到这个前缀。
4. 身份（先所有者后公开）：`Authorization: Bearer odt_…`（有效设备令牌）→ 所有者，无效 `odt_` → 401 `portal_unauthorized`；其它形态的 `Authorization` 属于应用，原样透传、不参与判定。Cookie `__Host-oryn_portal` 有效、标签与本主机一致且通过跨站检查 → 所有者。否则端口处于公开期（`expires_at > now`，每个匿名请求都按主键重读）→ 匿名访客。都不是：被跨站检查拦下 → 403 `cross_site_blocked`；导航 → `302 <主域名>/portal/auth?t=<code>&p=<port>&next=<RequestURI>`；其余 → 401 `portal_unauthorized`。均带 `Cache-Control: no-store`。
5. 已授权（所有者或匿名访客）：先记一次用户交互（同一 orb 每秒至多写库一次），再判可用性。旧 orbd → 409 `orbd_outdated`。orb 不可用时：镜像里该端口没有 portal → 404 且**不唤醒**；orb `failed` → 503 `orb_unavailable`；导航请求 → 触发唤醒后立即回 503 过渡页（`Retry-After: 2` + `<meta http-equiv="refresh" content="2">`）；其余请求 → 唤醒并至多等 25s，超时 503 `orb_unavailable`（`Retry-After: 3`）。orb 可用时不查镜像，直接代理（由 orbd 裁决端口）。
6. 并发名额：每台 orb ≤256 条 portal 流；每个公开端口匿名并发 ≤64；超出 503 `portal_busy`。

计交互并可唤醒：通过第 4 步的每个请求（所有者与公开访客都算）。不计、不唤醒：未授权请求（302 / 401 / 403）、握手端点、未声明端口。因此开着 portal 页面（HMR 重连、轮询）会让 orb 保持唤醒。

### 鉴权握手

|步|主机|端点|行为|
|---|---|---|---|
|A|portal|任意路径，未授权的导航请求|`302 <Origin>/portal/auth?t=<code>&p=<port>&next=<path?query>`|
|B|主域名|`GET /portal/auth`|`t`（12 位十六进制）或 `p`（1–65535）非法 → 400。网页会话（非恢复会话）已登录：短码不存在 → 404；否则签发一次性码 `opc_…`（内存，绑定标签与网页会话哈希，60s，单次）→ `302 <portal 根地址>__oryn/auth?code=<码>&next=<next>`（回跳主机由配置按 `(短码, 端口)` 重算，不接受调用方给的主机名）。未登录 → `302 /login?next=<本请求 path?query 的 URL 编码>`（登录页对 `/portal/auth?` 开头的 `next` 做整页跳转）。设备令牌主体 → 400|
|C|portal|`GET /__oryn/auth?code=&next=`|码存在、未过期、标签一致 → 写 `portal_sessions`（`expires_at = min(now+30d, 网页会话到期)`，不滑动）→ `Set-Cookie: __Host-oryn_portal=ops_…; Path=/; Max-Age=<秒>; Secure; HttpOnly; SameSite=None; Partitioned` → `302 /__oryn/auth/check?next=<next>`。码无效 → 回到 A|
|D|portal|`GET /__oryn/auth/check?next=`|Cookie 有效 → `302 <next>`；Cookie 没带回来（被浏览器拦截）→ 200 说明页 + `target="_blank"` 的本 portal 链接（握手不会循环）|
|E|portal|`GET /__oryn/login?next=`|已是所有者 → `302 <next>`；否则等同 A。主站 iframe 与「新窗口」从这里进入，公开中的 portal 也先认出所有者|

- `next`（B–E 共用）：以单个 `/` 开头、第二个字符不是 `/` 或 `\`、不含控制字符、≤2048 字节，否则取 `/`。没有任何参数能把人带到 portal 域之外。
- B–E 的响应带 `Cache-Control: no-store` 与 `Referrer-Policy: no-referrer`。
- 不需要 `state`：只有一个所有者，攻击者拿不到可兑换的码；B 是无副作用的 GET。
- portal Cookie 用 `SameSite=None; Partitioned` 是因为开发环境 `localhost` 与 `*.localhost` 互为跨站，`Lax` 的 Cookie 在内嵌 iframe 里设不上；CSRF 由下面的跨站策略负责。
- 吊销：网页会话登出 / 过期 / 恢复、设备吊销 → 取消该主体在 portal 上的全部连接（含已升级的 WebSocket）；`portal_sessions` 随 `web_sessions` 行级联删除，新请求回到握手。

### 跨站请求策略（只约束「凭 Cookie 成为所有者」这一步）

```text
Sec-Fetch-Site 存在：
  same-origin / none                         → 放行
  same-site / cross-site                     → 仅当 GET/HEAD 且 Sec-Fetch-Mode=navigate 且 Sec-Fetch-Dest ∈ {document, iframe} 放行
Sec-Fetch-Site 不存在：
  有 Origin 且 ≠ 本 portal 的 origin          → 拒绝
  其它（非浏览器客户端、老浏览器的同源请求）   → 放行
```

别的站点发来的带 Cookie 的 POST / fetch / WebSocket 拿不到所有者身份（公开端口上降为匿名，否则 403 `cross_site_blocked`）；顶层打开链接与被主站 iframe 内嵌的导航照常。设备令牌不受此约束。

### 反向代理与头

`httputil.ReverseProxy{FlushInterval: -1}`，每个请求一条 `portal` 流（禁用 keep-alive 与压缩，等待响应头 10 分钟，响应头 ≤1MiB）；WebSocket / SSE / 分块由标准库透传，请求 ctx 取消即断开两端。

|方向|处理|
|---|---|
|发给应用|`Host` 保持公开主机名；`X-Forwarded-For` 照常追加，`X-Forwarded-Proto` = portal 协议、`X-Forwarded-Host` = 公开主机名；删除全部入站 `X-Oryn-*` 后设 `X-Oryn-Authenticated: owner=yes`（所有者）或 `owner=no`（匿名）；`Cookie` 里去掉 `__Host-oryn_portal` 那一段（其余原样，剩空则删头）；`Authorization: Bearer odt_…` 删除，其它 `Authorization` 不动|
|回给浏览器|删除 `X-Frame-Options`；去掉各条 `Content-Security-Policy` 中的 `frame-ancestors` 指令，再追加 `Content-Security-Policy: frame-ancestors 'self' <主域名 origin>`；丢弃名为 `__Host-oryn_portal` 的 `Set-Cookie`；匿名请求的响应加 `X-Robots-Tag: noindex, nofollow`；其余头、状态码、响应体不动|

拨号错误映射：`not_exposed` → 404 `portal_not_found`；`forbidden_port` → 403；`connection_refused` → 502 `service_unreachable`；`timeout` → 504 `portal_timeout`；orb 不可用 → 503 `orb_unavailable`；其它 → 502 `portal_unreachable`（导航请求的 502 页带 3s 自动刷新）。

主站 SPA 的 CSP 在启用 portal 时把 `frame-src 'none'` 换成 `frame-src <scheme>://*.<portal_domain>[:端口]`（`Base.FrameSrc()`）；`frame-ancestors 'none'` 与 `X-Frame-Options: DENY` 不变。

### 公开分享

- 键是 `(orb, 端口)`：同一 orb 的其它端口、线程页面与 `/api/v1` 不受影响。端口未声明 portal → 404 `portal_not_found`。时长 `1h` / `3h` / `1d` / `7d` = 3600 / 10800 / 86400 / 604800 秒；续期只改 `expires_at`。
- 终止或到期：删除分享行，立即取消该端口全部匿名连接（含已升级的 WebSocket），经 `orb` 消息推送。每个匿名请求都比较 `expires_at`，定时器迟到也不会多放行。控制面启动时为未过期分享重建定时器、删除已过期的行；每小时清理过期分享与 portal 会话。
- orbd 的 `services.state` 不再声明某端口的 portal 时，该端口的分享随镜像替换一并删除；orb 删除时镜像与分享清空。

### 限额

|项|值|
|---|---|
|每台 orb 的服务数|≤32；服务名 `^[a-z0-9][a-z0-9-]{0,31}$`|
|自动分配端口|20000–29999，按服务名持久化在 `$HOME/.oryn/services/state.json`（跨停机 / 唤醒不变）|
|orbd 自身端口|egress 端口（默认 7811）永不暴露|
|就绪探测|间隔 500ms、单次 2s；60s 未就绪标 `unhealthy`，之后每 5s 一次直到成功；通过后不再持续探活|
|重启退避|1s、2s、4s、8s、16s、30s（封顶）；单次存活 ≥60s 清零；连续 5 次存活 <60s → `failed`|
|停止|进程组 SIGTERM，10s 后 SIGKILL|
|服务日志|10MiB 轮转为 `.log.1`（只留一份）|
|portal 流拨号|开流 + 应答行 10s；orbd 连本机端口 5s|
|等待响应头|10 分钟（之后不限时，由客户端与反代的 3600s 兜底）|
|并发|每台 orb ≤256 条 portal 流；每个公开端口匿名并发 ≤64|
|唤醒等待|非导航请求 25s；导航请求立即回过渡页（每 2s 自动刷新）|
|一次性码 / portal 会话|60s 单次 / 30 天（不滑动，且不长于其网页会话）|

### 错误码

|code|HTTP|来源|
|---|---|---|
|`portal_disabled`（未配置 `portal_domain` 时的分享接口）|409|控制面|
|`portal_not_found`（短码不存在、线程无 orb、端口未声明 portal）|404|控制面 / orbd `not_exposed`|
|`portal_unauthorized`|401|控制面（非导航请求）|
|`cross_site_blocked`|403|控制面|
|`forbidden_port`|403|orbd|
|`orbd_outdated`|409|控制面|
|`orb_deleted`|410|控制面|
|`service_unreachable`（端口无人监听）|502|orbd `connection_refused`|
|`portal_unreachable` / `portal_timeout`|502 / 504|控制面|
|`orb_unavailable`（带 `Retry-After: 3`）/ `portal_busy`|503|控制面|
|`service_not_found` / `invalid_service` / `name_conflict` / `services_not_ready`|404 / 400 / 409 / 409|orbd 本机 API 与 orbd.sock|

### 存储

`0010_services_portals.sql`：

- `threads.short_code`（唯一索引 `threads_short_code`；迁移时为既有 `T-<uuid>` 线程回填末 12 位）。
- `orb_services(orb_id → orbs ON DELETE CASCADE, name, port, has_portal, doc, updated_at)`，主键 `(orb_id, name)`，索引 `(orb_id, port)`；`doc` 为 `OrbService` 的 JSON；`has_portal = len(links)>0 && port != nil`。
- `portal_shares(orb_id → orbs ON DELETE CASCADE, port, created_at, expires_at)`，主键 `(orb_id, port)`。
- `portal_sessions(id_hash, web_session_hash → web_sessions ON DELETE CASCADE, label, created_at, expires_at)`，索引 `web_session_hash`；`id_hash` = SHA-256(Cookie 值 `ops_…`)。

查询见 `internal/store/queries/portals.sql` 与 `threads.sql` 的 `GetThreadByShortCode`。`GetPortalSession` 只返回自身与所属网页会话都未过期、且网页会话 `kind='full'` 的行。注意：sqlc 的 SQLite 引擎在查询文件含非 ASCII 字符时会错位改写参数，`internal/store/queries/*.sql` 的注释一律用英文。

## M5：公开分享、通知与 sync

公开面是一条独立、默认拒绝的出口：匿名 REST 全在 `/api/v1/public/` 前缀下（`security: [{}]`，tag `public`），匿名 WebSocket 在 hub 里走单独的连接表，只有显式写给它的消息才到得了；私有通道的 `index` / `orb` / `ui.*` / 终端 / 应答 / 通知消息在结构上够不着它。公开范围 = **分享范围内活动分支上的主会话条目**（实时：每次请求按当前叶子现算；快照：创建时扫描过的条目集合，按分支顺序连同每行原始 payload 的 SHA-256 固化在 `thread_share_entries`，之后被改写或删除的行不再公开——不回落到别的分支、不出现新内容，标题也冻结在创建时）；子 agent 转录与进度、被回退的分支、`display:false` 的注入消息与非渲染类型条目的内容一律不出控制面。密钥扫描（另含标题与将要列出的附件名）、`.md` 导出、图片与附件授权（以公开过滤之后的内容为准）、公开条目用同一个范围函数，所以「扫过的」与「公开的」恒等；实时帧走同一条隐藏规则。通知由线程状态转换（`done` / `error`）与新的待答请求（`input`）驱动，去抖后落库并经库驱动的投递队列发出。`oryn sync` 用「工作区快照提交 + git bundle」一条通道带走提交与未提交改动。

### 共享纯函数

|包|导出|用途|
|---|---|---|
|`internal/transcript`|`Entry`、`IsMainFile`、`ActiveBranch`、`Message` / `ToolCall` / `ParseMessage`、`LineText`、`Public`、`MarkdownMeta` / `Markdown`、`Snippet`|活动分支（主会话文件按首次出现的 seq 拼接；含叶子的文件从叶子沿 `parentId` 回溯，其余从最后一条回溯；合成 id 与子 agent 文件除外）、公开视图、`.md` 导出、通知正文。CLI、分享、通知共用|
|`internal/uiprompt`|`Prompt`、`Labels`、`Describe`、`AnswerOption`、`AnswerText`、`ErrUnsupported`、`ErrNeedOption`|待答请求的纯文本呈现与文本作答（Telegram、`oryn thread attach`）|

`transcript.Public(e)`：子 agent 文件 → 不公开；类型不在 `{message, compaction, branch_summary, custom_message, model_change, thinking_level_change, mode_change, ttsr_injection, title_change}`，或 `role ∈ {custom, hookMessage}` 且 `display:false` 的消息，或 `display:false` 的 `custom_message` → payload 换成 `{"type":"<type>"}`（**保留链、清空内容**：活动分支沿 `parentId` 回溯，丢掉条目会断链）；其余原样。

`uiprompt` 支持的请求与应答帧（都带 `type:"extension_ui_response", id`）：

|`method`|呈现|应答|
|---|---|---|
|`select`|`title` + `options`|`value: <选项原文>`|
|`confirm`|`title` / `message` + `[Yes, No]`（按渠道语言）；文字另认 `y yes 是 好 确认` / `n no 否 不 取消`|`confirmed`|
|`input` / `editor`|自由文本；`input` 带 `secret` / `authCodeFor` / `loginPromptFor` → 不支持|`value`|
|`form`|全部分节恰好一个字段：单选 choice（问题 = 字段 label，空则取其前最近的 `text` 块；`other` → 接受自由文本）或 text 字段；多字段 / 多选 → 不支持|`formData: {<name>: <选项 value 或文本>}`|
|`ask`|恰好一个非多选问题；总接受自由文本|`answers: [{id, selectedOptions: [label]}]` 或 `[{id, selectedOptions: [], customInput}]`|
|`askDialog`|同 `ask`|`askDialog: {kind:"submit", results:[{id, question, options, multi:false, selectedOptions, customInput?}]}`|
|其它 / `plan_proposal`|不支持（引导到 Web）|—|

文字作答：全为数字且在 1..选项数内 = 该选项；与某选项文字相等（忽略首尾空白与大小写）= 该选项；否则接受自由文本时按自由文本，不接受时 `ErrNeedOption`。

### REST（已鉴权）

鉴权同 C3（变更类在 Cookie 鉴权下要 `X-Oryn-CSRF: 1`）。

|方法与路径|operationId|参数 / 请求体|响应|
|---|---|---|---|
|`GET /api/v1/threads/{id}/shares`|`listThreadShares`|—|`ThreadShareList{shares}`|
|`POST /api/v1/threads/{id}/shares`|`createThreadShare`|`CreateThreadShareRequest{mode, acknowledgeFindings?}`|`201 ThreadShare`；扫描有命中且未确认 → `409 ShareScanConflict{code:"secrets_found", message, scan}`|
|`GET /api/v1/shares`|`listShares`|—|`ShareOverview{shares, files}`|
|`DELETE /api/v1/shares/{id}`|`revokeShare`|—|`204`|
|`POST /api/v1/attachments/{id}/publish`|`publishAttachment`|—|`PublishedFile`（幂等：已发布则返回现有）|
|`POST /api/v1/orbs/{id}/fs/publish`|`publishOrbFile`|`PublishOrbFileRequest{path, threadId}`|`201 PublishedFile`（发布的是此刻的副本，落成该线程的附件）|
|`DELETE /api/v1/published-files/{id}`|`revokePublishedFile`|—|`204`|
|`GET /api/v1/notifications`|`listNotifications`|`limit?`（默认 50，≤100）、`before?`（ms）|`NotificationList{notifications, unread}`|
|`POST /api/v1/notifications/read`|`markNotificationsRead`|`NotificationsReadRequest{threadId?}`（省略 = 全部）|`204`；随后广播 `notifications.read`|
|`GET /api/v1/notification-channels`|`listNotificationChannels`|—|`NotificationChannelList{channels}`|
|`POST /api/v1/notification-channels`|`createNotificationChannel`|`NotificationChannelInput`|`201 NotificationChannel`|
|`PATCH /api/v1/notification-channels/{id}`|`updateNotificationChannel`|`NotificationChannelPatch`|`NotificationChannel`|
|`DELETE /api/v1/notification-channels/{id}`|`deleteNotificationChannel`|—|`204`|
|`POST /api/v1/notification-channels/{id}/test`|`testNotificationChannel`|—|`NotificationTestResult{ok, error?}`|
|`POST /api/v1/notification-channels/{id}/telegram/link`|`linkTelegramChannel`|—|`TelegramLink{code, botUsername, expiresAt}`|
|`GET /api/v1/push/vapid-key`|`getVapidKey`|—|`VapidKey{publicKey}`（base64url 的 65 字节未压缩点）|
|`GET /api/v1/push/subscriptions`|`listPushSubscriptions`|—|`PushSubscriptionList{subscriptions}`|
|`POST /api/v1/push/subscriptions`|`createPushSubscription`|`PushSubscriptionInput{endpoint, keys:{p256dh, auth}}`（`userAgent` 取请求头）|`201 PushSubscription`（按 endpoint upsert）|
|`DELETE /api/v1/push/subscriptions/{id}`|`deletePushSubscription`|—|`204`|
|`POST /api/v1/orbs/{id}/sync/prepare`|`prepareOrbSync`|`bg?`；`OrbSyncPrepareRequest{root?, have?≤256, force?}`|`OrbSyncState{root, branch?, head, snapshot, dirty, common, bundle?{id, size}}`|
|`GET /api/v1/orbs/{id}/sync/bundle`|`getOrbSyncBundle`|`id`（`^[0-9a-f]{16}$`）|`application/octet-stream`|

路径参数：`ShareIdPath`（`^shr_[0-9A-Z]{26}$`）、`ShareSlugPath`（`^[0-9a-f]{32}$`）、`PublishedFileIdPath`（`^pub_…`）、`NotificationChannelIdPath`（`^nch_…`）、`PushSubscriptionIdPath`（`^psub_…`）。

模型要点：

- `ThreadShare{id, threadId, threadTitle, mode, slug, url, createdAt, findings, untilSeq?}`：`url = <public_url>/s/<slug>`；`untilSeq` 仅快照（创建时的 `threads.last_seq`）；`findings` = 创建时确认过的命中数。`Thread.publicShares`（未撤销分享数，0 时省略）与 `Attachment.publicUrl`（已发布时）为新增的可选字段。
- `ShareFinding{rule, seq, entryId, preview, count, secretName?, role?, tool?}`：`preview` 为脱敏片段（前 4 字符 + `…` + 后 2 字符，命中短于 12 字符只留前 2 字符）；标题与附件名的命中不对应条目，`entryId` 分别为 `__title__` 与 `attachment:<附件 id>`、`seq` 为 0；`ShareScan{findings ≤200, scanned, truncated}`。
- `PublishedFile{id, attachmentId, threadId?, name, mime, size, url, createdAt}`：`url = <public_url>/f/<slug>/<encodeURIComponent(name)>`。
- `Notification{id, kind, threadId?, title, body, url, createdAt, readAt?, resolvedAt?, deliveries[]}`：`url` 为站内路径 `/t/<threadId>`；`NotificationDelivery{target, channelId?, channelName?, status, attempts, at, error?}`，`at` 在 `pending` 时为下次尝试时间、其余为完成时间。
- `NotificationChannelInput{kind, name, enabled?, events?, locale?}` + 与 `kind` 同名的**恰好一个**配置对象：`telegram{botToken, apiBase?}`、`webhook{url, secret?, headers?≤16}`、`ntfy{server?, topic, token?}`、`bark{server?, deviceKey}`。`NotificationChannelPatch` 给出配置对象 = 整体替换（Telegram 换 token 会清掉绑定）。`NotificationChannel` 只回脱敏的 `summary`、`telegram?{botUsername, linked, chatTitle?}`、`lastError?`、`lastDelivery?{status, at, error?}`，**机密字段只写不读**。
- 枚举：`ShareMode`（`live | snapshot`）、`ShareFindingRule`、`NotificationKind`（`done | input | error`）、`NotificationChannelKind`、`NotificationLocale`（`zh-CN | en`）、`NotificationDeliveryTarget`（`telegram | webhook | ntfy | bark | webpush`，`apns | fcm` 预留）、`NotificationDeliveryStatus`、`PushSubscriptionKind`（`webpush`，`apns | fcm` 预留）。为避免 oapi-codegen 在常量名冲突时给既有常量加前缀，`ShareMode` 与匿名 WS 消息的 `type` 枚举用 `x-enum-varnames` 显式命名（`ShareModeSnapshot` 与 `JobOp` 的 `Snapshot` 共存）。

### 匿名面

全部响应带 `X-Robots-Tag: noindex, nofollow` 与 `Cache-Control: no-store`；slug 不存在或已撤销一律 `404 {code:"not_found"}`。`PublicShare` 与条目里**不含**线程 id、orb、项目、标签、模型配置。

|方法与路径|operationId|参数|响应|
|---|---|---|---|
|`GET /api/v1/public/shares/{slug}`|`getPublicShare`|—|`PublicShare{mode, title, state?, sharedAt, threadCreatedAt, lastSeq, leafEntryId?, transcriptRev, attachments: PublicAttachment[]}`（`state` 仅实时；`title` 快照为创建时的标题；`attachments` 只列已发布的附件与被范围内公开条目引用（按附件 id）的附件，快照只考虑创建时已存在的）|
|`GET /api/v1/public/shares/{slug}/entries`|`listPublicShareEntries`|`after?`、`limit?`（默认 200，≤500）|`PublicEntryPage{entries, hasMore, lastSeq, leafEntryId?}`（`lastSeq` = 范围上界）|
|`GET /api/v1/public/shares/{slug}/blobs/{sha256}`|`getPublicShareBlob`|—|字节；只放行被范围内公开条目（过滤之后的内容；快照只认未被改写的固化行）引用的 sha。类型白名单同 `/f`：`image/png\|jpeg\|gif\|webp` 内联，其余 `application/octet-stream` + `attachment`；`nosniff`、`sandbox`、`no-store`（私有 `getBlob` 同一白名单，可长期缓存）|
|`GET /api/v1/public/shares/{slug}/ws`|`connectPublicShareWs`|`after?`、`rev?`|`101`（仅 `mode=live`；快照 → 404）|
|`GET /s/<slug>`|—（SPA）|—|存在 → `index.html`（200，带 noindex 头）；否则 404 极简 HTML（含 `<meta name="robots" content="noindex">`）。只接受 GET / HEAD|
|`GET /s/<slug>.md`|—|—|`text/markdown; charset=utf-8`，`Content-Disposition: inline; filename*=UTF-8''<标题>.md`；内容 = `transcript.Markdown`|
|`GET /f/<slug>/<name>`|—|`Range`|发布文件；`name` 须与附件名一致。`nosniff`、`Content-Security-Policy: sandbox`、noindex、`no-store`；`image/png\|jpeg\|gif\|webp` 内联，`text/*`（非 `text/html`）以 `text/plain; charset=utf-8` 内联，其余附件下载|

匿名 WebSocket（`WsPublicServerMessage`，按 `type` 判别，**都不带 `threadId`**）：

|`type`|模型|语义|
|---|---|---|
|`share.snapshot`|`WsPublicSnapshot{title, state, leafEntryId?, lastSeq, transcriptRev, fullReplay, live, subagents}`|连接后首条；`live` 与 `frame` 同一过滤；`subagents` 恒为空；`fullReplay` 判定与私有订阅相同|
|`entries`|`WsPublicEntries{entries ≤200, leafEntryId?, lastSeq}`|补发与实时条目（实时条目经 `transcript.Public` 过滤）；补发各批 `lastSeq` 单调，最后一批 = 快照的 `lastSeq`|
|`frame`|`WsPublicFrame{frame}`|仅 `message_start / message_update / message_end / tool_execution_start / tool_execution_update / tool_stream_update / tool_execution_end / agent_start / agent_end`，且按条目同一隐藏规则过滤：`display:false` 的注入消息不发；`message_update` 只发 assistant 的（delta 投影不带 `display`）；`agent_end` 只留 `{type, isTerminal?}`；`task` 工具的 `tool_execution_update` / `tool_stream_update` 不发；`subagent_*` 一律不发|
|`live.reset` / `transcript.reset`|`WsPublicLiveReset` / `WsPublicTranscriptReset{transcriptRev}`|同私有通道；另外所有者把叶子移到客户端未持有的分支上（hub 为每条匿名连接跟踪其分支末端）时也发 `transcript.reset`。客户端收到后以 `after=0&rev=<transcriptRev>` 重连。`switch_leaf` / `navigate_tree` 成功把叶子移离受理时的位置时，控制面与会话文件重写同一条路径递增 `threads.transcript_rev` 并广播 `transcript.reset`，断线期间错过切换的客户端带旧 `rev` 重连即整段重拉|
|`share.state`|`WsPublicState{state, title}`|线程状态或标题变化时|
|`share.revoked`|`WsPublicRevoked`|撤销或线程删除；随后以 4001 关闭|
|`pong`|`WsServerPong`|客户端只许发 `WsClientPing`，其它消息以 1008 关闭|

匿名连接不算 focus、不记用户交互、不唤醒 orb；沿用 8MiB 待发预算（超出断开，客户端重连补拉）。

私有 WS 新增：`notification`（`WsServerNotification{notification}`）与 `notifications.read`（`WsServerNotificationsRead{threadId?}`，省略 = 全部已读），发给全部私有连接。

### 通知与投递

- 触发：`done`（`idle` 且上一状态 ∈ `working / awaiting_input`）与 `error`（转入 `error`）来自线程状态转换；`input` 来自新进入实时缓冲的待答请求（计划提案的 `request_id = "plan"`）。触发时有设备 focus 于该线程则不通知；去抖期间线程回到 `working`、被 focus / 操作、请求被作答都会取消。`(thread_id, request_id)` 的未决通知唯一（控制面重启后 orbd 重放待答请求时去重）。
- 投递目标：启用且 `events` 含该 kind 的渠道，以及全部可投递的推送订阅（所属设备未吊销）；每个目标一行 `pending` 投递。网络错误、408、429、5xx 可重试，其余 4xx 不重试；Web Push 404 / 410 删除订阅。
- Webhook：`POST <url>`，头 `Content-Type: application/json`、`User-Agent: Oryn/<version>`、`X-Oryn-Event: thread.<kind>`（测试为 `test`）、配置的自定义头；有 `secret` 时 `X-Oryn-Signature: sha256=<hex(HMAC-SHA256(secret, body))>`。体 `{"event","id","ts","thread":{"id","title","state","url"},"title","body"}`。
- ntfy：`POST <server>` JSON 发布 `{"topic","title","message","click","tags","priority"}`（`input` priority 4，其余 3）；有 `token` 时 `Authorization: Bearer`。Bark：`POST <server>/push` `{"device_key","title","body","url","group":"Oryn","level"}`，HTTP 200 且 `code == 200` 才算成功。
- Web Push：RFC 8291 `aes128gcm` 单记录 + VAPID（ES256 JWT，`aud` = 端点 origin，`exp` = now+12h）；载荷 `{"v":1,"id","kind","title","body","url":"/t/<threadId>","tag":"oryn-<threadId 末 12 位>"}`，`TTL: 86400`，`Urgency`：`input` 为 `high`、其余 `normal`。订阅端点须是 `https://`（`http://` 仅回环主机），`p256dh` 65 字节且以 0x04 开头、`auth` 16 字节，否则 400 `push_invalid`。
- Telegram：`getUpdates` 长轮询（不需要公网入站）；至多一个渠道、只接受绑定的那一个私聊（`/start <码>` 绑定，码 8 位、10 分钟）。`input` 且有选项时带 inline 按钮（`callback_data = "o:<deliveryID>:<序号>"`）；点按钮或回复通知作答待答请求，回复其它通知即发给该线程：线程 `working` 时发 `steer`，否则发 `prompt`（开启新回合，与 Web 输入区一致）；请求在别处被作答后撤回按钮。

### orbd 本机 API（`sync` 特性，`fs` 流上）

|路由|行为|
|---|---|
|`POST /sync/prepare`|体 `OrbSyncPrepareRequest`。用临时 index（`<git-dir>/oryn-sync.index`，不碰真实 index、不跑钩子）`git add -A` + `write-tree`；树与 HEAD 相同 → `snapshot = head`、`dirty=false`，否则 `commit-tree <tree> -p <head>`（作者 / 提交者固定为 `Oryn Sync <sync@oryn.invalid>`、时间 `@0 +0000`：同树同父 → 同 sha）；更新 `refs/oryn/sync/{snapshot,head}`；`common` = `have` 中本仓库已有的提交；`snapshot ∉ have` 时 `git bundle create $HOME/.oryn/tmp/sync-<16 hex>.bundle refs/oryn/sync/snapshot refs/oryn/sync/head --not <common…>`|
|`GET /sync/bundle?id=`|`http.ServeContent`；文件不存在 → 404 `sync_bundle_gone`|

hello `features` 追加 `sync`；旧版 orbd → 409 `orbd_outdated`。控制面代理：`prepare` 截止 6 分钟（`bg=1` 时 orb 不可用直接 409 `orb_not_ready`，不唤醒、不计交互），`bundle` 以原始字节转发。

### 限额

|项|值|
|---|---|
|slug|16 字节 `crypto/rand` → 32 位小写十六进制（明文存储）|
|每线程未撤销分享|≤20（超出 409 `invalid`）|
|匿名 WS|每分享 ≤64、全局 ≤512；读上限 4KiB；只接受 `ping`|
|扫描命中|≤200 项（`truncated`）|
|`.md` 导出|单个工具结果截到 4000 字符|
|发布 orb 文件|单文件 ≤100MiB|
|通知去抖|`done` / `error` 3s，`input` 2s|
|投递|单次 HTTP 超时 15s；重试间隔 10s、60s、5min（共 4 次尝试）；创建超过 1h 仍未送达 → `failed: expired`|
|通知保留|30 天（每小时清理）|
|Web Push 载荷|JSON ≤3000 字节（`body` 截到 300 字符）|
|Telegram|长轮询 `timeout=50`（HTTP 超时 70s）；按钮 ≤8 个、每个 ≤40 字符；正文 ≤3500 字符|
|sync|`have` ≤256；bundle ≤1GiB；orbd 侧 git 超时 5 分钟；bundle 文件保留 10 分钟|

### 错误码

|code|HTTP|来源|
|---|---|---|
|`not_found`（slug 不存在 / 已撤销）|404|匿名面|
|`invalid`（`mode` 非法 / 未绑定线程的附件 / 分享数已满）|400 / 409|控制面|
|`secrets_found`（体为 `ShareScanConflict`）|409|`createThreadShare`|
|`share_busy`（匿名连接名额已满）|503|匿名 WS|
|`channel_exists`（已有 Telegram 渠道）|409|控制面|
|`invalid_channel`（配置非法、Telegram `getMe` 失败、非 Telegram 渠道上的绑定）|400|控制面|
|`channel_not_linked`（Telegram 未绑定时测试）|409|控制面|
|`push_invalid`|400|`createPushSubscription`|
|`is_a_directory` / `too_large`|409 / 413|`publishOrbFile`|
|`sync_no_commits` / `sync_no_common` / `sync_shallow`|409|orbd `/sync/prepare`|
|`too_large`（bundle 超过 1GiB）|413|orbd `/sync/prepare`|
|`sync_bundle_gone`|404|orbd `/sync/bundle`|
|`orbd_outdated`（orbd 无 `sync` 特性）|409|控制面|

### 存储

`0011_share_notify.sql`：

- `thread_shares(id, thread_id → threads ON DELETE CASCADE, slug UNIQUE, mode ∈ {live, snapshot}, until_seq?, leaf_entry_id?, findings, created_at, revoked_at?)`，索引 `(thread_id, revoked_at)`。
- `published_files(id, attachment_id → attachments ON DELETE CASCADE, slug UNIQUE, created_at, revoked_at?)`，部分唯一索引 `(attachment_id) WHERE revoked_at IS NULL`（同一附件至多一条未撤回发布）。

`0012_share_snapshot.sql`：`thread_shares` 增列 `title?`（快照创建时的标题）；`thread_share_entries(share_id → thread_shares ON DELETE CASCADE, pos, seq, payload_sha256, PK(share_id, pos))` 固化快照扫描过的条目集合（`pos` = 分支顺序，`payload_sha256` = 创建时原始 payload 的 SHA-256）。迁移撤销此前创建的快照分享（没有固化集合，无法保证内容不变）。
- `notification_channels(id, kind, name, enabled, events JSON, locale, config_enc, state JSON, last_error?, created_at, updated_at)`：`config_enc = secrets.Box.Seal("notify/" || id, 配置 JSON)`（不进 `secrets` 表）；`state` 为非机密运行状态。
- `push_subscriptions(id, kind, endpoint UNIQUE, p256dh, auth, web_session_hash → web_sessions ON DELETE CASCADE, device_id → devices ON DELETE CASCADE, user_agent, created_at, last_success_at?, last_error?)`。
- `notifications(id, thread_id → threads ON DELETE CASCADE, kind, title, body, request_id?, request?, created_at, read_at?, resolved_at?)`，索引 `created_at`、`(thread_id, read_at)`，部分唯一索引 `(thread_id, request_id) WHERE request_id IS NOT NULL AND resolved_at IS NULL`（`InsertNotification` 用 `ON CONFLICT DO NOTHING`，影响 0 行 = 已通知过）。
- `notification_deliveries(id, notification_id → notifications ON DELETE CASCADE, channel_id → notification_channels ON DELETE CASCADE, subscription_id → push_subscriptions ON DELETE CASCADE, target, status ∈ {pending, sent, failed}, attempts, next_attempt_at?, last_error?, external_ref?, created_at, finished_at?)`，索引 `(status, next_attempt_at)`、`(target, external_ref)`、`notification_id`、`(channel_id, created_at)`；`external_ref` 为 Telegram 的 `"<chat_id>:<message_id>"`。
- VAPID 私钥存 `<data_dir>/vapid.key`（32 字节标量，0600，首次启动生成；与 `master.key` 一起备份）。

查询见 `internal/store/queries/share.sql`（含范围读取 `ListMainEntryLinks`、`ListEntriesBySeqRange`、`ListEntrySeqsContaining`，均以 `instr(file, '/') = 0` 排除子 agent 文件）与 `notify.sql`（含投递队列 `ListDueNotificationDeliveries`、`GetNextNotificationDeliveryAt`）。

## M6：App 会话

桌面（M6）与移动（M7）App 的壳加载所配对服务器自己的 Web UI。Web 客户端全部按同源写（REST、`/api/v1/blobs/…` 图片、下载链接、WS、portal 握手都靠主域名会话 Cookie），所以壳不把设备令牌交给页面，而是用它换一条**设备名下的网页会话**：令牌只留在壳进程与系统凭据库里，控制面不需要任何 CORS 改动。换取方式照搬 portal 握手的「一次性码 + 顶层跳转」，不依赖任何 WebView 的 Cookie API。

### 端点

|方法与路径|operationId|鉴权|行为|
|---|---|---|---|
|`POST /api/v1/auth/app-session`|`createAppSession`|Bearer `odt_…`（设备 `kind ∈ {desktop, mobile}`）|`200 AppSessionCode{code, expiresIn}`：`code = oac_…`；其它主体（网页会话 / App 会话 / `cli` 设备）→ `403 app_session_unsupported`；令牌无效或已吊销 → `401 unauthorized`|
|`GET /auth/app?code=<oac_…>&next=<站内路径>`|—（不在 `/api/v1` 下，经 `Deps.Mounts` 挂载）|码本身|只接受 GET / HEAD（其余 `405` + `Allow: GET, HEAD`）；按 `login` 桶限流（每 IP 突发 10、每 6s 补 1）；响应头 `Cache-Control: no-store`、`Referrer-Policy: no-referrer`；兑换成功写 `__Host-oryn_session`（属性同登录：`Path=/; HttpOnly; Secure; SameSite=Lax`，30 天）；**无论成败都 `302 next`**——失败时 SPA 的 `GET me` 得 401，由壳重新换取|

`next` 只放行站内绝对路径：以 `/` 开头、第二个字符不是 `/` 或 `\`、不含任何 ASCII 控制字符（浏览器会删掉 Tab / CR / LF，`/\t/evil` 会变成 `//evil`）、长度 ≤2048；否则跳到 `/`。

### 码的寿命

- 只在内存中（`auth.NewCodes("oac")`，与 WS 票据同一实现），`TicketTTL` = 30 秒、只能兑换一次；控制面重启即失效。
- 吊销设备时作废该设备全部未兑换的码；兑换在写事务（`BEGIN IMMEDIATE`）内复核设备未吊销且类型仍为 desktop / mobile，与吊销串行，不会留下吊销之后才建成的会话。

### 会话与设备的绑定

- 会话就是 `web_sessions(kind='full')` 加一列 `device_id`（`0013_app_sessions.sql`，部分索引 `WHERE device_id IS NOT NULL`；不声明外键——SQLite 的 `DROP COLUMN` 删不了带外键的列，设备行只吊销不删除）。所以 WS 票据、CSRF、portal 握手（`ws.kind = 'full'`）、Web Push 订阅与普通网页会话完全一样。
- 兑换时在同一事务里删除该设备已有的网页会话（事务后逐个发布 `(web, 哈希)` 吊销事件）再新建一条：**每台设备至多一条 App 会话**，重新换取即顶掉旧的。兑换与滑动续期都更新设备的 `last_seen_at`。
- 鉴权主体：`Principal{Kind: "web", ID: <会话哈希>, DeviceID: "dev_…"}`；`GET me` 返回 `auth.kind = "web"` 且带 `auth.deviceId`；`GET devices` 里该设备 `current = true`。
- 吊销设备（`DELETE /api/v1/devices/{id}`）：标记吊销 → 作废未兑换的码 → 删除其全部网页会话 → 先发布 `(device, id)`，再逐个发布 `(web, 哈希)`；实时通道、portal（portal 会话随网页会话级联删除）与票据表都已订阅吊销事件，连接立即断开。

### 权限边界

- App 会话是 Cookie 会话：变更请求同样要 `X-Oryn-CSRF: 1`。
- App 会话**不能**查看或批准设备配对（`requireWeb` 拒绝 `DeviceID` 非空的主体，`403 forbidden`），保住「设备令牌不能再签发设备」这条不变式；也不能再换 App 会话。
- 重新注册 Passkey 仍只接受初始化令牌或恢复会话，App 会话与完整网页会话一样做不到。

### 错误码

|code|HTTP|来源|
|---|---|---|
|`app_session_unsupported`|403|`createAppSession`（主体不是 desktop / mobile 设备令牌）|
