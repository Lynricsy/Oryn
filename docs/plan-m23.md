# M23 — Boxes（常驻机器与应用部署）：实施计划

> 本文自包含：执行者不需要读过产生它的对话。行号只作提示（取自 `b6ebe5b`），动手前按符号重读。
> 参考对象是 Amp Boxes（<https://ampcode.com/docs/boxes>，2026-11-11 版）；既往调研在 `docs/research/research-AmpApiSdk.md:1346-1398`、`docs/research/research-AmpUiVisuals.md:445-450`。仓库里此前没有任何 Boxes 实现，`M23` 与迁移号 `0045` 未被占用（动手时若已被占用则顺延，并同步改本文里的 `m23`、`0045`）。

## Context

要求：参照 Amp Boxes，给 Oryn 做 Boxes。终态：

- **box** 是一台常驻机器：由 Provider（Docker / Sysbox / Firecracker）拉起的 orb，带 box 标记——不空闲冻结、不随线程回收、掉线自动拉起。规格取 Provider 的 orb 类型。
- 在线程的 orb 里 `oryn boxes deploy` 把仓库构建成 release 送上 box；box 切换 release、重启服务、等端口监听，失败回滚；保留 live 加前两个 release。一台 box 跑多个 app，每个 app 有自己的地址 `https://<app>.<portal_domain>/`、密钥与数据目录。
- app **默认公开**，`--private` 才要求主人登录；orb 里的 agent 可以自由切换。
- 线程可以开在 box 上（「机器」或某个 app 的线上目录）。agent 在对话里可以主动建 box。
- 「更多」分区里有 Boxes 入口：总览页（没有 box 时是介绍页）、box 页（apps、活动流、21 天线程数 / 在线率图、About）、New Box 对话框（直接建，或交给 Agent）、首屏执行位置里的 Boxes 分组、app 行悬停实时预览。
- 另含：每日运行时段、单仓多 app（`include` / 多 target / `--move`）、带源码的 release（box 上的线程能改线上代码，新部署拒绝覆盖 box 上未推送的改动）、全局助手工具、orb 内 agent 的 `oryn-boxes` 技能。

不做（用户已排除）：每 app 独立 Linux 用户、自定义域名、反馈组件。Amp 的工作区共享、计费、封禁、service account 属于多租户面，Oryn 单主人不涉及。

### 已定的设计（实现时不再改）

| 项 | 决定 | 原因 |
|---|---|---|
| 承载 | `orbs.box = 1` 的普通 orb，另有 `boxes` 行持有名字、期望状态、时段 | link、orbd 监督器、portal 拨号、文件通道全部复用；`orbs.computer`（`0041`）是「不属于线程的 orb」的先例 |
| app 地址 | `<app>.<portal_domain>`，app 名全库唯一 | 单主人没有命名空间；box 改名不影响地址。通配证书只覆盖一级子域（`docs/deploy.md:74`），标签不能含点 |
| app 服务 | orbd 现有 `serviceSupervisor` 的第三种来源 `app`，服务名 `<app>--<svc>` | 复用进程组监督、退避、日志、就绪探测、`services.state` 镜像与现有的服务日志 / 重启接口 |
| release 传输 | 控制面把源 orb 的 `GET /fs/file` 响应体直接接到 box orb 的 `PUT /box/incoming/{releaseId}`，不落控制面磁盘 | 读端已存在（`internal/orbdrt/fsapi.go:257`）；写端必须是 box 专用口——通用的 `PUT /fs/file` 经 `mutablePath` 拒绝 `~/.oryn/**`（`localapi.go:251`），这条保护不放松 |
| 部署的意图与结果分开 | actor、repo、root、source、公开性先记在 `box_deploys` 行上，激活成功的事务里才写到 app 与 release | 失败、取消、控制面重启都不会留下「改了一半」的 app；`activating` 的行在重启后能凭 orbd 的状态补完或作废 |
| orb → 控制面 | 一个 ctl 方法 `box.call{threadId, op, params}` | 与 `artifact.call` / `ssh.call` 同形；REST 不接受 orb 令牌 |
| 线程执行位置 | 新增 `executor.kind = "box"`，服务端解析成 `orbId + cwd` | 客户端不拼路径；cwd 由服务端决定，不信任调用方 |
| 线程 cwd | `~`（机器）、`~/apps/<app>/current[/<root>]`（app），存进 `threads.cwd` | orbd 的 `resolvePath` 认 `~`（`internal/orbdrt/paths.go:19`），process 驱动的测试 HOME 是临时目录；`current` 是符号链接，线程重启后落到新 release。控制面需要绝对路径的地方（artifact 的文件能力）经 `/fs/roots` 换算 |
| 生命周期 | `boxes.desired ∈ running\|stopped`，后台对账把 orb 推向期望状态 | 手动启停、时段、掉线恢复共用一条路径 |

## Approach

### 通用约定

- 工作目录 `/root/Projects/Ling/Oryn`，分支 `main`。每个步骤一个提交，格式 `<type>(<scope>): <gitmoji> <subject>`，带 `Co-authored-by: Wine Fox <fox@ling.plus>`。工作区多会话共用：用 eval 的 `commit` 命名空间只提交本步骤自己改的行。
- 每个阶段结束用 `record-agent-log` 记一条（做了什么、为什么）。
- 动手前读 `skill://go`、`skill://sqlite`、`skill://api-design`；阶段 7 读 `skill://react`、`skill://typescript`、`skill://oil-ui`。
- `internal/store/queries/*.sql` 只能用 ASCII（英文注释）；事务里只用回调给的 `q`（库是单连接，`s.Q` 会死锁，见 `internal/store/store.go:40-48`）。
- 时间一律 UTC 毫秒整数（`TimestampMs`）。错误体 `{code, message}`。
- 依赖顺序：阶段 0 冻结契约 → 阶段 1、2、7 互不依赖可并行（1 改 `internal/orbs`、`internal/threads/agent.go` 与新包 `internal/boxes`，2 只改 `internal/orbdrt` 与 `internal/orbs/services.go`，7 只改 `web/` 且用 mock）→ 阶段 3 依赖 1+2 → 阶段 4、5 依赖 3，彼此不改同一文件可并行（4 改 `internal/portal`、`internal/webui` 的接线与 `internal/boxes/preview.go`；5 改 `internal/threads/threads.go`、`internal/api/threads.go`、`internal/artifacts/cap_files.go`、`cmd/oryn/thread.go` 与 `internal/boxes/threads.go`）→ 阶段 6 依赖 5 → 阶段 8 → 阶段 9。每个步骤只调用本步骤或更早步骤引入的符号；后面步骤才有的东西（预览探测、`DropApp` 的 portal 实现、助手的 box 参数）都在引入它的那一步接线。

### 名字与限额（全文引用）

| 项 | 规则 |
|---|---|
| box 名 | `^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`，不含 `--`，不以 `box_` 开头（正则已排除下划线）；全库唯一；至多 16 台 |
| app 名 | `^[a-z0-9]([a-z0-9-]{0,18}[a-z0-9])?$`，不含 `--`；`portalhost.ParseLabel` 或 `ParseArtifactLabel` 能解析的名字拒绝；全库唯一；每台 box 至多 16 个 |
| 服务名（box.yaml 里） | `^[a-z0-9][a-z0-9-]{0,9}$`，不含 `--`；每个 app 1–4 个；监督器里的名字是 `<app>--<svc>`（≤ 32 字符，符合现有 `serviceNameRe`） |
| 密钥 / 环境变量名 | `^[A-Za-z_][A-Za-z0-9_]{0,63}$`；保留名 `PORT`、`ORYN_DEPLOYMENT_URL`、`ORYN_DEPLOYMENT_NAME`、`ORYN_DEPLOYMENT_BOX`、`ORYN_DEPLOYMENT_DATA_DIRECTORY` 拒绝；单个值 ≤ 64 KiB；每个 app 至多 64 个，名字加值合计 ≤ 1 MiB（`execve` 的环境总量与 orbd 请求体上限都按这个数留了余量） |
| app 端口 | 控制面在 30000–30999 里取该 box 上未用的最小值（监督器的自动端口是 20000–29999，不重叠） |
| release | 压缩包 ≤ 512 MiB；解包后 ≤ 4 GiB、≤ 500000 个条目；保留 live + 前 2 个 |
| ID | `ids.New("box")`、`ids.New("bxa")`（app）、`ids.New("bxr")`（release）、`ids.New("bxd")`（一次部署）；形如 `bxr_<26 位 ULID>`，orbd 用 `^bxr_[0-9A-Z]{26}$` 校验 release id |
| box 上的目录 | `~/apps/<app>/releases/<releaseId>/`、`~/apps/<app>/current`（符号链接）、`~/apps/<app>/data/`；orbd 状态 `~/.oryn/box/apps.json`（0600）；上传落点 `~/.oryn/box/incoming/<releaseId>.tar.gz`（目录 0700）；源 orb 打包落点 `~/.cache/oryn/box-releases/<deployId>.tar.gz` |
| 时限 | 传输 30 分钟；激活 10 分钟（其中等端口 `readyTimeoutSec = 60`）；REST `exec` 的 `timeoutSec` 1–3600（缺省 600）；orb 内 `exec` 1–1800（缺省 600）；`box.call` 的 ctl 转发上限 45 分钟（盖住传输 + 激活） |

新错误码与 HTTP 状态：`box_not_found` 404、`app_not_found` 404、`release_not_found` 404、`deploy_not_found` 404、`box_exists` 409、`box_limit` 409、`box_stopped` 409、`box_not_ready` 409、`box_unsupported` 409、`orb_is_box` 409、`app_exists_elsewhere` 409、`app_repo_mismatch` 409、`app_not_deployed` 409、`app_limit` 409、`deploy_in_progress` 409、`box_changes` 409、`release_too_large` 413。名字或参数不合规用现有的 `invalid_request` 400。app 主机名上的数据面另有两个：`app_unavailable` 503、`app_not_deployed` 502（步骤 4.1）。

---

### 阶段 0 — 契约（先冻结，后面各阶段都只消费）

**步骤 0.1 — 迁移与查询**

新建 `internal/store/migrations/0045_boxes.sql`（goose，`-- +goose Up` / `-- +goose Down`，照 `0042_ssh.sql` 的写法）：

```sql
ALTER TABLE orbs ADD COLUMN box INTEGER NOT NULL DEFAULT 0 CHECK (box IN (0, 1));

CREATE TABLE boxes (
  id            TEXT PRIMARY KEY,                 -- box_<ULID>
  name          TEXT NOT NULL UNIQUE,
  orb_id        TEXT NOT NULL UNIQUE REFERENCES orbs(id),
  desired       TEXT NOT NULL DEFAULT 'running' CHECK (desired IN ('running', 'stopped')),
  desired_by    TEXT NOT NULL DEFAULT 'owner'   CHECK (desired_by IN ('owner', 'schedule')),
  desired_at    INTEGER NOT NULL,                 -- ms; a schedule boundary later than this overrides desired
  sched_on      TEXT,                             -- 'HH:MM'; all three NULL = always on
  sched_off     TEXT,
  sched_tz      TEXT,
  created_at    INTEGER NOT NULL
);

CREATE TABLE box_apps (
  id              TEXT PRIMARY KEY,               -- bxa_<ULID>
  box_id          TEXT NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
  name            TEXT NOT NULL UNIQUE,
  port            INTEGER NOT NULL,
  public          BOOLEAN NOT NULL DEFAULT 1,
  -- repo / root / source describe the live release; before the first activation they hold the first deploy's values
  repo            TEXT NOT NULL,                  -- normalized origin, or 'local:<root commit sha>'
  root            TEXT NOT NULL DEFAULT '',       -- app folder inside the repo, '' = repo root
  source          BOOLEAN NOT NULL DEFAULT 1,
  live_release_id TEXT,                           -- NULL = provisional row of a first deploy still in flight
  preview         BOOLEAN NOT NULL DEFAULT 0,
  preview_checked_at INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX box_apps_port ON box_apps(box_id, port);

CREATE TABLE box_releases (
  id           TEXT PRIMARY KEY,                  -- bxr_<ULID>
  app_id       TEXT NOT NULL REFERENCES box_apps(id) ON DELETE CASCADE,
  repo         TEXT NOT NULL,                     -- binding when this release was made; a rollback restores it with root / source
  commit_sha   TEXT NOT NULL,
  branch       TEXT NOT NULL,
  root         TEXT NOT NULL DEFAULT '',
  source       BOOLEAN NOT NULL,
  size_bytes   INTEGER NOT NULL,
  sha256       TEXT NOT NULL,
  services     TEXT NOT NULL,                     -- JSON array of {name, command, cwd}
  thread_id    TEXT,
  state        TEXT NOT NULL CHECK (state IN ('live', 'kept', 'pruned')),
  created_at   INTEGER NOT NULL,
  activated_at INTEGER NOT NULL
);
CREATE INDEX box_releases_app ON box_releases(app_id, created_at DESC);

CREATE TABLE box_deploys (
  id          TEXT PRIMARY KEY,                   -- bxd_<ULID>
  box_id      TEXT NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
  app         TEXT NOT NULL,
  release_id  TEXT NOT NULL,                      -- reserved at prepare
  actor       TEXT NOT NULL CHECK (actor IN ('owner', 'agent', 'assistant')),
  thread_id   TEXT,
  src_orb_id  TEXT NOT NULL,
  repo        TEXT NOT NULL,                      -- intent, applied to the app only when activation succeeds
  root        TEXT NOT NULL DEFAULT '',
  source      BOOLEAN NOT NULL,
  set_public  INTEGER CHECK (set_public IN (0, 1)), -- NULL = keep the app's visibility
  commit_sha  TEXT NOT NULL,
  branch      TEXT NOT NULL,
  first       BOOLEAN NOT NULL,                   -- the app row was created by this deploy
  size_bytes  INTEGER NOT NULL DEFAULT 0,         -- size_bytes / sha256 / services are filled when commit claims the row
  sha256      TEXT NOT NULL DEFAULT '',
  services    TEXT NOT NULL DEFAULT '[]',
  state       TEXT NOT NULL CHECK (state IN ('prepared', 'transferring', 'activating', 'succeeded', 'failed', 'canceled')),
  error       TEXT,
  created_at  INTEGER NOT NULL,
  finished_at INTEGER
);
-- at most one open deploy per app; the INSERT at prepare relies on this
CREATE UNIQUE INDEX box_deploys_open ON box_deploys(box_id, app) WHERE state IN ('prepared', 'transferring', 'activating');

CREATE TABLE box_secrets (
  box_id     TEXT NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
  app        TEXT NOT NULL,
  name       TEXT NOT NULL,
  value_enc  BLOB NOT NULL,                       -- secrets.Box.Seal("box/" || box_id || "/" || app || "/" || name, value)
  size       INTEGER NOT NULL,                    -- len(name) + len(plain value), for the per-app total
  secret     BOOLEAN NOT NULL DEFAULT 1,          -- 0 = plain env var, value readable through the API
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (box_id, app, name)
);

CREATE TABLE box_activity (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  box_id TEXT NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
  ts     INTEGER NOT NULL,
  kind   TEXT NOT NULL CHECK (kind IN ('created', 'started', 'stopped', 'renamed', 'schedule',
                                       'deployed', 'deploy_failed', 'rolled_back', 'restarted', 'undeployed', 'visibility')),
  actor  TEXT NOT NULL CHECK (actor IN ('owner', 'agent', 'assistant', 'schedule', 'system')),
  app    TEXT,
  detail TEXT NOT NULL DEFAULT '{}'               -- JSON: commit, releaseId, threadId, error, public, from, to
);
CREATE INDEX box_activity_box ON box_activity(box_id, id DESC);
```

密钥按 `(box_id, app 名)` 存而不是按 app 行：首次部署之前就能设；下线 app 不删密钥，删 box 时级联删除。删 box 是硬删 `boxes` 行（级联带走其余五张表），orb 走自己的软删除。带 `WHERE` 的唯一索引在本库已有先例（`0011_share_notify.sql:26`）。

新建 `internal/store/queries/boxes.sql`，查询名（sqlc，`:one` / `:many` / `:exec` / `:execrows` 按语义）：

- box：`CreateBox`、`GetBox`、`GetBoxByName`、`GetBoxByOrb`、`ListBoxes`（按 `created_at`）、`CountBoxes`、`RenameBox`、`SetBoxDesired`（`desired, desired_by, desired_at`）、`SetBoxSchedule`、`DeleteBox`。
- app：`CreateBoxApp`、`GetBoxApp`（`box_id, name`）、`GetBoxAppByName`、`ListBoxApps`（`box_id`，按 `name`）、`ListAllBoxApps`、`CountBoxApps`、`ListBoxAppPorts`、`SetBoxAppPublic`、`SetBoxAppLive`（`live_release_id, repo, root, source, updated_at`）、`SetBoxAppPreview`、`DeleteBoxApp`、`DeleteProvisionalBoxApp :execrows`（`box_id, name`，仅 `live_release_id IS NULL`）。
- release：`CreateBoxRelease`、`GetBoxRelease`、`ListBoxReleases`（`app_id`，新到旧）、`SetBoxReleaseState`。
- 部署：`CreateBoxDeploy`、`GetBoxDeploy`、`GetOpenBoxDeploy`（`box_id, app`，三个未结束状态）、`ListOpenBoxDeploysByBox`、`ListBoxDeploysByState`（`state`）、`ListExpiredBoxDeploys`（`prepared` 且 `created_at < @before`）、`ClaimBoxDeploy :execrows`（`UPDATE … SET state = 'transferring', size_bytes, sha256, services WHERE id = @id AND state = 'prepared' AND src_orb_id = @src_orb_id`）、`SetBoxDeployActivating :execrows`（`… WHERE id = @id AND state = 'transferring'`）、`FinishBoxDeploy :execrows`（`SET state = @state, error = @error, finished_at = @now WHERE id = @id AND state IN ('prepared', 'transferring', 'activating')`）、`CancelBoxDeploy :execrows`（同上但写死 `state = 'canceled'`，且只对 `prepared` / `transferring` 生效）。
- 密钥：`PutBoxSecret`（upsert）、`ListBoxSecrets`、`DeleteBoxSecret`、`CountBoxSecrets`、`SumBoxSecretSize`（不含 `@name` 那一条）。
- 活动：`InsertBoxActivity`、`ListBoxActivity`（`box_id`，`LIMIT`）、`ListAllBoxActivity`（`LIMIT`）。

在 `internal/store/queries/orbs.sql` 加 `ListOrbEventsSince`（`orb_id, ts >= @since`，按 `id`）、`GetOrbEventBefore`（`orb_id, ts < @before` 的最后一条）与 `ListOrphanBoxOrbs`（`box = 1`、未删除、`created_at < @before`、`NOT EXISTS (SELECT 1 FROM boxes WHERE orb_id = orbs.id)`）；在 `threads.sql` 加 `ListThreadsByOrbSince`（`orb_id, created_at >= @since`，取 `id, title, created_at, cwd, kind`）与 `CountWorkingThreadsByOrb`（`orb_id`，`state = 'working'`）。`CreateOrb` 的 INSERT 加 `box` 列（参数 `Box int64`）。

跑 `make gen-sqlc`。

**步骤 0.2 — OpenAPI 与生成**

改 `schema/openapi.yaml`，然后 `make gen`、`make lint-spec`。全部新增项：

1. 顶层 `tags` 加 `boxes`。
2. `Orb`（`:9974`）加 `box: { type: boolean, description: 'box 的常驻 orb（M23）；为 false 时省略' }`。
3. `OrbService.properties.source`（`:10143-10151`，是内联枚举，没有独立的组件，也没有 `x-enum-varnames`）：枚举改成 `[file, dynamic, app]`，描述补一句；oapi-codegen 按值生成 `apigen.OrbServiceSourceApp`，`…File` / `…Dynamic` 两个现有常量名不变。`OrbService` 另加可选 `app: string`（所属 box app；`source = app` 时给出）。
4. `ThreadExecutor`（`:9611`）的 `oneOf` 与 `discriminator.mapping` 加 `box: '#/components/schemas/ThreadExecutorBox'`；
   `ThreadExecutorBox`：`required: [kind, boxId]`，`kind: {type: string, enum: [box], x-enum-varnames: [ThreadExecutorBoxKindBox]}`，`boxId: string`，`app: string`（省略 = 机器）。
5. `LinkCtlMethod`（`:13283`）枚举加 `box.call`，`x-enum-varnames` 加 `MethodBoxCall`，描述里加一行「`box.call`（orbd→orynd 请求，M23，由 orb 内 `oryn boxes …` 经 `orbd.sock` 转来，参数 `LinkCtlBoxCallParams`，结果 `LinkCtlBoxCallResult`；runner 的 link 拒绝）」。
   `LinkCtlBoxCallParams{threadId: ThreadId, op: string, params: {type: object, additionalProperties: true}}`（三项必填）；`LinkCtlBoxCallResult{result: {type: object, additionalProperties: true}}`——生成的是 `map[string]interface{}`，所以**每个 op 的结果都是对象**：列表类用下面的 `BoxList` / `BoxReleaseList` / `BoxSecretList` 包一层，没有返回值的 op 给 `{}`。
6. `WsServerMessage`（`:12435`）的 `oneOf` 与 mapping 加 `boxes.changed`；`WsServerBoxesChanged{type: {type: string, enum: [boxes.changed], x-enum-varnames: [WsServerBoxesChangedTypeBoxesChanged]}}`（照 `WsServerSshChanged`，`:12736`）。
7. 协调层审计动作枚举（`:10881-10887`）追加 `create_box`、`start_box`、`stop_box`、`set_box_schedule`、`set_box_app_visibility`、`delete_box`，varnames `ActionCreateBox`、`ActionStartBox`、`ActionStopBox`、`ActionSetBoxSchedule`、`ActionSetBoxAppVisibility`、`ActionDeleteBox`。
8. 组件：

