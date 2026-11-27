# 设置页需后端 / 壳层新增的四项：接口设计

状态：设计稿，只出接口，不含实现（主人 2026-11-27 决定，见 [`settings-ia.md`](settings-ia.md) 开头的状态行与第 4 节）。
范围：Passkey 管理与恢复码重新生成、控制面运维参数、通知免打扰时段、桌面开机自启与启动时隐藏。
约定：
- 依据统一写成 `path:line`，行号以 2026-11-27 的工作树为准。
- 标 [推断] 的是没有在代码里直接核实的判断。
- 新增 REST 路径照现有风格放在 `/api/v1` 下。改动 `schema/openapi.yaml` 之后要跑 `make gen`，它会重新生成 `internal/apigen/apigen.gen.go`、`internal/store/db/*` 与 `web/src/api/schema.d.ts`（`Makefile:14-27`）。下面各节的文件清单不再重复列这几个生成文件。
- 每个新 operationId 都要登记进全局助手的 API 策略表 `internal/coord/apipolicy.go`。`TestAPIPolicyComplete`（`internal/coord/apipolicy_test.go:10`）要求所有操作都在表里；表里没有的操作默认拒绝（`apipolicy.go:14`）。

---

## 1. Passkey 管理与恢复码重新生成

**用户故事：** 我换了手机、新买了安全密钥，或者丢了一台设备，想在设置页里自己增删 Passkey、给它们改名，并在恢复码快用完或可能泄露时重新生成一套，不用登录服务器跑命令。

### 1.1 现状

- **接口：** 现有 auth 端点只有 `setup/begin|finish`、`login/begin|finish`、`recovery`、`logout`、`app-session`（`schema/openapi.yaml:114-232`）。`Me` 里只有一个 `passkeyCount`（`schema/openapi.yaml:8233-8250`），由 `internal/api/auth.go:175-179` 用 `CountPasskeys` 填写。
- **数据表：**
  - `passkeys` 的列为 id（credential id，BLOB）、public_key、sign_count、transports、credential（完整 JSON）、name、created_at、last_used_at（`internal/store/migrations/0001_init.sql:15-24`）。
  - `recovery_codes` 只有 code_hash 和 used_at 两列（`0001_init.sql:26-29`）。
  - `web_sessions` 不记录会话由哪把 Passkey 登录、何时验证过（`0001_init.sql:32-39`；`device_id` 列由 `0013_app_sessions.sql:4` 加入）。
- **已有但没人调用的查询：** `RenamePasskey`、`DeletePasskey`（`internal/store/queries/auth.sql:28-32`）在 Go 代码里没有任何调用方。`ListPasskeys`（`auth.sql:19-20`）只在 `loadOwner` 里用到（`internal/auth/webauthn.go:119-140`）。
- **注册仪式：** `BeginSetup` 有两条分支。
  - 带 `ost_` 令牌：首次初始化。
  - 带恢复会话：给已有所有者追加一把 Passkey。这条分支会用 `WithExclusions` 排除已注册的凭据（`webauthn.go:243-261`），并统一要求可发现凭据、UV preferred、不要 attestation（`webauthn.go:262-270`）。
  - `FinishSetup` 在一个事务里写入 Passkey（`webauthn.go:388-393`），同时签发一个新的完整会话（`webauthn.go:394`）。只有首次初始化会生成恢复码（`webauthn.go:362-384`）。
- **恢复码：**
  - 生成：10 个，80 位熵（`internal/auth/recovery.go:17-27`）。
  - 兑换：作废一个，换来 15 分钟、只能调用 `auth/setup/*` 的恢复会话（`recovery.go:53-76`；`internal/auth/auth.go:36-37`、`auth.go:101-105`）。
- **会话种类与权限：**
  - 会话只有 `full` 和 `recovery` 两种（`auth.go:43-44`）。
  - `requireWeb` 只接受完整网页会话，排除设备令牌、恢复会话和 App 会话（`internal/api/devices.go:42-53`）。
  - `admin create-web-session` 可以不经 Passkey 直接签发一个完整会话（`cmd/orynd/admin.go:73-88`，`auth.go:268`）。
- **前端：** 用 `@simplewebauthn/browser` 的 `startRegistration` / `startAuthentication`（`web/src/pages/AuthPages.tsx:5-10`、`61-91`、`170-190`）。

### 1.2 设计要点

1. **注册复用 `BeginSetup` 的「已有所有者」分支，不复用端点。**
   - 把 `webauthn.go:243-270` 抽成内部函数 `beginRegistration(user, name)`：排除已有凭据，同样的认证器要求。
   - 把 `webauthn.go:340-393` 的「解析 → `CreateCredential` → `markUsed` → `CreatePasskey`」抽成 `storeCredential(q, cred, name)`。
   - 新增 `BeginAddPasskey` / `FinishAddPasskey`，复用这两个函数。区别有三点：鉴权来自完整会话，而不是恢复 Cookie；不签发新会话；仪式类型用新的 `ceremonyAdd`，不能拿 setup 仪式的 id 来 finish（`takeCeremony` 按 kind 校验，`webauthn.go:159`）。
   - `/auth/setup/*` 保持原语义，仍在 `publicPaths` 里（`internal/api/router.go:47-53`）。新端点走鉴权中间件。
2. **增设「近期重新验证」（step-up）。**
   - 依据：网页会话 30 天滑动续期（`auth.go:34-35`），被盗的 Cookie 本身就足以长期驻留。如果它还能给自己加一把 Passkey 或换掉恢复码，就能把真正的主人锁在门外。
   - 做法：在 `web_sessions` 上加 `verified_at`，在新的 `/auth/reauth/*` 做一次非可发现的断言（`allowCredentials` = 所有者的全部 Passkey）后刷新它。
   - 敏感操作要求 `now - verified_at ≤ 5 分钟`，否则返回 403 `reauth_required`。5 分钟与仪式 TTL 一致（`webauthn.go:28`）。
   - 需要重新验证：新增、删除、重新生成恢复码。改名和列表不需要。
3. **不能删最后一把。** 在事务里先 `CountPasskeys` 再删；只剩 1 把时返回 409 `last_passkey`。恢复码不能替代 Passkey：恢复会话只能注册，不能登录（`auth.go:101-105`）。
4. **删除 Passkey 时，连带吊销用它登录的会话。**
   - `web_sessions` 加 `passkey_id`，由 `FinishLogin` / `FinishSetup` 写入（`webauthn.go:394`、`475`）。
   - 删除时吊销 `passkey_id` 相同、且不是当前会话的会话，沿用 `revocations.publish(KindWeb, …)`（`webauthn.go:403` 的做法）。
   - App 会话没有 `passkey_id`（来自设备令牌），不受影响。
5. **重新生成恢复码**：复用 `newRecoveryCodes` 与 `DeleteAllRecoveryCodes` / `InsertRecoveryCode`（`webauthn.go:377-383`）。旧码一次性全部作废，明文只在这次响应里出现一次。
6. **谁能调用：**
   - 完整网页会话与 App 会话可以调用（`p.Kind == web && !p.Recovery`）。
   - 设备 Bearer 令牌（CLI）一律 403：CLI 无法完成 WebAuthn。
   - 全局助手在 `apipolicy.go` 里登记为 `deny(denyHuman)`，与现有 auth 端点同组（`apipolicy.go:294-295`）。
   - 与 `requireWeb` 不同，这里放行 App 会话，因为敏感操作已经由 step-up 兜底。

