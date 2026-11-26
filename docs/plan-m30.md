# M30 — Agent 沙箱：实施计划

## Context

主人要求：把「给 orb 内 agent 用的临时隔离沙箱」做成 Oryn 一等能力——可选 docker / kvm 后端，是否联网由 agent 设定，能从 orb 传文件过去、在里面运行命令，并以原生工具提供给 agent。
终态：agent 在 orb 里调用 `sandbox_*` 工具（或等价的 `oryn sandbox …`），得到一台零凭据、带回收期限、用完即焚的平级环境；控制面负责调度、鉴权、中继与回收；主人在线程信息页能看到并销毁它们。

现状为什么不够（均已核对）：orb 内 `oryn thread new` 不能选 provider（`cmd/oryn/thread.go:666-681`），新线程继承项目凭据，归档后 orb 只冻结不删除；M23 box 的 exec 在 orb 内不是流式（两路各截 256 KiB），cp 只支持单文件，orbd 没有目录传输与精简模式。

## 已定决策（主人逐项确认）

- D1 沙箱是一台特殊 orb，内跑新增的「精简模式」orbd；沙箱的 link 默认拒绝一切控制面能力。
- D2 `backend: "kvm"` = 现有 Firecracker 驱动；`backend: "docker"` = docker 驱动（含 sysbox 类型）。不写新驱动。
- D3 只用 provider 的基础镜像 / 根盘。
- D4 `none` = 沙箱里完全没有网卡；控制通道一律经 provider 中继（docker：挂进容器的 unix socket；Firecracker：vsock）。`egress` 模式的 link 也走中继。
- D5 网络运行时双向可切换：创建时为 `egress` 的沙箱可在 `egress` ↔ `none` 间来回切；创建时为 `none` 的没有网卡，不能开网。
- D6 回收：空闲超时（默认 30 分钟，创建时可设 5–240）+ 最长寿命硬上限（默认 12 小时）；另可设绝对 TTL 并续期（到点即销毁，哪怕命令在跑；不能越过最长寿命）。空闲 = 没有命令在跑（含后台作业），也没有传文件或改网络。
- D7 级联：归属线程归档或删除 → 销毁；owner orb 停机 / 删除 / 失败 → 销毁；owner orb 冻结 → 不动。控制面重启后由对账补清理。
- D8 后台命令：`sandbox_exec` 的 `detach`，另有作业列表 / 读输出 / 终止。
- D9 文件传输：双向；文件或目录（流式 tar，经控制面中转不落盘）；两端路径限各自 `$HOME` 内；单次 2 GiB；排除规则 + `.gitignore` 开关。
- D10 每个动作一个原生工具，`loadMode: "discoverable"`；等价 CLI `oryn sandbox …`。
- D11 界面：线程信息页「沙箱」区块 + 工具卡 + REST 列表与销毁。不做沙箱终端、文件浏览、独立总览页。
- D12 docker / Firecracker orb 上的工作线程（含自动化线程、开在 box 上的线程）可用；runner 线程、Space 协调者、全局助手、沙箱自身不可用。`network` 必填；`backend` 省略时优先 kvm；规格用 provider 类型名；每 owner orb 2 台、全局 8 台；provider 在 `provider.yaml` 显式开启。
- 鉴权模型：权限主体是 link 认证的 owner orb；`owner_thread_id` 只做归因与生命周期。原因：orb 内调用方线程取自可改的 `ORYN_THREAD_ID`（`cmd/oryn/orb_thread.go:54-61`），控制面只能校验线程属于该 orb（`internal/boxes/call.go` 的 `checkCaller`），同 orb 的多条线程不是安全边界。

## 冻结契约

以下字面量在各步骤间共享，实现时原样使用。

### C1 数据模型（迁移 `internal/store/migrations/0064_sandboxes.sql`，goose 格式同 `0045_boxes.sql`）

```sql
-- +goose Up
ALTER TABLE orbs ADD COLUMN sandbox_network TEXT CHECK (sandbox_network IN ('none','egress'));
CREATE TABLE sandboxes (
    id              TEXT PRIMARY KEY,                 -- ids.New("sbx")
    orb_id          TEXT NOT NULL UNIQUE REFERENCES orbs(id),
    owner_orb_id    TEXT NOT NULL,                    -- 鉴权主体；不加外键（owner orb 软删除后行仍要可读）
    owner_thread_id TEXT NOT NULL,                    -- 归因；不加外键（线程删除后行保留到裁剪）
    backend         TEXT NOT NULL CHECK (backend IN ('docker','kvm')),
    provider_name   TEXT NOT NULL,
    type            TEXT NOT NULL,
    created_network TEXT NOT NULL CHECK (created_network IN ('none','egress')),
    network         TEXT NOT NULL CHECK (network IN ('none','egress')),
    state           TEXT NOT NULL CHECK (state IN ('creating','ready','destroying','destroyed','failed')),
    idle_sec        INTEGER NOT NULL,
    last_active_at  INTEGER NOT NULL,
    ttl_deadline    INTEGER,
    hard_deadline   INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    ready_at        INTEGER,
    destroyed_at    INTEGER,
    destroy_reason  TEXT,
    error           TEXT
);
CREATE INDEX sandboxes_owner_orb ON sandboxes (owner_orb_id, state);
CREATE INDEX sandboxes_owner_thread ON sandboxes (owner_thread_id, created_at DESC);
CREATE INDEX sandboxes_state ON sandboxes (state);
-- +goose Down
DROP TABLE sandboxes;
ALTER TABLE orbs DROP COLUMN sandbox_network;
```

- 时间一律 Unix 毫秒。`orbs.sandbox_network IS NOT NULL` 即「这台 orb 是沙箱」，值是创建时的网络模式。
- `destroy_reason` 取值：`agent` `owner` `idle` `ttl` `max_lifetime` `thread_archived` `thread_deleted` `owner_orb_gone` `lost` `create_failed` `create_timeout`。

### C2 provider 协议（`schema/openapi.yaml`）

- `JobOp` 枚举（`:8160`）加 `set_network`（生成 `apigen.SetNetwork`）。
- 新 `SandboxNetwork`：`{ type: string, enum: [none, egress], x-enum-varnames: [SandboxNetworkNone, SandboxNetworkEgress] }`。
- 新 `JobSpawnSandbox`：`{ type: object, required: [network], properties: { network: SandboxNetwork } }`；`JobSpawnParams`（`:15550`）加可选 `sandbox: JobSpawnSandbox`。
- 新 `JobNetworkParams`：`{ type: object, required: [network], properties: { network: SandboxNetwork } }`。
- `ProviderCapabilities`（`:11468`）加可选 `sandbox: boolean`（缺省 false）。
- link：新流类型 `sandbox`（`LinkStreamSandbox { t: [sandbox], threadId }`，在 `LinkStreamLibrary` 出现的每一处并列加入）；hello 的 `features` 说明里加 `sandbox`、`fs-tar`。

### C3 配置

`orynd.yaml`（`config.Orynd.Sandbox`，yaml 键 `sandbox`）：

| 键 | 类型 | 默认 | 校验 |
|---|---|---|---|
| `enabled` | bool | `true` | — |
| `max_per_orb` | int | `2` | ≥ 1 |
| `max_total` | int | `8` | ≥ `max_per_orb` |
| `idle` | Duration | `30m` | 5m–4h |
| `max_lifetime` | Duration | `12h` | ≥ `idle`，≤ 72h |
| `default_backend` | string | `kvm` | `kvm` 或 `docker` |

`provider.yaml`：

```yaml
docker:
  sandbox:
    enabled: true
    network: "oryn-sbx"          # egress 模式接入的 docker 网络；enabled 时必填
    dns: ["1.1.1.1"]             # 可选；egress 模式的 --dns，每项须是 IP
    socket_dir: "/run/oryn-sbx"  # 中继 socket 目录，须是绝对路径；这是默认值
firecracker:
  sandbox:
    enabled: true
```

### C4 环境变量与中继

- `ORYN_SANDBOX=1`：orbd 以精简模式运行。
- `ORYN_LINK_VIA`：orbd 拨 `ORYN_URL` 时底层连接的走法。取值 `unix:<绝对路径>` 或 `vsock:<端口>`（连 CID 2）。TLS、WebSocket 与 Bearer 令牌仍是 orbd ↔ orynd 端到端。
- docker：宿主目录 `<socket_dir>/<orbId>/`（0755）里的 `link.sock`（0666），整个目录以只读挂进容器的 `/run/oryn-link`；容器内 `ORYN_LINK_VIA=unix:/run/oryn-link/link.sock`。挂目录而不是挂 socket 文件：provider 重启后重建 socket，文件挂载会失效。
- Firecracker：guest 连 vsock `CID 2 : 5006`（`fcguest.LinkPort`），Firecracker 把它转到 jail 里的 `v.sock_5006`（Firecracker v1.18.0 `docs/vsock.md`：guest 发起的连接落到 `<uds_path>_<PORT>`）；guest 内 `ORYN_LINK_VIA=vsock:5006`。
- 中继目标：spawn 任务里 `env.ORYN_URL` 的 host:port（http / ws 缺省 80，https / wss 缺省 443）。每台沙箱同时最多 8 条中继连接，超出的立即关闭。

### C5 `sandbox` 流协议（owner orbd → orynd，一条流一次调用）

流头 `{"t":"sandbox","threadId":"<调用方线程>"}`。之后都是 JSON 行：

- orbd → orynd 一行请求（≤ 1 MiB）：`{"op":"<op>","params":{…}}`。
- orynd → orbd 零或多行输出：`{"s":"o"|"e","d":"<文本>"}`（只有 `exec` 有），最后恰好一行 `{"result":{…}}` 或 `{"error":{"code":"…","message":"…"}}`，然后 orynd 关流。
- orbd 在收到最后一行之前关流 = 放弃：orynd 取消这次操作（`create` 除外，见步骤 3.2）。

| op | params | result |
|---|---|---|
| `status` | `{}` | `{available, backends: ["kvm","docker"], reason?}` |
| `create` | `{network, backend?, type?, idleMinutes?, ttlMinutes?}` | `{sandbox: Sandbox}` |
| `list` | `{}` | `{sandboxes: Sandbox[]}`（本 owner orb 的在用沙箱，加最近 60 分钟内结束的） |
| `exec` | `{sandbox, command, cwd?, env?, timeoutSec?, detach?}` | 同步：`{exit, durationMs}`；`detach`：`{jobId}` |
| `cp` | `{sandbox, direction: "to_sandbox"\|"from_sandbox", source, dest, exclude?, gitignore?}` | `{files, bytes, skipped}` |
| `network` | `{sandbox, network}` | `{sandbox: Sandbox}` |
| `extend` | `{sandbox, ttlMinutes}` | `{sandbox: Sandbox}` |
| `jobs` | `{sandbox, action: "list"\|"logs"\|"kill", job?, offset?}` | list：`{jobs: OrbSandboxJob[]}`；logs：`OrbSandboxJobLog`；kill：`{ok: true}` |
| `destroy` | `{sandbox}` | `{ok: true}` |