```yaml
BoxState:      { type: string, enum: [starting, running, stopping, stopped, failed] }
BoxDesired:    { type: string, enum: [running, stopped] }
BoxActor:      { type: string, enum: [owner, agent, assistant, schedule, system] }
BoxActivityKind: { type: string, enum: [created, started, stopped, renamed, schedule, deployed, deploy_failed, rolled_back, restarted, undeployed, visibility, thread] }
BoxSchedule:   { required: [on, off, tz], properties: { on: {type: string, pattern: '^([01]\d|2[0-3]):[0-5]\d$'}, off: {同}, tz: {type: string} } }
BoxRelease:    { required: [id, commit, branch, repo, root, source, sizeBytes, state, createdAt, activatedAt],
                 properties: { id, commit, branch, repo, root, source: boolean, sizeBytes: integer, state: {enum: [live, kept, pruned]}, threadId, createdAt, activatedAt } }
BoxApp:        { required: [name, public, repo, root, source, port, deploying, preview, services, createdAt, updatedAt],
                 properties: { name, url, public: boolean, repo, root, source: boolean, port: integer,
                               live: BoxRelease,            # 省略 = 首次部署还在进行，没有可用的 release
                               deploying: boolean, preview: boolean,
                               services: {type: array, items: OrbService}, createdAt, updatedAt } }
Box:           { required: [id, name, orbId, state, desired, provider, type, cpus, memoryMB, createdAt, apps],
                 properties: { id, name, orbId: OrbId, state: BoxState, desired: BoxDesired,
                               stoppedBy: {enum: [owner, schedule]}, failureReason, provider, type,
                               cpus: number, memoryMB: integer, schedule: BoxSchedule, createdAt,
                               lastDeployAt, apps: {type: array, items: BoxApp} } }
BoxList:       { required: [boxes], properties: { boxes: [Box] } }
CreateBoxRequest: { required: [name], properties: { name, provider, type } }
UpdateBoxRequest: { properties: { name, schedule: BoxSchedule, clearSchedule: boolean } }
UpdateBoxAppRequest: { required: [public], properties: { public: boolean } }
BoxRollbackRequest: { properties: { releaseId } }
BoxReleaseList: { required: [releases], properties: { releases: [BoxRelease] } }
BoxSecret:     { required: [name, secret, updatedAt], properties: { name, secret: boolean, value, updatedAt } }   # value 只在 secret=false 时给
BoxSecretList: { required: [secrets], properties: { secrets: [BoxSecret] } }
PutBoxSecretRequest: { required: [value], properties: { value, secret: boolean } }                               # secret 缺省 true
BoxActivity:   { required: [id, boxId, boxName, ts, kind, actor],
                 properties: { id: string, boxId, boxName, ts, kind: BoxActivityKind, actor: BoxActor,
                               app, commit, releaseId, threadId, title, error, public: boolean, from, to } }
BoxActivityList: { required: [items], properties: { items: [BoxActivity] } }
BoxUsage:      { required: [days], properties: { days: { type: array, items: { required: [date, threads, upRatio],
                 properties: { date: {type: string, description: 'UTC, YYYY-MM-DD'}, threads: integer, upRatio: number } } } } }
BoxExecRequest: { required: [command], properties: { command, app, timeoutSec: {type: integer, minimum: 1, maximum: 3600} } }
# orbd 本机 API 的体（照 OrbAttachmentEnsureRequest 的惯例放在 components 里，不被任何 HTTP 操作引用）
OrbBoxService: { required: [name, command, cwd], properties: { name, command, cwd } }    # cwd 相对 release 根，'' = 根
OrbBoxActivateRequest: { required: [releaseId, port, services, env, source, keep, readyTimeoutSec],
                 properties: { releaseId, port: integer, services: [OrbBoxService],
                               env: {type: object, additionalProperties: {type: string}}, source: boolean,
                               keep: integer, readyTimeoutSec: integer, discardBoxChanges: boolean } }
OrbBoxResult:  { required: [ok], properties: { ok: boolean, liveReleaseId, pruned: {type: array, items: {type: string}},
                               code, message, log } }
OrbBoxRollbackRequest: { required: [releaseId, readyTimeoutSec], properties: { releaseId, readyTimeoutSec: integer } }
OrbBoxRestartRequest:  { required: [env, readyTimeoutSec], properties: { env: {同上}, readyTimeoutSec: integer } }
OrbBoxExecRequest:     { required: [command, cwd, timeoutSec], properties: { command, cwd, timeoutSec: integer } }
OrbBoxAppsState:       { required: [apps], properties: { apps: { type: array, items: { required: [name, port, live, releases],
                 properties: { name, port: integer, live: string, activating: string, releases: {type: array, items: {type: string}} } } } } }
                 # live = 最近一次确认就绪的 release（'' = 还没有）；activating = 正在试运行、尚未确认的 release id（没有则省略）
```

9. 路径参数 `BoxPath`（`box`，id 或名字）、`BoxAppPath`（`app`，app 名正则）、`BoxSecretNamePath`（`name`）。路径（tag `boxes`；变更类的 `security` 照 `createSshHost` 写 `[{cookieAuth: [], csrfHeader: []}, {deviceBearer: []}]`；每个操作末尾 `default: { $ref: '#/components/responses/Error' }`）：

| 方法与路径 | operationId | 请求 | 响应 |
|---|---|---|---|
| `GET /api/v1/boxes` | `listBoxes` | — | `BoxList` |
| `POST /api/v1/boxes` | `createBox` | `CreateBoxRequest` | 201 `Box` |
| `GET /api/v1/boxes/{box}` | `getBox` | — | `Box` |
| `PATCH /api/v1/boxes/{box}` | `updateBox` | `UpdateBoxRequest` | `Box` |
| `DELETE /api/v1/boxes/{box}` | `deleteBox` | — | 204 |
| `POST /api/v1/boxes/{box}/start` | `startBox` | — | 202 `Box` |
| `POST /api/v1/boxes/{box}/stop` | `stopBox` | — | 202 `Box` |
| `GET /api/v1/boxes/{box}/activity` | `listBoxActivity` | `limit`（1–100，缺省 50） | `BoxActivityList` |
| `GET /api/v1/box-activity` | `listAllBoxActivity` | `limit` | `BoxActivityList` |
| `GET /api/v1/boxes/{box}/usage` | `getBoxUsage` | — | `BoxUsage` |
| `POST /api/v1/boxes/{box}/exec` | `execBox` | `BoxExecRequest` | 200 `application/x-ndjson` |
| `PATCH /api/v1/boxes/{box}/apps/{app}` | `updateBoxApp` | `UpdateBoxAppRequest` | `BoxApp` |
| `DELETE /api/v1/boxes/{box}/apps/{app}` | `undeployBoxApp` | `purgeData`（boolean） | 204 |
| `POST /api/v1/boxes/{box}/apps/{app}/restart` | `restartBoxApp` | — | `BoxApp` |
| `POST /api/v1/boxes/{box}/apps/{app}/rollback` | `rollbackBoxApp` | `BoxRollbackRequest` | `BoxApp` |
| `POST /api/v1/boxes/{box}/apps/{app}/cancel-deploy` | `cancelBoxDeploy` | — | 204 |
| `GET /api/v1/boxes/{box}/apps/{app}/releases` | `listBoxReleases` | — | `BoxReleaseList` |
| `GET /api/v1/boxes/{box}/apps/{app}/secrets` | `listBoxSecrets` | — | `BoxSecretList` |
| `PUT /api/v1/boxes/{box}/apps/{app}/secrets/{name}` | `putBoxSecret` | `PutBoxSecretRequest` | 204 |
| `DELETE /api/v1/boxes/{box}/apps/{app}/secrets/{name}` | `deleteBoxSecret` | — | 204 |

服务日志与单个服务的重启不新增接口：用现有的 `getOrbServiceLog` / `restartOrbService`（`orbs/{orbId}/services/{<app>--<svc>}/…`）。`boxes cp` 在设备令牌 CLI 上用现有的 `orbs/{orbId}/fs/file`。

`Server` 嵌入了 `apigen.Unimplemented`，新操作在实现前回 501，树照常编译。

**步骤 0.3 — 主机标签**（`internal/portalhost/portalhost.go`，纯函数；阶段 1、3、4 都要用，所以放在契约里）