### 1.3 API 形状（OpenAPI 片段）

```yaml
  /api/v1/auth/passkeys:
    get:
      tags: [auth]
      operationId: listPasskeys
      summary: 列出所有者的 Passkey（按创建时间）
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/PasskeyList' } } } }
        default: { $ref: '#/components/responses/Error' }
  /api/v1/auth/passkeys/begin:
    post:
      tags: [auth]
      operationId: addPasskeyBegin
      summary: 开始追加 Passkey（需近期重新验证，否则 403 reauth_required）
      security: [{ cookieAuth: [], csrfHeader: [] }]
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/AddPasskeyBeginRequest' } } }
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/WebAuthnCeremony' } } } }
  /api/v1/auth/passkeys/finish:
    post:
      tags: [auth]
      operationId: addPasskeyFinish
      summary: 完成追加；不签发新会话
      security: [{ cookieAuth: [], csrfHeader: [] }]
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/AuthLoginFinishRequest' } } }  # 同为 {ceremonyId, credential}
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/Passkey' } } } }
  /api/v1/auth/passkeys/{passkeyId}:
    parameters:
      - { name: passkeyId, in: path, required: true, schema: { type: string, description: 'credential id 的无填充 base64url' } }
    patch:
      tags: [auth]
      operationId: renamePasskey
      security: [{ cookieAuth: [], csrfHeader: [] }]
      requestBody:
        content:
          application/json:
            schema: { type: object, required: [name], properties: { name: { type: string, minLength: 1, maxLength: 80 } } }
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/Passkey' } } } }
    delete:
      tags: [auth]
      operationId: deletePasskey
      summary: 删除（需近期重新验证）；最后一把 → 409 last_passkey；吊销用它登录的其它会话
      security: [{ cookieAuth: [], csrfHeader: [] }]
      responses:
        '204': { description: 已删除 }
  /api/v1/auth/reauth/begin:
    post:
      tags: [auth]
      operationId: reauthBegin
      summary: 对当前会话做一次 Passkey 断言（allowCredentials = 所有者全部凭据）
      security: [{ cookieAuth: [], csrfHeader: [] }]
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/WebAuthnCeremony' } } } }
  /api/v1/auth/reauth/finish:
    post:
      tags: [auth]
      operationId: reauthFinish
      security: [{ cookieAuth: [], csrfHeader: [] }]
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/AuthLoginFinishRequest' } } }
      responses:
        '200':
          content:
            application/json:
              schema: { type: object, required: [verifiedAt, validUntil], properties: { verifiedAt: { $ref: '#/components/schemas/TimestampMs' }, validUntil: { $ref: '#/components/schemas/TimestampMs' } } }
  /api/v1/auth/recovery-codes:
    get:
      tags: [auth]
      operationId: getRecoveryCodeStatus
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/RecoveryCodeStatus' } } } }
    post:
      tags: [auth]
      operationId: regenerateRecoveryCodes
      summary: 作废全部旧码并生成 10 个新码（需近期重新验证）；明文只返回这一次
      security: [{ cookieAuth: [], csrfHeader: [] }]
      responses:
        '200':
          content:
            application/json:
              schema: { type: object, required: [recoveryCodes], properties: { recoveryCodes: { type: array, items: { type: string }, minItems: 10, maxItems: 10 } } }

components:
  schemas:
    Passkey:
      type: object
      required: [id, name, createdAt, transports, backupEligible, backedUp, current]
      properties:
        id: { type: string, description: credential id（无填充 base64url） }
        name: { type: string }
        createdAt: { $ref: '#/components/schemas/TimestampMs' }
        lastUsedAt: { $ref: '#/components/schemas/TimestampMs' }
        transports: { type: array, items: { type: string } }
        backupEligible: { type: boolean, description: 取自 credential JSON 的 Flags（可同步的 Passkey） }
        backedUp: { type: boolean }
        current: { type: boolean, description: 当前会话由这把登录 }
    PasskeyList:
      type: object
      required: [items, reauthValidUntil]
      properties:
        items: { type: array, items: { $ref: '#/components/schemas/Passkey' } }
        reauthValidUntil: { allOf: [{ $ref: '#/components/schemas/TimestampMs' }], nullable: true, description: 'null = 需要先 reauth' }
    AddPasskeyBeginRequest:
      type: object
      properties: { name: { type: string, maxLength: 80 } }
    RecoveryCodeStatus:
      type: object
      required: [total, remaining]
      properties:
        total: { type: integer }
        remaining: { type: integer }
        generatedAt: { $ref: '#/components/schemas/TimestampMs' }
```

另外给 `Me` 增加 `recoveryCodesRemaining: integer`（`schema/openapi.yaml:8250` 旁）。账户页的提醒（剩余 ≤ 3 个时提示重新生成）不必为此单独请求一次。

错误码：
- `reauth_required`（403）
- `last_passkey`（409）
- `forbidden`（403，设备令牌或恢复会话调用）
- `not_found`（404）
- WebAuthn 校验失败沿用 `invalid_request` / `unauthorized` 与 `webauthnMsg`（`webauthn.go:482-491`）

限流：reauth 与 add 复用 `LimitLogin` / `LimitSetup`（`internal/auth/ratelimit.go:30-31`）。

### 1.4 数据模型 / 迁移（`0064_passkey_manage.sql`）

```sql
-- +goose Up
ALTER TABLE web_sessions ADD COLUMN verified_at INTEGER;          -- 最近一次 Passkey 验证（登录 / 注册 / reauth）；NULL = 从未（admin 签发、App 会话）
ALTER TABLE web_sessions ADD COLUMN passkey_id  BLOB;             -- 登录所用 Passkey；不设外键，删除时由代码吊销
ALTER TABLE recovery_codes ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;  -- 0 = 迁移前生成，未知
-- +goose Down
ALTER TABLE recovery_codes DROP COLUMN created_at;
ALTER TABLE web_sessions DROP COLUMN passkey_id;
ALTER TABLE web_sessions DROP COLUMN verified_at;
```

存量会话的 `verified_at` 保持 NULL，第一次做敏感操作时要求 reauth，不追溯。

### 1.5 文件清单