`Sandbox`（REST 与流共用，openapi schema 同名）：`{id, backend, type, provider, network, createdNetwork, state, ownerThreadId, idleSec, lastActiveAt, idleDeadline, ttlDeadline?, hardDeadline, createdAt, readyAt?, destroyedAt?, destroyReason?, error?}`，其中 `idleDeadline = lastActiveAt + idleSec*1000`。

参数范围：`idleMinutes` 5–240；`ttlMinutes` 1–720；同步 `timeoutSec` 1–1800（缺省 600）；`detach` 的 `timeoutSec` 0（不限）或 1–86400；`command` 1–32000 字符；`env` ≤ 64 项、键匹配 `^[A-Za-z_][A-Za-z0-9_]*$`、总长 ≤ 64 KiB；`exclude` ≤ 64 条、每条 ≤ 256 字节。

错误码：`sandbox_disabled` `no_sandbox_provider` `invalid_request` `invalid_type` `forbidden` `quota_per_orb` `quota_total` `sandbox_not_found` `sandbox_not_ready` `network_unavailable` `create_failed` `create_timeout` `capacity` `orbd_outdated` `transfer_too_large` `not_found` `outside_home` `protected_path` `not_a_git_worktree` `job_not_found` `too_many_jobs` `link_lost` `internal`。

### C6 orbd 本机路由（控制面经 `orbs.Service.Do` 调用；schema 加在 openapi 的 `OrbBoxExecRequest` 旁，`:9460`）

- `GET /fs/tar?path=&exclude=…&gitignore=1` → `application/x-tar` 流，响应头 `X-Oryn-Tar-Kind: file|dir`。普通 orb 与沙箱都有；runner 上 404。
- `PUT /fs/tar?path=&kind=file|dir`，请求体是 tar → `OrbFsTarResult {files, bytes, skipped}`。
- 以下只在精简模式注册：
  - `POST /sandbox/exec`，体 `OrbSandboxExecRequest {command, cwd, env, timeoutSec, detach}` → 同步时是 NDJSON（格式同 `POST /box/exec`：`{"s","d"}` 行 + `{"exit":N}`）；`detach` 时是 JSON `{jobId}`。
  - `GET /sandbox/jobs` → `{jobs: OrbSandboxJob[]}`，`OrbSandboxJob {id, command, state: "running"|"exited", exit?, startedAt, endedAt?, logBytes, logTruncated}`。
  - `GET /sandbox/jobs/{id}/log?offset=N` → `OrbSandboxJobLog {text, offset, nextOffset, eof, state, exit?}`。
  - `POST /sandbox/jobs/{id}/kill` → `{ok: true}`。
- hello 特性：普通 orb 追加 `fs-tar`；精简模式恰为 `["fs","fs-tar","metrics","sandbox"]`，监督着 dockerd 时再加 `docker`。

### C7 cp 的路径语义

`dest` 永远是目标的完整路径，不是父目录。`source` 是文件 → `dest` 成为该文件；`source` 是目录 → `dest` 成为该目录，内容合并进已存在的 `dest`。父目录自动创建。两端路径都必须落在各自 `$HOME` 之下、不等于 `$HOME`、不在 `$HOME/.oryn/` 之内。

### C8 工具与 CLI

| 工具 | 参数 | approval |
|---|---|---|
| `sandbox_create` | `network`（必填）、`backend?`、`type?`、`idleMinutes?`、`ttlMinutes?` | exec |
| `sandbox_exec` | `sandbox`、`command`、`cwd?`、`env?`、`timeoutSec?`、`detach?` | exec |
| `sandbox_cp` | `sandbox`、`direction`、`source`、`dest`、`exclude?`、`gitignore?` | exec |
| `sandbox_network` | `sandbox`、`network` | exec |
| `sandbox_jobs` | `sandbox`、`action`、`job?`、`offset?` | exec |
| `sandbox_extend` | `sandbox`、`ttlMinutes` | exec |
| `sandbox_list` | 无 | read |
| `sandbox_destroy` | `sandbox` | exec |

```
oryn sandbox up --network none|egress [--backend docker|kvm] [--type T] [--idle 30m] [--ttl 2h] [--json]
oryn sandbox exec <id> [--cwd D] [--env K=V]... [--timeout 600] [--detach] -- <command...>
oryn sandbox cp [--exclude P]... [--gitignore] <src> <dst>        # 恰有一侧形如 <id>:<path>
oryn sandbox net <id> none|egress
oryn sandbox jobs <id> [ls]
oryn sandbox jobs <id> logs <job> [--offset N]
oryn sandbox jobs <id> kill <job>
oryn sandbox extend <id> --ttl 1h
oryn sandbox ls [--json] [--thread <id>]
oryn sandbox rm <id>
```

### C9 REST 与 WS

- `GET /api/v1/sandboxes?thread=<线程 id>` → `SandboxList {sandboxes: Sandbox[]}`（operationId `listSandboxes`；`thread` 省略 = 全部在用的）。
- `DELETE /api/v1/sandboxes/{sandbox}` → 204（operationId `deleteSandbox`；不存在 404 `sandbox_not_found`）。
- WS `sandboxes.changed`：`WsServerSandboxesChanged {type, threadId}`，写法同 `WsServerBoxesChanged`（`:15179`，映射表 `:14904`）。

## Approach

通用约定：每步一次提交，`go build ./...` 无输出、所列 `go test` 打印 `ok`、涉及 web 的步骤 `cd web && bun run check && bun run test` 通过。改 openapi 后跑 `make gen`，改 SQL 后跑 `make gen-sqlc`，生成物一并提交（`make check-gen` 在 CI 里卡 diff）。

可并行的切片（契约已冻结）：阶段 1 完成后，「2.1–2.4 orbd」「2.5–2.7 provider」「3.1 orbs」三条互不依赖；3.2 依赖 3.1；3.3、4.x 依赖 3.2；5.2 依赖 5.1。同时至多 5 个子 agent。

### 阶段 0 — 探针

**0.1 docker 的 socket 挂载与网络切换**（root，本机已有 `oryn-orb:dev` 与 Docker 29.9.2）

```bash
docker network create -o com.docker.network.bridge.enable_icc=false oryn-sbx-probe
install -d -m 700 /run/oryn-sbx-probe && install -d -m 755 /run/oryn-sbx-probe/p1
python3 -c 'import socket,os;p="/run/oryn-sbx-probe/p1/link.sock";s=socket.socket(socket.AF_UNIX);s.bind(p);os.chmod(p,0o666);s.listen(1);c,_=s.accept();print("HOST GOT",c.recv(16))' &
docker run -d --name sbx-probe --network oryn-sbx-probe -v /run/oryn-sbx-probe/p1:/run/oryn-link:ro oryn-orb:dev sleep 600
docker exec -u user sbx-probe python3 -c 'import socket;s=socket.socket(socket.AF_UNIX);s.connect("/run/oryn-link/link.sock");s.send(b"hi")'
docker network disconnect oryn-sbx-probe sbx-probe && docker exec sbx-probe cat /proc/net/dev
docker network connect oryn-sbx-probe sbx-probe && docker exec sbx-probe curl -sS -m 10 -o /dev/null -w '%{http_code}\n' https://example.com
docker rm -f sbx-probe; docker network rm oryn-sbx-probe; rm -rf /run/oryn-sbx-probe
```

通过判据：宿主打印 `HOST GOT b'hi'`；断开后 `/proc/net/dev` 只剩 `lo`；重连后打印 `200`。`docker info --format '{{json .Runtimes}}'` 含 `sysbox-runc` 时，加 `--runtime sysbox-runc` 把前三条再跑一遍。失败分支见「Assumptions & contingencies」。

### 阶段 1 — 契约落地（无行为变化）

**1.1 openapi 与生成物**
- 按 C2 改 `schema/openapi.yaml`；另加：`Orb` 与 `ProviderOrbSummary` 各加可选 `sandbox: boolean`（紧挨 `box`）；provider 总览里与 `boxes` 计数同一 schema 的地方加 `sandboxes: integer`（`boxes` 在 `required` 里就一并加入）；C5 的 `Sandbox`、`SandboxList`、`SandboxBackend`（`[docker, kvm]`）、`SandboxState`；C6 的 `OrbSandboxExecRequest`、`OrbSandboxJob`、`OrbSandboxJobLog`、`OrbFsTarResult`；C9 的 `WsServerSandboxesChanged` 与映射项。REST 路径留到 5.1（加路径会要求处理器方法存在）。
- `make gen`。
- Accept：`go build ./...` 无输出；`make lint-spec` 通过。

**1.2 迁移与查询**
- 新建 C1 的迁移。
- 新建 `internal/store/queries/sandboxes.sql`，查询名与语义：
  - `CreateSandbox :exec`；`GetSandbox :one`（按 id）；`GetSandboxByOrb :one`。
  - `ListSandboxesByOwnerOrb :many`：`owner_orb_id = @owner_orb_id AND (state IN ('creating','ready','destroying') OR COALESCE(destroyed_at, created_at) >= @since) ORDER BY created_at`。
  - `ListSandboxesByThread :many`：同上，条件换成 `owner_thread_id`。
  - `ListLiveSandboxes :many`：`state IN ('creating','ready','destroying')`。
  - `CountLiveSandboxes :one` 与 `CountLiveSandboxesByOwnerOrb :one`：`state IN ('creating','ready')`。
  - `MarkSandboxReady :execrows`：`state='ready', ready_at=@now, last_active_at=@now WHERE id=@id AND state='creating'`。
  - `MarkSandboxFailed :execrows`：`state='failed', error=@error, destroy_reason=@reason, destroyed_at=@now WHERE id=@id AND state='creating'`。
  - `MarkSandboxDestroying :execrows`：`state='destroying', destroy_reason=@reason WHERE id=@id AND state IN ('creating','ready')`。
  - `MarkSandboxDestroyed :execrows`：`state='destroyed', destroyed_at=@now WHERE id=@id AND state='destroying'`。
  - `TouchSandbox :exec`：`last_active_at=@now WHERE id=@id AND state='ready'`。
  - `SetSandboxNetwork :exec`；`SetSandboxTTL :exec`（写 `ttl_deadline`）。
  - `TrimSandboxes :execrows`：删除 `state IN ('destroyed','failed') AND destroyed_at < @before`。
- `internal/store/queries/orbs.sql`：`CreateOrb` 加列 `sandbox_network`（`sqlc.narg('sandbox_network')`）；加 `ListOrphanSandboxOrbs :many`：`sandbox_network IS NOT NULL AND state NOT IN ('deleting','deleted') AND created_at < @before AND NOT EXISTS (SELECT 1 FROM sandboxes s WHERE s.orb_id = orbs.id AND s.state IN ('creating','ready'))`。
- `make gen-sqlc`。
- 新增 `internal/store/m30_test.go` 的 `TestM30SandboxesMigrationDownUp`（写法同 `github_migration_test.go:47`）：`DownTo(63)` 后 `sandboxes` 表与 `orbs.sandbox_network` 列消失，`Up` 后恢复。
- Accept：`go test ./internal/store/...` 打印 `ok`。

