# 部署 Oryn

单节点部署：`orynd`（控制面）只监听本机，放在现有反代（1Panel OpenResty）之后；同一台机器上的 `oryn provider run` 用 Docker 跑 orb。

## 1. 安装二进制与镜像

```bash
make web                        # 构建 Web 客户端，复制到 internal/webui/dist 以便嵌入
make build                      # 产出 bin/orynd bin/orbd bin/oryn（嵌入上一步的前端）
sudo install -m 0755 bin/orynd bin/oryn /usr/local/bin/
make image                      # 构建 orb 镜像 oryn-orb:dev（需要 build/core/x64/omp，见 scripts/fetch-core.sh）
```

创建服务用户与目录：

```bash
sudo useradd --system --home /var/lib/oryn --shell /usr/sbin/nologin oryn
sudo install -d -o oryn -g oryn -m 0700 /var/lib/oryn
sudo install -d -m 0755 /etc/oryn
```

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
```

docker0 地址因机器而异，用 `ip -4 addr show docker0` 查看（例如有的机器是 `172.18.0.1`）。orb 不经过反代，直接连这个地址。

`wake_on_focus` 控制打开线程页时是否唤醒冻结的 orb。已停机的 orb 不会因此唤醒：打开线程页时文件面板只读浏览，用户发消息或点「唤醒」后才冷启动。

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
- orbd 启动时探测能否建 user namespace：能则 Chromium 带沙箱运行；不能（升级前建的旧容器、dispatch 驱动的机器）退回 `--no-sandbox` 并保留 Chromium 的警告条。新建的 orb 才有沙箱。
- 镜像里的受管策略 `/etc/chromium/policies/managed/oryn.json`（root 所有，不随 Profile 传播）禁止安装扩展、代理固定直连，防止被注入的 agent 往共享 Profile 里植入扩展或代理。
- Chromium 随镜像发布，**要定期重建 orb 镜像**（`make image`）以跟进浏览器安全更新；已存在的 orb 用的仍是旧镜像。

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

### git 出站策略

orb 内的 git 请求经 orbd 的本机代理（`127.0.0.1:7811/git/<host>/…`）到达控制面，由控制面注入 `git/<host>/token` 密钥后转发到 `https://<host>/…`。令牌从不进入 orb。控制面按 orb 所属项目决定放行范围：

- **有项目且项目 `repo_url` 为 `https://` 地址的 orb**：只放行该项目仓库，可读可写（`git-upload-pack` 与 `git-receive-pack`）。仓库路径须与 `repo_url` 完全一致（不区分大小写，忽略 `.git` 后缀）。同一主机上的其它仓库、其它主机一律拒绝（403）。orb 内的 `url.<代理>.insteadOf` 只覆盖项目仓库这一个地址前缀，其它 `https://` git 地址照常直连公网、不带令牌，公开仓库仍可匿名克隆。
- **无项目的 orb**（以及项目没有 `https://` 仓库地址的 orb）：配置了 `git/<host>/token` 的主机上，任意仓库只读。只放行 `info/refs?service=git-upload-pack` 与 `git-upload-pack`，推送（`git-receive-pack`）一律拒绝（403）。这类 orb 的 `insteadOf` 覆盖整个主机。
- **路径与端点**：只放行 git smart-HTTP 端点（`info/refs`、`git-upload-pack`、`git-receive-pack`），其它路径返回 404。路径里出现 `.` 或 `..` 段、空段（`//`），或 `path.Clean` 后与原路径不同，一律返回 400。这包括 `%2e%2e` 这类编码形式，因为校验针对解码后的路径。git 路由不接受任何 `oryn-ph-…` 占位符。
- orbd 每次执行 setup / resume 时，先删掉自己以前写入的全部 `insteadOf` 条目，再按当前策略重写。项目设置变更后，在下一次唤醒时生效。

### 模型密钥绑定

`models.yml` 中，`providers.<key>.apiKey` 只能引用本 provider 自己的密钥 `omp/models/<key>/apiKey`。只有这一个密钥会走环境变量降级；它若已绑定上游主机（`hosts` 非空），就不允许降级。`models.yml` 里不允许引用 `git/*` 密钥。其它 `${oryn:secret:…}` 引用必须属于 `hosts` 非空的密钥，而且只绑定到 origin 主机与 `hosts` 匹配的路由。`hosts` 为空的密钥只能注入到它自己 provider 的路由，其它任何路由都不行。导入时，provider 密钥的 `hosts` 包括 provider `baseUrl` 与各模型 `baseUrl` 的全部主机。经代理的 WebSocket / Upgrade（101）连接只对响应头做真实值回显脱敏，不改写升级后的字节流，否则会破坏其分帧。

