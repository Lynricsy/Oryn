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

## 6. omp 配置

```bash
oryn config push ~/.omp/agent
```

`models.yml` 里的字面 API key 会被移入加密密钥库（`data_dir/master.key`，首次启动生成，务必备份），下发到 orb 的只有占位令牌。

## 7. 安全

orb 里的 agent 有 root（镜像给 `user` 配了免密 sudo），应把 orb 当作不可信代码来隔离。下面几项按需逐一启用。

### orb 令牌

- orb 用令牌 `oot_…` 建立 link。令牌 = `HMAC(orb-token.key, orbID ":" 代数)`，**每次入队 spawn（创建、唤醒、从冻结停机前的解冻）都会轮换**，旧令牌立即失效，现有 link 同时被关闭。
- 控制面只在 orb 处于 `spawning / connecting / setting_up / resuming / ready / waking / stopping` 时接受 link；`frozen / stopped / failed / deleting / deleted` 一律拒绝。唤醒只能由 provider 执行 spawn 驱动，泄露的令牌无法把一台冻结或停机的 orb 拉起来，也活不过它的下一次唤醒。
- 新令牌的下发：新建容器经环境变量 `ORYN_ORB_TOKEN`；已存在的机器写入 `$HOME/.oryn/orb-token`（orbd 每次拨号前优先读它）并向 orbd 发 `SIGHUP` 让它立即重连。docker 驱动对 unpause / start / 已在运行的容器执行 `docker exec -i <orb> sh -c 'umask 077; … cat > $HOME/.oryn/orb-token'`（令牌经 stdin，不进 argv 与任务输出），再 `docker exec <orb> pkill -HUP -x orbd`；从快照新建容器时，镜像里带着源 orb 的令牌文件，驱动在 `docker start` 之前用 `docker cp` 把它换成新 orb 的令牌。

### dispatch 驱动的脚本契约（令牌轮换部分）

`ORB_OPERATION=spawn` 时脚本必须保证机器内的 orbd 用本次的 `ORYN_ORB_TOKEN` 连接：

1. 机器不存在：创建并以分离方式启动 `orbd run`，环境里带 `ORYN_URL`、`ORYN_ORB_ID`、`ORYN_ORB_TOKEN`。若机器来自快照，启动 orbd **之前**把 `$HOME/.oryn/orb-token` 覆盖为本次令牌（或删除它），否则 orbd 会读到源 orb 的令牌。
2. 机器已存在（解冻、开机或已在运行）：恢复运行后，以 orbd 的用户原子写入 `$HOME/.oryn/orb-token`（内容 = `ORYN_ORB_TOKEN`，权限 0600，先写临时文件再 `mv`），然后向 orbd 发 `SIGHUP`。
3. 脚本退出码 0 = 成功。脚本在独立进程组中运行，任务超时（5 分钟）时整组被 `SIGKILL`；脚本退出后仍有后代进程占着 stdout / stderr 时，provider 至多再等 5 秒就返回（退出码 0 仍算成功），所以后台启动的 orbd 应把输出重定向到日志文件。

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