**1.3 配置**
- `internal/config/config.go`：新类型 `Sandbox`（字段与 yaml 键见 C3），`Orynd` 加 `Sandbox Sandbox \`yaml:"sandbox"\``，`DefaultOrynd` 填默认值，`Orynd.Validate` 按 C3 校验（错误文案前缀 `sandbox.<键>`）。
- 同文件：新类型 `DockerSandbox{Enabled bool; Network string; DNS []string; SocketDir string}`，`Docker` 加 `Sandbox DockerSandbox \`yaml:"sandbox"\``；`LoadProvider` 里 `SocketDir` 空则取 `/run/oryn-sbx`，`driver == "docker"` 且 `Enabled` 时校验 `network` 非空、`socket_dir` 是绝对路径、`dns` 每项能被 `netip.ParseAddr` 解析。
- `internal/config/firecracker.go`：新类型 `FirecrackerSandbox{Enabled bool}`，`Firecracker` 加 `Sandbox FirecrackerSandbox \`yaml:"sandbox"\``。
- 在现有的配置表驱动测试里加用例：默认值、每条校验的失败文案。
- Accept：`go test ./internal/config/` 打印 `ok`。

### 阶段 2 — orbd 与 provider

**2.1 link 的可选拨号方式**（`internal/orbdrt`）
- 把 `internal/providerrt/fc_dial.go` 的 `dialUnix` 移到新包 `internal/unixsock`：`func Dial(ctx context.Context, path string) (net.Conn, error)`（原样）与新增 `func Listen(path string, mode os.FileMode, uid int) (net.Listener, error)`（先删同名旧文件；路径 ≥ 100 字节时用 `O_PATH|O_DIRECTORY` 打开目录后绑定 `/proc/self/fd/<fd>/<名字>`，与 `Dial` 同一手法；绑定后 `chmod`，`uid >= 0` 时 `chown uid:uid`）。改掉 `dialUnix` 的全部调用点（`grep -rn "dialUnix(" internal/providerrt`），删除 `fc_dial.go` 里的实现，其测试 `fc_dial_test.go` 移到新包。
- `tlsclient.go`：签名改为 `orbHTTPClient(caPEM, via string) (*http.Client, error)`，调用点 `grep -rn "orbHTTPClient(" internal/orbdrt`。`via == ""` 时行为不变。否则无论有没有 CA 都构造 client：克隆 `http.DefaultTransport`，`Proxy = nil`，`DialContext` 忽略入参地址、返回 `dialVia(ctx, via)`；给了 CA 时照旧只信任它。
- 新文件 `linkvia.go`：`parseLinkVia(v string) (kind, arg string, err error)` 只接受 `unix:<绝对路径>` 与 `vsock:<1..4294967295>`；`dialVia` 的 unix 分支调 `unixsock.Dial`。
- 新文件 `linkvia_linux.go`：`dialVsock(ctx, port uint32) (net.Conn, error)`——`unix.Socket(AF_VSOCK, SOCK_STREAM|SOCK_CLOEXEC, 0)`，在 goroutine 里 `unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port})`，ctx 先结束则关 fd；连上后 `unix.SetNonblock(fd, true)`，`os.NewFile` 包成 `vsockConn`（内嵌 `*os.File`，补 `LocalAddr` / `RemoteAddr`，`Network()` 为 `"vsock"`）。没有现成等价物：仓库里只有 guest-init 的 vsock 监听（`internal/orbdrt/guestinit/init.go:215`），没有拨号。新文件 `linkvia_other.go`（`//go:build !linux`）：vsock 返回错误 `vsock link is only supported on linux`。
- `Config` 加 `LinkVia string`；`ConfigFromEnv` 读 `ORYN_LINK_VIA`；`New` 用 `orbHTTPClient(cfg.OrbCAPEM, cfg.LinkVia)`，解析失败则启动失败。
- 测试 `TestLinkViaUnix`：`httptest.NewTLSServer` 上挂 `link.Accept`；一个 unix 监听把连接转发到该服务器地址；`orbHTTPClient(<服务器证书 PEM>, "unix:"+path)` 交给 `link.DialClient`，握手成功且服务端收到 Bearer 令牌。`TestParseLinkVia` 覆盖非法写法。
- Accept：`go test ./internal/orbdrt/ -run 'LinkVia' ./internal/unixsock/` 打印 `ok`；`GOOS=darwin go build ./cmd/orbd` 成功。

**2.2 orbd 精简模式**（`internal/orbdrt/runtime.go`、`localapi.go`）
- `Config` 加 `Sandbox bool`；`ConfigFromEnv`：`cfg.Sandbox = os.Getenv("ORYN_SANDBOX") == "1"`。`New`：`Sandbox` 与 `Runner != nil` 或 `Coordinator` 同时出现时报错。
- `New` 里 `cfg.Sandbox` 为真时：不监听 egress（`egressLn` 留 nil，`EgressAddr()` 对 nil 返回空串）；不建 `r.box` / `r.desk` / `r.comp`，不调 `writeAgentAssets`；不调 `listenSSH`、`newBrowserRelayManager`、`detectAgentVersion`。`cfg.NestedDocker` 时照旧 `newDockerd`。`r.svc` 照旧构造（`Run` 结尾要 `killAll`）。新增 `r.sbxJobs = newSandboxJobs(r)`（2.4）。
- `Run`：`serveSocket` / `serveGh` / `svc.reportLoop` 的条件加 `&& !r.cfg.Sandbox`。
- `connectOnce`：`r.svc.markDirty()` 的条件加 `&& !r.cfg.Sandbox`。
- `acceptLoop`：`r.cfg.Sandbox` 时只接受 `link.StreamFS`，其余关闭。
- `onRequest`：`r.cfg.Sandbox` 时只放行 `shutdown`，其余返回 `link.ErrMethodNotFound`。
- `hello()`：按 C6 给特性；普通 orb 的切片追加 `"fs-tar"`。
- `localHandler` 的包装函数：`r.cfg.Sandbox` 时路径不以 `/fs/` 或 `/sandbox/` 开头的一律 404 `not_found`（`not available in a sandbox`）；`/sandbox/*` 路由只在 `r.cfg.Sandbox` 时注册；runner 的拦截前缀加 `/fs/tar`。
- `Shutdown`：`r.cfg.Sandbox` 时调 `r.sbxJobs.killAll()`。
- 测试（沿用本包现有起 `Runtime` 的夹具）：精简模式下 hello 特性恰为 C6 所列；`$HOME/.oryn/orbd.sock` 与 `$HOME/.oryn/core` 不存在；`GET /git/status` 回 404；ctl `setup.run` 回 `method_not_found`；`Sandbox` + `Runner` 报错。
- Accept：`go test ./internal/orbdrt/ -run 'Sandbox'` 打印 `ok`。

**2.3 目录传输 `/fs/tar`**（新文件 `internal/orbdrt/fstar.go`）
- 路径解析 `tarPath(p string, mutate bool) (string, error)`：复用 `safePath`（`paths.go:33`），再拒绝 `$HOME/.oryn` 之内的路径（`protected_path`，读写都拒）。
- `GET`：
  - 不存在 → 404 `not_found`。普通文件 → `X-Oryn-Tar-Kind: file`，tar 里一个条目，名字是 basename。目录 → `dir`，条目名相对源目录、不带前导 `./`；包含目录、普通文件、符号链接（原样，不跟随）；其余类型跳过。
  - `exclude`（可重复）：含 `/` 的模式用 `path.Match` 匹配相对路径；不含 `/` 的匹配任意深度的 basename；命中的目录连同子树跳过。
  - `gitignore=1`：只对目录有效（文件 → 400 `invalid_request`）；`git -C <dir> rev-parse --is-inside-work-tree` 不为 `true` → 400 `not_a_git_worktree`；文件清单取 `git -C <dir> ls-files -z --cached --others --exclude-standard`，只打包仍存在的普通文件与符号链接及其父目录，是目录的条目（子模块）跳过；之后再应用 `exclude`。`.git/` 因此不在其中。
- `PUT`：`http.MaxBytesReader` 上限 `MaxTarBytes = 2 << 30`（字段 `r.maxTarBytes` 供测试调小），超限 413 `transfer_too_large`。
  - `kind=file`：目标是已存在的目录 → 409 `is_a_directory`；`MkdirAll` 父目录；把第一个普通文件条目经 `receiveFile` + `commitFile` 覆盖写到 `path`。
  - `kind=dir`：`MkdirAll(path)` 后 `os.OpenRoot(path)`，全部写入经这个 `os.Root`（它拒绝解析到根之外，包括经由包里先放进来的符号链接；手法同 `boxManager.extract`，`boxapps.go:764`）。条目名用现成的 `archiveName`（`boxapps.go:888`）规范化，非法名 → 400 `invalid_request`。目录 `root.MkdirAll`，`mode & 0o777`；普通文件 `root.OpenFile(O_WRONLY|O_CREATE|O_TRUNC)` 覆盖写，`mode & 0o777`；符号链接：`path.Join(path.Dir(name), hdr.Linkname)` 是绝对路径或以 `..` 开头（指向目标根之外）→ 计入 `skipped`，否则 `root.Symlink`；硬链接与其余类型计入 `skipped`。条目数上限 500000（超出 413 `transfer_too_large`）。
  - 回 `OrbFsTarResult`。
- 路由在 `localHandler` 注册。
- 测试：目录往返（嵌套、可执行位、内部符号链接保留、指向根外的符号链接被跳过）；文件往返；两种 `exclude`；`gitignore`（临时 git 仓库，含已忽略文件与未跟踪文件）；`.oryn` 与 `$HOME` 之外被拒；超限 413。
- Accept：`go test ./internal/orbdrt/ -run 'FsTar'` 打印 `ok`。

**2.4 沙箱里的命令与后台作业**（新文件 `sandboxexec.go`、`sandboxjobs.go`）
- 从 `handleBoxExec`（`boxexec.go:135-195`）抽出 `func (r *Runtime) runExecNDJSON(w http.ResponseWriter, req *http.Request, dir, command string, extraEnv map[string]string, timeout time.Duration)`，承接「`loginEnv` → `bash -lc` → `Setpgid` 与组杀 → NDJSON → 退出码（超时 124、启动失败 127、信号 128+n）」整段；`handleBoxExec` 改为调它（`extraEnv` 为 nil），行为不变。`boxExecDir` 改名 `execDir`（两处共用）。
- `POST /sandbox/exec`：按 C5 的范围校验（不合法 400 `invalid_request`）；`cwd` 空则 `~`，经 `execDir`；非 `detach` 调 `runExecNDJSON`；`detach` 调 `r.sbxJobs.start(...)` 回 `{jobId}`。
- `sandboxJobs`：
  - `start(command, dir string, env map[string]string, timeout time.Duration) (string, error)`：id 为 `j1`、`j2`…递增；同时运行超过 16 个 → 409 `too_many_jobs`；进程同样 `bash -lc` + `Setpgid`；stdout 与 stderr 合写到 `$HOME/.oryn/sandbox/jobs/<id>.log`，写满 64 MiB 后丢弃后续输出并置 `logTruncated`；`timeout > 0` 到点组杀。
  - 记录只在内存里，最多留 200 条，超出时淘汰最早结束的并删其日志。
  - `list()`、`log(id string, offset int64, hasOffset bool)`（单次至多 256 KiB；没给 offset 时取末尾 256 KiB；用 `incompleteRuneTail` 保证 UTF-8 边界；`eof = 已结束 && nextOffset == 文件大小`）、`kill(id)`（组杀；未知 id → 404 `job_not_found`）、`killAll()`。
