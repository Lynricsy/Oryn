# M28 — 官方推送网关 + iOS APNs + Android gms 经网关 FCM：实施计划

> 编号按现状取 M28（`docs/plan-m27.md` 是最后一份），迁移号取 `0058`（`0057_provider_host_metrics.sql` 是最后一个）。动手时若被占用则顺延，并同步改本文里的 `m28`、`0058`。
> 本文自包含；行号只作提示，动手前按符号重读。工作区由多个会话共用，提交时只带本步骤改的行。
> **进度记录**：Step 1 的 `internal/fcm` 抽取已先行完成（`fcm.Client.Send(ctx, acct, Message) Result`，形状与下文 Step 1 的 Produces 略有不同：账户由调用方传入、结果为 `Result{Retry, RetryAfter, Unregistered, Err}`，401 不在 Send 内重试而由调用方按 Retry 重排；后续切片以已落地的签名为准）。


Goal: 官方签名的 iOS 包与 gms 包连任意自部署 orynd，配对后自动经 `https://push.oryn.best` 收到端到端加密推送；orynd 只保留 Web Push 与 gateway 两条推送路径。

Approach: 同仓库新增 `cmd/oryn-push-gateway`，只转发 RFC 8291 密文。orynd 以 Ed25519 主机密钥签名调用网关；手机以 P-256 设备密钥签名，并经 challenge 推送证明令牌归属后申请授权。登记编排、签名、账本全在 Rust 壳里，原生只提供令牌、接收端密钥与 challenge 事件。iOS 加 Notification Service Extension 解密。

Requirements: `local://oracle-m28-push-gateway-brief.md`；协议以 Step 1 定稿后的 `docs/protocol.md`「官方推送网关」为准。

Rollback point:
- Step 1 之后可停：只多出新包与文档，orynd 行为不变。
- S-B 含迁移 0058，会删除 `fcm` / `apns` 订阅。已部署后回滚要先 goose down 到 0057；被删的订阅不恢复，App 下次进入时重登记。
- S-B 与 S-C 必须同一次发版。只上 S-B，现有 gms 直发 FCM 用户会失去推送；只上 S-C，新 App 连旧 orynd 显示 `gateway_unsupported`。两种情况 UnifiedPush 与 Web Push 都不受影响。

## Global constraints
- Go 1.27.1，不新增 Go 依赖。APNs 用标准库 HTTP/2 与 `crypto/ecdsa`；yaml.v3、goose、modernc sqlite 已在 `go.mod`。
- `@tauri-apps/cli` 钉 `2.12.6`；tauri crate `=2.12.5` 不动。
- 推送载荷绝不明文。网关与壳的日志不含推送令牌、`e`、nonce、设备令牌。
- 错误体一律 `{code, message}`。
- `gen/apple` 不入库。NSE target 名不得含 `_iOS`：tauri 按这个子串找要改签名的配置（`tauri-cli/src/mobile/ios/mod.rs` 的 `synchronize_project_config`）。
- 文件独占，见各步 Files。切片不跑构建和测试；各步 Accept 由主 agent 在全部切片落地后执行。
- 工作区里未提交的 `docs/ui-audit/inventory.md` 不属于本任务。

## Non-goals
- 网关部署、DNS、真 APNs 联调：另派 agent 与用户完成。
- iOS 一触作答按钮与 iOS withdraw：后续。
- NSE 离线补拉（D4）、授权列表界面与多主机（D3）、硬件密钥（D2）。
- App Group、Share Extension、Universal Links。
- orynd 直发 FCM / APNs：已决定删除，自编 App 的人自建网关。

## Assumptions
- A1 `tauri ios init` 按 tauri 目录解析 `bundle.iOS.template` 的相对路径（未验证；只读到 build 流程会 `set_current_dir`）。影响 Step 0、5；失效走探针分支 P2。
- A2 运行时给 tao 的 `AppDelegate` 类加方法后，UIKit 会回调远程通知注册结果（未验证）。影响 Step 0、5；失效走 P1。
- A3 tauri 工程名取自 Cargo 包名，即 target 为 `oryn-desktop_iOS`（未验证）。影响 Step 5；脚本一律用通配，不写死名字。
- A4 `xcode-27` runner 的模拟器支持 `xcrun simctl push` 并会调起 NSE。**已证伪**（run 45443153455，iOS 27.2 模拟器）：`simctl push` 是本地投递、不调起 NSE；临时签名副本（签入 `aps-environment`）在模拟器上启动不了。按 P3 预案：探针 `PUSH_NSE_GATE=soft`、只跑不签名一轮；NSE 的解密与内容构造由单元测试硬门禁（见下文 P3）；NSE 被调起与共享钥匙串留给 Step 7 真机。P1（`apns.register_failed`，钩子被调到）、P2（appex 在包里、版本一致）、challenge 与自检均已通过。
- A5 只有 `notification_deliveries` 外键引用 `push_subscriptions`（0055 的注释如此；0056、0057 未读）。影响 Step 3；执行前跑 `grep -rn "REFERENCES push_subscriptions" internal/store/migrations`。
- A6 已启用 UnifiedPush 的 gms 老用户升级后自动改走网关 FCM（D5 = 迁移）；仍可在设置里手动切回 UnifiedPush。影响 Step 4。
- A7 App 同一时间只配对一台服务器，所以一把设备密钥至多一个有效授权。影响 Step 2、4。

## 草案修订记录（Step 1 同步进 protocol.md）
- R1 主机密钥改存 `<data_dir>/push-gateway.key`：32 字节 Ed25519 种子、0600，与 `vapid.key` 同约定；网关启用时在启动期生成。
- R2 设备密钥改为 Rust 软件 P-256，存放方式同设备令牌（D2）。
- R3 `POST /v1/push` 去掉 `kind`，改为可选 `withdraw: true`；APNs 一律 `apns-priority: 10`；APNs 授权收到 withdraw 直接回 202、不联系 APNs。
- R4 `e` 上限由 3800 改为 3872 字符。明文 2800 字节加 103 字节密文开销，base64url 后是 3871（`internal/notify/fcm.go:51-54` 的注释已算过）。
- R5 删除 413 后「去掉 body 重发」：orynd 发送前已用 `pushPayloadLimit(m, 2800)` 降级。
- R6 删除 `PUT /v1/devices/self/token`、`GET /v1/devices/self/grants`、`DELETE /v1/devices/self/grants` 与 `label`。`POST /v1/grants` 以 challenge 证明过的令牌覆盖原令牌，并吊销同一设备密钥名下指向其它主机的授权。
- R7 `/v1/info` 的 `fcm` 只有 `projectId`：服务账号 JSON 里没有 sender id。
- R8 删除 orynd 的礼貌清理 `DELETE /v1/grants/{id}`：订阅行删除后 orynd 不再发送，授权 90 天未用自然过期。主机签名的 DELETE 仍保留在网关接口里。
- R9 订阅的 `endpoint` 定义为 `<origin>/v1/grants/<grant>`，不加列。
- R10 新增错误码：网关 `challenge_invalid`（409）、`forbidden`（403）；orynd `gateway_disabled`、`gateway_unreachable`（502）。
- R11 关闭推送改为尽力吊销加待办重试，不再要求网关 DELETE 成功才算关闭：离线时也必须能关。
- R12 iOS 的 NSE 补拉与 App 启动补拉不做（D4）；「设置 → 通知」的授权列表不做（D3）。
- R13 网关随 `oryn-server` 包发二进制与 systemd unit，不单独发镜像：仓库没有任何服务端镜像流程。
- R14 APNs 占位文案改为 `{"title":"Oryn","body":"New notification"}`；NSE 一启动先按系统语言换成本地化占位。
- R15 challenge 的 APNs 推送用 `apns-expiration: 0`，不离线存储。
- R16 iOS 前台收到远程推送不展示，与 Android 一致；前台提示由页面的 `host_notify` 负责，否则会出两条。