```go
// ParseAppLabel 解析 box app 主机标签：就是 app 名（^[a-z0-9]([a-z0-9-]{0,18}[a-z0-9])?$，不含 `--`），
// 且不是 portal / artifact 标签。
func ParseAppLabel(label string) (app string, ok bool)
// ValidAppName 与 ParseAppLabel 同一判定（boxes 包校验 app 名用）。
func ValidAppName(name string) bool
// LabelURL 返回 `<Scheme>://<label>.<Domain>[:Port]/`；未启用时为空串。URL、ArtifactURL 改为调用它。
func (b Base) LabelURL(label string) string
func (b Base) AppURL(app string) string { return b.LabelURL(app) }
```

测试（`portalhost_test.go`）：`t-<12hex>-p80`、`a-…` / `p-…` 形的名字不是 app 标签；含 `--`、含点、超长的拒绝；`AppURL` 未启用时为空串；`URL` / `ArtifactURL` 的既有断言不变。

---

### 阶段 1 — 常驻 orb 与 box 生命周期

**步骤 1.1 — orbs：box 标记、豁免与闸门**（`internal/orbs`，以及闸门在 `internal/threads`、`internal/api`、`internal/coord` 的落点）

- `CreateParams`（`service.go:120`）加 `Box bool`，注释「M23：box 的常驻 orb——无项目、不进预热池、不来自快照、不冻结、不随线程回收」。`Create` 里照 `Computer` 那条（`:789`）加一条互斥校验（`Pool` / `ProjectID` / `SnapshotID` / `SnapshotOf` 非空 → `ErrInvalidState`）。`insertOrb`（`:849`）在 `p.Box` 时置 `base.Box = 1`。
- 三处豁免，都紧挨现有判断：`EvaluateIdle`（`:1555`）`if IsRunner(orb) || orb.Box != 0 { continue }`；`retired`（`:1609`）加 `|| orb.Box != 0`；`DeleteUnused`（`:1116`）加 `|| orb.Box != 0`。同步改两处照抄它的测试替身：`internal/threads/threadstest/harness.go:649`、`internal/threads/fixes_test.go:129`。
- `view.go:41` 旁加 `if o.Box == 1 { v.Box = new(true) }`。
- 新增 `var ErrBoxStopped = errors.New("orbs: box is stopped")` 与

  ```go
  // SetBoxGate 注册 box 闸门：返回 false 时 Wake 不唤醒这台 box 的 orb（它被主人或时段停掉了）。
  func (s *Service) SetBoxGate(fn func(orbID string) bool)
  // BoxStopped 报告 orbID 是不是一台被闸门挡住的 box（不是 box、没有闸门、读不到 orb 都是 false）。
  func (s *Service) BoxStopped(ctx context.Context, orbID string) bool
  // Revive 把 ready 却没有 link 的 orb 送回 waking 并入队 spawn（宿主重启、容器退出后的恢复）；状态已变则无操作。
  func (s *Service) Revive(ctx context.Context, orbID string) error
  ```

  `Wake`（`:942`）在取到 orb 之后、`switch orb.State` 之前：`if orb.Box != 0 && s.boxGate != nil && !s.boxGate(orbID) { return ErrBoxStopped }`。
  `Revive` 的函数体：`_, _, err := s.transitionThen(ctx, orbID, StateReady, StateWaking, map[string]any{"reason": "link lost"}, s.spawnIn); return err`——`transitionThen` 返回 `(db.Orb, bool, error)` 并且自己 publish、自己给 provider 发信号，这里不再重复。之后走现成的 spawn → `connecting` → hello → `resuming` → `ready`，orbd 在 resume 结尾 `Ensure()` 拉起服务。
- 线程侧的闸门必须在**受理消息时同步**判定（`wake`（`agent.go:1642`）是事后的后台唤醒，返回不了错误）：
  - `internal/threads/orbs.go:104` 的 `Orbs` 接口加 `BoxStopped(ctx context.Context, orbID string) bool`；实现这个接口的测试替身各补一个返回 false 的方法——至少 `threadstest/harness.go` 的假 orbs 与 `fork_test.go:36` 的 `nopOrbs`，其余由编译器指出。
  - `agent.go` 加 `func (s *Service) boxStoppedLocked(ctx context.Context, orbID string) error`：`s.orbState(orbID) != orbStateReady && s.orbs.BoxStopped(ctx, orbID)` 时返回 `errf(CodeBoxStopped, "this box is stopped; start it from the Boxes page")`，否则 nil。只在 orb 不是 `ready` 时才查库，普通线程的每条命令不多一次读。
  - `command`（`:1158`）：在 `orbID := t.orbID` 与 `passiveCommand` 分支之后、建 `pendingCmd` 之前调用它，出错就 `t.mu.Unlock()` 并返回——消息不进队列。WS `cmd` 因此经 `c.fail`（`internal/realtime/hub.go:470`）回 `box_stopped`。
  - `commandWithAttachments`（`:1407`）：在它自己持有 `t.mu` 的那一段里、调 `startDeliveryLocked` 之前调用，出错就解锁返回。不要放进 `startDeliveryLocked` / `reserveMsgLocked`（`:1466-1477`）：那里没有 `ctx`，而且线程首条消息的投递也走它。
  - `wake`（`:1648`）：`errors.Is(err, orbs.ErrBoxStopped)` 与 runner 离线同样只记 debug（box 启动后 orb 回到 `ready`，由 orb 事件触发启动）。
  - `Wake`（`:1692`，`POST threads/{id}/wake`）：`ErrBoxStopped` → `errf(CodeBoxStopped, …)`。
  - `internal/threads/runner.go` 的常量块加 `CodeBoxStopped = "box_stopped"`、`CodeBoxUnsupported = "box_unsupported"`、`CodeOrbIsBox = "orb_is_box"`；`internal/api/threads.go:16` 的 `threadErrStatus` 三个都映射 409。
- `ErrBoxStopped` 的其余映射（照 `ErrRunnerOffline` 的落点各加一支）：`internal/api/server_orbs.go:33,54` → 409 `box_stopped`；`internal/coord/coord.go:394` 的 `orbErrCode` → `"box_stopped"`。
- `internal/api/orbs.go` 的 `PauseOrb`、`ResumeOrb`、`DeleteOrb`（`:74-124`）：取到的 orb `Box == 1` → `WriteError(w, 409, "orb_is_box", "this orb is a box; use /api/v1/boxes")`。`internal/coord/admintools.go:760` 的 `orbTarget` switch 加 `case o.Box == 1:` → `CodeInvalidTarget`（"orb %s is a box; use the box tools"）。
- `internal/threads/threads.go` 的 `Fork`（`:1004`）：现在只有 `copy` 分支读源 orb 行（`:1036-1042`），而 box 线程走的是 `forkShare`。把这次 `GetOrb` 提到分支之前（源线程有 orb 时读一次），两处用它：`env == copy` 且 `Box == 1` → `errf(CodeBoxUnsupported, "threads on a box cannot be forked with a copied environment; fork with env=share")`（与 runner 那条 `CodeRunnerUnsupported` 并列）；阶段 5 在 `forkShare` 末尾发通知时也要它（作为参数传进去）。共用 fork 的子线程行已经沿用源线程的 `Cwd`（`:~1037`），不用改。

测试：`internal/orbs/box_test.go`（包内现有夹具）——box orb 过了空闲阈值不冻结；`retired` 为 false；`DeleteUnused` 返回 false；闸门为 false 时 `Wake` 返回 `ErrBoxStopped`、`BoxStopped` 为 true；`Revive` 之后状态是 `waking` 且队列里恰有一个 spawn。`internal/threads` 加用例——替身的 `BoxStopped` 为 true 且 orb 是 `stopped` 时，`Command` 发 prompt 返回 `box_stopped` 且队列为空；带附件的入口同样；orb 是 `ready` 时照常受理。

**步骤 1.2 — `internal/boxes` 服务**

新包。没有等价物：`internal/orbs` 只管单台 orb 的状态机，不持有名字、期望状态、时段、app。

```go
type Actor struct{ Kind, ThreadID string }          // Kind ∈ owner|agent|assistant|schedule|system
type Error struct{ Code, Message string }           // errf(code, format, ...)
type Config struct {
	Base    portalhost.Base
	Box     *secrets.Box
	Changed func()                                  // → Hub.BoxesChanged
	DropApp func(app string)                        // 断开该 app 的匿名连接；nil = 没有 portal 数据面（步骤 4.1 接线）
	Now     func() time.Time                        // 默认 time.Now
	Tick    time.Duration                           // 默认 30s
	UnlinkedAfter time.Duration                     // ready 却没有 link 多久之后 Revive；默认 90s
}
func New(cfg Config, st *store.Store, o Orbs, th Threads) *Service
func (s *Service) Run(ctx context.Context)
func (s *Service) Touch()                                                            // 防抖后调 Changed（线程增删时由 threads 调）
func (s *Service) List(ctx context.Context) ([]apigen.Box, error)
func (s *Service) Get(ctx context.Context, ref string) (apigen.Box, error)            // ref = id 或名字
func (s *Service) Create(ctx context.Context, name, provider, typ string, by Actor) (apigen.Box, error)
func (s *Service) Rename(ctx context.Context, ref, name string, by Actor) (apigen.Box, error)
func (s *Service) SetSchedule(ctx context.Context, ref string, sch *apigen.BoxSchedule, by Actor) (apigen.Box, error) // nil = 清除
func (s *Service) Start(ctx context.Context, ref string, by Actor) (apigen.Box, error)
func (s *Service) Stop(ctx context.Context, ref string, by Actor) (apigen.Box, error)
func (s *Service) Delete(ctx context.Context, ref string, by Actor) error
func (s *Service) Activity(ctx context.Context, ref string, limit int) ([]apigen.BoxActivity, error) // ref "" = 全部
func (s *Service) Usage(ctx context.Context, ref string) (apigen.BoxUsage, error)
```

`Orbs` 接口由 `*orbs.Service` 满足，方法签名逐个照抄它现有的导出方法：`Create`、`Get`、`Delete`、`Wake`、`Pause`、`Revive`、`Connected`、`HasFeature`、`Do`、`DialPortal`、`Subscribe`、`SetBoxGate`。服务镜像不进接口：直接调包级函数 `orbs.ServicesView(ctx, s.st.Q, orbID, now)`（`internal/orbs/services.go:223`，返回服务与 portal 两个切片，只用前者）。`Threads` 接口只有 `Busy(ctx, orbID) (bool, error)`，由 `internal/server/adapters.go` 里的小适配器 `boxThreads{st}` 用 `CountWorkingThreadsByOrb` 实现。

行为：

- `Create`：校验名字 → `CountBoxes ≥ 16` → `box_limit` → 重名 → `box_exists` → `orbs.Create(orbs.CreateParams{Provider: provider, Type: typ, Box: true})`（错误包成 `invalid_request`，消息原样）→ `CreateBox{desired: running, desired_by: owner, desired_at: now}`；插入失败时 `orbs.Delete(orbID)` 再返回（这一步也失败的话由下面的孤儿清理兜底）。记活动 `created`。
- `Rename`：有未结束的部署（`ListOpenBoxDeploysByBox` 非空）→ `deploy_in_progress`；记活动 `renamed{from, to}`。地址不含 box 名，不受影响；app 进程里的 `ORYN_DEPLOYMENT_BOX` 在下一次部署或重启后才是新名字。
- `Start` / `Stop`：`SetBoxDesired(running|stopped, owner, now)`，记活动，立即触发一次对账。
- `SetSchedule`：`on != off`；`time.LoadLocation(tz)` 失败 → `invalid_request`。记活动 `schedule`。
- `Delete`（顺序不能反：box 行是按名字重试删除的唯一把手，而 box 的 orb 不受空闲与无主回收管）：
  1. 中止这台 box 上全部在途的部署：调内部钩子 `abortDeploys(boxID)`（本步骤里先是空实现，步骤 3.2 填上）。
  2. `orbs.Delete(orbID)`——orb 进入 `deleting`；它对已经在 `deleting` / `deleted` 的 orb 返回 nil（`service.go:1132`），所以可以重试。出错就原样返回，box 行还在。
  3. `DeleteBox`（级联）。挂在这台 orb 上的线程按现有「orb 已删除」的规则变成只读转录。
- box 视图：`provider` / `type` / `cpus` / `memoryMB` 取自 orb 行与其 provider；`state` 由 orb 状态映射：`requested|spawning|connecting|setting_up|resuming|waking → starting`，`ready → running`，`freezing|stopping → stopping`，`frozen|stopped → stopped`，`failed → failed`（带 `failureReason`）；`stoppedBy` 仅在 `desired = stopped` 时给 `desired_by`。orb 已经 `deleting` / `deleted` 的 box 不出现在 `List` 里，`Get` 回 `box_not_found`。
- 对账（`Run`：每 `Tick` 一次，另由 `orbs.Subscribe` 收到 box orb 的事件时立即触发；同一台 box 的对账串行）：
  0. 自愈：orb 已 `deleting` / `deleted` 的 box 行 → `DeleteBox`（`Delete` 在第 2、3 步之间中断过）；`ListOrphanBoxOrbs`（创建超过 1 分钟仍没有 box 行的 box orb）→ `orbs.Delete`。
  1. 时段：三列都非空时，在 `sched_tz` 下算出不晚于现在的最近一个边界（今天与昨天的 `on`、`off` 四个时刻里取最大者；`on` 边界 = `running`，`off` 边界 = `stopped`）。边界时刻 `> desired_at` → `SetBoxDesired(边界状态, schedule, 边界时刻)`，记活动 `started` / `stopped`（actor `schedule`）。手动启停因此只保持到下一个边界。
  2. `desired = running`：orb 是 `stopped` / `frozen` → `orbs.Wake`；`failed` → 同一台 box 两次重试之间至少隔 60 秒再 `Wake`；`ready` 且 `!orbs.Connected` 已持续 ≥ `UnlinkedAfter`（内存里记 `unlinkedSince`，控制面重启后从启动时刻起算）→ `orbs.Revive`。
  3. `desired = stopped`：orb 是 `ready` / `frozen` → `orbs.Pause(orbID, orbs.PauseStop)`。`desired_by = schedule` 时先等：`Threads.Busy` 为真或有未结束的部署，就推迟到边界之后 30 分钟。
  4. 闸门：`orbs.SetBoxGate(func(orbID) bool { 该 orb 的 box.desired == running })`（查不到 box → true）。在 `New` 里注册，不等 `Run`。
- `Changed`：任何写操作之后，以及收到 box orb 的任何事件（状态、服务、link）之后调用，250ms 防抖；`Touch()` 走同一个防抖。
- `Activity`：`box_activity` 行加上这台 orb 上的工作线程（`ListThreadsByOrbSince`，近 30 天，`kind = 'work'`），线程项 `kind = thread`、`actor = owner`、`ts = created_at`、`threadId`、`title`、`app`（cwd 以 `~/apps/<名>/` 开头时取名字）、`id = "t:" + 线程 id`；表行的 `id = "a:" + 自增号`。按 `ts` 倒序截到 `limit`。
- `Usage`：最近 21 个 UTC 自然日。`threads` = 当天在这台 orb 上创建的工作线程数；`upRatio` = 当天 orb 处于 `ready` 的时长占比，由 `GetOrbEventBefore`（窗口起点之前的最后一条，定初始状态）加 `ListOrbEventsSince` 累加，今天只算到现在；box 创建之前的日子两项都是 0。

接线（`internal/server/server.go`）：

- `s.Boxes` 在 `ssh.call` 的注册（`:219`）**之前**构造——阶段 3 要在同一处用 `s.Orbs.HandleRequest` 登记 `s.Boxes.OnCall`，方法值取的时候接收者不能是 nil，而登记必须早于接受 link。此时 `base`（`:148`）、`box`、`s.Store`、`s.Orbs` 都已就绪；`s.Hub`（`:253`）还没有，所以回调写成晚绑定的闭包：
  `s.Boxes = boxes.New(boxes.Config{Base: base, Box: box, Changed: func() { if s.Hub != nil { s.Hub.BoxesChanged() } }, Tick: boxesTick, UnlinkedAfter: boxesUnlinkedAfter}, s.Store, s.Orbs, boxThreads{s.Store})`。
- 文件顶部的测试可调变量区（`:56-75`，`notifyDoneDelay` 旁）加 `var boxesTick, boxesUnlinkedAfter time.Duration`（零值 = boxes 的默认；e2e 在 `newStack` 之前设、`t.Cleanup` 里还原）。
- `go s.Boxes.Run(runCtx)` 放在 `s.Hub` 构造之后、其它 `Run` 旁边；`s.API.Boxes = &api.BoxesDeps{Service: s.Boxes}`（`:477` 旁）。
- `threads.Config`（`:199`）加 `BoxThreadsChanged func()`，这里给 `func() { if s.Boxes != nil { s.Boxes.Touch() } }`；threads 一侧的调用点在阶段 5。

`internal/realtime/public.go:395-410` 旁加 `func (h *Hub) BoxesChanged() { h.toAll(apigen.WsServerBoxesChanged{Type: apigen.WsServerBoxesChangedTypeBoxesChanged}) }`。
`internal/api/area.go` 加 `Boxes *BoxesDeps`；新建 `internal/api/boxes.go`：`BoxesDeps{Service *boxes.Service}`、`boxesSvc(w)`（nil → 501，照 `sshSvc`）、`writeBoxErr`（`*boxes.Error` 按上面的错误码表映射，其余 500）与本阶段的 10 个处理器（`listBoxes` … `getBoxUsage`），actor 一律 `owner`。

测试（`internal/boxes/boxes_test.go`，注入 `Now` 与假 `Orbs`）：时段边界表驱动（`05:00–23:00`、跨夜 `22:00–06:00`、夏令时切换日的 `Europe/Berlin`）；手动停机保持到下一个 `on` 边界；`ready` 无 link 第 89 秒不 `Revive`、第 90 秒 `Revive`；时段停机在 `Busy` 时推迟、30 分钟后照停；`Usage` 的占比；`Delete` 在 `orbs.Delete` 出错时 box 行仍在、再调一次成功；orb 已 `deleted` 的 box 行被对账删掉；孤儿 box orb 被对账删掉。

---

### 阶段 2 — orbd：app 运行时

改 `internal/orbdrt`，以及控制面镜像的上限（`internal/orbs/services.go`）。runner 与协调宿主不提供本阶段的任何东西。

**步骤 2.1 — 监督器的第三种来源**

锁的次序固定为：box 管理器的 `mu`（串行化 app 操作）→ 监督器的 `opMu` → 监督器的 `mu`。监督器回调管理器时只读管理器**发布的快照**，从不取管理器的锁（`reload` 是在 `s.mu` 之下用到它们的，取锁就是自己等自己）。

- `serviceSpec`（`servicefile.go:45`）加 `App, Dir string`（`Dir` 是绝对基目录）；`sameDefinition` 把这两项也比较进去。
- `serviceSupervisor` 加两个字段，都由管理器在每次状态变化后整体替换：`apps atomic.Pointer[[]fileEntry]`（全部 live app 的服务定义）与 `appPorts atomic.Pointer[map[int]bool]`（全部 app 的端口，含还没有 live 的）。nil = 没有 app。
- `reload`（`services.go:543`）：
  - 组 `desired` 时丢掉名字含 `--` 的文件 / 动态条目：文件里的打一行警告（"service name %q contains -- (reserved for box apps); ignored"），`st.Dynamic` 里遗留的直接删掉并置 `stateDirty`。只标成无效是不够的——同名的 app 条目随后追加进来，`s.order` 里会出现两次。
  - `checkPublicURLRefs(desired)`（`:588`）之后追加 `desired = append(desired, *s.apps.Load()...)`（nil 跳过）。app 条目因此不过 `maxServices` 的截断，也不过 `assignPortsLocked`（同一 app 的服务共用一个显式端口，过它会被判 `port_conflict`）。
- `assignPortsLocked`：把 `appPorts` 里的端口当作已占用——文件 / 动态服务显式写了它 → `port_conflict`，自动分配跳过它。
- `Exposed`（`:393`）：开头加一条——`port` 在 `appPorts` 里就返回 true（app 服务没有 `Links`，不出现在线程的 Portal 列表里）。
- `runOnce`（`:1052`）：`base := s.r.currentWorkdir(); if spec.Dir != "" { base = spec.Dir }; cmd.Dir = filepath.Join(base, spec.Cwd)`。
- `envLocked`（`:1253`）：`spec.App != ""` 时只返回 `baseEnv()` + `spec.Env` + `PORT`，不并入 `setupEnvSnapshot()`（全局 env 模式密钥不进 app），不设 `PUBLIC_URL` / `ORYN_THREAD_ID`，`thread`、`url` 为空。
- `snapshotLocked`（`:299`）：`spec.App != ""` 时填 `OrbService.App`。
- 现有入口对 app 服务的处理：
  - `Start`（`:750`）：`p.Command != nil` 且名字含 `--` → `CodeInvalidService`（"names containing -- are reserved for box apps"）。`:781` 的上限判断从 `len(s.svcs)` 改成只数 `spec.App == ""` 的条目（app 服务不占普通服务的 32 个名额）。`p.Command == nil` 的分支（启动已声明的服务）对 app 服务照常可用。
  - `Stop`（`:818`）：`remove` 且 `sv.spec.App != ""` → `CodeInvalidService`（"%q belongs to box app %s; undeploy the app instead"），与文件服务那条并列。不带 `remove` 的停止照常（记入 `stopped`）。
  - `Restart` 不用改。
- 新方法 `switchApp(app string, fn func() error) error`——换 release、回滚、重启、下线都只经它，整个过程是**一次**监督器操作（自己取 `opMu`，调用方不得持有；`fn` 里不得再调任何会取 `opMu` 的方法）：
  1. 把 `spec.App == app` 的全部 run 摘下来（`sv.run = nil`）并 `stopRuns` 等它们结束；
  2. 调 `fn()`——管理器在里面换链接、改状态、落盘、`publish()`；`fn` 出错就原样返回（服务保持停止，由调用方决定怎么恢复）；
  3. 把该 app 的服务名移出 `st.Stopped`，`s.reload(false)`（已经不在快照里的 app 条目在这里从 `s.svcs` / `s.order` 里消失，镜像随之更新）；
  4. 对每个 `spec.App == app`、定义有效、`sv.run == nil` 的服务调 `s.launch(sv)`。

  不能拆成「停」「起」两个各自取锁的方法：两次取锁之间 `Ensure`（服务接口与 resume 结尾都会调，`:741`）可以插进来，按旧快照把刚停掉的服务又拉起来，随后的「起」看到 run 还在就跳过，新 release 永远起不来。第 4 步必须显式 launch：`Dir` 固定是 `…/current`，换 release 之后定义没变，`reload(false)` 走的是 `default` 分支（`:614-618`），不会拉起第 1 步停掉的服务。

控制面一侧（`internal/orbs/services.go`）：`maxOrbServices` 改为 96，并在 `onServicesState` 的循环里另计数——`source != app` 的至多 32 个、`source = app` 的至多 64 个，超出的丢弃；`sanitizeService` 对 `App` 用 `portalhost.ValidAppName` 校验，不合规置空。

**步骤 2.2 — box 管理器与本机 API**

新建 `internal/orbdrt/boxapps.go`：

```go
type boxApp struct {
	Port     int                    `json:"port"`
	Live     string                 `json:"live"`
	Releases []boxRelease           `json:"releases"` // 新到旧，含 live
	Env      map[string]string      `json:"env"`
	Trial    *boxTrial              `json:"trial,omitempty"` // 非空 = 正在试运行一个还没确认就绪的 release
}
type boxTrial struct {
	Release string  `json:"release"`
	Prev    *boxApp `json:"prev"` // 试运行之前的整个 app（nil = 之前没有这个 app）；其中的 Trial 恒为 nil
}
type boxRelease struct {
	ID       string                 `json:"id"`
	Source   bool                   `json:"source"`
	Services []apigen.OrbBoxService `json:"services"`
}
type boxManager struct{ /* r *Runtime; mu sync.Mutex（串行化全部操作）; apps map[string]*boxApp; path string */ }
```

- 状态文件 `~/.oryn/box/apps.json`（`writeFileAtomic`，0600），`{"version": 1, "apps": {...}}`。`Runtime` 初始化时（非 runner、非协调宿主）加载它；**带 `Trial` 的 app 说明 orbd 在试运行中途退出了，结果没有确认过**——把它退回 `Trial.Prev`：做 activate 第 9 步里 `fn` 的那些事（链接、状态、落盘）与同一条删除规则，但**不经 `switchApp`、不起服务**（此刻还没有进程在跑，setup 参数也还没到；服务由 resume 结尾的 `Ensure()` 统一拉起），最后 `publish()`。`Live` 因此只有两种含义：没有 `Trial` 时是确认过就绪的 release，有 `Trial` 时是待确认的。
- `publish()`（每次改完内存状态后调）：算出新的服务定义切片与端口集合，存进监督器的两个 `atomic.Pointer`。服务定义：对每个有 `Live` 的 app、其 live release 的每个服务，`fileEntry{spec: serviceSpec{Name: app+"--"+svc, App: app, Source: apigen.OrbServiceSourceApp, Command, Cwd, Dir: <home>/apps/<app>/current, Port: app.Port, PortExplicit: true, Env: <展开后的 Env>, Links: []}}`；`Env` 里的 `ORYN_DEPLOYMENT_DATA_DIRECTORY` 在这里用 `resolvePath` 从字面量 `~/apps/<app>/data` 展开成绝对路径。
- 对外状态快照：管理器另有 `status atomic.Pointer[apigen.OrbBoxAppsState]`，`GET /box/apps` 只读它、不取 `mu`——activate 持有 `mu` 直到等就绪结束（一分钟以上），读状态不能排在它后面。它在三个时机更新：每次 `publish()`；activate / rollback 处理器**一进来**（取到 `mu`、校验完参数之后，做任何事之前）把该 app 的 `activating` 置为请求里的 release id（app 还不存在就先放一条 `live: ""` 的占位）；处理器返回前按最终状态重算。从控制面看，一次激活从头到尾都带着 `activating`，解包的那几十秒不会被误判成「没有发生」。
- hello 特性：在 `runtime.go:834` 附近的特性表里加 `"box"`；`internal/orbs/orbhttp.go:32` 的常量块加 `FeatureBox = "box"`。
- `localHandler`（`localapi.go:48`）注册下面的路由，并把 `/box/` 加进 runner 的 404 前缀判断（`:81`）。请求体带 `Env` 的两条（activate、restart）不用 `readJSON`（上限 1 MiB，`:175`），另写 `readBoxJSON`，上限 8 MiB（1 MiB 的密钥经 JSON 转义最坏约 6 倍）。

| 路由 | 请求 → 响应 | 行为 |
|---|---|---|
| `PUT /box/incoming/{releaseId}?size=<字节数>&sha256=<hex>` | 原始体 → 204 | `releaseId` 不合正则 → 400。先删掉 `incoming/` 里修改时间超过 1 小时的遗留包（传上来了却没等到激活的）。体过 `http.MaxBytesReader`（512 MiB，超出 → 413 `too_large`），用 `r.receiveFile(body, "box-", sha256Hasher)`（`localapi.go:300`）收进 `$HOME/.oryn/tmp`；字节数 ≠ `size` → 400 `size_mismatch`，摘要 ≠ `sha256` → 422 `checksum_mismatch`（都删掉临时文件）；通过后 `os.MkdirAll(incoming, 0o700)` 再 `commitFile(tmp, incoming/<releaseId>.tar.gz, false)` |
| `GET /box/apps` | → 200 `OrbBoxAppsState` | 返回上面的状态快照：每个 app 的端口、保留着的 release id、`live`（最近一次确认就绪的；试运行期间报 `Trial.Prev` 的，没有前任则为空串）与 `activating`（正在处理的 activate / rollback 的 release id，含解包阶段） |
| `POST /box/apps/{app}/activate` | `OrbBoxActivateRequest` → 200 `OrbBoxResult` | 见下 |
| `POST /box/apps/{app}/rollback` | `OrbBoxRollbackRequest` → 200 `OrbBoxResult` | `releaseId` 须在 `Releases` 里。已经是 live 且没有 `Trial` → 直接 `ok: true`（上一次的应答丢了、控制面重试）。否则与 activate 的第 5–9 步是同一套流程（记 `Trial`、`switchApp` 里换链接、等就绪、清 `Trial`；不就绪则再用一次 `switchApp` 换回去），只是不解包、不裁剪；目标本来就在 `Trial.Prev.Releases` 里，所以失败收尾与启动清理都不会删它的目录 |
| `POST /box/apps/{app}/restart` | `OrbBoxRestartRequest` → 200 `OrbBoxResult` | `switchApp(app, 换 Env、落盘、publish)`，等就绪；不回滚（不就绪回 `ok: false` + `not_listening`，新 `Env` 保留） |
| `DELETE /box/apps/{app}?purge=0\|1` | → 204 | `switchApp(app, 从状态里移除、落盘、publish)`——它的 reload 把这个 app 的条目从监督器里清掉；然后删 `releases/` 与 `current`（`purge=1` 连 `data/`）。app 不存在也回 204 |

  业务失败一律 200 + `ok: false` + `code` / `message`（`log` 是该 app 各服务日志末尾合计至多 16 KiB）；参数不合规才用 4xx。

- **activate 的步骤**。开头先做两件事：`releaseId == app.Live` 且没有 `Trial` → 直接回 `ok: true`（控制面重放同一次激活是无害的）；给 app 的现状做一份深拷贝 `prev`（没有这个 app 时为 nil）。第 1–4 步失败只需清掉本次建的目录与包；从第 5 步起**不再理会请求的取消**（对端断开也把这次切换做完或退回去），失败走第 9 步。
  1. 打开包：`os.OpenRoot(~/.oryn/box/incoming)` 之后 `root.Open("<releaseId>.tar.gz")`（文件名由校验过的 `releaseId` 拼出，请求里没有路径）。不存在 → `archive_missing`。
  2. `source` 为真、已有 live、`discardBoxChanges` 不为真时，在 `~/apps/<app>/current` 里跑 `git status --porcelain`：非空 → `box_changes`，`message` 列出前 20 行并附一句 `commit and push them from a thread on the box, or redeploy with --discard-box-changes`；记下 `liveHead = git rev-parse HEAD`。
  3. 解包。先 `os.RemoveAll` 再建 `releases/<releaseId>.tmp`，`os.OpenRoot` 打开它，**全部写入都经这个 `*os.Root`**（它拒绝解析到根之外，包括经由包里先放进来的符号链接）：
     - 条目名先规范化：去掉前导 `./`（`tar -C <dir> .` 打出来的就是这种）与目录条目结尾的 `/`，`.` 本身跳过；之后须非空、非绝对、`path.Clean(name) == name`、不是 `..` 也不以 `../` 开头——否则 `bad_archive`。写法照 `extractProfileArchive`（`desktopprofile.go:377-384`，它同样先去尾部的 `/`）。
     - 目录 → `root.MkdirAll(name, 0o755)`；普通文件 → 先 `root.MkdirAll(path.Dir(name), 0o755)`（包里不一定有它父目录的条目），再 `root.OpenFile(name, O_CREATE|O_EXCL|O_WRONLY, os.FileMode(hdr.Mode&0o777))`（`hdr.Mode` 是 `int64`）并 `io.CopyN`；符号链接 → `root.Symlink(hdr.Linkname, name)`（目标原样保留、不校验：它只在 app 运行时被跟随，而 app 本来就以同一个用户运行；`venv/bin/python → /usr/bin/python3` 这类绝对目标是正常的）；硬链接 → `root.Link(hdr.Linkname, name)`（两端都在根内解析，指不到根外）。
     - PAX 全局头跳过；设备、FIFO 等其余类型 → `bad_archive`。
     - 累计字节 > 4 GiB 或条目 > 500000 → `too_large`。
     - 完成后 rename 成 `releases/<releaseId>`，删掉包。
  4. 第 2 步记了 `liveHead` 时：`git -C <新 release> merge-base --is-ancestor <liveHead> HEAD` 非零退出 → `box_changes`（"commits made on the box are not in the deployed branch: <liveHead 前 12 位>"）。
  5. `mkdir -p ~/apps/<app>/data`。在内存状态里写下新值（`Port`、`Env`、`Live = releaseId`、新 release 插到 `Releases` 头部）与 `Trial{Release: releaseId, Prev: prev}`。
  6. `sup.switchApp(app, fn)`，`fn` 里依次：**先落盘**（磁盘上的状态带着 `Trial`）；再在 `current` 所在的目录里建一个临时符号链接、`os.Rename` 成 `current`；最后 `publish()`。次序不能反：先换链接后落盘的话，两者之间崩溃，重启后状态里没有 `Trial`、`current` 却已经指向没确认过的 release，`Ensure()` 会把它当成 live 拉起来。
  7. 等就绪：每 250ms 试连 `127.0.0.1:<port>`、再 `[::1]:<port>`，成功即通过；该 app 的服务全部进入 `failed`，或超过 `readyTimeoutSec` → `not_listening`。
  8. 成功：清掉 `Trial` 并落盘——**到这一步 `GET /box/apps` 才把它报成 `live`**。然后 `Releases` 超过 `keep` 的从最旧删起（不删 live），目录一并删除，再落盘，返回 `pruned`。
  9. 失败收尾（第 6、7 步出错）：再调一次 `sup.switchApp(app, fn)`，`fn` 里：`prev != nil` → 把 `current` 链回 `prev.Live`，用 `prev` **整体**替换内存里的 app（端口、`Env`、`Live`、`Releases` 一起回去，`Trial` 清空）；`prev == nil` → 从状态里移除这个 app、删 `current`；落盘；`publish()`。之后的删除规则：试运行的 release **不在** `prev.Releases` 里（它是这次激活新解出来的）才删 `releases/<releaseId>`，连同残留的 `.tmp` 与包；`prev == nil` 且 `data/` 为空就连 `~/apps/<app>` 一起删。回 `ok: false`。（启动时清理遗留 `Trial` 做的是同一个 `fn` 与同一条删除规则，只是不经 `switchApp`，见上面状态文件那一条。）

测试（`internal/orbdrt/boxapps_test.go`，用现有 `harness_test.go` 的假控制面）：

- 上传：摘要不符 422、字节数不符 400、超限 413，三种情况 `incoming/` 里都没有文件；通用的 `PUT /fs/file?path=~/.oryn/box/incoming/x` 仍是 403 `protected_path`（`TestLocalFSPathPolicy` 不改）。
- 打包一个监听 `$PORT` 的小脚本，activate 后端口可连、`current` 指向新 release、`services.state` 里出现 `source = app`。
- 第二个 release 不监听 → `ok: false`、`current` 仍是第一个、**它的服务重新在跑**、进程环境还是第一个 release 当时的 `Env`。
- 第二个 release 的命令与环境和第一个**完全相同**、只是文件内容不同 → 激活后服务是新进程（PID 变了）、读到的是新内容。
- 回滚到上一个 release（两者命令与环境相同）→ 服务是新进程、读到的是旧内容；回滚目标不监听 → `ok: false`，原来的 live 重新在跑。
- 试运行期间（`switchApp` 返回之后、就绪之前）另一个 goroutine 调 `Ensure()` → 最终在跑的是新 release 的进程，没有旧进程残留。
- 试运行进行中 `GET /box/apps` 报的 `live` 是旧 release、`activating` 是新 release；确认就绪后 `live` 才变。解包还没结束时（用测试钩子卡住解包）`GET /box/apps` 也立刻返回，`activating` 已经是新 release。
- 「orbd 中途退出」：手写一份带 `Trial` 的 `apps.json` 再初始化 `Runtime` → app 退回 `Trial.Prev`，`GET /box/apps` 的 `live` 是旧 release、没有 `activating`；`Prev` 为 nil 时 app 不存在。两种变体都要过：`current` 已经指向试运行的 release；`current` 还指向旧 release（落盘之后、换链接之前退出）。
- 回滚的 `Trial`（目标是已有的 kept release）被启动清理之后，目标 release 的目录还在；对已经是 live 的 release 再发一次 rollback 直接 `ok: true`。
- 下线之后 `List()` 与 `services.state` 里立刻没有这个 app 的服务。
- 目录条目名带结尾 `/`、文件条目的父目录没有单独的目录条目，都能解出来。
- 首次激活就失败 → 状态里没有这个 app、没有 `current`、`GET /box/apps` 不含它。
- 同一个 `releaseId` 激活两次，第二次直接 `ok: true`。
- 连发 4 个 release 后目录只剩 3 个。
- 包里先放符号链接 `evil -> /tmp`，再放文件 `evil/x` → `bad_archive`，`/tmp/x` 不存在；含 `../x` 的条目被拒；指向包外的符号链接本身被原样建出。
- 同一 app 两个服务共用端口都进入 `running`；进程环境里有 `PORT` 与给定的 `Env`、没有 `setupEnv` 里的变量。
- 文件里声明了名为 `a--web` 的服务、同时有 app `a` 的服务 `web` → 列表里只有一个 `a--web`，是 app 的。
- 普通服务已有 32 个时还能激活 app；app 服务 `stop --remove` 被拒。
- 带 `.git` 的 release 在 `current` 里改一个文件后再 activate → `box_changes`，带 `discardBoxChanges` 通过。

**步骤 2.3 — exec 与线程工作目录**

- `POST /box/exec`（`OrbBoxExecRequest`）：`bash -lc <command>`，`cmd.Dir = resolvePath(cwd)`（须经 `safePath` 落在 `$HOME` 之下，或等于 `$HOME` 本身），环境 = `loginEnv(ctx, dir, baseEnv())`，独立进程组。响应 200 `application/x-ndjson`，每行一个对象并立即 flush：`{"s":"o"|"e","d":"<文本块>"}`（每块至多 32 KiB），最后一行 `{"exit": <退出码>}`；超时杀进程组并回 `{"exit": 124}`；请求 ctx 结束（对端断开）杀进程组。
- `scope`（`runner.go:330`）的非 runner 分支：请求带 `X-Oryn-Workdir` 时，`abs := r.resolvePath(头的值)`、`real, err := evalExisting(abs)`，`err == nil`、`real` 是目录且 `within(real, home)` 就把 `workdir` 换成 `abs`（词法路径，保留 `current` 这一段——文件面板的根不随 release 目录变），否则保持 `currentWorkdir()`；`root` 不变（仍是真实 `$HOME`）。控制面已经会为有 `cwd` 的线程带这个头（`internal/api/orbproxy.go:149-171`），Web 的 orb 文件源只要有线程 id 就带 `thread` 参数（`web/src/lib/file-source.ts:355`），box 线程的文件与改动面板因此落在 app 目录。现在没有任何非 runner 线程带 `cwd`，普通 orb 的行为不变。
- `GET /fs/roots?strict=1`（只给控制面的 artifact 文件能力用，见阶段 5）：请求带了 `X-Oryn-Workdir` 而上一条没有采用它（目录不存在、不是目录、不在 `$HOME` 之下）→ 404 `workdir_missing`，不退回缺省工作目录。不带 `strict` 时行为同上一条——文件面板在 app 下线后退回缺省目录可以接受，artifact 的读取范围不能因此变宽。实现上把上一条的判断提成 `r.headerWorkdir(req) (abs string, given, ok bool)`，`scope` 与 `handleFsRoots` 共用。

测试：exec 的流式行、退出码、超时 124、`cwd` 逃出 `$HOME` 回 400；`/fs/roots` 带 `X-Oryn-Workdir: ~/apps/x/current` 时 `workdir` 以 `/apps/x/current` 结尾、`home` 不变；带 `/etc` 或不存在的目录时 `workdir` 不变；不带头时不变；`strict=1` 且头里的目录不存在 → 404 `workdir_missing`，目录存在 → 与不带 `strict` 相同。

---

### 阶段 3 — 部署管线

**步骤 3.1 — `internal/boxcfg`（纯函数，CLI 与测试共用）**

```go
type Service struct{ Name, Command, Cwd string }
type Config struct {
	Build     string
	Source    bool          // 缺省 true
	Artifacts []string
	Include   []string
	Services  []Service     // 按文件顺序
}
// Find 从 dir 向上到 repoRoot，返回最近的含 .oryn/box.yaml（回退 .amp/box.yaml）的目录相对 repoRoot 的路径（"" = 根）。
func Find(repoRoot, dir string) (root, file string, err error)
func Parse(data []byte) (Config, []string /* warnings */, error)
type Target struct {
	On      string `json:"on"`
	Name    string `json:"name"`
	Root    string `json:"root,omitempty"`
	Private bool   `json:"private,omitempty"`
}
type Targets struct{ Targets map[string]Target `json:"targets"` }
func LoadTargets(repoRoot string) (Targets, error)            // .oryn/box-deploy.json；不存在 = 空
func SaveTargets(repoRoot string, t Targets) error            // 缩进用制表符，键排序，末尾换行
func TargetKey(root string) string                            // root 的末段；"" → "default"
func NormalizeRepo(originURL string) string                   // 小写主机 + 路径，去 .git、去凭据、scp 形式转成 host/path
```

`Parse` 的校验：`services` 必须是 1–4 项的映射，名字合规，`command` 非空；`cwd`、`artifacts[]`、`include[]` 都是相对路径，`artifacts` 与 `cwd` 不得含 `..`，`include` 清洗后不得逃出仓库根（由调用方用 `root` 复核）；`source: false` 时 `artifacts` 非空，且每个服务的 `cwd`（空 = `.`）须等于某个 artifact 或在其之下（所以 `source: false` 的 app 要把可执行文件放进一个声明为 artifact 的目录，再把服务的 `cwd` 指到那里）；`source: true` 时给了 `include` 以外的 `artifacts` 忽略并出警告；未知键出警告不报错。

测试：Amp 文档里的三份示例（单 app、`source: false`、`include: ["../packages/ui"]`）都能解析；各条校验的反例（含 `source: false` 且服务 `cwd` 缺省、artifact 都是文件的情况被拒）；`TargetKey`；`NormalizeRepo("git@github.com:Acme/Wiki.git") == "github.com/acme/wiki"`。

**步骤 3.2 — boxes 服务：app、部署、密钥、exec**

`internal/boxes` 新增（`deploy.go`、`apps.go`、`call.go`）：

```go
type PrepareParams struct {
	Box, App                   string
	Create                     bool
	Provider, Type             string
	Repo, Root, Branch, Commit string
	Source, Move               bool
	Public                     *bool   // nil = 新 app 取 true，已有 app 不变
}
type PrepareResult struct {
	DeployID, ReleaseID, Box, App, URL, ArchivePath string
	MaxBytes int64
	Env      map[string]string          // 构建可见的三项 ORYN_DEPLOYMENT_URL/NAME/BOX
}
type CommitParams struct {
	DeployID, SHA256  string
	SizeBytes         int64
	Services          []apigen.OrbBoxService   // cwd 已换算成相对 release 根
	DiscardBoxChanges bool
}
type CommitResult struct {
	OK                         bool
	URL, ReleaseID, Code, Message, Log string
}
func (s *Service) PrepareDeploy(ctx context.Context, srcOrbID string, p PrepareParams, by Actor) (PrepareResult, error)
func (s *Service) CommitDeploy(ctx context.Context, srcOrbID string, p CommitParams) (CommitResult, error)
func (s *Service) CancelDeploy(ctx context.Context, ref, app string) error
func (s *Service) SetPublic(ctx context.Context, ref, app string, public bool, by Actor) (apigen.BoxApp, error)
func (s *Service) Undeploy(ctx context.Context, ref, app string, purge bool, by Actor) error
func (s *Service) Restart(ctx context.Context, ref, app string, by Actor) (apigen.BoxApp, error)
func (s *Service) Rollback(ctx context.Context, ref, app, releaseID string, by Actor) (apigen.BoxApp, error) // releaseID "" = 上一个
func (s *Service) Releases(ctx context.Context, ref, app string) ([]apigen.BoxRelease, error)
func (s *Service) Secrets(ctx context.Context, ref, app string) ([]apigen.BoxSecret, error)
func (s *Service) PutSecret(ctx context.Context, ref, app, name, value string, secret bool) error
func (s *Service) DeleteSecret(ctx context.Context, ref, app, name string) error
func (s *Service) Exec(ctx context.Context, ref string, req apigen.BoxExecRequest, w io.Writer) error       // 把 orbd 的 ndjson 原样写给 w
func (s *Service) Copy(ctx context.Context, srcOrbID, ref, app, boxPath, orbPath string, toBox bool) (int64, error)
func (s *Service) OnCall(ctx context.Context, orbID string, params json.RawMessage) (any, error)             // box.call
```

公共部分：

- `usable(box)`——调 box 的 orbd 之前统一过它，判定顺序固定：`desired = stopped` → `box_stopped`；orb 不是 `ready` 或 `!orbs.Connected` → `box_not_ready`；`!orbs.HasFeature(orbID, orbs.FeatureBox)` → `box_unsupported`（"this box runs an orbd without box support"）。请求用 `orbs.Do`，JSON 往返照 `internal/attachments/attachments.go:489` 的 `orbJSON` 写一个本包的 `orbJSON`（响应体读到 1 MiB）。
- app 视图：`url = Base.AppURL(name)` 去掉结尾 `/`（未启用 portal 时省略）；`live` 取 `live_release_id` 的 release（NULL 时省略）；`deploying` = 有未结束的部署；`services` = 镜像里 `source = app && app = 名字` 的项；`preview` 取列值。`Box.lastDeployAt` = 各 app live release 的 `activated_at` 最大值。
- 部署的两个收尾函数，**所有**结束路径都只经它们，都在调用方的 `Store.Tx` 里跑。开这些事务用的 ctx 一律是 `context.WithoutCancel(ctx)` 再加 15 秒超时——收尾往往正是因为原 ctx 被取消了才发生的，用原 ctx 开事务会直接失败，行就卡在未结束状态。
  - `finishDeployTx(q, row, state, errText) (bool, error)`（`state ∈ failed|canceled`）：`FinishBoxDeploy` 影响 0 行 → 别人已经收尾，返回 false；否则 `row.first` 时 `DeleteProvisionalBoxApp`（首次部署没成，名字与端口让出来）；`state = failed` 时记活动 `deploy_failed{error, commit, threadId}`（actor 取行上的）。
  - `completeDeployTx(q, row, pruned []string) (bool, error)`：`FinishBoxDeploy(succeeded)` 影响 0 行 → 返回 false；否则 `CreateBoxRelease{id: row.release_id, state: live, repo, root, source, size_bytes, sha256, services, commit, branch, thread_id}`、原 live → `kept`、`pruned` 里的 id → `pruned`、`SetBoxAppLive{release, repo: row.repo, root: row.root, source: row.source}`；`row.set_public` 非 NULL 时 `SetBoxAppPublic` 并记 `visibility`；记活动 `deployed{commit, releaseId, threadId}`。事务提交后：公开性变成私有的调 `cfg.DropApp(app)`（非 nil 时）；`Changed()`。
- `resolveActivating(ctx, row)`——一行停在 `activating` 而结果未知时用（控制面重启、调用 orbd 时连接断了）：`usable(box)` 不过 → 不动，下一轮对账再试；否则 `GET /box/apps`，看该 app：`activating == row.release_id` → orbd 还在试运行，不动，下一轮再看；`live == row.release_id` → `completeDeployTx`（`pruned` = 控制面里该 app 的 `live` / `kept` release 中 orbd 的 `releases` 已经没有的那些）；其余（含 app 不存在）→ `finishDeployTx(failed, "activation did not complete")`。orbd 只在确认就绪之后才把 release 报成 `live`（步骤 2.2 的 `Trial`），所以这里不会把一个没验证过的 release 记成成功。
- 内存表 `inflight map[deployID]*flight`，`flight{cancelTransfer, abort context.CancelFunc}`，有自己的互斥锁。`CommitDeploy` 在 `ClaimBoxDeploy` **之前**就把自己登记进去（认领失败再摘掉），返回时摘掉——先认领后登记的话，对账恰好在两者之间扫到这一行，会把一次正常的部署当成孤儿判失败。`Run` 启动时先把 `transferring` 的行全部 `finishDeployTx(failed, "control plane restarted")`，对每个 `activating` 的行调 `resolveActivating`；此后每个 `Tick` 对不在 `inflight` 里的行再过一遍：`transferring` → `finishDeployTx(failed, "interrupted")`，`activating` → `resolveActivating`。`prepared` 的行不动（orbd 会重连，CLI 的 commit 仍然有效），只受 60 分钟过期约束。

`PrepareDeploy`：

1. 按名字找 box；找不到且 `Create` → `s.Create(ctx, p.Box, p.Provider, p.Type, by)`；找不到且不 `Create` → `box_not_found`。
2. `desired = stopped` → `box_stopped`。之后每 500ms 看一次，至多 10 分钟（新建的 box 要等镜像拉起）：orb `ready`、有 link、有 `box` 特性 → 继续；有 link 却没有 `box` 特性 → **立即** `box_unsupported`（不等到超时）；orb `failed` → 立即 `box_not_ready`（带失败原因）；超时 → `box_not_ready`。
3. 一个 `Store.Tx` 里做完下面全部：
   - `ListExpiredBoxDeploys`（60 分钟前的 `prepared`）逐个 `finishDeployTx(canceled, "expired")`。
   - `GetOpenBoxDeploy` 有行 → `deploy_in_progress`（消息附 "run `oryn boxes cancel-deploy <box> --app <app>` if it is stuck"）。
   - `GetBoxAppByName`：在别的 box 上 → `app_exists_elsewhere`；在本 box 上且 `repo` 或 `root` 与入参不同、没带 `Move` → `app_repo_mismatch`（消息写出已绑定的 `repo` 与 `root`，并提示 `--move`）；带了 `Move` 就放行——**这里不改 app 行**，新的 `repo` / `root` 记在部署行上，激活成功才生效，所以一次失败的 `--move` 不会让 box 上的线程落到不存在的目录。不存在：校验名字，`CountBoxApps ≥ 16` → `app_limit`，取端口，`CreateBoxApp{public: p.Public 或 true, repo, root, source, live_release_id: NULL}`，`first = 1`。
   - `CreateBoxDeploy{state: prepared, release_id: ids.New("bxr"), actor: by.Kind, thread_id, src_orb_id, repo, root, source, set_public, commit_sha, branch, first}`；`set_public` 只在 app 原本就存在、`p.Public` 非 nil 且与现值不同时给值。唯一索引冲突（并发的另一个 prepare 先插入了）同样回 `deploy_in_progress`。
4. 返回 `ArchivePath = "~/.cache/oryn/box-releases/<deployId>.tar.gz"`、`MaxBytes = 512 << 20`。

`CommitDeploy`：

1. `SizeBytes > 512 MiB` → `release_too_large`（行仍是 `prepared`，CLI 随后会取消它）。从服务自己的根 ctx（不是调用方的 ctx）派生 `abortCtx`，再从它派生 `xferCtx`（30 分钟），把两者的取消函数登记进 `inflight`——`CancelDeploy` 只取消 `xferCtx`，删 box 与进程退出取消 `abortCtx`。然后 `ClaimBoxDeploy(id, srcOrbID, size, sha256, services)`：影响 0 行 → 摘掉登记，`deploy_not_found`（不存在、不是这台 orb 准备的、或已被别的 commit 认领）。
2. 传输（`xferCtx`）：`usable(box)`；向源 orb 发 `GET /fs/file?path=<ArchivePath>`，非 200 → `archive_missing`；`resp.ContentLength ≥ 0` 且不等于 `SizeBytes` → `size_mismatch`；把响应体作为 `PUT /box/incoming/<releaseId>?size=<SizeBytes>&sha256=<SHA256>` 的请求体发给 box orb（`req.ContentLength = SizeBytes`）。非 204 → 取 orbd 的错误码（`checksum_mismatch` / `size_mismatch` / `too_large`），取不到用 `transfer_failed`。本步任何失败 → `finishDeployTx(failed)`，返回 `OK: false` 与错误码；`finishDeployTx` 返回 false（行已经被 `CancelDeploy` 置成 `canceled`）时返回 `Code: "canceled"`。
3. `SetBoxDeployActivating` 影响 0 行（传输刚完成就被取消了）→ 返回 `OK: false, Code: "canceled"`。取消与进入激活的先后由这两条带条件的 `UPDATE` 在库里分出来，不靠内存里的时序。
4. 激活（`abortCtx` 加 10 分钟超时；`CancelDeploy` 影响不到它）：组 `Env`——`ListBoxSecrets` 逐个 `Box.Open`，加 `ORYN_DEPLOYMENT_URL`（app 地址，未启用 portal 时为空串）、`ORYN_DEPLOYMENT_NAME`、`ORYN_DEPLOYMENT_BOX`、`ORYN_DEPLOYMENT_DATA_DIRECTORY = "~/apps/<app>/data"`。`POST /box/apps/<app>/activate`，`keep: 3`、`readyTimeoutSec: 60`。
5. 收到 `ok: true` → `completeDeployTx(row, 结果里的 pruned)`，返回 `OK: true` 与 URL。
6. 收到 `ok: false` → `finishDeployTx(failed, "<code>: <message>")`，返回 `OK: false` 与 orbd 给的 `Code` / `Message` / `Log`。
7. 调用本身出错（连接断开、超时）→ 结果未知，**不直接标失败**：调 `resolveActivating(row)`，按它的结论返回；它也定不下来（box 此刻连不上，或 orbd 还在试运行）就返回 `OK: false, Code: "activate_unknown"`，行留在 `activating` 等对账。

其余方法：

- `CancelDeploy`：没有未结束的部署 → `deploy_not_found`。一个事务里 `CancelBoxDeploy`（只对 `prepared` / `transferring` 生效）：影响 1 行就接着做与 `finishDeployTx` 相同的善后（`first` 的删临时 app 行），提交后调 `inflight` 里的 `cancelTransfer` 掐断在途的传输；影响 0 行说明它已经进了 `activating` → 409 `deploy_in_progress`（"the release is being activated; it finishes or rolls back within a few minutes"）——换链接换到一半不能打断。`Delete` box 用的内部钩子 `abortDeploys(boxID)` 调这台 box 全部在途部署的 `abort`（行随后被级联删掉）。
- `Rollback`：有未结束的部署 → `deploy_in_progress`；目标须是该 app `state = kept` 的 release（`""` 取最新的 kept），否则 `release_not_found`；调 orbd rollback，`ok` 后一个事务里两个 release 的状态对调、`SetBoxAppLive{目标, repo、root、source 都取目标 release 的}`、活动 `rolled_back`。`repo` 也要跟着回去：`--move` 之后回滚到换仓库之前的 release，下一次从原仓库部署不应该再被判成 `app_repo_mismatch`。orbd 的 rollback 对「目标已经是 live」直接回 `ok: true`，所以上一次的应答丢了（orbd 已经切过去、库里还没改）时，再回滚一次就把库补齐了。
- `Restart`：有未结束的部署 → `deploy_in_progress`；没有 live → `app_not_deployed`；重发 `Env` 调 orbd restart，活动 `restarted`。
- `Undeploy`：有未结束的部署 → `deploy_in_progress`；`usable(box)`（box 停着 → `box_stopped`，不做半截下线）；调 orbd `DELETE`，再 `DeleteBoxApp`，活动 `undeployed`，`cfg.DropApp(app)`。
- `SetPublic`：写库、活动 `visibility{public}`；改成私有时在提交之后调 `cfg.DropApp(app)`。
- `PutSecret`：校验名字与保留名、值 ≤ 64 KiB、数量 ≤ 64、`SumBoxSecretSize`（不含同名旧值）加本条 ≤ 1 MiB，任一不过 → `invalid_request`；`Box.Seal("box/"+boxID+"/"+app+"/"+name, value)`。`Secrets` 对 `secret = 0` 的项解密给出 `value`。密钥改动在下一次部署或 `Restart` 之后才进到进程里。
- `Exec`：`usable(box)`；`cwd` = `~`，给了 `app` 则是 `~/apps/<app>/current`（app 须有 live，否则 `app_not_deployed`）；`timeoutSec` 缺省 600。把 orbd `/box/exec` 的响应体边读边写给 `w`。调用方的 ctx 结束时请求随之取消，orbd 杀掉进程组。
- `Copy`（只给 `box.call` 用，截止 30 分钟）：`boxPath` 是相对路径且不含 `..`，给了 `app` 时相对 `~/apps/<app>/data/`，否则相对 `~/`；`orbPath` 必须是绝对路径或 `~/…`（CLI 负责换算）。`toBox` 时源 orb `GET /fs/file` → box `PUT /fs/file?overwrite=true`，反之亦然；目标的父目录必须已存在（orbd 回 404 `not_found`，原样转给调用方）；返回字节数。
- `OnCall`：解 `LinkCtlBoxCallParams`；调用方校验照 `internal/coord/orbmethods.go:54` 的 `caller()`（线程存在、`kind = work`、未归档、`orb_id == orbID`）否则 `forbidden`；调用 orb 的 `Automation` 为真 → `forbidden`（"automation threads cannot use boxes"）。actor = `{agent, threadId}`。按 `op` 分发；结果一律是对象，包进 `LinkCtlBoxCallResult.Result`；`*boxes.Error` 转成 `link.CtlError{Code, Message}`：

| op | params | 结果 |
|---|---|---|
| `list` | — | `BoxList` |
| `show` | `{box}` | `Box` |
| `create` | `{name, provider?, type?}` | `Box` |
| `start` / `stop` | `{box}` | `Box` |
| `rename` | `{box, name}` | `Box` |
| `schedule` | `{box, on?, off?, tz?, clear?}` | `Box` |
| `deploy.prepare` | `PrepareParams` 的 JSON（字段名小驼峰） | `PrepareResult` |
| `deploy.commit` | `CommitParams` | `CommitResult` |
| `deploy.cancel` | `{box, app}` | `{}` |
| `visibility` | `{box, app, public}` | `BoxApp` |
| `undeploy` | `{box, app}` | `{}`（`purge = false`） |
| `restart` | `{box, app}` | `BoxApp` |
| `rollback` | `{box, app, releaseId?}` | `BoxApp` |
| `releases` | `{box, app}` | `BoxReleaseList` |
| `secrets.list` | `{box, app}` | `BoxSecretList` |
| `secrets.set` / `secrets.unset` | `{box, app, name, value?, secret?}` | `{}` |
| `exec` | `{box, command, app?, timeoutSec?}` | `{exit, stdout, stderr, truncated}`；`timeoutSec` > 1800 → `invalid_request`；`Exec` 写进内存缓冲，两路各截到 256 KiB。这条路径不能中途取消（ctl 的调用方放弃等待并不会结束控制面上的处理），命令跑到结束或超时 |
| `cp` | `{box, app?, boxPath, orbPath, toBox}` | `{bytes}` |
| `logs` | `{box, app, service?, tail?}` | `{text}`；向 box orb 发 `GET /services/<app>--<svc>/log?tail=`（`service` 缺省取该 app 的第一个服务，`tail` 缺省 65536、上限 1048576） |
| 其它（含 `delete`） | | `forbidden`（"deleting a box needs the owner: use the Boxes page or `oryn boxes delete` on a signed-in machine"） |

接线：`server.go` 里紧跟 `s.Boxes` 的构造（步骤 1.2，`:219` 之前）加 `s.Orbs.HandleRequest(string(apigen.MethodBoxCall), s.Boxes.OnCall)`。`internal/api/boxes.go` 补上其余 10 个处理器；`execBox` 设 `Content-Type: application/x-ndjson`、`X-Content-Type-Options: nosniff`，把一个每次 `Write` 之后都 `Flush` 的 writer 交给 `Exec`（`internal/server/listen.go:69` 的 `http.Server` 没有 `WriteTimeout`，长响应不会被掐断）。

测试（`internal/boxes/deploy_test.go`，假 `Orbs` 记录 `Do` 的请求并可按路径注入响应）：

- prepare 的每个拒绝分支；有 link 无 `box` 特性时立即 `box_unsupported`（假时钟没有前进）。
- 两个并发的 prepare 只有一个成功，另一个 `deploy_in_progress`；两个并发的 commit 只有一个认领成功。
- 首次部署的三条失败路径（orbd `ok: false`、取消、过期）之后 app 行都消失、名字可再用；已有 live 的 app 部署失败后 app 行原样。
- `--move` 的部署失败后 app 的 `repo` / `root` 没变；成功后变了。
- commit 成功后 release 状态迁移、`set_public` 生效与活动。
- 激活调用返回连接错误、随后 `GET /box/apps` 显示新 release 已 live → 部署记为成功；显示旧 release → 记为失败。
- 「控制面重启」：直接往库里放一行 `activating`，新建 Service 并跑一轮 `Run` → 按 orbd 的状态补完；放一行 `transferring` → `failed`。
- `CancelDeploy` 三种状态；密钥往返、保留名、合计超 1 MiB 被拒；自动化 orb 的 `box.call` 被拒；`OnCall` 的 `delete` 被拒；`list` 的结果是带 `boxes` 键的对象。

**步骤 3.3 — orbd 转发**

`internal/orbsock/orbsock.go` 的方法常量加 `MethodBoxCall = "box.call"`。新建 `internal/orbdrt/boxcall.go`（照 `sshcall.go`）：解 `apigen.LinkCtlBoxCallParams`，`validThreadID`，`op` 非空，`Params` 为 nil 时置空表，`r.ctlForwardFor(ctx, apigen.MethodBoxCall, p, 45*time.Minute)`。`selfsetup.go:165` 旁加 `case orbsock.MethodBoxCall: return r.boxCall(ctx, params)`。runner 的 sock 不提供它，runner 的 link 本来就只放行 `runnerRequests` 里的方法。

**步骤 3.4 — CLI `oryn boxes`**

新建 `cmd/oryn/boxes.go`（命令树与非部署子命令）、`cmd/oryn/boxes_deploy.go`、`cmd/oryn/boxes_transport.go`。`boxesCmd` 在 `init()` 里 `root.AddCommand`。

传输层：`type boxAPI interface` 两个实现，每个子命令只写一次、按 `inOrb()` 选实现。

- orb 内：先 `thread, err := callerThread()`（它返回 `(string, error)`，出错直接返回），再 `sockCall(ctx, orbsock.MethodBoxCall, apigen.LinkCtlBoxCallParams{ThreadID: thread, Op: op, Params: params}, &out, nil)`，从 `out.Result` 再解一次到具体类型。
- orb 外：`newClient()` 的 REST。`exec` 需要流式读，而 `apiClient.do` 一次解一个 JSON、`newClient` 的 `http.Client` 有 5 分钟总超时（`cmd/oryn/client.go:117`）：在 `client.go` 加 `func (c *apiClient) postStream(ctx context.Context, path string, body any) (io.ReadCloser, error)`——用一个 `Timeout` 为 0 的 `http.Client`（传输层沿用 `c.http.Transport`），非 2xx 走 `readAPIError`，写法照 `getRaw`（`:162`）。

| 命令 | 说明 |
|---|---|
| `oryn boxes list [--json]` | 表格列：名字、状态、规格、app 数、最近部署 |
| `oryn boxes show <box> [--json]` | 含时段、停机原因（`stoppedBy`）、各 app 的地址与 live commit |
| `oryn boxes create <name> [--provider P] [--type T]` | |
| `oryn boxes start\|stop <box>` | |
| `oryn boxes rename <box> <new>` | 成功后若当前目录在 git 检出里，把 `.oryn/box-deploy.json` 中 `on == 旧名` 的 target 改成新名并打印改了哪些 |
| `oryn boxes schedule <box> --on HH:MM --off HH:MM --tz <IANA>` / `--clear` | |
| `oryn boxes delete <box> [--yes]` | 只在 orb 外可用（orb 内报用法错误并转述上表那句话）；没带 `--yes` 时要求输入 box 名确认 |
| `oryn boxes deployments <box> [--json]` | app 表：名字、地址、公开 / 私有、仓库、分支、commit、部署时间（还没有 live 的显示 `deploying`） |
| `oryn boxes deploy [target] [--on <box>] [--name <app>] [--create] [--provider P] [--type T] [--private\|--public] [--move] [--discard-box-changes]` | 只在 orb 内可用（见下） |
| `oryn boxes cancel-deploy <box> --app <app>` | |
| `oryn boxes undeploy <box> --app <app> [--purge-data]` | `--purge-data` 只在 orb 外可用 |
| `oryn boxes restart <box> --app <app>` | |
| `oryn boxes rollback <box> --app <app> [--to <releaseId>]` | |
| `oryn boxes exec <box> [--app <app>] [--timeout 秒] -- <命令…>` | orb 外用 `postStream` 逐行解 NDJSON，`o` 写 stdout、`e` 写 stderr，Ctrl-C 取消请求（远端进程随之被杀）；orb 内等命令结束后整段打印，`--timeout` 上限 1800，不能中途取消。两边都以远端退出码退出（`exitCodeError`） |
| `oryn boxes cp <源> <目标> [--app <app>]` | 恰有一侧是 `<box>:<路径>`。本地一侧先 `filepath.Abs`（`./note.txt` 这类相对路径由 CLI 换算，orbd 的文件接口只认绝对路径与 `~/…`）。orb 内走 `cp` op（本地一侧须在 `$HOME` 之下）；orb 外用 `orbs/{orbId}/fs/file` 的 `GET` / `PUT`。目标的父目录不存在时报错，不自动创建（先 `oryn boxes exec <box> -- mkdir -p …`） |
| `oryn boxes logs <box> --app <app> [--service S] [--tail N]` | |
| `oryn boxes secrets set <NAME> --box <box> --app <app> (--data-file F \| --value V) [--plain]` / `secrets ls` / `secrets rm` | `--plain` = `secret: false`；`set` 之后打印 `run oryn boxes restart <box> --app <app> (or deploy again) to apply` |

`deploy` 的步骤（都在 orb 里的 `oryn` 进程内，外部命令是镜像自带的 `git`、`rsync`、`tar`）：

1. 不在 orb 里 → 用法错误 `boxes deploy runs inside a thread's environment; ask the agent in a thread to deploy`。`repoRoot = git rev-parse --show-toplevel`，失败 → `not a git checkout`。
2. 定 target：给了位置参数就从 `LoadTargets` 里取（没有 → 报错并列出已有 target），`root` 取它的；否则 `boxcfg.Find(repoRoot, cwd)` 定 `root`（找不到 → `no .oryn/box.yaml found between here and the repository root; see the oryn-boxes skill`），再找 `Root == root` 的 target。`--on` / `--name` 覆盖 target 里的值；合并后缺任一项 → 用法错误 `the first deploy needs --on <box> --name <app>`。`--private` → `Public = false`，`--public` → `true`，都没给：target 里 `private: true` → `false`，否则 nil。
3. `commit = git rev-parse HEAD`；`branch = git rev-parse --abbrev-ref HEAD`；`repo` = `boxcfg.NormalizeRepo(git config --get remote.origin.url)`，没有 origin 时 `"local:" + (git rev-list --max-parents=0 HEAD 的最后一行)`。解析 `<root>/.oryn/box.yaml`，把警告打到 stderr。
4. `deploy.prepare`。之后任何一步失败、或收到 SIGINT / SIGTERM，都先发 `deploy.cancel`（忽略它的错误）再退出。
5. 构建目录：`git status --porcelain` 为空 → 就在 `repoRoot`；否则 `git worktree add --detach ~/.cache/oryn/box-build/<deployId> HEAD`，stderr 打一行 `uncommitted changes are not deployed; building commit <短 sha> in a clean worktree`，结束时 `git worktree remove --force`。
6. `build` 非空时在 `<构建目录>/<root>` 跑 `bash -lc`，环境是当前环境加 prepare 返回的三项，输出直通；非零退出 → 失败。
7. 打包到 `ArchivePath`：
   - `source: false`：`tar -C <构建目录>/<root> -czf <包> <artifacts…>`；服务的 `cwd` 原样。
   - `source: true`：暂存目录 `~/.cache/oryn/box-build/<deployId>-stage`：`git clone --no-checkout <构建目录> <stage>`；有 origin 就 `git -C <stage> remote set-url origin <原 origin URL>`，没有就 `git remote remove origin`；`root != ""` 时 `git sparse-checkout set --no-cone '/*' '!/*/' '/<root>/'` 再为每个 `include` 追加 `'/<相对仓库根的路径>/'`；`branch != "HEAD"` 时 `git checkout -B <branch> <commit>`，否则 `git checkout --detach <commit>`；然后把构建目录里已装的依赖与构建产物叠上去：对 `<root>`（`root == ""` 时是整个仓库）与每个 `include` 目录各跑一次 `rsync -a --ignore-existing --exclude=.git <构建目录>/<目录>/ <stage>/<目录>/`；`tar -C <stage> -czf <包> .`。服务的 `cwd` 换算成 `path.Join(root, cwd)`。