## 8. 协调宿主（Space 与全局助手）

Space 协调者与全局助手的 agent 不跑在 orb 里，而是跑在控制面这台机器上：orynd 内置一个名为 `@host` 的 provider（在 provider 列表里看不到），需要时拉起子进程 `orynd coordinator-host`，子进程先把自己关进操作系统沙箱，再以无工具的协调档启动 omp（每个协调对话一个进程）。子进程唯一的对外通道是 orynd 交给它的 socketpair；模型请求经它内部的代理端口回到 orynd，由 orynd 注入真实密钥，协调 agent 拿不到任何密钥。

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
```

`dir` 下的内容：`bin/omp`（`omp_path` 的 0555 副本，源文件大小或 mtime 变了才重拷）、`homes/`（0755，属于 orynd）与其下每个协调 orb 一个的 `homes/<orb id>/`（0700，属于降权账号，协调 agent 唯一可写的目录：会话文件、`.omp/agent/` 里下发的 `models.yml` 与 `config.yml`、natives 缓存、`.oryn/coordinator/prompts/<线程 id>.md` 系统提示词）、`host.log`（子进程的 stderr，超过 5 MiB 轮转为 `host.log.1`）。`dir` 必须属于 orynd 的运行用户，且不能被组或其他人写。

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
| 启动开关 | omp 以 `--no-tools --tools read,write --no-skills --no-extensions --no-rules --no-context-files --no-lsp` 启动（内置工具只有 `read` / `write`，供全局助手读文件库、写 `~/library/out/`），带一份强制关闭 autolearn、memory、checkpoint、goal、plan、generate_image、lsp、项目 MCP、marketplace 自动更新、启动检查更新、direnv 的覆盖层；环境变量从零构造，只有 `HOME PATH LANG TMPDIR PI_RPC_EMIT_TITLE PI_NOTIFICATIONS OTEL_SDK_DISABLED PI_FILE_LOCK_BACKEND ORYN_THREAD_ID`（`PI_FILE_LOCK_BACKEND=sidecar` 让 omp 的原生文件锁改用 home 里的旁路文件加 OFD 锁，不再用 abstract unix socket） |
| 文件 | 只下发 `models.yml` 与 `config.yml`（`models.yml` 里只有占位密钥），每次协调 agent 启动前重写一遍（助手用 `write` 改过的会被还原）；全局助手文件库同步到 `~/library/{shared,threads}/`（只读）。`write` 能写 HOME 里任何地方，系统提示要求助手只写 `~/library/out/`；cwd 是空目录 |
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

omp 的原生文件锁（会话所有权租约等）在 Linux 上默认 bind 一个 abstract unix datagram socket；协调宿主给 omp 设了 `PI_FILE_LOCK_BACKEND=sidecar`（Oryn Core `oryn-v18.19.0-3` 起），锁改成 `~/.omp/run/…` 下的旁路文件加 OFD 写锁，沙箱因此可以禁止 `AF_UNIX` 的 `socket(2)`（只留 Bun 需要的、生来已连接的 stream `socketpair`）：宿主上的 abstract socket 与 `/dev/log` 这类带路径的 0666 socket 在任何内核上都联系不上。

`deploy/systemd/orynd.service` 不用改：`<data_dir>/coordinator` 在 `ReadWritePaths` 之内，单元没有限制命名空间与系统调用。**如果你自己给单元加了 `RestrictNamespaces=`，协调宿主会退到「按端口限制」模式**；加了 `SystemCallFilter=` 且挡掉了 `landlock_*` 或 `seccomp`，协调功能会不可用。

**代理端口**：有网络命名空间时 `egress_port` 在命名空间里，与宿主上的端口互不相干。没有命名空间（「按端口限制」模式）时它直接绑定在宿主的 `127.0.0.1` 上，必须是宿主上没人用的端口；被占用时协调功能立即不可用，`GET /api/v1/coordinator` 与助手页给出原因「出站代理端口 127.0.0.1:<port> 已被占用…」，`coordinator check` 也报同一条，此时在 `coordinator.egress_port` 换一个端口后重启 orynd（不会自动换：端口同时写进 omp 的配置与沙箱的连接白名单）。在这种模式下，orynd 运行着协调宿主时再跑 `coordinator check`，端口正被它自己占着，同样会报这一条，先停 orynd 再检查。

root 或 Docker 部署：`coordinator.dir` 放在降权账号进得去的地方（容器里挂一个卷），`data_dir` 保持 0700。专用账号建在容器里时，重建容器后会再建一次（uid 可能不同，HOME 属主随之改过来）。

### 残余风险

- 没有网络命名空间时，沙箱内进程能向**任意地址**的 `egress_port` 端口发起 TCP 连接（Landlock 只按端口管）。
- `/proc` 只读可见，其它进程的命令行可读（环境与内存读不到）。
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

宿主上的「冻结」就是结束子进程（协调 omp 一个就上百 MB，留在内存里没有意义）；会话文件在 `<dir>/homes/<orb id>`，下一条消息时接着同一个会话。子进程意外退出时，若协调 orb 已就绪，orynd 会把它停下再唤醒；若还在启动（尚未就绪），就原地重新拉起子进程。5 分钟内连续 3 次意外退出后不再拉起：已就绪的停在 `stopped` 等下一条消息再试，启动中的由连接超时转为 `failed`。

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

runner 是你自己的机器（笔记本、工作站、NAS）。在那台机器上以普通用户运行 `orbd run --runner`，它主动连到 orynd；之后新建线程时可以选「这台 runner 的某个目录」，线程里的 agent 就直接在那个目录工作。用的是那台机器自己的 omp 配置（`~/.omp/agent`）和 git 凭据。

### 安全边界（先读）

- **runner 完全信任控制面。** 能操作 orynd 的人（网页会话、设备令牌、或攻破了 orynd 的人），都能以 runner 进程的系统用户在那台机器上执行任意命令。agent 本身不受目录限制，`--dir` 约束的只是面板能看到什么、线程能选什么。所以：只在你信任 orynd 的机器上运行 runner；用一个权限有限的普通用户运行；`orbd` 以 root 运行时会直接拒绝（`--allow-root` 才放行）；`ORYN_URL` 是非回环的 `http://` 时会告警，请走 HTTPS。
- **反方向不信任：控制面的密钥不去 runner。** orynd 不往 runner 下发 omp 配置、env 密钥、模型 key，不开 egress 代理，也不改那台机器的 git 配置。runner 的 link 只接受转录同步（`entries.cursors` / `entries.append`）、artifact 工具调用（`artifact.call`，runner 线程只看得到自己建过或写过的 artifact）和状态类通知，`thread.create`、`schedule.*` 等其余请求一律回 `runner_unsupported`，所以 runner 也没法借别的线程把密钥绕回来；hello 里的主机名、目录、特性等字段有长度和数量上限，超出的部分截掉。orbd 在 runner 上只写自己目录下的 `~/.oryn/orbd/extensions/oryn-orb.ts`（只注册 artifact 工具）并以 `-e` 加载，不碰 `~/.agents/skills` 与你自己的 omp 扩展。
- **助手不碰 runner 线程。** 助手（协调者）的 send_message、read_thread、归档、定时等工具对 runner 线程一律回 `runner_unsupported`：普通 orb 里产出的内容不能经助手转成你机器上的指令。runner 线程只能由你自己发消息。
- **面板按声明收窄**：Files / Changes 只能看到线程工作目录里面的内容；线程的目录必须落在 `--dir` 声明的目录里（或 Oryn 在它旁边建的 worktree）；终端只有带 `--terminal` 才开。runner 上没有 Portal、受管服务、Desktop、`oryn sync`、项目快照，不能冻结、删除或「连环境 fork」，也不能绑浏览器 Profile。
- runner 令牌（`ort_…`）库里只存哈希。在「设置 → Runner」里删除 runner 即吊销，已连着的 link 会被断开。