## 冻结契约

### K1 签名（代码：`internal/pushproto`，Step 1）
- 主机头：`Oryn-Host-Key`（base64url 无填充的 32 字节公钥）、`Oryn-Timestamp`（Unix 毫秒十进制）、`Oryn-Signature`（base64url 的 64 字节 Ed25519 签名）。
- 设备头：`Oryn-Device-Key`（base64url 的 65 字节未压缩点）、`Oryn-Timestamp`、`Oryn-Signature`（base64url 的 ECDSA P-256 / SHA-256 DER）。
- 两种身份头恰有其一；同时出现或带查询串 → 400 `malformed`。
- 签名输入：`"oryn-push-gw/1\n" + METHOD + "\n" + origin + path + "\n" + timestamp + "\n" + hex(sha256(body))`，hex 小写，origin 为网关配置的 `public_origin`、无尾斜杠。
- 时钟差超过 300000 ms → 401 `clock_skew`；其它验签失败 → 401 `bad_signature`。请求体上限 8 KiB。
- `Collapse(grant, tag) = base64url(sha256(grant + tag))` 的前 22 个字符。

### K2 网关 HTTP 接口
|方法 路径|签名|请求 → 应答|
|---|---|---|
|`GET /healthz`|无|`{ok:true, version}`|
|`GET /v1/info`|无|`{v:1, apns?:{topic, environments}, fcm?:{projectId}}`|
|`POST /v1/challenges`|设备|`{channel:"apns"\|"fcm", appId, environment?, token}` → 202 `{challengeId}`|
|`POST /v1/grants`|设备|`{challengeId, nonce, hostKey}` → 201 `{grant, createdAt}`|
|`DELETE /v1/grants/{id}`|设备或主机|→ 204|
|`POST /v1/grants/verify`|主机|`{grant}` → 204|
|`POST /v1/push`|主机|`{grant, id, collapse?, withdraw?, e}` → 202|

- 格式：`grant`、`challengeId`、`nonce` 都是 `^[A-Za-z0-9_-]{22}$`（128 位随机）；`id` 是 `^[A-Za-z0-9_-]{1,80}$`；`collapse` 是 `^[A-Za-z0-9_-]{1,64}$`；`e` 至多 3872 字符。
- 令牌：APNs 为 `^[0-9a-f]{64,200}$`；FCM 用 `fcm.ValidToken`。
- `appId` 必须等于配置的 `apns.topic` 或 `fcm.app_id`；通道未配置、或上游判定令牌不属于本 App → 409 `app_mismatch`。
- challenge 存内存，5 分钟有效、一次性，只认发起它的设备密钥；否则 409 `challenge_invalid`。
- `POST /v1/grants` 在一个事务里：吊销该设备密钥指向其它主机的有效授权；同一对（设备，主机）已有有效授权 → 更新令牌并返回原 id；否则新建。
- `DELETE`：授权不存在或已吊销 → 204；存在但签名者不是绑定的设备或主机 → 403 `forbidden`。
- `verify` 与 `push`：授权不存在或已吊销 → 410 `grant_revoked`；主机不符 → 403 `host_mismatch`。
- 上游报令牌失效（FCM `UNREGISTERED`；APNs 410，或 400 的 `BadDeviceToken` / `DeviceTokenNotForTopic`）→ 吊销授权并回 410 `device_unregistered`。
- 其余：413 `too_big`、429 `rate_limited`（带 `Retry-After`）、502 `upstream`、503 `unavailable`。
- challenge 推送：
  - FCM：`data {kind:"gateway_challenge", challengeId, nonce}`，HIGH，ttl 300s。
  - APNs：`{"aps":{"alert":{"title":"Oryn","body":"Verifying push notifications"}},"kind":"gateway_challenge","challengeId":…,"nonce":…}`，`apns-push-type: alert`、priority 10、expiration 0。
- 通知推送：
  - FCM：`{"message":{"token","data":{"v":"2","id","e"},"android":{"priority":"HIGH","ttl":"86400s"}}}`；withdraw 时 priority 为 `NORMAL`。
  - APNs：`{"aps":{"alert":{"title":"Oryn","body":"New notification"},"mutable-content":1,"sound":"default"},"v":"2","id":…,"e":…}`；头 `apns-topic`、`apns-push-type: alert`、`apns-priority: 10`、`apns-expiration` 为 24 小时后、`apns-collapse-id` 为 `collapse`。
- 限额初值：每授权每分钟 60、每天 2000；每主机公钥每分钟 600；每 IP 每小时 30 次 challenge。

### K3 orynd API（文本冻结，S-B 落地）
- 新增 `GET /api/v1/push/gateway`，`operationId: getPushGateway`，鉴权写法同 `getVapidKey`。200 返回 `PushGateway{v: 1, origin, hostKey}`，三项必填；`push.gateway.url` 为空 → 404 `gateway_disabled`。
- `PushSubscriptionInput`：`kind` 枚举改为 `[webpush, gateway]`；删除 `token`；新增 `gateway`（string，1–255）与 `grant`（pattern `^[A-Za-z0-9_-]{22}$`）。`kind: gateway` 必须带 `gateway`、`grant`、`keys`，不得带 `endpoint`。
- `PushSubscriptionKind` 改为 `[webpush, gateway]`；`NotificationDeliveryTarget` 改为 `[telegram, webhook, ntfy, bark, webpush, gateway]`。
- 登记错误：
  - 400 `push_invalid`：形态错误，或旧的 `kind: fcm`。
  - 409 `gateway_disabled`：网关已关闭。
  - 409 `gateway_changed`：`gateway` 不等于当前 origin。
  - 409 `gateway_grant_invalid`：网关 verify 回 410 或 403。
  - 502 `gateway_unreachable`：verify 网络错误或 5xx。
- 删除 `fcm_unavailable` 的全部描述。

### K4 迁移 `0058_push_gateway.sql`（文本冻结，S-B 落地）
- Up：逐字沿用 `0055_browser_relay.sql` 里这两张表的搬迁写法（`:8-22` 暂存、`:40-41` 删除、`:70-101` 重建与四个索引、`:109-116` 搬回并删暂存），不动 `devices` 与 `device_pairings`。
- 只改三处：
  1. `kind` 的 CHECK 改为 `('webpush', 'gateway')`。
  2. `push_subscriptions_keep` 只收 `WHERE kind = 'webpush'`。
  3. `notification_deliveries_keep` 只收 `subscription_id IS NULL` 或其订阅 `kind = 'webpush'` 的行。
- Down：同样结构，CHECK 还原为 `('webpush', 'apns', 'fcm')`，只保留 `kind = 'webpush'`。

### K5 插件 JSON（文本冻结；Kotlin 归 S-C，Swift 归 S-D）
- `PushState`（camelCase）：
  - `permission`：`"granted" | "denied" | "prompt"`
  - `enabled`
  - `channel`：`"unifiedpush" | "fcm" | "apns" | null`
  - `distributor`、`endpoint`（UnifiedPush 用）
  - `p256dh`、`auth`
  - `fcmToken`；`apnsToken`（小写十六进制）
  - `fcmAvailable`（bool，iOS 缺省 false）；`fcmProjectId`
  - `syncedEndpoint`、`subscriptionId`、`error`