- 测试：同步 exec 的两路输出、退出码、超时 124、`env` 生效；`detach` 后 list 为 running，logs 读到输出，kill 后为 exited；第 17 个作业被拒；`handleBoxExec` 现有测试不改即通过。
- Accept：`go test ./internal/orbdrt/ -run 'SandboxExec|SandboxJobs|BoxExec'` 打印 `ok`。

**2.5 中继、任务解析与 process 驱动**（`internal/providerrt`）
- 新文件 `relay.go`：
  - `func relayTarget(orynURL string) (string, error)`：按 C4 取 host:port。
  - `type linkRelay`；`func newLinkRelay(ln net.Listener, target string) *linkRelay`（accept 循环；每条连接 `net.DialTimeout("tcp", target, 10*time.Second)` 后双向复制；并发超过 8 条的新连接立即关闭）；`func (r *linkRelay) Close()`（关监听与全部在途连接）。
  - `type relaySet`：`put(orbID string, r *linkRelay)`（替换并关闭旧的）、`drop(orbID string)`、`closeAll()`。
- `driver.go`：`Job` 加 `Network string`；`ParseJob` 加 `case apigen.SetNetwork`，解 `apigen.JobNetworkParams`，值不是 `none` / `egress` 则报错。
- `internal/jobs/jobs.go`：加 `OpSetNetwork = "set_network"`。
- `process.go`（测试用驱动）：`Info()` 置 `Capabilities.Sandbox = new(true)`。`spawn` 在 `j.Spawn.Sandbox != nil` 时：建 `<Root>/orbs/<id>/link/`，`unixsock.Listen(<该目录>/link.sock, 0o600, -1)` 并登记中继（目标 `relayTarget(p.Env.OrynURL)`）；子进程环境加 `ORYN_SANDBOX=1`、`ORYN_LINK_VIA=unix:<路径>`；把创建时的模式写进 `<Root>/orbs/<id>/created-network` 与 `<Root>/orbs/<id>/network`。`Exec` 加 `case "set_network"`：没有 `created-network` → 报错；`created-network` 为 `none` 而目标是 `egress` → 报错文本以 `network_unavailable:` 开头；否则改写 `network` 文件。`delete` 时 `relays.drop`。
- 测试：`relayTarget` 的端口缺省；中继转发与第 9 条连接被关；`ParseJob` 的新分支；process 驱动的 sandbox spawn（真 `orbd` 走 unix 中继连上测试里的 link 服务端，hello 带 `sandbox`）与 `set_network` 三种情形。
- Accept：`go test ./internal/providerrt/ -run 'Relay|ParseJob|ProcessSandbox' ./internal/jobs/` 打印 `ok`。

**2.6 docker 驱动**（`internal/providerrt/docker.go`）
- `Docker` 加 `relays relaySet`。`Info()`：`d.Cfg.Sandbox.Enabled` 时 `Capabilities.Sandbox = new(true)`。
- `Preflight`：开启沙箱时 `docker network inspect <network>` 必须成功，失败文案带 `deploy/docker/oryn-sbx-net.sh up`；`os.MkdirAll(socket_dir, 0o700)`。
- `spawn`：`p.Sandbox != nil` 而未开启 → 报错 `docker: sandbox is not enabled on this provider`。开启时在查容器状态之前 `ensureSandboxRelay(orbID, p.Env.OrynURL)`：建 `<socket_dir>/<orbID>/`（0755），`unixsock.Listen(…/link.sock, 0o666, -1)`，登记中继。已存在容器的分支不变。新建容器时镜像固定为 `d.Cfg.Image`，argv 恰为：

  ```
  create --init --name <id> --hostname <id> --label oryn-orb=1
    --label oryn-sandbox=<none|egress> --label oryn-sandbox-link=<host:port>
    --cpus <c> --memory <m> [--pids-limit N] --shm-size 1g
    [--cap-drop NET_RAW --cap-drop NET_ADMIN]            # 条件同现状：!typ.NestedDocker
    --network <none | cfg.Sandbox.Network> [--dns <ip>]...   # --dns 只在 egress 且配置了 dns 时
    -v <socket_dir>/<id>:/run/oryn-link:ro
    -e ORYN_URL -e ORYN_ORB_ID -e ORYN_ORB_TOKEN [-e ORYN_ORB_CA_PEM] [-e ORYN_NESTED_DOCKER]
    -e ORYN_SANDBOX -e ORYN_LINK_VIA
    [--runtime <R>] --security-opt seccomp=<内置 profile>
    <cfg.Image> orbd run
  ```

  与普通 orb 的差别：不加 `--add-host`，不追加 `extra_run_args`（那是给 orb 的，可能带 `--network`），seccomp 恒用内置 profile。环境值 `ORYN_SANDBOX=1`、`ORYN_LINK_VIA=unix:/run/oryn-link/link.sock` 走 `cmd.Env`，同现有令牌的传法。
- `Exec` 加 `case "set_network"` → `setNetwork`：`docker container inspect --format '{{index .Config.Labels "oryn-sandbox"}}|{{json .NetworkSettings.Networks}}' <id>`；标签为空 → 报错；标签为 `none` 而目标是 `egress` → 报错文本以 `network_unavailable:` 开头；已处于目标状态 → 不做事；否则 `network disconnect -f <net> <id>` 或 `network connect <net> <id>`。
- `delete`：`rm -f` 之后 `relays.drop(id)` 并 `os.RemoveAll(<socket_dir>/<id>)`。
- 实现 `Housekeeper`（接口在 `fc_console.go:37`，`cmd/oryn/provider_run.go:66` 已会调用）：`Housekeep(ctx)` 先 `docker ps --filter label=oryn-sandbox --format '{{.Names}}|{{.Label "oryn-sandbox-link"}}'`，为每台重建监听与中继（provider 重启后恢复），然后等 `ctx.Done()` 再 `relays.closeAll()`。
- 测试（`newFakeDocker`，`driver_test.go:75`；假 docker 是脚本常量 `fakeDocker`，每次调用把 argv 追加进 `$FAKE_DOCKER_DIR/log`，给它加一段：`container inspect` 的 `--format` 含 `oryn-sandbox` 时回放 `$FAKE_DOCKER_DIR/sandbox-inspect` 的内容）：两种模式的 argv 与上面逐字一致；`set_network` 的四种情形；`delete` 清理目录；未开启时 spawn 报错。真机用例 `TestRealDockerSandbox`（`ORYN_TEST_DOCKER=1`，写法同 `docker_real_test.go`，测试自建并删除网络）：`none` 沙箱经中继连上测试里的 link 服务端，hello 带 `sandbox`，`POST /sandbox/exec` 跑 `cat /proc/net/dev` 只有 `lo`；`egress` 沙箱 `set_network` 到 `none` 再回 `egress`，前后各查一次接口列表。
- Accept：`go test ./internal/providerrt/ -run 'Docker'` 打印 `ok`；`make image` 后 `ORYN_TEST_DOCKER=1 go test ./internal/providerrt/ -run TestRealDockerSandbox -v` 打印 `PASS`。

**2.7 Firecracker：guest 协议与驱动**
- `internal/fcguest/fcguest.go`：加常量 `LinkPort uint32 = 5006`；`Request` 加 `Sandbox bool \`json:"sandbox,omitempty"\`` 与 `LinkVia string \`json:"linkVia,omitempty"\``。
- `internal/orbdrt/guestinit/guest.go`：`orbdEnv` 在 `req.Sandbox` 时加 `ORYN_SANDBOX=1`，`req.LinkVia != ""` 时加 `ORYN_LINK_VIA=<值>`。`start` 的校验不变。
- `fc_state.go`：`vmMeta` 加 `Sandbox bool \`json:"sandbox,omitempty"\``、`NoNIC bool \`json:"noNic,omitempty"\``、`LinkTarget string \`json:"linkTarget,omitempty"\``。
- `fc_api.go`：`fcHost` 接口加 `TapLink(vm vmMeta, up bool) error`（真实现：`ip link set dev <tap> up|down`）与 `LinkListen(vm vmMeta) (net.Listener, error)`（真实现：`unixsock.Listen(<jailRoot>/v.sock_5006, 0o600, vm.UID)`）；测试替身 `fakeFC` 同步实现并记录调用。`fcBootArgs`：`vm.NoNIC` 时不带 ` ip=…` 段。`unixAPI.Boot`：`vm.NoNIC` 时跳过 `PUT /network-interfaces/eth0`。
- `firecracker.go`：
  - `Info()`：`d.Cfg.Sandbox.Enabled` 时 `Capabilities.Sandbox = new(true)`。
  - `create`：`p.Sandbox != nil` 而未开启 → 报错 `firecracker: sandbox is not enabled on this provider`；开启时置 `vm.Sandbox = true`、`vm.NoNIC = (network == none)`、`vm.LinkTarget = relayTarget(p.Env.OrynURL)`。
  - `launch`：`!vm.NoNIC` 才 `TapUp`。
  - `boot`：`d.launch` 成功后、`d.api.Boot` 之前，`vm.Sandbox` 时 `LinkListen` 并 `d.relays.put`；`boot` 的失败清理里 `d.relays.drop`。jail 根目录每次 `Launch` 都重建，所以监听只能建在这里。
  - `startRequest`：`vm.Sandbox` 时带 `Sandbox: true`、`LinkVia: "vsock:5006"`；`vm.NoNIC` 时 `DNS` 留空。
  - `Exec` 加 `case "set_network"` → `setNetwork`：VM 不存在或不是沙箱 → 报错；`vm.NoNIC` 而目标是 `egress` → 报错文本以 `network_unavailable:` 开头；否则 `d.host.TapLink(vm, 目标 == egress)`。
  - `delete`：`d.relays.drop(orbID)`。
  - `Housekeep`（`fc_console.go:42`）开头加 `restoreRelays()`：对 `listVMs()` 里 `Sandbox && d.host.Alive(id)` 的 VM 重建监听与中继。