| 文件 | 改动 |
|---|---|
| `schema/openapi.yaml` | 第 1.3 节的路径与 schema；`Me.recoveryCodesRemaining` |
| `internal/store/migrations/0064_passkey_manage.sql` | 新建，见第 1.4 节 |
| `internal/store/queries/auth.sql` | `CreateWebSession` 带 `verified_at` 和 `passkey_id`（`auth.sql:46-48`）；新增 `TouchWebSessionVerified`、`ListWebSessionsByPasskey`、`RecoveryCodesGeneratedAt`；`InsertRecoveryCode` 带 `created_at` |
| `internal/auth/webauthn.go` | 抽出 `beginRegistration` / `storeCredential`；新增 `BeginAddPasskey`、`FinishAddPasskey`、`BeginReauth`、`FinishReauth`（断言逻辑参照 `FinishLogin` `webauthn.go:430-479`，含 `CloneWarning` 检查 `:456`）；`FinishLogin` / `FinishSetup` 写入 `passkey_id` |
| `internal/auth/passkeys.go`（新） | `ListPasskeys`、`RenamePasskey`、`DeletePasskey`（计数 + 吊销）、`RequireRecentAuth(p, now)` |
| `internal/auth/recovery.go` | `RegenerateRecoveryCodes`、`RecoveryStatus` |
| `internal/auth/auth.go` | `createSession`（`:252`）加参数；`Principal` 带 `VerifiedAt`、`PasskeyID`（在 `:150` 处从 `ws` 填写） |
| `internal/auth/appsession.go` | App 会话的 `verified_at` 保持 NULL（只核对，不一定改）[推断] |
| `internal/api/auth.go` | 新处理器；`me()` 填写 `recoveryCodesRemaining`（`:154-181`） |
| `internal/coord/apipolicy.go` | 新 op 全部 `deny(denyHuman)`（`:295` 那一组） |
| `internal/api/auth_test.go`、`internal/auth/*_test.go` | 覆盖：最后一把、reauth 过期、吊销、旧恢复码失效 |
| `web/src/api/`（新 `auth-queries.ts`） | 列表、改名、删除、reauth、恢复码 hooks |
| `web/src/lib/webauthn.ts`（新） | 从 `AuthPages.tsx:33-36` 抽出 `unwrapOptions`，与注册、断言封装共用 |
| `web/src/pages/settings/AccountSettings.tsx`（IA 重排后的 `account` 分区文件）[推断文件名] | Passkey 列表、新增、改名、删除；恢复码卡片（剩余数、重新生成、一次性展示并复制） |
| `web/src/mock/server.ts` | mock 端点（`passkeyCount` 现在在 `:1574`） |
| `web/src/i18n/locales/{zh-CN,en}.json` | 文案 |
| `docs/deploy.md` | 「首次启动」一段（`:106`）补一句：可在设置里重新生成恢复码 |

### 1.6 风险与未决问题

- **壳内 WebView 的 WebAuthn 支持不一（[推断]，未在本仓库验证）：** Windows 的 WebView2 支持；macOS 的 WKWebView 需要关联域或浏览器权限；WebKitGTK 不支持。远程页面的 origin 就是 `public_url`，RP 校验本身没有问题。建议前端在 `PublicKeyCredential` 不可用时只显示列表，并提供「在浏览器中打开」（`host_open_external`）。
- **step-up 窗口：** 窗口定为 5 分钟，是否允许连续操作期间续期，待定。
- **认证器名称：** 列表是否展示 AAGUID 对应的厂商名，需要内置 AAGUID 表，本轮不做。
- **会话管理：** 「当前登录会话」列表和远程登出没有纳入本节。有了 `passkey_id` 和 `verified_at` 之后，再做这项的代价会很小。
- **CLI 恢复：** `admin create-web-session` 签发的会话 `verified_at` 为 NULL，所以用它做敏感操作也要先 reauth。真正丢光所有 Passkey 时，要靠恢复码或 `setup-token`（`cmd/orynd/admin.go:25`）。`setup-token` 目前只用于首次初始化，「全丢光」是否需要一个 `admin reset-passkeys`，待主人决定。

### 1.7 前端位置

个人 → **账户与设备 `account`**（`settings-ia.md` 第 2 节第 1 行）。Passkey 和恢复码放在「设备与配对」之上，单独一组。

---

## 2. 控制面运维参数（orb 空闲回收、数据保留期、磁盘上限）

**用户故事：** 我想在设置页里调整 orb 多久没人用就冻结或停机、统计数据保留多久、仓库镜像和快照最多占多少盘，改完立即生效，不用 SSH 上去改 `orynd.yaml` 再重启。

### 2.1 现状

- **加载：**
  - `orynd.yaml` 在启动时读一次：`cmd/orynd/main.go:38-52` 调用 `config.LoadOrynd`。解码是严格模式（`internal/config/config.go:642-648` `KnownFields`），读完跑一遍 `Validate`（`config.go:377-473`）。
  - 结果作为值拷贝传给 `server.New`（`internal/server/server.go:160`），再拆成各服务自己的 Config 值。
  - 没有任何 API 能读写这些参数。只读暴露的只有 `StatsMeta.retention`（`schema/openapi.yaml:13694`、`13711-13724`）。
- **本节涉及的字段及其缺省语义：**

| 键 | 类型 / 缺省 | 定义 | 校验（沿用） |
|---|---|---|---|
| `idle.agent_idle` | 5m | `config.go:42-47`、`:304-309` | `>0`（`config.go:401-403`） |
| `idle.user_idle` | 20m | 同上 | 同上 |
| `idle.stop_after_frozen` | 24h | 同上 | 同上 |
| `idle.wake_on_focus` | true | 同上 | — |
| `stats.retention.samples` | 720h，0 = 永久 | `config.go:129-137` | 0 或 ≥24h（`config.go:424-436`） |
| `stats.retention.events` | 8760h | 同上 | 同上 |
| `stats.retention.usage` | 0（永久） | 同上 | 同上 |
| `repos.max_mirror_gb` | 0 → 5 | `config.go:111-121` | ≥0（`config.go:462-470`） |
| `repos.min_free_gb` | 0 → 2 | 同上 | 同上 |
| `repos.max_lfs_cache_gb` | 0 → 2 | 同上 | 同上 |
| `snapshots.max_total_gb` | 40 | `config.go:144-158` | ≥1（`config.go:443-445`） |
| `snapshots.max_snapshot_gb` | 0 → min(10, total) | `config.go:154-158`、`:167-173` | 0 或 1..total（`config.go:446-448`） |

  注意三套「0」的语义不同：保留期的 0 表示永久，`repos.*` 的 0 表示用缺省值（`docs/deploy.md` 第 2 节「三个数值写 0 都按缺省值处理」），`max_snapshot_gb` 的 0 表示自动。API 必须能区分「未覆盖（null）」和「覆盖为 0」。
- **不纳入可热更新的字段：** `repos.git` 是可执行文件路径，经 API 写入等于远程指定执行程序；它也只在 `repos.New` 里定位一次（`internal/repos/repos.go:165`）。`listen`、`public_url`、`data_dir`、`orb_tls` 等必须重启才能生效，也不进 API。

### 2.2 存储：推荐 DB 覆盖层，不回写 YAML

推荐**数据库覆盖层**，理由：

1. **orynd 写不了 `/etc/oryn/orynd.yaml`。** 部署文档以 `sudo -u oryn orynd --config /etc/oryn/orynd.yaml …` 运行（`docs/deploy.md:106`、`:485`），配置文件在 `/etc` 下属于 root [推断：文件属主由安装方式决定，仓库里没有写死]。回写 YAML 就得放宽 `/etc` 的权限。
2. **YAML 常由配置管理工具（Ansible、Nix、1Panel 编排）生成**，回写的内容会在下一次下发时被冲掉；`yaml.v3` 的整文件往返也会丢注释和排版 [推断]。
3. **热更新无论如何都要在内存里传播**，回写 YAML 只解决持久化，不解决生效。覆盖层与已有的单例设置表同构（`assistant_settings`：`0041_assistant_m19.sql:5`；`memory_settings`：`0036_memory.sql:6`），备份 `oryn.db` 时也会一并带上。

**优先级：** 覆盖层 > YAML > 内建缺省值。
- YAML 是部署时的基线，界面里的改动是之后的明确意图，所以覆盖层优先。
- GET 同时返回 YAML 值和覆盖值，界面标出「已在设置中覆盖（YAML 为 X）」，并提供「恢复为 YAML」（PATCH 传 null）。
- **启动时的冲突处理：** 主人可能改了 YAML，使已有覆盖值和新的 YAML 值放在一起通不过 `Validate`（例如 YAML 把 `max_total_gb` 调小，覆盖里的 `max_snapshot_gb` 比它大）。这时逐个丢弃违规的覆盖键，记 Warn 日志，用 YAML 值启动，不拒绝启动。理由：运维参数不该让控制面起不来。