- 命令：
  - `pushState`
  - `pushEnable {vapid?: string, transport: "default" | "unifiedpush"}`：异步，返回 `PushState`；iOS 忽略两个参数。
  - `pushDisable`
  - `pushMarkSynced {endpoint, id, server}`
- 事件：`{type:"push", state}`；`{type:"pushchallenge", challengeId, nonce}`。

### K6 宿主 bridge（文本冻结；`host.rs` 归 S-C，`host.ts` 归 S-B）
- `BRIDGE = 9`。
- `host_push_enable` 增加可选参数 `transport: "default" | "unifiedpush"`，缺省 `default`。
- `MobileInfo.push = {state, channel?: "unifiedpush"|"fcm"|"apns", alt?: "unifiedpush"|"fcm", distributor?, endpointHost?, error?}`。`alt` 是可切换到的另一通道，只有 gms 包有。
- `state` 取值：`disabled | permission_denied | no_distributor | registering | active | error`。bridge ≥ 9 不再发 `unsupported`；页面保留默认分支给旧壳。
- `error` 码：
  - `gateway_disabled`
  - `gateway_unsupported`：orynd 没有该接口。
  - `gateway_untrusted`
  - `gateway_app_mismatch`
  - `gateway_challenge_timeout`
  - `gateway_rejected`：网关其它 4xx。
  - orynd 与原生的原有错误码原样透传。

## Step 0 — iOS 探针（与 Step 1 并行；S-D 的第一段）
Depends on: 无；只动 iOS 文件，日志走 `os_log`，不依赖任何契约。
Files: 用 Step 5 的同一批文件，在分支 `m28-ios-probe` 上只做最小实现；分支上可临时给 `ios-check` 的页头与端到端两步加 `if: false`，合并前还原。
Consumes: 无。
Produces: 对 P1–P3 的书面结论，决定 Step 5 走主路径还是预案。
Do:
- 模板加 NSE target。
- Swift 里实现 AppDelegate 钩子与只在 debug 生效的探针：环境变量 `ORYN_PUSH_PROBE=1` 时请求通知授权、调 `registerForRemoteNotifications()`，并把 RFC 8291 附录 A 的接收端私钥与 auth 写进 `PushKeyStore`。附录 A 的密钥取自 `plugins/oryn-mobile/android/src/test/java/com/lynricsy/oryn/mobile/FcmKeysTest.kt`。
- NSE 实现解密。全部用子系统 `com.lynricsy.oryn.push` 打 `os_log`。
- 脚本 `ci/ios-push-check.sh` 依次做四件事：
  1. 断言 `Oryn.app/PlugIns/NotificationService.appex` 存在。
  2. 跑 `PushE2ETests`：带探针环境变量启动，点掉系统的授权框。
  3. App 在前台时 `xcrun simctl push` 发 `challenge.apns`。
  4. `simctl terminate` 后发 `notify.apns`。
- 最后用 `xcrun simctl spawn "$udid" log show --last 10m --predicate 'subsystem == "com.lynricsy.oryn.push"'` 取日志。
Accept: `gh workflow run mobile.yml --ref m28-ios-probe`，`gh run watch` 到结束。日志里按下面三项判定：
- **P1 钩子**：出现 `apns.registered` 或 `apns.register_failed` 之一即通过（未签名包缺 `aps-environment`，失败回调同样证明钩子生效）。都没有 → 在模板的 App target 里加一个 ObjC 文件，用 `+load` 交换 `-[UIApplication setDelegate:]`；仍没有 → 停下重规划。
- **P2 模板**：appex 在包里即通过。init 报 `failed to read custom Xcode project template` → 把 `tauri.conf.json` 里的路径改成 init 实际工作目录能解析的写法。两轮内解决不了 → 去掉 `bundle.iOS.template`，由 `ci/ios-nse.sh` 在 init 之后往生成的 `gen/apple/project.yml` 追加 target，再跑 `xcodegen generate --spec`。
- **P3 NSE**：出现 `nse.decrypted ok=true` 即通过，并在 Step 5 设为硬门禁。
  - 没有 `nse.received` → 模拟器不调 NSE：解密正确性改由单元测试 target 把关（编 `Shared` 源码，跑附录 A 向量），NSE 留给 Step 7 真机。**已落地**：独立工程 `ci/ios-push-unit/`（`ci/ios-push-unit.sh`，ios-check 硬门禁；不放进 `ci/ios-e2e`，因为那是 UI 测试包、没有宿主进程的钥匙串）。除附录 A 外还跑 `notify.apns` 解密 → `PushContent.parse` → `build`、篡改与错 auth 失败、`PushKeyStore` 读写。
  - 有 `nse.received` 但 `nse.keys found=false` → 模拟器包去掉 `--no-sign` 改临时签名再试；仍不行同上。

## Step 1 — 冻结契约与共享前置（主 agent 顺序完成）
Depends on: 无。
Files:
- create `internal/pushproto/{pushproto.go, hostkey.go, wire.go, pushproto_test.go}`
- create `schema/pushgw/sign-vectors.json`
- create `internal/fcm/{fcm.go, fcm_test.go}`
- create `internal/clientip/{clientip.go, clientip_test.go}`
- modify `internal/api/router.go`、`internal/api/router_test.go`
- modify `docs/protocol.md`
Consumes: `internal/notify/fcm.go` 全文作为抽取来源，本步不删它。
Produces:
- `pushproto.SigningInput(method, origin, path string, timestampMs int64, body []byte) []byte`
- `pushproto.SignHost(req *http.Request, origin string, key ed25519.PrivateKey, now time.Time, body []byte)`
- `pushproto.Verify(r *http.Request, origin string, body []byte, now time.Time) (Signer, error)`
  - `Signer{Kind string; Key string}`，`Kind` 为 `"host"` 或 `"device"`。
  - 错误：`ErrMalformed`、`ErrBadSignature`、`ErrClockSkew`。
- `pushproto.LoadOrCreateHostKey(dataDir string) (ed25519.PrivateKey, error)`
- `pushproto.HostKeyString(pub ed25519.PublicKey) string`
- `pushproto.Collapse(grant, tag string) string`
- 线上结构：`Info`、`ChallengeRequest`、`ChallengeResponse`、`GrantRequest`、`GrantResponse`、`VerifyRequest`、`PushRequest`、`ErrorBody`；JSON 字段名按 K2。另有头名常量与错误码常量。
- `fcm.New(serviceAccountJSON []byte, o Options) (*Client, error)`，`Options{HTTP *http.Client; TokenURL, APIBase string; Now func() time.Time}`
- `(*fcm.Client).ProjectID() string`
- `(*fcm.Client).Send(ctx context.Context, m Message) error`，`Message{Token string; Data map[string]string; HighPriority bool; TTL time.Duration}`
- `fcm.Error{Status int; Code string; Retry bool; RetryAfter time.Duration}`
- `fcm.ValidToken(t string) bool`
- `clientip.Trusted(next http.Handler) http.Handler`
- 定稿的 protocol.md。
Do:
- `pushproto` 按 K1 实现。向量文件含 `hostSeed`、`hostKey`、`deviceScalar`、`deviceKey`，若干 `cases{method, origin, path, timestamp, body, input, hostSignature, deviceSignature}`，以及 `collapse` 样例。由带更新开关的 Go 测试生成一次后提交。
- `internal/fcm` 是 `notify/fcm.go` 发送部分的原样搬迁：服务账号解析、RS256 断言、55 分钟缓存、`messages:send`、错误码解析。唯一行为变化：401 时作废缓存并在 `Send` 内重试一次。
- `clientip.Trusted` 是 `router.go:117-146` 的原样搬迁；`router.go:61` 改用它，删掉原函数与 `forwardedClient`，对应测试迁走。
- protocol.md：去掉「草案，未实现」，按 R1–R16 与 K1–K3 重写网关一节；「FCM 发送」里与直发有关的内容并入网关一节，保留载荷格式、不设 collapse key 的理由、补拉规则；同步修改 `:424`、`:438`、`:473`、`:477`、`:482`、`:526-527`、`:588-611` 的表格与枚举。
Accept: `go test ./internal/pushproto/ ./internal/fcm/ ./internal/clientip/ ./internal/api/` 全部 `ok`；`go vet ./...` 无输出；`make build` 成功。测试断言：
- `TestVectors`：输入逐字节相等、主机签名逐字节相等、设备签名验签通过。
- `TestVerifyRejects`：坏签名、超时钟差、两种身份头并存、带查询串。
- `TestHostKeyStable`：两次加载同一把、文件权限 0600。
- `internal/fcm`：断言声明与签名、缓存与过期、`UNREGISTERED`、401 重试一次、令牌端点 4xx 不重试、请求体形状。