### 建立与启动

1. **建 runner**：「设置 → Runner → 新建」，或 `oryn runner create laptop`（标准输出只有令牌，启动命令打到标准错误）。令牌只显示这一次。
2. **在那台机器上**放好与 orynd 同版本的 `orbd`（`make build` 产出的 `bin/orbd`），并确保 `omp` 与 `git` 在 PATH 里。然后：

   ```bash
   install -D -m 600 /dev/stdin ~/.config/oryn/runner.token <<< 'ort_…'   # 或用环境变量 ORYN_RUNNER_TOKEN
   ORYN_URL=https://oryn.example.com orbd run --runner --dir ~/code/app --dir ~/notes [--terminal]
   ```

   `--dir` 可以重复，符号链接会先解析成真实路径；同一个系统用户只能跑一个 runner 实例。会话文件放在 `~/.oryn/threads/<线程>`，不会写进项目目录。
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

## 13. GitHub 集成（M14）

连接方式是一个属于你自己的 GitHub App。设置页「GitHub」按 manifest 一键创建 App（可填组织、可设为公开 App、可在「高级」里改 webhook 地址），完成后把 App 安装到需要的账号或组织，再连接两个 GitHub 用户：

- **我的账号**：交互式线程、首屏新建线程、项目表单里的仓库列表都用它。
- **Agent 账号**（可选）：专门给 agent 用的第二个真实 GitHub 账号，须对仓库有写权限。设置项「自动化任务的 GitHub 身份」选它之后，GitHub 触发规则与项目级定时创建的线程用它提交、推送、开 PR 与评论；agent 在 orb 内派生的子线程沿用父线程的身份。两个槽位不能是同一个账号。