```sql
-- 0065_config_overrides.sql
-- +goose Up
CREATE TABLE config_overrides (
    key        TEXT    PRIMARY KEY,   -- YAML 点路径，白名单见 internal/opsconf（如 'idle.agent_idle'）
    value      TEXT    NOT NULL,      -- JSON 标量：时长存毫秒整数，GB 存整数，布尔存 true/false
    updated_at INTEGER NOT NULL
);
-- +goose Down
DROP TABLE config_overrides;
```

选键值表而不是单例宽表：以后要放开 `pool.*`、`schedule.*` 时不必每次加列。键名必须在 Go 白名单里，白名单以外的键在启动时忽略并记日志。

### 2.3 热更新机制与读取点改造

新包 `internal/opsconf`：
- 持有 `base config.Orynd`（YAML 加载结果）和 `cur atomic.Pointer[config.Orynd]`（叠加覆盖层后的值）。
- `Current() config.Orynd` 无锁读取。
- `Apply(ctx, patch)` 流程：组装候选值 → `config.Validate()` → 事务内写 `config_overrides` → `cur.Store` → 依次调用订阅者。
- 各服务不再在构造时拷贝这些值，改为持有取值函数。具体读取点如下（逐个已核实）：

| 字段 | 现在的读取点 | 改法 | 生效时机 |
|---|---|---|---|
| `idle.agent_idle`、`idle.user_idle` | `internal/orbs/service.go:1620-1625` `idleReady`；`user_idle` 另在 `internal/orbs/orbhttp.go:275-279` `TouchUserCoalesced` 里算合并窗口 | `orbs.Config.Idle`（`service.go:95`）改为 `Idle func() config.Idle`；`server.go:229` 传入 `ops.Idle` | 下一个评估周期（默认 30s，`service.go:263-265`、`:1428`） |
| `idle.stop_after_frozen` | `internal/orbs/service.go:1642-1644` `idleFrozenTooLong` | 同上 | 同上 |
| `idle.wake_on_focus` | `internal/threads/agent.go:2125`；配置字段在 `internal/threads/service.go:61-62`，由 `server.go:234` 注入 | `WakeOnFocus func() bool` | 立即 |
| `stats.retention.*` | `internal/stats/retention.go:16` `Sweep`；`internal/stats/service.go:150-156` `Meta`；配置字段在 `stats/service.go:42-43`，由 `server.go:412` 注入 | `Retention func() config.StatsRetention` | `Meta` 立即反映；清理在下一次整点 Sweep（`stats/service.go:29`、`:127-129`），**刻意不立即触发**，留出撤回的窗口 |
| `repos.max_mirror_gb` | `internal/repos/sync.go:247`、`:408`（`cfg.MaxBytes`） | `repos.Config` 的三个字节数改为取值函数，`cmp.Or` 的缺省换算从 `server.go:419-421` 和 `repos.go:149-151` 挪进取值函数 | 下一次拉取或复查（每 5s，`repos.go:161`） |
| `repos.min_free_gb` | `sync.go:211`、`:404`；`smarthttp.go:119`、`:228`；`lfsfetch.go:595`；`lfsserver.go:282`、`:369` | 同上 | 立即（每次检查都现读） |
| `repos.max_lfs_cache_gb` | 只在打开时读一次：`repos.go:174` `openLFSCache` → `lfscache.go:25` 的 `max` 字段；淘汰逻辑在 `lfscache.go:202-203` | `lfsCache` 加 `SetMax(n)`：持 `mu` 改 `max` 后立即 `evictLocked(0)`；作为 opsconf 的订阅者调用 | 立即（调小时同步淘汰） |
| `snapshots.max_total_gb` | `internal/orbs/projsnap.go:641-650` `reserveSnapshotQuota`（调用点 `:348`、`:405`） | `orbs.Config.Snapshots`（`service.go:113`）里的两个上限改为取值函数；`enabled`、`max_age` 等其余字段这一轮仍是静态值 | 下一次建快照或刷新快照；调小**不会**立刻退役已有快照 [推断：`reserveSnapshotQuota` 只有这两个调用点] |
| `snapshots.max_snapshot_gb` | `projsnap.go:467`（`MaxSnapshotBytes`，`config.go:167-173`） | 同上 | 同上 |

`internal/api` 的 `Server.Config`（`server.go:102`）里也有一份配置拷贝。已用 grep 核实：`internal/api` 没有读上表这些字段（只用 `PublicURL` 等），不用改。

`cmd/orynd` 的子命令（`admin`、`coordinator check` 等，`cmd/orynd/main.go:60-66`）拿到的是纯 YAML 配置，不叠加覆盖层。它们都不读上表字段，可以接受。

### 2.4 API 形状

```yaml
  /api/v1/server/ops-settings:
    get:
      tags: [settings]
      operationId: getOpsSettings
      summary: 控制面运维参数：生效值、YAML 值与覆盖情况
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/OpsSettingsState' } } } }
    patch:
      tags: [settings]
      operationId: patchOpsSettings
      summary: 部分覆盖；字段给 null = 删除覆盖、回落 YAML；未出现的字段不变。整体先过 config.Validate，失败 → 400 invalid_config（message 为 Validate 的合并错误）
      security: [{ cookieAuth: [], csrfHeader: [] }]
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/OpsSettingsPatch' } } }
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/OpsSettingsState' } } } }

components:
  schemas:
    OpsSettings:            # 全部必填（effective / yaml 两份都是完整值）
      type: object
      required: [idle, statsRetention, repos, snapshots]
      properties:
        idle:
          type: object
          required: [agentIdleMs, userIdleMs, stopAfterFrozenMs, wakeOnFocus]
          properties:
            agentIdleMs: { type: integer, minimum: 1 }
            userIdleMs: { type: integer, minimum: 1 }
            stopAfterFrozenMs: { type: integer, minimum: 1 }
            wakeOnFocus: { type: boolean }
        statsRetention: { $ref: '#/components/schemas/StatsRetention' }   # 复用 samplesMs / eventsMs / usageMs
        repos:
          type: object
          required: [maxMirrorGb, minFreeGb, maxLfsCacheGb]
          properties:
            maxMirrorGb: { type: integer, minimum: 0, description: 0 = 缺省 5 }
            minFreeGb: { type: integer, minimum: 0, description: 0 = 缺省 2 }
            maxLfsCacheGb: { type: integer, minimum: 0, description: 0 = 缺省 2 }
        snapshots:
          type: object
          required: [maxTotalGb, maxSnapshotGb]
          properties:
            maxTotalGb: { type: integer, minimum: 1 }
            maxSnapshotGb: { type: integer, minimum: 0, description: 0 = min(10, maxTotalGb) }
    OpsSettingsPatch:       # 与 OpsSettings 同形，所有字段可选且 nullable: true
      type: object
      properties:
        idle:
          type: object
          properties:
            agentIdleMs: { type: integer, nullable: true }
            # …其余字段同理
        # statsRetention / repos / snapshots 同理
    OpsSettingsState:
      type: object
      required: [effective, yaml, overridden, updatedAt]
      properties:
        effective: { $ref: '#/components/schemas/OpsSettings' }
        yaml: { $ref: '#/components/schemas/OpsSettings' }
        overridden:
          type: array
          description: 被覆盖的键（YAML 点路径，如 idle.agent_idle）
          items: { type: string }
        updatedAt: { allOf: [{ $ref: '#/components/schemas/TimestampMs' }], nullable: true }
```