## Step 2 — S-A 网关
Depends on: Step 1。
Files:
- create `cmd/oryn-push-gateway/main.go`
- create `internal/pushgw/{config.go, store.go, server.go, handlers.go, challenge.go, limits.go, upstream.go, *_test.go}`
- create `internal/pushgw/migrations/0001_init.sql`
- create `internal/apns/{apns.go, apns_test.go}`
- create `deploy/systemd/oryn-push-gateway.service`、`deploy/openresty/oryn-push-gateway.conf`、`docs/push-gateway.md`
- modify `Makefile`（`BINS` 加 `oryn-push-gateway`）
- modify `.github/workflows/release.yml:148-149`（两行 `pkg` 末尾加 `oryn-push-gateway`）
Consumes: Step 1 的 `pushproto.*`、`fcm.*`、`clientip.Trusted`；`secrets.LoadOrCreate(dataDir string) (*Box, error)`、`(*Box).Seal`、`(*Box).Open`；`webpush` 只在测试里用来造密文。
Produces: 可执行文件 `bin/oryn-push-gateway`，行为符合 K2。别的切片不导入本步的任何符号。
Do:
- 配置 YAML 严格解析，键如下：
  - `listen`、`public_origin`、`data_dir`、`log_level`
  - `apns{key_file, key_id, team_id, topic, environments}`：`key_file` 为空即不提供 APNs。
  - `fcm{service_account_file, app_id}`：文件为空即不提供 FCM。
  - `limits{grant_per_minute, grant_per_day, host_per_minute, challenges_per_ip_hour}`
- `public_origin` 必须是 https，或回环主机的 http。上游地址覆盖只留在 Go 结构体里给测试用，不进 YAML。
- 命令行：`-config`（缺省 `/etc/oryn-push-gateway/gateway.yaml`）与 `-version`，写法照 `cmd/orynd/main.go`。
- 存储：`<data_dir>/gateway.db`，打开参数照 `internal/store/store.go`（WAL、`busy_timeout`、`_txlock=immediate`、单连接），goose 加嵌入迁移，SQL 手写在 `store.go`。不走 sqlc：只有一张表，且避免与 S-B 同时触发生成器。
- 表 `grants`：
  - 列：`id` 主键、`device_key`、`host_key`、`channel`（CHECK `apns` / `fcm`）、`app_id`、`environment`、`token_enc` BLOB、`created_at`、`last_used_at`、`revoked_at`。
  - 索引：`device_key` 上的部分唯一索引 `WHERE revoked_at IS NULL`。
  - `token_enc = box.Seal("grant/" + id, token)`。
- `last_used_at` 至多每小时写一次。每小时清理一次：90 天未用的授权删除，已吊销超过 7 天的删除。
- 路由用标准库 mux 的方法路径模式，最外层套 `clientip.Trusted`。
- 限流照 `internal/auth/ratelimit.go` 的令牌桶在包内自写一份：仓库里 auth、artifacts、coord 各有一份，是既有做法。
- `internal/apns`：ES256 provider token 缓存 50 分钟；`production` 发往 `api.push.apple.com`，`sandbox` 发往 `api.sandbox.push.apple.com`；403 的 `ExpiredProviderToken` / `InvalidProviderToken` 重签后重试一次。
- 日志只记授权 id、主机公钥哈希前 8 位、HTTP 结果、上游错误码。
- `docs/push-gateway.md` 写清：
  - 构建、配置、systemd、反代（须传 `X-Real-IP`）。
  - 备份 `master.key` 与 `gateway.db`。
  - 创建 APNs key 的步骤（见「需要用户做」U4）。
  - 自建网关时用 `ORYN_PUSH_GATEWAYS` 重编 App。
Accept: `go test ./internal/pushgw/... ./internal/apns/...` 全部 `ok`；`make build && ./bin/oryn-push-gateway -version` 打印版本；用只含 `listen`、`public_origin`、`data_dir` 的配置启动后 `curl -s http://127.0.0.1:7700/v1/info` 返回 `{"v":1}`。测试断言：
- `TestChallengeGrantPushFCM`：假 Google 收到的 challenge 与推送体符合 K2，没有任何明文；withdraw 为 `NORMAL`。
- `TestChallengeGrantPushAPNs`：`httptest` 开 HTTP/2 TLS 的假 APNs；断言各请求头、载荷，JWT 能用公钥验过且被缓存；withdraw 不产生上游请求。
- `TestGrantExclusivePerDevice`：换主机即吊销旧授权；同主机返回原 id 并更新令牌。
- `TestNonceSingleUseAndExpiry`
- `TestHostMismatchAndRevoked`
- `TestDeleteAuthorization`：设备或主机可删；陌生签名者 403；不存在 204。
- `TestUpstreamUnregisteredRevokes`
- `TestRateLimits`：三类限额都回 429 并带 `Retry-After`。
- `TestTooBig`：3873 字符 → 413。
- `TestTokenEncryptedAtRest`：数据库文件里搜不到令牌。
- `TestRetentionCleanup`
- `TestLogsOmitSecrets`

## Step 3 — S-B orynd + Web
Depends on: Step 1。
Files:
- delete `internal/notify/fcm.go`、`internal/notify/fcm_test.go`
- create `internal/notify/gateway.go`、`internal/notify/gateway_test.go`
- modify `internal/notify/{notify.go, deliver.go, channels.go}`、`internal/server/server.go`
- modify `internal/config/{config.go, config_test.go}`
- modify `internal/api/{notifications.go, notifications_test.go}`
- modify `schema/openapi.yaml`；重新生成 `internal/apigen/**`、`web/src/api/schema.d.ts`
- create `internal/store/migrations/0058_push_gateway.sql` 与 `internal/store` 下的迁移测试
- modify `dev/orynd.yaml`、`docs/deploy.md`
- modify `web/src/lib/host.ts`、`web/src/pages/settings/{MobilePushCard.tsx, MobilePushCard.test.tsx, NotificationsSettings.tsx}`、`web/src/i18n/locales/{en,zh-CN}.json`、`web/src/mock/notify.ts`、`web/e2e/ui-audit/host.e2e.ts`
Consumes: `pushproto.SignHost`、`pushproto.LoadOrCreateHostKey`、`pushproto.HostKeyString`、`pushproto.Collapse`、`pushproto.Verify`（只在测试的假网关里用）；`webpush.Encrypt`；`pushPayloadLimit`、`pushTag`、`parsePushKeys`；K3、K4、K6。
Produces: K3 的线上行为；页面侧的 K6。
Do:
- 配置：
  - `Push` 改为 `Push{Gateway Gateway}`，`Gateway{URL string}` 的 yaml 键为 `url`。
  - `DefaultOrynd` 置 `https://push.oryn.best`。
  - 校验：空串合法；否则必须是不带路径的 origin，且为 https 或回环主机的 http。
  - 删除 `FCM` 类型与 `:400-402` 的校验。