8. 包大小 > `MaxBytes` → `release_too_large`（提示改用 `source: false` 加 `artifacts`）。算 SHA-256。
9. `deploy.commit`。`OK`：`SaveTargets`（键 `TargetKey(root)`，值 `{on, name, root, private}`；`private` 只在最终是私有时写），打印 `deployed <app> to <box>: <URL>`。否则打印 `Code: Message` 与 `Log`，退出码 1。两种情况多打一行：`Code` 是 `activate_unknown` → `the box did not answer; check oryn boxes deployments <box> in a minute`，并且不发 `deploy.cancel`；带了 `--move` 而 `Code` 是 `box_changes` → `--move to a different repository cannot be checked against the code on the box; add --discard-box-changes once nothing there needs saving`（换仓库后两边历史不相干，orbd 的祖先检查必然不过）。
10. 无论成败删掉包、暂存目录与临时 worktree。

测试（`cmd/oryn/boxes_test.go`，假传输层）：target 解析的各分支；`--private` / `--public` / target 默认的三态；两种打包方式产出的 tar 条目（临时 git 仓库，含一个子目录 app 与 `include`）；脏工作区走临时 worktree 且未提交的文件不在包里；`rename` 改写 `box-deploy.json`；`cp ./a b1:a` 发出的 `orbPath` 是绝对路径；`exec` 的 NDJSON 解码与退出码；`callerThread` 出错时不发请求。