- 时长统一用毫秒整数，与 `StatsRetention.samplesMs` 一致（`internal/stats/service.go:152-155`）。服务端换算回 `config.Duration` 之后再交给 `Validate`。
- **校验：** 完全沿用 `config.Orynd.Validate()`。把「YAML 基线 + 现有覆盖 + 本次 patch」组装成完整的 `Orynd` 再整体校验，`max_snapshot_gb ≤ max_total_gb` 这类跨字段约束就自动覆盖到了。API 层只额外拒绝白名单以外的键和类型错误。
- **权限：**
  - 网页会话（含 App 会话）可读可写，不接受恢复会话。
  - 设备 Bearer 令牌只能 GET [建议]。
  - 全局助手：`getOpsSettings` 登记 `opRead`，`patchOpsSettings` 登记 `deny(denyHuman)`。理由：运维参数影响成本与数据删除，不应由一条网页注入触发。
  - 这是单所有者系统，「只有 owner」等价于「不是 CLI 设备、不是助手」。
- **审计：** 每次 PATCH 用 `slog.Info` 记录被改的键和新旧值（`log_level` 缺省为 info，`cmd/orynd/main.go:54`）。

### 2.5 文件清单

| 文件 | 改动 |
|---|---|
| `schema/openapi.yaml` | 第 2.4 节的路径与 schema；`StatsMeta.retention` 的说明改为「生效值」 |
| `internal/store/migrations/0065_config_overrides.sql`、`internal/store/queries/config.sql`（新） | 建表；`ListConfigOverrides`、`UpsertConfigOverride`、`DeleteConfigOverride` |
| `internal/opsconf/opsconf.go`（新） | 白名单、加载与叠加、`Apply`、订阅 |
| `internal/config/config.go` | 不改语义；可能导出一个「把覆盖值套到 `Orynd` 上」的辅助函数，给 opsconf 用 |
| `internal/server/server.go` | 打开存储后构造 opsconf（`:160` 之后），把取值函数传给 orbs（`:228-231`）、threads（`:233-234`）、stats（`:411-413`）、repos（`:417-421`）；注册 LFS 缓存上限的订阅 |
| `internal/orbs/service.go`、`orbhttp.go`、`projsnap.go` | 第 2.3 节列出的读取点 |
| `internal/threads/service.go`、`agent.go` | `WakeOnFocus` 改为取值函数 |
| `internal/stats/service.go`、`retention.go` | `Retention` 改为取值函数 |
| `internal/repos/repos.go`、`sync.go`、`smarthttp.go`、`lfsfetch.go`、`lfsserver.go`、`lfscache.go` | 上限改为取值函数；`lfsCache.SetMax` |
| `internal/api/ops_settings.go`（新） | GET / PATCH 处理器 |
| `internal/coord/apipolicy.go` | 登记两个 op（`settings` 组） |
| 各包 `_test.go` | 现有测试构造的是 `Config{Idle: …}` 之类的值，要改成传函数（编译期即可发现） |
| `web/src/api/ops-settings-queries.ts`（新）、设置页的运维分区文件 | 表单：时长选择、GB 数字框、「恢复为 YAML」、缩短保留期时二次确认 |
| `web/src/components/stats/OrbDrawer.tsx` | 保留期说明加一条链接，指到设置项（现在读的是 `StatsMeta.retention`，审计记作 `:32-79`） |
| `web/src/mock/server.ts`、`i18n` | mock 与文案 |
| `docs/deploy.md` 第 2 节 | 写明「这些键可在设置里覆盖，覆盖优先；删除覆盖回落 YAML」 |

### 2.6 风险与未决问题

- **调小空闲时长会造成一次性批量冻结：** 下一个 30s 周期里会冻结一批 orb。前端提示「将影响 N 台当前空闲的 orb」需要后端给出计数，本轮不做。
- **缩短保留期不可逆：** 下一次整点 Sweep 就会删数据。要不要再加一道服务端保护（例如 PATCH 缩短保留期必须带 `confirm: true`），待定。现方案只靠前端二次确认。
- **快照上限调小后不会主动退役已有快照**，只在下一次建快照时生效。要不要在 PATCH 后对每个 provider 触发一次配额回收，待定。
- **设置页缺用量数据：** 设置页上最好显示数据盘剩余空间、镜像总量和各 provider 的快照占用。`freeBytes` 已有（`internal/repos/sync.go:501`）；`providers_overview` 已经汇总了快照字节数（`internal/api/providers_overview.go:337-340`）。是否把这些放进 `OpsSettingsState`，待定。
- **能否锁死某个字段：** 主人能否在 YAML 里把某个字段锁死、禁止界面覆盖（例如在 YAML 加 `ops_locked: [...]`），待定。默认不提供。
- **后续扩展：** 同一套机制可以扩展到 `pool.*`、`schedule.*`、`snapshots.enabled/max_age/...`、`log_level`。本轮不纳入。

### 2.7 前端位置

主人已确认（2026-11-27）：在 `settings-ia.md` 第 2 节「工作区」组新增 **控制面 `server`**，放在「算力 `compute`」之后，页内分三组：空闲回收、数据保留、磁盘上限。理由是这些参数影响整个实例，不是「我和这台设备」。注册表项和页面随本节后端接口一起实现，接口未上线前不加入口。

---

## 3. 通知免打扰时段

**用户故事：** 晚上 23:00 到早上 8:00 我不想被手机、Telegram 或 ntfy 吵醒；这段时间里发生的事，第二天早上集中看到，等待我回答的请求也不能丢。

### 3.1 现状

- **渠道级设置：** 渠道只有 `enabled`、`events`、`locale` 三个维度（`schema/openapi.yaml:14488-14506`；表结构 `internal/store/migrations/0011_share_notify.sql:29-41`）。推送订阅（webpush、gateway）完全没有逐条设置（`0011_share_notify.sql:44-56`）。
- **生成：** `record` 在一个事务里落库通知，并为每个符合条件的渠道和每个推送订阅各排一条投递，`next_attempt_at = now`（`internal/notify/notify.go:609-662`；渠道过滤在 `:624-627`，订阅在 `:636-646`）。
- **投递：**
  - `DeliverDue` 按 `next_attempt_at` 取出到期的投递，每批 ≤50 条（`internal/notify/deliver.go:122-187`）。
  - **超过 1 小时的通知直接记作 failed "expired"**（`deliver.go:66`、`:137-140`）。按现状，任何超过 1 小时的延后都会被它判成过期。
  - 状态只有 `pending|sent|failed`（`internal/store/migrations/0055_browser_relay.sql:211-224` 的 CHECK）。
- **桌面壳的系统通知不走投递队列：**
  - 页面收到实时 `notification` 事件后，窗口在后台时调用 `host.notify`（`web/src/state/notifications.ts:194-217`）。
  - 浏览器里没有 Web Push 时，用 `Notification` 弹窗（`notifications.ts:219-224`）。
- **系统通知的来源：**
  - 预算告警：`stats/budget.go:191`。按「每周期每级只告警一次」去重（`internal/stats/retention.go:37-41` 的注释）。
  - 后台研究暂停：`internal/research/settings.go:98`，kind 为 budget。
  - GitHub 类 error 通知：`internal/github/checks.go:263`、`listen.go:367`、`triggers.go:791` 等。