- `notify.Config`：`FCM` 字段换成 `Gateway GatewayConfig{Origin string}`。origin 非空时 `New` 调 `LoadOrCreateHostKey(cfg.DataDir)`。
- `registerSenders` 只在 origin 非空时登记 `"gateway"`。
- `GatewayInfo()`：origin 为空时返回 404 `gateway_disabled`。
- `createGatewaySubscription`：按 K3 校验后，以主机签名调 `POST <origin>/v1/grants/verify`，再用 `UpsertPushSubscription` 写入，`Kind` 为 `gateway`，`Endpoint` 为 `<origin>/v1/grants/<grant>`。
- `gatewaySender.Send`：
  - 订阅的 `Endpoint` 前缀不是当前 origin → `Result{Gone: true}`。
  - 否则 `pushPayloadLimit(m, 2800)` → `webpush.Encrypt` → 签名 `POST /v1/push`。
  - 非撤回消息带 `collapse = pushproto.Collapse(grant, tag)`；撤回带 `withdraw: true`、不带 collapse。
  - 结果映射：202 成功；410 与 403 → `Gone`；429、502、503 → 可重试并采信 `Retry-After`；401 → 不重试并 `slog.Warn` 提示检查时钟；其余失败不重试。
- `server.go:333-337` 改接线；`internal/api` 加 `GetPushGateway`。
- 迁移按 K4。先跑 A5 的 grep 确认外键只有一处。
- `docs/deploy.md`：删掉 FCM 直发一节与 `:47` 的示例；加 `push.gateway.url` 说明；加升级说明（`push.fcm` 必须先从配置里删掉，否则 orynd 拒绝启动；回滚须先降迁移）。
- 页面：
  - `host.ts`：`BRIDGE` 改 9；`MobileInfo.push` 按 K6；`enablePush(transport?)` 只在壳的 bridge ≥ 9 时带参数。
  - `MobilePushCard`：新增 APNs 的通道文案；FCM 文案改为经官方网关；有 `alt` 时显示切换按钮（`data-testid="mobile-push-switch"`）；K6 的各错误码各一条文案，未知码沿用通用错误文案。
  - 两份语言文件里投递目标的 `apns`、`fcm` 标签（`:4610-4611`）换成 `gateway`。
  - 订阅列表里 `kind` 为 `gateway` 的行显示网关主机名；渲染位置先用 `grep -n "subscriptions" web/src/pages/settings/NotificationsSettings.tsx` 定位。
Accept: 依次执行。
- `make gen`，之后 `git status --short internal/apigen internal/store/db web/src/api` 只列出 apigen 与 `schema.d.ts`。
- `make lint-spec` 无错误。
- `go vet ./... && make test` 全绿。
- `grep -rIl "fcm" internal --include=*.go` 只剩 `internal/fcm` 与 `internal/pushgw`。
- `cd web && bun run check && bun run test` 全绿。

测试断言：
- `TestGatewayDisabled`
- `TestGatewaySubscriptionValidation`：K3 的五种错误各一例。
- `TestGatewaySender`：
  - 假网关用 `pushproto.Verify` 验过主机签名。
  - 用接收端私钥解开 `e`，等于 `pushPayloadLimit` 的输出。
  - 请求体里没有标题、正文、线程 id。
  - `collapse` 等于 `pushproto.Collapse`。
- `TestGatewaySenderResults`：410 与 403 删订阅；429 按 `Retry-After` 重排；401 不重试。
- `TestGatewayOriginChangedDropsSubscription`
- `TestValidatePushGatewayURL`
- `TestLoadRejectsPushFCM`：带 `push.fcm` 的 YAML 加载报错。
- `TestM28MigrationDownUp`：
  - 迁移前造 `webpush` 与 `fcm` 订阅各一条，各带一条投递。
  - Up 之后只剩 `webpush` 那一对；插入 `gateway` 成功，插入 `fcm` 违反 CHECK。
  - Down 再 Up 不丢 `webpush`。
- `MobilePushCard.test`：APNs 文案；切换按钮只在 bridge ≥ 9 且有 `alt` 时出现，并以 `"unifiedpush"` 调用；`gateway_untrusted` 显示专属文案。

## Step 4 — S-C 壳 Rust + Android Kotlin
Depends on: Step 1（向量文件与 protocol.md）。
Files:
- modify `web/src-tauri/Cargo.toml`、`web/src-tauri/Cargo.lock`
- create `web/src-tauri/src/pushgw.rs`
- modify `web/src-tauri/src/{api.rs, mobile_logic.rs, mobile.rs, host.rs, webview.rs, lib.rs}`
- modify `web/src-tauri/plugins/oryn-mobile/src/{models.rs, lib.rs}`
- modify `plugins/oryn-mobile/android/src/main/java/com/lynricsy/oryn/mobile/{OrynMobilePlugin.kt, PushStore.kt, PushSync.kt, Fcm.kt, FcmMessage.kt}`
- modify `plugins/oryn-mobile/android/src/test/**` 的对应测试
- modify `gen/android/app/src/gms/java/com/lynricsy/oryn/{OrynFcmService.kt, Flavor.kt}`
Consumes: K1、K2、K3、K5、K6；`schema/pushgw/sign-vectors.json`；`secrets::TokenStore`；`config::write_atomic`。
Produces: 壳侧的 K5 与 K6；Kotlin 侧的 K5。
Do（Rust）:
- 依赖：`p256`（`ecdsa` 特性）、`sha2`、`base64`、`rand_core`。具体版本取与 `Cargo.lock` 已有的 `sha2 0.10`、`base64 0.22` 兼容的最新版。
- `pushgw.rs`：
  - `trusted_origins() -> Vec<String>`：取 `option_env!("ORYN_PUSH_GATEWAYS")`，逗号分隔；缺省只有 `https://push.oryn.best`；只接受 https 或回环 http 的 origin。
  - `signing_input(method: &str, origin: &str, path: &str, ts_ms: i64, body: &[u8]) -> String`
  - `DeviceKey::load_or_create(store: &dyn TokenStore) -> Result<DeviceKey, String>`；存储账户名 `oryn:push-device-key`，值为 base64url 的 32 字节标量。
  - `DeviceKey::public(&self) -> String`、`DeviceKey::sign(&self, input: &[u8]) -> String`
  - `Ledger{auto_pending: bool, grant: Option<Grant>, pending_revokes: Vec<Revoke>}`，存 `<app_config_dir>/push.json`；文件不存在等价于 `auto_pending = true`。
  - `Grant{origin, host_key, grant, token_hash}`，`token_hash` 为令牌 SHA-256 的 base64url 前 22 位。
- `api.rs`：
  - `PushSubscription::Fcm` 换成 `Gateway{endpoint, gateway, grant, p256dh, auth}`。
  - `Api` 增加 `push_gateway(&self, base: &str, token: &str) -> Result<GatewayInfo, ApiError>`：404 且 code 为 `gateway_disabled` 映射成 `gateway_disabled`，其它 404 映射成 `gateway_unsupported`。
  - `Api` 增加 `gateway_call(&self, call: GatewayCall) -> Result<GatewayReply, ApiError>`，`GatewayCall{method, origin, path, headers, body}`，`GatewayReply{status, body}`。沿用现有不跟随重定向的客户端，3xx 当失败处理。