---

### 阶段 4 — 入口：app 主机名与悬停预览

**步骤 4.1 — portal 数据面认 app 主机**（`internal/portal`，接线在 `internal/server/server.go`）

```go
type AppTarget struct {
	BoxID, App, OrbID string
	Port              int
	Public, Live      bool   // Live = 已有成功激活过的 release
}
type Apps interface {
	ResolveApp(ctx context.Context, name string) (AppTarget, bool, error)
}
func (s *Service) SetApps(a Apps)
func (s *Service) DropApp(app string)   // 取消该 app 的全部匿名连接（改成私有、下线时调用）
```

`boxes.Service` 实现 `ResolveApp`（新文件 `internal/boxes/portal.go`：`GetBoxAppByName` + 所属 box 的 `orb_id`，`Live = live_release_id IS NOT NULL`）。接线：`portal.New`（`server.go:443`）之后、`go s.Portal.Run` 之前 `s.Portal.SetApps(s.Boxes)`；构造 `s.Boxes` 的地方（步骤 1.2）把 `DropApp` 填成 `func(app string) { if s.Portal != nil { s.Portal.DropApp(app) } }`。

改动（thread portal 的行为不变）：

- `portalReq`（`portal.go:119`）加 `root string`（本主机根地址，带结尾 `/`）与 `app *AppTarget`；`origin = strings.TrimSuffix(root, "/")`。thread portal 的 `root = s.base.URL(code, port)`，app 的 `root = s.base.AppURL(app)`。`handshake.go` 里 `redeem`、`check`、`login` 用到 `s.base.URL(p.code, p.port)` 的地方全部换成 `p.root`。
- 并发登记：`Service` 加第二个登记表 `appReg = newRegistry(1024, 512)`（每台 box 1024 条流、每个公开 app 匿名 512）；`Revoke` 对两个表都 `cancelKey`；`DropApp(app)` = `appReg.cancelKey("app/" + app)`（它同时递增 epoch）。
- `serve`（`:152`）：`ParseLabel` 不通过时试 `ParseAppLabel`；`s.apps == nil` 或解析不出 → 现有的 404 `portal_not_found`。app 分支单独成函数 `serveApp`，**次序是硬约束**（「改成私有」与一个在途的匿名请求赛跑时，匿名请求要么登记在 `DropApp` 之前、被它取消，要么登记时发现 epoch 变了、重新判定）：
  1. `epoch := s.appReg.current()`——先于解析。
  2. `ResolveApp`：没找到 → 404 `portal_not_found`；取 orb，`deleting` / `deleted` → 404。组 `portalReq{label, port, orbID, orbState, nav, root, origin, app}`。
  3. `isReserved` 的路径交给 `s.reserved`（握手端点）。
  4. `authorize`。
  5. `!p.app.Live` → 502 `app_not_deployed`（"这个应用还没有部署成功"；导航请求用普通错误页）。放在鉴权之后：私有 app 不向匿名访客透露部署状态；也不能交给拨号去发现——同一台 box 上若有普通服务恰好开着这个端口，请求会落到别人头上。
  6. 可用性：`orbs.EnsureUsable(ctx, orbID, featurePortal, false, 0)` 返回 `ErrOrbNotReady` → 503 `app_unavailable`（"这个应用暂时不可用"；导航请求用带 5 秒自动刷新的过渡页，加 `Retry-After: 5`），其它错误走现有的 `usableErr`。**不**调 `TouchUserCoalesced`，不唤醒：box 的启停只由 `boxes.desired` 决定。
  7. `appReg.enter(epoch, orbID, who.key, who.anon, cancel)`；返回 `errStale` 时（至多重试 3 次，同现有循环）：重取 `epoch`，然后**从第 2 步整个重来**——重新 `ResolveApp`、重取 orb、重建 `portalReq`（app 可能已经被下线、又在另一台 box 上重新部署，`orbID` 与端口都会变），再 `authorize`、第 5 步、第 6 步。现有循环只重做 `authorize`，对 app 不够。实现上把第 2、4、5、6 步收进一个返回 `(p, who, ok)` 的闭包，首次与每次重试都调它；`enter` 与 `target` 都用最后一次的 `p`。
  8. 代理。
- `authorize`（`:242`）：设备令牌与 Cookie 两步不变（Cookie 的 `label` 就是 app 名）；之后 app 走 `if p.app.Public { return identity{key: "app/" + p.app.App, anon: true}, true }`，不查 `portal_shares`。其余分支（跨站 403、导航跳握手、401）共用。`authURL` 改成收 `*portalReq`：app → `s.base.Origin + "/portal/auth?h=" + label + "&next=" + QueryEscape(next)`；thread portal 仍是 `?t=&p=`。
- `AuthHandler`（`handshake.go:43`）：查询里有 `h` 时走 app 分支——`ParseAppLabel(h)` 不通过 → 400；鉴权与未登录跳转照旧；`ResolveApp` 没找到 → 404；签发码 `s.codes.issue(h, pr.ID, now)`，回跳 `s.base.AppURL(h) + "__oryn/auth?code=…&next=…"`。
- `usableErr`（`:402`）加一支：`errors.Is(err, orbs.ErrBoxStopped)` → 503 `box_stopped`（"这台 box 已停止"）。它管的是开在 box 上的线程自己声明的 `t-…-p…` portal：那条路径会尝试唤醒，被闸门挡下。
- `target`（`proxy.go:19`）加 `app, preview bool`。`preview` = app 请求且查询里有 `oryn-preview` 参数。
- `modifyResponse`（`proxy.go:152`）：`t.app && !t.preview` 时不动 `X-Frame-Options` 与 CSP，也不加 `X-Robots-Tag`（公开站点的防嵌套与收录由 app 自己决定）；`Set-Cookie` 的处理（丢弃 portal Cookie、去掉 `Domain`）对 app 照做。`t.preview` 时沿用 thread portal 的整套改写（允许主站内嵌）。
- `rewrite` 不变：app 同样拿到 `X-Oryn-Authenticated: owner=yes|no`、`X-Forwarded-*`，名字含 `_` 的入站头同样被丢弃。
- `proxyError`：`t.app` 时 `connection_refused` 与默认分支的文案用「应用没有在监听（可能正在重启）」，状态码与错误码不变。
- 主站 CSP 的 `frame-src`（`server.go:410` 的 `frameSrc`）：启用 portal 时由 `base.FrameSrc()` 改成 `"'self' " + base.FrameSrc()`。原因：内嵌帧里的私有 app（悬停预览）没有 app 主机的 Cookie 时，`/__oryn/login` 会 302 到主站的 `/portal/auth` 再跳回来，`frame-src` 对重定向的每一跳都生效，只放行 portal 泛域名的话这一跳被拦。`'self'` 不带来新的可嵌入面：主站自己的文档仍然带 `frame-ancestors 'none'` 与 `X-Frame-Options: DENY`，`/portal/auth` 只回 302；未启用 portal 时 `frame-src` 本来就是 `'self'`。同步改断言这条 CSP 的测试（`grep -rln "frame-src" internal --include=*_test.go` 列出的四个文件里涉及主站 SPA 且启用 portal 的那些）与 `docs/protocol.md:321`。`/s/*` 分享页（`:404`）的 CSP 不动。

测试（`internal/portal/app_test.go`，沿用 `fixture_test.go` 的夹具加一个假 `Apps`）：

- 公开 app 匿名 GET 透传且带 `owner=no`；私有 app 导航 → 302 到 `/portal/auth?h=<app>`，完成握手后 200 且带 `owner=yes`；私有 app 非导航 → 401；设备令牌直通。
- `DropApp` 断开在途的匿名 WebSocket。
- 赛跑：假 `Apps` 第一次返回 `Public: true`，在 `enter` 之前（用登记表的测试钩子，或在假 `Apps` 的第一次返回之后）把它改成私有并调 `DropApp` → 该匿名请求最终得到 401 / 302，而不是被代理。
- 没有 live 的 app：主人（设备令牌）得到 502 `app_not_deployed`，匿名访问私有的得到 401；即使 orb 上有别的服务开着同一个端口也没有拨号。
- box 的 orb 不可用 → 503 `app_unavailable` 且没有调用 `Wake`。
- 普通请求的响应保留 app 自己的 `X-Frame-Options`，带 `?oryn-preview=dark` 的被改写。
- `t-…-p…` 的既有用例全部照过；box 上的 thread portal 在闸门关着时回 503 `box_stopped`。

**步骤 4.2 — 预览探测**（`internal/boxes/preview.go`）

契约：app 根页面 `<head>` 里的 `<meta name="oryn-preview" content="v1; enabled; iframe">`。`content` 按 `;` 切分并去空白，第一段须是 `v1`，其余是标志；同时有 `enabled` 与 `iframe` 才算有预览；未知标志忽略。

`boxes.Service` 加 `probePreview(ctx, app)`：经 `orbs.DialPortal(orbID, port)` 发一个不带 Cookie 的 `GET /`（`Host` = app 主机名，`X-Oryn-Authenticated: owner=no`，`Accept: text/html`），至多跟随 3 次同主机重定向，读响应体前 256 KiB，用 `golang.org/x/net/html` 的 tokenizer 在 `</head>` 之前找这个标签；结果写 `SetBoxAppPreview`，变化时 `Changed()`。单次探测 5 秒超时，任何失败都记成没有预览。

调用点都在本步骤加：`CommitDeploy` 与 `resolveActivating` 在 `completeDeployTx` 返回 true 之后起一个后台探测；`Rollback` 成功之后同样；`Run` 里每 5 分钟对 `running` 的 box 上有 live 的 app 各一次。

测试：标签解析的表驱动（缺 `v1`、缺 `iframe`、未知标志、标签在 `<body>` 里）；重定向到别的主机不跟随；部署成功后 `preview` 列被更新。

前端加载方式见步骤 7.4。

---

### 阶段 5 — 在 box 上开线程

- `internal/boxes/threads.go`：`func (s *Service) ThreadTarget(ctx context.Context, boxRef, app string) (orbID, cwd string, err error)`——`desired = stopped` → `box_stopped`；`app == ""` → `cwd = "~"`；否则 app 须在这台 box 上（`app_not_found`）且有 live（`app_not_deployed`），`cwd = "~/apps/<app>/current"`，app 行 `source = 1 && root != ""` 时再接 `/<root>`（app 行的 `root` / `source` 只在激活成功时才变，所以它指向的目录一定存在）。
- `internal/threads/threads.go`：
  - `CreateParams` 加 `Cwd string`（注释：只能与 `OrbID` 同用，且那台 orb 必须是 box；由 API 层从 `executor.kind = box` 解析得出，从不来自客户端）。
  - `Create` 的 `p.OrbID != ""` 分支末尾（`:188` 之后）两条互相对应的校验：`o.Box == 1 && p.Cwd == ""` → `errf(CodeOrbIsBox, "this orb is a box; start the thread with executor.kind = box")`（不让 `executor.kind = orb` 绕过 `ThreadTarget` 的停机与目录判定）；`o.Box != 1 && p.Cwd != ""` → `errf("invalid", "cwd is only valid for threads on a box")`。
  - `:232` 处改成：`rt != nil` 走原样，`else if p.Cwd != "" { row.Cwd = &p.Cwd }`。`startAgent` 已经把 `t.cwd` 放进 `agent.start`（`agent.go:221`），orbd 的普通分支已经认它（`internal/orbdrt/agent.go:603-606`），两处都不用改。
  - `Config` 加的 `BoxThreadsChanged func()`（步骤 1.2 已接线）在三处调用，都只在线程的 orb `Box == 1` 时调：`Create` 的 `s.out().IndexUpsert(out)`（`:331`）之后，用函数里外层的那个 `orb` 变量（`p.OrbID` 分支里的 `o` 是分支局部的，到这里已经出了作用域）；`forkShare` 的 `IndexUpsert`（`:~1116`）之后，源 orb 的 box 标记由 `Fork` 传进来（步骤 1.1 已把那次 `GetOrb` 提到分支之前）；`deleteThread`（`:730`）删行之后（`orbID` 非空时补一次 `GetOrb`）。box 页的活动流与用量图都数线程，而在一台已经 `ready` 的 orb 上增删线程不产生任何 orb 事件。