- **时区校验：** 已有 `stats.LoadLocation`，只接受 IANA 名，拒绝 `Local`（`internal/stats/stats.go:64-76`）。预算自带一个 `timezone` 字段（`internal/stats/budget.go:51-55`、`:114-119`）。

### 3.2 设计决定

1. **作用范围：账号级，渠道可以选择不受约束。**
   - 免打扰描述的是「人在睡觉」，应当同时作用于所有外部渠道和所有推送订阅。推送订阅本来就没有逐条设置，渠道级设计覆盖不到它们。
   - 渠道新增 `ignoreQuietHours`（缺省 false），给「接到自动化系统的 webhook」这类不打扰人的渠道用。
   - 应用内收件箱、toast、未读数一律不受影响。
2. **时区：** 设置里自带 IANA 时区，用 `stats.LoadLocation` 校验。首次保存时由前端填入 `Intl.DateTimeFormat().resolvedOptions().timeZone`。不和预算时区合并，原因见第 3.6 节。
3. **时段与跨午夜：**
   - `start`、`end` 用 `"HH:MM"`（0–1439 分钟）。
   - `start < end` 表示当天内的时段；`start > end` 表示跨午夜，例如 `23:00–08:00`；`start == end` 拒绝（400 `invalid_quiet_hours`），全天静默请直接关掉渠道。
   - 可选 `days`（0=周日 … 6=周六，缺省为每天）。跨午夜的时段按**开始那一天**归属：周五 23:00 开始的时段延续到周六 08:00。
   - 时刻用 `time.Date(y, m, d, h, min, 0, 0, loc)` 计算。夏令时跳过的时刻由 Go 规范化顺延 [推断：Go 的 `time.Date` 文档语义]。
4. **免打扰期间的事件：推荐「延后，到点后去重投递」，不丢弃。**
   - 不丢弃的理由：`input` 通知代表 agent 正在等人回答（Telegram 里还能直接点按钮作答，`notify.go:1-2`），丢了就要靠用户自己翻收件箱。
   - 做法：免打扰期间排的投递 `next_attempt_at = 时段结束`，并记录 `deferred_until`。
   - 到点释放时（在 `DeliverDue` 里）按顺序处理：
     - 通知已读（`read_at`）或者 input 已解决（`resolved_at`）：跳过，记 `skipped`。用户已经在应用里看过了，早上不必再响一遍。
     - 同一线程的多条 `done` / `error`：只投最新一条，其余记 `skipped`。这样把早上的一阵轰炸压成「每个线程一条」。
     - 未解决的 `input`：逐条投递，保留作答按钮。
   - 过期判断改为从 `max(created_at, deferred_until)` 起算 1 小时（`deliver.go:137`），否则延后的投递全部会被判过期。
   - 不另造一种「汇总消息」：现有各发送方都只认单条通知的 `Message`（`deliver.go:21-41`），而每个线程一条已经足够收敛。
5. **绕过：可配置的 `bypassKinds`，缺省 `["budget"]`。**
   - budget 缺省绕过：它按周期、按级别去重，量很小；而且它是夜里无人值守的定时任务烧钱时唯一的信号。
   - error 缺省不绕过：GitHub CI、触发器类 error 可能量很大，夜里也没法处理。
   - input 缺省不绕过：延后之后不会丢。
   - 「发送测试通知」（`internal/api/notifications.go:191`）永远绕过。
6. **桌面壳和浏览器的系统通知：** 生成通知时在库里记 `quiet = 1`，并随 `Notification` 下发。页面在 `quiet` 时不调用 `host.notify` / `showSystemNotification`，但仍然进收件箱、更新托盘计数。不在客户端自行判断时段，避免各端时钟和时区不一致。

### 3.3 API 形状

```yaml
  /api/v1/settings/quiet-hours:
    get:
      tags: [notifications]
      operationId: getQuietHours
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/QuietHoursStatus' } } } }
    put:
      tags: [notifications]
      operationId: putQuietHours
      summary: 整体替换；start == end、时区非法、days 越界 → 400 invalid_quiet_hours
      security: [{ cookieAuth: [], csrfHeader: [] }, { deviceBearer: [] }]
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/QuietHours' } } }
      responses:
        '200': { content: { application/json: { schema: { $ref: '#/components/schemas/QuietHoursStatus' } } } }

components:
  schemas:
    QuietHours:
      type: object
      required: [enabled, start, end, timezone]
      properties:
        enabled: { type: boolean }
        start: { type: string, pattern: '^([01]\d|2[0-3]):[0-5]\d$' }
        end: { type: string, pattern: '^([01]\d|2[0-3]):[0-5]\d$', description: 'start > end = 跨午夜' }
        timezone: { type: string, description: IANA 时区名 }
        days: { type: array, items: { type: integer, minimum: 0, maximum: 6 }, uniqueItems: true, description: '缺省 = 每天；跨午夜按开始日' }
        bypassKinds:
          type: array
          items: { $ref: '#/components/schemas/NotificationKind' }
          description: 缺省 ["budget"]
    QuietHoursStatus:
      allOf:
        - $ref: '#/components/schemas/QuietHours'
        - type: object
          required: [active]
          properties:
            active: { type: boolean, description: 此刻是否在免打扰中 }
            nextChangeAt: { $ref: '#/components/schemas/TimestampMs' }
```

另外三处小改：
- `NotificationChannelInput`、`NotificationChannelPatch`、`NotificationChannel`（`schema/openapi.yaml:14488`、`:14507`、`:14537`）各加 `ignoreQuietHours: boolean`。
- `Notification`（`:14325`）加 `quiet: boolean`。
- `NotificationDeliveryStatus`（`:14306-14308`）加 `skipped`，`NotificationDelivery` 加 `deferredUntil`。

全局助手：`getQuietHours` 登记 `opRead`，`putQuietHours` 登记 `opConfirm`，与渠道增删改同级（`internal/coord/apipolicy.go:274`）。

### 3.4 数据模型 / 迁移（`0066_quiet_hours.sql`）

- 新单例表 `notification_quiet_hours`：
  - `id INTEGER PRIMARY KEY CHECK (id = 1)`
  - `enabled INTEGER NOT NULL DEFAULT 0`
  - `start_min INTEGER NOT NULL`、`end_min INTEGER NOT NULL`
  - `timezone TEXT NOT NULL`
  - `days TEXT NOT NULL DEFAULT '[]'`
  - `bypass_kinds TEXT NOT NULL DEFAULT '["budget"]'`
  - `updated_at INTEGER NOT NULL`
  - 没有行时表示未开启。
- `ALTER TABLE notification_channels ADD COLUMN ignore_quiet_hours INTEGER NOT NULL DEFAULT 0;`
- `ALTER TABLE notifications ADD COLUMN quiet INTEGER NOT NULL DEFAULT 0;`
- `notification_deliveries` 要**重建表**：状态的 CHECK 加上 `'skipped'`，同时新增 `deferred_until INTEGER`。SQLite 不能修改 CHECK，要照 `0055_browser_relay.sql:8-115` 的 keep 表做法：保留数据、重建索引、写好 Down。

### 3.5 文件清单