- `mobile_logic.rs`：
  - `enum Transport{Default, UnifiedPush}`；`Native::push_enable(&self, vapid: &str, transport: Transport)`。
  - `MobileCore::new` 增加 `dir: PathBuf`、`keys: Box<dyn TokenStore>`、`app_id: String`。
  - 新方法 `challenge_received(&self, id: &str, nonce: &str)`、`take_auto_enable(&self) -> bool`、`unpaired(&self)`、`flush_revokes(&self)`（异步）。
  - 删除 `Unsupported` 类型与 `PushViewState::Unsupported`。
- `sync_push` 在通道为 `fcm` 或 `apns` 时的流程：
  1. 通知权限未授予 → 不登记。
  2. `flush_revokes`，再读 `push_gateway`。
  3. origin 不在受信列表 → 记 `gateway_untrusted`，不向它发任何请求。
  4. 账本里的授权与当前 origin 或主机公钥不一致 → 旧 origin 仍受信时尽力吊销，然后清账本。
  5. 没有授权、或令牌哈希变了 → 登记：先 `GET /v1/info` 做兼容检查（APNs 比对 topic 与环境，FCM 比对 `fcmProjectId`；不符记 `gateway_app_mismatch`），再 `POST /v1/challenges`，等 nonce 最多 30 秒（超时记 `gateway_challenge_timeout`），再 `POST /v1/grants`，写账本。
  6. `push_subscribe`。若回 409 的 `gateway_grant_invalid` 或 `gateway_changed` → 清账本、重新登记、只重试一次。
  7. `push_mark_synced`。
- nonce 可能先于 `challenges` 的应答到达：`challenge_received` 先把（id，nonce）放进一个至多 8 条、5 分钟过期的缓冲，等待方先查缓冲再等通知。
- iOS 的 `environment`：debug 构建为 `sandbox`，否则 `production`。
- 关闭推送：尽力吊销网关授权（失败则进 `pending_revokes`，至多 8 条，之后每次同步重试）→ 尽力删 orynd 订阅 → 无条件停用本机。
- 解除配对：`webview.rs` 的 `push_disable` 改调 `unpaired()`（授权挪进待吊销、停用本机、置 `auto_pending`），再起异步任务 `flush_revokes`。
- 自动启用：`mobile::host_ready` 里，若 `take_auto_enable()` 为真，且平台是 iOS 或原生状态 `fcmAvailable` 为真，则执行一次 `push_enable(conn, Transport::Default)`；否则照旧强制同步。
- `push_view` 增加账本参数：网关通道只有在授权存在、令牌哈希一致、且已同步的端点等于授权端点时才是 `active`。`alt` 的规则：`fcmAvailable` 且通道为 `fcm` → `unifiedpush`；`fcmAvailable` 且通道为 `unifiedpush` → `fcm`；其余为空。
- `host.rs`：`BRIDGE = 9`，并在注释里补第 9 版的含义。`host_push_enable` 加可选的 `transport`。
- `mobile.rs`：
  - Android 胶水：把 `transport` 传给插件；处理 `Event::PushChallenge`。
  - iOS：新增 `IosNative`，四个推送命令直接调插件，分享与 toast 为空操作。`listen` 里处理 `Event::Push`（通知页面并同步）与 `Event::PushChallenge`。
- `lib.rs`：移动端 setup 把配置目录、第二个密钥存储实例（Android 用 `KeystoreStore`，iOS 用 `KeychainStore`）、应用标识传进 `MobileRt::new`。
Do（Kotlin）:
- `PushStore`：
  - 新增偏好键 `preferUp`，缺省为假：升级上来、此前启用过 UnifiedPush 的 gms 用户也按 FCM 处理（A6）。下一次同步时 Rust 发现通道由 `unifiedpush` 变成 `fcm`，先删掉 orynd 上旧的 Web Push 订阅，再走网关登记；Kotlin 侧在切到 FCM 前注销 UnifiedPush。只有用户点了「切换到 UnifiedPush」才把 `preferUp` 置真。Kotlin 测试补一例：已有 UnifiedPush 端点、无 `preferUp` 的 gms 状态 → `channelFor` 为 `fcm`。
  - `channelFor(enabled, distributor, hasFcm, preferUp)`：未启用为空；带 FCM 且不偏好 UnifiedPush → `fcm`；有分发器 → `unifiedpush`；否则为空。
  - `state()` 增加 `fcmAvailable` 与 `fcmProjectId`。
- `enablePush`：`transport` 为 `unifiedpush` 或本包不带 FCM 时走 UnifiedPush，并先删掉 FCM 令牌；否则走 FCM，并先注销 UnifiedPush。
- `FcmBridge` 增加 `projectId(context): String?`；gms 实现取 `FirebaseApp.getInstance().options.projectId`。
- `FcmMessage.challenge(data): Pair<String, String>?`。`OrynFcmService.onMessageReceived` 最先判断它，命中就发 `pushchallenge` 事件并返回；日志只记事件名。
- `PushSync.request` 对 `fcm` 通道返回空。网关通道不做后台重登记，令牌变化等下次进入 App 由 Rust 处理。
Accept: 依次执行。
- `cd web/src-tauri && cargo test` 全绿。fmt 与 clippy 用 `.github/workflows/ci.yml` 里 `desktop` job 的原命令，执行前 `grep -n cargo .github/workflows/ci.yml`。
- `cd web/src-tauri/gen/android && ./gradlew --no-daemon :tauri-plugin-oryn-mobile:testDebugUnitTest` 全绿。
- `cd web && ORG_GRADLE_PROJECT_orynDistribution=gms bun run tauri android build --debug --apk --target x86_64` 成功。先把 `.dev/m7/google-services.json` 拷到 `web/src-tauri/gen/android/app/`。

Rust 测试断言：
- `signing_input_matches_vectors`
- `device_key_matches_vectors`
- `go_device_signature_verifies`
- `untrusted_origin_sends_nothing`：对网关的调用数为 0。
- `enroll_challenge_grant_subscribe`：调用顺序、各请求体字段正确；每个请求的签名能用设备公钥验过；登记体为 `{kind:"gateway", gateway, grant, keys}`。
- `challenge_before_response_is_buffered`
- `challenge_timeout_reports_error`
- `token_change_reenrolls`
- `host_key_change_revokes_then_reenrolls`
- `grant_invalid_reenrolls_once`
- `disable_revokes_and_queues_on_failure`
- `auto_enable_once_per_pairing`
- `info_mismatch_reports_gateway_app_mismatch`
- `push_view_gateway_states`
- 现有 `Rig` 的析构断言扩展为：事件日志不含令牌与 nonce。

Kotlin 测试断言：通道表的全部组合；`fcm` 通道不产生后台登记请求；challenge 数据解析。