- 测试（`fakeFC`）：`none` 沙箱没有 `TapUp`、Boot 请求序列里没有 `/network-interfaces/eth0`、`boot_args` 不含 `ip=`、`start` 请求带 `sandbox` 与 `linkVia`；`egress` 沙箱有 tap；`set_network` 调到 `TapLink`，`NoNIC` + `egress` 报错；`guestinit` 的 env 测试加两项。真机用例 `TestRealFirecrackerSandbox`（`ORYN_TEST_FIRECRACKER=1`，写法同 `firecracker_real_test.go`）：`none` 沙箱经 vsock 中继连上 link、hello 带 `sandbox`、`POST /sandbox/exec` 跑 `cat /proc/net/dev` 只有 `lo`；`egress` 沙箱里 `curl -sS -m 10 -o /dev/null -w '%{http_code}' https://example.com` 得 `200`，`set_network none` 后同一命令退出码非 0，`set_network egress` 后又得 `200`。
- Accept：`go test ./internal/providerrt/ ./internal/fcguest/ ./internal/orbdrt/guestinit/` 打印 `ok`；`make image && make fc-artifacts` 并按 `docs/deploy.md` 第 12 节安装产物后，以 root 跑 `ORYN_TEST_FIRECRACKER=1 ORYN_TEST_FC_ARTIFACTS=<产物目录> go test ./internal/providerrt/ -run TestRealFirecrackerSandbox -v` 打印 `PASS`。**这是「vsock 能承载 TLS + WebSocket + yamux」的闸门，过了再做阶段 3 之后依赖真机的验收。**

### 阶段 3 — 控制面

**3.1 orbs：沙箱 orb 这一种**（`internal/orbs` 及其调用方）
- `service.go`：
  - 常量 `SandboxNetNone = "none"`、`SandboxNetEgress = "egress"`、`CodeSandboxUnsupported = "sandbox_unsupported"`；`func IsSandbox(o db.Orb) bool { return o.SandboxNetwork != nil }`。
  - `CreateParams` 加 `SandboxNetwork string`（空 = 不是沙箱）。`threads.OrbCreate`（`internal/threads/orbs.go:61`）同步加同名字段，线程侧不使用（`internal/server/adapters.go:32` 的转换要求两者字段一致）。
  - `Create`：在 `Box` 的校验之后加沙箱校验——值不是两个常量之一，或与 `Pool` / `Box` / `Computer` / `Coordinator` / `ProjectID` / `SnapshotID` / `SnapshotOf` / `BrowserProfileID` 同时出现，或 `Provider == ""` → `ErrInvalidState`。
  - `insertOrb`：`base.SandboxNetwork = &p.SandboxNetwork`；事件 detail 加 `"sandbox": true`。
  - `spawnIn`：`t.orb.SandboxNetwork != nil` 时 `p.Sandbox = &apigen.JobSpawnSandbox{Network: apigen.SandboxNetwork(*t.orb.SandboxNetwork)}`。
  - 豁免：`EvaluateIdle`（`:1575`）、`retired`（`:1630`）、`DeleteUnused`（`:1136`）各加 `IsSandbox(orb)`；`Wake`（`:959`）、`Pause`（`:1014`）、`Snapshot`（`:1225`）对沙箱返回 `ErrInvalidState`。
  - 新方法 `func (s *Service) SetSandboxNetwork(ctx context.Context, orbID, network string) error`：照 `snapshotAs`（`:1249`）的写法 `s.jobs.Enqueue(ctx, orb.ProviderID, orb.ID, jobs.OpSetNetwork, apigen.JobNetworkParams{…})` → `s.jobs.Wait` → 非 `succeeded` 时返回带 `job.Error` 原文的错误。`onJobFinished` 不需要新分支。
- `orbhttp.go`：`FeatureSandbox = "sandbox"`、`FeatureFSTar = "fs-tar"`（挨着 `FeatureBox`）。
- `link.go`：
  - `orbLink` 加 `sandbox bool` 与 `lastSample time.Time`；`attach` 构造时 `sandbox: IsSandbox(orb)`。
  - `OnRequest`：`l.sandbox` 时一律返回 `&link.CtlError{Code: CodeSandboxUnsupported, Message: method + " is not available to a sandbox"}`。
  - `OnNotify`：`l.sandbox` 时只处理 `hello` 与 `metrics.sample`，后者距上次不足 10 秒的丢弃；其余丢弃。
  - 流分派循环：`l.sandbox` 时任何 orbd 发起的流（含 `egress`）直接关闭并告警。
  - `runPhase`：在 `l.runner` 分支之后加沙箱分支——hello 不含 `FeatureSandbox` → `connecting → failed`，detail `{"reason": "orbd in the provider image has no sandbox mode; rebuild the orb image"}`；否则 `connecting → ready`，detail `{"reason": "sandbox link"}`；两种情况都直接返回（不发 `setup.run`）。
- 把沙箱 orb 挡在通用入口之外：
  - `internal/orbs/view.go:44` 旁：`IsSandbox(o)` 时 `v.Sandbox = new(true)`。
  - `internal/api/orbs.go:74`：`rejectBoxOrb` 改名 `rejectManagedOrb`（三处调用点同文件 `:88`、`:112`、`:134`），沙箱回 409 `orb_is_sandbox`，文案 `this orb is a sandbox; it is managed by the thread that created it`。
  - `internal/api/providers_overview.go:209`、`:242`：沙箱计入 `item.Orbs.Sandboxes`，`summary.Sandbox = new(true)`。
  - `internal/coord/apitool.go:83`：加 `case orbs.IsSandbox(o)`，`CodeInvalidTarget`，文案 `orb %s is a sandbox; it is managed by the thread that created it`。
  - `internal/threads/threads.go:199` 旁：`orbs.IsSandbox(o)` → `errf("invalid", "orb is a sandbox; threads cannot run on it")`。
  - `internal/threads/threadstest/harness.go:688`：替身的豁免条件加 `orbs.IsSandbox(orb)`。
- 测试（`internal/orbs`）：创建校验的每个互斥项；spawn 任务参数带 `sandbox`；沙箱 link 上任意已注册的 ctl 请求回 `sandbox_unsupported`、`egress` 流被关、`metrics.sample` 限速；hello 后直接 `ready` 且没有 `setup.run` 调用；缺特性时 `failed`；`EvaluateIdle` 不冻结、`DeleteUnused` 返回 false、`Wake` 报错；`SetSandboxNetwork` 的成功与失败。
- Accept：`go test ./internal/orbs/ ./internal/threads/... ./internal/api/ ./internal/coord/` 打印 `ok`。

**3.2 sandboxes 服务**（新包 `internal/sandboxes`，骨架仿 `internal/boxes`）

```go
type Config struct {
    Sandbox       config.Sandbox
    Changed       func(threadID string) // nil = 不通知
    Now           func() time.Time      // 默认 time.Now
    Tick          time.Duration         // 默认 30s
    CreateTimeout time.Duration         // 默认 120s
    LostAfter     time.Duration         // 默认 2m
}
type Orbs interface { // *orbs.Service 满足
    Create(ctx context.Context, p orbs.CreateParams) (db.Orb, error)
    Get(ctx context.Context, orbID string) (db.Orb, error)
    Delete(ctx context.Context, orbID string) error
    Connected(orbID string) bool
    HasFeature(orbID, feature string) bool
    Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
    Subscribe(fn func(orbs.Event))
    SetSandboxNetwork(ctx context.Context, orbID, network string) error
}
func New(cfg Config, st *store.Store, o Orbs) *Service
func (s *Service) Run(ctx context.Context)
func (s *Service) OnStream(ctx context.Context, orbID string, h link.StreamHeader, st *link.Stream) // orbs.StreamFunc
func (s *Service) ThreadRetired(threadID string, deleted bool)
func (s *Service) List(ctx context.Context, threadID string) ([]apigen.Sandbox, error)
func (s *Service) Destroy(ctx context.Context, id string) error // 主人发起，reason owner
```

- 错误类型 `Error{Code, Message}` 与 `errf`，写法同 `boxes.Error`。
- **调用方校验** `caller(ctx, linkOrbID, threadID)`：线程存在、`Kind == "work"`、未归档、`OrbID == linkOrbID`，且该 orb 不是 runner，否则 `forbidden`（照 `boxes.checkCaller`，去掉它对 `Automation` 的拒绝）。除 `status` 外每个 op 都先过它。
- **沙箱定位** `own(ctx, linkOrbID, id)`：行不存在或 `OwnerOrbID != linkOrbID` → `sandbox_not_found`（不暴露别人的沙箱是否存在）；需要运行中的 op 再要求 `state == "ready"` 且 `Connected(orbID)`，否则 `sandbox_not_ready`。
- **选 provider** `pick(ownerOrb, backend, typ)`：候选 = 未停用、租约有效（`DisabledAt == nil && InstanceID != nil && LeaseUntil > now`，同 `internal/api/providers.go:24`）、hello 的 `capabilities.sandbox` 为真、driver 属于目标后端（`docker` 与 `process` 算 `docker`，`firecracker` 算 `kvm`）的 provider。`backend` 给了就只看它；没给先看 `cfg.Sandbox.DefaultBackend`，没有候选再看另一个。候选里有 owner orb 所在的 provider 就用它，否则取名字最小的。没有候选 → `no_sandbox_provider`。`typ` 空则取该 provider 的 `DefaultType`；不在其类型表里 → `invalid_type`，message 列出可用类型名。
- **op `status`**：`{available: cfg.Enabled && 至少一个后端有候选, backends: 有候选的后端, reason}`。
- **op `create`**：
  1. `!cfg.Enabled` → `sandbox_disabled`；参数越界 → `invalid_request`。
  2. 持 `createMu`：`CountLiveSandboxesByOwnerOrb >= MaxPerOrb` → `quota_per_orb`；`CountLiveSandboxes >= MaxTotal` → `quota_total`；`pick`；`orbs.Create(orbs.CreateParams{Provider, Type, SandboxNetwork})`；`CreateSandbox`（`state = creating`，`idle_sec`，`hard_deadline = now + MaxLifetime`，`ttl_deadline = min(now + ttl, hard_deadline)` 或 NULL）。写行失败则 `orbs.Delete` 回收。
  3. 之后的等待用 `context.WithoutCancel`（调用方中途放弃不影响创建，沙箱会出现在 `sandbox_list` 里）：每 500ms 查一次，直到 orb `ready` 且 `Connected` 且 `HasFeature(FeatureSandbox)` → `MarkSandboxReady`，回 `{sandbox}`。
  4. orb 进入 `failed`：`MarkSandboxFailed`（`error` 取 orb 的失败原因），`orbs.Delete`；失败原因以 `capacity:` 开头回 `capacity`，否则 `create_failed`。
  5. 超过 `CreateTimeout`：按 `create_timeout` 销毁并回同名错误。
