# 推送网关（oryn-push-gateway）

官方签名的 iOS 包与 `gms` 包在连任意自部署 orynd 时，经推送网关收推送：orynd 把 RFC 8291 密文交给网关，网关转给 APNs / FCM。网关只见密文，解不开载荷；APNs key 与 Firebase 服务账号只放在网关上。协议见 [protocol.md「官方推送网关（M28）」](protocol.md#官方推送网关m28)。

官方网关是 `https://push.oryn.best`。只有自己编 App（换了 bundle id / Firebase 项目）的部署者才需要自建网关，见文末「自建网关」。

## 构建

```bash
make build                     # 产出 bin/oryn-push-gateway（与 orynd 同一套 ldflags，静态链接）
./bin/oryn-push-gateway -version
```

也可以直接用 GitHub Release 的 `oryn-server_<版本>_linux-{x64,arm64}.tar.gz`，里面带 `oryn-push-gateway`。

命令行只有两个参数：`-config`（缺省 `/etc/oryn-push-gateway/gateway.yaml`）与 `-version`。

## 配置

YAML 严格解析，未知键启动报错。

```yaml
listen: "127.0.0.1:7700"                     # 只监听回环，由反代终止 TLS
public_origin: "https://push.oryn.best"      # 客户端看到的 origin：https，或回环主机的 http；不带路径。签名覆盖它
data_dir: "/var/lib/oryn-push-gateway"       # master.key 与 gateway.db
log_level: "info"                            # debug / info / warn / error

apns:                                        # key_file 为空 = 不提供 APNs（/v1/info 不报 apns）
  key_file: "/etc/oryn-push-gateway/apns.p8" # AuthKey_<KeyID>.p8
  key_id: "<KeyID>"
  team_id: "XDPWT9ABRD"
  topic: "com.lynricsy.oryn"                 # App 的 bundle id
  environments: ["production", "sandbox"]    # 缺省两者都接受；TestFlight / App Store 包是 production，Xcode 调试包是 sandbox

fcm:                                         # service_account_file 为空 = 不提供 FCM（/v1/info 不报 fcm）
  service_account_file: "/etc/oryn-push-gateway/fcm.json"
  app_id: "com.lynricsy.oryn"                # Android 包名

limits:                                      # 以下为缺省值，可整段省略
  grant_per_minute: 60                       # 每个授权每分钟
  grant_per_day: 2000                        # 每个授权每天
  host_per_minute: 600                       # 每个主机公钥（每台 orynd）每分钟
  challenges_per_ip_hour: 30                 # 每个来源 IP 每小时的 POST /v1/challenges
```

只写 `listen`、`public_origin`、`data_dir` 也能启动：两个通道都不提供，`/v1/info` 返回 `{"v":1}`，适合先验证部署链路。

`data_dir` 里有两样东西：

- `master.key`：32 字节主密钥（0600，首次启动生成），以 AES-256-GCM 静态加密授权表里的推送令牌。
- `gateway.db`：SQLite 授权表（WAL 模式，旁边会有 `-wal` / `-shm`）。只存授权（设备公钥、主机公钥、通道、App 标识、环境、加密后的推送令牌、时间），不存任何通知内容，也不存 orynd 的地址。90 天未用的授权与吊销满 7 天的授权每小时清理一次。

日志只记路由、HTTP 结果、授权 id、主机公钥哈希前 8 位与上游错误码，不记推送令牌、nonce、`e`、来源 IP 与其它请求内容。

## 安装与 systemd

二进制与 unit 的来源二选一（`GW_BIN` / `GW_UNIT` 用于后面的命令）：

```bash
# Release 包：解包后在 oryn-server_<版本>_linux-x64/ 目录里执行
GW_BIN=oryn-push-gateway GW_UNIT=oryn-push-gateway.service
# 从源码：仓库根目录 make build 之后执行
GW_BIN=bin/oryn-push-gateway GW_UNIT=deploy/systemd/oryn-push-gateway.service
```

```bash
sudo install -m 0755 "$GW_BIN" /usr/local/bin/oryn-push-gateway
sudo useradd --system --home /var/lib/oryn-push-gateway --shell /usr/sbin/nologin oryn-push
sudo install -d -m 0750 -o root -g oryn-push /etc/oryn-push-gateway
sudo install -m 0640 -o root -g oryn-push gateway.yaml /etc/oryn-push-gateway/gateway.yaml
# 凭据（见下文「APNs key」「Firebase 服务账号」）
sudo install -m 0640 -o root -g oryn-push AuthKey_<KeyID>.p8 /etc/oryn-push-gateway/apns.p8
sudo install -m 0640 -o root -g oryn-push fcm-service-account.json /etc/oryn-push-gateway/fcm.json

sudo install -m 0644 "$GW_UNIT" /etc/systemd/system/oryn-push-gateway.service
sudo systemctl daemon-reload && sudo systemctl enable --now oryn-push-gateway
journalctl -u oryn-push-gateway -f
```

unit 以 `oryn-push` 用户运行，`StateDirectory` 自动建好 `/var/lib/oryn-push-gateway`（0700）。改配置或换凭据后 `systemctl restart oryn-push-gateway`。

## 反代（OpenResty）

照 [`deploy/openresty/oryn-push-gateway.conf`](../deploy/openresty/oryn-push-gateway.conf)：TLS 终止在反代，`proxy_pass http://127.0.0.1:7700`。

- **必须传 `X-Real-IP $remote_addr`**。网关只在请求来自回环地址（即本机反代）时采信 `X-Real-IP` / `X-Forwarded-For`，用它做每 IP 的 challenge 限流；不传则所有手机共用反代的回环地址，一起被限流。网关因此必须只监听回环、只经本机反代对外。
- 不要改写路径、不要加查询串：签名覆盖 `public_origin` + 路径，带查询串的请求一律 400。
- `public_origin` 必须与手机、orynd 访问的 origin 逐字一致（`https://push.oryn.best`，无尾斜杠）。

## 备份

备份 `data_dir` 下的 **`master.key` 与 `gateway.db`**（两者缺一不可：没有 `master.key` 解不开库里的令牌）。在线备份用 `sqlite3 gateway.db ".backup gateway.bak"`，或停服务后连同 `-wal` 一起拷贝。

丢了它们的后果有限：所有授权失效，orynd 下一次投递收到 410 后删除订阅，App 下次同步时重新授权。期间不发推送。

## APNs key

需要 Apple Developer 账号的 Account Holder 或 Admin 角色。

1. 打开 Apple Developer → Certificates, IDs & Profiles → Keys → 加号（+）。
2. 填名字，勾选 **Apple Push Notifications service (APNs)**，点 Configure，环境选 **Sandbox & Production**，保存后 Continue → Register。
3. 下载 `AuthKey_<KeyID>.p8`。**只能下载一次**，丢了只能吊销重建。记下 Key ID（文件名里那串，也显示在 key 详情页）。
4. Team ID 是 `XDPWT9ABRD`（Membership details 页可查）。

看不到 Keys 入口说明账号不是 Account Holder 或 Admin。

把 `.p8` 放到 `/etc/oryn-push-gateway/apns.p8`（root:oryn-push 0640），配置里填 `key_file`、`key_id`、`team_id`、`topic: com.lynricsy.oryn`。另外 App ID `com.lynricsy.oryn` 要开启 Push Notifications（见 [mobile.md](mobile.md)）。

不配 APNs 时 `/v1/info` 没有 `apns`，iOS App 的推送卡片显示 `gateway_app_mismatch`。

## Firebase 服务账号

Firebase 控制台 → 项目设置 → 服务账号 → 生成新的私钥，得到服务账号 JSON（与 `gms` 包的 `google-services.json` 同一个项目）。放到 `/etc/oryn-push-gateway/fcm.json`（root:oryn-push 0640），配置里填 `service_account_file` 与 `app_id: com.lynricsy.oryn`。`/v1/info` 的 `fcm.projectId` 取自这份 JSON 的 `project_id`。

不配 FCM 时 `/v1/info` 没有 `fcm`，`gms` 包显示 `gateway_app_mismatch`。

## 自建网关

自己编 App（改了 bundle id、换了 Firebase 项目或 APNs 团队）时，官方网关不服务你的 App，需要自建：

1. 按上文部署网关，`apns.topic` / `fcm.app_id` 与凭据换成你自己的。
2. 重编 App 时用编译期环境变量 `ORYN_PUSH_GATEWAYS` 写入你的网关 origin（逗号分隔，可多个；缺省只有 `https://push.oryn.best`）：

   ```bash
   cd web && ORYN_PUSH_GATEWAYS=https://push.example.com bun run tauri android build
   ```

   App 只向这份列表里的 origin 发请求：orynd 给出的网关不在列表里时，推送卡片显示 `gateway_untrusted`。
3. orynd 的 `push.gateway.url` 改成同一个 origin。

本机联调可以用回环 http：`public_origin: http://localhost:7700`、`ORYN_PUSH_GATEWAYS=http://localhost:7700`。

## 部署清单（交给执行部署的 agent）

目标：`https://push.oryn.best` 上跑官方网关。仓库里不放官方部署的密钥、限额与域名配置以外的东西。

1. **DNS**：`push.oryn.best` 的 A / AAAA 记录指向网关主机。
2. **二进制**：取与生产 orynd 同版本的 Release `oryn-server_<版本>_linux-<arch>.tar.gz`（或在仓库 `make build`），按上文「安装与 systemd」设好 `GW_BIN` / `GW_UNIT`，`install -m 0755 "$GW_BIN" /usr/local/bin/oryn-push-gateway`。
3. **用户与目录**：`useradd --system --home /var/lib/oryn-push-gateway --shell /usr/sbin/nologin oryn-push`；`install -d -m 0750 -o root -g oryn-push /etc/oryn-push-gateway`。
4. **凭据**：
   - Firebase 服务账号 JSON（与 `.dev/m7/fcm-service-account.json` 同一个项目）→ `/etc/oryn-push-gateway/fcm.json`，root:oryn-push 0640。
   - APNs `.p8`（用户按「APNs key」一节创建后提供，附 Key ID）→ `/etc/oryn-push-gateway/apns.p8`，root:oryn-push 0640。没有时先不写 `apns` 段。
5. **配置** `/etc/oryn-push-gateway/gateway.yaml`（root:oryn-push 0640）：

   ```yaml
   listen: "127.0.0.1:7700"
   public_origin: "https://push.oryn.best"
   data_dir: "/var/lib/oryn-push-gateway"
   apns:
     key_file: "/etc/oryn-push-gateway/apns.p8"
     key_id: "<KeyID>"
     team_id: "XDPWT9ABRD"
     topic: "com.lynricsy.oryn"
     environments: ["production", "sandbox"]
   fcm:
     service_account_file: "/etc/oryn-push-gateway/fcm.json"
     app_id: "com.lynricsy.oryn"
   ```

6. **systemd**：`install -m 0644 "$GW_UNIT" /etc/systemd/system/oryn-push-gateway.service`，`systemctl daemon-reload && systemctl enable --now oryn-push-gateway`；`journalctl -u oryn-push-gateway` 里应有「推送网关已启动」且 `apns=true fcm=true`。
7. **TLS 与反代**：签 `push.oryn.best` 证书，按仓库里的 `deploy/openresty/oryn-push-gateway.conf`（Release 包不含，从同版本 tag 的源码取）建站点（回环 `127.0.0.1:7700`、传 `X-Real-IP`），重载 OpenResty。
8. **验证**：

   ```bash
   curl -s http://127.0.0.1:7700/v1/info      # 主机上，绕过反代
   curl -s https://push.oryn.best/healthz      # {"ok":true,"version":"<版本>"}
   curl -s https://push.oryn.best/v1/info
   # {"v":1,"apns":{"topic":"com.lynricsy.oryn","environments":["production","sandbox"]},"fcm":{"projectId":"<Firebase 项目>"}}
   ```

   `curl -s -X POST https://push.oryn.best/v1/push` 应返回 400 `{"code":"malformed",…}`（未签名），说明请求经反代到达了网关。
9. **备份**：把 `/var/lib/oryn-push-gateway/master.key` 与 `gateway.db` 加入主机的备份。