## Step 5 — S-D iOS 原生 + CI（接 Step 0）
Depends on: Step 0 的结论。K5 是文本契约，不必等 Step 4。
Files:
- create `web/src-tauri/ios/project.yml`
- create `web/src-tauri/ios/NotificationService/{NotificationService.swift, Signing.xcconfig}`
- create `plugins/oryn-mobile/ios/Sources/Shared/{WebPushDecrypt.swift, PushKeyStore.swift, PushContent.swift}`；把 `NotificationAvatarRenderer.swift` 移进 `Shared/`
- create `plugins/oryn-mobile/ios/Sources/{PushRegistrar.swift, PushStore.swift}`
- modify `plugins/oryn-mobile/ios/Sources/{OrynMobilePlugin.swift, NotificationManager.swift}`
- modify `web/src-tauri/tauri.conf.json`、`web/src-tauri/Info.ios.plist`
- modify `web/package.json`、`web/bun.lock`
- modify `web/src-tauri/ci/ios-entitlements.sh`
- create `web/src-tauri/ci/{ios-nse.sh, ios-push-check.sh}`、`web/src-tauri/ci/ios-push/{challenge.apns, notify.apns}`、`web/src-tauri/ci/ios-e2e/Tests/PushE2ETests.swift`
- modify `.github/workflows/mobile.yml`
Consumes: K5；K2 里 APNs 载荷的形状；Web Push 明文载荷 `{v, id, kind, title, body, url, tag, ts?, avatar?}`。
Produces: Swift 侧的 K5；CI secret 名 `IOS_NSE_MOBILE_PROVISION`。
Do:
- CLI 升到 `2.12.6`。`tauri.conf.json` 的 `bundle.iOS` 加 `"template": "ios/project.yml"`。
- 模板：
  - 以 tauri-cli 2.12.6 的内置 `templates/mobile/ios/project.yml` 为底逐字复制，只加两处。
  - App target 的 `dependencies` 加 `- target: NotificationService`。
  - 新 target `NotificationService`：
    - `type: app-extension`，`platform: iOS`。
    - 源码两个目录：`../../ios/NotificationService` 与 `../../plugins/oryn-mobile/ios/Sources/Shared`。
    - `configFiles` 的 debug 与 release 都指向 `Signing.xcconfig`。
    - `PRODUCT_BUNDLE_IDENTIFIER` 为 `{{app.identifier}}.NotificationService`。
    - Info 里 `NSExtensionPointIdentifier` 为 `com.apple.usernotifications.service`，`NSExtensionPrincipalClass` 为 `$(PRODUCT_MODULE_NAME).NotificationService`，另有 `OrynAppIdentifierPrefix: $(AppIdentifierPrefix)`。
    - entitlements 的 `keychain-access-groups` 为 `["$(AppIdentifierPrefix)com.lynricsy.oryn.push"]`。
- `Info.ios.plist` 加 `OrynAppIdentifierPrefix` = `$(AppIdentifierPrefix)`。
- `PushRegistrar`：
  - 插件 `init()` 里交换 `-[UIApplication setDelegate:]`，在原实现之前给新 delegate 的类 `class_addMethod` 两个方法：注册成功与注册失败的回调。
  - `load(webview:)` 里在主线程再检查一次；若此时才补上方法，把 delegate 置空再设回同一对象，刷新 UIKit 的缓存。
  - 令牌转小写十六进制。
- `PushKeyStore`：
  - CryptoKit 的 P-256 私钥与 16 字节 auth 存 Keychain 通用密码。
  - service 为 `com.lynricsy.oryn.push`，账户 `p256` 与 `auth`。
  - access group 为 Info 里的前缀加 `com.lynricsy.oryn.push`；可访问性为首次解锁后、仅本机。
- `WebPushDecrypt`：RFC 8291 `aes128gcm` 单记录解密，标 `@available(iOS 14.0, *)`。
- `PushContent`：把 `NotificationManager` 里私有的 tag、标题、头像、通信通知构造搬出来，本地通知与 NSE 共用。
- 插件四个推送命令：
  - `pushEnable`：请求授权 → 已授权则 `registerForRemoteNotifications()`，等令牌最多 15 秒（超时 `error = "apns_token"`）→ 确保密钥存在 → 返回状态。未授权也置 `enabled = true`，与 Android 一致。
  - `pushDisable`：注销远程通知、清密钥与本地状态。
- 应用回到前台且已启用时，重新注册并复查权限；令牌或权限有变就发 `push` 事件。
- `NotificationManager.willPresent`：
  - `userInfo` 的 `kind` 为 `gateway_challenge` → 不展示，发 `pushchallenge` 事件。
  - 触发器是远程推送 → 不展示（R16）。
  - 其余照旧。
- NSE：
  - 一启动先把占位文案换成按系统语言的「有新通知」/「New notification」。
  - 取 `e` → 解密 → 解析 → 用 `PushContent` 填标题、正文、`threadIdentifier` 以及 `userInfo` 的 `id` 与 `path`。
  - 任何一步失败或超时都交付占位内容。
- `ios-entitlements.sh`：
  - 只处理文件名以 `_iOS.entitlements` 结尾的那一个。
  - 写三项：通信通知为真；`aps-environment` 取环境变量 `ORYN_APS_ENVIRONMENT`，缺省 `development`；`keychain-access-groups` 依次为 `$(AppIdentifierPrefix)com.lynricsy.oryn` 与 `$(AppIdentifierPrefix)com.lynricsy.oryn.push`。第一项保持旧设备令牌所在的默认组不变。
- `ios-nse.sh <版本> [构建号]`：
  - 把 NSE 的 `CFBundleShortVersionString` 设为版本。
  - `CFBundleVersion`：有构建号时为 `<版本>.<构建号>`（与 `tauri-cli/src/mobile/ios/build.rs:441-445` 的拼法一致），否则为版本。
  - 设了 `IOS_NSE_MOBILE_PROVISION` 时另做三件事：
    - 解码描述文件，装到 `~/Library/MobileDevice/Provisioning Profiles/` 与 `~/Library/Developer/Xcode/UserData/Provisioning Profiles/`。
    - 把 `Signing.xcconfig` 写成 Manual 签名、身份 `Apple Distribution`、团队 `XDPWT9ABRD`、该描述文件的 UUID。
    - 在 `gen/apple/ExportOptions.plist` 的 `provisioningProfiles` 下加 `com.lynricsy.oryn.NotificationService` → UUID。tauri 2.12.6 会合并而不是覆盖这张表。
- `mobile.yml`：
  - `ios-check`：init 之后加跑 `ios-nse.sh`；断言 appex 在包里；新增「模拟器：推送探针」步骤跑 `ios-push-check.sh`，门禁范围按 Step 0 的结论。
  - `ios-release`：
    - `HAS_SIGN` 增加 `IOS_NSE_MOBILE_PROVISION`。
    - entitlements 步骤带 `ORYN_APS_ENVIRONMENT=production`。
    - `ios-nse.sh` 带版本与构建号，secret 只进这一步的 `env`。
    - 「校验 IPA」追加四项：主包 `aps-environment` 为 `production`；appex 存在且包名为 `com.lynricsy.oryn.NotificationService`；appex 的 entitlements 含 push 组；appex 与主包的两个版本号相等。
Accept: `gh workflow run mobile.yml --ref <分支>` 后 `gh run watch`，`ios-check` 成功；下载产物 `ios-screenshot`，其中 `push/.complete` 存在，`push/summary.md` 列出钩子、challenge、NSE 三项的日志原文。`ios-release` 的验证见 Step 7。

## Step 6 — 集成验收与文档收尾（主 agent）
Depends on: Step 2、3、4、5。
Files:
- modify `web/scripts/m7-accept.ts`（改写 A23）
- modify `docs/mobile.md`、`README.md:218`、`docs/features.md:487`
Consumes: `bin/oryn-push-gateway`、`bin/orynd`、gms debug 包、`.dev/m7/fcm-service-account.json`、AVD `oryn-m7`。
Produces: A23 的通过输出。
Do: 按下节的脚本形状改写 A23。`docs/mobile.md` 改四处：
- 两种安装包的表格与 FCM 一节：gms 默认走官方网关，UnifiedPush 改为手动切换。
- 自托管构建：自建网关，并用 `ORYN_PUSH_GATEWAYS` 重编。
- 附录 A：APNs、NSE、门户步骤、新 secret、「尚未完成」清单。
- Secrets 表加 `IOS_NSE_MOBILE_PROVISION`。
Accept: `cd web && bun run accept:m7 -- --only A23` 输出 `A23 PASS`。