- `internal/api/threads.go` 的 switch（`:168`）加 `case apigen.ThreadExecutorBox:`——`e.BoxID == ""` → 400；`s.Boxes == nil` → 501；`orbID, cwd, err := s.Boxes.Service.ThreadTarget(ctx, e.BoxID, deref(e.App))`，错误走 `writeBoxErr`；`p.OrbID, p.Cwd = orbID, cwd`。
- `internal/artifacts/cap_files.go` 的 `filesOpen`（`:202`）：现在是「线程有 cwd 就直接用」，而随后 `:218` 要求它是绝对路径，box 线程的 `~/…` 会被判成不可用。改成三路：`tg.cwd` 是绝对路径 → 照旧直接用；`tg.cwd` 非空但不是绝对路径 → 向 orbd 要 `/fs/roots?strict=1`（`fs.get` 本来就用 `orbs.SetWorkdir` 把 `tg.cwd` 带过去，`:237`），拿回来的 `workdir` 就是展开后的绝对路径；orbd 回 404 `workdir_missing`（app 已下线）→ 返回 `unavailable`，**不**退到缺省工作目录——否则一个建在 app 目录里的 artifact 在 app 下线后能读到整个工作区；`tg.cwd` 为空 → 现有的不带 `strict` 的分支不变。`fs.get` 现在把所有非 200 的应答按状态码换成页面错误（`:248-260`），404 一律成了「文件不存在」：在那段通用映射之前加一支，错误体的 `code == "workdir_missing"` 时返回包内的哨兵错误 `errWorkdirMissing`，`filesOpen` 用 `errors.Is` 认它。
- CLI `cmd/oryn/thread.go` 的 `threadNew`（`:70-170`）：加 `--box <名字或 id>` 与 `--app <app>`（`--app` 须与 `--box` 同用；`--box` 与 `--runner` / `--orb` / `--provider` / `--type` 互斥），发 `executor: {kind: "box", boxId, app}`。orb 内的 `oryn thread new` 走 ctl `thread.create`，那条路径不带执行位置，本计划不动它。

测试：`internal/threads` 加用例——box orb + `Cwd` 时线程行的 `cwd` 被写入且 `agent.start` 带上它；box orb 不带 `Cwd` → `orb_is_box`；非 box orb 带 `Cwd` 被拒；删掉 box 上最后一个线程后 orb 仍在；建、共用 fork、删各触发一次 `BoxThreadsChanged`，普通 orb 上不触发。`internal/api` 加 `executor.kind = box` 的三个错误分支。`internal/artifacts` 加用例——线程 cwd 是 `~/apps/x/current` 时 `filesOpen` 成功，`wd` 取自 `/fs/roots` 的应答，且请求带了 `X-Oryn-Workdir`。

另加一条 `internal/artifacts` 的用例：`/fs/roots?strict=1` 回 404 `workdir_missing` 时 `filesOpen` 返回不可用，没有再发不带 `strict` 的请求。

---

### 阶段 6 — agent 能用上

**步骤 6.1 — orb 内的技能**

新建 `internal/orbdrt/assets/oryn-boxes/SKILL.md`，`assets.go` 加 `//go:embed`，`selfsetup.go:283` 的写出表加一项，落到 `$HOME/.agents/skills/oryn-boxes/SKILL.md`（与 `oryn-setup` 同一处；runner 不发）。frontmatter：`name: oryn-boxes`，`description: Use when the user wants an app to keep running at its own URL, asks to deploy to a box, create/start/stop a box, set app secrets, or run commands on a box.`。正文（英文，与其它内置技能一致）必须写清：

- box 是什么、与 orb 的分工（orb 里构建，box 只运行）；用户说「部署到 box」而没有 box 时直接 `--create`，不用先问。
- `.oryn/box.yaml` 的全部字段与两份示例（`source: false` + `artifacts`，示例里可执行文件放在 `dist/`、服务 `cwd: dist`；单仓多 app 的 `include`）；服务必须监听 `$PORT`；四个 `ORYN_DEPLOYMENT_*` 变量，数据只写 `$ORYN_DEPLOYMENT_DATA_DIRECTORY`。
- 部署的是 `HEAD` 提交：未提交的改动不会带上，先提交。
- 命令速查：`oryn boxes deploy --on <box> --name <app> --create`、之后的 `oryn boxes deploy`、`--private` / `--public`、`--move`、`secrets set` 后要 `restart`、`exec`（在 orb 里至多 30 分钟、不能中途取消）/ `cp`（目标目录要先存在）/ `logs`、`rollback`。
- 部署失败时读 CLI 打印的日志尾；`box_changes` 的处理（在 box 上的线程里提交并推送，或 `--discard-box-changes`；`--move` 到另一个仓库时两边历史不相干，必须同时给它）；`deploy_in_progress` 卡住时 `cancel-deploy`。
- 不能做的：删 box（请主人在 Boxes 页或自己的机器上删）。

`internal/orbdrt/assets/oryn-orb.ts:35` 附近的系统提示里加一行指向这个技能（非 runner 时）。`selfsetup_test.go` 里断言技能 frontmatter 的用例加上它。

**步骤 6.2 — 全局助手工具**（`internal/coord`）

新建 `boxtools.go`：

```go
type Boxes interface {
	List(ctx context.Context) ([]apigen.Box, error)
	Get(ctx context.Context, ref string) (apigen.Box, error)
	Create(ctx context.Context, name, provider, typ string, by boxes.Actor) (apigen.Box, error)
	Start(ctx context.Context, ref string, by boxes.Actor) (apigen.Box, error)
	Stop(ctx context.Context, ref string, by boxes.Actor) (apigen.Box, error)
	SetSchedule(ctx context.Context, ref string, sch *apigen.BoxSchedule, by boxes.Actor) (apigen.Box, error)
	SetPublic(ctx context.Context, ref, app string, public bool, by boxes.Actor) (apigen.BoxApp, error)
	Delete(ctx context.Context, ref string, by boxes.Actor) error
	ThreadTarget(ctx context.Context, boxRef, app string) (orbID, cwd string, err error)
}
func (s *Service) SetBoxes(b Boxes)     // 未设置时工具回 coordinator_unavailable（boxesOr()，照 machinesOr）
```

- `errCode`（`coord.go:351`）加一支 `var be *boxes.Error; if errors.As(err, &be) { return be.Code }`，`errMessage` 同样取它的 `Message`——否则工具失败与确认卡的审计一律显示 `internal_error`。
- 工具名常量与定义（`boxToolDefs()`，并入 `profile.go:382-387` 的拼接；`HostToolCall` 的 switch 加一组 case，只对 `KindAssistant` 开放）：

| 工具 | 参数 | 行为 |
|---|---|---|
| `list_boxes` | — | 只读；结果过 `wrapUntrusted`（把它加进 `tools.go:140` 那个 switch；box 与 app 的名字可能出自 orb 里的 agent） |
| `get_box` | `box` | 只读，同样过 `wrapUntrusted` |
| `create_box` | `name`、`provider?`、`type?` | `allowMutate` + 审计 `create_box` |
| `start_box` / `stop_box` | `box` | 同上 |
| `set_box_schedule` | `box`、`on?`、`off?`、`tz?`、`clear?` | 同上 |
| `set_box_app_visibility` | `box`、`app`、`public` | 同上（app 本来就默认公开，不走确认卡） |
| `delete_box` | `box` | 照 `delete_orb`（`admintools.go:818-854`）：先 `allowMutate`（确认卡数量上限之外的那道限速），`Get` 把名字解析成 box，再 `requestConfirm{Action: ToolDeleteBox, Target: box.ID, TargetLabel: box.Name, Title: "Delete box <name>?", Summary: 连同全部 app 与数据一起删除, Args: boxArgs{BoxID: box.ID}}`——存的是不可变的 id，不是调用时给的名字。`coord.go:192` 旁 `s.onConfirm(ToolDeleteBox, s.confirmDeleteBox)`：按 `BoxID` 重新 `Get`（已不存在 → `not_found`），再 `Delete(id)` |

- `threadtools.go`：`assistantCreateArgs` 加 `Box, App string`（JSON `box`、`app`），`applyAssistantCreate` 里用 `boxesOr().ThreadTarget` 解析成 `OrbID` + `Cwd`；与 `orbId` / `runner` / `provider` 同给 → `invalid_request`。`create_thread` 的工具定义加这两个参数的说明。
- actor 一律 `{assistant, 对话线程 id}`。`server.go` 里 `s.Coord.SetBoxes(s.Boxes)`。部署需要仓库检出，助手不直接部署：工具说明里写「要部署就用 `create_thread` 开一个工作线程让它跑 `oryn boxes deploy`」。

测试：照 `admintools_test.go` 与 `confirm_test.go` 加工具名唯一性；`delete_box` 的确认卡往返（卡片挂出之后把 box 改名，批准时删的仍是原来那台）；`boxes.Error` 的错误码原样出现在工具结果里；Space 协调者调用被拒；`create_thread` 带 `box` + `app` 时建出的线程 `cwd` 正确，带 `box` 又带 `runner` 被拒。

---

### 阶段 7 — Web

落位：「更多」分区的概览入口。视觉沿用现有令牌与组件（`docs/design/web-guidelines.md` 的硬规则：不写颜色字面量、圆角用令牌、浮层走 `ui/Floating.tsx`、确认用 `confirmAction`、图标用已登记的 Phosphor、文案中英两份、普通主操作用 `variant="primary"`、悬停出现的东西不做进场动画）。

**步骤 7.1 — 数据层、实时、mock**

- `web/src/api/box-queries.ts`：`boxKeys = { all: ["boxes"], list, detail(id), activity(id | "all"), usage(id), releases(id, app), secrets(id, app) }`；`useBoxes`、`useBox`、`useBoxActivity`、`useBoxUsage`、`useBoxReleases`、`useBoxSecrets`、`useBoxOfOrb(orbId)`（从 `useBoxes` 的数据里按 `orbId` 找，线程页用）；mutation：`useCreateBox`、`useUpdateBox`、`useDeleteBox`、`useStartBox`、`useStopBox`、`useUpdateBoxApp`、`useUndeployBoxApp`、`useRestartBoxApp`、`useRollbackBoxApp`、`useCancelBoxDeploy`、`usePutBoxSecret`、`useDeleteBoxSecret`，成功后 `invalidateQueries({ queryKey: boxKeys.all })`；`useRealtimeBoxSync(client = realtime)` 照 `api/github-queries.ts:58-70`。
- `web/src/realtime/socket.ts`：`boxesListeners`、`onBoxesChanged(cb)`、`emitBoxesChanged()`；消息 switch（`~:751-770`）加 `case "boxes.changed"`；`ws.onopen`（`~:456-462`）里也 emit 一次。`AppShell.tsx:210-219` 调 `useRealtimeBoxSync()`。三处测试桩（`App.test.tsx`、`AppShell.test.tsx`、`useSection.test.tsx`）各加 `onBoxesChanged: vi.fn(() => () => undefined)`。
- `web/src/mock/boxes.ts`：`class MockBoxes`，`Deps { changed(): void; threads; orbs }`，`handle(req, p, m, url)` 实现步骤 0.2 表里除 `execBox` 外的全部路径（`execBox` 回 501）；`server.ts` 里 `readonly boxes = new MockBoxes({ changed: () => this.broadcast({ type: "boxes.changed" }), … })` 并接进 `handleFetch` 的分发链。
  - 建线程的分支（`~:2522`）认 `executor.kind === "box"`：box `desired = stopped` → 409 `box_stopped`，app 没有 live → 409 `app_not_deployed`；成功时线程挂到 box 的 orb、`cwd` 按阶段 5 的规则给。
  - fork（`~:2032-2041,2372-2411`）：现在的 mock 对 `share` 也新分配一台 orb。源线程在 box 的 orb 上时改成——`share` 的子线程留在同一台 orb 并沿用 `cwd`，`copy` 回 409 `box_unsupported`。
  - 在 box 的 orb 上建、fork、删线程之后都广播一次 `boxes.changed`（与后端的 `BoxThreadsChanged` 对应）。
  - 文件与改动接口的范围（`server.ts:2430` 的 `runnerScope`、`orbfs.ts:576-582`）：现在非 runner 一律是 `{ home: HOME, workdir: fs.workdir }`。box 线程（orb 有 `box` 标记、请求带 `thread`、线程有 `cwd`）改用 `{ home: HOME, workdir: <cwd 里的 ~ 换成 HOME> }`，该目录在 mock 文件系统里不存在时退回 `fs.workdir`（与 orbd 一致）。`orbfs.ts` 里 rename / delete 对「工作目录及其祖先」的保护（`:429`、`:445`）现在比的是 `fs.workdir`，改成比本次请求的 `scope.workdir`。mock 的 orb 文件系统里给两台 box 各放一份 `apps/<app>/current/` 目录（几个文件即可），否则文件面板打开的是夹具的缺省工作区。
  - 种子数据：`localStorage["oryn.mock.boxes"] === "empty"` 时没有 box；缺省一台 `workbench`（`running`，时段 `05:00–23:00 Europe/Berlin`，三个 app：`bug-tracker` 公开有预览、两个服务 `web` 与 `worker`；`team-wiki` 私有；`tiny-crm` 首次部署进行中——没有 `live`、`deploying: true`）加一台 `desired = stopped` 的 `lab`（一个已部署的 app），以及配套的活动与 21 天用量。app 的 `url` 用固定的 `https://<app>.apps.oryn.test`（截图与审计按这个地址拦截 iframe）。
  - 两台 box 的 orb 也要进 mock 的 orb 表（`box: true`），`services` 镜像里放上 `bug-tracker--web`、`bug-tracker--worker` 等条目并各带一段日志：现有的服务日志接口按 `orb.services` 精确匹配名字（`server.ts:2111-2136`），不放就打不开日志。
- `web/src/components/ui/primitives.tsx` 的 `FRIENDLY_CODES`（`~:716-755`）加本文全部新错误码；`errors.<code>` 中英两份。

**步骤 7.2 — 路由、入口、命令面板**

- `App.tsx`：懒加载 `BoxesPage`、`BoxPage`（照 `:11-63` 的写法），在 AppShell 组里加 `<Route path="boxes" …/>` 与 `<Route path="boxes/:boxId" …/>`（`artifacts` 路由旁，`:143`）。
- `lib/sections.ts` 的 `sectionOf`：`more` 那一支加 `|| under(p, "/boxes")`，注释同步。
- `components/layout/more-entries.ts`：在 `artifacts` 条目之后加 `{ key: "boxes", group: "overview", icon: "hardDrives", labelKey: "nav.boxes", to: "/boxes" }`。（`box` 图标已被 providers 与首屏执行位置胶囊占用；`hardDrives` 与 `agent-ssh` 条目不同组，可以共用。）
- `components/palette/CommandPalette.tsx` 的 `actionCands`（`~:376-430`）加 `action("boxes", "hardDrives", t("palette.actions.boxes"), "boxes deploy apps machine 部署 机器", () => go("/boxes"))`。

**步骤 7.3 — 总览页与 New Box**

`web/src/pages/BoxesPage.tsx`，骨架照 `pages/AssistantComputerPage.tsx`（`Sheet inset={bp === "expanded"}`、带 `data-window-drag` 的 `header`、`ChromeLead`、`WindowControls`）。页头：图标 `hardDrives`、标题、右侧 `Button variant="primary" icon="plus"`（新建 Box，有 box 时才显示）。滚动区带 `data-testid="boxes-scroll"`（截图的溢出断言用）。

- 没有 box → `components/boxes/BoxesHero.tsx`：左栏是眉题 `BOXES`（`text-2xs` 等宽大写）、两行标题、一段说明、三条带图标的要点（`terminal` / `lock` / `globe`）、主按钮「新建 Box →」（`variant="primary"`）与文字链接「阅读文档」（`https://github.com/Lynricsy/Oryn/blob/main/docs/features.md#boxes`，新窗口）；`expanded` 宽度下右栏是 `BoxesHeroPreview`——用真实的 `AppRow` 与活动行组件喂一份写死的演示数据（三个 app、三条活动），包在一个仿窗口的 `Card` 里，整体 `aria-hidden`、`inert`、不可交互；`compact` / `medium` 下不渲染它。
- 这个链接指向的锚点在本步骤一并建好：`docs/features.md` 的目录（`:7-14`）加 `- [Boxes](#boxes)`，在「Artifacts」一节之前加 `## Boxes`，写清——box 是什么、与线程 orb 的分工；怎么部署（`.oryn/box.yaml` 的字段与一份最小示例、`oryn boxes deploy`）；地址与公开 / 私有；密钥与数据目录；release 保留与回滚；时段；在 box 上开线程；预览标签 `oryn-preview`；边界（见本文「Assumptions」里面向用户的那几条）。
- 有 box → 两节：box 列表（`ListRow`：`StatusDot`、名字、`type · N apps · 最近部署的 app 与相对时间`，点进 `/boxes/<id>`）与「最近活动」（`useBoxActivity("all")`，`BoxActivityList`，每行带 box 名）。
- 加载 `Spinner`，出错 `ErrorText`。未配置 `portal_domain`（存在 app 且任一 app 没有 `url`）时页顶一条 `Callout`：app 没有访问地址，要先配 `portal_domain`。

`components/boxes/NewBoxDialog.tsx`（`Dialog wide`）：标题「新建 Box」，说明与三条要点同 Hero；表单：名字（`TextInput`，本地按正则校验，错误就地显示）、规格（`Select`，选项 = `useProviders()` 里 `name !== "@host"`、不是 runner、**没有 `disabledAt`** 的 provider × `orbTypes`——`/providers` 会返回已停用的，而建 box 时显式选它会被拒；文案 `<provider> · <type> · N 核 · 内存`，缺省项「默认」= 不传 provider / type）；页脚：`Button`「交给 Agent」（`uiStore.getState().seedHome({ text: t("boxes.new.seedPrompt") })` 后 `navigate("/")`）与 `Button variant="primary"`「创建」。创建失败（重名、超上限、默认落点解析不出等）时对话框**保持打开**、表单内容不丢，错误用 `ErrorText` + `describeError(e, t)` 显示在表单下方；成功后 `navigate("/boxes/" + id)`。`expanded` 宽度下对话框右侧放裁剪过的 `BoxesHeroPreview`。

**步骤 7.4 — box 页**

`web/src/pages/BoxPage.tsx`：`useBox(id)` 的三种状态分开处理——`isPending` → `Spinner`；出错且是 `ApiError` 的 `box_not_found` → `<Navigate to="/boxes" replace />`（分区记忆会记住路径，不能停在 404）；其它错误 → 原地 `ErrorText` 加重试，不跳转。滚动区带 `data-testid="box-scroll"`。

- 页头：面包屑 `Boxes / <名字>`（`Link` + `h1`，照 Artifacts 回收站页）；右侧圆形主按钮「新线程」（机器；`desired = stopped` 时禁用，`title` 说明要先启动）与 `Menu`：启动 / 停止（按 `desired`）、改名…、运行时段…、删除…。
- 主栏：
  - `APPS · N`：每行 `components/boxes/AppRow.tsx`——首字母色块 `AppBadge`（直接用 `components/ui/data.tsx` 已导出的 `nameHue` 与 `firstGrapheme`，以及 `ProjectBadge` 同款的色相样式，不另写取色）、名字、地址（`font-mono` 的链接，新窗口）、私有时一个中性 `Badge`「私有」、部署中时一个带脉冲的 `StatusDot`、一行元信息。元信息只在 `app.live` 存在时渲染 `repo · branch · 短 sha · 部署于 <相对时间>`；没有 `live`（首次部署进行中）时显示 `boxes.apps.firstDeploy`。服务有 `failed` / `unhealthy` 时行尾 `Badge tone="err"`。指针设备悬停或聚焦整行时，行尾显示「新线程」按钮（触屏常显）；没有 `live` 或 box `desired = stopped` 时这个按钮禁用。
  - 行菜单：设为私有 / 公开、重启、回滚…（列出 `kept` 的 release）、密钥…、日志、下线…。没有 `live` 时只留「取消部署」（`useCancelBoxDeploy`；已经进入激活时服务端回 409 `deploy_in_progress`，用 `describeError` 提示稍后再看）。「日志」打开现成的 `ServiceLogDialog`（`components/thread/ServicesSection.tsx:65`），参数是 `box.orbId` 与服务的**完整名字**（`app.services[i].name`，形如 `bug-tracker--web`）；app 只有一个服务时直接打开，多个时「日志」是带子菜单的项，每个服务一条。
  - 没有 app → `EmptyState`：一句话加可复制的 `oryn boxes deploy --on <名字> --name <app>`。
  - `ACTIVITY`：`BoxActivityList`（`useBoxActivity(id)`）。每行：图标或主人头像、文案（见文案表的 `boxes.activity.*`）、相对时间；线程项链到 `/t/<id>`。
- 侧栏（`expanded` 在右，其余宽度排在主栏下方）：
  - `components/boxes/BoxUsageChart.tsx`：标题「最近 21 天」，21 根竖条 = 每天线程数（高度按最大值归一，至少 2px），竖条下一条分 21 段的细线 = 在线率（段的不透明度 = `upRatio`）；图例 `N 个线程`、`M% 在线`（21 天平均）。用现有的 `--ok` 类令牌，不引入图表库。
  - About：`KeyValue` 行——状态（`StatusDot` + 文案；`stoppedBy = schedule` 时写「按时段停机」；`failed` 时附 `failureReason`）、时段（`05:00–23:00 Europe/Berlin` 或「一直运行」）、规格（`type · N 核 · 内存`）、Provider、创建时间；脚注一句「文件与数据在部署和重启之间保留在这台机器上」。