- **销毁** `destroy(ctx, sb, reason)`：`MarkSandboxDestroying`（0 行即已在销毁，直接返回）→ `orbs.Delete(sb.OrbID)` → `Changed`。行在 orb 到达 `deleted` 时由事件订阅或对账置为 `destroyed`。
- **op `exec`**：`busy[sb.ID]++`（结束时减），`TouchSandbox`；向沙箱发 `POST /sandbox/exec`；NDJSON 逐行原样写回流，遇到 `{"exit":N}` 行改写成 `{"result":{"exit":N,"durationMs":…}}`；没等到 exit 行就断了 → `link_lost`；结束再 `TouchSandbox`。`detach` 时直接回 `{jobId}`。调用方关流 → 取消对沙箱的请求（沙箱 orbd 随之杀进程组）。
- **op `cp`**：两端都要 `HasFeature(FeatureFSTar)`，否则 `orbd_outdated`。源端 `GET /fs/tar`（`to_sandbox` 时源是 owner orb，带 `exclude` / `gitignore`），读响应头的 kind；响应体套一层计数读取器，超过 2 GiB → `transfer_too_large`；目标端 `PUT /fs/tar?path=<dest>&kind=<kind>`。不落盘（手法同 `boxes.Service.Copy`）。任一端的 orbd 错误码原样透传。`busy` 与 `TouchSandbox` 同 exec。
- **op `network`**：`created_network == none` 而目标是 `egress` → `network_unavailable`；与当前相同 → 直接回；否则 `orbs.SetSandboxNetwork`，返回的错误文本以 `network_unavailable:` 开头时映射为同名错误码，其余 `internal`；成功后 `SetSandboxNetwork`（查询）、`TouchSandbox`、`Changed`。
- **op `extend`**：`ttl_deadline = min(now + ttlMinutes, hard_deadline)`；`TouchSandbox`；`Changed`。
- **op `jobs`**：转发到沙箱的三个 `/sandbox/jobs` 路由；`logs` 与 `kill` 调 `TouchSandbox`。
- **op `list`**：`ListSandboxesByOwnerOrb(since = now - 60min)`。**op `destroy`**：`destroy(…, "agent")`。
- **`OnStream`**：读一行请求（上限 1 MiB，超限回 `invalid_request`）；另起 goroutine 读流，读到 EOF 或出错即取消本次操作的 ctx；按上表分派；最后写 `result` 或 `error` 行。非 `*Error` 的错误记日志并回 `internal`。
- **级联**：
  - `ThreadRetired(threadID, deleted)`：异步销毁该线程名下在用的沙箱，reason 为 `thread_deleted` 或 `thread_archived`。
  - `Subscribe` 的回调只把事件投进带缓冲的 channel，由 `Run` 处理：非沙箱 orb 进入 `stopping` / `stopped` / `deleting` / `deleted` / `failed` → 销毁它拥有的在用沙箱（`owner_orb_gone`）；沙箱 orb 进入 `deleted` → `MarkSandboxDestroyed` 并 `Changed`；沙箱 orb 在 `ready` 之后进入 `failed` → 按 `lost` 销毁。
- **对账**（`Run` 每 `Tick` 一次，启动时先跑一次）：
  1. `ListOrphanSandboxOrbs(before = now - 60s)` → `orbs.Delete`。
  2. 逐个 `ListLiveSandboxes`：
     - `destroying`：orb 已 `deleted` → `MarkSandboxDestroyed`；orb 不在 `deleting` → 再调一次 `orbs.Delete`。
     - 归属线程不存在 → `thread_deleted`；已归档 → `thread_archived`。
     - owner orb 不存在或处于上面五个状态之一 → `owner_orb_gone`。
     - `creating` 且 `now - created_at > CreateTimeout + 30s`（创建它的进程已不在）→ `create_timeout`。
     - `ready`：`now >= hard_deadline` → `max_lifetime`；`ttl_deadline` 到点 → `ttl`；该沙箱的 provider 在线（租约有效）而 link 连续 `LostAfter` 不在 → `lost`（计时只在内存里，进程启动时从零开始；provider 离线期间不计时，沙箱留到 provider 回来或到 TTL / 最长寿命）；`busy == 0 && now - last_active_at >= idle_sec` 时查 `GET /sandbox/jobs`，有 `running` 的作业就 `TouchSandbox`，没有就按 `idle` 销毁，查询失败则本轮跳过。
  3. 每小时 `TrimSandboxes(before = now - 7 天)`。
- **接线**（`internal/server/server.go`）：
  - 在 `boxes.New`（`:244`）之后构造 `s.Sandboxes = sandboxes.New(sandboxes.Config{Sandbox: cfg.Sandbox, Changed: …, Tick: sandboxesTick, Now: sandboxesNow}, s.Store, s.Orbs)`（`sandboxesTick` 与 `sandboxesNow` 是包级变量，零值取默认，供 e2e 调小周期与拨快时钟，写法同 `boxesTick`，`server.go:69`）；`Changed` 晚绑定到 5.1 的 `s.Hub.SandboxesChanged`，本步先留 nil 判断。
  - `s.Orbs.HandleStream(link.StreamSandbox, s.Sandboxes.OnStream)`（挨着 `:273` 的 `StreamSSH`）；`internal/link/header.go` 加常量 `StreamSandbox = "sandbox"`。
  - `go s.Sandboxes.Run(runCtx)`（挨着 `:470`）。
  - `threads.Config`（`internal/threads/service.go:76` 旁）加 `ThreadRetired func(threadID string, deleted bool)`；`server.go:233` 的构造里接到 `s.Sandboxes.ThreadRetired`（带 nil 判断，写法同 `BoxThreadsChanged`）。调用点两处：`threads.go` 的 `Patch` 在解锁之后、`archived` 为真时（`:702` 算出 `archived`，就在处理它的那段里）调 `(id, false)`；`deleteThread`（`:791`）在 `s.forget(id)` 之后调 `(id, true)`。
- 测试（假 `Orbs` + 真 sqlite）：调用方校验的每个拒绝项（含别的 orb 的线程冒用、runner）；别的 owner orb 访问 → `sandbox_not_found`；两级配额；`pick` 的优先级与 `invalid_type`；创建成功 / orb 失败 / 超时三条路；idle、ttl、max_lifetime、lost 四种到期（假时钟）；有 running 作业时不按 idle 销毁；`extend` 被 `hard_deadline` 封顶；`network` 的三种情形；线程归档 / 删除与 owner orb 停机的级联；孤儿 orb 清理；exec 流的行转写与 `link_lost`；调用方关流取消 exec。
- Accept：`go test ./internal/sandboxes/ ./internal/server/ ./internal/threads/...` 打印 `ok`。

**3.3 orbd 的 owner 侧转发**（`internal/orbsock`、`internal/orbdrt`）
- `orbsock.go`：加 `MethodSandboxCall = "sandbox.call"` 与 `type SandboxCallParams struct { ThreadID string \`json:"threadId"\`; Op string \`json:"op"\`; Params json.RawMessage \`json:"params,omitempty"\` }`。
- 新文件 `internal/orbdrt/sandboxcall.go`：`func (r *Runtime) sandboxCall(ctx context.Context, params json.RawMessage, out *orbsock.Responder) (any, error)`——`validThreadID` 与 `op` 非空（否则 `invalid_params`）；没有当前 link → `orbsock.CodeOffline`；在当前会话上 `Open(ctx, link.StreamHeader{T: link.StreamSandbox, ThreadID: p.ThreadID})`；写请求行；逐行读（上限 1 MiB）：`s == "o"` → `out.Log(orbsock.StreamStdout, d)`，`"e"` → `StreamStderr`；`result` → 原样返回（`json.RawMessage`）；`error` → `orbsock.Errorf(code, message)`；流在最后一行之前断开 → `offline`。`ctx` 结束（调用方关了 sock）时关流。
- `selfsetup.go` 的 `handleSock`：在 `MethodBoxCall` 分支旁加 `case orbsock.MethodSandboxCall`。runner.sock 与精简模式都不提供（前者只认自己的两个方法，后者不开 orbd.sock）。
- 测试：假控制面接受 `sandbox` 流，回两行输出加 `result` → sock 客户端依次收到两条 log 与 result；回 `error` → sock 错误码一致；客户端中途断开 → 假控制面看到流被关。
- Accept：`go test ./internal/orbdrt/ -run 'SandboxCall' ./internal/orbsock/` 打印 `ok`。

### 阶段 4 — CLI、端到端与工具

**4.1 `oryn sandbox …`**（新文件 `cmd/oryn/sandbox.go`，注册方式同 `cmd/oryn/boxes.go:59` 的 `init`）
- 传输：orb 内（`inOrb()`）先 `thread, err := callerThread()`（`cmd/oryn/orb_thread.go:55`，出错原样返回），再 `sockCall(ctx, orbsock.MethodSandboxCall, orbsock.SandboxCallParams{ThreadID: thread, Op: op, Params: params}, &out, onLog)`（`cmd/oryn/orb_setup.go:116`）；`exec` 的 `onLog` 把 stdout / stderr 分别写到对应的标准流。orb 外只有 `ls` 与 `rm` 可用，走 5.1 的 REST；其余子命令在 orb 外报用法错误 `` `oryn sandbox <子命令>` 只能在 orb 内使用 ``（退出码 2）。5.1 之前 `ls` / `rm` 的 orb 外分支先返回同样的错误，5.1 再接上。
- 子命令与参数见 C8。细节：
  - `up`：`--network` 必填；`--idle` / `--ttl` 是 Go duration，向上取整到分钟；默认只打印沙箱 id，`--json` 打印 `Sandbox`。
  - `exec`：`--` 之后的参数用空格拼成命令（同 `boxes exec`）；退出码取远端的（经 `exitCodeError`）；`--detach` 打印作业 id。
  - `cp`：恰有一侧匹配 `^(sbx_[0-9A-Za-z]+):(.+)$`，否则用法错误；orb 侧的相对路径按当前目录转成绝对路径；打印 `copied <files> files, <bytes> bytes`，`skipped > 0` 时在 stderr 追加一行。
  - `jobs … logs`：正文打到 stdout，`next offset: N` 打到 stderr。
  - `ls`：表格列 `ID BACKEND TYPE NETWORK STATE IDLE-LEFT`。
- 错误：`*orbsock.Error` 打印成 `<code>: <message>`，退出码 2。
- 测试（假传输层，写法同 `cmd/oryn/boxes_test.go`）：`cp` 两侧解析与方向；duration 取整；`exec` 的参数拼接与退出码透传；orb 外受限子命令报错。
- Accept：`go test ./cmd/oryn/ -run 'Sandbox'` 打印 `ok`。