### Android 模拟器真 FCM 端到端：脚本形状
前置：
```bash
source .dev/m7-env.sh && make build
mkdir -p .dev/pushgw && cat > .dev/pushgw/gateway.yaml <<'EOF'
listen: "127.0.0.1:7700"
public_origin: "http://localhost:7700"
data_dir: "./.dev/pushgw/data"
fcm: { service_account_file: ".dev/m7/fcm-service-account.json", app_id: "com.lynricsy.oryn" }
EOF
./bin/oryn-push-gateway -config .dev/pushgw/gateway.yaml &
# dev/orynd.yaml 临时写 push: { gateway: { url: "http://localhost:7700" } }，然后 make dev
cp .dev/m7/google-services.json web/src-tauri/gen/android/app/
(cd web && ORYN_PUSH_GATEWAYS=http://localhost:7700 ORG_GRADLE_PROJECT_orynDistribution=gms \
  bun run tauri android build --debug --apk --target x86_64)
cp web/src-tauri/gen/android/app/build/outputs/apk/universal/debug/app-universal-debug.apk .dev/m7/oryn-gms-debug.apk
adb reverse tcp:7600 tcp:7600 && adb reverse tcp:7700 tcp:7700
```
A23 的断言顺序：
1. `GET /api/v1/push/gateway` 返回 200，`origin` 为 `http://localhost:7700`；旧的 `kind: fcm` 登记返回 400 `push_invalid`。
2. 装 gms 包并配对，不点任何按钮：出现 `push.enable {channel:"fcm"}` 事件与系统授权框；用 uiautomator 点允许后，推送卡 `data-push-state="active"`、`data-push-via="fcm"`。
3. 事件日志依次有 `push.gateway.challenge`、`push.gateway.granted`、`push.synced {kind:"gateway"}`。
4. orynd 库里该订阅 `kind='gateway'`，`endpoint` 以 `http://localhost:7700/v1/grants/` 开头，并归属本设备。
5. 网关库 `.dev/pushgw/data/gateway.db` 里有效授权恰好 1 条，库文件里搜不到 FCM 令牌。
6. 事件日志与网关日志都不含令牌和 nonce。
7. 回到桌面后发一个回合：出现 `push.received {via:"fcm"}`、`notify.posted`、系统通知；orynd 记下 `last_success_at`。
8. 杀掉进程后再发一个回合，仍收到。
9. 在卡片上关闭：网关授权的 `revoked_at` 非空、orynd 订阅行消失；再发一个回合，20 秒内没有 `push.received`。
10. 重新启用后启用 ntfy，点 `mobile-push-switch`：`data-push-via="unifiedpush"`，网关授权已吊销。结束时还原 ntfy 状态并装回 foss 包。

## Step 7 — 用户门控的真签名与真机验收
Depends on: Step 6，以及「需要用户做」的 U3–U6。
Files: 无代码改动。
Consumes: 用户配好的 secrets 与网关上的 APNs key。
Produces: TestFlight 构建与真机结论。
Do: 推预发布 tag `v<版本>-rc.N` 走 `release.yml`。
Accept:
- `ios-release` 的「校验 IPA」通过。
- TestFlight 装机后配对即弹授权框；允许后卡片显示经 APNs 接收。
- 锁屏状态下发一个回合：通知带真实标题、正文与头像。
- App 在前台时不重复弹。
- 关闭推送后不再收到。

## 并行论证（逐对）
- **S-A 与 S-B**：包完全不相交。共享的只有 Step 1 已落地、双方只读的 `pushproto` 与 `fcm`。`go.mod` 都不动。S-A 不用 sqlc，所以 S-B 新增迁移时没有生成器竞争。
- **S-A 与 S-C、S-A 与 S-D**：只经 K1、K2 的线上协议与向量文件耦合，文件不相交。
- **S-B 与 S-C**：只经 K3 与 K6 耦合。壳的 HTTP 是手写 JSON，不用生成类型；`host.ts` 与 `host.rs` 各归一方。
- **S-B 与 S-D**：不相交。S-B 的页面改动不碰 `web/package.json`，它归 S-D。
- **S-C 与 S-D**：只经 K5 耦合。全部 `.rs`、`Cargo.toml`、Kotlin 归 S-C；Swift、模板、CI 脚本、`mobile.yml`、`tauri.conf.json`、`package.json` 归 S-D。
- **Step 0 与 Step 1**：Step 0 只用 `os_log`，不依赖任何契约，所以可以并行。

## 风险与未知
最高风险是 iOS，且本机无法验证：
- AppDelegate 钩子是否被回调：P1。
- 自定义模板加 NSE 能否在 tauri 生成的工程里编出来：P2。
- NSE 能否读到共享 keychain：P3。
- 两份描述文件的签名导出：只能在 Step 7 验证，因为签名 secret 所在的 environment 只对 `v*` tag 开放。

前三项由 Step 0 用一次 CI 回答，后一项靠 rc tag。其余风险都能在本机用单测与 A23 覆盖。

## 需要用户做，以及不做时的状态
- **U1 DNS 与 TLS**：`push.oryn.best` 指向网关主机，反代终止 TLS。不做 → 登记请求连不上，卡片停在「注册中」，下次进入 App 自动重试；Web Push 与 UnifiedPush 正常。
- **U2 部署网关并放入 Firebase 服务账号**（另派 agent；文件是 `.dev/m7/fcm-service-account.json` 同一项目）。不做 → `/v1/info` 没有 `fcm`，gms 包显示 `gateway_app_mismatch`。
- **U3 APNs key**：
  1. Apple Developer → Certificates, IDs & Profiles → Keys → 加号。
  2. 勾选 Apple Push Notifications service (APNs)，环境选 Sandbox & Production。
  3. 下载 `AuthKey_<KeyID>.p8`（只能下载一次），记下 Key ID；Team ID 是 `XDPWT9ABRD`。

  看不到 Keys 入口说明账号不是 Account Holder 或 Admin。不做 → 网关不报 `apns`，iOS 卡片显示 `gateway_app_mismatch`；网关的 APNs 路径只经假 APNs 测试过。
- **U4 Apple 门户**：
  1. App ID `com.lynricsy.oryn` 开启 Push Notifications，重新导出描述文件并更新 `IOS_MOBILE_PROVISION`。
  2. 新建显式 App ID `com.lynricsy.oryn.NotificationService`（不需要任何能力，也不需要 App Group），建它的 App Store Connect 描述文件。
- **U5 GitHub secret**：`IOS_NSE_MOBILE_PROVISION`（base64）放进 environment `mobile-release`。U4 或 U5 不做 → `ios-check` 照常通过，但 `ios-release` 在凭据检查处失败，整次 Release 不发布。
- **U6 生产 orynd 升级**：先从 `orynd.yaml` 删掉 `push.fcm`，否则新版拒绝启动；之后可删密钥 `fcm/service-account`。
- **U7 TestFlight 真机验收**：即 Step 7。

## 用户决定（2026-11-24 已拍板）
- D1 = 直接删 `push.fcm`，旧配置启动报错，deploy.md 写升级说明（按推荐）。
- D2 = Rust 软件 P-256 设备密钥（按推荐）。
- D3 = 本轮不做授权列表界面（按推荐）。
- D4 = iOS 离线丢失不补发（按推荐）。
- D5 = **迁移**：已用 UnifiedPush 的 gms 老用户升级后自动改走网关 FCM（推翻推荐；已改 A6 与 Step 4 的 `preferUp` 规则）。