- 对话框：`ScheduleDialog`（两个 `type="time"` 输入加时区 `Select`，缺省浏览器时区；「清除时段」）、改名（`TextInput`）、`SecretsDialog`（列表：名字、是否密文、更新时间、删除；新增：名字、值 `TextArea`、「明文环境变量」`Toggle`；底部提示改动在重启或下次部署后生效，并给一个「现在重启」按钮）。失败都留在对话框里用 `describeError` 显示。
- 确认（都用 `confirmAction`）：删除 box（`danger`）、停止 box、下线 app（`danger`）、设为公开、在 box / app 上开线程（正文：线程直接操作线上的应用和它的数据）。
- 开线程：确认后 `uiStore.getState().seedHome({ text: "", executor: { kind: "box", boxId, app } })`、`navigate("/")`。
- 悬停预览 `components/boxes/AppPreview.tsx`：只在 `app.preview && app.url`、`matchMedia("(hover: hover)")` 为真时启用。
  - 触发器是包住 `AppBadge` 的 `button`（可聚焦，`aria-label` 用 `boxes.preview.label`），它的 ref 就是浮层的锚点。指针停 300ms 或键盘聚焦时打开。
  - 浮层用 `ui/Floating.tsx`（不是 `PickerPopover` 的 `Popover`：那个宽度写死 `w-80`、没有 `anchor`）：`<Floating anchor={badgeRef} side="bottom" align="start" gap={6} bare motion={false} className="w-96 max-w-[calc(100vw-1rem)]">`，里面是一个 `<a href={app.url} target="_blank">` 包着 8:5 的画框（宽 384 时正好 240 高）。`motion` 是给 `Floating` 新加的可选属性（缺省 true）：为 false 时不加 `pop-in` 类（`Floating.tsx:165`）。`bare` 并不去掉它，而 `.glass.pop-in` 仍有 180ms 的位移动画（`index.css:979-994`）；悬停出现的浮层不做进场动画（`web-guidelines.md:107`）。
  - 关闭：指针离开触发器**与**卡片两者 150ms 之后（期间回到任一个就取消——锚点与卡片之间有 6px 的缝，不加这段延迟指针过不去），或焦点离开两者，或 Escape。关闭即卸载 iframe。
  - 画框里先显示放大的 `AppBadge`；`iframe` 的 `load` 事件之后直接换成 iframe（切 `invisible`，不做淡入）。`iframe`：`data-testid="app-preview-frame"`、`sandbox="allow-scripts allow-same-origin"`、`allow=""`、`tabIndex={-1}`、`pointer-events: none`、`title` 为 app 名；`src` 对公开 app 是 `<url>/?oryn-preview=<light|dark>`，对私有 app 是 `<url>/__oryn/login?next=` + `encodeURIComponent("/?oryn-preview=<light|dark>")`（主题取当前生效的明暗）。

**步骤 7.5 — 首屏执行位置与线程页**

- `state/ui.ts:97`：`HomeSeed` 加 `executor?: Schemas["ThreadExecutorBox"]`。
- `components/ui/PickerPopover.tsx` 的 `Option` 加可选的 `disabled?: boolean`：给 `button` 加 `disabled` 与 `aria-disabled`、去掉 hover 底色、整行 `opacity-50`。缺省行为不变。
- `components/boxes/BoxOptions.tsx`：`export interface BoxPick { boxId: string; app?: string }`（**只存 id**，名字在渲染时从 `useBoxes` 的数据里查），`export function BoxOptions({ pick, onPick }: { pick: BoxPick | null; onPick(p: BoxPick | null): void })`——`PopoverLabel`「Boxes」，**每台** box 一组（含停着的）：一行「<box 名> · 机器」加每个有 `live` 的 app 一行（`leading` 用 `hardDrives` / `AppBadge`）。`desired = stopped` 的 box 整组用 `Option disabled` 并带 `Badge`「已停止」——看得见、点不了；可选与否只看 `desired`，与服务端 `ThreadTarget` 的判定一致。没有 box 时整个分组不渲染。
- `pages/HomePage.tsx`：
  - 加 `const [boxPick, setBoxPick] = useState<BoxPick | null>(null)`，与 `runnerPick` 互斥（选一个清另一个；选「新 orb」的选项两个都清）。执行位置弹层在 `<RunnerOptions …/>`（`:854`）之后渲染 `<BoxOptions pick={boxPick} onPick={…}/>`。
  - 「新 orb」各选项的 `selected`（`:815`）从 `!runnerPick && …` 改成 `!runnerPick && !boxPick && …`，否则选了 box 之后原来的 orb 类型还亮着。
  - `executorText`（`:347`）在 `boxPick` 时由 `boxLabel(boxPick, boxes.data)` 给出：数据还没到 → 主文案用 `…`；到了 → `{ label: "<box> · <app 或 机器>", main: box 名, badge: app 名或「机器」 }`；到了却找不到这台 box、或它 `desired = stopped`、或 app 没有 `live` → 胶囊显示 `boxes.picker.gone` 的警示态。胶囊图标 `hardDrives`。
  - seed 的 effect（`:282-294`）：`seed.executor` 存在时**立即** `setBoxPick({ boxId, app })`（不等 box 列表——`takeHomeSeed()` 取走即清空，effect 也不会因为查询数据到达而重跑，等列表就丢了），同时 `setPicked({ kind: "role", role: "default" })`：首屏偏好是「全局助手」的用户，`submit` 会在读到 `boxPick` 之前就走 `sendAssistant`（`:411`），执行位置胶囊也是藏着的。
  - `submit`（`:404`）：`boxPick` 处于上面的警示态时不发请求，打开执行位置弹层并显示 `boxes.picker.gone`；box 列表还在加载时等它。请求体 `executor: boxPick ? { kind: "box", boxId: boxPick.boxId, ...(boxPick.app ? { app: boxPick.app } : {}) } : runnerPick ? … : executor`；`boxPick` 时不带 `browserProfileId`，Profile 胶囊隐藏（`showProfile` 加 `&& !boxPick`）。
  - `catch` 的兜底分支（`:568`）从 `e instanceof Error ? e.message : String(e)` 改成 `describeError(e, t)`（文件里已经导入）：不改的话新加的人话错误码到不了首屏。Profile 冲突与目录冲突两支不动。
- 线程页（box 线程 = `orb.box === true`）：
  - `components/boxes/box-model.ts` 导出 `boxThreadLabel(orb, thread, boxes): string | null`（返回 `<box 名> · <app 或 机器>`，app 从 `thread.cwd` 的 `~/apps/<名>/` 前缀取）与 `boxLabel`（上面首屏用的）。
  - `components/boxes/BoxStartLink.tsx`：一个小号 `Link` 按钮「去 Boxes 页启动」（`boxes.startOnPage`），指向 `/boxes/<id>`。
  - `ThreadHeader.tsx:734-746`：`isRunner ? <RunnerChip/> : <OrbChip/>` 改成三路，box 线程用新的 `BoxChip`（图标 `hardDrives`、文案 `boxThreadLabel`、整体是到 `/boxes/<id>` 的 `Link`）；外层的 `!infoBeside` 条件保留。
  - `CompactThreadHeader.tsx`：`boxThreadLabel` 在 box 列表到达之前是 `null`——这段时间**保持现有的 orb 文案与图标**，不渲染到 Boxes 的入口；拿到 box 之后才换成下面的样子。`status` 里 `key: "env"` 的那一行（`:163-204`）——`icon: "hardDrives"`、`label: t("titleMenu.runsOn", { name: boxThreadLabel })`，`children` 追加 `{ key: "box", icon: "hardDrives", label: t("boxes.openBox"), onSelect: () => navigate("/boxes/" + box.id) }`；原生菜单由同一份 `status` 经 `nativeItems` 生成，自动带上这一项。标题胶囊里可见的环境片段（`meta` 中 `data-testid="title-env"` 的那个 `span`，`:~297`）显示 `boxThreadLabel` 与 `hardDrives` 图标；`nativeMeta`（`:339-356`）的对应项写成 `{ text: boxThreadLabel, icon: { symbol: "externaldrive" } }`——`NativeSeg.text` 必须是字符串、`icon` 是 `NativeIcon`（`lib/host.ts:188-201`），不认网页图标名。同时在 `web/src/lib/native-header.ts:81` 的 `SF_ICON` 里加 `hardDrives: "externaldrive"`：没列的图标在原生菜单里会退成 `circle`（`:236`）。只改 `:136-171` 的菜单文字是不够的，胶囊本身不会变。`ThreadHeader` 的 `BoxChip` 同理：没拿到 box 时仍渲染 `OrbChip`。
  - `InfoPanel.tsx`：`OrbSection` 里决定唤醒按钮的 `wake`（`:553`）加 `&& !orb.box`；box 线程在 `stopped` / `stopping` 时该位置渲染 `BoxStartLink`。`OrbSpecs`（`:478-481`）的 runner 分支旁加 box 分支显示 `boxThreadLabel`。
  - `FilesPanel.tsx`：两处 `<WakeButton threadId={threadId} />`（`:99`、`:131`）对 box 线程换成 `BoxStartLink`。
  - `Dialogs.tsx` 的 `ForkDialog`（`:373`）：`const runner = …` 改成 `const orb = useOrbOf(threadId); const shareOnly = orb?.kind === "runner" || orb?.box === true;`，下面 `:408` 的 `runner ?` 换成 `shareOnly ?`。这个对话框也是侧栏行菜单与转录入口用的那一个，一处改全。

**步骤 7.6 — 文案**

`web/src/i18n/locales/zh-CN.json` 与 `en.json` 同时加（`locales.test.ts` 要求键集合一致）。`nav.boxes`：Boxes / Boxes；`palette.actions.boxes`：打开 Boxes / Open Boxes。带数量的键照仓库惯例写 `_one` / `_other` 两条（`en.json:173-174`），中文两条同文。新命名空间 `boxes`：

| 键 | zh-CN | en |
|---|---|---|
| `hero.eyebrow` | BOXES | BOXES |
| `hero.title` | 你的应用跑在这里。\nAgent 也在。 | Your apps run here.\nSo does your agent. |
| `hero.body` | 一台常驻机器，放任何你想一直开着的东西：随手做的内部工具，或者对外的站点。同一台 box 上的应用共用这台机器。 | An always-on machine for anything you want running, from a quick internal tool to a customer-facing site. Every app on a box shares that one machine. |
| `hero.point1` | 在线程里、让全局助手，或用 `oryn boxes` 创建和更新 | Create and update them from threads, the assistant, or `oryn boxes` |
| `hero.point2` | 线程可以直接开在 box 上，操作线上的应用和数据 | Start threads right on the box to work on live apps and data |
| `hero.point3` | 每个应用都有自己的访问地址 | Get a live URL for every app |
| `hero.docs` | 阅读文档 | Read the Docs |
| `new.title` / `new.open` | 新建 Box | New Box |
| `new.name` / `new.size` / `new.sizeDefault` | 名字 / 规格 / 默认 | Name / Size / Default |
| `new.nameHint` | 小写字母、数字和连字符，至多 30 个字符 | Lowercase letters, digits and hyphens, up to 30 characters |
| `new.create` / `new.handOff` | 创建 / 交给 Agent | Create / Ask the agent |
| `new.seedPrompt` | 帮我建一台 box。先问我要在上面托管什么，再决定名字和规格。 | Create a box for me. Ask what I want to host on it first, then pick a name and a size. |
| `list.title` / `list.recent` | 你的 Box / 最近活动 | Your boxes / Recent activity |
| `list.apps_one` / `list.apps_other` | {{count}} 个应用 / {{count}} 个应用 | {{count}} app / {{count}} apps |
| `noUrl` | 还没有配置 portal_domain，应用没有访问地址。 | portal_domain is not configured, so apps have no URL. |
| `openBox` / `startOnPage` | 打开这台 Box / 去 Boxes 页启动 | Open this box / Start it on the Boxes page |
| `state.starting` / `running` / `stopping` / `stopped` / `failed` | 启动中 / 运行中 / 停止中 / 已停止 / 启动失败 | Starting / Running / Stopping / Stopped / Failed |
| `state.stoppedBySchedule` | 按时段停机 | Stopped by schedule |
| `apps.title` / `activity.title` | 应用 · {{count}} / 活动 | Apps · {{count}} / Activity |
| `apps.private` / `apps.deploying` / `apps.unhealthy` | 私有 / 部署中 / 服务异常 | Private / Deploying / Unhealthy |
| `apps.firstDeploy` | 首次部署进行中 | First deploy in progress |
| `apps.deployed` | 部署于 {{when}} | deployed {{when}} |
| `apps.empty` | 还没有应用。在线程里运行： | No apps yet. In a thread, run: |
| `apps.newThread` / `machineThread` | 在这个应用上开线程 / 在这台机器上开线程 | New thread on this app / New thread on this machine |
| `apps.startFirst` | 先启动这台 Box | Start the box first |
| `apps.makePrivate` / `makePublic` / `restart` / `rollback` / `secrets` / `logs` / `undeploy` / `cancelDeploy` | 设为私有 / 设为公开 / 重启 / 回滚… / 密钥… / 日志 / 下线… / 取消部署 | Make private / Make public / Restart / Roll back… / Secrets… / Logs / Undeploy… / Cancel deploy |
| `preview.label` | 预览 {{app}} | Preview {{app}} |
| `menu.start` / `stop` / `rename` / `schedule` / `delete` | 启动 Box / 停止 Box / 改名… / 运行时段… / 删除 Box… | Start Box / Stop Box / Rename… / Schedule… / Delete Box… |
| `about.title` / `status` / `schedule` / `size` / `provider` / `created` | 关于 / 状态 / 时段 / 规格 / Provider / 创建于 | About / Status / Schedule / Size / Provider / Created |
| `about.alwaysOn` | 一直运行 | Always on |
| `about.footnote` | 文件与数据在部署和重启之间保留在这台机器上。 | Files and data stay on the machine between deploys and restarts. |
| `usage.title` / `up` | 最近 21 天 / {{percent}}% 在线 | Last 21 days / {{percent}}% up |
| `usage.threads_one` / `usage.threads_other` | {{count}} 个线程 / {{count}} 个线程 | {{count}} thread / {{count}} threads |
| `activity.deployed` | {{app}} 已部署 · {{commit}} | {{app}} deployed · {{commit}} |
| `activity.deploy_failed` | {{app}} 部署失败 | {{app}} deploy failed |
| `activity.rolled_back` / `restarted` / `undeployed` | {{app}} 已回滚 / {{app}} 已重启 / {{app}} 已下线 | {{app}} rolled back / {{app}} restarted / {{app}} undeployed |
| `activity.visibility` | {{app}} 改为{{state}} | {{app}} made {{state}} |
| `activity.thread` | 开了线程 {{title}} | started {{title}} |
| `activity.created` / `started` / `stopped` / `renamed` / `schedule` | 创建了这台 box / 已启动 / 已停止 / 由 {{from}} 改名为 {{to}} / 更新了运行时段 | Box created / Started / Stopped / Renamed from {{from}} to {{to}} / Schedule updated |
| `actor.agent` / `assistant` / `schedule` / `system` | Agent / 助手 / 时段 / 系统 | Agent / Assistant / Schedule / System |
| `confirm.delete.title` / `body` / `action` | 删除 {{name}}？/ 这台 box 上的全部应用和数据都会删除，无法恢复。/ 删除 Box | Delete {{name}}? / Every app and all data on this box will be deleted. This cannot be undone. / Delete Box |
| `confirm.stop.title` / `body` / `action` | 停止 {{name}}？/ 上面的应用会离线，磁盘和数据保留。/ 停止 | Stop {{name}}? / Its apps go offline. The disk and data are kept. / Stop |
| `confirm.undeploy.title` / `body` / `action` | 下线 {{app}}？/ 应用停止并失去访问地址，数据目录保留。/ 下线 | Undeploy {{app}}? / The app stops and loses its URL. Its data directory is kept. / Undeploy |
| `confirm.public.title` / `body` / `action` | 公开 {{app}}？/ 任何拿到地址的人都能打开它。/ 设为公开 | Make {{app}} public? / Anyone with the URL can open it. / Make public |
| `confirm.thread.title` / `body` / `action` | 在 {{target}} 上开线程？/ 线程直接操作线上的应用和它的数据。/ 开线程 | Start a thread on {{target}}? / The thread works on the live app and its data. / Start thread |
| `schedule.title` / `on` / `off` / `tz` / `clear` | 运行时段 / 开机 / 关机 / 时区 / 清除时段 | Run schedule / On / Off / Time zone / Clear schedule |
| `secrets.title` / `add` / `value` / `plain` | {{app}} 的密钥 / 添加 / 值 / 明文环境变量 | Secrets for {{app}} / Add / Value / Plain environment variable |
| `secrets.applyHint` / `restartNow` | 改动在重启或下次部署后生效。/ 现在重启 | Changes apply after a restart or the next deploy. / Restart now |
| `picker.label` / `machine` / `stopped` | Boxes / 机器 / 已停止 | Boxes / Machine / Stopped |
| `picker.gone` | 这台 Box 或应用现在不能开线程，换一个执行位置。 | This box or app can't take a thread right now. Pick another place to run. |

`assistant.confirm.delete_box` 与六个审计动作的标签照 `delete_orb`（`en.json:3992,4101`）各加一条；`errors.<新错误码>` 逐个加人话文案。

**步骤 7.7 — 前端测试、截图与排版审计**

- vitest：
  - `BoxesPage.test.tsx`：空态渲染 Hero 与两个按钮；有 box 时列出并可进入。
  - `NewBoxDialog.test.tsx`：非法名字不发请求；已停用的 provider 不在选项里；服务端回 `box_exists` 时对话框还开着、输入还在、看得到人话错误；「交给 Agent」写入 seed 并跳首屏。
  - `BoxPage.test.tsx`：私有徽标；停止走确认；`box_not_found` 重定向、500 原地显示错误不跳转；`preview` 为假时不挂预览触发；没有 `live` 的 app 行不渲染 commit、开线程按钮禁用；两个服务的 app「日志」出子菜单，点开的对话框拿到的是 `<app>--<svc>`。
  - `box-model.test.ts`：`boxThreadLabel`、`boxLabel` 的三种状态、时段文案、相对时间。
  - `HomePage.executor.test.tsx` 加用例：选中 app 后请求体的 `executor` 等于 `{kind: "box", boxId, app}`；选了 box 之后「新 orb」的选项没有一个是选中态；停着的 box 的行点了没反应；seed 带 executor 且 box 列表**晚到**时仍预选、发送时 executor 正确；首屏偏好是全局助手、seed 带 executor 时请求发到 `/api/v1/threads` 而不是助手；box 列表到了却没有这台 box 时不发请求；服务端回 `box_stopped` 时显示的是 `errors.box_stopped` 的文案。
  - 新建 `components/thread/ForkDialog.test.tsx`（现在没有 fork 对话框的单测）：box 线程与 runner 线程都只有「共用」一个选项；普通 orb 线程两个都有。
- `web/scripts/screenshots.ts` 的 `SCREENS` 加 `boxes-empty`、`boxes-list`、`box-detail`、`box-new-dialog`、`home-executor-boxes`，`widths` 写成 `[360, 390, 1280]`（新做的窄屏布局要过 360）。`verify` 用 `noHorizontalOverflow(<testid>)`：它量的是那个元素自己的 `scrollWidth - clientWidth`，所以 testid 必须落在**真正的滚动体**上（落在内容的包裹层上，包裹层撑破父容器时它自己的差值仍是 0），并且找不到 testid 时直接判失败。五个界面的目标：`boxes-empty` 与 `boxes-list` → `boxes-scroll`；`box-detail` → `box-scroll`（这两个是页面自己的滚动区）；`box-new-dialog` → `new-box-dialog-body`；`home-executor-boxes` → `executor-options-body`。后两个需要给共享组件各加一个可选的透传属性 `bodyTestId?: string`：`Dialog` 把它写到自己的滚动体上（`primitives.tsx:240-245`），`Floating` 写到内层滚动区上（`Floating.tsx:169-173`），`PickerPopover` 的 `Popover` 原样转给 `Floating`；`NewBoxDialog` 与首屏的执行位置弹层各传一次。`box-detail` 的 `prepare` 里用 `page.route("https://bug-tracker.apps.oryn.test/**", …)` 给预览地址一个固定的 HTML 应答。
- 排版审计：新建 `docs/ui-audit/areas/boxes.md`（标题、范围、`## 清单` 四列表、`## 问题记录`，格式照 `areas/sections.md`）与 `web/e2e/ui-audit/boxes.e2e.ts`（照 `artifacts.e2e.ts` 用 `surface({ id })`）。清单的 id 与 spec 的用例标题一一对应，共 9 个：`boxes.empty`、`boxes.list`、`boxes.new-dialog`、`boxes.detail`、`boxes.detail.app-menu`、`boxes.detail.schedule`、`boxes.detail.secrets`、`boxes.detail.preview`（仅桌面：spec 里用 `only` 显式跳过 m390）、`boxes.home-executor`。
  - `boxes.detail.preview`：mock 的 `fetch` 替身管不到 iframe 的导航（`mock/index.ts:66` 的帧拦截只认 artifact 帧），要用 Playwright 的 `page.route` 给种子里的预览地址回一页带可见文字的 HTML。`surface()` 没有「导航前」的钩子（`init` 是页面脚本，`open` 在 `go()` 之后才跑，`audit.ts:432-445`），而预览的 iframe 是悬停之后才开始导航的，所以在这个界面的 `open` 里先 `page.route(…)` 再悬停即可。断言 `page.frameLocator("[data-testid=app-preview-frame]")` 里那段文字可见，而不只是浮层开了。另给它一个 `reopen`：审计的「关掉重开」是按 Escape 之后重跑打开步骤（`audit.ts:462-477`），指针此时还停在徽标上，再 `hover` 一次不会有新的进入事件——`reopen` 里先把指针移到页面角落、等浮层消失，再悬停。
  - 这 9 个界面在基线里不存在，而 `inventory.ts` 要求每个视口都有通过的 `before` 结果与截图（`inventory.ts:62-80`）。做法：`before` 阶段对着**当前工作区**跑这一个 spec——before 只记录、不判失败，产出的就是首版自己的截图。`areas/boxes.md` 的「说明」里写明「M23 新增，无更早基线，before 取自首版」。`inventory.ts` 不改。
  - `inventory.ts` 对**全部**已登记界面对账，缺任何一个界面的产物都以非零退出（`:175`），而 `docs/ui-audit/shots/` 不入库（`.gitignore:20`）。这台开发机上已有其它界面的产物，只需补跑 boxes 的 before 与 after；在干净检出上要先把其余界面跑出来（命令见 Verification）。