| 文件 | 改动 |
|---|---|
| `schema/openapi.yaml` | 第 3.3 节 |
| `internal/store/migrations/0066_quiet_hours.sql` | 第 3.4 节 |
| `internal/store/queries/notify.sql` | 免打扰设置读写；`InsertNotificationDelivery` 带 `deferred_until`；`InsertNotification` 带 `quiet`；释放时用的「同线程是否有更新的 done/error」查询 |
| `internal/notify/quiet.go`（新） | 时段计算（`active(now)`、`nextEnd(now)`）、校验、跨午夜与 `days` 判定，配表驱动测试 |
| `internal/notify/notify.go` | `record`（`:609-662`）读设置，决定 `quiet` 和每条投递的 `next_attempt_at` |
| `internal/notify/deliver.go` | 过期改按 `deferred_until` 起算（`:137`）；释放时的跳过与去重规则；新增 `statusSkipped` |
| `internal/notify/channels.go` | 渠道的 `ignoreQuietHours` 读写（`CreateChannel :363`、`UpdateChannel :423`、`channelAPI :558`） |
| `internal/api/notifications.go` | 两个新处理器；测试发送绕过 |
| `internal/stats/stats.go` | 无改动，复用 `LoadLocation`。如果不想让 notify 依赖 stats，就把它挪到小包里 [实现时定] |
| `internal/coord/apipolicy.go` | 登记 op |
| `web/src/state/notifications.ts` | `quiet` 时不弹系统通知（`:194-224`） |
| `web/src/pages/settings/NotificationsSettings.tsx` | 「免打扰」卡片：开关、起止时间、时区、星期、绕过类型；渠道表单加「不受免打扰影响」 |
| `web/src/api/notify-queries.ts`、`web/src/mock/server.ts`、`i18n` | hooks、mock、文案 |
| `internal/notify/*_test.go`、`NotificationsSettings.test.tsx` | 跨午夜、夏令时、释放去重、过期起算 |

### 3.6 风险与未决问题

- **释放瞬间的突发：** 释放时刻集中到期的投递可能超过一批 50 条。`deliverLoop` 在满批时会立即继续（`deliver.go:96-98`），不会卡住，但第三方渠道可能触发限流；现有退避会兜住（`deliver.go:73`）。
- **时区要不要统一成账号时区：** 预算和免打扰各自带一个时区，可能出现不一致。要不要提升为账号级 `owner.timezone`，留给以后决定。
- **推送订阅要不要按设备单独设免打扰**（例如只让手机静默、电脑照常提醒）：本方案不支持。
- **`input` 是否也该缺省绕过：** 有人希望 agent 被阻塞时立刻知道。缺省取「不绕过」，可在 `bypassKinds` 里勾选。

### 3.7 前端位置

个人 → **通知与预算 `notifications`**（`settings-ia.md` 第 2 节第 4 行）。「免打扰」卡片放在渠道列表之上，渠道表单里加一个「不受免打扰影响」开关。

---

## 4. 桌面开机自启与启动时隐藏

**用户故事：** 我希望开机后 Oryn 自动在托盘里待命，能收到通知、显示未读数，但不要每次开机都弹出窗口。

### 4.1 现状

- **配置：** 壳配置 `Config` 只有 `server_url`、`device_id`、`token_store`、`shortcut`、`close_to_tray`、`auto_update`、`lang`（`web/src-tauri/src/config.rs:41-54`；缺省值见 `:56-68`，结构体带 `#[serde(default)]`，旧文件缺字段能照常加载）。
- **设置接口：** 页面可设置的字段由 `DesktopSettings` / `SettingsPatch` 定义（`web/src-tauri/src/host.rs:517-533`），`host_set_settings` 负责应用（`host.rs:779-810`）。前端类型在 `web/src/lib/host.ts:66-70`、`:79-95`，界面在 `web/src/pages/settings/DesktopSettings.tsx:219-232`。
- **关窗到托盘：**
  - 关窗时，如果 `close_to_tray()` 为真就 `prevent_close` 并藏窗口（`host.rs:413-421`）。
  - `close_to_tray()` 要求托盘可用（`host.rs:207-210`）；托盘在主窗口建好之后才构建（`web/src-tauri/src/lib.rs:260` 建窗口，`:298-304` 建托盘）。
- **主窗口一律可见：** `create_main` 没有设置 `visible`（`web/src-tauri/src/webview.rs:155-157`、`:290`）。窗口状态插件不保存「可见」（`host.rs:306-311`）。
- **重复启动：** 第二次启动会把窗口叫到前台（`lib.rs:470-481`）。macOS 的 Dock 重新打开同样会显示窗口（`lib.rs:518`）。
- **命令行参数：** 只认 `--server`、`--quit`、`--smoke`；argv 里带深链接时，其余参数全部忽略（`lib.rs:57-98`）。
- **没有自启实现：** 仓库里搜不到 autostart（审计 `settings-audit-scattered.md` 第 3 节）；`Cargo.toml` 的桌面依赖里也没有（`web/src-tauri/Cargo.toml` 中 `[target.'cfg(not(any(target_os = "android", target_os = "ios")))'.dependencies]` 一段）。

### 4.2 设计决定

1. **插件选型：`tauri-plugin-autostart`。** 按仓库惯例钉精确版本：`tauri-plugin-autostart = "=2.7.0"`（2.x 的最新稳定版，`cargo info` 核实过；3.0 仍是 alpha）。只加在桌面依赖段，与 `tauri-plugin-window-state` 等放在一起。
   - 用 Rust 侧的 `ManagerExt::autolaunch().enable() / disable() / is_enabled()`，**不给远程页面开放插件的 JS 权限**（`autostart:allow-*`）。理由：远程能力只放行自家命令，`host_set_settings` 已有权限 `allow-host-set-settings`（`web/src-tauri/permissions/autogenerated/host_set_settings.toml:4`），沿用这一条通道，不扩大远程页面的能力面。
   - 注册方式：`tauri_plugin_autostart::Builder::new().args(["--autostart"]).app_name("Oryn").macos_launcher(MacosLauncher::LaunchAgent).build()`。按 `lib.rs:123-125` 的插件顺序注释，放在 updater 之后；冒烟模式（`--smoke`）不注册。
2. **「是否自启」以操作系统为准，不写进 `config.json`。** 用户可能在系统设置里把它关掉（macOS 登录项、Windows 任务管理器的启动页、Linux 删掉 `.desktop` 文件），所以每次 `host_info` 都现查 `is_enabled()`。
3. **「启动时隐藏」写进 `config.json`。** 新字段 `start_hidden: bool`，缺省 false。它**只在被自启拉起时生效**，即 argv 里带 `--autostart`；用户手动打开时永远显示窗口。
4. **与 `close_to_tray` 的关系：**
   - 两者相互独立，但「启动时隐藏」依赖托盘：`start_hidden` 实际生效 = 由自启拉起 && `start_hidden` && 托盘可用。托盘不可用时（例如 GNOME 没有装 AppIndicator 扩展）照常显示窗口，与 `close_to_tray()` 的兜底逻辑相同（`host.rs:207-210`）。
   - 不要求 `close_to_tray` 必须开着：开机隐藏启动后，用户从托盘打开窗口再点关闭，由 `close_to_tray` 决定藏起来还是退出，行为本来就一致。
   - 界面上「启动时隐藏」只在「开机自启」打开时可用，并加一句说明：「需要系统托盘；没有托盘时照常显示窗口」。