App、client secret、webhook secret 与两个账号的令牌都用控制面主密钥加密后存在数据库里，不出现在「密钥」设置页。

```yaml
# orynd.yaml（以下为默认值；只有测试与验收脚本会改）
github: { api_url: "https://api.github.com", web_url: "https://github.com" }
```

### 仓库绑定与 orb 内凭据

仓库地址是 `https://github.com/<owner>/<repo>` 且 App 已安装到该仓库的项目自动绑定（按仓库 id 跟踪，改名 / 转移后自动更新仓库地址，已有 orb 下次 setup / 唤醒时把 `origin` 改到新地址）。绑定项目的 orb：

- git 不需要 `git/github.com/token` 密钥：出站代理为该 orb 向 GitHub 申请一个只对项目仓库有效、约 50 分钟过期的令牌并注入；同主机其它仓库的 smart-HTTP 请求仍是 403。
- `gh` 免登录：orbd 写 `~/.oryn/gh/`（`hosts.yml` 里只有占位令牌 `oryn-proxied`），`gh` 经 `~/.oryn/gh.sock` 发出的 API 请求由出站代理换上同一个按仓库收窄的令牌。orb 的环境变量与磁盘上没有任何真实 GitHub 令牌。
- 提交作者：Agent 身份的 orb 是 Agent 账号（`<id>+<login>@users.noreply.github.com`）；我的账号身份维持「项目覆盖 > 所有者默认」，两者都没设时用我的 GitHub 账号推出的同格式身份。
- agent 经 `gh` 发往 GitHub 的 JSON 正文（评论、评审、issue、GraphQL `input.body`）末尾会被追加不可见标记 `<!-- oryn-agent -->`，回流时据此识别 agent 自己的发言。

Runner 线程与协调宿主不下发任何 GitHub 凭据。项目未绑定时仍走原来的 `git/<host>/token`。

### 事件：webhook 与轮询

App 的 webhook 默认指向 `<public_url>/api/v1/public/github/webhook`（按 `X-Hub-Signature-256` 校验）。GitHub 访问不到这个地址时不需要额外配置：控制面每 30 秒拉一次 App 的投递日志补齐，同一投递只处理一次，orynd 停机期间的事件在 GitHub 保留投递日志的窗口内重启后补处理。设置页「最近投递」显示来源是「实时」还是「轮询」。`public_url` 是 localhost 或内网地址时 GitHub 可能拒绝创建 App，在「高级」里填一个语法合法的公网地址即可（投递会失败，但会进投递日志，轮询照常取回）。

事件只在发送者对仓库有 `write` / `admin` 权限时驱动 agent；Bot、Agent 账号与带 agent 标记的发言不回流。单个 PR 关联每小时最多回流 20 条（超限暂停该关联），CI 失败自动回流最多 5 轮。

### 权限说明

App 申请 `contents`、`pull_requests`、`issues` 写权限与 `checks`、`statuses`、`actions`、`metadata` 读权限，**不申请 `workflows`**：agent 推送改动了 `.github/workflows/` 的分支会被 GitHub 拒绝。确有需要时在 GitHub 的 App 设置里手动加上并在安装页接受——这等于允许能推送的 agent 改 CI 定义。私有 App 只能装在创建者自己的账号下；要装到其它组织、或 Agent 账号授权失败时，创建时勾选「公开 App」。
