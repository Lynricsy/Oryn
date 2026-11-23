# 部署 Oryn

单节点部署：`orynd`（控制面）只监听本机，放在现有反代（1Panel OpenResty）之后；同一台机器上的 `oryn provider run` 用 Docker 跑 orb。

## 1. 安装二进制与镜像

二进制有两种来源，二选一；orb 镜像不随版本发布，总要在同一版本的源码里构建：

```bash
git checkout v<版本>

# 来源一：GitHub Release v<版本>（已嵌入 Web UI，静态链接；发版流程见 desktop.md「发版」）
sha256sum -c --ignore-missing oryn-server_<版本>_SHA256SUMS
tar -xzf oryn-server_<版本>_linux-x64.tar.gz      # 解出 orynd orbd oryn gemini-rerank；ARM64 用 linux-arm64
sudo install -m 0755 oryn-server_<版本>_linux-x64/{orynd,oryn} /usr/local/bin/

# 来源二：从源码构建
make web                        # 构建 Web 客户端，复制到 internal/webui/dist 以便嵌入
make build                      # 产出 bin/orynd bin/orbd bin/oryn（嵌入上一步的前端）
sudo install -m 0755 bin/orynd bin/oryn /usr/local/bin/

make image                      # 构建 orb 镜像 oryn-orb:dev（需要 build/core/x64/omp，见 scripts/fetch-core.sh）
```

`oryn-orb:dev` 解压后约 10.2 GB（桌面应用约增加 1.1 GB，主要来自按微软更新 API 的 `sha256` 校验安装的官方 VS Code tarball），Docker 数据盘需预留相应空间；Firecracker 根盘（第 12 节 `make fc-artifacts`）按镜像实际用量自动定大小。镜像内置 `/usr/local/share/oryn/desktop-apps.json` 应用清单与 `desktop-icons/` 图标目录；不含该清单的自定义镜像在桌面任务栏里只有内置的 Chromium。

创建服务用户与目录：

```bash
sudo useradd --system --home /var/lib/oryn --shell /usr/sbin/nologin oryn
sudo install -d -o oryn -g oryn -m 0700 /var/lib/oryn
sudo install -d -m 0755 /etc/oryn
```

控制面主机需要 git 2.31 或更高版本（项目仓库镜像与仓库浏览用它，见下文「项目仓库镜像」）。没有 git 或版本过旧时 orynd 照常启动，启动日志记一条「控制面没有可用的 git，仓库浏览停用」，项目页的仓库区块显示原因，其余功能照常；装好后重启 orynd 生效。控制面不需要装 `git-lfs`。

## 2. 控制面配置 `/etc/oryn/orynd.yaml`

```yaml
listen: ["127.0.0.1:7600", "172.17.0.1:7600"]   # 第二项 = docker0 网桥地址，供本机 orb 直连
public_url: "https://oryn.example.com"          # WebAuthn RP ID 取其主机名，必须与浏览器访问地址一致
portal_domain: "orb.example.com"               # portal 泛域名；留空则不启用 portal（见下文「Portal」）
data_dir: "/var/lib/oryn"
log_level: "info"
idle: { agent_idle: "5m", user_idle: "20m", stop_after_frozen: "24h", wake_on_focus: true }
orb_defaults: { provider: "", type: "" }
push: { fcm: { service_account_secret: "" } }   # Android gms 包的 FCM 推送；空 = 关闭（见下文「移动 App 推送」）
repos: { git: "", max_mirror_gb: 5, max_lfs_cache_gb: 2, min_free_gb: 2 }   # 项目仓库镜像（以上为默认值）
```

docker0 地址因机器而异，用 `ip -4 addr show docker0` 查看（例如有的机器是 `172.18.0.1`）。orb 不经过反代，直接连这个地址。

`wake_on_focus` 控制打开线程页时是否唤醒冻结的 orb。已停机的 orb 不会因此唤醒：打开线程页时文件面板只读浏览，用户发消息或点「唤醒」后才冷启动。

`repos` 是项目仓库镜像（`<data_dir>/repos/`）的设置：

- `git`：git 可执行文件。空（缺省）= 在 `PATH` 里找 `git`；不带斜杠的名字同样按 `PATH` 查找，带斜杠的相对路径按启动目录转成绝对路径。
- `max_mirror_gb`：单个镜像的大小上限，缺省 5。克隆或拉取过程中每 5 秒复查一次，超过就终止 git（首次克隆的半成品随即删掉，已有镜像保留旧内容）；拉取完成后镜像超过上限则删除镜像。两种情况项目页都显示「仓库超过镜像大小上限」。
- `max_lfs_cache_gb`：镜像项目 Git LFS 对象缓存的总上限（所有项目合计），缺省 2。超过时先删最久没用的对象。缓存在 `<data_dir>/repos/.lfs-cache/`，不计入镜像大小。
- `min_free_gb`：数据盘至少保留的可用空间，缺省 2。低于它时不克隆、不拉取（进行中的拉取会被终止）、不向上游取 LFS 对象。数据盘与 `oryn.db` 同盘，写满会让整个控制面停摆。

三个数值写 0 都按缺省值处理，负数启动时报错。