5. **窗口实现：**
   - `create_main` 增加参数 `visible: bool`，构建时 `.visible(visible)`（`webview.rs:155`）。
   - 建好托盘之后（`lib.rs:298-304`），如果是隐藏启动而托盘不可用，就调用 `host::show_main`。
   - `Desktop.visible` 的初值按实际可见性设置（`host.rs:113`），之后照常由 `refresh_visibility` 维护。
   - 隐藏启动期间会话照常建立（`lib.rs:314-318`），页面加载后通知走 `host.notify`（`web/src/state/notifications.ts:194`），托盘计数照常更新，这正是隐藏启动的目的。
6. **桥接版本：** `BRIDGE` 从 11 升到 12（`host.rs:35`，`host.ts` 第 13 行注释规定新增能力要加一）。页面只在 `bridge ≥ 12` 时显示这两个开关。

### 4.3 命令 / 类型形状

Rust（`web/src-tauri/src/config.rs`、`host.rs`）：

```rust
// config.rs: Config 新增
/// 由开机自启拉起（argv 含 `--autostart`）时不显示主窗口；托盘不可用时忽略。
pub start_hidden: bool,          // Default: false

// host.rs
pub struct DesktopSettings {
    pub shortcut: String,
    pub close_to_tray: bool,
    pub auto_update: bool,
    /// 操作系统里当前是否登记了自启；查询失败为 None（见 autostart_error）。
    pub autostart: Option<bool>,
    pub start_hidden: bool,
}
pub struct SettingsPatch {
    pub shortcut: Option<String>,
    pub close_to_tray: Option<bool>,
    pub auto_update: Option<bool>,
    pub autostart: Option<bool>,     // → app.autolaunch().enable()/disable()，不落 config.json
    pub start_hidden: Option<bool>,  // → update_config
}
pub struct DesktopInfo {
    // …现有字段
    #[serde(skip_serializing_if = "Option::is_none")]
    pub autostart_error: Option<String>,   // 与 shortcut_error 同理：enable/disable/is_enabled 的错误
}

// lib.rs: Args 新增
/// `--autostart`：由开机自启拉起（插件注册的启动参数）。
pub autostart: bool,
```

TypeScript（`web/src/lib/host.ts:66-95`）：

```ts
export interface DesktopSettings {
	shortcut: string;
	closeToTray: boolean;
	autoUpdate: boolean;
	/** bridge ≥ 12；null = 无法读取系统自启状态（见 autostartError）。 */
	autostart?: boolean | null;
	/** bridge ≥ 12：开机自启拉起时不显示窗口（需要托盘）。 */
	startHidden?: boolean;
}
export interface DesktopInfo {
	// …现有字段
	autostartError?: string | null;
}
```

`setSettings(patch: Partial<DesktopSettings>)` 的签名不变。旧壳会忽略未知字段：`SettingsPatch` 没有 `deny_unknown_fields`（`host.rs:526-533`）。

### 4.4 各平台行为差异

| 平台 | 自启方式（auto-launch 后端） | 注意 |
|---|---|---|
| macOS | LaunchAgent：`~/Library/LaunchAgents/Oryn.plist` 指向 `.app` 内的可执行文件 | 不需要「自动化」授权；macOS 13+ 会弹出一次「已添加后台项目」，在「系统设置 › 登录项」里显示为后台项目 [推断]。用户挪动 `.app` 后 plist 会失效，建议启动时若 `is_enabled()` 就再 `enable()` 一次，刷新路径。不选 AppleScript：它要「自动化 → System Events」授权，会弹 TCC 窗口 |
| Windows | `HKCU\…\CurrentVersion\Run` 的 `Oryn` 值 | 按用户登记，不需要管理员权限。卸载不会自动删除这个值（需要 NSIS 卸载钩子，未决）。用户在任务管理器里禁用时写的是 `StartupApproved`，`is_enabled()` 能否反映这一点 [推断，待实测] |
| Linux | `~/.config/autostart/Oryn.desktop`，`Exec` 带 `--autostart` | 插件对 AppImage 用 `$APPIMAGE` 的路径（插件源码 `tauri-plugin-autostart-2.7.0/src/lib.rs:259-264`），deb/rpm 用安装路径。GNOME 没有托盘时「启动时隐藏」自动失效（见第 4.2 节第 4 点）。Wayland 下隐藏启动没有额外问题 [推断] |
| Android / iOS | 不适用 | 这两项只存在于 `#[cfg(desktop)]` 的类型里，`host.ts` 的 `desktop` 在移动壳上为 null |

### 4.5 文件清单

| 文件 | 改动 |
|---|---|
| `web/src-tauri/Cargo.toml` | 桌面依赖段加 `tauri-plugin-autostart = "=2.7.0"`；`Cargo.lock` 随之更新 |
| `web/src-tauri/src/lib.rs` | 注册插件（`:147-153` 桌面段，非冒烟）；`Args.autostart` 与解析（`:57-98`）；`create_main` 传入可见性（`:260`）；托盘不可用时兜底显示（`:298-304`）；自启路径自愈（启动时若已启用就重新 `enable`） |
| `web/src-tauri/src/webview.rs` | `create_main(app, remote, visible)`（`:144`、`:155`）；移动端调用点（`lib.rs:397`）传 true |
| `web/src-tauri/src/config.rs` | `start_hidden` 字段与缺省值（`:43-68`）；测试（`:225-250`） |
| `web/src-tauri/src/host.rs` | `DesktopSettings` / `SettingsPatch` / `DesktopInfo`（`:517-544`）；`desktop_info`（`:546-570`）现查 `is_enabled`；`host_set_settings`（`:779-810`）；`BRIDGE = 12`（`:35`）与上方的版本注释；`Desktop.visible` 初值（`:113`）；形状测试（`:888-900` 附近） |
| `web/src/lib/host.ts` | 类型（`:66-95`）；文件头的 bridge 说明 |
| `web/src/lib/host.test.ts` | 新字段的透传 |
| `web/src/pages/settings/DesktopSettings.tsx` | 两个开关（`:219-232` 附近），`bridge < 12` 时隐藏；显示 `autostartError` |
| `web/src/pages/settings/DesktopSettings.test.tsx` | 覆盖开关和旧桥接 |
| `web/src/i18n/locales/{zh-CN,en}.json` | `desktop.autostart`、`desktop.startHidden`、说明（与 `desktop.closeToTray` 同组，`:5137`） |
| `docs/`（桌面相关文档，如有）[推断] | 写明自启的落点和卸载残留 |

### 4.6 风险与未决问题

- **卸载残留：** Windows 的 `Run` 值和 macOS 的 LaunchAgent 在卸载后都会留下，指向已不存在的路径，开机时静默失败。要不要加 NSIS `installerHooks` / 卸载时清理，待定。
- **开发构建误注册：** 开发构建（`bun run dev`）用的是 target 目录里的可执行文件，在开发时打开自启会登记一个开发路径。建议在 `debug_assertions` 下把开关显示为禁用 [建议]。
- **单实例的边界情况：** 自启拉起的实例被单实例插件转交时（用户已经手动打开），`on_second_instance` 会显示窗口（`lib.rs:480`）。对 `--autostart` 是否应该不显示，待定；建议与 `Args.quit` 一样单独判断，`--autostart` 只记日志、不 `show_main`。
- **macOS 的后台项目提示：** LaunchAgent 在 macOS 13+ 的「后台项目」提示文案由系统决定，无法定制。

### 4.7 前端位置

个人 → **桌面应用 `desktop`**（仅桌面壳；`settings-ia.md` 第 2 节第 5 行）。放在「关闭窗口时隐藏到托盘」紧上方，顺序为：开机自启 → 启动时隐藏（缩进，依赖上一项）→ 关闭到托盘。