---

### 阶段 8 — 端到端

**步骤 8.1 — Go e2e**

新建 `cmd/fakeapp/main.go`（测试用的小 HTTP 服务，与 `cmd/fakedesk` 同类，不进 `Makefile` 的 `BINS`）：监听 `127.0.0.1:$PORT`；`GET /` 回当前目录 `version.txt` 的内容，响应头 `X-Frame-Options: DENY`；`GET /env?k=NAME` 回该环境变量；`GET /data` 回 `$ORYN_DEPLOYMENT_DATA_DIRECTORY` 是否存在；`GET /pid` 回自己的 PID；当前目录有名为 `refuse` 的文件时启动即退出码 1；当前目录有 `preview.html` 时 `GET /` 回它。

新建 `internal/server/e2e_m23_test.go`；`TestMain` 的构建列表加 `fakeapp`；栈要启用 portal——按 `grep -n PortalDomain internal/server/*_test.go` 找到的既有写法配置。共用的夹具辅助函数：

- `boxFixture(t, s)`：建一个普通线程，在它的 orb home 里建 git 仓库——`.oryn/box.yaml` 为 `source: false`、`artifacts: [dist]`、`services: {web: {command: ./fakeapp, cwd: dist}}`；`dist/fakeapp`（拷贝构建出的二进制）与 `dist/version.txt`。**每次改动都先提交再部署**（部署的是 `HEAD`；提交用 `internal/server/e2e_m5_sync_test.go:29-37` 的 git 辅助函数）。改版本、加 `refuse`、加 `preview.html` 都是改 `dist/` 里的文件。
- `deploy(t, args…)`：以 orb 内的环境（`ORYN_ORB=1`、`ORYN_THREAD_ID`、orb 的 `HOME`，写法照 `grep -n 'ORYN_ORB' internal/server/*_test.go` 找到的既有用例）跑真实的 `oryn boxes deploy`，返回退出码与输出。
- 收尾：process 驱动下 orbd 与它拉起的服务都是宿主上的进程、共用回环地址，而栈的清理只杀 orbd（`internal/providerrt/process.go:299-323`），服务在自己的进程组里会活下来，占着 30000 端口等下一个测试——下一个测试的「等端口」会被它骗过。每个建了 box 的测试在 `newStack` **之后**登记 `t.Cleanup`（因此先于栈的清理执行）：box 还在 → `POST …/stop` 并等到 `stopped`；box 已经被测试删掉 → 等它的 orb 到 `deleted`（删除是先让 orbd 关停、再回收，`internal/orbs/service.go:1167-1178`；orbd 正常关停才会停掉它的服务）。这几个测试都不加 `t.Parallel()`，同一时刻全进程只有一台 box。

`TestE2EM23`（缺省空闲阈值；顺序子测试，前一步失败即停）：

| 编号 | 做什么 | 期望 |
|---|---|---|
| R1 | `POST /api/v1/boxes {name: "b1"}`，等 `state = running` | 201；orb 行 `box = 1` |
| R2 | `version.txt` = `v1`，提交，`deploy --on b1 --name hello` | 退出 0，stdout 含 `deployed hello to b1`；仓库里出现 `.oryn/box-deploy.json`；`box_deploys` 该行 `succeeded` |
| R3 | 对栈地址发 `GET /`，`Host: hello.<portal_domain>`，不带凭据 | 200，体 `v1`，保留 `X-Frame-Options: DENY` |
| R4 | `v2`，提交，`deploy --private` | R3 的请求带 `Sec-Fetch-Mode: navigate` → 302 到 `/portal/auth?h=hello`；不带 → 401；带设备令牌 → 200 `v2` |
| R5 | 加 `dist/refuse`，提交，部署 | 退出 1，输出含 `not_listening`；带令牌的 GET 仍是 `v2`；`GET …/releases` 里 live 仍是 v2 那个；app 的 `deploying` 为 false |
| R6 | 删 `refuse`，再部署 v3、v4（各自提交） | box 的 `~/apps/hello/releases` 下恰好 3 个目录；最旧的 release `state = pruned` |
| R7 | `PUT …/secrets/GREETING {value: "hi"}`，`POST …/restart` | 带令牌 `GET /env?k=GREETING` → `hi`；`GET /data` 报告数据目录存在 |
| R8 | `POST …/rollback {}` | 带令牌 GET → `v3` |
| R9 | `POST /api/v1/threads`，`executor: {kind: "box", boxId, app: "hello"}` | 201；fakeomp 的启动记录（`cmd/fakeomp/host.go:50` 的 dump）里的 `cwd` 经 `filepath.EvalSymlinks` 后等于 box home 下 `apps/hello/current` 解析出的目录；`GET /api/v1/orbs/{boxOrb}/fs/roots?thread=<id>` 的 `workdir` 以 `/apps/hello/current` 结尾，对它 `fs/list` 能看到 `dist`；`executor: {kind: "orb", orbId: <box 的 orb>}` → 409 `orb_is_box` |
| R10 | `POST …/stop`，等 `stopped` | 带设备令牌对 app 主机 GET → 503 `app_unavailable`，orb 事件里没有新的 `waking`；在 R9 的线程上经 WS 发 `cmd`（prompt）→ 回 `box_stopped` 的错误帧，线程没有排队的命令；`POST /api/v1/threads/{id}/wake` → 409 `box_stopped`；再用 box 执行位置建线程 → 409 `box_stopped` |
| R11 | `POST …/start`，等 `running` | 带令牌 GET → `v3`（服务随 resume 自动拉起） |
| R12 | 在源线程的 orb 里（当前目录在 `$HOME` 下）`oryn boxes exec b1 --app hello -- cat dist/version.txt`；`oryn boxes cp ./note.txt b1:note.txt --app hello` | 输出 `v3`；box 的 `~/apps/hello/data/note.txt` 存在 |
| R13 | 加 `dist/preview.html`（含 `oryn-preview` 标签），提交，部署 | 轮询 `GET /api/v1/boxes/b1` 直到该 app `preview = true` |
| R14 | `DELETE /api/v1/orbs/{boxOrbId}`；`DELETE /api/v1/boxes/b1` | 前者 409 `orb_is_box`；后者 204，`listBoxes` 为空，`hello.<portal_domain>` 回 404；等到 orb 行 `deleted`，此时 30000 端口上已经没有监听者 |

另外三个各自起栈的测试（需要的旋钮都是构造期读取的，必须在 `newStack` 之前设好）：

- `TestE2EM23AlwaysOn`：照 `TestFullStackFreezeAndWake`（`e2e_test.go:384`）把 `evalInterval` 设成 200ms、`AgentIdle` / `UserIdle` 设成 1 秒起栈。建 box；用 box 执行位置（机器）建一个线程，等它 `idle` 后删掉；同时建一个普通线程作对照。等 5 秒：对照线程的 orb 进入 `frozen`（证明阈值在生效），box 的 orb 仍是 `ready`，它的 orb 事件里没有 `freezing`、`deleting`。
- `TestE2EM23Revive`：起栈前 `boxesTick = 200ms`、`boxesUnlinkedAfter = time.Second`（`t.Cleanup` 还原）。建 box、部署 v1，记下 `GET /pid`。找到 box 的 orbd 进程（扫 `/proc`：`cmdline` 是 `orbd run` 且 `environ` 含 `ORYN_ORB_ID=<box 的 orb id>`），发 **SIGTERM**（SIGKILL 会留下 app 自己的进程组继续监听，后面的 GET 就证明不了任何事）。期望：orb 事件里出现 `ready → waking`（`reason = link lost`），随后回到 `ready`；再扫到的 orbd 是另一个 PID；匿名 GET → `v1` 且 `GET /pid` 变了。
- `TestE2EM23Source`：`source` 缺省、单仓两个 app（`crm/`、`status/` 各有 box.yaml，`crm` 的 `include: ["../shared"]`，服务命令用 `fakeapp` 的拷贝）；分别在两个目录里部署 → `box-deploy.json` 有两个 target；`crm` 的 release 里没有 `status/` 目录、有 `shared/`、`git status --porcelain` 为空；用 box 执行位置在 `crm` 上建线程 → cwd 以 `/apps/crm/current/crm` 结尾；在 box 的 `current` 里改一个被跟踪的文件后再部署 → 退出 1 且输出含 `box_changes`，加 `--discard-box-changes` 通过。换仓库（另建一个历史不相干的仓库，同名 app）依次：直接部署 → `app_repo_mismatch`；只加 `--move` → `box_changes`，输出里有提示加 `--discard-box-changes` 的那一行；`--move --discard-box-changes` 但带 `refuse` → 输出含 `not_listening`，app 的 `repo` 没变、在它上面还能建线程；去掉 `refuse` 再来 → 通过，app 的 `repo` 变了；随后 `rollback` → app 的 `repo` 回到原来那个，从原仓库再部署不需要 `--move`。

**步骤 8.2 — 浏览器验收**

新建 `web/scripts/m23-accept.ts`，`web/package.json` 注册 `accept:m23`。起栈、登录、`check` / `note` / `shot` 等辅助函数照 `web/scripts/m4-accept.ts`（它同样需要 portal 域名与真实的 orb）；app 用 `fakeapp`，部署走真实的 `oryn boxes deploy`。检查项：

| 编号 | 做什么 | 期望 |
|---|---|---|
| B1 | 「更多 → Boxes」→ 新建 Box `acc` | 进入 box 页，状态从「启动中」到「运行中」 |
| B2 | 在一个线程的 orb 里部署带 `preview.html` 的 `hello`，`--private` | APPS 出现 `hello`，带「私有」徽标与地址 |
| B3 | **全新的浏览器上下文**，只用 Cookie 登录主站（没有 app 主机的 Cookie，不带设备令牌）；另部署一个公开的 `pub`（同样带 `preview.html`）。打开 box 页，先悬停 `pub` 的徽标，再悬停私有的 `hello` | `pub`：`app-preview-frame` 里出现预览文字。`hello`：出现预览文字，期间有一次对 `/portal/auth?h=hello` 的请求，控制台没有 `Refused to frame`。`hello` 这一半不过时按 Assumptions 末条降级，降级后改看 B3′ |
| B3′ | （只在降级后替代 B3 里 `hello` 的那一半）悬停、聚焦 `hello` 的徽标 | 没有预览浮层，徽标不是可聚焦的触发器；它的地址链接照常能在新窗口打开并走完登录 |
| B4 | （观察项，不是 M23 的通过条件；**在决定是否降级之前跑**，那时 `frame-src` 还带着 `'self'`）同一个上下文里打开某个线程从未访问过的 Portal 面板 | 记下 iframe 是否显示出服务页面，写进本阶段的日志。它与私有预览走同一跳握手，用来判断 B3 的失败是 app 分支特有的，还是只凭 Cookie 的内嵌握手本来就走不通 |
| B5 | 把首屏默认对象设成全局助手；在 box 页点 `hello` 行的「新线程」并确认 | 首屏对象是工作线程，执行位置胶囊是 `acc · hello`；发送后发出的是 `POST /api/v1/threads`，`executor.kind = box` |
| B6 | 进入 B5 建出的线程 | 页头显示 `acc · hello`；文件面板的根下能看到 `dist`；fork 对话框只有「共用」 |
| B7 | 停止 box，回到该线程发一条消息 | 看到 `box_stopped` 的人话错误；信息面板与文件面板里没有「唤醒」按钮，有「去 Boxes 页启动」 |
| B8 | 无痕上下文打开 app 地址 | 被带去登录；把 app 设为公开并启动 box 后直接看到页面 |

---

### 阶段 9 — 文档

（`docs/features.md` 的 Boxes 一节已在步骤 7.3 写好。）

- `README.md`：功能表里加 Boxes 一格（一句话 + 链到 `docs/features.md#boxes`）；CLI 示例区（`:290-311`）加一组 `oryn boxes …`；目录树 / 文档索引若列了里程碑则补 M23。
- `docs/protocol.md`：新增 `## M23：Boxes` 一节——数据模型（六张表与 `orbs.box`）、REST 表、`box.call` 的 op 表、orbd 的 `/box/*` 路由与 `OrbBoxResult` 的错误码（`checksum_mismatch`、`size_mismatch`、`archive_missing`、`bad_archive`、`too_large`、`box_changes`、`not_listening`）、app 主机名的鉴权与 502 / 503、`boxes.changed`、`oryn-preview` 契约、部署状态机（`prepared → transferring → activating → succeeded | failed | canceled` 与重启后的补完规则）。`:321` 的 `frame-src` 说明同步成新值。
- `docs/deploy.md`：`portal_domain` 一节补一段——app 主机名与 thread portal、artifact 帧共用同一个泛域名与证书，不需要新的 DNS 记录；box 的数据在哪、哪些操作会丢（见 Assumptions）；Firecracker 的名额与磁盘说明。
- `docs/development.md`：测试一节加 `TestE2EM23*` 与 `accept:m23` 的跑法。

## Critical files & anchors

- `internal/orbdrt/services.go` — `reload`（`:543`，尤其 `:614-618` 的「定义没变就不拉起」）、`assignPortsLocked`（`:656`）、`Exposed`（`:393`）、`Start` / `Stop`（`:750`、`:818`）、`runOnce`（`:1037`）、`envLocked`（`:1253`）：app 服务挂进监督器的全部落点；同端口多服务必须绕开 `assignPortsLocked`，换 release 必须显式 `launch`。
- `internal/orbdrt/localapi.go:247-269` — `mutablePath` 对 `~/.oryn/**` 的保护：release 上传因此走专用路由，而不是放松这条规则。
- `internal/orbs/service.go` — `insertOrb`（`:818`）、`Wake`（`:942`）、`deleteIf`（`:1126`）、`EvaluateIdle` / `retired`（`:1547-1618`）：box 标记的联动、闸门，以及 `Delete` 对已删除 orb 的幂等。
- `internal/threads/agent.go` — `command`（`:1158`）与 `commandWithAttachments`（`:1407`）：停机的 box 必须在这里同步拒绝；`wake`（`:1642`）是后台的，返回不了错误。
- `internal/portal/portal.go` + `handshake.go` — `serve`（`:152`，epoch 在 `:191` 取）、`authorize`（`:242`）、`AuthHandler`（`handshake.go:43`）：app 分支要在解析 app **之前**取 epoch；握手里硬编码的 `t=&p=` 与 `s.base.URL(p.code, p.port)` 要换成 `p.root`。
- `internal/server/server.go` — `:219`（ctl 方法登记，`s.Boxes` 必须在它之前）、`:253`（Hub）、`:410`（`frameSrc`）、`:443`（Portal）。
- `web/src/pages/HomePage.tsx:203-222,282-294,347-362,404-432,568,806-859` — 目标对象、seed、胶囊文案、提交与兜底错误、弹层选中态，要一起改。

## Verification

每个步骤提交前跑对应包的测试；阶段结束跑全量。

```bash
make gen && make lint-spec                      # 阶段 0；提交后再跑 make check-gen（它比的是工作区与已提交的生成物）
go vet ./... && go test ./...                   # 需要 sqlc v1.31.1；CI 同款，设 ORYN_REQUIRE_SANDBOX=1
go test ./internal/server/ -run 'TestE2EM23' -count=1 -v
cd web && bun run check && bun run test
cd web && bun run shots:all --only box,home-executor-boxes --check     # 定向；--only 是前缀匹配，--check 才跑溢出断言
cd web && bun run shots:all --check                                    # 改了 PickerPopover 的 Option，全量再过一遍
cd web && UI_AUDIT_PHASE=before UI_AUDIT_WEB=$PWD npx playwright test e2e/ui-audit/boxes.e2e.ts   # 新界面的 before 取自首版
cd web && npx playwright test e2e/ui-audit/boxes.e2e.ts
cd web && bun e2e/ui-audit/inventory.ts         # 9 个新界面全 ✓、覆盖对账「一致」；它要求全部界面的产物都在
# 干净检出上（docs/ui-audit/shots/ 是空的）先把其余界面补齐，再跑上面三条：
#   UI_AUDIT_PHASE=before UI_AUDIT_WEB=/root/Projects/Ling/.oryn-ui-baseline/web npx playwright test e2e/ui-audit --grep-invert 'boxes\.'
#   npx playwright test e2e/ui-audit --grep-invert 'boxes\.'
cd web && bun run accept:m23                    # 需要真实开发栈，见 m4-accept.ts 头部的前置条件
```

新行为的判据：`TestE2EM23` 的 R3（匿名请求 `Host: hello.<portal_domain>` 得到 `v1`）证明「构建 → 传输 → 激活 → 入口」整条链；R5 证明失败回滚且不留半成品；R8 证明回滚换的是真进程；R9 证明线程与文件面板都落在 app 目录；R10–R11 证明停机在三个入口都被同步拒绝、启动后恢复；`TestE2EM23AlwaysOn` 证明常驻；`TestE2EM23Revive` 证明掉线恢复起的是新进程。浏览器一侧 B3 证明预览能在内嵌帧里渲染（私有的那一半若按预案降级，以 B3′ 为准），B5 证明执行位置不会被首屏偏好吞掉。

真机走查（开发栈，需要已配置 `portal_domain` 的 `dev/orynd.yaml` 与一个在线的 Docker provider）：

1. `make web && make build && make image`，重启 `./bin/orynd --config dev/orynd.yaml` 与 provider。
2. 浏览器打开「更多 → Boxes」：看到介绍页；「新建 Box」里填 `workbench` 创建，页面进入 box 页，状态从「启动中」变「运行中」。
3. 新开一个普通线程，说「写一个显示当前时间的小网页并部署到 workbench 这台 box」。agent 应读到 `oryn-boxes` 技能，写 `.oryn/box.yaml`，跑 `oryn boxes deploy --on workbench --name clock`；box 页的 APPS 出现 `clock` 与它的地址，无痕窗口能直接打开。
4. box 页点 `clock` 行的「新线程」，确认后首屏执行位置显示 `workbench · clock`；发一条「看看这个应用的日志」，线程页头显示同一标签，文件面板的根在 app 目录。
5. 「设为私有」后无痕窗口被带去登录；「停止 Box」后地址显示不可用页；「启动 Box」后恢复。
6. `docker restart <box 的容器名>`：两分钟内 box 页回到「运行中」，地址恢复。

## Assumptions & contingencies

- **部署只在线程的 orb 里可用。** 设备令牌 CLI 可以用其余全部子命令，但不能 `deploy`（release 经 orb link 取走，REST 没有上传入口）。要从 CI 或本机部署，另加一个 `POST /api/v1/boxes/{box}/apps/{app}/releases` 原始上传接口，接到 `CommitDeploy` 的传输一步即可。
- **不要求「已推送」。** Amp 要求分支已推送；这里部署的是当前 `HEAD` 提交（未提交的改动不带，并给出提示），因为 release 不经 Git 远端传输，且没有远端的草稿仓库也应当能部署。
- **app 之间没有隔离。** 所有 app 的服务与 box 上的线程都以同一个带免密 sudo 的 `user` 运行（未选「每 app 独立 Linux 用户」）。不可信的 app 放到单独的 box。release 里的符号链接原样保留，可以指向 release 之外——解包不会写到外面，但 app 运行时读得到同一台机器上的任何文件。
- **box 的数据就是那台 orb 的磁盘。** Docker 下是容器可写层，没有卷；`docker rm` 或删 box 即丢。已有 box 不会随镜像升级更新 orbd：新能力靠 hello 特性协商，缺 `box` 特性的 orbd 得到 `box_unsupported`。
- **全自动线程不能用 box。** 由 GitHub 触发、项目级定时等入口建的 orb（`orbs.automation = 1`）调用 `box.call` 一律 `forbidden`；这些线程可能由不可信输入驱动。
- **orb 里的 agent 不能删 box。** 删除只能由主人在页面、设备令牌 CLI，或经助手的确认卡。
- **激活中的部署不能取消。** 最长 10 分钟后要么成功要么回滚；控制面在此期间重启，回来后按 orbd 的实际状态补完。box 在那一刻连不上的话，这次部署一直显示「部署中」直到 box 回来或被删除。
- **orb 内的 `exec` 不能中途取消**，上限 30 分钟；要可取消、要更长就用设备令牌 CLI（流式，至多 1 小时）。
- **app 需要 `portal_domain`。** 没配置时 Boxes 的其余部分照常，但 app 没有地址，`ORYN_DEPLOYMENT_URL` 为空串，页面给出提示。
- **预览标签名是 `oryn-preview`**，不认 `amp-preview`。
- **Firecracker**：box 占 `limits.max_vms` 的名额；磁盘大小是全局的 `disk_gb`，不能按 box 调。
- 预案——`Revive` 的 `ready → waking`：若线程服务对这条从未出现过的边处理不对（在途线程卡住、状态不回落），改成两步：`transition(ready → stopped, {"reason": "link lost"})` 后调 `Wake`；两条都是它已经处理过的边。`TestE2EM23Revive` 是判据。
- 预案——`git sparse-checkout` 在某个镜像的 git 上行为不符（release 里出现别的 app 的目录，或 `git status` 不干净；本计划没有实测过这组 `--no-cone` 模式，`TestE2EM23Source` 是判据）：`root != ""` 的带源码 release 改为不带 `.git`（只打包 `<root>`、根目录文件与 `include`），并在该 app 上跳过 `box_changes` 检查；CLI 打一行说明「单仓子目录 app 的 release 不含 Git 历史」。
- 预案——release 传输里 orbd 的 `GET /fs/file` 不给 `Content-Length`（分块响应）：`CommitDeploy` 只在 `ContentLength ≥ 0` 时比对，`PUT` 的长度始终取 `CommitParams.SizeBytes`，由 box 一侧的字节数与 SHA-256 校验兜底。
- 未实测——私有 app 的内嵌预览：`frame-src` 放行之后，浏览器在内嵌帧里是否照常收发 app 主机的 `__Host-oryn_portal` Cookie（开发环境的 `localhost` 与 `<app>.localhost` 互为跨站；既有的 M4 验收只验过顶层导航与带设备令牌的 iframe，没有验过只凭 Cookie 的 iframe）。B3 里私有的那一半是判据。不过时：先跑 B4 并记下结果，再降级——`AppPreview` 只对公开 app 启用（私有 app 的徽标不是触发器），步骤 4.1 里给 `frame-src` 加 `'self'` 的改动连同它的测试与文档一并撤回，验收改看 B3′；其余功能不受影响。