**4.2 端到端**（新文件 `internal/server/e2e_m30_test.go`，结构同 `e2e_m23_test.go`：`newStack` + process 驱动 + 真 `orbd` + 真 `oryn`，串行步骤、失败即停）
- 栈：`cfg.Sandbox` 取默认；`sandboxesTick` 调到 200ms，`sandboxesNow` 换成「真实时间 + 可调偏移」（偏移存在 `atomic.Int64` 里），两者用 `t.Cleanup` 还原（写法同 `e2e_m23_test.go:662`）；一个项目两个线程（各自的 orb）。
- 步骤与期望：
  1. S1 `up --network none --json` → `state: "ready"`、`backend: "docker"`；orb 行 `sandbox_network = 'none'`。
  2. S2 零凭据：`exec <id> -- env` 的输出不含 `ORYN_ORB_TOKEN`、`HINDSIGHT`、`GH_`；`exec <id> -- 'test ! -e ~/.oryn/core && test ! -S ~/.oryn/orbd.sock && echo clean'` 打印 `clean`。
  3. S3 `exec <id> -- 'echo out; echo err >&2; exit 3'` → stdout `out`、stderr `err`、退出码 3。
  4. S4 `exec <id> --timeout 1 -- sleep 30` → 5 秒内返回，退出码 124。
  5. S5 目录上传：owner orb 里建一个 git 仓库（一个已跟踪文件、一个被 `.gitignore` 忽略的文件、一个 `skip.log`），`cp --gitignore --exclude '*.log' ~/src <id>:proj`；`exec <id> -- 'cd ~/proj && find . -type f | sort'` 只列出已跟踪文件与 `.gitignore`。
  6. S6 文件下载：`exec <id> -- 'echo result > ~/out.txt'`，`cp <id>:out.txt ~/got.txt`，内容为 `result`。
  7. S7 后台作业：`exec <id> --detach -- 'echo a; sleep 60'` 得作业 id；`jobs <id>` 显示 `running`；`jobs <id> logs <job>` 含 `a`；`jobs <id> kill <job>` 后显示 `exited`。
  8. S8 网络：`net <id> egress`（创建时为 none）→ `network_unavailable`；`up --network egress` 得第二台，`net <id2> none` 后 process 驱动的 `network` 文件内容为 `none`，再 `net <id2> egress` 后为 `egress`。
  9. S9 配额：第三次 `up` → `quota_per_orb`。
  10. S10 越权：另一个线程（另一台 orb）`exec <id> -- true` → `sandbox_not_found`。
  11. S11 到期：`rm <id2>`；`up --network none --ttl 1m`，把 `sandboxesNow` 的偏移加 2 分钟 → 该行 `destroyed`、`destroy_reason = 'ttl'`，对应 orb 进入 `deleted`。
  12. S12 级联：归档 owner 线程 → 剩余沙箱 `destroy_reason = 'thread_archived'`。
- Accept：`go test ./internal/server/ -run TestE2EM30 -v` 打印 `PASS`。

**4.3 原生工具**（`internal/orbdrt/assets/oryn-orb.ts`）
- 新辅助函数 `sandboxSockCall(sock, request, signal, onChunk?: (stream: "stdout" | "stderr", data: string) => void): Promise<{ ok: true; result: any } | { ok: false; code: string; message: string }>`。它照 `artifactSockCall`（`:312`）的连接与逐行解析写，但把 `log` 行的 `stream` 与未裁剪的 `data` 交给 `onChunk`。不能直接复用 `artifactSockCall`：它丢掉 `stream` 并对 `data` 做 `trimEnd()`。
- 新函数 `registerSandbox(pi)`：`ORYN_ARTIFACT_SOCK` 为空则不注册；整体包在 try / catch 里，失败只 `console.error("oryn-orb: register sandbox tools failed:", err)`（同 `registerSsh`，`:1332`）。按 C8 注册 8 个工具，全部 `loadMode: "discoverable"`；每个工具把参数原样放进 `{ method: "sandbox.call", params: { threadId: process.env.ORYN_THREAD_ID ?? "", op, params } }`；失败用 `sockError(name, code, message)`（`:1177`）。各工具：
  - `sandbox_create`：文本 `Sandbox <id> is ready: backend <b>, type <t>, network <n>. Idle timeout <m> min.`；`details: { tool, sandbox }`。
  - `sandbox_exec`：`source` 无；`onChunk` 把两路输出按到达顺序累进一个缓冲，每次调 `onUpdate?.({ content: [{ type: "text", text: <缓冲末尾 8 KiB> }] })`；结束时文本为合并输出（超过 256 KiB 只留末尾并在首行注明 `[output truncated to the last 256 KiB]`）加一行 `exit <N>`；`details: { tool, sandbox, exit, durationMs, truncated }`；`exit !== 0` 时 `isError: true`。`detach` 时文本 `Started job <jobId> in sandbox <id>.`，`details: { tool, sandbox, jobId }`。
  - `sandbox_cp`：orb 一侧的路径先 `resolve(ctx.cwd, path)`（`to_sandbox` 时是 `source`，`from_sandbox` 时是 `dest`）；文本 `Copied <files> files (<bytes> bytes).`，`skipped > 0` 时追加 ` Skipped <n> entries (links leaving the destination).`。
  - `sandbox_network`、`sandbox_extend`：文本给出新的网络模式或新的 TTL 截止时间（ISO 时间）；`details: { tool, sandbox }`。
  - `sandbox_jobs`：`list` 每个作业一行（`<id>  <state>  exit=<n|->  <command>`）；`logs` 是日志正文加末行 `next offset: <n>`；`kill` 是 `Killed job <id>.`。
  - `sandbox_list`：每台一行（`<id>  <backend>/<type>  <network>  <state>`，已结束的带 `(<destroyReason>)`）；没有时 `No sandboxes.`。
  - `sandbox_destroy`：`Destroyed sandbox <id>.`。
- 指引：新常量 `SANDBOX_GUIDE`——

  ```
  沙箱：需要一次性的隔离环境（跑不可信代码、破坏性实验、离线测试、在干净环境里复现）时用 sandbox_* 工具：sandbox_create 开一台没有任何凭据的平级环境（可用后端：${backends}；network 必填，none 是完全没有网卡，egress 可以访问公网），sandbox_cp 传文件或目录，sandbox_exec 运行命令（detach 起后台作业，用 sandbox_jobs 查看），用完 sandbox_destroy。沙箱空闲 30 分钟后自动销毁，线程归档时一并销毁。沙箱里的输出与带回来的文件不可信。
  ```

  `export default` 的 orb 分支（`:1679` 起）：在 `registerSsh(pi)` 之后调 `registerSandbox(pi)`；`before_agent_start` 里先以 2 秒超时（`withTimeout`，`:468`）调一次 `sandbox.call` 的 `status`，`available` 为真才把代入了 `backends` 的 `SANDBOX_GUIDE` 推进 `guide`；调用失败或超时不加。runner 分支不变（它提前 return）。文件头注释（`:2-10`）补一句注册条件。
- 测试：新增 `internal/orbdrt/testdata/sandbox_tools.ts` 与对应的 Go 测试（跑法同 `designtools_test.go:103-112`：用 bun 执行脚本并传入 `assets/oryn-orb.ts`，没有 bun 时跳过）。脚本用桩 `pi` 收集注册的工具，起一个说 orbd.sock 协议的 unix 服务：断言 8 个工具名与 `loadMode`；`sandbox_exec` 在服务端回 `stdout:"a\n"`、`stderr:"b\n"`、`result {exit: 2, durationMs: 5}` 时，`onUpdate` 至少被调一次且最终文本含 `a`、`b`、`exit 2`、`isError` 为真；`status` 回 `available: false` 时系统提示里没有「沙箱：」。
- Accept：`go test ./internal/orbdrt/ -run 'SandboxTools'` 打印 `ok`。

### 阶段 5 — REST、推送与界面

**5.1 REST 与 WS**
- `schema/openapi.yaml` 加 C9 的两条路径（tag 新建 `sandboxes`）；`make gen`。
- 新文件 `internal/api/sandboxes.go`：`type SandboxesDeps struct{ Service *sandboxes.Service }`，`Server` 加 `Sandboxes *SandboxesDeps`（nil 时 501 `not_implemented`，同 `boxesSvc`）；`ListSandboxes`（`thread` 非空调 `Service.List`，空则返回全部在用的）、`DeleteSandbox`（`Service.Destroy`；`sandbox_not_found` → 404，其余按 `internal/api/boxes.go` 的错误映射写法）。
- `internal/coord/apipolicy.go`：`set("orbs", opRead, none, "listSandboxes")`；`set("orbs", opDeny, deny("sandboxes are destroyed by the thread that owns them or by the owner"), "deleteSandbox")`。
- `internal/realtime/public.go`：`func (h *Hub) SandboxesChanged(threadID string)`，`toAll(apigen.WsServerSandboxesChanged{…})`（挨着 `BoxesChanged`，`:415`）。
- `internal/server/server.go`：`s.API.Sandboxes = &api.SandboxesDeps{Service: s.Sandboxes}`（挨着 `s.API.Boxes`）；3.2 留下的 `Changed` 接到 `s.Hub.SandboxesChanged`。
- `cmd/oryn/sandbox.go`：`ls` / `rm` 的 orb 外分支改走这两个接口（`ls --thread` 传查询参数）。
- 在 4.2 的 e2e 末尾追加 S13：设备令牌 `GET /api/v1/sandboxes?thread=<id>` 列出该线程的沙箱；`DELETE` 一台后其 `destroy_reason = 'owner'`；WS 客户端收到 `sandboxes.changed`。
- Accept：`go test ./internal/api/ ./internal/coord/ ./internal/server/ -run 'Sandbox|APIPolicy|TestE2EM30'` 打印 `ok`；`make check-gen` 退出码 0。

**5.2 Web**（`web/src`）
- 数据层 `api/sandbox-queries.ts`（写法同 `api/box-queries.ts`）：`useThreadSandboxes(threadId)`（`GET /api/v1/sandboxes?thread=`）与 `useDestroySandbox()`；键前缀 `["sandboxes"]`。`realtime/socket.ts`：加 `onSandboxesChanged(cb: (threadId: string) => void)` 与 `case "sandboxes.changed"`（写法同 `:189-201`、`:869`），收到事件与每次连接建立时让该前缀的查询失效。
- 信息页：新组件 `components/thread/SandboxSection.tsx`（结构仿 `JobsSection.tsx`，用 `info-parts.tsx` 的 `InfoBlock` / `InfoRow` / `InfoTag`），挂在 `InfoPanel.tsx` 里 `JobsSection` 之下；列表为空时整块不渲染。每行：id 末 6 位（悬停显示全 id）、后端标签（`Docker` / `KVM`）、网络标签（联网 / 断网）、状态、剩余空闲时间（由 `idleDeadline` 每秒本地倒计时，过点显示「即将回收」）、有 `ttlDeadline` 时显示到期时刻；在用的行有「销毁」按钮（确认框后调 `useDestroySandbox`）；已结束的行置灰并显示原因文案。
- 工具卡：新文件 `tool-render/tools/sandbox.tsx` 导出 `sandboxRenderer` 与 `SANDBOX_TOOLS`（8 个工具名），在 `tool-render/registry.ts` 里照 `ORYN_TOOLS` 的批量注册方式登记。`Summary`：exec 显示命令首行，cp 显示 `source → dest`，create 显示 `backend · network`，其余显示沙箱 id 末 6 位。`Meta`：exec 显示 `exit <n>` 与耗时，cp 显示文件数与字节数。`Result`：用现有的 `ResultText`（`tool-render/parts.tsx:360`）。不设 `livePartial`——运行中的输出尾巴由 `ToolView` 的通用逻辑显示。`ToolView.tsx` 里以工具名为键的两张表（`bash: "terminal"` 在 `:46`，`bash: "exec"` 在 `:101`）各为 8 个工具加一项，取值同 `bash`。
- i18n：`i18n/locales/zh-CN.json` 与 `en.json` 同步加 `infoPanel.sandboxes.*`（标题、各标签、倒计时、「即将回收」、销毁确认、11 种 `destroyReason` 的文案）与 `toolRender.sandbox.*`。
- mock：新文件 `mock/sandboxes.ts`（写法同 `mock/boxes.ts`）处理两条 REST 并在销毁后广播 `sandboxes.changed`；`mock/fixtures.ts` 给一个现有线程加两台沙箱（一台 `ready` + `egress`，一台 `destroyed` + `idle`）和一条 `sandbox_exec` 工具卡帧。
- 测试：`SandboxSection.test.tsx`（渲染两种状态、倒计时、销毁调用）；`tools/sandbox.test.tsx`（exec 的 Summary / Meta、非零退出码的呈现）。
- Accept：`cd web && bun run check && bun run test` 通过；`bun run dev:mock` 下打开带夹具的线程，信息页出现「沙箱」区块，点销毁后该行变为已结束。