`data_dir` 里的 `repos/` 不全是可再生的缓存：Oryn 托管仓库（[features.md「托管仓库」](features.md#托管仓库)）的正本也在这里（`<data_dir>/repos/<项目 id>.git`，目录里有标记文件 `oryn-hosted`），没有别的副本，**备份 `data_dir` 时必须包含 `repos/`**，并与 `oryn.db` 一起备份、一起恢复。把库恢复到旧备份后，找不到项目行的托管仓库在 orynd 启动时会被移到 `<data_dir>/repos/orphaned/<项目 id>.git-<Unix 秒>` 而不是删除（日志记一条错误），镜像的任何清理逻辑都不会删带标记的目录。

### 移动 App 推送

Android App 的后台推送默认走 UnifiedPush（手机上装 ntfy 等分发器），服务端不需要任何配置。要给装了 Google Play 服务、又不想装分发器的手机用 `gms` 包的 FCM 回退通道：

1. 在 Firebase 项目（包名 `com.lynricsy.oryn` 的 Android 应用）的「项目设置 → 服务账号」生成私钥，得到服务账号 JSON。
2. 存进密钥库（**proxy 模式**，即默认；env 模式会注入每台 orb，被 orynd 拒绝）：`oryn secrets set fcm/service-account < service-account.json`，随后删除本地副本。
3. `orynd.yaml` 写 `push: { fcm: { service_account_secret: "fcm/service-account" } }` 并重启 orynd。启动日志出现「FCM 已配置但服务账号不可用」说明密钥缺失或内容无效。

不配置时 App 登记 FCM 订阅会得到 `409 fcm_unavailable`，UnifiedPush 照常。移动 App 的安装、配对与发版见 [mobile.md](mobile.md)。

## 3. 反代（1Panel OpenResty）

1. 在 1Panel「网站」中创建两个反向代理站点，或直接把 `deploy/openresty/oryn.conf` 放进 OpenResty 的 `conf.d`，把示例域名替换为实际域名：
   - 主域名 `oryn.example.com` → `http://127.0.0.1:7600`
   - 泛域名 `*.orb.example.com` → `http://127.0.0.1:7600`（Portal：必须透传 `Host`，不得缓存）
2. 证书：主域名普通证书；泛域名在 1Panel「证书」里用 DNS 账号申请 `*.orb.example.com` 通配证书（ACME DNS-01）。
3. 配置要点（示例文件已包含）：`proxy_http_version 1.1`、`Upgrade`/`Connection` 头（WebSocket 与 orb link）、`proxy_read_timeout 3600s`、`proxy_buffering off`、`client_max_body_size 600m`。
4. 托管仓库（M24）：主域名另有 `location /git/ { client_max_body_size 0; … }`，其余各行与 `location /` 相同。主人本机的 `git push` 与 Git LFS 上传经这里到达 orynd，托管仓库不设大小上限，所以这条路径不限请求体，其余路径仍是 `600m`。数据盘由 orynd 自己保护（可用空间低于 `repos.min_free_gb` 时拒绝推送与上传）。orb 的推送与上传在 orb link 里传输，不受这条上限影响。自己写反代配置时同样要给 `/git/` 放开请求体上限并关闭请求缓冲（`proxy_request_buffering off`），否则大推送会被 413 拒绝，或先整个缓冲到反代的磁盘上。

## 4. 启动控制面并初始化

```bash
sudo install -m 0644 deploy/systemd/orynd.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now orynd
journalctl -u orynd | grep 'setup#token='
```

首次启动没有所有者时，日志里打印 `https://oryn.example.com/setup#token=ost_…`（令牌在 URL 片段里，不会进入反代访问日志）。浏览器打开它注册 Passkey，并妥善保存显示一次的 10 个恢复码。链接丢失时运行 `sudo -u oryn orynd --config /etc/oryn/orynd.yaml setup-token` 重新生成。

桌面 App（Windows / macOS / Linux）连接的就是这个 `public_url`，安装、配对与发版见 [desktop.md](desktop.md)。App 要求服务器走 HTTPS：会话 Cookie 带 `Secure`，Linux 上的 WebKitGTK 连 `http://localhost` 也不接受，本机试用时用 `dev/tlsproxy` 在 orynd 前面加一层 TLS。

## Portal（受管服务的浏览器入口）

orb 里带 portal 的受管服务经 `https://t-<线程短码>-p<端口>.<portal_domain>/` 从浏览器访问。orynd 按 `Host` 分流：落在 `portal_domain` 单级子域上的请求只走 portal 数据面，永远到不了 `/api/v1`、orb link 与 Web 客户端。

1. **DNS**：给 `portal_domain` 加通配记录，如 `*.orb.example.com` 指向反代所在主机（A / AAAA 或 CNAME）。
2. **证书**：`*.orb.example.com` 通配证书（ACME DNS-01，见第 3 节）。通配证书只覆盖一级子域，portal 主机名恰好是一级。
3. **`orynd.yaml`**：`portal_domain: "orb.example.com"`（小写主机名，不带协议、端口与尾点）。`public_url` 的主机名不得是 `<单标签>.<portal_domain>`（否则主域名会被当成 portal 主机，启动时报错）；主域名等于 `portal_domain` 本身是允许的。
4. **同协议、同端口**：portal 地址沿用 `public_url` 的协议与端口（`public_url` 带非默认端口时 portal 地址也带同一端口），反代必须在同一端口上同时服务主域名与泛域名。
5. **反代**：泛域名 server 块原样透传 `Host`，开启 WebSocket 升级、关闭缓冲与缓存（`deploy/openresty/oryn.conf` 已包含）。
6. **内嵌与第三方 Cookie**：线程页的 Portal 标签以 iframe 内嵌 portal，登录态是 portal 主机自己的 host-only Cookie（`__Host-oryn_portal`，`SameSite=None; Secure; Partitioned`）。主域名与 portal 域名放在同一注册域下（如 `oryn.example.com` + `*.orb.example.com`）时，两者同站，拦截第三方 Cookie 的浏览器里内嵌也可用；放在不同注册域下时，部分浏览器会拦截内嵌页的 Cookie，此时 portal 页面给出「在新窗口打开」的链接。
7. **访问控制**：portal 默认只有所有者能打开（网页会话经一次性码换取 portal Cookie；CLI / App 直接带 `Authorization: Bearer odt_…`）。线程页 Portal 标签可按端口限时公开（1 小时 / 3 小时 / 1 天 / 7 天），到期或终止时立即断开匿名连接。应用可读请求头 `X-Oryn-Authenticated: owner=yes|no` 区分访问者。
8. **唤醒**：已授权的 portal 请求（含公开访客）计为用户交互；orb 冻结或停机时，浏览器打开 portal 先看到「正在唤醒」过渡页并自动刷新，其它请求最多等待 25 秒。

开发环境（`dev/orynd.yaml`）用 `public_url: "http://localhost:7600"` + `portal_domain: "localhost"`，portal 地址形如 `http://t-<短码>-p<端口>.localhost:7600/`：Chromium 把 `*.localhost` 解析到回环并视为安全上下文，`Secure` Cookie 可用。命令行验证时可直接 `curl -H "Host: t-<短码>-p<端口>.localhost:7600" http://127.0.0.1:7600/`；`orynd --config dev/orynd.yaml admin create-web-session` 在 stdout 打印一个网页会话 Cookie（`__Host-oryn_session`）的值，供脚本走真实的鉴权握手。

### Boxes 应用与数据持久化（M23）

- **共用泛域名与证书**：Boxes 部署的应用地址是 `https://<app>.<portal_domain>/`，与线程 Portal（`t-<短码>-p<端口>`）、Artifact 隔离帧及发布页（`a-…` / `p-…`）共用同一个 `portal_domain` 通配 DNS 记录与 `*.<portal_domain>` 通配证书，不需要新的 DNS 记录或反代站点。与线程 Portal 不同，box 应用默认公开（设为私有后经 `/portal/auth?h=<app>` 握手换取该应用主机的 `__Host-oryn_portal` Cookie），请求不计用户交互、不唤醒停机的 box（停机时回 `503 app_unavailable`），普通响应保留应用自己的 `X-Frame-Options` 与 CSP（仅带 `?oryn-preview` 的主站悬停预览请求改写内嵌头）。未配置 `portal_domain` 时仍可使用 Boxes，但应用没有外部地址，`ORYN_DEPLOYMENT_URL` 为空串。
- **box 的数据在哪、哪些操作会丢**：每台 box 是一台带 `box` 标记的常驻 orb，应用的 release（`~/apps/<app>/releases/`）与跨部署数据目录（`~/apps/<app>/data/`）直接存在这台 orb 的磁盘里，不挂载独立宿主卷——Docker / Sysbox 驱动下是容器可写层，Firecracker 驱动下是该 VM 的可写盘。容器或 VM 重启、手动或按时段停机再启动、重新部署与回滚都保留数据；下线应用（`undeploy`）默认保留 `~/apps/<app>/data/`，传 `--purge-data`（或 REST `purgeData=true`）才一并删除；**删除 box，或在宿主上直接 `docker rm` 容器、删除 VM 目录，会连同整台机器上的全部应用与数据永久丢失**。同一台 box 上的所有应用与线程以同一个带免密 `sudo` 的 `user` 运行，彼此无隔离，不可信的应用应放到单独的 box；已存在的 box 不会随重建 `oryn-orb` 镜像自动更新 `orbd`。
- **Firecracker 的名额与磁盘**：Firecracker 驱动下，运行或冻结中的 box 持续占用 `firecracker.limits.max_vms` 的一个名额并计入 `limits.max_memory_mb`；磁盘大小取全局的 `firecracker.disk_gb`，不能按单台 box 调整（详见[第 12 节](#12-firecracker-providerm11b)）。

## 5. 本机 Docker provider

```bash
oryn login --url https://oryn.example.com       # 在网页上批准设备配对
oryn provider create local | sudo tee /etc/oryn/provider.token >/dev/null
sudo chmod 0600 /etc/oryn/provider.token
```

`/etc/oryn/provider.yaml`：

```yaml
url: "http://127.0.0.1:7600"         # provider 与控制面同机，直连本地端口
token_file: "/etc/oryn/provider.token"
orb_url: "http://172.17.0.1:7600"    # 注入 orb 的 ORYN_URL = docker0 地址 + orynd 的第二个 listen
driver: docker
docker:
  image: "oryn-orb:dev"
  types: { small: {cpus: 2, memory: "4g"}, medium: {cpus: 4, memory: "8g"}, large: {cpus: 8, memory: "16g"} }
  default_type: medium
```

```bash
sudo install -m 0644 deploy/systemd/oryn-provider.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now oryn-provider
```

provider 放在另一台主机时，`url` 与 `orb_url` 都写主域名（`https://oryn.example.com`），orb 经反代建立 link。

停机 orb 的只读预览：docker 与 firecracker provider 会在 hello 里声明 `offlineFs`，文件面板在 orb 停机时就不再唤醒它，而是由 provider 读取停机 orb 的磁盘：docker 用 `docker cp` 读已退出的容器，firecracker 在用户态解析 VM 的基础盘与可写盘（不挂载）。要用上这个功能，控制面与 provider 都要升级到同一版本；旧版 provider 不声明 `offlineFs`，文件面板会提示「唤醒后可浏览」。provider 回传文件时用的是 `POST /provider/v1/fs/{id}/reply`，请求体可能很大。provider 经反代连控制面时，这个路径不能缓冲请求体，也不能限制请求体大小，第 3 节示例里的 `proxy_request_buffering off` 与 `client_max_body_size 600m` 已满足；超过 600MiB 的文件无法在停机时预览。

## 6. omp 配置

```bash
oryn config push ~/.omp/agent
```

`models.yml` 里的字面 API key 会被移入加密密钥库（`data_dir/master.key`，首次启动生成，务必备份），下发到 orb 的只有占位令牌。

## 7. 安全

orb 里的 agent 有 root（镜像给 `user` 配了免密 sudo），应把 orb 当作不可信代码来隔离。下面几项按需逐一启用。

### orb 令牌

- orb 用令牌 `oot_…` 建立 link。令牌 = `HMAC(orb-token.key, orbID ":" 代数)`，**每次入队 spawn（创建、唤醒、从冻结停机前的解冻）都会轮换**，旧令牌立即失效，现有 link 同时被关闭。给 orb 拍快照（发布项目快照、连环境 fork）之后也会轮换一次，快照里残留的令牌随即作废：运行中的 orb 经一次只送令牌的 spawn 拿到新令牌并重连（项目快照的发布阶段会在新 link 上再跑一遍，setup 因完成标记直接跳过）；冻结或停机的 orb 只在库里轮换，下次唤醒时送达。
- 控制面只在 orb 处于 `spawning / connecting / setting_up / resuming / ready / waking / stopping` 时接受 link；`frozen / stopped / failed / deleting / deleted` 一律拒绝。唤醒只能由 provider 执行 spawn 驱动，泄露的令牌无法把一台冻结或停机的 orb 拉起来，也活不过它的下一次唤醒。
- 新令牌的下发：新建容器经环境变量 `ORYN_ORB_TOKEN`；已存在的机器写入 `$HOME/.oryn/orb-token`（orbd 每次拨号前优先读它）并向 orbd 发 `SIGHUP` 让它立即重连。docker 驱动对 unpause / start / 已在运行的容器执行 `docker exec -i <orb> sh -c 'umask 077; … cat > $HOME/.oryn/orb-token'`（令牌经 stdin，不进 argv 与任务输出），再 `docker exec <orb> pkill -HUP -x orbd`；从快照新建容器时，镜像里带着源 orb 的令牌文件，驱动在 `docker start` 之前用 `docker cp` 把它换成新 orb 的令牌。

### dispatch 驱动的脚本契约（令牌轮换部分）

`ORB_OPERATION=spawn` 时脚本必须保证机器内的 orbd 用本次的 `ORYN_ORB_TOKEN` 连接：

1. 机器不存在：创建并以分离方式启动 `orbd run`，环境里带 `ORYN_URL`、`ORYN_ORB_ID`、`ORYN_ORB_TOKEN`。若机器来自快照，启动 orbd **之前**把 `$HOME/.oryn/orb-token` 覆盖为本次令牌（或删除它），否则 orbd 会读到源 orb 的令牌。
2. 机器已存在（解冻、开机或已在运行）：恢复运行后，以 orbd 的用户原子写入 `$HOME/.oryn/orb-token`（内容 = `ORYN_ORB_TOKEN`，权限 0600，先写临时文件再 `mv`），然后向 orbd 发 `SIGHUP`。
3. 环境变量 `ORYN_ORB_CA_PEM`：orynd 开启 `orb_tls` 时，这里是 link 用的 CA（PEM），没开时为空。新建机器时把它原样传给 orbd 的环境；orbd 只信这一张 CA，不用系统根证书。
4. 任务带非空的 `ORB_SNAPSHOT_ID` 时，脚本**必须**在输出里单独打一行 `ORYN_SNAPSHOT_USED=1`（机器确实从快照创建）或 `ORYN_SNAPSHOT_USED=0`（退回了基础镜像），以最后一行为准。控制面按这一行判断快照血统，不信 orb 自报。没有这一行就视为来源未知：这台 orb 不继承快照血统，也不发布任何新快照（快照本身不退役）。所以不打标记的脚本，项目快照最多被复用到 `max_age`，期间不会增量刷新。
5. 脚本退出码 0 = 成功。脚本在独立进程组中运行，任务超时（5 分钟）时整组被 `SIGKILL`；脚本退出后仍有后代进程占着 stdout / stderr 时，provider 至多再等 5 秒就返回（退出码 0 仍算成功），所以后台启动的 orbd 应把输出重定向到日志文件。

### 容器能力

docker 驱动默认给每个 orb 加 `--cap-drop NET_RAW --cap-drop NET_ADMIN`：即使在容器内是 root，也不能发原始包（ARP 欺骗同网桥上其它 orb 的 link）或改路由与防火墙。

### 桌面浏览器沙箱（M8）

- docker 驱动给新容器加 `--security-opt seccomp=<orb-seccomp.json>`（`internal/providerrt/orb-seccomp.json`）：moby 默认配置末尾追加一条规则，放行 `clone` / `unshare` 建 user namespace，供 Chromium 自己的沙箱使用（mount、setns、clone3 仍按默认只给有 CAP_SYS_ADMIN 的进程）。`extra_run_args` 里自带 `seccomp` 选项时不追加。代价是 orb 内进程能建 user namespace，内核暴露面略增，与 Chrome 官方推荐的容器配置相同。
- orbd 启动时探测能否建 user namespace：能则 Chromium 与 VS Code 带沙箱运行；不能（升级前建的旧容器、dispatch 驱动的机器）都退回 `--no-sandbox`（Chromium 保留警告条）。新建的 orb 才有沙箱。
- 镜像里的受管策略 `/etc/chromium/policies/managed/oryn.json`（root 所有，不随 Profile 传播）禁止安装扩展、代理固定直连，防止被注入的 agent 往共享 Profile 里植入扩展或代理。
- Chromium 与 VS Code 随镜像发布，**要定期重建 orb 镜像**（`make image`）以跟进安全更新；已存在的 orb 用的仍是旧镜像。

### 网络隔离

默认网桥上的 orb 能访问宿主上的所有服务（1Panel、数据库等）、局域网、云厂商元数据地址 `169.254.169.254`，以及彼此。建议给 orb 一个专用网桥，并用防火墙只放行 orb → 控制面：

1. 创建关闭容器间通信（icc）的专用网桥：

   ```bash
   docker network create --driver bridge \
     --subnet 172.30.0.0/24 --gateway 172.30.0.1 \
     -o com.docker.network.bridge.name=oryn0 \
     -o com.docker.network.bridge.enable_icc=false \
     oryn-orbs
   ```

2. 让 orb 使用它，并把控制面地址改为该网桥的网关：

   ```yaml
   # /etc/oryn/orynd.yaml
   listen: ["127.0.0.1:7600", "172.30.0.1:7600"]
   # /etc/oryn/provider.yaml
   orb_url: "http://172.30.0.1:7600"
   docker:
     extra_run_args: ["--network", "oryn-orbs"]
   ```

   orynd 要绑定 `172.30.0.1`，需在 Docker 建好 `oryn0` 之后启动（例如给 `orynd.service` 加 `After=docker.service`）。

3. 防火墙。orb → 宿主自身地址走 `INPUT` 链，orb → 其它网络走 `FORWARD` 链（Docker 为用户规则预留了 `DOCKER-USER`）：

   ```bash
   # 宿主：只允许 orb 访问控制面 7600 端口（-I 逆序插入，最终顺序为 已建立 → 7600 → 丢弃）
   iptables -I INPUT -i oryn0 -j DROP
   iptables -I INPUT -i oryn0 -p tcp -d 172.30.0.1 --dport 7600 -j ACCEPT
   iptables -I INPUT -i oryn0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

   # 转发：orb 不能访问私有网段、CGNAT 与链路本地地址（含元数据 169.254.169.254）；公网照常
   for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16; do
     iptables -I DOCKER-USER -i oryn0 -d "$net" -j DROP
   done
   ```

   DNS：容器经 Docker 内置解析器（`127.0.0.11`）转发到宿主配置的上游。上游若在私有网段（例如路由器 `192.168.1.1`），转发可能被上面的丢弃规则挡住——在 `extra_run_args` 里加 `--dns 1.1.1.1` 之类的公网解析器，或在丢弃规则之前放行该上游的 53 端口。规则重启后不保留：用 `iptables-persistent` / `netfilter-persistent save`，或写进 1Panel 使用的防火墙（firewalld / ufw）的对应配置。网桥启用了 IPv6 时，用 `ip6tables` 对 `fc00::/7`、`fe80::/10` 做同样的限制。

4. 验证（在某个 orb 里）：`curl -m3 http://172.30.0.1:7600/healthz` 成功；`curl -m3 http://169.254.169.254/`、访问宿主其它端口、访问另一个 orb 的地址都超时。

### 项目仓库镜像

orynd 在 `<data_dir>/repos/` 为每个有 `https://` 仓库地址的项目维护一份完整裸镜像，项目页的提交、文件与 README 都从这里读（见 [features.md「仓库浏览」](features.md#仓库浏览)）。目录（0700）里有：

- `<项目 id>.git`：外部仓库的裸镜像，全部 `refs/heads/*` 与 `refs/tags/*`（不含 PR 引用）。项目删除、仓库地址改指别的仓库、镜像超过 `max_mirror_gb` 时删除。Oryn 托管项目的正本也用这个名字，目录里多一个标记文件 `oryn-hosted`（见下节「托管仓库入口」），只有删除项目才会删它。
- `.home/`：控制面 git 的 `HOME` 与 `XDG_CONFIG_HOME`，与 orynd 用户自己的 git 配置隔开。
- `.lfs-cache/<项目 id>/`：镜像项目的 Git LFS 对象缓存，总量受 `max_lfs_cache_gb` 约束。托管项目的 LFS 对象在它自己的 `<项目 id>.git/lfs/objects/` 里，不进缓存。
- `orphaned/`：项目行已不存在的托管正本（例如把库恢复到了旧备份），按 `<项目 id>.git-<Unix 秒>` 保存，不自动删除；确认不要了再手工删掉。
- 克隆中的 `<id>.git.tmp-*` 与待删的 `<id>.git.trash-*`：启动时清掉。启动时还会删掉项目已不存在的镜像（托管正本移到 `orphaned/`）、托管仓库残留的 LFS 上传临时目录 `lfs/tmp/`，以及各仓库里 10 分钟前留下的 `HEAD.lock`、`packed-refs.lock`、`refs/**/*.lock`（断电或 OOM 后的残留）。

git 的执行方式：

- 环境从空白开始，只放 `PATH`、`HOME`、`GIT_CONFIG_NOSYSTEM=1`、`GIT_TERMINAL_PROMPT=0`、`LC_ALL=C` 与低速中止阈值（60 秒内低于 1000 B/s）；命令行固定带 `protocol.allow=never`（只开 https / http）与 `core.hooksPath=/dev/null`，修订与路径参数一律放在 `--end-of-options` 之后。系统与用户的 git 配置、钩子、模板都不生效。
- 凭据只经环境变量交给 git：github.com 用主人的 GitHub 账号令牌，其它主机用全局密钥 `git/<host>/token`，以 `GIT_CONFIG_*` 注入 `http.<origin>/.extraHeader`（`Authorization: Basic x-access-token:<令牌>`）并关闭重定向跟随。令牌不进命令行、不写磁盘配置，git 的错误输出写库或写日志前先脱敏。
- **担保主机**（github.com，或有令牌的主机）沿用 orynd 进程的 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`（大小写两种写法）；需要经代理出网的部署，镜像与 GitHub 集成走同一个代理。**未担保主机**（没有令牌的其它主机）不读这些变量，而是经回环上的公网地址守卫：只接受 CONNECT 443，连接时检查实际 IP，私有、回环、链路本地等地址一律拒绝（DNS 重绑定无效），项目页显示「仓库主机解析到内网地址，已拦截」。
- 镜像项目的 LFS 对象由 orynd 用纯 Go 客户端取，不需要 `git-lfs`：只请求仓库主机自己的 `<仓库>.git/info/lfs/objects/batch`（不读仓库里的 `.lfsconfig`），凭据与镜像拉取相同；下载地址在别的主机时不带 Oryn 的凭据，并一律经公网地址守卫（只能是公网 https）。同时至多 4 个对象在取（单个项目至多 2 个），一次取对象至多 15 分钟，60 秒内少于 60 KB 即中止；内容按指针校验大小与 sha256，不符不入缓存。
- 磁盘保护：克隆与拉取开始前、进行中每 5 秒检查数据盘可用空间，低于 `min_free_gb` 就不执行或终止，记 `no_space`；取 LFS 对象前同样检查（可用空间减去对象大小）。取消与超时先向 git 的进程组发 SIGTERM，10 秒后仍未退出才 SIGKILL，免得留下锁文件。
- 同时至多两个克隆或拉取；超时：首次克隆 15 分钟、增量拉取 5 分钟、读操作 20 秒。

### 托管仓库入口 `/git/`

Oryn 托管仓库（M24）经 `<public_url>/git/<项目 id>.git/…` 提供 smart-HTTP 与 Git LFS。orynd 直接执行 `git upload-pack` / `git receive-pack --stateless-rpc`（不需要 `git http-backend`），用与镜像相同的受控 argv 与空白环境。

- **只认设备令牌**：`Authorization: Basic <任意用户名>:<odt_…>`（git 凭据助手 `oryn git-credential` 给的就是这个）或 `Bearer odt_…`，令牌须存在且未吊销；从不读 Cookie，浏览器的登录会话访问不了。这条路径挂在 `/api/v1` 的鉴权组之外，先鉴权再查项目，项目是否存在不向匿名者泄露。鉴权失败回 401 加 `WWW-Authenticate: Basic realm="Oryn"`（LFS 端点另加 `LFS-Authenticate`）；带 `Sec-Fetch-Site` 的浏览器请求不回这两个头，免得弹出原生口令框。路径只接受固定的几种端点（正则匹配，不含转义字符），`..`、多余的段一律 404。吊销设备即撤销它对全部托管仓库的访问。
- **只读与写**：已归档项目的仓库只读（推送与 LFS 上传 403「项目已归档，仓库只读」）。每个项目同一时刻只处理一个推送，等锁超过 60 秒回 503「仓库正忙」。客户端断开时 `receive-pack` 不会被强杀（它读到 EOF 后自己退出并清理锁文件），请求体 120 秒没有新数据就关闭它的输入。
- **orb 只能访问本项目的仓库**：托管项目的 orb 经出站代理访问克隆地址，请求在控制面本地服务，入站的 `Authorization` 被丢掉、不注入任何令牌；同一 Oryn 地址上别的托管仓库回 403「这个 orb 只能访问本项目的托管仓库」；只读评审线程的推送、LFS 上传 403。orb 推上来的对象先过 `receive.fsckObjects`：带 `.git` 路径项或恶意 `.gitmodules` 的树进不来，主人之后在本机克隆不会中招（主人本机的推送不开 fsck，免得历史仓库因旧瑕疵被拒）。
- **隐藏 ref**：被删除或被强推覆盖的分支旧值留存在 `refs/oryn/saved/` 下 30 天；两个服务都带 `transfer.hideRefs=refs/oryn`，客户端看不见、也改不了这些 ref。
- **Git LFS**：批量接口 + basic 传输，不实现文件锁（锁接口 404）。下载地址指回同一个 `/git/` 路径，主人本机拿到的 action 头回显他自己的设备令牌（重新拼成 `Basic oryn:<令牌>`，响应 `Cache-Control: no-store`）；对象下载一律 `attachment` 加 `nosniff`，不会在 Oryn 的源下被当页面打开。上传边写边校验大小与 sha256，不符不落盘。
- **资源**：托管 git 进程全局至多 8 个、每台 orb 2 个，没有名额回 503 加 `Retry-After: 5`；LFS 对象传输另有名额（全局 16、每台 orb 8，排队至多 30 秒）。仓库与 LFS 对象不设大小上限；推送开始前与进行中每 5 秒、LFS 上传每写 64 MiB 检查一次数据盘，可用空间低于 `repos.min_free_gb` 时拒绝或终止（推送 503「控制面磁盘空间不足」，LFS 上传 507）。
- **推送后 Webhook**：只有主人能设置（不在助手的工具里）。地址不检查是否内网：等同主人自己发请求。签名密钥用主密钥加密保存，附加数据绑定项目（`project-webhook:<项目 id>`），从不经 API 返回。投递用 orynd 进程的出站环境（`http.DefaultTransport`，会读代理变量），不跟随重定向。

### git 出站策略

orb 内的 git 请求经 orbd 的本机代理（`127.0.0.1:7811`）到达控制面，由控制面注入 `git/<host>/token` 密钥后转发到 `https://<host>/…`。令牌从不进入 orb。orbd 在 orb 的 `~/.gitconfig` 里写两类条目：`url.http://<前缀>.insteadOf https://<前缀>`，只把 https 换成 http，主机与 `owner/repo` 路径不变；以及 `http.http://<host>/.proxy http://127.0.0.1:<egress 端口>`，让这台主机的 http 请求以代理形式（请求行 `GET http://<host>/<owner>/<repo>/info/refs…`）发给本机代理。这样 `git remote -v` 显示的仍是 `http://github.com/<owner>/<repo>`，`gh` 不带 `-R` 也能从 remote 推断仓库。需要 git 支持按地址配置 `http.<url>.proxy`（1.8.5 起）。控制面按 orb 所属项目决定放行范围：

- **有项目且项目 `repo_url` 为 `https://` 地址的 orb**：只放行该项目仓库，可读可写（`git-upload-pack` 与 `git-receive-pack`，以及该仓库的 Git LFS 接口）。仓库路径须与 `repo_url` 完全一致（不区分大小写，忽略 `.git` 后缀）。同一主机上的其它仓库、其它主机一律拒绝（403，原因「这个 orb 只能访问项目仓库」）。orb 内的 `insteadOf` 只覆盖项目仓库这一个地址前缀，其它 `https://` git 地址照常直连公网、不带令牌，公开仓库仍可匿名克隆。
- **无项目的 orb**（以及项目没有 `https://` 仓库地址的 orb）：配置了 `git/<host>/token` 的主机上，任意仓库只读。只放行 `info/refs?service=git-upload-pack`、`git-upload-pack` 与 LFS 的读操作，推送（`git-receive-pack`）、LFS 上传与改动文件锁一律拒绝（403，原因「没有项目的 orb 对 git 仓库只读（不能推送，也不能上传 Git LFS 对象或改动文件锁）」）。这类 orb 的 `insteadOf` 覆盖整个主机。
- **github.com（M15）**：上面两条只适用于 PAT 主机。orb 由 GitHub 集成下发凭据时，github.com 改由出站代理注入线程身份槽位的 OAuth 令牌，不按项目仓库限制：orb 能读写哪些仓库取决于该身份账号自身的权限，以及项目开启的门禁。详见第 13 节。
- **Oryn 托管仓库（M24）**：托管项目的 orb 多一个 git 主机条目 `<public_url 的 host[:端口]>/<路径前缀>/git/<项目 id>.git`（不需要令牌），克隆地址按上面的方式改写后经本机代理回到控制面；该主机上只放行本项目的托管仓库，在控制面本地服务，不转发、不注入令牌（见第 7 节「托管仓库入口 `/git/`」）。托管项目没有 `repo_url`，其它主机按「无项目」的只读规则。`public_url` 是 `http://localhost:8080` 这类开发地址时同样适用；它被 `NO_PROXY` 覆盖时按上面的失败关闭规则直连并失败。
- **路径与端点**：控制面接受两种请求形式，规则相同。一种是 orbd 现在写的代理形式（absolute-form `http://<host>[:<端口>]/<owner>/<repo>/…`）；它只能进 git 路由，`host[:端口]` 就是主机键（同路径形式里的主机段），不是 `http://` 的绝对地址回 400，既不是 git 端点、也没有有效的 LFS 门票（见下）时回 403。另一种是 `/git/<host>/<owner>/<repo>/…` 路径形式，M15 之前的 orbd 用这种。只放行 git smart-HTTP 端点（`info/refs`、`git-upload-pack`、`git-receive-pack`）与 Git LFS 接口，其它路径返回 404（代理形式为 403）。路径里出现 `.` 或 `..` 段、空段（`//`），或 `path.Clean` 后与原路径不同，一律返回 400。这包括 `%2e%2e` 这类编码形式，因为校验针对解码后的路径。git 路由不接受任何 `oryn-ph-…` 占位符。被拒的请求回 `text/plain` 的说明（LFS 接口回 `application/vnd.git-lfs+json` 的 `{"message"}`）：按策略拒绝的以 `oryn: ` 加原因开头，其余为 `oryn egress: …`，git 与 git-lfs 会原样显示。
- **`NO_PROXY` 覆盖的主机不经出站代理（失败关闭）**：改写之后的地址是明文 http。git 一旦按 `NO_PROXY` / `no_proxy` 绕过本机代理，就会直连 `<host>:80`：请求不带令牌，但网络上的中间人能替换 refs 与代码，对象哈希证明不了对端是谁。因此 orbd 在写这些条目之前，先读 setup 的环境（orbd 进程环境加上 setup 下发的 env）里的 `NO_PROXY` 与 `no_proxy`，两种写法都读，任一命中就算。判定取 libcurl（git 用它，curl 8.23 `proxy_check_noproxy`）与 Go httpproxy（git-lfs 用它）两种规则的并集，宁可多判。下面几种都算命中：整串为 `*` 或任一条目为 `*`；条目等于主机名，或是主机名的后缀（`github.com`、`.github.com`）；`*.domain` 命中子域；`host:端口` 的端口与请求端口相同（http 缺省 80）；主机是 IP 时按 IP 或 CIDR 比较。命中的主机不写 https→http 改写，也不写代理。它的 remote 保持 `https://`，经 TLS 直连、不带令牌：公开仓库照常可读，私有仓库与推送会明确失败，gh 仍能从 https remote 推断仓库。setup 日志记一条 `warning: NO_PROXY/no_proxy covers <host> …`，把该主机从 `NO_PROXY` 和 `no_proxy` 里去掉、重新唤醒 orb 就会恢复。orbd 不改 `NO_PROXY` 本身，以免改变其它流量的代理语义。M16 合并记忆的 `NO_PROXY` 时会保留已有条目，所以项目 env 或镜像里的 `NO_PROXY` 会原样生效。这个判定只看 setup 时的环境：agent 在自己的 shell 里临时设置的 `NO_PROXY` 不在防护范围内。agent 本来就不可信，本来也能直连公网下载代码，只是这样拿不到令牌。
- orbd 每次执行 setup / resume 时，先删掉自己以前写入的全部条目，再按当前策略重写。要删的条目有三类：同址的 https→http `insteadOf`；值为本机回环端口的 `http.http://<host>/.proxy`，包括 orbd 重启前用过的旧端口；M15 之前的 `url.http://127.0.0.1:<端口>/git/<前缀>.insteadOf`。用户自己写的改写与代理设置保留。项目设置变更后，在下一次唤醒时生效。
- **Git LFS（M24）**：放行 `<仓库>.git/info/lfs/` 下的批量接口与锁接口，与 smart-HTTP 用同一套仓库与读写规则、注入同一个令牌：

  |端点|方法|读写|
  |---|---|---|
  |`info/lfs/objects/batch`|POST|按请求体里的 `operation`：`download` 读，`upload` 写|
  |`info/lfs/locks`|GET / POST|GET 读，POST（建锁）写|
  |`info/lfs/locks/verify`|POST|写（推送前的锁校验要求推送权限）|
  |`info/lfs/locks/<id>/unlock`|POST|写|

  batch 的请求体由代理解析后重新序列化再转发：上游收到的字节就是代理据以判断读写的那一份，`transfers` 固定为 `basic`，未知字段丢掉。带压缩编码回 415；超过 1 MiB 或多于 1000 个对象回 413；不是合法 JSON、`operation` 不是 download / upload、`hash_algo` 不是 sha256、oid 不是 64 位十六进制或 size 为负回 422，都不转发。github.com 的只读评审线程不能上传、建锁、解锁或 locks/verify。

  batch 的 200 响应同样解析后重写（失败关闭）：与上游同主机、同 scheme 与端口、路径规范的传输地址，去掉 action 里除 `Accept`、`Content-Type`、`Transfer-Encoding` 以外的头（包括上游回显的 Authorization 与新签发的凭据），改写成 orb 侧的 `http://<主机>/…` 经代理访问，并登记一张门票（下载 GET、上传 PUT、verify POST；有效期取 1 小时与 action 自带过期时间的较早者，每台 orb 至多 4096 张）。orb 用门票访问时，代理按当时的策略重新判定读写并注入令牌；门票只对代理认不出的路径生效，不能遮蔽已识别的 git 与 LFS 端点。不能交给 orb 的 action（地址不是绝对的 http / https 或带用户名密码；同主机但 scheme、端口不同、路径不规范或已过期）让该对象的 actions 清空并带 `error.code = 422`。其它主机的地址（对象存储的签名 URL）原样交给 orb 直连，只删掉值里含主机令牌的头。响应不是合法 JSON、超过 4 MiB、带压缩编码，或 batch 返回 200 以外的 2xx，一律换成 502 的 LFS 错误体。M15 之前的 orbd（路径形式）能用批量接口与锁接口，同主机的传输地址用不了。
- **orb 内检出不下载 LFS 对象（M24）**：orbd 的 clone 与 `repo.update` 带 `GIT_LFS_SKIP_SMUDGE=1`，检出只写指针文件；之后在仓库有 `filter=lfs` 文件时统一 `git lfs pull`。取不下对象（或 orb 里没有 `git-lfs`）计入 setup 失败（`setupError` 里含 `git lfs pull: …` 或 `git-lfs is not installed`，与脚本失败同时发生时以 `; ` 接在后面）。跳过 smudge 照样安装 pre-push 钩子，orb 内推送时 LFS 对象会上传。agent 自己执行的 git 不受影响，仍按镜像里的系统配置 smudge。
- **推送记录（M24）**：上游以 200 应答 `git-receive-pack` 时，出站代理把这次推送的 ref 更新交给控制面；推的是 orb 所属项目的仓库、orb 有唯一的未归档工作线程时，`refs/heads/*` 的更新（旧提交 → 新提交，删除分支除外）记进 `repo_pushes` 并触发一次镜像拉取，拉到新提交后展开成「提交 → 线程」关联。只看 HTTP 200，不解析 report-status；被上游以 `ng` 拒绝的 ref 在新提交出现在镜像里之前不会展开，24 小时后记为 `missing`。

### 模型密钥绑定

`models.yml` 中，`providers.<key>.apiKey` 只能引用本 provider 自己的密钥 `omp/models/<key>/apiKey`。只有这一个密钥会走环境变量降级；它若已绑定上游主机（`hosts` 非空），就不允许降级。`models.yml` 里不允许引用 `git/*` 密钥。其它 `${oryn:secret:…}` 引用必须属于 `hosts` 非空的密钥，而且只绑定到 origin 主机与 `hosts` 匹配的路由。`hosts` 为空的密钥只能注入到它自己 provider 的路由，其它任何路由都不行。导入时，provider 密钥的 `hosts` 包括 provider `baseUrl` 与各模型 `baseUrl` 的全部主机。经代理的 WebSocket / Upgrade（101）连接只对响应头做真实值回显脱敏，不改写升级后的字节流，否则会破坏其分帧。

## 8. 协调宿主（Space 与全局助手）

Space 协调者与全局助手的 agent 不跑在 orb 里，而是跑在控制面这台机器上：orynd 内置一个名为 `@host` 的 provider（在 provider 列表里看不到），需要时拉起子进程 `orynd coordinator-host`，子进程先把自己关进操作系统沙箱，再以协调档启动 omp（每个协调对话一个进程）。子进程唯一的对外通道是 orynd 交给它的 socketpair；模型请求经它内部的代理端口回到 orynd，由 orynd 注入真实密钥，协调 agent 拿不到任何密钥。全局助手（不含 Space 协调者）的联网与 MCP 也走同一个代理端口，由 orynd 的正向代理放行公网目标，见下文「全局助手联网与 MCP（M19）」。

### 安装 omp

协调宿主用的 omp 与 orb 镜像是同一份发布产物（`scripts/fetch-core.sh` 按 `core/source.json` 校验后放在 `build/core/<arch>/omp`）：

```bash
scripts/fetch-core.sh x64
sudo install -D -m 0755 build/core/x64/omp /usr/local/lib/oryn/omp
```

不放 `/usr/local/bin`，免得与宿主上你自己用的 `omp` 互相遮蔽。orynd 启动时与每次拉起协调宿主前都会核对它的 sha256 与编进 orynd 的 `core/source.json` 是否一致；不一致时协调功能不可用，原因写在日志与 `GET /api/v1/coordinator` 里（`omp at … does not match core/source.json`），普通线程不受影响。本地改过 Core、确实要用自己的构建时，设 `coordinator.verify_omp: false`。

### 配置

`orynd.yaml` 的 `coordinator` 段（全部可省）：

```yaml
coordinator:
  omp_path: "/usr/local/lib/oryn/omp"   # 缺省值
  dir: ""                               # 缺省 <data_dir>/coordinator：omp 副本、各协调 orb 的 HOME、host.log
  user: ""                              # orynd 以 root 运行时降权到的账号；空 = 自动使用本实例的专用账号（见下）
  egress_port: 7811                     # 沙箱内模型代理监听的回环端口
  verify_omp: true
  model: ""                             # 协调对话的默认模型（provider/id）；空 = omp 配置的默认模型
  web_allow_hosts: []                   # M19：全局助手联网时额外允许连接的非公网目标（小写 host 或 host:port），见下文
```

`dir` 下的内容：`bin/omp`（`omp_path` 的 0555 副本，源文件大小或 mtime 变了才重拷）、`homes/`（0755，属于 orynd）与其下每个协调 orb 一个的 `homes/<orb id>/`（0700，属于降权账号，协调 agent 唯一可写的目录：会话文件、`.omp/agent/` 里下发的 `models.yml` 与 `config.yml`（有助手可用的 MCP 服务器时还有 `mcp.json`）、natives 缓存、`.oryn/coordinator/prompts/<线程 id>.md` 系统提示词、`.oryn/coordinator/` 下的覆盖层（`overlay.yml`、`overlay-web.yml`，以及 M26 研究员的 `overlay-research-web.yml` 与 `overlay-research-private.yml`）、生图结果所在的 `tmp/`）、`host.log`（子进程的 stderr，超过 5 MiB 轮转为 `host.log.1`）。`dir` 必须属于 orynd 的运行用户，且不能被组或其他人写。

每个协调 orb 有自己的 HOME：删除一个协调 orb 只删它自己的 `homes/<orb id>/`；拉起新一代协调 orb 之前先停掉旧一代的子进程，所以同一时刻至多一个协调宿主子进程。旧版本的 `<dir>/home`（所有协调 orb 共用）在 orynd 启动时迁到 `homes/<当前协调 orb>/`，没有协调 orb 时删除。

### 专用账号（root 部署）

orynd 以 root 运行时，沙箱降权到一个只属于这个 Oryn 实例的系统账号，而不是与其它服务共用的 `nobody`：

- `coordinator.user` 为空（缺省）、`nobody`、`nfsnobody`，或解析出 uid 65534 的账号时，使用 `oryn-coord-<8 位十六进制>`，后缀是协调目录绝对路径的 fnv32a（同一台机器上不同实例的账号不同）。后三种写法记一条警告（共享身份，已改用专用账号），配置不用改。
- 账号不存在时 orynd 执行一次 `useradd --system --no-create-home --home-dir /nonexistent --shell <nologin> --user-group oryn-coord-<后缀>`（uid 由系统分配）。`useradd` 不存在或失败（例如单元带 `ProtectSystem=strict`，`/etc` 只读）时协调功能不可用，原因里有失败输出与这条命令，手工执行一次即可；也可以在 `coordinator.user` 指定一个你自己建的专用账号。
- 已有部署升级后，下次拉起协调宿主时 HOME 的属主递归改成新账号，会话照常接续。`GET /api/v1/coordinator` 的 `sandbox.uid` 就是这个账号的 uid（`id -u oryn-coord-<后缀>`）。
- 每次拉起前都会检查这个账号此刻没有被本实例以外的进程使用（扫描 `/proc/<pid>/status` 的 `Uid:`，本实例的协调宿主进程树不算）；有就不可用，原因「coordinator account "…" (uid N) is used by other processes (pid P); use a dedicated account」，orynd 不会去杀它。显式指定的账号同样检查，所以不要指定登录用户或其它服务的账号。
- 卸载时可以 `userdel oryn-coord-<后缀>`。

### 启动条件

下面任何一条不满足，协调功能就不可用（建 Space、开助手对话时回 `409 coordinator_unavailable` 并带原因），其它功能照常。没有「关掉沙箱」的开关。

沙箱只靠 Landlock 管文件、TCP 端口与信号；内核的 Landlock ABI 低于某项能力所需的版本时，go-landlock 的 BestEffort 会悄悄丢掉对应规则，所以缺的那一项必须由别的机制补上，补不上就 fail-closed：

| 要求 | 内核 / 条件 | 不满足时的原因（`GET /api/v1/coordinator`） |
|---|---|---|
| Landlock ABI ≥ 2（跨目录改名的 refer 权限） | 内核 ≥ 5.19，Landlock LSM 已启用（`cat /sys/kernel/security/lsm` 里有 `landlock`） | 「内核 Landlock ABI N < 2…」 |
| TCP：有网络命名空间，**或** Landlock ABI ≥ 4（按端口限制 TCP） | 能建网络命名空间（root，或开放了非特权 user namespace）；否则内核 ≥ 6.7 | 「内核 Landlock ABI N < 4 且没有网络命名空间，无法限制 TCP 出站…」 |
| 信号：有 PID 命名空间，**或**沙箱降权到独立用户（orynd 以 root 运行），**或** Landlock ABI ≥ 6（信号作用域） | 命名空间与网络命名空间一起建；否则 root 部署，或内核 ≥ 6.12 | 「内核 Landlock ABI N < 6、没有 PID 命名空间且沙箱与 orynd 是同一用户，无法阻止向 orynd 等同用户进程发信号…」 |
| orynd 以 root 运行时：降权账号可用（自动账号能建出来或已存在；显式账号存在）、不是 root、没有被本实例以外的进程使用，且 `coordinator.dir` 的每一级父目录对它可进入（other 有 `x` 位） | 默认的 `<data_dir>/coordinator` 在 0700 的 `data_dir` 下，root 部署必须把 `dir` 移出来（例如 `/var/lib/oryn-coordinator`） | 「cannot create the coordinator account …」「… is used by other processes …」「… is not reachable by user …」等 |

其余随 ABI 出现的 Landlock 能力不设门槛：按路径的 `truncate(2)`（ABI 3）由 seccomp 无条件拒绝；设备 ioctl（ABI 5）只涉及白名单里的 `/dev/null`、`/dev/{u,}random`、`/dev/zero`；abstract 与带路径的 unix socket（ABI 6 / 9）、UDP（ABI 10）由 seccomp 一律禁止。Debian 13 默认内核（6.12，ABI 6）无论有没有命名空间都可用。

### 沙箱里有什么

| 层 | 内容 |
|---|---|
| 启动开关 | omp 以 `--no-tools --tools <内置工具> --no-skills --no-extensions --no-rules --no-context-files --no-lsp` 启动。Space 协调者的内置工具只有 `read,write`；全局助手的是 `read,write,web_search,generate_image`（`read` 也能读 URL）。两者各带一份覆盖层：强制关闭 autolearn、memory、checkpoint、goal、plan、lsp、项目 MCP、marketplace 自动更新、启动检查更新、direnv；`generate_image` 只在全局助手的覆盖层（`overlay-web.yml`）里打开，生成的图片写进 `~/tmp/`。环境变量从零构造，只有 `HOME PATH LANG TMPDIR PI_RPC_EMIT_TITLE PI_NOTIFICATIONS OTEL_SDK_DISABLED PI_INTENT_TRACING ORYN_THREAD_ID`（`PI_INTENT_TRACING=1`：Core 19 起 RPC 默认不要求工具调用带 intent，Web 的工具卡要显示它）；全局助手另有 `PI_PROXY` 与 `ORYN_MCP_BASE`（都是 `http://127.0.0.1:<egress_port>`），以及 MCP 路由凭据 `ORYN_MCP_TOKEN` |
| 文件 | 下发 `models.yml` 与 `config.yml`（`models.yml` 里只有占位密钥），有助手可用的 MCP 服务器时再加一份 `mcp.json`（只有类型、代理地址与引用 `${ORYN_MCP_TOKEN}` 的凭据头模板，不含上游的任何请求头）；每次协调 agent 启动前重写一遍（助手用 `write` 改过的会被还原）。全局助手文件库同步到 `~/library/{shared,threads}/`（只读）。`write` 能写 HOME 里任何地方，系统提示要求助手只写 `~/library/out/`；能存进文件库的只有 `~/library/out/` 与 `~/tmp/` 下的文件；cwd 是空目录 |
| 控制面 | 协调线程的 RPC 命令走白名单；`bash`、`eval`、终端、设置写入等命令到不了 omp |
| Landlock | 只有本 orb 的 `<dir>/homes/<orb id>` 可写（不可执行）；可执行的只有 omp 本体与它的 ELF 解释器；只读可见 `/usr/lib`、`/lib*`、`/usr/share/zoneinfo`、`/proc`、`/sys/devices/system/cpu`、`/sys/fs/cgroup` 与少量 `/etc` 文件（清单见 `docs/protocol.md`「M9」）；ABI ≥ 4 时 TCP 只能 connect `egress_port`、不能 bind 任何端口（代理端口由子进程在施加沙箱之前监听好再交给 orbd）；ABI ≥ 6 时信号不出沙箱 |
| seccomp | 非本机架构与 x32 调用直接杀进程；`clone3` 回 `ENOSYS`；`socket` 只放行 TCP（`AF_INET` / `AF_INET6` 的 `SOCK_STREAM`），`AF_UNIX` 一律拒绝，netlink、UDP、MPTCP 等也拒绝；`socketpair` 只放行 `AF_UNIX` + `SOCK_STREAM`（Bun 启动时要建一对，拒绝会直接 abort；这对 socket 生来已连接，connect / sendto 别处都失败），datagram 与 seqpacket 拒绝；`prlimit64`、`sched_setaffinity`、`sched_setscheduler`、`sched_setparam`、`sched_setattr` 只能作用于自身（pid 为 0），`setpriority` 只能 `PRIO_PROCESS` + 自身，`ioprio_set` 只能 `IOPRIO_WHO_PROCESS` + 自身（同一用户时改不了 orynd 等进程的资源上限与调度）；带 `CLONE_NEW*` 的 `clone`、`unshare` / `setns` / 挂载族、`ptrace` 与跨进程内存访问、按路径的 `truncate`、`bpf`、`io_uring`、`keyctl`、内核模块、SysV IPC 等一律拒绝 |
| 权限 | `no_new_privs`；能力集与边界集清空；root 部署时降权到本实例的专用账号（见上） |
| 命令行 | 系统提示词写在 HOME 里的文件中（0600），omp 的命令行只带它的路径：`/proc/<pid>/cmdline` 里看不到提示词正文 |
| 命名空间（能建就建） | 独立的网络（只有回环）、PID、IPC、UTS 命名空间；非 root 时外加一个只映射自己的 user namespace |

### 不同环境得到的隔离

| 环境 | 命名空间 | Landlock | 降权 | 结果 |
|---|---|---|---|---|
| 开发机（root，Arch，内核 7.x） | 有 | 有 | 专用账号 `oryn-coord-<后缀>` | 全部 |
| systemd 生产单元（`User=oryn`；Debian 13 / Arch / Fedora） | 有（非特权 user namespace） | 有（Debian 13 = ABI 6） | 不适用 | 全部（文件靠 Landlock，不靠 uid） |
| systemd 生产单元，Ubuntu 23.10+ 默认 AppArmor | 探测失败时不用 | 有（6.8 = ABI 4） | 不适用 | 不可用：没有 PID 命名空间、同一用户，ABI < 6 拦不住信号（以 root 运行 orynd 或放开 user namespace 即可） |
| Docker 里的 orynd（默认 seccomp，容器内 root） | 无 | 宿主内核 Landlock ABI ≥ 4，且 Docker ≥ 20.10.18 | 专用账号（容器里要有 `useradd`，或预先建好并写进 `coordinator.user`） | 文件 + 按端口限制的网络；ABI < 4 时不可用 |

omp 的原生文件锁（会话所有权租约等）在 Linux 上是 `~/.omp/run/…` 下的旁路文件加 OFD 写锁（Core 19 起上游即如此；此前 Oryn Core 靠 `PI_FILE_LOCK_BACKEND=sidecar` 切换，该变量已不再设置）。它对旧版 omp 的 abstract socket 探测在 `socket(AF_UNIX)` 被拒时读作「未持有」（Oryn Core 的补丁，见其 ORYN.md），沙箱因此可以禁止 `AF_UNIX` 的 `socket(2)`（只留 Bun 需要的、生来已连接的 stream `socketpair`）：宿主上的 abstract socket 与 `/dev/log` 这类带路径的 0666 socket 在任何内核上都联系不上。

`deploy/systemd/orynd.service` 不用改：`<data_dir>/coordinator` 在 `ReadWritePaths` 之内，单元没有限制命名空间与系统调用。**如果你自己给单元加了 `RestrictNamespaces=`，协调宿主会退到「按端口限制」模式**；加了 `SystemCallFilter=` 且挡掉了 `landlock_*` 或 `seccomp`，协调功能会不可用。

**代理端口**：有网络命名空间时 `egress_port` 在命名空间里，与宿主上的端口互不相干。没有命名空间（「按端口限制」模式）时它直接绑定在宿主的 `127.0.0.1` 上，必须是宿主上没人用的端口；被占用时协调功能立即不可用，`GET /api/v1/coordinator` 与助手页给出原因「出站代理端口 127.0.0.1:<port> 已被占用…」，`coordinator check` 也报同一条，此时在 `coordinator.egress_port` 换一个端口后重启 orynd（不会自动换：端口同时写进 omp 的配置与沙箱的连接白名单）。在这种模式下，orynd 运行着协调宿主时再跑 `coordinator check`，端口正被它自己占着，同样会报这一条，先停 orynd 再检查。

root 或 Docker 部署：`coordinator.dir` 放在降权账号进得去的地方（容器里挂一个卷），`data_dir` 保持 0700。专用账号建在容器里时，重建容器后会再建一次（uid 可能不同，HOME 属主随之改过来）。

### 全局助手联网与 MCP（M19）

全局助手的对话（含主线）启动 agent 时，orynd 在 `agent.start` 里带 `web` 与 `mcpToken`。只有带了 `web`，协调宿主才给这个 omp 打开 `web_search` 与 `generate_image`、用 `overlay-web.yml`，并在环境里设 `PI_PROXY`、`ORYN_MCP_BASE` 与 `ORYN_MCP_TOKEN`（取 `mcpToken`）。Space 协调者的 agent 两者都不带：内置工具仍只有 `read` / `write`，生图关闭，环境里没有这三个变量，所以不联网，也没有 MCP 工具，与 M19 之前相同。

**出站路径**：沙箱里仍然只能连回环上的 `egress_port`。omp 的全局 `fetch` 按 `PI_PROXY` 把请求交给这个端口：https 目标发 `CONNECT host:port` 建隧道，http 目标发代理形式的请求（`GET http://host/…`）。请求经 link 回到 orynd 的出站代理，由 orynd 解析域名、连接上游。代理不解密 TLS：https 的证书由沙箱里的 omp 用 Bun 自带的根证书校验。`web_search`（grounded 搜索）与 `generate_image` 调用的模型按全局 omp 配置的模型角色选择，模型请求照常经模型路由注入密钥。

**公网判定**：orynd 在建立连接的那一刻对实际要连的 IP 做判定（`net.Dialer.Control`），不对域名做。以下地址一律拒绝，回 403 `destination is not a public address`，并以 Info 级记一条带目标主机的日志：

- 回环、私网（`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`fc00::/7`）；
- 链路本地（`169.254.0.0/16`，含云厂商元数据地址；`fe80::/10`）；
- 组播、未指定地址、`0.0.0.0/8`、CGNAT `100.64.0.0/10`、基准测试网段 `198.18.0.0/15`。

因为判定针对实际连接的 IP，解析到内网的域名（例如 `127.0.0.1.nip.io`）和 DNS 重绑定同样被拒。直接写内网或回环的字面 IP 时，omp 的代理包装层不走代理、直接连接，这在沙箱里本来就连不通。其它拨号错误回 502，拨号超时 10 秒。

**`coordinator.web_allow_hosts`**：公网判定的例外清单，元素是小写的 `host` 或 `host:port`，与请求里的主机名比较。命中的目标不做公网判定，用来放行主人明确想让助手访问的内网服务（如内网 wiki）。它只对域名有意义（字面 IP 不经代理，见上）。只在 orynd 启动时读取，改动后重启 orynd。清单里的服务对助手完全可达：网页内容里的注入指令同样能让助手去读它们，只放你愿意让助手读到的地址。

**联网总开关**：「设置 → 全局助手 → 联网」（`PUT /api/v1/settings/assistant` 的 `webAccess`，缺省开）。关闭后出站代理对一切 CONNECT 与代理形式的请求回 403 `web access is turned off`，`web_search` 与读网页随之失败，系统提示词也会告诉助手联网已关。保存后 orynd 立刻刷新协调宿主的出站策略，并让所有未归档的助手对话在静止后用新配置重启 agent。总开关不管 MCP，MCP 服务器在同一页面逐个开关。

**MCP 路由**：助手用的 MCP 服务器来自全局 omp 配置里的 `mcp.json`（「设置 → Agent」或 `oryn config push`），orynd 解析它：

- 可用：传输是 HTTP 或 SSE、请求头能由 orynd 注入的服务器。头里的 `${VAR}` 与 `${VAR:-默认}` 按 env 模式的密钥展开。
- 不可用，并在设置页标出原因：stdio（本地进程型）、需要 OAuth（`auth` / `oauth` 字段）、头的值由命令生成（以 `!` 开头）、头引用了不存在的变量、地址无效。

可用且没被关掉的服务器按名字排序后依次得到路由键 `m0`、`m1`……。下发到沙箱的 `mcp.json` 每项只有 `{"type": "http" | "sse", "url": "${ORYN_MCP_BASE}/mcp/m<i>", "headers": {"X-Oryn-MCP-Token": "${ORYN_MCP_TOKEN}"}}`，不含上游的任何头；omp 展开地址与头里的环境变量。

**MCP 路由凭据**：orynd 每次启动随机生成一个凭据，放进协调宿主的出站策略，并只经 `agent.start` 的 `mcpToken` 交给全局助手的 agent（orbd 以 `ORYN_MCP_TOKEN` 注入）。出站代理对 `/mcp/` 下的每个请求（任何方法、任何子路径，含 SSE 的 GET）先核对请求头 `X-Oryn-MCP-Token`（常量时间比较）：头缺失、值不对，或策略里没有凭据，一律 403，并且这一步在路由查找之前，不暴露某个路由键是否存在。核对通过后去掉该头再转发。上游回重定向时，同源且在该服务器地址路径之下的改写回 `/mcp/m<i>/…` 继续走代理，跳到别的主机或路径之外的一律回 502：客户端跟随重定向会保留自定义请求头，不拦就会把凭据带到别的服务器。所以 Space 协调者既不会自动连上 MCP（没有 `ORYN_MCP_BASE`），直接请求 `/mcp/m<i>` 也会被拒（没有凭据）。协调宿主随 orynd 一起重启，凭据随之更换。

出站代理把请求转发到原地址：先丢掉入站的 `Authorization`，再注入配置的头；名字含 auth、token、key、secret、cookie 的头，其值若出现在响应里会被抹掉。MCP 路由不做公网判定（主人配置的地址可以在内网），也不受联网总开关约束。全局 `mcp.json` 变化后，助手对话在静止后重启并重新下发。

### 后台研究（M26）

「设置 → 全局助手 → 后台研究」的总开关缺省关闭。打开后，orynd 的研究服务按节奏驱动协调宿主里两个隐藏的研究员对话（不进对话列表、搜索、主人记忆与通知，不能归档或删除）。它们与全局助手在同一个协调宿主里，但启动参数不同（`agent.start` 的 `variant`）。研究员的 agent 只在一次研究运行期间存在，运行结束就停掉，下一次运行拉起新进程，上一次的上下文不会带进来。三条管线的节奏与 REST 见 [`protocol.md`「M26」](protocol.md#m26全局助手的后台研究)。

**公网研究员**（`research_web`）手里没有私有数据：

- 内置工具只有 `web_search` 与 `read`，不加载技能，宿主工具表为空。环境有 `PI_PROXY`（联网与公网判定同上节），没有 `ORYN_MCP_BASE` 与 `ORYN_MCP_TOKEN`。
- 覆盖层 `overlay-research-web.yml` 打开 Oryn Core 的 `read.remoteOnly`：`read` 只读公网 http(s) 网址和本会话的 `local://`。本地路径、`file://`、`skill://` 等内部协议、`localhost`，以及写成字面量的回环、私网、链路本地、CGNAT 与 `0.0.0.0/8` 地址，一律得到 `This session can only read public web pages (http/https URLs).`。
- 同一覆盖层把 `prompt.fileMentions` 设为 `false`，关掉提示词里 `@路径` 的自动读取。这个读取不经过 `read` 工具，`read.remoteOnly` 管不到它。
- 提示词由 orynd 拼装，只含话题的种类、标题、问题、上一次的报告与当天日期；当时的回答（基线）与任何对话内容都不交给它。它即使被网页注入，也没有私有数据可泄露。
- 助手设置的联网总开关关着时，公网跟进不运行。

**私有研究员**（`research_private`）不能上网：

- 只有 `--no-tools`，没有任何内置工具，读不了文件；覆盖层 `overlay-research-private.yml` 同样关掉 `@路径`；环境没有 `PI_PROXY`。
- 宿主工具只在内部关联与 MCP 检查的运行里有，全部只读：`list_threads`、`read_thread`、`list_schedules`、`list_projects`、`get_stats`，以及助手记忆开着时的 `search_history`、`recall_memory`、`recall_engineering`。挖掘运行一个宿主工具都没有，对话摘录直接写在提示词里。
- 只有 MCP 运行里，orynd 才把**研究凭据**作为 `mcpToken` 交给它（orbd 同时设 `ORYN_MCP_BASE`）。它拿不到全局助手的 MCP 凭据。
- 私有研究员的覆盖层另加 `mcp: { shared: false, startupTimeoutMs: 0 }`，在进程内直接连 MCP 服务器，不等沙箱里没有的 omp daemon。MCP 运行时，orynd 等勾选的服务器在研究员里都连上且列出了工具，才投递提示词，最多等 30 秒。超时的运行记失败，不推进游标。研究员说某个服务器用不了时（`unavailable`）也一样。所以服务器连不上时，这段时间的邮件或日历不会被当成「没有新内容」跳过。

**研究凭据与 `/mcp/` 过滤**：orynd 每次启动另生成一个研究凭据，与助手凭据一起放进协调宿主的出站策略，请求头同为 `X-Oryn-MCP-Token`。持研究凭据的请求：

- 只能访问主人在「研究可用工具」里勾选过工具的服务器，其余 403。名单只在研究总开关与 MCP 检查都开着时生效；在助手设置里关掉的服务器，研究同样用不了。
- `POST` 的请求体必须是单个、未压缩、不超过 1 MiB 的 JSON-RPC 对象。握手、`ping`、`tools/list` 与通知放行；`tools/call` 只放行名单里的工具；名单外的工具与其他方法由 orynd 直接回 JSON-RPC 错误，上游收不到请求。逐条规则见 [`protocol.md`「M26」](protocol.md#m26全局助手的后台研究)。
- 持助手凭据的请求与 M19 相同，不受这份名单约束。

「研究可用工具」对话框里的工具列表由 orynd 以 Streamable HTTP 连上服务器取得（`GET /api/v1/assistant/mcp-servers/{name}/tools`，20 秒超时）；SSE 服务器列不出来，改为手填工具名。「自报只读」取自服务器的 `annotations.readOnlyHint`，是服务器自己的说法，不是保证，所以对话框不预先勾选任何工具。只勾不会对外发送数据、不会改动任何东西的读取工具：邮件的「发送」、日历的「创建」一旦勾上，网页或邮件里的注入就能借研究员调用它们。

**受限的一段**：公网研究与 MCP 检查的发现进主线时，主线 agent 先以 `main_restricted` 变体重启：内置工具只有 `read`，没有 `PI_PROXY` 与 MCP 凭据，宿主工具只剩只读的一组与 `stay_silent`（没有 `oryn`、`computer_*`、`update_main_notes` 与写类工具）。orynd 只在 orbd 报回这个变体之后才投递，这一轮结束后主线下一次启动恢复完整工具表。主人恰好在这一轮里发来的消息由受限的 agent 回答，下一条消息起恢复。内部关联的发现与「交给助手」的运行结果照常交给完整工具表的主线。

**花费**：研究员对话的模型用量照常记进统计。当天（本地时区）合计达到每日上限（缺省 20 美元，可设 0.1–1000）后，当天不再发起运行，并发一条 `budget` 类系统通知，每天至多一条。常驻占用不变：研究运行期间，每个研究员各多一个协调 omp。

**残余风险**：

- 私有研究员与受限的主线不联网，靠的是它们的 omp 没有 `PI_PROXY` 和相应的内置工具，与 Space 协调者相同，而不是出站代理按调用方拒绝。
- Oryn Core 的 `read` 跟随重定向时不检查目标主机：公网页面可以把公网研究员的 `read` 重定向到 `http://127.0.0.1:<egress_port>/…`。那里它只能发 GET，`/mcp/` 要它没有的凭据，它手里也没有私有数据，所以不另做处理。
- `/mcp/` 的过滤假定沙箱里没有哪个研究员能自己构造任意 HTTP 请求：公网研究员只有 `web_search` 与只读网址的 `read`，私有研究员没有内置工具。以后给研究员加任何能发任意请求的工具之前，必须先重看这层过滤。
- 研究凭据同样以环境变量出现在私有研究员的进程里，同宿主的 agent 能从 `/proc` 读到（见下文「残余风险」）；它能做的事是助手凭据的子集。

### 助手的电脑（M19）

助手的电脑是一台普通 orb，不在协调宿主里：provider 与类型取「设置 → 全局助手」里的选择，留空按默认解析；全库至多一台（库里 `orbs.computer = 1`），不进预热池、不参与项目快照，线程不能挂上去（`orb_is_computer`）。它的网络、出站策略与密钥下发和无项目的普通 orb 相同，第 7 节「网络隔离」同样适用。orbd 在里面惰性拉起一个 `omp --mode rpc --no-session` 当工具执行器，不调模型（只有 `find` 会用 judge 模型角色）。执行器连续 30 分钟没有调用就退出，下次调用再拉起；orb 本身照常空闲冻结，助手调用前由 orynd 唤醒，等待就绪最多 90 秒。`/assistant/computer` 页面的「重置」删除这台 orb，里面的文件随之丢失，下次用到时重建。

`computer_*` 调用不写审计（同 artifact 工具：操作只落在助手自己的电脑上）。但电脑里能执行任意命令、能联网，助手读到的不可信内容可以经它产生外部副作用；不希望这样时，在网络隔离上收紧这台 orb，或不要让助手用电脑处理不可信内容。

### 残余风险

- 联网（CONNECT 与代理形式的转发）按协调宿主（一台协调 orb）只有一份策略，不按 agent 区分：Space 协调者与全局助手的 omp 连的是同一个代理端口。Space 协调者不联网，靠的是它的 omp 没有 `PI_PROXY`、内置工具里没有 `web_search` 与生图，而不是代理按调用方拒绝。MCP 不同：出站代理对 `/mcp/` 的每个请求核对只有全局助手持有的凭据（见上文「MCP 路由凭据」）。
- 协调宿主里的各个 agent（全局助手与 Space 协调者）运行在同一个沙箱里：同一个 uid、共享 HOME，`/proc` 可读。同宿主的 agent 能读到别的 agent 进程的环境变量（含 `ORYN_MCP_TOKEN`）和 HOME 里的文件，这是协调宿主既有隔离边界的限制：沙箱隔开的是协调宿主与宿主机其它部分，不是同一宿主里的各个协调 agent。
- 全局助手会读网页、搜索结果、MCP 返回与工作线程的输出。这些内容都以不可信数据交给它，但注入的指令仍可能诱导它做事：给工作线程或 Runner 线程发消息、读写线程文件、在自己的电脑上执行命令、经联网把读到的内容发出去。删除与对外公开要主人点确认卡；新建、修改、归档、发消息等写类宿主工具直接执行并写审计，可在自动化操作记录里查看；联网、MCP、`computer_*` 以及 artifact 的创建与修改不写审计，只在对话转录里留下工具卡。
- 没有网络命名空间时，沙箱内进程能向**任意地址**的 `egress_port` 端口发起 TCP 连接（Landlock 只按端口管）。
- `/proc` 只读可见，沙箱外其它进程的命令行可读（环境与内存读不到；同一沙箱里的协调 agent 之间见本节第二条）。
- 不防资源耗尽（内存、磁盘、CPU）。
- root 部署、没有 PID 命名空间且 ABI < 6 时，沙箱能给同为降权账号的其它协调宿主进程发信号（同一所有者，不出信任域）。
- 没有网络命名空间时，放行的 stream `socketpair` 的一端可以 `bind` 到宿主上一个尚未被占用的 abstract unix 名字（seccomp 看不到地址）：只能抢占名字，不能借它连接或收发任何东西（这对 socket 已互相连接，不能 `listen`）。
- 「home 不可执行」挡不住执行 home 里的代码：经 ELF 解释器（`ld.so <home 里的文件>`）或 `memfd_create` + `execveat` 都能运行（已在内核 7.x、Landlock ABI 11 上实测）。隔离不依赖这一条：能执行的代码仍在同一个沙箱里，受同样的 Landlock、seccomp、命名空间与降权约束。
- 没有 PID 命名空间时，沙箱内用 `setsid` 脱离进程组的进程不随子进程结束：自动的专用账号在子进程退出后按 uid 清扫（独占检查保证这个 uid 的进程都属于本实例）；**显式指定的账号不清扫**，残留进程会一直留着，下次拉起时独占检查发现它们、协调功能不可用，需要手工结束。非 root 部署（沙箱与 orynd 同一用户）同样不清扫。
- TCP Fast Open（`sendto(MSG_FASTOPEN)`、`TCP_FASTOPEN_CONNECT`）在实测内核上与普通 `connect` 一样被 Landlock 拒绝，自检会逐项验证；没有另加 seccomp 规则。

### 排查

```bash
sudo -u oryn orynd --config /etc/oryn/orynd.yaml coordinator check   # root 部署去掉 sudo -u oryn
```

它先打印探测结论（能否建网络命名空间、Landlock ABI）与可用性（root 部署时带降权账号的名字与 uid），再用与正式运行相同的参数在一次性的 HOME 里起一个沙箱子进程，逐项打印一行 JSON：**必须失败**的（读 `master.key`、`oryn.db`、配置文件与 `/etc/shadow`，列 `/`，在 home 之外建文件，执行 `/bin/sh` 或 home 里的程序，连 `1.1.1.1:443`、`127.0.0.1:1` 与自检在宿主回环上真实监听的白名单外 TCP 端口、并对它做两种 TCP Fast Open（该端口也必须接不到连接、收不到数据），在 `0.0.0.0` 与 `[::]` 上 bind 代理端口，建 UDP / unix stream / unix datagram / netlink / MPTCP 套接字与 datagram / seqpacket 的 unix `socketpair`，以 stream 与 datagram 连宿主上的 abstract unix socket，对白名单之外的 0666 unix datagram socket 做 connect 与 sendto、并用放行的 stream `socketpair` 向它 sendto（宿主上这些 socket 也必须什么都收不到），按路径 `truncate` home 里的文件，`unshare`、`mount`、`ptrace`、`clone3`，对父进程（PID 命名空间里是 1 号）做 `prlimit64`、`setpriority`、`sched_setaffinity`，父进程可见时向它发信号）与**必须成功**的（home 内写文件并跨目录改名、home 内 flock 与 OFD 锁——另一个打开文件描述取不到 OFD 写锁且 `F_OFD_GETLK` 看得到持有者，即 omp 旁路文件锁所需的语义——建一对 stream unix `socketpair`、`omp --version`、自连施加沙箱之前就监听好的代理端口），以及 `/proc/self/status` 的 `NoNewPrivs: 1`、`Seccomp: 2`、能力集为空。全部通过时退出码 0。运行中的协调宿主的输出在 `<dir>/host.log`；当前生效的隔离见 `GET /api/v1/coordinator` 的 `sandbox`。root 部署时 `coordinator check` 与 orynd 一样会在需要时创建专用账号。

协调 orb 不参与空闲冻结与停机：全局助手要随时能立即回复。orynd 启动后（先处理掉上一个 orynd 留下的协调 orb），以及之后每 30 秒，会确认协调宿主子进程在运行、全局助手主线的 agent 已起好，并备好一个**备用助手对话**：agent 已起、对用户不可见（`threads.hidden = 1`，不进线程列表与推送）。新建助手对话时直接认领它（按请求下发模型与思考档位，再发首条消息），随即在后台补一个新的；没有就绪的备用对话时照常新建并起 agent。备用对话的系统提示词（含助手记忆摘要）在起 agent 时定下，放满 1 小时换新的。因此常驻占用是协调宿主子进程加两个协调 omp（每个上百 MB）；其它助手对话的 agent 仍按需启动，打开对话页时就会起。会话文件在 `<dir>/homes/<orb id>`，宿主重启后接着同一个会话。子进程意外退出时，若协调 orb 已就绪，orynd 会把它停下再唤醒；若还在启动（尚未就绪），就原地重新拉起子进程。5 分钟内连续 3 次意外退出后不再拉起，常驻检查也暂停：已就绪的停在 `stopped`，等最早那次崩溃滑出 5 分钟窗口后由常驻检查再唤醒（期间发消息照常唤醒）；启动中的由连接超时转为 `failed`，要在页面上点「重试」。

### 升级

同时替换 `orynd` 与 `/usr/local/lib/oryn/omp`，然后重启 orynd。旧的子进程随旧 orynd 退出（socketpair 断开、父进程死亡信号、PID 命名空间三重保证）；新进程拉起前发现源文件变了会重拷 `<dir>/bin/omp`，natives 由 omp 首次运行时解压到 `<dir>/homes/<orb id>/.omp/natives/<版本>` 并清理旧版本。

### 开发

`dev/orynd.yaml` 已带 `coordinator: { omp_path: "build/core/x64/omp", dir: "/var/tmp/oryn-dev-coordinator" }`，没写 `user`：开发机以 root 运行，用的是自动的专用账号 `oryn-coord-<fnv32a("/var/tmp/oryn-dev-coordinator")>`（首次拉起协调宿主或跑 `coordinator check` 时创建）；仓库在 `/root` 下，降权账号进不去，所以目录放在外面。测试用各自的临时协调目录（后缀与 dev 不同），在 root 下真实创建专用账号并在测试结束时 `userdel`。CI 设了 `ORYN_REQUIRE_SANDBOX=1`，沙箱相关的测试在条件不满足时直接失败而不是跳过；若 GitHub runner 不满足条件，去掉这个变量（测试改为跳过），本机验收不受影响。

## 9. 统计分析（M10）

统计页 `/stats`、`oryn stats` 与全局助手的 `get_stats` 都从控制面库里的事实行现算，不需要额外服务。`orynd.yaml` 里可设保留期（只在启动时读取，改动需重启 orynd）：

```yaml
stats:
  retention:
    samples: 720h    # orb 资源采样（orb_samples）；默认 30 天
    events: 8760h    # 已删除 orb 的状态事件与 setup 记录、预算告警账本（账本至少保留 33 天）；默认 1 年（活 orb 的事件永不删）
    usage: 0s        # 用量 / 工具调用 / 回合事实行；默认 0 = 永久
```

每项为 `0`（永久）或至少 `24h`，否则 orynd 拒绝启动。清理在启动时与之后每小时各跑一次。

- **升级后的首次启动**：事实行的版本（`stats_state.facts_version`）与当前代码不一致时，orynd 在后台逐线程按现有条目重算（去掉 fork 造成的重复计费、补上 `model_usage` 的开销）。期间统计页顶部显示「历史数据回填中 n / m」，其它功能照常；重算可中断，下次启动从断点续作。升级前就已删除的线程留下的用量行没有条目可重算，保持原样（在按项目 / Space 的分组里落在空键）。
- **手动重算**：`orynd --config /etc/oryn/orynd.yaml admin rebuild-stats` 全量重算（`--thread <id>` 只重算一个线程）。可在 orynd 运行时执行：每个事务很短，靠 SQLite 的 `busy_timeout` 排队。
- **预算告警**：在统计页或 `oryn stats budget set` 设置日 / 月上限后，orynd 每 60 秒评估一次，越过预警线或上限时发一条不属于任何线程的系统通知（应用内、推送，以及在「设置 → 通知」里订阅了「预算告警」的渠道）；同一周期同一级别只发一次。
- **统计数据的可信边界**：用量来自 orb 里的会话文件，orynd 入库时会钳制明显不合理的数值与字符串，但 orb 里的 agent 仍可以改写自己的会话来少报用量。预算告警是提醒，不是对抗恶意 agent 的计费防线；需要硬上限时，请在模型服务商一侧设置额度。

## 10. 加速：项目快照、预热池与带 Docker 的 orb（M11a）

```yaml
# orynd.yaml（以下为默认值）
snapshots: { enabled: true, max_age: "72h", max_depth: 16, min_setup: "10s", max_total_gb: 40, max_snapshot_gb: 10 }
pool:      { max_total: 2, max_age: "12h", active_window: "72h" }
```

### 项目快照

- **什么时候拍**：项目 orb 第一次 setup 成功、且 setup 用时不少于 `min_setup` 时，orynd 在它进入 ready 之前让 provider 把磁盘存成快照（docker 驱动是 `docker commit`，这期间容器暂停几秒；sysbox 类型要先停机再拍，见下文）。只在 agent 从未在这台 orb 上运行过时才拍，而且是在 setup 脚本跑完、resume 与受管服务启动**之前**拍。所以快照里只有 setup 的产物，没有会话、附件、浏览器 Profile、agent 的改动，也没有服务运行时写下的东西。认领来的预热 orb 已经跑过服务，不发布快照。
- **怎么用**：之后同一项目、同一 provider、同一类型的新 orb 从快照启动。orbd 会 `git fetch` 基线：HEAD 没变就跳过 setup，只跑 resume；HEAD 变了就在快照的环境上把 setup 增量重跑一遍，成功后发布新快照。所以 setup 脚本要能重复执行。如果新的基线改动了仓库里的 `.oryn/setup`、`.agents/setup`、`.oryn/services.yaml` 或 `.amp/services.yaml`（内容、可执行位、符号链接目标任一变化都算），或者旧 HEAD 不是新基线的祖先（force push 等情况），旧快照就直接退役，下一个 orb 从基础镜像全新 setup。浅克隆的项目会多 fetch 一次历史来判断祖先关系；仍判断不了的，按「不是快进」处理。
- **什么时候失效**：项目的仓库、分支、setup 脚本、env 模式密钥与降级模型 key（只看名字与修改时间），或者 provider 的基础镜像，任何一项变化，旧快照就退役，下一个 orb 从基础镜像重来。重建 orb 镜像（`make image`）会让该 provider 上的全部快照失效。同一条血统最多复用 `max_age`（72 小时），最多刷新 `max_depth` 次；orb 是否真的从快照启动，以 provider 的上报为准，orb 内的程序无法让控制面误判、借此续期。
- **磁盘**：快照是 provider 宿主上的镜像，名字是 `oryn-snap-…`。单个快照超过 `max_snapshot_gb` 就直接作废，不会挤掉别的项目（不写时取 10 与 `max_total_gb` 中较小的一个）。`max_total_gb` 约束的是快照缓存：每个 provider 把可用（ready）快照「相对基础镜像多占的字节」加起来，超过上限时按最近使用时间退役其它项目的快照，新发布的那份总是保留。退役的快照如果还有 orb 在用，它的镜像归那台 orb，不计入上限，等 orb 删掉后由回收器删除；所以宿主上快照镜像的实际占用，可能暂时高于 `max_total_gb`，多出的部分正是这些 orb 引用着的镜像。`docker image prune -a` 删掉未被容器引用的快照也没关系，下一个 orb 会退回基础镜像并重新发布。
- **事故处置**：怀疑某次 setup 把不该留的东西（后门、泄露的凭据）拍进了快照时，执行 `oryn project snapshots rm <项目>`。这会把可用的和正在构建的快照一起作废，之后的 orb 从基础镜像全新 setup。快照镜像的配置里不保留 orb 令牌等环境变量。
- **dispatch 驱动**：基础镜像只按引用名识别（`ref:<镜像>`），镜像内容变了但引用名不变时，要靠 72 小时上限兜底；它也不上报快照大小，所以配额对它不起作用。
- **管理**：项目设置页的「项目快照」开关与快照表；CLI `oryn project snapshots ls <项目>`，`oryn project snapshots rm <项目> [--type <类型>]`。

### 预热池

在项目设置里把「预热 orb」设成 1–3，orynd 就会为这个项目保持这么多台已完成 setup、还没绑定线程的 orb，新线程直接认领，跳过 spawn 与 setup。全局最多 `pool.max_total` 台（设成 0 即关闭）。预热 orb 会常驻占用内存（空闲时照常被冻结），每 `max_age`（12 小时）换新一次。以下情况不预热：项目既没有仓库也没有 setup 脚本；`active_window`（72 小时）内没有新线程；provider 离线；上一台预热 orb 的 setup 失败后 1 小时内。请求了浏览器 Profile 的线程不认领预热 orb。

### 带 Docker 的 orb（sysbox）

agent 在 orb 里有免密 sudo，所以**不支持 `--privileged`**（那等于把宿主 root 交给 agent），也不做 rootless DinD（它要求放宽 seccomp、AppArmor 与 `/proc` 掩码，放宽的部分正好落在 agent 手里）。需要在 orb 里跑 Docker 时，用 [sysbox](https://github.com/nestybox/sysbox)：容器里的 root 映射到宿主上的非特权 uid，不放宽宿主侧的任何防线。

1. **前置条件**：先按第 7 节建隔离网桥，并按下面「orb link 的 TLS」开启 TLS。sysbox 类型要保留 `NET_ADMIN` / `NET_RAW`，供内层 dockerd 建网桥、写 iptables。它们只在容器自己的网络命名空间里生效，但足以在共用的网桥上做 ARP 欺骗；`enable_icc=false` 挡不住这一点。所以 orb 与控制面之间的 link 必须走 TLS，否则一台 sysbox orb 可以截获、篡改同网桥上其它 orb 的 link，包括令牌、env 密钥和提示词。
2. **安装 sysbox**：Ubuntu / Debian 用官方 deb 包；Arch 用 AUR 的 `sysbox-ce-bin`。然后 `systemctl enable --now sysbox`。
3. **向 Docker 注册 runtime**，合并进 `/etc/docker/daemon.json`：

   ```json
   {
     "live-restore": true,
     "runtimes": { "sysbox-runc": { "path": "/usr/bin/sysbox-runc", "runtimeArgs": ["--no-kernel-check"] } }
   }
   ```

   这两项都可以用 `systemctl reload docker` 热加载，不需要重启 Docker。建议先单独加 `live-restore` 并 reload 一次，之后任何需要重启 Docker 的改动都不会停掉正在运行的容器。

   **要明确接受的风险**：
   - `--no-kernel-check` 关掉了 sysbox 唯一的内核兼容性检查。sysbox 不认识较新的内核版本号，不带它就无法启动；但 sysbox-fs / sysbox-mgr 以 root 运行，在未经验证的内核上是新增的宿主攻击面。条件允许时，把 sysbox 类型放在单独的 provider 宿主（例如一台虚拟机）上，不与 orynd 同机，并用 sysbox 支持矩阵内的内核。
   - `live-restore` 对宿主上**所有**容器生效：升级 dockerd / containerd / runc 后，正在运行的容器仍沿用旧 shim，安全修复要等容器重建才生效。升级后请重建 orb（停机再恢复即可）。
   - sysbox 包会写入 `/usr/lib/sysctl.d/99-sysbox-sysctl.conf`，在全宿主范围调高 `kernel.pid_max` 等上限。docker 驱动因此给每个 orb 加 `--pids-limit`（`docker.pids_limit`，默认 4096）。
4. **只在创建 sysbox 容器失败时**：Docker ≥ 29.5 在某些宿主上需要在 `daemon.json` 里合并 `"features": {"time-namespaces": false}`，这一项必须重启 Docker。在 Docker 29.9.2 加内核 7.3 上实测不需要。
5. **provider.yaml**：加一个类型。provider 启动时会预检：配置的 runtime 不在 `docker info` 里就拒绝启动。

   ```yaml
   docker:
     types:
       dind: { cpus: 4, memory: "8g", runtime: sysbox-runc, nested_docker: true }
   ```

这种类型的 orb 里，dockerd 由 orbd 托管（日志在 `~/.cache/oryn/logs/dockerd.log`），setup 会等它能接受请求后再开始。setup 里拉取的镜像会随项目快照保存（拍快照前，orbd 会停掉内层正在运行的容器）。普通类型的 orb 里也有 `docker` 命令，但连不上守护进程。

**sysbox orb 的快照会让它重启一次。** sysbox 自己挂着容器的 overlay，Docker 在容器运行或暂停时 commit 不到上层内容（`docker info` 里 `Native Overlay Diff: false` 的宿主都是这样），所以 provider 对 sysbox 容器拍快照时，先停机，再 commit，然后重新启动。项目快照发生在 setup 之后、进入 ready 之前，用户感知不到；对 sysbox orb 做「连环境 fork」时，源 orb 会重启一次，里面运行中的进程和内层容器会被中断。

### orb link 的 TLS

orb 与控制面之间的 link 默认是明文 HTTP，跑在宿主网桥上。开启 TLS 后，orynd 会在数据目录 `orb-tls/` 下生成一张自签 CA 和服务端证书，并另开一个 TLS 监听。新建的 orb 在 spawn 时随环境变量 `ORYN_ORB_CA_PEM` 拿到这张 CA，orbd 只信任它，不信任系统根证书。

```yaml
# orynd.yaml
orb_tls:
  listen: ["172.17.0.1:7643"]   # docker0（或专用网桥）的网关地址；证书的 SAN 会自动包含这些 IP
  hosts: []                      # 额外写进证书的主机名（可选）
# provider.yaml
orb_url: "https://172.17.0.1:7643"
```

- 开启后，**已存在的 orb 仍走原来的明文监听**：orbd 的 `ORYN_URL` 和 CA 是在创建容器时写进环境变量的，停机再恢复、冻结再唤醒都只换令牌，不会改成 TLS。只有删掉重建的 orb，才会改走 TLS（线程会在新 orb 里继续，转录不丢）。要关掉面向网桥的明文监听，先把开启 TLS 之前创建的 orb 都删掉重建（`docker inspect <orb> --format '{{range .Config.Env}}{{println .}}{{end}}' | grep ORYN_URL` 是 `http://` 的就是），否则它们会失去连接，直到被删除重建。
- provider 在另一台主机、经反代用正式证书连 orynd 时，不需要开启它：`orb_url` 写 `https://` 的主域名，orbd 用系统根证书校验。
- 服务端证书有效期 1 年，到期前 30 天或 `listen` / `hosts` 变化时，orynd 启动时自动重签；CA 不变，已下发 CA 的 orb 不受影响。删除 `orb-tls/` 目录会生成新的 CA，之后要重建所有 orb。

## 11. Runner：在自己的机器上跑线程（M11b）

runner 是你自己的机器（笔记本、工作站、NAS，支持 Linux 与 macOS；Windows 机器经 WSL2 运行 Linux 版）。在那台机器上以普通用户运行 `orbd run --runner`，它自动下载并校验钉住版本的 Oryn Core，主动连到 orynd；之后新建线程时可以选「这台 runner 的某个目录」，线程里的 agent 就直接在那个目录工作。模型、omp 配置、记忆与 SSH 主机由控制面提供（与那台机器上你自己的 `omp`、`~/.omp`、`~/.agents`、`~/.ssh` 完全独立），git 与其它命令行工具仍用那台机器自己的环境与凭据。

### 安全边界（先读）

- **runner 完全信任控制面。** 能操作 orynd 的人（网页会话、设备令牌、或攻破了 orynd 的人），都能以 runner 进程的系统用户在那台机器上执行任意命令。agent 本身不受目录限制，`--dir` 约束的只是面板能看到什么、线程能选什么。所以：只在你信任 orynd 的机器上运行 runner；用一个权限有限的普通用户运行；`orbd` 以 root 运行时会直接拒绝（`--allow-root` 才放行）；`ORYN_URL` 是非回环的 `http://` 时会告警，请走 HTTPS。
- **反方向不信任：控制面的密钥不去 runner。**
  - **无密钥配置与投递校验**：orynd 向 runner 下发全局 omp 配置（`omphome.apply`）与按线程所属项目交付的项目层配置（随 `agent.start`）。`models.yml` 里的模型 key 一律换成 `oryn-ph-…` 占位符、地址改写到本机常驻出站通道；`env` 模式的密钥不下发，无 `baseUrl`（需以 `ORYN_MODEL_KEY_<NAME>` 环境变量降级下发真实 key）的模型 provider 对 runner 关闭降级、在 runner 上不可用。下发前，orynd 对全局层与项目层的每个文本文件做无密钥校验（`agentcfg.ScreenSecrets`）：扫描形似密钥的串（占位符除外），并比对长度 ≥ 8 的已知密钥明文（密钥库全部值与密码登录 SSH 主机的密码）；命中的文件扣下不发，并在该 runner orb 的事件里写备注说明未下发的文件及原因。
  - **出站只放行模型中转与记忆**：runner 在 `127.0.0.1:7811`（`ORYN_EGRESS_ADDR` 可改）常驻开一条本地出站通道，orynd 的 `runnerEgressHandler` 只放行 origin-form 的 `/llm/`（模型中转）与 `/memory/`（记忆网关，第 14 节），由 orynd 在控制面注入真实的模型 key 与 Hindsight key（响应回显同步脱敏）；`/git/`、`/gh/`、`/mcp/`、CONNECT 与代理形式请求一律 403。因此控制了这台机器的进程可以经中转调用中央模型（消耗额度）或访问该 runner 线程所属项目的记忆，但拿不到任何密钥明文。
  - **GitHub 与 git 凭据不下发**：orynd 不往 runner 下发 GitHub OAuth 凭据、git 主机令牌、git 代理改写或提交身份，也不改那台机器的 git 配置；runner 上的 git 操作使用本机自己的凭据。
  - **link 与本地控制口白名单**：runner 的 link 只受理转录同步（`entries.cursors` / `entries.append`）、artifact 工具调用（`artifact.call`，runner 线程只看得到自己建过或写过的 artifact）、文件库存入（`library.put`，只收调用方线程工作目录内的普通文件）和状态类通知，`thread.*`、`schedule.*`、`github.*`、`ssh.call` 等其余请求一律回 `runner_unsupported`，所以 runner 也没法借别的线程把密钥绕回来；hello 里的主机名、目录、特性等字段有长度和数量上限，超出截断。本机 `~/.oryn/orbd/runner.sock`（0600）只认 `artifact.call` 与 `library.put`。
  - **SSH 每线程密钥与局限**：Oryn 登记的 SSH 主机私钥和密码留在 orynd，runner 上的 `orbd` 在 `127.0.0.1:7822` 监听并把连接转给控制面网关。因为一台 runner 可同时跑多个项目的线程，`orbd` 为每个线程的每一代监督器生成一把临时 ed25519 密钥（私钥写在 `~/.oryn/threads/<线程>/ssh/` 下，公钥随 `agent.start` 报给 orynd），网关对 runner 的流要求公钥认证，按认出的线程及其所属项目放行主机并记审计。**局限**：同一系统用户下的任何本机进程都能读到其它线程的私钥文件——这把密钥防的是某个线程的 agent 误用别的项目的主机，不防恶意本机进程。
- **谁能操作 runner 线程。** 除了你自己，只有全局助手（M19）：它可以像你一样在 runner 上新建线程（指定目录、worktree 或共用目录），给 runner 线程发消息、读取、归档、中止、改模型、fork（只能共用环境）、设线程内定时，读写线程目录里的文件、查看改动；删除要你点确认卡。Space 协调者的 send_message、read_thread、归档、定时等工具对 runner 线程仍一律回 `runner_unsupported`，orb 内 agent 只能联系自己的父子线程：普通 orb 里产出的内容不能经它们转成你机器上的指令。全局助手会读网页与工作线程的输出，注入的指令有可能经它到达 runner 线程（见第 8 节「残余风险」）。
- **面板按声明收窄**：Files / Changes 只能看到线程工作目录里面的内容；线程的目录必须落在 `--dir` 声明的目录里（或 Oryn 在它旁边建的 worktree）；终端只有带 `--terminal` 才开。runner 上没有 Portal、受管服务、Desktop、`oryn sync`、项目快照，不能冻结、删除或「连环境 fork」，也不能绑浏览器 Profile。
- runner 令牌（`ort_…`）库里只存哈希。在「设置 → Runner」里删除 runner 即吊销，已连着的 link 会被断开。

### 建立与启动

支持 **Linux**（`x64`、`arm64`）与 **macOS**（Apple 芯片，`arm64`）；不支持 Intel mac（Core 不发布 `darwin-x64` 构建，`orbd` 启动时报「此平台没有 Oryn Core 构建」），不支持原生 Windows，Windows 机器请在 **WSL2** 里运行 Linux 版 `orbd`。

1. **建 runner**：「设置 → Runner → 新建」，或 `oryn runner create laptop`（标准输出只有令牌，启动命令打到标准错误）。令牌只显示这一次。
2. **在那台机器上**放好与 orynd 同版本的 `orbd`（同一 Release 的 `oryn-server_<版本>_linux-{x64,arm64}.tar.gz` 或 macOS 用的 `orbd_<版本>_darwin-arm64.tar.gz`，或 `make build` 产出的 `bin/orbd`），确保系统 `PATH` 目录（`$HOME` 之外，如 `/usr/bin`、`/opt/homebrew/bin`）里有 `git`（不要求本机安装 `omp`；在 bash 里经网关连 SSH 主机还需 OpenSSH ≥ 8.4）。然后：

   ```bash
   install -D -m 600 /dev/stdin ~/.config/oryn/runner.token <<< 'ort_…'   # 或用环境变量 ORYN_RUNNER_TOKEN
   ORYN_URL=https://oryn.example.com orbd run --runner --dir ~/code/app --dir ~/notes [--terminal]
   ```

   - **下载 Core**：首次启动（或升级 `orbd` 换了钉定版本时）按内嵌的 `core/source.json` 从 GitHub Release 下载对应平台的 Oryn Core，校验 SHA-256 后缓存在 `~/.oryn/core/<tag>/omp`（旧版本目录自动清理）。无法直连 GitHub 时设 `ORYN_CORE_BASE_URL=<镜像根>`（请求 `<ORYN_CORE_BASE_URL>/<tag>/omp-<os>-<arch>`）；下载或校验失败即启动失败，不会回退到本机 `omp`。（`ORYN_AGENT_BIN` 仅供开发与测试。）
   - **本地端口**：出站通道常驻监听 `127.0.0.1:7811`，端口被占用时启动失败，可设 `ORYN_EGRESS_ADDR=127.0.0.1:<端口>`（只允许 `127.0.0.0/8` 或 `[::1]` 回环地址，下发的 `models.yml` 自动改写到该地址）。SSH 网关默认监听 `127.0.0.1:7822`（`ORYN_SSH_ADDR` 可改；监听失败不影响启动，只是该 runner 上 SSH 网关不可用）。
   - **目录与单实例**：`--dir` 可以重复，符号链接会先解析成真实路径；同一个系统用户只能跑一个 runner 实例。
   - **本机写入清单**：`orbd` 只在 `~/.oryn/` 下维护自己的文件，不会写进项目目录：
     - `~/.oryn/core/<tag>/omp`：下载并校验后的 Oryn Core；
     - `~/.oryn/omp/`：独立的 omp 配置根（agent 以 `PI_CONFIG_DIR=.oryn/omp`、`ORYN_ISOLATE_USER_SOURCES=1` 运行），含下发配置与 runner 版 `oryn-library` 技能所在的 `~/.oryn/omp/agent/`、Core 隔离模式用的 `~/.oryn/omp/ssh/{config,known_hosts}`（下发清理清单在 `~/.oryn/omp-home.manifest.json`）；
     - `~/.oryn/project-config/<项目 ID>-<sha12>/.omp/`：按线程所属项目、按内容寻址缓存的项目层配置（无运行中线程引用的旧版本自动清理）；
     - `~/.oryn/threads/<线程>/`：线程会话文件，以及开启 SSH 网关时每代监督器的临时私钥 `~/.oryn/threads/<线程>/ssh/<指纹>/id_ed25519`（监督器结束即删除）；
     - `~/.oryn/ssh/config`：供 bash 里 `ssh -F ~/.oryn/ssh/config <主机名>` 使用的网关配置；
     - `~/.oryn/orbd/`：实例锁 `runner.lock`、登记表 `threads.json` / `worktrees.json`、控制口 `runner.sock` 与自带扩展 `extensions/oryn-orb.ts`（另有 `~/.oryn/attachments/`、`~/.oryn/tmp/` 与启用记忆时的 `~/.oryn/memory-overlay.yml`）。

     **不读写**你自己的 `~/.omp`、`~/.agents`、`~/.claude`（及其它外部工具的用户级目录）、`~/.ssh` 与 git 配置；仓库内的项目级规则与技能（`AGENTS.md`、`.omp/`、`.claude/skills` 等）照常生效。
3. **常驻**（systemd user unit 示例）：

   ```ini
   # ~/.config/systemd/user/oryn-runner.service
   [Unit]
   Description=Oryn runner
   After=network-online.target
   [Service]
   Environment=ORYN_URL=https://oryn.example.com
   ExecStart=%h/.local/bin/orbd run --runner --dir %h/code/app --terminal
   Restart=always
   RestartSec=5
   [Install]
   WantedBy=default.target
   ```

   `systemctl --user enable --now oryn-runner`；要在未登录时也运行，执行一次 `loginctl enable-linger <用户>`。

### 使用

- **新建线程**：在执行位置里选 runner 和目录（可以再填子路径）。同一个目录已有未归档的线程在用时，会提示「新建 worktree（推荐）」或「仍然共用」。新 worktree 建在仓库旁边，目录是 `<仓库目录>-<名字>`（重名时加 `-2`、`-3`），分支同名。命令行：`oryn thread new --runner laptop --dir ~/code/app [--worktree fix-login | --share-dir] "…"`。
- **归档**：线程有 Oryn 建的 worktree 时，归档对话框可以「同时移除 worktree」。它执行 `git worktree remove` 和 `git branch -d`，不带 `--force` / `-D`：有未提交的改动时 git 会拒绝，分支没合并时会保留，报错原样显示，线程照常归档。
- **Changes 面板**只在线程目录就是仓库根目录时显示改动；线程目录是仓库的子目录时显示「不是 git 仓库」。
- **离线**：link 断开超过 30 秒，runner 显示为离线。期间发给它的消息排队（每个线程最多 100 条），runner 重新连上后按顺序送达。
- **中央配置与下发**：runner 线程与 orb 一样使用中央配置（模型、角色、技能、规则、子 agent、提示模板、MCP）与线程所属项目的项目层配置。全局配置改动后即时经 `omphome.apply` 下发（按变更项自动 reload 或标为需重启）；项目层随 `agent.start` 按线程交付，项目层或项目 SSH 主机改动后运行中的该线程会标为「需要重启」（runner 上的 `/omfg` 不回写中央配置）。
- **连接 SSH 主机**：Oryn 登记的全局主机与线程所属项目的主机可直接在 `ssh://<名>/<路径>` 与 `terminal` 工具中按名使用；在 bash 里执行 ssh 时用 `ssh -F ~/.oryn/ssh/config <名>`（裸 `ssh <名>` 仍走本机自己的 `~/.ssh/config`）。在 agent 进程之外（例如你本机自己的终端）运行该命令会因缺少线程环境变量 `ORYN_SSH_KEY` 而失败。
- **存入文件库**：runner 上没有 `oryn` CLI，要把线程工作目录内的普通文件（≤ 100 MiB）放进全局助手文件库时，让 agent 调用 `oryn_library_put` 工具（自带 `oryn-library` 技能）。
- **从旧版 runner 升级**：把本机 `orbd` 换成与 `orynd` 同版本并重启即可。启动后自动下载 Oryn Core 并接收控制面配置；本机以前安装的 `omp` 与你自己的 `~/.omp` 配置不再被 runner 使用（启用记忆时 `~/.oryn/memory-overlay.yml` 仍由 `orbd` 自动维护）。

## 12. Firecracker provider（M11b）

每个 orb 是一台 Firecracker microVM：独立内核、独立的根文件系统写层、每台 VM 一条 /30 点对点 tap。隔离强于容器，代价是 provider 必须以 root 运行，宿主要有 KVM。

### 前提

- x86_64 宿主，`/dev/kvm` 与 `/dev/net/tun` 可用（虚拟机里跑需要开嵌套虚拟化），cgroup v2，`ip`、`nft`、`mke2fs`、`cp` 在 PATH 里。
- Firecracker 官方支持的宿主内核是 5.10 / 6.1 / 6.18，其它版本预检只给警告。
- 状态目录与产物必须在同一个文件系统上（基础盘用硬链接）。

### 产物

```bash
make image                 # 根盘取自 orb 镜像（FC_IMAGE，默认 oryn-orb:dev）
make fc-artifacts          # build/fc/{firecracker,jailer,vmlinux,rootfs.ext4}；不需要 root，内核构建约 2 分钟
sudo install -D -m 755 build/fc/firecracker build/fc/jailer -t /usr/local/bin/
sudo install -D -m 644 build/fc/vmlinux build/fc/rootfs.ext4 -t /var/lib/oryn-fc/artifacts/
```

Firecracker 与内核源码的版本和 sha256、构建用的容器镜像 digest（内核构建的 Arch 镜像、根盘打包的 `debian:13-slim@sha256:…`）都锁定在 `images/fc/artifacts.lock.json`，构建脚本拒绝没带 digest 的镜像引用。**更新根盘要用 `install` 换成新文件，不能原地覆盖**：运行中的 VM 的基础盘是它的硬链接，原地写会改坏这些 VM。换了根盘或内核后，provider 上报的基础镜像身份随之变化，旧的项目快照全部失效。

### 网络

```bash
sudo install -D -m 755 deploy/firecracker/oryn-fc-net.sh /usr/local/lib/oryn/oryn-fc-net.sh
sudo install -m 644 deploy/systemd/oryn-fc-net.service /etc/systemd/system/
sudo systemctl enable --now oryn-fc-net
```

它建一个 dummy 接口 `oryn-fc`（地址 `172.31.255.1/32`，orynd 在这个地址上给 guest 提供 link），打开 `ip_forward`，并装两张 nft 表：

- guest 发往宿主的流量，只放行到 `172.31.255.1` 的 `ORYN_FC_LINK_PORTS` 端口（默认 `7643`）；
- guest 之间、guest 到私有网段 / 链路本地 / 环回一律丢弃，出网走 masquerade；
- 反伪造不靠 sysctl：从 `fc-*` 进来、源地址不在网段内或反向路径查不到本接口的包直接丢弃（宿主 `rp_filter` 设成宽松也照样生效）；`fc-*` 的 IPv6 进出一律丢弃（宿主开了 IPv6 转发也不会把 guest 送进内网）。`oryn-fc-net.sh rules | nft -c -f -` 只打印并校验规则，不改宿主。
- 有 Docker 时在 `DOCKER-USER` 里追加两条放行，否则 Docker 的 FORWARD 默认丢弃会拦掉 guest 出网。

每台 VM 的 tap 由 provider 建（`/30`、严格反向路径过滤），VM 之间没有共享二层。`oryn-fc-net.service` 要在 orynd 之前启动，因为 orynd 要监听 `172.31.255.1`。

### 配置与启动

```yaml
# orynd.yaml：给 guest 开 TLS 的 link 监听
orb_tls: { listen: ["172.17.0.1:7643", "172.31.255.1:7643"] }

# /etc/oryn/provider-fc.yaml
url: "https://oryn.example.com"
token_file: "/etc/oryn/provider-fc.token"
orb_url: "https://172.31.255.1:7643"
driver: firecracker
firecracker:
  disk_gb: 40                                  # 每台 VM 的可写盘（稀疏文件，按实际写入占空间）
  # uid_base: 2000000000                       # 槽位 n 的 jailer uid/gid = uid_base + n；须避开 subuid/subgid 与已有用户
  limits: { max_vms: 8, max_memory_mb: 32768 } # 超出时 spawn 失败，原因 capacity
  types: { small: {cpus: 2, memory: "4g"}, medium: {cpus: 4, memory: "8g"}, large: {cpus: 8, memory: "16g"} }
  default_type: medium
```

没开 `orb_tls` 的部署：`orb_url` 写 `http://172.31.255.1:7600`，`listen` 加 `172.31.255.1:7600`，并把 `oryn-fc-net.service` 的 `ORYN_FC_LINK_PORTS` 改成 `7600`。

```bash
sudo oryn provider preflight --config /etc/oryn/provider-fc.yaml   # 逐项打印检查结果，全过再启动
sudo install -m 644 deploy/systemd/oryn-provider-fc.service /etc/systemd/system/
sudo systemctl enable --now oryn-provider-fc
```

预检会核对：root、KVM、tun、cgroup v2、产物与状态目录同盘、`oryn-fc` 接口与 nft 表存在、`orb_url` 的端口在放行列表里、网段不与宿主其它接口重叠、槽位 uid/gid 区间 `[uid_base, uid_base+16320)` 不与 `/etc/subuid`、`/etc/subgid` 的任何一段及已有用户 / 组重叠（重叠时列出冲突并建议一个空闲的 `uid_base`；持有重叠 subuid 的本机用户能在自己的 user namespace 里变成槽位 uid，向 jail 里的 firecracker 发信号）。

### 行为差异

- **冻结**只停 vCPU，内存仍留在宿主进程里，和 `docker pause` 一样占内存；`limits.max_memory_mb` 把冻结的 VM 也算在内。
- **停机**向 guest 发关机请求，30 秒内不退出就强杀；磁盘和槽位保留，下次唤醒重新开机。
- **项目快照**是可写盘的稀疏复制（文件系统支持时用 reflink），运行中的 VM 拍快照时先在 guest 里冻结文件系统、暂停 VM、复制、再恢复，前后不到几秒。
- guest 里的 orbd 作为 PID 1 启动，时间、主机名、DNS 和令牌经 vsock 下发；模型与 git 的密钥仍然不进 VM。令牌只放在 guest 的 tmpfs `/run/oryn/orb-token`（`$HOME/.oryn/orb-token` 是指向它的链接），可写盘和由它复制出的快照里没有令牌；发布快照或 fork 之后，控制面还会给源 orb 换一次令牌。
- **串口日志**写在每台 VM jail 里的 4 MiB tmpfs 上（`jail/firecracker/<orb>/root/console/console.log`），写满后 Firecracker 丢弃后续字节、guest 照常运行，所以 provider 停机期间 guest 也写不满宿主磁盘；provider 在跑时每 30 秒把超过 2 MiB 的日志裁到最后 512 KiB，失败任务里带的串口尾部取自这里。
- **拍快照中途出错**：provider 在冻结 guest 文件系统前留一个 `thaw-pending` 标记，VM 恢复（`Resumed` + 解冻）全部成功才删掉；恢复失败时快照任务失败、不发布。下一次对这台 VM 的 spawn / 冻结 / 快照会先把它恢复好。provider 在 VM 已开机、guest 还没收到启动参数时崩溃，重启后的 spawn 会补发完整的启动请求，不会留下一台没有 orbd 的 VM。

## 13. GitHub 集成（M15）

Oryn 通过你自己注册的 **GitHub OAuth App** 连接 GitHub，用它连接两个 GitHub 用户：

- **我的账号**：交互式线程、首屏新建线程、项目表单里的仓库列表、仓库绑定、仓库 webhook 的管理、仓库级轮询与发布哨兵的读请求都用它。
- **Agent 账号**（可选，但全自动线程默认用它）：专门给 agent 用的第二个真实 GitHub 账号。两个槽位不能是同一个账号。

OAuth App 的 Client Secret、两个账号的令牌、每个仓库 webhook 的 secret 都用控制面主密钥加密后存在数据库里（AAD 分别为 `github/oauth/client-secret`、`github/identity/<slot>/access`、`github/hook/<repo_id>/secret`），不出现在「密钥」设置页，也不进 orb。

```yaml
# orynd.yaml（以下为默认值；只有测试与验收脚本会改）
github: { api_url: "https://api.github.com", web_url: "https://github.com" }
```

### 注册 OAuth App

1. 打开设置页「GitHub」→「OAuth App」卡片，点「在 GitHub 上新建 OAuth App」（`<web_url>/settings/applications/new`）。Homepage URL 随意填写。
2. **Authorization callback URL** 填卡片上显示的回调地址 `<public_url>/api/v1/public/github/oauth/callback`（旁边有「复制回调地址」）。没保存过配置时，这个地址也会显示。
3. 在 GitHub 上生成 Client secret，把 **Client ID** 和 **Client Secret** 填回卡片并保存（`PUT /api/v1/github/oauth-app`）。之后再改 Client ID 或 webhook 基址时，Secret 留空表示沿用已保存的值。
4. **Webhook 基址**默认等于 `public_url`，仓库 webhook 的投递地址是 `<webhook 基址>/api/v1/public/github/webhook`。改动基址后，Oryn 会立刻重写所有仓库 webhook 的 `config.url`，失败只记日志，之后每 10 分钟的同步还会再重写一遍。
5. 在「账号」卡片里分别「连接」我的账号与 Agent 账号。授权链接 10 分钟内有效，带 `prompt=select_account`；可以「复制链接」到已登录目标账号的另一个浏览器里打开。回调完成后 302 回到 `<public_url>/settings/github?connected=<slot>`，失败时是 `?error=<code>`（`state_invalid`、`exchange_failed`、`same_account`、`access_denied`、`github_not_configured`）。
6. 「删除配置」先删除 Oryn 在各仓库建的 webhook，再断开两个账号，最后删掉配置行。GitHub 上的 OAuth App 本身不会被删除。单独断开「我的账号」时，同样先删除全部仓库 webhook，因为这些 webhook 由我的账号管理。

没有 OAuth App 配置时，连接账号返回 409 `github_not_configured`。

### Scope

授权请求的 `scope` 恰好是 `repo workflow read:org admin:repo_hook gist write:packages user:email`（`internal/github` 的 `oauthScopes`）。实际授予的 scope 取自 `GET /user` 的 `X-OAuth-Scopes`，「账号」卡片会列出来。缺项时卡片显示「缺少权限」和缺少的 scope，这时需要重新连接。判断是否缺项时，`write:org`、`admin:org` 视为包含 `read:org`，`user` 视为包含 `user:email`。

|scope|用途|
|---|---|
|`repo`|公开与私有仓库的读写：git 克隆与推送、issue、PR、评审、评论、标签、Release、合并，以及控制面的仓库绑定、权限查询、CI 汇总与轮询|
|`workflow`|推送改动了 `.github/workflows/` 的提交。没有它，GitHub 会拒绝这类推送，发布哨兵修 CI 时尤其需要|
|`read:org`|读组织与团队的成员关系，例如 `gh` 按团队请求评审、列出组织仓库|
|`admin:repo_hook`|创建、校正、删除仓库 webhook，读取投递日志。实测只有 `repo` 也能做到，单列出来是为了不依赖这一隐含行为|
|`gist`|`gh gist` 经 API 创建和编辑 gist。经 git 克隆 gist（`gist.github.com`）不在本期范围|
|`write:packages`|只用于经 `gh api` 调用的 Packages REST API，见下文「不在本期范围」|
|`user:email`|读取账号邮箱（`GET /user/emails`）|

控制面绑定仓库、管理 hook、查询写权限、执行门禁现查、轮询投递日志或 REST 轮询时，如果某个槽位的令牌收到 401，就把这个槽位标为「需要重新授权」（`reauth`）。OAuth App 的令牌不过期，也没有刷新令牌，所以重新授权只能重新连接。

### 组织批准与 SAML SSO

新建的组织默认开启「OAuth App 访问限制」。没被组织批准的 OAuth App 读不到该组织的私有资源，对公开资源也不能做 privileged 的创建、更新、删除，私有仓库的 webhook 也收不到投递。开启 SAML SSO 的组织另外要求为令牌做 SSO 授权。

Oryn 按 403 响应区分这两种情况：

|状态|判定|出现的位置|界面给出的操作|
|---|---|---|---|
|`org_restricted`|403，message 含 `OAuth App access restrictions`|项目的未绑定原因（`bindStatus`）、仓库 webhook 状态|「申请组织批准」：`<web_url>/settings/connections/applications/<client_id>`|
|`sso_required`|403，且带 `X-GitHub-SSO` 响应头|同上|「完成 SSO 授权」：响应头 `X-GitHub-SSO: required; url=…` 里的地址，记在该 hook 的 `error` 中|

项目未绑定的第三种原因是 `not_found`，表示仓库不存在，或我的账号读不到它。组织批准或完成 SSO 授权之后，下一轮同步（每 10 分钟，依次重新绑定项目、校正仓库 webhook）会自动恢复。也可以重新连接我的账号，连接完成后会立即重新绑定全部项目。

以下行为未实测：开启访问限制的组织里，非成员在公开仓库发评论、开 PR 是否受影响。

### 仓库绑定与 orb 内凭据

项目的仓库地址是 `https://github.com/<owner>/<repo>`，并且我的账号能读到这个仓库时，项目自动绑定，记录 `github_repo_id`。仓库改名或转移后，Oryn 自动更新仓库地址，已有的 orb 在下次 setup 或唤醒时把 `origin` 改到新地址。我的账号是仓库 admin 时，Oryn 还会在仓库上建一个 webhook（见「事件来源与额度」）。最后一个绑定该仓库的项目解绑、归档或删除后，这个 webhook 也随之删除。

orb 内的凭据**与项目是否绑定无关**。只要线程身份对应的槽位可用，任何工作 orb（有没有项目都一样）都得到以下配置：

- **git**：`insteadOf` 覆盖整个 `github.com` 主机，出站代理注入 `Basic x-access-token:<OAuth 令牌>`。github.com 上不按仓库限制，能做什么由账号本身的权限决定。其它 git 主机（PAT）的规则不变，见第 7 节「git 出站策略」。
- **gh**：orbd 写 `~/.oryn/gh/`，其中 `hosts.yml` 里只有占位令牌 `oryn-proxied`。`gh` 经 `~/.oryn/gh.sock` 发往 `api.github.com` 与 `uploads.github.com` 的请求（后者用于 `gh release upload`）由出站代理换上真实令牌，响应里的令牌换成 `oryn-redacted`。orb 的环境变量、磁盘、出站代理的响应里都没有真实令牌。
- **提交作者**：Agent 身份的 orb 固定用 Agent 账号（`<id>+<login>@users.noreply.github.com`）。我的账号身份按「项目覆盖 > 所有者默认 > 我的 GitHub 账号推出的同格式身份」取。

runner 线程与协调宿主不下发任何 GitHub 凭据。我的账号未连接或需要重新授权时，我的账号身份的 orb 回落到原来的 `git/github.com/token`。Agent 身份的 orb 在 Agent 账号不可用时不回落，git 与 gh 都不可用。

### 身份规则

|线程|身份|
|---|---|
|交互式线程、首屏新建的线程|我的账号|
|全自动线程：GitHub 触发规则、项目级定时、发布哨兵|设置项「自动化任务的 GitHub 身份」，默认 `agent`|
|agent 在 orb 内派生的子线程|沿用父线程|

全自动线程的身份按以下规则决定（Q6）：

- **不可信入口**：「任何人」档（`drivers=anyone`）的规则，以及外部 PR 评审规则（`pull_request_opened`），只用 Agent 账号。Agent 账号未连接或需要重新授权时**不回退**：不建线程，事件记为 `ignored:agent_required`，同时给你发一条通知（「GitHub trigger skipped」）。
- **其余全自动线程**：设置为 `agent` 且 Agent 账号可用时用它，否则回退到我的账号，并在首条提示词末尾追加一行 `[Oryn: the Agent GitHub account is unavailable, so this automated thread uses the owner's GitHub account instead.]`。设置为 `user` 时一律用我的账号。
- 断开 Agent 账号**不会**把设置改回 `user`，Agent 账号未连接时也可以选 `agent`。`mention` 规则只能在 Agent 账号已连接时创建（否则 409 `github_agent_required`）。

触发规则的「驱动者」档位决定谁的发言能驱动 agent。档位同样作用于已关联线程的跟进评论：

|档位|放行|
|---|---|
|`writers`|我的账号、规则维护者、`author_association` 为 OWNER / MEMBER / COLLABORATOR，或经 `collaborators/{login}/permission` 查到 write / admin 权限（只在我的账号对仓库有 push 权限时才查这个接口，结果缓存 5 分钟）|
|`writers_and_author`（默认）|在上一档之外，再加 issue 或 PR 的作者|
|`anyone`|任何人，但按发送者限流：上一档的人不限，CONTRIBUTOR 每小时 10 次，其余每小时 3 次，超限记为 `ignored:sender_rate_limited`|

Bot 一律不驱动，除非它在规则的「评审机器人」列表里，这时它的发言视为维护者指令，不需要 @。`issues.labeled` 只看打标签的人，与档位无关：我的账号、仓库 owner、规则维护者，或查到写权限的人。每条规则每小时至多建 10 个线程，`issue_opened` 与 `issue_reopened` 共用同一份额度。不论档位，issue 与评论的内容都只放进首条提示词末尾的 `<untrusted-github-content>` 数据块，不作为指令。

### 事件来源与额度

三种来源合成同一种内部事件，后续处理完全相同。同一个投递 guid 只处理一次，投递记录与载荷保留 7 天，期间可以用 `oryn github replay <guid>` 重放已经处理完的投递。

|来源（`source`）|何时使用|做法|额度|
|---|---|---|---|
|`webhook`|已绑定的仓库，且我的账号是该仓库 admin|Oryn 建一个仓库 webhook（`name=web`、`content_type=json`、每个 hook 独立的 secret），订阅 `issues`、`issue_comment`、`pull_request`、`pull_request_review`、`pull_request_review_comment`、`check_suite`、`status`、`workflow_run`、`repository`、`push`（M24：触发项目仓库镜像拉取；升级前建的 hook 在下一次全量校正时补上）。入口按 `X-GitHub-Hook-ID` 找到这个 hook 的 secret，用它校验 `X-Hub-Signature-256`，并要求 `X-GitHub-Hook-Installation-Target-ID` 等于仓库 id，任一项不符都回 401。请求体上限 25 MiB，超过回 413。每 10 分钟全量校正一次，同 URL 的已有 hook 会被接管|不占 API 额度|
|`poll`|有 `active` webhook 的仓库|每 30 秒读一次 `GET /repos/{o}/{r}/hooks/{id}/deliveries`；最近 10 分钟收到过实时投递时放宽到 5 分钟。第一次只建立基线，之后为游标之后的新投递补取详情。GitHub 访问不到 webhook 地址时，投递照样记进日志，靠这条路补齐|单独计额：资源 `webhook_deliveries`，500 次/小时，列表与详情各计 1 次。带 `If-None-Match` 返回 304 时不计数。剩余低于 50 时暂停到 `X-RateLimit-Reset`|
|`rest`|没有 `active` webhook 的情况（`no_admin`、`org_restricted` 等）|**仓库级**：已绑定、有启用规则的仓库，每 60 秒用我的账号读 `issues?since=`（新 PR 也从这里发现）、`issues/comments?since=`、`actions/runs?event=push&status=completed`。**对象级**：线程关联的 issue 与 PR（包括向上游开的 PR），每 60 秒用线程 orb 的槽位读本体、评论、评审、行内评论、head 的 `check-runs` 与 `status`。请求都带 ETag，304 不产生事件。对象进入终态满 24 小时后不再轮询|按槽位记核心额度：`X-RateLimit-Remaining` 低于 500（`rateLimitFloor`）时该槽位的轮询间隔加倍，耗尽时暂停到 `X-RateLimit-Reset`|

设置页「仓库 webhook」卡片按仓库列出状态（`active` / `no_admin` / `org_restricted` / `sso_required` / `error`）和最近一次投递，来源显示为「实时」或「轮询」。GitHub 拒绝创建或改写 webhook 时，状态为 `error`，GitHub 的报错显示在列表里，下一轮同步会重试。如果是 GitHub 不接受 webhook 地址，就在「Webhook 基址」里改填一个语法合法的公网地址：投递即使失败也会进日志，轮询照常取回。

回流的规则与 M14 相同：评审、行内评审评论、会话评论在 15 秒窗口内合成一条自动消息；CI 转为失败立即投递，最多 5 轮；单个 PR 关联每小时最多回流 20 条，超限暂停这个关联。PR 合并后线程标记为已交付。和 PR 不在同一仓库的关联只在两种情况下建立：出站代理观察到本线程创建了这个 PR；或者 PR 的头提交是本线程推送的，并且作者是已连接的两个账号之一。

### 门禁

门禁是项目级配置（项目编辑器的「推送 / 开 PR 门禁」，`Project.githubGates`），默认关闭，由出站代理强制执行：

|字段|含义|
|---|---|
|`push`|推送门禁：经出站代理推送到 github.com 的每个 `refs/heads/*` 新 SHA，都必须有本 orb 的 `push` 级以上 `github_prepare` 通过记录|
|`pr`|PR 门禁：创建 PR 时，head SHA 必须有 `pr` 级通过记录，正文还要满足下面两项|
|`prHeadings`|PR 正文必需的二级标题（不含 `## `）|
|`requireClosingRef`|PR 正文必须含 `Fixes\|Closes\|Resolves #N`|
|`fixCmd` / `checkCmd` / `testCmd`|`github_prepare` 执行的修复、检查、测试命令（时限依次为 600、600、3600 秒）|

`github_prepare` 在线程工作目录里按顺序检查：HEAD 不是 detached；当前分支不是默认分支（发布线程例外）；`origin/<默认分支>..HEAD` 非空；把提交信息里的字面量 `\n` 改写为真换行（只改信息，tree 不变）；运行修复命令，产生的改动并入 HEAD；工作树干净；区间内每个提交的作者等于线程身份的提交身份；运行检查命令；`level=pr` 时再运行测试命令。全部通过后，orbd 经 ctl `github.prepare.report` 登记这个 SHA。`pr` 级记录同时满足 `push` 级。`skip_checks=true` 只跳过三条命令，必须带 `reason`，产生的记录带 `skipped` 标记，同样放行，并计入审计。

此外还有三项**线程限制**，由建立任务的触发规则决定。出站代理每处理一个写请求都向控制面现查一次（包括推送协商阶段 `info/refs?service=git-receive-pack`），不缓存在 setup 时生成的策略里。现查失败时三项都按开启处理：

|限制|来源|效果|
|---|---|---|
|只读评审（`ReviewOnly`）|`pull_request_opened` 规则建的线程|拒绝推送（推送协商阶段就回 403）、创建 PR、合并、一切改 ref 的 API|
|只发 COMMENT（`CommentOnlyReviews`）|规则选项「评审只发 COMMENT」|review 只放行 `COMMENT` 和没带 event 的待提交 review，`APPROVE` / `REQUEST_CHANGES` 被拒|
|实现需授权（`ImplAuth`）|规则选项「实现需授权」|创建 PR 前必须已获实现授权（见下文「issue 任务」）|

覆盖范围（REST 前缀 `/repos/{o}/{r}` 与 `/repositories/{id}` 等价）：

|类别|REST|GraphQL mutation|处理|
|---|---|---|---|
|创建 PR|`POST /pulls`|`createPullRequest`|只读评审时拒绝；实现需授权时检查授权；PR 门禁开启时检查 head SHA 与正文|
|改 ref（SHA 可知）|`POST /git/refs`、`PATCH\|DELETE /git/refs/*`|`createRef`、`updateRef`、`updateRefs`、`deleteRef`|只读评审时拒绝；推送门禁开启时按分支检查通过记录与默认分支规则|
|改 ref（SHA 不可知）|`PUT\|DELETE /contents/*`、`POST /merges`、`POST /merge-upstream`、`PUT /pulls/{n}/update-branch`、`POST /branches/{b}/rename`|`createCommitOnBranch`、`mergeBranch`、`updatePullRequestBranch`|只读评审或推送门禁开启时拒绝|
|合并|`PUT /pulls/{n}/merge`|`mergePullRequest`、`enablePullRequestAutoMerge`、`enqueuePullRequest`|只读评审时拒绝|
|review 事件|`POST /pulls/{n}/reviews`、`POST /pulls/{n}/reviews/{id}/events`|`addPullRequestReview`、`submitPullRequestReview`|只发 COMMENT 时检查 event|
|Oryn 的 webhook|`PATCH\|DELETE /hooks/{id}`、`PATCH /hooks/{id}/config`|—|不论门禁开关，始终拒绝|
|git 推送|`git-receive-pack`|—|只检查 `refs/heads/*`；删除分支不需要通过记录|

- **默认分支**：推送门禁开启时，只有进行中的发布任务（`awaiting_ci` / `fixing`）能推送或改写默认分支，其余线程一律被拒。这一条在出站代理执行，不依赖 orb 内 orbd 的检查。推送门禁关闭（默认）、或项目没有门禁（包括无项目的 orb）时不检查默认分支。
- **失败即拒绝**：推送门禁、PR 门禁或任一项线程限制开启时，写请求体超过 1 MiB、带压缩编码、无法解析，或受管控的 GraphQL 参数无法静态求值，一律拒绝。
- **拒绝的形式**：推送被拒时，git 显示 `! [remote rejected] HEAD -> <分支> (oryn: <原因>)`；REST / GraphQL 请求回 403，正文为 `oryn: <原因>`。被拒的请求不转发，但照样记一行审计。

robomp 预设会带上门禁配置：「robomp 分诊」打开推送门禁与 PR 门禁，必需标题为 `Repro`、`Cause`、`Fix`、`Verification`，并要求关闭引用；「robomp 发布哨兵」只打开推送门禁。套用预设时，规则编辑器会提示是否把这份门禁应用到本项目。

### 发布哨兵

`workflow_run_completed` 规则（默认不建，「robomp 发布哨兵」预设）只处理 `status=completed`、head 提交信息以规则前缀开头的 run（默认前缀 `chore: bump version to `），前缀之后的部分就是版本号。每个仓库的每个版本对应一行状态，同一仓库的事件与扫描串行处理：

|状态|含义|
|---|---|
|`awaiting_ci`|等待发布提交上的 CI 与 Release|
|`fixing`|发现阻塞结论，修复线程正在修|
|`green`|全部 run 完成、没有阻塞结论，且 tag `v<version>` 有非草稿 Release|
|`failed`|修复轮数用尽、默认分支被别人推进、发布线程被删除或归档、agent 调用 `github_abort_task`，或超时（见下）|
|`superseded`|同一仓库出现了更新的版本。被取代的版本会中止其线程正在进行的回合|

- **阻塞结论**：`failure`、`timed_out`、`startup_failure`、`action_required`。远端 tag `v<version>` 已不指向这个 run 的 SHA 时，这个 run 记为 `ignored:stale`。
- **修复轮数**：遇到阻塞结论时开启（第一轮）或续跑同一个线程，每轮加 1，**至多 5 轮**；第 5 轮之后仍阻塞就置为 `failed`。修复推送使用 `git push --atomic --force-with-lease=refs/tags/v<version>:<tagOid> origin HEAD:refs/heads/<默认分支> refs/tags/v<version>`：分支只能快进，tag 只有仍指向旧值时才移动。`tagOid` 由 `github_release_status` 给出。修复推送把 tag 移到新提交后，状态回到 `awaiting_ci`，等新提交的 CI。
- **推默认分支的例外**：只有 `awaiting_ci` 与 `fixing` 两个状态享有推送门禁下推默认分支的例外，`github_prepare` 也只在这两个状态下跳过「不在默认分支」一项。进入终态后，例外随即收回。
- **扫描**：每 60 秒扫一次进行中的版本。`awaiting_ci` 每次都用我的账号重新评估：tag 仍指向发布提交、CI 全部通过、有非草稿 Release 时置为 `green`（Release 可能在全部 run 结束之后才手动发布，`release` 事件不在订阅里）；自最后一次状态变化起超过 24 小时（`releaseAwaitMax`）仍不是 green，就置为 `failed` 并通知你。`fixing` 不重新评估 CI，但适用同样的 24 小时期限：从进入 `fixing` 起 24 小时内没有推送修复，就置为 `failed`（「no fix was pushed within 24h」）。
- **列表翻页**：发布提交上的 run 列表与每个阻塞 run 的 job 列表都分页读取，每页 100 条，各至多 3 页。run 列表读到上限仍有下一页时**不判 green**，保持 `awaiting_ci`，最终由 24 小时期限兜底；失败报告里会注明列表被截断。失败报告至多给 5 个阻塞 job 附失败步骤和日志最后 120 行，其余列入 overflow。
- **发言与通知**：终态不在 GitHub 上发言，只通知你。有线程时发线程通知；没有线程的版本只在 `failed` 时发一条系统通知。发布线程的 orb 不冻结。
- **保留**：`green`、`failed`、`superseded` 的版本行保留 30 天，之后删除。
- 修复线程的身份按「身份规则」中的非不可信规则决定，首条消息首行为 `[Oryn release sentinel on <owner>/<repo> v<version> — started automatically, not typed by the owner]`，失败报告放在 `<untrusted-github-content>` 数据块里。

### issue 任务

触发规则建立的 issue / PR 线程带一个任务，线程页头可以看到它的状态：

|状态|谁设置|
|---|---|
|`new`|建立任务时；终态任务被维护者重开时|
|`reproducing`、`fixing`、`reviewing`、`opened`、`needs_info`|agent 调用 `github_task_state`；`needs_info` 时还会加 `needs-info` 标签|
|`closed`|issue 在 GitHub 上被关闭（包括被自动关闭）|
|`merged`|关联的 PR 合并|
|`abandoned`|agent 调用 `github_abort_task`，或你执行 `oryn github cleanup <owner>/<repo>#<n>`|

- **终态**：`closed`、`merged`、`abandoned`。进入终态时，Oryn 取消待执行的自动关闭并冻结线程的 orb，线程本身保留。之后的非指令评论不再回流；会话评论在 issue / PR 下收到一条固定回帖（正文移植自 robomp），事件记为 `ignored:finalized`。维护者（我的账号、OWNER / MEMBER / COLLABORATOR、规则维护者）在评论里 @Agent 账号时，任务回到 `new`，评论照常回流并唤醒线程。
- **回合结束后冻结**：`github_abort_task` 由线程自己调用，工具应答还要经 ctl、orbd 才能送达 omp，所以 Oryn 不立即冻结，而是等线程离开 `working` 再冻结。登记后 5 分钟仍未离开 `working` 时，中止当前回合并直接冻结。其余进入终态的途径都立即排队冻结。
- **实现授权**（规则开启「实现需授权」时）：满足以下任一条件，出站代理才放行本线程开 PR，否则拒绝并说明原因：
  - 分类（`github_classify_issue`）是 `bug` 或 `documentation`；
  - 有能授权的人在任务 issue 上打了 `accepted` 标签，或在评论里写了 `go ahead`（不区分大小写）。能授权的人是：仓库 owner 本人、我的账号、规则维护者；评论还接受 `author_association` 为 OWNER 的作者（标签事件不带 `author_association`）。
- **任务时限**：规则的「任务时限」写入 `thread_issues.deadline_at`。每 60 秒扫描一次，到点时清除时限（每个只处理一次）、中止当前回合，并发一条线程通知。
- **完成提醒**：规则开启「完成提醒」后，GitHub 投递的回合正常结束时，如果任务状态仍是 `new`、`reproducing` 或 `fixing`，并且分类为 `bug`、`documentation` 或尚未分类，Oryn 就投递一条提醒，每个任务至多 2 次。
- **自动关闭**：agent 回答问题后调用 `github_autoclose`，Oryn 记下线程身份在该 issue 上的最近一条评论。N 小时后（默认 4，规则可配，工具参数可覆盖，范围 1–720），只检查 **issue 作者**在**这条评论**上的 👎：有就取消，没有就以 `state_reason=completed` 关闭 issue。别人的 👎 不算。等待期间有人发新评论、issue 被外部关闭、任务进入终态，排期都同步取消。到期项经原子认领，每条只执行一次；关闭请求返回 404 时记为 `already_closed`。

### 审计

出站代理为每个 orb 的 GitHub 写操作记一行审计：非 GET 的 REST 请求、解析出的 GraphQL mutation（query 不记），以及 `git-receive-pack`（每个 ref 一行）。被门禁拒绝的请求也记，`gate` 列是拒绝原因。agent 调用 `github_abort_task` 时额外记一行 `method=TASK`。审计保留 30 天，可以在设置页「审计」卡片、`GET /api/v1/github/audit` 和 `oryn github audit [--thread] [--repo]` 中按线程或仓库查询。

agent 的发言不追加任何标记。Oryn 在请求侧预登记自己的写操作来识别它们，所以这些写操作产生的事件不回流，也不触发规则，详见 `docs/protocol.md`「M15」一节的「防回流」。

### CLI

`oryn github` 的子命令都输出表格，支持 `--json`：`triage <owner>/<repo>#<n> [--rule]`（合成 `issues.opened` 交给触发规则，打印线程 id）、`replay <guid>`、`status`（issue 任务、发布状态、仓库 webhook 状态）、`cleanup <owner>/<repo>#<n>`、`audit`。

### 风险说明

OAuth App 的令牌**不能按仓库收窄**，对账号能看到的全部仓库都有效；出站代理也不按仓库拦截 github.com。线程能做什么，只取决于它所用账号的权限。因此：

- 全自动线程应使用 Agent 账号，并且这个账号只被邀请到确实需要的仓库，权限给到够用即可；
- 不要把有大量组织权限的个人账号设为 Agent 账号；
- 「任何人」档规则与外部 PR 评审由不可信输入驱动，它们只用 Agent 账号，Agent 账号不可用时宁可不跑，也不退回到我的账号。

### 不在本期范围

- **包仓库推送**：ghcr.io、npm.pkg.github.com 等包仓库要求以真实主机名做 TLS，经占位令牌注入的推送不在本期范围。`write:packages` 只服务于经 `gh api` 调用的 Packages REST API。
- 经 git 克隆 gist（`gist.github.com`）；`gh gist` 走 API，可以用。
- GitHub Enterprise Server 的专门验收、回写 Check run、`delete_repo` 与 `admin:org` scope。

### M14 历史

M14 用的是 GitHub App：设置页按 manifest 创建 App，安装到账号或组织后才能绑定仓库，orb 拿到的是按项目仓库收窄、约 50 分钟过期的 installation 令牌，agent 的发言末尾带不可见标记 `<!-- oryn-agent -->`。升级到 M15 时，迁移 `0034_github_drop_app.sql` 删除旧 App 与安装记录，并清空两个身份槽位：升级后要先按上文注册 OAuth App，再重新连接两个账号。项目的 `github_repo_id`、触发规则、PR 关联与 issue 线程都会保留。GitHub 上的旧 App 不会被自动删除，需要时去 GitHub 的 App 设置里手动卸载、删除。

## 14. Hindsight 记忆服务（M16）

Oryn 的 agent 记忆存放在一个单独部署的 [Hindsight](https://github.com/vectorize-io/hindsight)（MIT）服务里，版本固定为 1.2.x，镜像是 slim 版，不带本地模型。所有项目共用一个 bank `oryn`，靠 `project:<项目 ID>` 标签相互隔离；全局记忆打 `oryn:global` 标签，只有主人在管理页批准的候选才能写入。orb 与 runner 里的 omp 经各自的本机常驻出站通道只连 orynd 的记忆网关 `/memory/`，网关替它们填写项目标签（runner 按线程所属项目区分），并注入 Hindsight 的 API key。Hindsight 调用模型、嵌入和重排时，也经 orynd 的中转 `<public_url>/memory-relay/v1/`，由 orynd 换上真实的模型与密钥。所以 Hindsight 的 `.env` 里没有任何 provider 密钥，orb 与 runner 里也看不到 Hindsight 的 key。

### 部署

1. **同机部署**（推荐）：在 orynd 所在的机器上部署。API 只发布在 `127.0.0.1:8888`，Postgres 不发布端口。
2. **远端部署**：在本目录 `.env` 里加一行 `HINDSIGHT_PUBLISH_ADDR=<内网地址>`，或者在 Hindsight 前面加一层 TLS 反代，只让 orynd 所在的机器能访问。Hindsight 必须能访问 orynd 的 `public_url`，因为模型请求要走中转。

两种方式的步骤相同：

```bash
# 1. 管理页「更多 → Agent → 记忆」：先选好模型和嵌入接口，再点「生成 .env」，
#    把内容保存为 deploy/hindsight/.env。API key 和中转令牌只显示这一次；重新生成后旧值立即失效。
# 2. 启动（首次会构建带 pgvector 的 pgroonga 镜像）
docker compose -f deploy/hindsight/compose.yaml up -d
# 3. 管理页「连接」填 Hindsight 地址（同机为 http://127.0.0.1:8888），状态变为「正常」后打开开关
```

- `.env` 每种用途（default / retain / reflect / consolidation / mental model）只写一个固定的别名模型和协议类型（openai / anthropic / gemini）。在同一协议内换模型立即生效。换了协议类型时，管理页会给出要替换的几行，并提示重启 Hindsight（`docker compose -f deploy/hindsight/compose.yaml up -d api`）；在点「我已重启」之前，该用途的中转一律返回 409。
- 重排是可选的：管理页没配重排接口时 `.env` 写 `HINDSIGHT_API_RERANKER_PROVIDER=rrf`（Hindsight 自带，只按 RRF 分融合排序，不调用模型），配了才走 Cohere 兼容中转；cohere 的三行总是写出，rrf 下被忽略。之后添加或删除重排接口时，管理页同样给出要改的那一行（切到 cohere 时连同三行 cohere 配置），重启 Hindsight 后点「我已重启」。
- 「我已重启」会把所有待部署的变化（各用途的协议类型和重排模式）一起确认。所以协议类型和重排模式同时变了时，模型卡和接口卡给出的是同一份完整片段，按其中任何一份改完 `.env` 再确认都可以。
- 中文检索依赖 pgroonga（`HINDSIGHT_API_TEXT_SEARCH_EXTENSION=pgroonga`）。输出语言固定为简体中文。
- `HINDSIGHT_DB_PASSWORD` 只在数据卷首次初始化时生效，之后改它会导致 API 连不上库。数据库只在 compose 的内部网络里，所以默认值可以接受；要改就在首次 `up` 之前改。
- 开关、单项目关闭、生成 `.env` 之后，正在运行的线程会出现在 Agent 页的「需要重启」里，下一个会话（或点「重启」之后）才会生效。

### 备份

`hindsight-admin backup` 在一个 `REPEATABLE READ` 事务里导出整个库（bank、document、事实、实体、mental model），得到一个 zip。备份和恢复都只对 `public` schema 生效，这也是 Oryn 所用租户扩展的默认 schema。cron 示例（每天 3:30，保留 14 份）：

```cron
30 3 * * * cd /opt/oryn && f=hindsight-$(date +\%F).zip && docker compose -f deploy/hindsight/compose.yaml exec -T api hindsight-admin backup /tmp/$f && docker compose -f deploy/hindsight/compose.yaml cp api:/tmp/$f /var/backups/oryn/ && docker compose -f deploy/hindsight/compose.yaml exec -T api rm -f /tmp/$f && find /var/backups/oryn -name 'hindsight-*.zip' -mtime +14 -delete
```

也可以直接对库做 `pg_dump`：`docker compose -f deploy/hindsight/compose.yaml exec -T db pg_dump -U hindsight_user -Fc hindsight_db > hindsight.dump`。

恢复（会清空目标库）：先 `docker compose cp` 把 zip 放进容器，然后执行 `hindsight-admin restore /tmp/<文件>.zip --yes`，再执行一次 `hindsight-admin repair-bank --all`。管理页的「导出」是一个只读的 bank 归档（`export-bank`），用来在 Oryn 之外查看或迁移，不能代替备份。

### 升级

compose 里的镜像按 digest 固定。Oryn 只对一个小版本（当前是 1.2）做过测试：Hindsight 报告的版本不在这个小版本内时，管理页状态会显示「版本警告」。

- **补丁升级**（1.2.x → 1.2.y）：先备份，把 `compose.yaml` 里 api 镜像的 digest 换成新补丁的 digest，然后 `docker compose -f deploy/hindsight/compose.yaml pull api && docker compose -f deploy/hindsight/compose.yaml up -d api`。数据库迁移在启动时自动执行；管理页状态回到「正常」即完成。
- **小版本升级**（1.2 → 1.3），要等 Oryn 把测试过的小版本更新后再做，逐项检查：
  1. 读新版本的 release notes，重点看环境变量的改名或删除（与 `.env` 的键对照）、HTTP API（`/v1/default/banks/{bank}/memories`、`documents`、`mental-models`、`reflect`）的变化，以及标签过滤的语义；
  2. 先备份；
  3. 升级 Postgres 镜像之前，确认 pgroonga 与 pgvector 的版本仍然兼容；
  4. 换镜像后执行 `docker compose ... exec api hindsight-admin run-db-migration`，再执行 `hindsight-admin repair-bank --all --dry-run`；
  5. 在管理页确认状态正常，在任一项目线程里执行一次 recall 和 retain，并确认两个项目之间互相看不到对方的记忆。

### 换嵌入模型（reembed）

任何嵌入模型的更换都必须重新嵌入，维度相同也不例外，否则新旧向量混在一起会让召回变差。嵌入「接口地址」换到另一个服务，即便模型名相同，也按换模型处理。

**必须停机迁移。** Hindsight 按 `provider:model:dimension` 识别嵌入模型（`embeddings.py` 的 `embedding_model_id`），而 Oryn 对它只暴露一个别名 `oryn-embed`，所有请求都经同一个中转。中转一旦切到新模型，仍在运行的 API 和 worker 会立即用新模型生成查询向量与写入向量，与库里的旧向量混在一起。维度相同时 Hindsight 的写保护也认不出模型已换，所以官方文档里那种「边运行边迁移」的流程在这里不成立。迁移期间记忆不可用：agent 的 recall 会失败并给出提示，自动写入留在各 orb 与 runner 的 Core outbox 里，Hindsight 恢复后补发（outbox 最多保留 7 天）。

bank 里已有记忆时，管理页的嵌入模型与维度只读。只有勾选「换嵌入模型（reembed）」的保存才会被接受，而且 orynd 会先探测 Hindsight：还能连上时返回 409 `hindsight_running`，保证第 3 步发生在停机之后。步骤（下文 `dc` 代表 `docker compose -f deploy/hindsight/compose.yaml`）：

1. 先备份（见上文「备份」）；
2. 停 API（worker 跑在同一个容器里，会一起停）：`dc stop api`。Postgres 保持运行；
3. 在管理页把嵌入接口改成新模型和新维度，并勾选「换嵌入模型」后保存；
4. 维度有变化时，把 `.env` 里的 `HINDSIGHT_API_EMBEDDINGS_OPENAI_DIMENSIONS` 改成新维度；
5. 用一次性容器执行迁移，它读的是同一份 `.env` 与数据库地址，不发布端口：`dc run --rm --no-deps api hindsight-admin reembed --all-schemas`。每个 schema 显示 `swapped` 即完成。中途中断时重跑同一条命令，会从断点继续；在它完成之前不要启动 API；
6. 启动 API：`dc up -d api`。等管理页状态回到「正常」；
7. 解除写保护：`dc run --rm --no-deps api hindsight-admin reembed --all-schemas --finish`。

迁移失败、想退回旧模型时：保持 API 停止，把备份目录挂进一次性容器执行恢复：`dc run --rm --no-deps -v /var/backups/oryn:/backups:ro api hindsight-admin restore /backups/<文件>.zip --yes`。然后把管理页的嵌入改回旧模型（同样要勾选「换嵌入模型」），`.env` 的维度也改回旧值，再启动。

### 全局助手的主人记忆（M17）

主人记忆是全局助手专用的第二个 bank `oryn-owner`，与工程记忆共用同一个 Hindsight、同一把 key 和同一个模型中转，不需要额外部署。前提是工程记忆已经启用并连上（管理页状态「正常」）；否则「助手记忆」分区只显示提示。

**开启**：在「更多 → Agent → 记忆 → 助手记忆」打开开关，填写主人的名字与别名（写进 bank 的抽取说明，抽取时统一写成「主人」；别名填不全时，同一个人会被拆成两个实体）。首次开启回填最近 30 天内有活动的对话与线程，进度显示在状态卡里。

**写什么、什么时候写**：orynd 每 5 分钟扫描一次。助手对话在最后一条主人消息之后空闲 30 分钟或被归档时写入；工作线程（含 Space 子线程与 Runner 线程）在 idle 且空闲 30 分钟、归档或交付后写入。按回合写：主人的话加 agent 的最后一条回复（截到 600 字），工具调用、工具结果、thinking 与超过 8 行的代码块不写。写入前用发布与分享的同一套密钥扫描规则和密钥库里的明文值做脱敏，命中处替换成「〔已脱敏〕」。被排除的对话或项目不写入，已写入的随之遗忘。

**主题与注入**：每天一次主题整理，保留固定的「主人画像」，另有至多 12 个动态主题。主题生成时只读事实与 observation，不读其他主题（否则会照抄自己的旧内容）。助手启动时把主题（合计约 8,000 字）与最近 14 天的活动附在系统提示词末尾，按对话存快照，1 小时内重新拉起沿用快照。

**判断模型接口**：`recall_memory` 先在 Hindsight 召回前 50 条（关掉 Hindsight 自带的重排），再交给判断模型逐条打 0–3 分：≥ 2.0 正常给出，1.5–2.0 标注「部分相关」，< 1.5 不给。默认用 Google Gemini Decisions API 的 `gemini-decision-1`（地址 `https://generativelanguage.googleapis.com`），key 只写不读。没有配置、超时或返回 429 时，退回 Hindsight 的原始排序并标注「未经重排」，召回不会因此失败。

**成本**（2026-11-17 用主人 16 天的真实会话实测，抽取与主题模型为 `gemini-3.5-flash-lite`；按 token 计，价格以所用模型的发布价换算）：

| 环节 | 实测 |
|---|---|
| 回填 79 个会话（117 个文档、453 条记忆）：写入 + 整理 | 约 135 万 token |
| 首次主题整理：一次 reflect + 9 个主题全量生成 | 约 293 万 token（每个主题约 31 万，输入约一半命中缓存） |
| 之后的日常 | 只刷新有新事实的主题（增量），同一主题间隔不少于 20 小时，「主人画像」每周至少刷新一次 |
| 每次 `recall_memory` 的判断模型重排（50 条候选） | 探查时按发布价约 $0.0008 |

24 小时与 7 天的用量在状态卡里按 Hindsight 的 `llm-requests` 统计显示。

**真实数据回归**：改抽取说明、主题约束、换模型或换判断模型之后，对一个已写入真实数据的 bank 跑一次回归（`internal/ownermem/testdata/eval/cases.json`）：

```bash
ORYN_OWNERMEM_EVAL_URL=http://127.0.0.1:8888 ORYN_OWNERMEM_EVAL_KEY=<Hindsight key> \
ORYN_OWNERMEM_EVAL_DECIDE_KEY=<判断模型 key> ORYN_OWNERMEM_EVAL_BANK=oryn-owner \
  go test -tags ownermem_eval ./internal/ownermem/ -run Eval -v
```

不带构建标签时 `go test ./...` 不运行它。

## 15. SSH 主机与控制面网关（M18）

在「更多 → Agent → SSH 主机」（或 `oryn ssh …`）登记的主机，由 orynd 自己发起 SSH 连接；orb 与 runner 里的 agent 只连本机的 `127.0.0.1:7822`，经 link 交给 orynd 的网关。私钥和密码只存在 orynd 的数据库里（`secrets.Box` 加密），不进 orb 或 runner。协议细节见 [protocol.md](protocol.md)「M18」。

- **网络**：目标机（以及跳板）要能从 **orynd 所在的机器**访问，orb 或 runner 能不能直接访问无所谓。orb 的网络隔离（第 7 节）不用为 SSH 开口子。
- **网关主机密钥**：`<data_dir>/ssh-gateway.key`，首次启动时生成。备份数据目录时一并备份；换掉它之后，各 orb 与在线 runner 会在下一次下发时拿到新的 known_hosts 条目，不用手工处理。
- **授权钥匙**：在设置页生成钥匙，把公钥加到目标机用户的 `~/.ssh/authorized_keys`；也可以导入已有私钥（带口令的会在导入时解开，之后不再需要口令）。密码登录同样可用，遇到多因素认证会直接失败。
- **指纹**：第一次成功连接时固定目标机的主机公钥。目标机重装或换密钥后，连接会被拒绝并提示「指纹变了」；确认无误后在设置页（或 `oryn ssh reset-host-key <名>`）重置。
- **作用范围**：全局主机对所有工作 orb 与 Runner 线程可见，项目主机只对该项目的 orb 与属于该项目的 Runner 线程可见，同名时项目主机优先。触发规则、定时等无人值守入口建出来的全自动线程，只能用勾了「允许全自动线程使用」的主机。协调宿主不接入网关。Runner 不修改用户的 `~/.ssh`：`orbd` 为每个线程的监督器生成一把临时密钥供网关识别线程并按项目放行，bash 里用 `ssh -F ~/.oryn/ssh/config <名>` 连接（局限与用法见第 11 节）。
- **端口转发**：默认只允许执行命令、交互 shell 与 SFTP。勾选「允许本地端口转发」后才接受 `ssh -L`；远程转发、agent 转发与 X11 一律拒绝。
- **审计**：网关上的每次连接与命令、设置页的测试 / 终端 / 文件操作、agent 的 `ssh_exec_many` 都记一行审计（命令原文最多 4096 字节，交互式会话不录内容），保留 30 天，在设置页与 `oryn ssh audit` 查看。
- **反代**：网页终端走 `wss://<域名>/api/v1/ssh/hosts/<id>/terminal`，第 3 节反代示例里的 `Upgrade` / `Connection` 头已经覆盖，无需另加。

## 16. DevelopServerUS 本机生产实例

当前版本为 `v0.1.0-rc.7`（提交 `2fdda936`，Core `oryn-v19.0.1-4`），控制面、provider、重排适配器与 Orb 镜像 `oryn-orb:prod-2fdda936` 同版本；镜像以按 ID 钉定的 `oryn-orb:prod-base-6374201aae3e`（`oryn-orb:dev` 在 `7afe3505` 的构建）为底，只覆盖 `omp`、`orbd`、`oryn` 与冒烟脚本。生产产物在 `/root/Oryn/src` 独立克隆构建，未覆盖本仓库的开发二进制、数据、服务或 `oryn-orb:dev` 镜像。升级前已存在的 Orb 仍用旧镜像 `oryn-orb:prod-def2968`，按旧版 orbd 降级运行，删除重建后才换新镜像。

升级流程：在 `/root/Oryn/src` 检出目标提交并放好与 `core/source.json` 哈希一致的 `build/core/x64/omp`，改 `ops/build-production.sh` 头部的提交、版本、基础镜像与目标镜像，运行它（只构建与离线冒烟，不碰运行中的服务）；再改 `ops/install-production.sh` 头部的版本与镜像并运行：停控制面与 provider，先把 `data/` 整目录归档、把 `bin/`、`lib/`、`etc/` 原样（保留属主、权限与 ACL）拷到 `backups/pre-<版本>/`，然后才改 `etc/provider.yaml` 的 `image` 并安装新产物，启动后数据库迁移随 orynd 自动执行。备份只收紧备份根目录（0700），里面的文件保持原权限：`gemini-rerank` 以动态非特权账号运行，拷回的二进制必须仍是 0755。

回滚：

```bash
systemctl stop oryn-prod-provider.service oryn-prod.service
b=/root/Oryn/backups/pre-<版本>
mv /root/Oryn/data /root/Oryn/data.failed-$(date +%s) && tar -C /root/Oryn -xpf "$b/data.tar"
cp -a "$b/bin/." /root/Oryn/bin/ && cp -a "$b/lib/omp" /root/Oryn/lib/omp && cp -a "$b/etc/." /root/Oryn/etc/
grep '^  image: ' /root/Oryn/etc/provider.yaml          # 应是升级前的镜像
stat -c '%a %U:%G %n' /root/Oryn/bin/* /root/Oryn/lib/omp  # 应是 755 root:root
systemctl start oryn-prod.service && systemctl restart oryn-prod-rerank.service && systemctl start oryn-prod-provider.service
```

升级后新建的 Orb 用的是新镜像，回滚到旧控制面后删除重建。

### 入口与端口

| 用途 | 地址 |
|---|---|
| 正式入口 | `https://oryn.ling.plus` |
| 原始拼写别名 | `https://oyrn.ling.plus`，301 跳转正式入口并保留路径与查询参数 |
| Portal | `https://t-<线程短码>-p<端口>.oryn.ling.plus` |
| 控制面 HTTP | `127.0.0.1:26934`，不对公网开放 |
| Orb link TLS | `172.30.240.1:20433`，仅生产专用网桥可访问 |
| 协调沙箱出站代理 | `18549`，由协调宿主按需在自己的网络命名空间内监听 |
| Hindsight API | `127.0.0.1:22004` |
| Gemini Decisions 重排适配器 | `127.0.0.1:27183/v1/rerank`，仅回环 |

AliDNS 的 `oryn`、`oyrn`、`*.oryn` 均仅使用 default 线路 A 记录指向 `64.20.45.163`，TTL 600；不配置国内分流。1Panel 站点 12 承载主域名及通配域名，站点 13 承载别名。证书 5 覆盖三个名称，启用自动续期；面板每六小时检查，剩余有效期不足 30 天时续签并自动发布到绑定站点。部署时已演练一次重新签发和双站点自动发布。

反代复用现有 OpenResty，不新建或重启反代容器。配置在 `/opt/1panel/www/conf.d/oryn.ling.plus.conf` 与 `/opt/1panel/www/sites/oryn.ling.plus/proxy/root.conf`：WebSocket、Host 透传、3600 秒超时、600 MiB 上传上限、请求与响应禁缓冲、禁缓存、关闭 gzip。同目录的 `git.conf` 为托管仓库 `/git/` 单独放开请求体上限（`client_max_body_size 0`），其余与 `root.conf` 相同；改动后先 `docker exec OpenResty openresty -t` 再 `-s reload`。这两个文件是手写的，在 1Panel 面板里重新保存该站点的反代设置可能覆盖它们。未复制 Sub2API 专属接口拦截与国内分流规则。

### 运行目录与维护

| 路径 | 内容与权限边界 |
|---|---|
| `bin/`、`lib/`、`src/`、`etc/` | root 管理的二进制、钉定 Core、独立源码、配置及 provider 令牌；控制面账号不能修改 |
| `data/` | `oryn-prod` 专属的控制面数据库、加密主密钥、附件、协调宿主与渲染目录 |
| `cli/credentials.json` | root-only 的「本机生产运维」设备凭据；与开发 CLI 登录状态分离 |
| `systemd/` | 生产服务单元，链接到 `/etc/systemd/system/` |
| `hindsight/data/` | PostgreSQL bind mount，无新增命名数据卷 |
| `backups/gemini-memory-before/`、`backups/pre-<版本>/` | 切换 Gemini 前的控制面在线备份、Hindsight 导出及旧部署配置；每次升级前的 `data/` 归档、旧二进制、旧 Core 与配置。均受限访问 |
| `hindsight/.env`、`hindsight/db.env` | 分离保存中转密钥与数据库密码，均为 0600 |
| `ops/` | 构建与安装脚本、构建清单、网络脚本、无密钥 DNS 回滚记录及 TLS 验收证据 |

以下命令只操作生产实例：

```bash
systemctl status oryn-prod.service oryn-prod-provider.service oryn-prod-network.service oryn-prod-rerank.service
/root/Oryn/oryn provider ls
/root/Oryn/hindsight/dc ps
curl --fail https://oryn.ling.plus/healthz
```

`/root/Oryn/oryn` 固定使用生产专用 `ORYN_CREDENTIALS`；不要直接用默认凭据运行生产 CLI。`/root/Oryn/hindsight/dc` 固定读取 `db.env`，更新 Hindsight 的中转配置时不得修改数据库密码。

控制面以无 Docker 权限的专用账号 `oryn-prod` 运行，systemd 保留 `NoNewPrivileges`、只读系统与 HOME，并屏蔽宿主 `.omp`、`.ssh`、`Projects`。协调宿主使用非特权命名空间、Landlock 和 seccomp。Docker provider 单独以 root 运行，默认规格为 2 CPU / 4 GiB，预热池总上限为 1。

生产 Orb 使用 `oryn-prod-orbs` 网桥（`orynp0`，`172.30.240.0/24`），关闭 ICC。`oryn-prod-network.service` 只为该网桥安装自有防火墙规则：宿主仅放行 Orb TLS，转发阻断私网、CGNAT 与链路本地地址；不修改现有开发网桥、SSH 或默认防火墙策略。

### 模型、记忆与初始化

生产配置导入了宿主的模型凭据与角色，不导入仅适用于宿主路径的扩展、技能仓库或 MCP 配置。CodeHub 补齐模型后，`google/gemini-4-hydrogen` 的真实请求已返回成功；生产的 `tiny`、`memory` 角色与 Hindsight 默认用途恢复为 Hydrogen，经控制面代理走 CodeHub，不把新的 Google 官方密钥用于普通对话模型。其他命名角色保持宿主配置，可在管理页调整。

`exa`、`typesafe` 沿用宿主没有 `baseUrl` 的配置，导入时会产生 `env_fallback` 提示：这两个 provider 的真实密钥按现有实现以环境变量下发到 Orb，而不是由模型代理注入。不要把带这些凭据的 Orb 交给不可信用户。

记忆服务使用独立 Compose 项目 `oryn-prod-hindsight`：Hindsight 1.2.1 与 pgroonga / pgvector PostgreSQL。工程记忆 bank `oryn`、助手主人记忆 bank `oryn-owner` 共用 `gemini-embedding-2`，通过 Google 官方 OpenAI 兼容接口 `/v1beta/openai` 调用，固定 1536 维。切换时已停记忆 API、备份并执行全 schema `reembed`；即使事实表为空，也要迁移 mental model 等其它向量列。旧本地 Qwen 嵌入服务不再运行。助手记忆的全局开关保持原值，不因换模型自动开启。

工程记忆使用 `gemini-decision-1` 重排。Hindsight 继续走已有 Cohere 兼容协议，中转目标为本机 `gemini-rerank` 适配器；适配器与助手记忆复用 `internal/geminidecision` 的原生 Decisions 客户端。助手记忆自己的判断模型也配置为 `gemini-decision-1`；它召回时关闭 Hindsight 重排，避免重复判定。

适配器只监听回环，固定调用 Google `/v1beta/decisions`，以动态非特权账号运行，不能读取控制面数据、配置、CLI 凭据或数据库目录。Google key 只由控制面从加密存储注入当前请求，再转换为 `x-goog-api-key`，适配器不落盘、不记录密钥或记忆正文、不跟随重定向、不重试。候选的 0–3 期望分映射为 0–1 分数，同分保持原序；缺分、越界或上游失败使整批明确失败，不返回伪造结果。Google 嵌入和判断请求按官方账户计费。

`gemini-rerank` 随 `make build` 生成，通用 systemd 模板在 `deploy/systemd/oryn-gemini-rerank.service`；本机实际单元位于 `/root/Oryn/systemd/oryn-prod-rerank.service`。适配器无上游地址参数，不能用来代理任意 URL。Hindsight 的管理页会显示 Cohere 兼容传输与实际模型名 `gemini-decision-1`，并非调用 Cohere 的模型。

Hindsight API 的健康检查直接访问容器内 `/health/ready`，`dc up --wait` 会等待迁移与数据库连接就绪，不只检查进程是否运行。模型协议、嵌入维度和重排方式统一由管理页生成的 `.env` 提供，不在 Compose 中固定覆盖，避免后续换模型时出现配置不一致。

首次所有者必须由本人在正式域名的初始化页面注册 Passkey，并保存一次性显示的恢复码；初始化链接不得写入本说明或任务日志。「本机生产运维」设备拥有完整管理权限，完成部署后可在设备管理中吊销；吊销后本机 CLI 需重新配对。GitHub OAuth、可选 FCM、外部 SSH 主机等个人集成需各自授权，部署不代建这些身份。

### 备份与撤销边界

务必异机备份 `data/master.key`；失去主密钥后无法解密控制面保存的模型、GitHub 和 SSH 凭据。控制面数据库应使用 SQLite 在线备份或停机一致性备份，记忆库使用 `pg_dump` 或第 14 节的 Hindsight 备份流程。

全部新增业务 bind mount 的源目录均位于 `/root/Oryn`。Docker 的镜像、Orb 可写层与项目快照仍由现有 Docker data-root 管理，**不包含在 `/root/Oryn` 的普通目录备份里**，也不能用 `docker prune` 清理。宿主服务链接、系统账号，以及复用的 1Panel 站点、证书和面板数据库仍在各自系统目录；本次未为它们新增容器挂载。

停止本实例只操作 `oryn-prod*` 单元和 `/root/Oryn/hindsight/dc`；不要停止 `oryn-dev-*`、Docker 或 OpenResty。DNS 回滚工具 `/root/Oryn/ops/dns-manage.py rollback` 仅处理本次新增记录，并会拒绝覆盖后续修改。撤销 DNS 或站点前应先确认已无客户端与 Portal 使用这些域名。