### 阶段 6 — 部署脚本与真机配置

**6.1 docker 沙箱网络脚本**（新文件 `deploy/docker/oryn-sbx-net.sh`，写法同 `deploy/firecracker/oryn-fc-net.sh`：`set -euo pipefail`、幂等、`up|down`）
- 环境变量：`ORYN_SBX_NETWORK`（默认 `oryn-sbx`）、`ORYN_SBX_BRIDGE`（默认 `oryn-sbx0`）、`ORYN_SBX_SUBNET`（默认 `172.29.0.0/24`）。
- `up`：网络不存在时 `docker network create --driver bridge --subnet $SUBNET -o com.docker.network.bridge.name=$BRIDGE -o com.docker.network.bridge.enable_icc=false $NETWORK`；`iptables -C … || iptables -I …` 加两组规则：`INPUT -i $BRIDGE -j DROP`（沙箱到宿主一律丢弃——link 走 unix socket，不需要任何放行）；`DOCKER-USER -i $BRIDGE -d <10.0.0.0/8、172.16.0.0/12、192.168.0.0/16、100.64.0.0/10、169.254.0.0/16 各一条> -j DROP`。
- `down`：删掉这些规则与网络。
- Accept：`sudo deploy/docker/oryn-sbx-net.sh up` 连跑两次都退出 0；`docker network inspect oryn-sbx` 成功；`sudo iptables -S INPUT | grep oryn-sbx0` 有一行。

**6.2 dev 配置**
- `dev/provider.yaml` 的 `docker:` 下加 `sandbox: { enabled: true, network: "oryn-sbx", dns: ["1.1.1.1"] }`；`dev/provider-fc.yaml` 的 `firecracker:` 下加 `sandbox: { enabled: true }`。两处各加一行注释说明前置命令（6.1 的脚本；Firecracker 需重装含新 orbd 的产物）。
- Accept：`bin/oryn provider preflight --config dev/provider.yaml` 退出 0。

**6.3 文档**
- `docs/plan-m30.md`：本计划原文（里程碑计划的惯例位置，同 `docs/plan-m29.md`）。
- `docs/protocol.md`：新增 M30 一节——job op `set_network` 与 `JobSpawnParams.sandbox`、provider capability `sandbox`、link 流 `sandbox`（C5 全表）、orbd 本机路由（C6）、`ORYN_SANDBOX` / `ORYN_LINK_VIA`（C4）、REST 与 WS（C9）、沙箱 link 的默认拒绝。
- `docs/features.md`：新增「沙箱」一节——用途、两种后端、两种网络模式与切换、回收与级联规则、配额、谁能用。
- `docs/deploy.md`：第 2 节加 `sandbox:` 配置表；第 5 节加 docker 沙箱（`deploy/docker/oryn-sbx-net.sh`、`docker.sandbox`、防火墙规则不随重启持久化）；第 12 节加 `firecracker.sandbox` 与「需重装根盘」。
- `README.md`：功能格与终端命令各加一处沙箱说明。
- Accept：`grep -c sandbox docs/protocol.md docs/features.md docs/deploy.md README.md` 每个文件的计数都大于 0；`docs/plan-m30.md` 存在。

## Critical files & anchors

- `internal/orbs/link.go:235-358`（`attach`）与 `:486`（`runPhase`）——沙箱 link 的默认拒绝与「hello 后直接 ready」都落在这里；runner 的白名单与 `runnerReady` 是对照写法。
- `internal/orbdrt/runtime.go:313-530`（`New`）、`:554`（`Run`）、`:802`（`acceptLoop`）、`:850`（`hello`）、`:913`（`onRequest`）——精简模式的全部开关点；现有的 `Coordinator` / `runner` 条件是对照写法。
- `internal/providerrt/firecracker.go:329-352`（`boot`）与 `internal/providerrt/fc_state.go:49-58`（jail 路径）——vsock 中继的监听必须建在 `launch` 之后、`Boot` 之前，因为 jail 根目录每次启动都重建。
- `internal/boxes/call.go`（`OnCall` / `checkCaller`）与 `internal/boxes/apps.go`（`Exec` / `Copy`）——调用方校验与「两台 orbd 之间流式中转」的现成写法。
- `internal/orbdrt/assets/oryn-orb.ts:312`（`artifactSockCall`）、`:1332`（`registerSsh`）、`:1654`（`export default`）——工具注册、sock 调用与指引拼接的模板。

## Verification

自动化（全部在仓库根）：

```bash
go build ./... && go vet ./...
go test ./internal/store/... ./internal/config/ ./internal/unixsock/ ./internal/link/ ./internal/jobs/ \
        ./internal/fcguest/ ./internal/orbdrt/... ./internal/providerrt/ ./internal/orbs/ ./internal/sandboxes/ \
        ./internal/threads/... ./internal/api/ ./internal/coord/ ./internal/server/ ./cmd/oryn/
make check-gen
(cd web && bun run check && bun run test)
```

期望：每个包打印 `ok`，`make check-gen` 退出 0，web 两条命令通过。

真机闸门（本机 root，有 `/dev/kvm`）：

```bash
make build && make image && make fc-artifacts      # 之后按 docs/deploy.md 第 12 节安装 Firecracker 产物
ORYN_TEST_DOCKER=1 go test ./internal/providerrt/ -run TestRealDockerSandbox -v
sudo ORYN_TEST_FIRECRACKER=1 ORYN_TEST_FC_ARTIFACTS=build/fc go test ./internal/providerrt/ -run TestRealFirecrackerSandbox -v
```

期望：两条都 `PASS`。

真机验收（dev 栈：6.1 的脚本、6.2 的配置、`make dev` 与两个 provider 都已起；在一个 docker orb 的线程终端里执行）：

| 编号 | 操作 | 期望 |
|---|---|---|
| V1 | `id=$(oryn sandbox up --network none --backend kvm)`；`oryn sandbox exec $id -- cat /proc/net/dev` | 只有 `lo` 一行接口；`oryn sandbox ls` 显示 `kvm`、`none`、`ready` |
| V2 | `oryn sandbox exec $id -- 'curl -sS -m 5 https://example.com; echo rc=$?'` | `rc` 非 0 |
| V3 | `id2=$(oryn sandbox up --network egress --backend docker)`；`oryn sandbox exec $id2 -- curl -sS -m 10 -o /dev/null -w '%{http_code}\n' https://example.com` | `200` |
| V4 | `oryn sandbox exec $id2 -- 'curl -sS -m 5 http://172.18.0.1:7600/healthz; echo rc=$?'` | `rc` 非 0（宿主与私网段被挡） |
| V5 | `oryn sandbox net $id2 none`，重复 V3 的 curl；再 `oryn sandbox net $id2 egress`，再重复 | 先失败，后 `200` |
| V6 | `oryn sandbox net $id egress` | `network_unavailable: …` |
| V7 | `oryn sandbox cp --gitignore ~/workspace/repo $id:repo`；`oryn sandbox exec $id -- 'ls ~/repo && test ! -e ~/repo/.git && echo ok'` | 列出仓库文件并打印 `ok` |
| V8 | `oryn sandbox exec $id -- 'cat ~/.oryn/orb-token >/dev/null && env \| grep -c -E "ANTHROPIC\|OPENAI\|GH_TOKEN\|HINDSIGHT"'` | 打印 `0`（令牌文件存在，但环境里没有任何凭据） |
| V9 | 让该线程的 agent「开一台断网的 kvm 沙箱，把仓库拷进去跑测试」 | 对话里出现 `sandbox_create` / `sandbox_cp` / `sandbox_exec` 工具卡，exec 卡运行时输出滚动；信息页「沙箱」区块出现该沙箱与倒计时 |
| V10 | 在信息页点「销毁」 | 该行变为已结束，原因「主人销毁」；`docker ps` / Firecracker 进程里不再有它 |
| V11 | 重启 docker provider 进程，立即 `oryn sandbox exec $id2 -- true` | 至多 20 秒内恢复成功（link 经重建的中继重连） |
| V12 | 归档该线程 | 30 秒内所有沙箱销毁，`oryn sandbox ls --thread <id>`（orb 外）显示原因 `thread_archived` |

## Assumptions & contingencies

- **基础镜像要重建**：沙箱里跑的是镜像自带的 orbd，新 orbd 才有精简模式。上线需要 `make image`，Firecracker 还要 `make fc-artifacts` 并换根盘；换根盘会让旧的项目快照全部失效（`docs/deploy.md:681` 的既有行为）。镜像没更新时创建沙箱会得到 `create_failed`，文案指明要重建镜像。
- **已有 orb 里的 agent 拿不到新工具**，直到它的 orbd 升级（重建 orb）。这与以往给 orbd 加能力时一致。
- **`sandbox.enabled` 默认 `true`**，但 provider 默认不开启，所以不改 `provider.yaml` 就没有任何行为变化。主人若要默认关闭，改 `DefaultOrynd` 一处即可。
- **docker 的 `egress` 隔离依赖 6.1 的防火墙规则**（与现有 orb 网桥的做法一致，规则不随重启持久化，由运维按自己的方式持久化）。没装规则时沙箱能访问宿主与内网；`none` 模式不受影响。
- **0.1 失败时的预案**：
  - socket 挂载只在 sysbox 下失败 → docker 后端拒绝 `runtime` 非空的类型（`invalid_type`，文案说明原因），其余照做。
  - 断开唯一网络或重连失败 → docker 后端的 `set_network` 一律返回以 `network_unavailable:` 开头的错误（文案带 Docker 版本），docker 沙箱只支持创建时选定网络；kvm 不受影响。这会让 D5 在 docker 上降级，执行到这里先把探针输出记进 agent log 并在交付说明里写明。
- **2.7 的真机闸门失败（自写的 `vsockConn` 承载不了 TLS + WebSocket + yamux）** → 改用 `github.com/mdlayher/vsock` 的 `vsock.Dial(vsock.Host, port, nil)` 作为 `dialVsock` 的实现（`go get` 加依赖），其余不变；仍失败则停下修订计划。
- **同步 exec 上限 1800 秒**：与 box 在 orb 内的上限一致（`internal/boxes/call.go` 的 `orbExecMaxSec`），更长的任务用 `detach`。
- **沙箱的 link 令牌对沙箱内的代码可见**（`~/.oryn/orb-token`，同一用户）。它只能以这台沙箱的身份建立 link，而沙箱 link 上的一切 ctl 请求与出站流都被拒（3.1），所以不构成越权；V8 与 3.1 的测试各验一半。
